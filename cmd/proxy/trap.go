package main

import (
	"context"
	"log/slog"
	"net"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gosnmp/gosnmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"

	"meshcraft/internal/metricbuf"
	"meshcraft/internal/snmppoll"
	"meshcraft/internal/tunnel"
)

// SNMP trap reception, and port-transition detection.
//
// Two ideas govern this file, and they hold each other up.
//
//  1. A trap never carries the truth, only URGENCY. A v2c trap is
//     unauthenticated UDP: forgeable, losable, reorderable. Nothing of its
//     content is written into state — it only BRINGS FORWARD the `if_state`
//     poll that would have happened anyway (see tunnel.EventPortUp).
//  2. Transitions are therefore computed on EVERY `if_state` poll, whatever
//     triggered it, by diffing against the last observation. Operational
//     corollary: a trap channel that is cut, saturated or absent only degrades
//     LATENCY — we fall back to the polling cadence — and never correctness.
//
// What would be expensive is mixing the two: two sources of state for the same
// port, which would then have to be arbitrated at every disagreement.

// Triggers carried by tunnel.Event.Trigger. They do not say where the truth
// comes from (always the poll), but what saved the wait.
const (
	triggerPoll = "poll"
	triggerTrap = "trap"
)

// metricIfOperStatus copies the metric name emitted by snmppoll.Samples.
//
// There that name is an unexported constant, and opening it up does not belong
// in this batch. The duplication is therefore accepted, but it is CHECKED: a
// test runs an `ifOperStatus` table through snmppoll.Samples and checks that
// observedPorts finds it. A rename over there breaks a test here, not
// production in silence.
const metricIfOperStatus = "meshcraft_if_oper_status"

// `ifOperStatus` values (MIB-II, RFC 2863) that concern us.
//
// "up" is reserved for value 1, and for it alone. `lowerLayerDown(7)` is a port
// that passes no traffic: classifying it anywhere other than "down" would force
// the UI to invent a third state to say the same thing. The intermediate
// values — testing(3), unknown(4), dormant(5), notPresent(6) — are treated as
// "down" and the raw value is carried in the Detail: we do not claim a port is
// in service when the device does not claim it.
//
// The feared noise (a port oscillating 2 ↔ 7 emitting two events per flap) does
// not exist here, because the diff is on the BOOLEAN STATE and not on the
// value: 2 → 7 produces no event.
const (
	ifOperUp             = 1
	ifOperDown           = 2
	ifOperLowerLayerDown = 7
)

// Standard trap OIDs. Only two are recognised, deliberately: the poll tells the
// truth, the trap only wakes it up. A catalogue of vendor traps would be work
// to redo for every product line, for information we re-read from `ifTable`
// the very next millisecond anyway.
const (
	oidSnmpTrapOID = "1.3.6.1.6.3.1.1.4.1.0" // snmpTrapOID.0
	oidLinkDown    = "1.3.6.1.6.3.1.1.5.3"
	oidLinkUp      = "1.3.6.1.6.3.1.1.5.4"
)

// trapBindRetry: delay before retrying a bind that failed. Port 162 may free up
// (another collector being uninstalled), but retrying every second would not
// free it any faster.
const trapBindRetry = time.Minute

// trapSummaryEvery: cadence of the DEBUG reception summary. Without it, a fleet
// where no device is configured to send its traps is indistinguishable from a
// responsive one.
const trapSummaryEvery = 5 * time.Minute

// --- Transition detection: pure logic ---

// portObs is what a poll saw of a port: its operational state and its name.
//
// The name ILLUSTRATES, the index IDENTIFIES (see tunnel.Event.IfIndex): the
// name is not part of the comparison, otherwise renaming a port would fabricate
// a transition.
type portObs struct {
	status int
	name   string
}

// portUp translates `ifOperStatus` into "this port passes traffic".
func portUp(status int) bool { return status == ifOperUp }

// operStatusLabel names an `ifOperStatus` value. Useful only for the Detail and
// the log: an operator reads "dormant" much faster than "5".
func operStatusLabel(status int) string {
	switch status {
	case ifOperUp:
		return "up"
	case ifOperDown:
		return "down"
	case 3:
		return "testing"
	case 4:
		return "unknown"
	case 5:
		return "dormant"
	case 6:
		return "notPresent"
	case ifOperLowerLayerDown:
		return "lowerLayerDown"
	}
	return "unknown"
}

// observedPorts extracts each port's state from a batch of samples.
//
// We start from the samples ALREADY in hand rather than redoing a walk: the
// table has been read once, re-reading it to draw another conclusion would
// double the cost on the device and open the door to two diverging readings of
// the same instant.
func observedPorts(samples []metricbuf.Sample) map[int]portObs {
	var out = map[int]portObs{}
	var s metricbuf.Sample
	for _, s = range samples {
		if s.Name != metricIfOperStatus {
			continue
		}
		var idx, err = strconv.Atoi(s.Labels["ifindex"])
		if err != nil {
			continue // without an index, the sample identifies no port
		}
		// The `port` tag is absent when `ifDescr` could not be read: the name stays
		// empty, detection works anyway.
		out[idx] = portObs{status: int(s.Value), name: s.Labels["port"]}
	}
	return out
}

