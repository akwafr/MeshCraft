package swos

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"meshcraft/internal/driver"
)

// L'adoption du contrat access, verified sans hardware.
//
// Ce que ces tests couvrent est ce qui peut l'be hors network : la CONFORMITY au
// contrat (y compris les absences, qui sont un livrable), les decisions pures que la
// pose et la lecture prennent, et les refus emis avant qu'un octet ne parte.
//
// Ce qu'ils ne couvrent pas, et qui reste la campagne write : qu'un device
// accepte. Les lectures, elles, ont been reverifiees en direct sur le CSS326-24G-2S+ de
// reference a travers le tunnel du proxy.

// TestConformiteAuContrat : ce que ce connecteur declares savoir faire.
//
// Les assertions de compilation vivent dans contract.go et suffisent au sens strict.
// Ce test-ci existe pour l'AUTRE half, celle qu'aucune assertion de compilation ne
// peut porter : les interfaces facultatives que ce connecteur NE doit PAS satisfaire.
//
// Ces absences sont des faits de la plateforme, pas des retards, et elles ont une
// consequence directe a the UI — l'appelant fait une assertion de type et refuse
// AVANT de joindre the device, en nommant le geste et la raison. Le jour where
// quelqu'un ajoutera a *Admin une method qui satisfait par accident l'une de ces
// interfaces, la couche API se mettra a proposer un bouton qui ne mene nulle part, et
// rien ne le dirait. C'est tres exactement ce que ce test attrape.
func TestConformiteAuContrat(t *testing.T) {
	var connector any = (*Driver)(nil)
	var admin any = (*Admin)(nil)

	// Ce qui EST implemented. Redondant avec les assertions de compilation, et
	// volontairement : si une refonte les deplacait ou les supprimait, ce test
	// resterait.
	var _, isConnector = connector.(driver.Connector)
	if !isConnector {
		t.Error("*Driver doit satisfaire driver.Connector : sans cela, la couche API ne " +
			"peut RIEN write sur cette gamme — ConnectorFor refuserait en disant que ce " +
			"connecteur sait decrire la plateforme mais pas l'atteindre")
	}
	var _, isIdentifier = connector.(driver.Identifier)
	if !isIdentifier {
		t.Error("*Driver doit satisfaire driver.Identifier : le realm du challenge Digest " +
			"donne le model SANS identifiant, et c'est la seule gamme du projet qui l'offre")
	}
	var _, isAdmin = admin.(driver.Admin)
	if !isAdmin {
		t.Error("*Admin doit satisfaire driver.Admin")
	}
	var _, isRaw = admin.(driver.RawReader)
	if !isRaw {
		t.Error("*Admin doit satisfaire driver.RawReader : ces pages ne sont documentees " +
			"nulle part, et on ne sait write une page qu'after avoir vu son jeu de champs")
	}
	var _, isSNMP = admin.(driver.SNMPConfigurator)
	if !isSNMP {
		t.Error("*Admin doit satisfaire driver.SNMPConfigurator : la capability vide est " +
			"le verrou en amont, pas l'absence d'interface — le jour where un snmpget repond, " +
			"la liste des versions se leve d'une ligne sans toucher au connecteur")
	}

	// Ce qui NE doit PAS l'be, avec le fait qui l'establishes.
	var _, hasAccounts = admin.(driver.AccountManager)
	if hasAccounts {
		t.Error("SwOS n'a AUCUNE notion de compte : trois champs de formulaire, une key " +
			"{pwd:''}, et aucun nom d'utilisateur dans la sauvegarde de configuration " +
			"ENTIRE de the device. Declare AccountManager ferait proposer la creation " +
			"d'un compte qui n'existe pas")
	}
	var _, hasTraps = admin.(driver.TrapPusher)
	if hasTraps {
		t.Error("/snmp.b n'a AUCUN champ de destinataire, dans aucun onglet : declare " +
			"TrapPusher ferait ouvrir 162/udp sur le proxy pour ecouter un silence")
	}
	var _, hasPersist = admin.(driver.Persister)
	if hasPersist {
		t.Error("Commit = immediate : il n'y a rien a sauver sur cette plateforme, donc " +
			"aucun filet non plus — le seul retour arriere est ApplyResult.Backup")
	}
	var _, hasMgmt = admin.(driver.MgmtMover)
	if hasMgmt {
		t.Error("« Allow From VLAN » n'accepte qu'UN seul VLAN et il n'y a qu'une adresse " +
			"de management : la bascule est un saut, pas les trois temps verifiable que " +
			"MgmtMover exige")
	}
	var _, hasWriter = admin.(driver.MgmtAddrWriter)
	if !hasWriter {
		t.Error("*Admin must satisfy driver.MgmtAddrWriter: same-VLAN iptp/ip apply " +
			"on /sys.b after backup")
	}
	var _, hasJumper = admin.(driver.MgmtJumper)
	if !hasJumper {
		t.Error("*Admin must satisfy driver.MgmtJumper: single-address leap with " +
			"GET /backup.swb before rewriting /sys.b")
	}
	var _, hasOnboard = connector.(driver.Onboarder)
	if hasOnboard {
		t.Error("un SwOS est administrable au deballage : il n'y a aucun mode de gestion " +
			"a faire abandonner")
	}
}

