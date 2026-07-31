package driver

import (
	"context"
	"errors"
	"fmt"
)

// Le contrat d'ACCÈS aux équipements : ce que la couche API appelle pour lire,
// écrire et poser une identité, sans jamais savoir à quelle plateforme elle parle.
//
// # Pourquoi ce fichier existe
//
// driver.go décrit ce qu'une plateforme SAIT FAIRE (capabilities, fingerprint,
// procédure de reset). C'est du SAVOIR, et ça suffisait tant qu'un seul connecteur
// existait : la couche API appelait `instanton.OpenAdmin` en dur, et le savoir ne
// servait qu'à décider quoi demander. Le jour où un deuxième connecteur est arrivé,
// ce raccourci est devenu un défaut d'exploitation constaté, pas supposé : deux
// switchs MikroTik adoptés dans un site ont déclenché une pose SNMP qui a parlé le
// protocole Instant On à un MikroTik — `https://10.99.0.54/`, connexion refusée
// (cet équipement n'écoute pas en 443), `snmp.apply_failed` en sévérité 3. Rien n'a
// été écrit parce que le TCP a échoué. Le geste, lui, était bien parti.
//
// Ce fichier est ce qui rend cette faute impossible : il n'y a plus de chemin par
// lequel la couche API puisse atteindre un équipement sans passer par le connecteur
// que la découverte a désigné.
//
// # Le contrat part du BESOIN DE L'APPELANT, pas de l'implémentation historique
//
// Ce qui est décrit ici est ce que la couche API consomme réellement : ouvrir un
// accès d'administration avec une liste ordonnée de secrets, relire l'état, poser
// l'appartenance VLAN, poser un secret d'administration, poser une identité SNMP,
// poser une destination de traps, et rendre un transcript. Rien de plus.
//
// Il ne recopie DÉLIBÉRÉMENT pas la surface d'`instanton` : celle-ci décrit une
// interface web rejouée comme un navigateur (sections `wcd`, `SaveConfig`,
// `RawSection`), et un contrat calqué dessus condamnerait RouterOS à mimer une UI
// qu'il n'a pas. La règle appliquée partout ci-dessous : on nomme le RÉSULTAT
// attendu, jamais le geste par lequel une plateforme donnée l'obtient.
//
// # Les trois plateformes n'ont pas la même NATURE, et le contrat le porte
//
// C'est le piège principal, et il est déjà documenté en tête de
// internal/driver/mikrotik/swos/admin.go : « Il n'y a PAS de session, et c'est le
// premier fait à intégrer. » Les trois s'authentifient de trois façons :
//
//   - Instant On : formulaire (en réalité un GET chiffré en RSA), état conservé dans
//     un cookie et dans un préfixe d'URL `/csNNNNNNNN/`. Il y a une vraie session, et
//     l'équipement n'en accepte que CINQ ;
//   - SwOS : `Authorization: Digest` re-présenté à CHAQUE requête. Aucun état, rien
//     à ouvrir, rien à fermer ;
//   - RouterOS : `Authorization: Basic` re-présenté à chaque requête de l'API REST.
//     Idem.
//
// Un contrat en deux temps — `Open()` puis `Login()` — forcerait deux connecteurs
// sur trois à faire semblant : un `Login` qui ne fait rien, ou qui « vérifie » en
// tapant une page au hasard sans que l'appelant sache que c'est ça qui s'est passé.
// D'où [Connector.Authenticate], qui est UN SEUL appel : « rends-moi un accès
// d'administration déjà éprouvé, ou dis-moi pourquoi tu ne peux pas ». Ce que
// « éprouvé » veut dire est du ressort du connecteur — une session ouverte ici, une
// lecture authentifiée là — et l'appelant n'a pas à choisir pour lui.
//
// Symétriquement, il n'y a PAS de `Close`. Trois raisons, dans l'ordre de force :
// SwOS et RouterOS n'ont rien à fermer ; le relevé de l'Instant On dit qu'« aucun
// point de déconnexion n'a été identifié dans les scripts servis » ; et une méthode
// `Close` laisserait croire qu'appeler `defer close()` libère une des cinq sessions
// de cette gamme, ce qui est faux. Une promesse fausse sur une ressource rare est
// pire que pas de promesse.
//
// # Où passe la frontière entre OBLIGATOIRE et FACULTATIF
//
// Deux mécanismes coexistent dans ce paquet pour dire « je ne sais pas faire », et
// ils ne servent pas au même cas. La règle est mécanique :
//
//   - une capacité que toute plateforme de commutation POSSÈDE, mais qu'un
//     connecteur n'a pas encore codée → méthode du contrat OBLIGATOIRE, qui rend
//     [ErrNotImplemented]. Tout switch a des ports, des VLAN et un mot de passe
//     d'administration : l'absence est un manque de NOTRE côté, elle se comble par
//     une version, et l'appelant n'a rien à décider d'autre que d'afficher l'échec ;
//   - une capacité qu'une plateforme peut structurellement NE PAS AVOIR → interface
//     FACULTATIVE séparée, testée par assertion de type. SwOS n'a ni compte nommé,
//     ni destinataire de traps, ni syslog, ni sauvegarde en deux temps : ce ne sont
//     pas des retards, ce sont des faits de la plateforme.
//
// Pourquoi cette frontière-là et pas une autre : une assertion de type se fait AVANT
// que le geste ne commence. L'appelant peut donc refuser proprement, expliquer, et
// ne pas proposer un bouton qui ne mènera nulle part. `ErrNotImplemented`, lui, ne
// se découvre qu'en essayant — acceptable pour un manque temporaire, inacceptable
// pour une absence définitive qu'un écran doit annoncer. C'est l'application directe
// de la règle du projet : « une capability inconnue est supposée ABSENTE, et une
// absence se DÉCLARE au lieu de se découvrir sur le terrain ».
//
// # Ce que ce contrat ne connaît pas
//
// Ni SSH, ni HTTP, ni tunnel : un connecteur reçoit un [Dialer] dont le transport
// est déjà établi, directement ou à travers un [[Proxy]]. C'est la règle « un seul
// chemin de code » écrite en tête de driver.go, et elle n'est pas négociable — un
// connecteur qui ouvrirait sa propre connexion contournerait le proxy, donc la
// frontière de confidentialité, donc la couverture de sites.

