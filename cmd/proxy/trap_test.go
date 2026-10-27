package main

import (
	"encoding/json"
	"net"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"

	"meshcraft/internal/metricbuf"
	"meshcraft/internal/snmppoll"
	"meshcraft/internal/tunnel"
)

// OIDs of the tables read by snmppoll. Copied by hand, and that is the point:
// this test is the CONTRACT between the poller and transition detection. If
// snmppoll renames its metric or its `ifindex` tag, this is where it breaks —
// not in production, six months later, in the form of port events that no
// longer go out.
const (
	tOidIfDescr      = "1.3.6.1.2.1.2.2.1.2"
	tOidIfType       = "1.3.6.1.2.1.2.2.1.3"
	tOidIfOperStatus = "1.3.6.1.2.1.2.2.1.8"
)

func TestObservedPortsFollowsTheSnmppollContract(t *testing.T) {
	var tables = map[string]map[string]gosnmp.SnmpPDU{
		tOidIfDescr: {
			"1": {Type: gosnmp.OctetString, Value: []byte("GE1")},
			"2": {Type: gosnmp.OctetString, Value: []byte("GE2")},
		},
		tOidIfType: {
			"1": {Type: gosnmp.Integer, Value: snmppoll.IfTypeEthernet},
			"2": {Type: gosnmp.Integer, Value: snmppoll.IfTypeEthernet},
		},
		tOidIfOperStatus: {
			"1": {Type: gosnmp.Integer, Value: ifOperUp},
			"2": {Type: gosnmp.Integer, Value: ifOperDown},
		},
	}
	var got = observedPorts(snmppoll.Samples("sw1", tables, time.Now(), 0))
	if len(got) != 2 {
		t.Fatalf("2 ports expected, got %d: %+v", len(got), got)
	}
	if got[1].status != ifOperUp || got[1].name != "GE1" {
		t.Errorf("port 1 : %+v", got[1])
	}
	if got[2].status != ifOperDown || got[2].name != "GE2" {
		t.Errorf("port 2 : %+v", got[2])
	}
}

// operSample builds the sample snmppoll would return for a port.
func operSample(ifindex, port string, status float64) metricbuf.Sample {
	var labels = map[string]string{"device": "sw1", "ifindex": ifindex}
	if port != "" {
		labels["port"] = port
	}
	return metricbuf.Sample{Name: metricIfOperStatus, Labels: labels, Value: status}
}

func TestObservedPortsIgnoresWhatDoesNotIdentify(t *testing.T) {
	var samples = []metricbuf.Sample{
		operSample("3", "", ifOperUp), // no name: the index is enough to identify it
		{Name: "meshcraft_if_speed_mbps",
			Labels: map[string]string{"device": "sw1", "ifindex": "3"}, Value: 1000},
		{Name: metricIfOperStatus, Labels: map[string]string{"device": "sw1"}, Value: 1},
		{Name: metricIfOperStatus,
			Labels: map[string]string{"device": "sw1", "ifindex": "TRK1"}, Value: 1},
	}
	var got = observedPorts(samples)
	if len(got) != 1 {
		t.Fatalf("a single usable port expected, got %+v", got)
	}
	if got[3].status != ifOperUp || got[3].name != "" {
		t.Errorf("port 3 : %+v", got[3])
	}
}

