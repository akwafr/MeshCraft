package routeros

import (
	"context"
	"fmt"
	"meshcraft/internal/driver"
	"sort"
	"strconv"
	"strings"
)

// Reading the state ACTUALLY applied on a RouterOS device.
//
// This is the 'Read' step of the reconciliation pipeline
// (docs/en/01-Architecture/Reconciliation.md §2): without it, meshcraft cannot know
// whether what it believes is what is, and 'configuration drift' remains a
// buzzword.
//
// THE VLAN MODEL OF A CRS3xx, because everything in this file follows from it.
//
// This is NOT the model of a classic switch, where a 'VLAN × ports' table would
// carry everything. On RouterOS, VLAN configuration is spread across THREE menus,
// and all three are needed to know what a port does:
//
//	/interface/bridge        the `vlan-filtering` switch. OBSERVED: "true".
//	                         While false, ALL entries below are inert — the bridge
//	                         behaves like a dumb switch.
//	/interface/bridge/port   one record PER PORT, carrying `pvid`.
//	                         OBSERVED: pvid 100 on ether1..ether24, pvid 1 on the
//	                         two SFP+ ports.
//	/interface/bridge/vlan   one record PER VLAN, carrying `tagged` and `untagged`
//	                         lists. OBSERVED: VLAN 99 tagged "bridge,sfp-sfpplus2",
//	                         VLAN 100 tagged "sfp-sfpplus2", VLAN 1 untagged
//	                         "bridge,sfp-sfpplus2".
//
// Two traps the read must make visible instead of smoothing over:
//
//  1. PVID IS NOT MEMBERSHIP. This is the most costly difference from the Instant On
//     line, where a port's PVID IS DERIVED from its untagged membership — that is
//     even what its UI does. Here the two are independent, and the reference device
//     gives the perfect illustration: ether1..ether24 have `pvid=100`, but VLAN 100
//     has an EMPTY `untagged` list. Those ports therefore tag ingress traffic into
//     VLAN 100 without being VLAN 100 members on egress — incomplete configuration,
//     perfectly accepted by the device, and silent.
//     A connector that derived PVID from membership would read 'access on VLAN 1'
//     where the device does something else.
//
//  2. `bridge` IN A LIST IS NOT A PORT, IT IS THE CPU. OBSERVED: VLAN 99, which
//     carries administration, has `tagged: "bridge,sfp-sfpplus2"`. That `bridge` is
//     what lets tagged frames reach administration. Counting it as a physical port
//     would invent a jack; losing it when writing would cut access (cf. apply.go).

// BridgeVLAN is a `/interface/bridge/vlan` entry: a VLAN declared on the device,
// with what is needed to WRITE it.
//
// The contract view is [VLAN] (`driver.VLAN`), which carries only the identifier and
// name. The two extra fields below had no place in the contract — a RouterOS `.id` is
// a platform fact — and could not disappear: without `Dynamic`, apply tries to modify
// an entry the device rebuilds at will; without `RowID`, it has nothing to modify.
type BridgeVLAN struct {
	ID int `json:"id"`
	// Name: RouterOS does not NAME its VLANs — `/interface/bridge/vlan` has no name
	// field. The `comment` stands in for it, as free text (OBSERVED: "added by pvid" on
	// the entry the device created itself). We surface it as a name for lack of better,
	// knowing it is not an identifier.
	Name string `json:"name,omitempty"`
	// Dynamic: was the entry created by the DEVICE rather than an operator. OBSERVED:
	// VLAN 1 is `"dynamic":"true"` with comment 'added by pvid' — RouterOS creates it
	// on its own because ports have that PVID.
	//
	// The distinction is vital for apply: a dynamic entry has no stable `.id`, cannot be
	// modified, and disappears when the reason that created it disappears. Treating it as
	// an ordinary entry would fail writes without the message saying why.
	Dynamic bool `json:"dynamic,omitempty"`
	// Internal RouterOS ID, required to write. Never cached across sessions: these
	// identifiers do not survive a reboot.
	RowID string `json:"-"`
	// IDs carries ALL VLAN identifiers the record declares.
	//
	// THE FIELD THAT PREVENTS ONE WRITE FROM TOUCHING TEN VLANs AT ONCE. `vlan-ids` is
	// not an integer: it is a RouterOS-format list, and the device accepts '10-20,30'
	// in a single record (documented, and the read already expands it — cf. ParseVLANIDs).
	// Yet `tagged` and `untagged` lists are carried by the RECORD, not by each of its
	// VLANs: rewriting 'VLAN 10' membership on a line declaring 10-20 rewrites all
	// ELEVEN, silently, in one `set`.
	//
	// The read split the line into one entry per identifier, correct for DISPLAY — but
	// hid the fact they share a `.id`. Apply therefore could not know it would overflow.
	IDs []int `json:"ids,omitempty"`
	// TaggedMembers and UntaggedMembers are member lists AS THE DEVICE CARRIES THEM,
	// CPU pseudo-port included, in served order.
	//
	// They exist because a write replaces the ENTIRE list, so we must start from the
	// whole list. Apply reconstructed it from the per-PORT view, losing two things: the
	// `bridge` pseudo-port (deliberately excluded from the per-port view, re-injected only
	// on the TAGGED side — an untagged `bridge` was therefore lost, as on VLAN 1 on the
	// reference device), and any member that is not a bridge port in the read table.
	TaggedMembers   []string `json:"-"`
	UntaggedMembers []string `json:"-"`
}

