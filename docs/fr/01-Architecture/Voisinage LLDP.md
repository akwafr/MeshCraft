---
tags: [architecture, monitoring, topologie, lldp]
---

# Voisinage LLDP

Prolonge [[Monitoring]] et [[Monitoring unifié et pipelines]] sur une question qu'aucune
page ne traitait : **comment meshcraft apprend ce qui est branché en face de chaque
port**, et ce qu'il a le droit d'en conclure.

Jusqu'au 29 juillet 2026 la réponse était « il ne l'apprend pas ». Les connecteurs
*déclaraient* une capability et un canal `lldp` — RouterOS et Instant On — que **rien ne
lisait** ; la table `device_links` ne portait que des liaisons **saisies à la main**, et
la colonne « Voisin » de la page Ports en était dérivée. Ce lot livre la collecte, son
stockage, les liens dérivés et leur affichage.

> [!important] La phrase à retenir avant tout le reste
> **Un voisin LLDP est un CONSTAT daté et réfutable ; une liaison de `device_links` est
> une INTENTION.** Les confondre ferait disparaître un lien saisi parce qu'un switch a été
> débranché dix secondes — ou, dans l'autre sens, ferait survivre un câble que plus
> personne ne voit. C'est pourquoi rien n'est écrit dans `device_links`, et pourquoi les
> deux se dessinent différemment sur la [[Weathermap]].

## 1. Le relevé, avant le code

Trois équipements du parc de référence, walkés en **lecture seule** à travers
`proxy-akwalan` (`POST /api/devices/{id}/snmp-walk`), le **29 juillet 2026**. La
LLDP-MIB est sous `1.0.8802` — une MIB de l'**IEEE** adoptée telle quelle, pas une branche
entreprise.

| | `lab-crs326` (RouterOS 7.21beta3) | `lab-css326` (SwOS 2.17) | `sw-labo-1930` (InstantOn 3.4.0.0) |
|---|---|---|---|
| `1.0.8802` sert quelque chose | **oui** | **NON — zéro ligne, sans erreur** | **oui** |
| `lldpLocChassisId` | `B8:69:F4:5B:A0:D2` — **en texte** | — | six octets bruts → `0x8825101761a7` |
| `lldpLocSysName` | `lan-r4` | — | `CN2AKPH86J` (son numéro de série) |
| `lldpLocPortTable` | **2 entrées** (sur 29 interfaces) | — | **52 entrées** |
| `lldpLocPortIdSubtype` | `interfaceName(5)` partout | — | `5` sauf **ports 50 et 52 : `macAddress(3)`** |
| `lldpRemChassisId` (col. 5) | oui, **en texte** | — | oui, **octets bruts** |
| `lldpRemPortId` (col. 7) | oui | — | oui |
| `lldpRemPortDesc` (col. 8) | **ABSENTE — walk vide** | — | oui |
| `lldpRemSysName` / `SysDesc` (9, 10) | oui | — | oui |
| voisins vus | **5, dont 3 sur un seul port** | — | 3 |
| `lldpStatistics` | non relevé | — | oui (inserts 11, deletes 8, drops 0, ageouts 4) |

### 1.1 SwOS ne sert RIEN, et c'est définitif

Le walk de `1.0.8802` entier sur `lab-css326` rend **zéro ligne, sans erreur** — donc pas
un timeout, pas un refus de communauté : l'agent répond et n'a rien sous cette branche.
Idem pour `1.0.8802.1.1.2.1.3` et `1.0.8802.1.1.2.1.4.1.1` interrogés séparément.

C'est la **confirmation par la mesure** de ce que le connecteur `swos` déclarait déjà par
énumération de son interface web (`MetricUnavailable{Metric: "lldp"}`) : ce firmware ne
connaît que MNDP, le protocole propriétaire MikroTik. La conséquence de conception était
écrite dans le connecteur avant d'être livrée — *« au mieux un voisin le verra, lui ne
verra personne »* — et elle est **la raison pour laquelle un lien vu d'un seul côté est le
cas NORMAL** et non une anomalie.

### 1.2 Le même châssis, servi dans deux encodages

Le fait le plus lourd de conséquences du relevé, et il ne se devine pas :

```
châssis B8:69:F4:5B:A0:D2  (le CRS326)
  vu par lui-même  lldpLocChassisId  =  « B8:69:F4:5B:A0:D2 »   17 octets IMPRIMABLES
  vu par le 1930   lldpRemChassisId  =  b8 69 f4 5b a0 d2        six octets BRUTS
```