func TestDiffPorts(t *testing.T) {
	type cas struct {
		name      string
		prev, cur map[int]portObs
		expected  []portChange
	}
	var cases = []cas{{
		name: "first observation: silent",
		cur:  map[int]portObs{1: {status: ifOperUp}, 2: {status: ifOperDown}},
	}, {
		name:     "up to down",
		prev:     map[int]portObs{1: {status: ifOperUp, name: "GE1"}},
		cur:      map[int]portObs{1: {status: ifOperDown, name: "GE1"}},
		expected: []portChange{{ifIndex: 1, name: "GE1", up: false, status: ifOperDown}},
	}, {
		name:     "down to up",
		prev:     map[int]portObs{1: {status: ifOperDown, name: "GE1"}},
		cur:      map[int]portObs{1: {status: ifOperUp, name: "GE1"}},
		expected: []portChange{{ifIndex: 1, name: "GE1", up: true, status: ifOperUp}},
	}, {
		name: "port gone from the table: silent",
		prev: map[int]portObs{1: {status: ifOperUp}, 2: {status: ifOperUp}},
		cur:  map[int]portObs{1: {status: ifOperUp}},
	}, {
		name:     "exotic value: down, with the value kept",
		prev:     map[int]portObs{5: {status: ifOperUp}},
		cur:      map[int]portObs{5: {status: 3}},
		expected: []portChange{{ifIndex: 5, up: false, status: 3}},
	}, {
		name: "down to lowerLayerDown: same state, so silent",
		prev: map[int]portObs{5: {status: ifOperDown}},
		cur:  map[int]portObs{5: {status: ifOperLowerLayerDown}},
	}, {
		name: "rename without a state change: silent",
		prev: map[int]portObs{1: {status: ifOperUp, name: "GE1"}},
		cur:  map[int]portObs{1: {status: ifOperUp, name: "uplink"}},
	}, {
		name: "notPresent to down: same state, so silent",
		prev: map[int]portObs{9: {status: 6}},
		cur:  map[int]portObs{9: {status: ifOperDown}},
	}, {
		name: "burst ordered by index",
		prev: map[int]portObs{1: {status: ifOperUp}, 2: {status: ifOperUp}, 3: {status: ifOperUp}},
		cur:  map[int]portObs{1: {status: ifOperDown}, 2: {status: ifOperDown}, 3: {status: ifOperDown}},
		expected: []portChange{
			{ifIndex: 1, status: ifOperDown}, {ifIndex: 2, status: ifOperDown},
			{ifIndex: 3, status: ifOperDown},
		},
	}}

	var k cas
	for _, k = range cases {
		t.Run(k.name, func(t *testing.T) {
			var got = diffPorts(k.prev, k.cur)
			if len(got) != len(k.expected) {
				t.Fatalf("%d transitions expected, got %d: %+v",
					len(k.expected), len(got), got)
			}
			var i int
			for i = range got {
				if got[i] != k.expected[i] {
					t.Errorf("transition %d: got %+v, expected %+v",
						i, got[i], k.expected[i])
				}
			}
		})
	}
}

func TestPortDetail(t *testing.T) {
	if portDetail(portChange{status: ifOperDown}, "") != "" {
		t.Error("an ordinary down without a trap has nothing to add")
	}
	if portDetail(portChange{status: ifOperUp}, "linkUp") != "linkUp" {
		t.Error("the trap label must accompany the event")
	}
	var got = portDetail(portChange{status: 5}, "linkDown")
	if got != "ifOperStatus=5 (dormant) ; linkDown" {
		t.Errorf("unexpected composed detail: %q", got)
	}
}

// --- The collector, end to end, without a socket or a device ---

type eventRecorder struct{ evs []tunnel.Event }

func (r *eventRecorder) note(ev tunnel.Event) { r.evs = append(r.evs, ev) }

func freshCollector(t *testing.T) (*SNMPCollector, *eventRecorder) {
	t.Helper()
	var c = NewSNMPCollector("http://example.invalid", "px1", "s3cr3t")
	var rec = &eventRecorder{}
	c.SetEmitter(rec.note)
	c.SetWatches([]tunnel.SNMPWatch{
		{DeviceID: "sw1", IP: "10.0.0.5", Community: "readonly", StateIntervalS: 30},
	})
	return c, rec
}

func TestNotePortStatesSuiteDeCycles(t *testing.T) {
	var c, rec = freshCollector(t)

	// 1st cycle: two ports up. No transition — a change is not announced on the
	// first pass.
	c.notePortStates("sw1", []metricbuf.Sample{
		operSample("1", "GE1", ifOperUp), operSample("2", "GE2", ifOperUp),
	}, triggerPoll, "")
	if len(rec.evs) != 0 {
		t.Fatalf("silent first observation expected, got %+v", rec.evs)
	}

	// 2nd cycle: port 2 goes down, on a poll brought forward by a linkDown.
	c.notePortStates("sw1", []metricbuf.Sample{
		operSample("1", "GE1", ifOperUp), operSample("2", "GE2", ifOperDown),
	}, triggerTrap, "linkDown")
	if len(rec.evs) != 1 {
		t.Fatalf("one transition expected, got %+v", rec.evs)
	}
	var ev = rec.evs[0]
	if ev.Type != tunnel.EventPortDown || ev.IfIndex != 2 || ev.PortName != "GE2" {
		t.Errorf("unexpected event: %+v", ev)
	}
	if ev.DeviceID != "sw1" || ev.IP != "10.0.0.5" {
		t.Errorf("device identity lost: %+v", ev)
	}
	if ev.Trigger != triggerTrap || ev.Detail != "linkDown" {
		t.Errorf("unexpected trigger or detail: %+v", ev)
	}

	// 3rd cycle: partial read (no ifOperStatus). The previous observation is kept,
	// otherwise the next cycle would be a "first observation" and would hide the
	// port coming back up.
	rec.evs = nil
	c.notePortStates("sw1", []metricbuf.Sample{{
		Name:   "meshcraft_if_speed_mbps",
		Labels: map[string]string{"device": "sw1", "ifindex": "2"}, Value: 1000,
	}}, triggerPoll, "")
	if len(rec.evs) != 0 {
		t.Fatalf("partial read: no event expected, got %+v", rec.evs)
	}

	// 4th cycle: port 2 comes back up. This is where the retention above shows.
	c.notePortStates("sw1", []metricbuf.Sample{
		operSample("1", "GE1", ifOperUp), operSample("2", "GE2", ifOperUp),
	}, triggerPoll, "")
	if len(rec.evs) != 1 || rec.evs[0].Type != tunnel.EventPortUp ||
		rec.evs[0].IfIndex != 2 {
		t.Fatalf("port 2 coming back up expected, got %+v", rec.evs)
	}
	if rec.evs[0].Trigger != triggerPoll {
		t.Errorf("trigger expected %q: %+v", triggerPoll, rec.evs[0])
	}

	// 5th cycle: port 2 leaves the table (stack member removed). Silent.
	rec.evs = nil
	c.notePortStates("sw1", []metricbuf.Sample{
		operSample("1", "GE1", ifOperUp),
	}, triggerPoll, "")
	if len(rec.evs) != 0 {
		t.Fatalf("port gone: no event expected, got %+v", rec.evs)
	}

	// 6th cycle: exotic value on port 1 — down, with the value spelled out.
	c.notePortStates("sw1", []metricbuf.Sample{
		operSample("1", "GE1", 4),
	}, triggerPoll, "")
	if len(rec.evs) != 1 || rec.evs[0].Type != tunnel.EventPortDown {
		t.Fatalf("exotic value: a down expected, got %+v", rec.evs)
	}
	if rec.evs[0].Detail != "ifOperStatus=4 (unknown)" {
		t.Errorf("detail expected with the raw value: %+v", rec.evs[0])
	}
}

