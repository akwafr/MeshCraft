package api

import (
	"sort"
	"sync"
	"time"
)

// Correlation of port transitions reported by proxies.
//
// Decision 6 (docs/en/01-Architecture/Monitoring.md, "Controller-side processing"):
// the proxy establishes the FACT — this port went from up to down, and it holds
// that from an `ifOperStatus` poll, never from trap contents (cf. tunnel.EventPortUp)
// — the controller does the CORRELATION. This file is that correlation, and nothing
// else: it decides what must be logged; it writes nothing.
//
// The separation is the one the project already applies elsewhere (`snmpprov` decides
// / `snmpapply` applies; `netscope` filters / `watch_handlers` applies), and it has
// an extra reason here: the rules below are TIME rules. Proving them through a database
// and a tunnel would require actually waiting five minutes; proving them on a clock
// passed as a parameter takes a microsecond.
//
// # The two kinds of noise we filter, and why they are not filtered the same way
//
// 1. FLAPPING. A port that flaps (damaged cable, dying transceiver, duplex mismatch,
//    STP loop) produces dozens of real transitions. They are all accurate and the log
//    needs NONE of them: what an operator comes looking for is "this port has been
//    flapping since 14:12", not forty rows burying incidents from other sites. We
//    therefore aggregate into a `port.flapping` with a counter, and stay silent until
//    it calms down.
//
// 2. Inter-proxy DUPLICATES. The schema does not attach a device to a proxy:
//    `Store.DevicesWatchedBy` deduces eligibility from `proxy_sites` coverage PLUS
//    the `proxies.global` flag, and there are several global proxies in the database.
//    The same port is therefore observed by several proxies, each reporting the same
//    transition a few seconds apart. These are not two events; it is one event seen
//    twice — and counting it twice would also reach the flapping threshold twice as
//    fast.
//
// The second filter runs BEFORE the first: a duplicate must never feed the flapping
// counter.
//
// # One clock only: the CONTROLLER's
//
// All rules below are DIFFERENCES of timestamps — dupWindow, flapWindow, flapCalm,
// flapPurge. A difference only makes sense between two readings of the same clock,
// and correlation necessarily has two under its control: the observation arrives from
// a proxy; the end of flapping is detected on a controller ticker (cf. sweepPortFlaps).
//
// The first version dated the transition with `tunnel.Event.TS` — the PROXY clock,
// i.e. a machine deployed at a customer site that nothing synchronizes — and then
// compared it to the sweep's `time.Now()`. On the reference installation, that clock
// was measured ELEVEN SECONDS behind: the same skew that removed the proxy date from
// `trap_seen_at` (cf. store.ConfirmTrapPath).
//
// What that broke, without dramatizing: NOTHING observable. Windows are counted in
// minutes; eleven seconds shift them without inventing or losing events. What it
// really broke is elsewhere: the WRITTEN threshold was no longer the APPLIED
// threshold. "Three minutes of calm" was worth 2 min 49 s on that site and something
// else on the neighbor, and a proxy without NTP — which drifts in minutes, not
// seconds — would make detection erratic from one site to the next, with nothing
// flagging it. The `time.Now()` fallback when the proxy did not date an event was
// the same defect squared: two clocks cohabited in the SAME key, depending on
// whether the observation had been timestamped or not.
//
// So: the controller dates what it compares itself. `observe` and `sweep` both
// receive the instant as a parameter, `portTransition` carries no date anymore, and
// a threshold written here is worth the same across a fleet regardless of the state
// of the machines observing it.
//
// The clock remains a PARAMETER, and that is not a door left open: the rules to prove
// are counted in minutes; verifying them on hard-coded `time.Now()` would require
// actually waiting. A clock injected by a test is not a third party's clock — it is
// what makes tests deterministic. What is forbidden is a date FROM THE NETWORK entering
// here, and portevents_test.go locks the two places it could return: a date field on
// `portTransition`, or a production call passing anything other than `time.Now()`.
//
// The proxy date keeps one use, only one: the SKEW itself, logged as fleet diagnostics
// (cf. applyPortTransition). A fact about clock state, never an instant that would
// order itself with the controller's.
//
// # In memory, never in the database
//
// This is hot correlation over a window of a few minutes. A controller that reboots
// and starts from a blank state is CORRECT behavior: it will learn again that a port
// flaps on the fifth flap next time, and meanwhile it will have logged four real
// transitions. The inverse — persisting correlation state — would cost one write per
// flap in the database, at the exact moment the fleet is misbehaving.
//
// # The gap this leaves, and why we ACCEPT it
//
// This is the only port-transition logging path since batch E; the old one — a diff
// of telemetry against `devices.ports` on each metrics push — was removed (cf.
// proxymetrics_handlers.go). Yet the two did not diff against the same reference, and
// the difference has a consequence that must be written in black and white rather
// than discovered in a year:
//
//	the old path diffed against the DATABASE, which survives everything;
//	this one diffs against PROXY memory, which does not survive its reboot.
//
// So: a proxy that reboots has no reference state anymore; its first poll restores it
// silently ("first observation", not a transition), and a drop during its outage
// produces NO log row. The window equals the proxy outage duration.
//
// This is not recoverable on the proxy side without persisting its port state, and
// that is not worth the price. What is missing is framed:
//
//   - STATE stays correct. `applyPortTelemetry` still writes telemetry to the database
//     on the first poll, so the front panel shows the right link state as soon as the
//     proxy returns, outage or not.
//   - the MEASUREMENT is still there. `meshcraft_if_oper_status` is in VictoriaMetrics,
//     continuously and regardless of who rebooted: the fact is recoverable.
//   - it is the LOG LINE that is missing, and only that.
//
// The safety net one thinks of first — restoring a diff on the controller side on
// metrics push, limited to ports this file has no trace of — does not hold up, for
// three cumulative reasons:
//
//  1. "no trace here" does not distinguish "the proxy rebooted" from "this port has
//     not moved since controller startup". Correlation state is also in memory, and
//     it purges after flapPurge: after a CONTROLLER reboot, the whole fleet has no
//     trace. The safety net would then write one transition per port on the first
//     metrics batch — exactly the flood we just removed, and at the worst moment.
//  2. the metrics push carries no exploitable ifIndex as a key: it is indexed by a
//     label resolved to a front-panel name. Its rows could neither deduplicate across
//     proxies nor feed anti-flap — we would reintroduce a path that BYPASSES
//     correlation, which batch E exists to remove.
//  3. we would need a second in-memory index, by front-panel name this time, for the
//     sole fact of having already seen a port. Two correlation states for the same
//     fleet, desynchronized by construction.
//
// We therefore accept the gap. The day it must be filled, the fix is on the proxy
// side — persist its last known port state so it diffs against itself on reboot — and
// not on the controller side: that is where the missing information is.