// TestPrefixeDePortParDefaut : vide veut dire « GE », jamais « ecris a l'aveugle ».
//
// Ce prefix ne NOMME plus rien — les ports portent les noms de the device (cf.
// naming.go) — mais il decide encore de ce que « GE12 » veut dire quand une intention
// enregistree avant ce lot arrive. Un prefix absent doit donc donner un default connu :
// refuser empecherait de relever un switch neuf, et deviner ferait rater la traduction
// d'un nom que la base porte.
func TestPrefixeDePortParDefaut(t *testing.T) {
	var sans = &Admin{}
	if sans.portPrefix() != "GE" {
		t.Errorf("prefix par default = %q, expected « GE » — la same convention que le "+
			"connecteur Instant On, pour qu'un parc mele se compare", sans.portPrefix())
	}
	var avec = &Admin{prefix: "Gi"}
	if avec.portPrefix() != "Gi" {
		t.Errorf("le prefix de la cible doit primer ; obtenu %q", avec.portPrefix())
	}
}

// TestNomDePageRefuseAvantDenvoyer : ce nom part dans une URL.
//
// Deux dangers distincts, et le same refus les couvre : une injection detournerait la
// request vers un autre endpoint de the device — et cette plateforme en a qui
// AGISSENT (/reset, /reboot) —, et un chemin inconnu ne rend pas 404 mais l'interface
// web entire en HTTP 200, ce qui costs un aller-retour pour rien.
func TestNomDePageRefuseAvantDenvoyer(t *testing.T) {
	// Ce qui est accepted : les pages de data, les pages en lecture seule marquees
	// d'un « ! », et la sauvegarde.
	var bonnes = []string{"sys.b", "/sys.b", "vlan.b", "!dhost.b", "!igmp.b", "backup.swb"}
	var nom string
	for _, nom = range bonnes {
		var clean = strings.TrimPrefix(nom, "/")
		if !rawUnit.MatchString(clean) {
			t.Errorf("%q devrait be accepted : c'est une page de cet device", nom)
		}
	}

	// Ce qui est refused. L'Admin est VIDE et sans transport : si l'un de ces noms
	// passait le control, l'appel partirait vers le network et ce test paniquerait —
	// ce qui est une way tres claire d'echouer.
	var mauvaises = []string{
		"",
		"sys",             // sans extension : ce n'est pas une page
		"index.html",      // l'interface web elle-same
		"reset",           // un chemin qui AGIT
		"../etc/passwd",   // remontee de chemin
		"sys.b?x=1",       // parameters
		"sys.b#a",         //
		"vlan.b/../reset", //
		"SYS.B",           // les pages de SwOS sont en minuscules
		"pageaunombeaucouptroplongpourenetreune.b",
	}
	var a = &Admin{}
	for _, nom = range mauvaises {
		var _, err = a.ReadRaw(context.Background(), nom)
		if err == nil {
			t.Errorf("%q a been accepted comme nom de page", nom)
		}
	}

	if a.RawKind() != "page *.b" {
		t.Errorf("RawKind = %q ; the UI doit pouvoir dire a l'exploitant ce qu'« une "+
			"unite » veut dire ici, sinon la sonde de mise au point lui demandera une "+
			"section wcd devant un MikroTik", a.RawKind())
	}
}

