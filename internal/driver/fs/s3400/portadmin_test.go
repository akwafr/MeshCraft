package s3400

import (
	"os"
	"strings"
	"testing"

	"meshcraft/internal/driver"
)

// The write form, per port kind: the copper sockets are "g0/N" and the SFP+
// cages "tg0/N" (cliPortName, captured 2026-08-03 — "show interface tg0/1"
// answers for TGigaEthernet0/1).
func TestPortAdminCmds(t *testing.T) {
	var down, derr = portAdminCmds(cagesFrom1, "GE12", false)
	if derr != nil {
		t.Fatalf("a copper port needs no cage observation: %v", derr)
	}
	if len(down) != 3 || down[0] != "interface g0/12" || down[1] != "shutdown" || down[2] != "exit" {
		t.Errorf("shut form = %q", down)
	}
	var up, uerr = portAdminCmds(cagesFrom1, "XG2", true)
	if uerr != nil {
		t.Fatalf("the Build 95262 numbering was observed: %v", uerr)
	}
	if len(up) != 3 || up[0] != "interface tg0/2" || up[1] != "no shutdown" {
		t.Errorf("enable form on an SFP+ cage = %q", up)
	}
}

// The read side, replayed against the 2026-08-03/05 captures: a port reads "is
// down, line protocol is down" with nothing plugged in (g0/1, tg0/3, tg0/4) and
// "is up" when a cable is live (tg0/1, tg0/2). Neither is LinkDisabled, and
// this test pins that these three files stay that way.
//
// It is NO LONGER the statement that this hardware has never printed
// "administratively down": the 2026-08-10 shut → capture → revert exercise did
// get the word out of it, and portadmin_live_test.go replays it from
// testdata/show_interface_g01_admin_down.txt. What survives here is the narrower
// and still useful fact — the ordinary link captures of a switch with nothing
// shut must not read as disabled, or the applier would skip writes it was asked
// for on the strength of a mis-parse.
func TestCapturedInterfaceLinesCarryNoAdminDownWord(t *testing.T) {
	var files = []string{
		"testdata/show_interface_g01.txt",
		"testdata/show_interface_tg01.txt",
		"testdata/show_interface_tg03.txt",
	}
	var f string
	for _, f = range files {
		var b, err = os.ReadFile(f)
		if err != nil {
			t.Fatalf("reading %s: %v", f, err)
		}
		if strings.Contains(strings.ToLower(string(b)), "administratively") {
			t.Fatalf("%s DOES contain \"administratively\": these three are the 2026-08-03/05 "+
				"captures of a switch with nothing shut, so either the fixture was replaced or "+
				"it was mixed with the 2026-08-10 admin-down capture", f)
		}
		var links = ParseLinkStates(string(b))
		var name string
		var st driver.LinkState
		for name, st = range links {
			if st == driver.LinkDisabled {
				t.Errorf("%s: %s read as LinkDisabled, but no capture carries an admin-down line",
					f, name)
			}
		}
	}
}

// "administratively down" must read as LinkDisabled and NOT as a dead cable:
// the applier's "already where the plan wants it" test and its post-write
// verification both hang on this one value. CONSTRUCTED — kept because it drives
// three states through one call, which no single capture does; the captured
// version of the admin-down line lives in portadmin_live_test.go (2026-08-10),
// and this fixture matches it word for word.
func TestParseLinkStatesAdminDown(t *testing.T) {
	var out = "GigaEthernet0/12 is administratively down, line protocol is down\n" +
		"GigaEthernet0/13 is down, line protocol is down\n" +
		"TGigaEthernet0/1 is up, line protocol is up\n"
	var links = ParseLinkStates(out)
	if links["GE12"] != driver.LinkDisabled {
		t.Errorf("GE12 = %q, want %q: an operator who shut a port must not be told the cable "+
			"is out", links["GE12"], driver.LinkDisabled)
	}
	if links["GE13"] != driver.LinkDown {
		t.Errorf("GE13 = %q, want %q", links["GE13"], driver.LinkDown)
	}
	if links["XG1"] != driver.LinkUpState {
		t.Errorf("XG1 = %q, want %q", links["XG1"], driver.LinkUpState)
	}
}

// THE GUARDRAIL: link-gated, membership-gated, force-liftable, and — the part
// that differs per platform — a management VLAN of zero means IGNORANCE here, so
// a linked port must be spared rather than written.
func TestPortAdminGuardBlocks(t *testing.T) {
	var onMgmt = PortState{Mode: "access", PVID: 99}
	var trunk = PortState{Mode: "trunk", PVID: 1, Tagged: []int{42, 99}}
	var elsewhere = PortState{Mode: "access", PVID: 100}

	var block, reason = portAdminGuardBlocks(onMgmt, 99, true, false)
	if !block || reason == "" {
		t.Error("a LINKED port whose PVID is the management VLAN must be spared, with a reason")
	}
	if block, _ = portAdminGuardBlocks(trunk, 99, true, false); !block {
		t.Error("a LINKED trunk TAGGED in the management VLAN is the uplink case: it must be spared")
	}
	if block, _ = portAdminGuardBlocks(onMgmt, 99, false, false); block {
		t.Error("nothing plugged in means no session to cut")
	}
	if block, _ = portAdminGuardBlocks(onMgmt, 99, true, true); block {
		t.Error("Force must lift the refusal")
	}
	if block, _ = portAdminGuardBlocks(elsewhere, 99, true, false); block {
		t.Error("a linked port outside the management VLAN is not the session's port")
	}
	block, reason = portAdminGuardBlocks(elsewhere, 0, true, false)
	if !block {
		t.Error("management VLAN 0 on this platform means no SVI reported an address — a " +
			"desynchronised read, not permission: every linked port must be spared")
	}
	if !strings.Contains(reason, "SVI") {
		t.Errorf("the refusal must say why the management VLAN is unknown: %q", reason)
	}
}

// The DECLARATION, asserted next to the guardrail that makes it survivable: the
// API layer gates the gesture on WritesExtra("port_admin") (api/portadmin_apply.go),
// so a connector that implements ApplyPortAdmin without declaring it ships a
// button that leads nowhere. "dhcp_snooping" must stay false here — its forms
// have never been accepted on this range and dhcpsnoop_test.go guards it.
func TestDriverDeclaresPortAdmin(t *testing.T) {
	var d = &Driver{}
	if !d.WritesExtra("port_admin") {
		t.Error("ApplyPortAdmin exists and \"shutdown\" is the form the operator asked for " +
			"fleet-wide: the API layer would refuse the gesture this connector implements")
	}
	if !d.WritesExtra("stp") {
		t.Error("ApplySpanningTree exists and \"stp\" must stay declared")
	}
	var name string
	for _, name = range []string{"poe", "dhcp_snooping", "port_security", "port_power_cycle"} {
		if d.WritesExtra(name) {
			t.Errorf("declares %q writable, and nothing in this package writes it", name)
		}
	}
}
