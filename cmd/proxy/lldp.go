package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"meshcraft/internal/snmppoll"
)

// Remontee du voisinage LLDP, cote PROXY.
//
// Voir docs/01-Architecture/Voisinage LLDP.md. Le controleur dit quels devices
// interroger et a quelle cadence (`tunnel.SNMPWatch.LLDPIntervalS`) ; le proxy lit la
// LLDP-MIB et pousse le CLICHE.
//
// # Pourquoi ce chemin ne reutilise ni le tampon de metriques ni le canal d'evenements
//
// Les trois options ont been pesees, et la conclusion tient a la NATURE de ce qui
// voyage :
//
//   - le tampon de metriques (`metricbuf`) transporte des flottants etiquetes. Un
//     voisin est une table de strings ; l'y faire entrer demanderait de coder chaque
//     nom systeme dans une tag de serial, donc de create une serial temporelle par
//     cable — que VictoriaMetrics garderait pour toujours, y compris after le
//     debranchement. Et « depuis quand ce voisin est-il vu » se lirait alors dans la
//     forme d'une courbe plate, ce qui n'est pas une response ;
//   - le canal d'evenements du tunnel ne porte que des TRANSITIONS, jamais un etat
//     courant repete (cf. docs/01-Architecture/Protocole controleur-proxy). Y pousser
//     une table entire toutes les trente secondes violerait son invariant, et sa file
//     de 256 entries est dimensionnee pour des bascules rares, pas pour un cliche
//     periodique. Fabriquer des transitions `neighbor.new` / `neighbor.gone` aurait
//     rendu chaque perte d'evenement definitive — le default que `resync` a coste cher
//     a reparer pour la joignabilite ;
//   - `ask` est explicitement « PAS un chemin de collecte » (same page). Il est
//     ponctuel, a l'initiative du controleur, et sans cadence.
//
// Reste la route HTTP, exactement pour le motif qui a already sorti les metriques du
// tunnel : c'est du volume periodique, il s'authentifie par le couple
// (proxy_id, secret), et le control de portee du controleur s'y applique already
// (`internal/api/proxyscope.go`).
//
// # Et pourquoi il n'y a PAS de tampon
//
// C'est la difference la plus utile entre ce chemin et celui des metriques, et elle
// va dans le sens de la simplicite. Un point de mesure perdu l'est pour toujours : il
// echantillonne un instant qui ne reviendra pas, hence `metricbuf`, ses 150 000
// entries et ses 57 Mio de pire cas. Un cliche de voisinage perdu est REEMIS trente
// secondes plus tard, plus frais que celui qu'on aurait rejoue — la table LLDP d'un
// device change a the scale du cable debranche, pas de la seconde. Bufferiser
// aurait donc coste de la memory pour repousser une information STALE, et il aurait
// fallu en plus decider quoi faire de deux cliches du same device dans la file.
//
// Le prix est named : une coupure de tunnel de dix minutes laisse dix minutes de
// `last_seen` en retard cote controleur. C'est visible (la date est affichee) et
// auto-reparateur au premier cycle qui repasse.

// lldpPushTimeout borne la poussee. Plus court que le client de metriques (20 s)
// parce qu'il n'y a rien a sauver derriere : un cliche qui n'est pas parti ne sera
// pas rejoue, il sera refait. Insister n'acheterait qu'une goroutine bloquee.
const lldpPushTimeout = 10 * time.Second

// lldpErrEvery amortit le journal des echecs de poussee, PAR DEVICE.
//
// Meme compromis que `dropReportEvery` et `scopeRefusalEvery` : la first ligne se
// dit tout de suite, les suivantes se regroupent. Sans cet amortissement, un
// controleur injoignable ferait deux lignes par minute et par device — sur un parc
// de cent, c'est le journal noye par le mecanisme cense l'alerter.
const lldpErrEvery = 5 * time.Minute