// portChange is a retained transition, before it is shaped into an event.
type portChange struct {
	ifIndex int
	name    string
	up      bool
	status  int
}

// diffPorts compares two observations of the same device.
//
// Three silences, each of which avoids inventing a fact:
//
//   - A port seen for the FIRST time produces nothing. Announcing a transition
//     on the first pass would announce a change that did not happen — same
//     reason as targetState's `up == nil` in monitor.go.
//   - A port that DISAPPEARED from the table produces nothing: a stack member
//     removed or a shrunken front panel is an operating decision, not an outage.
//   - A value change that does not change the boolean state (2 → 7) produces
//     nothing: it is the same information said differently.
func diffPorts(prev, cur map[int]portObs) []portChange {
	var out []portChange
	var idx int
	var obs portObs
	for idx, obs = range cur {
		var before, known = prev[idx]
		if !known || portUp(before.status) == portUp(obs.status) {
			continue
		}
		out = append(out, portChange{
			ifIndex: idx, name: obs.name, up: portUp(obs.status), status: obs.status,
		})
	}
	// A 48-port switch that reboots produces forty-eight transitions at once: map
	// iteration order is random, and a log that lists the ports in a different
	// order on every incident is unreadable.
	sort.Slice(out, func(i, j int) bool { return out[i].ifIndex < out[j].ifIndex })
	return out
}

// portDetail composes the accompanying text of a port event.
//
// Two pieces of information of a different nature may meet there, and they must
// be told apart: the exotic `ifOperStatus` value describes THIS port and comes
// from the poll; the trap label qualifies the BURST that woke us up, not the
// port — it is a diagnostic hint, never data.
func portDetail(ch portChange, trapLabel string) string {
	var parts []string
	if ch.status != ifOperUp && ch.status != ifOperDown {
		parts = append(parts, "ifOperStatus="+strconv.Itoa(ch.status)+
			" ("+operStatusLabel(ch.status)+")")
	}
	if trapLabel != "" {
		parts = append(parts, trapLabel)
	}
	return strings.Join(parts, " ; ")
}

// --- Transition detection: wiring on the collector ---

// notePortStates diffs the current observation against the previous one and
// emits the fleet's transitions.
//
// Called after EVERY successful `if_state` poll, and only for that one:
// `if_counters` contains no `ifOperStatus`, and passing it here would make every
// port disappear on every counters cycle.
func (c *SNMPCollector) notePortStates(deviceID string,
	samples []metricbuf.Sample, trigger, trapLabel string) {
	var cur = observedPorts(samples)
	if len(cur) == 0 {
		// A poll that returned samples but no `ifOperStatus` is a PARTIAL read (the
		// table was missing from the walk), not a device whose ports vanished. The
		// previous observation is kept: overwriting it would turn the next read into a
		// "first observation", hence a silence on the following transition.
		return
	}

	c.mu.Lock()
	var changes = diffPorts(c.portStates[deviceID], cur)
	c.portStates[deviceID] = cur
	var ip string
	var w tunnel.SNMPWatch
	var found bool
	if w, found = findWatch(c.watches, deviceID); found {
		ip = w.IP
	}
	c.mu.Unlock()

	var ch portChange
	for _, ch = range changes {
		var kind = tunnel.EventPortDown
		if ch.up {
			kind = tunnel.EventPortUp
		}
		c.emitEvent(tunnel.Event{
			Type: kind, DeviceID: deviceID, IP: ip,
			IfIndex: ch.ifIndex, PortName: ch.name, Trigger: trigger,
			Detail: portDetail(ch, trapLabel),
		})
	}
}

// emitEvent drops an event on the proxy's channel, without ever blocking.
func (c *SNMPCollector) emitEvent(ev tunnel.Event) {
	c.mu.Lock()
	var fn = c.emit
	c.mu.Unlock()
	if fn == nil {
		// Collector not wired yet (or a test): say so without panicking or losing the
		// poll that produced the event.
		slog.Debug("port event with no output channel", "type", ev.Type)
		return
	}
	fn(ev)
}

// findWatch finds a target by its identifier. Called under c.mu.
func findWatch(watches []tunnel.SNMPWatch, deviceID string) (tunnel.SNMPWatch, bool) {
	var w tunnel.SNMPWatch
	for _, w = range watches {
		if w.DeviceID == deviceID {
			return w, true
		}
	}
	return tunnel.SNMPWatch{}, false
}

// --- Identity filter ---

// trapVerdict says what was done with a trap, and why. All three cases are
// COUNTED: a channel that rejects everything and a silent channel look far too
// alike in a log.
type trapVerdict int

