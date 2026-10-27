package api

import (
	"encoding/json"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"meshcraft/internal/metricbuf"
	"meshcraft/internal/model"
	"meshcraft/internal/snmppoll"
)

// Series query composition, and the SCOPING that makes them safe.
//
// # WHY THIS FILE EXISTS INSTEAD OF A PASS-THROUGH ROUTE
//
// The temptation when wiring a series store behind a web UI is to expose a route that
// relays the query exactly as the screen wrote it. That would be two flaws in one, and
// neither shows up in normal use:
//
//   - CROSS-SITE LEAK. The `device` label scopes a series to a site, and the store knows
//     nothing about sites: `sum(rate(...))` without a matcher returns traffic from ALL
//     instance tenants. Scoping cannot be verified after the fact; it must be BUILT here,
//     from the device list the caller is allowed to read;
//   - DENIAL OF SERVICE ONE QUERY AWAY. A one-year range at one-second step over a whole
//     fleet loads tens of millions of points to draw a six-hundred-pixel chart.
//
// Hence: queries are composed by PURE functions in this file, from a CLOSED metric name
// vocabulary (`internal/metricbuf`) and already-authorized device IDs. Nothing a client
// sends enters a query without passing through here.
//
// # WHAT IS NOT SHIPPED, DELIBERATELY
//
// No route serves raw series. It would have no consumer: the two readers of this set are
// internal — site traffic curve and per-port rate refresh — and a public route without a
// screen would be exactly the lapse this repo already regretted elsewhere ("JSON type
// tags ready and inert"). When a telemetry screen exists, its scoping will be defined
// here.

// --- Allowed vocabulary ---

// metricKind distinguishes GAUGE from COUNTER; the distinction drives the query, not
// just display.
//
// A gauge reads as-is: 74 °C IS a temperature. A counter means nothing without a
// derivative — `meshcraft_if_in_octets_total` is two billion on an active cage, total
// since last reboot, never a rate at any instant. Confusing them would show gigabytes
// where megabits per second are expected, or temperature divided by time.
type metricKind int

const (
	kindGauge metricKind = iota
	kindCounter
)

// metricSpec describes what a series family is and what we render from it.
type metricSpec struct {
	kind metricKind
	// unit is the unit of what is RETURNED, derivative and scale factor included. It
	// goes to the screen: it prevents showing "0.002 byte" for 0.002 Mbit/s.
	unit string
	// scale is the expression suffix applied AFTER any derivative. Empty for most
	// families; octets become megabits per second.
	scale string
}