// Correlation parameters. All here, all named: these are what we will revisit on the
// first production feedback, and hunting for them in the code body is the best way to
// forget one.
const (
	// flapThreshold: number of transitions kept on the SAME (device, ifIndex) in
	// flapWindow beyond which we stop logging one row per transition.
	//
	// 5, because no human action produces that many on the same port: unplugging a
	// workstation makes 1, replugging 2, a phone rebooting on its PoE 2, a tech
	// redoing a patch cord 2 or 3. From 5 within five minutes, it is no longer
	// someone handling a cable; it is a cable failing.
	flapThreshold = 5
	// flapWindow: the sliding window we count over.
	//
	// 5 minutes aligns with `if_state` polling cadence (30 to 60 s, cf.
	// docs/en/01-Architecture/Monitoring.md): about ten cycles, enough for real flapping to
	// show even when the only available channel is polling. Shorter, and the threshold
	// would become unreachable without traps — detection would only work on fleets
	// already reactive, i.e. those that need it least.
	flapWindow = 5 * time.Minute
	// flapCalm: silence required before declaring the end of flapping.
	//
	// 3 minutes, i.e. at least three polling cycles at the slowest (60 s). The trap is
	// here: with silence shorter than poll cadence, a port flapping MORE SLOWLY than we
	// observe would be declared calm between two seen transitions, then re-declared
	// flapping right after — the oscillation detector would oscillate, producing exactly
	// the noise it exists to remove.
	flapCalm = 3 * time.Minute
	// dupWindow: maximum gap between two observations of the SAME change by two
	// different proxies.
	//
	// 2 minutes = two polling cycles at the slowest. Deliberately generous: the
	// duplicate criterion (cf. observe) already requires the targeted state to match
	// the previous transition, which a real round trip never produces. Widening the
	// window therefore runs no risk of swallowing a real event, whereas narrowing it
	// would let through the duplicate of a proxy that just reconnected and rediscovers
	// fleet state.
	dupWindow = 2 * time.Minute
	// flapPurge: after how much silence a calm key is forgotten.
	//
	// Without purge, the structure grows by one key per port never seen again and never
	// shrinks: a leak, slow and therefore invisible until the day it is not. 30 minutes
	// keeps enough to deduplicate and detect slow flapping, and forgets everything else.
	flapPurge = 30 * time.Minute
	// maxPortKeys bounds the structure by number of keys.
	//
	// A fleet of 200 switches at 48 ports makes ~10,000 keys; 20,000 leaves double
	// margin without any realistic fleet hitting it. The bound is not for the nominal
	// fleet but for the degraded case: a proxy reporting aberrant ifIndexes (VLAN
	// interfaces, aggregates — on a 1930 there are up to 100,000) must not be able to
	// inflate controller memory.
	maxPortKeys = 20000
	// flapSweep: cadence of the sweep that closes finished flaps and purges.
	//
	// A flap end CANNOT be decided by an incoming event: by definition, it is detected
	// by ABSENCE of events. A clock is therefore required. 30 s: the flap exit row is
	// written at worst half a minute after the 3 minutes of calm, invisible at the
	// scale we read it.
	flapSweep = 30 * time.Second
)

