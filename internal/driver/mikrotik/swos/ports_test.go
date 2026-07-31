package swos

import (
	"reflect"
	"strings"
	"testing"
)

// Les tests du model VLAN complet : le REVEIL du filtrage, le VLAN de management
// jamais orphelin, et les trois champs par port que la lecture taisait.
//
// Tous rejouent les captures reelles du CSS326-24G-2S+ de reference (testdata/). Ce
// qu'elles portent, et qui rend ces tests utiles rather que theoriques :
//
//	/fwd.b `vlan`  → [0x01 × 26], i.e. « optional » partout — le DEFAULT
//	                 d'usine, ni disabled ni enabled ;
//	/fwd.b `vlni`  → [0x00 × 26], « any » partout ;
//	/fwd.b `fvid`  → 0x00000000, personne ;
//	/fwd.b `dvid`  → 100 sur les 24 ports cuivre, 1 sur les deux cages ;
//	/vlan.b        → VLAN 99 et 100, masque de membres 0x03ffffff — les 26 ports
//	                 membres des deux.
//
// Aucune WRITE n'a been exercee sur ce hardware : ces tests eprouvent la decision,
// pas la reaction de the device.

// modesDeReference charge le tableau /fwd.b `vlan` de la capture.
func modesDeReference(t *testing.T) []uint32 {
	t.Helper()
	var fwd, err = ParseObject(capture(t, "fwd.b"))
	if err != nil {
		t.Fatalf("/fwd.b : %v", err)
	}
	var modes, ok = fwd.Nums(FieldVLANMode)
	if !ok || len(modes) != 26 {
		t.Fatalf("/fwd.b « %s » : %d valeur(s), 26 attendues", FieldVLANMode, len(modes))
	}
	return modes
}

// TestModeDeReferenceEstOptional : le prior de tous les tests de mode.
//
// L'reference device est en « optional » sur ses 26 ports, PAS en « disabled ».
// C'est ce qui rend le garde-fou de planModeRaise verifiable dans les deux sens : sur
// cette capture, rien ne doit be reveille.
func TestModeDeReferenceEstOptional(t *testing.T) {
	var modes = modesDeReference(t)
	var i int
	for i = range modes {
		if modes[i] != VLANModeOptional {
			t.Fatalf("/fwd.b « %s »[%d] = %d, expected %d (optional) : la capture montre "+
				"le default d'usine", FieldVLANMode, i, modes[i], VLANModeOptional)
		}
	}
}

// TestRienNEstResserreSurUnPortQuiFonctionne — LE refus qui protege un parc en
// service.
//
// `optional` → `enabled` RESSERRE un filtre sur un port qui marche : toute trame d'un
// VLAN que la table ne declares pas cesse de passer a la seconde where c'est written.
// meshcraft ne sait pas quels VLAN non declares transitent par un port, et personne ne
// lui a requested de les couper. Meme chose, en pire, pour `enabled` → `strict`, qui
// ajoute un filtre d'INGRESS.
//
// Et RIEN n'est jamais abaisse : un exploitant qui a mis `strict` l'a voulu.
func TestRienNEstResserreSurUnPortQuiFonctionne(t *testing.T) {
	var membres = map[int]bool{1: true, 99: true, 100: true}
	var protege = map[int]string{99: "plan"}
	var cas = []struct {
		nom  string
		mode uint32
	}{
		{"optional n'est pas resserre", VLANModeOptional},
		{"enabled n'est pas pousse en strict", VLANModeEnabled},
		{"strict n'est jamais abaisse", VLANModeStrict},
	}
	var c = cas[0]
	for _, c = range cas {
		t.Run(c.nom, func(t *testing.T) {
			var got = planModeRaise("SFP2", c.mode, membres, 1, true, protege, false)
			if got.Change {
				t.Errorf("mode %d changed en %d : seul `disabled` se reveille",
					c.mode, got.Mode)
			}
			if got.Refused != "" {
				t.Errorf("un mode qu'on ne touche pas n'a pas de refus a expliquer : %q",
					got.Refused)
			}
		})
	}
	// Et `force` ne doit pas non plus resserrer : le bouton « forcer » leve les
	// garde-fous de la pose, il n'autorise pas une pose que personne n'a demandee.
	var got = planModeRaise("SFP2", VLANModeOptional, membres, 1, true, protege, true)
	if got.Change {
		t.Errorf("force a resserre un port en optional : ce n'est pas ce que force veut "+
			"dire (obtenu mode %d)", got.Mode)
	}
}

