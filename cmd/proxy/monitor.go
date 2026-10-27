package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"os"
	"sort"
	"sync"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"

	"meshcraft/internal/discovery"
	"meshcraft/internal/tunnel"
)

// Local monitoring: it's the PROXY that pings.
//
// The controller sends it the list of devices to watch and the ranges to
// sweep; the proxy pings continuously and only reports **transitions**. A
// round trip per second per device across the WAN would deliver the same
// information at a wildly disproportionate cost — and the proxy is the local
// executor by design (docs/en/01-Architecture/Proxy.md).
//
// ICMP needs a raw socket, hence CAP_NET_RAW: already granted by the
// generated docker-compose as well as by the systemd unit. Without it,
// monitoring cleanly disables itself while saying so, rather than pretending
// to work.

// Monitor holds the proxy's monitoring state.
type Monitor struct {
	mu       sync.Mutex
	cfg      tunnel.WatchConfig
	states   map[string]*targetState // key: IP
	events   chan tunnel.Event
	sock     *icmp.PacketConn
	echoID   int
	seq      int
	pending  map[string]time.Time // IP -> timestamp the last echo was sent
	sweeping bool
}

type targetState struct {
	deviceID string
	name     string
	// up: last KNOWN state. nil = no verdict yet.
	//
	// This nil is NOT silent, and the comment that used to live here claimed
	// the opposite ("avoids announcing a transition on the very first ping"):
	// both `onReply` and `evaluate` treat a nil as a flip and announce it,
	// which is the intended behavior — a device that has just been added to
	// monitoring must produce a first verdict, otherwise its status would
	// stay `unknown` until its first outage. This is exactly the invariant
	// used to reason about a missing status, so it was worth stating
	// correctly.
	//
	// What a nil does avoid is something else: concluding before having
	// pinged. A target that hasn't been polled yet is neither online nor
	// unreachable, and `Snapshot` skips it for that reason.
	up     *bool
	misses int
}

func NewMonitor() *Monitor {
	return &Monitor{
		states:  map[string]*targetState{},
		events:  make(chan tunnel.Event, 256),
		pending: map[string]time.Time{},
		echoID:  os.Getpid() & 0xffff,
	}
}

// Events is the stream to push to the controller.
func (m *Monitor) Events() <-chan tunnel.Event { return m.events }

// emit enqueues an event without ever blocking: an unreachable controller
// must not freeze monitoring.
func (m *Monitor) emit(ev tunnel.Event) {
	ev.TS = time.Now().UTC()
	select {
	case m.events <- ev:
	default:
		slog.Warn("event queue full — event dropped", "type", ev.Type)
	}
}

