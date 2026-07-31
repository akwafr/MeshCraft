---
tags: [architecture, modele, frontend]
---

# Profils de switchs

Un **profil de switch** est un jeu de **règles** qui produit la configuration des
ports d'un équipement. Il remplace l'écran des groupes d'équipements, et il
répond à une demande formulée en trois phrases :

> tous les SFP+ en trunk ; les quatre derniers RJ45 en trunk avec PVID 100 ;
> tous les autres ports en accès PVID 100.

Ces trois phrases décrivent **n'importe quel** switch d'un parc. Aucun des objets
existants ne savait les stocker : un profil de prise
([[Profils et groupes]]) dit ce que reçoit **une** prise, jamais **lesquelles**.

## 1. Diagnostic : ce que les groupes portaient réellement

La demande initiale était de tout faire avec des [[Étiquettes et portée des identifiants|étiquettes]] et de supprimer les groupes. L'état du code dit autre
chose, et il fallait le regarder avant de démonter quoi que ce soit.

| Ce que le groupe est censé porter | Ce qu'il en est |
|---|---|
| un **profil d'équipement** (`device_groups.device_profile_id`) | **inerte**. Aucun code Go n'**interprète** `device_profiles.spec` — l'import/export `meshcraft_site_config` le transporte tel quel, en JSON opaque, et c'est tout. L'héritage décrit au §1 « Compile » de [[Réconciliation]] n'existe pas |
| le choix de l'**identifiant** (`site_credentials.group_name`) | **vivant, mais inatteignable**. Aucun écran n'affecte un équipement à un groupe : la seule voie est l'import `meshcraft_site_config` ou un appel API direct |
| un **regroupement** de parc | jamais affiché nulle part |

Et du côté des étiquettes, tout est déjà là : `site_credentials.tag` existe
depuis la migration 0013, la résolution `étiquette > groupe > défaut du site` est
écrite en un seul endroit (`store.CredentialsForDevice`), l'étiquetage
automatique par règles fonctionne, et l'API refuse déjà qu'un identifiant vise un
groupe **et** une étiquette.

> [!warning] Ce qui casserait si l'on arrachait les groupes
> `site_credentials.group_name` n'est **pas** une clé étrangère : c'est un
> rapprochement par NOM. Supprimer la table `device_groups` ne casserait donc
> rien à la compilation — et c'est précisément le danger. Les identifiants visant
> un groupe cesseraient silencieusement de s'appliquer, et les équipements
> concernés retomberaient sur l'identifiant par défaut du site. Sur une gamme
> sans port série, se tromper de mot de passe se répare par un retour aux
> réglages d'usine.

### Le chemin retenu, par étapes

1. **Livré** — l'écran des groupes disparaît, remplacé par celui des profils de
   switchs ; l'ancienne URL y redirige. Le formulaire d'identifiant présente
   désormais l'étiquette **avant** le groupe, et dit que le groupe est hérité.
   Le modèle, l'API et l'import/export des groupes restent **intacts**.
2. **Ensuite** — une reprise qui, pour chaque identifiant visant un groupe, pose
   l'étiquette correspondante sur les équipements de ce groupe et bascule
   l'identifiant sur l'étiquette. Elle doit se faire en **aperçu d'abord**, comme
   `tag-rules/apply`, parce qu'elle déplace des mots de passe.
3. **Enfin, et seulement alors** — retirer `devices.group_id`, `device_groups` et
   `site_credentials.group_name`, avec une migration qui refuse de s'appliquer
   s'il reste un identifiant visant un groupe.

L'étape 2 n'est pas dans ce lot : elle touche à la portée des secrets, et rien ne
presse tant que l'axe « groupe » ne compte **aucun** identifiant en production.

## 2. Sur quoi les règles portent : la façade

`devices.port_groups` décrit la face avant en blocs homogènes :
`{prefix, from, count, speed, poe, label}`. C'est la seule description de
l'équipement dont meshcraft dispose hors ligne, et c'est donc la seule sur
laquelle un profil puisse raisonner.

`portrules.Expand` la déplie en prises ordonnées, et **déduit** pour chacune un
`media` que la façade ne déclare pas :

