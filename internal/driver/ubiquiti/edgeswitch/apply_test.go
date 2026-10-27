package edgeswitch

import "testing"

// TestMgmtGuardBlocksIsLinkGated: the whole point of the fix. A port that
// would stop carrying the management VLAN is refused ONLY while it carries
// an active link — the case where changing it could plausibly cut the
// session doing the writing. A port with nothing plugged in cannot carry
// that session, and blocking it anyway would refuse provisioning a device
// before it is cabled, which is exactly when adoption needs to write it.
func TestMgmtGuardBlocksIsLinkGated(t *testing.T) {
	var have = PortState{Mode: "access", PVID: 99}  // carries mgmt VLAN 99
	var want = PortState{Mode: "access", PVID: 100} // would stop carrying it

	if !mgmtGuardBlocks(have, want, 99, true, false) {
		t.Error("a LINKED port losing the management VLAN must be blocked")
	}
	if mgmtGuardBlocks(have, want, 99, false, false) {
		t.Error("an UNLINKED port losing the management VLAN must NOT be blocked — " +
			"nothing plugged in means no session to cut")
	}
}

func TestMgmtGuardBlocksRespectsForceAndNoOpMgmt(t *testing.T) {
	var have = PortState{Mode: "access", PVID: 99}
	var want = PortState{Mode: "access", PVID: 100}

	if mgmtGuardBlocks(have, want, 99, true, true) {
		t.Error("force must lift the guardrail even on a linked port")
	}
	if mgmtGuardBlocks(have, want, 0, true, false) {
		t.Error("mgmt <= 0 means the caller named no management VLAN: nothing to protect")
	}
}

func TestMgmtGuardBlocksOnlyWhenMembershipIsActuallyLost(t *testing.T) {
	var have = PortState{Mode: "trunk", PVID: 1, Tagged: []int{99}}
	// Still tagged in 99 afterward: the port never stops carrying it.
	var stillCarries = PortState{Mode: "trunk", PVID: 1, Tagged: []int{99, 100}}
	if mgmtGuardBlocks(have, stillCarries, 99, true, false) {
		t.Error("a port that keeps carrying the management VLAN must not be blocked")
	}
}

// TestStaleMembershipDropsWhatAdoptionLeftBehind reproduces the fault
// reported live on 192.0.2.4: ports provisioned trunk-tagged into VLAN 99 at
// adoption, desired as plain access on an unrelated VLAN, kept reading back
// "trunk, tagged 99" through every apply because PortCmds only ever adds.
func TestStaleMembershipDropsWhatAdoptionLeftBehind(t *testing.T) {
	var have = PortState{Mode: "trunk", PVID: 100, Tagged: []int{99}}
	var want = PortState{Mode: "access", PVID: 100}
	var stale = staleMembership(have, want)
	if len(stale) != 1 || stale[0] != 99 {
		t.Errorf("stale = %v, want [99]", stale)
	}
}

func TestStaleMembershipIncludesTheOldNativeVLAN(t *testing.T) {
	var have = PortState{Mode: "access", PVID: 1}
	var want = PortState{Mode: "access", PVID: 100}
	var stale = staleMembership(have, want)
	if len(stale) != 1 || stale[0] != 1 {
		t.Errorf("a pvid change must exclude the OLD native VLAN's participation: got %v, want [1]", stale)
	}
}

func TestStaleMembershipKeepsWhatIsStillWanted(t *testing.T) {
	// The old native VLAN survives as a TAGGED member of the new state: must
	// not be excluded.
	var have = PortState{Mode: "access", PVID: 1}
	var want = PortState{Mode: "trunk", PVID: 100, Tagged: []int{1, 42}}
	var stale = staleMembership(have, want)
	if len(stale) != 0 {
		t.Errorf("VLAN 1 is still (tagged) wanted: must not be excluded, got %v", stale)
	}
}

func TestStaleMembershipNothingToDrop(t *testing.T) {
	var have = PortState{Mode: "access", PVID: 100}
	var want = PortState{Mode: "access", PVID: 100}
	var stale = staleMembership(have, want)
	if len(stale) != 0 {
		t.Errorf("identical states: nothing to drop, got %v", stale)
	}
}

func TestRemoveTaggedCmds(t *testing.T) {
	var got = RemoveTaggedCmds("0/3", []int{99, 42})
	var want = []string{
		"interface 0/3",
		"vlan participation exclude 99",
		"vlan participation exclude 42",
		"exit",
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	var i int
	for i = range want {
		if got[i] != want[i] {
			t.Errorf("line %d: got %q, want %q", i, got[i], want[i])
		}
	}
	if RemoveTaggedCmds("0/3", nil) != nil {
		t.Error("nothing to remove must produce no command at all — no empty interface block")
	}
}
