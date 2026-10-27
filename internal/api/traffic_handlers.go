package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"meshcraft/internal/metricbuf"
	"meshcraft/internal/model"
)

// Site aggregate throughput over a sliding window.
//
// MEASURED when the series store responds; otherwise an empty time grid
// (`simulated: true`) so long windows do not invent a flat history.
// Missing slots inside a measured window leave rx/tx null (empty stroke).
//
//	simulated false — each point is a sum of rate() over interface counters.
//	simulated true  — no history available; points carry timestamps only.

type trafficPoint struct {
	TS time.Time `json:"ts"`
	// Nil means no sample at this slot — the UI leaves that stretch empty
	// instead of connecting across the gap or drawing a false zero.
	RX *float64 `json:"rx_mbps"`
	TX *float64 `json:"tx_mbps"`
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
		// adopted device — whose ports have no intent yet. "Active" is observation,
		// "managed" is intent.
		if e.Link == "" {
			continue
		}
		// An "all VLANs" trunk is an aggregation link: its traffic is already counted on
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
	// Step follows the displayed window (1 min / 5 min / 10 min / 15 min).
	// The obsolete `points` query param is ignored: a fixed budget coarsened
	// long ranges and made the same screen look sparse after a chip click.
	var window = time.Duration(hours) * time.Hour
	var step = chartStep(window)
	var points = chartPointCount(window, step)

	var devices, err = s.Store.ListDevices(r.Context(), siteID)
	if err != nil {
		fail(w, err)
		return
	}
	var now = time.Now()

	// MEASUREMENT FIRST. Failure is not returned to the caller: it falls back to
	// an empty grid (not a reconstituted curve). Inventing a flat line across 7 d
	// or 30 d where nothing was measured looked like history.
	var measured, err2 = s.measuredTraffic(r.Context(), devices, now, step, points)
	if err2 == nil && len(measured) > 0 {
		respond(w, http.StatusOK, trafficSeries{
			Hours: hours, Simulated: false, Points: measured})
		return
	}

	// Empty window aligned on the same grid: the chart shows axes with no stroke.
	var out = trafficSeries{Hours: hours, Simulated: true, Points: []trafficPoint{}}
	var i int
	for i = 0; i < points; i++ {
		var at = now.Add(-time.Duration(points-1-i) * step)
		out.Points = append(out.Points, trafficPoint{TS: at})
	}
	respond(w, http.StatusOK, out)
}

// measuredTraffic returns one point per chart slot for [now-(n-1)×step, now].
//
// Slots with no store sample leave RX/TX nil so the UI draws an empty stretch
// rather than bridging the gap or filling zero Mbps.
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
	if len(rx) == 0 && len(tx) == 0 {
		return nil, nil
	}

	var half = int64(step / time.Second / 2)
	if half < 1 {
		half = 1
	}
	var out = make([]trafficPoint, 0, points)
	var i int
	for i = 0; i < points; i++ {
		var at = from.Add(time.Duration(i) * step).UTC()
		var slot = trafficPoint{TS: at}
		var rv, rok = nearestSample(rx, at.Unix(), half)
		if rok {
			var v = round3(rv)
			slot.RX = &v
		}
		var tv, tok = nearestSample(tx, at.Unix(), half)
		if tok {
			var v = round3(tv)
			slot.TX = &v
		}
		out = append(out, slot)
	}
	return out, nil
}

// nearestSample finds a store sample within ±halfSec of the chart slot.
func nearestSample(m map[int64]float64, want, halfSec int64) (float64, bool) {
	var v, ok = m[want]
	if ok {
		return v, true
	}
	var bestTs int64
	var bestDist = halfSec + 1
	var ts int64
	for ts = range m {
		var d = ts - want
		if d < 0 {
			d = -d
		}
		if d <= halfSec && d < bestDist {
			bestDist = d
			bestTs = ts
		}
	}
	if bestDist > halfSec {
		return 0, false
	}
	return m[bestTs], true
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
