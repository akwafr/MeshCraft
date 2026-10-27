package main

import (
	"context"
	"errors"
	"log/slog"
	"math/rand"
	"sort"
	"strings"
	"sync"
	"time"

	"meshcraft/internal/gnmipoll"
	"meshcraft/internal/metricbuf"
	"meshcraft/internal/snmppoll"
	"meshcraft/internal/tunnel"
)

// Streaming telemetry collection, executed by the PROXY.
//
// The counterpart of snmp.go, and the differences are worth stating up front
// because almost everything about the SHAPE of the work changes even though the
// output does not:
//
//   - there is no poll loop. `pollDue` wakes every second and asks what is due;
//     here, a subscription is opened once and the DEVICE decides when to send.
//     The cadence is a parameter of the subscribe call and cannot be changed
//     without re-subscribing;
//   - a goroutine per (device, metric) rather than a bounded burst of jobs. They
//     are long-lived and mostly idle — blocked on a channel — where an SNMP job
//     is short and CPU-bound in bursts. The 16-way semaphore that protects a
//     proxy's NAT table from a fleet-wide poll would be actively wrong here: it
//     would cap the number of devices that can be SUBSCRIBED at sixteen;
//   - failure is a stream that ends, not a request that times out. So the whole
//     reliability story is the supervisor below.
//
// # WHAT IT SHARES WITH THE SNMP COLLECTOR, ON PURPOSE
//
// The metric BUFFER and the event emitter, both handed in rather than created
// here. internal/gnmipoll's header states the rule: the proxy already has exactly
// one buffer, with a Take/Ack cycle giving at-least-once delivery and ONE
// saturation counter. A second producer with a second buffer would give two drop
// counters, and a monitoring stack that drops samples somewhere its own
// saturation report does not mention is a monitoring stack that lies.
//
// The LLDP push is shared for the same reason — one route, one throttle, one
// snapshot shape (cf. cmd/proxy/lldp.go).

// gnmiCollector is the proxy's instance. Nil until the proxy is enrolled, same
// as snmpCollector: with no identity there is nobody to push to.
var gnmiCollector *GNMICollector

// errNoGNMIStream: a Subscriber returned neither a stream nor an error, which
// its own contract forbids. Classified as tunnel.CauseUnknown like any other
// unreadable failure — it is a meshcraft bug, not a fact about the device, and
// filing it as one would be the guess this batch refuses to make elsewhere.
var errNoGNMIStream = errors.New("gnmi subscribe returned neither a stream nor an error")

// Backoff bounds for a subscription that will not stay up.
//
// A subscription ends for ordinary reasons — the switch reboots, the management
// VLAN moves, someone saves a config that bounces the gNMI agent — and the
// collector must come back without hammering. The ceiling is a minute because
// that is roughly how long a switch takes to finish rebooting: longer would make
// a routine reload look like a long outage on the graphs, shorter would keep
// dialling a box that is still coming up.
const (
	gnmiBackoffMin = 2 * time.Second
	gnmiBackoffMax = time.Minute
)

// gnmiFailReportAfter is how long ONE subscription must have been failing
// CONTINUOUSLY before the proxy tells the controller about it.
//
// # Why there is a threshold at all
//
// A subscription ends for ordinary reasons all day: the switch reboots, someone
// saves a config and the gNMI agent bounces, the management VLAN moves. Every
// one of those is a failed resubscribe on the next attempt, and reporting the
// first one would put an alarm in the fleet log for every `write memory` on
// every Arista. The condition worth reporting is not a failure, it is a
// SUBSCRIPTION THAT HAS STOPPED COMING BACK.
//
// # Why five minutes
//
// gnmiBackoffMax above already fixes the reference: a minute is roughly how long
// a switch takes to finish rebooting, which is why the retry ceiling sits there.
// A reboot costs more than that end to end, though — the box answers before AAA
// and the gNMI agent are serving — so the threshold has to clear a reboot with
// room, not merely match it. Five times the ceiling does: every ordinary
// interruption on this fleet resolves well inside it, and each one that resolves
// resets the clock, so they never accumulate into a report.
//
// The other bound is the incident. core01.lab was dark for over an hour with
// nothing said; at five minutes the fleet log would have carried it while the
// person who rotated the credential was still at their desk. Anything much
// shorter buys minutes and pays for them in false alarms — which are worse than
// useless here, because an alarm that cries wolf on every reboot is one an
// operator learns to filter, and then the real one is invisible again.
//
// # Why a duration and not a number of attempts
//
// Counting attempts would make the threshold a function of the backoff curve:
// the same "five failures" is ten seconds at gnmiBackoffMin and five minutes at
// the cap, so tuning the backoff would silently retune the alarm. The attempt
// count is still reported — it says how hard the proxy tried — but it decides
// nothing.
const gnmiFailReportAfter = 5 * time.Minute

// lldpPushEvery is how often an accumulated neighbour table is pushed.
//
// A STREAM STILL NEEDS A TIMER, and the reason is not obvious. The subscription
// is ON CHANGE, so the table only moves when a cable does — but the controller's
// snapshot semantics are "the whole table, as of now", and its freshness is
// displayed. Pushing only on change would leave `last_seen` at the date of the
// last re-cabling, which on a stable site is months ago and reads as a dead
// collector.
//
// 30 s matches what the SNMP poller's LLDP cadence was, so the controller sees
// the same freshness from both collectors and nothing downstream has to know
// which one produced a snapshot.
const lldpPushEvery = 30 * time.Second

