package api

// PERIODIC CONFIG READ-BACK — the loop that stops meshcraft from knowing only
// its own intentions between two clicks.
//
// # The operational defect this closes (reported 2026-08-08)
//
// "I had to click refresh to see the state (shut / no shut) of the port. If
// config changes, it's not updated in the UI until I force a re-read of the
// equipment."
//
// That is exactly right, and it was structural. `handleReadDeviceState` — the
// whole gesture that produces `devices.observed`, per-port link state, drift,
// the faceplate, the STP / PoE / optics observations — was mounted on ONE
// trigger: POST /api/devices/{id}/read-state, i.e. a human clicking. Nothing
// called it on a schedule. So between two clicks the fleet screens showed
// meshcraft's INTENT and an observation of unknown age, and SwitchDetail.tsx
// says so in as many words above its own read-state mutation: "without
// rereading, meshcraft only knows its own intentions".
//
// What made this easy to miss is that meshcraft is NOT short of periodic
// collection — it is short of periodic CONFIG collection. The proxy's SNMP
// poller runs continuously (internal/snmppoll → metricbuf → /api/proxy/metrics),
// RunPortRates derives rates every 30 s, RunPoESampler samples watts every 5
// min, ping watch keeps `devices.status` current. Every one of those is
// TELEMETRY: counters, watts, link, reachability. Not one of them can answer
// "is GE12 shut", "what VLANs does this trunk actually carry", "has somebody
// changed this switch behind our back" — those live behind an administration
// session, and only the button opened one.
//
// # What this loop is
//
// The read-state gesture, scheduled. Not a reimplementation: it calls
// [Server.readDeviceState], the exact function the button calls, which is why
// that function was extracted from the handler in the same change (see its
// header for what stayed behind and why). Same session door (adminFor), same
// persistence shelves, same best-effort policy on every catch-up. A device
// refreshed by this loop is indistinguishable from one somebody just clicked.
//
// # Guardrails, inherited from RunPoESampler and RunSNMPHeal
//
//   - ONE device at a time, sequentially. Session budgets are small and
//     documented in blood: Instant On grants five, and a saturated ProCurve 2610
//     authenticates then refuses the channel — which reads like a pty bug, not
//     like a budget. A pass that opened sessions in parallel would starve the
//     operators' own gestures;
//   - NEVER alongside an apply: the pass consults the apply queue's own
//     serialization (applyQueuedOrRunning — the `busy` and `pending` maps of
//     autoapply.go) and SKIPS. Skipping rather than queuing, for the reason the
//     PoE sampler gives: waiting behind a three-minute apply would stall every
//     device after it, and a second waiter beside that queue would be a second
//     serialization mechanism;
//   - failure is logged and skipped, never retried within a pass. The next tick
//     is the only retry, and a per-device cooldown doubles behind it;
//   - a startup delay, so a controller restart does not stampede the fleet.
//
// # It cannot clobber intent, and that is a property of the gesture
//
// Every write [Server.readDeviceState] performs lands on an OBSERVATION shelf
// (`devices.observed` via store.SaveObserved, which touches `observed` and
// `observed_at` and nothing else; `extra.stp_observed` / `poe_observed` /
// `optics_observed`; the `lldp_*` tables) or on a hardware fact only the device
// knows (faceplate, model). None of them writes `devices.ports`, calls
// TouchDesired, calls MarkApplied, or schedules an apply. The one exception
// touching the ports column — remapDevicePortsIfStale — RENAMES keys the
// connector itself says translate and deliberately does not date the change.
// This is the same shape as [store.Store.UpdatePortTelemetry] on the telemetry
// side: a shelf that carries only what was measured, so mode / pvid / tagged /
// label survive whatever the device answers.
//
// So the loop cannot cause an apply. It can only make what the screen shows
// TRUE, which is the entire point.

import (
	"context"
	"encoding/json"
	"log/slog"
	"maps"
	"sort"
	"strings"
	"sync"
	"time"

	"meshcraft/internal/driver"
	"meshcraft/internal/model"
	"meshcraft/internal/recon"
)

