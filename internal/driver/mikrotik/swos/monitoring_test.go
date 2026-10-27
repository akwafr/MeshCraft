package swos

import (
	"strings"
	"testing"
	"time"

	"meshcraft/internal/driver"
)

// Observability declared by the SwOS connector.
//
// This is not structure paraphrase: the floors declared here BOUND the
// controller's collection policy. A channel that groups two metrics imposes
// the same floor on both — that is how the Instant On connector's interface
// counters ended up throttled by health and LLDP. The split is therefore a
// property to uphold, not a drafting detail.
//
// And on this platform there is one more reason to uphold it: SwOS pushes
// NOTHING. Where a 1930's trap channel allows slowing state polling down
// without losing reactivity, here polling IS the reactivity — the chosen
// cadence is exactly the detection delay of a link that drops.

func channelOf(t *testing.T, mon driver.MonitoringCapabilities,
	metric string) driver.MonitorChannel {
	t.Helper()
	var ch, ok = mon.Best(metric)
	if !ok {
		t.Fatalf("no channel declared for %q: the metric would go unmonitored", metric)
	}
	return ch
}

// TestWebFloorsEachOnItsOwnChannel: the four channels, and their floors.
//
// The values are not copied from another connector: each is justified by the
// COST of the corresponding page, measured on the reference device. That is
// why they are verified one by one — changing them must be a deliberate
// gesture, not a side effect of a "tidy-up" regrouping.
func TestWebFloorsEachOnItsOwnChannel(t *testing.T) {
	var mon = (&Driver{}).Capabilities("CSS326-24G-2S+").Monitoring

	// THE TEST NOW QUERIES BY CHANNEL KIND, and it is not a detail of form:
	// since the SNMP agent was observed, `if_state`, `if_counters` and
	// `health` are served by TWO channels with different floors. `Best`
	// returns the faster of the two — so querying by metric alone would say
	// nothing any more about the cost of the web page, which is what these
	// values justify.
	var expected = []struct {
		kind     string
		metric   string
		interval time.Duration
	}{
		// --- The WEB channel: every floor is the cost of a page, measured. ---
		//
		// Port state is the FASTEST of the web channels, and deliberately so:
		// without traps, polling is the only source for detecting a link that
		// drops.
		{"web", "if_state", 10 * time.Second},
		// Counters are SLOWER than state, the opposite of the Instant On
		// connector: /stats.b is six times bigger than /link.b and is
		// serialised whole, with no way to ask for less.
		{"web", "if_counters", 15 * time.Second},
		{"web", "health", 30 * time.Second},
		// Optical power IS served by SwOS, unlike Instant On — and by the WEB
		// channel ONLY (see below).
		{"web", "optical_power", 30 * time.Second},

		// --- The SNMP channel: a single floor, because a GETBULK does not cost a
		// page. That is the whole point of this channel, and the reason the
		// cost-based split that holds for the web does not have to be reproduced
		// here.
		{"snmp", "if_state", 10 * time.Second},
		{"snmp", "if_counters", 10 * time.Second},
		{"snmp", "health", 10 * time.Second},
	}
	var a = expected[0]
	for _, a = range expected {
		var ch, ok = mon.BestOfKind(a.kind, a.metric)
		if !ok {
			t.Errorf("no %q channel serves %q: the metric would go unmonitored by "+
				"this kind of channel", a.kind, a.metric)
			continue
		}
		if ch.MinInterval != a.interval {
			t.Errorf("%s/%s: floor %v, expected %v — this value is justified by the "+
				"cost of the read, changing it requires measuring it again",
				a.kind, a.metric, ch.MinInterval, a.interval)
		}
	}

	// OPTICAL POWERS DO NOT GO THROUGH SNMP, and this test locks it because it
	// is the kind of thing someone adds "for symmetry" on a clean-up day.
	//
	// OBSERVED: this agent's optical table stops at column 8 (`txBias`).
	// Neither `txPower` (column 9) nor `rxPower` (column 10) — complete walk of
	// 1.3.6.1.4.1.14988.1.1.19 on 192.0.2.36, ten values, columns 2, 5, 6, 7 and
	// 8. The powers are in /sfp.b (`tpw`, `rpw`) and nowhere else. Declaring
	// `optical_power` on the SNMP channel would promise a curve no collection
	// would fill.
	var _, opticalOverSNMP = mon.BestOfKind("snmp", "optical_power")
	if opticalOverSNMP {
		t.Error("optical_power declared on the SNMP channel: this platform's agent " +
			"serves the optical table WITHOUT its power columns (observed on hardware). " +
			"The curve would stay empty.")
	}

	// The COST-BASED SPLIT must remain real ON THE WEB CHANNEL: the counters,
	// which are the most expensive page, must never share a channel there with
	// a light metric. Grouping them would impose one's floor on the other.
	//
	// The rule is restricted to the web, and that is the direct consequence of
	// what grounds it: it is about the cost of a PAGE. In SNMP there is no page —
	// six columns are asked for in one GETBULK — and grouping is the right
	// choice there.
	var light = map[string]bool{"health": true, "optical_power": true, "if_state": true}
	var c driver.MonitorChannel
	for _, c = range mon.Channels {
		if c.Kind != "web" {
			continue
		}
		var carriesCounters, carriesLight bool
		var metric string
		for _, metric = range c.Metrics {
			if metric == "if_counters" {
				carriesCounters = true
			}
			if light[metric] {
				carriesLight = true
			}
		}
		if carriesCounters && carriesLight {
			t.Errorf("web channel %+v: the counters must not share their floor "+
				"with a light metric — /stats.b costs six times more than the other pages", c)
		}
	}
}

