package swos

import (
	"context"
	"crypto/rc4"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"meshcraft/internal/driver"
)

// La session d'administration d'un switch SwOS.
//
// Il n'y a PAS de session, et c'est le premier fait a integrate. SwOS
// s'authentifie par en-tete HTTP `Authorization: Digest` (MD5, qop=auth), pas par
// un formulaire qui poserait un cookie : chaque request re-present les
// identifiants, et rien ne survit d'une request a l'autre. Un connecteur qui
// « ouvrirait une session » et croirait ensuite y be encore se tromperait sur la
// nature de la plateforme.
//
// Cette mecanique n'est pas reimplemented ici : elle est dans le transport partage
// (`driver.WebSession.SetAuth` avec `driver.AuthDigest`), et c'est there qu'elle doit
// vivre — le calcul d'un digest Digest n'a rien de propre a MikroTik.
//
// Ce qui est propre a SwOS, en revanche, et qui vit ici :
//
//   - le SCHEMA est impose, pas negocie. `AuthDigest` et non `AuthAuto` : SwOS ne
//     sert que du Digest, et lui present  du Basic donnerait un second 401 qu'on
//     lirait comme un mot de passe faux — l'erreur la plus couteuse a diagnostiquer
//     sur un device dont on n'a pas la console ;
//   - la forme du corps des ecritures, qui n'est ni un formulaire ni du JSON
//     (cf. postRaw) ;
//   - tout est consigne dans un transcript. Sur une plateforme dont la
//     configuration est persistante d'office et sans filet (cf.
//     Capabilities.Commit), savoir AFTER COUP ce qui a been envoye n'est pas un
//     confort de developpeur : c'est la seule way de comprendre un failure sans
//     retourner sur site — et ce model n'a pas de serial port.

// `Step` n'est plus defini ici : c'est now un ALIAS du type du contrat
// (`type Step = driver.Step`, cf. contract.go). Les trois connecteurs en portaient
// une copie identique, chacun en ecrivant dans son commentaire qu'il recopiait la
// forme des autres « pour que l'API n'ait qu'une way de present  une pose ». Trois
// definitions identiques par convention tiennent jusqu'au jour where l'une gagne un
// champ : ce jour-there, la conversion silencieuse de la couche API le perd, et un ecart
// de configuration devient invisible. Le champ `Steps` ci-dessous n'a pas changed
// d'une ligne pour autant — c'est tout l'interet d'un alias.

// Admin est une session vers un switch SwOS.
type Admin struct {
	web *driver.WebSession
	// Steps est le transcript, dans l'ordre.
	Steps []Step
	// User est le compte used. Sur SwOS il n'y en a qu'un (cf.
	// Capabilities.Admin) — le champ existe pour que l'appelant n'ait pas a
	// distinguer cette plateforme des autres.
	User string
	// accepted est le candidat qui a ouvert cet access, tel qu'il a been fourni par
	// l'appelant (cf. Admin.Accepted). Renseigne par Driver.Authenticate, VIDE sur un
	// Admin obtenu par OpenAdmin — i.e. quand personne n'a presented de
	// candidat, ce qui est le cas de la discovery.
	//
	// Il porte un SECRET : rien de ce champ ne doit atterrir dans un transcript, qui
	// est fait pour be montre et conserve.
	accepted Candidate
	// prefix est le prefix de nommage des ports du model standard, tel que la cible
	// le porte (driver.Target.PortPrefix). VIDE tant que personne ne l'a dit, et c'est
	// portPrefix() qui applique alors le default rather que to write a l'aveugle : ce
	// connecteur NUMEROTE ses ports, et traduire « GE12 » en l'index 12 est ce qui
	// decide sur quel port writing atterrit.
	prefix string
	// ports est la geometry du model, lue une fois sur /link.b. Zero tant qu'elle
	// n'a pas been lue : aucune write ne se fait sans elle, le jeu de champs de
	// /fwd.b en dependant.
	ports int
	sfp   int
	// sfpFirst est l'index de la first cage SFP.
	sfpFirst int
}

func (a *Admin) step(ok bool, what string, detail ...string) {
	a.Steps = append(a.Steps, Step{What: what, Detail: strings.Join(detail, " "), OK: ok})
}

