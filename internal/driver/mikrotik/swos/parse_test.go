package swos

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// L'analyseur des pages `*.b`, verified sur les CAPTURES REELLES de the device.
//
// Les fichiers de testdata/ ne sont pas des exemples rediges : ce sont les responses
// telles que le MikroTik CSS326-24G-2S+ de reference (SwOS 2.17, 10.99.0.36) les a
// servies, rapatriees a travers le tunnel du proxy. C'est ce qui donne leur valeur a
// ces tests — un analyseur valide sur une syntaxe qu'on a soi-same written ne prouve
// rien de ce que the device envoie actually.
//
// La seule retouche faite a ces captures est la COMMUNITY SNMP de the device de
// production, remplacee par un motif de same forme dans snmp.b et backup.swb : un
// secret de production n'a rien a faire dans un depot.

// capture lit une real capture.
func capture(t *testing.T, name string) string {
	t.Helper()
	var b, err = os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("capture %s illisible : %v", name, err)
	}
	return string(b)
}

// TestParseSysReel : la page la plus dense de la plateforme, telle que servie.
func TestParseSysReel(t *testing.T) {
	var page, err = ParseObject(capture(t, "sys.b"))
	if err != nil {
		t.Fatalf("/sys.b refused : %v", err)
	}

	// Les strings sont de l'UTF-8 hex-encoded, et c'est ce decoding qui donne au
	// connecteur son identification.
	var cas = []struct {
		champ, expected string
	}{
		{FieldBoard, "CSS326-24G-2S+"},
		{FieldVersion, "2.17"},
		{FieldIdentity, "lan-m6"},
		{FieldSerial, "945809C232D5"},
	}
	var c = cas[0]
	for _, c = range cas {
		var got, ok = page.Text(c.champ)
		if !ok || got != c.expected {
			t.Errorf("%s = %q (ok=%v), expected %q", c.champ, got, ok, c.expected)
		}
	}

	// L'ADRESSE, et l'ordre des octets. C'est le decoding qui ferait afficher — puis
	// write — une adresse fausse s'il was inverse.
	var ip, ipOK = page.Num(FieldIP)
	if !ipOK {
		t.Fatalf("/sys.b ne porte pas %q", FieldIP)
	}
	if ip != 0x2400630a {
		t.Fatalf("valeur brute de %s = %#x, la capture porte 0x2400630a", FieldIP, ip)
	}
	var pointe = DecodeIPv4(ip)
	if pointe != "10.99.0.36" {
		t.Errorf("DecodeIPv4(%#x) = %q, expected 10.99.0.36 — c'est l'adresse par "+
			"laquelle the device a repondu a cette capture same", ip, pointe)
	}

	// La MAC, sans inversion.
	var brut, brutOK = page.Value(FieldMAC)
	if !brutOK || brut.Kind != KindStr {
		t.Fatalf("%s absent ou de type inattendu", FieldMAC)
	}
	var mac, macOK = DecodeMAC(brut.Raw)
	if !macOK || mac != "b8:69:f4:5f:14:a8" {
		t.Errorf("DecodeMAC(%q) = %q (ok=%v), expected b8:69:f4:5f:14:a8", brut.Raw, mac, macOK)
	}

	// Le VLAN de management de cet device.
	var avln, has = page.Num(FieldAllowVLAN)
	if !has || avln != 99 {
		t.Errorf("%s = %d (ok=%v), expected 99 — c'est le VLAN par lequel cet "+
			"device est administre", FieldAllowVLAN, avln, has)
	}
	// Adressage statique.
	var iptp, iptpOK = page.Num(FieldAddrMode)
	if !iptpOK || iptp != AddrStatic {
		t.Errorf("%s = %d, expected %d (statique)", FieldAddrMode, iptp, AddrStatic)
	}
}

