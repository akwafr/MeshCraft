package swos

import (
	"strings"
	"testing"
	"time"

	"meshcraft/internal/driver"
)

// Observability declared par le connecteur SwOS.
//
// Ce n'est pas de la paraphrase de structure : les planchers declares ici BORNENT la
// politique de collecte du controleur. Un canal qui regroupe deux metriques leur
// impose le same plancher — c'est ainsi que les compteurs d'interface du connecteur
// Instant On se sont retrouves brides par la sante et le LLDP. Le split est donc
// une property a tenir, pas un detail de redaction.
//
// Et sur cette plateforme il y a une raison de plus d'y tenir : SwOS ne pousse RIEN.
// There where le canal de traps d'un 1930 permet de ralentir le polling state sans perdre
// la reactivity, ici le polling EST la reactivity — la cadence retenue est
// exactement le delai de detection d'un lien qui tombe.

func canalDe(t *testing.T, mon driver.MonitoringCapabilities,
	metric string) driver.MonitorChannel {
	t.Helper()
	var ch, ok = mon.Best(metric)
	if !ok {
		t.Fatalf("aucun canal declared pour %q : la metrique serait non supervisee", metric)
	}
	return ch
}

// TestPlanchersWebChacunSonCanal : les quatre canaux, et leurs planchers.
//
// Les valeurs ne sont pas recopiees d'un autre connecteur : chacune est justified par
// le COST de la page correspondante, mesure sur l'reference device. C'est
// pourquoi elles sont verified une par une — les changer doit be un geste
// deliberate, pas un effet de bord d'un regroupement « de proprete ».
func TestPlanchersWebChacunSonCanal(t *testing.T) {
	var mon = (&Driver{}).Capabilities("CSS326-24G-2S+").Monitoring

	// LE TEST INTERROGE MAINTENANT PAR GENRE DE CANAL, et ce n'est pas un detail de
	// forme : depuis que l'agent SNMP est observed, `if_state`, `if_counters` et `health`
	// sont servis par DEUX canaux aux planchers differents. `Best` rend le plus rapide
	// des deux — donc interroger par metrique seule ne dirait plus rien du cost de la
	// page web, qui est ce que ces valeurs justifient.
	var expected = []struct {
		kind     string
		metric   string
		interval time.Duration
	}{
		// --- Le canal WEB : chaque plancher est le cost d'une page, mesure. ---
		//
		// State des ports est le plus RAPIDE des canaux web, et c'est deliberate : sans
		// trap, le polling est la seule source de detection d'un lien qui tombe.
		{"web", "if_state", 10 * time.Second},
		// Les compteurs sont plus LENTS que state, a l'inverse du connecteur Instant
		// On : /stats.b est six fois plus grosse que /link.b et se serialise en
		// entier, sans qu'aucun moyen n'existe d'en demander moins.
		{"web", "if_counters", 15 * time.Second},
		{"web", "health", 30 * time.Second},
		// La puissance optique EST servie par SwOS, contrairement a l'Instant On — et
		// par le canal WEB SEULEMENT (voir plus bas).
		{"web", "optical_power", 30 * time.Second},

		// --- Le canal SNMP : un seul plancher, parce qu'un GETBULK ne costs pas une
		// page. C'est tout l'interet de ce canal, et la raison pour laquelle le
		// split par cost qui vaut en web n'a pas a be reproduit ici.
		{"snmp", "if_state", 10 * time.Second},
		{"snmp", "if_counters", 10 * time.Second},
		{"snmp", "health", 10 * time.Second},
	}
	var a = expected[0]
	for _, a = range expected {
		var ch, ok = mon.BestOfKind(a.kind, a.metric)
		if !ok {
			t.Errorf("aucun canal %q ne sert %q : la metrique serait non supervisee par "+
				"ce genre de canal", a.kind, a.metric)
			continue
		}
		if ch.MinInterval != a.interval {
			t.Errorf("%s/%s : plancher %v, expected %v — cette valeur est justified par le "+
				"cost de la lecture, la changer demande de le remesurer",
				a.kind, a.metric, ch.MinInterval, a.interval)
		}
	}

	// LES PUISSANCES OPTIQUES NE PASSENT PAS PAR SNMP, et ce test le verrouille parce
	// que c'est le genre de chose qu'on ajoute « par symetrie » un jour de menage.
	//
	// OBSERVED : la table optique de cet agent stops a la colonne 8 (`txBias`). Ni
	// `txPower` (colonne 9), ni `rxPower` (colonne 10) — walk complet de
	// 1.3.6.1.4.1.14988.1.1.19 sur 10.99.0.36, dix valeurs, colonnes 2, 5, 6, 7 et 8.
	// Les puissances sont dans /sfp.b (`tpw`, `rpw`) et nulle part ailleurs. Declare
	// `optical_power` sur le canal SNMP promettrait une courbe qu'aucune collecte ne
	// remplirait.
	var _, optiqueEnSNMP = mon.BestOfKind("snmp", "optical_power")
	if optiqueEnSNMP {
		t.Error("optical_power declared sur le canal SNMP : l'agent de cette plateforme " +
			"sert la table optique SANS ses colonnes de puissance (observed sur hardware). " +
			"La courbe resterait vide.")
	}

	// Le SPLIT PAR COST doit rester reel SUR LE CANAL WEB : les compteurs, qui sont
	// la page la plus chere, ne doivent jamais y partager un canal avec une metrique
	// legere. Les regrouper imposerait a l'une le plancher de l'autre.
	//
	// La rule est restreinte au web, et c'est la consequence directe de ce qui la
	// fonde : elle parle du cost d'une PAGE. En SNMP il n'y a pas de page — six
	// colonnes se demandent en un GETBULK — et le regroupement y est le bon choix.
	var legeres = map[string]bool{"health": true, "optical_power": true, "if_state": true}
	var c driver.MonitorChannel
	for _, c = range mon.Channels {
		if c.Kind != "web" {
			continue
		}
		var porteCompteurs, porteLegeres bool
		var metric string
		for _, metric = range c.Metrics {
			if metric == "if_counters" {
				porteCompteurs = true
			}
			if legeres[metric] {
				porteLegeres = true
			}
		}
		if porteCompteurs && porteLegeres {
			t.Errorf("canal web %+v : les compteurs ne doivent pas partager leur plancher "+
				"avec une metrique legere — /stats.b costs six fois plus que les autres pages", c)
		}
	}
}

