---
Statut: Accepté
Date: 2026-10-24
---

# ADR-0010 — L'écran de réinitialisation liste tout, et la marque est un champ

## Contexte

Deux listes décrivent les plateformes, elles n'ont pas la même taille, et un
écran les consulte toutes les deux.

- `internal/discovery/reset.go` porte **21 procédures de réinitialisation**
  (plus `genericReset`, renvoyée pour un équipement non identifié et jamais
  listée). Chacune porte `Driver`, `Vendor`, `Family`, `Aliases` et un drapeau
  `Verified`.
- `web/src/pages/add/serialCatalog.ts` porte **6 plateformes** : celles dont la
  procédure **série** est écrite dans le navigateur.
- **15 procédures de réinitialisation sont hors catalogue, dont 7 marquées
  `Verified: true`** : `hpe-procurve`, `mikrotik-routeros`, `ubiquiti`,
  `extreme-exos`, `huawei-vrp`, `h3c-comware`, `cumulus-sonic`.

`/add/reset` s'en sort aujourd'hui avec une sentinelle « Une autre
plateforme… » qui bascule l'écran dans une recherche libre sur l'ensemble
servi par le contrôleur. Ça marche. Le prix est double : **deux grammaires sur
le même écran** — un couple marque/modèle d'un côté, un champ texte de
l'autre — et la majorité des procédures, verifiées comprises, rangée derrière
une porte dont le libellé dit « le reste ».

[[ADR-0005 — Sélecteur marque puis modèle pour les procédures série]] a créé le
catalogue et a accepté de **masquer les plateformes dont la procédure série
n'existe pas** : « une entrée du catalogue est une promesse tenue ». Cette
règle est juste, et elle parle de `/add/serial`, où une entrée mène à un
assistant qui doit avoir quelque chose à envoyer. Sur `/add/reset`, il n'y a
rien à envoyer : la procédure **existe**, elle est **vérifiée**, et l'écran ne
fait que l'afficher. Masquer une connaissance correcte parce qu'un autre écran
ne sait pas la jouer n'est pas la même décision.

La proposition sur la table était de dériver la liste des marques de
`reset.go` et de n'utiliser `serialCatalog.ts` que pour enrichir les lignes
qu'il couvre, en groupant sur la chaîne `Vendor`. **Son arithmétique est
fausse, et c'est instructif** : grouper les 21 procédures sur `Vendor` donne
**18 groupes, pas 22**, et trois d'entre eux sont HPE — `"HPE"` (ProCurve),
`"Aruba (HPE)"` (ArubaOS-CX), `"H3C / HPE Comware"`. Un quatrième,
`"Linux network"` (Cumulus / SONiC), n'est pas un constructeur. Le doublon
« Ubiquiti » que la proposition se reprochait n'est, lui, pas un problème :
c'est exactement ce qu'un regroupement résout — une marque, deux familles.

Le vrai défaut est ailleurs. `Vendor` est aujourd'hui un **libellé** : il est
affiché dans une puce, et personne ne l'a jamais traité comme une identité.
En faire une clé de regroupement donne une portée à une retouche de
formulation : écrire `"HPE (ProCurve)"` à la place de `"HPE"` scinderait une
marque dans l'interface, silencieusement, depuis un commit qui croyait
améliorer une phrase.

## Décision

**L'écran de réinitialisation liste toutes les procédures que le serveur
porte**, groupées par marque, sans sentinelle et sans bascule de mode. Le
regroupement se fait sur un **champ explicite**, `ResetProcedure.Brand`, et
non sur la chaîne affichée. `serialCatalog.ts` ne décide plus de ce qui est
listé : il **enrichit** les lignes qu'il couvre, joint par `driver`, qui était
déjà la seule clé entre les deux.

Trois points, qui sont la décision et pas sa mise en œuvre :

**1. `Brand` est l'identité, `Vendor` reste le libellé.** `Brand` est un slug
stable ; `Vendor` est la phrase affichée. Un test Go épingle que toute
procédure porte un `Brand`, et que deux procédures qui partagent un `Brand`
portent le **même** `Vendor`. La contrainte que la proposition créait sans le
dire devient explicite, vérifiée, et impossible à casser en reformulant.

**2. La fusion HPE est faite une fois, exprès.** `hpe-procurve` et `aruba-cx`
deviennent une seule marque `hpe`, libellée **« HPE / Aruba »** : c'est la même
lignée de produits, et le couvercle dit HP, HPE ou Aruba selon l'année.
`h3c-comware` **reste séparé** : Comware est la lignée 3Com/H3C, et quelqu'un
qui tient un 5130 cherche H3C, pas HPE. Ces deux arbitrages sont des
décisions, pas des conséquences d'une égalité de chaînes.

**3. La recherche atteint les alias, sans les afficher.** Les `Aliases`
(`s5720`, `2530`, `gs1900`…) sont exactement les numéros qu'un opérateur a sous
les yeux. `ui/SearchSelect.tsx` reçoit un champ `keywords` non affiché, et sa
correspondance passe en **mots** (chaque mot de la requête doit apparaître),
pliée sur les accents — la même règle que `resetMatches` côté Go. C'est ce qui
permet de supprimer le champ de recherche libre sans perdre ce qu'il savait
faire : taper `s5720` dans le champ Marque trouve Huawei.

