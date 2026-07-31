---
tags: [frontend, spec]
---

# Rendu des ports — spécification visuelle

Chaque port d'un équipement est une **petite case** dans la carte de l'équipement ([[Interface web]]). Objectif : lire d'un coup d'œil l'état physique (up/down) et la configuration VLAN de toute une façade, même sur un switch 48 ports, même sur mobile.

## Où en est cette page

C'est une **spécification**, et le rendu livré s'en écarte sur la technique plus que sur l'intention. Ce qu'elle décrit reste la cible ; le tableau dit où en est chaque trait.

| Trait | État |
|---|---|
| Case colorée par VLAN, façade complète lisible d'un coup d'œil | **livré** — `PortSquare` (`web/src/netui.tsx`), spec visuelle dans `web/src/network.ts` |
| Trunk en **stries** des VLANs membres, max 4 couleurs | **livré**, mais en `repeating-linear-gradient` CSS, pas en `pattern` SVG |
| Disposition en 2 rangées entrelacées au-delà de 16 ports | **livré sur la fiche du switch** ; à plat sur les autres écrans |
| Numéro inscrit dans la case au zoom | **livré**, mais c'est le numéro de **port**, pas de VLAN |
| Filtrage visuel par VLAN depuis la légende | **livré au survol** ; le clic pour verrouiller le filtre n'est pas branché |
| PoE signalé sur la case | **livré**, sous forme d'un petit carré — pas d'éclair, pas de couleur par consommation |
| **Clignotement de dérive** par port (`pending` / `drift`) | **livré** — trait absent de cette spec, décrit ci-dessous |
| **SVG** : un `<svg>` par façade, une `<g>` par port, `pattern` mémoïsés | **décidé, non construit** — le rendu est en DOM (§ *Implémentation*) |
| Bordure codant l'état physique (up/down/admin-down/inconnu) | **décidé, non construit** — c'est le **remplissage** qui code le lien |
| Rond central du VLAN natif | **décidé, non construit** — le natif d'un trunk est aujourd'hui **invisible** |
| Surcouches trafic, alertes de port, chevron LLDP, toggles de toolbar | **décidées, non construites** — mais le voisin LLDP est désormais **dans l'infobulle** de la case ([[Voisinage LLDP]]) |
| Palette générée en **OKLCH** à luminance contrainte | **décidée, non construite** — 16 teintes fixes, les 8 premières vérifiées en CVD |
| Tailles paramétrables (12 / 16 / 24–32 px), zoom de façade, pincement | **décidé, non construit** — trois tailles en dur : 38 / 13 / 9 px |
| Virtualisation de la liste de cartes | **décidée, non construite** |

## La case

Taille de base **16×16 px** (grille dense : 12 px ; zoom/tactile : 24–32 px), coins arrondis 2 px, espacement 3 px.

> [!note] La taille n'est pas un réglage, c'est une constante par écran
> Trois valeurs en dur, choisies pour ce que chaque écran doit répondre :
> **38 px** sur la fiche du switch (où l'on clique et sélectionne), **13 px** sur
> les cartes d'inventaire (où l'on balaye un parc), **9 px** dans un nœud de
> [[Weathermap]] (où la façade n'est qu'un indice de forme). La hauteur est
> `0,92 × largeur`, la bordure de 1 px et le rayon de 3 px.
>
> Le zoom de façade et le pincement ne sont pas construits, et c'est ce qui rend
> la fiche du switch **inutilisable au doigt** : la sélection multiple repose
> entièrement sur ⇧ et Ctrl ([[Interface web]]). Un zoom tactile n'est donc pas un
> confort, c'est le prérequis du mobile.

Disposition fidèle à la façade réelle : `rows` du catalogue de modèles ([[Connecteurs]]) — 2 rangées entrelacées (1,3,5… / 2,4,6…) comme sur le matériel, ports SFP séparés par un petit gap et coins droits pour les distinguer.