// GNMICollector holds one subscription per (device, metric).
type GNMICollector struct {
	mu      sync.Mutex
	watches []tunnel.GNMIWatch
	// subs: the running subscriptions, keyed "deviceID|metric". See gnmiSub for
	// why the value is not just the cancel function.
	subs map[string]gnmiSub
	// ports: last observed oper-status per device, per PORT NAME.
	//
	// BY NAME AND NOT BY ifIndex, which is the one structural difference from
	// snmp.go's `portStates`. gNMI names an interface by its OpenConfig key and
	// offers no interface index at all; internal/metricbuf/names.go makes that
	// absence load-bearing — a sample with no `iftype` can feed a curve and can
	// never create a port. A side lookup to invent an index would give this
	// collector the power to change inventory, which is exactly the rule that
	// exists to prevent.
	ports map[string]map[string]float64
	// tables: the accumulated LLDP neighbour state, per device.
	tables map[string]*gnmipoll.Table
	// reach: gNMI collection reachability, mirroring snmpReach.
	reach map[string]*gnmiReachState
	// fails: per-SUBSCRIPTION failure state, keyed "deviceID|metric" exactly like
	// `subs`.
	//
	// Per subscription and not per device, unlike `reach`, because that is the
	// granularity at which the fact is true: a device's four subscriptions are
	// four streams with four independent fates, and a `health` set that a
	// platform does not serve must not make its working `if_counters` stream look
	// broken.
	fails map[string]*gnmiFailState

	// buf and emit are BORROWED from the SNMP collector. See the file header.
	buf  *metricbuf.Buffer
	emit func(tunnel.Event)
	// pushLLDP is the shared snapshot route.
	pushLLDP func(context.Context, snmppoll.LLDPReport)
	// sub is the transport, injectable so the whole collector runs in a test with
	// no wire — the same seam internal/gnmipoll exists behind.
	sub gnmipoll.Subscriber
}

// NewGNMICollector builds the collector around the buffer and routes that
// already exist. It creates neither.
func NewGNMICollector(buf *metricbuf.Buffer,
	pushLLDP func(context.Context, snmppoll.LLDPReport)) *GNMICollector {

	return &GNMICollector{
		subs:     map[string]gnmiSub{},
		ports:    map[string]map[string]float64{},
		tables:   map[string]*gnmipoll.Table{},
		reach:    map[string]*gnmiReachState{},
		fails:    map[string]*gnmiFailState{},
		buf:      buf,
		pushLLDP: pushLLDP,
		sub:      gnmipoll.Transport(),
	}
}

// SetEmitter wires the event output. Same channel as everything else — see
// cmd/proxy/trap.go for why a second one would only add an ordering to arbitrate.
func (c *GNMICollector) SetEmitter(fn func(tunnel.Event)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.emit = fn
}

// gnmiReachState mirrors snmpReachState: has this device's telemetry been
// arriving?
type gnmiReachState struct {
	up *bool
	ip string
}

// gnmiFailState is what one subscription's current run of failures looks like.
//
// It exists to answer one question — has this been failing long enough to be a
// STATE rather than a blip? — and holds only what that question and its report
// need. In particular it holds NO error and no error text: the classification is
// kept, the error itself is not, so there is no field here through which a gRPC
// message could ever carry a credential into an event. Cf. gnmiFailingEvent.
type gnmiFailState struct {
	// since: the first failure of the CURRENT run. Zero means not failing.
	//
	// The run is what matters, not the total: one success resets it, so a device
	// that reboots every night never accumulates its nights into a report.
	since time.Time
	// attempts: consecutive failed attempts since `since`. REPORTED, never
	// decisive — cf. gnmiFailReportAfter on why the threshold is a duration.
	attempts int
	// cause: the class of the LATEST failure, in the wire vocabulary
	// (tunnel.Cause*). The latest and not the first, because a device that is
	// unreachable and then refuses the login is now an authentication problem,
	// and the report should say what is true now.
	cause string
	// reported: the controller has been told about THIS run. What makes the
	// report a transition instead of a line every retry — the difference between
	// an event and a metric.
	reported bool
}

// gnmiSub is a running subscription AND the parameters it was opened with.
//
// # Why the parameters are kept at all
//
// Stopping a subscription is cancelling it, so a cancel function used to be the
// whole value. That made SetWatches reconcile EXISTENCE and nothing else: a key
// already present was left alone whatever had changed underneath it, and there
// was nowhere to compare against even if it had wanted to.
//
// The cost was paid on core01.lab on 2026-08-10. The site's `gnmi` credential
// was rotated on the device, the corrected password was pushed down in a fresh
// watch, the controller logged `gnmi_targets:1` — and the proxy saw the key
// running and kept the OLD goroutine, retrying the old password until it sat at
// the 60 s backoff cap. Telemetry was dark for over an hour, a controller restart
// changed nothing (the proxy holds this map, not the controller), and there was
// no line anywhere saying a re-push had been absorbed and discarded.
//
// A subscription cannot be re-parameterised in place — the cadence and the
// credential both travel in the subscribe call, and [tunnel.GNMIWatch] says so:
// the proxy "cannot poll faster, it can only re-subscribe". So the only way to
// apply a changed parameter is to tear the stream down and open a new one, and
// the only way to know one changed is to have kept the old.
type gnmiSub struct {
	// cancel ends the supervisor's context, which is the entire teardown.
	cancel context.CancelFunc
	// target is what was dialled: address, login and TLS trust, exactly as
	// targetOfGNMI built it. Compared as a WHOLE (it is a comparable struct), so
	// a field added to gnmipoll.Target enters this comparison by itself rather
	// than being silently ignored the way every field was until now.
	target gnmipoll.Target
	// interval is the cadence this ONE subscription actually puts on the wire —
	// intervalOf's answer for this key's metric, not the four numbers in the
	// watch. See gnmiParamsMoved for why that distinction is the whole design.
	interval time.Duration
}