// queryable is the CLOSED set of series a query may name.
//
// Closed, not "everything the store contains": these names go into a query, and the
// only way to guarantee no client-supplied value enters is to accept only product
// constants. That also makes a metric rename fail at COMPILE time rather than on screen.
//
// What this table does NOT contain is also a decision: `if_oper_status` and
// `if_speed_mbps` are absent because inventory already has them (`devices.ports[].link`,
// cf. applyPortTelemetry), and serving them twice via two paths would give two truths for
// one cable. Optical sensors, voltages, fans, and PSUs are absent too: no fleet device
// publishes them today (cf. MikroTik Telemetry §3.3), and declaring them readable would
// promise a chart no collection fills.
var queryable = map[string]metricSpec{
	// Interface counters, derived to rate.
	metricbuf.MetricIfInOctets:  {kindCounter, "Mbit/s", " * 8 / 1e6"},
	metricbuf.MetricIfOutOctets: {kindCounter, "Mbit/s", " * 8 / 1e6"},
	// Errors, derived to errors per second. TOTAL means nothing — a device up a year
	// necessarily has some — whereas errors per second appearing NOW is a cable or
	// transceiver degrading.
	metricbuf.MetricIfInErrors:  {kindCounter, "err/s", ""},
	metricbuf.MetricIfOutErrors: {kindCounter, "err/s", ""},
	// Traffic decomposed by destination class, derived to PACKETS per second
	// (ADR-0028). Packets and not bits, deliberately: these classes are the ones
	// the storm limiter polices, the limiter's own unit vocabulary is pps
	// (StormRatePPS), and a broadcast storm is a frame-rate problem — ten
	// thousand 64-byte broadcasts per second is a melted control plane and a
	// barely visible bit rate. Converting to bits would need a mean frame size
	// nobody measures.
	//
	// Being counters, these go through portRateQuery like the octets above, so
	// VictoriaMetrics' rate() absorbs a reboot's counter reset. No raw counter
	// reaches the browser.
	metricbuf.MetricIfInMulticastPackets:  {kindCounter, "pkt/s", ""},
	metricbuf.MetricIfOutMulticastPackets: {kindCounter, "pkt/s", ""},
	metricbuf.MetricIfInBroadcastPackets:  {kindCounter, "pkt/s", ""},
	metricbuf.MetricIfOutBroadcastPackets: {kindCounter, "pkt/s", ""},
	// Device gauges.
	metricbuf.MetricTemperature: {kindGauge, "°C", ""},
	metricbuf.MetricCPULoad:     {kindGauge, "%", ""},
	metricbuf.MetricMemoryFree:  {kindGauge, "B", ""},
	metricbuf.MetricMemoryTotal: {kindGauge, "B", ""},
	metricbuf.MetricUptime:      {kindGauge, "s", ""},
	// Device PoE draw, produced by the controller's PoE sampler
	// (poesample.go). The DEVICE total only: the per-port series
	// (meshcraft_poe_power_watts) stays out until a screen consumes it —
	// the per-port query paths here are counter/rate-shaped (portRateQuery),
	// and declaring a family nothing composes would be exactly the "ready
	// and inert" lapse the header warns about.
	metricbuf.MetricPoEUsedW: {kindGauge, "W", ""},
	// Transceiver optical levels, per port (and per lane on producers that read
	// channels — the `lane` label passes through untouched). Gauges in dBm, read
	// through portGaugeQuery: the device page's optics chart and the low-RX
	// dashboard route are the consumers, which is what admits them to the
	// vocabulary at all.
	metricbuf.MetricSFPRxPowerDBM: {kindGauge, "dBm", ""},
	metricbuf.MetricSFPTxPowerDBM: {kindGauge, "dBm", ""},
}

// metricAllowed reports whether this series name may enter a query.
func metricAllowed(name string) bool {
	var _, ok = queryable[name]
	return ok
}

// metricUnit returns the unit of what is rendered for this family.
func metricUnit(name string) string { return queryable[name].unit }

// --- Identifiers and labels ---

