---
Statut: Accepté
Date: 2026-10-25
---

# ADR-0028 — La décomposition du trafic par classe est une capacité déclarée, et une classe non déclarée se dit à l'écran

## Contexte

La demande est un graphique de **paquets par seconde** sur l'overlay de port,
« affichant sur plusieurs lignes se sommant multicast, broadcast et unknown
unicast ». C'est le pendant d'**observation** du limiteur de tempête : les trois
classes demandées sont exactement celles que `StormClass` police
(`StormBroadcast`, `StormMulticast`, `StormUnknownUnicast`), et l'en-tête de
`wire/mdpv1/stormcontrol.go` renvoie déjà explicitement les compteurs observés
« à un chemin de supervision ». Ce chemin n'existait pas. ADR-0020 l'a laissé
ouvert en refusant de modéliser la colonne `Drops` du `show storm-control`
Arista pour la même raison.

Avant d'empiler trois séries, il a fallu établir lesquelles existent.

### Ce que le vocabulaire de séries porte aujourd'hui

`wire/metrics/names.go` ne connaît **aucune** décomposition par classe. Par
port, il porte `meshcraft_if_in_packets_total` / `out`, les octets, les erreurs
et les rejets — et rien d'autre. Vérifié côté stockage le 2026-10-25 :
VictoriaMetrics contient 19 noms de séries, aucun ne nomme une classe.

### Ce que chaque chemin de collecte rend réellement

Deux chemins produisent des compteurs par port, et ils ne rendent pas la même
chose.

**gNMI (Arista EOS) — relevé en lecture seule sur `core01-arena`
(192.0.2.1), 2026-10-25.** `internal/gnmipoll` s'abonne au **conteneur**
`/interfaces/interface[name=*]/state/counters`, donc tout ce que l'équipement
sert y arrive déjà. Le relevé brut donne quinze feuilles, et les voici en
entier :

| feuille servie | aujourd'hui | classe |
|---|---|---|
| `in-octets`, `out-octets` | mappée | — |
| `in-pkts`, `out-pkts` | mappée | total |
| `in-errors`, `out-errors`, `in-discards`, `out-discards` | mappées | — |
| `in-fcs-errors` | **ignorée** | — |
| `in-multicast-pkts`, `out-multicast-pkts` | **ignorée** | **multicast** |
| `in-broadcast-pkts`, `out-broadcast-pkts` | **ignorée** | **broadcast** |
| `in-unicast-pkts`, `out-unicast-pkts` | **ignorée** | unicast **total** |

Multicast et broadcast sont donc **déjà sur le fil**, payés, et jetés par le
défaut de `counterLeaves` dans `leafSample`. Les mapper ne coûte pas un octet de
plus à l'équipement.

**L'unknown unicast n'y est pas.** Pas sous un autre nom, pas ailleurs : ni
`in-unknown-protos`, ni une feuille approchante. Les quinze feuilles ci-dessus
sont la liste complète. Et `in-unicast-pkts` ne le donne pas par soustraction :
c'est l'unicast **total**, connu et inondé confondus, et la part inondée n'en
est pas déductible.

**SNMP (douze Aruba Instant On 1930, FS S3400).** `internal/snmppoll` lit huit
OID, dont `ifHCInOctets` (`1.3.6.1.2.1.31.1.1.1.6`) et `ifHighSpeed` (`.15`) —
donc **de l'ifXTable, et ça fonctionne sur ces équipements** (leurs séries
d'octets sont peuplées avec `ifindex`). Les quatre compteurs par classe de la
même table ne sont pas interrogés :

| OID | objet | lu ? |
|---|---|---|
| `1.3.6.1.2.1.31.1.1.1.2` | `ifInMulticastPkts` | non |
| `1.3.6.1.2.1.31.1.1.1.3` | `ifInBroadcastPkts` | non |
| `1.3.6.1.2.1.31.1.1.1.4` | `ifOutMulticastPkts` | non |
| `1.3.6.1.2.1.31.1.1.1.5` | `ifOutBroadcastPkts` | non |

L'unknown unicast, lui, **n'est pas dans la MIB standard**. `ifInUnknownProtos`
(`1.3.6.1.2.1.2.2.1.15`) est un autre objet — des trames jetées pour protocole
non supporté, pas de l'unicast inondé — et le confondre avec la classe que le
storm control police serait précisément inventer la donnée.

**Le seul compteur d'unknown unicast relevé sur le parc** est la colonne `Drops`
du `show storm-control` Arista, citée verbatim dans ADR-0020. Ce n'est pas la
même grandeur : ce sont les trames **jetées par le limiteur**, pas les trames
acheminées. L'empiler avec deux débits acheminés produirait un total qui n'est
le total de rien.

## Décision

**1. Deux classes réelles, pas trois dont une inventée.** Le vocabulaire gagne
quatre séries — `meshcraft_if_in_multicast_packets_total`,
`…_in_broadcast_packets_total` et leurs jumelles `out`. L'unknown unicast
**n'obtient pas de série** tant qu'aucune plate-forme ne sait le produire : un
nom de série sans producteur est une promesse que le graphe tiendrait en
peignant zéro.

**2. La décomposition est une capacité déclarée, PAR CLASSE.** Trois drapeaux
dans `Capabilities.Features`, suivant le motif de `FeatMSTRegion` :
`FeatTrafficClassMulticast`, `FeatTrafficClassBroadcast`,
`FeatTrafficClassUnknownUnicast`. Un drapeau par classe et non un « sait
décomposer » global, parce qu'une plate-forme peut très bien rendre multicast et
broadcast depuis l'ifXTable et pas l'unknown unicast — c'est le cas de **tout**
le parc. Une capacité unique serait donc fausse partout, et fausse dans le sens
dangereux : elle promettrait plus que la plate-forme ne tient.