const (
	// trapUnknownSource: the source IP is in no SNMPWatch. On a production network
	// this is ordinary — other devices spray the collector — and it deserves no
	// more than DEBUG.
	trapUnknownSource trapVerdict = iota
	// trapBadCommunity: the IP is known, the community does not match. This is the
	// case that stops a third party on the path from injecting port events by
	// spoofing the address of a watched device.
	trapBadCommunity
	trapAccepted
)

// effectiveCommunity returns the community actually expected from a device.
//
// Empty means "public" and not "any": that is exactly snmppoll.dialSNMP's
// fallback, and two different rules for the same watch would end up accepting
// traps from a device we cannot manage to poll.
func effectiveCommunity(w tunnel.SNMPWatch) string {
	if w.Community == "" {
		// The SAME constant as the poll, and not a "public" copied here: this fallback
		// once existed in three copies in this repository, and the day one of them
		// changes, the trap filter accepts packets from a device we can no longer poll
		// — or the reverse, with nothing saying so.
		return snmppoll.DefaultCommunity
	}
	return w.Community
}

// classifyTrap decides whether a trap comes from a watched device.
//
// Identity by SOURCE IP checked against the watch list, plus the community.
// That is weak — a source IP can be spoofed — but it is all a v2c trap offers,
// and that is precisely why nothing of its content is written: at worst, an
// attacker gets an early `if_state` walk, whose result comes from the device
// and not from them.
//
// v3 traps arrive without a community and are therefore rejected: v3 will come
// with service identity (see tunnel.SNMPWatch.Community), and an "accept when
// we cannot verify" would be the opposite of this filter.
func classifyTrap(watches []tunnel.SNMPWatch, srcIP, community string) (tunnel.SNMPWatch, trapVerdict) {
	var src, err = netip.ParseAddr(srcIP)
	if err != nil {
		return tunnel.SNMPWatch{}, trapUnknownSource
	}
	// Unmap: a dual-stack UDP socket returns "::ffff:10.0.0.5" where the watch says
	// "10.0.0.5". Comparing the texts would make the filter fail depending on the
	// mood of the host's network stack.
	src = src.Unmap()

	var ipKnown bool
	var w tunnel.SNMPWatch
	for _, w = range watches {
		var ip, parseErr = netip.ParseAddr(w.IP)
		if parseErr != nil || ip.Unmap() != src {
			continue
		}
		ipKnown = true
		if community == effectiveCommunity(w) {
			return w, trapAccepted
		}
	}
	if ipKnown {
		return tunnel.SNMPWatch{}, trapBadCommunity
	}
	return tunnel.SNMPWatch{}, trapUnknownSource
}

// trapOIDLabel returns the received trap's OID and, if recognised, its name.
//
// For a v2c trap, the OID lives in the `snmpTrapOID.0` varbind; a v1 trap
// carries it differently (Enterprise + GenericTrap) and returns an empty
// string here. That is not a gap to fill: the value only serves the log and
// the Detail.
func trapOIDLabel(pkt *gosnmp.SnmpPacket) (string, string) {
	if pkt == nil {
		return "", ""
	}
	var oid string
	var v gosnmp.SnmpPDU
	for _, v = range pkt.Variables {
		if strings.TrimPrefix(v.Name, ".") != oidSnmpTrapOID {
			continue
		}
		var s, ok = v.Value.(string)
		if ok {
			oid = strings.TrimPrefix(s, ".")
		}
		break
	}
	switch oid {
	case oidLinkUp:
		return oid, "linkUp"
	case oidLinkDown:
		return oid, "linkDown"
	}
	return oid, ""
}

// trapSeenDetail composes the Detail of the proof event: what got through.
//
// The label when the trap is recognised, the raw OID otherwise — that is what
// lets the operator tell "the device sends me its linkDowns" from "it only
// sends me vendor traps", two situations that prove the path equally but are
// not fixed the same way. Never EMPTY, even for a v1 trap that does not carry
// snmpTrapOID.0 (see trapOIDLabel): an empty Detail would read as a field lost
// on the way.
//
// What goes in under no form: the community. It is an operating secret, and the
// event goes to the controller and then to the UI.
func trapSeenDetail(oid, label string) string {
	if label != "" {
		return label
	}
	if oid != "" {
		return "oid=" + oid
	}
	return "trap without an identifiable OID"
}

// --- Per-device throttling ---

