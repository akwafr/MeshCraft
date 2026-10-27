---
Statut: Accepté
Date: 2026-10-25
---

# ADR-0022 — Rejoindre une région MST est une capacité déclarée, pas une conséquence de « sait écrire STP »

## Contexte

ADR-0021 a rendu l'identité de région **calculable et visible** : `siteMSTRegion`
dérive le triplet (nom, révision, table VLAN→instance) du site seul, et
`GET /api/sites/{siteID}/stp-mst-region` l'affiche. Ce qui n'a jamais été
modélisé, c'est **quels connecteurs savent la poser**.

Aujourd'hui, deux faits séparés sont confondus en un :

- `Connector.WritesExtra("stp")` dit « les écritures spanning-tree de ce
  connecteur sont déclarées » ;
- `Capabilities().STPModes` dit « voici les protocoles que cette gamme fait
  tourner ».

Aucun des deux ne dit « ce connecteur sait écrire un nom, une révision et une
table VLAN→instance ». Et `internal/api/stp_apply.go` remplit
`SpanningTreePlan.Region` **inconditionnellement**, pour tout équipement d'un
site, sans regarder ce que le connecteur en fera.

### Ce que ça produit, mesuré le 2026-10-25 sur ARENA

Les huit Aruba joignables portent la région attendue — `mst-7f0d238df180`,
révision 1, instance 1 = `10-11,42,60,98-100`. Les deux autres équipements du
même L2, dont la région n'avait jamais été relue, portent ceci :

| | `core01-arena` (Arista EOS 4.28.13M) | `sw01-arena` (FS S3400-48T4SP) |
|---|---|---|
| mode | `mstp` | **`RSTP(2004)`** |
| nom de région | `[]` (vide) | **`649D9929F706`** — sa propre MAC |
| révision | 0 | 0 |
| instance 1 | *aucune* — `0 1-4094` | *aucune* — `0 1-4094` |

Les deux sont donc des **régions d'un seul membre**, et l'Arista l'imprime
lui-même : ses dix ports vers la flotte Aruba sont des ports `P2p Boundary`.

Le FS, lui, est encore plus loin : il n'est pas en MSTP du tout. La « MST
Region » que sa CLI affiche est inerte, et son nom par défaut est sa MAC — le
cas exact que `MSTRegion.Configured` décrit comme « chaque équipement est sa
propre région ».

**Et le FS a reçu des applies.** Il déclare `WritesExtra("stp") = true` et
`STPModes = ["rstp"]` ; le résolveur du site choisit donc `rstp`, le plan arrive
**avec une région configurée**, le connecteur écrit le pont, et ne dit **rien**
de la région. `ApplyResult` revient propre. Un équipement hors région, avec un
apply réussi derrière lui et rien à l'écran pour le contredire : c'est la panne
silencieuse d'ADR-0021, obtenue cette fois non par un renommage mais par une
**capacité jamais déclarée ni refusée**.

### Pourquoi on ne peut pas simplement mettre le S3400 en MSTP

Parce que le geste est mesuré, et il est mauvais. Relevé le 2026-08-11 et écrit
dans l'en-tête de `internal/driver/fs/s3400/stp.go` : `spanning-tree mode <m>`
**ferme la session SSH avant que la CLI réponde**, l'arbre reconverge, et pendant
~45 s `show spanning-tree` répond « No spanning tree instance exists. ». Le
connecteur renverrait une erreur au-dessus d'un équipement qu'il **a** changé, et
vérifierait son travail contre un arbre vide.

S'y ajoute que la **grammaire d'écriture de la région n'est pas capturée** sur
cette plateforme. La lecture l'est depuis aujourd'hui — `show spanning-tree mst
region`, forme propre à cette gamme : `show spanning-tree mst configuration` est
répondu « Too many parameters ». La capturer côté écriture demande d'entrer en
mode configuration sur un équipement de production, ce qui n'a pas été fait.

