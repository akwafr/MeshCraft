package api

// Storm control at the API boundary — THE STATUS CODES OF THE **WRITE** (cf.
// stormcontrol.go, and internal/driver/stormcontrol.go for the contract). The
// body's judgements, the event line, the capability view and the profile's stored
// intent are pinned next door in stormcontrol_test.go; the READ's own rungs are in
// stormcontrol_read_test.go, which shares one function with this file and says
// which. This file exists for the ladder the apply handler used to judge inline.
//
// WHAT THESE TESTS CAN AND CANNOT REACH, said plainly so a green run is not
// over-read — the same limit as the two siblings, and it is why all three
// ladders exist as pure functions: Server.Store is a concrete *store.Store, so a
// handler that starts with deviceForWrite cannot be driven without PostgreSQL,
// which this package deliberately avoids (cf. connector_reject_test.go's
// header). handleApplyDeviceStormControl is therefore never ENTERED here. What
// is asserted is every judgement it makes:
//
//   - 400 — the request is wrong and no device was involved;
//   - 422 — the request is well formed and this PLATFORM will not do it, in its
//     four flavours: no connector, a connector this build does not carry, a
//     platform that does not declare the write, and a connector that came back
//     with an Admin having no ApplyStormControl;
//   - 502 — the device, or the path to it, said no, INCLUDING a device that handed
//     back no session at all: the staging sentence above is a claim about a
//     connector, and on a nil session there is no connector to claim it about
//     (this rung answered 422 until 2026-08-09, and the read answered 502 to the
//     identical shape — the two agree now, and a test below pins that);
//   - AND THE ONE RUNG NEITHER SIBLING HAS: the write's own failure graded into
//     422 or 502 depending on whether the connector called it a capability
//     refusal.
//
// Each is written into a real ResponseRecorder rather than read off the struct
// field, because the struct is not what an operator meets — the response is, and
// respondErr is part of the contract these tests exist to pin.
//
// OUT of reach without a database: the ORDER the handler consults the rungs in
// (body before platform, platform before dialling), the body the write's own
// failure answers with — result and transcript beside the sentence — and the 200
// path. Each rung is proven; the ladder they are bolted to is not.
//
// Nothing here is device output. These are API-layer decisions on values the
// frontend and the database supply, and nothing pretends to be captured.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"meshcraft/internal/driver"
)

// stormSpyAdmin adds ApplyStormControl to the plain Admin double declared in
// dhcpsnoop_apply_test.go, and records whether it was reached. It reaches no
// device: nothing here dials, so a call count of zero is proof that nothing
// could have gone out.
type stormSpyAdmin struct {
	snoopPlainAdmin
	applies int
	last    driver.StormControlPlan
}

func (a *stormSpyAdmin) ApplyStormControl(_ context.Context,
	plan driver.StormControlPlan) (driver.ApplyResult, error) {
	a.applies++
	a.last = plan
	return driver.ApplyResult{}, nil
}

var _ driver.Admin = (*stormSpyAdmin)(nil)

// The doubles must really differ in what they implement, or every refusal test
// below is vacuous: a spy with a panicking method would still satisfy the
// interface and prove nothing about the type assertion.
func TestTheStormTestDoublesReallyDifferInWhatTheyImplement(t *testing.T) {
	var _, plainIsApplier = driver.AsStormControlApplier(&snoopPlainAdmin{})
	if plainIsApplier {
		t.Fatal("snoopPlainAdmin implements ApplyStormControl: the refusal tests below " +
			"would be measuring the wrong thing")
	}
	var _, spyIsApplier = driver.AsStormControlApplier(&stormSpyAdmin{})
	if !spyIsApplier {
		t.Fatal("stormSpyAdmin does not implement ApplyStormControl")
	}
}

// answeredStormRefusal writes a refusal into a recorder and returns what a client
// would see. It fails the test on a refusal that writes nothing, so no assertion
// below can pass by vacuity.
func answeredStormRefusal(t *testing.T, ref stormRefusal) (int, string) {
	t.Helper()
	var rec = httptest.NewRecorder()
	if !ref.respond(rec) {
		t.Fatalf("refusal %+v wrote no response at all", ref)
	}
	var body struct {
		Error string `json:"error"`
	}
	var err = json.Unmarshal(rec.Body.Bytes(), &body)
	if err != nil {
		t.Fatalf("the refusal body is not JSON (%v): %q", err, rec.Body.String())
	}
	return rec.Code, body.Error
}

