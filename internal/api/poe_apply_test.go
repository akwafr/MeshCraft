package api

// PoE at the API boundary (cf. poe_apply.go, and internal/driver/poe.go for the
// contract).
//
// WHAT THESE TESTS CAN AND CANNOT REACH, said plainly so a green run is not
// over-read — the same limit as the DHCP-snooping sibling, and it is why both
// ladders exist as pure functions: Server.Store is a concrete *store.Store, so a
// handler that starts with deviceForWrite cannot be driven without PostgreSQL,
// which this package deliberately avoids. handleApplyDevicePoE is therefore
// never ENTERED here. What is asserted is every judgement it makes:
//
//   - the body's judgement — poeIntents: empty map, blank name, class
//     vocabulary, and the ORDER, which was a Go map's until 2026-08-09;
//   - THE STATUS CODES — 400 / 422 / 502 — through the poeRefusal ladder,
//     written into a real ResponseRecorder so the code AND the sentence an
//     operator reads are both pinned;
//   - the automatic path's refusals, each proven by the applier NEVER BEING
//     CALLED rather than by a message;
//   - the enable-only invariant that path rests on.
//
// OUT of reach without a database: the ORDER the handler consults the rungs in
// (body before platform, platform before dialling) and the 200 path. Each rung
// is proven; the ladder they are bolted to is not.
//
// Nothing here is device output. These are API-layer decisions on values the
// frontend and the database supply.

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
)

// --- the body's judgement ---

func TestPoEIntentsRefusesAnEmptyPortMap(t *testing.T) {
	var intents, bad = poeIntents(nil)
	if bad.Msg != "no ports in request" {
		t.Fatalf("refusal = %q", bad.Msg)
	}
	if intents != nil {
		t.Fatalf("intents = %+v on a refused body", intents)
	}
	// `{"ports":{}}` decodes to a non-nil empty map and must be refused too: it
	// names no port, so answering 200 would tell the operator power was written.
	var _, bad2 = poeIntents(map[string]string{})
	if bad2.Code == 0 {
		t.Fatal("an empty (non-nil) port map was accepted")
	}
}

// THE HOLE THIS CLOSES. Until 2026-08-09 the handler ranged over the body
// straight into the plan, so `{"ports":{"":"none"}}` travelled to the connector,
// came back as somebody's flavour of Skipped, and logged an event line reading
// "→ none" — a recorded power cut attributed to no port at all. The sibling
// endpoint refused blank names from 2026-08-08; this one did not.
func TestPoEIntentsRefusesABlankPortName(t *testing.T) {
	var _, bad = poeIntents(map[string]string{"": driver.PoEClassNone})
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("a port with no name answered %d: it would travel to the connector "+
			"and log a power cut attributed to nothing", bad.Code)
	}
	var _, bad2 = poeIntents(map[string]string{"0/5": driver.PoEClass54V, "  ": driver.PoEClassNone})
	if bad2.Code != http.StatusBadRequest {
		t.Fatal("a whitespace-only port name was accepted")
	}
}

// The class vocabulary is the rung PoE has and snooping does not. The EMPTY
// class is refused here on purpose: it is reserved for the automatic path, where
// it means "whatever this platform's default on is" (cf. driver.PoEPortIntent),
// and that is not a meaning a button press may carry.
func TestPoEIntentsRefusesAClassOutsideTheVocabulary(t *testing.T) {
	var bad poeRefusal
	var class string
	for _, class = range []string{"", "on", "off", "auto", "48v", "NONE"} {
		_, bad = poeIntents(map[string]string{"0/5": class})
		if bad.Code != http.StatusBadRequest {
			t.Errorf("class %q answered %d, want 400", class, bad.Code)
		}
	}
	var intents, ok = poeIntents(map[string]string{
		"0/5": driver.PoEClassNone, "0/6": driver.PoEClass24V, "0/7": driver.PoEClass54V})
	if ok.Code != 0 {
		t.Fatalf("the three legitimate classes were refused: %d %q", ok.Code, ok.Msg)
	}
	if !reflect.DeepEqual(intents, []driver.PoEPortIntent{
		{Port: "0/5", Class: driver.PoEClassNone},
		{Port: "0/6", Class: driver.PoEClass24V},
		{Port: "0/7", Class: driver.PoEClass54V}}) {
		t.Fatalf("intents = %+v", intents)
	}
}

