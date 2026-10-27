package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"meshcraft/internal/driver"
	"meshcraft/internal/model"
	"meshcraft/internal/snmpprov"
	"meshcraft/internal/store"
	"meshcraft/internal/tunnel"
)

// Write into the device the address to which it should emit traps — the last stage of
// the trap channel, and the EXACT counterpart of snmpapply.go for the return path.
//
// Everything else already existed: the proxy receives and filters (cmd/proxy/trap.go),
// the controller correlates (portevents.go), the connector can declare a recipient
// ([driver.TrapPusher]), and migration 0022 attachment provides the uniqueness without
// which "one address" has no meaning. The action linking them was missing, and no device
// had therefore ever received the order to emit.
//
// Done AT PROVISIONING, for the reason already written in snmpapply.go and which applies
// here word for word: adopting a device means making it administrable AND observable.
// Leaving supervision to a second manual step guarantees a half-supervised fleet where
// every omission is invisible.
//
// # Five constraints come from code already shipped, and two would break everything silently
//
//  1. Trap community MUST be POLLING community. `classifyTrap` accepts a trap only if
//     the community it carries equals that of the `SNMPWatch` — the one used to poll
//     the device. Setting a distinct trap community, even dedicated, even "clean", would
//     reject all traps as `trapBadCommunity`: silently, with only a DEBUG counter on the
//     proxy as witness. That is why the destination is DERIVED from a `tunnel.SNMPWatch`
//     (trapDestinationFrom) and not composed of independent fields: trap community has no
//     separate source from which it could diverge.
//  2. We only configure v2c. `classifyTrap` rejects any trap without community, yet a
//     v3 trap carries none: the connector can set a v3 recipient
//     ([driver.TrapDestination.User]), but those traps would be DROPPED on arrival. The
//     connector's v3 capability is therefore deliberately unused, and the lock is in the
//     proxy collector, not here.
//     Corollary not to disguise: `snmpWatchesFor` already excludes v3 devices from polling
//     (internal/snmppoll speaks only v2c). A v3-supervised device is therefore today neither
//     polled nor listened to — this batch does not fix that, and refuses configuration while
//     saying so rather than implying otherwise.
//  3. Address is not DEDUCED, it is ASKED. One might think it suffices to search, among
//     addresses the proxy declares (`proxies.ifaces`), for one on the device's subnet. That
//     is false as soon as the proxy reaches it by ROUTING: on the lab fleet, the proxy
//     exposes 10.42.0.67/24 and 172.17.0.1/16 for a device at 198.51.100.101 — none of its
//     addresses is on the right network, and the obvious algorithm returns nothing. Only the
//     proxy kernel has the routing table, hence the `tunnel.AskSourceAddr` question.
//  4. A declared COLLECTOR is required. A device in `null` mode (empty `devices.proxy_id`)
//     is collected by ALL proxies covering it: there is not one address to write, there are
//     several, and the election to choose is not built. We refuse, stating the repair action
//     — we never pick a proxy "at random", which would write into the device an address that
//     would change on the next calculation.
//  5. Configuration is a CLAIM; the first received trap is PROOF. A connector `statusCode 0`
//     means the device accepted the line, not that the return path works: a firewall,
//     asymmetric route, ACL on the management VLAN, and it emits into the void.
//     `tunnel.EventTrapSeen` is what turns "address written" into "return path established",
//     and those are TWO DISTINCT STATES (migration 0023) — a record conflating them would
//     imply reactive supervision that does not exist.
//     And proof itself has SCOPE, tightened after the first hardware trial: an accepted trap
//     establishes that this device's traps reach this proxy, which is not yet "the address I
//     wrote receives them". The proxy filter only looks at source IP, and listening is on the
//     wildcard address. It is `tunnel.Event.DestAddr` — the local address targeted by the
//     datagram, read in the kernel — that closes the gap, and `judgeTrapProof` that compares
//     it. Without it, proof remains valid but UNATTRIBUTED, and is said as such.

// --- The decision, pure ---

// trapDestVerdict: can this device receive a trap destination, and if not why?
type trapDestVerdict int

const (
	// trapDestReady: declared covering collector, v2c plan, community in vault,
	// connector declares push capability.
	trapDestReady trapDestVerdict = iota
	// trapDestNoMgmtIP: nothing to reach, therefore nothing to configure.
	trapDestNoMgmtIP
	// trapDestNoCollector: coverage mode. THE structural refusal of this batch,
	// cf. constraint 4.
	trapDestNoCollector
	// trapDestCollectorNotCovering: a collector is declared and no longer covers the
	// site. The device is collected by nobody (cf. devices_handlers.go).
	trapDestCollectorNotCovering
	// trapDestCollectorInternal: designated collector is the "Local" proxy, i.e. the
	// meshcraft instance itself. It has no tunnel, receives no watch configuration, and
	// listens to no traps.
	trapDestCollectorInternal
	// trapDestUnsupervised: no exploitable SNMP plan — unknown connector, or connector
	// declaring no version.
	trapDestUnsupervised
	// trapDestV3Only: supervised in v3. Cf. constraint 2.
	trapDestV3Only
	// trapDestNoTrapPush: connector does not declare ability to PUSH traps.
	trapDestNoTrapPush
	// trapDestNoCredential: v2c plan, but nothing in vault for this site.
	trapDestNoCredential
)

