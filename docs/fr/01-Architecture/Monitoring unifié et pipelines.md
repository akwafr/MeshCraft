---
tags: [architecture, monitoring, orchestration]
---

# Monitoring unifié et pipelines

[[Monitoring]] décrit *comment* meshcraft collecte : SNMP, traps, syslog. Cette
note décide *à quelle cadence*, *par quel chemin*, *qui en est capable* — et
surtout **où regarder quand ça ne marche pas**.

Le point de départ est une contrainte, pas une envie : un parc multi-vendor n'a
pas un seul niveau d'observabilité. Un switch d'entrée de gamme web-only donne
une page HTML toutes les trente secondes ; un châssis moderne pousse du gNMI en
continu. Le produit doit rendre les deux lisibles **sur le même écran**, sans
prétendre que le premier vaut le second.

## 1. Trois rythmes, décidés par la capability

L'accès disponible détermine le rythme. Ce n'est pas un réglage : personne ne
devrait avoir à savoir, équipement par équipement, ce que son parc supporte —
c'est la même logique que la résolution SNMP v2c/v3 (§2.1 de
[[Identité de service et rotation]]).

| Canal | Cadence **plancher** | Ce qu'on y met |
|---|---|---|
| **API / page web** | 30 s | l'état, rien de plus : la page d'un switch web-only coûte cher et ne se scrute pas |
| **SNMP** | 5 s pour l'état d'interface **et les compteurs**, 30 s pour le reste | `ifOperStatus`, les bits d'état et les compteurs de trafic peuvent descendre à 5 s ; santé, PoE et tables restent à 30 s ou plus (§1.1) |
| **Streaming telemetry** | 5 s | tout ce que l'équipement veut bien pousser |

> [!important] Ce sont des PLANCHERS, pas des objectifs
> « Maximum toutes les 5 secondes » veut dire *jamais plus souvent*. Un parc de
> deux cents équipements interrogés à 5 s, c'est quarante requêtes par seconde
> en permanence — dont l'essentiel ne change jamais. Le rythme réel est le plus
> lent qui satisfait le besoin ; le plancher n'est là que pour empêcher qu'on
> aille plus vite.

Deux raisons de ne pas descendre en dessous, et elles sont matérielles : le CPU
de supervision d'un switch d'accès est modeste, et un walk SNMP trop fréquent
dégrade le plan de contrôle avant de dégrader la métrique. Un équipement qu'on
supervise trop est un équipement qu'on abîme.

> [!note] Ce que le code faisait, et ne fait plus
> Le contrôleur envoyait dans `snmp[]` le `MinInterval` du canal comme cadence —
> donc 5 s sur un Instant On 1930, le plancher lui-même. « Jamais plus souvent que
> 5 s » était devenu « toutes les 5 s », et un parc entier se retrouvait interrogé
> au rythme maximal que la plateforme tolère, exactement ce que le callout ci-dessus
> interdit noir sur blanc. C'est corrigé : `MinInterval` **redevient un plancher**,
> et une politique d'exploitation choisit la cadence (§1.2).

### 1.1 La capability décide, le driver la déclare

Le connecteur ne dit pas seulement *quoi* il sait faire, mais *à quelle cadence
c'est raisonnable* et *par quel canal* :

```go
// internal/driver/instanton — la déclaration réelle de la gamme 1930
Monitoring: driver.MonitoringCapabilities{
    Channels: []driver.MonitorChannel{
        {Kind: "snmp", Metrics: []string{"if_state"},
         MinInterval: 5 * time.Second},
        {Kind: "snmp", Metrics: []string{"if_counters"},
         MinInterval: 5 * time.Second},
        {Kind: "snmp", Metrics: []string{"health", "lldp", "poe"},
         MinInterval: 30 * time.Second},
        {Kind: "web",  Metrics: []string{"if_state"},
         MinInterval: 30 * time.Second},
    },
    Traps:     true,
    Syslog:    true,
    Streaming: false,
}
```

Ce que la plateforme ne sait pas faire n'est pas contourné : l'équipement
apparaît avec le canal dont il dispose, et l'UI dit lequel. Un parc à moitié
supervisé sans que personne ne le voie serait le pire résultat — c'est la règle
5 du [[Modèle de données standard]] appliquée à la collecte.

**Un canal par coût, pas un canal par connecteur.** La déclaration ci-dessus
comptait deux canaux SNMP au lieu de trois : `if_state` et `if_counters`
partageaient une ligne, et `health` / `lldp` / `poe` étaient regroupés avec eux à
30 s. Les 30 s ne venaient d'aucune mesure — elles venaient du **regroupement**,
où quatre métriques déclarées ensemble héritent toutes du plancher de la plus
coûteuse. Rien dans la documentation de la gamme n'impose 30 s à `ifHCInOctets` ;
la réserve « fragile sous rafale » du 1930 porte sur son canal **web** et ses cinq
sessions ([[Connecteurs]]), pas sur SNMP, qui est en lecture et n'ouvre aucune
session d'administration.

Le canal a donc été **scindé** : `if_counters` seul, avec son propre plancher de
5 s éprouvé sur un 1930 48 ports — un walk de six colonnes sur 52 interfaces
toutes les cinq secondes ne déplace pas son CPU de supervision. Ce n'est pas le
garde-fou qu'on contourne, c'est **la déclaration qui devient exacte** : elle dit
enfin ce que chaque métrique coûte, au lieu de faire payer à toutes le prix de la
plus chère.

> [!important] La scission est ce qui protège `health`, `lldp` et `poe`
> Ces trois métriques **gardent 30 s**, et c'est toute la raison du découpage.
> Aucune collecte ne les lit encore. Le jour où elle le fera, les avoir laissées
> dans le canal des compteurs les ferait scruter à **5 s** — l'inverse exact de la
> conception, qui place la santé et le PoE à 5 min et les tables réseau à 5-15 min
> ([[Monitoring]] §1). Un plancher de 30 s leur laisse déjà six fois trop de
> marge ; descendre le canal entier pour accélérer le trafic aurait embarqué avec
> lui trois métriques qui n'ont rien demandé.

