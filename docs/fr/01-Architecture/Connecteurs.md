---
tags: [architecture, connecteurs]
---

# Connecteurs (drivers)

Le cœur de la modularité : un connecteur = le code qui sait parler à **une plateforme** (Cisco IOS, Aruba AOS-CX, MikroTik RouterOS, HP ProCurve, D-Link web-only…). Il consomme le [[Modèle de données standard]] en entrée et produit ce que la plateforme comprend (CLI, API REST, formulaires web) ; en lecture, il fait l'inverse : il normalise l'état de l'équipement vers le modèle standard.

## Interface Go

```go
type Driver interface {
    // Identité et périmètre
    Info() DriverInfo                          // nom, plateforme, versions supportées
    Capabilities(model DeviceModel) Capabilities

    // Cycle read → plan → apply (voir Réconciliation)
    ReadState(ctx context.Context, s Session) (*DeviceState, error)   // état courant normalisé
    Plan(ctx context.Context, diff Diff, caps Capabilities) (*Plan, error) // diff → opérations ordonnées
    Apply(ctx context.Context, s Session, p *Plan, opts ApplyOpts) (*ApplyResult, error)

    // Support
    BackupConfig(ctx context.Context, s Session) ([]byte, error)      // conf brute (archivage)
    ImportConfig(raw []byte) (*DeviceState, error)                    // conf brute → modèle standard
    Fingerprint(ctx context.Context, probe ProbeResult) (score int)   // "cet équipement est-il pour moi ?"
    ResetProcedure() ResetProcedure                                   // retour usine : CLI + matériel
}
```

- `Session` encapsule le transport déjà établi **via le [[Proxy]]** (`ProxyHandle.DialTCP`) : le driver reçoit un `net.Conn` et n'a aucune notion de tunnel.
- `ImportConfig` est le pendant lecture de `Plan`/`Apply` : il alimente le choix « importer la configuration locale » proposé à l'adoption ([[Discovery]]). Sans session : il travaille sur du texte, ce qui permet aussi d'importer une configuration collée depuis une console série.
- `ResetProcedure` est du **savoir**, pas une action : le driver décrit la remise à zéro (voie CLI, voie matérielle, ce qui est effacé, pièges), meshcraft ne l'exécute pas à la place de l'opérateur.

> [!info] Le voisinage LLDP n'est **pas** une méthode de connecteur, et c'est délibéré
> Une version aspirationnelle de ce contrat portait un `CollectLLDP`. Il n'a jamais existé
> dans le code et n'existera pas : le voisinage LLDP, livré le 29/07/2026
> ([[Voisinage LLDP]]), est un jeu d'OID de plus dans le **poller SNMP du proxy**
> (`internal/snmppoll/lldp.go`), au même titre que `if_state`, `health` ou
> `optical_power`.
>
> Ce n'est pas un raccourci mais la conséquence d'une règle déjà écrite ailleurs : la
> **collecte périodique s'exécute sur le proxy et ne parle que SNMP**, là où un connecteur
> s'exécute sur le contrôleur et dialogue par le tunnel. Une telle méthode appelée par le
> contrôleur aurait fait traverser le WAN à chaque relevé de table, pour une donnée que
> l'agent SNMP local sert déjà — et surtout elle aurait créé un **second chemin de
> vérité** pour la même information, à arbitrer contre le poll (le raisonnement des traps,
> [[Monitoring]] §2).
>
> Ce que les connecteurs gardent, et qui est bien leur rôle : **déclarer** le canal `lldp`
> et son plancher de cadence, et déclarer son **indisponibilité** avec son motif quand la
> plateforme ne le sert pas — ce que fait `swos` avec un relevé ferme. C'est ce qui allume
> ou éteint la collecte, équipement par équipement.
- `Apply` reçoit des options : `DryRun`, `ConfirmTimeout` (rollback automatique si non confirmé), voir [[Réconciliation]].

## Transports partagés

Les drivers ne réimplémentent pas SSH ou HTTP ; ils composent des briques communes :

| Brique | Base | Usage |
|---|---|---|
| `transport/cli` | **scrapligo** (SSH + Telnet, gestion prompt/paging) | plateformes CLI : IOS, AOS-CX, ProCurve, RouterOS |
| `transport/rest` | net/http + resty, auth pluggable | plateformes à API : AOS-CX REST, RouterOS REST, unifi-like |
| `transport/webform` | http + goquery (scraping/POST de formulaires) | vieux switchs web-only (D-Link smart, TP-Link Easy Smart) |
| `parse` | templates TextFSM (port Go) + regex maison | parser `show vlan`, `show interfaces status`… |
| `render` | text/template par plateforme | générer les blocs CLI |

Telnet : supporté par scrapligo ; à réserver aux équipements sans SSH, avec un warning permanent dans l'UI ([[Points durs et risques]]).

## Capabilities

Chaque connecteur **déclare** ce que la plateforme (et le modèle précis) sait faire ; l'orchestrateur valide l'intent avant de planifier :

```go
type Capabilities struct {
    Ports          []PortDef // nom, type (cuivre/SFP/SFP+), vitesse max, PoE
    VlanRange      [2]int    // ex. 1–4094
    Features       map[Feature]bool // Trunk8021Q, PVID, PoE, STP, RSTP, MSTP,
                                    // DHCPSnooping, RAGuard, IGMPSnooping, LACP,
                                    // SNMPv2, SNMPv3, SyslogRemote, SSHMgmt, HTTPMgmt...
    MaxACLs        int
    ConfigCommit   CommitStyle // none | commitConfirm | archiveRollback | rebootRequired

    // Interface d'administration : ce que le modèle admet réellement. Le bas de
    // gamme se limite souvent au VLAN 1, ou à l'untagged — pousser un VLAN de
    // management tagué le rendrait injoignable ([[Adressage de management]]).
    Mgmt MgmtCapabilities // VlanIDs (any|fixed), Tagging (tagged_ok|untagged_only),
                          // Addressing (static|dhcp|both)
}
```

Un intent qui demande une feature absente produit une **erreur de compilation lisible** (« RA-Guard non supporté par DGS-1210 ») — jamais un échec silencieux au déploiement. L'UI grise les options non disponibles pour l'équipement sélectionné.

> [!warning] Une capability inconnue est supposée ABSENTE
> Pour tout ce qui peut couper l'accès à l'équipement — au premier chef
> l'interface de management ([[Adressage de management]]) —, l'inconnu se traite
> comme un refus. Un modèle dont on ne sait pas s'il accepte un VLAN de
> management tagué est traité comme ne l'acceptant pas : au pire on demande une
> confirmation inutile, au mieux on évite un déplacement sur site.

> [!warning] Une capability déclarée que personne ne lit ne protège de rien
> `AdminAccount.PasswordMax` a passé un lot entier **déclaré par les connecteurs et
> lu par aucun code**. Le défaut ne s'est vu qu'à la première écriture réelle : le
> secret de site faisait 17 caractères, un MikroTik CSS326 en accepte 15, et aucun
> mot de passe n'était posable sur cette gamme. Le connecteur refusait correctement
> avant d'émettre un octet — la capability était juste, le garde-fou fonctionnait —
> mais rien en amont ne s'en servait pour produire un secret acceptable.
>
> C'est le mode d'échec propre à ce fichier : une capability n'est pas une
> documentation, c'est une **entrée de décision**. Le réflexe à garder en ajoutant
> un champ ici est de nommer, dans le même lot, **qui le consulte** — sinon il
> décrit fidèlement une contrainte que le produit continue de violer.
>
> Réparé pour la longueur (voir [[Étiquettes et portée des identifiants]] §3.2,
> l'étiquette technique `pwpolicy-`). Deux manques du même ordre restent ouverts :
>
> - **`AlphanumericOnly` est trop grossier.** La contrainte réelle de SwOS est
>   *ASCII seul*, pas *alphanumérique seul* : elle est aujourd'hui déclarée **en
>   prose** dans `AdminAccount.Notes` et appliquée à la main dans le
>   `checkPassword` du connecteur — donc invisible pour tout consommateur du
>   contrat. Un `PasswordCharset` (`alnum` | `ascii` | `any`) remplacerait le
>   booléen sans rien perdre. Non construit : trois connecteurs et la couche API
>   dépendent de ce contrat.
> - **La composition exigée n'est pas une capability du tout.** « Au moins une
>   majuscule, une minuscule, un chiffre » est câblé dans `internal/pwgen` pour
>   *tout* le parc, alors que c'est une politique de plateforme — et RouterOS,
>   justement, publie la sienne (`/rest/user/settings`) et la fait varier.

### Déclarer ce que la plateforme n'expose PAS

Tout ce qui précède décrit ce qu'une plateforme **sait faire**. Il manquait le pendant,
et son absence coûtait cher à l'endroit le moins visible : la **supervision**. Un
connecteur peut désormais déclarer qu'une métrique est **indisponible sur sa
plateforme**, et — c'est le champ qui fait la valeur du mécanisme — dire
si l'absence est **constatée sur matériel** ou seulement **supposée**
(`driver.MetricUnavailable`, `internal/driver/driver.go`). Le texte operator-facing
n'appartient **pas** au connecteur : l'UI le localise depuis la clé de métrique
(`monitoring.unavailable.<metric>`). C'est le calque exact de
`ResetProcedure.Confirmed`, qui distingue déjà le savoir vérifié du savoir déduit.

Tout ceci vit dans `Capabilities.Monitoring` (`driver.MonitoringCapabilities`), que
l'extrait de structure ci-dessus — antérieur au canal de monitoring — ne montre pas : ce
sont les canaux de collecte de chaque plateforme, leur plancher, sa capacité à **pousser**
traps et syslog, et désormais ce qu'elle **ne sert pas** ([[Monitoring unifié et
pipelines]] §1).

