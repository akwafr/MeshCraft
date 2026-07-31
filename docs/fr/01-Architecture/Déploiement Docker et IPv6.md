---
tags: [architecture, livraison, reseau]
---

# Déploiement Docker et IPv6

Deux `docker-compose` existent, et il ne faut pas les confondre :

| Fichier | Qui l'écrit | Contenu |
|---|---|---|
| `docker-compose.yml` (racine du dépôt) | nous, versionné | l'instance : **quatre** services — contrôleur, PostgreSQL (`db`), VictoriaMetrics (`metrics`) et Caddy (terminaison TLS) |
| le compose du proxy | **généré par le contrôleur** (`internal/distrib/compose.go`), jamais versionné | un service, l'image tirée du registre intégré, le token d'enrollment pré-rempli |

Le service `metrics` n'est pas un détail d'inventaire : les séries temporelles sont
**séparées de PostgreSQL délibérément**, parce que des compteurs d'interface
relevés toutes les cinq secondes sur un parc entier représentent une charge
d'écriture que Postgres accepte mais qu'il fait payer sur **tout le reste** —
inventaire et journal compris ([[Monitoring unifié et pipelines]] §3). Comme la
base, il n'écoute que sur le réseau du compose : les proxys écrivent **à travers**
le contrôleur, seul service à parler aux deux. Son absence n'empêche pas meshcraft
de démarrer — `MESHCRAFT_METRICS_URL` vide signifie « pas de stockage de
métriques », et le contrôleur le **dit** plutôt que d'échouer pour un service dont
l'inventaire n'a pas besoin.

## 1. Le compose du proxy porte une couverture, pas un site de rattachement

La génération du déploiement ne demande pas *où brancher le proxy* mais **pour quels sites il sera utilisable** — sa couverture ([[Proxy]]). Les endpoints sont des ressources d'instance (`POST /api/proxies/compose`, `POST /api/proxies/install-script`), ouverts au `site_admin` avec une couverture **bornée à ses sites**, et au `global-admin` qui peut en faire un proxy d'instance ([[RBAC]]). Chaque appel émet **son propre** token, puisqu'un token est à usage unique.

Par défaut, un site-admin obtient un proxy couvrant tous les sites qu'il administre, et un global-admin un proxy d'instance : dans les deux cas, le cas courant se règle sans rien cocher. Le geste reste : générer, coller sur l'hôte, `docker compose up -d`.

Ce qui est **réellement généré** aujourd'hui, à la virgule près :

```yaml
services:
  meshcraft-proxy:
    image: ctrl.example.com/meshcraft/proxy:latest
    restart: unless-stopped
    # host : indispensable pour les scans ARP/ND et la réception
    # syslog/traps sur les réseaux locaux — en bridge, l'IP source des
    # messages serait celle de la passerelle Docker, donc inutilisable.
    network_mode: host
    cap_add:
      - NET_RAW              # scan ARP / Neighbor Discovery
      - NET_BIND_SERVICE     # syslog 514/udp, traps 162/udp
    environment:
      MESHCRAFT_CONTROLLER: "https://ctrl.example.com"
      MESHCRAFT_ENROLL_TOKEN: "…"
    volumes:
      - meshcraft_proxy_state:/var/lib/meshcraft   # identité + binaire mis à jour
volumes:
  meshcraft_proxy_state:
```

Le volume d'état n'est pas un confort : il porte l'identité durable du proxy **et** le binaire téléchargé lors d'une mise à jour ([[Versionnage et mise à jour du proxy]]).

