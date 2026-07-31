package tunnel

import "time"

// Two long-lived channels add to `dial` streams, one per direction. They carry the
// usual JSON preamble, then a series of JSON lines.
//
//	controller -> proxy : {"op":"watch"}   then WatchConfig
//	proxy -> controller : {"op":"events"}  then Event
//
// Role split follows docs/01-Architecture/Proxy: the proxy is the local executor.
// It pings its own devices and sweeps its prefixes; the WAN carries only CHANGES.
// Having the controller ping through the tunnel would cost one round trip per second
// per device for the same information.
const (
	OpDial   = "dial"
	OpWatch  = "watch"
	OpEvents = "events"
	// OpAsk: a one-off QUESTION from controller to proxy, one per stream, with its
	// answer on the same stream. This is the third exchange pattern, and it exists
	// for a precise reason: some facts are knowable ONLY from the proxy, and neither
	// long-lived channel can request them.
	//
	// `watch` goes the right way but returns nothing -- it is an instruction, not a
	// query. `dial` yields a TCP stream, useless for UDP (SNMP) or reading the
	// proxy routing table. And the events channel goes the other way.
	//
	// Deliberately ONE-OFF and stateless: one question, one answer, stream closes. An
	// extra permanent channel would need request/response correlation for questions
	// asked manually a few times per day.
	OpAsk = "ask"
)

// Questions the controller can ask a proxy (Ask field of AskRequest).
const (
	// AskSNMPWalk: walk an OID on a device and return what it answers.
	//
	// Only the proxy can do this: SNMP is UDP, and the tunnel relays TCP only -- there
	// is nothing to relay. Without this question, discovering OIDs actually served
	// by a device (SFP optical sensors, for example) would mean GUESSING from a
	// vendor MIB when the device could say them.
	//
	// READ-ONLY by design: there is no write question here, and there never will be
	// -- configuration goes through connectors with their guardrails (plan, dry-run,
	// verification read-back).
	AskSNMPWalk = "snmp_walk"
	// AskSourceAddr: which local address do you use to reach this IP?
	//
	// This question makes trap destination setup possible, and it is less obvious than
	// it looks. One might think it is enough to pick, among addresses the proxy
	// declares (`proxies.ifaces`), the one on the device subnet. That fails when the
	// proxy reaches the device by ROUTING rather than a direct link: none of its
	// addresses is then on the right subnet, and the obvious algorithm returns
	// nothing at all.
	//
	// Only the proxy kernel can answer, because only it has the routing table. The
	// answer is therefore the source address it WOULD use -- not a controller deduction.
	AskSourceAddr = "source_addr"
)

// AskRequest is the question, written on the stream right after preamble {"op":"ask"}.
type AskRequest struct {
	Ask string `json:"ask"`
	// IP: the device in question. An ADDRESS not an identifier: the proxy does not
	// know controller identifiers, and teaching it to resolve them would give it a
	// fleet view it does not need.
	IP string `json:"ip"`
	// Community / Port: for AskSNMPWalk. Secret is passed PER OPERATION, never the
	// vault -- same rule as connectors (docs/01-Architecture/Connecteurs, contract shape).
	Community string `json:"community,omitempty"`
	Port      int    `json:"port,omitempty"`
	// OID: root to walk.
	OID string `json:"oid,omitempty"`
	// MaxRows bounds the response. A walk under `1.3.6.1` on a switch yields tens of
	// thousands of lines: without a bound, a diagnostic question would fill proxy
	// memory and the response body. 0 = DefaultAskRows.
	MaxRows   int `json:"max_rows,omitempty"`
	TimeoutMS int `json:"timeout_ms,omitempty"`
}

// AskRow is one value from a walk. Value is TEXT and type is given alongside: a 64-bit
// counter does not fit in a float without loss, and counters are what we query.
type AskRow struct {
	OID   string `json:"oid"`
	Type  string `json:"type"`
	Value string `json:"value"`
}

// AskResponse is the answer, a single JSON line.
type AskResponse struct {
	// Err: failure stated PLAINLY. A proxy that cannot answer must say so -- an empty
	// response and a mute device are not fixed the same way.
	Err string `json:"err,omitempty"`
	// Rows: for AskSNMPWalk.
	Rows []AskRow `json:"rows,omitempty"`
	// Truncated: MaxRows bound cut the answer. Without this flag, a truncated response
	// would pass for a complete table, and we would conclude a device lacks OIDs we sought.
	Truncated bool `json:"truncated,omitempty"`
	// SourceAddr: for AskSourceAddr, address without mask.
	SourceAddr string `json:"source_addr,omitempty"`
}

