package s3900

// THE ADMIN-DOWN ROW — s3900lab (S3900-24T4S, FSOS), 2026-08-10, over the
// controller's SOCKS route as `mcadmin`. CAPTURED, not constructed: a shut →
// read → revert exercise on a DARK port (ethernet 1/1), reverted and confirmed.
// Running configuration only — nothing was saved to startup.
//
// This is the row portadmin.go's header and contract.go's `port_admin` note said
// had never been seen: every line of labBrief (parse_test.go, the 2026-08-01/02
// serial session) reads "Up" in the Admin column, so until today the ONLY
// evidence for the column's meaning was the captured HEADER and the Accton
// family it belongs to. [briefPort.AdminUp] is now replayed against a real
// "Down".
//
// # WHAT THIS EXERCISE DOES *NOT* PROVE
//
// The commands were TYPED AT THE CLI by hand — "conf t" → "interface ethernet
// 1/1" → "shutdown" → "exit" → "exit", prompt walking Switch-1# →
// Switch-1(config)# — NOT driven through [Client.ApplyPortAdmin]. So the write
// form and the read-back row are proven on this firmware, and the APPLIER is
// not: its guardrail, its "already where the plan wants it" skip and its
// post-write verification are still exercised by constructed tests only
// (portadmin_test.go). No [Driver.WritesExtra] flag moved today.
//
// # One refusal worth keeping: "configure" is NOT the word here
//
// Plain "configure" was tried first and REFUSED — "% Invalid input detected
// at '^' marker." — before "conf t" was typed. [Client.runConfigBlock] already
// sends "conf t" (apply.go), so nothing had to change; the refusal is recorded
// because a future edit "spelling it out properly" would break every write in
// this package at once, and a captured refusal is the only thing that argues
// against that edit.

import (
	"strings"
	"testing"
)

// The capture, verbatim, header and separator included. The trailing spaces on
// the two rows are the device's; the SPACE inside "Eth 1/ 1" is the whole
// point of the second test below.
const liveBriefShutGE1 = `Interface Type         Admin  Link-Status  Negotiation  Speed/Duplex   Group
------------------------------------------------------------------------- -----
Eth 1/ 1  1000BASE-T   Down    Down          Auto                       None 
Eth 1/ 2  1000BASE-T   Up      Down          Auto                       None 
`

// "Down" in the Admin column, from the hardware at last.
//
// Both rows read "Down" in Link-Status — the two ports are dark — so the ONLY
// thing separating the shut port from its neighbour is the Admin column. Read
// the two columns as one, or in the wrong order, and this connector would report
// GE2 as shut and GE1 as merely unplugged: the applier would then skip the write
// it was asked for on GE1 and confirm a write it never sent on GE2.
func TestParseInterfacesBriefOnCapturedShutRow(t *testing.T) {
	var rows = parseInterfacesBrief(liveBriefShutGE1)
	if len(rows) != 2 {
		t.Fatalf("rows = %d (%+v) — the header and the dashed separator are not ports", len(rows), rows)
	}
	if rows[0].Name != "GE1" || rows[0].Num != 1 {
		t.Fatalf("row 0 = %+v, want the port \"Eth 1/ 1\" resolves to", rows[0])
	}
	if rows[0].AdminUp {
		t.Error("GE1 was SHUT when this was captured: the Admin column reads \"Down\" and " +
			"AdminUp must be false — this is the first hardware evidence of that column moving")
	}
	if rows[0].LinkUp {
		t.Error("GE1 reads \"Down\" in Link-Status too (a dark port)")
	}
	if !rows[1].AdminUp {
		t.Error("GE2 reads \"Up\" in the Admin column: an ENABLED port with no cable, which is " +
			"not the same thing as a shut one")
	}
	if rows[1].LinkUp {
		t.Error("GE2 reads \"Down\" in Link-Status")
	}
	if rows[0].AdminUp == rows[1].AdminUp {
		t.Fatal("the two captured rows differ ONLY in the Admin column: a connector that reports " +
			"them alike has lost the read side of ApplyPortAdmin")
	}
	if rows[0].Type != "1000BASE-T" || rows[0].SpeedMbps != 1000 {
		t.Errorf("the type column must survive the shut row too: %+v", rows[0])
	}
}

// THE PORT TOKEN CONTAINS A SPACE. "Eth 1/ 1" — the number is right-aligned in
// a two-character field, so single-digit ports carry a space between "1/" and
// the digit, and double-digit ones do not.
//
// Any splitter must survive that. This test states the trap as executable fact:
// on whitespace, the same table yields a DIFFERENT number of fields per row, so
// there is no fixed index at which the Admin column lives. reBriefLine reads the
// unit and the port as two numbers around the slash instead, which is why it
// works on both shapes — and this test fails the day someone "simplifies" it to
// strings.Fields.
func TestCapturedBriefRowPortTokenCarriesASpace(t *testing.T) {
	var shutRow string
	var line string
	for _, line = range strings.Split(liveBriefShutGE1, "\n") {
		if strings.HasPrefix(line, "Eth 1/ 1") {
			shutRow = line
		}
	}
	if shutRow == "" {
		t.Fatal("the capture must carry the \"Eth 1/ 1\" spelling, space included")
	}
	var fields = strings.Fields(shutRow)
	if fields[1] != "1/" || fields[2] != "1" {
		t.Fatalf("splitting on whitespace tears the port token in two — that is the trap being "+
			"pinned, and it changed: %q", fields)
	}
	// Same table, wider port number and an SFP+ type: a different field count and
	// a different index for the Admin column. Two rows of one capture cannot be
	// read positionally.
	var wide = "Eth 1/28  10GBASE SFP+ Up      Up            Disable       10Gfull      None"
	if len(strings.Fields(wide)) == len(fields) {
		t.Fatal("the two row shapes now split into the same number of fields, which would make " +
			"a positional reader viable — check this against the real capture before believing it")
	}

	var rows = parseInterfacesBrief(shutRow + "\n" + wide + "\n")
	if len(rows) != 2 || rows[0].Name != "GE1" || rows[1].Name != "GE28" {
		t.Fatalf("both row shapes must parse: %+v", rows)
	}
	if rows[0].AdminUp || !rows[1].AdminUp {
		t.Errorf("the Admin column must be read from the same place in both shapes: %+v", rows)
	}
}

// The 2026-08-01/02 capture is what today's row is new against: labBrief has no
// "Down" in the Admin column at all. Pinned so that the claim this file makes
// about its own novelty stays checkable — and so a future edit that quietly
// rewrites labBrief is caught rather than believed.
func TestLabBriefStillCarriesNoAdminDownRow(t *testing.T) {
	var rows = parseInterfacesBrief(labBrief)
	if len(rows) == 0 {
		t.Fatal("labBrief must still parse")
	}
	var r briefPort
	for _, r = range rows {
		if !r.AdminUp {
			t.Errorf("%s reads Admin \"Down\" in labBrief: that capture is described everywhere "+
				"in this package as admin-up throughout, and those comments are the ones to fix",
				r.Name)
		}
	}
}
