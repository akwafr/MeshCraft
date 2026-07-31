package swos

import (
	"reflect"
	"strings"
	"testing"
)

// Le chemin write, verified sur les captures REELLES de the device.
//
// AUCUNE de ces ecritures n'a been executee contre un switch : l'outil disponible pour
// atteindre the device a travers le tunnel est en lecture seule, par decision
// d'architecture du projet. Ce que ces tests establish est donc precisely ce
// qu'ils peuvent establish, et rien de plus :
//
//   - le CORPS produit, a l'octet pres, a partir de la page que the device a
//     actually servie ;
//   - le fait que le jeu de champs renvoye soit exactement celui que the device
//     stocke — la property qui empeche une write de VLAN d'emporter avec elle
//     l'isolation de ports ;
//   - les garde-fous, qui decident de ce qu'on does not write PAS.
//
// Ce qu'ils n'establish pas : que the device accepte. C'est la campagne write
// de l'orchestrateur.

// fwdDeReference lit /fwd.b tel que the device l'a servi.
func fwdDeReference(t *testing.T) *Page {
	t.Helper()
	var page, err = ParseObject(capture(t, "fwd.b"))
	if err != nil {
		t.Fatalf("/fwd.b : %v", err)
	}
	return page
}

// TestCorpsFwdRenvoieLeJeuComplet : LA property de surete de ce connecteur.
//
// SwOS n'accepte pas write partielle : son interface recompose l'objet ENTIER de
// la page et le poste tel quel. Renvoyer /fwd.b sans son tableau `srt` laisserait donc
// the device libre de remettre a zero les limites de storm de ses vingt-six ports
// en posant un VLAN — et il le ferait en silence, cette plateforme n'ayant aucun
// message d'erreur.
//
// Le test compare donc le corps produit au jeu de champs que the device STOCKE, tel
// que sa propre sauvegarde de configuration l'enumerates.
func TestCorpsFwdRenvoieLeJeuComplet(t *testing.T) {
	var fwd = fwdDeReference(t)

	// Un VLAN natif changed sur le port 1, comme le ferait ApplyPorts.
	var dvid, ok = fwd.Nums(FieldDefaultVLAN)
	if !ok {
		t.Fatalf("/fwd.b ne porte pas %q", FieldDefaultVLAN)
	}
	dvid[0] = 42
	fwd.SetNums(FieldDefaultVLAN, dvid)

	var body, missing = fwd.Only(FwdWritableFields(26))
	if len(missing) > 0 {
		t.Fatalf("champs manquants dans la page servie par the device : %v — "+
			"writing doit be REFUSED dans ce cas, pas truncated", missing)
	}

	// Le jeu de champs doit be exactement celui de la sauvegarde de the device.
	var backup, backupErr = ParseBackup(capture(t, "backup.swb"))
	if backupErr != nil {
		t.Fatalf("/backup.swb : %v", backupErr)
	}
	var stored *Page
	stored, ok = backup.Obj("fwd.b")
	if !ok {
		t.Fatal("la sauvegarde ne porte pas /fwd.b")
	}
	if !reflect.DeepEqual(body.Keys(), stored.Keys()) {
		t.Errorf("le corps produit ne porte pas le same jeu de champs que ce que "+
			"the device stocke.\n  produit : %v\n  stocke  : %v", body.Keys(), stored.Keys())
	}

	// Le changement est bien there, et RIEN d'autre n'a bouge : les 25 autres VLAN
	// natifs, les masques d'isolation, les limites de storm et les rates ingress
	// sont ceux de la page lue.
	var encoded = body.Encode()
	if !strings.Contains(encoded, "dvid:[0x2a,0x64,0x64") {
		t.Errorf("le VLAN natif du port 1 devait passer a 0x2a en laissant les autres a "+
			"0x64 ; corps : %s", excerpt(encoded, 400))
	}
	// `srt` (limites de storm) doit be intact : 26 valeurs a 0x64.
	if !strings.Contains(encoded, "srt:[0x64,0x64,0x64,0x64,0x64,0x64,0x64,0x64,0x64,0x64,"+
		"0x64,0x64,0x64,0x64,0x64,0x64,0x64,0x64,0x64,0x64,0x64,0x64,0x64,0x64,0x64,0x64]") {
		t.Error("les limites de storm ont been alterees par une write de VLAN")
	}
	// La matrice d'isolation aussi : `fp1:0x03fffffe` est « tous les ports sauf le
	// premier ».
	if !strings.Contains(encoded, "fp1:0x03fffffe") {
		t.Error("la matrice d'isolation de ports a been alteree")
	}
}

