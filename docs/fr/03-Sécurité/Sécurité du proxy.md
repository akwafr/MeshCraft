---
tags: [securite, proxy]
---

# Sécurité du proxy

Le [[Proxy]] est un pied dans le réseau du client : c'est l'actif le plus sensible du système après le coffre de [[Credentials]].

## Où en est cette page

| Question | État |
|---|---|
| Token d'enrollment : usage unique, TTL 1 h, couverture **figée à l'émission** | **livré** — consommation atomique (`UseEnrollToken`), `enrollTokenTTL`, `requestedCoverage` |
| Couverture de sites comme frontière de droits, proxy d'instance, refus du dernier site retiré | **livré** — voir la matrice de [[RBAC]] |
| Aucun chemin d'écriture SNMP, dans aucun sens | **livré, et garanti** — aucun `snmpset` dans le dépôt ; le proxy n'expose que `BulkWalk` |
| Questions ponctuelles du contrôleur (`ask`) : walk SNMP en lecture, adresse source | **livré** — § *Ce que le proxy accepte de faire* |
| Le contenu d'un trap n'est jamais source d'état | **livré** — le trap ne fait que **déclencher un poll** ([[Monitoring]]) |
| Le proxy ne peut rien **attribuer** à un site | **livré** — `host.seen` est ignoré, un candidat naît sans site |
| Durcissement systemd (non-root, capabilities, `ProtectSystem=strict`) | **livré** — unité **générée** par `internal/distrib/install.go` |
| Retour arrière automatique sur un binaire qui ne démarre pas | **livré** — `cmd/proxy/upgrade.go`, 2 tentatives puis repli sur celui de l'image |
| Authentification du proxy par **paire de clés ed25519** et challenge signé | **décidé, non construit** — c'est aujourd'hui un secret partagé (§ *Enrollment*) |
| Révocation qui fait **tomber la session en cours** | **livré** — `Hub.CloseSession`, appelée par la révocation, la **suppression** et la migration, plus un filet périodique (`revalidateTunnels`, toutes les minutes) |
| Allowlist de dial dans `proxy.yaml` | **partiellement livré** — variable d'environnement, et **ouverte par défaut** |
| Quotas d'ingestion par proxy, parsers fuzzés | **non construits** |
| Binaire **signé**, pinning de CA, statut local `127.0.0.1:9464` | **non construits** |
| Le contrôleur se méfie du proxy **jusqu'à sa couverture** | **livré** — `internal/api/proxyscope.go` : un fait n'est accepté que sur un équipement que le contrôleur a demandé à **ce** proxy de surveiller |

## Enrollment

1. Un `site_admin` ou un `global-admin` génère dans l'UI un **token d'enrollment** : usage unique, TTL court (1 h), **porteur de la couverture de sites** du futur proxy ([[Proxy]]). Un site-admin ne peut y mettre que les sites qu'il administre — c'est là que se joue le cloisonnement : il ne peut pas fabriquer un proxy qui verrait le réseau d'un autre site. Seul un `global-admin` émet un token de proxy d'instance ([[RBAC]]). La couverture est **figée à l'émission**, jamais relue à l'enrollment : les droits de l'émetteur peuvent changer entre les deux. **Livré**, y compris la consommation atomique — l'invalidation du token et la lecture de sa couverture sont un seul `UPDATE … RETURNING`, ce qui interdit qu'un token soit utilisé deux fois par deux enrollments simultanés.
2. `meshcraft-proxy enroll --controller wss://ctrl.example --token XXX`. Ce que le proxy fait ensuite est **plus faible que la conception** : il poste `{token, name}`, et le contrôleur lui **renvoie un secret** de 32 octets dont il ne garde que le SHA-256.
3. Le contrôleur enregistre le proxy (`proxy_id`, empreinte du secret, couverture) et invalide le token. **Livré.**
4. Connexions suivantes : le proxy **rejoue ce secret** à chaque appel — dans le corps JSON du heartbeat et des métriques, dans les en-têtes `X-Meshcraft-Proxy-Id` / `X-Meshcraft-Proxy-Secret` avant l'upgrade du tunnel. Pas de mTLS exigé (les middlebox TLS d'entreprise le cassent) — l'authentification est bien applicative, dans le WSS, comme décidé.

