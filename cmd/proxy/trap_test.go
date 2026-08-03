package main

import (
	"encoding/json"
	"net"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"

	"meshcraft/internal/metricbuf"
	"meshcraft/internal/snmppoll"
	"meshcraft/internal/tunnel"
)

// OID des tables lues par snmppoll. Recopies a la main, et c'est le but : ce
// test est le CONTRAT entre le poller et la detection de transitions. Si
// snmppoll renomme sa metrique ou son tag `ifindex`, c'est ici que ca
// casse — pas en production, six mois plus tard, sous la forme d'evenements de
// port qui ne partent plus.
const (
	tOidIfDescr      = "1.3.6.1.2.1.2.2.1.2"
	tOidIfType       = "1.3.6.1.2.1.2.2.1.3"
	tOidIfOperStatus = "1.3.6.1.2.1.2.2.1.8"
)

func TestObservedPortsSuitLeContratDeSnmppoll(t *testing.T) {
	var tables = map[string]map[string]gosnmp.SnmpPDU{
		tOidIfDescr: {
			"1": {Type: gosnmp.OctetString, Value: []byte("GE1")},
			"2": {Type: gosnmp.OctetString, Value: []byte("GE2")},
		},
		tOidIfType: {
			"1": {Type: gosnmp.Integer, Value: snmppoll.IfTypeEthernet},
			"2": {Type: gosnmp.Integer, Value: snmppoll.IfTypeEthernet},
		},
		tOidIfOperStatus: {
			"1": {Type: gosnmp.Integer, Value: ifOperUp},
			"2": {Type: gosnmp.Integer, Value: ifOperDown},
		},
	}
	var got = observedPorts(snmppoll.Samples("sw1", tables, time.Now(), 0))
	if len(got) != 2 {
		t.Fatalf("2 ports attendus, obtenu %d : %+v", len(got), got)
	}
	if got[1].status != ifOperUp || got[1].name != "GE1" {
		t.Errorf("port 1 : %+v", got[1])
	}
	if got[2].status != ifOperDown || got[2].name != "GE2" {
		t.Errorf("port 2 : %+v", got[2])
	}
}

// operSample fabrique l'sample que snmppoll rendrait pour un port.
func operSample(ifindex, port string, status float64) metricbuf.Sample {
	var labels = map[string]string{"device": "sw1", "ifindex": ifindex}
	if port != "" {
		labels["port"] = port
	}
	return metricbuf.Sample{Name: metricIfOperStatus, Labels: labels, Value: status}
}

func TestObservedPortsIgnoreCeQuiNIdentifiePas(t *testing.T) {
	var samples = []metricbuf.Sample{
		operSample("3", "", ifOperUp), // sans nom : l'index suffit a identifier
		{Name: "meshcraft_if_speed_mbps",
			Labels: map[string]string{"device": "sw1", "ifindex": "3"}, Value: 1000},
		{Name: metricIfOperStatus, Labels: map[string]string{"device": "sw1"}, Value: 1},
		{Name: metricIfOperStatus,
			Labels: map[string]string{"device": "sw1", "ifindex": "TRK1"}, Value: 1},
	}
	var got = observedPorts(samples)
	if len(got) != 1 {
		t.Fatalf("un seul port exploitable expected, obtenu %+v", got)
	}
	if got[3].status != ifOperUp || got[3].name != "" {
		t.Errorf("port 3 : %+v", got[3])
	}
}

func TestDiffPorts(t *testing.T) {
	type cas struct {
		nom       string
		prev, cur map[int]portObs
		expected  []portChange
	}
	var cases = []cas{{
		nom: "first observation : muette",
		cur: map[int]portObs{1: {status: ifOperUp}, 2: {status: ifOperDown}},
	}, {
		nom:      "up vers down",
		prev:     map[int]portObs{1: {status: ifOperUp, name: "GE1"}},
		cur:      map[int]portObs{1: {status: ifOperDown, name: "GE1"}},
		expected: []portChange{{ifIndex: 1, name: "GE1", up: false, status: ifOperDown}},
	}, {
		nom:      "down vers up",
		prev:     map[int]portObs{1: {status: ifOperDown, name: "GE1"}},
		cur:      map[int]portObs{1: {status: ifOperUp, name: "GE1"}},
		expected: []portChange{{ifIndex: 1, name: "GE1", up: true, status: ifOperUp}},
	}, {
		nom:  "port disparu de la table : muet",
		prev: map[int]portObs{1: {status: ifOperUp}, 2: {status: ifOperUp}},
		cur:  map[int]portObs{1: {status: ifOperUp}},
	}, {
		nom:      "valeur exotique : down, avec la valeur conservee",
		prev:     map[int]portObs{5: {status: ifOperUp}},
		cur:      map[int]portObs{5: {status: 3}},
		expected: []portChange{{ifIndex: 5, up: false, status: 3}},
	}, {
		nom:  "down vers lowerLayerDown : same etat, donc muet",
		prev: map[int]portObs{5: {status: ifOperDown}},
		cur:  map[int]portObs{5: {status: ifOperLowerLayerDown}},
	}, {
		nom:  "renommage sans changement state : muet",
		prev: map[int]portObs{1: {status: ifOperUp, name: "GE1"}},
		cur:  map[int]portObs{1: {status: ifOperUp, name: "uplink"}},
	}, {
		nom:  "notPresent vers down : same etat, donc muet",
		prev: map[int]portObs{9: {status: 6}},
		cur:  map[int]portObs{9: {status: ifOperDown}},
	}, {
		nom:  "rafale ordonnee par index",
		prev: map[int]portObs{1: {status: ifOperUp}, 2: {status: ifOperUp}, 3: {status: ifOperUp}},
		cur:  map[int]portObs{1: {status: ifOperDown}, 2: {status: ifOperDown}, 3: {status: ifOperDown}},
		expected: []portChange{
			{ifIndex: 1, status: ifOperDown}, {ifIndex: 2, status: ifOperDown},
			{ifIndex: 3, status: ifOperDown},
		},
	}}

	var k cas
	for _, k = range cases {
		t.Run(k.nom, func(t *testing.T) {
			var got = diffPorts(k.prev, k.cur)
			if len(got) != len(k.expected) {
				t.Fatalf("%d transitions attendues, obtenu %d : %+v",
					len(k.expected), len(got), got)
			}
			var i int
			for i = range got {
				if got[i] != k.expected[i] {
					t.Errorf("transition %d : obtenu %+v, expected %+v",
						i, got[i], k.expected[i])
				}
			}
		})
	}
}

