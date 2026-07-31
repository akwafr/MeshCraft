---
tags: [architecture, monitoring]
---

# Journal d'événements

Le fil chronologique unique de tout ce qui se passe sur le parc, alimenté par le [[Monitoring]] et par les actions du système lui-même. Visible par site (filtré [[RBAC]]), en temps réel dans l'[[Interface web]].

## Le catalogue, rangé par famille de préfixe

Cette page a longtemps rangé les types **par source**, et c'est ce qui l'a rendue
fausse. Deux raisons, et aucune n'est cosmétique :

- **la source n'est pas une propriété du type.** `config.applied` s'écrit sous la
  main d'un opérateur *et* sous l'application automatique ; `trap.dest_applied` sous
  un provisionnement de fond *et* sous un geste explicite (avec deux sévérités
  différentes, cf. plus bas) ; `proxy.coverage_changed` par deux routes dont l'une
  est de site et l'autre d'instance. Une colonne « source » oblige à en choisir
  une, donc à mentir sur les autres ;
- **le filtre du journal est un préfixe**, littéralement : `EventFilter.Type` devient
  `type like 'config.%'` (`internal/store/events.go`). La **famille** est donc
  l'unité de lecture réelle — c'est elle qu'on tape dans un champ de recherche,
  c'est elle qui décide de ce qu'on voit ensemble, et c'est elle qui fait remonter
  des lignes qu'on n'attendait pas (cf. *Les vieilles lignes*, plus bas).

Deux axes remplacent la source, et ils portent chacun une décision :

