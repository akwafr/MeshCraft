// Le proxy meshcraft — squelette de la phase 0.
//
// Il s'enrole aupres du controleur avec MESHCRAFT_ENROLL_TOKEN, persiste son
// identity (proxy_id + secret) et maintient un heartbeat HTTP. Le controleur
// peut lui ordonner de MIGRER vers une autre instance : le proxy s'enrole alors
// sur la nouvelle URL, rewrites son etat local et bascule definitivement dessus
// — sans aucune intervention sur la machine du proxy.
//
// Le tunnel WSS multiplexe (yamux) remplacera ce heartbeat en phase 0bis.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Injectes au build par -ldflags (cf. Dockerfile) :
//
//	version = horodatage de build UTC AAAAMMJJ.HHMMSS — c'est LA version
//	          technique, comparable en texte, celle que le controleur compare ;
//	product = version produit (fichier VERSION), informative.
//
// LES DEFAUTS NE SONT PLUS « dev », et le mot avait un cost. Un proxy qui annonce
// « dev » est lexicographiquement PLUS RECENT que tout horodatage — les chiffres se
// trient avant les lettres — donc le controleur ne lui propose plus rien et l'affiche
// « a jour », definitivement. Constate sur un proxy reel, et impossible a reparer
// autrement qu'en allant sur sa machine, ce que cette architecture s'interdit.
//
// Deux corrections tiennent ce default closed, et il faut les deux : la string de build
// REFUSE now de produire un binaire sans horodatage (garde-fou du Dockerfile), et
// [distrib.IsNewer] traite une version qui n'est pas un horodatage comme « a remplacer »
// au lieu de la comparer. Ce default-ci ne subsiste donc que pour un `go run` : il est
// named pour ce qu'il est, il ne ressemble pas a un canal de livraison, et il se fera
// remplacer par le build empaquete au premier heartbeat.
var (
	version = "hors-build"
	product = "hors-build"
)

// protocolVersion : compatibilite du protocole controleur ↔ proxy. Un horodatage
// de build ne peut pas dire ce qu'on refuse — c'est le role de cet entier
// (cf. docs/01-Architecture/Protocole controleur-proxy).
const protocolVersion = 1

const stateDir = "/var/lib/meshcraft"

// State est state local du proxy : ce fichier suffit a le rattacher.
//
// Aucun site : un proxy est un point de presence network de l'instance, il
// n'appartient a aucun site (cf. docs/01-Architecture/Proxy).
type State struct {
	Controller string `json:"controller"`
	ProxyID    string `json:"proxy_id"`
	Secret     string `json:"secret"`
	// Binary : le binaire mis a jour que ce proxy doit executer. C'est cet
	// ENREGISTREMENT — pas la presence d'un fichier — qui autorise l'execution :
	// un binaire deapplied dans le volume par un tiers n'a ni ingress ni SHA expected.
	Binary *BinaryRecord `json:"binary,omitempty"`
}

// BinaryRecord describes le binaire enregistre et state de sa mise en service.
type BinaryRecord struct {
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
	// BootAttempts compte les demarrages non confirmes : au-dela de deux, on
	// retombe sur le binaire de l'image (anti-crash-loop).
	BootAttempts int  `json:"boot_attempts"`
	Confirmed    bool `json:"confirmed"`
}

func statePath() string { return filepath.Join(stateDir, "state.json") }

func loadState() (State, error) {
	var st State
	var data, err = os.ReadFile(statePath())
	if err != nil {
		return st, err
	}
	err = json.Unmarshal(data, &st)
	return st, err
}

func saveState(st State) error {
	var err = os.MkdirAll(stateDir, 0o750)
	if err != nil {
		return err
	}
	var data, marshalErr = json.MarshalIndent(st, "", "  ")
	if marshalErr != nil {
		return marshalErr
	}
	return os.WriteFile(statePath(), data, 0o600)
}

// normalizeURL accepte ws:// et wss:// par confort : le heartbeat parle HTTP(S).
func normalizeURL(u string) string {
	u = strings.TrimSuffix(strings.TrimSpace(u), "/")
	u = strings.Replace(u, "wss://", "https://", 1)
	u = strings.Replace(u, "ws://", "http://", 1)
	return u
}

var client = &http.Client{Timeout: 20 * time.Second}

