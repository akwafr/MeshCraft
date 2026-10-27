package gnmipoll

import (
	"testing"
	"time"

	"meshcraft/internal/metricbuf"
)

func mustParse(t *testing.T, s string) Path {
	t.Helper()
	var p, err = ParsePath(s)
	if err != nil {
		t.Fatalf("ParsePath(%q): %v", s, err)
	}
	return p
}

// byName indexes samples for assertions. Order out of Samples follows the
// notification's updates and nothing depends on it, so tests must not either.
func byName(list []metricbuf.Sample) map[string]metricbuf.Sample {
	var out = map[string]metricbuf.Sample{}
	var s metricbuf.Sample
	for _, s = range list {
		out[s.Name] = s
	}
	return out
}

// The shape a real device sends: ONE notification, the port in the prefix, the
// leaves as relative updates. Getting the join wrong here is the classic way to
// attribute a counter to the wrong port — or to no port at all.
func TestSamplesFromACounterNotification(t *testing.T) {
	// PLAUSIBLE as a collection time — a device timestamp far from arrival is no
	// longer believed, cf. usableDeviceTS and the test below it.
	var arrived = time.Now()
	var when = arrived.Add(-40 * time.Millisecond)
	var n = Notification{
		Prefix: mustParse(t, "/interfaces/interface[name=Ethernet49]"),
		TS:     when,
		Update: []Update{
			{Path: mustParse(t, "state/counters/in-octets"), Value: NumberValue(1200)},
			{Path: mustParse(t, "state/counters/out-octets"), Value: NumberValue(3400)},
			{Path: mustParse(t, "state/counters/in-pkts"), Value: NumberValue(12)},
			{Path: mustParse(t, "state/counters/out-pkts"), Value: NumberValue(34)},
			{Path: mustParse(t, "state/counters/in-errors"), Value: NumberValue(1)},
			{Path: mustParse(t, "state/counters/out-errors"), Value: NumberValue(2)},
			{Path: mustParse(t, "state/counters/in-discards"), Value: NumberValue(3)},
			{Path: mustParse(t, "state/counters/out-discards"), Value: NumberValue(4)},
			{Path: mustParse(t, "state/counters/carrier-transitions"), Value: NumberValue(7)},
		},
	}
	var got = Samples("dev-1", n, arrived)
	if len(got) != 9 {
		t.Fatalf("%d samples, want 9: %+v", len(got), got)
	}
	var idx = byName(got)
	var want = map[string]float64{
		metricbuf.MetricIfInOctets:    1200,
		metricbuf.MetricIfOutOctets:   3400,
		metricbuf.MetricIfInPackets:   12,
		metricbuf.MetricIfOutPackets:  34,
		metricbuf.MetricIfInErrors:    1,
		metricbuf.MetricIfOutErrors:   2,
		metricbuf.MetricIfInDiscards:  3,
		metricbuf.MetricIfOutDiscards: 4,
		// carrier-transitions is link flaps since boot, which is what
		// meshcraft_if_link_downs_total means.
		metricbuf.MetricIfLinkDowns: 7,
	}
	var name string
	var v float64
	for name, v = range want {
		var s, ok = idx[name]
		if !ok {
			t.Errorf("no %s sample", name)
			continue
		}
		if s.Value != v {
			t.Errorf("%s = %v, want %v", name, s.Value, v)
		}
		if s.Labels["device"] != "dev-1" || s.Labels["port"] != "Ethernet49" {
			t.Errorf("%s labels = %v, want device=dev-1 port=Ethernet49", name, s.Labels)
		}
		if !s.TS.Equal(when) {
			t.Errorf("%s timestamp = %v, want the DEVICE's own %v — a plausible device "+
				"timestamp is the better number and must be kept", name, s.TS, when)
		}
	}
}

