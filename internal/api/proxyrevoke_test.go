package api

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net"
	"os"
	"strings"
	"testing"

	"github.com/hashicorp/yamux"

	"meshcraft/internal/model"
	"meshcraft/internal/store"
	"meshcraft/internal/tunnel"
)

// Revoking a proxy must cut its session, and periodic revalidation must do the same
// even when revocation did not go through this API.
//
// Everything holds here without database or HTTP server: the only PostgreSQL contact
// is the `probe` function passed to cutDisownedTunnels, and that is exactly what must
// be variable — revoked, deleted, database unreachable.

// hubWith mounts a Hub holding one tunnel per named proxy, on real yamux sessions
// (net.Pipe pair, no reserved port). Returns Hub and sessions to assert which closed.
func hubWith(t *testing.T, proxyIDs ...string) (*tunnel.Hub, map[string]*yamux.Session) {
	t.Helper()
	var h = tunnel.NewHub()
	var sessions = map[string]*yamux.Session{}
	var id string
	for _, id = range proxyIDs {
		var a, b = net.Pipe()
		var cfg = yamux.DefaultConfig()
		cfg.EnableKeepAlive = false
		cfg.LogOutput = io.Discard
		// Controller is the yamux client, cf. handleProxyTunnel.
		var client, err = yamux.Client(a, cfg)
		if err != nil {
			t.Fatalf("yamux session impossible: %v", err)
		}
		var server *yamux.Session
		server, err = yamux.Server(b, cfg)
		if err != nil {
			t.Fatalf("yamux session impossible: %v", err)
		}
		t.Cleanup(func() {
			_ = client.Close()
			_ = server.Close()
		})
		h.Register(id, "v1", "", client)
		sessions[id] = client
	}
	return h, sessions
}

func TestRevalidationCutsRevokedProxyInDB(t *testing.T) {
	var h, sessions = hubWith(t, "revoked", "valid")
	var cuts []string
	var n = cutDisownedTunnels(h, func(proxyID string) (tunnelCheck, error) {
		if proxyID == "revoked" {
			return tunnelCheck{verdict: tunnelRevoked, name: "proxy-lab"}, nil
		}
		return tunnelCheck{verdict: tunnelKeep, name: "proxy-local"}, nil
	}, func(proxyID string, c tunnelCheck) {
		cuts = append(cuts, proxyID+"/"+c.name+"/"+c.verdict.reason())
	})

	if n != 1 {
		t.Fatalf("cuts = %d, expected 1", n)
	}
	if !sessions["revoked"].IsClosed() {
		t.Error("revoked proxy session still open: revalidation cut nothing, and the " +
			"proxy keeps access to devices")
	}
	if sessions["valid"].IsClosed() {
		t.Fatal("VALID proxy session was cut: overzealous revalidation would disconnect " +
			"the whole fleet")
	}
	// Name read from DB goes to site log: a UUID is not human-readable.
	if len(cuts) != 1 || cuts[0] != "revoked/proxy-lab/"+revokedTunnelReason {
		t.Errorf("announced cuts = %v", cuts)
	}
}

// The case that must NEVER cut: database unreachable. A read error is not revocation,
// and treating it as such would turn a PostgreSQL hiccup into a fleet-wide disconnect.
func TestRevalidationCutsNothingWhenDBFails(t *testing.T) {
	var h, sessions = hubWith(t, "p1", "p2")
	var boom = errors.New("database connection lost")
	var cuts int
	var n = cutDisownedTunnels(h, func(string) (tunnelCheck, error) {
		return tunnelCheck{}, boom
	}, func(string, tunnelCheck) { cuts++ })

	if n != 0 || cuts != 0 {
		t.Fatalf("cuts = %d (announced %d), expected none on DB error", n, cuts)
	}
	var id string
	for id = range sessions {
		if sessions[id].IsClosed() {
			t.Errorf("session of %s cut although DB did not respond", id)
		}
	}
	if len(h.ProxyIDs()) != 2 {
		t.Errorf("remaining tunnels = %v, both should remain", h.ProxyIDs())
	}
}

// A proxy DELETED from the database: nobody can see or revoke it anymore, but its
// tunnel would still serve. The safety net must catch that too.
func TestRevalidationCutsDeletedProxy(t *testing.T) {
	var h, sessions = hubWith(t, "gone")
	var announced tunnelCheck
	var n = cutDisownedTunnels(h, func(string) (tunnelCheck, error) {
		// What tunnelCheckFor returns on store.ErrNotFound.
		return tunnelCheckFor(model.Proxy{}, store.ErrNotFound)
	}, func(_ string, c tunnelCheck) { announced = c })

	if n != 1 || !sessions["gone"].IsClosed() {
		t.Fatalf("deleted proxy keeps its tunnel (cuts = %d)", n)
	}
	if announced.verdict != tunnelGone {
		t.Errorf("verdict = %d, expected tunnelGone", announced.verdict)
	}
	if announced.verdict.reason() != goneTunnelReason {
		t.Errorf("logged reason = %q", announced.verdict.reason())
	}
}

