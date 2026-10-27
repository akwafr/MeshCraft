package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"meshcraft/internal/metricbuf"
	"meshcraft/internal/snmppoll"
	"meshcraft/internal/tunnel"
)

// SNMP collection, run by the PROXY.
//
// See docs/en/01-Architecture/Unified monitoring and pipelines.md §2. The
// controller says WHO to poll and at what cadence; the proxy polls, buffers and
// pushes. It does not arbitrate cadences: they come out of an operating policy
// bounded by the connector's floor, and both of those facts are known only to
// the controller.
//
// What the proxy does decide is the RATE at which it ships upstream — and that
// is the whole subject of the next section.

// --- Upstream rate: the arithmetic, before the values ---
//
// The collection policy (see internal/api/watch_handlers.go) puts traffic and
// errors at 5 s. The arithmetic that follows from it is redone HERE because
// this is where it is paid for:
//
//   - `snmppoll.Samples` returns FOUR samples per port for `if_counters`
//     (octets in/out, errors in/out) and TWO for `if_state` (state, speed);
//   - a 48-port 1930 exposes 52 physical interfaces (48 copper + 4 SFP);
//   - so 208 samples every 5 s (41.6/s) plus 104 every 30 s
//     (3.5/s): about 45 samples per second PER DEVICE.
//
// # THE BUFFER HAS TWO PRODUCERS, AND THIS ARITHMETIC DESCRIBES ONLY ONE
//
// Everything above talks about POLLING. Since streaming telemetry exists,
// `cmd/proxy/main.go` hands THIS buffer to the gNMI collector — one buffer, one
// saturation counter, on purpose (see cmd/proxy/gnmi.go) — and a streaming
// device has no poll cadence to put in the table above.
//
// The tempting conclusion is that streaming costs nothing: ON CHANGE
// subscriptions produce nothing on a stable fleet. It is WRONG, and defusing
// that trap is why this section exists. What dominates is not on-change, it is
// `if_counters`, subscribed in SAMPLE mode, at a cadence, on every port at
// once — exactly the shape of a poll and fully subject to the same
// arithmetic:
//
//   - EIGHT series per port arrive from a 7050SX (octets, packets, errors and
//     discards, in and out; `carrier-transitions` is NOT served by this stream);
//   - the reference chassis carries 64 front-panel ports;
//   - the resolved cadence is 5 s — the floor declared by the connector and the
//     value the controller wants coincide;
//   - so 512 samples every 5 s: about 102 samples per second per device,
//     MORE THAN TWICE a 1930 polled over SNMP.
//
// The two figures add up in the same buffer. A mixed fleet is therefore bounded
// by the sum of both rates, and a streaming fleet tops out at LESS THAN HALF
// the device count announced below for polling. Both calculations are checked
// on every test run, against the code that produces the samples and not
// against this comment: see cmd/proxy/snmp_throughput_test.go for polling and
// cmd/proxy/gnmi_throughput_test.go for streaming.
//
// The old setting — 5000 samples every 10 s — drained 500/s, i.e. ELEVEN
// 48-port devices. Beyond that, `metricbuf` dropped its oldest points: holes in
// the graphs, and not one log line to explain them, since `flush` only spoke of
// losses caused by a SEND failure. Shipping the cadence without fixing that
// would have meant shipping a cadence that never reaches the controller.
//
// # The ceiling is not in this file
//
// It lives on the controller: `/api/proxy/metrics` reads its body through an
// `http.MaxBytesReader` at 8 MiB (`maxProxyMetricsBody`) and refuses more than
// 20,000 samples per batch (`maxSamplesPerBatch`). That has not always been the
// case, and the history is worth keeping: this route used to go through the
// API's generic decoder, capped at 1 MiB, i.e. about 5000 samples. Its
// advertised guard of 20,000 was therefore UNREACHABLE, and truncation answered
// in its place with a 400 "invalid JSON". A slightly oversized batch did not
// degrade the upstream flow, it STOPPED it: the proxy did a `Nack`, which puts
// the batch back at the HEAD of the queue, so the next `Take` picked up exactly
// the same one — and replayed it forever on a batch that would never pass.
//
// That defect had two halves, and both had to be fixed. The controller now
// accepts what it advertises; and `flush` knows not to replay a PERMANENT
// refusal (see isPermanentRefusal). The second half matters at least as much as the
// first: a 413 is no longer reachable for an up-to-date proxy, but the loop was
// still armed for any OTHER shape refusal.
//
// What bounds `maxPerFlush` today is therefore the COUNT guard and no longer
// the byte size: measured on samples actually produced by `snmppoll` (52 ports,
// device identifier as a UUID), a counter weighs 196 B of JSON with factory
// naming and 229 B with a 37-character label typed by the operator — so 8 MiB
// lets ~36,000 samples through, well past the 20,000 wall. And it is still the
// FREQUENCY that carries the rate.