// trapGate groups bursts of traps from the same device.
//
// A 48-port switch that reboots emits a hundred traps in a few milliseconds.
// One poll per trap would turn the fast path into an amplifier aimed at a
// device already in trouble — and this product line (Instant On 1930) only
// holds five SNMP sessions. Since `ifTable` is read whole anyway, reading for
// one port or for fifty costs the same: we group.
//
// The decision is SEPARATE from the scheduling (admit returns a delay, it
// starts no timer) so it can be tested without a real clock.
type trapGate struct {
	mu sync.Mutex
	// pending: a poll is already scheduled for this device, arriving traps are
	// absorbed by it.
	pending map[string]bool
	// label: label of the trap that OPENED the current window.
	label map[string]string
	// lastPoll: last trap-triggered poll, which sets the floor.
	lastPoll map[string]time.Time
	// seen: devices whose return path has ALREADY been proven to the controller
	// (tunnel.EventTrapSeen emitted once).
	//
	// This state has nothing to do with throttling, and yet it lives here: the
	// proof must re-arm when the device leaves and comes back to monitoring, and
	// `forget` is the ONLY purge point that SetWatches (cmd/proxy/snmp.go) already
	// calls. One more map on the collector would have required one more line over
	// there — hence a change to a file outside this batch, and above all a second
	// place not to forget the day the purge changes. Attached to the structure
	// that is already purged, the property "nothing survives a device's removal"
	// is proven in a single place.
	seen map[string]bool
}

func newTrapGate() *trapGate {
	return &trapGate{
		pending:  map[string]bool{},
		label:    map[string]string{},
		lastPoll: map[string]time.Time{},
		seen:     map[string]bool{},
	}
}

// admit says whether a poll must be scheduled for this device, and in how long.
//
// The tunnel.TrapMinPollGap floor DELAYS, it does not discard: a trap arriving
// just after a poll may signal a state that poll has not seen yet, and dropping
// it would lose a real transition until the next ordinary cycle. A port
// flapping in a loop therefore produces at worst one walk every two seconds,
// which is the intended behaviour.
func (g *trapGate) admit(deviceID, label string, now time.Time) (time.Duration, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.pending[deviceID] {
		// Window already open. It still adopts the first usable label of the burst:
		// a vendor trap followed by a linkDown must leave "linkDown" for the diagnosis.
		if g.label[deviceID] == "" {
			g.label[deviceID] = label
		}
		return 0, false
	}
	var delay = tunnel.TrapCoalesce
	var floor = g.lastPoll[deviceID].Add(tunnel.TrapMinPollGap)
	if now.Add(delay).Before(floor) {
		delay = floor.Sub(now)
	}
	g.pending[deviceID] = true
	g.label[deviceID] = label
	return delay, true
}

// release records that a trap-triggered poll has just happened, and returns
// the label of the burst it settles.
func (g *trapGate) release(deviceID string, at time.Time) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	var label = g.label[deviceID]
	delete(g.pending, deviceID)
	delete(g.label, deviceID)
	g.lastPoll[deviceID] = at
	return label
}

// firstSeen returns true ONLY ONCE per device: on the first accepted trap since
// it entered monitoring.
//
// It is the guard of tunnel.EventTrapSeen. The return-path proof no longer
// changes once acquired; repeating it on every trap would turn a chatty fleet
// into a log generator, and drown the events that do announce a change.
//
// Test-and-set under the same lock as the rest of the structure: today all the
// traps of a burst arrive on gosnmp's read goroutine, but nothing in its
// contract promises that, and two proofs for one device would be exactly the
// noise we are trying to avoid.
func (g *trapGate) firstSeen(deviceID string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.seen[deviceID] {
		return false
	}
	g.seen[deviceID] = true
	return true
}

// forget drops a device that is no longer watched. Without it, the maps keep a
// trace of everything the controller ever entrusted to this proxy.
//
// It is also what RE-ARMS the return-path proof (see seen): a device coming back
// into the list comes back from a world where a firewall, a route or an ACL may
// have changed — yesterday's proof commits to nothing there.
func (g *trapGate) forget(watched map[string]bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	var id string
	for id = range g.lastPoll {
		if !watched[id] {
			delete(g.lastPoll, id)
		}
	}
	for id = range g.pending {
		if !watched[id] {
			delete(g.pending, id)
			delete(g.label, id)
		}
	}
	for id = range g.seen {
		if !watched[id] {
			delete(g.seen, id)
		}
	}
}

// --- Reception ---

// trapStats counts what the socket saw. Nothing here goes up to WARN: traps
// from unwatched devices are ordinary on a production network, and logging them
// loudly would drown what matters.
type trapStats struct {
	received      uint64
	accepted      uint64
	absorbed      uint64
	unknownSource uint64
	badCommunity  uint64
}

// trapHandler handles a decoded trap: the packet, its source, and the local
// address it targeted — invalid when the kernel cannot tell.
type trapHandler func(pkt *gosnmp.SnmpPacket, src *net.UDPAddr, dst netip.Addr)

