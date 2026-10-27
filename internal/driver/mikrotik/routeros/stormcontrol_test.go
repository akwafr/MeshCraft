package routeros

import (
	"strings"
	"testing"

	"meshcraft/internal/driver"
)

// Declared 2026-08-09. The unit stopped being an inference on 2026-08-07 (a port
// set to 1 % from the device's own UI read back as 1), and the gesture itself was
// sent on 2026-08-09 against ether2 of the lab CRS326: written, confirmed from an
// independent session, converged on a second apply, and the whole switch-port
// table compared equal afterwards. capabilities.go carries the evidence.
func TestWritesExtraStormControlIsDeclared(t *testing.T) {
	if !WritesExtra("storm_control") {
		t.Fatal("storm_control was proven on hardware on 2026-08-09 and the declaration " +
			"has been lost — the operator is being told meshcraft cannot do something it can")
	}
	if !WritesExtra("stp") || !WritesExtra("port_admin") {
		t.Fatal("the capabilities this connector does declare were lost")
	}
}

// The capability answer must carry all three shape facts a screen needs before a
// click: which classes exist, that the rate is SHARED, and that the unit is
// inferred rather than captured.
func TestStormControlCapsDeclareSharedRateAndRefuseMulticast(t *testing.T) {
	var caps = (&Driver{}).StormControlCaps()
	if !caps.Expresses(driver.StormBroadcast) || !caps.Expresses(driver.StormUnknownUnicast) {
		t.Error("the two classes this platform names as themselves are not declared")
	}
	// "limit-unknown-multicasts" is NARROWER than the multicast class: it covers
	// only what the snooping table has no entry for, so its coverage depends on
	// whether IGMP snooping is on — a limiter nobody could reason about.
	if caps.Expresses(driver.StormMulticast) {
		t.Fatal("multicast is declared on a platform whose only field is " +
			"\"limit-unknown-multicasts\"")
	}
	if !caps.SharedRate {
		t.Fatal("\"storm-rate\" is one field per port and the caps do not say so — a screen " +
			"reading this would offer three rate boxes, two of which are discarded")
	}
	// The capability answer must state what the rate MEANS. It used to have to
	// admit the unit was an inference; that inference has been settled on
	// hardware (see stormcontrol.go's header), so what it must carry now is the
	// captured unit and the one counter-intuitive consequence of it.
	var joined = strings.Join(caps.Why, " ")
	if !strings.Contains(joined, "PERCENTAGE") {
		t.Error("the capability answer does not say what the rate unit is")
	}
	if strings.Contains(joined, "INFERRED, NOT CAPTURED") {
		t.Error("the capability answer still calls the unit an inference: it was captured " +
			"on a CRS326-24G-2S+ running RouterOS 7.21beta3")
	}
}

// THE SHARED-RATE REFUSAL. Two enabled classes naming two different rates cannot
// both be honoured by one field, and NOTHING is written for that port — not the
// first, not the larger, not the one that sorted first.
func TestPlanStormPortRefusesTwoRatesOnOnePort(t *testing.T) {
	var have = stormPortRow{
		ID: "*1", Name: "ether3",
		Has: map[string]bool{
			fieldLimitBroadcasts: true, fieldLimitUnknownUni: true,
		},
		Flags: map[string]bool{},
	}
	var w = planStormPort("ether3", have, []driver.StormLimit{
		{Class: driver.StormBroadcast, Enabled: true, Rate: 5, Unit: driver.StormRatePercent},
		{Class: driver.StormUnknownUnicast, Enabled: true, Rate: 20, Unit: driver.StormRatePercent},
	})
	if len(w.Fields) != 0 {
		t.Fatalf("a port with two conflicting rates was written anyway: %v", w.Fields)
	}
	if len(w.Skipped) != 1 {
		t.Fatalf("expected exactly one refusal, got %v", w.Skipped)
	}
	// BOTH numbers must appear: an operator has to be able to see which two
	// values collided without opening the plan again.
	if !strings.Contains(w.Skipped[0], "5") || !strings.Contains(w.Skipped[0], "20") {
		t.Errorf("the refusal does not name both rates: %q", w.Skipped[0])
	}
}