// Decision kinds returned by correlation.
const (
	// portLogTransition: an ordinary transition, to log as-is.
	portLogTransition = "transition"
	// portLogFlapStart: this port started flapping — we write the aggregate, once.
	portLogFlapStart = "flap_start"
	// portLogFlapEnd: this port stopped flapping, in `Up` state.
	portLogFlapEnd = "flap_end"
)

// portTransition is a normalized port transition, ready to correlate.
//
// `ProxyID` does NOT enter the correlation key, and that is the whole point of
// deduplication: two proxies observing the same port observe the same port. It only
// serves the application log, to know who spoke first.
type portTransition struct {
	DeviceID string
	// IfIndex IDENTIFIES the port; PortName only illustrates it. `ifDescr` collides —
	// on a 1930, VLAN 42's interface is named "42" like physical port 42 — so nothing
	// correlates by name (cf. tunnel.Event).
	IfIndex int
	// PortName is kept to LABEL the event later, including at flap exit where no
	// incoming event supplies it anymore.
	PortName string
	Up       bool
	ProxyID  string
	// Trigger: "poll" or "trap" — what made us see the transition early, not where
	// truth comes from.
	Trigger string
	// No TIMESTAMP here, and that is the point of this structure: it carries what the
	// proxy ESTABLISHES, and the date it applies is not part of it (cf. "One clock
	// only" at the top of this file). The correlation instant is a parameter of
	// `observe`, read on the controller clock.
	//
	// The field existed, and it received `ev.TS`: a structure filled from `tunnel.Event`
	// attracts the event date like a magnet, and a warning comment that might have
	// accompanied it would not have prevented anything. As long as it does not exist,
	// nobody can put a third party's date in it.
}

