package main

import (
	"context"
	"log/slog"
	"net"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gosnmp/gosnmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"

	"meshcraft/internal/metricbuf"
	"meshcraft/internal/snmppoll"
	"meshcraft/internal/tunnel"
)

// Reception des traps SNMP, et detection des transitions de port.
//
// Deux idees gouvernent ce fichier, et elles se tiennent l'une l'autre.
//
//  1. Un trap ne porte jamais la verite, seulement l'URGENCE. Un trap v2c est de
//     l'UDP non authentifie : falsifiable, perdable, reordonnable. Rien de son
//     contenu n'est written dans state — il ne fait qu'AVANCER le poll `if_state`
//     qui aurait eu lieu de toute way (cf. tunnel.EventPortUp).
//  2. Les transitions sont donc computed a CHAQUE poll `if_state`, quel qu'en
//     soit le declencheur, en diffant contre la last observation. Corollaire
//     d'exploitation : un canal de traps coupe, sature ou absent ne degrade que
//     la LATENCE — on retombe sur la cadence de polling — et jamais la justesse.
//
// Ce qui couterait cher serait de melanger les deux : deux sources state pour un
// same port, qu'il faudrait ensuite arbitrer a chaque desaccord.

// Declencheurs portes par tunnel.Event.Trigger. Ils ne disent pas hence vient la
// verite (toujours du poll), mais ce qui a economise l'attente.
const (
	triggerPoll = "poll"
	triggerTrap = "trap"
)

// metricIfOperStatus recopie le nom de metrique emis par snmppoll.Samples.
//
// Ce nom y est une constante non exportee, et l'ouvrir n'appartient pas a ce lot.
// La duplication est donc assumee, mais elle est VERIFIEE : un test fait passer
// une table `ifOperStatus` par snmppoll.Samples et verifies qu'observedPorts la
// retrouve. Un renommage there-bas casse ici un test, pas la production en silence.
const metricIfOperStatus = "meshcraft_if_oper_status"

// Valeurs d'`ifOperStatus` (MIB-II, RFC 2863) qui nous concernent.
//
// « up » est reserve a la valeur 1, et a elle seule. `lowerLayerDown(7)` est un
// port qui ne passe pas de trafic : le classer ailleurs que dans « down »
// obligerait the UI a inventer un troisieme etat pour dire la same chose. Les
// valeurs intermediaires — testing(3), unknown(4), dormant(5), notPresent(6) —
// sont traitees comme « down » et la valeur brute est portee dans le Detail : on
// n'affirme pas qu'un port est en service quand the device ne l'affirme pas.
//
// Le bruit redoute (un port qui oscillerait 2 ↔ 7 en emettant deux evenements
// par battement) n'existe pas ici, parce que le diff porte sur l'ETAT BOOLEEN et
// pas sur la valeur : 2 → 7 ne produit aucun evenement.
const (
	ifOperUp             = 1
	ifOperDown           = 2
	ifOperLowerLayerDown = 7
)

// OID des traps standard. On n'en reconnait que deux, et c'est deliberate : le
// poll dit la verite, le trap ne fait que wake. Un catalogue de traps
// constructeur serait du travail a refaire a chaque gamme, pour une information
// qu'on relit de toute way dans `ifTable` la milliseconde suivante.
const (
	oidSnmpTrapOID = "1.3.6.1.6.3.1.1.4.1.0" // snmpTrapOID.0
	oidLinkDown    = "1.3.6.1.6.3.1.1.5.3"
	oidLinkUp      = "1.3.6.1.6.3.1.1.5.4"
)

// trapBindRetry : delai avant de retenter un bind qui a echoue. Le port 162 peut
// se liberer (un autre collecteur qu'on desinstalle), mais reessayer chaque
// seconde ne le libererait pas plus vite.
const trapBindRetry = time.Minute

// trapSummaryEvery : cadence du bilan de receive en DEBUG. Sans ce bilan, un
// parc where aucun device n'est configure pour envoyer ses traps est
// indiscernable d'un parc reactif.
const trapSummaryEvery = 5 * time.Minute

// --- Detection des transitions : logique pure ---

// portObs est ce qu'un poll a vu d'un port : son etat operationnel et son nom.
//
// Le nom ILLUSTRE, l'index IDENTIFIE (cf. tunnel.Event.IfIndex) : il n'entre pas
// dans la comparaison, sinon renommer un port fabriquerait une transition.
type portObs struct {
	status int
	name   string
}

// portUp traduit `ifOperStatus` en « ce port passe du trafic ».
func portUp(status int) bool { return status == ifOperUp }

// operStatusLabel nomme une valeur d'`ifOperStatus`. Utile seulement au Detail et
// au journal : un operator lit « dormant » beaucoup plus vite que « 5 ».
func operStatusLabel(status int) string {
	switch status {
	case ifOperUp:
		return "up"
	case ifOperDown:
		return "down"
	case 3:
		return "testing"
	case 4:
		return "unknown"
	case 5:
		return "dormant"
	case 6:
		return "notPresent"
	case ifOperLowerLayerDown:
		return "lowerLayerDown"
	}
	return "inconnu"
}

// observedPorts extrait d'un lot d'samples state de chaque port.
//
// On repart des samples ALREADY en main rather que de refaire un walk : la
// table a been lue une fois, la relire pour en tirer une autre conclusion
// doublerait le cost sur the device et ouvrirait la porte a deux lectures
// divergentes du same instant.
func observedPorts(samples []metricbuf.Sample) map[int]portObs {
	var out = map[int]portObs{}
	var s metricbuf.Sample
	for _, s = range samples {
		if s.Name != metricIfOperStatus {
			continue
		}
		var idx, err = strconv.Atoi(s.Labels["ifindex"])
		if err != nil {
			continue // sans index, l'sample n'identifie aucun port
		}
		// L'tag `port` est absente quand `ifDescr` n'a pas pu be lue : le
		// nom reste vide, la detection fonctionne quand same.
		out[idx] = portObs{status: int(s.Value), name: s.Labels["port"]}
	}
	return out
}

