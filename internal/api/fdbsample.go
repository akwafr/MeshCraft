package api

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"meshcraft/internal/driver"
	"meshcraft/internal/model"
	"meshcraft/internal/snmppoll"
)

// The FDB sampler: periodic forwarding-table reads over the ADMIN SESSION, for
// the platforms neither collector can serve.
//
// # Who this is for, exactly
//
// One platform today: Arista EOS 4.28, whose three telemetry doors were probed
// closed on 2026-08-13 — OpenConfig fdb rejected (schema node absent), the
// Smash export carries no bridging tree (enumerated live through Octa), and
// Sysdb/bridging serves config only. Its connector reads the table over the
// CLI (`ReadFDBObservation`), but until this file that read ran only at
// read-state — the Clients page went stale between operator gestures. The FS
// S3400 joins this list only if its bridge-MIB walk comes back empty once the
// v3 collector can ask (its connector implements no FDB reader yet either
// way).
//
// # Why the cadence is minutes and not the 30 s the collectors get
//
// Every sample is a full admin session: TCP, key exchange, authentication,
// shell, prompt discipline. The platforms that live on this path are exactly
// the ones with brutal session budgets elsewhere in this repo (the ProCurve
// accepts FOUR sessions including the console; the Instant On evicts a human
// admin per web read; the S3400 feeds a pager one round trip per screenful) —
// and the phenomenon does not need it: a bridge remembers a client for ~300 s
// (dot1dTpAgingTime default), so minutes lose no client the switch itself
// still testifies to. Where 30 s matters, that is collector work, never a
// session's. Same figures as the optics sampler, same reasoning.
//
// # Two deliberate deltas from opticssample.go, whose shape this borrows
//
//   - NO series-store exit clause: the optics sampler exits without a TSDB
//     because its only product beyond read-state IS the series. This one's
//     product is the mirror rows themselves (mac_sightings + the clients
//     ledger), which exist TSDB or not.
//   - GATING is by exclusion and memory, not by a stored observation: a
//     device whose connector declares an `fdb` channel (SNMP or gNMI) is the
//     collectors' business — sampling it too would make the "two paths cannot
//     fight" property false. Whether the connector implements the CLI read at
//     all is learned by asking (AsFDBReader) on the first session and
//     remembered per process, so a platform without the reader costs one
//     session per controller lifetime, not one per tick.

// fdbSampleEveryDefault: the cadence when MESHCRAFT_FDB_SAMPLE_EVERY is unset.
const fdbSampleEveryDefault = 2 * time.Minute

// fdbSampleEveryFloor: the floor below which a configured cadence is raised.
const fdbSampleEveryFloor = time.Minute

// fdbSampleStartupDelay leaves adoption/startup traffic room, like optics'.
const fdbSampleStartupDelay = 45 * time.Second

// fdbSampleDeviceBudget bounds ONE device's read — a pager-fed table on a busy
// switch is many round trips, but a hung session must not hold the pass.
const fdbSampleDeviceBudget = 2 * time.Minute

// fdbSampleEvery reads the env knob, same grammar as the optics sampler's.
func fdbSampleEvery(raw string) time.Duration {
	if raw == "" {
		return fdbSampleEveryDefault
	}
	if raw == "0" || raw == "off" {
		return 0
	}
	var d, err = time.ParseDuration(raw)
	if err != nil {
		slog.Warn("fdb sampler: unreadable MESHCRAFT_FDB_SAMPLE_EVERY, using default",
			"value", raw, "default", fdbSampleEveryDefault)
		return fdbSampleEveryDefault
	}
	if d <= 0 {
		return 0
	}
	if d < fdbSampleEveryFloor {
		return fdbSampleEveryFloor
	}
	return d
}

// fdbSampledByCollector says a device's forwarding table is a COLLECTOR's
// business: its connector declares an `fdb` channel on any kind. Pure.
func fdbSampledByCollector(d model.Device, driverName string) bool {
	var drv, known = driver.Get(driverName)
	if !known {
		return false
	}
	var ch driver.MonitorChannel
	for _, ch = range drv.Capabilities(d.Model).Monitoring.Channels {
		var m string
		for _, m = range ch.Metrics {
			if m == snmppoll.MetricFDB {
				return true
			}
		}
	}
	return false
}

