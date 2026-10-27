package routeros

// Shutting a port and bringing it back — `disabled` on `/interface`
// (driver/portadmin.go).
//
// # ONE FIELD, and it is the one RouterOS itself carries
//
// There is no `shutdown` verb on this platform: an interface is administratively
// down when its `/interface` record carries `disabled: true`. That field is CAPTURED
// — testdata/interface.json, read on the reference CRS326, has it on all 29 rows
// with its exact value vocabulary (`"false"`, the string, not a JSON boolean) — and
// the write is the SAME `Set` path this connector already uses everywhere: a POST to
// `/interface/set` carrying the record's `.id` and the field. Nothing new is invented
// here; what is new is the gesture.
//
// The `.id` is READ IMMEDIATELY BEFORE the write and never cached. [Client.Set] says
// why in general terms and it is worse on this path than on any other: RouterOS
// numbers its own records (`*1`, `*1B`) and those numbers do not survive a reboot, so
// a stale identifier does not fail — it disables A DIFFERENT INTERFACE. On a table
// that contains the bridge and the management VLAN interface, that is not a mistake
// with a small radius.
//
// # ONLY `type: ether` MAY BE SHUT, and this is a refusal, not a filter
//
// `/interface` is not a list of ports. The reference device's own capture carries
// `bridge`, `lo` (loopback) and `vlan99` alongits 26 ethernet rows, and every one of
// them accepts `disabled: true` perfectly well:
//
//   - disabling `bridge` stops switching on the WHOLE device — every port, every
//     VLAN, at once;
//   - disabling `vlan99` removes the interface the administration address sits on,
//     which is the session, immediately;
//   - disabling `lo` is meaningless and permanent-looking.
//
// A "shut this port" gesture that reached any of those would be catastrophically
// wider than what the operator asked for, so a non-ethernet interface is REFUSED with
// the reason. That is not conservatism about an unknown: the three names above are in
// the capture.
//
// # The guardrail: RouterOS has THREE ways administration can arrive
//
// Every other connector's guardrail asks one question ("does this port carry the
// management VLAN"). Here that is only one of three, and the other two are not
// theoretical — both are in the captures:
//
//  1. the address sits on a VLAN INTERFACE over the bridge (`vlan99`, `interface:
//     bridge`, `vlan-id: 99` — OBSERVED, and MgmtVLAN is 99). Administration then
//     arrives through whichever BRIDGE PORT is a member of VLAN 99, tagged or
//     untagged;
//  2. the address sits on the BRIDGE itself (`192.168.88.1/24` on `bridge` —
//     OBSERVED, factory configuration). Administration arrives UNTAGGED, through the
//     bridge's PVID; when that PVID cannot be read, ANY bridge port may be the path;
//  3. the address sits DIRECTLY ON A PHYSICAL PORT, or on a VLAN interface whose
//     parent is one. There is no VLAN membership to test at all — the port itself IS
//     administration, and a membership-only guardrail would wave it straight through.
//
// [resolveMgmtPath] derives which of the three applies from what the device said, and
// [portAdminGuardBlocks] answers per port. MgmtVLAN == 0 is NOT ignorance on this
// platform — DeviceState.MgmtVLAN says so explicitly ("that is NOT a read error, and
// MgmtInterface specifies which") — so zero alone must not spare everything. What
// DOES spare everything is having no management interface at all: a device reached
// only over MAC-winbox has no `/ip/address` row, and then nothing can be ruled out.
//
// # Recoverable, and that is not permission
//
// This platform has a serial console, MAC-winbox, and `/system/history` whose entries
// carry the inverse command in plain text — all three OBSERVED, and apply.go leans on
// them when it grades severity. So a mistake here is recoverable, unlike on Instant
// On or SwOS. It is still an outage, still a drive, and the guardrail is the same
// strength: "recoverable" is a reason to stay careful, not a reason to be less so
// (ports.go's planFiltering says it in those words).
//
// # NOT persisted, because there is nothing to persist
//
// RouterOS has no running/startup split: a `set` is the configuration, immediately.
// So unlike cisco/ios or fs/s3400 this connector cannot decline to save — and the
// consequence must be said rather than assumed, since it removes the "a reboot brings
// the port back" recovery those platforms have. [driver.ApplyResult.Notes] names
// `/system/history` instead, which is where the inverse of this write is written down
// by the device itself.
//
// # PoE power cycling is NOT implemented, and here the reason is the MODEL
//
// [WritableExtras] records it from the survey: `/interface/ethernet/poe` DOES NOT
// EXIST on this model — observed, not assumed. There is therefore no class to read
// and none to restore, and [driver.PortPowerCycler] would have nothing to promise. On
// a RouterOS model that does have PoE-out the prerequisite is the same as everywhere
// else: a [driver.PoEReader] first.

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"meshcraft/internal/driver"
)

