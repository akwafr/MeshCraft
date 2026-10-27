// Package socks serves a SOCKS5 proxy whose connections leave from the
// meshcraft proxy that can actually reach the device.
//
// # Why this exists, and what it replaces
//
// A device's management address sits on a VLAN the operator's browser cannot
// route to — that separation is the point of the proxy — so a vendor web
// interface, which on some ranges is the ONLY way to configure the device, was
// simply not openable.
//
// The first attempt at closing that was a rewriting reverse proxy: fetch the
// page through the tunnel, rewrite every URL back under the controller's origin,
// inject a shim for the ones scripts compute. It worked in tests and was the
// wrong shape. A vendor UI is an application, not a document — it computes URLs,
// it sets cookies scoped to paths, it ships bundles that a regex must never
// touch — and a rewriter is a permanent bet that none of that changes at the next
// firmware. Losing that bet looks like a broken switch, not a broken proxy.
//
// SOCKS moves the boundary to where it belongs. The browser talks to the device
// at the device's OWN address, with the device's own cookies, TLS and paths; the
// only thing meshcraft supplies is the route. Nothing is rewritten, so nothing
// can be rewritten wrongly.
//
// # What this deliberately is NOT
//
// It is not a general-purpose proxy into a customer's network. A CONNECT is
// resolved against the INVENTORY: the destination must be a device meshcraft
// knows, and the connection then leaves from that device's own proxy. An address
// that is not in the inventory is refused. The difference matters — "reach my
// switches" and "reach anything on the customer LAN" are not the same door, and
// only one of them is being opened here.
//
// It also carries NO TLS: SOCKS5 has none. The listener is off unless configured,
// and the deployment is expected to bind it to localhost or put it behind
// something that does provide confidentiality.
package socks

import (
	"context"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strconv"
	"time"
)

const (
	version5 = 0x05
	// authUserPass: RFC 1929. The ONLY method offered — "no authentication"
	// would make the route into a customer network available to whoever can
	// reach the port.
	authUserPass  = 0x02
	authNoneOfMy  = 0xFF
	authSubVer    = 0x01
	cmdConnect    = 0x01
	atypIPv4      = 0x01
	atypDomain    = 0x03
	atypIPv6      = 0x04
	replyOK       = 0x00
	replyGeneral  = 0x01
	replyNotAllow = 0x02
	replyHostUnr  = 0x04
	replyCmdUnsup = 0x07

	// handshakeTimeout bounds greeting, authentication and the request. A peer
	// that opens a socket and says nothing must not hold a goroutine.
	handshakeTimeout = 15 * time.Second
	// dialTimeout bounds the outbound connection through the tunnel.
	dialTimeout = 15 * time.Second
	// maxIdle bounds a relayed connection with no traffic in either direction.
	// A browser tab left open on a switch UI otherwise holds a tunnel stream
	// until someone closes the laptop.
	maxIdle = 30 * time.Minute
)

// Resolver maps a destination onto the proxy that can reach it.
//
// Returning ok=false REFUSES the connection, and that is the security boundary
// of this package: the implementation is expected to answer only for addresses
// that belong to a device in the inventory.
type Resolver interface {
	// ProxyFor answers for ONE subject: the authenticated operator, as
	// [Authenticator] returned them. Passing the subject is what makes this an
	// authorization decision rather than a routing table — without it, anyone
	// holding a valid token would reach every device in the instance, including
	// sites they have no role on.
	ProxyFor(ctx context.Context, subject, host string) (proxyID, deviceName string, ok bool)
}

// Authenticator checks the SOCKS5 username/password pair.
//
// `label` is what the audit line records — an operator identity, not the
// credential. It is returned rather than derived so the caller decides what a
// session is attributed to.
type Authenticator interface {
	Authenticate(ctx context.Context, user, pass string) (subject, label string, ok bool)
}

// Dialer opens a TCP connection through a named meshcraft proxy. Satisfied by
// tunnel.Hub.
type Dialer interface {
	Dial(ctx context.Context, proxyID, host string, port int) (net.Conn, error)
}

