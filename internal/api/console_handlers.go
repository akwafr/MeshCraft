package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"

	"meshcraft/internal/auth"
	"meshcraft/internal/console"
	"meshcraft/internal/driver"
	"meshcraft/internal/model"
)

// The device CONSOLE: an interactive shell in the browser, opened with the
// credential meshcraft already holds, and closed when nobody is using it.
//
// # Why this exists at all
//
// Every management address in this product sits on a management VLAN that the
// operator's laptop, by design, cannot reach: that separation is the point of the
// proxy. The practical consequence had been that the moment anything needed a
// look the product could not give — `show mac address-table`, a `show interfaces
// trunk` to settle whether a far end is tagging a VLAN — the answer was to drive
// to the site or stand up a jump host. The transport to do better has been in
// place the whole time (cf. internal/tunnel: the controller can already open a
// TCP connection to any device through a proxy), and this file is what finally
// points it at port 22.
//
// # The rule that shapes every choice below: the secret does not leave the
// controller
//
// The SSH client runs HERE. The browser gets terminal bytes and nothing else —
// it never learns the password, never sees it in a WebSocket frame, and cannot
// obtain it by reading the page. That is not an incidental property of the
// design, it IS the design: an operator who can open a console is not thereby
// given a credential they can carry off to another tool, and revoking their
// meshcraft access revokes the device access with it.
//
// The one deliberate exception is the reveal button on platforms with no SSH,
// and it is not new ground: revealing a site credential is already an audited,
// site-admin-gated action that emits `credential.revealed` at warning severity
// (cf. sitecreds_handlers.go). The console reuses that path rather than growing
// a second, quieter one.
//
// # Why site_admin and not operator
//
// An operator can already apply a configuration to a device, so the instinct is
// that a console is no worse. It is worse, and in a way that matters: an apply
// goes through a connector, which refuses to cut the management VLAN, spares
// active uplinks and writes a transcript. A shell has none of that. Whatever is
// typed there is unreviewed, unlogged beyond the fact it happened, and perfectly
// capable of making a switch unreachable in one line. That is site_admin
// territory.

const (
	// consoleIdleTimeout: the console closes itself when nobody types.
	//
	// Fifteen minutes is chosen against the resource being protected, not against
	// comfort: a vty. A Catalyst 2960 has sixteen and hands them out until they
	// run out, at which point NOBODY can log in — including whoever is trying to
	// undo the damage. An operator who steps away is far better served by
	// reconnecting than by finding the switch has no lines left.
	consoleIdleTimeout = 15 * time.Minute
	// consoleMaxLifetime: a hard ceiling regardless of typing. A session that
	// outlives a shift is one nobody is watching.
	consoleMaxLifetime = 4 * time.Hour
	// consoleReadLimit bounds one client frame, matching the relay's own input
	// bound. Set on the socket as well because a limit enforced only after the
	// frame is fully buffered is not a limit on memory.
	consoleReadLimit = 128 << 10
)

// handleDeviceConsoleInfo says what kind of console THIS device offers, without
// opening anything.
//
// It exists so the UI can decide what to draw BEFORE the operator clicks. The
// alternative — offer a console button on every device and let it fail — is the
// exact pattern the driver contract argues against at length: a capability that a
// platform structurally does not have must be DECLARED absent, not discovered by
// trying. An Instant On 1930 has no SSH at all, and a button that opens a
// terminal which then explains it cannot exist is worse than no button.
func (s *Server) handleDeviceConsoleInfo(w http.ResponseWriter, r *http.Request) {
	var d, ok = s.deviceForWrite(w, r, "site_admin")
	if !ok {
		return
	}
	var out = map[string]any{"device": d.Name}

	var conn, connErr = s.connectorFor(r.Context(), d)
	if connErr != nil {
		out["ssh"], out["web"] = false, false
		out["reason"] = connErr.Error()
		respond(w, http.StatusOK, out)
		return
	}
	var info = conn.Info()
	out["transports"] = info.Transport
	out["connector"] = info.Name

	var hasSSH = transportHas(info.Transport, "ssh")
	var hasWeb = transportHas(info.Transport, "web")
	out["ssh"] = hasSSH
	out["web"] = hasWeb

	if d.MgmtIP == "" {
		out["ssh"], out["web"] = false, false
		out["reason"] = errNoMgmtIP.Error()
		respond(w, http.StatusOK, out)
		return
	}
	// The proxy is named even when everything is fine: "which machine is this
	// shell coming from" is the first question asked when a console behaves
	// differently from an SSH client on someone's desk.
	var proxyID = s.proxyOf(r.Context(), d)
	out["proxy_id"] = proxyID
	var _, dialErr = s.dialerFor(proxyID)
	if dialErr != nil {
		out["ssh"], out["web"] = false, false
		out["reason"] = dialErr.Error()
		respond(w, http.StatusOK, out)
		return
	}
	// The device's OWN address, not a relayed one. The rewriting proxy that used
	// to serve it under this origin is gone (cf. internal/socks for why): the
	// browser reaches the device directly, through the SOCKS route, and nothing
	// in between edits the page.
	if hasWeb {
		var addr, isAddresser = driver.AsWebUIAddresser(conn)
		if isAddresser {
			out["web_url"] = addr.WebUIBase(d.MgmtIP)
			out["web_auth"] = string(addr.WebUIAuth())
		} else {
			out["web"] = false
			out["reason"] = "this connector does not say where its web interface answers"
		}
	}

	// The account name is shown, the secret is not. Naming it lets the operator
	// recognize which identity the session will carry — and, on a platform with
	// no SSH, know which username to type into the vendor UI.
	var groupName = s.groupNameOfCtx(r.Context(), d)
	var cred, _, credErr = s.credentialForDevice(r.Context(), d, groupName)
	if credErr == nil {
		out["credential_id"] = cred.ID
		out["credential_label"] = credLabel(cred)
		var caps = conn.Capabilities(d.Model)
		out["username"] = caps.Admin.EffectiveUsername(cred.Username)
	} else if !hasSSH && !hasWeb {
		out["reason"] = credErr.Error()
	}
	respond(w, http.StatusOK, out)
}

