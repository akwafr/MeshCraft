---
tags: [architecture, modele]
---

# Modèle de données standard (l'intent)

Le format pivot du système : **tout ce qu'on peut configurer sur un switch**, exprimé de façon agnostique du constructeur. C'est l'entrée des [[Connecteurs]] (écriture) et leur sortie (lecture normalisée). Inspiration : OpenConfig, en beaucoup plus simple — on ne modélise que ce que meshcraft gère réellement, et on étend le schéma version après version.

> [!warning] Ce document décrit DEUX choses, et une seule est construite
> Il y a en réalité **deux pivots** dans meshcraft, et les confondre est la faute
> que cette page invitait à commettre :
>
> - le **format d'échange de site** `meshcraft_site_config` — **livré**,
>   `internal/siteconfig`, exportable et importable, avec son exemple complet dans
>   `samples/site-demo.json`. C'est le contrat pivot côté SI, décrit plus bas ;
> - l'**intent par équipement** en YAML décrit juste en dessous — **cible**. Il n'a
>   ni schéma, ni structure Go, ni table : `schema_version`, `services`,
>   `management`, `cred://` et `device_intents` ne se trouvent nulle part dans le
>   code. Ce qui joue son rôle aujourd'hui est un `jsonb` beaucoup plus étroit,
>   `devices.ports`, dont le contenu réel est donné en § *Ce qui est réellement
>   stocké*.
>
> Le YAML reste ici, et il faut le garder : c'est la seule description de ce que le
> modèle devra porter quand une deuxième plateforme arrivera, et c'est de lui que le
> découpage des lots se déduira. Le lire comme un format en place fait chercher un
> parseur, un schéma et une table de révisions qui n'existent pas.

## Structure — la CIBLE

```yaml
schema_version: 1
device:
  hostname: sw-atelier-01
  management:                        # cf. Adressage de management
    mode: site_plan                 # site_plan | keep (keep = champs absents,
                                    # donc « non géré » : rien n'est réécrit)
    vlan: 10
    vlan_untagged: false            # management sur VLAN natif (cas des
                                    # équipements qui n'admettent que VLAN 1)
    ipv4: {mode: static, address: 10.0.10.5/24, gateway: 10.0.10.1}   # ou mode: dhcp
    dns: [10.0.10.1]
    ntp: [10.0.10.1]
    ssh: {enabled: true}
    http: {enabled: false}          # on coupe l'UI web quand SSH suffit
    telnet: {enabled: false}

vlans:                              # référence les VLANs du site (voir Profils et groupes)
  - {id: 10, name: mgmt}
  - {id: 20, name: users}
  - {id: 30, name: voip}
  - {id: 99, name: iot}

ports:
  - name: Gi1/0/1                   # nom canonique du catalogue de modèles
    enabled: true
    profile: bureau-standard        # trace du profil résolu (informationnel)
    mode: access                    # access | trunk
    pvid: 20                        # VLAN untagged (native en trunk)
    tagged: []                      # trunk : liste ou "all"
    voice_vlan: 30
    poe: {enabled: true, priority: low}
    stp: {edge: true, bpdu_guard: true, root_guard: false}
    security:
      dhcp_snooping_trust: false
      ra_guard: true
      storm_control: {broadcast_pps: 1000}
      port_security: {max_macs: null}
    lldp: true
    speed: auto                     # auto | 100M | 1G | 10G...
    description: "Bureau 12"

services:
  stp: {mode: rstp, priority: 32768}
  igmp_snooping: true
  dhcp_snooping: {enabled: true, vlans: [20, 30]}
  # Identité de service : poussée par meshcraft, politique globale surchargeable
  # par site, secret par équipement, rotation par générations.
  service_account:                  # compte CLI de meshcraft sur l'équipement
    username: meshcraft
    auth: pubkey                    # pubkey | password | pubkey_then_password
    key_ref: cred://svc-ssh         # clé publique déployée ; la privée reste au coffre
    privilege: 15
  snmp:                             # déployé automatiquement par le monitoring
    # v3 et v2c_ro sont exclusifs : la version est RÉSOLUE par équipement contre
    # les capabilities du connecteur, pas choisie dans la politique.
    v3: {user_ref: cred://svc-snmp, auth: sha256, priv: aes128}
    v2c_ro: null                    # renseigné à la place de v3 sur les plateformes
                                    # qui ne le supportent pas — lecture seule, jamais RW
    traps: {host: <ip du collecteur dérivé>, user_ref: cred://svc-snmp}
  syslog: {host: <ip du collecteur dérivé>, port: 514, severity_min: informational}
  telemetry:                        # streaming telemetry, quand la plateforme sait
    mode: gnmi_dialout              # off | gnmi_dialout | gnmi_dialin
    target: <ip du collecteur dérivé>
    cert_ref: cred://svc-telemetry  # certificat client + CA, à échéance datée
    interval: 10s
```