La MIB dit `OctetString` : un constructeur a parfaitement le droit d'y mettre du texte
lisible. Comparer les valeurs telles quelles ne rapprocherait donc **jamais** ces deux
constats. D'où `api.normalizeChassisID`, et deux corollaires :

- la conversion doit être **sans perte des deux côtés** — `json.Marshal` remplace
  silencieusement tout octet non-UTF-8 par U+FFFD, et une MAC ainsi transformée serait
  irréconciliable *sans qu'aucune erreur ne se produise*. C'est pourquoi la conversion
  hexadécimale préfixée `0x` de `cmd/proxy/rpc.go` a **migré** dans
  `snmppoll.OctetText` : deux appelants, une implémentation ;
- la normalisation est **conditionnelle**. Un `lldpChassisId` n'est pas forcément une MAC
  (la MIB énumère sept sous-types, dont `local(7)`, une chaîne libre). Réduire aveuglément
  aux chiffres hexadécimaux transformerait `salle-b-baie-12` en `bab12`, et deux
  équipements dont les noms partagent leurs seules lettres a-f seraient déclarés
  identiques. La réduction MAC ne s'applique donc **que si le résultat en a la forme**.

### 1.3 L'index de `lldpRemTable` : le piège central

```
1.0.8802.1.1.2.1.4.1.1.7 . 0 . 26 . 8
└──── colonne (portId) ──┘  │    │    └── lldpRemIndex
                            │    └─────── lldpRemLocalPortNum   ← LE port
                            └──────────── lldpRemTimeMark
```

**Trois arcs, et le premier n'est pas une donnée.** `lldpRemTimeMark` est un TimeFilter
(RFC 2021) : un index synthétique qui permet de ne demander que les lignes modifiées
depuis un instant. Un walk ordinaire le voit donc à **0** — relevé ainsi sur les deux
plateformes, sur **toutes** les lignes.

Deux façons de se tromper, et elles ne se voient pas :

- **lire deux arcs** (ce que fait `suffixIndex`, le décodage du reste de `snmppoll`)
  attribuerait tous les voisins du parc au port local **0**. `decodeRemIndex` refuse donc
  tout ce qui n'a pas exactement trois arcs, et refuse un port local nul — c'est
  précisément la valeur qu'une lecture à deux arcs produirait ;
- **ne garder que le dernier arc** — `lldpRemIndex` — écraserait entre eux les voisins de
  ports différents portant le même index distant. C'est pourquoi la collecte LLDP walke
  avec les **noms d'OID complets** (`snmppoll.TableClient`) et n'emprunte pas `pollClient`,
  dont la table est indexée par le dernier arc.

Un agent a le droit de rendre **plusieurs horodatages** pour la même ligne. Aucun de ceux
du parc ne le fait — **déduit** du relevé, toutes les lignes étant à 0 — mais la MIB
l'autorise, et un agent qui le ferait dupliquerait chaque voisin. L'assemblage garde donc
la ligne d'horodatage le plus **récent**, et la **réinitialise** au passage : mêler deux
horodatages produirait un voisin composite, dont le nom viendrait d'un constat et le port
d'un autre.

### 1.4 `lldpLocPortNum` n'est PAS `ifIndex`, même quand il en a l'air

Relevé :

```
CRS326   lldpLocPortNum 26 = « sfp-sfpplus2 »   ifDescr 26 = « sfp-sfpplus2 »   ✓ coïncide
1930     lldpLocPortNum 50 = « 50 »              ifDescr 50 = « 50 »             ✓ coïncide
```

Les deux coïncident sur les deux plateformes. **On ne s'y fie quand même pas**, et c'est
la même leçon que `snmppoll.physical` a déjà coûtée : la borne d'index marchait sur le
1930 « parce que cette gamme numérote ses ports de 1 à 52 — c'est une coïncidence de
constructeur, pas une propriété du protocole ». La MIB fait de `lldpLocPortNum` un index
propre au sous-système LLDP. La résolution passe donc **toujours** par
`lldpLocPortTable`.

### 1.5 Le nom du port local : aucune colonne ne suffit seule

C'est le relevé qui tranche, et il tranche dans des sens **opposés** :

| | `lldpLocPortIdSubtype` | `lldpLocPortId` | `lldpLocPortDesc` | ce qu'il faut prendre |
|---|---|---|---|---|
| CRS326, port 26 | `interfaceName(5)` | `sfp-sfpplus2` | `bridge/sfp-sfpplus2` | **le portId** |
| 1930, port 50 | `macAddress(3)` | `0x8825101761d9` | `50` | **le portDesc** |

