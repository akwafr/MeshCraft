---
Statut: Accepté
Date: 2026-10-24
---

# ADR-0005 — Sélecteur marque puis modèle pour les procédures série

## Contexte

L'écran `/add/serial` choisit la plateforme par une **grille de tuiles**. Six
tuiles sont rédigées et traduites (FS S3900, FS S3400, EdgeSwitch, Catalyst
2960-X, Arista EOS, Juniper EX) et portent chacune une procédure série
scriptée de bout en bout. Sous elles, la grille déverse tout ce que l'API
`/api/drivers` renvoie d'autre : `cisco-nxos`, `aruba-cx`, `hpe-procurve`,
`mikrotik-swos`, `mikrotik-routeros`, `ubiquiti`, `extreme-exos`,
`huawei-vrp`, `aruba-instant-on-1930` — des identifiants bruts avec un nom de
marque, et **aucune procédure derrière**.

Trois problèmes qui ne se voient qu'à l'usage :

1. **La grille promet ce qu'elle ne tient pas.** Une tuile `huawei-vrp` se
   clique. Elle mène à un assistant qui n'a rien à envoyer.
2. **Elle ne passe pas à l'échelle.** Quinze tuiles aujourd'hui, et chaque
   driver ajouté en pose une de plus, sans hiérarchie : la marque et le modèle
   sont aplatis sur la même ligne.
3. **Elle ne se cherche pas.** Trouver « 2960-X » demande de balayer la grille
   des yeux ; il n'y a pas de saisie.

La même question se pose à l'identique sur `/add/reset` (« Réinitialiser un
équipement »), qui affiche une procédure selon la plateforme.

## Décision

Remplacer la grille par **deux listes déroulantes avec recherche intégrée,
marque puis modèle**, suivies d'un bouton « Lancer la liaison série ». La
liste des marques contient **« Découverte automatique »** comme première
entrée ; la choisir masque la liste des modèles.

Le catalogue est un **module pur partagé** (`serialCatalog.ts`), consommé par
`/add/serial` et par `/add/reset`. Il ne contient **que les plateformes dont
la procédure est écrite** : une entrée du catalogue est une promesse tenue.

Le sélecteur lui-même est un primitif `ui/SearchSelect.tsx`, construit sur
`usePicker` comme `SiteSelect`, pour que les deux écrans ne divergent pas.

## Conséquences

- **On perd la découvrabilité de la grille.** Une tuile montre six plateformes
  d'un coup d'œil ; une liste fermée n'en montre aucune. C'est le prix, et il
  est accepté parce que la grille ne montrait de toute façon plus rien
  d'utile passé la sixième tuile.
- **Les plateformes sans procédure disparaissent de l'écran.** Un opérateur
  qui cherche `huawei-vrp` ne le trouvera plus et pourra croire que meshcraft
  ne connaît pas la marque. C'est préférable à l'actuel, qui le laisse cliquer
  puis échouer — mais il faudra une phrase disant que la liste ne contient que
  les procédures écrites, et que la découverte automatique reste ouverte à
  tout le reste.
- **Ajouter une plateforme devient deux gestes** : écrire la procédure, puis
  l'inscrire au catalogue. L'oubli du second est silencieux. Un test épingle
  que toute clé de `SOFT_RESET_DRIVERS` a une entrée au catalogue.
- Le catalogue duplique une information que l'API connaît déjà (la liste des
  drivers). La duplication est **volontaire** : l'API liste ce qui existe,
  le catalogue liste ce qui est **praticable en série**, et ce sont deux
  ensembles différents.

## Alternatives écartées

**Garder la grille et masquer les tuiles sans procédure.** Règle le problème 1
seul. La grille reste plate et non cherchable, et le jour où quinze procédures
existent le problème 2 revient entier.

**Un `<select>` natif marque + modèle.** Gratuit, accessible, et c'est
exactement ce que `SiteSelect` a déjà écarté : un `select` natif ne porte pas
de champ de recherche, et le projet a un primitif qui le fait.

**Un seul champ de recherche libre sur « marque modèle ».** Plus rapide pour
qui sait ce qu'il cherche, inutilisable pour qui veut savoir ce qui est
disponible — et c'est le cas majoritaire devant une baie avec un switch
inconnu dans les mains.

**Lire le catalogue depuis l'API.** Demanderait au contrôleur de déclarer
quelles plateformes ont une procédure *série*, alors que les procédures vivent
dans le frontend (`web/src/pages/add/*.ts`). Le serveur n'a pas cette
information et la lui donner serait un aller-retour pour rien.


## Addendum à la mise en œuvre (2026-10-24)

La décision prévoyait un test épinglant que chaque clé de
`SOFT_RESET_DRIVERS` ait une entrée au catalogue. La mise en œuvre fait
mieux et rend le test inutile : `SOFT_RESET_DRIVERS` et `TILED_DRIVERS`
étaient **deux tableaux écrits à la main portant les six mêmes clés**, et un
catalogue à côté en aurait fait trois. Les deux ont été supprimés au profit
de `softResetDrivers()`, **dérivé** du catalogue. Il n'existe plus d'endroit
où ils puissent diverger.

Ce que le test épingle à la place : qu'aucune des plateformes sans procédure
(`huawei-vrp`, `extreme-exos`, `aruba-cx`…) ne puisse revenir silencieusement
dans la liste.
