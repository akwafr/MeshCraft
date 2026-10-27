---
Statut: Précisé par ADR-0031
Date: 2026-10-25
Remplace: ADR-0013
---

# ADR-0019 — Le sélecteur ne porte que la marque, le mode se lit dans la barre

## Contexte

[[ADR-0013 — Commutation et routage sont deux modes de navigation, pas deux
produits]] reste juste sur tout sauf un point, et ce point est son §5 :

> **La couleur ne porte jamais le mode à elle seule.** Le sélecteur est un vrai
> contrôle : il a un **libellé textuel** (« Commutation » / « Routage »), il est
> atteignable au clavier, il annonce son état.

Construit ainsi, le sélecteur affiche en permanence les deux mots de part et
d'autre de la marque. À l'usage, deux choses :

1. **C'est le plus gros objet de la barre haute**, dans une barre qui débordait
   déjà entre 760 et 820 px avant qu'on y ajoute quoi que ce soit, et qui a
   fallu geler enfant par enfant pour tenir de 721 à 1440.
2. **Le libellé n'était pas le seul indice non-coloré, ni même le principal.**
   C'est le raisonnement qui manquait à ADR-0013 : en basculant, *la barre
   d'onglets change entièrement de contenu* — Équipements, Ports, Topologie,
   Clients d'un côté ; Routage, Pare-Feu, DNS, DHCP de l'autre. Quelqu'un qui
   ne distingue pas la teinte de la marque lit quand même, en clair et sans
   survol, dans quel mode il se trouve.

ADR-0013 a donc protégé un principe correct avec un moyen redondant, et payé ce
moyen au prix fort sur l'écran le plus contraint du produit.

## Décision

**Le sélecteur ne contient que la marque meshcraft. Les deux libellés
deviennent des survols.**

1. **Le principe d'ADR-0013 §5 est conservé, pas abandonné** : la couleur ne
   porte jamais le mode à elle seule. Ce qui change est **qui** le porte — la
   **barre d'onglets**, dont le contenu est l'information, et non deux mots
   collés à un bouton.
2. **Les libellés restent atteignables**, en survol et au focus clavier, par
   `QuickTip` — jamais un `title=` natif.
3. **Les noms accessibles ne disparaissent pas.** Le contrôle garde son
   `radiogroup` et ses deux `radio` avec leurs noms accessibles : un lecteur
   d'écran annonce toujours « Commutation » et « Routage » et lequel est actif.
   Un libellé visuellement retiré n'est pas un libellé supprimé.
4. **Le geste ne change pas** : glisser bascule, cliquer ramène au tableau de
   bord, avec le seuil et la dominance d'axe déjà mesurés.

## Conséquences

- La barre haute récupère la largeur des deux mots, sur l'écran où elle
  manquait le plus. C'est le bénéfice direct et le seul mesurable tout de suite.
- **Un utilisateur qui découvre le produit ne sait plus que le sélecteur est un
  sélecteur** tant qu'il ne l'a pas survolé. C'est le prix, et il est réel :
  l'affordance repose désormais sur la forme du contrôle et sur le fait que la
  barre change, pas sur un mot. À surveiller à la première prise en main
  observée.
- **Le survol n'existe pas au doigt.** Sur mobile, les libellés ne seront
  jamais lus : seule la bascule de la barre informe. C'est acceptable parce que
  c'est justement là que la barre d'onglets est la plus visible — mais ça veut
  dire que le point 1 n'est pas un filet de sécurité, c'est **le** mécanisme.
- ADR-0013 reste la référence pour tout le reste : l'URL fait autorité (§2), le
  mode est une préférence de visiteur (§3), le rouge est un jeton dédié et
  jamais `--danger` (§4), chaque écran déclare son mode.

## Alternatives écartées

**Garder les libellés et rétrécir ailleurs.** On l'a déjà fait une fois — la
barre ne tient de 721 à 1440 qu'au prix d'un gel enfant par enfant et d'un seul
élément autorisé à tronquer. Reprendre de la place à un autre pour financer
deux mots redondants est un mauvais échange.

**N'afficher que le libellé du mode actif.** Moitié du coût, moitié du
bénéfice, et le contrôle cesse d'annoncer qu'il a deux positions — ce qui est
la seule chose que le libellé apportait vraiment.

**Un libellé visible sous le seuil mobile seulement.** Exactement à l'envers :
le survol n'existe pas au doigt, donc ce serait montrer le texte là où la place
manque et le cacher là où elle abonde.

**Ne rien changer.** Défendable, et c'est ce qu'ADR-0013 disait. Écarté parce
que son §5 traite le libellé comme le garant du principe, alors que le garant
réel est la barre d'onglets — et qu'on paie un garant redondant sur l'écran le
plus contraint.

## Addendum du 2026-10-25 — ce que la mesure a corrigé

Quatre affirmations de cet ADR ont été vérifiées après coup. **Une était
fausse, deux étaient trop pessimistes, une était périmée.** Elles sont
corrigées ici plutôt que retouchées plus haut.

**« Elle ne tient de 721 à 1440 qu'au prix d'un gel enfant par enfant » —
FAUX. Elle ne tenait pas.** Balayage tous les 4 px, barre de commutation en
français : **74 largeurs sur 120 débordaient**, jusqu'à **143 px à 721**. Le
sélecteur n'était pas la cause, il était un aggravant. Après ce changement il
en reste **19 sur 120** (721–773, ≤ 53 px), et la barre de routage passe de
21 à **0**. Donc ce travail **améliore un défaut préexistant, il ne le ferme
pas** — et le moteur réel est le groupe d'identité plus six onglets.

Largeur intrinsèque, pire cas (français, commutation) : **1314 → 1180 px**.
La pastille elle-même : **208 → 74 px**.

**« Le survol n'existe pas au doigt, les libellés ne seront jamais lus » —
trop pessimiste.** `QuickTip` s'épingle sur pointeur grossier : une tape
bascule le mode *et* laisse le libellé à l'écran jusqu'à la suivante. Le
point reste vrai qu'on ne peut pas *survoler* pour apprendre avant d'agir.

**« Garder les libellés et rétrécir ailleurs » — l'alternative était écartée
pour la mauvaise raison.** Les trois relâchements ont été essayés : restaurer
le nom affiché rouvre 968–1152 (10 → 31 sur 90) ; restaurer la gouttière
`mid` creuse 721 de −50 à −70 px ; et **`flex-shrink: 1` ne change
strictement rien** — le minimum automatique d'un élément flex est sa largeur
min-content, et tous les enfants sont des mots insécables. Le gel est donc
une déclaration d'intention, pas le mécanisme : le seul vrai levier est le
plancher de 112 px du sélecteur de site, déjà dépensé.

**L'énumération du contexte est périmée** : c'est 6 contre 5, pas 4 contre 4
— le tableau de bord est dans les deux modes et le Wi-Fi a rejoint la
commutation (ADR-0016). L'argument tient a fortiori.

Enfin, un manque que ce travail a révélé et comblé : **`QuickTip` n'avait
aucun déclencheur au focus**. Sans lui, remplacer un libellé permanent par un
survol aurait été une régression déguisée en gain de largeur, et le point 3
de cet ADR aurait été faux au clavier.
