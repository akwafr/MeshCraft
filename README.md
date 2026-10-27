# meshcraft

[Français](README.fr.md)

Multi-vendor network switch and router orchestrator: a central controller that
discovers, configures, and monitors devices over SSH/Telnet/HTTP — directly or
through **proxies** deployed in remote networks.

**License:** [AGPL-3.0-only](LICENSE)

## Origin

The need for a unified way to manage a network grew out of **LAN parties** and
associative events where we only had **heterogeneous hardware** — different
vendors, different ages, different management interfaces, and no time to learn
each one from scratch before doors open.

meshcraft was born in the context of projects around
[Lyon e-Sport](https://lyon-esport.fr/). Huge thanks to **DrumSlayer** and
**Emma-0**.

## Quick start

Prerequisites: Docker (+ Compose plugin). **Everything builds in containers.**

```bash
cp .env.example .env   # public domain, ACME contact, admin account
make build             # Vite frontend + Go + multi-arch proxies + proxy OCI image
make up                # controller + PostgreSQL + Caddy (TLS)
make logs              # initial admin password is printed on first start
```

Initial account: `admin@meshcraft.local` (password: `MESHCRAFT_ADMIN_PASSWORD`,
or generated and logged once).

### Public HTTPS

The `caddy` Compose service terminates TLS and obtains/renews **Let's Encrypt**
certificates (ACME HTTP-01) for `MESHCRAFT_DOMAIN`. The domain must point at
the machine and ports 80/443 must be reachable. The controller is only bound
on `127.0.0.1:8080`.

With a valid HTTPS front, `docker pull <domain>/meshcraft/proxy:latest` works
without `insecure-registries`. Locally without a domain, keep
`MESHCRAFT_DOMAIN=localhost` and use http://localhost:8080.

## Screenshots

Captured on the [demo site](#demo-site) (fictional festival, generic switches,
synthetic telemetry).

**Dashboard** — fleet counters, 24 h site traffic, ports by VLAN, active alerts and recent events.

![Dashboard](docs/screenshots/dashboard.png)

**Monitoring** — fleet status with reachability durations, most loaded ports, event log.

![Monitoring](docs/screenshots/monitoring.png)

**Equipment** — one card per switch: port strip coloured by VLAN, throughput, PoE, state.

![Equipment](docs/screenshots/devices.png)

**Switch detail** — the port faceplate, coloured by VLAN, with the switch summary.

![Switch detail](docs/screenshots/switch-detail.png)

**Ports** — every port of the site, grouped by switch, with VLAN, link, traffic, PoE and neighbour.

![Ports](docs/screenshots/ports.png)

**Topology** — the site plan: location frames, link speeds, bidirectional rates and link diagnostics.

![Topology](docs/screenshots/topology.png)

**Switch profiles** — rules that turn a switch's faceplate into port configuration, previewed on a real device before anything is written.

![Switch profiles](docs/screenshots/switch-profiles.png)

**Port profiles** — reusable port settings next to the site's VLAN plan.

![Port profiles](docs/screenshots/port-profiles.png)

**Dark theme** — the same topology, in the dark palette.

![Topology, dark theme](docs/screenshots/topology-dark.png)

## Documentation

Full design lives in the Obsidian vault:

- English (primary): [`docs/en/`](docs/en/) — entry point [`docs/en/Index.md`](docs/en/Index.md)
- French (translation): [`docs/fr/`](docs/fr/)

Contribution guidelines and code conventions: [`CONTRIBUTING.md`](CONTRIBUTING.md).

## Interface (overview)

Main tabs (side menu only under Configuration):

- **Dashboard** — counters, active alerts, 24 h aggregate throughput, ports by VLAN, recent events
- **Equipment** — inventory with per-switch port faceplates; add via discovery, manual IP, serial, catalogue template, or factory-reset procedures
- **Ports** — all site ports grouped by switch
- **Topology** — full-page site plan; dotted frames follow device placement; links show bidirectional throughput
- **Monitoring** — a dashboard of its own: event log, fleet status, busiest ports
- **Configuration** — VLANs, profiles, groups, sites, users, proxies

## Supported platforms

Nine connectors, each written against one platform's own management interface —
there is no generic SNMP fallback that pretends to configure a switch it does not
know.

| Connector | Range | Reached over | Telemetry | Verified on hardware |
|---|---|---|---|---|
| `arista-eos` | Arista EOS (7000 / 720 / 750) | SSH, serial | **gNMI streaming** | reads + telemetry |
| `cisco-ios` | Catalyst 2960 / 2960-X / 2960-XR (IOS 12.2–15.2) | SSH, serial, Telnet | SNMP + traps | yes |
| `aruba-instant-on-1930` | Aruba (HPE) Instant On 1930 | web (`wcd`), SNMP | SNMP + web + traps | yes |
| `mikrotik-routeros` | MikroTik RouterOS (CRS…) | binary API, SSH, Telnet, serial, SNMP | API + SNMP + web, traps | yes |
| `mikrotik-swos` | MikroTik SwOS (CSS…) | web, SNMP | SNMP + web | yes |
| `ubiquiti-edgeswitch` | EdgeSwitch / UniFi Switch (FASTPATH) | SSH, serial, HTTP | SNMP | yes |
| `fs-s3400` | FS.com S3400 PoE+ (BDCOM-style CLI) | SSH, serial, HTTP | SNMP | yes |
| `fs-s3900` | FS.com S3900 / S3900-R | SSH, serial, HTTP | SNMP | yes |
| `hpe-procurve` | HPE ProCurve 2600 | SSH, Telnet, web, SNMP | SNMP | **parked** |

**Arista is the only range collected by streaming telemetry** and the only one
with no SNMP channel at all: `internal/gnmipoll` subscribes over gNMI/gRPC for
interface state, counters, LLDP and health. Everything else is polled by
`internal/snmppoll` from the proxy.

### What "verified" means here, and what it does not

The last column says a real device answered — not that every gesture has been
run on one. The project's rule is that a connector ships a command form only
once it has been seen, and declares the WRITE (`WritesExtra`) only once that
particular write has been exercised, because a declared write puts a button on
screen whose first use would be on somebody's production switch.

So today: eight ranges declare per-port administrative writes and six declare
spanning-tree writes, while **no connector declares DHCP snooping** — all nine
carry the code and none has a capture. The Arista connector's telemetry half is
captured (EOS 4.28.13.1M) and its configuration writes are not. Each remaining
declaration is waiting on a capture of that particular write on real hardware.

ProCurve 2600 is **parked**: the connector exists, the lab unit authenticates
and then refuses the channel past four concurrent sessions, and it is not to be
picked up without an explicit decision.

## Discovery and adoption

A discovered candidate has **no site**. A scan range belongs to the proxy that
can reach it — the **Local** proxy is meshcraft itself. The site is chosen **at
adoption** by a user with the `operator` role on that site.

## Development

```bash
make tidy         # go mod tidy (in container)
make vet          # go vet
make test         # go test ./...
make fmt          # gofmt
make web-dev      # Vite dev server with hot reload (API on :8080)
```

## Demo site

Import [`samples/site-demo.json`](samples/site-demo.json) from the fleet page
(fictional festival: generic switches, VLANs, port profiles, topology).

## Repository layout

```
cmd/controller/   controller (API + embedded SPA + OCI registry)
cmd/proxy/        proxy (enrollment + heartbeat + device access)
cmd/mkoci/        multi-arch OCI layout for the proxy without buildx
internal/         api, auth, store (PostgreSQL), drivers, …
web/              React + TypeScript SPA (Vite), embedded via go:embed
docs/en, docs/fr  architecture vault (English primary, French translation)
```