// TestFiltrageReveilleSurUnPortEteint : une pose qui n'agit pas est une pose qui mene.
//
// Un port dont /fwd.b `vlan` vaut `disabled` IGNORE la table des VLAN. meshcraft
// pouvait y write l'appartenance et le VLAN natif, relire, tout comparer, afficher
// « conforme » — et le port continuait de laisser tout passer. La lecture le DISAIT
// dans une note ; aucune pose ne le corrigeait, et le seul recours de l'exploitant
// was d'aller cliquer dans l'interface de son switch.
func TestFiltrageReveilleSurUnPortEteint(t *testing.T) {
	var membres = map[int]bool{1: true, 99: true, 100: true}
	var protege = map[int]string{99: "VLAN de management declared par the device"}

	// Port SANS lien : aucune session ne passe par there, le wake est sans risque.
	var got = planModeRaise("Port1", VLANModeDisabled, membres, 100, false, protege, false)
	if !got.Change || got.Mode != VLANModeEnabled {
		t.Fatalf("un port off et sans lien doit be reveille en enabled : %+v", got)
	}

	// Port AVEC lien, membre de tout ce qui doit sortir de lui : le wake reste sur.
	got = planModeRaise("SFP2", VLANModeDisabled, membres, 1, true, protege, false)
	if !got.Change || got.Mode != VLANModeEnabled {
		t.Fatalf("un port actif membre de son natif ET du VLAN de management peut be "+
			"reveille : %+v", got)
	}
}

// TestFiltrageNonReveilleSiCaCouperait — le garde-fou du wake, dans ses deux cas.
//
// `enabled` filtre a la SORTIE : le poser sur un port qui n'est pas membre d'un VLAN
// fait tomber ce VLAN sur ce port. Sur un port qui porte un lien, cela veut dire
// couper — et si ce port porte la session, couper l'administration d'un device qui
// n'a ni SSH, ni Telnet, ni serial port.
//
// Le refus est RENDU, pas silencieux : sans lui, l'exploitant verrait « conforme » sur
// un port dont la configuration reste decorative, et n'aurait aucune piste.
func TestFiltrageNonReveilleSiCaCouperait(t *testing.T) {
	var protege = map[int]string{99: "VLAN de management declared par the device"}

	// CAS 1 : pas membre de son propre VLAN natif. Le trafic untagged du port entre et
	// ne ressort plus.
	var got = planModeRaise("SFP2", VLANModeDisabled, map[int]bool{99: true}, 100,
		true, protege, false)
	if got.Change {
		t.Errorf("le filtrage a been enabled sur un port actif non membre de son VLAN "+
			"natif : son trafic untagged serait jete (mode %d)", got.Mode)
	}
	var expected string
	for _, expected = range []string{"SFP2", "100", "SANS EFFET", "lien ACTIF"} {
		if !strings.Contains(got.Refused, expected) {
			t.Errorf("le refus doit contenir %q ; obtenu : %q", expected, got.Refused)
		}
	}

	// CAS 2 : pas membre du VLAN de MANAGEMENT. C'est le cas grave — la panne ne se
	// verrait que du cote de meshcraft, le trafic du site continuant de commuter.
	got = planModeRaise("SFP2", VLANModeDisabled, map[int]bool{100: true}, 100,
		true, protege, false)
	if got.Change {
		t.Errorf("le filtrage a been enabled sur un port actif non membre du VLAN de "+
			"management : l'administration tomberait (mode %d)", got.Mode)
	}
	for _, expected = range []string{"management", "99", "serial"} {
		if !strings.Contains(got.Refused, expected) {
			t.Errorf("le refus doit contenir %q ; obtenu : %q", expected, got.Refused)
		}
	}

	// `force` leve ce garde-fou : geste explicite de l'exploitant, qui connait sa
	// topologie mieux que meshcraft.
	got = planModeRaise("SFP2", VLANModeDisabled, map[int]bool{}, 100, true, protege, true)
	if !got.Change || got.Mode != VLANModeEnabled {
		t.Errorf("force doit lever le garde-fou du wake : %+v", got)
	}
}

