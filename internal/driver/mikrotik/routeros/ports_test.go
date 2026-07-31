package routeros

import (
	"reflect"
	"strings"
	"testing"
)

// Tests for the OTHER HALF of filtering — the half the first delivery neither read nor wrote
// — and the three member losses a complete-list write made possible.
//
// All replay real captures from the reference CRS326-24G-2S+ (testdata/). What they carry,
// and what makes these tests useful rather than theoretical:
//
//	/interface/bridge       vlan-filtering true, pvid 1, protocol-mode mstp
//	/interface/bridge/port  frame-types admit-all and ingress-filtering false on all
//	                        26 ports; hw-offload true everywhere; pvid 100 on ether1..24
//	                        and 1 on both SFP+
//	/interface/bridge/vlan  VLAN 99 tagged "bridge,sfp-sfpplus2"; VLAN 100 tagged
//	                        "sfp-sfpplus2" with empty untagged; VLAN 1 DYNAMIC,
//	                        untagged "bridge,sfp-sfpplus2"
//
// No WRITE was exercised on this hardware: these tests prove the decision, not the device's
// reaction.

// pvidNote extracts the orphan PVID note from a note block.
//
// Used by tests that verify what a note does NOT say: other notes legitimately cite all
// bridge ports, and searching a name in the whole block would forbid any future note from
// naming the uplink.
func pvidNote(notes []string) string {
	var n string
	for _, n = range notes {
		if strings.Contains(n, "PVID WITHOUT MEMBERSHIP") {
			return n
		}
	}
	return ""
}

// --- What the reference device actually carries ---

// TestIngressFilteringAndFrameTypesObserved: prerequisite for all following tests.
//
// All 26 ports are `admit-all` and `ingress-filtering: false`. These are not capture
// details: they are the two values that make segmentation permeable, and the connector did
// not read them.
func TestIngressFilteringAndFrameTypesObserved(t *testing.T) {
	var st = stateFromFixtures(t)
	if len(st.Ports) != 26 {
		t.Fatalf("%d port(s) in bridge, 26 expected", len(st.Ports))
	}
	var name string
	var p BridgePort
	for name, p = range st.Ports {
		if p.FrameTypes != frameAdmitAll {
			t.Errorf("%s: frame-types = %q, expected %q (observed)", name, p.FrameTypes,
				frameAdmitAll)
		}
		if p.IngressFiltering {
			t.Errorf("%s: ingress-filtering = true, expected false (observed)", name)
		}
	}
}

// TestPermeableFilteringNotes: what the contract view cannot say.
//
// A per-port VLAN table presents segmentation. `ingress-filtering: false` makes it watertight
// in one direction only, and `frame-types: admit-all` on an access port lets a host leave its
// assigned VLAN by tagging its own traffic. Without these sentences, the screen presents
// segmentation that is not quite segmentation.
func TestPermeableFilteringNotes(t *testing.T) {
	var st = stateFromFixtures(t).Standard()
	var notes = strings.Join(st.Notes, "\n")

	if !strings.Contains(notes, "INGRESS FILTERING INACTIVE") {
		t.Errorf("notes must say the bridge table is consulted only on EGRESS on these 26 "+
			"ports:\n%s", notes)
	}
	if !strings.Contains(notes, "ACCESS PORTS THAT ACCEPT TAGGED FRAMES") {
		t.Errorf("notes must say an access port in admit-all lets a host leave its VLAN:\n%s",
			notes)
	}
	// The uplink carries tagged VLANs: it is NOT an access port, and citing it there would
	// send one hunting a fault on a correct configuration.
	var accessNote string
	var n string
	for _, n = range st.Notes {
		if strings.Contains(n, "ACCESS PORTS THAT ACCEPT TAGGED FRAMES") {
			accessNote = n
		}
	}
	if strings.Contains(accessNote, "sfp-sfpplus2") {
		t.Errorf("sfp-sfpplus2 is a trunk (tagged on 99 and 100): it has no place in the "+
			"access port note:\n%s", accessNote)
	}
}