// flushEvery: wake-up period of the flush.
//
// 2 s and not 10: with a bounded batch size, frequency is the only rate lever
// left. The cost is in HTTP requests, not bytes — the data volume is the same,
// cut finer — and it is only paid when there is something to send: `flush`
// returns immediately on an empty buffer, so a small site generates no traffic
// for nothing.
const flushEvery = 2 * time.Second

// maxPerFlush bounds a batch. 3000 × 229 B = 672 KiB measured at the weight of
// a long label, i.e. 8 % of the controller's 8 MiB body ceiling and 15 % of its
// 20,000-samples-per-batch guard — the LATTER is now the wall, and it is far
// away. The figures are checked on every test run against samples produced by
// `snmppoll`, and the wall is COMPUTED there from the measured weight rather
// than copied: see cmd/proxy/snmp_throughput_test.go.
//
// Raising this number remains a trade-off, but no longer a risk of a stall:
// past the wall, the controller answers 413 and `flush` DROPS the batch and
// logs it (see isPermanentRefusal) instead of replaying it forever. What is paid on
// the other side is the controller's transient memory — it decodes the whole
// batch in RAM before writing its first point.
const maxPerFlush = 3000

// maxFlushBurst: consecutive batches allowed per wake-up. THIS is what carries
// the sustainable rate, and it is also what lets a lagging buffer — after a
// tunnel outage — catch up instead of chasing the collection forever.
//
// 4 × 3000 every 2 s = 6000 samples/s. At the ceiling, that asks the controller
// for 2 requests per second of ~670 KiB, about 1.3 MB/s: that is the advertised
// price of the 5 s cadence, and it is linear in the number of devices.
//
// WHAT 6000 samples/s MEANS IN DEVICES DEPENDS ON THE PRODUCER, and stating it
// as a single figure was this comment's mistake:
//
//   - 133 devices of 48 ports polled over SNMP at 45 samples/s (versus 11 before
//     this batch);
//   - ~58 chassis of 64 ports on gNMI telemetry at ~102 samples/s.
//
// Since the buffer is shared, a mixed fleet is bounded by the sum of the rates
// and not by the larger of the two numbers. The second figure is the one to
// remember when sizing a proxy that sees Aristas: it is the lower one.
const maxFlushBurst = 4

// bufCapacity: buffer depth, in samples.
//
// Once the rate is set, the buffer no longer absorbs a permanent deficit —
// there is none up to the ceilings announced by `maxFlushBurst`, 133 devices
// polled or ~58 streaming — but covers a TUNNEL OUTAGE, which is what it was
// written for. Its size therefore reads as outage duration: 150,000 samples is
// 25 s at the rate ceiling, 2 min 46 s for a 20-device fleet and 4 min 37 s for
// 12 — where the old capacity of 50,000 covered only a minute and a half for
// the latter, while already overflowing in NORMAL operation.
//
// The upper bound is the proxy's MEMORY, since it runs on a minimal VM or a
// Raspberry Pi (docs/en/01-Architecture/Proxy.md): measured, a
// `metricbuf.Sample` takes 401 bytes of heap — mostly its tag map, not its
// value. 150,000 points therefore top out at ~57 MiB, and only during an
// outage. Doubling the capacity would double that figure: that is where the
// trade-off is, not in the sample count.
const bufCapacity = 150000

// dropReportEvery throttles the saturation alert. See reportSaturation:
// saturation lasts, and one line every two seconds for an hour would drown the
// log instead of informing it.
const dropReportEvery = time.Minute

// snmpCollector is the proxy's instance. Nil while the proxy is not enrolled:
// without an identity, it has nobody to push to.
var snmpCollector *SNMPCollector

// SNMPCollector polls the targets and pushes the results.
type SNMPCollector struct {
	mu      sync.Mutex
	watches []tunnel.SNMPWatch
	buf     *metricbuf.Buffer
	// last: last poll per (device, metric), to keep the cadence without one timer
	// per target — a hundred timers for a hundred devices cost more than looking
	// at the clock.
	last map[string]time.Time
	// portStates: last observed `ifOperStatus`, per device then per interface
	// index. Port transitions come out of THIS map, on every `if_state` poll and
	// whatever triggered it (see trap.go).
	//
	// It lives next to the metrics buffer, not inside it: the buffer goes to the
	// controller in batches (see flushEvery), the event goes RIGHT AWAY. That is
	// exactly where responsiveness is won — and it is what makes slowing
	// `if_state` to 30 s bearable, since a port's status dot does not depend on the
	// flush rhythm.
	portStates map[string]map[int]portObs
	// snmpReach: last known SNMP reachability per device. Same shape as ICMP
	// targetState — nil means never observed, and Snapshot stays silent then.
	snmpReach map[string]*snmpReachState
	// emit: hands an event to the controller, wired by the proxy (SetEmitter).
	// Nil = not wired yet.
	emit func(tunnel.Event)
	// traps: the listening trap socket, nil otherwise. trapTried / trapRetryAt /
	// trapErrSaid throttle a failing bind (see reconcileTraps).
	traps       *trapReceiver
	trapTried   int
	trapRetryAt time.Time
	trapErrSaid bool
	trapGate    *trapGate
	trapStats   trapStats
	// lldpSaid: last instant a neighbour-push failure was logged, per device (see
	// sayLLDPError). Under the same lock as the rest, because neighbour work runs
	// in the same burst of goroutines as the polls.
	//
	// Bounded by the number of WATCHED devices, not by the number of failures: one
	// entry per device whose push failed at least once. Purged with the watch
	// list, like `portStates`.
	lldpSaid map[string]time.Time
	// fdbSaid : same contract as lldpSaid, for the forwarding-table pushes.
	fdbSaid map[string]time.Time

	controllerURL string
	proxyID       string
	secret        string
	client        *http.Client
}