**La distinction qui justifie tout le mécanisme.** Deux situations se lisent
**identiquement** à l'écran — pas de courbe — et mènent à des décisions **opposées** :

| Ce que l'exploitant constate | Cause réelle | Ce qu'il doit faire |
|---|---|---|
| pas de courbe | *meshcraft ne le collecte pas encore* — un manque de **notre** côté | attendre une version, et il a raison d'attendre |
| pas de courbe | *la plateforme ne l'expose pas* — un fait de l'équipement | changer de matériel, ou renoncer. Aucun développement n'y changera rien |

Les confondre coûte du temps et de la crédibilité, dans les deux sens : un exploitant
qui attend une version qui ne viendra jamais, ou qui remplace un switch pour une
métrique qu'un lot de collecte aurait servie.

Trois propriétés tiennent la déclaration honnête :

- **`Observed` décide du TON.** Une absence constatée s'affiche sans réserve ; une
  absence supposée d'après la documentation d'une gamme doit rester **révisable** — le
  firmware suivant ou un autre modèle peut la démentir. Les afficher pareil ferait
  renoncer un exploitant sur une supposition, exactement la faute que ce champ existe
  pour empêcher. `Source` dit d'où vient le savoir : le relevé qui l'établit, ou la page
  de guide qui le laisse supposer ;
- **`Contradictions()` nomme les déclarations qui se mordent.** Une métrique annoncée à
  la fois **rendue par un canal** et **indisponible** est pire que pas de déclaration :
  soit l'écran promet une courbe qu'aucune collecte ne remplira, soit il affiche un
  motif d'absence à côté d'une courbe qui existe. Dans les deux cas l'exploitant perd la
  seule chose que ces capabilities lui apportent — la certitude que ce qui est affiché
  correspond à l'équipement. Ce n'est pas un contrôle d'exécution mais un **invariant
  verrouillé par les tests de chaque connecteur**, y compris ceux qui n'existent pas
  encore : l'incohérence devient impossible à introduire en silence ;
- **vide n'est pas un aveu d'ignorance, et ne dit rien de la disponibilité.** La règle
  du projet reste *une capability inconnue est supposée absente* (callout ci-dessus) :
  une métrique sans canal n'est pas supervisée, déclaration ou pas. Ce champ n'ajoute
  que le **motif** là où on le connaît — d'où le `ok = false` de `WhyUnavailable`, qui
  signifie « personne ne l'a documenté », jamais « c'est disponible », et l'`omitempty`
  du payload, un tableau vide se lisant à l'écran comme « tout le reste est
  disponible ».

> [!warning] Ce qui n'a RIEN à faire dans cette liste : nos propres manques
> Sur le premier connecteur, santé, LLDP et PoE n'y sont **pas**, et c'est le point le
> plus facile à rater. L'équipement les sert, ils ont leur canal déclaré avec son
> plancher ; si aucune courbe n'en sort, c'est **meshcraft** qui ne les lit pas encore
> (cf. [[Monitoring]] §1). Les ranger ici les déclarerait **impossibles**, et ferait
> renoncer un exploitant à ce qui n'attend qu'un lot de collecte.
>
> La distinction a été **vérifiée par les faits** depuis : `lldp` était dans ce cas — un
> manque de notre côté — et le lot de collecte est arrivé le 29/07/2026 sans qu'aucune
> déclaration de connecteur ne change, sauf celle de `swos`, qui l'annonçait
> **indisponible** et avait raison. Un exploitant qui avait lu « meshcraft ne le lit pas
> encore » a bien eu sa courbe ; celui qui lisait « ce firmware n'a pas la fonction »
> n'attend toujours rien, et c'est exact.
>
> Le critère est donc mécanique : cette liste ne porte que ce que **l'équipement ne
> sert pas**. Un manque de notre côté se voit autrement — à l'absence de collecte **en
> face** d'un canal qui, lui, sait rendre la métrique.

**La déclaration remonte jusqu'à la fiche équipement**, et c'est ce qui la rend utile :
elle était sinon du savoir enfermé dans le code du connecteur. `driver.Capabilities`
n'est sérialisé par aucune route — ses consommateurs sont internes (cadences de
surveillance, capacité à pousser des traps, compte d'administration) — si bien que les
tags JSON du type étaient prêts et **inertes**. La déclaration voyage donc sur le payload que
l'écran de l'équipement lit déjà (`monitoring_unavailable` : métrique, `observed`,
`source` technique optionnelle). L'UI affiche le **libellé localisé** pour cette métrique.
Une route dédiée aurait coûté un second aller-retour par fiche et
affiché **d'abord un blanc, puis l'explication** — or c'est précisément ce blanc qu'on
supprime. L'explication est ainsi **là où l'exploitant cherchait la donnée**, et le relevé qui
l'établit est **consultable** : un sceptique refait l'interrogation lui-même par
`POST /api/devices/{id}/snmp-walk` ([[Protocole contrôleur-proxy]], § *Les questions
ponctuelles*), sur son propre équipement, et retrouve la même réponse vide.

| Question | État |
|---|---|
| Déclarer une métrique indisponible (`metric`, `Observed`, `Source`) | **livré** — `driver.MetricUnavailable` |
| Refuser les déclarations qui se contredisent | **livré** — `Contradictions()`, verrouillé par les tests de connecteur |
| Explication affichée sur la fiche équipement | **livré** — déclaration `monitoring_unavailable` + i18n `monitoring.unavailable.*` |
| Premier cas réel déclaré | **livré** — la puissance optique du 1930 (§ *Puissance optique des SFP+*, plus bas) |
| Distinguer à l'écran « pas encore collecté » de « jamais collectable » pour les métriques **sans** déclaration | **ouvert** — aujourd'hui les deux se ressemblent tant que le connecteur ne dit rien |

## Variations de modèles

Une plateforme couvre des dizaines de modèles (8/24/48 ports, PoE ou non). Le connecteur embarque un **catalogue de modèles** (YAML embarqué) : nombre et nommage des ports, uplinks SFP, PoE budget, dimensions pour l'UI ([[Rendu des ports]]), logo. Le fingerprint ([[Discovery]]) associe `sysObjectID`/banner → plateforme → modèle.

```yaml
# connectors/cisco_iosl2/models/c1000-24p.yaml
model: C1000-24P-4G-L
ports:
  - {name: "Gi1/0/{1..24}", type: rj45, speed: 1G, poe: true}
  - {name: "Gi1/0/{25..28}", type: sfp, speed: 1G}
poe_budget_w: 195
ui: {rows: 2, logo: cisco}
```

## Organisation & extensibilité

- Un package Go par connecteur : `internal/connectors/<plateforme>/`, enregistré dans un registre au démarrage (`func init()` + build tags éventuels).
- **Compilés dans le binaire** au MVP. Si un écosystème tiers émerge : passage à `hashicorp/go-plugin` (process séparé, RPC) sans changer l'interface `Driver`. Pas de plugins dynamiques `.so` (fragiles en Go).
- **Tests** : chaque driver est livré avec des transcripts enregistrés (sorties `show` réelles anonymisées) rejoués contre le parser ; tests d'intégration optionnels sur containerlab / équipements de lab.
- Connecteurs cibles du MVP : voir [[Roadmap]].


## Premier connecteur livré : Aruba Instant On 1930

`internal/driver` porte le contrat, `internal/driver/instanton` le premier
connecteur. C'est volontairement une plateforme **web-only** : ni SSH, ni Telnet,
ni console série. Commencer par le cas le plus contraint évite de bâtir le
contrat autour d'une CLI et de découvrir trop tard qu'il ne s'y plie pas.

Ce que le guide constructeur (réf. 5200-5879) a appris, et qui change la
conception :

- **`Commit = apply_then_save`.** « *Changes take effect immediately but are not
  retained across a switch reset unless you click Save Configuration* » : appliquer
  et persister sont **deux gestes**. C'est un rollback gratuit et il faut s'en
  servir dans cet ordre — appliquer, **vérifier la joignabilité**, puis sauver. Si
  on perd l'équipement entre les deux, une coupure d'alimentation le ramène à sa
  dernière configuration sauvée. Cette stratégie rejoint le tableau de
  [[Réconciliation]] sans que la plateforme ait de `commit confirmed`.
- **Le VLAN de management est libre (1–4092)**, contrairement à ce qu'on suppose
  d'un switch d'entrée de gamme — mais « *un VLAN sans port membre ne peut pas
  être désigné VLAN de management* ». C'est une contrainte d'**ordre** : créer le
  VLAN, y affecter des ports, et seulement ensuite le désigner. Exactement le
  genre de savoir que `Plan()` doit porter ([[Adressage de management]]).
- **SNMP n'est autorisé que sur le VLAN de management** : le collecteur dérivé par
  le [[Monitoring]] doit être joignable dans ce VLAN, sinon la télémétrie ne
  remonte pas alors que la configuration semble correcte.
- **5 sessions HTTP/HTTPS au total.** Un connecteur qui ouvre des requêtes en
  parallèle sans se brider évince l'administrateur humain de sa propre interface.
  `MaxSessions` existe pour ça, et il doit être respecté.