// TestNoFilteringNoteWhenBridgeDoesNotFilter: do not bury the one sentence that matters.
//
// On a bridge whose `vlan-filtering` is inactive, everything already passes everywhere: adding
// two nuances about a nonexistent filter would push into the background the note that says
// NOTHING is applied.
func TestNoFilteringNoteWhenBridgeDoesNotFilter(t *testing.T) {
	var dev = stateFromFixtures(t)
	dev.VLANFiltering = false
	var notes = strings.Join(dev.Standard().Notes, "\n")

	if !strings.Contains(notes, "VLAN FILTERING INACTIVE") {
		t.Fatalf("main note must be there:\n%s", notes)
	}
	if strings.Contains(notes, "INGRESS FILTERING INACTIVE") ||
		strings.Contains(notes, "ACCESS PORTS THAT ACCEPT TAGGED FRAMES") {
		t.Errorf("on a bridge that does not filter, these nuances make no sense and "+
			"would bury the note that says nothing is applied:\n%s", notes)
	}
}

// --- What placement writes, and what it refuses to write ---

// TestTrunkNeverGetsAdmitOnlyVLANTagged — the most important refusal in ports.go, and it
// does not come from the device.
//
// `admit-only-vlan-tagged` is what a « clean » trunk should carry. It is INEXPRESSIBLE in
// meshcraft's model: a trunk ALWAYS has a native VLAN — when intent does not designate one,
// `recon.ParsePorts` puts VLAN 1, and that is a project rule — and a native VLAN means that
// traffic enters and leaves WITHOUT a tag. Writing `admit-only-vlan-tagged` would therefore
// cut the native VLAN the plan requests.
func TestTrunkNeverGetsAdmitOnlyVLANTagged(t *testing.T) {
	var cases = []struct {
		name string
		want PortState
	}{
		{"trunk with explicit native", PortState{Mode: "trunk", PVID: 100,
			Tagged: []int{99}}},
		{"trunk with implicit native (VLAN 1)", PortState{Mode: "trunk", PVID: 1,
			Tagged: []int{99, 100}}},
		{"access that still carries tagged", PortState{Mode: "access", PVID: 20,
			Tagged: []int{30}}},
	}
	var c = cases[0]
	for _, c = range cases {
		t.Run(c.name, func(t *testing.T) {
			var got = frameTypesFor(c.want)
			if got == frameAdmitTagged {
				t.Errorf("frame-types = %q: this would drop untagged traffic of native "+
					"VLAN %d that the plan explicitly requests", got, c.want.PVID)
			}
			if got != "" {
				t.Errorf("frame-types = %q, expected empty: a hybrid port stays at "+
					"admit-all, neither tightened nor relaxed", got)
			}
		})
	}
	// A pure ACCESS port, however, must reject tagged: that is what makes it a real access port.
	var got = frameTypesFor(PortState{Mode: "access", PVID: 20})
	if got != frameAdmitUntagged {
		t.Errorf("pure access frame-types = %q, expected %q", got, frameAdmitUntagged)
	}
}

// TestIngressFilteringEnabledOnRiskFreePort
//
// `ingress-filtering=yes` is what makes segmentation watertight IN BOTH DIRECTIONS. On a port
// without link, no session passes: there is nothing to protect.
func TestIngressFilteringEnabledOnRiskFreePort(t *testing.T) {
	var protected = map[int]string{99: "management VLAN declared by device"}
	var now = BridgePort{PVID: 100, FrameTypes: frameAdmitAll, IngressFiltering: false}

	// Port WITHOUT link.
	var got = planFiltering("ether1", PortState{Mode: "access", PVID: 100},
		now, map[int]bool{100: true}, false, protected, false)
	if !got.SetIngress || !got.Ingress {
		t.Errorf("port without link must receive ingress-filtering=yes: %+v", got)
	}
	if got.FrameTypes != frameAdmitUntagged {
		t.Errorf("access port without link must reject tagged: %+v", got)
	}
	if len(got.Refused) != 0 {
		t.Errorf("no refusal expected: %v", got.Refused)
	}

	// Port WITH link, member of all management VLANs: safe too.
	got = planFiltering("sfp-sfpplus2", PortState{Mode: "trunk", PVID: 1,
		Tagged: []int{99, 100}}, BridgePort{PVID: 1, FrameTypes: frameAdmitAll,
		Tagged: []int{99, 100}}, map[int]bool{1: true, 99: true, 100: true},
		true, protected, false)
	if !got.SetIngress || !got.Ingress {
		t.Errorf("active port member of management VLAN can receive ingress filtering: %+v", got)
	}
	if len(got.Refused) != 0 {
		t.Errorf("no refusal expected: %v", got.Refused)
	}
}