// CPUIsMember reports whether the CPU pseudo-port appears in this entry's member
// list, tagged or untagged.
//
// This is the question that decides whether administration can reach the CPU via this
// VLAN, and it applies on both sides: `bridge` tagged when administration arrives
// tagged (OBSERVED on VLAN 99 of the reference device), `bridge` UNTAGGED when it
// arrives without a tag (OBSERVED on its VLAN 1, `untagged: "bridge,sfp-sfpplus2"` —
// factory configuration).
func (v BridgeVLAN) CPUIsMember(bridgeName string, tagged bool) bool {
	var list = v.UntaggedMembers
	if tagged {
		list = v.TaggedMembers
	}
	var m string
	for _, m = range list {
		if m == bridgeName {
			return true
		}
	}
	return false
}

// BridgePort is a port's VLAN configuration as the device carries it.
//
// Deliberately limited to what is CONFIGURED: throughput, link, and counters are
// telemetry, and comparing them would make the entire fleet blink constantly.
//
// The contract view is [PortState] (`driver.PortState`), carrying mode, PVID, and the
// tagged list. What remains here is what RouterOS needs to write correctly — the
// record `.id`, owning bridge — and `Untagged`, the only way to see that a port tags
// into a VLAN it is NOT a member of. The reference device actually carries that
// anomaly; the contract cannot express it, so [State.Notes] SAYS it.
type BridgePort struct {
	Mode string `json:"mode"` // access | trunk | free
	PVID int    `json:"pvid,omitempty"`
	// Tagged is the list of VLANs the port carries TAGGED, sorted.
	Tagged []int `json:"tagged,omitempty"`
	// TaggedAtLeast: `Tagged` is a FLOOR to reach, not an exact set. Set only on
	// INTENT, never on a read state — a device carries a list, not a floor. Same
	// semantics as the Instant On connector, so apply behaves the same across the fleet.
	TaggedAtLeast bool `json:"tagged_at_least,omitempty"`
	// Untagged is the list of VLANs the port is an UNTAGGED member of.
	//
	// Almost always a single element, then identical to PVID — but the two are
	// independent on this platform (cf. header, trap #1), and the read must show when
	// they diverge. This field makes it possible to say 'this port tags into 100 but
	// is a member of nothing', the exact anomaly the reference device carries.
	Untagged []int `json:"untagged,omitempty"`
	// RowID is the `/interface/bridge/port` record identifier.
	RowID string `json:"-"`
	// Bridge is the bridge the port belongs to. A device may have several; writing a
	// VLAN on the wrong one would have no visible effect.
	Bridge string `json:"-"`
	// HWOffload: is this port's switching in the chip. OBSERVED "true" on all 26
	// ports. Warns rather than decides: configuration that drops it divides throughput
	// by ten without signaling anything.
	HWOffload bool `json:"hw_offload,omitempty"`
	// FrameTypes and IngressFiltering: THE OTHER HALF OF FILTERING, which the first
	// delivery neither read nor wrote.
	//
	// OBSERVED as `admit-all` and `false` on all 26 ports of the reference device, and
	// both values leave filtering PERMEABLE:
	//
	//   - `ingress-filtering: false` means the bridge table is consulted ONLY ON EGRESS.
	//     A frame tagged for a VLAN the port is not a member of enters, is learned, and
	//     leaves toward that VLAN's members: segmentation is one-way only;
	//   - `frame-types: admit-all` means an access port accepts TAGGED frames from a
	//     neighbor. A host that tags its own traffic leaves the VLAN we thought we gave it.
	//
	// Neither appears in the contract view — [PortState] carries only mode, PVID, and the
	// tagged list — and [State.Notes] tells the operator what they need to know (cf.
	// DeviceState.notes). Apply writes them under safeguards (cf. planFiltering).
	FrameTypes       string `json:"frame_types,omitempty"`
	IngressFiltering bool   `json:"ingress_filtering,omitempty"`
}

