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

// Le tunnel : une connexion WebSocket SORTANTE vers le controleur, maintenue en
// permanence, dans laquelle le controleur ouvre des connexions TCP vers les
// devices que ce proxy sait joindre.
//
// Rien d'entrant chez le client : c'est le principe n° 4 du projet. Le heartbeat
// HTTP reste en place a cote — il porte les ordres de migration et survit same
// si le tunnel ne s'establishes pas (mauvais reverse-proxy, WebSocket bloque).

// tunnelURL convertit l'URL du controleur en URL WebSocket du tunnel.
func tunnelURL(controller string) string {
	var u = strings.TrimSuffix(controller, "/")
	if strings.HasPrefix(u, "https://") {
		u = "wss://" + strings.TrimPrefix(u, "https://")
	} else if strings.HasPrefix(u, "http://") {
		u = "ws://" + strings.TrimPrefix(u, "http://")
	}
	return u + tunnel.Path
}

// runTunnel maintient le lien, avec backoff exponentiel et jitter. La fonction
// ne rend jamais la main : elle vit aussi longtemps que le proxy.
// monitor est partage entre les sessions : la configuration de surveillance
// survit a une reconnexion, et le ping ne stops pas parce que le WAN a
// hoquete. Seule la remontee d'evenements attend le retour du tunnel.
var monitor = NewMonitor()

func runTunnel(st State, version string) {
	var allow, err = tunnel.ParseAllowlist(os.Getenv("MESHCRAFT_DIAL_ALLOW"))
	if err != nil {
		// Une allowlist illisible ne doit pas se transformer en « tout ouvert » :
		// on prefers refuser tout dial et le dire fort.
		slog.Error("MESHCRAFT_DIAL_ALLOW invalide : aucun dial ne sera accepted", "err", err)
		allow, _ = tunnel.ParseAllowlist("127.0.0.255/32")
	}
	slog.Info("tunnel : destinations autorisees", "allowlist", allow.String())

	// La surveillance tourne independamment du tunnel : un proxy coupe du
	// controleur continue de savoir qui est en ligne, et remontera les
	// transitions a la reconnexion.
	go monitor.Run(context.Background())

	var url = tunnelURL(st.Controller)
	var attempt int
	for {
		var started = time.Now()
		err = tunnelOnce(url, st, version, allow)
		var lived = time.Since(started)
		// Une session qui a tenu se reinitialise le backoff : une coupure after
		// deux heures n'est pas le symptome d'un controleur en panne.
		if lived > 2*time.Minute {
			attempt = 0
		}
		if err != nil {
			slog.Warn("tunnel interrompu", "err", err,
				"duree", lived.Round(time.Second).String(), "attempt", attempt+1)
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

	// Pas de limite de taille : c'est yamux qui decoupe, et un stream peut
	// transporter un transfert de configuration entier.
	ws.SetReadLimit(-1)
	var nc = websocket.NetConn(ctx, ws, websocket.MessageBinary)

	var cfg = yamux.DefaultConfig()
	cfg.KeepAliveInterval = tunnel.KeepAlive
	cfg.ConnectionWriteTimeout = tunnel.WriteTimeout
	cfg.LogOutput = io.Discard

	// Le PROXY est le serveur yamux : il accepte les streams, il n'en ouvre pas.
	var session *yamux.Session
	session, err = yamux.Server(nc, cfg)
	if err != nil {
		return err
	}
	defer session.Close() //nolint:errcheck

	slog.Info("tunnel established", "controller", st.Controller)

	// Remontee des evenements sur un stream ouvert PAR le proxy.
	//
	// `monitor.Snapshot` est passe pour que la reconnexion REANNONCE state courant
	// after avoir vide le reliquat de la file : le moniteur est un singleton de
	// processus qui survit a la coupure, donc il sait ce qu'il croit de chaque cible,
	// et le controleur peut l'avoir manque. Sans ce cliche, une transition perdue
	// pendant une coupure ne se rejouait jamais (cf. tunnel.Event.Resync).
	// Reconnect re-announces BOTH ICMP and SNMP reachability. Two snapshots,
	// one stream: a lost SNMP transition is the same class of hole as a lost
	// ping transition (cf. tunnel.Event.Resync).
	go func() {
		var snap = func() []tunnel.Event {
			var out = monitor.Snapshot()
			if snmpCollector != nil {
				out = append(out, snmpCollector.Snapshot()...)
			}
			return out
		}
		var sendErr = tunnel.SendEvents(session, monitor.Events(), snap)
		if sendErr != nil {
			slog.Warn("remontee d'evenements interrompue", "err", sendErr)
		}
	}()
	// handleAsk repond aux questions ponctuelles (cf. rpc.go). Aucun etat a
	// initialiser et rien a stop : une question, une response, le stream se
	// ferme — donc rien qui doive survivre a une reconnexion, contrairement a
	// `monitor`.
	return tunnel.ServeStreams(session, allow, monitor.SetConfig, handleAsk)
}

// tunnelError distingue un refus du controleur (401 : proxy revoque, secret
// stale) d'une panne de transport — le premier ne se resout pas en reessayant
// plus vite.
type tunnelError struct {
	status int
	err    error
}

func (e *tunnelError) Error() string {
	if e.status == http.StatusUnauthorized {
		return "refused par le controleur (proxy revoque ou secret invalide)"
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

// tunnelBackoff : 1 s → 60 s, avec jitter. Sans jitter, tous les proxys d'un
// parc reviennent frapper a la same seconde after une coupure du controleur.
func tunnelBackoff(attempt int) time.Duration {
	var base = time.Duration(1<<min(attempt, 6)) * time.Second
	if base > 60*time.Second {
		base = 60 * time.Second
	}
	var jitter = time.Duration(rand.Int63n(int64(base / 2))) //nolint:gosec
	return base/2 + jitter
}
