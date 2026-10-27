---
Statut: Accepté
Date: 2026-10-24
Note: Consigné après coup, comme ADR-0001.
---

# ADR-0003 — La rampe de trafic est logarithmique et relative à la capacité

## Contexte

La colonne « Trafic » de la page Ports affichait `12.4 Mb/s / 3.1 Mb/s` : deux
nombres, aucun historique, et **aucun moyen de distinguer un gigabit chargé
d'un dix-gigabit au repos**.

## Décision

Une **rampe de magnitude** à six arrêts, du bleu au lime, lue contre la
**vitesse de ligne du port**, sur une échelle **logarithmique** :

```
t = log1p(débit / capacité × 50) / log1p(50)
```

Elle colore une **courbe** et rien d'autre. Jamais une pastille, une bordure,
un texte de statut.

Calée sur des lignes réelles d'un parc en gigabit : 13 Mb/s → 0,13 ·
124 Mb/s → 0,50 · 387 Mb/s → 0,77.

## Conséquences

- La rampe **s'arrête au lime**, pas au rouge. Le rouge et l'orange
  appartiennent à `--danger` et `--warn`, et un uplink saturé n'est pas une
  panne : c'est un lien qui fait son travail.
- C'est une **troisième palette non-statut**, après les VLAN et les teintes de
  règles. Ce qui la sépare de la colonne vertébrale est **où** elle apparaît :
  les encres de statut colorent des pastilles, des bordures, du texte ; celle-ci
  ne colore qu'un tracé.
- Le dégradé est ancré en **espace utilisateur sur la capacité**, pas sur la
  plage dessinée : une courbe qui plafonne à 5 % reste bleue au lieu d'être
  étirée jusqu'au lime parce que 5 % était son propre maximum. Deux ports côte
  à côte restent comparables.
- La **hauteur** de la courbe reste relative à son propre pic. Hauteur = forme,
  couleur = charge. Un plancher lié à la capacité a été essayé puis retiré : il
  écrasait chaque port d'accès contre le bas de sa case.
- Sur le tableau de bord il n'y a pas de vitesse de ligne — le plafond de l'axe
  en tient lieu. « Chaud » y veut dire « le haut de ce que ce site a fait sur
  cette fenêtre », ce qui est la seule affirmation honnête disponible.

## Alternatives écartées

**Une échelle linéaire.** L'utilisation réelle vit dans les premiers pourcents
quasiment tout le temps : tout le parc serait dans la première couleur et les
cinq autres réservées à un incident.

**Une couleur absolue en Mb/s.** Peindrait chaque port d'accès en froid et
chaque uplink en chaud — ce que l'opérateur sait déjà.

**Normaliser par graphique.** Chaque courbe atteindrait le lime à son propre
pic, et deux ports ne seraient plus comparables.