// OpenAdmin ouvre une session vers un switch SwOS.
//
// HTTP EN CLAIR, et ce n'est pas un oubli : SwOS n'ecoute que sur le port 80.
// Constate sur 10.99.0.36 (CSS326-24G-2S+, SwOS 2.17) — ni 443, ni 22, ni 23. La
// confidentialite du lien repose donc entirely sur le tunnel du proxy, et une
// tentative en HTTPS echouerait sans rien apprendre.
//
// `secret` peut be VIDE, et c'est le cas d'un device sorti d'usine : SwOS
// admet un mot de passe vide, l'reference device en portait un, et le
// Digest se calcule tres bien avec (cf. Capabilities.Admin.Notes).
func OpenAdmin(host, user, secret string, d driver.Dialer) *Admin {
	if user == "" {
		user = FixedAdminName
	}
	var web = driver.NewWebSession("http://"+host, d, 0)
	web.SetAuth(driver.AuthDigest, user, secret)
	return &Admin{web: web, User: user}
}

// DefaultPortPrefix est le prefix de la famille NUMEROTEE de meshcraft, celle que ce
// connecteur accepte encore en lecture et does not write plus.
//
// « GE », la same convention que le connecteur Instant On, avec une numerotation
// CONTINUE cages comprises : GE1 a GE24 le cuivre, GE25 / GE26 les deux cages d'un
// CSS326-24G-2S+. C'est ce que ce connecteur ECRIVAIT ; il written now les noms de
// the device (« Port1 », « SFP1 »), et ce prefix ne sert plus only relire les
// intentions already enregistrees sous l'ancienne convention. Le raisonnement complet est
// dans l'encadre de naming.go.
const DefaultPortPrefix = "GE"

// portPrefix rend le prefix de la famille numerotee a accepter sur cet access.
//
// Un DEFAULT et non un refus, parce que ce prefix n'est pas une information sur
// the device mais une convention de nommage de meshcraft : refuser de lire un
// device dont la cible n'a pas renseigne le champ empecherait de relever un
// switch tout neuf. Le contrat le dit ainsi : « Vide, ils appliquent leur propre default
// rather que to write a l'aveugle. »
func (a *Admin) portPrefix() string {
	if a.prefix == "" {
		return DefaultPortPrefix
	}
	return a.prefix
}

// facade rend la front panel NOMMEE de cet access : la geometry lue sur /link.b, plus les
// deux familles de noms que [Facade.Index] sait relire.
//
// Elle n'a de sens qu'AFTER une lecture de geometry (readAll ou Geometry) : avant, le
// nombre de ports vaut zero, donc aucun nom ne se traduit en index — ce qui est le bon
// refus, puisqu'write sans avoir lu la front panel serait write a l'aveugle.
func (a *Admin) facade() Facade {
	return NewFacade(a.ports, a.sfp, a.sfpFirst, a.portPrefix())
}

// Get lit une page et l'analyse comme un OBJET.
func (a *Admin) Get(ctx context.Context, page string) (*Page, error) {
	var body, err = a.raw(ctx, page)
	if err != nil {
		return nil, err
	}
	var obj, parseErr = ParseObject(body)
	if parseErr != nil {
		a.step(false, "page "+page+" illisible", parseErr.Error())
		return nil, fmt.Errorf("%s : %w", page, parseErr)
	}
	a.step(true, "page "+page+" lue",
		fmt.Sprintf("%d champ(s) : %s", len(obj.Keys()),
			strings.Join(obj.Keys(), ", ")))
	return obj, nil
}

// GetTable lit une page et l'analyse comme une TABLE. Une table vide est un
// result, pas une erreur.
func (a *Admin) GetTable(ctx context.Context, page string) ([]*Page, error) {
	var body, err = a.raw(ctx, page)
	if err != nil {
		return nil, err
	}
	var rows, parseErr = ParseTable(body)
	if parseErr != nil {
		a.step(false, "table "+page+" illisible", parseErr.Error())
		return nil, fmt.Errorf("%s : %w", page, parseErr)
	}
	a.step(true, "table "+page+" lue", fmt.Sprintf("%d ligne(s)", len(rows)))
	return rows, nil
}

