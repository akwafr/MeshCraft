// Alerting — who deserves to be woken, and who does not.
//
// ADR-0024. This file is the DECISION; alertqueue.go is the plumbing and
// internal/notify is the wire. Everything here is a pure function of facts the
// caller gathered, which is the point: the rules that decide whether somebody's
// phone buzzes at 3am are testable without a database, a proxy or a device.
//
// # The three sources, and the exclusion that is the whole subject
//
// A journal line becomes an alert only through one of three classes: the
// equipment went away (`device`), a port facing another device of the fleet
// moved (`interco`), or a port somebody took the trouble to DESCRIBE moved
// (`described`). A port event in none of them produces nothing at all.
//
// That exclusion is not an optimisation, it is the policy. On a fleet of two
// thousand ports — ARENA alone carries 88 clients — alerting on every managed
// port means a channel nobody reads by the end of the week, and a channel
// nobody reads is worse than no channel: it teaches its readers that the
// product cries wolf. `portManaged` (watch_handlers.go) already keeps "libre"
// ports out of the JOURNAL for exactly this reason; this is the same argument
// one notch further out, where the cost of noise is a human being.
//
// # What "described" means here, and why it is not ifAlias
//
// It means `devices.ports.<port>.label` — the text an operator typed in the
// Ports screen, which calls that field "description". It is NOT the device's
// own `ifAlias`: no connector reads that OID, `mdpv1.PortState` has no field to
// carry it, and MDP v1 could not transport it (ADR-0024, Context).
//
// The substitution is a gain, not a fallback. `ifAlias` says "somebody wrote a
// string on this switch", often a predecessor, often stale, sometimes an
// automated inventory with no human intent behind it. `label` says "somebody
// described this port IN MESHCRAFT, in the screen that exists to do it". The
// signal we want is human interest, and the second measures it better.
//
// # The blind spot, named
//
// A critical port nobody described, facing nothing meshcraft knows, falls
// silently. A UPS port, a camera, a door controller — plugged in a hurry, never
// described. The rule inherits human negligence, and that is accepted (the
// alternative is a channel nobody reads), but it is NOT hidden: every refusal
// increments `notify_channels.dropped_uninteresting`, so the screen can say
// "127 port transitions not alerted: no description, no known neighbour". An
// accepted blind spot that is counted is a different thing from a silence.
package api

import (
	"strings"
	"time"

	"meshcraft/internal/model"
	"meshcraft/internal/store"
)

// --- Tunables ---

// alertHold is how long a port event is RETAINED before it may be delivered.
//
// It is not a second flap detector. `portevents.go` already correlates
// transitions into episodes (5 in 5 min opens, 3 min of calm closes) and that
// is where "does this port flap" is decided — building a second correlator
// beside it would mean two sets of thresholds to keep in tune and a behaviour
// nobody can predict (ADR-0024, Rejected alternatives).
//
// This is a DELIVERY delay, and its rule is one sentence: a key that is moving
// does not alert. A second transition on the same key inside the window cancels
// the delivery and pushes the deadline back, so the message goes out when the
// port has either settled — one message, final state, count of transitions —
// or been declared flapping by the correlator, in which case THAT is the
// message. Six round trips in two minutes therefore produce one message.
//
// 45 s is chosen against the correlator, not against the hardware: it is long
// enough that a port bouncing fast enough to matter always catches its own
// deadline, and short enough that a clean one-way transition on an interco
// still arrives while somebody is reading.
const alertHold = 45 * time.Second

// alertHoldMax caps the pushing-back, so a port that flaps forever still speaks.
//
// The backstop matters more than it looks, because of a real hole: ports
// collected over gNMI emit transitions WITHOUT an ifIndex (cmd/proxy/gnmi.go),
// and `portFlaps.observe` rejects those, so no `port.flapping` is ever written
// for an Arista. Without this cap such a port would be retained indefinitely by
// its own instability, waiting for a verdict that cannot come. Five minutes
// matches `flapWindow`: past it, the correlator would have concluded if it were
// ever going to.
const alertHoldMax = 5 * time.Minute

