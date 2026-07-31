package api

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"time"

	"meshcraft/internal/metricbuf"
	"meshcraft/internal/model"
)

// Site aggregate throughput over a sliding window.
//
// MEASURED when the series store responds, RECONSTRUCTED otherwise — and the
// `simulated` field says which, without indirection. A plausible curve presented as
// measurement would be worse than no curve at all.
//
// # WHAT CHANGED, AND THE COMMENT THAT PRESCRIBED IT
//
// This file used to say: « replace with a VictoriaMetrics read once SNMP collection
// feeds `meshcraft_if_in_octets_total`: throughput will then be computed from counter
// deltas, and `simulated` becomes false ». The condition is met — collection runs and
// was verified on hardware — and that is what `measuredTraffic` does now.
//
// The reconstructed fallback REMAINS, and is not legacy: `MESHCRAFT_METRICS_URL` can
// be empty (compose allows it explicitly, « meshcraft works without metrics and says
// so »), a new instance has no history yet, and a temporarily unreachable store must
// not blank a screen. In those three cases the curve falls back to inventory-derived
// values, with its flag.
//
// # THE TWO MEANINGS OF `simulated`
//
//	false — each point is a sum of `rate()` over interface counters.
//	true  — the curve is derived from instant inventory rates, with interface variation.
//	        It has the right SHAPE, not the right value.

type trafficPoint struct {
	TS time.Time `json:"ts"`
	RX float64   `json:"rx_mbps"`
	TX float64   `json:"tx_mbps"`
}

type trafficSeries struct {
	Hours     int            `json:"hours"`
	Simulated bool           `json:"simulated"`
	Points    []trafficPoint `json:"points"`
}

// portRates reads a device's instant rates as inventory carries them. An unreachable
// device does not count: adding traffic from a powered-off switch would inflate the
// curve with throughput that no longer exists.
func portRates(d model.Device) (rx, tx float64) {
	if d.Status == "off" {
		return 0, 0
	}
	var ports map[string]struct {
		Mode   string          `json:"mode"`
		Link   string          `json:"link"`
		Tagged json.RawMessage `json:"tagged"`
		RX     float64         `json:"rx_mbps"`
		TX     float64         `json:"tx_mbps"`
	}
	if json.Unmarshal(d.Ports, &ports) != nil {
		return 0, 0
	}
	var p = ports
	var name string
	for name = range p {
		var e = p[name]
		// LINK IS ENOUGH; intent is not required. It used to be required, and that was
		// the same defect as port tiles: an ACTIVE port meshcraft does not configure still
		// carries traffic, and excluding it zeroed the fallback curve for every freshly
		// adopted device — whose ports have no intent yet. « Active » is observation,
		// « managed » is intent.
		if e.Link == "" {
			continue
		}
		// An « all VLANs » trunk is an aggregation link: its traffic is already counted on
		// access ports that feed it. Counting it twice would double the site curve.
		if e.Mode == "trunk" && string(e.Tagged) == `"all"` {
			continue
		}
		rx += e.RX
		tx += e.TX
	}
	return rx, tx
}

func (s *Server) handleSiteTraffic(w http.ResponseWriter, r *http.Request) {
	var siteID, ok = s.requireSite(w, r, "viewer")
	if !ok {
		return
	}
	var hours, _ = strconv.Atoi(r.URL.Query().Get("hours"))
	if hours <= 0 || hours > 720 {
		hours = 24
	}
	var points, _ = strconv.Atoi(r.URL.Query().Get("points"))
	if points <= 1 || points > 500 {
		points = 96
	}

	var devices, err = s.Store.ListDevices(r.Context(), siteID)
	if err != nil {
		fail(w, err)
		return
	}
	var now = time.Now()
	var step = time.Duration(hours) * time.Hour / time.Duration(points-1)

	// MEASUREMENT FIRST. Failure is not returned to the caller: it falls back to
	// reconstruction, which was behavior before this set. Returning 502 on a temporarily
	// unreachable store would blank a screen that worked fine without it.
	var measured, err2 = s.measuredTraffic(r.Context(), devices, now, step, points)
	if err2 == nil && len(measured) > 0 {
		respond(w, http.StatusOK, trafficSeries{
			Hours: hours, Simulated: false, Points: measured})
		return
	}

	var baseRX, baseTX float64
	var i int
	for i = range devices {
		var rx, tx = portRates(devices[i])
		baseRX += rx
		baseTX += tx
	}

	// THE FALLBACK CURVE IS FLAT, and that is a fix.
	//
	// It used to carry generated variation (`jitAt`) around the current value, so « the
	// historical curve and live display tell the same story ». That was makeup for missing
	// data: ±18% oscillation no octet caused, on which an operator bases capacity
	// decisions.
	//
	// What remains is the only fact available: instant rate inventory carries, held
	// constant over the window, with `simulated: true` to say it is not history. A
	// straight line claims nothing; an invented curve claims everything.
	var out = trafficSeries{Hours: hours, Simulated: true, Points: []trafficPoint{}}
	for i = 0; i < points; i++ {
		var at = now.Add(-time.Duration(points-1-i) * step)
		out.Points = append(out.Points, trafficPoint{TS: at, RX: baseRX, TX: baseTX})
	}
	respond(w, http.StatusOK, out)
}

