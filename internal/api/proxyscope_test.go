package api

import (
	"strings"
	"sync"
	"testing"
	"time"

	"meshcraft/internal/tunnel"
)

// A proxy's scope: which device is it allowed to speak about?
//
// What these tests lock in: a compromised proxy — or simply one running another
// instance's configuration after a migration — must not be able to write the state of
// a device it was not entrusted with. The defect had no loud failure mode: a switch's
// `off` status, a "port down" row in the fleet log, and a dark link in inventory look
// indistinguishable from reality on screen. That is the kind of fault one only
// discovers while looking for something else.
//
// Without a database, like the rest of the package: `judgeProxyScope`, `authorityFor`,
// `scopeSaysNow`, and `scopeRefusalPhrase` were written PURE so they can be verified
// here, following `judgeCollector`, `judgeTrapProof`, and `effectiveInterval`. The
// memory mechanics (`proxyScopes`) are also proven directly, without Hub or tunnel,
// because `record` receives its liveness predicate as a parameter.
//
// What a test in this package CANNOT cover, and it must be said so this list is not
// mistaken for end-to-end proof:
//
//   - the CONTENT of the two sets. That `cfg.Targets` is site coverage and `cfg.SNMP`
//     is attachment depends on `DevicesWatchedBy`, `ProxyDomains`, and the vault. The
//     pure part of that computation is proven by watch_attach_test.go; here we take a
//     `WatchConfig` as-is and verify what we RETAIN from it;
//   - the real STARTUP order. That `AcceptEvents` may read before `PushWatchTo` has
//     pushed requires two goroutines, a yamux tunnel, and a database: that is noted by
//     reading the code (cf. the header of proxyscope.go) and handled by set survival
//     across reconnection, which is proven here;
//   - the WIRING of the three write paths. That `handleProxyEvent` and
//     `applyPortTelemetry` consult scope BEFORE writing is visible in those two
//     functions; proving it would require a database, a tunnel, and metrics storage.
//     What is locked here is the `authorityFor` table — the only place a forgotten
//     event type would open a hole without breaking compilation.

// scopeT0: a fixed date. Thresholds in this file are counted in minutes, and proving
// them on `time.Now()` would require actually waiting.
var scopeT0 = time.Date(2026, 7, 28, 14, 12, 0, 0, time.UTC)

// twoSites: configuration for a proxy that covers one site and only collects part of
// it — the NORMAL situation since migration 0022, and the one that distinguishes the
// two granularities.
//
//	sw-a1: pinged AND collected by this proxy;
//	sw-a2: pinged only — another proxy is its declared collector;
//	sw-b1: from another site, never entrusted (therefore absent from both lists).
func twoSites() tunnel.WatchConfig {
	return tunnel.WatchConfig{
		Targets: []tunnel.WatchTarget{
			{DeviceID: "sw-a1", IP: "10.0.0.1", Name: "SW-A1"},
			{DeviceID: "sw-a2", IP: "10.0.0.2", Name: "SW-A2"},
		},
		SNMP: []tunnel.SNMPWatch{
			{DeviceID: "sw-a1", IP: "10.0.0.1", Community: "s3cr3t"},
		},
	}
}

// scopeOf: the set retained for a proxy, as `record` builds it.
func scopeOf(t *testing.T, r *proxyScopes, proxyID string) proxyScope {
	t.Helper()
	r.mu.RLock()
	defer r.mu.RUnlock()
	var sc, known = r.scopes[proxyID]
	if !known {
		t.Fatalf("no set retained for %q", proxyID)
	}
	return sc
}

// --- The decision, pure ---

// TestScopePingAllowsReachability: the device is in this proxy's ICMP targets, so
// `device.up` / `device.down` are acceptable.
//
// This is the MAJORITY case in production, and the first to verify: verification that
// is too strict would stop monitoring a fleet in service.
func TestScopePingAllowsReachability(t *testing.T) {
	var r = newProxyScopes()
	r.record("px-a", twoSites(), scopeT0, nil)
	var sc = scopeOf(t, r, "px-a")

	if judgeProxyScope(sc, true, authorityPing, "sw-a1") != scopeGranted {
		t.Error("a device entrusted for ICMP allows its reachability")
	}
	// sw-a2 is pinged without being collected: its reachability remains acceptable, and
	// that is the whole point of the distinction. Attachment does NOT restrict ping —
	// it provides reachability failover the day a proxy goes down.
	if judgeProxyScope(sc, true, authorityPing, "sw-a2") != scopeGranted {
		t.Error("attachment does not restrict ping: sw-a2 remains pinged by this proxy")
	}
}

