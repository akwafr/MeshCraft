package eos

import (
	"reflect"
	"strings"
	"testing"

	"meshcraft/internal/driver"
)

// --- the trunk write ---

// The SET form is the only one that RESTRICTS, and it is idempotent — which
// "add" on an all-allowed trunk is not.
func TestPortCmdsExactTrunkUsesTheSetForm(t *testing.T) {
	var want = PortState{Mode: "trunk", PVID: 99, Tagged: []int{30, 40}}
	var now = PortState{Mode: "access", PVID: 1}
	var cmds = portCmds("ET49", want, now, false)
	if idx(cmds, "interface Ethernet49") != 0 {
		t.Fatalf("cmds = %v", cmds)
	}
	// The native VLAN is ALWAYS in the allowed list: EOS drops native traffic on
	// a trunk whose allowed list omits it, producing a port that looks configured
	// and forwards nothing.
	if idx(cmds, "switchport trunk allowed vlan 30,40,99") < 0 {
		t.Errorf("cmds = %v — the exact list must contain the native VLAN", cmds)
	}
	if idx(cmds, "switchport trunk native vlan 99") < 0 {
		t.Errorf("cmds = %v", cmds)
	}
	// There is no trunk encapsulation on this platform, and sending the IOS line
	// would be a refused command in the middle of a block.
	if idxPrefix(cmds, "switchport trunk encapsulation") >= 0 {
		t.Error("\"switchport trunk encapsulation\" was written: EOS speaks 802.1Q only " +
			"and has no such command")
	}
	if cmds[len(cmds)-1] != "exit" {
		t.Errorf("the interface block is not closed: %v", cmds)
	}
}

// A TaggedAtLeast intent never restricts. On an all-allowed trunk — everything
// is already allowed — it must write NO allowed line at all, or a no-op "add"
// would be issued on every apply for ever.
func TestPortCmdsAtLeastOnAnAllAllowedTrunkAddsNothing(t *testing.T) {
	var want = PortState{Mode: "trunk", PVID: 1, Tagged: []int{30}, TaggedAtLeast: true}
	var now = PortState{Mode: "trunk", PVID: 1}
	var cmds = portCmds("ET1", want, now, true)
	if idxPrefix(cmds, "switchport trunk allowed") >= 0 {
		t.Errorf("cmds = %v — everything is already allowed; adding is a no-op the "+
			"re-read cannot see", cmds)
	}
}

func TestPortCmdsAccess(t *testing.T) {
	var cmds = portCmds("ET3", PortState{Mode: "access", PVID: 30}, PortState{}, false)
	var want = []string{
		"interface Ethernet3", "switchport mode access", "switchport access vlan 30", "exit",
	}
	if !reflect.DeepEqual(cmds, want) {
		t.Errorf("cmds = %v, want %v", cmds, want)
	}
}

// --- convergence ---

// An all-allowed trunk satisfies any AT-LEAST intent (everything is allowed) and
// satisfies NO exact intent (it allows more than was asked). Reading it as an
// empty tagged list inverts both answers.
func TestPortConvergedHandlesTheAllAllowedTrunk(t *testing.T) {
	var atLeast = PortState{Mode: "trunk", PVID: 1, Tagged: []int{30}, TaggedAtLeast: true}
	if !portConverged(atLeast, PortState{Mode: "trunk", PVID: 1}, true) {
		t.Error("an at-least intent is satisfied by a trunk that allows everything")
	}
	var exact = PortState{Mode: "trunk", PVID: 1, Tagged: []int{30}}
	if portConverged(exact, PortState{Mode: "trunk", PVID: 1}, true) {
		t.Error("an exact intent is NOT satisfied by a trunk that allows everything: " +
			"the port carries more than was asked, and only the SET form fixes it")
	}
}

func TestPortConvergedExactComparesTheNativeInclusiveList(t *testing.T) {
	var want = PortState{Mode: "trunk", PVID: 99, Tagged: []int{30}}
	// The device's list contains the native VLAN; meshcraft's model carries it as
	// PVID, and the parser removes it from Tagged. Convergence must compare the
	// two the same way round, or an exactly-applied trunk drifts for ever.
	var now = PortState{Mode: "trunk", PVID: 99, Tagged: []int{30, 99}}
	if !portConverged(want, now, false) {
		t.Errorf("exactAllowed(%+v) = %v did not match the device's %v",
			want, exactAllowed(want), now.Tagged)
	}
}

// --- the guardrails ---

// The classic guardrail protects the port whose NATIVE vlan is the management
// one. Administration often arrives TAGGED on an uplink whose native is 1 — so
// that check sees nothing, and the exact-set write would quietly remove the
// management VLAN from the only port carrying it.
func TestProtectedTaggedLossCatchesTheTaggedUplink(t *testing.T) {
	var protected = map[int]string{99: "site plan management VLAN"}
	var want = PortState{Mode: "trunk", PVID: 1, Tagged: []int{30, 40}}
	var now = PortState{Mode: "trunk", PVID: 1, Tagged: []int{30, 40, 99}}
	var lost, why = protectedTaggedLoss(want, now, false, protected)
	if lost != 99 || why == "" {
		t.Fatalf("lost=%d why=%q — the intent drops VLAN 99 from a port whose native "+
			"VLAN check passes cleanly", lost, why)
	}
}