// Policy, read where it is written: one proxy read → one verdict.
func TestTunnelCheckForOnlyCutsOnCertainty(t *testing.T) {
	var cases = []struct {
		name    string
		p       model.Proxy
		err     error
		verdict tunnelVerdict
		raises  bool // error must propagate to caller
	}{
		{"in good standing", model.Proxy{Name: "p", Revoked: false}, nil, tunnelKeep, false},
		{"revoked", model.Proxy{Name: "p", Revoked: true}, nil, tunnelRevoked, false},
		{"deleted", model.Proxy{}, store.ErrNotFound, tunnelGone, false},
		{"silent DB", model.Proxy{}, errors.New("timeout"), tunnelKeep, true},
		// Wrapped error is still ErrNotFound: triage uses sentinel, not text.
		{"deleted (wrapped error)", model.Proxy{},
			errors.Join(errors.New("get proxy"), store.ErrNotFound), tunnelGone, false},
	}
	var c = cases[0]
	for _, c = range cases {
		t.Run(c.name, func(t *testing.T) {
			var got, err = tunnelCheckFor(c.p, c.err)
			if got.verdict != c.verdict {
				t.Errorf("verdict = %d, expected %d", got.verdict, c.verdict)
			}
			if (err != nil) != c.raises {
				t.Errorf("returned error = %v, expected raise = %v", err, c.raises)
			}
			// NULL verdict is "keep": that protects the fleet from a read error treated
			// as refusal.
			if err != nil && got.verdict != tunnelKeep {
				t.Errorf("error yields cut verdict (%d)", got.verdict)
			}
		})
	}
}

// tunnelKeep must be the ZERO value of tunnelVerdict: the verdict returned on failed
// read, forgotten struct, unset field. If it meant "cut", any hole in the code would
// disconnect the fleet.
func TestNullVerdictDoesNotCut(t *testing.T) {
	var zero tunnelCheck
	if zero.verdict != tunnelKeep {
		t.Fatal("zero value of tunnelVerdict is not \"keep tunnel\"")
	}
	if tunnelKeep.reason() != "" {
		t.Error("tunnelKeep carries a cut reason")
	}
}

// ORDER, and it cannot be locked otherwise: database first, session second.
//
// Cutting before writing revocation would offer a mere disconnect — the proxy
// reconnects within a second (tunnel backoff restarts at zero after a session that
// held) and authentication would still accept it, since the database does not know yet.
// No execution test can distinguish the two orders without DB and a real proxy;
// syntax analysis can, and that pattern is already used in this package (cf.
// portevents_test.go).
func TestRevocationWritesDBBeforeCutting(t *testing.T) {
	// Three gestures that remove a proxy's right to a tunnel, and the DB write that
	// must precede the cut in each.
	var expected = map[string]string{
		"handleRevokeProxy":   "SetProxyRevoked",
		"handleProxyMigrated": "MarkProxyMigrated",
		"handleDeleteProxy":   "DeleteProxy",
	}
	var seen = map[string]bool{}

	var entries, err = os.ReadDir(".")
	if err != nil {
		t.Fatalf("package unreadable: %v", err)
	}
	var fset = token.NewFileSet()
	var entry os.DirEntry
	for _, entry = range entries {
		var name = entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") ||
			strings.HasSuffix(name, "_test.go") {
			continue
		}
		var file, parseErr = parser.ParseFile(fset, name, nil, 0)
		if parseErr != nil {
			t.Fatalf("%s unreadable: %v", name, parseErr)
		}
		var decl ast.Decl
		for _, decl = range file.Decls {
			var fn, isFn = decl.(*ast.FuncDecl)
			if !isFn || fn.Body == nil {
				continue
			}
			var write, ok = expected[fn.Name.Name]
			if !ok {
				continue
			}
			seen[fn.Name.Name] = true
			var posWrite, posCut = callsInOrder(fn, write, "CloseSession")
			if posWrite == token.NoPos {
				t.Errorf("%s no longer touches %s: test no longer aligned with code",
					fn.Name.Name, write)
				continue
			}
			if posCut == token.NoPos {
				t.Errorf("%s does not call CloseSession: revoking without cutting session "+
					"only revokes NEXT authentication, while established session suffices to "+
					"reach devices", fn.Name.Name)
				continue
			}
			if posWrite > posCut {
				t.Errorf("%s cuts session (line %d) BEFORE writing DB (line %d): proxy would "+
					"reconnect immediately and authentication would still accept it — a "+
					"disconnect, not revocation",
					fn.Name.Name, fset.Position(posCut).Line, fset.Position(posWrite).Line)
			}
		}
	}
	var name string
	for name = range expected {
		if !seen[name] {
			t.Errorf("%s not found in package: this lock proves nothing", name)
		}
	}
}

// callsInOrder returns position of FIRST call to each named method in the function
// body. token.NoPos = not found.
func callsInOrder(fn *ast.FuncDecl, first, second string) (token.Pos, token.Pos) {
	var posFirst, posSecond = token.NoPos, token.NoPos
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		var call, isCall = n.(*ast.CallExpr)
		if !isCall {
			return true
		}
		var sel, isSel = call.Fun.(*ast.SelectorExpr)
		if !isSel {
			return true
		}
		switch sel.Sel.Name {
		case first:
			if posFirst == token.NoPos {
				posFirst = call.Pos()
			}
		case second:
			if posSecond == token.NoPos {
				posSecond = call.Pos()
			}
		}
		return true
	})
	return posFirst, posSecond
}

// A hand-built Server — no hub or DB, like other tests in this package — must not
// crash the background goroutine. A panic in periodic sweep is not caught by
// middleware.Recoverer: it takes down the controller, therefore the whole fleet.
func TestRevalidationWithoutHubOrDBDoesNotPanic(t *testing.T) {
	var s = &Server{}
	s.revalidateTunnelsOnce()
}