- **la portée** — `site_id` non nul (journal du site, filtré [[RBAC]]) ou nul
  (journal d'**instance**, réservé au `global-admin` : cf. le callout ci-dessous) ;
- **`actor`** — l'adresse de qui a agi, ou `surveillance` / `proxy` / `meshcraft`
  pour ce que personne n'a demandé. C'est ce qui sépare l'**audit** du **constat**,
  et c'est le seul champ qui le fasse.

> [!note] Ce qui suit a été relevé par `grep` dans `internal/`, pas recopié d'une intention
> Chaque type de la première table a un producteur nommé dans le code. Chaque type
> de la seconde n'en a **aucun**, et c'est dit ligne par ligne. La distinction est la
> valeur principale de cette page : sans elle, on passe une heure à chercher le code
> qui écrit un type qui n'a jamais été écrit — et on finit par croire que c'est le
> code qui a un défaut.
>
> Ne sont **pas** des types de journal : `device.up`, `device.down`, `port.up`,
> `port.down`, `host.seen`, `sweep.done`, `trap.seen`. Ce sont les noms d'événements
> du **protocole** proxy → contrôleur (`internal/tunnel/control.go`), et la
> ressemblance de deux d'entre eux avec des types de journal est un piège :
> `tunnel.EventPortUp` est ce que le proxy *rapporte*, `port.up` est ce que le
> contrôleur *écrit après corrélation*, et il arrive que le premier n'engendre pas le
> second ([[Protocole contrôleur-proxy]]).

### Émis par le code — 88 types, 19 familles

| Famille | Portée | Types | Ce que la famille journalise |
|---|---|---|---|
| `config.` | site | `config.applied` · `config.apply_failed` · `config.drift` · `config.absorbed` · `config.forced` · `config.imported` · `config.import_failed` | le cycle de la configuration désirée : pose, échec, écart constaté à la relecture, absorption de l'écart dans l'intent ([[Réconciliation]]). `config.forced` marque le franchissement d'un garde-fou, `config.import_failed` est le seul type en **erreur** du catalogue |
| `credential.` | site, sauf `credential.created` (instance) | `credential.created` · `credential.generated` · `credential.updated` · `credential.deleted` · `credential.rotated` · `credential.revealed` · `credential.mode_changed` · `credential.installed` · `credential.apply_failed` · `credential.apply_unconfirmed` · `credential.generate_failed` · `credential.scope_ambiguous` · `credential.stale_account` | tout ce qui touche aux identifiants, **sans jamais porter de secret** ([[Credentials]]). `credential.revealed` et `credential.rotated` sont en **avertissement** : ce ne sont pas des alertes, c'est de l'audit qu'on veut voir passer. `credential.created` est d'instance parce qu'un identifiant standard n'appartient à aucun site ([[Étiquettes et portée des identifiants]]) |
| `device.` | site, sauf `device.onboarded`, `device.onboard_failed`, `device.page_read` (instance) | `device.created` · `device.adopted` · `device.deleted` · `device.moved_in` · `device.moved_out` · `device.collector` · `device.address_domain` · `device.relocate_started` · `device.relocated` · `device.unreachable` · `device.recovered` · `device.snmp_walk` · `device.web_probed` · `device.onboarded` · `device.onboard_failed` · `device.page_read` | la vie d'un équipement, de l'entrée au parc au changement de collecteur ([[Proxy]]) ou de domaine d'adressage ([[Domaines d'adressage]]). `device.unreachable` / `device.recovered` sont les **seules** lignes de joignabilité, et la seule source du statut affiché — une affirmation que le code **contredisait** jusqu'à ce lot, l'API acceptant `status` en écriture et permettant à un enregistrement de fiche de réécrire un constat sans qu'aucune de ces deux lignes n'apparaisse ([[Monitoring]], § *Un statut s'OBSERVE*). Elle est vraie depuis que ce chemin est fermé. Une réannonce à la reconnexion d'un proxy n'écrit une ligne que si le constat a **changé** : confirmer ce que la base porte déjà ne journalise rien. Les trois types d'instance sont d'avant le site : un enrôlement d'équipement ou la lecture d'une page n'ont pas encore de parc où se ranger |
| `discovery.` | **instance** | `discovery.scanned` · `discovery.probed` · `discovery.serial` | ce que la [[Discovery]] a relevé. D'instance **par construction** : un candidat n'a pas de site, le site est choisi à l'adoption |
| `group.` | site | `group.created` · `group.deleted` | groupes d'équipements, hérités ([[Profils et groupes]]) |
| `link.` | site | `link.created` · `link.deleted` | liaisons saisies entre équipements ([[Weathermap]]) |
| `mgmt.` | site | `mgmt.plan_changed` · `mgmt.migration_prepared` · `mgmt.migration_persisted` · `mgmt.migrated` | le plan de management et la bascule de VLAN d'administration ([[Adressage de management]]). Les trois étages de la bascule sont **trois types** parce qu'ils ne se réparent pas de la même façon : `prepared` n'est pas en flash (une coupure annule), `persisted` l'est, `migrated` a changé le chemin d'accès |
| `netscope.` | **instance** | `netscope.domain_created` | déclaration d'un domaine d'adressage — un objet d'instance ([[Domaines d'adressage]]) |
| `plan.` | site | `plan.updated` | enregistrement du plan de site |
| `port.` | site | `port.up` · `port.down` · `port.flapping` · `port.flap_cleared` · `port.configured` | les quatre événements de surveillance (section dédiée plus bas) plus la pose d'une configuration de port ([[Profils de switchs]]). Un port que meshcraft ne gère pas ne produit **rien** ici, volontairement |
| `profile.` | site | `profile.created` · `profile.updated` · `profile.deleted` | profils de port, d'équipement et de switch |
| `proxy.` | **instance**, sauf `proxy.coverage_changed` par la route « un seul site » | `proxy.enrolled` · `proxy.enroll_token_created` · `proxy.connected` · `proxy.disconnected` · `proxy.renamed` · `proxy.revoked` · `proxy.deleted` · `proxy.coverage_changed` · `proxy.migration_ordered` · `proxy.migration_cancelled` · `proxy.migrated` · `proxy.upgrade_offered` · `proxy.upgraded` · `proxy.upgrade_failed` · `proxy.upgrade_stalled` · `proxy.upgrade_policy` | le cycle de vie d'un boîtier ([[Proxy]], [[Versionnage et mise à jour du proxy]]). D'instance parce qu'un proxy n'appartient à aucun site, même quand il en couvre. **L'exception est signifiante** : ajouter ou retirer *un* site d'une couverture est une décision qui appartient à *ce* site et s'écrit chez lui ; remplacer la couverture entière est un acte d'instance |
| `site.` | site | `site.created` | création d'un site |
| `snmp.` | site | `snmp.applied` · `snmp.apply_failed` | pose de l'identité SNMP sur un équipement ([[Identité SNMP]]) |
| `tag.` | site | `tag.assigned` · `tag.removed` · `tag.rule_changed` · `tag.rules_applied` | étiquettes posées à la main et règles qui les posent ([[Étiquettes et portée des identifiants]]). Les deux derniers types séparent « la règle a changé » de « la règle a été passée sur le parc » — sans quoi on ne saurait pas si un parc est à jour de ses propres règles |
| `trap.` | site | `trap.dest_applied` · `trap.dest_failed` · `trap.path_confirmed` | la destination de traps écrite dans l'équipement, son échec, et la **preuve** que le chemin retour existe. Voir la requalification ci-dessous : ces trois types-là ne violent pas la règle « pas de traps dans ce journal » |
| `user.` | **instance** | `user.login` | audit des connexions. D'instance, et c'est *pourquoi* le journal d'instance est réservé au `global-admin` |
| `vlan.` | site | `vlan.created` · `vlan.updated` · `vlan.deleted` | VLAN d'un site ([[VLAN par défaut]]) |
| `watch.` | site | `watch.excluded` | un équipement a **perdu** sa surveillance, avec le motif. Émis uniquement à la perte — pas à chaque cycle — et c'est la seule piste qui mène l'exploitant à déclarer ses domaines d'adressage. Un équipement retiré de la surveillance en silence passerait pour une panne de meshcraft |

