---
tags: [architecture, security, orchestration]
---

# Tags and credential scope

§12 of [[Service identity and rotation]] settled it: the secret is **per site**, with
as many credentials **per device group** as needed. The reason for this nuance was
not convenience, it was a hardware constraint — *"password policies differ from one
model to another (maximum length, forbidden characters)"*.

This note covers what the group cannot do, and gives meshcraft the means to organise
a fleet without organising it by hand.

## 1. Why the group is not enough

A device has **only one** group (`devices.group_id`). Yet the reasons for giving a
device a particular password are not mutually exclusive:

- a **product range** that caps passwords at 16 characters;
- a **vendor** whose interface refuses punctuation;
- a **location** entrusted to a contractor, whom you do not want to entrust with the
  secret of the rest of the site;
- an **address range** corresponding to an isolated control-room network.

The group already serves something else: it carries the device profile
([[Profiles and groups]]), that is, the configuration. Diverting a configuration axis
to express a password constraint forces a choice between the two, and the day a switch
belongs to the "Stage location" *and* to the "8 ports" range, there is no good answer.

> [!important] A tag is not a group
> The **group** says *how the device is configured*: only one, it carries the
> profile. The **tag** says *which sets it belongs to*: as many as you want, they
> carry no configuration. Confusing the two reproduces the problem we are trying to
> solve.

## 2. The model

Shipped by migration `0013`:

```sql
-- Tags set on a device. Several per device.
device_tags(device_id fk, tag text,
            source text,     -- manual | rule:<uuid> | snmp | pwpolicy  (see §2.1)
            created_at, primary key(device_id, tag))

-- Automatic tagging rules, per site.
tag_rules(id pk, site_id fk, tag text, priority int, enabled bool,
          match jsonb,     -- vendor | model | location | ip_prefix, AND-ed
          note text, created_at, updated_by fk users,
          unique (site_id, tag, match))

-- And on site_credentials: a credential can target a TAG instead of a group.
-- The historical uniqueness (site_id, group_name, kind) is widened, otherwise a
-- site could not have two distinct tag credentials.
site_credentials(… , tag text default '', priority int default 0)
unique index site_credentials_scope_key (site_id, group_name, tag, kind)
```

> [!warning] Two `priority` columns, and only one is used
> This is the reading trap of this model. The `priority` that breaks ties between
> the credentials of §3 is the one on **`site_credentials`** — it is the one
> `CredentialsForDevice` sorts by. The one on `tag_rules` is read by nobody:
> `tagging.Rule` does not even have the field, and the order of tagging rules does
> not matter since a rule **sets** a tag without removing another — there is
> therefore nothing to arbitrate between them. The column stays, without effect;
> relying on it to prioritise tagging would produce nothing visible, which is the
> worst form of error.

### 2.1 `source`: what distinguishes the set from the deduced — and the technical

A tag set by hand and a tag deduced from a rule are not handled the same way.
Replaying the rules must be able to **remove** what a rule had set and no longer
applies, without ever touching what a human decided. Without the `source` column,
the only safe re-evaluation would be to remove nothing — and the fleet would
accumulate stale tags nobody would dare clean up.

A **third** source appeared at implementation, and it was not planned here: `snmp`,
set by SNMP identity provisioning ([[SNMP identity]],
`internal/api/snmpidentity.go`). Neither `manual` nor `rule:`:

- marking it `manual` **would lie** about its origin and expose it to editing by
  hand, whereas no operator decided anything;
- marking it `rule:` would make it removable by a rule replay — that is, a
  `tag-rules/apply` **would unplug the fleet's monitoring in passing**, since the v3
  credential targets this tag and would no longer be set on anything.

A separate source is therefore exactly what guarantees a replay breaks nothing:
`tagging.Evaluate` only removes sources prefixed `rule:`, and that is not an
implementation detail but the property that makes the replay safe.

A **fourth** source follows, for exactly the same reason: `pwpolicy`, set by the
provisioning of the **administration password** (`internal/api/pwpolicy_creds.go`).
The reasoning carries over word for word, replacing "monitoring" with "access", which
makes it more serious still: if a rule replay removed this tag, the device would fall
back on the site credential, whose secret its own connector refuses **before
sending**. A `tag-rules/apply` would then close the only door of an entire product
range, and nothing on screen would link the two gestures.

