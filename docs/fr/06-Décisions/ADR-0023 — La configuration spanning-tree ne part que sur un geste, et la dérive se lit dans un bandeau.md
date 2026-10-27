---
Statut: Accepté
Date: 2026-10-25
---

# ADR-0023 — La configuration spanning-tree ne part que sur un geste, et la dérive se lit dans un bandeau

## Contexte

ADR-0021 a rendu l'identité de région MST **calculable et visible au niveau du
site**. ADR-0022 a rendu la capacité de la poser **déclarée** par le connecteur.
Ni l'une ni l'autre n'a touché au **moment** où meshcraft écrit du
spanning-tree, et c'est là que vit le danger.

Aujourd'hui, `internal/api/autoapply.go` appelle `applyDeviceSTP` **dans
`runApply`**, c'est-à-dire dans le geste « appliquer les ports de cet
équipement ». Le commentaire de `runApply` le dit sans détour : *« Runs even
when ApplyPorts failed: VLAN drift must not block edge / mode push. »* Donc un
changement de VLAN sur un port d'accès entraîne une écriture du mode global, de
la priorité de pont **et du triplet 802.1s** sur l'équipement. Personne n'a
demandé cette écriture, personne ne la voit passer, et aucun écran ne dit
qu'elle a eu lieu.

### Trois démonstrations, toutes de la nuit du 2026-10-24 au 2026-10-25

1. **Un renommage de site a éjecté sept Instant On de leur région, sans
   symptôme** (ADR-0021 et son addendum). Sept équipements sur `site-exec`,
   un seul — ArubB — sur `arena`, parce que seul ArubB avait été ré-appliqué
   après le renommage. La scission a tenu des semaines. Le vecteur n'était pas
   un geste STP : c'était un apply de ports, qui emportait la région avec lui.

2. **`core01-arena` est racine du CIST à la priorité 0 avec dix ports
   frontière.** Le faire rejoindre la région déplace les VLAN 10, 11, 42, 60,
   98, 99 et 100 du CIST vers l'instance 1. Mesure du geste, écrite dans
   ADR-0022 : recalcul de topologie sur le cœur du site. Un apply de ports sur
   cet Arista aurait suffi à le déclencher, et rien dans l'écran de ports ne
   l'annonce.

3. **`sw01-arena` (FS S3400) déclarait `WritesExtra("stp")`, recevait un plan
   portant une région, écrivait son pont et ne disait rien** — un `apply`
   réussi au-dessus d'un commutateur hors de l'arbre de son site. C'est ce que
   ferme ADR-0022 côté *parole* ; cet ADR ferme le **geste** qui l'a provoqué.

### Ce que les trois ont en commun

Dans les trois cas, la grandeur écrite est **dérivée de données de site** — le
nom de région vient du plan ou de l'UUID, la table VLAN→instance vient de la
liste de VLAN, le mode vient de la liste de préférence. **Personne n'a jamais
nommé cet équipement-là.** Une écriture que personne n'a nommée sur une
grandeur que personne ne voyait : les deux moitiés de la même panne.

Et la grandeur écrite est précisément celle dont une modification **recalcule
une topologie**. Les mesures existantes : 2 à 5 s d'injoignabilité et environ
45 s de reprise sur le FS lors d'un changement de mode (relevé 2026-08-11,
en-tête de `internal/driver/fs/s3400/stp.go`), les ports repassant par
listening/learning à chaque déplacement de frontière MST.

## Décision

**Aucune écriture spanning-tree ne voyage sur un autre geste. Le contrôleur
n'écrit du spanning-tree que sur un geste qui le nomme — et quand la
configuration portée diverge de celle que le site veut, un bandeau au-dessus de
l'équipement l'énonce et propose « Conformer ».**

Quatre pièces.

### 1. L'apply automatique ne confirme que l'identique

`applyDeviceSTP` — l'appelé de `runApply` — relit l'arbre **avant** de décider,
compare, et n'écrit **que si l'équipement porte déjà l'identité que le site
veut** : même mode résolu, et même triplet de région quand le site en a un.
Toute divergence arrête l'écriture ; rien ne part, et une note nomme ce qui
diverge.

