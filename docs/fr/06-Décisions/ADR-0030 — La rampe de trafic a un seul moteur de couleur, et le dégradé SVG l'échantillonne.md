---
Statut: Accepté
Date: 2026-10-25
---

# ADR-0030 — La rampe de trafic a un seul moteur de couleur, et le dégradé SVG l'échantillonne

## Contexte

ADR-0003 fixe la rampe (logarithmique, `K = 50`, six arrêts, lue contre la
vitesse de ligne) et ADR-0026 l'a étendue aux deux moitiés d'un lien de
topologie. La règle est donc arbitrée. Pourtant la demande « la couleur des
liens doit reprendre la logique des couleurs des graphes » est revenue **après**
la livraison d'ADR-0026 : à l'écran, les deux ne se superposaient pas tout à
fait.

Les trois mesures faites avant d'écrire ceci, sur l'instance et sur le site
ARENA :

1. **Les primitives étaient déjà communes.** `trafficLevel`, `trafficRateAt` et
   `TRAFFIC_STOPS` vivaient dans `network.ts` et servaient les deux côtés. Il ne
   restait **aucune** rampe privée : `loadColor` est bien morte avec ADR-0026.
   Ce n'était donc pas « deux rampes ».

2. **Les ENTRÉES concordent.** La moitié de lien lit `tx_mbps` de l'inventaire
   (`portrates.go` : lissé sur deux minutes, rafraîchi toutes les 30 s), le
   graphe lit la série de télémétrie (pas de 60 s, fenêtre 24 h). Relevé sur les
   15 ports chargés de `core01-arena` : 43,0 contre 49,3 — 57,2 contre 57,7 —
   146,8 contre 147,5 Mb/s. Soit **au pire 3 % d'écart de position sur la
   rampe**. Ce n'est pas là que ça se joue.

