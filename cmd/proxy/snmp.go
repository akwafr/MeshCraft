package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"meshcraft/internal/metricbuf"
	"meshcraft/internal/snmppoll"
	"meshcraft/internal/tunnel"
)

// Collecte SNMP, executee par le PROXY.
//
// Voir docs/01-Architecture/Monitoring unifie et pipelines §2. Le controleur
// dit QUI interroger et a quelle cadence ; le proxy interroge, met en tampon, et
// pousse. Il n'arbitre pas les cadences : elles resultent d'une politique
// d'exploitation bornee par le plancher du connecteur, et ces deux data ne sont
// connues que du controleur.
//
// Ce que le proxy decide, en revanche, c'est le RATE de sa remontee — et c'est
// tout l'objet de la section suivante.

// --- Debit de remontee : le calcul, avant les valeurs ---
//
// La politique de collecte (cf. internal/api/watch_handlers.go) met le trafic et
// les erreurs a 5 s. Le calcul qui en decoule est refait ICI parce que c'est ici
// qu'il se paie :
//
//   - `snmppoll.Samples` rend QUATRE samples par port pour `if_counters`
//     (octets in/out, erreurs in/out) et DEUX pour `if_state` (etat, vitesse) ;
//   - un 1930 48 ports expose 52 interfaces physiques (48 cuivre + 4 SFP) ;
//   - donc 208 samples toutes les 5 s (41,6/s) plus 104 toutes les 30 s
//     (3,5/s) : environ 45 samples par seconde et PAR DEVICE.
//
// L'ancien reglage — 5000 samples toutes les 10 s — evacuait 500/s, soit
// ONZE devices de 48 ports. Au-dela, `metricbuf` jetait ses points les plus
// vieux : des trous dans les courbes, et pas une ligne de journal pour les
// expliquer, puisque `flush` ne parlait que des pertes par failure d'ENVOI. Livrer
// la cadence sans regler ca, c'eut been livrer une cadence qui ne remonte pas.
//
// # Le plafond n'est pas dans ce fichier
//
// Il est chez le controleur : `/api/proxy/metrics` lit son corps avec un
// `http.MaxBytesReader` a 8 MiB (`maxProxyMetricsBody`) et refuse au-dela de
// 20 000 samples par lot (`maxSamplesPerBatch`). Ca n'a pas toujours been le
// cas, et c'est l'histoire qu'il faut garder : cette route passait par le decoder
// generique de l'API, borne a 1 MiB, soit environ 5000 samples. Son garde-fou
// affiche de 20 000 was donc INATTEIGNABLE, et la troncature repondait a sa place
// par un 400 « JSON invalide ». Un lot un peu trop gros ne degradait pas la
// remontee, il l'ARRETAIT : le proxy faisait `Nack`, qui rend le lot EN TETE de
// file, donc le `Take` suivant reprenait exactement le same — et il le rejouait sans
// fin sur un lot qui ne passerait jamais.
//
// Ce default avait deux halves, et il fallait reparer les deux. Le controleur
// accepte now ce qu'il annonce ; et `flush` sait ne plus rejouer un refus
// DEFINITIF (voir refusDefinitif). La seconde half compte au moins autant que la
// first : le 413 n'est plus atteignable pour un proxy a jour, mais la boucle
// restait armee pour n'importe quel AUTRE refus de forme.
//
// Ce qui borne `maxPerFlush` aujourd'hui, c'est donc le garde-fou en NOMBRE et non
// plus la taille en octets : mesure sur des samples actually produits par
// `snmppoll` (52 ports, identifiant d'device en UUID), un compteur pese 196 o
// de JSON avec le nommage d'usine et 229 o avec un label de 37 caracteres saisi
// par l'exploitant — 8 MiB laissent donc passer ~36 000 samples, bien after le
// mur des 20 000. Et c'est toujours la FREQUENCE qui porte le rate.

// flushEvery : period de wake du vidage.
//
// 2 s et non 10 : a taille de lot bornee, la frequence est le seul levier de rate
// qui reste. Le cost est en requests HTTP, pas en octets — le
// volume de data est le same, decoupe plus fin — et il ne se paie que s'il y a
// quelque chose a envoyer : `flush` sort immediatement sur un tampon vide, donc un
// petit site ne fabrique pas de trafic pour rien.
const flushEvery = 2 * time.Second

// maxPerFlush borne un lot. 3000 × 229 o = 672 KiB mesures au poids d'un label long,
// soit 8 % du plafond de corps de 8 MiB du controleur et 15 % de son garde-fou de
// 20 000 samples par lot — c'est ce DERNIER qui est now le mur, et il est
// loin. Les chiffres sont verified a chaque execution des tests sur des samples
// produits par `snmppoll`, et le mur y est CALCULE a partir du poids mesure rather
// que recopie : voir cmd/proxy/snmp_throughput_test.go.
//
// Monter ce nombre reste un arbitrage, mais ce n'est plus un risque de blocage :
// au-dela du mur, le controleur repond 413 et `flush` ABANDONNE le lot en le
// journalisant (cf. refusDefinitif) au lieu de le rejouer pour toujours. Ce qui se
// paie de l'autre cote, c'est la memory transitoire du controleur — il decodes le
// lot entier en RAM avant d'en write le premier point.
const maxPerFlush = 3000

