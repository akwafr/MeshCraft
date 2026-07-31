package api

import (
	"log/slog"
	"sync"
	"time"

	"meshcraft/internal/tunnel"
)

// What a proxy is allowed to say, and about which device.
//
// Defect repaired: `handleProxyEvent` received a `tunnel.Event` whose
// `DeviceID` is chosen by the PROXY, and acted on it without asking whether this
// proxy had anything to do with that device. A compromised proxy — or simply one
// out of order after an instance migration — could declare unreachable a switch
// from ANOTHER site, make its ports blink in the fleet journal, and write telemetry
// there. Site coverage is presented everywhere in the vault as a confidentiality
// boundary (migration 0022); it was enforced on none of the three state-write paths.
//
// The correction pattern already existed: `judgeTrapProof` (trapdest.go) refuses
// proof from a proxy that is not the one written in the destination. This file
// applies the same gesture, one step earlier and to all facts.
//
// # The rule, in one sentence
//
//	the controller accepts a fact about a device only if it asked THIS proxy to
//	watch it.
//
// It is NOT read from the database. `watchConfigFor` already computes the exact
// set of devices entrusted to each proxy, and `PushWatchTo` pushes it; it only
// remained to RETAIN it. Verification becomes an in-memory lookup on the hot
// path, and above all it is EXACT: it queries the same value transmitted to the
// proxy, not a second expression of the coverage rule that could one day say
// something else. One query per port transition — the most frequent event in the
// product, a few hundred per minute on a flapping fleet — would also have put
// database load where none belongs.
//
// # Two granularities, and confusing them is a false positive or a hole
//
// `watchConfigFor` produces TWO sets, and produces them differently:
//
//   - `cfg.Targets`, ICMP targets, by site COVERAGE. Several proxies may
//     legitimately ping the same device — that is even the reason for
//     portevents.go deduplication — and this set authorizes `device.up` /
//     `device.down`;
//   - `cfg.SNMP`, collection targets, restricted to each device's declared
//     COLLECTOR (cf. snmpCandidates). This set authorizes port transitions,
//     `trap.seen`, and port telemetry, since all three derive from SNMP polling:
//     a proxy reporting a port transition for a device it pings but does not
//     poll is at fault, even if its coverage is impeccable.
//
// Retaining only the first set would let through precisely the most rewarding
// attack — writing to the fleet journal and port inventory for a device one is
// not the collector of. Retaining only the second would cut reachability for
// the whole fleet attached elsewhere.
//
// # Why the set survives tunnel reconnection
//
// This is the least obvious decision in the batch, and it comes from the REAL
// order of the code rather than from principle. `handleProxyTunnel`
// (tunnel_handlers.go) starts `AcceptEvents` and `PushWatchTo` in TWO
// goroutines, with no guaranteed order — and even with the reverse order the
// result would be the same, because `PushWatchTo` begins with several database
// round trips (`DevicesWatchedBy`, `ProxyDomains`, then a `GetDevice` and
// community decryption PER collected device) where the proxy has only a yamux
// stream to open. On the proxy side, `monitor` is a process singleton: its
// configuration and 256-entry event queue SURVIVE tunnel loss, and `SendEvents`
// drains that queue as soon as the session is restored. The first event packet
// of a reconnection therefore arrives, in practice always, BEFORE the end of the
// first watch computation.
//
// Purging the set on disconnect would therefore lose at every reconnection
// exactly the transitions accumulated during the outage — those with the most
// value, since they are the only ones nobody saw. That is not an acceptable
// compromise for a security fix, and it is not necessary: the retained set must
// reflect what the PROXY holds, and the proxy still holds the old configuration.
// We keep it, and the first packet is judged against the configuration the proxy
// is still working from. The push that follows replaces it entirely.
//
// This is not an open door: a retained set serves only to judge events from a
// proxy that speaks, therefore a connected proxy. An orphan entry does nothing.
//
// ONE window remains, and it is named rather than hidden: on CONTROLLER startup,
// memory is empty and the first packet from each proxy is refused until its
// first `watchConfigFor`. That is the only case where we truly have entrusted
// nothing; refusing is then the only just answer, and it is self-healing — the
// push happens on connection, once per proxy per startup. Accepting « until it
// gets set up » would not be a security fix.
//
// # Where this state lives
//
// In a `Server` field (`s.proxyScopes`), mounted by `Router()` beside `portFlaps`,
// `WatchExcl`, and `live`, for the reason already written in api.go: two
// controllers in the same process — which is what a test suite is — have no
// reason to share memory of what they entrusted to their proxies. It lived one
// batch at PACKAGE level for lack of being able to touch `api.go`, exactly like
// `portFlapsShared` before being moved back; it has returned since.
//
// Production call sites are therefore METHODS on `Server` (`allowProxyFact`,
// `recordProxyScope`, `scopeBatchFor`): that is the only place memory is reached,
// guaranteeing no write path can consult another controller's scope.

