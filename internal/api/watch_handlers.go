package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"meshcraft/internal/driver"
	"meshcraft/internal/model"
	"meshcraft/internal/netscope"
	"meshcraft/internal/renumber"
	"meshcraft/internal/snmppoll"
	"meshcraft/internal/snmpprov"
	"meshcraft/internal/tunnel"
)

// Monitoring: what the controller asks of proxies, and what it does with the answers.
//
// The controller pings nothing. It sends each proxy the list of devices on sites it
// covers and prefixes declared on it; the proxy pings locally and only reports
// TRANSITIONS (cf. docs/01-Architecture/Proxy and internal/tunnel/control.go).

// PushWatchTo (re)pushes watch configuration to a proxy.
func (s *Server) PushWatchTo(ctx context.Context, proxyID string) {
	var cfg, bilan, err = s.watchConfigFor(ctx, proxyID)
	if err != nil {
		slog.Error("monitoring: configuration not computed", "err", err, "proxy_id", proxyID)
		return
	}
	if err = s.Tunnels.PushWatch(proxyID, cfg); err != nil {
		slog.Warn("monitoring: configuration not sent", "err", err, "proxy_id", proxyID)
		return
	}
	// REMEMBER what we just entrusted: that is what makes verifiable, later and without a
	// query, that a fact reported by this proxy truly concerns a device in ITS watch (cf.
	// proxyscope.go). The controller already knew all of this, it just did not remember —
	// hence a compromised proxy could write the state of any device in the instance.
	//
	// AFTER the push, and only if it succeeded. Memory must reflect what the PROXY holds,
	// not what the controller wanted to tell it: a push that did not go out entrusted
	// nothing, and the proxy still works on the previous instruction — which therefore
	// remains, rightly, the one that authorizes it.
	//
	// This is also the only path through which configuration leaves (cf. Hub.PushWatch) and
	// it replaces the whole set ENTIRELY: a device removed from watch or attached elsewhere
	// ceases to be authorized the instant it ceases to be watched, with no expiry to manage.
	s.recordProxyScope(proxyID, cfg)
	// AND FORGET WHAT THIS PROXY NO LONGER WATCHES.
	//
	// A verdict only gets refreshed by a proxy that still watches the device, so a
	// row left behind by a detached proxy would hold its last opinion forever with
	// nothing able to overwrite it. That is not hypothetical: it is precisely how
	// the 2026-08-02 outage stayed stuck — proxies emit on TRANSITION only, so the
	// collector still holding "up" had nothing to say, and the false "down" simply
	// stayed.
	//
	// Runs after the push and after recordProxyScope, on the same principle: the
	// controller forgets an observation only once the proxy has actually been told
	// to stop making it.
	s.forgetDroppedReach(ctx, proxyID, cfg)
	// The summary reads as a SUBTRACTION, and that is what attachment change brings
	// (migration 0022): "this proxy pings thirty devices and polls only ten" is now normal,
	// whereas the two numbers alone looked like a failure. They especially did not say which
	// of the two causes produced it, and they are repaired by opposite actions — detach a
	// device (or extend coverage) on one side, configure an SNMP community on the other.
	//
	//	devices = snmp_targets + delegated_collection + no_snmp_identity
	//
	// Equality is a construction invariant (cf. snmpCandidates). A log line where it does not
	// hold exposes a calculation defect, not a fleet state: that is also why we log three
	// terms rather than a single "excluded" count.
	//
	// Stays at Info, with the rest of the summary: none of these gaps is this proxy's fault,
	// and two of them are even the policy we apply to it.
	slog.Info("monitoring pushed", "proxy_id", proxyID,
		"devices", len(cfg.Targets), "prefixes", len(cfg.Prefixes),
		"snmp_targets", len(cfg.SNMP),
		"delegated_collection", bilan.delegated,
		"no_snmp_identity", bilan.noIdentity)
}

// watchBilan: explains the gap between what a proxy PINGS and what it POLLS, without
// recalculating anything at read time.
//
// Two counters not one total: the two causes do not call for the same action, and a single
// "SNMP targets excluded: 20" would send the operator the wrong way half the time.
type watchBilan struct {
	// delegated: targets watched by this proxy that ANOTHER proxy is declared to collect.
	// Policy, not error.
	delegated int
	// noIdentity: targets this proxy MUST collect, but on which snmpWatchesFor found no
	// configurable SNMP identity — no v2c plan, no vault identifier, unreadable community.
	noIdentity int
}

// watchConfigFor builds the list: devices with an address, on sites this proxy covers, plus
// prefixes declared on it.
//
// The second return is the summary of targets EXCLUDED from SNMP collection only — it does
// not enter configuration pushed to the proxy (to whom we only send what it must do, never
// what we decided not to ask) but is indispensable to the caller's log, cf. PushWatchTo.
func (s *Server) watchConfigFor(ctx context.Context,
	proxyID string) (tunnel.WatchConfig, watchBilan, error) {
	var cfg = tunnel.WatchConfig{
		Targets: []tunnel.WatchTarget{}, Prefixes: []string{},
		PingIntervalMS: int(tunnel.DefaultPingInterval.Milliseconds()),
		DownAfter:      tunnel.DefaultDownAfter,
		SweepIntervalS: int(tunnel.DefaultSweepInterval.Seconds()),
	}
	var bilan watchBilan
	var eligible, err = s.Store.DevicesWatchedBy(ctx, proxyID)
	if err != nil {
		return cfg, bilan, err
	}
	// Site coverage says who is ELIGIBLE; it does not say who is identifiable. An instance
	// proxy covering two sites reusing 192.168.1.0/24 would get two devices at the same
	// address — and the proxy indexes watch by address. The second would silently overwrite
	// the first, and reported state would be whoever actually responds on THAT network: a
	// green light on a powered-off switch.
	//
	// Address domains disambiguate (cf. internal/netscope).
	var domains map[string]bool
	domains, err = s.Store.ProxyDomains(ctx, proxyID)
	if err != nil {
		return cfg, bilan, err
	}
	var sel = selectWatch(eligible, domains)
	cfg.Targets = s.withRenumberTargets(sel.targets)
	// A device removed from watch without a word would look like a meshcraft failure. We say
	// it, with the reason — that is also the only clue leading the operator to declare address
	// domains.
	//
	// The controller log was not enough: nobody reads server logs to understand why a badge
	// disappeared. State is therefore stored (it feeds the device record banner) and an event
	// is emitted, but ONLY when the device loses watch — i.e. when no proxy takes charge
	// anymore.
	var rej netscope.Rejected
	for _, rej = range sel.rejected {
		slog.Warn("monitoring: target excluded due to ambiguous address",
			"proxy_id", proxyID, "device_id", rej.DeviceID, "name", rej.Name,
			"ip", rej.IP, "reason", rej.Reason)
	}
	for _, rej = range s.WatchExcl.record(proxyID, sel.kept, sel.rejected) {
		var siteID, deviceID = rej.SiteID, rej.DeviceID
		_ = s.Store.InsertEvent(ctx, model.Event{
			SiteID: &siteID, DeviceID: &deviceID,
			Type: "watch.excluded", Severity: model.SevWarning, Actor: "meshcraft",
			Message: "\"" + rej.Name + "\" is no longer watched: " + rej.Reason,
		})
		s.NotifySiteChanged(siteID, ChangeSync)
	}

	bilan.delegated = sel.delegated
	// SNMP targets: controller decides WHO to poll and at what cadence, proxy executes. It
	// knows connector capabilities and the vault; the proxy knows neither.
	var trapCapable int
	cfg.SNMP, trapCapable = s.snmpWatchesFor(ctx, sel.collect)
	// What snmpWatchesFor excluded without saying, for lack of configurable SNMP identity.
	// Count is taken HERE, by difference, rather than returned by it: same information and
	// costs nothing, where a third return would imply this function distinguishes reasons it
	// does not.
	bilan.noIdentity = len(sel.collect) - len(cfg.SNMP)

	// Trap listening: decided by controller, like the rest of watch (cf.
	// tunnel.WatchConfig.TrapListen).
	//
	// Condition applies to RETAINED SNMP targets, not the fleet: a trap only ADVANCES the poll
	// that would have happened anyway (cf. tunnel.EventPortUp), so a trap concerning a device
	// this proxy does not poll advances nothing. And the connector must declare it can PUSH —
	// otherwise opening 162/udp on this proxy would mean holding a listen port on the
	// management network to receive nothing, an attack surface offered for no return.
	cfg.TrapListen = trapCapable > 0
	if cfg.TrapListen {
		// Set EXPLICITLY although 0 would already mean DefaultTrapPort: what the proxy will
		// open must be readable in what we sent, not in the default of the binary version it
		// runs.
		cfg.TrapPort = tunnel.DefaultTrapPort
	}
	// The count, not just the boolean: "this proxy listens" does not say whether for three
	// devices or a hundred, and that is the first question when asking why a fleet is not
	// reactive.
	slog.Info("monitoring: trap listening", "proxy_id", proxyID,
		"listening", cfg.TrapListen, "port", cfg.TrapPort,
		"trap_capable_targets", trapCapable, "snmp_targets", len(cfg.SNMP))

	var prefixes, pErr = s.Store.ListScanPrefixes(ctx, proxyID)
	if pErr != nil {
		return cfg, bilan, pErr
	}
	var p model.ScanPrefix
	for _, p = range prefixes {
		if p.Enabled {
			cfg.Prefixes = append(cfg.Prefixes, p.Prefix)
		}
	}
	return cfg, bilan, nil
}