// SetConfig replaces the monitoring configuration. A full config replaces
// the previous one: nothing to reconcile, so no divergence is possible.
func (m *Monitor) SetConfig(cfg tunnel.WatchConfig) {
	// SNMP collection follows the same config: a single source of truth on
	// the controller side, so no divergence is possible between what is
	// pinged and what is polled.
	if snmpCollector != nil {
		snmpCollector.SetWatches(cfg.SNMP)
	}
	// Streaming telemetry follows the same config, for the same reason. The two
	// arrays are EXCLUSIVE per device — the controller decides which protocol
	// carries a device and zeroes the other side (cf. tunnel.WatchConfig.GNMI) —
	// so this is not a second opinion on the same targets, it is the other half
	// of one list.
	if gnmiCollector != nil {
		gnmiCollector.SetWatches(context.Background(), cfg.GNMI)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cfg = cfg
	var seen = map[string]bool{}
	var t tunnel.WatchTarget
	for _, t = range cfg.Targets {
		if t.IP == "" {
			continue
		}
		seen[t.IP] = true
		var st, exists = m.states[t.IP]
		if !exists {
			st = &targetState{}
			m.states[t.IP] = st
		}
		// A DIFFERENT DEVICE AT THE SAME ADDRESS IS A NEW TARGET, and forgetting
		// that cost a device its status for good.
		//
		// This map is keyed by IP, and re-using the entry is right as long as the
		// address describes the same thing. It stops being right when the DEVICE
		// changes underneath: `up` is then carried over from the previous
		// occupant, the ping that follows agrees with it, no flip is announced —
		// and the controller, which has never heard of the new device id, leaves
		// it `unknown` for ever. The `up == nil` above exists precisely to
		// guarantee "a device that has just been added to monitoring produces a
		// first verdict", and recycling the entry defeated it.
		//
		// MEASURED on 2026-08-12: a CRS305 was re-adopted at 198.51.100.100 after
		// the first device record was deleted. SNMP polled it, its port rates and
		// spanning tree were live all along, and its reachability read `unknown`
		// for six hours — until the next reconnection would have replayed a
		// Snapshot. Same shape as the CSS326 the Snapshot comment describes, by
		// another route.
		//
		// Resetting the verdict, not the entry: `misses` goes with it, and the
		// next poll announces whatever it finds.
		if st.deviceID != "" && st.deviceID != t.DeviceID {
			st.up, st.misses = nil, 0
		}
		st.deviceID, st.name = t.DeviceID, t.Name
	}
	// A target removed from the list stops being monitored, with no event:
	// this isn't an outage, it's an operational decision.
	var ip string
	for ip = range m.states {
		if !seen[ip] {
			delete(m.states, ip)
		}
	}
	slog.Info("monitoring configured", "devices", len(cfg.Targets),
		"prefixes", len(cfg.Prefixes))
}

// Snapshot returns the CURRENT state of every monitored target, as events
// marked `Resync`.
//
// WHY THIS ISN'T REDUNDANT WITH THE TRANSITIONS. The proxy only reports
// flips, and a lost flip is never replayed: the queue of 256 entries drops
// events once full, the event being written is lost if the tunnel goes down
// at that exact instant, and a reconnection re-announces nothing since
// `SetConfig` keeps in-memory state for targets already known. Observed on a
// CSS326 that stayed `unknown` for nineteen hours while it was responding to
// pings flawlessly: its one transition had vanished in an outage, and only a
// real failure — a manual reboot — eventually rewrote its status. The
// snapshot closes this gap by construction, without changing anything about
// steady-state behavior: it only fires when the stream opens.
//
// What gets SKIPPED, and why:
//
//   - a target with no verdict (`up == nil`) has nothing to say. It hasn't
//     been pinged yet, and announcing "online" or "unreachable" would be
//     inventing a finding nobody made;
//   - a target with no `deviceID` doesn't correspond to any device known to
//     the controller, which would ignore it anyway (see applyDeviceState).
//
// The order is STABLE, by address. Two snapshots of the same state must
// produce the same byte sequence: this is what makes a reconnection stream
// comparable from one time to the next, and a test reproducible.
func (m *Monitor) Snapshot() []tunnel.Event {
	m.mu.Lock()
	var ips = make([]string, 0, len(m.states))
	var ip string
	for ip = range m.states {
		ips = append(ips, ip)
	}
	sort.Strings(ips)

	var now = time.Now().UTC()
	var out = make([]tunnel.Event, 0, len(ips))
	for _, ip = range ips {
		var st = m.states[ip]
		if st.up == nil || st.deviceID == "" {
			continue
		}
		var ev = tunnel.Event{
			Type: tunnel.EventDeviceUp, TS: now, DeviceID: st.deviceID, IP: ip,
			Detail: st.name, Resync: true,
		}
		if !*st.up {
			ev.Type = tunnel.EventDeviceDown
			ev.Misses = st.misses
		}
		out = append(out, ev)
	}
	m.mu.Unlock()
	return out
}

// Run starts monitoring. Only returns when the context is done.
func (m *Monitor) Run(ctx context.Context) {
	var conn, err = icmp.ListenPacket("ip4:icmp", "0.0.0.0")
	if err != nil {
		// Without CAP_NET_RAW, we say so clearly instead of staying silent.
		slog.Error("ICMP monitoring unavailable: raw socket refused "+
			"(missing CAP_NET_RAW?) — device state will not be tracked",
			"err", err)
		return
	}
	m.sock = conn
	defer conn.Close() //nolint:errcheck
	slog.Info("ICMP monitoring active")

	go m.readReplies(ctx)
	go m.sweepLoop(ctx)

	var interval = m.pingInterval()
	var ticker = time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.pingRound()
		}
	}
}