## Règles de conception

Sept règles, et leur état n'est pas le même : deux gouvernent réellement le code aujourd'hui, deux sont partiellement tenues, trois attendent le format.

| Règle | État |
|---|---|
| 1. **Le modèle est le contrat** — l'UI ne produit que ça, les drivers ne consomment que ça ; toute feature nouvelle commence par une évolution versionnée du schéma (`schema_version`) | **cible.** Il n'y a pas de `schema_version` ; le versionnement existe sur le **format d'échange** (`meshcraft_site_config: 1`), pas sur l'intent par équipement |
| 2. **Deux instances du même type**, `desired` et `observed`, dont le diff pilote la [[Réconciliation]] | **livré, et c'est la règle la plus rentable de la liste.** `devices.ports` porte l'intention, `devices.observed` la mesure (migration `0014`) — les confondre reviendrait à ne jamais pouvoir dire laquelle des deux est en retard. Une nuance : ce ne sont pas exactement le *même* type, l'observé étant sérialisé depuis le connecteur (`instanton.PortState`) et l'intention depuis l'UI ; c'est `recon.ParsePorts` qui les ramène au même dénominateur |
| 3. **Champs optionnels = « non géré »** | **livré** — `recon.unmanaged` : un port sans mode, ou en « libre », n'est ni diffé ni écrit. Et la règle ne joue que dans **un sens** : un port que meshcraft gère et que l'équipement ne porte plus est un écart bien réel |
| 4. **Normalisation stricte à la lecture** : noms canoniques, unités fixes, booléens explicites, jamais de comparaison de chaînes brutes | **livré pour ce qui est modélisé.** Le nom de port canonique vient du préfixe de façade de l'équipement (`portPrefix`, lu dans `port_groups`), et c'est ce qui évite un état relu dont les noms ne correspondraient à aucun port de meshcraft. Le « catalogue de modèles par plateforme » annoncé n'existe pas : `internal/catalog` ne porte que des gabarits **génériques** (`vendor: generic`), utiles pour maquetter un site, pas pour piloter du matériel réel. La façade des équipements adoptés vient donc du **relevé** fait par le connecteur, ce qui est mieux — c'est un fait et non une fiche produit |
| 5. **Les capabilities filtrent** : validation à la compilation, message lisible plutôt qu'échec silencieux | **partiel, et l'arbitrage a changé.** Rien ne *rejette* un intent : la pose écrit tout ce qui est supporté, considère l'équipement à jour pour autant, et **dit** ce qui est resté à quai (`last_apply.unsupported`, dérivé de `instanton.WritableExtras`). Refuser aurait bloqué l'exploitant sur une intention légitime que la plateforme suivante saura poser ; se taire aurait affiché une configuration qui n'est nulle part. La troisième voie est d'écrire et d'annoncer le reste — détails en § *Trois façons de ne pas poser* de [[Réconciliation]] |
| 6. **Références, pas de secrets** — des `cred://` vers le coffre, jamais de secret en clair | **respectée dans l'esprit, absente dans la lettre.** Aucun `cred://` n'existe : les secrets vivent dans `site_credentials` (chiffrés par `internal/secretbox`) et sont résolus **par équipement** à l'usage, via la portée par étiquette ([[Étiquettes et portée des identifiants]]). Le résultat est celui voulu — aucun secret ne transite par `devices.ports` — obtenu en ne mettant *rien* dans le modèle plutôt qu'une référence |
| 7. **La référence ne porte pas la génération** | **livré**, par construction : puisque l'intent ne référence aucun secret, une rotation ne peut pas créer de révision d'intent. Les générations vivent dans `credential_generations` (migration `0011`), append-only. La règle était juste ; le mécanisme qui l'aurait fait tenir n'a simplement pas eu à être écrit |

## Représentation en Go

**Cible** : structs générées depuis un schéma JSON Schema unique (source de vérité dans `schema/intent/v1.json`), `go-jsonschema` vers `internal/model/`, les mêmes types projetés en TypeScript, et validation runtime des imports/exports par ce schéma.

**Réalité** : il n'y a ni `schema/`, ni générateur, ni validation par schéma. `internal/model/model.go` est **écrit à la main**, et les types du frontend (`web/src/api.ts`) le sont aussi, séparément. Les parties structurées du modèle sont volontairement laissées en `json.RawMessage` (`Ports`, `PortGroups`, `Observed`, `Extra`, `LastApply`, les `spec` de profils) : le contour bougeant encore, un type Go figé aurait imposé une migration par changement de forme — et surtout, un aller-retour par une struct **perd tout champ que la version courante ne connaît pas**, ce qui est la pire façon de perdre une configuration (le raisonnement est explicite dans `siteconfig.RemapSwitchRules`).

