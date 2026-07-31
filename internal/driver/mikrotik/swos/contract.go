package swos

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"meshcraft/internal/driver"
)

// L'adoption du contrat access (internal/driver/connector.go) par ce connecteur.
//
// Ce fichier ne contient AUCUN savoir de plateforme : everything that knows how to talk to a
// SwOS vit dans admin.go, state.go, apply.go, snmp.go, inspect.go et pages.go. Ce
// qu'il porte est la traduction entre ce que la couche API demande et ce que cette
// plateforme sait faire — et, tout aussi important, la liste written de ce qu'elle ne
// sait pas faire.
//
// # POURQUOI CE CONNECTEUR TIENT LE CONTRAT SANS RIEN SIMULER
//
// Le contrat a been written en partie CONTRE cette plateforme, et c'est ce qui le rend
// tenable ici. Deux decisions viennent directement de SwOS :
//
//   - [driver.Connector.Authenticate] est UN SEUL appel, et non un `Open` suivi d'un
//     `Login`. SwOS n'a AUCUNE session : authentication is a header
//     `Authorization: Digest` re-presented a chaque request, rien ne survit d'une
//     request a l'autre, there is nothing to open and nothing to close. Un contrat en deux
//     temps aurait force ce connecteur a write un `Login` qui ne fait rien — ou qui
//     « verifies » en tapant une page sans que l'appelant sache que c'est ca qui se
//     passe. Ici, la verification est explicite et assumee : Authenticate LIT /sys.b,
//     et c'est cette lecture qui fait dire « verified » ;
//   - [driver.ApplyResult.Backup] existe pour cette plateforme et pour elle seule.
//     SwOS applique et persiste d'un seul geste (`Commit: immediate`) : une coupure
//     d'alimentation n'annule rien, et le SEUL retour arriere est la copie de
//     configuration prise juste avant writing. ApplyPorts la prend ; elle ne se
//     reconstitue pas after.
//
// # TWO CONTRACT METHODS ARE NOT IN THIS FILE
//
// [driver.Admin.ReadState] vit dans state.go et [driver.Admin.ApplyPorts] dans
// apply.go, parce qu'elles ne sont pas des traductions : elles SONT l'implementation,
// et les separate du raisonnement qui les justifie — la rule d'tagging de SwOS
// pour l'une, le garde-fou de l'uplink pour l'autre — ferait que l'un des deux
// changerait un jour sans l'autre.

// --- Les types du contrat, ALIASED and not redefined ---
//
// Un ALIAS (`=`) and not a definition : les champs, les fonctions pures et les
// soixante-huit tests de ce paquet continuent de compiler tels quels, et ce que ce
// connecteur manipule EST le type que la couche API consomme. Aucune conversion,
// donc aucun champ perdu en silence le jour where le contrat en gagne un — c'est tres
// exactement ce que l'en-tete de internal/driver/state.go reproche aux trois copies
// qui existaient avant lui.
//
// TrapDestination et TrapResult sont aliases bien que ce connecteur does not implement PAS
// [driver.TrapPusher] (voir plus bas pourquoi). C'est deliberate et ca costs zero : le
// jour where quelqu'un will write ici une fonction qui parle de traps — ne serait-ce que
// pour expliquer un refus — il emploiera la forme du contrat au lieu d'en inventer
// une troisieme.
type (
	Step            = driver.Step
	VLAN            = driver.VLAN
	PortState       = driver.PortState
	State           = driver.State
	ApplyResult     = driver.ApplyResult
	Candidate       = driver.Candidate
	SNMPIdentity    = driver.SNMPIdentity
	SNMPResult      = driver.SNMPResult
	TrapDestination = driver.TrapDestination
	TrapResult      = driver.TrapResult
)

// Identity, en revanche, is NOT aliased, et c'est le contrat lui-same qui le
// demande : « Platform-specific fields do not belong here — version
// du chargeur boot, revision de carte, index de la first cage optique. Les
// connecteurs gardent leur propre type identity, plus riche, et remplissent
// celui-ci pour la couche API. »
//
// Ce n'est pas une preference de style, c'est une contrainte de compilation : le type
// de ce paquet porte cinq champs que le contrat n'a pas (`Revision`, `BoardName`,
// `SFPFirst`, `MgmtIP`, `MgmtVLAN`) et surtout une METHOD, `PortGroups()`, que la
// discovery appelle already (internal/discovery/probe.go). Go interdit d'attacher une
// method a un type d'un autre paquet : aliaser `Identity` aurait donc casse la
// discovery, qui est hors du scope de ce lot. La traduction se fait dans
// [Identity.Contract] (inspect.go), en un seul endroit et en disant ce qu'elle perd.

