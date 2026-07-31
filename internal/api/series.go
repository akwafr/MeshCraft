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
// screen would be exactly the lapse this repo already regretted elsewhere (« JSON type
// tags ready and inert »). When a telemetry screen exists, its scoping will be defined
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
	// goes to the screen: it prevents showing « 0.002 byte » for 0.002 Mbit/s.
	unit string
	// scale is the expression suffix applied AFTER any derivative. Empty for most
	// families; octets become megabits per second.
	scale string
}

// queryable is the CLOSED set of series a query may name.
//
// Closed, not « everything the store contains »: these names go into a query, and the
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
	// Device gauges.
	metricbuf.MetricTemperature: {kindGauge, "°C", ""},
	metricbuf.MetricCPULoad:     {kindGauge, "%", ""},
	metricbuf.MetricMemoryFree:  {kindGauge, "B", ""},
	metricbuf.MetricMemoryTotal: {kindGauge, "B", ""},
	metricbuf.MetricUptime:      {kindGauge, "s", ""},
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
// (« SFP1 », « sfp-sfpplus2 », « 49 »). A quote or backslash is enough to break the whole
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
	return "rate(" + metric + `{device=~"` + strings.Join(kept, "|") +
		`",iftype="` + strconv.Itoa(snmppoll.IfTypeEthernet) + `"}[` +
		promDuration(rateWindow) + "])" + spec.scale
}

// --- Site throughput: the curve, and double-counting trap ---

// siteRateQuery composes summed site rates in Mbit/s.
//
// # DOUBLE COUNTING IS THE TRAP OF THIS SUM
//
// Access port traffic exits via the switch uplink: summing all ports on a site therefore
// counts each octet TWICE, and the chart shows double real traffic. The reconstructed
// version already applied this rule on inventory side (« an all-VLAN trunk is aggregation:
// its traffic is already counted on access ports feeding it »), and it must survive the
// move to series — otherwise replacing a simulated curve with measurement would DOUBLE
// the display, which reads as an incident.
//
// Exclusion uses a per-DEVICE negative matcher, and cannot be global: « GE49 » is an uplink
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

// aggregationPorts returns ports whose traffic is ALREADY counted elsewhere: « all VLAN »
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
// would show « 2 billion » on a megabits-per-second chart.
//
// COUNTERS ARE SUMMED, gauges are not, and the difference is not cosmetic. An interface
// counter exists once PER PORT: without sum, a 1930 device page got fifty-two zero error
// series — not information but a wall. What an operator reads on a device page is « is
// this device dropping frames? », and that is a sum. Per-port detail belongs on another
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
// site chart. Three copies of a decode is three ways to get « full trunk » wrong.
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
// `time.Duration.String()` yields « 2m0s », which PromQL rejects. The rejection would be
// visible, but its message would talk syntax to someone looking for an empty chart.
func promDuration(d time.Duration) string {
	var sec = int64(d.Seconds())
	if sec < 1 {
		sec = 1
	}
	return strconv.FormatInt(sec, 10) + "s"
}
