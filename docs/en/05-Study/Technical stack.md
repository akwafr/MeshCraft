---
tags: [study, stack]
---

# Technical stack — choices and justifications

Constraint set: **backend and proxy in Go**. What remains is deciding the frontend and the tooling.

## Go backend — accepted consequences

Go is an excellent choice here: a single binary (controller + embedded frontend), natural concurrency for thousands of sessions/tunnels, the same language as the proxy (sharing protocol code, models, framing — a `pkg/wire` module common to both binaries).

The point to know: the network automation ecosystem is historically **Python** (netmiko, NAPALM, nornir). In Go, the equivalent exists but is younger:

| Need | Go library chosen | Maturity |
|---|---|---|
| Network SSH/Telnet (prompts, paging, platforms) | **scrapligo** (Carl Montanari, same family as Python scrapli) | good, major platforms covered |
| Low-level SSH | `golang.org/x/crypto/ssh` | standard |
| SNMP | **gosnmp** | mature |
| CLI output parsing | Go port of TextFSM + ntc-templates, in-house regexes | adequate; templates will have to be written |
| HTTP API/scraping | net/http, resty, goquery | standard |
| WS + multiplexing | nhooyr/websocket + **hashicorp/yamux** | mature |
| Web framework | chi (routing) + oapi-codegen (OpenAPI → handlers + TS client) | mature |
| DB | pgx + sqlc, goose migrations | mature |

Practical consequence: the effort per [[Connectors|connector]] is a little higher than in Python (fewer ready-made parsers) — offset by the robustness of typing on the pivot model and the absence of a cross-language boundary.

## Frontend — decision: React + TypeScript as a PWA, not Flutter

Question asked: a reactive WebSocket interface, functional on mobile — Flutter?

An honest **evaluation of Flutter (web)**:
- ✅ a single codebase for web + native apps; very good for "product" mobile apps.
- ❌ Flutter *Web* renders into a canvas: heavy initial bundle (~1.5 MB+), text not selectable by default, non-native zoom/scroll, degraded accessibility and browser extensions;
- ❌ our UI is **dense and "document-like"**: tables, panels, hundreds of SVG faceplates ([[Port rendering]]), a pan/zoom graph ([[Weathermap]]) — exactly where native DOM/SVG excels and where the Flutter canvas forces reimplementing everything;
- ❌ visualisation ecosystem (React Flow, d3, list virtualisation) without a mature equivalent on the Flutter side;
- ❌ two languages in the project (Dart + TS for the remaining web tooling) for a small team.

**Choice: React 18 + TypeScript SPA, installable PWA.**
- **WS reactivity**: a single WS hook that patches the **TanStack Query** cache — the whole UI becomes real-time without parallel state ([[Web interface]]).
- **Mobile**: responsive + PWA (manifest, service worker, installable on the home screen, web push notifications). For an admin console, the PWA covers the real mobile need (consult, check a port, acknowledge) without an App Store pipeline.
- **Native SVG** for ports and topology: performance and rendering finesse, exactly the need.
- Tooling: Vite, Tailwind CSS, `@xyflow/react` for the weathermap, API types generated from the backend's OpenAPI (end-to-end contract).
- Alternative considered: **SvelteKit** (lighter, very good at reactivity) — ruled out solely for ecosystem size (React Flow, components, hiring). If a **native app** becomes a real need (v2+: rich notifications, enrolment QR scanning…), *mobile-only* Flutter on top of the existing API becomes a good option again — the web decision mortgages nothing.

## Summary

| Layer | Choice |
|---|---|
| Controller | Go 1.22+, chi + oapi-codegen, pgx/sqlc, yamux over WSS |
| Proxy | Go, same repo (monorepo, `cmd/controller`, `cmd/proxy`, shared `pkg/wire`) |
| Connectors | in-tree Go packages, scrapligo/gosnmp/goquery ([[Connectors]]) |
| Frontend | React + TS, Vite, Tailwind, TanStack Query, React Flow, PWA — embedded via go:embed |
| Database | PostgreSQL 16 ([[Database schema]]) |
| Delivery | controller binary + Docker image; proxy as binary/deb/rpm/multi-arch docker (GoReleaser) |
| CI | golangci-lint, connector transcript tests, vitest/playwright on the front end |
