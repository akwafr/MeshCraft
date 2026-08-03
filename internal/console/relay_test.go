package console

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// The relay is tested against the SAME in-process SSH server as console_test.go,
// with only the browser faked. That split is deliberate: the device side is where
// the surprises live (a pty, a shell channel, EOF on hangup) and faking it would
// test the fake, whereas the browser side is trivially describable and its
// interesting states — going silent forever, dropping mid-write — cannot be
// arranged on demand with a real one.

type fakePeer struct {
	in     chan Frame
	out    chan Frame
	closed chan struct{}

	mu       sync.Mutex
	pings    int
	pingErr  error
	closeMsg string
	once     sync.Once
}

func newFakePeer() *fakePeer {
	return &fakePeer{
		in:     make(chan Frame, 16),
		out:    make(chan Frame, 256),
		closed: make(chan struct{}),
	}
}

func (p *fakePeer) Read(ctx context.Context) (Frame, error) {
	var f Frame
	select {
	case f = <-p.in:
		return f, nil
	case <-p.closed:
		return Frame{}, io.EOF
	case <-ctx.Done():
		return Frame{}, ctx.Err()
	}
}

func (p *fakePeer) Write(ctx context.Context, f Frame) error {
	select {
	case p.out <- f:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *fakePeer) Ping(_ context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pings++
	return p.pingErr
}

func (p *fakePeer) Close(reason string) error {
	p.mu.Lock()
	p.closeMsg = reason
	p.mu.Unlock()
	p.once.Do(func() { close(p.closed) })
	return nil
}

func (p *fakePeer) reason() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.closeMsg
}

// collect drains output frames until the relay reports "closed", or the budget
// runs out. Returns everything binary as one string, and the terminal status.
func (p *fakePeer) collect(t *testing.T, budget time.Duration) (string, status) {
	t.Helper()
	var terminal status
	var b strings.Builder
	var deadline = time.After(budget)
	var f Frame
	for {
		select {
		case f = <-p.out:
			if !f.Text {
				b.Write(f.Data)
				continue
			}
			var st status
			if json.Unmarshal(f.Data, &st) == nil && st.Op == "closed" {
				terminal = st
				return b.String(), terminal
			}
		case <-deadline:
			t.Fatalf("the relay never reported a close (collected %q)", b.String())
			return "", terminal
		}
	}
}

func openTestSession(t *testing.T, srv *testServer) *Session {
	t.Helper()
	var sess, err = Open(context.Background(), optsFor(srv),
		[]Credential{{Username: "admin", Secret: "good", Why: "generation 1"}})
	if err != nil {
		t.Fatal(err)
	}
	return sess
}

// The first thing the operator's terminal must learn is WHICH ACCOUNT it got.
func TestRelayAnnouncesTheAcceptedAccount(t *testing.T) {
	var srv = newTestServer(t, map[string]string{"admin": "good"})
	var peer = newFakePeer()
	var sess = openTestSession(t, srv)

	go func() {
		time.Sleep(150 * time.Millisecond)
		_ = peer.Close("test done")
	}()
	var err = Relay(context.Background(), sess, peer, RelayOptions{
		IdleTimeout: 2 * time.Second, MaxLifetime: 5 * time.Second,
		PingInterval: time.Second,
	})
	if err != nil {
		t.Fatalf("Relay: %v", err)
	}

	// The ready frame is written before any device byte.
	var f Frame
	select {
	case f = <-peer.out:
	case <-time.After(time.Second):
		t.Fatal("no frame at all")
	}
	if !f.Text {
		t.Fatal("the first frame was terminal data, not the ready status")
	}
	var st status
	jsonUnmarshal(t, f.Data, &st)
	if st.Op != "ready" {
		t.Fatalf("first status = %q, want ready", st.Op)
	}
	if st.Username != "admin" || st.Why != "generation 1" {
		t.Errorf("ready = %+v, want the account and the reason it was chosen", st)
	}
}

// A CONTROL FRAME MUST NEVER REACH THE DEVICE. Were the two merged, a client bug
// would type `{"op":"resize"}` into a privileged shell.
func TestRelayNeverTypesControlFramesAtTheDevice(t *testing.T) {
	var srv = newTestServer(t, map[string]string{"admin": "good"})
	var peer = newFakePeer()
	var sess = openTestSession(t, srv)

	peer.in <- Frame{Text: true, Data: []byte(`{"op":"resize","cols":90,"rows":25}`)}
	peer.in <- Frame{Data: []byte("show version\r")}
	go func() {
		time.Sleep(400 * time.Millisecond)
		_ = peer.Close("test done")
	}()

	var err = Relay(context.Background(), sess, peer, RelayOptions{
		IdleTimeout: 3 * time.Second, MaxLifetime: 5 * time.Second,
		PingInterval: time.Second,
	})
	if err != nil {
		t.Fatalf("Relay: %v", err)
	}
	var got, _ = peer.collect(t, 2*time.Second)
	// The server echoes whatever it is written.
	if !strings.Contains(got, "show version") {
		t.Errorf("terminal input never reached the device: %q", got)
	}
	if strings.Contains(got, "resize") || strings.Contains(got, "{\"op\"") {
		t.Errorf("a control frame was typed at the device: %q", got)
	}
}