// TestScopePollRequiresCollector: THE test of the batch. The same device, pinged by
// this proxy but collected by another, must not report port transitions.
//
// A proxy that did so would be at fault even if its coverage were impeccable: port
// transitions come from SNMP polling, and it does not poll that device. Allowing on
// coverage alone would have let writes into the fleet log and port inventory for
// someone else's device, which is precisely the most rewarding attack.
func TestScopePollRequiresCollector(t *testing.T) {
	var r = newProxyScopes()
	r.record("px-a", twoSites(), scopeT0, nil)
	var sc = scopeOf(t, r, "px-a")

	if judgeProxyScope(sc, true, authorityPoll, "sw-a1") != scopeGranted {
		t.Error("the declared collector may report its device's ports")
	}
	var verdict = judgeProxyScope(sc, true, authorityPoll, "sw-a2")
	if verdict != scopeNotCollected {
		t.Fatalf("a port transition on a pinged but NOT collected device must be refused: %s",
			verdictLabel(verdict))
	}
	// The verdict must be the SPECIFIC one, not "never entrusted": that is what
	// distinguishes in the log a proxy overstepping its role from a proxy speaking
	// about a site it does not cover. The two causes are fixed in opposite ways.
	if scopeRefusalPhrase(verdict) == "" {
		t.Error("a refusal must say why, otherwise it looks like a bug")
	}
	if !strings.Contains(scopeRefusalPhrase(verdict), "collector") {
		t.Errorf("the phrase must name attachment: %s", scopeRefusalPhrase(verdict))
	}
}

// TestScopeForeignDevice: a device from another site, never entrusted to this proxy.
// Everything is refused, at both granularities, and the refusal is named.
func TestScopeForeignDevice(t *testing.T) {
	var r = newProxyScopes()
	r.record("px-a", twoSites(), scopeT0, nil)
	var sc = scopeOf(t, r, "px-a")

	var need proxyAuthority
	for _, need = range []proxyAuthority{authorityPing, authorityPoll} {
		var verdict = judgeProxyScope(sc, true, need, "sw-b1")
		if verdict != scopeNotEntrusted {
			t.Errorf("a device from another site is refused for %s: %s",
				authorityLabel(need), verdictLabel(verdict))
		}
	}
	if !strings.Contains(scopeRefusalPhrase(scopeNotEntrusted), "NEVER") {
		t.Error("a serious refusal must read as such")
	}
}

// TestScopeUnknownProxy: nothing was ever pushed to this proxy. Everything is refused,
// without panicking, and the verdict distinguishes itself from a definitive refusal.
//
// The distinction is not cosmetic: `scopeNoConfig` is a startup race that resolves
// itself; `scopeNotEntrusted` requires investigation. Confusing them would look for an
// attack on every controller reboot.
func TestScopeUnknownProxy(t *testing.T) {
	var r = newProxyScopes()
	var verdict = judgeProxyScope(proxyScope{}, false, authorityPing, "sw-a1")
	if verdict != scopeNoConfig {
		t.Fatalf("a proxy entrusted with nothing distinguishes itself from a definitive refusal: %s",
			verdictLabel(verdict))
	}
	// And through the full path, the one that consults memory: no panic on an empty map,
	// and a refusal.
	if r.allow("px-never-seen", authorityPing, "sw-a1", scopeT0) {
		t.Error("a proxy entrusted with nothing cannot assert anything")
	}
	if r.allow("px-never-seen", authorityPoll, "", scopeT0) {
		t.Error("an event without a device is acceptable from nobody")
	}
}

// TestScopeEmptyConfigIsNotAbsence: a proxy covering no site legitimately receives an
// EMPTY configuration. Its facts are then refused definitively, not "waiting for the
// push".
//
// That is why `known` is a separate parameter and not deduced from set size: confusing
// the two would wait indefinitely for a push that already happened, and would hide
// from the log the real problem — a proxy without coverage still reporting facts.
func TestScopeEmptyConfigIsNotAbsence(t *testing.T) {
	var r = newProxyScopes()
	r.record("px-bare", tunnel.WatchConfig{}, scopeT0, nil)
	var sc = scopeOf(t, r, "px-bare")
	var verdict = judgeProxyScope(sc, true, authorityPing, "sw-a1")
	if verdict != scopeNotEntrusted {
		t.Fatalf("an EMPTY configuration PUSHED is a definitive refusal, not a wait: %s",
			verdictLabel(verdict))
	}
}