// TestEcritureRefuseeSurUnJeuDeChampsInattendu : refuser rather que d'amputer.
//
// Un firmware qui ne servirait pas un champ expected ne doit PAS conduire a une
// write partielle. C'est la consequence directe de la rule previous, et le seul
// comportement sur sur une plateforme dont les refus sont muets.
func TestEcritureRefuseeSurUnJeuDeChampsInattendu(t *testing.T) {
	var page, err = ParseObject("{fp1:0x02,dvid:[0x01,0x01],vlan:[0x01,0x01]}")
	if err != nil {
		t.Fatalf("page refused : %v", err)
	}
	var _, missing = page.Only(FwdWritableFields(2))
	if len(missing) == 0 {
		t.Fatal("aucun champ manquant flagged sur une page manifestement incomplete")
	}
	// Les champs qui protegent le trafic doivent figurer parmi les manquants : c'est
	// ce que the operator doit lire dans le message de refus.
	var joined = strings.Join(missing, " ")
	var expected string
	for _, expected = range []string{"srt", "ir", "fp2", "lck"} {
		if !strings.Contains(joined, expected) {
			t.Errorf("le champ %q devrait be flagged manquant ; obtenu %v", expected, missing)
		}
	}
}

// TestVLANCreeAvecLesDefautsDeLequipement : un VLAN created ne doit pas be created a zero.
//
// Les valeurs par default de l'interface de the device ne sont pas des zeros :
// l'isolation par VLAN et l'APPRENTISSAGE valent 1 (`u:1` dans ses definitions de
// champs). Un VLAN created avec l'apprentissage disabled inonderait tout son trafic sur
// tous ses ports membres — un default qui ne se verrait only la charge.
//
// Et le masque de membres part VIDE, alors que le default de l'interface est « tous les
// ports » (`u:268435455`) : c'est ApplyPorts qui y pose les ports voulus, et create un
// VLAN avec tous les ports membres ferait passer tout le trafic partout.
func TestVLANCreeAvecLesDefautsDeLequipement(t *testing.T) {
	var row = newVLANRow(VLAN{ID: 42, Name: "atelier"}, 26)

	// L'ordre des champs suit celui de la sauvegarde de the device.
	var stored, err = ParseBackup(capture(t, "backup.swb"))
	if err != nil {
		t.Fatalf("/backup.swb : %v", err)
	}
	var rows []*Page
	var ok bool
	rows, ok = stored.List("vlan.b")
	if !ok || len(rows) == 0 {
		t.Fatal("la sauvegarde ne porte pas la table des VLAN")
	}
	if !reflect.DeepEqual(row.Keys(), rows[0].Keys()) {
		t.Errorf("l'ordre des champs d'une ligne created differe de celui de "+
			"the device.\n  created : %v\n  lue   : %v", row.Keys(), rows[0].Keys())
	}

	var cas = []struct {
		champ  string
		attend uint32
	}{
		{FieldVLANID, 42},
		{FieldVLANMembers, 0}, // vide : ApplyPorts y pose les ports
		{"piso", 1},           // default de l'interface, pas zero
		{"lrn", 1},            // apprentissage ACTIF : a zero, le VLAN inonderait
		{"mrr", 0},
		{"igmp", 0},
	}
	var c = cas[0]
	for _, c = range cas {
		var got, has = row.Num(c.champ)
		if !has || got != c.attend {
			t.Errorf("%s = %d (ok=%v), expected %d", c.champ, got, has, c.attend)
		}
	}
	var name string
	name, ok = row.Text(FieldVLANName)
	if !ok || name != "atelier" {
		t.Errorf("nom = %q (ok=%v), expected « atelier »", name, ok)
	}
}