// trapReceiver: a listening trap socket, with the port it occupies.
//
// # Why OUR socket, and not gosnmp.TrapListener
//
// Because its contract only returns the datagram's SOURCE
// (`TrapHandlerFunc(*SnmpPacket, *net.UDPAddr)`) and its socket is an
// unexported field: nothing in gosnmp gives access to the DESTINATION address
// — i.e. to which of our addresses the device actually targeted. And that is
// exactly what was missing for the return-path proof to be attributable to the
// address meshcraft wrote (see tunnel.EventTrapSeen): a listener on the
// wildcard address accepts everything, and a device configured towards another
// collector on the same machine used to fabricate an unrelated "proof".
//
// What it does NOT cost, and that is what made the decision: nothing of the
// SNMP decoding. `gosnmp.GoSNMP.UnmarshalTrap` and `SnmpPacket.MarshalMsg` are
// EXPORTED — so we keep the library's parser, down to the response to
// `InformRequest`. The only thing lost is engineID reporting for v3 traps
// (`reportAuthoritativeEngineID`), which only concerns packets without a
// community, hence ones `classifyTrap` throws away anyway.
//
// And it SIMPLIFIES the bind: `net.ListenUDP` returns its error right away,
// whereas `TrapListener.Listen` blocks and forced us to chase its listening
// signal — a goroutine, two channels and a guard delay — to tell "bound" from
// "refused".
type trapReceiver struct {
	conn *net.UDPConn
	// p4 / p6: the SAME socket as seen by x/net, which alone knows how to ask the
	// kernel for each datagram's destination address. Only one of the two is
	// non-nil; both may be nil, and then we listen without being able to attribute
	// — losing attribution is better than losing traps.
	p4 *ipv4.PacketConn
	p6 *ipv6.PacketConn
	// port: the REQUESTED port. That is what reconcileTraps compares to the
	// instruction, not the one obtained: otherwise an instruction of 0 (a test)
	// would rebind the socket every second.
	port int
	// bound: the port actually obtained, for the log and for tests.
	bound int
	// params: our instance, never gosnmp.Default — that singleton is shared with
	// the polling in the same process, and decoding writes into it.
	params *gosnmp.GoSNMP
	handle trapHandler
	// closing: a `Close` makes the pending read fail, and that error is not an
	// incident. Without the flag, every port change would write an error line for a
	// shutdown we had just asked for.
	closing atomic.Bool
	// dead: the read loop returned on an error nobody asked of it. This is what
	// prevents the channel's SILENT DEATH, and it had to be planned for when leaving
	// gosnmp: its loop ignored its read errors and spun forever. A socket that stays
	// registered in the collector while nobody reads it any more would be
	// indistinguishable from a quiet fleet — exactly the failure mode this whole
	// file fights. reconcileTraps therefore rebinds it as if the instruction had
	// changed.
	dead atomic.Bool
}

func (r *trapReceiver) stop() {
	if r == nil {
		return
	}
	r.closing.Store(true)
	_ = r.conn.Close()
}

// dstKnown: can this socket tell which address a datagram was sent to? The
// proxy announces it at startup, because it is the difference between an
// attributed proof and a proof that is only a bet.
func (r *trapReceiver) dstKnown() bool { return r != nil && (r.p4 != nil || r.p6 != nil) }

// startTrapReceiver binds the socket, asks the kernel for the datagrams'
// destination address, and starts reading.
func startTrapReceiver(port int, handle trapHandler) (*trapReceiver, error) {
	// WILDCARD address, as before: the proxy does not know which of its addresses a
	// device will target — that is precisely the question PKTINFO answers. A nil IP
	// gives the same dual-stack socket as the "0.0.0.0:162" of before.
	var conn, err = net.ListenUDP("udp", &net.UDPAddr{Port: port})
	if err != nil {
		return nil, err
	}
	var r = &trapReceiver{
		conn: conn, port: port, handle: handle,
		params: &gosnmp.GoSNMP{Version: gosnmp.Version2c, Transport: "udp"},
	}
	var local, ok = conn.LocalAddr().(*net.UDPAddr)
	if ok {
		r.bound = local.Port
	}
	// The ORDER follows the socket's FAMILY, and it is not interchangeable:
	// IPV6_RECVPKTINFO only exists on an AF_INET6 socket — which is what a wildcard
	// listen on a dual-stack host gives, and where the kernel also reports the
	// destination of IPv4 datagrams in mapped form — while IP_PKTINFO only exists on
	// an AF_INET one, the case of a host without IPv6. So we ask for one, then the
	// other, and do without both if the kernel refuses: a trap whose destination is
	// unknown is still a received trap, and the controller knows what such a proof
	// is worth.
	var p6 = ipv6.NewPacketConn(conn)
	if p6.SetControlMessage(ipv6.FlagDst, true) == nil {
		r.p6 = p6
	} else {
		var p4 = ipv4.NewPacketConn(conn)
		if p4.SetControlMessage(ipv4.FlagDst, true) == nil {
			r.p4 = p4
		}
	}
	go r.readLoop()
	return r, nil
}