// Connector est un [Driver] qui sait ATTEINDRE l'équipement, et non seulement le
// décrire.
//
// La séparation des deux est ce qui ferme la faute rapportée en tête de fichier. Un
// `Driver` peut exister sans savoir écrire : c'est le cas d'une gamme dont on ne
// connaît que le fingerprint et les capabilities, ce qui suffit à la RANGER dans
// l'inventaire. Le jour où la couche API veut ÉCRIRE, l'assertion de type sur
// `Connector` est le seul chemin — et elle échoue proprement au lieu de tomber par
// défaut sur le connecteur historique.
//
// Le registre continue de rendre des `Driver` (cf. [Get]) : c'est [ConnectorFor] qui
// fait l'assertion en un seul endroit, avec la phrase qui va avec.
type Connector interface {
	Driver

	// Authenticate rend un accès d'administration ÉPROUVÉ sur cet équipement.
	//
	// UN SEUL APPEL, et c'est le point de conception le plus important de ce fichier
	// (cf. l'en-tête, § sur la nature des plateformes). Le connecteur essaie les
	// candidats DANS L'ORDRE, s'arrête au premier qui entre, et rend l'accès. Ce que
	// « entrer » signifie lui appartient : ouvrir une session pour l'Instant On,
	// réussir une lecture authentifiée pour SwOS et RouterOS.
	//
	// L'ORDRE VIENT DE L'APPELANT et ne se réordonne pas. Il est calculé par la base
	// (cf. store.SecretsForCredential et api.buildCandidates) : génération constatée
	// d'abord, générations en retrait ensuite, compte d'usine en DERNIER. Essayer le
	// secret le plus récent d'abord échouerait sur tout équipement resté en arrière,
	// et le compte d'usine en tête échouerait sur tout le parc déjà pris en main.
	//
	// LE NOMBRE D'ESSAIS EST UN BUDGET, pas une commodité : l'appelant le plafonne
	// déjà à deux par compte (api.triesPerAccount), parce que ces gammes
	// VERROUILLENT les comptes. Un connecteur ne doit donc JAMAIS rejouer un candidat
	// de sa propre initiative, ni en fabriquer un « pour voir ». Sur un équipement
	// sans SSH ni port série, épuiser les essais revient à fermer la seule porte.
	//
	// Un échec est une erreur ORDINAIRE, pas un état à interpréter : « aucun
	// identifiant accepté » et « l'équipement ne répond pas » se distinguent par le
	// message et par le transcript, que l'appelant affiche tel quel.
	Authenticate(ctx context.Context, tgt Target, cands []Candidate) (Admin, error)

	// WritesExtra dit si ce connecteur pose l'intention nommée, en dehors de
	// l'appartenance VLAN (cf. recon.ExtraIntents : `poe`, `stp`, `dhcp_snooping`,
	// `port_security`).
	//
	// Sur le Connector et non sur l'Admin : la réponse ne dépend ni d'une session ni
	// d'un modèle, et l'appelant en a besoin AVANT de joindre l'équipement — ce que
	// la plateforme ne sait pas écrire doit être annoncé même quand la pose échoue
	// (cf. api.unsupportedNotes, qui le relève avant le premier paquet).
	//
	// Faux pour tout ce qui n'est pas explicitement posé. C'est la même règle que
	// partout : ce qui n'est pas déclaré est absent, et une capacité qui MENT est
	// pire que pas de capacité du tout, parce qu'elle donne à l'exploitant une
	// configuration qu'il croit posée.
	WritesExtra(name string) bool
}

// Target est l'équipement à joindre, et par où.
//
// Un agrégat plutôt que quatre paramètres répétés dans dix signatures : le jour où
// il faut y ajouter un port non standard ou un schéma, les connecteurs changent une
// fois et les appelants zéro fois.
type Target struct {
	// Host est l'adresse d'administration, sans schéma ni port. C'est le connecteur
	// qui sait comment sa plateforme s'atteint : `https://` sur 443 pour l'Instant
	// On, `http://` sur 80 pour SwOS (qui n'écoute QUE là), `http://` pour l'API REST
	// de RouterOS (dont le 443 est fermé faute de certificat d'usine). L'appelant ne
	// doit surtout pas en décider — c'est très exactement la faute rapportée en tête
	// de ce fichier.
	Host string
	// Dialer est le transport DÉJÀ ÉTABLI : direct, ou à travers le tunnel d'un
	// proxy. Nul vaut « depuis cette machine », ce que seul le proxy « Local »
	// justifie.
	Dialer Dialer
	// Model est le modèle tel que l'inventaire le connaît. Sert au connecteur à
	// choisir entre deux comportements d'une même gamme ; il ne le DEVINE jamais
	// quand le champ est vide.
	Model string
	// PortPrefix est le préfixe de nommage des ports du modèle standard pour cet
	// équipement (« GE »), tel que ses groupes de façade le portent.
	//
	// À NE PAS PRENDRE POUR UNE VÉRITÉ UNIVERSELLE, et c'est le relevé de RouterOS
	// qui l'établit : sur cette plateforme les noms meshcraft SONT les noms de
	// l'équipement (`ether3`, `sfp-sfpplus1`), donc la façade porte PLUSIEURS
	// préfixes et aucun ne résume l'équipement. Un connecteur qui nomme lui-même ses
	// ports IGNORE ce champ, et c'est le comportement correct — le lire en le prenant
	// pour le préfixe de tous les ports écrirait sur le mauvais port, ce qui coupe un
	// client.
	//
	// Les connecteurs qui NUMÉROTENT leurs ports (Instant On, SwOS) en ont besoin :
	// c'est ce qui traduit « GE12 » en l'index 12 de l'équipement. Vide, ils
	// appliquent leur propre défaut plutôt que d'écrire à l'aveugle.
	PortPrefix string
}