- **SNMPv1/v2c/v3** : la gamme sait faire du v3, donc la résolution de
  [[Identité de service et rotation]] §2.1 y choisira v3 — l'entrée de gamme
  n'implique pas v2c.

Le fingerprint accroche la **famille** (« Instant On ») avec une confiance
modérée et non le modèle : les gammes 1930 et 1830 servent la même interface web,
et leurs capabilities diffèrent. Le modèle exact se précise après connexion.
Prétendre reconnaître un 1930 sur la seule foi d'un titre HTML serait faux, et ce
sont des capabilities trop dangereuses pour être devinées.


### Ce que l'essai contre l'équipement réel a appris

Un 1930 48 ports (JL685A, firmware 3.4.0.0) a servi de premier cas. Quatre
enseignements, tous issus de l'essai et non de la lecture :

- **Un équipement s'identifie sans authentification, si on sait où regarder.**
  Fraîchement sorti d'usine, l'UI de provisionnement (port 4443) expose
  `/api/systemInfo`. Une fois basculé en administration locale ce port se ferme —
  mais la **page de connexion** de l'UI classique porte les mêmes informations
  dans ses champs cachés : `sysDescr` donne le libellé complet, la référence
  `JL685A`, le firmware et la façade (« 48p Gigabit 4p SFP+ »), et
  `rlPhdUnitGenParamSerialNum` donne le numéro de série. D'où un `Inspect` à deux
  sources, qui fait passer la confiance de 55 (titre HTML) à 96 (l'équipement
  parle de lui-même).

- **La sonde générique ne doit pas essayer de tout savoir.** Modèle exact, série,
  façade : c'est du savoir de plateforme. Le connecteur l'apporte par un point
  d'enrichissement appelé après l'identification, et la sonde reste ignorante des
  particularités constructeur.

- **La façade physique est un fait, pas une intention.** À l'adoption en mode
  « pousser », l'équipement démarre vierge dans le modèle — mais ses 48 ports
  cuivre et ses 4 SFP+ existent. Sans reprise de la façade relevée, l'adoption
  produisait un équipement **sans aucun port**, ce qui n'est pas « vierge » mais
  faux.

- **Ce switch est fragile sous rafale.** Le guide annonce 5 sessions HTTP/HTTPS au
  total ; une sonde qui ouvre dix connexions en deux secondes se fait refouler par
  intermittence. Un enrichissement raté était initialement **silencieux** — donc
  indiagnosticable. Il laisse maintenant une note explicite : on doit pouvoir
  distinguer « le connecteur n'a rien à dire » de « le connecteur n'a pas pu
  parler à l'équipement ».


### Interface d'administration classique (port 443) — ce qui a été relevé sur matériel

L'UI de configuration du 1930 n'est pas documentée par le constructeur. Ce qui
suit est relevé sur un équipement réel (48 ports, firmware 3.4.0.0), et chaque
point a coûté un aller-retour :

**La connexion ne passe pas par le formulaire.** La page sert un `<form>`
`signinForm` avec `inputUsername`/`inputPassword` — il n'est JAMAIS soumis. Le
bouton appelle un `formSubmit()` qui :

1. lit `/device/wcd?{EncryptionSetting}` (servi à la racine, **sans**
   authentification) : `passwEncryptEnable`, `rsaPublicKey` (PKCS#1, 2048 bits),
   `loginToken` ;
2. chiffre en RSA PKCS#1 v1.5 le message
   `user=<u>&password=<p>&ssd=true&token=<tok>&`, où `<u>` et `<p>` sont passés à
   `encodeURIComponent` — et non à un encodage de formulaire, qui rendrait un
   espace en `+` ;
3. fait un **GET** sur `<session>/hpe/config/system.xml?action=login&cred=<hex>`.

Poster le formulaire tel quel renvoie un 404.

**Le code d'état ment.** La page affiche pour le code 12 « Login failed due to
Device password encryption problems », alors que son propre `stateChange()`
traite ce même 12 comme une connexion RÉUSSIE et que l'équipement renvoie
`statusString = "Default user Login"`. Les codes qui valent session ouverte sont
`0`, `9`, `10`, `12`, `13`, `14` ; seul `4` est un refus d'identifiants, et `5`
signale la saturation des 5 sessions. Se fier au message de l'UI faisait conclure
à une panne de chiffrement là où le compte d'usine venait d'ouvrir une session.

**Le compte d'usine ne se teste que par cette porte.** Chiffrement actif et mot
de passe vide donnent le code 12 « Default user Login » : c'est une réussite, pas
une erreur.

**Les cinq sessions sont une vraie limite.** Chaque essai de mise au point en
consomme une, et aucun point de déconnexion n'a été identifié dans les scripts
servis. Une sonde qui ouvre une session par requête s'évince elle-même en cinq
appels — d'où la sonde `POST /api/devices/{id}/web-probe` (lecture seule,
global-admin) qui accepte PLUSIEURS chemins dans une seule session.

**L'écriture n'est pas encore acquise.** Le bundle décrit un `localPost` qui
POSTe vers `tmpl.url` un corps XML produit sans enveloppe
(`<UpdatePassword><edit><userName/><oldPassword/><newPassword/><newPasswordConfirmation/></edit></UpdatePassword>`),
succès quand `ActionStatus/statusCode` vaut `0`. Mais `tmpl.url` n'apparaît dans
aucun des trois scripts servis par la page — vraisemblablement un chunk chargé
dynamiquement. Toutes les variantes essayées (`…/hpe/wcd`, types de contenu
`application/xml` puis `x-www-form-urlencoded`, en-têtes d'un `$.ajax` jQuery,
enveloppe `<DeviceConfiguration>`) répondent **200 avec un corps vide**. Le
connecteur le signale comme tel plutôt que de conclure au succès.

**La gamme sait créer des comptes nommés.** `AdminUserSetting.add` avec
`AdminUserEntry{userName, password, accessLevel}`, où nom et mot de passe sont
chiffrés en RSA et préfixés `crypto_!`. `mcadmin` est donc posable sur ce
matériel — la capability `Admin.Custom` reste à `false` tant que le connecteur ne
sait pas le faire, parce qu'annoncer un nom posable et échouer à la pose est pire
que d'annoncer `admin` et tenir parole.

### Destinataires de traps SNMP — relevé sur matériel, en lecture seule

Le connecteur savait poser une identité SNMP (`SNMPGlobalSetting`, `CommunityList`,
`SNMPGroupList`, `SNMPUserISList`) mais **rien du chemin retour** : aucune section
de destinataire de traps. Ce qui suit comble ce trou. Tout a été relevé sur
`sw-labo-1930` (JL685A, firmware 3.4.0.0) à travers `POST /api/devices/{id}/web-probe`,
**sans écrire une seule fois** sur l'équipement. Coût : **deux** sessions
authentifiées sur les cinq — les bundles JavaScript et le dictionnaire se lisent
**sans authentification**, ils ne consomment pas de session, et une fois rapatriés
ils se fouillent hors ligne autant qu'on veut. C'est la bonne façon de travailler
sur cette gamme : rapatrier le savoir, puis n'ouvrir de session que pour lire
l'état.

**Il y a DEUX sections, pas une.** L'interface sépare les destinataires selon la
version SNMP, et les deux moitiés n'ont ni le même nom, ni la même forme :

| Version | Section `wcd` | Liste | Entrée |
|---|---|---|---|
| v1 / v2c | `SNMPTrap` | `SNMPTrapReceiverList` | `SNMPTrapReceiverEntry` |
| v3 | `NotificationReceiverList` | *(la section EST la liste)* | `Receiver` |

Les deux lectures — `wcd?{SNMPTrap}` et `wcd?{NotificationReceiverList}` — ont été
faites et répondent une section `type="section"` en bonne et due forme.

> [!warning] `SNMPTrapReceiverList` n'est PAS un nom de section
> C'est un **enfant** de `SNMPTrap`. Le piège est le même que pour
> `SNMPCommunityList` contre `CommunityList` : un nom plausible, et la réponse
> d'une section inexistante. La liste v1/v2c ne se lit qu'en demandant `SNMPTrap`,
> et le POST d'un destinataire v1/v2c porte `<SNMPTrap action="set">` — pas
> `<SNMPTrapReceiverList action="set">`. Symétriquement, pour le v3 il n'existe
> **aucune** section « SNMPTrapV3 » : c'est `NotificationReceiverList`, sans
> préfixe `SNMP`.

**Les champs, et leurs codes.** Relevés dans `pages2.js` (page « Switching → SNMP »,
`addTrapReceiversV1V2Submit` et `addTrapReceiversV3Submit`), traduits par
`dictionary1.xml` :

| Champ | v1/v2c | v3 | Valeurs |
|---|---|---|---|
| `receiverIPAddress` | oui | oui | IPv4 unicast (`unicastipv4` sur cette famille) |
| `UDPPortNo` | oui | oui | défaut **162**, plage 1–65535 |
| `notificationType` | oui | oui | `1` = Trap, `2` = Inform (défaut `1`) |
| `timeout` | oui | oui | 1–300 s, défaut 15 — **Inform seulement** |
| `retries` | oui | oui | 1–255, défaut 3 — **Inform seulement** |
| `communityString` | oui | — | 1–20 caractères |
| `SNMPVersion` | oui | — | `1` = SNMPv1, `2` = SNMPv2 (défaut du formulaire : **`1`**) |
| `userName` | — | oui | 1–20 caractères, **texte libre** |
| `securityType` | — | oui | `1` = noAuthNoPriv, `2` = authNoPriv, `3` = authPriv |
| `filterEnabled` | oui | oui | `1` = un filtre est rattaché, `2` = aucun |
| `filterName` | oui | oui | nom d'un `Filter` de `NotificationFilterList`, vide sinon |

