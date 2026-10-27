---
tags: [study, risks]
---

# Hard points and risks

By severity × probability, with the countermeasure chosen.

## 1. Connector reliability IS the product
Parsing heterogeneous CLIs and web UIs is endless work; a wrong parse can produce a wrong diff, hence a wrong apply. **Countermeasures**: tests on real transcripts replayed in CI ([[Connectors]]); the diff is shown to the human before apply ([[Reconciliation]]); a "read-only" mode per connector until it is certified for writing; complete transcripts kept for diagnosis.

## 2. Cutting off your own hand (lockout)
Changing mgmt VLAN/IP/uplink remotely can isolate the device — and sometimes the whole site if it is the core switch. **Countermeasures**: hierarchy commit-confirm → archive-rollback → application rollback + reachability watchdog ([[Reconciliation]]); `disruptive` flag with reinforced confirmation; never an automatic replay of an interrupted apply; the on-site proxy survives the cut and can restore.

## 3. Web-only devices (webform)
Firmwares that change the HTML from one version to the next, home-made JS encryption, single admin session. **Countermeasures**: target by UI *family* rather than by model; honest degraded capabilities (the driver declares the subset it can do); [[Web pass-through access]] as a permanent escape hatch for the user; prioritise families with stable JSON endpoints ([[State of the art]]).

## 4. Monitoring volume
500 switches × 48 ports × 60 s polling = ~400k points/min at a naive peak. **Countermeasures**: aggregated getbulk on the [[Proxy]] side (one report per cycle, not one per OID); deltas only (a port that does not change produces no event); partitions + aggregates ([[Database schema]]); metrics interface ready for a switch to VictoriaMetrics.

## 5. Security of the controller→proxy chain
The controller can dial into customer networks: a prime target. **Countermeasures**: [[Proxy security]] (enrolment, revocation, local CIDR allowlist, ingestion quotas), [[Credentials]] (envelope, secrets never readable back), systematic audit ([[RBAC]]).

## 6. Telnet and SNMP v2c
Cleartext protocols, sometimes unavoidable on an old fleet. **Countermeasures**: v3/SSH preferred automatically when the capability exists; permanent, visible warning in the UI on every device on telnet/v2c; cleartext traffic stays confined to the proxy's local LAN (never on the WAN, the WSS tunnel encrypts everything).

## 7. LLDP heterogeneity
Fields filled unevenly (chassis-id MAC vs name, port-id ifName vs ifIndex). **Countermeasure**: multi-criteria correlation (MAC, sysname, mgmt-ip) with a displayed confidence level ([[Weathermap]]: solid vs dashed) — accept uncertainty rather than display something false.

## 8. The temptation of a distributed architecture too early
Microservices, message bus, multi-region… **Countermeasure**: Go monolith + Postgres until proven otherwise; the two real boundaries are already in place (`ProxyHandle` interface, `Driver` interface) and are enough to scale later ([[Controller]]).

## 9. Product scope
"Everything you can configure on a switch" is infinite (L3 ACLs, routing, QoS, stacking…). **Countermeasure**: the versioned [[Standard data model]] bounds the scope; the "absent = not managed" rule allows coexistence with the existing config; roadmap by feature families ([[Roadmap]]), not by vendor.