func (m *Monitor) pingInterval() time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cfg.PingIntervalMS > 0 {
		return time.Duration(m.cfg.PingIntervalMS) * time.Millisecond
	}
	return tunnel.DefaultPingInterval
}

func (m *Monitor) downAfter() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cfg.DownAfter > 0 {
		return m.cfg.DownAfter
	}
	return tunnel.DefaultDownAfter
}

// pingRound sends an echo to each target, then judges those that haven't
// responded for long enough.
func (m *Monitor) pingRound() {
	m.mu.Lock()
	var targets = make([]string, 0, len(m.states))
	var ip string
	for ip = range m.states {
		targets = append(targets, ip)
	}
	m.mu.Unlock()

	for _, ip = range targets {
		m.sendEcho(ip)
	}

	// We leave time for replies to arrive before concluding.
	time.AfterFunc(m.pingInterval()*3/4, m.evaluate)
}

func (m *Monitor) sendEcho(ip string) {
	var addr = net.ParseIP(ip)
	if addr == nil || addr.To4() == nil {
		return // IPv6: to be handled with ip6:ipv6-icmp, not yet shipped
	}
	m.mu.Lock()
	m.seq = (m.seq + 1) & 0xffff
	var seq = m.seq
	m.pending[ip] = time.Now()
	m.mu.Unlock()

	var msg = icmp.Message{
		Type: ipv4.ICMPTypeEcho,
		Body: &icmp.Echo{ID: m.echoID, Seq: seq, Data: []byte("meshcraft")},
	}
	var data, err = msg.Marshal(nil)
	if err != nil {
		return
	}
	_, _ = m.sock.WriteTo(data, &net.IPAddr{IP: addr.To4()})
}

// readReplies continuously reads replies. A single socket for the whole
// fleet: monitoring a hundred devices costs a hundred packets per second,
// not a hundred sockets.
func (m *Monitor) readReplies(ctx context.Context) {
	var buf = make([]byte, 1500)
	for {
		if ctx.Err() != nil {
			return
		}
		_ = m.sock.SetReadDeadline(time.Now().Add(2 * time.Second))
		var n, peer, err = m.sock.ReadFrom(buf)
		if err != nil {
			continue // read timeout: normal
		}
		var msg, parseErr = icmp.ParseMessage(1, buf[:n])
		if parseErr != nil || msg.Type != ipv4.ICMPTypeEchoReply {
			continue
		}
		var echo, ok = msg.Body.(*icmp.Echo)
		if !ok || echo.ID != m.echoID {
			continue // another process's reply
		}
		m.onReply(peer.String())
	}
}

func (m *Monitor) onReply(ip string) {
	m.mu.Lock()
	var sentAt, wasPending = m.pending[ip]
	delete(m.pending, ip)
	var st, watched = m.states[ip]
	var latency int
	if wasPending {
		latency = int(time.Since(sentAt).Milliseconds())
	}
	if !watched {
		// Reply from an address we're sweeping, not from a tracked device.
		m.mu.Unlock()
		m.noteSweepHit(ip)
		return
	}
	var wasUp = st.up
	st.misses = 0
	var up = true
	st.up = &up
	var deviceID, name = st.deviceID, st.name
	m.mu.Unlock()

	// TRANSITION only: a device that's been fine for an hour has nothing to
	// say every second.
	if wasUp == nil || !*wasUp {
		m.emit(tunnel.Event{
			Type: tunnel.EventDeviceUp, DeviceID: deviceID, IP: ip,
			LatencyMS: latency, Detail: name,
		})
	}
}