// trapDestInputs gathers what the decision depends on, and nothing else.
//
// An aggregate rather than eight parameters, for the same reason as watchSelection: what
// matters is the SEQUENCE of refusals, and a test must be able to replay it whole without
// database, vault, or proxy.
type trapDestInputs struct {
	deviceName string
	mgmtIP     string
	// collector: `devices.proxy_id`, the DECLARED collector. Empty = coverage mode,
	// which is a refusal here.
	collector     string
	collectorName string
	// plan / credentialID: output of snmpPlanFor. Conditions retained are EXACTLY those
	// of snmpWatchesFor — v2c plan, present identifier — because a device whose
	// destination we write and a device we poll must be the same set. Two diverging rules
	// would give a device configured to emit toward a collector that does not listen.
	plan         snmpprov.Plan
	credentialID string
	// trapsPush: device can push traps AND its connector can tell it to. TWO conditions
	// and one field, because both lead to the same refusal and deserve the same sentence:
	//
	//   - `driver.MonitoringCapabilities.Traps` says the PLATFORM can. It is the same
	//     capability that decides the proxy opens 162/udp (cf. watchConfigFor): without it,
	//     we would write a destination in a device that cannot use it, toward a port nobody
	//     listens on. On SwOS it is false, established by enumeration — the COMPLETE web
	//     interface fits in one file and knows no recipient field;
	//   - [driver.TrapPusher] says the CONNECTOR can. A platform may know how to emit
	//     without meshcraft having coded how to ask it.
	//
	// One verdict for both (`trapDestNoTrapPush`): from where the operator stands,
	// "nothing to expect from the device side" and "nothing to expect from meshcraft side"
	// are repaired the same way — i.e. not at all, and polling remains the source of truth.
	trapsPush bool
}

// judgeTrapDest decides, and states in what order refusals apply.
//
// Pure, and that is the batch rule: the package has no database in tests (cf.
// trapdest_test.go), and this is where the two refusals that protect against silent failure
// are made.
//
// Coverage is NOT re-evaluated here: `judgeCollector` is reused as-is. That is deliberate
// and not a line-saving economy — site coverage is a confidentiality boundary (migration
// 0022), and a second expression of the same predicate would eventually say something
// different. The day one moves, the other moves with it.
//
// Refusal ORDER is the message: we only talk about the vault for a device that has a
// collector, a v2c plan, and a connector capable of pushing. Reversing would send an
// operator to generate a community for a device that, anyway, cannot emit traps.
func judgeTrapDest(in trapDestInputs, covering []model.Proxy) (trapDestVerdict, model.Proxy) {
	if in.mgmtIP == "" {
		return trapDestNoMgmtIP, model.Proxy{}
	}
	var verdict, target = judgeCollector(covering, in.collector)
	switch verdict {
	case collectorDetach:
		return trapDestNoCollector, model.Proxy{}
	case collectorNotCovering:
		return trapDestCollectorNotCovering, model.Proxy{}
	}
	if target.Internal {
		return trapDestCollectorInternal, target
	}
	if !in.plan.Supervised() {
		return trapDestUnsupervised, target
	}
	if in.plan.Kind == snmpprov.KindV3 {
		return trapDestV3Only, target
	}
	if in.plan.Kind != snmpprov.KindV2c {
		return trapDestUnsupervised, target
	}
	if !in.trapsPush {
		return trapDestNoTrapPush, target
	}
	if in.credentialID == "" {
		return trapDestNoCredential, target
	}
	return trapDestReady, target
}

