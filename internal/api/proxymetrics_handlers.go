package api

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"meshcraft/internal/auth"
	"meshcraft/internal/driver"
	"meshcraft/internal/metricbuf"
	"meshcraft/internal/model"
	"meshcraft/internal/snmppoll"
	"meshcraft/wire/agentv1"
)

// Reception of metrics pushed by proxies.
//
// The proxy collects (SNMP today, traps and streaming tomorrow) and pushes BATCHES;
// the controller relays them to VictoriaMetrics. Storage is never reachable from a
// proxy network — which we do not control — and that is precisely why the relay exists.
//
// Authentication is the same as the heartbeat: the (proxy_id, secret) pair. No user
// session here; a proxy has none.

// maxSamplesPerBatch caps a batch. A proxy replaying several hours of buffer at once
// must not blow up controller memory: it will replay in several passes, which its
// buffer knows how to do.
const maxSamplesPerBatch = 20000

// worstBytesPerSample: what ONE sample weighs in the JSON body, in the realistic
// worst case.
//
// Measured, not estimated, on samples that `snmppoll.Samples` actually produces —
// `device` (UUID), `ifindex`, `port`, `iftype` labels, metric names like
// `meshcraft_if_in_octets_total`, RFC 3339 timestamps with nanoseconds, and the
// POST authentication envelope:
//
//	counters, port "GE12"                          -> 196 B/sample
//	counters, port "Server room - rack B12 - ..."  -> 229 B/sample (37 chars)
//	both OID sets mixed, short port                -> 193 B/sample
//
// COUNTERS dimension the budget: `if_state` is read every 30 s and `if_counters`
// every 5 s, so a batch is almost exclusively counters — and a counter is the heaviest
// of the six, thirteen value digits against one for a state.
//
// These figures match the 208 B measured by the proxy batch
// (cmd/proxy/snmp_throughput_test.go), whose prototype names ports
// "GigabitEthernet52": the twelve-byte gap is exactly the label length difference.
// It is the PORT NAME that varies this figure, and the operator chooses it — so there
// is no bound on the controller side.
//
// 320 leaves 40% beyond the measured long case, room for a 128-character label.
// Locked by TestSNMPSampleWeight: the day a sample shape grows, this test will say so,
// not a proxy blocked in production.
const worstBytesPerSample = 320

// maxProxyMetricsBody caps the BODY of this route, and that is the ceiling that
// actually decided until now.
//
// The fixed default: this route advertised maxSamplesPerBatch = 20,000 and read its
// body with `decode`, capped at 1 MiB — about 5,000 samples. The displayed safeguard
// was therefore UNREACHABLE: you could not submit 20,000 samples, the body was cut well
// before. And the cut did not present as a cut: the decoder returned a syntax error, the
// controller a silent 400, the proxy did `Nack`, `metricbuf.Take` returned the SAME
// reserved batch, and it replayed forever. Not partial loss: complete upload stop for
// that proxy, without a word saying why.
//
// The chosen fix is to LOOSEN this route rather than lower the advertised count, for
// two reasons:
//
//  1. a COUNT cap cannot be made exact by a BYTE cap it does not know: sample weight
//     depends on port name length, which the operator chooses. Lowering to ~4000 would
//     have moved the lie without removing it, and sacrificed 75% of advertised capacity
//     to cover the worst naming case;
//  2. lowering would have REFUSED a proxy from another version that read "20,000" and
//     sticks to it. That is exactly the proxy that blocks today. Loosening unblocks it
//     instead of opposing a better-formulated refusal.
//
// 20,000 x 320 B = 6.1 MiB; 8 MiB leaves 31% headroom. At measured weight (229 B), a
// full batch weighs 4.4 MiB, 54% of the cap. And 8 MiB is already the cap used for
// site configuration import (devices_handlers.go), so not a new precedent in this code.
//
// The cost is transient memory: 20,000 decoded samples, each with a label map, hold
// about ten MiB for the duration of one request. That is the price of the batch this
// route says it accepts — and that is precisely what maxSamplesPerBatch bounds.
const maxProxyMetricsBody = 8 << 20

// batchRecovery: the recovery to announce when the body was refused for size. It names the
// sender (the proxy) and its setting, because on this route there is no human to read
// the message: it will end up in a log, and must be sufficient there.
const batchRecovery = "; proxy batch too large, reduce maxPerFlush" +
	" (this is not a content problem)"