// --- Les assertions de CONFORMITY ---
//
// Elles ne servent a rien a l'execution, et c'est le but : elles font echouer la
// COMPILATION le jour where le contrat bouge. L'alternative — une assertion de type
// faite par l'appelant au moment to write — fails en silence sur un device en
// production, i.e. au pire endroit et au pire moment. Une capability
// facultative absente doit se lire AVANT le premier paquet ; une capability qu'on croit
// present et qui a cesse de l'be doit se lire a la construction du binaire.
//
// Une ligne par interface ACTUALLY implemented, et aucune pour les autres : cette
// liste est donc aussi la response a « qu'est-ce que ce connecteur sait faire ? ».
var (
	_ driver.Connector  = (*Driver)(nil)
	_ driver.Identifier = (*Driver)(nil)

	_ driver.Admin            = (*Admin)(nil)
	_ driver.RawReader        = (*Admin)(nil)
	_ driver.SNMPConfigurator = (*Admin)(nil)
)

// --- CE QUE CE CONNECTEUR DOES NOT IMPLEMENT PAS, ET POURQUOI ---
//
// Ces absences sont un LIVRABLE, pas une liste de taches. Chacune repond a la seule
// question qui matters un exploitant — « y a-t-il quelque chose a attendre ? » — et
// la response est non pour les cinq. Le mecanisme choisi par le contrat (une interface
// facultative, testee par assertion de type) est ce qui permet a the UI de refuser
// AVANT de joindre the device, en nommant le geste et la raison.
//
// [driver.AccountManager] — LA PLATEFORME N'A AUCUNE NOTION DE COMPTE.
//
// Pas « pas encore » : pas du tout. Trois faits l'establish, et ce sont des
// constats, pas des deductions de prudence — le formulaire de changement de mot de
// passe de l'embedded interface ne porte que TROIS champs (ancien, nouveau,
// confirmation) ; la page qui le receives ne contient qu'une seule key, `{pwd:''}` ; et
// la sauvegarde de configuration ENTIRE de the device (testdata/backup.swb, observed
// sur le CSS326-24G-2S+ de reference) ne mentionne AUCUN nom d'utilisateur. Il n'y a
// litteralement rien a nommer, donc rien a create, renommer ou supprimer.
//
// Consequence pour l'appelant : le nom de compte unifie du parc n'est pas posable sur
// un CSS. C'est [driver.AdminAccount.EffectiveUsername] qui le resout en « admin »
// AVANT toute tentative, et `Capabilities().Admin.Custom = false` qui l'annonce.
// L'absence de cette interface est le SECOND verrou, pas le premier : si l'appelant
// se retrouve a vouloir create un compte ici, c'est que la capability a been ignored en
// amont.
//
// [driver.TrapPusher] — /snmp.b N'A AUCUN CHAMP DE DESTINATAIRE.
//
// Quatre champs en tout : activation, community, contact, localisation. Aucun
// destinataire, dans aucun onglet, ni dans la sauvegarde de configuration. Et ce
// n'est pas une page masquee comme il en existait sur la gamme Instant On :
// l'interface web COMPLETE d'un SwOS tient en un seul fichier de 44 807 octets, qui
// definit tous ses onglets et tous leurs champs — enumeration exhausts la question.
// Un CSS se supervise donc par INTERROGATION : l'information est there, c'est la
// reactivity qui manque. Declare cette interface would write une ligne dans un
// device qui ne s'en servirait jamais, et ferait ouvrir 162/udp sur le proxy pour
// ecouter un silence — l'exact contraire d'un service rendu. Consistent avec
// `Capabilities().Monitoring.Traps = false`, qui est la same information vue de
// l'autre bout.
//
// [driver.Persister] — `Commit: immediate` : IL N'Y A RIEN A SAUVER.
//
// Les seuls boutons de l'embedded interface sont « Apply All », qui poste la page,
// et « Discard Changes », qui se contente de RELIRE la page depuis the device — un
// annulateur purement local, qui n'annule rien de ce qui est parti. Aucune
// configuration de demarrage distincte, aucun commit a confirmer. Ne pas implement
// cette interface est donc une INFORMATION exacte, et elle a un corollaire dur : il
// n'y a pas non plus de FILET. There where l'Instant On offre un retour arriere gratuit
// (ne pas sauver, couper le courant), ici la seule marche arriere est
// [driver.ApplyResult.Backup].
//
// [driver.MgmtMover] — « ALLOW FROM VLAN » N'ACCEPTE QU'UN SEUL VLAN.
//
// Ce contrat-there exige de pouvoir AJOUTER une voie d'administration, constater qu'elle
// repond, puis basculer : trois temps verifiable. SwOS ne porte qu'une seule adresse
// de management et un seul `avln` (/sys.b) : la bascule est un SAUT, sans period where
// les deux voies coexistent. [driver.MgmtJumper] is therefore what this connector
// implements (JumpMgmt + backup), never MgmtMover.
//
// [driver.Onboarder] — RIEN A BASCULER : un SwOS est administrable au deballage.
//
// Cette interface existe pour un Instant On sorti d'usine, qui attend le portail
// cloud du constructeur et qu'il faut convaincre d'abandonner. SwOS n'a pas
// d'equivalent : compte « admin », mot de passe vide, interface web sur le port 80,
// et c'est tout.