// Server is a SOCKS5 listener. Every field is required.
type Server struct {
	Resolver Resolver
	Auth     Authenticator
	Dialer   Dialer
	// OnSession reports a completed relay, for the audit trail. Optional.
	OnSession func(label, deviceName, host string, port int, sent, received int64, d time.Duration)
	// OnRefusal reports a refused CONNECT. Optional, and worth wiring: a refusal
	// is the interesting event here, not a success.
	OnRefusal func(label, host string, port int, reason string)
}

// Serve accepts connections until the listener is closed or ctx is done.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()
	for {
		var conn, err = ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go s.handle(ctx, conn)
	}
}

func (s *Server) handle(ctx context.Context, conn net.Conn) {
	defer conn.Close() //nolint:errcheck
	_ = conn.SetDeadline(time.Now().Add(handshakeTimeout))

	var subject, label, err = s.negotiate(ctx, conn)
	if err != nil {
		return
	}
	var host string
	var port int
	host, port, err = readRequest(conn)
	if err != nil {
		return
	}

	var proxyID, deviceName, known = s.Resolver.ProxyFor(ctx, subject, host)
	if !known {
		// NOT a generic proxy (cf. the package header). An unknown destination is
		// refused with "connection not allowed", which is the truthful SOCKS reply
		// — the route exists, the policy does not permit it.
		s.refuse(label, host, port, "not a device meshcraft knows")
		_ = writeReply(conn, replyNotAllow)
		return
	}

	var dctx, cancel = context.WithTimeout(ctx, dialTimeout)
	var up net.Conn
	up, err = s.Dialer.Dial(dctx, proxyID, host, port)
	cancel()
	if err != nil {
		s.refuse(label, host, port, "proxy could not reach it: "+err.Error())
		_ = writeReply(conn, replyHostUnr)
		return
	}
	defer up.Close() //nolint:errcheck

	if writeReply(conn, replyOK) != nil {
		return
	}
	// Handshake is over: from here the deadline belongs to the relay, and an
	// operator reading a configuration page is legitimately idle for minutes.
	_ = conn.SetDeadline(time.Time{})

	var started = time.Now()
	var sent, received = relay(conn, up)
	if s.OnSession != nil {
		s.OnSession(label, deviceName, host, port, sent, received, time.Since(started))
	}
}

func (s *Server) refuse(label, host string, port int, reason string) {
	if s.OnRefusal != nil {
		s.OnRefusal(label, host, port, reason)
	}
}

// negotiate runs the method greeting and RFC 1929 authentication.
//
// ONLY username/password is offered. A client that will not do it is told so
// (0xFF) and dropped, rather than being quietly downgraded — an "authentication
// optional" proxy into a management network is an unauthenticated one.
func (s *Server) negotiate(ctx context.Context, conn net.Conn) (string, string, error) {
	var head = make([]byte, 2)
	var _, err = io.ReadFull(conn, head)
	if err != nil {
		return "", "", err
	}
	if head[0] != version5 {
		return "", "", errors.New("not SOCKS5")
	}
	var methods = make([]byte, int(head[1]))
	if _, err = io.ReadFull(conn, methods); err != nil {
		return "", "", err
	}
	var offered bool
	var m byte
	for _, m = range methods {
		if m == authUserPass {
			offered = true
		}
	}
	if !offered {
		_, _ = conn.Write([]byte{version5, authNoneOfMy})
		return "", "", errors.New("client will not authenticate")
	}
	if _, err = conn.Write([]byte{version5, authUserPass}); err != nil {
		return "", "", err
	}

	var sub = make([]byte, 2)
	if _, err = io.ReadFull(conn, sub); err != nil {
		return "", "", err
	}
	if sub[0] != authSubVer {
		return "", "", errors.New("bad auth version")
	}
	var user = make([]byte, int(sub[1]))
	if _, err = io.ReadFull(conn, user); err != nil {
		return "", "", err
	}
	var plen = make([]byte, 1)
	if _, err = io.ReadFull(conn, plen); err != nil {
		return "", "", err
	}
	var pass = make([]byte, int(plen[0]))
	if _, err = io.ReadFull(conn, pass); err != nil {
		return "", "", err
	}

	var subject, label, ok = s.Auth.Authenticate(ctx, string(user), string(pass))
	if !ok {
		_, _ = conn.Write([]byte{authSubVer, 0x01})
		return "", "", errors.New("authentication refused")
	}
	if _, err = conn.Write([]byte{authSubVer, 0x00}); err != nil {
		return "", "", err
	}
	return subject, label, nil
}

