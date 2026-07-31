package tunnel

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/yamux"
)

// PROXY side: accept streams and open requested connections.
//
// The proxy never chooses its targets -- it executes what the controller requests. That
// is assumed in docs/03-Security/Proxy security ("installing the proxy = trusting the
// controller operator"), with a guardrail that stays local: the CIDR allowlist, configured
// on the proxy machine and therefore out of controller reach.

// Allowlist bounds destinations the proxy accepts dialing.
// Empty = no restriction.
type Allowlist struct {
	prefixes []netip.Prefix
}

// ParseAllowlist reads a comma-separated list of CIDRs.
func ParseAllowlist(s string) (*Allowlist, error) {
	var a = &Allowlist{}
	var part string
	for _, part = range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		var p, err = netip.ParsePrefix(part)
		if err != nil {
			return nil, fmt.Errorf("invalid CIDR %q: %w", part, err)
		}
		a.prefixes = append(a.prefixes, p)
	}
	return a, nil
}

func (a *Allowlist) Empty() bool { return a == nil || len(a.prefixes) == 0 }

func (a *Allowlist) String() string {
	if a.Empty() {
		return "all destinations"
	}
	var out = make([]string, 0, len(a.prefixes))
	var p netip.Prefix
	for _, p = range a.prefixes {
		out = append(out, p.String())
	}
	return strings.Join(out, ",")
}

