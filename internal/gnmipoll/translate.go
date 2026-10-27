package gnmipoll

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"

	"meshcraft/internal/metricbuf"
)

// The OpenConfig path table, and the translation into samples.
//
// The analogue of snmppoll's OID block, and it carries the same warning: this is
// where correctness lives. Associating a counter with the wrong port, or
// publishing an enum as if it were a number, produces a CREDIBLE FALSE GRAPH —
// strictly worse than a gap, because nothing on screen says to distrust it.
//
// # NOTHING HERE HAS BEEN CAPTURED
//
// The paths are read off the OpenConfig models, not off a device. Which leaves a
// given EOS train actually serves is unverified, and three are in real doubt:
// `counters/in-discards`, `counters/out-discards` and `counters/
// carrier-transitions`. A leaf the device does not serve costs nothing here — it
// simply never arrives — which is why the table is written wide rather than
// trimmed to what is certain.
//
// One doubt is NOT free, and it is the speed: OpenConfig has both
// `ethernet/state/port-speed` (the configured/negotiated speed enum) and
// `ethernet/state/negotiated-port-speed` on newer models. If a train serves only
// the second, [MetricIfSpeedMbps] stays empty for every Arista and nothing says
// why. Adding the second path is one line; knowing which to add needs one
// capture.
//
// # The two mappings a silent lie would live in
//
// Both are string enums, and both must land in a series a SNMP producer already
// fills for the rest of the fleet:
//
//  1. `oper-status` is UP / DOWN / TESTING…, and meshcraft_if_oper_status is
//     defined as the raw IF-MIB value (1 = up, 2 = down — cf.
//     internal/metricbuf/names.go, and the transition detection in cmd/proxy that
//     reads it). Emitting 1 for "the string was UP" and 0 otherwise would look
//     right on a graph and be wrong everywhere a comparison happens: a port
//     reported DOWN by SNMP would sit at 2 and the same port reported DOWN by
//     gNMI would sit at 0, in one series;
//  2. `port-speed` is SPEED_1GB…, and meshcraft_if_speed_mbps is Mbit/s. The
//     ENUM SPELLING IS THE RISK: it differs across models and trains
//     (SPEED_1GB vs SPEED_1G has been seen in the wild), and a spelling this
//     table does not know produces no sample at all rather than a wrong one —
//     deliberately, and that is the same rule as snmppoll.toFloat's refusal to
//     turn an unusable value into a zero.
//
// # LABELS: `device` and `port`, and nothing else
//
// No `ifindex`, no `iftype`, and their absence is a DECISION rather than a gap.
// gNMI names an interface by its OpenConfig key and offers no interface index;
// looking one up on the side to fill the label would be inventing identity the
// protocol did not carry. internal/metricbuf/names.go states what the absence
// buys: the controller refuses to draw an inventory conclusion from a sample
// with no `iftype`, so these samples feed curves and can never create a port.
// The rule "telemetry never creates a port" therefore holds here by
// construction.

// mustPath parses a constant path or panics.
//
// Panicking is right for exactly this: the argument is a literal in this file,
// so a failure is a typo committed by whoever edited it, and it is caught by any
// test that touches the package rather than by a proxy failing to subscribe at
// two in the morning.
func mustPath(s string) Path {
	var p, err = ParsePath(s)
	if err != nil {
		panic("gnmipoll: bad constant path " + s + ": " + err.Error())
	}
	return p
}

