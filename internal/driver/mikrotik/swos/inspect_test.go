package swos

import (
	"reflect"
	"testing"

	"meshcraft/internal/driver"
)

// L'identification, et la bonne surprise de cette plateforme : le modèle s'obtient
// SANS le moindre identifiant.

// TestModeleDansLeRealm : le challenge Digest porte la référence exacte.
//
// C'est le challenge RÉEL de l'équipement, relevé en frappant /sys.b sans
// identifiants :
//
//	HTTP/1.1 401 Unauthorized
//	WWW-Authenticate: Digest realm="CSS326-24G-2S+", qop="auth",
//	                  nonce="84aaad9d", stale=FALSE
//
// Là où le connecteur Instant On doit fouiller les champs cachés d'une page de
// connexion pour espérer un descripteur, ici il suffit de frapper à la porte. C'est
// ce qui permet à la découverte de choisir les bonnes capabilities — donc la bonne
// géométrie de façade — avant d'avoir un secret.
func TestModeleDansLeRealm(t *testing.T) {
	var challenge = `Digest realm="CSS326-24G-2S+", qop="auth", nonce="84aaad9d", stale=FALSE`
	var model, ok = ModelFromRealm(challenge)
	if !ok || model != "CSS326-24G-2S+" {
		t.Fatalf("ModelFromRealm = %q (ok=%v), attendu CSS326-24G-2S+", model, ok)
	}
	if FamilyOf(model) != "CSS" {
		t.Errorf("FamilyOf(%q) = %q, attendu CSS", model, FamilyOf(model))
	}
}

// TestRealmInattenduRefuse : un realm qui n'est pas une référence MikroTik veut dire
// qu'on ne parle pas à un SwOS.
//
// Le dire vaut mieux que de ranger l'équipement de force dans cette gamme : les
// capabilities qui en découlent décident de ce qu'on écrit.
func TestRealmInattenduRefuse(t *testing.T) {
	var cas = []string{
		"",
		"Basic realm=\"index.html\"",
		`Digest realm="RouterOS", qop="auth", nonce="x"`,
		`Digest realm="TP-LINK", qop="auth", nonce="x"`,
		`Digest qop="auth", nonce="x"`, // pas de realm du tout
	}
	var c string
	for _, c = range cas {
		var model, ok = ModelFromRealm(c)
		if ok {
			t.Errorf("ModelFromRealm(%q) a accroché %q", c, model)
		}
	}
	// Et les deux sous-gammes que SwOS anime sont bien reconnues.
	var m, crsOK = ModelFromRealm(`Digest realm="CRS328-24P-4S+", qop="auth", nonce="x"`)
	if !crsOK || m != "CRS328-24P-4S+" || FamilyOf(m) != "CRS" {
		t.Errorf("ModelFromRealm sur un CRS = %q (ok=%v)", m, crsOK)
	}
}

// TestEnTeteInsensibleALaCasse : l'en-tête se retrouve quelle que soit la casse.
//
// `driver.Response.Headers` est une carte nue qui porte les clés dans la casse que le
// SERVEUR a envoyée. Un accès direct par « WWW-Authenticate » rendrait une chaîne
// vide le jour où un firmware capitalise autrement — c'est-à-dire ferait conclure
// « ce n'est pas un SwOS » sur un équipement qui en est un.
func TestEnTeteInsensibleALaCasse(t *testing.T) {
	var cas = []string{"WWW-Authenticate", "Www-Authenticate", "www-authenticate"}
	var k string
	for _, k = range cas {
		var h = map[string][]string{k: {`Digest realm="CSS326-24G-2S+"`}}
		var got = header(h, hdrAuthenticate)
		if got == "" {
			t.Errorf("en-tête %q non retrouvé", k)
		}
	}
	var absent = header(map[string][]string{}, hdrAuthenticate)
	if absent != "" {
		t.Errorf("en-tête absent rend %q", absent)
	}
	// Une valeur vide ne doit pas être prise pour une valeur.
	var sansValeur = header(map[string][]string{"WWW-Authenticate": {}}, hdrAuthenticate)
	if sansValeur != "" {
		t.Errorf("en-tête sans valeur rend %q", sansValeur)
	}
}

