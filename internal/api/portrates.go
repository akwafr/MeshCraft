package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"math"
	"time"

	"meshcraft/internal/metricbuf"
	"meshcraft/internal/model"
)

// PER-PORT THROUGHPUT, computed from counters and written to inventory.
//
// # WHAT THIS FILE FIXES
//
// Three screens already read `rx_mbps` / `tx_mbps` on each port: Monitoring port top,
// device page sparklines, and network map. A fourth derived site traffic curves from
// them. And NO code ever WROTE those two fields — they arrived only via config import.
//
// On a discovered fleet: `rx = tx = 0` everywhere, empty widgets and a flat curve, on an
// instance where eleven series families still arrived every second. The operator saw "no
// telemetry"; the store was full.
//
// `applyPortTelemetry` (proxymetrics_handlers.go) already writes `link` on this path, and
// its comment said where its job stopped: "Two metrics only: link and negotiated speed.
// Those are what the faceplate can render — an octet counter has no tile." That was true
// of a COUNTER. A RATE has its tile: these two fields. What was missing was the
// derivative, and it cannot be computed on batch arrival — you need two samples and time
// between them, which the series store knows and a handler does not.
//
// # WHY A PERIODIC LOOP, NOT READ-TIME COMPUTATION
//
// The inventory schema always planned this — `internal/siteconfig` documents these four
// fields as "last observed state". Consequences are concrete:
//
//   - FIVE readers are served by one write. Computing at read time would force every route
//     returning a device to query the series store, and site config export — which has no
//     reason to know the store — would have nothing;
//   - cost is BOUNDED and does not depend on open screens. One query per pass, however
//     many browsers watch the fleet.
//
// # THE TRAP OF STALE DISPLAYED RATE
//
// A port whose series DISAPPEARS — collection stopped, collector detached, device removed
// from polling — would keep its last rate in inventory forever. That is the worst display:
// frozen load nothing contradicts, on a device nobody polls anymore.
//
// Each pass therefore ZEROES faceplate ports for which the store returned nothing. Zero is
// not "no measurement" — inventory cannot express ignorance on this field — but between
// a curve that drops and a permanent phantom load, the first reads correctly.

// portRatesEvery is the refresh cadence.
//
// THIRTY SECONDS, for a two-minute computation window: rate is smoothed over two minutes
// anyway (cf. rateWindow), so querying more often does not produce a finer curve — only
// the same value more often. Screens refresh themselves at a similar pace.
const portRatesEvery = 30 * time.Second

