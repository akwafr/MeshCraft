package swos

import (
	"reflect"
	"testing"
)

// La lecture de state applique, verified sur les captures REELLES de the device.
//
// Ces tests sont ce qui rend la traduction du model VLAN de SwOS verifiable par
// quelqu'un d'autre : ils rejouent /vlan.b, /fwd.b et /link.b tels que le
// CSS326-24G-2S+ de reference les a servis, et verify que la configuration
// reconstruite est celle qui explique le comportement observe de the device.

// etatDeReference lit les trois pages de la capture et reconstruit state, sans
// network.
func etatDeReference(t *testing.T) (map[string]PortState, VLANFiltering) {
	t.Helper()
	var vlanRows, err = ParseTable(capture(t, "vlan.b"))
	if err != nil {
		t.Fatalf("/vlan.b : %v", err)
	}
	var fwd *Page
	fwd, err = ParseObject(capture(t, "fwd.b"))
	if err != nil {
		t.Fatalf("/fwd.b : %v", err)
	}
	var st, filtering, buildErr = portsFrom(vlanRows, fwd, facadeCSS326())
	if buildErr != nil {
		t.Fatalf("reconstruction refused : %v", buildErr)
	}
	return st, filtering
}

// TestAppartenanceVLANDeReference : la traduction du model VLAN de SwOS.
//
// L'reference device porte deux VLAN (99 et 100), tous les ports membres des
// deux, un « Default VLAN ID » de 100 sur les 24 ports cuivre et de 1 sur les deux
// cages SFP. La rule d'tagging de SwOS — une trame sort non tagged si son VLAN
// est le VLAN natif du port, tagged sinon — donne donc :
//
//	Port1…Port24 : natif 100, tagged [99]        → trunk
//	SFP1 / SFP2  : natif 1,   tagged [99, 100]   → trunk
//
// LES NOMS SONT CEUX DE THE DEVICE, et c'est ce que la capture establishes : /link.b sert
// « Port1 »… « Port24 », « SFP1 », « SFP2 » dans son champ `nm` (cf. naming.go et
// TestLesNomsDePortsSontCeuxDeLEquipement).
//
// CE QUE CETTE LECTURE PREDIT, ET QUI EST VERIFIED : la cage SFP2 transporte le
// VLAN 99 TAGGED. Or c'est le seul port de cet device qui porte un lien, et
// the device est administre en VLAN 99 — et il repond. Une rule d'tagging
// inversee predirait que le VLAN 99 sort untagged de ce port et que le VLAN 1 en
// sort tagged, ce qui rendrait the device muet. La traduction est donc corroborated
// par le seul fait de commutation observable de l'exterieur.
func TestAppartenanceVLANDeReference(t *testing.T) {
	var st, _ = etatDeReference(t)

	if len(st) != 26 {
		t.Fatalf("%d port(s) reconstruit(s), expected 26", len(st))
	}

	// Les 24 ports cuivre.
	var i int
	for i = 1; i <= 24; i++ {
		var name = CopperPortPrefix + itoa(i)
		var p = st[name]
		if p.Mode != "trunk" || p.PVID != 100 ||
			!reflect.DeepEqual(p.Tagged, []int{99}) {
			t.Errorf("%s = %+v ; expected un trunk de natif 100 tagged [99]", name, p)
		}
	}
	// Les deux cages SFP, dont le VLAN natif est 1 : elles portent donc 99 ET 100
	// tagged.
	for i = 1; i <= 2; i++ {
		var name = SFPPortPrefix + itoa(i)
		var p = st[name]
		if p.Mode != "trunk" || p.PVID != 1 ||
			!reflect.DeepEqual(p.Tagged, []int{99, 100}) {
			t.Errorf("%s = %+v ; expected un trunk de natif 1 tagged [99 100] — c'est ce "+
				"port qui porte l'administration en VLAN 99 tagged", name, p)
		}
	}
}