// trapDestRefusal says what is missing, and the ACTION that fixes it.
//
// Each refusal has its sentence, and that is half the mechanism: "trap destination
// impossible" would cover all eight and help in none of the cases. They are repaired by
// actions unrelated to each other — attach a device, widen coverage, deploy a proxy,
// generate an SNMP identity, or do nothing because the platform does not push traps.
func trapDestRefusal(verdict trapDestVerdict, in trapDestInputs) string {
	var who = "\u00ab " + in.deviceName + " \u00bb"
	switch verdict {
	case trapDestNoMgmtIP:
		return "Trap destination impossible: " + who + " has no management " +
			"address. There is nothing to configure."
	case trapDestNoCollector:
		return "Trap destination impossible: no collector is declared for " +
			who + ". Without attachment, this device is collected by ALL proxies " +
			"that cover its site: there is not one address to write into it, there are " +
			"several, and picking one at random would engrave an address " +
			"that would change on the next calculation. Attach the device to a proxy " +
			"(device page, collector), then re-apply the destination."
	case trapDestCollectorNotCovering:
		var him = "its declared collector"
		if in.collectorName != "" {
			him = "its declared collector (\u00ab " + in.collectorName + " \u00bb)"
		}
		return "Trap destination impossible: " + him + " does not cover the site of " +
			who + ". This device is not collected by anybody — neither polled in SNMP, " +
			"nor listened to for traps — and this proxy's address would lead to no " +
			"collector. Add this site to the proxy's coverage, or attach " +
			"the device to a proxy that already covers it."
	case trapDestCollectorInternal:
		return "Trap destination impossible: " + who + " is attached to the " +
			"\u00ab Local \u00bb proxy, i.e. the meshcraft instance itself. It scans and " +
			"reaches networks in its reach, but it has no tunnel: it receives " +
			"no watch configuration and does not listen on 162/udp. There is therefore " +
			"no trap collector to designate. Attach the device to a proxy " +
			"deployed in its network."
	case trapDestUnsupervised:
		var why = in.plan.Reason
		if why == "" {
			why = "no SNMP version is known for this device."
		}
		return "Trap destination impossible on " + who + ": " + why +
			" A trap destination is declared inside an SNMP agent; meshcraft does not " +
			"know whether this one has one."
	case trapDestV3Only:
		return "Trap destination impossible on " + who + ": this device is " +
			"monitored with SNMPv3. The connector could declare a v3 destination, " +
			"but a v3 trap carries no community and the proxy filter would " +
			"reject it on arrival (see classifyTrap): the line would be written, " +
			"the device would emit, and nothing would ever arrive. Note, because " +
			"it is the same lock: a v3 device is today also not " +
			"POLLED — the proxy collector speaks only v2c. Both halves " +
			"will come with the service-identity transport."
	case trapDestNoTrapPush:
		return noTrapPushRefusal(who)
	case trapDestNoCredential:
		return "Trap destination impossible on " + who + ": no SNMPv2c " +
			"community exists in the vault for its site. The community written into " +
			"the device MUST be the polling one — the proxy rejects a trap whose " +
			"community does not match the one it polls with — and there is therefore " +
			"no other one to invent. Generate the device's SNMP identity " +
			"(snmp-apply), then re-apply the destination."
	}
	return ""
}

// noTrapPushRefusal is the trapDestNoTrapPush sentence, extracted because it has
// TWO emission points.
func noTrapPushRefusal(who string) string {
	return "Trap destination impossible on " + who + ": its connector does " +
		"not declare the ability to push traps. Nothing to fix — this platform is " +
		"supervised by polling, and polling remains the source of truth. What " +
		"is missing is REACTIVITY, not information."
}

// trapDestinationFrom derives the recipient to configure FROM THE DEVICE'S SNMP WATCH
// line.
//
// This is the batch's most important function, and its sole secret parameter is the reason
// for its signature. A trap's community must be the POLLING one: `classifyTrap`
// (cmd/proxy/trap.go) compares the received trap's community to that of `tunnel.SNMPWatch`,
// and rejects anything different as `trapBadCommunity` — without error, without event,
// with only a DEBUG counter on the proxy as witness. A dedicated trap community, however
// clean, would therefore drop 100% of fleet traps.
//
// Taking the ENTIRE watch as input, rather than a "community" string, is what makes this
// confusion hard to commit: there is no other source here from which a community could
// come. Connector survey shows an example with community `mctrap` (the device field is free
// text the firmware does not tie to any `CommunityList` entry): that is a FORM example, and
// copying it would break the channel.
//
// `User` stays EMPTY, therefore v2c: cf. constraint 2 at file head. Port is
// `tunnel.DefaultTrapPort`, the same constant sent to the proxy in its watch configuration
// — hardcoding 162 here would make two places to read the listen port.
//
// Second return is a readable REFUSAL rather than a boolean: the two values that can be
// missing are missing for opposite reasons, and a single `false` would send you looking for
// the wrong one.
func trapDestinationFrom(w tunnel.SNMPWatch,
	sourceAddr string) (driver.TrapDestination, string) {
	if sourceAddr == "" {
		return driver.TrapDestination{}, "Trap destination impossible: the collector proxy " +
			"could not say which address it uses to reach this device. It, and it alone, can " +
			"answer — the address depends on its routing table, not on addresses it declares. " +
			"An empty response usually means it has no route to this device."
	}
	if w.Community == "" {
		// The proxy reads an empty community as "public" (effectiveCommunity), but we do
		// NOT invent that fallback here: a device without community is not in SNMP targets
		// (snmpWatchesFor excludes it), therefore it is not watched, therefore its traps
		// would be rejected as unknown source anyway. Setting "public" would write a secret
		// nobody chose into a production device.
		return driver.TrapDestination{}, "Trap destination impossible: this device's SNMPv2c " +
			"community is empty or unreadable (encryption key changed?). That is the same " +
			"cause that deprives it of SNMP collection: the community written into the device " +
			"is the polling one, there is no other."
	}
	return driver.TrapDestination{
		Address: sourceAddr,
		Port:    tunnel.DefaultTrapPort,
		// THE line not to touch: polling community, as-is.
		Community: w.Community,
	}, ""
}

// --- Journal phrases ---

// trapPosedMessage says what was written, and REFUSES to say it works.
//
// Pure and tested, because vocabulary here depends on the only costly misconception of the
// batch. "Traps configured on sw-a1" reads as "sw-a1 is reactive", and that is false: the
// line is in the device, the address comes from the proxy routing table, and nothing has
// crossed back yet. The day a firewall blocks 162/udp, this sentence is the only thing that
// will stop you looking for the fault in meshcraft.
func trapPosedMessage(deviceName, addr string, confirmed bool) string {
	var out = "Trap destination of \u00ab " + deviceName + " \u00bb written: " + addr
	if confirmed {
		return out + " — return path already confirmed by a received trap."
	}
	return out + " — the device carries the line, which does NOT prove that " +
		"its traps arrive: one will have to be received. Until then supervision remains " +
		"polling-based, i.e. correct but not reactive."
}

