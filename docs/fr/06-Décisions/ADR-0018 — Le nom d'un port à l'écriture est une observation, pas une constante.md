---
Statut: Accepté
Date: 2026-10-25
---

# ADR-0018 — Le nom d'un port à l'écriture est une observation, pas une constante

## Contexte

Le connecteur `fs-s3400` traduit dans les deux sens entre le modèle standard de
meshcraft (`GE1..GE48`, `XG1..XG4`) et les noms que le CLI prononce :

- **en lecture**, `Driver.StandardPortName` : `TGigaEthernet0/51` → `XG3` ;
- **en écriture**, `cliPortName` : `XG3` → `interface tg0/3`.

Deux builds du **même** firmware 2.0.2J sont en service, et ils ne numérotent
pas les cages SFP+ pareil. Le nom est identique (`TGigaEthernet0/N`), seul le N
bouge : Build 95262 (le labo) recommence la série à 1, Build 130907
(`sw01-arena`, production, site ARENA) la continue après le cuivre et numérote
**49 à 52**.

Le correctif du 2026-10-25 (`sfpCageNumber`) a appris **la lecture** à accepter
les deux conventions, et a rendu optionnel le mot « Series » dans la bannière —
ce qui rouvre `looksLikeS3400CLI`, donc **rouvre les écritures** sur cet
équipement. L'écriture, elle, n'a pas bougé : `cliPortName` émet toujours
`tg0/1..4`.

Trois faits enchaînent, et c'est l'enchaînement qui décide de cet ADR.

1. **Un refus lisible est devenu un échec tardif.** Avant, toute écriture sur
   `sw01-arena` était refusée par `requireWritable` avec une phrase qui disait
   quoi faire. Maintenant elle est autorisée, et `interface tg0/1` part vers un
   châssis qui n'a pas de `tg0/1`. Fonctionnellement c'est un progrès — les
   écritures SNMP passent, et la supervision du site en dépend. En lisibilité
   c'est une régression : le diagnostic passe d'une phrase à un message du CLI.

2. **Le message qu'on obtient accuse la mauvaise chose.** `ApplyPortAdmin`
   formule son échec ainsi : « `shutdown` sous `interface g0/N` a été accepté à
   la main sur le S3400 du labo le 2026-08-10, donc un refus ici signifie que
   le firmware de CET équipement diffère et que ses formes doivent être
   relevées ». La forme de commande est juste ; c'est le **nom** qui est
   inventé. C'est exactement le quatrième défaut du correctif précédent, celui
   qui a coûté une heure : un refus qui énonce un constat jamais fait.

3. **Le bloc de configuration ne s'arrête pas à la ligne refusée.**
   `Client.configBlock` envoie toutes ses lignes et collecte les échecs ; ce
   n'est pas un oubli, c'est ce qui permet d'attribuer un refus à la ligne qui
   l'a mérité. Mais si `interface tg0/1` est refusé, le prompt reste
   `Switch_config#` et les lignes suivantes — `switchport mode access`,
   `shutdown` — sont tapées **dans le contexte englobant** au lieu de l'être
   sur le port. Ce que ce CLI fait d'un `shutdown` global n'a pas été relevé,
   et c'est précisément la raison de ne pas le découvrir sur un équipement de
   production.

`StandardPortName` est une méthode de `Driver`, qui est unique pour toute la
flotte : elle n'a aucun contexte de session et ne peut pas savoir quelle
convention porte l'équipement en face. L'information, en revanche, **est déjà
payée** : le `show interface`, le `show vlan` et le `show running-config` que
chaque chemin d'écriture lit **avant** d'écrire prononcent tous les noms de
cages de l'équipement.

## Décision

**Le nom qu'on écrit vient de ce que la session a observé, et une session qui
n'a rien observé refuse de nommer une cage.**

1. **L'état vit sur `Client`**, à côté de `host`, `facts` et `unknownWhy` : un
   `Client` est **une** session SSH ouverte sur **un** hôte, ce qui est
   exactement la portée du fait. Le champ est un type, `cageNumbering`, pas un
   entier : il porte son propre `known`, donc « pas encore observé » n'est pas
   représentable par un zéro qui se confondrait avec la convention `tg0/1..4`.

2. **L'observation est gratuite et elle ne se cherche pas.** `noteCageNumbering`
   est appelée sur la sortie des lectures que le connecteur paie déjà, à
   l'endroit où ces mêmes sorties sont analysées pour y trouver des noms de
   ports : les deux formes de `readLinks`, le `show running-config` et le
   `show vlan` de `ReadState`, le `show interface | include protocol` de
   `portGroups`, le `show running-config` de `readStormControl`, le
   `show spanning-tree` et son `show running-config` de `ReadSpanningTree`.
   Aucun aller-retour n'est ajouté.

   Le choix de ces points n'est pas arbitraire : **c'est l'ensemble des
   pré-lectures des quatre chemins d'écriture**. `ApplyPorts` et
   `ApplyPortAdmin` lisent `ReadState`, `ApplyStormControl` lit
   `readStormControl`, `ApplySpanningTree` lit `ReadSpanningTree`. Aucune
   boucle de ports n'est atteignable sans qu'une observation ait eu lieu, et
   c'est cette propriété-là qui est testée.