// readRequest reads one SOCKS5 request and returns its destination.
//
// CONNECT only. BIND and UDP ASSOCIATE are refused: neither has a meaning when
// the far side is a tunnel stream the controller opened, and answering them with
// anything but "not supported" would promise a channel that does not exist.
func readRequest(conn net.Conn) (string, int, error) {
	var head = make([]byte, 4)
	var _, err = io.ReadFull(conn, head)
	if err != nil {
		return "", 0, err
	}
	if head[0] != version5 {
		return "", 0, errors.New("not SOCKS5")
	}
	if head[1] != cmdConnect {
		_ = writeReply(conn, replyCmdUnsup)
		return "", 0, errors.New("only CONNECT is served")
	}
	var host string
	switch head[3] {
	case atypIPv4:
		var b = make([]byte, 4)
		if _, err = io.ReadFull(conn, b); err != nil {
			return "", 0, err
		}
		host = net.IP(b).String()
	case atypIPv6:
		var b = make([]byte, 16)
		if _, err = io.ReadFull(conn, b); err != nil {
			return "", 0, err
		}
		host = net.IP(b).String()
	case atypDomain:
		var n = make([]byte, 1)
		if _, err = io.ReadFull(conn, n); err != nil {
			return "", 0, err
		}
		var b = make([]byte, int(n[0]))
		if _, err = io.ReadFull(conn, b); err != nil {
			return "", 0, err
		}
		host = string(b)
	default:
		_ = writeReply(conn, replyGeneral)
		return "", 0, errors.New("unknown address type")
	}
	var pb = make([]byte, 2)
	if _, err = io.ReadFull(conn, pb); err != nil {
		return "", 0, err
	}
	return host, int(binary.BigEndian.Uint16(pb)), nil
}

// writeReply answers a request. The bound address is reported as 0.0.0.0:0:
// the real one is a tunnel stream with no address a client could use, and
// inventing one would tell the client something false about its own path.
func writeReply(conn net.Conn, code byte) error {
	var _, err = conn.Write([]byte{version5, code, 0x00, atypIPv4, 0, 0, 0, 0, 0, 0})
	return err
}

// relay copies in both directions until either end stops, and returns the byte
// counts for the audit line.
//
// Both directions are closed as soon as ONE finishes. A half-open relay is the
// classic way a proxy accumulates goroutines on a device that hangs up without a
// FIN in the other direction, and on a platform that only tolerates a handful of
// sessions that costs the operator their access.
func relay(client, upstream net.Conn) (int64, int64) {
	return relayWithIdle(client, upstream, maxIdle)
}

