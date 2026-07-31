---
tags: [architecture, monitoring, connecteurs, mikrotik]
---

# Télémétrie MikroTik — CSS326 et CRS326

Prolonge [[Monitoring]] et [[Monitoring unifié et pipelines]] sur les deux plateformes
MikroTik du parc de référence, et répond à une question que ces deux pages posaient sans
la trancher : **par quel canal obtient-on quoi, sur des équipements qui n'ont ni le même
firmware ni les mêmes capteurs**, alors qu'ils sortent du même constructeur ?

Les deux équipements sont dans le même laboratoire, ils portent la même référence de
châssis à un préfixe près, et **tout ce qui suit les sépare** :

| | `lab-css326` — CSS326-24G-2S+ | `lab-crs326` — CRS326-24G-2S+ |
|---|---|---|
| Firmware | **SwOS** 2.17 | **RouterOS** 7.21beta3 |
| Connecteur | `internal/driver/mikrotik/swos` | `internal/driver/mikrotik/routeros` |
| Canal de télémétrie | scraping de pages `*.b` (HTTP) | REST, **API binaire**, SNMP |
| Interrogé en SNMP aujourd'hui | oui, depuis le relevé du 29/07/2026 (§4) | oui — mais **muet** jusqu'au 29/07/2026, l'agent refusant la source du proxy (§4.6) |
| Charge processeur, mémoire | **jamais** : le firmware ne les publie pas | oui |
| Diagnostic optique | oui, par un simple GET | oui, par une **commande** (§2) |
| Débit négocié d'un lien | oui, `/link.b` | **pas par la REST** (§2) |
| **Voisinage LLDP** | **jamais** : le firmware ne parle pas 802.1AB — walk de `1.0.8802` **vide**, mesuré le 29/07/2026 | oui, LLDP-MIB complète en SNMP ([[Voisinage LLDP]]) |

Ce que le catalogue déclare, au passage, parce que c'est une question qui revient :
`internal/catalog` ne connaît **aucun** de ces deux modèles. Il ne porte que des gabarits
`Generic-*`, et son commentaire dit pourquoi — c'est un catalogue de maquettage, alimenté
un jour par le registre des drivers. La façade réelle d'un MikroTik vient donc de son
connecteur, qui la **lit** sur l'équipement (`parseDesignation` pour RouterOS,
`geometryOf` pour SwOS) au lieu de la chercher dans une table.

## 1. Les trois canaux de RouterOS, et pourquoi il en faut trois

C'est le point de départ, et il n'était pas écrit : **aucun** des trois canaux de cette
plateforme ne rend l'ensemble. Ce ne sont pas trois façons de faire la même chose.

| | SNMP (161/udp) | REST (80) | **API binaire** (8728 / 8729) |
|---|---|---|---|
| état de lien | oui | oui | oui |
| **débit NÉGOCIÉ du lien** | oui (`ifHighSpeed`) | **NON** | oui, une requête par port |
| octets / paquets | oui | oui | oui |
| erreurs détaillées | partiel (`ifInErrors`) | oui, une dizaine de compteurs | oui |
| compteur de chutes de lien | non | oui (`link-downs`) | oui |
| CPU / mémoire / temps de marche | oui | oui | oui |
| température de carte | oui | oui | oui |
| **diagnostic optique d'un SFP** | oui (`mtxrOpticalTable`) | **NON** | oui |
| exécuté par | le **proxy** | le contrôleur | le contrôleur |

Deux cases décident du canal, et les deux sont des **absences de la REST** :

- **le débit négocié n'y est pas.** C'est constaté sur les captures : les 87 champs de
  `/interface/ethernet` ne portent aucun `speed`, et `/interface` n'en porte pas
  davantage. Il n'y a qu'`advertise`, qui dit ce que le port *propose* et non ce qu'il a
  *négocié*. Un port tombé à 100 Mbit/s faute d'une paire coupée est donc **invisible par
  la REST** — le mode de panne le plus sournois d'un réseau cuivre, puisque tout
  fonctionne, dix fois moins vite. Un test verrouille cette absence : si un firmware
  ajoutait un champ de débit, c'est la justification du canal d'API binaire qui changerait
  avec ;
- **le diagnostic optique n'y est pas non plus**, et c'était déjà écrit dans le
  connecteur : « `monitor` est un POST, hors de portée d'un outil restreint à GET ».

Les deux manquent pour la **même** raison, et c'est ce qui rend la conclusion simple :
`/interface/ethernet/monitor` est une **commande**, pas un menu. La REST de RouterOS sert
très bien les menus ; les commandes, c'est l'API binaire qui les exécute.

### 1.1 Trois protocoles à ne pas confondre, dont un qu'on n'implémente pas

C'est la confusion la plus fréquente sur cette gamme, et elle coûte cher parce que les
trois répondent à des ports voisins, sur le même équipement, avec le même compte :

| Port | Ce que c'est | Implémenté |
|---|---|---|
| 80 / 443 | la **REST**. HTTP, JSON, Basic à chaque requête | oui, `rest.go` |
| **8728 / 8729** | l'**API binaire**. Protocole à *sentences*, mots préfixés par leur longueur | oui, `api.go` |
| 8291 | **Winbox**. Un protocole *encore différent*, chiffré, non documenté, dont la charge utile est une sérialisation propre à l'outil graphique | **non**, et ce n'est pas prévu |

Dire « l'API que Winbox utilise » désigne en pratique **l'API binaire**, parce que c'est
le seul des deux qui soit spécifié par le constructeur. Le protocole de 8291, lui, n'a
aucune spécification publique ; le réimplémenter serait de la rétro-ingénierie sans fin
pour des données que 8728 sert déjà.

Les trois capabilities existaient déjà et étaient relevées (`/ip/service` : `www` sur 80,
`api` sur 8728, `api-ssl` sur 8729, `winbox` sur 8291, aucun désactivé). La seule chose
qui a changé est le commentaire de `BinaryAPI`, qui disait « que ce connecteur n'utilise
pas ».

### 1.2 Le protocole, en une page — ce qu'il faut savoir pour le relire

Un échange est une suite de **phrases**. Une phrase est une suite de **mots**, chacun
précédé de sa longueur, et terminée par un mot **vide**. Il n'y a aucune autre marque de
fin : l'oublier ne produit pas une erreur, ça produit une session muette qu'aucun délai
d'attente de notre côté ne débloque, puisque l'équipement attend patiemment la suite.

