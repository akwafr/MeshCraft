---
tags: [study]
---

# State of the art — what exists, what we take from it

| Tool | What it does | What we keep / reject |
|---|---|---|
| **UniFi Controller** (Ubiquiti) | single-vendor controller: adoption, port profiles, groups, declarative provisioning, coloured-port UI | ✅ the reference UX model (adoption, profiles, [[Profiles and groups]], sites); ❌ vendor lock-in — meshcraft is its multi-vendor version |
| **Omada** (TP-Link), **Central/InstantOn** (Aruba), **Catalyst Center** | the same, each in its own world | confirms the need: every vendor has its controller, nobody bridges them |
| **LibreNMS / Observium** | multi-vendor SNMP monitoring, discovery, alerting | ✅ proof that multi-vendor SNMP works at scale; their MIB/OS detections are a goldmine of inspiration for the fingerprint ([[Discovery]]); ❌ monitoring only, no configuration |
| **NetBox / Nautobot** | source of truth (IPAM/DCIM), no execution | ✅ the "intended state in the database" idea; meshcraft is SoT **and** execution engine — for interop, a NetBox export/import is a plus (v2) |
| **NAPALM / netmiko / nornir** | multi-vendor Python drivers (normalised getters, config replace/merge) | ✅ proof of the "normalised multi-vendor model" concept; their getters/config split inspires the `Driver` interface ([[Connectors]]); not directly reusable (Python, cf. [[Technical stack]]) |
| **scrapli / scrapligo** | network CLI transport (prompts, paging) | ✅ scrapligo is our CLI transport building block |
| **Oxidized / RANCID** | multi-vendor config backup | ✅ the backup+diff of raw config function is taken over as is ([[Reconciliation]], `config_backups`); their models = a catalogue of CLI quirks per platform |
| **Unimus** | multi-vendor backup + command push, mass config | ✅ validates the "multi-vendor config" market; ❌ pushes *commands*, not a declarative state — meshcraft aims at intent |
| **Ansible (ios_*, junos_*)** | declarative config through playbooks | ❌ no real time, no UI, simulated state; useful as a reference for intent→CLI mapping per platform |
| **OpenConfig / gNMI** | normalised YANG model, streaming telemetry | ✅ inspiration for the [[Standard data model]], and now a real collector: `internal/gnmipoll` subscribes over gRPC (`openconfig/gnmic` library) and Arista EOS is collected by it ALONE, with no SNMP. ❌ the initial observation still holds for the rest of the fleet — gNMI remains the preserve of the high end, and the seven other connectors stay on SNMP. It really is a COLLECTION channel and not a connector: nothing is configured over gNMI here |
| **Weathermap (Network Weathermap, LibreNMS plugin)** | link load maps | ✅ the visual reference for the [[Weathermap]] page |

## meshcraft's niche

Nobody today combines: **multi-vendor + declarative configuration + UniFi-style adoption/profiles + integrated monitoring + remote proxies without VPN**. The building blocks exist separately (LibreNMS for monitoring, Unimus for pushing, NetBox for intent, UniFi for UX) — the value is in the integration, and particularly in the [[Connectors]] for entry/mid-range "smart switch" hardware (D-Link, TP-Link, Netgear, Zyxel) that every serious tool ignores because it is web-only ([[Web pass-through access]]).