**Livré pour l'entrelacement**, sur la fiche du switch : un groupe de **16 ports ou plus** est scindé en impairs dessus / pairs dessous, comme le matériel. C'est ce qui rend la façade reconnaissable, et c'est aussi ce qui rend la **sélection de plage** piégeuse — d'où la règle du §*Sélection* de [[Interface web]] : la plage se mesure sur l'ordre **physique**, jamais sur les rangées affichées. Les cartes d'inventaire et les nœuds du plan, eux, disposent les cases **à plat** : à 13 et 9 px, deux rangées coûteraient de la hauteur sans rendre la façade plus lisible.

La distinction des **SFP** est en revanche presque absente : les groupes de la façade sont bien séparés (chaque groupe a son intitulé), mais la case elle-même n'a ni gap dédié ni coins droits. Le seul indice est le numéro, préfixé d'un `S` (`sfp2` → `S2`). Sur un switch dont les quatre dernières cages sont optiques, la façade ne le montre pas.

### État physique → bordure

| État | Bordure |
|---|---|
| **up** (ifOperStatus=up) | 2 px **blanc** (thème sombre) — halo léger |
| **down** | 2 px **gris** `#555`, remplissage assombri (60 %) |
| **désactivé** (admin down) | gris + motif diagonal discret |
| **inconnu** (device unreachable) | bordure pointillée |

> [!warning] Ce n'est pas la bordure qui code l'état, c'est le REMPLISSAGE
> Le rendu livré tranche autrement, et il faut le savoir pour lire une façade :
>
> | Cas réel | Rendu |
> |---|---|
> | configuré **et** lien présent | case **pleine** (couleur ou stries) |
> | configuré, **pas** de lien | case **transparente**, bordure de la couleur du VLAN |
> | non géré / libre | fond neutre, bordure neutre |
> | équipement injoignable | toute la façade à **35 % d'opacité** |
>
> Autrement dit : *plein = ça vit*, *contour = c'est prévu mais rien n'est
> branché*. C'est plus lisible que la spec sur le cas le plus fréquent — repérer
> les prises mortes d'une salle — parce que la différence plein/vide se voit à
> 13 px alors qu'une bordure de 2 px non.
>
> Ce que la spec avait de plus, et qui manque vraiment : **admin-down** et
> **inconnu** n'ont aucun rendu distinct. Un port volontairement désactivé se
> présente comme un port simplement débranché, ce qui est la confusion la plus
> coûteuse de la liste — on va chercher un câble pour un port qu'on a coupé
> exprès. Le motif diagonal de la spec est la bonne réponse et reste à faire.
>
> La source du remplissage est le champ `link` du port, et c'est bien une donnée
> **observée** : le polling `if_state` l'écrit à partir d'`ifOperStatus`, et les
> transitions produisent de vrais événements `port.up` / `port.down` /
> `port.flapping` ([[Monitoring]]). Sur ce point, la case dit la vérité du réseau.

### ACTIF n'est pas GÉRÉ — le défaut qui rendait une façade entière éteinte

C'est la correction la plus visible de cette page, et elle portait sur le premier écran
qu'un exploitant ouvre après une adoption.

Le remplissage se décide sur l'**intention** (accès, trunk) ; l'état du lien ne fait que
choisir entre *plein* et *contour*. Un port **sans intention** ne tombait donc dans aucune
branche et sortait avec l'apparence d'un port libre — **quel que soit son lien**. Sur un
switch fraîchement adopté, dont aucun port n'a encore d'intention, la façade s'affichait
donc entièrement éteinte avec un uplink 10 G branché.

> [!bug] Relevé sur le parc, le 29 juillet 2026
> ```
> lab-crs326   sfp-sfpplus2  link=10G  mode=∅   → affiché LIBRE
> lab-css326   SFP2          link=10G  mode=∅   → affiché LIBRE
> sw-labo-1930 GE49…GE52     link=10G  mode=trunk → affiché actif
> ```
> Les deux MikroTik venaient d'être adoptés en mode « push » : `devices.ports` ne portait
> que ce que la télémétrie y avait écrit (`link`), aucune intention. Le 1930, configuré,
> s'affichait correctement — ce qui rendait le défaut d'autant plus trompeur : l'écran
> semblait fonctionner.

