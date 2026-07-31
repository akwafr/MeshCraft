---
tags: [architecture, securite, orchestration]
---

# Étiquettes et portée des identifiants

Le §12 de [[Identité de service et rotation]] a tranché : le secret est **par
site**, avec autant d'identifiants **par groupe d'équipements** que nécessaire.
La raison de cette nuance n'était pas le confort, c'était une contrainte
matérielle — *« les politiques de mot de passe diffèrent d'un modèle à l'autre
(longueur maximale, caractères interdits) »*.

Cette note traite ce que le groupe ne sait pas faire, et donne à meshcraft de
quoi ranger un parc sans le ranger à la main.

## 1. Pourquoi le groupe ne suffit pas

Un équipement n'a **qu'un seul** groupe (`devices.group_id`). Or les raisons de
donner un mot de passe particulier à un équipement ne s'excluent pas :

- une **gamme** qui plafonne les mots de passe à 16 caractères ;
- un **constructeur** dont l'interface refuse la ponctuation ;
- un **lieu** confié à un prestataire, à qui l'on ne veut pas confier le secret
  du reste du site ;
- une **plage d'adresses** correspondant à un réseau de régie isolé.

Le groupe sert déjà à autre chose : il porte le profil d'équipement
([[Profils et groupes]]), c'est-à-dire la configuration. Détourner un axe de
configuration pour exprimer une contrainte de mot de passe force à choisir entre
les deux, et le jour où un switch appartient au « lieu Scène » *et* à la gamme
« 8 ports », il n'y a pas de bonne réponse.

> [!important] Une étiquette n'est pas un groupe
> Le **groupe** dit *comment l'équipement est configuré* : un seul, il porte le
> profil. L'**étiquette** dit *à quels ensembles il appartient* : autant qu'on
> veut, elles ne portent aucune configuration. Confondre les deux, c'est
> reproduire le problème qu'on cherche à résoudre.

## 2. Le modèle

Livré par la migration `0013` :

```sql
-- Étiquettes posées sur un équipement. Plusieurs par équipement.
device_tags(device_id fk, tag text,
            source text,     -- manual | rule:<uuid> | snmp | pwpolicy  (cf. §2.1)
            created_at, primary key(device_id, tag))

-- Règles d'étiquetage automatique, par site.
tag_rules(id pk, site_id fk, tag text, priority int, enabled bool,
          match jsonb,     -- vendor | model | location | ip_prefix, en ET
          note text, created_at, updated_by fk users,
          unique (site_id, tag, match))

-- Et sur site_credentials : un identifiant peut viser une ÉTIQUETTE au lieu
-- d'un groupe. L'unicité historique (site_id, group_name, kind) est élargie,
-- sinon un site ne pourrait pas avoir deux identifiants d'étiquette distincts.
site_credentials(… , tag text default '', priority int default 0)
unique index site_credentials_scope_key (site_id, group_name, tag, kind)
```

> [!warning] Deux colonnes `priority`, et une seule sert
> C'est le piège de lecture de ce modèle. La `priority` qui départage les
> identifiants du §3 est celle de **`site_credentials`** — c'est elle que
> `CredentialsForDevice` trie. Celle de `tag_rules` n'est lue par personne :
> `tagging.Rule` n'a même pas le champ, et l'ordre des règles d'étiquetage
> n'importe pas puisqu'une règle **pose** une étiquette sans en retirer d'autre —
> il n'y a donc rien à arbitrer entre elles. La colonne reste, sans effet ; s'y
> fier pour prioriser un étiquetage ne produirait rien de visible, ce qui est la
> pire forme d'erreur.

### 2.1 `source` : ce qui distingue le posé du déduit — et le technique

Une étiquette posée à la main et une étiquette déduite d'une règle ne se
traitent pas pareil. Rejouer les règles doit pouvoir **retirer** ce qu'une règle
avait posé et qui ne s'applique plus, sans jamais toucher à ce qu'un humain a
décidé. Sans la colonne `source`, la seule réévaluation sûre serait de ne rien
retirer — et le parc accumulerait des étiquettes périmées que personne n'oserait
nettoyer.