Prendre `portDesc` partout casserait le MikroTik : `bridge/sfp-sfpplus2` n'est pas un port
de sa façade — c'est le nom d'une **appartenance de pont** — et `resolve` l'écarterait, à
raison. Prendre `portId` partout casserait le 1930 sur ses **deux seuls ports qui voient
quelque chose**. D'où la règle : **le sous-type décide, `portDesc` est le repli**
(`snmppoll.localPortName`).

### 1.6 Le chassis-id qu'un équipement ÉMET n'est pas celui qu'il DÉCLARE

Le fait le plus surprenant du relevé, et sans lui le seul lien bilatéral du parc se
présenterait comme unilatéral :

```
le 1930 DÉCLARE     lldpLocChassisId       = 0x8825101761a7
le CRS326 a RELEVÉ  lldpRemChassisId       = 88:25:10:17:61:D9     ← ce n'est pas a7 !
                    …et « …61:d9 » est le lldpLocPortId du port 50 du 1930.
```

Autrement dit, ce firmware annonce dans ses trames la **MAC du port émetteur** en guise
d'identifiant de châssis. Le CRS326 enregistre donc un voisin dont l'identifiant
n'apparaît **nulle part** dans l'identité déclarée du 1930.

C'est ce qui justifie la table `lldp_self` et sa colonne `port_ids` : le contrôleur
indexe, pour chaque équipement, son châssis **et** ceux de ses `lldpLocPortId` qui ont la
forme d'une MAC. La voie de reconnaissance correspondante est nommée à part
(`peer_via: "port_id"`) plutôt que fondue dans « reconnu par son châssis », parce qu'un
exploitant qui lit cette phrase comprend qu'il regarde un firmware qui s'annonce
bizarrement.

### 1.7 Trois voisins sur un seul port — et ce que ça interdit de conclure

Relevé sur `lab-crs326`, port local 26 (`sfp-sfpplus2`) :

| `lldpRemIndex` | châssis | port distant | nom système |
|---|---|---|---|
| 8 | `04:F4:1C:1D:74:5F` | `bridge/sfp-sfpplus1` | `RB5009UG` |
| 9 | `D4:01:C3:AB:32:F6` | `bridge/sfp-sfpplus2` | `CRS305` |
| 11 | `88:25:10:17:61:D9` | `50` | `CN2AKPH86J` (le 1930) |

**Un voisin LLDP n'est donc pas une preuve de câble direct.** L'explication est
**déduite** — elle n'a pas été mesurée sur le chemin — mais elle est la seule cohérente
avec le reste du relevé : un commutateur intermédiaire qui **ne consomme pas** LLDP en
**inonde** les trames au lieu de les absorber, et c'est très exactement le comportement
d'un CSS326 en SwOS (§1.1).

Trois conséquences de conception, toutes livrées :

1. la clé de `lldp_neighbors` porte le **voisin** et pas seulement le port
   (`device_id, local_port, chassis_id, port_id`). Une clé `(device_id, local_port)` aurait
   écrasé deux constats sur trois, et l'écran aurait affiché un voisin **pris au hasard** ;
2. un port multi-voisins **ne confirme aucune réciprocité**. Dire « ce port est en face de
   celui-là » quand il en voit trois serait choisir au hasard : les trois constats restent
   des liens **unilatéraux**, ce qui est exactement ce qu'ils sont ;
3. l'infobulle d'un tel port **dit** le nombre de voisins et ce que ça signifie. C'est le
   seul endroit où un exploitant peut apprendre que son port ne mène pas directement à ce
   qu'il croit.

### 1.8 « Aucun voisin » ne veut pas dire « rien n'est branché »

Trois causes de silence, mesurées ou relevées, qui se réparent **différemment** :

| Cause | Relevé | Ce qu'on en fait |
|---|---|---|
| le **connecteur déclare** que la plateforme ne parle pas LLDP | CSS326 : walk de `1.0.8802` **vide**, et `swos` l'annonce avec sa preuve | `silent: platform_unsupported`, **avec le motif du connecteur rendu tel quel**. Ne se répare pas |
| l'équipement a été **interrogé** et n'a rien rendu | — (aucun cas sur le parc) | `lldp_self.served = false` → `silent: not_served`. Une déclaration de connecteur à corriger, ou un LLDP désactivé sur l'équipement |
| aucun cliché n'est encore arrivé | pas de ligne `lldp_self` | `silent: never_polled`. Il manque un collecteur, ou le premier cycle n'a pas tourné : ça se répare |
| LLDP ne tourne que sur certaines interfaces | CRS326 : **2** entrées dans `lldpLocPortTable` pour **29** interfaces | rien à signaler par port — mais l'écran ne doit pas conclure de l'absence d'infobulle qu'il n'y a pas de câble |