// portChange est une transition retenue, avant sa mise en forme en evenement.
type portChange struct {
	ifIndex int
	name    string
	up      bool
	status  int
}

// diffPorts compare deux observations d'un same device.
//
// Trois silences, et chacun evite d'inventer un fait :
//
//   - Un port vu pour la FIRST fois ne produit rien. Annoncer une transition
//     au premier passage reviendrait a annoncer un changement qui n'a pas eu lieu
//     — same raison que le `up == nil` de targetState dans monitor.go.
//   - Un port DISPARU de la table ne produit rien : un membre de pile retire ou
//     une front panel retrecie est une decision d'exploitation, pas une panne.
//   - Un changement de valeur qui ne change pas state booleen (2 → 7) ne produit
//     rien : c'est la same information dite autrement.
func diffPorts(prev, cur map[int]portObs) []portChange {
	var out []portChange
	var idx int
	var obs portObs
	for idx, obs = range cur {
		var before, known = prev[idx]
		if !known || portUp(before.status) == portUp(obs.status) {
			continue
		}
		out = append(out, portChange{
			ifIndex: idx, name: obs.name, up: portUp(obs.status), status: obs.status,
		})
	}
	// Un 48 ports qui redemarre produit quarante-huit transitions d'un coup :
	// l'ordre de parcours d'une map est aleatoire, et un journal qui egrene les
	// ports dans un ordre different a chaque incident est illisible.
	sort.Slice(out, func(i, j int) bool { return out[i].ifIndex < out[j].ifIndex })
	return out
}

// portDetail compose le texte d'accompagnement d'un evenement de port.
//
// Deux informations de nature differente peuvent s'y croiser, et il faut les
// distinguer : la valeur exotique d'`ifOperStatus` describes CE port et vient du
// poll ; le label du trap qualifie la RAFALE qui nous a reveilles, pas le port
// — c'est un indice de diagnostic, jamais une data.
func portDetail(ch portChange, trapLabel string) string {
	var parts []string
	if ch.status != ifOperUp && ch.status != ifOperDown {
		parts = append(parts, "ifOperStatus="+strconv.Itoa(ch.status)+
			" ("+operStatusLabel(ch.status)+")")
	}
	if trapLabel != "" {
		parts = append(parts, trapLabel)
	}
	return strings.Join(parts, " ; ")
}

// --- Detection des transitions : cablage sur le collecteur ---

// notePortStates diffe l'observation courante contre la previous et emet les
// transitions du parc.
//
// Appelee after CHAQUE poll `if_state` reussi, et seulement pour celui-there :
// `if_counters` ne contient pas d'`ifOperStatus`, et le passer ici ferait
// disparaitre tous les ports a chaque cycle de compteurs.
func (c *SNMPCollector) notePortStates(deviceID string,
	samples []metricbuf.Sample, trigger, trapLabel string) {
	var cur = observedPorts(samples)
	if len(cur) == 0 {
		// Un poll qui a rendu des samples mais aucun `ifOperStatus` est une
		// lecture PARTIELLE (la table a manque au walk), pas un device dont les
		// ports se sont volatilises. On garde l'observation previous : l'ecraser
		// ferait de la prochaine lecture une « first observation », donc un
		// silence sur la transition suivante.
		return
	}

	c.mu.Lock()
	var changes = diffPorts(c.portStates[deviceID], cur)
	c.portStates[deviceID] = cur
	var ip string
	var w tunnel.SNMPWatch
	var found bool
	if w, found = findWatch(c.watches, deviceID); found {
		ip = w.IP
	}
	c.mu.Unlock()

	var ch portChange
	for _, ch = range changes {
		var kind = tunnel.EventPortDown
		if ch.up {
			kind = tunnel.EventPortUp
		}
		c.emitEvent(tunnel.Event{
			Type: kind, DeviceID: deviceID, IP: ip,
			IfIndex: ch.ifIndex, PortName: ch.name, Trigger: trigger,
			Detail: portDetail(ch, trapLabel),
		})
	}
}

// emitEvent depose un evenement sur le canal du proxy, sans jamais bloquer.
func (c *SNMPCollector) emitEvent(ev tunnel.Event) {
	c.mu.Lock()
	var fn = c.emit
	c.mu.Unlock()
	if fn == nil {
		// Collecteur pas encore cable (ou test) : on le dit sans paniquer ni perdre
		// le poll qui a produit l'evenement.
		slog.Debug("evenement de port sans canal de sortie", "type", ev.Type)
		return
	}
	fn(ev)
}

// findWatch retrouve une cible par son identifiant. Appelee sous c.mu.
func findWatch(watches []tunnel.SNMPWatch, deviceID string) (tunnel.SNMPWatch, bool) {
	var w tunnel.SNMPWatch
	for _, w = range watches {
		if w.DeviceID == deviceID {
			return w, true
		}
	}
	return tunnel.SNMPWatch{}, false
}

// --- Filtre identity ---

// trapVerdict dit ce qu'on a fait d'un trap, et pourquoi. Les trois cas sont
// COMPTES : un canal qui rejette tout et un canal muet se ressemblent beaucoup
// trop dans un journal.
type trapVerdict int

