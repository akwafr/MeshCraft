---
tags: [architecture, connectors]
---

# Connectors (drivers)

The heart of the modularity: a connector = the code that knows how to talk to **one platform** (Cisco IOS, Aruba AOS-CX, MikroTik RouterOS, HP ProCurve, D-Link web-only…). It consumes the [[Standard data model]] as input and produces what the platform understands (CLI, REST API, web forms); on the read side it does the reverse: it normalises the device's state into the standard model.

## Go interface

```go
type Driver interface {
    // Identity and scope
    Info() DriverInfo                          // name, platform, supported versions
    Capabilities(model DeviceModel) Capabilities

    // read → plan → apply cycle (see Reconciliation)
    ReadState(ctx context.Context, s Session) (*DeviceState, error)   // normalised current state
    Plan(ctx context.Context, diff Diff, caps Capabilities) (*Plan, error) // diff → ordered operations
    Apply(ctx context.Context, s Session, p *Plan, opts ApplyOpts) (*ApplyResult, error)

    // Support
    BackupConfig(ctx context.Context, s Session) ([]byte, error)      // raw config (archiving)
    ImportConfig(raw []byte) (*DeviceState, error)                    // raw config → standard model
    Fingerprint(ctx context.Context, probe ProbeResult) (score int)   // "is this device mine?"
    ResetProcedure() ResetProcedure                                   // factory reset: CLI + hardware
}
```

- `Session` wraps the transport already established **through the [[Proxy]]** (`ProxyHandle.DialTCP`): the driver receives a `net.Conn` and has no notion of a tunnel.
- `ImportConfig` is the read-side counterpart of `Plan`/`Apply`: it feeds the "import the local configuration" choice offered at adoption ([[Discovery]]). No session: it works on text, which also makes it possible to import a configuration pasted from a serial console.
- `ResetProcedure` is **knowledge**, not an action: the driver describes the reset (CLI route, hardware route, what gets erased, pitfalls); meshcraft does not run it in the operator's place.

> [!info] LLDP neighbours are **not** a connector method, and that is deliberate
> An aspirational version of this contract carried a `CollectLLDP`. It never existed
> in the code and never will: LLDP neighbours, shipped on 29/07/2026
> ([[LLDP neighbours]]), are one more set of OIDs in the **proxy's SNMP poller**
> (`internal/snmppoll/lldp.go`), on the same footing as `if_state`, `health` or
> `optical_power`.
>
> This is not a shortcut but the consequence of a rule already written elsewhere:
> **periodic collection runs on the proxy and speaks only SNMP**, whereas a connector
> runs on the controller and talks through the tunnel. Such a method called by the
> controller would have sent every table read across the WAN, for data the local SNMP
> agent already serves — and above all it would have created a **second path of
> truth** for the same information, to be arbitrated against the poll (the reasoning
> about traps, [[Monitoring]] §2).
>
> What connectors keep, and what really is their role: **declaring** the `lldp` channel
> and its cadence floor, and declaring its **unavailability** with its reason when the
> platform does not serve it — which is what `swos` does, with a firm capture behind it.
> That is what switches collection on or off, device by device.
- `Apply` takes options: `DryRun`, `ConfirmTimeout` (automatic rollback if not confirmed), see [[Reconciliation]].

## Shared transports

Drivers do not reimplement SSH or HTTP; they compose common building blocks:

| Block | Based on | Use |
|---|---|---|
| `transport/cli` | **scrapligo** (SSH + Telnet, prompt/paging handling) | CLI platforms: IOS, AOS-CX, ProCurve, RouterOS |
| `transport/rest` | net/http + resty, pluggable auth | API platforms: AOS-CX REST, RouterOS REST, unifi-like |
| `transport/webform` | http + goquery (form scraping/POST) | old web-only switches (D-Link smart, TP-Link Easy Smart) |
| `parse` | TextFSM templates (Go port) + home-made regexes | parse `show vlan`, `show interfaces status`… |
| `render` | text/template per platform | generate CLI blocks |

Telnet: supported by scrapligo; to be reserved for devices without SSH, with a permanent warning in the UI ([[Hard points and risks]]).

## Capabilities

Each connector **declares** what the platform (and the precise model) can do; the orchestrator validates the intent before planning:

```go
type Capabilities struct {
    Ports          []PortDef // name, type (copper/SFP/SFP+), max speed, PoE
    VlanRange      [2]int    // e.g. 1–4094
    Features       map[Feature]bool // Trunk8021Q, PVID, PoE, STP, RSTP, MSTP,
                                    // DHCPSnooping, RAGuard, IGMPSnooping, LACP,
                                    // SNMPv2, SNMPv3, SyslogRemote, SSHMgmt, HTTPMgmt...
    MaxACLs        int
    ConfigCommit   CommitStyle // none | commitConfirm | archiveRollback | rebootRequired

    // Management interface: what the model really accepts. The low end of a
    // range often limits itself to VLAN 1, or to untagged — pushing a tagged
    // management VLAN would make it unreachable ([[Management addressing]]).
    Mgmt MgmtCapabilities // VlanIDs (any|fixed), Tagging (tagged_ok|untagged_only),
                          // Addressing (static|dhcp|both)
}
```

An intent that asks for an absent feature produces a **readable compilation error** ("RA-Guard not supported by DGS-1210") — never a silent failure at deployment. The UI greys out the options unavailable for the selected device.

> [!warning] An unknown capability is assumed ABSENT
> For anything that can cut off access to the device — first and foremost the
> management interface ([[Management addressing]]) — the unknown is treated
> as a refusal. A model of which we do not know whether it accepts a tagged
> management VLAN is treated as not accepting one: at worst we ask for a
> needless confirmation, at best we avoid a trip on site.

> [!warning] A declared capability that nobody reads protects against nothing
> `AdminAccount.PasswordMax` went through an entire batch **declared by the connectors
> and read by no code**. The defect only showed at the first real write: the site
> secret was 17 characters, a MikroTik CSS326 accepts 15, and no password could be
> set on that range. The connector refused correctly before emitting a single byte —
> the capability was right, the guard worked — but nothing upstream used it to
> produce an acceptable secret.
>
> That is the failure mode specific to this file: a capability is not
> documentation, it is a **decision input**. The reflex to keep when adding a field
> here is to name, in the same batch, **who consults it** — otherwise it faithfully
> describes a constraint the product keeps violating.
>
> Fixed for length (see [[Tags and credential scope]] §3.2, the
> technical tag `pwpolicy-`). Two gaps of the same kind remain open:
>
> - **`AlphanumericOnly` is too coarse.** SwOS's real constraint is *ASCII only*,
>   not *alphanumeric only*: today it is declared **in prose** in
>   `AdminAccount.Notes` and enforced by hand in the connector's `checkPassword` —
>   hence invisible to any consumer of the contract. A `PasswordCharset`
>   (`alnum` | `ascii` | `any`) would replace the boolean without losing anything.
>   Not built: three connectors and the API layer depend on this contract.
> - **The required composition is not a capability at all.** "At least one
>   uppercase letter, one lowercase letter, one digit" is hard-wired in
>   `internal/pwgen` for the *whole* fleet, whereas it is a platform policy — and
>   RouterOS, precisely, publishes its own (`/rest/user/settings`) and varies it.

### Declaring what the platform does NOT expose