// TWO IDENTICAL CLICKS MUST PRODUCE TWO IDENTICAL TRANSCRIPTS, and Go randomises
// map iteration. This is not cosmetic on this endpoint: the plan's order is the
// order power is cut in, and a connector that stops at its first refusal writes
// a DIFFERENT SET OF PORTS depending on which order it got them in.
func TestPoEIntentsAreOrderedTheSameEveryTime(t *testing.T) {
	var body = map[string]string{
		"0/9": driver.PoEClassNone, "0/1": driver.PoEClass54V,
		"0/4": driver.PoEClassNone, "0/7": driver.PoEClass24V,
	}
	var first, _ = poeIntents(body)
	if first[0].Port != "0/1" {
		t.Fatalf("intents are not sorted: %+v", first)
	}
	var i int
	for i = 0; i < 50; i++ {
		var again, _ = poeIntents(body)
		if !reflect.DeepEqual(first, again) {
			t.Fatalf("run %d produced a different order:\n %+v\n %+v", i, first, again)
		}
	}
}

// --- the status codes, through a real ResponseRecorder ---

func answeredPoERefusal(t *testing.T, ref poeRefusal) (int, string) {
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

// A ZERO REFUSAL MUST WRITE NOTHING. The rung shape depends on it: a rung that
// answered a 0-status header would freeze everything the handler wrote after it.
func TestAZeroPoERefusalWritesNothing(t *testing.T) {
	var rec = httptest.NewRecorder()
	var zero poeRefusal
	if zero.respond(rec) {
		t.Fatal("the zero refusal claimed to have answered")
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("the zero refusal wrote %q", rec.Body.String())
	}
}

// 400: the request is wrong and no device was involved. Not 422 — nothing about
// the platform is in question — and above all not 500.
func TestARefusedPoEBodyIsFourHundred(t *testing.T) {
	var _, empty = poeIntents(nil)
	var code, msg = answeredPoERefusal(t, empty)
	if code != http.StatusBadRequest {
		t.Fatalf("an empty gesture answered %d, want 400", code)
	}
	if msg != "no ports in request" {
		t.Fatalf("message = %q", msg)
	}
	var _, blank = poeIntents(map[string]string{"  ": driver.PoEClassNone})
	var blankCode, blankMsg = answeredPoERefusal(t, blank)
	if blankCode != http.StatusBadRequest {
		t.Fatalf("a blank port name answered %d, want 400", blankCode)
	}
	if !strings.Contains(blankMsg, "empty name") {
		t.Fatalf("message = %q — it must name what is wrong with the body", blankMsg)
	}
	var _, badClass = poeIntents(map[string]string{"0/5": "48v"})
	var classCode, classMsg = answeredPoERefusal(t, badClass)
	if classCode != http.StatusBadRequest {
		t.Fatalf("an unknown class answered %d, want 400", classCode)
	}
	if !strings.Contains(classMsg, "0/5") {
		t.Fatalf("message = %q — it must name the port it is about", classMsg)
	}
}

// 422 on a device no connector was ever associated with, carrying
// driver.ConnectorFor's OWN sentence: it names the gesture that repairs each of
// its cases, and a message rewritten at this boundary would drop the repair.
func TestADeviceWithoutAConnectorIsFourTwentyTwoForPoE(t *testing.T) {
	var absent, absentErr = driver.ConnectorFor("")
	var code, msg = answeredPoERefusal(t, poePlatformRefusal(absent, absentErr))
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("an absent connector answered %d, want 422 — 502 would accuse the "+
			"network of a fault that is in the database", code)
	}
	if !strings.Contains(msg, "re-run discovery") {
		t.Fatalf("message = %q — the repair instruction did not survive", msg)
	}
	var unknown, unknownErr = driver.ConnectorFor("no-such-connector-ships-here")
	var unknownCode, unknownMsg = answeredPoERefusal(t,
		poePlatformRefusal(unknown, unknownErr))
	if unknownCode != http.StatusUnprocessableEntity {
		t.Fatalf("an unknown connector answered %d, want 422", unknownCode)
	}
	if unknownMsg == msg {
		t.Fatal("two refusals repaired by unrelated gestures carry the same sentence")
	}
	// A nil connector with no error is ANSWERED, not dereferenced: a panic here
	// would be a 500 from a handler whose whole job is to produce a status code.
	var nilCode, _ = answeredPoERefusal(t, poePlatformRefusal(nil, nil))
	if nilCode != http.StatusUnprocessableEntity {
		t.Fatalf("a nil connector answered %d, want 422", nilCode)
	}
}