// proxyAuthority: the kind of authority a fact REQUIRES.
//
// Two values, not three: what matters is not the event type but the
// `watchConfigFor` set it derives from. `trap.seen` and a port transition
// require the same thing — being the collector — because both come from SNMP
// polling.
type proxyAuthority int

const (
	// authorityPing: this fact derives from PING. Authorized by `cfg.Targets`,
	// therefore by site coverage, therefore possibly by several proxies for the
	// same device.
	authorityPing proxyAuthority = iota
	// authorityPoll: this fact derives from SNMP POLLING. Authorized by
	// `cfg.SNMP`, therefore by attachment: one proxy, the declared collector.
	authorityPoll
)

// scopeVerdict: is this fact acceptable from this proxy, and if not why?
//
// Three refusals, not one, because they are not repaired the same way and above
// all they do not WEIGH the same. A « no configuration yet » refusal is a startup
// race that resolves itself; a « pinged but not collected » refusal is a proxy
// overstepping its role; a « never entrusted » refusal is a proxy speaking about
// a device from another site — the case this whole check exists to catch.
// Flattening them into a boolean would make the journal unusable at the precise
// moment one must decide defect or attack.
type scopeVerdict int

const (
	// scopeGranted: the controller did ask THIS proxy to watch this device, at
	// the granularity the fact requires.
	scopeGranted scopeVerdict = iota
	// scopeNoConfig: nothing has been entrusted to this proxy yet. Controller
	// startup race, or a proxy whose first push failed.
	scopeNoConfig
	// scopeNotEntrusted: this proxy does not watch this device, in any capacity.
	// THE serious refusal.
	scopeNotEntrusted
	// scopeNotCollected: this proxy does ping this device, but is not its
	// collector — it does not poll it in SNMP, and therefore cannot know
	// anything about its ports.
	scopeNotCollected
)

// proxyScope: the exact set the last push entrusted to ONE proxy.
//
// Two identifier sets and a date, nothing more. No `tunnel.Event`, no full
// `WatchConfig` kept: only what the decision depends on, bounding memory to
// two keys per watched device and making the structure comparable test to test.
type proxyScope struct {
	// ping: identifiers from `cfg.Targets`.
	ping map[string]bool
	// poll: identifiers from `cfg.SNMP`. Subset of `ping` by construction (both
	// derive from `kept`, cf. selectWatch) — but the decision does NOT rely on
	// that inclusion; it consults the set matching the required authority. The day
	// construction changes, the rule would not move an inch.
	poll map[string]bool
	// at: instant of the retained push, for the journal. Knowing a refusal relies
	// on configuration three seconds or three days old is the first question one
	// asks when reading the line.
	at time.Time
}

// judgeProxyScope decides. PURE function, and that is the rule for the batch:
// the package has no database in tests, and this is where the refusal protecting
// the whole fleet's state is taken — like `judgeCollector`, `judgeTrapProof`, and
// `effectiveInterval`.
//
// `known` comes from memory LOOKUP, and is distinct from a scope with empty sets:
// a proxy covering no site legitimately receives an empty configuration, and its
// events are then refused as `scopeNotEntrusted` — a definitive refusal, not a
// startup race. Confusing the two would wait indefinitely for a push that already
// happened.
//
// The ORDER of lookups is the message. For a polling fact, look at `poll` first —
// the only thing that decides — and `ping` second, ONLY to choose between « it
// overstepped its role » and « it speaks about a device that does not concern
// it ». Reversing would make the verdict depend on the larger set, which is
// exactly the hole we are closing.
func judgeProxyScope(sc proxyScope, known bool, need proxyAuthority,
	deviceID string) scopeVerdict {
	if !known {
		return scopeNoConfig
	}
	if need == authorityPoll {
		if sc.poll[deviceID] {
			return scopeGranted
		}
		if sc.ping[deviceID] {
			return scopeNotCollected
		}
		return scopeNotEntrusted
	}
	if sc.ping[deviceID] {
		return scopeGranted
	}
	return scopeNotEntrusted
}