func TestPortDetail(t *testing.T) {
	if portDetail(portChange{status: ifOperDown}, "") != "" {
		t.Error("un down ordinaire sans trap n'a rien a preciser")
	}
	if portDetail(portChange{status: ifOperUp}, "linkUp") != "linkUp" {
		t.Error("le label du trap doit accompagner l'evenement")
	}
	var got = portDetail(portChange{status: 5}, "linkDown")
	if got != "ifOperStatus=5 (dormant) ; linkDown" {
		t.Errorf("detail compose inattendu : %q", got)
	}
}

// --- Le collecteur, de bout en bout, sans socket ni device ---

type collecte struct{ evs []tunnel.Event }

func (r *collecte) note(ev tunnel.Event) { r.evs = append(r.evs, ev) }

func nouveauCollecteur(t *testing.T) (*SNMPCollector, *collecte) {
	t.Helper()
	var c = NewSNMPCollector("http://exemple.invalid", "px1", "s3cr3t")
	var rec = &collecte{}
	c.SetEmitter(rec.note)
	c.SetWatches([]tunnel.SNMPWatch{
		{DeviceID: "sw1", IP: "10.0.0.5", Community: "lecture", StateIntervalS: 30},
	})
	return c, rec
}

func TestNotePortStatesSuiteDeCycles(t *testing.T) {
	var c, rec = nouveauCollecteur(t)

	// 1er cycle : deux ports up. Aucune transition — on n'annonce pas un
	// changement au premier passage.
	c.notePortStates("sw1", []metricbuf.Sample{
		operSample("1", "GE1", ifOperUp), operSample("2", "GE2", ifOperUp),
	}, triggerPoll, "")
	if len(rec.evs) != 0 {
		t.Fatalf("first observation muette attendue, obtenu %+v", rec.evs)
	}

	// 2e cycle : le port 2 tombe, sur un poll avance par un linkDown.
	c.notePortStates("sw1", []metricbuf.Sample{
		operSample("1", "GE1", ifOperUp), operSample("2", "GE2", ifOperDown),
	}, triggerTrap, "linkDown")
	if len(rec.evs) != 1 {
		t.Fatalf("une transition attendue, obtenu %+v", rec.evs)
	}
	var ev = rec.evs[0]
	if ev.Type != tunnel.EventPortDown || ev.IfIndex != 2 || ev.PortName != "GE2" {
		t.Errorf("evenement inattendu : %+v", ev)
	}
	if ev.DeviceID != "sw1" || ev.IP != "10.0.0.5" {
		t.Errorf("identity de the device perdue : %+v", ev)
	}
	if ev.Trigger != triggerTrap || ev.Detail != "linkDown" {
		t.Errorf("declencheur ou detail inattendu : %+v", ev)
	}

	// 3e cycle : lecture partielle (aucun ifOperStatus). L'observation previous
	// est conservee, sinon le cycle suivant serait une « first observation » et
	// tairait la remontee du port.
	rec.evs = nil
	c.notePortStates("sw1", []metricbuf.Sample{{
		Name:   "meshcraft_if_speed_mbps",
		Labels: map[string]string{"device": "sw1", "ifindex": "2"}, Value: 1000,
	}}, triggerPoll, "")
	if len(rec.evs) != 0 {
		t.Fatalf("lecture partielle : aucun evenement expected, obtenu %+v", rec.evs)
	}

	// 4e cycle : le port 2 remonte. C'est there que la conservation ci-dessus se voit.
	c.notePortStates("sw1", []metricbuf.Sample{
		operSample("1", "GE1", ifOperUp), operSample("2", "GE2", ifOperUp),
	}, triggerPoll, "")
	if len(rec.evs) != 1 || rec.evs[0].Type != tunnel.EventPortUp ||
		rec.evs[0].IfIndex != 2 {
		t.Fatalf("remontee du port 2 attendue, obtenu %+v", rec.evs)
	}
	if rec.evs[0].Trigger != triggerPoll {
		t.Errorf("declencheur expected %q : %+v", triggerPoll, rec.evs[0])
	}

	// 5e cycle : le port 2 quitte la table (membre de pile retire). Muet.
	rec.evs = nil
	c.notePortStates("sw1", []metricbuf.Sample{
		operSample("1", "GE1", ifOperUp),
	}, triggerPoll, "")
	if len(rec.evs) != 0 {
		t.Fatalf("port disparu : aucun evenement expected, obtenu %+v", rec.evs)
	}

	// 6e cycle : valeur exotique sur le port 1 — down, avec la valeur en clair.
	c.notePortStates("sw1", []metricbuf.Sample{
		operSample("1", "GE1", 4),
	}, triggerPoll, "")
	if len(rec.evs) != 1 || rec.evs[0].Type != tunnel.EventPortDown {
		t.Fatalf("valeur exotique : un down expected, obtenu %+v", rec.evs)
	}
	if rec.evs[0].Detail != "ifOperStatus=4 (unknown)" {
		t.Errorf("detail expected avec la valeur brute : %+v", rec.evs[0])
	}
}