| Constat | Classement |
|---|---|
| ni le préfixe ni le libellé ne contiennent « sfp » | `copper` |
| débit ≥ 10G, ou libellé contenant « SFP+ » | `sfp_plus` |
| sinon | `sfp` |

Le préfixe **et** le libellé sont interrogés parce qu'aucun des deux ne suffit :
le catalogue distingue par le préfixe (`ether` / `sfp-sfpplus`), le connecteur
Instant On nomme **tout** `GE` et ne distingue que par le libellé (« 1–48 · PoE »
contre « SFP+ »). N'en regarder qu'un ferait passer la moitié du parc pour du
cuivre — en silence, et « tous les SFP+ en trunk » ne désignerait plus rien.

## 3. Les sélecteurs

Un sélecteur travaille en **deux temps**, et c'est ce qui le rend portable.

**Un filtre**, prise par prise :

| Critère | Valeurs | Note |
|---|---|---|
| `media` | `any` / `copper` / `sfp` / `sfp_plus` | `sfp` prend **aussi** les SFP+ : c'est ce qu'on veut dire par « les ports fibre » |
| `poe` | `any` / `yes` / `no` | la **capacité** de la prise, pas l'intention posée dessus |
| `group` | sous-chaîne du libellé du groupe de façade | insensible à la casse |

**Puis une tranche**, sur l'ensemble ainsi obtenu : `all`, `first N`, `last N`,
ou `range N..M`.

> [!important] La tranche porte sur le RANG, jamais sur le numéro de port
> Les numéros ne se comparent pas d'une façade à l'autre : le catalogue
> redémarre à 1 par préfixe (`ether1…48`, `sfp-sfpplus1…4`) tandis qu'un Instant
> On numérote ses SFP dans la continuité du cuivre (`GE49…52`). Une règle écrite
> sur des numéros désignerait deux ensembles différents sur deux modèles — soit
> exactement ce qu'un profil ne doit pas faire.

Les bornes sont **rognées**, pas refusées : « les 4 derniers » sur une façade qui
n'en a que 2 en désigne 2, « les rangs 1 à 44 » sur un 24 ports en désigne 24.
Refuser serait défendable sur un formulaire ; sur un profil destiné à un parc
hétérogène, ça reviendrait à n'avoir aucun profil.

Le `group` par sous-chaîne suit la doctrine des règles d'étiquetage (§4 de
[[Étiquettes et portée des identifiants]]) : les libellés ne sont pas normalisés
d'un modèle à l'autre, et l'égalité rendrait la règle muette au deuxième modèle.
La contrepartie est dite à l'écran — viser un libellé attache la règle à une
gamme.

### Ce qui a été écarté

- **l'expression régulière** sur le nom de port : une regex fausse ne dit pas
  qu'elle est fausse, elle ne désigne rien ;
- **la sélection sur l'état observé** (lien actif, débit négocié, voisin LLDP) :
  la télémétrie n'est pas une intention. Un profil dont le résultat change selon
  qu'un câble est branché ne serait pas rejouable, et l'aperçu ne vaudrait plus
  rien ;
- **l'exception par port dans le profil** : c'est le rôle de la retouche port par
  port, qui existe déjà dans la vue switch.

## 4. Conflits : la première règle gagne

Le conflit est certain — une règle attrape-tout en croise forcément d'autres.
Trois arbitrages étaient possibles.

