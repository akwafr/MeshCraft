---
tags: [architecture, model]
---

# Standard data model (the intent)

The system's pivot format: **everything that can be configured on a switch**, expressed in a vendor-agnostic way. It is the input of the [[Connectors]] (writing) and their output (normalised reading). Inspiration: OpenConfig, much simpler — we only model what meshcraft actually manages, and extend the schema version after version.

> [!warning] This document describes TWO things, and only one is built
> There are in reality **two pivots** in meshcraft, and confusing them is the
> mistake this page invited:
>
> - the **site exchange format** `meshcraft_site_config` — **shipped**,
>   `internal/siteconfig`, exportable and importable, with its full example in
>   `samples/site-demo.json`. It is the pivot contract on the IT-systems side,
>   described below;
> - the **per-device intent** in YAML described just below — **target**. It has
>   no schema, no Go structure, no table: `schema_version`, `services`,
>   `management`, `cred://` and `device_intents` are nowhere in the code. What
>   plays its role today is a much narrower `jsonb`, `devices.ports`, whose actual
>   content is given in § *What is actually stored*.
>
> The YAML stays here, and it must be kept: it is the only description of what the
> model will have to carry when a second platform arrives, and it is from it that
> the batching of work will be derived. Reading it as a format in place sends
> people looking for a parser, a schema and a revisions table that do not exist.

## Structure — the TARGET

```yaml
schema_version: 1
device:
  hostname: sw-atelier-01
  management:                        # see Management addressing
    mode: site_plan                 # site_plan | keep (keep = absent fields,
                                    # hence "unmanaged": nothing is rewritten)
    vlan: 10
    vlan_untagged: false            # management on native VLAN (case of
                                    # devices that only accept VLAN 1)
    ipv4: {mode: static, address: 10.0.10.5/24, gateway: 10.0.10.1}   # or mode: dhcp
    dns: [10.0.10.1]
    ntp: [10.0.10.1]
    ssh: {enabled: true}
    http: {enabled: false}          # web UI is turned off when SSH is enough
    telnet: {enabled: false}

vlans:                              # references the site's VLANs (see Profiles and groups)
  - {id: 10, name: mgmt}
  - {id: 20, name: users}
  - {id: 30, name: voip}
  - {id: 99, name: iot}

ports:
  - name: Gi1/0/1                   # canonical name from the model catalogue
    enabled: true
    profile: bureau-standard        # trace of the resolved profile (informational)
    mode: access                    # access | trunk
    pvid: 20                        # untagged VLAN (native on a trunk)
    tagged: []                      # trunk: list or "all"
    voice_vlan: 30
    poe: {enabled: true, priority: low}
    stp: {edge: true, bpdu_guard: true, root_guard: false}
    security:
      dhcp_snooping_trust: false
      ra_guard: true
      storm_control: {broadcast_pps: 1000}
      port_security: {max_macs: null}
    lldp: true
    speed: auto                     # auto | 100M | 1G | 10G...
    description: "Office 12"

services:
  stp: {mode: rstp, priority: 32768}
  igmp_snooping: true
  dhcp_snooping: {enabled: true, vlans: [20, 30]}
  # Service identity: pushed by meshcraft, global policy overridable
  # per site, secret per device, rotation by generations.
  service_account:                  # meshcraft's CLI account on the device
    username: meshcraft
    auth: pubkey                    # pubkey | password | pubkey_then_password
    key_ref: cred://svc-ssh         # public key deployed; the private one stays in the vault
    privilege: 15
  snmp:                             # deployed automatically by monitoring
    # v3 and v2c_ro are mutually exclusive: the version is RESOLVED per device against
    # the connector's capabilities, not chosen in the policy.
    v3: {user_ref: cred://svc-snmp, auth: sha256, priv: aes128}
    v2c_ro: null                    # filled in instead of v3 on platforms
                                    # that do not support it — read-only, never RW
    traps: {host: <derived collector ip>, user_ref: cred://svc-snmp}
  syslog: {host: <derived collector ip>, port: 514, severity_min: informational}
  telemetry:                        # streaming telemetry, when the platform can
    mode: gnmi_dialout              # off | gnmi_dialout | gnmi_dialin
    target: <derived collector ip>
    cert_ref: cred://svc-telemetry  # client certificate + CA, with a dated expiry
    interval: 10s
```