// An unparseable or unknown control frame is IGNORED, not forwarded and not
// fatal: an older client sending something new must not end up typing JSON.
func TestRelayIgnoresUnknownControlFrames(t *testing.T) {
	var srv = newTestServer(t, map[string]string{"admin": "good"})
	var peer = newFakePeer()
	var sess = openTestSession(t, srv)

	peer.in <- Frame{Text: true, Data: []byte("not json at all")}
	peer.in <- Frame{Text: true, Data: []byte(`{"op":"something-new"}`)}
	peer.in <- Frame{Data: []byte("ping\r")}
	go func() {
		time.Sleep(400 * time.Millisecond)
		_ = peer.Close("test done")
	}()
	_ = Relay(context.Background(), sess, peer, RelayOptions{
		IdleTimeout: 3 * time.Second, MaxLifetime: 5 * time.Second,
		PingInterval: time.Second,
	})
	var got, _ = peer.collect(t, 2*time.Second)
	if !strings.Contains(got, "ping") {
		t.Errorf("the session died on an unknown control frame: %q", got)
	}
	if strings.Contains(got, "not json") || strings.Contains(got, "something-new") {
		t.Errorf("an unknown control frame was forwarded to the device: %q", got)
	}
}

// THE RULE THAT PROTECTS THE VTY POOL. A Catalyst has sixteen lines and hands
// them out until it has none left, at which point nobody can log in — including
// whoever is trying to fix it.
func TestRelayClosesAnIdleSession(t *testing.T) {
	var srv = newTestServer(t, map[string]string{"admin": "good"})
	var peer = newFakePeer()
	var sess = openTestSession(t, srv)

	var started = time.Now()
	var err = Relay(context.Background(), sess, peer, RelayOptions{
		IdleTimeout: 300 * time.Millisecond, MaxLifetime: 10 * time.Second,
		PingInterval: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("Relay: %v", err)
	}
	var elapsed = time.Since(started)
	if elapsed > 3*time.Second {
		t.Errorf("idle close took %v, the timer did not fire", elapsed)
	}
	if !strings.Contains(peer.reason(), "without activity") {
		t.Errorf("close reason = %q, want it to name the idle timeout", peer.reason())
	}
}

// Typing keeps it alive: the idle timer measures the OPERATOR, not the device.
func TestRelayIdleTimerIsResetByInput(t *testing.T) {
	var srv = newTestServer(t, map[string]string{"admin": "good"})
	var peer = newFakePeer()
	var sess = openTestSession(t, srv)

	var stop = make(chan struct{})
	go func() {
		var tick = time.NewTicker(100 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-tick.C:
				select {
				case peer.in <- Frame{Data: []byte("x")}:
				default:
				}
			case <-stop:
				return
			}
		}
	}()

	var done = make(chan time.Duration, 1)
	go func() {
		var t0 = time.Now()
		_ = Relay(context.Background(), sess, peer, RelayOptions{
			IdleTimeout: 400 * time.Millisecond, MaxLifetime: 1200 * time.Millisecond,
			PingInterval: 5 * time.Second,
		})
		done <- time.Since(t0)
	}()

	var d time.Duration
	select {
	case d = <-done:
		close(stop)
		// It must have survived well past the idle timeout, ending on the
		// lifetime ceiling instead.
		if d < 900*time.Millisecond {
			t.Errorf("closed after %v: typing did not reset the idle timer", d)
		}
		if !strings.Contains(peer.reason(), "maximum") {
			t.Errorf("close reason = %q, want the lifetime ceiling", peer.reason())
		}
	case <-time.After(5 * time.Second):
		close(stop)
		t.Fatal("the relay never returned")
	}
}

// A session that outlives a shift is one nobody is watching.
func TestRelayEnforcesMaxLifetime(t *testing.T) {
	var srv = newTestServer(t, map[string]string{"admin": "good"})
	var peer = newFakePeer()
	var sess = openTestSession(t, srv)

	var started = time.Now()
	_ = Relay(context.Background(), sess, peer, RelayOptions{
		IdleTimeout: 10 * time.Second, MaxLifetime: 300 * time.Millisecond,
		PingInterval: 5 * time.Second,
	})
	var elapsed = time.Since(started)
	if elapsed > 3*time.Second {
		t.Errorf("lifetime ceiling took %v to fire", elapsed)
	}
	if !strings.Contains(peer.reason(), "maximum") {
		t.Errorf("close reason = %q, want the lifetime ceiling named", peer.reason())
	}
}