// THE DEFECT THIS PINS, measured on core01.lab (EOS 4.28.13.1M) 2026-08-11.
//
// That train stamps a SAMPLE notification with the instant the leaf's value LAST
// CHANGED. Every quiet counter is re-published on cadence carrying a timestamp
// days old, and taken literally it is written into storage days in the past —
// where no query window reaches it. On the fleet core that hid ~94% of the
// samples the proxy was demonstrably POSTing, and left only the leaves whose
// value moved looking alive.
//
// The rule is symmetric: a clock running AHEAD writes into a future no query
// reaches either, which is the same defect mirrored.
func TestSamplesRefuseADeviceTimestampThatCannotBeACollectionTime(t *testing.T) {
	var arrived = time.Date(2026, 8, 11, 21, 12, 46, 0, time.UTC)
	var cases = []struct {
		what string
		ts   time.Time
		want time.Time
	}{
		{"frozen four days back, the shape observed on EOS",
			arrived.Add(-4 * 24 * time.Hour), arrived},
		{"clock running an hour ahead", arrived.Add(time.Hour), arrived},
		{"ordinary transit latency is kept",
			arrived.Add(-30 * time.Millisecond), arrived.Add(-30 * time.Millisecond)},
		{"a device that gives none falls back to arrival", time.Time{}, arrived},
	}
	var c = cases[0]
	for _, c = range cases {
		var n = Notification{
			Prefix: mustParse(t, "/interfaces/interface[name=Ethernet7]"),
			TS:     c.ts,
			Update: []Update{
				{Path: mustParse(t, "state/counters/in-errors"), Value: NumberValue(0)},
			},
		}
		var got = Samples("dev-1", n, arrived)
		if len(got) != 1 {
			t.Fatalf("%s: %d samples, want 1", c.what, len(got))
		}
		if !got[0].TS.Equal(c.want) {
			t.Errorf("%s: timestamp = %v, want %v", c.what, got[0].TS, c.want)
		}
	}
}

// oper-status is a STRING enum and lands in a series a SNMP producer already
// fills with the raw IF-MIB value. "UP -> 1, everything else -> 0" would look
// right on a graph and be wrong at every comparison: a port DOWN over SNMP sits
// at 2, and the same port DOWN over gNMI would sit at 0, in one series.
func TestSamplesMapOperStatusOntoIfMibNumbering(t *testing.T) {
	var cases = []struct {
		in   string
		want float64
	}{
		{"UP", 1},
		{"DOWN", 2},
		{"TESTING", 3},
		{"NOT_PRESENT", 6},
		{"LOWER_LAYER_DOWN", 7},
		// Module-qualified spelling: the same value, and which of the two arrives
		// depends on the target's encoding, not on anything meshcraft chose.
		{"openconfig-interfaces:UP", 1},
		{"up", 1},
	}
	var c = cases[0]
	for _, c = range cases {
		var n = Notification{
			Prefix: mustParse(t, "/interfaces/interface[name=Ethernet1]"),
			Update: []Update{
				{Path: mustParse(t, "state/oper-status"), Value: StringValue(c.in)},
			},
		}
		var got = Samples("dev-1", n, time.Now())
		if len(got) != 1 {
			t.Fatalf("oper-status %q gave %d samples", c.in, len(got))
		}
		if got[0].Name != metricbuf.MetricIfOperStatus {
			t.Errorf("oper-status landed in series %q", got[0].Name)
		}
		if got[0].Value != c.want {
			t.Errorf("oper-status %q = %v, want %v", c.in, got[0].Value, c.want)
		}
	}
}

// An enum spelling the table does not know produces NO sample. The alternative
// — a zero — reads as "the port is not up" for oper-status and as a dead link
// for speed, on a port that is in fact running fine.
func TestSamplesDropUnknownEnums(t *testing.T) {
	var n = Notification{
		Prefix: mustParse(t, "/interfaces/interface[name=Ethernet1]"),
		Update: []Update{
			{Path: mustParse(t, "state/oper-status"), Value: StringValue("HALF_UP")},
			{Path: mustParse(t, "ethernet/state/port-speed"), Value: StringValue("SPEED_3GB")},
		},
	}
	var got = Samples("dev-1", n, time.Now())
	if len(got) != 0 {
		t.Errorf("an unknown enum was published: %+v", got)
	}
}