L'encodage des longueurs est la partie où une erreur **ne se voit pas tout de suite** :
un mot mal cadré ne produit pas une erreur de syntaxe, il **décale le flux** —
l'équipement lit la fin d'un mot comme le début du suivant, et la session part en
`!fatal` plusieurs commandes plus tard, sans rapport apparent avec la cause.

| Longueur | Octets | Encodage |
|---|---|---|
| 0 … 0x7F | 1 | `len` |
| 0x80 … 0x3FFF | 2 | `len \| 0x8000` |
| 0x4000 … 0x1FFFFF | 3 | `len \| 0xC00000` |
| 0x200000 … 0xFFFFFFF | 4 | `len \| 0xE0000000` |
| ≥ 0x10000000 | 5 | `0xF0` puis `len` sur quatre octets |

> [!note] Deux protocoles du même constructeur, deux conventions d'octets OPPOSÉES
> La longueur est ici en **gros-boutien** (octet de poids fort d'abord). Les adresses IP
> de SwOS, elles, sont en **petit-boutien** — `0x2400630a` se lit 10.99.0.36, et le
> connecteur voisin consacre un paragraphe entier à ce piège. Inverser l'un ou l'autre
> produit des octets parfaitement valides et une valeur fausse, ce qui est la seule
> catégorie d'erreur qu'aucun message ne signale. D'où, des deux côtés, un test
> d'aller-retour.

Les réponses portent leur type en premier mot : `!re` un enregistrement, `!done` la
clôture, `!trap` un refus **avec** son message, `!fatal` une session fermée par
l'équipement, `!empty` un « il n'y a rien » explicite (RouterOS ≥ 7.18).

Deux décisions de conception valent d'être écrites ici, parce qu'elles vont à l'encontre
de l'usage habituel :

- **le marquage `.tag` sert à DÉTECTER, pas à multiplexer.** Ce connecteur n'émet jamais
  deux commandes en vol ; une réponse d'un autre tour ne peut donc signifier qu'une chose
  — le flux est décalé, et ce qu'on lit n'est pas la réponse à ce qu'on a demandé. Elle
  est donc **refusée**, la session abandonnée, plutôt que triée : trier silencieusement
  rendrait les valeurs d'un enregistrement pour un autre, c'est-à-dire une télémétrie
  crédible et fausse — exactement ce que l'indexation stricte du poller SNMP existe pour
  éviter ;
- **un `!trap` est une erreur, jamais un résultat vide.** Sur une commande de lecture, un
  refus qui se lirait « aucune donnée » ferait conclure « ce menu est vide » là où
  l'équipement a dit « tu n'as pas le droit ». Les deux se réparent par des gestes
  opposés : chercher une fonction absente, ou corriger une politique de groupe.

### 1.3 L'ouverture de session : deux protocoles, et l'ordre qui les rend gratuits

MikroTik a changé l'ouverture en 6.43, et les deux formes coexistent dans le parc :

| Firmware | Forme | Ce que ça vaut |
|---|---|---|
| ≥ 6.43 | `/login =name=… =password=…` en **une** phrase | le mot de passe part **en clair** |
| < 6.43 | deux phrases : `/login` nu rend `=ret=<défi>`, le client répond `=response=00<md5(0x00 ‖ secret ‖ défi)>` | le secret ne circule pas, mais MD5 non salé n'authentifie pas le **serveur** |

Aucune des deux n'est « sûre » : la première expose le secret, la seconde ne prouve rien
sur l'identité de l'équipement. C'est le **tunnel du proxy** qui protège l'échange, comme
il protège déjà le HTTP Basic de la REST — et c'est écrit ici plutôt que sous-entendu.

**La forme moderne est essayée d'abord, et cela ne coûte rien à personne** : un firmware
historique *ignore* les attributs qu'il ne comprend pas et répond son défi. La présence
d'un `=ret=` dans le `!done` est donc à la fois « voici le défi » et « je suis un firmware
d'avant 6.43 » — un seul discriminant, aucune détection de version, aucun aller-retour
dépensé à sonder. Une phrase dans le cas moderne, deux dans le cas historique, jamais
d'essai perdu.

Essayer dans l'autre sens coûterait une phrase à tout le monde et enverrait un `/login`
**nu** — donc sans compte — à des firmwares modernes qui le refusent, ce qui produirait un
refus à expliquer pour un équipement parfaitement sain.

> [!warning] Trois façons de rater le calcul du défi, et elles donnent le MÊME message
> « cannot log in » sur un secret pourtant bon, et rien pour désigner laquelle :
> l'octet nul de tête est dans le **haché** alors que le `00` de tête est dans la
> **chaîne rendue** ; le défi se décode de l'hexadécimal **avant** d'entrer dans le haché
> (le hacher tel quel donne une empreinte valide et fausse) ; et l'ordre est nul, secret,
> défi. Le test recalcule l'empreinte depuis la spécification plutôt que de recopier une
> constante — une constante recopiée du code vérifierait que le code s'accorde avec
> lui-même.

Et un quatrième piège, qui n'est pas dans le calcul : sur cette plateforme, « cannot log
in » a **deux** causes que l'équipement ne distingue pas — un mauvais secret, ou un secret
valide dans un groupe qui ne porte pas la politique `api`. Le message d'erreur du
connecteur nomme la seconde, sinon on cherche un mot de passe pendant une heure.

### 1.4 TLS sur 8729 : le port écoute, et ça ne suffit pas

C'est la limite la plus contre-intuitive de ce canal, et elle est **structurelle** plutôt
que provisoire.

Relevé : `/ip/service` sert `api-ssl` sur 8729, **non désactivé** — contrairement à
`www-ssl`, qui est `disabled` **et** `invalid` faute de certificat. On pourrait en
conclure que 8729 est utilisable tel quel. C'est probablement faux : RouterOS sert
historiquement `api-ssl` **sans certificat** en négociant une suite Diffie-Hellman
**anonyme** (`ADH-…`), et les suites anonymes sont **absentes** de la bibliothèque TLS de
Go — par décision de conception de celle-ci, puisqu'elles n'authentifient aucune des deux
parties.

Conséquence pratique : sur un RouterOS où personne n'a installé de certificat, demander
TLS échouera à la négociation. Le chemin TLS existe donc pour les équipements dont
l'exploitant a **posé un certificat**, et le message d'erreur nomme le repli (l'API en
clair, à travers le tunnel). Là où un certificat existe, il est **auto-signé** et
meshcraft n'a aucune autorité pour le valider : la vérification est désactivée, ce qui
ramène TLS au rang de chiffrement sans authentification — donc au même niveau de
confiance que la REST en clair, celui du tunnel.

## 2. Ce que chaque plateforme rend, métrique par métrique

Le vocabulaire des séries est celui de `internal/metricbuf` (fichier `names.go`), et il
est désormais **écrit en un seul endroit**. Il ne l'était pas : les noms vivaient en dur
dans `internal/snmppoll`, seul producteur d'échantillons du produit — ce qui allait très
bien tant qu'il l'était. Il ne l'est plus, et deux producteurs d'une même série sont
exactement la situation où un nom recopié finit par diverger d'une lettre. Le coût ne
serait pas une erreur de compilation mais **deux séries voisines**, chacune renseignée
pour la moitié du parc : aucun écran ne signalerait rien, il afficherait une courbe
interrompue là où l'équipement change de gamme.

| Série | CSS326 (scraping) | CRS326 (API binaire) | CRS326 (SNMP) |
|---|---|---|---|
| `meshcraft_if_oper_status` | `/link.b` `lnk` | `running` | `ifOperStatus` |
| `meshcraft_if_speed_mbps` | `/link.b` `spd` | `monitor` `rate` | `ifHighSpeed` |
| `meshcraft_if_in_octets_total` | `/stats.b` `rb`+`rbh` (64 bits) | `rx-byte` | `ifHCInOctets` |
| `meshcraft_if_in_packets_total` | `/stats.b` `rtp` | `rx-packet` | — |
| `meshcraft_if_in_errors_total` | `/stats.b` `rte` | somme énumérée (§2.2) | `ifInErrors` |
| `meshcraft_if_in_discards_total` | — | `rx-overflow` | — |
| `meshcraft_if_in_bits_per_second` | **`/stats.b` `rrb`** | — | — |
| `meshcraft_if_link_downs_total` | — | `link-downs` | — |
| `meshcraft_uptime_seconds` | `/sys.b` `upt` (centièmes) | `uptime` (`1d17h38m11s`) | `sysUpTime` (centièmes) |
| `meshcraft_cpu_load_percent` | **jamais** | `cpu-load` | `hrProcessorLoad` |
| `meshcraft_memory_free_bytes` | **jamais** | `free-memory` | `hrStorage` |
| `meshcraft_temperature_celsius` | `/sys.b` `temp` | `/system/health` | `mtxrHealth` |
| `meshcraft_sfp_rx_power_dbm` | `/sfp.b` `rpw` | `monitor` `sfp-rx-power` | `mtxrOpticalRxPower` |

### 2.1 Trois canaux, trois échelles pour la même grandeur physique

C'est le fait le plus utile de cette page, et il explique pourquoi la conversion vit près
de chaque lecture et non dans le vocabulaire de séries. Pour une **puissance optique** :

| Canal | Ce que l'équipement sert | Ce qu'il faut faire |
|---|---|---|
| API binaire | `-3.2` — **déjà en dBm** | rien |
| SNMP | `-3197` — millièmes de dBm, entier **signé** | diviser par 1 000 |
| SwOS | `4790` — dixièmes de **microwatt** | `10·log₁₀(brut / 10 000)` |

Le vocabulaire n'impose donc qu'une **unité** — le dBm, parce qu'un budget optique
s'écrit en dB, qu'une atténuation s'y additionne, et que les seuils du constructeur d'un
transceiver sont donnés ainsi — et laisse chaque canal faire son propre chemin pour y
arriver. Publier des milliwatts obligerait chaque alerte à refaire le logarithme.

> [!important] Zéro n'a pas de valeur en dBm, et c'est le cas qui compte
> `log₁₀(0)` n'existe pas. Une cage sous tension mais sans lumière — fibre débranchée,
> équipement d'en face éteint — ne produit donc **aucun** échantillon, plutôt que moins
> l'infini. Ce n'est pas une coquetterie mathématique : un point à `-Inf` dans
> VictoriaMetrics écrase l'échelle du graphe entier, si bien qu'une **seule** fibre
> débranchée rendrait illisibles les courbes de tous les liens qui fonctionnent.

Et une cage **vide** ne produit rien non plus, avec une sentinelle différente par canal —
RouterOS omet les champs `sfp-*`, SwOS rend **-128 °C**, SNMP n'inscrit pas la ligne.
Publier des zéros ferait apparaître deux transceivers morts sur tout switch dont les
cages sont libres, c'est-à-dire une alerte permanente et fausse sur la majorité du parc.

### 2.2 Erreurs et rejets : la distinction qui décide du geste

RouterOS sépare ce que MIB-II fond, et c'est une des rares choses que ce canal donne
**mieux** que SNMP :

- une **erreur** accuse le câble, le connecteur ou le transceiver : la trame est arrivée
  abîmée ;
- un **rejet** accuse le dimensionnement : la trame était bonne, il n'y avait plus de
  place.

Les additionner ferait chercher un défaut physique devant un lien simplement saturé. Ce
qui entre dans chaque total est **énuméré** dans le code, parce qu'un total dont on ignore
le contenu n'est pas une mesure : `rx-fcs-error`, `rx-fragment`, `rx-jabber`,
`rx-too-long`, `rx-too-short` pour les erreurs de réception ; `rx-overflow` pour les
rejets ; `tx-collision`, `tx-late-collision`, `tx-underrun` pour les erreurs d'émission ;
`tx-drop-packet` **plus** `tx-queue-drop` pour les rejets d'émission — le premier est le
rejet du chip, le second celui de la file logicielle, les deux existent et n'en prendre
qu'un laisserait un mode de perte invisible.

> [!note] `rx-error-events` est délibérément laissé DEHORS
> C'est un compteur d'agrégat du chip Marvell : il recouvre tout ou partie des cinq
> compteurs de réception, et rien dans les relevés ne dit lesquels. L'additionner
> compterait certaines trames **deux fois**, et le symptôme serait une courbe d'erreurs
> plausible et exagérée — c'est-à-dire le pire des résultats, puisqu'elle ferait remplacer
> une jarretière saine.

Le connecteur SwOS applique la même prudence dans l'autre sens : `/stats.b` porte `rte`
(« erreurs de MAC en réception ») **et** `rfcs` (erreurs de séquence de contrôle), et si
`rte` est un total, les additionner double-compterait. Seul `rte` entre donc dans la
série ; `rfcs` reste visible au diagnostic, en attendant qu'une injection de trafic
tranche lequel des deux contient l'autre.

### 2.3 Les échelles de SwOS, et une corroboration qui vaut mieux qu'une lecture de code

Les facteurs d'échelle de SwOS sont relevés dans le JavaScript de son interface
embarquée, qui est la seule référence de la plateforme. Pour le **débit instantané**
(`rrb`, `trb`), le relevé dit « diviser par 100 pour des bits par seconde » — et cette
valeur est **corroborée par les compteurs de la même capture**, ce qui est plus solide
qu'une lecture de code :

```
octets reçus       rb  = 0x018f2a86 = 26 159 750
temps de marche    upt = 0x00e4cf41 = 14 995 265 centièmes = 149 952 s
débit MOYEN                          ≈ 1 396 bit/s
débit instantané   rrb = 0x0000145f = 5 215 → /100 = 52,15 bit/s
```

L'instantané est bien **sous** la moyenne, ce qui est cohérent avec un lien de laboratoire
calme après une campagne d'adoption. L'hypothèse concurrente — un facteur ×100, donc
521 500 bit/s — impliquerait **9,8 Go** transférés sur la durée de marche, contre 26 Mo
réellement comptés : elle est écartée par trois ordres de grandeur. Un test vérifie cette
corroboration au lieu de la citer.

Ce qui n'est **pas** établi pour autant : que ce débit soit moyenné sur la même fenêtre
que celle de l'interface. Aucune injection de trafic n'a été faite. Le chiffre est bon à
une fenêtre près, et il ne faut pas bâtir un seuil d'alerte serré dessus avant de l'avoir
confronté à un débit connu.

Ce que ce champ change en revanche, et c'est ce qui rend un plancher de 15 s acceptable
sur cette plateforme là où il ne le serait pas ailleurs : **meshcraft n'a pas besoin de
dériver deux lectures de compteur** pour tracer une courbe de débit. Un échantillonnage
plus large ne la lisse donc pas.

### 2.4 Les compteurs de SwOS sont sur 64 bits, en deux moitiés

`rb` porte les 32 bits bas, `rbh` les 32 hauts. Lire `rb` seul ferait repasser le compteur
à zéro tous les 4 Gio — **sur un lien 10G, toutes les trois secondes et demie**, ce qui
produirait une courbe de trafic entièrement fausse. Ce connecteur supervise des cages
10G : ce n'est pas une précaution théorique.

La capture de référence a sa moitié haute à zéro et ne peut donc pas prouver la
recomposition ; le test la force sur une page fabriquée, et le dit — croire un test
couvrant quand il ne l'est pas est plus dangereux que de ne pas l'avoir.

### 2.5 L'indexation de `/sfp.b` : le seul vrai piège de cette page

Ses tableaux sont indexés par **cage** et non par port : deux entrées sur un
CSS326-24G-2S+, alors que `/stats.b` en porte vingt-six. La cage de rang *i* est le port
d'index `sfpo + i` — relevé `sfpo: 0x18`, donc la première cage est le port **numéroté
25**. Recoller les deux indexations en supposant qu'elles coïncident attribuerait le
diagnostic optique de SFP1 au port 1, et l'écran afficherait une **puissance optique sur
une prise cuivre**.

## 3. Ce que la collecte fait réellement, et ce que les canaux ne décident pas

> [!warning] Le canal déclaré par le connecteur ne choisit PAS le transport — il borne la cadence
> C'est le défaut que ce lot a trouvé en écrivant, et il faut le comprendre avant de lire
> les planchers de qui que ce soit.
>
> **Le collecteur périodique du produit s'exécute sur le proxy et ne parle QUE SNMP**
> (`internal/snmppoll`, `cmd/proxy/snmp.go`). Or le contrôleur résolvait la cadence d'un
> poll avec `MonitoringCapabilities.Best(metric)`, qui rend le canal le plus rapide **tous
> genres confondus**. Sur le connecteur RouterOS, qui déclare un canal `web` à 5 s et un
> canal `snmp` à 60 s, `Best` rendait donc **5 s** — appliquées à un walk SNMP, c'est-à-dire
> au canal dont la ligne voisine annonçait « jamais plus souvent que 60 s ». Le plancher
> d'un canal était opposé à un **autre** canal, ce qui est très exactement ce qu'un
> plancher n'est pas.
>
> Le plus instructif est **pourquoi** ces 60 s existaient : le commentaire du connecteur
> le disait franchement, elles étaient hautes « pour que `Best` rende toujours le canal
> REST », donc pour **perdre exprès une course d'arbitrage**, un canal jamais éprouvé ne
> devant pas devenir le chemin principal d'un parc. L'intention était juste. Mais la
> course n'existait pas : le canal REST ne gagnait rien, il n'était **jamais** emprunté ;
> il prêtait seulement son plancher.
>
> **Livré** : `MonitoringCapabilities.BestOfKind(kind, metric)` interroge le canal **par
> genre**, et la planification d'un poll SNMP lit désormais le plancher SNMP et lui seul.
> L'intention d'origine est préservée par le mécanisme qui convient — un canal ne peut plus
> gagner une course qui n'existe pas — et le détour a pu être retiré : le plancher SNMP de
> RouterOS passe de 60 s à **10 s**, valeur *déduite par comparaison* avec le 1930 (5 s
> **mesurées** sur un walk deux fois plus long, sur un processeur moins capable) et non
> mesurée ici. `Best` reste ce qu'un écran doit interroger pour répondre « à quelle vitesse
> cet équipement peut-il être observé ».

Le repli est laissé à l'**appelant** et n'est pas fait dans `BestOfKind` : une plateforme
qui ne déclare aucun canal du genre demandé doit être traitée explicitement, et masquer ce
choix derrière un repli silencieux ferait interroger en SNMP une plateforme qui n'en a
jamais annoncé la capacité. Les deux traitements retenus :

| Métrique | Aucun canal `snmp` déclaré | Pourquoi |
|---|---|---|
| `if_state`, `if_counters` | plancher de l'**inconnu** (60 s), et on collecte | elles tiennent entièrement dans MIB-II : tout agent SNMP les sert |
| `health`, `optical_power` | **on ne collecte pas** (cadence 0) | leur socle est **propriétaire** ; les demander à un équipement d'une autre marque ne rendrait que des « no such object » |

### 3.1 Les cadences livrées

| Métrique | Cadence voulue | Plancher SNMP RouterOS | Retenu sur le CRS326 |
|---|---|---|---|
| `if_state` | 30 s | 10 s | **30 s** |
| `if_counters` | 5 s | 10 s | **10 s** — ravalé, et journalisé |
| `health` | **5 min** | 60 s | **5 min** |
| `optical_power` | **60 s** | 30 s | **60 s** |

Les deux nouvelles cadences sont argumentées à l'endroit où elles sont écrites
(`internal/api/watch_handlers.go`) ; en résumé :

- **santé à 5 min**, ce qui est la valeur que la conception portait depuis le début
  ([[Monitoring]] §1, ligne *Santé*) sans que rien ne la lise. Ce lot ne la renégocie pas,
  il la branche. Une température de carte est une **masse thermique** : ce n'est pas la
  cadence de mesure qui décide de la latence de détection, c'est la physique du phénomène.
  Et la charge processeur comme la mémoire libre sont des échantillons **instantanés** :
  les lire toutes les cinq secondes donnerait du bruit, pas une courbe ;
- **optique à 60 s**, et l'arbitrage mérite d'être dit parce que deux phénomènes de
  vitesses opposées vivent dans la même mesure. Un budget optique dérive sur des
  **semaines** ; une coupure se voit en une **seconde**. Ce qui tranche est que le second
  cas est déjà couvert ailleurs — par les transitions de port, qui sont temps réel — et que
  cette série n'a donc pas à le détecter, seulement à l'**expliquer après coup**. La
  [[Roadmap]] annonçait pour cette métrique la cadence des compteurs (5 s), « même nature
  de donnée » ; ce lot s'en écarte, et le motif est le **coût du bus I²C** du module — le
  plus lent de la carte — plutôt que la nature de la courbe.

### 3.2 Ce que les capteurs coûtent : la réponse est « presque rien »

| Métrique | Échantillons | Cadence | Débit |
|---|---|---|---|
| `if_counters` | 52 ports × 4 | 5 s | 41,6 /s |
| `if_state` | 52 ports × 2 | 30 s | 3,5 /s |
| `health` | ~8 séries d'équipement | 300 s | **0,03 /s** |
| `optical_power` | 4 cages × 8 | 60 s | **0,53 /s** |

Les deux nouveaux jeux ajoutent **moins de 1,3 %** au débit d'un équipement de 48 ports.
Le plafond de 133 équipements par proxy (§2.4 de [[Monitoring unifié et pipelines]]) ne
bouge pas, et il n'y avait aucune raison de retoucher les constantes de vidage.

Le coût est ailleurs, et il est sur l'**équipement** : `health` demande **seize walks
distincts**, parce que les capteurs de la MIKROTIK-MIB sont des **scalaires d'une même
branche**. Le poller indexe ses tables par le dernier segment de l'OID, et tous les
scalaires d'une branche finissent par `.0` : un walk de la branche entière les écraserait
les uns sur les autres et n'en garderait qu'un, **au hasard**. Une température qui serait
parfois une tension — le genre de défaut qu'aucun écran ne peut signaler. Seize requêtes
toutes les cinq minutes font un paquet toutes les vingt secondes : deux ordres de grandeur
sous ce que coûte déjà le trafic. Un test vérifie que ce nombre de seize est bien celui que
le code produit, parce que deux fichiers l'annoncent.

### 3.3 Il n'existe aucune MIB normalisée de santé de commutateur

C'est la différence de nature avec `if_state` et `if_counters`, qui tiennent entièrement
dans MIB-II — donc qui marchent partout sans catalogue par constructeur.

`ENTITY-SENSOR-MIB` (RFC 3433) existe, mais MikroTik ne l'implémente pas, ce qui laisse le
choix entre une **branche propriétaire** et **pas de courbe du tout**. Le compromis retenu
est de mettre les OID propriétaires dans le **poller** et non dans les connecteurs : la
collecte s'exécute sur le proxy, qui ne connaît aucun connecteur, et un OID que
l'équipement ne sert pas ne rend simplement rien.

> [!success] Le facteur d'échelle des températures est CONSTATÉ sur matériel — relevé du 29 juillet 2026
> Ce callout portait l'inverse : « aucun de ces OID n'a été lu sur les deux équipements de
> référence », le seul canal disponible étant HTTP en lecture. Le walk de diagnostic du
> proxy, lui, parle UDP — et c'est par lui que le geste prescrit au §4 a été fait.
>
> **Deux canaux indépendants du même équipement, lus au même instant.** Sur le
> CSS326-24G-2S+ de référence (10.99.0.36), `/sys.b` rend `temp:0x45`, soit **69 °C**, et
> `mtxrHealth.11.0` rend **690**. Le diviseur de 10 est donc juste — il n'est plus emprunté
> à Observium et LibreNMS, il est vérifié. La série collectée affiche bien 69 °C.
>
> **Et le SIGNE était un défaut réel, que ce relevé a mis au jour.** La cage SFP1, VIDE,
> rend une température optique de `4294967168` — c'est-à-dire `0xFFFFFF80`, c'est-à-dire
> **-128** en complément à deux. La MIKROTIK-MIB déclare ces capteurs en `Gauge32`, type
> **non signé** par la RFC 2578, et l'équipement y écrit pourtant des valeurs négatives :
> c'est le constructeur qui sort de son type. Sans correction, la série publiait
> **429 496 716 °C**.
>
> Ce n'est pas une valeur aberrante qu'un exploitant écarte d'un coup d'œil : elle **écrase
> l'échelle** de toutes les autres courbes du même graphe, et un seuil d'alerte sur une cage
> vide se déclenche. La même faute frappait les **puissances optiques**, qui sont négatives
> en dBm sur tout lien réel — donc fausses sur *chaque* transceiver du parc, et pas seulement
> sur les cages vides. Le commentaire des colonnes optiques décrivait précisément ce piège ;
> aucune ligne de code ne l'appliquait. Corrigé par `snmppoll.signed32`, avec le signe
> appliqué **avant** l'échelle.
>
> **Ce qui reste déduit**, et le restera jusqu'à ce qu'un châssis qui les porte entre au
> parc : les tensions (`mtxrHealth.8`), les ventilateurs (`.17`, `.18`) et les alimentations
> (`.15`, `.16`). Aucun des deux MikroTik du parc de référence ne les publie — le CSS326 les
> sert à zéro dans `/sys.b` et son agent SNMP ne les expose pas du tout.
>
> **Le geste qui tranche a été fait, et il confirme le diviseur sur les DEUX gammes** —
> relevé du 29 juillet 2026 sur le CRS326. Le walk de la branche entière
> `1.3.6.1.4.1.14988.1.1.3` rend six valeurs, et trois d'entre elles apprennent quelque
> chose :
>
> | OID | Valeur | Ce que ça établit |
> |---|---|---|
> | `mtxrHealth.11.0` | `740` | **74,0 °C** — le diviseur de 10 est juste ici aussi. `/system/health` en REST rendait `72` à un autre instant, `73` dans la table ci-dessous au même instant |
> | `mtxrHealth.100.1.2.17` / `.3.17` | `cpu-temperature` / `73` | une **table de capteurs nommés** existe sur RouterOS, et sa valeur est en **degrés ENTIERS** : le diviseur de `mtxrHealth.11` ne s'y applique PAS. Le poller ne la lit pas — c'est une ressource, pas un défaut |
> | `mtxrHealth.14.0` | `800` | la **fréquence processeur** en MHz, pas une grandeur de santé. À ne jamais ranger parmi les scalaires à diviser : 80 V ou 80 °C seraient parfaitement plausibles à l'écran |
>
> Le poller ne demande ni `.14` ni `.9` (qui rend la chaîne `n/a`, l'équipement n'ayant pas
> de ventilateur), et c'est le bon jeu — vérifié dans `internal/snmppoll/health.go`.
>
> À noter, parce que c'est le genre de détail qui fait conclure « pas publié » : le CRS326
> ne sert **rien** sur `mtxrHealth.6` (`cpu`), l'OID que ce callout prescrivait de walker.
> C'est `.11` (`processor`) qu'il publie. Walker la BRANCHE plutôt qu'un OID deviné est ce
> qui a tranché.
>
> Et un piège dans le piège : la température d'un **transceiver** n'a pas la même échelle
> que celle de la **carte** dans la même MIB. `mtxrOpticalTemperature` est un `Gauge32` nu,
> en degrés entiers. Lui appliquer le diviseur de `mtxrHealth` rendrait 5 °C pour un module
> à 50 °C.

Un dernier point, et c'est le seul de ce fichier où se tromper **inverse une alarme** :
`BoolValue` de la MIKROTIK-MIB énumère `true(1)` / `false(2)`. Sur un `raw != 0`, la valeur
2 — « alimentation en défaut » — serait lue comme « vrai », donc « saine », et la série
afficherait un équipement en bonne santé pendant que son alimentation est morte. La
traduction est donc explicite, et une valeur **hors énumération** vaut « pas sain » : sur
une alarme, l'inconnu se traite comme un défaut, jamais comme un acquittement.

## 4. Le SNMP du CSS326 : le verrou, et le relevé qui l'a levé

Ce paragraphe décrivait un blocage et prescrivait le geste qui le lèverait. **Le geste a été
fait le 29 juillet 2026**, et ce qui suit garde les deux moitiés : le verrou, parce que sa
mécanique reste instructive, et le relevé, parce que c'est lui qui fait foi.

### 4.1 Le verrou, tel qu'il était

Le CSS326 n'était **interrogé en SNMP par personne**. Ni mal, ni partiellement : pas du
tout. Et c'était un enchaînement de décisions cohérentes, pas un oubli :

```
swos.Capabilities().SNMPVersions = nil
        │
        ▼
snmpprov.For  →  Plan{Version: "", Reason: "SNMP non géré : cet équipement
        │                              n'annonce aucune version SNMP."}
        ▼
snmpWatchesFor  →  l'équipement n'entre PAS dans snmp[]
        │
        ▼
le proxy ne reçoit aucune cible : ni état, ni compteurs, ni capteurs
```

> [!warning] Le verrou était CIRCULAIRE, et c'est la leçon de méthode de ce lot
> Le walk de diagnostic — l'instrument même qui devait établir la présence d'un agent —
> **était refusé** pour la raison qu'aucun agent n'était déclaré : `askCommunityFor` passe
> par `snmpprov.For`, qui répondait « SNMP non géré », et l'API rendait *« Un walk se pose
> sur un agent SNMP ; meshcraft ne sait pas si celui-ci en a un. »*
>
> L'outil de mesure était donc indisponible exactement quand il servait. La sortie a été de
> déclarer `v2c` comme une **hypothèse à vérifier**, de poser l'identité SNMP, puis de
> walker — l'ordre étant : si le walk n'avait pas répondu, c'est la déclaration qu'il aurait
> fallu retirer. C'est une piste ouverte pour le produit : un walk en lecture seule devrait
> pouvoir s'exécuter dès qu'une communauté existe au coffre, précisément **parce qu'**on ne
> sait pas encore si l'agent répond.

### 4.2 Le relevé

Séquence exécutée sur l'équipement de référence, à travers le tunnel de `proxy-akwalan` :

1. **lecture** de `/snmp.b` — l'agent est **actif** (`en:0x01`) et porte une communauté ;
2. **constat** que la communauté de l'équipement **diffère** de celle du coffre (15 caractères
   contre 16, comparaison par empreinte, aucun secret imprimé). Un walk avec celle du coffre
   se serait **tu**, et un silence ne distingue pas « pas d'agent » de « mauvaise
   communauté » — c'est le piège que `askFailure` décrit déjà : *« un agent qui refuse une
   communauté se tait, il ne proteste pas »* ;