// portDecision is what correlation asks to write. One transition produces zero,
// one, or exceptionally two (a flap end followed by the transition that woke it).
type portDecision struct {
	Kind     string
	DeviceID string
	IfIndex  int
	PortName string
	// Up: the state targeted by the transition, or the FINAL STABLE state for a flap
	// end. This is the distinction that matters most to an operator: a port that stopped
	// flapping in `down` is still down; a port that stopped flapping in `up` is fixed.
	// The log must say which of the two.
	Up bool
	// Count: aggregated transitions, for both flap kinds.
	Count int
	// Traps: how many observations were seen thanks to a trap SINCE aggregate OPENING.
	// This is the only way to confirm a fleet actually receives its traps, therefore
	// that 162/udp serves a purpose.
	//
	// Counted from opening, not from the first transition in the window: the trigger of
	// earlier transitions already went to the application log, one by one, and keeping
	// it here would require storing it beside each timestamp — memory per port for a
	// statistic.
	Traps int
	// Since: flap start.
	Since time.Time
	// Trigger: that of the returned transition ("" for a flap end, which is triggered
	// by no observation).
	Trigger string
	// At: the CONTROLLER instant at which the decision is returned — that of `observe`
	// or `sweep`. `Since` is read on the same clock, which is all that makes their
	// difference — the flap duration displayed by portEventOf — exact. When `Since`
	// came from the proxy and `At` from the sweep, that duration carried the customer
	// machine's drift.
	At time.Time
}

// portKey: what identifies a port stably across proxies.
type portKey struct {
	deviceID string
	ifIndex  int
}

// portFlapState is a port's correlation state.
type portFlapState struct {
	// stamps: timestamps of transitions KEPT in the window, capped at flapThreshold —
	// beyond that we are already flapping and count by counter, not by timestamp.
	stamps []time.Time
	// last / lastUp: the last kept transition. The pair serves deduplication (same
	// targeted state consecutively), calm detection, and purge.
	last     time.Time
	lastUp   bool
	seen     bool
	portName string
	flapping bool
	// count / traps / since: the current aggregate.
	count int
	traps int
	since time.Time
}

// portFlaps holds correlation state for the whole fleet.
//
// Concurrent by construction: `Hub.readEvents` starts one read goroutine PER PROXY,
// and that is exactly the case we care about — inter-proxy duplicates arrive from
// two different goroutines. Unprotected access would show up in production, not in
// development.
type portFlaps struct {
	mu     sync.Mutex
	states map[portKey]*portFlapState
}

func newPortFlaps() *portFlaps {
	return &portFlaps{states: map[portKey]*portFlapState{}}
}