func postJSON(url string, in any, out any) (int, error) {
	var body, err = json.Marshal(in)
	if err != nil {
		return 0, err
	}
	var resp, reqErr = client.Post(url, "application/json", bytes.NewReader(body))
	if reqErr != nil {
		return 0, reqErr
	}
	defer resp.Body.Close()
	if out != nil {
		_ = json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode, nil
}

type enrollReply struct {
	Status        string `json:"status"`
	ProxyID       string `json:"proxy_id"`
	Secret        string `json:"secret"`
	ControllerURL string `json:"controller_url"`
	Error         string `json:"error"`
}

// enroll echange un token d'enrollment contre une identity durable.
func enroll(controller, token, name string) (State, error) {
	var out enrollReply
	var code, err = postJSON(controller+"/api/proxy/enroll",
		map[string]string{"token": token, "name": name}, &out)
	if err != nil {
		return State{}, err
	}
	if code != http.StatusOK || out.ProxyID == "" {
		return State{}, fmt.Errorf("enrollment refused (HTTP %d): %s", code, out.Error)
	}
	// Le controleur peut annoncer son URL publique canonique.
	var base = controller
	if out.ControllerURL != "" {
		base = normalizeURL(out.ControllerURL)
	}
	return State{Controller: base, ProxyID: out.ProxyID, Secret: out.Secret}, nil
}

type heartbeatReply struct {
	Status  string `json:"status"`
	Migrate struct {
		ControllerURL string `json:"controller_url"`
		EnrollToken   string `json:"enroll_token"`
	} `json:"migrate"`
	Upgrade *upgradeOffer `json:"upgrade"`
	Error   string        `json:"error"`
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	var envController = normalizeURL(os.Getenv("MESHCRAFT_CONTROLLER"))
	var token = os.Getenv("MESHCRAFT_ENROLL_TOKEN")
	var name = os.Getenv("MESHCRAFT_PROXY_NAME")

	var st, loadErr = loadState()
	if loadErr != nil || st.ProxyID == "" {
		// Premier demarrage : enrollment obligatoire.
		if envController == "" || token == "" {
			slog.Error("premier demarrage : MESHCRAFT_CONTROLLER et MESHCRAFT_ENROLL_TOKEN requis")
			os.Exit(1)
		}
		var attempt int
		for attempt = 0; ; attempt++ {
			var newState, err = enroll(envController, token, name)
			if err == nil {
				st = newState
				var saveErr = saveState(st)
				if saveErr != nil {
					slog.Error("etat local non persiste", "err", saveErr)
				}
				slog.Info("enrole", "controller", st.Controller, "proxy_id", st.ProxyID)
				break
			}
			slog.Error("enrollment impossible", "err", err, "attempt", attempt+1)
			time.Sleep(backoff(attempt))
		}
	} else {
		slog.Info("etat local charge", "controller", st.Controller, "proxy_id", st.ProxyID)
		// MESHCRAFT_CONTROLLER ne rewrites jamais un etat existant : la bascule
		// d'instance passe par un ordre de migration du controleur.
		if envController != "" && envController != st.Controller {
			slog.Warn("MESHCRAFT_CONTROLLER differe de state local — state local prime",
				"env", envController, "state", st.Controller)
		}
	}

	// AVANT toute autre chose : rebasculer sur le binaire mis a jour si state
	// local en enregistre un (le reboot d'un conteneur relance toujours
	// celui de l'image). En cas de success, ce processus est remplace ici.
	st = bootstrapBinary(st)

	slog.Info("meshcraft proxy demarre", "version", version, "product", product,
		"protocol", protocolVersion, "controller", st.Controller)

	// Le tunnel vit dans sa propre boucle : le heartbeat continue same si le
	// WebSocket ne s'establishes pas (reverse-proxy recalcitrant, WS bloque), et
	// c'est lui qui portera toujours les ordres de migration.
	// Collecte SNMP : le controleur enverra les cibles par le canal de
	// surveillance, le proxy interroge et pousse (cf. cmd/proxy/snmp.go).
	snmpCollector = NewSNMPCollector(st.Controller, st.ProxyID, st.Secret)
	// Les transitions de port sortent par le SAME canal que les transitions
	// d'device : c'est already celui que le tunnel remonte, et il ne bloque
	// jamais. Deux canaux pour deux evenements qui vont au same endroit
	// n'apporteraient qu'un ordre a arbitrer (cf. cmd/proxy/trap.go).
	snmpCollector.SetEmitter(monitor.emit)
	go snmpCollector.Run(context.Background())

	go runTunnel(st, version)

	var fails int
	for {
		var out heartbeatReply
		var body = map[string]any{
			"proxy_id": st.ProxyID, "secret": st.Secret, "version": version,
			"protocol": protocolVersion, "os": runtime.GOOS, "arch": goarch(),
			"ifaces": localIfaces(),
		}
		if pendingReport != nil {
			body["upgrade"] = pendingReport
		}
		var code, err = postJSON(st.Controller+"/api/proxy/heartbeat", body, &out)
		switch {
		case err != nil:
			fails++
			slog.Warn("controleur injoignable", "err", err, "fails", fails)
		case code == http.StatusUnauthorized:
			// Revoque ou migre : on n'insiste pas en boucle serree.
			fails++
			slog.Error("proxy refused par le controleur (revoque ?)", "controller", st.Controller)
		case code != http.StatusOK:
			fails++
			slog.Warn("heartbeat en erreur", "status", code, "error", out.Error)
		default:
			fails = 0
			// Un heartbeat accepted vaut confirmation : la version tient debout.
			pendingReport = nil
			confirmBinary(&st)
			if out.Upgrade != nil {
				var upErr = applyUpgrade(st, *out.Upgrade)
				if upErr != nil {
					slog.Error("mise a jour impossible — on reste en place",
						"version", out.Upgrade.Version, "err", upErr)
					pendingReport = &upgradeReport{
						State: "failed", Version: out.Upgrade.Version,
						Error: firstLine(upErr.Error()),
					}
				}
			}
			if out.Status == "migrate" && out.Migrate.ControllerURL != "" {
				var migrated, mErr = migrate(st, out.Migrate.ControllerURL, out.Migrate.EnrollToken, name)
				if mErr != nil {
					slog.Error("migration echouee — on reste sur l'instance actuelle",
						"cible", out.Migrate.ControllerURL, "err", mErr)
				} else {
					st = migrated
					slog.Info("migration terminee", "controller", st.Controller,
						"proxy_id", st.ProxyID)
				}
			}
		}
		time.Sleep(heartbeatDelay(fails))
	}
}

// migrate enrole le proxy sur la nouvelle instance, persiste le nouvel etat,
// puis warns l'ancienne (au mieux) qu'elle peut nous oublier.
func migrate(old State, targetURL, token, name string) (State, error) {
	targetURL = normalizeURL(targetURL)
	if token == "" {
		return old, errors.New("ordre de migration sans token d'enrollment")
	}
	slog.Info("ordre de migration recu", "cible", targetURL)
	var st, err = enroll(targetURL, token, name)
	if err != nil {
		return old, err
	}
	// State local bascule AVANT de warn l'ancienne instance : si le
	// processus meurt ici, on redemarre already rattache a la nouvelle.
	if err = saveState(st); err != nil {
		return old, fmt.Errorf("nouvel etat non persiste: %w", err)
	}
	var code, ackErr = postJSON(old.Controller+"/api/proxy/migrated", map[string]string{
		"proxy_id": old.ProxyID, "secret": old.Secret, "new_controller": st.Controller,
	}, nil)
	if ackErr != nil || code != http.StatusOK {
		slog.Warn("ancienne instance non warned (sans consequence)",
			"status", code, "err", ackErr)
	}
	return st, nil
}

// localIfaces annonce les IP locales : le controleur peut ainsi proposer les
// subnets a scanner.
func localIfaces() []map[string]string {
	var out = []map[string]string{}
	var addrs, err = netInterfaceAddrs()
	if err != nil {
		return out
	}
	var a string
	for _, a = range addrs {
		out = append(out, map[string]string{"ip": a})
	}
	return out
}

func backoff(attempt int) time.Duration {
	var d = time.Duration(1<<min(attempt, 6)) * time.Second
	if d > 60*time.Second {
		d = 60 * time.Second
	}
	return d
}

func heartbeatDelay(fails int) time.Duration {
	if fails == 0 {
		return 30 * time.Second
	}
	return backoff(fails)
}

// goarch traduit l'architecture Go en suffixe d'artefact published par le
// controleur. GOARM n'apparait pas dans runtime.GOARCH : « arm » suffit.
func goarch() string { return runtime.GOARCH }

func firstLine(s string) string {
	var i = strings.IndexAny(s, "\r\n")
	if i >= 0 {
		return s[:i]
	}
	return s
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
