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
func TestCloseSessionCutsAndForgetsTheSession(t *testing.T) {
	var h = NewHub()
	var ymux = pairYamux(t)
	var release = h.Register("p1", "v1", "", ymux)
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
func TestCloseSessionLeavesOtherProxiesAlone(t *testing.T) {
	var h = NewHub()
	var revoked = pairYamux(t)
	var valid = pairYamux(t)
	defer h.Register("revoked", "v1", "", revoked)()
	defer h.Register("valid", "v1", "", valid)()

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
func TestCloseSessionWithoutTunnel(t *testing.T) {
	var h = NewHub()
	if h.CloseSession("never-seen", "proxy revoked") {
		t.Error("CloseSession announces a cut when no tunnel existed")
	}
}

// A session already dead but not yet reaped (tunnel handler has not returned) does
// not count as a cut: otherwise periodic revalidation would write a log line on every
// pass for a tunnel nobody cut.
func TestCloseSessionAnnouncesNothingOnADeadSession(t *testing.T) {
	var h = NewHub()
	var ymux = pairYamux(t)
	defer h.Register("p1", "v1", "", ymux)()
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
func TestCloseSessionThenImmediateReconnect(t *testing.T) {
	var h = NewHub()
	var old = pairYamux(t)
	var releaseOld = h.Register("p1", "v1", "", old)
	h.CloseSession("p1", "proxy revoked")

	var fresh = pairYamux(t)
	defer h.Register("p1", "v2", "", fresh)()

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
func TestProxyIDsReturnsEstablishedTunnels(t *testing.T) {
	var h = NewHub()
	defer h.Register("p1", "v1", "", pairYamux(t))()
	defer h.Register("p2", "v1", "", pairYamux(t))()

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
func TestRegisterClosesThePreviousSession(t *testing.T) {
	var h = NewHub()
	var first = pairYamux(t)
	h.Register("p1", "v1", "", first)
	var second = pairYamux(t)
	defer h.Register("p1", "v2", "", second)()

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

// --- Resync tracking, and the compatibility contract it carries ---

// THE OLD PROXY, which is the property that must never break. A session that did not
// announce FeatureResyncDone will never send an end-of-burst marker, and the Hub says
// so with a state of its own instead of leaving it pending forever: a controller that
// waited on it would freeze a fleet that was working yesterday, and the fleet is not
// upgraded atomically.
func TestResyncUnsupportedForAProxyThatAnnouncesNothing(t *testing.T) {
	var h = NewHub()
	defer h.Register("p1", "v1", "", pairYamux(t))()

	if h.Resync("p1") != ResyncUnsupported {
		t.Fatalf("Resync = %v, want unsupported: this proxy announced no capability",
			h.Resync("p1"))
	}
	// And an unrelated capability list is not a claim to this one.
	defer h.Register("p2", "v1", "traps,lldp-push", pairYamux(t))()
	if h.Resync("p2") != ResyncUnsupported {
		t.Errorf("Resync = %v, want unsupported: another feature is not this one",
			h.Resync("p2"))
	}
}

// A proxy nobody holds a session for has said nothing at all -- distinct from "cannot
// say". The two are separated because only one of them is worth waiting for.
func TestResyncAbsentWithoutASession(t *testing.T) {
	var h = NewHub()
	if h.Resync("never-seen") != ResyncAbsent {
		t.Errorf("Resync = %v, want absent", h.Resync("never-seen"))
	}
}

// THE FULL PATH, on a real yamux pair and through the real event reader: a proxy that
// announces the capability starts pending, and the marker it writes at the end of its
// burst is what moves it to done. The Hub records this ITSELF -- the handler is only
// told -- so no controller wiring can lose it.
func TestResyncPendingUntilTheProxyClosesItsBurst(t *testing.T) {
	var h = NewHub()
	var proxy, controller = pairYamuxBoth(t)
	defer h.Register("p1", "v1", FeatureResyncDone, controller)()

	if h.Resync("p1") != ResyncPending {
		t.Fatalf("Resync = %v, want pending: the burst has not been closed",
			h.Resync("p1"))
	}

	var seen = make(chan Event, 8)
	go h.AcceptEvents("p1", func(_ string, ev Event) { seen <- ev })

	var events = make(chan Event, 4)
	go func() { _ = SendEvents(proxy, events, nil) }()

	var deadline = time.Now().Add(2 * time.Second)
	for h.Resync("p1") != ResyncDone && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if h.Resync("p1") != ResyncDone {
		t.Fatalf("Resync = %v, want done: the proxy closed its burst", h.Resync("p1"))
	}
	// Forwarded as well as recorded: that journal line is the only trace an operator
	// has of a proxy declaring itself caught up.
	var ev Event
	select {
	case ev = <-seen:
		if ev.Type != EventResyncDone {
			t.Errorf("handler saw %q, want %q", ev.Type, EventResyncDone)
		}
	case <-time.After(2 * time.Second):
		t.Error("the marker was recorded but never handed to the handler")
	}

	// A RECONNECTION IS A NEW BURST. The state lives on the session, so the proxy is
	// pending again without anyone having to remember to reset it -- which is exactly
	// what a controller restart looks like from here.
	defer h.Register("p1", "v1", FeatureResyncDone, pairYamux(t))()
	if h.Resync("p1") != ResyncPending {
		t.Errorf("Resync = %v after reconnection, want pending", h.Resync("p1"))
	}
}

// The capability list crosses an HTTP header written by a binary we do not control, so
// its parsing is tolerant on form and strict on content: a near-miss token is not the
// capability, and claiming it by accident would make the controller wait for a marker
// that is never coming.
func TestHasFeatureIsTolerantOnFormAndStrictOnContent(t *testing.T) {
	if !HasFeature("resync-done", FeatureResyncDone) {
		t.Error("the plain token was not recognized")
	}
	if !HasFeature("traps, resync-done , lldp", FeatureResyncDone) {
		t.Error("spacing in the list defeated the match")
	}
	if !HasFeature("Resync-Done", FeatureResyncDone) {
		t.Error("case in the list defeated the match")
	}
	if HasFeature("", FeatureResyncDone) {
		t.Error("an empty list announced a capability")
	}
	if HasFeature("resync-done-maybe", FeatureResyncDone) {
		t.Error("a longer token was accepted as this one")
	}
}