// DeviceState is the observed state of a device, AS ROUTEROS CARRIES IT.
//
// The contract view is [State] (`driver.State`), obtained via [DeviceState.Standard].
// Five fields of this type do not enter it, and it matters which, because they are
// exactly what apply cannot do without: `BridgeName`, `VLANFiltering`, `MgmtInterface`,
// `MgmtAddress`, and `CPUTagged`. Menu names and platform facts — precisely what the
// contract says it does not want to carry. What the OPERATOR must still know goes
// through [State.Notes].
type DeviceState struct {
	VLANs []BridgeVLAN          `json:"vlans"`
	Ports map[string]BridgePort `json:"ports"`
	// MgmtVLAN is the VLAN through which the device says it is administrable, as IT
	// declares — not as the site plan wishes.
	//
	// The distinction is vital, not cosmetic: a device not yet migrated to the plan is
	// reachable via ITS OWN VLAN. Protecting the plan's would protect a VLAN the device
	// does not use, while leaving the one carrying the current session open to rewrite.
	//
	// 0 means 'administration does not arrive via a tagged VLAN': either the address
	// is on the bridge itself, or on a physical interface. That is NOT a read error, and
	// MgmtInterface specifies which.
	MgmtVLAN int `json:"mgmt_vlan"`
	// MgmtInterface is the interface carrying the administration address, as the device
	// names it (OBSERVED: `vlan99`).
	MgmtInterface string `json:"mgmt_interface,omitempty"`
	// MgmtAddress is the address found, with its prefix.
	MgmtAddress string `json:"mgmt_address,omitempty"`
	// BridgeName is the bridge carrying VLAN filtering (OBSERVED: `bridge`).
	BridgeName string `json:"bridge_name,omitempty"`
	// BridgeRowID is the `/interface/bridge` record `.id` for BridgeName.
	// Needed to enable `vlan-filtering` at the end of apply.
	BridgeRowID string `json:"-"`
	// VLANFiltering: is filtering ACTIVE on this bridge.
	//
	// When false, everything this file reads in `/interface/bridge/vlan` is INERT
	// configuration until apply enables filtering (after membership is written).
	// Enabling filtering BEFORE entries exist cuts everything instantly.
	VLANFiltering bool `json:"vlan_filtering"`
	// BridgePVID is the bridge's own `pvid` (OBSERVED: `"pvid":"1"` on `/interface/bridge`).
	//
	// THE FIELD WITHOUT WHICH A FACTORY DEVICE WAS NOT CONFIGURABLE. When the
	// administration address is on the bridge and not on a VLAN interface, administration
	// arrives UNTAGGED and the VLAN involved is the bridge's PVID. Derivation looked for
	// that PVID in `Ports[BridgeName]` — but the bridge is never a `/interface/bridge/port`
	// record, so that map never contains its name. MgmtVLAN stayed at 0, and apply refused
	// to write 'unknown management VLAN' on exactly the most common case: a factory
	// RouterOS with its address on the bridge.
	BridgePVID int `json:"bridge_pvid,omitempty"`
	// LinkUp: ports that have link. A port WITHOUT link cannot carry the current
	// administration session — this allows reconfiguring a new fleet without sparing
	// every port.
	LinkUp map[string]bool `json:"link_up,omitempty"`
	// CPUTagged is the list of VLANs whose `bridge` pseudo-port is a tagged member
	// (OBSERVED: VLAN 99). Administration can arrive through them; knowing them is what
	// allows refusing to break them.
	CPUTagged []int `json:"cpu_tagged,omitempty"`
	// Bondings: the device's link aggregates (`/interface/bonding`), read
	// beside the bridge tables the bonds appear in. Nil = menu unreadable.
	Bondings []Bonding `json:"bondings,omitempty"`
}

// cpuPort is the name RouterOS gives the CPU in bridge VLAN member lists.
//
// This is not a meshcraft convention: it is literally the bridge name used as a member
// of its own VLAN table, OBSERVED in `"tagged":"bridge,sfp-sfpplus2"`. The reference
// device's bridge happens to be named 'bridge'; on a device where it were named
// differently, that name would appear. Comparison is therefore against the REAL bridge
// name, never against the hard-coded string 'bridge' — hence State.BridgeName.
const defaultBridgeName = "bridge"

// ReadState re-reads the device and returns state in CONTRACT form.
//
// READ ONLY: replayable as often as needed, including on a production fleet. This is
// the 'Read' step of the reconciliation pipeline, and also what allows DATING an
// apply — nothing is considered applied on the faith of a return code alone.
//
// `Target.PortPrefix` IS IGNORED, and that is not an oversight: this connector does not
// even receive it, because it does not want it. On this platform meshcraft names ARE
// device names (`ether3`, `sfp-sfpplus1`), so the facade carries MULTIPLE prefixes —
// `ether` and `sfp-sfpplus` on the reference device — and none summarizes them. Using
// one as the prefix for all ports would write on the wrong port, cutting a client.
// Connectors that NUMBER their ports (Instant On, SwOS) need it to translate 'GE12' to
// index 12; this one has no translation to do, and that is correct behavior.
//
// THE RETURNED STATE IS POORER THAN WHAT THE DEVICE SAYS, and [State.Notes] is what
// keeps that impoverishment from becoming a lie. See [DeviceState.notes]: that is where
// inactive VLAN filtering and ports whose PVID matches no membership are stated. Without
// those sentences, the screen would present PRESENT AND INERT configuration as
// segmentation in place.
//
// ERROR DOES NOT PREVENT RETURNING WHAT WAS READ: a partial read is more useful than an
// empty state, and the caller has the error to know it does not hold everything. NOTES
// are then replaced, and that matters: [DeviceState.notes] describe what the device
// CARRIES, and on an interrupted read they would describe a device we read nothing from
// — 'no bridge on this device' where the tunnel simply dropped. A false sentence on a
// diagnostic screen sends you looking for a fault that does not exist.
func (c *Client) ReadState(ctx context.Context) (State, error) {
	var dev, err = c.ReadDeviceState(ctx)
	var out = dev.Standard()
	if err != nil {
		out.Notes = []string{"INCOMPLETE READ: " + err.Error() + " — what is " +
			"shown is what could be read before the interruption, and does not necessarily " +
			"describe the device. Do NOT treat this as a configuration drift: a read that " +
			"stops and a device that changed look the same on screen but are fixed by " +
			"opposite actions."}
	}
	return out, err
}

