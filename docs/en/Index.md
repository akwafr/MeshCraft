# Documentation index (English)

**meshcraft** is a multi-vendor switch and router orchestrator: a central
controller discovers, configures, and monitors devices over SSH, Telnet, or
HTTP — directly or through **proxies** in remote networks. Inspiration:
UniFi-style control, but **vendor-agnostic**.

French vault (full depth, Obsidian wikilinks): [`../fr/Meshcraft - Index.md`](../fr/Meshcraft%20-%20Index.md).

## Guiding principles

1. **Intent-based** — describe desired state; connectors translate per platform.
2. **Modular** — each platform is a pluggable connector with declared capabilities.
3. **One code path** — the controller embeds an internal proxy; local devices use the same path as remote ones.
4. **No inbound holes at the client** — proxies open an **outbound** WebSocket to the controller.
5. **Never cut your hand off** — apply goes through plan, dry-run, and rollback.

## Architecture (English notes)

| Topic | English | French (canonical depth) |
|-------|---------|---------------------------|
| Overview | [Overview.md](01-Architecture/Overview.md) | [Vue d'ensemble](../fr/01-Architecture/Vue%20d'ensemble.md) |
| Controller | [Controller.md](01-Architecture/Controller.md) | [Contrôleur](../fr/01-Architecture/Contrôleur.md) |
| Proxy | [Proxy.md](01-Architecture/Proxy.md) | [Proxy](../fr/01-Architecture/Proxy.md) |
| Discovery | [Discovery.md](01-Architecture/Discovery.md) | [Discovery](../fr/01-Architecture/Discovery.md) |
| Credentials | [Credentials.md](03-Security/Credentials.md) | [Credentials](../fr/03-Sécurité/Credentials.md) |
| RBAC | [RBAC.md](03-Security/RBAC.md) | [RBAC](../fr/03-Sécurité/RBAC.md) |

Other topics (Roadmap, Monitoring, LLDP, Docker deploy, …) remain fully documented in French under [`../fr/`](../fr/). English stubs may be added over time; until then, use the French pages.