// port-speed is an enum and the series is Mbit/s. BOTH leaf spellings are
// accepted because which one a train serves is unverified, and an empty speed
// series with nothing to explain it is the failure that costs the most to
// diagnose.
func TestSamplesMapPortSpeedToMbps(t *testing.T) {
	var cases = []struct {
		leaf string
		in   string
		want float64
	}{
		{"ethernet/state/port-speed", "SPEED_1GB", 1000},
		{"ethernet/state/port-speed", "openconfig-if-ethernet:SPEED_10GB", 10000},
		{"ethernet/state/negotiated-port-speed", "SPEED_100MB", 100},
		{"ethernet/state/port-speed", "SPEED_400GB", 400000},
	}
	var c = cases[0]
	for _, c = range cases {
		var n = Notification{
			Prefix: mustParse(t, "/interfaces/interface[name=Ethernet1]"),
			Update: []Update{
				{Path: mustParse(t, c.leaf), Value: StringValue(c.in)},
			},
		}
		var got = Samples("dev-1", n, time.Now())
		if len(got) != 1 {
			t.Fatalf("%s = %q gave %d samples", c.leaf, c.in, len(got))
		}
		if got[0].Name != metricbuf.MetricIfSpeedMbps || got[0].Value != c.want {
			t.Errorf("%s = %q gave %s=%v, want %s=%v", c.leaf, c.in,
				got[0].Name, got[0].Value, metricbuf.MetricIfSpeedMbps, c.want)
		}
	}
	// SPEED_UNKNOWN is the device saying it does not know. Publishing a number
	// would be us claiming to.
	var n = Notification{
		Prefix: mustParse(t, "/interfaces/interface[name=Ethernet1]"),
		Update: []Update{
			{Path: mustParse(t, "ethernet/state/port-speed"),
				Value: StringValue("SPEED_UNKNOWN")},
		},
	}
	if len(Samples("dev-1", n, time.Now())) != 0 {
		t.Error("SPEED_UNKNOWN was published as a speed")
	}
}

// A leaf this build does not know is IGNORED, not emitted as zero — the same
// rule snmppoll.toFloat states, restated because this table will grow leaves and
// the pressure to default them arrives with them.
func TestSamplesIgnoreUnknownLeavesAndWrongTypes(t *testing.T) {
	var n = Notification{
		Prefix: mustParse(t, "/interfaces/interface[name=Ethernet1]"),
		Update: []Update{
			// A leaf outside the table. `in-unicast-pkts` rather than any
			// invented name, and rather than the `in-broadcast-pkts` that
			// stood here until broadcast became a mapped class (ADR-0028):
			// EOS really serves this leaf, and it is DELIBERATELY unmapped —
			// it is unicast TOTAL, not the flooded share the stacked graph
			// would need. So this case now pins a decision as well as the
			// default branch.
			{Path: mustParse(t, "state/counters/in-unicast-pkts"), Value: NumberValue(9)},
			// A counter that arrived as a string.
			{Path: mustParse(t, "state/counters/in-octets"), Value: StringValue("12")},
			// An enum that arrived as a number.
			{Path: mustParse(t, "state/oper-status"), Value: NumberValue(1)},
			// No payload at all.
			{Path: mustParse(t, "state/counters/out-octets")},
			// And one good leaf, to prove the others were dropped and not the
			// whole notification.
			{Path: mustParse(t, "state/counters/out-errors"), Value: NumberValue(5)},
		},
	}
	var got = Samples("dev-1", n, time.Now())
	if len(got) != 1 {
		t.Fatalf("%d samples, want only the well-formed one: %+v", len(got), got)
	}
	if got[0].Name != metricbuf.MetricIfOutErrors || got[0].Value != 5 {
		t.Errorf("kept the wrong sample: %+v", got[0])
	}
}

