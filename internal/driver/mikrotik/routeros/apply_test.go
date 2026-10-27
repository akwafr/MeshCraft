package routeros

import (
	"fmt"
	"reflect"
	"testing"
)

// Apply tests.
//
// They exercise the LOGIC of writes against real states captured in testdata/, not
// the device's reaction: no write could be exercised on the reference hardware (channel
// restricted to GET/HEAD). This is therefore where everything that can be verified
// without writing is verified — and that is a lot, because the faults that cut access
// on this platform are faults of LIST calculation, made before the first byte leaves.

// TestPortToVLANPivot: meshcraft reasons by port, RouterOS by VLAN.
//
// The difference from the Instant On connector: PVID here engenders an explicit
// UNTAGGED membership. On RouterOS the two are independent, and setting a PVID
// without setting membership gives a port that tags its incoming traffic into a VLAN
// it does not egress — the exact defect on the reference device, which a correct apply
// must not reproduce.
func TestPortToVLANPivot(t *testing.T) {
	var desired = map[string]PortState{
		"ether1":       {Mode: "access", PVID: 20},
		"ether2":       {Mode: "access", PVID: 20},
		"ether3":       {Mode: "trunk", PVID: 1, Tagged: []int{20, 30}},
		"sfp-sfpplus1": {Mode: "libre"}, // unmanaged: ignored
		"sfp-sfpplus2": {},              // no mode: ignored
	}
	var got = membershipFor(desired)

	if len(got) != 3 {
		t.Fatalf("%d VLANs involved, 3 expected (1, 20, 30): %+v", len(got), got)
	}
	// VLAN 1: ether3's native.
	if got[0].VLAN != 1 || !reflect.DeepEqual(got[0].Untagged, []string{"ether3"}) {
		t.Errorf("VLAN 1 = %+v, expected ether3 untagged", got[0])
	}
	// VLAN 20: ether1 and ether2 UNTAGGED (PVID engenders membership),
	// ether3 tagged.
	if got[1].VLAN != 20 {
		t.Fatalf("second VLAN = %d, expected 20", got[1].VLAN)
	}
	if !reflect.DeepEqual(got[1].Untagged, []string{"ether1", "ether2"}) {
		t.Errorf("VLAN 20 untagged = %v, expected [ether1 ether2] — a PVID must "+
			"engender untagged membership, otherwise the port tags into a VLAN it does "+
			"not egress", got[1].Untagged)
	}
	if !reflect.DeepEqual(got[1].Tagged, []string{"ether3"}) {
		t.Errorf("VLAN 20 tagged = %v, expected [ether3]", got[1].Tagged)
	}
	// Unmanaged ports appear NOWHERE: 'absent = unmanaged'.
	var m membership
	for _, m = range got {
		var name string
		for _, name = range append(append([]string{}, m.Tagged...), m.Untagged...) {
			if name == "sfp-sfpplus1" || name == "sfp-sfpplus2" {
				t.Errorf("unmanaged port %q appears in VLAN %d: meshcraft claims nothing "+
					"on a port it does not manage", name, m.VLAN)
			}
		}
	}
}

// TestNativeVLANIsNotAlsoTagged: native wins.
//
// RouterOS would accept both lists, and the port would then receive the same VLAN in
// two forms — undefined behavior, which we do not write.
func TestNativeVLANIsNotAlsoTagged(t *testing.T) {
	var got = membershipFor(map[string]PortState{
		"ether5": {Mode: "trunk", PVID: 10, Tagged: []int{10, 20}},
	})
	var m membership
	for _, m = range got {
		if m.VLAN != 10 {
			continue
		}
		if len(m.Tagged) != 0 {
			t.Errorf("VLAN 10: ether5 is native there, it must not ALSO be tagged "+
				"(got tagged=%v)", m.Tagged)
		}
		if !reflect.DeepEqual(m.Untagged, []string{"ether5"}) {
			t.Errorf("VLAN 10 untagged = %v, expected [ether5]", m.Untagged)
		}
	}
}

// TestMergePreservesWhatMeshcraftDoesNotManage — THE test of this file.
//
// RouterOS does not accept a delta: writing VLAN membership means replacing the
// ENTIRE list. Yet that list contains members meshcraft does not manage — a port left
// 'free' in the plan, a port added manually by the operator, an interface that is
// not a front-panel port.
//
// Keeping only what meshcraft wants would be the naive behavior, and it would destroy
// these members on EVERY apply: every port configured outside meshcraft would disappear
// from its VLANs on the first apply. That is the translation, on a full-list write, of
// the 'absent = unmanaged' rule.
func TestMergePreservesWhatMeshcraftDoesNotManage(t *testing.T) {
	// The device carries three members; meshcraft manages only two.
	var have = []string{"ether1", "ether2", "ether9"}
	var managed = map[string]bool{"ether1": true, "ether2": true}
	// meshcraft wants ether1 only: ether2 must leave, ether9 must STAY.
	var got = mergeMembers(have, []string{"ether1"}, managed)

	var expected = []string{"ether1", "ether9"}
	if !reflect.DeepEqual(got, expected) {
		t.Errorf("merge = %v, expected %v.\n"+
			"  ether2 is managed and no longer wanted: it leaves, that is correct.\n"+
			"  ether9 is NOT managed by meshcraft: losing it would erase an operator's "+
			"configuration on every apply.", got, expected)
	}
}