// measuredTraffic returns the MEASURED curve, or an empty list when there is nothing
// to measure.
//
// # TIMESTAMPS COME FROM THE STORE, NEVER FROM LOCAL MATH
//
// That fixes a defect in the first version, and it did not look like a bug: the curve
// displayed, with one point in four. The function built expected instants (`now - k ×
// step`) and fetched each value at that key. VictoriaMetrics aligns its own grid to
// multiples of step from `start`: the two grids coincide only by rounding luck, and
// surviving point count CHANGED from request to request — four, then two, then one.
//
// A gapped curve does not read as a code error: it reads as intermittent collection,
// sending you looking for a network fault.
//
// Returned points are therefore what the store served, in chronological order, and
// requested count only drives step size.
//
// # BOTH DIRECTIONS ARE JOINED BY TIMESTAMP, NOT BY INDEX
//
// Same reason, other consequence: the store returns a point only where it has data, so
// the two series may differ in length — a port emitting nothing has no egress series.
// Pairing by index would shift the transmit curve in time, which reads as a network
// incident.
//
// A step SMALLER than the rate window (cf. rateWindow) is legitimate: points overlap,
// smoothing the curve. A much larger step SUBSAMPLES — a 24 h window in 96 points
// takes a two-minute average every fifteen minutes, and a burst between points does not
// appear. That is the nature of a screen curve, and it is said here so nobody hunts a
// missing octet.
func (s *Server) measuredTraffic(ctx context.Context, devices []model.Device,
	now time.Time, step time.Duration, points int) ([]trafficPoint, error) {
	if s.TSDB == nil {
		return nil, nil
	}
	var from = now.Add(-time.Duration(points-1) * step)
	var rx, err = s.trafficSeries(ctx, devices, metricbuf.MetricIfInOctets, from, now, step)
	if err != nil {
		return nil, err
	}
	var tx map[int64]float64
	tx, err = s.trafficSeries(ctx, devices, metricbuf.MetricIfOutOctets, from, now, step)
	if err != nil {
		return nil, err
	}
	// UNION of both grids, sorted. An instant where only one direction was measured
	// yields a point with the other direction at zero — which is exact: the store did
	// respond on that window, and has nothing for that direction.
	var at = make([]int64, 0, len(rx)+len(tx))
	var seen = map[int64]bool{}
	var maps = []map[int64]float64{rx, tx}
	var i int
	for i = range maps {
		var ts int64
		for ts = range maps[i] {
			if !seen[ts] {
				seen[ts] = true
				at = append(at, ts)
			}
		}
	}
	sort.Slice(at, func(a, b int) bool { return at[a] < at[b] })

	var out []trafficPoint
	for i = range at {
		out = append(out, trafficPoint{
			TS: time.Unix(at[i], 0).UTC(),
			RX: round3(rx[at[i]]),
			TX: round3(tx[at[i]]),
		})
	}
	return out, nil
}

// trafficSeries queries the store and returns site sum by timestamp.
//
// Indexed by UNIX SECOND rather than time.Time: that is the key the store aligns to
// the requested step, and comparing time.Time values with different zones and
// monotonicities is a reliable way never to find a point.
func (s *Server) trafficSeries(ctx context.Context, devices []model.Device, metric string,
	from, to time.Time, step time.Duration) (map[int64]float64, error) {
	var query = siteRateQuery(devices, metric)
	if query == "" {
		return nil, nil
	}
	var series, err = s.TSDB.QueryRange(ctx, query, from, to, step)
	if err != nil {
		return nil, err
	}
	var out = map[int64]float64{}
	var i int
	for i = range series {
		var j int
		for j = range series[i].Points {
			var p = series[i].Points[j]
			out[p.At.Unix()] += p.Value
		}
	}
	return out, nil
}