// TestIngressFilteringRefusedWhenPortNotManagementMember — guardrail #1.
//
// `ingress-filtering=yes` drops, ON RECEPTION, traffic for every VLAN the port is not a member
// of. On an active port that is not a member of the management VLAN, administration falls if
// the session goes through there — and the outage is visible only from meshcraft's side, site
// traffic keeps switching.
func TestIngressFilteringRefusedWhenPortNotManagementMember(t *testing.T) {
	var protected = map[int]string{99: "management VLAN declared by device"}
	var now = BridgePort{PVID: 100, FrameTypes: frameAdmitAll}

	var got = planFiltering("sfp-sfpplus2", PortState{Mode: "trunk", PVID: 100,
		Tagged: []int{30}}, now, map[int]bool{100: true, 30: true}, true, protected, false)
	if got.SetIngress {
		t.Error("ingress filtering was enabled on active port not member of management VLAN: " +
			"administration would fall if the session goes through there")
	}
	if len(got.Refused) != 1 {
		t.Fatalf("%d refusals, expected 1: %v", len(got.Refused), got.Refused)
	}
	var expected string
	for _, expected = range []string{"sfp-sfpplus2", "99", "ACTIVE link",
		"airtight in one direction only"} {
		if !strings.Contains(got.Refused[0], expected) {
			t.Errorf("refusal must contain %q; got: %q", expected, got.Refused[0])
		}
	}

	// `force` lifts the guardrail: explicit operator gesture who knows topology better than
	// meshcraft.
	got = planFiltering("sfp-sfpplus2", PortState{Mode: "trunk", PVID: 100,
		Tagged: []int{30}}, now, map[int]bool{100: true}, true, protected, true)
	if !got.SetIngress || len(got.Refused) != 0 {
		t.Errorf("force must lift this guardrail: %+v", got)
	}
}

// TestFrameTypesRefusedWhenManagementArrivesTagged — guardrail #2.
//
// `admit-only-untagged-and-priority-tagged` drops TAGGED frames. On this platform that case
// is not theoretical: the reference device is administered through a TAGGED VLAN (`vlan99` on
// the bridge, VLAN 99 `tagged: bridge,sfp-sfpplus2`). Tightening the port where that session
// arrives cuts it off.
func TestFrameTypesRefusedWhenManagementArrivesTagged(t *testing.T) {
	var protected = map[int]string{99: "management VLAN declared by device"}
	// Port carries VLAN 99 TAGGED, and the plan would make it an access port.
	var now = BridgePort{PVID: 20, FrameTypes: frameAdmitAll, Tagged: []int{99},
		IngressFiltering: true}

	var got = planFiltering("sfp-sfpplus2", PortState{Mode: "access", PVID: 20},
		now, map[int]bool{20: true, 99: true}, true, protected, false)
	if got.FrameTypes != "" {
		t.Errorf("frame-types = %q: management VLAN arrives TAGGED on this active port, "+
			"dropping tagged would cut administration", got.FrameTypes)
	}
	if len(got.Refused) != 1 {
		t.Fatalf("%d refusals, expected 1: %v", len(got.Refused), got.Refused)
	}
	var expected string
	for _, expected = range []string{"sfp-sfpplus2", "TAGGED", "serial", "MAC-winbox"} {
		if !strings.Contains(got.Refused[0], expected) {
			t.Errorf("refusal must contain %q; got: %q", expected, got.Refused[0])
		}
	}

	// Same port WITHOUT link: nothing left to protect.
	got = planFiltering("ether5", PortState{Mode: "access", PVID: 20}, now,
		map[int]bool{20: true, 99: true}, false, protected, false)
	if got.FrameTypes != frameAdmitUntagged {
		t.Errorf("port without link must be tighten-able: %+v", got)
	}
}

// TestNothingWrittenWhenAlreadyConformant: on a platform that persists immediately, a `set`
// that changes nothing is risk taken for nothing — and it would be taken on every
// reconciliation pass across the whole fleet.
func TestNothingWrittenWhenAlreadyConformant(t *testing.T) {
	var now = BridgePort{PVID: 20, FrameTypes: frameAdmitUntagged, IngressFiltering: true}
	var got = planFiltering("ether5", PortState{Mode: "access", PVID: 20}, now,
		map[int]bool{20: true}, true, map[int]string{99: "plan"}, false)

	if got.SetIngress {
		t.Error("ingress-filtering already active: nothing to write")
	}
	if got.FrameTypes != "" {
		t.Errorf("frame-types already correct: nothing to write (got %q)", got.FrameTypes)
	}
	if len(got.Refused) != 0 {
		t.Errorf("nothing to write, therefore nothing to refuse: %v", got.Refused)
	}
}