// The SAME rate on two classes is not a conflict, and must go through as one
// write carrying both flags and one shared rate.
func TestPlanStormPortAcceptsAgreeingRates(t *testing.T) {
	var have = stormPortRow{
		ID: "*1", Name: "ether3",
		Has:   map[string]bool{fieldLimitBroadcasts: true, fieldLimitUnknownUni: true},
		Flags: map[string]bool{},
	}
	var w = planStormPort("ether3", have, []driver.StormLimit{
		{Class: driver.StormBroadcast, Enabled: true, Rate: 5, Unit: driver.StormRatePercent},
		{Class: driver.StormUnknownUnicast, Enabled: true, Rate: 5, Unit: driver.StormRatePercent},
	})
	if len(w.Skipped) != 0 {
		t.Fatalf("agreeing rates were refused: %v", w.Skipped)
	}
	if w.Fields[fieldLimitBroadcasts] != boolTrue ||
		w.Fields[fieldLimitUnknownUni] != boolTrue ||
		w.Fields[stormRateField] != "5" {
		t.Fatalf("write body = %v", w.Fields)
	}
}

// A REMOVAL names no rate and constrains nothing: disabling broadcast while
// enabling unknown-unicast at 20 is a coherent gesture, not a conflict.
func TestPlanStormPortRemovalDoesNotConstrainTheSharedRate(t *testing.T) {
	var have = stormPortRow{
		ID: "*1", Name: "ether3",
		Has:   map[string]bool{fieldLimitBroadcasts: true, fieldLimitUnknownUni: true},
		Flags: map[string]bool{fieldLimitBroadcasts: true},
	}
	var w = planStormPort("ether3", have, []driver.StormLimit{
		{Class: driver.StormBroadcast, Enabled: false},
		{Class: driver.StormUnknownUnicast, Enabled: true, Rate: 20, Unit: driver.StormRatePercent},
	})
	if len(w.Skipped) != 0 {
		t.Fatalf("a removal beside an enable was refused: %v", w.Skipped)
	}
	if w.Fields[fieldLimitBroadcasts] != boolFalse {
		t.Errorf("the removal was not written: %v", w.Fields)
	}
	if w.Fields[stormRateField] != "20" {
		t.Errorf("the shared rate = %q, want 20", w.Fields[stormRateField])
	}
}

// A class the platform cannot express is refused per port, and the OTHER classes
// in the same intent are still written. Killing the gesture would cost the
// operator a limiter this hardware genuinely has.
func TestPlanStormPortRefusesMulticastAndWritesTheRest(t *testing.T) {
	var have = stormPortRow{
		ID: "*1", Name: "ether3",
		Has:   map[string]bool{fieldLimitBroadcasts: true, fieldLimitUnknownUni: true},
		Flags: map[string]bool{},
	}
	var w = planStormPort("ether3", have, []driver.StormLimit{
		{Class: driver.StormMulticast, Enabled: true, Rate: 5, Unit: driver.StormRatePercent},
		{Class: driver.StormBroadcast, Enabled: true, Rate: 5, Unit: driver.StormRatePercent},
	})
	if len(w.NotApplied) != 1 || !strings.Contains(w.NotApplied[0].Text, "limit-unknown-multicasts") {
		t.Fatalf("the multicast refusal does not name the field: %v", w.NotApplied)
	}
	if w.Fields[fieldLimitBroadcasts] != boolTrue {
		t.Error("the broadcast limit was lost with the refused multicast one")
	}
	// And it must never be written into the unknown-multicast field.
	var _, wrote = w.Fields[fieldLimitUnknownMcast]
	if wrote {
		t.Fatal("a multicast request was substituted into \"limit-unknown-multicasts\"")
	}
}

// A unit this platform does not speak is refused, NOT converted — and the other
// class in the plan still lands.
func TestPlanStormPortRefusesForeignUnits(t *testing.T) {
	var have = stormPortRow{
		ID: "*1", Name: "ether3",
		Has:   map[string]bool{fieldLimitBroadcasts: true, fieldLimitUnknownUni: true},
		Flags: map[string]bool{},
	}
	var w = planStormPort("ether3", have, []driver.StormLimit{
		{Class: driver.StormBroadcast, Enabled: true, Rate: 1000, Unit: driver.StormRatePPS},
	})
	if len(w.Fields) != 0 {
		t.Fatalf("a packet rate was written into a percentage field: %v", w.Fields)
	}
	if !strings.Contains(notAppliedTexts(w.NotApplied), "Nothing was converted") {
		t.Errorf("the refusal does not say the number was left alone: %v", w.NotApplied)
	}
}

// A field the firmware does not serve is a refusal naming it, never a write into
// something adjacent.
func TestPlanStormPortRefusesUnservedField(t *testing.T) {
	var have = stormPortRow{
		ID: "*1", Name: "ether3",
		Has:   map[string]bool{fieldLimitBroadcasts: true},
		Flags: map[string]bool{},
	}
	var w = planStormPort("ether3", have, []driver.StormLimit{
		{Class: driver.StormUnknownUnicast, Enabled: true, Rate: 5, Unit: driver.StormRatePercent},
	})
	if len(w.Fields) != 0 {
		t.Fatalf("a write was built for a field the firmware does not serve: %v", w.Fields)
	}
	if !strings.Contains(notAppliedTexts(w.NotApplied), fieldLimitUnknownUni) {
		t.Errorf("the refusal does not name the missing field: %v", w.NotApplied)
	}
}