// gnmiParamsMoved names the CLASSES of parameter that differ between a running
// subscription and the one now wanted, or "" when nothing that reaches the device
// has changed.
//
// # What is compared, and what is deliberately not
//
// Everything in gnmipoll.Target is compared, because every field of it is an
// argument to the dial or to the authentication that follows it: a changed IP or
// port talks to a different box, a changed login is this incident, and a changed
// Plaintext/SkipVerify/CAPEM changes whether the login crosses the management
// network in the clear. None of the three can be applied to a live gRPC stream.
//
// The cadence is compared as `intervalOf(w, metric)` and NOT as the four interval
// fields of the watch, and that is the one judgement call in this function.
// `if_state` and `lldp` are subscribed ON CHANGE: their number in the watch is
// read as a boolean by metricsOf and never reaches the wire (cf.
// gnmipoll.SubscriptionsFor, which ignores the interval for the on-change sets).
// Comparing the raw fields would therefore restart a device's `if_state` and
// `lldp` streams because its `if_counters` cadence moved from 5 s to 10 s —
// resetting an accumulated LLDP neighbour table (runLLDP calls Reset on every
// resubscribe) and re-issuing a full initial-state dump, to apply a change that
// those two subscriptions cannot express. That is exactly the fleet-wide churn
// the reconcile in SetWatches exists to avoid, and it would have been reintroduced
// by the fix for it.
//
// Going to zero is not handled here and does not need to be: zero means DO NOT
// COLLECT, so metricsOf drops the metric, the key never enters `want`, and the
// stop loop cancels it — which is the correct outcome and already the behaviour.
//
// # Do all of them deserve a restart?
//
// They do, but not for the same reason, and it is worth separating the two:
//
//   - a target change is MANDATORY. Left alone, the stream never recovers on its
//     own — that is this incident, and no amount of backoff fixes a wrong
//     password;
//   - a cadence change is merely CORRECT. The old stream keeps working, just at
//     the cadence the operator no longer wants. It is still applied, because
//     there is no cheaper way to apply it and because a resolved cadence that
//     silently does nothing is precisely the bug internal/gnmipoll's own header
//     warns about (SubscriptionsFor hard-coded 30 s for a while, and the only
//     symptom was that changing a connector's declared floor changed nothing).
//
// What keeps the second from being expensive is the per-KEY interval above: an
// interval change restarts the one metric whose wire cadence it alters, and the
// device's other subscriptions are not touched.
//
// The result is a comma-joined list of class names — never a value, and above all
// never the password. See the caller.
func gnmiParamsMoved(cur, next gnmiSub) string {
	var moved []string
	if cur.target.IP != next.target.IP || cur.target.Port != next.target.Port {
		moved = append(moved, "address")
	}
	if cur.target.Username != next.target.Username ||
		cur.target.Password != next.target.Password {
		moved = append(moved, "credential")
	}
	if cur.target.Plaintext != next.target.Plaintext ||
		cur.target.SkipVerify != next.target.SkipVerify ||
		cur.target.CAPEM != next.target.CAPEM {
		moved = append(moved, "transport")
	}
	if cur.interval != next.interval {
		moved = append(moved, "interval")
	}
	// The catch-all, and it is not defensive padding. A field added to
	// gnmipoll.Target and not named above would change the dial and go unnoticed —
	// the same shape of silence this whole file is a fix for. Struct equality
	// catches it, and the vague class name is the signal that this list needs a
	// line.
	if len(moved) == 0 && cur.target != next.target {
		moved = append(moved, "target")
	}
	return strings.Join(moved, ",")
}