> [!important] The general rule, now that there are two cases
> A mechanism that sets tags in order to **find itself again** must have its own
> source. Neither `manual` (lying about the origin, and exposing itself to editing
> by hand), nor `rule:` (exposing itself to removal by replay). The question to ask
> when facing the third case: *if this tag disappeared, would something stop working
> without saying so?* If yes, a dedicated source, and a test that locks in that
> `tagging.Evaluate` does not touch it.

## 3. Choosing a device's credential: from the most specific to the most general

A device can match several credentials. The order is **total and deterministic**,
because indeterminism would be costly: two runs choosing different credentials would
set two different passwords on the same device.

1. credential targeting a **tag** the device carries;
2. credential targeting its **group**;
3. **site default** credential (`tag` and `group_name` empty).

The order is written **only once**, in `store.CredentialsForDevice`:

```sql
order by specificity desc,   -- 3 = tag, 2 = group, 1 = site default
         priority desc,      -- site_credentials.priority
         tag,                -- alphabetical
         id                  -- last resort: nothing is ever left undecided
```

Between several tag credentials, the highest `priority` wins; on a tie, the tag name
in alphabetical order; on a further tie, the identifier. The last two criteria are not
matters of taste: they exist only so that there is **never** an unresolved case. Two
credentials with the same tag and the same priority are possible — nothing forbids
it, only the quadruple `(site, group, tag, kind)` is unique — and without `id` as last
resort PostgreSQL would return an arbitrary order, hence **variable from one run to
the next**. That is the case `id` closes.

The function returns the **whole** list, not only the winner. That is what lets the
caller flag the ambiguity instead of masking it.

> [!warning] An ambiguity is flagged, not guessed
> When a device matches several tag credentials of the same priority, meshcraft
> picks one — it has to — **and says so** in the UI and in the [[Event log]]. A
> silent tie-break between two contradictory intents of the operator is a bug
> waiting to happen.

The tag beats the group because it is the only axis that can be set *in addition*: if
the operator took the trouble to create a credential for a tag, they were aiming at an
exception.

### 3.1 Not every kind of credential can be split by tag

This note assumed tag scoping was **universal**: an organising axis, indifferent to
what it organises. SNMP identity demonstrated the opposite, and `checkCredScope`
(`internal/api/sitecreds_handlers.go`) draws the consequences kind by kind.

| Kind | Scope allowed | Why |
|---|---|---|
| `admin` | site, group **or** tag, as you choose — **except** the `pwpolicy-` namespace, reserved | it is this note's original case: password policies differ from one model to another, and operations chooses its axis. Nothing to constrain, and nothing to change for what exists. The exception arose from a defect observed on hardware (§3.2): when a platform's policy **excludes** the site secret, it is no longer a filing choice, it is a constraint — and meshcraft enforces it itself |
| `snmp_v2c` | **site only** — tag *and* group refused | not because a community would be indivisible, but because the v2c provisioning plan **carries no tag**: a community filed under a tag or a group **would never be found**. That is the decisive reasoning of this whole safeguard — a secret filed in a scope that provisioning does not consult *exists without ever serving*, and the screen would show it. Worse than an absent secret: you would believe monitoring configured |
| `snmp_v3` | **tag mandatory**, and it is **imposed**: `snmpv3-<auth>-<priv>` | a v3 key **derives from the pair of algorithms**. Two devices that do not accept the same ones *cannot* carry the same identity — it is not a preference, it is cryptography. A "site" v3 would be a secret half the fleet would silently refuse |

The list is **closed**: an invented kind is refused, with the three allowed values in
the message. A free kind would produce a credential nothing consumes, silently inert —
the same failure mode as a badly chosen scope, by another path.

The consequence is structural: the `snmpv3-` prefix is a **reserved namespace**, and
`snmpv3.ParseTag` is its sole judge. An operations tag must never be interpreted as an
encryption instruction, and a v3 credential cannot target a free tag — it would be set
on a set of devices nothing guarantees share algorithms.

A detail of form that matters: the tag recorded is **the output of `Choice.Tag()`**,
never the string entered. A spelling variant — case, `aes-128` versus `aes128` — would
manufacture a **second** credential for the same pair of algorithms, and the fleet
would end up cut in two without any screen saying so. The pair without encryption is
explicitly named `-nopriv` for the same reason: two tags that look alike must be
distinguishable at a glance, otherwise you believe you encrypted when you only
authenticated.