> [!warning] Deux écarts entre ce commentaire et la réalité du proxy
> Le compose généré est fidèle à cet extrait — c'est bien lui, ligne pour ligne.
> Mais deux de ses affirmations sont fausses aujourd'hui, et elles se lisent comme
> des états :
>
> - **le tag est `:latest`, pas une version.** `distrib.ImageRef` le concatène en
>   dur. Épingler une version dans le compose est **décidé, non construit**, et
>   demande deux choses qui n'existent pas encore : un tag `:<version>` écrit par
>   `mkoci`, et un registre qui **404 sur un tag inconnu**
>   ([[Versionnage et mise à jour du proxy]] §1). Ce n'est pas bloquant pour la
>   mise à jour, qui remplace le **binaire** et non le conteneur ;
> - **il n'y a pas de listener syslog.** `NET_BIND_SERVICE` est nécessaire pour
>   **162/udp** — les traps, livrés et validés sur matériel réel. Le commentaire
>   `# syslog 514/udp, traps 162/udp` annonce une écoute qui n'existe nulle part
>   dans `cmd/proxy`. Le mentionner dans un fichier que l'opérateur lit fait
>   chercher un service absent, et fait croire à une collecte syslog qui n'a jamais
>   eu lieu.

> [!important] Le registre doit rendre son index adressable par digest
> Un client `docker` résout le tag, lit le digest dans l'en-tête
> `Docker-Content-Digest`, **puis redemande le manifeste par ce digest**. Si le
> registre ne sait servir l'index que par son tag, cette seconde requête prend un
> 404 : `docker pull` s'en sort par un repli « pull by tag » **déprécié** (avec
> avertissement), mais `docker compose up` échoue franchement —
> *failed to copy: httpReadSeeker: failed open: content at … not found*.
>
> C'est un bug rencontré en vrai. L'index est donc écrit **aussi comme blob** par
> `mkoci`, et un test de non-régression le vérifie. Leçon générale : dans un
> registre, tout document est adressable par son contenu, l'index compris — le
> tag n'est qu'un alias.

### Sans Docker : script d'installation et unité systemd

Le contrôleur génère aussi un **script d'installation POSIX** pour les hôtes sans Docker (VM minimale, Raspberry Pi, politique interne). Il fait ce qu'un opérateur ferait à la main, mais sans se tromper :

1. `uname -m` → artefact correspondant parmi ceux que **ce** contrôleur sert (amd64 / arm64 / armv7) ;
2. téléchargement, puis **vérification du SHA-256** relevé à la génération du script — un binaire non conforme n'est jamais installé ;
3. création d'un compte de service non privilégié (`meshcraft`), état dans `/var/lib/meshcraft` ;
4. token d'enrollment écrit dans `/etc/meshcraft/proxy.env` en **0600** — *jamais* dans l'unité systemd, qui est lisible par tous ;
5. unité durcie, puis `systemctl enable --now`.

L'unité applique le moindre privilège annoncé par [[Sécurité du proxy]] : `User=meshcraft`, seules les capabilities `CAP_NET_RAW` (scan ARP/ND) et `CAP_NET_BIND_SERVICE` (syslog 514/udp, traps 162/udp), `NoNewPrivileges`, `ProtectSystem=strict`, `RestrictAddressFamilies` limité à `AF_INET AF_INET6 AF_NETLINK AF_PACKET`, et `StateDirectory=meshcraft` pour le seul chemin inscriptible — celui où se rangera aussi le binaire d'une mise à jour automatique ([[Versionnage et mise à jour du proxy]]).

Le script est **idempotent** : relancé, il remplace le binaire et l'unité sans toucher à l'identité déjà enrôlée (`state.json`). C'est aussi le chemin de mise à jour manuel pour qui n'active pas l'auto-upgrade.

> [!note] Pas de `curl | sh`
> Le script embarque un secret à usage unique : il est **téléchargé depuis l'UI
> par un `global-admin` authentifié**, puis lancé avec `sudo sh`. Un one-liner
> `curl … | sh` supposerait une URL porteuse du token, donc un secret dans un
> historique de shell et dans les journaux d'un reverse-proxy.

## 2. IPv6, côté proxy

Le proxy est en `network_mode: host` : **il partage la pile réseau de l'hôte, donc son IPv6 aussi.** Il n'y a rien à activer dans le compose — et surtout rien à essayer d'activer :