// authorityFor says which authority an event type requires, and whether control
// APPLIES.
//
// Pure, and the table is here rather than in `handleProxyEvent`'s `switch` so a
// test can read it whole: this is the kind of mapping where a forgotten type
// breaks nothing at compile time and opens a silent hole.
//
// The second return is false for SWEEP events (`host.seen`, `sweep.done`), and
// that is a choice, not an omission. They carry no `DeviceID`: they speak about a
// RANGE declared on the proxy itself (`ListScanPrefixes`) — a proxy therefore
// cannot make them refer to someone else's device. And they write nothing:
// `host.seen` is a `slog.Debug` that deliberately creates no candidate,
// `sweep.done` an application journal line. There is no state to protect.
//
// An UNKNOWN type returns false too, and that opens nothing: `handleProxyEvent`
// does not handle it. Refusing higher up an event nobody consumes would only add
// a journal line per proxy version newer than the controller.
func authorityFor(evType string) (proxyAuthority, bool) {
	switch evType {
	case tunnel.EventDeviceUp, tunnel.EventDeviceDown:
		// PING alone: site coverage authorizes it, because several proxies
		// legitimately ping the same device.
		return authorityPing, true
	case tunnel.EventPortUp, tunnel.EventPortDown, tunnel.EventTrapSeen:
		// All three facts derive from SNMP polling. `trap.seen` belongs here
		// although it comes from a datagram not a poll: the proxy accepts a trap
		// only from a source in its SNMP targets with THAT target's community
		// (cf. classifyTrap), so the proof does derive from collection.
		return authorityPoll, true
	}
	return authorityPing, false
}

// --- Retained set and lookup ---

// scopeRefusalEvery amortizes refusal logging, per proxy.
//
// The refusal must be LOUD — a proxy speaking about a device not entrusted to
// it is a defect or an attack, and neither should be silent — but a looping proxy
// must not drown the journal, which would be denial of service on the only thing
// that would diagnose it. One minute, the value of `dropReportEvery`
// (cmd/proxy/snmp.go), and the same exact compromise: the FIRST line is said
// immediately, the following ones are grouped with their count.
const scopeRefusalEvery = time.Minute

// scopeRefusals: journal amortization for one proxy.
type scopeRefusals struct {
	// saidAt: when we last said it. Zero = never.
	saidAt time.Time
	// since: refusals since the last line. This count goes to the journal, never
	// the cumulative total: « 4,200 refusals since the last minute » is actionable;
	// « 3,100,000 since startup » is endured (cf. reportSaturation).
	since uint64
	// total: cumulative, for the same line. It says how long this has been going on,
	// which the period count does not.
	total uint64
}

// scopeSaysNow: should this refusal be said NOW? Pure, so amortization can be
// exercised without waiting a minute.
//
// Zero `saidAt` = nothing said yet, and the first occurrence is said immediately:
// amortization must prevent REPETITION of the symptom, never delay its first
// appearance. A refusal that happens only once — the startup race case — would
// otherwise be the only one never seen.
func scopeSaysNow(saidAt, now time.Time, every time.Duration) bool {
	return saidAt.IsZero() || now.Sub(saidAt) >= every
}

// scopeRefusalPhrase says what the refusal means, and the GESTURE that fixes it.
//
// One sentence per verdict, like `trapDestRefusal`: « fact refused » would fit all
// three and help in none. They are repaired by gestures that have nothing in
// common — wait a few seconds, fix an attachment, or investigate why a proxy
// speaks about a site it does not cover.
func scopeRefusalPhrase(v scopeVerdict) string {
	switch v {
	case scopeNoConfig:
		return "no watch configuration has been entrusted to this proxy yet; " +
			"it is computed on tunnel connection, and this refusal disappears by itself " +
			"once it is pushed — if it persists, the computation is failing"
	case scopeNotCollected:
		return "this proxy does ping this device but is NOT its collector: it " +
			"does not poll it in SNMP and therefore cannot know anything about its ports. " +
			"Either an attachment just changed and this proxy is still working from the old " +
			"instruction, or it is reporting facts it did not establish"
	case scopeNotEntrusted:
		return "this device was NEVER entrusted to this proxy, in any capacity: it is " +
			"not in its site coverage. A proxy cannot have observed it — this fact is " +
			"fabricated, or this proxy runs on a configuration that is not its own " +
			"(instance migration?)"
	}
	return ""
}

