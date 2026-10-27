package api

// PERIODIC PoE SAMPLING — the loop that keeps the panel's draw figure honest.
//
// # What existed before this file, and the hole it leaves
//
// `ReadPoE` rode the read-state gesture only (cf. deviceaddr.go readPoE and
// poe_apply.go): the observation is persisted under `extra.poe_observed` and
// served as `poe_state`, but ONLY when an operator clicks. Between clicks the
// PoE tile and the port faceplate show watts measured whenever someone last
// asked — hours or weeks ago. And `poe_w` in `devices.ports`, which three
// screens already read (network map poeDot, Ports.tsx wattage cell, the
// telemetry panel's PoE tile summing it), was NEVER written by any collector:
// it only ever arrived via config import. No PoE series reached the metrics
// pipeline at all, so "draw over time" had no chart and never could.
//
// # The architecture this sampler grafts onto (studied 2026-08-05)
//
// SNMP metrics flow PROXY-side: the proxy's poller (internal/snmppoll)
// samples OIDs on cadence, stamps every sample with the `device` label,
// buffers them (internal/metricbuf, at-least-once Take/Ack), and POSTs
// batches to /api/proxy/metrics. The controller (proxymetrics_handlers.go)
// authenticates the (proxy_id, secret) pair, filters the batch against the
// proxy's COLLECTION scope, relays it to VictoriaMetrics through
// [tsdb.Client.Write], then patches `devices.ports` with what the facade can
// render (applyPortTelemetry: `link`, negotiated speed) via
// [store.Store.UpdatePortTelemetry] — a jsonb merge that touches ONLY
// measured keys and never clobbers intent (`mode`, `pvid`, `tagged`,
// `label`).
//
// Port telemetry also lands via a CONTROLLER loop: RunPortRates
// (portrates.go, 30 s) queries the series store for octet rates and writes
// `rx_mbps`/`tx_mbps` through the same UpdatePortTelemetry shelf, zeroing
// ports whose series vanished so no phantom load survives. The controller's
// other periodic loops are RunSNMPHeal (snmpheal.go, 15 min with a 90 s
// startup delay and per-device doubling backoff), sweepPortFlaps (30 s),
// revalidateTunnels, and main.go's hourly session/candidate purge. Automatic
// apply is not a ticker but a per-device debounce queue (autoapply.go): one
// 6 s timer per touched device and a `busy` map that serializes applies —
// one at a time per device.
//
// # Why the sampler is controller-driven, and why it writes tsdb directly
//
// PoE draw is CLI-read on every platform surveyed (EdgeSwitch: `show poe
// port all` / `show poe status all` — no SNMP OID serves it), so the proxy
// SNMP poller structurally cannot produce these samples. The reader lives
// behind [driver.Admin], which only the controller can open (adminFor:
// credentials, transport, try budgets). So the sampler is the read-state
// gesture, scheduled: same session door, same persistence shelf.
//
// Emission into the metrics pipeline is CONTROLLER-SIDE insertion through
// s.TSDB.Write — the same client /api/proxy/metrics relays through, so no
// new pipeline is introduced. Routing the samples through a proxy instead
// would buy nothing and cost two things: a proxy round-trip for values the
// controller already holds, and a scope-filter question that does not exist
// here (the `device` label comes from OUR inventory row, not from a proxy's
// claim).
//
// # Guardrails
//
//   - ONE device at a time, sequentially: session budgets are small (Instant
//     On five, ProCurve four), and a pass that opened N sessions at once
//     would starve the operators' own gestures;
//   - never alongside an apply: the pass consults the apply queue's own
//     serialization (applyQueuedOrRunning — the `busy`/`pending` maps in
//     autoapply.go) and SKIPS the device. Skipping, not queuing: the next
//     tick comes soon enough, and blocking behind a 3-minute apply would
//     stall every device after it in the pass;
//   - devices without PoE hardware are skipped by a PURE eligibility rule
//     (poeSampleWanted): last observation shows at least one Capable port.
//     A device never read has no observation and is skipped — read-state is
//     the gesture that bootstraps eligibility, deliberately: guessing PoE
//     hardware from a model name is what driver/poe.go forbids;
//   - failure is logged and SKIPPED, never retried within a pass — same
//     stance as automatic apply ("retrying forever on a refusing device is
//     the best way to lock it out"). The next tick is the only retry.
import (
	"context"
	"encoding/json"
	"log/slog"
	"math"
	"time"

	"meshcraft/internal/driver"
	"meshcraft/internal/metricbuf"
	"meshcraft/internal/model"
)