3. **pose** de l'identité SNMP (`snmp-apply`), vérifiée par relecture de la page ;
4. **walk**, qui répond.

Le walk du proxy parle **v2c en dur** (`gosnmp.Version2c`), ce qui fait de chaque réponse
obtenue une preuve de v2c et non d'autre chose :

| OID | Valeur relevée | Ce que ça établit |
|---|---|---|
| `sysDescr.0` | `CSS326-24G-2S+ SwOS v2.17` | l'agent répond, et c'est bien ce modèle |
| `sysObjectID.0` | `1.3.6.1.4.1.14988.2` | l'arbre MikroTik — et **`.2`**, non `.1` comme RouterOS |
| `sysName.0` | `lan-m6` | **croise** le champ `id` de `/sys.b`, décodé |
| `ifNumber.0` | `26` | **croise** les 26 ports de `/link.b` (`prt:0x1a`) |
| `ifDescr` | `Port1`…`SFP1`, `SFP2` | les noms de `/link.b` — et, depuis, **ceux du connecteur** ([[Connecteurs]] § Le nom des ports) : une série SNMP et une configuration lue en web désignent la même prise par le même nom |
| `ifOperStatus.26` | `1` | la cage SFP2, seule à porter un lien |
| `ifHCInOctets.26` | `189 323 935` | compteurs **64 bits** vivants |
| `ifHighSpeed.26` | `10000` (Mbit/s) | le lien 10 G |
| `sysUpTime.0` | servi | |
| `mtxrHealth.11.0` | `690` | la **seule** grandeur de santé publiée |