// --- The three member losses a complete-list write made possible ---

// TestUntaggedCPUPreserved — THE bug that closed UNTAGGED administration.
//
// Preserving pseudo-port `bridge` applied only to the TAGGED list. Yet the CPU can be an
// UNTAGGED member, and factory configuration does: the reference device carries
// `untagged: "bridge,sfp-sfpplus2"` on VLAN 1. As `membersOf` excludes the CPU from BOTH
// lists, rewriting VLAN 1 untagged membership lost it silently — a technically successful
// apply, verified by reread, and administration closed.
func TestUntaggedCPUPreserved(t *testing.T) {
	var st = stateFromFixtures(t)

	var row, found = vlanRowAny(st, 1)
	if !found {
		t.Fatal("prerequisite: reference device carries an entry for VLAN 1")
	}
	if !row.CPUIsMember(st.BridgeName, false) {
		t.Fatalf("prerequisite: CPU must be UNTAGGED member of VLAN 1 (observed "+
			"`untagged: \"bridge,sfp-sfpplus2\"`); got %v", row.UntaggedMembers)
	}
	if row.CPUIsMember(st.BridgeName, true) {
		t.Errorf("CPU is NOT tagged in VLAN 1: %v", row.TaggedMembers)
	}

	// ApplyPorts gesture: placement moving sfp-sfpplus2 out of VLAN 1.
	var managed = map[string]bool{"sfp-sfpplus2": true}
	var newUntagged = mergeMembers(membersOf(st, 1, false), nil, managed)
	var member string
	for _, member = range newUntagged {
		if member == st.BridgeName {
			t.Fatal("membersOf must not return CPU pseudo-port: it is reinjected " +
				"separately, so its survival never depends on list comparison")
		}
	}
	_, newUntagged = placeCPU(st.BridgeName, 1, 99, st, row, nil, newUntagged)
	var foundCPU bool
	for _, member = range newUntagged {
		if member == st.BridgeName {
			foundCPU = true
		}
	}
	if !foundCPU {
		t.Errorf("CPU disappeared from VLAN 1 UNTAGGED list: untagged administration "+
			"would be closed by a successful apply. List obtained: %v", newUntagged)
	}
}

// TestMembersReadFromRowNotReconstructed — the second member loss.
//
// The `have` list merge starts from was reconstructed from the per-PORT view, which contains
// only interfaces present in `/interface/bridge/port` for the retained bridge. Any other
// member — an interface removed from the bridge but left in a list, an aggregate the read did
// not attach — was not in `have`, therefore disappeared on first `set`. This whole file exists
// to NOT lose members.
func TestMembersReadFromRowNotReconstructed(t *testing.T) {
	var st = stateFromFixtures(t)
	// A member the bridge port table does not know: exactly the case that disappeared.
	var i int
	for i = range st.VLANs {
		if st.VLANs[i].ID == 99 {
			st.VLANs[i].TaggedMembers = append(st.VLANs[i].TaggedMembers, "bond1")
		}
	}
	var _, known = st.Ports["bond1"]
	if known {
		t.Fatal("prerequisite: bond1 must not be a bridge port in the capture")
	}

	var got = membersOf(st, 99, true)
	var expected = []string{"bond1", "sfp-sfpplus2"}
	if !reflect.DeepEqual(got, expected) {
		t.Errorf("VLAN 99 tagged members = %v, expected %v: a member absent from the "+
			"bridge port table must still be seen, or first placement erases it",
			got, expected)
	}
	// And it is NOT managed by meshcraft, so merge preserves it.
	var after = mergeMembers(got, []string{"sfp-sfpplus2"},
		map[string]bool{"sfp-sfpplus2": true})
	if !reflect.DeepEqual(after, expected) {
		t.Errorf("merge = %v, expected %v: bond1 is not managed, it stays", after, expected)
	}
}