> [!warning] Pas de `sysctls` dans un conteneur en réseau hôte
> Docker **refuse** tout `sysctl` du préfixe `net.*` quand le conteneur n'a pas
> son propre namespace réseau (`sysctl … not allowed in host network
> namespace`). Écrire `net.ipv6.conf.all.disable_ipv6=0` dans le compose généré
> ne « active » pas IPv6 : ça empêche le conteneur de démarrer. Si IPv6 est
> désactivé sur l'hôte, c'est sur l'hôte que ça se règle.

Ce qui doit réellement être fait, côté code du proxy — et où en est chaque point :

- **Traps en double pile : livré.** `startTrapReceiver` lie `net.ListenUDP("udp", :162)` sur l'adresse **joker**, ce qui rend sous Linux une socket `AF_INET6` acceptant aussi l'IPv4 mappée (`net.ipv6.bindv6only=0`, défaut). Le joker n'est pas un raccourci : le proxy **ne sait pas** laquelle de ses adresses un équipement va viser, et c'est exactement la question à laquelle `PKTINFO` répond. Il réclame donc au noyau l'adresse de **destination** de chaque datagramme — `IPV6_RECVPKTINFO` sur une socket v6, `IP_PKTINFO` sur une v4, dans cet ordre parce que chacune n'existe que sur sa famille. L'adresse rendue est *unmappée* avant comparaison : une socket double pile rend `::ffff:10.42.0.67` là où le contrôleur a écrit `10.42.0.67` dans l'équipement, et comparer les textes ferait échouer l'attribution selon l'humeur de la pile de l'hôte.
- **Ce que le proxy journalise au démarrage** : le port réellement obtenu et si la **destination est lisible** (`destination_lisible`) — pas les familles écoutées. C'est la bonne information, et elle vaut mieux : sans destination, un trap reste un trap reçu, mais il ne peut plus **prouver** vers quelle adresse l'équipement émet. Un noyau qui refuse les deux options produit un avertissement explicite.
- **Sur un hôte où `bindv6only=1`**, il faudrait **deux listeners explicites**. Ce n'est **pas** traité : la socket unique retomberait en v4 seule, et rien ne le dirait autrement que par l'absence de traps v6. C'est un cas de figure rare et une limite connue, pas une propriété acquise.
- **IP source conservée telle quelle : livré.** L'émetteur d'un trap est identifié par son adresse, v4 ou v6, normalisée en `netip.Addr` avant d'être remontée. En revanche, **les colonnes ne sont pas des `inet`** : `devices.mgmt_ip`, `devices.trap_dest_addr` et les champs du plan de management sont des `text` ([[Schéma base de données]]). Le choix est cohérent avec le reste — la validation vit dans le code, où elle sait expliquer un refus — mais il faut le savoir avant d'écrire une requête qui compterait sur un opérateur de sous-réseau PostgreSQL.
- **Annonce des interfaces : livré.** Le proxy remonte ses adresses v4 et v6 en CIDR, en excluant loopback et link-local (`cmd/proxy/ifaces.go`) — l'UI s'en sert pour suggérer les plages à scanner.
- **URL du contrôleur : livré.** `MESHCRAFT_CONTROLLER` accepte un littéral IPv6, obligatoirement **entre crochets** (`https://[2001:db8::1]:8080`). Toute lecture d'hôte:port passe par `net.SplitHostPort` ou `netip.ParseAddrPort` — jamais un découpage sur `:`, qui casse silencieusement sur IPv6.