// Candidate est un secret à essayer, avec la raison de sa position dans l'ordre.
//
// `Why` n'est pas un commentaire : il part au transcript, et c'est lui qui permet à
// l'exploitant de comprendre APRÈS COUP quel secret a ouvert l'équipement (« la
// génération 2, constatée » contre « le compte d'usine »). Il est aussi ce qui
// permet à l'appelant de retrouver le secret accepté sans le redemander à la base.
type Candidate struct {
	Username string
	Secret   string
	Why      string
}

// Step est une étape de transcript, telle qu'elle sera montrée à l'opérateur.
//
// COMMUN aux trois connecteurs, et c'était déjà vrai avant ce contrat : les trois
// avaient recopié la même structure dans leur propre paquet, en écrivant chacun dans
// son commentaire « même forme que celle du connecteur Instant On, pour que l'API
// n'ait qu'une façon de présenter une pose ». Trois copies d'un type identique ne
// tiennent que jusqu'au jour où l'une gagne un champ.
//
// La migration côté connecteur ne coûte rien : `type Step = driver.Step` (un ALIAS,
// pas une définition) fait de leur champ `Steps []Step` existant un `[]driver.Step`
// sans toucher une ligne de leur code. C'est ce que doivent faire les trois.
//
// Les tags JSON sont ceux d'origine, et ce n'est pas un détail : ces étapes sont
// sérialisées dans la réponse HTTP de chaque pose et lues par le frontend. Les
// changer casserait l'affichage des transcripts sans qu'aucun test Go ne le voie.
type Step struct {
	What   string `json:"what"`
	Detail string `json:"detail,omitempty"`
	OK     bool   `json:"ok"`
}