// RunPortRates runs refresh until context cancellation.
//
// The first pass is IMMEDIATE, not delayed one period: at controller startup inventory
// still carries rates from before shutdown, and waiting thirty seconds would show them —
// traffic measured while nothing is being measured.
func (s *Server) RunPortRates(ctx context.Context) {
	if s.TSDB == nil {
		// No store configured: nothing to read, and saying so ONCE beats an empty loop
		// every thirty seconds.
		slog.Info("per-port rates: no metrics store, refresh inactive")
		return
	}
	var t = time.NewTicker(portRatesEvery)
	defer t.Stop()
	for {
		s.refreshPortRates(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// refreshPortRates runs ONE pass: one query per metric, then writes.
//
// Failures are logged and do not stop the loop: a temporarily unreachable store must not
// stop rates until the next controller restart after a ten-second incident.
func (s *Server) refreshPortRates(ctx context.Context) {
	var devices, err = s.allDevices(ctx)
	if err != nil {
		slog.Warn("per-port rates: unreadable inventory", "err", err)
		return
	}
	if len(devices) == 0 {
		return
	}
	var ids = make([]string, 0, len(devices))
	var i int
	for i = range devices {
		ids = append(ids, devices[i].ID)
	}

	// Both directions are queried SEPARATELY rather than one query: they are distinct
	// metrics, and merging in PromQL (`or`) would return series whose name you must
	// re-parse to know the direction.
	var rx = s.portRates(ctx, ids, metricbuf.MetricIfInOctets)
	var tx = s.portRates(ctx, ids, metricbuf.MetricIfOutOctets)
	if rx == nil && tx == nil {
		return
	}

	for i = range devices {
		var patch = portRatePatch(devices[i], rx[devices[i].ID], tx[devices[i].ID])
		if len(patch) == 0 {
			continue
		}
		var raw, mErr = json.Marshal(patch)
		if mErr != nil {
			continue
		}
		if err = s.Store.UpdatePortTelemetry(ctx, devices[i].ID, raw); err != nil {
			slog.Warn("per-port rates: write refused", "device", devices[i].ID,
				"err", err)
		}
	}
}

// portRates queries the store and returns, per device then per FACEPLATE port name,
// rate in Mbit/s.
//
// Returns nil on failure — distinct from an empty map, which means "the store responded,
// and there is no series". They lead to different writes: empty map zeroes rates;
// failure touches nothing.
func (s *Server) portRates(ctx context.Context, ids []string,
	metric string) map[string]map[string]float64 {
	var query = portRateQuery(ids, metric)
	if query == "" {
		return nil
	}
	var series, err = s.TSDB.QueryInstant(ctx, query)
	if err != nil {
		slog.Warn("per-port rates: series read refused", "metric", metric,
			"err", err)
		return nil
	}
	var out = map[string]map[string]float64{}
	var i int
	for i = range series {
		var labels = series[i].Labels
		var deviceID = labels["device"]
		var port = labels["port"]
		if deviceID == "" || port == "" {
			continue
		}
		var p, ok = series[i].Last()
		if !ok {
			continue
		}
		if out[deviceID] == nil {
			out[deviceID] = map[string]float64{}
		}
		// The `port` label carries interface text as the device serves it; the faceplate
		// has its own names. Resolution happens in the caller, which has the device —
		// here we keep the store key.
		out[deviceID][port] = p.Value
	}
	return out
}

// portRatePatch builds ONE device's inventory patch.
//
// PURE function, and necessary: it decides which rate displays on which port, and
// mapping wrong produces a perfectly credible false chart.
//
// Name resolution uses [facadeOf], same as `applyPortTelemetry`: that is how a `port="49"`
// from a 1930 lands on "GE49", and `port="SFP2"` from SwOS on "SFP2". An unresolvable
// label is DROPPED, never invented — telemetry does not create ports.
//
// ALL faceplate ports are in the patch, not only those with a series — the guard against
// phantom rate described at the top of this file.
func portRatePatch(d model.Device, rx, tx map[string]float64) map[string]map[string]any {
	var f = facadeOf(d)
	var known = f.names()
	if len(known) == 0 {
		return nil
	}
	// Store values, mapped to faceplate names.
	var byPort = map[string][2]float64{}
	var fold = func(src map[string]float64, idx int) {
		var raw string
		for raw = range src {
			var name = f.resolve(raw)
			if name == "" {
				continue
			}
			var pair = byPort[name]
			pair[idx] = src[raw]
			byPort[name] = pair
		}
	}
	fold(rx, 0)
	fold(tx, 1)

	var current = portIntents(d)
	var out = map[string]map[string]any{}
	var name string
	for _, name = range known {
		var pair = byPort[name]
		var newRX, newTX = round3(pair[0]), round3(pair[1])
		// Skip when unchanged: `UpdatePortTelemetry` rewrites the device `ports` column,
		// and doing that thirty times per minute on a static fleet is free DB write plus
		// screen wakeups.
		var now = current[name]
		if round3(now.RX) == newRX && round3(now.TX) == newTX {
			continue
		}
		out[name] = map[string]any{"rx_mbps": newRX, "tx_mbps": newTX}
	}
	return out
}

// round3 rounds to thousandths of Mbit/s, i.e. kilobit precision.
//
// Rounded because these values are COMPARED pass to pass to decide whether to write: a
// rate oscillating on the fifteenth decimal would trigger write and screen refresh every
// pass for an identical display.
func round3(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	if v < 0 {
		// Negative rate does not exist. `rate()` does not produce them, but a misread
		// counter reset upstream would, and displaying it sends you looking for cabling
		// faults.
		return 0
	}
	return math.Round(v*1000) / 1000
}

// roundSigned3 rounds to thousandths and KEEPS the sign.
//
// round3's negative clamp is a statement about RATES — a negative rate does not
// exist, so showing one would send somebody hunting cabling faults. It is
// exactly wrong for the gauge families where negative is the ordinary case:
// every healthy receive level is negative dBm, and clamping it displays a dead
// fibre as 0 dBm — a strong signal. The two rounders therefore split on what
// the number IS, and telemetryOf picks by the family's kind.
func roundSigned3(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return math.Round(v*1000) / 1000
}

// allDevices returns all devices in the instance.
//
// Goes through SITES because inventory is organized that way and no product read ever
// needed a whole fleet: `ListSites(…, all=true)` is the same door existing background
// tasks use, and using it here avoids a cross-cutting query nothing else would use.
func (s *Server) allDevices(ctx context.Context) ([]model.Device, error) {
	var sites, err = s.Store.ListSites(ctx, "", true)
	if err != nil {
		return nil, err
	}
	var out []model.Device
	var i int
	for i = range sites {
		var devices []model.Device
		devices, err = s.Store.ListDevices(ctx, sites[i].ID)
		if err != nil {
			return nil, err
		}
		out = append(out, devices...)
	}
	return out, nil
}