func TestMergeAddsWithoutDuplicate(t *testing.T) {
	var got = mergeMembers([]string{"ether1"}, []string{"ether1", "ether2"},
		map[string]bool{"ether1": true, "ether2": true})
	if !reflect.DeepEqual(got, []string{"ether1", "ether2"}) {
		t.Errorf("merge = %v, expected [ether1 ether2] without duplicate", got)
	}
}

// TestCPUIsEnsuredOnMgmtVLAN — the gravest fault on the platform, locked down.
//
// Losing the pseudo-port `bridge` in the management VLAN does NOT cut site traffic:
// ports keep switching between each other. But administration is closed permanently.
// Preservation alone is not enough: apply must SET the CPU on the plan management
// VLAN even when it was absent (new site VLAN, incomplete factory state).
func TestCPUIsEnsuredOnMgmtVLAN(t *testing.T) {
	var st = stateFromFixtures(t)
	if !st.CPUCarries(st.MgmtVLAN) {
		t.Fatalf("test prerequisite: the CPU must be tagged in management VLAN %d on "+
			"the reference device", st.MgmtVLAN)
	}
	var entry, ok = vlanRowFor(st, 99)
	if !ok {
		t.Fatal("VLAN 99 row missing from fixtures")
	}

	// Apply that empties facade members of VLAN 99 — without placeCPU the CPU vanishes.
	var managed = map[string]bool{"sfp-sfpplus2": true}
	var haveTagged = membersOf(st, 99, true)
	var newTagged = mergeMembers(haveTagged, nil, managed)
	var member string
	for _, member = range newTagged {
		if member == st.BridgeName {
			t.Fatal("membersOf must not return the CPU: placeCPU re-injects it")
		}
	}
	newTagged, _ = placeCPU(st.BridgeName, 99, 99, st, entry, newTagged, nil)
	var found bool
	for _, member = range newTagged {
		if member == st.BridgeName {
			found = true
		}
	}
	if !found {
		t.Errorf("the CPU (%q) disappeared from VLAN 99's tagged list: %v",
			st.BridgeName, newTagged)
	}

	// ENSURE when absent: a VLAN row with no CPU yet still receives bridge in tagged
	// for a tagged plan management VLAN.
	var empty = BridgeVLAN{ID: 99, TaggedMembers: nil, UntaggedMembers: nil}
	var ensured, _ = placeCPU(st.BridgeName, 99, 99, DeviceState{
		BridgeName: "bridge", MgmtInterface: "vlan99", BridgePVID: 1,
	}, empty, []string{"sfp-sfpplus2"}, nil)
	if !reflect.DeepEqual(ensured, []string{"bridge", "sfp-sfpplus2"}) &&
		!(len(ensured) == 2 && ensured[0] == "sfp-sfpplus2" && ensured[1] == "bridge") {
		// insertOnce appends; order is facade first then bridge.
		var hasCPU bool
		for _, member = range ensured {
			if member == "bridge" {
				hasCPU = true
			}
		}
		if !hasCPU {
			t.Errorf("CPU must be SET on plan management VLAN when absent: %v", ensured)
		}
	}

	// Untagged administration (address on the bridge, plan = bridge PVID): ensure untagged.
	var _, untagged = placeCPU("bridge", 1, 1, DeviceState{
		BridgeName: "bridge", MgmtInterface: "bridge", BridgePVID: 1,
	}, BridgeVLAN{ID: 1}, nil, []string{"ether1"})
	var hasUntaggedCPU bool
	for _, member = range untagged {
		if member == "bridge" {
			hasUntaggedCPU = true
		}
	}
	if !hasUntaggedCPU {
		t.Errorf("untagged admin must SET CPU in untagged list: %v", untagged)
	}
}

// TestUnionRemovesNothing: what protects management VLANs.
//
// On a management VLAN, apply only ADDS. Removing a member from the VLAN through
// which administration arrives is a gesture whose benefit is nil and whose risk is
// maximal.
func TestUnionRemovesNothing(t *testing.T) {
	var got = union([]string{"bridge", "sfp-sfpplus2"}, []string{"ether7"})
	var expected = []string{"bridge", "ether7", "sfp-sfpplus2"}
	if !reflect.DeepEqual(got, expected) {
		t.Errorf("union = %v, expected %v: no existing member must leave a management "+
			"VLAN", got, expected)
	}
}