// TestRetraitCalculeEtSesRefus : la pose CONVERGE, et ce qu'elle refuse de retirer se
// lit.
//
// La first livraison ne retirait rien : sur SwOS, appartenance et tagging sont la
// same data, et retirer un port de la liste des membres coupe ce VLAN sur ce port. Ce
// que « ajout seulement » coutait ne se voyait pas — un device annonce « conforme »
// pouvait transporter pendant des mois un VLAN que le plan avait retire, et une
// segmentation qui fuit ne se remarque qu'en cherchant autre chose.
//
// Le retrait est donc calcule, avec ses refus. Eprouve sur la real capture : les
// 24 ports cuivre de l'reference device sont en VLAN natif 100 et membres du 99
// ET du 100, les deux cages en natif 1 et membres des deux, et SEULE la cage SFP2 porte
// un lien.
//
// # CE TEST A CHANGED DE CAMP, ET C'EST LE FOND DU LOT
//
// Il exigeait qu'un VLAN de management ne soit JAMAIS retire, y compris d'un port sans
// lien. Cette politique-there rendait la convergence impossible sur cet device : son
// VLAN 99 est membre des vingt-six ports, donc une pose « les 24 ports cuivre en access
// VLAN 100 » ecrivait zero port et refusait vingt-quatre fois, a chaque passage
// (mesure le 29 juillet 2026 : `ports_written` vide, 48 ecarts irreductibles). Et le
// refus coutait la security qu'il pretendait servir : le VLAN d'administration restait
// joignable par tagging depuis n'importe quelle prise access.
//
// Le critere est now le LIEN, comme pour les autres garde-fous de ce fichier.
func TestRetraitCalculeEtSesRefus(t *testing.T) {
	var current, rows = planDeReference(t)
	var protege = map[int]string{99: "VLAN de management declared par the device"}

	var table, _, err = withVLANs(rows, []VLAN{{ID: 20}}, 26, nil)
	if err != nil {
		t.Fatalf("completion refused : %v", err)
	}

	// PORT SANS LIEN : il perd les DEUX, management compris. Aucune session ne traverse
	// une prise vide, et c'est ce retrait qui fait d'un access port un vrai port
	// access.
	var changes, skipped, planErr = planDe(current, table,
		map[string]PortState{"Port1": {Mode: "access", PVID: 20}}, facadeCSS326(), protege, false)
	if planErr != nil {
		t.Fatalf("plan refused : %v", planErr)
	}
	if len(changes) != 1 {
		t.Fatalf("%d changement(s), expected 1 : %+v", len(changes), changes)
	}
	if !reflect.DeepEqual(changes[0].Leave, []int{99, 100}) {
		t.Errorf("VLAN a quitter = %v, expected [99 100] : le plan ne demande plus que le "+
			"20, et ce port ne porte AUCUN lien — laisser le VLAN d'administration membre "+
			"d'un access port le rend joignable par tagging depuis n'importe quel poste",
			changes[0].Leave)
	}
	if len(skipped) != 0 {
		t.Errorf("aucun refus expected sur un port sans lien : %v", skipped)
	}

	// PORT AVEC LIEN : la cage SFP2 est la seule active de la capture, et c'est par elle
	// que l'administration arrive. Elle GARDE le 99, avec son motif — SwOS ne dit pas
	// lequel de ses ports porte la session.
	changes, skipped, planErr = planDe(current, table,
		map[string]PortState{"SFP2": {Mode: "access", PVID: 20}}, facadeCSS326(), protege, false)
	if planErr != nil {
		t.Fatalf("plan refused : %v", planErr)
	}
	if len(skipped) != 1 {
		t.Fatalf("%d refus, expected 1 (le VLAN de management sur un port actif) : %v",
			len(skipped), skipped)
	}
	var expected string
	for _, expected = range []string{"SFP2", "99", "MANAGEMENT", "ACTIF", "forcer"} {
		if !strings.Contains(skipped[0], expected) {
			t.Errorf("le refus doit contenir %q ; obtenu : %q", expected, skipped[0])
		}
	}
	var ch PortChange
	for _, ch = range changes {
		var vid int
		for _, vid = range ch.Leave {
			if vid == 99 {
				t.Error("le VLAN de management a been retire du port qui porte le lien")
			}
		}
	}

	// `force` leve ce refus-there aussi : c'est un geste explicite de l'exploitant, qui
	// connait sa topologie mieux que meshcraft.
	changes, skipped, planErr = planDe(current, table,
		map[string]PortState{"SFP2": {Mode: "access", PVID: 20}}, facadeCSS326(), protege, true)
	if planErr != nil {
		t.Fatalf("plan refused : %v", planErr)
	}
	if len(skipped) != 0 {
		t.Errorf("force doit lever le refus de retrait : %v", skipped)
	}
	if len(changes) != 1 || !reflect.DeepEqual(changes[0].Leave, []int{99, 100}) {
		t.Errorf("avec force, SFP2 devait quitter 99 et 100 : %+v", changes)
	}

	// Le VLAN 1 n'est JAMAIS retire : il n'a pas de ligne dans la table (cf.
	// implicitVLAN), il n'y a donc rien a en retirer.
	changes, _, planErr = planDe(current, rows,
		map[string]PortState{"Port1": {Mode: "trunk", PVID: 1, Tagged: []int{99, 100}}},
		facadeCSS326(), protege, false)
	if planErr != nil {
		t.Fatalf("plan refused : %v", planErr)
	}
	for _, ch = range changes {
		var vid int
		for _, vid = range ch.Leave {
			if vid == 1 {
				t.Errorf("%s : le VLAN 1 n'est pas dans la table, il n'y a rien a en "+
					"retirer", ch.Port)
			}
		}
	}

	// RIEN a retirer quand le plan demande au moins ce que le port porte already.
	changes, skipped, planErr = planDe(current, rows, map[string]PortState{
		"SFP2": {Mode: "trunk", PVID: 100, Tagged: []int{99}},
	}, facadeCSS326(), protege, false)
	if planErr != nil {
		t.Fatalf("plan refused : %v", planErr)
	}
	if len(skipped) != 0 {
		t.Errorf("aucun retrait n'was necessary : %v", skipped)
	}
	for _, ch = range changes {
		if len(ch.Leave) != 0 {
			t.Errorf("%s : rien a retirer, obtenu %v", ch.Port, ch.Leave)
		}
	}

	// Unresolved « all » (`TaggedAtLeast`) ne justifie AUCUN retrait tagged — le
	// plan de site n'existe pas encore. Un « all » resolu porte la liste exacte
	// et retire les extras (cf. autoapply).
	changes, skipped, planErr = planDe(current, rows, map[string]PortState{
		"Port1": {Mode: "trunk", PVID: 100, Tagged: []int{99}, TaggedAtLeast: true},
	}, facadeCSS326(), protege, false)
	if planErr != nil {
		t.Fatalf("plan refused : %v", planErr)
	}
	if len(skipped) != 0 {
		t.Errorf("unresolved all ne demande aucun retrait, et n'a donc aucun refus a "+
			"expliquer : %v", skipped)
	}
	for _, ch = range changes {
		if len(ch.Leave) != 0 {
			t.Errorf("unresolved all ne retire rien ; %s quitterait %v", ch.Port, ch.Leave)
		}
	}

	// Un port NON MANAGED ne dit rien de ce qu'il doit porter : on n'y touche pas.
	changes, skipped, planErr = planDe(current, rows, map[string]PortState{
		"Port1": {Mode: "libre"}, "Port2": {},
	}, facadeCSS326(), protege, false)
	if planErr != nil {
		t.Fatalf("plan refused : %v", planErr)
	}
	if len(changes) != 0 || len(skipped) != 0 {
		t.Errorf("« absent = non managed » : %+v / %v", changes, skipped)
	}
}

