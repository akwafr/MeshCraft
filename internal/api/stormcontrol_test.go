package api

import (
	"encoding/json"
	"meshcraft/internal/model"
	"strings"
	"testing"

	"meshcraft/internal/driver"
)

// The boundary's judgements, asserted as the FAILURES THEY PREVENT. Every body
// below would otherwise have opened a session on a switch to be refused there —
// or, worse, not refused at all.

// A RATE WITH NO UNIT is the one this whole feature is built around. "10" means
// ten percent of a gigabit on one switch in a rack and ten packets a second on
// the one beside it, and the refusal has to happen here, where the message can
// name the field, rather than as a 502 from a device that was never the problem.
func TestStormIntentsRefuseRateWithoutUnit(t *testing.T) {
	var _, bad = stormIntents(map[string][]stormLimitBody{
		"GE5": {{Class: "broadcast", Enabled: true, Rate: 10}},
	}, nil)
	if bad.Code == 0 {
		t.Fatal("a threshold with no unit was accepted at the boundary")
	}
	if !strings.Contains(bad.Msg, "unit is mandatory") {
		t.Errorf("the 400 does not say the unit is mandatory: %q", bad.Msg)
	}
}

// A RATE OF ZERO on an enabled limit is not "no limit". On this fleet it drops
// every frame of the class, and on broadcast that takes ARP with it — the port
// stops passing anything at all while the screen shows a limiter.
func TestStormIntentsRefuseZeroRate(t *testing.T) {
	var _, bad = stormIntents(map[string][]stormLimitBody{
		"GE5": {{Class: "broadcast", Enabled: true, Rate: 0, Unit: "percent"}},
	}, nil)
	if bad.Code == 0 {
		t.Fatal("a zero threshold was accepted on an enabled limiter")
	}
	if !strings.Contains(bad.Msg, "send it disabled") {
		t.Errorf("the 400 does not point at the removal spelling: %q", bad.Msg)
	}
}

// A class the contract does not carry — "unicast", the broader keyword several
// platforms offer — must not travel as if it were one of the three.
func TestStormIntentsRefuseUnknownClass(t *testing.T) {
	var _, bad = stormIntents(map[string][]stormLimitBody{
		"GE5": {{Class: "unicast", Enabled: true, Rate: 5, Unit: "percent"}},
	}, nil)
	if bad.Code == 0 {
		t.Fatal("\"unicast\" was accepted as a traffic class")
	}
}

// An empty map, an empty port name, and a port named with no limits are all
// malformed rather than no-ops: a port meshcraft has nothing to say about is left
// out of the request.
func TestStormIntentsRefuseMalformedBodies(t *testing.T) {
	var _, bad = stormIntents(nil, nil)
	if bad.Msg != "no ports in request" {
		t.Errorf("empty body = %q", bad.Msg)
	}
	_, bad = stormIntents(map[string][]stormLimitBody{
		"  ": {{Class: "broadcast", Enabled: false}},
	}, nil)
	if bad.Code == 0 {
		t.Error("a port with an empty name was accepted")
	}
	_, bad = stormIntents(map[string][]stormLimitBody{"GE5": {}}, nil)
	if bad.Code == 0 {
		t.Error("a port named with no limits was accepted")
	}
}

// A well-formed body must survive intact, in a STABLE order — two identical
// gestures have to produce two identical transcripts, and on command forms
// nothing has captured yet a transcript is how the form gets learned.
func TestStormIntentsPreserveTheBodyAndSortStably(t *testing.T) {
	var intents, bad = stormIntents(map[string][]stormLimitBody{
		"GE9": {{Class: "multicast", Enabled: false}},
		"GE1": {
			{Class: "broadcast", Enabled: true, Rate: 5, Unit: "percent"},
			{Class: "unknown_unicast", Enabled: true, Rate: 1000, Unit: "pps"},
		},
	}, nil)
	if bad.Code != 0 {
		t.Fatalf("a valid body was refused: %d %q", bad.Code, bad.Msg)
	}
	if len(intents) != 2 || intents[0].Port != "GE1" || intents[1].Port != "GE9" {
		t.Fatalf("intents = %+v", intents)
	}
	var uu, ok = intents[0].Limit(driver.StormUnknownUnicast)
	if !ok || uu.Rate != 1000 || uu.Unit != driver.StormRatePPS {
		t.Errorf("the unit did not survive the boundary: %+v", uu)
	}
}