// TestMasqueDeMembresPrendLaFormeDuModele : un model de plus de 32 ports emploie un
// tableau de mots.
//
// Sans cela, SetBit refuserait sur la ligne created et l'appartenance ne serait pas
// posable — writing partirait avec un VLAN sans membre, donc un VLAN qui ne
// transporte rien.
func TestMasqueDeMembresPrendLaFormeDuModele(t *testing.T) {
	var petit = newVLANRow(VLAN{ID: 10}, 26)
	var v, _ = petit.Value(FieldVLANMembers)
	if v.Kind != KindNum {
		t.Errorf("jusqu'a 32 ports, le masque est un scalaire ; obtenu %v", v.Kind)
	}
	if !petit.SetBit(FieldVLANMembers, 25, true) {
		t.Error("appartenance non posable sur un masque scalaire")
	}

	var grand = newVLANRow(VLAN{ID: 10}, 48)
	var grandMasque, _ = grand.Value(FieldVLANMembers)
	if grandMasque.Kind != KindList {
		t.Errorf("au-dela de 32 ports, le masque est un tableau de mots ; obtenu %v",
			grandMasque.Kind)
	}
	if !grand.SetBit(FieldVLANMembers, 47, true) {
		t.Error("appartenance non posable sur le port 48")
	}
	var on, ok = grand.Bit(FieldVLANMembers, 47)
	if !ok || !on {
		t.Error("le bit applied sur le port 48 ne se relit pas")
	}
}

// TestTableCompleteeSansPerdreDeLigne : une table s'written ENTIRE, donc omettre une
// ligne la supprime.
//
// Les lignes existantes doivent be conservees champ pour champ, y compris celles
// qu'on ne comprend pas : l'isolation par VLAN, l'apprentissage, le miroir et le
// snooping IGMP sont regles par l'exploitant et ne sont pas a nous.
func TestTableCompleteeSansPerdreDeLigne(t *testing.T) {
	var rows, err = ParseTable(capture(t, "vlan.b"))
	if err != nil {
		t.Fatalf("/vlan.b : %v", err)
	}
	var avant = EncodeTable(rows)

	var out, created, buildErr = withVLANs(rows,
		[]VLAN{{ID: 42, Name: "atelier"}, {ID: 99}}, 26, nil)
	if buildErr != nil {
		t.Fatalf("completion refused : %v", buildErr)
	}
	// Le VLAN 99 existait already : seul le 42 est created.
	if !reflect.DeepEqual(created, []int{42}) {
		t.Errorf("VLAN created = %v, expected [42] — le 99 est already dans la table", created)
	}
	if len(out) != 3 {
		t.Fatalf("%d ligne(s) after completion, expected 3", len(out))
	}
	// Les deux lignes d'origine sont there, intactes, et l'ordre est celui des
	// identifiants — stable d'une execution a l'autre.
	var vids []uint32
	var r *Page
	for _, r = range out {
		var v, _ = r.Num(FieldVLANID)
		vids = append(vids, v)
	}
	if !reflect.DeepEqual(vids, []uint32{42, 99, 100}) {
		t.Errorf("identifiants = %v, expected [42 99 100] tries", vids)
	}
	// Les lignes d'origine n'ont pas bouge d'un octet.
	var encoded = EncodeTable(out)
	var ligne string
	for _, ligne = range strings.Split(strings.Trim(avant, "[]"), "},{") {
		ligne = strings.Trim(ligne, "{}")
		if !strings.Contains(encoded, ligne) {
			t.Errorf("une ligne d'origine a been alteree : %q absente du corps produit", ligne)
		}
	}
}

// TestTableRefuseLesBornesDeSwOS : ce que the device refuserait, on le refuse avant.
func TestTableRefuseLesBornesDeSwOS(t *testing.T) {
	var rows, _ = ParseTable(capture(t, "vlan.b"))
	var cas = []struct {
		nom string
		vid int
	}{
		{"VLAN 0", 0},
		{"VLAN 4095", 4095},
		{"VLAN 5000", 5000},
	}
	var c = cas[0]
	for _, c = range cas {
		t.Run(c.nom, func(t *testing.T) {
			var _, _, err = withVLANs(rows, []VLAN{{ID: c.vid}}, 26, nil)
			if err == nil {
				t.Errorf("VLAN %d accepted alors que la table de SwOS va de 1 a 4094", c.vid)
			}
		})
	}

	// Et la borne de 250 entries que l'interface s'impose.
	var pleine []*Page
	var i int
	for i = 1; i <= 250; i++ {
		pleine = append(pleine, newVLANRow(VLAN{ID: i}, 26))
	}
	var _, _, pleineErr = withVLANs(pleine, []VLAN{{ID: 300}}, 26, nil)
	if pleineErr == nil {
		t.Error("un 251e VLAN a been accepted : the device refuse au-dela de 250")
	}
}