// TestSwOSNePoussePasEtLeDit : l'absence de traps est ce qui JUSTIFIE la cadence.
//
// La declaration `Traps: false` n'est pas cosmetique : c'est elle qui explique
// pourquoi `if_state` est ici le canal le plus rapide, alors qu'il est le plus lent
// chez l'Instant On. La faire passer a vrai sans qu'un trap existe ferait ralentir le
// polling en croyant gagner de la reactivity — et la detection d'un lien coupe
// disparaitrait purement et simplement.
func TestSwOSNePoussePasEtLeDit(t *testing.T) {
	var mon = (&Driver{}).Capabilities("CSS326-24G-2S+").Monitoring
	if mon.Traps {
		t.Error("SwOS n'a AUCUN destinataire de trap : /snmp.b n'a que quatre champs, " +
			"activation, community, contact, localisation")
	}
	if mon.Syslog {
		t.Error("SwOS n'a aucun serveur syslog, et n'a same pas d'horloge pour " +
			"horodater un message")
	}
	if mon.Streaming {
		t.Error("aucune telemetry continue sur cette plateforme")
	}

	// Le canal state doit PORTER cette consequence dans sa note : c'est there que
	// l'exploitant la lira.
	var ch = canalDe(t, mon, "if_state")
	if !strings.Contains(strings.ToLower(ch.Note), "trap") {
		t.Errorf("la note du canal if_state doit dire que rien n'est pousse, donc que "+
			"ce polling est la seule detection d'un lien coupe ; obtenu : %q", ch.Note)
	}
}