// proxyMetricsBatch is the body expected on /api/proxy/metrics.
// proxyMetricsBatch is the agent contract's metrics push (agentv1.MetricsRequest):
// the credentialed envelope plus a batch of wire samples.
type proxyMetricsBatch = agentv1.MetricsRequest

// readProxyMetricsBatch reads and caps a batch, and responds itself on refusal.
//
// Isolated from the rest of processing for testability: HERE is where the coincidence
// between what the route advertises and what it accepts is decided, and that property
// must be exercisable without a database or authenticated proxy.
//
// Order matters. The count safeguard runs BEFORE authentication — therefore before the
// database round trip: a batch manifestly out of spec is refused on shape; there is
// nothing to gain from querying the database first. And it must run after decoding,
// which is precisely why the byte cap must be larger than the advertised batch.
func readProxyMetricsBatch(w http.ResponseWriter, r *http.Request,
	in *proxyMetricsBatch) bool {
	var err = readJSON(w, r, in, maxProxyMetricsBody)
	if err != nil {
		failJSON(w, err, batchRecovery)
		return false
	}
	if len(in.Samples) > maxSamplesPerBatch {
		// Refuse outright rather than truncate silently: a silently truncated batch would
		// leave gaps nobody could explain. The message carries both numbers: "too large"
		// without saying by how much does not help anyone tune batch size.
		respondErr(w, http.StatusRequestEntityTooLarge,
			"batch too large: "+strconv.Itoa(len(in.Samples))+" samples for a "+
				"maximum of "+strconv.Itoa(maxSamplesPerBatch)+
				"; split it up, the proxy buffer knows how")
		return false
	}
	return true
}

func (s *Server) handleProxyMetrics(w http.ResponseWriter, r *http.Request) {
	var in proxyMetricsBatch
	if !readProxyMetricsBatch(w, r, &in) {
		return
	}
	var _, _, err = s.Store.AuthProxy(r.Context(), in.ProxyID, auth.HashToken(in.Secret))
	if err != nil {
		respondErr(w, http.StatusUnauthorized, "unknown or revoked proxy")
		return
	}
	if s.TSDB == nil {
		// Say so, rather than accept and drop. The proxy will keep its batch.
		respondErr(w, http.StatusServiceUnavailable,
			"metrics storage not configured on this controller")
		return
	}
	// THE SCOPE FILTER, BEFORE STORAGE. A written sample is a curve an operator
	// believes: cf. scopedSamples for what is dropped and why.
	//
	// `in.ProxyID` is the one `AuthProxy` just validated, never the one the body
	// announces without proof: that is what makes the control opposable.
	var kept, drop = s.scopedSamples(in.ProxyID, in.Samples)
	// An entirely dropped batch has nothing to write — and especially not an empty POST
	// to VictoriaMetrics, whose failure would make the proxy replay a batch refused
	// identically.
	if len(kept) > 0 {
		if err = s.TSDB.Write(r.Context(), kept); err != nil {
			// An error here must reach the proxy: that is what makes it PUT the batch
			// back in its buffer instead of losing it.
			respondErr(w, http.StatusBadGateway, err.Error())
			return
		}
	}
	// The facade must show REAL port state, not storage alone. A metric that only
	// reaches VictoriaMetrics forces opening another screen to know if a cable is
	// plugged — when the answer fits in the port cell you are already looking at.
	//
	// Receives `kept` yet runs the check again (cf. applyPortTelemetry): defense in
	// depth, not forgetfulness. A path that WRITES inventory must not depend on caller
	// vigilance, and the second consultation costs nothing — one shared lock per device,
	// on an already decoded batch.
	s.applyPortTelemetry(r.Context(), in.ProxyID, kept)
	// The rejection count goes to the PROXY, in its response. The sender can fix it,
	// and that is the only un-amortized path: the controller log is capped at one line
	// per proxy per minute (cf. proxyScopes.refuse), and adding one line per batch — six
	// per minute per proxy on an ordinary fleet, as many as the proxy decides under
	// attack — would have been a second amortization mechanism beside the first.
	//
	// 200 despite rejections, and that is deliberate: a partially authorized batch is
	// written in part. Refusing the whole would lose LEGITIMATE metrics because of one
	// doubtful label, and the proxy would `Nack` then replay the same batch forever (cf.
	// metricbuf.Take) — a denial of service on its own monitoring, triggered by the
	// attacker we are trying to contain.
	var body = map[string]any{"accepted": len(kept)}
	if drop.total() > 0 {
		body["rejected"] = drop.total()
		body["rejected_scope"] = drop.refused
		body["rejected_no_device"] = drop.unlabelled
	}
	respond(w, http.StatusOK, body)
}