// The EVENT LINE is read months later by somebody hunting for when an application
// went slow. It must spell the unit out: a bare "5" in a log tells nobody
// whether a port was limited to five percent or five packets a second.
func TestStormApplySummarySpellsTheUnitOut(t *testing.T) {
	var line = stormApplySummary([]driver.StormControlPortIntent{{
		Port: "GE5",
		Limits: []driver.StormLimit{
			{Class: driver.StormMulticast, Enabled: false},
			{Class: driver.StormBroadcast, Enabled: true, Rate: 5, Unit: driver.StormRatePercent},
		},
	}})
	if !strings.Contains(line, "% of line rate") {
		t.Errorf("the event line does not name the unit: %q", line)
	}
	if !strings.Contains(line, "removed") {
		t.Errorf("the event line does not distinguish a removal: %q", line)
	}
	// Canonical class order, so two identical gestures produce two identical
	// lines.
	if strings.Index(line, "broadcast") > strings.Index(line, "multicast") {
		t.Errorf("the classes are not in canonical order: %q", line)
	}
}

// THE TWO CAPABILITY QUESTIONS MUST STAY APART. "this hardware cannot" and
// "meshcraft has not proven the form yet" lead an operator to opposite
// conclusions, and merging them into one flag is the defect the PoE power-cycle
// button carried for a while.
func TestStormControlCapsViewKeepsCannotApartFromNotYet(t *testing.T) {
	// A platform that expresses everything and has no proven form: Classes full,
	// Write false. The UI must say "not verified on this range yet".
	var notYet = StormControlCapsView{
		Known:   true,
		Classes: driver.StormClasses,
		Write:   false,
	}
	if len(notYet.Classes) == 0 || notYet.Write {
		t.Fatal("the fixture does not describe the \"not yet\" case")
	}
	// A platform that expresses nothing: Classes empty, and a reason to show.
	var cannot = StormControlCapsView{
		Known: true,
		Why:   []string{"this range has one device-wide storm control"},
	}
	if len(cannot.Classes) != 0 || len(cannot.Why) == 0 {
		t.Fatal("the fixture does not describe the \"cannot\" case")
	}
	// And "we could not resolve the connector", which is neither: Known false.
	var unknown = StormControlCapsView{}
	if unknown.Known {
		t.Fatal("an unresolved connector must not claim to know anything")
	}
}

// A platform refusal must come back as 422 and a device that misbehaved as 502.
// One tells the operator to stop looking, the other to try again, and answering
// with the wrong one sends them off in the wrong direction.
func TestIsUnsupportedRecognisesCapabilityRefusals(t *testing.T) {
	var refusal = driver.NotSupported("aruba-instant-on-1930",
		"limit storms on one port", "this range has no per-port dimension")
	if !isUnsupported(refusal) {
		t.Error("a driver.Unsupported was not recognised as a capability refusal")
	}
	if isUnsupported(errStormTestTransport{}) {
		t.Error("an ordinary transport error was read as a capability refusal")
	}
}

type errStormTestTransport struct{}

func (errStormTestTransport) Error() string { return "dial tcp: connection refused" }

// --- The profile's declared intent, read back out of devices.ports ---