// maxFlushBurst : lots consecutifs autorises par wake. C'est LUI qui porte le
// rate soutenable, et c'est aussi lui qui permet a un tampon en retard — retour
// de coupure de tunnel — de se rattraper au lieu de courir derriere la collecte
// pour toujours.
//
// 4 × 3000 toutes les 2 s = 6000 samples/s, soit 133 devices de
// 48 ports par proxy (contre 11 avant ce lot). Au plafond, cela demande au
// controleur 2 requests par seconde de ~670 KiB, environ 1,3 Mo/s : c'est le prix
// affiche de la cadence a 5 s, et il est lineaire en nombre d'devices.
const maxFlushBurst = 4

// bufCapacity : profondeur du tampon, en samples.
//
// Une fois le rate regle, le tampon ne sert plus a absorber un deficit
// permanent — il n'y en a plus jusqu'a 133 devices — mais a couvrir une
// COUPURE DE TUNNEL, ce pour quoi il a been written. Sa taille se lit donc en duree
// d'indisponibilite : 150 000 samples, c'est 25 s au plafond de rate,
// 2 min 46 s pour un parc de 20 devices et 4 min 37 s pour 12 — there where
// l'ancienne capability de 50 000 ne couvrait qu'une minute et demie pour ce
// dernier, tout en debordant already en regime NORMAL.
//
// La borne haute est la MEMORY du proxy, qui tourne sur une VM minimale ou un
// Raspberry Pi (docs/01-Architecture/Proxy.md) : mesure, un `metricbuf.Sample`
// occupe 401 octets en tas — l'essentiel etant sa carte d'tags, pas sa
// valeur. 150 000 points plafonnent donc a ~57 Mio, et seulement pendant une
// coupure. Doubler la capability doublerait ce chiffre : c'est there qu'est
// l'arbitrage, pas dans le nombre d'samples.
const bufCapacity = 150000

// dropReportEvery amortit l'alerte de saturation. Voir reportSaturation : une
// saturation dure, et une ligne toutes les deux secondes pendant une heure
// noierait le journal au lieu de l'informer.
const dropReportEvery = time.Minute

// snmpCollector est l'instance du proxy. Nil tant que le proxy n'est pas
// enrole : sans identity, il n'a personne a qui pousser.
var snmpCollector *SNMPCollector

// SNMPCollector interroge les cibles et pousse les results.
type SNMPCollector struct {
	mu      sync.Mutex
	watches []tunnel.SNMPWatch
	buf     *metricbuf.Buffer
	// last : last interrogation par (device, metrique), pour tenir la cadence
	// sans un minuteur par cible — cent minuteurs pour cent devices coutent
	// plus que de regarder l'heure.
	last map[string]time.Time
	// portStates : dernier `ifOperStatus` observe, par device puis par index
	// d'interface. C'est de CETTE carte que sortent les transitions de port, a
	// chaque poll `if_state` et quel qu'en soit le declencheur (cf. trap.go).
	//
	// Elle vit a cote du tampon de metriques, pas dedans : le tampon part par lots
	// vers le controleur (cf. flushEvery), l'evenement part TOUT DE SUITE. C'est
	// exactement there que se gagne la reactivity — et c'est ce qui rend tenable de
	// ralentir `if_state` a 30 s, puisque la pastille d'un port ne depend pas du
	// rythme de vidage.
	portStates map[string]map[int]portObs
	// snmpReach: last known SNMP reachability per device. Same shape as ICMP
	// targetState — nil means never observed, and Snapshot stays silent then.
	snmpReach map[string]*snmpReachState
	// emit : depot d'un evenement vers le controleur, branche par le proxy
	// (SetEmitter). Nil = pas encore cable.
	emit func(tunnel.Event)
	// traps : la socket de traps en ecoute, nil sinon. trapTried / trapRetryAt /
	// trapErrSaid amortissent un bind qui fails (voir reconcileTraps).
	traps       *trapReceiver
	trapTried   int
	trapRetryAt time.Time
	trapErrSaid bool
	trapGate    *trapGate
	trapStats   trapStats
	// lldpSaid : dernier instant where failure de poussee du voisinage a been journalise,
	// par device (cf. sayLLDPError). Sous le same verrou que le reste, parce qu'un
	// travail de voisinage tourne dans la same rafale de goroutines que les polls.
	//
	// Borne par le nombre d'devices SURVEILLES, pas par le nombre d'echecs : une
	// ingress par device dont une poussee a echoue au moins une fois. Purge avec la
	// liste de surveillance, comme `portStates`.
	lldpSaid map[string]time.Time

	controllerURL string
	proxyID       string
	secret        string
	client        *http.Client
}

