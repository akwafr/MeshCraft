package socks

import (
	"bytes"
	"context"
	"io"
	"net"
	"testing"
	"time"
)

// The protocol is where the bugs hide: a SOCKS server that mis-frames one reply
// fails as "the browser cannot reach the switch", with nothing on screen saying
// why. Every test below drives a real socket through a real handshake.

type fakeResolver struct {
	known map[string]string // host → proxy id
}

func (r fakeResolver) ProxyFor(_ context.Context, _, host string) (string, string, bool) {
	var id, ok = r.known[host]
	return id, "switch-" + host, ok
}

type fakeAuth struct{ user, pass string }

func (a fakeAuth) Authenticate(_ context.Context, u, p string) (string, string, bool) {
	if u == a.user && ConstantTimeMatch(p, a.pass) {
		return "user-1", "operator@example.test", true
	}
	return "", "", false
}

// fakeDialer hands back one end of a pipe and keeps the other, so a test can be
// the device.
type fakeDialer struct {
	device net.Conn
	err    error
	asked  chan string
}

func (d *fakeDialer) Dial(_ context.Context, proxyID, host string, port int) (net.Conn, error) {
	if d.asked != nil {
		d.asked <- proxyID + " " + HostPort(host, port)
	}
	if d.err != nil {
		return nil, d.err
	}
	var mine, theirs = net.Pipe()
	d.device = mine
	return theirs, nil
}

func startServer(t *testing.T, s *Server) net.Addr {
	t.Helper()
	var ln, err = net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var ctx, cancel = context.WithCancel(context.Background())
	go s.Serve(ctx, ln) //nolint:errcheck
	t.Cleanup(func() {
		cancel()
		_ = ln.Close()
	})
	return ln.Addr()
}

// greet performs the method negotiation and RFC 1929 authentication.
func greet(t *testing.T, c net.Conn, user, pass string) byte {
	t.Helper()
	var _, err = c.Write([]byte{version5, 1, authUserPass})
	if err != nil {
		t.Fatal(err)
	}
	var resp = make([]byte, 2)
	if _, err = io.ReadFull(c, resp); err != nil {
		t.Fatal(err)
	}
	if resp[1] != authUserPass {
		return resp[1]
	}
	var req = []byte{authSubVer, byte(len(user))}
	req = append(req, user...)
	req = append(req, byte(len(pass)))
	req = append(req, pass...)
	if _, err = c.Write(req); err != nil {
		t.Fatal(err)
	}
	var sub = make([]byte, 2)
	if _, err = io.ReadFull(c, sub); err != nil {
		t.Fatal(err)
	}
	return sub[1]
}

func connect(t *testing.T, c net.Conn, host string, port int) byte {
	t.Helper()
	var req = []byte{version5, cmdConnect, 0x00, atypDomain, byte(len(host))}
	req = append(req, host...)
	req = append(req, byte(port>>8), byte(port))
	var _, err = c.Write(req)
	if err != nil {
		t.Fatal(err)
	}
	var resp = make([]byte, 10)
	if _, err = io.ReadFull(c, resp); err != nil {
		t.Fatal(err)
	}
	return resp[1]
}

