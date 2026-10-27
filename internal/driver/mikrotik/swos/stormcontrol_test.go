package swos

import (
	"strings"
	"testing"

	"meshcraft/internal/driver"
)

// Declared 2026-08-08, and unlike this connector's other two extras it was
// declared on a gesture that was SENT: /fwd.b was posted to the lab CSS326, the
// change was observed from an independent session, a second apply converged, and
// the whole page came back as found. capabilities.go carries the evidence and
// live_stormcontrol_test.go carries the exercise.
func TestWritesExtraStormControlIsDeclared(t *testing.T) {
	if !WritesExtra("storm_control") {
		t.Fatal("storm_control was proven on hardware on 2026-08-08 and the declaration " +
			"has been lost — the operator is being told meshcraft cannot do something it can")
	}
	if !WritesExtra("stp") || !WritesExtra("port_admin") {
		t.Fatal("the capabilities this connector does declare were lost")
	}
}

// ONE class, and the two absences are grounded in the device's own enumeration
// rather than in a failed search.
func TestStormControlCapsDeclareUnknownUnicastOnly(t *testing.T) {
	var caps = (&Driver{}).StormControlCaps()
	if !caps.Expresses(driver.StormUnknownUnicast) {
		t.Error("\"suni\" is a per-port unknown-unicast limiter and it is not declared")
	}
	if caps.Expresses(driver.StormBroadcast) {
		t.Fatal("broadcast is declared on a platform whose enumerated /fwd.b field set " +
			"contains no broadcast field")
	}
	if caps.Expresses(driver.StormMulticast) {
		t.Fatal("multicast is declared on a platform whose only multicast field is a FLOOD " +
			"toggle, not a rate limiter")
	}
	if !caps.SharedRate {
		t.Error("\"srt\" is one value per port and the caps do not say so")
	}
	var joined = strings.Join(caps.Why, " ")
	// The refusals must be grounded in the enumeration, and must name the field
	// an operator would otherwise go hunting for in the UI.
	if !strings.Contains(joined, "configuration backup") {
		t.Error("the broadcast refusal does not say the absence is an enumeration")
	}
	if !strings.Contains(joined, "Flood Unknown Multicast") {
		t.Error("the multicast refusal does not name the field it declined to hijack")
	}
}

// THE TWO SUBSTITUTIONS THIS FILE EXISTS TO REFUSE, asserted as refusals.
//
// Writing `srt` for a broadcast request would install a threshold whose coverage
// cannot be stated; writing `fmc` for a multicast one would STOP the port
// flooding unregistered multicast rather than limit it — a drop, not a threshold.
func TestPlanStormPortRefusesBroadcastAndMulticast(t *testing.T) {
	var p = planStormPort("GE1", []driver.StormLimit{
		{Class: driver.StormBroadcast, Enabled: true, Rate: 5, Unit: driver.StormRatePercent},
		{Class: driver.StormMulticast, Enabled: true, Rate: 5, Unit: driver.StormRatePercent},
	})
	if p.HasWant {
		t.Fatal("a broadcast or multicast request produced something to write")
	}
	if len(p.NotApplied) != 2 {
		t.Fatalf("expected two refusals, got %v", p.NotApplied)
	}
	var joined = notAppliedTexts(p.NotApplied)
	if !strings.Contains(joined, "no broadcast storm field") {
		t.Error("the broadcast refusal does not state the absence")
	}
	if !strings.Contains(joined, "STOPS the port flooding") {
		t.Error("the multicast refusal does not say that `fmc` is a drop, not a threshold")
	}
	if !strings.Contains(joined, "mikrotik-swos") {
		t.Error("the refusals do not say which connector said no")
	}
}

// The one class this platform DOES name as itself goes through, and the refusals
// beside it do not cost it.
func TestPlanStormPortAcceptsUnknownUnicastBesideRefusals(t *testing.T) {
	var p = planStormPort("GE1", []driver.StormLimit{
		{Class: driver.StormBroadcast, Enabled: true, Rate: 5, Unit: driver.StormRatePercent},
		{Class: driver.StormUnknownUnicast, Enabled: true, Rate: 20, Unit: driver.StormRatePercent},
	})
	if !p.HasWant {
		t.Fatal("the unknown-unicast limiter was lost with the refused broadcast one")
	}
	if p.Want.Rate != 20 || p.Want.Unit != driver.StormRatePercent {
		t.Errorf("want = %+v", p.Want)
	}
}

