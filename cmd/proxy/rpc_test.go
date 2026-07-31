package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"

	"meshcraft/internal/snmppoll"
	"meshcraft/internal/tunnel"
)

// Les questions ponctuelles du contrôleur, éprouvées SANS réseau ni équipement.
//
// Ce qui se teste ici est ce qui se trompe : la borne de lignes (une réponse
// tronquée qui passerait pour complète est l'erreur exacte que ce mécanisme existe
// pour éviter), la traduction des types SNMP, et le format de fil.

// --- Interlocuteur factice ---

// walkerFactice rejoue une liste de PDU, puis une erreur optionnelle. Le walk
// s'arrête net dès que la fonction de rappel rend une erreur, exactement comme
// gosnmp.
type walkerFactice struct {
	pdus []gosnmp.SnmpPDU
	err  error
	// vues : PDU réellement passées à la fonction de rappel. C'est ce qui permet de
	// vérifier que la borne coupe le WALK, et pas seulement la réponse — un walk qui
	// continue en jetant ses lignes coûterait à l'équipement pour rien.
	vues   int
	racine string
	fermé  bool
}

func (w *walkerFactice) BulkWalk(rootOID string, walkFn gosnmp.WalkFunc) error {
	w.racine = rootOID
	var pdu gosnmp.SnmpPDU
	for _, pdu = range w.pdus {
		w.vues++
		var err = walkFn(pdu)
		if err != nil {
			return err
		}
	}
	return w.err
}

func (w *walkerFactice) Close() error {
	w.fermé = true
	return nil
}

// pdusCompteur fabrique n lignes d'une table de compteurs 64 bits.
func pdusCompteur(n int) []gosnmp.SnmpPDU {
	var out = make([]gosnmp.SnmpPDU, 0, n)
	var i int
	for i = 1; i <= n; i++ {
		out = append(out, gosnmp.SnmpPDU{
			Name:  ".1.3.6.1.2.1.31.1.1.1.6." + strconv.Itoa(i),
			Type:  gosnmp.Counter64,
			Value: uint64(i),
		})
	}
	return out
}

// avecWalker installe un interlocuteur factice pour la durée d'un test.
func avecWalker(t *testing.T, w *walkerFactice, err error) {
	t.Helper()
	askDial = func(tunnel.AskRequest) (askWalker, error) { return w, err }
	t.Cleanup(func() { askDial = nil })
}

// --- Bornes ---

func TestAskMaxRowsAppliqueDefautEtPlafond(t *testing.T) {
	type cas struct {
		nom              string
		demandé, attendu int
	}
	var cases = []cas{
		{nom: "absent", demandé: 0, attendu: tunnel.DefaultAskRows},
		{nom: "négatif", demandé: -1, attendu: tunnel.DefaultAskRows},
		{nom: "raisonnable", demandé: 12, attendu: 12},
		{nom: "au plafond", demandé: tunnel.MaxAskRows, attendu: tunnel.MaxAskRows},
		// Une borne qu'on peut relever depuis l'appelant n'est pas une borne.
		{nom: "au-delà du plafond", demandé: 10 * tunnel.MaxAskRows,
			attendu: tunnel.MaxAskRows},
	}
	var k cas
	for _, k = range cases {
		t.Run(k.nom, func(t *testing.T) {
			var got = askMaxRows(k.demandé)
			if got != k.attendu {
				t.Errorf("askMaxRows(%d) = %d, attendu %d", k.demandé, got, k.attendu)
			}
		})
	}
}

func TestAskRowsCoupeEtSignaleLaTroncature(t *testing.T) {
	var w = &walkerFactice{pdus: pdusCompteur(10)}
	var rows, truncated, err = askRows(context.Background(), w, "1.3.6.1.2.1", 4)
	if err != nil {
		t.Fatalf("erreur inattendue : %v", err)
	}
	if len(rows) != 4 {
		t.Fatalf("4 lignes attendues, obtenu %d", len(rows))
	}
	if !truncated {
		t.Error("Truncated doit être positionné : sans lui, une table tronquée " +
			"passerait pour complète")
	}
	// La borne arrête le WALK : on n'a lu qu'une PDU de plus, celle qui a établi
	// qu'il en restait.
	if w.vues != 5 {
		t.Errorf("le walk doit s'arrêter à la 5e PDU, %d lues", w.vues)
	}
	if rows[0].Value != "1" || rows[3].Value != "4" {
		t.Errorf("lignes dans l'ordre du walk attendues, obtenu %+v", rows)
	}
}