func dialServer(t *testing.T, addr net.Addr) net.Conn {
	t.Helper()
	var c, err = net.DialTimeout("tcp", addr.String(), 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	return c
}

func TestConnectReachesTheDeviceThroughItsOwnProxy(t *testing.T) {
	var dialer = &fakeDialer{asked: make(chan string, 1)}
	var srv = &Server{
		Resolver: fakeResolver{known: map[string]string{"192.0.2.38": "proxy-lab"}},
		Auth:     fakeAuth{user: "op", pass: "s3cret"},
		Dialer:   dialer,
	}
	var addr = startServer(t, srv)
	var c = dialServer(t, addr)

	var st = greet(t, c, "op", "s3cret")
	if st != 0 {
		t.Fatalf("authentication refused (status %d)", st)
	}
	var rep = connect(t, c, "192.0.2.38", 80)
	if rep != replyOK {
		t.Fatalf("CONNECT reply = %d, want success", rep)
	}
	// THE POINT OF THE PACKAGE: it left through the proxy that can reach it.
	var gotDial string
	select {
	case gotDial = <-dialer.asked:
		if gotDial != "proxy-lab 192.0.2.38:80" {
			t.Errorf("dialled %q, want it through proxy-lab", gotDial)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the dialer was never asked")
	}

	// And bytes travel both ways.
	go func() { _, _ = c.Write([]byte("GET / HTTP/1.0\r\n\r\n")) }()
	var buf = make([]byte, 18)
	_ = dialer.device.SetDeadline(time.Now().Add(3 * time.Second))
	var _, rerr = io.ReadFull(dialer.device, buf)
	if rerr != nil {
		t.Fatalf("device never saw the request: %v", rerr)
	}
	if !bytes.HasPrefix(buf, []byte("GET /")) {
		t.Errorf("device saw %q", buf)
	}
	go func() { _, _ = dialer.device.Write([]byte("HTTP/1.0 200 OK\r\n\r\n")) }()
	var back = make([]byte, 15)
	_, rerr = io.ReadFull(c, back)
	if rerr != nil {
		t.Fatalf("response never came back: %v", rerr)
	}
	if !bytes.HasPrefix(back, []byte("HTTP/1.0 200")) {
		t.Errorf("client saw %q", back)
	}
}

// THE SECURITY BOUNDARY. This is a route to meshcraft's switches, not a way into
// a customer's network.
func TestConnectRefusesAHostThatIsNotADevice(t *testing.T) {
	var refusals = make(chan string, 1)
	var dialer = &fakeDialer{}
	var srv = &Server{
		Resolver: fakeResolver{known: map[string]string{"192.0.2.38": "p"}},
		Auth:     fakeAuth{user: "op", pass: "s3cret"},
		Dialer:   dialer,
		OnRefusal: func(_, host string, port int, reason string) {
			refusals <- HostPort(host, port) + ": " + reason
		},
	}
	var addr = startServer(t, srv)
	var c = dialServer(t, addr)
	greet(t, c, "op", "s3cret")

	var rep = connect(t, c, "10.42.0.9", 22)
	if rep != replyNotAllow {
		t.Fatalf("CONNECT reply = %d, want 'not allowed' for an unknown host", rep)
	}
	var gotRefusal string
	select {
	case gotRefusal = <-refusals:
		if gotRefusal == "" {
			t.Error("refusal reported without a reason")
		}
	case <-time.After(2 * time.Second):
		t.Error("the refusal was not reported for audit")
	}
	if dialer.device != nil {
		t.Error("a connection was opened to a host that is not a device")
	}
}

func TestBadPasswordIsRefused(t *testing.T) {
	var srv = &Server{
		Resolver: fakeResolver{known: map[string]string{}},
		Auth:     fakeAuth{user: "op", pass: "s3cret"},
		Dialer:   &fakeDialer{},
	}
	var addr = startServer(t, srv)
	var c = dialServer(t, addr)
	var st = greet(t, c, "op", "wrong")
	if st == 0 {
		t.Fatal("a wrong password was accepted")
	}
}

// An "authentication optional" proxy into a management network is an
// unauthenticated one: a client offering only "no auth" must be turned away.
func TestClientRefusingAuthenticationIsTurnedAway(t *testing.T) {
	var srv = &Server{
		Resolver: fakeResolver{known: map[string]string{}},
		Auth:     fakeAuth{user: "op", pass: "s3cret"},
		Dialer:   &fakeDialer{},
	}
	var addr = startServer(t, srv)
	var c = dialServer(t, addr)

	var _, err = c.Write([]byte{version5, 1, 0x00}) // "no authentication required"
	if err != nil {
		t.Fatal(err)
	}
	var resp = make([]byte, 2)
	if _, err = io.ReadFull(c, resp); err != nil {
		t.Fatal(err)
	}
	if resp[1] != authNoneOfMy {
		t.Fatalf("server offered method %d to a client refusing auth", resp[1])
	}
}

// BIND and UDP ASSOCIATE have no meaning when the far side is a tunnel stream
// the controller opened. Answering anything but "not supported" would promise a
// channel that does not exist.
func TestOnlyConnectIsServed(t *testing.T) {
	var srv = &Server{
		Resolver: fakeResolver{known: map[string]string{"192.0.2.38": "p"}},
		Auth:     fakeAuth{user: "op", pass: "s3cret"},
		Dialer:   &fakeDialer{},
	}
	var addr = startServer(t, srv)
	var c = dialServer(t, addr)
	greet(t, c, "op", "s3cret")

	var req = []byte{version5, 0x02 /* BIND */, 0x00, atypIPv4, 192, 0, 2, 38, 0, 80}
	var _, err = c.Write(req)
	if err != nil {
		t.Fatal(err)
	}
	var resp = make([]byte, 10)
	if _, err = io.ReadFull(c, resp); err != nil {
		t.Fatal(err)
	}
	if resp[1] != replyCmdUnsup {
		t.Errorf("BIND reply = %d, want 'command not supported'", resp[1])
	}
}

// A device the proxy cannot reach must be reported as unreachable, not as a
// policy refusal: the two send an operator to completely different places.
func TestUnreachableDeviceIsReportedAsSuch(t *testing.T) {
	var srv = &Server{
		Resolver: fakeResolver{known: map[string]string{"192.0.2.38": "p"}},
		Auth:     fakeAuth{user: "op", pass: "s3cret"},
		Dialer:   &fakeDialer{err: io.ErrUnexpectedEOF},
	}
	var addr = startServer(t, srv)
	var c = dialServer(t, addr)
	greet(t, c, "op", "s3cret")
	var rep = connect(t, c, "192.0.2.38", 443)
	if rep != replyHostUnr {
		t.Errorf("reply = %d, want 'host unreachable'", rep)
	}
}

func TestIPv4AndIPv6Destinations(t *testing.T) {
	var dialer = &fakeDialer{asked: make(chan string, 2)}
	var srv = &Server{
		Resolver: fakeResolver{known: map[string]string{
			"192.0.2.38": "p", "fd00::1": "p",
		}},
		Auth:   fakeAuth{user: "op", pass: "s3cret"},
		Dialer: dialer,
	}
	var addr = startServer(t, srv)

	var c = dialServer(t, addr)
	greet(t, c, "op", "s3cret")
	var _, err = c.Write([]byte{version5, cmdConnect, 0, atypIPv4, 192, 0, 2, 38, 0, 80})
	if err != nil {
		t.Fatal(err)
	}
	var resp = make([]byte, 10)
	if _, err = io.ReadFull(c, resp); err != nil {
		t.Fatal(err)
	}
	if resp[1] != replyOK {
		t.Fatalf("IPv4 CONNECT reply = %d", resp[1])
	}
	var gotV4 string
	select {
	case gotV4 = <-dialer.asked:
		if gotV4 != "p 192.0.2.38:80" {
			t.Errorf("IPv4 destination decoded as %q", gotV4)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no dial")
	}

	var c6 = dialServer(t, addr)
	greet(t, c6, "op", "s3cret")
	var req = []byte{version5, cmdConnect, 0, atypIPv6}
	req = append(req, net.ParseIP("fd00::1").To16()...)
	req = append(req, byte(443>>8), byte(443&0xff))
	if _, err = c6.Write(req); err != nil {
		t.Fatal(err)
	}
	if _, err = io.ReadFull(c6, resp); err != nil {
		t.Fatal(err)
	}
	var gotV6 string
	select {
	case gotV6 = <-dialer.asked:
		if gotV6 != "p [fd00::1]:443" {
			t.Errorf("IPv6 destination decoded as %q", gotV6)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no dial")
	}
}

func TestConstantTimeMatch(t *testing.T) {
	if !ConstantTimeMatch("abc", "abc") {
		t.Error("equal secrets did not match")
	}
	if ConstantTimeMatch("abc", "abd") {
		t.Error("different secrets matched")
	}
	// An empty expected token must never match — that is what an unconfigured
	// account looks like, and it must not be an open door.
	if ConstantTimeMatch("", "") {
		t.Error("an unset token accepted an empty presentation")
	}
	if ConstantTimeMatch("anything", "") {
		t.Error("an unset token accepted a presentation")
	}
}

// --- the idle bound, i.e. the client that vanishes without a FIN ---
//
// THE LEAK THIS ANSWERS. During the ProCurve survey four tunnel streams
// outlived their SOCKS sessions, each holding one of that switch's four
// sessions, and only a controller restart cleared them. Reading the code
// closed the obvious half: every exit path after the dial defers `up.Close()`,
// and `relay` returns only once BOTH directions have finished. What a read
// cannot settle is the case where NEITHER direction ever finishes — a laptop
// that sleeps, a Wi-Fi drop, a killed browser. No FIN arrives, both `io.Copy`
// calls block for ever, and `relay` never returns, so the deferred close never
// runs and the stream is held until the process dies. That is the shape of the
// observed leak, and the idle guard is the only thing standing in front of it.
//
// A `net.Pipe` is the right stand-in precisely because it is the worst case: it
// is synchronous and unbuffered, it has no keepalive, no RST and no OS timeout,
// so nothing but the guard can ever unblock these copies.

// A relay whose two ends both go silent must END, and must close the upstream —
// the tunnel stream — rather than hold it.
func TestARelayNobodyIsUsingIsClosedRatherThanHeld(t *testing.T) {
	var clientLocal, clientRemote = net.Pipe()
	var upLocal, upRemote = net.Pipe()
	defer clientRemote.Close() //nolint:errcheck
	defer upRemote.Close()     //nolint:errcheck

	var done = make(chan struct{})
	go func() {
		relayWithIdle(clientLocal, upLocal, 40*time.Millisecond)
		close(done)
	}()

	// Nobody writes anything, and nobody hangs up: exactly the vanished client.
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("relay never returned: a client that vanished without a FIN holds the tunnel stream open, which is the observed leak")
	}

	// The DEVICE side must see the connection go away. If it does not, the
	// switch keeps counting the session and the operator loses a slot.
	_ = upRemote.SetReadDeadline(time.Now().Add(2 * time.Second))
	var buf = make([]byte, 1)
	var _, err = upRemote.Read(buf)
	if err == nil {
		t.Fatal("the device end is still open after the relay ended")
	}
}

// The bound must not cut a session that is merely QUIET. An operator reading a
// configuration page sends nothing for minutes, and a relay that treats that as
// abandonment is a worse bug than the leak: the page dies under the reader.
func TestTrafficInEitherDirectionKeepsTheRelayAlive(t *testing.T) {
	var clientLocal, clientRemote = net.Pipe()
	var upLocal, upRemote = net.Pipe()
	defer clientRemote.Close() //nolint:errcheck
	defer upRemote.Close()     //nolint:errcheck

	var limit = 120 * time.Millisecond
	var done = make(chan struct{})
	go func() {
		relayWithIdle(clientLocal, upLocal, limit)
		close(done)
	}()

	// Drain the device end so the writes below complete on an unbuffered pipe.
	go io.Copy(io.Discard, upRemote) //nolint:errcheck

	// Four beats at half the limit. A guard that failed to reset would fire
	// during this loop; one that resets carries the session past 2x the bound.
	var i int
	for i = 0; i < 4; i++ {
		time.Sleep(limit / 2)
		var _, err = clientRemote.Write([]byte("x"))
		if err != nil {
			t.Fatalf("the relay was cut while it was carrying traffic: %v", err)
		}
	}
	select {
	case <-done:
		t.Fatal("a relay that is moving bytes was closed as idle")
	default:
	}

	// And it still ends once the traffic stops — the beat resets the bound, it
	// does not remove it.
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("relay never returned after the traffic stopped")
	}
}

// stop() ends the guard's goroutine and must NOT close the connections: on the
// normal path the relay has already finished and the caller owns both ends.
// A guard that closed on the way out would turn every clean session into a
// double close, and would race with the caller's own deferred Close.
func TestStoppingTheGuardLeavesTheConnectionsToTheirOwner(t *testing.T) {
	var a, aPeer = net.Pipe()
	var b, bPeer = net.Pipe()
	defer a.Close()     //nolint:errcheck
	defer b.Close()     //nolint:errcheck
	defer aPeer.Close() //nolint:errcheck
	defer bPeer.Close() //nolint:errcheck

	var g = newIdleGuard(a, b, time.Hour)
	g.stop()

	// Give the goroutine a moment to act on `done`, then prove it did not close.
	time.Sleep(20 * time.Millisecond)
	var errs = make(chan error, 1)
	go func() {
		var _, err = a.Write([]byte("still open"))
		errs <- err
	}()
	var buf = make([]byte, 10)
	_ = aPeer.SetReadDeadline(time.Now().Add(2 * time.Second))
	var _, err = io.ReadFull(aPeer, buf)
	if err != nil {
		t.Fatalf("the guard closed a connection it does not own: %v", err)
	}
	if err = <-errs; err != nil {
		t.Fatalf("write failed after stop(): %v", err)
	}
}

// noHalfClose is a net.Conn WITHOUT CloseWrite — a tunnel stream, whose yamux
// Stream has none. net.Pipe already lacks it; this names the property under test
// so the test does not silently stop testing it if net.Pipe ever gains one.
type noHalfClose struct{ net.Conn }

// A client that hangs up CLEANLY against an upstream that cannot half-close must
// still free the pair, instead of blocking until the idle guard fires.
//
// This is the leak measured on unifimoule1 (2026-08-10): one stream per SOCKS
// connection, held ~30 min. It is NOT the fin-less case the other tests cover —
// here the client sends a real FIN, and the leak happens anyway, because the FIN
// has no way to cross a tunnel stream. The assertion is on the DEVICE end: the
// far side of the upstream pipe must see EOF, i.e. the switch stops counting the
// session, not merely that a goroutine exited.
func TestCleanHangupFreesAnUpstreamThatCannotHalfClose(t *testing.T) {
	var clientSide, clientEnd = net.Pipe()
	var upstreamSide, deviceEnd = net.Pipe()

	var done = make(chan struct{})
	go func() {
		relayWithIdle(clientSide, noHalfClose{upstreamSide}, time.Hour)
		close(done)
	}()

	// The client says its piece and hangs up cleanly. The device stays mute —
	// a switch sitting at a prompt, waiting to be spoken to.
	var _, wErr = clientEnd.Write([]byte("show version\n"))
	if wErr != nil {
		t.Fatalf("write: %v", wErr)
	}
	var buf = make([]byte, 32)
	var _, rErr = deviceEnd.Read(buf)
	if rErr != nil {
		t.Fatalf("device read: %v", rErr)
	}
	clientEnd.Close()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("relay never returned: the stream would live until the idle guard")
	}

	deviceEnd.SetReadDeadline(time.Now().Add(2 * time.Second))
	var _, devErr = deviceEnd.Read(buf)
	if devErr == nil {
		t.Fatal("device end still open: the switch would keep counting the session")
	}
}

// The other half, and the one that would be a worse bug: a client that shuts
// down only its WRITE side and keeps READING is a real pattern, and an upstream
// that CAN half-close must not be torn down under it.
func TestHalfCloseCapableUpstreamIsNotTornDown(t *testing.T) {
	var clientSide, clientEnd = net.Pipe()
	var upstreamSide, deviceEnd = net.Pipe()

	var done = make(chan struct{})
	go func() {
		relayWithIdle(clientSide, halfCloseable{upstreamSide}, time.Hour)
		close(done)
	}()

	clientEnd.Write([]byte("q\n"))
	var buf = make([]byte, 32)
	deviceEnd.Read(buf)

	// The device answers AFTER the client's write side is done. That reply must
	// still reach the client — if the relay had torn the pair down on a
	// successful half-close, this read would fail and an operator would lose the
	// last page of output.
	go deviceEnd.Write([]byte("bye\n"))
	clientEnd.SetReadDeadline(time.Now().Add(2 * time.Second))
	var n, err = clientEnd.Read(buf)
	if err != nil || n == 0 {
		t.Fatalf("reply lost after a supported half-close: n=%d err=%v", n, err)
	}

	// Teardown: both ends go away, so the relay can return and the test does not
	// depend on the idle guard.
	clientEnd.Close()
	deviceEnd.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("relay never returned after both ends closed")
	}
}

// halfCloseable HAS CloseWrite, so closeWrite reports true.
type halfCloseable struct{ net.Conn }

func (h halfCloseable) CloseWrite() error { return nil }