// SetWatches replaces the subscription list.
//
// RECONCILES rather than restarting everything, and that is not an optimisation.
// A WatchConfig is re-pushed on every controller-side change — a device renamed,
// a prefix added — and tearing down every subscription each time would drop the
// accumulated LLDP tables and re-issue a full initial-state dump per device, on a
// fleet, for a change that touched none of them.
//
// It reconciles PARAMETERS as well as existence, and for a long time it did not:
// a key already running was left alone whatever had changed under it, so a
// rotated password was absorbed and thrown away. gnmiSub carries that history and
// gnmiParamsMoved carries the comparison.
func (c *GNMICollector) SetWatches(ctx context.Context, w []tunnel.GNMIWatch) {
	c.mu.Lock()
	c.watches = w

	// What SHOULD be running, and with which parameters.
	var want = map[string]tunnel.GNMIWatch{}
	var watched = map[string]bool{}
	var one tunnel.GNMIWatch
	for _, one = range w {
		watched[one.DeviceID] = true
		if one.Username == "" || one.Password == "" {
			// Not subscribed, and said ONCE per push rather than per retry. A
			// watch with no credential is a controller-side gap — no `gnmi` site
			// credential resolved for this device — and it is invisible otherwise:
			// the device simply never streams, and the graph is empty with nothing
			// to explain it.
			slog.Warn("gnmi watch carries no device login: not subscribing",
				"device", one.DeviceID, "ip", one.IP)
			continue
		}
		var metric string
		for metric = range metricsOf(one) {
			want[one.DeviceID+"|"+metric] = one
		}
	}

	// Stop what is no longer wanted. Covers a device dropped from the watch, a
	// metric switched off (its interval set to zero), and a watch whose credential
	// went away — that last one is filtered out above, so its key is absent from
	// `want` and it is stopped here rather than left streaming with the login the
	// controller has just stopped vouching for.
	var key string
	for key = range c.subs {
		var _, keep = want[key]
		if keep {
			continue
		}
		c.subs[key].cancel()
		delete(c.subs, key)
		// The failure state goes with it. A subscription that is no longer wanted
		// cannot still be failing, and keeping its clock running would let a
		// device that comes back into the watch list be reported instantly on the
		// strength of an outage that happened while nobody was collecting it.
		delete(c.fails, key)
	}
	// Start what is missing, and RESTART what is running on parameters that no
	// longer match.
	for key = range want {
		var w2 = want[key]
		var metric = key[len(w2.DeviceID)+1:]
		var next = gnmiSub{
			target:   targetOfGNMI(w2),
			interval: intervalOf(w2, metric),
		}
		var cur, running = c.subs[key]
		if running {
			var moved = gnmiParamsMoved(cur, next)
			if moved == "" {
				// The property the reconcile defends, and the reason this is a
				// comparison and not an unconditional restart: an unrelated re-push
				// — a device renamed, a prefix added — must leave a healthy
				// subscription alone, with its accumulated LLDP table and without a
				// fleet-wide initial-state dump.
				continue
			}
			// Said ONCE per push, on the same principle as the "not subscribing"
			// warning above: this is the event whose absence made core01.lab's
			// outage unreadable for an hour. A resubscribe is visible from the
			// outside as a gap in the curves and as a fresh initial-state dump, and
			// without this line there is nothing to attribute either to.
			//
			// `changed` carries the CLASS of parameter and never a value. The one
			// field this file must never put in a log is the password, and "the new
			// password's length" or "its first characters" are not exceptions —
			// journals get shipped off-box, and a device login on this fleet
			// configures the switch rather than merely reading it (cf.
			// tunnel.GNMIWatch). The class is what a reader needs anyway: it says
			// which controller-side change to go and look at.
			slog.Info("gnmi subscription parameters changed: resubscribing",
				"device", w2.DeviceID, "ip", w2.IP, "metric", metric, "changed", moved)
			cur.cancel()
			// The failure clock RESTARTS with the subscription, and that is a
			// judgement worth naming because it cuts both ways. A push that
			// changes the credential is the operator fixing exactly this
			// condition, and holding the old clock would report a fault five
			// minutes into a repair that already worked. The cost is that a
			// re-push for an unrelated reason — a cadence edit — also defers the
			// report of a genuinely broken login by another five minutes. That is
			// the acceptable direction: a watch push is a deliberate change to
			// what this subscription IS, so the run of failures that came before
			// it was a run of failures of something else.
			delete(c.fails, key)
		}
		var subCtx, cancel = context.WithCancel(ctx)
		next.cancel = cancel
		c.subs[key] = next
		go c.supervise(subCtx, w2, metric)
	}

	// A device removed from the watch loses its port states, WITHOUT an event —
	// an operational decision is not an outage. Same rule as SNMPCollector's, and
	// for the extra reason stated there: keeping them would make its
	// reintegration diff against a state days old and invent a transition.
	var id string
	for id = range c.ports {
		if !watched[id] {
			delete(c.ports, id)
		}
	}
	for id = range c.tables {
		if !watched[id] {
			delete(c.tables, id)
		}
	}
	for id = range c.reach {
		if !watched[id] {
			delete(c.reach, id)
		}
	}
	c.mu.Unlock()
}

// metricsOf says which subscription sets a watch asks for.
//
// Zero means DO NOT COLLECT, exactly as in SNMPWatch, and that rule is what lets
// the controller turn one metric off per platform without a protocol change.
func metricsOf(w tunnel.GNMIWatch) map[string]bool {
	var out = map[string]bool{}
	if w.StateIntervalS > 0 {
		out[gnmipoll.MetricIfState] = true
	}
	if w.CountersIntervalS > 0 {
		out[gnmipoll.MetricIfCounters] = true
	}
	if w.HealthIntervalS > 0 {
		out[gnmipoll.MetricHealth] = true
	}
	if w.LLDPIntervalS > 0 {
		out[gnmipoll.MetricLLDP] = true
	}
	if w.OpticalIntervalS > 0 {
		out[gnmipoll.MetricOptical] = true
	}
	return out
}