> [!warning] Les deux premières causes ont failli être confondues, et le contresens était grave
> `platform_unsupported` a été ajouté en relisant le chemin réel plutôt qu'en le supposant.
> `optionalIntervalFor` n'allume la collecte que si le connecteur déclare un canal SNMP
> `lldp` : un CSS326 n'est donc **jamais interrogé**, et il n'aurait **jamais** de ligne
> `lldp_self`. Avec deux causes seulement, il se serait présenté comme
> `never_polled` — « il lui manque un collecteur » — c'est-à-dire un exploitant envoyé
> chercher un réglage qui n'existe pas, sur le seul équipement du parc dont on sait avec
> certitude qu'il n'y a rien à régler.
>
> La distinction se lit dans la **déclaration du connecteur** (`WhyUnavailable("lldp")`), et
> elle passe **avant** l'absence de cliché dans `buildLLDPView`. L'ordre est le message.

La dernière est celle qui n'a **pas** de marqueur à l'écran, et c'est une limite assumée :
la LLDP-MIB ne dit pas « LLDP est éteint sur ce port », elle se contente de ne pas lister
le port. On ne peut donc pas distinguer, pour un port donné, « LLDP tourne et personne en
face » de « LLDP ne tourne pas ici ». Le marqueur « LLDP ? » de la page Équipements ne
couvre que les trois premières causes, celles qui portent sur l'équipement entier.

## 2. Par quel canal, et pourquoi pas les trois autres

C'est la décision d'architecture du lot, et elle mérite d'être écrite parce que trois
chemins existants avaient l'air de convenir.

| Chemin | Ce qu'il transporte | Pourquoi il ne convient pas |
|---|---|---|
| lot de métriques (`POST /api/proxy/metrics`) | des **flottants étiquetés** | un voisin est une table de chaînes. L'y forcer demanderait de coder chaque nom système dans une étiquette de série — donc **une série temporelle par câble**, que VictoriaMetrics garderait pour toujours, y compris après le débranchement. Et « depuis quand ce voisin est-il vu » se lirait dans la forme d'une courbe plate |
| canal d'événements du tunnel | des **transitions**, jamais un état courant répété | un cliché périodique violerait son invariant, et sa file de 256 entrées est dimensionnée pour des bascules rares. Fabriquer des `neighbor.new` / `neighbor.gone` rendrait chaque perte d'événement **définitive** — le défaut que `resync` a coûté cher à réparer pour la joignabilité |
| `ask` (`tunnel.OpAsk`) | une **question ponctuelle** | [[Protocole contrôleur-proxy]] le dit noir sur blanc : *« `ask` n'est pas un chemin de collecte »*. Il est à l'initiative du contrôleur et sans cadence |

**Retenu : une route HTTP à part, `POST /api/proxy/lldp`**, pour le motif exact qui a déjà
sorti les métriques du tunnel — c'est du volume périodique, ça s'authentifie par le couple
`(proxy_id, secret)`, et le **contrôle de portée** du contrôleur s'y applique déjà.

> [!note] Et pourquoi il n'y a PAS de tampon
> C'est la différence la plus utile entre ce chemin et celui des métriques. Un point de
> mesure perdu l'est pour toujours : il échantillonne un instant qui ne reviendra pas, d'où
> `metricbuf`, ses 150 000 entrées et ses 57 Mio de pire cas. Un **cliché de voisinage
> perdu est réémis une minute plus tard, plus frais que celui qu'on aurait rejoué** — la
> table LLDP d'un équipement change à l'échelle du câble débranché, pas de la seconde.
>
> Bufferiser aurait donc coûté de la mémoire pour repousser une information **périmée**, et
> il aurait fallu en plus décider quoi faire de deux clichés du même équipement dans la
> file. Le prix est nommé : une coupure de tunnel de dix minutes laisse dix minutes de
> `last_seen` en retard. C'est **visible** (la date est affichée) et auto-réparateur au
> premier cycle qui repasse.

### 2.1 Le contrôle de portée