### 3.2 The second technical population: `pwpolicy-`

§3.1 held the v3 case to be an exception born of cryptography. A defect observed on
hardware showed the mechanism was **general**, and it is shipped:

```
POST /api/devices/<css326>/credentials/apply
→ ok:false — "password refused before sending: new password:
             17 characters, the device accepts at most 15"
```

The SwOS connector refused **before sending a single byte**, which is exactly its job.
The cause was upstream: `driver.AdminAccount.PasswordMax` was declared by all three
connectors and **read by nobody**. Result: *no* password could be set on a MikroTik
CSS326 as soon as the site secret exceeded 15 characters. The capability existed
precisely to prevent this case; what was missing was someone to consult it at
generation time.

**What was not done, and why.** Globally capping the generator at the smallest ceiling
encountered would make a whole fleet pay for the constraint of a single model: a site
with one CSS326 and forty-eight Instant On ports would see its forty-nine secrets drop
to 15 characters. It is the use case of §1, word for word — *"a product range that caps
passwords at 16 characters"* — so it is up to the tag mechanism to handle it.

**What is shipped** (`internal/pwpolicy`, `internal/api/pwpolicy_creds.go`): when
setting a password, if the secret actually targeted does not fit the platform's
policy, meshcraft **uses** a compatible credential already present in the site, or
**generates one** targeting the technical tag, tags the device (source `pwpolicy`)
and continues the write. Idempotent: replaying does not create a second credential.

> [!note] The dry run CREATES the technical credential — deliberate, to be arbitrated
> The adaptation happens **before** the `dry_run` branch, and that is intended: a
> trial that does not follow the same path as the write does not verify the write.
> That is how a `dry_run` announces the tag chosen and the reason for the adaptation,
> which is precisely what is expected of it.
>
> The flip side is that a dry run **has an effect in the database**: it can create a
> credential and set a tag, with the two log events that go with them. Nothing goes
> to the device — the promise `dry_run` makes is kept where it is risky — but "dry"
> does not mean "without trace". To be settled: either the gesture's name changes, or
> the adaptation becomes a distinct gesture that the dry run merely **forecasts**.

| Kind | Scope | Why this form |
|---|---|---|
| `admin` under `pwpolicy-len<N>` | **imposed tag**, generated by meshcraft | `N` is the length of the secrets meshcraft generates under this policy. The credential is created **only** if no compatible secret already exists in the site — in the sense of the **bounds**, not of the name: a `pwpolicy-len12` perfectly serves a device capped at 15 |

**Why the POLICY and not the MODEL.** The tag could name the range (`…-css326`). It
names the constraint, and that is the v3 reasoning: the v3 key derives from the **pair
of algorithms** and not from the model, because *what must share a secret is what
shares the constraint*. One tag per model would give as many secrets as catalogue
references, whereas two ranges with the same bounds share theirs perfectly well — and
multiplying secrets means multiplying what has to be rotated, revealed and monitored.

Two consequences of form, each deliberate:

- **the length alone**, and not both bounds (`pwpolicy-min0-max15`). What decides a
  secret's compatibility is its size: two policies `0..15` and `15..15` accept exactly
  the same 15-character secret, and giving them two names would manufacture two
  secrets for a single need. `len15` is the **exact equivalence class**;
- **`AlphanumericOnly` does not enter the name**, although it does enter the judgement
  of an existing secret. `internal/pwgen` generates **only** alphanumerics: every
  secret coming out of meshcraft already satisfies this constraint, and naming it
  would split the fleet between two tags whose secrets would be interchangeable. It
  does serve, on the other hand, to judge a secret **entered by hand**, which may
  carry punctuation — and it is that judgement which triggers generation. The day
  `pwgen.Alphabet` gained punctuation, this deduction would fall: a test locks its
  content.

> [!warning] The refusal on entry is the design's "irrevocable"
> The `pwpolicy-` namespace is forbidden in all **three** operations gestures:
> creation, modification, deletion. The judge is unique (`pwpolicy.IsTechnicalTag`)
> and so is the message; the path is not — the first two gestures go through
> `checkCredScope`, deletion queries the judge directly. The difference has its
> reason: `checkCredScope`'s list of kinds is **closed**, and routing deletion
> through it would make a credential of a *legacy* kind permanently undeletable. A
> safeguard that prevents cleaning up an old row is no longer a safeguard.
>
> The refusal covers the whole prefix and not only the canonical forms —
> `pwpolicy-len015` looks like the official tag and would be set on no device.
> **Rotation**, on the other hand, remains available: the secret of an entire range
> must be able to rotate. It is merely **bounded** to the length the tag names,
> otherwise a 20-character password filed under `pwpolicy-len15` would reproduce the
> original defect — except that meshcraft would have filed it in the vault itself.