// TestPortWithoutLinkIsNotProtected: the safeguard must stay USABLE.
//
// On a new device, all ports are in the default VLAN which is also the administration
// VLAN. If the safeguard applied to all, 'adopt' would mean nothing — no port would
// ever be modifiable. The LINK condition is what makes it practical: a port without a
// cable cannot carry any session.
//
// The reference device's real state is the ideal case to show it: `sfp-sfpplus2`
// carries a link, the 24 copper ports do not.
func TestPortWithoutLinkIsNotProtected(t *testing.T) {
	var st = stateFromFixtures(t)

	// The uplink port carries a link AND its PVID (1) is the factory untagged
	// management VLAN: that is the one we protect.
	if !st.LinkUp["sfp-sfpplus2"] {
		t.Error("prerequisite: sfp-sfpplus2 carries a link on the reference device")
	}
	// Copper ports carry none: they stay modifiable, otherwise a new device would be
	// impossible to configure.
	var name string
	for _, name = range []string{"ether1", "ether12", "ether24"} {
		if st.LinkUp[name] {
			t.Errorf("%s does not carry a link on the reference device (observed "+
				"`running: false`): the safeguard must not apply there", name)
		}
	}
}

// TestDynamicEntryIsNeverModified
//
// RouterOS fabricates VLAN entries on its own (observed: VLAN 1, `dynamic: true`,
// comment 'added by pvid'). They have no stable `.id`, are not modifiable, and
// disappear when their reason for being disappears. Trying to write them fails without
// the message saying why; apply therefore creates a STATIC entry alongside.
func TestDynamicEntryIsNeverModified(t *testing.T) {
	var st = stateFromFixtures(t)

	var _, found = vlanRowFor(st, 1)
	if found {
		t.Error("VLAN 1 is dynamic on the reference device: vlanRowFor must not offer " +
			"it as a write target")
	}
	// Static VLANs, on the other hand, are modifiable and carry their `.id`.
	var row BridgeVLAN
	row, found = vlanRowFor(st, 100)
	if !found || row.RowID == "" {
		t.Errorf("VLAN 100 is static (observed `dynamic: false`) and must be modifiable: "+
			"got %+v (found=%t)", row, found)
	}
}

// TestVLANMembersInRealState: the read that feeds the merge.
func TestVLANMembersInRealState(t *testing.T) {
	var st = stateFromFixtures(t)

	// VLAN 99: only sfp-sfpplus2 is tagged on the port side (the CPU is excluded).
	var tagged = membersOf(st, 99, true)
	if !reflect.DeepEqual(tagged, []string{"sfp-sfpplus2"}) {
		t.Errorf("VLAN 99 tagged members = %v, expected [sfp-sfpplus2] (the CPU "+
			"pseudo-port is deliberately excluded)", tagged)
	}
	// VLAN 100: sfp-sfpplus2 tagged, and NO untagged members — the real anomaly on
	// this device, whose 24 copper ports have pvid 100 without being members.
	var untagged = membersOf(st, 100, false)
	if len(untagged) != 0 {
		t.Errorf("VLAN 100 untagged members = %v, expected none: the capture shows an "+
			"empty `untagged` list while 24 ports have their PVID there", untagged)
	}
}

func TestListComparison(t *testing.T) {
	if !sameMembers([]string{"a", "b"}, []string{"a", "b"}) {
		t.Error("two identical lists must be equal")
	}
	if sameMembers([]string{"a"}, []string{"a", "b"}) {
		t.Error("lists of different lengths are not equal")
	}
	if sameMembers([]string{"a", "b"}, []string{"a", "c"}) {
		t.Error("lists of different contents are not equal")
	}
	// The case that matters for apply: two equal lists avoid a round trip.
	if !sameMembers(nil, nil) {
		t.Error("two empty lists must be equal")
	}
}

// TestNothingWrittenWithoutIdentifier: a `set` without `.id` would apply to something
// other than what we think.
//
// On a VLAN or bridge-port table, that means touching the wrong port. The refusal is
// therefore in the client, before the request, and not left to the device.
func TestNothingWrittenWithoutIdentifier(t *testing.T) {
	var c = &Client{}
	var _, err = c.Set(nil, "interface/bridge/vlan", "", map[string]string{"tagged": "ether1"})
	if err == nil {
		t.Error("a `set` without .id must be refused BEFORE being sent")
	}
	_, err = c.Remove(nil, "user", "  ")
	if err == nil {
		t.Error("a `remove` without .id must be refused BEFORE being sent")
	}
}

func TestIsVLANAlreadyAdded(t *testing.T) {
	var err = fmt.Errorf("interface/bridge/vlan/add refused: request refused: 400 Bad Request: failure: vlan already added")
	if !isVLANAlreadyAdded(err) {
		t.Fatal("expected already-added")
	}
	if isVLANAlreadyAdded(fmt.Errorf("failure: invalid value")) {
		t.Fatal("other failures must still abort create")
	}
	if isVLANAlreadyAdded(nil) {
		t.Fatal("nil")
	}
}

// TestWritableExtrasSTP: spanning-tree is applied via ApplySpanningTree.
func TestWritableExtrasSTP(t *testing.T) {
	if !WritesExtra("stp") {
		t.Fatal("WritesExtra(stp) must be true once ApplySpanningTree exists")
	}
	var name string
	for _, name = range []string{"poe", "port_security"} {
		if WritesExtra(name) {
			t.Errorf("WritesExtra(%q) = true without apply code", name)
		}
	}
}