// intervalOf picks the cadence for one metric set.
func intervalOf(w tunnel.GNMIWatch, metric string) time.Duration {
	switch metric {
	case gnmipoll.MetricIfCounters:
		return time.Duration(w.CountersIntervalS) * time.Second
	case gnmipoll.MetricHealth:
		return time.Duration(w.HealthIntervalS) * time.Second
	case gnmipoll.MetricOptical:
		return time.Duration(w.OpticalIntervalS) * time.Second
	}
	// if_state and lldp are ON CHANGE: there is no cadence to send, and the
	// number in the watch is read as a boolean by metricsOf.
	return 0
}

// supervise keeps one subscription up until the context ends.
//
// # WHY A STREAM NEEDS THIS AND A POLL DOES NOT
//
// A poll that fails costs one cycle: the next tick simply tries again, and the
// loop is the retry. A subscription that ends stays ended — nothing re-opens it —
// so without this loop a single switch reboot would silently stop that device's
// telemetry for ever, with a healthy-looking proxy and no error anywhere. That is
// the failure mode this whole file is arranged around.
func (c *GNMICollector) supervise(ctx context.Context, w tunnel.GNMIWatch, metric string) {
	var key = w.DeviceID + "|" + metric
	var backoff = gnmiBackoffMin
	for {
		if ctx.Err() != nil {
			return
		}
		var err = c.runOnce(ctx, w, metric)
		if ctx.Err() != nil {
			return
		}
		// CLASSIFIED before it is branched on, not tested for nil. A stream that
		// ended with io.EOF is an error value and an ordinary end; one that ended
		// with codes.Unauthenticated is an error value and a state. Only
		// gnmipoll.ClassifyEnd tells them apart, and putting it here means the two
		// branches below cannot drift from what the transport actually reported.
		var kind = gnmipoll.ClassifyEnd(err)
		var report *tunnel.Event
		// `time.Now()` is read at the PRODUCTION call site and written literally,
		// which is the doctrine internal/api/portevents.go states: the threshold
		// below is counted in minutes and a test that had to wait five of them
		// would not be written.
		if kind == gnmipoll.EndClean {
			// The stream ended cleanly. Still a reason to back off before
			// re-subscribing — a device that closes immediately would otherwise
			// spin — but reset the delay, because a stream that ran is evidence
			// the target is healthy.
			backoff = gnmiBackoffMin
			report = c.noteSubHealthy(key, w, metric, time.Now())
		} else {
			c.noteReach(w.DeviceID, w.IP, false)
			// `cause` and never the credential. The error text is a gRPC status
			// message and this line already existed with it at Debug; the CLASS is
			// what makes the line readable, and it is also the only part of the
			// failure that leaves this machine (cf. gnmiFailingEvent).
			slog.Debug("gnmi subscription failed", "device", w.DeviceID, "ip", w.IP,
				"metric", metric, "cause", string(kind), "err", err.Error(),
				"retry_in", backoff.String())
			report = c.noteSubFailing(key, w, metric, kind, time.Now())
		}
		if report != nil {
			c.emitEvent(*report)
		}
		// JITTERED, and this is not decoration. A proxy watching forty Aristas
		// through one switch stack reboots them all at once from its point of
		// view; without jitter all forty supervisors would re-dial in the same
		// millisecond, every time, for as long as the outage lasts.
		var wait = backoff + time.Duration(rand.Int63n(int64(backoff/2+1))) //nolint:gosec
		var timer = time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		backoff *= 2
		if backoff > gnmiBackoffMax {
			backoff = gnmiBackoffMax
		}
	}
}

// gnmiCauseOf maps the transport's classification onto the WIRE vocabulary.
//
// Explicit rather than a string conversion. The two vocabularies spell the three
// classes the same way today and there is no reason they must: gnmipoll's is
// about a gRPC status, tunnel's is a protocol between two binaries that are
// deployed separately and upgraded on different days. A rename on either side
// must break here, at compile time or in a test, rather than quietly change what
// the controller receives.
//
// The default is tunnel.CauseUnknown, which is what makes a class ADDED to
// gnmipoll arrive as "I could not tell" on a proxy that predates it rather than
// as silence.
func gnmiCauseOf(kind gnmipoll.EndKind) string {
	switch kind {
	case gnmipoll.EndAuth:
		return tunnel.CauseAuth
	case gnmipoll.EndUnreachable:
		return tunnel.CauseUnreachable
	}
	return tunnel.CauseUnknown
}

// gnmiFailDue: has this run of failures lasted long enough to be a STATE?
//
// PURE, and separated from the bookkeeping for the reason the whole batch is
// arranged this way: this is the decision, it is counted in minutes, and a test
// that had to spend five of them to exercise it would never be run. Cf.
// judgeProxyScope and scopeSaysNow in internal/api, same shape and same reason.
//
// `reported` is checked HERE and not at the call site so that the "at most once
// per run" property is a property of the decision rather than of the caller
// remembering.
func gnmiFailDue(st gnmiFailState, now time.Time, after time.Duration) bool {
	if st.reported || st.since.IsZero() {
		return false
	}
	return !now.Before(st.since.Add(after))
}