// trapDstAddr normalises the destination address returned by the kernel.
//
// Unmap for classifyTrap's reason: a dual-stack socket returns
// "::ffff:10.42.0.67" where the controller wrote "10.42.0.67" into the device,
// and comparing the texts would make attribution fail depending on the mood of
// the host's stack. An absent or wildcard address says nothing and therefore
// means "I don't know" — which is a result, not a default value.
func trapDstAddr(ip net.IP) netip.Addr {
	var addr, ok = netip.AddrFromSlice(ip)
	if !ok {
		return netip.Addr{}
	}
	addr = addr.Unmap()
	if !addr.IsValid() || addr.IsUnspecified() {
		return netip.Addr{}
	}
	return addr
}

// read returns the datagram AND the local address it was sent to.
func (r *trapReceiver) read(buf []byte) (int, netip.Addr, *net.UDPAddr, error) {
	var n int
	var src net.Addr
	var err error
	var dst net.IP
	switch {
	case r.p6 != nil:
		var cm *ipv6.ControlMessage
		n, cm, src, err = r.p6.ReadFrom(buf)
		if cm != nil {
			dst = cm.Dst
		}
	case r.p4 != nil:
		var cm *ipv4.ControlMessage
		n, cm, src, err = r.p4.ReadFrom(buf)
		if cm != nil {
			dst = cm.Dst
		}
	default:
		var from *net.UDPAddr
		n, from, err = r.conn.ReadFromUDP(buf)
		src = from
	}
	var udp, _ = src.(*net.UDPAddr)
	return n, trapDstAddr(dst), udp, err
}

// readLoop reads, decodes, and hands over to the filter.
//
// ONE goroutine only, like the gosnmp loop it replaces: `onTrap` is written to
// be called on the socket's critical path and does not block there (the poll
// leaves in a timer).
func (r *trapReceiver) readLoop() {
	for {
		// A FRESH buffer per datagram, like gosnmp, and not a reused one — the saving
		// would be a trap. `decodeValue` returns a varbind's `OctetString`s by SLICING
		// into the read buffer (`retVal.Value = data[cursor:length]`): the decoded
		// packet therefore aliases that buffer, and the next loop iteration would
		// silently overwrite the content of a packet still being processed. The current
		// filter only reads strings that are already copied, but 4 KB per trap is a
		// trivial price for that to stay true the day someone wants to read a varbind.
		//
		// 4096 bytes, gosnmp's size: beyond that the datagram is truncated, hence
		// rejected at decoding — and a device that emits 4 KB traps has a bigger
		// problem than its responsiveness.
		var buf = make([]byte, 4096)
		var n, dst, src, err = r.read(buf)
		if err != nil {
			if r.closing.Load() {
				return
			}
			// A bound UDP socket does not "break" along the way: the error is either the
			// shutdown we just asked for, or an incident one does not recover from by
			// retrying in a tight loop. Say so, declare ourselves dead, and reconcileTraps
			// binds a fresh socket the next second — with its throttling if the bind fails
			// in turn.
			slog.Warn("trap reading interrupted — the socket will be rebound",
				"err", err, "port", r.bound)
			r.dead.Store(true)
			return
		}
		var pkt, decErr = r.params.UnmarshalTrap(buf[:n], false)
		if decErr != nil {
			// 162/udp receives everything on a production network. An undecodable datagram
			// is noise, not an incident: DEBUG, like an unwatched source.
			slog.Debug("trap indecodable", "err", decErr, "src", src)
			continue
		}
		r.handle(pkt, src, dst)
		r.answerInform(pkt, src)
	}
}

// answerInform answers an `InformRequest`, which is an ACKNOWLEDGED trap:
// without a response, the sender retransmits then gives up.
//
// Carried over from gosnmp.TrapListener, because losing it would be a silent
// regression. meshcraft never sets up informs — `instanton.ApplyTrapReceiver`
// declares a trap recipient — but a hand-configured device may emit them, and it
// has no reason to retransmit for nothing. The packet is sent back as is,
// variables included (RFC 3416 § 4.2.4): that is what the sender expects to
// settle its retransmission.
func (r *trapReceiver) answerInform(pkt *gosnmp.SnmpPacket, src *net.UDPAddr) {
	if pkt == nil || src == nil || pkt.PDUType != gosnmp.InformRequest {
		return
	}
	pkt.PDUType = gosnmp.GetResponse
	pkt.Error, pkt.ErrorIndex = gosnmp.NoError, 0
	var out, err = pkt.MarshalMsg()
	if err != nil {
		slog.Debug("response a un inform non serialisable", "err", err, "src", src)
		return
	}
	if _, err = r.conn.WriteToUDP(out, src); err != nil {
		slog.Debug("inform response not sent", "err", err, "src", src)
	}
}