func NewSNMPCollector(controllerURL, proxyID, secret string) *SNMPCollector {
	return &SNMPCollector{
		buf:           metricbuf.New(bufCapacity),
		last:          map[string]time.Time{},
		portStates:    map[string]map[int]portObs{},
		snmpReach:     map[string]*snmpReachState{},
		trapGate:      newTrapGate(),
		controllerURL: controllerURL,
		proxyID:       proxyID,
		secret:        secret,
		client:        &http.Client{Timeout: 20 * time.Second},
	}
}

// SetEmitter wires the collector's event output.
//
// The collector does not own this channel: it is the Monitor's, already linked
// to the controller by the tunnel (see tunnel.SendEvents) and already
// non-blocking. A second channel would force merging two streams going to the
// same place, and arbitrating their order — work for no extra information.
func (c *SNMPCollector) SetEmitter(fn func(tunnel.Event)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.emit = fn
}

// snmpDownAfter: consecutive failed MIB-II polls before declaring SNMP down.
// Counters fire every 5 s — three misses ≈ 15 s, same order as ICMP's default.
const snmpDownAfter = 3

// snmpReachState mirrors ICMP targetState for SNMP collection.
type snmpReachState struct {
	up     *bool
	misses int
	ip     string
}

// noteSNMPReach records a poll success or failure and emits a transition when
// the observation changes. Called outside c.mu (job goroutines).
func (c *SNMPCollector) noteSNMPReach(deviceID, ip string, ok bool) {
	c.mu.Lock()
	var st = c.snmpReach[deviceID]
	if st == nil {
		st = &snmpReachState{}
		c.snmpReach[deviceID] = st
	}
	st.ip = ip
	var ev *tunnel.Event
	if ok {
		st.misses = 0
		if st.up == nil || !*st.up {
			var on = true
			st.up = &on
			ev = &tunnel.Event{
				Type: tunnel.EventSNMPUp, TS: time.Now().UTC(),
				DeviceID: deviceID, IP: ip,
			}
		}
	} else {
		st.misses++
		if st.misses >= snmpDownAfter && (st.up == nil || *st.up) {
			var off = false
			st.up = &off
			ev = &tunnel.Event{
				Type: tunnel.EventSNMPDown, TS: time.Now().UTC(),
				DeviceID: deviceID, IP: ip, Misses: st.misses,
			}
		}
	}
	c.mu.Unlock()
	if ev != nil {
		c.emitEvent(*ev)
	}
}

// Snapshot returns current SNMP reachability as Resync events, for reconnect.
// Same contract as Monitor.Snapshot: silent when never observed.
func (c *SNMPCollector) Snapshot() []tunnel.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	var ids = make([]string, 0, len(c.snmpReach))
	var id string
	for id = range c.snmpReach {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var now = time.Now().UTC()
	var out = make([]tunnel.Event, 0, len(ids))
	for _, id = range ids {
		var st = c.snmpReach[id]
		if st == nil || st.up == nil {
			continue
		}
		var ev = tunnel.Event{
			Type: tunnel.EventSNMPUp, TS: now, DeviceID: id, IP: st.ip, Resync: true,
		}
		if !*st.up {
			ev.Type = tunnel.EventSNMPDown
			ev.Misses = st.misses
		}
		out = append(out, ev)
	}
	return out
}