// TestIdentiteDepuisLesCapturesReelles : ce que /sys.b et /link.b ajoutent au realm.
//
// Le realm donne le modèle ; tout le reste — série, firmware, nom système, MAC,
// façade, adressage de management — exige de s'authentifier. Ce test rejoue les deux
// pages telles que l'équipement les a servies.
func TestIdentiteDepuisLesCapturesReelles(t *testing.T) {
	var sys, err = ParseObject(capture(t, "sys.b"))
	if err != nil {
		t.Fatalf("/sys.b : %v", err)
	}
	var link *Page
	link, err = ParseObject(capture(t, "link.b"))
	if err != nil {
		t.Fatalf("/link.b : %v", err)
	}

	// On part de ce que le realm a donné, comme le fait Inspect.
	var id = Identity{Model: "CSS326-24G-2S+", Family: "CSS", Source: "digest_realm"}
	fillFromSys(&id, sys)
	var fillErr = fillFromLink(&id, link)
	if fillErr != nil {
		t.Fatalf("façade illisible : %v", fillErr)
	}

	var cas = []struct {
		champ, got, attendu string
	}{
		{"modèle", id.Model, "CSS326-24G-2S+"},
		{"gamme", id.Family, "CSS"},
		{"série", id.Serial, "945809C232D5"},
		{"firmware", id.Firmware, "2.17"},
		{"nom système", id.Hostname, "lan-m6"},
		{"MAC", id.MAC, "b8:69:f4:5f:14:a8"},
		{"adresse de management", id.MgmtIP, "10.99.0.36"},
		// La source doit passer à `sys.b` : le modèle a été CONFIRMÉ à l'intérieur, et
		// l'interface ne doit pas présenter au même rang ce qui vient de la porte et
		// ce qui vient de l'intérieur.
		{"source", id.Source, "sys.b"},
	}
	var c = cas[0]
	for _, c = range cas {
		if c.got != c.attendu {
			t.Errorf("%s = %q, attendu %q", c.champ, c.got, c.attendu)
		}
	}
	if id.MgmtVLAN != 99 {
		t.Errorf("VLAN de management = %d, attendu 99", id.MgmtVLAN)
	}
	// Le firmware porte aussi son horodatage de compilation, que l'interface de
	// l'équipement affiche à côté de la version.
	if id.Build == "" {
		t.Error("l'horodatage de compilation (`bld`) n'a pas été lu")
	}
	// Ce modèle n'a ni révision ni nom commercial : la capture les sert VIDES, et il
	// ne faut rien inventer à leur place.
	if id.Revision != "" || id.BoardName != "" {
		t.Errorf("révision et nom commercial sont vides dans la capture, obtenu %q / %q",
			id.Revision, id.BoardName)
	}
	// La façade.
	if id.CopperPorts != 24 || id.SFPPorts != 2 || id.SFPFirst != 24 || !id.SFPPlus {
		t.Errorf("façade = %d cuivre, %d SFP (première à %d, SFP+ = %v) ; attendu 24/2/24/true",
			id.CopperPorts, id.SFPPorts, id.SFPFirst, id.SFPPlus)
	}
	if id.PoE {
		t.Error("le CSS326-24G-2S+ n'a pas de PoE")
	}
}