// The four per-class counter leaves map to the four per-class series (ADR-0028).
//
// Pinned by NAME against the vocabulary, because the whole mechanism is a
// lookup table and the one failure it can have is silent: an unmapped leaf
// takes the default branch and is dropped, so a typo here would not break a
// build or raise an error — it would leave the stacked graph permanently
// showing "no measurement yet" on a platform that is in fact streaming the
// counters, which reads as a collection problem rather than a one-letter bug.
//
// The leaf spellings are the ones actually observed on core01-arena
// (EOS 4.28.13M) on 2026-10-25, not ones read off a model.
func TestSamplesMapTheTrafficClassCounters(t *testing.T) {
	var want = map[string]string{
		"state/counters/in-multicast-pkts":  metricbuf.MetricIfInMulticastPackets,
		"state/counters/out-multicast-pkts": metricbuf.MetricIfOutMulticastPackets,
		"state/counters/in-broadcast-pkts":  metricbuf.MetricIfInBroadcastPackets,
		"state/counters/out-broadcast-pkts": metricbuf.MetricIfOutBroadcastPackets,
	}
	var leaf, metric string
	for leaf, metric = range want {
		var n = Notification{
			Prefix: mustParse(t, "/interfaces/interface[name=Ethernet1]"),
			Update: []Update{{Path: mustParse(t, leaf), Value: NumberValue(42)}},
		}
		var got = Samples("dev-1", n, time.Now())
		if len(got) != 1 {
			t.Errorf("%s gave %d samples, want 1", leaf, len(got))
			continue
		}
		if got[0].Name != metric {
			t.Errorf("%s mapped to %s, want %s", leaf, got[0].Name, metric)
		}
		if got[0].Value != 42 || got[0].Labels["port"] != "Ethernet1" {
			t.Errorf("%s gave %+v", leaf, got[0])
		}
	}
}

// Unicast TOTAL is served by the device and must stay OUT of the table.
//
// A separate test from the unknown-leaf one above because this is a decision
// rather than a default: mapping `in-unicast-pkts` as a third band would put
// ordinary point-to-point traffic under the two flooding classes, and the
// class an operator actually wants there — unknown unicast — is not derivable
// from it (ADR-0028). The temptation is real and arrives with the leaf.
func TestSamplesDoNotMapUnicastTotalAsAClass(t *testing.T) {
	var leaf string
	for _, leaf = range []string{
		"state/counters/in-unicast-pkts",
		"state/counters/out-unicast-pkts",
	} {
		var n = Notification{
			Prefix: mustParse(t, "/interfaces/interface[name=Ethernet1]"),
			Update: []Update{{Path: mustParse(t, leaf), Value: NumberValue(7)}},
		}
		var got = Samples("dev-1", n, time.Now())
		if len(got) != 0 {
			t.Errorf("%s was published as %+v", leaf, got)
		}
	}
}

// An update whose joined path carries no port is DROPPED. Never attributed to
// the last port seen, and never emitted without the label — a sample with no
// `port` would land in the series as a second, portless curve for the device.
func TestSamplesDropUpdatesWithNoPort(t *testing.T) {
	var n = Notification{
		Prefix: mustParse(t, "/interfaces/interface"),
		Update: []Update{
			{Path: mustParse(t, "state/counters/in-octets"), Value: NumberValue(1)},
		},
	}
	if len(Samples("dev-1", n, time.Now())) != 0 {
		t.Error("a counter with no port key was published")
	}
	// Something that is not an interface at all.
	var other = Notification{
		Prefix: mustParse(t, "/system/state"),
		Update: []Update{
			{Path: mustParse(t, "hostname"), Value: StringValue("sw-1")},
		},
	}
	if len(Samples("dev-1", other, time.Now())) != 0 {
		t.Error("a non-interface notification produced a sample")
	}
}