// TestMultiVLANRowIsNotWritten — the third, and the most insidious.
//
// `tagged` and `untagged` lists belong to the RECORD, not to each VLAN it declares. A row
// `vlan-ids: "10-20"` declares eleven and has only one set of lists: writing membership for
// VLAN 10 alone would rewrite all eleven's membership in one `set`, without error and without
// trace. meshcraft reasons per VLAN and cannot compose membership common to eleven VLANs: the
// only honest gesture is to refuse.
func TestMultiVLANRowIsNotWritten(t *testing.T) {
	var rows = []Row{{
		".id": "*9", "bridge": "bridge", "vlan-ids": "10-12",
		"tagged": "sfp-sfpplus2", "untagged": "", "dynamic": "false",
	}}
	var vlans, _, _ = foldVLANs(rows, fixture(t, "interface_bridge_port.json"), "bridge")

	if len(vlans) != 3 {
		t.Fatalf("%d entries read, expected 3 (VLAN 10, 11, 12): %+v", len(vlans), vlans)
	}
	var v BridgeVLAN
	for _, v = range vlans {
		if len(v.IDs) != 3 {
			t.Errorf("VLAN %d: IDs = %v, expected all three identifiers from the row — "+
				"that is what lets placement see it would overflow", v.ID, v.IDs)
		}
		var why = whyRowIsUnwritable(v)
		if why == "" {
			t.Fatalf("VLAN %d: this row's membership cannot be written without touching the "+
				"other two, and refusal must be explicit", v.ID)
		}
		var expected string
		for _, expected = range []string{"*9", "10, 11, 12", "split"} {
			if !strings.Contains(why, expected) {
				t.Errorf("refusal must contain %q; got: %q", expected, why)
			}
		}
	}

	// A single-VLAN row writes without reservation: the normal case, and refusing there would
	// make the connector useless.
	var st = stateFromFixtures(t)
	var row, found = vlanRowFor(st, 100)
	if !found {
		t.Fatal("prerequisite: VLAN 100 is static on the reference device")
	}
	if whyRowIsUnwritable(row) != "" {
		t.Errorf("single-VLAN row must be writable: %q",
			whyRowIsUnwritable(row))
	}
}

// --- Hardware offload, observed not decided ---

// TestHardwareOffloadSignaledOnlyWhenPartiallyLost
//
// `hw-offload` is a RESULT, not a setting: RouterOS clears it by itself when bridge
// configuration requires the CPU to see frames, and throughput on a 24-port gigabit switch
// collapses without anything signaling it.
//
// Comparison is RELATIVE, and that is what makes it usable: a device with NO offloaded port is
// probably a model without switching ASIC, and crying wolf there would cry permanently.
func TestHardwareOffloadSignaledOnlyWhenPartiallyLost(t *testing.T) {
	var st = stateFromFixtures(t)

	// Reference device: all 26 ports offloaded. Nothing to say.
	if hwOffloadNote(st) != "" {
		t.Errorf("all 26 capture ports are offloaded (observed `hw-offload: true`): "+
			"nothing must be signaled. Got: %q", hwOffloadNote(st))
	}

	// Two ports lose offload: that is the case an operator wants to see.
	var p = st.Ports["ether3"]
	p.HWOffload = false
	st.Ports["ether3"] = p
	p = st.Ports["ether10"]
	p.HWOffload = false
	st.Ports["ether10"] = p

	var got = hwOffloadNote(st)
	var expected string
	for _, expected = range []string{"HARDWARE OFFLOAD LOST", "ether3, ether10",
		"RESULT", "other 24"} {
		if !strings.Contains(got, expected) {
			t.Errorf("note must contain %q; got: %q", expected, got)
		}
	}

	// No offloaded port at all: silence. Bridge without ASIC, or offload disabled.
	var name string
	for name = range st.Ports {
		p = st.Ports[name]
		p.HWOffload = false
		st.Ports[name] = p
	}
	if hwOffloadNote(st) != "" {
		t.Errorf("no offloaded port: probably a model without switching ASIC, "+
			"crying wolf would cry permanently. Got: %q", hwOffloadNote(st))
	}
}

// --- Bridge PVID, without which a factory device was unconfigurable ---

