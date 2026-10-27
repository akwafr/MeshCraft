---
Statut: Accepté
Date: 2026-10-25
---

# ADR-80600 — Ctrl+F cherche dans le site, la seconde pression rend la main au navigateur

## Contexte

Un site de taille ordinaire pour ce produit — ARENA, mesuré le 2026-10-25 —
contient **14 équipements, 724 ports et 111 clients**. Ces trois vocabulaires
sont déjà servis en entier : `GET /api/sites/{id}/devices` porte les
équipements *et* leurs ports (`Device.ports`, `port_groups`), `GET
/api/sites/{id}/clients` porte les 111 MAC localisées. Deux réponses, 849
lignes nommables.

Aucun écran n'en montre plus d'une tranche à la fois. La page Ports pagine par
équipement, la façade d'un commutateur montre une boîte, la page Clients en
montre autant que la fenêtre. **La recherche du navigateur ne cherche donc que
dans la tranche affichée** : sur 724 ports, `Ctrl+F` « GE27 » trouve ce qui est
déjà sous les yeux et rate les 700 autres. C'est le cas général, pas un cas
limite — c'est l'état normal de toutes les pages de ce produit.

Il manque donc une recherche qui porte sur le site entier, et le raccourci que
tout le monde connaît pour « chercher » est déjà pris par celle qui ne sert à
rien ici.

Deux faits de la maison encadrent la question :

- **`focusInEditable()`** (`web/src/ui/dismiss.ts`) existe depuis le chapitre 8
  du guide, parce qu'un raccourci d'écran qui se déclenche dans la propre boîte
  de recherche de l'écran est le défaut précis que ce garde-fou a corrigé une
  fois.
- **La barre haute n'a plus de place.** Le commentaire de `styles.css` autour
  de `.topbar .tabs` le dit en chiffres : les enfants sont gelés un par un, le
  seul ressort restant est le plancher de 112 px du sélecteur de site, et les
  134 px rendus par l'ADR-0019 « paient un déficit antérieur au sélecteur
  plutôt qu'ils n'achètent de la place pour un septième enfant ».

## Décision

**1. `Ctrl+F` et `⌘F` ouvrent la recherche de meshcraft. La deuxième pression
appartient au navigateur.**

Le raccourci est détourné une fois, et une seule. Trois refus, dans cet ordre,
et chaque refus laisse filer l'évènement sans `preventDefault()` — donc la
barre de recherche du navigateur s'ouvre normalement :

| Situation | Qui prend `Ctrl+F` |
|---|---|
| Aucune couche ouverte, focus hors champ de saisie | meshcraft |
| Une couche de congédiement est ouverte (la palette elle-même, une modale) | le navigateur |
| Le focus est dans un `input`, `textarea` ou `contenteditable` | le navigateur |

La deuxième ligne est l'échappatoire, et elle n'est pas un effet de bord : la
palette *est* une couche de congédiement (`useDismissable` via `usePicker`), et
son champ de recherche *est* un champ de saisie. Les deux refus se déclenchent
donc ensemble dès qu'elle est ouverte. Qui voulait la recherche du navigateur
presse `Ctrl+F` deux fois et l'obtient, sans rien avoir à apprendre — et
l'état vide de la palette l'écrit en une ligne, parce qu'une échappatoire que
personne ne connaît n'en est pas une.

Le raccourci est porté par un primitif de `ui/dismiss.ts`
(`useGlobalModKey`), au même endroit et avec les mêmes garde-fous que
`useGlobalKey`. Aucun composant n'ajoute d'écouteur `keydown`.

**2. Les trois vocabulaires cohabitent en groupes, dans l'ordre
équipements → clients → ports.**

Une seule liste, trois en-têtes, chaque ligne marquée par un glyphe et par une
ligne de détail propre à son espèce. L'ordre est celui du **cardinal
croissant**, qui est aussi celui du **pouvoir discriminant décroissant** :

- 14 équipements : nommer un équipement, c'est presque toujours en désigner
  un seul ;
- 111 clients : une MAC est unique, mais c'est un fragment hexadécimal qu'on
  reconnaît plus qu'on ne le nomme ;
- 724 ports : `1` existe sur les quatorze boîtes, et `GE4` sur douze. Un nom
  de port est le jeton le moins discriminant du produit.

Mettre les ports en premier, c'est noyer les deux autres groupes à chaque
requête courte. Chaque groupe est donc plafonné à **cinq lignes**, et son
en-tête porte `montrés / trouvés` : le plafond est visible, pas silencieux.