// ReadDeviceState re-reads the device and returns EVERYTHING RouterOS says about itself.
//
// READ ONLY. This is the read apply uses, because it needs record `.id`s, bridge name,
// filtering state, and the list of VLANs whose CPU pseudo-port is a member — five things
// the contract does not carry and without which writing would be blind.
//
// VALIDATED LIVE against 192.0.2.54. On the reference device returns: bridge `bridge`
// with filtering ACTIVE, 26 ports, 3 VLANs (1 dynamic, 99, 100), management VLAN 99 on
// `vlan99` at `192.0.2.54/24`, CPU tagged in 99, one port with link (`sfp-sfpplus2`).
func (c *Client) ReadDeviceState(ctx context.Context) (DeviceState, error) {
	var out = DeviceState{
		VLANs:  []BridgeVLAN{},
		Ports:  map[string]BridgePort{},
		LinkUp: map[string]bool{},
	}

	// 1. The bridge and its filtering. Without it, everything else is inert — and a
	//    device with no bridge at all simply has no VLANs to read.
	var bridges, err = c.List(ctx, "interface/bridge")
	if err != nil {
		return out, err
	}
	if len(bridges) == 0 {
		c.step(true, "no bridge on this device",
			"no switching VLANs to read")
		return out, nil
	}
	// The chosen bridge is the one that FILTERS, if any (cf. pickBridge). The rule
	// lives in a function and not inline here because a SECOND reader now depends on
	// it: DHCP snooping is a bridge property (dhcpsnoop.go), and a read that answered
	// about one bridge while apply's guardrails reasoned about another would report
	// trust flags belonging to a switching table nobody is looking at.
	var chosen = pickBridge(bridges)
	out.BridgeName = chosen.Get("name")
	out.BridgeRowID = chosen.ID()
	out.VLANFiltering = chosen.Bool("vlan-filtering")
	// The bridge's PVID, read here because this is the only place it is used: without it,
	// UNTAGGED administration on the bridge stays invisible and apply refuses to write (cf.
	// DeviceState.BridgePVID).
	var bridgePVID, hasBridgePVID = chosen.Int("pvid")
	if hasBridgePVID {
		out.BridgePVID = bridgePVID
	}
	if len(bridges) > 1 {
		// A device with several bridges is outside meshcraft's model, which assumes one
		// switching table. Saying so in the transcript beats reading the first silently and
		// comparing against the wrong one.
		c.step(true, "multiple bridges present",
			fmt.Sprintf("%d bridges; '%s' chosen (the one filtering VLANs)",
				len(bridges), out.BridgeName))
	}

	// 2. Declared VLANs.
	var vlanRows []Row
	vlanRows, err = c.List(ctx, "interface/bridge/vlan")
	if err != nil {
		return out, err
	}
	// 3. Bridge ports, carrying PVIDs.
	var portRows []Row
	portRows, err = c.List(ctx, "interface/bridge/port")
	if err != nil {
		return out, err
	}

	out.VLANs, out.Ports, out.CPUTagged = foldVLANs(vlanRows, portRows, out.BridgeName)

	// 3bis. Link aggregates. Tolerated absence: a reduced firmware without the
	// menu still has ports and VLANs worth reading, and `Bondings` nil reads as
	// "not read" downstream (cf. driver.State.LAGs).
	var bondRows, bondErr = c.List(ctx, "interface/bonding")
	if bondErr == nil {
		out.Bondings = foldBondings(bondRows)
	} else {
		c.step(true, "bonding menu unreadable",
			"aggregates not read: "+bondErr.Error())
	}

	// 4. Link state, read on physical interfaces.
	//
	//    `running` not `disabled`: an administratively active port with no cable carries
	//    no session, and that is the only question that matters for apply safeguards.
	var ifaces []Row
	ifaces, err = c.List(ctx, "interface")
	if err == nil {
		var r Row
		for _, r = range ifaces {
			if r.Bool("running") {
				out.LinkUp[r.Get("name")] = true
			}
		}
	}

	// 5. Management VLAN, FROM the device. The only source that says where the current
	//    session goes.
	err = c.readMgmt(ctx, &out)
	if err != nil {
		return out, err
	}

	c.step(true, "switching state read",
		fmt.Sprintf("bridge '%s' (filtering %s), %d VLAN(s), %d port(s), "+
			"management VLAN %d via %s", out.BridgeName,
			map[bool]string{true: "active", false: "INACTIVE"}[out.VLANFiltering],
			len(out.VLANs), len(out.Ports), out.MgmtVLAN, out.MgmtInterface))
	return out, nil
}