// TestReveilDecideSurLAppartenanceDArrivee : la pose declares PUIS filtre, dans cet
// ordre, et le calcul doit suivre le same ordre.
//
// Le garde-fou du wake se calcule sur l'appartenance TELLE QU'ELLE SERA — la table
// est already completee quand PlanPorts decide. La juger sur l'appartenance d'AVANT
// refuserait de wake le filtrage d'un port au moment precise where la pose lui donne
// les appartenances qui le rendent sur : le port resterait a `disabled` a jamais, et
// chaque passage de reconciliation reproduirait le same refus.
func TestReveilDecideSurLAppartenanceDArrivee(t *testing.T) {
	var current, rows = planDeReference(t)
	// The device est off cote filtrage sur tous ses ports.
	var modes = make([]uint32, 26)

	// Un VLAN 42 tout neuf, et SFP2 (le port qui porte le lien) qui le rejoint tagged
	// en gardant son natif 1 et son 99.
	var table, _, err = withVLANs(rows, []VLAN{{ID: 42}}, 26, nil)
	if err != nil {
		t.Fatalf("completion refused : %v", err)
	}
	var plan, planErr = PlanPorts(PortsInput{
		Current: current, Table: table, Facade: facadeCSS326(), Modes: modes,
		Desired: map[string]PortState{
			"SFP2": {Mode: "trunk", PVID: 1, Tagged: []int{42, 99, 100}},
		},
		Protected: map[int]string{99: "VLAN de management declared par the device"},
	})
	if planErr != nil {
		t.Fatalf("plan refused : %v", planErr)
	}
	if len(plan.Skipped) != 0 {
		t.Errorf("aucun refus expected : %v", plan.Skipped)
	}
	if len(plan.Changes) != 1 {
		t.Fatalf("%d changement(s), expected 1 : %+v", len(plan.Changes), plan.Changes)
	}
	if !plan.Changes[0].RaiseMode || plan.Changes[0].Mode != VLANModeEnabled {
		t.Errorf("le filtrage devait be reveille sur SFP2 : %+v", plan.Changes[0])
	}
	if !reflect.DeepEqual(plan.Changes[0].Join, []int{42}) {
		t.Errorf("VLAN a rejoindre = %v, expected [42]", plan.Changes[0].Join)
	}
}

// TestModesAbsentsNEmpechentPasLaPose : un firmware qui ne sert pas `vlan` ne doit pas
// faire echouer une pose d'appartenance.
//
// Refuser la pose entire parce qu'on ne peut pas wake un filtrage serait refuser
// de configurer un switch parce qu'on ne peut pas tout configurer.
func TestModesAbsentsNEmpechentPasLaPose(t *testing.T) {
	var current, rows = planDeReference(t)
	var plan, err = PlanPorts(PortsInput{
		Current: current, Table: rows, Facade: facadeCSS326(), Modes: nil,
		Desired:   map[string]PortState{"Port1": {Mode: "access", PVID: 99}},
		Protected: map[int]string{99: "plan"},
	})
	if err != nil {
		t.Fatalf("plan refused faute de tableau des modes : %v", err)
	}
	if len(plan.Changes) != 1 || plan.Changes[0].PVID != 99 {
		t.Fatalf("l'appartenance doit rester posable : %+v", plan.Changes)
	}
	if plan.Changes[0].RaiseMode {
		t.Error("aucun mode ne doit be written quand le firmware ne sert pas le champ")
	}
}

// --- Le VLAN de management, jamais orphelin ---