La confusion était entre deux notions que la même case porte :

| | Nature | Source |
|---|---|---|
| **géré** | une **intention** de meshcraft | `devices.ports[].mode` |
| **actif** | un **constat** | `ifOperStatus` + `ifHighSpeed`, écrits dans `devices.ports[].link` par la collecte |

Un quatrième état de case existe donc désormais : **lien actif, non géré** — case **pleine
en gris ardoise**, prise HORS de la palette des VLAN. Lui donner une teinte de la palette
ferait lire une configuration qui n'existe pas ; la case pleine dit ce qu'elle doit dire,
« le lien est actif », dans le même langage que partout ailleurs. L'infobulle le nomme au
long, et la légende de façade porte l'entrée.

La même confusion se trouvait dans trois autres endroits, tous corrigés avec :

- le **compte de ports actifs** (`deviceRates`) — il exigeait une intention, donc annonçait
  « 0 port actif » sur les deux MikroTik ;
- le **classement des ports les plus chargés** du [[Monitoring]] — vide sur tout équipement
  non configuré ;
- la **courbe de trafic de repli** côté contrôleur (`portRates`), pour la même raison.

Les uplinks « tous VLANs » restent exclus des **débits** (leur trafic est déjà compté sur
les ports d'accès qui l'alimentent), et cette exclusion-là se lit bien sur l'intention —
faute d'autre source. C'est assumé et écrit dans le code : un port non géré ne peut pas
être reconnu comme lien d'agrégation, donc il est compté.

### Le débit d'un ÉQUIPEMENT n'est pas celui d'un SITE

Deuxième moitié de la même confusion, trouvée en corrigeant la première. Le calcul des
débits écartait les trunks « tous VLANs » — les liens d'agrégation — au motif que « leur
trafic est déjà compté sur les ports d'accès qui l'alimentent ».

Le motif est juste pour un **total de site** et faux pour **un équipement**, et le parc l'a
montré : les quatre seuls ports actifs du 1930 de laboratoire sont ses quatre cages SFP+,
**toutes** en trunk complet. Son débit affiché était donc zéro alors que la métrique
existait et remontait — ce qui se lit comme une collecte en panne.

Sur un seul équipement, il n'y a rien à corriger, parce que les deux sens sont comptés
**séparément** : une trame qui entre par GE1 et sort par l'uplink compte une fois en
entrant et une fois en sortant, jamais deux fois dans la même somme. `rx` est « tout ce qui
entre dans ce switch », `tx` « tout ce qui en sort ».

Deux fonctions, donc, pour deux questions :

| Question | Fonction | Règle |
|---|---|---|
| combien passe par **ce switch** ? | `deviceRates` | tous les ports actifs, les deux sens séparés |
| combien passe sur **ce site** ? | `siteRates` | sans les liens **inter-switchs**, sinon une trame qui traverse deux équipements est comptée deux fois |

La courbe de trafic du contrôleur applique la même règle sur les séries — les deux doivent
raconter la même histoire, sinon la vignette et la courbe du même écran se contredisent.