// gnmiFailingEvent builds the report. PURE, and THE place the credential rule is
// enforced.
//
// It takes a tunnel.GNMIWatch — which carries Username and Password — and reads
// DeviceID and IP from it and nothing else. That is deliberate and it is the
// reason this function exists at all instead of the event being assembled inline
// at three call sites: the rule "a gNMI login on this fleet configures the
// switch, so it never reaches a log or a record, not even truncated" is easy to
// state and easy to forget, and here it is checkable in one place by reading
// eighteen lines. There is a test that reads it the other way, from the outside
// (cf. gnmi_failreport_test.go).
//
// No error text either, for the same reason one step further out: the text is a
// gRPC status message from firmware this project does not own, and "the device
// never echoes the password back" is an assumption about somebody else's
// software. The CLASS is what an operator needs anyway — it names the gesture —
// and the class cannot carry a secret because it is drawn from a closed set.
func gnmiFailingEvent(w tunnel.GNMIWatch, metric string, st gnmiFailState,
	now time.Time) tunnel.Event {

	return tunnel.Event{
		Type: tunnel.EventGNMIFailing, TS: now.UTC(),
		DeviceID: w.DeviceID, IP: w.IP,
		Metric: metric, Cause: st.cause,
		// Misses reads as "consecutive failures" here, which is what it already
		// means on the SNMP path (applySNMPState says "consecutive poll
		// failures"). Reusing it beats a third field spelling the same idea.
		Misses: st.attempts,
		Detail: "failing for " + now.Sub(st.since).Round(time.Second).String(),
	}
}

// gnmiRecoveredEvent is the counterpart, and is emitted ONLY when a failing
// report went out — see noteSubHealthy.
func gnmiRecoveredEvent(w tunnel.GNMIWatch, metric string, st gnmiFailState,
	now time.Time) tunnel.Event {

	return tunnel.Event{
		Type: tunnel.EventGNMIRecovered, TS: now.UTC(),
		DeviceID: w.DeviceID, IP: w.IP,
		Metric: metric,
		Detail: "was failing for " + now.Sub(st.since).Round(time.Second).String(),
	}
}

// noteSubFailing records one failed attempt and returns the event to report, or
// nil — which is the ordinary answer, since the whole point of the threshold is
// that almost every failure is reported by staying silent.
//
// It returns the event rather than emitting it so that the emit happens outside
// the lock (the shape noteReach already uses) and so that a test can drive the
// threshold on an injected clock without an event channel.
func (c *GNMICollector) noteSubFailing(key string, w tunnel.GNMIWatch, metric string,
	kind gnmipoll.EndKind, now time.Time) *tunnel.Event {

	c.mu.Lock()
	var st = c.fails[key]
	if st == nil {
		st = &gnmiFailState{}
		c.fails[key] = st
	}
	if st.since.IsZero() {
		st.since = now
	}
	st.attempts++
	st.cause = gnmiCauseOf(kind)
	if !gnmiFailDue(*st, now, gnmiFailReportAfter) {
		c.mu.Unlock()
		return nil
	}
	st.reported = true
	var ev = gnmiFailingEvent(w, metric, *st, now)
	c.mu.Unlock()

	// Said locally as well as reported, at Info: the proxy's own journal is not
	// the operator's surface — it was unreadable during the incident, which is
	// the entire reason the event above exists — but when it IS readable this is
	// the line that dates the condition.
	slog.Info("gnmi subscription has been failing long enough to report",
		"device", w.DeviceID, "ip", w.IP, "metric", metric,
		"cause", ev.Cause, "attempts", ev.Misses, "failing_for", ev.Detail)
	return &ev
}

// noteSubHealthy clears the failure state and returns the recovery event, or
// nil.
//
// The clear is unconditional and the EVENT is not: a run of failures that was
// never reported ends with nothing said, because announcing the end of something
// nobody was told about is noise that reads as a fault. That is the same rule
// applyDeviceState follows when its verdict is "unknown".
func (c *GNMICollector) noteSubHealthy(key string, w tunnel.GNMIWatch, metric string,
	now time.Time) *tunnel.Event {

	c.mu.Lock()
	var st = c.fails[key]
	if st == nil || st.since.IsZero() {
		// Never failing, or already cleared. The overwhelmingly common path — one
		// map lookup per stream end — and it must stay silent.
		delete(c.fails, key)
		c.mu.Unlock()
		return nil
	}
	var was = *st
	delete(c.fails, key)
	c.mu.Unlock()

	if !was.reported {
		return nil
	}
	var ev = gnmiRecoveredEvent(w, metric, was, now)
	slog.Info("gnmi subscription is delivering again",
		"device", w.DeviceID, "ip", w.IP, "metric", metric,
		"cause_while_failing", was.cause, "attempts", was.attempts,
		"failing_for", ev.Detail)
	return &ev
}

// runOnce holds one subscription for as long as it lasts.
func (c *GNMICollector) runOnce(ctx context.Context, w tunnel.GNMIWatch, metric string) error {
	var target = targetOfGNMI(w)
	if metric == gnmipoll.MetricLLDP {
		return c.runLLDP(ctx, target, w)
	}
	var coll = gnmipoll.Collector{
		Target:   target,
		Metric:   metric,
		Interval: intervalOf(w, metric),
	}
	return coll.Run(ctx, c.sub, func(samples []metricbuf.Sample) {
		// Reachability is noted from the SAMPLE side and only for the two
		// interface sets, mirroring snmp.go: a health subscription that yields
		// nothing on a switch serving no component tree must not paint the
		// telemetry pill red.
		if metric == gnmipoll.MetricIfState || metric == gnmipoll.MetricIfCounters {
			c.noteReach(w.DeviceID, w.IP, true)
		}
		c.buf.AddAll(samples)
		if metric == gnmipoll.MetricIfState {
			c.notePorts(w.DeviceID, w.IP, samples)
		}
	})
}