const (
	// poeSampleStartupDelay: let proxy tunnels come back up before the first
	// pass — same reasoning and same figure as snmpHealStartupDelay: a
	// session through a tunnel that is not up yet fails for a reason that
	// disappears on its own.
	poeSampleStartupDelay = 90 * time.Second
	// poeSampleEveryDefault: the cadence when MESHCRAFT_POE_SAMPLE_EVERY is
	// unset. FIVE MINUTES is deliberately coarse next to the SNMP poll (5 s
	// counters): every sample here costs a full admin session on a device
	// that counts them, and a camera's draw moves on the minute scale, not
	// the second.
	poeSampleEveryDefault = 5 * time.Minute
	// poeSampleEveryFloor: refuse to hammer devices however the env is set.
	// One admin session per device per minute is already the aggressive end.
	poeSampleEveryFloor = time.Minute
	// poeSampleDeviceBudget: cap ONE device's session + read. Two minutes is
	// the autoapply ceiling family (3 min there, for writes); a CLI PoE read
	// is two `show` commands and must not stall the whole pass behind one
	// wedged transport.
	poeSampleDeviceBudget = 2 * time.Minute
)

// poeSampleEvery resolves the cadence from configuration.
//
// Returns 0 to say DISABLED. Accepted forms for MESHCRAFT_POE_SAMPLE_EVERY:
// empty = default; "0" or "off" = disabled; otherwise a Go duration ("5m",
// "90s"), floored at one minute. An unreadable value falls back to the
// default and says so — a typo in an env var must not silently kill a
// monitoring loop.
func poeSampleEvery(raw string) time.Duration {
	if raw == "" {
		return poeSampleEveryDefault
	}
	if raw == "0" || raw == "off" {
		return 0
	}
	var d, err = time.ParseDuration(raw)
	if err != nil {
		slog.Warn("PoE sampler: unreadable MESHCRAFT_POE_SAMPLE_EVERY, "+
			"using default", "value", raw, "default", poeSampleEveryDefault)
		return poeSampleEveryDefault
	}
	if d <= 0 {
		return 0
	}
	if d < poeSampleEveryFloor {
		return poeSampleEveryFloor
	}
	return d
}

