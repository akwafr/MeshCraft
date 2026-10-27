package procurve

import "testing"

// TestMgmtGuardBlocksIsLinkGatedAndForceable is the whole point of this fix:
// before it, ApplyPorts refused to move the management VLAN off a port
// UNCONDITIONALLY — no link check, and no way to force it. Of every connector
// in the fleet this was the one where the product's own "force anyway" button
// silently did nothing.
func TestMgmtGuardBlocksIsLinkGatedAndForceable(t *testing.T) {
	if !mgmtGuardBlocks(true, 99, true, false) {
		t.Error("a LINKED port losing the management VLAN must be blocked without force")
	}
	if mgmtGuardBlocks(true, 99, false, false) {
		t.Error("an UNLINKED port losing the management VLAN must NOT be blocked — " +
			"nothing plugged in means no session to cut")
	}
	if mgmtGuardBlocks(true, 99, true, true) {
		t.Error("force must lift the guardrail even on a linked port")
	}
}

func TestMgmtGuardBlocksNoOpWhenNothingIsLost(t *testing.T) {
	if mgmtGuardBlocks(false, 99, true, false) {
		t.Error("a port that does not lose the management VLAN must not be blocked")
	}
	if mgmtGuardBlocks(true, 0, true, false) {
		t.Error("mgmt <= 0 means the caller named no management VLAN: nothing to protect")
	}
}
