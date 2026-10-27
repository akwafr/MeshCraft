package api

// DHCP snooping at the API boundary (cf. dhcpsnoop_apply.go, and
// internal/driver/dhcpsnoop.go for the contract).
//
// WHAT THESE TESTS CAN AND CANNOT REACH, said plainly so a green run is not
// over-read: Server.Store is a concrete *store.Store, so a handler that starts
// with deviceForWrite cannot be driven without PostgreSQL — which the whole
// package deliberately avoids (cf. connector_reject_test.go's header). The
// handler is therefore never ENTERED here. What is asserted instead is every
// judgement it makes, each one lifted out of it into a pure function:
//
//   - the body's judgement (empty map, blank name, ordering) — dhcpSnoopIntents;
//   - THE STATUS CODES — 400 / 422 / 502 — through the dhcpSnoopRefusal ladder,
//     written into a real ResponseRecorder so the code and the sentence an
//     operator reads are both pinned;
//   - the three refusals of the automatic path, each proven by the applier
//     NEVER BEING CALLED rather than by a message;
//   - the enable-only invariant the whole file rests on;
//   - the stored-observation guard, where "not observed" and "observed off"
//     must stay two different sentences.
//
// What remains OUT of reach without a database: the ORDER the handler consults
// those judgements in (body before platform, platform before dialling) and the
// 200 path. Each rung is proven; the ladder they are bolted to is not.
//
// No fixture here is device output: these are API-layer decisions on values the
// frontend and the database supply. Nothing is CAPTURED and nothing pretends to
// be.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"meshcraft/internal/driver"
	"meshcraft/internal/model"
	"meshcraft/internal/recon"
)

// --- the body's judgement ---

func TestDHCPSnoopIntentsRefusesAnEmptyPortMap(t *testing.T) {
	var intents, bad = dhcpSnoopIntents(nil)
	if bad.Msg != "no ports in request" {
		t.Fatalf("refusal = %q", bad.Msg)
	}
	if intents != nil {
		t.Fatalf("intents = %+v on a refused body", intents)
	}
	// `{"ports":{}}` decodes to a non-nil empty map and must be refused too: it
	// is a gesture that names nothing, and answering 200 to it would tell the
	// operator something was written.
	var _, bad2 = dhcpSnoopIntents(map[string]bool{})
	if bad2.Code == 0 {
		t.Fatal("an empty (non-nil) port map was accepted")
	}
}

func TestDHCPSnoopIntentsRefusesABlankPortName(t *testing.T) {
	var _, bad = dhcpSnoopIntents(map[string]bool{"": true})
	if bad.Code == 0 {
		t.Fatal("a port with no name was accepted: it would travel to the connector, come " +
			"back as a Skipped entry, and log an event line reading \"→ trusted\"")
	}
	var _, bad2 = dhcpSnoopIntents(map[string]bool{"GE5": true, "   ": false})
	if bad2.Code == 0 {
		t.Fatal("a whitespace-only port name was accepted")
	}
}

// The bool is TRUSTED and is taken at face value IN BOTH DIRECTIONS here — that
// is what this endpoint exists for. Untrust is only reachable from a human
// gesture, and it must survive the trip.
func TestDHCPSnoopIntentsCarryBothDirectionsVerbatim(t *testing.T) {
	var intents, bad = dhcpSnoopIntents(map[string]bool{"GE5": true, "GE6": false})
	if bad.Code != 0 {
		t.Fatalf("refused a legitimate body: %d %q", bad.Code, bad.Msg)
	}
	if !reflect.DeepEqual(intents, []driver.DHCPSnoopPortIntent{
		{Port: "GE5", Trusted: true}, {Port: "GE6", Trusted: false}}) {
		t.Fatalf("intents = %+v", intents)
	}
}

// Two identical gestures must produce two identical transcripts, and a Go map's
// iteration order is deliberately randomised.
func TestDHCPSnoopIntentsAreOrderedTheSameEveryTime(t *testing.T) {
	var body = map[string]bool{"GE9": true, "GE1": false, "GE4": true, "GE7": false}
	var first, _ = dhcpSnoopIntents(body)
	var i int
	for i = 0; i < 50; i++ {
		var again, _ = dhcpSnoopIntents(body)
		if !reflect.DeepEqual(first, again) {
			t.Fatalf("run %d produced a different order:\n %+v\n %+v", i, first, again)
		}
	}
}