func TestAskRowsPasDeTroncaturePileSurLaBorne(t *testing.T) {
	// Un walk qui tombe exactement sur la borne n'est PAS tronqué : la ligne de trop
	// est ce qui prouve qu'il en restait, et il n'y en a pas eu.
	var w = &walkerFactice{pdus: pdusCompteur(4)}
	var rows, truncated, err = askRows(context.Background(), w, "1.3.6.1.2.1", 4)
	if err != nil || len(rows) != 4 || truncated {
		t.Fatalf("4 lignes non tronquées attendues, obtenu %d lignes, tronqué=%v, err=%v",
			len(rows), truncated, err)
	}
}

func TestAskWalkPlafondDurNonContournable(t *testing.T) {
	// Le contrôleur demande dix fois le plafond ; le proxy en rend le plafond.
	var w = &walkerFactice{pdus: pdusCompteur(tunnel.MaxAskRows + 1)}
	avecWalker(t, w, nil)

	var resp = handleAsk(context.Background(), tunnel.AskRequest{
		Ask: tunnel.AskSNMPWalk, IP: "10.99.200.101", OID: "1.3.6.1.2.1",
		MaxRows: 10 * tunnel.MaxAskRows,
	})
	if resp.Err != "" {
		t.Fatalf("aucune erreur attendue, obtenu %q", resp.Err)
	}
	if len(resp.Rows) != tunnel.MaxAskRows || !resp.Truncated {
		t.Errorf("%d lignes tronquées attendues, obtenu %d (tronqué=%v)",
			tunnel.MaxAskRows, len(resp.Rows), resp.Truncated)
	}
	if !w.fermé {
		t.Error("la session SNMP doit être refermée : un proxy qui fuit ses sockets " +
			"finit par ne plus pouvoir interroger personne")
	}
}

func TestAskWalkRendCeQuIlALuAvantUnEchec(t *testing.T) {
	// Un équipement qui répond puis se tait : les lignes déjà lues partent AVEC
	// l'erreur. « Voilà les deux premières lignes et voilà pourquoi ça s'est
	// arrêté » se répare ; une réponse vide, non.
	var w = &walkerFactice{pdus: pdusCompteur(2), err: errors.New("request timeout")}
	avecWalker(t, w, nil)

	var resp = handleAsk(context.Background(), tunnel.AskRequest{
		Ask: tunnel.AskSNMPWalk, IP: "10.99.200.101", OID: "1.3.6.1.2.1",
	})
	if len(resp.Rows) != 2 {
		t.Errorf("2 lignes attendues malgré l'échec, obtenu %d", len(resp.Rows))
	}
	if !strings.Contains(resp.Err, "request timeout") {
		t.Errorf("la cause doit remonter en clair, obtenu %q", resp.Err)
	}
	// Truncated ne parle que de la borne : un walk coupé par une panne n'est pas une
	// réponse bornée, et confondre les deux masquerait la panne.
	if resp.Truncated {
		t.Error("Truncated ne doit pas signaler un échec")
	}
}

func TestAskWalkRespecteLeContexte(t *testing.T) {
	var ctx, cancel = context.WithCancel(context.Background())
	cancel()
	var w = &walkerFactice{pdus: pdusCompteur(10)}
	var rows, truncated, err = askRows(ctx, w, "1.3.6.1.2.1", tunnel.DefaultAskRows)
	if err == nil {
		t.Fatal("un contexte annulé doit arrêter le walk")
	}
	if len(rows) != 0 || truncated {
		t.Errorf("aucune ligne attendue, obtenu %d (tronqué=%v)", len(rows), truncated)
	}
}

// --- Conventions de connexion ---

