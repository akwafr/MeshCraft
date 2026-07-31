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
	"sync"
	"time"

	"github.com/hashicorp/yamux"
)

// ErrNoTunnel: the targeted proxy has no established tunnel. This is a common
// operational error (proxy down, WAN cut), not a bug: callers must present it as such.
var ErrNoTunnel = errors.New("no tunnel established with this proxy")

// Hub holds live tunnels. One proxy = at most one session: a reconnection replaces the
// previous one, which we close so a ghost session cannot receive dials that will never
// succeed.
type Hub struct {
	mu       sync.RWMutex
	sessions map[string]*session
}

type session struct {
	proxyID string
	version string
	since   time.Time
	ymux    *yamux.Session

	// watch is the watch configuration stream, opened on demand and reused: config is
	// rewritten on it at every change.
	watchMu sync.Mutex
	watch   net.Conn
}

func NewHub() *Hub {
	return &Hub{sessions: map[string]*session{}}
}

// Register records a session and returns the function to call at its end. It unregisters
// only if the session is still the current one -- otherwise a fast reconnection would be
// evicted by the old session's cleanup.
func (h *Hub) Register(proxyID, version string, ymux *yamux.Session) func() {
	var s = &session{proxyID: proxyID, version: version, since: time.Now(), ymux: ymux}
	h.mu.Lock()
	var previous = h.sessions[proxyID]
	h.sessions[proxyID] = s
	h.mu.Unlock()
	if previous != nil {
		_ = previous.ymux.Close()
	}
	return func() {
		h.mu.Lock()
		if h.sessions[proxyID] == s {
			delete(h.sessions, proxyID)
		}
		h.mu.Unlock()
		_ = ymux.Close()
	}
}

// CloseSession cuts a proxy tunnel ON DEMAND, and LOGS why.
//
// What "revoke" meant before this method: refusal at the NEXT authentication, nothing
// more. The already established session kept opening connections to devices (Dial),
// writing events to the database (AcceptEvents), answering questions (Ask), and receiving
// watch configuration (PushWatch) -- therefore SNMP communities from the fleet in plain
// text. We do not revoke a proxy for tomorrow: we revoke because it is stolen, and its
// session is already there.
//
// The reason is LOGGED, and that is essential: the cut proxy reconnects immediately (its
// heartbeat keeps running, by design), so its own log will show "tunnel interrupted".
// Without this line on the controller side, the operator would hunt a WAN outage for a
// cut they requested themselves.
//
// The entry is removed from the table HERE, without waiting for Register's cleanup: that
// runs only when the tunnel handler returns, and until then a PERIODIC caller (cf.
// api.revalidateTunnels) would find the same dying session every pass and replay its log
// line. Early removal is safe: cleanup deletes only if the entry is still ITS own, so a
// reconnection in between stays in place.
//
// Returns false when there was nothing to cut -- no tunnel, or session already dead and
// not yet reaped. The caller uses that to avoid announcing a cut that did not happen.
func (h *Hub) CloseSession(proxyID, reason string) bool {
	h.mu.Lock()
	var s = h.sessions[proxyID]
	if s != nil {
		delete(h.sessions, proxyID)
	}
	h.mu.Unlock()
	if s == nil || s.ymux.IsClosed() {
		return false
	}
	slog.Warn("tunnel cut", "proxy_id", proxyID, "reason", reason,
		"duration", time.Since(s.since).Round(time.Second).String())
	_ = s.ymux.Close()
	return true
}

// ProxyIDs returns proxies with a registered tunnel, and nothing else.
//
// List() also returns them, but it PINGs each to measure latency: one WAN round trip per
// proxy, which a failing link stretches until yamux write timeout. Fair price for a
// diagnostic screen you open; a bad fit for a security loop that runs every minute and
// only needs identities.
func (h *Hub) ProxyIDs() []string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	var out = make([]string, 0, len(h.sessions))
	var id string
	for id = range h.sessions {
		out = append(out, id)
	}
	return out
}

func (h *Hub) get(proxyID string) *session {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.sessions[proxyID]
}

// Connected reports whether a proxy is reachable right now.
func (h *Hub) Connected(proxyID string) bool {
	var s = h.get(proxyID)
	return s != nil && !s.ymux.IsClosed()
}

// List inventories established tunnels.
func (h *Hub) List() []Status {
	h.mu.RLock()
	defer h.mu.RUnlock()
	var out = make([]Status, 0, len(h.sessions))
	var s *session
	for _, s = range h.sessions {
		var st = Status{
			ProxyID: s.proxyID, Version: s.version, Since: s.since,
			Streams: s.ymux.NumStreams(),
		}
		var rtt, err = s.ymux.Ping()
		if err == nil {
			st.RemoteRTT = rtt.Round(time.Millisecond).String()
		}
		out = append(out, st)
	}
	return out
}

