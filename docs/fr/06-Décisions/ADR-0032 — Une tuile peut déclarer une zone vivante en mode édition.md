---
Statut: Accepté
Date: 2026-10-25
---

# ADR-0032 — Une tuile peut déclarer une zone vivante en mode édition

## Contexte

La grille du tableau de bord (`web/src/dashboardGrid.tsx`) a **deux modes**, et
la frontière entre les deux est une règle d'un seul mot : en mode édition, le
contenu d'une tuile **ne reçoit plus d'événements de pointeur**.

```css
.dash-grid-tile--arrangeable .dash-grid-tile-clip {
  pointer-events: none;
}
```

La raison est bonne et il faut la garder écrite : le geste de déplacement est un
`pointerdown` sur la **tuile entière**. Si le contenu restait vivant, chaque
clic sur une courbe, sur un lien d'équipement, sur une puce de fenêtre
deviendrait un déplacement d'un pixel — et inversement, chaque amorce de
glissement partant du milieu d'une tuile serait avalée par le contrôle qui s'y
trouve. Les deux ne peuvent pas être vrais au même endroit en même temps. La
règle est scopée à `--arrangeable`, donc empilé (sous 720 px, où il n'y a pas de
plan et donc pas de glissement) le contenu reste vivant : ce n'est pas une
règle sur le *mode*, c'est une règle sur le *geste*.

Conséquence directe, assumée il y a quelques heures par ADR-0027 : la tuile
« Trafic d'un port » montre ses deux sélecteurs **hors** mode édition. C'était
obligatoire, pas esthétique — un sélecteur qui n'existe qu'en édition est un
sélecteur que personne ne peut ouvrir. Le commentaire du composant le dit
exactement comme ça.

Et c'est **le mauvais produit**. Un tableau de bord qu'on consulte affiche des
mesures, pas des formulaires ; la tuile « Trafic d'un port » consacre
aujourd'hui soixante-dix pixels de chaque affichage à deux listes déroulantes
qu'on ouvre une fois dans la vie de la tuile. Trois autres tuiles portent le
même défaut en plus petit (les puces de fenêtre du trafic de site, le filtre de
sévérité du journal). La demande — « que ce soit un réglage de tuile pendant le
mode édition, mais que le formulaire soit caché à l'affichage simple » — est la
bonne ergonomie, et elle est **incompatible avec la règle actuelle telle
qu'elle est écrite**.

La question n'est donc pas « où mettre un bouton ». C'est : **la grille a-t-elle
le droit de laisser une partie d'une tuile vivante pendant qu'on arrange le
plan, et si oui, laquelle ?**

## Décision

**L'inertie reste le défaut. Une tuile peut en déclarer UNE exception, et cette
exception est une zone de réglages.**

Le contrat s'appelle `dash-tile-live` (`web/src/dashboardTileEdit.tsx`, qui
porte la classe, le composant qui l'applique et le prédicat que la grille
interroge). Une tuile qui l'utilise obtient trois choses, et il faut les trois —
une seule manque et le formulaire est inutilisable ou vole le geste :

1. **Les événements de pointeur reviennent**, par un
   `pointer-events: auto` sur la zone, qui annule localement le `none` de
   l'ancêtre. C'est la moitié facile.
2. **La zone avale `pointerdown`.** Sans ça l'événement remonte jusqu'à la
   racine de la tuile, dont le `onPointerDown` démarre un déplacement : le clic
   d'ouverture du sélecteur décalerait la tuile d'une cellule. C'est exactement
   ce que font déjà les deux boutons « renommer » et « retirer », pour la même
   raison — la zone vivante ne fait que généraliser un motif que la grille
   pratiquait sur ses propres contrôles.
3. **Le clavier de la grille se retire.** En édition, les flèches déplacent la
   tuile, Maj+flèches la redimensionnent et `Suppr` la **supprime**. Dans un
   champ de recherche, ces trois touches veulent dire déplacer le curseur,
   sélectionner, et effacer un caractère. `Suppr` est le cas grave : taper puis
   corriger dans le champ de recherche d'un sélecteur **effacerait la tuile**.
   La grille cède donc toute touche dont la cible est à l'intérieur d'une zone
   vivante.

Trois règles bornent ce droit, et elles sont la décision autant que le
mécanisme :

- **Une zone vivante porte des réglages, jamais du contenu.** Elle n'existe
  qu'en mode édition et disparaît à l'affichage. C'est ce qui rend l'exception
  sûre : on ne rouvre pas la porte au « clic sur une courbe = déplacement d'un
  pixel », parce que la courbe n'est pas dans la zone.
- **Une seule par tuile, en haut du corps.** Pas sur la bordure droite ni dans
  le coin inférieur droit, qui appartiennent à la poignée de
  redimensionnement et à la paire d'outils. Une zone qui recouvrirait la
  poignée retirerait le redimensionnement à la souris, et ce serait silencieux.
- **Elle reste découpée par la cellule.** Une zone vivante est du contenu de
  tuile ; `overflow: hidden` continue de s'appliquer. Une liste déroulante plus
  haute que ce qui reste de la cellule est coupée — la carte défile, comme elle
  le fait déjà pour un tableau trop long. On ne fait pas déborder une tuile hors
  de son carreau pour un formulaire.

## Conséquences

