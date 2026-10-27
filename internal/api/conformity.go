package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/netip"

	"meshcraft/internal/driver"
	"meshcraft/internal/model"
)

// Management-plan conformity for a device on its site
// (see docs/en/01-Architecture/Management addressing.md).
//
// This is a DISTINCT axis from reachability, and conflating them would be a
// display mistake: a device can respond perfectly to ping while being off-plan.
// It is not down — it needs migration. Hence a warning state (orange), not an
// error (red).
//
// Conformity is CALCULATED on read, never stored: the site plan can change at any
// moment, and a boolean frozen in the database would lie from the first plan
// change.

// MgmtConformity says whether a device is in the plan, and if not, why.
type MgmtConformity struct {
	// Checked: does the site have a plan to compare against?
	Checked  bool   `json:"checked"`
	Conforms bool   `json:"conforms"`
	Reason   string `json:"reason,omitempty"`
	// Target summarizes what the plan requires, for display without a second call.
	TargetVlan   int    `json:"target_vlan,omitempty"`
	TargetTagged bool   `json:"target_tagged,omitempty"`
	TargetSubnet string `json:"target_subnet,omitempty"`
	TargetSource string `json:"target_source,omitempty"`
	// Migratable: can migration be proposed? An unreachable device is not migrated —
	// we do not switch over what we cannot verify afterwards.
	Migratable bool `json:"migratable"`
	// PathState says whether there is, RIGHT NOW, a practicable path for the target
	// management VLAN: "ready", "absent", or "unknown".
	//
	// This is the question reachability does not ask. A device can respond perfectly
	// on its current VLAN and have no uplink carrying the target VLAN: switching
	// management into that VLAN would silence it immediately, and this product line
	// has neither SSH nor a serial port.
	PathState  string `json:"path_state,omitempty"`
	PathReason string `json:"path_reason,omitempty"`
}

// HostnameConformity says whether the device answers to the name the inventory
// carries — and when it does not, whether anything is going to fix that.
//
// A SEPARATE AXIS from MgmtConformity, and calculated on read for the same reason:
// the intent is `devices.name`, which changes whenever an operator renames, and a
// verdict frozen in a column would lie from the first rename. The observation is
// free (cf. hostnameheal.go): `lldp_self.sys_name` arrives on every poll cycle, so
// this costs one query per site and no device session at all.
//
// FIVE STATES, AND THE UI MUST TELL THEM APART. Collapsing any two of them was the
// design mistake to avoid, and one pair in particular: `unsupported` and `drift`.
// The first says the connector has no proven system-name write, so this device can
// NEVER converge; the second says meshcraft is about to repair it. A screen showing
// the second for the first would promise a repair that never comes, which is
// exactly what internal/driver/hostname.go's last paragraph forbids.
//
// The decision itself lives in hostnameConformityOf (hostnameheal.go), next to the
// loop that acts on it: this is the DTO, and the two must not be able to disagree
// about what `drift` means.
type HostnameConformity struct {
	// Desired: normalized devices.name — what would be pushed. EMPTY when the
	// meshcraft name cannot be a hostname at all (State == "unnameable"), because
	// showing a name meshcraft would refuse to send is worse than showing none.
	Desired string `json:"desired"`
	// Observed: what the device announces about itself, from LLDP or from the last
	// read-state identity. Absent when neither source has reported.
	Observed string `json:"observed,omitempty"`
	// Source: "lldp" (refreshed every poll cycle) or "read_state" (as old as the
	// last read of this device). Travels so a surprising verdict can be traced to
	// the reading that produced it.
	Source string `json:"source,omitempty"`
	// State: exactly one of "ok", "drift", "not_observed", "unsupported",
	// "unnameable" — cf. the constants in hostnameheal.go, which document each.
	State string `json:"state"`
	// Because: the operator-facing sentence. Empty only for a plain "ok", where
	// there is nothing to explain.
	Because string `json:"because,omitempty"`
}

// outOfBandOf says whether this device's platform keeps an OUT-OF-BAND
// administration path — serial console, MAC-winbox — through which we recover a
// device rendered silent by an addressing mistake.
//
// PURE AND DATABASE-FREE, deliberately: it is called on LISTS of two hundred
// devices (cf. withConformity), and going back through the discovery candidate
// would cost one query per row. `devices.extra.driver` is set at adoption and
// suffices.
//
// FALSE when unknown — connector missing from `extra`, or unknown to this
// binary. This is the project rule applied where it costs the most: erring this
// way shows too harsh a warning; erring the other way attempts an irreversible
// maneuver on a device we believed recoverable.
func outOfBandOf(d model.Device) bool {
	var drv, known = driver.Get(driver.DriverNameFromExtra(d.Extra))
	if !known {
		return false
	}
	return drv.Capabilities(d.Model).Mgmt.OutOfBand
}