// --- Le connecteur ---

// Authenticate rend un access d'administration VERIFIED sur un switch SwOS.
//
// THERE IS NO SESSION TO OPEN HERE, et c'est le fait a integrate avant tout le
// reste. SwOS authenticates via header `Authorization: Digest` (MD5, qop=auth)
// re-presented a CHAQUE request : rien ne survit d'un appel au suivant, aucun cookie,
// aucun prefix d'URL, aucun compteur de clients. Ce que cette method rend n'est
// donc pas une connexion mais un porteur d'identifiants dont on a OBSERVED qu'ils
// passent — hence le nom `Admin` (« verified ») et l'absence de tout `Close` dans le
// contrat.
//
// C'est precisely ce que le contrat en un seul appel permet d'exprimer sans faire
// semblant. Un `Open()` puis `Login()` aurait oblige ce connecteur a write un
// `Login` vide, or to hit a page in secret : l'appelant aurait alors cru ouvrir
// une session there where il n'y en a aucune, et aurait fini par en liberer une — geste sans
// objet ici, et promesse fausse sur une ressource rare ailleurs.
//
// COMMENT « VERIFIED » EST OBTENU : par une lecture de /sys.b. C'est la page la moins
// chere de la plateforme (780 octets) et the only one whose content proves at once that
// the credentials work AND that we are talking to a SwOS — l'analyseur refuse ce qui
// n'est pas une page `*.b`, ce qui attrape le platform trap de cette plateforme : un
// chemin inconnu rend l'interface web ENTIRE en HTTP 200.
//
// CE QUE CA COSTS A THE DEVICE : DEUX requests HTTP par candidat essaye. Le Digest
// se renegocie a chaque fois — un 401 qui porte le challenge, puis la request
// authentifiee — et le transport partage ne conserve pas le nonce d'une request a
// l'autre. Un `Authenticate` qui reussit du premier coup costs donc deux requests ;
// avec trois candidats dont le dernier passe, six. Aucune does not write quoi que ce soit.
//
// CANDIDATES ARE TRIED IN RECEIVED ORDER, AND NONE IS REPLAYED. L'ordre vient
// de l'appelant (generation observed d'abord, compte d'usine en dernier) et ce
// connecteur ne le reorders pas, n'en fabrique pas « pour voir », et ne retente pas
// un candidat qui a echoue. Le nombre d'essais est un BUDGET : sur un device sans
// SSH ni serial port, exhaust les essais revient a fermer la seule porte. SwOS n'a, a
// notre connaissance, aucun verrouillage de compte — mais « to our knowledge » ne
// justifie pas de depenser ce qui ne nous appartient pas.
//
// UN FAILURE NE REND AUCUN Admin, deliberately : un access non verified qui circulerait
// derriere une erreur finirait par servir a write. Le transcript des tentatives part
// donc dans le MESSAGE d'erreur, qui distingue les deux issues que l'exploitant doit
// pouvoir separate — « aucun identifiant accepted » (the device repond, il refuse) et
// « the device ne repond pas » (rien n'a been essaye au-dela).
func (d *Driver) Authenticate(ctx context.Context, tgt driver.Target,
	cands []Candidate) (driver.Admin, error) {
	if tgt.Host == "" {
		return nil, errors.New("no address to reach : ce connecteur ne devine pas " +
			"par where un device s'atteint")
	}
	// Aucun candidat : on ne touche pas the device. Essayer « admin / mot de passe
	// vide » parce que c'est l'factory state serait fabriquer un candidat que personne
	// n'a requested — exactement ce que le contrat interdit.
	if len(cands) == 0 {
		return nil, errors.New("no credential to try : SwOS n'a qu'un compte, " +
			"sans nom, et ce connecteur n'en fabrique pas — pas same le couple d'usine " +
			"(« admin », mot de passe vide), qui doit be propose comme candidat pour " +
			"be tente")
	}

	var refus []string
	var i int
	for i = 0; i < len(cands); i++ {
		var cand = cands[i]
		var a = OpenAdmin(tgt.Host, cand.Username, cand.Secret, tgt.Dialer)
		a.prefix = tgt.PortPrefix
		a.accepted = cand
		// Le secret ne part JAMAIS au transcript : celui-ci est fait pour be montre,
		// conserve et rattache au pipeline. Le compte et la RAISON de la position du
		// candidat suffisent a comprendre after coup ce qui a ouvert the device.
		a.step(true, "essai des identifiants « "+a.User+" »", cand.Why)

		var _, err = a.Get(ctx, PageSys)
		if err == nil {
			a.step(true, "connected as « "+a.User+" »",
				"identifiants verified par une lecture de "+PageSys+" — "+cand.Why+
					". Aucune session n'a been ouverte : SwOS re-present le Digest a chaque "+
					"request, il n'y a rien a fermer")
			return a, nil
		}
		if !errors.Is(err, errCredentialsRefused) {
			// The device ne repond pas, ou ne repond pas ce qu'un SwOS repond. Insister
			// avec les candidats suivants ne prouverait rien et brouillerait le
			// diagnostic : ce n'est pas un probleme d'identifiants.
			return nil, fmt.Errorf("the device %s ne repond pas comme un switch SwOS, "+
				"les identifiants n'y sont pour rien : %w — %s", tgt.Host, err,
				strings.Join(transcriptLines(a.Steps), " | "))
		}
		refus = append(refus, fmt.Sprintf("« %s » (%s) : refused", cand.Username, cand.Why))
	}
	return nil, fmt.Errorf("aucun des %d identifiants proposes n'a been accepted par %s "+
		"(the device repond, il refuse) : %s. SwOS n'a qu'un compte, sans nom, et son "+
		"secret n'est pas recuperable : le perdre impose une factory reset d'usine sur "+
		"site, ce model n'ayant ni SSH, ni Telnet, ni serial port",
		len(cands), tgt.Host, strings.Join(refus, " ; "))
}

