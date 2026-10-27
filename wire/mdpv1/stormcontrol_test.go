package mdpv1_test

import (
	"errors"
	"strings"
	"testing"

	"meshcraft/wire/mdpv1"
)

// The storm-control contract's judgements, asserted as the FAILURES THEY
// PREVENT rather than as coverage. Every case below is a configuration that
// would have reached a device and done something other than what was asked.

// A rate with no unit is the whole reason this contract exists. "10" means ten
// percent of a gigabit on Arista and ten packets a second on a Cisco configured
// with the pps keyword — five orders of magnitude apart, both accepted, neither
// refused.
func TestStormLimitRefusesRateWithoutUnit(t *testing.T) {
	var l = mdpv1.StormLimit{Class: mdpv1.StormBroadcast, Enabled: true, Rate: 10}
	var err = l.Validate()
	if err == nil {
		t.Fatal("a rate with no unit was accepted: a bare number is exactly the ambiguity " +
			"this contract exists to remove")
	}
	if !strings.Contains(err.Error(), "unit is mandatory") {
		t.Errorf("the refusal must say the unit is mandatory, got %q", err)
	}
}

// A rate of ZERO on an ENABLED limit is not "no limit". On this fleet it means
// "drop every frame of this class", and on broadcast that takes ARP with it
// and disconnects whatever is behind the port. Removing a limiter is expressed
// by Enabled=false, and the two must never be spelled the same way.
func TestStormLimitRefusesZeroRateOnEnabledLimit(t *testing.T) {
	var l = mdpv1.StormLimit{
		Class: mdpv1.StormBroadcast, Enabled: true, Rate: 0,
		Unit: mdpv1.StormRatePercent,
	}
	var err = l.Validate()
	if err == nil {
		t.Fatal("a zero rate was accepted on an enabled limit: that configuration drops " +
			"every broadcast frame, ARP included")
	}
	if !strings.Contains(err.Error(), "send it disabled") {
		t.Errorf("the refusal must point at the disabled form, got %q", err)
	}
}

// A DISABLED limit carries no rate and no unit, and must not be refused for
// lacking them: "remove the broadcast limiter" is a complete instruction.
func TestStormLimitDisabledNeedsNoRate(t *testing.T) {
	var l = mdpv1.StormLimit{Class: mdpv1.StormUnknownUnicast, Enabled: false}
	if l.Validate() != nil {
		t.Fatalf("a removal was refused for having no rate: %v", l.Validate())
	}
	if !strings.Contains(l.Describe(), "removed") {
		t.Errorf("a disabled limit must read as a removal, got %q", l.Describe())
	}
}

// Fractional PACKET counts and fractional BIT counts are not thresholds any
// platform takes. Refused rather than rounded — rounding is the same silent
// reinterpretation the unit field exists to prevent, in miniature.
func TestStormLimitRefusesFractionalWholeUnits(t *testing.T) {
	var unit mdpv1.StormRateUnit
	for _, unit = range []mdpv1.StormRateUnit{mdpv1.StormRatePPS, mdpv1.StormRateBPS} {
		var l = mdpv1.StormLimit{
			Class: mdpv1.StormMulticast, Enabled: true, Rate: 1500.5, Unit: unit,
		}
		if l.Validate() == nil {
			t.Errorf("%s accepted a fractional count", unit)
		}
	}
	// PERCENT is the one unit where a fraction is legitimate: 0.5 % of a
	// ten-gigabit port is a real setting, and Cisco takes two decimals.
	var pct = mdpv1.StormLimit{
		Class: mdpv1.StormMulticast, Enabled: true, Rate: 0.5,
		Unit: mdpv1.StormRatePercent,
	}
	if pct.Validate() != nil {
		t.Errorf("a fractional percentage was refused: %v", pct.Validate())
	}
}

// Above 100 % is not a threshold. It is a typo that reads as "no limit" and
// would be written to the device as one.
func TestStormLimitRefusesPercentAbove100(t *testing.T) {
	var l = mdpv1.StormLimit{
		Class: mdpv1.StormBroadcast, Enabled: true, Rate: 150,
		Unit: mdpv1.StormRatePercent,
	}
	if l.Validate() == nil {
		t.Fatal("150 % of line rate was accepted as a threshold")
	}
}