// TestGroupesDeFacadeNommesCommeLEquipement : la façade enregistrée porte les noms que
// le switch emploie.
//
// DEUX FAMILLES, chacune repartant de 1 : « Port1 »… « Port24 » pour le cuivre, « SFP1 »
// et « SFP2 » pour les cages. C'est ce que /link.b sert dans `nm`, et c'est ce que
// l'interface de l'équipement affiche — donc ce que l'exploitant lit sur les deux écrans
// qu'il compare.
//
// Ce test portait l'INVERSE : « les cages SFP CONTINUENT la numérotation du cuivre (25 et
// 26 sur un 24 ports) ». C'était la convention du connecteur Instant On, empruntée pour
// qu'un parc mêlé se compare — au prix d'un rapprochement laissé à faire de tête devant
// chaque MikroTik. Le connecteur RouterOS voisin ne l'a jamais payé : ses groupes sont
// `ether` à partir de 1 et `sfp-sfpplus` à partir de 1. Les anciens noms restent ACCEPTÉS
// en entrée (cf. TestIndexApprendLesDeuxFamilles) : ce sont les groupes qui changent, pas
// la capacité à relire ce que la base porte déjà.
//
// Ces groupes ne sont pas décoratifs : c'est eux que l'interface web déplie pour énumérer
// les prises (`expandPorts`), et internal/portrules qui en tire le média — d'où le
// libellé « SFP+ » conservé, qui est ce qui fait classer la cage en 10 gigabits.
func TestGroupesDeFacadeNommesCommeLEquipement(t *testing.T) {
	var id = Identity{
		Model: "CSS326-24G-2S+", CopperPorts: 24, SFPPorts: 2, SFPFirst: 24, SFPPlus: true,
	}
	var groups = id.PortGroups()
	if len(groups) != 2 {
		t.Fatalf("%d groupe(s), attendu 2", len(groups))
	}
	if groups[0]["prefix"] != CopperPortPrefix || groups[0]["from"] != 1 ||
		groups[0]["count"] != 24 || groups[0]["speed"] != "1G" {
		t.Errorf("groupe cuivre = %+v ; attendu « Port1 » … « Port24 » à 1G", groups[0])
	}
	if groups[1]["prefix"] != SFPPortPrefix || groups[1]["from"] != 1 ||
		groups[1]["count"] != 2 || groups[1]["speed"] != "10G" ||
		groups[1]["label"] != "SFP+" {
		t.Errorf("groupe SFP = %+v ; attendu « SFP1 » et « SFP2 », 10G, libellé SFP+",
			groups[1])
	}

	// ET LES NOMS DÉPLIÉS DOIVENT ÊTRE CEUX DE LA FAÇADE DU CONNECTEUR. C'est le
	// rapprochement qui compte : la lecture d'état nomme ses ports avec [Facade], l'écran
	// les énumère avec ces groupes-ci, et les deux listes doivent coïncider — sinon une
	// configuration lue existe sans qu'aucune prise de l'écran ne la porte.
	var deplie []string
	var g map[string]any
	for _, g = range groups {
		var from = g["from"].(int)
		var i int
		for i = 0; i < g["count"].(int); i++ {
			deplie = append(deplie, g["prefix"].(string)+itoa(from+i))
		}
	}
	if !reflect.DeepEqual(deplie, facadeCSS326().Names()) {
		t.Errorf("façade dépliée = %v\nnoms du connecteur = %v", deplie,
			facadeCSS326().Names())
	}
}

// TestGeometrieSansOffsetDeCages : le repli quand le firmware ne dit pas où sont les
// cages.
//
// Le champ `sfpo` est FACULTATIF, et l'interface de l'équipement place alors les
// cages à la fin (`t = void 0==d.sfpo ? da : d.sfpo`). Reproduire ce repli est
// nécessaire : un firmware plus ancien peut l'omettre, et supposer 0 placerait les
// cages en tête de façade.
func TestGeometrieSansOffsetDeCages(t *testing.T) {
	var page, err = ParseObject("{prt:0x1a,sfp:0x02}")
	if err != nil {
		t.Fatalf("page refusée : %v", err)
	}
	var ports, sfp, first, geoErr = geometryOf(page)
	if geoErr != nil {
		t.Fatalf("géométrie refusée : %v", geoErr)
	}
	if ports != 26 || sfp != 2 || first != 24 {
		t.Errorf("géométrie = %d/%d/%d, attendu 26/2/24 (cages à la fin)", ports, sfp, first)
	}
}