// The subscribed paths. `[name=*]` is the gNMI wildcard: one subscription covers
// every interface, where SNMP walks a table.
var (
	// pathIfState: the state leaf that says a port fell.
	pathIfState = mustPath("/interfaces/interface[name=*]/state/oper-status")
	// pathEthernetState: negotiated speed, on the ethernet branch rather than the
	// generic interface one — OpenConfig puts anything layer-1 there.
	pathEthernetState = mustPath("/interfaces/interface[name=*]/ethernet/state/port-speed")
	// pathCounters: the whole counters container, subscribed as ONE path.
	//
	// Ten separate subscriptions would give the device ten sets of state to keep
	// per port and would arrive as ten notifications to correlate; a container
	// subscription arrives as one notification whose prefix carries the port and
	// whose updates carry the leaves — which is precisely the shape Samples
	// below is written for.
	pathCounters = mustPath("/interfaces/interface[name=*]/state/counters")

	// --- Health, replacing what the SNMP poller reads from enterprise MIBs ---
	//
	// The reason these exist at all: `health` on this platform used to be
	// declared over SNMP, and the connector's own note said CPU and memory were
	// NOT claimed because EOS serves them under HOST-RESOURCES-MIB and its
	// enterprise tree (30065), neither of which internal/snmppoll queries. So the
	// SNMP `health` channel on an Arista delivered sysUpTime and nothing else.
	// OpenConfig models all of it, which is why moving health here is a gain
	// rather than a like-for-like port.

	// pathSystemState: boot time, from which uptime is DERIVED (see leafSample).
	// OpenConfig has no uptime leaf — it publishes the instant the box came up,
	// which is the better datum and the one this has to convert.
	pathSystemState = mustPath("/system/state")
	// pathSystemMemory: physical and reserved memory.
	pathSystemMemory = mustPath("/system/memory/state")
	// pathCPUState: per-CPU utilisation. Keyed by `index`, and the key matters —
	// a multi-core box publishes one instance per core plus an aggregate.
	pathCPUState = mustPath("/components/component[name=*]/cpu/utilization/state")
	// pathComponentState: temperature and the component's own status. OpenConfig
	// hangs these off the component tree rather than off a sensor list of its own.
	pathComponentState = mustPath("/components/component[name=*]/state")
	// pathPSUState: the power-supply subtree.
	//
	// A SEPARATE SUBSCRIPTION, and that is not a style choice — it is a bug that
	// a capture found. `power-supply` is a SIBLING of `state` under the component,
	// not a child of it, so `/components/component[name=*]/state` does not cover
	// it and never did. The declaration looked complete and the subtree was simply
	// never subscribed.
	//
	// OBSERVED EMPTY on the lab 7050SX (EOS 4.28.13.1M): all six leaves —
	// capacity, input/output current, voltage and power — arrive with NO VALUE.
	// The chassis does not populate PSU telemetry through OpenConfig. The path is
	// kept because it costs nothing when unserved and another model may fill it;
	// nothing is invented from its absence.
	pathPSUState = mustPath("/components/component[name=*]/power-supply/state")

	// pathTransceiverChannel: the DDM readings of every optical cage, per
	// physical channel.
	//
	// CAPTURED on the lab 7050SX (EOS 4.28.13.1M, 2026-08-12), which is what
	// settles the two questions the OpenConfig model leaves open:
	//
	//   - the component is named after the INTERFACE plus a literal suffix:
	//     `Ethernet34 transceiver`. That suffix is what [opticsPort] strips, and
	//     stripping it is the entire port attribution — no side lookup through
	//     /interfaces/interface/state/transceiver, which this train was not asked
	//     to serve;
	//   - the leaves that arrive are `input-power/instant`, `output-power/instant`
	//     and `laser-bias-current/instant`, already in dBm and mA. Empty cages
	//     publish NOTHING (seven modules, seven channel sets, on a 64-port box),
	//     and a module receiving no light publishes input-power at exactly -30 —
	//     EOS's floor, kept verbatim: it is the device's own claim, and a fibre
	//     that drops from -4 to -30 is precisely the event a level series exists
	//     to show.
	pathTransceiverChannel = mustPath(
		"/components/component[name=*]/transceiver/physical-channels/channel[index=*]/state")

	// --- LLDP, which produces a TABLE and not samples (see lldp.go) ---

	// pathLLDPNeighbor: the neighbours CONTAINER, per local interface.
	//
	// ONE WILDCARD, AND STOPPING BEFORE `neighbor[id=*]/state` IS THE WHOLE POINT.
	// The subscription is still two keys deep in what it RECEIVES — the local
	// interface's `name` and the neighbour's `id`, both of which arrive in the
	// notification path and both of which [Table.applyNeighbor] keys on, because a
	// port facing a hub or a phone-plus-PC sees several neighbours and folding them
	// onto the interface alone would make the last one arriving overwrite the rest.
	// What changed is only how far down the SUBSCRIBED path the wildcards go.
	//
	// It goes only this far because EOS 4.28.13.1M SILENTLY DROPS ROWS when a
	// second wildcard is asked for, measured on core01.lab (2026-08-13) with
	// four neighbours the device lists in `show lldp neighbors`:
	//
	//	/lldp                                             → 4 neighbours
	//	/lldp/interfaces                                  → 4
	//	/lldp/interfaces/interface[name=*]                → 4
	//	/lldp/interfaces/interface[name=*]/neighbors      → 4
	//	…/neighbors/neighbor[id=*]/state                  → 2   ← what we asked for
	//
	// Two of four, on a FRESH subscription, in both SAMPLE and ON_CHANGE mode, and
	// the same two every time (Ethernet37 and Ethernet39 — Ethernet34 and
	// Ethernet48 never arrive). It is not a leaf-naming problem and not stream
	// drift: the leaves that DO arrive are correctly named and correctly folded,
	// and the dropped rows carry the same leaf set as the kept ones.
	//
	// The cost of the failure is what makes the shorter path worth its extra
	// notifications: a neighbour that never arrives is a link the topology cannot
	// draw, and the far end DOES see us — so the cable reads as one-sided, which is
	// indistinguishable on screen from a genuinely unidirectional LLDP problem.
	// Two of this lab's inter-switch links were invisible for days that way.
	//
	// Subscribing higher costs nothing measurable: the container holds exactly the
	// leaves the old path did, plus a static `neighbor[id=N]/id` that
	// `neighborLeaf` does not record and `suffixAfter(…, "state")` resolves to ""
	// for, so it is refused before it can touch the table. The ticking leaves
	// (`last-update-time`, `registration-time`) were already inside `/state` and
	// were already arriving.
	pathLLDPNeighbor = mustPath(
		"/lldp/interfaces/interface[name=*]/neighbors")
	// pathLLDPLocal: what the device says about ITSELF — chassis id and system
	// name. Needed because an LLDPReport carries the reporter's own identity, and
	// that is what lets the controller match two ends of one cable.
	pathLLDPLocal = mustPath("/lldp/state")
)