// WritesExtra dit si ce connecteur pose l'intention named, hors appartenance VLAN.
//
// Sur le Driver et non sur l'Admin, comme le contrat l'impose : la response ne depend
// ni d'un model ni d'un access, et l'appelant en a besoin AVANT de joindre
// the device — ce que la plateforme ne sait pas write doit be annonce same quand
// la pose fails.
//
// La declaration elle-same est dans capabilities.go, a cote du (non-)code qui pose :
// elle est VIDE, et son commentaire dit ce qui est instructif — la raison n'est pas
// l'absence de ces fonctions sur SwOS (il en a trois sur quatre), c'est qu'aucune n'a
// pu be verified en write sur du hardware, et que chacune vit dans une page qui
// porte aussi les champs capables de couper access.
func (d *Driver) WritesExtra(name string) bool { return WritesExtra(name) }

// Identify dit quel device repond a une adresse.
//
// LA BONNE SURPRISE DE CETTE PLATEFORME, et la raison pour laquelle cette interface
// facultative est implemented ici : le realm du challenge Digest porte la REFERENCE
// EXACTE du model, et il est lisible SANS LE MOINDRE IDENTIFIANT.
//
//	HTTP/1.1 401 Unauthorized
//	WWW-Authenticate: Digest realm="CSS326-24G-2S+", qop="auth", nonce="4753ad9d"
//
// Hence l'ordre de cette method, qui n'est pas negociable : le model est acquis et
// RENDU d'abord, l'erreur ne vient qu'after. « C'est un CSS326-24G-2S+ dont on n'a pas
// le mot de passe » est un result utile — il donne les capabilities, donc la
// geometry de front panel, donc de quoi ranger the device dans l'inventaire ; « failure »
// n'en est pas un. Renvoyer l'un pour l'autre ferait perdre a la discovery la seule
// chose que cette gamme offre gratuitement.
//
// Ce que le realm ne donne PAS et qui exige d'entrer : la serial (le seul champ qui
// fasse foi pour reconnaitre un device a une autre adresse), le firmware, le nom
// systeme, la MAC et la geometry reelle. Hence les candidats, essayes DANS L'ORDRE et
// jamais rejoues, exactement comme dans [Driver.Authenticate].
func (d *Driver) Identify(ctx context.Context, tgt driver.Target,
	cands []Candidate) (driver.Identity, error) {
	var model, err = realmModel(ctx, tgt.Host, tgt.Dialer)
	if err != nil {
		return driver.Identity{}, err
	}
	// Ce qui est acquis a la porte, avant toute tentative.
	var partial = Identity{
		Model:  model,
		Family: FamilyOf(model),
		PoE:    ModelHasPoE(model),
		Source: "digest_realm",
	}
	if len(cands) == 0 {
		return partial.Contract(), fmt.Errorf("model identifie sans identifiant (%s, "+
			"lu dans le realm du challenge Digest) ; la serial, le firmware et la front panel "+
			"exigent une lecture authentifiee, et aucun identifiant n'a been propose", model)
	}

	var lastErr error
	var i int
	for i = 0; i < len(cands); i++ {
		var admin = OpenAdmin(tgt.Host, cands[i].Username, cands[i].Secret, tgt.Dialer)
		// Une copie par tentative : un remplissage a half fait par un candidat refused
		// ne doit pas se melanger a celui du suivant.
		var full = partial
		var fillErr = admin.fillIdentity(ctx, &full)
		if fillErr == nil {
			return full.Contract(), nil
		}
		if !errors.Is(fillErr, errCredentialsRefused) {
			// On est ENTRE — les identifiants passent — mais une page a manque. Ce qui a
			// been lu est plus riche que le realm : c'est `full` qu'on rend, pas `partial`.
			return full.Contract(), fmt.Errorf("model identifie (%s) mais lecture "+
				"incomplete : %w", model, fillErr)
		}
		lastErr = fillErr
	}
	return partial.Contract(), fmt.Errorf("model identifie (%s) mais aucun des %d "+
		"identifiants proposes n'a been accepted : la serial et le firmware restent "+
		"inconnus. Dernier refus : %w", model, len(cands), lastErr)
}