// Admin est un accès d'administration ÉPROUVÉ à UN équipement.
//
// « Éprouvé » et non « ouvert » : sur deux des trois plateformes il n'y a rien
// d'ouvert, seulement des identifiants dont on a constaté qu'ils passent. Le nom
// évite la promesse qu'un objet à fermer existe (cf. l'en-tête, § sur l'absence de
// `Close`).
//
// Les méthodes de CETTE interface sont celles que toute plateforme de commutation
// possède : lire son état, poser l'appartenance VLAN de ses ports, changer le secret
// du compte qui vient d'entrer. Un connecteur qui ne les a pas encore codées rend
// [ErrNotImplemented] ; il n'a pas le droit de rendre un succès vide, qui laisserait
// croire à une configuration en place.
//
// Tout ce qu'une plateforme peut structurellement ne PAS avoir vit dans les
// interfaces facultatives déclarées plus bas, et se teste par assertion de type.
type Admin interface {
	// Connector rend l'identité du connecteur qui a produit cet accès.
	//
	// Sert aux MESSAGES, et c'est suffisant pour justifier son existence : « cette
	// plateforme ne sait pas déclarer de destinataire de traps » n'aide personne si
	// l'écran ne dit pas de quelle plateforme il parle. Évite aussi à l'appelant de
	// promener le nom du connecteur en parallèle de l'accès, deux variables qui
	// finiraient par désigner deux équipements différents.
	Connector() Info

	// Account est le compte qui a RÉELLEMENT ouvert l'accès.
	//
	// Pas celui qu'on souhaitait : celui qui a répondu. La différence décide de la
	// suite du provisionnement — même compte, on change son secret ; compte
	// différent, c'est un RENOMMAGE, et il faut créer le nouveau avant de retirer
	// l'ancien (cf. api.handleApplyDeviceCredential).
	Account() string

	// Accepted est le candidat qui a fonctionné, tel qu'il a été fourni.
	//
	// Rendu explicitement parce que l'appelant en a besoin : le secret ACTUEL de
	// l'équipement est celui qui vient d'entrer, et certaines plateformes le
	// réclament pour en poser un nouveau (SwOS répond HTTP 405 « Bad old password »).
	//
	// Le faire rendre par le contrat remplace une reconstitution par le transcript
	// que la couche API pratiquait jusqu'ici : elle cherchait une étape dont le
	// libellé commence par « connecté en » puis recroisait `Why` avec la liste des
	// candidats. Un libellé de transcript est du TEXTE POUR UN HUMAIN — le reformuler
	// est légitime et sans conséquence apparente, et cassait silencieusement la pose
	// d'identifiant. Ce genre de couplage n'a pas à survivre au contrat.
	//
	// Vide si aucun candidat n'a servi (cas d'une plateforme qui n'authentifie rien,
	// qui n'existe pas aujourd'hui).
	Accepted() Candidate

	// Transcript est la trace de tout ce que cet accès a fait, dans l'ordre.
	//
	// Pas un confort de développeur : sur un équipement qu'on peut rendre injoignable
	// en se trompant, et qui n'a ni SSH ni port série, c'est la seule façon de
	// comprendre un échec sans retourner sur site. Il est rendu à l'opérateur dans la
	// réponse HTTP ET rattaché au pipeline, ce qui le rend consultable après coup —
	// c'est-à-dire au moment où on en a besoin.
	//
	// Nommé `Transcript` et non `Steps` pour une raison prosaïque mais qui compte
	// pour les trois connecteurs : ils exposent tous un CHAMP `Steps`, et un type Go
	// ne peut pas porter un champ et une méthode du même nom. Ce nom-ci leur permet
	// d'adopter le contrat par ajout, sans renommer un champ que leurs propres tests
	// lisent.
	Transcript() []Step

	// ReadState relit l'équipement. LECTURE SEULE : rejouable aussi souvent qu'on
	// veut, y compris sur un parc en production.
	//
	// C'est l'étape « Read » du pipeline de réconciliation. Sans elle, meshcraft ne
	// peut pas savoir si ce qu'il croit est ce qui est, et « dérive de
	// configuration » reste un mot. C'est aussi ce qui autorise à DATER une pose :
	// rien n'est tenu pour appliqué sur la foi d'un code de retour.
	ReadState(ctx context.Context) (State, error)

	// ApplyPorts pose l'appartenance VLAN des ports.
	//
	// L'opération la plus dangereuse du produit sur deux des trois plateformes :
	// l'Instant On et SwOS n'ont ni SSH, ni Telnet, ni port série, et une écriture
	// qui retire le port d'uplink de son VLAN rend l'équipement muet — le seul
	// recours est un trombone, sur site. RouterOS a une console série et MAC-winbox,
	// ce qui rend l'erreur récupérable sans la rendre acceptable.
	//
	// LES GARDE-FOUS SONT DANS LE CONNECTEUR, pas ici et pas dans l'appelant. C'est
	// la règle « ne jamais se couper la main » placée là où elle peut être garantie —
	// celui qui écrit. Le contrat n'impose donc pas COMMENT protéger : il impose
	// seulement de dire ce qui a été épargné, par [ApplyResult.Skipped]. La règle
	// diffère réellement d'une plateforme à l'autre, et c'est instructif : sur
	// l'Instant On on protège le VLAN natif d'un port actif, sur SwOS c'est
	// l'ÉTIQUETAGE du VLAN de management sur ce port qui ne doit pas changer — la
	// règle naïve n'y protégeait rien, la session de l'équipement de référence
	// arrivant taguée sur une cage au VLAN natif 1.
	//
	// Une pose ne relit JAMAIS un état en cache : elle relit l'équipement d'abord.
	//
	// LA POSE PERSISTE, quand la plateforme distingue les deux. C'est tranché ici et
	// non laissé au connecteur, parce que deux connecteurs qui répondraient
	// différemment produiraient deux comportements que rien à l'écran ne distingue :
	// une configuration de ports qu'un redémarrage annule est PIRE qu'une pose
	// refusée, puisqu'elle a été constatée comme réussie. L'appelant n'a donc pas à
	// appeler [Persister] derrière `ApplyPorts` — et s'il le faisait, ce serait un
	// aller-retour de plus sur une interface qui n'accepte que cinq sessions.
	//
	// L'échec de la persistance n'annule PAS la pose : le changement est en place, et
	// le prétendre absent serait faux. Il est consigné au transcript.
	ApplyPorts(ctx context.Context, plan PortPlan) (ApplyResult, error)

	// SetSecret change le mot de passe DU COMPTE QUI VIENT D'ENTRER.
	//
	// Le compte n'est pas un paramètre, et c'est délibéré : c'est [Admin.Account], et
	// laisser l'appelant en nommer un autre ouvrirait la porte à « changer le mot de
	// passe d'un compte auquel on n'a pas prouvé qu'on accède ». Créer ou modifier un
	// AUTRE compte est un geste distinct, et il vit dans [AccountManager] parce que
	// toutes les plateformes ne l'ont pas.
	//
	// `oldSecret` est transmis même quand la plateforme ne le réclame pas :
	// l'Instant On l'ignore (la session ouverte fait foi), SwOS en fait la CLÉ RC4 du
	// condensé qu'il attend et répond HTTP 405 s'il est faux. L'appelant ne doit pas
	// avoir à distinguer les deux cas — c'est exactement ce que le contrat existe
	// pour absorber.
	//
	// LA POSE PERSISTE, comme [Admin.ApplyPorts] et pour une raison plus forte
	// encore : un mot de passe d'administration qui ne survit pas au redémarrage
	// ramène l'équipement à son compte d'usine sans mot de passe, ce qui est
	// exactement le piège que le provisionnement existe pour fermer. L'appelant n'a
	// donc pas à enchaîner sur [Persister].
	//
	// Ce qui reste à la charge de l'appelant, et qu'aucun connecteur ne peut faire à
	// sa place : n'enregistrer le nouveau secret en base qu'après avoir CONSTATÉ
	// qu'il ouvre l'équipement — c'est-à-dire après un second [Connector.Authenticate]
	// portant ce seul candidat. Un formulaire accepté sans effet est un mode d'échec
	// observé sur l'Instant On, et une base convaincue d'un succès qui n'a pas eu lieu
	// coûte un déplacement.
	SetSecret(ctx context.Context, oldSecret, newSecret string) error
}

// --- Les capacités FACULTATIVES ---
//
// Chacune est une interface séparée, testée par assertion de type. L'appelant peut
// donc refuser AVANT de commencer et dire quoi, ce qui est la seule façon de ne pas
// proposer un bouton qui ne mènera nulle part (cf. l'en-tête, § sur la frontière).
//
// La convention pour l'appelant : une assertion qui échoue se traduit par
// [NotSupported], dont le message nomme le connecteur, le geste et la raison. Une
// seule phrase pour toutes les capacités absentes vaut mieux que huit variantes
// approximatives.