// counterLeaves maps a path SUFFIX (element names after the `interface`
// element, joined by "/") to a series name.
//
// Numbers only: a leaf here whose value is not numeric is dropped rather than
// coerced.
var counterLeaves = map[string]string{
	"state/counters/in-octets":  metricbuf.MetricIfInOctets,
	"state/counters/out-octets": metricbuf.MetricIfOutOctets,
	// gNMI says `pkts` where MIB-II says `UcastPkts`. The series keeps the
	// meshcraft name — that is the entire reason internal/metricbuf exists.
	"state/counters/in-pkts":     metricbuf.MetricIfInPackets,
	"state/counters/out-pkts":    metricbuf.MetricIfOutPackets,
	"state/counters/in-errors":   metricbuf.MetricIfInErrors,
	"state/counters/out-errors":  metricbuf.MetricIfOutErrors,
	"state/counters/in-discards": metricbuf.MetricIfInDiscards,
	// The distinction errors/discards is the most useful one in the whole set
	// (cf. metricbuf/names.go): an error blames the cable, a discard blames
	// capacity. Merging them would send an operator hunting for a bad patch lead
	// in front of a saturated link.
	"state/counters/out-discards": metricbuf.MetricIfOutDiscards,
	// carrier-transitions counts link flaps since boot, which is exactly what
	// meshcraft_if_link_downs_total is: the only way to see a port that flapped
	// three times between two samples.
	"state/counters/carrier-transitions": metricbuf.MetricIfLinkDowns,
	// The per-class decomposition (ADR-0028). These four leaves COST NOTHING
	// EXTRA: pathCounters subscribes the whole `state/counters` container, so
	// EOS has been streaming them all along and leafSample has been dropping
	// them on the default branch below. The only change is that they now have
	// names.
	//
	// Verified on core01-arena (EOS 4.28.13M) on 2026-10-25 by dumping every
	// leaf of the container: fifteen arrive, and these four are among them.
	"state/counters/in-multicast-pkts":  metricbuf.MetricIfInMulticastPackets,
	"state/counters/out-multicast-pkts": metricbuf.MetricIfOutMulticastPackets,
	"state/counters/in-broadcast-pkts":  metricbuf.MetricIfInBroadcastPackets,
	"state/counters/out-broadcast-pkts": metricbuf.MetricIfOutBroadcastPackets,
	// `in-unicast-pkts` / `out-unicast-pkts` ARE SERVED AND DELIBERATELY NOT
	// MAPPED, which is the one entry worth justifying by its absence.
	//
	// They are unicast TOTAL — known and flooded confounded. Mapping them as a
	// third class of the stacked graph would put a band under the other two that
	// is mostly ordinary point-to-point traffic, and the one class an operator
	// actually wants there (unknown unicast, the one the storm limiter polices)
	// is NOT derivable from it: the device serves no counter for the known share
	// to subtract. See wire/metrics/names.go and ADR-0028.
}

// operStatusValue maps the OpenConfig enum onto IF-MIB numbering.
//
// The numbering is NOT arbitrary and is not ours: it is ifOperStatus's, because
// meshcraft_if_oper_status is defined as that raw value and an SNMP producer
// already fills the series for the rest of the fleet. See the file header for
// what picking a different numbering would cost.
var operStatusValue = map[string]float64{
	"UP":               1,
	"DOWN":             2,
	"TESTING":          3,
	"UNKNOWN":          4,
	"DORMANT":          5,
	"NOT_PRESENT":      6,
	"LOWER_LAYER_DOWN": 7,
}

