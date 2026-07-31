package api

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"meshcraft/internal/metricbuf"
	"meshcraft/internal/model"
)

// DEVICE TELEMETRY: GET /api/devices/{id}/telemetry.
//
// # WHAT THIS ROUTE FIXES
//
// Eleven series families reached the series store, and only TWO reached a screen — link
// state and negotiated rate, via inventory (`devices.ports[].link`). The other nine had
// no path: no route, no screen. From the operator's view, an interface without telemetry
// is indistinguishable from broken collection, and that is exactly what was reported.
//
// Two later found their place — octet counters, derived to per-port rate (cf.
// portrates.go). This route serves the remaining SEVEN, plus rate history for charts.
//
// # WHY PER DEVICE, NOT A GENERIC SERIES ROUTE
//
// A route relaying a screen-written query would leak across sites: the `device` label is
// the only thing that scopes a series to a site, and the store knows nothing about sites.
// Scoping is therefore BUILT here — one device, authorized on ITS site, and a closed
// vocabulary of families (cf. series.go). The caller chooses window and step, both bounded.
//
// # WHAT AN EMPTY RESPONSE MEANS
//
// A family with no series is not an error, and silence would be the worst display: « no
// chart » reads as « collection down ». Three causes are distinguished, and the screen can
// name them:
//
//	`available: false`         — no series store on this instance. Nothing will ever
//	                             display, and that is not a device fault.
//	a missing family           — the store responded; this platform does not publish
//	                             that quantity. The device page already carries the
//	                             REASON from the connector (`monitoring_unavailable`),
//	                             and that is what to show — not a dash.
//	empty `series` entirely    — the device is not collected (no collector, silent agent,
//	                             tunnel down).

// telemetryPoint is a timestamped value as the screen consumes it.
type telemetryPoint struct {
	TS    time.Time `json:"ts"`
	Value float64   `json:"value"`
}

// telemetrySeries is a series served to the screen.
type telemetrySeries struct {
	// Metric is the family name as internal/metricbuf fixes it. The screen uses it as a
	// KEY, never as a label: labels live in the UI, in French, and renaming a metric
	// must not change a title.
	Metric string `json:"metric"`
	// Unit is the unit of what is returned, derivative included (« Mbit/s », « °C »).
	Unit string `json:"unit"`
	// Sensor / Port: what distinguishes two series of the same family. Empty when the
	// family carries neither.
	Sensor string `json:"sensor,omitempty"`
	Port   string `json:"port,omitempty"`
	// Last is the last known value, ABSENT when the series has no points. A pointer, not
	// zero: « no measurement » and « zero » do not display the same — the difference
	// between a silent port and an idle port.
	Last   *float64         `json:"last,omitempty"`
	Points []telemetryPoint `json:"points"`
}

// telemetryReply is what the route returns.
type telemetryReply struct {
	// Available: is there a series store on this instance.
	Available bool   `json:"available"`
	Why       string `json:"why,omitempty"`
	// WindowSeconds / StepSeconds: the window ACTUALLY served, after bounding. Returned
	// because the screen writes axis labels with them — guessing would lie by a factor of
	// two when a bound applies.
	WindowSeconds int               `json:"window_seconds"`
	StepSeconds   int               `json:"step_seconds"`
	Device        []telemetrySeries `json:"device"`
	Ports         []telemetrySeries `json:"ports"`
}

// deviceMetrics are DEVICE-level families on the device page, in display order. Fixed
// order, not a map: this is a list for a screen, and Go map iteration order would dance
// on every reload.
var deviceMetrics = []string{
	metricbuf.MetricTemperature,
	metricbuf.MetricCPULoad,
	metricbuf.MetricMemoryFree,
	metricbuf.MetricMemoryTotal,
	metricbuf.MetricUptime,
	metricbuf.MetricIfInErrors,
	metricbuf.MetricIfOutErrors,
}

// portMetrics are PER-PORT families on the device page: history of both traffic directions
// for port charts.
var portMetrics = []string{
	metricbuf.MetricIfInOctets,
	metricbuf.MetricIfOutOctets,
}

// Bounds on the requested window.
//
// They exist for the reason written at the top of series.go: a one-year range at one-
// second step would load tens of millions of points to draw six hundred pixels. The point
// cap is the real bound — it holds whatever (window, step) pair is requested.
const (
	defaultTelemetryWindow = 24 * time.Hour
	maxTelemetryWindow     = 30 * 24 * time.Hour
	minTelemetryStep       = 15 * time.Second
	maxTelemetryPoints     = 600
)