Everything above describes what a platform **can do**. The counterpart was missing,
and its absence was costly in the least visible place: **monitoring**. A connector
can now declare that a metric is **unavailable on its platform**, and — this is the
field that gives the mechanism its value — say whether the absence was **observed on
hardware** or only **assumed** (`driver.MetricUnavailable`,
`internal/driver/driver.go`). The operator-facing text does **not** belong to the
connector: the UI localises it from the metric key
(`monitoring.unavailable.<metric>`). It is the exact counterpart of
`ResetProcedure.Confirmed`, which already distinguishes verified knowledge from
inferred knowledge.

All of this lives in `Capabilities.Monitoring` (`driver.MonitoringCapabilities`),
which the structure excerpt above — older than the monitoring channel — does not
show: these are each platform's collection channels, their floor, its ability to
**push** traps and syslog, and now what it **does not serve** ([[Unified monitoring and pipelines]] §1).

**The distinction that justifies the whole mechanism.** Two situations read
**identically** on screen — no graph — and lead to **opposite** decisions:

| What the operator sees | Real cause | What they should do |
|---|---|---|
| no graph | *meshcraft does not collect it yet* — a gap on **our** side | wait for a release, and they are right to wait |
| no graph | *the platform does not expose it* — a fact of the device | change hardware, or give up. No development will change anything |

Confusing them costs time and credibility, in both directions: an operator waiting
for a release that will never come, or replacing a switch for a metric that a
collection batch would have served.

Three properties keep the declaration honest:

- **`Observed` sets the TONE.** An observed absence is shown without reservation; an
  absence assumed from a range's documentation must stay **revisable** — the next
  firmware or another model may contradict it. Showing them the same way would make
  an operator give up on an assumption, exactly the mistake this field exists to
  prevent. `Source` says where the knowledge comes from: the capture that establishes
  it, or the guide page that suggests it;
- **`Contradictions()` names the declarations that bite each other.** A metric
  announced as both **served by a channel** and **unavailable** is worse than no
  declaration: either the screen promises a graph no collection will fill, or it shows
  a reason for absence next to a graph that exists. In both cases the operator loses
  the only thing these capabilities give them — the certainty that what is shown
  matches the device. This is not a runtime check but an **invariant locked by each
  connector's tests**, including those that do not exist yet: the inconsistency
  becomes impossible to introduce silently;
- **empty is not an admission of ignorance, and says nothing about availability.** The
  project rule remains *an unknown capability is assumed absent* (callout above): a
  metric without a channel is not monitored, declaration or not. This field only adds
  the **reason** where we know it — hence the `ok = false` of `WhyUnavailable`, which
  means "nobody documented it", never "it is available", and the `omitempty` of the
  payload, since an empty array would read on screen as "everything else is
  available".

> [!warning] What has NOTHING to do in this list: our own gaps
> On the first connector, health, LLDP and PoE are **not** in it, and it is the easiest
> point to miss. The device serves them, they have their declared channel with its
> floor; if no graph comes out of them, it is **meshcraft** that does not read them yet
> (cf. [[Monitoring]] §1). Putting them here would declare them **impossible**, and
> make an operator give up on something that only awaits a collection batch.
>
> The distinction has since been **confirmed by the facts**: `lldp` was in that case — a
> gap on our side — and the collection batch arrived on 29/07/2026 without any connector
> declaration changing, except that of `swos`, which declared it **unavailable** and was
> right. An operator who had read "meshcraft does not read it yet" did get their graph;
> the one who read "this firmware does not have the feature" is still waiting for
> nothing, and that is accurate.
>
> The criterion is therefore mechanical: this list carries only what **the device does
> not serve**. A gap on our side shows differently — as the absence of collection
> **facing** a channel that, for its part, can return the metric.

**The declaration travels all the way up to the device page**, and that is what makes it
useful: otherwise it would be knowledge locked inside the connector's code.
`driver.Capabilities` is serialised by no route — its consumers are internal
(monitoring cadences, ability to push traps, admin account) — so the type's JSON tags
were ready and **inert**. The declaration therefore travels on the payload the device
screen already reads (`monitoring_unavailable`: metric, `observed`, optional technical
`source`). The UI shows the **localised label** for that metric. A dedicated route would
have cost a second round trip per page and shown **first a blank, then the
explanation** — and that blank is precisely what is being removed. The explanation is
thus **where the operator was looking for the data**, and the capture that establishes it
is **consultable**: a sceptic can redo the query themselves via
`POST /api/devices/{id}/snmp-walk` ([[Controller-proxy protocol]], § *One-off
questions*), on their own device, and get the same empty answer.

| Question | Status |
|---|---|
| Declare a metric unavailable (`metric`, `Observed`, `Source`) | **shipped** — `driver.MetricUnavailable` |
| Refuse declarations that contradict each other | **shipped** — `Contradictions()`, locked by connector tests |
| Explanation shown on the device page | **shipped** — `monitoring_unavailable` declaration + i18n `monitoring.unavailable.*` |
| First real declared case | **shipped** — the 1930's optical power (§ *SFP+ optical power*, below) |
| Distinguish on screen "not yet collected" from "never collectable" for metrics **without** a declaration | **open** — today the two look alike as long as the connector says nothing |

## Model variations

A platform covers dozens of models (8/24/48 ports, PoE or not). The connector embeds a **model catalogue** (embedded YAML): number and naming of ports, SFP uplinks, PoE budget, dimensions for the UI ([[Port rendering]]), logo. The fingerprint ([[Discovery]]) maps `sysObjectID`/banner → platform → model.

```yaml
# connectors/cisco_iosl2/models/c1000-24p.yaml
model: C1000-24P-4G-L
ports:
  - {name: "Gi1/0/{1..24}", type: rj45, speed: 1G, poe: true}
  - {name: "Gi1/0/{25..28}", type: sfp, speed: 1G}
poe_budget_w: 195
ui: {rows: 2, logo: cisco}
```

## Organisation & extensibility

- One Go package per connector: `internal/connectors/<platform>/`, registered in a registry at startup (`func init()` + build tags where needed).
- **Compiled into the binary** for the MVP. If a third-party ecosystem emerges: move to `hashicorp/go-plugin` (separate process, RPC) without changing the `Driver` interface. No dynamic `.so` plugins (fragile in Go).
- **Tests**: each driver ships with recorded transcripts (real, anonymised `show` outputs) replayed against the parser; optional integration tests on containerlab / lab devices.
- Target connectors for the MVP: see [[Roadmap]].


## First connector shipped: Aruba Instant On 1930

`internal/driver` carries the contract, `internal/driver/instanton` the first
connector. It is deliberately a **web-only** platform: no SSH, no Telnet, no
serial console. Starting with the most constrained case avoids building the
contract around a CLI and discovering too late that it does not bend to it.

What the vendor guide (ref. 5200-5879) taught us, and what changes the design:

- **`Commit = apply_then_save`.** "*Changes take effect immediately but are not
  retained across a switch reset unless you click Save Configuration*": applying
  and persisting are **two gestures**. That is a free rollback and it must be used
  in that order — apply, **check reachability**, then save. If the device is lost
  in between, a power cycle brings it back to its last saved configuration. This
  strategy fits the table in [[Reconciliation]] without the platform having a
  `commit confirmed`.
- **The management VLAN is free (1–4092)**, contrary to what one assumes of an
  entry-level switch — but "*a VLAN with no member port cannot be designated the
  management VLAN*". That is an **ordering** constraint: create the VLAN, assign
  ports to it, and only then designate it. Exactly the kind of knowledge `Plan()`
  must carry ([[Management addressing]]).
- **SNMP is only allowed on the management VLAN**: the collector derived by
  [[Monitoring]] must be reachable in that VLAN, otherwise telemetry does not come
  up even though the configuration looks correct.