| Arbitrage | Verdict |
|---|---|
| **spécificité** (la règle la plus précise gagne) | écarté. Il faut un ordre total sur les combinaisons de critères, que l'exploitant ne voit pas et doit apprendre. « Les 4 derniers RJ45 PoE » est-il plus précis que « les SFP+ » ? Toute réponse est arbitraire, et un arbitrage invisible se découvre en production |
| **dernière règle** (façon CSS) | cohérent, mais oblige à écrire l'attrape-tout **en premier**, à rebours de la façon dont l'intention s'énonce |
| **première règle** (façon liste de contrôle d'accès) | **retenu** : il correspond littéralement à l'énoncé, où l'attrape-tout est écrit en dernier et doit perdre |

Deux corollaires qui comptent autant que la règle elle-même :

- **une prise reçoit la configuration d'exactement UNE règle.** Pas de fusion par
  champ entre règles : « qui a mis le PoE à faux sur cette prise ? » doit avoir
  une réponse d'un mot, et une fusion à quatre niveaux n'en a pas ;
- **les sélecteurs s'évaluent sur la façade entière**, pas sur ce que les règles
  précédentes ont laissé. « Les 4 derniers RJ45 » désigne le même ensemble quelle
  que soit la place de la règle ; seul le fait de **gagner** dépend de l'ordre.
  L'autre convention rendrait le sens d'une règle dépendant de tout ce qui la
  précède : déplacer une règle changerait en silence l'ensemble désigné par les
  suivantes.

L'écran affiche, par règle, **ce qu'elle désigne** et **ce qu'elle obtient**. Les
deux nombres diffèrent dès qu'une règle placée plus haut est passée avant : une
règle qui désigne trente prises et n'en gagne aucune n'est pas fausse, elle est
**masquée** — et c'est indiscernable si l'on n'affiche qu'un seul des deux.

## 5. Ce qu'une règle pose

Les quatre champs du [[Modèle de données standard]], et rien de plus : `mode`
(`access` / `trunk` / `libre`), `pvid`, `tagged` (liste ou `all`), `poe`. Y
ajouter des champs qu'aucun connecteur n'écrit donnerait à l'aperçu l'air
d'annoncer une configuration qui n'arriverait jamais.

Une règle peut aussi **pointer un profil de prise existant** plutôt que porter
son propre réglage. C'est la raison d'être des profils de prise, et une règle qui
recopierait leur contenu s'en écarterait à la première modification sans que rien
ne le signale. Un profil de prise supprimé ne se rattrape **pas** par un repli :
la règle est déclarée en défaut, elle ne prend aucune prise, et la pose entière
est refusée en nommant la règle fautive.

> [!warning] « all » n'est jamais développé par l'évaluation
> Les VLANs du site sont pourtant connus. Mais `all` est un **plancher**, pas une
> énumération ([[Réconciliation]], « le trunk all est un plancher, pas une
> liste ») : le développer ici en ferait un ensemble exact, ce qui autoriserait
> la pose à **retirer** d'un trunk tout VLAN que le site ne déclare pas. Sur un
> uplink, c'est une coupure. La résolution appartient à `recon.Resolve`, en un
> seul endroit.

Une inadéquation est corrigée plutôt que refusée : le PoE demandé sur une prise
qui n'en a pas est **ignoré** — un profil « bureau » avec PoE doit pouvoir servir
sur les cages SFP du même équipement — mais il est **dit**, faute de quoi le
garde-fou passerait pour un bug.

## 6. Les prises non couvertes restent intactes

Un profil ne parle pas de tout. Ce dont il ne parle pas n'est **pas** remis à
zéro : c'est la règle « absent = non géré » du [[Modèle de données standard]]
appliquée aux profils. Poser `libre` sur les prises non désignées détruirait en
silence une configuration que personne n'avait demandé de toucher, et ça ne se
rattrape pas. L'exploitant qui veut le contraire ajoute une règle attrape-tout en
dernier — et il la **voit**.

Même raison pour la fusion à l'écriture : l'objet d'un port porte aussi le
**libellé** de la prise — une annotation meshcraft que l'équipement ne connaît
pas — et le dernier relevé de télémétrie. Réécrire l'objet entier effacerait
quarante-huit descriptions de prises pour une application de profil.

## 7. Prévisualisation

L'aperçu n'est pas une option qu'on déplie : il occupe le panneau droit en
permanence. Il montre, sur un **équipement réel choisi dans le site** :

- la façade entière, colorée par VLAN comme partout ailleurs
  ([[Rendu des ports]]) — survoler une règle éteint les prises qu'elle n'a pas
  gagnées ;
- le décompte **désigné / obtenu** par règle ;
- le tableau **prise par prise** : nom, règle gagnante, configuration produite,
  avertissement éventuel ;
- le nombre de prises laissées inchangées, et ce que « all » vaut sur ce site.

Il est calculé par le **contrôleur** (`POST /api/devices/{id}/switch-profile/preview`),
jamais réimplémenté en TypeScript. C'est le même arbitrage que pour
`recon.Resolve` : deux évaluations qui décideraient chacune de leur côté
finiraient par diverger, et elles divergeraient exactement là où personne ne
regarde — sur la façade d'un modèle rare. L'aperçu accepte une spec **non
enregistrée**, faute de quoi il faudrait sauvegarder pour voir, donc sauvegarder
pour découvrir qu'on s'est trompé.

## 8. Ce que la pose fait, et ne fait pas

`POST /api/devices/{id}/switch-profile/apply` écrit dans `devices.ports`, et rien
d'autre. La pose sur le matériel suit son chemin habituel : `noteDesiredChanged`
la programme, regroupée avec les autres modifications de la même salve
(pose automatique, [[Réconciliation]] §5). Un seul événement
`switch_profile.applied` (ou `port.configured` selon le chemin) est émis pour
tout le geste.

Il n'y a **pas** de clé étrangère équipement → profil, et c'est délibéré. Un
profil se **pose** : il produit l'intention des ports, que l'exploitant reste
libre de retoucher port par port. Un lien dur ferait de toute retouche locale
une « dérive au profil » à arbitrer, alors que la retouche est le mode de travail
normal.

### Affectation douce par étiquette

Depuis la migration `0030`, un profil de switch peut porter une **étiquette
d'affectation** (`switch_profiles.tag`, unique par site lorsqu'elle est non
vide). Poser cette étiquette sur un équipement (`PUT /api/devices/{id}/tags/…`,
ou une règle d'étiquetage qui l'ajoute) **évalue et pose** le profil sur cet
équipement — même chemin que l'apply manuel, donc mêmes garde-fous connecteur
(VLAN de management sur ports up, `Force: false`).