// mgmtPathOf looks for an uplink that ALREADY carries the target management VLAN.
//
// The criterion is deliberately strict: a port that carries the target VLAN AND
// an active link. Both matter. A VLAN configured on a port with no cable proves
// no path, and that is precisely the mistake we seek to prevent — "the VLAN is on
// the switch" has never meant "it egresses there".
//
// Three outcomes, and indeterminate is a REAL one: a device never read back, or
// read back before meshcraft stored link state, does not allow a conclusion.
// Answering "absent" in that case would turn ignorance into a prohibition, and
// answering "ready" would cause exactly the damage we want to avoid. We say we do
// not know, and invite a read-back.
//
// `outOfBand` DOES NOT CHANGE THE VERDICT, it changes the PHRASE — and that was a
// lie to fix. This refusal claimed "this product line has neither SSH nor serial
// port: the only recourse would be a factory reset on site". True of the two
// web-only lines shipped, FALSE of RouterOS, which keeps a serial console AND
// MAC-winbox (both observed). An entire refusal was calibrated on an unrecoverable
// platform and recited verbatim to a platform recoverable with a cable. A message
// that exaggerates severity gets learned and ignored, and the day it is true,
// nobody listens anymore.
func mgmtPathOf(d model.Device, plan model.MgmtPlan, outOfBand bool) (string, string) {
	if plan.VlanID <= 0 {
		return "", ""
	}
	var target = itoa(plan.VlanID)
	if d.ObservedAt == nil || len(d.Observed) == 0 {
		return "unknown", "this device has never been read back: meshcraft cannot " +
			"say whether an uplink carries VLAN " + target + ". Read it back before switching."
	}
	var obs observedState
	if json.Unmarshal(d.Observed, &obs) != nil {
		return "unknown", "observed state unreadable: read the device back before switching."
	}
	if obs.LinkUp == nil {
		return "unknown", "the last read predates link-state verification: " +
			"read the device back to learn whether an uplink carries VLAN " + target + "."
	}

	// Tagged or native: the plan says which, and the two are not looked up in the
	// same place. A "native" plan expects the VLAN as PVID; a "tagged" plan expects
	// it in the port's tagged VLAN list.
	var name string
	var p driver.PortState
	for name, p = range obs.Ports {
		if !obs.LinkUp[name] {
			continue
		}
		if plan.Untagged {
			if p.PVID == plan.VlanID {
				return "ready", "port " + name + " has an active link and VLAN " +
					target + " as native."
			}
			continue
		}
		var v int
		for _, v = range p.Tagged {
			if v == plan.VlanID {
				return "ready", "port " + name + " has an active link and VLAN " +
					target + " tagged."
			}
		}
	}

	var how = "tagged"
	if plan.Untagged {
		how = "as native"
	}
	return "absent", "NO port with an active link carries VLAN " + target + " " +
		how + ". Switching management into this VLAN now would make the device " +
		"unreachable over the network, " + recoveryClause(outOfBand) +
		" Configure the management uplink first, then read the device back."
}

// mgmtVLANPathOf checks whether ANY up port already carries vlanID — as access
// native (PVID) OR as tagged trunk membership.
//
// Used when the operator picks a management VLAN in the UI (not only the site
// plan). Plan conformity still uses [mgmtPathOf] with the plan's tagged/native
// preference; this check is the union the operator asked for before a VLAN move.
func mgmtVLANPathOf(d model.Device, vlanID int, outOfBand bool) (string, string) {
	if vlanID <= 0 {
		return "", ""
	}
	var target = itoa(vlanID)
	if d.ObservedAt == nil || len(d.Observed) == 0 {
		return "unknown", "this device has never been read back: meshcraft cannot " +
			"say whether an uplink carries VLAN " + target + ". Read it back before switching."
	}
	var obs observedState
	if json.Unmarshal(d.Observed, &obs) != nil {
		return "unknown", "observed state unreadable: read the device back before switching."
	}
	if obs.LinkUp == nil {
		return "unknown", "the last read predates link-state verification: " +
			"read the device back to learn whether an uplink carries VLAN " + target + "."
	}

	var name string
	var p driver.PortState
	for name, p = range obs.Ports {
		if !obs.LinkUp[name] {
			continue
		}
		if p.PVID == vlanID {
			return "ready", "port " + name + " has an active link and VLAN " +
				target + " as native (access)."
		}
		var v int
		for _, v = range p.Tagged {
			if v == vlanID {
				return "ready", "port " + name + " has an active link and VLAN " +
					target + " tagged (trunk)."
			}
		}
	}

	return "absent", "NO port with an active link carries VLAN " + target +
		" as access (native) or trunk (tagged). Switching management into this VLAN " +
		"now would make the device unreachable over the network, " +
		recoveryClause(outOfBand) +
		" Put VLAN " + target + " on an up trunk or access port first, then read the device back."
}

// resolveMgmtTargetVLAN picks the VLAN a migrate/jump should target.
// Body vlan_id wins when > 0; otherwise the site plan; otherwise error text.
func (s *Server) resolveMgmtTargetVLAN(ctx context.Context, d model.Device,
	bodyVLAN int) (int, model.MgmtPlan, string) {
	var plan, err = s.Store.GetMgmtPlan(ctx, d.SiteID)
	if err != nil {
		return 0, plan, err.Error()
	}
	if bodyVLAN > 0 {
		return bodyVLAN, plan, ""
	}
	if plan.Configured && plan.VlanID > 0 {
		return plan.VlanID, plan, ""
	}
	return 0, plan, "no management VLAN selected and this site has no usable management plan"
}

