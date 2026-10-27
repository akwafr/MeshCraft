package edgeswitch

import (
	"context"
	"strings"
	"testing"

	"meshcraft/internal/driver"
)

// Replayed against the REAL baseline capture (testdata/show_port_all.txt — lab
// EdgePoint S16, 2026-08-05). Every port on that unit is administratively
// "Enable" and only 0/18 is linked, which is exactly the shape that catches the
// bug this parser exists for: parseShowPortAll's AdminUp says "the row contains
// the word enable somewhere", and every row contains it twice more (Link Trap,
// LACP Mode).
func TestParsePortAdminRowsBaselineCapture(t *testing.T) {
	var rows = parsePortAdminRows(mustRead(t, "testdata/show_port_all.txt"))

	if len(rows) != 18 {
		t.Fatalf("expected the 18 faceplate sockets (0/1-0/18), got %d: %+v", len(rows), rows)
	}
	// The slot-3 LAG rows are interfaces, not sockets: a faceplate applier that
	// took them would offer six jacks that are not on the front panel.
	var _, sawLAG = rows["3/1"]
	if sawLAG {
		t.Error("a slot-3 LAG row must not appear as a port")
	}

	var ge1 = rows["GE1"]
	if !ge1.AdminSeen || !ge1.AdminUp {
		t.Errorf("GE1: Admin Mode reads Enable in the capture, got %+v", ge1)
	}
	if ge1.LinkUp {
		t.Errorf("GE1: Link Status reads Down in the capture, got %+v", ge1)
	}
	var ge18 = rows["GE18"]
	if !ge18.AdminUp || !ge18.LinkUp {
		t.Errorf("GE18: the one linked port of the capture (\"10G Full  Up\"), got %+v", ge18)
	}
}

// CONSTRUCTED, not captured: the word "Disable" has never been seen in the
// Admin Mode column on this hardware (cf. portadmin.go's status section — it is
// what makes this connector's "shutdown" a DEDUCED form even though
// WritesExtra now declares "port_admin"). The row below is the baseline capture's
// own 0/5 line with Enable replaced by Disable in the Admin Mode column ONLY,
// leaving Link Trap and LACP Mode as they are — the shape that must not read back
// as administratively up.
func TestParsePortAdminRowsShutPort(t *testing.T) {
	var out = "" +
		"--------- ------ --------- ---------- ---------- ------ ------- ------ -------\n" +
		"0/5              Disable   Auto                  Down   Enable  Enable Disable\n"
	var rows = parsePortAdminRows(out)
	var ge5, known = rows["GE5"]
	if !known {
		t.Fatalf("row not parsed: %q", out)
	}
	if !ge5.AdminSeen {
		t.Error("the Admin Mode column is present and must be reported as read")
	}
	if ge5.AdminUp {
		t.Error("Admin Mode \"Disable\" must read as administratively DOWN — the two later " +
			"\"Enable\" words on the row (Link Trap, LACP Mode) must not flip it")
	}
	if ge5.LinkUp {
		t.Error("Link Status \"Down\" must not read as a link")
	}
}

// A port linked while shut: the combination the applier must handle without
// contradicting itself (the guardrail looks at the LINK, the "already done" test
// looks at ADMIN). CONSTRUCTED from the captured 0/18 row.
func TestParsePortAdminRowsLinkedAndShut(t *testing.T) {
	var out = "0/18             Disable   Auto D     10G Full   Up     Enable  Enable Disable\n"
	var rows = parsePortAdminRows(out)
	var ge18 = rows["GE18"]
	if ge18.AdminUp {
		t.Errorf("Admin Mode Disable must read down, got %+v", ge18)
	}
	if !ge18.LinkUp {
		t.Errorf("Link Status Up must read as a link even on a shut port, got %+v", ge18)
	}
}

func TestPortAdminCmds(t *testing.T) {
	var down = portAdminCmds("0/5", false)
	if len(down) != 3 || down[0] != "interface 0/5" || down[1] != "shutdown" || down[2] != "exit" {
		t.Errorf("shut form = %q", down)
	}
	var up = portAdminCmds("0/5", true)
	if len(up) != 3 || up[1] != "no shutdown" {
		t.Errorf("enable form = %q — \"no shutdown\" is the only inverse this CLI family has", up)
	}
}