func NewSNMPCollector(controllerURL, proxyID, secret string) *SNMPCollector {
	return &SNMPCollector{
		buf:           metricbuf.New(bufCapacity),
		last:          map[string]time.Time{},
		portStates:    map[string]map[int]portObs{},
		snmpReach:     map[string]*snmpReachState{},
		trapGate:      newTrapGate(),
		controllerURL: controllerURL,
		proxyID:       proxyID,
		secret:        secret,
		client:        &http.Client{Timeout: 20 * time.Second},
	}
}

// SetEmitter branche la sortie d'evenements du collecteur.
//
// Le collecteur ne possede pas ce canal : c'est celui du Monitor, already relie au
// controleur par le tunnel (cf. tunnel.SendEvents) et already non bloquant. Un
// second canal obligerait a fusionner deux flux qui vont au same endroit, et a
// arbitrer leur ordre — du travail pour aucune information de plus.
func (c *SNMPCollector) SetEmitter(fn func(tunnel.Event)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.emit = fn
}

// snmpDownAfter: consecutive failed MIB-II polls before declaring SNMP down.
// Counters fire every 5 s — three misses ≈ 15 s, same order as ICMP's default.
const snmpDownAfter = 3

// snmpReachState mirrors ICMP targetState for SNMP collection.
type snmpReachState struct {
	up     *bool
	misses int
	ip     string
}

// noteSNMPReach records a poll success or failure and emits a transition when
// the observation changes. Called outside c.mu (job goroutines).
func (c *SNMPCollector) noteSNMPReach(deviceID, ip string, ok bool) {
	c.mu.Lock()
	var st = c.snmpReach[deviceID]
	if st == nil {
		st = &snmpReachState{}
		c.snmpReach[deviceID] = st
	}
	st.ip = ip
	var ev *tunnel.Event
	if ok {
		st.misses = 0
		if st.up == nil || !*st.up {
			var on = true
			st.up = &on
			ev = &tunnel.Event{
				Type: tunnel.EventSNMPUp, TS: time.Now().UTC(),
				DeviceID: deviceID, IP: ip,
			}
		}
	} else {
		st.misses++
		if st.misses >= snmpDownAfter && (st.up == nil || *st.up) {
			var off = false
			st.up = &off
			ev = &tunnel.Event{
				Type: tunnel.EventSNMPDown, TS: time.Now().UTC(),
				DeviceID: deviceID, IP: ip, Misses: st.misses,
			}
		}
	}
	c.mu.Unlock()
	if ev != nil {
		c.emitEvent(*ev)
	}
}

// Snapshot returns current SNMP reachability as Resync events, for reconnect.
// Same contract as Monitor.Snapshot: silent when never observed.
func (c *SNMPCollector) Snapshot() []tunnel.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	var ids = make([]string, 0, len(c.snmpReach))
	var id string
	for id = range c.snmpReach {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var now = time.Now().UTC()
	var out = make([]tunnel.Event, 0, len(ids))
	for _, id = range ids {
		var st = c.snmpReach[id]
		if st == nil || st.up == nil {
			continue
		}
		var ev = tunnel.Event{
			Type: tunnel.EventSNMPUp, TS: now, DeviceID: id, IP: st.ip, Resync: true,
		}
		if !*st.up {
			ev.Type = tunnel.EventSNMPDown
			ev.Misses = st.misses
		}
		out = append(out, ev)
	}
	return out
}

// SetWatches remplace la liste des cibles.
func (c *SNMPCollector) SetWatches(w []tunnel.SNMPWatch) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.watches = w

	// Un device retire de la surveillance sort des etats de port, SANS
	// evenement : ce n'est pas une panne, c'est une decision d'exploitation — la
	// same rule que pour les cibles ICMP dans Monitor.SetConfig. Le garder
	// laisserait en plus, a sa reintegration, un etat vieux de plusieurs jours
	// contre lequel diffe produirait une transition imaginaire.
	var watched = make(map[string]bool, len(w))
	var one tunnel.SNMPWatch
	for _, one = range w {
		watched[one.DeviceID] = true
	}
	var id string
	for id = range c.portStates {
		if !watched[id] {
			delete(c.portStates, id)
		}
	}
	// L'amortissement du journal de voisinage suit la same rule, et pour une raison de
	// plus : un device qui revient dans la surveillance revient d'un monde where la
	// configuration du controleur a pu changer. Taire le premier failure de sa poussee
	// ferait manquer precisely celui qui denonce le nouveau reglage.
	for id = range c.lldpSaid {
		if !watched[id] {
			delete(c.lldpSaid, id)
		}
	}
	for id = range c.snmpReach {
		if !watched[id] {
			delete(c.snmpReach, id)
		}
	}
	c.trapGate.forget(watched)
}