> [!warning] La paire de clés ed25519 est décidée, non construite — aujourd'hui c'est un porteur
> Il n'y a **aucun ed25519 dans le code du proxy ni du contrôleur**, et aucun
> challenge. L'authentification est un **secret partagé**, comparable à un jeton
> porteur, avec quatre conséquences qu'un exploitant doit connaître :
>
> - il est **rejouable** : quiconque le lit peut se faire passer pour ce proxy.
>   Une clé privée qui signe un challenge ne se rejoue pas, et c'est tout l'intérêt
>   du schéma décidé ;
> - il est **écrit en clair** dans `/var/lib/meshcraft/state.json` (fichier `0600`,
>   répertoire `0750`). Lire ce fichier suffit ; avec une clé privée, il faudrait
>   encore la faire signer ;
> - il n'a **ni expiration ni rotation** : la table `proxies` n'a pas de colonne
>   d'échéance et aucun code ne le renouvelle ;
> - il transite **entièrement** à chaque requête, donc quiconque termine le TLS le
>   voit — alors qu'une signature ne révèle jamais le secret qui l'a produite.
>
> La conception reste la bonne cible, et elle est peu coûteuse à atteindre :
> `enroll` porte déjà une clé publique au lieu d'un nom, et le préambule du tunnel
> porte une signature au lieu d'un secret. La garder écrite ici est ce qui empêche
> de considérer le porteur comme l'état final.

Révocation depuis l'UI (proxy volé/compromis) : `POST /api/proxies/{id}/revoke`, réversible. **Livré**, et elle **coupe la session en cours** — voici ce que le geste fait exactement.

`revoked` reste vérifié à l'**authentification** : le heartbeat, la remontée de métriques et l'**ouverture** d'un tunnel passent à 401. Mais `handleRevokeProxy` appelle en plus `Hub.CloseSession` (`internal/tunnel/hub.go`), qui **retire la session de la table du `Hub` puis ferme le multiplexage yamux** : `Dial`, `Ask` et `PushWatch` ne trouvent plus de session à qui parler. C'est la position réseau qui est coupée, pas seulement la porte d'entrée. C'est le scénario pour lequel le bouton existe — sur un proxy volé, l'attaquant contrôle le processus, il ne va pas rouvrir sa connexion, il va la **garder**.

Le motif de la coupure part à **deux** journaux : `tunnel coupé` côté application (avec la durée qu'avait la session) et, dans le [[Journal d'événements]], le message de `proxy.revoked` lui-même, qui gagne « — tunnel en cours coupé » **quand la coupure a réellement eu lieu** (`CloseSession` rend faux s'il n'y avait rien à couper). Sans cette mention, rien ne distinguerait au journal un geste qui a atteint la session vivante d'un geste qui n'a touché que la ligne en base. Et le proxy coupé, lui, se reconnecte aussitôt — son heartbeat continue de tourner, c'est voulu — donc son propre journal montrera « tunnel interrompu » : sans la ligne côté contrôleur, l'exploitant partirait chercher une panne de WAN pour une coupure qu'il a demandée.

> [!warning] L'ordre est la sécurité : la base d'abord, la session ensuite
> Couper avant d'avoir écrit la révocation n'offrirait pas une révocation mais une
> **déconnexion** : le proxy se reconnecte dans la seconde (son backoff repart de
> zéro après une session qui a tenu) et l'authentification l'accepterait encore, sur
> une base qui ne sait pas encore qu'il est révoqué. Le résultat serait une coupure
> spectaculaire et sans effet — le pire des deux mondes, puisque l'exploitant aurait
> vu quelque chose se produire.
>
> Cet ordre est **verrouillé par un test**, et pas par une convention : il compare
> les **positions dans le source** de l'écriture en base et de l'appel à
> `CloseSession`, dans les trois handlers concernés, en parcourant l'AST du paquet.
> Un test fonctionnel n'y arriverait pas — les deux ordres donnent le même résultat
> observable tant que le proxy ne se reconnecte pas assez vite —, et c'est précisément
> pourquoi le verrou est syntaxique. Il échoue aussi si l'un des trois handlers
> **cesse** d'appeler `CloseSession`.
>
> Réactiver ne coupe rien, évidemment : c'est le geste inverse.