L'écran charge l'ensemble une fois (21 lignes) et filtre dans le navigateur.
Le paramètre `q` de `/api/discovery/reset-procedures` reste ce qu'il est, pour
`/add/serial` et pour les clients de l'API.

## Conséquences

- **Les 7 procédures vérifiées hors catalogue deviennent atteignables en deux
  gestes** au lieu d'être derrière une sentinelle. C'est le but.
- **Ajouter une procédure demande désormais de choisir une marque.** L'oubli
  n'est pas silencieux : le test Go refuse un `Brand` vide. En revanche, la
  cohérence `Brand` → `Vendor` est une contrainte **nouvelle** sur un champ qui
  n'en avait pas, et quelqu'un la rencontrera en reformulant un libellé.
- **La puce `Vendor` change à l'écran** pour `aruba-cx` et `hpe-procurve` :
  « Aruba (HPE) » et « HPE » deviennent « HPE / Aruba ». La `Family` continue
  de distinguer les deux lignées.
- **Une `Family` change aussi**, et c'est un défaut que la liste unique
  révèle : les deux procédures Ubiquiti se retrouvent côte à côte sous une
  seule marque, et « UniFi / EdgeSwitch » en face de « EdgeSwitch / UniFi
  Switch (FASTPATH) » demandait de choisir entre deux anagrammes. La première
  devient « UniFi (adoption par le contrôleur) » : `Family` nomme la **voie**,
  pas la gamme. La leçon est générale — deux familles d'une même marque
  doivent se lire l'une contre l'autre, ce que la sentinelle dispensait de
  vérifier.
- **La liste de `/add/reset` ne promet plus rien sur le série.** Une entrée y
  dit « cette procédure de réinitialisation existe », et rien d'autre.
  `/add/serial` garde la règle d'ADR-0005 intacte, et le catalogue garde sa
  promesse : il décide toujours seul de ce qui est **jouable** en série, et
  `softResetDrivers()` en reste dérivé.
- **Une marque qui ne porte qu'une procédure la sélectionne d'office**, et le
  champ Modèle n'apparaît pas. On perd le temps de confirmation du modèle ;
  c'est la règle que `/add/serial` applique déjà (« une liste à une seule
  réponse est une cérémonie »).
- **« Linux network » figure dans une liste de marques sans être une marque.**
  Accepté : personne qui tient une white box n'a de marque à chercher, et le
  nom de l'OS est ce qu'il connaît.
- `SearchSelect` change de règle de correspondance pour **tous** ses appelants,
  `/add/serial` compris. Le changement est strictement plus permissif sur une
  requête multi-mots (`cisco 2960` ne correspondait à rien) et identique sur un
  mot.
- Le champ de recherche libre disparaît. Les frappes sont les mêmes, une
  décision en moins — mais ce n'est plus la correspondance multi-mots du
  serveur qui répond, c'est celle du navigateur. Les deux doivent rester
  d'accord ; elles sont écrites pour ça et c'est tout ce qui les tient.

## Alternatives écartées

**Garder la sentinelle** (le statu quo). Elle fonctionne, et elle laisse deux
grammaires sur un écran de dix lignes. Surtout, elle range la majorité du
contenu derrière un libellé qui dit « autre » : sur 21 procédures dont 15 sont
derrière la porte, ce n'est plus une sortie de secours, c'est l'entrée
principale mal nommée.

**Grouper sur la chaîne `Vendor` telle quelle**, la proposition telle que
posée. Elle donne 18 groupes dont trois HPE côte à côte, et surtout elle rend
porteuse une chaîne d'affichage sans rien pour la tenir : une retouche de
copie scinde ou fusionne une marque dans l'interface. Écartée pour ça, pas
pour le doublon « Ubiquiti » — le regroupement est précisément ce qui le
résout.

**Padder `serialCatalog.ts` des 15 plateformes manquantes.** Une seule liste
aussi, et elle mentirait à l'écran pour lequel le catalogue a été écrit :
`softResetDrivers()` en est dérivé, donc `/add/serial` se remettrait à
proposer `huawei-vrp` et à mener à un assistant vide. C'est exactement la
régression que le test d'ADR-0005 épingle, et il a raison.

**Déplacer le catalogue série côté serveur** (un champ « procédure série
écrite » dans `reset.go`), pour n'avoir qu'une source. ADR-0005 l'a déjà
écartée pour la bonne raison : les procédures série vivent dans le navigateur,
le serveur ne peut pas savoir lesquelles existent, et le lui faire déclarer
serait un aller-retour qui se désynchronise au premier oubli.

**Un seul champ de recherche libre, sans marque ni modèle.** Déjà écarté par
ADR-0005, pour une raison qui n'a pas changé : inutilisable pour qui veut
savoir ce qui est disponible, et c'est le cas majoritaire devant une baie avec
un switch inconnu dans les mains.

**Deux écrans, un par ensemble.** Supprime la question en dupliquant la
navigation, et laisse l'opérateur choisir entre deux pages « réinitialiser »
avant d'avoir regardé son matériel.