const (
	// configPollStartupDelay: 90 s is snmpHealStartupDelay's figure — let the
	// proxies' tunnels come back up, so a session does not fail for a reason
	// that disappears on its own. The rest is COLLISION AVOIDANCE, and it is the
	// reason this constant is not simply 90 s: RunSNMPHeal and RunPoESampler
	// both wake at 90 s and RunHostnameHeal at 2 min, all four sweep the same
	// fleet, and three loops arriving on one device at once is precisely the
	// concurrency the "one at a time" rule exists to prevent. Three minutes puts
	// this pass — the most expensive of the four per device — alone on the wire
	// when it starts.
	configPollStartupDelay = 3 * time.Minute
	// configPollEveryDefault: the cadence when MESHCRAFT_CONFIG_POLL_EVERY is
	// unset, and the effective FRESHNESS GUARANTEE — see configPollWanted, which
	// gates on the age of `observed_at` rather than on the tick.
	//
	// THIRTY MINUTES, and the number is argued from cost, not from taste. A PoE
	// sample is two `show` commands on a session (5 min, poeSampleEveryDefault);
	// this is the FULL read — ReadState plus management addressing plus spanning
	// tree plus PoE plus optics plus LLDP plus faceplate plus identity, a dozen
	// commands or REST round-trips, tens of seconds on a CLI platform behind a
	// proxy. Six times the interval for roughly six times the work is the honest
	// ratio, and it puts this loop in the same family as RunHostnameHeal (30 min)
	// for the same reason that one gives: nothing DEPENDS on this working, while
	// several things depend on it being right.
	//
	// The upper bound on the number is human: half an hour is about how long an
	// operator will believe a screen they did not just refresh. The lower bound
	// is the pass's own duration — a fleet of two hundred devices read
	// sequentially cannot complete a pass in much less, and a cadence shorter
	// than the pass just means the loop never sleeps.
	configPollEveryDefault = 30 * time.Minute
	// configPollEveryFloor: refuse to hammer the fleet however the env is set.
	// FIVE MINUTES rather than the PoE sampler's one minute, because the unit
	// being rate-limited is not comparable: one minute of PoE sampling is two
	// commands, one minute of this would be a full re-read still in progress
	// when the next one starts.
	configPollEveryFloor = 5 * time.Minute
	// configPollDeviceBudget caps ONE device's session and reads. Two minutes is
	// the poeSampleDeviceBudget figure and sits under the autoapply ceiling
	// (3 min, for writes). It is generous for a read on purpose — a full gesture
	// on a slow CLI behind a proxy is legitimately tens of seconds — and it
	// exists so one wedged transport cannot stall every device queued behind it.
	configPollDeviceBudget = 2 * time.Minute
	// Cooldown after a failed read, doubled on each subsequent failure.
	//
	// The FLOOR is one cadence: a device that refused this pass must not be
	// asked again before the pass after it, or a fleet with ten dead devices
	// spends its budget on them. The CEILING is deliberately SIX HOURS and not
	// the twenty-four that snmpheal.go and hostnameheal.go use, because those
	// two bound a WRITE that may be structurally impossible on the platform,
	// while this is a read that needs nothing but an answer. Capping the retry
	// of a staleness bound at a full day would defeat the loop it belongs to:
	// the promise here is "what you are looking at is at most a few hours old",
	// and a sulking device must still land inside that sentence.
	configPollBackoffMin = 30 * time.Minute
	configPollBackoffMax = 6 * time.Hour
)