// AccountManager : la plateforme a des COMPTES NOMMÉS.
//
// FACULTATIF parce que SwOS n'en a pas — pas « pas encore », pas du tout. Le relevé
// est sans ambiguïté : le formulaire de changement de mot de passe ne porte que trois
// champs (ancien, nouveau, confirmation), la page qui le reçoit ne contient qu'une
// clé (`{pwd:”}`), et la sauvegarde de configuration ENTIÈRE de l'équipement ne
// mentionne aucun nom d'utilisateur. Il n'y a rien à nommer.
//
// Conséquence que l'appelant doit tenir : sur une telle plateforme, le nom de compte
// unifié du parc (`mcadmin`) n'est pas posable, et c'est
// [AdminAccount.EffectiveUsername] qui le résout en `admin` AVANT toute tentative.
// L'absence de cette interface est donc le second verrou, pas le premier — si
// l'appelant se retrouve à vouloir créer un compte sur SwOS, c'est que la capability
// a été ignorée en amont.
//
// CES GESTES NE PERSISTENT PAS, à la différence de [Admin.SetSecret] — et c'est la
// seule asymétrie du contrat, donc elle se justifie. Un RENOMMAGE met deux comptes en
// vol : on crée le nouveau, on prouve qu'il ouvre l'équipement, et on ne retire
// l'ancien qu'ensuite. Qui persiste quoi et QUAND est alors une décision de
// séquencement que seul l'appelant peut prendre — entre les deux, une coupure de
// courant ne doit pas laisser l'équipement avec un seul compte, et pas celui qui a
// survécu. Un connecteur qui persisterait de son propre chef à chaque écriture
// retirerait ce choix, et le retirerait en silence.
//
// L'appelant enchaîne donc lui-même sur [Persister] aux endroits qu'il a choisis.
type AccountManager interface {
	// Accounts liste les comptes d'administration présents.
	//
	// Une LECTURE, et c'est elle qui rend les écritures de compte sûres : les
	// plateformes distinguent « créer » de « modifier » et refusent l'une à la place
	// de l'autre. C'est aussi ce qui rend l'opération rejouable — une pose
	// interrompue puis reprise ne bute pas sur un compte à moitié créé.
	Accounts(ctx context.Context) ([]string, error)

	// EnsureAccount garantit qu'un compte nommé existe et porte ce secret.
	//
	// Idempotent par construction : le connecteur lit l'existant et choisit lui-même
	// entre créer et modifier. L'appelant n'a pas à savoir lequel des deux verbes sa
	// plateforme attend.
	EnsureAccount(ctx context.Context, user, secret string) error

	// DeleteAccount retire un compte d'administration.
	//
	// DANGEREUX par nature. L'appelant DOIT avoir prouvé — en obtenant un
	// [Admin] réel sous le nouveau compte, pas en lisant un code de retour — qu'une
	// autre porte fonctionne avant d'appeler ceci. Sur une gamme sans SSH ni port
	// série, supprimer d'abord et vérifier ensuite se paie d'un déplacement.
	//
	// Le connecteur refuse de supprimer le compte de l'accès en cours : c'est le seul
	// garde-fou qu'il puisse porter tout seul.
	DeleteAccount(ctx context.Context, user string) error
}

// Persister : la plateforme distingue APPLIQUER de PERSISTER.
//
// FACULTATIF, et le champ qui décide est déjà dans le contrat :
// [Capabilities.Commit]. L'appelant ne doit appeler `Persist` que sur
// [CommitApplyThenSave] — c'est le seul cas où l'omettre perd la configuration au
// prochain redémarrage, et le seul où l'appeler trop tôt grave une erreur.
//
// Les trois plateformes livrées montrent les deux mondes, et la différence change la
// stratégie d'écriture entière :
//
//   - Instant On : `apply_then_save`. Un rollback GRATUIT, à condition de s'en
//     servir dans l'ordre — appliquer, vérifier la joignabilité, PUIS sauver. C'est
//     ce qui rend la bascule du VLAN de management testable sur du matériel en
//     service : tant que rien n'est en flash, une coupure d'alimentation annule ;
//   - SwOS et RouterOS : `immediate`. Rien à sauver, et donc pas de filet non plus.
//     Sur SwOS, le SEUL retour arrière est la copie de configuration prise AVANT
//     l'écriture (cf. [ApplyResult.Backup]).
//
// Ne pas implémenter cette interface est donc une INFORMATION, pas un manque.
type Persister interface {
	// Persist recopie la configuration courante dans la configuration de démarrage.
	//
	// Son échec n'annule pas ce qui vient d'être écrit — le changement est bel et
	// bien en place, et le prétendre absent serait faux. Il est consigné au
	// transcript, qui est le canal prévu pour ce que l'opérateur doit savoir.
	Persist(ctx context.Context) error
}