// SetWatches replaces the target list.
func (c *SNMPCollector) SetWatches(w []tunnel.SNMPWatch) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.watches = w

	// A device removed from monitoring leaves the port states WITHOUT an event:
	// it is not an outage, it is an operating decision — the same rule as for ICMP
	// targets in Monitor.SetConfig. Keeping it would also leave, when it comes
	// back, a state several days old against which the diff would produce an
	// imaginary transition.
	var watched = make(map[string]bool, len(w))
	var one tunnel.SNMPWatch
	for _, one = range w {
		watched[one.DeviceID] = true
	}
	var id string
	for id = range c.portStates {
		if !watched[id] {
			delete(c.portStates, id)
		}
	}
	// The neighbour-log throttle follows the same rule, and for one more reason: a
	// device coming back into monitoring comes back from a world where the
	// controller's configuration may have changed. Silencing the first failure of
	// its push would miss precisely the one that exposes the new setting.
	for id = range c.lldpSaid {
		if !watched[id] {
			delete(c.lldpSaid, id)
		}
	}
	for id = range c.fdbSaid {
		if !watched[id] {
			delete(c.fdbSaid, id)
		}
	}
	for id = range c.snmpReach {
		if !watched[id] {
			delete(c.snmpReach, id)
		}
	}
	// The cadences follow, for both reasons at once.
	//
	// The first is size: `c.last` was the only collector map that was never
	// purged, hence the only one bounded by devices EVER watched rather than by
	// devices watched. On a controller that moves its devices between proxies, it
	// never shrinks.
	//
	// The second is the first poll on return, and it is narrower than size: `due`
	// compares `now - prev` to the cadence, so a stale timestamp delays nothing as
	// soon as it is older than the cadence — a device gone for an hour is polled
	// again right away. What is paid is removal then return WITHIN the cadence (a
	// watch correction, a controller restart): the map says "already polled" and
	// the first poll waits up to a whole period. The device therefore comes back
	// silent precisely during the seconds one wants to see it.
	//
	// Keys are composite (`deviceID|metric`, see pollDue and trapPoll): split on
	// the LAST separator, the suffix never contains one.
	var key string
	for key = range c.last {
		var cut = strings.LastIndex(key, "|")
		if cut < 0 {
			continue
		}
		if !watched[key[:cut]] {
			delete(c.last, key)
		}
	}
	c.trapGate.forget(watched)
}

// Run loops until stopped: poll what is due, push what is waiting.
func (c *SNMPCollector) Run(ctx context.Context) {
	// Trap listening is reconciled in its own goroutine: binding or closing a
	// socket has no business delaying the fleet poll.
	go c.trapLoop(ctx)
	// So is flushing, and the new cadence makes that change necessary: it is five
	// times more frequent than before and carries the whole fleet's traffic. Left
	// on this loop's tick, a slow controller — up to a 20 s HTTP timeout — would
	// have delayed device POLLING by as much. Upstream would then have degraded
	// collection, i.e. exactly what a buffer exists to prevent.
	go c.flushLoop(ctx)

	var tick = time.NewTicker(time.Second)
	defer tick.Stop()
	var now time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case now = <-tick.C:
			c.pollDue(ctx, now)
		}
	}
}

// flushLoop drains the buffer to the controller.
//
// Up to `maxFlushBurst` batches per wake-up, stopping as soon as a batch goes
// out INCOMPLETE: that signals nothing is left waiting, and insisting would
// send empty requests. See also `flush`'s return value for the case of a
// controller that refuses.
func (c *SNMPCollector) flushLoop(ctx context.Context) {
	var tick = time.NewTicker(flushEvery)
	defer tick.Stop()
	// These two counters are LOCAL to this goroutine: nobody else reads them, so
	// no lock to take and nothing to add to the struct. Putting them in
	// `SNMPCollector` would have opened a third piece of shared state for a need
	// that never leaves this loop.
	var saidAt time.Time
	var saidDropped uint64
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			var i int
			for i = 0; i < maxFlushBurst; i++ {
				if !c.flush(ctx) {
					break
				}
			}
			saidAt, saidDropped = c.reportSaturation(saidAt, saidDropped)
		}
	}
}

// reportSaturation logs a loss of points caused by SATURATION.
//
// `flush` only spoke of losses accompanying a send failure, and that is not
// the same symptom: a buffer overflowing because collection produces faster
// than upstream drains is the failure mode the 5 s cadence makes possible, and
// it is completely SILENT from the operator's point of view — the tunnel is
// up, requests succeed, and all that is left is holes in the graphs. So that
// is the loss that needed naming.
//
// IT IS NOT ONLY ABOUT SNMP, even though it lives on the SNMP collector. The
// buffer is shared with the gNMI collector (see cmd/proxy/main.go), so lost
// points may come from one, the other, or both — and that is precisely why
// there is only one counter: two buffers would mean two counters, and a
// saturation on the streaming side would be named nowhere. The sustainable rate
// attached to the line reads against the SUM of both productions (see the
// header of this file).
//
// The count announced is the PERIOD's, not the total since startup: "42,000
// points lost in the last minute" can be acted on, "3,200,000 since startup"
// can only be endured. The sustainable rate comes with the line, because the
// first question of whoever reads it is "how much can I absorb?".
//
// Returns the new pair (instant of the last report, total already reported).
func (c *SNMPCollector) reportSaturation(saidAt time.Time,
	saidDropped uint64) (time.Time, uint64) {
	var st = c.buf.Stats()
	if st.Dropped <= saidDropped {
		return saidAt, saidDropped
	}
	var now = time.Now()
	// `saidAt` zero = nothing said yet: the FIRST loss is reported right away.
	// Throttling must not delay the symptom's appearance, it must only prevent its
	// repetition.
	if !saidAt.IsZero() && now.Sub(saidAt) < dropReportEvery {
		return saidAt, saidDropped
	}
	slog.Warn("metrics buffer saturated: measurement points are being lost",
		"lost_since", st.Dropped-saidDropped, "lost_total", st.Dropped,
		"queued", st.Queued, "capacity", st.Cap,
		"sustainable_per_s", sustainedSamplesPerSecond())
	return now, st.Dropped
}