// TWO LIMITS OF THE SAME CLASS on one port is not redundancy: which one lands
// would be decided by the order a connector happened to write them in. The plan
// is refused rather than half applied.
func TestStormPlanRefusesDuplicateClassOnOnePort(t *testing.T) {
	var plan = mdpv1.StormControlPlan{Ports: []mdpv1.StormControlPortIntent{{
		Port: "GE1",
		Limits: []mdpv1.StormLimit{
			{Class: mdpv1.StormBroadcast, Enabled: true, Rate: 5, Unit: mdpv1.StormRatePercent},
			{Class: mdpv1.StormBroadcast, Enabled: true, Rate: 50, Unit: mdpv1.StormRatePercent},
		},
	}}}
	var err = plan.Validate()
	if err == nil {
		t.Fatal("two contradictory broadcast limits on one port were accepted")
	}
	if !strings.Contains(err.Error(), "two broadcast limits") {
		t.Errorf("the refusal must name the doubled class, got %q", err)
	}
}

// A port named with an EMPTY limit list, and a port with an empty NAME, are both
// malformed rather than no-ops: a port meshcraft has nothing to say about is
// left out of the plan.
func TestStormPlanRefusesEmptyPortAndEmptyLimits(t *testing.T) {
	var noLimits = mdpv1.StormControlPlan{Ports: []mdpv1.StormControlPortIntent{
		{Port: "GE1"},
	}}
	if noLimits.Validate() == nil {
		t.Error("a port with no limit at all was accepted")
	}
	var noName = mdpv1.StormControlPlan{Ports: []mdpv1.StormControlPortIntent{{
		Port:   "  ",
		Limits: []mdpv1.StormLimit{{Class: mdpv1.StormBroadcast, Enabled: false}},
	}}}
	if noName.Validate() == nil {
		t.Error("a port with an empty name was accepted")
	}
	if (mdpv1.StormControlPlan{}).Validate() == nil {
		t.Error("a plan naming no port at all was accepted")
	}
}

// A class the contract does not carry — "unicast", the near-equivalent that
// several platforms offer — must not travel as if it were one of the three.
func TestStormClassValidity(t *testing.T) {
	if mdpv1.StormClass("unicast").Valid() {
		t.Error("\"unicast\" must not pass as a class: it is the BROADER keyword " +
			"several platforms offer in place of unknown_unicast")
	}
	var c mdpv1.StormClass
	for _, c = range mdpv1.StormClasses {
		if !c.Valid() {
			t.Errorf("%q is in StormClasses and does not validate", c)
		}
	}
}

// The ONE arithmetic this contract sanctions, and its refusal. 1500 bit/s is
// neither 1 nor 2 kbit/s, and a connector that picked either would be doing what
// the file header forbids.
func TestStormRateInKbpsRefusesInexactValues(t *testing.T) {
	var v, ok = mdpv1.StormRateInKbps(64000)
	if !ok || v != 64 {
		t.Errorf("64000 bit/s must convert exactly to 64 kbit/s, got %d ok=%v", v, ok)
	}
	if _, ok = mdpv1.StormRateInKbps(1500); ok {
		t.Error("1500 bit/s was rounded to a whole number of kilobits")
	}
	if _, ok = mdpv1.StormRateInKbps(0); ok {
		t.Error("a zero rate converted")
	}
}

// SortStormLimits pins the ORDER a connector writes in. Two identical gestures
// must produce two identical transcripts — on a fleet where nothing here has
// been captured yet, a transcript is how a command form gets learned, and two
// that differ only in ordering cannot be compared.
func TestSortStormLimitsIsCanonical(t *testing.T) {
	var limits = []mdpv1.StormLimit{
		{Class: mdpv1.StormUnknownUnicast},
		{Class: mdpv1.StormBroadcast},
		{Class: mdpv1.StormMulticast},
	}
	mdpv1.SortStormLimits(limits)
	var i int
	for i = range mdpv1.StormClasses {
		if limits[i].Class != mdpv1.StormClasses[i] {
			t.Fatalf("limit %d is %s, want %s", i, limits[i].Class, mdpv1.StormClasses[i])
		}
	}
}