// TestUntaggedAdministrationOnBridgeRecognized
//
// When the address is carried by the bridge itself, administration arrives UNTAGGED and the
// VLAN concerned is the bridge PVID. Deduction looked in `Ports[BridgeName]` — yet the bridge
// is never a `/interface/bridge/port` record, so that map never contains its name. MgmtVLAN
// stayed at 0, and placement refused to write « unknown management VLAN » on exactly the most
// common case: a RouterOS out of the box.
func TestUntaggedAdministrationOnBridgeRecognized(t *testing.T) {
	var bridges = fixture(t, "interface_bridge.json")
	var pvid, ok = bridges[0].Int("pvid")
	if !ok || pvid != 1 {
		t.Fatalf("prerequisite: `/interface/bridge` serves `pvid` (observed \u00ab 1 \u00bb); "+
			"got %d, readable=%t", pvid, ok)
	}

	var st = DeviceState{
		BridgeName: "bridge",
		BridgePVID: pvid,
		Ports:      map[string]BridgePort{},
	}
	// Device has ONLY its factory address, on the bridge.
	foldMgmt([]Row{{
		"address": "192.168.88.1/24", "interface": "bridge",
		"actual-interface": "bridge", "comment": "defconf",
	}}, nil, &st)

	if st.MgmtVLAN != 1 {
		t.Errorf("management VLAN = %d, expected 1 (bridge PVID): without this "+
			"deduction, placement refuses to write on every factory device", st.MgmtVLAN)
	}
	if st.MgmtInterface != "bridge" {
		t.Errorf("management interface = %q, expected \u00ab bridge \u00bb", st.MgmtInterface)
	}
}

// --- Removal, which was IMPOSSIBLE not refused ---

// TestVLANDroppedByPlanEntersLoop — the missing half of convergence.
//
// On RouterOS, removal is not written: it is PRODUCED when one rewrites a complete list
// without putting a member back. A loop visiting only intent VLANs therefore NEVER rewrites
// the list of a VLAN intent no longer mentions — the member stayed forever, with no message.
//
// The concrete case, and it is not rare: an access port moved from VLAN 99 to VLAN 100. Its
// `pvid` moves, it enters VLAN 100's `untagged` list, and it STAYS in VLAN 99's — untagged
// member of two VLANs, which RouterOS accepts and nothing defines.
func TestVLANDroppedByPlanEntersLoop(t *testing.T) {
	var st = stateFromFixtures(t)
	// ether5 is untagged member of VLAN 50 and tagged member of VLAN 60: neither is requested
	// by the plan below.
	var p = st.Ports["ether5"]
	p.Untagged = []int{50}
	p.Tagged = []int{60}
	st.Ports["ether5"] = p

	var desired = map[string]PortState{"ether5": {Mode: "access", PVID: 100}}
	var managed = map[string]bool{"ether5": true}
	var got = withStaleVLANs(membershipFor(desired), st, managed, managed)

	var vlans []int
	var m membership
	for _, m = range got {
		vlans = append(vlans, m.VLAN)
	}
	if !reflect.DeepEqual(vlans, []int{50, 60, 100}) {
		t.Fatalf("VLANs visited = %v, expected [50 60 100]: 100 from plan, 50 and 60 are what "+
			"the port still carries and the plan no longer requests", vlans)
	}
	// Added entries request NO member: that is what makes mergeMembers remove managed members
	// and preserve others.
	for _, m = range got {
		if m.VLAN == 100 {
			continue
		}
		if !isStale(m) {
			t.Errorf("VLAN %d: a removal entry requests no member; got tagged=%v untagged=%v",
				m.VLAN, m.Tagged, m.Untagged)
		}
	}
	// And removal does happen: merge on a list with no wanted member removes managed port and
	// keeps others.
	var after = mergeMembers([]string{"ether5", "ether9"}, nil, managed)
	if !reflect.DeepEqual(after, []string{"ether9"}) {
		t.Errorf("merge = %v, expected [ether9]: ether5 is managed and no longer wanted, "+
			"ether9 is not managed and stays", after)
	}
}

