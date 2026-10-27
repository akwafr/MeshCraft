---
Statut: Accepté
Date: 2026-10-25
---

# ADR-0024 — Une alerte se mérite : le journal décide, la description et l'interco qualifient

## Contexte

Le produit observe déjà beaucoup, et n'avertit personne. Trois mécanismes
existent, mesurés et livrés :

**L'injoignabilité d'un équipement.** Le proxy pingue à 1 Hz, exige trois
pertes consécutives avant de conclure (`DefaultDownAfter = 3`,
`internal/tunnel/control.go:551`), et ne remonte que des **transitions**. Le
contrôleur agrège les verdicts par proxy (`judgeReach`,
`internal/api/reachability.go:103`) et écrit `devices.status`. Délai de
détection : **~2,75 s à 3,75 s**. Rétablissement instantané et asymétrique —
une seule réponse écho suffit. Le journal reçoit `device.unreachable`
(`SevWarning`) et `device.recovered` (`SevNotice`) ;
`internal/api/watch_handlers.go:1204`. Le verdict `unknown` n'est jamais
journalisé, délibérément : classer en `device.recovered` un équipement que
personne n'a encore regardé mettrait une guérison au journal pour une
ignorance.

**Le battement de port, déjà corrélé.** `internal/api/portevents.go` est un
corrélateur complet, en mémoire, horloge injectée, testé au Monte-Carlo :
`flapThreshold = 5` transitions dans `flapWindow = 5 min` ouvrent un épisode,
`flapCalm = 3 min` de calme le ferment, `dupWindow = 2 min` déduplique entre
proxies. Il produit `port.up` / `port.down` (`SevInfo`), et surtout
`port.flapping` (`SevWarning`) puis `port.flap_cleared`. **Le regroupement des
transitions en « ce port bat » est donc déjà écrit**, et son en-tête dit
pourquoi les sévérités ne suivent pas l'intuition : un port qui tombe n'est pas
une panne, c'est le quotidien d'une flotte de deux mille ports.

**Un filtre d'intérêt, déjà posé.** `portManaged`
(`internal/api/watch_handlers.go:1586`) refuse d'écrire au journal tout port
dont l'inventaire dit « libre ». Le raisonnement est écrit sur place :
journaliser chaque branchement de portable, c'est des centaines de lignes par
jour, et c'est noyer ce qui compte.

### Ce qui n'existe pas

