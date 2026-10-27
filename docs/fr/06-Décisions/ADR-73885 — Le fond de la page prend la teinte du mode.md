---
Statut: Accepté
Date: 2026-10-27
---

# ADR-73885 — Le fond de la page prend la teinte du mode

## Contexte

Le fond de l'application est bleu. Ce n'est pas l'accent : c'est l'échelle
des neutres — `--bg`, `--bg-panel`, `--bg-hover`, `--border` — tous à la
teinte OKLCH ≈ 270°, et les deux nappes lumineuses de `body::before`
(`--wash-a` indigo, `--wash-b` sarcelle). Ce bleu ne change pas avec le mode.

Le rouge du mode routage existe depuis
[[ADR-0013 — Commutation et routage sont deux modes de navigation, pas deux produits]]
§4 : un jeton dédié, `--mode-routing`, cramoisi, mesuré à ΔE 31 de
`--danger`. Aujourd'hui il ne colore que la marque, dans le sélecteur. Les
ADR ne l'interdisent pas ailleurs, mais le commentaire de `tokens.css` le dit
« CONTAINED » : le statut rouge peint des pastilles et du texte, le mode ne
peint que le logo, donc les deux ne se confondent pas. Ce raisonnement n'a
jamais été une décision écrite.

Le constat de l'usage : on passe de commutation à routage, la barre d'onglets
change, mais le reste de l'écran est identique. Seul un logo de 26 px change
de couleur. La demande : un fond qui passe au rouge, dans les mêmes nuances,
en même temps que le logo glisse.

## Décision

**En mode routage, l'échelle des neutres de fond et les nappes lumineuses
prennent la teinte de `--mode-routing`, à luminosité et chroma OKLCH
identiques, et la bascule s'anime sur `--dur-fast`, le jeton du glissement
du sélecteur.**

1. **Ce qui change** : `--bg`, `--bg-panel`, `--bg-hover`, `--border`,
   `--glass-bg` (le verre des barres, bleu lui aussi), `--wash-a`,
   `--wash-b`. Dans les deux thèmes.
2. **Ce qui ne change pas** : le texte, l'accent bleu (interaction, écriture
   en attente), toute la palette de statut. `--danger` reste le seul rouge
   saturé à l'écran.
3. **« Mêmes nuances »** veut dire : on garde L et C, on remplace la teinte
   H par celle de `--mode-routing` (1,6°). Les neutres font 0,016 à 0,032 de
   chroma : ce sont des gris chauds, pas des rouges. Seules les nappes
   portent de la vraie couleur, et elles plafonnent à 10 % d'opacité.
4. **Le mode est posé sur `<html data-mode>`** par `Layout`, comme le thème
   l'est par `theme.ts`. Les jetons se redéfinissent sous
   `:root[data-mode="routing"]`.
5. **L'animation** passe par un seul nombre enregistré par `@property`,
   `--mode-t`, que `:root` fait transiter de 0 à 1. Chaque jeton se
   décline en deux moitiés (`--bg-switching`, `--bg-routing`) et vaut leur
   `color-mix` au ratio `--mode-t`. Chaque consommateur de `var(--bg-panel)`
   suit sans transition propre. Le jeton de durée est **le même** que celui
   du glissement du logo : si l'un ralentit, l'autre aussi.

## Conséquences

- Le cantonnement du cramoisi à la marque, écrit dans `tokens.css`, est
  abandonné. Le jeton reste distinct de `--danger` (ADR-0013 §4 tient), mais
  l'argument « ils n'apparaissent jamais comme le même genre d'objet » ne
  tient plus pour le fond. Une bannière d'erreur se pose désormais sur un
  fond rosé en routage. Elle reste lisible parce que le fond est à 0,02 de
  chroma et la bannière à pleine saturation, mais la distance est plus
  courte qu'avant.
- **La couleur ne porte toujours pas le mode seule** (ADR-0013 §5,
  ADR-0019 §1). La barre d'onglets reste la lecture principale ; le fond est
  un renfort, invisible pour qui ne distingue pas la teinte.
- Toute nouvelle surface doit consommer les jetons et jamais une valeur
  littérale, sinon elle reste bleue en routage. Les couleurs littérales en
  ligne qui existent déjà (760 `style={{…}}` au ratchet) en contiennent
  sûrement : elles resteront bleues jusqu'à leur nettoyage.
- Animer `--mode-t` sur `:root` recalcule le style de tout l'arbre à
  chaque image pendant 120 ms. Mesurable sur un vieux téléphone, rare à
  l'échelle d'une session.
- Un navigateur sans `@property` bascule sans animation ; un navigateur sans
  `light-dark()` garde les valeurs de repli sombres, déclinées aussi en
  rouge.
- `prefers-reduced-motion` met `--dur-fast` à 0 : la bascule est instantanée.

## Alternatives écartées

- **Enregistrer les sept jetons comme `<color>` et les faire transiter
  directement.** Essayé d'abord : Chrome 124 garde une valeur
  `light-dark()` non résolue dans la valeur calculée d'une propriété
  `<color>` enregistrée, et deux valeurs non résolues ne s'interpolent pas.
  La bascule était instantanée. Un `<number>` s'interpole toujours.

- **Fondu enchaîné de deux pseudo-éléments** (un `body::after` rouge dont
  l'opacité monte). Simple et bon marché, mais n'anime que les nappes : les
  panneaux, les bordures et le fond lui-même sont des jetons, et ils
  basculeraient d'un coup pendant que les nappes fondent.
- **Ne teinter que les nappes.** Le constat porte sur « le thème de fond »,
  et l'échelle des neutres est ce qui donne au fond sa couleur. Des nappes
  rouges sur des panneaux bleus donnent un fond mal accordé, pas une
  bascule.
- **Utiliser `--danger`.** Écarté par ADR-0013 §4 pour les raisons qu'il
  donne, qui valent encore plus pour une surface entière.
- **Une durée propre au fond, plus longue.** 120 ms est court pour un
  changement de fond, et un fondu de 320 ms serait plus doux. Mais la demande
  est que le fond souligne le geste du sélecteur, et deux durées différentes
  feraient deux événements au lieu d'un. Si le fond paraît brutal, on
  ralentit les deux ensemble.
- **Un fond rouge saturé**, au sens courant de « passer au rouge ». C'est ce
  qui rendrait le mode indiscernable d'une alerte, et c'est aussi illisible :
  les neutres portent du texte.