// --- Access d'administration ---

// Connector rend identity du connecteur qui a produit cet access.
//
// Sert aux MESSAGES, et ca suffit a le justifier : « cette plateforme ne sait pas
// declare de destinataire de traps » n'aide personne si the UI ne dit pas de quelle
// plateforme il parle. Evite aussi a l'appelant de promener le nom du connecteur en
// parallele de access — deux variables qui finiraient par designer deux devices
// differents.
func (a *Admin) Connector() driver.Info { return (&Driver{}).Info() }

// Account est le compte qui a ACTUALLY ouvert access.
//
// Sur SwOS, c'est toujours le same et il n'a pas de nom : la plateforme n'a aucune
// notion de compte (voir le bloc des absences at the top of this file), et
// [FixedAdminName] est ce que le Digest present. Le champ existe quand same pour que
// l'appelant n'ait pas a distinguer cette plateforme des autres — et il porte ce qui
// a been presented, pas ce qu'on souhaitait.
func (a *Admin) Account() string { return a.User }

// Accepted est le candidat qui a fonctionne, tel qu'il a been fourni.
//
// Rendu par le contrat parce que l'appelant en a besoin, et sur cette plateforme plus
// qu'ailleurs : changer le mot de passe EXIGE de connaitre le previous, qui est la
// key RC4 du digest expected (cf. [PasswordBlob]). Le reconstituer en relisant le
// transcript — ce que la couche API pratiquait — revenait a faire dependre une pose
// d'identifiant du label d'une phrase written pour un humain.
//
// Vide sur un Admin obtenu autrement que par [Driver.Authenticate] (par [OpenAdmin],
// donc par la discovery) : aucun candidat n'y a been presented.
func (a *Admin) Accepted() Candidate { return a.accepted }

