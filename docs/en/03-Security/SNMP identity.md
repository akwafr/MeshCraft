---
tags: [security, monitoring, orchestration]
---

# SNMP identity

Extends [[Service identity and rotation]] to the monitoring channel, and relies on
[[Tags and credential scope]] for sharing secrets. Nothing is reinvented
here: same generations, same vault, same *Configuration → Credentials* screen. What
changes is **what decides a secret's scope**.

## Where this page stands

| Question | Status |
|---|---|
| Kinds `snmp_v2c` (site scope) and `snmp_v3` (tag scope) in the vault and on screen | **shipped** — `checkCredScope`, `internal/api/sitecreds_handlers.go`; `web/src/pages/SiteCredentials.tsx` |
| Negotiation of the algorithm pair against the capabilities, floor, non-compliant flag | **shipped** — `internal/snmpv3`, `internal/snmpprov` (a **pure** package: it sets nothing) |
| Setting the technical tag and generating missing secrets | **shipped** — `ensureSNMPIdentity`, `internal/api/snmpidentity.go`; wired to device creation and to adoption |
| Writing to the device, as a delta, with a verification read-back | **shipped for v2c** — `internal/driver/instanton/snmp.go` (`ApplySNMP` + `verifySNMP`) |
| Writing a v3 user to the device | **coded and tested, never exercised** — no connector declares v3 (§3.1) |
| **Collecting a device over v3** | **not built** — see the callout below. It is the most serious gap on the page |
| Rotating an SNMP secret through generations | **shipped** in the sense of [[Service identity and rotation]] §13 (create a generation), **not built** in the sense of automatic deployment |

> [!warning] The whole v3 mechanism stops at the edge of the collector
> v3 is decided, negotiated, generated, and its write to the device is written and
> tested. But **the proxy's collector only speaks v2c**, and neither does the trap
> receiver. `snmppoll.conventions` forces `Version: gosnmp.Version2c` whatever the
> target asks for — a test even locks this behaviour (`Target{Version: "3"}` →
> `Version2c`) — and `classifyTrap` structurally rejects a v3 trap, since it compares
> a community that v3 packets do not have.
>
> The consequence, in one sentence: **a device switched to v3 today would be neither
> polled nor listened to**. Not degraded: silent.
>
> What prevents the failure is therefore not the collector, it is **four locks placed
> upstream**, and one needs to know them because they are what will be seen refusing:
>
> - `snmpWatchesFor` (`internal/api/watch_handlers.go`) **does not send** the proxy
>   devices whose plan is not v2c: a device on v3 is silently absent from the targets;
> - `trapdest` **refuses** to write a trap destination on a v3 device (verdict
>   `trapDestV3Only`), and the message states both halves of the lock — no reception
>   *and* no polling;
> - the on-demand diagnostic walk refuses it too (`askSNMPv3Only`,
>   `internal/api/ask_handlers.go`);
> - and above all, **no connector declares v3** (§3.1), so `snmpprov` falls back to
>   v2c across the whole fleet actually supported.
>
> The v3 path is thus *written, unit-tested, and dead in production*. It is not a
> waste: the chosen order — decide before writing, write before collecting — made it
> possible to validate the resolution on hardware without ever setting a secret the
> collector would then have ignored. But the first connector that declares
> `SNMPv3{auth, priv}` **will make this hole immediate**, and it will not show: the
> device will have its v3 user, its tag and its secret, the screen will say
> monitoring is configured, and no metric will arrive. The prerequisite of any v3
> connector is therefore `Version3` + `SecurityParameters` in `snmppoll`, and USM in
> the trap receiver — not the other way round.

## 1. Why SNMP cannot have a single site secret

An administration password is text: the same one works for the whole fleet. An
SNMPv3 identity is not text, it is a **key derived** from a password, an
authentication algorithm and an encryption algorithm. Two devices that do not accept
the same algorithms cannot carry the same identity, even with the same password.

Hence the central rule:

> [!important] The scope of a v3 secret is the algorithm PAIR
> An SNMPv3 credential targets a `snmpv3-<auth>-<priv>` tag, never a whole site. A
> site with a homogeneous fleet will have only one tag; a mixed fleet will have as
> many as there are cryptographic families, and it is the reality of the fleet that
> decides it, not the operator.

Corollary, deliberately simple: **no v3 tag = SNMPv2c**. The default is the weakest,
because a device we could not qualify must not receive a configuration it would
silently refuse.

## 2. Three kinds of credentials — *shipped*

`site_credentials.kind` already carries this distinction; it just has to be used.
That is done, and the list of kinds is **closed** on the API side: any other value is
refused with a 400.