Les deux lignes « croise » ne sont pas décoratives : elles confrontent le SNMP à ce que le
connecteur lit en **web** sur le même équipement. Un agent qui répondrait pour un autre
matériel — cas réel quand une adresse a été recyclée — ne rendrait pas ce nom-là et ce
compte de ports-là.

### 4.3 Ce que l'agent NE sert PAS, et qui décide du canal autant que le reste

- **`hrProcessorLoad` et `hrStorage` : aucune valeur.** Pas de CPU, pas de mémoire par SNMP
  sur cette plateforme. Le jeu `health` du poller les demande et n'obtient rien, ce qui est
  sans danger — un OID absent ne rend simplement rien — mais il ne faut pas les promettre.
- **La table optique s'arrête à la colonne 8 (`txBias`).** Ni `txPower` (colonne 9), ni
  `rxPower` (colonne 10) : walk complet de `1.3.6.1.4.1.14988.1.1.19`, dix valeurs, colonnes
  2, 5, 6, 7 et 8. **`optical_power` n'est donc PAS déclaré sur le canal SNMP** de SwOS,
  alors que le canal **web** le sert : les puissances sont dans `/sfp.b` (`tpw`, `rpw`) et
  nulle part ailleurs. C'est le seul endroit du projet où une métrique est servie par le
  scraping et **pas** par l'agent SNMP du même équipement.
