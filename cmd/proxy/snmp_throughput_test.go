package main

import (
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"

	"meshcraft/internal/metricbuf"
	"meshcraft/internal/snmppoll"
)

// The upstream rate setting, in black and white.
//
// This file does not exist to prove the code works: it exists so that the day
// someone touches `flushEvery`, `maxPerFlush`, `maxFlushBurst` or `bufCapacity`,
// they see WHAT THEY BREAK. Every constant in this batch was chosen against a
// figure, and an unchecked figure turns back into an opinion within six months.

// The counter OIDs, in addition to those trap_test.go already declares for
// `if_state` (`tOidIfDescr`, `tOidIfType`, `tOidIfOperStatus`) — reused as is,
// the contract with `snmppoll` being the same. Copied because `snmppoll` keeps
// them private, and that is risk-free: they are PROTOCOL constants.
const (
	cOidIfHighSpeed   = "1.3.6.1.2.1.31.1.1.1.15"
	cOidIfHCInOctets  = "1.3.6.1.2.1.31.1.1.1.6"
	cOidIfHCOutOctets = "1.3.6.1.2.1.31.1.1.1.10"
	cOidIfInErrors    = "1.3.6.1.2.1.2.2.1.14"
	cOidIfOutErrors   = "1.3.6.1.2.1.2.2.1.20"
)

// portsDu1930_48: 48 copper ports + 4 SFP slots. This is the front panel the
// whole rate calculation uses as reference, because it is the largest device
// of the product line meshcraft drives.
const portsDu1930_48 = 52

// What the controller accepts, in BYTES and in COUNT. Both matter, and that is
// the whole point of the update: `maxPerFlush` has two walls, not one.
//
// The values are those of `maxProxyMetricsBody` and `maxSamplesPerBatch`
// (internal/api/proxymetrics_handlers.go). Copied and not imported: `cmd/proxy`
// is a binary deployed separately, it MUST owe nothing to the version of the
// controller listening to it — and that is precisely what we want to test, the
// agreement between two settings living in two processes.
//
// The previous figure was 1 MiB, that of the API's generic decoder
// (internal/api.decode) this route used back then. The route now has its own
// ceiling, loosened to 8 MiB so that its advertised guard of 20,000 samples
// finally becomes reachable. The assertions written against 1 MiB still passed —
// they had become conservative, not wrong — but a figure that is right by
// accident turns back into an opinion at the first re-read.
//
// Exceeding it no longer STALLS, and that is not a detail either: the controller
// answers an explicit 413, and `flush` drops the batch and logs it (see
// isPermanentRefusal) instead of replaying it endlessly. What this test protects is
// therefore no longer a stall but WASTE — a setting that would make dropping
// routine.
const (
	controllerBodyCapBytes = 8 << 20
	controllerMaxSamples   = 20000
)

// refInstant: a timestamp with its NANOSECONDS, and that matters. A round
// instant encodes as "2026-07-28T12:00:00Z", ten bytes less per sample than a
// real instant. Measuring a batch on a round instant would underestimate it by
// 5 % — on an assertion whose whole point is a margin, that is the mistake not
// to make.
func refInstant() time.Time {
	return time.Date(2026, 7, 28, 12, 0, 0, 123456789, time.UTC)
}

// tDeviceID / tProxyID / tSecret: UUIDs and a secret of the real length. A
// device identifier is paid for on EVERY sample, measuring a test "sw1" would
// underestimate the batch.
const (
	tDeviceID = "6f1c9a2e-4b7d-4c3a-9f88-2d0e5b71a3c4"
	tProxyID  = "b2f4d1c8-9e3a-4f52-8c11-7a6d0b4e2f39"
	tSecret   = "0123456789abcdef0123456789abcdef"
)

// tPortFactory: the factory naming of an Instant On 1930, straight out of the box.
func tPortFactory(i int) string { return "GE" + strconv.Itoa(i) }