// errCredentialsRefused est le refus D'IDENTIFIANTS, distingue de tout autre failure.
//
// Une SENTINELLE et pas seulement un message, parce que deux appelants ont besoin de
// faire la difference et qu'aucun ne peut la faire sur du texte : Driver.Authenticate
// doit passer au candidat SUIVANT quand the device refuse, et s'STOP net quand
// il ne repond pas — insister avec les autres candidats ne prouverait rien et
// brouillerait le diagnostic. Driver.Identify a exactement le same besoin.
//
// Comparer des strings pour cela reviendrait a faire dependre le nombre d'essais
// consommes sur un device d'un label qu'on est libre de reformuler. Le message
// rendu a l'exploitant, lui, ne change pas : « /sys.b : identifiants refused par
// the device ».
var errCredentialsRefused = errors.New("identifiants refused par the device")

// raw lit une page et rend son corps, en refusant ce qui n'en est visiblement pas
// une.
//
// Le control de longueur n'est pas de la paranoia : sur cette plateforme, un
// chemin inconnu rend l'interface web ENTIRE en HTTP 200 (44 807 octets sur le
// reference model). Sans ce garde-fou, une faute de frappe dans un nom de page
// se presenterait comme une response valide, et l'analyseur signalerait une syntaxe
// invalide there where le vrai default est le chemin.
func (a *Admin) raw(ctx context.Context, page string) (string, error) {
	var resp, err = a.web.Get(ctx, page)
	if err != nil {
		a.step(false, "lecture de "+page, err.Error())
		return "", err
	}
	if resp.Status == 401 {
		a.step(false, "authentification refused sur "+page,
			fmt.Sprintf("HTTP 401 — compte « %s » ; challenge : %s", a.User,
				header(resp.Headers, hdrAuthenticate)))
		return "", fmt.Errorf("%s : %w", page, errCredentialsRefused)
	}
	if resp.Status != 200 {
		a.step(false, "lecture de "+page, fmt.Sprintf("HTTP %d", resp.Status))
		return "", fmt.Errorf("%s : HTTP %d", page, resp.Status)
	}
	if resp.Truncated {
		// Une page tronquee est un objet dont l'accolade fermante manque :
		// l'analyseur le dirait, mais avec un message qui accuserait la syntaxe.
		a.step(false, "response tronquee sur "+page,
			fmt.Sprintf("%d octets, borne atteinte", len(resp.Body)))
		return "", fmt.Errorf("%s : response tronquee", page)
	}
	if strings.Contains(resp.Body, "<!doctype") || strings.Contains(resp.Body, "<html") {
		a.step(false, "chemin inconnu de the device : "+page,
			fmt.Sprintf("HTTP 200 mais l'interface web complete (%d octets) a been servie "+
				"a la place des data — sur SwOS, un chemin qui n'existe pas ne rend pas 404",
				len(resp.Body)))
		return "", fmt.Errorf("%s : page inexistante sur ce firmware "+
			"(the device a servi son interface web)", page)
	}
	return resp.Body, nil
}

// Backup rapatrie la configuration ENTIRE en une request.
//
// LECTURE SEULE, et le filet de security de cette plateforme. SwOS applique et
// persiste d'un seul geste (cf. Capabilities.Commit) : il n'existe aucun rollback
// gratuit, et la seule way de revenir en arriere after une write malheureuse
// est de restaurer un fichier de sauvegarde. Le prendre AVANT to write ne costs
// qu'une request et environ trois kilo-octets.
//
// La restauration, elle, n'est pas ici : c'est un POST multipart vers la same
// URL, qu'aucun essai n'a pu valider (cf. le rapport de livraison). La proposer
// sans l'avoir verified serait promettre un filet qui n'existe peut-be pas.
func (a *Admin) Backup(ctx context.Context) (string, error) {
	var body, err = a.raw(ctx, PageBackup)
	if err != nil {
		return "", err
	}
	// The device rend un corps VIDE quand la configuration est celle d'usine —
	// son interface affiche alors « Default configuration. Nothing to save. ».
	// Ce n'est pas un failure, et le confondre avec une lecture ratee ferait refuser
	// to write sur un device fraichement remis a zero, i.e. exactement
	// celui qu'on veut adopter.
	if strings.TrimSpace(body) == "" {
		a.step(true, "aucune sauvegarde a prendre",
			"the device porte la configuration d'usine : il n'y a rien a restaurer")
		return "", nil
	}
	a.step(true, "sauvegarde de configuration prise avant write",
		fmt.Sprintf("%d octets — c'est le SEUL retour arriere de cette plateforme", len(body)))
	return body, nil
}

