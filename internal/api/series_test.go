package api

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"meshcraft/internal/metricbuf"
	"meshcraft/internal/model"
)

// SCOPING of series queries, and per-port rate calculation.
//
// What these tests protect comes in two kinds, and the first is a leak: a query
// without a device matcher returns traffic from ALL instance clients, because
// nothing in the series store knows about sites. The second is false display — a
// rate attached to the wrong port produces a perfectly credible graph.

func dev(id, status string, ports map[string]any) model.Device {
	var raw, _ = json.Marshal(ports)
	var groups, _ = json.Marshal([]map[string]any{
		{"prefix": "GE", "from": 1, "count": 50, "speed": "1G"},
	})
	return model.Device{ID: id, Status: status, Ports: raw, PortGroups: groups}
}

const idA = "11111111-1111-1111-1111-111111111111"
const idB = "22222222-2222-2222-2222-222222222222"

// TestNoQueryWithoutADeviceMatcher — gate that prevents cross-site leakage.
//
// All exit paths return an EMPTY string, and the caller must then send NOTHING.
// Worst case: a well-formed query without a matcher succeeds, and one site's
// chart shows traffic for the entire instance.
func TestNoQueryWithoutADeviceMatcher(t *testing.T) {
	var cas = []struct {
		name   string
		query  string
		reason string
	}{
		{"no devices", portRateQuery(nil, metricbuf.MetricIfInOctets),
			"empty list allows no query"},
		{"identifiers that are not UUIDs",
			portRateQuery([]string{"*", "..", "d1"}, metricbuf.MetricIfInOctets),
			"malformed id never enters a query"},
		{"metric outside vocabulary",
			portRateQuery([]string{idA}, "meshcraft_invented"),
			"vocabulary is CLOSED: product constants only"},
		{"metric outside vocabulary, site side",
			siteRateQuery([]model.Device{dev(idA, "ok", nil)}, "up"),
			"same rule for site sum"},
		{"site with no devices", siteRateQuery(nil, metricbuf.MetricIfInOctets), ""},
		{"site where all devices are off",
			siteRateQuery([]model.Device{dev(idA, "off", nil)}, metricbuf.MetricIfInOctets),
			"powered-off switch does not count: its rate no longer exists"},
	}
	var c = cas[0]
	for _, c = range cas {
		if c.query != "" {
			t.Errorf("%s: query composed when nothing should be requested — %q (%s)",
				c.name, c.query, c.reason)
		}
	}
}

// TestThePerPortQueryFramesAndFilters: three elements that make it correct.
func TestThePerPortQueryFramesAndFilters(t *testing.T) {
	var q = portRateQuery([]string{idA, idB, "not-a-uuid"}, metricbuf.MetricIfInOctets)
	var want = []string{
		metricbuf.MetricIfInOctets, `device=~"` + idA + "|" + idB + `"`,
		// `=~"6|"`: Ethernet by SNMP's iftype, OR no iftype at all — the gNMI
		// label shape. The strict form excluded every streaming device's ports
		// from every rate consumer while the store held them fresh (core01,
		// measured 2026-08-12: 0 series with iftype="6", 65 without).
		`iftype=~"6|"`, "[120s]", "* 8 / 1e6",
	}
	var m string
	for _, m = range want {
		if !strings.Contains(q, m) {
			t.Errorf("query must contain %q; got %s", m, q)
		}
	}
	if strings.Contains(q, "not-a-uuid") {
		t.Error("malformed id entered the query")
	}
	// Window in seconds: time.Duration.String() would yield "2m0s", which PromQL
	// rejects — with a syntax message to someone staring at an empty chart.
	if strings.Contains(q, "2m0s") {
		t.Error("duration must be written in seconds")
	}
}

// TestASiteSumExcludesUplinks — DOUBLE COUNTING trap.
//
// Access port traffic exits via uplink: summing everything counts every octet
// twice. Replacing a simulated curve with a measurement that DOUBLES display
// would read as an incident, and this test prevents that.
func TestASiteSumExcludesUplinks(t *testing.T) {
	var d = dev(idA, "ok", map[string]any{
		"GE1":  map[string]any{"mode": "access", "pvid": 100},
		"GE49": map[string]any{"mode": "trunk", "tagged": "all"},
		"GE50": map[string]any{"mode": "trunk", "tagged": []int{10, 20}},
	})
	var q = siteRateQuery([]model.Device{d}, metricbuf.MetricIfInOctets)
	if !strings.Contains(q, `port!~"GE49"`) {
		t.Errorf("all-VLAN trunk must be excluded: %s", q)
	}
	if strings.Contains(q, "GE50") {
		t.Errorf("trunk listing VLANs is not an aggregation link: %s", q)
	}
	if strings.Contains(q, "GE1") {
		t.Errorf("access port must not be excluded: %s", q)
	}
}