3. **L'observation vote, et une contradiction n'élit personne.**
   `observeCageNumbering` relève tous les `tg*0/N` d'une sortie, les passe par
   `sfpCageNumber` — la **même** fenêtre que la lecture, pas une seconde
   définition — et retient l'écart `N − cage`. Si deux écarts différents
   apparaissent dans la même sortie, le résultat est « inconnu » : un
   équipement qui prononcerait les deux conventions n'est pas un équipement
   dont on devine la numérotation. La première observation **connue** gagne et
   n'est plus remplacée : l'équipement ne change pas de build pendant une
   session, et une valeur stable est une valeur sur laquelle un transcript se
   relit.

4. **Une lecture qui n'a pas de pré-lecture se la paie elle-même.**
   `ensureCageNumbering` établit la numérotation en **une** lecture quand la
   session n'a rien vu. Un seul appelant la justifie : l'échantillonneur
   d'optique (`internal/api/opticssample.go`) ouvre une session et appelle
   `ReadOptics` **seul**, et `ReadOptics` est faite de noms de cages. Les
   quatre chemins d'écriture ne l'appellent pas, parce que ce serait un
   aller-retour qui ne peut jamais se déclencher — et parce qu'une écriture
   n'est pas l'endroit où insérer quatre-vingt-dix secondes de pager
   imprévues.

   La commande est `show interface | include protocol`, celle qui **nomme**
   les sockets, et non une sonde par cage : `show interface tg0/1` porte
   l'**écho** de la commande, donc un équipement qui la refuse rend quand même
   la chaîne « tg0/1 » — une session qui lirait sa propre question comme la
   réponse.

