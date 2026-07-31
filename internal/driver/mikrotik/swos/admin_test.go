package swos

import (
	"strings"
	"testing"
)

// Le compte d'administration de SwOS, et le digest maison qui porte son secret.
//
// C'est le seul endroit de ce connecteur where une write est verified par une SECONDE
// implementation rather que par une relecture : l'algorithme du mot de passe est trop
// particulier — et son failure trop couteux, ce model n'ayant pas de serial port — pour
// se contenter de verify le code contre lui-same.

// TestCondenseDeMotDePasse : l'algorithme, verified par une SECONDE voie.
//
// Les valeurs attendues ne sont pas celles que ce code produit : elles ont been
// computed par une reimplementation LITTERALE en Python des fonctions `sa` et `ra`
// du JavaScript de the device, independante de l'implementation Go. C'est ce qui
// donne sa valeur au test — verify un code contre lui-same ne prouve rien, et cette
// write n'a pas pu be exercee sur du hardware.
//
// Deux cas portent tout le risque :
//
//   - le mot de passe ancien VIDE, qui est l'factory state et celui de the device de
//     reference. La key RC4 devient alors « * », et sans ce remplacement RC4 n'aurait
//     pas de key ;
//   - les SOIXANTE-QUATRE octets de flux appliques sur trente-deux, donc deux tours.
//     S'stop a 32 produit un digest plausible que the device rejette.
func TestCondenseDeMotDePasse(t *testing.T) {
	var cas = []struct {
		nouveau, ancien, expected string
	}{
		{
			// Le cas d'un device sorti d'usine : ancien secret vide.
			"MeshCraft2026", "",
			"6459d8580c55190317531ec3ef2264ad0f479a5109cdc8e40d14998fdcbef5df",
		},
		{
			"nouveau", "ancien",
			"a170b4d0aac65e145e6c4ecf40b14412e14c4d32ad24efcd2fcdd3724a17b19c",
		},
		{
			// Les deux vides : effacer le mot de passe d'un device neuf.
			"", "",
			"6414bd2b64166b6271272cf3dd1464ad0f479a5109cdc8e40d14998fdcbef5df",
		},
		{
			"aB3", "xY",
			"07e8c5bbc5786ddd0452b367ebfb4c7953b5db5c0c1aedbf727f36d0c6cc7d7b",
		},
	}
	var c = cas[0]
	for _, c = range cas {
		var got, err = PasswordBlob(c.nouveau, c.ancien)
		if err != nil {
			t.Errorf("PasswordBlob(%q, %q) : %v", c.nouveau, c.ancien, err)
			continue
		}
		if got != c.expected {
			t.Errorf("PasswordBlob(%q, %q) =\n  %s\nattendu (calcul independant depuis le "+
				"JavaScript de the device) :\n  %s", c.nouveau, c.ancien, got, c.expected)
		}
		// Le digest fait toujours 32 octets, donc 64 caracteres hexadecimaux.
		if len(got) != 64 {
			t.Errorf("digest de %d caracteres, expected 64", len(got))
		}
	}
}

// TestMotDePasseRefuseAvantEnvoi : refuser ici, pas sur the device.
//
// Les deux contraintes sont observed dans l'interface de the device, et les deux se
// paient d'un deplacement si on les ignore : ce model n'a pas de serial port, et un
// secret applied qui n'est pas celui qu'on croit n'est pas recuperable.
func TestMotDePasseRefuseAvantEnvoi(t *testing.T) {
	// Plus de 15 caracteres : l'interface refuse (« New password is too long »).
	var _, err = PasswordBlob("0123456789abcdef", "")
	if err == nil {
		t.Error("un secret de 16 caracteres a been accepted")
	}
	if err != nil && !strings.Contains(err.Error(), "15") {
		t.Errorf("le refus doit citer la borne de the device ; obtenu : %v", err)
	}
	// L'ancien secret est borne de la same way (« Bad old password »).
	if _, err = PasswordBlob("court", "0123456789abcdef"); err == nil {
		t.Error("un ancien secret de 16 caracteres a been accepted")
	}

	// Hors ASCII : l'interface de the device construit ses octets avec
	// `charCodeAt`, qui rend des unites UTF-16. Au-dela de 127, ce qu'enverrait un
	// navigateur et ce qu'enverrait Go divergent, et le secret applied n'est pas celui
	// qu'on croit.
	if _, err = PasswordBlob("motdepassé", ""); err == nil {
		t.Error("un secret non ASCII a been accepted : le mot de passe applied ne serait pas " +
			"celui qu'on croit, et cette plateforme n'a pas de console de secours")
	}
	if err != nil && !strings.Contains(strings.ToUpper(err.Error()), "ASCII") {
		t.Errorf("le refus doit nommer la contrainte ; obtenu : %v", err)
	}
	// La ponctuation ASCII, elle, passe : la contrainte n'est pas l'alphanumerique.
	if _, err = PasswordBlob("a!B#c$3", ""); err != nil {
		t.Errorf("la ponctuation ASCII doit be accepted : %v", err)
	}
	// Et la capability dit la same chose que le code.
	var admin = (&Driver{}).Capabilities("CSS326-24G-2S+").Admin
	if admin.AlphanumericOnly {
		t.Error("AlphanumericOnly est faux sur cette plateforme : la contrainte est l'ASCII")
	}
	if admin.PasswordMax != 15 {
		t.Errorf("PasswordMax = %d, l'interface de the device refuse au-dela de 15",
			admin.PasswordMax)
	}
}

// TestCompteFixeSurSwOS : il n'y a rien a nommer, et c'est un fait.
//
// SwOS n'a aucune notion de compte : le formulaire de changement de mot de passe ne
// porte que trois champs, la page qui le receives une seule key, et la sauvegarde de
// configuration ENTIRE de the device aucun nom d'utilisateur (verified dans
// TestSauvegardeReelle).
//
// Consequence pour meshcraft : le nom unifie du parc ne peut PAS be applied ici, et
// c'est EffectiveUsername qui doit le resoudre. Le verify ici evite qu'un
// provisionnement enregistre en base un nom de compte qui n'existe sur aucun
// device.
func TestCompteFixeSurSwOS(t *testing.T) {
	var admin = (&Driver{}).Capabilities("CSS326-24G-2S+").Admin
	if admin.Custom {
		t.Error("SwOS ne sait pas create de compte named")
	}
	if admin.FixedName != "admin" {
		t.Errorf("FixedName = %q, expected « admin »", admin.FixedName)
	}
	// C'est ce que l'appelant doit actually enregistrer, quel que soit le souhait.
	var got = admin.EffectiveUsername("mcadmin")
	if got != "admin" {
		t.Errorf("EffectiveUsername(« mcadmin ») = %q ; le nom unifie du parc n'est pas "+
			"posable sur SwOS, et c'est « admin » qui atterrit sur the device", got)
	}
	// Un mot de passe VIDE est l'factory state, et il doit rester declarable : refuser
	// zero empecherait d'adopter un CSS neuf.
	if admin.PasswordMin != 0 {
		t.Errorf("PasswordMin = %d ; SwOS accepte un secret vide, observed sur "+
			"l'reference device", admin.PasswordMin)
	}
}