// --- Attachment, from store to targets ---
//
// What follows is the PURE part of watchConfigFor: store rows to two target lists. It is
// isolated because it was the SEQUENCE — not any one step — that lost attachment, and a
// test must replay it whole without database, vault, or proxy (watch_attach_test.go). What
// remains in watchConfigFor reads the database, logs, and a few assignments.

// watchSelection: what sorting produces, in one value.
//
// An aggregate rather than five returns, so WIRING — which list feeds ping, which feeds
// collection — lives in a function a test can call, not in watchConfigFor which cannot be
// exercised without a database. That is where the error would be easiest to commit and most
// silent: wiring ICMP targets to `collect` compiles perfectly and removes reachability
// failover for the entire attached fleet.
type watchSelection struct {
	// targets: ICMP targets, i.e. ALL retained targets, attached or not. Attachment does not
	// restrict ping (migration 0022).
	targets []tunnel.WatchTarget
	// collect: rows where this proxy is reference collector — the only ones to poll in SNMP.
	collect []model.WatchedDevice
	// delegated: how many watched targets ANOTHER proxy is declared to collect. For log
	// summary, cf. watchBilan.
	delegated int
	// kept / rejected: raw netscope output, returned as-is because exclusion tracking
	// (watchexcl.go) and the log reason over candidates and their REASONS, which ping
	// targets no longer carry.
	kept     []netscope.WatchCandidate
	rejected []netscope.Rejected
}

// selectWatch sorts eligible rows into targets: first ADDRESS (netscope disambiguates
// who responds for an IP), then ATTACHMENT (who collects).
//
// Order matters, and not only for performance: a device we refuse to ping because we do not
// know who responds at its address must not be polled in SNMP either — series would be
// attributed to the wrong device, which is worse than no series at all.
func selectWatch(eligible []model.WatchedDevice, domains map[string]bool) watchSelection {
	var kept, rejected = netscope.SelectWatchTargets(watchCandidates(eligible), domains)
	var collect, delegated = snmpCandidates(eligible, kept)
	return watchSelection{
		targets: watchTargets(kept), collect: collect, delegated: delegated,
		kept: kept, rejected: rejected,
	}
}

// watchCandidates converts store rows into candidates for netscope.
//
// Information narrows here: `netscope.WatchCandidate` carries ONLY what resolves an address
// ambiguity. Attachment is not there, and must not be — cf. snmpCandidates.
func watchCandidates(eligible []model.WatchedDevice) []netscope.WatchCandidate {
	var cands = make([]netscope.WatchCandidate, 0, len(eligible))
	var d model.WatchedDevice
	for _, d = range eligible {
		cands = append(cands, netscope.WatchCandidate{
			DeviceID: d.ID, SiteID: d.SiteID, Name: d.Name, IP: d.MgmtIP,
			DomainID: d.AddressDomainID,
		})
	}
	return cands
}

// watchTargets translates retained candidates into ping targets. The proxy receives only
// the triplet it needs: an identifier to reply with, an address to ping, a name for its own
// logs.
func watchTargets(kept []netscope.WatchCandidate) []tunnel.WatchTarget {
	var out = make([]tunnel.WatchTarget, 0, len(kept))
	var c netscope.WatchCandidate
	for _, c = range kept {
		out = append(out, tunnel.WatchTarget{
			DeviceID: c.DeviceID, IP: c.IP, Name: c.Name,
		})
	}
	return out
}

// withRenumberTargets appends pending renumber candidate IPs to the ICMP watch list.
//
// Same device_id, extra IPs: the proxy indexes by address, so both old and new are
// pinged until serial verification promotes one. Candidates whose IP already belongs
// to another watched target are skipped — two device_ids on one IP would collide.
func (s *Server) withRenumberTargets(targets []tunnel.WatchTarget) []tunnel.WatchTarget {
	if s.Renumber == nil {
		return targets
	}
	var usedIP = map[string]bool{}
	var watched = map[string]bool{}
	var t tunnel.WatchTarget
	for _, t = range targets {
		usedIP[t.IP] = true
		watched[t.DeviceID] = true
	}
	var c renumber.Candidate
	for _, c = range s.Renumber.All() {
		if !watched[c.DeviceID] || usedIP[c.IP] {
			continue
		}
		usedIP[c.IP] = true
		targets = append(targets, tunnel.WatchTarget{
			DeviceID: c.DeviceID, IP: c.IP, Name: c.Name,
		})
	}
	return targets
}

