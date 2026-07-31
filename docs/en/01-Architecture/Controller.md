---
tags: [architecture, controller]
---

# Controller

Central server: a **modular Go monolith** (not microservices). One binary serves
the API, the embedded SPA (`go:embed`), proxy tunnels, and orchestration.
Deployed as a container or bare binary.

## Real package layout (today)

| Concern | Package / location | Status |
|---------|-------------------|--------|
| HTTP API | `internal/api` (chi) | shipped |
| Auth | `internal/auth` + `internal/secretbox` | shipped; OIDC open |
| Inventory | `internal/store` + API handlers | shipped, not extracted |
| Port / switch profiles | `internal/portrules`, `internal/siteconfig` | shipped |
| Diff / apply | `internal/recon` + `internal/api/autoapply.go` | partial |
| Drivers | `internal/driver`, `internal/driver/instanton`, MikroTik, … | shipped (growing) |
| Tunnels | `internal/tunnel` | shipped |
| Discovery | `internal/discovery` | shipped |
| Monitoring | `internal/snmppoll` (proxy) + API watch handlers + `internal/tsdb` | SNMP/traps shipped; syslog open |
| Events | `internal/store/events.go` + API | shipped |
| Credentials | `secretbox`, store sitecreds, `pwgen`, `tagging` | shipped |
| Scheduler | none yet | open |
| Store | `internal/store` (pgx) + numbered SQL migrations | shipped |
| SPA | `web/` embedded via `internal/api/spa.go` | shipped |

Entry point: `cmd/controller/main.go`.

French deep dive: [Contrôleur](../../fr/01-Architecture/Contrôleur.md).