> [!note] What this changes in reading this note
> There are now **two populations of tags** on the same device, and they are not
> handled the same way: **operations** tags (set by hand or by rule, arbitrary,
> removable) and **technical** tags (set by meshcraft, reserved sources, untouchable
> by a replay). This second population now has **two families**, and they decide
> different things:
>
> | Family | Source | What it decides | If it disappears |
> |---|---|---|---|
> | `snmpv3-<auth>-<priv>` | `snmp` | the SNMPv3 identity shared by a cryptographic family | monitoring is unplugged, silently |
> | `pwpolicy-len<N>` | `pwpolicy` | the administration password of a range whose policy excludes the site secret | the device falls back on a secret its connector refuses: no password can be set any more |
>
> The UI distinguishes them (**shipped**): token marked with a padlock, no removal
> cross, with the tooltip saying which of the two mechanisms set it; and on the
> credentials screen, a reserved scope offers neither "Edit" nor "Delete". Presenting a
> technical tag as freely editable filing invites deleting it — and depending on the
> family, that unplugs monitoring or closes the door.

## 4. Automatic tagging

Organising two hundred devices by hand does not get done, so it does not get done at
all. The rules observe what discovery has **already** found — no additional probe:

| Criterion | Compared with | Form |
|---|---|---|
| `vendor` | `devices.vendor` | substring, case-insensitive |
| `model` | `devices.model` | substring, case-insensitive |
| `location` | `devices.location` | substring, case-insensitive |
| `ip_prefix` | `devices.mgmt_ip` | CIDR prefix (IPv4 or IPv6) |

The filled-in criteria of a rule are combined with **AND**. A rule with no criterion at
all applies to nothing: it is an input error, not a rule that takes the whole fleet.
The safe default is to do nothing.

Substring rather than regular expression: the values compared are vendor labels
(`HPE Networking Instant On Switch 48p … 1930 JL685A`), which an operator recognises at
a glance but does not know how to turn into a regex. A wrong regex does not say it is
wrong — it matches nothing.

An **unreadable** CIDR prefix does not fall back on "no filter": it matches nothing.
The difference is the whole subject — falling back would turn a typo into a rule that
takes the whole fleet, hence that silently reassigns it a password. Same logic for a
device without a known management address: it is not *in* the range, it is *without
an address*, and counting it in would assign it a secret on the strength of absent
information. `Match.Problems()` states these cases in plain words, because a rule that
never matches and does not say why is the worst of settings: it looks set.

### 4.1 When the rules run

- at the **adoption** of a device and every time its identity changes (model, vendor,
  address, location): that is when the information arrives. **Shipped** —
  `autoTagDevice`, called from the adoption of a candidate and from the update of a
  device. **Silent on failure**, and that is deliberate: not tagging is an annoyance
  repaired on the next pass, making an adoption fail for that would be a mistake;
- **on demand**, across the whole site, after modifying a rule
  (`POST /api/sites/{id}/tag-rules/apply`, `site_admin`). The preview is the
  **default**: without `apply: true`, nothing is written. It is not a convenience —
  it is what makes the operation reversible in the operator's head before it is in
  the database. Only the devices actually concerned are returned: a list of two
  hundred lines of which three matter does not get read.

Re-evaluation is **idempotent** and only touches tags with source `rule:*`. A disabled
rule removes its tags; it does not abandon them in place. The same tag set **by hand
*and* by a rule** keeps the manual source and therefore survives the disabling of the
rule: the human decision wins.

### 4.2 What automatic tagging does NOT do

It **never** changes a password on its own. A rule that would move a device from one
credential to another modifies the *expected* secret, not the *installed* one: the
device stays on what it carries until an explicit `credentials/apply`, and the
difference is visible in the fleet view (§10 of [[Service identity and rotation]]).

It is the same rule as everywhere else in meshcraft: the database learns from reality,
it does not decree it. A tagging that triggered cascading writes on the fleet would
turn an input error into an incident.