// A rate in packets per second is refused rather than converted, and a fractional
// percentage is refused rather than rounded: `srt` stores a whole percentage, and
// picking 5 or 6 for "5.5" would be the connector choosing a threshold the
// operator did not type.
func TestPlanStormPortRefusesForeignUnitsAndFractions(t *testing.T) {
	var pps = planStormPort("GE1", []driver.StormLimit{
		{Class: driver.StormUnknownUnicast, Enabled: true, Rate: 1000, Unit: driver.StormRatePPS},
	})
	if pps.HasWant {
		t.Fatal("a packet rate was accepted into a percentage field")
	}
	if !strings.Contains(notAppliedTexts(pps.NotApplied), "Nothing was converted") {
		t.Errorf("the unit refusal does not say the number was left alone: %v", pps.NotApplied)
	}

	var frac = planStormPort("GE1", []driver.StormLimit{
		{Class: driver.StormUnknownUnicast, Enabled: true, Rate: 5.5, Unit: driver.StormRatePercent},
	})
	if frac.HasWant {
		t.Fatal("a fractional percentage was accepted and would have been rounded")
	}
	// Out of range is refused for the same reason, on the same path.
	var over = planStormPort("GE1", []driver.StormLimit{
		{Class: driver.StormUnknownUnicast, Enabled: true, Rate: 0.5, Unit: driver.StormRatePercent},
	})
	if over.HasWant {
		t.Fatal("a sub-1 percentage was accepted into a field that stores whole percents")
	}
}

// A REMOVAL carries no rate and must go through: "take the unknown-unicast
// limiter off this port" is a complete instruction, and it clears the bit rather
// than writing a rate of zero.
func TestPlanStormPortAcceptsRemoval(t *testing.T) {
	var p = planStormPort("GE1", []driver.StormLimit{
		{Class: driver.StormUnknownUnicast, Enabled: false},
	})
	if !p.HasWant || p.Want.Enabled {
		t.Fatalf("a removal was not accepted: %+v", p)
	}
	if len(p.Skipped) != 0 {
		t.Errorf("a removal produced refusals: %v", p.Skipped)
	}
}

// The rate this platform stores, rendered for a transcript.
func TestStormRateText(t *testing.T) {
	if stormRateText(100) != "100 %" {
		t.Fatalf("got %q", stormRateText(100))
	}
}

// THE REGRESSION THE DEVICE FOUND, and the one a fixture cannot find by accident.
//
// A plan may name ports in meshcraft's numbered family ("GE2") while the device
// always answers in its own ("Port2"). Before 2026-08-08 the verify step
// compared those two strings, so a legacy-family plan confirmed nothing: the lab
// CSS326 took the write, reported the limiter on Port2, and ApplyStormControl
// answered "the device did not take it" with an empty PortsWritten.
//
// Both directions are checked, because either family may appear on either side.
func TestVerifyStormWritesAcceptsEitherNamingFamily(t *testing.T) {
	var facade = NewFacade(26, 2, 24, "GE")
	// The device always answers in its own family.
	var after = driver.StormControlState{Ports: []driver.StormControlPortState{
		{Port: "Port2", Limits: []driver.StormLimit{{
			Class: driver.StormUnknownUnicast, Enabled: true,
			Rate: 10, Unit: driver.StormRatePercent,
		}}},
		{Port: "SFP1", Limits: []driver.StormLimit{{
			Class: driver.StormUnknownUnicast, Enabled: false,
		}}},
	}}

	var cases = []struct {
		name    string
		planned string
	}{
		{"the device's own family", "Port2"},
		{"meshcraft's numbered family", "GE2"},
	}
	var c = cases[0]
	for _, c = range cases {
		t.Run(c.name, func(t *testing.T) {
			var idx, ok = facade.Index(c.planned)
			if !ok {
				t.Fatalf("%s does not resolve on this faceplate", c.planned)
			}
			var confirmed, missed = verifyStormWrites(after, facade, []stormWrite{{
				Name: c.planned, Idx: idx,
				Want: driver.StormLimit{
					Class: driver.StormUnknownUnicast, Enabled: true,
					Rate: 10, Unit: driver.StormRatePercent,
				},
			}})
			if len(missed) > 0 {
				t.Errorf("the device shows Port2 policing at 10 %% and a plan naming it "+
					"%q was told otherwise: %v", c.planned, missed)
			}
			if len(confirmed) != 1 || confirmed[0] != c.planned {
				t.Errorf("confirmed = %v, want [%s] — the answer must quote the name the "+
					"caller used, not the one the device happens to print",
					confirmed, c.planned)
			}
		})
	}
}