// trapStillConfirmed says whether the PREVIOUS confirmation survives configuration toward
// `addr`. Mirrors the `case` in store.SetTrapDestination, giving the same Go verdict to
// choose the journal phrase.
//
// Only ADDRESS is compared, where SQL also compares proxy, and that is exact here: configuration
// always targets the CURRENT collector, and a collector change already erased all stored
// destination (store.SetDeviceProxy). Address is therefore the only thing that can still
// differ — same proxy seen via another route, after topology change.
func trapStillConfirmed(d model.Device, addr string) bool {
	return d.TrapSeenAt != nil && d.TrapDestAddr == addr
}

// trapConfirmedMessage announces PROOF, and says WHICH of the two was obtained.
//
// This is the only batch message allowed to assert the channel works, and it is written
// once. Hence parameter `attributed`, which is not presentation detail: an accepted trap
// always proves this device's traps REACH this proxy, but whether the address meshcraft
// wrote receives them is known only by reading the datagram destination address
// (tunnel.Event.DestAddr). When the proxy cannot say it, the first sentence stays true and
// the second becomes a bet — conflating them was the first version's defect, and exactly
// the misconception this batch fights elsewhere.
func trapConfirmedMessage(deviceName, addr, proxyName string, attributed bool) string {
	var who = "\u00ab " + deviceName + " \u00bb"
	var by string
	if proxyName != "" {
		by = " by proxy \u00ab " + proxyName + " \u00bb"
	}
	if !attributed {
		var out = "Traps from " + who + " REACH the collector: a first trap " +
			"was accepted" + by + ". Supervision is therefore reactive. What is " +
			"not yet proven: that it is indeed the address written by meshcraft"
		if addr != "" {
			out += " (" + addr + ")"
		}
		return out + " that receives them — this proxy cannot say which of its " +
			"addresses a datagram was sent to, and the device could emit toward " +
			"another reachable destination on the same machine. Attribution remains " +
			"a hypothesis; the path itself exists."
	}
	var out = "Trap return path of " + who + " ESTABLISHED: a first trap was " +
		"accepted" + by
	if addr != "" {
		out += " on " + addr + ", the very address written into the device"
	}
	return out + ". The written address was until now a hypothesis about return " +
		"routing; it is verified."
}

// trapClockNote describes the gap between the date the PROXY put on the event and the
// controller's when it receives it. Empty when there is nothing to report.
//
// This is the place left to `tunnel.Event.TS` after removing it from `trap_seen_at`
// (cf. store.ConfirmTrapPath): a DIAGNOSTIC fact about fleet state, never a date that
// would pretend to order with controller dates.
//
// Despite its name, this measure now concerns only traps: port flap correlation also calls
// it (cf. applyPortTransition). That is even where it helps most, because events are
// FREQUENT there — drift shows on a flapping fleet, not on a destination configuration that
// happens once. Renaming would require moving it out of this file, which only matters the
// day a third path calls it.
//
// And the measure is not worthless: `portevents.go` long compared a proxy date to a
// controller date, so its flap windows were the written threshold PLUS machine drift — eleven
// seconds on the installation where the defect was found. It no longer does (single clock,
// the controller's), but nothing else in meshcraft measures this drift, and a proxy without
// NTP can drift by minutes.
//
// SIGN is what makes the measure readable, and why the two thresholds differ. A proxy date
// BEHIND is confusable with legitimate transport delay: the event may have waited in the
// proxy queue, then in a cut WAN. A proxy date in the FUTURE has no innocent explanation —
// no transport moves time backward.
func trapClockNote(proxyTS, at time.Time) string {
	if proxyTS.IsZero() {
		// Proxy older than the field: there is no skew, there is absence.
		return ""
	}
	var skew = at.Sub(proxyTS)
	if skew >= trapClockLate {
		return "the proxy timestamp is behind by " + skew.Round(time.Millisecond).String() +
			" compared to the controller — clock drift, or event delayed by the " +
			"proxy queue and WAN; the two look alike in that direction"
	}
	if -skew >= trapClockAhead {
		return "the proxy timestamp is AHEAD by " + (-skew).Round(time.Millisecond).String() +
			" compared to the controller — no transport delay can explain it: " +
			"this proxy's clock and the controller's diverge"
	}
	return ""
}

// trapClockNote thresholds. They frame a single question — "does this skew deserve to be
// said?" — whose answer is not symmetric.
const (
	// trapClockLate: beyond this, delay stops being explainable by healthy WAN latency alone.
	// Below, saying it would flood the log with a ping measurement.
	trapClockLate = 5 * time.Second
	// trapClockAhead: tighter, because a future date is a PURE symptom. One second passes
	// clock rounding and nothing else.
	trapClockAhead = time.Second
)