- **Aucun code de notification.** Ni webhook, ni courriel, ni Telegram, ni
  Slack. `grep -i 'webhook|telegram|slack|smtp|notifier'` sur tout le Go et le
  TS ne rend que deux faux positifs (le bus de réveil des écrans, et un hook
  d'interface). C'est un terrain nu.
- **Aucune fenêtre de maintenance, aucun silence, aucune sourdine.**
  `maintenance|quiet|mute|suppress` ne rend que de la prose en commentaire.
- **Aucune description de port lue sur l'équipement.** Et c'est le fait central
  de cet ADR.

### La description de port n'est pas ce qu'on croit

`mdpv1.PortState` (`wire/mdpv1/state.go:68`) porte quatre champs — `Mode`,
`PVID`, `Tagged`, `TaggedAtLeast` — et son commentaire borne volontairement la
portée : « delibérément limité à ce qui est CONFIGURÉ ». Il n'y a **ni
`Description`, ni `Alias`, ni `Name`**. Le contrat MDP v1 ne sait pas
transporter une description de port.

Côté connecteurs, l'inventaire est net : **aucun** ne lit `ifAlias`
(1.3.6.1.2.1.31.1.1.1.18). L'OID n'apparaît nulle part sauf dans deux fixtures
de test. Les neuf pilotes externes walkent `ifDescr` / `ifName`, et s'en servent
pour **résoudre un nom de port**, jamais comme description. Un seul endroit
touche la colonne : le parseur Arista `show interfaces status`
(`internal/driver/arista/eos/parse.go:243`) dont la regex **capture** la colonne
Name — `Et49  core-uplink  connected` — et ne lit jamais `m[2]`. La donnée est
sur le fil, elle est appariée, elle est jetée.

Ce que l'écran *appelle* une description est autre chose :
`devices.ports.<port>.label`, une annotation **tapée par un opérateur de
meshcraft**. L'écran Ports l'intitule « description »
(`portsPage.describePlaceholder`, `web/src/pages/Ports.tsx:273`). Et le produit
la **protège activement** des lectures d'équipement : la migration
`0019_purge_ports_fantomes.sql:25` la classe comme INTENTION et ne la purge
jamais ; `internal/api/recon_handlers.go:1538` dit « la configuration vient de
l'équipement, le label reste à meshcraft » ; `internal/store/devices.go:954`
laisse `label` intact en absorbant une dérive.

Il y a donc deux candidats pour « port avec description », et ils ne disent pas
la même chose. `ifAlias` dit « quelqu'un a décrit ce port **sur l'équipement** »
— souvent un prédécesseur, souvent périmé, parfois un inventaire automatique
sans aucune intention. `label` dit « quelqu'un a décrit ce port **dans
meshcraft**, dans l'écran qui sert à le faire ». Le second est à la fois le seul
disponible et le meilleur signal.

### Ce qui a forcé la question

La demande est arrivée en une phrase : alerter sur les équipements UP/DOWN, sur
les ports d'interco UP/DOWN, sur les ports **avec description** UP/DOWN — « les
ports sans description, on s'en fiche ». Et dans la nuit du 24 au 25, huit
commutateurs de ARENA ont eu leur région MSTP réécrite. Si un alerting avait
tourné, il aurait sonné sur chaque transition de port causée par **notre propre
geste**.

## Décision

**L'alerting est un abonné du journal d'événements, pas un second modèle
d'événement** ; et une alerte de port n'est émise que si le port **porte une
description d'opérateur** (`devices.ports.<port>.label` non vide) **ou fait face
à un équipement de la flotte**.

Six règles, qui tiennent chacune en une phrase.

**1. Le journal est la seule source.** Pas de nouveau flux d'observation, pas de
seconde détection. Un `notify_deliveries` référence `events.id` par clé
étrangère : une alerte est le **bordereau de livraison** d'une ligne de journal,
jamais un fait concurrent. Le vocabulaire alertable est exactement six types
déjà émis : `device.unreachable`, `device.recovered`, `port.down`, `port.up`,
`port.flapping`, `port.flap_cleared`.

**2. Trois classes, et une règle d'exclusion.** Un abonnement choisit parmi
`device` (l'équipement), `interco` (port face à un équipement connu), `described`
(port portant un label). Un événement de port qui n'est **ni** interco **ni**
décrit n'engendre aucun bordereau — il reste au journal, visible sur la fiche du
port, et ne réveille personne. Un port à la fois interco et décrit compte comme
`interco` : la classe la plus conséquente gagne.

**3. « Interco » est dérivé, jamais stocké.** Un port est interco si
`LLDPNeighbor.PeerDevice != ""` pour ce port (reconnaissance faite à la lecture
par `identifyPeer`, `internal/api/lldp.go:289`) **ou** si une ligne
`device_links` touche ce port. C'est l'union que `topologyLinks`
(`internal/api/stp_rootbridge.go:316`) assemble déjà : le déclaré d'abord, puis
l'observé dont le bout lointain est reconnu. Aucune colonne `is_uplink`, aucune
énumération de rôle : le jour où l'opérateur adopte l'équipement d'en face, le
port devient interco sans réécrire une table.

**4. Une clé qui bouge n'alerte pas.** Tout événement est **retenu** avant
livraison — `alertHold = 45 s` pour un port, `alertDeviceHold = 20 s` pour un
équipement. Une deuxième transition sur la même clé dans la fenêtre **annule la
livraison et repousse l'échéance** : tant que la clé bouge, rien ne part. La
livraison a lieu quand la clé s'est **tue** pendant la fenêtre — un seul
message, portant l'état final et le nombre de transitions vues — ou quand
`port.flapping` arrive, et c'est alors ce message-là qui part. Plafond
`alertHoldMax = 5 min` pour qu'un port qui bat sans fin parle quand même. Six
allers-retours en deux minutes produisent **un** message.

Aucune classe n'échappe à la retenue, et il a fallu s'y reprendre. La première
version exemptait l'équipement, au nom de la latence : sa détection est déjà
anti-rebondie là où il faut (trois pertes ICMP consécutives chez le proxy), et
un équipement tombé ne l'est pas moins au bout de 45 s. Les deux moitiés sont
vraies et la conclusion ne suit pas, à cause de l'**asymétrie** : trois pertes
pour tomber, **une seule réponse écho pour remonter**. Un équipement derrière un
lien qui bat produit donc une paire descente/remontée toutes les quelques
secondes, chacune un vrai changement de statut que `SetDeviceStatus` ne
dédupliquera pas — et, sans retenue, chacune un message. C'était la tempête du
piège n°1 entrant par la porte laissée ouverte pour la latence. 20 s la
referment pour un coût que personne ne perçoit : nul ne distingue 3 s de 23 s
sur une notification de téléphone.

**5. Une alerte attribuable à notre geste est différée, jamais perdue.** Un
candidat est supprimé si, dans les `selfActionWindow = 10 min` qui précèdent, le
journal porte pour cet équipement un événement de **geste** (`config.applied`,
`device.port_admin`, `device.stp_apply`, `mgmt.applied`, `credential.installed`,
`device.adopted`, `proxy.upgraded`…) ou si `applyQueuedOrRunning(deviceID)`
(`internal/api/autoapply.go:77`) est vrai. La suppression ne retarde **que les
mauvaises nouvelles** — la doctrine déjà éprouvée par `holdReach`
(`internal/api/reachability.go:284`) — et un balayage **ré-arme** l'alerte à la
fermeture de la fenêtre si l'état est toujours mauvais. Le journal nous sert
donc deux fois : il dit ce qui s'est passé, et il dit que c'est nous.

**6. La livraison est un effet, pas une condition.** Le bordereau est écrit en
base avant tout appel sortant. Un canal injoignable fait avancer `attempts`,
`last_error`, `next_attempt_at` et finit en `failed` — l'alerte **a existé**, le
produit sait le dire, et la détection n'a jamais attendu. Chaque canal porte
`last_ok_at` / `last_error` / `consecutive_failures`, et un **test d'envoi**
explicite est offert à l'écran. Les secrets (jeton de bot Telegram, URL de
webhook Slack) vont dans `internal/secretbox`, sont inscrits dans
`store.SealedColumns()`, ne sont jamais rendus par la route de liste — un
`has_token` suffit — et ne sont révélés que par une route qui journalise
d'abord.

## Conséquences

**Un port critique que personne n'a décrit reste muet.** C'est l'angle mort
assumé, et il faut le nommer sans l'adoucir : la règle fait de la description un
*signal d'intérêt humain*, et elle hérite donc de toute la négligence humaine.
Un port d'onduleur, un port de caméra, un port de contrôleur d'accès — branchés
à la hâte, jamais décrits, jamais face à un équipement meshcraft — tombent sans
un mot. Trois atténuations, aucune n'est une solution :

- l'interco rattrape tout ce qui fait face à la flotte, sans description ;
- l'écran **compte et affiche** ce qu'il a tu (« 127 transitions de port non
  alertées : ni description ni voisin connu, sur 14 ports »), ce qui transforme
  l'angle mort en **chiffre visible** au lieu d'un silence ;
- décrire un port est une action d'un champ, dans un écran qui existe déjà.

Le prix est accepté parce que l'autre sens coûte plus : alerter sur tout port
managé, c'est, sur ARENA et ses 88 clients, un canal que personne ne lit au
bout d'une semaine.

**L'alerting est en retard sur la détection, exprès.** 20 s de retenue sur un
équipement, 45 s sur un port, jusqu'à 5 min sur un port instable, 10 min de plus
derrière un de nos gestes. Le produit **détecte** en 3 s et **alerte** bien plus
tard. Qui veut la seconde près regarde l'écran, qui veut dormir prend le canal.
Ça s'écrit, parce que quelqu'un mesurera l'écart et croira à un bug.

**Une fenêtre de geste peut masquer une vraie panne simultanée.** Dix minutes
après un `apply`, un équipement qui tombe pour une raison étrangère est différé.
Le ré-armement le rattrape — au pire dix minutes plus tard — mais le délai est
réel, et il est le prix de ne pas sonner sur nos propres écritures.

**Le journal devient un chemin chaud, et il n'a aucune purge.**
`internal/store/events.go` se déclare append-only et la table grandit sans
limite ; aucune migration n'a jamais ajouté de TTL. Brancher l'alerting dessus
rend cette absence plus coûteuse. On ne la corrige pas ici, on l'inscrit.

**Il faut toucher les points d'émission du journal.** `emit`
(`internal/api/events_handlers.go:16`) couvre ~143 appels, mais `emitDeviceBG`
(`autoapply.go:178`) et **vingt appels directs** à `Store.InsertEvent` le
contournent — dont `writePortDecisions`, qui est précisément le chemin des
événements de port. Le crochet doit couvrir les deux, et c'est un travail
mécanique mais non nul.

**Les ports collectés en gNMI n'émettront aucune alerte de port.** Bug trouvé en
établissant cet état des lieux, et il préexiste à cette décision :
`cmd/proxy/gnmi.go:933` émet ses transitions **sans `IfIndex`**, et
`portFlaps.observe` (`internal/api/portevents.go:345`) rejette tout ce qui a
`IfIndex <= 0`. Aucun `port.up` / `port.down` / `port.flapping` n'est donc jamais
écrit pour un Arista. La colonne `link` se met à jour (ce chemin apparie par
nom), les écrans sont justes, et le journal est muet. L'alerting hérite du trou
tel quel ; `alertHoldMax` est ce qui évite qu'un port Arista instable soit
*aussi* retenu indéfiniment, puisque le `port.flapping` qui devrait le libérer
n'arrivera jamais. **À corriger séparément**, et c'est l'alerting qui rendra ce
trou visible.

**On s'interdit désormais** d'ajouter un type d'événement « d'alerte » qui ne
soit pas d'abord une ligne de journal, et d'évaluer l'intérêt d'un port ailleurs
qu'à la décision de livraison. Une alerte sans ligne de journal correspondante
serait indébogable.

## Alternatives écartées

**Lire `ifAlias` et alerter sur la description de l'équipement.** C'est la
lecture littérale de la demande, et elle perd pour trois raisons cumulées.
D'abord le coût : `mdpv1.PortState` n'a pas le champ, donc il faut un champ
optionnel dans `wire/mdpv1`, régénérer `schemas.json`, écrire `openapi.yaml` à
la main, une migration, et une lecture **par pilote** sur neuf pilotes externes
plus le legacy — pour une fonctionnalité dont la détection, elle, est déjà là.
Ensuite la sémantique : `ifAlias` est souvent un reliquat, posé par un
prédécesseur ou par un inventaire automatique, et « ce port porte un texte » n'y
veut pas dire « quelqu'un s'en soucie aujourd'hui ». Enfin la collision : il
faudrait arbitrer `ifAlias` contre `label`, c'est-à-dire décider qui gagne entre
l'équipement et l'opérateur — exactement le partage observation/intention que
les migrations 0019 et `recon_handlers.go` ont déjà tranché dans l'autre sens.
Le `label` donne le même service tout de suite, avec un signal plus propre. Si
`ifAlias` est lu un jour, ce sera pour l'**afficher à côté** du label, et un
nouvel ADR dira lequel qualifie.

**Alerter sur tous les ports managés, et laisser l'opérateur filtrer.**
`portManaged` existe déjà et exclut les ports « libres » : la tentation est de
s'arrêter là. Mais « managé » veut dire « l'inventaire lui a donné un mode », ce
qu'ont la grande majorité des ports d'accès d'un site configuré. Sur ARENA,
c'est tous les ports qui portent les 88 clients. Un filtre côté destinataire
n'existe pas dans Telegram, et un filtre côté canal que personne ne règle est un
canal que personne ne lit. L'exclusion doit être le **défaut**, pas une option.

**Un modèle d'alerte autonome, avec ses propres sondes.** L'approche classique :
une table `alerts`, des règles, un évaluateur qui interroge l'état périodiquement.
Écartée parce qu'elle construit une seconde vérité à côté du journal. Deux
systèmes qui décident séparément si un équipement est tombé divergeront, et le
jour où le canal dit une chose et l'écran l'autre, c'est la confiance dans les
deux qui part. Ancrer sur `events.id` rend chaque message **traçable** jusqu'à
la ligne qui l'a causé.

**Un second corrélateur de battement dans l'alerting.** Tentant, parce que
`portevents.go` est en mémoire et que ses seuils (5 en 5 min) sont choisis pour
le journal, pas pour un canal. Écartée : deux anti-flaps en série, c'est deux
jeux de seuils à accorder, et un comportement que personne ne sait prédire. La
retenue de 45 s n'est pas une seconde corrélation, c'est une **temporisation de
livraison** — elle ne décide pas si le port bat, elle attend que la question
soit tranchée, par le corrélateur ou par le silence.

**Une fenêtre de maintenance explicite, posée à la main.** L'outil habituel, et
il aurait sa place un jour. Mais il suppose qu'on pense à l'ouvrir — et la nuit
du 24, personne ne l'aurait fait avant de réécrire huit régions MSTP. Le journal
sait déjà que nous avons agi, horodaté, par équipement : s'en servir est à la
fois gratuit et infaillible sur l'oubli. Une fenêtre manuelle resterait utile
pour un geste **hors** meshcraft (un électricien, un déménagement de baie) ;
c'est un ajout, pas ce socle.

**Un battement de cœur périodique vers le canal.** La réponse réflexe au
silence ambigu, et elle est écartée **comme défaut** : un message quotidien « tout
va bien » est exactement ce qu'un lecteur apprend à ne plus voir, et il entraîne
à ignorer le canal. Le silence est désambiguïsé autrement, et sans rien envoyer :
le bordereau persiste indépendamment de la livraison, chaque canal affiche
`last_ok_at` et son dernier échec, l'écran compte les livraisons en attente et en
échec, et le test d'envoi permet de vérifier **avant** l'incident. « Le canal
n'a rien envoyé » devient donc une question à laquelle l'écran répond. Un
battement de cœur **optionnel**, par canal, reste un ajout légitime pour qui
surveille son alerting depuis l'extérieur ; il n'est pas le socle.

**Mettre les jetons dans `.env`.** Écartée pour la raison d'ADR-0015 : un secret
par canal, modifiable à l'écran, appartient au coffre. Et l'inscription dans
`SealedColumns()` n'est pas optionnelle — une colonne scellée absente de cet
inventaire est **détruite à la prochaine rotation de clé**, ce que le test de
garde `TestInventaireDesColonnesScellees` (`internal/store/rekey_test.go:44`)
attrape dans les deux sens.
