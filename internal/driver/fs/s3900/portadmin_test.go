package s3900

import (
	"strings"
	"testing"
)

// The write form. "ethernet 1/N" is the captured interface vocabulary of this
// dialect (cf. contract.go's dialect note), and the verb pair is the Accton
// family's — DEDUCED, and declared anyway since 2026-08-06 (contract.go's
// WritesExtra comment says on what terms). This test pins the ONE spelling that
// gets sent: there is no fallback candidate in either lineage, so a change here is
// a change to the only thing this connector will ever type at a port.
func TestPortAdminCmds(t *testing.T) {
	var down = portAdminCmds(cliPortName("GE5"), false)
	if len(down) != 3 || down[0] != "interface ethernet 1/5" ||
		down[1] != "shutdown" || down[2] != "exit" {
		t.Errorf("shut form = %q", down)
	}
	var up = portAdminCmds(cliPortName("GE28"), true)
	if len(up) != 3 || up[0] != "interface ethernet 1/28" || up[1] != "no shutdown" {
		t.Errorf("enable form = %q", up)
	}
}

// The lab capture (labBrief) reads "Up" in the Admin column on every port: the
// applier's read side must report that faithfully rather than confuse it with the
// Link-Status column, which is "Down" on all but GE28.
func TestParseInterfacesBriefAdminColumnFromCapture(t *testing.T) {
	var rows = parseInterfacesBrief(labBrief)
	var byName = map[string]briefPort{}
	var r briefPort
	for _, r = range rows {
		byName[r.Name] = r
	}
	var ge1, ok = byName["GE1"]
	if !ok {
		t.Fatal("GE1 must be present in the capture")
	}
	if !ge1.AdminUp {
		t.Error("GE1 reads \"Up\" in the Admin column of the capture")
	}
	if ge1.LinkUp {
		t.Error("GE1 reads \"Down\" in the Link-Status column: an administratively enabled port " +
			"with no cable, which is NOT the same thing as a shut one")
	}
	if !byName["GE28"].LinkUp || !byName["GE28"].AdminUp {
		t.Errorf("GE28 is the one linked port of the capture: %+v", byName["GE28"])
	}
}

// The row shape the applier depends on: Admin "Down". CONSTRUCTED from the
// captured header's column order (Admin, then Link-Status) — the two columns must
// not be read in the wrong order, or a shut port would be reported as merely
// unplugged and the applier would skip the write it was asked for.
//
// It is no longer the ONLY evidence for that shape: the hardware printed the row
// on 2026-08-10 and portadmin_live_test.go replays it verbatim. This one is kept
// because it uses ports the capture does not (GE7/GE8), so a parser that somehow
// special-cased the captured numbers would still be caught.
func TestParseInterfacesBriefShutPort(t *testing.T) {
	var out = "Interface Type         Admin  Link-Status  Negotiation  Speed/Duplex   Group\n" +
		"Eth 1/ 7  1000BASE-T   Down    Down          Auto                       None\n" +
		"Eth 1/ 8  1000BASE-T   Up      Down          Auto                       None\n"
	var rows = parseInterfacesBrief(out)
	if len(rows) != 2 {
		t.Fatalf("rows = %d (%+v)", len(rows), rows)
	}
	if rows[0].Name != "GE7" || rows[0].AdminUp || rows[0].LinkUp {
		t.Errorf("a \"Down Down\" row is an administratively SHUT port: %+v", rows[0])
	}
	if rows[1].Name != "GE8" || !rows[1].AdminUp || rows[1].LinkUp {
		t.Errorf("an \"Up Down\" row is an enabled port with no cable: %+v", rows[1])
	}
}

// THE GUARDRAIL. Note the platform-specific part: a management VLAN of zero is
// IGNORANCE here (no management SVI could be named while meshcraft is talking to
// the device over IP), so a linked port must be spared — but the SAFE
// direction, bringing a port back up, is never gated at all, which is why the
// applier does not refuse the whole gesture the way ApplyPorts does.
func TestPortAdminGuardBlocks(t *testing.T) {
	var native = PortState{Mode: "access", PVID: 99}
	var trunk = PortState{Mode: "trunk", PVID: 1, Tagged: []int{10, 99}}
	var elsewhere = PortState{Mode: "access", PVID: 100}

	var block, reason = portAdminGuardBlocks(native, 99, true, false)
	if !block || !strings.Contains(reason, "99") {
		t.Errorf("a linked port whose native VLAN is the management one must be spared: %v, %q",
			block, reason)
	}
	if block, _ = portAdminGuardBlocks(trunk, 99, true, false); !block {
		t.Error("a linked trunk with the management VLAN in its allowed list must be spared")
	}
	if block, _ = portAdminGuardBlocks(native, 99, false, false); block {
		t.Error("nothing plugged in means no session to cut")
	}
	if block, _ = portAdminGuardBlocks(native, 99, true, true); block {
		t.Error("Force must lift the refusal")
	}
	if block, _ = portAdminGuardBlocks(elsewhere, 99, true, false); block {
		t.Error("a linked port outside the management VLAN is not the session's port")
	}
	block, reason = portAdminGuardBlocks(elsewhere, 0, true, false)
	if !block {
		t.Error("with the management VLAN unknown, every LINKED port must be spared")
	}
	if !strings.Contains(reason, "SVI") {
		t.Errorf("the refusal must name what is unknown: %q", reason)
	}
}

func TestCarriesVLAN(t *testing.T) {
	if carriesVLAN(PortState{PVID: 5}, 0) {
		t.Error("VLAN 0 is not a VLAN: nothing carries it")
	}
	if !carriesVLAN(PortState{PVID: 5}, 5) {
		t.Error("the native VLAN counts as carried")
	}
	if !carriesVLAN(PortState{PVID: 1, Tagged: []int{5}}, 5) {
		t.Error("a tagged VLAN counts as carried")
	}
	if carriesVLAN(PortState{PVID: 1, Tagged: []int{5}}, 6) {
		t.Error("a VLAN neither native nor tagged is not carried")
	}
}

// The DECLARATION. api/portadmin_apply.go gates the gesture on
// WritesExtra("port_admin"), so an undeclared capability means the button never
// reaches this connector's ApplyPortAdmin at all. "dhcp_snooping" joined it on
// 2026-08-11 once ApplyDHCPSnooping had been driven on hardware; dhcpsnoop_test.go
// guards that one separately, and the list below is what this package still does
// NOT write.
func TestDriverDeclaresPortAdmin(t *testing.T) {
	var d = &Driver{}
	if !d.WritesExtra("port_admin") {
		t.Error("ApplyPortAdmin exists and the operator asked for the gesture fleet-wide: " +
			"the API layer would refuse what this connector implements")
	}
	var name string
	for _, name = range []string{"stp", "poe", "port_security",
		"port_power_cycle"} {
		if d.WritesExtra(name) {
			t.Errorf("declares %q writable, and nothing in this package writes it", name)
		}
	}
}