// relayWithIdle is relay with the idle bound made an argument.
//
// The bound exists for the client that VANISHES WITHOUT A FIN — a laptop that
// sleeps, a Wi-Fi drop, a browser process killed — where neither io.Copy ever
// returns and the tunnel stream behind `upstream` is held open indefinitely. On
// a range that tolerates four concurrent sessions, a handful of those is the
// operator's access. That case cannot be reproduced through the public entry
// point without sitting out [maxIdle], so the bound is a parameter here and the
// constant is applied by the one production caller above.
func relayWithIdle(client, upstream net.Conn, limit time.Duration) (int64, int64) {
	var idle = newIdleGuard(client, upstream, limit)
	defer idle.stop()

	type result struct{ n int64 }
	var toUpstream = make(chan result, 1)
	go func() {
		var n, _ = io.Copy(upstream, idle.reader(client))
		// THE HALF-CLOSE IS THE WHOLE LEAK. When it works, the device sees EOF
		// and answers with its own, the copy below returns, and the deferred
		// Close runs. When the upstream CANNOT half-close — every tunnel stream,
		// because yamux's Stream has no CloseWrite — the device is never told
		// anything, so a device sitting at a prompt never speaks first, the copy
		// below blocks, and the stream lives until the idle guard fires half an
		// hour later. MEASURED 2026-08-10 on unifimoule1: one leaked stream per
		// SOCKS connection, reaped at ~29-30 min, and the client had hung up
		// CLEANLY — so this is not the fin-less case the guard was built for.
		//
		// If the peer cannot be told, there is nothing to wait for: tearing the
		// pair down is what the proxy side already does (its `pipe` returns on
		// the FIRST direction to finish). This restores that symmetry for the
		// connections that cannot signal, and leaves genuine half-close intact
		// for the ones that can — a client that shuts down its write side and
		// keeps reading is a real pattern, and closing on it would be a worse
		// bug than the leak.
		if !closeWrite(upstream) {
			_ = client.Close()
			_ = upstream.Close()
		}
		toUpstream <- result{n}
	}()
	var received, _ = io.Copy(client, idle.reader(upstream))
	closeWrite(client)
	var sent = <-toUpstream
	return received, sent.n
}

// closeWrite half-closes when the connection supports it, so the peer sees EOF
// instead of waiting, and REPORTS WHETHER IT DID.
//
// The boolean is the point. This returned an error before, and the one thing
// that could go wrong here does not surface as one: a connection that has no
// CloseWrite at all took the nil-error path, indistinguishable from a
// half-close that worked. Both callers discarded it with `_ =`, so the case the
// comment had already identified — "tunnel streams do not" — was silently a
// success, and the caller went on to wait for an EOF that could never arrive.
// A false here means the peer WILL NOT be told; see the caller for what that
// costs and what it now does about it.
func closeWrite(c net.Conn) bool {
	var cw, ok = c.(interface{ CloseWrite() error })
	if !ok {
		return false
	}
	return cw.CloseWrite() == nil
}

// idleGuard closes both connections when nothing has moved for the timeout.
//
// A deadline on each connection would not do: the two directions are idle at
// different times — a page loads, then nothing travels while it is read — and a
// per-connection deadline would cut a session that is merely quiet. What matters
// is that NEITHER direction has moved.
type idleGuard struct {
	a, b  net.Conn
	beat  chan struct{}
	done  chan struct{}
	limit time.Duration
}

func newIdleGuard(a, b net.Conn, limit time.Duration) *idleGuard {
	var g = &idleGuard{a: a, b: b, beat: make(chan struct{}, 1),
		done: make(chan struct{}), limit: limit}
	go g.run()
	return g
}

func (g *idleGuard) run() {
	var t = time.NewTimer(g.limit)
	defer t.Stop()
	for {
		select {
		case <-g.beat:
			if !t.Stop() {
				select {
				case <-t.C:
				default:
				}
			}
			t.Reset(g.limit)
		case <-t.C:
			_ = g.a.Close()
			_ = g.b.Close()
			return
		case <-g.done:
			return
		}
	}
}

func (g *idleGuard) stop() { close(g.done) }

func (g *idleGuard) reader(c net.Conn) io.Reader {
	return readerFunc(func(p []byte) (int, error) {
		var n, err = c.Read(p)
		if n > 0 {
			select {
			case g.beat <- struct{}{}:
			default:
			}
		}
		return n, err
	})
}

type readerFunc func([]byte) (int, error)

func (f readerFunc) Read(p []byte) (int, error) { return f(p) }

// ConstantTimeMatch compares a presented secret with the expected one without
// leaking its length through timing.
//
// Exported because the Authenticator implementation lives in the API layer,
// where the tokens are stored, and a comparison written a second time is a
// comparison written with `==` once.
func ConstantTimeMatch(presented, expected string) bool {
	if expected == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(presented), []byte(expected)) == 1
}

// HostPort renders a destination the way the audit line shows it.
func HostPort(host string, port int) string {
	return net.JoinHostPort(host, strconv.Itoa(port))
}