À l'intérieur d'un groupe, les correspondances dont le **libellé commence** par
le premier mot tapé passent devant celles qui ne font que le contenir, puis
celles qui n'ont été trouvées que par du texte non affiché (le modèle, l'IP de
gestion, la MAC recollée). À score égal l'ordre de la source est conservé — ce
qui garde les ports d'une même boîte dans leur ordre **physique**, celui
qu'`expandPorts` produit et celui qu'on lit sur la face avant.

**3. Une MAC se cherche dans ses cinq écritures, et à partir de quatre
chiffres.**

`parseMacQuery` (`pages/macSearch.ts`) réduit la requête à ses chiffres
hexadécimaux ; si elle en garde **au moins quatre**, ils sont aussi comparés à
la MAC recollée de chaque client. En dessous de quatre, non : `1` et `ab`
figurent dans presque toutes les MAC du site et enterreraient les
correspondances par nom. La correspondance par MAC s'**ajoute** à celle par
nom, elle ne la remplace pas.

**4. Aucune route nouvelle, et aucune URL nouvelle. Un résultat mène à la
boîte.**

Les deux charges utiles d'un site sont déjà en cache sous `["devices", siteId]`
et `["clients", siteId]` ; la palette répète ces clés et ne paie rien quand une
page les a déjà lues. Un filtre servi par le serveur redessinerait plus
lentement pour filtrer les mêmes lignes.

Les trois espèces de résultat mènent au même genre de destination : la **page
de détail de l'équipement concerné** — l'équipement lui-même, le commutateur
qui porte le port, le commutateur où le client est vu. `deviceDetailPath`
existe, elle est stable au renommage, et c'est la seule adresse profonde dont
ce produit dispose : il n'y a **pas d'URL qui désigne un port** ni une MAC
(`useSearchParams` n'apparaît nulle part dans `web/src`). La ligne de résultat
porte donc elle-même ce que la recherche a trouvé — le port et sa boîte, la MAC
avec son commutateur et son port — parce que sur 724 ports, « où est GE27 /
User ? » est une question à laquelle la liste répond sans qu'on navigue.

**5. La palette n'appartient à aucun mode de navigation** (ADR-0013,
ADR-0019). Comme l'engrenage, elle ne figure dans aucune des deux barres et ne
change pas avec elles. Un résultat qui mène à `/{site}/equipment/…` fait
basculer le mode par la règle « l'URL gagne » déjà en place dans `Layout.tsx` ;
la palette ne déclare rien.

## Conséquences

- **Qui presse `Ctrl+F` pour chercher dans la page obtient autre chose.** C'est
  le prix, il est réel, et il n'est pas remboursé : il faut une seconde
  pression. On accepte ce coût parce que la recherche du navigateur ne voit
  qu'une tranche de 724 ports, et qu'elle reste joignable.