La relecture est faite sur la session déjà ouverte par `adminFor`, et son
résultat est persisté dans `extra.stp_observed` — donc le bandeau lit une
observation que l'apply vient de rafraîchir, et non un instantané de la veille.

Ce qui reste donc automatique est une **re-confirmation de ce qui est déjà en
place**, et c'est vérifiable plutôt que supposé : le mode ré-émis est le mode
qui tourne, la région ré-émise est la région portée, le condensé MD5 ne bouge
pas, donc aucune frontière MST ne se déplace. Les quatre connecteurs qui savent
écrire une région sont déjà idempotents sur ce chemin (`procurve` émet un
différentiel, `cisco/ios` a un chemin « already as planned », `fs-s3400`
compare avant d'écrire le mode, et `arista-eos` comme `instanton` ré-écrivent la
même valeur).

### 2. « Première pose » et « conformation » ne se distinguent pas depuis le contrôleur

La question méritait d'être posée : un équipement qu'on vient d'adopter n'a pas
d'arbre à casser, un équipement en service oui. **La distinction est réelle dans
le monde et illusoire dans le code.**

`core01-arena` est la preuve. Sa région est **vide** — exactement ce que porte
un équipement sorti d'usine — et c'est le commutateur le plus dangereux à
écrire de tout le site : racine du CIST à la priorité 0, dix ports frontière.
Dans ce produit, l'adoption est une **découverte** d'équipements déjà en
service, pas une installation : la flotte ARENA a été découverte. Donc « cet
équipement ne porte pas encore de région » ne veut pas dire « cet équipement
est neuf », et aucune lecture ne permet de trancher.

Il n'y a donc **pas de cas automatique de première pose**. Un équipement qui ne
porte pas déjà l'identité du site attend le geste, qu'il soit sur un établi ou
au cœur d'une production.

### 3. `POST /api/devices/{id}/stp/apply` devient le geste de conformation, sans changer de forme

Il était déjà le seul chemin d'écriture explicite, par équipement, avec son mode
nommé dans le corps. Il le reste, et c'est ce que « Conformer » appelle. **On ne
distingue pas « proposer » et « poser » par deux routes**, parce que la
proposition est déjà une **lecture** : le triplet que le site veut est
`GET /api/sites/{siteID}/stp-mst-region` (ADR-0021), ce que l'équipement porte
est `stp_observed`, et la comparaison des deux est servie sur la vue
d'équipement — dans `deviceView.stp_region`, à côté de `stp` et `stp_caps`, sans
session sur aucun équipement. Une troisième route qui « proposerait » serait un
troisième endroit où le triplet se calcule, et ADR-0021 a payé pour savoir ce
que coûtent deux endroits de vérité sur cette grandeur.

La confirmation est **dans l'interface**, pas dans la route : un
`ConfirmDialog` qui énonce le prix avant d'appeler. La route n'exige pas de
jeton `confirmed` — cf. Conséquences.

### 4. Le bandeau énonce les trois composantes, et n'offre le bouton que là où il mène quelque part

Au-dessus de l'équipement, dans la pile de `DeviceBanners`, quand le site veut
une région (sa préférence contient `mstp`) et que l'équipement ne la porte pas
démonstrativement :

- il nomme **le nom attendu contre le nom porté, la révision attendue contre la
  révision portée, et la table VLAN→instance attendue contre la portée** — les
  trois composantes de l'identité 802.1s, jamais « la configuration diffère ».
  C'est exactement ce que personne ne voyait ;
- il marque laquelle des trois diverge, parce qu'une seule suffit à scinder la
  région et que les trois ne se réparent pas de la même façon ;
- quand le connecteur sait rejoindre une région (`mdpv1.FeatMSTRegion`), il est
  en `tone="act"` et porte **« Conformer »**, qui demande confirmation et dit le
  prix : recalcul de topologie, ports repassant par listening/learning, 2 à 5 s
  d'injoignabilité et environ 45 s de reprise ;
- quand le connecteur ne sait pas (`fs-s3400`), il le dit et **n'offre pas le
  bouton** — et il passe en `tone="info"`, parce qu'UI-GUIDE §2 réserve
  l'orange à ce qui appelle une décision et qu'un bandeau orange sans geste
  pendant des mois est la définition de ce qui apprend à ignorer l'orange.

