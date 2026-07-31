package swos

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"meshcraft/internal/driver"
)

// Identification d'un switch SwOS — et la bonne surprise de cette plateforme.
//
// SwOS livre la REFERENCE EXACTE DU MODEL sans aucun identifiant. Son challenge
// d'authentification HTTP porte le model dans son `realm` :
//
//	$ GET /sys.b (sans identifiants)
//	HTTP/1.1 401 Unauthorized
//	WWW-Authenticate: Digest realm="CSS326-24G-2S+", qop="auth",
//	                  nonce="84aaad9d", stale=FALSE
//
// Verifie sur 10.99.0.36. C'est de l'or pour la discovery : le model precise — donc
// les capabilities, donc la geometry de la front panel — s'obtient AVANT d'avoir le
// moindre secret, ce qu'aucune autre plateforme du projet n'offre. There where le
// connecteur Instant On doit fouiller les champs caches de sa page de connexion pour
// esperer un `sysDescr`, ici il suffit de frapper a la porte.
//
// Ce que le realm ne donne PAS, et qui exige donc de s'authentifier : le number de
// serial, la version du firmware, le nom systeme, l'adresse MAC et la geometry
// reelle. Tout cela vit dans /sys.b et /link.b, sous Digest.
//
// La distinction est structurelle dans Identity.Source : l'interface ne doit pas
// present  au same rang ce qui a been obtenu a la porte et ce qui a been lu a
// l'interieur.

// Identity est ce qu'on sait d'un switch SwOS.
type Identity struct {
	// Model est la reference constructeur exacte (« CSS326-24G-2S+ »).
	Model string `json:"model"`
	// Family est la sous-gamme (« CSS », « CRS »), utile pour les differences de
	// capabilities qui se jouent a ce niveau.
	Family   string `json:"family"`
	Serial   string `json:"serial,omitempty"`
	MAC      string `json:"mac,omitempty"`
	Firmware string `json:"firmware,omitempty"`
	// Build est l'horodatage de compilation du firmware, tel que l'interface de
	// the device l'affiche a cote de la version.
	Build string `json:"build,omitempty"`
	// Hostname est le champ « Identity » de the device.
	Hostname string `json:"hostname,omitempty"`
	// Revision et BoardName : souvent vides sur les CSS, renseignes sur d'autres
	// cartes. Rendus tels quels rather que devines.
	Revision  string `json:"revision,omitempty"`
	BoardName string `json:"board_name,omitempty"`
	// Facade : ce qui permet de dessiner the device sans catalogue.
	CopperPorts int `json:"copper_ports"`
	SFPPorts    int `json:"sfp_ports"`
	// SFPFirst est l'index (base 0) de la first cage SFP. Sur tous les modeles
	// rencontres les cages sont en fin de front panel, mais SwOS a un champ pour le dire
	// et il vaut mieux le lire que le supposer.
	SFPFirst int  `json:"sfp_first"`
	SFPPlus  bool `json:"sfp_plus"`
	PoE      bool `json:"poe"`
	// MgmtIP / MgmtVLAN : l'adressage d'administration TEL QUE THE DEVICE LE
	// PORTE. Sert au pre-vol d'une bascule : c'est la seule source qui dise par where
	// passe la session en cours.
	MgmtIP   string `json:"mgmt_ip,omitempty"`
	MgmtVLAN int    `json:"mgmt_vlan,omitempty"`
	// Source dit hence vient l'information. `digest_realm` = obtenu SANS
	// authentification ; `sys.b` = lu sous authentification.
	Source string `json:"source"`
}

// reRealm extrait le realm d'un challenge `WWW-Authenticate`.
var reRealm = regexp.MustCompile(`(?i)realm="([^"]+)"`)

// header retrouve un en-tete dans une response du transport partage.
//
// `driver.Response.Headers` est une carte NUE (`map[string][]string`), pas un
// `http.Header` : elle n'a pas de `Get`, et surtout elle porte les keys dans la
// casse que le SERVEUR a envoyee. Un access direct par `"WWW-Authenticate"`
// fonctionnerait tant que le firmware capitalise ainsi, et rendrait une string vide
// le jour where l'un capitalise autrement — i.e. ferait conclure « ce n'est pas
// un SwOS » sur un device qui en est un. La comparaison est donc insensible a la
// casse, comme HTTP l'impose.
func header(headers map[string][]string, name string) string {
	var k string
	var v []string
	for k, v = range headers {
		if strings.EqualFold(k, name) && len(v) > 0 {
			return v[0]
		}
	}
	return ""
}

// hdrAuthenticate est l'en-tete qui porte le challenge — et, sur SwOS, le model.
const hdrAuthenticate = "WWW-Authenticate"