// onTrap is called by the read loop, hence on the socket's critical path: it
// filters, decides, and returns. The poll leaves in a timer, never here —
// holding this goroutine would lose the following traps of the burst.
func (c *SNMPCollector) onTrap(pkt *gosnmp.SnmpPacket, src *net.UDPAddr, dst netip.Addr) {
	c.mu.Lock()
	c.trapStats.received++
	var watches = c.watches
	c.mu.Unlock()

	var srcIP string
	if src != nil {
		srcIP = src.IP.String()
	}
	// The TARGETED address, or nothing at all. An "I don't know" is passed on
	// openly: filling it with the address the proxy believes it uses to reach the
	// device would make the controller's check tautological, since that address is
	// precisely the hypothesis to verify.
	var dstAddr string
	if dst.IsValid() {
		dstAddr = dst.String()
	}
	var oid, label = trapOIDLabel(pkt)
	var w, verdict = classifyTrap(watches, srcIP, pkt.Community)

	c.mu.Lock()
	switch verdict {
	case trapUnknownSource:
		c.trapStats.unknownSource++
	case trapBadCommunity:
		c.trapStats.badCommunity++
	}
	c.mu.Unlock()

	if verdict != trapAccepted {
		var reason = "unwatched source"
		if verdict == trapBadCommunity {
			reason = "unexpected community"
		}
		slog.Debug("trap ignored", "reason", reason, "src", srcIP, "oid", oid)
		return
	}
	slog.Debug("trap received", "device", w.DeviceID, "ip", srcIP, "oid", oid, "trap", label,
		"destination", dstAddr)

	// The return-path proof, on the first accepted trap and on it alone.
	//
	// Placed HERE, between the identity filter and the throttling, and that is the
	// important point: what proves the path is that a trap from this device
	// ARRIVED and was recognised, not that it earned a poll. A trap absorbed by the
	// grouping window (admit returning false) went through the same firewall, the
	// same route and the same ACL as the one that opened it: putting this after
	// `admit` would make the proof depend on scheduling, and a device whose first
	// trap fell into an ongoing burst would stay indefinitely "address applied,
	// path not verified" in the UI.
	//
	// BEFORE the filter, on the other hand, never: anyone on the path could then
	// fabricate the proof of a device that emitted nothing.
	//
	// Emitting does not block the socket's critical path (see emitEvent), and
	// happens outside the gate's lock — otherwise the Monitor's output channel
	// would govern the lifetime of a lock taken on every trap of the fleet.
	if c.trapGate.firstSeen(w.DeviceID) {
		c.emitEvent(tunnel.Event{
			Type: tunnel.EventTrapSeen, DeviceID: w.DeviceID,
			// The WATCH's IP, not the socket's: it is the address the controller knows,
			// and classifyTrap has just guaranteed they designate the same host. Copying
			// the source would give "::ffff:10.0.0.5" every other day, depending on the
			// host's network stack.
			IP: w.IP,
			// The targeted LOCAL address, by contrast, does come from the socket and from
			// nowhere else: it is the only fact of this packet the proxy knows and the
			// controller cannot deduce. It is what makes the proof attributable to the
			// written destination (see tunnel.Event.DestAddr).
			DestAddr: dstAddr,
			// No Trigger: that field says what saved the wait on a TRANSITION (see
			// tunnel.Event.Trigger), and there is none here. The event type already says a
			// trap got through.
			Detail: trapSeenDetail(oid, label),
		})
	}

	var delay, schedule = c.trapGate.admit(w.DeviceID, label, time.Now())
	if !schedule {
		c.mu.Lock()
		c.trapStats.absorbed++
		c.mu.Unlock()
		return
	}
	c.mu.Lock()
	c.trapStats.accepted++
	c.mu.Unlock()

	var deviceID = w.DeviceID
	time.AfterFunc(delay, func() { c.trapPoll(deviceID) })
}

// trapPoll execute le poll de confirmation, SUR LE PROXY.
//
// No round trip to the controller: the trap arrives here, the response is here,
// and asking permission across the WAN would cancel the second we just gained.
// The controller receives the RESULT, in the form of a port event like any
// other.
func (c *SNMPCollector) trapPoll(deviceID string) {
	var label = c.trapGate.release(deviceID, time.Now())

	c.mu.Lock()
	// The watch is re-read NOW and not at trap time: the grouping window leaves
	// time for a new WatchConfig to arrive, and polling with a stale community
	// would fail for no visible reason.
	var w, found = findWatch(c.watches, deviceID)
	if found {
		// The trap BRINGS FORWARD the `if_state` poll: the ordinary cadence is shifted
		// by as much. Without that, the regular cycle would re-read the same table
		// within the second — two walks for one piece of information, on the very
		// device that is having a problem.
		c.last[deviceID+"|state"] = time.Now()
	}
	c.mu.Unlock()
	if !found {
		return // removed from monitoring in the meantime: nothing to confirm
	}
	c.job(context.Background(), targetOf(w), snmppoll.MetricIfState, triggerTrap, label)()
}

// --- Listener lifecycle ---

