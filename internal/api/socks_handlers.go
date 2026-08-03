package api

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"meshcraft/internal/auth"
	"meshcraft/internal/model"
	"meshcraft/internal/socks"
)

// The SOCKS route: meshcraft as the way a BROWSER reaches a device's own
// administration interface.
//
// The transport lives in internal/socks, which knows nothing about meshcraft.
// This file supplies the two decisions that are meshcraft's: who may use the
// route, and which device an address names.
//
// # Authorization is per user AND per site
//
// A valid token is not enough. Every CONNECT is resolved to a device, and the
// operator must hold a role on THAT device's site. Without the second half, one
// token would reach every device in the instance — an instance being, by design,
// several customers.
//
// # Why the token is separate from the session
//
// SOCKS5 authenticates with a username and password (RFC 1929); it has no notion
// of cookies. A browser configured with a SOCKS proxy presents credentials to the
// proxy, not to the site, so the session that authenticates the UI cannot
// authenticate the route. Hence a second credential — hashed, shown once, and
// revoked with the account (cf. migration 0036).

// socksAuth answers the SOCKS5 username/password exchange.
type socksAuth struct{ s *Server }

// Authenticate resolves the SOCKS username (an operator's email) and token.
//
// An unknown user and a user with no token are answered IDENTICALLY, and the
// timing is not made to depend on which: distinguishing them turns the proxy into
// an oracle for which accounts exist on the instance.
func (a socksAuth) Authenticate(ctx context.Context, user, pass string) (string, string, bool) {
	var userID, hash, found, err = a.s.Store.SocksTokenFor(ctx, strings.TrimSpace(user))
	if err != nil {
		slog.Warn("socks: token lookup failed", "err", err.Error())
		return "", "", false
	}
	if !found {
		// Still spend the comparison, against a value that cannot match, so a
		// nonexistent account does not answer measurably faster than a wrong
		// token.
		_ = socks.ConstantTimeMatch(pass, "")
		return "", "", false
	}
	if !socks.ConstantTimeMatch(string(auth.HashToken(pass)), string(hash)) {
		return "", "", false
	}
	a.s.Store.TouchSocksToken(ctx, userID)
	return userID, strings.TrimSpace(user), true
}

// socksResolver maps a destination address onto the device it belongs to, and
// refuses everything else.
type socksResolver struct{ s *Server }

// ProxyFor answers only for an address that IS a device this operator may reach.
//
// Two refusals, deliberately indistinguishable from the client's side: "no device
// has this address" and "you have no role on its site". Telling them apart would
// let a token holder map the instance's addressing by watching which refusals
// differ.
//
// When several devices share an address — two sites on the same private range,
// which is ordinary — the one the operator has rights on wins. If that is still
// several, the request is refused rather than guessed: sending a browser to the
// wrong customer's switch is not a mistake worth risking to save a click.
func (r socksResolver) ProxyFor(ctx context.Context, subject, host string) (string, string, bool) {
	var routes, err = r.s.Store.DevicesByMgmtIP(ctx, host)
	if err != nil || len(routes) == 0 {
		return "", "", false
	}
	var allowed []int
	var i int
	for i = range routes {
		if r.s.Auth.CanSiteFor(ctx, subject, routes[i].SiteID, "operator") {
			allowed = append(allowed, i)
		}
	}
	if len(allowed) != 1 {
		return "", "", false
	}
	var route = routes[allowed[0]]
	var proxyID = route.ProxyID
	if proxyID == "" {
		var d, getErr = r.s.Store.GetDevice(ctx, route.DeviceID)
		if getErr != nil {
			return "", "", false
		}
		proxyID = r.s.proxyOf(ctx, d)
	}
	if !r.s.Tunnels.Connected(proxyID) {
		return "", "", false
	}
	return proxyID, route.Name, true
}