// sustainedSamplesPerSecond returns the rate upstream drains in steady state.
// Computed, never copied: it is the figure that must change ON ITS OWN when
// `flushEvery`, `maxPerFlush` or `maxFlushBurst` are touched, otherwise the
// log would end up advertising a capacity that no longer exists.
//
// The computation goes through `time.Duration` (an int64) and not a platform
// integer: the intermediate product is 1.2 × 10¹³ nanoseconds, which would
// overflow a 32-bit `int`. This form is also the only one that stays correct if
// `flushEvery` dropped below one second.
func sustainedSamplesPerSecond() int {
	return int(time.Duration(maxFlushBurst*maxPerFlush) * time.Second / flushEvery)
}

func (c *SNMPCollector) due(key string, now time.Time, every int) bool {
	if every <= 0 {
		return false
	}
	var prev, seen = c.last[key]
	if seen && now.Sub(prev) < time.Duration(every)*time.Second {
		return false
	}
	c.last[key] = now
	return true
}

// pollDue polls the targets whose cadence has elapsed.
func (c *SNMPCollector) pollDue(ctx context.Context, now time.Time) {
	c.mu.Lock()
	var watches = append([]tunnel.SNMPWatch{}, c.watches...)
	var jobs []func()
	var w tunnel.SNMPWatch
	for _, w = range watches {
		var target = targetOf(w)
		if c.due(w.DeviceID+"|state", now, w.StateIntervalS) {
			jobs = append(jobs, c.job(ctx, target, snmppoll.MetricIfState, triggerPoll, ""))
		}
		if c.due(w.DeviceID+"|counters", now, w.CountersIntervalS) {
			jobs = append(jobs, c.job(ctx, target, snmppoll.MetricIfCounters, triggerPoll, ""))
		}
		// The two SENSOR sets, whose cadence may be zero — in which case `due`
		// returns false and nothing is polled. That is not a degraded case but the
		// NORMAL state of a device whose connector does not declare these channels:
		// unlike state and counters, which fit in MIB-II and exist everywhere, health
		// and optical diagnostics rely on proprietary branches. Polling them on a
		// device that does not serve them would only return "no such object", every
		// cycle and across the whole fleet.
		//
		// The controller decides to turn them on, because it is the only one that
		// knows the connector's capabilities (see tunnel.SNMPWatch).
		if c.due(w.DeviceID+"|health", now, w.HealthIntervalS) {
			jobs = append(jobs, c.job(ctx, target, snmppoll.MetricHealth, triggerPoll, ""))
		}
		if c.due(w.DeviceID+"|optical", now, w.OpticalIntervalS) {
			jobs = append(jobs, c.job(ctx, target, snmppoll.MetricOptical, triggerPoll, ""))
		}
		// NEIGHBOURS, whose work is not of the same family as the other four: it
		// returns no samples and does not go through the buffer. It is scheduled here
		// anyway — rather than in a loop of its own — because the same instruction,
		// the same target and the same parallelism cap must apply to it. Two schedulers
		// on the same target list would end up polling a device removed from
		// monitoring by one and not by the other.
		if c.due(w.DeviceID+"|lldp", now, w.LLDPIntervalS) {
			jobs = append(jobs, c.lldpJob(ctx, target))
		}
		// The FORWARDING TABLE, same family as neighbours: a snapshot, no samples, its
		// own route — and the same scheduler, for the same reason (see the neighbour
		// comment above).
		if c.due(w.DeviceID+"|fdb", now, w.FDBIntervalS) {
			jobs = append(jobs, c.fdbJob(ctx, target))
		}
	}
	c.mu.Unlock()

	// In parallel, but bounded: a fleet of two hundred devices polled all at once
	// would saturate the proxy's NAT table before saturating anything else.
	var sem = make(chan struct{}, 16)
	var wg sync.WaitGroup
	var j func()
	for _, j = range jobs {
		wg.Add(1)
		sem <- struct{}{}
		go func(run func()) {
			defer wg.Done()
			defer func() { <-sem }()
			run()
		}(j)
	}
	wg.Wait()
}