**La comparaison se fait état à état**, jamais configuration à état. Les deux
côtés sont des valeurs structurées : l'intention sort de la base, l'observation
sort de `ReadSpanningTree`. Deux normalisations sont obligatoires et sont la
raison pour laquelle la comparaison vit dans le contrôleur et non dans le
navigateur :

- **l'instance 0 est implicite et ne se compare pas.** EOS annonce
  `Instance 0  1-4094`, le FS aussi ; `mdpv1.MSTRegion.InstanceVLANs` ne la
  porte jamais. Comparer les tables brutes afficherait une dérive permanente
  sur toute la flotte ;
- **un nom de région égal à la propre adresse de pont de l'équipement n'est pas
  un nom.** C'est le défaut d'usine sur chaque plateforme relevée — le FS
  portait `649D9929F706`, sa propre MAC — et il signifie « cet équipement est sa
  propre région », pas « il est dans la région 649D9929F706 ».

### 5. `FeatMSTRegion` est déclaré partout où il est vrai

ADR-0022 n'a déclaré la capacité que sur `arista-eos` et ne l'a refusée que sur
`fs-s3400`. Les trois autres connecteurs qui émettent le triplet complet —
`instanton`, `hpe/procurve`, `cisco/ios` — ne la déclaraient pas, par omission
et non par décision.

Sans cette pièce, la décision ci-dessus serait inerte : le bandeau n'offrirait
« Conformer » sur aucun équipement de ARENA sauf l'Arista, c'est-à-dire pas
sur les sept Instant On que l'addendum d'ADR-0021 désigne nommément comme ceux
à remettre en région. **Une porte qui ne s'ouvre nulle part n'est pas une
porte.**

La déclaration ne promet rien de neuf — la capacité signifie « ce CODE émet une
région », et les trois l'émettent : nom, révision et `instance N vlan …`. Sur
`instanton` elle est en outre prouvée sur matériel : huit des douze Aruba de
ARENA portent `mst-7f0d238df180`, révision 1, instance 1 =
`10-11,42,60,98-100`, écrits par ce connecteur.

Et le relevé de la base ce jour ajoute un fait que l'addendum d'ADR-0021 ne
pouvait pas voir : **quatre Instant On divergent encore** — ArubE, ArubI et
ArubM sur `site-exec` avec la bonne table, et **ArubK** sur `site-exec`
avec en plus l'ancienne table `42,98-100`. Ce sont exactement les équipements
que le bouton doit atteindre, et sans la déclaration il ne les atteindrait pas.

## Conséquences