// A class or unit refusal must be a mdpv1.Unsupported — i.e. it must answer
// errors.Is(err, ErrNotImplemented) — so a caller can tell "this platform
// cannot" from "the device refused the command", and must NAME the platform's
// own keyword rather than merely saying no.
func TestStormRefusalsAreCapabilityAnswers(t *testing.T) {
	var classErr = mdpv1.RefuseStormClass("cisco-ios", mdpv1.StormUnknownUnicast, "GE1",
		"this platform spells it \"storm-control unicast\", which polices ALL unicast")
	if !errors.Is(classErr, mdpv1.ErrNotImplemented) {
		t.Error("a class refusal is not a capability answer")
	}
	if !strings.Contains(classErr.Error(), "storm-control unicast") {
		t.Errorf("a class refusal must name the platform keyword, got %q", classErr)
	}

	var unitErr = mdpv1.RefuseStormUnit("hpe-procurve", mdpv1.StormBroadcast, "GE1",
		mdpv1.StormRatePPS, []mdpv1.StormRateUnit{mdpv1.StormRatePercent})
	if !errors.Is(unitErr, mdpv1.ErrNotImplemented) {
		t.Error("a unit refusal is not a capability answer")
	}
	// The operator must learn the number was NOT changed behind their back:
	// "it probably converted it" is the assumption that lets a silently
	// reinterpreted threshold survive a review.
	if !strings.Contains(unitErr.Error(), "Nothing was converted") {
		t.Errorf("a unit refusal must state that nothing was converted, got %q", unitErr)
	}
}

// The capability answer keeps "the platform cannot" apart from "meshcraft has
// not proven it yet", which is WritesExtra's job. A screen reading only one of
// the two greys out a control for the wrong reason.
func TestStormControlCapsQueries(t *testing.T) {
	var caps = mdpv1.StormControlCaps{
		Classes: []mdpv1.StormClass{mdpv1.StormBroadcast},
		Units:   []mdpv1.StormRateUnit{mdpv1.StormRatePercent},
	}
	if !caps.Expresses(mdpv1.StormBroadcast) {
		t.Error("a declared class does not answer Expresses")
	}
	if caps.Expresses(mdpv1.StormMulticast) {
		t.Error("an undeclared class answers Expresses")
	}
	if !caps.Accepts(mdpv1.StormRatePercent) || caps.Accepts(mdpv1.StormRatePPS) {
		t.Error("Accepts does not follow the declared unit list")
	}
}

// A foreign limiter is the third state the port report was missing, and the
// whole point is that it does NOT answer Limit — ADR-0020. A caller that read
// "not read" as "no limiter" would silently replace something an operator
// put on that port, which is the write this field exists to make visible.
func TestStormForeignIsNotAnAbsentLimiter(t *testing.T) {
	var st = mdpv1.StormControlPortState{
		Port: "XG3",
		Limits: []mdpv1.StormLimit{
			{Class: mdpv1.StormMulticast, Enabled: false},
		},
		Foreign: []mdpv1.StormForeignLimit{
			{
				Class:    mdpv1.StormBroadcast,
				Spelling: "storm-control broadcast level rate 10 mbps",
				Because:  "the unit vocabulary of the \"rate\" form was not established",
			},
			{
				Spelling: "storm-control all level 5",
				Because:  "\"all\" polices every frame, not one class",
			},
		},
	}
	var _, read = st.Limit(mdpv1.StormBroadcast)
	if read {
		t.Fatal("a class whose only observation is foreign answers Limit — a caller " +
			"would then treat an unreadable limiter as an absent one")
	}
	var f, ok = st.ForeignFor(mdpv1.StormBroadcast)
	if !ok || f.Spelling != "storm-control broadcast level rate 10 mbps" {
		t.Fatalf("ForeignFor lost the verbatim line, got %+v (%v)", f, ok)
	}
	// An entry with no class matches NO class. "all" answering yes for
	// broadcast would re-make, inside the helper, the conflation the empty
	// Class was chosen to avoid.
	var _, mok = st.ForeignFor(mdpv1.StormMulticast)
	if mok {
		t.Error("a classless foreign entry was matched to a class")
	}
	// A class positively established as carrying nothing still reads as such.
	var l, lok = st.Limit(mdpv1.StormMulticast)
	if !lok || l.Enabled {
		t.Error("the foreign list disturbed a class that WAS read")
	}
}