Les codes de `securityType` sont ceux que `snmp.go` connaît déjà sous le nom
`securityType` des groupes : même énumération, même signification.

> [!warning] Deux défauts de formulaire à ne pas recopier
> `SNMPVersion` vaut **`1` (SNMPv1)** par défaut dans l'interface : un connecteur
> qui omet le champ ou recopie le défaut déclare un destinataire **v1**, et le
> collecteur v2c ne verra jamais rien d'exploitable. Et quand
> `notificationType = 1` (Trap), l'interface **vide** `timeout` et `retries` avant
> d'envoyer — les envoyer renseignés sur un Trap est un cas non observé.

**Le port de destination est configurable.** `UDPPortNo` est un champ de saisie
ordinaire, 162 par défaut, validé sur 1–65535. C'est directement utile : le
[[Proxy]] n'a pas à écouter sur un port privilégié si on ne le souhaite pas.

**linkUp / linkDown : c'est un réglage PAR PORT, et il est déjà actif.** Il n'y a
sur cette gamme ni case « activer les traps de lien », ni liste de traps par
catégorie. Le réglage vit dans la configuration d'interface :

```text
Standard802_3List → Entry{interfaceName, linkStateTrapEnabled}   1 = activé, 2 = désactivé
```

Relevé sur l'équipement : **68 entrées** (52 ports physiques + 16 agrégats `TRK`),
`linkStateTrapEnabled = 1` sur **toutes**. Autrement dit, la télémétrie de lien ne
demande aucune préparation côté port : il suffit de déclarer un destinataire.
Trois autres interrupteurs sont portés par l'équipement, tous à `1` :

```text
SNMPGlobalSetting → trapsEnbl = 1       émission des traps
SNMPGlobalSetting → authTrapsEnbl = 1   traps d'échec d'authentification
SNMPTrap          → enabled   = 1       (interrupteur de la section v1/v2c)
```