// TestAucunRetraitSurUnPortActifQuandLeVLANDadminEstInconnu : le refus n° 2.
//
// `avln` a 0 veut dire « administration accepted depuis n'importe quel VLAN », state
// d'usine. Rien ne dit alors quel port porte la session, et retirer une appartenance de
// celui qui la porte la coupe — sur un model sans SSH, sans Telnet et sans serial port.
func TestAucunRetraitSurUnPortActifQuandLeVLANDadminEstInconnu(t *testing.T) {
	var current, rows = planDeReference(t)
	current.MgmtVLAN = 0

	// SFP2 porte le lien dans la capture, et il est membre de 99 et 100.
	var changes, skipped, err = planDe(current, rows, map[string]PortState{
		"SFP2": {Mode: "trunk", PVID: 1, Tagged: []int{99}},
	}, facadeCSS326(), map[int]string{}, false)
	if err != nil {
		t.Fatalf("plan refused : %v", err)
	}
	var ch PortChange
	for _, ch = range changes {
		if len(ch.Leave) != 0 {
			t.Errorf("aucun retrait ne doit be calcule sur un port ACTIF quand le VLAN "+
				"d'administration est inconnu ; %s quitterait %v", ch.Port, ch.Leave)
		}
	}
	if len(skipped) != 1 ||
		!strings.Contains(strings.ToLower(skipped[0]), "n'importe quel vlan") {
		t.Fatalf("le refus doit expliquer POURQUOI on ne sait pas : %v", skipped)
	}
}