Contreparties assumées :

- **retirer** l'étiquette ne remet **pas** les ports à zéro (absent = non géré) ;
- **modifier** le profil ne repose **pas** tout seul le parc déjà étiqueté ;
- l'étiquette peut rester posée même si la pose est refusée (règle en faute) —
  l'appartenance est enregistrée, le toast / la réponse API dit pourquoi rien
  n'a été écrit.

L'écran des profils garde un aperçu sur un équipement réel et un
« Appliquer une fois » pour tester un brouillon sans étiquette.

### Ce qu'une règle ne peut pas demander, parce que `Config` ne le porte pas

`portrules.Config` a **quatre** champs — `mode`, `pvid`, `tagged`, `poe` — et son
commentaire dit pourquoi : « y ajouter des champs qu'aucun connecteur n'écrit
donnerait à l'écran de prévisualisation l'air d'annoncer une configuration qui
n'arriverait jamais ». La règle est bonne ; sa conséquence mérite d'être écrite,
parce que ce sont des demandes banales d'exploitant :

| Ce qu'un exploitant demanderait | Pourquoi aucune règle ne le porte |
|---|---|
| **éteindre** les prises non utilisées | il n'y a pas d'intention « port désactivé » dans le [[Modèle de données standard]]. Les deux plateformes MikroTik ont le champ (`/link.b` `en`, `disabled`), aucun connecteur ne l'écrit |
| forcer une vitesse ou couper l'auto-négociation | pas d'intention. `/link.b` `an` / `dpxc` et `/interface/ethernet` `advertise` sont **lus** pour les capabilities, jamais écrits |
| des trames géantes sur les uplinks | pas d'intention. RouterOS publie son plafond (`max-l2mtu: 10218` relevé) ; SwOS n'a **aucun** réglage — sa capability `JumboFrames` est à faux et c'est un fait de plateforme |
| un trunk qui **refuse** le trafic non tagué | inexprimable : un trunk meshcraft a toujours un VLAN natif ([[VLAN par défaut]] §3) |
| l'isolation d'un groupe de prises | pas d'intention. Les deux plateformes savent le faire (`/fwd.b` `fp1…fp26`, `horizon`), aucun connecteur ne l'écrit |
| une agrégation LACP sur les deux SFP+ | pas d'intention, et pas de nom : un agrégat est une **prise de plus** sur la façade, ce que `Expand` ne sait pas produire |

