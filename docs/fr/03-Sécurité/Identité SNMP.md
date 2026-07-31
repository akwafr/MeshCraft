---
tags: [securite, monitoring, orchestration]
---

# Identité SNMP

Prolonge [[Identité de service et rotation]] au canal de supervision, et
s'appuie sur [[Étiquettes et portée des identifiants]] pour le partage des
secrets. Rien n'est réinventé ici : mêmes générations, même coffre, même écran
*Configuration → Identifiants*. Ce qui change, c'est **ce qui décide du
périmètre d'un secret**.

## Où en est cette page

| Question | État |
|---|---|
| Genres `snmp_v2c` (portée site) et `snmp_v3` (portée étiquette) au coffre et à l'écran | **livré** — `checkCredScope`, `internal/api/sitecreds_handlers.go` ; `web/src/pages/SiteCredentials.tsx` |
| Négociation du couple d'algorithmes contre les capabilities, plancher, drapeau non conforme | **livré** — `internal/snmpv3`, `internal/snmpprov` (paquet **pur** : il ne pose rien) |
| Pose de l'étiquette technique et engendrement des secrets manquants | **livré** — `ensureSNMPIdentity`, `internal/api/snmpidentity.go` ; branché sur la création d'équipement et sur l'adoption |
| Écriture sur l'équipement, en delta, avec relecture de vérification | **livré pour v2c** — `internal/driver/instanton/snmp.go` (`ApplySNMP` + `verifySNMP`) |
| Écriture d'un utilisateur v3 sur l'équipement | **codée et testée, jamais exercée** — aucun connecteur ne déclare v3 (§3.1) |
| **Collecte d'un équipement en v3** | **non construite** — voir le callout ci-dessous. C'est l'écart le plus grave de la page |
| Rotation d'un secret SNMP par générations | **livré** au sens de [[Identité de service et rotation]] §13 (créer une génération), **non construit** au sens du déploiement automatique |

> [!warning] Tout le mécanisme v3 s'arrête au bord du collecteur
> Le v3 est décidé, négocié, engendré, et son écriture sur l'équipement est
> écrite et testée. Mais **le collecteur du proxy ne parle que v2c**, et le
> récepteur de traps non plus. `snmppoll.conventions` force
> `Version: gosnmp.Version2c` quel que soit ce que la cible demande — un test
> verrouille même ce comportement (`Target{Version: "3"}` → `Version2c`) — et
> `classifyTrap` rejette structurellement un trap v3, puisqu'il compare une
> communauté que les paquets v3 n'ont pas.
>
> Conséquence, en une phrase : **un équipement passé en v3 aujourd'hui ne serait
> ni interrogé, ni écouté**. Pas dégradé : muet.
>
> Ce qui empêche la panne n'est donc pas le collecteur, ce sont **quatre verrous
> posés en amont**, et il faut les connaître parce que ce sont eux qu'on verra
> refuser :
>
> - `snmpWatchesFor` (`internal/api/watch_handlers.go`) **n'envoie pas** au proxy
>   les équipements dont le plan n'est pas v2c : un équipement en v3 est
>   silencieusement absent des cibles ;
> - `trapdest` **refuse** d'écrire une destination de traps sur un équipement en
>   v3 (verdict `trapDestV3Only`), et le message dit les deux moitiés du verrou —
>   pas de réception *et* pas d'interrogation ;
> - le walk de diagnostic à la demande le refuse aussi (`askSNMPv3Only`,
>   `internal/api/ask_handlers.go`) ;
> - et surtout, **aucun connecteur ne déclare v3** (§3.1), donc `snmpprov` retombe
>   en v2c sur tout le parc réellement supporté.
>
> Le chemin v3 est ainsi *écrit, testé unitairement, et mort en production*. Ce
> n'est pas un gâchis : l'ordre choisi — décider avant d'écrire, écrire avant de
> collecter — a permis de valider la résolution sur du matériel sans jamais poser
> un secret que le collecteur aurait ensuite ignoré. Mais le premier connecteur
> qui déclarera `SNMPv3{auth, priv}` **rendra ce trou immédiat**, et il ne se
> verra pas : l'équipement aura son utilisateur v3, son étiquette et son secret,
> l'écran dira que la supervision est configurée, et aucune métrique n'arrivera.
> Le prérequis de tout connecteur v3 est donc `Version3` + `SecurityParameters`
> dans `snmppoll`, et l'USM dans le récepteur de traps — pas l'inverse.

## 1. Pourquoi SNMP ne peut pas avoir un seul secret de site