// observe correlates a transition and returns what must be logged.
//
// # The duplicate criterion
//
// A transition is a duplicate if it targets THE SAME STATE as the previously kept
// transition on this key, less than dupWindow ago.
//
// The word that carries everything is "previously": the criterion does not compare to
// "a state seen recently" but to the LAST kept state. This saves legitimate
// round-trips — a down followed by an up then a down in ten seconds has an
// interleaved `up`, so each down is compared to an `up` and nothing is dropped. Conversely,
// two consecutive transitions toward the same state are impossible from a single
// observer: a proxy only reports CHANGES relative to its own state, so it must
// alternate. Twice the same state in a row means two observers, never two events.
//
// The duplicate is inert: it does not count toward flapping, and it does not push
// calm or purge horizons either — it is an echo; it proves no activity the original
// transition had not already proved.
//
// # `now`
//
// The instant where THE CONTROLLER learns of the transition: `time.Now()` in production,
// an explicit date in tests. It dates the transition, not the date the proxy applied
// to its event — cf. "One clock only" at the top of this file. The parameter is the
// same as `sweep`'s, and it must be: the two dates correlation subtracts come from
// here and there.
//
// No fallback to `time.Now()` when the instant is zero, and that is the corollary of
// the rest: a conditional fallback is exactly what made two clocks cohabit in the same
// key. A zero instant would be a caller bug, not data to recover.
func (t *portFlaps) observe(tr portTransition, now time.Time) []portDecision {
	// Without a device ID or ifIndex, there is nothing to correlate: port name cannot
	// serve as a fallback key, since it collides.
	if t == nil || tr.DeviceID == "" || tr.IfIndex <= 0 {
		return nil
	}
	var key = portKey{deviceID: tr.DeviceID, ifIndex: tr.IfIndex}

	t.mu.Lock()
	defer t.mu.Unlock()

	var st = t.states[key]
	if st == nil {
		if !t.makeRoomLocked(now) {
			// Structure saturated with keys we cannot sacrifice — they are ALL flapping.
			// Return the transition as-is, without tracking it: noisy but true. This is
			// the only acceptable fallback, because the other — letting the structure
			// grow — leads to a controller running out of memory, and a dead controller
			// logs nothing at all.
			return []portDecision{{
				Kind: portLogTransition, DeviceID: key.deviceID, IfIndex: key.ifIndex,
				PortName: tr.PortName, Up: tr.Up, Trigger: tr.Trigger, At: now,
			}}
		}
		st = &portFlapState{}
		t.states[key] = st
	}
	// The label is refreshed on each pass: it is what the last observation returned,
	// and a rename on the device must show up in the next log row.
	if tr.PortName != "" {
		st.portName = tr.PortName
	}

	if st.seen && st.lastUp == tr.Up && now.Sub(st.last) < dupWindow {
		return nil
	}

	var out []portDecision
	// A flap may have ended without anyone having noticed yet: the transition arriving
	// after a long silence proves the silence happened. Close the aggregate BEFORE
	// processing the transition, otherwise the flap end would read in the log after the
	// next flap restart.
	if st.flapping && now.Sub(st.last) >= flapCalm {
		out = append(out, t.endFlapLocked(key, st, st.last))
	}

	st.seen = true
	st.lastUp = tr.Up
	st.last = now

	if st.flapping {
		// Stay silent: that is the whole point of the aggregate. Writing individual
		// transitions anyway would mean paying for correlation for nothing.
		st.count++
		if tr.Trigger == "trap" {
			st.traps++
		}
		return out
	}

	st.stamps = append(st.stamps, now)
	st.stamps = trimStamps(st.stamps, now)
	if len(st.stamps) < flapThreshold {
		return append(out, portDecision{
			Kind: portLogTransition, DeviceID: key.deviceID, IfIndex: key.ifIndex,
			PortName: st.portName, Up: tr.Up, Trigger: tr.Trigger, At: now,
		})
	}

	// Threshold reached: this transition is NOT logged alone; it becomes the first row
	// of the aggregate. The previous flapThreshold-1 were already written individually,
	// and that is intentional — they were indistinguishable from an operator action when
	// they arrived.
	st.flapping = true
	st.since = st.stamps[0]
	st.count = len(st.stamps)
	st.traps = 0
	if tr.Trigger == "trap" {
		st.traps = 1
	}
	st.stamps = nil
	return append(out, portDecision{
		Kind: portLogFlapStart, DeviceID: key.deviceID, IfIndex: key.ifIndex,
		PortName: st.portName, Up: tr.Up, Count: st.count, Traps: st.traps,
		Since: st.since, Trigger: tr.Trigger, At: now,
	})
}

// sweep closes flaps that have calmed down and purges forgettable keys.
//
// Called by a clock, because a flap end is detected by absence of events. Returning
// decisions in STABLE order is not vanity: two ports on the same device calming down
// together must read in the same order from one time to the next, otherwise the log
// becomes impossible to compare with itself.
func (t *portFlaps) sweep(now time.Time) []portDecision {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	var out []portDecision
	var key portKey
	var st *portFlapState
	for key, st = range t.states {
		if st.flapping && now.Sub(st.last) >= flapCalm {
			out = append(out, t.endFlapLocked(key, st, st.last))
		}
		// Never a key that is STILL flapping: forgetting it would mean never writing a
		// flap exit and announcing a new one on the next flap.
		//
		// The order of the two tests means a key silent beyond both horizons — a sweep
		// that missed its turn, a suspended controller — is closed THEN forgotten in
		// the same pass. That is the right result, not a shortcut: the exit is already
		// in `out`, and what remains of the key only serves deduplication, which does
		// not look thirty minutes back.
		if !st.flapping && now.Sub(st.last) >= flapPurge {
			delete(t.states, key)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].DeviceID != out[j].DeviceID {
			return out[i].DeviceID < out[j].DeviceID
		}
		return out[i].IfIndex < out[j].IfIndex
	})
	return out
}