// evaluate declares unreachable the targets that have been silent for
// enough rounds.
func (m *Monitor) evaluate() {
	var limit = m.downAfter()
	type fallen struct {
		deviceID, ip, name string
		misses             int
	}
	var down []fallen

	m.mu.Lock()
	var ip string
	var st *targetState
	for ip, st = range m.states {
		var _, stillPending = m.pending[ip]
		if !stillPending {
			continue // a reply arrived
		}
		delete(m.pending, ip)
		st.misses++
		if st.misses < limit {
			continue
		}
		if st.up != nil && !*st.up {
			continue // already announced
		}
		var no = false
		st.up = &no
		down = append(down, fallen{st.deviceID, ip, st.name, st.misses})
	}
	m.mu.Unlock()

	var f fallen
	for _, f = range down {
		m.emit(tunnel.Event{
			Type: tunnel.EventDeviceDown, DeviceID: f.deviceID, IP: f.ip,
			Misses: f.misses, Detail: f.name,
		})
	}
}

// --- Continuous sweeping of local ranges ---

var sweepHits = struct {
	sync.Mutex
	seen map[string]bool
}{seen: map[string]bool{}}

func (m *Monitor) noteSweepHit(ip string) {
	sweepHits.Lock()
	defer sweepHits.Unlock()
	if sweepHits.seen == nil {
		sweepHits.seen = map[string]bool{}
	}
	sweepHits.seen[ip] = true
}

// sweepLoop continuously sweeps the ranges entrusted to the proxy. The pace
// is slow by design: discovering a new device isn't urgent, and a
// continuous sweep at full speed would look like an attack.
func (m *Monitor) sweepLoop(ctx context.Context) {
	var period = tunnel.DefaultSweepInterval
	m.mu.Lock()
	if m.cfg.SweepIntervalS > 0 {
		period = time.Duration(m.cfg.SweepIntervalS) * time.Second
	}
	m.mu.Unlock()

	var ticker = time.NewTicker(period)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.sweepOnce(ctx)
		}
	}
}

func (m *Monitor) sweepOnce(ctx context.Context) {
	m.mu.Lock()
	if m.sweeping {
		m.mu.Unlock()
		return
	}
	m.sweeping = true
	var prefixes = append([]string{}, m.cfg.Prefixes...)
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		m.sweeping = false
		m.mu.Unlock()
	}()

	var prefix string
	for _, prefix = range prefixes {
		if ctx.Err() != nil {
			return
		}
		var addrs, err = discovery.ExpandPrefix(prefix)
		if err != nil {
			m.emit(tunnel.Event{Type: tunnel.EventSweepDone, Prefix: prefix,
				Detail: "cannot enumerate: " + err.Error()})
			continue
		}
		sweepHits.Lock()
		sweepHits.seen = map[string]bool{}
		sweepHits.Unlock()

		// Controlled pace: 200 echoes/s is enough to cover a /24 in one
		// second and a /16 in five minutes, without saturating a slow link.
		var ip string
		var sent int
		for _, ip = range addrs {
			if ctx.Err() != nil {
				return
			}
			m.sendEcho(ip)
			sent++
			if sent%200 == 0 {
				time.Sleep(time.Second)
			}
		}
		// Let the last replies come in.
		time.Sleep(2 * time.Second)

		sweepHits.Lock()
		var found = make([]string, 0, len(sweepHits.seen))
		for ip = range sweepHits.seen {
			found = append(found, ip)
		}
		sweepHits.Unlock()

		for _, ip = range found {
			m.emit(tunnel.Event{Type: tunnel.EventHostSeen, IP: ip, Prefix: prefix})
		}
		m.emit(tunnel.Event{Type: tunnel.EventSweepDone, Prefix: prefix,
			Scanned: len(addrs), Found: len(found)})
	}
}

// --- Wiring with the tunnel ---

// handleWatchStream reads the monitoring configurations pushed by the
// controller over a dedicated stream.
func (m *Monitor) handleWatchStream(dec *json.Decoder) error {
	for {
		var cfg tunnel.WatchConfig
		var err = dec.Decode(&cfg)
		if err != nil {
			return err
		}
		m.SetConfig(cfg)
	}
}