// TestAskDialHeriteDesConventionsDuPoll : le diagnostic n'a PLUS de conventions de
// connexion à lui.
//
// Ce fichier a recopié celles de snmppoll (v2c, timeout 2 s, un seul réessai,
// communauté vide = public, port 161). La copie n'était pas le danger, son
// invisibilité l'était : un timeout relevé d'un seul côté, et un walk de
// diagnostic décrit un autre équipement qu'un poll ordinaire sans que rien ne le
// signale — on cherche alors la panne dans l'équipement. Le TYPE de la session est
// donc vérifié : elle sort de snmppoll.Dial, et de nulle part ailleurs. Un dialeur
// local réintroduit ici ferait échouer ce test avant d'avoir pu diverger.
func TestAskDialHeriteDesConventionsDuPoll(t *testing.T) {
	// Aucun paquet n'est émis : en UDP, « connecter » une socket ne fait que résoudre
	// la route. Ce test ne parle donc à aucun équipement.
	var w, err = dialAskSNMP(tunnel.AskRequest{IP: "127.0.0.1", Port: 1161})
	if err != nil {
		t.Fatalf("session : %v", err)
	}
	defer w.Close() //nolint:errcheck

	var _, ok = w.(*snmppoll.Session)
	if !ok {
		t.Fatalf("la session du walk doit venir de snmppoll.Dial — unique endroit où "+
			"les conventions de connexion du projet sont écrites (cf. "+
			"snmppoll.TestConventionsDeConnexionUniques) ; obtenu %T", w)
	}
}

// TestAskWalkSurLeFilPorteLesConventionsDuPoll vérifie ces conventions SUR LE FIL,
// depuis ce côté-ci, sans équipement.
//
// Une socket UDP qui ne répond jamais suffit : en v2c, le datagramme porte la
// version et la communauté EN CLAIR — c'est toute la « sécurité » de SNMPv2c — et
// le nombre de datagrammes dit le nombre d'essais. Le test précédent vérifie d'où
// vient la session ; celui-ci vérifie ce qu'elle met réellement sur le réseau, donc
// il tient même si un jour la session est obtenue autrement.
//
// Il dure les ~4 s de la séquence d'essais, et c'est le prix exact de ce qu'il
// mesure : on ne peut pas constater un timeout sans l'attendre.
func TestAskWalkSurLeFilPorteLesConventionsDuPoll(t *testing.T) {
	var conn, err = net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("socket d'écoute : %v", err)
	}
	var local = conn.LocalAddr().(*net.UDPAddr)

	// Un seul lecteur, et la fin de sa vie sert de barrière : les datagrammes ne se
	// lisent qu'après <-fini, sans verrou et sans course.
	var datagrammes [][]byte
	var fini = make(chan struct{})
	go func() {
		defer close(fini)
		var tampon = make([]byte, 4096)
		for {
			var n, _, errLecture = conn.ReadFromUDP(tampon)
			if errLecture != nil {
				return
			}
			var copie = make([]byte, n)
			copy(copie, tampon[:n])
			datagrammes = append(datagrammes, copie)
		}
	}()

	// Communauté ABSENTE dans la requête : c'est le repli qu'on veut voir passer sur
	// le fil, parce que c'est celui qu'on serait tenté de réécrire ailleurs.
	var w, errDial = dialAskSNMP(tunnel.AskRequest{IP: "127.0.0.1", Port: local.Port})
	if errDial != nil {
		_ = conn.Close()
		<-fini
		t.Fatalf("session : %v", errDial)
	}
	var début = time.Now()
	var walkErr = w.BulkWalk("1.3.6.1.2.1.1", func(gosnmp.SnmpPDU) error { return nil })
	var écoulé = time.Since(début)
	_ = w.Close()
	_ = conn.Close()
	<-fini

	if walkErr == nil {
		t.Fatal("un équipement qui ne répond jamais doit rendre une erreur")
	}
	// Retries: 1 — l'essai initial et UN réessai. Un réessai de plus coûterait un
	// cycle de collecte à chaque équipement muet ; un de moins et la moindre perte
	// de datagramme passerait pour une panne.
	if len(datagrammes) != 2 {
		t.Fatalf("2 envois attendus (essai + 1 réessai), obtenu %d", len(datagrammes))
	}
	// « 02 01 01 » : INTEGER version = 1, soit v2c. « 04 06 public » : OCTET STRING
	// de la communauté de repli. Les deux se suivent dans l'en-tête du message, donc
	// une seule recherche les atteste ensemble.
	var entête = []byte("\x02\x01\x01\x04\x06public")
	if !bytes.Contains(datagrammes[0], entête) {
		t.Errorf("v2c + communauté « public » attendues sur le fil, datagramme : %x",
			datagrammes[0])
	}
	// Timeout: 2 s par essai, donc ~4 s pour la séquence. La fenêtre est large à
	// dessein — ce qui doit échouer, c'est un timeout devenu franchement autre :
	// trop court, un équipement lent passerait pour muet ; trop long, un équipement
	// muet retiendrait un cycle de collecte.
	if écoulé < 3*time.Second || écoulé > 8*time.Second {
		t.Errorf("séquence d'essais de ~4 s attendue (2 s × 2), obtenu %v", écoulé)
	}
}

