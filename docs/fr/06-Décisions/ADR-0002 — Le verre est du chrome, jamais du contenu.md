---
Statut: Accepté
Date: 2026-10-24
Note: Consigné après coup, comme ADR-0001.
---

# ADR-0002 — Le verre est du chrome, jamais du contenu

## Contexte

La demande était un équivalent « liquid glass », responsive. Le glassmorphisme
mal fait est lui-même un cliché — fonds flous violets partout — et le produit
affiche des **façades de ports** dont chaque couleur porte un sens arbitré en
un seul endroit (`network.ts`).

## Décision

Le verre s'applique à la **chrome et aux contrôles** : barre haute, barre
d'onglets, barre latérale, tiroir, modale, popover, boutons secondaires.
**Jamais** à une carte, un tableau, une façade.

Trois couches, et la troisième est celle qu'on oublie : une teinte
translucide, un `backdrop-filter: blur() saturate()`, et un **liseré
spéculaire** — `inset 0 1px 0` sur l'arête haute. Le flou seul donne du
brouillard, pas du verre.

Le bouton **primaire reste opaque**.

## Conséquences

- Un carré de port vu à travers `blur(24px)` à 82 % n'est plus le vert
  `--ok` : c'est *un* vert. Le contenu reste opaque pour que la couleur
  continue de vouloir dire ce que `network.ts` a dit.
- La teinte est **plus haute qu'attendu** (0,82 sombre, 0,86 clair). À 0,68
  une façade de 48 ports se lisait à travers la barre d'onglets mobile : ce
  n'est plus de la profondeur, c'est un défaut de rendu. Le clair a besoin de
  **plus** que le sombre, à l'inverse de l'intuition, parce que le vert saturé
  qui transparaît est bien plus bruyant sur une teinte blanche.
- Trois replis obligatoires, qui comptent plus que l'effet :
  `@supports not (backdrop-filter)` → opaque ; `prefers-reduced-transparency`
  → opaque ; `forced-colors` → `Canvas`, sans flou ni liseré.
- La profondeur passe par l'élévation, donc les hairlines de 1 px disparaissent
  des cartes. Une page de rectangles bordés se lit comme le *filaire* d'une
  page — c'est l'essentiel de ce qui fait « généré ».

## Alternatives écartées

**Du verre partout, cartes comprises.** C'est l'esthétique attendue, et elle
casse la seule chose que ce produit ne peut pas casser : la fiabilité d'une
couleur de statut.

**Du verre nulle part, juste des ombres.** Sûr, et à côté de la demande.

**Un fond dégradé marqué pour que le verre réfracte quelque chose.** Écarté au
profit de deux lavis radiaux à 4-7 %, sous le seuil de « il y a un dégradé » et
au-dessus de celui de « cette surface a de la profondeur ».