// Bounds for one-off questions.
const (
	DefaultAskRows = 500
	// MaxAskRows: HARD ceiling the controller cannot exceed by asking. A bound the
	// caller can raise is not a bound.
	MaxAskRows     = 5000
	DefaultAskWait = 15 * time.Second
	MaxAskWait     = 60 * time.Second
)

// WatchConfig is what the controller asks the proxy to watch. Sent at tunnel setup,
// then on every change -- it fully replaces the previous one, avoiding state drift.
type WatchConfig struct {
	// Targets: devices to watch, with controller-side identity.
	Targets []WatchTarget `json:"targets"`
	// Prefixes: local ranges to sweep continuously.
	Prefixes []string `json:"prefixes"`
	// PingInterval: watch cadence (default 1 s).
	PingIntervalMS int `json:"ping_interval_ms,omitempty"`
	// SweepInterval: period between full sweeps of a range.
	SweepIntervalS int `json:"sweep_interval_s,omitempty"`
	// DownAfter: consecutive lost pings before declaring a device unreachable. Without
	// debounce, one lost packet would make the whole fleet blink in the log.
	DownAfter int `json:"down_after,omitempty"`
	// SNMP: devices to poll and at what cadence.
	//
	// The CONTROLLER decides list and cadences -- it knows connector capabilities -- and
	// the PROXY executes. A walk crossing the WAN in UDP would cost one round trip per
	// OID and fail on the first link incident.
	SNMP []SNMPWatch `json:"snmp,omitempty"`
	// TrapListen: should this proxy listen for SNMP traps?
	//
	// Decided by the controller like the rest of watch: one source of truth, and a
	// channel that can be turned off fleet-wide without redeploying a binary. A proxy
	// that does not listen is not degraded -- it loses REACTIVITY, not information
	// (cf. EventPortUp).
	TrapListen bool `json:"trap_listen,omitempty"`
	// TrapPort: trap listen port. 0 = DefaultTrapPort.
	TrapPort int `json:"trap_port,omitempty"`
}

// SNMPWatch: one device to poll via SNMP, with cadences.
//
// Intervals are IN SECONDS and already bounded by connector floor (cf.
// driver.MonitoringCapabilities): the proxy applies, it does not arbitrate. Letting
// the proxy decide to speed up would scatter a rule that protects devices across the
// fleet.
type SNMPWatch struct {
	DeviceID string `json:"device_id"`
	IP       string `json:"ip"`
	// Community: read-only v2c. v3 will come with service identity.
	Community string `json:"community,omitempty"`
	Port      int    `json:"port,omitempty"`
	// StateIntervalS: `if_state` cadence, what must stay fresh.
	StateIntervalS int `json:"state_interval_s,omitempty"`
	// CountersIntervalS: `if_counters` cadence, bulky and slow.
	CountersIntervalS int `json:"counters_interval_s,omitempty"`
	// HealthIntervalS: `health` cadence -- temperature, voltages, fans, PSUs, CPU
	// load, memory, uptime.
	//
	// ZERO MEANS DO NOT COLLECT, and that differs from the two cadences above, which
	// are always set. These sensors are not universal: their base is proprietary
	// (MIKROTIK-MIB for MikroTik) and there is no standardized switch health MIB.
	// Querying sixteen branches on a device whose connector declares no `health`
	// channel would only yield "no such object" traffic for nothing -- and on a fleet,
	// useless traffic every cycle.
	//
	// So the CONTROLLER decides to enable it from connector declarations (cf.
	// `optionalIntervalFor`), and the proxy executes. A proxy choosing on its own would
	// scatter a fleet protection rule -- and it knows no connector.
	HealthIntervalS int `json:"health_interval_s,omitempty"`
	// OpticalIntervalS: `optical_power` cadence -- transceiver diagnostics. Same rule:
	// 0 = do not collect.
	OpticalIntervalS int `json:"optical_interval_s,omitempty"`
	// LLDPIntervalS: LLDP neighbor cadence. Same rule as above, 0 = do not collect, and
	// for an even stronger reason: LLDP-MIB is optional and an entire reference fleet
	// platform does NOT serve it at all (SwOS, walk of `1.0.8802` empty -- cf. swos
	// connector, which declares it in its survey). Querying there only yields silence,
	// every cycle, forever.
	//
	// What this cadence does NOT carry must be said here: it does not push samples.
	// Neighbor data goes its own path (`POST /api/proxy/lldp`), because a table
	// snapshot does not fit a labeled float buffer -- cf. header of internal/snmppoll/lldp.go.
	LLDPIntervalS int `json:"lldp_interval_s,omitempty"`
	// MaxIfIndex bounds retained interfaces to REAL front-panel ports.
	//
	// `ifTable` is not only physical ports: a switch exposes aggregates, stack port,
	// VLAN interfaces -- on a 1930, indexes at 8000, 9000 and 100000. Collecting them
	// would produce series no screen can show and skew any "active port count". The
	// controller knows the facade (port_groups), the proxy does not: it bounds. 0 = no
	// bound, take all.
	MaxIfIndex int `json:"max_ifindex,omitempty"`
}