// --- Question inconnue ---

func TestAskQuestionInconnue(t *testing.T) {
	// Un proxy plus ancien que son contrôleur est un cas NORMAL : il doit le dire.
	var resp = handleAsk(context.Background(), tunnel.AskRequest{
		Ask: "arp_table", IP: "10.99.200.101",
	})
	if resp.Err == "" {
		t.Fatal("une question inconnue doit rendre une Err, pas un silence")
	}
	if !strings.Contains(resp.Err, "arp_table") || !strings.Contains(resp.Err, "inconnue") {
		t.Errorf("Err doit nommer la question inconnue, obtenu %q", resp.Err)
	}
	if resp.Rows != nil || resp.SourceAddr != "" || resp.Truncated {
		t.Errorf("aucune donnée ne doit accompagner un refus : %+v", resp)
	}

	// Un champ vide (contrôleur qui bafouille) suit le même chemin.
	if handleAsk(context.Background(), tunnel.AskRequest{}).Err == "" {
		t.Error("une question vide doit rendre une Err")
	}

	// Le nom vient du fil : il est borné et échappé avant d'atteindre un journal.
	var long = handleAsk(context.Background(),
		tunnel.AskRequest{Ask: strings.Repeat("x", 500) + "\n\x1b[2J"})
	if len(long.Err) > 200 || strings.ContainsAny(long.Err, "\n\x1b") {
		t.Errorf("Err doit rester bornée et échappée, obtenu %d octets : %q",
			len(long.Err), long.Err)
	}
}

// --- Traduction des types et des valeurs ---