// TestScopeAuthorityTable locks the event type → authority mapping.
//
// This is the only place in the batch where an omission opens no compile error: an
// event type missing from the table passes control without being seen. The table is
// therefore read in full, including what it does NOT control.
func TestScopeAuthorityTable(t *testing.T) {
	var cas = []struct {
		evType string
		need   proxyAuthority
		gated  bool
	}{
		// Ping: site coverage is enough; several proxies ping the same device.
		{tunnel.EventDeviceUp, authorityPing, true},
		{tunnel.EventDeviceDown, authorityPing, true},
		// SNMP polling: the collector, and only the collector.
		{tunnel.EventPortUp, authorityPoll, true},
		{tunnel.EventPortDown, authorityPoll, true},
		{tunnel.EventTrapSeen, authorityPoll, true},
		// Sweep: no DeviceID, no state write, a prefix declared on the proxy itself.
		// Nothing to protect, and gating it would refuse by sweep.
		{tunnel.EventHostSeen, authorityPing, false},
		{tunnel.EventSweepDone, authorityPing, false},
		// A type no controller version handles.
		{"something.new", authorityPing, false},
	}
	var c int
	for c = range cas {
		var need, gated = authorityFor(cas[c].evType)
		if gated != cas[c].gated {
			t.Errorf("%s: gated=%v, expected %v", cas[c].evType, gated, cas[c].gated)
			continue
		}
		if gated && need != cas[c].need {
			t.Errorf("%s: authority %s, expected %s", cas[c].evType,
				authorityLabel(need), authorityLabel(cas[c].need))
		}
	}
}

// --- The retained set follows the fleet ---

// TestScopeFollowsPushes: the fleet changes, the retained set changes with it, both
// ways.
//
// This is the property that keeps the set from going STALE, and it depends on a push
// replacing everything: a device removed from monitoring stops being authorized at the
// same instant it stops being watched. Retaining deltas would reopen the hole in
// another form — a detached device would stay authorized indefinitely.
func TestScopeFollowsPushes(t *testing.T) {
	var r = newProxyScopes()
	r.record("px-a", twoSites(), scopeT0, nil)
	if !r.allow("px-a", authorityPoll, "sw-a1", scopeT0) {
		t.Fatal("initial state: sw-a1 is collected by px-a")
	}

	// A push that REMOVES sw-a1 from monitoring (device deleted, site removed from
	// coverage, address cleared).
	r.record("px-a", tunnel.WatchConfig{
		Targets: []tunnel.WatchTarget{{DeviceID: "sw-a2", IP: "10.0.0.2"}},
	}, scopeT0, nil)
	if r.allow("px-a", authorityPoll, "sw-a1", scopeT0) {
		t.Error("a device removed from monitoring no longer authorizes anything")
	}
	if r.allow("px-a", authorityPing, "sw-a1", scopeT0) {
		t.Error("removed from ICMP targets, its reachability is no longer acceptable either")
	}

	// A push that ADDS it back, for collection this time.
	r.record("px-a", twoSites(), scopeT0, nil)
	if !r.allow("px-a", authorityPoll, "sw-a1", scopeT0) {
		t.Error("a device returned to this proxy becomes acceptable again without another gesture")
	}
}