// targetOf translates a controller instruction into a pollable target. One
// place, so that the ordinary poll and the poll brought forward by a trap talk
// to the same device with the same parameters.
func targetOf(w tunnel.SNMPWatch) snmppoll.Target {
	var t = snmppoll.Target{
		DeviceID: w.DeviceID, IP: w.IP, Community: w.Community, Port: w.Port,
		MaxIfIndex: w.MaxIfIndex, FDBPerVLAN: w.FDBPerVLAN,
	}
	// A watch carrying a USM identity is a v3 device; the version flag is
	// derived rather than shipped, so the two can never disagree.
	if w.V3User != "" {
		t.Version = "v3"
		t.V3User, t.V3AuthAlg, t.V3AuthSecret = w.V3User, w.V3AuthAlg, w.V3AuthSecret
	}
	return t
}

// --- What the sensor sets change in the RATE, and what they do not ---
//
// The rate arithmetic at the top of this file still holds, and it is better to
// say why than to leave it assumed: `health` and `optical_power` are negligible
// next to `if_counters`, by a factor measured in hundreds.
//
//	if_counters      52 ports × 4 series      every 5 s          41.6 samples/s
//	if_state         52 ports × 2 series      every 30 s          3.5 samples/s
//	health           ~8 device series         every 300 s        0.03 sample/s
//	optical_power    4 cages × 8 series       every 60 s         0.53 sample/s
//
// `optical_power` has walked FIFTEEN branches since 2026-08-12, no longer nine:
// the Cisco family (CISCO-ENTITY-SENSOR-MIB) adds six — four sensor columns and
// the two ENTITY-MIB columns they must be JOINED with to know which port they
// refer to. The rate does not move for all that: a switch serves one of the two
// families, never both, and the empty walk of the other produces no sample.
//
// In other words, the two new sets add less than 1.3 % to the rate of a 48-port
// device. The ceiling of 133 devices per proxy does not move, and there was no
// reason to touch `maxPerFlush` or `maxFlushBurst` for this batch.
//
// What they cost is elsewhere, and it is on the DEVICE: `health` needs sixteen
// separate walks because MIKROTIK-MIB sensors are scalars of a single branch,
// which therefore cannot be walked in one go without overwriting each other
// (see the header of internal/snmppoll/health.go). Sixteen requests every five
// minutes make one packet every twenty seconds: two orders of magnitude below
// what the traffic already costs, and that is what makes the five-minute
// cadence comfortable rather than cautious.

// job returns the work of one poll. `trigger` and `trapLabel` change nothing
// in what is READ — they only qualify the events that come out of it: what
// made us look now, not where the truth comes from.
func (c *SNMPCollector) job(ctx context.Context, t snmppoll.Target,
	metric, trigger, trapLabel string) func() {
	return func() {
		var p = &snmppoll.Poller{Target: t}
		var samples, err = p.Poll(ctx, metric)
		if err != nil {
			// A silent device is an ordinary operating fact — it is off, or SNMP is not
			// configured on it yet. Logging it every cycle would drown the rest: DEBUG,
			// not WARN.
			//
			// And above all: port states are NOT touched. A failing poll proves nothing
			// about the ports; concluding they are down would make an entire front panel
			// blink on every lost UDP packet, while ICMP monitoring already says whether
			// the device answers.
			slog.Debug("SNMP no response", "device", t.DeviceID, "ip", t.IP,
				"metric", metric, "err", err.Error())
			// Reachability for if_state / if_counters only: those are MIB-II floors
			// every agent answers. A failed optical walk on a copper switch must not
			// paint the SNMP pill red.
			if metric == snmppoll.MetricIfState || metric == snmppoll.MetricIfCounters {
				c.noteSNMPReach(t.DeviceID, t.IP, false)
			}
			return
		}
		if metric == snmppoll.MetricIfState || metric == snmppoll.MetricIfCounters {
			c.noteSNMPReach(t.DeviceID, t.IP, true)
		}
		// The metric continues on its usual path: the batch leaves on the next flush.
		c.buf.AddAll(samples)
		if metric == snmppoll.MetricIfState {
			c.notePortStates(t.DeviceID, samples, trigger, trapLabel)
		}
	}
}

// reasonMaxBytes bounds how much of the refusal reason is read. These bodies are
// JSON error responses of a few dozen bytes; reading without a bound whatever a
// misconfigured intermediary may send back (an HTML error page of several
// hundred kilobytes) would make the proxy pay in memory for what it is already
// refusing.
const reasonMaxBytes = 512