// NO `ifindex`, NO `iftype`, and that is a decision rather than a gap: gNMI
// carries no interface index, and the controller refuses to draw an inventory
// conclusion from a sample without `iftype`. The rule "telemetry never creates a
// port" holds here by construction — this test is what keeps it that way if
// somebody later "enriches" the labels.
func TestSamplesCarryNoInventoryLabels(t *testing.T) {
	var n = Notification{
		Prefix: mustParse(t, "/interfaces/interface[name=Ethernet49]"),
		Update: []Update{
			{Path: mustParse(t, "state/counters/in-octets"), Value: NumberValue(1)},
			{Path: mustParse(t, "state/oper-status"), Value: StringValue("UP")},
		},
	}
	var got = Samples("dev-1", n, time.Now())
	if len(got) != 2 {
		t.Fatalf("%d samples, want 2", len(got))
	}
	var s metricbuf.Sample
	for _, s = range got {
		var _, hasIdx = s.Labels["ifindex"]
		var _, hasType = s.Labels["iftype"]
		if hasIdx || hasType {
			t.Errorf("%s carries an inventory label: %v — gNMI gives neither, so filling "+
				"one would be inventing identity the protocol did not carry", s.Name, s.Labels)
		}
		if len(s.Labels) != 2 {
			t.Errorf("%s carries %d labels, want exactly device and port: %v",
				s.Name, len(s.Labels), s.Labels)
		}
	}
}

// A device that gave no timestamp is stamped with arrival time, not with 1970.
func TestSamplesFallBackToArrivalTime(t *testing.T) {
	var at = time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	var n = Notification{
		Prefix: mustParse(t, "/interfaces/interface[name=Ethernet1]"),
		Update: []Update{
			{Path: mustParse(t, "state/counters/in-octets"), Value: NumberValue(1)},
		},
	}
	var got = Samples("dev-1", n, at)
	if len(got) != 1 || !got[0].TS.Equal(at) {
		t.Fatalf("timestamp fallback not applied: %+v", got)
	}
}

// An ABSOLUTE update with an empty prefix is the other shape a device may send.
// It must work identically — the join is what makes the two indistinguishable
// downstream.
func TestSamplesAcceptAbsoluteUpdatesWithNoPrefix(t *testing.T) {
	var n = Notification{
		Update: []Update{
			{Path: mustParse(t,
				"/interfaces/interface[name=Ethernet2]/state/counters/in-octets"),
				Value: NumberValue(42)},
		},
	}
	var got = Samples("dev-1", n, time.Now())
	if len(got) != 1 {
		t.Fatalf("%d samples, want 1: %+v", len(got), got)
	}
	if got[0].Labels["port"] != "Ethernet2" || got[0].Value != 42 {
		t.Errorf("absolute update mistranslated: %+v", got[0])
	}
}

// --- Optics ---