// selfActionWindow is how long a meshcraft gesture keeps its own consequences
// from being announced as news.
//
// Measured need: on the night of 2026-10-24 eight ARENA switches had their
// MSTP region rewritten. An alerting running then would have fired on every
// port transition caused by OUR OWN write. A product that alarms itself about
// what it just did loses its reader's trust in one evening.
//
// Ten minutes is sized on the tail, not the gesture: an apply returns in
// seconds, but spanning tree reconverges, a port renegotiates, a device that
// took a credential reboots. The consequences arrive well after the write
// completes, which is why `applyQueuedOrRunning` alone is not enough.
const selfActionWindow = 10 * time.Minute

// notifyMaxAttempts is the store's constant, not a copy of it: the retry
// ceiling appears in the due-query's WHERE clause and in the worker's decision
// to schedule another attempt, and two constants that must agree are two
// constants that eventually will not.
const notifyMaxAttempts = store.NotifyMaxAttempts

// Retry schedule of a delivery. Exponential, clamped, bounded — the same shape
// as snmpHeal's backoff and for the same reason: a channel that is down stays
// down for a while, and hammering it neither helps nor informs.
const (
	notifyBackoffMin   = 30 * time.Second
	notifyBackoffMax   = 30 * time.Minute
	notifySweepEvery   = 10 * time.Second
	notifySendTimeout  = 10 * time.Second
	notifyBatchPerPass = 50
)

// --- The alertable vocabulary ---

// alertableTypes is the EXHAUSTIVE set of journal types that can become an
// alert, mapped to the class they belong to before the port rules apply.
//
// Six types, all already emitted today. Nothing new is observed, nothing new is
// detected: ADR-0024's first rule is that the journal is the only source, so
// this map is a SELECTION over `events.type` and never a vocabulary of its own.
// An alert with no journal line behind it would be undebuggable.
var alertableTypes = map[string]string{
	// Device reachability. ~3 s detection, already debounced at three
	// consecutive ICMP misses by the proxy (control.go DefaultDownAfter).
	// `unknown` is deliberately absent here because it is absent from the
	// journal too: nobody is woken for a device nobody has looked at yet.
	"device.unreachable": model.AlertClassDevice,
	"device.recovered":   model.AlertClassDevice,
	// Port link. The class is decided per-event by the interest rule below;
	// "port" here is a marker, never a channel-subscribable class.
	"port.down":         alertPortPending,
	"port.up":           alertPortPending,
	"port.flapping":     alertPortPending,
	"port.flap_cleared": alertPortPending,
}

// alertPortPending marks a type whose class cannot be known from the type
// alone: it depends on the port, and therefore on a database read.
const alertPortPending = "?port"

// portFacts is what the caller had to look up to apply the interest rule. A
// struct rather than two bools so a call site cannot silently swap them.
type portFacts struct {
	// Described: devices.ports.<port>.label is non-empty.
	Described bool
	// Interco: an LLDP neighbour on this port resolved to a fleet device, or a
	// device_links row names it. Derived, never stored (ADR-0024 rule 3).
	Interco bool
}

// classifyAlert returns the class a journal line belongs to, and whether it
// qualifies at all.
//
// PURE function of the event and the facts gathered about its port. The whole
// policy of ADR-0024 is readable in twenty lines here, which is the reason it
// takes `portFacts` instead of a Store.
//
// Precedence when a port is both described and interco: `interco` wins. It is
// the more consequential class — a cable between two switches carries everyone
// else's traffic — and a channel subscribed to intercos only must not miss it
// because somebody also typed a description.
func classifyAlert(ev model.Event, pf portFacts) (string, bool) {
	var class, known = alertableTypes[ev.Type]
	if !known {
		return "", false
	}
	if class != alertPortPending {
		return class, true
	}
	// A port event with no port name is not attributable to a jack. It happens
	// when the facade did not recognise the ifIndex, and `portEventOf` leaves
	// PortName empty on purpose in that case. We cannot evaluate interest, and
	// guessing would be the credible false alert: silence.
	if strings.TrimSpace(ev.PortName) == "" {
		return "", false
	}
	switch {
	case pf.Interco:
		return model.AlertClassInterco, true
	case pf.Described:
		return model.AlertClassDescribed, true
	}
	// THE EXCLUSION. Neither described nor facing anything we know: the event
	// stays in the journal, visible on the port's record, and wakes nobody.
	// The caller counts this refusal (dropped_uninteresting) so the blind spot
	// is a number instead of a silence.
	return "", false
}