- **Les intentions STP par port cessent d'être automatiques, et c'est le prix
  principal.** `portfast`/edge, BPDU guard et coût de chemin voyagent sur le
  même `SpanningTreePlan` que le mode et la région (c'est déjà ce que
  `stpPreferenceEmptyNote` énonce au sujet de l'interrupteur par site). Sur un
  équipement **conforme**, ils continuent d'arriver, puisque la pièce 1
  autorise la re-confirmation de l'identique. Sur un équipement **divergent**,
  ils attendent le geste, donc un port d'accès peut rester sans BPDU guard
  tant que personne ne clique.

  Accepté, et l'arbitrage est explicite : l'alternative est de laisser le mode
  et la région partir pour faire passer la garde BPDU, c'est-à-dire de couper
  un arbre pour protéger un port. Et le retard est **visible** — c'est la
  raison d'être du bandeau — alors que l'écriture silencieuse ne l'était pas.

- **Un bandeau qu'on ignore est une dérive qui dure. C'est le prix direct de
  ce choix et il est assumé.** Trois dispositions le rendent supportable sans
  le rendre harcelant, et aucune des trois n'est un rappel :

  1. **Il n'y a pas de bouton « ignorer ».** Jamais. Un bandeau qu'on peut
     masquer est une dérive qu'on ne reverra plus, c'est-à-dire exactement
     l'état d'avant ADR-0021 avec une case cochée en plus.
  2. **Il n'apparaît que là où il y a quelque chose à décider** : seulement sur
     les équipements qui divergent, seulement quand le site veut une région.
     Sur une flotte saine la pile de bandeaux reste vide, donc la présence du
     bandeau porte de l'information.
  3. **Il informe au lieu de réclamer.** Il nomme les trois composantes et
     leur écart. Le lire suffit à décider d'attendre — et attendre en sachant
     est un choix d'exploitation légitime, dont l'addendum d'ADR-0021 donne le
     cas réel : « aucun apply STP sur ArubL, ArubA, ArubC, ArubD, ArubF, ArubG,
     ArubH sans l'avoir planifié ».

- **La route d'écriture n'exige pas de jeton `confirmed`.** La confirmation est
  dans l'interface. La raison est un arbitrage et non une facilité :
  `POST /api/devices/{id}/stp/apply` est déjà appelée par le panneau STP et par
  `cmd/mcp` (`tools_write.go`), et exiger un champ nouveau les casserait tous
  les deux pour une garantie que le `ConfirmDialog` fournit déjà. La
  conséquence assumée est qu'un appel direct à l'API écrit sans confirmation —
  ce qui est vrai de toutes les routes d'écriture de ce produit, et un appel
  direct à une route nommée `stp/apply` est un geste qui se nomme.

- **La garde ne couvre ni la priorité de pont ni le drapeau Super Root, et c'est
  une limite assumée.** Le principe est « aucune écriture que personne n'a
  nommée », pas « aucune écriture » : ces deux-là sont des intentions par
  équipement — le drapeau stocké sur l'équipement, ou la désignation de racine du
  site, qui nomme les équipements un par un et porte déjà son propre avertissement
  de racines contestées et son ordre d'application. Elles continuent donc de
  voyager sur l'apply, **y compris l'écriture de la priorité 0**, qui déplace une
  racine. Quelqu'un qui trouvera ça incohérent aura un argument : c'est écrit ici
  pour qu'il le trouve écrit plutôt qu'à découvrir.

- **Un apply coûte une lecture de plus.** `applyDeviceSTP` relit l'arbre avant
  de décider, sur la session déjà ouverte. C'est un aller-retour
  supplémentaire par apply, et il est payé délibérément : décider sur
  l'observation persistée aurait permis le cas précis qu'on ferme — une
  observation périmée disant « conforme » au-dessus d'un équipement qui a
  dérivé depuis, donc une ré-émission de l'identité du site sur un équipement
  divergent, donc le renommage.

- **Le bandeau dépend d'une observation, donc il peut être muet.** Un
  équipement jamais lu, ou dont le connecteur ne lit pas la région, ne produit
  aucun verdict : le bandeau ne s'affiche pas, et son absence ne prouve pas la
  conformité. C'est le revers de la règle état-à-état, et c'est préférable au
  contraire : afficher « dérive » sur une absence de lecture apprend à ne plus
  croire le bandeau.

- **La conformation reste dangereuse, et le produit ne la rend pas sûre — il la
  rend choisie.** « Conformer » sur l'un des sept Instant On de `site-exec` le
  renomme en `mst-7f0d238df180` et le sort de la région des six autres. Le
  bandeau affiche le nom attendu contre le nom porté, donc l'information est là,
  mais **l'ordre des gestes sur une flotte scindée n'est pas un bandeau par
  équipement** : c'est une campagne, et elle n'est pas dans cet ADR. La
  convergence inverse — épingler `site-exec` par `PUT /api/sites/{id}/plan`
  — reste le chemin à un seul geste décrit par l'addendum d'ADR-0021.

- **Déclarer `FeatMSTRegion` sur trois connecteurs de plus change le gating de
  l'interface sur des installations en service.** C'est additif et ne déclenche
  aucune écriture : la clé ne décide d'aucun comportement d'apply, seulement de
  ce qu'un écran propose. Sur `hpe/procurve` et `cisco/ios` elle repose sur la
  lecture du code et non sur un apply observé en laboratoire ; c'est le niveau
  de preuve que la clé demande — « ce code émet-il le triplet ? » — mais
  l'inventaire des régions réellement posées par ces deux gammes n'a jamais été
  relevé.

## Alternatives écartées

**Ne rien automatiser du tout : retirer `applyDeviceSTP` de `runApply` et point
final.** Le plus court et le plus facile à défendre en une phrase. Écartée parce
qu'elle fait tomber les intentions STP par port — BPDU guard sur les ports
d'accès — sur **toute** la flotte, y compris sur les équipements parfaitement
conformes où la ré-émission est démontrablement un non-événement. On aurait payé
la protection de tout le monde pour le risque de quelques-uns, et la garde BPDU
qui cesse d'arriver est elle aussi une dégradation silencieuse : c'est la forme
même de la panne que cet ADR combat, retournée.

**N'exclure que la région, et laisser le mode et le reste rouler.** Séduisant :
la région est la grandeur des trois démonstrations. Écartée sur la mesure du
2026-08-11 : sur le FS, `spanning-tree mode <m>` **ferme la session SSH avant
que la CLI réponde** et laisse l'arbre illisible une quarantaine de secondes.
Le mode est au moins aussi coupant que la région, et une règle qui protège une
grandeur sur deux est une règle qu'on croira protectrice.

**Un drapeau par site « autoriser les écritures STP automatiques ».** Écartée
parce que l'interrupteur existe déjà et que son histoire est instructive : une
préférence de modes vide désarme tout l'apply STP du site
(`stpPreferenceEmptyNote`), et ce commentaire de trente lignes existe parce que
personne ne devinait **l'étendue** de ce que l'interrupteur coupait. Un second
interrupteur, de granularité différente, sur la même grandeur, se contredirait
avec le premier dès la première combinaison, et c'est l'opérateur qui
découvrirait laquelle gagne.

**Un bandeau dismissible, ou avec un rappel différé (« me le redire dans 30
jours »).** La réponse naturelle à « un bandeau qu'on ignore ». Écartée : ADR-0021
a mesuré ce que coûte une dérive invisible, et un masquage est un retour à
l'invisible avec l'illusion d'une trace. Si quelqu'un veut suivre les écarts
dans le temps, c'est un inventaire de flotte — une liste de tous les
équipements hors région du site, triable — pas une case à cocher sur un
équipement. Ce serait un écran, et il mériterait sa propre décision.

**Faire la comparaison dans le navigateur, à partir de
`GET /api/sites/{siteID}/stp-mst-region` et de `dev.stp.region`.** C'est le
chemin le plus court vers un bandeau : les deux données sont déjà servies.
Écartée sur les deux normalisations de la pièce 4. L'exclusion de l'instance 0
et la reconnaissance d'un nom-MAC comme « pas de nom » sont des règles 802.1s
et de plateforme, pas de présentation ; `IonRegionIsDefault` et le filtre
`Instance == 0` d'`arista-eos` existent déjà côté Go. Les retranscrire en
TypeScript crée un second jeu de règles qui dérivera, et la dérive se verra
sous la forme d'un bandeau qui se trompe — ce qu'on ne pardonne pas à un
bandeau dont tout l'objet est d'être cru.

**Une route de comparaison par équipement,
`GET /api/devices/{id}/stp/region-conformity`.** Honnête, et c'était l'autre
option sérieuse. Écartée parce qu'il n'existe pas de `GET /api/devices/{id}` :
`withConformity` est le seul endroit où un enregistrement d'équipement est
publié (son propre commentaire l'écrit, et prévient celui qui ajoutera le GET
unitaire). Ajouter la route obligerait le bandeau à une seconde requête par
équipement sur une page qui a déjà la liste, et laisserait la vue de liste —
donc toute vue de flotte future — incapable de dire qui diverge. Le verdict
voyage sur le payload qui porte déjà `stp`, `stp_caps` et `stp_super_root`, et
il ne coûte aucune session : le plan de site est lu une fois par site, comme le
plan d'adressage et les VLAN à côté de lui.

**Déduire « conforme » de l'absence de ligne `Skipped` au dernier apply.** La
donnée existe, dans `last_apply`. Écartée parce qu'elle répond à une autre
question : `Skipped` dit ce que le connecteur a refusé d'écrire **la dernière
fois qu'on a écrit**, et la dérive dont il s'agit ici naît précisément quand on
n'écrit pas — un VLAN ajouté au site déplace la région de tous les équipements
sans qu'aucun apply n'ait lieu. Un verdict fondé sur le dernier apply serait
vert sur une flotte entière que le site vient de laisser derrière lui.