// snmpCandidates separates, among targets netscope RETAINED, those where this proxy is
// reference collector — the only ones to poll in SNMP — and counts those another proxy is
// declared to collect.
//
// This is the step that makes attachment ACT: determinism where uniqueness is required (a
// trap destination is ONE address; two collectors report each port transition twice),
// redundancy where it is free (ping, not filtered here and must not be — it provides
// reachability failover the day a proxy goes down).
//
// # Why attachment does not pass through netscope
//
// `netscope.WatchCandidate` does not carry `Collector`, so rebuilding a `model.WatchedDevice`
// from a retained candidate — what this path did — lost attachment along the way. Making the
// candidate carry the field was the other option; it was rejected twice.
//
// First for netscope's role, which resolves ADDRESS ambiguities (two sites reusing
// 192.168.1.0/24) and has no reason to know collection policy. A collection field on its type
// would eventually enter its arbitration, and a device attached elsewhere would then
// disappear from PING — the exact opposite of the intended arbitration, obtained through an
// innocuous-looking cleanup in a package that is not about the subject.
//
// Second because there is nothing to rebuild: the original row is always there, in `eligible`,
// with everything the store read — SiteID, ProxyID, ProxyName, Collector. Rebuilding already
// lost more than attachment (SiteID stayed empty). We LOOK UP the row instead of rebuilding
// it, which immunizes this path against the next field added to `WatchedDevice`, and costs
// only a map the size of the list — no query, no extra round trip.
//
// # What the summary says, and what it cannot say
//
// A device removed from THIS proxy's collection because it is attached elsewhere is a NORMAL
// operations fact: someone requested it, and it is collected elsewhere. Saying it device by
// device would produce, at every proxy connection and every fleet change, as many lines as the
// fleet has attached devices — for information the device record already carries (`ProxyName`
// exists to write it plainly). Hence a COUNT, reported to PushWatchTo, and nothing more.
//
// The SERIOUS case is the neighbor, and we must say why it is not handled here rather than
// look like an omission: a device attached to a proxy that does NOT cover its site is
// collected by nobody. This path cannot distinguish that from a healthy attachment, and not
// from lack of zeal — by construction. `DevicesWatchedBy` filters by coverage: from the
// attached proxy, the device is not even in `eligible`; from any other proxy, `Collector` is
// false, exactly as for a perfectly legitimate attachment. Separating them would require
// re-querying coverage (`ProxiesCoveringSite`, one query per site) on every watch calculation
// path, for a verdict that would change NOTHING about what this proxy must do: in both cases,
// it does not collect.
//
// The case is therefore handled where it is decided and where it is visible: writing a
// non-covering attachment is REFUSED (`judgeCollector`), and the only remaining path — coverage
// that shrinks AFTER the fact — is signaled by the device record banner, the only place to
// reason about a device rather than a proxy. Detecting it server-side would mean auditing the
// fleet: that is an audit, not a watch calculation.
//
// PURE function, like the other decisions in this file: it is the batch rule, testable
// without a database.
func snmpCandidates(eligible []model.WatchedDevice,
	kept []netscope.WatchCandidate) ([]model.WatchedDevice, int) {
	var byID = make(map[string]model.WatchedDevice, len(eligible))
	var d model.WatchedDevice
	for _, d = range eligible {
		byID[d.ID] = d
	}
	var out = make([]model.WatchedDevice, 0, len(kept))
	var delegated int
	// Walk in `kept` order, not `eligible` order: that is the order netscope already
	// stabilized, and a target list that reorders from one call to the next makes pushed
	// proxy configuration "change" when nothing moved.
	var c netscope.WatchCandidate
	for _, c = range kept {
		var w, ok = byID[c.DeviceID]
		if !ok {
			// Unreachable by construction: `kept` is a subset of candidates built from
			// `eligible`. If it happened anyway, better to drop the SNMP target than fabricate
			// it from the candidate alone: without the original row, we do not know if this
			// proxy is the collector, and assuming yes would make collection non-deterministic
			// — precisely what this batch removes. Not counted as delegated either: we do not
			// know to whom.
			continue
		}
		if !w.Collector {
			delegated++
			continue
		}
		out = append(out, w)
	}
	return out, delegated
}

// --- Collection cadence policy ---
//
// Two distinct notions, and confusing them was the defect this batch fixes. POLICY chooses a
// rate; the connector FLOOR bounds it from below. The code took `MinInterval` for the cadence
// itself, whereas that field means "never more often than" (cf. driver.MonitorChannel): an
// entire fleet ended up polled at the maximum rate the platform tolerates — exactly what
// docs/01-Architecture/Unified monitoring and pipelines §1 forbids outright, and which was
// already recorded there as a known gap.
//
// The two values below REVERSE the previous arbitration, and that is deliberate:
//
//   - `if_state` goes from 5 s to 30 s. Port state no longer needs to be SCRUTINIZED, because
//     the trap channel provides reactivity: a `linkDown` triggers a poll on the proxy in 250 ms
//     (cf. tunnel.TrapCoalesce). Periodic polling is therefore no longer the source of
//     reactivity; it is the safety net for devices that push nothing — and a net is checked
//     every 30 s, not every 5 s.
//   - traffic and errors go from 60 s to 5 s. These counters draw CURVES: at one point per
//     minute, a two-minute saturation is invisible, guessed from two points. Fine grain is what
//     makes the graph usable, and it is the only reason to pay for it.
//
// The throughput consequence is real and handled on the other side, in cmd/proxy/snmp.go: at
// 5 s, a 48-port switch alone produces about forty samples per second. A cadence that does
// not scale back is not a cadence.
const (
	wantedStateInterval    = 30 * time.Second
	wantedCountersInterval = 5 * time.Second
	// wantedHealthInterval: device health — temperature, voltages, fans, power supplies,
	// processor load, memory, uptime.
	//
	// FIVE MINUTES, and that is the value design had written from the start ([[Monitoring]] §1,
	// "Health" line) without anything reading it. This batch does not renegotiate it, it wires
	// it up.
	//
	// Why five minutes is the right rate and not timidity: a board temperature is THERMAL MASS.
	// It cannot usefully move in one minute, and a fan that stops takes several minutes to show
	// on the thermometer — detection latency is not decided by measurement cadence, it is
	// decided by the physics of the phenomenon. Processor load and free memory are INSTANTANEOUS
	// samples: reading them every five seconds would add noise, not a curve.
	//
	// What this rate does NOT do, and it must be said rather than discovered: detect a short
	// reboot. `sysUpTime` is the only series that signals a device came back without ICMP watch
	// seeing it fall, and at five-minute intervals the break is clearly seen — but its DATE is
	// wrong by up to five minutes. For second-level diagnosis, the event log speaks, not this
	// series.
	wantedHealthInterval = 5 * time.Minute
	// wantedOpticalInterval: transceiver diagnostics.
	//
	// ONE MINUTE, and the arbitration deserves to be written because two phenomena of opposite
	// speeds live in the same measurement:
	//
	//   - an OPTICAL BUDGET drifts over weeks. An aging diode, dusty connector, degrading
	//     solder: at that scale, one point per hour would suffice;
	//   - a CUT shows in one second. An unplugged fiber drops received power instantly, and
	//     that is precisely what we want to correlate with a `port.down` to distinguish
	//     "someone unplugged" from "the transceiver failed".
	//
	// What decides is that the second case is ALREADY covered elsewhere — by port transitions,
	// which are real time — and this series therefore does not need to detect it, only explain
	// it afterward. One minute gives enough points for that without reading a module's I²C bus
	// every five seconds: that bus is the slowest on the board, shared with the rest of
	// diagnostics.
	//
	// The [[Roadmap]] announced counter cadence (5 s) for this metric, "same kind of data".
	// This batch diverges, and the reason is bus cost rather than curve shape — the page was
	// corrected with the code.
	wantedOpticalInterval = 60 * time.Second
	// wantedLLDPInterval: LLDP neighborhood.
	//
	// SIXTY SECONDS, and the reasoning starts from the SOURCE, not from us. A device rebuilds
	// its neighbor table at its own pace: on RouterOS, `/ip/neighbor/discovery-settings`
	// announces `discover-interval: 30s`, and the connector makes that its floor — the best
	// grounded in the repo, since it is neither negotiated nor prudential but imposed by how
	// the source works. Polling faster than that rereads a table the device has not touched.
	//
	// Why 60 and not 30, i.e. why we do NOT stick to the floor: because the operational value
	// of this data is not measured in seconds. An LLDP neighbor changes when someone unplugs a
	// cable or moves a switch — a physical act nobody expects detected in thirty seconds. What
	// matters is that the table be RIGHT, not fresh to the second, and the link drop that
	// accompanies unplugging is already reported in 250 ms by the trap channel. Doubling the
	// floor halves the load of seven walks per device (cf. snmppoll.lldpOIDs) for a reactivity
	// loss nothing observes.
	//
	// What this cadence does NOT do, and it is better written down: a cable moved from one port
	// to another in under a minute may be seen only once, on its new port, without the
	// `first_seen` of the old observation serving any purpose. Neighborhood says where cables
	// are, it does not tell their history — the event log would, and it deliberately carries no
	// neighborhood events (cf. docs/en/01-Architecture/Event log.md).
	wantedLLDPInterval = 60 * time.Second
)

