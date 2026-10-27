---
Statut: Accepté
Date: 2026-10-24
---
Note: Écrit sous le numéro 0007, renuméroté en 0009 à la fusion — 0007 était
  déjà pris par la décision sur les agrégats, écrite en parallèle. Un numéro
  n'est jamais réutilisé.
---

# ADR-0009 — Le balayage ne vole jamais un geste de défilement

## Contexte

Sous 720 px la coque s'inverse (UI-GUIDE §9.4) : les cinq destinations du
quotidien passent dans une barre basse, la barre latérale devient un tiroir.
Changer de page demande alors de viser une cible de 44 px en bas de l'écran,
ou d'ouvrir le tiroir, choisir, le laisser se refermer. Sur un téléphone tenu
d'une main devant une baie, le balayage latéral est le geste que tout le monde
essaie d'abord.

Ce qui rend la question non triviale : **cette interface défile déjà dans les
deux axes, et le défilement horizontal y est du contenu, pas de la
décoration.**

- `.scroll-cue` / `.scroll-x` (UI-GUIDE §9.2) : les tableaux larges gardent
  leur largeur réelle et défilent — `.ports-table` fait 860 px minimum,
  `.clients-table` 900 px, sur un écran de 393 px.
- La façade de la page équipement est un défileur horizontal **plein écran**
  qui ne s'enroule jamais (UI-GUIDE §9.6) : ses rangées sont les rangées
  physiques du boîtier, et la sélection par plage se mesure sur l'ordre
  physique. Un balayage qui lui prend son geste ne dégrade pas l'écran : il
  casse la seule chose que cet écran sait faire.
- L'axe vertical est encore pire : `.content-scroll` est la surface de
  défilement unique de **toutes** les pages.

Il n'existe donc aucun endroit de l'application où un axe soit libre par
construction. La question n'est pas « comment reconnaître un balayage » mais
« à qui appartient le geste ».

Deux autres faits cadrent la réponse. Un pavé tactile émet des gestes
horizontaux à deux doigts, qui arrivent en `wheel` avec `deltaX` — **indiscernables
d'un défilement de tableau large**, et déjà revendiqués par le retour-arrière
historique de Safari et de Chrome. Et le cliquet frontend interdit
`addEventListener("keydown")` hors de `ui/` : tout raccourci clavier passe par
`useGlobalKey`, qui ne transmet pas l'évènement et ne peut donc pas filtrer un
modificateur.

## Décision

**Un élément qui défile dans un axe possède cet axe.** Le reconnaisseur
(`web/src/ui/swipeDecide.ts`, câblé par `web/src/ui/swipe.ts`) est purement
observateur : il n'appelle jamais `preventDefault()`, donc un geste qu'il lit
de travers fait quand même ce que le navigateur aurait fait.

Le balayage horizontal parcourt les cinq onglets dans l'ordre de la barre ; le
balayage vertical parcourt les entrées de la barre latérale active
(configuration, ajout) dans l'ordre du menu. Les deux sont reconnus **sur la
zone de contenu seule**, jamais sur la barre latérale ni sur les barres de
navigation.

Les quatre arbitrages, nommément :

**1. L'axe horizontal se perd par ascendance, l'axe vertical par position.**
Si le geste *commence* dans un ascendant qui peut défiler horizontalement, la
navigation horizontale est morte pour tout le geste — quelle que soit la
position du défileur. Verticalement, c'est l'inverse : la navigation n'est
reconnue que si le défileur vertical le plus proche est **déjà sur le bord
qu'on pousse** (`scrollTop === 0` vers le bas, en butée vers le haut).

L'asymétrie est volontaire. Un bord horizontal, on le traverse en lisant un
tableau de 48 ports ; y déclencher une navigation ferait du même geste, au même
endroit, deux choses différentes selon la position de lecture. Un bord vertical,
on y arrive exprès, et la règle d'ascendance y interdirait la fonctionnalité
partout puisque toutes les pages défilent verticalement.

**2. Seuil et vélocité.** Verrou d'axe d'abord : à 10 px de mouvement, l'axe
dominant doit dépasser l'autre d'un facteur 1,7, sinon le geste est abandonné —
une diagonale est une hésitation. Puis deux chemins de validation : une
**distance** de `max(64 px sur écran étroit | 96 px au-delà, 12 % de l'axe)`, ou
une **chasse** de 32 px à 0,5 px/ms mesurée sur les 120 dernières
millisecondes. Un second doigt annule : un pincement n'est pas une navigation.

**3. La page ne suit pas le doigt.** Au franchissement du seuil, une pastille
de verre apparaît sur le bord visé et **nomme la destination** ; relâcher y va,
revenir sous le seuil l'annule. Ce que les gens veulent d'une page qui suit le
doigt, c'est pouvoir annuler et savoir où ils vont : la pastille donne les deux
sans transformer la page.

Transformer la page coûterait trois choses qu'on refuse : posséder son
`transform` et son `touch-action`, donc se battre avec les deux surfaces de
défilement ; monter deux routes à la fois ou photographier la sortante, sur des
écrans qui interrogent le réseau en continu ; et animer à chaque image une
façade de 48 ports. La pastille est un composite GPU sur un élément de 150 px.