// SNMPConfigurator : la plateforme a un agent SNMP configurable.
//
// FACULTATIF parce qu'une plateforme peut n'en avoir aucun, et parce que « avoir un
// agent » ne veut pas dire « savoir poser une identité dessus ». Ce sont deux
// questions distinctes, et [Capabilities.SNMPVersions] répond à la première : un
// [Capabilities.SNMPVersions] vide écarte l'équipement en AMONT — `snmpprov.For` ne
// produit aucun plan supervisé sans version déclarée — donc l'appelant n'a rien à
// poser, que cette interface soit implémentée ou non.
//
// La présence de cette interface n'est donc pas ce qui décide de poser : c'est
// [Capabilities.SNMPVersions] qui décide, et l'interface qui exécute. Les deux doivent
// concorder, et un connecteur qui déclarerait des versions sans implémenter cette
// interface annoncerait une supervision qu'il ne sait pas mettre en service.
//
// SwOS est le cas qui a éprouvé cette séparation. Sa liste de versions est restée vide
// longtemps — un agent existait bien (`/snmp.b`, quatre champs), mais aucune requête
// n'avait jamais obtenu de réponse, donc personne ne savait à quelle version il
// répondait — tandis que `ApplySNMP` était déjà écrite et éprouvée. Quand le walk du
// 29 juillet 2026 a répondu, la supervision de cette gamme s'est ouverte en levant UNE
// ligne de capability, sans rien à coder. Dans l'autre ordre, il aurait fallu annoncer
// la supervision puis découvrir qu'on ne savait pas la mettre en service.
type SNMPConfigurator interface {
	// ApplySNMP pose l'identité SNMP sur l'équipement.
	//
	// Le résultat dit ce qui a été fait ET ce qui ne l'a pas été
	// ([SNMPResult.Skipped]) : un apply partiel présenté comme complet laisserait
	// croire à une supervision qui n'existe pas.
	//
	// Une identité que la plateforme ne peut pas porter est REFUSÉE, jamais
	// rapprochée du « plus proche ». Poser MD5 là où l'appelant voulait SHA-256
	// donnerait une supervision qui marche et une conformité fausse ; poser une
	// communauté v2c là où on demandait un utilisateur v3 donnerait un secret en
	// clair sur le réseau que personne n'a choisi.
	ApplySNMP(ctx context.Context, want SNMPIdentity) (SNMPResult, error)
}

// TrapPusher : la plateforme sait POUSSER des traps vers un collecteur.
//
// FACULTATIF, et SwOS en est la démonstration : `/snmp.b` n'a AUCUN champ de
// destinataire, dans aucun onglet, ni dans la sauvegarde de configuration complète.
// Ce n'est pas une page masquée — l'interface web entière d'un SwOS tient en un
// fichier, et elle ne connaît pas les traps. Un tel équipement se supervise par
// interrogation : l'information est là, c'est la RÉACTIVITÉ qui manque, et le dire
// vaut mieux que d'écrire une ligne dans un équipement qui ne s'en servira pas.
//
// La présence de cette interface doit concorder avec
// [MonitoringCapabilities.Traps] : c'est la même capability qui décide que le proxy
// ouvre 162/udp. Déclarer l'un sans l'autre écrirait une destination vers un port
// que personne n'écoute, ou ouvrirait un port que personne n'alimente.
type TrapPusher interface {
	// ApplyTrapReceiver déclare le collecteur comme destinataire des traps.
	//
	// Le connecteur REFUSE si l'agent SNMP est éteint : un destinataire posé sur un
	// agent muet donne une configuration exacte et un silence total. C'est
	// [SNMPConfigurator.ApplySNMP] qui allume l'agent, et l'ordre entre les deux
	// n'est pas négociable.
	//
	// Aucun destinataire TIERS n'est retiré, même pour faire de la place : couper la
	// supervision de quelqu'un d'autre pour installer la sienne serait le pire des
	// services. Une table pleine se SIGNALE.
	//
	// Et le résultat n'est qu'une PRÉTENTION : la ligne est dans l'équipement, ce qui
	// ne prouve pas que ses traps arrivent. La preuve est le premier trap reçu, et
	// elle appartient à l'appelant (cf. api.confirmTrapPath) — un pare-feu, une route
	// asymétrique ou une ACL sur le VLAN de management suffisent à faire émettre dans
	// le vide.
	ApplyTrapReceiver(ctx context.Context, want TrapDestination) (TrapResult, error)
}

// MgmtMover : la plateforme sait déplacer son ADRESSAGE DE MANAGEMENT sous la
// session qui le commande.
//
// FACULTATIF, et le geste le plus dangereux du produit : il déplace l'accès pendant
// qu'on s'en sert. D'où trois méthodes et non une — la manœuvre se fait en deux
// temps VÉRIFIABLES, jamais en un saut.
//
// Ce découpage vient du relevé de l'Instant On, qui accepte PLUSIEURS interfaces
// IPv4 : on ajoute, on constate qu'un bail est arrivé, puis seulement on bascule.
// Tant que l'ancienne interface est là, l'accès actuel reste ouvert. Une plateforme
// qui ne saurait pas porter deux adresses ne peut pas implémenter ce contrat en
// l'état, et c'est la bonne réponse : elle doit alors NE PAS l'implémenter, plutôt
// que de sauter sans regarder.
//
// LA BASCULE NE PERSISTE PAS, et c'est l'exception qui justifie que [Persister]
// existe séparément. Tout le reste du contrat persiste ce qu'il pose
// ([Admin.ApplyPorts], [Admin.SetSecret]) ; ici, NON — tant que rien n'est écrit en
// flash, une coupure d'alimentation ramène l'équipement à son adressage d'avant, et
// c'est ce filet qui autorise à tenter la manœuvre sur du matériel en service. Un
// connecteur qui persisterait au passage retirerait le seul recours de l'opérateur.
//
// La persistance est donc un TROISIÈME geste, à la charge de l'appelant, et il doit
// exiger d'avoir CONSTATÉ que l'équipement déclare lui-même administrer par le VLAN
// visé avant de l'appeler : sauver une bascule non vérifiée graverait une erreur.
type MgmtMover interface {
	// PrepareMgmt ajoute un accès d'administration sur le VLAN visé, SANS rien
	// retirer. L'équipement garde son adresse actuelle et reste joignable pendant
	// toute l'opération.
	PrepareMgmt(ctx context.Context, vlanID int) error

	// MgmtAddressed dit si l'accès préparé a OBTENU son adresse, et laquelle.
	//
	// C'est le feu vert de la seconde moitié : tant qu'il est rouge, basculer
	// couperait l'accès sans contrepartie. Une adresse absente n'est pas forcément un
	// défaut de configuration — sur l'Instant On, une interface DHCP sans bail se lit
	// à un état distinct, et confondre les deux ferait « corriger » une écriture déjà
	// juste.
	MgmtAddressed(ctx context.Context, vlanID int) (bool, string, error)

	// CommitMgmt déplace l'administration sur le VLAN visé.
	//
	// À n'appeler QU'APRÈS un [MgmtMover.MgmtAddressed] favorable. C'est cet appel
	// qui coupe la session en cours s'il est passé trop tôt.
	//
	// Le connecteur y fait tout ce que sa plateforme impose dans l'ordre qu'elle
	// impose — sur l'Instant On, retirer l'interface de répétition parce que
	// l'équipement refuse un VLAN de management portant déjà une configuration IP. Ce
	// genre de contrainte d'ORDRE n'a rien à faire chez l'appelant : elle est propre
	// à la plateforme, et la répartir entre deux paquets garantit qu'un jour l'un
	// oublie sa moitié.
	CommitMgmt(ctx context.Context, vlanID int) error
}