// PushWatch sends (or rewrites) a proxy watch configuration. The stream is opened lazily
// and kept: rewriting on it is enough, and a full config replaces the previous one -- no
// delta to reconcile.
func (h *Hub) PushWatch(proxyID string, cfg WatchConfig) error {
	var s = h.get(proxyID)
	if s == nil || s.ymux.IsClosed() {
		return ErrNoTunnel
	}
	s.watchMu.Lock()
	defer s.watchMu.Unlock()
	if s.watch == nil {
		var stream, err = s.ymux.OpenStream()
		if err != nil {
			return err
		}
		if err = writeLine(stream, DialRequest{Op: OpWatch}); err != nil {
			_ = stream.Close()
			return err
		}
		s.watch = stream
	}
	var err = writeLine(s.watch, cfg)
	if err != nil {
		// Dead stream: discard so next attempt opens a fresh one.
		_ = s.watch.Close()
		s.watch = nil
	}
	return err
}

// EventHandler processes an event pushed by a proxy.
type EventHandler func(proxyID string, ev Event)

// AcceptEvents listens to streams opened BY the proxy. yamux is bidirectional: the
// proxy pushes events on a stream it opens itself, so the controller need not fetch them.
func (h *Hub) AcceptEvents(proxyID string, handle EventHandler) {
	var s = h.get(proxyID)
	if s == nil {
		return
	}
	for {
		var stream, err = s.ymux.Accept()
		if err != nil {
			return // session ended
		}
		go h.readEvents(proxyID, stream, handle)
	}
}

func (h *Hub) readEvents(proxyID string, stream net.Conn, handle EventHandler) {
	defer stream.Close() //nolint:errcheck
	var br = bufio.NewReader(stream)
	var req DialRequest
	var err = readLine(br, &req)
	if err != nil {
		return
	}
	if req.Op != OpEvents {
		return
	}
	for {
		var ev Event
		if err = readLine(br, &ev); err != nil {
			return
		}
		if ev.Type == "" {
			continue
		}
		handle(proxyID, ev)
	}
}

// Dial opens a TCP connection to host:port FROM the proxy. The returned connection is an
// ordinary net.Conn: the caller (driver, probe, HTTP client) has no notion of tunnel --
// the "single code path" rule from docs/01-Architecture/Vue d'ensemble.
func (h *Hub) Dial(ctx context.Context, proxyID, host string, port int) (net.Conn, error) {
	var s = h.get(proxyID)
	if s == nil || s.ymux.IsClosed() {
		return nil, ErrNoTunnel
	}
	var stream, err = s.ymux.OpenStream()
	if err != nil {
		return nil, fmt.Errorf("opening stream: %w", err)
	}

	// Preamble and its response are time-bounded: a mute proxy must not block the caller
	// indefinitely.
	var deadline, hasDeadline = ctx.Deadline()
	if !hasDeadline {
		deadline = time.Now().Add(DefaultDialTimeout + 2*time.Second)
	}
	_ = stream.SetDeadline(deadline)

	var timeoutMS = int(time.Until(deadline).Milliseconds())
	if timeoutMS <= 0 || timeoutMS > 60_000 {
		timeoutMS = int(DefaultDialTimeout.Milliseconds())
	}
	var req = DialRequest{Op: "dial", Host: host, Port: port, TimeoutMS: timeoutMS}
	if err = writeLine(stream, req); err != nil {
		_ = stream.Close()
		return nil, fmt.Errorf("sending dial: %w", err)
	}

	// Response is read through a buffered reader: device bytes that follow immediately (an
	// SSH banner, for example) are already in the buffer, so the returned connection must
	// deliver them first.
	var br = bufio.NewReader(stream)
	var reply DialReply
	if err = readLine(br, &reply); err != nil {
		_ = stream.Close()
		return nil, fmt.Errorf("proxy response: %w", err)
	}
	if reply.Op != "dial_ok" {
		_ = stream.Close()
		if reply.Err == "" {
			reply.Err = "refused by the proxy"
		}
		return nil, errors.New(reply.Err)
	}
	// No global deadline anymore: caller sets its own timeouts.
	_ = stream.SetDeadline(time.Time{})
	return &bufferedConn{Conn: stream, r: br}, nil
}

// bufferedConn returns what the buffered reader already consumed first.
type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.r.Read(p) }