> [!warning] Ces trois interrupteurs n'apparaissent dans AUCUNE page de l'UI
> Aucun des trois bundles servis (`mainpage.js`, `pages1.js`, `pages2.js`) ne
> mentionne `trapsEnbl`, `authTrapsEnbl` ni `SNMPTrap/enabled` : l'équipement les
> **rend** en lecture, mais son interface ne les écrit jamais. Le connecteur doit
> donc les **vérifier** (un équipement qui les porterait à `2` n'émettrait rien,
> et rien dans l'UI ne le montrerait) et **ne pas** chercher à les poser : un champ
> qu'aucun chemin de l'interface n'écrit est un champ dont on ne sait pas si
> l'écriture est acceptée. Même prudence que pour `Admin.Custom` : ne pas annoncer
> ce qu'on ne sait pas tenir.

**Le filtrage se fait par OID, pas par catégorie.** `NotificationFilterList` porte
des `Filter{filterName, OIDSubtreeList{OIDEntry{OIDSubtree, filterAction}}}`, avec
`filterAction` `1` = *Included* et `2` = *Excluded* (nom de filtre ≤ 30
caractères, OID ≤ 255). Un destinataire s'y rattache par `filterEnabled = 1` +
`filterName`. Sur l'équipement de labo la liste est **vide** : aucun filtre, donc
aucun trap écarté. Pour le [[Monitoring]] c'est la situation souhaitable — on ne
veut pas d'un filtre qui masque silencieusement les événements de lien.

**v2c et v3 : ce que le v3 exige de plus.** Le champ `userName` du formulaire v3 est
un **texte libre**, sans liste déroulante alimentée par `SNMPUserISList` :
l'équipement n'oppose donc aucune vérification à la saisie d'un utilisateur
inexistant. La conséquence est désagréable — un destinataire v3 mal nommé
s'enregistre sans erreur et n'émet rien d'authentifiable. Le connecteur doit
poser l'utilisateur v3 **avant** le destinataire, et vérifier la correspondance
lui-même. Le groupe doit porter un `notifyView`, ce que `groupEntry()` fait déjà.

> [!warning] Un Inform v3 n'est pas un Trap v3
> Un **Trap** v3 s'authentifie contre le moteur SNMP local, dont l'identifiant est
> relevé (`localEngineID = 8000000b0302005e1061a7`, `localEngineIDType = 2`,
> `localEngineIdState = 1`) : l'utilisateur posé en `engineIDType 1` suffit. Un
> **Inform** v3, lui, est acquitté par le récepteur et se dérive contre le moteur
> **du récepteur** — il réclame une entrée `SNMPRemoteEngineIDList{Entry{IPAddress,
> remoteEngineID}}` (10–64 caractères hexadécimaux) et un utilisateur en
> `engineIDType 2`. Ce dernier point est **déduit du bundle**, non vérifié sur
> matériel : tant qu'il ne l'est pas, viser le **Trap** v3 ou v2c, et non l'Inform.

**L'IP source des traps est l'IP de management, et ce n'est pas un choix.** Aucun
des bundles ne porte de réglage de type `source-interface` pour SNMP (les seuls
`sourceInterface` du code concernent RMON et le port-mirroring). Et l'équipement
n'a qu'**une** interface IP exploitable : `IPv4InterfaceList` rend `VLAN99` =
`198.51.100.101/24` (`owner = 2`, statique), la seule autre entrée étant
`User Defined Port 1` = `169.254.0.1/24` en `owner = 3`, que l'interface elle-même
exclut de ses listes. Les traps partiront donc de l'adresse de management
([[Adressage de management]]) — ce qui est exactement ce dont le [[Proxy]] a besoin
pour reconnaître l'émetteur contre sa liste de surveillance.

> [!warning] Le corollaire : c'est le VLAN de management qui décide
> SNMP n'écoute et ne parle que sur le VLAN de management sur cette gamme. Changer
> ce VLAN change l'IP source des traps, donc invalide silencieusement la liste de
> surveillance du proxy — les traps continueront d'arriver et seront tous jetés en
> « source non surveillée ». Toute relocalisation de management doit être suivie
> d'une reprise des watches.

**État de cet équipement : aucun destinataire n'est déclaré.** `SNMPTrapReceiverList`
et `NotificationReceiverList` sont vides, `NotificationFilterList` aussi — lu deux
fois, dans deux sessions distinctes. `SNMPEnbl = 1` : SNMP est actif (meshcraft l'y
a posé). Rien à retirer, rien à réconcilier : le terrain est vierge.

#### Ce qui n'est PAS établi, et ne peut pas l'être en lecture seule

- **Combien de destinataires la plateforme accepte.** Aucun plafond n'existe côté
  client : ni validation, ni message de dictionnaire (« table pleine », « maximum
  atteint »), ni compteur dans la configuration des tables. Les deux tables sont
  déclarées `multiSelect` + `selectAll`, ce qui **suggère** que plusieurs entrées
  sont attendues — mais suggérer n'est pas savoir, et le plafond réel est appliqué
  par le firmware au moment de l'ajout. Le connaître demande d'ajouter des entrées
  jusqu'au refus, donc d'écrire. **La capability « plusieurs destinataires » doit
  rester à `false` jusqu'à ce test** : c'est elle qui permettrait à la télémétrie
  de survivre à la perte d'un proxy sans redéployer le parc, et l'annoncer à faux
  coûterait la panne au moment précis où on comptait sur la redondance.
- **Le geste d'écriture.** Voir ci-dessous : hypothèse, jamais exécutée.

#### Le geste d'écriture — HYPOTHÈSE à valider par un test réel

`modules.js` livre enfin le mécanisme que la partie précédente laissait ouvert.
`tmpl.post()` fait un `POST` sur **`tmpl.url`**, et `tmpl.url` est positionné par
`tmpl.wcd(url)` à la **dernière URL de lecture, query string comprise** — d'où
l'obligation de porter `?{Section}` sur le POST. Le corps est produit par
`tmpl.json2xml()`, dont voici la règle exacte : enveloppe
`<?xml version='1.0' encoding='utf-8'?><DeviceConfiguration>…</DeviceConfiguration>`,
et pour chaque section un élément `<Section action="…">`. C'est **mot pour mot** la
convention que `writeSection()` applique déjà : rien à changer au transport.

En composant cela avec les objets relevés dans `pages2.js`, le geste v2c **serait** :

```text
POST  <session>/hpe/wcd?{SNMPTrap}
<?xml version='1.0' encoding='utf-8'?><DeviceConfiguration>
<SNMPTrap action="set"><SNMPTrapReceiverList><SNMPTrapReceiverEntry>
  <receiverIPAddress>…</receiverIPAddress><UDPPortNo>162</UDPPortNo>
  <notificationType>1</notificationType><timeout></timeout><retries></retries>
  <communityString>…</communityString><SNMPVersion>2</SNMPVersion>
  <filterEnabled>2</filterEnabled><filterName></filterName>
</SNMPTrapReceiverEntry></SNMPTrapReceiverList></SNMPTrap>
</DeviceConfiguration>
```

et le geste v3, `POST <session>/hpe/wcd?{NotificationReceiverList}` avec
`<NotificationReceiverList action="set"><Receiver>…</Receiver></NotificationReceiverList>`
portant `receiverIPAddress`, `UDPPortNo`, `notificationType`, `timeout`, `retries`,
`securityType`, `userName`, `filterEnabled`, `filterName`. La suppression est un
`action="delete"` ; elle rappelle la **clé** de l'entrée et non l'entrée entière —
`receiverIPAddress` + `notificationType` + `managementStationIPAddress` en v1/v2c,
`receiverIPAddress` + `notificationType` + `userName` + `securityType` en v3. Le
champ `managementStationIPAddress` de la suppression v1/v2c **n'existe pas** dans
l'ajout : il est lu depuis la ligne du tableau, donc rendu par l'équipement. Une
suppression construite sans lecture préalable ne peut pas désigner sa cible.

> [!warning] Ceci n'est pas un fait, c'est une reconstitution
> Rien de ce bloc n'a été exécuté. Sur cette interface, `statusCode 0` sans effet
> est un cas avéré, et un `200` à corps vide aussi : la seule preuve d'écriture est
> une **relecture** de `wcd?{SNMPTrap}` / `wcd?{NotificationReceiverList}` qui
> montre l'entrée. Le lot d'implémentation doit donc, dans l'ordre : lire, écrire,
> **relire et comparer champ par champ** (`snmp.go`/`verifySNMP` est le modèle),
> puis `SaveConfig` — et seulement alors déclarer la capability
> ([[Réconciliation]]).

### Puissance optique des SFP+ : la gamme ne l'expose pas — relevé SNMP

Premier usage réel de la déclaration d'indisponibilité (§ *Déclarer ce que la plateforme
n'expose PAS*), et **un relevé, pas une supposition** : `Observed = true`. La question de
départ est celle qu'un exploitant pose devant quatre liens fibre — « quel est mon budget
optique ? » — et la réponse est qu'il n'y en aura pas sur cette gamme.

Relevé sur `sw-labo-1930` (JL685A, firmware `InstantOn_1930_3.4.0.0`,
`sysObjectID = 1.3.6.1.4.1.11.2.3.7.11.199`, série `CN0EXAMPL1`), interrogé en **SNMP
v2c à travers le proxy** :

| Ce qui a été interrogé | Résultat | Ce qu'il faut en conclure |
|---|---|---|
| `ENTITY-SENSOR-MIB` (`1.3.6.1.2.1.99`) | **zéro ligne** | la voie **standard** des capteurs n'est pas servie du tout — ni température, ni optique |
| l'arbre entreprise `1.3.6.1.4.1`, **énuméré en entier** | **5001 OID**, tous sous `1.3.6.1.4.1.11.2` (branches `.29`, `.43`, `.35`, `.26`, `.2`, `.1`, `.38`, `.42`, `.36`) | ce que l'équipement sert en privé est connu **exhaustivement** : la conclusion ne repose pas sur un OID qu'on n'aurait pas pensé à essayer |
| une table indexée par les `ifIndex` des SFP+ (**49 à 52**) | **aucune**. Les seules tables à index numériques sont un registre de variables MIB (`snmpInPkts`, `ipForwarding`, `icmpInMsgs`) | il n'existe nulle part de grandeur *par port optique* — et un registre de compteurs SNMP n'est pas un capteur |
| `1.3.6.1.4.1.89` (Radlan/Marvell, que le `sysORID` **annonce**) | **zéro ligne** | une **capacité MIB déclarée** n'est pas de la donnée lisible. C'est le piège de ce relevé : `sysORID` invitait à chercher là |
| l'état des SFP+ au moment du relevé | **trois des quatre étaient up** (`ifIndex` 49, 50, 52) | transceivers insérés et liens établis : l'hypothèse « table vide faute de module » est **écartée**, et c'est ce qui transforme un doute en fait |

La déclaration qui en découle vit dans `Capabilities().Monitoring.Unavailable`
(`internal/driver/instanton/instanton.go`) sous la clé `optical_power`, avec son motif
rédigé pour un exploitant — *aucune version de meshcraft n'y changera quoi que ce soit ;
suivre un budget optique sur ces liens demande un équipement qui publie ces capteurs* —
et le relevé ci-dessus en `Source`.

> [!note] Comment ce relevé a été possible, et c'est l'enseignement de méthode
> La question `snmp_walk` posée **à travers le tunnel** permet de **découvrir** ce qu'un
> équipement sert, au lieu de deviner des OID dans une MIB constructeur et de livrer une
> collecte qui ne remonte rien ([[Protocole contrôleur-proxy]], § *`snmp_walk` :
> l'équipement sait dire ce qu'il expose*). Une MIB dit ce qu'une **gamme** est censée
> servir ; un walk dit ce que **cet** équipement, avec **ce** firmware, sert vraiment —
> et `sysORID` annonçant une branche vide montre que l'écart n'est pas théorique.
>
> Deux détails du contrat portent tout le relevé. Les trois réponses de SNMPv2 qui ne
> sont pas des valeurs (`NoSuchObject`, `NoSuchInstance`, `EndOfMibView`) sont rendues
> **telles quelles** : c'est exactement ce qu'on cherchait à savoir. Et le drapeau
> `truncated` est ce qui rend un « zéro ligne » **interprétable** : sans lui, une réponse
> coupée au plafond passerait pour une table complète, ou l'inverse. Énumérer un arbre
> entreprise entier dépasse d'ailleurs les 5000 lignes d'une seule question — ça se fait
> **par branches**, et c'est `truncated` qui dit quand découper.

> [!warning] Une conclusion NÉGATIVE est un livrable, à condition d'être déclarée
> Ce relevé a la même valeur qu'une capability positive : il **ferme** une question qui,
> sinon, se rouvre à chaque exploitant qui cherche ses courbes optiques et à chaque
> développeur tenté d'écrire un collecteur « au cas où ». Mais il ne vaut que déclaré :
> tant que ce savoir restait dans un compte rendu d'essai, l'écran affichait un blanc, et
> un blanc se lit comme « meshcraft ne le fait pas encore ». Le coût de la confusion
> était concret — attendre une version qui ne viendra jamais.
>
> Ce qui **n'est pas** établi pour autant, et qu'on ne prétend pas : que la gamme
> n'expose ces capteurs par **aucune** voie. Le relevé porte sur **SNMP**, standard et
> privé, et rien ici ne dit ce que l'interface web de l'équipement affiche ou non. Le
> motif déclaré parle donc de ce qui a été mesuré — et si un chemin web apparaissait, il
> vaudrait révision de la déclaration, pas contradiction du relevé.

## Le modèle VLAN des deux plateformes MikroTik

Le même châssis 24 ports + 2 SFP+ existe dans les deux mondes de MikroTik, et les
deux mondes n'ont **rien en commun** sur les VLAN. C'est le seul cas du parc où le
choix du connecteur se joue sur le système qui a démarré et non sur le matériel
([[Discovery]] : le titre de la page web tranche, le modèle ne dit rien).

Matériels de référence, relevés : **CSS326-24G-2S+ / SwOS 2.17** et
**CRS326-24G-2S+ / RouterOS 7.21beta3**.

### Ce que chaque plateforme porte réellement

| | SwOS (CSS326) | RouterOS (CRS326) |
|---|---|---|
| Appartenance | table `/vlan.b` : une ligne par VLAN, un **masque de ports membres** | `/interface/bridge/vlan` : une entrée par VLAN (ou par **plage**), listes `tagged` et `untagged` |
| VLAN natif | `/fwd.b` `dvid`, un par port | `/interface/bridge/port` `pvid`, **indépendant** de l'appartenance |
| Étiquetage | **déduit** : une trame sort non taguée si son VLAN est le natif du port | **explicite** : le port est dans `tagged` ou dans `untagged` |
| Interrupteur global | aucun — c'est le **mode par port** (`/fwd.b` `vlan`) | `/interface/bridge` `vlan-filtering`, tout ou rien |
| Le CPU | implicite : `/sys.b` `avln` dit **de quel VLAN** l'administration est acceptée | **un membre comme un autre** : le pseudo-port `bridge` doit figurer dans `tagged` (ou `untagged`) du VLAN de management |
| Filtrage d'entrée | `/fwd.b` `vlni` (any / only tagged / only untagged) | `/interface/bridge/port` `ingress-filtering` + `frame-types` |
| Recours si on se coupe | **aucun** : ni SSH, ni Telnet, ni série. Un déplacement | console série **et** MAC-winbox, les deux relevés |

Deux asymétries décident de tout le reste :

- sur **SwOS**, appartenance et étiquetage sont la **même donnée**. Retirer un port
  de la liste des membres coupe ce VLAN sur ce port, sans demi-mesure ;
- sur **RouterOS**, `pvid` et appartenance sont deux réglages **dans deux menus
  différents**. L'équipement de référence porte l'anomalie à l'état pur : ses 24
  ports cuivre ont `pvid=100` alors que le VLAN 100 a une liste `untagged` **vide**.
  Ils étiquettent leur trafic entrant dans un VLAN dont rien ne ressort, et le
  firmware ne dit rien.

### Le nom des ports : celui de l'équipement, avec l'ancien encore accepté

Les deux connecteurs MikroTik nomment désormais les prises **comme leur firmware les
nomme**, et le connecteur SwOS a rejoint le RouterOS sur ce point.

| | Cuivre | Cages | D'où ça vient |
|---|---|---|---|
| **SwOS** (CSS326) | `Port1` … `Port24` | `SFP1`, `SFP2` | champ `nm` de `/link.b`, **relevé** sur la capture |
| **RouterOS** (CRS326) | `ether1` … `ether24` | `sfp-sfpplus1`, `sfp-sfpplus2` | les noms d'interface, depuis la première livraison |

Le connecteur SwOS écrivait auparavant `GE1` … `GE26` — la convention de la gamme
Instant On, empruntée pour qu'un parc mêlé se compare, avec une numérotation
**continue** cages comprises. Le prix était payé à chaque diagnostic : la prise que
meshcraft appelait `GE25` s'appelle `SFP1` dans l'interface du switch, et le
rapprochement était laissé à faire de tête sur la seule donnée qui décide **où une
écriture atterrit**.

Deux compteurs INDÉPENDANTS, donc, et c'est le point à retenir : `Port25` n'existe
pas sur un 24 ports + 2 cages, et ce nom est **refusé** plutôt que traduit.

> [!warning] Les DEUX familles sont relues, et le contraire aurait cassé un parc en silence
> `Facade.Index` (`swos/naming.go`) accepte aussi l'ancienne numérotation : `GE25`
> désigne toujours la première cage. Refuser ces noms aurait fait ignorer **en silence**
> les intentions déjà enregistrées — `PlanPorts` range un nom inconnu dans ses `Skipped`,
> donc une configuration de port aurait cessé d'être posée sans qu'aucune erreur ne le
> dise. Sur les deux équipements de référence `devices.ports` est vide et rien n'aurait
> été perdu ; la base d'un parc en service, elle, ne l'est pas.
>
> Et la traduction ne s'arrête pas à l'index. La décision de pose cherche dans l'état
> **lu** le VLAN natif actuel du port et **s'il porte un lien** — et l'état lu est nommé
> par la façade. Chercher avec le nom du plan rendrait le port inconnu : natif 0, **aucun
> lien**, ce qui est très exactement ce qui **lève** les garde-fous. Une intention héritée
> aurait ainsi désarmé la protection du port qui porte la session, sur la seule plateforme
> du parc qui n'a ni SSH, ni Telnet, ni port série. D'où le **nom canonique** dans
> `PlanPorts`, et un test qui échoue si on le retire.

**`nm` n'est PAS devenu une clef**, et c'est une décision distincte de la précédente :
c'est du texte libre, renommable port par port depuis l'interface du switch, sans
contrainte d'unicité, et rien ne suit un renommage. Trois défauts d'un coup si on en
faisait la clef — deux ports nommés pareil s'écraseraient dans les cartes de `State`,
`port_groups` ne sait pas exprimer une liste de noms quelconques (or c'est elle que
l'interface web déplie pour énumérer les prises), et une intention cesserait de désigner
un port au premier renommage. Ce qui est retenu est donc la **convention** du firmware,
pas les chaînes. Les chaînes restent servies là où elles ne décident de rien : le champ
`device_name` de la télémétrie, et une **note d'état** qui signale les ports dont
l'étiquette locale diffère du nom de façade — sans quoi l'écart entre les deux écrans
reviendrait, à l'envers et sans explication.

> [!note] Un équipement SwOS adopté AVANT ce changement garde sa façade en `GE`
> `port_groups` n'est écrit qu'à la découverte : sa mise à jour demande une
> ré-identification de l'équipement. Tant qu'elle n'est pas faite, l'écran énumère
> `GE1` … `GE26` pendant que la lecture d'état rend `Port1` … `SFP2`, et l'état relu ne
> se rattache à aucune prise de l'écran. Ce n'est pas une perte de configuration — les
> deux MikroTik de référence ont `devices.ports` **vide** — mais c'est un écran vide, et
> il faut savoir pourquoi.

### La configuration peut être présente et INERTE

C'est le fait le plus coûteux de ces deux plateformes, et il n'entre dans **aucun**
champ de `driver.PortState` : une configuration VLAN peut être écrite, relue,
comparée, trouvée conforme — et sans effet sur une seule trame.

| Cas | Plateforme | Ce que l'écran montrerait sans la note |
|---|---|---|
| mode VLAN `disabled` sur un port | SwOS | un VLAN natif et une appartenance qui n'agissent pas : le port ignore la table |
| « Force VLAN ID » (`fvid`) sur un port | SwOS | un **trunk** — alors que le port range TOUT son trafic entrant dans son natif, étiquettes comprises |
| `vlni` = only tagged sur un port d'accès | SwOS | un port d'accès configuré, qui jette tout son trafic |
| `vlan-filtering` inactif sur le bridge | RouterOS | une segmentation en place, sur un bridge qui laisse tout passer partout |
| `ingress-filtering: false` (le **défaut**) | RouterOS | une segmentation étanche, alors qu'elle ne l'est qu'à la **sortie** |
| `frame-types: admit-all` sur un port d'accès | RouterOS | un port d'accès, alors qu'un poste qui étiquette lui-même son trafic en sort |
| `hw-offload` retombé à faux sur une partie des ports | RouterOS | un débit de chip, sur des ports qui passent par le CPU |

Ces phrases sortent par `driver.State.Notes`, sont enregistrées dans
`last_apply.notes`, et sont **affichées** par `ConnectorNotesBanner`
([[Rendu des ports]]). Aucune n'appelle de bouton : ni activer `vlan-filtering`,
ni resserrer un filtre qu'un garde-fou a refusé de toucher n'est un geste que
meshcraft prend à la place d'un humain qui regarde.

### Ce que la pose écrit, et ce qu'elle refuse

**SwOS** — `swos/apply.go`, `swos/ports.go`

| Écrit | Refusé, et pourquoi |
|---|---|
| l'appartenance **gagnée** (masque de `/vlan.b`) | — |
| l'appartenance **perdue** | jamais un VLAN de management **sur un port qui porte un lien** (voir le callout) ; jamais le natif effectif ; rien sur un port en « all » (c'est un **plancher**) ; rien sur un port **actif** quand `avln` vaut 0 |
| le VLAN natif (`dvid`) | un port dont le natif est gelé par le garde-fou est gelé **en entier** — un port épargné à moitié se retrouve avec un natif d'un côté et une appartenance de l'autre, et ne fait circuler ni l'un ni l'autre |
| le mode VLAN, **`disabled` → `enabled`** seulement | jamais `optional` → `enabled` (cela **resserre** un port qui marche) ; jamais `strict` (filtre d'entrée) ; jamais d'abaissement |
| — | `vlni` et `fvid` ne sont **jamais** écrits : les resserrer jette du trafic, et le modèle standard n'exprime pas ce qu'il faudrait pour décider |

> [!bug] « Le bénéfice d'un tel retrait est nul » était faux, et ça coûtait TOUTE la convergence — relevé du 29 juillet 2026
> Le refus de retirer un VLAN de management portait sur **tous** les ports, sans regarder
> le lien, au motif qu'« un port sans lien aujourd'hui est peut-être l'uplink de demain,
> et le bénéfice d'un tel retrait est nul ». Les deux moitiés de la phrase étaient
> fausses.
>
> **Mesuré sur le CSS326 du parc** : son VLAN 99 était membre des **vingt-six** ports.
> Une pose demandant « les 24 ports cuivre en accès VLAN 100 » écrivait donc **zéro
> port** et refusait vingt-quatre fois, à chaque passage, pour toujours :
>
> ```
> 21:58  port.configured  24 ports → accès VLAN 100
> 21:58  config.applied   0 port(s) écrit(s), 24 épargné(s), 48 écart(s) restants
> ```
>
> L'exploitant lisait « les VLAN ne s'appliquent pas », et c'était le bon diagnostic :
> l'intention était **inatteignable par construction**, et la fiche affichait un écart
> que rien ne pouvait fermer.
>
> **Le bénéfice n'est pas nul, il est de sécurité.** Sur SwOS, appartenance et étiquetage
> sont la **même donnée** : laisser le VLAN d'administration membre d'un port d'accès,
> c'est laisser n'importe quel poste branché là atteindre le plan de management en
> étiquetant lui-même son trafic. C'est exactement le trou que « accès VLAN 100 » existe
> pour fermer.
>
> **Le critère est donc le LIEN**, comme pour les deux autres garde-fous du même fichier
> et pour le réveil du filtrage : une session ne traverse pas une prise vide. Un port
> **actif** qui n'est pas celui de la session garde son appartenance — c'est le résidu
> conservateur, et il est assumé.
>
> **Vérifié sur le matériel après correction** : 24 ports écrits, **0 refus**, VLAN 99
> passé de 26 membres à 2 (les deux cages, qui sont des trunks « all »), équipement
> toujours joignable, `sync = in_sync`, **0 écart**. C'est aussi la première pose de
> VLAN de ce connecteur **exercée sur du matériel** — l'encadré « NON VÉRIFIÉ SUR
> MATÉRIEL » de `swos/apply.go` ne vaut plus pour ce chemin.

**RouterOS** — `routeros/apply.go`, `routeros/ports.go`

| Écrit | Refusé, et pourquoi |
|---|---|
| les entrées `/interface/bridge/vlan` manquantes | une entrée **dynamique** n'est jamais modifiée : une statique est créée à côté |
| les listes `tagged` / `untagged` | une ligne à **plusieurs `vlan-ids`** : ses listes sont communes à tous ses VLAN, y écrire l'appartenance d'un seul réécrirait celle des autres |
| le `pvid` | jamais sur un port **actif** dont le PVID est un VLAN de management |
| `ingress-filtering=yes` | jamais sur un port **actif** qui n'est pas membre d'un VLAN de management |
| `frame-types=admit-only-untagged-and-priority-tagged`, ports d'**accès** seulement | jamais sur un port actif où un VLAN de management arrive **tagué** |
| — | `admit-only-vlan-tagged` n'est **jamais** écrit : un trunk meshcraft a toujours un VLAN natif ([[VLAN par défaut]] §2), donc du trafic non tagué que cette valeur jetterait |
| — | `vlan-filtering` n'est **jamais** activé : d'un coup, tout ce qui n'est pas déclaré tombe, l'administration comprise |

### L'ordre des opérations, et pourquoi il n'est pas négociable

Deux règles, les mêmes des deux côtés, et chacune évite une coupure :

1. **DÉCLARER AVANT DE FILTRER.** La table des VLAN d'abord, le resserrement
   ensuite, dans deux requêtes séparées. L'ordre inverse fait tomber le trafic d'un
   port pendant l'intervalle — et définitivement si la seconde requête échoue.
2. **AJOUTER AVANT DE RETIRER.** Un port qui passe du VLAN 50 au VLAN 60 est membre
   des deux à l'instant du calcul, jamais d'aucun.

Et un invariant : **le VLAN de management n'est jamais orphelin**. Il entre en tête
de la liste des VLAN à déclarer, qu'il soit dans le plan du site ou seulement
déclaré par l'équipement.

> [!warning] Sur SwOS, créer proprement la ligne du VLAN de management est ce qui coupe
> Créer une ligne dans `/vlan.b` fait **passer** ce VLAN du régime « inconnu de la
> table » au régime « déclaré, donc filtré ». Or sur un port en mode `optional` — le
> défaut d'usine, et l'état des 26 ports de l'équipement de référence — un VLAN que
> la table ne déclare **pas** circule librement. Le déclarer avec un masque de
> membres **vide** le fait donc tomber partout, instantanément.
>
> Autrement dit : la façon la plus rapide de rendre un CSS muet est de mettre de
> l'ordre dans sa table de VLAN. La pose sème donc la ligne créée avec **tous les
> ports qui portent un lien**, et le dit dans `Skipped` — faute de savoir lequel
> porte la session, un port actif de trop est un excès de portée qu'on corrige, un
> port actif de moins est un déplacement.

### Ce qui reste non faisable, par plateforme

| | SwOS | RouterOS |
|---|---|---|
| Trunk qui **refuse** le non tagué | pas d'intention : `vlni` n'est pas écrit | **inexprimable** dans le modèle standard — un trunk y a toujours un natif |
| Port d'accès qui refuse le tagué | pas d'intention : `vlni` n'est pas écrit | **écrit**, sous garde-fou |
| Filtrage d'entrée strict | `strict` non écrit (jette des trames à l'entrée) | **écrit** (`ingress-filtering`), sous garde-fou |
| Ajouter un port au bridge | sans objet — tous les ports sont dans la table | **non fait** : un port hors bridge est signalé, pas ajouté |
| Agrégation (LACP / bonding) | `/lacp.b` lu pour les capabilities, **jamais écrit** | `/interface/bonding` relevé vide, **jamais écrit** |
| STP / RSTP | `/rstp.b` lu **et écrit** (`ena`, `fc`, `cost`, `edge`) via `ApplySpanningTree` ; mode site via préférence `stp_mode_preference` (MSTP/RSTP-PVST/STP/none, ou **vide** : voir ci-dessous) | `edge`, `path-cost`, `bpdu-guard` relevés, **jamais écrits** |
| Vitesse, duplex, auto-négociation, MTU, contrôle de flux | `/link.b` porte `an`, `dpxc`, `fctc`, `fctr` — **jamais écrits**, et aucune intention du modèle standard ne les demande | `/interface/ethernet` porte `advertise`, `l2mtu` (plafond `max-l2mtu: 10218` relevé) — **jamais écrits** |
| Isolation de ports | `/fwd.b` `fp1…fp26`, matrice complète, **jamais écrite** | `horizon` par port, **jamais écrit** |
| Activation / désactivation d'un port | `/link.b` `en` — **écrit** (`ApplyPortAdmin`, page entière) | `disabled` sur `/interface` — **écrit** (`ApplyPortAdmin`, `type: ether` uniquement) |

Cette dernière ligne a longtemps dit « jamais écrit » des deux côtés, et le manque
était en amont : le [[Modèle de données standard]] n'avait pas d'intention pour
éteindre un port. Elle en a une depuis le 2026-08-05, et **les deux connecteurs
l'écrivent**, chacun prouvé sur le matériel du labo le 2026-08-10 (SwOS `Port2`,
RouterOS `ether2`, via `POST /api/devices/{id}/ports/admin` puis re-lecture, second
apply à vide, et retour arrière confirmé).