func TestSetWatchesOublieLesEquipementsRetires(t *testing.T) {
	var c, rec = nouveauCollecteur(t)
	c.notePortStates("sw1", []metricbuf.Sample{operSample("1", "GE1", ifOperUp)},
		triggerPoll, "")
	if len(c.portStates) != 1 {
		t.Fatalf("etat de port expected pour sw1, obtenu %+v", c.portStates)
	}

	c.SetWatches(nil)
	if len(c.portStates) != 0 {
		t.Fatalf("sw1 retire de la surveillance doit sortir de la carte : %+v",
			c.portStates)
	}
	if len(rec.evs) != 0 {
		t.Errorf("un retrait de surveillance n'est pas une panne : %+v", rec.evs)
	}

	// Reintegrated : on repart d'une first observation, donc muet, same si le port
	// est maintenant down.
	c.SetWatches([]tunnel.SNMPWatch{{DeviceID: "sw1", IP: "10.0.0.5"}})
	c.notePortStates("sw1", []metricbuf.Sample{operSample("1", "GE1", ifOperDown)},
		triggerPoll, "")
	if len(rec.evs) != 0 {
		t.Errorf("reintegration : first observation muette attendue, obtenu %+v",
			rec.evs)
	}
}

// --- Filtre identity ---

func TestClassifyTrap(t *testing.T) {
	var watches = []tunnel.SNMPWatch{
		{DeviceID: "sw1", IP: "10.0.0.5", Community: "lecture"},
		{DeviceID: "sw2", IP: "10.0.0.6"}, // sans community : « public » expected
		{DeviceID: "bogus", IP: "pas-une-ip"},
	}
	type cas struct {
		nom, ip, community string
		device             string
		expected           trapVerdict
	}
	var cases = []cas{
		{nom: "bonne paire", ip: "10.0.0.5", community: "lecture",
			device: "sw1", expected: trapAccepted},
		{nom: "community vide vaut public", ip: "10.0.0.6", community: "public",
			device: "sw2", expected: trapAccepted},
		{nom: "IP en forme mappee IPv6", ip: "::ffff:10.0.0.5", community: "lecture",
			device: "sw1", expected: trapAccepted},
		{nom: "IP inconnue", ip: "192.168.1.1", community: "lecture",
			expected: trapUnknownSource},
		{nom: "mauvaise community", ip: "10.0.0.5", community: "public",
			expected: trapBadCommunity},
		{nom: "community absente (v3)", ip: "10.0.0.5", community: "",
			expected: trapBadCommunity},
		{nom: "community absente sur watch sans community", ip: "10.0.0.6",
			community: "", expected: trapBadCommunity},
		{nom: "source illisible", ip: "", community: "lecture",
			expected: trapUnknownSource},
	}
	var k cas
	for _, k = range cases {
		t.Run(k.nom, func(t *testing.T) {
			var w, verdict = classifyTrap(watches, k.ip, k.community)
			if verdict != k.expected {
				t.Fatalf("verdict %v expected, obtenu %v", k.expected, verdict)
			}
			if w.DeviceID != k.device {
				t.Errorf("device %q expected, obtenu %q", k.device, w.DeviceID)
			}
		})
	}
}