// MgmtJumper: the platform cannot carry TWO management paths at once, and meshcraft
// still offers an explicit LEAP for operators who accept the risk.
//
// FACULTATIVE, SwOS-shaped. Distinct from [MgmtMover]: there is no prepare/verify
// window where both addresses coexist. The connector MUST take a backup before the
// write and return it, so the operator has something to restore if relocate fails.
//
// A connector that can implement [MgmtMover] MUST NOT implement this instead —
// dual-path is always preferable. Flip [MgmtCapabilities.JumpVLAN] only after the
// jump and backup restore have been proven on hardware.
type MgmtJumper interface {
	// JumpMgmt applies the new management VLAN and/or addressing in one shot.
	// Returns the pre-write backup (may be empty only if the platform has none).
	JumpMgmt(ctx context.Context, vlanID int, want MgmtAddrIntent) (backup []byte, err error)
}

// AsMgmtJumper returns the admin as a management jumper when it is one.
func AsMgmtJumper(a Admin) (MgmtJumper, bool) {
	var j, ok = a.(MgmtJumper)
	return j, ok
}

// MgmtStaticOwnerProber learns the platform's static-interface owner value by a
// temporary write on a NON-management VLAN (lab / bring-up). Instant On–shaped.
type MgmtStaticOwnerProber interface {
	ProbeStaticOwner(ctx context.Context, vlanID int, addr, mask string) (owner int, raw string, err error)
}

// AsMgmtStaticOwnerProber returns the admin as a static-owner prober when it is one.
func AsMgmtStaticOwnerProber(a Admin) (MgmtStaticOwnerProber, bool) {
	var p, ok = a.(MgmtStaticOwnerProber)
	return p, ok
}

// RawReader : la plateforme sait rendre une unité de configuration TELLE QUELLE.
//
// FACULTATIF, et c'est un outil de RECONNAISSANCE, pas une fonction de produit. Il
// existe parce que ces interfaces ne sont pas documentées : on ne sait ÉCRIRE une
// section qu'après avoir vu son jeu de champs réel, et deviner produit le mode
// d'échec caractéristique de l'Instant On — `statusCode 0`, aucun changement, aucun
// message.
//
// LECTURE SEULE, et réservé à l'administration globale par l'appelant.
type RawReader interface {
	// ReadRaw rend le contenu brut de l'unité nommée.
	//
	// Le connecteur NETTOIE le nom avant de s'en servir : il part dans une URL, et
	// une injection y détournerait la requête vers un autre endpoint de
	// l'équipement.
	ReadRaw(ctx context.Context, unit string) (string, error)

	// RawKind nomme, pour un humain, ce qu'« une unité » veut dire sur cette
	// plateforme : « section wcd », « page *.b », « menu REST ».
	//
	// Sert à l'écran. Sans lui, la sonde de mise au point demanderait un nom de
	// section `wcd` à quelqu'un qui regarde un MikroTik, et l'échec ressemblerait à
	// une panne de l'équipement.
	RawKind() string
}

// Identifier : le connecteur sait dire QUEL équipement répond à une adresse.
//
// FACULTATIF, mais c'est la capacité qui rend la RELOCALISATION possible : quand un
// équipement change d'adresse, on le reconnaît à son numéro de série, et rien
// d'autre ne fait foi. Sur le Connector et non sur l'Admin, parce que la quantité
// d'identifiants nécessaires varie du tout au rien selon la plateforme :
//
//   - Instant On : AUCUN. La page de connexion porte `sysDescr` et le numéro de
//     série dans ses champs cachés, et l'UI de provisionnement du port 4443 les sert
//     en clair sur un équipement neuf ;
//   - SwOS : le MODÈLE sans identifiant — le realm de son challenge Digest le porte
//     (`realm="CSS326-24G-2S+"`) —, mais la série et le firmware demandent à entrer ;
//   - RouterOS : RIEN sans identifiant. L'API REST répond 401 sur toutes ses
//     ressources, y compris celle qui porte le modèle.
//
// D'où des candidats en paramètre, éventuellement vides. Un connecteur qui n'en a
// pas besoin les ignore ; un connecteur qui en a besoin rend ce qu'il a PU établir
// EN MÊME TEMPS que la raison de son arrêt — « c'est un CSS326-24G-2S+ dont on n'a
// pas le mot de passe » est un résultat utile là où « échec » n'en est pas un.
type Identifier interface {
	Identify(ctx context.Context, tgt Target, cands []Candidate) (Identity, error)
}