3. **C'est le RENDU qui divergeait, et il y en avait deux.**
   - `trafficColor` rend une couleur unique : `color-mix(in oklab, …)` entre les
     deux arrêts encadrants, interpolée linéairement en **`t`** — le paramètre
     de la rampe ;
   - `TrafficGradient` pose les **six** arrêts à l'offset `trafficRateAt(i/5)/
     capacité`, et SVG interpole entre deux arrêts linéairement en **offset**,
     c'est-à-dire linéairement en **débit**, et en **sRGB**.

   Les deux coïncident exactement *sur* les six arrêts et dérivent entre eux,
   parce que la rampe est convexe là où le dégradé est droit. Mesuré sur un port
   à 1 G, par balayage de 0 à 1 Gb/s : écart maximal **ΔE(oklab)×100 = 1,60** en
   thème sombre, **1,40** en clair, au pire vers 10 Mb/s.

Ce 1,6 est à la limite du perceptible, et il n'explique pas à lui seul
l'impression de départ : sur ARENA, `core01-arena` est un 10 G, donc 147 Mb/s
valent 1,5 % de la ligne et **les douze moitiés de lien du site tiennent dans la
première bande** (`--traffic-0` → `--traffic-1`, de 0 % à 72 %). Un trait qui
pousse 147 Mb/s et un trait qui en pousse 0,3 sont deux bleus. C'est le
fonctionnement voulu par ADR-0003 — « l'utilisation réelle vit dans les premiers
pourcents » — et non un défaut à corriger ici.

Reste qu'un produit qui calcule **deux fois** la même couleur par deux chemins
différents finira par les faire diverger pour de bon. Le 1,6 d'aujourd'hui est
le symptôme, pas la maladie.

## Décision

**Il y a un seul moteur de couleur de trafic, et c'est `trafficColorAt(t)`.**
Tout le reste en dépend :

1. **`trafficColorAt(t)` est l'autorité.** Une position `t` sur la rampe, une
   couleur. `trafficColor(mbps, capacité)` n'est plus qu'une composition :
   `trafficColorAt(trafficLevel(mbps, capacité))`.

2. **Le dégradé SVG n'est plus une seconde définition, c'est un
   échantillonnage de la première.** `trafficRampStops(capacité)` rend les
   arrêts en appelant `trafficColorAt`, et **subdivise chaque bande en 4** : 21
   arrêts au lieu de 6. L'interpolation sRGB-linéaire-en-débit de SVG ne travaille
   plus que sur un quart de bande, où la rampe est quasi droite. Écart maximal
   ramené à **ΔE 0,17** (sombre) et **0,15** (clair), soit **dix fois moins**.

3. **Une seule lecture de capacité pour la rampe.** `linkCapacityMbps` de
   `topologyLinks.ts` n'est plus un analyseur : il délègue à `speedMbps`. Les
   deux tombaient d'accord sur tout le parc réel (`""`, `1G`, `10G`, `10M`,
   `100M`), mais pas en théorie — sur une chaîne sans unité, `linkCapacityMbps`
   devinait l'unité par la magnitude (`n >= 10 ? "g" : "m"`), donc `"1000"`
   valait **1 Tb/s** pour un lien et 1 Gb/s pour un graphe. Un facteur 1000 sur
   l'ancre de la rampe aurait cloué la moitié de lien au bas du bleu pour
   toujours. `speedMbps` lit désormais un nombre nu comme des Mb/s, ce qui est la
   convention des champs `link`.

4. **La rampe déménage dans `trafficRamp.ts`, qui n'importe rien.** C'est la
   condition pour que `topologyLinks.ts` — dont l'absence d'import est
   porteuse, puisque la suite `node --experimental-strip-types` ne sait charger
   ni React ni i18n — puisse partager le parseur plutôt que d'en garder une
   copie. `network.ts` ré-exporte, donc aucun appelant ne bouge.

**Ce qui reste légitimement différent est l'ENTRÉE, pas la fonction.** Une
moitié de lien colore un instant (l'inventaire lissé sur 2 min), une courbe
colore une série (des seaux de 60 s). Les deux passent par `trafficColorAt`, et
c'est le seul écart qui subsiste — mesuré à 3 %, et assumé : il faudrait une
requête télémétrie par équipement sur le plan de site pour l'effacer, ce que le
survol paie déjà pour un équipement à la fois.

## Conséquences

- **Le prix est en nœuds DOM : 21 `<stop>` par dégradé au lieu de 6.** Sur la
  page Ports, 52 pastilles de courbe font passer 312 nœuds à 1 092. C'est le
  coût assumé de l'unification ; la subdivision plafonne à 4 pour cette raison
  et pas pour une raison de couleur — au-delà, le gain stagne (ΔE 0,13 à S=6)
  parce que le reste vient de l'arrondi au pourcent entier de `color-mix`.
- **Cet arrondi est maintenant partagé.** `Math.round(frac * 100)` était dans
  `trafficColor` ; il est dans `trafficColorAt`, donc les deux côtés quantifient
  à l'identique au lieu d'arrondir chacun pour soi.
- **ADR-0003 et ADR-0026 ne sont pas rejugés.** L'échelle, `K = 50`, les six
  arrêts, l'arrêt au citron, la lecture contre la vitesse négociée, les deux
  moitiés, le gris `?` d'une moitié sans mesure, les pointes réservées aux liens
  qui acheminent : tout tient. Cet ADR ne parle que du chemin par lequel une
  position sur la rampe devient une couleur.
- **`lowBandwidth.ts` garde SON `speedMbps`, et ce n'est pas un oubli.** Il rend
  `null` sur une chaîne illisible là où celui de la rampe retombe sur 1 G, et son
  commentaire dit pourquoi : « une vitesse inconnue ne doit pas être signalée
  comme basse », car la tuile envoie quelqu'un dans un rack. Fusionner les deux
  échangerait un faux froid sur une courbe contre un faux déplacement. Deux
  usages, deux contrats, et la différence est écrite des deux côtés.
- **Le dégradé reste ancré en espace utilisateur sur la capacité.** La
  subdivision change la densité des arrêts, pas l'ancre : une courbe qui plafonne
  à 5 % reste bleue, et deux ports côte à côte restent comparables.

## Alternatives écartées

**Ne rien faire : 1,6 ΔE est invisible.** Vrai aujourd'hui, et c'est bien
pourquoi cet ADR ne prétend pas corriger un bug visible. Mais le produit
contenait deux définitions de la même couleur, dont une seule était citée par
les ADR ; la prochaine retouche de l'une les aurait écartées pour de bon, et
personne n'aurait su laquelle était la règle.

**Faire interpoler `trafficColor` linéairement en débit, pour qu'il colle au
dégradé.** Aligne les deux au prix de la rampe elle-même : la position sur la
rampe est `t`, et une couleur qui avance linéairement en débit entre deux
arrêts re-linéarise par morceaux l'échelle qu'ADR-0003 a rendue logarithmique
exprès. C'est aligner la règle sur son approximation.

**Garder six arrêts et accepter la dérive.** C'est l'état d'avant, et c'est
l'alternative qu'un lecteur pressé proposera en voyant 21 arrêts dans le DOM.
Elle laisse l'écart là où il est et, surtout, elle laisse le dégradé **définir**
une couleur au lieu de l'échantillonner — donc deux autorités.

**Subdiviser beaucoup plus (S = 12, 61 arrêts).** Le gain s'arrête (ΔE 0,12
contre 0,17 à S=4) parce que le plancher n'est plus l'interpolation mais
l'arrondi au pourcent. Trois fois les nœuds pour 0,05 ΔE.

**Rendre la couleur en JS et peindre la courbe en segments.** Supprimerait le
dégradé et donc toute approximation. Mais une courbe lissée devient alors N
chemins au lieu d'un, `smoothSegments` perd ses tangentes continues aux
jointures, et on échange 21 `<stop>` contre des dizaines de `<path>`.

**Déplacer la rampe dans `network.ts` et faire importer `network.ts` par
`topologyLinks.ts`.** Le plus court en diff, et ça casse la suite de tests :
`network.ts` importe `./i18n`, que `node --experimental-strip-types` ne charge
pas. C'est précisément la raison pour laquelle `topologyLinks.ts` n'importait
rien.