func transportHas(list []string, want string) bool {
	var v string
	for _, v = range list {
		if strings.EqualFold(strings.TrimSpace(v), want) {
			return true
		}
	}
	return false
}

// handleDeviceConsole upgrades to a WebSocket and relays an SSH shell.
//
// # Authentication of the SOCKET, and the attack this refuses
//
// The session cookie authenticates the upgrade, as it does every other route —
// but a WebSocket is not subject to the same-origin policy the way fetch is, so
// a page on any other origin could open one against this endpoint and ride the
// operator's cookie. `websocket.Accept` refuses a mismatched Origin by default,
// and that default is left ON deliberately: the alternative would hand a
// privileged shell to any tab the operator happens to have open.
func (s *Server) handleDeviceConsole(w http.ResponseWriter, r *http.Request) {
	var d, ok = s.deviceForWrite(w, r, "site_admin")
	if !ok {
		return
	}
	var opts, creds, info, err = s.consoleAccess(r.Context(), d)
	if err != nil {
		// Refused BEFORE the upgrade: an ordinary HTTP status is readable by the
		// fetch that the UI already made, whereas a WebSocket that accepts and
		// then closes with a reason string is a much worse way to say "this
		// device has no SSH".
		if errors.Is(err, errNoTunnel) {
			respondErr(w, http.StatusServiceUnavailable, err.Error())
			return
		}
		respondErr(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	var wsConn *websocket.Conn
	wsConn, err = websocket.Accept(w, r, &websocket.AcceptOptions{
		// Compression off, same as the proxy tunnel: terminal traffic is small
		// and latency-sensitive, and per-message deflate buys nothing on
		// keystrokes while adding a buffer on both ends.
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		return // Accept already answered
	}
	wsConn.SetReadLimit(consoleReadLimit)

	// FROM HERE ON, FAILURES ARE REPORTED THROUGH THE SOCKET. The response is
	// already committed to a 101, so respondErr would write into a hijacked
	// connection.
	var ctx = r.Context()
	var sess *console.Session
	sess, err = console.Open(ctx, opts, creds)
	if err != nil {
		slog.Warn("console.open_failed", "device_id", d.ID, "device", d.Name,
			"host", opts.Host, "err", err.Error())
		s.emitDeviceEvent(r, d, "device.console_failed", model.SevWarning,
			"Console refused on \""+d.Name+"\": "+firstLine(err.Error()))
		_ = wsConn.Close(websocket.StatusPolicyViolation, wsCloseReason(err.Error()))
		return
	}

	var me = auth.UserFrom(r.Context())
	// AUDITED AT WARNING SEVERITY, and on the site log rather than only in slog.
	// An interactive shell on a production switch is exactly the kind of event
	// that has to be findable afterwards, next to the configuration changes it
	// may explain.
	s.emitDeviceEvent(r, d, "device.console_opened", model.SevWarning,
		"Console opened on \""+d.Name+"\" by "+me.Email+
			" (account "+sess.Accepted.Username+", "+info+")")

	var started = time.Now()
	err = console.Relay(ctx, sess, &wsPeer{conn: wsConn}, console.RelayOptions{
		IdleTimeout: consoleIdleTimeout,
		MaxLifetime: consoleMaxLifetime,
		OnClose: func(reason string, sent, received int64, d2 time.Duration) {
			slog.Info("console.closed", "device_id", d.ID, "device", d.Name,
				"user", me.Email, "reason", reason,
				"bytes_from_device", sent, "bytes_from_operator", received,
				"duration", d2.Round(time.Second).String())
		},
	})
	if err != nil {
		slog.Warn("console.relay_error", "device_id", d.ID, "err", err.Error())
	}
	// A BACKGROUND CONTEXT, and this is not tidiness. The ordinary way a console
	// ends is the operator closing the tab, which cancels the request context —
	// so an event written on `r` would be dropped in exactly the case that
	// happens most, leaving the site log with an opening and no closing. That
	// asymmetry is worse than no closing event: it reads as a session still open.
	s.emitDeviceBG(d, "device.console_closed", model.SevInfo,
		"Console on \""+d.Name+"\" opened by "+me.Email+" closed after "+
			time.Since(started).Round(time.Second).String())
}

// consoleAccess resolves the device into everything [console.Open] needs.
//
// Deliberately mirrors [Server.accessFor] rather than reusing it: that one
// returns a driver.Target built for a CONNECTOR (scheme and port decided by the
// platform, port prefix for name translation), none of which means anything to a
// shell. What the two must share, and do, is the credential ORDER — it comes from
// the same [Server.candidatesFor], because a console that tried secrets in a
// different order from every other path would lock accounts the rest of the
// product is careful not to.
func (s *Server) consoleAccess(ctx context.Context, d model.Device) (
	console.Options, []console.Credential, string, error) {
	var opts console.Options

	var conn, connErr = s.connectorFor(ctx, d)
	if connErr != nil {
		return opts, nil, "", connErr
	}
	var info = conn.Info()
	if !transportHas(info.Transport, "ssh") {
		// NAMES WHAT THE PLATFORM IS, not just what it lacks. "No SSH" sends
		// someone hunting for a setting to enable; saying the range is
		// administered over its web interface ends the search.
		return opts, nil, "", driver.NotSupported(info.Name, "open an SSH console",
			"this platform is administered over "+strings.Join(info.Transport, "/")+
				" and has no SSH server at all — use the web interface button instead")
	}
	if d.MgmtIP == "" {
		return opts, nil, "", errNoMgmtIP
	}
	var dialer, dErr = s.dialerFor(s.proxyOf(ctx, d))
	if dErr != nil {
		return opts, nil, "", dErr
	}

	var groupName = s.groupNameOfCtx(ctx, d)
	var cred, _, credErr = s.credentialForDevice(ctx, d, groupName)
	if credErr != nil {
		return opts, nil, "", credErr
	}
	var wanted = conn.Capabilities(d.Model).Admin.EffectiveUsername(cred.Username)
	var cands, _, _, _, buildErr = s.candidatesFor(ctx, d, wanted, groupName, "")
	if buildErr != nil {
		return opts, nil, "", buildErr
	}

	opts = console.Options{Host: d.MgmtIP, Dialer: dialer}
	var out = make([]console.Credential, 0, len(cands))
	var c driver.Candidate
	for _, c = range cands {
		out = append(out, console.Credential{
			Username: c.Username, Secret: c.Secret, Why: c.Why,
		})
	}
	return opts, out, info.Name, nil
}

// wsCloseReason fits a failure into a WebSocket close frame.
//
// The limit is 123 BYTES, enforced by the protocol, and exceeding it makes the
// close frame itself invalid — so a long refusal would reach the operator as a
// connection that dropped with no explanation at all, which is precisely the
// outcome the message existed to prevent.
func wsCloseReason(msg string) string {
	msg = firstLine(msg)
	if len(msg) > 120 {
		msg = msg[:117] + "..."
	}
	return msg
}

// wsPeer adapts coder/websocket to [console.Peer].
//
// Thin on purpose: everything about the lifecycle lives in the relay, where it
// can be tested without a network, and this type is left with nothing to get
// wrong beyond mapping message types.
type wsPeer struct{ conn *websocket.Conn }

func (p *wsPeer) Read(ctx context.Context) (console.Frame, error) {
	var typ, data, err = p.conn.Read(ctx)
	if err != nil {
		return console.Frame{}, err
	}
	return console.Frame{Text: typ == websocket.MessageText, Data: data}, nil
}

func (p *wsPeer) Write(ctx context.Context, f console.Frame) error {
	var typ = websocket.MessageBinary
	if f.Text {
		typ = websocket.MessageText
	}
	return p.conn.Write(ctx, typ, f.Data)
}

func (p *wsPeer) Ping(ctx context.Context) error { return p.conn.Ping(ctx) }

func (p *wsPeer) Close(reason string) error {
	return p.conn.Close(websocket.StatusNormalClosure, wsCloseReason(reason))
}