Une **troisième** source est apparue à l'implémentation, et elle n'était pas
prévue ici : `snmp`, posée par le provisionnement d'identité SNMP
([[Identité SNMP]], `internal/api/snmpidentity.go`). Ni `manual`, ni `rule:` :

- la marquer `manual` **mentirait** sur son origine et l'exposerait à l'édition à
  la main, alors qu'aucun exploitant n'a décidé quoi que ce soit ;
- la marquer `rule:` la rendrait retirable par un rejeu des règles — c'est-à-dire
  qu'un `tag-rules/apply` **débrancherait la supervision du parc en passant**,
  puisque l'identifiant v3 vise cette étiquette et ne serait plus posé sur rien.

Une source à part est donc exactement ce qui garantit qu'un rejeu ne casse rien :
`tagging.Evaluate` ne retire que les sources préfixées `rule:`, et ce n'est pas un
détail d'implémentation mais la propriété qui rend le rejeu sûr.

Une **quatrième** source suit, pour le même motif exactement : `pwpolicy`, posée
par le provisionnement du **mot de passe d'administration**
(`internal/api/pwpolicy_creds.go`). Le raisonnement se recopie mot pour mot en
remplaçant « supervision » par « accès », ce qui le rend plus grave encore : si un
rejeu des règles retirait cette étiquette, l'équipement retomberait sur
l'identifiant du site, dont son propre connecteur refuse le secret **avant
envoi**. Un `tag-rules/apply` fermerait alors la seule porte d'une gamme entière,
et rien à l'écran ne relierait les deux gestes.