// portSpeedMbps maps the OpenConfig speed enum to Mbit/s.
//
// A spelling absent from this map yields NO SAMPLE. That is deliberate: the
// alternative is a port graphed at 0 Mbit/s, which reads as a dead link on a
// port that is in fact running fine at a speed we failed to name.
var portSpeedMbps = map[string]float64{
	"SPEED_10MB":   10,
	"SPEED_100MB":  100,
	"SPEED_1GB":    1000,
	"SPEED_2500MB": 2500,
	"SPEED_5GB":    5000,
	"SPEED_10GB":   10000,
	"SPEED_25GB":   25000,
	"SPEED_40GB":   40000,
	"SPEED_50GB":   50000,
	"SPEED_100GB":  100000,
	"SPEED_200GB":  200000,
	"SPEED_400GB":  400000,
	// SPEED_UNKNOWN is deliberately absent: it is the device saying it does not
	// know, and publishing a number for it would be us claiming to.
}

// enumText strips the YANG module qualifier a device may prefix an identity
// value with.
//
// `openconfig-if-ethernet:SPEED_1GB` and `SPEED_1GB` are the SAME value, and
// which of the two arrives depends on the encoding the target chose — not on
// anything meshcraft controls. A table keyed on the qualified spelling would
// work against one device and silently produce nothing against the next.
func enumText(s string) string {
	var trimmed = strings.TrimSpace(s)
	var i = strings.LastIndex(trimmed, ":")
	if i >= 0 {
		trimmed = trimmed[i+1:]
	}
	return strings.ToUpper(trimmed)
}

// leafSuffix returns the path element names AFTER the `interface` element,
// joined by "/" — the key of the two tables above.
//
// Empty when the path does not describe an interface at all, which is how a
// notification about something else (the system container, a component) is
// dropped without a special case.
func leafSuffix(p Path) string {
	var names = p.Names()
	var i int
	var at = -1
	for i = range names {
		if names[i] == "interface" {
			at = i
		}
	}
	if at < 0 || at+1 >= len(names) {
		return ""
	}
	return strings.Join(names[at+1:], "/")
}

// maxDeviceTSSkew is how far a device's own timestamp may sit from the instant
// the notification arrived and still be believed as a COLLECTION TIME.
//
// Generous on purpose: it is not a clock-accuracy bound, it is the line past
// which a timestamp cannot be describing this sample at all. A switch whose NTP
// drifted by a minute still produces usable curves; one that hands out a
// timestamp four days old is not describing when it read the counter.
const maxDeviceTSSkew = 2 * time.Minute

// usableDeviceTS decides between the device's timestamp and arrival time.
//
// # WHY THIS FUNCTION EXISTS — MEASURED ON core01.lab, 2026-08-11
//
// EOS 4.28.13.1M stamps a STREAM/SAMPLE notification with the instant the LEAF'S
// VALUE LAST CHANGED, not the instant it was sampled. A counter that has not
// moved is still re-published at every sample interval — the wire was captured
// and all 65 interfaces × 8 leaves arrive every 5 s, correctly — but carrying a
// timestamp from whenever it last moved.
//
// Taken literally, as this package used to, that timestamp is written into
// storage: `internal/tsdb.Format` emits it as the sample's millisecond, and
// VictoriaMetrics stores the point exactly there. So every quiet counter on the
// fleet's core landed FOUR DAYS IN THE PAST, over and over, ~200 times an hour
// onto the same instant. The series existed and every query window was empty.
// Ethernet35's `meshcraft_if_in_octets_total` held 7905 written points spread
// over 1259 distinct timestamps, the last of them 2026-08-11T11:49:15.955Z — the
// exact instant its link dropped, and the exact timestamp the proxy was still
// POSTing nine hours later.
//
// What it looked like from the graph, and why it cost so long: the ONLY leaves
// that appeared fresh were the ones whose value was moving — in/out octets and
// packets on the ports that carry traffic — because those get a current
// timestamp for the same reason. It reads exactly like a suppress-redundant
// filter somewhere in the pipeline, and there is none; every stage from
// `Collector.Run` to `handleProxyMetrics` was cleared by reading before the
// notification's own timestamp was suspected.
//
// # WHY ARRIVAL TIME IS NOT THE LOSS THE OLD COMMENT FEARED
//
// The rule this replaces said a device timestamp "survives a queue on the proxy",
// implying arrival time would be the moment of UPLOAD. It is not: `at` is read in
// [Collector.Run] the moment the notification is received, before
// internal/metricbuf ever sees it, so a batch replayed after a tunnel outage
// still lands where it was measured. The device timestamp was buying transit
// latency — milliseconds — and paying for it with this.
//
// The device's own is still preferred when it is plausible, because on a target
// that stamps sample time properly it is the better number. `future` is bounded
// as well as `past`: a device whose clock runs ahead would otherwise write points
// into a future no query reaches, which is the same defect mirrored.
func usableDeviceTS(deviceTS, at time.Time) time.Time {
	// A zero timestamp is the device saying nothing; it must not land as 1970.
	if deviceTS.IsZero() {
		return at
	}
	// `at` zero happens only in a test that passes none. Nothing to compare
	// against, so the device's stands.
	if at.IsZero() {
		return deviceTS
	}
	var gap = at.Sub(deviceTS)
	if gap < 0 {
		gap = -gap
	}
	if gap > maxDeviceTSSkew {
		return at
	}
	return deviceTS
}