// TestVLANDeManagementToujoursDansLaTable : le plan du site est cense le declare,
// rien ne le garantit.
//
// Un site importe d'un fichier fabrique, un device dont `avln` designe un VLAN que
// personne n'a modelise : la pose posait alors tout le reste et laissait precisely le
// VLAN par lequel elle est arrivee hors de la table.
func TestVLANDeManagementToujoursDansLaTable(t *testing.T) {
	var got = wantedVLANs(
		map[string]PortState{"Port1": {Mode: "access", PVID: 20}},
		[]VLAN{{ID: 20, Name: "users"}},
		map[int]string{7: "VLAN de management du plan du site",
			99: "VLAN de management declared par the device"})

	var expected = []VLAN{{ID: 7}, {ID: 20, Name: "users"}, {ID: 99}}
	if !reflect.DeepEqual(got, expected) {
		t.Errorf("VLAN voulus = %+v, expected %+v : les DEUX VLAN de management doivent y "+
			"be, celui du plan comme celui que the device declares", got, expected)
	}
	// Le motif destine a l'exploitant ne doit PAS devenir un nom de VLAN sur
	// the device : `nm` fait 16 octets, et personne n'a choisi ce label.
	var v VLAN
	for _, v = range got {
		if v.ID != 20 && v.Name != "" {
			t.Errorf("le VLAN %d a recu le nom %q, qui est un motif d'UI", v.ID, v.Name)
		}
	}
}

// TestLigneDeManagementCreeeAvecLesPortsActifs — le trap le moins intuitif de cette
// plateforme.
//
// Create une ligne de VLAN sur SwOS fait PASSER ce VLAN du regime « inconnu de la
// table » au regime « declared, donc filtre ». Sur un port en mode `optional` — le
// default d'usine, et state des 26 ports de la capture — un VLAN que la table ne
// declares pas circule librement ; le declare avec un masque de membres VIDE le fait
// tomber partout, instantanement.
//
// Autrement dit : la way la plus rapide de rendre un CSS muet est de create proprement
// la ligne de son VLAN de management.
func TestLigneDeManagementCreeeAvecLesPortsActifs(t *testing.T) {
	var current, rows = planDeReference(t)
	// The device se dit administre en VLAN 7, qui n'existe pas dans sa table.
	var protege = map[int]string{7: "VLAN de management declared par the device"}

	var seed = mgmtSeed(current, protege, rows, facadeCSS326())
	// SFP2 est le SEUL port qui porte un lien dans la capture (/link.b lnk:0x02000000).
	if !reflect.DeepEqual(seed, map[int][]int{7: {25}}) {
		t.Fatalf("semis = %v, expected {7: [25]} : seul SFP2 porte un lien", seed)
	}

	var out, created, err = withVLANs(rows, []VLAN{{ID: 7}}, 26, seed)
	if err != nil {
		t.Fatalf("completion refused : %v", err)
	}
	if !reflect.DeepEqual(created, []int{7}) {
		t.Fatalf("VLAN created = %v, expected [7]", created)
	}
	var row = rowFor(out, 7)
	if row == nil {
		t.Fatal("la ligne du VLAN 7 n'a pas been created")
	}
	var membre, ok = row.Bit(FieldVLANMembers, 25)
	if !ok || !membre {
		t.Error("la ligne du VLAN de management a been created SANS le port actif : sur un " +
			"port en mode optional, declare un VLAN sans membre le fait tomber — c'est " +
			"exactement le geste qui coupe access")
	}
	// Les ports SANS lien n'y entrent pas : le semis est large, pas total.
	membre, ok = row.Bit(FieldVLANMembers, 0)
	if !ok || membre {
		t.Error("Port1 ne porte aucun lien : il n'a pas a entrer dans le VLAN de management")
	}
}

// TestPasDeSemisQuandLeVLANDeManagementExisteDeja : l'appartenance applied par
// l'exploitant n'est pas a nous.
func TestPasDeSemisQuandLeVLANDeManagementExisteDeja(t *testing.T) {
	var current, rows = planDeReference(t)
	// Le VLAN 99 EST dans la table de la capture.
	var seed = mgmtSeed(current, map[int]string{99: "plan"}, rows, facadeCSS326())
	if len(seed) != 0 {
		t.Errorf("semis = %v, expected vide : le VLAN 99 est already declared, son "+
			"appartenance est celle de l'exploitant", seed)
	}
	// Le VLAN 1 n'entre JAMAIS dans la table, same comme VLAN de management : le create
	// changerait le comportement de tout le trafic untagged de the device.
	seed = mgmtSeed(current, map[int]string{1: "plan"}, rows, facadeCSS326())
	if len(seed) != 0 {
		t.Errorf("semis = %v, expected vide : le VLAN 1 est le default implicite du "+
			"hardware et n'a pas de ligne", seed)
	}
}