Cette route **écrit de l'inventaire** : ce qu'elle range en base finit dessiné sur une
weathermap. Le `device_id` du cliché est choisi par le **proxy**, comme le `DeviceID` d'un
événement de tunnel ou l'étiquette `device` d'un échantillon.

L'autorité exigée est celle de la **collecte SNMP** (`authorityPoll`, cf.
`internal/api/proxyscope.go`) et non du ping, pour la raison de `applyPortTelemetry` : ces
constats sortent du polling, et seul le collecteur déclaré d'un équipement l'interroge.

Une différence assumée avec `/api/proxy/metrics`, qui répond **200** en jetant les
échantillons hors portée : ici le refus est un **403 franc**. Là-bas un lot mêle plusieurs
équipements, et refuser le tout ferait perdre les mesures légitimes puis rejouer
indéfiniment le même lot ; ici une requête = **un** équipement, il n'y a rien de légitime à
sauver, et le proxy n'a aucun tampon à débloquer. Le refus franc est donc gratuit, et il a
une valeur propre — il apparaît dans le journal du **proxy**, qui est la machine mal
configurée.

### 2.2 La cadence, et d'où elle sort

**60 s**, et le raisonnement part de la **source** :
`/ip/neighbor/discovery-settings` annonce `discover-interval: 30s` sur RouterOS — donc
l'équipement ne reconstruit sa propre table qu'à ce rythme. C'est le plancher le mieux
fondé du dépôt : ni négocié ni prudentiel, **imposé par le fonctionnement de la source**.
Interroger plus vite relit une table que l'équipement n'a pas touchée.

Pourquoi 60 et non 30, c'est-à-dire pourquoi on ne colle pas au plancher : la valeur
d'exploitation de cette donnée ne se mesure pas en secondes. Un voisin change quand
quelqu'un débranche un câble — un geste physique — et la chute de lien qui l'accompagne
est déjà remontée en **250 ms** par le canal de traps. Doubler le plancher divise par deux
la charge de **sept walks** par équipement, pour une perte de réactivité que rien
n'observe.

Comme `health` et `optical_power`, la collecte n'est **allumée que si le connecteur
déclare un canal SNMP `lldp`** (`optionalIntervalFor`) — et le motif est plus fort ici : la
LLDP-MIB est facultative, et une plateforme entière du parc n'en sert pas une ligne.
Allumer partout aurait fait sept walks muets par minute et par CSS326, indéfiniment, pour
une réponse qu'on connaît déjà.

> [!warning] Le connecteur RouterOS déclarait `lldp` sur le canal `web` UNIQUEMENT
> Donc `BestOfKind("snmp", "lldp")` ne rendait rien, et la collecte serait restée éteinte
> sur cette plateforme alors que son agent sert la MIB richement. Un canal `snmp` pour
> `lldp` lui a été ajouté, **daté et mesuré** (§1). C'est le même défaut que celui corrigé
> pour `if_state` : un canal ne prête pas son plancher à un autre, et il ne prête pas
> davantage sa métrique. Le canal `web` reste déclaré à côté — il existe réellement, via
> `/ip/neighbor` — il n'est simplement emprunté par personne, la collecte périodique
> tournant sur le proxy qui ne parle que SNMP.

### 2.3 Sept walks étroits plutôt qu'un walk de la racine

Un walk de `1.0.8802` rendrait tout en une passe, et rendrait aussi ce dont on n'a que
faire : `lldpStatistics` (dix lignes mesurées sur le 1930), `lldpConfiguration` avec une
ligne par port et par TLV, et les deux colonnes de capabilities de chaque voisin. Sur un
48 ports, `lldpPortConfigTable` seule fait quatre colonnes × 52 ports — **plus de lignes
que tout le reste réuni**, pour des valeurs qu'aucun écran ne montre.

Dix racines étroites coûtent neuf aller-retours de plus par cycle et par équipement,
toutes les soixante secondes. C'est le bon échange : la LLDP-MIB est large et creuse, et
les colonnes utiles y sont dispersées entre deux sous-arbres.

## 3. Le stockage : deux tables, aucune dérivation

Migration `0028_voisinage_lldp.sql`.

```
lldp_self       (device_id pk, site_id, chassis_id, sys_name, port_ids[], served, seen_at)
lldp_neighbors  (device_id, site_id, local_port, local_port_num,
                 chassis_id, port_id, port_desc, sys_name, sys_desc,
                 first_seen, last_seen,
                 pk (device_id, local_port, chassis_id, port_id))
```