// scopeDrop: what a batch lost at the filter, in SAMPLES.
//
// Two counters and not one total, for the same reason as `watchTally`: the two causes
// do not call for the same fix. "Out of scope" is a proxy speaking about a device not
// entrusted to it — a fleet defect, or someone trying; "no label" is a sample SHAPE
// defect, therefore a sender of another kind or version. One total would send diagnosis
// into a wall half the time.
type scopeDrop struct {
	// refused: samples carrying a `device` this proxy may not describe.
	refused int
	// unlabelled: samples without an exploitable `device` label.
	unlabelled int
}

func (d scopeDrop) total() int { return d.refused + d.unlabelled }

// scopedSamples keeps from a batch only what THIS proxy may write.
//
// # Why this filter exists
//
// The other two proxy fact write paths are guarded (cf. proxyscope.go); this one was
// not. A compromised proxy could no longer write foreign device state or inventory, but
// could still POLLUTE its curves — and a curve is state an operator believes. Invented
// traffic on a port, a fantasy temperature, an exploding error counter, and someone is
// sent looking for a fault that does not exist, or a real one is masked. VictoriaMetrics
// knows nothing of proxies: what enters stays, and a false series does not leave a graph.
//
// # Required authority is COLLECTION
//
// Same as port telemetry, for the same reason: these samples come from SNMP polling,
// and only a device's declared collector polls it. A proxy that pings the device without
// being its collector has no way to measure its octets. Requiring only site coverage
// would have let curve writes through on a neighbor's device — the hole, in a slightly
// less visible form.
//
// # The `device` label, and the sample that lacks it
//
// `snmppoll.Samples` sets it on EVERY sample, unconditionally, before any other (cf.
// internal/snmppoll). A sample without it therefore does not come from an up-to-date
// proxy collector. It is DROPPED, and counted separately:
//
//   - there is nothing to verify. Scope is judged per device; without an identifier,
//     no authority opposes that sample, and letting it through would make omitting the
//     label the complete filter bypass — an attacker would stop writing `device`, and
//     write everything else;
//   - serial name and other labels suffice to pollute a graph. "No device" does not
//     mean "no effect";
//   - closed by default, as `iftype` already is one step later to touch inventory, and
//     for the same trade-off: guessing produces a credible false graph.
//
// The price is named: a future sample producer WITHOUT a device — a proxy-native metric,
// for example — must go through its own authority, not slip into this batch. That is a
// visible refusal (the count goes in the response) rather than an invisible hole.
//
// # Cost on the hot path
//
// One label read per sample, and one scope consultation per DEVICE (cf. scopeBatch):
// a few dozen shared locks for twenty thousand samples, against a multi-megabyte JSON
// decode already paid just before.
//
// And NO allocation in the normal case: when nothing is dropped — every well-ordered
// fleet, on every push, every ten seconds — the original batch is returned as-is. Copy
// is paid only when there is truly something to remove, which is also the only moment
// it makes sense. The second pass rereads the first pass memoization: no extra decision,
// and no extra log line.
//
// The returned list is never partial aliasing of the input batch: filtering in place
// would have saved this allocation by overwriting the caller's batch, whose size is
// what counts rejections. A security filter that damages its input is a filter you read
// twice before touching.
func (s *Server) scopedSamples(proxyID string,
	samples []metricbuf.Sample) ([]metricbuf.Sample, scopeDrop) {
	var batch = s.scopeBatchFor(proxyID, authorityPoll)
	var drop scopeDrop
	var i int
	for i = range samples {
		var deviceID = samples[i].Labels["device"]
		if deviceID == "" {
			drop.unlabelled++
			continue
		}
		if !batch.allows(deviceID) {
			drop.refused++
		}
	}
	if drop.total() == 0 {
		return samples, drop
	}
	var kept = make([]metricbuf.Sample, 0, len(samples)-drop.total())
	for i = range samples {
		if batch.allows(samples[i].Labels["device"]) {
			kept = append(kept, samples[i])
		}
	}
	return kept, drop
}

