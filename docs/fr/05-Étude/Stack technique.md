---
tags: [etude, stack]
---

# Stack technique — choix et justifications

Contrainte posée : **backend et proxy en Go**. Reste à trancher le frontend et l'outillage.

## Backend Go — conséquences assumées

Go est un excellent choix ici : un binaire unique (contrôleur + frontend embarqué), concurrence naturelle pour des milliers de sessions/tunnels, même langage que le proxy (partage du code de protocole, des modèles, du framing — un module `pkg/wire` commun aux deux binaires).

Le point à connaître : l'écosystème d'automatisation réseau est historiquement **Python** (netmiko, NAPALM, nornir). En Go, l'équivalent existe mais est plus jeune :

| Besoin | Lib Go retenue | Maturité |
|---|---|---|
| SSH/Telnet réseau (prompts, paging, platforms) | **scrapligo** (Carl Montanari, même famille que scrapli Python) | bonne, plateformes majeures couvertes |
| SSH bas niveau | `golang.org/x/crypto/ssh` | standard |
| SNMP | **gosnmp** | mature |
| Parsing de sorties CLI | port Go de TextFSM + templates ntc-templates, regex maison | correct ; il faudra écrire des templates |
| HTTP API/scraping | net/http, resty, goquery | standard |
| WS + multiplexage | nhooyr/websocket + **hashicorp/yamux** | mature |
| Web framework | chi (routing) + oapi-codegen (OpenAPI → handlers + client TS) | mature |
| DB | pgx + sqlc, migrations goose | mature |

Conséquence pratique : l'effort par [[Connecteurs|connecteur]] est un peu plus élevé qu'en Python (moins de parsers tout faits) — compensé par la robustesse du typage sur le modèle pivot et l'absence de frontière inter-langages.

## Frontend — décision : React + TypeScript en PWA, pas Flutter

Question posée : interface réactive WebSocket, fonctionnelle sur mobile — Flutter ?

**Évaluation Flutter (web)** honnête :
- ✅ un seul code pour web + apps natives ; très bon pour des apps mobiles « produit ».
- ❌ Flutter *Web* rend dans un canvas : bundle initial lourd (~1,5 Mo+), texte non sélectionnable par défaut, zoom/scroll non natifs, accessibilité et extensions navigateur dégradées ;
- ❌ notre UI est **dense et « document »** : tableaux, panneaux, centaines de façades SVG ([[Rendu des ports]]), graphe pan/zoom ([[Weathermap]]) — exactement là où le DOM/SVG natif excelle et où le canvas Flutter oblige à tout réimplémenter ;
- ❌ écosystème de visualisation (React Flow, d3, virtualisation de listes) sans équivalent mûr côté Flutter ;
- ❌ deux langages dans le projet (Dart + TS pour l'outillage web restant) pour une équipe réduite.

**Choix : SPA React 18 + TypeScript, PWA installable.**
- **Réactivité WS** : un hook WS unique qui patche le cache **TanStack Query** — l'UI entière devient temps réel sans état parallèle ([[Interface web]]).
- **Mobile** : responsive + PWA (manifest, service worker, installable sur l'écran d'accueil, notifications push web). Pour une console d'admin, la PWA couvre le besoin mobile réel (consulter, vérifier un port, acquitter) sans pipeline App Store.
- **SVG natif** pour ports et topologie : performance et finesse de rendu, exactement le besoin.
- Outillage : Vite, Tailwind CSS, `@xyflow/react` pour la weathermap, types API générés depuis l'OpenAPI du backend (contrat bout en bout).
- Alternative considérée : **SvelteKit** (plus léger, très bon en réactivité) — écarté uniquement pour la taille d'écosystème (React Flow, composants, recrutement). Si une **app native** devient un vrai besoin (v2+ : notifications riches, scan de QR d'enrollment…), Flutter *mobile-only* par-dessus l'API existante redevient une bonne option — la décision web n'hypothèque rien.

## Récapitulatif

| Couche | Choix |
|---|---|
| Contrôleur | Go 1.22+, chi + oapi-codegen, pgx/sqlc, yamux sur WSS |
| Proxy | Go, même repo (monorepo, `cmd/controller`, `cmd/proxy`, `pkg/wire` partagé) |
| Connecteurs | packages Go in-tree, scrapligo/gosnmp/goquery ([[Connecteurs]]) |
| Frontend | React + TS, Vite, Tailwind, TanStack Query, React Flow, PWA — embarqué via go:embed |
| Base | PostgreSQL 16 ([[Schéma base de données]]) |
| Livraison | binaire contrôleur + image Docker ; proxy en binaire/deb/rpm/docker multi-arch (GoReleaser) |
| CI | golangci-lint, tests transcripts connecteurs, vitest/playwright côté front |