// 400: the request is wrong and no device was involved. Not 422 — nothing about
// the platform is in question — and above all not 502, which would send somebody
// looking at a switch that was never dialled.
//
// The rate rungs are the ones this feature is built around: "10" means ten
// percent of a gigabit on one switch and ten packets a second on the one beside
// it, and a rate of ZERO on an enabled limiter drops every frame of the class —
// on broadcast, ARP with it. Both are 400 HERE, where the sentence can name the
// field, rather than a 502 from a device that was never the problem.
func TestARefusedStormBodyIsFourHundred(t *testing.T) {
	var cases = []struct {
		what  string
		ports map[string][]stormLimitBody
		says  string
	}{
		{"a gesture naming no port", nil, "no ports in request"},
		{"a port with no name",
			map[string][]stormLimitBody{"  ": {{Class: "broadcast", Enabled: false}}},
			"empty name"},
		{"a port named with no limit at all",
			map[string][]stormLimitBody{"GE5": {}}, "no limit at all"},
		{"a rate with no unit",
			map[string][]stormLimitBody{"GE5": {{Class: "broadcast", Enabled: true, Rate: 10}}},
			"unit is mandatory"},
		{"a rate of zero on an enabled limiter",
			map[string][]stormLimitBody{
				"GE5": {{Class: "broadcast", Enabled: true, Rate: 0, Unit: "percent"}}},
			"send it disabled"},
		{"a class the contract does not carry",
			map[string][]stormLimitBody{
				"GE5": {{Class: "unicast", Enabled: true, Rate: 5, Unit: "percent"}}},
			"unicast"},
		{"one port carrying two limits of the same class",
			map[string][]stormLimitBody{"GE5": {
				{Class: "broadcast", Enabled: true, Rate: 5, Unit: "percent"},
				{Class: "broadcast", Enabled: true, Rate: 50, Unit: "percent"}}},
			"two broadcast limits"},
	}
	var i int
	for i = range cases {
		var intents, bad = stormIntents(cases[i].ports, nil)
		if intents != nil {
			t.Errorf("%s produced intents anyway: %+v", cases[i].what, intents)
		}
		var code, msg = answeredStormRefusal(t, bad)
		if code != http.StatusBadRequest {
			t.Errorf("%s answered %d, want 400", cases[i].what, code)
		}
		if !strings.Contains(msg, cases[i].says) {
			t.Errorf("%s answered %q, which does not name what is wrong (%q)",
				cases[i].what, msg, cases[i].says)
		}
	}
}

// 422 on a device no connector was ever associated with, with driver.ConnectorFor's
// OWN sentence: it names the gesture that repairs each of its cases, and a message
// rewritten at this boundary would drop the repair.
func TestADeviceWithoutAConnectorIsFourTwentyTwoForStormControl(t *testing.T) {
	var absent, absentErr = driver.ConnectorFor("")
	var code, msg = answeredStormRefusal(t,
		stormPlatformRefusal(absent, absentErr, StormControlCapsView{}))
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("an absent connector answered %d, want 422 — 502 would accuse the "+
			"network of a fault that is in the database", code)
	}
	if !strings.Contains(msg, "re-run discovery") {
		t.Fatalf("message = %q — the repair instruction did not survive", msg)
	}
	// A connector this binary does not carry is the same code and a DIFFERENT
	// sentence: nothing is wrong on the device, the controller is the wrong build.
	var unknown, unknownErr = driver.ConnectorFor("no-such-connector-ships-here")
	var unknownCode, unknownMsg = answeredStormRefusal(t,
		stormPlatformRefusal(unknown, unknownErr, StormControlCapsView{}))
	if unknownCode != http.StatusUnprocessableEntity {
		t.Fatalf("an unknown connector answered %d, want 422", unknownCode)
	}
	if unknownMsg == msg {
		t.Fatal("two refusals repaired by unrelated gestures carry the same sentence")
	}
	// A nil connector with no error is ANSWERED, not dereferenced. This is the one
	// thing the extraction changed: the handler tested only the error and then
	// called conn.WritesExtra, so this shape was a panic — a 500 out of a handler
	// whose whole job is to produce a status code.
	var nilCode, nilMsg = answeredStormRefusal(t,
		stormPlatformRefusal(nil, nil, StormControlCapsView{}))
	if nilCode != http.StatusUnprocessableEntity {
		t.Fatalf("a nil connector answered %d, want 422", nilCode)
	}
	if nilMsg != "no connector is associated with this device" {
		t.Fatalf("message = %q", nilMsg)
	}
}

