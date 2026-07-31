package tunnel

import (
	"io"
	"net"
	"testing"
	"time"

	"github.com/hashicorp/yamux"
)

// The FIRST tests in this package, and they cover what was just fixed here:
// on-demand session closure.
//
// The Hub is tested without network or controller: yamux only needs a net.Conn,
// and a net.Pipe pair provides two in-memory ends -- with deadlines, since Go 1.10,
// which yamux needs. No port to reserve, so no test fails on a busy machine, and no
// socket survives the test.

// pairYamux mounts a real yamux session and returns the CONTROLLER side, i.e. the
// one the Hub holds (the controller is the yamux client, cf. api.handleProxyTunnel:
// it opens streams).
func pairYamux(t *testing.T) *yamux.Session {
	t.Helper()
	var a, b = net.Pipe()
	var cfg = yamux.DefaultConfig()
	// No keepalive: on an in-memory pair there is nothing to watch, and periodic
	// ping would keep a goroutine alive after the test ends.
	cfg.EnableKeepAlive = false
	cfg.LogOutput = io.Discard

	var client, err = yamux.Client(a, cfg)
	if err != nil {
		t.Fatalf("yamux session (client) failed: %v", err)
	}
	var server *yamux.Session
	server, err = yamux.Server(b, cfg)
	if err != nil {
		t.Fatalf("yamux session (server) failed: %v", err)
	}
	t.Cleanup(func() {
		_ = client.Close()
		_ = server.Close()
	})
	return client
}

// The central property of the suite: a revoked session is CUT, and the Hub no longer
// holds it. While it stayed open, revocation only applied to the next authentication,
// yet the existing session was enough to reach devices and receive SNMP communities
// from the fleet.
func TestCloseSessionCoupeEtOublieLaSession(t *testing.T) {
	var h = NewHub()
	var ymux = pairYamux(t)
	var release = h.Register("p1", "v1", ymux)
	defer release()

	if !h.Connected("p1") {
		t.Fatal("tunnel should be established before cut")
	}
	if !h.CloseSession("p1", "proxy revoked") {
		t.Fatal("CloseSession reports nothing cut while session was alive")
	}
	if !ymux.IsClosed() {
		t.Error("yamux session still open: revocation did not cut anything")
	}
	if h.Connected("p1") {
		t.Error("Hub still holds this proxy as connected after cut")
	}
	// Removed from table immediately, without waiting for release: otherwise a
	// periodic caller would find the same session on every pass and replay its log line.
	if len(h.ProxyIDs()) != 0 {
		t.Errorf("ProxyIDs still returns %v after cut", h.ProxyIDs())
	}
	// The Hub is the only gateway to a proxy; nothing must get through anymore.
	var _, dialErr = h.Dial(t.Context(), "p1", "10.0.0.1", 22)
	if dialErr == nil {
		t.Error("Dial still succeeds on a proxy whose session was cut")
	}
}

// The mirror risk, and the worst one: an overly broad cut would disconnect the whole
// fleet. A proxy that is not targeted must feel nothing.
func TestCloseSessionLaisseLesAutresProxysTranquilles(t *testing.T) {
	var h = NewHub()
	var revoked = pairYamux(t)
	var valid = pairYamux(t)
	defer h.Register("revoked", "v1", revoked)()
	defer h.Register("valid", "v1", valid)()

	h.CloseSession("revoked", "proxy revoked")

	if valid.IsClosed() || !h.Connected("valid") {
		t.Fatal("session of a NON-targeted proxy was cut: revocation must " +
			"never spill over to the rest of the fleet")
	}
	var ids = h.ProxyIDs()
	if len(ids) != 1 || ids[0] != "valid" {
		t.Errorf("remaining tunnels = %v, expected [valid]", ids)
	}
}

// A proxy with no tunnel: nothing to cut, and the caller must be able to say so --
// that is what lets it avoid announcing a cut that did not happen.
func TestCloseSessionSansTunnel(t *testing.T) {
	var h = NewHub()
	if h.CloseSession("never-seen", "proxy revoked") {
		t.Error("CloseSession announces a cut when no tunnel existed")
	}
}

// A session already dead but not yet reaped (tunnel handler has not returned) does
// not count as a cut: otherwise periodic revalidation would write a log line on every
// pass for a tunnel nobody cut.
func TestCloseSessionNAnnonceRienSurUneSessionDejaMorte(t *testing.T) {
	var h = NewHub()
	var ymux = pairYamux(t)
	defer h.Register("p1", "v1", ymux)()
	_ = ymux.Close()

	if h.CloseSession("p1", "proxy revoked") {
		t.Error("CloseSession announces a cut on an already closed session")
	}
	if len(h.ProxyIDs()) != 0 {
		t.Error("dead session was not removed from table")
	}
}

// Register invariant that makes early removal safe: release from a cut session must
// not evict a reconnection that arrived since. A revoked proxy reconnects in a loop --
// authentication will refuse it -- but a reactivated proxy comes back for good.
func TestCloseSessionPuisReconnexionImmediate(t *testing.T) {
	var h = NewHub()
	var old = pairYamux(t)
	var releaseOld = h.Register("p1", "v1", old)
	h.CloseSession("p1", "proxy revoked")

	var fresh = pairYamux(t)
	defer h.Register("p1", "v2", fresh)()

	// Old session release arrives AFTER reconnection: real order when a proxy returns
	// faster than the handler it left.
	releaseOld()

	if !h.Connected("p1") {
		t.Fatal("cut session release evicted reconnection")
	}
	if fresh.IsClosed() {
		t.Error("new session was closed by old session release")
	}
}

// ProxyIDs is what periodic revalidation walks: it must return established tunnels
// without PINGing them, unlike List(). Content is verified here; absence of ping is
// visible in code (no round trip possible on a session whose other end does not read).
func TestProxyIDsRendLesTunnelsEtablis(t *testing.T) {
	var h = NewHub()
	defer h.Register("p1", "v1", pairYamux(t))()
	defer h.Register("p2", "v1", pairYamux(t))()

	var ids = h.ProxyIDs()
	if len(ids) != 2 {
		t.Fatalf("ProxyIDs = %v, expected two tunnels", ids)
	}
	var seen = map[string]bool{}
	var id string
	for _, id = range ids {
		seen[id] = true
	}
	if !seen["p1"] || !seen["p2"] {
		t.Errorf("ProxyIDs = %v, expected p1 and p2", ids)
	}
}

// Register closes a same-proxy previous session -- behavior already in place and never
// tested, on which cut relies: same gesture (close a yamux) and same table.
func TestRegisterFermeLaSessionPrecedente(t *testing.T) {
	var h = NewHub()
	var first = pairYamux(t)
	h.Register("p1", "v1", first)
	var second = pairYamux(t)
	defer h.Register("p1", "v2", second)()

	// Close happens outside Register lock: allow a moment.
	var deadline = time.Now().Add(2 * time.Second)
	for !first.IsClosed() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !first.IsClosed() {
		t.Error("previous session stayed open after re-enrollment")
	}
	if second.IsClosed() {
		t.Error("new session was closed")
	}
}