// RunFDBSampler runs periodic forwarding-table sampling until context
// cancellation. Mounted from cmd/controller/main.go beside the optics sampler.
func (s *Server) RunFDBSampler(ctx context.Context) {
	var every = fdbSampleEvery(s.Cfg.FDBSampleEvery)
	if every == 0 {
		slog.Info("fdb sampler: disabled (MESHCRAFT_FDB_SAMPLE_EVERY=0/off)")
		return
	}
	slog.Info("fdb sampler: active", "every", every)
	select {
	case <-ctx.Done():
		return
	case <-time.After(fdbSampleStartupDelay):
	}
	// noReader remembers, per process, the devices whose open session answered
	// "no FDB reader": one exploratory session per controller lifetime each.
	var noReader = &sync.Map{}
	var t = time.NewTicker(every)
	defer t.Stop()
	for {
		s.fdbSamplePass(ctx, noReader)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// fdbSamplePass runs ONE pass, one device at a time — session budgets.
func (s *Server) fdbSamplePass(ctx context.Context, noReader *sync.Map) {
	var devices, err = s.allDevices(ctx)
	if err != nil {
		slog.Warn("fdb sampler: device list unreadable", "err", err)
		return
	}
	var i int
	for i = range devices {
		if ctx.Err() != nil {
			return
		}
		var d = devices[i]
		if d.MgmtIP == "" || d.Status == "off" {
			continue
		}
		var _, skip = noReader.Load(d.ID)
		if skip {
			continue
		}
		var _, driverName = s.deviceIdentity(ctx, d)
		if fdbSampledByCollector(d, driverName) {
			continue
		}
		if s.applyQueuedOrRunning(d.ID) {
			continue
		}
		s.sampleDeviceFDB(ctx, d, noReader)
	}
}

// sampleDeviceFDB reads one device's table and lands it through the SAME
// snapshot path as read-state (fdbCLISnapshot → ReplaceFDB) — one vocabulary,
// one drop rule, whoever triggered the read.
func (s *Server) sampleDeviceFDB(parent context.Context, d model.Device,
	noReader *sync.Map) {
	var ctx, cancel = context.WithTimeout(parent, fdbSampleDeviceBudget)
	defer cancel()
	// Same rule as the PoE sampler: consult the connector's declaration before
	// spending a session — and remember the "no" the same way the assertion
	// path does, so the device is not re-asked every sweep.
	var conn, connErr = s.connectorFor(ctx, d)
	if connErr == nil && !driver.Has(conn, driver.GestureFDB) {
		noReader.Store(d.ID, true)
		return
	}
	var _, adm, _, err = s.adminFor(ctx, d)
	if adm != nil {
		defer adm.Close()
	}
	if err != nil {
		slog.Warn("fdb sampler: session refused", "device", d.Name, "err", err)
		return
	}
	var reader, canRead = driver.AsFDBReader(adm)
	if !canRead {
		noReader.Store(d.ID, true)
		return
	}
	var obs driver.FDBObservation
	if obs, err = reader.ReadFDBObservation(ctx); err != nil {
		// An errored read persists NOTHING — a truncated table written through
		// ReplaceFDB would read as mass departure on the Clients page.
		slog.Warn("fdb sampler: read failed", "device", d.Name, "err", err)
		return
	}
	var sightings, dropped = fdbCLISnapshot(d, obs)
	if err = s.Store.ReplaceFDB(ctx, d.ID, d.SiteID, sightings); err != nil {
		slog.Warn("fdb sampler: snapshot not saved", "device", d.Name, "err", err)
		return
	}
	if dropped > 0 {
		slog.Debug("fdb sampler: entries without a nameable port dropped",
			"device", d.Name, "dropped", dropped)
	}
}