// TestLLDPDeclareIndisponible : SwOS ne parle pas LLDP, et le connecteur le DIT.
//
// Ce que ce test protege n'est pas la structure, c'est la DIFFERENCE entre « nous ne
// le collectons pas encore » et « the device ne l'expose pas ». La first fait
// attendre une version, la seconde fait changer de hardware. Effacer cette
// declaration ramenerait le voisinage au rang de simple manque de meshcraft, et
// ferait patienter un exploitant devant une carte qui n'arrivera jamais.
func TestLLDPDeclareIndisponible(t *testing.T) {
	// L'absence est un fait de PLATEFORME, pas de model : elle vaut pour le CSS326
	// du observed comme pour les autres references, y compris quand le model est
	// inconnu — cas dans lequel la rule du projet impose already de supposer le pire.
	var modele string
	for _, modele = range []string{"CSS326-24G-2S+", "CSS610-8P-2S+", "CRS328-24P-4S+", ""} {
		var mon = (&Driver{}).Capabilities(modele).Monitoring

		var u, ok = mon.WhyUnavailable("lldp")
		if !ok {
			t.Fatalf("model %q : LLDP doit be declared indisponible, sinon son absence "+
				"n'est qu'un silence", modele)
		}
		if !u.Observed {
			t.Errorf("model %q : l'absence est CONSTATEE par enumeration de l'interface "+
				"entire de the device, pas supposee", modele)
		}
		// Operator copy lives in the UI catalog; the connector only declares the metric
		// and carries verifiable survey evidence in Source.
		var expected string
		for _, expected = range []string{"CSS326-24G-2S+", "44,807", "backup.swb"} {
			if !strings.Contains(u.Source, expected) {
				t.Errorf("model %q : la source du observed doit citer %q, obtenu : %q",
					modele, expected, u.Source)
			}
		}
		// Et la feature correspondante doit be fausse : les deux declarations ne
		// doivent pas pouvoir diverger.
		if (&Driver{}).Capabilities(modele).Has("LLDP") {
			t.Errorf("model %q : la feature LLDP est vraie alors que la metrique est "+
				"declared indisponible", modele)
		}
	}
}

// TestPoEIndisponibleSelonLeModele : l'absence depend du HARDWARE, et elle est
// declared there where elle existe — pas ailleurs.
//
// Sur un model sans PoE, le motif est un fait : il n'y a pas d'electronique a
// mesurer. Sur un model avec PoE, la metrique n'est ni servie par un canal ni
// declared impossible, et c'est la verite : SwOS l'expose, meshcraft ne la lit pas
// encore. Declare l'absence pour tout le monde ferait renoncer un exploitant sur un
// CRS PoE qui, lui, sert bien ces valeurs.
func TestPoEIndisponibleSelonLeModele(t *testing.T) {
	var sansPoE = (&Driver{}).Capabilities("CSS326-24G-2S+").Monitoring
	var u, ok = sansPoE.WhyUnavailable("poe")
	if !ok {
		t.Fatal("un model sans PoE doit dire pourquoi il n'a pas de courbe de PoE")
	}
	if !u.Observed || !strings.Contains(u.Source, "npoe") {
		t.Errorf("l'absence de PoE est observed sur /sys.b (`npoe:0x00`) ; source : %q", u.Source)
	}

	var avecPoE = (&Driver{}).Capabilities("CRS328-24P-4S+").Monitoring
	var _, declared = avecPoE.WhyUnavailable("poe")
	if declared {
		t.Error("un model PoE ne doit PAS declare le PoE indisponible : SwOS l'expose, " +
			"c'est meshcraft qui ne le lit pas encore, et confondre les deux ferait " +
			"changer de hardware pour rien")
	}
	// Et la feature suit le model.
	if (&Driver{}).Capabilities("CSS326-24G-2S+").Has("PoE") {
		t.Error("le CSS326-24G-2S+ n'a pas de PoE")
	}
	if !(&Driver{}).Capabilities("CRS328-24P-4S+").Has("PoE") {
		t.Error("le CRS328-24P-4S+ est un model PoE")
	}
}