// Run boucle jusqu'a l'arret : interroger ce qui est du, pousser ce qui attend.
func (c *SNMPCollector) Run(ctx context.Context) {
	// L'ecoute des traps se reconcilie dans sa propre goroutine : lier ou fermer
	// une socket n'a pas a retarder l'interrogation du parc.
	go c.trapLoop(ctx)
	// Le vidage aussi, et c'est un changement que la nouvelle cadence rend
	// necessary : il est cinq fois plus frequent qu'avant et porte le trafic de
	// tout le parc. Laisse sur le tic de cette boucle, un controleur lent — jusqu'a
	// 20 s de delai d'attente HTTP — aurait retarde d'autant l'INTERROGATION des
	// devices. La remontee aurait alors degrade la collecte, i.e.
	// exactement ce qu'un tampon existe pour empecher.
	go c.flushLoop(ctx)

	var tick = time.NewTicker(time.Second)
	defer tick.Stop()
	var now time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case now = <-tick.C:
			c.pollDue(ctx, now)
		}
	}
}

// flushLoop evacue le tampon vers le controleur.
//
// Jusqu'a `maxFlushBurst` lots par wake, et l'on stops from qu'un lot part
// INCOMPLET : c'est le signal qu'il n'y a plus rien en attente, et insister
// enverrait des requests vides. Voir aussi le retour de `flush` pour le cas d'un
// controleur qui refuse.
func (c *SNMPCollector) flushLoop(ctx context.Context) {
	var tick = time.NewTicker(flushEvery)
	defer tick.Stop()
	// Ces deux compteurs sont LOCAUX a cette goroutine : personne d'autre ne les
	// lit, donc aucun verrou a prendre et rien a ajouter a la structure. Les
	// ranger dans `SNMPCollector` aurait ouvert un troisieme etat partage pour un
	// besoin qui ne quitte jamais cette boucle.
	var saidAt time.Time
	var saidDropped uint64
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			var i int
			for i = 0; i < maxFlushBurst; i++ {
				if !c.flush(ctx) {
					break
				}
			}
			saidAt, saidDropped = c.reportSaturation(saidAt, saidDropped)
		}
	}
}

// reportSaturation journalise une perte de points par SATURATION.
//
// `flush` ne parlait que des pertes accompagnant un failure d'envoi, et ce n'est pas
// le same symptome : un tampon qui deborde parce que la collecte produit plus vite
// que la remontee n'evacue est le mode failure que la cadence a 5 s rend possible,
// et il est parfaitement SILENCIEUX vu de l'exploitant — le tunnel est debout, les
// requests reussissent, et il ne reste que des trous dans les courbes. C'est donc
// la perte qu'il fallait nommer.
//
// Le compte annonce est celui de la PERIOD, pas le cumul depuis le demarrage :
// « 42 000 points perdus depuis la last minute » se decide, « 3 200 000 depuis
// le demarrage » se subit. Le rate soutenable accompagne la ligne, parce que la
// first question de celui qui la lit est « combien puis-je en encaisser ? ».
//
// Rend le nouveau couple (instant du dernier dit, cumul already dit).
func (c *SNMPCollector) reportSaturation(saidAt time.Time,
	saidDropped uint64) (time.Time, uint64) {
	var st = c.buf.Stats()
	if st.Dropped <= saidDropped {
		return saidAt, saidDropped
	}
	var now = time.Now()
	// `saidAt` nul = on n'a encore rien dit : la FIRST perte se dit tout de
	// suite. L'amortissement ne doit pas retarder l'apparition du symptome, il
	// doit seulement empecher sa repetition.
	if !saidAt.IsZero() && now.Sub(saidAt) < dropReportEvery {
		return saidAt, saidDropped
	}
	slog.Warn("tampon de metriques sature : des points de mesure sont perdus",
		"perdus_depuis", st.Dropped-saidDropped, "perdus_total", st.Dropped,
		"en_attente", st.Queued, "capability", st.Cap,
		"soutenable_par_s", sustainedSamplesPerSecond())
	return now, st.Dropped
}

// sustainedSamplesPerSecond rend le rate que la remontee evacue en regime
// permanent. Calcule, jamais recopie : c'est le chiffre qui doit changer TOUT
// SEUL quand on retouche `flushEvery`, `maxPerFlush` ou `maxFlushBurst`, sans quoi
// le journal finirait par annoncer une capability qui n'existe plus.
//
// Le calcul passe par `time.Duration` (un int64) et non par un entier de
// plateforme : le produit intermediaire vaut 1,2 × 10¹³ nanosecondes, ce qui
// deborderait un `int` de 32 bits. La forme est aussi la seule qui reste juste si
// `flushEvery` descendait sous la seconde.
func sustainedSamplesPerSecond() int {
	return int(time.Duration(maxFlushBurst*maxPerFlush) * time.Second / flushEvery)
}