// WatchTarget: one device to watch.
type WatchTarget struct {
	DeviceID string `json:"device_id"`
	IP       string `json:"ip"`
	Name     string `json:"name,omitempty"`
}

// Event types pushed upstream by the proxy.
const (
	// EventDeviceUp / EventDeviceDown: state TRANSITIONS, never current state every second.
	EventDeviceUp   = "device.up"
	EventDeviceDown = "device.down"
	// EventSNMPUp / EventSNMPDown: SNMP COLLECTION transitions, never every poll.
	// Distinct from EventDeviceUp/Down (ICMP): a muted agent with a live ping is
	// exactly the case these exist to surface.
	EventSNMPUp   = "device.snmp_up"
	EventSNMPDown = "device.snmp_down"
	// EventHostSeen: an address responded in a swept range, and the controller did not
	// know it yet.
	EventHostSeen = "host.seen"
	// EventSweepDone: end of a sweep, with coverage.
	EventSweepDone = "sweep.done"
	// EventPortUp / EventPortDown: front-panel port TRANSITIONS from SNMP-polled
	// `ifOperStatus`.
	//
	// They ALWAYS come from poll, never trap content. A v2c trap is unauthenticated UDP:
	// forgeable, lossy, reorderable. Making it a state source would let anyone on the
	// path turn a port off on screen. The trap only ADVANCES the poll that would happen
	// anyway (Trigger field): see a port fall in one second instead of thirty, without a
	// second truth path to arbitrate.
	//
	// Operational corollary: a broken trap channel does not degrade correctness, only
	// latency -- we fall back to polling cadence.
	EventPortUp   = "port.up"
	EventPortDown = "port.down"
	// EventTrapSeen: a trap from this device was just ACCEPTED by this proxy, for the
	// first time.
	//
	// This is a PROOF event, because setting a trap destination in a device is otherwise
	// unverifiable. The controller writes an address -- the one the proxy says it uses to
	// reach the device -- betting the return path works. Nothing guarantees that: a
	// firewall, asymmetric routing, management VLAN ACL, and the device emits into the
	// void. Setup `statusCode 0` says nothing about any of that.
	//
	// A received trap, however, is not debatable: it got through.
	//
	// # WHAT IT PROVES, EXACTLY -- the first draft claimed too much
	//
	// It said turning "we wrote an address" into "return path is established". That is
	// more than the collector verifies: `classifyTrap` (cmd/proxy/trap.go) matches datagram
	// SOURCE IP and community to the watch list, nothing more. A device sending to ANOTHER
	// reachable recipient on the same host -- a leftover line from a previous collector,
	// second monitoring software -- therefore produced "proof" of the address meshcraft
	// just wrote, unrelated to it. The fact was "this device's traps reach this proxy";
	// ATTRIBUTION to the written address was a tacit bet.
	//
	// Hence DestAddr: the proxy says which of ITS addresses the datagram targeted, and
	// only the controller -- holder of the claim -- compares it to what it wrote. When
	// they match, "return path is established" is finally true in the strong sense. When
	// the proxy could not say (older binary, kernel without IP_PKTINFO), proof stays good
	// in the weak sense and the controller SAYS so instead of asserting.
	//
	// Emitted ONCE per device per proxy, not every trap: a chatty fleet would fill the
	// log with information that no longer changes once acquired. It re-arms when the
	// device leaves then re-enters the watch list -- i.e. when something may have changed.
	EventTrapSeen = "trap.seen"
)