func TestTrapOIDLabel(t *testing.T) {
	var paquet = func(oid string) *gosnmp.SnmpPacket {
		return &gosnmp.SnmpPacket{Variables: []gosnmp.SnmpPDU{
			{Name: ".1.3.6.1.2.1.1.3.0", Type: gosnmp.TimeTicks, Value: uint32(42)},
			{Name: ".1.3.6.1.6.3.1.1.4.1.0", Type: gosnmp.ObjectIdentifier, Value: oid},
		}}
	}
	var oid, label = trapOIDLabel(paquet(".1.3.6.1.6.3.1.1.5.3"))
	if oid != oidLinkDown || label != "linkDown" {
		t.Errorf("linkDown expected, obtenu %q / %q", oid, label)
	}
	oid, label = trapOIDLabel(paquet("1.3.6.1.6.3.1.1.5.4"))
	if oid != oidLinkUp || label != "linkUp" {
		t.Errorf("linkUp expected, obtenu %q / %q", oid, label)
	}
	// Un trap constructeur reste journalisable sans be named : on ne construit
	// pas de table de traps par gamme.
	oid, label = trapOIDLabel(paquet("1.3.6.1.4.1.11.2.14.11.5.1.7.1.29.1"))
	if oid == "" || label != "" {
		t.Errorf("trap constructeur : OID conserve, label vide ; obtenu %q / %q",
			oid, label)
	}
	// Un trap v1 ne porte pas snmpTrapOID.0 : rien a en dire, et ce n'est pas une
	// erreur.
	oid, label = trapOIDLabel(&gosnmp.SnmpPacket{})
	if oid != "" || label != "" {
		t.Errorf("paquet sans snmpTrapOID : %q / %q", oid, label)
	}
}

// --- Amortissement ---

var refTime = time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)

func TestTrapGateAbsorbeUneRafale(t *testing.T) {
	var g = newTrapGate()

	// Le premier trap ouvre la fenetre.
	var delay, schedule = g.admit("sw1", "", refTime)
	if !schedule || delay != tunnel.TrapCoalesce {
		t.Fatalf("un poll expected dans %v, obtenu %v / %v",
			tunnel.TrapCoalesce, delay, schedule)
	}

	// Les 95 suivants — un 48 ports qui redemarre — sont absorbes par lui.
	var i int
	for i = 0; i < 95; i++ {
		var d, s = g.admit("sw1", "", refTime.Add(time.Duration(i)*time.Millisecond))
		if s {
			t.Fatalf("trap %d : un second poll programme dans %v", i, d)
		}
	}

	// Un autre device n'est pas amorti par celui-there : l'amortissement est PAR
	// device, sinon un switch bavard masquerait la panne du voisin.
	var _, autre = g.admit("sw2", "", refTime)
	if !autre {
		t.Error("sw2 doit obtenir son propre poll")
	}

	// La rafale a adopte le premier label exploitable, pas le dernier vide.
	g.admit("sw1", "linkDown", refTime.Add(10*time.Millisecond))
	if g.release("sw1", refTime.Add(tunnel.TrapCoalesce)) != "linkDown" {
		t.Error("le label de la rafale doit remonter au poll")
	}
	if len(g.pending) != 1 || !g.pending["sw2"] {
		t.Errorf("la fenetre de sw1 doit be refermee : %+v", g.pending)
	}
}

func TestTrapGateRespecteLePlancher(t *testing.T) {
	var g = newTrapGate()
	g.admit("sw1", "", refTime)
	var poll = refTime.Add(tunnel.TrapCoalesce)
	g.release("sw1", poll)

	// Un trap juste after le poll : RETARDE au plancher, pas jete — l'abandonner
	// perdrait une transition reelle jusqu'au prochain cycle ordinaire.
	var arrivee = poll.Add(10 * time.Millisecond)
	var delay, schedule = g.admit("sw1", "", arrivee)
	if !schedule {
		t.Fatal("le trap doit be programme, pas abandonne")
	}
	var expected = poll.Add(tunnel.TrapMinPollGap).Sub(arrivee)
	if delay != expected {
		t.Fatalf("delai %v expected (plancher), obtenu %v", expected, delay)
	}
	if delay < tunnel.TrapCoalesce {
		t.Fatalf("le plancher ne peut pas raccourcir la fenetre : %v", delay)
	}

	// Bien after le plancher, on retombe sur la coalescence simple.
	g.release("sw1", arrivee.Add(delay))
	var tard = arrivee.Add(delay).Add(10 * tunnel.TrapMinPollGap)
	delay, schedule = g.admit("sw1", "", tard)
	if !schedule || delay != tunnel.TrapCoalesce {
		t.Fatalf("coalescence simple attendue, obtenu %v / %v", delay, schedule)
	}
}

func TestTrapGateOublieLesEquipementsRetires(t *testing.T) {
	var g = newTrapGate()
	g.admit("sw1", "linkUp", refTime)
	g.admit("sw2", "", refTime)
	g.release("sw2", refTime)
	g.firstSeen("sw1")
	g.firstSeen("sw2")

	g.forget(map[string]bool{"sw2": true})
	if len(g.pending) != 0 || len(g.label) != 0 {
		t.Errorf("sw1 doit be oublie : %+v / %+v", g.pending, g.label)
	}
	if len(g.lastPoll) != 1 {
		t.Errorf("sw2 reste surveille, son plancher doit tenir : %+v", g.lastPoll)
	}
	// La preuve du chemin retour suit la same rule : elle se rearme pour sw1, elle
	// tient pour sw2 — sinon un device toujours surveille re-prouverait son
	// chemin a chaque WatchConfig recue.
	if len(g.seen) != 1 || !g.seen["sw2"] {
		t.Errorf("preuve : sw1 rearme, sw2 conserve ; obtenu %+v", g.seen)
	}
	if !g.firstSeen("sw1") {
		t.Error("sw1 reintegrated doit pouvoir re-prouver son chemin retour")
	}
	if g.firstSeen("sw2") {
		t.Error("sw2 n'a pas quitte la surveillance : rien a re-prouver")
	}
}