// unknownFloorInterval: the floor kept when we do NOT KNOW.
//
// Unregistered connector, or connector declaring no channel for this metric: in both cases we
// ignore what the platform can handle. The safe default is not the wanted cadence — applying
// 5 s counters to an unknown device bets on its control plane, and *a device supervised too
// often is a device harmed*. Neither is "collect nothing", which would deny supervision to
// precisely the devices we know least.
//
// One minute, then: slow enough to harm nobody, fast enough that a screen does not stay empty.
// It is also the value this path already used as fallback before this batch, avoiding a load
// regression on an existing fleet by side effect.
const unknownFloorInterval = 60 * time.Second

// effectiveInterval applies policy: `wanted` is the cadence WANTED by the operator, `floor`
// the connector floor — 0 or negative meaning "unknown floor".
//
// PURE function, and intentionally so: it is the arbitration rule for the whole batch, testable
// without database, connector, or clock.
//
// The second return says the wanted cadence was LOWERED to the floor. It is not decorative: the
// caller MUST log it. An operator who asked for 5 s and gets 30 s must not have to notice by
// comparing graphs — silence here would turn an operations decision into a mystery.
func effectiveInterval(wanted, floor time.Duration) (time.Duration, bool) {
	if floor <= 0 {
		floor = unknownFloorInterval
	}
	if wanted < floor {
		return floor, true
	}
	return wanted, false
}

// intervalFor resolves a metric's cadence on a given device and makes any lowering VISIBLE.
//
// The log carries the device, its model, the metric, wanted cadence and kept cadence: it is
// the only place to learn that a policy is not applied as decided. At `Warn`, because it is an
// unmet instruction, not routine information.
//
// It repeats at every watch recalculation — proxy connection, fleet change — not at every poll:
// that is what makes it tolerable, and also guarantees it reappears after a restart rather
// than getting lost in history nobody scrolls.
//
// Metric vocabulary is that of `snmppoll`: constants are reused as-is rather than rewritten,
// so a rename breaks at compile time instead of silencing this path.
func intervalFor(d model.Device, mon driver.MonitoringCapabilities, known bool,
	metric string, wanted time.Duration) time.Duration {
	// 0 = unknown floor, cf. effectiveInterval. `origin` serves the log: without it,
	// "lowered to 60 s" does not say whether the connector requires it or we do not know the
	// device — two situations, two fixes.
	var floor time.Duration
	var origin = "unknown connector"
	if known {
		// `BestOfKind("snmp", …)` NOT `Best(…)`, and that is fixing a verifiable defect
		// rather than a refinement.
		//
		// What is pushed here is an SNMP POLL cadence: the proxy collector speaks only that
		// protocol (`internal/snmppoll`). But `Best` returns the fastest channel ACROSS ALL
		// KINDS. On the RouterOS connector, which declares a `web` channel at 5 s and an
		// `snmp` channel at 60 s — the latter deliberately high because no OID was ever read
		// there — `Best` returned 5 s, and the controller therefore polled SNMP twelve times
		// faster than the connector allowed for SNMP. One channel's floor was pitted against
		// another channel's, which is exactly what a floor is not.
		//
		// LOGGED ORIGIN is therefore more precise than before: it said the KIND of channel
		// kept (`snmp`, `web`), which taught nothing since the kind is now fixed. It now says
		// the floor comes from the SNMP channel, or that there is none.
		var ch, ok = mon.BestOfKind("snmp", metric)
		if ok {
			floor, origin = ch.MinInterval, "connector snmp channel"
		} else {
			// No SNMP channel for this metric. We do NOT fall back to another kind — that was
			// the defect above — and we do not give up collecting: `if_state` and `if_counters`
			// live entirely in MIB-II, so every SNMP agent serves them, including on a platform
			// whose connector never bothered to declare the channel. The unknown floor (60 s) is
			// the right fallback: slow enough to harm nobody.
			origin = "no snmp channel declared for this metric"
		}
	}
	var kept, raised = effectiveInterval(wanted, floor)
	if raised {
		slog.Warn("SNMP collection: wanted cadence lowered to floor",
			"device_id", d.ID, "name", d.Name, "model", d.Model,
			"metric", metric, "wanted", wanted.String(), "kept", kept.String(),
			"floor", origin)
	}
	return kept
}

// optionalIntervalFor resolves a SENSOR metric cadence, and returns 0 when there is nothing
// to collect.
//
// # Why these two metrics are not handled like the other two
//
// `if_state` and `if_counters` live entirely in MIB-II: every SNMP agent serves them, so
// requesting them from an unknown device is always a winning bet, which justifies the 60 s
// fallback floor for them.
//
// `health` and `optical_power` are the opposite. There is NO standardized MIB for switch
// health: the foundation is proprietary, and the poller reads MIKROTIK-MIB
// (`internal/snmppoll/health.go`). Requesting them from another vendor would only yield "no
// such object" — sixteen requests per cycle for nothing, across the whole fleet, indefinitely.
//
// Hence the reversal: these polls run only if the connector DECLARES an SNMP channel for them.
// That is the project rule — "an unknown capability is assumed absent" — applied to collection,
// and it makes adding these two polls free for platforms that do not support them.
//
// # Silence is INTENTIONAL here, unlike a floor lowering
//
// A floor lowering is logged, because an operations instruction is not honored. An undeclared
// metric is NOT logged: that is not an unmet instruction, it is the connector declaration
// applying. One line per device per watch recalculation would tell at every proxy connection
// that twenty switches on a site have no optical sensors, which is true, known, and will not
// change.
//
// What stays visible, and suffices: a device screen reads `Monitoring.Unavailable` to tell
// the operator whether to expect anything.
func optionalIntervalFor(d model.Device, mon driver.MonitoringCapabilities, known bool,
	metric string, wanted time.Duration) time.Duration {
	if !known {
		return 0
	}
	var ch, ok = mon.BestOfKind("snmp", metric)
	if !ok {
		return 0
	}
	var kept, raised = effectiveInterval(wanted, ch.MinInterval)
	if raised {
		slog.Warn("SNMP collection: wanted cadence lowered to floor",
			"device_id", d.ID, "name", d.Name, "model", d.Model,
			"metric", metric, "wanted", wanted.String(), "kept", kept.String(),
			"floor", "connector snmp channel")
	}
	return kept
}

// facadePortCount returns the number of PHYSICAL ports on a device, from its facade. 0 when
// unknown — better to collect too much than deny supervision to a device whose facade has not
// been read yet.
func facadePortCount(d model.Device) int {
	var groups []struct {
		Count int `json:"count"`
		From  int `json:"from"`
	}
	if json.Unmarshal(d.PortGroups, &groups) != nil {
		return 0
	}
	var max int
	var g int
	for g = range groups {
		var from = groups[g].From
		if from <= 0 {
			from = 1
		}
		var last = from + groups[g].Count - 1
		if last > max {
			max = last
		}
	}
	return max
}