La coupure au clic ne suffit pourtant pas, et le manque n'est pas théorique : une révocation faite **directement en SQL**, prononcée par une **autre instance**, ou survenue pendant que ce contrôleur **redémarrait** laisserait un proxy révoqué connecté indéfiniment. Or la révocation est justement le geste qu'on fait quand on est pressé et qu'on ne veut dépendre d'aucune interface. D'où `revalidateTunnels` (`internal/api/proxy_handlers.go`), démarré par `Router()` pour la vie du processus, qui redemande à la base si les tunnels établis ont encore le droit d'exister.

> [!note] La cadence du filet, et pourquoi il ne coupe rien quand il ne sait pas
> `tunnelRevalidate` vaut **une minute**, et l'arbitrage se lit dans les deux sens.
> La borne haute est une **fenêtre d'exposition** : le temps pendant lequel un proxy
> révoqué hors de cette API garde ses connexions et la surveillance déjà reçue. La
> borne basse est le **coût** : une lecture par clé primaire et par proxy connecté,
> quand le heartbeat de ces mêmes proxys touche déjà la même ligne trois fois toutes
> les trente secondes. Descendre plus bas achèterait quelques secondes au prix d'une
> boucle qui n'apprend rien la quasi-totalité du temps. Un passage entier est borné
> par `tunnelRevalidateBudget` (30 s) : il serait rejoué au tic suivant de toute
> façon, et une base qui ne répond pas en trente secondes ne dira rien d'utile en
> cinq minutes.
>
> **Une erreur de lecture ne coupe rien** — « je ne sais pas » n'est pas « il est
> révoqué ». Ce n'est pas une tolérance ajoutée après coup : `tunnelKeep`, le verdict
> « garder le tunnel », est la **valeur zéro** du type, délibérément, pour que tout
> trou de code futur se lise ainsi. Le défaut inverse déconnecterait le parc entier à
> la première seconde d'indisponibilité de PostgreSQL — un remède bien pire que le
> mal. Le refus est journalisé, le proxy laissé tranquille.
>
> Le filet attrape aussi le proxy **supprimé** (`tunnelGone` : plus de ligne en
> base). Ce qu'il coupe, il l'annonce dans le [[Journal d'événements]] avec un
> événement d'instance à lui, `proxy.tunnel_cut` (sévérité **avertissement**, acteur
> `meshcraft`), qui dit explicitement « constaté par la revalidation périodique, hors
> action d'un utilisateur » : personne n'a cliqué, et c'est justement l'information.
> Sans elle, l'exploitant verrait un proxy tomber sans cause visible.

Deux autres gestes coupent, pour la même raison qu'ils ferment un accès :

- la **suppression** (`handleDeleteProxy`) — au moins aussi forte que la révocation : la ligne disparue, plus personne ne peut ni voir ni révoquer ce proxy, alors que sa session, elle, servirait toujours. La laisser ouverte fabriquait un pied dans le réseau **hors de tout inventaire** ;
- la **migration** (`handleProxyMigrated`) — un proxy passé chez le voisin n'a plus lieu de recevoir la surveillance de *cette* instance, ni d'y ouvrir des connexions. La séquence de migration n'en souffre pas : quand le proxy poste ici, il s'est déjà enrôlé sur la cible et a persisté son nouvel état ; cet appel passe par HTTP et non par le tunnel, et le proxy traite même son échec comme sans conséquence.

> [!note] Ce que « révoquer » valait avant ce lot
> Un refus à la **prochaine** authentification, et rien de plus. La session déjà
> établie continuait d'ouvrir des connexions vers les équipements (`Dial`), d'écrire
> des événements en base (`AcceptEvents`), de répondre aux questions (`Ask`) et de
> recevoir la configuration de surveillance (`PushWatch`) — donc les **communautés
> SNMP du parc en clair**. Le `Hub` ne connaissait pas la révocation et ne revalidait
> jamais : un tunnel établi restait pleinement exploitable jusqu'à ce qu'il se rompe
> de lui-même. Révoquer dans l'UI donnait le sentiment d'avoir coupé le pied dans le
> réseau alors qu'on n'avait fermé que la porte d'entrée. C'est écrit ici parce que
> tout déploiement antérieur à ce lot se comporte encore ainsi.

Deux corollaires, plus mineurs et **inchangés** par ce lot : « la clé est blacklistée » ne veut rien dire tant qu'il n'y a pas de clé (le `secret_hash` reste intact, et dé-révoquer restitue le **même** credential) ; et les **plages de scan** ne deviennent pas orphelines — elles restent attachées au proxy révoqué. Elles ne sont effacées qu'à la **suppression** du proxy, en cascade. Un proxy révoqué reste par ailleurs délibérément candidat aux chemins d'accès (`ProxiesCoveringSite` ne le filtre pas), pour que l'inventaire ne perde pas la mémoire de qui joignait quoi.

Les équipements, eux, ne sont pas rattachés à ce proxy ([[Proxy]]) : ils restent pilotables par tout autre proxy qui les joint, et ceux dont il était le collecteur de télémétrie voient leur cible trap/syslog recalculée ([[Monitoring]]) — le recalcul, lui, reste **décidé, non construit** (voir *L'élection* dans [[Monitoring]]). À la suppression du proxy, `devices.proxy_id`, `discovered.proxy_id` et `devices.trap_dest_proxy_id` repassent bien à `null`.

## Ce que le proxy accepte de faire

Le contrôleur ne pousse pas seulement une configuration de surveillance : il pose aussi au proxy des **questions ponctuelles** — une question, une réponse, fermeture. Il y en a exactement **deux**, et la brièveté de cette liste est le moindre privilège lui-même :

| Question | Ce qu'elle rend | Exposition |
|---|---|---|
| `snmp_walk` | un walk SNMP **en lecture seule** (`BulkWalk`), OID validé numérique, bornes dures de 500/5000 lignes et 15/60 s | `POST /api/devices/{id}/snmp-walk`, **global-admin** seulement |
| `source_addr` | « par quelle adresse locale joins-tu cette IP ? » — un `connect` UDP sans émission, dont on lit l'adresse locale | **aucune** route HTTP : usage interne, pour poser la destination de traps ([[Monitoring]]) |

Trois choses valent d'être dites parce qu'elles sont des décisions, pas des accidents :

- **il n'y aura pas d'écriture.** L'interface SNMP du proxy ne comporte que `BulkWalk` et `Close`, et il n'y a aucun `snmpset` dans le dépôt. Ajouter un verbe d'écriture ferait du proxy un point unique depuis lequel reconfigurer le parc ; la configuration passe par les connecteurs, à travers le relai TCP, avec l'authentification de l'équipement ([[Connecteurs]]).
- **`source_addr` n'est pas exposé en HTTP**, et c'est volontaire : c'est une primitive de diagnostic interne. L'ouvrir donnerait à qui a un compte un moyen de cartographier les interfaces du réseau du client, une IP à la fois.
- **l'adresse se demande, elle ne se déduit pas.** Le proxy déclare ses interfaces, mais laquelle sert à joindre *cet* équipement dépend de la table de routage. Deviner aurait posé dans les équipements une destination de traps vers une adresse par laquelle ils ne répondent pas — une panne muette.

Les quatre verbes de flux sont, symétriquement, aussi peu nombreux : `dial` (relai TCP), `watch` (le contrôleur pousse ce qu'il faut surveiller), `ask` (ci-dessus), et `events` — le seul qui remonte. Une question inconnue est **refusée avec son nom**, jamais absorbée en silence.

## Moindre privilège, dans les deux sens

**Le proxy ne détient rien** — presque. Ni configuration, ni donnée métier, ni credential SSH ou HTTP : le contrôleur parle ces protocoles lui-même à travers le relai, et le proxy ne fait que recopier des octets. À nuancer sur deux points, parce que « rien » est trop simple :

- il reçoit des **communautés SNMPv2c en lecture**, forcément — sans elles il ne peut pas interroger. Elles viennent du coffre, restent **en mémoire seulement**, ne sont jamais écrites sur disque, et une règle explicite du code interdit de les journaliser (respectée : la trace d'un walk ne porte que l'IP, l'OID et un nombre de lignes) ;
- il persiste **son propre** secret d'identité en clair dans son fichier d'état (voir le callout d'enrollment) ;
- et un protocole relayé **non chiffré** (HTTP, telnet) traverse en clair le processus du proxy. Ce n'est pas un secret *stocké*, c'est un secret *vu au vol* — la distinction compte pour l'analyse de risque, pas pour l'exploitant.

Sa compromission donne donc une position réseau (grave), l'identité de ce proxy, et les communautés de lecture des équipements qu'il surveille — mais aucun accès administrateur au parc (voir [[Credentials]]).

**Le contrôleur se méfie du proxy** : tout ce qui remonte (résultats de scan, syslog, traps, résultats RPC) est traité comme entrée non fiable. Ce qui est livré de cette méfiance :

- **un fait ne vaut que sur un équipement confié à ce proxy** — le contrôle de portée ci-dessous, et la pièce qui manquait le plus longtemps ;
- **le contenu d'un trap n'est jamais source d'état.** Un trap ne fait que *déclencher* un poll, dont c'est le résultat qui compte ([[Monitoring]]). C'est le durcissement le plus important du lot, parce qu'un trap est un datagramme UDP non authentifié qu'un tiers du réseau peut forger ;
- **identité par IP source** : un proxy ne traite un trap que si la source figure dans **sa** liste de surveillance, et la communauté doit correspondre. Un trap d'une source non surveillée est jeté et compté ;
- **anti-amplification** : coalescence de 250 ms et plancher de 2 s entre deux polls déclenchés, donc un port qui bat en boucle ne produit pas un walk par battement ;
- **anti-battement** au journal (`portevents.go`) : un port instable produit **un** `port.flapping`, pas cinquante lignes ;
- **bornes de charge** sur les métriques : 20 000 échantillons et 8 MiB par lot, refus explicite en 413 — et un test verrouille la coïncidence des deux bornes, parce qu'un lot accepté par l'une et refusé par l'autre provoquait un rejeu infini ;
- `host.seen` est **ignoré** : un proxy qui voit un hôte ne crée rien.

Ce qui **n'est pas** construit, et qu'il ne faut pas croire acquis : les **quotas d'ingestion par proxy** (aucun rate-limit nulle part ; un proxy fou peut inonder le [[Journal d'événements]] par des `device.up`/`down`, l'anti-battement ne couvrant que les ports — le contrôle de portée ci-dessous borne désormais la **cible** de cette inondation aux équipements qui lui sont confiés, pas son **débit**), et les **parsers fuzzés** (aucun `func Fuzz` dans le dépôt).

**Le contrôle de portée — de quel équipement un proxy a le DROIT de parler** était l'écart le plus grave de cette page ; il est **livré** (`internal/api/proxyscope.go`, consulté depuis `handleProxyEvent` et `applyPortTelemetry`). L'invariant tient en une phrase :

> [!important] Le contrôleur n'accepte un fait sur un équipement que s'il a demandé à **ce** proxy de le surveiller
> Le `DeviceID` d'un événement de tunnel — comme l'étiquette `device` d'un
> échantillon de métrique — est choisi par le **proxy**. Il est désormais confronté à
> l'ensemble que la **dernière poussée de surveillance** a confié à ce proxy
> (`PushWatchTo`, retenu en mémoire par `recordProxyScope`), **avant** que le fait
> n'écrive quoi que ce soit — statut d'équipement, transition de port au
> [[Journal d'événements]], état de lien dans l'inventaire (`devices.ports`).
> Refusé, le fait n'est pas absorbé en silence : il part au journal en
> **avertissement**, avec le proxy, l'équipement, l'autorité manquante et l'âge de la
> consigne — mais **amorti à une ligne par proxy et par minute**, parce qu'un proxy
> en boucle ne doit pas pouvoir noyer la seule chose qui permettrait de le
> diagnostiquer.

Ce qui était ouvert avant ce lot, et qui vaut d'être su parce que tout déploiement antérieur se comporte ainsi : un proxy authentifié du site A pouvait marquer injoignables les équipements du site B, y fabriquer des transitions de port et y écrire de la télémétrie. Il ne pouvait pas les configurer ni lire leurs secrets — la barrière tenait là où elle porte le plus —, mais il pouvait **mentir sur tout le parc** alors que toute cette page dit qu'il ne peut mentir que sur ce qu'il voit. Et la couverture n'est pas une frontière décorative : c'est la même que [[RBAC]] défend site par site, et **la seule** raison pour laquelle un site-admin ne peut pas fabriquer un proxy qui verrait ailleurs. La laisser ouverte en écriture rendait le cloisonnement partiel — on empêchait de *voir* le réseau d'un autre site, pas d'y *écrire* de la fausse supervision.

Deux traits de la forme retenue comptent autant que le contrôle lui-même :

- **la même source de vérité que la consigne poussée.** La règle n'est pas un contrôle de couverture refait à la main : c'est la consultation de ce qui est **réellement parti** dans le tunnel. Un second calcul de couverture, écrit ailleurs, finirait tôt ou tard par dire autre chose que le premier — et une divergence entre les deux se manifesterait par des faits légitimes refusés, c'est-à-dire par une panne de supervision qu'on n'attribuerait pas à une règle de sécurité ;
- **une consultation en mémoire, donc gratuite.** Ce chemin voit passer **toutes** les transitions de port du parc, l'événement le plus fréquent du produit. Une requête de base par transition aurait mis une charge SQL là où il n'en faut pas, et la première réaction à cette charge aurait été de vouloir la contourner.

> [!warning] Deux granularités, et les confondre serait un faux positif ou un trou
> La poussée de surveillance produit **deux** ensembles, et l'autorité exigée dépend
> de ce dont le fait dérive ([[Proxy]], § *L'arbitrage*) :
>
> - les cibles **ICMP** sont par **couverture de sites** — plusieurs proxys peuvent
>   légitimement pinguer le même équipement. C'est cet ensemble qui autorise
>   `device.up` / `device.down` ;
> - les cibles **SNMP** sont restreintes au **collecteur déclaré**. C'est cet
>   ensemble qui autorise les transitions de port, `trap.seen` et la télémétrie de
>   ports, puisque les trois dérivent du polling.
>
> Conséquence contre-intuitive et voulue : un proxy qui remonterait une transition de
> port pour un équipement qu'il **pingue mais n'interroge pas** est en faute, même si
> sa couverture est irréprochable — il n'a aucun moyen d'en connaître les ports. Le
> refus a son propre verdict (« pingué mais pas collecté »), distinct de « jamais
> confié » : le premier se répare en corrigeant un rattachement ou en attendant qu'un
> changement se propage, le second veut dire qu'un proxy parle d'un site qu'il ne
> couvre pas. Les aplatir en un booléen rendrait le journal inexploitable au moment
> précis où il faut décider s'il s'agit d'un défaut ou d'une attaque.
>
> N'exiger que la couverture aurait laissé passer l'attaque la plus payante — écrire
> dans l'inventaire des ports et au journal du parc d'un équipement dont on n'est pas
> le collecteur ; n'exiger que la collecte aurait coupé la joignabilité de tout le
> parc rattaché ailleurs.

> [!note] L'ensemble retenu n'est PAS purgé à la déconnexion du proxy, et c'est un choix
> Le purger ferait perdre, à chaque reconnexion, exactement les transitions
> accumulées **pendant la coupure** — les seules que personne n'a vues, donc les plus
> précieuses. Le proxy, lui, garde sa configuration et sa file d'événements en travers
> de la rupture du tunnel, et il vide cette file dès la session rétablie ; la mémoire
> du contrôleur doit refléter **ce que le proxy tient**, et il tient encore l'ancienne
> consigne. Le premier paquet d'une reconnexion est donc jugé contre la consigne même
> sur laquelle le proxy a travaillé, et la poussée qui suit la remplace en entier.
>
> Ça n'ouvre rien : une entrée retenue ne sert qu'à juger les faits d'un proxy qui
> **parle**, donc d'un proxy connecté et authentifié. Une entrée orpheline est inerte,
> et elle disparaît à la connexion suivante de n'importe lequel de ses voisins.

Reste **une fenêtre**, écrite ici plutôt que masquée : au démarrage du **contrôleur**, la mémoire est vide et le **premier paquet de chaque proxy est refusé** le temps du premier calcul de surveillance. C'est le seul cas où l'on n'a réellement rien confié — refuser est alors la seule réponse juste — et c'est **auto-réparateur** : la poussée a lieu à la connexion, donc une fois par proxy et par démarrage. Accepter « le temps que ça se mette en place » ne serait pas une correction de sécurité. Le refus porte son propre verdict (« aucune consigne poussée »), ce qui le distingue d'un vrai débordement : s'il **persiste**, ce n'est plus une course de démarrage, c'est le calcul de surveillance qui échoue.

Puisqu'un proxy n'a pas de site et que la découverte peut venir de n'importe lequel ([[Discovery]]), le garde-fou n'est pas « un proxy = un site » mais : **un proxy ne peut rien attribuer**. **Livré** : ce qu'il remonte n'est qu'un *candidat sans site* — la table le porte par construction, le site n'apparaît qu'à l'adoption — et le rattachement est un geste humain soumis au [[RBAC]] sur le site cible. Un proxy compromis peut donc mentir sur ce qu'il voit ; il ne peut pas faire entrer un équipement dans un site, ni se faire confier les credentials d'un site : ceux-ci ne sont essayés qu'après l'adoption, sur décision d'un utilisateur habilité.

**Le client se méfie du contrôleur** : le contrôleur peut ouvrir des TCP arbitraires via `dial` — c'est sa fonction. Limitation : une **allowlist de CIDR** vers lesquels le proxy accepte de dialer. **Partiellement livré**, et les écarts changent la posture :

- elle se configure par la variable d'environnement `MESHCRAFT_DIAL_ALLOW`, **pas** par un `proxy.yaml` — ce fichier n'existe nulle part dans le code. Le proxy ne lit en tout que quatre variables. L'intention « hors de portée du contrôleur » est tenue : c'est bien une configuration locale, posée à l'installation ;
- elle est **vide par défaut, donc totalement ouverte**. Le journal du proxy l'annonce (« toutes destinations »), mais un déploiement fait au bouton n'en pose aucune. Le défaut sûr aurait été l'inverse ; l'inverse aurait rendu le produit inutilisable à l'installation, et c'est l'arbitrage qui a été fait. Il vaut d'être su plutôt que supposé ;
- elle n'est lue **qu'une fois au démarrage** : la resserrer demande un redémarrage du proxy ;
- elle ne couvre **que `dial`**. Le walk SNMP, `source_addr`, le polling, les pings de surveillance et le balayage de plages ne la consultent pas — le code l'assume explicitement. La raison est cohérente : ces chemins-là ne portent pas de flux arbitraire, ils exécutent une opération bornée décidée par le contrôleur. Mais « le proxy n'atteint que les CIDR autorisés » serait faux.
- un point où elle est plus solide que prévu : un nom de domaine doit résoudre **entièrement** dans le périmètre autorisé, sinon il est refusé. Autrement, un DNS complice suffisait à la contourner. Et une allowlist **illisible** replie sur `127.0.0.255/32` — c'est-à-dire rien : une erreur de syntaxe ne doit pas ouvrir le réseau.

Documenté honnêtement : installer le proxy = faire confiance à l'opérateur du contrôleur.

## Durcissement runtime

- Process non-root et capabilities Linux ciblées : **livré pour le déploiement systemd**. L'unité n'est pas un fichier du dépôt, elle est **générée** (`internal/distrib/install.go`) et servie par `GET …/install-script` — un fichier statique aurait divergé du script qui l'installe. Elle porte `User=meshcraft`, `AmbientCapabilities=CAP_NET_RAW CAP_NET_BIND_SERVICE` (l'ARP scan et les ports 514/162) avec le `CapabilityBoundingSet` correspondant, `NoNewPrivileges`, `ProtectSystem=strict`, `StateDirectoryMode=0750`, plus `ProtectHome`, `PrivateTmp`, `ProtectKernelTunables`/`Modules`/`ControlGroups`, `RestrictNamespaces`, `RestrictSUIDSGID`, `LockPersonality`, `RestrictAddressFamilies` limité à INET/INET6/NETLINK/PACKET et `SystemCallFilter=@system-service`. Le script crée un compte système sans shell, vérifie le SHA-256 **avant** d'installer, et pose le token dans `/etc/meshcraft/proxy.env` en `0600` — **jamais** dans l'unité, qui est lisible de tous.

> [!warning] Le déploiement Docker généré, lui, tourne en root
> Le `docker-compose.yml` produit par le bouton demande `network_mode: host` et
> `cap_add: NET_RAW, NET_BIND_SERVICE` — nécessaires — mais **ne porte pas de
> `user:`**, et l'image construite par `cmd/mkoci` ne déclare aucun `User`. Le
> chemin recommandé ([[Déploiement Docker et IPv6]]) est donc le **moins** durci
> des deux, ce qui est l'inverse de ce qu'on veut : c'est celui que tout le monde
> prend. Le contrôleur, lui, tourne bien en `USER meshcraft`.
>
> Avec `network_mode: host`, les capabilities suffisent : root dans le conteneur
> n'apporte rien de plus que `NET_RAW` sur l'hôte, et un `user:` dédié plus
> `cap_add` donne le même résultat fonctionnel. Il n'y a donc pas d'arbitrage à
> faire, seulement une ligne à écrire.

- Auto-update (opt-in) : **livré, sauf la signature**. Le binaire tiré du contrôleur est vérifié par **SHA-256** en flux au téléchargement *et* relu au démarrage avant tout `exec` ; l'URL doit être sur le même hôte et le même schéma que le contrôleur enregistré ; l'autorisation d'exécuter vient de l'**enregistrement dans `state.json`**, pas de la présence du fichier — un binaire déposé dans le volume par un tiers n'a ni entrée ni SHA attendu ; et un binaire qui ne démarre pas provoque un **retour arrière automatique** sur celui de l'image après deux tentatives, avec un garde-fou anti-boucle. Côté contrôleur, le déploiement est progressif (un proxy en vol), une offre sans réponse passe `stalled` après 15 min, et un échec **suspend** les offres jusqu'à un geste humain — voir [[Versionnage et mise à jour du proxy]].

> [!note] « Binaire signé » n'est pas encore vrai, et l'ancre de confiance manque
> Il n'y a **aucune vérification de signature** : seulement le SHA-256. Or
> l'empreinte attendue arrive par le **même canal** que le binaire (l'offre du
> heartbeat), et `GET /downloads/proxy/{name}` est **anonyme**. Le SHA protège
> donc contre une corruption de transfert et contre un fichier déposé
> localement — ce qui est déjà l'essentiel du risque quotidien — mais **pas**
> contre un contrôleur compromis, qui signerait son propre binaire de son propre
> hash.
>
> Une signature n'a de valeur que si sa clé publique arrive autrement que par le
> canal qu'elle protège : embarquée dans le binaire installé initialement, ou posée
> à l'installation. C'est ce que le mécanisme devra prévoir, faute de quoi il
> ajoutera une cérémonie sans ajouter de garantie.

- TLS : vérification **stricte** du certificat du contrôleur — **livré**, par le comportement par défaut de Go, sans aucun `InsecureSkipVerify` dans le chemin proxy (ceux du dépôt visent les **équipements**, dont les certificats sont auto-signés). Le **pinning de CA** reste **décidé, non construit**, faute de fichier de configuration où le mettre. À savoir aussi : le clair est accepté et normalisé (`ws://` → `http://`), et un compose généré depuis un contrôleur atteint en HTTP embarquera un `MESHCRAFT_CONTROLLER` en HTTP — le tunnel n'est alors pas chiffré, et le secret partagé y circule.
- Journalisation locale minimale (pas de payloads) : **livré**. Le statut exposé en local sur `127.0.0.1:9464` (healthz + métriques Prometheus du proxy lui-même) est en revanche **décidé, non construit** : il n'y a **aucun serveur HTTP dans le proxy**, ses seules sockets d'écoute sont l'UDP des traps. Ce qui existe à la place : des statistiques de traps en journal toutes les 5 minutes, et le `/api/healthz` du **contrôleur**. Diagnostiquer un proxy muet passe donc aujourd'hui par ses logs, pas par une sonde — c'est précisément ce que le point voulait éviter.
