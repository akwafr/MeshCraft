package gnmipoll

// The health translation and the JSON fallback, both tested against handcrafted
// notifications and no wire — the same posture as translate_test.go next door,
// and for the same reason: these are the places where a mistake produces a
// credible false graph rather than a visible failure.

import (
	"testing"
	"time"

	"meshcraft/internal/metricbuf"
)

// sampleNamed finds one sample by series name.
func sampleNamed(t *testing.T, out []metricbuf.Sample, name string) metricbuf.Sample {
	t.Helper()
	var s metricbuf.Sample
	for _, s = range out {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("no %s sample among %d", name, len(out))
	return metricbuf.Sample{}
}

// UPTIME IS DERIVED FROM boot-time, and this is the one conversion in the file.
//
// OpenConfig publishes the instant the box came up, in nanoseconds since the
// epoch; meshcraft_uptime_seconds is a duration whose useful signal is its BREAK
// — a value that drops is a reboot. Getting the unit wrong here (seconds for
// nanoseconds) yields an uptime of billions of years, which no graph flags as
// impossible.
func TestUptimeIsDerivedFromBootTime(t *testing.T) {
	var now = time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	var booted = now.Add(-3 * time.Hour)
	var n = Notification{
		Prefix: mustPath("/system/state"),
		TS:     now,
		Update: []Update{
			{Path: mustPath("/boot-time"), Value: NumberValue(float64(booted.UnixNano()))},
		},
	}
	var out = Samples("dev-1", n, now)
	var s = sampleNamed(t, out, metricbuf.MetricUptime)
	if s.Value != (3 * time.Hour).Seconds() {
		t.Errorf("uptime = %v s, want %v", s.Value, (3 * time.Hour).Seconds())
	}
	if s.Labels["device"] != "dev-1" {
		t.Errorf("uptime labels = %v, want device=dev-1", s.Labels)
	}
	if s.Labels["port"] != "" {
		t.Error("a device-level sample must carry no port label")
	}
}

// A BOOT TIME IN THE FUTURE PRODUCES NOTHING.
//
// It happens on a switch that has not reached NTP yet, and a negative uptime
// would land as a downward spike in the one series whose downward spikes MEAN
// "this box rebooted". The alert would fire on a clock skew.
func TestBootTimeInTheFutureProducesNoUptime(t *testing.T) {
	var now = time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	var n = Notification{
		Prefix: mustPath("/system/state"),
		TS:     now,
		Update: []Update{
			{Path: mustPath("/boot-time"),
				Value: NumberValue(float64(now.Add(time.Hour).UnixNano()))},
		},
	}
	var out = Samples("dev-1", n, now)
	var one metricbuf.Sample
	for _, one = range out {
		if one.Name == metricbuf.MetricUptime {
			t.Fatalf("a future boot time produced an uptime of %v", one.Value)
		}
	}
}

// A SENSOR READING IS LABELLED BY ITS COMPONENT, and an unlabelled one is
// refused.
//
// One series for all sensors is metricbuf's rule — a board carries one, two or
// ten thermal probes depending on the model, and a series name per probe makes a
// fleet-wide alert impossible to write. The corollary is that the label is not
// optional: a temperature with no `sensor` in a series where every other point
// has one is not a reading, it is a point nobody can attribute.
func TestComponentSamplesCarryTheSensorLabel(t *testing.T) {
	var now = time.Now().UTC()
	var n = Notification{
		Prefix: mustPath("/components/component[name=Fan1/1]/state"),
		TS:     now,
		Update: []Update{
			{Path: mustPath("/temperature/instant"), Value: NumberValue(41.5)},
		},
	}
	var s = sampleNamed(t, Samples("dev-1", n, now), metricbuf.MetricTemperature)
	if s.Labels["sensor"] != "Fan1/1" {
		t.Errorf("sensor label = %q, want Fan1/1", s.Labels["sensor"])
	}
	if s.Value != 41.5 {
		t.Errorf("temperature = %v, want 41.5", s.Value)
	}

	// The same leaf with no component key must yield nothing at all.
	var bare = Notification{
		Prefix: mustPath("/components/state"),
		TS:     now,
		Update: []Update{
			{Path: mustPath("/temperature/instant"), Value: NumberValue(41.5)},
		},
	}
	if len(Samples("dev-1", bare, now)) != 0 {
		t.Error("a component sample with no sensor key was published anyway")
	}
}

// A slash inside a component name survives, for the same reason it has to
// survive in a port name: Arista names fans and breakout ports that way, and a
// parser that split on "/" would attribute the reading to a component called
// "Fan1" that may also exist.
func TestComponentNameKeepsItsSlash(t *testing.T) {
	var now = time.Now().UTC()
	var n = Notification{
		Prefix: mustPath("/components/component[name=Fan1/1]/state"),
		TS:     now,
		Update: []Update{{Path: mustPath("/temperature/instant"), Value: NumberValue(1)}},
	}
	var s = sampleNamed(t, Samples("d", n, now), metricbuf.MetricTemperature)
	if s.Labels["sensor"] != "Fan1/1" {
		t.Errorf("sensor = %q, want the slashed name intact", s.Labels["sensor"])
	}
}

// A JSON CONTAINER IS DECOMPOSED INTO ITS LEAVES.
//
// THE FAILURE THIS PREVENTS IS TOTAL AND SILENT. This package subscribes
// containers; under JSON encoding one arrives as a single update whose path stops
// at the container and whose value is an object. Without expansion, `leafSuffix`
// yields "state/counters", which is in no table, and every counter is dropped
// behind a perfectly healthy subscription. transport.go asks for PROTO, but the
// encoding a target actually answers in is its firmware's decision, not ours.
func TestJSONContainerIsExpandedIntoLeaves(t *testing.T) {
	var now = time.Now().UTC()
	var n = Notification{
		Prefix: mustPath("/interfaces/interface[name=Ethernet49/1]/state"),
		TS:     now,
		Update: []Update{
			{Path: mustPath("/counters"), Value: JSONValue([]byte(
				`{"in-octets":"123456789","out-octets":42,"in-errors":7}`))},
		},
	}
	var out = Samples("dev-1", n, now)
	if len(out) != 3 {
		t.Fatalf("%d samples from a JSON counters container, want 3", len(out))
	}
	// The port key comes from the PREFIX and must survive expansion — the classic
	// way to attribute a counter to the wrong port.
	var s = sampleNamed(t, out, metricbuf.MetricIfInOctets)
	if s.Labels["port"] != "Ethernet49/1" {
		t.Errorf("port = %q, want Ethernet49/1", s.Labels["port"])
	}
	// RFC 7951 encodes 64-bit integers AS STRINGS. Read as text, every octet
	// counter on a JSON_IETF target would be dropped.
	if s.Value != 123456789 {
		t.Errorf("in-octets = %v, want 123456789 parsed out of its quotes", s.Value)
	}
	if sampleNamed(t, out, metricbuf.MetricIfOutOctets).Value != 42 {
		t.Error("a plain JSON number did not survive expansion")
	}
}

// The YANG module qualifier a JSON_IETF document prefixes its keys with is
// stripped. Kept, it would make every expanded leaf match nothing — the same
// empty graph, one layer further down.
func TestJSONKeysAreUnqualified(t *testing.T) {
	var now = time.Now().UTC()
	var n = Notification{
		Prefix: mustPath("/interfaces/interface[name=Et1]/state"),
		TS:     now,
		Update: []Update{
			{Path: mustPath("/counters"),
				Value: JSONValue([]byte(`{"openconfig-interfaces:in-errors":3}`))},
		},
	}
	var s = sampleNamed(t, Samples("d", n, now), metricbuf.MetricIfInErrors)
	if s.Value != 3 {
		t.Errorf("in-errors = %v, want 3", s.Value)
	}
}

// A JSON document that does not parse yields NOTHING, rather than a partial set.
// Half a counters container is not a smaller truth — it is a set of samples with
// no way to tell which ones are missing.
func TestUnparseableJSONYieldsNothing(t *testing.T) {
	var now = time.Now().UTC()
	var n = Notification{
		Prefix: mustPath("/interfaces/interface[name=Et1]/state"),
		TS:     now,
		Update: []Update{{Path: mustPath("/counters"), Value: JSONValue([]byte(`{"in-octets":`))}},
	}
	if len(Samples("d", n, now)) != 0 {
		t.Error("a truncated JSON container produced samples")
	}
}

// A null leaf produces no sample. A null is the device saying it HAS no value,
// and inventing a zero for it is the one thing this package never does — a zero
// counter reads as an idle link, which is a quiet lie.
func TestJSONNullProducesNoSample(t *testing.T) {
	var now = time.Now().UTC()
	var n = Notification{
		Prefix: mustPath("/interfaces/interface[name=Et1]/state"),
		TS:     now,
		Update: []Update{{Path: mustPath("/counters"),
			Value: JSONValue([]byte(`{"in-octets":null,"out-octets":5}`))}},
	}
	var out = Samples("d", n, now)
	if len(out) != 1 || out[0].Name != metricbuf.MetricIfOutOctets {
		t.Errorf("a null leaf was published: %v", out)
	}
}

// DECIMAL64 PRODUCES A SAMPLE. Regression guard for a bug a live switch found.
//
// EOS 4.28.13.1M publishes every environmental sensor as gNMI's decimal64 — an
// integer plus a decimal exponent — and `valueOf` had no case for it. The
// consequence was the shape this package is built to prevent: the subscription
// established, `/components/component[name=*]/state/temperature/instant` arrived
// on every tick, decoded to ValueNone, produced nothing, and
// meshcraft_temperature_celsius stayed empty for the whole fleet with no error
// anywhere.
//
// The transport-level decode is exercised in transport_test.go; this asserts the
// half that matters downstream — that a decimal-sourced value reaches the series
// with its scale intact. 52.418 °C read off the lab switch's Ethernet35
// transceiver is the value used, so the number in this test is a measurement and
// not an invention.
func TestADecimalSourcedTemperatureBecomesASample(t *testing.T) {
	var now = time.Now().UTC()
	var n = Notification{
		Prefix: mustPath("/components/component[name=Ethernet35 transceiver]/state"),
		TS:     now,
		Update: []Update{
			{Path: mustPath("/temperature/instant"), Value: NumberValue(52.41796875)},
		},
	}
	var s = sampleNamed(t, Samples("dev-1", n, now), metricbuf.MetricTemperature)
	if s.Value != 52.41796875 {
		t.Errorf("temperature = %v, want the decimal value intact", s.Value)
	}
	// A component name containing a SPACE survives as a label. Arista names
	// transceiver sensors this way, and a key split on whitespace would have
	// produced a sensor called "Ethernet35".
	if s.Labels["sensor"] != "Ethernet35 transceiver" {
		t.Errorf("sensor = %q, want the spaced name intact", s.Labels["sensor"])
	}
}

// FAN SPEED IS NOT PUBLISHED AS RPM, and must not come back.
//
// The lab 7050SX reports `fan/state/speed` = 34. A switch fan does not turn at
// 34 rpm — these run in the thousands — so the leaf carries a PERCENTAGE of
// maximum whatever the OpenConfig model calls it. Mapped to meshcraft_fan_rpm it
// would have put "34 RPM" beside a fan running perfectly normally, and nothing
// downstream could catch the unit because the series name asserts it.
func TestFanSpeedIsNotPublishedAsRPM(t *testing.T) {
	var now = time.Now().UTC()
	var n = Notification{
		Prefix: mustPath("/components/component[name=Fan1/1]/fan/state"),
		TS:     now,
		Update: []Update{{Path: mustPath("/speed"), Value: NumberValue(34)}},
	}
	var one metricbuf.Sample
	for _, one = range Samples("dev-1", n, now) {
		if one.Name == metricbuf.MetricFanRPM {
			t.Fatalf("fan speed %v was published as RPM: the lab switch reports a "+
				"PERCENTAGE there, and the series name asserts a unit it does not have",
				one.Value)
		}
	}
}