// The shape the lab 7050SX actually sends (captured 2026-08-12): the component
// named `EthernetN transceiver`, one channel keyed by index, three numeric
// leaves already in dBm and mA. The port label must be the INTERFACE name —
// suffix stripped — and the lane must ride its own label, "0" included.
func TestSamplesFromATransceiverChannelNotification(t *testing.T) {
	var arrived = time.Now()
	var n = Notification{
		Prefix: mustParse(t,
			"/components/component[name=Ethernet34 transceiver]"+
				"/transceiver/physical-channels/channel[index=0]/state"),
		TS: arrived.Add(-30 * time.Millisecond),
		Update: []Update{
			{Path: mustParse(t, "index"), Value: NumberValue(0)},
			{Path: mustParse(t, "input-power/instant"), Value: NumberValue(-4.066)},
			{Path: mustParse(t, "output-power/instant"), Value: NumberValue(-2.185)},
			{Path: mustParse(t, "laser-bias-current/instant"), Value: NumberValue(42.578)},
		},
	}
	var got = Samples("dev-1", n, arrived)
	// Three series: `index` is the lane's identity, not a measurement, and must
	// not become a curve.
	if len(got) != 3 {
		t.Fatalf("%d samples, want 3: %+v", len(got), got)
	}
	var idx = byName(got)
	var want = map[string]float64{
		metricbuf.MetricSFPRxPowerDBM:      -4.066,
		metricbuf.MetricSFPTxPowerDBM:      -2.185,
		metricbuf.MetricSFPTxBiasMilliamps: 42.578,
	}
	var name string
	var v float64
	for name, v = range want {
		var s, ok = idx[name]
		if !ok {
			t.Errorf("series %s missing", name)
			continue
		}
		if s.Value != v {
			t.Errorf("%s = %v, want %v", name, s.Value, v)
		}
		if s.Labels["port"] != "Ethernet34" {
			t.Errorf("%s port = %q, want Ethernet34 (component suffix stripped)",
				name, s.Labels["port"])
		}
		if s.Labels["lane"] != "0" {
			t.Errorf("%s lane = %q, want \"0\" — the label is not optional on "+
				"single-lane modules", name, s.Labels["lane"])
		}
	}
}

// EOS publishes input-power at exactly -30 when the cage receives no light.
// That is the device's own claim and it must land in storage verbatim: a fibre
// dropping from -4 to -30 is the event the level series exists to show, and
// filtering the floor here would erase it.
func TestSamplesKeepTheNoLightFloorVerbatim(t *testing.T) {
	var n = Notification{
		Update: []Update{
			{Path: mustParse(t,
				"/components/component[name=Ethernet35 transceiver]"+
					"/transceiver/physical-channels/channel[index=0]"+
					"/state/input-power/instant"),
				Value: NumberValue(-30)},
		},
	}
	var got = Samples("dev-1", n, time.Now())
	if len(got) != 1 || got[0].Value != -30 {
		t.Fatalf("no-light floor mistranslated: %+v", got)
	}
}

// A multi-lane module (QSFP) publishes one channel per lane; each must keep its
// own lane label, or four fibres' histories collapse onto one series.
func TestSamplesKeepLanesApart(t *testing.T) {
	var n = Notification{
		Update: []Update{
			{Path: mustParse(t,
				"/components/component[name=Ethernet49 transceiver]"+
					"/transceiver/physical-channels/channel[index=1]"+
					"/state/input-power/instant"),
				Value: NumberValue(-3.1)},
			{Path: mustParse(t,
				"/components/component[name=Ethernet49 transceiver]"+
					"/transceiver/physical-channels/channel[index=3]"+
					"/state/input-power/instant"),
				Value: NumberValue(-7.9)},
		},
	}
	var got = Samples("dev-1", n, time.Now())
	if len(got) != 2 {
		t.Fatalf("%d samples, want 2: %+v", len(got), got)
	}
	var lanes = map[string]float64{}
	var s = got[0]
	for _, s = range got {
		lanes[s.Labels["lane"]] = s.Value
	}
	if lanes["1"] != -3.1 || lanes["3"] != -7.9 {
		t.Errorf("lanes folded: %+v", lanes)
	}
}

// A channel leaf whose path lost its keys is refused, not attributed. Same rule
// as the unlabelled temperature: a reading nobody can pin on a cage is not a
// reading.
func TestSamplesRefuseAChannelWithNoIdentity(t *testing.T) {
	var n = Notification{
		Update: []Update{
			// No [index=…] key on the channel element.
			{Path: mustParse(t,
				"/components/component[name=Ethernet34 transceiver]"+
					"/transceiver/physical-channels/channel"+
					"/state/input-power/instant"),
				Value: NumberValue(-4)},
		},
	}
	var got = Samples("dev-1", n, time.Now())
	if len(got) != 0 {
		t.Fatalf("unattributable channel reading published: %+v", got)
	}
}