// TestVLANVoulusRassembles : le plan du site ET ce que les ports reclament.
//
// Le plan VLAN du site est applied en ENTIER, pas seulement les VLAN qu'un port utilise :
// un device adopte doit connaitre les VLAN de son site, sans quoi y affecter un
// port depuis l'interface de the device — ou brancher un trunk — ne proposerait
// rien.
func TestVLANVoulusRassembles(t *testing.T) {
	var desired = map[string]PortState{
		"Port1": {Mode: "access", PVID: 20},
		"Port2": {Mode: "trunk", PVID: 1, Tagged: []int{30, 40}},
		// Un port non managed ne reclame rien.
		"Port3": {Mode: "libre", PVID: 999},
		"Port4": {},
	}
	var site = []VLAN{{ID: 10, Name: "mgmt"}, {ID: 20, Name: "users"}}
	var got = wantedVLANs(desired, site, nil)

	var expected = []VLAN{
		{ID: 1}, {ID: 10, Name: "mgmt"}, {ID: 20, Name: "users"}, {ID: 30}, {ID: 40},
	}
	if !reflect.DeepEqual(got, expected) {
		t.Errorf("VLAN voulus = %+v, expected %+v", got, expected)
	}
	// Le nom du plan du site n'est pas ecrase par le vide d'une intention de port.
	if got[2].Name != "users" {
		t.Errorf("le VLAN 20 a perdu son nom : %+v", got[2])
	}
	// Un port « libre » n'a rien reclame.
	var v VLAN
	for _, v = range got {
		if v.ID == 999 {
			t.Error("un port non managed a reclame un VLAN")
		}
	}
}

// L'ancien TestIndexDePortRefuseCeQuilNeComprendPas vit now dans naming_test.go,
// sous TestIndexApprendLesDeuxFamilles : la traduction nom → index n'est plus une
// fonction de ce fichier mais une method de [Facade], et elle accepte DEUX familles de
// noms — celle de the device (« SFP1 ») et l'ancienne numerotation continue (« GE25 »).

// TestPolitiqueDeRetraitDeclaree : ce que la pose fait ET ce qu'elle refuse doit se
// lire depuis l'exterieur du paquet.
//
// La pose RETIRE now les appartenances que le plan ne demande plus, et refuse
// quatre cas — VLAN de management, VLAN natif effectif, ports en « all », ports actifs
// d'un device dont le VLAN d'administration est inconnu. Un UI qui affiche
// « conforme » doit pouvoir dire laquelle des deux choses il annonce.
func TestPolitiqueDeRetraitDeclaree(t *testing.T) {
	var s = RemovalPolicy()
	if s == "" {
		t.Fatal("la politique de retrait doit be lisible depuis l'exterieur du paquet")
	}
	var mot string
	for _, mot = range []string{"retrait", "management", "motif"} {
		if !strings.Contains(strings.ToLower(s), mot) {
			t.Errorf("la phrase doit contenir %q ; obtenu : %q", mot, s)
		}
	}
}

// TestRefusDEcrireVLANReceiveEtForceVLAN : ce qu'on refuse to write, on le dit.
//
// `vlni` et `fvid` sont inscriptibles, et sont same already renvoyes a the device a
// chaque pose avec leur valeur lue. Ne pas les write est un CHOIX — les resserrer jette
// du trafic, et le model standard n'exprime pas ce qu'il faudrait pour decider — et un
// choix qui ne se lit pas ressemble a un oubli.
func TestRefusDEcrireVLANReceiveEtForceVLAN(t *testing.T) {
	var s = receivePolicy()
	var mot string
	for _, mot = range []string{FieldVLANReceive, FieldForceVLAN, "trafic"} {
		if !strings.Contains(s, mot) {
			t.Errorf("la phrase doit contenir %q ; obtenu : %q", mot, s)
		}
	}
}

// TestWritableExtrasVide : le vide se lit, il ne se decouvre pas sur le terrain.
func TestWritableExtrasVide(t *testing.T) {
	if len(WritableExtras) != 0 {
		t.Errorf("WritableExtras = %v ; ce connecteur ne pose que l'appartenance VLAN, "+
			"et si cela change, la declaration doit changer avec le code qui pose",
			WritableExtras)
	}
	var name string
	for _, name = range []string{"poe", "stp", "dhcp_snooping", "port_security"} {
		if WritesExtra(name) {
			t.Errorf("WritesExtra(%q) = true sans code qui la pose", name)
		}
	}
}

