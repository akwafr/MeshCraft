---
Statut: Accepté
Date: 2026-10-24
Note: Consigné après coup, comme ADR-0001.
---

# ADR-0004 — Provisioning Junos sans `commit confirmed`

## Contexte

Le provisioning série des Juniper EX pousse une configuration initiale puis la
valide. Junos offre `commit confirmed` : la configuration est appliquée et
**revient automatiquement** si elle n'est pas confirmée dans le délai. Juniper
le présente précisément pour ce cas, et c'est la recommandation usuelle.

## Décision

Utiliser un **`commit` simple, précédé d'un `commit check`**. Pas de
`commit confirmed`.

## Conséquences

- Le garde-fou habituel est perdu : une configuration qui coupe l'accès reste
  en place. Acceptable ici, et seulement ici, parce que la passe tourne sur la
  **console** — le changement ne peut pas couper la session qui le porte.
- En échange, on évite un mode de panne que `commit confirmed` **ajouterait** :
  une passe qui meurt après l'application et avant la confirmation (onglet
  fermé, adaptateur débranché, annulation) laisse un switch provisionné,
  vérifié vert, qui **revient en usine cinq minutes plus tard** pendant que
  chaque enregistrement meshcraft le dit adopté.
- `commit check` porte la vérification : un échec déclenche `rollback 0` et une
  nouvelle tentative dans l'autre dialecte, sans que l'équipement ait bougé.
- À revoir si un jour le provisioning Junos passe **par le réseau** plutôt que
  par la console. L'argument tombe entièrement.

## Alternatives écartées

**`commit confirmed 5`.** La recommandation Juniper, écartée pour la raison
ci-dessus : sur console, elle échange un risque inexistant contre un risque
réel.

**`commit` seul, sans `commit check`.** Laisse l'équipement refuser une
transaction à moitié saisie et oblige à lire le refus à la volée.
