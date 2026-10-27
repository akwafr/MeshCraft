package procurve

import (
	"strings"
	"testing"
)

// "disable" and not "shutdown": this range does not speak IOS, and the form
// below is the one the reference unit's own running configuration prints
// (testdata/show_running_config.txt, "interface 1 / disable / exit").
func TestPortAdminCmds(t *testing.T) {
	var down = portAdminCmds(1, false)
	if len(down) != 3 || down[0] != "interface 1" || down[1] != "disable" || down[2] != "exit" {
		t.Errorf("disable form = %q — the capture spells it \"interface 1 / disable / exit\"", down)
	}
	var up = portAdminCmds(47, true)
	if len(up) != 3 || up[0] != "interface 47" || up[1] != "enable" {
		t.Errorf("enable form = %q", up)
	}
	var cmd string
	for _, cmd = range append(down, up...) {
		if strings.Contains(cmd, "shutdown") {
			t.Fatalf("\"shutdown\" is not a word this CLI knows: %q", cmd)
		}
	}
}

// Replayed against the REAL capture: port 1 of SW-PLAYER-79 was administratively
// disabled when it was read ("Enabled No"), every other port was not. This is
// the read side of the applier, and the one column that tells a port someone shut
// apart from a port with no cable.
func TestInterfacesBriefCarriesBothEnabledValues(t *testing.T) {
	var status = parseInterfacesBrief(fixture(t, "show_interfaces_brief.txt"))
	var one, ok = status[1]
	if !ok {
		t.Fatal("port 1 must be present in the capture")
	}
	if one.Enabled {
		t.Error("port 1 reads \"Enabled No\" in the capture: it must read as administratively down")
	}
	if one.Up {
		t.Error("port 1 reads \"Down\": a disabled port carries no link")
	}
	var two = status[2]
	if !two.Enabled {
		t.Error("port 2 reads \"Enabled Yes\" in the capture")
	}
}

// THE GUARDRAIL, against the reference unit's real membership: VLAN 99 is the
// management VLAN (the only one with a static address) and it is "tagged 45-52"
// — the uplinks — while every faceplate port is untagged in VLAN 200. So the port
// that must be protected here is a TAGGED one, which is exactly the case a
// PVID-only guardrail would wave through.
func TestPortAdminGuardBlocksProtectsTheTaggedUplink(t *testing.T) {
	var rc = parseRunningConfig(fixture(t, "show_running_config.txt"))
	if rc.MgmtVLAN != 99 {
		t.Fatalf("the capture's management VLAN is 99, parsed %d", rc.MgmtVLAN)
	}

	var block, reason = portAdminGuardBlocks(rc, 47, rc.MgmtVLAN, true, false)
	if !block {
		t.Error("port 47 is tagged in the management VLAN and linked: it must be spared")
	}
	if !strings.Contains(reason, "99") {
		t.Errorf("the refusal must name the management VLAN: %q", reason)
	}
	if block, _ = portAdminGuardBlocks(rc, 47, rc.MgmtVLAN, false, false); block {
		t.Error("nothing plugged in means no session to cut: an unlinked port must not be spared")
	}
	if block, _ = portAdminGuardBlocks(rc, 47, rc.MgmtVLAN, true, true); block {
		t.Error("Force must lift the refusal")
	}
	if block, _ = portAdminGuardBlocks(rc, 20, rc.MgmtVLAN, true, false); block {
		t.Error("port 20 carries neither an untagged nor a tagged membership of VLAN 99: it is " +
			"not the port the session arrives on")
	}
}

// An UNTAGGED member of the management VLAN is protected too — the other of the
// two ways this VLAN-centric platform expresses membership. CONSTRUCTED (the
// reference unit has no port untagged in VLAN 99): a minimal stanza in the exact
// shape parseRunningConfig reads, with the trailing spaces this CLI emits.
func TestPortAdminGuardBlocksProtectsAnUntaggedMember(t *testing.T) {
	var cfg = "hostname \"x\" \n" +
		"vlan 99 \n" +
		"   name \"admin\" \n" +
		"   ip address 203.0.113.79 255.255.255.0 \n" +
		"   untagged 3 \n" +
		"   exit \n"
	var rc = parseRunningConfig(cfg)
	if rc.MgmtVLAN != 99 {
		t.Fatalf("management VLAN parsed as %d", rc.MgmtVLAN)
	}
	var block, _ = portAdminGuardBlocks(rc, 3, rc.MgmtVLAN, true, false)
	if !block {
		t.Error("a linked port untagged in the management VLAN must be spared")
	}
}

// ZERO IS IGNORANCE ON THIS RANGE, not permission: a unit addressed by
// "ip address dhcp-bootp" — the factory default — carries no static address on
// any VLAN, so MgmtVLAN reads 0 while administration genuinely arrives on VLAN 1.
// Every LINKED port must be spared. CONSTRUCTED from the factory-default stanza
// this platform prints.
func TestPortAdminGuardBlocksSparesEverythingWhenMgmtIsUnknown(t *testing.T) {
	var cfg = "hostname \"x\" \n" +
		"vlan 1 \n" +
		"   name \"DEFAULT_VLAN\" \n" +
		"   ip address dhcp-bootp \n" +
		"   untagged 1-52 \n" +
		"   exit \n"
	var rc = parseRunningConfig(cfg)
	if rc.MgmtVLAN != 0 {
		t.Fatalf("a dhcp-bootp address must not be read as the management VLAN, got %d", rc.MgmtVLAN)
	}
	var block, reason = portAdminGuardBlocks(rc, 5, rc.MgmtVLAN, true, false)
	if !block {
		t.Error("with the management VLAN unknown, a LINKED port must be spared: a guardrail " +
			"that fails open is not a guardrail")
	}
	if !strings.Contains(reason, "static address") {
		t.Errorf("the refusal must say why the management VLAN is unknown: %q", reason)
	}
	if block, _ = portAdminGuardBlocks(rc, 5, rc.MgmtVLAN, false, false); block {
		t.Error("even with the management VLAN unknown, an unlinked port carries no session")
	}
}