// proxyScopes retains, per proxy, what the last push entrusted to it.
//
// # Memory bound
//
// One entry per ENROLLED proxy, and that is a real bound not a hope: the key is
// a `proxy_id` that passed `AuthProxy` (cf. handleProxyTunnel and
// handleProxyMetrics), therefore a row in `proxies`. An attacker cannot forge
// them, and a fleet has a handful. Each entry weighs two keys per watched device
// — the same order of magnitude as what `portFlaps` already holds.
//
// Purge happens on each `record`, against LIVE tunnels: that is the only hook
// this file has; `handleProxyTunnel` (which calls `WatchExcl.forget` on close)
// is outside its scope. That suffices, because a push happens on every proxy
// connection: an entry for a removed proxy disappears at the next connection of
// any neighbor. And meanwhile it is inert, cf. this file's header.
//
// No bound on COUNT, deliberately. It could apply only by evicting a live proxy's
// entry — i.e. stopping its supervision — or by refusing to record its own,
// which amounts to the same. Faced with a closed key space, the bound would be
// worse than what it protects.
//
// # Concurrency
//
// `Hub.readEvents` starts one read goroutine PER PROXY, so several proxies read
// in parallel; `PushWatchTo` writes from others still (tunnel connection,
// attachment change, prefix edits). Hence an `RWMutex`: lookup is the hot path and
// takes a shared lock; record and refusal accounting take the exclusive lock.
// Refusal being amortized, promotion to write pays only at the rare moments
// something goes wrong.
type proxyScopes struct {
	mu     sync.RWMutex
	scopes map[string]proxyScope
	// refusals: journal amortization, per proxy. Purged with the scope.
	refusals map[string]*scopeRefusals
}

func newProxyScopes() *proxyScopes {
	return &proxyScopes{
		scopes:   map[string]proxyScope{},
		refusals: map[string]*scopeRefusals{},
	}
}