// snmpWatchesFor builds SNMP targets for a proxy.
//
// Input is NOT "what this proxy watches" but "what this proxy COLLECTS": attachment sorting
// happens earlier, via snmpCandidates. That is the cheapest order — it depends on neither
// vault nor connectors, and there was no reason to pay for a device read, community decryption,
// and cadence resolution for a device another proxy polls.
//
// A device appears ONLY if it has a truly configurable SNMP identity: usable plan, identifier
// present in the vault, decryptable community. Polling a device without its secret would only
// produce timeouts — and a proxy busy waiting collects the rest less well.
//
// v3 is not transmitted yet: the proxy collector speaks only v2c (cf. internal/snmppoll). A v3
// device is therefore silently absent from targets rather than polled with a community it does
// not have.
//
// These exclusions stay silent CASE BY CASE, and that remains the right choice: three devices
// without community would produce three lines at every proxy connection, indefinitely, for a
// state that does not change. They are no longer silent IN AGGREGATE, however — the caller logs
// the count (`no_snmp_identity`, cf. PushWatchTo), enough to distinguish "secrets missing here"
// from "collection is elsewhere", the only two ways to poll only ten of thirty pinged devices.
//
// The second return is the count of retained targets whose connector declares it can PUSH traps.
// It is returned here, not recomputed by the caller, because we already hold the device, its
// SNMP plan, and its connector: redoing that outside would cost a second database round trip
// per device, at every proxy connection and every fleet change.
func (s *Server) snmpWatchesFor(ctx context.Context,
	devices []model.WatchedDevice) ([]tunnel.SNMPWatch, int) {
	var out = []tunnel.SNMPWatch{}
	var trapCapable int
	var w model.WatchedDevice
	for _, w = range devices {
		if w.MgmtIP == "" {
			continue
		}
		var d, err = s.Store.GetDevice(ctx, w.ID)
		if err != nil {
			continue
		}
		var st snmpIdentityState
		if st, err = s.snmpPlanFor(ctx, d); err != nil ||
			st.Plan.Kind != snmpprov.KindV2c || st.CredentialID == "" {
			continue
		}
		// Second phrase deliberately ignored: we only pass here for v2c, where community IS
		// the secret and there is never a second one.
		var _, enc, _, revErr = s.Store.RevealSiteCredential(ctx, st.CredentialID)
		if revErr != nil {
			continue
		}
		var community string
		if community, err = s.Secrets.Open(enc); err != nil || community == "" {
			continue
		}

		// Cadences: POLICY chooses, connector FLOOR bounds. Connector alone was not enough — it
		// says what the platform can handle, not what the operator needs to see — and policy
		// alone was not enough either: a device supervised too often is a device harmed. Hence
		// two separate resolutions, one per metric, each with its own floor (cf. 1930 connector
		// channel split).
		var mon driver.MonitoringCapabilities
		var drv, known = driver.Get(st.Driver)
		if known {
			var caps = drv.Capabilities(d.Model)
			mon = caps.Monitoring
			if mon.Traps {
				trapCapable++
			}
		}
		var state = intervalFor(d, mon, known,
			snmppoll.MetricIfState, wantedStateInterval)
		var counters = intervalFor(d, mon, known,
			snmppoll.MetricIfCounters, wantedCountersInterval)
		// The two SENSOR polls, enabled only if the connector declares an SNMP channel for
		// them — 0 means "do not collect" (cf. optionalIntervalFor).
		var health = optionalIntervalFor(d, mon, known,
			snmppoll.MetricHealth, wantedHealthInterval)
		var optical = optionalIntervalFor(d, mon, known,
			snmppoll.MetricOptical, wantedOpticalInterval)
		// NEIGHBORHOOD follows the same rule as the two sensors — enabled only if the connector
		// declares an SNMP channel for `lldp` — and the reason is even stronger. LLDP-MIB is
		// OPTIONAL: an entire reference-fleet platform serves not one line (SwOS, empty
		// `1.0.8802` walk, measured), and its connector declares that with proof. Enabling
		// collection everywhere would have meant seven silent walks per minute per CSS326,
		// indefinitely, for an answer we already know.
		var lldp = optionalIntervalFor(d, mon, known,
			snmppoll.MetricLLDP, wantedLLDPInterval)
		out = append(out, tunnel.SNMPWatch{
			DeviceID: d.ID, IP: d.MgmtIP, Community: community,
			StateIntervalS:    int(state.Seconds()),
			CountersIntervalS: int(counters.Seconds()),
			HealthIntervalS:   int(health.Seconds()),
			OpticalIntervalS:  int(optical.Seconds()),
			LLDPIntervalS:     int(lldp.Seconds()),
			// Facade bounds what we collect: beyond it are aggregates and logical interfaces no
			// screen could display.
			MaxIfIndex: facadePortCount(d),
		})
	}
	return out, trapCapable
}

// handleProxyEvent handles an event reported by a proxy. These are transitions: each deserves
// to be recorded.
//
// # SCOPE check, before any write
//
// The event's `DeviceID` is chosen by the PROXY. Without verification, a site's proxy could
// declare another site's device unreachable, flash its ports in the fleet log, and write its
// telemetry — site coverage, which the whole vault presents as a confidentiality boundary, was
// opposed to none of these paths. The check is here, in one place, because this is the UNIQUE
// entry point for tunnel facts: putting it in each `apply*` would create three chances to
// forget one, and the next event type would arrive unprotected.
//
// It relies on what the controller PUSHED to this proxy, retained by `PushWatchTo` — not a
// second coverage read. Two reasons, and the second matters more: no database query on the hot
// path (a port transition is the product's most frequent event), and especially the same source
// of truth as the instruction sent, where a second coverage calculation would eventually say
// something different. Cf. proxyscope.go for the two granularities — ping authorizes
// reachability, SNMP collection authorizes ports and traps — and for what happens to an event
// arriving before the first push.
func (s *Server) handleProxyEvent(proxyID string, ev tunnel.Event) {
	var need, gated = authorityFor(ev.Type)
	if gated && !s.allowProxyFact(proxyID, need, ev.DeviceID) {
		// Refused, and SAID (in bounded form, cf. proxyScopes.refuse). Do not return silently:
		// the only witness of a misbehaving or hostile proxy is this line, and a missing event
		// is never noticed.
		return
	}
	var ctx, cancel = context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	switch ev.Type {
	case tunnel.EventDeviceUp, tunnel.EventDeviceDown:
		s.applyDeviceState(ctx, proxyID, ev)
	case tunnel.EventSNMPUp, tunnel.EventSNMPDown:
		s.applySNMPState(ctx, ev)
	case tunnel.EventPortUp, tunnel.EventPortDown:
		s.applyPortTransition(ctx, proxyID, ev)
	case tunnel.EventTrapSeen:
		// PROOF event: a trap from this device was accepted. The only fact that turns
		// "meshcraft wrote an address in the device" into "the return path works" — placement
		// only proves what the device HOLDS (cf. trapdest.go).
		s.confirmTrapPath(ctx, proxyID, ev)
	case tunnel.EventHostSeen:
		// An address responded in a watched prefix. We do not create a candidate here: ping
		// response says nothing about what the device is. Identification scan remains an
		// explicit action.
		slog.Debug("host seen by a proxy", "ip", ev.IP, "prefix", ev.Prefix, "proxy", proxyID)
	case tunnel.EventSweepDone:
		slog.Info("monitoring sweep finished", "proxy_id", proxyID,
			"prefix", ev.Prefix, "scanned", ev.Scanned, "responders", ev.Found,
			"detail", ev.Detail)
	}
}

