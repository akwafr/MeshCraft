---
Statut: Accepté
Date: 2026-10-25
---

# ADR-0021 — Le nom d'une région MST est une identité, pas un libellé

## Contexte

`internal/api/stp_apply.go` dérive la région MST de chaque équipement depuis le
**slug du site** : `siteMSTRegion` lit `sites.slug`, le passe à
`mdpv1.SiteMSTRegion`, et le nom de région qui part sur le fil est ce slug,
tronqué à 32 caractères.

Le commentaire de cette fonction nomme déjà le danger, et explique le choix du
slug par ce danger :

> A region needs a stable name and the slug is the only one guaranteed unique
> per site. Without it, deriving from the site name would produce a region that
> changes the day someone renames the site — which silently evicts every device
> not re-pushed.

Le raisonnement est juste. Sa prémisse est fausse : **le slug n'est pas stable.**
`PATCH /api/sites/{id}` accepte un champ `slug` et `Store.RenameSite` l'écrit.
Le slug est modifiable par l'opérateur, exactement comme le nom.

### Ce que coûte un renommage, et pourquoi ce n'est pas cosmétique

802.1s ne décide pas l'appartenance à une région en la demandant. Chaque pont
annonce un **triplet** : le nom de région, le numéro de révision, et un condensé
MD5 calculé sur la table complète des 4096 associations VLAN→instance. Deux
ponts sont dans la même région **si et seulement si** les trois concordent, au
bit près.

Un équipement renommé n'est pas « un équipement avec une étiquette différente ».
Il est **sorti de sa région** : il devient sa propre région d'un seul membre, les
frontières MST se déplacent, et la topologie est recalculée. Sur une fabric en
production c'est une interruption.

Et cet échec est **silencieux**. Rien ne lève d'erreur, aucun lien ne tombe,
aucune ligne de journal n'apparaît : le voisin est simplement traité comme un
pont RSTP ordinaire, et le seul symptôme est un port bloquant là où personne ne
l'attend. `mdpv1.MSTRegion` porte déjà ce commentaire ; il décrit un risque qui
s'est réalisé.

### Le relevé du 2026-10-25 : la fabric ARENA est déjà scindée

Douze Aruba Instant On 1930 sur ARENA, `192.0.2.200`→`212`, relus en lecture
seule par la route de reconnaissance `GET /api/devices/{id}/wcd/*` (sections
`MSTP`, `SpanningTreeGlobalParam`, `MSTPInstanceList`, `MSTPVLANList`). Huit
répondent, quatre sont hors ligne (`.209`, `.210`, `.211`, `.212` : *no route to
host*). Les huit joignables sont tous en MSTP (`enabled=1`,
`STPOperationMode=3`), révision 1, `maxHops=20`.

Et ils portent **deux identités de région distinctes** :

| Identité | Nom | Révision | Instance 1 | Membres |
|---|---|---|---|---|
| 1 | `site-exec` | 1 | `42,98-100` | ArubL, ArubA, ArubC, ArubD, ArubF, ArubG, ArubH (7) |
| 2 | `arena` | 1 | `10-11,42,60,98-100` | ArubB (1) |

Les VLAN du site sont aujourd'hui 1, 10, 11, 42, 60, 98, 99, 100. En excluant le
VLAN 1 — qui appartient au CIST sur toutes les plateformes — la table attendue
est `10-11,42,60,98-100`. **ArubB est le seul équipement conforme au plan
courant.** Les sept autres portent un état antérieur, et la divergence est
double :

1. **Le nom** : `site-exec` est un ancien slug du site, `arena` est le slug
   actuel. Le site a été renommé, et seul ArubB a été ré-appliqué depuis.
2. **La table VLAN→instance** : les VLAN 10, 11 et 60 ont été ajoutés au site, et
   seul ArubB a été ré-appliqué depuis.

Chacune des deux, seule, suffit à scinder la région. ArubB est donc une région
d'un seul membre, isolée des sept autres, et le condensé MD5 qu'il annonce ne
correspond à celui d'aucun voisin. Personne ne l'a vu, parce que **le nom de
région n'est affiché nulle part dans l'interface** : ni `SpanningTreeConfig.tsx`,
ni `SpanningTreeDevice.tsx` ne le mentionnent. Une grandeur dont dépend la
topologie, qu'aucun écran ne montre.