func TestSetWatchesForgetsRemovedDevices(t *testing.T) {
	var c, rec = freshCollector(t)
	c.notePortStates("sw1", []metricbuf.Sample{operSample("1", "GE1", ifOperUp)},
		triggerPoll, "")
	if len(c.portStates) != 1 {
		t.Fatalf("port state expected for sw1, got %+v", c.portStates)
	}

	c.SetWatches(nil)
	if len(c.portStates) != 0 {
		t.Fatalf("sw1 removed from monitoring must leave the map: %+v",
			c.portStates)
	}
	if len(rec.evs) != 0 {
		t.Errorf("removal from monitoring is not an outage: %+v", rec.evs)
	}

	// Reinstated: we start again from a first observation, so silent, even if the
	// port is now down.
	c.SetWatches([]tunnel.SNMPWatch{{DeviceID: "sw1", IP: "10.0.0.5"}})
	c.notePortStates("sw1", []metricbuf.Sample{operSample("1", "GE1", ifOperDown)},
		triggerPoll, "")
	if len(rec.evs) != 0 {
		t.Errorf("reinstatement: silent first observation expected, got %+v",
			rec.evs)
	}
}

// --- Identity filter ---

func TestClassifyTrap(t *testing.T) {
	var watches = []tunnel.SNMPWatch{
		{DeviceID: "sw1", IP: "10.0.0.5", Community: "readonly"},
		{DeviceID: "sw2", IP: "10.0.0.6"}, // no community: "public" expected
		{DeviceID: "bogus", IP: "not-an-ip"},
	}
	type cas struct {
		name, ip, community string
		device              string
		expected            trapVerdict
	}
	var cases = []cas{
		{name: "matching pair", ip: "10.0.0.5", community: "readonly",
			device: "sw1", expected: trapAccepted},
		{name: "empty community means public", ip: "10.0.0.6", community: "public",
			device: "sw2", expected: trapAccepted},
		{name: "IP in IPv6-mapped form", ip: "::ffff:10.0.0.5", community: "readonly",
			device: "sw1", expected: trapAccepted},
		{name: "unknown IP", ip: "192.168.1.1", community: "readonly",
			expected: trapUnknownSource},
		{name: "wrong community", ip: "10.0.0.5", community: "public",
			expected: trapBadCommunity},
		{name: "community absent (v3)", ip: "10.0.0.5", community: "",
			expected: trapBadCommunity},
		{name: "community absent on a watch without community", ip: "10.0.0.6",
			community: "", expected: trapBadCommunity},
		{name: "unreadable source", ip: "", community: "readonly",
			expected: trapUnknownSource},
	}
	var k cas
	for _, k = range cases {
		t.Run(k.name, func(t *testing.T) {
			var w, verdict = classifyTrap(watches, k.ip, k.community)
			if verdict != k.expected {
				t.Fatalf("verdict %v expected, got %v", k.expected, verdict)
			}
			if w.DeviceID != k.device {
				t.Errorf("device %q expected, got %q", k.device, w.DeviceID)
			}
		})
	}
}