const (
	// trapUnknownSource : l'IP source n'est dans aucune SNMPWatch. Sur un network
	// de production, c'est l'ordinaire — d'autres devices arrosent le
	// collecteur — et ca ne merite pas mieux que DEBUG.
	trapUnknownSource trapVerdict = iota
	// trapBadCommunity : l'IP est connue, la community ne correspond pas. C'est
	// ce cas qui empeche un tiers sur le chemin d'injecter des evenements de port
	// en usurpant l'adresse d'un device surveille.
	trapBadCommunity
	trapAccepted
)

// effectiveCommunity rend la community actually attendue d'un device.
//
// Vide signifie « public » et non « n'importe laquelle » : c'est exactement le
// repli de snmppoll.dialSNMP, et deux rules differentes pour la same watch
// finiraient par accepter des traps d'un device qu'on n'arrive pas a
// interroger.
func effectiveCommunity(w tunnel.SNMPWatch) string {
	if w.Community == "" {
		// La SAME constante que le poll, et non un « public » recopie ici : ce repli
		// a existe en trois exemplaires dans ce depot, et le jour where l'un d'eux
		// change, le filtre des traps accepte des paquets d'un device qu'on
		// n'arrive plus a interroger — ou l'inverse, sans que rien ne le dise.
		return snmppoll.DefaultCommunity
	}
	return w.Community
}

// classifyTrap decide si un trap vient d'un device surveille.
//
// Identite par IP SOURCE confrontee a la liste de surveillance, plus la
// community. C'est faible — une IP source s'usurpe — mais c'est tout ce qu'un
// trap v2c offre, et c'est precisely pour ca que rien de son contenu n'est
// written : au pire, un attaquant obtient un walk `if_state` anticipe, dont le
// result vient de the device et pas de lui.
//
// Les traps v3 arrivent sans community et sont donc rejetes : la v3 viendra avec
// identity de service (cf. tunnel.SNMPWatch.Community), et un « on accepte
// quand on ne sait pas verify » serait le contraire de ce filtre.
func classifyTrap(watches []tunnel.SNMPWatch, srcIP, community string) (tunnel.SNMPWatch, trapVerdict) {
	var src, err = netip.ParseAddr(srcIP)
	if err != nil {
		return tunnel.SNMPWatch{}, trapUnknownSource
	}
	// Unmap : une socket UDP en dual-stack rend « ::ffff:10.0.0.5 » there where la watch
	// dit « 10.0.0.5 ». Comparer les textes ferait echouer le filtre selon
	// l'humeur de la pile network de l'hote.
	src = src.Unmap()

	var ipKnown bool
	var w tunnel.SNMPWatch
	for _, w = range watches {
		var ip, parseErr = netip.ParseAddr(w.IP)
		if parseErr != nil || ip.Unmap() != src {
			continue
		}
		ipKnown = true
		if community == effectiveCommunity(w) {
			return w, trapAccepted
		}
	}
	if ipKnown {
		return tunnel.SNMPWatch{}, trapBadCommunity
	}
	return tunnel.SNMPWatch{}, trapUnknownSource
}

// trapOIDLabel rend l'OID du trap recu et, si on le reconnait, son nom.
//
// Pour un trap v2c, l'OID vit dans le varbind `snmpTrapOID.0` ; un trap v1 le
// porte autrement (Enterprise + GenericTrap) et rend ici une string vide. Ce
// n'est pas une lacune a combler : la valeur ne sert qu'au journal et au Detail.
func trapOIDLabel(pkt *gosnmp.SnmpPacket) (string, string) {
	if pkt == nil {
		return "", ""
	}
	var oid string
	var v gosnmp.SnmpPDU
	for _, v = range pkt.Variables {
		if strings.TrimPrefix(v.Name, ".") != oidSnmpTrapOID {
			continue
		}
		var s, ok = v.Value.(string)
		if ok {
			oid = strings.TrimPrefix(s, ".")
		}
		break
	}
	switch oid {
	case oidLinkUp:
		return oid, "linkUp"
	case oidLinkDown:
		return oid, "linkDown"
	}
	return oid, ""
}

// trapSeenDetail compose le Detail de l'evenement de preuve : ce qui a traverse.
//
// Le label quand on reconnait le trap, l'OID brut sinon — c'est ce qui permet a
// l'exploitant de distinguer « the device m'envoie ses linkDown » de « il ne
// m'envoie que des traps constructeur », deux situations qui prouvent also le
// chemin mais ne se reglent pas pareil. Jamais VIDE, same pour un trap v1 qui ne
// porte pas snmpTrapOID.0 (cf. trapOIDLabel) : un Detail vide se lirait comme un
// champ perdu en route.
//
// Ce qui n'y entre sous aucune forme : la community. C'est un secret d'operation,
// et l'evenement part au controleur puis a the UI.
func trapSeenDetail(oid, label string) string {
	if label != "" {
		return label
	}
	if oid != "" {
		return "oid=" + oid
	}
	return "trap sans OID identifiable"
}

// --- Amortissement par device ---