> [!note] Les sévérités ne sont pas une propriété du type
> Cinq types du catalogue s'écrivent avec **deux** sévérités selon le contexte, et
> c'est délibéré : `config.applied` (info en automatique, notice quand un humain a
> demandé), `snmp.applied`, `trap.dest_applied` (même raison), et
> `port.flap_cleared`, dont les deux valeurs portent deux nouvelles opposées (section
> dédiée plus bas). Le principe : **la sévérité dit combien ça vaut la peine d'être
> regardé, le type dit de quoi ça parle.** Un geste que quelqu'un a demandé sur un
> équipement de production mérite d'être vu ; le même geste posé mille fois par un
> provisionnement de fond ne doit pas noyer le journal.

### Annoncé par le vault, aucun producteur dans le code

Rien de ce qui suit n'a jamais été écrit dans le journal. Ces lignes restent parce
qu'elles portent une intention datée et argumentée ailleurs dans le vault — les
supprimer ferait perdre la décision, les laisser sans marque ferait chercher un
producteur qui n'existe pas.

| Type(s) | Annoncé par | État réel |
|---|---|---|
| `stp.topology_change`, `poe.fault`, `login.failed`, `auth.failure` | cette page (ligne « syslog ») | **aucune source.** Il n'y a pas de listener 514/udp : seul le terrain est posé (`network_mode: host` et `NET_BIND_SERVICE` dans le `docker-compose` que génère `internal/distrib`, commentés « syslog 514/udp, traps 162/udp »). `auth.failure` en particulier ne peut venir ni d'un syslog qu'on ne lit pas, ni d'un trap dont on ne décode pas le catalogue ([[Monitoring]] §2) |
| `device.rebooted` (sur `sysUpTime`), `port.speed_changed` | cette page | intentions de polling jamais écrites. Le redémarrage se **voit** dans le [[Monitoring unifié et pipelines]] sans produire de ligne de journal |
| `device.discovered`, `neighbor.new` | [[Discovery]], cette page | jamais écrits. Le relevé de découverte s'écrit sous `discovery.scanned` / `discovery.probed` / `discovery.serial` ; l'entrée au parc sous `device.created` puis `device.adopted`. Le voisinage LLDP ne produit **aucun** événement : `link.created` / `link.deleted` sont des liaisons **saisies**, pas découvertes |
| `config.rolled_back` | cette page | pas de retour arrière automatique. Un échec de pose s'écrit `config.apply_failed` et laisse l'équipement où il est ; c'est un choix, pas un manque |
| `svc.policy_changed`, `svc.generation_created`, `svc.installed`, `svc.install_failed`, `svc.retired`, `svc.compromised`, `svc.breakglass_exported` | [[Identité de service et rotation]] | le sous-système n'est pas construit. **À réconcilier avant de l'écrire** : la famille livrée qui recouvre déjà une partie de ce terrain est `credential.*` (`installed`, `rotated`, `apply_failed`…). Deux vocabulaires pour un même fait est exactement ce qui rend un journal illisible — la question à trancher est laquelle des deux familles absorbe l'autre, pas laquelle arrive en second |
| `mgmt.address_allocated`, `mgmt.address_released`, `mgmt.address_drift`, `mgmt.plan_violation`, `mgmt.exception_granted` | [[Adressage de management]] | aucun producteur. La famille `mgmt.*` livrée ne porte que le plan (`plan_changed`) et les trois étages de la bascule de VLAN d'administration. L'IPAM alloue en base sans écrire au journal |
| `proxy.upgrade_rolled_back` | [[Versionnage et mise à jour du proxy]] | aucun producteur. L'échec s'écrit `proxy.upgrade_failed`, l'enlisement `proxy.upgrade_stalled` |

