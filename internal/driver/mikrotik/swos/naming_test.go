package swos

import (
	"reflect"
	"strings"
	"testing"
)

// Le NOMMAGE des ports, verified sur la capture REELLE de /link.b.
//
// C'est le test qui fait foi : la convention retenue — « Port1 »… « Port24 », « SFP1 »,
// « SFP2 » — n'est pas une preference de meshcraft, c'est ce que le CSS326-24G-2S+ de
// reference sert lui-same dans le champ `nm` de sa page /link.b. Le verify octet a
// octet contre la capture est ce qui distingue « on nomme comme the device » de « on
// croit nommer comme the device ».

// facadeCSS326 est la front panel de l'reference device : 26 ports, 2 cages a partir
// de l'index 24 (`prt:0x1a`, `sfp:0x02`, `sfpo:0x18` dans la capture).
func facadeCSS326() Facade { return NewFacade(26, 2, 24, DefaultPortPrefix) }

// facadeCuivre est la front panel d'un model SANS cage, pour les cas synthetiques.
func facadeCuivre(ports int) Facade { return NewFacade(ports, 0, ports, DefaultPortPrefix) }

// TestLaGeometrieDeReferenceEstCelleDeLaCapture ancre les trois nombres dont tout le
// nommage depend. Les lire faux ferait nommer « Port25 » une cage optique.
func TestLaGeometrieDeReferenceEstCelleDeLaCapture(t *testing.T) {
	var link, err = ParseObject(capture(t, "link.b"))
	if err != nil {
		t.Fatalf("/link.b : %v", err)
	}
	var ports, sfp, first, geoErr = geometryOf(link)
	if geoErr != nil {
		t.Fatalf("geometry refused : %v", geoErr)
	}
	if ports != 26 || sfp != 2 || first != 24 {
		t.Fatalf("geometry = %d ports, %d cage(s), first a %d ; expected 26/2/24",
			ports, sfp, first)
	}
}

// TestLesNomsDePortsSontCeuxDeLEquipement : la front panel de meshcraft rend EXACTEMENT les
// noms que the device porte.
//
// La comparaison se fait contre le champ `nm` DECODE de la capture, pas contre une liste
// written a la main : c'est the device qui a raison, et une liste recopiee ne prouverait
// que la constance de la personne qui l'a recopiee.
func TestLesNomsDePortsSontCeuxDeLEquipement(t *testing.T) {
	var link, err = ParseObject(capture(t, "link.b"))
	if err != nil {
		t.Fatalf("/link.b : %v", err)
	}
	var releves, ok = link.Texts(FieldPortName)
	if !ok {
		t.Fatalf("la capture ne porte pas le champ « %s » : c'est lui qui fait foi ici",
			FieldPortName)
	}
	if len(releves) != 26 {
		t.Fatalf("%d nom(s) observed(s) dans la capture, expected 26", len(releves))
	}

	var f = facadeCSS326()
	if !reflect.DeepEqual(f.Names(), releves) {
		t.Errorf("noms de front panel = %v\nnoms observed sur the device = %v\n"+
			"les deux doivent coincider : c'est tout l'objet de ce lot", f.Names(), releves)
	}
	// Et les deux extremites, nommement, parce qu'un test qui ne compare que deux listes
	// entre elles passerait aussi si les deux were fausses de la same way.
	if releves[0] != "Port1" || releves[23] != "Port24" ||
		releves[24] != "SFP1" || releves[25] != "SFP2" {
		t.Errorf("la capture nomme ses ports %q, %q … %q, %q — ce lot a been written sur "+
			"« Port1 », « Port24 », « SFP1 », « SFP2 »",
			releves[0], releves[1], releves[24], releves[25])
	}
	// Aucun renommage sur cet device : la note state doit donc rester muette.
	var note = renamedNote(link, f)
	if note != "" {
		t.Errorf("note de renommage sur un device aux noms d'usine : %q", note)
	}
}

