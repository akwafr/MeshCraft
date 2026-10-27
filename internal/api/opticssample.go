package api

// PERIODIC TRANSCEIVER-DDM SAMPLING — the CLI leg of optical level history.
//
// # Why this loop exists when three collectors already read optics
//
// The optical series (meshcraft_sfp_rx_power_dbm and friends) have three
// producers: the gNMI subscription (Arista, internal/gnmipoll), the RouterOS
// binary API and the SwOS page reader (both proxy/connector channels). The
// blind spot is the platform whose ONLY vantage point on its cages is a CLI
// `show` command — the FS S3400 is the measured case: a full DDM block on the
// CLI, empty enterprise trees over SNMP (cf. internal/driver/optics.go). Its
// readings reached the panel at read-state cadence (extra.optics_observed) and
// never reached the series store, so the level HISTORY every other platform
// gets had a hole exactly where the fleet's cheapest optics live.
//
// driver/optics.go's own header names the exit condition: per-port history
// "belongs to the metrics pipeline the day a collector can take a CLI-sourced
// sample". poesample.go is that collector for wattage; this file is the same
// loop for light, and every architectural decision is inherited from it
// rather than re-argued: controller-driven because only the controller opens
// admin sessions, TSDB written directly because the samples are born here,
// one device at a time against small session budgets, never alongside an
// apply, failures skipped until the next tick.
//
// # Who enters the pass
//
// Devices whose LAST OBSERVATION (extra.optics_observed) exists — the same
// bootstrap rule as PoE: an observation exists only because a ReadOptics
// succeeded, so it proves the connector reads this platform's cages and that
// there is (or recently was) something in them. Platforms whose optics travel
// by gNMI or the MikroTik channels never produce this observation and never
// enter; there is no double producer by construction.

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"meshcraft/internal/driver"
	"meshcraft/internal/metricbuf"
	"meshcraft/internal/model"
)

const (
	// opticsSampleEveryDefault: same figure and same reasoning as the PoE
	// sampler — a level moves on the minute scale, and each sample costs a full
	// admin session on a device that counts them.
	opticsSampleEveryDefault = 5 * time.Minute
	opticsSampleEveryFloor   = time.Minute
	opticsSampleDeviceBudget = 2 * time.Minute
	opticsSampleStartupDelay = 90 * time.Second
)

// opticsSampleEvery resolves the cadence. Same contract as poeSampleEvery:
// empty = default, "0"/"off" = disabled, floor applied, unreadable value falls
// back to the default and says so.
func opticsSampleEvery(raw string) time.Duration {
	if raw == "" {
		return opticsSampleEveryDefault
	}
	if raw == "0" || raw == "off" {
		return 0
	}
	var d, err = time.ParseDuration(raw)
	if err != nil {
		slog.Warn("optics sampler: unreadable MESHCRAFT_OPTICS_SAMPLE_EVERY, "+
			"using default", "value", raw, "default", opticsSampleEveryDefault)
		return opticsSampleEveryDefault
	}
	if d <= 0 {
		return 0
	}
	if d < opticsSampleEveryFloor {
		return opticsSampleEveryFloor
	}
	return d
}