// THE GUARD THAT MATTERS MOST ON THIS FEATURE, and today it is the answer the
// endpoint gives on the WHOLE FLEET: no connector in this codebase declares
// "storm_control" writable, because no command form has been run on hardware
// and every package carries a test keeping the flag false until one is.
//
// The sentence must keep the two capability questions apart — "this hardware
// cannot" and "meshcraft has not proven the form yet" lead an operator to
// opposite conclusions, and merging them is the defect the PoE power-cycle button
// carried for a while (cf. StormControlCapsView).
func TestAPlatformThatDoesNotDeclareStormControlWritableIsFourTwentyTwo(t *testing.T) {
	// The "not yet" case: the platform expresses the classes, so the refusal has
	// to say the form is unproven rather than let the operator read "impossible".
	var notYet = StormControlCapsView{Known: true, Classes: driver.StormClasses}
	var code, msg = answeredStormRefusal(t,
		stormPlatformRefusal(snoopFakeConnector{writes: "poe"}, nil, notYet))
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("an undeclared write answered %d, want 422 — \"stop looking\" and "+
			"\"try again\" are different instructions", code)
	}
	if !strings.HasPrefix(msg,
		"this platform does not declare per-port storm control as writable") {
		t.Fatalf("message = %q", msg)
	}
	if !strings.Contains(msg, "has not yet run the command form on this range") {
		t.Fatalf("message = %q — a platform that expresses every class must not be "+
			"described as one that cannot", msg)
	}

	// The "cannot" case: no class at all, and the connector's own reason carried
	// through, because only the connector knows why.
	var cannot = StormControlCapsView{Known: true,
		Why: []string{"this range has one device-wide storm control and no per-interface " +
			"dimension"}}
	var cannotCode, cannotMsg = answeredStormRefusal(t,
		stormPlatformRefusal(snoopFakeConnector{writes: "poe"}, nil, cannot))
	if cannotCode != http.StatusUnprocessableEntity {
		t.Fatalf("a platform that expresses nothing answered %d, want 422", cannotCode)
	}
	if !strings.Contains(cannotMsg, "one device-wide storm control") {
		t.Fatalf("message = %q — the connector's own reason was dropped", cannotMsg)
	}
	if strings.Contains(cannotMsg, "has not yet run the command form") {
		t.Fatalf("message = %q — a platform that expresses no class is waiting on "+
			"nothing, and telling an operator to wait for a capture is a lie", cannotMsg)
	}
	if cannotMsg == msg {
		t.Fatal("\"this hardware cannot\" and \"meshcraft has not proven the form yet\" " +
			"carry the same sentence")
	}

	// And a connector that DOES declare the write passes the rung untouched: no
	// status, no body, the handler goes on.
	var rec = httptest.NewRecorder()
	var wrote = stormPlatformRefusal(snoopFakeConnector{writes: "storm_control"}, nil,
		notYet).respond(rec)
	if wrote || rec.Body.Len() != 0 {
		t.Fatalf("a declared capability was refused: %q", rec.Body.String())
	}
}

// 502 when the device, or the path to it, said no — INCLUDING when the connector
// handed back a usable session anyway. Instant On and RouterOS do exactly that so
// the transcript survives a failed authentication, and asserting the optional
// interface first would find a perfectly good applier on unproven access and
// answer 422 "this platform cannot do that" about a refused password.
func TestAnUnreachableDeviceIsFiveOhTwoForStormControlEvenWhenASessionCameBack(t *testing.T) {
	var applier, refusal = stormApplierFor(nil,
		errors.New("password refused on account \"mcadmin\""))
	if applier != nil {
		t.Fatal("an applier was returned on access that was never proven")
	}
	var code, msg = answeredStormRefusal(t, refusal)
	if code != http.StatusBadGateway {
		t.Fatalf("an unreachable device answered %d, want 502", code)
	}
	if msg != "password refused on account \"mcadmin\"" {
		t.Fatalf("message = %q — the device's own words are the diagnosis", msg)
	}
	// The session came back and implements the applier: still 502, because the
	// error is tested before the interface.
	var spy = &stormSpyAdmin{}
	var alsoApplier, alsoRefusal = stormApplierFor(spy, errors.New("session closed"))
	if alsoApplier != nil {
		t.Fatal("a usable applier was handed out over an authentication that failed")
	}
	var alsoCode, _ = answeredStormRefusal(t, alsoRefusal)
	if alsoCode != http.StatusBadGateway {
		t.Fatalf("a returned-but-unproven session answered %d, want 502 — the operator "+
			"would be told the hardware cannot do what it does every day", alsoCode)
	}
	if spy.applies != 0 {
		t.Fatalf("the applier was called %d time(s) on unproven access", spy.applies)
	}
	// A capability refusal cannot come out of the DIAL, and this rung must not
	// read one out of it: adminFor authenticates and knows nothing about storm
	// control, so grading its error would let an unrelated wrapped
	// ErrNotImplemented tell an operator their hardware is incapable.
	var _, wrapped = stormApplierFor(&stormSpyAdmin{},
		driver.NotSupported("aruba-instant-on-1930", "open a session", "no SSH on this range"))
	if wrapped.Code != http.StatusBadGateway {
		t.Fatalf("a failure on the way to the device answered %d, want 502", wrapped.Code)
	}
}