### 3.1 Pourquoi pas `device_links`

Trois raisons, dont chacune suffirait :

1. **une intention et un constat ne se réfutent pas pareil** (l'encadré d'ouverture) ;
2. les deux extrémités de `device_links` sont `not null` avec une **clé étrangère** vers
   `devices`. Un voisin hors inventaire — routeur du fournisseur, point d'accès, le S3900
   relevé sur le parc — y est **structurellement inexprimable**, alors que c'est le cas le
   plus fréquent en exploitation ;
3. **un port peut porter plusieurs voisins** (§1.7), ce qu'un lien de topologie n'admet
   pas.

### 3.2 Écart avec ce que le vault avait esquissé

[[Schéma base de données]] esquissait une table `lldp_links` portant `confirmed bool`. Ce
n'est **pas** ce qui est livré :

- le nom retenu est `lldp_neighbors`, parce que la table porte des **constats dirigés**
  (« ce port a vu ceci ») et non des liens. Le lien est ce qu'on en **dérive** ;
- `confirmed` — « les deux côtés se voient » — est une **dérivation de deux constats**, pas
  un fait. Stockée, elle se périme dès que l'un des deux côtés cesse de voir l'autre, et il
  faudrait alors décider **qui**, du prochain cliché de A ou de celui de B, a le droit de
  l'effacer. Elle est calculée à la lecture par une fonction pure
  (`api.deriveLLDPLinks`), et rien ne peut la faire mentir ;
- de même, `peer_device` / `peer_port` **ne sont pas stockés**. Le rapprochement dépend de
  l'inventaire, qui bouge : le jour où l'exploitant adopte l'équipement d'en face, tous les
  constats qui le mentionnaient doivent le reconnaître **sans** qu'on réécrive une table.

### 3.3 Un cliché REMPLACE, et `first_seen` survit

Une transaction par cliché : `upsert` de `lldp_self`, suppression des voisins **absents**
du cliché, `upsert` des présents. Le remplacement complet est ce qui rend le constat
réfutable — c'est la règle de `tunnel.WatchConfig`, et ici un delta laisserait un voisin
débranché affiché indéfiniment.

L'`upsert` **ne touche pas** `first_seen` : c'est la moitié de la valeur d'exploitation de
cette table, puisque c'est ce qui répond à « depuis quand ce câble est-il là ». Un voisin
qui disparaît a été **supprimé** au cliché précédent : s'il revient, il repart à zéro — le
câble a bougé, l'ancienneté du constat d'avant ne dit plus rien.

Les deux horodatages sont ceux du **contrôleur** (`now()` en base), jamais ceux du proxy.
C'est la règle que le lot des traps a coûté cher à établir
([[Protocole contrôleur-proxy]], *« Event.TS est un fait de diagnostic »*) : l'écart entre les
deux horloges atteignait **onze secondes**, et ici la conséquence serait directement
visible — « vu depuis 3 min » affiché au survol d'un port.

## 4. Reconnaître un voisin : trois voies, dans cet ordre

`api.identifyPeer`, pure et testée. **L'ordre est la décision** : le premier qui répond
gagne, et l'on n'essaie pas les suivants.

| Voie | Sur quoi | Force |
|---|---|---|
| `chassis` | le châssis annoncé = le `lldpLocChassisId` d'un équipement du parc | **forte** — une MAC est unique au monde |
| `port_id` | le châssis annoncé = l'un des `lldpLocPortId` d'un équipement du parc | **forte**, et c'est la voie qui sauve le seul lien bilatéral du parc (§1.6) |
| `sys_name` | le nom système annoncé = le `lldpLocSysName` d'un équipement du parc | **faible** — un nom est une chaîne qu'un exploitant a saisie, et deux équipements peuvent la partager |

La voie retenue part jusqu'à l'écran (`peer_via`) : un lien tracé sur la foi d'un nom
mérite d'être lu comme moins sûr, et présenter les deux comme équivalents serait le genre
de silence qui fait perdre une heure.

> [!important] C'est le nom ANNONCÉ qui est comparé, jamais le nom meshcraft
> Le relevé est sans ambiguïté : `lab-crs326` s'annonce **`lan-r4`**, et `sw-labo-1930`
> s'annonce par son numéro de série **`CN2AKPH86J`**. Comparer au nom meshcraft n'aurait
> rapproché **aucun** équipement du parc de référence.

Deux refus, tous deux dans le sens de la prudence :