// applyDeviceState updates a device's status and logs the transition. It is the sole source of
// "online / unreachable" status.
//
// # Two kinds of input, one write
//
// A SWITCH just happened, or a STATE is re-announced because the proxy reconnects and the
// controller may have missed the switch (`ev.Resync`, cf. tunnel.Event). Status is written the
// same in both cases — same observed fact — but the LOG must distinguish them: the time of a
// missed switch is unknowable, and announcing it as fresh would report an outage at tunnel
// recovery time.
//
// What makes the distinction workable is store deduplication: a re-announcement confirming
// what the database already holds writes and says nothing. That is the ORDINARY reconnection
// case — all fine, everyone knew — and it must be perfectly silent, otherwise the re-announce
// itself becomes the noise it was meant to avoid.
//
// # Why the miss count left `status_note`
//
// The note carried "no longer responds to ping (3 consecutive misses)", and that count GROWS
// while the device is mute (`evaluate` keeps incrementing it). A note that changes every
// snapshot would make every re-announcement "different", therefore logged: deduplication would
// never bite. The count is an EVENT property, not state — it stays in the log message, where
// it is informative, and leaves the note, which must describe stable state.
func (s *Server) applyDeviceState(ctx context.Context, proxyID string, ev tunnel.Event) {
	if ev.DeviceID == "" {
		return
	}
	// Renumber candidate answered ICMP: verify SERIAL before promoting mgmt_ip.
	// Never promote on ping alone — a recycled lease can be another device.
	if ev.Type == tunnel.EventDeviceUp && ev.IP != "" &&
		s.Renumber != nil && s.Renumber.Has(ev.DeviceID, ev.IP) {
		go s.confirmRenumberCandidate(ev.DeviceID, ev.IP)
	}
	// THIS PROXY'S OBSERVATION IS RECORDED, IT IS NOT THE VERDICT.
	//
	// The distinction is the whole repair (cf. reachability.go and migration
	// 0035). Writing `devices.status` straight from the event is what let a proxy
	// on the wrong network mark four live devices down 3.7 seconds after it first
	// connected, simply by speaking last.
	var up = ev.Type != tunnel.EventDeviceDown
	var reachErr = s.Store.SetProxyReach(ctx, ev.DeviceID, proxyID, up,
		ev.Misses, ev.LatencyMS)
	if reachErr != nil {
		slog.Error("proxy observation not recorded", "err", reachErr,
			"device", ev.DeviceID, "proxy_id", proxyID)
		return
	}
	var obs, obsErr = s.Store.ProxyReachFor(ctx, ev.DeviceID)
	if obsErr != nil {
		slog.Error("proxy observations not read", "err", obsErr, "device", ev.DeviceID)
		return
	}
	var verdict = judgeReach(obs, s.Tunnels.Connected)
	var status, note = verdict.Status, verdict.Note

	var severity = model.SevNotice
	var message = "Device online (" + itoa(ev.LatencyMS) + " ms)"
	if status == "off" {
		severity = model.SevWarning
		message = "Device unreachable: " + note +
			" (" + itoa(ev.Misses) + " consecutive misses)"
	} else if status == "unknown" {
		// Nobody has concluded. Nothing to announce and nothing to journal:
		// silence is the honest report, and it is also what keeps a freshly
		// connected proxy from producing an event per device.
		severity = model.SevNotice
		message = "No proxy has concluded on this device yet"
	} else if verdict.Confirmed {
		message = "Device online (" + itoa(ev.LatencyMS) + " ms), " + note
	}
	if ev.Resync {
		// We do not claim to know WHEN: we state what we see and that we missed it.
		// That is the only true statement, and it is more useful than a false time —
		// it points to the tunnel outage as the thing to inspect.
		message = "State resynced on proxy reconnection: " + status +
			map[bool]string{true: " (" + note + ")", false: ""}[note != ""] +
			". The controller missed this transition; its time is unknown."
	}
	var changed, err = s.Store.SetDeviceStatus(ctx, ev.DeviceID, status, note)
	if err != nil {
		slog.Error("status not recorded", "err", err, "device", ev.DeviceID)
		return
	}
	if !changed {
		// Database already held this state. Nothing to write, nothing to log, no screen to
		// wake: ordinary re-announcement case, and also duplicate transition reported twice.
		return
	}
	var dev, getErr = s.Store.GetDevice(ctx, ev.DeviceID)
	if getErr != nil {
		return
	}
	var deviceID = ev.DeviceID
	var siteID = dev.SiteID
	// Reachability transition: exactly what fleet screens expect without asking. No user HTTP
	// request here, so no middleware to do it for us.
	s.NotifySiteChanged(siteID, ChangeDevice)
	if status == "unknown" {
		// The screen is refreshed — the badge did change — but nothing is
		// JOURNALED. "unknown" is the absence of a conclusion, and the two event
		// types available both assert one: filing it as `device.recovered` would
		// put a recovery in the log for a device nobody has looked at yet.
		return
	}
	_ = s.Store.InsertEvent(ctx, model.Event{
		SiteID: &siteID, DeviceID: &deviceID,
		Type:     map[bool]string{true: "device.unreachable", false: "device.recovered"}[status == "off"],
		Severity: severity,
		Message:  "\"" + dev.Name + "\" — " + message,
		Actor:    "monitoring",
	})
}

// applySNMPState updates SNMP collection reachability. Parallel to applyDeviceState
// for ICMP: same write/dedup/log shape, separate column — the two axes must not share
// a light (cf. migration 0031).
func (s *Server) applySNMPState(ctx context.Context, ev tunnel.Event) {
	if ev.DeviceID == "" {
		return
	}
	var status = "ok"
	var note = ""
	var severity = model.SevNotice
	var message = "SNMP collection responding"
	if ev.Type == tunnel.EventSNMPDown {
		status = "off"
		note = "no longer responds to SNMP"
		severity = model.SevWarning
		message = "SNMP unreachable: " + note +
			" (" + itoa(ev.Misses) + " consecutive poll failures)"
	}
	if ev.Resync {
		message = "SNMP state resynced on proxy reconnection: " + status +
			map[bool]string{true: " (" + note + ")", false: ""}[note != ""] +
			". The controller missed this transition; its time is unknown."
	}
	var changed, err = s.Store.SetDeviceSNMPStatus(ctx, ev.DeviceID, status, note)
	if err != nil {
		slog.Error("snmp status not recorded", "err", err, "device", ev.DeviceID)
		return
	}
	if !changed {
		return
	}
	var dev, getErr = s.Store.GetDevice(ctx, ev.DeviceID)
	if getErr != nil {
		return
	}
	var deviceID = ev.DeviceID
	var siteID = dev.SiteID
	s.NotifySiteChanged(siteID, ChangeDevice)
	_ = s.Store.InsertEvent(ctx, model.Event{
		SiteID: &siteID, DeviceID: &deviceID,
		Type: map[bool]string{
			true:  "device.snmp_unreachable",
			false: "device.snmp_recovered",
		}[status == "off"],
		Severity: severity,
		Message:  "\"" + dev.Name + "\" — " + message,
		Actor:    "monitoring",
	})
}

// --- Port transitions ---