// One-off question failures, SEPARATE.
//
// They are fixed by different actions -- restart a proxy, check the device, upgrade the
// proxy, open a ticket -- and a caller merging them into one "question failed" would make
// those actions unknowable. Hence public sentinels, not plain text: API code must SORT
// them (cf. api.askFailure), and sorting on message content does not survive the first
// rewording.
var (
	// ErrAskTimeout: nothing arrived within the deadline. The proxy is there (tunnel
	// holds), but it did not return its line -- mute device, troubled local network, or
	// saturated proxy.
	ErrAskTimeout = errors.New("the proxy did not respond in time")
	// ErrAskClosed: the stream closed without a single line. The most likely case is not a
	// fault but VERSION: a proxy that does not yet know the "ask" operation closes the
	// stream it cannot serve. Distinguishing this from a timeout avoids sending people to
	// check a mute device when the proxy binary needs upgrading.
	ErrAskClosed = errors.New("the proxy closed the stream without responding")
	// ErrAskUnreadable: something arrived, but it is not a response -- broken JSON, or an
	// endless line cut by the read bound. Nothing to do on the fleet: this is a protocol
	// fault between two binaries.
	ErrAskUnreadable = errors.New("unreadable proxy response")
)

// AskRemoteError: the proxy ANSWERED, and says it could not.
//
// A separate type, not text merged with other failures: the only case where we hold a
// diagnosis from whoever tried -- "SNMP timeout on 10.20.0.4", "community refused". Detail
// must reach the operator verbatim, because they name the fix.
type AskRemoteError struct{ Detail string }

func (e *AskRemoteError) Error() string { return "the proxy could not respond: " + e.Detail }

const (
	// maxAskResponse bounds the JSON response line to a question.
	//
	// maxPreamble (8 KiB) cannot serve here: a walk yields up to MaxAskRows lines, ~1.5
	// MiB JSON in ordinary cases. But the opposite -- unbounded read -- is the default this
	// project just fixed elsewhere (cf. api.maxProxyMetricsBody): the response comes from a
	// binary deployed at a customer, not necessarily the same version, and a line of
	// hundreds of megabytes would take down the CONTROLLER, i.e. the whole fleet, for a
	// diagnostic question on one device.
	//
	// 8 MiB: enough for 5000 lines at 1.6 KiB each -- an SNMP value is text the device
	// chooses (sysDescr can be hundreds of bytes) -- same order of magnitude as the metrics
	// batch ceiling crossing the same link. When the bound cuts, we SAY so: a silently
	// truncated response would read as a device without OIDs.
	maxAskResponse = 8 << 20
	// askMargin: what we keep from the budget announced to the proxy.
	//
	// The proxy must finish BEFORE our deadline, otherwise its "I got no answer from the
	// device" -- the line that actually says what happened -- arrives after we stopped
	// listening, and failure looks like proxy silence.
	askMargin = 2 * time.Second
	// askMinBudget: floor of budget left to the proxy. A question with zero or negative
	// budget is not a question.
	askMinBudget = time.Second
)

// ClampAskRows applies the HARD row ceiling of a response.
//
// Pure, exported for a precise reason: the ceiling must be applied at BOTH ends. Here
// because Hub is the only gateway to a proxy, and at the API caller so it can tell the
// operator which bound was actually used. A ceiling only one side checks is not a ceiling:
// the proxy may be older than the controller, and a bound applied only there could be
// raised by deploying an old binary.
func ClampAskRows(n int) int {
	if n <= 0 {
		return DefaultAskRows
	}
	if n > MaxAskRows {
		return MaxAskRows
	}
	return n
}

// ClampAskWait bounds wait for a response. 0 = DefaultAskWait; beyond MaxAskWait, clamp
// down -- an HTTP request must not be held ten minutes by a struggling proxy even if the
// asker requests it.
func ClampAskWait(ms int) time.Duration {
	if ms <= 0 {
		return DefaultAskWait
	}
	var d = time.Duration(ms) * time.Millisecond
	if d > MaxAskWait {
		return MaxAskWait
	}
	return d
}