// refusalReason extracts what the controller SAYS about its refusal.
//
// Used only on the DROP path, and deliberately so. It is the only one where
// points die: "3000 points dropped, status 413" leaves the operator guessing
// which of their settings is at fault, whereas the controller's sentence names
// it (see batchRecovery, internal/api/proxymetrics_handlers.go). On the replay
// path, by contrast, the same line would come back every two seconds: nothing
// to gain, and a body to read on every attempt.
//
// A body truncated at reasonMaxBytes is no longer valid JSON: falling back to
// the raw text is therefore the NORMAL branch of that case, not an error case.
func refusalReason(r io.Reader) string {
	var raw, err = io.ReadAll(io.LimitReader(r, reasonMaxBytes))
	if err != nil || len(raw) == 0 {
		return ""
	}
	var payload struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(raw, &payload) == nil && payload.Error != "" {
		return payload.Error
	}
	return strings.TrimSpace(string(raw))
}

// isPermanentRefusal says whether a controller refusal is PERMANENT, i.e. whether
// replaying the same batch cannot change the verdict.
//
// This is the most loaded boundary in this file, because the two possible
// mistakes do not cost the same price:
//
//   - taking as permanent what was transient LOSES a batch that would have gone
//     through ten seconds later. A bounded, visible loss worth one batch;
//   - taking as transient what was permanent STOPS upstream. `Nack` puts the
//     batch back at the HEAD of the queue, so the next `Take` picks up exactly
//     the same one: a batch refused for its shape comes back IDENTICAL on the
//     next flush, and on every one after. The buffer no longer drains, it
//     overflows from the top, and the collection loses its MOST RECENT points.
//     Not a partial loss: the end of upstream for this proxy.
//
// The second mistake is unbounded, the first is not — and yet the list of
// permanent cases is CLOSED: what cannot be classified is treated as
// TRANSIENT, i.e. on the side of the more expensive mistake. That is not a
// contradiction, it is a trade-off on REPAIRABILITY, and it must be stated
// that way round:
//
//   - a drop is IRREVERSIBLE and quiet. The points are destroyed, and the only
//     witness is a log line nobody may ever read;
//   - a stall is NOISY and repairable. Every refusal is logged, saturation
//     shouts every minute, and the points wait in the buffer. An operator ends
//     up seeing the symptom, and the batch goes out as soon as the cause is fixed.
//
// Faced with a status code nobody planned for, we do not know which of the two
// cases we have in hand. Choosing the stall leaves a chance to understand and
// repair; choosing the drop destroys the data before anyone could ask the
// question. Hence EXPLICIT enumeration on both sides: the fallback only catches
// codes this route does not produce.
//
// Case by case, and the reasoning rather than the code:
//
//   - `Retry-After` present → TRANSIENT, whatever the code. A refusal that says
//     when to come back is a refusal waiting for you to come back. Tested FIRST,
//     because it is exactly what a slightly broad `>= 400` would file on the
//     wrong side: a 429 or a maintenance 503 asks for patience, it is not a refusal;
//   - 400 → PERMANENT. The controller read the body and does not want it. The
//     same bytes will get the same verdict for as long as they are resent. Same
//     reasoning as the `Ack` on a failed `json.Marshal`, further down in
//     `flush`: unreadable, replaying it would change nothing;
//   - 413 → PERMANENT. Body over `maxProxyMetricsBody`, or batch over
//     `maxSamplesPerBatch`. The content is fine, it is the VOLUME that does not
//     fit — and replaying it identically shrinks nothing (on re-splitting, see below);
//   - 415 → PERMANENT. `flush` always sends the same `Content-Type`;
//   - 422 → PERMANENT. The controller understood the request and rejects its substance.
//
// And the refusals that are replayed, the ones for which we must say WHY they
// are not on the list:
//
//   - 401 / 403 → TRANSIENT, and this is the most debatable decision of the lot.
//     It rests on one fact: here the controller holds nothing against THE BATCH —
//     it has not even looked at it, authentication happening after the body is
//     read. A secret gets rotated, a revocation gets undone, a restarting
//     controller may refuse for a second before accepting; dropping would throw
//     away points that an up-to-date enrolment would have taken. The objection is
//     real — a revoked proxy replays forever and ships nothing — but it is not
//     fixed here: dropping on 403 would not ship one more point, it would only
//     erase the trace. The buffer is bounded, it drops its oldest,
//     `reportSaturation` says so every minute and every refusal has its line. A
//     revoked proxy has no recipient for its metrics anyway;
//   - 404 / 405 → TRANSIENT. Route missing: controller of an earlier version, or
//     a misconfigured URL. A deployment or a configuration fix repairs it, and
//     the batch then goes out as is — which is precisely what a buffer exists to
//     cover;
//   - 408 / 429 → TRANSIENT by definition, with or without `Retry-After`;
//   - 5xx → TRANSIENT, without exception. The controller is in trouble, not in
//     disagreement: 502 on a failed VictoriaMetrics write, 503 on unconfigured
//     storage (see handleProxyMetrics, which answers those two precisely SO THAT
//     the proxy keeps its batch).
//
// # Why a refusal on SIZE is dropped too
//
// This is the case where dropping hurts most: the batch's content is fine,
// only its volume does not fit, and RE-SPLITTING it would be better than
// throwing it away. The `metricbuf` API does not naturally allow it. `Take`
// returns the reserved batch AS IS, ignoring its argument, as long as that batch
// is neither acknowledged nor returned; there is neither partial acknowledgement
// nor resizing. The only path would be `Nack` — which puts the batch back at
// the top — then a smaller `Take` on the next round, and the reduced size would
// then have to be remembered in the collector, a decision made on when to raise
// it again, and made to coexist with `maxFlushBurst`. That is extra state and a
// whole fallback policy for a case the controller no longer produces: at an
// 8 MiB ceiling, a 3000-sample batch is 672 KiB. A logged drop is enough, and it
// has the advantage of being unable to loop.
//
// Takes the code and the header rather than the whole response: the boundary is
// a decision on two values, and it must be tested case by case without
// building an HTTP response (see TestPermanentRefusalBoundary).
func isPermanentRefusal(status int, retryAfter string) bool {
	if retryAfter != "" {
		return false
	}
	switch status {
	case http.StatusBadRequest, http.StatusRequestEntityTooLarge,
		http.StatusUnsupportedMediaType, http.StatusUnprocessableEntity:
		return true
	}
	return false
}

