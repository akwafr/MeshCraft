---
tags: [architecture, discovery]
---

# Discovery

Operators declare **scan ranges** on a [Proxy](Proxy.md) (or the controller’s
Local proxy for directly reachable networks). The pipeline finds devices,
identifies them, and queues them for **adoption** (UniFi-style).

> [!important] A candidate belongs to no one
> A scan range belongs to the proxy that can reach it, **not to a site**.
> Devices can be discovered from any proxy. The site is chosen **at adoption**
> by a user with `operator` on that site.

## Pipeline (target)

1. **Sweep** — ARP/ICMP target; today TCP probes via controller + tunnelled dial.
2. **Probe** — characteristic ports 22/23/80/443/8080/8443 (**shipped**).
3. **Fingerprint** — SSH banner + HTTP headers/title (**shipped**); SNMP
   sysObjectID still to wire into discovery.
4. **Match** — pattern table in `internal/discovery/fingerprint.go` (**shipped**);
   connector enrichment afterwards.
5. **Adoption** — choose site, optional config import vs push, SNMP provisioning
   (**shipped**). Full credential apply and `ReadState` remain separate API gestures.

French deep dive: [Discovery](../../fr/01-Architecture/Discovery.md).