> [!warning] L'exclusion se lit sur l'INTENTION, et c'est sa faiblesse
> « Lien inter-switch » est approché par `trunk` + `tagged: all`, faute d'autre source. Ça
> a deux conséquences à connaître :
>
> - un lien d'agrégation que personne n'a déclaré comme tel est **compté** ;
> - le lien d'**arrivée** d'un site — celui qui monte vers le routeur, et dont le trafic
>   est précisément ce qu'un total de site devrait compter — est un trunk complet lui
>   aussi, donc **exclu**. Sur le parc de référence, où tous les ports actifs sont des
>   trunks complets, le total du site vaut donc **zéro** : c'est cohérent avec la règle, et
>   ce n'est pas ce qu'on veut lire.
>
> Le bon discriminant existe depuis peu et n'est pas encore employé ici : le **voisinage
> LLDP** ([[Voisinage LLDP]]). Un port dont le voisin est un autre équipement **du même
> site** est un lien interne — à exclure ; un trunk complet dont le voisin est inconnu de
> meshcraft est un lien d'**arrivée** — à compter. C'est le prochain pas, et il vaut mieux
> le lire ici que de recalibrer une constante en cherchant pourquoi un total est faux.

### Configuration VLAN → remplissage

La couleur de chaque VLAN est définie site-wide dans les réglages ([[Interface web]]).

| Cas | Rendu | État |
|---|---|---|
| Access (un seul VLAN) | case **pleine** de la couleur du VLAN | **livré** |
| Trunk multi-VLAN | **stries diagonales** alternant les couleurs des VLANs tagués (max 4 visibles) | **livré** — 135°, stries de 3 px (4 px sur la grande façade) ; le `+n` au tooltip n'est pas fait |
| PVID / natif (trunk avec untagged) | stries des tagués + **rond central** (Ø 6 px, liseré sombre 1 px) de la couleur du VLAN natif | **non construit** |
| Trunk "all vlans" | stries neutres gris/blanc | **livré autrement** — les stries prennent la couleur du **VLAN natif**, et le gris neutre `#94a3b8` seulement si ce natif est inconnu du site |
| Non géré par meshcraft | case vide, bordure seule | **livré** |

Le rond central signifie toujours « le VLAN untagged de ce port » : sur un port access on peut l'omettre (la case entière l'exprime déjà).

> [!warning] Sans le rond central, le VLAN natif d'un trunk ne se voit nulle part
> La logique du natif **existe** et elle est même plus fine que la spec : un trunk
> qui ne désigne aucun natif n'est pas un trunk sans natif — le matériel y place le
> VLAN 1, et c'est ce que la relecture de l'équipement remontera toujours. Le code
> le sait (`nativeVlan`) et s'en sert pour dire si un port *porte* un VLAN.
>
> Mais rien ne le **dessine**. Sur un trunk `20, 30, 99` de natif `10`, le VLAN 10
> est invisible : les stries ne montrent que les tagués. Or le natif d'un trunk est
> précisément ce qu'on vient vérifier quand quelque chose ne passe pas, et c'est
> aussi ce qu'on écrase le plus facilement sans le voir.
>
> C'est le manque le plus important de cette page — plus que le SVG, plus que les
> surcouches : une case peut aujourd'hui **taire** une partie de la configuration
> qu'elle prétend montrer.

Un détail livré que la spec n'avait pas prévu, et qui règle un cas gênant : un trunk qui ne porte **qu'un** VLAN recevrait une strie unicolore, donc indistinguable d'un access. Une seconde teinte est donc **dérivée** de la première (mélange à 35 % avec le fond) pour que la case reste rayée. Le mode d'un port doit se voir sans lire l'infobulle.

### Surcouches optionnelles (toggles dans la toolbar)