// Geometry lit la geometry du model : nombre de ports, nombre de cages SFP,
// index de la first cage.
//
// Necessaire AVANT toute write sur /fwd.b, dont le jeu de champs inscriptible
// depend du nombre de ports (cf. FwdWritableFields). La lire rather que la deduire
// du nom du model est le choix sur : c'est the device qui sait combien de ports
// il a.
func (a *Admin) Geometry(ctx context.Context) (int, int, int, error) {
	if a.ports > 0 {
		return a.ports, a.sfp, a.sfpFirst, nil
	}
	var link, err = a.Get(ctx, PageLink)
	if err != nil {
		return 0, 0, 0, err
	}
	var ports, sfp, first, geoErr = geometryOf(link)
	if geoErr != nil {
		a.step(false, "geometry du model illisible", geoErr.Error())
		return 0, 0, 0, geoErr
	}
	a.ports, a.sfp, a.sfpFirst = ports, sfp, first
	a.step(true, "geometry du model lue",
		fmt.Sprintf("%d ports dont %d cage(s) SFP, la first a l'index %d",
			ports, sfp, first))
	return ports, sfp, first, nil
}

// geometryOf extrait la geometry de /link.b. Fonction pure, donc verifiable sur
// une capture.
func geometryOf(link *Page) (int, int, int, error) {
	var prt, ok = link.Num(FieldPortCount)
	if !ok || prt == 0 || prt > 512 {
		return 0, 0, 0, fmt.Errorf("/link.b ne dit pas combien de ports a cet device "+
			"(champ %q absent ou hors bornes)", FieldPortCount)
	}
	// Le nombre de cages est facultatif : un model tout cuivre n'en a pas.
	var sfp, _ = link.Num(FieldSFPCount)
	if sfp > prt {
		return 0, 0, 0, fmt.Errorf("/link.b annonce %d cage(s) SFP pour %d port(s)", sfp, prt)
	}
	// `sfpo` absent : l'interface de the device place les cages a la FIN.
	var first, hasOffset = link.Num(FieldSFPOffset)
	if !hasOffset {
		first = prt - sfp
	}
	if first > prt {
		return 0, 0, 0, fmt.Errorf("/link.b place la first cage SFP a l'index %d "+
			"pour %d port(s)", first, prt)
	}
	return int(prt), int(sfp), int(first), nil
}

// postRaw envoie une write a the device.
//
// Le geste est celui de son interface (`Q` dans son JavaScript) : un POST vers
// l'URL DE LA PAGE, dont le corps est l'objet complet dans la syntaxe des pages —
// ni un formulaire, ni du JSON. Les octets envoyes ici sont, a l'octet pres, ceux
// que le navigateur envoie : `PostForm` du transport partage transmet le corps TEL
// QUEL, sans le reencoder.
//
// Le type de contenu est celui de l'interface de the device, `text/plain`, et
// non le `application/x-www-form-urlencoded` d'un formulaire : c'est pour ce
// connecteur que `PostText` a been ajoute au transport partage. Rien n'indique que
// SwOS lise cet en-tete — le corps part dans un evaluateur — mais rien ne le
// prouve non plus, et sur cette plateforme un refus est MUET. Envoyer ce que
// the device a toujours vu costs moins cher que de le decouvrir sur le terrain.
//
// Ce que the device repond quand il accepte : 200 et un corps VIDE. Son interface
// ne lit rien d'autre que le code HTTP — il n'y a donc aucun message d'erreur a
// traduire, et la seule verification possible est de RELIRE la page.
func (a *Admin) postRaw(ctx context.Context, page, body, what string) error {
	return a.post(ctx, page, body, what, true)
}

// postRawSecret est postRaw pour une page dont le corps porte un SECRET.
//
// Le transcript est fait pour be lu, montre et conserve : une community SNMP ou un
// digest de mot de passe qui y atterrit y reste. Seule la taille du corps est
// consignee — assez pour verify qu'une write est bien partie, pas assez pour
// divulguer ce qu'elle portait.
func (a *Admin) postRawSecret(ctx context.Context, page, body, what string) error {
	return a.post(ctx, page, body, what, false)
}