// reModel reconnait une reference de carte MikroTik commutee : la sous-gamme, puis
// tout ce qui suit jusqu'au bout du realm.
//
// Volontairement ancree sur les DEUX sous-gammes que SwOS anime, CSS et CRS, et pas
// sur « n'importe quoi qui ressemble a un model » : accrocher trop large ferait
// passer pour un SwOS un device qui n'en est pas, et les capabilities qui en
// decoulent decident de ce qu'on written.
var reModel = regexp.MustCompile(`^(CSS|CRS)[0-9A-Za-z+\-]*$`)

// ModelFromRealm extrait le model du realm d'un challenge Digest.
//
// Rend `ok=false` rather qu'une string vide quand le realm ne ressemble pas a une
// reference MikroTik : un realm inattendu veut dire qu'on ne parle pas a un SwOS, et
// il vaut mieux le dire que de ranger the device de force dans cette gamme.
func ModelFromRealm(challenge string) (string, bool) {
	var m = reRealm.FindStringSubmatch(challenge)
	if m == nil {
		return "", false
	}
	var realm = strings.TrimSpace(m[1])
	if !reModel.MatchString(realm) {
		return "", false
	}
	return realm, true
}

// FamilyOf rend la sous-gamme d'un model (« CSS326-24G-2S+ » → « CSS »).
func FamilyOf(model string) string {
	var up = strings.ToUpper(model)
	switch {
	case strings.HasPrefix(up, "CSS"):
		return "CSS"
	case strings.HasPrefix(up, "CRS"):
		return "CRS"
	}
	return ""
}

// Inspect identifie the device a l'adresse data.
//
// Deux temps, et l'ordre compte : d'abord le realm, SANS identifiant, ce qui rend le
// model same si le secret est faux ou inconnu ; ensuite /sys.b et /link.b sous
// Digest, pour tout le reste. Une identification partielle est rendue avec son
// erreur rather que perdue — savoir « c'est un CSS326-24G-2S+ dont on n'a pas le mot
// de passe » vaut infiniment mieux que « failure ».
func Inspect(ctx context.Context, host, user, secret string, d driver.Dialer) (Identity, error) {
	var out Identity

	// 1. Le realm, a la porte.
	var model, err = realmModel(ctx, host, d)
	if err != nil {
		return out, err
	}
	out.Model, out.Family, out.Source = model, FamilyOf(model), "digest_realm"
	out.PoE = ModelHasPoE(model)

	// 2. Le reste, a l'interieur. Identity partielle est rendue AVEC l'erreur : le
	//    model est acquis, et le perdre parce que la suite a echoue serait perdre la
	//    seule chose que cette plateforme donne gratuitement.
	var admin = OpenAdmin(host, user, secret, d)
	err = admin.fillIdentity(ctx, &out)
	if err != nil {
		return out, fmt.Errorf("model identifie (%s) mais %w", model, err)
	}
	return out, nil
}

// realmModel frappe a la porte et rend le model que le challenge Digest annonce.
//
// SANS AUCUN IDENTIFIANT, et c'est tout l'interet : la session n'a pas
// d'authentification armee, donc le 401 n'est pas rejoue et son en-tete est
// exactement ce qu'on vient chercher. Cette lecture-there costs UNE request HTTP, pas
// deux — il n'y a pas de request authentifiee derriere.
//
// Verifie a nouveau en direct sur l'reference device a travers le tunnel :
// `WWW-Authenticate: Digest realm="CSS326-24G-2S+", qop="auth", nonce="4753ad9d",
// stale=FALSE`, servi avec le 401 lui-same, et rendu a l'identique quand le secret
// presented est faux.
func realmModel(ctx context.Context, host string, d driver.Dialer) (string, error) {
	var anon = driver.NewWebSession("http://"+host, d, 0)
	var resp, err = anon.Get(ctx, PageSys)
	if err != nil {
		return "", fmt.Errorf("aucune response de %s sur le port 80 : %w", host, err)
	}
	var challenge = header(resp.Headers, hdrAuthenticate)
	var model, ok = ModelFromRealm(challenge)
	if !ok {
		return "", fmt.Errorf("the device a %s ne present pas un challenge SwOS "+
			"(HTTP %d, %s : %q) — ce n'est pas un switch SwOS, ou son firmware ne "+
			"s'annonce pas ainsi", host, resp.Status, hdrAuthenticate, challenge)
	}
	return model, nil
}