// --- the automatic path's three refusals, proven by absence of a call ---

// snoopPlainAdmin implements driver.Admin and NEITHER optional half of the DHCP
// snooping contract. That is the shape of a connector staged with the write not
// yet implemented, and it must be a distinct TYPE rather than a spy with a
// panicking method: a method that panics still satisfies the interface, so a
// test written that way proves nothing about the type assertion.
type snoopPlainAdmin struct{}

func (a *snoopPlainAdmin) Connector() driver.Info     { return driver.Info{Name: "plain"} }
func (a *snoopPlainAdmin) Account() string            { return "admin" }
func (a *snoopPlainAdmin) Accepted() driver.Candidate { return driver.Candidate{} }
func (a *snoopPlainAdmin) Transcript() []driver.Step  { return nil }
func (a *snoopPlainAdmin) Close()                     {}
func (a *snoopPlainAdmin) ReadState(context.Context) (driver.State, error) {
	return driver.State{}, nil
}
func (a *snoopPlainAdmin) ApplyPorts(context.Context,
	driver.PortPlan) (driver.ApplyResult, error) {
	return driver.ApplyResult{}, nil
}
func (a *snoopPlainAdmin) SetSecret(context.Context, string, string) error { return nil }

// snoopSpyAdmin adds the two optional halves and records whether they were
// reached. It reaches no device: nothing here dials, so a call count of zero is
// proof that nothing could have gone out.
type snoopSpyAdmin struct {
	snoopPlainAdmin
	applies int
	reads   int
	readErr error
	state   driver.DHCPSnoopState
	notes   []string
}

func (a *snoopSpyAdmin) ApplyDHCPSnooping(_ context.Context,
	_ driver.DHCPSnoopPlan) (driver.ApplyResult, error) {
	a.applies++
	return driver.ApplyResult{Notes: a.notes}, nil
}

func (a *snoopSpyAdmin) ReadDHCPSnooping(context.Context) (driver.DHCPSnoopState, error) {
	a.reads++
	return a.state, a.readErr
}

// The two test doubles must really differ in what they implement, or the
// refusal tests below are vacuous.
var (
	_ driver.Admin = (*snoopPlainAdmin)(nil)
	_ driver.Admin = (*snoopSpyAdmin)(nil)
)

func TestTheTestDoublesReallyDifferInWhatTheyImplement(t *testing.T) {
	var _, plainIsApplier = driver.AsDHCPSnoopApplier(&snoopPlainAdmin{})
	var _, plainIsReader = driver.AsDHCPSnoopReader(&snoopPlainAdmin{})
	if plainIsApplier || plainIsReader {
		t.Fatal("snoopPlainAdmin implements an optional half: every refusal test below " +
			"would be measuring the wrong thing")
	}
	var _, spyIsApplier = driver.AsDHCPSnoopApplier(&snoopSpyAdmin{})
	var _, spyIsReader = driver.AsDHCPSnoopReader(&snoopSpyAdmin{})
	if !spyIsApplier || !spyIsReader {
		t.Fatal("snoopSpyAdmin does not implement both halves")
	}
}

// snoopFakeConnector declares exactly one extra writable, and reaches nothing.
type snoopFakeConnector struct{ writes string }

func (c snoopFakeConnector) Info() driver.Info { return driver.Info{Name: "fake"} }
func (c snoopFakeConnector) Capabilities(string) driver.Capabilities {
	return driver.Capabilities{}
}
func (c snoopFakeConnector) Fingerprint(driver.Evidence) int { return 0 }
func (c snoopFakeConnector) ResetProcedure(string) driver.ResetProcedure {
	return driver.ResetProcedure{}
}
func (c snoopFakeConnector) Authenticate(context.Context, driver.Target,
	[]driver.Candidate) (driver.Admin, error) {
	return nil, errors.New("this fake never reaches a device")
}
func (c snoopFakeConnector) WritesExtra(name string) bool { return name == c.writes }
func (c snoopFakeConnector) Supports() []driver.Gesture   { return nil }

