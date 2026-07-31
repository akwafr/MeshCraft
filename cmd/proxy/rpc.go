package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/netip"
	"strconv"
	"strings"

	"github.com/gosnmp/gosnmp"

	"meshcraft/internal/snmppoll"
	"meshcraft/internal/tunnel"
)

// Repondre aux questions ponctuelles du controleur : la half PROXY de
// tunnel.OpAsk.
//
// Les deux questions du contrat n'ont qu'une raison d'be commune, et elle
// justifie a elle seule le mecanisme : ces faits ne sont connaissables QUE d'ici.
// SNMP est de l'UDP et le tunnel ne relaie que du TCP — il n'y a rien a relayer ;
// et la table de routage qui choisit une adresse source est celle de CE noyau, pas
// une deduction que le controleur pourrait faire depuis un inventaire d'adresses.
//
// Trois rules gouvernent ce fichier.
//
//  1. LECTURE SEULE, definitivement. Il n'existe pas de question write dans
//     le contrat et il n'en existera pas : la configuration passe par les
//     connecteurs, qui ont leurs garde-fous (plan, dry-run, relecture). Rien ici
//     n'est prepare « pour plus tard » — une extension write demanderait
//     d'abord de refaire ce raisonnement.
//  2. RIEN DE SECRET DANS LES JOURNAUX. AskRequest.Community est un secret
//     d'operation, comme pour les connecteurs : il ne sort jamais dans un
//     slog, pas same en DEBUG, et pas plus dans le texte d'une erreur — le champ
//     Err d'une response remonte jusqu'a the UI d'un exploitant et jusqu'aux
//     journaux du controleur.
//  3. TOUT EST BORNE, et par le proxy. Le nombre de lignes comme l'attente : la
//     question vient d'ailleurs, la memory est ici.
//
// A noter, sur ce que ce fichier NE verifies pas : l'allowlist de dial
// (MESHCRAFT_DIAL_ALLOW) ne s'applique pas au walk. Elle borne le RELAI TCP, seul
// chemin par lequel le controleur fait circuler du trafic arbitraire ; une lecture
// SNMP part exactement comme un poll ordinaire ou un balayage de plage, qui ne la
// consultent pas non plus. Deux rules differentes pour joindre le same
// device finiraient par autoriser sa supervision tout en refusant son
// diagnostic.

// handleAsk aiguille une question. C'est la fonction cablee dans le tunnel
// (cf. cmd/proxy/tunnel.go).
//
// Une question INCONNUE rend une Err explicite, jamais un silence : un proxy plus
// ancien que son controleur est un cas normal dans un parc qu'on met a jour
// progressivement — c'est same la raison d'be du champ Ask. L'exploitant doit
// lire « ce proxy ne connait pas cette question » et non « pas de response », qui
// se confond avec un tunnel coupe.
func handleAsk(ctx context.Context, req tunnel.AskRequest) tunnel.AskResponse {
	switch req.Ask {
	case tunnel.AskSNMPWalk:
		return askSNMPWalk(ctx, req)
	case tunnel.AskSourceAddr:
		return askSourceAddr(req)
	}
	return tunnel.AskResponse{
		Err: "question inconnue de ce proxy : " + quotedAsk(req.Ask) +
			" (proxy plus ancien que le controleur ?)",
	}
}

// quotedAsk rend le nom de la question tel qu'on peut le journaliser et
// l'afficher : entre guillemets, borne, echappe.
//
// Le nom vient DU FIL. Le recopier tel quel dans une Err mettrait du texte
// arbitraire — mille caracteres, des retours a la ligne, des sequences
// d'echappement de terminal — dans un journal et dans une interface.
func quotedAsk(ask string) string {
	if ask == "" {
		return "(vide)"
	}
	const maxLen = 40
	if len(ask) > maxLen {
		ask = ask[:maxLen] + "…"
	}
	return strconv.Quote(ask)
}

// --- AskSNMPWalk ---

// askWalker : le strict necessary pour walker. Isole derriere une interface pour
// la same raison que snmppoll.Client — la partie qui se trompe est la TRADUCTION
// des PDU, et elle doit se tester sans device ni network.
//
// *snmppoll.Session le satisfait tel quel (cf. dialAskSNMP) : l'interface describes un
// besoin, elle ne describes pas une implementation locale.
type askWalker interface {
	BulkWalk(rootOID string, walkFn gosnmp.WalkFunc) error
	Close() error
}

// askDial est remplacable par les tests. Nil = SNMP reel.
var askDial func(tunnel.AskRequest) (askWalker, error)