// fillIdentity complete une identity avec ce qui exige de s'authentifier.
//
// Sur un Admin rather que sur des parameters d'hote, pour deux raisons : les erreurs
// entrent au TRANSCRIPT de cet access, et le refus d'identifiants remonte a l'appelant
// sous la forme d'errCredentialsRefused — ce qui permet a Driver.Identify de savoir
// s'il doit essayer le candidat suivant ou s'stop.
//
// Identity est completee EN PLACE et progressivement : quand /link.b fails after
// un /sys.b reussi, la serial et le firmware sont already there, et l'appelant les rend avec
// son erreur. Une identification partielle vaut infiniment mieux qu'un failure.
func (a *Admin) fillIdentity(ctx context.Context, id *Identity) error {
	var sys, sysErr = a.Get(ctx, PageSys)
	if sysErr != nil {
		return fmt.Errorf("%s illisible : %w", PageSys, sysErr)
	}
	fillFromSys(id, sys)

	var link, linkErr = a.Get(ctx, PageLink)
	if linkErr != nil {
		return fmt.Errorf("%s illisible, la front panel reste inconnue : %w", PageLink, linkErr)
	}
	return fillFromLink(id, link)
}

// Contract traduit ce qu'on a observed en identity que le contrat porte.
//
// UNE TRADUCTION ET NON UN ALIAS, et le contrat le demande explicitement : « Les
// champs propres a une plateforme n'ont pas leur place ici […] Les connecteurs gardent
// leur propre type identity, plus riche, et remplissent celui-ci pour la couche
// API. » Le sens de la circulation est celui-there : le contrat porte ce que l'APPELANT
// consomme.
//
// CE QUE CETTE CONVERSION LAISSE DERRIERE ELLE, dit ici pour que personne ne le
// cherche : `Revision` et `BoardName` (vides sur les CSS, renseignes sur d'autres
// cartes), `SFPFirst` (l'index de la first cage — l'information survit dans
// PortGroups, dont le second groupe demarre there), `MgmtIP` et `MgmtVLAN` (l'adressage
// d'administration tel que the device le porte, que le contrat expose autrement :
// [driver.State.MgmtVLAN], rendu par ReadState). Tout cela reste lisible par
// [Inspect], que la discovery appelle.
//
// `ModelCode` reste VIDE, et c'est exact : MikroTik n'a pas de reference catalogue
// distincte du label du produit — « CSS326-24G-2S+ » EST la reference. Y recopier le
// model donnerait deux champs identiques, dont l'un ferait croire a une seconde
// source.
func (id Identity) Contract() driver.Identity {
	return driver.Identity{
		Model:       id.Model,
		Family:      id.Family,
		Serial:      id.Serial,
		MAC:         id.MAC,
		Firmware:    id.Firmware,
		Build:       id.Build,
		Hostname:    id.Hostname,
		CopperPorts: id.CopperPorts,
		SFPPorts:    id.SFPPorts,
		SFPPlus:     id.SFPPlus,
		PoE:         id.PoE,
		PortGroups:  id.PortGroups(),
		Source:      id.Source,
	}
}

// fillFromSys complete identity depuis /sys.b. Fonction pure, donc verifiable sur
// une real capture.
//
// Chaque champ est facultatif par construction : un firmware plus ancien peut
// n'en pas servir un, et ce qui manque reste VIDE. Aucun champ n'est deduit d'un
// autre — un serial number invented a partir du nom systeme, comme le fait le
// connecteur Instant On faute de mieux, n'aurait ici aucun fondement.
func fillFromSys(id *Identity, sys *Page) {
	// Le model de /sys.b confirme celui du realm. Il ne le remplace que s'il est
	// present, le realm etant la source la plus sure — c'est le firmware qui le
	// compose, pas un champ modifiable.
	var brd, ok = sys.Text(FieldBoard)
	if ok && brd != "" {
		id.Model = brd
		id.Family = FamilyOf(brd)
		id.PoE = ModelHasPoE(brd)
		id.Source = "sys.b"
	}
	// Les champs texte, dans l'ordre where the device les sert. La table rather qu'une
	// suite de blocs : elle rend visible d'un coup ce qui est lu et ce qui ne l'est
	// pas, et le champ absent laisse la destination VIDE sans que personne n'ait a y
	// penser.
	var textes = []struct {
		champ string
		dest  *string
	}{
		{FieldSerial, &id.Serial},
		{FieldVersion, &id.Firmware},
		{FieldRevision, &id.Revision},
		{FieldMarket, &id.BoardName},
		{FieldIdentity, &id.Hostname},
	}
	var tf struct {
		champ string
		dest  *string
	}
	for _, tf = range textes {
		var v, has = sys.Text(tf.champ)
		if has {
			*tf.dest = v
		}
	}

	var build, hasBuild = sys.Num(FieldBuild)
	if hasBuild && build != 0 {
		id.Build = strconv.FormatUint(uint64(build), 10)
	}

	// La MAC est servie sans separateur : `'b869f45f14a8'`.
	var raw, hasMAC = sys.Value(FieldMAC)
	if hasMAC && raw.Kind == KindStr {
		var mac, macOK = DecodeMAC(raw.Raw)
		if macOK {
			id.MAC = mac
		}
	}

	// L'adresse EN COURS (`cip`) rather que l'adresse configuree (`ip`) : en DHCP
	// les deux different, et c'est par la first que la session passe.
	var cip, hasCIP = sys.Num(FieldCurrentIP)
	if hasCIP && cip != 0 {
		id.MgmtIP = DecodeIPv4(cip)
	}
	var avln, hasVLAN = sys.Num(FieldAllowVLAN)
	if hasVLAN {
		id.MgmtVLAN = int(avln)
	}
}