// TestIndexApprendLesDeuxFamilles : la traduction nom → index accepte la convention de
// the device ET l'ancienne numerotation continue.
//
// Le cas qui compte est celui des cages : the device appelle l'index 24 « SFP1 », la
// famille numerotee l'appelait « GE25 ». Les deux doivent atterrir sur 24 — sans quoi
// une intention enregistree avant ce lot poserait un VLAN sur un port cuivre.
func TestIndexApprendLesDeuxFamilles(t *testing.T) {
	var f = facadeCSS326()
	var cas = []struct {
		nom    string
		index  int
		attend bool
		pourq  string
	}{
		{"Port1", 0, true, "premier port cuivre, famille de the device"},
		{"Port24", 23, true, "dernier port cuivre"},
		{"SFP1", 24, true, "first cage : the device la nomme ainsi"},
		{"SFP2", 25, true, "seconde cage, celle qui porte le lien de la capture"},
		{"GE1", 0, true, "ancienne famille : les intentions already enregistrees"},
		{"GE24", 23, true, "ancienne famille, dernier port cuivre"},
		{"GE25", 24, true, "ancienne famille : la first cage was « GE25 »"},
		{"GE26", 25, true, "ancienne famille : la seconde cage was « GE26 »"},

		{"Port25", 0, false, "the device n'a que 24 ports cuivre : son port 25 est " +
			"une CAGE, et elle s'appelle SFP1. Accepter ce nom ferait write sur une " +
			"cage en croyant parler de cuivre"},
		{"SFP3", 0, false, "il n'y a que deux cages"},
		{"GE27", 0, false, "au-dela de la front panel"},
		{"GE0", 0, false, "la numerotation commence a 1"},
		{"port1", 0, false, "la casse n'est pas devinee : les deux familles sont " +
			"engendrees par du code"},
		{"SFP", 0, false, "un prefix sans number ne designe aucun port"},
		{"", 0, false, "le nom vide"},
		{"ether3", 0, false, "un nom d'une autre plateforme"},
		{"26", 0, false, "un number sans famille : rien ne dit laquelle"},
		{"GEabc", 0, false, "un suffixe qui n'est pas un nombre"},
		{"Port1x", 0, false, "un suffixe qui n'est pas un entier"},
		{"Port+1", 0, false, "un signe que Atoi accepterait ailleurs"},
	}
	var c = cas[0]
	for _, c = range cas {
		var got, ok = f.Index(c.nom)
		if ok != c.attend || (ok && got != c.index) {
			t.Errorf("Index(%q) = %d,%v ; expected %d,%v — %s",
				c.nom, got, ok, c.index, c.attend, c.pourq)
		}
	}
}

// TestNomEtIndexFontUnAllerRetour : sur les 26 ports, nommer puis relire doit rendre le
// same index.
//
// C'est le control qui attrape une formule dupliquee qui aurait diverge — [Facade.Name]
// et [Facade.deviceIndex] decrivent la same front panel dans les deux sens, et une erreur de
// l'une seule est exactement ce qui written sur le port voisin.
func TestNomEtIndexFontUnAllerRetour(t *testing.T) {
	var f = facadeCSS326()
	var i int
	for i = 0; i < f.Ports(); i++ {
		var back, ok = f.Index(f.Name(i))
		if !ok || back != i {
			t.Errorf("index %d named %q, relu %d (ok=%v)", i, f.Name(i), back, ok)
		}
	}
}

// TestFacadeDontLesCagesNeSontPasALaFin : `sfpo` place la first cage where il veut.
//
// Aucun model du parc ne le fait, et c'est justement pourquoi le cas est verified ici :
// la rule written est « deux compteurs independants, le cuivre compte les ports qui ne
// sont pas des cages », et elle ne se verifies pas sur un device dont les cages sont
// a la fin. Si un CSS de la gamme les placait at the top, ses prises cuivre resteraient
// numerotees a partir de 1.
func TestFacadeDontLesCagesNeSontPasALaFin(t *testing.T) {
	// 6 ports, 2 cages en TETE (index 0 et 1).
	var f = NewFacade(6, 2, 0, DefaultPortPrefix)
	var expected = []string{"SFP1", "SFP2", "Port1", "Port2", "Port3", "Port4"}
	if !reflect.DeepEqual(f.Names(), expected) {
		t.Fatalf("noms = %v, expected %v", f.Names(), expected)
	}
	var i int
	for i = 0; i < f.Ports(); i++ {
		var back, ok = f.Index(f.Name(i))
		if !ok || back != i {
			t.Errorf("index %d named %q, relu %d (ok=%v)", i, f.Name(i), back, ok)
		}
	}
	// L'ancienne famille reste continue, cages comprises : c'est ce qu'elle a toujours
	// voulu dire, et ce que la base porte.
	var got, ok = f.Index("GE3")
	if !ok || got != 2 {
		t.Errorf("Index(GE3) = %d,%v ; expected 2 : la numerotation continue ne saute pas "+
			"les cages", got, ok)
	}
}

// TestUnPortRenommeEstSIGNALE : la note qui empeche l'ecart entre deux ecrans.
//
// Le nom de front panel ne suit PAS le renommage — c'est deliberate, `nm` est du texte libre
// sans unicite et en faire une clef perdrait une intention au premier renommage. La
// consequence est qu'un exploitant peut chercher « Uplink » dans une liste qui ne le
// contient pas, et cette note est la seule chose qui l'explique.
func TestUnPortRenommeEstSIGNALE(t *testing.T) {
	// Deux ports, dont le second renomme. « 55706c696e6b » = « Uplink ».
	var link, err = ParseObject(
		"{prt:0x02,nm:['506f727431','55706c696e6b']}")
	if err != nil {
		t.Fatalf("page refused : %v", err)
	}
	var f = facadeCuivre(2)
	var diffs = renamedPorts(link, f)
	if !reflect.DeepEqual(diffs, []string{"Port2 est named « Uplink » sur the device"}) {
		t.Fatalf("ecarts observed = %v ; seul le second port est renomme", diffs)
	}
	var note = renamedNote(link, f)
	var mot string
	for _, mot = range []string{"Uplink", "Port2", FieldPortName} {
		if !strings.Contains(note, mot) {
			t.Errorf("la note ne dit pas « %s » : %q", mot, note)
		}
	}

	// Champ absent : rien a comparer, donc rien a signaler. Un firmware qui ne sert pas
	// `nm` laisse la front panel nommer seule, ce qu'elle sait faire.
	var sans *Page
	sans, err = ParseObject("{prt:0x02}")
	if err != nil {
		t.Fatalf("page refused : %v", err)
	}
	if note = renamedNote(sans, f); note != "" {
		t.Errorf("note rendue sans champ « %s » : %q", FieldPortName, note)
	}
}

