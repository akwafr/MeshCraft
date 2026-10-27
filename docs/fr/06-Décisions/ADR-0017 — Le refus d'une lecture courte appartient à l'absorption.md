---
Statut: Accepté
Date: 2026-10-24
---

# ADR-0017 — Le refus d'une lecture courte appartient à l'absorption

## Contexte

`Server.absorbObserved` fait entrer la configuration **observée** d'un
équipement dans l'intention de meshcraft. Elle remplace `devices.ports`
**en bloc** : une observation partielle ne fusionne pas, elle **supprime**
l'intention de chaque port que la lecture n'a pas atteint.

Deux incidents, et une consigne qui n'a pas tenu.

**2026-08-11, `poefs3400lab`.** Une observation de 23 ports sur 52 a été
persistée : `show running-config` était coupé au milieu de la strophe de GE23,
et le connecteur l'a acceptée parce qu'il ne considère une lecture comme ratée
que si la sortie est **vide**. Le bouton « importer » était offert. L'appuyer
aurait effacé 29 ports, dont les quatre trunks 10G. Correctif : un refus dans
`handleImportDeviceConfig`, mesuré contre la **façade** — qui vient du modèle,
pas de la lecture qui a peut-être été tronquée.

Et, dans l'en-tête d'`absorbObserved`, une consigne en capitales :

> THE CALLER OWNS THE SAFETY CHECK … any new caller has to make the same
> refusal before arriving here.

**2026-10-24, `sw01-arena`.** Ce nouvel appelant est arrivé :
`importFromDevice`, l'import à l'adoption. Il ne testait que
`len(state.Ports) == 0`. Adoption d'un FS S3400-48T4SP, façade **52 ports**
(48 × GE + 4 × XG 10G) :

- **48 ports importés sur 52.** Les quatre uplinks `XG1`–`XG4` sont absents du
  modèle.
- Les 48 sont **tous `access`** sur un commutateur qui porte 7 VLANs — et c'est
  justement l'uplink qui manque.
- **`sync: in_sync`.** L'intention et l'observation sortent de la **même**
  lecture tronquée, donc la dérive ne compare rien : les deux sont également
  fausses. Rien ne le signalera jamais.

Un `apply` sur cet équipement traiterait les quatre ports 10G comme non
configurés.

Le fait qui décide de cet ADR n'est pas l'incident, c'est la consigne : elle
était écrite, explicite, en capitales, dans la fonction même — et le deuxième
appelant a été écrit sans elle. Elle n'a pas tenu **une seule fois**. C'est le
même motif que le bug trouvé plus tôt le même soir dans `parse.go` : la règle
est dans un commentaire, donc elle dépend de la bonne volonté de quelqu'un qui
lit, et elle se recopie mal.

## Décision

**Le contrôle descend dans `absorbObserved`, et l'adoption sur lecture courte
adopte sans importer.**

1. **Le refus est dans la fonction**, exécuté **avant toute écriture et avant
   toute lecture de la base**. Aucun appelant ne peut arriver au-delà. La
   consigne « tout nouvel appelant doit refaire ce refus » est supprimée parce
   qu'elle n'a plus d'objet.

2. **Le refus est un type**, `*shortRead`, pas un booléen ni une chaîne. Chaque
   appelant choisit encore son rendu — 422 sur la page de l'équipement, phrase
   d'avertissement à l'adoption — mais le **défaut de l'oubli a changé de
   camp** : un appelant qui ignore ce cas obtient un refus et une erreur
   propagée, plus une suppression silencieuse. La sûreté ne dépend plus de ce
   que l'appelant a lu, seule la qualité du message en dépend.

3. **Une observation sans aucun port est refusée aussi**, quelle que soit la
   façade, dès que le modèle de l'équipement en porte déjà. Sans cette clause
   la fonction resterait sûre uniquement sur les plateformes qui annoncent une
   façade, et le garde-fou de l'autre moitié resterait chez l'appelant — ce que
   cet ADR défait.

4. **À l'adoption, une lecture courte adopte l'équipement et n'importe rien.**
   L'équipement existe, son modèle de ports reste **vide**, et la phrase dit
   quoi faire. Rien de faux n'est affirmé.

Le point 4 est l'arbitrage réel, et il se lit sur une seule question : qu'est-ce
qui est le plus réparable ? Un modèle **vide** est visiblement vide — la façade
est dessinée, les ports sont libres, l'opérateur relit et importe depuis la page
de l'équipement. Un modèle **tronqué** affirme `in_sync` : il est faux et il
prétend être juste, et personne ne vient réparer ce que le produit déclare
conforme.