// TWO RUNGS THAT LOOK ALIKE AND ARE NOT, which is the whole point of asserting
// them together: an Admin that came back and cannot apply is 422, an Admin that
// never came back is 502, and the line between them is whether anything was ever
// asserted against the connector.
//
// It was 422 for both until 2026-08-09, and the nil half was a guess: "its Admin
// has no ApplyStormControl" is a claim about staging, and on a nil session there
// was no Admin to make the claim about. A status code may report what happened and
// must not invent why (cf. stormApplierFor). Collapsing them back — either way —
// is what this test is here to catch.
func TestAStagedWrongStormConnectorIsFourTwentyTwoAndAMissingSessionIsFiveOhTwo(t *testing.T) {
	// The genuine staging fault: the device answered fine, the CONNECTOR is half
	// built — it declares "storm_control" writable and its Admin has no
	// ApplyStormControl. Nothing to retry, nothing wrong on the network, and the
	// sentence is the same one the automatic path writes into its notes.
	var applier, refusal = stormApplierFor(&snoopPlainAdmin{}, nil)
	if applier != nil {
		t.Fatal("an applier came back from an Admin that does not implement one")
	}
	var code, msg = answeredStormRefusal(t, refusal)
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("a staging mismatch answered %d, want 422 — 502 would send someone "+
			"looking at a switch that behaved perfectly", code)
	}
	if !strings.Contains(msg, "no ApplyStormControl") {
		t.Fatalf("message = %q", msg)
	}
	// A nil Admin with no error is answered rather than dereferenced, and answered
	// 502: nothing was reached, so nothing can be said about the connector's
	// staging. The sentence must name the missing session and must NOT name the
	// applier, or it accuses a connector nobody asked anything of.
	var nilApplier, nilRefusal = stormApplierFor(nil, nil)
	if nilApplier != nil {
		t.Fatal("an applier came back from an Admin that does not exist")
	}
	var nilCode, nilMsg = answeredStormRefusal(t, nilRefusal)
	if nilCode != http.StatusBadGateway {
		t.Fatalf("a missing session answered %d, want 502 — 422 is what it used to "+
			"answer, and it blamed the staging of a connector nothing was asserted "+
			"against", nilCode)
	}
	if !strings.Contains(nilMsg, "session") {
		t.Fatalf("message = %q — it must name what is missing", nilMsg)
	}
	if strings.Contains(nilMsg, "ApplyStormControl") {
		t.Fatalf("message = %q — that sentence is a claim about a connector, and no "+
			"connector answered here", nilMsg)
	}
	if nilCode == code {
		t.Fatal("a session that never came back and a connector staged wrong now " +
			"answer the same code: \"look at the switch\" and \"wire the applier\" " +
			"are different instructions")
	}
	// Proven access that DOES implement the half is not refused.
	var good, none = stormApplierFor(&stormSpyAdmin{}, nil)
	if good == nil || none.Code != 0 {
		t.Fatalf("a complete connector was refused: %+v", none)
	}
}