// TestChampAbsentNestPasZero : le parti pris central de l'analyseur.
//
// L'interface de the device fait l'INVERSE — son accesseur rend la valeur par
// default declared dans la definition du champ quand la page ne porte pas la key.
// C'est acceptable pour dessiner un formulaire et inacceptable ici : « ce port est
// dans le VLAN 0 » et « la page ne dit rien de ce port » menent a des ecritures
// opposees, et l'une des deux coupe un uplink.
func TestChampAbsentNestPasZero(t *testing.T) {
	var page, err = ParseObject(capture(t, "sys.b"))
	if err != nil {
		t.Fatalf("/sys.b refused : %v", err)
	}

	// Tous les accesseurs doivent refuser, et pour deux raisons distinctes : la key
	// n'existe pas, ou elle existe mais porte une AUTRE FORME. Lire un tableau comme
	// un scalaire donnerait un masque de ports faux, donc une write sur les
	// mauvais ports.
	var refus = []struct {
		quoi string
		lu   func() bool // rend le second retour de l'accesseur
	}{
		{"Num sur une key absente — le champ serait lu comme zero",
			func() bool { var _, ok = page.Num("nexistepas"); return ok }},
		{"Text sur une key absente",
			func() bool { var _, ok = page.Text("nexistepas"); return ok }},
		{"Nums sur une key absente",
			func() bool { var _, ok = page.Nums("nexistepas"); return ok }},
		{"Bit sur une key absente — le masque serait lu comme « aucun port »",
			func() bool { var _, ok = page.Bit("nexistepas", 0); return ok }},
		{"NumAt sur une key absente",
			func() bool { var _, ok = page.NumAt("nexistepas", 0); return ok }},
		{"Num sur une string",
			func() bool { var _, ok = page.Num(FieldBoard); return ok }},
		{"Text sur un nombre",
			func() bool { var _, ok = page.Text(FieldIP); return ok }},
		{"Nums sur un scalaire",
			func() bool { var _, ok = page.Nums(FieldIP); return ok }},
		{"Obj sur un nombre",
			func() bool { var _, ok = page.Obj(FieldIP); return ok }},
	}
	var r struct {
		quoi string
		lu   func() bool
	}
	for _, r = range refus {
		if r.lu() {
			t.Errorf("%s : l'accesseur a rendu ok=true", r.quoi)
		}
	}
	if page.Has("nexistepas") {
		t.Error("Has d'une key absente rend true")
	}
	// Un index hors bornes d'un tableau n'est pas un zero non plus.
	var _, dansLesBornes = page.NumAt(FieldAllowVLAN, 99)
	if dansLesBornes {
		t.Error("NumAt hors bornes rend ok=true")
	}

	// Et un zero ACTUALLY present se distingue d'une absence : c'est l'autre
	// half de la property, sans laquelle « prudent » deviendrait « aveugle ».
	var alla, allaOK = page.Num(FieldAllowFrom)
	if !allaOK {
		t.Fatalf("%s devrait be present dans la capture", FieldAllowFrom)
	}
	if alla != 0 {
		t.Errorf("%s = %#x, la capture porte 0 (aucune restriction d'adresse)",
			FieldAllowFrom, alla)
	}
}