// TestGeometrieIncoherenteRefusee : une géométrie qu'on ne comprend pas doit arrêter
// la lecture.
//
// Le nombre de ports décide du jeu de champs à renvoyer sur /fwd.b : se tromper
// enverrait une page amputée, et l'équipement serait libre d'y remettre à zéro
// l'isolation de ports de tous ses ports.
func TestGeometrieIncoherenteRefusee(t *testing.T) {
	var cas = []struct{ nom, src string }{
		{"sans nombre de ports", "{sfp:0x02}"},
		{"zéro port", "{prt:0x00}"},
		{"plus de cages que de ports", "{prt:0x02,sfp:0x04}"},
		{"cages hors façade", "{prt:0x1a,sfp:0x02,sfpo:0x40}"},
	}
	var c = cas[0]
	for _, c = range cas {
		t.Run(c.nom, func(t *testing.T) {
			var page, err = ParseObject(c.src)
			if err != nil {
				t.Fatalf("page refusée par l'analyseur : %v", err)
			}
			if _, _, _, geoErr = geometryOf(page); geoErr == nil {
				t.Error("géométrie incohérente acceptée")
			}
		})
	}
}

// geoErr est déclaré au niveau du fichier pour que la boucle de test ci-dessus
// n'utilise pas `:=`, interdit dans ce dépôt.
var geoErr error

// TestAdressageAllerRetour : le décodage et l'encodage d'une adresse concordent.
//
// C'est LE détail qui fait poser une adresse fausse. L'ordre des octets est celui du
// petit-boutien : le premier octet de la notation pointée est l'octet de poids
// faible. Le vérifier dans les deux sens est ce qui prouve qu'on n'a pas simplement
// inversé les deux fonctions de façon cohérente.
func TestAdressageAllerRetour(t *testing.T) {
	// La valeur de gauche est celle que l'équipement a réellement servie pour
	// l'adresse par laquelle il a répondu.
	var cas = []struct {
		brut   uint32
		pointe string
	}{
		{0x2400630a, "10.99.0.36"},
		{0x0100a8c0, "192.168.0.1"},
		{0x00000000, "0.0.0.0"},
		{0xffffffff, "255.255.255.255"},
	}
	var c = cas[0]
	for _, c = range cas {
		var got = DecodeIPv4(c.brut)
		if got != c.pointe {
			t.Errorf("DecodeIPv4(%#x) = %q, attendu %q", c.brut, got, c.pointe)
		}
		var absent, ok = EncodeIPv4(c.pointe)
		if !ok || absent != c.brut {
			t.Errorf("EncodeIPv4(%q) = %#x (ok=%v), attendu %#x", c.pointe, absent, ok, c.brut)
		}
	}
	// Ce qui n'est pas une adresse doit être refusé, pas transformé en zéro : poser
	// 0.0.0.0 en croyant poser une adresse rendrait l'équipement injoignable.
	var mauvaises = []string{"", "10.99.0", "10.99.0.256", "10.99.0.36.1", "dix.99.0.36"}
	var s string
	for _, s = range mauvaises {
		var _, crsOK = EncodeIPv4(s)
		if crsOK {
			t.Errorf("EncodeIPv4(%q) a été accepté", s)
		}
	}
}

// TestDecodageMAC : sans inversion, et refus de ce qui n'est pas une MAC.
func TestDecodageMAC(t *testing.T) {
	var got, ok = DecodeMAC("b869f45f14a8")
	if !ok || got != "b8:69:f4:5f:14:a8" {
		t.Errorf("DecodeMAC = %q (ok=%v)", got, ok)
	}
	var mauvaises = []string{"", "b869f45f14", "b869f45f14a8ff", "zz69f45f14a8"}
	var s string
	for _, s = range mauvaises {
		var _, mauvaisOK = DecodeMAC(s)
		if mauvaisOK {
			t.Errorf("DecodeMAC(%q) accepté", s)
		}
	}
}