func (a *Allowlist) permits(ip netip.Addr) bool {
	if a.Empty() {
		return true
	}
	var p netip.Prefix
	for _, p = range a.prefixes {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// WatchHandler processes the watch configuration stream.
type WatchHandler func(cfg WatchConfig)

// AskHandler answers ONE one-off question from the controller (OpAsk).
//
// Same split as `watch`: this package owns WIRE FORMAT (preamble, one request, one
// response, close) and not content. What knows how to walk an OID or read a routing table
// is the proxy, because those facts are knowable only from its machine (cf. cmd/proxy/rpc.go).
//
// ctx already carries the granted wait, bounded by MaxAskWait: the handler recalculates no
// delay, it only respects it.
type AskHandler func(ctx context.Context, req AskRequest) AskResponse

// preambleWait bounds preamble read -- and, for a question, the request line that follows
// immediately. A peer that opens a stream without saying what it wants must not block a
// goroutine forever.
const preambleWait = 15 * time.Second

// ServeStreams loops on incoming streams until session end. Preamble says what the stream
// carries: a connection to open, watch configuration, or a one-off question.
//
// ONE GOROUTINE PER STREAM, and that is not a detail since `ask` exists: an SNMP walk can
// last fifteen seconds, and handling it in this loop would block acceptance of all other
// streams -- therefore connector dials -- for all that time.
func ServeStreams(y *yamux.Session, allow *Allowlist, onWatch WatchHandler,
	onAsk AskHandler) error {
	for {
		var stream, err = y.Accept()
		if err != nil {
			return err
		}
		go serveStream(stream, allow, onWatch, onAsk)
	}
}

func serveStream(stream net.Conn, allow *Allowlist, onWatch WatchHandler,
	onAsk AskHandler) {
	// Preamble is read here for all stream types; `serveDial` receives the already decoded
	// request.
	_ = stream.SetDeadline(time.Now().Add(preambleWait))
	var br = bufio.NewReader(stream)
	var req DialRequest
	var err = readLine(br, &req)
	if err != nil {
		_ = stream.Close()
		return
	}
	switch req.Op {
	case OpWatch:
		_ = stream.SetDeadline(time.Time{})
		serveWatch(stream, br, onWatch)
	case OpAsk:
		serveAsk(stream, br, onAsk)
	default:
		serveDial(stream, br, req, allow)
	}
}

// ServeAskStream handles a question stream end to end, preamble included.
//
// Exported for the RESPONDING package (cmd/proxy): wire format is locked by a test on a
// net.Pipe pair, without mounting yamux or touching the network. The accept loop enters
// through serveAsk -- it already read the preamble to know it was a question.
func ServeAskStream(stream net.Conn, onAsk AskHandler) {
	_ = stream.SetDeadline(time.Now().Add(preambleWait))
	var br = bufio.NewReader(stream)
	var pre DialRequest
	var err = readLine(br, &pre)
	if err != nil || pre.Op != OpAsk {
		_ = stream.Close()
		return
	}
	serveAsk(stream, br, onAsk)
}

// serveAsk: one question, one answer, then the stream closes.
//
// Stateless and uncorrelated -- that is the whole point of the pattern (cf. OpAsk): nothing
// to match, nothing to clean up if the controller disappears mid-flight.
func serveAsk(stream net.Conn, br *bufio.Reader, onAsk AskHandler) {
	defer stream.Close() //nolint:errcheck
	var req AskRequest
	var err = readLine(br, &req)
	if err != nil {
		// Unreadable question: close without writing. Answering "error" to an undecoded
		// request would answer the wrong thing, and the controller already reads close as
		// failure.
		return
	}

	// SAME bound as the controller applies (ClampAskWait, hub.go): two identical functions
	// in this package -- one per tunnel side -- would eventually diverge, and a ceiling that
	// differs by side is no ceiling.
	var wait = ClampAskWait(req.TimeoutMS)
	// Preamble deadline would kill the stream BEFORE writing the response: a walk may last
	// longer than stream open. Reset to granted wait plus margin for the write itself.
	_ = stream.SetDeadline(time.Now().Add(wait + 5*time.Second))

	var resp AskResponse
	if onAsk == nil {
		// Proxy with no handler wired: SAY so. A stream that closes silently is
		// indistinguishable, on the controller side, from a tunnel that just dropped.
		resp = AskResponse{Err: "this proxy does not answer any questions"}
	} else {
		var ctx, cancel = context.WithTimeout(context.Background(), wait)
		defer cancel()
		resp = onAsk(ctx, req)
	}
	_ = writeLine(stream, resp)
}

// askWait bounds granted wait for a question.
//
// Controller proposes, PROXY decides: a bound the caller can raise is not a bound (same
// rule as MaxAskRows). An absurd request is CLAMPED to the ceiling rather than refused --
// an operator who typed ninety seconds wants a walk, not an error message.
// serveWatch reads successive configurations. Stream stays open: controller rewrites config
// on every change.
func serveWatch(stream net.Conn, br *bufio.Reader, onWatch WatchHandler) {
	defer stream.Close() //nolint:errcheck
	var dec = json.NewDecoder(br)
	var err error
	for {
		var cfg WatchConfig
		if err = dec.Decode(&cfg); err != nil {
			return
		}
		if onWatch != nil {
			onWatch(cfg)
		}
	}
}

// SendEvents opens the upstream stream and pushes proxy events. The proxy opens it: yamux is
// bidirectional, so the controller need not fetch them.
//
// THREE PHASES, AND ORDER IS THE FIX: backlog, then snapshot, then live.
//
// `snapshot` returns current state of each watched target (cf. Event.Resync). Optional --
// nil for a caller with nothing to re-announce -- and CALLED HERE rather than passed as a
// slice, because timing matters: after backlog, after delivering what waited in the queue.
//
// ORDER IS NOT A READING PREFERENCE. Events sleeping in the queue are OLDER than the
// snapshot: transitions accumulated during the outage. Delivering them AFTER it would
// overwrite current state with a stale transition -- a device that fell then came back
// during outage would show unreachable while responding. Snapshot cannot be alone either:
// taken before backlog it would lose intermediate transitions, precisely those nobody saw.
//
// Backlog is bounded by MEASURED queue length, not "while any remain": on a chatty fleet,
// watch that never stops producing would never hand off to snapshot, and that is the
// struggling fleet that needs it most. What measurement misses -- an event posted between
// measure and snapshot -- arrives after it, which is correct: monitor updates state BEFORE
// emitting, so such an event says nothing older than the snapshot.
func SendEvents(y *yamux.Session, events <-chan Event, snapshot func() []Event) error {
	var stream, err = y.Open()
	if err != nil {
		return err
	}
	defer stream.Close() //nolint:errcheck
	if err = writeLine(stream, DialRequest{Op: OpEvents}); err != nil {
		return err
	}

	// 1. Backlog: what already waited, in production order.
	var backlog = len(events)
	var ev Event
	var i int
	var drained bool
	for i = 0; i < backlog && !drained; i++ {
		select {
		case ev = <-events:
			if err = writeLine(stream, ev); err != nil {
				return err
			}
		default:
			// Drained meanwhile by a previous send: nothing to wait for.
			drained = true
		}
	}

	// 2. Snapshot: current state, which has final say over everything before it.
	if snapshot != nil {
		var snap = snapshot()
		for _, ev = range snap {
			if err = writeLine(stream, ev); err != nil {
				return err
			}
		}
	}

	// 3. Live, until session end.
	for ev = range events {
		if err = writeLine(stream, ev); err != nil {
			return err
		}
	}
	return nil
}

func serveDial(stream net.Conn, br *bufio.Reader, req DialRequest, allow *Allowlist) {
	defer stream.Close() //nolint:errcheck
	var err error
	if req.Op != OpDial || req.Host == "" || req.Port <= 0 || req.Port > 65535 {
		_ = writeLine(stream, DialReply{Op: "dial_err", Err: "invalid request"})
		return
	}

	var target = net.JoinHostPort(req.Host, strconv.Itoa(req.Port))
	var timeout = time.Duration(req.TimeoutMS) * time.Millisecond
	if timeout <= 0 || timeout > time.Minute {
		timeout = DefaultDialTimeout
	}

	var ctx, cancel = context.WithTimeout(context.Background(), timeout)
	defer cancel()

	err = checkAllowed(ctx, allow, req.Host)
	if err != nil {
		slog.Warn("dial refused by local allowlist", "host", req.Host, "err", err)
		_ = writeLine(stream, DialReply{Op: "dial_err", Err: err.Error()})
		return
	}

	var d = net.Dialer{Timeout: timeout}
	var remote net.Conn
	remote, err = d.DialContext(ctx, "tcp", target)
	if err != nil {
		_ = writeLine(stream, DialReply{Op: "dial_err", Err: firstLine(err.Error())})
		return
	}
	defer remote.Close() //nolint:errcheck

	if err = writeLine(stream, DialReply{Op: "dial_ok"}); err != nil {
		return
	}
	// From here, byte copying: the proxy understands neither SSH nor HTTP, it relays
	// (docs/01-Architecture/Proxy).
	_ = stream.SetDeadline(time.Time{})
	pipe(stream, br, remote)
}

// pipe relays both ways and stops on first close. Controller -> device direction starts from
// the buffered reader: useful bytes may already be there.
func pipe(stream net.Conn, buffered io.Reader, remote net.Conn) {
	var done = make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(remote, buffered)
		// Close write only: device may still respond.
		var cw, ok = remote.(interface{ CloseWrite() error })
		if ok {
			_ = cw.CloseWrite()
		}
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(stream, remote)
		done <- struct{}{}
	}()
	<-done
}

// checkAllowed verifies destination against allowlist. When an allowlist exists, a name
// resolving outside scope is refused -- otherwise restriction is bypassed with a DNS record.
func checkAllowed(ctx context.Context, allow *Allowlist, host string) error {
	if allow.Empty() {
		return nil
	}
	var ip, err = netip.ParseAddr(host)
	if err == nil {
		if !allow.permits(ip) {
			return errors.New("destination outside proxy allowlist")
		}
		return nil
	}
	var addrs []net.IPAddr
	addrs, err = net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return fmt.Errorf("resolving %s: %s", host, firstLine(err.Error()))
	}
	var a net.IPAddr
	for _, a = range addrs {
		var parsed, ok = netip.AddrFromSlice(a.IP)
		if !ok || !allow.permits(parsed.Unmap()) {
			return errors.New("destination outside proxy allowlist")
		}
	}
	return nil
}

func firstLine(s string) string {
	var i = strings.IndexAny(s, "\r\n")
	if i >= 0 {
		return s[:i]
	}
	return s
}