// A write the device really did decline still has to be reported as declined —
// the index match must not turn into a blanket confirmation.
func TestVerifyStormWritesStillReportsARefusedWrite(t *testing.T) {
	var facade = NewFacade(26, 2, 24, "GE")
	var after = driver.StormControlState{Ports: []driver.StormControlPortState{
		{Port: "Port2", Limits: []driver.StormLimit{{
			Class: driver.StormUnknownUnicast, Enabled: false,
		}}},
	}}
	var confirmed, missed = verifyStormWrites(after, facade, []stormWrite{{
		Name: "GE2", Idx: 1,
		Want: driver.StormLimit{
			Class: driver.StormUnknownUnicast, Enabled: true,
			Rate: 10, Unit: driver.StormRatePercent,
		},
	}})
	if len(confirmed) != 0 {
		t.Errorf("confirmed = %v on a port that reads back unlimited", confirmed)
	}
	if len(missed) != 1 || !strings.Contains(missed[0], "GE2") {
		t.Errorf("missed = %v, want one line naming GE2", missed)
	}
}

// A rate that came back different is a disagreement, not a success: the bit alone
// does not say the limiter is the one that was asked for.
func TestVerifyStormWritesRejectsAWrongRate(t *testing.T) {
	var facade = NewFacade(26, 2, 24, "GE")
	var after = driver.StormControlState{Ports: []driver.StormControlPortState{
		{Port: "Port2", Limits: []driver.StormLimit{{
			Class: driver.StormUnknownUnicast, Enabled: true,
			Rate: 50, Unit: driver.StormRatePercent,
		}}},
	}}
	var confirmed, missed = verifyStormWrites(after, facade, []stormWrite{{
		Name: "Port2", Idx: 1,
		Want: driver.StormLimit{
			Class: driver.StormUnknownUnicast, Enabled: true,
			Rate: 10, Unit: driver.StormRatePercent,
		},
	}})
	if len(confirmed) != 0 || len(missed) != 1 {
		t.Errorf("confirmed=%v missed=%v — a port limited at 50 %% does not confirm a "+
			"plan that asked for 10 %%", confirmed, missed)
	}
}

// Disabling is confirmed by the bit alone. The rate is deliberately NOT compared
// on that branch: "srt" is a standing per-port value the enable bit does not own
// (cf. stormRateNoLimit), so a disabled port still carries whatever rate was last
// set, and demanding it read as no-limit would report every removal as a failure.
func TestVerifyStormWritesConfirmsRemovalWhateverTheRateReads(t *testing.T) {
	var facade = NewFacade(26, 2, 24, "GE")
	var after = driver.StormControlState{Ports: []driver.StormControlPortState{
		{Port: "Port2", Limits: []driver.StormLimit{{
			Class: driver.StormUnknownUnicast, Enabled: false, Rate: 10,
		}}},
	}}
	var confirmed, missed = verifyStormWrites(after, facade, []stormWrite{{
		Name: "Port2", Idx: 1,
		Want: driver.StormLimit{Class: driver.StormUnknownUnicast, Enabled: false},
	}})
	if len(confirmed) != 1 || len(missed) != 0 {
		t.Errorf("confirmed=%v missed=%v — the bit is clear, which is what removal means",
			confirmed, missed)
	}
}

// stormPage builds a /fwd.b page carrying just the two fields the reader looks
// at: the unknown-unicast mask and the per-port rate array.
//
// `mask` is the `suni` value and `rates` the `srt` array, both in the device's own
// hexadecimal spelling — written out rather than mutated from testdata/fwd.b so
// each case states the whole situation it is about.
func stormPage(t *testing.T, mask string, rates []string) *Page {
	t.Helper()
	var page, err = ParseObject("{" + FieldLimitUnknownUnicast + ":" + mask + "," +
		FieldStormRate + ":[" + strings.Join(rates, ",") + "]}")
	if err != nil {
		t.Fatalf("building the /fwd.b fixture: %v", err)
	}
	return page
}