func TestTrapOIDLabel(t *testing.T) {
	var packet = func(oid string) *gosnmp.SnmpPacket {
		return &gosnmp.SnmpPacket{Variables: []gosnmp.SnmpPDU{
			{Name: ".1.3.6.1.2.1.1.3.0", Type: gosnmp.TimeTicks, Value: uint32(42)},
			{Name: ".1.3.6.1.6.3.1.1.4.1.0", Type: gosnmp.ObjectIdentifier, Value: oid},
		}}
	}
	var oid, label = trapOIDLabel(packet(".1.3.6.1.6.3.1.1.5.3"))
	if oid != oidLinkDown || label != "linkDown" {
		t.Errorf("linkDown expected, got %q / %q", oid, label)
	}
	oid, label = trapOIDLabel(packet("1.3.6.1.6.3.1.1.5.4"))
	if oid != oidLinkUp || label != "linkUp" {
		t.Errorf("linkUp expected, got %q / %q", oid, label)
	}
	// A vendor trap remains loggable without being named: we do not build a trap
	// table per product line.
	oid, label = trapOIDLabel(packet("1.3.6.1.4.1.11.2.14.11.5.1.7.1.29.1"))
	if oid == "" || label != "" {
		t.Errorf("vendor trap: OID kept, label empty; got %q / %q",
			oid, label)
	}
	// A v1 trap does not carry snmpTrapOID.0: nothing to say about it, and it is
	// not an error.
	oid, label = trapOIDLabel(&gosnmp.SnmpPacket{})
	if oid != "" || label != "" {
		t.Errorf("packet without snmpTrapOID: %q / %q", oid, label)
	}
}

// --- Throttling ---

var refTime = time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)

func TestTrapGateAbsorbsABurst(t *testing.T) {
	var g = newTrapGate()

	// The first trap opens the window.
	var delay, schedule = g.admit("sw1", "", refTime)
	if !schedule || delay != tunnel.TrapCoalesce {
		t.Fatalf("a poll expected in %v, got %v / %v",
			tunnel.TrapCoalesce, delay, schedule)
	}

	// The next 95 — a 48-port switch rebooting — are absorbed by it.
	var i int
	for i = 0; i < 95; i++ {
		var d, s = g.admit("sw1", "", refTime.Add(time.Duration(i)*time.Millisecond))
		if s {
			t.Fatalf("trap %d: a second poll scheduled in %v", i, d)
		}
	}

	// Another device is not throttled by this one: throttling is PER device,
	// otherwise a chatty switch would mask its neighbour's outage.
	var _, other = g.admit("sw2", "", refTime)
	if !other {
		t.Error("sw2 must get its own poll")
	}

	// The burst adopted the first usable label, not the last empty one.
	g.admit("sw1", "linkDown", refTime.Add(10*time.Millisecond))
	if g.release("sw1", refTime.Add(tunnel.TrapCoalesce)) != "linkDown" {
		t.Error("the burst label must reach the poll")
	}
	if len(g.pending) != 1 || !g.pending["sw2"] {
		t.Errorf("sw1's window must be closed: %+v", g.pending)
	}
}

func TestTrapGateHonoursTheFloor(t *testing.T) {
	var g = newTrapGate()
	g.admit("sw1", "", refTime)
	var poll = refTime.Add(tunnel.TrapCoalesce)
	g.release("sw1", poll)

	// A trap right after the poll: DELAYED to the floor, not discarded — dropping
	// it would lose a real transition until the next ordinary cycle.
	var arrival = poll.Add(10 * time.Millisecond)
	var delay, schedule = g.admit("sw1", "", arrival)
	if !schedule {
		t.Fatal("the trap must be scheduled, not dropped")
	}
	var expected = poll.Add(tunnel.TrapMinPollGap).Sub(arrival)
	if delay != expected {
		t.Fatalf("delay %v expected (floor), got %v", expected, delay)
	}
	if delay < tunnel.TrapCoalesce {
		t.Fatalf("the floor cannot shorten the window: %v", delay)
	}

	// Well after the floor, we fall back to simple coalescing.
	g.release("sw1", arrival.Add(delay))
	var late = arrival.Add(delay).Add(10 * tunnel.TrapMinPollGap)
	delay, schedule = g.admit("sw1", "", late)
	if !schedule || delay != tunnel.TrapCoalesce {
		t.Fatalf("simple coalescing expected, got %v / %v", delay, schedule)
	}
}