// The read: the observed record shape, folded and turned into contract state.
//
// The row below is the capture Capabilities().Features has carried since this
// connector was written.
func TestStormStateOfObservedRecord(t *testing.T) {
	var rows = []Row{{
		".id": "*1", "name": "ether1",
		fieldLimitBroadcasts:   "true",
		fieldLimitUnknownUni:   "false",
		fieldLimitUnknownMcast: "false",
		stormRateField:         "100",
	}}
	var st = stormStateOf(foldStormRows(rows))
	if len(st.Ports) != 1 || st.Ports[0].Port != "ether1" {
		t.Fatalf("ports = %+v", st.Ports)
	}
	var bc, ok = st.Ports[0].Limit(driver.StormBroadcast)
	if !ok || !bc.Enabled || bc.Rate != 100 || bc.Unit != driver.StormRatePercent {
		t.Errorf("broadcast = %+v", bc)
	}
	var uu, _ = st.Ports[0].Limit(driver.StormUnknownUnicast)
	if uu.Enabled {
		t.Error("unknown-unicast reads as limited where the flag is false")
	}
	// The multicast class must NOT appear at all: this platform serves no field
	// that means it, and a present-and-disabled entry would invite a screen to
	// offer changing it.
	var _, read = st.Ports[0].Limit(driver.StormMulticast)
	if read {
		t.Fatal("\"limit-unknown-multicasts\" was folded into the multicast class")
	}
	// Both shape notes must ride on every read.
	//
	// The second one USED to assert the unit was "INFERRED, NOT CAPTURED", and
	// that assertion is gone because the inference was settled on hardware: an
	// operator set ether1 to 1 % from the device's own UI and it read back as
	// `storm-rate=1` on a CRS326-24G-2S+ (RouterOS 7.21beta3).
	//
	// What replaces it is not "nothing". The number is still surprising in one
	// direction — 100 is the DEFAULT and means NO LIMITING — so a plan asking
	// for 100 % switches off the limiter it meant to configure. That is the fact
	// that has to ride on every read now, and it is what this asserts.
	var notes = strings.Join(st.Notes, " ")
	if !strings.Contains(notes, "ONE value per port") {
		t.Errorf("the read notes lose the shared-rate note: %v", st.Notes)
	}
	if !strings.Contains(notes, "PERCENTAGE") || !strings.Contains(notes, "NO LIMITING") {
		t.Errorf("the read notes no longer say what the rate MEANS, or that 100 disables "+
			"the limiter: %v", st.Notes)
	}
}

// A class switched ON whose rate cannot be read is left UNREAD, never reported as
// enabled at zero: "every frame of this class is dropped" is a claim about a
// live port that nobody made.
func TestStormStateOfLeavesEnabledClassUnreadWithoutARate(t *testing.T) {
	var rows = []Row{{
		".id": "*1", "name": "ether1",
		fieldLimitBroadcasts: "true",
	}}
	var st = stormStateOf(foldStormRows(rows))
	var _, read = st.Ports[0].Limit(driver.StormBroadcast)
	if read {
		t.Fatal("a limiter with no readable rate was reported as configured")
	}
}

// A firmware serving "interface" in place of "name" must still be indexable:
// a table this connector could key by nothing reads on screen as "no port has
// storm control", which is a claim about a device that answered in full.
func TestFoldStormRowsFallsBackToInterfaceName(t *testing.T) {
	var got = foldStormRows([]Row{{
		".id": "*2", "interface": "sfp-sfpplus1", fieldLimitBroadcasts: "true",
	}})
	var p, ok = got["sfp-sfpplus1"]
	if !ok || p.ID != "*2" {
		t.Fatalf("row not indexed: %+v", got)
	}
}