// --- Preuve du chemin retour (tunnel.EventTrapSeen) ---

// paquetTrap fabrique un trap v2c tel que gosnmp le rend a onTrap : la community
// au niveau du paquet, l'OID du trap dans `snmpTrapOID.0`.
func paquetTrap(oid, community string) *gosnmp.SnmpPacket {
	return &gosnmp.SnmpPacket{
		Version: gosnmp.Version2c, Community: community,
		Variables: []gosnmp.SnmpPDU{
			{Name: ".1.3.6.1.2.1.1.3.0", Type: gosnmp.TimeTicks, Value: uint32(7)},
			{Name: ".1.3.6.1.6.3.1.1.4.1.0", Type: gosnmp.ObjectIdentifier, Value: oid},
		},
	}
}

func sourceUDP(ip string) *net.UDPAddr {
	return &net.UDPAddr{IP: net.ParseIP(ip), Port: 41234}
}

// viseeLocale : l'adresse locale a laquelle le datagramme was adresse, telle que
// le noyau la rend a la lecture. C'est celle du proxy de labo, celle-there same que le
// controleur written dans les devices — le controleur seul sait la comparer.
var viseeLocale = netip.MustParseAddr("10.42.0.67")

// desamorce coupe le poll qu'un trap accepted a programme, sans attendre ses 250 ms.
//
// trapPoll relit la liste de surveillance au moment de partir et renonce quand la
// cible n'y est plus (c'est ce qui le rend sur face a une WatchConfig qui change
// pendant la fenetre). Vider la liste suffit donc a garantir qu'aucun de ces tests
// n'ouvre de socket vers une IP de documentation after sa propre fin. A appeler
// AFTER les verifications : la purge rearme la preuve.
func desamorce(c *SNMPCollector) { c.SetWatches(nil) }

func TestOnTrapProuveLeCheminRetourUneSeuleFois(t *testing.T) {
	var c, rec = nouveauCollecteur(t)
	defer desamorce(c)

	c.onTrap(paquetTrap(oidLinkDown, "lecture"), sourceUDP("10.0.0.5"), viseeLocale)
	if len(rec.evs) != 1 {
		t.Fatalf("un evenement de preuve expected, obtenu %+v", rec.evs)
	}
	var ev = rec.evs[0]
	if ev.Type != tunnel.EventTrapSeen {
		t.Errorf("type %q expected, obtenu %+v", tunnel.EventTrapSeen, ev)
	}
	if ev.DeviceID != "sw1" || ev.IP != "10.0.0.5" {
		t.Errorf("la preuve doit nommer the device et son adresse : %+v", ev)
	}
	if ev.Detail != "linkDown" {
		t.Errorf("detail expected %q, obtenu %q", "linkDown", ev.Detail)
	}

	// Deuxieme trap de la same rafale : ABSORBE par la fenetre de regroupement,
	// donc sans poll — et sans preuve non plus, puisqu'elle est already acquise. Ce cas
	// est le plus important des deux : il verifies que le point transmit depend du
	// filtre identity et pas de l'ordonnancement.
	c.onTrap(paquetTrap(oidLinkUp, "lecture"), sourceUDP("10.0.0.5"), viseeLocale)
	if len(rec.evs) != 1 {
		t.Fatalf("la preuve ne se repete pas, obtenu %+v", rec.evs)
	}
	c.mu.Lock()
	var stats = c.trapStats
	c.mu.Unlock()
	if stats.received != 2 || stats.accepted != 1 || stats.absorbed != 1 {
		t.Errorf("le second trap devait be absorbe, pas recompte : %+v", stats)
	}
}

// TestOnTrapProuveDepuisUnTrapAbsorbe : le tout premier trap d'un device peut
// tomber dans une fenetre already ouverte — un poll par trap ayant been programme pour
// lui a la milliseconde previous. Il a pourtant traverse le same chemin, et sans
// ca the device resterait « adresse applied, chemin non verified » a the UI jusqu'a
// sa prochaine rafale.
func TestOnTrapProuveDepuisUnTrapAbsorbe(t *testing.T) {
	var c, rec = nouveauCollecteur(t)

	// Fenetre ouverte a la main : aucun minuteur ne part, donc rien a desamorcer.
	var _, ouverte = c.trapGate.admit("sw1", "linkDown", time.Now())
	if !ouverte {
		t.Fatal("la fenetre devait s'ouvrir")
	}

	c.onTrap(paquetTrap(oidLinkUp, "lecture"), sourceUDP("10.0.0.5"), viseeLocale)
	if len(rec.evs) != 1 || rec.evs[0].Type != tunnel.EventTrapSeen {
		t.Fatalf("un trap absorbe prouve le chemin autant qu'un autre : %+v", rec.evs)
	}
	if rec.evs[0].Detail == "" {
		t.Error("un Detail vide se lirait comme un champ perdu en route")
	}
}