// facade is what a device actually carries on its front panel.
type facade struct {
	prefix string
	ports  map[string]bool
	// norm: normalized form -> canonical name, to match an SNMP label to a port without
	// requiring character-perfect equality. An AMBIGUOUS normalized form is removed:
	// matching at random is worse than not matching.
	norm map[string]string
	// namer: the connector's own translation from a PLATFORM port name to the
	// standard-model one, when it declares that capability (driver.PortNamer).
	//
	// THIS IS WHAT KEEPS A NUMBERING CONNECTOR RESOLVABLE. On such a platform the
	// facade carries standard names ("GE11") while SNMP `ifDescr` carries the
	// hardware's own ("Ethernet1/11"): the three purely textual steps below all
	// miss, and every sample would be skipped — a switch whose ports all read as
	// down, and whose every LLDP neighbor is dropped, while the device is fine.
	// Observed on an FS S3900 the day its connector adopted GE-style names.
	namer driver.PortNamer
}

// normalizePort reduces a port name to what distinguishes it: lowercase, and only
// letters and digits. "SFP-SFPplus1", "sfp sfpplus 1", and "sfp-sfpplus1" designate the
// same port, and a vendor changes this punctuation from one firmware version to the next.
func normalizePort(s string) string {
	var b []rune
	var r rune
	for _, r = range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b = append(b, r)
		}
	}
	return string(b)
}

// resolve matches an SNMP interface label (`ifDescr`) to a facade port, and returns ""
// when it cannot.
//
// Returning "" is a result, not a failure to work around: a sample we cannot attach is
// SKIPPED. That is the rule replacing the old "prefix + ifIndex" arithmetic — which
// only worked on devices whose ports are numbered 1..N in index order, a vendor
// coincidence and not a protocol property.
//
// Scale goes from surest to most tolerant:
//
//  1. exact equality — MikroTik name interfaces like their facade ("ether1", "sfp-sfpplus1");
//  2. normalized equality — same name modulo case and punctuation;
//  3. facade prefix + label — Instant On returns a bare number ("1"), which the facade
//     calls "GE1";
//  4. the CONNECTOR's own translation (driver.PortNamer) — the only step that can
//     bridge two different vocabularies, e.g. FS S3900's "Ethernet1/11" to the
//     standard-model "GE11". Last because it is the only one that needs the driver
//     registry; a connector that declares no translation simply never reaches it.
//
// THIS IS THE METRICS ENTRY POINT, and step 3 is the reason there are two: see
// [facade.resolveNeighborPort] for the caller that must not have it.
func (f facade) resolve(descr string) string {
	return f.match(descr, true)
}

// resolveNeighborPort matches a FAR END's port identifier — `lldpRemPortId` or
// `lldpRemPortDesc`, as reported by a neighbour about ITSELF — to a facade port.
//
// Same ladder as [facade.resolve] MINUS the prefix arithmetic, and the two
// callers' inputs are why they cannot share one:
//
//   - `ifDescr` is OUR OWN agent's label for OUR OWN interface. When an Instant
//     On serves a bare "1" there, the number IS this device's port number —
//     that is what the platform means by it — and step 3 turning it into GE1 is
//     a translation, not a guess;
//   - `lldpRemPortId` is whatever the FAR platform decided to put in the TLV,
//     and the standard lets it be anything: an interface name, a MAC, a
//     locally-assigned string, an ifIndex. MEASURED on this fleet 2026-08-10,
//     five dialects for the field and two rows carrying a bare number —
//     "521" from a Juniper (its PortDesc says the port is really ge-0/0/9.0)
//     and "50" / "51" from sw-labo-1930, in inventory but announcing itself
//     by serial so it resolves to no device yet. Concatenated, "521" would
//     have become GE521 and "50" GE50, judged by nothing but whether the
//     facade happens to be that big.
//
// So on this path step 3 reintroduces exactly the rule this function's doc says
// it REPLACED — "prefix + ifIndex arithmetic, which only worked on devices
// whose ports are numbered 1..N in index order, a vendor coincidence and not a
// protocol property". On the metrics path the label really is a port number; on
// this one it need not be, and nothing in the row says which.
//
// WHAT IT COSTS, stated rather than hidden: a far end that only ever announces
// an ifIndex now resolves to nothing, so its cables are drawn "seen from one
// side" instead of end to end. That is the CHEAP error. The expensive one is the
// other direction — [markRefutedObservations] is port-exact, so a confidently
// wrong far port held by one fresher stranger can retire a cable that really
// exists (cf. resolvePeerPort's own header). Under-assertion is fully covered
// downstream; over-assertion is not.
func (f facade) resolveNeighborPort(descr string) string {
	return f.match(descr, false)
}