| `kind` | Scope | Content | Constraint applied |
|---|---|---|---|
| `admin` | site, group or tag | the administration password (existing) | free scope |
| `snmp_v2c` | **site** | the community, READ-only | tag and group **refused**, `username` forced to empty |
| `snmp_v3` | **tag** `snmpv3-…` | user, authentication password, encryption password | tag **mandatory** and validated by `snmpv3.ParseTag`, then **rewritten in canonical form** |

`snmp_v2c` is site-scoped because a community is text: nothing prevents sharing it.
`snmp_v3` is always per tag, for the reason in §1.

Rewriting the tag instead of refusing it over a detail of case or hyphen is
deliberate: `snmpv3-SHA-256-AES128` and `snmpv3-sha256-aes128` designate the same
cryptographic family, and letting two spellings coexist would create two secrets
where the fleet expects only one — hence half the fleet monitored with an identity the
other half does not carry.

> [!note] A v3 credential carries TWO phrases, necessarily different
> A shipped detail the design had not written, and which is not one: RFC 3414 derives
> two distinct keys, one for authentication, one for encryption. The vault therefore
> has a second sealed field (`secret2_enc`, migration `0017`), reserved for the
> `snmp_v3` kind, and the API **requires the pair** and **refuses two identical
> phrases**. Reusing the same phrase for both keys breaks nothing visible — which is
> exactly why it must be refused at input rather than hoped against.

Generations, the traced reveal and rotation are those of
[[Service identity and rotation]] — no parallel path. Revealing a v3 credential
emits **a single** event for the pair: two lines for a single gesture would suggest
two accesses.

> [!note] The v2c community is ONLY FOR READING
> meshcraft never deploys a write community. Configuration goes through the
> connector; an RW community on a fleet is a root password in cleartext on the
> network.

## 3. What provisioning decides — *shipped*

At adoption, and every time a device's identity changes:

```
connector capabilities  →  Negotiate(auth[], priv[], floor)
                                     │
              ┌──────────────────────┴───────────────────┐
        usable v3                                   no v3
              │                                         │
   tag snmpv3-<auth>-<priv>                    site's snmp_v2c
   set if absent                               credential (created if absent)
              │
   snmp_v3 credential for that tag,
   generated if it does not exist
```

The separation of roles is clear in the code, and it is what makes the thing
re-readable: `internal/snmpprov` **decides** — it is a pure package, with no database,
no network, which sets nothing — and `internal/api/snmpidentity.go` **acts**. One can
therefore ask for the plan without changing anything:
`GET /api/devices/{id}/snmp-plan`, `operator` level, read-only. It was step 2 of the
delivery order in §5, and it is what made it possible to confront the resolution with
the real fleet before writing a single line on it.

Four points that make the difference between a useful automation and a dangerous one —
all four shipped:

1. **The strongest the device can carry**, not the strongest of the floor.
   `Negotiate` takes the maximum of each declared list. Requiring what it cannot do
   does not harden it, it deprives it of monitoring. The default floor is `sha256` /
   `aes128`, and *no authentication at all = no v3*: `noAuthNoPriv` is refused,
   because a v3 without authentication is a more complicated v2c.
2. **The gap to the floor is recorded, not hidden.** A platform that offers only
   MD5/SHA-1 and DES receives `snmpv3-sha-des` and the *non-compliant* flag — and the
   note is written **in the vault**, not merely computed on the fly, so that it is read
   where the secret is read. Deploying it is a choice; presenting it as safe would be a
   lie.
3. **The tag is TECHNICAL.** It is set by provisioning with the source `snmp`, carries
   the reserved prefix `snmpv3-`, and `ParseTag` refuses any other form. The concrete,
   verified consequence: the rule-based tagging engine only removes tags with source
   `rule:`, so an operational rule cannot carry off the technical tag in passing —
   which would have detached the device from its v3 credential without anyone touching
   SNMP.
4. **Nothing is generated twice.** The round trip name → pair → name is stable, and
   `snmpprov.For` is a pure function whose idempotence is locked by a test. The
   generation gesture, for its part, starts from the list of what is **missing**
   (`Plan.Missing`) and exits immediately if it is empty. A detail that decides
   everything: the lookup of the existing credential is done on the **site's**
   credentials, not on those resolved for the device. Looking up "what this device
   sees" would have generated a second v3 secret at the second device of the same
   cryptographic family — and broken the promise of §1, where the tag is precisely
   what **shares** a secret.

Provisioning is triggered on manual creation of a device, on adoption from discovery,
and on a device update **only if its inputs have changed** (model, vendor, management
IP, group). The guard is not cosmetic: full provisioning touches the device and costs
seconds, against milliseconds for a rename. A screen that saves a label must not
trigger an SNMP session.

A provisioning failure **never fails adoption**: it is logged and filed in the
`monitoring` pipeline. Refusing to adopt a device because its SNMP did not take would
leave the device outside, that is to say invisible — whereas adopted without SNMP it is
at least manageable, and its gap is shown.