- **`Ctrl+K` est détourné aussi, et c'est aussi un raccourci du navigateur**
  (la barre d'adresse en mode recherche). Il est pris parce que c'est
  l'idiome des palettes et qu'un synonyme ne coûte rien ; il tombe sous la
  même règle et la même échappatoire à deux pressions, sans aucun code en
  plus.
- **La barre de recherche du navigateur s'ouvre par-dessus la palette** quand
  on la réclame, et c'est alors elle qui prend `Échap` tant qu'elle a le focus.
  La palette se ferme ensuite par `Échap` ou par un clic au large. On n'essaie
  pas de corriger cet empilement : il n'est pas sous notre contrôle.
- **Sept enfants dans la barre haute, et +40 px.** Mesuré avec la sonde de
  l'ADR-0019, en français, sur ARENA, après la bascule de l'ADR-0031 :

  | | au-dessus de 960 | en dessous de 960 |
  |---|---|---|
  | barre de commutation | 1151 → **1191 px** | 767 → **803 px** |
  | barre de routage | 1046 → **1086 px** | 661 → **697 px** |

  +40 px et +36 px, c'est-à-dire le glyphe de 30 px plus le gap du moment
  (`--sp-4`, puis `--sp-2`) : il n'y a pas de troisième chiffre, une icône
  ne rétrécissant pas. Le ressort reste celui d'avant — le plancher de
  112 px du sélecteur de site. Une **icône**, donc, jamais un champ déployé
  en permanence : un champ utilisable en coûterait 180.
- **La barre a une discontinuité à 960 px** et toute mesure future doit lire
  les deux côtés : le groupe d'identité se replie dans `.topbar-more`
  (398 px d'enfants deviennent 30), si bien que la barre veut ~380 px de
  moins juste en dessous que juste au-dessus. Un seul chiffre serait vrai
  sur un tiers de la plage.
- **Un port trouvé n'est pas un port sélectionné.** On arrive sur sa façade, pas
  sur son panneau. Fabriquer une URL de port est une décision qui appartient à
  qui tient la façade, et elle est délibérément laissée ouverte.
- **Le détail complet d'un client reste sur la page Clients** (volumes, VLAN,
  autres emplacements). La palette dit où il est, pas tout ce qu'on sait de lui.
- **Les deux requêtes ne partent qu'à l'ouverture.** Une page qui n'a jamais lu
  `["clients", siteId]` paie un aller-retour à la première ouverture de la
  palette. On préfère ça à faire lire la table des clients par tous les écrans
  du produit pour un raccourci qui peut n'être jamais pressé.
- **Un client sans nom LLDP n'a que sa MAC et son constructeur.** `ClientRow`
  ne porte ni nom d'hôte ni IP ; « chercher dans les noms de clients » veut dire
  ce que le produit sait d'un client, pas ce qu'on aimerait qu'il sache.

## Alternatives écartées

**Laisser `Ctrl+F` au navigateur et prendre `Ctrl+K`.** L'option honnête, et
celle que quelqu'un proposera de bonne foi. Elle perd pour une raison et une
seule : `Ctrl+K` doit s'apprendre. Le geste que fait un exploitant qui cherche
un port est `Ctrl+F`, il le fera de toute façon, et aujourd'hui il obtient une
recherche qui ne regarde pas la donnée. Rendre le raccourci au navigateur,
c'est garder ce ratage silencieux et parier sur une découverte. On a donc
détourné `Ctrl+F` **et** gardé `Ctrl+K` comme synonyme, pour que celui qui
connaît l'idiome des palettes ne soit pas contraint d'apprendre l'inverse.

**Détourner `Ctrl+F` sans aucune échappatoire.** Moins de code et une règle
plus simple à dire. Écartée : la recherche du navigateur fait des choses que
celle-ci ne fera jamais — chercher dans une note de statut, dans un message de
refus affiché verbatim, dans la page d'API. Supprimer un geste universel sans
le remplacer sur son propre terrain, c'est le genre de décision qu'on retrouve
dans un ticket six mois plus tard.

**Une troisième pression qui referme et rend la main.** Un cran de plus dans la
même direction, et un état de plus à tenir (« combien de fois a-t-on pressé »).
Écartée parce que l'échappatoire retenue ne coûte aucun état : elle tombe des
garde-fous que `useGlobalKey` applique déjà.

**Un classement par pertinence, les trois espèces mêlées.** Ce qu'un moteur
ferait. Écartée sur les chiffres : avec 724 ports contre 14 équipements, toute
requête courte remonte une colonne de ports et l'équipement cherché sort de
l'écran. Les groupes sont ce qui garantit qu'une espèce reste visible même
quand une autre a mille correspondances.

**L'ordre demandé — ports, clients, équipements.** C'est l'ordre dans lequel la
demande a été formulée, et c'est l'ordre inverse du pouvoir discriminant. Voir
ci-dessus.

**Un seul plafond global de vingt lignes.** Plus simple, et elle redonne le
problème précédent : vingt lignes de ports et rien d'autre. Le plafond est par
groupe parce que c'est le groupe qu'on protège.

**Une route `GET /api/sites/{id}/search?q=…`.** Elle aurait sa place si la
donnée n'était pas déjà là. Elle ne l'est pas : les deux charges utiles sont
servies en entier, en cache, et invalidées par le flux SSE du site. Un
aller-retour par frappe pour filtrer des lignes déjà présentes serait plus lent
à l'écran et plus lourd sur le serveur — et il casserait la frappe hors réseau,
qui marche aujourd'hui parce que le filtrage est local.

**Un champ de recherche déployé en permanence dans la barre.** Ce que la
demande décrit presque, et ce que la barre refuse : elle débordait déjà entre
721 et 773 px avec six enfants gelés. Un champ utilisable coûte 180 px, une
icône en coûte une trentaine. L'icône s'ouvre en popover, qui lui n'est
contraint par rien.

**Rendre le port et la MAC adressables par URL pour que les résultats soient
des liens profonds.** Le bon objectif, le mauvais moment : cela touche la
façade et la page Clients, c'est-à-dire deux écrans entiers, pour une
fonctionnalité dont la valeur principale — répondre sans naviguer — ne
l'attend pas.