Un mot de passe d'administration est du texte : le même vaut pour tout le parc.
Une identité SNMPv3 n'est pas du texte, c'est une **clé dérivée** d'un mot de
passe, d'un algorithme d'authentification et d'un algorithme de chiffrement. Deux
équipements qui n'acceptent pas les mêmes algorithmes ne peuvent pas porter la
même identité, même avec le même mot de passe.

D'où la règle centrale :

> [!important] Le périmètre d'un secret v3 est le COUPLE d'algorithmes
> Un identifiant SNMPv3 vise une étiquette `snmpv3-<auth>-<priv>`, jamais un
> site entier. Un site à parc homogène n'aura qu'une étiquette ; un parc mixte en
> aura autant que de familles cryptographiques, et c'est la réalité du parc qui
> le décide, pas l'exploitant.

Corollaire, volontairement simple : **pas d'étiquette v3 = SNMPv2c**. Le défaut
est le plus faible, parce qu'un équipement qu'on n'a pas su qualifier ne doit pas
recevoir une configuration qu'il refuserait en silence.

## 2. Trois genres d'identifiants — *livré*

`site_credentials.kind` porte déjà cette distinction ; il suffit de l'utiliser.
C'est fait, et la liste des genres est **fermée** côté API : tout autre valeur
est refusée en 400.

| `kind` | Portée | Contenu | Contrainte appliquée |
|---|---|---|---|
| `admin` | site, groupe ou étiquette | le mot de passe d'administration (existant) | portée libre |
| `snmp_v2c` | **site** | la communauté, en LECTURE seule | étiquette et groupe **refusés**, `username` forcé à vide |
| `snmp_v3` | **étiquette** `snmpv3-…` | utilisateur, mot de passe d'authentification, mot de passe de chiffrement | étiquette **obligatoire** et validée par `snmpv3.ParseTag`, puis **réécrite sous forme canonique** |

`snmp_v2c` est de portée site parce qu'une communauté est du texte : rien
n'empêche de la partager. `snmp_v3` est toujours par étiquette, pour la raison
du §1.

Réécrire l'étiquette au lieu de la refuser sur un détail de casse ou de tiret est
délibéré : `snmpv3-SHA-256-AES128` et `snmpv3-sha256-aes128` désignent la même
famille cryptographique, et laisser coexister deux orthographes créerait deux
secrets là où le parc n'en attend qu'un — donc la moitié du parc supervisée avec
une identité que l'autre moitié ne porte pas.

> [!note] Un identifiant v3 porte DEUX phrases, obligatoirement différentes
> Détail livré que la conception n'avait pas écrit, et qui n'en est pas un : la
> RFC 3414 dérive deux clés distinctes, une pour l'authentification, une pour le
> chiffrement. Le coffre a donc un second champ scellé (`secret2_enc`, migration
> `0017`), réservé au genre `snmp_v3`, et l'API **exige la paire** et **refuse
> deux phrases identiques**. Réutiliser la même phrase pour les deux clés ne
> casse rien de visible — c'est exactement pour ça qu'il faut le refuser à la
> saisie plutôt que l'espérer.

Les générations, la révélation tracée et la rotation sont celles de
[[Identité de service et rotation]] — aucun chemin parallèle. La révélation d'un
identifiant v3 émet **un seul** événement pour la paire : deux lignes pour un
seul geste feraient croire à deux accès.

> [!note] La communauté v2c ne sert QU'À LIRE
> meshcraft ne déploie jamais de communauté en écriture. La configuration passe
> par le connecteur ; une communauté RW sur un parc, c'est un mot de passe root
> en clair sur le réseau.

## 3. Ce que le provisionnement décide — *livré*

À l'adoption, et à chaque fois que l'identité d'un équipement change :

```
capabilities du connecteur  →  Negotiate(auth[], priv[], plancher)
                                     │
              ┌──────────────────────┴───────────────────┐
        v3 exploitable                            pas de v3
              │                                         │
   étiquette snmpv3-<auth>-<priv>              identifiant snmp_v2c
   posée si absente                            du site (créé si absent)
              │
   identifiant snmp_v3 de cette étiquette,
   engendré s'il n'existe pas
```

La séparation des rôles est nette dans le code, et c'est elle qui rend la chose
relisible : `internal/snmpprov` **décide** — c'est un paquet pur, sans base, sans
réseau, qui ne pose rien — et `internal/api/snmpidentity.go` **agit**. On peut
donc demander le plan sans rien changer : `GET /api/devices/{id}/snmp-plan`,
niveau `operator`, lecture seule. C'était l'étape 2 de l'ordre de livraison du
§5, et c'est ce qui a permis de confronter la résolution au parc réel avant
d'écrire une seule ligne dessus.

