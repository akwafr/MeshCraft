---
Statut: Accepté
Date: 2026-10-25
Précise: ADR-0013, ADR-0019
---

# ADR-0031 — Le sélecteur de mode est une bascule, et la marque n'est plus une porte

## Contexte

[[ADR-0013 — Commutation et routage sont deux modes de navigation, pas deux
produits]] avait nommé le risque, en toutes lettres, dans ses conséquences :

> Le sélecteur occupe le logo, qui est **aussi** le retour au tableau de bord.
> Deux gestes sur un même objet : il faut que glisser et cliquer restent
> distinguables, au doigt comme à la souris.

[[ADR-0019 — Le sélecteur ne porte que la marque, le mode se lit dans la
barre]] a reconduit ce point tel quel, et c'est son §4 : « Le geste ne change
pas : glisser bascule, cliquer ramène au tableau de bord, avec le seuil et la
dominance d'axe déjà mesurés. »

**Le risque s'est matérialisé.** Le départage était soigné et il était mesuré :
seuil de 24 px et dominance d'axe de 1,7 (`modeFromDrag`), capture du pointeur
pour suivre un doigt qui quitte un bouton de 26 px, `draggable={false}` pour
tuer le glisser-déposer natif du lien, `touch-action: pan-y` pour laisser
l'axe vertical au navigateur, et le clic qui suit un geste avalé une fois
(`swallowNextClick`). En laboratoire, ça fonctionne. À l'usage, le retour est :
« le switcher routing/switching est encore buggué ».

C'est le seul verdict qui compte, et il ne dit pas que le départage est mal
réglé. Il dit que **la seule façon de changer de mode était un glissement que
rien n'annonce**. Pire : ADR-0019 avait retiré de la vue les deux mots qui
laissaient deviner qu'il y avait là un sélecteur, ce qu'il avait lui-même
inscrit au débit (« Un utilisateur qui découvre le produit ne sait plus que le
sélecteur est un sélecteur »). Il restait donc, à l'écran, un logo. On le
presse — réflexe acquis sur à peu près tout le web — la page part au tableau de
bord et le mode ne change pas. L'utilisateur appelle ça un bug. Il a raison.