Ce que cette absence coûte, et il vaut mieux le savoir avant de la prolonger : rien ne garantit que le frontend et le backend parlent du même objet, et la seule chose qui empêche un `jsonb` malformé d'arriver en base est un `Unmarshal` tolérant. Le jour où un deuxième connecteur consommera ce modèle, le schéma cessera d'être un confort.

## Ce qui est réellement stocké

`devices.ports`, une carte `nom de port → objet`. Le contenu **effectif**, tel que l'UI l'écrit et que `recon` / `instanton` le lisent :

| Champ | Rôle | Consommé par |
|---|---|---|
| `mode` | `access` \| `trunk` \| `libre` | diff **et** pose |
| `pvid` | VLAN natif ; absent sur un trunk vaut **1** ([[VLAN par défaut]]) | diff **et** pose |
| `tagged` | liste de VLANs, **ou** la chaîne `"all"` — un joker résolu en les VLANs du site, et traité comme un **plancher** et non un ensemble exact | diff **et** pose |
| `poe` | intention de PoE | **enregistré, jamais posé** : `recon.RequestedExtras` le relève et le bandeau le dit |
| `label` | annotation meshcraft, que l'équipement ne connaît pas | affichage seulement — et **exclu** des intentions non posées, sinon le bandeau se remplirait de bruit sur tous les ports documentés |
| `link`, `poe_w`, `rx_mbps`, `tx_mbps` | dernier état **observé** | affichage. Jamais diffé : *la télémétrie n'est pas une intention*, et la comparer ferait clignoter tout le parc en permanence |

`recon.ExtraIntents` déclare d'avance quatre intentions hors VLAN — `poe`, `stp`, `dhcp_snooping`, `port_security` — dont l'UI ne propose aujourd'hui que la première. Les déclarer avant de les proposer ne coûte rien et évite qu'une nouvelle case à cocher redevienne silencieuse.

Et le reste du YAML cible ? Il existe, mais **ailleurs que dans l'intent**, ce qui est le point important de cette section :

