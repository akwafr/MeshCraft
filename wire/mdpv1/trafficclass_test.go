package mdpv1

import "testing"

// The three classes each resolve to their OWN flag, and a platform holding two
// of them does not thereby hold the third.
//
// This is the test that would have caught the mistake ADR-0028 was written to
// prevent: one "can decompose" flag, or a shared one between two classes, makes
// the fleet's actual shape — multicast and broadcast yes, unknown unicast
// nowhere — inexpressible, and the screen then promises a band no platform can
// fill.
func TestCountsTrafficClassIsPerClass(t *testing.T) {
	var caps = Capabilities{Features: map[string]bool{
		FeatTrafficClassMulticast: true,
		FeatTrafficClassBroadcast: true,
	}}
	if !caps.CountsTrafficClass(StormMulticast) {
		t.Error("multicast declared but not reported")
	}
	if !caps.CountsTrafficClass(StormBroadcast) {
		t.Error("broadcast declared but not reported")
	}
	if caps.CountsTrafficClass(StormUnknownUnicast) {
		t.Error("unknown unicast was NOT declared and came back true — the one " +
			"answer that puts a band nobody can fill on the screen")
	}
}

// An explicit false and a missing key are the same answer, and a nil map does
// not panic. Absent means "has not declared it", which is the safe default
// everywhere in this contract.
func TestCountsTrafficClassAbsentMeansNo(t *testing.T) {
	var nilMap Capabilities
	var cls StormClass
	for _, cls = range StormClasses {
		if nilMap.CountsTrafficClass(cls) {
			t.Errorf("nil Features declared %s", cls)
		}
	}
	var explicit = Capabilities{Features: map[string]bool{
		FeatTrafficClassMulticast: false,
	}}
	if explicit.CountsTrafficClass(StormMulticast) {
		t.Error("an explicit false read as true")
	}
}

// An unknown class resolves to no flag at all, and must come back false rather
// than reading an empty-string key out of the map — a Features map that
// happened to carry "" would otherwise answer for every class at once.
func TestCountsTrafficClassUnknownClass(t *testing.T) {
	var caps = Capabilities{Features: map[string]bool{"": true}}
	if caps.CountsTrafficClass(StormClass("jumbo")) {
		t.Error("an unknown class resolved through the empty-string key")
	}
	if TrafficClassFeature(StormClass("jumbo")) != "" {
		t.Error("an unknown class produced a feature name")
	}
}

// Every class the storm limiter polices has a counting flag. The two vocabularies
// are deliberately the same (the graph is that function's observation side), so a
// class added to one must not silently lack the other.
func TestEveryStormClassHasACountingFeature(t *testing.T) {
	var cls StormClass
	for _, cls = range StormClasses {
		if TrafficClassFeature(cls) == "" {
			t.Errorf("storm class %q has no traffic-class capability flag", cls)
		}
	}
}