- **Le réglage se voit pendant qu'on le règle.** C'est le gain qui a fait
  préférer cette solution à une boîte de dialogue : la zone est au-dessus du
  graphique, dans la même tuile, donc inverser IN/OUT ou changer la fenêtre
  redessine la courbe sous les yeux de l'exploitant. Un réglage dont on ne voit
  pas l'effet est un réglage qu'on applique deux fois pour comprendre.
- **`Suppr` ne détruit plus une tuile pendant qu'on tape.** C'est un bug qui
  n'existait pas encore — rien n'était focusable en édition — et qui serait
  arrivé avec le premier champ. Le garde est posé dans la grille, une fois,
  pour toutes les tuiles présentes et futures.
- **Les trois autres tuiles à réglages peuvent suivre, et ce n'est pas fait
  ici.** Les puces de fenêtre du trafic de site et le filtre de sévérité du
  journal restent visibles hors édition. Les déplacer est désormais possible et
  reste un choix par tuile : la fenêtre du trafic de site est une lecture
  qu'on change plusieurs fois par visite, pas un réglage qu'on pose une fois.
  **La règle ne dit pas « tout réglage va en édition »**, elle dit « un réglage
  *peut* y aller ».
- **Le mode édition gagne du contenu, donc de la hauteur.** La zone vivante
  prend de la place dans une cellule déjà dimensionnée. Le graphique de la
  tuile devient donc élastique plutôt que de hauteur fixe, sinon la zone le
  pousse hors du carreau. C'est une amélioration indépendante, que cette
  décision rend obligatoire.
- **Rien à migrer, rien sur le fil.** Les réglages voyagent dans
  `dashboard_tiles.options` par la route qui existe (ADR-0027), et une zone
  vivante est une classe CSS plus un garde clavier. Un ancien tableau de bord
  s'affiche à l'identique, simplement sans ses formulaires.
- **Échap appartient toujours au calque.** Un sélecteur ouvert dans une zone
  vivante s'enregistre dans la pile de `useDismissable` : Échap ferme le
  sélecteur, pas le mode édition. Aucune règle nouvelle, mais c'est la première
  fois qu'un calque s'ouvre *dans* une tuile en édition, et il faut que ce soit
  vérifié plutôt que supposé.

## Alternatives écartées

**Garder les sélecteurs hors édition, comme ADR-0027.** Le statu quo, et il a
le mérite d'être déjà écrit et déjà testé. Écarté parce qu'il fait payer à
**chaque consultation** le prix d'un geste qu'on fait une fois : sur un mur
d'écran, la tuile « Trafic d'un port » montre en permanence deux listes que
personne n'ouvrira plus. Un tableau de bord qui affiche ses formulaires est un
tableau de bord qu'on ne peut pas montrer.

**Un `Modal` de configuration, ouvert depuis la paire d'outils de la tuile.**
C'était la solution la plus facile et elle ne touche à aucune règle : le dialogue
de renommage fait déjà exactement ça, hors du plan, hors du découpage, sans
aucun conflit de geste — et elle réglait au passage le découpage des listes
déroulantes. Écartée pour deux raisons. D'abord **l'effet du réglage disparaît
pendant qu'on le règle** : le dialogue recouvre la tuile, donc « Inverser
IN/OUT » et le changement de fenêtre se jugent de mémoire, après fermeture.
Ensuite elle **crée un deuxième endroit où vivent les réglages d'une tuile** :
le renommage dans un dialogue, la cible dans un autre, les puces de fenêtre
dans la tuile — trois conventions pour un même mode. La zone vivante n'en crée
aucune nouvelle, elle généralise celle des outils de tuile.

**Retirer `pointer-events: none` purement et simplement.** La lecture naïve de
la demande, et celle qu'il faut nommer pour l'écarter : la règle n'existe pas
seulement pour le glissement. Elle empêche aussi qu'un clic sur un lien
d'équipement du tableau de flotte **quitte la page** au milieu d'un
réarrangement, avec une disposition non enregistrée. Le contenu d'une tuile
n'est pas inerte par timidité.

**Un seuil de déplacement en pixels** — un `pointerdown` ne devient un
glissement qu'au-delà de quatre ou cinq pixels, en dessous c'est un clic qui
part au contrôle. C'est la solution qu'emploient les bibliothèques de
glisser-déposer, elle est honnête, et elle rendrait *tout* le contenu vivant en
édition. Écartée pour trois raisons cumulées : elle demande une machine à états
de geste là où il y a aujourd'hui trois lignes sans état ; elle ne protège pas
du départ de page décrit juste au-dessus, puisqu'un clic court resterait un
clic ; et elle vole les premiers pixels de tout défilement interne à une tuile,
donc précisément le geste qu'on fait dans une liste déroulante. On paierait une
complexité générale pour obtenir moins que la zone déclarée.

**Faire déborder la tuile pendant qu'un sélecteur est ouvert**
(`overflow: visible` conditionnel, par `:has()`, plus un `z-index` sur la
tuile). Tentant, parce que le seul vrai défaut restant est une liste coupée par
le carreau. Écarté comme une porte à ne pas ouvrir : la discipline du plan tient
entièrement à ce qu'une tuile ne dépasse jamais de sa cellule, et une exception
« seulement quand un calque est ouvert » est une exception que la prochaine
tuile étendra. La carte défile déjà ; c'est le comportement qu'un tableau trop
long a toujours eu.