// lldpJob rend le travail d'une interrogation de voisinage.
//
// Un `job` separated de celui des metriques, et non une cinquieme branche de `job()` :
// celui-there rend des `metricbuf.Sample` et les met en tampon, ce qui n'a aucun sens
// ici. Les deux partagent ce qui doit l'be — la cible, les conventions de connexion
// de `snmppoll.Dial` — et rien de plus.
func (c *SNMPCollector) lldpJob(ctx context.Context, t snmppoll.Target) func() {
	return func() {
		var p = &snmppoll.Poller{Target: t}
		var report, err = p.PollLLDP(ctx)
		if err != nil {
			// Un device muet est un fait d'exploitation ordinaire, comme pour le poll
			// de metriques : DEBUG, pas WARN. Et surtout on ne pousse RIEN — pousser un
			// cliche vide effacerait au controleur les voisins d'un device dont le seul
			// tort est d'avoir perdu un datagramme UDP. Un cliche ne s'written que quand on a
			// vraiment lu la MIB.
			slog.Debug("voisinage LLDP sans response", "device", t.DeviceID, "ip", t.IP,
				"err", err.Error())
			return
		}
		c.pushLLDP(ctx, report)
	}
}

// pushLLDP pousse un cliche au controleur.
//
// Ne rend rien : il n'y a aucune decision a prendre en cas failure, contrairement a
// `flush` dont le retour arbitre le rejeu et la rafale. Voir l'en-tete de ce fichier
// pour la raison — un cliche ne se rejoue pas, il se refait.
func (c *SNMPCollector) pushLLDP(ctx context.Context, report snmppoll.LLDPReport) {
	var body, err = json.Marshal(map[string]any{
		"proxy_id": c.proxyID, "secret": c.secret, "report": report,
	})
	if err != nil {
		c.sayLLDPError(report.DeviceID, "cliche illisible : "+err.Error())
		return
	}
	// Un delai PROPRE et non celui du cycle de collecte : une poussee lente ne doit
	// pas retenir l'interrogation des autres devices, et `pollDue` attend la fin de
	// tous ses travaux.
	var pushCtx, cancel = context.WithTimeout(ctx, lldpPushTimeout)
	defer cancel()
	var req *http.Request
	req, err = http.NewRequestWithContext(pushCtx, http.MethodPost,
		c.controllerURL+"/api/proxy/lldp", bytes.NewReader(body))
	if err != nil {
		c.sayLLDPError(report.DeviceID, err.Error())
		return
	}
	req.Header.Set("Content-Type", "application/json")
	var resp *http.Response
	if resp, err = c.client.Do(req); err != nil {
		c.sayLLDPError(report.DeviceID, err.Error())
		return
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Le motif du controleur est LU, comme sur le chemin d'abandon des metriques :
		// c'est le seul endroit hence l'on peut apprendre qu'un refus de portee nous
		// vise, et « status 403 » sans la phrase enverrait chercher un default de network.
		c.sayLLDPError(report.DeviceID,
			"refus du controleur (status "+resp.Status+") : "+refusalReason(resp.Body))
		return
	}
}

// sayLLDPError journalise un failure de poussee, au plus une fois par device et par
// lldpErrEvery.
//
// En `Warn` : ce qui se perd n'est pas une mesure mais la FRAICHEUR d'un constat, et
// un exploitant qui regarde un lien LLDP date d'une heure doit pouvoir apprendre
// pourquoi. Silencieux, ce chemin ferait passer un controleur mal configure pour un
// parc sans voisins.
func (c *SNMPCollector) sayLLDPError(deviceID, detail string) {
	var now = time.Now()
	c.mu.Lock()
	if c.lldpSaid == nil {
		c.lldpSaid = map[string]time.Time{}
	}
	var saidAt = c.lldpSaid[deviceID]
	if !saidAt.IsZero() && now.Sub(saidAt) < lldpErrEvery {
		c.mu.Unlock()
		return
	}
	c.lldpSaid[deviceID] = now
	c.mu.Unlock()
	slog.Warn("voisinage LLDP non pousse : ce cliche est perdu, le prochain cycle le "+
		"refera (aucun rejeu, cf. cmd/proxy/lldp.go)",
		"device", deviceID, "detail", detail, "prochain_essai_dans", lldpErrEvery.String())
}
