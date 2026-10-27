---
tags: [moc, meshcraft]
---

# Meshcraft — Index (Map of Content)

> English is the primary version of this vault. A French version is kept alongside it: [French index](../fr/Meshcraft%20-%20Index.md) · [docs README](../README.md)

**Meshcraft** is a multi-vendor orchestrator for network switches and routers: a central controller that discovers, configures and monitors devices over SSH, Telnet or HTTP, directly or through **proxies** deployed in remote networks. Openly acknowledged inspiration: the UniFi controller, but **vendor-agnostic**.

## Guiding principles

1. **Intent-based**: the user describes a desired state (the [[Standard data model]]); the [[Connectors]] translate it into configuration specific to each platform.
2. **Modular**: each platform (Cisco IOS, Aruba, MikroTik, web-only D-Link…) is a pluggable connector that declares its capabilities.
3. **A single code path**: the controller embeds an internal proxy — a directly reachable device is treated exactly like a device behind a remote proxy ([[Proxy]]).
4. **Nothing inbound at the customer's**: proxies establish an **outbound** WebSocket connection to the controller ([[Controller-proxy protocol]]).
5. **Never cut off your own hand**: every configuration apply goes through a plan, a dry-run and a rollback mechanism ([[Reconciliation]]).

## Architecture

- [[Overview]] — overall diagram and main flows
- [[Controller]] — the central server and its modules
- [[Proxy]] — the agent deployed in remote networks
- [[Controller-proxy protocol]] — multiplexed WebSocket tunnel
- [[Proxy versioning and updates]] — build timestamp, controller-driven auto-upgrade
- [[Docker deployment and IPv6]] — the two compose files, systemd script, dual stack end to end
- [[Connectors]] — the per-platform drivers and their capabilities
- [[Standard data model]] — the pivot configuration format
- [[Switch profiles]] — rules that produce port configuration: faceplate selectors, first matching rule wins, port-by-port preview — and what becomes of the device group
- [[Management addressing]] — keep the addressing or apply the site plan, IPAM, management VLAN
- [[Discovery]] — automatic discovery over scan ranges, adoption with site choice
- [[Addressing domains]] — in which network an IP makes sense: deduplicating without confusing two LANs that share the same addressing plan
- [[Reconciliation]] — from desired to applied: diff, plan, apply, rollback
- [[Default VLAN]] — VLAN 1: present by default, undeletable, implicit native VLAN of trunks
- [[Monitoring]] — SNMP, traps, syslog, metrics
- [[Event log]] — the event log fed by monitoring
- [[Unified monitoring and pipelines]] — per-capability rates, proxy-side collection, VictoriaMetrics, visible pipelines and debug zone
- [[MikroTik telemetry]] — CSS326 and CRS326: three channels for RouterOS (REST, binary API, SNMP), page scraping for SwOS, health sensors and optical diagnostics, and what each platform will never publish

## Frontend

- [[Web interface]] — navigation, sites, real time
- [[Port rendering]] — visual specification of port cells (up/down, VLAN colours, PVID)
- [[Weathermap]] — LLDP topology and device placement
- [[LLDP neighbours]] — what devices see in front of them: survey, collection, derived links
- [[Profiles and groups]] — port and device profiles, inheritance

## Security & data

- [[RBAC]] — authentication, roles, per-site scope
- [[Credentials]] — encrypted storage of SSH/HTTP/SNMP credentials
- [[Proxy security]] — enrollment, rotation, least privilege
- [[Service identity and rotation]] — the accesses meshcraft installs itself, global policy overridable per site, secret generations
- [[SNMP identity]] — site v2c community, v3 identities per algorithm tag, negotiation at provisioning
- [[Tags and credential scope]] — tagging a fleet (by hand or by rules) and targeting a tag with a specific credential
- [[Database schema]] — main tables

## In-depth study

- [[Technical stack]] — Python / Go / React / PostgreSQL choices and alternatives
- [[State of the art]] — UniFi, LibreNMS, NetBox, NAPALM… what we take from them
- [[Web pass-through access]] — driving "web only" devices through the proxy
- [[Hard points and risks]] — the real problems to anticipate

## Decisions