// THE GUARD THAT MATTERS MOST ON THIS FEATURE. Only ubiquiti/edgeswitch declares
// "poe" writable; cisco/ios, hpe/procurve and fs/s3400 all record PoE as
// writable-on-this-platform and UNIMPLEMENTED, awaiting a command survey. This
// 422 is what stops a UI offering a power switch on those ranges — and "stop
// looking" is a different instruction from "try again", which is why it is
// not 502.
func TestAPlatformThatDoesNotDeclarePoEWritableIsFourTwentyTwo(t *testing.T) {
	var code, msg = answeredPoERefusal(t,
		poePlatformRefusal(snoopFakeConnector{writes: "stp"}, nil))
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("an undeclared capability answered %d, want 422", code)
	}
	if !strings.Contains(msg, "does not declare PoE") {
		t.Fatalf("message = %q", msg)
	}
	// And a connector that DOES declare it passes the rung untouched.
	var ref = poePlatformRefusal(snoopFakeConnector{writes: "poe"}, nil)
	if ref.Code != 0 {
		t.Fatalf("a declaring connector was refused %d %q", ref.Code, ref.Msg)
	}
}

// 502 when the device, or the path to it, said no — and the applier is not even
// looked for in that case. THE ORDER IS THE JUDGEMENT: adminFor can return an
// Admin AND an error at once (Instant On and RouterOS hand the session back so
// the transcript survives), so testing the interface first would answer "this
// platform cannot do PoE" about a device that merely refused the password.
func TestAnUnreachableDeviceIsFiveOhTwoForPoEEvenWhenASessionCameBack(t *testing.T) {
	var applier, ref = poeApplierFor(&poeSpyAdmin{}, errors.New("ssh: handshake failed"))
	var code, msg = answeredPoERefusal(t, ref)
	if code != http.StatusBadGateway {
		t.Fatalf("an unreachable device answered %d, want 502", code)
	}
	if !strings.Contains(msg, "handshake") {
		t.Fatalf("message = %q — the device's own words must survive", msg)
	}
	if applier != nil {
		t.Fatal("an applier was handed back for access that was never proven")
	}
}

// A connector that declares "poe" writable while its Admin has no ApplyPoE is
// a STAGING mismatch, not a hardware limit, and it is 422 with the same sentence
// the automatic path writes into its notes.
func TestAConnectorWithNoPoEApplierIsFourTwentyTwo(t *testing.T) {
	var applier, ref = poeApplierFor(&snoopPlainAdmin{}, nil)
	var code, msg = answeredPoERefusal(t, ref)
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("a half-built connector answered %d, want 422", code)
	}
	if !strings.Contains(msg, "no ApplyPoE") {
		t.Fatalf("message = %q", msg)
	}
	if applier != nil {
		t.Fatal("an applier was returned by a refusal")
	}
	// The happy rung: proven access plus a real applier refuses nothing.
	var good, ok = poeApplierFor(&poeSpyAdmin{}, nil)
	if ok.Code != 0 || good == nil {
		t.Fatalf("a complete connector was refused %d %q", ok.Code, ok.Msg)
	}
}

// The three codes must stay DISTINCT. Collapsing 422 into 502 sends an operator
// hunting a network fault that does not exist; collapsing 502 into 422 tells
// them their hardware cannot do something it does every day.
func TestThePoERefusalsKeepTheirDistinctCodes(t *testing.T) {
	var _, body = poeIntents(nil)
	var _, platform = poeApplierFor(&snoopPlainAdmin{}, nil)
	var device = poeDeviceRefusal(errors.New("connection refused"))
	if body.Code == platform.Code || platform.Code == device.Code || body.Code == device.Code {
		t.Fatalf("codes collapsed: body=%d platform=%d device=%d",
			body.Code, platform.Code, device.Code)
	}
	if poeDeviceRefusal(nil).Code != 0 {
		t.Fatal("a nil error produced a refusal")
	}
}

// --- the automatic path, proven by the applier never being called ---

// poeSpyAdmin adds ApplyPoE to the plain Admin double next door and records
// whether it was reached. It reaches no device: nothing here dials, so a call
// count of zero is proof that nothing could have gone out.
type poeSpyAdmin struct {
	snoopPlainAdmin
	applies int
	last    driver.PoEPlan
}

func (a *poeSpyAdmin) ApplyPoE(_ context.Context,
	plan driver.PoEPlan) (driver.ApplyResult, error) {
	a.applies++
	a.last = plan
	return driver.ApplyResult{}, nil
}

var _ driver.Admin = (*poeSpyAdmin)(nil)

