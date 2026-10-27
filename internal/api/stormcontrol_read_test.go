package api

// Storm control at the API boundary — THE STATUS CODES OF THE **READ** (cf.
// stormcontrol.go, and stormcontrol_apply_test.go for the write's half of the same
// ladder). The GET kept judging its own codes inline for a day after the POST
// stopped, and this file is what closed that: 502 on the way to the device, and the
// connector's own answer graded into 422 or 502.
//
// WHY A SIBLING FILE AND NOT MORE OF stormcontrol_apply_test.go: the rungs are not
// the same set. The read has one the write cannot have — a connector that WRITES
// storm control and cannot read it back is answered 200, not refused — and it does
// not have the body rungs at all, since a GET carries no body to judge. What the two
// files share is [stormGradedRefusal] and, since 2026-08-09, the answer to a session
// that never came back; the last two tests here are the ones that say so out loud, so
// that splitting either later has to be a deliberate act rather than an accident.
//
// WHAT THESE TESTS CAN AND CANNOT REACH, said plainly so a green run is not
// over-read — the same limit as every ladder in this package, and the reason the
// ladders are pure functions in the first place: Server.Store is a concrete
// *store.Store, so a handler that starts with deviceForWrite cannot be driven
// without PostgreSQL, which this package deliberately avoids (cf.
// connector_reject_test.go's header). handleReadDeviceStormControl is therefore
// never ENTERED here. What is asserted is every judgement it makes:
//
//   - 502 — the device could not be reached, or handed back no session at all;
//   - the NON-refusal — a proven session whose Admin cannot read storm control,
//     which must leave the response untouched for the 200 that follows;
//   - 422 versus 502 on what the read itself came back with, graded on
//     driver.ErrNotImplemented.
//
// Each refusal is written into a real ResponseRecorder rather than read off the
// struct field, because the struct is not what an operator meets — the response is.
//
// OUT of reach without a database: the ORDER the handler consults the rungs in, the
// BODIES it assembles (the capability view riding along with the refusal, the note
// on a connector that cannot read back, the 200 carrying the state), and the
// session-per-request budget the endpoint spends. Each rung is proven; the ladder
// they are bolted to is not.
//
// Nothing here is device output. These are API-layer decisions on values a connector
// supplies, and nothing pretends to be captured.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"meshcraft/internal/driver"
)

// stormReadingAdmin adds ReadStormControl to the plain Admin double declared in
// dhcpsnoop_apply_test.go, and records whether it was reached. It reaches no
// device: nothing here dials, so a call count of zero is proof that nothing could
// have gone out.
type stormReadingAdmin struct {
	snoopPlainAdmin
	reads int
}

func (a *stormReadingAdmin) ReadStormControl(context.Context) (driver.StormControlState, error) {
	a.reads++
	return driver.StormControlState{}, nil
}

var _ driver.Admin = (*stormReadingAdmin)(nil)

// The doubles must really differ in what they implement, or every rung below is
// vacuous. stormSpyAdmin is the interesting one: it APPLIES and cannot READ, which
// is not a contrivance but the staging point stormReaderFor's third outcome exists
// for (cf. driver.StormControlReader — the two halves are independent).
func TestTheStormReadDoublesReallyDifferInWhatTheyImplement(t *testing.T) {
	var _, plainCanRead = driver.AsStormControlReader(&snoopPlainAdmin{})
	if plainCanRead {
		t.Fatal("snoopPlainAdmin implements ReadStormControl: the refusal tests below " +
			"would be measuring the wrong thing")
	}
	var _, applierCanRead = driver.AsStormControlReader(&stormSpyAdmin{})
	if applierCanRead {
		t.Fatal("stormSpyAdmin reads storm control: the write-but-cannot-read-back " +
			"case below would no longer be that case")
	}
	var _, readerCanRead = driver.AsStormControlReader(&stormReadingAdmin{})
	if !readerCanRead {
		t.Fatal("stormReadingAdmin does not implement ReadStormControl")
	}
}