| Bloc de la cible | Où c'est réellement |
|---|---|
| `device.management` (VLAN, IP, DNS, NTP, SSH/HTTP/Telnet) | table `site_mgmt_plan` — **par site**, pas par équipement (migration `0009`, [[Adressage de management]]). Le mode `keep` de la cible correspond à un équipement non conforme au plan, que `internal/api/conformity.go` calcule au lieu de le stocker |
| `vlans` | table `vlans`, par site, avec couleur et propos — le modèle standard n'en porte qu'une référence par identifiant |
| `services.snmp`, `services.snmp.traps` | `site_credentials` (le secret) + `devices.trap_dest_addr` (l'adresse écrite) + `devices.trap_seen_at` (la preuve du chemin retour). **Livré** ([[Monitoring]], [[Identité SNMP]]) |
| `services.service_account` | `internal/api/devicecreds_handlers.go` + `instanton.EnsureAccount`. Il n'y a **pas** d'authentification par clé publique : la seule plateforme livrée est web-only, donc mot de passe |
| `services.syslog` | **rien.** Ni champ, ni pose, ni collecteur |
| `services.telemetry` (gNMI) | **rien** — aucune plateforme livrée ne sait le faire ([[Connecteurs]] : `Streaming: false`) |
| `services.stp`, `igmp_snooping`, `dhcp_snooping` globaux | **rien** au niveau équipement |
| `ports[].enabled`, `voice_vlan`, `stp`, `security`, `lldp`, `speed` | **rien** — ni stockés, ni posés. `stp`, `dhcp_snooping` et `port_security` sont pré-déclarés dans `recon.ExtraIntents`, ce qui est le premier pas et pas davantage |
| `ports[].profile` (trace du profil résolu) | remplacé par un mécanisme différent : les [[Profils de switchs]] sont des **règles** (`internal/portrules`) qui produisent la configuration, avec un rapport par règle disant combien de ports elle a gagnés — plus utile qu'une trace figée dans chaque port, parce qu'il se recalcule |

## Format d'échange `meshcraft_site_config` — *livré*

Toute la configuration réseau d'un site tient dans **un seul JSON** exportable / importable (`GET /api/sites/{id}/export`, `POST /api/sites/{id}/import`, `internal/siteconfig` + `internal/store/siteconfig_store.go`) :

```json
{
  "meshcraft_site_config": 1,
  "site": {"name": "…", "plan": {"zones": […]}},
  "vlans": [{"vlan_id": 20, "name": "DANTE PRI", "color": "#46a758"}],
  "port_profiles": [{"name": "Caméra PTZ", "spec": {"mode": "trunk", "pvid": 40, "tagged": [40, 41], "poe": true}}],
  "device_profiles": […], "device_groups": […],
  "switch_profiles": [{"name": "Salle 24P", "spec": {"rules": [{"label": "SFP+ en uplink", "select": {"media": "sfp"}, "port_profile": "Uplink"}]}}],
  "devices": [{"name": "FOH", "model": "Generic-24P-4X", "ports": {"ether5": {"mode": "trunk", "pvid": 10, "tagged": [10, 70, 71], "poe": true}}}],
  "links": [{"a": "CORE", "b": "FOH", "speed": "10G", "status": "up"}]
}
```

Règles, toutes **livrées** : version dans le champ racine ; **références par nom** (jamais d'id → portable entre instances) ; import = **remplacement transactionnel complet** (purge puis réinsertion de `vlans`, `port_profiles`, `switch_profiles`, `device_profiles`, `device_groups`, `devices` dans une seule transaction — tout ou rien). C'est le contrat pivot côté SI, indépendant du constructeur. Exemple complet : `samples/site-demo.json`.

Deux détails de conception qui valent d'être lus, parce qu'ils disent comment ce format doit évoluer :

- **`switch_profiles` a été ajouté sans incrémenter la version.** Un lecteur ancien ignore le champ, un lecteur récent sait ne pas le trouver : il n'y avait rien à casser. Une version de format se réserve aux changements qu'un ancien lecteur interpréterait **de travers**, pas à ceux qu'il ne voit pas.
- **La traduction des références se fait sur le JSON brut**, sans passer par les types Go des règles. Un aller-retour par une struct amputerait un fichier exporté par une version plus récente de tout ce qu'elle sait faire — en silence. Et une référence inconnue est **laissée en place** plutôt qu'effacée : elle s'affiche comme une règle en défaut, ce qui se répare, là où l'effacer produirait une prise recevant silencieusement ce que la règle suivante lui donne.

> [!warning] Un import peut faire perdre à un site son VLAN 1
> L'import purge la table `vlans` du site puis réinsère celles du fichier. Un fichier
> écrit à la main sans VLAN 1 laisse donc le site sans son VLAN par défaut — ce que
> `store.CreateSite` et la migration `0018` existent précisément pour empêcher
> ([[VLAN par défaut]]). L'export ne produit jamais ce fichier (le VLAN 1 est en
> base), donc le cas ne s'atteint que par un fichier fabriqué ; il reste que
> l'invariant est tenu à la création et **pas** à l'import. Le geste de réparation est
> le même que celui de la migration : un `where not exists` après la réinsertion.

## Versionnement & historique — *décidé, non construit*

**Rien de cette section n'existe.** Il n'y a pas de table `device_intents`, pas de révision, pas d'auteur, pas de diff entre révisions, pas de retour en arrière — et pas non plus de sauvegarde de configuration *brute* façon Oxidized : `BackupConfig` n'est pas au contrat `driver.Driver` et aucun connecteur ne l'implémente.

Ce qui est livré et qu'il ne faut pas prendre pour son remplaçant :

| Ce qui existe | Ce que ça couvre — et ce que ça ne couvre pas |
|---|---|
| `devices.desired_changed_at` | **quand** l'intention a bougé pour la dernière fois. Sert à *attribuer* un écart (§ *Attribution* de [[Réconciliation]]), pas à dire ce qui a changé |
| `devices.last_apply` (migration `0015`) | ce que la **dernière** pose a écrit, épargné, laissé hors capacités, ou raté. Une seule pose, écrasée à la suivante |
| tables `pipelines` / `pipeline_steps` (migration `0016`) | le **transcript** de chaque opération sur le parc, consultable après coup, avec son auteur. C'est un journal d'**actions**, pas un historique d'**états** : on peut y relire ce qu'une pose a fait, jamais reconstituer la configuration d'avant |
| [[Journal d'événements]] | `config.applied`, `config.drift`, `config.absorbed`… — les faits, sans leur contenu |

Autrement dit : on sait **qui a fait quoi et quand**, on ne sait pas **revenir**. C'est l'écart que `device_intents` comblerait, et la raison de le vouloir n'est pas l'audit (déjà couvert) mais l'exploitation : aujourd'hui, une absorption de dérive (`config.absorbed`) **remplace l'intention** sans que rien ne conserve celle qu'elle écrase.