## Décision

**Un connecteur DÉCLARE qu'il sait rejoindre une région, et un connecteur qui
reçoit une région qu'il ne sait pas poser le DIT, sur tous les chemins.**

Trois pièces.

1. **`mdpv1.FeatMSTRegion` (`"MSTRegion"`) rejoint le vocabulaire de
   `Capabilities.Features`.** Il signifie exactement « ce connecteur écrit
   `SpanningTreePlan.Region` : le nom, la révision et la table ». Il est
   **séparé** de `WritesExtra("stp")` et séparé de `STPModes`, pour la raison que
   `FeatSTPSuperRoot` porte déjà dans son commentaire : savoir qu'une gamme fait
   tourner MSTP et savoir lui écrire une région sont deux faits différents, et
   celui qui compte pour l'opérateur est le second.

   Additif : une clé de plus dans une `map[string]bool` déjà sur le fil. Un
   driver v1 qui ne la connaît pas la laisse absente, ce qui est la bonne réponse
   par défaut — « je ne sais pas poser de région » est l'hypothèse sûre.

2. **Une région configurée qu'on ne pose pas produit une ligne
   `ApplyResult.Skipped`, nommément.** Pas un silence, pas une note générique :
   le nom de la région demandée, la raison plateforme, et la conséquence en
   clair — cet équipement reste sa propre région et commute hors de l'arbre du
   site. EOS le faisait déjà pour le cas « région fournie sous rstp » ; la règle
   devient générale et s'applique au S3400, qui ne disait rien.

3. **`arista-eos` déclare la capacité ; `fs-s3400` la refuse.** L'Arista écrit
   déjà le triplet complet (`spanning-tree mst configuration` / `name` /
   `revision` / `instance N vlan …`), vérifié sur capture et sur un apply réel le
   2026-08-12 : la déclaration ne promet rien de neuf, elle rend lisible ce qui
   était déjà vrai. Le FS refuse, avec les deux raisons ci-dessus écrites dans la
   ligne que l'opérateur lit.

## Conséquences

- **`sw01-arena` restera hors région jusqu'à une décision d'exploitation
  séparée**, et c'est désormais dit à chaque apply au lieu d'être tu. Le prix est
  réel : un commutateur de production qui commute parfaitement et qui n'est pas
  dans l'arbre que le dessin décrit. Il est accepté parce que l'alternative — le
  basculer en MSTP au fil d'un apply automatique — coupe la session en plein
  geste et vérifie son travail contre un arbre vide.

- **La pose sur `core01-arena` reste un geste planifié, et le commit ne la fait
  pas.** L'Arista est root du CIST à la priorité 0 avec les 4094 VLAN, et dix de
  ses ports sont des frontières MST. L'y faire rejoindre la région déplace les
  VLAN 10, 11, 42, 60, 98, 99 et 100 du CIST vers l'instance 1 : c'est un
  recalcul de topologie sur le cœur du site, pas un réglage.

- **Trois défauts latents de l'écriture EOS sont fermés dans le même commit**,
  parce qu'ils ont tous la même forme — une ligne que l'équipement refuse, dans
  un bloc que `runConfigBlock` abandonne à la première erreur, ce qui emporte la
  région **et** le mode avec elle :

  - une entrée `instance 0` dans `InstanceVLANs` était émise telle quelle, alors
    que le CIST est implicite et que mapper des VLAN dedans est refusé ;
  - la révision partait en `strconv.Itoa` sans borne, donc une valeur hors
    0–65535 produisait une ligne refusée ;
  - `sanitizeRegionName` repliait sur la chaîne littérale `"meshcraft"` tout nom
    dont aucun caractère ne survivait au filtre. C'est **l'alternative
    explicitement écartée par ADR-0021** — un nom fixe unique pour toute
    l'instance — atteinte par accident : deux sites épinglés sur des noms
    non-ASCII distincts auraient partagé une région.