> [!important] La règle générale, maintenant qu'il y a deux cas
> Un mécanisme qui pose des étiquettes pour **se retrouver lui-même** doit avoir
> sa propre source. Ni `manual` (mentir sur l'origine, et s'exposer à l'édition à
> la main), ni `rule:` (s'exposer à un retrait par rejeu). La question à se poser
> devant le troisième cas : *si cette étiquette disparaissait, est-ce que quelque
> chose cesserait de fonctionner sans le dire ?* Si oui, source dédiée, et un test
> qui verrouille que `tagging.Evaluate` n'y touche pas.

## 3. Choisir l'identifiant d'un équipement : du plus précis au plus général

Un équipement peut correspondre à plusieurs identifiants. L'ordre est **total et
déterministe**, parce que l'indéterminisme se paierait cher : deux exécutions
qui choisissent des identifiants différents poseraient deux mots de passe
différents sur le même équipement.

1. identifiant visant une **étiquette** que porte l'équipement ;
2. identifiant visant son **groupe** ;
3. identifiant **par défaut du site** (`tag` et `group_name` vides).

L'ordre est écrit **une seule fois**, dans `store.CredentialsForDevice` :

```sql
order by specificity desc,   -- 3 = étiquette, 2 = groupe, 1 = défaut du site
         priority desc,      -- site_credentials.priority
         tag,                -- alphabétique
         id                  -- dernier recours : rien ne reste jamais indécis
```

Entre plusieurs identifiants d'étiquette, la `priority` la plus haute gagne ; à
égalité, le nom d'étiquette dans l'ordre alphabétique ; à égalité encore,
l'identifiant. Les deux derniers critères ne sont pas des choix de goût : ils
n'existent que pour qu'il n'y ait **jamais** de cas non tranché. Deux identifiants
de même étiquette et même priorité sont possibles — rien ne l'interdit, seul le
quadruplet `(site, groupe, étiquette, genre)` est unique — et sans `id` en dernier
recours PostgreSQL rendrait un ordre arbitraire, donc **variable d'une exécution à
l'autre**. C'est ce cas-là que l'`id` ferme.

La fonction rend **toute** la liste, pas seulement le gagnant. C'est ce qui permet
à l'appelant de signaler l'ambiguïté au lieu de la masquer.

> [!warning] Une ambiguïté se signale, elle ne se devine pas
> Quand un équipement correspond à plusieurs identifiants d'étiquette de même
> priorité, meshcraft en choisit un — il le faut bien — **et le dit** dans l'UI
> et dans le [[Journal d'événements]]. Un départage silencieux entre deux
> intentions contradictoires de l'exploitant est un bogue en attente.

L'étiquette l'emporte sur le groupe parce qu'elle est le seul axe qu'on puisse
poser *en plus* : si l'exploitant a pris la peine de créer un identifiant pour
une étiquette, c'est qu'il visait une exception.

### 3.1 Tout genre d'identifiant ne se découpe pas par étiquette

Cette note supposait la portée par étiquette **universelle** : un axe de
rangement, indifférent à ce qu'on range. L'identité SNMP a démontré le contraire,
et `checkCredScope` (`internal/api/sitecreds_handlers.go`) en tire les
conséquences genre par genre.

| Genre | Portée admise | Pourquoi |
|---|---|---|
| `admin` | site, groupe **ou** étiquette, au choix — **sauf** l'espace de noms `pwpolicy-`, réservé | c'est le cas d'origine de cette note : les politiques de mot de passe diffèrent d'un modèle à l'autre, et l'exploitation choisit son axe. Rien à contraindre, et rien à changer pour l'existant. L'exception est née d'un défaut constaté sur matériel (§3.2) : quand la politique d'une plateforme **exclut** le secret du site, ce n'est plus un choix de rangement, c'est une contrainte — et meshcraft la tient lui-même |
| `snmp_v2c` | **site seulement** — étiquette *et* groupe refusés | non parce qu'une communauté serait indivisible, mais parce que le plan de provisionnement v2c **ne porte aucune étiquette** : une communauté rangée sous une étiquette ou un groupe **ne serait jamais retrouvée**. C'est le raisonnement décisif de tout ce garde-fou — un secret rangé dans une portée que le provisionnement ne consulte pas *existe sans jamais servir*, et l'écran le montrerait. Pire qu'un secret absent : on croirait la supervision configurée |
| `snmp_v3` | **étiquette obligatoire**, et elle est **imposée** : `snmpv3-<auth>-<priv>` | une clé v3 **dérive du couple d'algorithmes**. Deux équipements qui n'acceptent pas les mêmes ne *peuvent pas* porter la même identité — ce n'est pas une préférence, c'est de la cryptographie. Un v3 « de site » serait un secret que la moitié du parc refuserait en silence |

La liste est **fermée** : un genre inventé est refusé, avec les trois valeurs
admises dans le message. Un genre libre produirait un identifiant que rien ne
consomme, silencieusement inerte — le même mode d'échec que la portée mal choisie,
par un autre chemin.

La conséquence est structurante : le préfixe `snmpv3-` est un **espace de noms
réservé**, et `snmpv3.ParseTag` en est l'unique juge. Une étiquette
d'exploitation ne doit jamais être interprétée comme une consigne de chiffrement,
et un identifiant v3 ne peut pas viser une étiquette libre — il serait posé sur un
ensemble d'équipements dont rien ne garantit qu'ils partagent des algorithmes.

Un détail de forme qui compte : l'étiquette enregistrée est **la sortie de
`Choice.Tag()`**, jamais la chaîne saisie. Une variante d'écriture — casse,
`aes-128` contre `aes128` — fabriquerait un **second** identifiant pour le même
couple d'algorithmes, et le parc se retrouverait coupé en deux sans qu'aucun
écran ne le dise. Le couple sans chiffrement est nommé explicitement `-nopriv`
pour la même raison : deux étiquettes qui se ressemblent doivent se distinguer
d'un coup d'œil, sinon on croit avoir chiffré alors qu'on n'a fait
qu'authentifier.

### 3.2 La deuxième population technique : `pwpolicy-`

Le §3.1 tenait le cas v3 pour une exception née de la cryptographie. Un défaut
constaté sur du matériel a montré que le mécanisme était **général**, et il est
livré :

```
POST /api/devices/<css326>/credentials/apply
→ ok:false — « mot de passe refusé avant envoi : nouveau mot de passe :
             17 caractères, l'équipement en accepte 15 au plus »
```

Le connecteur SwOS refusait **avant d'émettre un octet**, ce qui est exactement son
travail. La cause était en amont : `driver.AdminAccount.PasswordMax` était déclaré
par les trois connecteurs et **lu par personne**. Résultat : *aucun* mot de passe
n'était posable sur un MikroTik CSS326 dès lors que le secret du site dépassait 15
caractères. La capability existait précisément pour empêcher ce cas ; il manquait
quelqu'un pour la consulter au moment d'engendrer.

**Ce qui n'a pas été fait, et pourquoi.** Borner globalement le générateur au plus
petit plafond rencontré ferait payer à tout un parc la contrainte d'un seul modèle :
un site avec un CSS326 et quarante-huit ports d'Instant On verrait ses
quarante-neuf secrets tomber à 15 caractères. C'est le cas d'usage du §1, mot pour
mot — *« une gamme qui plafonne les mots de passe à 16 caractères »* —, donc c'est
au mécanisme d'étiquettes de le traiter.

**Ce qui est livré** (`internal/pwpolicy`, `internal/api/pwpolicy_creds.go`) : au
moment de poser un mot de passe, si le secret réellement visé ne tient pas dans la
politique de la plateforme, meshcraft **emploie** un identifiant compatible déjà
présent dans le site, ou en **engendre un** visant l'étiquette technique, étiquette
l'équipement (source `pwpolicy`) et poursuit la pose. Idempotent : rejouer ne crée
pas un second identifiant.

> [!note] L'essai à blanc CRÉE l'identifiant technique — délibéré, à arbitrer
> L'adaptation a lieu **avant** la branche `dry_run`, et c'est voulu : un essai qui
> ne suit pas le même chemin que la pose ne vérifie pas la pose. C'est ainsi qu'un
> `dry_run` annonce l'étiquette retenue et la raison de l'adaptation, ce qui est
> précisément ce qu'on attend de lui.
>
> La contrepartie est qu'un essai à blanc **a un effet en base** : il peut créer un
> identifiant et poser une étiquette, avec les deux événements de journal qui vont
> avec. Rien ne part vers l'équipement — la promesse tenue par `dry_run` est
> respectée là où elle est risquée — mais « à blanc » ne veut pas dire « sans
> trace ». À trancher : soit le nom du geste change, soit l'adaptation devient un
> geste distinct que l'essai à blanc se contente de **prévoir**.

| Genre | Portée | Pourquoi cette forme |
|---|---|---|
| `admin` sous `pwpolicy-len<N>` | **étiquette imposée**, engendrée par meshcraft | `N` est la longueur des secrets que meshcraft engendre sous cette politique. L'identifiant est créé **seulement** si aucun secret compatible n'existe déjà dans le site — au sens des **bornes**, pas du nom : un `pwpolicy-len12` sert parfaitement un équipement qui plafonne à 15 |

**Pourquoi la POLITIQUE et non le MODÈLE.** L'étiquette pourrait nommer la gamme
(`…-css326`). Elle nomme la contrainte, et c'est le raisonnement du v3 : la clé v3
dérive du **couple d'algorithmes** et non du modèle, parce que *ce qui doit partager
un secret est ce qui partage la contrainte*. Une étiquette par modèle donnerait
autant de secrets que de références au catalogue, là où deux gammes aux mêmes bornes
partagent parfaitement le leur — et multiplier les secrets, c'est multiplier ce
qu'il faut faire tourner, révéler et surveiller.

Deux conséquences de forme, chacune délibérée :

- **la longueur seule**, et non les deux bornes (`pwpolicy-min0-max15`). Ce qui
  décide de la compatibilité d'un secret, c'est sa taille : deux politiques
  `0..15` et `15..15` acceptent exactement le même secret de 15 caractères, et
  leur donner deux noms fabriquerait deux secrets pour un seul besoin. `len15` est
  la **classe d'équivalence exacte** ;
- **`AlphanumericOnly` n'entre pas dans le nom**, alors qu'il entre bien dans le
  jugement d'un secret existant. `internal/pwgen` n'engendre **que** de
  l'alphanumérique : tout secret sorti de meshcraft satisfait déjà cette
  contrainte, et la nommer scinderait le parc entre deux étiquettes dont les
  secrets seraient interchangeables. Elle sert en revanche à juger un secret
  **saisi à la main**, qui peut porter de la ponctuation — et c'est ce jugement-là
  qui déclenche l'engendrement. Le jour où `pwgen.Alphabet` gagnerait de la
  ponctuation, cette déduction tomberait : un test verrouille son contenu.

> [!warning] Le refus à la saisie est le « irrévocable » de la conception
> L'espace de noms `pwpolicy-` est interdit dans les **trois** gestes
> d'exploitation : création, modification, suppression. Le juge est unique
> (`pwpolicy.IsTechnicalTag`) et le message aussi ; le chemin, non — les deux
> premiers gestes passent par `checkCredScope`, la suppression interroge le juge
> directement. La différence a sa raison : la liste de genres de `checkCredScope`
> est **fermée**, et l'y faire passer rendrait un identifiant de genre *hérité*
> définitivement insupprimable. Un garde-fou qui empêche de nettoyer une vieille
> ligne n'est plus un garde-fou.
>
> Le refus porte sur tout le
> préfixe et pas seulement sur les formes canoniques — `pwpolicy-len015` a l'air de
> l'étiquette officielle et ne serait posée sur aucun équipement. La **rotation**,
> elle, reste offerte : le secret d'une gamme entière doit pouvoir tourner. Elle est
> seulement **bornée** à la longueur que l'étiquette nomme, sans quoi un mot de
> passe de 20 caractères rangé sous `pwpolicy-len15` reproduirait le défaut d'origine
> — à ceci près que meshcraft l'aurait rangé lui-même dans le coffre.

> [!note] Ce que ça change dans la lecture de cette note
> Il y a désormais **deux populations d'étiquettes** sur un même équipement, et
> elles ne se traitent pas pareil : celles d'**exploitation** (posées à la main ou
> par règle, arbitraires, retirables) et celles **techniques** (posées par
> meshcraft, sources réservées, intouchables par un rejeu). Cette seconde
> population compte maintenant **deux familles**, et elles décident de choses
> différentes :
>
> | Famille | Source | Ce qu'elle décide | Si elle disparaît |
> |---|---|---|---|
> | `snmpv3-<auth>-<priv>` | `snmp` | l'identité SNMPv3 partagée par une famille cryptographique | la supervision se débranche, en silence |
> | `pwpolicy-len<N>` | `pwpolicy` | le mot de passe d'administration d'une gamme dont la politique exclut le secret du site | l'équipement retombe sur un secret que son connecteur refuse : plus aucun mot de passe posable |
>
> L'UI les distingue (**livré**) : jeton marqué d'un cadenas, sans croix de
> retrait, avec l'infobulle qui dit lequel des deux mécanismes l'a posée ; et sur
> l'écran des identifiants, une portée réservée n'offre ni « Modifier » ni
> « Supprimer ». Présenter une étiquette technique comme un rangement librement
> modifiable invite à la supprimer — et selon la famille, ça débranche la
> supervision ou ça ferme la porte.

## 4. Étiquetage automatique

Ranger deux cents équipements à la main ne se fait pas, donc ne se fait pas du
tout. Les règles observent ce que la découverte a **déjà** constaté — aucune
sonde supplémentaire :

| Critère | Comparé à | Forme |
|---|---|---|
| `vendor` | `devices.vendor` | sous-chaîne, insensible à la casse |
| `model` | `devices.model` | sous-chaîne, insensible à la casse |
| `location` | `devices.location` | sous-chaîne, insensible à la casse |
| `ip_prefix` | `devices.mgmt_ip` | préfixe CIDR (IPv4 ou IPv6) |

Les critères renseignés d'une règle sont combinés en **ET**. Une règle sans
aucun critère ne s'applique à rien : c'est une erreur de saisie, pas une règle
qui prend tout le parc. Le défaut sûr est de ne rien faire.

La sous-chaîne plutôt que l'expression régulière : les valeurs comparées sont
des libellés constructeur (`HPE Networking Instant On Switch 48p … 1930 JL685A`),
qu'un exploitant reconnaît d'un coup d'œil mais ne sait pas transformer en
regex. Une regex fausse ne dit pas qu'elle est fausse — elle ne matche rien.

Un préfixe CIDR **illisible** ne se rabat pas sur « pas de filtre » : il ne matche
rien. La différence est tout le sujet — se rabattre transformerait une faute de
frappe en règle qui prend tout le parc, donc qui lui réattribue silencieusement un
mot de passe. Même logique pour un équipement sans adresse de management connue :
il n'est pas *dans* la plage, il est *sans adresse*, et le compter dedans lui
attribuerait un secret sur la foi d'une information absente. `Match.Problems()`
dit ces cas en clair, parce qu'une règle qui ne matche jamais et ne dit pas
pourquoi est le pire des réglages : elle a l'air posée.

### 4.1 Quand les règles tournent

- à l'**adoption** d'un équipement et à chaque fois que son identité change
  (modèle, constructeur, adresse, lieu) : c'est là que l'information arrive.
  **Livré** — `autoTagDevice`, appelé depuis l'adoption d'un candidat et depuis la
  mise à jour d'un équipement. **Silencieux en cas d'échec**, et c'est délibéré :
  ne pas étiqueter est un désagrément réparé au passage suivant, faire échouer une
  adoption pour ça serait une faute ;
- à la **demande**, sur tout le site, après avoir modifié une règle
  (`POST /api/sites/{id}/tag-rules/apply`, `site_admin`). L'aperçu est le
  **défaut** : sans `apply: true`, rien n'est écrit. Ce n'est pas un confort —
  c'est ce qui rend l'opération réversible dans la tête de l'exploitant avant de
  l'être dans la base. Seuls les équipements réellement concernés sont rendus :
  une liste de deux cents lignes dont trois comptent ne se lit pas.

La réévaluation est **idempotente** et ne touche que les étiquettes de source
`rule:*`. Une règle désactivée retire ses étiquettes ; elle ne les abandonne pas
sur place. Une même étiquette posée **à la main *et* par une règle** garde la
source manuelle et survit donc à la désactivation de la règle : la décision
humaine gagne.

### 4.2 Ce que l'étiquetage automatique ne fait PAS

Il ne change **jamais** de mot de passe tout seul. Une règle qui déplacerait un
équipement d'un identifiant à un autre modifie le secret *attendu*, pas le
secret *posé* : l'équipement reste sur ce qu'il porte jusqu'à un
`credentials/apply` explicite, et la différence est visible dans la vue de parc
(§10 de [[Identité de service et rotation]]).

C'est la même règle que partout ailleurs dans meshcraft : la base apprend de la
réalité, elle ne la décrète pas. Un étiquetage qui provoquerait des écritures en
cascade sur le parc transformerait une erreur de saisie en incident.

Il existe malgré tout un rattrapage, et il fallait le prévoir : réétiqueter un
équipement le fait basculer d'un identifiant à un autre, et le nouveau n'a **jamais
été posé sur lui**. Sans filet, meshcraft essaierait un secret qu'il n'a jamais
écrit et ignorerait celui qu'il a écrit lui-même — sur une gamme sans port série,
ça se répare par un retour aux réglages d'usine. `store.PreviousInstalls` rend
donc, en plus, ce que l'équipement est **connu porter** au titre d'autres
identifiants du même site. C'est la règle *ne jamais se couper la main* appliquée
à la portée : **changer d'avis sur le rangement d'un équipement ne doit jamais
coûter son accès.**

## 5. Traçabilité

Tous ces événements sont **émis** aujourd'hui, au [[Journal d'événements]] :