// The device hanging up (exec-timeout, `exit`, a reload) must end the session
// and SAY SO — a terminal that simply stops leaves the operator guessing.
func TestRelayReportsTheDeviceHangingUp(t *testing.T) {
	var srv = newTestServer(t, map[string]string{"admin": "good"})
	var peer = newFakePeer()
	var sess = openTestSession(t, srv)

	go func() {
		time.Sleep(150 * time.Millisecond)
		// Closing the SSH session under the relay is what a device hangup looks
		// like from here.
		_ = sess.session.Close()
	}()
	var done = make(chan struct{})
	go func() {
		_ = Relay(context.Background(), sess, peer, RelayOptions{
			IdleTimeout: 10 * time.Second, MaxLifetime: 10 * time.Second,
			PingInterval: 5 * time.Second,
		})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the relay did not notice the device hanging up")
	}
	if peer.reason() == "" {
		t.Error("the session ended without telling the operator why")
	}
}

// Paste is legitimate; using the console as an upload channel is not.
func TestRelayRefusesAnOversizedInputFrame(t *testing.T) {
	var srv = newTestServer(t, map[string]string{"admin": "good"})
	var peer = newFakePeer()
	var sess = openTestSession(t, srv)

	peer.in <- Frame{Data: make([]byte, maxInputFrame+1)}
	var done = make(chan struct{})
	go func() {
		_ = Relay(context.Background(), sess, peer, RelayOptions{
			IdleTimeout: 10 * time.Second, MaxLifetime: 10 * time.Second,
			PingInterval: 5 * time.Second,
		})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("an oversized frame did not end the session")
	}
	if !strings.Contains(peer.reason(), "too large") {
		t.Errorf("close reason = %q, want the frame bound named", peer.reason())
	}
}

// The keepalive is what stops a NAT from silently eating the connection while
// both ends wait on a socket that no longer goes anywhere.
func TestRelayPingsAndClosesWhenThePingFails(t *testing.T) {
	var srv = newTestServer(t, map[string]string{"admin": "good"})
	var peer = newFakePeer()
	peer.pingErr = io.ErrClosedPipe
	var sess = openTestSession(t, srv)

	var started = time.Now()
	_ = Relay(context.Background(), sess, peer, RelayOptions{
		IdleTimeout: 10 * time.Second, MaxLifetime: 10 * time.Second,
		PingInterval: 150 * time.Millisecond,
	})
	var elapsed = time.Since(started)
	if elapsed > 3*time.Second {
		t.Errorf("a dead peer took %v to notice", elapsed)
	}
	peer.mu.Lock()
	var pings = peer.pings
	peer.mu.Unlock()
	if pings == 0 {
		t.Error("no keepalive was sent")
	}
	if !strings.Contains(peer.reason(), "stopped responding") {
		t.Errorf("close reason = %q", peer.reason())
	}
}

// OnClose is what the audit line is built from: it must report the bytes and the
// duration, not just the fact.
func TestRelayReportsCloseToTheCaller(t *testing.T) {
	var srv = newTestServer(t, map[string]string{"admin": "good"})
	var peer = newFakePeer()
	var sess = openTestSession(t, srv)

	var gotReason string
	var gotSent, gotReceived int64
	var called bool
	peer.in <- Frame{Data: []byte("hello\r")}
	go func() {
		time.Sleep(300 * time.Millisecond)
		_ = peer.Close("done")
	}()
	_ = Relay(context.Background(), sess, peer, RelayOptions{
		IdleTimeout: 3 * time.Second, MaxLifetime: 5 * time.Second,
		PingInterval: time.Second,
		OnClose: func(reason string, sent, received int64, _ time.Duration) {
			called, gotReason, gotSent, gotReceived = true, reason, sent, received
		},
	})
	if !called {
		t.Fatal("OnClose was never called: nothing would reach the audit log")
	}
	if gotReason == "" {
		t.Error("OnClose got no reason")
	}
	if gotReceived == 0 {
		t.Error("operator input was not counted")
	}
	if gotSent == 0 {
		t.Error("device output was not counted")
	}
}

func jsonUnmarshal(t *testing.T, data []byte, v any) {
	t.Helper()
	var err = json.Unmarshal(data, v)
	if err != nil {
		t.Fatalf("unmarshal %q: %v", string(data), err)
	}
}