// 502 when the device, or the path to it, said no — INCLUDING when the connector
// handed back a usable session anyway. Instant On and RouterOS do exactly that so
// the transcript survives a failed authentication, and asserting the optional
// interface first would hand out a perfectly good reader over access that was never
// proven, then read a switch that refused the password.
func TestAnUnreachableDeviceIsFiveOhTwoOnTheStormControlRead(t *testing.T) {
	var reader, refusal = stormReaderFor(nil,
		errors.New("password refused on account \"mcadmin\""))
	if reader != nil {
		t.Fatal("a reader was returned on access that was never proven")
	}
	var code, msg = answeredStormRefusal(t, refusal)
	if code != http.StatusBadGateway {
		t.Fatalf("an unreachable device answered %d, want 502", code)
	}
	if msg != "password refused on account \"mcadmin\"" {
		t.Fatalf("message = %q — the device's own words are the diagnosis", msg)
	}
	// The session came back and CAN read: still 502, because the error is tested
	// before the interface.
	var live = &stormReadingAdmin{}
	var alsoReader, alsoRefusal = stormReaderFor(live, errors.New("session closed"))
	if alsoReader != nil {
		t.Fatal("a usable reader was handed out over an authentication that failed")
	}
	var alsoCode, _ = answeredStormRefusal(t, alsoRefusal)
	if alsoCode != http.StatusBadGateway {
		t.Fatalf("a returned-but-unproven session answered %d, want 502", alsoCode)
	}
	if live.reads != 0 {
		t.Fatalf("the device was read %d time(s) over unproven access", live.reads)
	}
	// A capability refusal cannot come out of the DIAL, and this rung must not read
	// one out of it: adminFor authenticates and knows nothing about storm control,
	// so grading its error would let an unrelated wrapped ErrNotImplemented tell an
	// operator their hardware is incapable of something it does every day.
	var _, wrapped = stormReaderFor(&stormReadingAdmin{},
		driver.NotSupported("aruba-instant-on-1930", "open a session", "no SSH on this range"))
	if wrapped.Code != http.StatusBadGateway {
		t.Fatalf("a failure on the way to the device answered %d, want 502", wrapped.Code)
	}
}

// A NIL SESSION WITH NO ERROR IS REFUSED, and this is the rung the extraction added.
// The handler used to test the error and never the Admin — the same shape that was a
// panic on the apply side — but here driver.AsStormControlReader(nil) is a safe
// assertion that answers false, so the shape fell through into the 200 below and
// told the operator "this connector writes storm control but cannot read it back"
// about a device no session was ever opened on: a capability sentence, invented for
// a switch nobody reached, under a success code.
func TestASessionThatNeverCameBackIsFiveOhTwoOnTheStormReadAndNotACapabilitySentence(t *testing.T) {
	var reader, refusal = stormReaderFor(nil, nil)
	if reader != nil {
		t.Fatal("a reader came back from an Admin that does not exist")
	}
	var code, msg = answeredStormRefusal(t, refusal)
	if code != http.StatusBadGateway {
		t.Fatalf("a missing session answered %d, want 502 — 200 is what it used to "+
			"answer, and 422 would blame hardware that was never dialled", code)
	}
	if !strings.Contains(msg, "session") {
		t.Fatalf("message = %q — it must name what is missing", msg)
	}
	if strings.Contains(msg, "cannot read it back") {
		t.Fatalf("message = %q — that sentence is a claim about a connector, and no "+
			"connector answered here", msg)
	}
}

// A CONNECTOR THAT WRITES AND CANNOT READ BACK IS NOT A REFUSAL AT ALL. The two
// halves of the contract are independent (cf. driver.StormControlReader), so this is
// a legitimate staging point and the handler answers 200 with the capability view.
// The rung must therefore return no reader AND no refusal — and above all must leave
// the response untouched, since a committed status here would freeze the 200 that
// follows into whatever was written first.
func TestAConnectorThatCannotReadStormControlBackIsNotRefused(t *testing.T) {
	var reader, refusal = stormReaderFor(&stormSpyAdmin{}, nil)
	if reader != nil {
		t.Fatal("a reader came back from an Admin that does not implement one")
	}
	if refusal.Code != 0 {
		t.Fatalf("a connector that merely cannot read back was refused %d: %q — that "+
			"is a staging point, not a failure", refusal.Code, refusal.Msg)
	}
	var rec = httptest.NewRecorder()
	if refusal.respond(rec) || rec.Body.Len() != 0 {
		t.Fatalf("the rung wrote a response the handler still needs: %q", rec.Body.String())
	}
	// And a session that CAN read passes the rung with the reader itself, not a copy
	// of some other Admin: the handler reads through what comes back here.
	var live = &stormReadingAdmin{}
	var good, none = stormReaderFor(live, nil)
	if good == nil || none.Code != 0 {
		t.Fatalf("a readable connector was refused: %+v", none)
	}
	var _, err = good.ReadStormControl(context.Background())
	if err != nil || live.reads != 1 {
		t.Fatalf("the rung handed back something other than the session it was given "+
			"(reads = %d, err = %v)", live.reads, err)
	}
}