// --- Le garde-fou du natif FREEZES le port en entier ---

// TestUnPortEpargneNEstPasReconfigureAMoitie
//
// Une version qui refusait le seul changement de VLAN natif et continuait sur
// l'appartenance produisait exactement l'anomalie que la lecture state signale : un
// port dont le natif reste 99 et dont l'appartenance est rewritten pour le VLAN 100. Le
// trafic untagged entre alors en 99 sur un port qui n'est plus membre de 99, et rien
// n'en sort. Sur le port qui porte la session, c'est la panne exacte que le garde-fou
// existe pour empecher — il l'aurait causee lui-same.
func TestUnPortEpargneNEstPasReconfigureAMoitie(t *testing.T) {
	var current, rows = planDeReference(t)
	// SFP2 porte le lien ET l'administration non tagged : c'est le cas classique.
	current.Ports["SFP2"] = PortState{Mode: "access", PVID: 99}

	var plan, err = PlanPorts(PortsInput{
		Current: current, Table: rows, Facade: facadeCSS326(),
		Modes:     modesDeReference(t),
		Desired:   map[string]PortState{"SFP2": {Mode: "access", PVID: 100}},
		Protected: map[int]string{99: "VLAN de management du plan du site"},
	})
	if err != nil {
		t.Fatalf("plan refused : %v", err)
	}
	if len(plan.Changes) != 0 {
		t.Errorf("le port epargne ne doit recevoir AUCUNE write, pas same une "+
			"appartenance : %+v", plan.Changes)
	}
	if len(plan.Skipped) != 1 || !strings.Contains(plan.Skipped[0], "NON TAGGED") {
		t.Fatalf("un seul refus, celui du natif : %v", plan.Skipped)
	}
}

// --- La lecture : les trois champs par port que state taisait ---

// TestAucuneAnomalieSurLaCaptureReelle : le prior, et il compte.
//
// L'reference device est consistent — 26 ports membres des VLAN 99 et 100, natifs
// 100 et 1, `vlni` a « any » partout, `fvid` a zero. Une fonction d'anomalies qui
// signalerait quelque chose ici serait une fonction qui crie en permanence, donc une
// fonction qu'on apprend a ne plus lire.
func TestAucuneAnomalieSurLaCaptureReelle(t *testing.T) {
	var vlanRows, err = ParseTable(capture(t, "vlan.b"))
	if err != nil {
		t.Fatalf("/vlan.b : %v", err)
	}
	var fwd *Page
	fwd, err = ParseObject(capture(t, "fwd.b"))
	if err != nil {
		t.Fatalf("/fwd.b : %v", err)
	}
	var got = vlanAnomalies(vlanRows, fwd, facadeCSS326())
	if len(got) != 0 {
		t.Errorf("la configuration de l'reference device est consistent : %v", got)
	}
}

// TestAnomalieForceVLANID : le champ qui fait mentir toute la front panel.
//
// Un port en « Force VLAN ID » range TOUT son trafic entrant dans son VLAN natif,
// tags comprises. Ce n'est donc PAS un trunk, quoi que la table dise de son
// appartenance — et the UI affichait pourtant sa liste de VLAN tagged sans reserve.
func TestAnomalieForceVLANID(t *testing.T) {
	var vlanRows, _ = ParseTable(capture(t, "vlan.b"))
	var fwd, _ = ParseObject(capture(t, "fwd.b"))
	// Port3 (index 2) passe en Force VLAN ID.
	if !fwd.SetBit(FieldForceVLAN, 2, true) {
		t.Fatal("le masque fvid de la capture doit be modifiable")
	}

	var got = vlanAnomalies(vlanRows, fwd, facadeCSS326())
	if len(got) == 0 {
		t.Fatal("un port en Force VLAN ID doit be flagged")
	}
	var expected string
	for _, expected = range []string{"FORCE VLAN ID", "Port3", "PAS des trunks",
		"does not write jamais"} {
		if !strings.Contains(got[0], expected) {
			t.Errorf("la note doit contenir %q ; obtenu : %q", expected, got[0])
		}
	}
}

