package api

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"meshcraft/internal/metricbuf"
	"meshcraft/internal/model"
)

// The bandwidth BUCKET: how many bytes each client's port carried while the
// client was there.
//
// # The accounting model, stated before the mechanics
//
// The user-facing promise is per MAC: a bucket that fills while the machine
// is plugged in, stops when its port goes down or the bridge forgets it, and
// RESUMES on the same MAC when it is rediscovered — on any port, any switch
// of the site. The measure is the PORT's octet counters (the only truth a
// switch offers), attributed to the MACs located on that port:
//
//   - a port with one client (the ordinary access port) attributes exactly;
//   - a port with several located MACs (hypervisor trunk, unmanaged switch)
//     attributes the SAME port total to each — the bucket answers "how much
//     did the wire this machine sits on carry", not "how much did this
//     machine itself send", and per-MAC flow accounting would need sFlow or
//     port mirroring, which none of the fleet's platforms offer meshcraft
//     today. The column header on the page says "port volume" for this
//     reason;
//   - UPLINK ports attribute to nobody (clientLocations already excludes
//     them): transit is not residence.
//
// "Stops when the port goes down" needs no special case: a down port's
// counters do not move, so its increase is zero; and the bridge ages the MAC
// out within ~300 s, after which the location itself disappears.
//
// # Why increase() over a watermark window, and what a restart costs
//
// Each pass attributes `increase(counter[now-mark])` — the device's own
// cumulative counters diffed by VictoriaMetrics, which absorbs counter wraps
// and collection gaps. The watermark is in memory: a controller restart loses
// at most one window (60 s) of attribution, which is the right trade against
// persisting a high-frequency cursor. The LEDGER (clients.rx/tx_bytes) is the
// durable accumulator — restarts never zero it.

// clientBucketEvery is the accounting cadence. Sixty seconds: the FDB cadence
// itself, so presence and attribution move together.
const clientBucketEvery = 60 * time.Second

// clientBucketMaxWindow bounds one pass's window. A controller that slept
// (laptop dev instance, suspended VM) must not attribute a day of port
// traffic to whoever happens to be on the port NOW — beyond this, the stale
// window is dropped and accounting restarts from the present.
const clientBucketMaxWindow = 10 * time.Minute

// clientBucketMarks holds the per-site watermark.
type clientBucketMarks struct {
	mu    sync.Mutex
	marks map[string]time.Time
}

// RunClientBuckets runs the accounting until context cancellation. Mounted
// from cmd/controller/main.go beside the optics sampler; exits when no series
// store is configured, for the same reason it does (its only product IS the
// attribution, and there is nothing to attribute from).
func (s *Server) RunClientBuckets(ctx context.Context) {
	if s.TSDB == nil {
		slog.Info("client buckets: no series store, not accounting")
		return
	}
	var marks = &clientBucketMarks{marks: map[string]time.Time{}}
	var t = time.NewTicker(clientBucketEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		var sites, err = s.Store.ListSites(ctx, "", true)
		if err != nil {
			slog.Warn("client buckets: site list unreadable", "err", err.Error())
			continue
		}
		var i int
		for i = range sites {
			if ctx.Err() != nil {
				return
			}
			s.clientBucketPass(ctx, sites[i].ID, marks)
		}
	}
}

// clientBucketPass attributes one window for one site.
func (s *Server) clientBucketPass(ctx context.Context, siteID string,
	marks *clientBucketMarks) {
	var now = time.Now()
	marks.mu.Lock()
	var mark, had = marks.marks[siteID]
	marks.marks[siteID] = now
	marks.mu.Unlock()
	if !had {
		// First sight of this site: set the watermark, attribute nothing. An
		// unbounded first window would attribute VictoriaMetrics' whole
		// retention to whoever is plugged in today.
		return
	}
	var window = now.Sub(mark)
	if window <= 0 {
		return
	}
	if window > clientBucketMaxWindow {
		window = clientBucketMaxWindow
	}

	var sightings, err = s.Store.ListMACSightings(ctx, siteID)
	if err != nil || len(sightings) == 0 {
		return
	}
	// Only FRESH sightings hold the port: a MAC that left keeps its ledger but
	// must not be billed for the next occupant's traffic.
	var fresh = make([]model.MACSighting, 0, len(sightings))
	var i int
	for i = range sightings {
		if now.Sub(sightings[i].LastSeen) < clientFreshWindow {
			fresh = append(fresh, sightings[i])
		}
	}
	if len(fresh) == 0 {
		return
	}
	var devices []model.Device
	if devices, err = s.Store.ListDevices(ctx, siteID); err != nil {
		return
	}
	var neighbors, nErr = s.Store.ListLLDPNeighbors(ctx, siteID)
	var selves, sErr = s.Store.ListLLDPSelf(ctx, siteID)
	var manual, mErr = s.Store.ListLinks(ctx, siteID)
	if nErr != nil || sErr != nil || mErr != nil {
		return
	}
	var view = buildLLDPView(neighbors, selves, devices)
	var uplinks = uplinkPorts(view.Links, manual)
	var locations = clientLocations(fresh, uplinks)
	if len(locations) == 0 {
		return
	}

	// Two composed queries for the whole site, resolved onto facade names.
	var in = s.portIncrease(ctx, devices, metricbuf.MetricIfInOctets, window)
	var out = s.portIncrease(ctx, devices, metricbuf.MetricIfOutOctets, window)
	if len(in) == 0 && len(out) == 0 {
		return
	}

	var macs []string
	var rx, tx []int64
	var mac string
	for mac = range locations {
		var key = clientPortKey(locations[mac].DeviceID, locations[mac].Port)
		// rx = toward the client = the port's EGRESS; tx = from it = ingress.
		var toward = int64(out[key])
		var from = int64(in[key])
		if toward == 0 && from == 0 {
			continue
		}
		macs = append(macs, mac)
		rx = append(rx, toward)
		tx = append(tx, from)
	}
	if len(macs) == 0 {
		return
	}
	if err = s.Store.AddClientTraffic(ctx, siteID, macs, rx, tx, now); err != nil {
		slog.Warn("client buckets: attribution not recorded", "site", siteID,
			"err", err.Error())
	}
}

// portIncrease reads one counter's increase over the window for every port of
// the given devices, keyed by (device, facade port).
func (s *Server) portIncrease(ctx context.Context, devices []model.Device,
	metric string, window time.Duration) map[string]float64 {
	var out = map[string]float64{}
	if s.TSDB == nil || len(devices) == 0 {
		return out
	}
	var ids = make([]string, 0, len(devices))
	var byID = map[string]model.Device{}
	var i int
	for i = range devices {
		ids = append(ids, devices[i].ID)
		byID[devices[i].ID] = devices[i]
	}
	var q = "sum by (device, port) (increase(" + metric +
		`{device=~"` + strings.Join(ids, "|") + `"}[` + promDuration(window) + "]))"
	var series, err = s.TSDB.QueryInstant(ctx, q)
	if err != nil {
		return out
	}
	for i = range series {
		var dev = series[i].Labels["device"]
		var raw = series[i].Labels["port"]
		var d, known = byID[dev]
		if !known || raw == "" {
			continue
		}
		var name = facadeOf(d).resolve(raw)
		if name == "" {
			continue
		}
		var p, has = series[i].Last()
		if !has || p.Value <= 0 {
			continue
		}
		out[clientPortKey(dev, name)] += p.Value
	}
	return out
}