// alertKeyOf builds the grouping key of a candidate: (class, device, port).
//
// It is the identity under which the hold window coalesces, so it must be
// stable across the events of one episode. The class is in it deliberately: a
// device alert and a port alert on the same device are different stories and
// must not absorb each other.
func alertKeyOf(class string, ev model.Event) string {
	var device string
	if ev.DeviceID != nil {
		device = *ev.DeviceID
	}
	return class + "|" + device + "|" + ev.PortName
}

// --- The hold window ---

// holdRelease says when a delivery may go out.
//
// `first` is the first event of the episode and never moves; `now` is the event
// being filed. The deadline is one hold from now, unless that would exceed the
// cap measured from `first`, in which case it is the cap. A flapping key is
// therefore pushed back repeatedly but not forever.
//
// Returns the deadline and whether the cap bound it — the caller wants to know,
// because a capped release is "this port is still unstable, say so now" and not
// "this port settled", and the two deserve different wording.
func holdRelease(first, now time.Time, hold, cap time.Duration) (time.Time, bool) {
	var wanted = now.Add(hold)
	var ceiling = first.Add(cap)
	if wanted.After(ceiling) {
		return ceiling, true
	}
	return wanted, false
}

// alertDeviceHold is the hold for a device alert — shorter than a port's, but
// NOT zero.
//
// Zero was the first answer and it was wrong. The reasoning for zero went:
// device reachability is already debounced where debouncing belongs (three
// consecutive ICMP misses at the proxy), and a device that is gone does not
// become less gone in 45 s. Both halves are true and the conclusion still does
// not follow, because of the ASYMMETRY: down takes three misses (~2.75 s), up
// takes a single echo reply. A device behind a flapping uplink therefore
// produces a down/up pair every few seconds, each one a genuine status change
// that `SetDeviceStatus` will not dedup — and with no hold, each one a message.
// That is the storm of trap #1 arriving through the door left open for latency.
//
// 20 s buys the coalescing and costs nothing a human notices: nobody reading a
// phone notification can tell 3 s from 23 s, and a device that is really down
// is still down at 20 s. The rule is now uniform — a key that is moving does
// not alert — with one duration per class instead of a special case.
const alertDeviceHold = 20 * time.Second

// holdFor returns how long an alert of this class is retained before delivery.
//
// One rule, one duration per class, no exemption. A port waits longer because a
// port flaps more and its correlator (portevents.go) needs room to pronounce;
// a device waits less because its detection is already debounced and the alert
// is the one whose latency matters most.
func holdFor(class string) time.Duration {
	if class == model.AlertClassDevice {
		return alertDeviceHold
	}
	return alertHold
}

// --- Not alerting on our own gestures ---

// gestureTypes is the journal vocabulary that means MESHCRAFT WROTE SOMETHING.
//
// The list is the point of the mechanism, and it is deliberately generous: a
// missing entry is a false alert attributed to a device when it was us, which
// is the failure this exists to prevent. All of these are already emitted —
// using the journal as the record of our own actions costs nothing and cannot
// be forgotten the way a manual maintenance window can (ADR-0024, Rejected
// alternatives).
var gestureTypes = map[string]bool{
	// Configuration reconciliation.
	"config.applied":      true,
	"config.forced":       true,
	"config.apply_failed": true,
	"config.absorbed":     true,
	"config.imported":     true,
	// Per-feature writes. Each of these can bounce a port by itself: port
	// admin shuts it, PoE cycles it, STP reconverges it, storm control and
	// DHCP snooping re-apply an interface policy.
	"device.port_admin":          true,
	"device.port_power_cycle":    true,
	"device.poe_apply":           true,
	"device.stp_apply":           true,
	"device.storm_control":       true,
	"device.dhcp_snooping_apply": true,
	"lag.applied":                true,
	"switch_profile.applied":     true,
	"port.configured":            true,
	// Management addressing. The most violent gestures we make: a device that
	// changes address disappears and comes back, which is indistinguishable
	// from a failure if we do not remember we asked for it.
	"mgmt.applied":             true,
	"mgmt.jumped":              true,
	"mgmt.migrated":            true,
	"mgmt.migration_persisted": true,
	"device.relocate_started":  true,
	"device.relocated":         true,
	// Identity and collection.
	"snmp.applied":         true,
	"trap.dest_applied":    true,
	"credential.installed": true,
	// Lifecycle. An adoption reads, writes and reboots; onboarding likewise.
	"device.adopted":   true,
	"device.onboarded": true,
}