## Conséquences

- **Un équipement adopté sur lecture courte porte un modèle vide**, et
  l'opérateur a un geste de plus à faire. C'est le prix, assumé : l'adoption a
  réussi, et le geste manquant est visible.
- **La façade devient portante pour l'import.** Une plateforme qui déclarerait
  une façade **trop grande** bloquerait l'import d'un équipement sain. Mesuré
  sur la flotte avant le premier refus : huit équipements sur neuf observent
  **exactement** leur nombre de façade, le neuvième en observe **plus**
  (CiscoLabFirst1, 28 contre 26 déclarés). Le `>` n'est donc délibérément pas
  refusé, et une façade inconnue (`0`) ne bloque rien — sinon toutes les
  plateformes qui n'en annoncent pas perdraient l'import.
- **Un nouvel appelant reste libre de mal rendre le message.** Le type garantit
  qu'il ne détruira rien ; il ne garantit pas que l'opérateur lira une phrase
  utile plutôt que « internal error ». C'est le résidu assumé : on a déplacé la
  sûreté, pas l'ergonomie.
- **L'existant en base n'est pas réparé, et il n'est pas détecté par ce
  correctif.** `sw01-arena` porte aujourd'hui une intention de 48 ports pour une
  façade de 52, avec `sync: in_sync`, et le restera : la dérive compare
  l'intention à l'observation, qui viennent de la même lecture. Le détecter
  demande de comparer le **nombre de ports de l'intention** à la façade — un
  contrôle de cohérence du modèle, pas de la dérive. Il n'est pas fait ici, et
  **aucune donnée n'est modifiée en base**.

## Alternatives écartées

**Recopier les trois lignes dans `importFromDevice`.** Le correctif minimal, et
celui qu'on proposera. Écarté parce que **la duplication est précisément ce qui
a échoué** : la consigne existait, en capitales, et le deuxième appelant est
passé outre. Le troisième paierait le même prix, et l'incident de ce soir est la
mesure de ce que coûte cette option.

**Garder le contrôle chez l'appelant et renforcer le commentaire.** Même motif.
Rien n'applique un commentaire. Le dépôt en a de très longs et de très bons —
celui-là était bon, et il n'a servi à rien.

**Faire d'`absorbObserved` une fonction à paramètre obligatoire
`faceplate int`.** Sérieusement envisagé : le compilateur forcerait chaque
appelant à fournir l'étalon. Écarté pour deux raisons. Un appelant peut passer
`0` pour faire taire le contrôle, et la seule chose qu'on sait des appelants de
cette fonction est que l'un d'eux prendra le chemin le plus court. Surtout,
`faceplatePortCount(d)` se **dérive** du `model.Device` qu'`absorbObserved`
reçoit déjà : demander à l'appelant une valeur que la fonction sait calculer,
c'est ouvrir la porte à ce qu'il en fournisse une autre.

**Refuser toute l'adoption sur lecture courte.** Le plus sûr en apparence.
Écarté parce que l'équipement **ne rentre pas du tout** : l'opérateur qui adopte
une baie perd celui dont la lecture a été coupée, et une adoption refusée est le
seul état d'où l'interface ne permet plus rien — le candidat reste candidat, et
la lecture courte, elle, se répare en relisant.

**Adopter et importer les 48 quand même, avec un avertissement.** C'est le
comportement d'aujourd'hui, plus une phrase. Écarté parce que l'avertissement
vit dans la réponse d'adoption, se lit une fois, et que le modèle faux reste —
en affirmant `in_sync`. Un avertissement n'annule pas une affirmation fausse.

**Importer la lecture courte et marquer l'équipement « partiel ».** Il faudrait
un troisième état de synchronisation, compris par la dérive, l'apply, la
conformité et l'interface. Cher, et le résultat est qu'on garde un modèle faux
en base pour le décorer.

**Comparer le nombre observé à l'intention existante** (refuser toute absorption
qui réduit l'ensemble des ports) plutôt qu'à la façade. Écarté : un équipement
légitimement re-modélisé — ports renommés, membre de pile retiré — serait bloqué
sans issue. La façade a la propriété que l'intention n'a pas : elle vient du
**modèle**, pas de la lecture qui a peut-être été coupée.