- **5 HTTP/HTTPS sessions in total.** A connector that opens requests in parallel
  without throttling itself evicts the human administrator from their own interface.
  `MaxSessions` exists for that, and it must be honoured.
- **SNMPv1/v2c/v3**: the range can do v3, so the resolution in
  [[Service identity and rotation]] §2.1 will pick v3 there — entry level does not
  imply v2c.

The fingerprint latches onto the **family** ("Instant On") with moderate confidence,
not onto the model: the 1930 and 1830 ranges serve the same web interface, and their
capabilities differ. The exact model is refined after connecting. Claiming to
recognise a 1930 on the sole strength of an HTML title would be wrong, and these
capabilities are too dangerous to be guessed.


### What the trial against the real device taught us

A 48-port 1930 (JL685A, firmware 3.4.0.0) served as the first case. Four lessons,
all drawn from the trial and not from reading:

- **A device identifies itself without authentication, if you know where to look.**
  Fresh out of the factory, the provisioning UI (port 4443) exposes
  `/api/systemInfo`. Once switched to local administration that port closes — but
  the classic UI's **login page** carries the same information in its hidden fields:
  `sysDescr` gives the full label, the `JL685A` reference, the firmware and the
  faceplate ("48p Gigabit 4p SFP+"), and `rlPhdUnitGenParamSerialNum` gives the
  serial number. Hence a two-source `Inspect`, which raises confidence from 55 (HTML
  title) to 96 (the device speaks for itself).

- **The generic probe must not try to know everything.** Exact model, serial,
  faceplate: that is platform knowledge. The connector provides it through an
  enrichment hook called after identification, and the probe stays ignorant of
  vendor specifics.

- **The physical faceplate is a fact, not an intent.** At adoption in "push" mode,
  the device starts blank in the model — but its 48 copper ports and 4 SFP+ exist.
  Without taking over the captured faceplate, adoption produced a device **with no
  ports at all**, which is not "blank" but wrong.

- **This switch is fragile under bursts.** The guide states 5 HTTP/HTTPS sessions in
  total; a probe that opens ten connections in two seconds gets turned away
  intermittently. A failed enrichment was initially **silent** — hence
  undiagnosable. It now leaves an explicit note: one must be able to tell "the
  connector has nothing to say" from "the connector could not talk to the device".


### Classic administration interface (port 443) — what was captured on hardware

The 1930's configuration UI is not documented by the vendor. What follows was
captured on a real device (48 ports, firmware 3.4.0.0), and every point cost a round
trip:

**Login does not go through the form.** The page serves a `<form>`
`signinForm` with `inputUsername`/`inputPassword` — it is NEVER submitted. The
button calls a `formSubmit()` which:

1. reads `/device/wcd?{EncryptionSetting}` (served at the root, **without**
   authentication): `passwEncryptEnable`, `rsaPublicKey` (PKCS#1, 2048 bits),
   `loginToken`;
2. encrypts with RSA PKCS#1 v1.5 the message
   `user=<u>&password=<p>&ssd=true&token=<tok>&`, where `<u>` and `<p>` are passed
   through `encodeURIComponent` — and not through form encoding, which would render
   a space as `+`;
3. does a **GET** on `<session>/hpe/config/system.xml?action=login&cred=<hex>`.

Posting the form as is returns a 404.

**The status code lies.** For code 12 the page shows "Login failed due to Device
password encryption problems", whereas its own `stateChange()` treats that same 12
as a SUCCESSFUL login and the device returns `statusString = "Default user Login"`.
The codes that mean an open session are `0`, `9`, `10`, `12`, `13`, `14`; only `4`
is a credential refusal, and `5` signals that the 5 sessions are saturated. Trusting
the UI's message led to concluding an encryption failure where the factory account
had just opened a session.

**The factory account can only be tested through this door.** Encryption on and an
empty password give code 12 "Default user Login": that is a success, not an error.

**The five sessions are a real limit.** Every debugging attempt consumes one, and no
logout endpoint was identified in the served scripts. A probe that opens one session
per request evicts itself in five calls — hence the
`POST /api/devices/{id}/web-probe` probe (read-only, global-admin), which accepts
SEVERAL paths in a single session.

**Writing is not yet achieved.** The bundle describes a `localPost` that POSTs to
`tmpl.url` an XML body produced without an envelope
(`<UpdatePassword><edit><userName/><oldPassword/><newPassword/><newPasswordConfirmation/></edit></UpdatePassword>`),
successful when `ActionStatus/statusCode` is `0`. But `tmpl.url` appears in none of
the three scripts served by the page — probably a dynamically loaded chunk. Every
variant tried (`…/hpe/wcd`, content types `application/xml` then
`x-www-form-urlencoded`, headers of a jQuery `$.ajax`, `<DeviceConfiguration>`
envelope) answers **200 with an empty body**. The connector reports it as such
rather than concluding success.

**The range can create named accounts.** `AdminUserSetting.add` with
`AdminUserEntry{userName, password, accessLevel}`, where name and password are
RSA-encrypted and prefixed `crypto_!`. `mcadmin` can therefore be set on this
hardware — the `Admin.Custom` capability stays `false` as long as the connector
cannot do it, because announcing a settable name and failing to set it is worse than
announcing `admin` and keeping one's word.

### SNMP trap receivers — captured on hardware, read-only

The connector could set an SNMP identity (`SNMPGlobalSetting`, `CommunityList`,
`SNMPGroupList`, `SNMPUserISList`) but **nothing of the return path**: no trap
receiver section. What follows fills that gap. Everything was captured on
`sw-labo-1930` (JL685A, firmware 3.4.0.0) through `POST /api/devices/{id}/web-probe`,
**without writing once** to the device. Cost: **two** authenticated sessions out of
five — the JavaScript bundles and the dictionary are read **without
authentication**, they consume no session, and once fetched they can be searched
offline as much as one likes. That is the right way to work on this range: fetch the
knowledge, then only open a session to read state.

**There are TWO sections, not one.** The interface separates receivers by SNMP
version, and the two halves have neither the same name nor the same shape:

| Version | `wcd` section | List | Entry |
|---|---|---|---|
| v1 / v2c | `SNMPTrap` | `SNMPTrapReceiverList` | `SNMPTrapReceiverEntry` |
| v3 | `NotificationReceiverList` | *(the section IS the list)* | `Receiver` |

Both reads — `wcd?{SNMPTrap}` and `wcd?{NotificationReceiverList}` — were done and
answer a proper `type="section"` section.

> [!warning] `SNMPTrapReceiverList` is NOT a section name
> It is a **child** of `SNMPTrap`. The trap is the same as for `SNMPCommunityList`
> versus `CommunityList`: a plausible name, and the answer of a non-existent section.
> The v1/v2c list can only be read by asking for `SNMPTrap`, and the POST of a v1/v2c
> receiver carries `<SNMPTrap action="set">` — not
> `<SNMPTrapReceiverList action="set">`. Symmetrically, for v3 there is **no**
> "SNMPTrapV3" section: it is `NotificationReceiverList`, without the `SNMP` prefix.

**The fields, and their codes.** Captured in `pages2.js` ("Switching → SNMP" page,
`addTrapReceiversV1V2Submit` and `addTrapReceiversV3Submit`), translated by
`dictionary1.xml`:

| Field | v1/v2c | v3 | Values |
|---|---|---|---|
| `receiverIPAddress` | yes | yes | unicast IPv4 (`unicastipv4` on this family) |
| `UDPPortNo` | yes | yes | default **162**, range 1–65535 |
| `notificationType` | yes | yes | `1` = Trap, `2` = Inform (default `1`) |
| `timeout` | yes | yes | 1–300 s, default 15 — **Inform only** |
| `retries` | yes | yes | 1–255, default 3 — **Inform only** |
| `communityString` | yes | — | 1–20 characters |
| `SNMPVersion` | yes | — | `1` = SNMPv1, `2` = SNMPv2 (form default: **`1`**) |
| `userName` | — | yes | 1–20 characters, **free text** |
| `securityType` | — | yes | `1` = noAuthNoPriv, `2` = authNoPriv, `3` = authPriv |
| `filterEnabled` | yes | yes | `1` = a filter is attached, `2` = none |
| `filterName` | yes | yes | name of a `Filter` from `NotificationFilterList`, empty otherwise |

The `securityType` codes are the ones `snmp.go` already knows under the name
`securityType` for groups: same enumeration, same meaning.

> [!warning] Two form defaults not to copy
> `SNMPVersion` defaults to **`1` (SNMPv1)** in the interface: a connector that omits
> the field or copies the default declares a **v1** receiver, and the v2c collector
> will never see anything usable. And when `notificationType = 1` (Trap), the
> interface **empties** `timeout` and `retries` before sending — sending them filled
> in on a Trap is an unobserved case.

**The destination port is configurable.** `UDPPortNo` is an ordinary input field,
162 by default, validated on 1–65535. That is directly useful: the [[Proxy]] does not
have to listen on a privileged port if we do not want it to.

**linkUp / linkDown: it is a PER-PORT setting, and it is already active.** On this
range there is neither an "enable link traps" checkbox nor a per-category trap list.
The setting lives in the interface configuration:

```text
Standard802_3List → Entry{interfaceName, linkStateTrapEnabled}   1 = enabled, 2 = disabled
```

Captured on the device: **68 entries** (52 physical ports + 16 `TRK` aggregates),
`linkStateTrapEnabled = 1` on **all** of them. In other words, link telemetry needs
no preparation on the port side: declaring a receiver is enough. Three other switches
are carried by the device, all at `1`:

```text
SNMPGlobalSetting → trapsEnbl = 1       trap emission
SNMPGlobalSetting → authTrapsEnbl = 1   authentication failure traps
SNMPTrap          → enabled   = 1       (switch of the v1/v2c section)
```

> [!warning] These three switches appear on NO page of the UI
> None of the three served bundles (`mainpage.js`, `pages1.js`, `pages2.js`) mentions
> `trapsEnbl`, `authTrapsEnbl` or `SNMPTrap/enabled`: the device **returns** them on
> read, but its interface never writes them. The connector must therefore **check**
> them (a device that had them at `2` would emit nothing, and nothing in the UI would
> show it) and **not** try to set them: a field that no path of the interface writes
> is a field of which we do not know whether a write is accepted. Same caution as for
> `Admin.Custom`: do not announce what we cannot deliver.

**Filtering is by OID, not by category.** `NotificationFilterList` carries
`Filter{filterName, OIDSubtreeList{OIDEntry{OIDSubtree, filterAction}}}`, with
`filterAction` `1` = *Included* and `2` = *Excluded* (filter name ≤ 30 characters,
OID ≤ 255). A receiver attaches to it with `filterEnabled = 1` + `filterName`. On the
lab device the list is **empty**: no filter, hence no trap dropped. For
[[Monitoring]] that is the desirable situation — we do not want a filter that
silently masks link events.

**v2c and v3: what v3 additionally requires.** The `userName` field of the v3 form is
**free text**, with no drop-down fed by `SNMPUserISList`: the device therefore applies
no check when a non-existent user is entered. The consequence is unpleasant — a
misnamed v3 receiver is saved without error and emits nothing authenticatable. The
connector must set the v3 user **before** the receiver, and check the match itself.
The group must carry a `notifyView`, which `groupEntry()` already does.

> [!warning] A v3 Inform is not a v3 Trap
> A v3 **Trap** authenticates against the local SNMP engine, whose identifier was
> captured (`localEngineID = 8000000b0302005e1061a7`, `localEngineIDType = 2`,
> `localEngineIdState = 1`): a user set with `engineIDType 1` is enough. A v3
> **Inform**, however, is acknowledged by the receiver and is derived against the
> **receiver's** engine — it requires a `SNMPRemoteEngineIDList{Entry{IPAddress,
> remoteEngineID}}` entry (10–64 hexadecimal characters) and a user with
> `engineIDType 2`. This last point is **inferred from the bundle**, not verified on
> hardware: until it is, aim for v3 **Trap** or v2c, not Inform.

**The traps' source IP is the management IP, and that is not a choice.** None of the
bundles carries a `source-interface`-style setting for SNMP (the only
`sourceInterface` in the code concern RMON and port mirroring). And the device has
only **one** usable IP interface: `IPv4InterfaceList` returns `VLAN99` =
`198.51.100.101/24` (`owner = 2`, static), the only other entry being
`User Defined Port 1` = `169.254.0.1/24` with `owner = 3`, which the interface itself
excludes from its lists. Traps will therefore leave from the management address
([[Management addressing]]) — which is exactly what the [[Proxy]] needs to
recognise the sender against its watch list.

> [!warning] The corollary: the management VLAN decides
> SNMP only listens and speaks on the management VLAN on this range. Changing that
> VLAN changes the traps' source IP, and so silently invalidates the proxy's watch
> list — traps will keep arriving and all be dropped as "unwatched source". Any
> management relocation must be followed by a refresh of the watches.

**State of this device: no receiver is declared.** `SNMPTrapReceiverList` and
`NotificationReceiverList` are empty, `NotificationFilterList` too — read twice, in
two separate sessions. `SNMPEnbl = 1`: SNMP is active (meshcraft set it). Nothing to
remove, nothing to reconcile: the ground is clear.

#### What is NOT established, and cannot be read-only

- **How many receivers the platform accepts.** No ceiling exists on the client side:
  no validation, no dictionary message ("table full", "maximum reached"), no counter
  in the table configuration. Both tables are declared `multiSelect` + `selectAll`,
  which **suggests** several entries are expected — but suggesting is not knowing,
  and the real ceiling is enforced by the firmware at add time. Knowing it requires
  adding entries until refused, hence writing. **The "several receivers" capability
  must stay `false` until that test**: it is what would let telemetry survive the
  loss of a proxy without redeploying the fleet, and announcing it falsely would
  cost the outage at the very moment we were counting on the redundancy.
- **The write gesture.** See below: a hypothesis, never executed.

#### The write gesture — HYPOTHESIS to be validated by a real test

`modules.js` finally delivers the mechanism the previous part left open.
`tmpl.post()` does a `POST` on **`tmpl.url`**, and `tmpl.url` is set by
`tmpl.wcd(url)` to the **last read URL, query string included** — hence the
obligation to carry `?{Section}` on the POST. The body is produced by
`tmpl.json2xml()`, whose exact rule is: envelope
`<?xml version='1.0' encoding='utf-8'?><DeviceConfiguration>…</DeviceConfiguration>`,
and for each section an element `<Section action="…">`. That is **word for word** the
convention `writeSection()` already applies: nothing to change in the transport.

Combining this with the objects captured in `pages2.js`, the v2c gesture **would be**:

```text
POST  <session>/hpe/wcd?{SNMPTrap}
<?xml version='1.0' encoding='utf-8'?><DeviceConfiguration>
<SNMPTrap action="set"><SNMPTrapReceiverList><SNMPTrapReceiverEntry>
  <receiverIPAddress>…</receiverIPAddress><UDPPortNo>162</UDPPortNo>
  <notificationType>1</notificationType><timeout></timeout><retries></retries>
  <communityString>…</communityString><SNMPVersion>2</SNMPVersion>
  <filterEnabled>2</filterEnabled><filterName></filterName>