// TestResolvedAllYieldsStaleTaggedVLAN: resolved « all » is an exact site set —
// tagged VLANs the site no longer declares become stale and apply removes them.
func TestResolvedAllYieldsStaleTaggedVLAN(t *testing.T) {
	var st = stateFromFixtures(t)
	var p = st.Ports["sfp-sfpplus2"]
	p.Untagged = []int{50}
	p.Tagged = []int{60, 70}
	st.Ports["sfp-sfpplus2"] = p

	var desired = map[string]PortState{
		"sfp-sfpplus2": {Mode: "trunk", PVID: 1, Tagged: []int{99}},
	}
	var got = withStaleVLANs(membershipFor(desired), st,
		map[string]bool{"sfp-sfpplus2": true},
		map[string]bool{"sfp-sfpplus2": true})

	var stale = map[int]bool{}
	var m membership
	for _, m = range got {
		if isStale(m) {
			stale[m.VLAN] = true
		}
	}
	if !stale[50] || !stale[60] || !stale[70] {
		t.Errorf("stale = %v, expected 50 (untagged) and 60,70 (tagged extras)", stale)
	}
	if stale[99] || stale[1] {
		t.Errorf("wanted VLANs must not be stale: %v", stale)
	}
}

// TestUnresolvedAllYieldsNoStaleTaggedVLAN: unresolved « all » must not strip
// tagged membership until the site plan resolves.
func TestUnresolvedAllYieldsNoStaleTaggedVLAN(t *testing.T) {
	var st = stateFromFixtures(t)
	var p = st.Ports["sfp-sfpplus2"]
	p.Untagged = []int{50}
	p.Tagged = []int{60, 70}
	st.Ports["sfp-sfpplus2"] = p

	var desired = map[string]PortState{
		"sfp-sfpplus2": {Mode: "trunk", PVID: 1, Tagged: []int{99}, TaggedAtLeast: true},
	}
	// `managed` carries the port, `managedTagged` does not: unresolved « all ».
	var got = withStaleVLANs(membershipFor(desired), st,
		map[string]bool{"sfp-sfpplus2": true}, map[string]bool{})

	var vlans []int
	var m membership
	for _, m = range got {
		vlans = append(vlans, m.VLAN)
	}
	// 50 is stale — it is UNTAGGED. 60 and 70 are tagged on an unresolved « all »
	// port: untouchable until the site plan resolves.
	if !reflect.DeepEqual(vlans, []int{1, 50, 99}) {
		t.Errorf("VLANs visited = %v, expected [1 50 99]: tagged VLANs on unresolved "+
			"\u00ab all \u00bb are never held stale, its untagged one is", vlans)
	}
}

// TestUnmanagedPortHasNoStaleVLAN: « absent = unmanaged ».
//
// meshcraft does not claim ownership of what it never placed. A port left « free » in the plan
// must trigger NO removal, or first placement would erase configuration an operator made by hand.
func TestUnmanagedPortHasNoStaleVLAN(t *testing.T) {
	var st = stateFromFixtures(t)
	var p = st.Ports["ether9"]
	p.Untagged = []int{50}
	p.Tagged = []int{60}
	st.Ports["ether9"] = p

	var got = withStaleVLANs(nil, st, map[string]bool{}, map[string]bool{})
	if len(got) != 0 {
		t.Errorf("VLANs visited = %+v, expected none: ether9 is not managed", got)
	}
}

// TestManagementRemovalRefusalIsStated
//
// A management VLAN never loses a member without force, and that is the right tradeoff. But it
// leaves a port actually untagged member of TWO VLANs — the plan's and the old administration
// VLAN — i.e. administration VLAN leaking onto an access port. That came out as a TRANSCRIPT
// line, which the screen shows only when expanding placement detail.
func TestManagementRemovalRefusalIsStated(t *testing.T) {
	// Exact ApplyPorts calculation on a protected VLAN: ether5 is untagged member of 99, plan
	// no longer requests it, merge therefore removed it — and union puts it back.
	var have = []string{"ether5", "ether9"}
	var fusion = mergeMembers(have, nil, map[string]bool{"ether5": true})
	if !reflect.DeepEqual(fusion, []string{"ether9"}) {
		t.Fatalf("prerequisite: merge removes ether5; got %v", fusion)
	}
	var kept = union(have, fusion)
	var refuses = lacking(kept, fusion)
	if !reflect.DeepEqual(refuses, []string{"ether5"}) {
		t.Errorf("refused removals = %v, expected [ether5]: that is what must be SAID to the "+
			"operator, not only logged to transcript", refuses)
	}
	// Nothing to say when nothing was refused: a banner that speaks for nothing teaches one not
	// to read it.
	if len(lacking(fusion, fusion)) != 0 {
		t.Error("no refused removal: nothing to say")
	}
}