// The doubles must really differ in what they implement, or every refusal test
// above is vacuous.
func TestThePoETestDoublesReallyDifferInWhatTheyImplement(t *testing.T) {
	var _, plainIsApplier = driver.AsPoEApplier(&snoopPlainAdmin{})
	if plainIsApplier {
		t.Fatal("snoopPlainAdmin implements ApplyPoE: the refusal tests measure nothing")
	}
	var _, spyIsApplier = driver.AsPoEApplier(&poeSpyAdmin{})
	if !spyIsApplier {
		t.Fatal("poeSpyAdmin does not implement ApplyPoE")
	}
}

// A port panel requesting PoE on a platform that does not declare it writable
// must produce NO gesture at all. Not a refused one — none.
func TestAutoPoESendsNothingWhenTheConnectorDoesNotDeclareItWritable(t *testing.T) {
	var s = &Server{}
	var adm = &poeSpyAdmin{}
	var d = model.Device{Ports: json.RawMessage(
		`{"0/5":{"mode":"access","poe":true}}`)}
	var res, err = s.applyDevicePoE(context.Background(), d,
		snoopFakeConnector{writes: "stp"}, adm)
	if err != nil {
		t.Fatalf("an undeclared capability is not an error: %v", err)
	}
	if adm.applies != 0 {
		t.Fatalf("ApplyPoE was called %d time(s) on a connector that does not declare "+
			"poe writable", adm.applies)
	}
	if len(res.PortsWritten) != 0 || len(res.Notes) != 0 || len(res.Skipped) != 0 {
		t.Fatalf("a result was manufactured for a gesture that never happened: %+v", res)
	}
}

func TestAutoPoESendsNothingWhenNoPortRequestsIt(t *testing.T) {
	var s = &Server{}
	var adm = &poeSpyAdmin{}
	var d = model.Device{Ports: json.RawMessage(`{"0/1":{"mode":"access","vlan":10}}`)}
	var _, err = s.applyDevicePoE(context.Background(), d,
		snoopFakeConnector{writes: "poe"}, adm)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if adm.applies != 0 {
		t.Fatalf("a device whose ports request nothing produced %d apply call(s)", adm.applies)
	}
}

// The optional interface is genuinely optional (cf. driver.PoEApplier): a
// connector may declare the capability before its Admin implements it, and that
// mismatch must be NAMED rather than crash or be silent.
func TestAutoPoENamesTheMissingOptionalInterface(t *testing.T) {
	var s = &Server{}
	var adm = &snoopPlainAdmin{}
	var d = model.Device{Ports: json.RawMessage(
		`{"0/5":{"mode":"access","poe":true}}`)}
	var res, err = s.applyDevicePoE(context.Background(), d,
		snoopFakeConnector{writes: "poe"}, adm)
	if err != nil {
		t.Fatalf("a staging mismatch is an ordinary result, not a Go error: %v", err)
	}
	if len(res.Notes) != 1 || !strings.Contains(res.Notes[0], "no ApplyPoE") {
		t.Fatalf("Notes = %v — the mismatch must be legible in the apply result", res.Notes)
	}
}

// ENABLE ONLY, and it is the invariant the whole automatic path rests on.
// recon.RequestedExtras reports POSITIVE intents only, so a stored
// `poe: false` — indistinguishable from a port that says nothing — must never
// become a request to CUT POWER. The class must also arrive EMPTY, which is what
// tells the connector to use its own default "on" rather than any class this
// layer picked (cf. driver.PoEPortIntent).
func TestAutoPoENeverCutsPowerAndNamesNoClass(t *testing.T) {
	var s = &Server{}
	var adm = &poeSpyAdmin{}
	var d = model.Device{Ports: json.RawMessage(
		`{"0/5":{"mode":"access","poe":true},"0/6":{"mode":"access","poe":false}}`)}
	var _, err = s.applyDevicePoE(context.Background(), d,
		snoopFakeConnector{writes: "poe"}, adm)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if adm.applies != 1 {
		t.Fatalf("applies = %d, want 1", adm.applies)
	}
	if len(adm.last.Ports) != 1 || adm.last.Ports[0].Port != "0/5" {
		t.Fatalf("plan = %+v — a `poe: false` port must not be in it at all: it is a "+
			"neutral value, and cutting power on it would be an outage nobody asked for",
			adm.last.Ports)
	}
	if adm.last.Ports[0].Class != "" {
		t.Fatalf("class = %q on the automatic path — it must stay empty so the "+
			"connector applies its own platform default \"on\"", adm.last.Ports[0].Class)
	}
}