// errAskTruncated arrete le walk sur la borne de lignes. Sentinelle INTERNE :
// atteindre une borne voulue n'est pas un failure, et elle ne sort jamais d'ici.
var errAskTruncated = errors.New("borne de lignes atteinte")

// askSNMPWalk walke req.OID sur req.IP et rend les lignes lues.
func askSNMPWalk(ctx context.Context, req tunnel.AskRequest) tunnel.AskResponse {
	var ip, err = askIP(req.IP)
	if err != nil {
		return tunnel.AskResponse{Err: err.Error()}
	}
	var oid string
	oid, err = askOID(req.OID)
	if err != nil {
		return tunnel.AskResponse{Err: err.Error()}
	}

	var dial = askDial
	if dial == nil {
		dial = dialAskSNMP
	}
	// La session part sur l'adresse VALIDEE, pas sur la string brute : ce qui a been
	// verified et ce qui est joint doivent be la same chose, sans quoi le control
	// ne control rien.
	req.IP = ip
	var w askWalker
	w, err = dial(req)
	if err != nil {
		return tunnel.AskResponse{Err: "session SNMP vers " + ip + " impossible : " + err.Error()}
	}
	defer w.Close() //nolint:errcheck

	var rows, truncated, walkErr = askRows(ctx, w, oid, askMaxRows(req.MaxRows))
	var resp = tunnel.AskResponse{Rows: rows, Truncated: truncated}
	if walkErr != nil {
		// Les lignes ALREADY lues partent avec l'erreur, elles ne sont pas jetees : sur
		// un device qui repond puis se tait, « voila les 40 premieres lignes et
		// voila pourquoi ca s'est stopped » vaut infiniment mieux que rien du tout.
		resp.Err = "walk interrompu after " + strconv.Itoa(len(rows)) +
			" ligne(s) : " + walkErr.Error()
	}
	// Jamais la community — ni ici ni ailleurs (cf. l'en-tete de ce fichier).
	slog.Debug("question snmp_walk", "ip", ip, "oid", oid,
		"lignes", len(rows), "tronque", truncated)
	return resp
}

// askRows execute le walk et rend les lignes, le drapeau de troncature et
// l'erreur eventuelle.
func askRows(ctx context.Context, w askWalker, oid string,
	maxRows int) ([]tunnel.AskRow, bool, error) {
	var rows []tunnel.AskRow
	var truncated bool
	var err = w.BulkWalk(oid, func(pdu gosnmp.SnmpPDU) error {
		// Le ctx est verified DANS la fonction de walk : gosnmp n'en prend pas, et
		// c'est le seul point de reprise entre deux PDU (same motif que le cote
		// metrique, snmppoll.pollClient.BulkWalk). Sans ca, l'attente accordee par le
		// controleur ne serait qu'un vœu.
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if len(rows) >= maxRows {
			// La ligne DE TROP n'est pas gardee : elle sert seulement a establish qu'il
			// en restait. Truncated dit donc exactement « il y avait plus », jamais
			// « peut-be » — un walk qui tombe pile sur la borne n'est pas flagged
			// tronque, parce qu'il ne l'est pas.
			truncated = true
			return errAskTruncated
		}
		rows = append(rows, askRow(pdu))
		return nil
	})
	if errors.Is(err, errAskTruncated) {
		err = nil
	}
	// Truncated ne parle QUE de la borne MaxRows, jamais d'un walk coupe par un
	// timeout ou par un device muet : ces deux-there se disent dans Err, avec leur
	// cause. Melanger les deux ferait passer une panne pour une response bornee.
	return rows, truncated, err
}

// askMaxRows applique la borne du contrat : default et PLAFOND DUR.
//
// Le plafond ne se observes pas depuis la request. Un walk sous `1.3.6.1` sur un
// switch rend des dizaines de milliers de lignes : la memory qui les tient est
// celle du proxy, qui tourne sur une VM minimale ou un Raspberry Pi.
func askMaxRows(want int) int {
	if want <= 0 {
		return tunnel.DefaultAskRows
	}
	if want > tunnel.MaxAskRows {
		return tunnel.MaxAskRows
	}
	return want
}

// askRow traduit une PDU en ligne de response.
func askRow(pdu gosnmp.SnmpPDU) tunnel.AskRow {
	return tunnel.AskRow{
		// gosnmp prefix les OID d'un point ; le controleur, lui, en envoie sans.
		// Rendre les deux formes selon la source obligerait l'appelant a normaliser
		// avant toute comparaison, et il ne le ferait pas partout.
		OID:   strings.TrimPrefix(pdu.Name, "."),
		Type:  pduTypeLabel(pdu.Type),
		Value: pduValueText(pdu),
	}
}