func TestAskTraductionDesTypes(t *testing.T) {
	type cas struct {
		nom         string
		pdu         gosnmp.SnmpPDU
		typ, valeur string
	}
	var cases = []cas{
		{nom: "libellé de port", typ: "OctetString", valeur: "GigabitEthernet1/0/1",
			pdu: gosnmp.SnmpPDU{Type: gosnmp.OctetString,
				Value: []byte("GigabitEthernet1/0/1")}},
		// Le cas qui justifie le rendu en TEXTE : ce compteur ne survit pas à un
		// float64, et c'est précisément sur les compteurs qu'on interroge.
		{nom: "compteur 64 bits au maximum", typ: "Counter64",
			valeur: "18446744073709551615",
			pdu: gosnmp.SnmpPDU{Type: gosnmp.Counter64,
				Value: uint64(18446744073709551615)}},
		{nom: "compteur 64 bits au-delà de 2^53", typ: "Counter64",
			valeur: "9007199254740993",
			pdu:    gosnmp.SnmpPDU{Type: gosnmp.Counter64, Value: uint64(9007199254740993)}},
		{nom: "état de port", typ: "Integer", valeur: "1",
			pdu: gosnmp.SnmpPDU{Type: gosnmp.Integer, Value: 1}},
		{nom: "jauge", typ: "Gauge32", valeur: "1000",
			pdu: gosnmp.SnmpPDU{Type: gosnmp.Gauge32, Value: uint(1000)}},
		{nom: "uptime", typ: "TimeTicks", valeur: "4294967295",
			pdu: gosnmp.SnmpPDU{Type: gosnmp.TimeTicks, Value: uint32(4294967295)}},
		{nom: "compteur 32 bits", typ: "Counter32", valeur: "17",
			pdu: gosnmp.SnmpPDU{Type: gosnmp.Counter32, Value: uint(17)}},
		// gosnmp préfixe les OID d'un point, le contrôleur non : une seule notation
		// sort d'ici, sinon rien ne se compare.
		{nom: "identifiant d'objet", typ: "ObjectIdentifier",
			valeur: "1.3.6.1.4.1.11.2.3.7.11.253",
			pdu: gosnmp.SnmpPDU{Type: gosnmp.ObjectIdentifier,
				Value: ".1.3.6.1.4.1.11.2.3.7.11.253"}},
		{nom: "adresse", typ: "IPAddress", valeur: "10.99.200.101",
			pdu: gosnmp.SnmpPDU{Type: gosnmp.IPAddress, Value: "10.99.200.101"}},
		// Les trois exceptions de SNMPv2 sont des réponses, pas des valeurs : toute
		// l'information est dans le type, et inventer un « 0 » se lirait comme une
		// mesure.
		{nom: "objet absent", typ: "NoSuchObject", valeur: "",
			pdu: gosnmp.SnmpPDU{Type: gosnmp.NoSuchObject, Value: nil}},
		{nom: "instance absente", typ: "NoSuchInstance", valeur: "",
			pdu: gosnmp.SnmpPDU{Type: gosnmp.NoSuchInstance, Value: nil}},
		{nom: "fin de vue", typ: "EndOfMibView", valeur: "",
			pdu: gosnmp.SnmpPDU{Type: gosnmp.EndOfMibView, Value: nil}},
		{nom: "nul", typ: "Null", valeur: "",
			pdu: gosnmp.SnmpPDU{Type: gosnmp.Null, Value: nil}},
		{nom: "flottant opaque", typ: "OpaqueFloat", valeur: "36.75",
			pdu: gosnmp.SnmpPDU{Type: gosnmp.OpaqueFloat, Value: float32(36.75)}},
		{nom: "double opaque", typ: "OpaqueDouble", valeur: "-3.5",
			pdu: gosnmp.SnmpPDU{Type: gosnmp.OpaqueDouble, Value: float64(-3.5)}},
		// Un type qu'on ne connaît pas rend son TAG, pas un libellé faux : un
		// exploitant qui lit « Type0x2b » sait qu'il doit aller voir la MIB, là où un
		// libellé inventé l'enverrait dans le mur.
		{nom: "type inattendu", typ: "Type0x2b", valeur: "peu importe",
			pdu: gosnmp.SnmpPDU{Type: gosnmp.Asn1BER(0x2b), Value: "peu importe"}},
	}
	var k cas
	for _, k = range cases {
		t.Run(k.nom, func(t *testing.T) {
			k.pdu.Name = ".1.3.6.1.2.1.1.1.0"
			var row = askRow(k.pdu)
			if row.OID != "1.3.6.1.2.1.1.1.0" {
				t.Errorf("OID sans point de tête attendu, obtenu %q", row.OID)
			}
			if row.Type != k.typ {
				t.Errorf("type %q attendu, obtenu %q", k.typ, row.Type)
			}
			if row.Value != k.valeur {
				t.Errorf("valeur %q attendue, obtenue %q", k.valeur, row.Value)
			}
		})
	}
}