// applyPortTransition normalizes a port transition reported by a proxy and logs what
// correlation retains.
//
// Decision 6: the proxy establishes the fact, the controller correlates. Anything that looks
// like arbitration — flapping, inter-proxy duplicates — lives in portevents.go, with no
// database or implicit clock; here only the write remains.
//
// Correlation state is a `Server` field (`portFlaps`, mounted by `Router()`), not packet
// state: two controllers in one process — what a test suite is — have no reason to share
// flap memory.
//
// # Timestamp comes from HERE, not the event
//
// `ev.TS` is the time the PROXY set, i.e. a machine deployed at a customer site that nothing
// synchronizes — eleven seconds behind the reference install. Correlation compared it to
// controller times (the `time.Now()` of sweepPortFlaps), making its thresholds depend on drift
// of a clock we do not control: cf. "One clock only" at the head of portevents.go, and
// store.ConfirmTrapPath for the same defect elsewhere.
//
// The instant is therefore read HERE, and `time.Now()` is written LITERALLY in the call to
// `observe` — not stored in a variable, however readable. That is what makes the property
// verifiable from outside: a test reads what production calls pass as clock (cf.
// portevents_test.go, the batch lock), and could not follow a variable. The second read for
// skew measurement costs nanoseconds a five-second threshold cannot notice.
func (s *Server) applyPortTransition(ctx context.Context, proxyID string, ev tunnel.Event) {
	var tr = portTransition{
		DeviceID: ev.DeviceID, IfIndex: ev.IfIndex, PortName: ev.PortName,
		Up: ev.Type == tunnel.EventPortUp, ProxyID: proxyID,
		Trigger: ev.Trigger,
	}
	var decisions = s.portFlaps.observe(tr, time.Now())
	// Application log carries trigger and proxy on every retained transition. The only place
	// to see whether a fleet actually receives its traps: without this line, a fleet where no
	// device pushes anything would be indistinguishable from a reactive fleet — same events,
	// just thirty seconds later, and nobody would notice 162/udp serves no purpose. The
	// OPERATIONS log does not mention it: "trap" is protocol vocabulary, not fleet information.
	var args = []any{"proxy_id", proxyID, "device_id", ev.DeviceID,
		"ifindex", ev.IfIndex, "port", ev.PortName, "state", ev.Type,
		"trigger", ev.Trigger, "kept", len(decisions) > 0}
	// Clock skew, when present: a FIELD on the existing line, never an extra line. A port
	// transition is a thousand times more frequent than placing a trap destination — the other
	// place skew is reported (confirmTrapPath) — and a `slog.Warn` per transition would flood
	// the application log exactly when a fleet flaps. Cadence is therefore exactly this line,
	// already emitted once per received transition, and the field appears only when skew
	// crosses trapClockNote's asymmetric thresholds — below them, it would be ping measurement.
	//
	// Still useful though correlation no longer depends on it: a drifting proxy clock falsifies
	// its own logs, its metrics, and what an operator reads from them. Here it is observable
	// on an ordinary fleet — without waiting for a device to receive a trap destination.
	var note = trapClockNote(ev.TS, time.Now())
	if note != "" {
		args = append(args, "clock_skew", note, "proxy_date", ev.TS)
	}
	slog.Info("port transition", args...)
	s.writePortDecisions(ctx, decisions)
}

// sweepPortFlaps closes calmed flaps and purges the structure. Runs for process lifetime: there
// is no clean controller shutdown to hook, and an unwritten flap end would leave the log
// showing a port flapping forever.
//
// Started by `Router()`, with other background tasks, not on first port event. Lazy start saved
// one sleeping goroutine in a proxy-less controller — zero gain — at the cost of packet
// `sync.Once` on EVERY transition path and shared state across all `Server` instances in the
// process.
func (s *Server) sweepPortFlaps() {
	var tick = time.NewTicker(flapSweep)
	defer tick.Stop()
	for range tick.C {
		var decisions = s.portFlaps.sweep(time.Now())
		if len(decisions) == 0 {
			continue
		}
		var ctx, cancel = context.WithTimeout(context.Background(), 30*time.Second)
		s.writePortDecisions(ctx, decisions)
		cancel()
	}
}

// writePortDecisions writes decisions to the site log and wakes screens.
func (s *Server) writePortDecisions(ctx context.Context, decisions []portDecision) {
	var dec portDecision
	for _, dec = range decisions {
		// Device is re-read per decision: it carries the name the operator reads and the facade
		// that names the port. Nothing cached here — one decision per retained transition is one
		// read per retained transition, and flapping (the only voluminous case) is precisely
		// where correlation returns nothing.
		var d, err = s.Store.GetDevice(ctx, dec.DeviceID)
		if err != nil {
			slog.Warn("port transition: device not found",
				"err", err, "device_id", dec.DeviceID, "ifindex", dec.IfIndex)
			continue
		}
		var ev = portEventOf(d, dec)
		if ev.Type == "" {
			// Common case, and INTENTIONAL: a port meshcraft does not manage does not go to the
			// fleet log (cf. portManaged). Correlation did retain it — applyPortTransition's
			// application log says "kept", and that is exact, it speaks of correlation — but
			// there is no line to write. Otherwise every laptop plug-in would produce one.
			continue
		}
		if err = s.Store.InsertEvent(ctx, ev); err != nil {
			slog.Warn("port transition not logged", "err", err,
				"device_id", dec.DeviceID, "ifindex", dec.IfIndex)
			continue
		}
		// Both aggregate kinds deserve application trace: they carry how many observations
		// arrived via the fast path, i.e. what the trap channel actually brings on this fleet.
		// An ordinary transition was already traced at entry.
		if dec.Kind != portLogTransition {
			slog.Info("port flapping", "kind", dec.Kind,
				"device_id", dec.DeviceID, "ifindex", dec.IfIndex, "port", ev.PortName,
				"transitions", dec.Count, "traps_seen", dec.Traps, "stable_up", dec.Up)
		}
		// ChangePort not ChangeDevice: the category sitebus.go defines literally as "a link
		// goes up or down". A port going down changes NOTHING about device reachability —
		// announcing ChangeDevice would make the client re-read fleet badges for an event that
		// does not touch them, and blur the diagnostic these categories exist to provide (we
		// read in the stream WHY the screen refreshed).
		s.NotifySiteChanged(d.SiteID, ChangePort)
	}
}