// TestAucuneContradictionDeclaree : l'invariant du contrat.
//
// Une declaration qui se contredit est pire que pas de declaration : soit the UI
// annonce une courbe qu'aucune collecte ne remplira jamais, soit il affiche un motif
// d'absence a cote d'une courbe qui existe. Dans les deux cas l'exploitant perd la
// seule chose que ces capabilities lui apportent — la certitude que ce qui est
// affiche correspond a the device.
//
// Verifie sur PLUSIEURS modeles, y compris le model inconnu : la liste des
// indisponibles varie avec le PoE, et c'est exactement le genre de variation qui
// introduit une contradiction sans qu'on y pense.
func TestAucuneContradictionDeclaree(t *testing.T) {
	var modele string
	for _, modele = range []string{
		"CSS326-24G-2S+", "CSS610-8G-2S+", "CRS328-24P-4S+", "CSS610-8P-2S+", "",
	} {
		var mon = (&Driver{}).Capabilities(modele).Monitoring
		var contradictions = mon.Contradictions()
		if len(contradictions) != 0 {
			t.Errorf("model %q : declaration contradictoire pour %v — une metrique est a "+
				"la fois rendue par un canal et declared indisponible", modele, contradictions)
		}
	}
}

// TestPuissanceOptiqueServie : la difference avec l'Instant On, et il faut qu'elle
// tienne.
//
// L'Instant On ne sert PAS la puissance optique et le declares indisponible ; SwOS la
// sert. Deux connecteurs, deux verites opposees sur la same metrique, et c'est
// precisely a cela que sert le mecanisme. Declare ici l'indisponibilite par
// symetrie avec l'autre connecteur ferait perdre une data que the device met a
// disposition.
func TestPuissanceOptiqueServie(t *testing.T) {
	var mon = (&Driver{}).Capabilities("CSS326-24G-2S+").Monitoring
	var _, ok = mon.Best("optical_power")
	if !ok {
		t.Fatal("SwOS sert le diagnostic optique de ses cages (/sfp.b) : temperature, " +
			"tension, courant de polarisation, puissances emise et recue")
	}
	var _, declared = mon.WhyUnavailable("optical_power")
	if declared {
		t.Error("la puissance optique est SERVIE par cette plateforme : la declare " +
			"indisponible priverait d'une data que the device rend")
	}
}