// A PORT THAT DECLARES NOTHING PRODUCES NO INTENT.
//
// The distinction this protects is the one that deletes configuration: absent
// means "no profile has an opinion", and turning that into an explicit disabled
// intent would make every automatic apply strip limiters an operator set by hand
// on the switch. Every port written before this feature existed is in exactly
// that state, so getting it wrong would clear the fleet on the next apply.
func TestPortsDeclaringNoStormControlProduceNoIntent(t *testing.T) {
	var raw = json.RawMessage(`{
		"GE1": {"mode":"access","pvid":10},
		"GE2": {"mode":"trunk","security":{}},
		"GE3": {"mode":"access","security":{"dhcp_snooping":{"trusted":true}}}
	}`)
	var got = portStormIntents(raw, nil)
	if len(got) != 0 {
		t.Fatalf("ports with no storm_control produced %d intents: %+v", len(got), got)
	}
}

// A DECLARED BLOCK travels with every class it names — including the ones
// switched OFF, because "this profile wants no broadcast limiter" is a real
// intent and the connector needs it to clear one.
func TestDeclaredStormControlTravelsWithEveryClass(t *testing.T) {
	var raw = json.RawMessage(`{
		"GE5": {"mode":"access","security":{"storm_control":
			{"broadcast":true,"multicast":false,"unknown_unicast":true,
			 "rate":1,"unit":"percent"}}}
	}`)
	var got = portStormIntents(raw, map[string]int{"GE5": 1000})
	if len(got) != 1 {
		t.Fatalf("%d intents, want one", len(got))
	}
	if got[0].SpeedMbps != 1000 {
		t.Errorf("the port's line rate did not travel: %d — a pps platform cannot "+
			"convert the percentage without it", got[0].SpeedMbps)
	}
	if len(got[0].Limits) != 3 {
		t.Fatalf("%d limits, want all three classes: %+v", len(got[0].Limits), got[0].Limits)
	}
	var b, ok = got[0].Limit(driver.StormBroadcast)
	if !ok || !b.Enabled || b.Rate != 1 || b.Unit != driver.StormRatePercent {
		t.Errorf("broadcast limit = %+v, want enabled 1%%", b)
	}
	// The class switched off carries no rate: a disabled limiter with a number
	// beside it invites a reader to think the number is in force.
	var m, _ = got[0].Limit(driver.StormMulticast)
	if m.Enabled || m.Rate != 0 {
		t.Errorf("multicast limit = %+v, want disabled with no rate", m)
	}
}

// A DECLARATION WITH NO UNIT PRODUCES NOTHING.
//
// Guessing percent would write, on the two ranges whose field is packets per
// second, a limiter five orders of magnitude away from anything intended.
func TestStormDeclarationWithoutUnitIsSkipped(t *testing.T) {
	var raw = json.RawMessage(`{
		"GE5": {"mode":"access","security":{"storm_control":
			{"broadcast":true,"rate":10}}}
	}`)
	var got = portStormIntents(raw, nil)
	if len(got) != 0 {
		t.Fatalf("a rate with no unit produced an intent: %+v", got)
	}
}

// The faceplate reader turns port groups into per-port line rates, and refuses
// to invent one for a label it does not recognise — which is what keeps an
// assumed capacity out of a live limiter.
func TestStormPortSpeedsReadsTheFaceplate(t *testing.T) {
	var d = model.Device{PortGroups: json.RawMessage(
		`[{"prefix":"GE","count":2,"from":1,"speed":"1G"},
		  {"prefix":"XE","count":2,"from":1,"speed":"10G"},
		  {"prefix":"ZZ","count":1,"from":1,"speed":"auto"}]`)}
	var got = stormPortSpeeds(d)
	if got["GE1"] != 1000 || got["GE2"] != 1000 {
		t.Errorf("1G group = %v", got)
	}
	if got["XE1"] != 10000 {
		t.Errorf("10G group = %v", got)
	}
	var _, invented = got["ZZ1"]
	if invented {
		t.Error("an unparseable speed label produced a capacity anyway")
	}
}