// TestUnNomVideNEstPasUnRenommage : les cages d'un firmware qui ne les nomme pas.
//
// Un `nm` vide n'est pas un port renomme « rien » : c'est un champ que the device n'a
// pas rempli, et le signaler produirait une note sur chaque port de certains firmwares.
func TestUnNomVideNEstPasUnRenommage(t *testing.T) {
	var link, err = ParseObject("{prt:0x02,nm:['506f727431','']}")
	if err != nil {
		t.Fatalf("page refused : %v", err)
	}
	var diffs = renamedPorts(link, facadeCuivre(2))
	if diffs != nil {
		t.Errorf("un nom vide a been pris pour un renommage : %v", diffs)
	}
}

// TestUneIntentionSousLAncienNomTrouveQuandMemeLEtatDuPort — le trap de la traduction,
// et la raison d'be du nom canonique dans [PlanPorts].
//
// State LU est named par la front panel (« SFP2 »). Une intention enregistree avant ce lot,
// elle, designe le same port par « GE26 ». [Facade.Index] les fait tomber sur le same
// index — mais la decision de pose ne lit pas que l'index : elle cherche dans state LU
// le VLAN natif ACTUEL du port et SI CE PORT PORTE UN LIEN.
//
// Chercher avec le nom du plan rendrait donc le port inconnu : natif 0, AUCUN LIEN. Et
// « aucun lien » est tres exactement ce qui LEVE les garde-fous de ce connecteur. Une
// intention heritee aurait ainsi desarme la protection du port qui porte la session, sur
// la seule plateforme du parc qui n'a ni SSH, ni Telnet, ni serial port — i.e. que
// le geste cense be le plus prudent serait devenu le plus dangereux.
func TestUneIntentionSousLAncienNomTrouveQuandMemeLEtatDuPort(t *testing.T) {
	var current, rows = planDeReference(t)
	// SFP2 est le seul port de la capture qui porte un lien, et il porte l'administration
	// en VLAN 99 untagged : le cas classique.
	current.Ports["SFP2"] = PortState{Mode: "access", PVID: 99}
	var protege = map[int]string{99: "VLAN de management du plan du site"}

	// L'ANCIEN NOM, tel qu'une intention enregistree avant ce lot le porte.
	var plan, err = PlanPorts(PortsInput{
		Current: current, Table: rows, Facade: facadeCSS326(), Protected: protege,
		Desired: map[string]PortState{"GE26": {Mode: "access", PVID: 100}},
	})
	if err != nil {
		t.Fatalf("plan refused : %v", err)
	}
	if len(plan.Changes) != 0 {
		t.Fatalf("le port qui porte la session a recu %d write(s) alors que le plan "+
			"le designait sous son ancien nom : %+v — state du port n'a pas been retrouve, "+
			"donc le garde-fou ne s'est pas arme", len(plan.Changes), plan.Changes)
	}
	if len(plan.Skipped) != 1 {
		t.Fatalf("%d refus, expected 1 : %v", len(plan.Skipped), plan.Skipped)
	}
	// Et le refus nomme la prise comme the device le fait : c'est ce nom-there que
	// l'exploitant lit dans SwOS.
	if !strings.Contains(plan.Skipped[0], "SFP2") {
		t.Errorf("le refus doit nommer « SFP2 », le nom que porte the device : %q",
			plan.Skipped[0])
	}

	// Controle : sous le nom de la front panel, la decision est la SAME. Les deux familles
	// designent la same prise, donc elles doivent produire le same refus.
	var parFacade PortsPlan
	parFacade, err = PlanPorts(PortsInput{
		Current: current, Table: rows, Facade: facadeCSS326(), Protected: protege,
		Desired: map[string]PortState{"SFP2": {Mode: "access", PVID: 100}},
	})
	if err != nil {
		t.Fatalf("plan refused : %v", err)
	}
	if !reflect.DeepEqual(parFacade, plan) {
		t.Errorf("les deux familles ne decident pas pareil :\nGE26 → %+v\nSFP2 → %+v",
			plan, parFacade)
	}
}