// TestOnTrapNeProuveRienDepuisUneSourceNonAutorisee : la preuve ne doit pas pouvoir
// be FABRIQUEE. Un trap v2c est de l'UDP non authentifie : si un tiers sur le
// chemin pouvait faire dire au controleur « le chemin retour de sw1 est established »,
// l'evenement ne prouverait plus rien du tout.
func TestOnTrapNeProuveRienDepuisUneSourceNonAutorisee(t *testing.T) {
	type cas struct {
		nom, ip, community string
	}
	var cases = []cas{
		{nom: "source inconnue", ip: "192.168.1.1", community: "lecture"},
		{nom: "community inattendue", ip: "10.0.0.5", community: "public"},
		{nom: "community absente (trap v3)", ip: "10.0.0.5", community: ""},
		{nom: "source illisible", ip: "", community: "lecture"},
	}
	var k cas
	for _, k = range cases {
		t.Run(k.nom, func(t *testing.T) {
			var c, rec = nouveauCollecteur(t)
			c.onTrap(paquetTrap(oidLinkDown, k.community), sourceUDP(k.ip), viseeLocale)
			if len(rec.evs) != 0 {
				t.Fatalf("aucun evenement expected, obtenu %+v", rec.evs)
			}
			// Et la preuve reste disponible : un trap rejete ne doit pas non plus
			// CONSOMMER le droit de prouver plus tard, sinon un usurpateur suffirait a
			// rendre the device muet pour toujours.
			if !c.trapGate.firstSeen("sw1") {
				t.Error("un trap rejete ne doit pas consommer la preuve de sw1")
			}
		})
	}
}

func TestOnTrapReprouveApresUnRetraitDeSurveillance(t *testing.T) {
	var c, rec = nouveauCollecteur(t)
	var watch = []tunnel.SNMPWatch{
		{DeviceID: "sw1", IP: "10.0.0.5", Community: "lecture", StateIntervalS: 30},
	}

	c.onTrap(paquetTrap(oidLinkDown, "lecture"), sourceUDP("10.0.0.5"), viseeLocale)
	if len(rec.evs) != 1 {
		t.Fatalf("preuve initiale attendue, obtenu %+v", rec.evs)
	}

	// Retire de la surveillance : purge silencieuse, comme pour les etats de port.
	rec.evs = nil
	c.SetWatches(nil)
	if len(rec.evs) != 0 {
		t.Fatalf("un retrait de surveillance n'emet rien : %+v", rec.evs)
	}

	// Reintegrated : le pare-feu, la route ou l'ACL a pu changer entre-temps, donc la
	// preuve d'hier n'engage plus rien — elle est refaite, UNE fois.
	c.SetWatches(watch)
	defer desamorce(c)
	c.onTrap(paquetTrap(oidLinkDown, "lecture"), sourceUDP("10.0.0.5"), viseeLocale)
	c.onTrap(paquetTrap(oidLinkDown, "lecture"), sourceUDP("10.0.0.5"), viseeLocale)
	if len(rec.evs) != 1 || rec.evs[0].Type != tunnel.EventTrapSeen {
		t.Fatalf("une seule preuve after reintegration, obtenu %+v", rec.evs)
	}
}

// TestOnTrapNeDivulguePasLaCommunaute : la community est un secret d'operation.
// L'evenement part au controleur, y est stocke, puis affiche — le verify sur la
// forme SERIALISEE rather que champ par champ garde le test vrai le jour where
// tunnel.Event gagne un champ.
func TestOnTrapNeDivulguePasLaCommunaute(t *testing.T) {
	const communaute = "s3cr3t-de-labo"
	var c = NewSNMPCollector("http://exemple.invalid", "px1", "s3cr3t")
	var rec = &collecte{}
	c.SetEmitter(rec.note)
	c.SetWatches([]tunnel.SNMPWatch{
		{DeviceID: "sw1", IP: "10.0.0.5", Community: communaute, StateIntervalS: 30},
	})
	defer desamorce(c)

	c.onTrap(paquetTrap(oidLinkUp, communaute), sourceUDP("10.0.0.5"), viseeLocale)
	if len(rec.evs) != 1 {
		t.Fatalf("un evenement de preuve expected, obtenu %+v", rec.evs)
	}
	var brut, err = json.Marshal(rec.evs[0])
	if err != nil {
		t.Fatalf("serialisation de l'evenement : %v", err)
	}
	if strings.Contains(string(brut), communaute) {
		t.Errorf("la community n'a rien a faire dans un evenement : %s", brut)
	}
}

// --- Attribution de la preuve : l'adresse VISEE par le datagramme ---

// TestOnTrapPorteLAdresseVisee : la preuve doit dire A QUELLE ADRESSE le trap est
// arrive, sans quoi elle ne prouve pas ce que le controleur lui fait dire.
//
// Le filtre du proxy n'examine que l'IP SOURCE et la community ; sa socket ecoute
// l'adresse joker et accepte tout ce qui atteint 162/udp. Un device qui emettrait
// vers un autre collecteur de la same machine fabriquerait donc une « preuve » de
// l'adresse que meshcraft vient to write, sans le moindre rapport. Seule l'adresse
// locale visee referme cet ecart, et elle ne peut venir que d'ici : le controleur, lui,
// ne voit pas les datagrams.
func TestOnTrapPorteLAdresseVisee(t *testing.T) {
	var c, rec = nouveauCollecteur(t)
	defer desamorce(c)

	c.onTrap(paquetTrap(oidLinkDown, "lecture"), sourceUDP("10.0.0.5"), viseeLocale)
	if len(rec.evs) != 1 {
		t.Fatalf("un evenement de preuve expected, obtenu %+v", rec.evs)
	}
	if rec.evs[0].DestAddr != "10.42.0.67" {
		t.Errorf("la preuve doit porter l'adresse visee, obtenu %q", rec.evs[0].DestAddr)
	}
}