// flush pushes one batch to the controller.
//
// Take/Ack/Nack: until the controller has confirmed, the batch stays reserved
// and returns to the buffer if sending fails. That is why a tunnel outage does
// not lose the minutes it lasts — EXCEPT on a permanent refusal, where the
// batch is dropped so the buffer can move on (see isPermanentRefusal).
//
// A drop is always logged, with the number of points sacrificed. A silent drop
// would be worse than the loop it replaces: the loop, at least, ends up being
// seen.
//
// Returns true when a FULL batch went out: there is then probably enough left to
// fill another right away, and that is what lets the caller chain (see
// maxFlushBurst). False stops the burst, in the cases where continuing would be
// harmful or useless: nothing to send, an incomplete batch hence an empty
// buffer, or a controller that has just refused. INCLUDING on a drop, even
// though the buffer did move on: if the refusal is systematic — a controller
// that refuses everything this proxy sends — chaining the burst would sacrifice
// four batches per wake-up instead of one. Losing slowly gives the log time to
// be read.
func (c *SNMPCollector) flush(ctx context.Context) bool {
	var batch = c.buf.Take(maxPerFlush)
	if len(batch) == 0 {
		return false
	}
	var body, err = json.Marshal(map[string]any{
		"proxy_id": c.proxyID, "secret": c.secret, "samples": batch,
	})
	if err != nil {
		c.buf.Ack() // unreadable: replaying it would change nothing
		return false
	}
	var req *http.Request
	req, err = http.NewRequestWithContext(ctx, http.MethodPost,
		c.controllerURL+"/api/proxy/metrics", bytes.NewReader(body))
	if err != nil {
		c.buf.Nack()
		return false
	}
	req.Header.Set("Content-Type", "application/json")
	var resp *http.Response
	if resp, err = c.client.Do(req); err != nil {
		c.buf.Nack()
		var st = c.buf.Stats()
		slog.Warn("metrics not pushed, kept in buffer",
			"err", err.Error(), "queued", st.Queued, "dropped", st.Dropped)
		return false
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if isPermanentRefusal(resp.StatusCode, resp.Header.Get("Retry-After")) {
			// The reason is read BEFORE the drop, while the body is still open.
			var reason = refusalReason(resp.Body)
			c.buf.Ack()
			// "dropped" is the word that matters in this line: it is a number of
			// measurement points that will never exist again, and it has no other place to
			// be said. The batch size comes with it because the most likely refusal here
			// is still the refusal on VOLUME, and the diagnosis misses the point without
			// that figure.
			slog.Warn("metrics DROPPED: permanent refusal from the controller, "+
				"replaying them would change nothing",
				"status", resp.StatusCode, "dropped", len(batch),
				"bytes", len(body), "reason", reason,
				"queued", c.buf.Stats().Queued)
			return false
		}
		c.buf.Nack()
		// Returned to the buffer: this batch will go out as is on the next wake-up.
		// Saying so — "kept", not "lost" — avoids sending someone looking for a loss
		// that did not happen.
		slog.Warn("metrics refused by the controller, kept in buffer",
			"status", resp.StatusCode, "samples", len(batch),
			"bytes", len(body), "queued", c.buf.Stats().Queued)
		return false
	}
	c.buf.Ack()
	return len(batch) == maxPerFlush
}