| Événement | Émis par |
|---|---|
| `tag.assigned` | pose manuelle, **et** pose d'une étiquette technique par l'un des deux provisionnements — avec, dans ce cas, la phrase qui dit *pourquoi* : c'est elle qui décide du secret v3 que l'équipement partagera, ou du mot de passe adapté à sa politique |
| `credential.generated` | l'engendrement d'un identifiant que **personne n'a demandé** : identité SNMP (§3.1) ou mot de passe de politique (§3.2). Le message dit ce qui l'a rendu nécessaire — pour le second, le refus que la plateforme aurait opposé au secret du site, longueur comprise et **jamais** le secret |
| `tag.removed` | retrait manuel |
| `tag.rule_changed` | création, modification, suppression d'une règle |
| `tag.rules_applied` | rejeu sur un site, avec le décompte **et** le rappel qu'aucun mot de passe n'a changé sur le parc |
| `credential.scope_ambiguous` | le cas du §3 : plusieurs identifiants de même précision |

Le rejeu ne journalise **pas** équipement par équipement, et c'est cohérent avec
le reste du vault : un rejeu sur deux cents machines produirait deux cents lignes
pour un seul geste. Le décompte agrégé plus l'aperçu préalable disent la même
chose sans noyer le journal.

### Décidé / livré / ouvert