## Design rules

Seven rules, and their status is not the same: two really govern the code today, two are partially upheld, three are waiting for the format.

| Rule | Status |
|---|---|
| 1. **The model is the contract** — the UI produces only that, drivers consume only that; every new feature starts with a versioned evolution of the schema (`schema_version`) | **target.** There is no `schema_version`; versioning exists on the **exchange format** (`meshcraft_site_config: 1`), not on the per-device intent |
| 2. **Two instances of the same type**, `desired` and `observed`, whose diff drives [[Reconciliation]] | **shipped, and it is the most profitable rule on the list.** `devices.ports` carries the intent, `devices.observed` the measurement (migration `0014`) — conflating them would mean never being able to say which of the two is behind. One nuance: they are not exactly the *same* type, the observed one being serialised from the connector (`instanton.PortState`) and the intent from the UI; it is `recon.ParsePorts` that brings them to a common denominator |
| 3. **Optional fields = "unmanaged"** | **shipped** — `recon.unmanaged`: a port without a mode, or "libre", is neither diffed nor written. And the rule only plays in **one direction**: a port meshcraft manages and the device no longer carries is a very real deviation |
| 4. **Strict normalisation on read**: canonical names, fixed units, explicit booleans, never raw string comparison | **shipped for what is modelled.** The canonical port name comes from the device's faceplate prefix (`portPrefix`, read from `port_groups`), and that is what avoids a read-back state whose names would match no meshcraft port. The announced "per-platform model catalogue" does not exist: `internal/catalog` carries only **generic** templates (`vendor: generic`), useful to mock up a site, not to drive real hardware. The faceplate of adopted devices therefore comes from the **survey** made by the connector, which is better — it is a fact and not a product sheet |
| 5. **Capabilities filter**: validation at compile time, readable message rather than silent failure | **partial, and the trade-off has changed.** Nothing *rejects* an intent: the apply writes everything that is supported, considers the device up to date for all that, and **says** what was left behind (`last_apply.unsupported`, derived from `instanton.WritableExtras`). Refusing would have blocked the operator on a legitimate intent that the next platform will be able to apply; keeping quiet would have displayed a configuration that is nowhere. The third way is to write and announce the rest — details in § *Three ways of not applying* of [[Reconciliation]] |
| 6. **References, not secrets** — `cred://` pointing to the vault, never a secret in clear | **respected in spirit, absent in letter.** No `cred://` exists: secrets live in `site_credentials` (encrypted by `internal/secretbox`) and are resolved **per device** at use, through tag scoping ([[Tags and credential scope]]). The result is the one wanted — no secret travels through `devices.ports` — obtained by putting *nothing* in the model rather than a reference |
| 7. **The reference does not carry the generation** | **shipped**, by construction: since the intent references no secret, a rotation cannot create an intent revision. Generations live in `credential_generations` (migration `0011`), append-only. The rule was right; the mechanism that would have upheld it simply did not have to be written |

## Representation in Go

**Target**: structs generated from a single JSON Schema (source of truth in `schema/intent/v1.json`), `go-jsonschema` into `internal/model/`, the same types projected into TypeScript, and runtime validation of imports/exports by that schema.