func (c *SNMPCollector) due(key string, now time.Time, every int) bool {
	if every <= 0 {
		return false
	}
	var prev, seen = c.last[key]
	if seen && now.Sub(prev) < time.Duration(every)*time.Second {
		return false
	}
	c.last[key] = now
	return true
}

// pollDue interroge les cibles dont la cadence est echue.
func (c *SNMPCollector) pollDue(ctx context.Context, now time.Time) {
	c.mu.Lock()
	var watches = append([]tunnel.SNMPWatch{}, c.watches...)
	var jobs []func()
	var w tunnel.SNMPWatch
	for _, w = range watches {
		var target = targetOf(w)
		if c.due(w.DeviceID+"|state", now, w.StateIntervalS) {
			jobs = append(jobs, c.job(ctx, target, snmppoll.MetricIfState, triggerPoll, ""))
		}
		if c.due(w.DeviceID+"|counters", now, w.CountersIntervalS) {
			jobs = append(jobs, c.job(ctx, target, snmppoll.MetricIfCounters, triggerPoll, ""))
		}
		// Les deux jeux de CAPTEURS, et leur cadence peut valoir zero — auquel cas
		// `due` rend faux et rien n'est interroge. Ce n'est pas un cas degrade mais le
		// regime NORMAL d'un device dont le connecteur ne declares pas ces canaux :
		// contrairement a state et aux compteurs, qui tiennent dans MIB-II et existent
		// partout, la sante et le diagnostic optique reposent sur des branches
		// proprietaires. Les interroger sur un device qui ne les sert pas ne
		// rendrait que des « no such object », a chaque cycle et sur tout le parc.
		//
		// C'est le controleur qui decide de les allumer, parce qu'il est le seul a
		// connaitre les capabilities du connecteur (cf. tunnel.SNMPWatch).
		if c.due(w.DeviceID+"|health", now, w.HealthIntervalS) {
			jobs = append(jobs, c.job(ctx, target, snmppoll.MetricHealth, triggerPoll, ""))
		}
		if c.due(w.DeviceID+"|optical", now, w.OpticalIntervalS) {
			jobs = append(jobs, c.job(ctx, target, snmppoll.MetricOptical, triggerPoll, ""))
		}
		// Le VOISINAGE, et son travail n'est pas de la same famille que les quatre
		// autres : il ne rend pas d'samples et ne passe pas par le tampon. Il est
		// ordonnance ici quand same — rather que dans une boucle a lui — parce que c'est
		// la same consigne, la same cible et le same plafond de parallelisme qui doivent
		// s'y appliquer. Deux ordonnanceurs sur la same liste de cibles finiraient par
		// interroger un device retire de la surveillance par l'un et pas par l'autre.
		if c.due(w.DeviceID+"|lldp", now, w.LLDPIntervalS) {
			jobs = append(jobs, c.lldpJob(ctx, target))
		}
	}
	c.mu.Unlock()

	// En parallele, mais borne : un parc de deux cents devices interroges
	// tous en same temps saturerait la table NAT du proxy avant de saturer quoi
	// que ce soit d'autre.
	var sem = make(chan struct{}, 16)
	var wg sync.WaitGroup
	var j func()
	for _, j = range jobs {
		wg.Add(1)
		sem <- struct{}{}
		go func(run func()) {
			defer wg.Done()
			defer func() { <-sem }()
			run()
		}(j)
	}
	wg.Wait()
}

// targetOf traduit une consigne du controleur en cible interrogeable. Un seul
// endroit, pour que le poll ordinaire et le poll avance par un trap parlent au
// same device avec les memes parameters.
func targetOf(w tunnel.SNMPWatch) snmppoll.Target {
	return snmppoll.Target{
		DeviceID: w.DeviceID, IP: w.IP, Community: w.Community, Port: w.Port,
		MaxIfIndex: w.MaxIfIndex,
	}
}

// --- Ce que les jeux de capteurs changent au RATE, et ce qu'ils ne changent pas ---
//
// Le calcul de rate at the top de ce fichier reste juste, et il vaut mieux dire pourquoi
// que de le laisser supposer : `health` et `optical_power` sont negligeables devant
// `if_counters`, d'un facteur qui se mesure en centaines.
//
//	if_counters      52 ports × 4 series      toutes les 5 s     41,6 samples/s
//	if_state         52 ports × 2 series      toutes les 30 s     3,5 samples/s
//	health           ~8 series d'device   toutes les 300 s    0,03 sample/s
//	optical_power    4 cages × 8 series       toutes les 60 s     0,53 sample/s
//
// Autrement dit, les deux nouveaux jeux ajoutent moins de 1,3 % au rate d'un
// device de 48 ports. Le plafond de 133 devices par proxy ne bouge pas, et il
// n'y avait aucune raison de retoucher `maxPerFlush` ni `maxFlushBurst` pour ce lot.
//
// Ce qu'ils coutent est ailleurs, et c'est sur l'DEVICE : `health` demande seize
// walks distincts parce que les capteurs de la MIKROTIK-MIB sont des scalaires d'une
// same branche, qu'on ne peut donc pas parcourir d'un coup sans les ecraser les uns sur
// les autres (cf. l'en-tete de internal/snmppoll/health.go). Seize requests toutes les
// cinq minutes font un paquet toutes les vingt secondes : c'est deux ordres de grandeur
// sous ce que costs already le trafic, et c'est ce qui rend la cadence de cinq minutes
// confortable rather que prudente.