Quatre points qui font la différence entre un automatisme utile et un
automatisme dangereux — tous les quatre livrés :

1. **Le plus fort que l'équipement sait porter**, pas le plus fort du plancher.
   `Negotiate` prend le maximum de chaque liste déclarée. Exiger ce qu'il ne sait
   pas faire ne le durcit pas, ça le prive de supervision. Le plancher par défaut
   est `sha256` / `aes128`, et *pas d'authentification du tout = pas de v3* :
   `noAuthNoPriv` est refusé, parce qu'un v3 sans authentification est un v2c plus
   compliqué.
2. **L'écart au plancher est enregistré, pas masqué.** Une plateforme qui
   n'offre que MD5/SHA-1 et DES reçoit `snmpv3-sha-des` et le drapeau *non
   conforme* — et la note est écrite **dans le coffre**, pas seulement calculée à
   la volée, pour qu'elle se lise là où le secret se lit. Le déployer est un
   choix ; le présenter comme sûr serait un mensonge.
3. **L'étiquette est TECHNIQUE.** Elle est posée par le provisionnement avec la
   source `snmp`, porte le préfixe réservé `snmpv3-`, et `ParseTag` refuse toute
   autre forme. La conséquence concrète, vérifiée : le moteur d'étiquetage par
   règles ne retire que les étiquettes de source `rule:`, donc une règle
   d'exploitation ne peut pas emporter l'étiquette technique en passant — ce qui
   aurait détaché l'équipement de son identifiant v3 sans que personne ne touche
   à SNMP.
4. **Rien n'est engendré deux fois.** L'aller-retour nom → couple → nom est
   stable, et `snmpprov.For` est une fonction pure dont l'idempotence est
   verrouillée par un test. Le geste d'engendrement, lui, part de la liste de ce
   qui **manque** (`Plan.Missing`) et sort immédiatement si elle est vide.
   Détail qui décide de tout : la recherche de l'identifiant existant se fait sur
   les identifiants **du site**, pas sur ceux résolus pour l'équipement. Chercher
   « ce que cet équipement voit » aurait fait engendrer un second secret v3 au
   deuxième équipement de la même famille cryptographique — et cassé la promesse
   du §1, où l'étiquette est justement ce qui **partage** un secret.

Le provisionnement est déclenché à la création manuelle d'un équipement, à
l'adoption depuis la découverte, et à une mise à jour d'équipement **seulement si
ses entrées ont changé** (modèle, constructeur, IP de management, groupe). La
garde n'est pas cosmétique : le provisionnement complet touche l'équipement et
coûte des secondes, contre des millisecondes pour un renommage. Un écran qui
enregistre un libellé ne doit pas déclencher une session SNMP.

Un échec de provisionnement **n'échoue jamais l'adoption** : il est journalisé et
rangé dans le pipeline `monitoring`. Refuser d'adopter un équipement parce que
son SNMP n'a pas pris laisserait l'équipement dehors, c'est-à-dire invisible —
alors qu'adopté sans SNMP il est au moins pilotable, et son manque est affiché.

### 3.1 Une exception constatée : la gamme Instant On 1930

Son guide décrit un SNMPv3 (MD5/SHA-1, DES) et son firmware en embarque le code —
les sections `SNMPUserISList` et `SNMPGroupList` existent et répondent. Mais la
page qui l'exposerait est **masquée** sur ce modèle (`hidden:_top.isDeviceTypek9`
dans son bundle) : l'équipement n'offre réellement que le v2c de « Get
Connected ».

Le connecteur déclare donc `SNMPVersions: []string{"v1", "v2c"}` et rien d'autre.
Annoncer v3 conduirait le provisionnement à engendrer une étiquette et un secret
que la pose refuserait ensuite — et **un identifiant qui existe en base et nulle
part ailleurs est pire qu'un identifiant absent**, parce que l'écran le montre et
laisse croire la supervision configurée.

> [!note] C'est la gamme qui est l'exception… mais c'est aujourd'hui la règle du parc
> Tout le mécanisme v3 — négociation, étiquettes par couple d'algorithmes,
> secrets par étiquette — reste entier et sert au reste du parc. Une plateforme
> ne déclare que ce qu'elle OFFRE, jamais ce que sa documentation décrit.
>
> À constater cependant, pour ne pas se tromper sur l'état : **aucun connecteur
> ne renseigne aujourd'hui `SNMPv3Auth` / `SNMPv3Priv`**. Les deux champs
> existent dans le contrat de [[Connecteurs]] et ne sont lus que par `snmpprov`.
> Tout le parc supporté retombe donc en v2c — un test le dit d'ailleurs
> explicitement pour le 1930. Le v3 n'est pas « rarement utilisé » : il n'est pas
> utilisé.

