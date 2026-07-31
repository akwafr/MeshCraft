package swos

import (
	"strings"
	"testing"

	"meshcraft/internal/driver"
)

// La lecture de l'adressage de management, verified sur la CAPTURE REELLE de
// l'reference device.
//
// Ce test est aussi le OBSERVED qui justifie le mode d'adressage par device
// (migration 0027) : il constate, sur du hardware, qu'un CSS326 adopte dans le bon
// sous-network de management y est en adresse FIXE, alors que le plan du site dit
// « DHCP externe ». Si `iptp` cessait un jour de valoir 1 dans cette capture, ce n'est
// pas le test qu'il faudrait corriger, c'est la justification qu'il faudrait relire.

// TestMgmtAddrReel : /sys.b tel que le switch l'a servi.
func TestMgmtAddrReel(t *testing.T) {
	var page, err = ParseObject(capture(t, "sys.b"))
	if err != nil {
		t.Fatalf("/sys.b refused : %v", err)
	}
	var got = MgmtAddrOf(page)

	if got.Mode != driver.MgmtAddrModeStatic {
		t.Errorf("mode = %q, « static » expected : la capture porte iptp:0x01, et c'est "+
			"tout le point du observed", got.Mode)
	}
	if got.Address != "10.99.0.36" {
		t.Errorf("adresse = %q, « 10.99.0.36 » attendue (cip:0x2400630a en petit-boutien)",
			got.Address)
	}
	// La plateforme n'a NI passerelle NI DNS : les deux champs doivent rester vides, et
	// les remplir d'une valeur quelconque serait pire qu'un vide.
	if got.Gateway != "" || len(got.DNS) != 0 {
		t.Errorf("passerelle = %q, DNS = %v : SwOS n'en porte aucun (jeu de champs de "+
			"/sys.b enumerated par backup.swb)", got.Gateway, got.DNS)
	}
	// Absence of gateway/DNS lives on MgmtCapabilities (Gateway/DNS/Routed/IPARP),
	// not in observation notes — so the UI can localize it.
	if hasNoteAbout(got.Notes, "passerelle") {
		t.Errorf("platform capability leaked into observation notes: %v", got.Notes)
	}
	// Aucune anomalie a signaler : sur cet device `ip` et `cip` sont egales, donc
	// la note d'ecart ne doit PAS apparaitre.
	if hasNoteAbout(got.Notes, "mais repond en") {
		t.Errorf("ecart adresse configuree / en cours flagged a tort : %v", got.Notes)
	}
}

// TestMgmtAddrModes : les trois modes du selecteur, plus les deux cas where l'on ne
// conclut pas.
//
// Les deux derniers cas sont les plus importants du tableau : un firmware qui rendrait
// une quatrieme valeur, ou qui ne servirait pas le champ, ne doit produire AUCUN mode.
// Deviner « statique » ferait conserver comme intentionnelle une adresse de bail, qui
// peut changer a tout moment.
func TestMgmtAddrModes(t *testing.T) {
	var cas = []struct {
		nom       string
		iptp      uint32
		absent    bool
		expected   string
		noteParle string
	}{
		{nom: "statique", iptp: AddrStatic, expected: driver.MgmtAddrModeStatic},
		{nom: "DHCP seul", iptp: AddrDHCPOnly, expected: driver.MgmtAddrModeDHCP},
		{
			nom: "DHCP avec repli", iptp: AddrDHCPFallback,
			expected: driver.MgmtAddrModeDHCP, noteParle: "repli",
		},
		{nom: "valeur inconnue", iptp: 7, expected: "", noteParle: "non reconnu"},
		{nom: "champ absent", absent: true, expected: "", noteParle: "ne sert pas le champ"},
	}

	var c = cas[0]
	for _, c = range cas {
		var page = NewPage()
		page.SetNum(FieldCurrentIP, 0x2400630a)
		page.SetNum(FieldIP, 0x2400630a)
		if !c.absent {
			page.SetNum(FieldAddrMode, c.iptp)
		}
		var got = MgmtAddrOf(page)
		if got.Mode != c.expected {
			t.Errorf("%s : mode = %q, %q expected", c.nom, got.Mode, c.expected)
		}
		if c.noteParle != "" && !hasNoteAbout(got.Notes, c.noteParle) {
			t.Errorf("%s : aucune note contenant %q — notes = %v",
				c.nom, c.noteParle, got.Notes)
		}
	}
}

// TestMgmtAddrCourantePrimeSurConfiguree : en DHCP, c'est l'adresse EN COURS qui
// s'affiche.
//
// Le trap que ce test verrouille : rendre `ip` ferait afficher l'adresse de repli d'un
// device en bail, i.e. une adresse a laquelle il ne repond pas. C'est le
// genre de valeur qu'un exploitant recopie dans un navigateur avant de conclure a une
// panne.
func TestMgmtAddrCourantePrimeSurConfiguree(t *testing.T) {
	var page = NewPage()
	page.SetNum(FieldAddrMode, AddrDHCPFallback)
	page.SetNum(FieldIP, 0x0158a8c0)        // 192.168.88.1, le repli
	page.SetNum(FieldCurrentIP, 0x2a00630a) // 10.99.0.42, le bail
	var got = MgmtAddrOf(page)
	if got.Address != "10.99.0.42" {
		t.Errorf("adresse = %q, « 10.99.0.42 » attendue (le bail, pas le repli)", got.Address)
	}
	// Un ecart entre configuree et en cours est NORMAL en DHCP : aucune anomalie a
	// signaler, sinon toute la gamme en bail serait affichee comme incoherente.
	if hasNoteAbout(got.Notes, "mais repond en") {
		t.Errorf("ecart flagged a tort sur un device en DHCP : %v", got.Notes)
	}
}

// TestMgmtAddrIncoherenceStatique : statique ET deux adresses differentes est une
// vraie anomalie, et elle se dit.
//
// C'est le seul cas where l'ecart entre `ip` et `cip` merite d'be flagged : l'un des deux
// champs a been changed sans que l'autre suive, et write sur cette page sans l'avoir vu
// poserait une adresse que personne n'a demandee.
func TestMgmtAddrIncoherenceStatique(t *testing.T) {
	var page = NewPage()
	page.SetNum(FieldAddrMode, AddrStatic)
	page.SetNum(FieldIP, 0x2400630a)        // 10.99.0.36, configuree
	page.SetNum(FieldCurrentIP, 0x2a00630a) // 10.99.0.42, en cours
	var got = MgmtAddrOf(page)
	if !hasNoteAbout(got.Notes, "mais repond en") {
		t.Errorf("incoherence non flagged sur un device statique : %v", got.Notes)
	}
}

// TestMgmtAddrSansAdresse : une page sans aucune adresse ne fabrique rien.
func TestMgmtAddrSansAdresse(t *testing.T) {
	var page = NewPage()
	page.SetNum(FieldAddrMode, AddrStatic)
	var got = MgmtAddrOf(page)
	if got.Address != "" {
		t.Errorf("adresse = %q sur une page qui n'en porte aucune", got.Address)
	}
}

func hasNoteAbout(notes []string, motif string) bool {
	var n string
	for _, n = range notes {
		if strings.Contains(n, motif) {
			return true
		}
	}
	return false
}