C'est le second fait qui force cet ADR : la dérivation depuis un identifiant
modifiable n'est pas un risque théorique, elle a déjà produit une fabric scindée
en silence.

## Décision

**Le nom d'une région MST n'est jamais dérivé d'une donnée que quelqu'un peut
renommer, et un nom déjà posé sur du matériel ne change pas parce que le code a
changé.**

Trois pièces, et c'est la troisième qui porte la garantie.

1. **Le nom générique se dérive de l'identifiant de site, qui est immuable.**
   `mdpv1.GenericMSTRegionName(siteID)` rend `mst-` suivi des douze premiers
   caractères hexadécimaux de l'UUID du site, soit seize caractères — très en
   dessous de la limite de 32. L'UUID est la clé primaire de `sites` : aucune
   route ne le modifie, et aucune ne le modifiera sans que ce soit une migration
   de données délibérée. Le nom est donc stable **par construction**, et non par
   convention.

   Il est générique au sens que l'utilisateur demande : il ne porte aucun texte
   lisible, donc personne n'a de raison de vouloir le « corriger ». Un nom qui ne
   dit rien est un nom qu'on ne renomme pas.

2. **Un nom épinglé dans le plan de site est autoritaire.**
   `sites.plan` est un `jsonb` libre qui porte déjà `stp_mst_revision`. La clé
   `stp_mst_region` le rejoint. Quand elle est présente et non vide,
   `siteMSTRegion` l'emploie **verbatim** et ne dérive rien. Quand elle est
   absente, le nom générique du point 1 s'applique.

   L'épinglage est donc le mécanisme par lequel un nom existant survit au
   changement de code, et aussi celui par lequel un opérateur peut délibérément
   aligner un site sur un nom que du matériel porte déjà.

3. **La migration 0047 épingle le slug courant de TOUS les sites existants.**
   C'est la garantie de non-renommage, et elle est mécanique, pas déclarative :

   ```sql
   update sites
      set plan = plan || jsonb_build_object('stp_mst_region', slug)
    where coalesce(slug, '') <> ''
      and coalesce(plan->>'stp_mst_region', '') = '';
   ```

   Après cette migration, tout site qui existait porte un nom épinglé **égal au
   slug**, c'est-à-dire exactement ce que le code produisait la veille. La région
   calculée pour chacun de ces sites est donc **identique octet pour octet** à
   celle d'avant le changement. Aucun équipement déjà posé ne peut être renommé
   par ce commit, parce que la valeur ne bouge pas.

   Le nom générique ne s'applique qu'aux sites **créés après** la migration, qui
   par définition n'ont aucun matériel portant une région.

4. **La règle de nommage ne décide pas de la table VLAN→instance.** Elle reste
   celle de `SiteMSTRegion` : tous les VLAN du site sauf le 1 dans l'instance 1.
   Cet ADR ne touche que le nom.

5. **L'identité de région devient visible.**
   `GET /api/sites/{siteID}/stp-mst-region` rend le triplet que le contrôleur
   appliquerait — nom, révision, table par instance — et `SpanningTreeConfig`
   l'affiche en lecture seule, sous un `HelpTip` qui énonce la règle : les trois
   valeurs forment une identité, un équipement dont l'une diffère quitte la
   région sans que rien ne le signale, et modifier la liste des VLAN du site
   oblige à ré-appliquer le spanning-tree sur tout le site.

   La route est en lecture et ne touche aucun équipement : elle lit la base et
   recalcule, comme le fait `siteMSTRegion`. Ce n'est pas un relevé du matériel.

## Conséquences

- **Les sites existants gardent un nom dérivé d'un slug, donc un nom qui
  « parle ».** C'est le prix direct du point 3 : on n'obtient la généricité que
  sur les sites neufs. ARENA restera épinglé sur `arena` jusqu'à ce que
  quelqu'un décide autre chose. Accepté sans réserve : un nom laid et stable est
  strictement meilleur qu'un nom propre et mouvant, et c'est le mouvement qui
  coupe le trafic.