// TestSwOSDoesNotPushAndSaysSo: the absence of traps is what JUSTIFIES the
// cadence.
//
// The `Traps: false` declaration is not cosmetic: it is what explains why
// `if_state` is the fastest channel here, whereas it is the slowest on
// Instant On. Flipping it to true without a trap existing would slow polling
// down while believing reactivity was gained — and detection of a cut link
// would simply disappear.
func TestSwOSDoesNotPushAndSaysSo(t *testing.T) {
	var mon = (&Driver{}).Capabilities("CSS326-24G-2S+").Monitoring
	if mon.Traps {
		t.Error("SwOS has NO trap receiver: /snmp.b has only four fields, " +
			"enable, community, contact, location")
	}
	if mon.Syslog {
		t.Error("SwOS has no syslog server, and does not even have a clock to " +
			"timestamp a message")
	}
	if mon.Streaming {
		t.Error("no streaming telemetry on this platform")
	}

	// The state channel must CARRY this consequence in its note: that is
	// where the operator will read it.
	var ch = channelOf(t, mon, "if_state")
	if !strings.Contains(strings.ToLower(ch.Note), "trap") {
		t.Errorf("the if_state channel's note must say nothing is pushed, hence that "+
			"this polling is the only detection of a cut link; got: %q", ch.Note)
	}
}

// TestLLDPDeclaredUnavailable: SwOS does not speak LLDP, and the connector
// SAYS so.
//
// What this test protects is not the structure, it is the DIFFERENCE between
// "we do not collect it yet" and "the device does not expose it". The first
// means waiting for a version, the second means changing hardware. Erasing
// this declaration would bring neighbour discovery back to the rank of a mere
// meshcraft gap, and would keep an operator waiting in front of a map that
// will never come.
func TestLLDPDeclaredUnavailable(t *testing.T) {
	// The absence is a PLATFORM fact, not a model one: it holds for the
	// surveyed CSS326 as for the other references, including when the model is
	// unknown — a case in which the project rule already requires assuming the
	// worst.
	var model string
	for _, model = range []string{"CSS326-24G-2S+", "CSS610-8P-2S+", "CRS328-24P-4S+", ""} {
		var mon = (&Driver{}).Capabilities(model).Monitoring

		var u, ok = mon.WhyUnavailable("lldp")
		if !ok {
			t.Fatalf("model %q: LLDP must be declared unavailable, otherwise its absence "+
				"is only a silence", model)
		}
		if !u.Observed {
			t.Errorf("model %q: the absence is OBSERVED by enumerating the device's "+
				"entire UI, not assumed", model)
		}
		// Operator copy lives in the UI catalog; the connector only declares the metric
		// and carries verifiable survey evidence in Source.
		var expected string
		for _, expected = range []string{"CSS326-24G-2S+", "44,807", "backup.swb"} {
			if !strings.Contains(u.Source, expected) {
				t.Errorf("model %q: the survey source must cite %q, got: %q",
					model, expected, u.Source)
			}
		}
		// And the corresponding feature must be false: the two declarations
		// must not be able to diverge.
		if (&Driver{}).Capabilities(model).Has("LLDP") {
			t.Errorf("model %q: the LLDP feature is true although the metric is "+
				"declared unavailable", model)
		}
	}
}