</SNMPTrapReceiverEntry></SNMPTrapReceiverList></SNMPTrap>
</DeviceConfiguration>
```

and the v3 gesture, `POST <session>/hpe/wcd?{NotificationReceiverList}` with
`<NotificationReceiverList action="set"><Receiver>…</Receiver></NotificationReceiverList>`
carrying `receiverIPAddress`, `UDPPortNo`, `notificationType`, `timeout`, `retries`,
`securityType`, `userName`, `filterEnabled`, `filterName`. Deletion is an
`action="delete"`; it repeats the entry's **key** and not the whole entry —
`receiverIPAddress` + `notificationType` + `managementStationIPAddress` in v1/v2c,
`receiverIPAddress` + `notificationType` + `userName` + `securityType` in v3. The
`managementStationIPAddress` field of the v1/v2c deletion **does not exist** in the
add: it is read from the table row, hence returned by the device. A deletion built
without a prior read cannot designate its target.

> [!warning] This is not a fact, it is a reconstruction
> Nothing in this block has been executed. On this interface, `statusCode 0` with no
> effect is an established case, and so is a `200` with an empty body: the only proof
> of a write is a **re-read** of `wcd?{SNMPTrap}` / `wcd?{NotificationReceiverList}`
> that shows the entry. The implementation batch must therefore, in order: read,
> write, **re-read and compare field by field** (`snmp.go`/`verifySNMP` is the model),
> then `SaveConfig` — and only then declare the capability ([[Reconciliation]]).

### SFP+ optical power: the range does not expose it — SNMP capture

First real use of the unavailability declaration (§ *Declaring what the platform does
NOT expose*), and **a capture, not an assumption**: `Observed = true`. The starting
question is the one an operator asks in front of four fibre links — "what is my
optical budget?" — and the answer is that there will not be one on this range.

Captured on `sw-labo-1930` (JL685A, firmware `InstantOn_1930_3.4.0.0`,
`sysObjectID = 1.3.6.1.4.1.11.2.3.7.11.199`, serial `CN0EXAMPL1`), queried over **SNMP
v2c through the proxy**:

| What was queried | Result | What to conclude |
|---|---|---|
| `ENTITY-SENSOR-MIB` (`1.3.6.1.2.1.99`) | **zero rows** | the **standard** sensor route is not served at all — neither temperature nor optics |
| the enterprise tree `1.3.6.1.4.1`, **enumerated in full** | **5001 OIDs**, all under `1.3.6.1.4.1.11.2` (branches `.29`, `.43`, `.35`, `.26`, `.2`, `.1`, `.38`, `.42`, `.36`) | what the device serves privately is known **exhaustively**: the conclusion does not rest on an OID we did not think of trying |
| a table indexed by the SFP+ `ifIndex` values (**49 to 52**) | **none**. The only tables with numeric indexes are a registry of MIB variables (`snmpInPkts`, `ipForwarding`, `icmpInMsgs`) | there is no *per optical port* quantity anywhere — and a registry of SNMP counters is not a sensor |
| `1.3.6.1.4.1.89` (Radlan/Marvell, which `sysORID` **announces**) | **zero rows** | a **declared MIB capability** is not readable data. That is the trap in this capture: `sysORID` invited us to look there |
| the state of the SFP+ at capture time | **three of the four were up** (`ifIndex` 49, 50, 52) | transceivers inserted and links established: the "empty table for lack of a module" hypothesis is **ruled out**, and that is what turns a doubt into a fact |

The resulting declaration lives in `Capabilities().Monitoring.Unavailable`
(`internal/driver/instanton/instanton.go`) under the `optical_power` key, with its
reason written for an operator — *no version of meshcraft will change anything here;
tracking an optical budget on these links requires a device that publishes these
sensors* — and the capture above as `Source`.

> [!note] How this capture was possible, and that is the lesson of method
> The `snmp_walk` question asked **through the tunnel** makes it possible to
> **discover** what a device serves, instead of guessing OIDs in a vendor MIB and
> shipping a collection that returns nothing ([[Controller-proxy protocol]],
> § *`snmp_walk`: the device can say what it exposes*). A MIB says what a **range** is
> supposed to serve; a walk says what **this** device, with **this** firmware, really
> serves — and `sysORID` announcing an empty branch shows the gap is not theoretical.
>
> Two details of the contract carry the whole capture. The three SNMPv2 responses that
> are not values (`NoSuchObject`, `NoSuchInstance`, `EndOfMibView`) are returned **as
> is**: that is exactly what we were trying to find out. And the `truncated` flag is
> what makes a "zero rows" **interpretable**: without it, a response cut at the ceiling
> would pass for a complete table, or the reverse. Enumerating a whole enterprise tree
> in fact exceeds the 5000 rows of a single question — it is done **by branches**, and
> `truncated` is what says when to split.

> [!warning] A NEGATIVE conclusion is a deliverable, provided it is declared
> This capture has the same value as a positive capability: it **closes** a question
> that would otherwise reopen with every operator looking for their optical graphs and
> every developer tempted to write a collector "just in case". But it is only worth
> anything declared: as long as this knowledge stayed in a trial report, the screen
> showed a blank, and a blank reads as "meshcraft does not do it yet". The cost of the
> confusion was concrete — waiting for a release that will never come.
>
> What is **not** established for all that, and what we do not claim: that the range
> exposes these sensors by **no** route at all. The capture covers **SNMP**, standard
> and private, and nothing here says what the device's web interface shows or not. The
> declared reason therefore speaks of what was measured — and if a web path appeared,
> it would call for revising the declaration, not contradicting the capture.

## The VLAN model of the two MikroTik platforms

The same 24-port + 2 SFP+ chassis exists in both MikroTik worlds, and the two worlds
have **nothing in common** on VLANs. It is the only case in the fleet where the choice
of connector hinges on the system that booted and not on the hardware
([[Discovery]]: the web page title decides, the model says nothing).

Reference hardware, captured: **CSS326-24G-2S+ / SwOS 2.17** and
**CRS326-24G-2S+ / RouterOS 7.21beta3**.

### What each platform really carries

| | SwOS (CSS326) | RouterOS (CRS326) |
|---|---|---|
| Membership | `/vlan.b` table: one row per VLAN, a **member-port mask** | `/interface/bridge/vlan`: one entry per VLAN (or per **range**), `tagged` and `untagged` lists |
| Native VLAN | `/fwd.b` `dvid`, one per port | `/interface/bridge/port` `pvid`, **independent** of membership |
| Tagging | **inferred**: a frame leaves untagged if its VLAN is the port's native one | **explicit**: the port is in `tagged` or in `untagged` |
| Global switch | none — it is the **per-port mode** (`/fwd.b` `vlan`) | `/interface/bridge` `vlan-filtering`, all or nothing |
| The CPU | implicit: `/sys.b` `avln` says **from which VLAN** administration is accepted | **a member like any other**: the `bridge` pseudo-port must appear in `tagged` (or `untagged`) of the management VLAN |
| Ingress filtering | `/fwd.b` `vlni` (any / only tagged / only untagged) | `/interface/bridge/port` `ingress-filtering` + `frame-types` |
| Recourse if you cut yourself off | **none**: no SSH, no Telnet, no serial. A trip on site | serial console **and** MAC-winbox, both captured |

Two asymmetries decide everything else:

- on **SwOS**, membership and tagging are the **same data**. Removing a port from the
  member list cuts that VLAN on that port, no half-measures;
- on **RouterOS**, `pvid` and membership are two settings **in two different menus**.
  The reference device carries the anomaly in its purest form: its 24 copper ports
  have `pvid=100` while VLAN 100 has an **empty** `untagged` list. They tag their
  incoming traffic into a VLAN from which nothing comes out, and the firmware says
  nothing.

### Port names: the device's own, with the old one still accepted

Both MikroTik connectors now name the sockets **the way their firmware names them**,
and the SwOS connector has joined the RouterOS one on this point.

| | Copper | Cages | Where it comes from |
|---|---|---|---|
| **SwOS** (CSS326) | `Port1` … `Port24` | `SFP1`, `SFP2` | `nm` field of `/link.b`, **captured** |
| **RouterOS** (CRS326) | `ether1` … `ether24` | `sfp-sfpplus1`, `sfp-sfpplus2` | the interface names, since the first delivery |

The SwOS connector previously wrote `GE1` … `GE26` — the Instant On range's
convention, borrowed so that a mixed fleet would compare, with **continuous**
numbering including cages. The price was paid at every diagnosis: the socket meshcraft
called `GE25` is called `SFP1` in the switch's interface, and the reconciliation was
left to be done in one's head on the one piece of data that decides **where a write
lands**.

Two INDEPENDENT counters, then, and that is the point to remember: `Port25` does not
exist on a 24-port + 2-cage switch, and that name is **refused** rather than
translated.

> [!warning] BOTH families are read back, and the opposite would have silently broken a fleet
> `Facade.Index` (`swos/naming.go`) also accepts the old numbering: `GE25` still
> designates the first cage. Refusing these names would have **silently** ignored the
> intents already recorded — `PlanPorts` puts an unknown name in its `Skipped`, so a
> port configuration would have stopped being applied without any error saying so. On
> the two reference devices `devices.ports` is empty and nothing would have been lost;
> the database of a fleet in service is not.
>
> And the translation does not stop at the index. The apply decision looks in the
> **read** state for the port's current native VLAN and **whether it carries a link** —
> and the read state is named by the faceplate. Looking up with the plan's name would
> make the port unknown: native 0, **no link**, which is precisely what **lifts** the
> guards. A legacy intent would thus have disarmed the protection of the port carrying
> the session, on the only platform in the fleet that has neither SSH, Telnet nor a
> serial port. Hence the **canonical name** in `PlanPorts`, and a test that fails if it
> is removed.

**`nm` did NOT become a key**, and that is a decision distinct from the previous one:
it is free text, renameable port by port from the switch's interface, with no
uniqueness constraint, and nothing follows a rename. Three defects at once if it were
made the key — two ports with the same name would overwrite each other in `State`'s
maps, `port_groups` cannot express a list of arbitrary names (and it is what the web
interface unfolds to enumerate the sockets), and an intent would stop designating a
port at the first rename. What is kept is therefore the firmware's **convention**, not
the strings. The strings stay served where they decide nothing: the telemetry's
`device_name` field, and a **state note** that flags ports whose local label differs
from the faceplate name — without which the gap between the two screens would come
back, reversed and unexplained.

> [!note] A SwOS device adopted BEFORE this change keeps its faceplate in `GE`
> `port_groups` is only written at discovery: updating it requires re-identifying the
> device. Until that is done, the screen enumerates `GE1` … `GE26` while the state read
> returns `Port1` … `SFP2`, and the re-read state attaches to no socket on the screen.
> It is not a loss of configuration — both reference MikroTiks have **empty**
> `devices.ports` — but it is an empty screen, and one needs to know why.

### The configuration can be present and INERT

This is the costliest fact of these two platforms, and it fits into **no** field of
`driver.PortState`: a VLAN configuration can be written, re-read, compared, found
compliant — and have no effect on a single frame.

| Case | Platform | What the screen would show without the note |
|---|---|---|
| VLAN mode `disabled` on a port | SwOS | a native VLAN and a membership that do not act: the port ignores the table |
| "Force VLAN ID" (`fvid`) on a port | SwOS | a **trunk** — whereas the port puts ALL its incoming traffic into its native VLAN, tags included |
| `vlni` = only tagged on an access port | SwOS | a configured access port, which drops all its traffic |
| `vlan-filtering` inactive on the bridge | RouterOS | segmentation in place, on a bridge that lets everything through everywhere |
| `ingress-filtering: false` (the **default**) | RouterOS | watertight segmentation, whereas it is only watertight on **egress** |
| `frame-types: admit-all` on an access port | RouterOS | an access port, whereas a host that tags its own traffic gets out of it |
| `hw-offload` fallen back to false on some of the ports | RouterOS | chip throughput, on ports that go through the CPU |

These sentences come out through `driver.State.Notes`, are recorded in
`last_apply.notes`, and are **shown** by `ConnectorNotesBanner`
([[Port rendering]]). None of them calls for a button: neither enabling
`vlan-filtering` nor tightening a filter that a guard refused to touch is a gesture
meshcraft takes in place of a human who is watching.

### What apply writes, and what it refuses

**SwOS** — `swos/apply.go`, `swos/ports.go`

| Written | Refused, and why |
|---|---|
| **gained** membership (`/vlan.b` mask) | — |
| **lost** membership | never a management VLAN **on a port that carries a link** (see the callout); never the effective native VLAN; nothing on a port in "all" (it is a **floor**); nothing on an **active** port when `avln` is 0 |
| the native VLAN (`dvid`) | a port whose native VLAN is frozen by the guard is frozen **entirely** — a half-spared port ends up with a native VLAN on one side and a membership on the other, and carries neither |
| the VLAN mode, **`disabled` → `enabled`** only | never `optional` → `enabled` (that **tightens** a working port); never `strict` (ingress filter); never a downgrade |
| — | `vlni` and `fvid` are **never** written: tightening them drops traffic, and the standard model does not express what it would take to decide |

> [!bug] "The benefit of such a removal is nil" was wrong, and it cost ALL convergence — capture of 29 July 2026
> The refusal to remove a management VLAN applied to **all** ports, without looking at
> the link, on the grounds that "a port without a link today may be tomorrow's uplink,
> and the benefit of such a removal is nil". Both halves of the sentence were wrong.
>
> **Measured on the fleet's CSS326**: its VLAN 99 was a member of **twenty-six** ports.
> An apply asking for "the 24 copper ports as access VLAN 100" therefore wrote **zero
> ports** and refused twenty-four times, on every pass, forever:
>
> ```
> 21:58  port.configured  24 ports → access VLAN 100
> 21:58  config.applied   0 port(s) written, 24 spared, 48 drift(s) remaining
> ```
>
> The operator read "VLANs don't apply", and that was the right diagnosis: the intent
> was **unreachable by construction**, and the device page showed a drift that nothing
> could close.
>
> **The benefit is not nil, it is security.** On SwOS, membership and tagging are the
> **same data**: leaving the administration VLAN a member of an access port means
> letting any host plugged in there reach the management plane by tagging its own
> traffic. That is exactly the hole "access VLAN 100" exists to close.
>
> **The criterion is therefore the LINK**, as for the two other guards in the same file
> and for waking up filtering: a session does not cross an empty socket. An **active**
> port that is not the session's port keeps its membership — that is the conservative
> residue, and it is accepted.
>
> **Verified on hardware after the fix**: 24 ports written, **0 refusals**, VLAN 99 went
> from 26 members to 2 (the two cages, which are "all" trunks), device still reachable,
> `sync = in_sync`, **0 drift**. It is also this connector's first VLAN apply
> **exercised on hardware** — the "NOT VERIFIED ON HARDWARE" box in `swos/apply.go` no
> longer holds for this path.

**RouterOS** — `routeros/apply.go`, `routeros/ports.go`

| Written | Refused, and why |
|---|---|
| missing `/interface/bridge/vlan` entries | a **dynamic** entry is never modified: a static one is created alongside |
| the `tagged` / `untagged` lists | a line with **several `vlan-ids`**: its lists are shared by all its VLANs, writing one VLAN's membership into it would rewrite the others' |
| the `pvid` | never on an **active** port whose PVID is a management VLAN |
| `ingress-filtering=yes` | never on an **active** port that is not a member of a management VLAN |
| `frame-types=admit-only-untagged-and-priority-tagged`, **access** ports only | never on an active port where a management VLAN arrives **tagged** |
| — | `admit-only-vlan-tagged` is **never** written: a meshcraft trunk always has a native VLAN ([[Default VLAN]] §2), hence untagged traffic that this value would drop |
| — | `vlan-filtering` is **never** enabled: at a stroke, everything not declared drops, administration included |

### The order of operations, and why it is not negotiable

Two rules, the same on both sides, and each avoids an outage:

1. **DECLARE BEFORE FILTERING.** The VLAN table first, the tightening afterwards, in
   two separate requests. The reverse order drops a port's traffic during the
   interval — and permanently if the second request fails.
2. **ADD BEFORE REMOVING.** A port moving from VLAN 50 to VLAN 60 is a member of both
   at the moment of computation, never of neither.

And one invariant: **the management VLAN is never orphaned**. It goes at the head of
the list of VLANs to declare, whether it is in the site plan or only declared by the
device.

> [!warning] On SwOS, cleanly creating the management VLAN's row is what cuts you off
> Creating a row in `/vlan.b` **moves** that VLAN from the "unknown to the table"
> regime to the "declared, hence filtered" regime. Yet on a port in `optional` mode —
> the factory default, and the state of all 26 ports of the reference device — a VLAN
> the table does **not** declare flows freely. Declaring it with an **empty** member
> mask therefore drops it everywhere, instantly.
>
> In other words: the fastest way to make a CSS go silent is to tidy up its VLAN
> table. Apply therefore seeds the created row with **all ports that carry a link**,
> and says so in `Skipped` — since we cannot know which one carries the session, one
> active port too many is an excess of scope that gets corrected, one active port too
> few is a trip on site.

### What remains infeasible, per platform

| | SwOS | RouterOS |
|---|---|---|
| Trunk that **refuses** untagged | no intent: `vlni` is not written | **inexpressible** in the standard model — a trunk there always has a native VLAN |
| Access port that refuses tagged | no intent: `vlni` is not written | **written**, under guard |
| Strict ingress filtering | `strict` not written (drops frames on ingress) | **written** (`ingress-filtering`), under guard |
| Adding a port to the bridge | not applicable — all ports are in the table | **not done**: a port outside the bridge is flagged, not added |
| Aggregation (LACP / bonding) | `/lacp.b` read for the capabilities, **never written** | `/interface/bonding` captured empty, **never written** |
| STP / RSTP | `/rstp.b` read **and written** (`ena`, `fc`, `cost`, `edge`) via `ApplySpanningTree`; site mode via the `stp_mode_preference` preference (MSTP/RSTP-PVST/STP/none, or **empty**: see below) | `edge`, `path-cost`, `bpdu-guard` captured, **never written** |
| Speed, duplex, auto-negotiation, MTU, flow control | `/link.b` carries `an`, `dpxc`, `fctc`, `fctr` — **never written**, and no standard-model intent asks for them | `/interface/ethernet` carries `advertise`, `l2mtu` (ceiling `max-l2mtu: 10218` captured) — **never written** |
| Port isolation | `/fwd.b` `fp1…fp26`, full matrix, **never written** | `horizon` per port, **never written** |
| Enabling / disabling a port | `/link.b` `en` — **written** (`ApplyPortAdmin`, whole page) | `disabled` on `/interface` — **written** (`ApplyPortAdmin`, `type: ether` only) |

This last row long said "never written" on both sides, and the gap was upstream: the
[[Standard data model]] had no intent for shutting down a port. It has had one
since 2026-08-05, and **both connectors write it**, each proven on lab hardware on
2026-08-10 (SwOS `Port2`, RouterOS `ether2`, via `POST /api/devices/{id}/ports/admin`
then re-read, a second empty apply, and a confirmed rollback).

The two gestures do not look alike, and it is the SwOS one that must be read twice:

- **RouterOS** writes ONE field (`disabled`) on ONE record, whose `.id` is re-read
  just before the write — these numbers do not survive a reboot, and a stale
  identifier does not fail, it disables *another interface*. Only `type: ether` is
  accepted: `bridge`, `lo` and `vlan99` accept `disabled: true` perfectly well and
  cut, respectively, all switching, the loopback and the administration address;
- **SwOS** has no per-port write. The administrative state is a BIT in a MASK (`en`)
  of a page the device returns and expects WHOLE: writing a port means reposting `nm`
  (the twenty-six port names), `an`, `dpxc`, `fctc`, `fctr`, `spdc` with it. Hence
  refusal rather than truncation when a field is missing — posting without `nm` would
  rename twenty-six ports to nothing. And persistence is immediate: no
  `running`/`startup`, no reboot that undoes the gesture. The only way back is the
  `GET /backup.swb` taken just before the write, or the reset button on the front
  panel, on site.

#### Empty `stp_mode_preference`: the per-site kill switch

A site's list of preferred modes has **three** states, not two, and the third is the
only one that does not read on the screen:

- **absent** — the site has never opened the STP screen: `driver.DefaultSTPPreference`
  applies, apply pushes a mode;
- **filled** — the first mode the platform announces is pushed;
- **explicitly empty** — the operator emptied it: `applyDeviceSTP` returns
  immediately and **sends nothing**. This is the case designed for a Catalyst left
  deliberately in (rapid-)PVST+ during a migration, on a connector that has no "read,
  identical, nothing to do" path: before this switch, any port change re-sent the mode
  and reconverged the tree.

> [!warning] The switch is broader than its name
> It does not only suspend the mode. `SpanningTreePlan` carries the mode, the MST
> region **and** the per-port intents — portfast/edge, BPDU guard, path cost — and
> everything goes through the same `pushDeviceSTP`. Emptying the list to protect a
> single device's global mode therefore also stops BPDU guard from being applied on
> all of the site's access ports.
>
> And a site using MST stops being **re-homogenised**: the region digest covers the
> site's whole VLAN list (`siteMSTRegion`), so when that list moves, devices already
> carrying a meshcraft region keep the OLD one. In 802.1s, a non-matching region is
> treated as an ordinary RSTP neighbour: the eviction is **silent**, the ports still
> forward, the device stays green. That is exactly the invisible 802.1s failure the
> region comparison exists to catch.
>
> The operator is not left defenceless for all that: `handleApplyDeviceSTP` takes an
> explicit mode and never consults the preference, so a per-device push from the STP
> panel still works. What stops is the automatic RIDE-ALONG.

### RouterOS's third transport: the binary API

The RouterOS connector now has **two** transports, and it is not redundancy — it was
an **absence** in REST that forced it. Everything is developed in
[[MikroTik telemetry]]; what belongs on this page is the port map, because three
different protocols answer on the same device, with the same account:

| Port | Protocol | What this connector does with it |
|---|---|---|
| 80 | **REST** — HTTP, JSON, Basic replayed on every request | everything: identification, state read, apply, SNMP, traps |
| **8728 / 8729** | **binary API** — sentences, length-prefixed words | the telemetry REST cannot return |
| 8291 | **Winbox** — *yet another* protocol, encrypted, undocumented | **nothing**, and none is planned |

The three capabilities were already captured and declared (`/ip/service`: `www` on 80,
`api` on 8728, `api-ssl` on 8729, `winbox` on 8291, none disabled). What changed is
that `BinaryAPI` no longer says "which this connector does not use".

**What the binary API brings, and why a third transport was needed.** Only two things,
and both come from `/interface/ethernet/monitor`, which is a **command** and not a menu:

- **a link's NEGOTIATED speed.** Observed in the captures: neither the 87 fields of
  `/interface/ethernet` nor those of `/interface` carry a `speed`. There is only
  `advertise`, which says what the port *offers*. A port that has fallen to 100 Mbit/s
  because of a cut pair is therefore **invisible through REST** — everything works,
  ten times slower;
- **the optical diagnostics of an SFP+ cage.** This point is not a discovery: the
  connector already said so, and drew the only honest conclusion possible at the time
  — "it could not be read, `monitor` is a POST, out of reach of a tool restricted to
  GET, so it has neither a channel nor an unavailability entry". It is that sentence
  the new transport has made obsolete.

> [!warning] Not one byte of this protocol has been exchanged with hardware
> The only path to the CRS326 from the development host is an HTTP tool restricted to
> GET/HEAD: it cannot open a bare TCP socket on 8728. What **is** captured is that the
> service listens and that both ports accept the connection without speaking HTTP. The
> protocol is implemented from the specification published by the vendor and tested
> against a **simulated device**.
>
> What the simulator catches, and that is what matters: **framing**. A badly encoded
> word does not produce a syntax error, it shifts the stream, and the session dies
> several commands later with no apparent relation to the cause — the costliest failure
> mode to diagnose on hardware, and precisely the one a simulator catches without
> hardware. What it does **not** prove: that this firmware answers as the specification
> describes. The declared channel therefore carries a higher floor than REST's and says
> so in its note; only a campaign on hardware will close the gap, and it is the same
> common thread as for everything else — *read, write, re-read and compare, then
> declare the capability*.

A structural limit deserves to be known before relying on it: **TLS on 8729 listens but
is probably not usable** on a RouterOS without an installed certificate. RouterOS has
historically served an **anonymous** Diffie-Hellman suite there, absent from Go's TLS
library — by a design decision of that library, since an anonymous suite authenticates
neither party. The fallback is the cleartext API through the proxy's tunnel, which is
exactly REST's trust regime on this platform.

## Connectors as plugins: independent binaries and a contract

One connector per platform, written by different people, at different paces: the only
organisation that holds at scale is the **plugin**. Decision: **independent
binaries**, outside the controller process, linked by a versioned contract.

What it buys: a vendor or an integrator can write their connector without touching the
core or waiting for a meshcraft release; a faulty connector does not bring down the
controller; it can be written in another language; and it is distributed, signed and
updated separately.

### The hard point: a connector has no network access

This is the structuring decision, and it is counter-intuitive. **A connector never
opens a connection itself.** It asks the platform, which hands it back as a stream.

The reason is architectural: the device is almost never reachable from the
controller's machine, it is reachable from a [[Proxy]] — so the connector would have
to speak yamux, WebSocket and manage the tunnels. That is the opposite of the goal.
By going through the platform:

- the connector stays ignorant of the path — the "single code path" rule
  ([[Overview]]);
- the **proxy's local allowlist** and the logging stay on the platform side, hence out
  of reach of a third-party plugin;
- the same connector works directly, through a proxy, or against a recording replayed
  in tests.

### Shape of the contract

| Topic | Choice |
|---|---|
| Transport | **gRPC over a local socket**, plugin launched by the controller, `go-plugin` style. Typed, bidirectional, native streaming, multi-language. |
| Handshake | the plugin announces `protocol` (integer), its `Info` and its `Capabilities`; a protocol that is too old is **refused at startup**, never silently degraded. |
| Platform → plugin direction | `Fingerprint`, `Capabilities`, `ReadState`, `Plan`, `Apply`, `BackupConfig`, `ImportConfig`, `ResetProcedure`. LLDP neighbours are not there: collected by the proxy's SNMP poller (`internal/snmppoll/lldp.go`), cf. callout. |
| Plugin → platform direction | `Dial(host, port)` → TCP stream; `Log`; `Progress`. Nothing else: no disk, no database, no direct network. |
| Secrets | passed **per operation**, never the vault ([[Credentials]]). A plugin sees the credential it needs, at the moment it needs it. |
| Isolation | separate process, dedicated user, no access to the controller's file system. A plugin remains **third-party code running next to sensitive data**: say so, and partition it. |
| Distribution | signed binary + manifest (covered platforms, contract version, fingerprint). Verification before execution, as for the proxy binary ([[Proxy versioning and updates]]). |

### Path from what exists

`internal/driver` already defines the contract in Go (`Info`, `Capabilities`,
`ResetProcedure`, `Fingerprint`) and `internal/driver/instanton` implements it
in-process. That is not wasted work: **it is the contract stabilising**. Moving to
plugins consists of projecting these same methods into a gRPC service and making the
built-in connector the first client of that service — keeping the possibility of
loading a connector in-process for those we maintain, and out-of-process for the
others.

Sequencing it this way is not taking the easy route: freezing an IPC contract before
having written two or three real connectors means freezing the first one's
assumptions. The 1930 has already disproved several — two APIs on the same device, a
two-step commit, an identification readable without authentication.

Two more connectors confirmed it, and `internal/driver/connector.go` was born of that:
the API layer was written **directly against the 1930 connector's types**, so that
adopting a MikroTik sent it an SNMP apply in the Instant On protocol. Three
inconveniences of the new contract are known and not fixed — noting them is better
than rediscovering them at the third connector.

> [!warning] Three known limits of the connector contract
> - **Optional capabilities are carried by the session, not by the connector.** The
>   written intent was for a type assertion to happen *before* the gesture starts; yet
>   `TrapPusher`, `MgmtMover`, `Persister`, `AccountManager`, `SNMPConfigurator` and
>   `RawReader` are asserted on the object returned by `Authenticate` — hence
>   **after** talking to the device, and after spending one of an Instant On's five
>   sessions. A workaround is in place for traps, whose absence is read first in the
>   `Monitoring.Traps` capability; for the management switchover, there is **no** way
>   to refuse before contact. The fix would be a `Supports(gesture)` on the connector,
>   or these interfaces duplicated on the connector side.
> - **`ApplyResult.Skipped` mixes two natures.** A port spared by a guard is
>   **forceable**; a platform write refusal is not — on RouterOS, a
>   `/interface/bridge/vlan` line that declares several `vlan-ids` will never have its
>   membership rewritten, because its member lists are shared by all its VLANs and
>   meshcraft cannot compose a membership common to eleven. The screen presents both
>   the same way and offers a "force" that will change nothing for the second. A
>   per-entry marker is missing.
>
>   *(The original example was SwOS and its "add only". It no longer holds: this
>   connector now REMOVES memberships the plan no longer asks for, with four named
>   refusals — see §"The VLAN model of the two MikroTik platforms" below.)*
> - **`Identify` does not say whether it needs credentials.** The Instant On gives its
>   identity to an anonymous caller, SwOS gives its model through its challenge's
>   realm, and RouterOS returns **nothing** without authentication. The caller
>   discovers it by trying, which costs one generic probe per live address — and that
>   is precisely what complicated relocation ([[Discovery]]).