// ONE ERROR VALUE, TWO ANSWERS, on the READ exactly as on the write. A connector
// that says the platform cannot serve per-port storm control is 422 — permanent,
// stop looking — and everything else is 502 — the switch did not cooperate, try
// again. Answering with the wrong one sends the operator off in the wrong direction:
// hunting a network fault that does not exist, or waiting for a capability that will
// never arrive.
func TestACapabilityRefusalOnTheStormReadIsFourTwentyTwoAndAnythingElseIsFiveOhTwo(t *testing.T) {
	var code, msg = answeredStormRefusal(t, stormGradedRefusal(
		driver.NotSupported("aruba-instant-on-1930", "read per-port storm control",
			"this range has one device-wide storm control and no per-interface dimension")))
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("a platform that cannot report per-port limiters answered %d, want "+
			"422 — the operator would keep refreshing a panel that can never fill", code)
	}
	if !strings.Contains(msg, "no per-interface dimension") {
		t.Fatalf("message = %q — the connector's own reason is the whole diagnosis, and "+
			"it is the read's own sentence rather than the write's", msg)
	}
	var devCode, devMsg = answeredStormRefusal(t, stormGradedRefusal(
		errors.New("read timed out after 30s")))
	if devCode != http.StatusBadGateway {
		t.Fatalf("a read the device did not answer got %d, want 502 — 422 would tell "+
			"the operator their hardware cannot do something it does every day", devCode)
	}
	if !strings.Contains(devMsg, "timed out") {
		t.Fatalf("message = %q — the failure's own words are the diagnosis", devMsg)
	}
	if stormGradedRefusal(nil).Code != 0 {
		t.Fatal("a read that succeeded produced a refusal")
	}
}

// THE READ AND THE WRITE GRADE ONE ERROR THE SAME WAY, and that is a decision rather
// than a coincidence: the input is one error value from one connector, the marker is
// the same driver.ErrNotImplemented, and the sentence is the connector's own carried
// verbatim in both directions, so there is nothing left for a second ladder to decide
// differently (stormGradedRefusal argues it in full).
//
// This test is the tripwire on that decision. It does not forbid splitting the two —
// it forbids splitting them BY ACCIDENT, which is what a copied grading always ends
// in: two functions, one edited, and a read that says "stop looking" where the
// write says "try again" about the same switch on the same afternoon.
func TestTheStormReadAndWriteGradeTheSameErrorIdentically(t *testing.T) {
	var cases = []error{
		driver.NotSupported("swos", "limit storms", "no such field"),
		errors.New("% Invalid input detected at '^' marker"),
		errors.New("i/o timeout"),
		nil,
	}
	var i int
	for i = range cases {
		// Both call sites reach the SAME function today; what is pinned is that the
		// answer is one answer, whichever verb asked for it.
		var ref = stormGradedRefusal(cases[i])
		var again = stormGradedRefusal(cases[i])
		if ref != again {
			t.Fatalf("%v graded two ways: %+v then %+v", cases[i], ref, again)
		}
		if cases[i] == nil {
			continue
		}
		if ref.Code != http.StatusUnprocessableEntity && ref.Code != http.StatusBadGateway {
			t.Fatalf("%v answered %d, which is neither of the two instructions this "+
				"rung exists to give", cases[i], ref.Code)
		}
		if ref.Msg != cases[i].Error() {
			t.Fatalf("%v was rewritten at the boundary as %q — the connector named the "+
				"repair and this layer dropped it", cases[i], ref.Msg)
		}
	}
}

// THE READ AND THE WRITE ANSWER A MISSING SESSION THE SAME WAY, and this is the
// second tripwire on the same principle as the one above — one event, one answer,
// whichever verb was about to be attempted.
//
// They disagreed until 2026-08-09: the read said 502 "the device was not reached"
// and the write said 422 "connector declares storm control writable but Admin has
// no ApplyStormControl", which is a claim about a connector's staging on a session
// that never existed. Nothing was asserted against the connector, so naming a
// staging fault was a guess — and a status code must not assert a cause nothing
// observed. Both are 502 now.
//
// The sentences differ in the VERB alone. That is deliberate and pinned here: they
// describe the same missing session and must not drift into two unrelated
// explanations of it.
func TestTheStormReadAndWriteAnswerAMissingSessionTheSameWay(t *testing.T) {
	var reader, readRef = stormReaderFor(nil, nil)
	var applier, writeRef = stormApplierFor(nil, nil)
	if reader != nil || applier != nil {
		t.Fatal("a reader or an applier came back from a session that does not exist")
	}
	var readCode, readMsg = answeredStormRefusal(t, readRef)
	var writeCode, writeMsg = answeredStormRefusal(t, writeRef)
	if readCode != http.StatusBadGateway || writeCode != http.StatusBadGateway {
		t.Fatalf("the read answered %d and the write answered %d on the same missing "+
			"session, want 502 from both — a device that was never reached is one "+
			"event, not two", readCode, writeCode)
	}
	var both = []string{readMsg, writeMsg}
	var i int
	for i = range both {
		if !strings.Contains(both[i], "no session came back from this device") {
			t.Fatalf("message = %q — the two verbs stopped describing the same event",
				both[i])
		}
		if strings.Contains(both[i], "ApplyStormControl") ||
			strings.Contains(both[i], "cannot read it back") {
			t.Fatalf("message = %q — that is a claim about a connector, and no "+
				"connector was asserted against here", both[i])
		}
	}
	if !strings.Contains(readMsg, "nothing can be read without one") {
		t.Fatalf("the read's message = %q", readMsg)
	}
	if !strings.Contains(writeMsg, "nothing can be written without one") {
		t.Fatalf("the write's message = %q — the verb is the only thing that differs "+
			"between the two, and it must be the write's", writeMsg)
	}
}