### 1.2 La politique choisit, le plancher borne — et un ravalement se dit

Deux notions, longtemps confondues dans le code, et les séparer est tout le lot :

| | Qui la porte | Ce qu'elle dit |
|---|---|---|
| **Cadence voulue** | la politique d'exploitation (`internal/api/watch_handlers.go`) | à quel rythme l'exploitant a besoin de voir |
| **Plancher** | le connecteur (`MinInterval`, §1.1) | « jamais plus souvent que » — ce que la plateforme encaisse |

La politique **choisit**, le plancher **borne par le bas**. Ce qui est livré :

| Métrique | Cadence voulue | Plancher du 1930 | Retenu sur un 1930 |
|---|---|---|---|
| `if_state` — état et vitesse des ports | **30 s** | 5 s | 30 s |
| `if_counters` — `ifHCInOctets`/`Out`, `ifInErrors`/`Out` | **5 s** | 5 s | 5 s |
| `health` — `sysUpTime`, CPU, mémoire, températures, tensions | **5 min** | 30 s | *rien : ce connecteur ne déclare pas de canal `snmp` pour elle* |
| `optical_power` — diagnostic des transceivers | **60 s** | — | *rien, même raison* |
| `lldp` | **60 s** (`wantedLLDPInterval`) | 30 s — imposé par l'équipement, `discover-interval` | ne remonte PAS par le pipeline de métriques : c'est une table, cf. [[Voisinage LLDP]] §2 |

> [!important] `health` et `optical_power` sont collectées depuis [[Télémétrie MikroTik]], et PAS partout
> Les deux jeux d'OID existent désormais dans le poller (`internal/snmppoll/health.go`), et
> le tableau ci-dessus dit une chose qu'il faut lire attentivement : le connecteur du 1930
> regroupe `health` avec `lldp` et `poe` dans un canal `snmp` à 30 s, mais la cadence
> retenue reste *rien* — parce que la planification interroge désormais le canal **par
> genre** et que ces trois métriques ne sont allumées que si un canal `snmp` les porte
> **nommément**. Sur le 1930 elles y sont ; ce qui manque est que le poller ne sait lire
> ni `lldp` ni `poe`, et qu'`optical_power` n'y est pas déclarée du tout — la gamme ne
> l'expose pas, et c'est écrit dans [[Connecteurs]].
>
> Ce renversement est délibéré : il n'existe **aucune MIB normalisée** pour la santé d'un
> commutateur, donc le socle est propriétaire. Demander seize branches MikroTik à un
> équipement d'une autre marque ne rendrait que des « no such object » — du trafic pour
> rien, à chaque cycle et sur tout le parc. Une cadence de **zéro** vaut donc « ne pas
> collecter », et c'est le régime normal, pas un cas dégradé.

