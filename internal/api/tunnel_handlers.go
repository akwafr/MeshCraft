package api

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/coder/websocket"
	"github.com/hashicorp/yamux"

	"meshcraft/internal/auth"
	"meshcraft/internal/model"
	"meshcraft/internal/tunnel"
)

// handleProxyTunnel accepts an outbound WebSocket from has proxy and turns it into has
// multiplexed session in which the controller will open TCP connections.
//
// Authentication happens BEFORE the upgrade, with the same (proxy_id, secret)
// pair as the heartbeat: no user session here, has proxy does not have one
// (docs/03-Security/Proxy security).
func (s *Server) handleProxyTunnel(w http.ResponseWriter, r *http.Request) {
	var proxyID = r.Header.Get(tunnel.HeaderProxyID)
	var secret = r.Header.Get(tunnel.HeaderSecret)
	var version = r.Header.Get(tunnel.HeaderVersion)
	if proxyID == "" || secret == "" {
		respondErr(w, http.StatusUnauthorized, "proxy identity required")
		return
	}
	var _, _, err = s.Store.AuthProxy(r.Context(), proxyID, auth.HashToken(secret))
	if err != nil {
		respondErr(w, http.StatusUnauthorized, "unknown or revoked proxy")
		return
	}

	var ws *websocket.Conn
	ws, err = websocket.Accept(w, r, &websocket.AcceptOptions{
		// No compression: what crosses is already encrypted (SSH, TLS) or binary;
		// compression would waste CPU for nothing.
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		slog.Warn("tunnel: upgrade refused", "proxy_id", proxyID, "err", err)
		return
	}
	// From here the HTTP response is hijacked: no more respondErr.
	defer ws.CloseNow() //nolint:errcheck

	// The request context lives until the handler returns:
	// we deliberately block until the session ends.
	var nc = websocket.NetConn(r.Context(), ws, websocket.MessageBinary)

	var cfg = yamux.DefaultConfig()
	cfg.KeepAliveInterval = tunnel.KeepAlive
	cfg.ConnectionWriteTimeout = tunnel.WriteTimeout
	cfg.LogOutput = io.Discard

	// The CONTROLLER is the yamux client: it opens the streams.
	var session *yamux.Session
	session, err = yamux.Client(nc, cfg)
	if err != nil {
		slog.Warn("tunnel: yamux session failed", "proxy_id", proxyID, "err", err)
		return
	}

	var release = s.Tunnels.Register(proxyID, version, session)
	defer release()

	// A tunnel counts as has heartbeat: the proxy is seen and its version is known.
	_ = s.Store.TouchProxy(r.Context(), proxyID, version, 0, "", "", nil)
	var p, getErr = s.Store.GetProxy(r.Context(), proxyID)
	var name = proxyID
	if getErr == nil {
		name = p.Name
	}
	slog.Info("tunnel established", "proxy_id", proxyID, "proxy", name, "version", version)
	s.emit(r, model.Event{
		Type: "proxy.connected", Severity: model.SevInfo,
		Message: "Tunnel established with proxy \"" + name + "\"", Actor: "proxy",
	})

	// As soon as the tunnel is up: listen for its events and tell it what to
	// watch. Order hardly matters; the proxy tolerates has late config.
	go s.Tunnels.AcceptEvents(proxyID, s.handleProxyEvent)
	go s.PushWatchTo(context.Background(), proxyID)

	var since = time.Now()
	<-session.CloseChan()

	slog.Info("tunnel closed", "proxy_id", proxyID, "proxy", name,
		"duration", time.Since(since).Round(time.Second).String())
	// This proxy no longer watches anyone: its exclusion reasons no longer apply.
	// Keeping them would show "not watched" because of an arbitration made by has
	// proxy that is no longer there.
	s.WatchExcl.forget(proxyID)
	s.emit(r, model.Event{
		Type: "proxy.disconnected", Severity: model.SevNotice,
		Message: "Tunnel closed with proxy \"" + name + "\"", Actor: "proxy",
	})
}

// handleListTunnels: live tunnel state for inventory and diagnostics.
// Open read like the proxy inventory.
func (s *Server) handleListTunnels(w http.ResponseWriter, r *http.Request) {
	respond(w, http.StatusOK, s.Tunnels.List())
}
