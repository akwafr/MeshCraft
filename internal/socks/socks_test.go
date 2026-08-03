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
		Resolver: fakeResolver{known: map[string]string{"10.99.0.38": "proxy-akwalan"}},
		Auth:     fakeAuth{user: "op", pass: "s3cret"},
		Dialer:   dialer,
	}
	var addr = startServer(t, srv)
	var c = dialServer(t, addr)

	var st = greet(t, c, "op", "s3cret")
	if st != 0 {
		t.Fatalf("authentication refused (status %d)", st)
	}
	var rep = connect(t, c, "10.99.0.38", 80)
	if rep != replyOK {
		t.Fatalf("CONNECT reply = %d, want success", rep)
	}
	// THE POINT OF THE PACKAGE: it left through the proxy that can reach it.
	var gotDial string
	select {
	case gotDial = <-dialer.asked:
		if gotDial != "proxy-akwalan 10.99.0.38:80" {
			t.Errorf("dialled %q, want it through proxy-akwalan", gotDial)
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
		Resolver: fakeResolver{known: map[string]string{"10.99.0.38": "p"}},
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
		Resolver: fakeResolver{known: map[string]string{"10.99.0.38": "p"}},
		Auth:     fakeAuth{user: "op", pass: "s3cret"},
		Dialer:   &fakeDialer{},
	}
	var addr = startServer(t, srv)
	var c = dialServer(t, addr)
	greet(t, c, "op", "s3cret")

	var req = []byte{version5, 0x02 /* BIND */, 0x00, atypIPv4, 10, 99, 0, 38, 0, 80}
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
		Resolver: fakeResolver{known: map[string]string{"10.99.0.38": "p"}},
		Auth:     fakeAuth{user: "op", pass: "s3cret"},
		Dialer:   &fakeDialer{err: io.ErrUnexpectedEOF},
	}
	var addr = startServer(t, srv)
	var c = dialServer(t, addr)
	greet(t, c, "op", "s3cret")
	var rep = connect(t, c, "10.99.0.38", 443)
	if rep != replyHostUnr {
		t.Errorf("reply = %d, want 'host unreachable'", rep)
	}
}

func TestIPv4AndIPv6Destinations(t *testing.T) {
	var dialer = &fakeDialer{asked: make(chan string, 2)}
	var srv = &Server{
		Resolver: fakeResolver{known: map[string]string{
			"10.99.0.38": "p", "fd00::1": "p",
		}},
		Auth:   fakeAuth{user: "op", pass: "s3cret"},
		Dialer: dialer,
	}
	var addr = startServer(t, srv)

	var c = dialServer(t, addr)
	greet(t, c, "op", "s3cret")
	var _, err = c.Write([]byte{version5, cmdConnect, 0, atypIPv4, 10, 99, 0, 38, 0, 80})
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
		if gotV4 != "p 10.99.0.38:80" {
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
