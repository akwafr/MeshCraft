---
tags: [roadmap]
---

# Roadmap

## Phase 0 — Foundations (the skeleton that walks)
- Go monorepo (`cmd/controller`, `cmd/proxy`, `pkg/wire`), CI, migrations, minimal [[Database schema]].
- WSS + yamux tunnel end to end: the proxy enrols, `DialTCP` works through it ([[Controller-proxy protocol]], [[Proxy security]]) — **shipped**: probing and sweeping from a remote proxy go through the tunnel, HTTP included.
- Controller's internal proxy (same interface) ([[Controller]]).
- [[Proxy versioning and updates]]: build timestamp, offer in the heartbeat, download verified by SHA-256, `exec`, anti-crash-loop bootstrap — **shipped**.
- Local auth + sites + minimal [[RBAC]] (viewer/admin), embedded React SPA, UI WS.

**Exit criterion: from the UI, open a tunnelled SSH session to a lab device behind the proxy.**

## Phase 1 — MVP "see your fleet"
- [[Discovery]]: scan ranges per proxy (including the "Local" proxy), scan, SNMP/SSH/HTTP fingerprint, site-less candidates, **adoption with choice of site**, [[Credentials]] (encrypted vault, instance standard credentials, ordered trials).
- Manual add by IP and by **serial port** (Web Serial), same fingerprint pipeline.
- At adoption: choice between configuration **pushed by the platform** and **import of the local configuration** (ports + VLANs).
- SNMP polling of port states + counters, run by the proxy ([[Monitoring]]) — **shipped**, with `port.up` / `port.down` transitions diffed on `ifOperStatus` by the proxy and flap correlation (`port.flapping`, `port.flap_cleared`) at the controller ([[Event log]]).
- SNMP traps received by the proxy on 162/udp — **shipped**, and shipped as a **polling accelerator**: a trap brings forward the poll that would have happened anyway, it writes no state. What changes is detection latency (poll cadence → ~1 s), not correctness; the channel switches off without breaking anything.
- **One-off questions to the proxy** (`ask`, the tunnel's third exchange pattern) and **diagnostic SNMP walk** (`POST /api/devices/{id}/snmp-walk`, `global-admin`, read-only, no screen) — **shipped** ([[Controller-proxy protocol]], § *One-off questions*). What it changes: the OIDs a device **actually** serves are surveyed instead of guessed from a vendor MIB — for SNMP, the same "read-only" half of the path that the `web-probe` probe opened for web connectors. Three clarifications, because "we can discover" is not "it is discovered":
    - the **discovery of the optical power OIDs on the 1930 is not done**, and it depends on **redeploying the proxy**: a binary already running does not know the question and closes the stream without a word. The controller can say so ("upgrade it"); it cannot answer in its place;
    - the sensor collector, on the other hand, **is now shipped**: `internal/snmppoll` knows four sets — `if_state`, `if_counters`, and the two sets added by [[MikroTik telemetry]], `health` (`sysUpTime`, `hrProcessorLoad`, `hrStorage`, the `mtxrHealth` branch) and `optical_power` (`mtxrOpticalTable`). The statement that stood here — "no optical power collector, and there never was one" — is therefore no longer true **on MikroTiks**, and remains so on the 1930: that range does not expose it ([[Connectors]], § *SFP+ optical power*). Two limits named rather than masked: the sensor foundation is **proprietary** (there is no standard switch health MIB), so sensors are only turned on where a connector declares an `snmp` channel for them; and the **scale factors** of `mtxrHealth` were *deduced* from two reference collectors: the one for **temperatures** is now SURVEYED on hardware (69 °C on the web versus 690 over SNMP at the same instant on the CSS326), those for voltages and fans remain deduced for lack of a device that publishes them. The same survey uncovered a REAL sign defect — an empty SFP cage returns -128 °C encoded in two's complement in a `Gauge32`, hence published as 429,496,716 °C — fixed, and which also distorted every optical power in the fleet, negative by nature;
    - this page announced that the **5 s** traffic cadence "would also apply" to optical power, "same nature of data". **That is not what was shipped**, and the reason is cost rather than the nature of the curve: a transceiver's diagnostic registers are read over the module's **I²C bus**, the slowest on the board, and an optical budget drifts over weeks. The cadence chosen is **60 s** (`wantedOpticalInterval`), health being at **5 min** — the value the design had carried from the start ([[Monitoring]] §1) without anything reading it. What remains true of the original sentence: an optical cut is visible within a second, but it is **already** covered by port transitions, which are real time; this series does not have to detect it, only to explain it afterwards.

    The contract's second question, `source_addr`, is shipped too: it returns the address the proxy's kernel **would use** to reach a device, a fact no address inventory can deduce as soon as there is routing (real survey: proxy on `10.42.0.67/24` and `172.17.0.1/16`, device on `198.51.100.101`, no match and yet reachable). No API route calls it yet; what is done with it belongs to the trap recipient batch, tracked in phase 2.
- Syslog received by the proxy — **not shipped**, and not "half done": no 514/udp listener exists, so no line of the [[Event log]] comes from it. Only the ground is ready (`network_mode: host` + `NET_BIND_SERVICE` set by `internal/distrib`). Keeping it on the same line as polling and traps made what has not started read as acquired — precisely the error the delivery order (§7 of [[Unified monitoring and pipelines]]) exists to avoid.
- Devices page with real-time [[Port rendering]], [[Event log]], device record.
- **Read-only** connector: 2 CLI platforms (e.g. Cisco IOS L2 + Aruba/ProCurve) — ReadState, config backup, LLDP.

**Exit criterion: a real mixed fleet visible, ports coloured by up-to-date VLAN, live log.**

## Phase 2 — MVP "configure"
- Complete [[Standard data model]] v1 + site VLANs + [[Profiles and groups]].
- [[Reconciliation]]: compile/diff/plan/apply with preview, reachability watchdog, backups before apply.
- [[Management addressing]]: plan per site (administration VLAN, subnet, external DHCP or meshcraft IPAM), *keep* / *site plan* modes at adoption, management VLAN capability and argued refusal for devices that cannot carry it. The addressing cutover is the first truly dangerous apply: it arrives with the watchdog, not before. The **per-device addressing mode** (DHCP or fixed address, with gateway and DNS) is shipped ahead of that, and without risk, because it deliberately stops where the danger begins: it records and displays an intent, it does not write it to the device.
- Writing on the 2 CLI connectors.
- Automatic deployment of the collection configuration, to be read in **two halves** because they are not in the same state:
    - setting the **SNMP identity** on the device is **shipped** (`internal/api/snmpapply.go`, at provisioning, with verification read-back): meshcraft can already give itself the means to *poll* the fleet;
    - declaring a **trap recipient** in the device — the return path — is still **not done**, but it is no longer **blocked upstream**. What was missing was a concrete IP to write; `devices.proxy_id` now provides it by designating a device's **reference collector** — **shipped**, migration `0022` ([[Proxy]], § *The reference collector: a DECLARED attachment*). The **election** of a "preferred collector" among the eligible proxies remains **decided and not built** ("Choice of collection point" section of [[Monitoring]]), but it is no longer a prerequisite: the declaration delivers the missing building block, without the automatic recomputation — hence without the risk of seeing a collector change on its own after its address has been written into the fleet. What remained was no longer a design question but an implementation batch, and **it is shipped**: `internal/api/trapdest.go` derives the address by querying the collecting proxy, `instanton.ApplyTrapReceiver` writes the `wcd` section surveyed in [[Connectors]] then **reads back and compares field by field**, the write happens at provisioning and is replayed through `POST /api/devices/{id}/trap-apply`, and migration `0023` separates the **claim** from the **proof**. Proven on hardware: the destination was written into a real 1930, then **verified** by a trap received at the very address written there.

    Two limits remain, and they are not delivery delays. Writing assumes an **attachment in place**: a device left in the `null` regime has no single address to receive, and the write refuses it, saying so. And it only writes **v2c**: the proxy's collector cannot receive v3 traps, so setting a v3 recipient would produce packets dropped on arrival — a device on v3 is today neither polled nor listened to;
    - there is, on the other hand, **nothing to deploy for syslog**: you do not configure a fleet to send to a collector that does not exist.
- Setting the trap recipient on the Instant On 1930 family: **the knowledge is ready, the write remains to be done.** The survey on `sw-labo-1930` is documented in [[Connectors]] ("SNMP trap recipients") and it was done **read-only**: two distinct `wcd` sections depending on the version (`SNMPTrap` for v1/v2c, `NotificationReceiverList` for v3), the codes of all fields, two form defects not to copy, and the fact that link traps are already active on the 68 interfaces — it is enough to declare a recipient. **One explicit unknown remains, not determinable read-only**: the **number of simultaneous recipients** the firmware accepts, which is only enforced at the moment of adding. It is what decides whether telemetry survives the loss of a proxy; the "several recipients" capability therefore stays `false` until adding has been pushed to refusal on hardware. Same discipline as the common thread: this batch does not open writing on the strength of a survey, it opens it on transcripts — read, write, **read back and compare field by field**, then declare the capability.
- [[Service identity and rotation]]: instance policy overridable per site, CLI service account and SNMP v3 user pushed, **secret generations** with add → verify → retire, traced emergency access. It is the first feature in which meshcraft holds the customer's access: it cannot arrive after configuration writing, it arrives with it.
- [[Weathermap]] v1: LLDP, manual placement, links up/down. **LLDP collection and derived links are shipped** (29/07/2026, [[LLDP neighbours]]); the adoption of a ghost neighbour and the rendering of aggregates remain.

**Exit criterion: change the VLAN of 50 ports through a profile, deployed without lockout, drift detected.**

## Phase 3 — Multi-vendor for real
- Webform building block + 1st web-only connector (D-Link DGS-1210 or Zyxel GS1900 family) ([[Web pass-through access]] included).
- MikroTik connector (API) + 1 modern REST connector (AOS-CX). **MikroTik's binary API is shipped ahead of phase** — protocol, two session openings, telemetry — because it is the only channel that returns a link's negotiated rate and an SFP's optical diagnostics on RouterOS ([[MikroTik telemetry]]). It has **never exchanged a byte with hardware**: it is proven on a simulated device, and its certification follows the common thread like the others.
- Streaming telemetry (gNMI/gRPC) on the platforms that support it: service certificates with dated expiry, automatic renewal at two thirds of the lifetime ([[Service identity and rotation]]).
- Simple alerting (email/webhook).
- OIDC, full roles, finished mobile PWA ([[Web interface]]).

## Phase 4 — Comfort & scale
- Weathermap with link load (real weathermap), background plan, NOC mode.
- Drift auto-absorption, deployment scheduling (maintenance windows).
- NetBox import/export, documented public API, possible gNMI connector ([[State of the art]]).
- Multi-instance controller if needed ([[Hard points and risks]] §8).

## Permanent common thread
Every new connector follows the same path: fingerprint → ReadState → backup → LLDP → **read certification** → Plan/Apply → **write certification**; never writing opened without test transcripts replayed in CI ([[Connectors]]).

Corollary, learned on the 1930's trap recipients: a read-only survey is the **first half** of the path, never the certification. This first half now has one tool per transport family — `web-probe` for the web, the `ask` walk for SNMP ([[Controller-proxy protocol]]) — and both are read-only **by construction**: neither of them will open writing, which remains the connectors' path. A limit only a write would reveal — a table ceiling, a field no page of the interface writes — leaves the corresponding capability at `false`: announcing redundancy you have not proven costs the outage at the exact moment you were counting on it.