// Ask asks ONE question of a proxy and returns ONE answer.
//
// Same shape as Dial and PushWatch: fresh stream, JSON preamble, then the payload line --
// except here the stream closes behind us. One question, one answer, no state or
// correlation to maintain (cf. OpAsk in control.go).
//
// `req.MaxRows` and `req.TimeoutMS` are WISHES: clamped to contract bounds before send,
// and the clamped value goes to the proxy. Caller context wins when shorter, deadline or
// cancel: an abandoned HTTP request must not leave a read waiting fifteen seconds.
func (h *Hub) Ask(ctx context.Context, proxyID string, req AskRequest) (AskResponse, error) {
	var s = h.get(proxyID)
	if s == nil || s.ymux.IsClosed() {
		// Immediate readable error, not a wait: Dial and PushWatch rule, and a down proxy
		// is ordinary operations no delay will fix.
		return AskResponse{}, ErrNoTunnel
	}
	req.MaxRows = ClampAskRows(req.MaxRows)

	var wait = ClampAskWait(req.TimeoutMS)
	var deadline = time.Now().Add(wait)
	var ctxDeadline, hasDeadline = ctx.Deadline()
	if hasDeadline && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
		wait = time.Until(deadline)
	}
	// Budget ANNOUNCED to proxy is shorter than ours, cf. askMargin.
	var budget = wait - askMargin
	if budget < askMinBudget {
		budget = askMinBudget
	}
	req.TimeoutMS = int(budget.Milliseconds())

	var stream, err = s.ymux.OpenStream()
	if err != nil {
		return AskResponse{}, fmt.Errorf("opening stream: %w", err)
	}
	defer stream.Close() //nolint:errcheck
	_ = stream.SetDeadline(deadline)

	// Deadline above covers EXPIRING context; not CANCELLED -- browser closing a page,
	// caller giving up. Without this watcher, read would keep waiting for a response
	// nobody will read. Closing the stream unblocks Read, and yamux accepts concurrent close.
	var done = make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = stream.Close()
		case <-done:
		}
	}()

	if err = writeLine(stream, DialRequest{Op: OpAsk}); err != nil {
		return AskResponse{}, askFail(ctx, err, wait)
	}
	if err = writeLine(stream, req); err != nil {
		return AskResponse{}, askFail(ctx, err, wait)
	}

	// BOUNDED read: buffered reader cannot pull more than the limit, so a proxy that never
	// sends newline does not grow controller memory -- it fails its own question.
	var limited = &io.LimitedReader{R: stream, N: maxAskResponse + 1}
	var br = bufio.NewReader(limited)
	var line string
	line, err = br.ReadString('\n')
	if len(line) > maxAskResponse {
		return AskResponse{}, fmt.Errorf(
			"%w: more than %d bytes without newline, read abandoned",
			ErrAskUnreadable, maxAskResponse)
	}
	if err != nil {
		// One tolerance only: proxy closes stream after writing its line but without final
		// newline. Response is there and complete -- dropping it loses information we
		// already hold, for a protocol mismatch that costs nothing to absorb. Everything
		// else (cut JSON, empty line) stays failure: `json.Valid` decides, not a guess about
		// peer version.
		if !errors.Is(err, io.EOF) || !json.Valid([]byte(line)) {
			return AskResponse{}, askFail(ctx, err, wait)
		}
	}
	var reply AskResponse
	if err = json.Unmarshal([]byte(line), &reply); err != nil {
		return AskResponse{}, fmt.Errorf("%w: %v", ErrAskUnreadable, err)
	}
	if reply.Err != "" {
		// Return response WITH error: proxy may have rows AND failure (walk interrupted
		// mid-table), and dropping rows loses the only partial answer we have.
		return reply, &AskRemoteError{Detail: reply.Err}
	}
	// Ceiling checked ON RESPONSE, not only requested in question: older or laxer proxy
	// may return more. Trim, and flag says table is incomplete.
	if len(reply.Rows) > req.MaxRows {
		reply.Rows = reply.Rows[:req.MaxRows]
		reply.Truncated = true
	}
	return reply, nil
}

// askFail names question failure so the caller can sort it.
//
// Case order matters: context first, because caller deadline ALSO drops stream deadline,
// and then returning "proxy did not answer" would blame the proxy for a delay the caller
// imposed. A exceeded delay is still exceeded, from wherever it comes -- same diagnostic
// action.
func askFail(ctx context.Context, err error, wait time.Duration) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("%w (caller budget: %v)", ErrAskTimeout,
			wait.Round(time.Millisecond))
	}
	if ctx.Err() != nil {
		return fmt.Errorf("question abandoned: %w", ctx.Err())
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return fmt.Errorf("%w (expected %v)", ErrAskTimeout, wait.Round(time.Millisecond))
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return ErrAskClosed
	}
	return fmt.Errorf("exchange with the proxy: %w", err)
}

func writeLine(w io.Writer, v any) error {
	var data, err = json.Marshal(v)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	_, err = w.Write(data)
	return err
}

// readLine reads a bounded JSON line: beyond maxPreamble, peer is not speaking the protocol.
func readLine(r *bufio.Reader, v any) error {
	var line, err = r.ReadString('\n')
	if err != nil {
		return err
	}
	if len(line) > maxPreamble {
		return errors.New("preamble too long")
	}
	return json.Unmarshal([]byte(line), v)
}