// TestPuissanceOptiqueLisibleSurLaCapture : la declaration ci-dessus est adossee a
// un observed, et le observed est dans le paquet.
func TestPuissanceOptiqueLisibleSurLaCapture(t *testing.T) {
	var page, err = ParseObject(capture(t, "sfp.b"))
	if err != nil {
		t.Fatalf("/sfp.b : %v", err)
	}
	// Deux cages : la first vide, la seconde occupee par un SFP-10G-BX.
	var tmp, ok = page.Nums(FieldSFPTemp)
	if !ok || len(tmp) != 2 {
		t.Fatalf("%s = %v (ok=%v), expected 2 valeurs", FieldSFPTemp, tmp, ok)
	}
	// La cage VIDE se reconnait a sa temperature : -128 °C, la valeur que l'interface
	// de the device emploie pour « pas de module ». La lire comme une temperature
	// afficherait une cage a -128 degrees.
	if tmp[0] != sfpNoModule {
		t.Errorf("cage 1 : temperature %#x, expected %#x (cage vide)", tmp[0], sfpNoModule)
	}
	// La cage OCCUPEE porte une temperature vivante — et c'est bien une valeur
	// vivante : elle a been observed a 0x32 (50 °C) puis a 0x35 (53 °C) a quelques
	// minutes d'ecart sur le same device. On verifies donc la PROPERTY (une
	// temperature plausible, et pas le marqueur de cage vide) et non le observed exact :
	// figer un chiffre de telemetry ferait echouer ce test au prochain
	// rafraichissement des captures, ce qui n'apprendrait rien a personne.
	if tmp[1] == sfpNoModule {
		t.Error("cage 2 : marqueur de cage vide alors qu'un transceiver y est insere")
	}
	if tmp[1] < 10 || tmp[1] > 90 {
		t.Errorf("cage 2 : temperature %d °C hors de toute plage plausible — le champ "+
			"est-il bien en degrees entiers sans facteur d'echelle ?", tmp[1])
	}
	// Les puissances de la cage occupee sont non nulles : c'est ce qui establishes que la
	// plateforme les SERT actually, et pas seulement qu'elle a un champ pour elles.
	var rpw []uint32
	rpw, ok = page.Nums(FieldSFPRxPower)
	if !ok || rpw[1] == 0 {
		t.Errorf("%s = %v : la cage occupee doit porter une puissance recue non nulle",
			FieldSFPRxPower, rpw)
	}
	// Et le transceiver s'identifie, champs SFF-8472 rembourres par des espaces.
	var pnr, textOK = page.TextAt(FieldSFPPart, 1)
	if !textOK || strings.TrimSpace(pnr) != "SFP-10G-BX" {
		t.Errorf("reference du transceiver = %q (ok=%v), expected « SFP-10G-BX » after "+
			"rognage des espaces de rembourrage", pnr, textOK)
	}
}

// TestCompteursSurLaCapture : les compteurs 64 bits et le rate calcule par
// the device.
//
// Deux properties qui decident de la justesse d'une courbe de trafic.
func TestCompteursSurLaCapture(t *testing.T) {
	var page, err = ParseObject(capture(t, "stats.b"))
	if err != nil {
		t.Fatalf("/stats.b : %v", err)
	}
	var low, ok = page.Nums(FieldRxBytes)
	if !ok || len(low) != 26 {
		t.Fatalf("%s = %d valeur(s), expected 26", FieldRxBytes, len(low))
	}
	var high []uint32
	high, ok = page.Nums(FieldRxBytesHigh)
	if !ok || len(high) != 26 {
		t.Fatalf("%s = %d valeur(s), expected 26", FieldRxBytesHigh, len(high))
	}
	// La capture porte du trafic sur la seule cage active. Le compteur 64 bits se
	// recompose des deux halves : lire `rb` seul ferait repasser la courbe a zero
	// tous les 4 Go, soit toutes les trois secondes et demie sur un lien 10G.
	var octets = Counter64(low[25], high[25])
	if octets == 0 {
		t.Errorf("la cage active doit porter du trafic ; rb=%#x rbh=%#x", low[25], high[25])
	}
	if octets != uint64(low[25]) {
		t.Errorf("dans cette capture les 32 bits hauts sont nuls, Counter64 doit donc "+
			"rendre exactement rb ; obtenu %d", octets)
	}
	// Recomposition avec un mot haut non nul : c'est there que l'ordre compte.
	var got = Counter64(0x00000002, 0x00000001)
	if got != 0x100000002 {
		t.Errorf("Counter64(2, 1) = %#x, expected 0x100000002", got)
	}

	// Le rate est SERVI par the device : c'est ce qui permet un plancher de
	// collecte large sans lisser la courbe.
	if !page.Has(FieldRxRate) || !page.Has(FieldTxRate) {
		t.Errorf("/stats.b doit porter les rates computed (%s, %s) : c'est la "+
			"justification du plancher de 15 s", FieldRxRate, FieldTxRate)
	}
}