- **Le sous-arbre `14988.2` ne contient rien** : l'OID d'identité n'est pas une branche de
  données.

> [!important] `ifSpeed` rend ZÉRO sur un lien à 10 Gbit/s
> Relevé sur la cage active : `ifSpeed.26 = 0` là où `ifHighSpeed.26 = 10000`. Un collecteur
> qui lirait `ifSpeed` afficherait donc un débit **nul** sur les liaisons les plus rapides du
> parc. Le poller lit bien `ifHighSpeed` ; c'est noté parce que c'est le genre de chose
> qu'on « corrige » un jour dans le mauvais sens.

### 4.4 Ce qui est déclaré à l'issue du relevé

```go
Info.Transport            = []string{"web", "snmp"}   // web pour ÉCRIRE, snmp pour LIRE
Capabilities.SNMPVersions = []string{"v2c"}           // relevé ; v3 n'existe pas
MonitorChannel{Kind: "snmp", MinInterval: 10 * time.Second,
               Metrics: []string{"if_state", "if_counters", "health"}}
```

**v1 n'est pas annoncé**, et l'omission est volontaire : l'agent le sert probablement — SwOS
n'a aucun sélecteur de version — mais rien ne l'a montré, et c'est v2c que le collecteur
emploie. Une capability se relève, elle ne se déduit pas d'une vraisemblance.