// RunOpticsSampler runs periodic DDM sampling until context cancellation.
// Mounted from cmd/controller/main.go beside RunPoESampler.
//
// Unlike the PoE sampler it EXITS when no series store is configured: its only
// product beyond what read-state already maintains IS the series. The
// observation refresh alone would not justify an admin session per device per
// five minutes on an instance that retains nothing.
func (s *Server) RunOpticsSampler(ctx context.Context) {
	var every = opticsSampleEvery(s.Cfg.OpticsSampleEvery)
	if every == 0 {
		slog.Info("optics sampler: disabled (MESHCRAFT_OPTICS_SAMPLE_EVERY=0/off)")
		return
	}
	if s.TSDB == nil {
		slog.Info("optics sampler: no series store, not sampling " +
			"(readings still refresh at read-state cadence)")
		return
	}
	slog.Info("optics sampler: active", "every", every)
	select {
	case <-ctx.Done():
		return
	case <-time.After(opticsSampleStartupDelay):
	}
	var t = time.NewTicker(every)
	defer t.Stop()
	for {
		s.opticsSamplePass(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// opticsSamplePass runs ONE pass, one device at a time — session budgets, cf.
// the file header.
func (s *Server) opticsSamplePass(ctx context.Context) {
	var devices, err = s.allDevices(ctx)
	if err != nil {
		slog.Warn("optics sampler: device list unreadable", "err", err)
		return
	}
	var i int
	for i = range devices {
		if ctx.Err() != nil {
			return
		}
		var d = devices[i]
		if !opticsSampleWanted(d) {
			continue
		}
		if s.applyQueuedOrRunning(d.ID) {
			slog.Info("optics sampler: apply in progress, skipping",
				"device", d.Name)
			continue
		}
		s.sampleDeviceOptics(ctx, d)
	}
}

// opticsSampleWanted decides whether THIS device is worth an admin session.
//
// PURE, same standing as poeSampleWanted and the same shape of answer: the
// last observation is the proof of readability AND of there being something
// to read. A device whose cages were all empty at the last read still
// qualifies when the observation carries notes (a cage the read failed on is
// exactly what the next pass should retry); a device with no observation at
// all is one whose connector never proved the read, and read-state is the
// gesture that bootstraps it — deliberately, so this loop can never invent
// the capability.
func opticsSampleWanted(d model.Device) bool {
	if d.MgmtIP == "" || d.Status == "off" {
		return false
	}
	return opticsObservedOf(d) != nil
}

// sampleDeviceOptics reads DDM on ONE device and lands the two products: the
// refreshed observation (the panel repaints from it exactly as after a manual
// read-state) and the series. Independent landings, same rule as PoE.
func (s *Server) sampleDeviceOptics(parent context.Context, d model.Device) {
	var ctx, cancel = context.WithTimeout(parent, opticsSampleDeviceBudget)
	defer cancel()
	// Same rule as the PoE sampler: consult the connector's declaration before
	// spending a session on a platform that declares no DDM read.
	var conn, connErr = s.connectorFor(ctx, d)
	if connErr == nil && !driver.Has(conn, driver.GestureOptics) {
		return
	}
	var _, adm, _, err = s.adminFor(ctx, d)
	if adm != nil {
		defer adm.Close()
	}
	if err != nil {
		slog.Warn("optics sampler: session refused", "device", d.Name, "err", err)
		return
	}
	var reader, canRead = driver.AsOpticsReader(adm)
	if !canRead {
		// The stored observation predates a connector change; the next
		// read-state refreshes eligibility.
		return
	}
	var st driver.OpticsState
	if st, err = reader.ReadOptics(ctx); err != nil {
		slog.Warn("optics sampler: read failed", "device", d.Name, "err", err)
		return
	}
	var raw, marshalErr = json.Marshal(st)
	if marshalErr == nil {
		if err = s.Store.SaveOpticsObserved(ctx, d.ID, raw); err != nil {
			slog.Warn("optics sampler: observation not saved",
				"device", d.Name, "err", err)
		}
	}
	var samples = opticsSamples(d, st, time.Now())
	if len(samples) > 0 {
		if err = s.TSDB.Write(ctx, samples); err != nil {
			slog.Warn("optics sampler: series write refused",
				"device", d.Name, "err", err)
		}
	}
}

// opticsSamples renders ONE device's observation as metric samples.
//
// PURE. One sample per served reading — the OpticsPortState pointers are the
// contract: nil is "the module does not serve this" and produces NOTHING,
// because 0.0 dBm is a legal power and the difference between "no reading" and
// "zero" is the difference between a module that cannot measure and a fibre at
// exactly one milliwatt (cf. driver/optics.go). No `lane` label: this channel
// reads one figure per cage, the single-reading shape names.go documents.
//
// The port name is used AS the connector reports it — the OpticsReader
// contract says it is already a facade name — but still resolved, which is
// what holds that contract to account; an unresolvable name is dropped, never
// invented. Same clause as poeSamples.
func opticsSamples(d model.Device, st driver.OpticsState,
	now time.Time) []metricbuf.Sample {

	var f = facadeOf(d)
	var out []metricbuf.Sample
	var one = func(name, port string, v *float64) {
		if v == nil {
			return
		}
		out = append(out, metricbuf.Sample{
			Name:   name,
			Labels: map[string]string{"device": d.ID, "port": port},
			Value:  *v,
			TS:     now,
		})
	}
	var p driver.OpticsPortState
	for _, p = range st.Ports {
		var port = f.resolve(p.Port)
		if port == "" {
			continue
		}
		one(metricbuf.MetricSFPRxPowerDBM, port, p.RxPowerDBm)
		one(metricbuf.MetricSFPTxPowerDBM, port, p.TxPowerDBm)
		one(metricbuf.MetricSFPTemperature, port, p.TemperatureC)
		one(metricbuf.MetricSFPSupplyVolts, port, p.VoltageV)
		one(metricbuf.MetricSFPTxBiasMilliamps, port, p.BiasCurrentMA)
	}
	return out
}
