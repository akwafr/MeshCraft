---
tags: [etude]
---

# État de l'art — ce qui existe, ce qu'on en retient

| Outil | Ce qu'il fait | Ce qu'on retient / rejette |
|---|---|---|
| **UniFi Controller** (Ubiquiti) | contrôleur mono-constructeur : adoption, profils de ports, groupes, provisioning déclaratif, UI ports colorés | ✅ le modèle UX de référence (adoption, profils, [[Profils et groupes]], sites) ; ❌ verrouillage constructeur — meshcraft en est la version multi-vendor |
| **Omada** (TP-Link), **Central/InstantOn** (Aruba), **Catalyst Center** | idem, chacun chez soi | confirme le besoin : chaque vendeur a son contrôleur, personne ne fait le pont |
| **LibreNMS / Observium** | monitoring SNMP multi-vendor, discovery, alerting | ✅ preuve que le SNMP multi-vendor à grande échelle marche ; leurs MIB/OS detections sont une mine d'inspiration pour le fingerprint ([[Discovery]]) ; ❌ monitoring only, pas de configuration |
| **NetBox / Nautobot** | source of truth (IPAM/DCIM), pas d'exécution | ✅ l'idée « intended state en base » ; meshcraft est SoT **et** moteur d'exécution — pour l'interop, un export/import NetBox est un plus (v2) |
| **NAPALM / netmiko / nornir** | drivers Python multi-vendor (getters normalisés, config replace/merge) | ✅ la preuve du concept « modèle normalisé multi-vendor » ; leur découpage getters/config inspire l'interface `Driver` ([[Connecteurs]]) ; non réutilisables directement (Python, cf. [[Stack technique]]) |
| **scrapli / scrapligo** | transport CLI réseau (prompts, paging) | ✅ scrapligo est notre brique transport CLI |
| **Oxidized / RANCID** | backup de configs multi-vendor | ✅ la fonction backup+diff de conf brute est reprise telle quelle ([[Réconciliation]], `config_backups`) ; leurs models = catalogue de quirks CLI par plateforme |
| **Unimus** | backup + push de commandes multi-vendor, mass config | ✅ valide le marché « multi-vendor config » ; ❌ pousse des *commandes*, pas un état déclaratif — meshcraft vise l'intent |
| **Ansible (ios_*, junos_*)** | config déclarative par playbooks | ❌ pas de temps réel, pas d'UI, état simulé ; utile comme référence de mapping intent→CLI par plateforme |
| **OpenConfig / gNMI** | modèle YANG normalisé, streaming telemetry | ✅ inspiration du [[Modèle de données standard]], et désormais un collecteur réel : `internal/gnmipoll` s'abonne en gRPC (bibliothèque `openconfig/gnmic`) et Arista EOS est collecté par lui SEUL, sans SNMP. ❌ le constat de départ tient toujours pour le reste du parc — gNMI reste l'apanage du haut de gamme, et les sept autres connecteurs restent en SNMP. C'est bien un canal de COLLECTE et non un connecteur : rien ne se configure par gNMI ici |
| **Weathermap (Network Weathermap, LibreNMS plugin)** | cartes de charge de liens | ✅ la référence visuelle de la page [[Weathermap]] |

## La niche de meshcraft

Personne ne combine aujourd'hui : **multi-constructeur + configuration déclarative + adoption/profils façon UniFi + monitoring intégré + proxys distants sans VPN**. Les briques existent séparément (LibreNMS pour le monitoring, Unimus pour le push, NetBox pour l'intent, UniFi pour l'UX) — la valeur est dans l'intégration, et particulièrement dans les [[Connecteurs]] pour le matériel « smart switch » d'entrée/milieu de gamme (D-Link, TP-Link, Netgear, Zyxel) que tous les outils sérieux ignorent car il est web-only ([[Accès web pass-through]]).