func TestAskOctetStringBinaire(t *testing.T) {
	// Une adresse MAC : six octets bruts. json.Marshal remplacerait silencieusement
	// tout octet non UTF-8 par U+FFFD, ce qui la rendrait illisible POUR TOUJOURS
	// sans que rien ne signale la substitution. D'où l'hexadécimal préfixé.
	var mac = gosnmp.SnmpPDU{
		Name:  ".1.3.6.1.2.1.2.2.1.6.1",
		Type:  gosnmp.OctetString,
		Value: []byte{0x00, 0x1b, 0x2f, 0xff, 0x00, 0x0a},
	}
	var row = askRow(mac)
	if row.Value != "0x001b2fff000a" {
		t.Errorf("hexadécimal préfixé attendu, obtenu %q", row.Value)
	}

	// Le préfixe n'est pas décoratif : sans lui, « 001b2f » se confondrait avec un
	// libellé de port qui s'appellerait vraiment comme ça.
	if !strings.HasPrefix(row.Value, "0x") {
		t.Error("le rendu binaire doit être reconnaissable")
	}

	// Et la valeur rendue traverse le JSON sans altération, ce qui est le but.
	var data, err = json.Marshal(row)
	if err != nil {
		t.Fatalf("sérialisation : %v", err)
	}
	if !strings.Contains(string(data), "0x001b2fff000a") {
		t.Errorf("valeur altérée par le JSON : %s", data)
	}

	// Un texte multiligne (sysDescr de la plupart des constructeurs) reste du
	// TEXTE : json.Marshal l'échappe correctement, et l'hexadécimaliser le rendrait
	// illisible pour rien.
	var descr = askRow(gosnmp.SnmpPDU{Type: gosnmp.OctetString,
		Value: []byte("Instant On 1930\r\nfirmware 2.7.0")})
	if descr.Value != "Instant On 1930\r\nfirmware 2.7.0" {
		t.Errorf("texte multiligne conservé attendu, obtenu %q", descr.Value)
	}
	// De l'UTF-8 valide accentué aussi : ce n'est pas parce que ce n'est pas de
	// l'ASCII que c'est du binaire.
	var accent = askRow(gosnmp.SnmpPDU{Type: gosnmp.OctetString,
		Value: []byte("liaison secours — étage 2")})
	if accent.Value != "liaison secours — étage 2" {
		t.Errorf("UTF-8 conservé attendu, obtenu %q", accent.Value)
	}
	// Un octet 0x80 isolé n'est pas de l'UTF-8 : hexadécimal, y compris quand le
	// reste de la chaîne est parfaitement lisible — une valeur à moitié traduite
	// serait pire qu'illisible.
	var invalide = askRow(gosnmp.SnmpPDU{Type: gosnmp.OctetString,
		Value: []byte{'p', 'o', 'r', 't', 0x80}})
	if invalide.Value != "0x706f727480" {
		t.Errorf("UTF-8 invalide → hexadécimal, obtenu %q", invalide.Value)
	}
	// Chaîne vide : vide, et non « 0x ».
	if askRow(gosnmp.SnmpPDU{Type: gosnmp.OctetString, Value: []byte{}}).Value != "" {
		t.Error("un OctetString vide rend une valeur vide")
	}
}

// --- Validation des entrées ---

func TestAskOIDEtIPInvalides(t *testing.T) {
	var w = &walkerFactice{pdus: pdusCompteur(3)}
	avecWalker(t, w, nil)

	type cas struct {
		nom, ip, oid string
	}
	var cases = []cas{
		{nom: "oid manquant", ip: "10.99.200.101", oid: ""},
		{nom: "oid non numérique", ip: "10.99.200.101", oid: "1.3.6.sysDescr"},
		{nom: "oid à segment vide", ip: "10.99.200.101", oid: "1.3..6"},
		{nom: "adresse manquante", ip: "", oid: "1.3.6.1"},
		{nom: "nom au lieu d'une adresse", ip: "switch-labo", oid: "1.3.6.1"},
	}
	var k cas
	for _, k = range cases {
		t.Run(k.nom, func(t *testing.T) {
			var resp = handleAsk(context.Background(), tunnel.AskRequest{
				Ask: tunnel.AskSNMPWalk, IP: k.ip, OID: k.oid,
			})
			if resp.Err == "" {
				t.Fatalf("une Err explicite est attendue, obtenu %+v", resp)
			}
			if len(resp.Rows) != 0 {
				t.Errorf("aucune ligne ne doit accompagner un refus : %+v", resp.Rows)
			}
		})
	}
	if w.vues != 0 {
		t.Errorf("une requête invalide ne doit rien demander à l'équipement, %d PDU lues",
			w.vues)
	}

	// Le point de tête est accepté des deux notations, et une seule sort.
	var oid, err = askOID(".1.3.6.1.2.1")
	if err != nil || oid != "1.3.6.1.2.1" {
		t.Errorf("askOID(\".1.3.6.1.2.1\") = %q, %v", oid, err)
	}
}

// --- AskSourceAddr ---

func TestAskSourceAddrRendUneAdresseSansMasque(t *testing.T) {
	// 127.0.0.1 suffit à éprouver la méthode sans rien savoir du réseau de la
	// machine de test : la boucle locale est routée partout.
	var resp = handleAsk(context.Background(), tunnel.AskRequest{
		Ask: tunnel.AskSourceAddr, IP: "127.0.0.1",
	})
	if resp.Err != "" {
		t.Fatalf("aucune erreur attendue, obtenu %q", resp.Err)
	}
	if resp.SourceAddr != "127.0.0.1" {
		t.Errorf("adresse source 127.0.0.1 attendue, obtenue %q", resp.SourceAddr)
	}
	// Sans masque, sans port, sans zone : le contrôleur écrit cette chaîne telle
	// quelle dans une destination de traps.
	if strings.ContainsAny(resp.SourceAddr, "/:%") {
		t.Errorf("adresse nue attendue, obtenue %q", resp.SourceAddr)
	}
	if net.ParseIP(resp.SourceAddr) == nil {
		t.Errorf("%q n'est pas une adresse", resp.SourceAddr)
	}
	// Aucune ligne de walk ne doit accompagner cette réponse.
	if resp.Rows != nil {
		t.Errorf("réponse polluée : %+v", resp.Rows)
	}
}

