---
tags: [security]
---

# RBAC

## Status (summary)

| Topic | Status |
|-------|--------|
| Site roles `viewer` / `operator` / `site_admin` + `is_global_admin` | **shipped** |
| Local passwords argon2id | **shipped** |
| Opaque session cookie; only SHA-256 stored | **shipped** |
| Per-route site checks | **shipped** (hand-written, not a single middleware) |
| Proxy visibility / admin matrix | **shipped** |
| Adoption requires `operator` on chosen site | **shipped** |
| TOTP 2FA, OIDC, API PATs | decided, not built |

## Roles

| Role | Rights |
|------|--------|
| **viewer** | read devices, topology, journal, profiles |
| **operator** | viewer + edit intents/profiles, deploy, adopt |
| **site_admin** | operator + VLANs, site management plan, site credentials, site users, deploy proxies covering their sites |
| **global-admin** | everything, all sites, instance proxies, connectors, instance settings |

Realtime UI stream is **SSE** (`GET /api/sites/{id}/stream`), reusing the same
session middleware as HTTP routes.

French deep dive: [RBAC](../../fr/03-Sécurité/RBAC.md).