// Event is what the proxy pushes to the controller.
type Event struct {
	Type string    `json:"type"`
	TS   time.Time `json:"ts"`
	// DeviceID / IP: target concerned.
	DeviceID string `json:"device_id,omitempty"`
	IP       string `json:"ip,omitempty"`
	// LatencyMS: last successful ping latency.
	LatencyMS int `json:"latency_ms,omitempty"`
	// Misses: consecutive lost pings at the moment of switching to `down`.
	Misses int `json:"misses,omitempty"`
	// Resync: this event is a restated STATUS at stream open, not a transition that just
	// happened.
	//
	// It exists because the proxy only pushes transitions, and a lost transition was lost
	// forever. Three mechanisms combined: `emit` drops when its queue is full, `SendEvents`
	// removes the event from the queue BEFORE writing -- so loses it if the tunnel drops
	// during write -- and reconnect announced nothing, the monitor remembering it already
	// said it. A device whose sole transition vanished stayed `unknown` indefinitely WHILE
	// RESPONDING: the healthy lasting state was the only one nobody ever confirmed.
	//
	// What the controller must do is precise, and that is the point of the field: write
	// status like a transition, but do NOT log it as something that just happened. An
	// "unreachable after 3 misses" re-announced every reconnect would fill the log with
	// transitions that did not occur -- nine lines per proxy restart on the demo fleet,
	// which is what we saw before. The date of a missed transition is unknowable; what is
	// knowable is that the controller missed it.
	Resync bool `json:"resync,omitempty"`
	// Prefix / Scanned / Found: for sweep events.
	Prefix  string `json:"prefix,omitempty"`
	Scanned int    `json:"scanned,omitempty"`
	Found   int    `json:"found,omitempty"`
	// IfIndex / PortName: port concerned, for port events.
	//
	// Index IDENTIFIES, name ILLUSTRATES. `ifDescr` collides -- on a 1930, VLAN 42
	// interface is named "42", like physical port 42 (cf. internal/snmppoll.physical) --
	// so nothing correlates by name alone.
	IfIndex  int    `json:"ifindex,omitempty"`
	PortName string `json:"port_name,omitempty"`
	// Trigger: what made us SEE this transition now -- "poll" for ordinary cycle, "trap"
	// for trap-advanced poll.
	//
	// This field does not say where truth comes from (always poll): it says what saved
	// waiting. It is the only way to see in operations that the trap channel actually
	// helps -- without it, a fleet where no device sends traps is indistinguishable from a
	// reactive one.
	Trigger string `json:"trigger,omitempty"`
	// DestAddr: LOCAL address the datagram targeted, as the proxy kernel reports
	// (IPV6_PKTINFO / IP_PKTINFO). For EventTrapSeen, and only that.
	//
	// This makes return-path proof ATTRIBUTABLE. A wildcard socket accepts everything on
	// 162/udp regardless of which local address the device aimed at -- and the identity
	// filter, source-only, cannot know. Without this field, meshcraft conflated "a trap
	// arrived here" and "the address I wrote receives this device's traps".
	//
	// EMPTY = proxy could not say, and reads as such: "I don't know", never confirmation.
	// The proxy must NOT fill it with the address it thinks it uses to reach the device,
	// which would make verification tautological -- precisely the hypothesis to test.
	DestAddr string `json:"dest_addr,omitempty"`
	// Detail carries failure text or precision.
	Detail string `json:"detail,omitempty"`
}

// Watch defaults.
const (
	DefaultPingInterval  = time.Second
	DefaultDownAfter     = 3
	DefaultSweepInterval = 10 * time.Minute
	// DefaultTrapPort: standard SNMP trap port (RFC 3411).
	DefaultTrapPort = 162
	// TrapCoalesce: grouping window for traps from the SAME device before confirm poll.
	//
	// A 48-port reboot emits a hundred traps in milliseconds. One poll per trap would
	// turn the fast channel into an amplifier on the already struggling device -- and this
	// class holds five sessions. So burst becomes ONE poll: `ifTable` is read whole anyway,
	// one port or fifty costs the same.
	TrapCoalesce = 250 * time.Millisecond
	// TrapMinPollGap: floor between trap-triggered polls on one device. Rule "over-watched
	// device is a harmed device" applies to the fast path too: a flapping port must not
	// spawn one walk per flap.
	TrapMinPollGap = 2 * time.Second
)