> [!important] Un ravalement au plancher n'est jamais silencieux
> Quand la cadence voulue passe sous le plancher, c'est le plancher qui gagne — et
> la substitution est **journalisée** en avertissement : l'équipement, son modèle,
> la métrique, la cadence voulue, celle retenue, et **l'origine du plancher**
> (le canal du connecteur, ou le fait qu'on ne connaisse pas le connecteur). Un
> opérateur qui a demandé 5 s et qui obtient 30 s doit pouvoir l'apprendre
> autrement qu'en comparant des graphes. Le silence, ici, transformerait une
> décision d'exploitation en mystère.
>
> La ligne est émise à chaque **recalcul** de la surveillance — connexion d'un
> proxy, changement du parc — et non à chaque poll : c'est ce qui la rend
> supportable, et c'est aussi ce qui la fait réapparaître après un redémarrage
> plutôt que se perdre dans un historique que personne ne remonte.

**L'inversion des deux cadences est délibérée.** Lue seule, la ligne « `if_state`
à 30 s » ressemble à une régression — c'était 5 s avant. C'est l'inverse :

- **l'état des ports RALENTIT** parce qu'il n'a plus à être *scruté*. La
  réactivité vient désormais du canal de traps : un `linkDown` déclenche le poll
  **sur le proxy** en 250 ms (§2.3). Le polling périodique n'est plus la source de
  la réactivité, il est le **filet de sécurité** de ceux qui ne poussent rien — et
  un filet se relève toutes les 30 s, pas toutes les 5 s ;
- **le trafic ACCÉLÈRE** parce qu'un compteur sert à tracer une **courbe**. À un
  point par minute, une saturation de deux minutes ne se voit pas, elle se devine
  sur deux points. Le grain fin est ce qui rend le graphe exploitable, et c'est la
  seule raison de le payer.

Autrement dit : on a arrêté de payer 5 s pour une information booléenne que les
traps donnent en une seconde, et on a mis cet argent là où il achète quelque
chose. Le prix, lui, est réel et il est chiffré en §2.4 — *une cadence qui ne
remonte pas n'est pas une cadence*.

> [!warning] Le plancher d'un canal ne doit borner QUE ce canal — le confondre était un défaut
> Le collecteur périodique s'exécute sur le proxy et ne parle **que SNMP** (§2). Or la
> résolution passait par `MonitoringCapabilities.Best(metric)`, qui rend le canal le plus
> rapide **tous genres confondus** : sur un connecteur déclarant un canal `web` à 5 s et un
> canal `snmp` à 60 s, le contrôleur poussait 5 s… à un walk SNMP. Le plancher d'un canal
> était opposé à un **autre** canal, ce qui est exactement ce qu'un plancher n'est pas.
>
> Ce n'était pas visible sur le 1930, dont les deux canaux `snmp` sont les plus rapides ;
> ça l'était sur RouterOS, et le plus instructif est **pourquoi** son plancher SNMP valait
> 60 s : pour *perdre exprès* une course d'arbitrage, un canal jamais éprouvé ne devant pas
> devenir le chemin principal d'un parc. L'intention était juste, mais la course n'existait
> pas — le canal REST n'était **jamais** emprunté, il prêtait seulement son plancher.
>
> **Livré** : `BestOfKind(kind, metric)`, et le repli reste à l'appelant plutôt que caché
> dans la méthode — une plateforme qui ne déclare aucun canal du genre demandé doit être
> traitée explicitement. Détail complet en §3 de [[Télémétrie MikroTik]].

**Connecteur inconnu : plancher de 60 s.** Connecteur non enregistré, ou
connecteur qui ne déclare aucun canal `snmp` pour cette métrique : dans les deux cas on
ignore ce que la plateforme encaisse, et le ravalement est annoncé comme les
autres. Ce repli vaut pour `if_state` et `if_counters`, qui tiennent dans MIB-II ; pour
les deux jeux de **capteurs**, l'absence de canal `snmp` veut dire « ne pas collecter »
(callout ci-dessus). Appliquer 5 s serait **parier sur le plan de contrôle** d'un équipement
qu'on ne connaît pas — *un équipement qu'on supervise trop est un équipement qu'on
abîme*. Ne rien collecter priverait de supervision précisément les équipements les
moins bien connus. Une minute : assez lent pour n'abîmer personne, assez rapide
pour qu'un écran ne reste pas vide. C'est aussi la valeur que ce chemin utilisait
déjà en repli, ce qui évite de faire porter à un parc existant une régression de
charge par effet de bord.

> [!warning] La contrepartie : à 30 s, la réactivité des ports DÉPEND des traps
> C'est un arbitrage assumé, pas un oubli. Sur un équipement dont le destinataire
> de traps n'est pas configuré, un port tombé met désormais jusqu'à **30 s** à
> apparaître, contre 5 s avant ce lot.
>
> Et ce n'est pas un cas de bord : la pose du destinataire de traps dans les
> équipements **n'est construite nulle part aujourd'hui** (§7, étape 6). Le
> listener du proxy fonctionne, mais il ne reçoit que ce que des équipements
> configurés à la main veulent bien lui envoyer. Sur un parc neuf, la cadence
> effective de détection d'un port tombé est donc de 30 s, point.
>
> Ce qui rend l'arbitrage tenable est ce qui le rend **réversible** : la cadence
> voulue est une constante de politique, pas une propriété du protocole. Ce qui le
> rend **honnête** est le champ `trigger` des événements de port (`"poll"` /
> `"trap"`), seul moyen de constater qu'un parc reçoit réellement ses traps — sans
> lui, un parc muet et un parc réactif produisent les mêmes événements à trente
> secondes près, ce que personne ne remarque sur un écran.

## 2. Où ça s'exécute : le proxy, pas le contrôleur

SNMP et streaming telemetry sont **exécutés par le [[Proxy]]**, et les traps y
sont *forcément* reçus : un trap est un datagramme UDP envoyé à une adresse
concrète, il n'y a rien à relayer et rien à tunneliser.

Faire poller le contrôleur à travers le tunnel coûterait un aller-retour WAN par
métrique et par équipement, et transformerait chaque coupure de lien en trou de
supervision généralisé. Le proxy collecte, agrège, et ne remonte que le
résultat ([[Protocole contrôleur-proxy]]).

Conséquence assumée : **le proxy garde une file locale**. Si le tunnel tombe,
il continue de collecter et rejoue à la reconnexion, dans la limite de sa
rétention. Une supervision qui perd les cinq minutes d'une coupure est une
supervision qui ment précisément au moment le plus intéressant. Cette limite est
un nombre, pas une intention : elle est chiffrée en §2.4, avec le débit que la
remontée évacue et ce qui se passe au-delà.

### 2.1 Rattacher une mesure au bon port

Une métrique SNMP arrive indexée par `ifIndex`. Encore faut-il savoir de quel
**port de façade** elle parle, et c'est plus retors qu'il n'y paraît. Trois
approches, dont deux fausses.

**Par arithmétique sur l'index** (`préfixe + ifIndex`) : c'est ce qui était fait,
et ça ne marchait que par chance. Un Instant On 1930 numérote ses ports de 1 à 52
dans l'ordre des index — coïncidence de constructeur, pas propriété du protocole.
Sur un MikroTik dont la façade mélange `ether1` et `sfp-sfpplus1`, l'arithmétique
désigne des ports qui n'existent pas. Pire, un équipement expose bien plus
d'interfaces qu'il n'a de ports : relevés sur un 1930, les agrégats `TRK*` à
l'index 1000, les tunnels à 3000, les interfaces de VLAN à 100000. Écrites telles
quelles, elles créaient des **ports fantômes** dans l'inventaire — 93 entrées pour
52 ports réels sur le switch qui a servi à les débusquer (purge : migration
`0019`).

**Par le libellé seul** (`ifDescr`) : pire encore, parce que les libellés
**collisionnent**. Sur ce même 1930, l'interface du VLAN 42 s'appelle « 42 »,
exactement comme le port physique 42. Se fier au libellé attribuerait le trafic
d'un VLAN à un port : un graphe crédible et faux, plus grave qu'un port fantôme,
qui lui se voit.

| ifIndex | ifDescr | nature |
|---|---|---|
| 42 | `42` | port physique |
| 100041 | `42` | interface du VLAN 42 |
| 1000 | `TRK1` | agrégat |

**La bonne réponse tient en deux temps.** D'abord `ifType` décide de ce qui est
physique : `ethernetCsmacd(6)` est la seule réponse vendor-neutre, et c'est une
colonne **obligatoire** de MIB-II. Elle écarte d'un coup agrégats, tunnels et
interfaces de VLAN, quel que soit le constructeur. Ensuite seulement `ifDescr`
sert à **nommer** le port, par une échelle qui va du plus sûr au plus tolérant :
égalité exacte (les MikroTik nomment leurs interfaces comme leur façade), égalité
normalisée à la casse et à la ponctuation, puis préfixe de façade + libellé
(« 1 » devient « GE1 »). Un libellé qu'on ne sait pas rattacher est **écarté**,
jamais inventé — et deux ports que la normalisation confondrait ne sont pas
départagés au hasard, ils sont refusés.

> [!important] La télémétrie ne CRÉE jamais un port
> Une mesure décrit un port, elle ne le découvre pas. Le contrôleur exige
> l'étiquette `iftype` pour toucher l'inventaire : un proxy d'une version
> antérieure alimente encore les métriques mais ne modifie plus la façade.
> Fermé par défaut, parce que l'alternative serait de deviner.

Le filtrage a lieu **côté proxy**, pour ne pas payer la cardinalité de séries
qu'on ne veut pas ; le rattachement au port a lieu **côté contrôleur**, seul à
connaître la façade.

### 2.2 Qui a le droit de répondre pour une adresse

Le proxy pingue par **adresse** : sa table d'états est indexée par IP. Lui
envoyer deux équipements différents à la même adresse ne produit donc pas deux
surveillances mais une seule, la seconde écrasant la première en silence — un
voyant vert sur un switch éteint. Le cas survient dès qu'un proxy d'instance
couvre deux sites qui réutilisent le même plan d'adressage privé.

C'est le [[Domaines d'adressage|domaine d'adressage]] qui départage : l'emporte
l'équipement dont le domaine est déclaré **sur ce proxy**. Si aucun ne l'est, ou
si plusieurs le sont, tous sont écartés avec leur motif — ne rien dire vaut mieux
que dire faux. L'exclusion apparaît en bandeau sur la fiche de l'équipement et
émet un `watch.excluded`, à la transition seulement.

Le filtre vaut **aussi pour les traps** (§2.3) : un équipement écarté de la
surveillance n'a pas de `SNMPWatch` sur ce proxy, donc son IP source n'y est pas
reconnue et ses traps sont ignorés. C'est la bonne conséquence — attribuer un
`linkDown` à celui des deux équipements qui n'a pas bougé serait pire que de
n'avoir rien reçu.

### 2.3 Le chemin rapide : un trap avance un poll, il ne le remplace pas

Trois cadences cohabitent, et il faut les distinguer pour comprendre où va quoi :

| Chemin | Transport | Rythme | Ce qu'il porte |
|---|---|---|---|
| Échantillons de métriques | tampon local → contrôleur | vidage toutes les **2 s**, jusqu'à 4 lots (§2.4) | séries temporelles (§3) |
| Transitions | canal d'événements du tunnel | **immédiat** | `device.*`, `port.*` ([[Protocole contrôleur-proxy]]) |
| Trap reçu | rien ne traverse le WAN | — | un **déclencheur de poll**, local au proxy |

Le trap n'est pas un quatrième canal de données : il ne remonte pas. Il provoque,
**sur le proxy**, le poll `if_state` qui était de toute façon programmé, et c'est
la transition dérivée de ce poll qui part sur le canal d'événements. Toute la
justification est dans [[Monitoring]] §2 ; ce qui appartient à cette note, c'est
la conséquence sur les rythmes :

- la réactivité d'un port ne dépend **pas** du vidage du tampon. Une pastille qui
  attendrait l'ingestion des séries afficherait le retard du pipeline de
  métriques, pas l'état du réseau ;
- le plancher du connecteur (`MinInterval`, §1.1) borne le polling **périodique**.
  Le chemin rapide a son propre amortisseur, et il est plus serré : 250 ms de
  coalescence, 2 s au minimum entre deux polls déclenchés par trap sur un même
  équipement. Ce n'est pas une exception au plancher, c'est son application à un
  régime différent — *un équipement qu'on supervise trop est un équipement qu'on
  abîme* reste vrai à une seconde comme à trente ;
- un `if_state` avancé par un trap **est** le poll du cycle, pas un poll de plus :
  il repousse la cadence ordinaire d'autant. Sans ce décalage, le cycle régulier
  relirait la même table dans la seconde qui suit — deux walks pour une seule
  information, et sur l'équipement qui va justement mal.

> [!warning] Le chemin rapide se mesure, sinon il s'imagine
> Le champ `trigger` des événements de port (`"poll"` / `"trap"`) est la seule
> façon de constater en exploitation que des traps arrivent réellement. Un parc où
> les équipements n'ont jamais reçu leur configuration de destination produit
> exactement les mêmes événements qu'un parc réactif — à trente secondes près, ce
> que personne ne remarque sur un écran.

### 2.4 Ce que la remontée évacue : les limites, en chiffres

Une cadence qui ne remonte pas n'est pas une cadence. Descendre `if_counters` à
5 s (§1.2) déplace le problème du plan de contrôle de l'équipement vers le **débit
de la remontée** du proxy, et celui-là se calcule.

**Ce qu'un équipement produit.** Le collecteur rend **quatre** échantillons par
port pour `if_counters` (octets entrants et sortants, erreurs entrantes et
sortantes) et **deux** pour `if_state` (état, vitesse). Un 1930 48 ports expose 52
interfaces physiques — 48 cuivre plus 4 SFP :

| Métrique | Échantillons | Cadence | Débit |
|---|---|---|---|
| `if_counters` | 52 × 4 = 208 | 5 s | 41,6 /s |
| `if_state` | 52 × 2 = 104 | 30 s | ≈ 3,5 /s |
| **total par équipement** | | | **≈ 45 /s** |

**Ce que la remontée évacue.** Le tampon est vidé toutes les **2 s**, par rafales
de **4 lots** au plus, de **3000** échantillons chacun — soit **6000 échantillons
par seconde** en régime permanent, donc **133 équipements de 48 ports par proxy**.
Le réglage précédent (5000 échantillons toutes les 10 s) n'évacuait que 500/s,
c'est-à-dire **onze** équipements : la cadence à 5 s aurait débordé dès le
douzième. La rafale est aussi ce qui permet à un tampon en retard — retour de
coupure de tunnel — de se rattraper au lieu de courir derrière la collecte pour
toujours. Au plafond, cela demande au contrôleur environ 2 requêtes par seconde de
~670 KiB, ≈ 1,3 Mo/s — c'est le prix affiché de la cadence, et il est **linéaire**
en nombre d'équipements.

**Ce qui se passe au-delà.** Le tampon **déborde et jette ses points les plus
vieux**. C'est le mode d'échec que la cadence à 5 s rend possible, et il est
parfaitement silencieux vu de l'exploitant : le tunnel est debout, les requêtes
réussissent, il ne reste que des trous dans les courbes. Il est donc **journalisé**
en avertissement, distinctement des pertes qui accompagnent un échec d'envoi et,
depuis ce lot, de l'**abandon** d'un lot sur refus définitif : trois lignes de
journal pour trois symptômes, parce que ce ne sont pas les mêmes gestes (voir plus
bas). La ligne porte le compte de la **période** et non le cumul depuis le
démarrage (« 42 000 points perdus
depuis la dernière minute » se décide, « 3 200 000 depuis le démarrage » se subit),
et elle porte le **débit soutenable**, parce que la première question de celui qui
la lit est « combien puis-je en encaisser ? ». Amortie à une ligne par minute : une
saturation dure, et une ligne toutes les deux secondes noierait le journal au lieu
de l'informer. La toute première perte, elle, se dit immédiatement :
l'amortissement doit empêcher la répétition d'un symptôme, pas retarder son
apparition.

**Ce que le tampon couvre.** Le débit étant réglé, le tampon ne sert plus à
absorber un déficit permanent : il sert à couvrir une **coupure de tunnel**, ce
pour quoi il a été écrit (§2). Sa taille — 150 000 échantillons — se lit donc en
durée d'indisponibilité :

| Parc du proxy | Coupure absorbée |
|---|---|
| au plafond de débit (133 équipements) | 25 s |
| 20 équipements de 48 ports | 2 min 46 s |
| 12 équipements de 48 ports | 4 min 37 s |

La borne haute n'est pas un nombre d'échantillons, c'est la **mémoire du proxy**,
qui tourne sur une VM minimale ou un Raspberry Pi ([[Proxy]]) : mesuré, un
échantillon occupe 401 octets en tas — l'essentiel étant sa carte d'étiquettes,
pas sa valeur. 150 000 points plafonnent donc à **~57 Mio**, et seulement pendant
une coupure. Doubler la capacité doublerait ce chiffre : **c'est là qu'est
l'arbitrage**, pas dans le nombre d'échantillons.

> [!warning] Deux plafonds bornent un lot, et la route de télémétrie a le sien
> Cette route décode son corps derrière un plafond de **8 MiB**
> (`maxProxyMetricsBody`, `internal/api/proxymetrics_handlers.go`), là où **tout le
> reste de l'API** garde le plafond générique de **1 MiB** (`internal/api.maxBody`,
> emprunté par `decode`). Ce n'est pas un privilège, c'est une différence de nature :
> le plafond générique est taillé pour ce que cette API reçoit vraiment — des
> formulaires, des identifiants, des listes de VLAN — et une route dont la charge
> utile **est un lot** ne se règle pas comme un formulaire. Sa taille n'est pas un
> accident à borner par prudence, c'est le paramètre de débit lui-même.
>
> **Ce que l'ancien réglage coûtait.** La route annonçait un garde-fou de **20 000**
> échantillons par lot (`maxSamplesPerBatch`) tout en lisant son corps avec le
> décodeur générique à 1 MiB, soit ~5000 échantillons : le garde-fou affiché était
> **inatteignable**, on ne pouvait pas lui soumettre ce qu'il disait accepter. Et la
> troncature répondait à sa place — erreur de syntaxe au décodeur, **400 muet** au
> proxy, rejeu perpétuel (voir *Le mode de défaillance*, plus bas).
>
> **Desserrer plutôt qu'abaisser le nombre annoncé**, et ce n'est pas du confort : un
> plafond en **nombre** ne peut pas être rendu exact par un plafond en **octets**
> qu'il ne connaît pas, puisque le poids d'un échantillon dépend d'un nom de port que
> l'exploitant choisit. Abaisser à ~4000 aurait **déplacé le mensonge** sans le
> supprimer, et sacrifié 75 % de la capacité annoncée pour couvrir le pire cas de
> nommage. Abaisser aurait en plus **refusé** un proxy d'une autre version qui a lu
> « 20 000 » et s'y tient — c'est-à-dire exactement le proxy qui se bloquait. Le
> desserrement le débloque au lieu de lui opposer un refus mieux formulé.
>
> **Pourquoi 8 MiB et pas autre chose.** Le plafond en octets est dérivé du plafond
> en nombre, dans ce sens-là et pas l'inverse : 20 000 × `worstBytesPerSample`
> (320 o, voir plus bas) = 6,1 MiB, et 8 MiB laisse 31 % au-dessus. Au poids
> réellement mesuré (229 o), un lot plein pèse 4,4 MiB, soit 54 % du plafond.
>
> Le prix est en **mémoire transitoire** : 20 000 échantillons décodés, chacun avec
> sa carte d'étiquettes, tiennent une dizaine de Mo le temps d'une requête. C'est le
> prix du lot que la route dit accepter, et c'est précisément ce que
> `maxSamplesPerBatch` borne. 8 MiB n'est pas non plus un motif nouveau dans ce
> code : c'est déjà le plafond de l'import de configuration de site.