// trapSummary summarizes what configuration did, and especially what it did not verify.
func trapSummary(res driver.TrapResult) string {
	var out = "recipient " + res.Version + " toward " + res.Address + ":" + itoa(res.Port)
	if res.Set {
		out += " configured"
	} else {
		out += " already declared and conforming"
	}
	if res.Replaced {
		out += ", previous recipient replaced"
	}
	// Connector notes are what it could NOT verify (a `trapsEnbl` the device does not
	// return, for example). Hiding them would pass partially verified configuration for
	// verified configuration.
	var n string
	for _, n = range res.Notes {
		out += " — " + n
	}
	return out
}

// --- Configuration ---

// trapDestPose: destination to write, and proxy it comes from.
type trapDestPose struct {
	dest      driver.TrapDestination
	proxyID   string
	proxyName string
}

// trapAskError wraps failure of the QUESTION asked to the proxy.
//
// A type, so the handler can render the four distinct failures `askFailure` translates —
// proxy absent, proxy too old, timeout, protocol fault. Flattened into an ordinary error,
// they would send the operator to check their device where they need to restart a proxy.
type trapAskError struct{ err error }

func (e trapAskError) Error() string { return e.err.Error() }
func (e trapAskError) Unwrap() error { return e.err }

// trapDestToPose gathers everything needed to configure: collector, polling community, and
// address that collector says it uses.
//
// Three returns because three natures, like askCommunityFor: configuration, readable REFUSAL
// (a normal operations situation, with its repair action), and technical error.
func (s *Server) trapDestToPose(ctx context.Context,
	d model.Device) (trapDestPose, string, error) {
	var out trapDestPose
	if s.Secrets == nil {
		return out, "Trap destination impossible: this instance's vault is unavailable, " +
			"yet the community to write can only come from it.", nil
	}
	var covering, err = s.Store.ProxiesCoveringSite(ctx, d.SiteID)
	if err != nil {
		return out, "", err
	}
	var st snmpIdentityState
	if st, err = s.snmpPlanFor(ctx, d); err != nil {
		return out, "", err
	}
	var in = trapDestInputs{
		deviceName: d.Name, mgmtIP: d.MgmtIP,
		collector: d.ProxyID, collectorName: d.ProxyName,
		plan: st.Plan, credentialID: st.CredentialID,
	}
	// PUSH capability is read on the same connector as the plan, and it is the same one
	// that decides proxy listening (watchConfigFor). An unknown connector does not get here:
	// its plan is not supervised.
	//
	// ONLY the "PLATFORM" half is read here, and it must be understood why: the "connector"
	// half is [driver.TrapPusher], an optional interface on ADMINISTRATION ACCESS, not the
	// connector. It therefore asserts only once access is obtained, i.e. after reaching the
	// device (cf. applyTrapDestToDevice, which then returns the SAME refusal). This is not
	// loosening: capability is enough to spare a session on platforms we already know do not
	// emit — SwOS — and late assertion only serves inconsistent declarations.
	var drv, known = driver.Get(st.Driver)
	if known {
		in.trapsPush = drv.Capabilities(d.Model).Monitoring.Traps
	}

	var verdict, target = judgeTrapDest(in, covering)
	if verdict != trapDestReady {
		return out, trapDestRefusal(verdict, in), nil
	}
	out.proxyID, out.proxyName = target.ID, target.Name

	// Community: POLLING's, by snmpWatchesFor recipe — vault identifier, decryption, and
	// second phrase deliberately ignored (in v2c community IS the secret, there is never a
	// second).
	var _, enc, _, revErr = s.Store.RevealSiteCredential(ctx, st.CredentialID)
	if revErr != nil {
		return out, "", revErr
	}
	var community string
	// Unreadable community is not a controller failure: same cause deprives device of
	// collection, and trapDestinationFrom says it.
	community, _ = s.Secrets.Open(enc)

	// ADDRESS. Asked of collector, not deduced: cf. constraint 3 at file head, and
	// tunnel.AskSourceAddr for demonstration.
	if s.Tunnels == nil {
		// Wrapped like other question failures, so caller has only one translation table.
		return out, "", trapAskError{err: errNoTunnel}
	}
	var reply, askErr = s.Tunnels.Ask(ctx, target.ID, tunnel.AskRequest{
		Ask: tunnel.AskSourceAddr, IP: d.MgmtIP,
	})
	if askErr != nil {
		return out, "", trapAskError{err: askErr}
	}

	// Watch is reconstructed for this sole use, and that guarantees community equality:
	// destination cannot derive a different value from polling (cf. trapDestinationFrom).
	var dest, refusal = trapDestinationFrom(tunnel.SNMPWatch{
		DeviceID: d.ID, IP: d.MgmtIP, Community: community,
	}, reply.SourceAddr)
	if refusal != "" {
		return out, refusal, nil
	}
	out.dest = dest
	return out, "", nil
}