Ce tableau n'est pas une liste de tâches du paquet `portrules` : l'évaluation des
règles est complète pour ce qu'elle décrit. Le manque est **en amont** — dans
`devices.ports`, donc dans le modèle standard — et le combler demande de trancher
trois fois : la forme stockée, la comparaison de dérive, et ce que chaque
connecteur en fait. Une intention posée dans le modèle et écrite par un seul
connecteur sur trois est exactement ce que `WritableExtras` existe pour rendre
visible, et `recon.ExtraIntents` en porte déjà quatre dans cet état (`poe`, `stp`,
`dhcp_snooping`, `port_security`).

Ce que la pose fait, elle, **au-delà** des quatre champs : les connecteurs MikroTik
écrivent depuis ce lot le mode VLAN par port (SwOS) et le filtrage d'entrée
(RouterOS), sans qu'aucune règle ne les demande. Ce n'est pas une intention cachée
— c'est ce qui rend les quatre champs **effectifs** sur ces plateformes, où une
appartenance VLAN peut être écrite et sans effet. Le raisonnement est dans
[[Connecteurs]], §*Le modèle VLAN des deux plateformes MikroTik*.

## 9. Où ça vit

| Élément | Emplacement |
|---|---|
| évaluation (pure, testée) | `internal/portrules/` |
| stockage | table `switch_profiles`, migration `0021` (+ `tag` en `0030`) — même forme que les deux autres profils pour le CRUD, plus l'étiquette d'affectation |
| CRUD | `GET`/`POST /api/sites/{id}/profiles/switch`, `PATCH`/`DELETE /api/profiles/switch/{id}` — corps JSON : `name`, `spec`, `tag` (switch) |
| aperçu / pose | `POST /api/devices/{id}/switch-profile/preview` et `/apply` ; pose aussi déclenchée par `PUT …/tags/{tag}` si le tag matche un profil |
| écran | `web/src/pages/SwitchProfiles.tsx`, route `/{slug}/config/switch-profiles` ; l'ancienne `/…/config/groupes` y redirige |

### Décidé / livré / ouvert

| Question | État |
|---|---|
| Déduction du `media` à partir du préfixe **et** du libellé, débit en arbitre | **livré** — `portrules.mediaOf` |
| Sélecteur en deux temps : filtre puis tranche sur le **rang** | **livré** — `Selector.matches` puis `Selector.cut` |
| Bornes rognées et non refusées | **livré** — un « 4 derniers » sur une façade de 2 en désigne 2 |
| Première règle gagnante, une prise = une règle, sélecteurs sur la façade entière | **livré** — `portrules.Evaluate` |
| Décompte **désigné / obtenu** par règle | **livré** — `RuleReport.Matched` / `Won` |
| `all` jamais développé | **livré** — `Config.TaggedAll`, résolution laissée à `recon.Resolve` |
| Prises non couvertes laissées intactes ; fusion clé par clé à l'écriture | **livré** — `Plan.Uncovered`, `Config.Fields()` |
| Refus global si une règle est en défaut | **livré** — `Plan.Issues` non vide ⇒ on ne pose **rien** |
| PoE demandé sur une prise qui n'en a pas : corrigé **et dit** | **livré** — `portrules.fit`, `Assignment.Warn` |
| Aperçu calculé par le contrôleur, sur une spec **non enregistrée** | **livré** — jamais réimplémenté en TypeScript |
| Étape 2 : reprise des identifiants de groupe vers l'étiquette | **décidé, non construit** — elle déplace des mots de passe, donc aperçu d'abord |
| Étape 3 : retrait de `devices.group_id`, `device_groups`, `site_credentials.group_name` | **décidé, non construit** — la migration devra **refuser de s'appliquer** s'il reste un identifiant visant un groupe |
| Lien persistant équipement → profil | **écarté**, pas reporté (§8) — mais ajoutable plus tard, l'inverse non |
| Profil d'équipement (STP, SNMP, syslog, services) | **décidé, non construit** — c'est l'autre moitié de ce qu'un groupe promettait, et elle reste à faire |