// trapGate regroupe les rafales de traps d'un same device.
//
// Un 48 ports qui redemarre emet une centaine de traps en quelques
// millisecondes. Un poll par trap ferait du chemin rapide un amplificateur
// braque sur the device already en difficulte — et cette gamme (Instant On 1930)
// ne tient que cinq sessions SNMP. La table `ifTable` etant lue en entier de
// toute way, lire pour un port ou pour cinquante costs pareil : on regroupe.
//
// La decision est SEPAREE de l'ordonnancement (admit rend un delai, elle ne
// demarre aucun minuteur) pour qu'elle se teste sans horloge reelle.
type trapGate struct {
	mu sync.Mutex
	// pending : un poll est already programme pour cet device, les traps qui
	// arrivent sont absorbes par lui.
	pending map[string]bool
	// label : label du trap qui a OUVERT la fenetre courante.
	label map[string]string
	// lastPoll : dernier poll declenche par trap, qui fixe le plancher.
	lastPoll map[string]time.Time
	// seen : devices dont le chemin retour a ALREADY been prouve au controleur
	// (tunnel.EventTrapSeen emis une fois).
	//
	// Cet etat n'a rien a voir avec l'amortissement, et il vit pourtant ici : la
	// preuve doit se rearmer quand the device quitte puis retrouve la
	// surveillance, et `forget` est le SEUL point de purge que SetWatches
	// (cmd/proxy/snmp.go) appelle already. Une carte de plus sur le collecteur aurait
	// exige une ligne de plus there-bas — donc une modification d'un fichier qui ne
	// observes pas de ce lot, et surtout un second endroit a ne pas oublier le jour
	// where la purge changera. Rattachee a la structure already purgee, la property
	// « rien ne survit au retrait d'un device » se demontre en un seul point.
	seen map[string]bool
}

func newTrapGate() *trapGate {
	return &trapGate{
		pending:  map[string]bool{},
		label:    map[string]string{},
		lastPoll: map[string]time.Time{},
		seen:     map[string]bool{},
	}
}

// admit dit s'il faut programmer un poll pour cet device, et dans combien de
// temps.
//
// Le plancher tunnel.TrapMinPollGap RETARDE, il ne jette pas : un trap arrive
// juste after un poll signale peut-be un etat que ce poll n'a pas encore vu, et
// l'abandonner perdrait une transition reelle jusqu'au prochain cycle ordinaire.
// Un port qui bat en boucle produit donc au pire un walk toutes les deux
// secondes, ce qui est le comportement voulu.
func (g *trapGate) admit(deviceID, label string, now time.Time) (time.Duration, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.pending[deviceID] {
		// Fenetre already ouverte. Elle adopte tout de same le premier label
		// exploitable de la rafale : un trap constructeur suivi d'un linkDown doit
		// laisser « linkDown » au diagnostic.
		if g.label[deviceID] == "" {
			g.label[deviceID] = label
		}
		return 0, false
	}
	var delay = tunnel.TrapCoalesce
	var floor = g.lastPoll[deviceID].Add(tunnel.TrapMinPollGap)
	if now.Add(delay).Before(floor) {
		delay = floor.Sub(now)
	}
	g.pending[deviceID] = true
	g.label[deviceID] = label
	return delay, true
}

// release note qu'un poll declenche par trap vient d'avoir lieu, et rend le
// label de la rafale qu'il solde.
func (g *trapGate) release(deviceID string, at time.Time) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	var label = g.label[deviceID]
	delete(g.pending, deviceID)
	delete(g.label, deviceID)
	g.lastPoll[deviceID] = at
	return label
}

// firstSeen rend vrai UNE SEULE fois par device : au premier trap accepted
// depuis son ingress dans la surveillance.
//
// C'est le garde-fou de tunnel.EventTrapSeen. La preuve du chemin retour ne change
// plus une fois acquise ; la repeter a chaque trap ferait d'un parc bavard un
// generateur de journal, et noierait les evenements qui, eux, disent un changement.
//
// Test-et-pose sous le same verrou que le reste de la structure : aujourd'hui les
// traps d'une rafale arrivent tous sur la goroutine de lecture de gosnmp, mais rien
// dans son contrat ne le promet, et deux preuves pour un device seraient
// exactement le bruit qu'on cherche a eviter.
func (g *trapGate) firstSeen(deviceID string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.seen[deviceID] {
		return false
	}
	g.seen[deviceID] = true
	return true
}

// forget oublie un device qui n'est plus surveille. Sans ca, les cartes
// gardent trace de tout ce que le controleur a un jour confie a ce proxy.
//
// C'est aussi ce qui REARME la preuve du chemin retour (cf. seen) : un device
// qui revient dans la liste revient d'un monde where un pare-feu, une route ou une ACL
// a pu changer — la preuve d'hier n'y engage plus rien.
func (g *trapGate) forget(watched map[string]bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	var id string
	for id = range g.lastPoll {
		if !watched[id] {
			delete(g.lastPoll, id)
		}
	}
	for id = range g.pending {
		if !watched[id] {
			delete(g.pending, id)
			delete(g.label, id)
		}
	}
	for id = range g.seen {
		if !watched[id] {
			delete(g.seen, id)
		}
	}
}

// --- Reception ---

// trapStats compte ce que la socket a vu. Rien ici ne monte en WARN : des traps
// d'devices non surveilles, c'est l'ordinaire d'un network de production, et
// les journaliser fort noierait ce qui compte.
type trapStats struct {
	received      uint64
	accepted      uint64
	absorbed      uint64
	unknownSource uint64
	badCommunity  uint64
}

// trapHandler traite un trap decode : le paquet, sa source, et l'adresse locale
// qu'il visait — invalide quand le noyau ne sait pas la dire.
type trapHandler func(pkt *gosnmp.SnmpPacket, src *net.UDPAddr, dst netip.Addr)