// configPollEvery resolves the cadence from configuration.
//
// Returns 0 to say DISABLED. Accepted forms for MESHCRAFT_CONFIG_POLL_EVERY,
// deliberately identical to MESHCRAFT_POE_SAMPLE_EVERY's so an operator learns
// one convention: empty = default; "0" or "off" = disabled; otherwise a Go
// duration ("30m", "2h"), floored at configPollEveryFloor. An unreadable value
// falls back to the default and SAYS SO — a typo in an env var must not
// silently switch a fleet back to click-only refresh, which is the exact defect
// this loop exists to fix.
func configPollEvery(raw string) time.Duration {
	if raw == "" {
		return configPollEveryDefault
	}
	if raw == "0" || raw == "off" {
		return 0
	}
	var d, err = time.ParseDuration(raw)
	if err != nil {
		slog.Warn("config poll: unreadable MESHCRAFT_CONFIG_POLL_EVERY, using default",
			"value", raw, "default", configPollEveryDefault)
		return configPollEveryDefault
	}
	if d <= 0 {
		return 0
	}
	if d < configPollEveryFloor {
		return configPollEveryFloor
	}
	return d
}

// configPollState: memory of failed attempts, IN MEMORY ONLY. A restart clears
// it, and that is intended — a fresh binary deserves an immediate retry.
//
// Third loop with this shape, and hostnameheal.go's own comment says "if a
// third loop needs it, THAT is when it becomes a type". It is still not,
// and the reason is the one that comment gives in its first half: the
// interesting part of each loop's policy is its min and its max, and they all
// differ (1 h → 24 h twice, 30 min → 6 h here). A shared type must either carry
// them as fields — losing the zero-value-ready property all three rely on, since
// Server holds these by value and the tests build them with `var h T` — or take
// them per call, which churns two other files' tests to save thirty lines. Worth
// doing on the day a fourth arrives, or the day Server stops holding them by
// value; not worth dragging into the change that fixes the refresh.
type configPollState struct {
	mu   sync.Mutex
	next map[string]time.Time     // device -> not before this instant
	wait map[string]time.Duration // device -> current cooldown
}

func (h *configPollState) due(id string, now time.Time) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.next == nil {
		return true
	}
	return now.After(h.next[id])
}

func (h *configPollState) failed(id string, now time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.next == nil {
		h.next = map[string]time.Time{}
		h.wait = map[string]time.Duration{}
	}
	var w = h.wait[id]
	if w == 0 {
		w = configPollBackoffMin
	} else {
		w *= 2
		if w > configPollBackoffMax {
			w = configPollBackoffMax
		}
	}
	h.wait[id] = w
	h.next[id] = now.Add(w)
}

func (h *configPollState) succeeded(id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.next, id)
	delete(h.wait, id)
}