// pduTypeLabel nomme un type ASN.1 comme une MIB le nomme.
//
// Un exploitant doit pouvoir comprendre ce qu'il lit : « Counter64 » dit qu'une
// valeur s'interprete en delta et peut boucler, « Gauge32 » qu'elle se lit telle
// quelle, « TimeTicks » qu'elle compte des hundredths de seconde. Le number de tag
// ne dit rien de tout ca. Un type inconnu rend son tag en hexadecimal rather
// qu'un label faux.
func pduTypeLabel(t gosnmp.Asn1BER) string {
	switch t {
	case gosnmp.Boolean:
		return "Boolean"
	case gosnmp.Integer:
		return "Integer"
	case gosnmp.BitString:
		return "BitString"
	case gosnmp.OctetString:
		return "OctetString"
	case gosnmp.Null:
		return "Null"
	case gosnmp.ObjectIdentifier:
		return "ObjectIdentifier"
	case gosnmp.ObjectDescription:
		return "ObjectDescription"
	case gosnmp.IPAddress:
		return "IPAddress"
	case gosnmp.Counter32:
		return "Counter32"
	case gosnmp.Gauge32:
		return "Gauge32"
	case gosnmp.TimeTicks:
		return "TimeTicks"
	case gosnmp.Opaque:
		return "Opaque"
	case gosnmp.NsapAddress:
		return "NsapAddress"
	case gosnmp.Counter64:
		return "Counter64"
	case gosnmp.Uinteger32:
		return "Uinteger32"
	case gosnmp.OpaqueFloat:
		return "OpaqueFloat"
	case gosnmp.OpaqueDouble:
		return "OpaqueDouble"
	// Les trois exceptions de SNMPv2 ne sont pas des valeurs mais des REPONSES :
	// « cet objet n'existe pas », « cette instance n'existe pas », « fin de vue ».
	// Les rendre telles quelles est le cœur du diagnostic — c'est exactement ce
	// qu'on cherche a savoir en demandant un walk.
	case gosnmp.NoSuchObject:
		return "NoSuchObject"
	case gosnmp.NoSuchInstance:
		return "NoSuchInstance"
	case gosnmp.EndOfMibView:
		return "EndOfMibView"
	}
	return "Type0x" + strconv.FormatUint(uint64(t), 16)
}

// pduValueText rend la valeur EN TEXTE.
//
// Cette conversion reste ICI et ne rejoint PAS snmppoll, dont la session est
// now partagee (cf. dialAskSNMP). Ce n'est pas un oubli : snmppoll.toFloat et
// snmppoll.asString repondent a un autre besoin et doivent y rester. Elles
// produisent des metriques — un float64, et le REFUS de ce qui n'en est pas un,
// parce qu'un compteur converti en zero se lirait comme un lien inactif. Le
// diagnostic, lui, doit rendre TOUT ce que the device a dit, exactement : un
// Counter64 au-dela de 2⁵³ ne survit pas au float64, six octets d'adresse MAC ne
// survivent pas a json.Marshal, et « NoSuchObject » est precisely la response
// qu'on cherchait — there where la metrique doit l'ignorer.
//
// Deux besoins, donc deux conversions, et c'est la CONNEXION qu'il fallait unifier :
// une convention de connexion written deux fois fait decrire deux devices
// differents en silence ; deux conversions assumees, chacune flagged par l'autre,
// ne trompent personne.
//
// C'est la decision du contrat, et elle est structurante : un compteur de 64 bits
// ne passe pas dans un float sans perte, et c'est precisely sur les compteurs
// qu'on interroge. `ifHCInOctets` d'un lien charge depasse 2^53 au bout de
// quelques mois, et un JSON qui l'aurait arrondi ne se voit pas — on lirait un
// rate faux sans jamais savoir pourquoi. Hence le passage par big.Int
// (gosnmp.ToBigInt) pour les entiers : exact, quel que soit le type Go que la
// bibliotheque a choisi.
func pduValueText(pdu gosnmp.SnmpPDU) string {
	switch pdu.Type {
	case gosnmp.Boolean, gosnmp.Integer, gosnmp.Counter32, gosnmp.Gauge32,
		gosnmp.TimeTicks, gosnmp.Counter64, gosnmp.Uinteger32:
		return gosnmp.ToBigInt(pdu.Value).String()
	case gosnmp.OpaqueFloat:
		var f, ok = pdu.Value.(float32)
		if ok {
			return strconv.FormatFloat(float64(f), 'g', -1, 32)
		}
	case gosnmp.OpaqueDouble:
		var f, ok = pdu.Value.(float64)
		if ok {
			return strconv.FormatFloat(f, 'g', -1, 64)
		}
	case gosnmp.ObjectIdentifier:
		var s, ok = pdu.Value.(string)
		if ok {
			return strings.TrimPrefix(s, ".")
		}
	case gosnmp.Null, gosnmp.NoSuchObject, gosnmp.NoSuchInstance, gosnmp.EndOfMibView:
		// Rien a rendre : l'information est ENTIRE dans le type. Fabriquer un
		// « null » ou un « 0 » textuel se lirait comme une valeur.
		return ""
	}
	// OctetString, IPAddress, NsapAddress, Opaque, et tout type inattendu :
	// gosnmp rend soit des octets, soit une string already mise en forme.
	if pdu.Value == nil {
		return ""
	}
	var raw, isBytes = pdu.Value.([]byte)
	if isBytes {
		return octetStringText(raw)
	}
	var text, isText = pdu.Value.(string)
	if isText {
		return octetStringText([]byte(text))
	}
	return gosnmp.ToBigInt(pdu.Value).String()
}

