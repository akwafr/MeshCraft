---
tags: [moc, meshcraft]
---

# Meshcraft — Index (Map of Content)

> Documentation bilingue : [English index](../en/Index.md) · [docs README](../README.md)

**Meshcraft** est un orchestrateur multi-constructeurs de switchs et routeurs réseau : un contrôleur central qui découvre, configure et supervise des équipements via SSH, Telnet ou HTTP, directement ou à travers des **proxys** déployés dans des réseaux distants. Inspiration assumée : le contrôleur UniFi, mais **agnostique du constructeur**.

## Principes directeurs

1. **Intent-based** : l'utilisateur décrit un état voulu (le [[Modèle de données standard]]) ; les [[Connecteurs]] le traduisent en configuration spécifique à chaque plateforme.
2. **Modulaire** : chaque plateforme (Cisco IOS, Aruba, MikroTik, D-Link web-only…) est un connecteur enfichable qui déclare ses capacités.
3. **Un seul chemin de code** : le contrôleur embarque un proxy interne — un équipement joignable directement est traité exactement comme un équipement derrière un proxy distant ([[Proxy]]).
4. **Rien d'entrant chez le client** : les proxys établissent une connexion WebSocket **sortante** vers le contrôleur ([[Protocole contrôleur-proxy]]).
5. **Ne jamais se couper la main** : toute application de configuration passe par un plan, un dry-run et un mécanisme de rollback ([[Réconciliation]]).

## Architecture

- [[Vue d'ensemble]] — schéma global et flux principaux
- [[Contrôleur]] — le serveur central et ses modules
- [[Proxy]] — l'agent déployé dans les réseaux distants
- [[Protocole contrôleur-proxy]] — tunnel WebSocket multiplexé
- [[Versionnage et mise à jour du proxy]] — horodatage de build, auto-upgrade piloté par le contrôleur
- [[Déploiement Docker et IPv6]] — les deux composes, script systemd, double pile de bout en bout
- [[Connecteurs]] — les drivers par plateforme et leurs capabilities
- [[Modèle de données standard]] — le format pivot de configuration
- [[Profils de switchs]] — des règles qui produisent la configuration des ports : sélecteurs de façade, première règle gagnante, aperçu port par port — et ce que devient le groupe d'équipements
- [[Adressage de management]] — conserver l'adressage ou appliquer le plan du site, IPAM, VLAN de management
- [[Discovery]] — découverte automatique sur les plages de scan, adoption avec choix du site
- [[Domaines d'adressage]] — dans quel réseau une IP a un sens : dédupliquer sans confondre deux LAN qui partagent le même plan d'adressage
- [[Réconciliation]] — du désiré à l'appliqué : diff, plan, apply, rollback
- [[VLAN par défaut]] — le VLAN 1 : présent d'office, indélébile, natif implicite des trunks
- [[Monitoring]] — SNMP, traps, syslog, métriques
- [[Journal d'événements]] — l'event log alimenté par le monitoring
- [[Monitoring unifié et pipelines]] — cadences par capability, collecte côté proxy, VictoriaMetrics, pipelines visibles et zone de debug
- [[Télémétrie MikroTik]] — CSS326 et CRS326 : trois canaux pour RouterOS (REST, API binaire, SNMP), scraping de pages pour SwOS, capteurs de santé et diagnostic optique, et ce que chaque plateforme ne publiera jamais

## Frontend

- [[Interface web]] — navigation, sites, temps réel
- [[Rendu des ports]] — spécification visuelle des cases de ports (up/down, couleurs VLAN, PVID)
- [[Weathermap]] — topologie LLDP et placement des équipements
- [[Voisinage LLDP]] — ce que les équipements voient en face d'eux : relevé, collecte, liens dérivés
- [[Profils et groupes]] — profils de ports et d'équipements, héritage

## Sécurité & données

- [[RBAC]] — authentification, rôles, portée par site
- [[Credentials]] — stockage chiffré des identifiants SSH/HTTP/SNMP
- [[Sécurité du proxy]] — enrollment, rotation, moindre privilège
- [[Identité de service et rotation]] — les accès que meshcraft installe lui-même, politique globale surchargeable par site, générations de secrets
- [[Identité SNMP]] — communauté v2c de site, identités v3 par étiquette d'algorithmes, négociation au provisionnement
- [[Étiquettes et portée des identifiants]] — étiqueter un parc (à la main ou par règles) et viser une étiquette avec un identifiant particulier
- [[Schéma base de données]] — tables principales

## Étude approfondie

- [[Stack technique]] — choix Python / Go / React / PostgreSQL et alternatives
- [[État de l'art]] — UniFi, LibreNMS, NetBox, NAPALM… ce qu'on en retient
- [[Accès web pass-through]] — piloter les équipements « web only » à travers le proxy
- [[Points durs et risques]] — les vrais problèmes à anticiper

## Livraison

- [[Roadmap]] — phases MVP → v1 → v2
- [[Notes de version]] — ce que chaque version apporte, et ce qu'elle laisse ouvert