// TestASiteSumJoinsByOrNotByPlus — THE PromQL subtlety in this file.
//
// sum(a) + sum(b) yields EMPTY as soon as one member has no series: one
// uncollected device would erase the whole site chart. or unions vectors, and the
// outer sum aggregates them.
func TestASiteSumJoinsByOrNotByPlus(t *testing.T) {
	var q = siteRateQuery([]model.Device{dev(idA, "ok", nil), dev(idB, "ok", nil)},
		metricbuf.MetricIfInOctets)
	if strings.Count(q, "sum(") != 1 {
		t.Errorf("want one outer sum: %s", q)
	}
	if !strings.Contains(q, " or ") {
		t.Errorf("terms must be joined by or: %s", q)
	}
	if strings.Contains(q, ") + ") {
		t.Errorf("+ between sums clears chart when one member is empty: %s", q)
	}
}

// TestAPortNameDoesNotBreakTheQuery: labels come from DEVICES.
//
// Fleets already have every shape ("SFP1", "sfp-sfpplus2", "49"), and one quote
// in a name would break the query — making a whole site's chart vanish for one
// badly named port on one switch.
func TestAPortNameDoesNotBreakTheQuery(t *testing.T) {
	var d = dev(idA, "ok", map[string]any{
		`GE1"; drop`: map[string]any{"mode": "trunk", "tagged": "all"},
	})
	var q = siteRateQuery([]model.Device{d}, metricbuf.MetricIfInOctets)
	if strings.Contains(q, `GE1";`) {
		t.Errorf("quote not escaped: %s", q)
	}
	if !strings.Contains(q, `GE1\"`) {
		t.Errorf("name must be escaped and preserved: %s", q)
	}
}

// TestExcludedPortsOrderIsSTABLE: these names compose a query compared call
// to call.
func TestExcludedPortsOrderIsSTABLE(t *testing.T) {
	var ports = map[string]any{
		"GE49": map[string]any{"mode": "trunk", "tagged": "all"},
		"GE50": map[string]any{"mode": "trunk", "tagged": "all"},
		"GE2":  map[string]any{"mode": "trunk", "tagged": "all"},
	}
	var first = siteRateQuery([]model.Device{dev(idA, "ok", ports)}, metricbuf.MetricIfInOctets)
	var i int
	for i = 0; i < 20; i++ {
		if siteRateQuery([]model.Device{dev(idA, "ok", ports)},
			metricbuf.MetricIfInOctets) != first {
			t.Fatal("query changes between runs: Go map iteration order " +
				"decides its shape")
		}
	}
}

// --- Writing rates into inventory ---

// TestTheRateLandsOnTheRightJack: label resolution.
//
// Most costly mistake in this batch: rate on the wrong port yields a credible
// false graph. Three forms seen in the field are tested — exact facade name,
// label differing only in punctuation, and bare number (what a 1930 reports).
func TestTheRateLandsOnTheRightJack(t *testing.T) {
	var d = dev(idA, "ok", nil)
	var patch = portRatePatch(d,
		map[string]float64{"GE1": 12.5, "ge-2": 3, "49": 100},
		map[string]float64{"GE1": 1.25})
	var cas = []struct {
		port string
		rx   float64
		tx   float64
	}{
		{"GE1", 12.5, 1.25},
		{"GE2", 3, 0},
		{"GE49", 100, 0},
	}
	var c = cas[0]
	for _, c = range cas {
		var got, ok = patch[c.port]
		if !ok {
			t.Errorf("no rate on %s: %v", c.port, patch)
			continue
		}
		if got["rx_mbps"] != c.rx || got["tx_mbps"] != c.tx {
			t.Errorf("%s = %v; want rx=%v tx=%v", c.port, got, c.rx, c.tx)
		}
	}
}

// TestAnUnknownLabelCreatesNoPort: telemetry must NEVER create a port.
//
// A device exposes far more SNMP interfaces than front-panel ports — VLANs,
// aggregates, loops, with indexes seen at 1000 and 100000 on a 1930. Accepting
// them would fill inventory with ghost ports carrying traffic.
func TestAnUnknownLabelCreatesNoPort(t *testing.T) {
	var patch = portRatePatch(dev(idA, "ok", nil),
		map[string]float64{"vlan99": 50, "lo0": 1, "GE99": 7}, nil)
	var name string
	for name = range patch {
		if name == "vlan99" || name == "lo0" || name == "GE99" {
			t.Errorf("ghost port created: %s", name)
		}
	}
}