// readMgmt determines how administration arrives, and it is the most important read
// in this file.
//
// The derivation chain, entirely OBSERVED on the reference device:
//
//	/ip/address      → {"address":"192.0.2.54/24","interface":"vlan99",
//	                    "actual-interface":"vlan99"}
//	/interface/vlan  → {"name":"vlan99","vlan-id":"99","interface":"bridge"}
//
// therefore: the address is on `vlan99`, a VLAN interface with ID 99 on the bridge ⇒
// management VLAN is 99. And `/interface/bridge/vlan` confirms the indispensable
// consequence: VLAN 99 has `bridge` in its `tagged` list, otherwise nothing would reach
// the CPU.
//
// The case where the address is on the BRIDGE itself is handled separately and leaves
// MgmtVLAN at 0: administration then arrives UNTAGGED, and the VLAN involved is the
// bridge's PVID. That is factory configuration (OBSERVED: `192.168.88.1/24` on `bridge`).
func (c *Client) readMgmt(ctx context.Context, out *DeviceState) error {
	var addrs, err = c.List(ctx, "ip/address")
	if err != nil {
		return err
	}
	// The VLAN interface menu may be unreadable on reduced firmware. We then pass a
	// nil slice: foldMgmt will conclude 'untagged' or 'unknown' rather than invent an
	// identifier.
	var vlans []Row
	vlans, _ = c.List(ctx, "interface/vlan") //nolint:errcheck // absence handled by foldMgmt

	foldMgmt(addrs, vlans, out)
	if out.MgmtAddress == "" {
		// No address: the device is reachable otherwise (MAC-winbox, serial), or we are
		// not talking to who we think. In either case, assert nothing — and say so, because
		// apply will refuse to write for this reason.
		c.step(true, "no active IP address",
			"management VLAN cannot be determined — apply will refuse "+
				"to write (cf. apply.go)")
	}
	return nil
}

// foldMgmt determines how administration arrives from raw records. PURE function, so
// tests replay it on real captures: all apply safeguards depend on this derivation, and
// it must not be verifiable only in production.
func foldMgmt(addrs, vlans []Row, out *DeviceState) {
	// The address that matters is the one WE speak through. We cannot guess it from the
	// device, but we can do better than taking the first: we skip disabled or invalid
	// ones, and AVOID those factory configuration left behind.
	//
	// Sorting by `comment: defconf` is not a cosmetic trick. OBSERVED on the reference
	// device, `/ip/address` carries TWO addresses: `192.168.88.1/24` on the bridge with
	// `comment: defconf` — the factory address nobody uses but which still responds — and
	// `192.0.2.54/24` on `vlan99`, the real one. Keeping the first would designate VLAN 1
	// as management VLAN, therefore protect the wrong VLAN and leave the session carrier
	// open to rewrite.
	var best Row
	var r Row
	for _, r = range addrs {
		if r.Bool("disabled") || r.Bool("invalid") {
			continue
		}
		if best == nil {
			best = r
			continue
		}
		if r.Get("comment") != "defconf" && best.Get("comment") == "defconf" {
			best = r
		}
	}
	if best == nil {
		return
	}
	out.MgmtAddress = best.Get("address")
	out.MgmtInterface = best.Get("actual-interface")
	if out.MgmtInterface == "" {
		out.MgmtInterface = best.Get("interface")
	}

	// Is this interface a VLAN interface? If so, its `vlan-id` IS the management VLAN,
	// and administration arrives TAGGED.
	for _, r = range vlans {
		if r.Get("name") != out.MgmtInterface {
			continue
		}
		var id, ok = r.Int("vlan-id")
		if ok {
			out.MgmtVLAN = id
		}
		return
	}
	// The address is on the bridge itself: UNTAGGED administration, and the VLAN
	// involved is the bridge's PVID.
	//
	// Read on `/interface/bridge` ([DeviceState.BridgePVID]) not in `Ports[BridgeName]`:
	// the bridge is never a `/interface/bridge/port` record, so that map never carries its
	// name. Looking there left MgmtVLAN at 0 on every factory device, and apply refused to
	// write 'unknown management VLAN' — on the most common case there is.
	if out.MgmtInterface == out.BridgeName && out.BridgePVID > 0 {
		out.MgmtVLAN = out.BridgePVID
	}
}