// portEventOf translates a correlation decision into a log line.
//
// PURE function of a device and a decision: it decides type, severity, and wording — three
// things we want to test without a database.
//
// # Severities, and why they do not follow intuition
//
// A port going down is NOT an outage. An access port dropping because a user unplugs on the
// way out is the most banal network event — on a two-thousand-port fleet, hundreds per day.
// Logging it as `warning` would fill the log with noise and, worse, teach operators to ignore
// color: the day a real incident logs as warning, it would be the six-hundredth warning line
// of the day. Both directions are therefore `SevInfo` — visible on a port record, invisible
// in a problem-filtered list.
//
// FLAPPING, however, is `SevWarning`, and that reversal is the whole point of the batch:
// nobody unplugs the same cable five times in five minutes. A flapping port is a physical
// defect — bad crimp, dying transceiver, duplex mismatch, STP loop. That is precisely what
// should surface, and what individual transitions drowned.
//
// Flap END splits in two, because these are opposite situations: stabilized in service means
// fixed (`SevNotice`, good news); stabilized out of service means the port stopped flapping
// because the link is dead for good (`SevWarning`, problem still there).
//
// Returns a NULL event — therefore nothing to write — for a port meshcraft does not manage:
// cf. portManaged, the filter without which this path would flood the log.
func portEventOf(d model.Device, dec portDecision) model.Event {
	var siteID, deviceID = d.SiteID, d.ID
	var f = facadeOf(d)
	// Facade name is what the operator sees. Otherwise keep SNMP label AND index: label alone
	// would lie, since it collides (on a 1930 the VLAN 42 interface is named "42", like physical
	// port 42), and index alone would be unreadable.
	var name = f.resolve(dec.PortName)
	if name != "" && !portManaged(d, name) {
		return model.Event{}
	}
	var label = name
	switch {
	case label != "":
	case dec.PortName != "":
		label = dec.PortName + " (ifIndex " + itoa(dec.IfIndex) + ")"
	default:
		label = "ifIndex " + itoa(dec.IfIndex)
	}
	var ev = model.Event{
		SiteID: &siteID, DeviceID: &deviceID, Actor: "monitoring",
		// PortName is set only when the facade RECOGNIZED the port: this field is an identifier
		// for screens, which use it to attach the line to a jack. Writing an unresolved `ifDescr`
		// would attach the event to a port that does not exist, or worse a homonym.
		PortName: name,
	}
	var prefix = "\"" + d.Name + "\" · " + label + " — "

	switch dec.Kind {
	case portLogTransition:
		if dec.Up {
			ev.Type, ev.Severity = "port.up", model.SevInfo
			ev.Message = prefix + "port back up"
			return ev
		}
		ev.Type, ev.Severity = "port.down", model.SevInfo
		ev.Message = prefix + "port down"
		return ev
	case portLogFlapStart:
		ev.Type, ev.Severity = "port.flapping", model.SevWarning
		ev.Message = prefix + "port flapping: " + itoa(dec.Count) +
			" rapid state changes — subsequent ones are grouped"
		return ev
	case portLogFlapEnd:
		ev.Type = "port.flap_cleared"
		var duration = frMinutes(dec.At.Sub(dec.Since))
		if dec.Up {
			ev.Severity = model.SevNotice
			ev.Message = prefix + "BACK IN SERVICE after " + itoa(dec.Count) +
				" state changes in " + duration
			return ev
		}
		ev.Severity = model.SevWarning
		ev.Message = prefix + "OUT OF SERVICE after " + itoa(dec.Count) +
			" state changes in " + duration + ": the link does not come back"
		return ev
	}
	return model.Event{}
}

// portManaged says whether meshcraft MANAGES facade port `name` — i.e. whether inventory gave
// it a mode other than "libre".
//
// # Why this filter exists
//
// It comes from the old logging path (metrics push, cf. proxymetrics_handlers.go), which
// already applied it, and that was the only thing worth saving: reporting an unmanaged port
// coming up would flood the log on every laptop plug-in. On a two-thousand-port fleet, that
// is hundreds of lines per day, drowning transitions that matter — removing the old path
// without carrying this filter would not remove duplication, it would replace it with a flood.
//
// The filter applies to ALL decision kinds, flapping included. A free port flapping is not a
// dying transceiver: it is someone replugging, or a laptop waking. The rule fits in one
// sentence, and that is what makes it tenable: the fleet log speaks only of ports meshcraft
// manages.
//
// # When we do not know
//
// Correlation key is `(device, ifIndex)`; mode lives in `devices.ports` by FACADE name. Facade
// must therefore be resolved to know mode, and the caller consults this function ONLY when
// resolution succeeded. An unresolved port is still logged — the only case where we know
// nothing about the port, and silence by default would mute the channel on every device whose
// facade has not been read yet, i.e. precisely those we know least. An unknown port going down
// is information; a free port coming up is not.
//
// Do not confuse with the adjacent case: a RESOLVED port with no row in `devices.ports` is
// indeed a free port. The facade answered, and it says meshcraft has nothing to do there.
// Silence, as for "libre".
func portManaged(d model.Device, name string) bool {
	var ports map[string]map[string]any
	if json.Unmarshal(d.Ports, &ports) != nil {
		// Empty or unreadable `ports`: no port is managed, nothing to say about this one.
		// Closed by default, as the old path did.
		return false
	}
	// Indexing a nil map is legal: a port absent from inventory yields empty mode, i.e. free.
	var mode, _ = ports[name]["mode"].(string)
	return mode != "" && mode != "libre"
}

// frMinutes renders a duration as operators read it in a log. To the minute: nobody decides
// flap timing to the second, and "4 min" compares at a glance where "4m12.318s" must be
// decoded.
func frMinutes(d time.Duration) string {
	var m = int(d.Minutes())
	if m < 1 {
		return "less than a minute"
	}
	return itoa(m) + " min"
}

// forgetDroppedReach removes this proxy's observations about devices it no longer
// watches, and recomputes the status of each one.
//
// The recomputation is the point. Deleting the row silently would leave
// `devices.status` on a verdict whose only source has just been withdrawn — the
// device would keep showing the colour a proxy gave it before losing the right to
// have an opinion.
//
// Best-effort throughout, and deliberately quiet when there is nothing to do,
// which is the overwhelming majority of pushes: a watch set that did not change
// removes no rows and touches no device.
func (s *Server) forgetDroppedReach(ctx context.Context, proxyID string,
	cfg tunnel.WatchConfig) {
	var keep = make([]string, 0, len(cfg.Targets))
	var t tunnel.WatchTarget
	for _, t = range cfg.Targets {
		if t.DeviceID != "" {
			keep = append(keep, t.DeviceID)
		}
	}
	var dropped, err = s.Store.ForgetProxyReach(ctx, proxyID, keep)
	if err != nil {
		slog.Warn("stale proxy observations not cleared", "proxy_id", proxyID,
			"err", err.Error())
		return
	}
	if len(dropped) == 0 {
		return
	}
	slog.Info("proxy observations forgotten", "proxy_id", proxyID,
		"devices", len(dropped),
		"why", "these devices are no longer in this proxy's watch")
	var deviceID string
	for _, deviceID = range dropped {
		s.recomputeReach(ctx, deviceID)
	}
}

// recomputeReach re-derives one device's status from the observations that REMAIN.
//
// Called when the set of observers changes rather than when an observation does —
// a proxy detached, revoked, or no longer covering the site. Without it, losing an
// observer would leave its verdict standing indefinitely.
func (s *Server) recomputeReach(ctx context.Context, deviceID string) {
	var obs, err = s.Store.ProxyReachFor(ctx, deviceID)
	if err != nil {
		return
	}
	var verdict = judgeReach(obs, s.Tunnels.Connected)
	var changed, setErr = s.Store.SetDeviceStatus(ctx, deviceID, verdict.Status, verdict.Note)
	if setErr != nil || !changed {
		return
	}
	var dev, getErr = s.Store.GetDevice(ctx, deviceID)
	if getErr != nil {
		return
	}
	var siteID = dev.SiteID
	s.NotifySiteChanged(siteID, ChangeDevice)
	if verdict.Status == "unknown" {
		return
	}
	var id = deviceID
	_ = s.Store.InsertEvent(ctx, model.Event{
		SiteID: &siteID, DeviceID: &id,
		Type: map[bool]string{true: "device.unreachable",
			false: "device.recovered"}[verdict.Status == "off"],
		Severity: map[bool]int{true: model.SevWarning,
			false: model.SevNotice}[verdict.Status == "off"],
		Message: "\"" + dev.Name + "\" — status re-established after a change of " +
			"observers: " + verdict.Note,
		Actor: "monitoring",
	})
}