// octetStringText rend un OctetString sans jamais corrompre la response.
//
// L'implementation a MIGRE dans `snmppoll.OctetText`, et ce qui reste ici est la
// delegation. Le commentaire d'origine expliquait pourquoi cette conversion ne
// rejoignait pas `snmppoll` — parce que `toFloat` et `asString` repondent a un autre
// besoin, la metrique, where un compteur converti en zero se lirait comme un lien
// inactif. Ce raisonnement reste juste et ces deux fonctions y restent separees.
//
// Ce qui a changed, c'est qu'un SECOND appelant est apparu dans `snmppoll` lui-same :
// la collecte du voisinage LLDP a exactement le same besoin de fidelite que le
// diagnostic, et sur les memes octets — un `lldpRemChassisId` de six octets bruts. La
// recopier aurait fait deux implementations d'une same rule, dont la divergence
// n'aurait produit AUCUNE erreur : un chassis vu par le diagnostic simplement
// incomparable avec le same chassis vu par la collecte. Le nom local est kept parce
// que c'est celui que `pduValueText` lit, et parce que les tests de ce paquet
// verify already la rule sur des octets reels.
func octetStringText(b []byte) string {
	return snmppoll.OctetText(b)
}

// askOID valide la root a walker.
//
// La string vient du fil et part vers une bibliotheque network : on verifies
// nous-memes qu'elle a la forme d'un OID, pour rendre « oid invalide » rather
// qu'une erreur d'encodage de gosnmp que personne ne saura interpreter. Un OID
// vide n'est pas remplace par une root par default : walker `1.3` parce que la
// request was incomplete produirait des dizaines de milliers de lignes que
// personne n'a demandees.
func askOID(oid string) (string, error) {
	// Le point de tete est accepted et retire : les deux notations circulent (gosnmp
	// rend « .1.3.6… », les MIB s'write « 1.3.6… ») et refuser l'une des deux
	// serait un trap gratuit.
	var clean = strings.TrimPrefix(strings.TrimSpace(oid), ".")
	if clean == "" {
		return "", errors.New("oid manquant : il n'y a pas de root par default a walker")
	}
	var seg string
	for _, seg = range strings.Split(clean, ".") {
		// Un segment est un entier non signe sur 32 bits, et sa notation est UNIQUE :
		// « 1.00.3 » designe le same objet que « 1.0.3 » mais ne se compare pas au
		// same texte, donc on le refuse au lieu de le laisser semer la confusion.
		var _, err = strconv.ParseUint(seg, 10, 32)
		if err != nil || (len(seg) > 1 && seg[0] == '0') {
			return "", errors.New("oid invalide : " + strconv.Quote(clean))
		}
	}
	return clean, nil
}

// askIP valide the device vise.
//
// Une ADRESSE, jamais un nom : le contrat en fait une adresse (le proxy ne connait
// pas les identifiants du controleur), et laisser passer un nom ferait resoudre du
// DNS sur une string arbitraire venue du fil. Unmap ramene une forme « ::ffff:… »
// a sa notation v4, pour que le texte rendu soit comparable a celui de
// l'inventaire.
func askIP(ip string) (string, error) {
	var addr, err = netip.ParseAddr(strings.TrimSpace(ip))
	if err != nil {
		return "", errors.New("adresse d'device invalide : " + strconv.Quote(ip))
	}
	return addr.Unmap().String(), nil
}