> [!warning] La surveillance ICMP est en IPv4 SEULEMENT
> C'est le trou de double pile qui compte le plus, parce qu'il touche le voyant
> vert/rouge de tout le parc. `cmd/proxy/monitor.go` ouvre
> `icmp.ListenPacket("ip4:icmp", "0.0.0.0")`, et la boucle d'envoi **écarte
> explicitement** toute cible v6 — le code le dit lui-même : *« IPv6 : à traiter
> avec `ip6:ipv6-icmp`, pas encore livré »*.
>
> Conséquence à connaître avant de déployer un site en IPv6 : un équipement dont
> l'adresse de management est une v6 **n'est jamais pingué**. Il ne devient pas
> rouge — il n'a simplement pas de statut, et un équipement sans statut se lit
> comme un équipement qu'on n'a pas encore configuré, pas comme une lacune de
> meshcraft.
>
> Le correctif est borné et connu : une **seconde** socket `ip6:ipv6-icmp`, avec le
> tri des cibles par famille. Il n'est pas plus subtil que ça — il n'a simplement
> pas été fait, et cette page l'annonçait comme acquis.

> [!note] Pas de collecte syslog, nulle part
> Cette section listait `:514/udp` et « le statut local » parmi les écoutes du
> proxy. Ni l'un ni l'autre n'existe : `cmd/proxy` n'ouvre que la socket de traps
> et la socket ICMP brute, et il n'y a aucun serveur HTTP de statut local. Le
> canal **syslog** est **décidé, non construit** dans son intégralité — pas de
> listener, pas de parseur, pas de destination écrite dans les équipements. Le
> garder en cible est juste ; le décrire comme une écoute en place faisait chercher
> un service absent, et laissait croire qu'un site « qui n'envoie pas ses syslog »
> avait un problème de réseau.

> [!important] Le registre intégré et les littéraux IPv6
> Le parseur de références d'images de Docker n'accepte un littéral IPv6 **que
> suivi d'un port explicite** : `[2001:db8::1]:8080/meshcraft/proxy:latest` est
> valide, `[2001:db8::1]/meshcraft/proxy:latest` ne l'est pas. Quand l'URL
> publique du contrôleur est une adresse IPv6 nue, la génération du compose
> ajoute donc **toujours** le port explicite (`:443` ou `:80`).
> Recommandation réelle : publier le contrôleur derrière un **nom DNS avec A et
> AAAA**. C'est ce qui rend le déploiement indifférent à la famille d'adresses,
> et c'est de toute façon nécessaire pour le certificat TLS.

### Découverte en IPv6 : ce qui est possible, et ce qui ne l'est pas

Un balayage IPv6 exhaustif n'existe pas — un `/64` compte 2⁶⁴ adresses. La [[Discovery]] en IPv6 ne peut donc pas reposer sur le sweep, mais sur :

- la **table de voisinage** (Neighbor Discovery) du segment local, y compris `ff02::1` (all-nodes) : c'est l'équivalent utile du scan ARP ;
- les préfixes **explicitement déclarés et petits** — le seuil est `/112`
  (`discovery.MinIPv6Bits`), et un `/120` de plage de management se balaye donc
  très bien ;
- LLDP et SNMP, qui livrent les adresses des voisins sans les chercher ;
- l'ajout manuel par adresse, qui reste la porte d'entrée fiable.

> [!important] Déclarer et balayer sont deux questions distinctes
> Cette page annonçait un refus **à la saisie**. Le code fait mieux, et la nuance
> est celle qui rend le comportement défendable : `ValidPrefix` **accepte** un
> `/64` — c'est le sous-réseau réel du site, et le refuser obligerait l'opérateur à
> mentir sur son adressage. C'est l'**énumération** qui est bornée, et seulement
> elle : `Enumerable` refuse le balayage d'un préfixe v6 plus large qu'un `/112`,
> avec sa raison en clair (« un `/64` = 1,8·10¹⁹ adresses ») et le geste de
> réparation. Côté IPv4, la borne est `MaxHosts = 65536`, soit un `/16` : au-delà,
> c'est un balayage qui se compte en heures et qui se déclare par tranches.
>
> La stratégie de découverte se choisit donc **au moment du balayage**, pas à la
> déclaration. Refuser à la saisie aurait rendu le sous-réseau v6 réel du site
> indéclarable, donc invisible dans l'inventaire des plages — un préfixe qu'on ne
> peut pas nommer est un préfixe qu'on oublie.
>
> Ce qui reste **ouvert** : la table de voisinage du proxy (NDP, `ff02::1`) n'est
> pas remontée. En attendant, un `/64` déclaré est un `/64` qu'on ne découvre pas —
> et le message de refus le dit explicitement plutôt que de laisser croire à un
> balayage silencieux.