Les deux gestes ne se ressemblent pas, et c'est le SwOS qu'il faut lire deux fois :

- **RouterOS** écrit UN champ (`disabled`) sur UN enregistrement, dont l'`.id` est
  relu juste avant l'écriture — ces numéros ne survivent pas à un redémarrage, et un
  identifiant périmé n'échoue pas, il désactive *une autre interface*. Seul
  `type: ether` est accepté : `bridge`, `lo` et `vlan99` acceptent parfaitement
  `disabled: true` et coupent respectivement toute la commutation, la boucle locale
  et l'adresse d'administration ;
- **SwOS** n'a pas d'écriture par port. L'état administratif est un BIT dans un
  MASQUE (`en`) d'une page que l'équipement rend et attend en ENTIER : écrire un port
  veut dire reposter `nm` (les vingt-six noms de ports), `an`, `dpxc`, `fctc`,
  `fctr`, `spdc` avec lui. D'où le refus plutôt que la troncature quand un champ
  manque — poster sans `nm` renommerait vingt-six ports en rien. Et la persistance
  est immédiate : ni `running`/`startup`, ni redémarrage qui défasse le geste. La
  seule marche arrière est le `GET /backup.swb` pris juste avant l'écriture, ou le
  bouton de reset en façade, sur site.

#### `stp_mode_preference` vide : l'interrupteur d'arrêt par site