// RunPoESampler runs periodic PoE sampling until context cancellation.
//
// Mounted from cmd/controller/main.go like RunPortRates and RunSNMPHeal.
// Unlike RunPortRates it does NOT exit when no series store is configured:
// its primary product is `poe_w` in inventory and a fresh `poe_observed`,
// which need no store — only the series emission is skipped then.
func (s *Server) RunPoESampler(ctx context.Context) {
	var every = poeSampleEvery(s.Cfg.PoESampleEvery)
	if every == 0 {
		slog.Info("PoE sampler: disabled (MESHCRAFT_POE_SAMPLE_EVERY=0/off)")
		return
	}
	slog.Info("PoE sampler: active", "every", every)
	select {
	case <-ctx.Done():
		return
	case <-time.After(poeSampleStartupDelay):
	}
	var t = time.NewTicker(every)
	defer t.Stop()
	for {
		s.poeSamplePass(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// poeSamplePass runs ONE pass, one device at a time. One device's failure
// stops neither the pass nor the loop.
func (s *Server) poeSamplePass(ctx context.Context) {
	var devices, err = s.allDevices(ctx)
	if err != nil {
		slog.Warn("PoE sampler: device list unreadable", "err", err)
		return
	}
	var i int
	for i = range devices {
		if ctx.Err() != nil {
			return
		}
		var d = devices[i]
		if !poeSampleWanted(d) {
			continue
		}
		if s.applyQueuedOrRunning(d.ID) {
			// An apply is running or due on this device: its session comes
			// first. Skip — the next tick retries on its own.
			slog.Info("PoE sampler: apply in progress, skipping",
				"device", d.Name)
			continue
		}
		s.sampleDevicePoE(ctx, d)
	}
}

// poeSampleWanted decides whether THIS device is worth an admin session.
//
// PURE, because this is the decision that would silently burn session budget
// fleet-wide if it went wrong, and it must be testable without a database or
// a device.
//
// The gate is the LAST OBSERVATION (extra.poe_observed): at least one port
// with power-sourcing hardware behind it ([driver.PoEPortState.Capable]).
// That single fact carries three answers at once: the platform has PoE
// hardware, the connector proved it can read it (an observation exists only
// because a ReadPoE succeeded), and there is something to measure. A device
// whose connector cannot read PoE never produced an observation, and a
// PoE-less unit's observation shows no capable port — both skip without a
// session.
//
// `status == "off"` skips too: every proxy that knows this device has lost
// it (cf. reachability.go), so a session attempt is a guaranteed timeout
// paid every tick. "unknown" still tries — nobody is watching, which is not
// evidence the controller's own session path is down.
func poeSampleWanted(d model.Device) bool {
	if d.MgmtIP == "" || d.Status == "off" {
		return false
	}
	var obs = poeObservedOf(d)
	if obs == nil {
		return false
	}
	var p driver.PoEPortState
	for _, p = range obs.Ports {
		if p.Capable {
			return true
		}
	}
	return false
}

// sampleDevicePoE reads PoE on ONE device and lands the three products:
// the observation (extra.poe_observed), per-port `poe_w` in devices.ports,
// and the wattage series. Each landing is independent — a refused series
// write must not cost the inventory refresh that already succeeded.
func (s *Server) sampleDevicePoE(parent context.Context, d model.Device) {
	var ctx, cancel = context.WithTimeout(parent, poeSampleDeviceBudget)
	defer cancel()
	// Ask the DECLARATION before spending a session: on a range that hands out
	// five, authenticating just to learn the platform has no PoE read is the
	// most expensive possible way to hear "no". The assertion below remains as
	// the check that cannot lie.
	var conn, connErr = s.connectorFor(ctx, d)
	if connErr == nil && !driver.Has(conn, driver.GesturePoERead) {
		return
	}
	var _, adm, _, err = s.adminFor(ctx, d)
	if adm != nil {
		defer adm.Close()
	}
	if err != nil {
		slog.Warn("PoE sampler: session refused", "device", d.Name, "err", err)
		return
	}
	var reader, canRead = driver.AsPoEReader(adm)
	if !canRead {
		// The stored observation predates a connector change: nothing to
		// read anymore, and the next read-state will refresh eligibility.
		return
	}
	var st driver.PoEState
	if st, err = reader.ReadPoE(ctx); err != nil {
		slog.Warn("PoE sampler: read failed", "device", d.Name, "err", err)
		return
	}

	// Same shelf as the read-state gesture, so the panel's `poe_state`
	// repaints from this pass exactly as it would from a manual read.
	var raw, marshalErr = json.Marshal(st)
	if marshalErr == nil {
		if err = s.Store.SavePoEObserved(ctx, d.ID, raw); err != nil {
			slog.Warn("PoE sampler: observation not saved",
				"device", d.Name, "err", err)
		}
	}

	// Per-port draw into inventory, same shelf as `rx_mbps`/`link`
	// (UpdatePortTelemetry merges measured keys only, intent survives).
	var patch = poeWattsPatch(d, st)
	if len(patch) > 0 {
		var rawPatch, patchErr = json.Marshal(patch)
		if patchErr == nil {
			if err = s.Store.UpdatePortTelemetry(ctx, d.ID, rawPatch); err != nil {
				slog.Warn("PoE sampler: poe_w write refused",
					"device", d.Name, "err", err)
			}
		}
	}

	// The series, when a store exists. Controller-side insertion — cf. the
	// file header for why this does not ride the proxy path.
	if s.TSDB != nil {
		var samples = poeSamples(d, st, time.Now())
		if len(samples) > 0 {
			if err = s.TSDB.Write(ctx, samples); err != nil {
				slog.Warn("PoE sampler: series write refused",
					"device", d.Name, "err", err)
			}
		}
	}
}

// poeWattsPatch builds the `poe_w` inventory patch for ONE device.
//
// PURE, same standing as portRatePatch: it decides which wattage displays on
// which port, and mapping wrong produces a perfectly credible false reading.
//
// The rules, each inherited from a neighbor that learned it the hard way:
//
//   - names resolve through the FACADE ([facadeOf]), never trusted raw — the
//     connector contract says PoEPortState.Port is already a faceplate name,
//     and resolve() is what holds that contract to account. An unresolvable
//     port is DROPPED, never invented: telemetry does not create ports;
//   - a capable port that stopped delivering writes ZERO, and a port the
//     platform lists without PSE hardware (Capable=false) zeroes any stale
//     figure too — the anti-phantom rule of portrates.go: between a value
//     that drops and a permanent ghost wattage, the first reads correctly;
//   - values are rounded to 0.1 W and UNCHANGED values are omitted: this
//     patch is compared pass to pass through the database, and rewriting an
//     identical figure every five minutes is free DB churn (cf.
//     portRatePatch's identical skip).
func poeWattsPatch(d model.Device, st driver.PoEState) map[string]map[string]any {
	var f = facadeOf(d)
	// Current poe_w per port, to skip no-op writes. Unreadable inventory
	// decodes to an empty map, which makes every measured value "changed" —
	// the right fallback: write, don't guess.
	var current = map[string]struct {
		PoEW float64 `json:"poe_w"`
	}{}
	_ = json.Unmarshal(d.Ports, &current)

	var out = map[string]map[string]any{}
	var p driver.PoEPortState
	for _, p = range st.Ports {
		var name = f.resolve(p.Port)
		if name == "" {
			continue
		}
		var watts float64
		if p.Capable && p.Delivering {
			watts = poeRound(p.PowerW)
		}
		if poeRound(current[name].PoEW) == watts {
			continue
		}
		out[name] = map[string]any{"poe_w": watts}
	}
	return out
}

// poeSamples renders ONE device's observation as metric samples.
//
// PURE for the same reason as poeWattsPatch. One sample per CAPABLE port
// (MetricPoEPowerW, zero included — cf. names.go: zero is a measurement),
// plus one device total (MetricPoEUsedW): the platform's own sum when it
// serves one, otherwise the sum of port draws — adding watts is arithmetic,
// not a home-grown derivative.
//
// Returns nil when the observation shows no capable port: a PoE-less device
// must produce NO series, not a flat zero line that promises hardware.
func poeSamples(d model.Device, st driver.PoEState, now time.Time) []metricbuf.Sample {
	var f = facadeOf(d)
	var out []metricbuf.Sample
	var sum float64
	var anyCapable bool
	var p driver.PoEPortState
	for _, p = range st.Ports {
		if !p.Capable {
			continue
		}
		anyCapable = true
		var name = f.resolve(p.Port)
		if name == "" {
			continue
		}
		var watts float64
		if p.Delivering && p.PowerW > 0 {
			watts = p.PowerW
		}
		sum += watts
		out = append(out, metricbuf.Sample{
			Name:   metricbuf.MetricPoEPowerW,
			Labels: map[string]string{"device": d.ID, "port": name},
			Value:  watts,
			TS:     now,
		})
	}
	if !anyCapable {
		return nil
	}
	var used = st.UsedW
	if used <= 0 {
		used = sum
	}
	out = append(out, metricbuf.Sample{
		Name:   metricbuf.MetricPoEUsedW,
		Labels: map[string]string{"device": d.ID},
		Value:  used,
		TS:     now,
	})
	return out
}

// poeRound rounds to tenths of a watt.
//
// Rounded because these values are COMPARED pass to pass to decide whether
// to write (cf. round3 in portrates.go, same trap): a draw oscillating on
// the milliwatt would trigger a DB write every pass for an identical
// display. NaN, infinities and negatives collapse to zero — a negative watt
// does not exist, and displaying one sends someone hunting a wiring fault.
func poeRound(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return 0
	}
	return math.Round(v*10) / 10
}