// TestAPortWithoutSeriesFallsBackToZero — guard against PHANTOM rate.
//
// A port whose series vanishes — collection stopped, collector detached — would
// keep its last displayed rate forever: frozen load nothing contradicts, on a
// device nobody polls anymore. Between a chart that drops and a permanent ghost,
// the first reads correctly.
func TestAPortWithoutSeriesFallsBackToZero(t *testing.T) {
	var d = dev(idA, "ok", map[string]any{
		"GE1": map[string]any{"mode": "access", "rx_mbps": 40.0, "tx_mbps": 4.0},
	})
	var patch = portRatePatch(d, nil, nil)
	var got, ok = patch["GE1"]
	if !ok {
		t.Fatalf("port that had a rate must be zeroed: %v", patch)
	}
	if got["rx_mbps"] != 0.0 || got["tx_mbps"] != 0.0 {
		t.Errorf("GE1 = %v, want zero", got)
	}
}

// TestNoWriteWhenNothingChanges: this path rewrites the ports column and
// wakes site screens.
//
// Without this check, a fleet where nothing moves would still get a database
// write and wake every browser twice a minute to redraw the same picture.
func TestNoWriteWhenNothingChanges(t *testing.T) {
	var d = dev(idA, "ok", map[string]any{
		"GE1": map[string]any{"mode": "access", "rx_mbps": 12.5, "tx_mbps": 1.25},
	})
	var patch = portRatePatch(d, map[string]float64{"GE1": 12.5},
		map[string]float64{"GE1": 1.25})
	var _, present = patch["GE1"]
	if present {
		t.Errorf("GE1 rewritten although rate unchanged: %v", patch)
	}
	// One thousandth of Mbit/s — one kilobit — is a change.
	patch = portRatePatch(d, map[string]float64{"GE1": 12.502},
		map[string]float64{"GE1": 1.25})
	_, present = patch["GE1"]
	if !present {
		t.Error("one-kilobit delta must be written")
	}
	// Noise below one thousandth must not: it would write every poll for identical display.
	patch = portRatePatch(d, map[string]float64{"GE1": 12.5000004},
		map[string]float64{"GE1": 1.25})
	_, present = patch["GE1"]
	if present {
		t.Errorf("sub-thousandth noise triggered rewrite: %v", patch)
	}
}

// TestAberrantValuesBroughtBackToZero: NaN, infinity, and negative never reach the UI.
//
// Negative rate does not exist; showing it would send someone hunting bad cabling.
// NaN serialized to JSON would break the ENTIRE device record response — not
// just the offending value.
//
// Device already carries rates: that forces a write, making the corrected value
// observable. Without that the fix would be empty, which is correct but proves
// nothing.
func TestAberrantValuesBroughtBackToZero(t *testing.T) {
	var d = dev(idA, "ok", map[string]any{
		"GE1": map[string]any{"rx_mbps": 40.0},
		"GE2": map[string]any{"rx_mbps": 40.0},
		"GE3": map[string]any{"rx_mbps": 40.0},
	})
	var patch = portRatePatch(d,
		map[string]float64{"GE1": math.NaN(), "GE2": -5, "GE3": math.Inf(1)}, nil)
	var name string
	for _, name = range []string{"GE1", "GE2", "GE3"} {
		var got, ok = patch[name]
		if !ok {
			t.Errorf("%s should be zeroed: %v", name, patch)
			continue
		}
		if got["rx_mbps"] != 0.0 {
			t.Errorf("%s = %v, want 0", name, got)
		}
	}
	// Patch must remain serializable: practical consequence of NaN.
	var _, err = json.Marshal(patch)
	if err != nil {
		t.Errorf("patch not serializable: %v", err)
	}
}

// TestThePerPortGaugeFramesWithoutDeriving: the optics query is a GAUGE read.
//
// Two properties, each preventing a false display: no rate() — a dBm level
// through a derivative would graph the DRIFT of the level and call it the
// level — and the same closed vocabulary and device matcher as every other
// composed query. A counter asked through this path composes nothing.
func TestThePerPortGaugeFramesWithoutDeriving(t *testing.T) {
	var q = portGaugeQuery([]string{idA, "not-a-uuid"}, metricbuf.MetricSFPRxPowerDBM)
	if q == "" {
		t.Fatal("gauge query refused for a metric the vocabulary declares")
	}
	if !strings.Contains(q, `device=~"`+idA+`"`) {
		t.Errorf("device matcher missing: %s", q)
	}
	if strings.Contains(q, "rate(") {
		t.Errorf("a gauge must be read as-is: %s", q)
	}
	if strings.Contains(q, "not-a-uuid") {
		t.Errorf("malformed id entered the query: %s", q)
	}
	if portGaugeQuery([]string{idA}, metricbuf.MetricIfInOctets) != "" {
		t.Error("a counter through the gauge path would graph two billion " +
			"octets as a level")
	}
	if portGaugeQuery(nil, metricbuf.MetricSFPRxPowerDBM) != "" {
		t.Error("no devices allows no query")
	}
	if portGaugeQuery([]string{idA}, "meshcraft_invented") != "" {
		t.Error("vocabulary is CLOSED for gauges too")
	}
}