// TestVLANNatifJamaisTague : le cœur de la rule d'tagging.
//
// Un port membre d'un VLAN qui est aussi son VLAN natif ne doit JAMAIS apparaitre
// comme tagged dans ce VLAN. Le faire produirait un port qui semble a la fois natif et
// tagged dans le same VLAN — ce que la reconciliation lirait comme un ecart permanent,
// et que la pose tenterait de « corriger » a chaque passage.
func TestVLANNatifJamaisTague(t *testing.T) {
	// Un port unique, membre de 10 et 20, dont le natif est 10.
	var rows, err = ParseTable(
		"[{nm:'',mbr:0x01,vid:0x000a},{nm:'',mbr:0x01,vid:0x0014}]")
	if err != nil {
		t.Fatalf("table refused : %v", err)
	}
	var fwd *Page
	fwd, err = ParseObject("{vlan:[0x02],dvid:[0x000a]}")
	if err != nil {
		t.Fatalf("page refused : %v", err)
	}
	var st, _, buildErr = portsFrom(rows, fwd, facadeCuivre(1))
	if buildErr != nil {
		t.Fatalf("reconstruction refused : %v", buildErr)
	}
	var p = st["Port1"]
	if p.PVID != 10 {
		t.Errorf("PVID = %d, expected 10", p.PVID)
	}
	if !reflect.DeepEqual(p.Tagged, []int{20}) {
		t.Errorf("tagged = %v, expected [20] : le VLAN natif ne doit pas y figurer", p.Tagged)
	}
}

// TestPortSansVLANTagueEstUnAcces : un port membre de son seul VLAN natif est un
// access, pas un trunk. Le classer en trunk ferait diverger l'intention de ce que
// the device rapportera toujours.
func TestPortSansVLANTagueEstUnAcces(t *testing.T) {
	var rows, err = ParseTable("[{nm:'',mbr:0x01,vid:0x0014}]")
	if err != nil {
		t.Fatalf("table refused : %v", err)
	}
	var fwd *Page
	fwd, err = ParseObject("{vlan:[0x02],dvid:[0x0014]}")
	if err != nil {
		t.Fatalf("page refused : %v", err)
	}
	var st, _, buildErr = portsFrom(rows, fwd, facadeCuivre(1))
	if buildErr != nil {
		t.Fatalf("reconstruction refused : %v", buildErr)
	}
	if st["Port1"].Mode != "access" || st["Port1"].PVID != 20 || st["Port1"].Tagged != nil {
		t.Errorf("Port1 = %+v, expected un access en VLAN 20 sans VLAN tagged", st["Port1"])
	}
}

// TestFiltrageVLANRapporte : « disabled » doit se voir.
//
// Un port dont le filtrage VLAN est off porte une configuration VLAN INERTE : le
// PVID et l'appartenance sont bien there, mais le port ne les applique pas. Taire cette
// distinction ferait present  une configuration comme active alors qu'elle ne fait
// rien — et c'est le genre de silence qui fait chercher une panne ailleurs.
func TestFiltrageVLANRapporte(t *testing.T) {
	// Deux ports : le premier avec filtrage disabled, le second en « strict ».
	var rows, err = ParseTable("[{nm:'',mbr:0x03,vid:0x000a}]")
	if err != nil {
		t.Fatalf("table refused : %v", err)
	}
	var fwd *Page
	fwd, err = ParseObject("{vlan:[0x00,0x03],dvid:[0x000a,0x000a]}")
	if err != nil {
		t.Fatalf("page refused : %v", err)
	}
	var _, filtering, buildErr = portsFrom(rows, fwd, facadeCuivre(2))
	if buildErr != nil {
		t.Fatalf("reconstruction refused : %v", buildErr)
	}
	if filtering["Port1"] != "disabled" || filtering["Port2"] != "strict" {
		t.Errorf("filtrage = %v, expected Port1 disabled et Port2 strict", filtering)
	}
	// Et le mode par port sait se rendre en une liste exploitable : c'est elle qui
	// devient la note obligatoire de State.Notes (cf. stateOf). Le mode VLAN par port
	// n'est PAS un champ de state standard — il n'a rien d'universel — mais ce qu'il
	// implique doit atteindre l'exploitant.
	if !reflect.DeepEqual(filtering.Disabled(), []string{"Port1"}) {
		t.Errorf("Disabled = %v, expected [Port1]", filtering.Disabled())
	}
}