// job rend le travail d'une interrogation. `trigger` et `trapLabel` ne changent
// rien a ce qui est LU — ils ne servent only qualifier les evenements qui en
// sortent : ce qui nous a fait regarder maintenant, pas hence vient la verite.
func (c *SNMPCollector) job(ctx context.Context, t snmppoll.Target,
	metric, trigger, trapLabel string) func() {
	return func() {
		var p = &snmppoll.Poller{Target: t}
		var samples, err = p.Poll(ctx, metric)
		if err != nil {
			// Un device muet est un fait d'exploitation ordinaire — il est
			// off, ou SNMP n'y est pas encore configure. Le journaliser a chaque
			// cycle noierait le reste : DEBUG, pas WARN.
			//
			// Et surtout : on ne touche PAS aux etats de port. Un poll qui fails ne
			// prouve rien sur les ports ; en conclure qu'ils sont tombes ferait
			// clignoter une front panel entire a chaque paquet UDP perdu, alors que le
			// suivi ICMP dit already, lui, si the device repond.
			slog.Debug("SNMP sans response", "device", t.DeviceID, "ip", t.IP,
				"metric", metric, "err", err.Error())
			// Reachability for if_state / if_counters only: those are MIB-II floors
			// every agent answers. A failed optical walk on a copper switch must not
			// paint the SNMP pill red.
			if metric == snmppoll.MetricIfState || metric == snmppoll.MetricIfCounters {
				c.noteSNMPReach(t.DeviceID, t.IP, false)
			}
			return
		}
		if metric == snmppoll.MetricIfState || metric == snmppoll.MetricIfCounters {
			c.noteSNMPReach(t.DeviceID, t.IP, true)
		}
		// La metrique continue son chemin habituel : le lot part au prochain flush.
		c.buf.AddAll(samples)
		if metric == snmppoll.MetricIfState {
			c.notePortStates(t.DeviceID, samples, trigger, trapLabel)
		}
	}
}

// motifMaxBytes borne la lecture du motif de refus. Ces corps-there sont des responses
// d'erreur JSON de quelques dizaines d'octets ; lire sans borne ce qu'un
// intermediaire mal regle peut renvoyer (une page HTML d'erreur de plusieurs
// centaines de kilooctets) ferait payer au proxy la memory de ce qu'il refuse already.
const motifMaxBytes = 512

// refusalReason extrait ce que le controleur DIT de son refus.
//
// Ne sert que sur le chemin d'ABANDON, et c'est deliberate. C'est le seul where des
// points meurent : « 3000 points abandonnes, status 413 » laisse l'exploitant
// deviner lequel de ses reglages est en cause, alors que la phrase du controleur la
// nomme (cf. batchGeste, internal/api/proxymetrics_handlers.go). Sur le chemin du
// rejeu, au contraire, la same ligne reviendrait toutes les deux secondes : rien a y
// gagner, et un corps a lire a chaque tentative.
//
// Un corps tronque a motifMaxBytes n'est plus du JSON valide : le repli sur le texte
// brut est donc la branche NORMALE de ce cas, pas un cas d'erreur.
func refusalReason(r io.Reader) string {
	var raw, err = io.ReadAll(io.LimitReader(r, motifMaxBytes))
	if err != nil || len(raw) == 0 {
		return ""
	}
	var payload struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(raw, &payload) == nil && payload.Error != "" {
		return payload.Error
	}
	return strings.TrimSpace(string(raw))
}