// StartSocks starts the SOCKS listener, or explains why it is not starting.
//
// OFF unless MESHCRAFT_SOCKS_LISTEN is set. This is a door into customer
// management networks and it does not open by accident.
//
// A NON-LOOPBACK BIND IS WARNED ABOUT, LOUDLY, and still honoured. SOCKS5 has no
// transport security: on a listener the internet can reach, the token and
// everything the browser exchanges with the switch — including whatever is typed
// into a vendor login form — cross in clear. Binding to localhost and reaching it
// over an SSH forward gives the same experience with none of that. The choice
// stays the operator's; being quiet about it would not.
func (s *Server) StartSocks(ctx context.Context, listen string) {
	listen = strings.TrimSpace(listen)
	if listen == "" {
		return
	}
	var host, _, err = net.SplitHostPort(listen)
	if err == nil && !isLoopbackHost(host) {
		slog.Warn("socks: listening beyond loopback WITHOUT transport security",
			"listen", listen,
			"why", "SOCKS5 has no TLS: the token and every byte exchanged with the "+
				"device cross in clear on this listener. In a container this bind is "+
				"normal — a published port cannot reach a loopback bind — and it is "+
				"then the PORT MAPPING that must restrict it (127.0.0.1:1080:1080), "+
				"with operators reaching it over an SSH forward. Check the mapping if "+
				"you have not.")
	}

	var ln net.Listener
	ln, err = net.Listen("tcp", listen)
	if err != nil {
		slog.Error("socks: listener not started", "listen", listen, "err", err.Error())
		return
	}
	slog.Info("socks: listening", "listen", listen)

	var srv = &socks.Server{
		Resolver: socksResolver{s: s},
		Auth:     socksAuth{s: s},
		Dialer:   s.Tunnels,
		OnSession: func(label, device, dst string, port int, sent, received int64,
			d time.Duration) {
			slog.Info("socks.session", "user", label, "device", device,
				"dst", socks.HostPort(dst, port), "bytes_to_device", received,
				"bytes_from_device", sent, "duration", d.Round(time.Second).String())
		},
		// A REFUSAL IS THE EVENT WORTH KEEPING. A successful route is ordinary
		// work; a refused one is either a misconfigured browser or somebody
		// reaching for a device they have no business reaching, and both need to
		// be findable afterwards.
		OnRefusal: func(label, dst string, port int, reason string) {
			slog.Warn("socks.refused", "user", label,
				"dst", socks.HostPort(dst, port), "reason", reason)
		},
	}
	go func() {
		var serveErr = srv.Serve(ctx, ln)
		if serveErr != nil {
			slog.Error("socks: listener stopped", "err", serveErr.Error())
		}
	}()
}

func isLoopbackHost(host string) bool {
	if host == "" {
		return false // an empty host in ":1080" means every interface
	}
	if host == "localhost" {
		return true
	}
	var ip = net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// --- Token management, for the operator's own account ---

// handleGetSocksToken reports whether the caller HAS a token, never what it is.
func (s *Server) handleGetSocksToken(w http.ResponseWriter, r *http.Request) {
	var me = auth.UserFrom(r.Context())
	var has, created, used, err = s.Store.SocksTokenState(r.Context(), me.ID)
	if err != nil {
		fail(w, err)
		return
	}
	respond(w, http.StatusOK, map[string]any{
		"username": me.Email, "has_token": has,
		"created_at": created, "last_used_at": used,
		"listen": s.Cfg.SocksListen,
	})
}

// handleCreateSocksToken generates a token and returns it ONCE.
//
// Regenerating replaces the previous one, which is the revocation gesture: there
// is no separate "revoke" because a token nobody can present is revoked, and one
// button that always works beats two that can disagree.
//
// For the CALLER'S OWN account only. A token is a credential in a person's hands
// and an administrator minting one for someone else would break the single
// property that makes the audit line meaningful — that a SOCKS session names the
// person who opened it.
func (s *Server) handleCreateSocksToken(w http.ResponseWriter, r *http.Request) {
	var me = auth.UserFrom(r.Context())
	var token, hash, err = auth.NewSessionToken()
	if err != nil {
		fail(w, err)
		return
	}
	if err = s.Store.SetSocksToken(r.Context(), me.ID, hash); err != nil {
		fail(w, err)
		return
	}
	s.emitInstance(r, "socks.token_created", model.SevWarning,
		"SOCKS route token generated by "+me.Email)
	respond(w, http.StatusOK, map[string]any{
		"username": me.Email, "token": token, "listen": s.Cfg.SocksListen,
	})
}

// handleDeleteSocksToken closes the caller's route.
func (s *Server) handleDeleteSocksToken(w http.ResponseWriter, r *http.Request) {
	var me = auth.UserFrom(r.Context())
	var err = s.Store.DeleteSocksToken(r.Context(), me.ID)
	if err != nil {
		fail(w, err)
		return
	}
	s.emitInstance(r, "socks.token_revoked", model.SevNotice,
		"SOCKS route token revoked by "+me.Email)
	respond(w, http.StatusNoContent, nil)
}