// TestAnomaliePVIDSansAppartenance : l'equivalent SwOS du default reel du CRS326.
//
// Sur SwOS, une trame ne sort d'un port que s'il est membre du VLAN. Un port dont le
// VLAN natif n'est pas dans son appartenance range son trafic untagged dans un VLAN
// hence rien ne ressort. The device l'accepte sans rien signaler.
func TestAnomaliePVIDSansAppartenance(t *testing.T) {
	var vlanRows, _ = ParseTable(capture(t, "vlan.b"))
	var fwd, _ = ParseObject(capture(t, "fwd.b"))
	// Port1 (index 0) sort du VLAN 100, qui reste son VLAN natif.
	var row = rowFor(vlanRows, 100)
	if row == nil || !row.SetBit(FieldVLANMembers, 0, false) {
		t.Fatal("le masque de membres du VLAN 100 doit be modifiable")
	}

	var got = vlanAnomalies(vlanRows, fwd, facadeCSS326())
	if len(got) != 1 {
		t.Fatalf("%d note(s), expected 1 : %v", len(got), got)
	}
	var expected string
	for _, expected = range []string{"PVID SANS APPARTENANCE", "Port1", "ne ressort nulle part"} {
		if !strings.Contains(got[0], expected) {
			t.Errorf("la note doit contenir %q ; obtenu : %q", expected, got[0])
		}
	}
}

// TestAnomalieVLANNatifAbsentDeLaTable : a ne pas confondre avec le VLAN 1.
//
// Un VLAN natif sans ligne dans /vlan.b n'a aucun membre : rien ne peut en sortir vers
// un autre port. Le VLAN 1, lui, est le default implicite du hardware et n'a
// LEGITIMATELY pas de ligne — les deux cages de la capture sont dans ce cas, et les
// signaler ferait crier la note sur tout device sorti d'usine.
func TestAnomalieVLANNatifAbsentDeLaTable(t *testing.T) {
	var vlanRows, _ = ParseTable(capture(t, "vlan.b"))
	var fwd, _ = ParseObject(capture(t, "fwd.b"))
	var dvid, ok = fwd.Nums(FieldDefaultVLAN)
	if !ok {
		t.Fatal("/fwd.b dvid illisible")
	}
	dvid[0] = 300 // Port1 en VLAN natif 300, absent de la table
	fwd.SetNums(FieldDefaultVLAN, dvid)

	var got = vlanAnomalies(vlanRows, fwd, facadeCSS326())
	if len(got) != 1 {
		t.Fatalf("%d note(s), expected 1 : %v", len(got), got)
	}
	if !strings.Contains(got[0], "VLAN NATIF ABSENT") || !strings.Contains(got[0], "Port1") {
		t.Errorf("note inattendue : %q", got[0])
	}
	// Les deux cages sont en VLAN natif 1 sans ligne : la note ne doit PAS les citer.
	if strings.Contains(got[0], "SFP1") || strings.Contains(got[0], "SFP2") {
		t.Errorf("le VLAN 1 n'a legitimately pas de ligne : %q", got[0])
	}
}