**Reality**: there is no `schema/`, no generator, no schema validation. `internal/model/model.go` is **written by hand**, and so are the frontend types (`web/src/api.ts`), separately. The structured parts of the model are deliberately left as `json.RawMessage` (`Ports`, `PortGroups`, `Observed`, `Extra`, `LastApply`, the profiles' `spec`): since the outline is still moving, a frozen Go type would have imposed a migration per shape change — and above all, a round trip through a struct **loses any field the current version does not know**, which is the worst way to lose a configuration (the reasoning is explicit in `siteconfig.RemapSwitchRules`).

What this absence costs, and it is better to know before prolonging it: nothing guarantees that the frontend and the backend are talking about the same object, and the only thing preventing a malformed `jsonb` from reaching the database is a tolerant `Unmarshal`. The day a second connector consumes this model, the schema will stop being a comfort.

## What is actually stored

`devices.ports`, a `port name → object` map. The **effective** content, as the UI writes it and `recon` / `instanton` read it:

| Field | Role | Consumed by |
|---|---|---|
| `mode` | `access` \| `trunk` \| `libre` | diff **and** apply |
| `pvid` | native VLAN; absent on a trunk means **1** ([[Default VLAN]]) | diff **and** apply |
| `tagged` | list of VLANs, **or** the string `"all"` — a wildcard resolved to the site's VLANs, and treated as a **floor** and not an exact set | diff **and** apply |
| `poe` | PoE intent | **recorded, never applied**: `recon.RequestedExtras` picks it up and the banner says so |
| `label` | meshcraft annotation, which the device does not know | display only — and **excluded** from unapplied intents, otherwise the banner would fill with noise on every documented port |
| `link`, `poe_w`, `rx_mbps`, `tx_mbps` | last **observed** state | display. Never diffed: *telemetry is not an intent*, and comparing it would make the whole fleet blink permanently |

`recon.ExtraIntents` declares four non-VLAN intents in advance — `poe`, `stp`, `dhcp_snooping`, `port_security` — of which the UI today only offers the first. Declaring them before offering them costs nothing and prevents a new checkbox from becoming silent again.

And the rest of the target YAML? It exists, but **elsewhere than in the intent**, which is the important point of this section:

| Target block | Where it actually is |
|---|---|
| `device.management` (VLAN, IP, DNS, NTP, SSH/HTTP/Telnet) | `site_mgmt_plan` table — **per site**, not per device (migration `0009`, [[Management addressing]]). The target's `keep` mode corresponds to a device not conforming to the plan, which `internal/api/conformity.go` computes instead of storing |
| `vlans` | `vlans` table, per site, with colour and purpose — the standard model only carries a reference by identifier |
| `services.snmp`, `services.snmp.traps` | `site_credentials` (the secret) + `devices.trap_dest_addr` (the written address) + `devices.trap_seen_at` (the proof of the return path). **Shipped** ([[Monitoring]], [[SNMP identity]]) |
| `services.service_account` | `internal/api/devicecreds_handlers.go` + `instanton.EnsureAccount`. There is **no** public-key authentication: the only shipped platform is web-only, hence password |
| `services.syslog` | **nothing.** No field, no apply, no collector |
| `services.telemetry` (gNMI) | **Arista EOS only** — the only connector declaring `Streaming: true` and `gnmi` channels (state, counters, LLDP, health). The other seven stay on SNMP. The collector is `internal/gnmipoll`, run by the proxy |
| global `services.stp`, `igmp_snooping`, `dhcp_snooping` | **nothing** at device level |
| `ports[].enabled`, `voice_vlan`, `stp`, `security`, `lldp`, `speed` | **nothing** — neither stored nor applied. `stp`, `dhcp_snooping` and `port_security` are pre-declared in `recon.ExtraIntents`, which is the first step and no more |
| `ports[].profile` (trace of the resolved profile) | replaced by a different mechanism: [[Switch profiles]] are **rules** (`internal/portrules`) that produce the configuration, with a per-rule report saying how many ports it won — more useful than a trace frozen in each port, because it is recomputed |

## The `meshcraft_site_config` exchange format — *shipped*

The whole network configuration of a site fits in **a single JSON** that can be exported / imported (`GET /api/sites/{id}/export`, `POST /api/sites/{id}/import`, `internal/siteconfig` + `internal/store/siteconfig_store.go`):

```json
{
  "meshcraft_site_config": 1,
  "site": {"name": "…", "plan": {"zones": […]}},
  "vlans": [{"vlan_id": 20, "name": "DANTE PRI", "color": "#46a758"}],
  "port_profiles": [{"name": "PTZ camera", "spec": {"mode": "trunk", "pvid": 40, "tagged": [40, 41], "poe": true}}],
  "device_profiles": […], "device_groups": […],
  "switch_profiles": [{"name": "Salle 24P", "spec": {"rules": [{"label": "SFP+ en uplink", "select": {"media": "sfp"}, "port_profile": "Uplink"}]}}],
  "devices": [{"name": "FOH", "model": "Generic-24P-4X", "ports": {"ether5": {"mode": "trunk", "pvid": 10, "tagged": [10, 70, 71], "poe": true}}}],
  "links": [{"a": "CORE", "b": "FOH", "speed": "10G", "status": "up"}]
}
```

Rules, all **shipped**: version in the root field; **references by name** (never ids → portable between instances); import = **complete transactional replacement** (purge then reinsertion of `vlans`, `port_profiles`, `switch_profiles`, `device_profiles`, `device_groups`, `devices` in a single transaction — all or nothing). It is the pivot contract on the IT-systems side, vendor-independent. Full example: `samples/site-demo.json`.

Two design details worth reading, because they say how this format must evolve:

- **`switch_profiles` was added without incrementing the version.** An old reader ignores the field, a recent reader knows how to cope with not finding it: there was nothing to break. A format version is reserved for changes an old reader would interpret **wrongly**, not for those it does not see.
- **Translating references is done on the raw JSON**, without going through the rules' Go types. A round trip through a struct would strip a file exported by a more recent version of everything that version can do — silently. And an unknown reference is **left in place** rather than erased: it shows up as a faulty rule, which can be repaired, whereas erasing it would produce a socket silently receiving whatever the next rule gives it.

> [!warning] An import can make a site lose its VLAN 1
> The import purges the site's `vlans` table then reinserts those from the file.
> A hand-written file without VLAN 1 therefore leaves the site without its default
> VLAN — which `store.CreateSite` and migration `0018` exist precisely to prevent
> ([[Default VLAN]]). Export never produces such a file (VLAN 1 is in the
> database), so the case can only be reached through a fabricated file; the fact
> remains that the invariant is upheld at creation and **not** at import. The
> repair gesture is the same as the migration's: a `where not exists` after the
> reinsertion.

## Versioning & history — *decided, not built*

**Nothing in this section exists.** There is no `device_intents` table, no revision, no author, no diff between revisions, no going back — and no *raw* configuration backup in the style of Oxidized either: `BackupConfig` is not part of the `driver.Driver` contract and no connector implements it.

What is shipped and must not be mistaken for its replacement:

| What exists | What it covers — and what it does not |
|---|---|
| `devices.desired_changed_at` | **when** the intent last moved. Serves to *attribute* a deviation (§ *Attribution* of [[Reconciliation]]), not to say what changed |
| `devices.last_apply` (migration `0015`) | what the **last** apply wrote, spared, left outside capabilities, or failed. A single apply, overwritten by the next |
| `pipelines` / `pipeline_steps` tables (migration `0016`) | the **transcript** of every operation on the fleet, consultable after the fact, with its author. It is a log of **actions**, not a history of **states**: one can reread what an apply did, never reconstruct the configuration from before |
| [[Event log]] | `config.applied`, `config.drift`, `config.absorbed`… — the facts, without their content |

In other words: we know **who did what and when**, we do not know how to **go back**. That is the gap `device_intents` would fill, and the reason to want it is not auditing (already covered) but operations: today, a drift absorption (`config.absorbed`) **replaces the intent** without anything keeping the one it overwrites.