// record retains what the push just entrusted to `proxyID`, and replaces ENTIRELY
// what was there before.
//
// Total replacement is what makes the set unable to go stale: a `WatchConfig` is
// complete by construction (cf. Hub.PushWatch, « a complete config replaces the
// previous one — no delta to reconcile »), so a device removed from supervision
// disappears from here at the same instant it disappears from the proxy. Retaining
// deltas would reopen the hole in another form: a detached device would stay
// authorized.
//
// `live` says whether a proxy still has a tunnel; nil = unknown, and we purge
// nothing rather than empty memory we might need. It is a parameter not a direct
// call to `s.Tunnels.Connected` so purge can be exercised without Hub, like the
// rest of this file.
//
// `now` is a parameter for the same reason as in `allow`: the ONLY date this
// file compares is `now - sc.at`, and both reads must come from the same clock.
// A hard-coded `time.Now()` here and an injected instant there would make two
// clocks coexist in the same subtraction — the exact defect portevents.go fixed —
// and a test would see a negative instruction age in the journal.
func (r *proxyScopes) record(proxyID string, cfg tunnel.WatchConfig, now time.Time,
	live func(string) bool) {
	if r == nil || proxyID == "" {
		return
	}
	var ping = make(map[string]bool, len(cfg.Targets))
	var t tunnel.WatchTarget
	for _, t = range cfg.Targets {
		if t.DeviceID != "" {
			ping[t.DeviceID] = true
		}
	}
	var poll = make(map[string]bool, len(cfg.SNMP))
	var w tunnel.SNMPWatch
	for _, w = range cfg.SNMP {
		if w.DeviceID != "" {
			poll[w.DeviceID] = true
		}
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.scopes[proxyID] = proxyScope{ping: ping, poll: poll, at: now}
	// Amortization starts over: a new instruction is a new situation, and
	// silencing the first refusal after it would miss precisely the one that
	// denounces a miscalculated push.
	delete(r.refusals, proxyID)
	r.purgeLocked(proxyID, live)
}

// purgeLocked forgets proxies that no longer have a tunnel. Called with lock held.
//
// Never the one just recorded: when `PushWatchTo` runs, the session is up but
// `Connected` may well answer false if the tunnel just dropped — and we would
// lose the record we just made, therefore the events from the next proxy to
// reconnect.
func (r *proxyScopes) purgeLocked(keep string, live func(string) bool) {
	if live == nil {
		return
	}
	var id string
	for id = range r.scopes {
		if id == keep || live(id) {
			continue
		}
		delete(r.scopes, id)
		delete(r.refusals, id)
	}
}

// allow looks up the retained set and logs the refusal, in a bounded way.
//
// Returns false on a nil receiver, and that is deliberately the opposite of nil
// tolerance elsewhere in the package (`portFlaps`, `WatchExcl`, `live`, whose
// methods do nothing rather than panic). Those correlate or notify in the void,
// which is consequence-free; this one AUTHORIZES. An access control that passes
// when its memory is missing is not access control. That case does not happen in
// production — `Router()` mounts memory before the first proxy can connect — and
// that is precisely why closing it costs nothing.
//
// `now` is a PARAMETER, with `time.Now()` written literally at the production
// call site (cf. allowProxyFact): that is portevents.go doctrine, and it applies
// here for journal amortization, counted in minutes and otherwise untestable.
func (r *proxyScopes) allow(proxyID string, need proxyAuthority, deviceID string,
	now time.Time) bool {
	if r == nil {
		return false
	}
	r.mu.RLock()
	var sc, known = r.scopes[proxyID]
	r.mu.RUnlock()

	var verdict = judgeProxyScope(sc, known, need, deviceID)
	if verdict == scopeGranted {
		return true
	}
	r.refuse(proxyID, verdict, need, deviceID, sc, now)
	return false
}

// refuse counts the refusal and says it, at most once per proxy per
// scopeRefusalEvery.
//
// At `Warn`. That is the severity `reportSaturation` uses for measurement loss;
// a proxy speaking about a device not entrusted to it is at least as serious, and
// has only two explanations — faulty fleet wiring, or someone trying.
// `trapProofForeign` settles for `Info`, but its case is benign by nature (a
// device carrying a trap destination set by hand); here the fact was REFUSED, and
// a mute refusal would look like a meshcraft fault.
//
// The line carries proxy, device, missing authority, and instruction age — enough
// to decide among the three causes without guessing. And both counters, because
// an isolated refusal and a loop are not handled the same.
func (r *proxyScopes) refuse(proxyID string, verdict scopeVerdict, need proxyAuthority,
	deviceID string, sc proxyScope, now time.Time) {
	r.mu.Lock()
	var tally = r.refusals[proxyID]
	if tally == nil {
		tally = &scopeRefusals{}
		r.refusals[proxyID] = tally
	}
	tally.since++
	tally.total++
	if !scopeSaysNow(tally.saidAt, now, scopeRefusalEvery) {
		r.mu.Unlock()
		return
	}
	var since, total = tally.since, tally.total
	tally.saidAt, tally.since = now, 0
	var configAge = "never"
	if !sc.at.IsZero() {
		configAge = now.Sub(sc.at).Round(time.Second).String()
	}
	var watched, collected = len(sc.ping), len(sc.poll)
	r.mu.Unlock()

	slog.Warn("fact REFUSED: this proxy speaks about a device not entrusted to it — "+
		scopeRefusalPhrase(verdict),
		"proxy_id", proxyID, "device_id", deviceID,
		"authority_required", authorityLabel(need), "verdict", verdictLabel(verdict),
		"refusals_since", since, "refusals_total", total,
		"instruction_age", configAge,
		"devices_entrusted", watched, "snmp_targets_entrusted", collected)
}

// authorityLabel / verdictLabel: the two journal labels.
//
// Words, not integers. A « verdict: 2 » would force opening this file to read a
// journal line, which is exactly what one does not do at three in the morning.
func authorityLabel(need proxyAuthority) string {
	if need == authorityPoll {
		return "snmp_collection"
	}
	return "ping"
}

func verdictLabel(v scopeVerdict) string {
	switch v {
	case scopeGranted:
		return "granted"
	case scopeNoConfig:
		return "no_instruction_pushed"
	case scopeNotCollected:
		return "pinged_not_collected"
	case scopeNotEntrusted:
		return "never_entrusted"
	}
	return "unknown"
}

// allowProxyFact is THE production call site: the controller clock is read here,
// and `time.Now()` is written literally.
//
// A method not a direct call to `s.proxyScopes.allow` on each write path, for two
// reasons: memory is reached only here, so there is one place to reread what the
// control consults; and a test can see at a glance that all production calls pass
// the controller clock — the same property portevents_test.go locks for
// correlation.
func (s *Server) allowProxyFact(proxyID string, need proxyAuthority, deviceID string) bool {
	return s.proxyScopes.allow(proxyID, need, deviceID, time.Now())
}

// recordProxyScope retains what a push entrusted. Counterpart to allowProxyFact —
// the controller clock is read here, literally — and the only point through which
// memory is filled, guaranteeing it cannot say anything other than what went out
// on the tunnel.
//
// The liveness predicate is resolved HERE not at the caller: `PushWatchTo` must
// not know memory is purged, and a caller forgetting to pass it would let memory
// grow with nothing failing.
func (s *Server) recordProxyScope(proxyID string, cfg tunnel.WatchConfig) {
	s.proxyScopes.record(proxyID, cfg, time.Now(), s.tunnelLive())
}

// --- Decide ONCE per device, for a whole batch ---

// scopeBatch memoizes scope verdict per device, for ONE batch's duration.
//
// The pattern was born in `applyPortTelemetry` and lives here because a second
// path needs it identically — the metrics filter (proxymetrics_handlers.go).
// Copying it would be the easiest fault in the batch: two variants of the same
// access control always diverge, and the one that diverges opens a hole without
// breaking compilation.
//
// Memoization is not cosmetic optimization. A full batch holds twenty thousand
// samples for a few dozen devices: consulting per sample would, on a refused
// batch, make twenty thousand passes through `refuse` — therefore twenty thousand
// promotions from shared to exclusive lock on the hot path, and a refusal counter
// counting measurements where the journal announces facts. A FALSE entry is
// therefore a result we keep, not « not decided yet »: that bounds the journal to
// one line per refused device per batch.
//
// Carries its `Server` and proxy: scope is that of a (controller, proxy) pair,
// and a batch never mixes two proxies — `handleProxyMetrics` and `Hub.readEvents`
// each work on the one they just authenticated.
type scopeBatch struct {
	srv     *Server
	proxyID string
	need    proxyAuthority
	granted map[string]bool
}

// scopeBatchFor opens a batch decision. `need` is fixed at open because a batch
// does not mix two authorities: telemetry like metrics come from SNMP polling, and
// an event batch is judged fact by fact.
func (s *Server) scopeBatchFor(proxyID string, need proxyAuthority) *scopeBatch {
	return &scopeBatch{srv: s, proxyID: proxyID, need: need, granted: map[string]bool{}}
}

// allows returns the verdict for a device, computing it at most once.
//
// An EMPTY identifier is refused without consulting memory, and that is the only
// case where this path bypasses `allowProxyFact`: there is nothing to consult, no
// entry can hold the empty key (cf. `record`, which skips targets without
// identifier), and pushing this case to the scope refusal journal would make a
// scope verdict carry a sample SHAPE defect. Counting those samples belongs to the
// caller, which knows what it counts.
func (b *scopeBatch) allows(deviceID string) bool {
	if b == nil || deviceID == "" {
		return false
	}
	var allowed, decided = b.granted[deviceID]
	if decided {
		return allowed
	}
	allowed = b.srv.allowProxyFact(b.proxyID, b.need, deviceID)
	b.granted[deviceID] = allowed
	return allowed
}

// tunnelLive returns the « does this proxy still have a tunnel? » predicate, or
// nil when there is no Hub — a `Server` built by hand in a test. Nil purges
// nothing, which is the right default: better one extra inert entry than memory
// emptied under a live proxy's feet.
func (s *Server) tunnelLive() func(string) bool {
	if s == nil || s.Tunnels == nil {
		return nil
	}
	return s.Tunnels.Connected
}