// THE GUARDRAIL. Link-gated, mgmt-membership-gated, force-liftable — and on this
// platform a management VLAN of zero means IGNORANCE ("show network" always
// prints the line), so it must SPARE a linked port rather than let the write
// through. That asymmetry is the difference from SwOS, where the same zero is a
// legitimate device state.
func TestPortAdminGuardBlocks(t *testing.T) {
	var onMgmt = PortState{Mode: "access", PVID: 99}
	var elsewhere = PortState{Mode: "access", PVID: 100}

	var block, reason = portAdminGuardBlocks(onMgmt, 99, true, false)
	if !block || reason == "" {
		t.Error("a LINKED port carrying the management VLAN must be spared, with a reason")
	}
	if block, _ = portAdminGuardBlocks(onMgmt, 99, false, false); block {
		t.Error("nothing plugged in means no session to cut: an UNLINKED port must not be spared")
	}
	if block, _ = portAdminGuardBlocks(onMgmt, 99, true, true); block {
		t.Error("Force must lift the refusal")
	}
	if block, _ = portAdminGuardBlocks(elsewhere, 99, true, false); block {
		t.Error("a linked port that does not carry the management VLAN is not the session's port")
	}
	block, reason = portAdminGuardBlocks(elsewhere, 0, true, false)
	if !block {
		t.Error("management VLAN 0 on this platform is a FAILED READ, not permission: every " +
			"linked port must be spared")
	}
	if reason == "" {
		t.Error("the refusal must say that the management VLAN could not be read")
	}
	if block, _ = portAdminGuardBlocks(elsewhere, 0, false, false); block {
		t.Error("even with the management VLAN unknown, an unlinked port carries no session")
	}
}

// A trunk tagged into the management VLAN is as much "the port the session rides"
// as an access port whose pvid is it — the uplink case, and the one that would
// cost a site visit.
func TestPortAdminGuardBlocksTaggedTrunk(t *testing.T) {
	var trunk = PortState{Mode: "trunk", PVID: 1, Tagged: []int{42, 99}}
	var block, _ = portAdminGuardBlocks(trunk, 99, true, false)
	if !block {
		t.Error("a linked trunk TAGGED in the management VLAN must be spared")
	}
}

func TestPortLessIsNumeric(t *testing.T) {
	if !portLess("GE2", "GE10") {
		t.Error("GE2 must sort before GE10: the faceplate order is numeric, not lexical")
	}
	if portLess("GE10", "GE2") {
		t.Error("GE10 must not sort before GE2")
	}
}

// The cycler's restore vocabulary: every class the READ can produce must be a
// class the WRITE can send, or the cut half would be made with no way back. This
// is the invariant CyclePortPower checks per port before touching anything, and
// the one that would break silently if a future class were added to
// classOfOpMode alone.
func TestEveryReadableClassIsSendable(t *testing.T) {
	var classes = []string{driver.PoEClassNone, driver.PoEClass24V, driver.PoEClass54V}
	var cl string
	for _, cl = range classes {
		var _, _, ok = opModeOfClass(cl)
		if !ok {
			t.Errorf("class %q can be read from \"show poe port all\" but not written back: "+
				"a power cycle on such a port would be half a power cycle", cl)
		}
	}
}

// The four declarations this package makes, and the ones it must not. Each name is
// a separate promise to api/portadmin_apply.go and the screens above it: "poe"
// (exercised live 2026-08-05), "port_power_cycle" (the only implementation in the
// fleet, cf. contract.go), "port_admin" (declared 2026-08-06 on a DEDUCED form)
// and "dhcp_snooping" (declared 2026-08-08 on a live exercise of all four
// gestures, cf. dhcpsnoop_test.go which guards it separately).
//
// This assertion is a second lock on the same door, because the names are set in
// ONE expression and a careless edit there is how a button turns itself on. That
// is not hypothetical: "dhcp_snooping" moved from the false list to the true one
// on 2026-08-08, and it had to be moved HERE as well as next door before the suite
// would go green — which is the guard working.
func TestDriverDeclaresPortAdminAlongsidePoE(t *testing.T) {
	var d = &Driver{}
	var name string
	for _, name = range []string{"port_admin", "poe", "port_power_cycle", "dhcp_snooping"} {
		if !d.WritesExtra(name) {
			t.Errorf("%q is implemented here and not declared: the API layer gates the gesture "+
				"on this name and would refuse it", name)
		}
	}
	for _, name = range []string{"stp", "port_security", "hostname"} {
		if d.WritesExtra(name) {
			t.Errorf("declares %q writable, and nothing in this package writes it", name)
		}
	}
}