- un **jeton ambigu est neutralisé**. Deux équipements qui annoncent le même nom système —
  un modèle sorti d'usine, deux switchs jamais renommés — ne doivent pas se voir attribuer
  les voisins l'un de l'autre. Même doctrine que `facade.norm` : rapprocher au hasard est
  pire que ne pas rapprocher ;
- un `lldpLocPortId` n'est indexé que s'il a **la forme d'une MAC**. Un identifiant qui vaut
  `45` ou `ether3` n'identifie rien au-delà de son équipement, et l'indexer ferait
  reconnaître comme le 1930 n'importe quel voisin dont le châssis s'appelle `45`.

### 4.1 Le port distant réutilise `resolve`

`facade.resolve` (`internal/api/proxymetrics_handlers.go`) est la **seule** façon du
produit de rapprocher un libellé d'interface d'un port de façade, et elle couvre les trois
formes relevées sur le parc : égalité exacte (`sfp-sfpplus2`), égalité normalisée (`SFP2`),
préfixe de façade + libellé (`50` → `GE50`). En écrire une seconde version aurait produit
deux façons de nommer le même port, donc un lien dont les deux moitiés ne se reconnaissent
pas.

Deux colonnes sont essayées, `PortID` puis `PortDesc`, parce qu'aucune n'est servie
partout (§1). Un libellé qu'on ne sait pas rattacher rend `""`, et le lien reste **vu d'un
seul côté** : c'est une **sous-affirmation assumée**, inventer un nom dessinerait un câble
sur le mauvais port de la bonne façade.

## 5. Dériver un lien de un ou deux constats

`api.deriveLLDPLinks`, pure.

**La réciprocité exigée est EXACTE** : deux constats se rapprochent quand l'extrémité que
l'un *clame* est l'extrémité que l'autre *occupe*, dans les deux sens. La règle plus large
— « A et B se voient, donc c'est le même lien » — aurait attrapé quelques cas de plus et se
serait trompée sur celui qui compte : **deux équipements reliés par deux câbles**, ce que
tout uplink redondant du monde réel présente. Elle aurait alors confirmé mutuellement les
deux mauvaises moitiés.

Trois issues, et la première est la plus fréquente :

| Issue | Ce que c'est | Tracé sur la weathermap |
|---|---|---|
| **vu d'un seul côté** | un seul constat, extrémité distante reconnue | **oui**, pointillé court atténué |
| **vu des deux côtés** | deux constats réciproques exacts | **oui**, trait continu léger |
| **voisin hors inventaire** | `b_device` vide | **non** — pas de seconde extrémité à joindre. Listé dans le panneau de diagnostic |

Les dates fusionnent quand le lien est bilatéral : `first_seen` est le **plus ancien** des
deux constats (le lien existe depuis que le premier côté l'a vu) et `last_seen` le **plus
récent** (il a été confirmé au dernier cliché).

Rien dans cette fonction ne compare une date à `time.Now()` : la fraîcheur est jugée **à
l'écran**, avec l'horloge de celui qui regarde, parce que c'est la seule qui rende « vu il
y a 3 minutes » vrai pour lui.

## 6. Ce que l'écran montre

- **Survol d'une case de port** (page Équipements) : nom système du voisin, port distant,
  **depuis quand** il est vu, et la voie de reconnaissance. Plusieurs voisins sont tous
  listés, avec la phrase qui dit ce que ça signifie. **Rien n'est affiché s'il n'y a pas de
  voisin** — voir §1.8 ;
- **marqueur « LLDP ? »** sur la carte d'un équipement muet, dont l'infobulle porte
  **laquelle** des deux causes s'applique, parce qu'elles se réparent à l'opposé ;
- **weathermap** : les liens observés sont dessinés **sous** les liens saisis et **décalés
  de 5 px perpendiculairement**. Les deux relient les centres des mêmes nœuds : sans
  décalage, un constat qui confirme une déclaration serait superposé au pixel près et l'un
  des deux disparaîtrait. Voir [[Weathermap]] pour le choix des attributs de trait, et
  pourquoi ni la couleur ni le pointillé long ne pouvaient servir ;
- **panneau « Voisinage LLDP observé »** sous le plan, avec la phrase qui empêche la
  lecture fausse : ces liens sont des constats, ils ne remplacent pas les liaisons saisies,
  et un lien vu d'un seul côté est le cas ordinaire.

Aucun **événement** n'est écrit au [[Journal d'événements]], et c'est la règle qu'il posait
déjà : *« le voisinage LLDP ne produit aucun événement »*. `link.created` / `link.deleted`
restent réservés au saisi. Aucune **notification de site** non plus (`NotifySiteChanged`) :
les écrans concernés ont déjà un rafraîchissement périodique, et réveiller tous les
navigateurs d'un site deux fois par minute et par switch les ferait relire la façade pour
redessiner exactement la même image.