// Transcript est la trace de tout ce que cet access a fait, dans l'ordre.
//
// Nomme ainsi par le contrat, et pas `Steps`, pour une raison prosaique qui vaut pour
// les trois connecteurs : ils exposent tous un CHAMP `Steps`, et un type Go ne peut
// pas porter un champ et une method du same nom. Ce nom-there permet d'adopter le
// contrat par simple ajout, sans renommer un champ que les tests du paquet lisent.
//
// Sur une plateforme dont la configuration est persistante d'office, sans filet, et
// qui n'a ni SSH ni serial port, ce n'est pas un confort de developpeur : c'est la
// seule way de comprendre un failure sans retourner sur site.
func (a *Admin) Transcript() []Step { return a.Steps }

// SetSecret change le mot de passe du compte qui vient d'entrer.
//
// C'EST CETTE PLATEFORME QUI JUSTIFIE LE PARAMETER `oldSecret` DU CONTRAT, et il faut
// le dire ici pour que personne ne le prenne pour une precaution decorative : sur
// SwOS, l'ancien secret n'est pas une confirmation, c'est la KEY RC4 du digest que
// the device attend (cf. [PasswordBlob], observed a la lettre dans le JavaScript de
// l'embedded interface). Sans lui, il n'y a rien a envoyer. Et quand il est faux,
// the device repond HTTP 405 — le seul message d'erreur exploitable de toute la
// plateforme.
//
// L'Instant On, lui, l'ignore : sa session ouverte fait foi. Le contrat transmet donc
// le parameter dans les deux cas rather que de laisser l'appelant deviner lequel des
// deux mondes il a en face — c'est exactement le genre de difference que ce contrat
// existe pour absorber.
//
// LA POSE PERSISTE, sans rien de plus a faire : `Commit: immediate`. Ce n'est pas un
// confort mais un risque a connaitre — la request SUIVANTE doit present  le nouveau
// secret (ce que [Admin.SetPassword] fait), et un reboot ne ramenera pas
// l'ancien.
func (a *Admin) SetSecret(ctx context.Context, oldSecret, newSecret string) error {
	return a.SetPassword(ctx, oldSecret, newSecret)
}

// --- Lecture brute : la reconnaissance d'un firmware inconnu ---

// rawUnit reconnait un nom de page de SwOS.
//
// Volontairement ETROIT — une page `*.b` eventuellement prefixee d'un `!` (les pages
// que l'interface ne rewrites jamais : /!dhost.b, /!igmp.b, /!pwd.b), ou la sauvegarde
// `.swb` — pour deux raisons de nature differente :
//
//   - ce nom part dans une URL. Une injection y detournerait la request vers un autre
//     endpoint de the device, et cette plateforme en a qui AGISSENT (/reset,
//     /reboot). Ils ne repondent qu'au POST, ce qui est un garde-fou du firmware et
//     non du notre : s'appuyer dessus serait faire dependre notre surete d'un detail
//     de version ;
//   - ce qui n'est pas une page ne rend pas 404 sur SwOS. Refuser en amont ce qui ne
//     ressemble pas a une page evite de payer un aller-retour pour recevoir 44 807
//     octets d'interface web.
var rawUnit = regexp.MustCompile(`^!?[a-z0-9_]{1,24}\.(b|swb)$`)