// applyTrapDestToDevice configures the destination and RECORDS what was configured.
//
// Administration path is that of applySNMPToDevice, identically — discovery proxy, site
// credentials, connector: the path we know works to talk to this device. It has no reason
// to be the collector's, and they are two different things: one carries a web admin session,
// the other is the address to INSCRIBE in the device.
//
// Recording comes AFTER configuration, never before. A stored destination the device did not
// retain is exactly the lie this batch exists to avoid — and the connector re-reads the device
// to know, because on this line a `statusCode 0` does not mean "done".
func (s *Server) applyTrapDestToDevice(ctx context.Context,
	d model.Device) (driver.TrapResult, []driver.Step, string, error) {
	var out driver.TrapResult
	var pose, refusal, err = s.trapDestToPose(ctx, d)
	if refusal != "" || err != nil {
		return out, nil, refusal, err
	}
	var conn, adm, _, admErr = s.adminFor(ctx, d)
	if adm != nil {
		defer adm.Close()
	}
	if admErr != nil {
		return out, transcriptOf(adm), "", admErr
	}
	// TRAP PUSH IS OPTIONAL, and this assertion is the second lock. The first —
	// `Capabilities.Monitoring.Traps` — already excluded platforms that cannot emit, BEFORE
	// reaching the device. This one catches capability and interface mismatch: a connector
	// that would announce push without knowing how to write it. REFUSAL is returned as
	// refusal not error — same sentence, because same fact from the operator's view (cf.
	// noTrapPushRefusal).
	var pusher, canPush = adm.(driver.TrapPusher)
	if !canPush {
		return out, adm.Transcript(), noTrapPushRefusal("\"" + d.Name + "\" (" +
			conn.Info().Name + ")"), nil
	}
	if out, err = pusher.ApplyTrapReceiver(ctx, pose.dest); err != nil {
		return out, adm.Transcript(), "", err
	}
	if err = s.Store.SetTrapDestination(ctx, d.ID,
		pose.dest.Address, pose.proxyID); err != nil {
		return out, adm.Transcript(), "", err
	}
	return out, adm.Transcript(), "", nil
}

// pipelineForTrapDest makes configuration inspectable afterward, like pipelineForSNMP of
// which it is the mirror: the only way to understand a failure without being at the screen
// when it happens. And here more than elsewhere, the transcript is the only trace of the
// write action — whose form is a reconstruction of the device bundle, not a documented API.
func pipelineForTrapDest(d model.Device, steps []driver.Step, failure error,
	summary string) store.Pipeline {
	var p = store.Pipeline{
		SiteID: d.SiteID, Kind: "monitoring", DeviceID: &d.ID,
		Steps: stepsFrom(steps),
	}
	if failure != nil {
		p.State = store.PipelineFailed
		p.Label = "Trap destination of \u00ab " + d.Name + " \u00bb"
		p.BlockedReason = failure.Error()
		return p
	}
	p.State = store.PipelineDone
	p.Label = "Traps of \u00ab " + d.Name + " \u00bb — " + summary
	return p
}

// provisionTrapDest is the PROVISIONING action: write the destination into the device just
// adopted.
//
// Called by provisionSNMP, AFTER identity configuration, and order is not negotiable: the
// connector refuses to declare a recipient on a device whose SNMP is off ("a trap recipient
// would serve no purpose there"), and ApplySNMP turns it on.
//
// An error never bubbles to the caller, as in provisionSNMP: an adopted device whose trap
// channel could not be opened remains an adopted device — it is supervised by polling,
// which is correct, just less reactive. Refusing adoption for that would be disproportionate.
//
// # Why REFUSALS do not go to the fleet journal
//
// They are expected, and for the entire existing fleet: no device has a declared collector,
// so `trapDestNoCollector` would be emitted at every adoption and every address change,
// indefinitely, for a state that does not change. Same reasoning as snmpWatchesFor, and it
// applies here for the same reason.
//
// That does not make the state invisible, and that is the difference that makes silence
// acceptable: `devices.trap_dest_addr` is null until something was configured, and the
// device record reads it (migration 0023). Information is where you look for it — on the
// device — instead of being shouted where nobody will reread it. A FAILURE goes to the
// journal: it is not expected.
func (s *Server) provisionTrapDest(ctx context.Context, d model.Device) {
	var res, steps, refusal, err = s.applyTrapDestToDevice(ctx, d)
	if refusal != "" {
		slog.Info("trap destination not configured", "device_id", d.ID,
			"name", d.Name, "reason", firstLine(refusal))
		return
	}
	if err != nil {
		s.emitDeviceBG(d, "trap.dest_failed", model.SevWarning,
			"Trap destination not written on \u00ab "+d.Name+" \u00bb: "+err.Error()+
				" — this device's supervision remains polling-based: correct, but not reactive.")
		s.recordPipeline(ctx, pipelineForTrapDest(d, steps, err, trapSummary(res)), "")
		return
	}
	s.emitDeviceBG(d, "trap.dest_applied", model.SevInfo,
		trapPosedMessage(d.Name, res.Address, trapStillConfirmed(d, res.Address)))
	s.recordPipeline(ctx, pipelineForTrapDest(d, steps, nil, trapSummary(res)), "")
}