La liste des modes préférés d'un site a **trois** états, pas deux, et le troisième
est le seul qui ne se lise pas sur l'écran :

- **absente** — le site n'a jamais ouvert l'écran STP : `driver.DefaultSTPPreference`
  s'applique, l'apply pousse un mode ;
- **remplie** — le premier mode que la plateforme annonce est poussé ;
- **explicitement vide** — l'exploitant l'a vidée : `applyDeviceSTP` sort
  immédiatement et **n'envoie rien**. C'est le cas prévu pour un Catalyst laissé
  volontairement en (rapid-)PVST+ pendant une migration, sur un connecteur qui n'a
  pas de chemin « lu, identique, rien à faire » : avant cet interrupteur, n'importe
  quel changement de port renvoyait le mode et reconvergeait l'arbre.

> [!warning] L'interrupteur est plus large que son nom
> Il ne suspend pas seulement le mode. `SpanningTreePlan` transporte le mode, la
> région MST **et** les intentions par port — portfast/edge, BPDU guard, coût de
> chemin — et tout passe par le même `pushDeviceSTP`. Vider la liste pour protéger
> le mode global d'un seul équipement arrête donc aussi l'application du BPDU guard
> sur tous les ports d'accès du site.
>
> Et un site qui utilise MST cesse d'être **ré-homogénéisé** : le condensé de région
> couvre toute la liste de VLAN du site (`siteMSTRegion`), donc quand cette liste
> bouge, les équipements portant déjà une région meshcraft gardent l'ANCIENNE. En
> 802.1s, une région qui ne correspond pas est traitée comme un voisin RSTP
> ordinaire : l'éviction est **silencieuse**, les ports commutent toujours,
> l'équipement reste vert. C'est exactement la panne 802.1s invisible que la
> comparaison de région existe pour attraper.
>
> L'exploitant n'est pas désarmé pour autant : `handleApplyDeviceSTP` prend un mode
> explicite et ne consulte jamais la préférence, donc un push par équipement depuis
> le panneau STP fonctionne toujours. Ce qui s'arrête, c'est la RIDE automatique.

### Le troisième transport de RouterOS : l'API binaire

Le connecteur RouterOS a désormais **deux** transports, et ce n'est pas de la redondance
— c'est une **absence** de la REST qui l'a imposé. Tout est développé dans
[[Télémétrie MikroTik]] ; ce qui appartient à cette page est la carte des ports, parce que
trois protocoles différents répondent sur le même équipement, avec le même compte :

| Port | Protocole | Ce que ce connecteur en fait |
|---|---|---|
| 80 | **REST** — HTTP, JSON, Basic rejoué à chaque requête | tout : identification, lecture d'état, pose, SNMP, traps |
| **8728 / 8729** | **API binaire** — phrases, mots préfixés par leur longueur | la télémétrie que la REST ne sait pas rendre |
| 8291 | **Winbox** — protocole *encore différent*, chiffré, non documenté | **rien**, et ce n'est pas prévu |

Les trois capabilities étaient déjà relevées et déclarées (`/ip/service` : `www` sur 80,
`api` sur 8728, `api-ssl` sur 8729, `winbox` sur 8291, aucun désactivé). Ce qui a changé
est que `BinaryAPI` ne dit plus « que ce connecteur n'utilise pas ».