> [!warning] Un type que cette page nommait mal, et non une intention
> `config.failed` n'a jamais existé sous ce nom : le type réel est
> **`config.apply_failed`**. Ce n'est pas un détail — une règle d'alerting ou une
> requête écrite sur `config.failed` ne remonte **rien**, silencieusement, et rien
> dans le produit ne le signale. C'est le défaut le plus coûteux qu'un catalogue de
> types puisse porter : pas une absence, une **faute de frappe institutionnalisée**.
> Le préfixe `config.` reste la façon sûre de tout voir.

### Présent en base, plus aucun producteur

Trois types se lisent encore dans le journal d'une installation en marche alors que
plus une ligne de code ne les écrit. Un journal est **append-only** — on ne réécrit
pas l'histoire pour la faire ressembler au présent :

| Type | Remplacé par | Où c'est expliqué |
|---|---|---|
| `port.link_up`, `port.link_down` | `port.up` / `port.down` | callout *Les vieilles lignes*, plus bas — avec les trois différences de comportement qu'un filtre à cheval fait apparaître |
| `proxy.sites_updated` | `proxy.coverage_changed` | le remplacement dit la bonne chose : ce qui change n'est pas une **liste de sites**, c'est une **couverture**, et la couverture est la frontière de droits du proxy ([[Proxy]]) |

## Les traps ne sont pas une source, et trois types `trap.*` n'y changent rien