// provisionTrapDestBG replays configuration on a CLEAN context, for callers that cannot
// wait — an HTTP request whose response already left.
//
// This is the ATTACHMENT CHANGE call point: changing collector changes the address to write,
// therefore falsifies what is in the device. `store.SetDeviceProxy` already forgets trace and
// confirmation — database state will not lie — but the device keeps emitting to the old
// address until someone rewrites the line. Reconfiguration remains to wire in
// handleSetDeviceCollector, beside the `PushWatchTo` it already does to both affected
// proxies, and for the same reason.
func (s *Server) provisionTrapDestBG(d model.Device) {
	// A clean bounded budget: request context dies with the response, and a web admin session
	// on this line is counted in seconds.
	var ctx, cancel = context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	s.provisionTrapDest(ctx, d)
}

// --- Explicit entry point ---

// handleApplyDeviceTrapDest reconfigures the destination on demand.
//
// Symmetric to POST /api/devices/{id}/snmp-apply: same permission level (`site_admin` on the
// device's site), same response body (`steps`, `result`, `ok`, `summary`). It is NECESSARY,
// and not only for symmetry: provisioning configuration fails durably for a cause that, itself,
// is repaired elsewhere — a device adopted before a collector was declared, a proxy down at
// the wrong moment, a firewall opened afterward. Without this route, the only way to replay
// configuration would be to change the device's model or address to trigger provisioning as a
// side effect, i.e. lie in inventory to obtain a supervision action.
//
// ONE divergence from snmp-apply, assumed: a REFUSAL is returned as 422 with its sentence,
// where snmp-apply puts `errNothingToPose` in a 200 `ok:false`. Refusals here all carry a
// repair action — attach, widen coverage, generate identity — and 422 is the form the rest
// of the product uses for that (cf. ask_handlers.go). Burying them in the same envelope as
// write failures would force the screen to read text to know whether to offer a button.
func (s *Server) handleApplyDeviceTrapDest(w http.ResponseWriter, r *http.Request) {
	var d, ok = s.deviceForWrite(w, r, "site_admin")
	if !ok {
		return
	}
	var res, steps, refusal, err = s.applyTrapDestToDevice(r.Context(), d)
	if refusal != "" {
		respondErr(w, http.StatusUnprocessableEntity, refusal)
		return
	}
	var ask trapAskError
	if errors.As(err, &ask) {
		// Question to proxy has four distinct failures and its translation table. A single
		// 502 would send you to restart a device where you need to restart a proxy.
		var code, msg = askFailure(ask.err)
		respondErr(w, code, "Trap destination not written: "+msg)
		return
	}
	var out = map[string]any{"steps": steps, "result": res}
	if err != nil {
		out["ok"], out["error"] = false, err.Error()
		respond(w, http.StatusOK, out)
		return
	}
	out["ok"], out["summary"] = true, trapSummary(res)
	// SevNotice not SevInfo: unlike provisioning, someone requested this action, and it
	// changes what a production device carries.
	s.emitDeviceEvent(r, d, "trap.dest_applied", model.SevNotice,
		trapPosedMessage(d.Name, res.Address, trapStillConfirmed(d, res.Address)))
	respond(w, http.StatusOK, out)
}

// --- Proof ---

// trapProofVerdict: what does this accepted trap prove?
type trapProofVerdict int

const (
	// trapProofConfirms: accepted by the proxy whose address is written in the device, AND at
	// that very address. FULL confirmation of our claim, and the only one that allows saying
	// the configured return path is verified.
	trapProofConfirms trapProofVerdict = iota
	// trapProofUnattributed: accepted by the right proxy, which could not say which of its
	// addresses the datagram targeted — binary older than `tunnel.Event.DestAddr`, or kernel
	// refusing PKTINFO.
	//
	// Device traps REACH this proxy: that is established, essential for reactivity, and it
	// gets recorded. What is missing is ATTRIBUTION to the configured address, and the message
	// says so instead of asserting. Refusing to record would be worse than nuancing: an entire
	// fleet of outdated proxies would show "never confirmed" although supervision is reactive
	// — and you would look for firewalls that do not exist.
	trapProofUnattributed
	// trapProofOtherAddr: accepted by the right proxy, but at ANOTHER of its addresses than
	// the one written in the device.
	//
	// Our claim is not confirmed, and that is the case this whole verification exists to
	// catch: the device carries a recipient meshcraft did not configure (a line left from a
	// previous collector, a second supervision tool), it emits toward it, and the address we
	// wrote may be reachable by nobody. Recording confirmation here would write "path
	// verified" on a path nothing took.
	trapProofOtherAddr
	// trapProofForeign: accepted by a proxy that is not the stored destination's — or when no
	// destination is stored. Return path works for that proxy, but not what meshcraft
	// configured: either someone configured the device by hand, or stored destination is stale
	// (symptom of attachment changed without reconfiguration). Worth noting, never to record
	// as confirmation.
	trapProofForeign
)

// judgeTrapProof says what an accepted trap proves about the stored destination.
//
// Pure, and separated for that reason: the decision distinguishing PROOF from coincidence,
// and conflating them would write "return path verified" on an address meshcraft did not
// configure — therefore on a path it knows nothing about.
//
// TWO comparisons not one, and the second is this batch's fundamental fix. The proxy only
// examines trap source IP and community: its socket listens on the wildcard and accepts
// everything on 162/udp. Attribution is therefore verified here, and nowhere else — the
// controller alone knows the address it wrote in the device.
//
// Empty `seenAddr` is not a false address, it is absence of answer, and the two are not
// treated alike: one nuances proof, the other refuses it.
func judgeTrapProof(destAddr, destProxyID, seenAddr, reportingProxyID string) trapProofVerdict {
	if destProxyID == "" || destProxyID != reportingProxyID {
		return trapProofForeign
	}
	if seenAddr == "" {
		return trapProofUnattributed
	}
	if seenAddr != destAddr {
		return trapProofOtherAddr
	}
	return trapProofConfirms
}