// recoveryClause says WHAT IT COSTS to get it wrong, on this platform.
//
// Separate and pure because this is vocabulary, and vocabulary is half the
// safeguard here: the two phrases do not lead to the same action. On a platform
// with no fallback path, repair is a move WITH loss of all configuration; with a
// fallback path, it is a move — or a colleague on site with a cable — and
// configuration stays intact. Presenting them the same would either give up
// needlessly or attempt blindly.
func recoveryClause(outOfBand bool) string {
	if outOfBand {
		return "and you would then recover it through its OUT OF BAND fallback " +
			"(serial console, MAC-winbox depending on platform): the mistake is " +
			"recoverable on site without wiping configuration, which makes it less " +
			"costly — not acceptable."
	}
	return "and meshcraft knows NO out-of-band fallback on this platform — no SSH, " +
		"no Telnet, no serial port: the only recourse would be a factory reset at the " +
		"button, on site, with loss of all configuration."
}

// conformityOf compares a device to its site's plan.
func (s *Server) conformityOf(ctx context.Context, d model.Device) MgmtConformity {
	var plan, err = s.Store.GetMgmtPlan(ctx, d.SiteID)
	if err != nil {
		return MgmtConformity{}
	}
	return conformityWith(d, plan)
}

// conformityWith is the decision itself, without database access: the plan is the
// same for an entire site and is read once, not once per device.
func conformityWith(d model.Device, plan model.MgmtPlan) MgmtConformity {
	var c MgmtConformity
	if !plan.Configured {
		// No plan: nothing to compare, therefore nothing to fault.
		return c
	}
	c.Checked = true
	// Management VLAN path does not depend on subnet or current address: it is
	// computed as soon as a plan exists. It also applies to a device ALREADY
	// conforming — an uplink can unplug later, and the question "where does
	// management egress?" remains relevant.
	c.PathState, c.PathReason = mgmtPathOf(d, plan, outOfBandOf(d))
	c.TargetVlan, c.TargetTagged = plan.VlanID, !plan.Untagged
	c.TargetSubnet, c.TargetSource = plan.SubnetV4, plan.Source
	c.Conforms = true

	if plan.SubnetV4 == "" {
		// A plan without subnet says nothing about the address: we cannot judge,
		// and we do not pretend otherwise.
		c.Checked = false
		return c
	}
	var prefix, prefixErr = netip.ParsePrefix(plan.SubnetV4)
	if prefixErr != nil {
		c.Checked = false
		return c
	}
	var addr, addrErr = netip.ParseAddr(d.MgmtIP)
	if addrErr != nil {
		c.Conforms = false
		c.Reason = "no known administration address"
		return c
	}
	if !prefix.Contains(addr) {
		c.Conforms = false
		c.Reason = "address " + d.MgmtIP + " outside the site's management subnet (" +
			plan.SubnetV4 + ")"
		// Migration requires being able to verify afterwards: the device must
		// respond now, AND the target VLAN must have a path out.
		//
		// An INDETERMINATE path does not block: that would turn ignorance into a
		// prohibition on a fleet where many devices have not yet been read back. An
		// ABSENT path, however, is certainty — and that one costs an on-site visit.
		c.Migratable = d.Status == "ok" && c.PathState != "absent"
		return c
	}
	// The address is in the plan. Management VLAN is not yet readable without
	// connecting to the device: we do not claim it conforming, we say it is not
	// verified.
	c.Reason = "management VLAN not verified (requires a device read-back)"
	return c
}