// Samples translates one notification into samples.
//
// PURE, and that is where correctness lives — same rule, same sentence, as
// snmppoll.Samples. The port comes from the path's `name` key and from nowhere
// else: no positional guess, no memory of the previous notification, and an
// update whose joined path carries no port is DROPPED rather than attributed to
// the last one seen.
//
// `at` is the instant the notification ARRIVED at the collector, read in
// [Collector.Run] before anything is buffered. It is used when the device gave no
// timestamp of its own, and — see [usableDeviceTS] — whenever the device's own is
// not usable as a collection time.
func Samples(deviceID string, n Notification, at time.Time) []metricbuf.Sample {
	var ts = usableDeviceTS(n.TS, at)
	var out []metricbuf.Sample
	var u Update
	// Expand FIRST: a JSON container update carries many leaves in one, and
	// everything below is written per leaf. See [Expand].
	for _, u = range Expand(n.Update) {
		var full = Join(n.Prefix, u.Path)
		// Two families, tried in order, and a leaf belongs to exactly one. The
		// interface one is first because it is the overwhelming majority of the
		// volume — a 48-port switch produces ten counters per port and about a
		// dozen health leaves in total.
		var s, ok = interfaceSample(deviceID, full, u.Value, ts)
		if ok {
			out = append(out, s)
			continue
		}
		// Optics BEFORE health, and the order is load-bearing: both live under
		// /components, and healthSample keys on the suffix after `component` —
		// a transceiver channel leaf is not in its table so it would merely be
		// dropped, but the day componentLeaves grows an entry that collides,
		// the more specific family must already have claimed its paths.
		s, ok = opticsSample(deviceID, full, u.Value, ts)
		if ok {
			out = append(out, s)
			continue
		}
		s, ok = healthSample(deviceID, full, u.Value, ts)
		if ok {
			out = append(out, s)
		}
	}
	return out
}

// interfaceSample resolves a per-port leaf.
//
// The port comes from the path's `name` key and from nowhere else: no positional
// guess, no memory of the previous notification, and an update whose joined path
// carries no port is refused rather than attributed to the last one seen.
func interfaceSample(deviceID string, full Path, v Value,
	ts time.Time) (metricbuf.Sample, bool) {

	var port = full.Key("interface", "name")
	if port == "" {
		return metricbuf.Sample{}, false
	}
	var name, value, ok = leafSample(leafSuffix(full), v)
	if !ok {
		return metricbuf.Sample{}, false
	}
	return metricbuf.Sample{
		Name: name,
		// device and port ONLY — see the file header for why the absence of
		// ifindex/iftype is a decision.
		Labels: map[string]string{"device": deviceID, "port": port},
		Value:  value,
		TS:     ts,
	}, true
}

// --- Optics: the transceiver channels, labelled by PORT and LANE ---

// opticsSuffix is the transceiver-channel marker inside a /components path.
// Everything the DDM subscription produces carries it; nothing health produces
// does.
const opticsSuffix = "transceiver/physical-channels/channel/state/"

// opticsLeaves maps the leaf under `channel/state/` to a series.
//
// Numbers only, all three CAPTURED on the lab 7050SX (see
// [pathTransceiverChannel]). The units are the OpenConfig model's and match
// what internal/metricbuf promises: power in dBm — the device does the
// logarithm, unlike the milliwatt-scaled SNMP columns — and bias in mA.
// `state/index` also arrives as a number and is deliberately absent: it is the
// lane's identity, already carried by the label, and publishing it as a curve
// would graph a constant.
var opticsLeaves = map[string]string{
	"input-power/instant":        metricbuf.MetricSFPRxPowerDBM,
	"output-power/instant":       metricbuf.MetricSFPTxPowerDBM,
	"laser-bias-current/instant": metricbuf.MetricSFPTxBiasMilliamps,
}

// opticsPort turns a transceiver COMPONENT name into the interface name its
// cage belongs to.
//
// CAPTURED, not modelled: EOS names the component `Ethernet34 transceiver` —
// the interface name plus that literal suffix. The suffix is stripped; a
// component named any other way is kept verbatim, because the OpenConfig model
// does not fix the spelling and the downstream facade resolution
// (internal/api, facade.resolve with the connector's PortNamer) is the place
// that decides whether a label names a faceplate port. A spelling it cannot
// resolve feeds no screen and creates nothing — the same rule as every other
// label in this file.
func opticsPort(component string) string {
	var trimmed = strings.TrimSuffix(component, " transceiver")
	return strings.TrimSpace(trimmed)
}