// TestScopeSurvivesReconnection: the set is NOT purged when a proxy disconnects, and
// that is the least obvious decision in the batch.
//
// On the proxy side, `monitor` is a process singleton: its configuration and 256-event
// queue survive tunnel loss, and `SendEvents` drains that queue as soon as the session
// is restored. On the controller side, `AcceptEvents` and `PushWatchTo` start in two
// goroutines and the second begins with several database round trips. The first packet
// of a reconnection therefore arrives BEFORE the new instruction — and that packet
// carries exactly the transitions accumulated during the outage, i.e. the only ones
// nobody saw.
//
// Purging on disconnect would have lost them all, on every reconnection. We therefore
// keep the set: the proxy still works from the old instruction, and it must be judged
// against that. It opens nothing — a retained set only serves to judge a proxy that is
// speaking, therefore connected.
func TestScopeSurvivesReconnection(t *testing.T) {
	var r = newProxyScopes()
	// px-a and px-b are known; we then simulate px-b reconnecting alone.
	r.record("px-a", twoSites(), scopeT0, nil)
	r.record("px-b", tunnel.WatchConfig{
		Targets: []tunnel.WatchTarget{{DeviceID: "sw-b1", IP: "10.1.0.1"}},
		SNMP:    []tunnel.SNMPWatch{{DeviceID: "sw-b1", IP: "10.1.0.1"}},
	}, scopeT0, nil)

	// px-a lost its tunnel; px-b reconnects and repushes its instruction. Purge relies
	// on LIVE tunnels: px-a is forgotten, px-b kept.
	var live = func(id string) bool { return id == "px-b" }
	r.record("px-b", tunnel.WatchConfig{
		Targets: []tunnel.WatchTarget{{DeviceID: "sw-b1", IP: "10.1.0.1"}},
		SNMP:    []tunnel.SNMPWatch{{DeviceID: "sw-b1", IP: "10.1.0.1"}},
	}, scopeT0, live)

	r.mu.RLock()
	var _, aStillThere = r.scopes["px-a"]
	var nb = len(r.scopes)
	r.mu.RUnlock()
	if aStillThere {
		t.Error("a proxy without a tunnel must be forgotten: memory is bounded")
	}
	if nb != 1 {
		t.Errorf("one set per connected proxy: %d entries", nb)
	}
	if !r.allow("px-b", authorityPoll, "sw-b1", scopeT0) {
		t.Error("the reconnected proxy keeps its scope")
	}
}

// TestScopePurgeNeverSacrificesCurrentProxy: purge does not touch the one just
// recorded, even if the Hub already says it is disconnected.
//
// `PushWatchTo` runs in its own goroutine and the tunnel may drop in the meantime:
// purging on that criterion alone would discard the record we just made, and the first
// events of the next reconnection with it.
func TestScopePurgeNeverSacrificesCurrentProxy(t *testing.T) {
	var r = newProxyScopes()
	// `live` returns false for everyone, including the one we register.
	r.record("px-a", twoSites(), scopeT0, func(string) bool { return false })
	if !r.allow("px-a", authorityPing, "sw-a1", scopeT0) {
		t.Error("the record in progress is never its own victim")
	}
}

// TestScopePurgeWithoutPredicate: with no way to know who disappeared, purge nothing.
// Clearing memory a live proxy depends on would stop its monitoring, which is
// infinitely worse than one extra entry — which is inert.
func TestScopePurgeWithoutPredicate(t *testing.T) {
	var r = newProxyScopes()
	r.record("px-a", twoSites(), scopeT0, nil)
	r.record("px-b", twoSites(), scopeT0, nil)
	r.mu.RLock()
	var nb = len(r.scopes)
	r.mu.RUnlock()
	if nb != 2 {
		t.Errorf("without a liveness predicate, nothing is purged: %d entries", nb)
	}
}

// --- Log volume ---

// TestScopeRefusalFirstLineImmediate: the first refusal is spoken RIGHT AWAY.
//
// Damping must prevent repeating the symptom, never delay its appearance: a refusal
// that only happens once — the startup race case — would otherwise be the only one
// never seen.
func TestScopeRefusalFirstLineImmediate(t *testing.T) {
	if !scopeSaysNow(time.Time{}, scopeT0, scopeRefusalEvery) {
		t.Error("the first occurrence is spoken immediately")
	}
}

// TestScopeRefusalDamped: a looping proxy does not flood the log, and the count of
// suppressed refusals is kept for the next line.
//
// The compromise is that of `reportSaturation`: one line per period, carrying the
// count for THE PERIOD and the cumulative total. "4,200 refusals since the last
// minute" is actionable; one line per refused transition on a flapping fleet would
// be denial of service on the only thing that would let one diagnose it.
func TestScopeRefusalDamped(t *testing.T) {
	var r = newProxyScopes()
	r.record("px-a", twoSites(), scopeT0, nil)

	// A thousand refusals in the same second: one line spoken, a thousand counted.
	var i int
	for i = 0; i < 1000; i++ {
		if r.allow("px-a", authorityPoll, "sw-b1", scopeT0) {
			t.Fatal("sw-b1 was never entrusted to px-a")
		}
	}
	r.mu.RLock()
	var tally = *r.refusals["px-a"]
	r.mu.RUnlock()
	if tally.total != 1000 {
		t.Errorf("the cumulative total counts all refusals: %d", tally.total)
	}
	// The first line was spoken and reset the period counter; the next 999 are
	// suppressed but counted.
	if tally.since != 999 {
		t.Errorf("suppressed refusals stay counted for the next line: %d", tally.since)
	}
	if !tally.saidAt.Equal(scopeT0) {
		t.Errorf("the first line is timestamped: %v", tally.saidAt)
	}

	// Within the period: still nothing spoken.
	if scopeSaysNow(tally.saidAt, scopeT0.Add(scopeRefusalEvery-time.Second),
		scopeRefusalEvery) {
		t.Error("within the period, the refusal stays silent")
	}
	// Beyond it: the line returns, with the accumulated count.
	if !scopeSaysNow(tally.saidAt, scopeT0.Add(scopeRefusalEvery), scopeRefusalEvery) {
		t.Error("once the period elapsed, the symptom is spoken again")
	}
	r.allow("px-a", authorityPoll, "sw-b1", scopeT0.Add(scopeRefusalEvery))
	r.mu.RLock()
	tally = *r.refusals["px-a"]
	r.mu.RUnlock()
	if tally.since != 0 || tally.total != 1001 {
		t.Errorf("a spoken line resets the period and keeps the cumulative total: %d / %d",
			tally.since, tally.total)
	}
}

