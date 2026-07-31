---
tags: [architecture]
---

# Overview

## Target architecture

> [!note] This diagram is the target, not the live state
> It describes the decided architecture. Some edges are not yet in the code —
> see the status table below.

```mermaid
flowchart TB
    subgraph Internet
        U[User browser]
    end

    subgraph DC["Controller (datacenter / cloud)"]
        FE[React SPA]
        API[REST API + WebSocket/SSE]
        ORCH[Orchestrator<br/>intent → plan]
        CONN[Connector registry<br/>SSH / Telnet / HTTP drivers]
        TM[Tunnel manager<br/>proxy sessions]
        ING[Event ingestion<br/>traps, syslog, polling]
        IPX[Internal proxy<br/>same API as remote proxies]
        DB[(PostgreSQL)]
    end

    subgraph SiteA["Client site A"]
        PA[meshcraft proxy<br/>Go binary]
        SW1[Cisco switch - SSH]
        SW2[D-Link switch - HTTP only]
    end

    subgraph SiteB["Controller LAN"]
        SW3[Aruba switch - SSH]
    end

    U -->|HTTPS| FE
    FE --> API
    API --> ORCH --> CONN
    CONN -->|tunnelled TCP streams| TM
    TM <-->|outbound multiplexed WSS| PA
    PA -->|SSH 22| SW1
    PA -->|HTTP 80/443| SW2
    SW1 -.->|syslog/traps UDP| PA
    SW2 -.->|syslog/traps UDP| PA
    PA -->|events| ING
    CONN --> IPX -->|direct SSH| SW3
    SW3 -.->|syslog/traps| IPX --> ING
    ING --> DB
    ORCH --> DB
```

| Edge | Status |
|------|--------|
| `U → FE → API`, `API → DB` | **shipped** |
| `TM ↔ PA` (outbound multiplexed WSS) | **shipped** — yamux over WebSocket, `internal/tunnel` |
| HTTP via proxy | **shipped** — Instant On connector path |
| SSH via proxy | **shipped for discovery** — full CLI connectors still growing |
| SNMP traps UDP | **shipped** — 162/udp on the proxy |
| Syslog UDP | **decided, not built** |
| Internal “Local” proxy | **partial** — direct TCP dial yes; local UDP watch not fully equivalent |
| Distinct `ORCH` module | **not under that name** — read/diff/apply lives in `internal/api` + `internal/recon` |

## Where work runs

The proxy is intentionally **dumb**: it knows no vendors. Intelligence (drivers, parsing, diff) stays on the controller. Exchange patterns:

- **Tunnelled TCP (`dial`)** — driver runs on the controller; raw TCP flows through the WebSocket tunnel. One implementation for local and remote. **Shipped.**
- **Watch config (`watch`)** — controller pushes lists and intervals; proxy loops and reports **transitions**. ICMP, SNMP poll, continuous scan, trap listen. **Shipped.**
- **One-shot ask (`ask`)** — closed set of read-only questions (`snmp_walk`, `source_addr`). No SNMP `set` on this path; writes go through connectors. **Shipped.**

French deep dive: [Vue d'ensemble](../../fr/01-Architecture/Vue%20d'ensemble.md).