// refusDefinitif dit si un refus du controleur est DEFINITIF, i.e. si
// rejouer le same lot ne peut pas en changer le verdict.
//
// C'est la frontiere la plus chargee de ce fichier, parce que les deux erreurs
// possibles ne coutent pas le same prix :
//
//   - croire definitif ce qui was passager PERD un lot qui serait passe dix
//     secondes plus tard. Une perte bornee, visible, et de la valeur d'un lot ;
//   - croire passager ce qui was definitif ARRETE la remontee. `Nack` rend le lot
//     EN TETE de file, donc le `Take` suivant reprend exactement le same : un lot
//     refused pour sa forme revient IDENTIQUE au flush suivant, et a tous les
//     suivants. Le tampon ne se vide plus, il deborde par le haut, et ce sont les
//     points les plus RECENTS que la collecte perd. Pas une perte partielle : la fin
//     de la remontee pour ce proxy.
//
// La seconde erreur est sans borne, la first non — et pourtant la liste des cas
// definitifs est FERMEE : ce qu'on ne sait pas classer est traite comme PASSAGER,
// donc du cote de l'erreur la plus chere. Ce n'est pas une contradiction, c'est un
// arbitrage sur la REPARABILITE, et il faut le dire dans ce sens-there :
//
//   - un abandon est IRREVERSIBLE et discret. Les points sont detruits, et le seul
//     temoin est une ligne de journal que personne ne lira peut-be jamais ;
//   - un blocage est BRUYANT et reparable. Chaque refus se journalise, la saturation
//     se crie chaque minute, et les points attendent dans le tampon. Un exploitant
//     finit par voir le symptome, et le lot repart from que la cause est corrigee.
//
// Face a un code de retour qu'on n'a pas planned, on ne sait pas lequel des deux cas
// on a sous la main. Choisir le blocage laisse une chance de comprendre et de
// reparer ; choisir l'abandon detruit la data avant qu'on ait pu se poser la
// question. Hence enumeration EXPLICITE des deux cotes : le repli ne rattrape que
// des codes que cette route ne produit pas.
//
// Cas par cas, et le raisonnement rather que le code :
//
//   - `Retry-After` present → PASSAGER, quel que soit le code. Un refus qui dit
//     quand revenir est un refus qui attend qu'on revienne. Teste en PREMIER, parce
//     que c'est exactement ce qu'un `>= 400` un peu large rangerait du mauvais cote :
//     un 429 ou un 503 de maintenance sont des demandes de patience, pas des refus ;
//   - 400 → DEFINITIF. Le controleur a lu le corps et n'en veut pas. Les memes
//     octets rendront le same verdict, aussi longtemps qu'on les renverra. C'est le
//     same raisonnement que le `Ack` sur un `json.Marshal` en failure, plus bas dans
//     `flush` : illisible, le rejouer ne changerait rien ;
//   - 413 → DEFINITIF. Corps au-dela de `maxProxyMetricsBody`, ou lot au-dela de
//     `maxSamplesPerBatch`. Le contenu est bon, c'est le VOLUME qui ne passe pas — et
//     rejouer a l'identique ne retrecit rien (sur le redecoupage, voir plus bas) ;
//   - 415 → DEFINITIF. `flush` envoie toujours le same `Content-Type` ;
//   - 422 → DEFINITIF. Le controleur a compris la request et refuse le fond.
//
// Et les refus qu'on rejoue, ceux dont il faut dire POURQUOI ils ne sont pas dans
// la liste :
//
//   - 401 / 403 → PASSAGER, et c'est la decision la plus discutable du lot. Elle se
//     tient sur un fait : ici le controleur ne reproche rien AU LOT — il ne l'a same
//     pas regarde, l'authentification passant after la lecture du corps. Un secret se
//     rotationne, une revocation s'annule, un controleur qui redemarre peut refuser
//     une seconde avant d'accepter ; abandonner jetterait des points qu'un
//     enrolement a jour aurait pris. L'objection est reelle — un proxy revoque rejoue
//     pour toujours et ne remonte plus rien — mais elle ne se repare pas ici :
//     abandonner sur 403 ne remonterait pas un point de plus, ca n'en effacerait que
//     la trace. Le tampon est borne, il jette ses plus vieux, `reportSaturation` le
//     dit toutes les minutes et chaque refus a sa ligne. Un proxy revoque n'a de
//     toute way plus de destinataire pour ses metriques ;
//   - 404 / 405 → PASSAGER. Route absente : controleur d'une version anterieure, ou
//     URL mal reglee. Un deploiement ou une correction de configuration repare, et le
//     lot repart alors tel quel — c'est precisely ce qu'un tampon existe pour
//     couvrir ;
//   - 408 / 429 → PASSAGER par definition, avec ou sans `Retry-After` ;
//   - 5xx → PASSAGER, sans exception. Le controleur est en difficulte, pas en
//     desaccord : 502 sur une write VictoriaMetrics en failure, 503 sur un stockage
//     non configure (cf. handleProxyMetrics, qui repond ces deux-there precisely POUR
//     que le proxy garde son lot).
//
// # Pourquoi un refus par la TAILLE s'abandonne aussi
//
// C'est le cas where l'abandon fait le plus mal : le contenu du lot est bon, seul son
// volume ne passe pas, et le REDECOUPER serait mieux que le jeter. L'API de
// `metricbuf` ne le permet pas naturellement. `Take` rend le lot reserve TEL QUEL en
// ignorant son argument tant que ce lot n'est ni acquitte ni rendu ; il n'existe ni
// acquittement partiel ni redimensionnement. Le seul chemin serait `Nack` — qui rend
// le lot at the top — puis un `Take` plus petit au tour suivant, et il faudrait alors
// memoriser la taille reduite dans le collecteur, decider quand la faire remonter, et
// la faire cohabiter avec `maxFlushBurst`. Soit un etat supplementaire et une
// politique de repli entire pour un cas que le controleur ne produit plus : a 8 MiB
// de plafond, un lot de 3000 en fait 672 KiB. L'abandon journalise suffit, et il a
// l'avantage de ne pas pouvoir boucler.
//
// Prend le code et l'en-tete rather que la response entire : la frontiere est une
// decision sur deux valeurs, et elle doit s'eprouver cas par cas sans fabriquer de
// response HTTP (cf. TestFrontiereDuRefusDefinitif).
func refusDefinitif(status int, retryAfter string) bool {
	if retryAfter != "" {
		return false
	}
	switch status {
	case http.StatusBadRequest, http.StatusRequestEntityTooLarge,
		http.StatusUnsupportedMediaType, http.StatusUnprocessableEntity:
		return true
	}
	return false
}