// TestOnTrapNInventePasLAdresseVisee : quand le noyau n'a pas dit la destination, le
// champ reste VIDE.
//
// Le repli tentant serait d'y mettre l'adresse par laquelle le proxy joint
// the device — celle qu'il a rendue a `AskSourceAddr`, donc celle que le controleur
// a written. Ce serait rendre la verification tautologique : elle confirmerait toujours,
// puisqu'elle comparerait la pretention a elle-same. Un « je ne sais pas » doit
// traverser le tunnel tel quel, et c'est le controleur qui decide de ce qu'une preuve
// non attribuee vaut.
func TestOnTrapNInventePasLAdresseVisee(t *testing.T) {
	var c, rec = nouveauCollecteur(t)
	defer desamorce(c)

	c.onTrap(paquetTrap(oidLinkDown, "lecture"), sourceUDP("10.0.0.5"), netip.Addr{})
	if len(rec.evs) != 1 {
		t.Fatalf("un evenement de preuve expected, obtenu %+v", rec.evs)
	}
	if rec.evs[0].DestAddr != "" {
		t.Errorf("une destination inconnue ne s'invente pas, obtenu %q",
			rec.evs[0].DestAddr)
	}
}

// TestTrapDstAddrNormalise : ce que le noyau rend n'est pas comparable tel quel.
func TestTrapDstAddrNormalise(t *testing.T) {
	// Socket double pile : le noyau rend la destination d'un datagramme IPv4 sous
	// forme mappee. Comparer « ::ffff:10.42.0.67 » a l'inventaire du controleur, qui
	// written « 10.42.0.67 », ferait echouer l'attribution selon l'humeur de la pile de
	// l'hote — la same precaution que classifyTrap.
	var got = trapDstAddr(net.ParseIP("::ffff:10.42.0.67"))
	if got.String() != "10.42.0.67" {
		t.Errorf("adresse mappee non demappee : %q", got.String())
	}
	// Rien a dire, et il faut que ca se DISE : une adresse absente ou joker n'est pas
	// une adresse fausse, c'est une absence de response. Le controleur en tire une
	// preuve non attribuee, pas un refus.
	if trapDstAddr(nil).IsValid() {
		t.Error("une destination absente doit be invalide")
	}
	if trapDstAddr(net.ParseIP("0.0.0.0")).IsValid() {
		t.Error("l'adresse joker ne designe rien et ne doit pas be rendue")
	}
	if trapDstAddr(net.ParseIP("::")).IsValid() {
		t.Error("l'adresse joker v6 ne designe rien et ne doit pas be rendue")
	}
}

// collecteSure : le same enregistreur, mais verrouille.
//
// Necessaire pour le test de bout en bout et pour lui seul : l'evenement y arrive sur
// la goroutine de lecture de la socket, alors que tous les autres tests appellent
// `onTrap` eux-memes. Un `collecte` nu y serait une course de data (et `-race` le
// dirait).
type collecteSure struct {
	mu  sync.Mutex
	evs []tunnel.Event
}

func (r *collecteSure) note(ev tunnel.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.evs = append(r.evs, ev)
}