// TestPoEUnavailableDependingOnTheModel: the absence depends on the HARDWARE,
// and it is declared where it exists — not elsewhere.
//
// On a model without PoE, the reason is a fact: there is no electronics to
// measure. On a model with PoE, the metric is neither served by a channel nor
// declared impossible, and that is the truth: SwOS exposes it, meshcraft does
// not read it yet. Declaring the absence for everyone would make an operator
// give up on a PoE CRS that does serve these values.
func TestPoEUnavailableDependingOnTheModel(t *testing.T) {
	var withoutPoE = (&Driver{}).Capabilities("CSS326-24G-2S+").Monitoring
	var u, ok = withoutPoE.WhyUnavailable("poe")
	if !ok {
		t.Fatal("a model without PoE must say why it has no PoE curve")
	}
	if !u.Observed || !strings.Contains(u.Source, "npoe") {
		t.Errorf("the absence of PoE is observed on /sys.b (`npoe:0x00`); source: %q", u.Source)
	}

	var withPoE = (&Driver{}).Capabilities("CRS328-24P-4S+").Monitoring
	var _, declared = withPoE.WhyUnavailable("poe")
	if declared {
		t.Error("a PoE model must NOT declare PoE unavailable: SwOS exposes it, " +
			"it is meshcraft that does not read it yet, and confusing the two would " +
			"change hardware for nothing")
	}
	// And the feature follows the model.
	if (&Driver{}).Capabilities("CSS326-24G-2S+").Has("PoE") {
		t.Error("the CSS326-24G-2S+ has no PoE")
	}
	if !(&Driver{}).Capabilities("CRS328-24P-4S+").Has("PoE") {
		t.Error("the CRS328-24P-4S+ is a PoE model")
	}
}

// TestNoContradictionDeclared: the contract's invariant.
//
// A declaration that contradicts itself is worse than no declaration: either
// the UI announces a curve no collection will ever fill, or it displays an
// absence reason next to a curve that exists. In both cases the operator
// loses the only thing these capabilities give them — the certainty that what
// is displayed matches the device.
//
// Verified on SEVERAL models, including the unknown model: the list of
// unavailable metrics varies with PoE, and that is exactly the kind of
// variation that introduces a contradiction without anyone noticing.
func TestNoContradictionDeclared(t *testing.T) {
	var model string
	for _, model = range []string{
		"CSS326-24G-2S+", "CSS610-8G-2S+", "CRS328-24P-4S+", "CSS610-8P-2S+", "",
	} {
		var mon = (&Driver{}).Capabilities(model).Monitoring
		var contradictions = mon.Contradictions()
		if len(contradictions) != 0 {
			t.Errorf("model %q: contradictory declaration for %v — a metric is both "+
				"returned by a channel and declared unavailable", model, contradictions)
		}
	}
}

// TestOpticalPowerServed: the difference with Instant On, and it must hold.
//
// Instant On does NOT serve optical power and declares it unavailable; SwOS
// serves it. Two connectors, two opposite truths on the same metric, and that
// is precisely what the mechanism is for. Declaring unavailability here for
// symmetry with the other connector would lose a piece of data the device
// makes available.
func TestOpticalPowerServed(t *testing.T) {
	var mon = (&Driver{}).Capabilities("CSS326-24G-2S+").Monitoring
	var _, ok = mon.Best("optical_power")
	if !ok {
		t.Fatal("SwOS serves the optical diagnostics of its cages (/sfp.b): temperature, " +
			"voltage, bias current, transmitted and received powers")
	}
	var _, declared = mon.WhyUnavailable("optical_power")
	if declared {
		t.Error("optical power is SERVED by this platform: declaring it " +
			"unavailable would deprive users of data the device returns")
	}
}