Le plancher de **10 s** contre 15 s pour les compteurs en web n'est pas une préférence : un
GETBULK sur six colonnes coûte à cet équipement infiniment moins que la sérialisation des
12 694 octets de `/stats.b`. Le canal SNMP ne **remplace** pas le web pour autant —
`/stats.b` porte les débits instantanés calculés par l'équipement, que SNMP n'expose pas, et
`/sfp.b` porte les puissances optiques.

### 4.5 La collecte, vérifiée de bout en bout

Les séries sont dans VictoriaMetrics, sur l'équipement réel :

```
meshcraft_temperature_celsius{device=…, sensor=processor}        = 69
meshcraft_uptime_seconds{device=…}                               = 10110
meshcraft_if_speed_mbps{device=…, ifindex=26, port=SFP2}         = 10000
meshcraft_if_in_octets_total{device=…, ifindex=26, port=SFP2}    = 195 290 040
```

69 °C et non 690, et non 429 496 716 : la chaîne entière — déclaration, provisionnement,
cadence, collecte, échelle, signe — est vérifiée sur du matériel, ce qui n'était le cas
d'aucun capteur avant ce lot.

### 4.6 Le CRS326, lui, était DÉCLARÉ et muet — et c'est le symétrique exact

Le tableau d'ouverture de cette page dit « interrogé en SNMP aujourd'hui : oui » pour le
CRS326. C'était vrai de la **déclaration** et faux du **fait** : aucune série n'est jamais
remontée de cet équipement jusqu'au 29 juillet 2026.