func TestAskSourceAddrRefuseCeQuiNEstPasUneAdresse(t *testing.T) {
	var resp = handleAsk(context.Background(), tunnel.AskRequest{
		Ask: tunnel.AskSourceAddr, IP: "10.99.200.101/24",
	})
	if resp.Err == "" || resp.SourceAddr != "" {
		t.Fatalf("un refus explicite est attendu, obtenu %+v", resp)
	}
}

// --- Format de fil ---

// TestAskEchangeComplet verrouille le motif : préambule, UNE requête, UNE réponse,
// puis fermeture. Sur une paire de connexions en mémoire, donc sans yamux ni
// WebSocket : ce qui est éprouvé ici est le format, pas le transport.
func TestAskEchangeComplet(t *testing.T) {
	var client, serveur = net.Pipe()
	defer client.Close() //nolint:errcheck

	go tunnel.ServeAskStream(serveur, handleAsk)

	var err = json.NewEncoder(client).Encode(tunnel.DialRequest{Op: tunnel.OpAsk})
	if err != nil {
		t.Fatalf("préambule : %v", err)
	}
	err = json.NewEncoder(client).Encode(tunnel.AskRequest{
		Ask: tunnel.AskSourceAddr, IP: "127.0.0.1",
	})
	if err != nil {
		t.Fatalf("requête : %v", err)
	}

	_ = client.SetReadDeadline(time.Now().Add(10 * time.Second))
	var br = bufio.NewReader(client)
	var ligne string
	ligne, err = br.ReadString('\n')
	if err != nil {
		t.Fatalf("réponse : %v", err)
	}
	var resp tunnel.AskResponse
	if err = json.Unmarshal([]byte(ligne), &resp); err != nil {
		t.Fatalf("réponse illisible %q : %v", ligne, err)
	}
	if resp.SourceAddr != "127.0.0.1" || resp.Err != "" {
		t.Errorf("réponse inattendue : %+v", resp)
	}
	// Les champs sans objet ne sont pas sur le fil : une réponse de source_addr ne
	// porte ni « rows » ni « truncated ».
	if strings.Contains(ligne, "rows") || strings.Contains(ligne, "truncated") {
		t.Errorf("réponse polluée : %s", ligne)
	}
	// Une question, une réponse, le stream se ferme : rien après la ligne.
	var reste []byte
	reste, err = br.ReadBytes('\n')
	if err == nil || len(reste) != 0 {
		t.Errorf("le stream doit se fermer après la réponse, obtenu %q / %v", reste, err)
	}
}

func TestAskEchangeWalkSurLeFil(t *testing.T) {
	var w = &walkerFactice{pdus: pdusCompteur(3)}
	avecWalker(t, w, nil)

	var client, serveur = net.Pipe()
	defer client.Close() //nolint:errcheck
	go tunnel.ServeAskStream(serveur, handleAsk)

	var enc = json.NewEncoder(client)
	var err = enc.Encode(tunnel.DialRequest{Op: tunnel.OpAsk})
	if err == nil {
		err = enc.Encode(tunnel.AskRequest{
			Ask: tunnel.AskSNMPWalk, IP: "10.99.200.101",
			OID: "1.3.6.1.2.1.31.1.1.1.6", Community: "secret-de-labo", MaxRows: 2,
		})
	}
	if err != nil {
		t.Fatalf("émission : %v", err)
	}

	_ = client.SetReadDeadline(time.Now().Add(10 * time.Second))
	var ligne string
	ligne, err = bufio.NewReader(client).ReadString('\n')
	if err != nil {
		t.Fatalf("réponse : %v", err)
	}
	var resp tunnel.AskResponse
	if err = json.Unmarshal([]byte(ligne), &resp); err != nil {
		t.Fatalf("réponse illisible %q : %v", ligne, err)
	}
	if len(resp.Rows) != 2 || !resp.Truncated {
		t.Fatalf("2 lignes tronquées attendues, obtenu %+v", resp)
	}
	if resp.Rows[0].OID != "1.3.6.1.2.1.31.1.1.1.6.1" || resp.Rows[0].Type != "Counter64" {
		t.Errorf("ligne inattendue : %+v", resp.Rows[0])
	}
	if w.racine != "1.3.6.1.2.1.31.1.1.1.6" {
		t.Errorf("racine walkée %q", w.racine)
	}
	// La communauté est un secret d'opération : elle n'a rien à faire dans une
	// réponse, pas plus que dans un journal.
	if strings.Contains(ligne, "secret-de-labo") {
		t.Errorf("la communauté est repartie dans la réponse : %s", ligne)
	}
}