// stormRates repeats one value across n ports, then overrides the given indices.
func stormRates(n int, base string, at map[int]string) []string {
	var out = make([]string, n)
	var i int
	for i = range out {
		out[i] = base
	}
	var idx int
	var v string
	for idx, v = range at {
		out[idx] = v
	}
	return out
}

// A port whose `suni` bit is SET beside an `srt` of 100 is armed and polices
// nothing, and the read has to say which ports those are.
//
// # Why this case exists on SwOS at all, given the factory mask is empty
//
// The reference CSS326 ships `suni:0x00000000`, so a pristine unit never reaches
// this state — unlike the RouterOS sibling, whose factory boolean is SET and whose
// note therefore fires on twenty-five ports out of the box. What makes it ordinary
// HERE is the revert: clearing `suni` does not reset `srt` (cf. stormRateNoLimit),
// so the way back from a limiter is to enable at 100 and then disable, and a page
// caught between those two steps reads exactly like this. An operator ticking the
// box in SwOS's own UI without touching the rate produces it too.
//
// THE OBSERVATION MUST NOT MOVE. Reporting Enabled false would hide a bit that is
// genuinely set, and a later rate change to 50 would then start policing a class
// the screen had called off.
func TestStormStateOfNamesThePortsArmedAtTheNoLimitRate(t *testing.T) {
	var facade = NewFacade(26, 2, 24, "GE")
	// Bits 0 and 1 set: Port1 at 100 (armed, idle), Port2 at 10 (really policing).
	var page = stormPage(t, "0x00000003", stormRates(26, "0x64", map[int]string{1: "0x0a"}))

	var st, limited = stormStateOf(page, []string{"Port1", "Port2", "Port3"}, facade)
	if limited != 2 {
		t.Errorf("limited = %d, want 2 — the count is about the BIT, and Port1's is set",
			limited)
	}

	var l, ok = st.Ports[0].Limit(driver.StormUnknownUnicast)
	if !ok || !l.Enabled || l.Rate != 100 || l.Unit != driver.StormRatePercent {
		t.Fatalf("the armed-at-100 port must still read exactly as observed: %+v", l)
	}

	var notes = strings.Join(st.Notes, " ")
	if !strings.Contains(notes, "ARMED BUT NOT POLICING") {
		t.Fatalf("a port armed at the no-limit rate is not called out: %v", st.Notes)
	}
	if !strings.Contains(notes, "Port1") {
		t.Errorf("the note must NAME the port: %v", st.Notes)
	}
	// Port2 caps at 10 % and Port3's bit is clear. Sweeping either into the note
	// would be as misleading as no note at all.
	if strings.Contains(notes, "Port2") || strings.Contains(notes, "Port3") {
		t.Errorf("a really-policing port or a switched-off one was swept into the "+
			"armed-but-idle note: %v", st.Notes)
	}
}

// The factory page — `suni` empty, every `srt` at 100 — must produce NO such note.
// The rate says nothing on its own; it is the bit beside it that makes 100 worth
// remarking on, and a note on twenty-six unlimited ports would train an operator
// to skip the one that matters.
func TestStormStateOfStaysSilentOnAFactoryPage(t *testing.T) {
	var facade = NewFacade(26, 2, 24, "GE")
	var page = stormPage(t, "0x00000000", stormRates(26, "0x64", nil))

	var st, limited = stormStateOf(page, []string{"Port1", "Port2"}, facade)
	if limited != 0 {
		t.Errorf("limited = %d on a page whose mask is empty", limited)
	}
	if strings.Contains(strings.Join(st.Notes, " "), "ARMED BUT NOT POLICING") {
		t.Fatalf("no bit is set, so no port is armed at all: %v", st.Notes)
	}
	// The two shape notes still ride on every read: they are facts about the
	// PLATFORM, not about this observation.
	if len(st.Notes) != 2 {
		t.Errorf("notes = %v, want exactly the two platform notes", st.Notes)
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
