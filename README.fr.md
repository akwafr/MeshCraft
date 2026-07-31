# meshcraft

[English](README.md)

Orchestrateur multi-constructeurs de switchs et routeurs réseau : un contrôleur
central qui découvre, configure et supervise des équipements via SSH/Telnet/HTTP,
directement ou à travers des **proxys** déployés dans des réseaux distants.

**Licence :** [AGPL-3.0-only](LICENSE)

## Origine

Le besoin de gérer un réseau de façon unifiée est né des **LAN parties** et des
événements associatifs où nous ne disposions que de **matériel hétérogène** —
constructeurs, générations et interfaces d'administration différents, sans le
luxe d'apprendre chaque plateforme avant l'ouverture des portes.

meshcraft est né dans le cadre de projets autour de
[Lyon e-Sport](https://lyon-esport.fr/). Gros remerciements à **DrumSlayer** et
**Emma-0**.

## Démarrage rapide

Prérequis : Docker (+ plugin Compose). **Tout se construit en conteneur.**

```bash
cp .env.example .env   # domaine public, contact ACME, compte admin
make build             # frontend Vite + Go + proxys multi-arch + image OCI du proxy
make up                # contrôleur + PostgreSQL + Caddy (TLS)
make logs              # le mot de passe admin initial y est affiché au premier démarrage
```

Compte initial : `admin@meshcraft.local` (mot de passe : `MESHCRAFT_ADMIN_PASSWORD`,
sinon généré et loggé une seule fois).

### HTTPS public

Le service `caddy` du compose termine le TLS et obtient/renouvelle seul les
certificats **Let's Encrypt** (ACME HTTP-01) pour `MESHCRAFT_DOMAIN`. Le domaine
doit pointer vers la machine et les ports 80/443 doivent être joignables. Le
contrôleur n'est exposé que sur `127.0.0.1:8080`.

Avec un front HTTPS valide, `docker pull <domaine>/meshcraft/proxy:latest`
fonctionne sans `insecure-registries`. En local sans domaine, laisser
`MESHCRAFT_DOMAIN=localhost` et utiliser http://localhost:8080.

## Captures d'écran

![Dashboard](docs/screenshots/dashboard.png)

![Équipements](docs/screenshots/devices.png)

![Ports](docs/screenshots/ports.png)

![Topologie](docs/screenshots/topology.png)

![Monitoring](docs/screenshots/monitoring.png)

![Configuration](docs/screenshots/configuration.png)

## Documentation

La conception complète vit dans le vault Obsidian :

- Français : [`docs/fr/`](docs/fr/) (entrée : `Meshcraft - Index.md`)
- Anglais : [`docs/en/`](docs/en/)

Conventions de code : [`CLAUDE.md`](CLAUDE.md). Contribuer : [`CONTRIBUTING.md`](CONTRIBUTING.md).

## L'interface (aperçu)

Onglets principaux (menu latéral uniquement sous Configuration) :

- **Dashboard** — compteurs, alertes, débit 24 h, ports par VLAN, événements
- **Équipements** — inventaire et façades de ports ; ajout via découverte, IP, série, catalogue ou procédures de reset
- **Ports** — tous les ports du site groupés par switch
- **Plan** — synoptique ; les cadres suivent les places ; liaisons avec débit bidirectionnel
- **Monitoring** — journal, état du parc, ports chargés
- **Configuration** — VLANs, profils, groupes, sites, utilisateurs, proxys

## Découverte et adoption

Un candidat découvert **n'a pas de site**. Une plage à scanner appartient au
proxy qui sait l'atteindre — le proxy **Local** est meshcraft elle-même. Le
site est choisi **à l'adoption** par un utilisateur `operator` sur ce site.

## Développement

```bash
make tidy         # go mod tidy (en conteneur)
make vet          # go vet
make test         # go test ./...
make fmt          # gofmt
make web-dev      # serveur Vite avec hot reload (API sur :8080)
```

## Site de démonstration

Importer [`samples/site-demo.json`](samples/site-demo.json) depuis la page parc
(festival fictif : switchs Generic, VLANs, profils, topologie).

## Structure du dépôt

```
cmd/controller/   contrôleur (API + SPA embarquée + registre OCI)
cmd/proxy/        proxy (enrollment + heartbeat + accès équipements)
cmd/mkoci/        layout OCI multi-arch du proxy sans buildx
internal/         api, auth, store (PostgreSQL), drivers, …
web/              SPA React + TypeScript (Vite), embarquée via go:embed
docs/fr, docs/en  vault d'architecture (bilingue)
```