// THE RUNG NEITHER SIBLING HAS: one error value, two answers. A connector that
// says the platform cannot express the gesture is 422 — permanent, stop retrying
// — and everything else is 502 — the switch did not cooperate, try again.
// driver.Unsupported attaches itself to driver.ErrNotImplemented so the two can
// be told apart here, and answering with the wrong one sends the operator off in
// the wrong direction.
//
// stormGradedRefusal, not stormWriteRefusal, since 2026-08-09: the GET's read makes
// the identical judgement on the identical kind of error value and now goes through
// this same function, which stormcontrol_read_test.go pins from its side.
func TestACapabilityRefusalOnTheWriteIsFourTwentyTwoAndAnythingElseIsFiveOhTwo(t *testing.T) {
	var code, msg = answeredStormRefusal(t, stormGradedRefusal(
		driver.NotSupported("aruba-instant-on-1930", "limit storms on one port",
			"this range has no per-port dimension")))
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("a capability refusal answered %d, want 422 — the operator would keep "+
			"retrying a gesture the hardware will never perform", code)
	}
	if !strings.Contains(msg, "no per-port dimension") {
		t.Fatalf("message = %q — the connector's own reason is the whole diagnosis", msg)
	}
	var devCode, devMsg = answeredStormRefusal(t, stormGradedRefusal(
		errors.New("% Invalid input detected at '^' marker")))
	if devCode != http.StatusBadGateway {
		t.Fatalf("a device that refused the configuration answered %d, want 502 — 422 "+
			"would tell the operator their hardware cannot do something it does every "+
			"day", devCode)
	}
	if !strings.Contains(devMsg, "Invalid input detected") {
		t.Fatalf("message = %q — the device's answer is the diagnosis", devMsg)
	}
	if stormGradedRefusal(nil).Code != 0 {
		t.Fatal("a write that succeeded produced a refusal")
	}
}

// A zero refusal writes NOTHING — not an empty body, not a header. Every rung
// depends on it: a handler that reaches one it must not refuse has to leave the
// response untouched for the 200 that follows.
func TestAZeroStormRefusalWritesNothingAtAll(t *testing.T) {
	var rec = httptest.NewRecorder()
	var zero stormRefusal
	if zero.respond(rec) {
		t.Fatal("the zero refusal claims to have answered")
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("body = %q", rec.Body.String())
	}
	if rec.Header().Get("Content-Type") != "" {
		t.Fatalf("a header was committed: %q", rec.Header().Get("Content-Type"))
	}
}

// The refusals must stay DIFFERENT answers. This is the test that fails when
// somebody collapses two of them — the whole point of pinning them. Collapsing
// 422 into 502 sends an operator hunting a network fault that does not exist;
// collapsing 502 into 422 tells them their hardware cannot do something it does
// every day.
func TestTheStormRefusalsKeepTheirDistinctCodes(t *testing.T) {
	var _, badBody = stormIntents(nil, nil)
	var _, noConnector = driver.ConnectorFor("")
	var _, noApplier = stormApplierFor(&snoopPlainAdmin{}, nil)
	var cases = []struct {
		what string
		ref  stormRefusal
		want int
	}{
		{"an empty body", badBody, http.StatusBadRequest},
		{"a device with no connector",
			stormPlatformRefusal(nil, noConnector, StormControlCapsView{}),
			http.StatusUnprocessableEntity},
		{"a platform that does not declare the write",
			stormPlatformRefusal(snoopFakeConnector{writes: "dhcp_snooping"}, nil,
				StormControlCapsView{Known: true}),
			http.StatusUnprocessableEntity},
		{"a connector with no applier", noApplier, http.StatusUnprocessableEntity},
		{"an unreachable device",
			stormReachRefusal(errors.New("i/o timeout")), http.StatusBadGateway},
		{"a write the device refused",
			stormGradedRefusal(errors.New("session closed")), http.StatusBadGateway},
		{"a write the platform cannot express",
			stormGradedRefusal(driver.NotSupported("swos", "limit storms", "no such field")),
			http.StatusUnprocessableEntity},
	}
	var i int
	for i = range cases {
		var code, msg = answeredStormRefusal(t, cases[i].ref)
		if code != cases[i].want {
			t.Errorf("%s answered %d, want %d", cases[i].what, code, cases[i].want)
		}
		if strings.TrimSpace(msg) == "" {
			t.Errorf("%s answered %d with no sentence: the screen shows a bare code",
				cases[i].what, code)
		}
	}
	// The three codes are three instructions, and the body / platform / device
	// rungs must never share one.
	if badBody.Code == noApplier.Code ||
		noApplier.Code == stormReachRefusal(errors.New("x")).Code ||
		badBody.Code == stormReachRefusal(errors.New("x")).Code {
		t.Fatal("the body, platform and device refusals collapsed into one code")
	}
}
