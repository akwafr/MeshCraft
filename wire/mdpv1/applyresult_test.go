package mdpv1_test

// [mdpv1.ApplyResult.PortsWritten] means CONFIRMED, never SENT — the rule
// settled on 2026-08-09 after a survey found the nine connectors reading it
// three different ways on one event: a verifying re-read that FAILED.
//
// These tests hold the rule where it is stated rather than nine times over in
// the connectors. What they can reach is the helper every applier now routes
// that event through; what they cannot reach is a connector's own branch, which
// is held in the connector's package against its own CLI stand-in (cf.
// hpe/procurve/dhcpsnoop_session_test.go, fs/s3400/dhcpsnoop_test.go).
//
// Nothing here is a device fixture: this is a contract-level decision about a
// struct, on values the connectors supply.

import (
	"strings"
	"testing"

	"meshcraft/wire/mdpv1"
)

// THE PROPERTY THE WHOLE SWEEP EXISTS FOR. Six connectors used to fill
// PortsWritten from the sent list under a note saying nothing had been
// confirmed — the field and the prose making opposite claims about one gesture.
func TestUnconfirmedNeverPutsAPortInPortsWritten(t *testing.T) {
	var res mdpv1.ApplyResult
	res.Unconfirmed("DHCP snooping trust", []string{"GE1", "GE2"},
		"session closed by the device")
	if len(res.PortsWritten) != 0 {
		t.Fatalf("PortsWritten = %v after a re-read that never landed: a return code is "+
			"not an observation, and the event line downstream reads \"N port(s) written\"",
			res.PortsWritten)
	}
	// It does not become a refusal either: Skipped is what meshcraft REFUSED to
	// do, and a failed re-read is not a refusal — offering a "force" button for
	// a network hiccup would be the wrong instruction entirely.
	if len(res.Skipped) != 0 {
		t.Fatalf("Skipped = %v: a re-read that failed is a fact, not a decision", res.Skipped)
	}
	if len(res.Notes) != 1 {
		t.Fatalf("Notes = %v, want exactly one sentence", res.Notes)
	}
}

// A retraction that did not say WHICH ports went out would lose the only record
// of a half-applied list — and on SwOS and RouterOS, which persist immediately,
// that record is what an operator needs to know where it stopped.
func TestUnconfirmedNamesThePortsAndTheFailure(t *testing.T) {
	var res mdpv1.ApplyResult
	res.Unconfirmed("storm control", []string{"Port2", "SFP1"}, "i/o timeout")
	var note = res.Notes[0]
	var want string
	for _, want = range []string{"storm control", "Port2", "SFP1", "i/o timeout"} {
		if !strings.Contains(note, want) {
			t.Errorf("the note drops %q: %s", want, note)
		}
	}
	// It must also tell the operator what to DO, and the instruction is not
	// "re-send": the commands were accepted, so this may be a failed
	// verification rather than a failed write (the sentence ApplyPoE's own
	// version of this branch settled on).
	if !strings.Contains(note, "re-read the device before sending again") {
		t.Errorf("the note gives no next step: %s", note)
	}
}

// A gesture whose GLOBAL half went out and could not be checked owes the same
// sentence, and phrasing it as "SENT on  and NOT confirmed" would read as a
// bug in meshcraft rather than a fact about the device.
func TestUnconfirmedWithNoPortsStillSaysSomethingReadable(t *testing.T) {
	var res mdpv1.ApplyResult
	res.Unconfirmed("DHCP snooping trust", nil, "connection reset")
	if len(res.Notes) != 1 {
		t.Fatalf("Notes = %v", res.Notes)
	}
	if strings.Contains(res.Notes[0], "SENT on ") {
		t.Fatalf("an empty port list left a dangling \"on\": %s", res.Notes[0])
	}
	if !strings.Contains(res.Notes[0], "connection reset") {
		t.Fatalf("the failure is not named: %s", res.Notes[0])
	}
}

// Two failed re-reads in one apply are two facts, not one overwriting the
// other: an applier that touches a global page and then a per-port page can
// lose both.
func TestUnconfirmedAppendsRatherThanReplaces(t *testing.T) {
	var res mdpv1.ApplyResult
	res.Notes = append(res.Notes, "a note the connector wrote first")
	res.Unconfirmed("storm control", []string{"GE1"}, "first failure")
	res.Unconfirmed("administrative state", []string{"GE2"}, "second failure")
	if len(res.Notes) != 3 {
		t.Fatalf("Notes = %v, want the connector's note plus both retractions", res.Notes)
	}
	if !strings.Contains(res.Notes[1], "GE1") || !strings.Contains(res.Notes[2], "GE2") {
		t.Fatalf("the two retractions did not keep their own ports: %v", res.Notes)
	}
}

// The three name-extractors exist so no connector re-grows the three-line loop
// that made "sent" and "confirmed" easy to conflate. Order is preserved:
// every applier has already sorted its intents into faceplate order by the time
// it gets here, and re-sorting would print a list in an order no panel shows.
func TestIntentPortExtractorsKeepOrderAndNeverReturnNil(t *testing.T) {
	var snoop = mdpv1.SnoopIntentPorts([]mdpv1.DHCPSnoopPortIntent{
		{Port: "GE9", Trusted: true}, {Port: "GE1"}})
	if len(snoop) != 2 || snoop[0] != "GE9" || snoop[1] != "GE1" {
		t.Fatalf("SnoopIntentPorts = %v", snoop)
	}
	var storm = mdpv1.StormIntentPorts([]mdpv1.StormControlPortIntent{
		{Port: "XG2"}, {Port: "GE3"}})
	if len(storm) != 2 || storm[0] != "XG2" {
		t.Fatalf("StormIntentPorts = %v", storm)
	}
	var admin = mdpv1.PortAdminIntentPorts([]mdpv1.PortAdminIntent{{Port: "Port7"}})
	if len(admin) != 1 || admin[0] != "Port7" {
		t.Fatalf("PortAdminIntentPorts = %v", admin)
	}
	// Empty in, empty (non-nil) out: the callers hand the result straight to
	// Unconfirmed, which must not have to guard against nil.
	if mdpv1.SnoopIntentPorts(nil) == nil || mdpv1.StormIntentPorts(nil) == nil ||
		mdpv1.PortAdminIntentPorts(nil) == nil {
		t.Fatal("an extractor returned nil for an empty list")
	}
}