// opticsSample resolves one transceiver-channel leaf.
//
// The labels are `device`, `port` and `lane` — lane being the channel index,
// "0" on every SFP/SFP+ and 0-3 on a four-lane QSFP. Always present, including
// on single-lane modules: a label that appears only when a second lane shows up
// would split one port's history into two series the day a module is swapped.
// (The SNMP and RouterOS producers of these families carry no lane — those
// channels serve single-lane readings only, and a device is produced by exactly
// one collector, so the label sets never mix within one device.)
func opticsSample(deviceID string, full Path, v Value,
	ts time.Time) (metricbuf.Sample, bool) {

	var names = full.Names()
	if len(names) == 0 || names[0] != "components" {
		return metricbuf.Sample{}, false
	}
	var suffix = suffixAfter(names, "component")
	if !strings.HasPrefix(suffix, opticsSuffix) {
		return metricbuf.Sample{}, false
	}
	var series, known = opticsLeaves[strings.TrimPrefix(suffix, opticsSuffix)]
	if !known || v.Kind != ValueNumber {
		return metricbuf.Sample{}, false
	}
	var comp = full.Key("component", "name")
	var lane = full.Key("channel", "index")
	if comp == "" || lane == "" {
		// A reading nobody can attribute to a cage is not a reading. Same
		// refusal as an unlabelled temperature.
		return metricbuf.Sample{}, false
	}
	return metricbuf.Sample{
		Name: series,
		Labels: map[string]string{
			"device": deviceID, "port": opticsPort(comp), "lane": lane,
		},
		Value: v.Num,
		TS:    ts,
	}, true
}

// Expand turns container updates carrying a JSON subtree into per-leaf updates.
//
// # WHY THIS EXISTS AT ALL
//
// This package subscribes containers, not leaves, because one container is one
// notification instead of ten. Under PROTO encoding the device still decomposes
// it — one update per leaf, each carrying its own path — and that is the shape
// everything downstream assumes. Under JSON or JSON_IETF it does not: the whole
// subtree arrives as ONE update whose path stops at the container.
//
// transport.go asks for PROTO explicitly, so in principle this never fires. It is
// here because "the target answered in the encoding we asked for" is an
// assumption about someone else's firmware, and the cost of it being wrong was
// total and silent: `leafSuffix` would return "state/counters", which is in no
// table, and every counter would be dropped with a healthy subscription behind
// it. A fallback that costs one type switch is worth not betting a fleet's graphs
// on an EOS train's encoding negotiation.
//
// A document that does not parse is DROPPED, not partially applied: half a
// counters container is not a smaller truth, it is a set of samples with no way
// to tell which ones are missing.
func Expand(ups []Update) []Update {
	var out = make([]Update, 0, len(ups))
	var u Update
	for _, u = range ups {
		if u.Value.Kind != ValueJSON {
			out = append(out, u)
			continue
		}
		var doc any
		if json.Unmarshal(u.Value.JSON, &doc) != nil {
			continue
		}
		out = jsonLeaves(out, u.Path, doc)
	}
	return out
}

// jsonLeaves walks a decoded JSON document, appending one update per scalar.
//
// Keys are visited in SORTED order. Go randomises map iteration, and without the
// sort the same notification would produce the same samples in a different order
// every time — which does not change what is stored but makes a transcript
// impossible to compare against another run, and that is the only way anybody
// debugs this layer.
func jsonLeaves(out []Update, base Path, v any) []Update {
	switch t := v.(type) {
	case map[string]any:
		var keys = make([]string, 0, len(t))
		var k string
		for k = range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k = range keys {
			// The YANG module qualifier a JSON_IETF document prefixes its keys
			// with (`openconfig-interfaces:state`) is stripped: the leaf tables are
			// keyed on the bare element name, exactly as a PROTO path spells it.
			// Keeping the qualifier here would make this fallback match nothing,
			// i.e. would leave the bug it exists to fix in place.
			out = jsonLeaves(out, Join(base, Path{{Name: unqualify(k)}}), t[k])
		}
	case float64:
		out = append(out, Update{Path: base, Value: NumberValue(t)})
	case bool:
		var n float64
		if t {
			n = 1
		}
		out = append(out, Update{Path: base, Value: NumberValue(n)})
	case string:
		// RFC 7951 (JSON_IETF) encodes int64 and uint64 AS STRINGS, because JSON
		// numbers cannot hold them exactly. So a counter arrives quoted, and
		// treating every string as text would drop every counter on a JSON_IETF
		// target — the same failure, one layer down. Parsed first, kept as text
		// when it does not parse, which is what leaves the enums (`UP`,
		// `SPEED_1GB`) intact.
		var f, err = strconv.ParseFloat(t, 64)
		if err == nil {
			out = append(out, Update{Path: base, Value: NumberValue(f)})
			break
		}
		out = append(out, Update{Path: base, Value: StringValue(t)})
	}
	// Anything else — a nested list, a null — is dropped. A null is the device
	// saying it has no value, and inventing a zero for it is the one thing this
	// package never does.
	return out
}