// A port panel asking for snooping on a platform that does not declare it
// writable must produce NO gesture at all. Not a refused one — none.
func TestAutoSnoopSendsNothingWhenTheConnectorDoesNotDeclareItWritable(t *testing.T) {
	var s = &Server{}
	var adm = &snoopSpyAdmin{}
	var d = model.Device{Ports: json.RawMessage(
		`{"GE28":{"mode":"trunk","dhcp_snooping":true}}`)}
	var res, err = s.applyDeviceDHCPSnooping(context.Background(), d,
		snoopFakeConnector{writes: "stp"}, adm)
	if err != nil {
		t.Fatalf("an undeclared capability is not an error: %v", err)
	}
	if adm.applies != 0 {
		t.Fatalf("ApplyDHCPSnooping was called %d time(s) on a connector that does not "+
			"declare dhcp_snooping writable", adm.applies)
	}
	if len(res.PortsWritten) != 0 || len(res.Notes) != 0 || len(res.Skipped) != 0 {
		t.Fatalf("a result was manufactured for a gesture that never happened: %+v", res)
	}
}

func TestAutoSnoopSendsNothingWhenNoPortRequestsIt(t *testing.T) {
	var s = &Server{}
	var adm = &snoopSpyAdmin{}
	// The port is managed and says nothing about snooping.
	var d = model.Device{Ports: json.RawMessage(`{"GE1":{"mode":"access","vlan":10}}`)}
	var _, err = s.applyDeviceDHCPSnooping(context.Background(), d,
		snoopFakeConnector{writes: "dhcp_snooping"}, adm)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if adm.applies != 0 {
		t.Fatalf("a device whose ports request nothing produced %d apply call(s)", adm.applies)
	}
}

// The optional interface is genuinely optional (cf. driver.DHCPSnoopApplier): a
// connector may declare the capability before the Admin implements it, and that
// mismatch must be NAMED rather than crash or be silent.
func TestAutoSnoopNamesTheMissingOptionalInterface(t *testing.T) {
	var s = &Server{}
	var adm = &snoopPlainAdmin{}
	var d = model.Device{Ports: json.RawMessage(
		`{"GE28":{"mode":"trunk","dhcp_snooping":true}}`)}
	var res, err = s.applyDeviceDHCPSnooping(context.Background(), d,
		snoopFakeConnector{writes: "dhcp_snooping"}, adm)
	if err != nil {
		t.Fatalf("a staging mismatch is an ordinary result, not a Go error: %v", err)
	}
	if len(res.Notes) != 1 || !strings.Contains(res.Notes[0], "no ApplyDHCPSnooping") {
		t.Fatalf("Notes = %v — the mismatch must be legible in the apply result", res.Notes)
	}
	if len(res.PortsWritten) != 0 {
		t.Fatalf("PortsWritten = %v with no applier at all", res.PortsWritten)
	}
}

// THE INVARIANT THE WHOLE AUTOMATIC PATH RESTS ON. Its comment says
// recon.RequestedExtras only ever reports POSITIVE intents, and that is what
// makes it safe for the path to hard-code Trusted:true. If that ever changed,
// an automatic pass would start untrusting uplinks somebody set on purpose.
func TestAutoSnoopCanOnlyEverTrustNeverUntrust(t *testing.T) {
	var ports = json.RawMessage(`{
		"GE1":{"mode":"access","dhcp_snooping":true},
		"GE2":{"mode":"access","dhcp_snooping":false},
		"GE3":{"mode":"access"},
		"GE4":{"mode":"libre","dhcp_snooping":true}
	}`)
	var got = recon.RequestedExtras(ports)["dhcp_snooping"]
	if !reflect.DeepEqual(got, []string{"GE1"}) {
		t.Fatalf("requested = %v, want [GE1] only: a stored false is a NEUTRAL value, "+
			"indistinguishable from a port that says nothing, and acting on it would take "+
			"a trust flag away from an uplink somebody set deliberately", got)
	}
	// And the nested shape is NOT the stored shape: `{trusted:false}` must not
	// come back as a positive intent, because that is the one form that would
	// invert the flag's meaning while looking correct.
	var nested = json.RawMessage(`{"GE9":{"mode":"access","dhcp_snooping":{"trusted":false}}}`)
	var alsoGot = recon.RequestedExtras(nested)["dhcp_snooping"]
	if len(alsoGot) != 0 {
		t.Fatalf("a nested object was read as a positive intent: %v", alsoGot)
	}
	// The intent is declared, so a UI can say "asked for and not applied".
	var declared bool
	var name string
	for _, name = range recon.ExtraIntents {
		if name == "dhcp_snooping" {
			declared = true
		}
	}
	if !declared {
		t.Fatal("dhcp_snooping is not in recon.ExtraIntents: a port asking for it would " +
			"never be reported as unapplied")
	}
	if recon.ExtraLabel("dhcp_snooping") != "DHCP snooping" {
		t.Fatalf("label = %q — the banner must not show a JSON key",
			recon.ExtraLabel("dhcp_snooping"))
	}
}