- **PoE** : éclair ⚡ 6 px coin haut-droit si le port délivre du PoE (couleur = consommation vs budget). **Livré, en plus discret** : un carré de 5 px en bas à droite, de la couleur de l'encre, affiché quand le port a un lien **et** consomme. Pas d'éclair, pas de codage de la consommation, et pas de toggle — c'est toujours affiché. Conséquence à connaître : un port **configuré** en PoE mais sans équipement branché ne montre rien. La case dit « du PoE sort d'ici », pas « du PoE est prévu ici ».
- **Trafic** : opacité/pulse proportionnelle au débit (données [[Monitoring]]). **Décidé, non construit** — et le prérequis manque : les débits par port ne sont pas encore dérivés des compteurs SNMP (voir le callout des débits simulés dans [[Weathermap]]).
- **Alertes** : point rouge clignotant si événement récent sur le port (flap, err-disable) — lié au [[Journal d'événements]]. **Décidé, non construit — mais la donnée existe désormais.** La corrélation de battement est livrée et éprouvée sur du matériel : un port qui bat produit un `port.flapping`, et sa stabilisation un `port.flap_cleared` ([[Monitoring]]). C'est donc la surcouche la moins chère à construire, et la plus utile : le battement est exactement le défaut qu'on ne trouve pas en regardant une façade, parce que le port est *up* la moitié du temps.
- **Uplink LLDP** : chevron ▲ si un voisin est connu ([[Weathermap]]). **Décidé, non construit** — mais son prérequis l'est : la collecte existe depuis le 29/07/2026 ([[Voisinage LLDP]]), et le voisin s'affiche **au survol** de la case (nom système, port distant, depuis quand il est vu, et sur quoi le rapprochement s'appuie).

  Pourquoi l'infobulle d'abord et le chevron ensuite : la case fait 13 px sur la page Équipements et porte déjà quatre informations — le VLAN par la couleur, l'état de lien, le PoE par un point, la dérive par un clignotement. Y ajouter un marqueur demande de trancher sa **priorité** contre les quatre autres, et ce n'est pas une décision de collecte. L'infobulle, elle, n'entre en concurrence avec rien.

  Ce que l'infobulle ne fait PAS, et qu'il faut savoir avant de conclure quoi que ce soit d'une case muette : **l'absence de voisin n'est pas l'absence de câble**. Trois causes ordinaires, toutes relevées sur le parc — l'équipement d'en face ne parle pas 802.1AB (les MikroTik en SwOS), celui-ci ne fait tourner LLDP que sur certaines de ses interfaces (**deux sur vingt-neuf** sur le CRS326 de laboratoire), ou aucun cliché n'est encore arrivé. Les deux premières causes sont signalées par un marqueur « LLDP ? » sur la carte de l'équipement ; la troisième ne peut pas l'être, la MIB ne disant pas *pourquoi* elle ne liste pas un port.

### Surcouche livrée que la spec n'avait pas prévue : la dérive

Deux classes de clignotement portent sur **le port**, et non sur l'équipement :

- **`pending`** (bleu) : meshcraft porte une configuration que l'équipement n'a pas encore ;
- **`drift`** (orange) : l'équipement a été modifié en dehors de meshcraft.

Le clignotement ne porte **que sur les ports concernés**. Faire clignoter toute la façade pour un port qui diffère ferait chercher longtemps — et c'est le même raisonnement que la pastille d'équipement, où la joignabilité prime sur la synchronisation ([[Réconciliation]]). Un repli statique est prévu sous `prefers-reduced-motion` : une information ne doit pas disparaître parce qu'on a désactivé les animations.

C'est la surcouche la plus utile de celles qui existent, parce qu'elle répond à la question qu'aucune couleur de VLAN ne répond : *est-ce que ce que je vois est ce que l'équipement porte ?*

### La question que la case ne pose même pas : *est-ce que ça AGIT ?*

Le clignotement de dérive répond à « ce que je vois est-il ce que l'équipement
porte ? ». Il reste une question en dessous, et c'est la plus désagréable : **ce que
l'équipement porte agit-il sur le trafic ?**

Sur les deux plateformes MikroTik, la réponse est parfois non, et une case pleine
ment alors deux fois — pas de dérive, pas de `pending`, une couleur franche, et rien
qui circule :

| Cas réel | Ce que la case dessine |
|---|---|
| SwOS, port en mode VLAN `disabled` | la couleur de son VLAN natif — que le port ignore, puisqu'il ne consulte pas la table |
| SwOS, port en « Force VLAN ID » | des **stries** de trunk — alors que le port range tout son trafic entrant dans son seul VLAN natif |
| RouterOS, bridge sans `vlan-filtering` | la segmentation entière d'une façade qui laisse tout passer partout |
| RouterOS, `ingress-filtering: false` (le **défaut**) | une segmentation étanche, alors qu'elle ne l'est qu'à la sortie |
| RouterOS, port d'accès en `frame-types: admit-all` | un accès — dont un poste sort en étiquetant lui-même son trafic |

**Livré, mais pas dans la case** : ces faits sortent des connecteurs sous forme de
**phrases** (`driver.State.Notes`), sont enregistrés dans `last_apply.notes`, et sont
affichés par `ConnectorNotesBanner` en bas des bandeaux de la fiche du switch —
après les ports épargnés et les intentions non supportées, parce qu'il n'appelle
aucune décision.

> [!warning] Le bandeau existait côté serveur depuis le début, et rien ne l'affichait
> `rememberApply` écrit ces phrases dans `last_apply.notes` depuis la livraison des
> connecteurs MikroTik, et son commentaire dit même que « l'écran doit pouvoir le dire
> au même endroit qu'il dit les ports épargnés ». Aucun écran ne les lisait : elles
> étaient produites, stockées, et jetées. C'est le pire des trois silences de cette
> page — le manque de la spec (§*État physique*) fait chercher un câble pour un port
> coupé exprès, celui-ci laisse conclure « ce switch est segmenté » sur un switch qui
> ne l'est pas.
>
> Ce qui reste à faire est **dans la case** : rien ne distingue visuellement un port
> dont la configuration est inerte. Le motif diagonal que la spec réserve à
> l'admin-down est le bon vocabulaire, et le cas est de la même famille — « ce n'est
> pas ce que ça a l'air d'être ». Un bandeau nomme les ports ; une façade de 48 cases
> ne dit toujours pas *lesquelles*.

*(À ne pas confondre avec les trois autres natures de silence, qui se répondent :
`skipped` = meshcraft a REFUSÉ, et l'exploitant peut forcer ; `unsupported` = la
plateforme ne sait pas l'écrire, et il n'y a rien à forcer ; `notes` = c'est écrit,
et ça n'agit pas.)*

## Implémentation

**SVG par équipement** (un `<svg>` par façade, une `<g>` par port), pas de DOM par sous-élément :
- les stries = `pattern` SVG générés par combinaison de VLANs (mémoïsés — un même trunk profile réutilise son pattern) ;
- événements de survol/clic par délégation ; tooltip riche (nom, profil, VLANs nommés, vitesse, voisin, MACs) ;
- mise à jour temps réel : le message WS `port_state` ne re-render que la `<g>` concernée (React memo par port) ;
- un switch 48 ports = ~50 nœuds SVG → des centaines d'équipements par page restent fluides ; au-delà, virtualisation de la liste de cartes.

> [!warning] Le rendu livré est en DOM, et le raisonnement de performance ne tient plus
> Rien de cette section n'est construit. Le rendu réel :
>
> - **un élément DOM par port** — un `<button>` s'il est cliquable, un `<span>`
>   sinon — avec styles en ligne. Une façade de 48 ports = 48 éléments, pas une
>   `<g>` dans un `<svg>` ;
> - les stries sont un **`repeating-linear-gradient` CSS** à 135°, recalculé à
>   chaque rendu, sans mémoïsation ni `pattern` réutilisé ;
> - le survol et le clic sont des gestionnaires **par élément**, pas une
>   délégation ;
> - l'infobulle est un attribut `title` du navigateur : elle porte le nom, le
>   libellé et l'état de dérive. Ni profil, ni VLANs nommés, ni vitesse, ni
>   voisin, ni MACs — ces deux derniers n'existant de toute façon pas encore ;
> - il n'y a **pas de `React.memo` par port**, et il n'y a pas de message
>   `port_state` : le temps réel est un flux SSE qui annonce « ce site a changé »,
>   après quoi l'écran **recharge et re-rend la façade entière**
>   ([[Interface web]]) ;
> - aucune **virtualisation** de la liste de cartes.
>
> La conséquence n'est pas visible aujourd'hui — un site d'essai tient largement —
> mais l'argument « des centaines d'équipements par page restent fluides » était
> adossé au SVG et à la mise à jour par port. Avec un élément DOM par port et un
> re-rendu complet à chaque changement du site, un parc de deux cents switchs 48
> ports fait dix mille éléments re-rendus à chaque transition de port. Le premier
> symptôme sera l'inventaire, pas la fiche.
>
> Le choix du DOM n'est pas une erreur : il a permis d'obtenir la sélection
> multiple, le clignotement de dérive et l'accessibilité au clavier (un port est un
> vrai `<button>`) sans réimplémenter en SVG ce que le navigateur donne. C'est le
> **raisonnement de charge** qui doit être refait, et l'ordre à suivre est
> probablement l'inverse de la spec : mémoïser par port et virtualiser la liste de
> cartes d'abord — ce qui se fait en DOM — et ne passer au SVG que si ça ne suffit
> pas.

```
   up access V20    up trunk 20+30+99, natif 10     down
   ┌─────────┐        ┌─────────┐                 ┌─────────┐
   │█████████│        │▨▨▨▨▨▨▨▨▨│ stries          │         │ gris,
   │█████████│ vert   │▨▨▨●▨▨▨▨▨│ + rond natif    │         │ éteint
   └─────────┘        └─────────┘                 └─────────┘
```

*(Le rond central du deuxième cas est la cible ; il n'est pas dessiné aujourd'hui.)*

## Sous la façade : le panneau de TÉLÉMÉTRIE

Ce que la case de port ne peut pas porter s'affiche **sous** la façade, sur la fiche
équipement — et pas dans le panneau de droite, qui décrit le port sélectionné : ces
grandeurs décrivent l'**équipement**, et les mettre à droite les ferait disparaître dès
qu'on clique une prise.

| Vignette | Source | Absente quand |
|---|---|---|
| Température (une par capteur) | `meshcraft_temperature_celsius`, étiquetée `sensor` | la plateforme ne la publie pas |
| Charge processeur | `meshcraft_cpu_load_percent` | idem — relevé sur SwOS : le firmware ne la mesure pas |
| Mémoire utilisée | `memory_total − memory_free`, en % | idem |
| Temps de marche | `meshcraft_uptime_seconds` | rarement : tout agent SNMP le sert |
| Erreurs de trame | `rate(if_in_errors) + rate(if_out_errors)`, **sommé tous ports** | le canal de compteurs n'est pas collecté |

Trois courbes accompagnent les vignettes (température, processeur + mémoire sur une même
échelle de pourcentage, erreurs), sur une fenêtre réglable — 1 h, 6 h, 24 h. La fenêtre est
un état de la fiche et non de l'URL : c'est un réglage de lecture, pas un endroit où l'on
revient.

> [!important] TROIS ABSENCES SE DISTINGUENT, et un cadre vide n'en distingue aucune
> C'est la règle de ce panneau, et elle vient d'un constat d'exploitation : « pas de
> courbe » se lit comme « collecte en panne », donc l'écran doit dire **laquelle** des
> trois causes s'applique.
>
> | Ce qu'il n'y a pas | Ce que l'écran dit |
> |---|---|
> | aucun stockage de séries sur l'instance | une phrase, et aucun cadre — `MESHCRAFT_METRICS_URL` est vide, la collecte tourne mais rien n'est conservé |
> | la plateforme ne publie pas la grandeur | l'explication **localisée** pour la métrique déclarée dans `monitoring_unavailable` (`monitoring.unavailable.*`), avec la mention *déduit* quand l'absence n'est pas constatée sur matériel |
> | l'équipement n'est pas collecté | « aucune mesure » — pas de collecteur désigné, agent muet, tunnel coupé. Ce n'est pas un zéro |
>
> La deuxième ligne a demandé d'ÉLARGIR la granularité des motifs : ils étaient déclarés
> par canal (`health`, `if_counters`), et `health` couvre à la fois la température et la
> charge processeur. Or un CSS326 publie la première et pas la seconde — déclarer `health`
> indisponible aurait fait disparaître sa température. Les connecteurs déclarent donc
> désormais `cpu` et `memory` séparément, et l'écran cherche du plus précis au plus large.

La courbe de trafic d'un port, dans le panneau de droite, vient de la **même** route : les
compteurs d'octets dérivés par `rate()`, un point tous les 15 s au pas le plus fin. Elle
était fabriquée par un jitter autour de la valeur courante — trente points d'un passé qui
n'avait jamais été mesuré (cf. [[Weathermap]], le callout sur `jitAt`).

## Légende de façade

Sous la façade, une bande de pastilles dit ce que valent les couleurs et les motifs : accès sur lien actif, trunk (strié), configuré mais lien down, libre, PoE actif. Elle porte en outre, en tête, l'**aide de la sélection** — les quatre gestes de ⇧ et Ctrl/⌘ ([[Interface web]], § *Sélection de ports sur la façade*).

C'est délibérément **là** et non dans le panneau latéral, où ces raccourcis vivaient jusqu'ici en trois lignes de texte gris. Deux raisons : l'aide d'un geste doit être contre l'objet qu'il manipule, et le panneau latéral ne montre ce paragraphe que lorsque **rien** n'est sélectionné — c'est-à-dire au seul moment où l'on n'est pas en train de s'en servir. Le principe général — le texte d'explication vit derrière une aide, le constat reste à l'écran — est écrit dans [[Interface web]].

## Accessibilité & lisibilité

- La couleur n'est jamais seule : infobulle systématique — **livré** — et mode « étiquettes » (n° inscrit dans la case au zoom ≥ 24 px) — **livré, avec une différence** : le numéro apparaît automatiquement dès que la case atteint 24 px, donc uniquement sur la fiche du switch, et ce n'est **pas un mode** qu'on active. Et c'est le numéro de **port**, pas de VLAN. La distinction compte : sur une façade dense, le numéro de port aide à *nommer* ce qu'on clique, le numéro de VLAN aiderait à *lire* la configuration sans passer sur chaque case. Le second est celui qu'il manque.
- Palette VLAN générée en OKLCH à luminance contrainte — **décidé, non construit**. Ce qui est livré : **16 teintes fixes**, dont les **8 premières** ont été vérifiées à la main pour la vision des couleurs (séparation des paires voisines sur fond sombre). Un site jusqu'à 8 VLANs est donc lisible par construction ; au-delà, l'identité repose sur le numéro dans la case, l'infobulle et la légende. La couleur d'un VLAN est de toute façon stockée **par VLAN** en base : la palette ne fait que *proposer* la première teinte non utilisée, et l'exploitant peut en choisir n'importe quelle autre — auquel cas aucune garantie de contraste ne s'applique. C'est l'argument le plus fort pour la génération OKLCH : elle rendrait la garantie indépendante du choix humain.
- Légende des VLANs du site repliable en bas de page, cliquable pour **filtrer** : ne surligner que les ports portant tel VLAN — très efficace pour auditer « où passe le VLAN 30 ». **Livré au survol** : passer sur une ligne de la légende fait tomber à 12 % d'opacité tous les ports qui ne servent pas ce VLAN, sur toute la page. L'effet est exactement celui décrit, et il est immédiat.

  Ce qui manque est le **clic** : le filtre n'est pas verrouillable. Le composant accepte déjà un gestionnaire de clic sur la ligne, mais aucun écran ne le lui passe. Trois conséquences : on ne peut pas relâcher la souris pour aller regarder ailleurs, le filtre est inutilisable **au clavier**, et il est inutilisable **au tactile** — où il n'y a pas de survol du tout. Brancher le clic est une ligne par écran, et c'est ce qui rend la fonction accessible plutôt que simplement jolie.