func TestTrapGateForgetsRemovedDevices(t *testing.T) {
	var g = newTrapGate()
	g.admit("sw1", "linkUp", refTime)
	g.admit("sw2", "", refTime)
	g.release("sw2", refTime)
	g.firstSeen("sw1")
	g.firstSeen("sw2")

	g.forget(map[string]bool{"sw2": true})
	if len(g.pending) != 0 || len(g.label) != 0 {
		t.Errorf("sw1 must be forgotten: %+v / %+v", g.pending, g.label)
	}
	if len(g.lastPoll) != 1 {
		t.Errorf("sw2 stays watched, its floor must hold: %+v", g.lastPoll)
	}
	// The return-path proof follows the same rule: it re-arms for sw1, it holds for
	// sw2 — otherwise a device still watched would re-prove its path on every
	// WatchConfig received.
	if len(g.seen) != 1 || !g.seen["sw2"] {
		t.Errorf("proof: sw1 re-armed, sw2 kept; got %+v", g.seen)
	}
	if !g.firstSeen("sw1") {
		t.Error("reinstated sw1 must be able to re-prove its return path")
	}
	if g.firstSeen("sw2") {
		t.Error("sw2 never left monitoring: nothing to re-prove")
	}
}

// --- Return-path proof (tunnel.EventTrapSeen) ---

// trapPacket builds a v2c trap as gosnmp hands it to onTrap: the community at
// packet level, the trap OID in `snmpTrapOID.0`.
func trapPacket(oid, community string) *gosnmp.SnmpPacket {
	return &gosnmp.SnmpPacket{
		Version: gosnmp.Version2c, Community: community,
		Variables: []gosnmp.SnmpPDU{
			{Name: ".1.3.6.1.2.1.1.3.0", Type: gosnmp.TimeTicks, Value: uint32(7)},
			{Name: ".1.3.6.1.6.3.1.1.4.1.0", Type: gosnmp.ObjectIdentifier, Value: oid},
		},
	}
}

func sourceUDP(ip string) *net.UDPAddr {
	return &net.UDPAddr{IP: net.ParseIP(ip), Port: 41234}
}

// localTarget: the local address the datagram was sent to, as the kernel returns
// it on read. It is the lab proxy's, the very one the controller writes into
// devices — only the controller knows how to compare it.
var localTarget = netip.MustParseAddr("10.42.0.67")

// defuse cancels the poll an accepted trap scheduled, without waiting its 250 ms.
//
// trapPoll re-reads the watch list when it leaves and gives up when the target
// is no longer there (that is what makes it safe against a WatchConfig changing
// during the window). Emptying the list is therefore enough to guarantee none of
// these tests opens a socket towards a documentation IP after its own end. Call
// it AFTER the checks: the purge re-arms the proof.
func defuse(c *SNMPCollector) { c.SetWatches(nil) }

func TestOnTrapProvesTheReturnPathOnlyOnce(t *testing.T) {
	var c, rec = freshCollector(t)
	defer defuse(c)

	c.onTrap(trapPacket(oidLinkDown, "readonly"), sourceUDP("10.0.0.5"), localTarget)
	if len(rec.evs) != 1 {
		t.Fatalf("a proof event expected, got %+v", rec.evs)
	}
	var ev = rec.evs[0]
	if ev.Type != tunnel.EventTrapSeen {
		t.Errorf("type %q expected, got %+v", tunnel.EventTrapSeen, ev)
	}
	if ev.DeviceID != "sw1" || ev.IP != "10.0.0.5" {
		t.Errorf("the proof must name the device and its address: %+v", ev)
	}
	if ev.Detail != "linkDown" {
		t.Errorf("detail expected %q, got %q", "linkDown", ev.Detail)
	}

	// Second trap of the same burst: ABSORBED by the grouping window, hence no poll
	// — and no proof either, since it is already acquired. This case is the more
	// important of the two: it checks that the emission point depends on the
	// identity filter and not on scheduling.
	c.onTrap(trapPacket(oidLinkUp, "readonly"), sourceUDP("10.0.0.5"), localTarget)
	if len(rec.evs) != 1 {
		t.Fatalf("the proof does not repeat, got %+v", rec.evs)
	}
	c.mu.Lock()
	var stats = c.trapStats
	c.mu.Unlock()
	if stats.received != 2 || stats.accepted != 1 || stats.absorbed != 1 {
		t.Errorf("the second trap should have been absorbed, not recounted: %+v", stats)
	}
}

// TestOnTrapProvesFromAnAbsorbedTrap: a device's very first trap may fall into
// an already open window — a poll per trap having been scheduled for it the
// previous millisecond. It still went through the same path, and without this
// the device would stay "address applied, path not verified" in the UI until
// its next burst.
func TestOnTrapProvesFromAnAbsorbedTrap(t *testing.T) {
	var c, rec = freshCollector(t)

	// Window opened by hand: no timer starts, so nothing to cancel.
	var _, open = c.trapGate.admit("sw1", "linkDown", time.Now())
	if !open {
		t.Fatal("the window should have opened")
	}

	c.onTrap(trapPacket(oidLinkUp, "readonly"), sourceUDP("10.0.0.5"), localTarget)
	if len(rec.evs) != 1 || rec.evs[0].Type != tunnel.EventTrapSeen {
		t.Fatalf("an absorbed trap proves the path as much as any other: %+v", rec.evs)
	}
	if rec.evs[0].Detail == "" {
		t.Error("an empty Detail would read as a field lost on the way")
	}
}