// tPortRenamed: a port renamed by the operator, 37 characters. THIS is the
// sizing case, and not out of pessimism: the controller does not bound the
// length of this label, the operator chooses it. It is the same shape as the one
// measured on the controller side (worstBytesPerSample), so that both packages
// talk about the same device when they announce a weight per sample.
func tPortRenamed(i int) string {
	return "Server room 01 - rack B12 - uplink " + strconv.Itoa(i)
}

func tPDU(oid, idx string, typ gosnmp.Asn1BER, v any) gosnmp.SnmpPDU {
	return gosnmp.SnmpPDU{Name: oid + "." + idx, Type: typ, Value: v}
}

// counterTables builds the tables an `if_counters` walk reports from a 1930:
// twelve-digit counters, so the measured size is that of a device in service
// and not a textbook case. Port naming is a parameter because it is the ONLY
// weight variable the product does not control — see tPortFactory and
// tPortRenamed.
func counterTables(name func(int) string, n int) map[string]map[string]gosnmp.SnmpPDU {
	var tables = map[string]map[string]gosnmp.SnmpPDU{
		tOidIfDescr: {}, tOidIfType: {}, cOidIfHCInOctets: {},
		cOidIfHCOutOctets: {}, cOidIfInErrors: {}, cOidIfOutErrors: {},
	}
	var i int
	for i = 1; i <= n; i++ {
		var idx = strconv.Itoa(i)
		tables[tOidIfDescr][idx] = tPDU(tOidIfDescr, idx, gosnmp.OctetString,
			[]byte(name(i)))
		tables[tOidIfType][idx] = tPDU(tOidIfType, idx, gosnmp.Integer,
			snmppoll.IfTypeEthernet)
		// The magnitudes are those of the controller's test set
		// (internal/api/proxymetrics_limits_test.go): thirteen digits for an octet
		// counter, four for an error counter. This is not fussiness — the number of
		// DIGITS counts in the JSON weight, and two packages announcing "196 B per
		// sample" must have measured the same device, otherwise the shared figure means
		// nothing.
		tables[cOidIfHCInOctets][idx] = tPDU(cOidIfHCInOctets, idx, gosnmp.Counter64,
			uint64(4982133770615))
		tables[cOidIfHCOutOctets][idx] = tPDU(cOidIfHCOutOctets, idx, gosnmp.Counter64,
			uint64(3170488291204))
		tables[cOidIfInErrors][idx] = tPDU(cOidIfInErrors, idx, gosnmp.Counter32, uint(1734))
		tables[cOidIfOutErrors][idx] = tPDU(cOidIfOutErrors, idx, gosnmp.Counter32, uint(902))
	}
	return tables
}

func stateTables(name func(int) string, n int) map[string]map[string]gosnmp.SnmpPDU {
	var tables = map[string]map[string]gosnmp.SnmpPDU{
		tOidIfDescr: {}, tOidIfType: {}, tOidIfOperStatus: {}, cOidIfHighSpeed: {},
	}
	var i int
	for i = 1; i <= n; i++ {
		var idx = strconv.Itoa(i)
		tables[tOidIfDescr][idx] = tPDU(tOidIfDescr, idx, gosnmp.OctetString,
			[]byte(name(i)))
		tables[tOidIfType][idx] = tPDU(tOidIfType, idx, gosnmp.Integer,
			snmppoll.IfTypeEthernet)
		tables[tOidIfOperStatus][idx] = tPDU(tOidIfOperStatus, idx, gosnmp.Integer,
			ifOperUp)
		tables[cOidIfHighSpeed][idx] = tPDU(cOidIfHighSpeed, idx, gosnmp.Gauge32,
			uint(1000))
	}
	return tables
}