// --- La decision de pose, verified sur les captures reelles ---
//
// PlanPorts est pure, et c'est ce qui rend la logique la plus dangereuse du
// connecteur verifiable sans hardware : c'est elle qui decide quel port garde son VLAN
// natif, donc elle qui decide si the device restera joignable.

// planDe appelle [PlanPorts] en ne renseignant PAS le tableau des modes VLAN.
//
// Un raccourci de test, et il dit quelque chose : `Modes` a nil fait renoncer au
// wake du filtrage, donc ces tests-there eprouvent l'appartenance et le VLAN natif SEULS
// — ce qu'ils ont toujours fait. Le wake du mode a ses propres tests, where le tableau
// est fourni explicitement (cf. TestFiltrageReveilleSurUnPortEteint). Les melanger
// ferait passer un test d'appartenance pour un test de mode, et l'inverse.
func planDe(current State, table []*Page, desired map[string]PortState,
	facade Facade, protected map[int]string,
	force bool) ([]PortChange, []string, error) {
	var out, err = PlanPorts(PortsInput{
		Current: current, Table: table, Desired: desired, Facade: facade,
		Protected: protected, Force: force,
	})
	return out.Changes, out.Skipped, err
}

// planDeReference prepare state et la table de l'reference device.
func planDeReference(t *testing.T) (State, []*Page) {
	t.Helper()
	// Le mode VLAN par port est ignored ici, et ce n'est pas une perte : la decision de
	// pose ne s'en sert pas, et il n'est plus un champ de state — c'est une note
	// destinee a l'exploitant (cf. VLANFiltering et stateOf).
	var st, _ = etatDeReference(t)
	var rows, err = ParseTable(capture(t, "vlan.b"))
	if err != nil {
		t.Fatalf("/vlan.b : %v", err)
	}
	// State de l'reference device : administre en VLAN 99, et un SEUL port
	// avec lien — la cage SFP2, SFP2.
	return State{
		VLANs: []VLAN{{ID: 99}, {ID: 100}}, Ports: st,
		MgmtVLAN: 99, LinkUp: map[string]bool{"SFP2": true},
	}, rows
}

// TestPlanNeFaitRienQuandCestDejaConforme : un apply conforme ne poste RIEN.
//
// Sur cette plateforme, writing est persistante d'office et sans retour arriere : un
// POST qui ne change rien est un risque pris pour rien, et il serait pris a CHAQUE
// passage de reconciliation, sur tout le parc. L'intention demandee ici est exactement
// state lu sur the device.
func TestPlanNeFaitRienQuandCestDejaConforme(t *testing.T) {
	var current, rows = planDeReference(t)

	// On redemande a the device ce qu'il porte already.
	var desired = map[string]PortState{}
	var name string
	var p PortState
	for name, p = range current.Ports {
		desired[name] = p
	}

	var changes, skipped, err = planDe(current, rows, desired, facadeCSS326(),
		map[int]string{99: "VLAN de management declared par the device"}, false)
	if err != nil {
		t.Fatalf("plan refused : %v", err)
	}
	if len(changes) != 0 {
		t.Errorf("%d changement(s) sur un device already conforme : %+v — aucune page ne "+
			"doit be postee dans ce cas", len(changes), changes)
	}
	if len(skipped) != 0 {
		t.Errorf("des ports ont been epargnes alors que rien ne changeait : %v", skipped)
	}
}