// THE FACEPLATE HALF of the same repair, and the reason it was worth making.
//
// [LinkStates] keys [driver.LinkDisabled] on PortLine.AdminUp, and that field used
// to be "the row contains the word enable somewhere". Every row on this platform
// carries "Enable" twice more (Link Trap, LACP Mode), so a shut port read as
// administratively UP and the faceplate could only ever show it as unplugged —
// the one reading that sends an operator looking for a cable that is fine. This
// connector DECLARES port_admin, so it can produce that state itself.
//
// CONSTRUCTED, like the rows above: "Disable" has never been seen in the Admin
// Mode column on this hardware.
func TestLinkStatesReportsAShutPortAsDisabledAndNotMerelyDown(t *testing.T) {
	var out = "" +
		"0/1              Enable    Auto                  Down   Enable  Enable Disable\n" +
		"0/5              Disable   Auto                  Down   Enable  Enable Disable\n" +
		"0/6              Disable   Auto       1000 Full  Up     Enable  Enable Disable\n" +
		"0/18             Enable    Auto D     10G Full   Up     Enable  Enable Disable\n"

	var links = LinkStates(out)
	if links["GE5"] != driver.LinkDisabled {
		t.Errorf("GE5 = %q, want %q: Admin Mode \"Disable\" is a SHUT port, and the two later "+
			"\"Enable\" words on the row must not flip it", links["GE5"], driver.LinkDisabled)
	}
	if links["GE6"] != driver.LinkDisabled {
		t.Errorf("GE6 = %q, want %q: administration beats the link — a port shut while a cable "+
			"is still negotiating is shut", links["GE6"], driver.LinkDisabled)
	}
	if links["GE1"] != driver.LinkDown {
		t.Errorf("GE1 = %q, want %q: enabled with nothing plugged in", links["GE1"], driver.LinkDown)
	}
	if links["GE18"] != driver.LinkUpState {
		t.Errorf("GE18 = %q, want %q", links["GE18"], driver.LinkUpState)
	}
}

// A row whose Admin Mode column could not be read at all must NOT be reported as
// shut: "the parser did not find the column" and "the device says Disable" are
// different claims, and inventing a shut port out of the first is how a faceplate
// starts showing outages that do not exist. The link is still readable, so that is
// what gets reported.
func TestLinkStatesDoesNotInventAShutPortFromAnUnreadableAdminColumn(t *testing.T) {
	var rows = parseShowPortAll("0/7                                             Up\n")
	if len(rows) != 1 {
		t.Fatalf("expected one row, got %+v", rows)
	}
	if rows[0].AdminSeen {
		t.Errorf("no Enable/Disable token on this row: AdminSeen must be false, got %+v", rows[0])
	}
	var links = LinkStates("0/7                                             Up\n")
	if links["GE7"] != driver.LinkUpState {
		t.Errorf("GE7 = %q, want %q: the admin column is unreadable, the link is not",
			links["GE7"], driver.LinkUpState)
	}
}

// THE GUARDRAIL MUST FAIL CLOSED WHEN THE MEMBERSHIP READ COMES BACK SHORT.
//
// This is the arista/eos defect of 2026-08-09 checked on this connector, where
// it was also present. `show running-config` is accepted when the COMMAND
// succeeded, and a command can succeed and still be short — an undisabled pager,
// a transport closing mid-output, a firmware ordering its sections differently.
// "show network" then answers the management VLAN perfectly while the
// membership map is empty, so the guard skips its own mgmt<=0 branch and asks
// `carries(zero, 99)` — false for every port, including the trunked uplink
// carrying this very session.
//
// The fake below is the exact hazard: a full `show port all` (so GE18 reads
// LINKED) and a running-config with NO interface block at all.
func TestApplyPortAdminSparesALinkedPortWhenTheMembershipReadCameBackEmpty(t *testing.T) {
	var portAll = "" +
		"                 Admin     Physical   Physical   Link   Link    LACP   Flow\n" +
		"Intf      Type   Mode      Mode       Status     Status Trap    Mode   Mode\n" +
		"--------- ------ --------- ---------- ---------- ------ ------- ------ -------\n" +
		"0/18             Enable    Auto       1000 Full  Up     Enable  Enable Disable\n"
	// Successful, and short: not one `interface` block. The VLAN section survived,
	// which is what makes it look like a valid answer.
	var runShort = "vlan database\nvlan 99\nexit\n"
	var network = "Management VLAN ID............................. 99\n"

	var c, _ = newSnoopFakeClient(func(cmd string) string {
		switch {
		case strings.HasPrefix(cmd, "show port all"):
			return portAll
		case strings.HasPrefix(cmd, "show running-config"):
			return runShort
		case strings.HasPrefix(cmd, "show network"):
			return network
		}
		return ""
	})

	var res, err = c.ApplyPortAdmin(context.Background(), driver.PortAdminPlan{
		Ports: []driver.PortAdminIntent{{Port: "GE18", Enabled: false}},
	})
	if err != nil {
		t.Fatalf("ApplyPortAdmin: %v", err)
	}
	if len(res.PortsWritten) != 0 {
		t.Fatalf("GE18 WAS SHUT. It carries a link, and the membership read returned no "+
			"interface block at all — so nothing established that it does NOT carry the "+
			"management VLAN. PortsWritten=%v", res.PortsWritten)
	}
	if len(res.Skipped) == 0 {
		t.Error("nothing was written and nothing was reported")
	}
	// And the operator must be told WHY, in terms that distinguish a short read
	// from a device that genuinely has no membership.
	var notes = strings.Join(res.Notes, " ")
	if !strings.Contains(notes, "no interface block") {
		t.Errorf("the notes do not say the membership read came back empty: %v", res.Notes)
	}
	t.Logf("spared: %v", res.Skipped)
	t.Logf("notes: %v", res.Notes)
}