// match is the shared ladder. `prefixArithmetic` says whether this caller may
// read a label as a bare port NUMBER of this facade — step 3, and the one part
// of step 4 that amounts to the same thing.
func (f facade) match(descr string, prefixArithmetic bool) string {
	if descr == "" {
		return ""
	}
	if f.ports[descr] {
		return descr
	}
	var name, seen = f.norm[normalizePort(descr)]
	if seen {
		return name
	}
	if prefixArithmetic {
		if f.ports[f.prefix+descr] {
			return f.prefix + descr
		}
		if name, seen = f.norm[normalizePort(f.prefix+descr)]; seen {
			return name
		}
	}
	if f.namer != nil {
		// A connector whose translation IS prefix arithmetic would otherwise let
		// step 3 back in through step 4: hpe/procurve's StandardPortName turns a
		// bare number into GE<n>, which is character for character what was just
		// refused above. Narrowing one and not the other would have made the
		// guarantee depend on which connector the far device happens to run.
		//
		// Only a DIGITS-ONLY label is held back — every fleet dialect the namer
		// exists for carries letters or a slash ("Te1/0/2", "0/18",
		// "Eth 1/26"), so nothing that resolves today stops resolving.
		if prefixArithmetic || !allDigits(descr) {
			var std, ok = f.namer.StandardPortName(descr)
			// The translation is only trusted when the facade actually carries the
			// port it names: a connector answering for a stacking unit this device
			// does not expose must not conjure an inventory port.
			if ok && f.ports[std] {
				return std
			}
		}
	}
	return ""
}