// Compile-time: this Admin shuts and un-shuts ports.
//
// [driver.PortPowerCycler] is DELIBERATELY ABSENT (cf. the header): this model has no
// PoE menu at all, so there is no class to restore.
var _ driver.PortAdminApplier = (*Client)(nil)

// etherType is the `type` value of a physical ethernet port, as the device prints it
// (CAPTURED: `"type":"ether"` on the 26 faceplate rows; `bridge`, `vlan` and
// `loopback` on the three others).
const etherType = "ether"

// disabledField / boolTrue / boolFalse: the field and the two values, in RouterOS's
// own spelling. STRINGS, not Go booleans: this REST API sends and accepts `"true"` /
// `"false"` as text (cf. Row.Bool, which compares against the string), and a payload
// carrying a JSON boolean is not the shape the rest of this connector produces.
const (
	disabledField = "disabled"
	boolTrue      = "true"
	boolFalse     = "false"
)

// ifaceRow is what `/interface` says about one interface, reduced to what this file
// needs. Kept as a type so the derivation below can be tested on captures.
type ifaceRow struct {
	Name     string
	ID       string
	Type     string
	Disabled bool
	Running  bool
}

// foldInterfaces indexes `/interface` by name.
//
// A row with no `.id` is DROPPED rather than carried: a write without an identifier is
// refused by [Client.Set] anyway, and keeping the row would let this applier report a
// port it cannot possibly touch as "not already in the wanted state".
func foldInterfaces(rows []Row) map[string]ifaceRow {
	var out = map[string]ifaceRow{}
	var r Row
	for _, r = range rows {
		var name = r.Get("name")
		if name == "" || r.ID() == "" {
			continue
		}
		out[name] = ifaceRow{
			Name:     name,
			ID:       r.ID(),
			Type:     r.Get("type"),
			Disabled: r.Bool(disabledField),
			Running:  r.Bool("running"),
		}
	}
	return out
}

// mgmtPath is HOW administration reaches this device, resolved into the terms the
// guardrail can act on.
type mgmtPath struct {
	// Interface is the interface the address sits on, as the device names it.
	Interface string
	// VLAN is the VLAN administration arrives through, or 0.
	VLAN int
	// PhysicalPort is the physical port that IS administration — the address is on it
	// directly, or on a VLAN interface whose parent is it. Empty when administration
	// arrives through the bridge.
	PhysicalPort string
	// AnyBridgePort: administration arrives UNTAGGED through the bridge and the VLAN
	// involved could not be determined, so any bridge port may be carrying it.
	AnyBridgePort bool
	// Unknown: nothing identified. Every LINKED port is spared.
	Unknown bool
	// Why explains Unknown, for the operator.
	Why string
}

// resolveMgmtPath works out which of the three arrival shapes this device has (cf.
// the file header).
//
// PURE, over the state the read already produced plus the `/interface/vlan` rows: this
// is the function that decides whether the port carrying the session may be shut, and
// no write of this connector has ever been exercised on hardware — so it has to be
// testable against the captures instead.
func resolveMgmtPath(st DeviceState, vlanRows []Row, ifaces map[string]ifaceRow) mgmtPath {
	var p = mgmtPath{Interface: st.MgmtInterface, VLAN: st.MgmtVLAN}
	if st.MgmtInterface == "" {
		p.Unknown = true
		p.Why = "no administration address was found on this device: it may be reached only " +
			"over MAC-winbox or the serial console, and meshcraft cannot rule out that any " +
			"given port is carrying the session"
		return p
	}

	// The address on a PHYSICAL port: the port itself is administration.
	var self, isIface = ifaces[st.MgmtInterface]
	if isIface && self.Type == etherType {
		p.PhysicalPort = st.MgmtInterface
		return p
	}

	// A VLAN interface: its parent decides. `interface: bridge` is the captured case
	// (vlan99 over the bridge), and a parent that is a physical port is the one a
	// membership-only guardrail would miss.
	var r Row
	for _, r = range vlanRows {
		if r.Get("name") != st.MgmtInterface {
			continue
		}
		var parent = r.Get("interface")
		var parentIface, known = ifaces[parent]
		if known && parentIface.Type == etherType {
			p.PhysicalPort = parent
		}
		return p
	}

	// The address is on the bridge itself: administration arrives UNTAGGED. When
	// state.go managed to read the bridge PVID it is already in MgmtVLAN and the
	// membership test below covers it; when it did not, any bridge port may be the path.
	if st.MgmtInterface == st.BridgeName && st.MgmtVLAN <= 0 {
		p.AnyBridgePort = true
	}
	return p
}