// trapReceiver : une socket de traps en ecoute, avec le port qu'elle occupe.
//
// # Pourquoi NOTRE socket, et non gosnmp.TrapListener
//
// Parce que son contrat ne rend que la SOURCE du datagramme
// (`TrapHandlerFunc(*SnmpPacket, *net.UDPAddr)`) et que sa socket est un champ non
// exporte : rien, dans gosnmp, ne donne access a l'adresse de DESTINATION —
// i.e. a celle de nos adresses que the device a actually visee. Or
// c'est exactement ce qui manquait pour que la preuve du chemin retour soit
// attribuable a l'adresse que meshcraft a written (cf. tunnel.EventTrapSeen) : une
// ecoute sur l'adresse joker accepte tout, et un device configure vers un autre
// collecteur de la same machine fabriquait une « preuve » sans rapport.
//
// Ce que ca ne costs PAS, et c'est ce qui a fait la decision : rien du decoding
// SNMP. `gosnmp.GoSNMP.UnmarshalTrap` et `SnmpPacket.MarshalMsg` sont EXPORTES —
// on garde donc le parseur de la bibliotheque, et jusqu'a la response aux
// `InformRequest`. La seule chose perdue est le rapport d'engineID des traps v3
// (`reportAuthoritativeEngineID`), qui ne concerne que des paquets sans community,
// donc que `classifyTrap` jette de toute way.
//
// Et ca SIMPLIFIE le bind : `net.ListenUDP` rend son erreur tout de suite, there where
// `TrapListener.Listen` est bloquant et obligeait a courir after son signal
// d'ecoute — une goroutine, deux canaux et un delai de garde — pour distinguer
// « lie » de « refused ».
type trapReceiver struct {
	conn *net.UDPConn
	// p4 / p6 : la SAME socket vue par x/net, qui seule sait reclamer au noyau
	// l'adresse de destination de chaque datagramme. Une seule des deux est non
	// nulle ; les deux peuvent l'be, et alors on ecoute sans savoir attribuer —
	// perdre l'attribution vaut mieux que perdre les traps.
	p4 *ipv4.PacketConn
	p6 *ipv6.PacketConn
	// port : le port DEMANDE. C'est lui que reconcileTraps compare a la consigne, et
	// pas celui obtenu : sinon une consigne a 0 (un test) relierait la socket a
	// chaque seconde.
	port int
	// bound : le port actually obtenu, pour le journal et pour les tests.
	bound int
	// params : notre instance, jamais gosnmp.Default — ce singleton est partage avec
	// le polling du same processus, et le decoding written dedans.
	params *gosnmp.GoSNMP
	handle trapHandler
	// closing : un `Close` fait echouer la lecture en cours, et cette erreur-there n'est
	// pas un incident. Sans le drapeau, tout changement de port would write une ligne
	// d'erreur pour un arret qu'on venait de demander.
	closing atomic.Bool
	// dead : la boucle de lecture a rendu la main sur une erreur qu'on ne lui
	// demandait pas. C'est ce qui empeche la MORT SILENCIEUSE du canal, et il fallait
	// le prevoir en quittant gosnmp : sa boucle, elle, ignorait ses erreurs de lecture
	// et tournait pour toujours. Une socket qui reste enregistree dans le collecteur
	// alors que plus personne ne la lit serait indiscernable d'un parc calme —
	// exactement le mode failure que tout ce fichier combat. reconcileTraps la relie
	// donc comme si la consigne avait changed.
	dead atomic.Bool
}

func (r *trapReceiver) stop() {
	if r == nil {
		return
	}
	r.closing.Store(true)
	_ = r.conn.Close()
}

// dstKnown : cette socket sait-elle dire a quelle adresse un datagramme was
// adresse ? Ce que le proxy annonce a son demarrage, parce que c'est la difference
// entre une preuve attribuee et une preuve qui n'est qu'un pari.
func (r *trapReceiver) dstKnown() bool { return r != nil && (r.p4 != nil || r.p6 != nil) }

// startTrapReceiver lie la socket, reclame au noyau l'adresse de destination des
// datagrams, et demarre la lecture.
func startTrapReceiver(port int, handle trapHandler) (*trapReceiver, error) {
	// Adresse JOKER, comme avant : le proxy ne sait pas laquelle de ses adresses un
	// device va viser — c'est precisely la question a laquelle PKTINFO repond.
	// Une IP nulle rend la same socket double pile que le « 0.0.0.0:162 » d'avant.
	var conn, err = net.ListenUDP("udp", &net.UDPAddr{Port: port})
	if err != nil {
		return nil, err
	}
	var r = &trapReceiver{
		conn: conn, port: port, handle: handle,
		params: &gosnmp.GoSNMP{Version: gosnmp.Version2c, Transport: "udp"},
	}
	var local, ok = conn.LocalAddr().(*net.UDPAddr)
	if ok {
		r.bound = local.Port
	}
	// L'ORDRE suit la FAMILLE de la socket, et il n'est pas interchangeable :
	// IPV6_RECVPKTINFO n'existe que sur une socket AF_INET6 — ce que rend une ecoute
	// joker sur un hote a double pile, et where le noyau rapporte aussi la destination
	// des datagrams IPv4 sous forme mappee — tandis qu'IP_PKTINFO n'existe que sur
	// une AF_INET, le cas d'un hote sans IPv6. On demande donc l'un, puis l'autre, et
	// on se passe des deux si le noyau refuse : un trap dont on ignore la destination
	// reste un trap recu, et le controleur sait dire ce qu'une telle preuve vaut.
	var p6 = ipv6.NewPacketConn(conn)
	if p6.SetControlMessage(ipv6.FlagDst, true) == nil {
		r.p6 = p6
	} else {
		var p4 = ipv4.NewPacketConn(conn)
		if p4.SetControlMessage(ipv4.FlagDst, true) == nil {
			r.p4 = p4
		}
	}
	go r.readLoop()
	return r, nil
}