// TestOnTrapProvesNothingFromAnUnauthorisedSource: the proof must not be
// FORGEABLE. A v2c trap is unauthenticated UDP: if a third party on the path
// could make the controller say "sw1's return path is established", the event
// would prove nothing at all any more.
func TestOnTrapProvesNothingFromAnUnauthorisedSource(t *testing.T) {
	type cas struct {
		name, ip, community string
	}
	var cases = []cas{
		{name: "unknown source", ip: "192.168.1.1", community: "readonly"},
		{name: "unexpected community", ip: "10.0.0.5", community: "public"},
		{name: "community absent (v3 trap)", ip: "10.0.0.5", community: ""},
		{name: "unreadable source", ip: "", community: "readonly"},
	}
	var k cas
	for _, k = range cases {
		t.Run(k.name, func(t *testing.T) {
			var c, rec = freshCollector(t)
			c.onTrap(trapPacket(oidLinkDown, k.community), sourceUDP(k.ip), localTarget)
			if len(rec.evs) != 0 {
				t.Fatalf("no event expected, got %+v", rec.evs)
			}
			// And the proof stays available: a rejected trap must not CONSUME the right to
			// prove later either, otherwise a spoofer would be enough to silence the device
			// forever.
			if !c.trapGate.firstSeen("sw1") {
				t.Error("a rejected trap must not consume sw1's proof")
			}
		})
	}
}

func TestOnTrapReprovesAfterARemovalFromMonitoring(t *testing.T) {
	var c, rec = freshCollector(t)
	var watch = []tunnel.SNMPWatch{
		{DeviceID: "sw1", IP: "10.0.0.5", Community: "readonly", StateIntervalS: 30},
	}

	c.onTrap(trapPacket(oidLinkDown, "readonly"), sourceUDP("10.0.0.5"), localTarget)
	if len(rec.evs) != 1 {
		t.Fatalf("initial proof expected, got %+v", rec.evs)
	}

	// Removed from monitoring: silent purge, as for port states.
	rec.evs = nil
	c.SetWatches(nil)
	if len(rec.evs) != 0 {
		t.Fatalf("a removal from monitoring emits nothing: %+v", rec.evs)
	}

	// Reinstated: the firewall, the route or the ACL may have changed in the
	// meantime, so yesterday's proof commits to nothing — it is redone, ONCE.
	c.SetWatches(watch)
	defer defuse(c)
	c.onTrap(trapPacket(oidLinkDown, "readonly"), sourceUDP("10.0.0.5"), localTarget)
	c.onTrap(trapPacket(oidLinkDown, "readonly"), sourceUDP("10.0.0.5"), localTarget)
	if len(rec.evs) != 1 || rec.evs[0].Type != tunnel.EventTrapSeen {
		t.Fatalf("a single proof after reinstatement, got %+v", rec.evs)
	}
}

// TestOnTrapDoesNotDiscloseTheCommunity: the community is an operating secret. The
// event goes to the controller, is stored there, then displayed — checking the
// SERIALISED form rather than field by field keeps the test true the day
// tunnel.Event gains a field.
func TestOnTrapDoesNotDiscloseTheCommunity(t *testing.T) {
	const community = "s3cr3t-de-labo"
	var c = NewSNMPCollector("http://example.invalid", "px1", "s3cr3t")
	var rec = &eventRecorder{}
	c.SetEmitter(rec.note)
	c.SetWatches([]tunnel.SNMPWatch{
		{DeviceID: "sw1", IP: "10.0.0.5", Community: community, StateIntervalS: 30},
	})
	defer defuse(c)

	c.onTrap(trapPacket(oidLinkUp, community), sourceUDP("10.0.0.5"), localTarget)
	if len(rec.evs) != 1 {
		t.Fatalf("a proof event expected, got %+v", rec.evs)
	}
	var rawJSON, err = json.Marshal(rec.evs[0])
	if err != nil {
		t.Fatalf("serialising the event: %v", err)
	}
	if strings.Contains(string(rawJSON), community) {
		t.Errorf("the community has no business in an event: %s", rawJSON)
	}
}

// --- Proof attribution: the address TARGETED by the datagram ---