There is nonetheless a catch-up, and it had to be planned: retagging a device makes
it switch from one credential to another, and the new one has **never been set on
it**. Without a net, meshcraft would try a secret it never wrote and ignore the one it
wrote itself — on a range without a serial port, that is repaired by a factory reset.
`store.PreviousInstalls` therefore also returns what the device is **known to carry**
under other credentials of the same site. It is the rule *never cut off your own hand*
applied to scope: **changing your mind about how a device is filed must never cost its
access.**

## 5. Traceability

All these events are **emitted** today, to the [[Event log]]:

| Event | Emitted by |
|---|---|
| `tag.assigned` | manual setting, **and** setting of a technical tag by one of the two provisionings — with, in that case, the sentence saying *why*: it is what decides the v3 secret the device will share, or the password adapted to its policy |
| `credential.generated` | the generation of a credential **nobody asked for**: SNMP identity (§3.1) or policy password (§3.2). The message says what made it necessary — for the latter, the refusal the platform would have opposed to the site secret, length included and **never** the secret |
| `tag.removed` | manual removal |
| `tag.rule_changed` | creation, modification, deletion of a rule |
| `tag.rules_applied` | replay on a site, with the count **and** the reminder that no password changed on the fleet |
| `credential.scope_ambiguous` | the case of §3: several credentials of the same specificity |

The replay does **not** log device by device, and that is consistent with the rest of
the vault: a replay on two hundred machines would produce two hundred lines for a
single gesture. The aggregated count plus the prior preview say the same thing without
drowning the log.

### Decided / shipped / open

| Question | State |
|---|---|
| Multiple tags per device, distinct from the group | **shipped** — `device_tags` (`0013`) |
| Credential scope by tag, resolved in a single place | **shipped** — `store.CredentialsForDevice` |
| Total and deterministic resolution order | **shipped** — `specificity`, `priority`, `tag`, `id` |
| Automatic tagging rules with AND, substring and CIDR | **shipped** — `internal/tagging`, pure and table-tested |
| Replay at adoption and on identity change | **shipped** — `autoTagDevice` |
| Site replay, with preview by default | **shipped** — `POST /api/sites/{id}/tag-rules/apply` |
| `snmp` source and reserved `snmpv3-` namespace | **shipped** — not planned by this note, imposed by [[SNMP identity]] |
| `pwpolicy` source and reserved `pwpolicy-len<N>` namespace | **shipped** — not planned by this note either, imposed by a refusal observed on a CSS326 (§3.2) |
| Reading `driver.AdminAccount.PasswordMin/Max/AlphanumericOnly` | **shipped** — `internal/pwpolicy`, sole translator of the capability into a constraint; `pwgen.Length` / `pwgen.NewBounded` generate under bounds, and **refuse** an unsatisfiable constraint instead of truncating |
| Automatic, idempotent generation of an adapted credential | **shipped** — `ensureAdminSecretFits`, called from the only place that writes a password |
| Refusal of inconsistent scopes according to kind | **shipped** — `checkCredScope` (§3.1), now also consulted on **deletion** |
| Catch-up of secrets already set under another scope | **shipped** — `store.PreviousInstalls`. It is what makes painless the switch of a device from the site secret to its technical tag secret: the new one was never set on it |
| `tag_rules.priority` | **inert** — column present, never read (§2) |
| Visual distinction between operations tag and technical tag | **shipped** — padlock, no removal cross, tooltip per family; and on the credentials screen, a reserved scope offers neither editing nor deletion |
| Migrating credentials targeting a **group** to a tag | **decided, not built** — step 2 of [[Switch profiles]]; it moves passwords, so preview first |
| **Name form: `pwpolicy-len<N>` or `<range>`** | **open** — operations had proposed a per-model name (`wksecret-css326`); the implementation chose the equivalence class by length, for the reason of §3.2. Both hold up: the per-model name reads without knowing anything about the mechanism, the per-policy one avoids multiplying secrets. A single place decides (`pwpolicy.Policy.Tag()`), so the change is local — but it **would rename tags already set**, which is not a cosmetic renaming: it is a move of credential scope |
| **`AlphanumericOnly` too coarse, hard-wired composition** | **open** — see the callout of [[Connectors]] § *Capabilities*: SwOS's real constraint is *ASCII only*, declared in prose and enforced in the connector; and "uppercase + lowercase + digit" is hard-wired in `pwgen` for the whole fleet whereas it is a platform policy |