// TestScopeNewPushRearmsLogging: a new instruction is a new situation. Silencing the
// first refusal after it would miss precisely the one that denounces a miscalculated
// push.
func TestScopeNewPushRearmsLogging(t *testing.T) {
	var r = newProxyScopes()
	r.record("px-a", twoSites(), scopeT0, nil)
	r.allow("px-a", authorityPoll, "sw-b1", scopeT0)
	r.mu.RLock()
	var said = r.refusals["px-a"] != nil
	r.mu.RUnlock()
	if !said {
		t.Fatal("the refusal must have been counted")
	}
	r.record("px-a", twoSites(), scopeT0, nil)
	r.mu.RLock()
	var tally = r.refusals["px-a"]
	r.mu.RUnlock()
	if tally != nil {
		t.Error("a new push re-arms log damping")
	}
}

// TestScopeNullReceiverRefuses: an access control whose memory is missing refuses.
//
// This is deliberately the opposite of nil tolerance elsewhere in the package
// (`portFlaps`, `WatchExcl`, `live`, which correlate or notify into the void): those
// are harmless; this one AUTHORIZES. The case does not happen in production —
// `Router()` mounts memory before the first proxy can connect — and that is exactly
// why closing it costs nothing.
func TestScopeNullReceiverRefuses(t *testing.T) {
	var r *proxyScopes
	if r.allow("px-a", authorityPing, "sw-a1", scopeT0) {
		t.Error("without memory, access control refuses; it does not let things through")
	}
	// `record` on a nil receiver does not panic either: a hand-built `Server` in a
	// test should not have to worry about that.
	r.record("px-a", twoSites(), scopeT0, nil)
}

// --- Concurrency ---

// TestScopeConcurrency: pushes and lookups in parallel.
//
// `Hub.readEvents` starts one read goroutine PER PROXY — that is even the case we care
// about, since several proxies legitimately ping the same device — and `PushWatchTo`
// writes from yet others: tunnel connection, attachment change, sweep prefix change.
// Verify under `-race`: unprotected access would show up in production, not in
// development.
//
// Refusal is the most exposed path, because it PROMOTES the shared lock to an
// exclusive lock mid-decision — we therefore exercise both outcomes in parallel, granted
// and refused.
func TestScopeConcurrency(t *testing.T) {
	var r = newProxyScopes()
	var wg sync.WaitGroup
	var proxies = []string{"px-a", "px-b", "px-c", "px-d"}

	var p int
	for p = range proxies {
		// Pushes: each replaces the whole set, alternating two configurations so the
		// map really changes under readers.
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			var i int
			for i = 0; i < 300; i++ {
				if i%2 == 0 {
					r.record(id, twoSites(), scopeT0, func(string) bool { return true })
					continue
				}
				r.record(id, tunnel.WatchConfig{
					Targets: []tunnel.WatchTarget{{DeviceID: "sw-a2", IP: "10.0.0.2"}},
				}, scopeT0, nil)
			}
		}(proxies[p])
		// Lookups: one granted fact, one refused fact, alternating.
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			var i int
			for i = 0; i < 300; i++ {
				r.allow(id, authorityPing, "sw-a2", scopeT0.Add(time.Duration(i)*time.Second))
				r.allow(id, authorityPoll, "sw-b1", scopeT0.Add(time.Duration(i)*time.Second))
			}
		}(proxies[p])
	}
	wg.Wait()
}