// TestGardeFouProtegeLEtiquetageDuManagement : LE garde-fou, et il n'est pas celui de
// l'Instant On.
//
// Sur l'reference device, la session d'administration arrive TAGGED en VLAN 99
// sur la cage SFP2 (SFP2), dont le VLAN natif est 1. La rule de l'Instant On — « ne
// pas toucher au VLAN natif d'un port actif membre UNTAGGED du VLAN de management » —
// ne protegerait donc rien ici : le VLAN natif de ce port n'est pas 99.
//
// Ce qui compte sur SwOS est l'TAGGING : changer le VLAN natif d'un port fait passer
// l'ancien natif en tagged et le nouveau en untagged. Le changement est sur si et
// seulement s'il ne modifie pas l'tagging du VLAN de management sur ce port.
func TestGardeFouProtegeLEtiquetageDuManagement(t *testing.T) {
	var current, rows = planDeReference(t)
	var protege = map[int]string{99: "VLAN de management declared par the device"}

	// CAS SUR : SFP2 passe du VLAN natif 1 au VLAN natif 100. Le VLAN 99 reste tagged
	// avant comme after, la session ne bouge pas. Refuser ici rendrait l'uplink
	// impossible a configurer.
	var changes, skipped, err = planDe(current, rows,
		map[string]PortState{"SFP2": {Mode: "trunk", PVID: 100, Tagged: []int{99}}},
		facadeCSS326(), protege, false)
	if err != nil {
		t.Fatalf("plan refused : %v", err)
	}
	if len(skipped) != 0 {
		t.Errorf("passer de natif 1 a natif 100 laisse le VLAN 99 tagged : la session ne "+
			"bouge pas, cela doit be autorise. Refus : %v", skipped)
	}
	if len(changes) != 1 || changes[0].PVID != 100 {
		t.Fatalf("SFP2 devait passer en VLAN natif 100 : %+v", changes)
	}

	// CAS DANGEREUX : SFP2 passe au VLAN natif 99, celui du management. L'administration
	// sortirait alors SANS tag, et le voisin qui l'envoie tagged ne serait plus
	// entendu.
	changes, skipped, err = planDe(current, rows,
		map[string]PortState{"SFP2": {Mode: "access", PVID: 99}},
		facadeCSS326(), protege, false)
	if err != nil {
		t.Fatalf("plan refused : %v", err)
	}
	if len(changes) != 0 {
		t.Errorf("le port qui porte la session tagged a been bascule en natif 99 : %+v", changes)
	}
	if len(skipped) != 1 {
		t.Fatalf("%d motif(s) de refus, expected 1 : %v", len(skipped), skipped)
	}
	// Le motif doit be exploitable par un operator : dire quel port, que
	// l'administration y arrive TAGGED, et qu'il n'y a pas de console de secours.
	var expected string
	for _, expected = range []string{"SFP2", "lien ACTIF", "TAGGED", "serial"} {
		if !strings.Contains(skipped[0], expected) {
			t.Errorf("le motif de refus doit contenir %q ; obtenu : %q", expected, skipped[0])
		}
	}

	// `force` leve le garde-fou — c'est un geste explicite de l'exploitant, qui connait
	// sa topologie mieux que meshcraft et accepte le risque du deplacement.
	changes, skipped, err = planDe(current, rows,
		map[string]PortState{"SFP2": {Mode: "access", PVID: 99}},
		facadeCSS326(), protege, true)
	if err != nil {
		t.Fatalf("plan refused : %v", err)
	}
	if len(skipped) != 0 {
		t.Errorf("force doit lever le garde-fou ; refus : %v", skipped)
	}
	if len(changes) != 1 || changes[0].PVID != 99 {
		t.Errorf("avec force, SFP2 devait passer en VLAN natif 99 : %+v", changes)
	}
}

// TestGardeFouProtegeLeCasClassiqueNonTague : l'autre sens, celui que couvrait already la
// rule de l'Instant On.
//
// Un port actif dont l'administration arrive NON TAGGED — VLAN natif egal au VLAN de
// management — ne doit pas changer de VLAN natif : l'administration deviendrait tagged
// et access tomberait.
func TestGardeFouProtegeLeCasClassiqueNonTague(t *testing.T) {
	var current, rows = planDeReference(t)
	// On place the device dans le cas classique : SFP2 est en access dans le VLAN de
	// management, et il porte le lien.
	current.Ports["SFP2"] = PortState{Mode: "access", PVID: 99}

	var changes, skipped, err = planDe(current, rows,
		map[string]PortState{"SFP2": {Mode: "access", PVID: 100}},
		facadeCSS326(), map[int]string{99: "VLAN de management du plan du site"}, false)
	if err != nil {
		t.Fatalf("plan refused : %v", err)
	}
	if len(changes) != 0 {
		t.Errorf("le port qui porte l'administration non tagged a been modifie : %+v", changes)
	}
	if len(skipped) != 1 || !strings.Contains(skipped[0], "NON TAGGED") {
		t.Fatalf("le motif doit dire que l'administration arrive non tagged : %v", skipped)
	}
}