// foldVLANs is the heart of translation: RouterOS's three-menu model reduced to
// meshcraft's per-port model.
//
// PURE function, deliberately — tests replay it on real captures in testdata/. An error
// here would only show when a switch goes silent.
//
// Also returns the list of VLANs whose CPU is a tagged member, because the same pass
// discovers it and apply cannot do without it.
func foldVLANs(vlanRows, portRows []Row, bridgeName string) (
	[]BridgeVLAN, map[string]BridgePort, []int) {
	if bridgeName == "" {
		bridgeName = defaultBridgeName
	}
	var vlans = []BridgeVLAN{}
	var ports = map[string]BridgePort{}
	var cpuTagged []int

	// Ports first, for their PVID. The only PVID source: it is NOT derived from
	// membership (cf. header, trap #1).
	var r Row
	for _, r = range portRows {
		if r.Get("bridge") != bridgeName {
			continue // port of another bridge: not our switching table
		}
		var name = r.Get("interface")
		if name == "" {
			continue
		}
		var st = BridgePort{
			RowID:            r.ID(),
			Bridge:           r.Get("bridge"),
			HWOffload:        r.Bool("hw-offload"),
			FrameTypes:       r.Get("frame-types"),
			IngressFiltering: r.Bool("ingress-filtering"),
		}
		var pvid, ok = r.Int("pvid")
		if ok {
			st.PVID = pvid
		}
		ports[name] = st
	}

	// Then VLANs, for memberships.
	var tagged = map[string][]int{}
	var untagged = map[string][]int{}
	for _, r = range vlanRows {
		if r.Get("bridge") != bridgeName {
			continue
		}
		// `vlan-ids` may carry MULTIPLE identifiers and RANGES: a RouterOS-format list,
		// not an integer. Simple OBSERVED values here ("99", "100", "1"), but the device
		// accepts '10-20,30' in the same record, and reading an integer would fail
		// silently on that form.
		var ids = ParseVLANIDs(r.Get("vlan-ids"))
		if len(ids) == 0 {
			continue
		}
		// Member lists are kept AS IS, CPU pseudo-port included: writes start from the
		// ENTIRE list, and reconstructing from the per-port view lost untagged `bridge` and
		// any member that is not a port in the read table (cf. BridgeVLAN.TaggedMembers).
		var rowTagged = SplitList(r.Get("tagged"))
		var rowUntagged = SplitList(r.Get("untagged"))
		var id int
		for _, id = range ids {
			vlans = append(vlans, BridgeVLAN{
				ID:      id,
				Name:    r.Get("comment"),
				Dynamic: r.Bool("dynamic"),
				RowID:   r.ID(),
				// ALL identifiers from the line, on EACH entry it spawns: this lets apply
				// see that writing 'VLAN 10' membership would also touch the other ten.
				IDs:             ids,
				TaggedMembers:   rowTagged,
				UntaggedMembers: rowUntagged,
			})
		}
		// `tagged` / `untagged` carry INTENDED configuration; `current-tagged` /
		// `current-untagged` carry what the device actually applies, dynamic members
		// included. We read the first: those are what apply compares and rewrites, and the
		// second contain members we did not set and must not treat as ours.
		var member string
		for _, member = range SplitList(r.Get("tagged")) {
			for _, id = range ids {
				if member == bridgeName {
					cpuTagged = append(cpuTagged, id)
					continue
				}
				tagged[member] = append(tagged[member], id)
			}
		}
		for _, member = range SplitList(r.Get("untagged")) {
			for _, id = range ids {
				if member == bridgeName {
					continue // CPU untagged: not a facade port
				}
				untagged[member] = append(untagged[member], id)
			}
		}
	}

	// Reassembly per port.
	var name string
	var ids []int
	for name, ids = range tagged {
		var st = ports[name]
		sort.Ints(ids)
		st.Tagged = dedupe(ids)
		ports[name] = st
	}
	for name, ids = range untagged {
		var st = ports[name]
		sort.Ints(ids)
		st.Untagged = dedupe(ids)
		ports[name] = st
	}

	// MODE, derived last with a simple rule: a port carrying tagged VLANs is a trunk,
	// whatever its native VLAN; a port carrying none is access. Same rule as the Instant
	// On connector, so two product lines compare cleanly.
	//
	// A port with neither PVID nor membership stays in empty mode: it is in the bridge but
	// nothing concerns it. 'Absent = unmanaged' means meshcraft claims nothing for it,
	// and pretending it is 'access VLAN 0' would be false.
	for name = range ports {
		var st = ports[name]
		switch {
		case len(st.Tagged) > 0:
			st.Mode = "trunk"
		case st.PVID > 0 || len(st.Untagged) > 0:
			st.Mode = "access"
		}
		ports[name] = st
	}

	sort.Slice(vlans, func(i, j int) bool { return vlans[i].ID < vlans[j].ID })
	sort.Ints(cpuTagged)
	return vlans, ports, dedupe(cpuTagged)
}

// dedupe removes duplicates from a SORTED slice. Needed because the same VLAN can
// appear in several `/interface/bridge/vlan` records — nothing forbids that on this
// platform, and the dynamic 'added by pvid' entry often overlaps a static one.
func dedupe(in []int) []int {
	if len(in) == 0 {
		return nil
	}
	var out = make([]int, 0, len(in))
	var i int
	for i = 0; i < len(in); i++ {
		if i > 0 && in[i] == in[i-1] {
			continue
		}
		out = append(out, in[i])
	}
	return out
}

// ParseVLANIDs expands RouterOS `vlan-ids` notation: '99', '10-20,30', '1-4094'.
//
// Ranges are bounded to what 802.1Q space allows, and a reversed range is SKIPPED rather
// than guessed: a response we cannot read must not spawn four thousand imaginary VLANs.
// Same caution as ParsePortRange on Instant On, and for the same reason — a complete
// false state is worse than an incomplete honest one.
func ParseVLANIDs(spec string) []int {
	var out []int
	var token string
	for _, token = range SplitList(spec) {
		var dash = strings.IndexByte(token, '-')
		if dash < 0 {
			var v, err = strconv.Atoi(token)
			if err != nil || v < 1 || v > 4094 {
				continue
			}
			out = append(out, v)
			continue
		}
		var from, errFrom = strconv.Atoi(strings.TrimSpace(token[:dash]))
		var to, errTo = strconv.Atoi(strings.TrimSpace(token[dash+1:]))
		if errFrom != nil || errTo != nil {
			continue
		}
		if from < 1 || to > 4094 || to < from {
			continue
		}
		var v int
		for v = from; v <= to; v++ {
			out = append(out, v)
		}
	}
	sort.Ints(out)
	return dedupe(out)
}