// TestFingerprintReconnaissance : SwOS s'accroche par son nom, et RouterOS est exclu.
//
// L'exclusion de RouterOS est le seul cas de ce projet où deux connecteurs se
// disputent le même châssis : un CRS démarre sous l'un ou l'autre système, et leurs
// capabilities n'ont rien à voir.
func TestFingerprintReconnaissance(t *testing.T) {
	var d = &Driver{}
	var cas = []struct {
		nom      string
		ev       driver.Evidence
		attendu  int
		operator string
	}{
		{
			nom: "titre et modèle",
			ev: driver.Evidence{
				HTTPTitle: "MikroTik SwOS",
				HTTPBody:  `Digest realm="CSS326-24G-2S+"`,
			},
			attendu: 98,
		},
		{
			nom:     "titre seul",
			ev:      driver.Evidence{HTTPTitle: "MikroTik SwOS"},
			attendu: 95,
		},
		{
			nom:     "modèle seul, sans le nom du système",
			ev:      driver.Evidence{HTTPBody: `Digest realm="CSS610-8G-2S+"`},
			attendu: 55,
		},
		{
			// Le châssis est un CRS, mais c'est RouterOS qui a démarré : ce n'est pas
			// pour ce connecteur.
			nom: "CRS sous RouterOS",
			ev: driver.Evidence{
				HTTPTitle: "RouterOS",
				SysDescr:  "RouterOS CRS328-24P-4S+",
			},
			attendu: 0,
		},
		{
			nom:     "autre plateforme",
			ev:      driver.Evidence{HTTPTitle: "Instant On - Hewlett Packard Enterprise"},
			attendu: 0,
		},
		{nom: "rien", ev: driver.Evidence{}, attendu: 0},
	}
	var c = cas[0]
	for _, c = range cas {
		t.Run(c.nom, func(t *testing.T) {
			var got = d.Fingerprint(c.ev)
			if got != c.attendu {
				t.Errorf("Fingerprint = %d, attendu %d", got, c.attendu)
			}
		})
	}
}

// TestJeuDeChampsFwdSuitLaTaille : le nombre de ports décide du jeu de champs.
//
// /fwd.b est la seule page dont le jeu inscriptible dépend de la taille du modèle :
// elle porte un masque d'isolation par port SOURCE. Recopier tel quel le jeu relevé
// sur un 26 ports vers un modèle plus petit enverrait des masques pour des ports qui
// n'existent pas ; vers un plus grand, il en oublierait, et l'équipement serait libre
// d'y remettre l'isolation à zéro.
//
// C'est aussi pourquoi la géométrie est LUE sur /link.b et jamais déduite du nom du
// modèle : une convention de nommage se trompe, un équipement interrogé sur sa propre
// géométrie ne se trompe pas.
func TestJeuDeChampsFwdSuitLaTaille(t *testing.T) {
	var petits = FwdWritableFields(10)
	var grands = FwdWritableFields(26)
	if len(grands)-len(petits) != 16 {
		t.Errorf("un modèle de 26 ports doit porter 16 masques d'isolation de plus qu'un "+
			"modèle de 10 ; obtenu %d et %d champs", len(grands), len(petits))
	}
	if petits[0] != "fp1" || petits[9] != "fp10" {
		t.Errorf("les masques d'isolation doivent être numérotés à partir de 1 : %v", petits[:10])
	}
	// Les champs communs viennent APRÈS, dans l'ordre de la sauvegarde de
	// l'équipement.
	if petits[10] != "lck" || grands[26] != "lck" {
		t.Errorf("l'ordre des champs communs ne suit pas celui de la sauvegarde")
	}
	// Et le VLAN natif, qui est ce que ce connecteur écrit, y est bien.
	var trouve bool
	var f string
	for _, f = range grands {
		if f == FieldDefaultVLAN {
			trouve = true
		}
	}
	if !trouve {
		t.Errorf("le jeu inscriptible de /fwd.b doit porter %q", FieldDefaultVLAN)
	}
}

// TestPortGroupsVideSansFacade : sans façade lue, on n'invente pas de ports.
func TestPortGroupsVideSansFacade(t *testing.T) {
	var id = Identity{Model: "CSS326-24G-2S+"}
	if !reflect.DeepEqual(id.PortGroups(), []map[string]any{}) {
		t.Errorf("PortGroups sans façade = %v, attendu vide", id.PortGroups())
	}
}