// TestAnomalieVLANReceiveIncoherent : le champ que meshcraft does not write pas mais lit.
//
// Un access port en « only tagged » jette tout son trafic — un poste branche there n'a
// rien qui passe. Un trunk en « only untagged » ne fonctionne que dans un sens. Les
// deux sont configures, actifs, et silencieux.
func TestAnomalieVLANReceiveIncoherent(t *testing.T) {
	var vlanRows, _ = ParseTable(capture(t, "vlan.b"))
	var fwd, _ = ParseObject(capture(t, "fwd.b"))
	var recv, ok = fwd.Nums(FieldVLANReceive)
	if !ok {
		t.Fatal("/fwd.b vlni illisible")
	}
	// Port1 est un trunk dans la capture (natif 100, membre du 99) : « only untagged » y
	// est incoherent.
	recv[0] = 2
	fwd.SetNums(FieldVLANReceive, recv)

	var got = vlanAnomalies(vlanRows, fwd, facadeCSS326())
	if len(got) != 1 {
		t.Fatalf("%d note(s), expected 1 : %v", len(got), got)
	}
	if !strings.Contains(got[0], "TRUNK QUI N'ACCEPTE QUE LE UNTAGGED") ||
		!strings.Contains(got[0], "Port1") {
		t.Errorf("note inattendue : %q", got[0])
	}

	// L'autre sens : un port d'ACCESS en « only tagged ». On le fabrique en retirant Port1
	// du VLAN 99, ce qui en fait un access pur au VLAN 100.
	fwd, _ = ParseObject(capture(t, "fwd.b"))
	recv, _ = fwd.Nums(FieldVLANReceive)
	recv[0] = 1
	fwd.SetNums(FieldVLANReceive, recv)
	var row = rowFor(vlanRows, 99)
	if row == nil || !row.SetBit(FieldVLANMembers, 0, false) {
		t.Fatal("le masque de membres du VLAN 99 doit be modifiable")
	}

	got = vlanAnomalies(vlanRows, fwd, facadeCSS326())
	var trouve bool
	var note string
	for _, note = range got {
		if strings.Contains(note, "PORT ACCESS QUI N'ACCEPTE QUE LE TAGGED") &&
			strings.Contains(note, "Port1") {
			trouve = true
		}
	}
	if !trouve {
		t.Errorf("un access port en « only tagged » doit be flagged : %v", got)
	}
}

// TestAnomalieStrictSansAppartenance : le mode le plus dur, sur un port qu'il rend
// muet.
//
// `strict` control l'appartenance A L'INGRESS. Un port en strict qui n'est pas membre
// de son propre VLAN natif jette son trafic untagged from la receive.
func TestAnomalieStrictSansAppartenance(t *testing.T) {
	var vlanRows, _ = ParseTable(capture(t, "vlan.b"))
	var fwd, _ = ParseObject(capture(t, "fwd.b"))
	var modes, ok = fwd.Nums(FieldVLANMode)
	if !ok {
		t.Fatal("/fwd.b vlan illisible")
	}
	modes[0] = VLANModeStrict
	fwd.SetNums(FieldVLANMode, modes)
	var row = rowFor(vlanRows, 100)
	if row == nil || !row.SetBit(FieldVLANMembers, 0, false) {
		t.Fatal("le masque de membres du VLAN 100 doit be modifiable")
	}

	var got = vlanAnomalies(vlanRows, fwd, facadeCSS326())
	var trouve bool
	var note string
	for _, note = range got {
		if strings.Contains(note, "FILTRAGE STRICT SANS APPARTENANCE") &&
			strings.Contains(note, "Port1") {
			trouve = true
		}
	}
	if !trouve {
		t.Errorf("un port en strict non membre de son natif doit be flagged : %v", got)
	}
}

// TestAppartenanceDemandeeContientLeNatif : sur SwOS, un port doit be MEMBRE du VLAN
// qui sort untagged de lui.
//
// Le confondre avec la liste des VLAN tagged est ce qui produit un PVID orphelin, donc
// l'anomalie testee juste au-dessus.
func TestAppartenanceDemandeeContientLeNatif(t *testing.T) {
	var got = askedVLANs(PortState{Mode: "trunk", PVID: 100, Tagged: []int{99, 30}})
	if !reflect.DeepEqual(got, []int{30, 99, 100}) {
		t.Errorf("appartenance demandee = %v, expected [30 99 100] triee, natif compris", got)
	}
	// Un natif a zero veut dire « inchange », jamais « VLAN 0 » : SwOS n'en a pas.
	got = askedVLANs(PortState{Mode: "trunk", Tagged: []int{99}})
	if !reflect.DeepEqual(got, []int{99}) {
		t.Errorf("appartenance demandee = %v, expected [99] : un natif a zero n'existe pas", got)
	}
	// Un VLAN a la fois natif et tagged ne compte qu'une fois.
	got = askedVLANs(PortState{Mode: "trunk", PVID: 99, Tagged: []int{99}})
	if !reflect.DeepEqual(got, []int{99}) {
		t.Errorf("appartenance demandee = %v, expected [99] sans doublon", got)
	}
}