// runLLDP holds the neighbour subscription and pushes snapshots on a timer.
//
// It does not go through gnmipoll.Collector, and the reason is that a Collector
// translates into SAMPLES. A neighbour is a table of strings and cannot ride the
// metric buffer — cmd/proxy/lldp.go's header argues that out, and the argument
// does not change because the transport did.
func (c *GNMICollector) runLLDP(ctx context.Context, target gnmipoll.Target,
	w tunnel.GNMIWatch) error {

	var table = c.tableFor(w.DeviceID)
	// RESET on every (re)subscribe. A resubscribe replays the device's full
	// initial state, which is the only moment the two views are known to agree;
	// keeping what came before would let a neighbour that vanished while the
	// stream was down survive the reconnect and be re-pushed as a live cable.
	table.Reset()

	var st, err = c.sub.Subscribe(ctx, target,
		gnmipoll.SubscriptionsFor(gnmipoll.MetricLLDP, 0))
	if err != nil {
		return err
	}
	if st == nil {
		// The interface says a nil error comes with a stream; a transport that
		// breaks that contract must not take the proxy down on a nil channel.
		return errNoGNMIStream
	}
	var tick = time.NewTicker(lldpPushEvery)
	defer tick.Stop()
	var n gnmipoll.Notification
	var open bool
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
			// Pushed only once something has actually arrived. An empty snapshot
			// would erase, on the controller side, the neighbours of a device
			// whose only fault was a stream that had not yet delivered its
			// initial state — the same rule the SNMP path applies when a walk
			// fails.
			var report = table.Report()
			if !report.Served {
				continue
			}
			c.pushLLDP(ctx, report)
		case n, open = <-st.C:
			if !open {
				// The REASON, not a flat nil. An LLDP subscription authenticates
				// like every other one, so a refused login ends this loop too —
				// and returning nil here would tell the supervisor the stream had
				// simply finished, which is precisely the silence this batch
				// exists to remove.
				return st.Reason()
			}
			// Deletes are how a stream says a cable went away. Ignoring them
			// would re-assert an unplugged link for ever, with a fresh timestamp
			// on it every cycle — and applying them AFTER the same message's
			// updates loses a live one for ever, which is why the order lives in
			// [gnmipoll.Table.Fold] rather than here.
			table.Fold(n)
		}
	}
}

// tableFor returns the accumulated neighbour table for a device, creating it on
// first use.
func (c *GNMICollector) tableFor(deviceID string) *gnmipoll.Table {
	c.mu.Lock()
	defer c.mu.Unlock()
	var t = c.tables[deviceID]
	if t == nil {
		t = gnmipoll.NewTable(deviceID)
		c.tables[deviceID] = t
	}
	return t
}

// targetOfGNMI translates a controller instruction into a subscribable target.
// One place, so that every subscription of one device speaks to it with the same
// parameters.
func targetOfGNMI(w tunnel.GNMIWatch) gnmipoll.Target {
	return gnmipoll.Target{
		DeviceID: w.DeviceID, IP: w.IP, Port: w.Port,
		Username: w.Username, Password: w.Password,
		Plaintext: w.Plaintext, SkipVerify: w.SkipVerify, CAPEM: w.CAPEM,
	}
}

// notePorts diffs the current oper-status against the previous one and emits the
// fleet's port transitions.
//
// # THE HALF THAT WOULD HAVE GONE SILENT
//
// port.up / port.down are derived on the proxy from `if_state`, and that
// derivation used to read `ifindex` — an SNMP label that gNMI samples do not
// carry and deliberately never will. Moving Arista's `if_state` to gNMI without
// this function would have stopped every port transition for every Arista:
// telemetry would have looked richer, the graphs would have filled, and the fleet
// log would simply have gone quiet. internal/driver/arista/eos/gnmi.go's header
// names this as the reason it once proposed declaring `if_counters` ALONE.
//
// The events it emits carry PortName and no IfIndex, which is honest — there is
// no index to give — and is what the controller already tolerates, since it
// matches ports by name (`portManaged`).
func (c *GNMICollector) notePorts(deviceID, ip string, samples []metricbuf.Sample) {
	var cur = map[string]float64{}
	var s metricbuf.Sample
	for _, s = range samples {
		if s.Name != metricbuf.MetricIfOperStatus {
			continue
		}
		var port = s.Labels["port"]
		if port == "" {
			continue
		}
		cur[port] = s.Value
	}
	if len(cur) == 0 {
		// A batch with no oper-status is a PARTIAL observation — a speed update
		// on its own, say — not a device whose ports vanished. Keeping the
		// previous observation is what stops the next real one from looking like
		// a first sighting, which would silence the transition after it.
		return
	}

	c.mu.Lock()
	var prev = c.ports[deviceID]
	var changes []portNameChange
	var port string
	var val float64
	for port, val = range cur {
		var before, known = prev[port]
		if !known {
			// A port seen for the FIRST time produces nothing: announcing a
			// transition on first sight would announce a change that did not
			// happen. Same rule as diffPorts, and as targetState's `up == nil`.
			continue
		}
		if gnmiPortUp(before) == gnmiPortUp(val) {
			// A value that changed without changing the boolean (2 → 7) is the
			// same fact said differently.
			continue
		}
		changes = append(changes, portNameChange{
			name: port, up: gnmiPortUp(val), status: int(val),
		})
	}
	if prev == nil {
		c.ports[deviceID] = map[string]float64{}
		prev = c.ports[deviceID]
	}
	// MERGED rather than replaced: `if_state` is subscribed ON CHANGE, so a
	// notification carries only the ports that moved. Replacing the map would
	// forget every port that did not, and the next change on one of them would
	// read as a first sighting and be swallowed.
	for port, val = range cur {
		prev[port] = val
	}
	c.mu.Unlock()

	// A 48-port switch coming back produces forty-eight transitions at once, and
	// map iteration order is random: a log that lists ports in a different order
	// at every incident is unreadable.
	sort.Slice(changes, func(i, j int) bool { return changes[i].name < changes[j].name })
	var ch portNameChange
	for _, ch = range changes {
		var kind = tunnel.EventPortDown
		if ch.up {
			kind = tunnel.EventPortUp
		}
		c.emitEvent(tunnel.Event{
			Type: kind, DeviceID: deviceID, IP: ip,
			PortName: ch.name, Trigger: triggerPoll,
			Detail: gnmiPortDetail(ch),
		})
	}
}