// TestOnTrapCarriesTheTargetedAddress: the proof must say AT WHICH ADDRESS the trap
// arrived, otherwise it does not prove what the controller makes it say.
//
// The proxy's filter only examines the SOURCE IP and the community; its socket
// listens on the wildcard address and accepts everything reaching 162/udp. A
// device emitting towards another collector on the same machine would therefore
// fabricate a "proof" of the address meshcraft just wrote, entirely unrelated.
// Only the targeted local address closes that gap, and it can only come from
// here: the controller does not see the datagrams.
func TestOnTrapCarriesTheTargetedAddress(t *testing.T) {
	var c, rec = freshCollector(t)
	defer defuse(c)

	c.onTrap(trapPacket(oidLinkDown, "readonly"), sourceUDP("10.0.0.5"), localTarget)
	if len(rec.evs) != 1 {
		t.Fatalf("a proof event expected, got %+v", rec.evs)
	}
	if rec.evs[0].DestAddr != "10.42.0.67" {
		t.Errorf("the proof must carry the targeted address, got %q", rec.evs[0].DestAddr)
	}
}

// TestOnTrapDoesNotInventTheTargetedAddress: when the kernel did not give the
// destination, the field stays EMPTY.
//
// The tempting fallback would be to put there the address through which the
// proxy reaches the device — the one it returned to `AskSourceAddr`, hence the
// one the controller wrote. That would make the check tautological: it would
// always confirm, since it would compare the claim with itself. An "I don't
// know" must cross the tunnel as is, and it is the controller that decides what
// an unattributed proof is worth.
func TestOnTrapDoesNotInventTheTargetedAddress(t *testing.T) {
	var c, rec = freshCollector(t)
	defer defuse(c)

	c.onTrap(trapPacket(oidLinkDown, "readonly"), sourceUDP("10.0.0.5"), netip.Addr{})
	if len(rec.evs) != 1 {
		t.Fatalf("a proof event expected, got %+v", rec.evs)
	}
	if rec.evs[0].DestAddr != "" {
		t.Errorf("an unknown destination is not invented, got %q",
			rec.evs[0].DestAddr)
	}
}

// TestTrapDstAddrNormalises: what the kernel returns is not comparable as is.
func TestTrapDstAddrNormalises(t *testing.T) {
	// Dual-stack socket: the kernel returns an IPv4 datagram's destination in mapped
	// form. Comparing "::ffff:10.42.0.67" with the controller's inventory, which
	// writes "10.42.0.67", would make attribution fail depending on the mood of the
	// host's stack — the same precaution as classifyTrap.
	var got = trapDstAddr(net.ParseIP("::ffff:10.42.0.67"))
	if got.String() != "10.42.0.67" {
		t.Errorf("mapped address not unmapped: %q", got.String())
	}
	// Nothing to say, and that must BE SAID: an absent or wildcard address is not a
	// wrong address, it is an absence of answer. The controller derives an
	// unattributed proof from it, not a refusal.
	if trapDstAddr(nil).IsValid() {
		t.Error("an absent destination must be invalid")
	}
	if trapDstAddr(net.ParseIP("0.0.0.0")).IsValid() {
		t.Error("the wildcard address designates nothing and must not be returned")
	}
	if trapDstAddr(net.ParseIP("::")).IsValid() {
		t.Error("the v6 wildcard address designates nothing and must not be returned")
	}
}

// safeEventRecorder: the same recorder, but locked.
//
// Needed for the end-to-end test and for it alone: the event arrives there on
// the socket's read goroutine, whereas all the other tests call `onTrap`
// themselves. A bare `eventRecorder` would be a data race there (and `-race` would
// say so).
type safeEventRecorder struct {
	mu  sync.Mutex
	evs []tunnel.Event
}

func (r *safeEventRecorder) note(ev tunnel.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.evs = append(r.evs, ev)
}