### 3.1 An observed exception: the Instant On 1930 range

Its guide describes an SNMPv3 (MD5/SHA-1, DES) and its firmware embeds the code — the
`SNMPUserISList` and `SNMPGroupList` sections exist and answer. But the page that would
expose it is **hidden** on this model (`hidden:_top.isDeviceTypek9` in its bundle): the
device really offers only the v2c of "Get Connected".

The connector therefore declares `SNMPVersions: []string{"v1", "v2c"}` and nothing
else. Announcing v3 would lead provisioning to generate a tag and a secret that setting
would then refuse — and **a credential that exists in the database and nowhere else is
worse than an absent credential**, because the screen shows it and suggests monitoring
is configured.

> [!note] It is the range that is the exception… but today it is the fleet's rule
> The whole v3 mechanism — negotiation, tags per algorithm pair, secrets per tag —
> remains intact and serves the rest of the fleet. A platform declares only what it
> OFFERS, never what its documentation describes.
>
> To be noted, however, so as not to be mistaken about the state: **no connector fills
> in `SNMPv3Auth` / `SNMPv3Priv` today**. Both fields exist in the [[Connectors]]
> contract and are read only by `snmpprov`. The whole supported fleet therefore falls
> back to v2c — a test in fact says so explicitly for the 1930. v3 is not "rarely
> used": it is not used.

## 4. Writing to the device — *shipped, in v2c*

The last step, and the only one that touches the fleet. On the Instant On range,
captured on a real device:

- `SNMPGlobalSetting` carries `SNMPEnbl` — **at 2, that is to say disabled** on a
  factory-fresh device. Nothing will be collected until it is enabled;
- the v2c community section is called **`CommunityList`** (and not
  `SNMPCommunityList`, which answers `-1`);
- `SNMPUserISList` carries the v3 users, with `authenticationMethod` and
  `privacyMethod` as numeric codes.

Table of codes, captured from the device's bundle (`pages2.js` and its dictionary) and
not inferred — copied identically into the connector, with its provenance:

| Field | Codes |
|---|---|
| `SNMPEnbl` | **1 = enabled**, 2 = disabled |
| `authenticationMethod` | **0** = none, 1 = MD5, 2 = SHA |
| `privacyMethod` | **1** = none, 2 = DES |
| `accessMode` (v2c) | 1 = read, 2 = write, 3 = super |

> [!warning] "None" does not have the same code depending on the field
> `0` for authentication, `1` for encryption. Setting `privacyMethod=0` by symmetry
> would send a value outside the enumeration.

The `accessMode` codes 2 and 3 appear nowhere in the connector: there is no code path
that sets a write community. It is not an intention, it is **triple-guaranteed**: the
constants do not exist in the file, building the entry hard-codes read-only, and the
**read-back refuses** a community that would come back with another mode.

The order of sections is imposed, and there are **four** of them, not three: enable the
service, the community, the **group**, then the v3 user. The group is dedicated
(`meshcraft-ro`) and set with an **empty** `writeView`, precisely because factory
groups carry one: reusing an existing group would have given meshcraft's v3 user a
write right no line of code asked for — and the read-back checks that too.

The write follows the conventions already established for this framework
([[Reconciliation]], *Apply* section): section, `action="set"`, delta and not full
state, then **verification read-back** — on this range, a `statusCode 0` without effect
is an established case. The read-back compares seven things: the service enabled, the
community present, its mode **read-only**, the user present, **both algorithm codes**,
the group present, and the absence of a `writeView`. Two additional refusals before any
write: `noAuthNoPriv`, and the absence of a `localEngineID` — without a local engine ID,
a v3 user is set into the void.

`SNMPUserISList` does not accept `set`: a diverging user is **deleted then recreated**,
which is the only sequence the device honours. And the transcript returned to the
operator carries only counts, never a secret.

## 5. Delivery order — *all four steps are done*

That was the plan, and it held:

1. the `snmp_v2c` / `snmp_v3` kinds in the vault and on the Credentials screen —
   **shipped** (scope validation, second phrase, badges on screen);
2. resolution at provisioning (`snmpprov`), which says what WOULD be needed without
   writing anything — **shipped** (`GET …/snmp-plan`, pure package);
3. actual generation of missing tags and secrets — **shipped**
   (`ensureSNMPIdentity`, events `tag.assigned` and `credential.generated`);
4. writing to the device, last, with its read-back — **shipped for v2c**; the v3 path
   is written and tested but no connector reaches it.

What is missing is therefore not in this list, and that is what makes the old §5
misleading once ticked off: it only counted the **"decide and write" half**. The
**"collect"** half was never in the plan, and it is what is missing — hence the opening
callout. The next milestone is not a fifth identity step, it is `gosnmp.Version3` and
USM in `internal/snmppoll` and `cmd/proxy/trap.go` ([[Monitoring]]).