// trapSettings reads what concerns traps from the monitoring configuration.
//
// Why fetch it, instead of receiving it? `Monitor.SetConfig` is the entry point
// of the WatchConfig, but it only passes `cfg.SNMP` to the collector, and it
// does so BEFORE writing `m.cfg` — a collector reading the config from
// SetWatches would therefore read the PREVIOUS one, the kind of config lag no
// test catches. Reconciliation happens elsewhere, from the collector's loop:
// idempotent by construction, it rebinds no socket when nothing changed, and at
// worst one second passes before a change takes effect — on a channel whose
// role is precisely to be only an accelerator.
func (m *Monitor) trapSettings() (bool, int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var port = m.cfg.TrapPort
	if port <= 0 {
		port = tunnel.DefaultTrapPort
	}
	return m.cfg.TrapListen, port
}

// trapLoop keeps the trap listener aligned with the configuration.
//
// In its OWN goroutine: binding or closing a socket may take a few seconds
// (gosnmp waits for its read loop to finish), and that delay has no business
// in the loop that polls the fleet.
func (c *SNMPCollector) trapLoop(ctx context.Context) {
	var tick = time.NewTicker(time.Second)
	defer tick.Stop()
	defer c.stopTraps()

	var now time.Time
	var lastSummary = time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case now = <-tick.C:
			c.reconcileTraps(now)
			if now.Sub(lastSummary) >= trapSummaryEvery {
				lastSummary = now
				c.logTrapStats()
			}
		}
	}
}

// reconcileTraps aligns the socket with the instruction. Idempotent: nothing
// moves as long as the instruction does not — rebinding a socket on every
// WatchConfig received would lose traps at every configuration change, and
// that would show in no log.
func (c *SNMPCollector) reconcileTraps(now time.Time) {
	var listen, port = monitor.trapSettings()

	c.mu.Lock()
	var cur = c.traps
	if !listen || port != c.trapTried {
		// The instruction changed: failure throttling starts over from zero. The
		// operator fixing a port should not have to wait for the end of a backoff they
		// cannot see.
		c.trapRetryAt, c.trapErrSaid, c.trapTried = time.Time{}, false, port
	}
	var retryAt = c.trapRetryAt
	c.mu.Unlock()

	if cur != nil && (!listen || cur.port != port || cur.dead.Load()) {
		slog.Info("SNMP trap listener stopped", "port", cur.port,
			"read_dead", cur.dead.Load())
		// A closed socket does not reopen: changing port — or replacing a dead read —
		// means building a fresh one.
		cur.stop()
		c.mu.Lock()
		c.traps = nil
		c.mu.Unlock()
		cur = nil
	}
	if !listen || cur != nil || now.Before(retryAt) {
		return
	}

	var rec, err = startTrapReceiver(port, c.onTrap)
	if err != nil {
		c.mu.Lock()
		c.trapRetryAt = now.Add(trapBindRetry)
		var said = c.trapErrSaid
		c.trapErrSaid = true
		c.mu.Unlock()
		if said {
			slog.Debug("trap listener still impossible", "port", port, "err", err)
			return
		}
		// ONCE, and loudly — then carry on without. Losing responsiveness is not losing
		// monitoring; pretending to listen would be the worst outcome, because an
		// operator would believe they see their ports go down within a second.
		slog.Error("SNMP trap listener impossible — monitoring continues without "+
			"the fast path, port transitions will be seen at the polling cadence "+
			"(port already taken by another collector? "+
			"CAP_NET_BIND_SERVICE missing?)", "port", port, "err", err)
		return
	}
	c.mu.Lock()
	c.traps = rec
	c.mu.Unlock()
	slog.Info("SNMP trap listener active", "port", port,
		"destination_readable", rec.dstKnown())
	if !rec.dstKnown() {
		// Once per bind, and loudly: reactive monitoring works, but this proxy will
		// not be able to say which of its addresses a device sends its traps to. The
		// controller will therefore record UNATTRIBUTED proofs — "traps arrive here",
		// without being able to assert that it is the address written into the device
		// that receives them. Keeping quiet would suggest a check that does not happen.
		slog.Warn("this kernel does not return the datagrams' destination address " +
			"(PKTINFO refused): traps are received normally, but the return-path " +
			"proof cannot be attributed to the address written into " +
			"the device")
	}
}

func (c *SNMPCollector) stopTraps() {
	c.mu.Lock()
	var cur = c.traps
	c.traps = nil
	c.mu.Unlock()
	cur.stop()
}

func (c *SNMPCollector) logTrapStats() {
	c.mu.Lock()
	var s = c.trapStats
	var listening = c.traps != nil
	c.mu.Unlock()
	if !listening && s.received == 0 {
		return
	}
	slog.Debug("trap summary", "received", s.received, "polls_triggered", s.accepted,
		"absorbed", s.absorbed, "unknown_source", s.unknownSource,
		"bad_community", s.badCommunity)
}