// TestVLAN1JamaisCreeDansLaTable : le VLAN 1 est un default IMPLICITE du hardware.
//
// OBSERVED : les deux cages de l'reference device ont un VLAN natif de 1 alors
// que sa table des VLAN ne contient que les VLAN 99 et 100. Le VLAN 1 n'y a pas de
// ligne, et il ne doit surtout pas en recevoir une — la create ferait du VLAN 1 un VLAN
// declared dont seuls les ports listes seraient membres, i.e. changerait le
// comportement de tout le trafic untagged de the device, sans que personne l'ait
// requested.
func TestVLAN1JamaisCreeDansLaTable(t *testing.T) {
	var rows, err = ParseTable(capture(t, "vlan.b"))
	if err != nil {
		t.Fatalf("/vlan.b : %v", err)
	}
	var out, created, buildErr = withVLANs(rows,
		[]VLAN{{ID: 1, Name: "default"}, {ID: 42}}, 26, nil)
	if buildErr != nil {
		t.Fatalf("completion refused : %v", buildErr)
	}
	if !reflect.DeepEqual(created, []int{42}) {
		t.Errorf("VLAN created = %v, expected [42] seulement : le VLAN 1 ne s'ajoute pas", created)
	}
	if rowFor(out, 1) != nil {
		t.Error("une ligne a been created pour le VLAN 1")
	}

	// Et un port dont le VLAN natif est 1 ne reclame AUCUNE appartenance : chercher une
	// ligne pour le VLAN 1 et se plaindre de son absence ferait echouer la pose sur un
	// device parfaitement configure.
	var current, table = planDeReference(t)
	var changes, skipped, planErr = planDe(current, table,
		map[string]PortState{"Port1": {Mode: "trunk", PVID: 1, Tagged: []int{99}}},
		facadeCSS326(), map[int]string{99: "plan"}, false)
	if planErr != nil {
		t.Fatalf("plan refused : %v", planErr)
	}
	if len(skipped) != 0 {
		t.Errorf("un VLAN natif de 1 ne doit rien faire echouer : %v", skipped)
	}
	if len(changes) != 1 || changes[0].PVID != 1 || len(changes[0].Join) != 0 {
		t.Errorf("Port1 devait seulement passer en VLAN natif 1, sans appartenance a "+
			"poser (il est already membre du 99) : %+v", changes)
	}
}

// TestPlanAutoriseLAjoutDeVLANTagueSurLePortDeSession : ce que le garde-fou NE bloque
// pas, et il ne faut pas qu'il le bloque.
//
// Ajouter un VLAN TAGGED a un port ne touche pas a son trafic untagged, donc ne coupe
// pas la session. Refuser cela aussi rendrait un uplink impossible a configurer, et
// c'est justement le port par lequel tout le site passe.
func TestPlanAutoriseLAjoutDeVLANTagueSurLePortDeSession(t *testing.T) {
	var current, rows = planDeReference(t)
	// Le VLAN 42 doit exister dans la table pour qu'un port puisse le rejoindre.
	var complete, _, err = withVLANs(rows, []VLAN{{ID: 42}}, 26, nil)
	if err != nil {
		t.Fatalf("completion refused : %v", err)
	}

	// SFP2 garde son VLAN natif de 1 et gagne le VLAN 42 tagged.
	var desired = map[string]PortState{
		"SFP2": {Mode: "trunk", PVID: 1, Tagged: []int{42, 99, 100}},
	}
	var changes, skipped, planErr = planDe(current, complete, desired, facadeCSS326(),
		map[int]string{99: "VLAN de management declared par the device"}, false)
	if planErr != nil {
		t.Fatalf("plan refused : %v", planErr)
	}
	if len(skipped) != 0 {
		t.Errorf("l'ajout d'un VLAN tagged ne touche pas au trafic untagged : cela ne "+
			"doit pas be bloque. Refus : %v", skipped)
	}
	if len(changes) != 1 {
		t.Fatalf("%d changement(s), expected 1 : %+v", len(changes), changes)
	}
	// Le port est ALREADY membre de 99 et 100 : seul le 42 doit be rejoint, et le VLAN
	// natif ne doit pas bouger.
	if !reflect.DeepEqual(changes[0].Join, []int{42}) {
		t.Errorf("VLAN a rejoindre = %v, expected [42] — le port est already membre de 99 et 100",
			changes[0].Join)
	}
	if changes[0].PVID != 0 {
		t.Errorf("le VLAN natif ne change pas : PVID = %d, expected 0 (inchange)",
			changes[0].PVID)
	}
	if changes[0].Index != 25 {
		t.Errorf("index SwOS de SFP2 = %d, expected 25", changes[0].Index)
	}
}

