package main

import (
	"strconv"
	"testing"
	"time"

	"meshcraft/internal/gnmipoll"
)

// The STREAMING half of the reporting-rate arithmetic.
//
// cmd/proxy/snmp_throughput_test.go computes what a polled device produces, and
// every constant in snmp.go was chosen against that number. This file exists
// because the buffer those constants size has TWO producers — cmd/proxy/main.go
// hands the SNMP collector's buffer straight to the gNMI one — and only one of
// them was ever in the model.
//
// # WHY THE MISSING HALF WAS NOT A DETAIL
//
// The saturation ceiling read "133 devices of 48 ports". It is derived from a
// poll cadence times a sample count per port, and a streaming device has no poll
// cadence at all: on-change subscriptions produce nothing on a quiet fleet and
// burst on a flapping one. That is genuinely outside the model, and the
// temptation is to conclude that streaming is therefore cheap.
//
// It is the opposite, and this file is here to say so with a number. The
// dominant subscription is NOT on-change: `if_counters` is subscribed in SAMPLE
// mode, at a cadence, on every port at once — the same shape as a poll and
// perfectly amenable to the same arithmetic. A 64-port Arista streaming eight
// counter series every five seconds produces more than twice what a 52-port
// polled switch does, so the ceiling for a fleet of them is less than HALF the
// documented one.
//
// Nothing here is invented: the eight series are the ones the connector's
// channel note records as CAPTURED on a DCS-7050SX-64-R, and the count comes out
// of gnmipoll's own translation rather than out of this comment.

// gnmiPortsDu7050SX: the faceplate of the reference streaming chassis, the
// DCS-7050SX-64-R the gNMI work was captured on. Sixty-four, against the
// fifty-two of the 1930 that sizes the polled half — the streaming device is the
// bigger box as well as the faster producer, and both differences push the same
// way.
const gnmiPortsDu7050SX = 64

// gnmiCountersIntervalS: the cadence `if_counters` actually resolves to on this
// platform, in seconds.
//
// FIVE. Repeated here rather than imported, exactly as the polled half repeats
// the controller's cadences: cmd/proxy is a binary deployed on its own and must
// owe nothing to the controller's build. Both ends of the resolution agree on
// this value anyway — internal/api/watch_handlers.go wants 5 s
// (`wantedCountersInterval`) and internal/driver/arista/eos declares a 5 s floor
// on its gnmi if_counters channel, so `gnmiIntervalFor` keeps the wanted value
// untouched.
//
// The floor means something different from a poll floor and it is worth holding
// on to while reading the number below: it bounds how often the DEVICE samples,
// not how often we ask. Raising it is the only lever that reduces this traffic —
// there is no burst size to trim.
const gnmiCountersIntervalS = 5

// gnmiCounterLeavesServed: the counter leaves the lab chassis actually sends.
//
// EIGHT, and the missing ninth is the point of listing them by hand. gnmipoll's
// table maps nine (`carrier-transitions` is the last), and the capture recorded
// that this EOS train does not serve it — which is why
// meshcraft_if_link_downs_total stays empty on Arista. So nine is what the code
// can decode and eight is what arrives, and the rate has to be computed on what
// arrives.
//
// If a future train starts serving the ninth, this list is where it is added and
// TestGNMIProductionPerDevice is where the ceiling below is re-derived.
var gnmiCounterLeavesServed = []string{
	"state/counters/in-octets",
	"state/counters/out-octets",
	"state/counters/in-pkts",
	"state/counters/out-pkts",
	"state/counters/in-errors",
	"state/counters/out-errors",
	"state/counters/in-discards",
	"state/counters/out-discards",
}

// gnmiCountersNotification builds what a device sends for ONE port: the port in
// the prefix, one update per leaf.
//
// That split is the real shape and not a convenience — internal/gnmipoll's
// header calls attributing a counter to the wrong port the classic failure of
// this protocol, precisely because the port lives in the prefix and the leaf in
// the update. Building it this way means the count below comes out of the same
// Join the wire path uses.
func gnmiCountersNotification(t *testing.T, port string, at time.Time) gnmipoll.Notification {
	t.Helper()
	var prefix, err = gnmipoll.ParsePath("/interfaces/interface[name=" + port + "]")
	if err != nil {
		t.Fatalf("prefix for %s: %v", port, err)
	}
	var ups []gnmipoll.Update
	var leaf string
	for _, leaf = range gnmiCounterLeavesServed {
		var p, perr = gnmipoll.ParsePath(leaf)
		if perr != nil {
			t.Fatalf("leaf %s: %v", leaf, perr)
		}
		// A twelve-digit counter, the same magnitude the polled half measures
		// with: a sample's weight depends on its number of DIGITS, and two files
		// quoting one figure must have measured the same device.
		ups = append(ups, gnmipoll.Update{Path: p, Value: gnmipoll.NumberValue(4982133770615)})
	}
	return gnmipoll.Notification{Prefix: prefix, TS: at, Update: ups}
}

// gnmiSamplesPerSecondPerDevice computes what one streaming chassis puts in the
// shared buffer, from gnmipoll's translation rather than from a comment.
func gnmiSamplesPerSecondPerDevice(t *testing.T) float64 {
	t.Helper()
	var at = refInstant()
	var total int
	var i int
	for i = 1; i <= gnmiPortsDu7050SX; i++ {
		var n = gnmiCountersNotification(t, "Ethernet"+strconv.Itoa(i), at)
		total += len(gnmipoll.Samples(tDeviceID, n, at))
	}
	return float64(total) / float64(gnmiCountersIntervalS)
}