// unqualify strips a YANG module prefix from a JSON key.
func unqualify(k string) string {
	var i = strings.IndexByte(k, ':')
	if i >= 0 {
		return k[i+1:]
	}
	return k
}

// leafSample resolves one leaf into a series name and a value, and says whether
// there is one at all.
//
// The `ok=false` cases are the interesting ones and they all mean the same
// thing: WE DO NOT KNOW, so we publish nothing. An unknown leaf, an enum
// spelling absent from the tables, a counter that arrived as a string — none of
// them becomes a zero. That rule is snmppoll.toFloat's, restated here because
// this package will grow leaves and the pressure to "just default it" arrives
// with them.
func leafSample(suffix string, v Value) (string, float64, bool) {
	switch suffix {
	case "state/oper-status":
		if v.Kind != ValueString {
			return "", 0, false
		}
		var n, known = operStatusValue[enumText(v.Str)]
		if !known {
			return "", 0, false
		}
		return metricbuf.MetricIfOperStatus, n, true
	case "ethernet/state/port-speed", "ethernet/state/negotiated-port-speed":
		// Both spellings accepted, and the second is the reason: newer
		// OpenConfig models moved the negotiated value to its own leaf, and
		// which one a train serves is unverified here. Accepting both costs a
		// map entry; accepting only one risks an empty series with nothing to
		// explain it.
		if v.Kind != ValueString {
			return "", 0, false
		}
		var mbps, known = portSpeedMbps[enumText(v.Str)]
		if !known {
			return "", 0, false
		}
		return metricbuf.MetricIfSpeedMbps, mbps, true
	}
	var name, known = counterLeaves[suffix]
	if !known || v.Kind != ValueNumber {
		return "", 0, false
	}
	return name, v.Num, true
}

// --- Health: the device, not its ports ---
//
// # What moving health off SNMP actually buys, stated because it is not obvious
//
// This is NOT a like-for-like port. The Arista connector's SNMP `health` channel
// declared sysUpTime and said so in its own note: CPU and memory were explicitly
// NOT claimed, because EOS serves them under HOST-RESOURCES-MIB and its
// enterprise tree (30065) and internal/snmppoll queries neither. So `health` over
// SNMP on an Arista was one number. OpenConfig models the whole set, and these
// paths are why the migration gains something rather than merely relocating it.
//
// # NOTHING HERE HAS BEEN CAPTURED EITHER
//
// Same standing as the interface table above, and the same consequence: a leaf
// the device does not serve simply never arrives, so the table is written wide.
// The one entry that is a CONVERSION rather than a lookup — boot-time into uptime
// — is the one to distrust first, because it is the only one that can be wrong
// while still producing a plausible number.

// componentLeaves maps a suffix under `/components/component[name=X]/` to a
// series. Every one of them is labelled by `sensor`, carrying the component name.
//
// One series for all sensors rather than one per sensor, which is
// internal/metricbuf/names.go's rule and not a choice made here: a board has one,
// two or ten thermal probes depending on the model, and a series name per probe
// makes a fleet-wide alert impossible to write.
var componentLeaves = map[string]string{
	"state/temperature/instant": metricbuf.MetricTemperature,
	// OpenConfig gives a PSU an output voltage under its own subtree; the generic
	// component branch carries none, which is why this is the power-supply
	// spelling and not a `state/` one. Observed EMPTY on the lab 7050SX — see
	// [pathPSUState] — so this maps a leaf that currently never arrives.
	"power-supply/state/output-voltage": metricbuf.MetricVoltage,
	"cpu/utilization/state/instant":     metricbuf.MetricCPULoad,

	// FAN SPEED IS DELIBERATELY ABSENT, and this is the one entry worth arguing.
	//
	// The obvious mapping is `fan/state/speed` -> meshcraft_fan_rpm, and it was
	// written that way. A capture killed it: on the lab 7050SX that leaf reads
	// 34. A switch fan does not turn at 34 revolutions per minute — these run in
	// the thousands — so the value is a PERCENTAGE of maximum, which is what
	// Arista publishes there whatever the OpenConfig model calls the leaf.
	//
	// Mapping it anyway would have put "34 RPM" on a dashboard next to a fan
	// spinning perfectly normally, and the series is named `_rpm`: nothing
	// downstream could have caught the unit. That is precisely the credible false
	// graph this file's header exists to prevent, so the leaf is dropped until
	// metricbuf has a percentage series to put it in — an absent curve being the
	// honest answer, and a wrong unit not being one.
}