// flush pousse un lot vers le controleur.
//
// Take/Ack/Nack : tant que le controleur n'a pas confirmed, le lot reste reserve
// et revient dans le tampon si l'envoi fails. C'est ce qui fait qu'une coupure
// de tunnel ne perd pas les minutes qu'elle dure — SAUF sur un refus definitif, where
// le lot est abandonne pour que le tampon puisse avancer (voir refusDefinitif).
//
// Un abandon se journalise toujours, avec le nombre de points sacrifies. Un abandon
// silencieux serait pire que la boucle qu'il remplace : la boucle, elle, finit par
// se voir.
//
// Rend vrai quand un lot PLEIN est parti : il reste alors probablement de quoi en
// remplir un autre tout de suite, et c'est ce qui autorise l'appelant a enchainer
// (cf. maxFlushBurst). Faux arrete la rafale, dans les cas where continuer serait
// nuisible ou inutile : rien a envoyer, lot incomplet donc tampon vide, ou
// controleur qui vient de refuser. Y COMPRIS sur un abandon, alors que le tampon a
// pourtant avance : si le refus est systematique — un controleur qui refuse tout ce
// que ce proxy lui envoie — enchainer la rafale sacrifierait quatre lots par wake
// au lieu d'un. Perdre lentement laisse au journal le temps d'be lu.
func (c *SNMPCollector) flush(ctx context.Context) bool {
	var lot = c.buf.Take(maxPerFlush)
	if len(lot) == 0 {
		return false
	}
	var body, err = json.Marshal(map[string]any{
		"proxy_id": c.proxyID, "secret": c.secret, "samples": lot,
	})
	if err != nil {
		c.buf.Ack() // illisible : le rejouer ne changerait rien
		return false
	}
	var req *http.Request
	req, err = http.NewRequestWithContext(ctx, http.MethodPost,
		c.controllerURL+"/api/proxy/metrics", bytes.NewReader(body))
	if err != nil {
		c.buf.Nack()
		return false
	}
	req.Header.Set("Content-Type", "application/json")
	var resp *http.Response
	if resp, err = c.client.Do(req); err != nil {
		c.buf.Nack()
		var st = c.buf.Stats()
		slog.Warn("metriques non poussees, conservees en tampon",
			"err", err.Error(), "en_attente", st.Queued, "perdues", st.Dropped)
		return false
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if refusDefinitif(resp.StatusCode, resp.Header.Get("Retry-After")) {
			// Le motif se lit AVANT l'abandon, tant que le corps est encore ouvert.
			var motif = refusalReason(resp.Body)
			c.buf.Ack()
			// « abandonnes » est le mot qui compte dans cette ligne : c'est un nombre
			// de points de mesure qui n'existeront plus jamais, et il n'a pas d'autre
			// endroit where se dire. La taille du lot l'accompagne parce que le refus le
			// plus probable ici reste le refus par le VOLUME, et le diagnostic passe a
			// cote sans ce chiffre.
			slog.Warn("metriques ABANDONNEES : refus definitif du controleur, "+
				"les rejouer ne changerait rien",
				"status", resp.StatusCode, "abandonnes", len(lot),
				"octets", len(body), "motif", motif,
				"en_attente", c.buf.Stats().Queued)
			return false
		}
		c.buf.Nack()
		// Rendu au tampon : ce lot repartira tel quel au prochain wake. Le dire —
		// « conservees », pas « perdues » — evite de faire chercher une perte qui n'a
		// pas eu lieu.
		slog.Warn("metriques refusees par le controleur, conservees en tampon",
			"status", resp.StatusCode, "samples", len(lot),
			"octets", len(body), "en_attente", c.buf.Stats().Queued)
		return false
	}
	c.buf.Ack()
	return len(lot) == maxPerFlush
}
