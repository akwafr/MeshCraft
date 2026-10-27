---
Statut: Accepté
Date: 2026-10-26
---

# ADR-74818 — Un numéro d'ADR se tire au sort, il ne se prend pas au suivant

## Contexte

Jusqu'à ADR-0032, un ADR prenait **le numéro suivant** : on lit le dernier
de l'index, on ajoute un. La règle tient tant qu'une seule personne écrit à
la fois.

Le 2026-10-25, deux agents lancés en parallèle, chacun dans son worktree, ont
lu le même index, vu le même dernier numéro (0031), et écrit chacun un
**ADR-0032** :

- `ADR-0032 — Une tuile peut déclarer une zone vivante en mode édition` ;
- `ADR-0032 — Ctrl+F cherche dans le site, la seconde pression rend la main
  au navigateur`.

Les deux sont passés en `Accepté` dans leur branche, et la collision ne se
voit qu'à la fusion, alors que le commit d'au moins l'un des deux cite déjà
son numéro. C'est structurel, et pas un accident de discipline : un worktree
ne voit pas les autres, et le travail en parallèle est désormais la façon
normale de faire avancer ce dépôt. Plus on ouvre de branches à la fois, plus
la collision devient probable.

Renuméroter à la fusion est exactement ce que l'index interdit, pour une
bonne raison : le numéro est cité dans des messages de commit, dans des
commentaires de code (`ADR-0016 §3` apparaît dans quatre fichiers Go) et dans
d'autres ADR. Un renumérotage laisse ces citations pointer vers le mauvais
document, ou vers aucun.

## Décision

**À partir de cet ADR, un nouvel ADR prend un identifiant de cinq chiffres
tirés au hasard, entre 10000 et 99999, et vérifié libre sur toutes les
branches.** Le numéro devient un identifiant, plus un rang.

1. **Le tirage se fait au moment de créer le fichier**, avec la commande
   écrite dans l'index. Elle écarte tout identifiant déjà présent dans
   l'historique de **n'importe quelle** branche, worktrees d'agents compris.
2. **Les ADR-0001 à ADR-0032 gardent leur numéro.** Aucun renumérotage : ils
   sont cités partout, et la règle d'immutabilité vaut pour leur nom comme
   pour leur texte. La largeur fait la différence entre les deux régimes :
   quatre chiffres pour un numéro séquentiel, cinq pour un identifiant tiré,
   donc aucune confusion n'est possible entre eux.
3. **L'ordre se lit dans la date, plus dans le numéro.** L'index range les
   nouveaux ADR à la suite des anciens, par `Date` d'en-tête. Deux ADR du
   même jour se rangent dans l'ordre où ils sont entrés dans `main`.
4. **La collision du 2026-10-25 se règle par la nouvelle règle, pas par un
   renumérotage.** ADR-0032 reste celui des tuiles, dont le commit cite déjà
   ce numéro. Celui de Ctrl+F n'a encore été cité par aucun code fusionné
   dans `main`, et il devient
   [[ADR-80600 — Ctrl+F cherche dans le site, la seconde pression rend la main au navigateur]].

## Conséquences

- **Deux agents qui travaillent en parallèle ne peuvent plus se marcher
  dessus**, sauf malchance : sur 90 000 valeurs, deux tirages indépendants
  tombent sur le même identifiant une fois sur 90 000. Même avec dix branches
  ouvertes à la fois, le risque reste sous 1 sur 1 000. Le tirage écarte déjà
  tout ce qui est visible, donc la seule collision encore possible oppose
  deux branches qui ne se voient pas, et elle se voit à la fusion comme
  aujourd'hui.
- **On perd la lecture « 0031 vient après 0030 ».** Savoir qu'ADR-80600 est
  plus récent qu'ADR-74818 demande de lire la date. C'est le vrai prix, et
  il est payé à chaque lecture de l'index, pas une seule fois.
- **On perd aussi le compte.** « 32 décisions » ne se lit plus dans le
  dernier numéro.
- Un identifiant à cinq chiffres ne se retient pas comme « le 7 ». C'est
  accepté : les ADR se citent par lien et par copier-coller, pas de mémoire.
- L'index reste **le seul endroit** qui range les ADR. Un ADR absent de
  l'index n'est plus repérable par un trou dans la numérotation.

## Alternatives écartées

**Réserver le numéro dans l'index avant de lancer le travail.** C'était la
première idée, et elle revient à exiger la discipline qui vient de manquer.
Une réservation n'existe que dans la branche où elle est écrite. Pour que les
autres la voient, il faut la commiter dans `main` avant de créer le worktree,
et un agent qui rédige son ADR en cours de route (ce qui arrive) ne peut pas
le faire.

**Renuméroter à la fusion.** C'est ce que demandait le cas du 2026-10-25, et
c'est ce que la règle d'immutabilité interdit : les citations dans les
commits ne se réécrivent pas sans réécrire l'historique, et cet historique a
déjà été réécrit une fois, pour une autre raison, à un coût qu'on ne veut pas
repayer.

**Un identifiant daté (`ADR-2026-10-25-a`).** Il garde l'ordre, ce qui est sa
seule qualité. Mais deux agents lancés le même jour tombent sur le même
suffixe, et le parallélisme se fait précisément dans la même journée. La
collision change de forme, elle ne disparaît pas.

**Un hachage du titre.** Unique sans tirage, mais un titre se reformule
pendant la rédaction, et l'identifiant changerait avec lui. Un identifiant
doit être fixé avant que le texte ne le soit.

**Un suffixe propre à la branche (`ADR-0032-a068`).** Unique par
construction, mais illisible, et il garde un numéro séquentiel qui a l'air
de dire un ordre qu'il ne garantit plus : deux `0033` auraient des suffixes
différents.