- **Ce commit ne répare pas ARENA, et ne doit pas le prétendre.** Après
  migration, le nom épinglé est `arena` — celui d'ArubB, pas celui des sept
  autres. Une ré-application du spanning-tree sur l'un des sept le renommerait
  d'`site-exec` en `arena`. **Cette ré-application renommerait déjà avant ce
  commit** : le code actuel dérive `arena` du slug courant pour tous les
  équipements du site. Le changement est donc neutre en comportement, et la
  scission préexistante n'est ni causée ni guérie par lui.

  Ce qu'il apporte, en revanche, est un moyen de la réparer à moindre coût.
  Épingler `stp_mst_region` à `site-exec` via `PUT /api/sites/{id}/plan`
  réaligne l'intention sur ce que **sept** équipements portent déjà, et réduit la
  remise en région à un seul geste sur ArubB. Sans l'épinglage, la convergence
  demandait de renommer sept équipements en production. Le choix reste celui de
  l'opérateur, et il doit être planifié : la route ne le fait pas seule.

- **Le danger du renommage est désormais réduit, pas supprimé.** `stp_mst_region`
  est une clé du plan, et `PUT /api/sites/{id}/plan` l'écrit. Un opérateur peut
  toujours renommer une région et éjecter sa flotte. C'est délibéré : il faut que
  ce soit possible, précisément pour le cas ARENA ci-dessus. Ce qui change, c'est
  qu'un renommage devient un **geste explicite sur une clé nommée**, et non plus
  l'effet de bord d'un changement de slug entrepris pour une raison sans rapport.

- **Le nom générique est opaque au dépannage.** Devant un `mst-7f0d238df180` lu
  sur un commutateur, il faut une requête pour savoir quel site c'est. C'est
  l'exact revers de la propriété qui le rend sûr, et la raison du point 5 : si le
  nom ne se lit plus, l'écran doit le montrer. Le préfixe `mst-` est conservé pour
  qu'un humain voie au moins qu'il s'agit d'une région gérée par meshcraft, et non
  d'un nom d'usine dérivé de la MAC.

- **Une collision de noms reste théoriquement possible.** Douze caractères
  hexadécimaux, c'est 48 bits ; deux sites qui collisionneraient porteraient le
  même nom. Ce n'est pas un problème de sûreté : deux sites distincts ont des
  listes de VLAN distinctes, donc des condensés distincts, donc des régions
  distinctes malgré le nom commun. Et deux sites qui partageraient à la fois le
  nom et la table VLAN sont deux sites qu'il serait correct de fusionner en une
  région.

- **`SiteMSTRegion` ne décide plus du nom.** Son premier paramètre cesse d'être
  « le slug du site » pour devenir « le nom de région », et la fonction se borne à
  le valider et le tronquer. La décision du nom remonte dans `internal/api`, où
  vivent le plan et l'identifiant du site. `wire/mdpv1` reste ce qu'il doit être :
  la forme sur le fil, pas la politique.

## Alternatives écartées

**Garder le slug et interdire son changement.** Le plus court : refuser le champ
`slug` dans `PATCH /api/sites/{id}`. Écartée parce qu'elle paie le problème avec
une fonctionnalité sans rapport — le slug sert d'identifiant lisible dans les URL
et les exports, et le geler pour protéger MSTP fait porter à tout le produit une
contrainte née d'un protocole de spanning-tree. Et elle ne répare rien :
`site-exec` est déjà posé sur sept équipements, le slug est déjà `arena`, la
scission est déjà là.

**Un nom fixe unique pour toute l'instance, par exemple `meshcraft`.** La
généricité maximale, et la plus simple à lire. Écartée parce qu'elle fait
dépendre l'identité de région de la seule table VLAN. Deux sites adjacents en L2
— ce qui arrive sur une extension de site — porteraient le même nom et la même
révision, et leur appartenance à une même région ne tiendrait plus qu'à l'égalité
de leurs listes de VLAN. Le jour où elles coïncident, deux sites fusionnent en une
région sans que personne ne l'ait demandé, et le diagnostic est le pire qui soit :
tout concorde, et la topologie n'est pas celle du dessin. Dériver de l'UUID garde
les sites séparés par construction.