// TestReconstructionRefuseUnePageIncomplete : mieux vaut refuser que deviner.
//
// Sur cette plateforme, une write REWRITES la table entire. Une lecture qui aurait
// devine l'appartenance d'un port produirait une write qui la pose pour de vrai.
func TestReconstructionRefuseUnePageIncomplete(t *testing.T) {
	var rows, _ = ParseTable("[{nm:'',mbr:0x01,vid:0x000a}]")

	var cas = []struct {
		nom, fwd string
		ports    int
	}{
		{"sans VLAN natifs", "{vlan:[0x01]}", 1},
		{"VLAN natifs trop courts", "{vlan:[0x01,0x01],dvid:[0x000a]}", 2},
	}
	var c = cas[0]
	for _, c = range cas {
		t.Run(c.nom, func(t *testing.T) {
			var fwd, err = ParseObject(c.fwd)
			if err != nil {
				t.Fatalf("page refused : %v", err)
			}
			var _, _, buildErr = portsFrom(rows, fwd, facadeCuivre(c.ports))
			if buildErr == nil {
				t.Error("reconstruction accepted sur une page incomplete")
			}
		})
	}

	// Et un masque de membres illisible doit stop la lecture : deviner
	// l'appartenance d'un port, c'est decider pour lui.
	var mauvais, _ = ParseTable("[{nm:'',vid:0x000a}]")
	var minimal, _ = ParseObject("{vlan:[0x01],dvid:[0x000a]}")
	var _, _, err = portsFrom(mauvais, minimal, facadeCuivre(1))
	if err == nil {
		t.Error("un VLAN sans masque de membres a been accepted")
	}
}

// TestTableVLANSansIdentifiantRefusee : perdre une ligne serait perdre un VLAN.
func TestTableVLANSansIdentifiantRefusee(t *testing.T) {
	var rows, err = ParseTable("[{nm:'',mbr:0x01,vid:0x000a},{nm:'',mbr:0x02}]")
	if err != nil {
		t.Fatalf("table refused par l'analyseur : %v", err)
	}
	var _, vlanErr = vlansFrom(rows)
	if vlanErr == nil {
		t.Error("une ligne sans identifiant a been ignored en silence : la rewrite " +
			"de la table perdrait ce VLAN")
	}
}

// TestLiensDeReference : state des liens, et pourquoi un masque absent est une
// erreur.
//
// Supposer « aucun lien » leverait le garde-fou de writing sur TOUS les ports,
// i.e. l'inverse exact de la prudence.
func TestLiensDeReference(t *testing.T) {
	var link, err = ParseObject(capture(t, "link.b"))
	if err != nil {
		t.Fatalf("/link.b : %v", err)
	}
	var up, linkErr = linksFrom(link, facadeCSS326())
	if linkErr != nil {
		t.Fatalf("lecture des liens refused : %v", linkErr)
	}
	if !reflect.DeepEqual(up, map[string]bool{"SFP2": true}) {
		t.Errorf("liens actifs = %v ; la capture n'en porte qu'un, la cage SFP2 — que "+
			"the device nomme « SFP2 » et que l'ancienne front panel appelait « SFP2 »", up)
	}

	// Masque absent : erreur, pas « aucun lien ».
	var sans *Page
	sans, err = ParseObject("{prt:0x1a}")
	if err != nil {
		t.Fatalf("page refused : %v", err)
	}
	var _, e = linksFrom(sans, facadeCSS326())
	if e == nil {
		t.Error("un masque de liens absent a been lu comme « aucun lien » : le garde-fou " +
			"de writing serait leve sur tous les ports")
	}
}

// TestSNMPDeReference : /snmp.b, et l'exigence que ses champs soient present.
func TestSNMPDeReference(t *testing.T) {
	var page, err = ParseObject(capture(t, "snmp.b"))
	if err != nil {
		t.Fatalf("/snmp.b : %v", err)
	}
	var cfg, snmpErr = snmpFrom(page)
	if snmpErr != nil {
		t.Fatalf("lecture SNMP refused : %v", snmpErr)
	}
	if !cfg.Enabled {
		t.Error("l'agent est actif dans la capture (`en:0x01`)")
	}
	// La community de la capture est un motif de substitution : le secret de
	// the device de production n'est pas dans le depot. Ce qui est verified est le
	// DECODAGE, pas la valeur.
	if cfg.Community != "REDACTED-SNMP-1" {
		t.Errorf("community decodee = %q", cfg.Community)
	}
	if cfg.Contact != "" || cfg.Location != "" {
		t.Errorf("contact et localisation sont vides dans la capture, obtenu %q / %q",
			cfg.Contact, cfg.Location)
	}

	// Un champ obligatoire manquant doit stop la lecture : une write qui
	// suivrait renverrait une community vide et couperait la supervision en silence.
	var ampute *Page
	ampute, err = ParseObject("{en:0x01}")
	if err != nil {
		t.Fatalf("page refused : %v", err)
	}
	var _, e = snmpFrom(ampute)
	if e == nil {
		t.Error("une page /snmp.b sans community a been accepted")
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var digits []byte
	for i > 0 {
		digits = append([]byte{byte('0' + i%10)}, digits...)
		i /= 10
	}
	return string(digits)
}