// TestOpticalPowerReadableOnTheCapture: the declaration above rests on a
// survey, and the survey is in the package.
func TestOpticalPowerReadableOnTheCapture(t *testing.T) {
	var page, err = ParseObject(capture(t, "sfp.b"))
	if err != nil {
		t.Fatalf("/sfp.b: %v", err)
	}
	// Two cages: the first empty, the second holding an SFP-10G-BX.
	var tmp, ok = page.Nums(FieldSFPTemp)
	if !ok || len(tmp) != 2 {
		t.Fatalf("%s = %v (ok=%v), expected 2 values", FieldSFPTemp, tmp, ok)
	}
	// The EMPTY cage is recognised by its temperature: -128 °C, the value the
	// device's UI uses for "no module". Reading it as a temperature would show
	// a cage at -128 degrees.
	if tmp[0] != sfpNoModule {
		t.Errorf("cage 1: temperature %#x, expected %#x (empty cage)", tmp[0], sfpNoModule)
	}
	// The OCCUPIED cage carries a live temperature — and it is indeed a live
	// value: it was observed at 0x32 (50 °C) then at 0x35 (53 °C) a few minutes
	// apart on the same device. We therefore verify the PROPERTY (a plausible
	// temperature, and not the empty-cage marker) and not the exact survey:
	// freezing a telemetry figure would fail this test at the next refresh of
	// the captures, which would teach nobody anything.
	if tmp[1] == sfpNoModule {
		t.Error("cage 2: empty-cage marker although a transceiver is inserted")
	}
	if tmp[1] < 10 || tmp[1] > 90 {
		t.Errorf("cage 2: temperature %d °C outside any plausible range — is the field "+
			"really in whole degrees with no scale factor?", tmp[1])
	}
	// The occupied cage's powers are non-zero: that is what establishes the
	// platform actually SERVES them, and not merely that it has a field for
	// them.
	var rpw []uint32
	rpw, ok = page.Nums(FieldSFPRxPower)
	if !ok || rpw[1] == 0 {
		t.Errorf("%s = %v: the occupied cage must carry a non-zero received power",
			FieldSFPRxPower, rpw)
	}
	// And the transceiver identifies itself, SFF-8472 fields padded with
	// spaces.
	var pnr, textOK = page.TextAt(FieldSFPPart, 1)
	if !textOK || strings.TrimSpace(pnr) != "SFP-10G-BX" {
		t.Errorf("transceiver part = %q (ok=%v), expected \"SFP-10G-BX\" after "+
			"trimming the padding spaces", pnr, textOK)
	}
}

// TestCountersOnTheCapture: the 64-bit counters and the rate computed by the
// device.
//
// Two properties that decide whether a traffic curve is right.
func TestCountersOnTheCapture(t *testing.T) {
	var page, err = ParseObject(capture(t, "stats.b"))
	if err != nil {
		t.Fatalf("/stats.b: %v", err)
	}
	var low, ok = page.Nums(FieldRxBytes)
	if !ok || len(low) != 26 {
		t.Fatalf("%s = %d value(s), expected 26", FieldRxBytes, len(low))
	}
	var high []uint32
	high, ok = page.Nums(FieldRxBytesHigh)
	if !ok || len(high) != 26 {
		t.Fatalf("%s = %d value(s), expected 26", FieldRxBytesHigh, len(high))
	}
	// The capture carries traffic on the only active cage. The 64-bit counter
	// is recombined from both halves: reading `rb` alone would make the curve
	// drop to zero every 4 GB, i.e. every three and a half seconds on a 10G
	// link.
	var octets = Counter64(low[25], high[25])
	if octets == 0 {
		t.Errorf("the active cage must carry traffic; rb=%#x rbh=%#x", low[25], high[25])
	}
	if octets != uint64(low[25]) {
		t.Errorf("in this capture the high 32 bits are zero, so Counter64 must "+
			"return exactly rb; got %d", octets)
	}
	// Recombination with a non-zero high word: that is where the order
	// matters.
	var got = Counter64(0x00000002, 0x00000001)
	if got != 0x100000002 {
		t.Errorf("Counter64(2, 1) = %#x, expected 0x100000002", got)
	}

	// The rate is SERVED by the device: that is what allows a broad collection
	// floor without smoothing the curve.
	if !page.Has(FieldRxRate) || !page.Has(FieldTxRate) {
		t.Errorf("/stats.b must carry the computed rates (%s, %s): they are the "+
			"justification for the 15 s floor", FieldRxRate, FieldTxRate)
	}
}