func (a *Admin) post(ctx context.Context, page, body, what string, logBody bool) error {
	var sent = fmt.Sprintf("%d octets (corps non consigne : il porte un secret)", len(body))
	if logBody {
		sent = "corps envoye : " + excerpt(body, 400)
	}
	var resp, err = a.web.PostText(ctx, page, body)
	if err != nil {
		a.step(false, what, "POST "+page+" : "+err.Error())
		return err
	}
	if resp.Status != 200 {
		a.step(false, what+" — REFUSED par the device",
			fmt.Sprintf("POST %s → HTTP %d | %s", page, resp.Status, sent))
		return fmt.Errorf("%s : the device a repondu HTTP %d", page, resp.Status)
	}
	a.step(true, what, fmt.Sprintf("POST %s → HTTP 200 | %s", page, sent))
	return nil
}

// --- Le mot de passe d'administration ---

// FixedAdminName est le SEUL nom de compte de SwOS.
//
// Ce n'est pas une hypothese de prudence : la plateforme n'a pas de notion de
// compte. Le formulaire de changement de mot de passe ne porte que trois champs
// (ancien, nouveau, confirmation), la page qui le receives ne contient qu'une key
// (`{pwd:”}`), et la sauvegarde de configuration ENTIRE de the device ne
// mentionne aucun nom d'utilisateur — observed sur CSS326-24G-2S+ en SwOS 2.17,
// testdata/backup.swb. Il n'y a donc rien a nommer.
//
// Consequence pour meshcraft, et elle compte : le nom de compte unifie du parc
// (`mcadmin`) ne peut PAS be applied ici. C'est le role de
// AdminAccount.EffectiveUsername de le resoudre en `admin`, et de
// Capabilities.Admin.Custom=false de l'annoncer avant qu'une pose ne l'apprenne.
const FixedAdminName = "admin"

// maxPasswordLen est la longueur maximale admise, imposee par l'interface de
// the device : elle refuse au-dela de 15 caracteres, pour l'ancien
// (« Bad old password ») comme pour le nouveau (« New password is too long »).
const maxPasswordLen = 15

// SetPassword change le mot de passe d'administration.
//
// NON VERIFIED SUR HARDWARE. Le geste et l'algorithme sont observed a la lettre dans
// le JavaScript de the device, mais aucune write n'a pu be menee depuis
// l'hote de developpement — l'outil access a travers le tunnel est en lecture
// seule par decision d'architecture. Voir le rapport de livraison pour la commande
// exacte a passer.
//
// Le geste : `POST /!pwd.b`, corps `{pwd:'<64 chiffres hex>'}`.
//
// Les codes de retour de the device, tels que son interface les distingue :
// 200 accepted, **405 ancien mot de passe incorrect**. Ce 405 est le seul message
// d'erreur utile de toute la plateforme, et le traduire correctement evite de
// conclure a une panne there where il ne manque qu'un secret.
//
// The device RECHARGE ensuite sa page : le nouveau secret prend effet
// immediatement, et la request suivante doit le present .
func (a *Admin) SetPassword(ctx context.Context, oldSecret, newSecret string) error {
	var blob, err = PasswordBlob(newSecret, oldSecret)
	if err != nil {
		a.step(false, "mot de passe refused avant envoi", err.Error())
		return err
	}
	// La valeur n'est pas du texte a hex-encoder mais un digest ALREADY en
	// hexadecimal : il se pose brut. Le reencoder doublerait l'encodage et
	// the device lirait un secret de 64 caracteres qui n'est pas le bon.
	var page = NewPage()
	page.set("pwd", Value{Kind: KindStr, Raw: blob})

	var resp, postErr = a.web.PostText(ctx, PagePassword, page.Encode())
	if postErr != nil {
		a.step(false, "changement du mot de passe d'administration", postErr.Error())
		return postErr
	}
	switch resp.Status {
	case 200:
		// The device porte now le nouveau secret : la session doit le
		// present , sinon toute lecture suivante repondrait 401 et passerait pour
		// une panne.
		a.web.SetAuth(driver.AuthDigest, a.User, newSecret)
		a.step(true, "mot de passe d'administration changed",
			"POST "+PagePassword+" → HTTP 200 ; la session present now le nouveau secret")
		return nil
	case 405:
		a.step(false, "ancien mot de passe incorrect",
			"POST "+PagePassword+" → HTTP 405 : the device a rejete l'ANCIEN secret, "+
				"le nouveau n'a pas been applied")
		return fmt.Errorf("the device refuse l'ancien mot de passe (HTTP 405)")
	}
	a.step(false, "changement du mot de passe REFUSED",
		fmt.Sprintf("POST %s → HTTP %d", PagePassword, resp.Status))
	return fmt.Errorf("changement de mot de passe refused (HTTP %d)", resp.Status)
}