// trapDstAddr normalise l'adresse de destination rendue par le noyau.
//
// Unmap pour la raison de classifyTrap : une socket double pile rend
// « ::ffff:10.42.0.67 » there where le controleur a written « 10.42.0.67 » dans
// the device, et comparer les textes ferait echouer l'attribution selon l'humeur
// de la pile de l'hote. Une adresse absente ou joker ne dit rien et vaut donc
// « je ne sais pas » — ce qui est un result, pas une valeur par default.
func trapDstAddr(ip net.IP) netip.Addr {
	var addr, ok = netip.AddrFromSlice(ip)
	if !ok {
		return netip.Addr{}
	}
	addr = addr.Unmap()
	if !addr.IsValid() || addr.IsUnspecified() {
		return netip.Addr{}
	}
	return addr
}

// read rend le datagramme ET l'adresse locale a laquelle il was adresse.
func (r *trapReceiver) read(buf []byte) (int, netip.Addr, *net.UDPAddr, error) {
	var n int
	var src net.Addr
	var err error
	var dst net.IP
	switch {
	case r.p6 != nil:
		var cm *ipv6.ControlMessage
		n, cm, src, err = r.p6.ReadFrom(buf)
		if cm != nil {
			dst = cm.Dst
		}
	case r.p4 != nil:
		var cm *ipv4.ControlMessage
		n, cm, src, err = r.p4.ReadFrom(buf)
		if cm != nil {
			dst = cm.Dst
		}
	default:
		var from *net.UDPAddr
		n, from, err = r.conn.ReadFromUDP(buf)
		src = from
	}
	var udp, _ = src.(*net.UDPAddr)
	return n, trapDstAddr(dst), udp, err
}

// readLoop lit, decodes, et rend la main au filtre.
//
// UNE seule goroutine, comme la boucle de gosnmp qu'elle remplace : `onTrap` est
// written pour be appele dans le chemin critique de la socket et n'y bloque pas
// (le poll part dans un minuteur).
func (r *trapReceiver) readLoop() {
	for {
		// Un tampon NEUF par datagramme, comme gosnmp, et pas un tampon reused —
		// l'economie serait un trap. `decodeValue` rend les `OctetString` d'un varbind
		// en TRANCHANT dans le tampon de lecture (`retVal.Value = data[cursor:length]`) :
		// le paquet decode alias donc ce tampon, et le tour de boucle suivant ecraserait
		// silencieusement le contenu d'un paquet encore en cours de traitement. Le filtre
		// actuel n'en lit que des strings already copiees, mais 4 Ko par trap est un prix
		// derisoire pour que ca reste vrai le jour where quelqu'un voudra lire un varbind.
		//
		// 4096 octets, la taille de gosnmp : au-dela le datagramme est tronque, donc
		// rejete au decoding — et un device qui emet des traps de 4 Ko a un autre
		// probleme que sa reactivity.
		var buf = make([]byte, 4096)
		var n, dst, src, err = r.read(buf)
		if err != nil {
			if r.closing.Load() {
				return
			}
			// Une socket UDP liee ne se « casse » pas en cours de route : l'erreur est
			// soit l'arret qu'on vient de demander, soit un incident dont on ne se
			// remet pas en reessayant en boucle serree. On le dit, on se declares morte,
			// et reconcileTraps relie une socket neuve a la seconde suivante — avec son
			// amortissement si le bind fails a son tour.
			slog.Warn("lecture des traps interrompue — la socket va be reliee",
				"err", err, "port", r.bound)
			r.dead.Store(true)
			return
		}
		var pkt, decErr = r.params.UnmarshalTrap(buf[:n], false)
		if decErr != nil {
			// 162/udp receives de tout sur un network de production. Un datagramme
			// indecodable est du bruit, pas un incident : DEBUG, comme une source non
			// surveillee.
			slog.Debug("trap indecodable", "err", decErr, "src", src)
			continue
		}
		r.handle(pkt, src, dst)
		r.answerInform(pkt, src)
	}
}

// answerInform repond a un `InformRequest`, qui est un trap ACQUITTE : sans
// response, l'emetteur retransmet puis abandonne.
//
// Reprise de gosnmp.TrapListener, parce que la perdre serait une regression
// silencieuse. meshcraft ne pose jamais d'inform — `instanton.ApplyTrapReceiver`
// declares un destinataire de traps — mais un device configure a la main peut en
// emettre, et il n'a pas a retransmettre pour rien. Le paquet est renvoye tel quel,
// variables comprises (RFC 3416 § 4.2.4) : c'est ce que l'emetteur attend pour
// solder sa retransmission.
func (r *trapReceiver) answerInform(pkt *gosnmp.SnmpPacket, src *net.UDPAddr) {
	if pkt == nil || src == nil || pkt.PDUType != gosnmp.InformRequest {
		return
	}
	pkt.PDUType = gosnmp.GetResponse
	pkt.Error, pkt.ErrorIndex = gosnmp.NoError, 0
	var out, err = pkt.MarshalMsg()
	if err != nil {
		slog.Debug("response a un inform non serialisable", "err", err, "src", src)
		return
	}
	if _, err = r.conn.WriteToUDP(out, src); err != nil {
		slog.Debug("response a un inform non envoyee", "err", err, "src", src)
	}
}

