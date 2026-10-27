package main

import (
	"context"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/hashicorp/yamux"

	"meshcraft/internal/tunnel"
)

// The tunnel: an OUTBOUND WebSocket connection to the controller, kept up
// permanently, inside which the controller opens TCP connections to the
// devices this proxy can reach.
//
// Nothing inbound at the customer's: that is the project's principle no. 4. The
// HTTP heartbeat stays in place alongside — it carries migration orders and
// survives even if the tunnel cannot be established (bad reverse proxy,
// WebSocket blocked).

// tunnelURL converts the controller URL into the tunnel's WebSocket URL.
func tunnelURL(controller string) string {
	var u = strings.TrimSuffix(controller, "/")
	if strings.HasPrefix(u, "https://") {
		u = "wss://" + strings.TrimPrefix(u, "https://")
	} else if strings.HasPrefix(u, "http://") {
		u = "ws://" + strings.TrimPrefix(u, "http://")
	}
	return u + tunnel.Path
}

// runTunnel keeps the link up, with exponential backoff and jitter. The
// function never returns: it lives as long as the proxy.
// monitor is shared between sessions: the monitoring configuration survives a
// reconnection, and pinging does not stop because the WAN hiccupped. Only the
// upstream of events waits for the tunnel to come back.
var monitor = NewMonitor()

func runTunnel(st State, version string) {
	var allow, err = tunnel.ParseAllowlist(os.Getenv("MESHCRAFT_DIAL_ALLOW"))
	if err != nil {
		// An unreadable allowlist must not turn into "everything open": we prefer to
		// refuse every dial and say so loudly.
		slog.Error("MESHCRAFT_DIAL_ALLOW invalid: no dial will be accepted", "err", err)
		allow, _ = tunnel.ParseAllowlist("127.0.0.255/32")
	}
	slog.Info("tunnel: allowed destinations", "allowlist", allow.String())

	// Monitoring runs independently of the tunnel: a proxy cut off from the
	// controller keeps knowing who is online, and will report the transitions on
	// reconnection.
	go monitor.Run(context.Background())

	var url = tunnelURL(st.Controller)
	var attempt int
	for {
		var started = time.Now()
		err = tunnelOnce(url, st, version, allow)
		var lived = time.Since(started)
		// A session that held resets the backoff: an outage after two hours is not
		// the symptom of a failed controller.
		if lived > 2*time.Minute {
			attempt = 0
		}
		if err != nil {
			slog.Warn("tunnel interrupted", "err", err,
				"duration", lived.Round(time.Second).String(), "attempt", attempt+1)
		}
		time.Sleep(tunnelBackoff(attempt))
		attempt++
	}
}

func tunnelOnce(url string, st State, version string, allow *tunnel.Allowlist) error {
	var ctx, cancel = context.WithCancel(context.Background())
	defer cancel()

	var header = http.Header{}
	header.Set(tunnel.HeaderProxyID, st.ProxyID)
	header.Set(tunnel.HeaderSecret, st.Secret)
	header.Set(tunnel.HeaderVersion, version)
	// What this binary can do, declared by the code that does it — and not
	// deduced from `version`, which is only a text stamped at build time. An older
	// controller ignores this header, a recent one uses it to know whether it can
	// wait for the end of the resynchronisation burst (see tunnel.HeaderFeatures).
	header.Set(tunnel.HeaderFeatures, tunnel.FeatureResyncDone)

	var ws, resp, err = websocket.Dial(ctx, url, &websocket.DialOptions{
		HTTPHeader:      header,
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		if resp != nil {
			return &tunnelError{status: resp.StatusCode, err: err}
		}
		return err
	}
	defer ws.CloseNow() //nolint:errcheck

	// No size limit: yamux does the chunking, and a stream may carry an entire
	// configuration transfer.
	ws.SetReadLimit(-1)
	var nc = websocket.NetConn(ctx, ws, websocket.MessageBinary)

	var cfg = yamux.DefaultConfig()
	cfg.KeepAliveInterval = tunnel.KeepAlive
	cfg.ConnectionWriteTimeout = tunnel.WriteTimeout
	cfg.LogOutput = io.Discard

	// The PROXY is the yamux server: it accepts streams, it does not open them.
	var session *yamux.Session
	session, err = yamux.Server(nc, cfg)
	if err != nil {
		return err
	}
	defer session.Close() //nolint:errcheck

	slog.Info("tunnel established", "controller", st.Controller)

	// Events go upstream on a stream opened BY the proxy.
	//
	// `monitor.Snapshot` is passed so that the reconnection RE-ANNOUNCES the current
	// state after draining what is left in the queue: the monitor is a process
	// singleton that survives the outage, so it knows what it believes about each
	// target, and the controller may have missed it. Without this snapshot, a
	// transition lost during an outage was never replayed (see tunnel.Event.Resync).
	// Reconnect re-announces BOTH ICMP and SNMP reachability. Two snapshots,
	// one stream: a lost SNMP transition is the same class of hole as a lost
	// ping transition (see tunnel.Event.Resync).
	go func() {
		var snap = func() []tunnel.Event {
			var out = monitor.Snapshot()
			if snmpCollector != nil {
				out = append(out, snmpCollector.Snapshot()...)
			}
			// And the streaming half. A device carried on gNMI is absent from
			// the SNMP collector entirely — the two lists are exclusive — so
			// without this its collection state would never be re-announced, and
			// a transition lost during a tunnel outage would stay lost. That is
			// the same hole Resync exists to close, arriving through the door
			// this lot opened.
			if gnmiCollector != nil {
				out = append(out, gnmiCollector.Snapshot()...)
			}
			return out
		}
		var sendErr = tunnel.SendEvents(session, monitor.Events(), snap)
		if sendErr != nil {
			slog.Warn("event upstream interrupted", "err", sendErr)
		}
	}()
	// handleAsk answers one-off questions (see rpc.go). No state to initialise
	// and nothing to stop: one question, one answer, the stream closes — so
	// nothing that must survive a reconnection, unlike `monitor`.
	return tunnel.ServeStreams(session, allow, monitor.SetConfig, handleAsk)
}

// tunnelError tells a refusal from the controller (401: proxy revoked, stale
// secret) from a transport failure — the former is not solved by retrying
// faster.
type tunnelError struct {
	status int
	err    error
}

func (e *tunnelError) Error() string {
	if e.status == http.StatusUnauthorized {
		return "refused by the controller (proxy revoked or invalid secret)"
	}
	return "HTTP " + itoa(e.status) + ": " + e.err.Error()
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

// tunnelBackoff: 1 s → 60 s, with jitter. Without jitter, every proxy of a fleet
// comes back knocking in the same second after a controller outage.
func tunnelBackoff(attempt int) time.Duration {
	var base = time.Duration(1<<min(attempt, 6)) * time.Second
	if base > 60*time.Second {
		base = 60 * time.Second
	}
	var jitter = time.Duration(rand.Int63n(int64(base / 2))) //nolint:gosec
	return base/2 + jitter
}