// dialAskSNMP ouvre la session SNMP d'une question.
//
// AUCUNE convention de connexion ici, et c'est tout l'objet de cette fonction :
// snmppoll.Dial est le SEUL endroit where le projet written comment on joint un
// device (v2c, timeout 2 s, un seul reessai, community vide = public,
// port 161). Une question de diagnostic et un poll de supervision doivent decrire
// le SAME device : si le walk manuel reussissait avec des parameters plus
// larges que le poll, on conclurait que the device expose des OID que la
// supervision « perd », alors que c'est la question qui aurait triche.
//
// Ces cinq valeurs ont been recopiees ici pendant un lot. La copie n'was pas le
// danger — son INVISIBILITE l'was : rien n'aurait flagged qu'un timeout observed
// d'un seul cote faisait decrire deux devices differents, et on aurait cherche
// la panne dans the device.
//
// *snmppoll.Session satisfait askWalker sans adaptateur : sa method BulkWalk a la
// signature de gosnmp, celle qui passe les PDU BRUTES dans l'ordre du walk — le
// diagnostic doit rendre ce qu'il a lu. snmppoll.Client, l'autre vue de la same
// session, agrege par index d'interface pour la metrique : ce n'est pas ce qu'il
// faut ici, une table rendue a un exploitant ne se deduplique pas.
func dialAskSNMP(req tunnel.AskRequest) (askWalker, error) {
	// DeviceID reste vide : le proxy ne connait pas les identifiants du controleur
	// (cf. tunnel.AskRequest.IP) et Dial n'en a pas l'usage — il n'tag rien.
	var s, err = snmppoll.Dial(snmppoll.Target{
		IP:        req.IP,
		Port:      req.Port,
		Community: req.Community,
	})
	if err != nil {
		// nil EXPLICITE, et non « return s, err » : un *Session nil range dans une
		// interface donne un askWalker non nil, sur lequel un « if w != nil » futur se
		// tromperait. La forme actuelle interdit ce trap au lieu de compter dessus.
		return nil, err
	}
	return s, nil
}

// --- AskSourceAddr ---

// askSourceAddr rend l'adresse locale que le noyau EMPLOIERAIT pour joindre req.IP.
//
// La method evidente — chercher, parmi les adresses declarees du proxy, celle du
// sous-network de the device — est FAUSSE from que le proxy y accede par routage.
// Releve sur l'installation reelle : le proxy expose 10.42.0.67/24 et
// 172.17.0.1/16, the device de labo est en 10.99.200.101 — aucune
// correspondance, et pourtant il l'atteint. L'algorithme evident ne rendrait rien
// du tout, et un operator en conclurait que le proxy ne voit pas the device.
//
// Seule la table de routage sait. On l'interroge en « connectant » une socket UDP
// vers la cible : aucun paquet n'est emis (UDP n'establishes rien), le noyau se
// contente de resoudre la route pour choisir l'adresse source, et LocalAddr la dit.
// Le port ne joue donc aucun role — on met 161 pour que le journal reste lisible.
func askSourceAddr(req tunnel.AskRequest) tunnel.AskResponse {
	var ip, err = askIP(req.IP)
	if err != nil {
		return tunnel.AskResponse{Err: err.Error()}
	}
	var c net.Conn
	c, err = net.Dial("udp", net.JoinHostPort(ip, "161"))
	if err != nil {
		// Pas de route : le DIRE. Rendre une adresse vide passerait pour un success et
		// on poserait une destination de traps que the device n'atteindrait jamais.
		return tunnel.AskResponse{
			Err: "aucune route vers " + ip + " depuis ce proxy : " + err.Error(),
		}
	}
	defer c.Close() //nolint:errcheck

	var local, ok = c.LocalAddr().(*net.UDPAddr)
	if !ok || local.IP == nil {
		return tunnel.AskResponse{Err: "adresse locale illisible pour " + ip}
	}
	var addr netip.Addr
	addr, ok = netip.AddrFromSlice(local.IP)
	if !ok {
		return tunnel.AskResponse{Err: "adresse locale illisible pour " + ip}
	}
	// Unmap : une socket en dual-stack rend « ::ffff:10.42.0.67 » selon l'humeur de
	// la pile de l'hote, et le controleur compare cette response a des adresses
	// d'inventaire written en v4 (same precaution que classifyTrap).
	addr = addr.Unmap()
	if !addr.IsValid() || addr.IsUnspecified() {
		return tunnel.AskResponse{
			Err: "le noyau n'a choisi aucune adresse source pour " + ip,
		}
	}
	slog.Debug("question source_addr", "ip", ip, "source", addr.String())
	// String() : sans masque, sans port, et sans zone — c'est ce que le controleur
	// will write tel quel dans une destination de traps.
	return tunnel.AskResponse{SourceAddr: addr.String()}
}