// handleSetDeviceAddressDomain declares the network in which this device's
// address makes sense.
//
// Reserved to `site_admin`: the domain decides which proxy is legitimate to
// monitor this address, so getting it wrong cuts supervision — not the same order
// as labeling a port.
//
// Empty string accepted: it removes the declaration. A wrong domain is worse than
// no domain at all, so we must be able to revert.
func (s *Server) handleSetDeviceAddressDomain(w http.ResponseWriter, r *http.Request) {
	var d, ok = s.deviceForWrite(w, r, "site_admin")
	if !ok {
		return
	}
	var in struct {
		AddressDomainID string `json:"address_domain_id"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.AddressDomainID != "" {
		// An unknown ID would land in the database as an unreadable foreign-key
		// violation: refuse with a message that makes sense.
		var _, domErr = s.Store.GetAddressDomain(r.Context(), in.AddressDomainID)
		if domErr != nil {
			respondErr(w, http.StatusUnprocessableEntity, "unknown address domain")
			return
		}
	}
	var fresh, err = s.Store.SetDeviceAddressDomain(r.Context(), d.ID, in.AddressDomainID)
	if err != nil {
		fail(w, err)
		return
	}
	s.emitDeviceEvent(r, d, "device.address_domain", model.SevNotice,
		domainChangeMessage(d.Name, in.AddressDomainID))
	respond(w, http.StatusOK, s.withSync(r.Context(), fresh))
}

func domainChangeMessage(deviceName, domainID string) string {
	if domainID == "" {
		return "Address domain removed from \"" + deviceName + "\"."
	}
	return "Address domain declared on \"" + deviceName + "\"."
}

// deviceView is a device enriched with what the UI needs without a second call.
type deviceView struct {
	model.Device
	Mgmt MgmtConformity `json:"mgmt"`
	// MgmtAddressing: the EFFECTIVE addressing mode and where it comes from (migration 0027).
	//
	// Separate from `Mgmt`, which is plan conformity, because these are two distinct
	// axes and merging them would make them inseparable in the UI: a device on a
	// static address in the right subnet is perfectly CONFORMING to the plan — that
	// is not a drift, it is a mode. Conflating them would show an orange warning on
	// the three MikroTik devices in the reference installation, which have nothing to
	// fault.
	MgmtAddressing MgmtAddressingView `json:"mgmt_addressing"`
	// Hostname: does the equipment answer to the name the inventory carries, and
	// if not, is a repair coming? Cf. HostnameConformity — five states, none of
	// which may be collapsed into another.
	//
	// TAGGED `hostname_conformity` AND NOT `hostname`, unlike the `mgmt` field
	// above whose short name it was first modelled on. The reason is a collision
	// of MEANING rather than of keys: three other payloads in this API already
	// carry `hostname` as a plain STRING — the discovery candidate, the parsed
	// configuration, the identity — so a `hostname` that deserialized into an
	// object would be the one field whose type nobody predicts from its name.
	// `mgmt` has no such string twin anywhere, which is why it can afford to be
	// terse. Guessing wrong on this key fails SILENTLY on the frontend — a row
	// that never renders, on a screen where "nothing shown" is a legitimate
	// state — so it is worth the extra word.
	Hostname HostnameConformity `json:"hostname_conformity"`
	// STP: last spanning-tree observation (Extra.stp_observed), for topology view.
	STP *driver.SpanningTreeState `json:"stp,omitempty"`
	// STPCaps: per-port spanning-tree edge / enable capabilities from the connector.
	STPCaps STPCapsView `json:"stp_caps"`
	// STPRegion: how the MST region this device CARRIES compares with the one its
	// site WANTS — nil when there is nothing to say (cf. stpRegionViewOf).
	//
	// IT TRAVELS ON THIS PAYLOAD AND NOT ON A ROUTE OF ITS OWN, which ADR-0023
	// argues at length and withConformity's own comment explains: there is no
	// `GET /api/devices/{id}`, so a device record is published here or nowhere,
	// and a per-device comparison endpoint would leave every fleet-wide view
	// unable to say who is out of the tree. It costs no device session — the site
	// plan is read once per site, like the addressing plan beside it.
	STPRegion *STPRegionView `json:"stp_region,omitempty"`
	// STPSuperRoot: the operator's SUPER ROOT intent for this device
	// (Extra.stp_super_root) — "this switch is to win the root election of its
	// MST region and keep winning it".
	//
	// AN INTENT AND NOT AN OBSERVATION, unlike `STP` right above it, and the
	// distinction is load-bearing on screen: the checkbox must render what was
	// ASKED FOR, never what the tree currently looks like. Inferring it from
	// `stp.bridge_priority == 0` would tick the box on a switch somebody set to 0
	// by hand years ago, and un-ticking it would then offer to "restore" a value
	// meshcraft never chose (cf. store.SaveSTPSuperRoot).
	STPSuperRoot bool `json:"stp_super_root"`
	// PoE: last PoE observation (Extra.poe_observed) — per-port scope, configured
	// class, measured snapshot. For the device sheet's PoE panel.
	PoE *driver.PoEState `json:"poe_state,omitempty"`
	// PoECaps: whether this device's CONNECTOR can write PoE, so the UI offers
	// an immediate on/off only where a click will actually reach the hardware.
	PoECaps PoECapsView `json:"poe_caps"`
	// TrafficClassCaps: which traffic classes this platform can COUNT per port,
	// so the port overlay's stacked graph says "not reported here" instead of
	// quietly dropping a band (ADR-0028).
	TrafficClassCaps TrafficClassCapsView `json:"traffic_class_caps"`
	// Optics: last transceiver-diagnostics observation (Extra.optics_observed)
	// — module identity and DDM readings per occupied cage. Same shelf and
	// same cadence as PoE; no caps counterpart because optics has no write
	// path (cf. internal/api/optics.go).
	Optics *driver.OpticsState `json:"optics_state,omitempty"`
	// DHCPSnoop: last DHCP-snooping observation (Extra.dhcp_snooping_observed)
	// — the device-wide enable, the VLANs the feature inspects, and the
	// per-port trust flags.
	//
	// ALL THREE LEVELS TRAVEL TOGETHER, and that is the point (cf.
	// internal/driver/dhcpsnoop.go): a panel holding only the port flags would
	// show "trusted" on a device whose global snooping is off, which is an
	// exact configuration protecting nothing. The UI reads `enabled` next to
	// every trust toggle for exactly that reason.
	DHCPSnoop *driver.DHCPSnoopState `json:"dhcp_snoop_state,omitempty"`
	// PortCaps: which of the two per-port ACTIONS this connector can actually
	// perform — shut/un-shut, and PoE power cycle.
	//
	// Separate from PoECaps even though the cycler rides on the PoE write path:
	// a connector can know how to SET a class without being able to promise the
	// restore half of a cycle, and half a power cycle is an outage
	// (cf. driver.PortPowerCycler). The UI must be able to offer one button
	// without the other.
	PortCaps PortActionCapsView `json:"port_caps"`
	// LAGCaps: what this device's platform can do with AGGREGATES — write them
	// at all, and (separately) honour an LACP fallback VLAN.
	//
	// The faceplate reads this to decide what to DRAW, which is why the two
	// are separate flags: an aggregate block with a dashed "+" appears only
	// where a write would reach the device, and the fallback field inside the
	// builder appears only where the connector declares the gesture — ABSENT,
	// not disabled, on the platforms that cannot (ADR-0007, UI-GUIDE §2: do
	// not paint a platform limit in orange).
	LAGCaps LAGCapsView `json:"lag_caps"`
}

// LAGCapsView says what this device's PLATFORM can do with link aggregation.
// Same shape of answer as PortActionCapsView, and the same Driver-versus-
// Connector caveat: a range whose fingerprint is known but which meshcraft
// cannot reach is a Driver only, and the honest answer for it is "known, not
// writable".
type LAGCapsView struct {
	// Known: the connector was resolved at all. False means "we cannot tell",
	// which is not "the platform says no".
	Known bool `json:"known"`
	// Write: this platform aggregates AND this connector has a proven write.
	// Both halves are required — Capabilities.Features["LACP"] says the
	// hardware does it, the lags gesture says this code can ask it to.
	Write bool `json:"write"`
	// Fallback: the connector declares GestureLAGFallback — LAGState.Fallback
	// will be honoured.
	//
	// THREE STATES, NOT TWO (ADR-0007): fallback set, fallback off, and
	// fallback not supported. This flag separates the third from the other
	// two, and the screen must render it as ABSENCE of the control. A
	// disabled checkbox with no explanation reads as "off", which would tell
	// an operator a port falls back when nothing will.
	Fallback bool `json:"fallback"`
	// MaxLAGs and PortsPerLAG: the platform bounds the write path enforces
	// anyway (handleApplyDeviceLAGs). Published so the builder can refuse
	// BEFORE spending a session on a list that cannot land. 0 means "no bound
	// declared", never "zero allowed".
	MaxLAGs     int `json:"max_lags,omitempty"`
	PortsPerLAG int `json:"ports_per_lag,omitempty"`
}

// lagCapsOf resolves the aggregate capabilities from the connector.
func lagCapsOf(d model.Device) LAGCapsView {
	var name = driver.DriverNameFromExtra(d.Extra)
	var drv, ok = driver.Get(name)
	if !ok {
		return LAGCapsView{}
	}
	var conn, isConn = drv.(driver.Connector)
	if !isConn {
		return LAGCapsView{Known: true}
	}
	var caps = conn.Capabilities(d.Model)
	return LAGCapsView{
		Known:       true,
		Write:       caps.Features["LACP"] && driver.Has(conn, driver.GestureLAGs),
		Fallback:    driver.Has(conn, driver.GestureLAGFallback),
		MaxLAGs:     caps.MaxLAGs,
		PortsPerLAG: caps.PortsPerLAG,
	}
}

// PortActionCapsView says which per-port actions this device's PLATFORM can
// carry, so the double-click overlay offers only buttons that will reach the
// hardware. Same shape of answer as STPCapsView and PoECapsView.
//
// There is no `read` field here for the same reason PoECapsView has none: these
// are ACTIONS, and what gates them is writability, known before any observation
// exists.
type PortActionCapsView struct {
	// Known: the connector was resolved at all. False means "we cannot tell",
	// which is not "the platform says no".
	Known bool `json:"known"`
	// Admin: the connector declares WritesExtra("port_admin") — shut and
	// un-shut will reach the device.
	Admin bool `json:"admin"`
	// PowerCycle: the connector declares WritesExtra("port_power_cycle") — it can
	// cut PoE and promise to put the SAME class back.
	//
	// NOT keyed on "poe", and that was a real defect while it was: EdgeSwitch,
	// Instant On and RouterOS all declare "poe" and only EdgeSwitch implements
	// driver.PortPowerCycler, so this flag offered the button on two platforms
	// that answer it with a 422. Setting a class and promising to restore it are
	// two capabilities (cf. driver/portadmin.go).
	//
	// A NECESSARY but not sufficient condition even so — the port must also be
	// PoE-capable, which is a PER-PORT fact the UI reads from `poe_state` (a mixed
	// unit like the EdgePoint S16 has jacks with no PSE hardware at all). This
	// flag answers only "could any port on this box be cycled".
	PowerCycle bool `json:"power_cycle"`
}

// portActionCapsOf resolves the two action capabilities from the connector,
// with the same Driver-versus-Connector caveat as poeCapsOf: a range whose
// fingerprint is known but which meshcraft cannot reach is a Driver only, and
// the honest answer for it is "known, not writable".
func portActionCapsOf(d model.Device) PortActionCapsView {
	var name = driver.DriverNameFromExtra(d.Extra)
	var drv, ok = driver.Get(name)
	if !ok {
		return PortActionCapsView{}
	}
	var conn, isConn = drv.(driver.Connector)
	if !isConn {
		return PortActionCapsView{Known: true}
	}
	return PortActionCapsView{
		Known:      true,
		Admin:      conn.WritesExtra("port_admin"),
		PowerCycle: conn.WritesExtra("port_power_cycle"),
	}
}

// TrafficClassCapsView says which traffic classes this device's PLATFORM can
// COUNT per port, so the port overlay's stacked graph can tell a band that is
// missing from a band that is zero (ADR-0028).
//
// THE WHOLE POINT IS THE THIRD STATE, and it is why this cannot be inferred
// from the series. An empty series has two meanings — collection just started,
// or this platform will never produce it — and they call for opposite
// reactions. The samples answer "is there data"; only this view answers "could
// there ever be".
//
// Same shape of answer as STPCapsView and PoECapsView, and no `read` field for
// the same reason: readability here is not proven by an observation, because
// its ABSENCE is exactly the ambiguous case.
type TrafficClassCapsView struct {
	// Known: the connector was resolved at all. False means "we cannot tell",
	// which the overlay must not render as "the platform says no".
	Known bool `json:"known"`
	// Multicast / Broadcast / UnknownUnicast: one flag per class, never one
	// "can decompose". A platform serving the ifXTable per-class counters holds
	// the first two and not the third, which is the entire fleet today — a
	// single flag would therefore promise unknown unicast everywhere it
	// promised broadcast.
	Multicast bool `json:"multicast"`
	Broadcast bool `json:"broadcast"`
	// UnknownUnicast: false on every connector today, and the field exists
	// anyway so the overlay has something to render the sentence "this platform
	// does not report unknown unicast" from. Dropping the band instead would
	// make a two-class stack look like a complete one.
	UnknownUnicast bool `json:"unknown_unicast"`
}

// trafficClassCapsOf resolves the three per-class counting capabilities from the
// connector. A Driver that is not a Connector still answers: counting is a READ
// capability, so unlike poeCapsOf there is nothing here that needs writability.
func trafficClassCapsOf(d model.Device) TrafficClassCapsView {
	var name = driver.DriverNameFromExtra(d.Extra)
	var drv, ok = driver.Get(name)
	if !ok {
		return TrafficClassCapsView{}
	}
	var caps = drv.Capabilities(d.Model)
	return TrafficClassCapsView{
		Known:          true,
		Multicast:      caps.CountsTrafficClass(driver.StormMulticast),
		Broadcast:      caps.CountsTrafficClass(driver.StormBroadcast),
		UnknownUnicast: caps.CountsTrafficClass(driver.StormUnknownUnicast),
	}
}

// STPCapsView says what this device's PLATFORM allows for spanning tree.
// Copied from connector Capabilities so the UI can gate Edge / modes without a
// second capabilities fetch.
type STPCapsView struct {
	Known      bool     `json:"known"`
	Edge       bool     `json:"edge"`
	EdgeAuto   bool     `json:"edge_auto"`
	PortEnable bool     `json:"port_enable"`
	BPDUGuard  bool     `json:"bpdu_guard"`
	Modes      []string `json:"modes,omitempty"`
	// Write: the connector declares WritesExtra("stp") — an Apply will actually
	// reach the device.
	//
	// SEPARATE FROM `Modes`, and it had to become so. The card used to gate its
	// Apply button on `len(modes) > 0`, which comes from Capabilities.STPModes and
	// is populated for every platform whose modes are KNOWN — including arista-eos,
	// whose STP write path has never reached a device and whose WritesExtra("stp")
	// is therefore false. The button was offered and the endpoint answered 422.
	// Knowing which protocols a range runs and being able to configure them are
	// two different facts.
	//
	// Same Driver-versus-Connector caveat as poeCapsOf: a range whose fingerprint
	// is known but which meshcraft cannot reach is a Driver only, and the honest
	// answer for it is "known, not writable".
	Write bool `json:"write"`
	// SuperRoot: the connector IMPLEMENTS SpanningTreePlan.SuperRoot
	// (driver.FeatSTPSuperRoot). Distinct from Write, and the distinction is what
	// keeps the checkbox off cisco/ios — which declares WritesExtra("stp") and
	// deliberately does NOT implement Super Root, so a gate on Write alone put
	// the control on a live Catalyst where un-ticking it would have written a
	// bridge priority through the clear path.
	SuperRoot bool `json:"super_root"`
	// MSTRegion: the connector WRITES SpanningTreePlan.Region — name, revision and
	// the VLAN-to-instance table (driver.FeatMSTRegion).
	//
	// Distinct from Write and from Modes for the reason ADR-0022 records, and the
	// case that forces it is fs-s3400: it declares WritesExtra("stp"), so `Write`
	// is true, and it writes no region at all. A screen that showed the site's
	// intended region beside a device gated on `Write` would have claimed
	// sw01-arena was in it, while the switch carried its own bridge MAC as a
	// region name. `Modes` does not answer it either — that says what the RANGE
	// runs, not what this code emits.
	MSTRegion bool `json:"mst_region"`
}

func stpCapsOf(d model.Device) STPCapsView {
	var name = driver.DriverNameFromExtra(d.Extra)
	var drv, ok = driver.Get(name)
	if !ok {
		return STPCapsView{}
	}
	var caps = drv.Capabilities(d.Model)
	var f = caps.Features
	var write bool
	var conn, isConn = drv.(driver.Connector)
	if isConn {
		write = conn.WritesExtra("stp")
	}
	return STPCapsView{
		Known:      true,
		Edge:       f[driver.FeatSTPEdge],
		EdgeAuto:   f[driver.FeatSTPEdgeAuto],
		PortEnable: f[driver.FeatSTPPortEnable],
		BPDUGuard:  f[driver.FeatBPDUGuard],
		Modes:      append([]string{}, caps.STPModes...),
		Write:      write,
		SuperRoot:  f[driver.FeatSTPSuperRoot],
		MSTRegion:  f[driver.FeatMSTRegion],
	}
}

// PoECapsView says what this device's PLATFORM allows for PoE, copied from the
// connector so the UI can gate the power controls without a second fetch —
// same shape of answer as STPCapsView, one feature over.
//
// There is no `read` field, deliberately: readability is proven by the
// presence of `poe_state` itself (a connector that cannot read never produced
// one), whereas writability must be known BEFORE any observation exists —
// it gates whether the on/off buttons promise an immediate device write or
// only a stored intention.
type PoECapsView struct {
	// Known: the connector was resolved at all. False distinguishes "we cannot
	// tell" from "the platform says no" — the same distinction STPCapsView.Known
	// exists to carry.
	Known bool `json:"known"`
	// Write: the connector declares WritesExtra("poe") — an on/off sent from
	// the port panel will actually be pushed to the device, not merely stored.
	Write bool `json:"write"`
}

func poeCapsOf(d model.Device) PoECapsView {
	var name = driver.DriverNameFromExtra(d.Extra)
	var drv, ok = driver.Get(name)
	if !ok {
		return PoECapsView{}
	}
	// WritesExtra lives on the Connector, not the Driver: a range whose
	// fingerprint is known but that meshcraft cannot reach yet is a Driver
	// only, and for it the honest answer is "known, not writable".
	var write = false
	var conn, isConn = drv.(driver.Connector)
	if isConn {
		write = conn.WritesExtra("poe")
	}
	return PoECapsView{Known: true, Write: write}
}

// poeObservedOf extracts the PoE observation stored under Extra.poe_observed.
// Same contract as stpObservedOf: absent, unreadable, or empty means nil —
// a bare {} must never be shown as "no PoE anywhere on this device".
func poeObservedOf(d model.Device) *driver.PoEState {
	if len(d.Extra) == 0 {
		return nil
	}
	var bag map[string]json.RawMessage
	if json.Unmarshal(d.Extra, &bag) != nil {
		return nil
	}
	var raw, ok = bag["poe_observed"]
	if !ok || len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var st driver.PoEState
	if json.Unmarshal(raw, &st) != nil {
		return nil
	}
	if len(st.Ports) == 0 && len(st.Notes) == 0 && st.BudgetW == 0 {
		return nil
	}
	return &st
}

// withConformity enriches a device list with conformity AND sync state.
//
// The ORDER of the two is binding, which is why they are done here rather than by
// the caller: conformity needs observed state — it says which port carries a link —
// while sync enrichment REMOVES it, to avoid loading a list of two hundred devices.
// Chaining them the other way breaks nothing visible: the management VLAN path
// simply becomes "indeterminate" everywhere, silently.
//
// # THIS IS THE ONLY PLACE THE HOSTNAME VERDICT IS PUBLISHED — read this before
// # adding a single-device GET
//
// There is no `GET /api/devices/{id}` today: api.go registers PATCH and DELETE on
// that path and nothing else, so every consumer of a device record gets it from
// the LIST, and the list is this function. The hostname conformity axis therefore
// travels on one payload and one only.
//
// That is a DEFERRAL, not a gap, exactly as long as the single-device GET stays
// absent. Whoever adds it must carry the verdict on it too. The failure mode if
// they do not is quiet and wrong in the worst direction: a device sheet loaded on
// its own — deep link, refresh, a tab reopened — would show the axis as ABSENT
// rather than as UNKNOWN, and "meshcraft has nothing to say about this name" reads
// on screen as "there is nothing to say about this name".
//
// The cost is already paid and does not need re-deriving per device: the per-site
// reads below (plan, VLANs, LLDP self-rows) are the whole expense, and for ONE
// device it is one query each followed by pure arithmetic with NO device session.
func (s *Server) withConformity(ctx context.Context, devices []model.Device) []deviceView {
	var out = make([]deviceView, 0, len(devices))
	// Plan and VLANs are the same for an entire site: do not re-read them per device.
	// So are the LLDP self-rows, which is what makes the hostname verdict affordable
	// on a list: one query per site, then pure arithmetic, and NO device session.
	var plans = map[string]model.MgmtPlan{}
	var vlans = map[string][]int{}
	var selves = map[string]map[string]model.LLDPSelf{}
	// The MST region the site WANTS, and whether it wants one at all — both read
	// out of one site plan, once per site, exactly like the three caches above.
	// The region is derived from `vlans[siteID]` rather than from a second
	// ListVlans: it is the same list [Server.mstRegionOfSite] would read, and
	// going twice to the database for it on a list of two hundred devices would
	// be the cost that got this verdict refused.
	var regions = map[string]driver.MSTRegion{}
	var wantsMST = map[string]bool{}
	var d model.Device
	for _, d = range devices {
		var plan, seen = plans[d.SiteID]
		if !seen {
			plan, _ = s.Store.GetMgmtPlan(ctx, d.SiteID)
			plans[d.SiteID] = plan
			vlans[d.SiteID] = s.siteVLANIDs(ctx, d.SiteID)
			selves[d.SiteID] = s.lldpSelfBySite(ctx, d.SiteID)
			var sitePlan, planErr = s.Store.GetSitePlan(ctx, d.SiteID)
			if planErr == nil {
				regions[d.SiteID] = driver.SiteMSTRegion(
					stpRegionNameOf(sitePlan, d.SiteID),
					stpRegionRevisionOf(sitePlan), vlans[d.SiteID])
				wantsMST[d.SiteID] = stpPreferenceWantsMST(sitePlan)
			}
			// A plan that cannot be read leaves both zero, which stpRegionViewOf
			// renders as "nothing to say". Not as a drift: a database hiccup must
			// not paint a fleet orange, for the same reason mstRegionOfSite
			// returns no region at all rather than falling through to a derived
			// name (ADR-0021).
		}
		var self, announced = selves[d.SiteID][d.ID]
		var hostObserved, hostSource = hostnameObserved(d, self, announced)
		var stpCaps = stpCapsOf(d)
		var c = conformityWith(d, plan)
		var enriched = withSyncVLANs(d, vlans[d.SiteID])
		// Why no proxy watches this device, if there is a reason.
		// Computed here rather than in withSyncVLANs, which is pure and has no access
		// to proxy state.
		enriched.WatchExcluded = s.WatchExcl.reasonFor(d.ID)
		// Which protocol collects it (cf. model.Device.CollectKind). Read from
		// `d.Extra` directly rather than through deviceIdentity: this loop runs
		// over the whole fleet and that helper reads the discovery row per device.
		enriched.CollectKind = collectKindOf(driver.DriverNameFromExtra(d.Extra), d.Model)
		// Live provisioning state, so the list badge animates while an apply runs.
		enriched.Applying = s.applyingNow(d.ID)
		// Addressing is computed on `d`, not on `enriched`: withSyncVLANs REMOVES
		// observed state to avoid loading a list of two hundred devices, and the
		// "observed and kept" mode comes from observed state.
		// Order already cost a defect on the management VLAN path, just above.
		out = append(out, deviceView{
			Device: enriched, Mgmt: c,
			MgmtAddressing: addressingViewOf(d, plan),
			// On `d` and not `enriched`, same reason as addressing just above: the
			// verdict reads `extra`, and it must read the row as the database holds
			// it rather than whatever enrichment left behind.
			Hostname: hostnameConformityOf(d, hostObserved, hostSource),
			STP:      stpObservedOf(d),
			STPCaps:  stpCaps,
			// On `d` for the same reason as the two above: the observation is read
			// out of `extra`, and withSyncVLANs strips observed state off `enriched`.
			STPRegion: stpRegionViewOf(regions[d.SiteID], wantsMST[d.SiteID],
				stpObservedOf(d), stpCaps.MSTRegion),
			STPSuperRoot:     stpSuperRootOf(d),
			PoE:              poeObservedOf(d),
			PoECaps:          poeCapsOf(d),
			TrafficClassCaps: trafficClassCapsOf(d),
			Optics:           opticsObservedOf(d),
			DHCPSnoop:        dhcpSnoopObservedOf(d),
			PortCaps:         portActionCapsOf(d),
			LAGCaps:          lagCapsOf(d),
		})
	}
	return out
}

// stpSuperRootOf reads the SUPER ROOT intent stored under Extra.stp_super_root.
//
// Shaped exactly like stpObservedOf next to it, and absent means FALSE — an
// unset flag is a device nobody has asked to be root, which is the only reading
// that does not invent a decision. Note that this is the one field on this shelf
// that is NOT an observation (cf. store.SaveSTPSuperRoot).
func stpSuperRootOf(d model.Device) bool {
	if len(d.Extra) == 0 {
		return false
	}
	var bag map[string]json.RawMessage
	if json.Unmarshal(d.Extra, &bag) != nil {
		return false
	}
	var raw, ok = bag["stp_super_root"]
	if !ok || len(raw) == 0 {
		return false
	}
	var on bool
	if json.Unmarshal(raw, &on) != nil {
		return false
	}
	return on
}

// stpObservedOf extracts spanning-tree observation stored under Extra.stp_observed.
func stpObservedOf(d model.Device) *driver.SpanningTreeState {
	if len(d.Extra) == 0 {
		return nil
	}
	var bag map[string]json.RawMessage
	if json.Unmarshal(d.Extra, &bag) != nil {
		return nil
	}
	var raw, ok = bag["stp_observed"]
	if !ok || len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var st driver.SpanningTreeState
	if json.Unmarshal(raw, &st) != nil {
		return nil
	}
	// Empty blob after unmarshal: never treat a bare {} as "STP off".
	if !st.Enabled && st.Mode == "" && len(st.Ports) == 0 && len(st.Notes) == 0 {
		return nil
	}
	return &st
}