// VLANIDs returns VLAN identifiers from the state, sorted and deduplicated.
func (s DeviceState) VLANIDs() []int {
	var out []int
	var v BridgeVLAN
	for _, v = range s.VLANs {
		out = append(out, v.ID)
	}
	sort.Ints(out)
	return dedupe(out)
}

// CPUCarries reports whether the CPU pseudo-port is a tagged member of the given VLAN —
// therefore whether administration can arrive through it.
func (s DeviceState) CPUCarries(vlanID int) bool {
	var v int
	for _, v = range s.CPUTagged {
		if v == vlanID {
			return true
		}
	}
	return false
}

// --- Conversion to the contract view ---

// Standard reduces rich state to what the contract carries, and SAYS what is lost.
//
// Conversion is explicit and lives here, in a pure function tests replay, rather than
// scattered in the API layer: five [DeviceState] fields do not enter `driver.State`, and
// the question is not whether they disappear — they do — but whether their disappearance
// LIES.
//
// It would lie without [State.Notes]. Two facts of this platform are invisible in the
// contract view, and both turn configuration into scenery:
//
//   - a bridge whose `vlan-filtering` is inactive lets EVERYTHING pass EVERYWHERE. The
//     VLANs and memberships rendered below do exist on the device, and have no effect on
//     a single frame;
//   - a port can carry a PVID without being a member of the corresponding VLAN. OBSERVED
//     on the reference device: ether1..ether24 have `pvid=100` while VLAN 100 has an
//     EMPTY `untagged` list. The device accepts that, silently.
//
// A screen presenting both cases as segmentation in place would make an operator conclude
// 'this switch is compliant' while traffic is not segmented at all. This is the only
// place in the connector where that can still be said.
func (s DeviceState) Standard() State {
	var out = State{
		VLANs:    make([]VLAN, 0, len(s.VLANs)),
		Ports:    make(map[string]PortState, len(s.Ports)),
		MgmtVLAN: s.MgmtVLAN,
		Links:    driver.LinksFromUp(s.LinkUp),
		Notes:    s.notes(),
	}

	// VLANs, DEDUPLICATED by identifier with the STATIC entry preferred.
	//
	// The same VLAN can appear in several records — nothing forbids that on this platform,
	// and the dynamic 'added by pvid' entry often overlaps a static one. The contract
	// describes 'VLANs declared on the device': returning one twice would count two VLANs
	// where there is one, which drift calculation would read as a gap. We keep the STATIC
	// entry's name, because a dynamic entry's name is a comment the device wrote to itself.
	var at = map[int]int{}
	var v BridgeVLAN
	for _, v = range s.VLANs {
		var pos, seen = at[v.ID]
		if !seen {
			at[v.ID] = len(out.VLANs)
			out.VLANs = append(out.VLANs, VLAN{ID: v.ID, Name: v.Name})
			continue
		}
		if !v.Dynamic && out.VLANs[pos].Name == "" {
			out.VLANs[pos].Name = v.Name
		}
	}

	// Ports. `Untagged`, `RowID`, `Bridge`, and `HWOffload` stay behind: the first is
	// stated in notes, the other three serve only to write.
	//
	// `TaggedAtLeast` is NOT copied, and that is not an oversight: it is an INTENT field.
	// A device carries a list, not a floor, and setting it on a read state would make
	// comparison believe no removal is ever needed.
	var name string
	var st BridgePort
	for name, st = range s.Ports {
		out.Ports[name] = PortState{Mode: st.Mode, PVID: st.PVID, Tagged: st.Tagged}
	}

	// Aggregates, and their bridge-port entries LEAVE the port map: a bond is
	// not a faceplate jack, and a "bonding1" key in Ports would read as an
	// unmanaged port to every consumer that iterates the map. Its VLAN half
	// lives on the LAG (standardLAGs read it from this same map first).
	out.LAGs = standardLAGs(s)
	var i int
	for i = range s.Bondings {
		delete(out.Ports, s.Bondings[i].Name)
	}
	return out
}