## 7. Décidé / livré / ouvert

| Question | État |
|---|---|
| Relevé LLDP-MIB sur les trois plateformes du parc, colonne par colonne | **livré** — §1, walks en lecture seule du 29/07/2026 |
| Décodage de l'index à trois arcs de `lldpRemTable` | **livré**, pur et testé — `snmppoll.decodeRemIndex` |
| Résolution du port local par `lldpLocPortTable`, jamais par arithmétique | **livré** — `snmppoll.localPortName` |
| Conversion sans perte des `OctetString`, une seule implémentation | **livré** — `snmppoll.OctetText`, `cmd/proxy/rpc.go` délègue |
| Canal de remontée : route HTTP à part, sans tampon | **décidé et livré** — §2, `POST /api/proxy/lldp` |
| Contrôle de portée (autorité de collecte), 403 franc | **livré** — `handleProxyLLDP` |
| Cadence 60 s, allumée par déclaration du connecteur | **livré** — `wantedLLDPInterval`, `optionalIntervalFor` |
| Canal `snmp` / `lldp` sur le connecteur RouterOS, **mesuré et daté** | **livré** |
| Deux tables distinctes de `device_links`, cliché qui remplace, `first_seen` préservé | **livré** — migration 0028 |
| Reconnaissance d'un voisin : trois voies, jeton ambigu neutralisé | **livré**, pur et testé |
| Liens dérivés, réciprocité exacte, lien unilatéral tracé | **livré**, pur et testé |
| Survol d'un port, marqueur de silence, weathermap, panneau de diagnostic | **livré** |
| **Adoption d'un voisin fantôme d'un clic** | **NON FAIT.** [[Weathermap]] la prévoit (nœud grisé → clic → adoption). Le lien est listé avec son châssis et son `sysDescr`, ce qui donne de quoi le créer à la main, mais il n'y a aucun geste d'adoption |
| **Colonne « Voisin » de la page Ports alimentée par LLDP** | **FAIT**, et l'arbitrage laissé ouvert a été tranché dans l'autre sens le 30 juillet 2026 : c'est là que l'exploitant cherche « qu'est-ce qu'il y a en face ? », et un tiret sur un switch qui parle 802.1AB envoyait chercher une collecte en panne. Le **constat gagne**, la **saisie est le repli** — elle couvre ce que LLDP ne voit pas (SwOS, voisin muet, LLDP éteint sur un port) — et la cellule **dit toujours laquelle des deux elle montre** (`· LLDP` ou `· saisi`). Plusieurs voisins sur un port : le premier, un `+n`, et l'infobulle complète. Ce qui reste non fait est la CONFRONTATION des deux, ligne suivante |
| **Chevron « uplink LLDP » sur une case de port** | **NON FAIT.** [[Rendu des ports]] le décide ; ce lot n'ajoute qu'une infobulle. Un marqueur visuel demande de trancher sa priorité face au VLAN, à la dérive et au PoE, qui occupent déjà la case |
| **Comparaison saisi ↔ observé** (« ce lien déclaré n'est vu par personne ») | **NON FAIT.** C'est la suite naturelle, et elle est délibérément séparée : elle demande de décider ce qu'un désaccord VEUT DIRE, alors que ce lot n'établit que les deux moitiés à comparer |
| **Voisinage LLDP par un autre canal que SNMP** | **non prévu.** `/ip/neighbor` de RouterOS et la page web du 1930 sauraient le rendre, mais la collecte périodique tourne sur le proxy, qui ne parle que SNMP. Un second chemin pour la même donnée demanderait de les arbitrer |
| **`lldpStatistics`** (`ageouts`, `drops`) | **non collecté.** Relevé sur le 1930 (§1) et ce serait une vraie métrique de santé de voisinage — des flottants, donc qui entreraient dans le pipeline existant. Rien ne le lit |
| **LLDP en SNMPv3** | **hors sujet par construction** : la collecte est v2c comme le reste ([[Identité de service et rotation]]) |
| **Distinguer « LLDP éteint sur ce port » de « personne en face »** | **impossible en l'état** — §1.8. La MIB ne liste simplement pas le port, elle ne dit pas pourquoi |