// RunConfigPoll runs the periodic config read-back until context cancellation.
//
// Mounted from cmd/controller/main.go beside RunPoESampler, RunSNMPHeal and
// RunHostnameHeal. Needs no series store and no proxy of its own: its product is
// `devices.observed` and everything the read gesture persists around it.
func (s *Server) RunConfigPoll(ctx context.Context) {
	var every = configPollEvery(s.Cfg.ConfigPollEvery)
	if every == 0 {
		slog.Info("config poll: disabled (MESHCRAFT_CONFIG_POLL_EVERY=0/off) — " +
			"observed state will only refresh when an operator clicks read-state")
		return
	}
	slog.Info("config poll: active", "every", every)
	select {
	case <-ctx.Done():
		return
	case <-time.After(configPollStartupDelay):
	}
	var t = time.NewTicker(every)
	defer t.Stop()
	for {
		s.configPollPass(ctx, every)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// configPollPass runs ONE pass, one device at a time, stalest first. One
// device's failure stops neither the pass nor the loop.
//
// A pass that overruns its cadence simply delays the next tick — Go's ticker
// drops the ticks nobody read — so a fleet too large for the interval degrades
// to a continuous sweep rather than to overlapping passes. That is the right
// failure mode, and it is why the ordering below matters: whoever gets cut off
// is whoever was least stale.
//
// The site VLAN lists are read ONCE PER SITE and cached for the pass, the way
// hostnameHealPass caches the LLDP self-rows: `driftOf` needs them to resolve
// "all", the question is per device but the query is per site, and a fleet of
// two hundred must not cost two hundred round trips.
func (s *Server) configPollPass(ctx context.Context, every time.Duration) {
	var devices, err = s.allDevices(ctx)
	if err != nil {
		slog.Warn("config poll: device list unreadable", "err", err)
		return
	}
	var now = time.Now()
	var queue = configPollOrder(devices, now, every)
	var vlansBySite = map[string][]int{}
	// The sites a device was actually read on this sweep — see the nudge below.
	var touched = map[string]bool{}
	var read int
	var i int
	for i = range queue {
		if ctx.Err() != nil {
			return
		}
		var d = queue[i]
		if !s.configPoll.due(d.ID, now) {
			continue
		}
		if s.credDeployRunningOn(d.SiteID) {
			// A fleet credential deployment is changing the only door into this
			// site's switches. applyQueuedOrRunning does not see it — it watches
			// the apply queue — so without this the poller would open a second
			// session on the very device whose password is being rewritten, and
			// the fleet's per-device session ceilings would decide which of the
			// two won. A read losing that race costs one cadence; the write
			// losing it is a half-finished credential change.
			slog.Info("config poll: credential deployment on this site, skipping",
				"device", d.Name)
			continue
		}
		if s.applyQueuedOrRunning(d.ID) {
			// An apply is running or due on this device: its session comes
			// first, and it re-reads state itself when it finishes (cf.
			// runApply's saveObservedState). Skip — the next tick retries.
			slog.Info("config poll: apply in progress, skipping", "device", d.Name)
			continue
		}
		var vlans, cached = vlansBySite[d.SiteID]
		if !cached {
			vlans = s.siteVLANIDs(ctx, d.SiteID)
			vlansBySite[d.SiteID] = vlans
		}
		s.refreshDeviceConfig(ctx, d, vlans)
		read++
		touched[d.SiteID] = true
	}

	// ONE NUDGE PER SITE PER SWEEP, and it is not the same nudge as the
	// per-device one in refreshDeviceConfig.
	//
	// That one fires when the OBSERVATION changed, which is the right trigger for
	// waking a screen that is showing something wrong. This one fires when the
	// observation was merely REFRESHED — because `devices.observed_at` advances on
	// every successful poll, and the interface now renders it ("Config read 12 min
	// ago", freshness.ts).
	//
	// Without this, the healthiest device in the fleet gets the worst label. A
	// browser sitting on a site with a live SSE stream has `refetchInterval` at
	// `false` by design, so with nothing changing it never refetches, and the age
	// on screen keeps climbing while the controller is in fact polling on time —
	// "read 6 h ago" about a device read twelve minutes ago. It errs toward
	// distrust rather than false reassurance, which is the right direction to err,
	// but a freshness indicator that cries stale on a healthy fleet is one nobody
	// believes when it matters.
	//
	// Deliberately per SITE and per SWEEP, not per device: the client reaction is
	// one list refetch, so N device notifications would buy nothing and cost N−1
	// refetches. And deliberately keyed on "a device was read", not "a read
	// succeeded" — a failed read leaves `observed_at` where it was, so the extra
	// refetch is redundant rather than wrong, and threading success back through
	// refreshDeviceConfig to save one query per site per half hour is not a trade
	// worth making.
	var site string
	for site = range touched {
		s.NotifySiteChanged(site, ChangeSync)
	}
	if read > 0 {
		slog.Info("config poll: pass done",
			"read", read, "eligible", len(queue), "sites", len(touched))
	}
}

// configPollWanted decides whether THIS device's config view is stale enough to
// be worth an administration session.
//
// PURE, for poeSampleWanted's reason: this is the decision that would silently
// burn the fleet's session budget if it went wrong, and it must be testable
// without a database or a device.
//
// THE GATE IS AGE, NOT THE TICK, and that choice does three things at once that
// a plain per-tick sweep does not. A device somebody just clicked refresh on is
// skipped, because the click already did this work — the manual gesture and the
// loop share one clock, `devices.observed_at`, so they cannot duplicate each
// other. A fleet adopted at different moments spreads itself across the
// interval instead of arriving in one thundering herd. And the guarantee the
// loop offers becomes statable in the operator's own terms — "what you are
// looking at is at most `every` old" — rather than "a sweep happens every
// `every`, and your device is somewhere in it".
//
// A device NEVER READ is wanted immediately: an unread device is the extreme
// case of the defect this loop exists for — adopted, never clicked, permanently
// showing intent alone. This is the deliberate opposite of poeSampleWanted's
// stance that a device with no observation is skipped, and the difference is
// that read-state is the gesture that BOOTSTRAPS observations. It is us. There
// is nobody else to wait for.
//
// `status == "off"` skips: every proxy that watches this device has lost it
// (cf. reachability.go), so a session attempt is a guaranteed timeout paid every
// pass. "unknown" still tries — nobody watching is not evidence that the
// controller's own session path is down.
func configPollWanted(d model.Device, now time.Time, every time.Duration) bool {
	if d.MgmtIP == "" || d.Status == "off" {
		return false
	}
	if d.ObservedAt == nil {
		return true
	}
	return !now.Before(d.ObservedAt.Add(every))
}

// configPollOrder is the pass's queue: the eligible devices, STALEST FIRST.
//
// PURE and separate from the pass for the same reason configPollWanted is, plus
// one of its own: ordering is the only protection a pass that cannot finish
// has. Read in inventory order (ListDevices sorts by name), a fleet too large
// for its cadence would refresh the alphabet's beginning forever and never
// reach the end — the devices whose view is oldest would be exactly the ones
// never chosen. Oldest-first inverts that: whatever the pass has time for, the
// number it bounds is the WORST staleness in the fleet, which is the number the
// operator actually feels.
//
// Never-read devices sort ahead of everything, being infinitely stale. Ties
// break on ID so a pass is reproducible and a test can assert an order.
func configPollOrder(devices []model.Device, now time.Time,
	every time.Duration) []model.Device {

	var out = make([]model.Device, 0, len(devices))
	var i int
	for i = range devices {
		if configPollWanted(devices[i], now, every) {
			out = append(out, devices[i])
		}
	}
	sort.SliceStable(out, func(a, b int) bool {
		var x, y = out[a].ObservedAt, out[b].ObservedAt
		if x == nil || y == nil {
			if x == nil && y == nil {
				return out[a].ID < out[b].ID
			}
			return x == nil
		}
		if x.Equal(*y) {
			return out[a].ID < out[b].ID
		}
		return x.Before(*y)
	})
	return out
}

// refreshDeviceConfig runs the read gesture on ONE device and decides what, if
// anything, the rest of meshcraft should hear about it.
//
// Events are emitted through [Server.emitDeviceBG] — the request-free twin
// autoapply.go already uses — so history attributes them to "meshcraft" and
// not to whichever operator's session happened to be open.
func (s *Server) refreshDeviceConfig(parent context.Context, d model.Device,
	siteVLANs []int) {

	var ctx, cancel = context.WithTimeout(parent, configPollDeviceBudget)
	defer cancel()

	// What the fleet believed BEFORE this read, captured while `d` still carries
	// it: both are needed to decide whether anything worth telling happened.
	var before = parseObserved(d.Observed)
	var driftBefore = driftOf(d, siteVLANs)

	// `out` is the handler's response body, and here it is diagnostics. The
	// per-capability notes it collects ("spanning tree not read back: …") are
	// worth exactly one debug line: at INFO, one unsupported capability on one
	// connector would print a paragraph per device per pass, for ever.
	var out = map[string]any{}
	var res, err = s.readDeviceState(ctx, d, out, s.emitDeviceBG)
	if err != nil {
		// THE TRANSCRIPT RIDES THE WARNING. The connector wrote down which command
		// failed and why, in `out["steps"]`, and this line used to print only the
		// error the last layer produced — so the diagnosis stopped at "read failed"
		// and the evidence was garbage-collected with the map.
		slog.Warn("config poll: read failed", "device", d.Name, "err", err,
			"failed_steps", configPollFailedSteps(out))
		s.configPoll.failed(d.ID, time.Now())
		return
	}
	s.configPoll.succeeded(d.ID)

	// A READ THAT SUCCEEDED WITH FAILED STEPS IN IT IS THE INTERESTING CASE, and
	// it is the one that had no voice at all.
	//
	// On 2026-08-11 poefs3400lab's "show running-config" died some eight seconds
	// in. The connector recorded that as a failed step; the connector's own caller
	// accepted the partial output, so `err` was nil, this function took the happy
	// path, and the whole transcript went out at Debug — which nothing was running
	// at. A fully self-diagnosing incident deleted its own evidence, and WHY that
	// session died is STILL unknown because of this line.
	//
	// So a failed step on a scheduled read is a WARN whatever the verdict was. The
	// noise argument that keeps the notes at Debug does not apply: notes are the
	// ordinary shape of an unsupported capability and print every pass, a failed
	// step is a command that did not work.
	var failed = configPollFailedSteps(out)
	if failed != "" {
		slog.Warn("config poll: the read reported success but some steps failed",
			"device", d.Name, "sync", res.Sync, "drift", len(res.Changes),
			"failed_steps", failed)
	}
	slog.Debug("config poll: device read", "device", d.Name, "sync", res.Sync,
		"drift", len(res.Changes), "notes", configPollNotes(out))

	// DRIFT, ANNOUNCED ONLY WHEN IT MOVED. See configPollDriftWorthEvent: the
	// button announces every verdict because a human asked; a timer must
	// announce only news, or the event log stops being read.
	if configPollDriftWorthEvent(driftBefore, res.Changes) {
		s.emitDeviceBG(res.Device, "config.drift", model.SevWarning,
			"Configuration drift on \""+d.Name+"\": "+itoa(len(res.Changes))+
				" difference(s) between what meshcraft knows and the device — "+
				"found by the periodic read-back, not by a click.")
		return // emitDeviceBG already woke the site's screens
	}

	// No news for history, but the screens may still be showing the wrong
	// thing: a port shut at the console changes `links`, and link state is NOT
	// drift (it is not a PortIntent — cf. recon.PortIntent's header). That case
	// IS the operator's original complaint, so it gets the live nudge without
	// getting an event.
	if observedFactsChanged(before, parseObserved(res.Device.Observed)) {
		s.NotifySiteChanged(d.SiteID, ChangeSync)
	}
}

// configPollFailedSteps names the steps the connector marked FAILED, in the
// order it recorded them, for one log line.
//
// Only the failures: a transcript is a dozen lines of "authenticated", "model
// recognized", "running-config read" on a healthy device, and printing all of
// it per device per pass is how a log stops being read. The failed ones are the
// ones nobody can reconstruct afterwards — `out` is a local map that dies with
// the call, so a step not logged here is gone.
//
// Empty when the map holds no transcript, or one with nothing wrong in it, which
// is what a caller tests to decide whether the line is worth emitting at all.
func configPollFailedSteps(out map[string]any) string {
	var steps, ok = out["steps"].([]driver.Step)
	if !ok {
		return ""
	}
	var parts []string
	var i int
	for i = range steps {
		if steps[i].OK {
			continue
		}
		var text = steps[i].What
		if steps[i].Detail != "" {
			text += ": " + steps[i].Detail
		}
		parts = append(parts, text)
	}
	return strings.Join(parts, " | ")
}

// configPollNotes collects the per-capability notes the read gesture left in
// `out` ("PoE not read: …"), for one debug line. Sorted, so a log line is
// comparable from one pass to the next.
func configPollNotes(out map[string]any) string {
	var keys = make([]string, 0, len(out))
	var k string
	for k = range out {
		if strings.HasSuffix(k, "_note") {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return ""
	}
	sort.Strings(keys)
	var parts = make([]string, 0, len(keys))
	var i int
	for i = range keys {
		var text, ok = out[keys[i]].(string)
		if ok {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, " | ")
}

// parseObserved reads `devices.observed` back into the shape the read gesture
// writes. Unreadable or absent yields the zero value, which simply means "we
// knew nothing" — and any observation then counts as new.
func parseObserved(raw json.RawMessage) observedState {
	var obs observedState
	if len(raw) == 0 {
		return obs
	}
	_ = json.Unmarshal(raw, &obs)
	return obs
}

// configPollDriftWorthEvent decides whether a SCHEDULED read owes history a
// `config.drift` line. PURE.
//
// # Why this rule exists at all
//
// Until this loop, drift was discovered only when somebody clicked, so
// "announce it every time you find it" cost one event per click and was
// obviously right. On a timer that rule becomes an event per drifted device per
// cadence — a switch somebody reconfigured by hand and nobody has arbitrated
// would emit a severity-3 line every thirty minutes, for weeks, saying the same
// thing. That does not make drift more visible; it makes the event log
// unreadable, and an event log people have learned to scroll past is worse than
// none, because the NEXT real event hides in it.
//
// # The rule
//
// Announce a drift SET that differs from the one the previous read left. That
// covers the three transitions that are news — drift appearing, drift changing
// shape (another port went), drift being partially resolved — and stays silent
// on the one that is not: the same gap still there, still waiting for the same
// arbitration.
//
// Drift DISAPPEARING is silent too, and deliberately so: `after` empty returns
// false. The device going back in sync is already visible where it is looked
// for — the badge turns green, and the live bus wakes the screens — and
// "config.drift" is the wrong event type to carry good news.
//
// Comparison is on the SET, not the slice: recon.DiffPorts walks a map, so the
// order of two identical drift readings is not stable, and a naive slice
// compare would announce phantom news on roughly every other pass.
func configPollDriftWorthEvent(before, after []recon.Change) bool {
	if len(after) == 0 {
		return false
	}
	return !maps.Equal(changeSet(before), changeSet(after))
}

// changeSet renders a drift reading as an order-independent set.
func changeSet(changes []recon.Change) map[string]bool {
	var out = make(map[string]bool, len(changes))
	var i int
	for i = range changes {
		out[changes[i].Label()] = true
	}
	return out
}

// observedFactsChanged reports whether a fresh observation differs from the
// previous one in a way a SCREEN must repaint for. PURE.
//
// # Why not simply compare the two observations whole
//
// Because `observedState` carries `vlans` and per-port `tagged` as SLICES, and
// nothing promises a device enumerates them in the same order twice. Comparing
// the structures whole would report a change on most passes, and this function's
// only job is to decide whether to wake every browser watching the site — a
// predicate that is wrong half the time is a polling loop with extra steps.
//
// # What it compares, and why exactly this
//
// The three facts that move WITHOUT producing drift, so nothing else would
// notice them:
//
//   - `Links` — up / down / disabled / unplugged / err-disabled per port. This
//     is the operator's original complaint: a port shut at the console shows as
//     `disabled` here and is NOT drift, because link state is a measurement and
//     recon.PortIntent deliberately excludes measurements (see its header: a
//     fleet whose indicator blinks on every cable event has an indicator that
//     means nothing);
//   - `LinkUp` — kept separate from Links on purpose (cf. observedState), and
//     compared because it is what the migration-safety rule reads;
//   - `MgmtVLAN` — the VLAN the device says it is administered through. A change
//     here is a fact the management screens draw, and it is not diffed either.
//
// VLAN membership moving IS drift, and drift is handled by the caller's other
// branch — this predicate is only consulted when drift did not move.
func observedFactsChanged(before, after observedState) bool {
	if before.MgmtVLAN != after.MgmtVLAN {
		return true
	}
	if !maps.Equal(before.LinkUp, after.LinkUp) {
		return true
	}
	return !maps.Equal(before.Links, after.Links)
}