// awaitEvent returns the first collected event, or gives up. A trap is UDP over
// loopback: if it has not arrived within a second, it will not.
func (r *safeEventRecorder) awaitEvent(t *testing.T) tunnel.Event {
	t.Helper()
	var fin = time.Now().Add(time.Second)
	for time.Now().Before(fin) {
		r.mu.Lock()
		var n = len(r.evs)
		var first tunnel.Event
		if n > 0 {
			first = r.evs[0]
		}
		r.mu.Unlock()
		if n > 0 {
			return first
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("no proof event received by the trap socket")
	return tunnel.Event{}
}

// TestTrapSocketReadsTheTargetedAddress: the collector's END-TO-END test, and the
// only one that checks what the kernel is willing to say.
//
// It sends a real v2c trap on a real socket and waits for the proof at the
// other end. That is what makes this batch's decision defensible: reading the
// destination address (PKTINFO) is not logic that can be simulated — either the
// kernel gives it, or it does not. If it did not, the channel would keep working
// but every proof would be unattributed, and that must be KNOWN here rather
// than discovered on a customer's fleet.
//
// It checks at the same time that replacing `gosnmp.TrapListener` with our
// socket broke nothing in decoding: the packet is built and sent by gosnmp, and
// it is `gosnmp.UnmarshalTrap` — exported, hence kept — that reads it back.
func TestTrapSocketReadsTheTargetedAddress(t *testing.T) {
	var c = NewSNMPCollector("http://example.invalid", "px1", "s3cr3t")
	var rec = &safeEventRecorder{}
	c.SetEmitter(rec.note)
	// Loopback as the watched device: it is the only address a test can be both
	// the source and the destination of.
	c.SetWatches([]tunnel.SNMPWatch{
		{DeviceID: "sw1", IP: "127.0.0.1", Community: "readonly", StateIntervalS: 30},
	})
	defer defuse(c)

	// Port 0: the kernel picks a free one. Asking for 162 would require a privilege
	// no test container has, and the port teaches nothing here.
	var rx, err = startTrapReceiver(0, c.onTrap)
	if err != nil {
		t.Fatalf("trap socket not bound: %v", err)
	}
	defer rx.stop()
	if !rx.dstKnown() {
		t.Fatal("this kernel does not return the datagrams' destination address: " +
			"every return-path proof would be unattributed")
	}

	var g = &gosnmp.GoSNMP{
		Target: "127.0.0.1", Port: uint16(rx.bound), Transport: "udp", //nolint:gosec
		Community: "readonly", Version: gosnmp.Version2c,
		Timeout: time.Second, Retries: 0,
	}
	if err = g.Connect(); err != nil {
		t.Fatalf("trap sender connection: %v", err)
	}
	defer g.Conn.Close() //nolint:errcheck
	if _, err = g.SendTrap(gosnmp.SnmpTrap{Variables: []gosnmp.SnmpPDU{
		{Name: oidSnmpTrapOID, Type: gosnmp.ObjectIdentifier, Value: oidLinkDown},
	}}); err != nil {
		t.Fatalf("sending the trap: %v", err)
	}

	var ev = rec.awaitEvent(t)
	if ev.Type != tunnel.EventTrapSeen || ev.DeviceID != "sw1" {
		t.Fatalf("return-path proof expected, got %+v", ev)
	}
	if ev.DestAddr != "127.0.0.1" {
		t.Errorf("the trap was sent to 127.0.0.1, the proof says %q", ev.DestAddr)
	}
	if ev.Detail != "linkDown" {
		t.Errorf("the trap must be decoded down to its OID, Detail %q", ev.Detail)
	}
}

// TestSocketShutdownIsNotAnIncident: closing the socket makes the pending read
// fail, and that error must not declare itself "dead".
//
// It is the counterpart of the anti-silent-death guard: a read that returns on
// an unexpected error marks the socket, and reconcileTraps binds a fresh one.
// Mistaking the REQUESTED shutdown for an incident would therefore rebind a
// socket right after every voluntary shutdown — i.e. every time the controller
// removes trap listening from the monitoring configuration.
func TestSocketShutdownIsNotAnIncident(t *testing.T) {
	var rx, err = startTrapReceiver(0, func(*gosnmp.SnmpPacket, *net.UDPAddr, netip.Addr) {})
	if err != nil {
		t.Fatalf("trap socket not bound: %v", err)
	}
	rx.stop()
	// The loop leaves its blocking read on close; a few milliseconds are enough,
	// and a plain wait beats a test that depends on the scheduler.
	time.Sleep(50 * time.Millisecond)
	if rx.dead.Load() {
		t.Error("a requested shutdown must not read as a dead read")
	}
}

func TestTrapSeenDetail(t *testing.T) {
	if trapSeenDetail(oidLinkUp, "linkUp") != "linkUp" {
		t.Error("the recognised label goes before the OID")
	}
	// Vendor trap: no name, but the OID locates the product line — "it emits, but
	// no linkUp/linkDown" is not fixed like a complete silence.
	var got = trapSeenDetail("1.3.6.1.4.1.11.2.14.11.5.1.7.1.29.1", "")
	if got != "oid=1.3.6.1.4.1.11.2.14.11.5.1.7.1.29.1" {
		t.Errorf("raw OID expected, got %q", got)
	}
	// v1 trap: no snmpTrapOID.0 to quote, and the Detail must not be empty for all
	// that.
	if trapSeenDetail("", "") == "" {
		t.Error("an empty Detail would read as a field lost on the way")
	}
}

func TestOperStatusLabel(t *testing.T) {
	// "up" is reserved for 1, and for it alone: that is the decision governing the
	// rest of the file.
	var status int
	for status = 2; status <= 7; status++ {
		if portUp(status) {
			t.Errorf("ifOperStatus=%d must not count as up", status)
		}
	}
	if !portUp(ifOperUp) {
		t.Error("ifOperStatus=1 must count as up")
	}
	if operStatusLabel(7) != "lowerLayerDown" || operStatusLabel(42) != "unknown" {
		t.Errorf("unexpected labels: %q / %q", operStatusLabel(7), operStatusLabel(42))
	}
}