// TestProductionPerDevice grounds the setting's arithmetic in the CODE and
// not in a comment: four samples per port for counters, two for state. If
// `snmppoll.Samples` ever emits a fifth, the sustainable rate announced below
// becomes wrong, and this is where we find out.
func TestProductionPerDevice(t *testing.T) {
	var at = refInstant()
	var counters = snmppoll.Samples(tDeviceID,
		counterTables(tPortFactory, portsDu1930_48), at, 0)
	if len(counters) != 4*portsDu1930_48 {
		t.Fatalf("if_counters: %d samples expected (4 per port), got %d",
			4*portsDu1930_48, len(counters))
	}
	var state = snmppoll.Samples(tDeviceID,
		stateTables(tPortFactory, portsDu1930_48), at, 0)
	if len(state) != 2*portsDu1930_48 {
		t.Fatalf("if_state: %d samples expected (2 per port), got %d",
			2*portsDu1930_48, len(state))
	}

	// The cadence decided by the operator: counters at 5 s, state at 30 s. The
	// values are the controller's (internal/api/watch_handlers.go); repeating them
	// here is deliberate, the proxy does not know them — the controller sends them,
	// and this test reasons about the instruction, not about a local setting.
	var perSecond = float64(len(counters))/5 + float64(len(state))/30
	if perSecond < 44 || perSecond > 46 {
		t.Fatalf("~45 samples/s per device expected, got %.1f", perSecond)
	}
	t.Logf("a 48-port 1930 produces %.1f samples/s "+
		"(%d counters / 5 s + %d states / 30 s)",
		perSecond, len(counters), len(state))
}