// --- the status codes ---
//
// Each rung of the refusal ladder, asserted THROUGH a real ResponseRecorder
// rather than by reading dhcpSnoopRefusal.Code: the struct field is not what an
// operator meets, the response is, and respondErr is part of the contract these
// tests exist to pin.

// answeredRefusal writes a refusal into a recorder and returns what a client
// would see. It fails the test on a refusal that writes nothing, so no
// assertion below can pass by vacuity.
func answeredRefusal(t *testing.T, ref dhcpSnoopRefusal) (int, string) {
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
// the platform is in question — and above all not 500, which would send someone
// reading controller logs for a body the sender can fix.
func TestARefusedBodyIsFourHundred(t *testing.T) {
	var _, empty = dhcpSnoopIntents(nil)
	var code, msg = answeredRefusal(t, empty)
	if code != http.StatusBadRequest {
		t.Fatalf("an empty gesture answered %d, want 400", code)
	}
	if msg != "no ports in request" {
		t.Fatalf("message = %q", msg)
	}
	var _, blank = dhcpSnoopIntents(map[string]bool{"  ": true})
	var blankCode, blankMsg = answeredRefusal(t, blank)
	if blankCode != http.StatusBadRequest {
		t.Fatalf("a blank port name answered %d, want 400", blankCode)
	}
	if !strings.Contains(blankMsg, "empty name") {
		t.Fatalf("message = %q — it must name what is wrong with the body", blankMsg)
	}
}

// 422 on a device no connector was ever associated with, with driver.ConnectorFor's
// OWN sentence: it names the gesture that repairs each of its three cases, and a
// message rewritten at this boundary would drop the repair.
func TestADeviceWithoutAConnectorIsFourTwentyTwo(t *testing.T) {
	var absent, absentErr = driver.ConnectorFor("")
	var code, msg = answeredRefusal(t, dhcpSnoopPlatformRefusal(absent, absentErr))
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
	var unknownCode, unknownMsg = answeredRefusal(t,
		dhcpSnoopPlatformRefusal(unknown, unknownErr))
	if unknownCode != http.StatusUnprocessableEntity {
		t.Fatalf("an unknown connector answered %d, want 422", unknownCode)
	}
	if unknownMsg == msg {
		t.Fatal("two refusals repaired by unrelated gestures carry the same sentence")
	}
	// And a nil connector with no error is answered, not dereferenced: a panic
	// here would be a 500 from a handler whose job is to produce a status code.
	var nilCode, _ = answeredRefusal(t, dhcpSnoopPlatformRefusal(nil, nil))
	if nilCode != http.StatusUnprocessableEntity {
		t.Fatalf("a nil connector answered %d, want 422", nilCode)
	}
}

// THE GUARD THAT MATTERS MOST ON THIS FEATURE: a platform that does not declare
// DHCP snooping writable is refused 422, before anything is dialled. It is what
// stops a UI offering a trust control on a range where nobody proved the write —
// a control that would look like protection and install none.
func TestAPlatformThatDoesNotDeclareSnoopingWritableIsFourTwentyTwo(t *testing.T) {
	var code, msg = answeredRefusal(t,
		dhcpSnoopPlatformRefusal(snoopFakeConnector{writes: "stp"}, nil))
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("an undeclared capability answered %d, want 422 — \"stop looking\" and "+
			"\"try again\" are different instructions", code)
	}
	if msg != "this platform does not declare DHCP snooping as writable" {
		t.Fatalf("message = %q", msg)
	}
	// And the platform that DOES declare it is not refused at all: no status, no
	// body, the handler goes on.
	var rec = httptest.NewRecorder()
	var ok = dhcpSnoopPlatformRefusal(snoopFakeConnector{writes: "dhcp_snooping"}, nil).
		respond(rec)
	if ok || rec.Body.Len() != 0 {
		t.Fatalf("a declared capability was refused: %q", rec.Body.String())
	}
}