**Ce que pèse un échantillon : mesuré, avec une seule variable libre.** Le chiffre
n'est pas une estimation — il sort de `snmppoll.Samples` sur des échantillons
réellement produits (52 ports, identifiant d'équipement en UUID), avec leurs
étiquettes `device`, `ifindex`, `port`, `iftype`, un nom de métrique du genre
`meshcraft_if_in_octets_total`, un horodatage RFC 3339 **avec ses nanosecondes** et
l'enveloppe d'authentification du POST :

| Cas mesuré | Poids d'un compteur en JSON |
|---|---|
| port au nommage d'usine (« GE12 ») | **196 o** |
| port renommé par l'exploitant (« Salle serveurs - baie B12 - uplink 12 », 37 car.) | **229 o** |

Ce sont les **compteurs** qui dimensionnent un lot, et c'est une conséquence directe
des cadences de §1.2 : `if_counters` se lit toutes les **5 s** quand `if_state` se
lit toutes les **30 s**, donc un lot est fait de compteurs presque exclusivement — et
un compteur est le plus lourd des six échantillons d'un port, treize chiffres de
valeur contre un pour un état. Dimensionner sur la moyenne des deux métriques
sous-estimerait tous les lots réels.

> [!important] Le nom de port est TOUTE la variable — c'est ça qu'il faut retenir
> Un chiffre plus ancien de **208 o** a circulé, y compris dans cette page. Il n'était
> pas faux, il était **daté** : il venait d'un prototype qui nommait ses ports
> « GigabitEthernet52 », et les douze octets d'écart avec les 196 o du nommage
> d'usine sont exactement la différence de longueur du libellé. Rien d'autre ne bouge
> d'un cas à l'autre.
>
> Or ce libellé est **saisi par l'exploitant** et **aucun code ne le borne**. Les deux
> valeurs ci-dessus sont donc des points de repère, pas des invariants : ce qu'il faut
> retenir, c'est que la seule inconnue du dimensionnement d'un lot est une chaîne de
> caractères que le produit ne maîtrise pas. D'où `worstBytesPerSample = 320`, qui
> laisse 40 % au-delà du cas long mesuré — la place d'un libellé de 128 caractères — et
> le test `TestPoidsDUnEchantillonSNMP`, qui la verrouille : le jour où la forme d'un
> échantillon grossit, c'est un test qui le dit, pas un proxy bloqué en production.
>
> À ne pas confondre : les **208 échantillons** du tableau de production ci-dessus
> sont un nombre de points par poll d'un 52 ports (52 × 4). La coïncidence avec
> l'ancien 208 o est fortuite, et elle a coûté assez de relectures pour mériter d'être
> dite.

**Le mur de `maxPerFlush` a changé de côté.** Il y a deux murs, et c'est le plus bas
qui compte :

| Mur | Valeur | Lot maximal qu'il autorise |
|---|---|---|
| plafond de corps de la route | 8 MiB | ~**36 000** échantillons au poids long (229 o) |
| garde-fou en nombre | **20 000** échantillons | 20 000 |

Tant que la route empruntait le décodeur générique à 1 MiB, ce sont les **octets**
qui mordaient les premiers : l'ancien lot de 5000 pesait 1015 KiB, soit **99 %** du
plafond — un blocage à la première étiquette un peu plus longue. Depuis le
desserrement, le mur est le **garde-fou en nombre**, et il est loin. Le lot est fixé
à **3000**, soit **15 % du mur** — et 8 % du plafond de corps, un lot plein au poids
long pesant ~672 KiB. Le test ne recopie plus ce mur, il le **calcule** à partir du
poids qu'il vient de mesurer et échoue si `maxPerFlush` dépasse 75 % de sa valeur
(`cmd/proxy/snmp_throughput_test.go`) : un chiffre juste par accident redevient une
opinion à la première relecture. C'est toujours la **fréquence** qui porte le débit,
pas la taille du lot.

**Le mode de défaillance, réparé — et c'est le vrai objet de ce lot.** Ce qui précède
change une capacité ; ceci change un **comportement en cas d'échec**, et il faut
comprendre d'abord ce que l'ancien coûtait. `metricbuf.Nack` rend le lot réservé **en
tête de file** : le `Take` suivant reprend donc exactement le même. Un lot refusé
pour sa **forme** revient identique au flush suivant, et à tous les suivants. Ce
n'était donc pas une perte partielle, c'était **l'arrêt de toute la remontée** de ce
proxy : le tampon ne se vide plus, il déborde par le haut, et ce sont les points les
plus **récents** — ceux que la collecte vient de produire — qui meurent, sans borne,
pour un seul lot que le contrôleur ne prendrait jamais.

La frontière retenue (`refusDefinitif`, `cmd/proxy/snmp.go`) :

| Verdict | Cas | Ce que `flush` en fait |
|---|---|---|
| **refus définitif** — le contrôleur a lu la requête et la refuse pour sa **forme** | `400`, `413`, `415`, `422` | **abandonne** le lot (`Ack`), avec un journal portant le nombre de points **sacrifiés**, la taille du lot en octets et le **motif rendu par le contrôleur** |
| **échec passager** | transport, `5xx`, `429`, `408`, `404`/`405` (route absente) | **garde** le lot en tampon (`Nack`) — c'est la raison d'être du tampon |
| `Retry-After` présent | **quel que soit le code** | **passager**, et c'est testé en premier |

`Retry-After` d'abord, délibérément : un refus qui dit *quand revenir* est un refus
qui attend qu'on revienne. C'est exactement ce qu'un « `>= 400` » un peu large
rangerait du mauvais côté — un 429 ou un 503 de maintenance sont des demandes de
patience, pas des désaccords sur le lot.

Le mot qui compte dans la ligne d'abandon est « **abandonnés** » : c'est un nombre de
points de mesure qui n'existeront plus jamais, et il n'a pas d'autre endroit où se
dire. Le motif du contrôleur, lui, n'est lu que sur ce chemin-là, et c'est délibéré :
« 3000 points abandonnés, status 413 » laisse deviner lequel des réglages est en
cause, alors que la phrase du contrôleur le **nomme** (« réduisez `maxPerFlush`, ce
n'est pas un problème de contenu »). Sur le chemin du rejeu, la même ligne reviendrait
toutes les deux secondes pour rien. Un abandon **arrête aussi la rafale**, alors que
le tampon a pourtant avancé : si le refus est systématique — un contrôleur qui refuse
tout ce que ce proxy lui envoie — enchaîner sacrifierait quatre lots par réveil au
lieu d'un. *Perdre lentement laisse au journal le temps d'être lu.*

> [!important] La liste des cas définitifs est FERMÉE, et l'asymétrie l'explique
> Les deux erreurs de classement ne coûtent pas le même prix. Croire **définitif** ce
> qui était passager perd **un lot** : borné, visible, chiffré. Croire **passager** ce
> qui était définitif **arrête la remontée** : sans borne. Ce qu'on ne sait pas classer
> est pourtant traité comme **passager** — donc du côté coûteux, et il faut dire
> pourquoi ce n'est pas une incohérence. Un abandon est **irréversible** : les points
> sont détruits. Un blocage est **bruyant et réparable** : chaque refus a sa ligne de
> journal, `reportSaturation` répète la perte chaque minute, et un déploiement ou une
> correction de configuration le lève sans rien avoir perdu de plus. Entre détruire en
> silence et bloquer en criant, on bloque.
>
> Ce qui rend le choix tenable n'est donc pas le repli lui-même mais le fait que la
> **liste passagère soit énumérée aussi**, cas par cas, plutôt que laissée à un
> défaut : sur cette route, les refus que le contrôleur produit réellement sont tous
> classés à la main, des deux côtés. Le repli ne couvre que des codes qui n'arrivent
> pas — et si l'un d'eux arrivait, il bloquerait la remontée en le disant.

> [!warning] `401` / `403` classés PASSAGERS : la décision la plus discutable du lot
> Elle se tient sur un fait vérifiable dans `handleProxyMetrics` : ici, le contrôleur
> ne reproche rien **au lot** — il ne l'a même pas regardé, l'authentification passant
> **après** la lecture du corps. Et l'autorisation d'un proxy est une chose qui
> **change** : un secret se rotationne ([[Identité de service et rotation]]), une
> révocation s'annule, un contrôleur qui redémarre peut refuser une seconde avant
> d'accepter. Abandonner jetterait des points qu'un enrôlement à jour aurait pris.
>
> **L'objection est réelle, et elle n'est pas réparée** : un proxy réellement révoqué
> rejoue pour toujours et ne remonte plus rien. Ce qui fait accepter la décision n'est
> pas que l'objection soit petite, c'est qu'elle **ne se répare pas ici** —
> abandonner sur 403 ne remonterait pas un point de plus, ça n'en effacerait que la
> trace. Le tampon est borné et jette ses plus vieux, `reportSaturation` le dit chaque
> minute, chaque refus a sa ligne, et un proxy révoqué n'a de toute façon plus de
> destinataire pour ses métriques. À écrire ici plutôt qu'à découvrir : si un jour un
> proxy révoqué doit **cesser** de rejouer, c'est de l'enrôlement que la décision
> viendra, pas de cette frontière.

> [!note] Pourquoi un refus par la TAILLE s'abandonne AUSSI
> C'est le cas où l'abandon fait le plus mal : le contenu du lot est bon, seul son
> volume ne passe pas, et le **redécouper** vaudrait mieux que le jeter. L'API de
> `metricbuf` ne le permet pas naturellement : `Take` rend le lot réservé **tel quel**
> en ignorant son argument tant qu'il n'est ni acquitté ni rendu, et il n'existe ni
> acquittement partiel ni redimensionnement. Le seul chemin serait `Nack` puis un
> `Take` plus petit au tour suivant — donc une taille réduite à mémoriser, un moment
> où la faire remonter, une cohabitation à régler avec la rafale : une politique de
> repli entière pour un cas que le contrôleur **ne produit plus**, un lot de 3000
> pesant ~672 KiB sous un plafond de 8 MiB. L'abandon journalisé suffit, et il a
> l'avantage de ne pas pouvoir boucler.

> [!note] Ce sont des limites CONNUES, pas des promesses
> Aucun de ces chiffres n'est une garantie de tenue commerciale : ils disent ce que
> les constantes livrées impliquent, et ils changent avec elles — le débit
> soutenable est d'ailleurs **calculé** dans le code plutôt que recopié, pour qu'un
> réglage retouché ne laisse pas le journal annoncer une capacité qui n'existe
> plus. Ce qu'ils apportent est ailleurs : une capacité qu'on découvre en
> production est une panne, une capacité écrite est un choix d'architecture. « Ce
> proxy tient 133 switchs de 48 ports » se dimensionne ; « ça devrait passer » se
> constate un mardi après-midi, dans un graphe troué.

## 3. Stockage : VictoriaMetrics

[[Monitoring]] prévoyait Postgres partitionné « au MVP ». La décision est prise :
c'est **VictoriaMetrics**, dès maintenant.

La raison n'est pas la mode, c'est la forme de la donnée. Le calcul de §2.4 la
donne directement : ≈ 45 échantillons par seconde et par équipement de 48 ports,
donc **9000 par seconde** sur un parc de deux cents équipements — de l'ordre de
**trente millions de points par heure**. C'est une charge d'écriture que Postgres
accepte, mais qu'il fait payer sur *tout le reste*, y compris l'inventaire et le
journal qui vivent dans la même base. Séparer les deux, c'est empêcher qu'un pic de
métriques ralentisse l'écran d'administration.

> [!note] Le même chiffre dit aussi combien il faut de proxys
> 9000 échantillons/s dépassent ce qu'**un** proxy évacue (6000/s, §2.4). Un parc
> de deux cents équipements de 48 ports demande donc **deux proxys au moins**, non
> pas pour la joignabilité mais pour le débit de remontée. C'est une contrainte de
> dimensionnement, et elle se lit ici plutôt que se découvrir dans un journal de
> saturation.

Ce qui reste en Postgres : l'inventaire, les intents, le
[[Journal d'événements]] et les pipelines. **Ce qui part en VictoriaMetrics : les
séries temporelles, et rien d'autre.** Un événement n'est pas une métrique — le
confondre donnerait un journal qu'on ne peut pas relire et des métriques qu'on ne
peut pas agréger.

- ingestion par le protocole Prometheus (`/api/v1/import/prometheus`), que le
  contrôleur expose au proxy ;
- rétention par défaut 90 jours, dégradation par `-downsampling` ;
- pas d'exposition publique : VictoriaMetrics n'est joignable que par le
  contrôleur, comme la base.

## 4. Pousser la configuration de collecte

Un équipement onboardé doit être **configuré pour être supervisé** : user SNMPv3
ou communauté v2c en lecture seule, cibles de traps et de syslog, abonnement
gNMI. C'est de l'intent comme le reste, donc de la [[Réconciliation]] — pas un
chemin spécial.

La cible de traps est **une adresse IP concrète**, et c'est ce qui rend la question
du collecteur inévitable ici — nulle part ailleurs. Deux réponses possibles, et
c'est la seconde qui est construite :

| | Élection | **Déclaration** |
|---|---|---|
| Qui décide | le contrôleur, d'après `device_paths` (chemin valide le plus rapide) | l'**opérateur**, sur la fiche de l'équipement |
| Où c'est écrit | `device_collectors`, matérialisé depuis un calcul | `devices.proxy_id` ([[Proxy]]) |
| État | **décidée, non construite** ([[Monitoring]], § *Choix du point de collecte*) | **livrée** — migration `0022` |

Le rapport entre les deux est simple, et il ne s'invente pas : la déclaration est
**la brique manquante de l'élection**, pas son remplacement. Elle donne dès
maintenant l'unique adresse dont l'étape 6 de §7 a besoin, sans le recalcul
automatique — c'est-à-dire sans le risque que porte l'élection, qui est de faire
changer le collecteur **tout seul** et de rendre fausse une destination de traps
déjà écrite dans les équipements, sans que personne n'ait rien touché. Le jour où
l'élection existera, elle ne pourra que **proposer** un rattachement, jamais
l'imposer.

> [!note] Recevoir un trap ne demande toujours aucun collecteur
> Le proxy qui reçoit le datagramme reconnaît l'IP source dans sa propre liste de
> surveillance et résout l'équipement localement. Le rattachement ne change rien à
> cette réception ; il décide **qui interroge** en SNMP et **quelle adresse sera
> écrite** dans l'équipement. C'est pour ça que l'étape 5 de §7 peut précéder
> l'étape 6, et pas l'inverse.

## 5. Pipelines : rendre visible ce qui coince

C'est la partie qu'on oublie toujours, et c'est celle qui décide si le produit
est exploitable. Un déploiement de configuration, une pose d'identifiant, une
campagne de supervision : tout cela **prend du temps, échoue partiellement, et
reste coincé**. Aujourd'hui, un apply qui échoue laisse un événement et rien
d'autre — il faut deviner.

Un **pipeline** est donc un objet de première classe :

```sql
pipeline(id pk, site_id fk, kind text,        -- config | credential | monitoring
         device_id fk nullable,               -- null = campagne de parc
         state text,                          -- pending|running|blocked|done|failed
         created_at, started_at, ended_at,
         blocked_reason text,                 -- EN CLAIR, pas un code
         created_by fk users)

pipeline_step(id pk, pipeline_id fk, seq int, name text,
              state text, started_at, ended_at,
              detail text,                    -- ce que l'équipement a répondu
              transcript jsonb)               -- la trace intégrale
```

Trois principes :

1. **`blocked` est un état, pas un échec.** Un équipement éteint, un proxy
   déconnecté, une fenêtre de maintenance non ouverte : le pipeline attend, et
   dit quoi. Le confondre avec `failed` pousse à relancer ce qui n'a pas besoin
   de l'être.
2. **La raison est une phrase**, pas un code. « En attente : le proxy
   `proxy-festival` est déconnecté depuis 4 min » se lit ; `ERR_PROXY_DOWN` se
   cherche.
3. **Le transcript est conservé**, comme pour les sessions web des connecteurs.
   Sur une gamme où une erreur se paie d'un déplacement, savoir *après coup* ce
   qui a été envoyé est la seule façon de comprendre sans retourner sur site.

L'UI en découle : une vue **Pipelines** filtrable par état, où `blocked` et
`failed` remontent en tête. La question « qu'est-ce qui est coincé, et
pourquoi ? » doit se répondre en un écran, sans lire un journal.

## 6. Zone de debug : le journal vivant de l'application

Distinct du [[Journal d'événements]], et il ne faut pas les confondre :

| | [[Journal d'événements]] | Zone de debug |
|---|---|---|
| Sujet | le PARC | l'APPLICATION |
| Public | l'exploitant | celui qui diagnostique meshcraft |
| Exemple | « port 12 down » | « connecteur instanton : HTTP 200 sans code d'état » |
| Rétention | longue, en base | courte, en mémoire |

Concrètement : un anneau en mémoire (quelques milliers de lignes) alimenté par
`slog`, exposé en flux temps réel au `global-admin`, filtrable par niveau et par
composant. Trois exigences :

- **en mémoire, pas en base.** Un journal de debug qui écrit en base ajoute de la
  charge au moment précis où quelque chose va mal ;
- **jamais de secret.** Le flux passe par le même filtre que les transcripts :
  un mot de passe qui transite dans un log de debug est un mot de passe fuité ;
- **réservé au `global-admin`** ([[RBAC]]). Les erreurs internes décrivent la
  structure du produit ; ce n'est pas une information d'exploitant.

## 7. Ordre de livraison

Rien de tout ceci ne s'allume d'un coup, et l'ordre n'est pas indifférent —
chaque étage a besoin du précédent pour être vérifiable :

1. **VictoriaMetrics** déployé et joignable, sans producteur : on vérifie le
   stockage avant d'y écrire ;
2. **la capability de monitoring** déclarée par les connecteurs : sans elle,
   toute planification serait une devinette ;
3. **les pipelines**, avec les poses de configuration existantes comme premiers
   clients : c'est ce qui rend la suite diagnosticable ;
4. **la zone de debug**, qui devient utile dès qu'il y a des pipelines à
   observer ;
5. **la collecte proxy** (SNMP, puis traps, puis streaming), un canal à la fois,
   chacun vérifié sur un équipement réel avant le suivant. Les traps arrivent
   **après** le polling, et pas par commodité : sans polling il n'y aurait rien à
   accélérer, et un listener livré avant lui n'aurait aucune source d'état à
   avancer (§2.3) ;
6. **la pousse de la configuration de collecte**, en dernier : c'est une
   écriture sur le parc, et elle ne doit partir que quand on sait déjà lire. Tant
   qu'elle n'est pas là, le listener de traps est fonctionnel mais **muet sur la
   plupart des parcs** — seuls les équipements déjà configurés à la main pointent
   vers un proxy. Un canal qui ne reçoit rien n'est pas un canal en panne : c'est
   l'étape 6 qui manque, et le champ `trigger` est ce qui permet de le distinguer.