// allDigits reports whether the label is a bare number and nothing else — the
// shape that carries no evidence of being a port name.
func allDigits(s string) bool {
	if s == "" {
		return false
	}
	var r rune
	for _, r = range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// names returns facade port names, SORTED.
//
// Sorted because callers compose inventory patches compared from one pass to the next:
// Go map iteration order varies each run, and would make two identical patches differ.
func (f facade) names() []string {
	var out = make([]string, 0, len(f.ports))
	var k string
	for k = range f.ports {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// facadeOf enumerates ports the facade declares.
//
// Serves as a FILTER to telemetry. A device exposes far more SNMP interfaces than ports
// — VLAN interfaces, aggregates, loops, with indexes at 1000, 3000, 20000, and 100000
// on a 1930 — and "prefix + ifIndex" turned as many into phantom inventory ports.
//
// Falls back to ALREADY known ports when the facade is unreadable: we keep refreshing
// what exists, never creating anything. The invariant holds in both cases, and that is
// what matters — a measurement describes a port, it does not discover one.
func facadeOf(d model.Device) facade {
	var out = facade{
		prefix: portPrefix(d),
		ports:  map[string]bool{},
		norm:   map[string]string{},
	}
	// The connector's port-name translation, when it has one. Resolved from the
	// registry by the driver the device was identified as; absent driver, absent
	// capability, or unknown driver simply leaves it nil (cf. resolve step 4).
	var drv, known = driver.Get(driver.DriverNameFromExtra(d.Extra))
	if known {
		var namer, canName = driver.AsPortNamer(drv)
		if canName {
			out.namer = namer
		}
	}
	// ambiguous: normalized forms shared by several ports. They can only serve one purpose
	// — picking the wrong port.
	var ambiguous = map[string]bool{}
	var add = func(name string) {
		out.ports[name] = true
		var key = normalizePort(name)
		if key == "" {
			return
		}
		var prev, seen = out.norm[key]
		if seen && prev != name {
			ambiguous[key] = true
			return
		}
		out.norm[key] = name
	}
	var groups []struct {
		Prefix string `json:"prefix"`
		From   int    `json:"from"`
		Count  int    `json:"count"`
	}
	if json.Unmarshal(d.PortGroups, &groups) == nil {
		var g int
		for g = range groups {
			var prefix = groups[g].Prefix
			if prefix == "" {
				prefix = out.prefix
			}
			var from = groups[g].From
			if from <= 0 {
				from = 1
			}
			// An absurd range comes from a badly entered facade: skip it rather than
			// generate thousands of names.
			if groups[g].Count < 1 || groups[g].Count > 4096 {
				continue
			}
			var i int
			for i = 0; i < groups[g].Count; i++ {
				add(prefix + strconv.Itoa(from+i))
			}
		}
	}
	if len(out.ports) == 0 {
		var existing map[string]json.RawMessage
		if json.Unmarshal(d.Ports, &existing) == nil {
			var name string
			for name = range existing {
				add(name)
			}
		}
	}
	var key string
	for key = range ambiguous {
		delete(out.norm, key)
	}
	return out
}

// inventoryEligible says whether ONE sample may refresh the facade, from its
// labels and from what collects its device. PURE, so the decision is testable
// without the database, the tunnel and the metrics storage its caller needs.
//
//   - a sample carrying SNMP identity (`ifindex`/`iftype`) must say `iftype` =
//     ethernet. An older proxy does not send `iftype`: its samples still feed
//     metrics, but no longer modify the facade — deliberately closed-by-default,
//     because the alternative is guessing, and picking the wrong port produces a
//     credible false graph;
//   - a sample with NEITHER label is a streaming collector's, and it may patch
//     ONLY a device whose declared collector is gNMI. For two days
//     core01.lab's faceplate showed every port unplugged while 26 links were
//     up and its oper-status samples flowed: the old gate read "no iftype" as
//     "old proxy" and dropped every one of them, so `link` was only ever written
//     back when a device was SNMP-polled. The port-name gate that actually
//     protects inventory is `facade.resolve` at the caller — a gNMI key it
//     cannot match to the facade (Vlan99, Port-Channel11, Management1) is
//     skipped, never invented — so "telemetry never creates a port" holds on
//     this path exactly as on the SNMP one (cf. internal/metricbuf/names.go).
func inventoryEligible(labels map[string]string, streamed bool) bool {
	var snmpShaped = labels["iftype"] != "" || labels["ifindex"] != ""
	if snmpShaped {
		return labels["iftype"] == strconv.Itoa(snmppoll.IfTypeEthernet)
	}
	return streamed
}

// applyPortTelemetry reports measured port state into inventory.
//
// Two metrics only: link and negotiated speed. Those are what the facade can render — an
// octet counter has no cell to display, and writing it would bloat inventory unseen.
//
// Silent on failure: the metric is ALREADY stored at this stage, and refusing the proxy
// batch because a display could not refresh would make it replay in a loop.
//
// # Scope control
//
// A sample's `device` label is chosen by the PROXY, like a tunnel event's `DeviceID`,
// and this path WRITES inventory (`devices.ports`). Without verification, a proxy could
// show arbitrary link state on any switch in the instance — a screen that LIES about a
// cable, exactly what this path exists to prevent.
//
// Required authority is COLLECTION, not ping: these samples come from the device's
// declared collector — an SNMP poll or a gNMI subscription, the scope's `poll` set is
// the union of both (cf. proxyscope.go) — and a proxy that merely pings the device has
// no way to measure its ports.
//
// The decision is made ONCE per device per batch (cf. scopeBatch, which carries this
// memoization and shares it with the metrics filter): a full batch counts twenty thousand
// samples for a few dozen devices, and consulting per sample would produce as many refusal
// log lines as measurements — the log drowned by the mechanism meant to alert.
//
// The check SUBSISTS although the caller already filters the batch (cf. scopedSamples).
// That is intentional: this function writes `devices.ports`, and an inventory write path
// must not depend on caller vigilance. The next caller will not have read this comment.
func (s *Server) applyPortTelemetry(ctx context.Context, proxyID string,
	samples []metricbuf.Sample) {
	// Naming prefix comes from EACH device's facade: "GE1" here is not "Gi1" elsewhere,
	// and a key matching no facade port would create a phantom port in inventory.
	var facades = map[string]facade{}
	// streamed: this device's declared collector is gNMI, whose samples carry
	// neither `ifindex` nor `iftype` BY DESIGN (cf. internal/gnmipoll/translate.go).
	// Memoized beside the facade because both come from the same device read.
	var streamed = map[string]bool{}
	var patches = map[string]map[string]map[string]any{}
	var scope = s.scopeBatchFor(proxyID, authorityPoll)

	var sm metricbuf.Sample
	for _, sm = range samples {
		var deviceID = sm.Labels["device"]
		if !scope.allows(deviceID) {
			continue
		}
		var f, known = facades[deviceID]
		if !known {
			var d, err = s.Store.GetDevice(ctx, deviceID)
			if err != nil {
				facades[deviceID] = facade{}
				continue
			}
			f = facadeOf(d)
			facades[deviceID] = f
			streamed[deviceID] = collectKindOf(
				driver.DriverNameFromExtra(d.Extra), d.Model) == "gnmi"
		}
		if !inventoryEligible(sm.Labels, streamed[deviceID]) {
			continue
		}
		// The name comes from the interface LABEL, never from arithmetic on its index.
		// Telemetry never CREATES a port either: a label we cannot match to the facade is
		// skipped, not invented.
		var name = f.resolve(sm.Labels["port"])
		if name == "" {
			continue
		}
		if patches[deviceID] == nil {
			patches[deviceID] = map[string]map[string]any{}
		}
		if patches[deviceID][name] == nil {
			patches[deviceID][name] = map[string]any{}
		}

		switch sm.Name {
		case "meshcraft_if_oper_status":
			// ifOperStatus: 1 = up, everything else = not up. The facade expects link
			// speed or nothing; let speed fill it in.
			if sm.Value != 1 {
				patches[deviceID][name]["link"] = ""
			}
		case "meshcraft_if_speed_mbps":
			patches[deviceID][name]["speed_mbps"] = sm.Value
		}
	}

	// Speed is valid only if the link is up: a down port keeps its ADMINISTRATIVE speed
	// in ifHighSpeed, and showing it would suggest a cable.
	var deviceID string
	for deviceID = range patches {
		var ports = patches[deviceID]
		var name string
		for name = range ports {
			var down, isDown = ports[name]["link"]
			var speed, hasSpeed = ports[name]["speed_mbps"]
			if isDown && down == "" {
				ports[name]["link"] = ""
				delete(ports[name], "speed_mbps")
				continue
			}
			if hasSpeed {
				// Facade label comes from the catalog: one way to name "1G" across the product.
				var mbps, isNum = speed.(float64)
				if isNum && mbps > 0 {
					ports[name]["link"] = speedLabel(int(mbps))
				} else {
					ports[name]["link"] = ""
				}
				delete(ports[name], "speed_mbps")
			}
		}
		// This path no longer writes to the fleet log, and that is the whole point of the
		// batch: a port drop has only one logging path, the tunnel event channel (cf.
		// portevents.go and handleProxyEvent). Both paths coexisted for one batch, and the
		// result was two lines per transition, two vocabularies for one fact, and anti-flap
		// bypassed by this one — a duplicate worse than either path alone. What remained to
		// save here was the free-port filter: it lives on the event path (cf. portManaged).
		//
		// What remains is WAKING SCREENS. It cannot come from elsewhere: `/api/proxy/metrics`
		// is outside the session group (a proxy has none), so `notifyOnWrite` middleware never
		// runs there. And it is conditioned on a real CHANGE: a push arrives every ten seconds
		// on a fleet where almost nothing moves, and notifying each time would make every
		// browser on the site reread the facade six times per minute to redraw the same image.
		var before, getErr = s.Store.GetDevice(ctx, deviceID)
		var raw, mErr = json.Marshal(ports)
		if mErr != nil {
			continue
		}
		if s.Store.UpdatePortTelemetry(ctx, deviceID, raw) != nil {
			// Silent, and no notification: nothing changed in the database, so nothing to
			// reread. The metric, however, is already stored at this stage.
			continue
		}
		if getErr == nil && portLinksChanged(before, ports) {
			s.NotifySiteChanged(before.SiteID, ChangePort)
		}
	}
}

// portLinksChanged says whether a telemetry batch actually changes link state as stored.
//
// Serves ONLY to decide whether to wake screens open on the site — there is no log
// behind it anymore, so no need to know transition direction or order several. A boolean
// suffices: the site stream carries no change detail, only "this site moved, reread".
//
// A device whose `ports` is unreadable is assumed CHANGED. That is the right default
// this way: one extra refresh costs only a read; a missed refresh leaves a screen that
// LIES about a cable.
func portLinksChanged(d model.Device, patch map[string]map[string]any) bool {
	var current map[string]map[string]any
	if json.Unmarshal(d.Ports, &current) != nil {
		return true
	}
	var name string
	for name = range patch {
		var after, hasLink = patch[name]["link"].(string)
		if !hasLink {
			continue
		}
		// Missing `current[name]` yields a nil map you can index: an unknown inventory port
		// therefore means "no link", which is exact.
		var before, _ = current[name]["link"].(string)
		if before != after {
			return true
		}
	}
	return false
}