// systemLeaves maps a suffix under `/system/` to a series.
//
// `memory/state/physical` is the box's TOTAL. FREE MEMORY IS NEVER DERIVED:
// OpenConfig's system-memory container publishes `physical` and `reserved`, and
// reserved is not free. Computing one from the other would publish a number the
// device never said — metricbuf defines free and total as two independent
// readings precisely so that an occupancy ratio is never mistaken for a
// measurement.
//
// The `memory/state/free` entry below is NOT that derivation and does not
// contradict it: it maps a leaf a device would have to publish ITSELF, verbatim.
// The lab 7050SX does not — `/system/memory/state` came back carrying `physical`
// and `reserved` only — so on this chassis the entry never fires and
// meshcraft_memory_free_bytes stays empty, which is the honest answer rather than
// a computed one. It is kept for the device that serves the leaf, and it is the
// only way this table would ever publish free memory.
//
// (This comment used to claim there was "deliberately no entry for free memory"
// while the entry sat two lines under it. The reasoning was right and the
// sentence describing the code was false.)
var systemLeaves = map[string]string{
	"memory/state/physical": metricbuf.MetricMemoryTotal,
	"memory/state/free":     metricbuf.MetricMemoryFree,
}

// healthSample resolves a device-level leaf into a sample.
//
// Split from [interfaceSample] rather than folded into one table because the two
// carry DIFFERENT LABELS — a port name on one side, a sensor name on the other —
// and a single table would have had to encode which, per entry. That is the kind
// of side condition that gets forgotten when a leaf is added.
func healthSample(deviceID string, full Path, v Value,
	ts time.Time) (metricbuf.Sample, bool) {

	var names = full.Names()
	if len(names) == 0 {
		return metricbuf.Sample{}, false
	}

	// A component: the sensor name is the list key, and a leaf without one is
	// refused rather than published unlabelled. An unlabelled temperature in a
	// series where every other point carries a sensor is not a reading, it is a
	// point nobody can attribute.
	if names[0] == "components" {
		var comp = full.Key("component", "name")
		if comp == "" {
			return metricbuf.Sample{}, false
		}
		var suffix = suffixAfter(names, "component")
		var series, known = componentLeaves[suffix]
		if !known || v.Kind != ValueNumber {
			return metricbuf.Sample{}, false
		}
		return metricbuf.Sample{
			Name:   series,
			Labels: map[string]string{"device": deviceID, "sensor": comp},
			Value:  v.Num,
			TS:     ts,
		}, true
	}

	if names[0] != "system" {
		return metricbuf.Sample{}, false
	}
	var suffix = strings.Join(names[1:], "/")

	// UPTIME IS DERIVED, and this is the one conversion in the file.
	//
	// OpenConfig has no uptime leaf. It publishes `boot-time`: the instant the
	// system came up, in NANOSECONDS since the epoch — the same unit as a gNMI
	// timestamp, which is why it is not seconds. meshcraft_uptime_seconds is a
	// duration, and its useful signal is its BREAK (a value that drops is a
	// reboot), so the subtraction has to happen somewhere. Here is the only place
	// that holds both the boot instant and the sample's own timestamp.
	if suffix == "state/boot-time" {
		if v.Kind != ValueNumber || v.Num <= 0 {
			return metricbuf.Sample{}, false
		}
		var up = ts.Sub(time.Unix(0, int64(v.Num)))
		if up < 0 {
			// A boot time in the FUTURE means the device's clock and ours
			// disagree, which happens on a switch that has not reached NTP yet.
			// Publishing a negative uptime would put a downward spike in the one
			// series whose downward spikes MEAN "this box rebooted" — the alert
			// would fire on a clock skew.
			return metricbuf.Sample{}, false
		}
		return metricbuf.Sample{
			Name:   metricbuf.MetricUptime,
			Labels: map[string]string{"device": deviceID},
			Value:  up.Seconds(),
			TS:     ts,
		}, true
	}

	var series, known = systemLeaves[suffix]
	if !known || v.Kind != ValueNumber {
		return metricbuf.Sample{}, false
	}
	return metricbuf.Sample{
		Name:   series,
		Labels: map[string]string{"device": deviceID},
		Value:  v.Num,
		TS:     ts,
	}, true
}

// suffixAfter joins the element names following the LAST occurrence of `elem`.
//
// Last and not first, for the same reason [Path.Key] reads the last keyed
// element: a path may legitimately repeat a container name, and the innermost is
// the one describing the thing being reported.
func suffixAfter(names []string, elem string) string {
	var at = -1
	var i int
	for i = range names {
		if names[i] == elem {
			at = i
		}
	}
	if at < 0 || at+1 >= len(names) {
		return ""
	}
	return strings.Join(names[at+1:], "/")
}