// TestParseLinkGeometrieEtMasques : la geometry du model et l'ordre des bits.
//
// Se tromper d'ordre de bits donne un masque de ports OFFSET, i.e. une
// configuration applied sur les mauvais ports. Sur la capture, un seul port porte un
// lien, et c'est verifiable de trois ways independantes.
func TestParseLinkGeometrieEtMasques(t *testing.T) {
	var page, err = ParseObject(capture(t, "link.b"))
	if err != nil {
		t.Fatalf("/link.b refused : %v", err)
	}

	var ports, sfp, first, geoErr = geometryOf(page)
	if geoErr != nil {
		t.Fatalf("geometry illisible : %v", geoErr)
	}
	if ports != 26 || sfp != 2 || first != 24 {
		t.Errorf("geometry = %d ports, %d SFP, first cage a %d ; expected 26/2/24 "+
			"pour un CSS326-24G-2S+", ports, sfp, first)
	}

	// Le masque de liens : `lnk:0x02000000`, donc le bit 25, donc le port 26 —
	// i.e. la SECONDE cage SFP. Bit 0 = port 1.
	var i int
	for i = 0; i < ports; i++ {
		var up, ok = page.Bit(FieldLink, i)
		if !ok {
			t.Fatalf("bit %d du masque de liens illisible", i)
		}
		if up != (i == 25) {
			t.Errorf("lien du port %d (index %d) = %v ; seul l'index 25 (cage SFP2) "+
				"porte un lien dans cette capture", i+1, i, up)
		}
	}

	// Tous les ports sont enabled (`en:0x03ffffff` = 26 bits a 1). C'est ce qui
	// prouve que le masque est bien lu jusqu'au bout, et qu'on ne stops pas a 24.
	for i = 0; i < ports; i++ {
		var on, onOK = page.Bit(FieldEnabled, i)
		if !onOK || !on {
			t.Errorf("port %d : enabled = %v (ok=%v), la capture les active tous", i+1, on, onOK)
		}
	}
	// Et le bit 26 (index 26), qui n'existe pas sur ce model, est bien a zero : le
	// masque ne deborde pas.
	var horsFacade, horsFacadeOK = page.Bit(FieldEnabled, 26)
	if !horsFacadeOK || horsFacade {
		t.Errorf("bit 26 du masque d'activation = %v : le masque de 26 ports deborderait",
			horsFacade)
	}

	// Le RATE n'a de sens que sur un port qui porte un lien. Les 25 autres portent
	// l'index 7, hors table : rendre « 10M » pour eux serait inventer une valeur.
	var spd, spdOK = page.Nums(FieldSpeed)
	if !spdOK || len(spd) != ports {
		t.Fatalf("%s = %v (ok=%v), expected %d valeurs", FieldSpeed, spd, spdOK, ports)
	}
	var label, labelOK = SpeedLabel(spd[25])
	if !labelOK || label != "10G" {
		t.Errorf("rate de la cage SFP2 = %q (ok=%v), expected 10G", label, labelOK)
	}
	var _, hors = SpeedLabel(spd[0])
	if hors {
		t.Errorf("le port 1 est sans lien et porte l'index %d : SpeedLabel doit refuser "+
			"rather que d'inventer un rate", spd[0])
	}

	// Les noms de ports, hex-encoded : « Port1 »… « SFP2 ».
	var noms []string
	var nomsOK bool
	noms, nomsOK = page.Texts(FieldPortName)
	if !nomsOK || len(noms) != ports {
		t.Fatalf("%s illisible (ok=%v, %d noms)", FieldPortName, nomsOK, len(noms))
	}
	if noms[0] != "Port1" || noms[23] != "Port24" || noms[24] != "SFP1" || noms[25] != "SFP2" {
		t.Errorf("noms de ports = [%q … %q %q %q]", noms[0], noms[23], noms[24], noms[25])
	}
}