// TestNotesDeLetat : ce que le model standard ne sait pas dire.
//
// State.Notes est OBLIGATOIRE sur cette plateforme, et pour un fait precise : un port
// dont le mode VLAN est « disabled » ne filtre RIEN. Son PVID et son appartenance sont
// configures et sans effet. Sans la note, the UI presenterait une configuration
// INERTE comme une segmentation en place.
func TestNotesDeLetat(t *testing.T) {
	var a = &Admin{}
	var snap = snapshotDeReference(t)

	// L'reference device, tel qu'il est : tous ses ports sont en mode
	// « optional » (/fwd.b `vlan:[0x01,…]`, reverifie en direct). Aucune note de
	// filtrage inerte ne doit donc apparaitre — une note qui crierait au loup sur un
	// device sain ferait ignorer les suivantes.
	var st, err = a.stateOf(snap)
	if err != nil {
		t.Fatalf("etat refused : %v", err)
	}
	var note string
	for _, note = range st.Notes {
		if strings.Contains(note, "DISABLED") {
			t.Errorf("aucun port de la capture n'a le filtrage disabled : %q", note)
		}
	}
	if st.MgmtVLAN != 99 {
		t.Errorf("VLAN de management = %d, expected 99", st.MgmtVLAN)
	}

	// Le same device avec le filtrage off sur son premier port.
	snap = snapshotDeReference(t)
	var modes, ok = snap.fwd.Nums(FieldVLANMode)
	if !ok {
		t.Fatalf("/fwd.b ne porte pas « %s »", FieldVLANMode)
	}
	modes[0] = VLANModeDisabled
	snap.fwd.SetNums(FieldVLANMode, modes)

	st, err = a.stateOf(snap)
	if err != nil {
		t.Fatalf("etat refused : %v", err)
	}
	var trouvee string
	for _, note = range st.Notes {
		if strings.Contains(note, "DISABLED") {
			trouvee = note
		}
	}
	if trouvee == "" {
		t.Fatalf("aucune note sur un port au filtrage off : the UI presenterait une "+
			"configuration inerte comme une segmentation en place. Notes = %v", st.Notes)
	}
	var expected string
	for _, expected = range []string{"Port1", "SANS EFFET"} {
		if !strings.Contains(trouvee, expected) {
			t.Errorf("la note doit contenir %q ; obtenu : %q", expected, trouvee)
		}
	}
	// Et le port reste lu : la note ne remplace pas state, elle le qualifie.
	if st.Ports["Port1"].PVID != 100 {
		t.Errorf("Port1 doit rester lu tel que the device le porte : %+v", st.Ports["Port1"])
	}
}

// TestNoteQuandLadministrationVientDeNimporteQuelVLAN : zero n'est pas « inconnu »
// ici, et l'appelant n'a pas a le savoir.
//
// C'est l'factory state d'un CSS : `avln` a 0 veut dire « administration accepted depuis
// n'importe quel VLAN ». Sur l'Instant On, le same zero veut dire « inconnu » et fait
// REFUSER writing. Le contrat interdit donc a la couche API d'interpreter ce champ
// elle-same — c'est au connecteur de traduire, et une phrase est le seul canal planned.
func TestNoteQuandLadministrationVientDeNimporteQuelVLAN(t *testing.T) {
	var a = &Admin{}
	var snap = snapshotDeReference(t)
	var sys, err = ParseObject("{avln:0x00}")
	if err != nil {
		t.Fatalf("page refused : %v", err)
	}
	snap.sys = sys

	var st, stateErr = a.stateOf(snap)
	if stateErr != nil {
		t.Fatalf("etat refused : %v", stateErr)
	}
	if st.MgmtVLAN != 0 {
		t.Errorf("VLAN de management = %d, expected 0", st.MgmtVLAN)
	}
	var trouvee bool
	var note string
	for _, note = range st.Notes {
		if strings.Contains(note, "N'IMPORTE QUEL VLAN") {
			trouvee = true
		}
	}
	if !trouvee {
		t.Errorf("l'factory state doit be explique, pas laisse a interpreter : %v", st.Notes)
	}
}