5. **Convention inconnue : l'écriture visant une cage est refusée, et le refus
   dit quoi faire.** `cageNumbering.cliPortName` rend une erreur plutôt qu'un
   nom. Le refus nomme les deux conventions, le build où chacune a été relevée,
   et le geste qui le lève (lire l'état une fois). Il arrive **avant** que
   `configBlock` n'ouvre quoi que ce soit, donc aucune ligne ne part dans le
   mauvais contexte.

   **Le cuivre n'est pas concerné** : `g0/N` est identique sous les deux
   conventions. Le refus porte donc sur les quatre cages, jamais sur les
   quarante-huit ports cuivre, et une session qui n'a rien observé écrit
   toujours le cuivre.

6. **Le refus n'est pas un `driver.NotSupported`.** Celui-là s'enveloppe dans
   `ErrNotImplemented` et signifie « cette plateforme ne sait pas faire » —
   définitif, rien à réessayer. Ici le connecteur sait parfaitement faire, il
   n'a simplement pas encore regardé. Classer une ignorance temporaire en
   capacité absente, c'est mentir à l'appelant sur la réparabilité.

7. **La sérigraphie ne s'affirme que si elle a été observée.** Le groupe `XG`
   de `portGroups` ne porte `"silk": "tg0/"` que lorsque la session a vu la
   convention `tg0/1..4`. Sous la convention `49..52` — et quand rien n'a été
   observé — le champ est **absent**, et la façade retombe sur le nom standard
   (`XG1`). La raison est mécanique : le frontend compose `silk + k` où `k` est
   l'indice **standard** (`web/src/network.ts`, `expandPorts`), donc un préfixe
   seul ne peut pas exprimer un décalage de 48. Entre afficher `tg0/1`, un nom
   que l'équipement refuse si l'opérateur le recopie dans son CLI, et afficher
   `XG1`, le nom que meshcraft emploie partout ailleurs, le second n'affirme
   rien de faux.

## Conséquences

- **Un chemin d'écriture qui n'aurait pas de pré-lecture refuserait les quatre
  cages.** C'est le prix, et il est délibérément payé dans ce sens : le défaut
  de l'oubli est un refus avec une phrase, pas une commande inventée. Un
  nouveau geste d'écriture qui ne lit rien le découvrira au premier essai sur
  une cage, et il a deux façons d'en sortir — lire l'état, ce que tous les
  gestes existants font déjà, ou appeler `ensureCageNumbering` comme
  `ReadOptics` le fait.
- **`ReadOptics` paie une lecture de plus quand elle arrive seule.** C'est le
  coût du point 4, et il ne tombe que sur l'échantillonneur d'optique, qui
  n'avait aucune pré-lecture. En échange, sur un châssis `49..52`, elle ramène
  enfin quelque chose : avant, ses quatre commandes interrogeaient quatre
  interfaces inexistantes et le site dont les seules optiques sont ses uplinks
  ne rapportait aucun DDM. Convention malgré tout inétablissable : une ligne de
  `failures` dans ses notes, dans le mécanisme par-cage qui existait déjà.
- **La façade d'un châssis `49..52` perd sa sérigraphie** et affiche `XG1..XG4`.
  Exprimer `tg0/49..52` à l'écran demanderait un champ de décalage dans le
  contrat `PortGroup`, partagé par tous les connecteurs. Ce n'est pas fait ici
  (cf. les alternatives), donc l'opérateur de ce site lit un nom standard là où
  l'opérateur du labo lit la sérigraphie. Asymétrie assumée.
- **La latitude des constantes n'est pas refermée.** `sfpCageNumber` borne
  toujours sur `copperPorts = 48` / `sfpPorts = 4` et non sur les comptes
  déduits du modèle : sur un S3400-**24**T4SP dont les cages seraient numérotées
  25..28, la seconde fenêtre ne s'ouvre pas. Aucune capture ne dit ce que fait
  ce modèle, et l'élargir à l'aveugle inventerait de l'inventaire. Ce que cette
  décision change, c'est la **conséquence** de cette latitude : l'observation
  passe par `sfpCageNumber`, donc une numérotation que la lecture refuse reste
  inobservable, donc l'écriture vers une cage est **refusée avec une phrase** au
  lieu de partir sur `tg0/1`. La lacune est la même, son coût n'est plus le
  même — et le jour où `sfpCageNumber` dérivera ses fenêtres du modèle, les deux
  côtés suivront ensemble parce qu'il n'y a qu'une définition.
- **Un `Client` ne se partage pas entre équipements.** Rien ne le faisait, et
  rien ne doit commencer : la convention mémorisée sur une session serait alors
  celle du dernier équipement lu. C'est la raison pour laquelle ce champ n'est
  pas sur `Driver`.

## Alternatives écartées

**Deviner `tg0/1` et laisser le CLI trancher.** L'état actuel du dépôt, et donc
l'option par défaut. Écartée pour la raison 3 du contexte : le refus de la ligne
`interface` n'arrête pas le bloc, et les lignes suivantes s'exécutent dans le
contexte englobant. Le coût du pari n'est pas « un message d'erreur », il est
« un geste dont on ne sait pas dire ce qu'il a fait ». S'y ajoute le message qui
accuse la forme de commande au lieu du nom.

**Essayer les deux noms et garder celui que le CLI accepte.** Séduisant : la
session apprend par l'échec, sans pré-lecture. Écartée parce qu'une écriture
n'est pas une sonde. `interface tg0/1` sur un châssis `49..52` ouvre un bloc qui
échoue, et l'essai suivant se fait depuis un prompt dont on ne sait plus l'état.
Sonder avec une **lecture** serait acceptable, mais la lecture qui répond à la
question est déjà faite — payer un aller-retour pour redemander ce qu'on a lu
trente secondes avant n'a pas de justification sur une plateforme dont le pager
coûte quatre-vingt-dix secondes.

**Mettre la convention sur `Driver` et la laisser se remplir à la première
lecture.** Un champ de moins à faire circuler. Écartée sans hésitation : `Driver`
est unique pour la flotte, donc deux S3400 de builds différents partageraient le
fait. L'écriture sur l'un dépendrait de qui a été lu en dernier — un bug non
reproductible et dépendant de l'ordre, la pire catégorie sur un chemin
d'écriture.

**Faire de `cliPortName` une méthode de `*Client`.** Le plus court. Écarté parce
que les constructeurs de commandes — `portConfigCmds`, `portAdminCmds`,
`stormPortCmds` — sont **purs**, et c'est ce qui permet d'épingler la forme
exacte du fil dans un test sans session ni équipement. Le paramètre est donc le
**fait** (`cageNumbering`), pas la session : la pureté tient, et la convention
devient une entrée de test comme une autre.

**Déduire la convention du build lu dans la bannière** (`Build 130907` ⇒ `49..52`).
Écartée : ce serait une table de builds à tenir à jour contre un catalogue qu'on
ne contrôle pas, et le troisième build arriverait sans y être. L'équipement
**prononce** sa numérotation à chaque lecture ; déduire ce qu'on peut observer,
c'est remplacer une mesure par une hypothèse — la même erreur que faire porter
une décision par le mot « Series ».

**Observer dans `cliConn.runPaged`**, le goulot par lequel passe toute sortie,
pour qu'aucune lecture ne puisse être oubliée. C'était l'option la plus conforme
à la doctrine d'ADR-0017 (« la règle va là où on ne peut pas la contourner ») et
elle a été sérieusement envisagée. Écartée pour une raison précise : les
**écritures** passent par le même goulot, et le CLI fait l'écho de la commande
envoyée. Une session se mettrait alors à s'observer elle-même — `interface tg0/1`
écrit puis relu comme une preuve que l'équipement numérote à partir de 1. La
sûreté reposerait sur « on n'émet jamais un nom de cage avant de le connaître »,
un invariant vrai aujourd'hui et invisible depuis le transport.

**Ajouter `silk_from` au contrat `PortGroup`** pour afficher `tg0/49..52`.
Écartée ici, pas réfutée : c'est la bonne forme si la question se repose. Elle
touche un contrat partagé par tous les connecteurs, le frontend et ses tests de
libellés, pour un gain **cosmétique** sur un seul build d'un seul modèle. Le
mensonge est retiré maintenant ; son remplacement exact attend une raison plus
large que celle-ci.