// TestParseTableVLANReelle : la table des VLAN de the device.
func TestParseTableVLANReelle(t *testing.T) {
	var rows, err = ParseTable(capture(t, "vlan.b"))
	if err != nil {
		t.Fatalf("/vlan.b refused : %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("%d ligne(s), la capture en porte 2 (VLAN 99 et 100)", len(rows))
	}
	var attendus = []uint32{99, 100}
	var i int
	for i = 0; i < 2; i++ {
		var vid, ok = rows[i].Num(FieldVLANID)
		if !ok || vid != attendus[i] {
			t.Errorf("ligne %d : vid = %d (ok=%v), expected %d", i+1, vid, ok, attendus[i])
		}
		// Les 26 ports sont membres des deux VLAN (`mbr:0x03ffffff`).
		var p int
		for p = 0; p < 26; p++ {
			var member, bitOK = rows[i].Bit(FieldVLANMembers, p)
			if !bitOK || !member {
				t.Errorf("VLAN %d : port %d membre = %v (ok=%v), la capture les rend tous "+
					"membres", attendus[i], p+1, member, bitOK)
			}
		}
	}
}

// TestTableVideEstUnResultat : `[]` veut dire « aucune rule », pas « je n'ai pas su
// lire ». Confondre les deux ferait signaler une panne sur un device dont la
// table d'ACL est simplement vide, ce qui est le cas normal.
func TestTableVideEstUnResultat(t *testing.T) {
	var nom string
	for _, nom = range []string{"acl.b", "host.b"} {
		var rows, err = ParseTable(capture(t, nom))
		if err != nil {
			t.Errorf("%s : %v", nom, err)
			continue
		}
		if rows == nil {
			t.Errorf("%s rend une table nulle : une table vide doit be une tranche "+
				"vide, pour que l'appelant n'ait pas a distinguer les deux", nom)
		}
		if len(rows) != 0 {
			t.Errorf("%s : %d ligne(s), la capture est vide", nom, len(rows))
		}
	}
}

// TestPageHTMLRefusee : le trap le plus couteux de cette plateforme.
//
// Sur SwOS, un chemin qui n'existe pas ne rend PAS 404 : il rend l'interface web
// complete, en HTTP 200. Un analyseur qui accepterait ce corps, ou qui rendrait une
// page vide, ferait conclure « cette fonction n'est pas configuree » there where la vraie
// cause est une faute de frappe dans le nom de la page.
func TestPageHTMLRefusee(t *testing.T) {
	var html = `<!doctype html><style>html{height:100%}</style><script>let l={};</script>`
	var _, _, err = Parse(html)
	if err == nil {
		t.Fatal("l'interface web de the device a been accepted comme une page de data")
	}
	// Le message doit porter un extrait : sans lui, « ce n'est pas une page SwOS »
	// n'aide personne a comprendre que le chemin est faux.
	if !strings.Contains(err.Error(), "doctype") {
		t.Errorf("le message d'erreur doit citer ce qui a been recu, obtenu : %v", err)
	}
}

// TestSyntaxesRefusees : ce que l'analyseur doit rejeter rather que d'interpreter.
func TestSyntaxesRefusees(t *testing.T) {
	var cas = []struct {
		nom, src string
	}{
		{"vide", ""},
		{"objet non closed", "{a:0x01"},
		{"tableau non closed", "{a:[0x01,0x02"},
		{"apostrophe non fermee", "{a:'6162"},
		{"deux-points manquant", "{a 0x01}"},
		{"valeur manquante", "{a:}"},
		{"key repetee", "{a:0x01,a:0x02}"},
		{"residu after l'objet", "{a:0x01} parasite"},
		{"nombre hors 32 bits", "{a:0x1ffffffff}"},
		{"table de non-objets", "[0x01,0x02]"},
	}
	var c = cas[0]
	for _, c = range cas {
		t.Run(c.nom, func(t *testing.T) {
			var _, _, err = Parse(c.src)
			if err == nil {
				t.Errorf("Parse(%q) accepted : une page qu'on ne sait pas lire doit se "+
					"signaler, pas se deviner", c.src)
			}
		})
	}
}

// TestEncodageIdentiqueALinterface : le corps produit en write doit be celui que
// l'interface de the device produirait.
//
// L'encodage des nombres reproduit sa fonction `Pa` : hexadecimal minuscule,
// rembourre a un nombre PAIR de chiffres. Rien ne prouve que the device y soit
// sensible ; reproduire a la lettre la seule forme dont on sait qu'elle est accepted
// est le choix qui ne costs rien.
func TestEncodageIdentiqueALinterface(t *testing.T) {
	var cas = []struct {
		v      uint32
		attend string
	}{
		{0, "0x00"},
		{1, "0x01"},
		{15, "0x0f"},
		{99, "0x63"},
		{0x100, "0x0100"},
		{0x03ffffff, "0x03ffffff"},
		{0xffffffff, "0xffffffff"},
	}
	var c = cas[0]
	for _, c = range cas {
		var got = EncodeNum(c.v)
		if got != c.attend {
			t.Errorf("EncodeNum(%#x) = %q, expected %q", c.v, got, c.attend)
		}
	}
}

// TestAllerRetourSurCapturesReelles : relire ce qu'on vient to write doit rendre ce
// qu'on avait lu.
//
// C'est la property qui rend le LIRE-MODIFIER-REWRITE sur : si l'encodeur perdait
// ou alterait un champ, une write d'appartenance VLAN emporterait avec elle
// l'isolation de ports ou les limites de storm, en silence.
func TestAllerRetourSurCapturesReelles(t *testing.T) {
	var nom string
	for _, nom = range []string{"sys.b", "link.b", "fwd.b", "snmp.b", "rstp.b", "lacp.b", "sfp.b", "stats.b"} {
		t.Run(nom, func(t *testing.T) {
			var page, err = ParseObject(capture(t, nom))
			if err != nil {
				t.Fatalf("%s refused : %v", nom, err)
			}
			var relu *Page
			relu, err = ParseObject(page.Encode())
			if err != nil {
				t.Fatalf("le corps reencoded de %s n'est pas relisible : %v", nom, err)
			}
			if !reflect.DeepEqual(page.Keys(), relu.Keys()) {
				t.Fatalf("les keys de %s ont changed a l'aller-retour", nom)
			}
			var k string
			for _, k = range page.Keys() {
				var a, _ = page.Value(k)
				var b, _ = relu.Value(k)
				if !reflect.DeepEqual(a, b) {
					t.Errorf("%s : champ %q altere par l'aller-retour (%+v → %+v)", nom, k, a, b)
				}
			}
		})
	}
}

// TestSauvegardeReelle : /backup.swb, la configuration entire en une request.
//
// C'est la source qui a permis d'establish le jeu de champs INSCRIPTIBLE de chaque
// page — l'information qu'aucune documentation ne donne, et sans laquelle une
// write partielle laisserait the device remettre des champs a zero.
func TestSauvegardeReelle(t *testing.T) {
	var page, err = ParseBackup(capture(t, "backup.swb"))
	if err != nil {
		t.Fatalf("/backup.swb refused : %v", err)
	}
	// Les keys sont des NOMS DE PAGE, point et point d'exclamation compris.
	var attendues = []string{"vlan.b", "lacp.b", ".pwd.b", "snmp.b", "rstp.b",
		"link.b", "fwd.b", "sys.b", "acl.b", "host.b"}
	var k string
	for _, k = range attendues {
		if !page.Has(k) {
			t.Errorf("la sauvegarde ne porte pas la page %q", k)
		}
	}

	// LA property qui compte : le jeu de champs declared pour /fwd.b doit be
	// exactement celui que the device stocke. Un ecart voudrait dire qu'une
	// write enverrait une page truncated — ou des champs qu'il n'attend pas.
	var fwd, ok = page.Obj("fwd.b")
	if !ok {
		t.Fatal("la sauvegarde ne porte pas /fwd.b comme un objet")
	}
	var declares = FwdWritableFields(26)
	if !reflect.DeepEqual(fwd.Keys(), declares) {
		t.Errorf("le jeu inscriptible de /fwd.b declared par le connecteur differe de "+
			"celui que the device stocke.\n  sauvegarde : %v\n  declared    : %v",
			fwd.Keys(), declares)
	}

	var snmp *Page
	snmp, ok = page.Obj("snmp.b")
	if !ok {
		t.Fatal("la sauvegarde ne porte pas /snmp.b comme un objet")
	}
	var champs, known = WritableFields(PageSNMP)
	if !known {
		t.Fatal("jeu inscriptible de /snmp.b non declared")
	}
	if !reflect.DeepEqual(snmp.Keys(), champs) {
		t.Errorf("jeu inscriptible de /snmp.b : sauvegarde %v, declared %v",
			snmp.Keys(), champs)
	}

	// AUCUN nom d'utilisateur dans la configuration entire de the device : c'est
	// ce qui establishes qu'il n'y a pas de compte a nommer sur SwOS.
	var pwd *Page
	pwd, ok = page.Obj(".pwd.b")
	if !ok {
		t.Fatal("la sauvegarde ne porte pas la page du mot de passe")
	}
	if !reflect.DeepEqual(pwd.Keys(), []string{"pwd"}) {
		t.Errorf("la page du mot de passe porte %v : s'il y avait un nom de compte, "+
			"il serait there, et AdminAccount.Custom devrait passer a vrai", pwd.Keys())
	}
}

// TestDecodageTexteCommeLequipement : le decoding des strings hex-encodees.
func TestDecodageTexteCommeLequipement(t *testing.T) {
	var cas = []struct {
		nom, raw, attend string
		ok               bool
	}{
		{"vide", "", "", true},
		{"ASCII", "506f727431", "Port1", true},
		// L'octet NUL TERMINE : ces champs sont des tampons de taille fixe, et ce qui
		// suit est du residu que l'interface de the device n'affiche pas non plus.
		{"NUL terminateur", "506f7274310041424344", "Port1", true},
		// Le rembourrage par ESPACES est conserve : c'est ainsi que les champs
		// SFF-8472 d'un transceiver sont written, et c'est a l'appelant de le rogner.
		{"espaces conserves", "465320", "FS ", true},
		// UTF-8 multi-byte: switch identity may carry non-ASCII.
		{"UTF-8", "6d6574656f", "meteo", true},
		// Un nombre impair de chiffres perd le dernier, comme dans le decoder de
		// the device.
		{"impair", "506f7274315", "Port1", true},
		{"non hexadecimal", "zzzz", "", false},
	}
	var c = cas[0]
	for _, c = range cas {
		t.Run(c.nom, func(t *testing.T) {
			var got, ok = decodeHexText(c.raw)
			if ok != c.ok || got != c.attend {
				t.Errorf("decodeHexText(%q) = %q,%v ; expected %q,%v",
					c.raw, got, ok, c.attend, c.ok)
			}
		})
	}
}

// TestMasqueEnTableauDeMots : l'ordre des mots d'un masque de plus de 32 ports.
//
// Aucun model SwOS connu ne depasse 32 ports, et c'est precisely pourquoi ce test
// existe : le jour where l'un en depassera, l'ordre des mots ne sera pas devine mais
// verified. Il est observed dans le JavaScript de the device, qui parcourt le tableau
// A L'ENVERS — le DERNIER element porte les bits 0 a 31.
func TestMasqueEnTableauDeMots(t *testing.T) {
	// [mot haut, mot bas] : le port 1 est le bit 0 du DERNIER mot.
	var page, err = ParseObject("{mbr:[0x00000003,0x80000001]}")
	if err != nil {
		t.Fatalf("page refused : %v", err)
	}
	var cas = []struct {
		index  int
		attend bool
	}{
		{0, true},   // bit 0 du mot bas
		{1, false},  //
		{31, true},  // bit 31 du mot bas
		{32, true},  // bit 0 du mot haut
		{33, true},  // bit 1 du mot haut
		{34, false}, //
	}
	var c = cas[0]
	for _, c = range cas {
		var got, ok = page.Bit("mbr", c.index)
		if !ok || got != c.attend {
			t.Errorf("Bit(index %d) = %v (ok=%v), expected %v", c.index, got, ok, c.attend)
		}
	}
	// Et writing conserve la FORME : renvoyer un scalaire there where the device
	// attend un tableau, c'est renvoyer un masque qu'il ne lira pas comme written.
	// Le port 35 est le bit 2 du mot HAUT, qui portait already les bits 0 et 1 : il doit
	// passer de 0x03 a 0x07, et le mot BAS ne doit pas be touche.
	if !page.SetBit("mbr", 34, true) {
		t.Fatal("SetBit refused sur un masque en tableau")
	}
	var pointe = page.Encode()
	if pointe != "{mbr:[0x07,0x80000001]}" {
		t.Errorf("after SetBit(34) : %q, expected {mbr:[0x07,0x80000001]}", pointe)
	}
}

// TestSetBitRefuseUneCleAbsente : ne jamais create un masque de rien.
//
// Un masque fabrique a partir de zero vaut « aucun port ». Sur `allp` (« Allow From
// Ports »), cela suffit a rendre the device injoignable — et ce serait fait en
// croyant simplement autoriser un port de plus.
func TestSetBitRefuseUneCleAbsente(t *testing.T) {
	var page = NewPage()
	if page.SetBit("allp", 3, true) {
		t.Error("SetBit a created un masque qui n'existait pas dans la page lue")
	}
	if page.Has("allp") {
		t.Error("une key absente a been created")
	}
}

// TestOnlySignaleLesChampsManquants : le filtre write ne complete jamais.
//
// Renvoyer a the device une page truncated d'un champ qu'il attend, c'est le laisser
// le remettre a zero. Un champ requested mais absent de la page lue doit donc STOP
// writing, pas be invented.
func TestOnlySignaleLesChampsManquants(t *testing.T) {
	var page, err = ParseObject("{a:0x01,b:0x02}")
	if err != nil {
		t.Fatalf("page refused : %v", err)
	}
	var out, missing = page.Only([]string{"a", "b", "c"})
	if !reflect.DeepEqual(missing, []string{"c"}) {
		t.Errorf("champs manquants = %v, expected [c]", missing)
	}
	if out.Has("c") {
		t.Error("Only a invented le champ absent")
	}
	// L'ordre requested est respecte : reproduire l'ordre que the device sert rend
	// deux transcripts comparables ligne a ligne.
	out, missing = page.Only([]string{"b", "a"})
	if len(missing) != 0 || out.Encode() != "{b:0x02,a:0x01}" {
		t.Errorf("Only ne respecte pas l'ordre requested : %q", out.Encode())
	}
}