// handleDeviceTelemetry serves a device's series.
//
// `viewer` level: reading a measurement changes nothing and reveals no secret — same
// reasoning as listing tags.
func (s *Server) handleDeviceTelemetry(w http.ResponseWriter, r *http.Request) {
	var d, ok = s.deviceForWrite(w, r, "viewer")
	if !ok {
		return
	}
	var window, step = telemetryWindow(r)
	var out = telemetryReply{
		Available:     s.TSDB != nil,
		WindowSeconds: int(window.Seconds()),
		StepSeconds:   int(step.Seconds()),
		Device:        []telemetrySeries{},
		Ports:         []telemetrySeries{},
	}
	if s.TSDB == nil {
		// An instance without a series store WORKS, and that is a documented choice
		// (docker-compose: « empty = no metrics store »). The screen must say why there
		// is nothing rather than show an empty frame that looks like a fault.
		out.Why = "This instance has no time-series store " +
			"(MESHCRAFT_METRICS_URL empty): collection runs, but nothing is " +
			"retained, so no chart can be displayed."
		respond(w, http.StatusOK, out)
		return
	}

	var now = time.Now().Truncate(time.Second)
	var from = now.Add(-window)

	var metric string
	for _, metric = range deviceMetrics {
		out.Device = append(out.Device,
			s.telemetryOf(r.Context(), d, metric, from, now, step, false)...)
	}
	for _, metric = range portMetrics {
		out.Ports = append(out.Ports,
			s.telemetryOf(r.Context(), d, metric, from, now, step, true)...)
	}
	respond(w, http.StatusOK, out)
}

// telemetryOf queries ONE family and translates its series.
//
// Failure is absorbed — the family is simply absent from the response. Returning 502
// because one of seven families failed would hide the other six, and the whole device
// page with them.
func (s *Server) telemetryOf(ctx context.Context, d model.Device, metric string,
	from, to time.Time, step time.Duration, perPort bool) []telemetrySeries {
	var query string
	if perPort {
		// Same query as inventory refresh: filtered to Ethernet interfaces, excluding
		// VLAN and loopback interfaces.
		query = portRateQuery([]string{d.ID}, metric)
	} else {
		query = deviceSeriesQuery(d.ID, metric)
	}
	if query == "" {
		return nil
	}
	var series, err = s.TSDB.QueryRange(ctx, query, from, to, step)
	if err != nil {
		return nil
	}
	var facade = facadeOf(d)
	var out []telemetrySeries
	var i int
	for i = range series {
		var one = telemetrySeries{
			Metric: metric,
			Unit:   metricUnit(metric),
			Sensor: series[i].Labels["sensor"],
			Points: []telemetryPoint{},
		}
		if perPort {
			// FACEPLATE name, not store label: the screen finds the port by this name,
			// and an unresolvable label is dropped rather than returned — telemetry does
			// not create ports.
			one.Port = facade.resolve(series[i].Labels["port"])
			if one.Port == "" {
				continue
			}
		}
		var j int
		for j = range series[i].Points {
			one.Points = append(one.Points, telemetryPoint{
				TS:    series[i].Points[j].At,
				Value: round3(series[i].Points[j].Value),
			})
		}
		var last, hasLast = series[i].Last()
		if hasLast {
			var v = round3(last.Value)
			one.Last = &v
		}
		out = append(out, one)
	}
	return sortedSeries(out)
}

// sortedSeries orders series of one family in STABLE order.
//
// Two series of the same family differ by sensor or port, and store return order is not
// guaranteed. Without this sort, device page tiles would move on every refresh — every ten
// seconds on a screen left open.
func sortedSeries(in []telemetrySeries) []telemetrySeries {
	var i, j int
	for i = 1; i < len(in); i++ {
		for j = i; j > 0 && seriesLess(in[j], in[j-1]); j-- {
			in[j], in[j-1] = in[j-1], in[j]
		}
	}
	return in
}

func seriesLess(a, b telemetrySeries) bool {
	if a.Sensor != b.Sensor {
		return a.Sensor < b.Sensor
	}
	return a.Port < b.Port
}

// telemetryWindow reads and bounds the requested window.
//
// The POINT CAP is the bound that actually holds: it applies whatever pair is requested,
// and prevents a one-month range at fifteen-second step from crossing the
// controller for a six-hundred-pixel chart. Step is then WIDENED rather than shrinking the
// window — the operator asked for a month, they get it, less finely.
func telemetryWindow(r *http.Request) (time.Duration, time.Duration) {
	var window = defaultTelemetryWindow
	var asked, err = strconv.Atoi(r.URL.Query().Get("minutes"))
	if err == nil && asked > 0 {
		window = time.Duration(asked) * time.Minute
	}
	if window > maxTelemetryWindow {
		window = maxTelemetryWindow
	}
	var step = window / maxTelemetryPoints
	if step < minTelemetryStep {
		step = minTelemetryStep
	}
	// Rounded to seconds: step goes as whole seconds in the query (cf. tsdb.QueryRange),
	// and a fractional step would be silently truncated there.
	step = step.Round(time.Second)
	return window, step
}