Les deux demi-positions existaient déjà et répondaient au clic. Elles ne
peignent rien (c'est ADR-0019 §1), elles mesurent 16 px, et l'une des deux est
posée **entre** la marque et le bord de la pastille : viser le logo et tomber
deux pixels à côté ne fait rien du tout. Une cible invisible à côté du seul
objet visible n'est pas une cible.

## Décision

**Le sélecteur est une bascule. Toute pression dessus change de mode et la
marque glisse de l'autre côté. Elle ne ramène plus au tableau de bord.**

1. **Un geste, un seul.** Le glissement est **supprimé**, pas rendu optionnel :
   `modeFromDrag`, son seuil, sa dominance d'axe, le suivi du doigt et
   l'avalement du clic suivant quittent le code. Il n'y a plus deux gestes sur
   un même objet, donc plus rien à départager — ni à re-régler la prochaine
   fois que quelqu'un trouve que ça accroche.

2. **Toute la pastille bascule.** La marque est le curseur et occupe
   **exactement** la moitié active de la piste ; l'autre moitié est le logement
   vide de la position où l'on n'est pas. Ce sont les deux seules surfaces
   qu'un pointeur peut atteindre, et les deux basculent : presser la marque,
   presser le vide, taper au doigt n'importe où sur la pastille donne le même
   résultat. Il n'y a plus de zone morte, parce que la demi-position active est
   **couverte** par la marque.

3. **Le retour au tableau de bord quitte la marque.** L'onglet « Tableau de
   bord » existe dans les deux modes — c'est une donnée de `MODE_TABS` et
   `navMode.test.ts` l'affirme pour les deux barres — donc en haut à 1440 px et
   dans la barre du pouce à 393 px. Avant qu'un site existe, la seule zone
   atteignable est la configuration, et c'est l'engrenage qui y mène
   (`configTo`), pas la marque. La fonction n'est pas perdue : elle cesse
   d'être doublée sur un objet qui avait déjà un rôle.

4. **Le survol dit ce que la pression fera.** La marque porte un `QuickTip` qui
   nomme la **destination** — « Routage » quand on est en commutation,
   « Commutation » quand on est en routage — et c'est le mot que porte déjà la
   demi-position vide, puisque la seule moitié atteignable est toujours celle
   où l'on n'est pas. Les deux surfaces annoncent donc le même mot et font la
   même chose. C'est plus que ce qu'ADR-0019 laissait (deux noms de positions
   qu'il fallait survoler un par un pour apprendre), et ça rend une partie de
   l'affordance qu'il avait perdue.

5. **Le clavier garde deux positions nommées, et elles restent sélectives.**
   ADR-0019 §3 est intact : un `radiogroup`, deux `radio`, `aria-checked`,
   atteignables par Tab, activables par Espace, et leur libellé de position
   (« Commutation » / « Routage ») s'ouvre au focus — ce qui est la raison pour
   laquelle `QuickTip` a appris à réagir au focus. Au clavier on **choisit** une
   position ; au pointeur on **bascule**. Les deux conduisent au même état et
   aucun des deux ne promet autre chose que ce qu'il fait.

## Conséquences

- **La marque n'est plus un lien.** Elle devient un élément inerte pour les
  technologies d'assistance (`aria-hidden`), hors de l'ordre de tabulation,
  dont le clic est un **raccourci de pointeur** : tout ce qu'il fait, les deux
  `radio` le font aussi, donc rien n'est réservé à la souris. Quelqu'un qui
  cherche le tableau de bord en cliquant le logo ne le trouve plus là. C'est le
  prix, il est réel, et on le préfère à l'ambiguïté qu'on vient de mesurer.
- **Au pointeur, on ne peut plus choisir une position, seulement basculer.**
  Sur deux positions c'est la même chose. Le jour où le mode en aura trois, ce
  choix est à refaire — et ce sera un autre ADR, parce qu'une bascule à trois
  états n'existe pas.
- **Le glissement disparaît** : qui l'avait appris le perd. On l'accepte parce
  qu'il n'était découvrable par personne.
- `swallowNextClick` perd son second client et redevient privé à `ui/swipe.ts`
  ([[ADR-0009 — Le balayage ne vole jamais un geste de défilement]]). Le
  commentaire qui expliquait pourquoi il était exporté devenait faux.
- **L'exception à ADR-0009 tombe avec le geste** : plus de `touch-action:
  pan-y` sur la marque, le navigateur reprend les deux axes, et un pouce qui
  part du logo défile la page comme partout ailleurs. Vérifier que le balayage
  entre onglets (`useSwipeNav`, lié au seul élément de contenu) reste
  indifférent à la barre haute — il l'est par construction, il doit le rester.
- **La pastille ne grandit pas, elle maigrit** — mesuré, pas supposé. Deux
  moitiés de 34 px remplacent les 34 + 16 + 16 plus deux gouttières : **74 →
  72 px**. Balayage de 721 à 1440 par pas de 8, en français, barre de
  commutation (le pire cas d'ADR-0019) : **6 largeurs sur 90 débordent, au pire
  de 27 px**, contre 10 sur 90 qu'ADR-0019 laissait ; la barre de routage reste
  à 0 sur 90. Le budget de la barre haute n'est donc pas rouvert, il est un peu
  moins mauvais. Au `narrow` la pastille passe de 94 à 84 px **et** la cible
  utile passe de deux bandes de 28 px à un objet de 84 px dont chaque pression
  bascule.
- **La marque glisse, en `--dur-fast`**, parce que le mouvement *est*
  l'information demandée. `prefers-reduced-motion` met déjà ce jeton à 0 ms et
  la fait donc sauter : la transition n'est pas une décoration à nettoyer.
- Le reste ne bouge pas : l'URL fait autorité (ADR-0013 §2), le mode est une
  préférence de visiteur (§3), le rouge du routage reste un jeton dédié et
  jamais `--danger` (§4), et c'est la barre d'onglets qui dit le mode
  (ADR-0019 §1).

## Alternatives écartées

**Garder le clic vers le tableau de bord et distinguer les deux gestes
autrement.** C'est ce qu'on fait depuis ADR-0013, avec un départage mesuré, et
le verdict d'usage est « buggué ». Les variantes qui restent sont pires : un
appui long pour basculer est invisible et entre en conflit avec le menu
contextuel ; un double-clic, personne ne le devine ; « le logo pour le tableau
de bord, les demi-positions pour le mode » est *exactement* l'état actuel, dont
le défaut est que les demi-positions ne peignent rien. Toutes font reposer la
fonction principale du contrôle sur le geste le moins probable.

**Sortir le sélecteur du logo.** Honnête, et elle résout le problème par
construction. Écartée parce que la demande d'origine (ADR-0013, Contexte) est un
sélecteur **porté par le logo**, parce que c'est ce qui donne un objet de moins
à la barre haute, et parce que la marque qui change de côté est précisément le
signal demandé : « un simple switcher où le logo de meshcraft change de place
de droite à gauche ».

**Un vrai `role="switch"` plutôt qu'un `radiogroup`.** Plus fidèle à ce que le
pointeur fait désormais, et c'est son seul avantage. Un `switch` a un nom et
deux états : un lecteur d'écran annonce « Routage, activé » et n'annonce plus
qu'**une** position. ADR-0019 §3 exige les deux, et une bascule entre deux
fonctions nommées n'est pas une case à cocher.

**Faire basculer les demi-positions aussi, sans logique de position** — tout
clic sur la pastille inverse le mode, et le code n'a même plus à savoir où on a
cliqué. Plus simple d'une ligne, et le survol devient menteur : la moitié
gauche dirait « Commutation » et donnerait le routage. Écartée parce qu'aucune
surface ne doit promettre autre chose que ce qu'elle fait — et parce que la
simplification est illusoire : la moitié atteignable étant toujours l'inactive,
« sélectionner sa position » et « basculer » y sont déjà le même geste.

**Garder le glissement en plus du clic.** Il ne coûte presque rien à laisser en
place et il ne gêne plus personne, puisque la marque n'est plus un lien : il n'y
a plus de clic à lui disputer. Écartée quand même, parce qu'un geste que rien
n'annonce et que le clic rend inutile est du code que personne ne teste et que
le prochain lecteur devra comprendre pour rien. Deux chemins vers le même état
sont un chemin de trop quand l'un des deux est invisible.