// A class SWITCHED ON at "storm-rate" 100 is armed and polices nothing, and the
// read has to say so.
//
// # Why this is the ordinary case and not an edge one
//
// `limit-broadcasts=true` beside `storm-rate=100` is the FACTORY state of every
// port on this hardware — twenty-five of the lab CRS326's twenty-seven records
// read that way, untouched, on 2026-08-09. Without the note an operator opening a
// pristine switch sees twenty-five ports listed as broadcast-limited when not one
// of them caps anything.
//
// THE OBSERVATION ITSELF MUST NOT MOVE, which is the other half of what this
// pins. Reporting Enabled false would hide a bit that is genuinely set, and a
// later rate change to 50 would then start policing a class the screen had called
// off; dropping the limit from the list would claim the class was not read, which
// is what an absence is reserved for.
func TestStormStateOfNamesThePortsArmedAtTheNoLimitRate(t *testing.T) {
	var rows = []Row{
		{".id": "*1", "name": "ether1",
			fieldLimitBroadcasts: "true", stormRateField: "100"},
		{".id": "*2", "name": "ether2",
			fieldLimitBroadcasts: "true", stormRateField: "10"},
		{".id": "*3", "name": "ether3",
			fieldLimitBroadcasts: "false", stormRateField: "100"},
	}
	var st = stormStateOf(foldStormRows(rows))

	// The reading is unchanged: ether1 is still enabled, still at 100, still
	// percent. The note is an addition, never a redaction.
	var bc, ok = st.Ports[0].Limit(driver.StormBroadcast)
	if !ok || !bc.Enabled || bc.Rate != 100 || bc.Unit != driver.StormRatePercent {
		t.Fatalf("the armed-at-100 port must still read exactly as observed: %+v", bc)
	}

	var notes = strings.Join(st.Notes, " ")
	if !strings.Contains(notes, "ARMED BUT NOT POLICING") {
		t.Fatalf("a port armed at the no-limit rate is not called out: %v", st.Notes)
	}
	if !strings.Contains(notes, "ether1") {
		t.Errorf("the note must NAME the port: %v", st.Notes)
	}
	// ether2 caps at 10 % and ether3's flag is off. Neither is armed-and-idle,
	// and a note sweeping them in would be as misleading as no note at all.
	if strings.Contains(notes, "ether2") || strings.Contains(notes, "ether3") {
		t.Errorf("a really-policing port and a switched-off one were swept into the "+
			"armed-but-idle note: %v", st.Notes)
	}
}

// No port armed at the no-limit rate, no note. A sentence that appears on every
// read is a sentence nobody reads, and the ones this file does emit unconditionally
// (the shared rate, the unit) earn it by being true of the platform rather than of
// one observation.
func TestStormStateOfStaysSilentWhenNoPortIsArmedAtTheNoLimitRate(t *testing.T) {
	var st = stormStateOf(foldStormRows([]Row{{
		".id": "*1", "name": "ether1",
		fieldLimitBroadcasts: "true", stormRateField: "5",
	}}))
	if strings.Contains(strings.Join(st.Notes, " "), "ARMED BUT NOT POLICING") {
		t.Fatalf("a port limited at 5 %% must not be described as policing nothing: %v",
			st.Notes)
	}
}

// stormArmedAtNoLimit judges the LIMIT LIST, not the boolean alone, and the
// distinction is the whole point: on this hardware the class flag is set out of
// the factory, so anything deciding whether a port is really policed has to read
// the rate too.
func TestStormArmedAtNoLimitReadsTheRateNotJustTheFlag(t *testing.T) {
	var cases = []struct {
		name   string
		limits []driver.StormLimit
		want   bool
	}{
		{"enabled at the no-limit rate", []driver.StormLimit{{
			Class: driver.StormBroadcast, Enabled: true,
			Rate: stormRateNoLimit, Unit: driver.StormRatePercent,
		}}, true},
		{"enabled at a real threshold", []driver.StormLimit{{
			Class: driver.StormBroadcast, Enabled: true,
			Rate: 5, Unit: driver.StormRatePercent,
		}}, false},
		{"disabled", []driver.StormLimit{{
			Class: driver.StormBroadcast, Enabled: false,
		}}, false},
		{"one class idle beside one that bites", []driver.StormLimit{
			{Class: driver.StormBroadcast, Enabled: true,
				Rate: stormRateNoLimit, Unit: driver.StormRatePercent},
			{Class: driver.StormUnknownUnicast, Enabled: true,
				Rate: stormRateNoLimit, Unit: driver.StormRatePercent},
		}, true},
		{"nothing read at all", nil, false},
	}
	var i int
	for i = range cases {
		var got = stormArmedAtNoLimit(cases[i].limits)
		if got != cases[i].want {
			t.Errorf("%s: stormArmedAtNoLimit = %v, want %v",
				cases[i].name, got, cases[i].want)
		}
	}
}

// notAppliedTexts flattens entries for substring assertions.
func notAppliedTexts(entries []driver.NotApplied) string {
	var parts []string
	var e driver.NotApplied
	for _, e = range entries {
		parts = append(parts, e.Text)
	}
	return strings.Join(parts, " ")
}