// confirmTrapPath consumes `tunnel.EventTrapSeen`: a device's first trap was just ACCEPTED
// by a proxy.
//
// This is the event that moves state from "address written" to "return path established", and
// nothing else can. A received trap is not debated: it crossed firewall, route, and ACL the
// configuration knew nothing about.
//
// But it does not alone say WHICH WAY it crossed, and that is what `judgeTrapProof` decides
// here in four outcomes rather than two. The proxy filters on source IP; datagram destination
// address (`ev.DestAddr`) is the only fact linking received trap to address meshcraft wrote.
// Depending on match, absence, or difference, the same trap is full confirmation,
// confirmation to call unattributed, or no confirmation at all.
//
// Pattern is that of applyDeviceState and applyPortTransition: write, fleet journal, wake
// screens. With a difference that matters, justifying database rather than Go test: this fact
// is journalized ONLY ONCE. The proxy emits this event once per device and proxy, but resets
// its counter as soon as the device leaves then re-enters its watch list — therefore at every
// tunnel reconnection and every fleet change. Without a controller-side bound, a proxy restart
// would produce a "return path established" line for the entire fleet at once.
// `store.ConfirmTrapPath` puts that bound in its `update`'s `where`, making it insensitive to
// two simultaneous traps.
func (s *Server) confirmTrapPath(ctx context.Context, proxyID string, ev tunnel.Event) {
	if ev.DeviceID == "" {
		return
	}
	var d, err = s.Store.GetDevice(ctx, ev.DeviceID)
	if err != nil {
		slog.Warn("trap accepted from unknown device", "err", err,
			"device_id", ev.DeviceID, "proxy_id", proxyID)
		return
	}
	// What is confirmed is THE written destination: cf. judgeTrapProof.
	var verdict = judgeTrapProof(d.TrapDestAddr, d.TrapDestProxyID, ev.DestAddr, proxyID)
	switch verdict {
	case trapProofForeign:
		slog.Info("trap accepted outside stored destination",
			"device_id", d.ID, "name", d.Name, "proxy_received", proxyID,
			"destination_proxy", d.TrapDestProxyID,
			"written_address", d.TrapDestAddr)
		return
	case trapProofOtherAddr:
		// WARN, and the only one in this function: channel works, but NOT where meshcraft
		// wrote it. Therefore the line configured in the device may serve no purpose, and
		// observed reactivity will vanish the day the other recipient is removed. Two causes,
		// and the message must carry both, because they are repaired oppositely — remove an
		// extra recipient in the device, assume address translation in front of the proxy.
		//
		// Bounded like the rest: proxy emits proof once per device and watch attachment, so
		// this repeats only at reconnections — not at every trap.
		slog.Warn("trap accepted at a DIFFERENT address than written in device: configured "+
			"destination is therefore not proven. Either the device carries a trap recipient "+
			"meshcraft did not configure and emits toward it, or this proxy receives its traps "+
			"via address translation",
			"device_id", d.ID, "name", d.Name, "proxy_id", proxyID,
			"written_address", d.TrapDestAddr, "reached_address", ev.DestAddr)
		return
	}
	// Proxy date no longer goes to database (cf. store.ConfirmTrapPath): not the same clock
	// as `trap_dest_at`, and adjacent columns invited meaningless comparison. It keeps
	// diagnostic value, and that is where it has it.
	var note = trapClockNote(ev.TS, time.Now())
	if note != "" {
		slog.Warn("fleet clocks disagree", "device_id", d.ID, "name", d.Name,
			"proxy_id", proxyID, "skew", note, "proxy_date", ev.TS)
	}
	var first, confErr = s.Store.ConfirmTrapPath(ctx, d.ID, proxyID)
	if confErr != nil {
		slog.Warn("trap path confirmation not recorded", "err", confErr,
			"device_id", d.ID, "proxy_id", proxyID)
		return
	}
	if !first {
		// Already confirmed: nothing to write, nothing to announce. Majority case as soon as a
		// proxy restarts.
		return
	}
	var siteID, deviceID = d.SiteID, d.ID
	_ = s.Store.InsertEvent(ctx, model.Event{
		SiteID: &siteID, DeviceID: &deviceID,
		Type: "trap.path_confirmed", Severity: model.SevNotice, Actor: "monitoring",
		Message: trapConfirmedMessage(d.Name, d.TrapDestAddr, d.ProxyName,
			verdict == trapProofConfirms),
	})
	// ChangeDevice: what moved is the device RECORD (trap channel state), not a link or
	// reconciliation state. No user HTTP request here, therefore no middleware to wake screens
	// for us.
	s.NotifySiteChanged(siteID, ChangeDevice)
}