// Onboarder : la plateforme impose un CHOIX DE MODE DE GESTION avant de pouvoir
// être administrée localement.
//
// FACULTATIF, et à ce jour propre à une seule gamme : un Instant On sorti d'usine
// attend le portail cloud du constructeur, et il faut le lui faire abandonner
// (« Configure Locally ») pour qu'il accepte d'être administré par nous. Ni SwOS ni
// RouterOS n'ont d'équivalent — ils sont administrables dès le déballage.
//
// L'interface existe pour que le REFUS soit une absence de capacité et non une
// comparaison de chaîne : la couche API testait `driver == "aruba-instant-on-1930"`,
// ce qui range toute plateforme future du même côté que celles qui n'en ont pas
// besoin, et ne le dit pas.
type Onboarder interface {
	// Onboard fait basculer l'équipement en administration locale.
	//
	// ÉCRITURE, et choix DURABLE : sur la gamme connue, seul un retour aux réglages
	// d'usine le défait. Le connecteur vérifie donc l'état AVANT et relit APRÈS — un
	// 2xx qui ne change rien est un échec silencieux, et le comparer est la seule
	// preuve qui existe.
	Onboard(ctx context.Context, tgt Target) (OnboardResult, error)
}

// --- Résoudre un connecteur, et refuser lisiblement ---

// ConnectorFor rend le connecteur d'un équipement, ou l'explication de son absence.
//
// LE POINT DE PASSAGE OBLIGÉ de la couche API, et la raison d'être de ce fichier :
// tant que chaque handler appelait `instanton.OpenAdmin` en dur, aucun d'eux ne
// pouvait se tromper de plateforme — ils se trompaient tous, tout le temps, et ça ne
// se voyait que sur un parc à un seul constructeur. Un seul endroit qui résout, et
// trois refus distincts qui se lisent :
//
//   - aucun connecteur associé : la découverte n'a pas su ranger cet équipement, ou
//     il a été saisi à la main. Il n'y a rien à réparer côté meshcraft ;
//   - connecteur inconnu de ce binaire : la base porte le nom d'un connecteur que
//     cette version ne contient pas. Symptôme d'un retour en arrière de version, pas
//     d'un problème d'équipement ;
//   - connecteur présent mais qui ne sait pas ATTEINDRE la plateforme : meshcraft
//     sait décrire cette gamme (fingerprint, capabilities) et pas lui parler. Cet
//     équipement s'inventorie et se supervise, il ne se configure pas.
//
// Les trois se réparent par des gestes qui n'ont rien à voir entre eux, d'où trois
// phrases et non une.
func ConnectorFor(name string) (Connector, error) {
	if name == "" {
		return nil, errors.New("no connector is associated with this device: " +
			"meshcraft does not know which platform it would talk to, and does not guess — " +
			"re-run discovery on its address so it can be identified")
	}
	var d, known = Get(name)
	if !known {
		return nil, fmt.Errorf("connector %q is unknown to this meshcraft version: "+
			"the device was identified by a version that contained it. "+
			"Nothing to do on the device — it is the controller binary that is not "+
			"the expected one", name)
	}
	var c, reachable = d.(Connector)
	if !reachable {
		return nil, fmt.Errorf("connector %q can DESCRIBE this platform but "+
			"not yet reach it: meshcraft can inventory and monitor it, it cannot "+
			"write anything on it. This is not a failure, it is a capability that "+
			"is not shipped", name)
	}
	return c, nil
}

// Unsupported : le connecteur ne porte pas cette capacité.
//
// Un TYPE et non un message, pour deux raisons. D'abord parce qu'un appelant doit
// pouvoir le reconnaître : `errors.Is(err, driver.ErrNotImplemented)` répond vrai,
// ce qui donne UN seul test pour les deux mécanismes du paquet — l'interface
// facultative absente et la méthode obligatoire pas encore codée. Ensuite parce que
// les trois champs sont ce qu'un écran doit afficher séparément : ce qui a été
// demandé, à qui, et pourquoi c'est sans issue.
//
// `Because` est le champ qui fait la valeur de tout ceci, et il ne doit pas être
// vide. « Non supporté » envoie chercher une case à cocher ; « cette plateforme n'a
// aucun champ de destinataire de traps, elle se supervise par interrogation »
// termine la recherche.
type Unsupported struct {
	// Connector est le nom du connecteur, tel que [Info.Name] le porte.
	Connector string
	// Gesture est ce qui a été demandé, nommé comme un exploitant le nommerait —
	// « déclarer un destinataire de traps », pas « ApplyTrapReceiver ».
	Gesture string
	// Because est la raison, du côté de la PLATEFORME. Elle doit répondre à « y a-t-il
	// quelque chose à attendre ? ».
	Because string
}

func (e *Unsupported) Error() string {
	var out = e.Gesture + " : le connecteur « " + e.Connector + " » ne sait pas le faire"
	if e.Because != "" {
		out += " — " + e.Because
	}
	return out
}

// Unwrap rattache ce refus à [ErrNotImplemented] : les deux façons de dire « je ne
// sais pas faire » se testent alors d'une seule manière chez l'appelant.
func (e *Unsupported) Unwrap() error { return ErrNotImplemented }

// NotSupported construit le refus d'une capacité absente.
//
// À utiliser DÈS QU'UNE ASSERTION DE TYPE ÉCHOUE, et avant d'avoir joint
// l'équipement : c'est tout l'intérêt des interfaces facultatives. Joindre
// l'équipement pour lui demander quelque chose qu'on sait déjà impossible coûte une
// session sur une plateforme qui n'en accepte que cinq, et rend un message qui
// parlera de transport là où le fait est une capacité.
func NotSupported(connector, gesture, because string) error {
	return &Unsupported{Connector: connector, Gesture: gesture, Because: because}
}