// PasswordBlob calcule ce que SwOS attend dans `{pwd:…}`.
//
// L'algorithme est observed a la lettre dans le JavaScript de the device
// (fonctions `sa` et `ra` de son index.html, CSS326-24G-2S+ en SwOS 2.17) :
//
//	tampon = octets(ancien) + 0x00 + octets(nouveau), complete de 0x00 a 32 octets
//	key    = ancien, ou « * » si l'ancien est VIDE, repete puis coupe a 16 octets
//	flux   = RC4(key), sans rejet initial
//	pour i de 0 a 63 : tampon[i mod 32] ^= flux[i]
//	result = hexadecimal des 32 octets
//
// Deux details qu'on ne devinerait pas et qui decident du success :
//
//   - la key RC4 est l'ANCIEN mot de passe. Un device sorti d'usine en a un
//     vide, hence le remplacement par « * » — sans lui, RC4 n'aurait pas de key, et
//     c'est tres exactement le cas de l'reference device, dont le mot de
//     passe est vide ;
//   - le flux est applique SOIXANTE-QUATRE fois sur trente-deux octets, donc deux
//     tours. S'stop a 32 produit un digest plausible que the device rejette.
//
// Les caracteres doivent be de l'ASCII : l'interface de the device construit
// ses octets avec `charCodeAt`, qui rend des unites UTF-16. Au-dela de 127, ce que
// le navigateur envoie et ce que Go enverrait divergeraient, et le mot de passe
// applied ne serait pas celui qu'on croit — sur une plateforme sans serial port, cela
// se repare avec un trombone. La verification est donc faite ICI, avant l'envoi.
func PasswordBlob(newSecret, oldSecret string) (string, error) {
	var err = checkPassword("nouveau mot de passe", newSecret)
	if err != nil {
		return "", err
	}
	if err = checkPassword("ancien mot de passe", oldSecret); err != nil {
		return "", err
	}

	var buf = make([]byte, 32)
	copy(buf, oldSecret)
	// L'octet NUL separates l'ancien du nouveau ; il est already en place puisque le
	// tampon part a zero. Le nouveau commence juste after.
	if len(oldSecret)+1 < len(buf) {
		copy(buf[len(oldSecret)+1:], newSecret)
	}

	var key = oldSecret
	if key == "" {
		key = "*"
	}
	for len(key) < 16 {
		key += key
	}
	key = key[:16]

	var stream, rcErr = rc4.NewCipher([]byte(key))
	if rcErr != nil {
		return "", rcErr
	}
	// XORKeyStream sur des zeros rend le flux lui-same : c'est la way la plus
	// directe d'obtenir les 64 octets de flux qu'exige l'algorithme.
	var ks = make([]byte, 64)
	stream.XORKeyStream(ks, ks)
	var i int
	for i = 0; i < 64; i++ {
		buf[i&31] ^= ks[i]
	}
	return hex.EncodeToString(buf), nil
}

func checkPassword(label, s string) error {
	if len(s) > maxPasswordLen {
		return fmt.Errorf("%s : %d caracteres, the device en accepte %d au plus",
			label, len(s), maxPasswordLen)
	}
	var i int
	for i = 0; i < len(s); i++ {
		if s[i] > 0x7f {
			return fmt.Errorf("%s : SwOS n'accepte que de l'ASCII, et le caractere a la "+
				"position %d n'en est pas — le poser rendrait the device inaccessible", label, i+1)
		}
	}
	return nil
}

// Note ajoute au transcript ce qu'un chemin a repondu. Sert a la reconnaissance
// d'un firmware inconnu : distinguer une page absente d'une page vide sans ouvrir
// de navigateur.
func (a *Admin) Note(page string, status int, body string) {
	a.step(status == 200 && !strings.Contains(body, "<!doctype"),
		"response de "+page,
		fmt.Sprintf("HTTP %d, %d octets : %s", status, len(body), excerpt(body, 200)))
}