// attendre rend le premier evenement collecte, ou renonce. Un trap est de l'UDP en
// boucle locale : s'il n'est pas arrive en une seconde, il n'arrivera pas.
func (r *collecteSure) attendre(t *testing.T) tunnel.Event {
	t.Helper()
	var fin = time.Now().Add(time.Second)
	for time.Now().Before(fin) {
		r.mu.Lock()
		var n = len(r.evs)
		var first tunnel.Event
		if n > 0 {
			first = r.evs[0]
		}
		r.mu.Unlock()
		if n > 0 {
			return first
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("aucun evenement de preuve recu par la socket de traps")
	return tunnel.Event{}
}

// TestSocketDeTrapsLitLAdresseVisee : le test de BOUT EN BOUT du collecteur, et le
// seul qui verifies ce que le noyau veut bien dire.
//
// Il envoie un vrai trap v2c sur une vraie socket et attend la preuve a l'autre bout.
// C'est ce qui rend la decision de ce lot defendable : la lecture de l'adresse de
// destination (PKTINFO) n'est pas de la logique qu'on peut simuler — ou le noyau la
// donne, ou il ne la donne pas. S'il ne la donnait pas, le canal continuerait de
// fonctionner mais toutes les preuves seraient non attribuees, et il faut le SAVOIR
// ici rather que de le decouvrir sur le parc d'un client.
//
// Il verifies du same coup que remplacer `gosnmp.TrapListener` par notre socket n'a
// rien casse du decoding : le paquet est fabrique et envoye par gosnmp, et c'est
// `gosnmp.UnmarshalTrap` — exporte, donc conserve — qui le relit.
func TestSocketDeTrapsLitLAdresseVisee(t *testing.T) {
	var c = NewSNMPCollector("http://exemple.invalid", "px1", "s3cr3t")
	var rec = &collecteSure{}
	c.SetEmitter(rec.note)
	// La boucle locale comme device surveille : c'est la seule adresse dont un test
	// puisse be a la fois la source et la destination.
	c.SetWatches([]tunnel.SNMPWatch{
		{DeviceID: "sw1", IP: "127.0.0.1", Community: "lecture", StateIntervalS: 30},
	})
	defer desamorce(c)

	// Port 0 : le noyau en choisit un libre. Demander 162 exigerait un privilege que
	// n'a aucun conteneur de test, et le port n'apprend rien ici.
	var rx, err = startTrapReceiver(0, c.onTrap)
	if err != nil {
		t.Fatalf("socket de traps non liee : %v", err)
	}
	defer rx.stop()
	if !rx.dstKnown() {
		t.Fatal("ce noyau ne rend pas l'adresse de destination des datagrams : " +
			"toute preuve du chemin retour serait non attribuee")
	}

	var g = &gosnmp.GoSNMP{
		Target: "127.0.0.1", Port: uint16(rx.bound), Transport: "udp", //nolint:gosec
		Community: "lecture", Version: gosnmp.Version2c,
		Timeout: time.Second, Retries: 0,
	}
	if err = g.Connect(); err != nil {
		t.Fatalf("connexion de l'emetteur de trap : %v", err)
	}
	defer g.Conn.Close() //nolint:errcheck
	if _, err = g.SendTrap(gosnmp.SnmpTrap{Variables: []gosnmp.SnmpPDU{
		{Name: oidSnmpTrapOID, Type: gosnmp.ObjectIdentifier, Value: oidLinkDown},
	}}); err != nil {
		t.Fatalf("envoi du trap : %v", err)
	}

	var ev = rec.attendre(t)
	if ev.Type != tunnel.EventTrapSeen || ev.DeviceID != "sw1" {
		t.Fatalf("preuve du chemin retour attendue, obtenu %+v", ev)
	}
	if ev.DestAddr != "127.0.0.1" {
		t.Errorf("le trap a been adresse a 127.0.0.1, la preuve dit %q", ev.DestAddr)
	}
	if ev.Detail != "linkDown" {
		t.Errorf("le trap doit be decode jusqu'a son OID, Detail %q", ev.Detail)
	}
}

// TestArretDeSocketNEstPasUnIncident : fermer la socket fait echouer la lecture en
// cours, et cette erreur-there ne doit pas se declare « morte ».
//
// C'est la contrepartie du garde-fou anti-mort-silencieuse : une lecture qui rend la
// main sur une erreur inattendue marque la socket, et reconcileTraps en relie une
// neuve. Confondre l'arret DEMANDE avec un incident ferait donc relier une socket
// juste after chaque extinction volontaire — i.e. a chaque fois que le
// controleur retire l'ecoute des traps de la configuration de surveillance.
func TestArretDeSocketNEstPasUnIncident(t *testing.T) {
	var rx, err = startTrapReceiver(0, func(*gosnmp.SnmpPacket, *net.UDPAddr, netip.Addr) {})
	if err != nil {
		t.Fatalf("socket de traps non liee : %v", err)
	}
	rx.stop()
	// La boucle sort de sa lecture bloquante a la fermeture ; quelques millisecondes
	// suffisent, et une attente franche vaut mieux qu'un test qui depend de
	// l'ordonnanceur.
	time.Sleep(50 * time.Millisecond)
	if rx.dead.Load() {
		t.Error("un arret requested ne doit pas se lire comme une lecture morte")
	}
}

func TestTrapSeenDetail(t *testing.T) {
	if trapSeenDetail(oidLinkUp, "linkUp") != "linkUp" {
		t.Error("le label reconnu passe devant l'OID")
	}
	// Trap constructeur : pas de nom, mais l'OID situe la gamme — « il emet, mais
	// pas de linkUp/linkDown » ne se rule pas comme un silence complet.
	var got = trapSeenDetail("1.3.6.1.4.1.11.2.14.11.5.1.7.1.29.1", "")
	if got != "oid=1.3.6.1.4.1.11.2.14.11.5.1.7.1.29.1" {
		t.Errorf("OID brut expected, obtenu %q", got)
	}
	// Trap v1 : aucun snmpTrapOID.0 a citer, et le Detail ne doit pas be vide
	// pour autant.
	if trapSeenDetail("", "") == "" {
		t.Error("un Detail vide se lirait comme un champ perdu en route")
	}
}

func TestOperStatusLabel(t *testing.T) {
	// « up » est reserve a 1, et a lui seul : c'est la decision qui gouverne tout
	// le reste du fichier.
	var status int
	for status = 2; status <= 7; status++ {
		if portUp(status) {
			t.Errorf("ifOperStatus=%d ne doit pas compter comme up", status)
		}
	}
	if !portUp(ifOperUp) {
		t.Error("ifOperStatus=1 doit compter comme up")
	}
	if operStatusLabel(7) != "lowerLayerDown" || operStatusLabel(42) != "inconnu" {
		t.Errorf("labels inattendus : %q / %q", operStatusLabel(7), operStatusLabel(42))
	}
}