// 502 when the device, or the path to it, said no — INCLUDING when the connector
// handed back a usable session anyway. Instant On and RouterOS do exactly that so
// the transcript survives a failed authentication, and asserting the optional
// interface first would find a perfectly good applier on unproven access and
// answer 422 "this platform cannot do that" about a refused password.
func TestAnUnreachableDeviceIsFiveOhTwoEvenWhenASessionCameBack(t *testing.T) {
	var applier, refusal = dhcpSnoopApplierFor(nil,
		errors.New("password refused on account \"mcadmin\""))
	if applier != nil {
		t.Fatal("an applier was returned on access that was never proven")
	}
	var code, msg = answeredRefusal(t, refusal)
	if code != http.StatusBadGateway {
		t.Fatalf("an unreachable device answered %d, want 502", code)
	}
	if msg != "password refused on account \"mcadmin\"" {
		t.Fatalf("message = %q — the device's own words are the diagnosis", msg)
	}
	// The session came back and implements BOTH halves: still 502, because the
	// error is tested before the interface.
	var spy = &snoopSpyAdmin{}
	var alsoApplier, alsoRefusal = dhcpSnoopApplierFor(spy, errors.New("session closed"))
	if alsoApplier != nil {
		t.Fatal("a usable applier was handed out over an authentication that failed")
	}
	var alsoCode, _ = answeredRefusal(t, alsoRefusal)
	if alsoCode != http.StatusBadGateway {
		t.Fatalf("a returned-but-unproven session answered %d, want 502 — the operator "+
			"would be told the hardware cannot do what it does every day", alsoCode)
	}
	if spy.applies != 0 {
		t.Fatalf("the applier was called %d time(s) on unproven access", spy.applies)
	}
}

// 422, not 502: the device answered fine, the CONNECTOR is half built. Nothing
// to retry, nothing wrong on the network.
func TestAConnectorWithNoApplierIsFourTwentyTwo(t *testing.T) {
	var applier, refusal = dhcpSnoopApplierFor(&snoopPlainAdmin{}, nil)
	if applier != nil {
		t.Fatal("an applier came back from an Admin that does not implement one")
	}
	var code, msg = answeredRefusal(t, refusal)
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("a staging mismatch answered %d, want 422 — 502 would send someone "+
			"looking at a switch that behaved perfectly", code)
	}
	if !strings.Contains(msg, "no ApplyDHCPSnooping") {
		t.Fatalf("message = %q", msg)
	}
	// A nil Admin with no error is not a shape adminFor produces, and it must
	// still be answered rather than dereferenced.
	var _, nilRefusal = dhcpSnoopApplierFor(nil, nil)
	if nilRefusal.Code != http.StatusUnprocessableEntity {
		t.Fatalf("a nil Admin answered %d", nilRefusal.Code)
	}
	// Proven access that DOES implement the half is not refused.
	var good, none = dhcpSnoopApplierFor(&snoopSpyAdmin{}, nil)
	if good == nil || none.Code != 0 {
		t.Fatalf("a complete connector was refused: %+v", none)
	}
}

