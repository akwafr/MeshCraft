---
tags: [architecture, proxy]
---

# Proxy

Agent installed by the user on a remote network. Single **static Go binary**
(amd64/arm64, Docker image): minimal footprint (VM, Pi, or container).

## Roles

1. **Transport gateway** — open TCP to devices (SSH, Telnet, HTTP/S) and relay
   multiplexed streams to the controller. The proxy does not speak SSH; it
   copies bytes.
2. **Local UDP / scan executor** — what cannot be tunnelled as TCP runs on site:
   mass SNMP polling, read-only `snmp_walk`, source-address query for trap
   targeting, discovery-related sweeps. **No `snmp.set`** — writes go through
   connectors with plan / dry-run / rollback.
3. **Telemetry receiver** — SNMP traps (162/udp) shipped; syslog (514/udp) planned.
4. **Web pass-through** — study / future: relay browser sessions to device UIs.

## Lifecycle

- **Enrollment** — one-time token from the UI (site coverage) exchanged for a
  durable shared secret; outbound WSS only (NAT-friendly).
- **Disconnect** — exponential backoff reconnect; metric buffers are bounded.
- **Update** — controller-driven upgrade with SHA-256 integrity check (signing
  is a target; hash alone is not origin authenticity).

## Site coverage

- **Instance proxy (`global`)** — usable by all sites; administered by
  `global-admin` only.
- **Restricted proxy** — limited to its coverage sites.
- **Empty coverage means nowhere**, not everywhere (avoids privilege escalation
  when the last site is removed).

French deep dive: [Proxy](../../fr/01-Architecture/Proxy.md).