// onTrap est appelee par la boucle de lecture de gosnmp, donc dans le chemin
// critique de la socket : elle filtre, decide, et rend la main. Le poll part dans
// un minuteur, jamais ici — retenir cette goroutine ferait perdre les traps
// suivants de la rafale.
func (c *SNMPCollector) onTrap(pkt *gosnmp.SnmpPacket, src *net.UDPAddr, dst netip.Addr) {
	c.mu.Lock()
	c.trapStats.received++
	var watches = c.watches
	c.mu.Unlock()

	var srcIP string
	if src != nil {
		srcIP = src.IP.String()
	}
	// L'adresse VISEE, ou rien du tout. Un « je ne sais pas » se transmet en clair :
	// le combler par l'adresse que le proxy croit employer pour joindre the device
	// rendrait la verification du controleur tautologique, puisque c'est precisely
	// cette adresse-there qui est l'hypothese a verify.
	var dstAddr string
	if dst.IsValid() {
		dstAddr = dst.String()
	}
	var oid, label = trapOIDLabel(pkt)
	var w, verdict = classifyTrap(watches, srcIP, pkt.Community)

	c.mu.Lock()
	switch verdict {
	case trapUnknownSource:
		c.trapStats.unknownSource++
	case trapBadCommunity:
		c.trapStats.badCommunity++
	}
	c.mu.Unlock()

	if verdict != trapAccepted {
		var reason = "source non surveillee"
		if verdict == trapBadCommunity {
			reason = "community inattendue"
		}
		slog.Debug("trap ignored", "raison", reason, "src", srcIP, "oid", oid)
		return
	}
	slog.Debug("trap recu", "device", w.DeviceID, "ip", srcIP, "oid", oid, "trap", label,
		"destination", dstAddr)

	// La preuve du chemin retour, au premier trap accepted et a lui seul.
	//
	// Placee ICI, entre le filtre identity et l'amortissement, et c'est le point
	// important : ce qui prouve le chemin, c'est qu'un trap de cet device soit
	// ARRIVE et reconnu, pas qu'il ait obtenu un poll. Un trap absorbe par la
	// fenetre de regroupement (admit rendant false) a traverse le same pare-feu, la
	// same route et la same ACL que celui qui l'a ouverte : le mettre after `admit`
	// ferait dependre la preuve de l'ordonnancement, et un device dont le
	// premier trap tomberait dans une rafale en cours resterait indefiniment
	// « adresse applied, chemin non verified » a the UI.
	//
	// En revanche AVANT le filtre, jamais : n'importe qui sur le chemin pourrait
	// alors fabriquer la preuve d'un device qui, lui, n'a rien emis.
	//
	// L'transmit ne bloque pas le chemin critique de la socket (cf. emitEvent), et
	// se fait hors du verrou de la porte — sinon le canal de sortie du Monitor
	// commanderait la duree de vie d'un verrou pris a chaque trap du parc.
	if c.trapGate.firstSeen(w.DeviceID) {
		c.emitEvent(tunnel.Event{
			Type: tunnel.EventTrapSeen, DeviceID: w.DeviceID,
			// L'IP de la WATCH, pas celle de la socket : c'est l'adresse que le
			// controleur connait, et classifyTrap vient de garantir qu'elles
			// designent le same hote. Recopier la source rendrait « ::ffff:10.0.0.5 »
			// un jour sur deux, selon la pile network de l'hote.
			IP: w.IP,
			// L'adresse LOCALE visee, elle, vient bien de la socket et de nulle part
			// ailleurs : c'est le seul fait de ce paquet que le proxy connaisse et que
			// le controleur ne puisse pas deduire. C'est lui qui rend la preuve
			// attribuable a la destination written (cf. tunnel.Event.DestAddr).
			DestAddr: dstAddr,
			// Pas de Trigger : ce champ dit ce qui a economise l'attente sur une
			// TRANSITION (cf. tunnel.Event.Trigger), et il n'y en a aucune ici. Le
			// type de l'evenement dit already qu'un trap est passe.
			Detail: trapSeenDetail(oid, label),
		})
	}

	var delay, schedule = c.trapGate.admit(w.DeviceID, label, time.Now())
	if !schedule {
		c.mu.Lock()
		c.trapStats.absorbed++
		c.mu.Unlock()
		return
	}
	c.mu.Lock()
	c.trapStats.accepted++
	c.mu.Unlock()

	var deviceID = w.DeviceID
	time.AfterFunc(delay, func() { c.trapPoll(deviceID) })
}

// trapPoll execute le poll de confirmation, SUR LE PROXY.
//
// Aucun aller-retour au controleur : le trap arrive ici, la response est ici, et
// demander la permission a travers le WAN annulerait la seconde qu'on vient de
// gagner. Le controleur receives le RESULT, sous la forme d'un evenement de port
// comme les autres.
func (c *SNMPCollector) trapPoll(deviceID string) {
	var label = c.trapGate.release(deviceID, time.Now())

	c.mu.Lock()
	// La watch est relue MAINTENANT et pas au moment du trap : la fenetre de
	// regroupement laisse le temps a une nouvelle WatchConfig d'arriver, et
	// interroger avec une community stale echouerait sans raison visible.
	var w, found = findWatch(c.watches, deviceID)
	if found {
		// Le trap AVANCE le poll `if_state` : on decale la cadence ordinaire
		// d'autant. Sans ca, le cycle regulier relirait la same table dans la
		// seconde — deux walks pour une information, sur the device qui est
		// justement en train d'avoir un probleme.
		c.last[deviceID+"|state"] = time.Now()
	}
	c.mu.Unlock()
	if !found {
		return // retire de la surveillance entre-temps : rien a confirmer
	}
	c.job(context.Background(), targetOf(w), snmppoll.MetricIfState, triggerTrap, label)()
}

// --- Cycle de vie de l'ecoute ---