// The write itself: a device that took the session and then refused the
// configuration is 502 too, and says what it said.
func TestAWriteTheDeviceRefusedIsFiveOhTwo(t *testing.T) {
	var code, msg = answeredRefusal(t, dhcpSnoopDeviceRefusal(
		errors.New("% Invalid input detected at '^' marker")))
	if code != http.StatusBadGateway {
		t.Fatalf("a refused write answered %d, want 502", code)
	}
	if !strings.Contains(msg, "Invalid input detected") {
		t.Fatalf("message = %q — the device's answer is the whole diagnosis", msg)
	}
	if dhcpSnoopDeviceRefusal(nil).Code != 0 {
		t.Fatal("a write that succeeded produced a refusal")
	}
}

// A zero refusal writes NOTHING — not an empty body, not a header. Every rung
// depends on it: a handler that reaches one it must not refuse has to leave the
// response untouched for the 200 that follows.
func TestAZeroRefusalWritesNothingAtAll(t *testing.T) {
	var rec = httptest.NewRecorder()
	var zero dhcpSnoopRefusal
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

// The four refusals must stay four DIFFERENT answers. This is the test that
// fails when someone collapses two of them — the whole point of pinning them.
func TestTheRefusalsKeepTheirDistinctCodes(t *testing.T) {
	var _, badBody = dhcpSnoopIntents(nil)
	var _, noConnector = driver.ConnectorFor("")
	var _, noApplier = dhcpSnoopApplierFor(&snoopPlainAdmin{}, nil)
	var cases = []struct {
		what string
		ref  dhcpSnoopRefusal
		want int
	}{
		{"an empty body", badBody, http.StatusBadRequest},
		{"a device with no connector",
			dhcpSnoopPlatformRefusal(nil, noConnector), http.StatusUnprocessableEntity},
		{"a platform that does not declare the write",
			dhcpSnoopPlatformRefusal(snoopFakeConnector{writes: "poe"}, nil),
			http.StatusUnprocessableEntity},
		{"a connector with no applier", noApplier, http.StatusUnprocessableEntity},
		{"an unreachable device",
			dhcpSnoopDeviceRefusal(errors.New("i/o timeout")), http.StatusBadGateway},
	}
	var i int
	for i = range cases {
		var code, msg = answeredRefusal(t, cases[i].ref)
		if code != cases[i].want {
			t.Errorf("%s answered %d, want %d", cases[i].what, code, cases[i].want)
		}
		if strings.TrimSpace(msg) == "" {
			t.Errorf("%s answered %d with no sentence: the screen shows a bare code",
				cases[i].what, code)
		}
	}
}

// --- the observation refresh ---

// A connector that writes without being able to read back is a legitimate
// staging point: the two driver interfaces are independent. Silence, not a note.
func TestRefreshObservedIsSilentWhenTheAdminCannotRead(t *testing.T) {
	var s = &Server{}
	var res driver.ApplyResult
	var adm driver.Admin = &snoopPlainAdmin{}
	s.refreshDHCPSnoopObserved(context.Background(), "dev-1", adm, &res)
	if len(res.Notes) != 0 {
		t.Fatalf("Notes = %v: an absent reader is a staging point, not a fault", res.Notes)
	}
}

// A reader that FAILS is a note, because somebody asked and did not get an
// answer — and the panel is about to repaint from a stale snapshot.
func TestRefreshObservedAnnotatesAFailedReadAndLeavesTheWriteStanding(t *testing.T) {
	var s = &Server{}
	var res = driver.ApplyResult{PortsWritten: []string{"GE28"}}
	var adm = &snoopSpyAdmin{readErr: errors.New("session closed by the device")}
	s.refreshDHCPSnoopObserved(context.Background(), "dev-1", adm, &res)
	if adm.reads != 1 {
		t.Fatalf("the reader was called %d time(s)", adm.reads)
	}
	if len(res.Notes) != 1 || !strings.Contains(res.Notes[0], "not re-read after write") {
		t.Fatalf("Notes = %v", res.Notes)
	}
	// Best-effort means the write stands: the note annotates, it does not undo.
	if !reflect.DeepEqual(res.PortsWritten, []string{"GE28"}) {
		t.Fatalf("a failed re-read retracted the write: %+v", res.PortsWritten)
	}
}

// --- the event line ---

// Read months later by someone who will not remember which way the bool
// pointed, so it is spelled out in words and sorted.
func TestApplySummaryIsSortedAndSpelledOutInWords(t *testing.T) {
	var got = dhcpSnoopApplySummary(map[string]bool{"GE6": false, "GE5": true, "GE1": true})
	if got != "GE1 → trusted, GE5 → trusted, GE6 → untrusted" {
		t.Fatalf("summary = %q", got)
	}
	if strings.Contains(got, "true") || strings.Contains(got, "false") {
		t.Fatalf("the log carries raw bools: %q", got)
	}
	if dhcpSnoopApplySummary(nil) != "" {
		t.Fatalf("an empty gesture produced a line: %q", dhcpSnoopApplySummary(nil))
	}
}

func TestTrustWordNamesTheProtectedState(t *testing.T) {
	if trustWord(true) != "trusted" || trustWord(false) != "untrusted" {
		t.Fatalf("trustWord: %q / %q", trustWord(true), trustWord(false))
	}
}

// --- the stored observation: "not observed" is not "observed off" ---

func TestDHCPSnoopObservedOfGuards(t *testing.T) {
	var cases = map[string]json.RawMessage{
		"no extra":     nil,
		"no key":       json.RawMessage(`{"driver":"cisco-ios"}`),
		"null value":   json.RawMessage(`{"dhcp_snooping_observed":null}`),
		"empty object": json.RawMessage(`{"dhcp_snooping_observed":{}}`),
		"unreadable":   json.RawMessage(`{"dhcp_snooping_observed":"not an object"}`),
		"broken extra": json.RawMessage(`{not json`),
	}
	var name string
	var extra json.RawMessage
	for name, extra = range cases {
		var got = dhcpSnoopObservedOf(model.Device{Extra: extra})
		if got != nil {
			t.Errorf("%s: expected nil, got %+v — a bare {} shown as \"DHCP snooping is "+
				"off on this device\" is an assertion nobody made", name, got)
		}
	}
}

// A device genuinely OBSERVED with the feature off must come back non-nil: the
// screen has to be able to say "off", which is a different sentence from
// "never looked at" and the one that matters most on this feature.
func TestObservedOffIsNotConfusedWithNotObserved(t *testing.T) {
	var d = model.Device{Extra: json.RawMessage(`{"dhcp_snooping_observed":{
		"enabled":false,
		"notes":["DHCP snooping is OFF switch-wide: every trust flag is inert"]}}`)}
	var st = dhcpSnoopObservedOf(d)
	if st == nil {
		t.Fatal("a device read and found OFF came back as \"not observed\": the operator " +
			"loses the one fact that makes every trust flag on the panel meaningless")
	}
	if st.Enabled {
		t.Fatal("enabled=false was not preserved")
	}
	if len(st.Notes) != 1 {
		t.Fatalf("the explanation was dropped: %+v", st.Notes)
	}
}

func TestDHCPSnoopObservedOfParsesTheThreeLevels(t *testing.T) {
	var d = model.Device{Extra: json.RawMessage(`{"dhcp_snooping_observed":{
		"enabled":true,
		"vlans":[99,100],
		"ports":[{"port":"GE2","trusted":false},{"port":"GE28","trusted":true}],
		"notes":["VLAN(s) 100 are configured for snooping but NOT operational"]}}`)}
	var st = dhcpSnoopObservedOf(d)
	if st == nil {
		t.Fatal("expected a parsed DHCPSnoopState, got nil")
	}
	if !st.Enabled {
		t.Fatal("the global level was lost")
	}
	if !reflect.DeepEqual(st.VLANs, []int{99, 100}) {
		t.Fatalf("VLANs = %v", st.VLANs)
	}
	if len(st.Ports) != 2 || st.Ports[0].Trusted || !st.Ports[1].Trusted {
		t.Fatalf("the trust flags did not survive the round trip: %+v", st.Ports)
	}
	// A port ABSENT from the stored list was not read, and the state must not
	// have grown one for the rest of the faceplate on the way through.
	var p driver.DHCPSnoopPortState
	for _, p = range st.Ports {
		if p.Port != "GE2" && p.Port != "GE28" {
			t.Fatalf("port %q appeared from nowhere", p.Port)
		}
	}
	if len(st.Notes) != 1 {
		t.Fatalf("Notes = %v", st.Notes)
	}
}