// TestGNMIProductionPerDevice grounds the streaming arithmetic on the CODE, the
// way TestProductionPerDevice grounds the polled one: eight samples per port
// per interval, and the per-second figure that follows.
//
// It fails the day gnmipoll's counter table gains or loses a leaf, which is
// exactly when the ceiling documented in snmp.go stops being true.
func TestGNMIProductionPerDevice(t *testing.T) {
	var at = refInstant()
	var one = gnmipoll.Samples(tDeviceID, gnmiCountersNotification(t, "Ethernet1", at), at)
	if len(one) != len(gnmiCounterLeavesServed) {
		t.Fatalf("%d samples for one port, want %d: a leaf in gnmiCounterLeavesServed "+
			"no longer translates, so the rate below is computed on a set the code "+
			"does not produce", len(one), len(gnmiCounterLeavesServed))
	}
	if len(one) != 8 {
		t.Fatalf("%d counter series per port, 8 captured on the 7050SX: the ceiling in "+
			"cmd/proxy/snmp.go is derived from this number and must be redone", len(one))
	}

	var perSecond = gnmiSamplesPerSecondPerDevice(t)
	t.Logf("a 7050SX-64 streaming if_counters produces %.1f samples/s "+
		"(%d series x %d ports every %d s)",
		perSecond, len(one), gnmiPortsDu7050SX, gnmiCountersIntervalS)

	// The comparison that matters, and the reason this file exists: a streaming
	// device is NOT the cheap producer the absence of a poll cadence suggests.
	// 45 samples/s is the polled figure TestProductionPerDevice establishes.
	const perDeviceSNMP = 45
	if perSecond <= perDeviceSNMP {
		t.Errorf("%.1f samples/s streaming against %d polled: the whole point of the "+
			"note in snmp.go is that streaming costs MORE per device, and it no "+
			"longer does — redo the note before trusting the ceiling",
			perSecond, perDeviceSNMP)
	}
}

// TestGNMIOnChangeCarriesNoSteadyRate is the other half of the finding, and the
// half that cannot be turned into a number.
//
// `if_state` and `lldp` are subscribed ON CHANGE. They put NOTHING in the buffer
// on a stable fleet and burst on a moving one — a 64-port chassis coming back
// from a reload delivers its whole initial state at once. There is no cadence to
// divide by, so they are deliberately absent from the ceiling below, and what
// this test pins is the shape of the burst instead: it is bounded by the port
// count, not by the fleet size, and it is smaller than one counters interval's
// worth of samples. A ceiling computed on counters therefore already covers it.
func TestGNMIOnChangeCarriesNoSteadyRate(t *testing.T) {
	// Two leaves per port, oper-status and port-speed (the connector's if_state
	// channel note). The worst burst is every port at once.
	var burst = 2 * gnmiPortsDu7050SX
	var counters = len(gnmiCounterLeavesServed) * gnmiPortsDu7050SX
	t.Logf("worst on-change burst: %d samples at once (a whole chassis reloading), "+
		"against %d every %d s from the counters subscription",
		burst, counters, gnmiCountersIntervalS)
	if burst >= counters {
		t.Errorf("an on-change burst of %d samples now exceeds one counters interval "+
			"(%d): it can no longer be treated as covered by the steady-state "+
			"ceiling and needs its own line in the arithmetic", burst, counters)
	}
	// It must also fit in one flush, or a reloading chassis is reported as
	// saturation rather than as an event.
	if burst > maxPerFlush {
		t.Errorf("a chassis coming back produces %d samples and a lot carries %d: "+
			"the burst would be split across flushes", burst, maxPerFlush)
	}
}

// TestStreamingFleetCeiling is the correction this file was written for.
//
// The buffer drains at one rate whoever fills it, so the ceiling is a fleet
// COMPOSITION and not a device count. Stated in devices of one kind at a time —
// which is what an operator sizing a proxy actually has — the streaming figure
// is less than half the polled one, and the polled one is what snmp.go used to
// state alone.
func TestStreamingFleetCeiling(t *testing.T) {
	var sustainable = float64(sustainedSamplesPerSecond())
	var perDevice = gnmiSamplesPerSecondPerDevice(t)
	var deviceCount = int(sustainable / perDevice)

	// The polled figure, recomputed here rather than quoted, so the two ceilings
	// in the log line come from the same divisor.
	const perDeviceSNMP = 45
	var deviceCountSNMP = int(sustainable) / perDeviceSNMP

	t.Logf("sustainable %.0f samples/s = %d streaming 7050SX-64 (%.1f samples/s each) "+
		"or %d polled 1930-48 (%d samples/s each); a mixed fleet is bounded by the SUM",
		sustainable, deviceCount, perDevice, deviceCountSNMP, perDeviceSNMP)

	if deviceCount >= deviceCountSNMP {
		t.Errorf("%d streaming devices against %d polled: this file exists because the "+
			"streaming ceiling is the LOWER of the two, and snmp.go documents the "+
			"higher one — if that has changed, change the note with it",
			deviceCount, deviceCountSNMP)
	}
	// The floor a site has to keep clearing. A proxy that cannot carry a rack of
	// Aristas is not a proxy for this fleet; the threshold is deliberately round
	// and well under the ~58 computed, exactly as the polled test's 100 is under
	// its 133.
	if deviceCount < 40 {
		t.Errorf("%d streaming devices only (%.0f samples/s sustainable, %.1f per "+
			"device): a site's worth of Aristas would not report", deviceCount,
			sustainable, perDevice)
	}
	// The number snmp.go's comment must carry. Not an assertion on the exact
	// value — that would break on any cadence change for no reason — but on the
	// comment being in the right ORDER OF MAGNITUDE, which is what a reader
	// sizing a proxy takes away from it.
	if deviceCount > 100 {
		t.Errorf("%d streaming devices: the note in snmp.go says the streaming ceiling "+
			"is roughly half the polled one, and it no longer is", deviceCount)
	}
}