## 3. IPv6, côté instance

> [!warning] Décidé, non construit — le `docker-compose.yml` du dépôt est en IPv4 seule
> Rien de cette section n'est en place aujourd'hui. Le compose versionné publie
> `"80:80"`, `"443:443"`, `"443:443/udp"` en notation courte, expose le contrôleur
> sur `127.0.0.1:8080` seulement, et **ne déclare aucun `enable_ipv6`**. Le fragment
> ci-dessous est la **cible**, et il est gardé tel quel pour une raison : les trois
> conditions sont cumulatives et non évidentes, et en oublier une donne un
> déploiement qui a l'air v6 sans l'être — le mode d'échec le plus coûteux de ce
> sujet, parce qu'il ne se voit qu'à l'usage.

Trois conditions cumulatives, dont deux sortent du compose :

1. **Le daemon Docker** doit savoir faire de l'IPv6 — `/etc/docker/daemon.json` :
   ```json
   {"ipv6": true, "ip6tables": true, "fixed-cidr-v6": "fd00:cafe::/64"}
   ```
   `ip6tables` est actif par défaut depuis Docker Engine 27 ; il reste à déclarer explicitement pour les versions antérieures, sans quoi les conteneurs ont une adresse v6 mais aucun retour de trafic.
2. **Le réseau du compose** : `enable_ipv6: true` avec un sous-réseau ULA.
3. **La publication des ports en double pile.** Selon la version du daemon, `"80:80"` ne publie **qu'en IPv4**. On ne veut pas dépendre de la version : les deux familles se publient explicitement.

```yaml
services:
  controller:
    ports:
      - "127.0.0.1:8080:8080"
      - "[::1]:8080:8080"        # pas d'exposition publique : Caddy est devant
  caddy:
    ports:
      - "0.0.0.0:80:80"
      - "[::]:80:80"
      - "0.0.0.0:443:443"
      - "[::]:443:443"
      - "0.0.0.0:443:443/udp"    # HTTP/3
      - "[::]:443:443/udp"
networks:
  default:
    enable_ipv6: true
    ipam:
      config:
        - subnet: fd00:cafe:1::/64
```

> [!note] Un préfixe ULA ne contient que des chiffres hexadécimaux
> Une version antérieure de cette page écrivait `fd00:meshcraft::/64`, ce qui n'est
> **pas** une adresse IPv6 : `m`, `s`, `h`, `r`, `t` ne sont pas des chiffres hex.
> Copié tel quel, Docker refuse le réseau au démarrage. Le genre de faute que seul
> un essai révèle, et que la mémoire d'un nom lisible entretient — d'où la
> correction en `fd00:cafe:…`, mnémotechnique **et** valide.

Points de vigilance côté contrôleur :

- **`MESHCRAFT_EXTERNAL_URL`** : un nom DNS (A + AAAA) de préférence ; sinon littéral entre crochets **avec port explicite**, pour la raison d'image donnée plus haut. Cette URL se retrouve dans le compose généré, dans la référence d'image, dans les descripteurs de mise à jour et dans les ordres de migration : une erreur ici se propage partout.
- **ACME sur IPv6** : Let's Encrypt valide indifféremment via A ou AAAA. Un enregistrement AAAA publié mais non joignable **fait échouer** la validation même si l'A fonctionne — le piège IPv6 le plus courant en production.
- **Adresse du client** : `X-Forwarded-For` en IPv6 contient des `:`. L'extraction passe par `middleware.RealIP` (chi), monté en tête de chaîne, et non par un découpage maison. Le contrôleur ne fait confiance à cet en-tête que parce que **Caddy est le seul chemin d'entrée** — et c'est le compose qui le garantit, en n'exposant le contrôleur que sur la boucle locale. Publier le port 8080 casserait cette hypothèse sans toucher une ligne de Go.
- **PostgreSQL et VictoriaMetrics** ne sont jamais publiés : ils restent sur le réseau interne du compose, la famille d'adresses n'y change rien.