// notes renders, in sentences for an operator, what the contract view cannot say about
// this device.
//
// SENTENCES NOT CODES: the caller displays them as-is and makes no decision from them.
// That is deliberate, and it is the contract rule — a connector that wants a decision
// taken must refuse itself, which [Client.ApplyPorts] does for everything that matters.
//
// ORDER GOES FROM MOST SEVERE TO MOST ANECDOTAL, because a note list is read from the
// top: inactive filtering cancels everything else, an orphan PVID concerns only named ports.
func (s DeviceState) notes() []string {
	var out []string

	if s.BridgeName == "" {
		// No bridge: there is no switching table on this device. That is not a read error
		// — a RouterOS can route without switching — but the screen must not present 'no
		// VLAN' as a reset switch.
		out = append(out, "No bridge on this device: there is no switching table, "+
			"therefore no VLAN to read or apply. A RouterOS without a bridge routes without "+
			"switching — it is not an empty switch, it is a different use case.")
		return out
	}

	if !s.VLANFiltering {
		out = append(out, fmt.Sprintf("VLAN FILTERING INACTIVE on bridge '%s': "+
			"the VLANs and memberships listed here EXIST on the device but have NO effect — "+
			"the bridge lets everything pass everywhere, tagged or untagged. This is therefore "+
			"not segmentation in place, it is configuration ready to take effect. Apply enables "+
			"`vlan-filtering` last, after membership and per-port filters are written.",
			s.BridgeName))
	}

	// Ports whose PVID matches no membership.
	//
	// OBSERVED, accepted by the device, and SILENT: these ports tag ingress traffic into a
	// VLAN they are not egress members of. On the reference device, that is all 24 copper ports.
	var orphans []string
	var name string
	var st BridgePort
	for name, st = range s.Ports {
		if st.PVID > 0 && !containsInt(st.Untagged, st.PVID) {
			orphans = append(orphans, name)
		}
	}
	if len(orphans) > 0 {
		sortPortNames(orphans)
		out = append(out, fmt.Sprintf("PVID WITHOUT MEMBERSHIP on %d port(s): %s. These "+
			"ports tag their ingress traffic into their native VLAN but do not appear in "+
			"that VLAN's `untagged` list — so nothing egresses from it. RouterOS accepts "+
			"this configuration without signaling: on this platform PVID and membership are "+
			"two INDEPENDENT settings in two different menus. This is incomplete "+
			"configuration, not a fault, and a meshcraft apply completes it by setting both.",
			len(orphans), strings.Join(orphans, ", ")))
	}

	// THE OTHER HALF OF FILTERING, which makes segmentation permeable without saying so.
	//
	// These two notes are the exact RouterOS counterpart of what SwOS read says about
	// `vlni` and VLAN mode: configuration EXISTS, it may even be correct, and it does not
	// do what a screen suggests. A per-port VLAN table presents segmentation;
	// `ingress-filtering: false` makes it one-way only, and `frame-types: admit-all` on an
	// access port lets a host leave the VLAN we assigned by tagging its own traffic.
	//
	// They are rendered only when VLAN filtering is ACTIVE: on a bridge that does not
	// filter, everything already passes everywhere, the note above says so, and adding two
	// nuances about a nonexistent filter would bury the only sentence that matters.
	if s.VLANFiltering {
		var permeable, accessAdmitAll []string
		for name, st = range s.Ports {
			if !st.IngressFiltering {
				permeable = append(permeable, name)
			}
			if st.FrameTypes == "admit-all" && len(st.Tagged) == 0 && st.PVID > 0 {
				accessAdmitAll = append(accessAdmitAll, name)
			}
		}
		if len(permeable) > 0 {
			sortPortNames(permeable)
			out = append(out, fmt.Sprintf("INGRESS FILTERING INACTIVE on %d port(s): %s "+
				"(`ingress-filtering: false`). On these ports, the bridge table is consulted "+
				"ONLY ON EGRESS: a frame tagged for a VLAN the port is not a member of is "+
				"accepted, learned, and switched to that VLAN's members. Segmentation is "+
				"therefore one-way — egress is filtered, ingress is not. This is the RouterOS "+
				"default, not a particular configuration, and a meshcraft apply enables it "+
				"when it can do so without risking access.", len(permeable),
				strings.Join(permeable, ", ")))
		}
		if len(accessAdmitAll) > 0 {
			sortPortNames(accessAdmitAll)
			out = append(out, fmt.Sprintf("ACCESS PORTS THAT ACCEPT TAGGED FRAMES: %s "+
				"(`frame-types: admit-all`). These ports carry no tagged VLANs yet accept "+
				"tagged frames on ingress: a host that tags its own traffic leaves the VLAN "+
				"the plan assigns it. Refusing that requires `admit-only-untagged-and-priority-tagged`, "+
				"which a meshcraft apply writes on access ports when no management VLAN arrives "+
				"tagged on them.",
				strings.Join(accessAdmitAll, ", ")))
		}
	}

	// HW OFFLOAD lost on some ports: throughput is no longer from the chip, and nothing
	// on the device signals it.
	var offload = hwOffloadNote(s)
	if offload != "" {
		out = append(out, offload)
	}

	if s.MgmtAddress == "" {
		// Apply will refuse to write for this exact reason, and the operator must be able
		// to connect that before trying.
		out = append(out, "No active IP address observed: meshcraft cannot determine how "+
			"administration arrives, and WILL refuse to write VLAN memberships — rewriting "+
			"member lists without knowing how we got in is exactly the gesture that cuts "+
			"access. A device reached via MAC-winbox or serial console is legitimately in "+
			"this case.")
		return out
	}
	// Address / interface / VLAN are shown on the Management network card (observed
	// addressing + VLAN dropdown). Restating them here flooded last_apply.notes after
	// every port apply without giving the operator anything new to act on.
	return out
}

// containsInt reports whether a slice contains a value. Two lines rather than a loop
// copied at every membership comparison.
func containsInt(list []int, want int) bool {
	var v int
	for _, v = range list {
		if v == want {
			return true
		}
	}
	return false
}

// sortPortNames sorts port names the way a human reads them on the facade: by prefix,
// then by NUMBER.
//
// Alphabetical sort would place `ether10` before `ether2`, and a note listing twenty-four
// ports in that order is unreadable — which makes it useless, therefore equivalent to
// saying nothing.
func sortPortNames(names []string) {
	sort.SliceStable(names, func(i, j int) bool {
		var pi, ni = splitPortName("", names[i])
		var pj, nj = splitPortName("", names[j])
		if pi != pj {
			return pi < pj
		}
		return ni < nj
	})
}