> [!warning] Rien n'est jamais écrit *depuis le contenu* d'un trap
> Cette page a longtemps attribué `port.up` / `port.down` aux traps. C'est faux, et
> pas d'un détail de vocabulaire. Un trap v2c est un datagramme UDP non authentifié,
> falsifiable et perdable ; en faire une source d'état donnerait à quiconque se
> trouve sur le chemin le pouvoir d'**éteindre un port à l'écran** sans toucher au
> réseau. Ce qu'un trap fait, et rien d'autre : **avancer** le poll `ifOperStatus`
> qui aurait eu lieu de toute façon ([[Monitoring]] §2). La ligne de journal sort du
> poll, pas du datagramme — la source est donc le polling, et le trap un
> **accélérateur de latence**.
>
> C'est une propriété structurelle, et elle décide de ce qu'on lit ici : un canal de
> traps en panne — listener non démarré, 162/udp déjà pris, équipement qui n'a
> jamais reçu de destinataire — ne rend pas ce journal **faux**, il le rend **plus
> lent** (la cadence de polling au lieu d'~1 s). Il n'y a donc aucun arbitrage entre
> deux sources qui se contrediraient, et aucun mode dégradé à décrire : c'est le
> même chemin, moins vite.

### La règle, requalifiée : ce qui est exclu, c'est `trigger`

Cette page posait que « *trap* est du vocabulaire de protocole, pas une information
de parc ». La formule était commode et elle est devenue fausse : trois types
`trap.*` s'écrivent aujourd'hui dans le journal du parc. Ce n'est pas une entorse à
la règle, c'est la preuve que la règle visait mal — elle interdisait un **mot** là où
il fallait interdire une **catégorie d'information**.

La règle juste tient en une phrase : **le journal d'exploitation ne dit jamais par
quel mécanisme meshcraft a appris un état ; il dit ce qui est arrivé au parc.**

Le discriminant est un test, et il se pose sur chaque ligne :

> *Si le canal de traps n'existait pas du tout, cette ligne aurait-elle encore
> quelque chose à décrire ?*

- **`trigger`** (`"poll"` / `"trap"`) : non. La transition de port aurait été écrite
  de toute façon, à la cadence de polling, avec le même type, la même sévérité et la
  même phrase. Le champ ne décrit donc pas le parc, il décrit **meshcraft** —
  précisément ce que l'exploitant n'a pas à savoir et ce dont celui qui diagnostique
  meshcraft ne peut pas se passer. Il reste au journal **applicatif**, où il est la
  seule façon de constater qu'un parc reçoit réellement ses traps : sans lui, un parc
  dont aucun équipement n'émet serait indiscernable d'un parc réactif — les mêmes
  événements, trente secondes plus tard, et personne ne remarquerait jamais que
  162/udp ne sert à rien ([[Monitoring]] §*Observabilité du chemin rapide*) ;
- **`trap.dest_applied` / `trap.dest_failed`** : oui, mais dans l'autre sens — ces
  lignes n'existeraient pas, parce que le **geste** qu'elles décrivent n'aurait pas
  lieu. Elles ne parlent pas d'un mécanisme d'apprentissage : elles disent
  qu'**une configuration a été écrite dans un équipement de production**, ou qu'elle
  a échoué. C'est le même genre de fait que `snmp.applied` ou `port.configured`, et
  ça se journalise pour la même raison : quelqu'un l'a demandé, ou quelque chose l'a
  fait en son nom, et l'équipement n'est plus dans l'état où on l'avait laissé ;
- **`trap.path_confirmed`** : oui, et c'est le cas le plus intéressant. Cette ligne
  est le seul endroit d'où l'exploitant apprend que la **prétention** (« l'adresse est
  écrite ») est devenue une **preuve** (« un trap est arrivé »). Sans elle, l'état
  « adresse écrite, jamais confirmée » — celui, exactement, où l'on cherche un
  pare-feu ou une ACL sur le VLAN de management — serait invisible. Ce n'est pas du
  vocabulaire de protocole, c'est un **fait d'infrastructure** que rien d'autre ne
  rend.

Ce que la requalification préserve, et qui était le vrai contenu de l'ancienne
formule : **aucun de ces trois types n'est un état d'équipement dérivé du contenu
d'un trap.** `trap.path_confirmed` n'affirme rien de plus que « un datagramme
attribuable à cet équipement a atteint l'adresse que nous avons écrite ». Il ne dit
pas qu'un port est tombé, il ne dit pas qu'un équipement a redémarré — un trap ne
peut toujours pas faire ça.

Deux propriétés de ces lignes valent d'être connues, parce qu'elles se lisent mal
autrement :

- **`trap.path_confirmed` s'écrit au plus une fois par destination posée.** La borne
  est prise **en base**, dans le `where` de l'`update` (`store.ConfirmTrapPath`), et
  pas par une lecture suivie d'un test : le proxy réarme son compteur à chaque
  reconnexion de tunnel, donc compter sur la parcimonie de l'émetteur produirait une
  ligne « chemin retour établi » par redémarrage de proxy, **sur tout le parc à la
  fois** ([[Protocole contrôleur-proxy]] §`trap.seen`) ;
- **un trap arrivé à une autre adresse que celle écrite ne confirme rien**, et ne
  produit aucune ligne de parc : il va au journal applicatif en avertissement, avec
  ses **deux** causes possibles nommées — un destinataire que meshcraft n'a pas posé,
  ou une traduction d'adresse devant le proxy. Elles se réparent à l'opposé l'une de
  l'autre, et n'en nommer qu'une enverrait démonter la mauvaise moitié de
  l'installation.

> [!important] Les événements sans site ont besoin de leur propre vue
> Le cycle de vie d'un proxy ne se rattache à aucun site — même quand le proxy
> porte une couverture, l'enrôler ou le renommer est un acte d'instance
> ([[Proxy]]). Un candidat de [[Discovery]] n'a pas de site non plus, ni un
> `user.login` : ces événements ont `site_id` nul,
> donc **aucun journal de site ne peut les afficher**. Ils sont exposés par un
> journal d'instance distinct (`GET /api/events`), réservé au `global-admin`
> puisqu'il porte l'audit des connexions et les identifiants d'instance
> ([[RBAC]]). Un événement écrit mais affiché nulle part serait pire que pas
> d'événement du tout.

## Les quatre événements de surveillance des ports, et qui les calcule

Le lot de surveillance des ports est celui où la répartition proxy / contrôleur se
voit le mieux : **le proxy établit le fait, le contrôleur en fait la corrélation**.
Les deux moitiés écrivent dans ce journal, mais elles ne répondent pas à la même
question.

| Événement | Calculé par | Comment | Sévérité |
|---|---|---|---|
| `port.up` / `port.down` | le **proxy** | diff de l'`ifOperStatus` relevé contre sa dernière observation, à chaque poll, quel qu'en soit le déclencheur | **info** |
| `port.flapping` | le **contrôleur** | corrélation des transitions remontées par les proxys : 5 sur 5 min, dédoublonnées d'abord | **avertissement** |
| `port.flap_cleared` | le **contrôleur** | fin de battement, constatée à l'**absence** de transition pendant 3 min | notice **ou** avertissement, selon l'état final |

Deux conséquences que le tableau ne dit pas seul :

- **les événements de port marchent sans traps**, à la cadence de polling. Le trap
  ne les crée pas, il les avance ;
- l'anti-flap est au contrôleur **parce que** ses fenêtres sont une politique
  produit : les régler dans le proxy obligerait à redéployer un binaire dans chaque
  réseau client pour passer de cinq transitions à six ([[Monitoring]]).

### Les sévérités ne suivent pas l'intuition

| Événement | Sévérité | Pourquoi |
|---|---|---|
| `port.up` / `port.down` | **info** | sur deux mille prises, le poste débranché en partant le soir se compte par centaines chaque jour. Une couleur d'alerte posée sur l'ordinaire cesse d'être regardée, et le vrai incident devient la six-centième ligne de la journée |
| `port.flapping` | **avertissement** | cinq allers-retours en cinq minutes sur la même prise ne sont plus un geste humain : c'est du matériel — sertissage douteux, optique en fin de vie, duplex en désaccord, boucle. C'est exactement ce que les transitions unitaires enterraient |
| `port.flap_cleared`, stabilisé **en service** | notice | le lien est revenu et il tient. Ça se lit, ça ne se surveille pas |
| `port.flap_cleared`, stabilisé **hors service** | **avertissement** | le battement s'est arrêté faute de lien. Le port est muet, pas réparé : seul le bruit a cessé, la panne est intacte |

Ce qui donne son intérêt au lot est ce renversement : l'information n'est pas dans
la transition, elle est dans sa **répétition** — et dans la façon dont elle s'arrête.

### Pourquoi un seul type porte deux situations opposées

`port.flap_cleared` est le même type pour la bonne et la mauvaise nouvelle. Ce
n'est pas une économie de vocabulaire, c'est ce qui rend les deux lisibles.

Un battement est une **histoire** ouverte par `port.flapping` : la question de
l'exploitant est « est-ce que ça bat encore ? », et la réponse doit se lire au même
endroit quelle qu'elle soit. Avec deux types — un `port.repaired`, un
`port.dead` — tout lecteur (écran, requête, règle d'alerting) devrait connaître les
**deux** noms pour savoir qu'un battement est terminé ; celui qui n'en connaîtrait
qu'un verrait des battements sans fin, et c'est le pire défaut possible pour un
journal dont on attend la clôture d'un incident.

Les deux lectures utiles sortent alors des mêmes lignes, parce que le filtre du
journal combine un **préfixe de type** et un **plancher de sévérité**
([[Interface web]]) :

- filtrer sur `port.flap` rend le cycle complet — début, fin, et les deux issues.
  C'est la vue de qui suit un port ou instruit un incident ;
- filtrer « avertissements et plus » laisse passer le début du battement **et** la
  fin hors service, et écarte la fin en service. C'est la vue de qui trie : ne
  restent que les situations où il y a encore quelque chose à faire.

C'est donc la **sévérité** qui sépare le réparé du mort, pas le type. Ranger la
bonne nouvelle sous un type distinct l'aurait rendue invisible à qui suit un port,
et visible à personne d'autre.

> [!note] Les vieilles lignes ne portent pas ces types, et on ne les réécrit pas
> Les événements déjà enregistrés parlent `port.link_up` / `port.link_down` : c'est
> un **chemin de journalisation antérieur**, dont il ne reste plus une ligne de code
> — `grep` sur ces deux chaînes ne rend rien dans `internal/`. Il dérivait du même
> polling, mais le diff était fait au contrôleur à l'ingestion des échantillons
> de télémétrie, donc au rythme du tampon (vidé toutes les 10 s) et non sur le canal
> d'événements temps réel. Un journal est **append-only** : réécrire ses anciennes
> lignes pour les faire ressembler aux nouvelles falsifierait ce qui a été observé,
> et ferait mentir l'horodatage dont dépend tout le reste de cette page.
>
> Trois différences visibles, pour qui cherche pourquoi ses lignes anciennes ne se
> comportent pas comme les nouvelles :
>
> - **les sévérités ne coïncident pas** : l'ancien chemin gravait un lien tombé en
>   *avertissement* et un lien rétabli en *notice*, là où `port.down` et `port.up`
>   sont en *info*. Un filtre « avertissements et plus » sur une période à cheval
>   mélange donc des lignes anciennes parfaitement banales et des battements réels ;
> - **le périmètre n'était pas le même** : l'ancien chemin se taisait sur les ports
>   que meshcraft ne gère pas (mode « libre »), le nouveau journalise les
>   transitions de la façade observée ;
> - **le filtre par type est un préfixe** : `port.` remonte `port.link_down` comme
>   `port.down`, alors qu'une recherche sur `port.down` ne voit que l'après. C'est
>   la raison pour laquelle ces deux types **doivent** figurer au catalogue : ils
>   apparaissent sous le filtre le plus naturel qu'on puisse taper, et un catalogue
>   qui les tairait laisserait croire à une anomalie. Les lignes du nouveau chemin
>   portent en plus `actor = "surveillance"`, ce qui est le seul moyen sûr de les
>   distinguer.

## Schéma

```
events(
  id            bigint (identity),
  ts            timestamptz default now(),  -- horloge du CONTRÔLEUR, cf. ci-dessous
  site_id       uuid null,          -- null = événement d'INSTANCE (cf. ci-dessus)
  device_id     uuid null,
  port_name     text,               -- '' quand la façade n'a pas reconnu le port
  type          text,               -- taxonomie ci-dessus
  severity      smallint,           -- 0 debug … 5 critical
  message       text,               -- lisible, déjà localisable
  actor         text,               -- qui écrit : « surveillance », un utilisateur…
  payload       jsonb               -- diff, détail d'un état, contexte
)
```

Index : `events_site_ts_idx (site_id, ts desc)`, `events_device_ts_idx (device_id,
ts desc)`, `events_type_ts_idx (type, ts desc)`. Détails dans
[[Schéma base de données]].

Trois points où la table est plus simple que ce que cette page décrivait, et c'est
assumé :

- **pas de partitionnement.** Les partitions mensuelles avec purge par `DROP`
  restent la cible — c'est la seule façon de purger douze mois de journal sans un
  `DELETE` qui réécrit la table — mais elles arrivent avec le volume réel du
  monitoring, pas avant. Partitionner une table qui tient dans un index est une
  complexité de migration payée d'avance. La table créée en `0003_proxies_events`
  est une table simple, et son propre commentaire le dit ;
- **pas de `dedup_key` ni de `count`.** La corrélation vit **en mémoire** au
  contrôleur ([[Monitoring]]) : elle n'a donc rien à retrouver en base, et le
  nombre de transitions d'un battement est porté par la **phrase** de
  `port.flapping` (et par celle de `port.flap_cleared`, avec la durée). Le prix à
  connaître : ce compteur n'est pas interrogeable en SQL — on lit « 7 changements
  d'état », on ne fait pas la moyenne des battements du trimestre. Le jour où on
  la voudra, ce sera une colonne, pas un compteur incrémenté sur une ligne
  existante ;
- **`ts` n'est pas un paramètre.** `store.InsertEvent` ne l'écrit pas : la colonne
  prend son `default now()`, donc l'horloge de PostgreSQL. Ce n'est pas un oubli,
  c'est la même décision que pour `trap_dest_at` et `trap_seen_at` — voir la règle
  ci-dessous.

## Règles

- **Append-only** : on n'édite jamais un événement — ni pour ajuster un type, ni pour incrémenter un compteur. Le battement n'agrège donc pas *par mise à jour* : il agrège **par silence**, en écrivant une ligne `port.flapping` à la place des dizaines de transitions qu'on renonce à journaliser, puis une ligne de clôture. Persister l'état de corrélation coûterait une écriture par battement au moment précis où le parc va mal.
- **Une seule horloge, celle du contrôleur.** Cette page a longtemps promis un « horodatage source », les événements rejoués par un proxy s'insérant à leur date réelle. C'est **abandonné**, et le contraire est désormais garanti par la signature : `InsertEvent` n'accepte pas de date, comme `ConfirmTrapPath` n'en accepte plus. La raison a été mesurée sur matériel réel — un proxy déployé chez un client, sans NTP, était **onze secondes** en retard, ce qui suffisait à faire apparaître une preuve avant ce qu'elle prouve ([[Protocole contrôleur-proxy]], §*`Event.TS` est un fait de DIAGNOSTIC*). Un fil chronologique mélangeant deux horloges n'est pas plus honnête qu'un fil décalé : il est **inordonnable**. La date posée par un proxy garde exactement un usage — mesurer la dérive elle-même, en avertissement au journal applicatif, avec des seuils asymétriques (5 s de retard, 1 s d'avance : aucun transport ne fait remonter le temps).
- **Deux niveaux de lecture** : le journal (événements normalisés, taxonomie stable) et — *quand le syslog existera* — le brut, en rétention courte sur un écran de diagnostic séparé.
- **Rétention** : normalisés 12 mois par défaut (configurable par site) ; 7 jours pour le brut syslog, le jour où il y en aura.
- **Temps réel** : chaque événement inséré est publié sur le hub WebSocket (`site:{id}`) → toast/badge dans l'UI, mise à jour du [[Rendu des ports]] sans refresh. C'est `emit` lui-même qui notifie, et pas seulement les écritures passées par l'API : sans ça, tout ce qui journalise sans écrire — la surveillance, les provisionnements de fond — n'apparaîtrait qu'au prochain rafraîchissement manuel.
- **Le journal ne bloque jamais l'action** : une erreur d'insertion est tracée en `slog.Error` et l'action métier réussit quand même. Le journal est un observateur, pas un verrou — l'inverse ferait échouer une pose de configuration parce qu'on n'a pas su la raconter.
- **Audit inclus** : les actions humaines (login, apply approuvé, credential modifié) sont des événements comme les autres, `actor` = l'adresse de l'auteur, remplie par défaut depuis la session. Un seul endroit où chercher « qui a fait quoi quand ».

## Décidé / livré / ouvert

| Question | État |
|---|---|
| Catalogue rangé par famille de préfixe, portée et `actor` explicites | **livré** — 88 types, 19 familles, tous vérifiés par `grep` dans `internal/` |
| Séparation « émis par le code » / « intention du vault » | **livré**, et c'est la propriété à ne pas perdre au prochain lot |
| Filtre par préfixe de type + plancher de sévérité | **livré** — `store.EventFilter`, `type like '<préfixe>%'` |
| Journal d'instance séparé (`site_id` nul), réservé au `global-admin` | **livré** — `GET /api/events` |
| Horodatage unique, horloge du contrôleur | **livré** — aucun paramètre de date dans `InsertEvent` |
| Réconciliation `svc.*` / `credential.*` | **ouvert**, et bloquant pour [[Identité de service et rotation]] : deux familles pour un même fait |
| Événements de syslog (`stp.*`, `poe.*`, `auth.*`) | **ouvert** — subordonné à un listener 514/udp qui n'existe pas |
| Partitionnement mensuel, `DROP PARTITION` pour la purge | **décidé, non construit** — attend le volume réel |
| `count` / `dedup_key` en base pour les battements | **non prévu** : la corrélation est en mémoire, le compte est dans la phrase |
| Réécriture des types historiques (`port.link_*`, `proxy.sites_updated`) | **refusé, définitif** — un journal append-only ne se réécrit pas |