// proxyGestureTypes is the same idea one level up: a gesture on a PROXY whose
// consequences land on every device it watches.
//
// Kept separate because the attribution is not by device id — these journal
// lines are instance events with no device — and because the blast radius is
// different in kind: a proxy upgrade makes a whole site look like it moved.
//
// Note what is NOT here, and why it needs nothing: `proxy.disconnected`. A
// proxy that drops does not make its devices look DOWN, it makes them look
// `unknown` (judgeReach returns unknown when no witness is connected), and
// `unknown` is never journalled. The worst self-inflicted storm imaginable is
// therefore already prevented upstream, by a decision taken for another reason.
var proxyGestureTypes = map[string]bool{
	"proxy.upgraded":        true,
	"proxy.upgrade_stalled": true,
	"proxy.migrated":        true,
	"proxy.tunnel_cut":      true,
}

// attributedToGesture reports whether a candidate falls inside the shadow of
// one of our own writes, and names it.
//
// `recent` is the device's journal lines over the last selfActionWindow, newest
// first — the caller reads them once per candidate device. The returned reason
// is stored on the suppressed delivery slip, so a silence stays explainable
// months later ("suppressed: device.stp_apply 4 min earlier").
//
// SUPPRESSION ONLY EVER DELAYS BAD NEWS. That is the doctrine `holdReach`
// already proved in this codebase (reachability.go: "only ever delays BAD
// news"), and it is why the sweep re-arms a suppressed alert when the window
// closes on a state that is still wrong. Nothing is lost; it is postponed past
// our own noise.
func attributedToGesture(ev model.Event, recent []model.Event, now time.Time,
	window time.Duration) (string, bool) {
	var e model.Event
	for _, e = range recent {
		if !gestureTypes[e.Type] && !proxyGestureTypes[e.Type] {
			continue
		}
		if now.Sub(e.TS) > window {
			// Newest first, so the first line past the window ends the walk.
			break
		}
		// A gesture logged AFTER the event it would excuse explains nothing:
		// the consequence cannot precede the cause. Guarding this is what keeps
		// the window from swallowing an outage that happened just before
		// somebody started an apply on the same device.
		if e.TS.Before(ev.TS) {
			return e.Type, true
		}
	}
	return "", false
}

// --- Channel subscription ---

// channelWants reports whether this channel asked for this class at this
// severity.
//
// Order matters and is deliberate: classes first, severity second. The severity
// floor is a volume knob INSIDE what the operator subscribed to, not a way to
// receive a class they excluded. Reversing the two would let a `min_severity`
// of 0 deliver every ordinary port transition of the fleet to a channel that
// only ever asked for intercos.
func channelWants(ch model.NotifyChannel, class string, severity int) bool {
	if !ch.Enabled {
		return false
	}
	var c string
	var subscribed bool
	for _, c = range ch.Classes {
		if c == class {
			subscribed = true
			break
		}
	}
	if !subscribed {
		return false
	}
	return severity >= ch.MinSeverity
}

// backoffAfter returns the wait before attempt n+1, doubling from the floor and
// clamped at the ceiling.
func backoffAfter(attempts int) time.Duration {
	var d = notifyBackoffMin
	var i int
	for i = 1; i < attempts; i++ {
		d *= 2
		if d >= notifyBackoffMax {
			return notifyBackoffMax
		}
	}
	return d
}