// TestIdentiteDuContrat : la traduction vers le type que la couche API consomme.
//
// Le type de ce paquet est plus riche, et c'est le contrat qui le demande. Ce qui
// compte ici est que rien d'UTILE ne se perde en route — au premier rang la front panel,
// sans laquelle une adoption produirait un device SANS AUCUN PORT, ce qui n'est
// pas « vierge » mais faux.
func TestIdentiteDuContrat(t *testing.T) {
	var id = Identity{
		Model: "CSS326-24G-2S+", Family: "CSS", Serial: "945809C232D5",
		MAC: "b8:69:f4:5f:14:a8", Firmware: "2.17", Build: "1725372990",
		Hostname: "lan-m6", Revision: "", BoardName: "",
		CopperPorts: 24, SFPPorts: 2, SFPFirst: 24, SFPPlus: true,
		MgmtIP: "10.99.0.36", MgmtVLAN: 99, Source: "sys.b",
	}
	var out = id.Contract()

	var cas = []struct {
		champ, got, expected string
	}{
		{"model", out.Model, "CSS326-24G-2S+"},
		{"gamme", out.Family, "CSS"},
		{"serial", out.Serial, "945809C232D5"},
		{"MAC", out.MAC, "b8:69:f4:5f:14:a8"},
		{"firmware", out.Firmware, "2.17"},
		{"nom systeme", out.Hostname, "lan-m6"},
		{"source", out.Source, "sys.b"},
		// MikroTik n'a pas de reference catalogue distincte du label : « CSS326-24G-2S+ »
		// EST la reference. Recopier le model ici donnerait deux champs identiques dont
		// l'un ferait croire a une seconde source.
		{"reference catalogue", out.ModelCode, ""},
	}
	var c = cas[0]
	for _, c = range cas {
		if c.got != c.expected {
			t.Errorf("%s = %q, expected %q", c.champ, c.got, c.expected)
		}
	}
	if out.CopperPorts != 24 || out.SFPPorts != 2 || !out.SFPPlus || out.PoE {
		t.Errorf("front panel perdue a la traduction : %+v", out)
	}
	// La front panel au format que le frontend consomme : c'est elle qui permet de dessiner
	// the device sans catalogue de modeles.
	if !reflect.DeepEqual(out.PortGroups, id.PortGroups()) {
		t.Errorf("groupes de ports = %v, expected %v", out.PortGroups, id.PortGroups())
	}
	// Les cages ont leur PROPRE compteur — « SFP1 », « SFP2 » — comme the device les
	// nomme. Voir TestGroupesDeFacadeNommesCommeLEquipement pour le raisonnement.
	if len(out.PortGroups) != 2 || out.PortGroups[1]["prefix"] != SFPPortPrefix ||
		out.PortGroups[1]["from"] != 1 {
		t.Errorf("les cages doivent be named « SFP1 » et « SFP2 » : %v", out.PortGroups)
	}
}

// snapshotDeReference reconstruit the snapshot des quatre pages de state a partir
// des captures REELLES de the device, sans network.
//
// Rendu FRAIS a chaque appel : les tests qui l'emploient modifient les pages pour
// eprouver un cas, et une page partagee ferait dependre un test de l'ordre
// d'execution des autres.
func snapshotDeReference(t *testing.T) *snapshot {
	t.Helper()
	// La front panel accompagne la geometry, comme le fait readAll sur the device : c'est
	// elle qui NOMME les ports, et un snapshot sans elle ne rendrait aucun port du tout.
	var out = &snapshot{ports: 26, sfp: 2, sfpFirst: 24, facade: facadeCSS326()}
	var err error
	out.link, err = ParseObject(capture(t, "link.b"))
	if err != nil {
		t.Fatalf("/link.b : %v", err)
	}
	out.fwd, err = ParseObject(capture(t, "fwd.b"))
	if err != nil {
		t.Fatalf("/fwd.b : %v", err)
	}
	out.sys, err = ParseObject(capture(t, "sys.b"))
	if err != nil {
		t.Fatalf("/sys.b : %v", err)
	}
	out.vlanRows, err = ParseTable(capture(t, "vlan.b"))
	if err != nil {
		t.Fatalf("/vlan.b : %v", err)
	}
	return out
}