// An all-allowed trunk is the dangerous case: everything is currently allowed,
// so an exact list drops every VLAN it does not name — including the management
// one, which never appeared in any list to begin with.
func TestProtectedTaggedLossCatchesTheAllAllowedTrunk(t *testing.T) {
	var protected = map[int]string{99: "management VLAN declared by the device"}
	var want = PortState{Mode: "trunk", PVID: 1, Tagged: []int{30}}
	var lost, _ = protectedTaggedLoss(want, PortState{Mode: "trunk", PVID: 1}, true, protected)
	if lost != 99 {
		t.Errorf("lost=%d — an exact list on a trunk that allows everything drops the "+
			"management VLAN with nothing on screen to say so", lost)
	}
}

// An AT-LEAST intent never removes anything, so it can never lose a protected
// VLAN — and a guardrail that fired on it would refuse safe writes for ever.
func TestProtectedTaggedLossIgnoresAtLeastIntents(t *testing.T) {
	var protected = map[int]string{99: "management"}
	var want = PortState{Mode: "trunk", PVID: 1, Tagged: []int{30}, TaggedAtLeast: true}
	var lost, _ = protectedTaggedLoss(want, PortState{Mode: "trunk", PVID: 1}, true, protected)
	if lost != 0 {
		t.Errorf("lost=%d on an at-least intent, which removes nothing", lost)
	}
}

// A trunk with no allowed-vlan line carries EVERY VLAN, so it carries the
// management one. Reading its empty tagged list as "carries nothing" would hand
// the operator a shutdown on the uplink with no warning at all.
func TestCarriesMgmtVLANSeesTheAllAllowedTrunk(t *testing.T) {
	var rc = runConfig{
		Ports:      map[string]driver.PortState{"ET49": {Mode: "trunk", PVID: 1}},
		AllowedAll: map[string]bool{"ET49": true},
	}
	if !carriesMgmtVLAN("ET49", rc, 99) {
		t.Error("an all-allowed trunk was read as not carrying the management VLAN")
	}
}

// A port with no block in the running configuration is at its factory default:
// access VLAN 1. It carries the management VLAN exactly when that VLAN is 1.
func TestCarriesMgmtVLANOnAnUnwrittenPort(t *testing.T) {
	var rc = runConfig{Ports: map[string]driver.PortState{}, AllowedAll: map[string]bool{}}
	if !carriesMgmtVLAN("ET7", rc, 1) {
		t.Error("an unwritten port must read as access VLAN 1")
	}
	if carriesMgmtVLAN("ET7", rc, 99) {
		t.Error("an unwritten port does not carry VLAN 99")
	}
}

// --- the VLANs an apply must create ---

func TestMissingVLANsUnionsPlanManagementAndPortIntents(t *testing.T) {
	var have = []VLAN{{ID: 1, Name: "default"}, {ID: 30, Name: "Voice"}}
	var site = []VLAN{{ID: 30, Name: "Voice"}, {ID: 40, Name: "Data"}}
	var desired = map[string]PortState{
		"ET1": {Mode: "access", PVID: 50},
		"ET2": {Mode: "trunk", PVID: 1, Tagged: []int{60}},
	}
	var got = missingVLANs(have, site, 99, desired)
	var ids []int
	var v VLAN
	for _, v = range got {
		ids = append(ids, v.ID)
	}
	if !reflect.DeepEqual(ids, []int{40, 50, 60, 99}) {
		t.Errorf("missingVLANs = %v, want [40 50 60 99]", ids)
	}
}

// A VLAN with no name is created bare: EOS then keeps its own default
// (VLAN0100), which is exactly what parseVlanTable reads back — so no name is
// invented and no drift is produced.
func TestVlanCreateCmdsOmitsAnAbsentName(t *testing.T) {
	var cmds = vlanCreateCmds([]VLAN{{ID: 100}, {ID: 99, Name: "Management"}})
	if idx(cmds, "vlan 100") < 0 || idxPrefix(cmds, "name VLAN") >= 0 {
		t.Errorf("cmds = %v — an unnamed VLAN must be created bare", cmds)
	}
	if idx(cmds, "name Management") < 0 {
		t.Errorf("cmds = %v", cmds)
	}
}

// --- the transcript must not carry the fleet's credential ---

func TestRedactSecretsHidesTheSecretAndKeepsTheRest(t *testing.T) {
	var got = redactSecrets(accountCmds("mcadmin", "hunter2"))
	if strings.Contains(got, "hunter2") {
		t.Fatalf("the secret reached the transcript: %s", got)
	}
	if !strings.Contains(got, "username mcadmin privilege 15 role network-admin secret •••") {
		t.Errorf("redacted too much — the rest of the line is what makes a transcript "+
			"diagnosable: %s", got)
	}
	if !strings.Contains(got, "management ssh") {
		t.Errorf("the SSH lines were lost from the transcript: %s", got)
	}
}