**Un nom tiré du nom d'affichage du site, simplement « assaini ».** C'est
l'origine du problème, en pire : le nom d'affichage change plus souvent que le
slug.

**Épingler le nom au moment du premier `apply`, plutôt que par migration.**
Séduisant : rien à migrer, et le nom se fige exactement quand il touche du
matériel. Écartée pour une raison de fenêtre. Entre le déploiement du code et le
premier `apply` d'un site existant, le nom épinglé est absent, donc le nom
générique s'applique, donc **le premier `apply` est le renommage** — précisément
l'événement que cet ADR existe pour empêcher. Le piège est que le code aurait
l'air correct : la règle « ne pas renommer » serait écrite, et violée par l'ordre
des opérations. La migration ferme la fenêtre avant qu'elle n'existe.

**Épingler, à la migration, ce que les équipements portent réellement, plutôt que
le slug.** La seule option qui aurait réparé ARENA automatiquement : lire la
région majoritaire de la flotte et l'épingler. Écartée fermement. Une migration
SQL ne parle pas aux équipements ; il faudrait une tâche qui ouvre une session
sur chaque commutateur de chaque site au déploiement, ce qui est une campagne de
lecture sur une flotte de production déclenchée par un `migrate`. Et « la région
majoritaire » est une notion qui n'a pas de sens quand la flotte est à moitié
hors ligne : quatre des douze Aruba de ARENA ne répondent pas, et rien ne dit
quelle région ils porteront en revenant. Le choix d'aligner un site sur
`site-exec` est un **geste d'opérateur**, informé par un relevé, pas une
déduction automatique.

**Afficher le nom de région en champ modifiable sur l'écran spanning-tree.**
Écartée pour l'instant : un champ texte invite à l'édition, et c'est le geste
qu'on veut rendre rare. La lecture seule plus un `HelpTip` dit la grandeur et son
risque sans offrir le bouton ; le renommage délibéré passe par le plan de site,
qui est déjà la surface des décisions coûteuses d'un site. Si le besoin d'un
renommage assisté se présente, il méritera son propre écran, avec la liste des
équipements qu'il va éjecter et dans quel ordre les reprendre — ce qui est un
assistant, pas un champ.

**Ne rien afficher, et documenter la règle dans le vault.** C'est l'état avant ce
commit, et il a produit une fabric scindée que personne n'a vue pendant des
semaines. Un invariant qui ne s'affiche nulle part n'est pas surveillé.

## Addendum du 2026-10-25 — l'arbitrage sur ARENA

La scission constatée sur ARENA a été portée à l'utilisateur : sept Aruba en
`site-exec` (instance 1 = `42,98-100`), **ArubB seul** en `arena`
(instance 1 = `10-11,42,60,98-100`, le seul conforme au plan du site).

**Décision : garder `arena`.** C'est ce que la migration d'épinglage retient
déjà, donc ce choix ne déclenche **aucune écriture** et le comportement est
inchangé.

**La conséquence est un piège, et c'est pour ça qu'elle est écrite ici.**
L'intention du site nomme `arena` tandis que sept équipements portent
`site-exec`. Donc **appliquer le spanning-tree sur l'un de ces sept le
renomme**, l'éjecte de la région des six autres et déplace une frontière MST.
Ce n'est pas une régression introduite par cet ADR — le code d'avant faisait
déjà exactement ça — mais le danger est désormais *connu*, ce qui change ce
qu'on a le droit d'ignorer.

Règle d'exploitation qui en découle : **aucun apply STP sur ArubL, ArubA,
ArubC, ArubD, ArubF, ArubG, ArubH sans l'avoir planifié**. Sept écritures,
sept recalculs. La convergence inverse — réaligner sur `site-exec` et ne
toucher qu'ArubB — reste disponible par `PUT /api/sites/{id}/plan` si
quelqu'un la préfère plus tard.

Deux inconnues demeurent : les **4 Aruba hors ligne** peuvent revenir avec une
troisième identité, et la région de l'Arista `core01-arena` comme celle du FS
`sw01-arena`, qui sont sur le même L2, **n'a jamais été vérifiée** — la route
`wcd` est propre à Instant On et `read-state` était hors limites.
