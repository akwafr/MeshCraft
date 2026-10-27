package s3400

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