// TestPlanModifieLibrementUnPortSansLien : sinon « adopter » ne voudrait rien dire.
//
// Sur un device sorti d'usine, tous les ports partagent le same VLAN par default,
// qui est aussi celui de l'administration. Sans la condition de LIEN, aucun port ne
// serait jamais modifiable.
func TestPlanModifieLibrementUnPortSansLien(t *testing.T) {
	var current, rows = planDeReference(t)
	// Port1 porte le VLAN natif 100 et n'a AUCUN lien dans la capture.
	var desired = map[string]PortState{"Port1": {Mode: "access", PVID: 99}}

	var changes, skipped, err = planDe(current, rows, desired, facadeCSS326(),
		map[int]string{99: "VLAN de management declared par the device"}, false)
	if err != nil {
		t.Fatalf("plan refused : %v", err)
	}
	if len(skipped) != 0 {
		t.Errorf("un port sans lien ne peut porter aucune session : %v", skipped)
	}
	if len(changes) != 1 || changes[0].PVID != 99 || changes[0].Index != 0 {
		t.Fatalf("Port1 devait passer en VLAN natif 99 : %+v", changes)
	}
	// Il est already membre de 99 dans la capture : rien a rejoindre.
	if len(changes[0].Join) != 0 {
		t.Errorf("VLAN a rejoindre = %v, expected aucun — Port1 est already membre du 99",
			changes[0].Join)
	}
}

// TestPlanEpargneToutPortActifQuandLeVLANDadminEstInconnu : le cas de the device
// sorti d'usine.
//
// `avln` a 0 veut dire « administration accepted depuis n'importe quel VLAN ». Rien ne
// dit alors quel port porte la session, et la protection se reporte sur TOUS les ports
// actifs — plus conservateur que le cas nominal, ce qui est le bon sens.
func TestPlanEpargneToutPortActifQuandLeVLANDadminEstInconnu(t *testing.T) {
	var current, rows = planDeReference(t)
	current.MgmtVLAN = 0

	var desired = map[string]PortState{
		"SFP2":  {Mode: "access", PVID: 100}, // port actif : epargne
		"Port1": {Mode: "access", PVID: 99},  // port sans lien : modifiable
	}
	var changes, skipped, err = planDe(current, rows, desired, facadeCSS326(),
		map[int]string{}, false)
	if err != nil {
		t.Fatalf("plan refused : %v", err)
	}
	if len(skipped) != 1 || !strings.Contains(skipped[0], "SFP2") {
		t.Errorf("le port actif devait be epargne : %v", skipped)
	}
	if !strings.Contains(skipped[0], "n'importe quel VLAN") {
		t.Errorf("le motif doit expliquer POURQUOI on ne sait pas : %q", skipped[0])
	}
	if len(changes) != 1 || changes[0].Port != "Port1" {
		t.Errorf("Port1 devait rester modifiable : %+v", changes)
	}
}

// TestPlanIgnoreCeQuilNeGerePas : « absent = non managed ».
//
// meshcraft ne reclame pas la property de ce qu'il n'a jamais applied, et un nom de port
// qu'il ne comprend pas ne doit surtout pas devenir une write au hasard.
func TestPlanIgnoreCeQuilNeGerePas(t *testing.T) {
	var current, rows = planDeReference(t)
	var desired = map[string]PortState{
		"Port1": {Mode: "libre", PVID: 99},
		"Port2": {},
		"GE99":  {Mode: "access", PVID: 99}, // port inexistant sur ce model
	}
	var changes, skipped, err = planDe(current, rows, desired, facadeCSS326(),
		map[int]string{99: "plan"}, false)
	if err != nil {
		t.Fatalf("plan refused : %v", err)
	}
	if len(changes) != 0 {
		t.Errorf("aucun de ces ports ne doit be written : %+v", changes)
	}
	// Le port inexistant, en revanche, doit be SIGNALE : le taire laisserait croire
	// que le plan a been applied en entier.
	if len(skipped) != 1 || !strings.Contains(skipped[0], "GE99") {
		t.Errorf("le port inexistant doit be flagged, pas ignored en silence : %v", skipped)
	}
}