| Question | État |
|---|---|
| Étiquettes multiples par équipement, distinctes du groupe | **livré** — `device_tags` (`0013`) |
| Portée d'identifiant par étiquette, résolue en un seul endroit | **livré** — `store.CredentialsForDevice` |
| Ordre total et déterministe de la résolution | **livré** — `specificity`, `priority`, `tag`, `id` |
| Règles d'étiquetage automatique en ET, sous-chaîne et CIDR | **livré** — `internal/tagging`, pur et testé en table |
| Rejeu à l'adoption et au changement d'identité | **livré** — `autoTagDevice` |
| Rejeu sur site, avec aperçu par défaut | **livré** — `POST /api/sites/{id}/tag-rules/apply` |
| Source `snmp` et espace de noms réservé `snmpv3-` | **livré** — non prévu par cette note, imposé par [[Identité SNMP]] |
| Source `pwpolicy` et espace de noms réservé `pwpolicy-len<N>` | **livré** — non prévu par cette note non plus, imposé par un refus constaté sur un CSS326 (§3.2) |
| Lecture de `driver.AdminAccount.PasswordMin/Max/AlphanumericOnly` | **livré** — `internal/pwpolicy`, unique traducteur de la capability en contrainte ; `pwgen.Length` / `pwgen.NewBounded` engendrent sous bornes, et **refusent** une contrainte insatisfiable au lieu de tronquer |
| Engendrement automatique d'un identifiant adapté, idempotent | **livré** — `ensureAdminSecretFits`, appelé au seul endroit qui écrit un mot de passe |
| Refus des portées incohérentes selon le genre | **livré** — `checkCredScope` (§3.1), désormais consulté aussi à la **suppression** |
| Rattrapage des secrets déjà posés sous une autre portée | **livré** — `store.PreviousInstalls`. C'est lui qui rend indolore la bascule d'un équipement du secret de site vers son secret d'étiquette technique : le nouveau n'a jamais été posé sur lui |
| `tag_rules.priority` | **inerte** — colonne présente, jamais lue (§2) |
| Distinction visuelle étiquette d'exploitation / étiquette technique | **livré** — cadenas, pas de croix de retrait, infobulle par famille ; et sur l'écran des identifiants, une portée réservée n'offre ni modification ni suppression |
| Reprise des identifiants visant un **groupe** vers une étiquette | **décidé, non construit** — étape 2 de [[Profils de switchs]] ; elle déplace des mots de passe, donc aperçu d'abord |
| **Forme du nom : `pwpolicy-len<N>` ou `<gamme>`** | **ouvert** — l'exploitation avait proposé un nom par modèle (`wksecret-css326`) ; l'implémentation a retenu la classe d'équivalence par longueur, pour la raison du §3.2. Les deux se tiennent : le nom par modèle se lit sans rien savoir du mécanisme, celui par politique évite de multiplier les secrets. Un seul endroit décide (`pwpolicy.Policy.Tag()`), le changement est donc local — mais il **renommerait des étiquettes déjà posées**, ce qui n'est pas un renommage cosmétique : c'est un déplacement de portée d'identifiant |
| **`AlphanumericOnly` trop grossier, composition câblée** | **ouvert** — voir le callout de [[Connecteurs]] § *Capabilities* : la contrainte réelle de SwOS est *ASCII seul*, déclarée en prose et appliquée dans le connecteur ; et « majuscule + minuscule + chiffre » est câblé dans `pwgen` pour tout le parc alors que c'est une politique de plateforme |