// portAdminGuardBlocks answers, for ONE port: would disabling it plausibly cut
// administration?
//
// LINK-GATED like every other guardrail in this codebase: a port with no cable cannot
// carry the session (state.go reads `running`, not `disabled`, for exactly this
// question). Force lifts it; re-ENABLING a port is never gated at all.
func portAdminGuardBlocks(name string, port BridgePort, isBridgePort bool,
	path mgmtPath, linked, force bool) (block bool, reason string) {

	if force || !linked {
		return false, ""
	}
	if path.Unknown {
		return true, "it carries an ACTIVE link and " + path.Why
	}
	if path.PhysicalPort == name {
		if path.VLAN > 0 {
			return true, fmt.Sprintf("it carries an ACTIVE link and the administration address "+
				"reaches this device through it (%s, VLAN %d): there is no VLAN membership to "+
				"work around here — this port IS administration", path.Interface, path.VLAN)
		}
		return true, fmt.Sprintf("it carries an ACTIVE link and the administration address sits "+
			"on it (%s): this port IS administration", path.Interface)
	}
	if !isBridgePort {
		// Not a bridge port and not the management interface: nothing switched
		// through it can be the session.
		return false, ""
	}
	if path.AnyBridgePort {
		return true, fmt.Sprintf("it carries an ACTIVE link and administration arrives UNTAGGED "+
			"through bridge %q, whose PVID could not be read — so any bridge port may be the "+
			"one carrying the session", path.Interface)
	}
	if path.VLAN > 0 && portCarriesVLAN(port, path.VLAN) {
		return true, fmt.Sprintf("it carries an ACTIVE link and is a member of the management "+
			"VLAN %d (%s), tagged or untagged — very likely the port this session arrives on",
			path.VLAN, path.Interface)
	}
	return false, ""
}

// portCarriesVLAN: is this VLAN on the port, by any of the three lists RouterOS keeps?
//
// PVID, `untagged` AND `tagged` are all consulted, because on this platform they are
// INDEPENDENT — BridgePort.Untagged's own comment records the reference device
// carrying a port that tags into a VLAN it is not a member of. Reading only the PVID
// would miss the uplink, which is the port that matters most here.
func portCarriesVLAN(port BridgePort, vlan int) bool {
	if vlan <= 0 {
		return false
	}
	if port.PVID == vlan {
		return true
	}
	var v int
	for _, v = range port.Untagged {
		if v == vlan {
			return true
		}
	}
	for _, v = range port.Tagged {
		if v == vlan {
			return true
		}
	}
	return false
}