// trapSettings lit dans la configuration de surveillance ce qui concerne les
// traps.
//
// Pourquoi aller la chercher, au lieu de la recevoir ? `Monitor.SetConfig` est le
// point ingress de la WatchConfig, mais il ne transmet au collecteur que
// `cfg.SNMP`, et il le fait AVANT to write `m.cfg` — un collecteur qui lirait la
// config depuis SetWatches lirait donc la PRECEDENTE, ce qui est le genre de
// decalage d'une config qu'aucun test ne rattrape. La reconciliation se fait
// ailleurs, depuis la boucle du collecteur : idempotente par construction, elle
// ne relie aucune socket quand rien n'a changed, et au pire une seconde s'ecoule
// avant qu'un changement prenne effet — sur un canal dont le role est justement
// de n'be qu'un accelerateur.
func (m *Monitor) trapSettings() (bool, int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var port = m.cfg.TrapPort
	if port <= 0 {
		port = tunnel.DefaultTrapPort
	}
	return m.cfg.TrapListen, port
}

// trapLoop tient l'ecoute des traps alignee sur la configuration.
//
// Dans sa PROPRE goroutine : lier ou fermer une socket peut prendre quelques
// secondes (gosnmp attend la fin de sa boucle de lecture), et ce delai n'a rien a
// faire dans la boucle qui interroge le parc.
func (c *SNMPCollector) trapLoop(ctx context.Context) {
	var tick = time.NewTicker(time.Second)
	defer tick.Stop()
	defer c.stopTraps()

	var now time.Time
	var lastSummary = time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case now = <-tick.C:
			c.reconcileTraps(now)
			if now.Sub(lastSummary) >= trapSummaryEvery {
				lastSummary = now
				c.logTrapStats()
			}
		}
	}
}

// reconcileTraps aligne la socket sur la consigne. Idempotente : rien ne bouge
// tant que la consigne ne bouge pas — relier une socket a chaque WatchConfig
// recue perdrait des traps a chaque changement de configuration, et ca ne se
// verrait dans aucun journal.
func (c *SNMPCollector) reconcileTraps(now time.Time) {
	var listen, port = monitor.trapSettings()

	c.mu.Lock()
	var cur = c.traps
	if !listen || port != c.trapTried {
		// La consigne a changed : l'amortissement des echecs repart de zero.
		// L'operator qui corrige un port n'a pas a attendre la fin d'un backoff
		// qu'il ne voit pas.
		c.trapRetryAt, c.trapErrSaid, c.trapTried = time.Time{}, false, port
	}
	var retryAt = c.trapRetryAt
	c.mu.Unlock()

	if cur != nil && (!listen || cur.port != port || cur.dead.Load()) {
		slog.Info("ecoute des traps SNMP arretee", "port", cur.port,
			"lecture_morte", cur.dead.Load())
		// Une socket fermee ne se rouvre pas : changer de port — ou remplacer une
		// lecture morte — veut dire en construire une neuve.
		cur.stop()
		c.mu.Lock()
		c.traps = nil
		c.mu.Unlock()
		cur = nil
	}
	if !listen || cur != nil || now.Before(retryAt) {
		return
	}

	var rec, err = startTrapReceiver(port, c.onTrap)
	if err != nil {
		c.mu.Lock()
		c.trapRetryAt = now.Add(trapBindRetry)
		var said = c.trapErrSaid
		c.trapErrSaid = true
		c.mu.Unlock()
		if said {
			slog.Debug("ecoute des traps toujours impossible", "port", port, "err", err)
			return
		}
		// UNE fois, et fort — puis on continue sans. Perdre la reactivity n'est pas
		// perdre la supervision ; faire semblant d'ecouter serait le pire result,
		// parce qu'un operator croirait voir ses ports tomber en une seconde.
		slog.Error("ecoute des traps SNMP impossible — la supervision continue sans "+
			"le chemin rapide, les transitions de port seront views a la cadence de "+
			"polling (port already pris par un autre collecteur ? "+
			"CAP_NET_BIND_SERVICE manquante ?)", "port", port, "err", err)
		return
	}
	c.mu.Lock()
	c.traps = rec
	c.mu.Unlock()
	slog.Info("ecoute des traps SNMP active", "port", port,
		"destination_lisible", rec.dstKnown())
	if !rec.dstKnown() {
		// Une fois par bind, et fort : la supervision reactive fonctionne, mais ce
		// proxy ne pourra pas dire a laquelle de ses adresses un device adresse ses
		// traps. Le controleur enregistrera donc des preuves NON ATTRIBUEES — « les
		// traps arrivent ici », sans pouvoir affirmer que c'est bien l'adresse written
		// dans the device qui les receives. Le taire laisserait croire a une
		// verification qui n'a pas lieu.
		slog.Warn("ce noyau ne rend pas l'adresse de destination des datagrams " +
			"(PKTINFO refused) : les traps sont recus normalement, mais la preuve du " +
			"chemin retour ne pourra pas be attribuee a l'adresse written dans " +
			"the device")
	}
}

func (c *SNMPCollector) stopTraps() {
	c.mu.Lock()
	var cur = c.traps
	c.traps = nil
	c.mu.Unlock()
	cur.stop()
}

func (c *SNMPCollector) logTrapStats() {
	c.mu.Lock()
	var s = c.trapStats
	var listening = c.traps != nil
	c.mu.Unlock()
	if !listening && s.received == 0 {
		return
	}
	slog.Debug("bilan des traps", "recus", s.received, "polls_declenches", s.accepted,
		"absorbes", s.absorbed, "source_inconnue", s.unknownSource,
		"communaute_invalide", s.badCommunity)
}