// fillFromLink complete la front panel depuis /link.b.
func fillFromLink(id *Identity, link *Page) error {
	var ports, sfp, first, err = geometryOf(link)
	if err != nil {
		return err
	}
	id.CopperPorts, id.SFPPorts, id.SFPFirst = ports-sfp, sfp, first
	// SFP ou SFP+ : la reference le dit (« 2S+ » = deux cages 10 gigabits), et le
	// rate negocie le confirme quand un transceiver est insere. On se fie a la
	// reference, seule a repondre same cages vides.
	id.SFPPlus = sfp > 0 && strings.Contains(id.Model, "S+")
	return nil
}

// reModelPoE reconnait une reference a alimentation par le cable.
//
// La convention de nommage de MikroTik marque le PoE par un `P` there where les modeles
// sans PoE portent un `G` : « CRS328-24P-4S+ » contre « CSS326-24G-2S+ ». Le motif
// est donc un nombre de ports suivi de `P`, et rien d'autre — accrocher sur un `P`
// isole ferait passer « CSS326-24G-2S+ » pour du PoE au premier `+`.
//
// Vaut ce que vaut une convention de nommage, i.e. moins qu'un observed :
// c'est pourquoi le PoE de Features n'est pas declared par ce seul motif quand la
// geometry de the device, elle, est lisible (cf. le commentaire de Features).
var reModelPoE = regexp.MustCompile(`(?i)[0-9]+P(-|$)|PoE`)

// ModelHasPoE dit si la reference annonce du PoE.
func ModelHasPoE(model string) bool { return reModelPoE.MatchString(model) }

// PortGroups traduit la front panel en groupes de ports du model standard.
//
// DEUX FAMILLES, ET CE SONT CELLES DE THE DEVICE : « Port1 »… « Port24 » pour le
// cuivre, « SFP1 » et « SFP2 » pour les cages, chacune repartant de 1. C'est ce que
// /link.b sert dans `nm`, c'est ce que l'interface du switch affiche, et c'est donc ce
// que l'exploitant lit sur les deux ecrans qu'il compare.
//
// La numerotation CONTINUE cages comprises — « GE25 » pour la first cage, empruntee
// a la gamme Instant On — was ce que ce connecteur ecrivait. Ces groupes-ci sont ce
// que l'interface web DEPLIE pour enumerate les prises (`expandPorts`), donc changer le
// prefix ici est ce qui fait que the UI nomme les ports comme the device. Les noms
// de l'ancienne famille restent ACCEPTED en ingress par [Facade.Index] : les intentions
// already enregistrees continuent d'atterrir sur le bon port.
//
// Le connecteur RouterOS voisin fait exactement cela depuis sa first livraison, et
// pour la same raison — ses deux groupes sont `ether` a partir de 1 et `sfp-sfpplus` a
// partir de 1. Deux gammes du same constructeur named comme elles se nomment vaut
// mieux que deux gammes numerotees pareil et differemment de leur firmware.
func (id Identity) PortGroups() []map[string]any {
	var out = []map[string]any{}
	if id.CopperPorts > 0 {
		var label = fmt.Sprintf("1–%d", id.CopperPorts)
		if id.PoE {
			label += " · PoE"
		}
		out = append(out, map[string]any{
			"prefix": CopperPortPrefix, "count": id.CopperPorts, "from": 1,
			"speed": "1G", "poe": id.PoE, "label": label,
		})
	}
	if id.SFPPorts > 0 {
		var speed, name = "1G", "SFP"
		if id.SFPPlus {
			speed, name = "10G", "SFP+"
		}
		// `from` vaut 1 et non `SFPFirst + 1` : les cages ont leur PROPRE compteur, et
		// c'est ce qui distingue cette front panel de l'ancienne. Le label, lui, garde le
		// « + » quand la reference le porte — c'est aussi ce qui fait classer la prise en
		// SFP+ par internal/portrules.
		out = append(out, map[string]any{
			"prefix": SFPPortPrefix, "count": id.SFPPorts, "from": 1,
			"speed": speed, "poe": false, "label": name,
		})
	}
	return out
}