// portNameChange is a transition keyed by NAME, since gNMI has no interface
// index to key on.
type portNameChange struct {
	name   string
	up     bool
	status int
}

// gnmiPortUp reads the oper-status value as "this port passes traffic".
//
// The value is IF-MIB's, not OpenConfig's: internal/gnmipoll/translate.go maps
// the enum onto the MIB numbering precisely so that one series holds both
// producers. Reading it any other way here would reintroduce the divergence that
// mapping exists to prevent.
func gnmiPortUp(v float64) bool { return int(v) == ifOperUp }

// gnmiPortDetail composes the text accompanying a transition.
//
// It names an exotic oper-status and says nothing otherwise. There is no trap
// label to weave in, unlike snmp.go's portDetail: a subscription has no
// out-of-band trigger — the stream IS the notification — which is the one place
// where streaming removes a moving part instead of adding one.
func gnmiPortDetail(ch portNameChange) string {
	if ch.status == ifOperUp || ch.status == ifOperDown {
		return ""
	}
	return "oper-status " + operStatusLabel(ch.status)
}

// noteReach records whether telemetry is arriving and emits the transition.
//
// Reuses EventSNMPUp / EventSNMPDown deliberately, despite the names. Those
// events mean "COLLECTION is up / down for this device" — the controller reads
// them to decide whether a silent device is unreachable or merely uncollected —
// and that question is the same one whichever protocol collects. Minting a
// parallel pair would have made every consumer handle two spellings of one fact,
// and a screen that knew only the SNMP pair would show an Arista as permanently
// uncollected.
func (c *GNMICollector) noteReach(deviceID, ip string, ok bool) {
	c.mu.Lock()
	var st = c.reach[deviceID]
	if st == nil {
		st = &gnmiReachState{}
		c.reach[deviceID] = st
	}
	st.ip = ip
	var ev *tunnel.Event
	// No miss counter, unlike the SNMP side. A poll is a request that can be lost
	// — hence three misses before believing it — whereas a subscription failing to
	// establish is one connection refused, which is not ambiguous. Debouncing
	// would only delay a fact already certain.
	if st.up == nil || *st.up != ok {
		var v = ok
		st.up = &v
		var kind = tunnel.EventSNMPUp
		if !ok {
			kind = tunnel.EventSNMPDown
		}
		ev = &tunnel.Event{
			Type: kind, TS: time.Now().UTC(), DeviceID: deviceID, IP: ip,
		}
	}
	c.mu.Unlock()
	if ev != nil {
		c.emitEvent(*ev)
	}
}

// Snapshot returns current telemetry reachability as Resync events, for
// reconnect. Same contract as Monitor.Snapshot: silent when never observed.
func (c *GNMICollector) Snapshot() []tunnel.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	var ids = make([]string, 0, len(c.reach))
	var id string
	for id = range c.reach {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var now = time.Now().UTC()
	var out = make([]tunnel.Event, 0, len(ids))
	for _, id = range ids {
		var st = c.reach[id]
		if st == nil || st.up == nil {
			continue
		}
		var ev = tunnel.Event{
			Type: tunnel.EventSNMPUp, TS: now, DeviceID: id, IP: st.ip, Resync: true,
		}
		if !*st.up {
			ev.Type = tunnel.EventSNMPDown
		}
		out = append(out, ev)
	}
	return out
}

// emitEvent drops an event on the proxy's channel, never blocking.
func (c *GNMICollector) emitEvent(ev tunnel.Event) {
	c.mu.Lock()
	var fn = c.emit
	c.mu.Unlock()
	if fn == nil {
		slog.Debug("gnmi event with no output channel", "type", ev.Type)
		return
	}
	fn(ev)
}