## 4. Écrire sur l'équipement — *livré, en v2c*

Dernière étape, et la seule qui touche le parc. Sur la gamme Instant On, relevé
sur un équipement réel :

- `SNMPGlobalSetting` porte `SNMPEnbl` — **à 2, c'est-à-dire désactivé** sur un
  équipement sorti d'usine. Rien ne se collectera tant qu'il n'est pas activé ;
- la section des communautés v2c s'appelle **`CommunityList`** (et non
  `SNMPCommunityList`, qui répond `-1`) ;
- `SNMPUserISList` porte les utilisateurs v3, avec `authenticationMethod` et
  `privacyMethod` en codes numériques.

Table des codes, relevée dans le bundle de l'équipement (`pages2.js` et son
dictionnaire) et non déduite — recopiée à l'identique dans le connecteur, avec sa
provenance :

| Champ | Codes |
|---|---|
| `SNMPEnbl` | **1 = activé**, 2 = désactivé |
| `authenticationMethod` | **0** = aucune, 1 = MD5, 2 = SHA |
| `privacyMethod` | **1** = aucune, 2 = DES |
| `accessMode` (v2c) | 1 = lecture, 2 = écriture, 3 = super |

> [!warning] « Aucun » n'a pas le même code selon le champ
> `0` pour l'authentification, `1` pour le chiffrement. Poser `privacyMethod=0`
> par symétrie enverrait une valeur hors énumération.

Les codes `accessMode` 2 et 3 n'apparaissent nulle part dans le connecteur : il
n'existe aucun chemin de code qui pose une communauté en écriture. Ce n'est pas
une intention, c'est **triplement garanti** : les constantes n'existent pas dans
le fichier, la construction de l'entrée code en dur la lecture seule, et la
**relecture refuse** une communauté qui reviendrait avec un autre mode.

L'ordre des sections est imposé, et il y en a **quatre**, pas trois : activer le
service, la communauté, le **groupe**, puis l'utilisateur v3. Le groupe est
dédié (`meshcraft-ro`) et posé avec un `writeView` **vide**, précisément parce
que les groupes d'usine en portent un : réutiliser un groupe existant aurait
donné à l'utilisateur v3 de meshcraft un droit d'écriture qu'aucune ligne de code
ne demandait — et la relecture le vérifie aussi.

L'écriture suit les conventions déjà établies pour ce framework
([[Réconciliation]], section *Apply*) : section, `action="set"`, delta et non
état complet, puis **relecture de vérification** — sur cette gamme, un
`statusCode 0` sans effet est un cas avéré. La relecture compare sept choses :
le service activé, la communauté présente, son mode **en lecture seule**,
l'utilisateur présent, **les deux codes d'algorithme**, le groupe présent, et
l'absence de `writeView`. Deux refus supplémentaires avant toute écriture :
`noAuthNoPriv`, et l'absence de `localEngineID` — sans engine ID local, un
utilisateur v3 est posé dans le vide.

`SNMPUserISList` n'accepte pas de `set` : un utilisateur divergent est
**supprimé puis recréé**, ce qui est la seule séquence que l'équipement honore.
Et le transcript rendu à l'opérateur ne porte que des dénombrements, jamais un
secret.

## 5. Ordre de livraison — *les quatre étapes sont faites*

C'était le plan, et il a tenu :

1. les genres `snmp_v2c` / `snmp_v3` dans le coffre et l'écran Identifiants —
   **livré** (validation de portée, seconde phrase, badges à l'écran) ;
2. la résolution au provisionnement (`snmpprov`), qui dit ce qu'il FAUDRAIT sans
   rien écrire — **livré** (`GET …/snmp-plan`, paquet pur) ;
3. l'engendrement effectif des étiquettes et des secrets manquants — **livré**
   (`ensureSNMPIdentity`, événements `tag.assigned` et `credential.generated`) ;
4. l'écriture sur l'équipement, en dernier, avec sa relecture — **livré pour
   v2c** ; le chemin v3 est écrit et testé mais aucun connecteur ne l'atteint.

Ce qui manque n'est donc pas dans cette liste, et c'est ce qui rend le §5 d'avant
trompeur une fois coché : il ne comptait que la **moitié « décider et écrire »**.
La moitié **« collecter »** n'a jamais été dans le plan, et c'est elle qui
manque — d'où le callout de tête. Le prochain jalon n'est pas une cinquième
étape d'identité, c'est `gosnmp.Version3` et l'USM dans `internal/snmppoll` et
`cmd/proxy/trap.go` ([[Monitoring]]).