// endFlapLocked closes a key's aggregate. Called with lock held.
//
// The last kept transition's state is preserved (`lastUp`, `last`): it remains the
// deduplication reference, and it IS the stable state we announce.
func (t *portFlaps) endFlapLocked(key portKey, st *portFlapState,
	at time.Time) portDecision {
	var d = portDecision{
		Kind: portLogFlapEnd, DeviceID: key.deviceID, IfIndex: key.ifIndex,
		PortName: st.portName, Up: st.lastUp, Count: st.count, Traps: st.traps,
		Since: st.since, At: at,
	}
	st.flapping = false
	st.count, st.traps = 0, 0
	st.since = time.Time{}
	st.stamps = nil
	return d
}

// makeRoomLocked guarantees the memory bound before adding a key. Returns false when
// there is nothing to sacrifice. Called with lock held.
//
// Three passes, from least to most destructive, and each frees a SLICE, not one key.
// The detail matters: evicting a single key per insertion would make every new port
// pay a full structure walk, i.e. quadratic cost introduced by the very mechanism meant
// to protect memory. Freeing a tenth of the bound at once amortizes the walk over two
// thousand insertions.
func (t *portFlaps) makeRoomLocked(now time.Time) bool {
	if len(t.states) < maxPortKeys {
		return true
	}
	var slack = maxPortKeys / 10
	// 1. Keys we would have purged anyway on the next sweep.
	// 2. Calm keys, which only served deduplication — abandoning them may log one extra
	//    echo, which is incomparable to losing tracking of a flapping port.
	t.dropLocked(now, flapPurge, 0)
	if len(t.states)+slack <= maxPortKeys {
		return true
	}
	t.dropLocked(now, flapCalm, 0)
	if len(t.states)+slack <= maxPortKeys {
		return true
	}
	// 3. Pure degraded case: the bound is hit by ALL-recent keys. No realistic fleet
	// does this; a proxy reporting aberrant ifIndexes does very well. Sacrifice a slice
	// of arbitrary non-flapping keys — at this stage, WHICH one leaves matters infinitely
	// less than staying bounded, and picking "the oldest" would cost a sort for zero
	// gain since they all have the same age to the second.
	t.dropLocked(now, 0, slack)
	return len(t.states) < maxPortKeys
}

// dropLocked deletes keys that are NOT flapping and silent for at least `age`. `limit`
// 0 = no limit. Called with lock held.
//
// A flapping key is never sacrificed, whatever the memory pressure: it is the only
// one carrying information we cannot reconstruct — an ongoing flap counter and the
// obligation to write its exit.
func (t *portFlaps) dropLocked(now time.Time, age time.Duration, limit int) {
	var dropped int
	var key portKey
	var st *portFlapState
	for key, st = range t.states {
		if st.flapping || now.Sub(st.last) < age {
			continue
		}
		delete(t.states, key)
		dropped++
		if limit > 0 && dropped >= limit {
			return
		}
	}
}

// trimStamps keeps only timestamps still in the window, and at most flapThreshold:
// beyond that we are flapping, and a slice that grew with transition count would be a
// leak disguised as a counter.
func trimStamps(stamps []time.Time, now time.Time) []time.Time {
	var cut = now.Add(-flapWindow)
	var i int
	for i = 0; i < len(stamps); i++ {
		if stamps[i].After(cut) {
			break
		}
	}
	stamps = stamps[i:]
	if len(stamps) > flapThreshold {
		stamps = stamps[len(stamps)-flapThreshold:]
	}
	return stamps
}