- [[Decision index]] — the Architecture Decision Records: format, rules, and the full table with statuses
- [[ADR-0001 — Two themes, and the status palette that must survive both|ADR-0001]] — Two themes
- [[ADR-0002 — Glass is chrome, never content|ADR-0002]] — Material
- [[ADR-0003 — The traffic ramp is logarithmic and relative to capacity|ADR-0003]] — Traffic ramp
- [[ADR-0004 — Junos provisioning without commit confirmed|ADR-0004]] — Junos
- [[ADR-0005 — Brand then model picker for serial procedures|ADR-0005]] — Serial picker
- [[ADR-0006 — Freshness is measured on the server clock|ADR-0006]] — Clock
- [[ADR-0007 — The aggregate is a port on the faceplate|ADR-0007]] — Aggregates
- [[ADR-0008 — The time reference is the viewer's clock|ADR-0008]] — Clock
- [[ADR-0009 — A swipe never steals a scroll gesture|ADR-0009]] — Swipe navigation
- [[ADR-0010 — The reset screen lists everything, and the brand is a field|ADR-0010]] — Reset list
- [[ADR-0011 — A token authenticates, it does not authorise|ADR-0011]] — API tokens
- [[ADR-0012 — The tool surface is not the API surface|ADR-0012]] — MCP server
- [[ADR-0013 — Switching and routing are two navigation modes, not two products|ADR-0013]] — Navigation modes
- [[ADR-0014 — The declared label is evidence, identity is derived|ADR-0014]] — Model identity
- [[ADR-0015 — The site extension lives in the controller, and its inventory is a read-only mirror|ADR-0015]] — Site extensions
- [[ADR-0016 — A Wi-Fi access point is a device, but it only shows in Wi-Fi and on the topology|ADR-0016]] — Wi-Fi model
- [[ADR-0017 — Rejecting a short read belongs to absorption|ADR-0017]] — Short read
- [[ADR-0018 — A port's name at write time is an observation, not a constant|ADR-0018]] — Port naming
- [[ADR-0019 — The switcher carries only the brand, the mode reads in the bar|ADR-0019]] — Mode switcher
- [[ADR-0020 — A storm limiter we cannot read is reported, not silenced|ADR-0020]] — Storm control
- [[ADR-0021 — An MST region name is an identity, not a label|ADR-0021]] — MSTP region
- [[ADR-0022 — Joining an MST region is a declared capability, not a consequence of "can write STP"|ADR-0022]] — MSTP region
- [[ADR-0023 — Spanning-tree configuration is sent only on a gesture, and drift reads in a banner|ADR-0023]] — STP writes
- [[ADR-0024 — An alert is earned - the log decides, the description and the interconnect qualify|ADR-0024]] — Alerting
- [[ADR-0025 — meshcraft remote-controls the UniFi controller, it holds no intent|ADR-0025]] — UniFi gestures
- [[ADR-0026 — A topology link has two halves, each coloured by its end's egress|ADR-0026]] — Link rendering
- [[ADR-0027 — A tile's title is data, its target is an unguaranteed reference|ADR-0027]] — Dashboard tiles
- [[ADR-0028 — Per-class traffic breakdown is a declared capability, and an undeclared class says so on screen|ADR-0028]] — Per-port monitoring
- [[ADR-0029 — The SSID is editable, the access point is not, and only what reads back is offered|ADR-0029]] — Wi-Fi editing
- [[ADR-0030 — The traffic ramp has one colour engine, and the SVG gradient samples it|ADR-0030]] — Traffic ramp
- [[ADR-0031 — The mode switcher is a toggle, and the brand is no longer a door|ADR-0031]] — Mode switcher
- [[ADR-0032 — A tile may declare a live zone in edit mode|ADR-0032]] — Dashboard grid
- [[ADR-80600 — Ctrl+F searches the site, the second press hands back to the browser|ADR-80600]] — Global search
- [[ADR-74818 — An ADR number is drawn at random, not taken as the next one|ADR-74818]] — ADR numbering
- [[ADR-46584 — Identity is an icon, and sign-out lives behind it|ADR-46584]] — Account menu
- [[ADR-73885 — The page background takes the mode's tint|ADR-73885]] — Per-mode background

## Delivery

- [[Roadmap]] — phases MVP → v1 → v2
- [[Release notes]] — what each version brings, and what it leaves open