// batchBody serialises the EXACT envelope `flush` sends: the two
// authentication fields count in the bytes the controller bounds.
func batchBody(t *testing.T, batch []metricbuf.Sample) []byte {
	t.Helper()
	var body, err = json.Marshal(map[string]any{
		"proxy_id": tProxyID, "secret": tSecret, "samples": batch,
	})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

// stretchBatch repeats a set of samples up to `n`. What is measured is the SHAPE of
// a sample; repeating it does not change its weight.
func stretchBatch(proto []metricbuf.Sample, n int) []metricbuf.Sample {
	var out = make([]metricbuf.Sample, 0, n)
	var i int
	for i = 0; i < n; i++ {
		out = append(out, proto[i%len(proto)])
	}
	return out
}

// TestABatchFitsUnderTheControllerCeiling is the most important assertion of the
// file, and the least intuitive: the `maxPerFlush` wall is not in this package.
//
// It is double — the body in BYTES and the batch in COUNT — and it has CHANGED
// SIDES. As long as the route used the generic 1 MiB decoder, bytes bit first:
// the old setting of 5000 samples weighed 1015 KiB, i.e. 99 % of the ceiling,
// hence a stall on the first slightly longer tag. Since the route got its own
// 8 MiB ceiling, the count guard (20,000) comes first. The test therefore no
// longer copies "~4500": it COMPUTES the wall from the weight it has just
// measured, and it will fail if `maxPerFlush` gets close to it, whichever of the
// two walls moves.
func TestABatchFitsUnderTheControllerCeiling(t *testing.T) {
	var at = refInstant()
	// Two namings, because a sample's weight has only one free variable and this
	// is it. Measured here rather than copied from a comment: the 208 B this file
	// used to announce came from a prototype naming its ports "GigabitEthernet52",
	// a naming nobody has in production on a 1930.
	var short = snmppoll.Samples(tDeviceID,
		counterTables(tPortFactory, portsDu1930_48), at, 0)
	var long = snmppoll.Samples(tDeviceID,
		counterTables(tPortRenamed, portsDu1930_48), at, 0)
	var shortWeight = len(batchBody(t, short)) / len(short)
	var longWeight = len(batchBody(t, long)) / len(long)
	t.Logf("weight of a COUNTER: %d B (%q), %d B (%q)",
		shortWeight, tPortFactory(12), longWeight, tPortRenamed(12))

	// The figures written in snmp.go and in worstBytesPerSample on the controller
	// side. If they no longer hold, it is the COMMENTS that must be fixed before the
	// constants — a ceiling justified by a stale measurement is exactly the defect
	// this batch fixes. The thresholds allow the noise of one more counter digit,
	// not room for an extra tag.
	if shortWeight > 200 || longWeight > 240 {
		t.Errorf("a counter weighs %d B (factory naming, 196 documented) and %d B "+
			"(37-character label, 229 documented): the sample shape has "+
			"grown, revisit the comments of maxPerFlush and worstBytesPerSample",
			shortWeight, longWeight)
	}

	// The batch actually sent, in the heaviest case.
	var body = batchBody(t, stretchBatch(long, maxPerFlush))
	t.Logf("batch of %d samples = %d bytes (%d KiB), i.e. %d %% of the ceiling of "+
		"corps de %d KiB", maxPerFlush, len(body), len(body)/1024,
		100*len(body)/controllerBodyCapBytes, controllerBodyCapBytes/1024)
	if len(body) >= controllerBodyCapBytes {
		t.Fatalf("batch of %d samples = %d bytes: the controller WILL REFUSE it "+
			"(body ceiling %d) and the proxy will DROP it on every flush — "+
			"upstream would turn into logged waste",
			maxPerFlush, len(body), controllerBodyCapBytes)
	}

	// The wall, computed from the measurement. Two candidates, the lower wins — and
	// today it is the count, not the bytes. Saying it in the test log is half of
	// its value: whoever comes to raise `maxPerFlush` will read the figure instead
	// of deducing it.
	var wallByBytes = controllerBodyCapBytes / longWeight
	var wall = min(controllerMaxSamples, wallByBytes)
	t.Logf("maxPerFlush wall: %d samples — %d from the count guard, "+
		"%d from the body ceiling at %d B/sample; current setting %d (%d %% of the wall)",
		wall, controllerMaxSamples, wallByBytes, longWeight, maxPerFlush,
		100*maxPerFlush/wall)
	if maxPerFlush > wall {
		t.Fatalf("maxPerFlush=%d crosses the controller's wall (%d): every full batch "+
			"will be refused permanently, hence dropped", maxPerFlush, wall)
	}
	// A margin, not a close shave. The weight per sample depends on a label the
	// operator chooses and no code bounds: under 75 % of the wall, port names
	// growing by a third still fit.
	if maxPerFlush > wall*3/4 {
		t.Errorf("maxPerFlush=%d exceeds 75 %% of the %d wall: margin too thin for "+
			"port labels longer than those measured", maxPerFlush, wall)
	}
}

// TestSustainableRate documents what upstream absorbs AFTER the setting, and at
// what price. The three assertions are the batch's three trade-offs; changing
// one without changing the figure opposite is a silent regression.
func TestSustainableRate(t *testing.T) {
	// ~45 samples/s per 48 ports: established by TestProductionPerDevice.
	const perDevice = 45
	var sustainable = sustainedSamplesPerSecond()
	var deviceCount = sustainable / perDevice
	t.Logf("sustainable rate %d samples/s = %d devices of 48 ports "+
		"(%d batches of %d every %v)",
		sustainable, deviceCount, maxFlushBurst, maxPerFlush, flushEvery)

	// Before this batch: 5000 / 10 s = 500/s, i.e. 11 devices. The threshold kept
	// here is deliberately round and low next to the 133 computed: it says
	// "upstream must hold a whole site fleet", not "it must be exactly 6000".
	if deviceCount < 100 {
		t.Errorf("only %d devices of 48 ports (%d samples/s): "+
			"the 5 s cadence would not keep up on a site fleet", deviceCount, sustainable)
	}

	// The other side of the scale: the cost in HTTP requests to the controller. At
	// the rate ceiling, not in ordinary operation — an empty buffer produces no
	// request, `flush` returning before sending.
	var requestsPerSecond = float64(maxFlushBurst) / flushEvery.Seconds()
	t.Logf("at the ceiling: %.1f request/s to the controller", requestsPerSecond)
	if requestsPerSecond > 3 {
		t.Errorf("%.1f request/s at the ceiling: a more frequent flush multiplies "+
			"requests, it is not free for the controller", requestsPerSecond)
	}

	// Once the rate is sufficient, the buffer only serves to cover a TUNNEL
	// outage. Its size therefore reads in seconds of unavailability, and its upper
	// bound is the memory of a proxy running on a Raspberry Pi.
	//
	// 401 B/sample on the heap: measured (runtime.MemStats over 200,000 samples
	// with their tag map). Most of it is the map, not the value.
	const heapBytes = 401
	var coverageAtCeiling = bufCapacity / sustainable
	var memoryMiB = bufCapacity * heapBytes / (1 << 20)
	t.Logf("buffer of %d samples: %d s of outage at the rate ceiling, "+
		"%d s for a 12-device fleet, ~%d MiB of memory at worst",
		bufCapacity, coverageAtCeiling, bufCapacity/(12*perDevice), memoryMiB)
	if coverageAtCeiling < 20 {
		t.Errorf("buffer too short: %d s of outage covered at the ceiling, "+
			"a tunnel reconnection takes longer than that", coverageAtCeiling)
	}
	if memoryMiB > 80 {
		t.Errorf("buffer at ~%d MiB: too much for a proxy on a minimal VM", memoryMiB)
	}
}

// TestSaturationShowsThenThrottles: a SATURATION loss is the symptom the 5 s
// cadence makes possible, and it is the most treacherous — the tunnel is up,
// requests succeed, `flush` has nothing to report, and all that is left is
// holes in the graphs. It must therefore be said right away, then fall silent.
func TestSaturationShowsThenThrottles(t *testing.T) {
	// Tiny buffer, built by hand: what is tested is putting the loss into words,
	// not the real capacity — filling 150,000 samples would cost 57 MiB for
	// exactly the same assertion.
	var c = &SNMPCollector{buf: metricbuf.New(10)}

	// Nothing lost: nothing to say, and the throttling state does not move. Without
	// this condition, the very first log line of a healthy proxy would announce a
	// saturation with zero points lost.
	var at, dropped = c.reportSaturation(time.Time{}, 0)
	if !at.IsZero() || dropped != 0 {
		t.Fatalf("without loss, nothing must be announced: %v / %d", at, dropped)
	}

	// Fifteen samples in a buffer of ten: five points dropped.
	var i int
	for i = 0; i < 15; i++ {
		c.buf.Add(metricbuf.Sample{Name: "meshcraft_if_in_octets_total"})
	}
	if at, dropped = c.reportSaturation(time.Time{}, 0); at.IsZero() || dropped != 5 {
		t.Fatalf("first loss: immediate announcement of 5 points expected, "+
			"got %v / %d", at, dropped)
	}

	// The loss goes on, but we just said so. Throttled — and the total already
	// announced is KEPT as is, otherwise the next line could no longer announce the
	// whole period.
	for i = 0; i < 4; i++ {
		c.buf.Add(metricbuf.Sample{Name: "meshcraft_if_in_octets_total"})
	}
	var at2, dropped2 = c.reportSaturation(at, dropped)
	if !at2.Equal(at) || dropped2 != dropped {
		t.Fatalf("throttling expected, got %v / %d", at2, dropped2)
	}

	// Once the delay passes, the line comes back. The total returned is the
	// buffer's (9), and the line announces the difference — the 4 points of the
	// period, not the 9 since startup: "4 lost in the last minute" can be acted
	// on, "9 since startup" can only be endured.
	if at2, dropped2 = c.reportSaturation(at.Add(-dropReportEvery),
		dropped); dropped2 != 9 {
		t.Fatalf("after throttling: total of 9 expected, got %d", dropped2)
	}
	if !at2.After(at) {
		t.Errorf("the instant of the last report must move forward, got %v", at2)
	}
}