## 4. Mode bridge : documenté, jamais généré

Sur un hôte où `network_mode: host` est refusé (politique de sécurité, orchestrateur), le proxy tourne en bridge avec `enable_ipv6` — mais **en mode dégradé assumé** : le scan ARP/ND ne voit plus le segment local, et l'IP source des traps devient celle de la passerelle Docker, ce qui rend l'attribution des messages aux équipements impossible ([[Monitoring]]). Le contrôleur ne génère pas cette variante ; elle est donnée en documentation, avec ses limites, pour les cas où seul le tunnel TCP est attendu du proxy.

La dégradation est en fait **double**, et la seconde moitié n'était pas dite : le
proxy n'identifie pas seulement un trap par son IP **source**, il vérifie aussi son
adresse de **destination** pour prouver que le chemin retour passe bien par
l'adresse écrite dans l'équipement (`trap_seen_at`, migration `0023`). En bridge,
la destination vue est une adresse du réseau Docker, jamais celle que le contrôleur
a écrite : la preuve du chemin retour devient inatteignable, en plus de
l'attribution par source. Un proxy en bridge peut donc servir de **tunnel** et de
sonde ICMP ; il ne peut pas être un **collecteur de traps**.

## 5. Décidé / livré / ouvert

| Question | État |
|---|---|
| Génération du compose du proxy avec couverture et token à usage unique | **livré** — `POST /api/proxies/compose`, `internal/distrib/compose.go` |
| Script d'installation POSIX + unité systemd durcie, sans Docker | **livré** — `internal/distrib/install.go` : `uname -m`, SHA-256 vérifié, compte de service, `0600` sur le fichier d'environnement |
| Moindre privilège de l'unité : `User=`, `NoNewPrivileges`, `ProtectSystem=strict`, `RestrictAddressFamilies`, `StateDirectory` | **livré** — capabilities bornées à `CAP_NET_RAW` + `CAP_NET_BIND_SERVICE` |
| Idempotence du script d'installation (remplace binaire et unité, garde l'identité) | **livré** |
| Registre intégré : index adressable par digest, `Content-Length` en `HEAD` | **livré**, avec test de non-régression |
| Réception des traps `162/udp` en double pile, destination lisible par `PKTINFO` | **livré** et validé sur matériel réel |
| Annonce des interfaces v4 + v6 en CIDR | **livré** |
| Littéral IPv6 dans `MESHCRAFT_CONTROLLER` et dans la référence d'image | **livré** — crochets, et port explicite ajouté d'office |
| Surveillance **ICMP en IPv6** | **ouvert** — v4 seule aujourd'hui, correctif borné et connu (§2) |
| Deux listeners explicites si `bindv6only=1` | **ouvert** — non traité, échoue silencieusement |
| Collecte **syslog** (listener, parseur, destination poussée) | **décidé, non construit** — rien n'existe |
| IPv6 côté instance : `enable_ipv6`, publication double pile | **décidé, non construit** — le compose du dépôt est en v4 seule (§3) |
| Épinglage d'une version d'image dans le compose généré | **décidé, non construit** — voir [[Versionnage et mise à jour du proxy]] §1 |
| Bornes d'énumération : `/112` en v6, `/16` en v4, avec refus expliqué | **livré** — `internal/discovery/prefix.go`, et le refus porte sur le **balayage**, non sur la déclaration |
| Découverte v6 par table de voisinage (NDP, `ff02::1`) | **décidé, non construit** — nommé dans le message de refus lui-même |