La cause n'est pas dans son connecteur, elle est dans les **bornes de source** de la
communauté que meshcraft y avait écrite — `addresses=10.99.0.0/16`, le sous-réseau du plan
du site — alors que le proxy l'interroge depuis `10.42.0.67`, parce qu'il y est **routé**.
Un agent RouterOS qui refuse une source se tait. Le mécanisme complet, le relevé et le
correctif (bornes cumulées, et collecteur choisi à l'adoption) sont dans [[Monitoring]]
§ *Le DÉFAUT à l'adoption*.

Les deux moitiés de cette page se répondent donc : sur le **CSS326**, le verrou était en
amont — aucune capability, donc aucune cible poussée au proxy, donc rien à interroger. Sur
le **CRS326**, tout était en place et c'est l'équipement qui refusait le paquet. Les deux
se présentaient de la même façon : une courbe vide et rien dans les journaux.

```
meshcraft_temperature_celsius{device=lab-crs326, sensor=processor}  = 74
meshcraft_if_in_octets_total{device=lab-crs326, …}                  = 26 séries
```

## 5. État de la page : ce qui est livré, ce qui ne l'est pas

| Question | État |
|---|---|
| Client de l'API binaire : cadrage, phrases, `!re`/`!done`/`!trap`/`!fatal`/`!empty`, marquage | **livré** — `routeros/api.go`, éprouvé contre un équipement simulé |
| Ouverture de session, **les deux** protocoles (clair ≥ 6.43, défi/réponse < 6.43) | **livré**, avec le calcul du défi recalculé depuis la spécification dans le test |
| TLS sur 8729 | **codé**, et **probablement inutilisable** sans certificat installé (§1.4) |
| Télémétrie RouterOS par l'API binaire ou par la REST, derrière une même abstraction | **livré** — `routeros/telemetry.go`, `TelemetrySource` |
| Télémétrie SwOS par scraping des quatre pages | **livré** — `swos/telemetry.go`, éprouvé sur les captures **réelles** |
| Jeux d'OID `health` et `optical_power` dans le poller | **livré** — `internal/snmppoll/health.go` |
| Cadences des capteurs décidées par le contrôleur, exécutées par le proxy, 0 = ne pas collecter | **livré** — `tunnel.SNMPWatch`, `optionalIntervalFor`, `cmd/proxy/snmp.go` |
| Plancher résolu **par genre de canal** au lieu du plus rapide toutes gammes | **livré** — `MonitoringCapabilities.BestOfKind` (§3) |
| Vocabulaire de séries en un seul endroit | **livré** — `internal/metricbuf/names.go` |
| **Collecte périodique par un canal de connecteur** (API binaire, scraping) | **non construite** — voir le callout ci-dessous |
| **Un seul octet échangé avec du matériel sur l'API binaire** | **non fait** — tout est éprouvé sur simulateur. À ne pas confondre avec le SNMP du CSS326, qui l'est (§4) |
| Facteurs d'échelle des **températures** de `mtxrHealth` confirmés | **fait sur les DEUX gammes** — 69 °C en web contre 690 en SNMP sur le CSS326, 74 °C contre 740 sur le CRS326 (§3.3, §4.2) |
| Collecte SNMP réellement obtenue du **CRS326** | **livré** — elle ne l'était pas : l'agent refusait la source du proxy (§4.6) |
| Signe des températures et des puissances optiques | **corrigé** — `snmppoll.signed32` ; c'était un défaut RÉEL, relevé sur une cage SFP vide (§3.3) |
| Facteurs d'échelle des tensions, ventilateurs, alimentations | **toujours déduits** — aucun équipement du parc ne les publie |
| SNMP sur le CSS326 | **LIVRÉ et vérifié sur matériel** — v2c relevé, canal déclaré, séries collectées (§4) |
| PoE | **sans objet** sur ces deux modèles, et déclaré `Unavailable` avec son relevé |
| LLDP sur SwOS | **impossible** : le firmware n'a pas la fonction, déclaré `Unavailable` avec son relevé |
| Lecture des séries par le produit | **livré** — `tsdb.QueryInstant` / `QueryRange` ; ce paquet n'avait que `Write`, donc onze familles de séries que rien ne relisait |
| Débit par port dérivé des compteurs | **livré** — `internal/api/portrates.go`. Les trois écrans qui lisaient déjà `rx_mbps` / `tx_mbps` affichent du mesuré, et la courbe de site passe en `simulated: false` |
| Écran de télémétrie dédié (température, CPU/mémoire, temps de marche, erreurs de trame) | **livré** — `GET /api/devices/{id}/telemetry` et `web/src/telemetry.tsx`, sous la façade. Six des sept familles ont désormais leur case |
| Budget optique à l'écran | **non fait**, et c'est délibéré : `queryable` exclut les séries optiques, les tensions, les ventilateurs et les alimentations — personne au parc ne les publie, et une case vide se lirait comme une collecte en panne |

> [!warning] La télémétrie par connecteur n'est pas collectée PÉRIODIQUEMENT, et ce n'est pas un oubli
> `routeros/telemetry.go` et `swos/telemetry.go` n'ont ni boucle, ni cadence, ni tampon :
> ils rendent un **instantané**, et une fonction pure le traduit dans le vocabulaire de
> séries. La collecte périodique du produit s'exécute sur le **proxy** et ne parle que SNMP.
>
> Brancher un canal de connecteur dans cette boucle demanderait au **proxy** de détenir les
> identifiants d'administration du parc. C'est une décision d'architecture — le proxy est
> aujourd'hui délibérément dépourvu de secrets d'équipement, il ne porte qu'une communauté
> SNMP en lecture — et pas un branchement. Elle **n'est pas prise ici**.
>
> Ce que ces lectures servent donc réellement, et c'est déjà beaucoup : le **diagnostic à
> la demande** depuis le contrôleur — l'écran qui répond « pourquoi ce lien est-il
> lent ? » (le débit négocié, que la REST ne donne pas) et « ce transceiver est-il en fin
> de vie ? » (le budget optique) — et le socle éprouvé du jour où la collecte par
> connecteur existera. C'est écrit ici plutôt que découvert devant un graphe vide.

Et deux limites de **plateforme**, qu'aucune version ne comblera :

- **un CSS326 n'a aucune série de charge processeur ni de mémoire.** SwOS ne publie ni
  l'une ni l'autre, dans aucune page ni dans sa sauvegarde de configuration complète. La
  seule mesure de « fatigue » disponible est la température de puce. Ces phrases
  accompagnent **chaque** instantané de cet équipement, y compris complet : l'absence de
  courbe se lit exactement comme une collecte en panne, et un exploitant qui ouvre cet
  écran pour comprendre une lenteur doit apprendre en une phrase qu'il n'y a rien à
  attendre ;
- **SwOS ne distingue pas « capteur absent » de « capteur à zéro ».** Les champs de
  ventilateur et de tension de `/sys.b` sont toujours présents, à zéro quand la carte n'a
  pas l'électronique. Les publier ferait quatre séries « ventilateur à 0 tr/min » sur tout
  le parc, donc une alerte permanente et fausse ; les filtrer rend un ventilateur
  **réellement arrêté** indiscernable d'un ventilateur absent. Aucun code ne peut lever
  cette ambiguïté — la page ne porte pas l'information. Ce qui la contient partiellement
  est la **température**, qui monte quand le ventilateur s'arrête : c'est ce signal-là
  qu'il faut surveiller sur cette plateforme.