**Ce que l'API binaire apporte, et pourquoi il en fallait un troisième transport.** Deux
choses seulement, et les deux viennent de `/interface/ethernet/monitor`, qui est une
**commande** et non un menu :

- **le débit NÉGOCIÉ d'un lien.** Constaté sur les captures : ni les 87 champs de
  `/interface/ethernet` ni ceux de `/interface` ne portent de `speed`. Il n'y a
  qu'`advertise`, qui dit ce que le port *propose*. Un port tombé à 100 Mbit/s faute d'une
  paire coupée est donc **invisible par la REST** — tout fonctionne, dix fois moins vite ;
- **le diagnostic optique d'une cage SFP+.** Ce point-ci n'est pas une découverte : le
  connecteur le disait déjà, et en tirait la seule conclusion honnête possible à l'époque
  — « elle n'a pas pu être lue, `monitor` est un POST, hors de portée d'un outil restreint
  à GET, elle n'a donc ni canal ni entrée d'indisponibilité ». C'est cette phrase-là que le
  nouveau transport a rendue caduque.

> [!warning] Aucun octet de ce protocole n'a été échangé avec du matériel
> Le seul chemin vers le CRS326 depuis l'hôte de développement est un outil HTTP restreint
> à GET/HEAD : il ne sait pas ouvrir une socket TCP nue sur 8728. Ce qui **est** relevé,
> c'est que le service écoute et que les deux ports acceptent la connexion sans parler
> HTTP. Le protocole est implémenté d'après la spécification publiée par le constructeur et
> éprouvé contre un **équipement simulé**.
>
> Ce que le simulateur attrape, et c'est ce qui compte : le **cadrage**. Un mot mal encodé
> ne produit pas une erreur de syntaxe, il décale le flux, et la session meurt plusieurs
> commandes plus tard sans rapport apparent avec la cause — le mode d'échec le plus coûteux
> à diagnostiquer sur du matériel, et précisément celui qu'un simulateur attrape sans
> matériel. Ce qu'il ne prouve **pas** : que ce firmware réponde comme la spécification le
> décrit. Le canal déclaré porte donc un plancher plus haut que celui de la REST et le dit
> dans sa note ; seule une campagne sur matériel refermera l'écart, et c'est le même fil
> rouge que pour tout le reste — *lire, écrire, relire et comparer, puis déclarer la
> capability*.

Une limite structurelle mérite d'être connue avant d'y compter : **TLS sur 8729 écoute mais
n'est probablement pas utilisable** sur un RouterOS sans certificat installé. RouterOS y
sert historiquement une suite Diffie-Hellman **anonyme**, absente de la bibliothèque TLS de
Go — par décision de conception de celle-ci, puisqu'une suite anonyme n'authentifie aucune
des deux parties. Le repli est l'API en clair à travers le tunnel du proxy, ce qui est
exactement le régime de confiance de la REST sur cette plateforme.

## Les connecteurs comme greffons : binaires indépendants et contrat

Un connecteur par plateforme, écrit par des gens différents, à des rythmes
différents : la seule organisation qui tienne à l'échelle est le **greffon**.
Décision : **des binaires indépendants**, hors du processus contrôleur, reliés
par un contrat versionné.

Ce que ça achète : un constructeur ou un intégrateur peut écrire son connecteur
sans toucher au cœur ni attendre une release de meshcraft ; un connecteur fautif
ne fait pas tomber le contrôleur ; il peut être écrit dans un autre langage ; et
il se distribue, se signe et se met à jour séparément.

### Le point dur : un connecteur n'a pas accès au réseau

C'est la décision structurante, et elle est contre-intuitive. **Un connecteur
n'ouvre jamais de connexion lui-même.** Il demande à la plateforme, qui la lui
rend sous forme de flux.

La raison est architecturale : l'équipement n'est presque jamais joignable depuis
la machine du contrôleur, il l'est depuis un [[Proxy]] — le connecteur devrait
donc parler yamux, WebSocket et gérer les tunnels. C'est le contraire du but
recherché. En passant par la plateforme :

- le connecteur reste ignorant du chemin — règle « un seul chemin de code »
  ([[Vue d'ensemble]]) ;
- l'**allowlist locale du proxy** et la journalisation restent du côté de la
  plateforme, donc hors de portée d'un greffon tiers ;
- le même connecteur marche en direct, à travers un proxy, ou contre un
  enregistrement rejoué en test.

### Forme du contrat

| Sujet | Choix |
|---|---|
| Transport | **gRPC sur socket local**, greffon lancé par le contrôleur, façon `go-plugin`. Typé, bidirectionnel, streaming natif, multi-langage. |
| Poignée de main | le greffon annonce `protocol` (entier), son `Info` et ses `Capabilities` ; un protocole trop ancien est **refusé au démarrage**, jamais dégradé en silence. |
| Sens plateforme → greffon | `Fingerprint`, `Capabilities`, `ReadState`, `Plan`, `Apply`, `BackupConfig`, `ImportConfig`, `ResetProcedure`. Le voisinage LLDP n'y figure pas : collecté par le poller SNMP du proxy (`internal/snmppoll/lldp.go`), cf. callout. |
| Sens greffon → plateforme | `Dial(host, port)` → flux TCP ; `Log` ; `Progress`. Rien d'autre : pas de disque, pas de base, pas de réseau direct. |
| Secrets | passés **par opération**, jamais le coffre ([[Credentials]]). Un greffon voit l'identifiant dont il a besoin, à l'instant où il en a besoin. |
| Isolation | processus séparé, utilisateur dédié, pas d'accès au système de fichiers du contrôleur. Un greffon reste du **code tiers exécuté à côté de données sensibles** : le dire, et le cloisonner. |
| Distribution | binaire signé + manifeste (plateformes couvertes, version de contrat, empreinte). Vérification avant exécution, comme pour le binaire du proxy ([[Versionnage et mise à jour du proxy]]). |

### Chemin depuis l'existant

`internal/driver` définit déjà le contrat en Go (`Info`, `Capabilities`,
`ResetProcedure`, `Fingerprint`) et `internal/driver/instanton` l'implémente
en-processus. Ce n'est pas du travail perdu : **c'est le contrat qui se
stabilise**. Le passage au greffon consiste à projeter ces mêmes méthodes en
service gRPC et à faire du connecteur intégré le premier client de ce service —
en gardant la possibilité de charger un connecteur en-processus pour ceux que
nous maintenons, et hors-processus pour les autres.

Séquencer ainsi n'est pas une facilité : figer un contrat d'IPC avant d'avoir
écrit deux ou trois connecteurs réels, c'est figer les hypothèses du premier. Le
1930 en a déjà démenti plusieurs — deux API sur le même équipement, un commit en
deux temps, une identification lisible sans authentification.

Deux connecteurs de plus l'ont confirmé, et `internal/driver/connector.go` est né
de là : la couche API était écrite **directement contre les types du connecteur
1930**, si bien que l'adoption d'un MikroTik a fait partir une pose SNMP en
protocole Instant On vers lui. Trois gênes du nouveau contrat sont connues et non
réparées — les noter vaut mieux que les redécouvrir au troisième connecteur.

> [!warning] Trois limites connues du contrat de connecteur
> - **Les capacités facultatives sont portées par la session, pas par le
>   connecteur.** L'intention écrite était qu'une assertion de type se fasse
>   *avant* que le geste ne commence ; or `TrapPusher`, `MgmtMover`, `Persister`,
>   `AccountManager`, `SNMPConfigurator` et `RawReader` s'assertent sur l'objet
>   rendu par `Authenticate` — donc **après** avoir parlé à l'équipement, et après
>   avoir dépensé une des cinq sessions d'un Instant On. Contournement en place
>   pour les traps, dont l'absence se lit d'abord dans la capability
>   `Monitoring.Traps` ; pour la bascule de management, il n'existe **aucun** moyen
>   de refuser avant contact. La réparation serait un `Supports(gesture)` sur le
>   connecteur, ou ces interfaces dédoublées côté connecteur.
> - **`ApplyResult.Skipped` mélange deux natures.** Un port épargné par un
>   garde-fou est **forçable** ; un refus d'écriture de plateforme ne l'est pas —
>   sur RouterOS, une ligne `/interface/bridge/vlan` qui déclare plusieurs
>   `vlan-ids` ne verra jamais son appartenance réécrite, parce que ses listes de
>   membres sont communes à tous ses VLAN et que meshcraft ne sait pas composer une
>   appartenance commune à onze. L'écran présente les deux pareil et propose un
>   « forcer » qui ne changera rien pour le second. Il manque un marqueur par entrée.
>
>   *(L'exemple d'origine était SwOS et son « ajout seulement ». Il n'est plus
>   valable : ce connecteur RETIRE désormais les appartenances que le plan ne
>   demande plus, avec quatre refus nommés — voir §« Le modèle VLAN des deux
>   plateformes MikroTik » ci-dessous.)*
> - **`Identify` ne dit pas s'il a besoin d'identifiants.** L'Instant On livre son
>   identité à un anonyme, SwOS livre son modèle par le realm de son challenge, et
>   RouterOS ne rend **rien** sans authentification. L'appelant le découvre en
>   essayant, ce qui coûte une sonde générique par adresse vivante — et c'est
>   précisément ce qui a compliqué la relocalisation ([[Discovery]]).