// TestAskAttenteBornée : le contrôleur propose l'attente, le proxy tranche. Une
// borne qu'un appelant peut relever n'est pas une borne.
func TestAskAttenteBornée(t *testing.T) {
	var vue = make(chan time.Duration, 1)
	var handler = func(ctx context.Context, req tunnel.AskRequest) tunnel.AskResponse {
		var deadline, ok = ctx.Deadline()
		if !ok {
			vue <- 0
			return tunnel.AskResponse{Err: "sans deadline"}
		}
		vue <- time.Until(deadline)
		return tunnel.AskResponse{SourceAddr: "127.0.0.1"}
	}

	type cas struct {
		nom       string
		timeoutMS int
		attendue  time.Duration
	}
	var cases = []cas{
		{nom: "absent", timeoutMS: 0, attendue: tunnel.DefaultAskWait},
		{nom: "raisonnable", timeoutMS: 3000, attendue: 3 * time.Second},
		{nom: "au-delà du plafond", timeoutMS: 600_000, attendue: tunnel.MaxAskWait},
	}
	var k cas
	for _, k = range cases {
		t.Run(k.nom, func(t *testing.T) {
			var client, serveur = net.Pipe()
			defer client.Close() //nolint:errcheck
			go tunnel.ServeAskStream(serveur, handler)

			var enc = json.NewEncoder(client)
			var err = enc.Encode(tunnel.DialRequest{Op: tunnel.OpAsk})
			if err == nil {
				err = enc.Encode(tunnel.AskRequest{
					Ask: tunnel.AskSourceAddr, IP: "127.0.0.1", TimeoutMS: k.timeoutMS,
				})
			}
			if err != nil {
				t.Fatalf("émission : %v", err)
			}
			_ = client.SetReadDeadline(time.Now().Add(10 * time.Second))
			if _, err = bufio.NewReader(client).ReadString('\n'); err != nil {
				t.Fatalf("réponse : %v", err)
			}

			var restante = <-vue
			// Marge d'une seconde : le temps de traversée du tuyau n'est pas nul.
			if restante > k.attendue || restante < k.attendue-time.Second {
				t.Errorf("attente ≈ %v attendue, obtenu %v", k.attendue, restante)
			}
		})
	}
}

// TestAskSansRepondeur : un proxy dont le répondeur n'est pas câblé le DIT. Un
// stream qui se ferme en silence ne se distingue pas d'un tunnel qui tombe.
func TestAskSansRepondeur(t *testing.T) {
	var client, serveur = net.Pipe()
	defer client.Close() //nolint:errcheck
	go tunnel.ServeAskStream(serveur, nil)

	var enc = json.NewEncoder(client)
	var err = enc.Encode(tunnel.DialRequest{Op: tunnel.OpAsk})
	if err == nil {
		err = enc.Encode(tunnel.AskRequest{Ask: tunnel.AskSourceAddr, IP: "127.0.0.1"})
	}
	if err != nil {
		t.Fatalf("émission : %v", err)
	}
	_ = client.SetReadDeadline(time.Now().Add(10 * time.Second))
	var ligne string
	ligne, err = bufio.NewReader(client).ReadString('\n')
	if err != nil {
		t.Fatalf("réponse : %v", err)
	}
	var resp tunnel.AskResponse
	if err = json.Unmarshal([]byte(ligne), &resp); err != nil {
		t.Fatalf("réponse illisible %q : %v", ligne, err)
	}
	if resp.Err == "" {
		t.Error("une Err est attendue quand aucun répondeur n'est câblé")
	}
}