// ReadRaw rend le contenu brut d'une page, telle que the device la sert.
//
// LECTURE SEULE, et c'est un outil de RECONNAISSANCE, pas une fonction de produit : ces
// interfaces ne sont documentees nulle part, et on ne sait write une page qu'after
// avoir vu son jeu de champs reel.
//
// LE GARDE-FOU DE CETTE METHOD EST LE PLUS PRECIEUX DU PAQUET, et il est conserve
// tel quel (cf. [Admin.raw]) : sur SwOS, un chemin inconnu ne rend pas 404 — il rend
// l'INTERFACE WEB ENTIRE, en HTTP 200 et en `text/html` (44 807 octets sur le model
// de reference ; verified a nouveau en direct sur /lldp.b, qui sert /index.html).
// Sans ce control, une faute de frappe dans un nom de page se presenterait comme une
// response valide, et le message d'erreur accuserait la syntaxe de la page there where le
// vrai default est le chemin. C'est la difference entre « cette page n'existe pas sur
// ce firmware » et une heure passee a debuguer un analyseur qui va bien.
func (a *Admin) ReadRaw(ctx context.Context, unit string) (string, error) {
	var clean = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(unit), "/"))
	if !rawUnit.MatchString(clean) {
		return "", fmt.Errorf("« %s » n'est pas un nom de page SwOS : expected quelque "+
			"chose comme « sys.b », « vlan.b », « !dhost.b » ou « backup.swb ». Ce nom "+
			"part dans une URL, et cette plateforme a des chemins qui AGISSENT : il est "+
			"refused avant d'be envoye, pas nettoye", unit)
	}
	return a.raw(ctx, "/"+clean)
}

// RawKind nomme, pour un humain, ce qu'« une unite » veut dire sur cette plateforme.
//
// Sert a the UI, et evite le pire malentendu de la sonde de mise au point :
// demander un nom de section `wcd` a quelqu'un qui regarde un MikroTik, puis
// present  failure comme une panne de the device.
func (a *Admin) RawKind() string { return "page *.b" }

// --- SNMP ---