C'est la leçon d'ADR-0022, à l'identique : rien ne distinguait « sait écrire du
STP » de « sait rejoindre une région », et un `apply` propre a laissé
`sw01-arena` hors de l'arbre de son site. Ici, rien ne distinguerait « la
collecte est jeune » de « cette classe n'existera jamais ici ».

**3. Une classe non déclarée se dit à l'écran, elle ne manque pas.** C'est le
cœur de la décision. Le graphe doit distinguer trois états que rien ne
distinguait :

| situation | ce que l'écran dit |
|---|---|
| classe déclarée, échantillons présents | la bande, empilée |
| classe déclarée, série vide | « pas encore relevé » — transitoire |
| classe **non déclarée** | « cette plate-forme ne la remonte pas » — définitif |

Omettre silencieusement la bande d'une classe non déclarée rendrait un sommet de
pile qui se lit comme un total alors qu'il n'en est pas un, et personne ne
pourrait le savoir. C'est la panne qu'ADR-0020 a corrigée sur le storm control,
où une ligne non lue valait « port non protégé ».

**4. Où la capacité est prouvée, et où elle est déduite.** ADR-0022 a été
déclarée sur le seul Arista et a failli cacher un bouton sur quatre Instant On ;
la consigne qui en sort est de dire sur quoi on s'appuie.

| connecteur | multicast / broadcast | sur quelle base |
|---|---|---|
| `arista/eos` | déclarés | **prouvé** — les quatre feuilles relevées sur `core01-arena` le 2026-10-25 |
| plates-formes relevées en SNMP | déclarés | **déduit** — l'ifXTable répond déjà sur ces équipements (`.6`, `.15` peuplent leurs séries d'octets) ; les quatre compteurs par classe sont du même tableau. **Non vérifié sur l'équipement** : SNMP est de l'UDP et la route de laboratoire est un SOCKS5 TCP, donc aucun `walk` n'a pu être tenté depuis cet hôte. |
| toutes | unknown unicast | **non déclaré nulle part** — aucune source connue |

## Conséquences

- Quatre séries au vocabulaire, mappées dans `internal/gnmipoll/translate.go`
  (gratuit, l'abonnement existe) et quatre OID ajoutés à `internal/snmppoll`.
  Ce dernier coût est réel et se paie à chaque cycle sur chaque équipement :
  l'en-tête du poller le dit, et c'est pourquoi on n'ajoute que quatre OID et
  pas l'ifXTable entière.
- Le graphe affiche l'**entrant**, et les deux sens sont collectés. Ce que la
  pile répond, c'est « quelle part de ce que ce port REÇOIT est de l'inondation »
  — et c'est l'ingress que le limiteur de tempête police. Les séries sortantes
  existent et répondent à l'autre question, « ce commutateur est-il la SOURCE »,
  mais mettre les deux sens sur une seule pile doublerait chaque bande et rendrait
  illisible la proportion, qui est précisément ce qu'on vient y lire.
- Le graphe est **empilé** et se lit en paquets par seconde. Les compteurs sont
  dérivés **côté serveur** par `rate()`, comme toute la famille `_total` de ce
  produit : VictoriaMetrics absorbe la remise à zéro d'un redémarrage, et
  `round3` écrase déjà le négatif résiduel qu'un compteur mal relu produirait.
  C'est ce qui rend le cas `core01-arena` (trois redémarrages dans son
  historique) et `sw01-arena` (horloge à `2000-1-1`) sans effet sur la pile.
  Aucune dérivation ne descend dans le navigateur.
- L'axe de temps passe par `viewerDate` / `ageMs` (ADR-0008). Ces échantillons
  sont horodatés **par le relais**, pas par le contrôleur, donc l'écart n'est
  pas borné et un `new Date(ts)` brut afficherait une heure qui n'est celle de
  personne.
- `Drops` par classe reste hors du contrat, comme ADR-0020 l'a décidé. Si une
  seconde plate-forme sait compter l'unknown unicast **acheminé**, la classe
  obtient sa série et son drapeau, et le graphe passe de deux bandes à trois
  sans rien changer à l'écran — c'est tout l'intérêt d'avoir déclaré par classe.

## Alternatives écartées

**Déduire l'unknown unicast de `in-unicast-pkts` moins les unicast connus.** Il
n'y a pas de compteur d'unicast connus. La soustraction n'a pas de second terme,
et l'inventer à partir de la table FDB donnerait un nombre qui varie avec
l'âge des entrées plutôt qu'avec le trafic.

**Empiler la colonne `Drops` du storm control comme troisième bande.** Deux
grandeurs incompatibles dans une pile dont le sommet se lit comme un total :
des trames acheminées et des trames jetées. Le sommet ne serait le total de
rien. ADR-0020 a déjà rangé ces compteurs ailleurs.

**Un drapeau unique « sait décomposer le trafic ».** Faux sur la totalité du
parc actuel, puisque aucune plate-forme ne tient les trois classes. Il
promettrait l'unknown unicast partout où il promet le broadcast.

**Ne rien déclarer et déduire la capacité de l'absence d'échantillons.** C'est
exactement ce que cette décision refuse, et la raison tient en une phrase : un
trou se lit comme un creux de trafic. Une collecte qui vient de démarrer et une
plate-forme qui ne saura jamais produisent le même vide.

**Omettre la bande de l'unknown unicast.** Le graphe afficherait une pile de
deux bandes indiscernable d'une pile complète, sur une demande qui en nommait
trois. Le lecteur conclurait qu'il n'y a pas d'unknown unicast sur ce port,
alors que la vérité est qu'on n'en sait rien.