// ApplyPortAdmin disables and re-enables ports, NOW.
//
// Everything is read first — the switching state, `/interface` for the records and
// their `.id`s, `/interface/vlan` to resolve where administration arrives — and the
// write is one `Set` per port, followed by ONE re-read that decides what actually
// moved.
func (c *Client) ApplyPortAdmin(ctx context.Context,
	plan driver.PortAdminPlan) (ApplyResult, error) {

	var out ApplyResult
	if len(plan.Ports) == 0 {
		return out, nil
	}

	// The RICH read, not the contract one: the guardrail needs the bridge name, the
	// membership lists and where administration arrives, and none of that is in
	// driver.State.
	var current, err = c.ReadDeviceState(ctx)
	if err != nil {
		return out, fmt.Errorf("pre-write read failed: %w — the guardrail cannot tell which "+
			"port carries administration, so nothing is disabled", err)
	}

	var ifRows []Row
	ifRows, err = c.List(ctx, "interface")
	if err != nil {
		return out, fmt.Errorf("reading /interface: %w — without the records' `.id` no write "+
			"can name the right row, and RouterOS renumbers them on every reboot", err)
	}
	var ifaces = foldInterfaces(ifRows)
	if len(ifaces) == 0 {
		return out, fmt.Errorf("/interface returned no usable record: nothing is disabled")
	}

	// `/interface/vlan` failing is NOT fatal: it only means a VLAN interface's parent
	// cannot be resolved, and resolveMgmtPath then leaves PhysicalPort empty — the
	// guardrail falls back to the membership test, which is the conservative side.
	var vlanRows []Row
	vlanRows, err = c.List(ctx, "interface/vlan")
	if err != nil {
		out.Notes = append(out.Notes, "`/interface/vlan` could not be read ("+err.Error()+"): "+
			"if administration arrives through a VLAN interface over a PHYSICAL port, that "+
			"port is protected only by its VLAN membership here")
		vlanRows = nil
	}
	var path = resolveMgmtPath(current, vlanRows, ifaces)
	if path.Unknown {
		out.Notes = append(out.Notes, path.Why+": every port with a LINK is spared unless the "+
			"plan forces it")
	}

	var intents = make([]driver.PortAdminIntent, len(plan.Ports))
	copy(intents, plan.Ports)
	sort.Slice(intents, func(i, j int) bool { return intents[i].Port < intents[j].Port })

	var attempted []driver.PortAdminIntent
	var it driver.PortAdminIntent
	for _, it = range intents {
		var row, known = ifaces[it.Port]
		if !known {
			out.Skipped = append(out.Skipped, fmt.Sprintf(
				"%s not touched: this device has no `/interface` record by that name — on this "+
					"platform a port is named as RouterOS names it (`ether3`, `sfp-sfpplus1`), "+
					"with no translation", it.Port))
			continue
		}
		if row.Type != etherType {
			out.Skipped = append(out.Skipped, fmt.Sprintf(
				"%s not touched: it is a %q interface, not a physical port. Disabling it would "+
					"be far wider than shutting a port — a bridge takes switching down on every "+
					"port at once, and a VLAN interface takes the administration address with it",
				it.Port, row.Type))
			continue
		}

		// THE GUARDRAIL. Disabling only — bringing a port back up cuts nothing.
		if !it.Enabled {
			var port, isBridgePort = current.Ports[it.Port]
			var block, why = portAdminGuardBlocks(it.Port, port, isBridgePort, path,
				current.LinkUp[it.Port], plan.Force)
			if block {
				out.Skipped = append(out.Skipped, fmt.Sprintf(
					"%s NOT disabled: %s. Recovery on this platform is the serial console, "+
						"MAC-winbox, or `/system/history` on site — recoverable, which is a "+
						"reason to stay careful and not a reason to be less so. Force only "+
						"knowing why", it.Port, why))
				continue
			}
		}

		// Already where the plan wants it: nothing is sent, and the port does not
		// appear in PortsWritten (contract). On a platform that persists immediately,
		// a `set` that changes nothing is a risk taken for nothing.
		if row.Disabled == !it.Enabled {
			continue
		}

		var want = boolTrue
		if it.Enabled {
			want = boolFalse
		}
		var _, serr = c.Set(ctx, "interface", row.ID, map[string]string{disabledField: want})
		if serr != nil {
			out.Skipped = append(out.Skipped, fmt.Sprintf("%s not %s: %v", it.Port,
				adminVerb(it.Enabled), serr))
			continue
		}
		attempted = append(attempted, it)
	}

	if len(attempted) == 0 {
		return out, nil
	}

	// The re-read is what says the ports moved — never the HTTP status. It is also
	// cheap here: one GET of the same menu that was written.
	ifRows, err = c.List(ctx, "interface")
	if err != nil {
		out.Unconfirmed("administrative state", driver.PortAdminIntentPorts(attempted),
			err.Error())
	} else {
		var after = foldInterfaces(ifRows)
		for _, it = range attempted {
			var row, known = after[it.Port]
			if known && row.Disabled == !it.Enabled {
				out.PortsWritten = append(out.PortsWritten, it.Port)
				continue
			}
			out.Skipped = append(out.Skipped, fmt.Sprintf(
				"%s: the device accepted the write and `%s` still reads %q — nothing on this "+
					"port changed", it.Port, disabledField, boolOf(row.Disabled)))
		}
	}

	if len(out.PortsWritten) > 0 {
		c.step(true, fmt.Sprintf("%d port(s) administratively %s",
			len(out.PortsWritten), adminVerb(attempted[0].Enabled)),
			strings.Join(out.PortsWritten, ", "))
		out.Notes = append(out.Notes, "RouterOS has no running/startup split: this took effect "+
			"immediately and is already the configuration, so — unlike on the CLI platforms — a "+
			"reboot does NOT bring these ports back. The inverse command is written down by the "+
			"device itself in `/system/history`, which is the way back if the session is lost")
	}
	return out, nil
}

func adminVerb(enabled bool) string {
	if enabled {
		return "enabled"
	}
	return "disabled"
}

func boolOf(b bool) string {
	if b {
		return boolTrue
	}
	return boolFalse
}