// ApplySNMP pose identity SNMP sur the device.
//
// WRITTEN AVANT D'AVOIR UN EMPLOI, et elle en a now un. La repartition des roles
// que le contrat describes n'a pas changed : la CAPABILITY decide s'il faut poser,
// l'INTERFACE execute. Ce qui a changed est la capability. `SNMPVersions` was VIDE,
// et ce vide ecartait the device en amont — `snmpprov.For` ne produit aucun plan
// supervise sans version declared — de sorte que cette method n'was jamais appelee.
//
// La seule chose qui manquait a cette plateforme was UN FAIT : savoir a quelle
// version son agent repond. /snmp.b prouvait qu'un agent existe, qu'il est actif et
// qu'il porte une community, sans jamais pouvoir dire s'il sert du v1, du v2c ou les
// deux. Le walk du 29 juillet 2026 sur 10.99.0.36 a repondu, `SNMPVersions` vaut
// `["v2c"]` (swos.go), et la liste s'est effectivement levee d'UNE LIGNE sans que rien
// n'ait been a write ici — c'est exactement ce que cet ordre visait. L'inverse (lever
// la liste puis decouvrir qu'il faut coder la pose) aurait fait annoncer une
// supervision qu'on ne savait pas mettre en service.
//
// Ce qui n'est pas affirme : qu'une pose ait been exercee sur le reference hardware.
// Elle ne l'a pas been. Le code est verified sur la real capture de /snmp.b, pas sur
// un aller-retour write.
//
// CE QUI EST REFUSED EST REFUSED, JAMAIS RAPPROCHE DU PLUS PROCHE. Il n'existe AUCUN
// SNMPv3 sur cette plateforme — pas d'utilisateur, pas d'algorithme
// d'authentification, pas de chiffrement, aucun champ, dans aucun onglet ni dans la
// sauvegarde de configuration complete. Poser une community v2c there where l'appelant
// voulait un utilisateur v3 mettrait un secret en clair sur le network que personne
// n'a choisi, et afficherait une conformity fausse.
func (a *Admin) ApplySNMP(ctx context.Context, want SNMPIdentity) (SNMPResult, error) {
	var out SNMPResult

	if want.User != "" {
		out.Skipped = append(out.Skipped, "utilisateur SNMPv3 « "+want.User+" » NON applied : "+
			"il n'y a aucun SNMPv3 sur cette plateforme — ni utilisateur, ni algorithme "+
			"d'authentification, ni chiffrement, dans aucun onglet de son interface ni dans "+
			"sa sauvegarde de configuration complete. Aucune version de meshcraft n'y "+
			"changera rien : c'est le firmware qui n'a pas la fonction.")
	}
	if len(want.Sources) > 0 {
		// Le contrat warns qu'un connecteur dont la plateforme n'a pas ce reglage
		// IGNORE ce champ. On l'ignore, mais on le DIT : un exploitant qui a borne les
		// sources dans le plan doit savoir que cet device ne l'a pas applique.
		out.Skipped = append(out.Skipped, "restriction des sources d'interrogation NON "+
			"applied : /snmp.b n'a pas de champ d'adresses. Ce qui borne access a l'agent sur "+
			"cette plateforme est le groupe d'administration de /sys.b (« alla », « allm », "+
			"« avln »), qui coupe access WEB en same temps que SNMP et que ce connecteur "+
			"does not write pas.")
	}
	if want.Community == "" {
		// Rien de posable : le dire par une erreur, pas par un success vide. Un apply qui
		// rend « tout va bien » sans avoir rien written laisse croire a une supervision en
		// place, et c'est le pire des deux mondes — rien ne repond, et rien ne dit
		// pourquoi.
		var why = "aucune community a poser : SwOS n'a QU'UNE community pour toute " +
			"configuration SNMP (/snmp.b « com ») et aucun v3 — sans elle, il n'y a rien " +
			"a write sur cet device"
		if len(out.Skipped) > 0 {
			why += ". " + strings.Join(out.Skipped, " ")
		}
		return out, errors.New(why)
	}

	// Idempotence : un parc already qualifie ne doit RIEN rewrite. Sur une plateforme
	// dont writing est persistante d'office et sans retour arriere, un POST qui ne
	// change rien est un risque pris pour rien — et il serait pris a chaque passage.
	var cfg, err = a.ReadSNMP(ctx)
	if err != nil {
		return out, err
	}
	if cfg.Enabled && cfg.Community == want.Community {
		a.step(true, "community SNMP already conforme",
			"l'agent est actif et porte already la community attendue : aucune write")
		out.Enabled = true
		return out, nil
	}

	if err = a.SetCommunity(ctx, want.Community); err != nil {
		return out, err
	}

	// RELECTURE OBLIGATOIRE, et c'est le mode failure de cette plateforme qui la rend
	// necessary : un refus de SwOS est MUET — 200, corps vide, rien de changed. Son
	// interface ne lit d'ailleurs rien d'autre que le code HTTP. La seule preuve qu'une
	// write a porte est donc de relire, et c'est ce que `CommunitySet` pretend
	// (« une write a eu lieu ET a been verified »).
	var after, reErr = a.ReadSNMP(ctx)
	if reErr != nil {
		return out, fmt.Errorf("community envoyee mais relecture impossible, writing "+
			"n'est donc pas verified : %w", reErr)
	}
	if after.Community != want.Community || !after.Enabled {
		a.step(false, "write SNMP sans effet",
			"the device a repondu HTTP 200 mais la relecture ne montre pas la "+
				"configuration applied : sur SwOS un refus est MUET, et c'est ce cas-there")
		return out, errors.New("the device a accepted la request (HTTP 200) mais la " +
			"relecture de /snmp.b ne montre pas la community applied : writing n'a pas " +
			"porte, et cette plateforme ne dit pas pourquoi")
	}
	a.step(true, "community SNMP verified par relecture",
		"l'agent est actif et porte la community attendue")
	out.Enabled, out.CommunitySet = true, true
	return out, nil
}

// transcriptLines aplatit un transcript pour le glisser dans un message d'erreur.
//
// Existe pour le seul cas where le transcript ne peut PAS be rendu par
// [Admin.Transcript] : un [Driver.Authenticate] qui fails ne rend aucun Admin, donc
// aucun objet sur lequel l'appelant pourrait lire les etapes. Les perdre reviendrait
// a present  « identifiants refused » sans dire ce qui a been tente.
func transcriptLines(steps []Step) []string {
	var out = make([]string, 0, len(steps))
	var s Step
	for _, s = range steps {
		var line = s.What
		if s.Detail != "" {
			line += " (" + s.Detail + ")"
		}
		out = append(out, line)
	}
	return out
}