// reDeviceID: a device ID is a UUID, nothing else.
//
// Validation here is IN ADDITION to origin (these IDs come from the database): a value
// entering a query must be validated where the query is composed, otherwise the next
// caller inherits trust it did not earn.
var reDeviceID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-` +
	`[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// escapeMatcher protects a label value inside a matcher.
//
// Port names come from DEVICES: free labels, and fleet records already show every shape
// ("SFP1", "sfp-sfpplus2", "49"). A quote or backslash is enough to break the whole
// query — and blank the chart for an entire site because one switch port was badly named.
func escapeMatcher(v string) string {
	var r = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", ``, "\r", ``)
	return r.Replace(v)
}

// --- Per-PORT rate: what feeds inventory ---

// rateWindow is the window over which rate is computed.
//
// TWO MINUTES, not an arbitrary setting: `rate()` needs at least two points in its window,
// and the slowest collection floor in the fleet is 60 s (cf. BestOfKind). A 60 s window
// would therefore be EMPTY on those devices — i.e. zero rate, indistinguishable from an
// idle port. Two minutes cover the worst case with one sample of margin.
//
// The price is SMOOTHING: a two-second burst is diluted. That is the right trade-off for
// the three screens reading these fields — they show load, not a trigger — and fine
// measurement stays in the series.
const rateWindow = 2 * time.Minute

// portRateQuery composes per-PORT rates in Mbit/s.
//
// `iftype` is required, as in applyPortTelemetry and for the same reason: only Ethernet
// interfaces have a faceplate port. Without this filter, a tunnel or loopback would get
// rate on a port that does not exist.
//
// Returns an EMPTY string when there is nothing to ask — no devices, or metric name outside
// vocabulary. An empty query must NEVER fire: without a `device` matcher it would return
// traffic for the whole instance.
func portRateQuery(deviceIDs []string, metric string) string {
	var spec, ok = queryable[metric]
	if !ok || spec.kind != kindCounter {
		return ""
	}
	var kept []string
	var id string
	for _, id = range deviceIDs {
		if reDeviceID.MatchString(id) {
			kept = append(kept, id)
		}
	}
	if len(kept) == 0 {
		return ""
	}
	// ALTERNATION (`device=~"a|b|c"`) rather than one query per device: a two-hundred-switch
	// fleet would mean two hundred HTTP round trips every thirty seconds, for a response
	// the store can compose in one shot.
	//
	// `iftype` matches "6" OR ABSENT (`=~"6|"` — the empty alternative is how
	// PromQL says "no such label"), and the second half is a FIX, not a
	// relaxation for comfort. gNMI samples deliberately carry no iftype
	// (internal/gnmipoll/translate.go: the protocol has no interface index, and
	// inventing one would let telemetry touch inventory), so the strict filter
	// silently excluded every streaming device: core01's 65 ports were IN the
	// store, fresh, and this query returned none of them — port charts and
	// facade rates empty over a healthy collection. Measured 2026-08-12: with
	// iftype="6", zero series; without, 65. The non-Ethernet interfaces the
	// strict filter kept out of SNMP results are kept out of gNMI results by the
	// consumers instead: every reader of this query resolves the port label
	// against the facade and DROPS what does not resolve (portRatePatch,
	// telemetryOf), so a Management1 series feeds no screen and creates nothing.
	return "rate(" + metric + `{device=~"` + strings.Join(kept, "|") +
		`",iftype=~"` + strconv.Itoa(snmppoll.IfTypeEthernet) + `|"}[` +
		promDuration(rateWindow) + "])" + spec.scale
}

// portGaugeQuery composes a per-PORT gauge read — the optics levels.
//
// The gauge twin of portRateQuery, separate rather than a flag on it because
// the two differ in everything but the matcher: no rate(), no scale, and no
// iftype term at all — the only producers of per-port gauges in the vocabulary
// (gNMI transceiver channels, the MikroTik optical readers) label with `port`
// alone, and `iftype=~"…|"` matching absent would be a term that never selects
// anything. The same consumer-side facade resolution applies: an unresolvable
// port label feeds no screen.
func portGaugeQuery(deviceIDs []string, metric string) string {
	var spec, ok = queryable[metric]
	if !ok || spec.kind != kindGauge {
		return ""
	}
	var kept []string
	var id string
	for _, id = range deviceIDs {
		if reDeviceID.MatchString(id) {
			kept = append(kept, id)
		}
	}
	if len(kept) == 0 {
		return ""
	}
	return metric + `{device=~"` + strings.Join(kept, "|") + `"}` + spec.scale
}

// --- Optical receive levels: the low-RX comparison ---

// The low-RX rule, in one place because three numbers compose it.
//
// A receive level is flagged when it sits at least [opticsAlertMarginDB] below
// the LOWEST HEALTHY level ever recorded for that cage — "this fibre is worse
// than it has ever been, by more than noise". The reference is each SFP's own
// history rather than a fleet threshold, because the product deliberately does
// not parse module alarm tables (cf. web/src/pages/switchdetail/optics.ts): a
// -14 dBm that has been -14 dBm for a month is a long fibre doing fine, and the
// same -14 on a link that never sat below -9 is a degradation.
//
//   - [opticsDarkCutoffDBm] separates "low" from "no usable light". Common SFP
//     receivers bottom out around -18‥-24 dBm; nothing operates at -25, and EOS
//     clamps an unlit cage at exactly -30. Samples at or under the cutoff are
//     EXCLUDED from the floor: one maintenance unplug would otherwise pin the
//     floor at -30 for thirty days and nothing could ever be flagged again.
//     They are NOT excluded from the current reading — a port that goes dark
//     after a healthy history is precisely a row this list must show;
//   - [opticsFreshWindow] bounds how old a "current" reading may be. The
//     subscription samples every 30-60 s, so fifteen minutes tolerates a
//     collection gap while refusing to alert on the last reading of a device
//     that has been off since yesterday — a stale level is a device problem,
//     and the reachability tiles own that;
//   - [opticsFloorWindow] is how much history the floor considers, minus the
//     fresh window (`offset`), so the current dip is never its own reference.
const (
	opticsAlertMarginDB = 1.0
	opticsDarkCutoffDBm = -25.0
	opticsFreshWindow   = 15 * time.Minute
	opticsFloorWindow   = 30 * 24 * time.Hour
)

// opticsCurrentQuery reads the freshest receive level of every cage of the
// given devices. Instant query; one series per (device, port, lane).
func opticsCurrentQuery(deviceIDs []string) string {
	var matcher = opticsDeviceMatcher(deviceIDs)
	if matcher == "" {
		return ""
	}
	return "last_over_time(" + metricbuf.MetricSFPRxPowerDBM + matcher +
		"[" + promDuration(opticsFreshWindow) + "])"
}

// opticsFloorQuery reads the lowest HEALTHY receive level each cage has shown
// over the floor window, current window excluded.
//
// The `> cutoff` sits INSIDE the subquery so dark samples never enter the min;
// applying it outside would instead drop every port whose floor is dark — the
// exact ports the comparison exists for. The subquery step is five minutes:
// the floor is a slow-moving reference, and sampling a 30-day window at the
// collection cadence would make this the most expensive query the product
// composes.
func opticsFloorQuery(deviceIDs []string) string {
	var matcher = opticsDeviceMatcher(deviceIDs)
	if matcher == "" {
		return ""
	}
	return "min_over_time((" + metricbuf.MetricSFPRxPowerDBM + matcher +
		" > " + strconv.FormatFloat(opticsDarkCutoffDBm, 'f', -1, 64) +
		")[" + promDuration(opticsFloorWindow) + ":" + promDuration(5*time.Minute) +
		"] offset " + promDuration(opticsFreshWindow) + ")"
}

// opticsDeviceMatcher composes the shared device-scoping matcher, or "" when
// nothing may be asked — same contract as every composer in this file.
func opticsDeviceMatcher(deviceIDs []string) string {
	var kept []string
	var id string
	for _, id = range deviceIDs {
		if reDeviceID.MatchString(id) {
			kept = append(kept, id)
		}
	}
	if len(kept) == 0 {
		return ""
	}
	return `{device=~"` + strings.Join(kept, "|") + `"}`
}

// --- Site throughput: the curve, and double-counting trap ---

// siteRateQuery composes summed site rates in Mbit/s.
//
// # DOUBLE COUNTING IS THE TRAP OF THIS SUM
//
// Access port traffic exits via the switch uplink: summing all ports on a site therefore
// counts each octet TWICE, and the chart shows double real traffic. The reconstructed
// version already applied this rule on inventory side ("an all-VLAN trunk is aggregation:
// its traffic is already counted on access ports feeding it"), and it must survive the
// move to series — otherwise replacing a simulated curve with measurement would DOUBLE
// the display, which reads as an incident.
//
// Exclusion uses a per-DEVICE negative matcher, and cannot be global: "GE49" is an uplink
// on one switch and an access port on its neighbor. Terms are joined with `or`, not `+` —
// THE PromQL subtlety here: `sum(a) + sum(b)` returns EMPTY as soon as one side has no
// series, so one uncollected device would erase the whole site chart.
//
// Returns empty string when no device has ports to count.
func siteRateQuery(devices []model.Device, metric string) string {
	var spec, ok = queryable[metric]
	if !ok || spec.kind != kindCounter {
		return ""
	}
	var terms []string
	var i int
	for i = range devices {
		var d = devices[i]
		if !reDeviceID.MatchString(d.ID) {
			continue
		}
		// Unreachable device does not count: adding traffic from a powered-off switch
		// would inflate the curve with throughput that no longer exists. Same rule as
		// portRates, which this replaces.
		if d.Status == "off" {
			continue
		}
		// STRICT `iftype="6"` here, unlike portRateQuery, and the asymmetry is a
		// decision: this sum happens store-side, with no facade resolution on
		// arrival, and its correctness rests on the `port!~` exclusion below —
		// which is written in FACADE names ("ET48"). A gNMI device's labels are
		// the platform's own spellings ("Ethernet48"), so the exclusion cannot
		// match them and its uplinks would be double-counted into the curve.
		// Until the exclusion can be composed in label vocabulary, a streaming
		// device stays out of the site sum entirely — under-counting, which reads
		// as "one device missing", over the double-count that reads as an
		// incident.
		var m = metric + `{device="` + d.ID + `",iftype="` +
			strconv.Itoa(snmppoll.IfTypeEthernet) + `"`
		var excluded = aggregationPorts(d)
		if len(excluded) > 0 {
			m += `,port!~"` + strings.Join(excluded, "|") + `"`
		}
		m += "}"
		terms = append(terms, "rate("+m+"["+promDuration(rateWindow)+"])")
	}
	if len(terms) == 0 {
		return ""
	}
	return "sum(" + strings.Join(terms, " or ") + ")" + spec.scale
}

// aggregationPorts returns ports whose traffic is ALREADY counted elsewhere: "all VLAN"
// trunks, i.e. aggregation links.
//
// Read from intent, not measurement, because this is topology data: what makes a port an
// uplink is the role the operator gave it.
//
// Names are escaped here, not at join time: a port name containing a quote is real on
// hardware with free labels, and must not break an entire site query.
func aggregationPorts(d model.Device) []string {
	var ports = portIntents(d)
	var out []string
	var name string
	for _, name = range sortedPortNames(ports) {
		var p = ports[name]
		if p.Mode == "trunk" && string(p.Tagged) == `"all"` {
			out = append(out, escapeMatcher(name))
		}
	}
	return out
}

// --- One family, one device: what the device page shows ---

// deviceSeriesQuery composes ONE series family query for ONE device.
//
// This is the device page query, and it carries NO interface filter: concerned families
// are device-level quantities (temperature, load, memory, uptime) or counters where we
// want ALL interfaces. Per-port split happens on arrival, on the `port` label.
//
// A GAUGE reads as-is, a COUNTER goes through `rate()`. Wrong form is the mistake that
// would show "2 billion" on a megabits-per-second chart.
//
// COUNTERS ARE SUMMED, gauges are not, and the difference is not cosmetic. An interface
// counter exists once PER PORT: without sum, a 1930 device page got fifty-two zero error
// series — not information but a wall. What an operator reads on a device page is "is
// this device dropping frames?", and that is a sum. Per-port detail belongs on another
// screen.
//
// Gauges keep their series: `temperature_celsius` is labeled by `sensor`, and summing two
// sensors would show 143 °C on a healthy board.
//
// Returns empty string on malformed ID or family outside vocabulary.
func deviceSeriesQuery(deviceID, metric string) string {
	var spec, ok = queryable[metric]
	if !ok || !reDeviceID.MatchString(deviceID) {
		return ""
	}
	var selector = metric + `{device="` + deviceID + `"}`
	if spec.kind == kindGauge {
		return selector
	}
	return "sum(rate(" + selector + "[" + promDuration(rateWindow) + "]))" + spec.scale
}

// --- Reading `devices.ports`, in one place ---

// portIntent is what this file reads from an inventory port.
//
// A named type rather than an anonymous struct copied twice: the same decode lived in two
// places in this package (portRates, portSummary), and this path decides what counts in a
// site chart. Three copies of a decode is three ways to get "full trunk" wrong.
type portIntent struct {
	Mode   string          `json:"mode"`
	Link   string          `json:"link"`
	Tagged json.RawMessage `json:"tagged"`
	RX     float64         `json:"rx_mbps"`
	TX     float64         `json:"tx_mbps"`
}

// portIntents decodes `devices.ports`. Returns an EMPTY never-nil map on unreadable
// inventory: indexing is then safe, and the result — no ports to count — is the right
// fallback.
func portIntents(d model.Device) map[string]portIntent {
	var out = map[string]portIntent{}
	// Error ignored deliberately: missing `ports` is a device no config describes yet,
	// not a read anomaly.
	_ = json.Unmarshal(d.Ports, &out)
	return out
}

// sortedPortNames returns sorted port names.
//
// Sorted because these names compose a QUERY: Go map iteration order varies each run, and
// a query changing shape every call makes store logs unreadable — and the composing
// function untestable.
func sortedPortNames(m map[string]portIntent) []string {
	var out = make([]string, 0, len(m))
	var k string
	for k = range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// promDuration writes a duration as PromQL expects: whole seconds.
//
// `time.Duration.String()` yields "2m0s", which PromQL rejects. The rejection would be
// visible, but its message would talk syntax to someone looking for an empty chart.
func promDuration(d time.Duration) string {
	var sec = int64(d.Seconds())
	if sec < 1 {
		sec = 1
	}
	return strconv.FormatInt(sec, 10) + "s"
}