**4. Tactile seulement, et le clavier reste ce qu'il est.** Le reconnaisseur
filtre sur `pointerType === "touch"`, n'écoute aucun `wheel` et n'ajoute aucune
liaison clavier. La voie clavier et lecteur d'écran du changement de page reste
celle qui existe : les liens de la barre d'onglets et de la barre latérale, dans
l'ordre de tabulation. Le balayage est strictement **additif** — c'est la
condition pour qu'il n'ait pas à être accessible lui-même.

`prefers-reduced-motion` : la pastille apparaît sans transition
(`--dur` vaut 0 ms sous cette préférence, tokens.css §9) et aucune page ne
glisse. Le mouvement n'était de toute façon pas le porteur de l'information —
UI-GUIDE §4 : *le mouvement n'est jamais le seul porteur*. La pastille porte un
texte.

## Conséquences

- **On ne peut pas changer d'onglet au balayage depuis la façade** de la page
  équipement, ni depuis un tableau large tant que le doigt y est posé. C'est le
  prix exact de la règle 1, et il est payé à l'endroit où le défilement
  horizontal est la fonction de l'écran. Le reste de la page reste sensible.
- **Un balayage vertical vers le bas en haut d'une page courte navigue.** Une
  page qui ne défile pas est en butée des deux côtés : les deux directions sont
  disponibles d'emblée. Voulu, et à retenir en concevant une page courte sous
  une barre latérale.
- **Le balayage horizontal est inerte dans `/config`** : aucun onglet n'y est
  sélectionné (l'engrenage possède la configuration, pas un onglet), donc il
  n'y a pas d'ordre à parcourir. Seul l'axe vertical y vit.
- **Une entrée marquée « sortante » (↗) est exclue de l'ordre vertical.** Le
  lien vers les identifiants globaux quitte la section ; un balayage ne doit pas
  faire sortir d'une zone par inadvertance. Le glyphe et l'exclusion disent la
  même chose.
- Le reconnaisseur lit `scrollWidth`/`scrollHeight` et un `getComputedStyle`
  par ascendant **au `pointerdown`**, jamais pendant le mouvement. Un coût de
  mise en page par geste, pas par image.
- Deux comportements existent désormais pour un même mouvement du doigt selon
  l'élément de départ. C'est une complexité réelle ; elle est inévitable dès
  qu'on superpose un geste de navigation à une interface qui défile, et la
  règle « qui défile possède » est la seule formulation qu'on puisse tenir à
  l'oral.

## Alternatives écartées

**Que la page suive le doigt.** La forme attendue, et la plus chère : il faut
posséder `touch-action` sur la zone de contenu, ce qui désarme les défileurs
qu'on vient de passer un ADR à protéger ; il faut monter la route voisine pour
avoir quelque chose à faire glisser, sur des écrans branchés sur des requêtes
réseau ; et il faut animer par image une façade qui est déjà le point chaud du
rendu. La pastille rend le seul service que le suivi rendait vraiment —
« je sais où je vais, je peux annuler » — pour un centième du coût.

**Le test de bord dans les deux axes** (naviguer quand le défileur horizontal
est déjà en butée). Séduisant par symétrie, et c'est ce qui fait qu'un même
balayage, au même endroit de la façade, défile ou navigue selon la position de
lecture. Un geste dont l'effet dépend d'un état invisible n'est pas apprenable.

**Exiger deux doigts** pour naviguer. Résout le conflit d'un trait, et personne
ne le découvre : un geste à deux doigts n'a aucune affordance, et sur iOS il est
déjà revendiqué par VoiceOver.

**Prendre aussi le pavé tactile** (`wheel` avec `deltaX`). Un défilement
horizontal à deux doigts sur un tableau large est bit pour bit le même
évènement qu'un balayage de navigation ; on perdrait la lecture des tableaux
larges sur le poste de travail pour gagner un geste qui, là, se fait déjà au
clic sur un onglet visible. Safari et Chrome revendiquent en plus cet axe pour
l'historique.

**Un raccourci clavier `Alt+Flèche` ou `Flèche` nue.** `useGlobalKey` ne
transmet pas l'évènement : impossible d'exiger un modificateur. Une liaison sur
`ArrowLeft` nu entrerait en collision avec la navigation au clavier de la façade
et des tableaux, et `Alt+Flèche` est l'historique du navigateur. La barre
d'onglets et la barre latérale sont déjà des liens focalisables : la voie
clavier existe, il ne faut pas lui en superposer une qui casse les autres.

**Gater le balayage sur `useBreakpoint() === "narrow"`.** Une tablette de
1024 px tactile est un poste de terrain réel et n'a pas de raison d'en être
privée. La largeur sert à autre chose ici : elle fixe le plancher du seuil
(64 px étroit, 96 px au-delà), parce qu'un glissement accidentel de 70 px est
banal sur une grande dalle.