- **Un `Features` sans clé ne vaut pas un refus.** Une absence veut dire « ce
  connecteur n'a pas déclaré savoir », ce qui est le défaut sûr, mais l'appelant
  ne doit pas en conclure que l'équipement a été prévenu. Seule la ligne
  `Skipped` du point 2 est la preuve que le refus a été prononcé.

- **La clé n'est pas documentée dans `openapi.yaml`, et c'est la convention
  existante.** Aucune clé de `Capabilities.Features` ne l'est : la map voyage
  comme `map[string]bool` libre dans `schemas.json`, et le vocabulaire vit dans
  les commentaires des constantes de `wire/mdpv1`. Ajouter une section pour
  celle-ci seule aurait créé deux endroits de vérité. `schemas.json` est donc
  inchangé, ce qui est vérifiable : la génération reflète les types, pas les
  valeurs de constantes.

## Alternatives écartées

**Déduire la capacité de `STPModes` contenant `"mstp"`.** La plus courte : pas de
clé à ajouter. Écartée parce qu'elle refait exactement l'erreur que
`FeatSTPSuperRoot` a coûté cher à corriger. `STPModes` répond « quels protocoles
cette gamme fait tourner » — un fait sur le matériel — et la question posée ici
est « que sait écrire ce code », un fait sur le connecteur. Instant On et EOS
listent tous deux `mstp` et posent la région ; rien n'empêche un connecteur de
lister `mstp` et de n'écrire que la ligne de mode, et ce connecteur serait alors
annoncé comme capable de rejoindre une région. La déduction est vraie aujourd'hui
par coïncidence, et une déduction vraie par coïncidence est une déduction qui
cassera sans bruit.

**Déduire la capacité de `WritesExtra("stp")`.** Encore plus court, et faux dès
maintenant : le S3400 la déclare. C'est la confusion que cet ADR existe pour
défaire.

**Ne pas envoyer de `Region` aux connecteurs qui ne savent pas la poser.** Propre
en apparence : `stp_apply.go` interroge la capacité et laisse le champ vide.
Écartée parce qu'elle **supprime le refus** — un plan sans région est un plan
qui n'a rien à refuser, le connecteur n'a plus rien à dire, et l'équipement
redevient silencieusement hors région. Or le seul livrable qui compte ici est
que l'opérateur VOIE que ce commutateur n'est pas dans l'arbre. Le champ part,
et le refus revient.

**Écrire la grammaire de région du S3400 derrière le refus de mode, « prête pour
plus tard ».** Séduisant, et c'est précisément le piège que le contrat nomme. La
forme d'écriture n'a pas été capturée sur cette plateforme, et cette gamme a déjà
produit la démonstration que deviner coûte : `spanning-tree mstp instance 0
priority 36864` — la forme déduite de la grammaire d'un voisin — est **acceptée
silencieusement et ne configure rien**. Du code non vérifié derrière un refus
devient du code vérifié le jour où quelqu'un lève le refus en croyant lever une
seule chose.

**Mettre le S3400 en MSTP et poser la région.** C'est ce que l'énoncé du besoin
demande littéralement. Écartée sur la mesure du 2026-08-11 citée en contexte : le
changement de mode ferme la session avant la réponse, et laisse ~45 s pendant
lesquelles l'équipement est illisible. Ce n'est pas une limite de commande, c'est
un geste que ce connecteur ne sait pas conduire — il lui faudrait une
reconnexion, une attente de reconvergence et une re-lecture, ce qui est un
mécanisme à construire et non une ligne à ajouter.

**Un drapeau par sous-partie du triplet** (sait écrire le nom / la révision / la
table). Écartée parce que le triplet n'est pas divisible : 802.1s compare les
trois ou rien. Un connecteur qui saurait écrire le nom et pas la table ne
rejoindrait aucune région, donc la capacité partielle n'a pas de valeur à
exposer, et trois drapeaux inviteraient à en déclarer un seul.
