---
tags: [architecture, model, frontend]
---

# Switch profiles

A **switch profile** is a set of **rules** that produces the configuration of a
device's ports. It replaces the device groups screen, and it answers a request
phrased in three sentences:

> all SFP+ as trunks; the last four RJ45 as trunks with PVID 100;
> all other ports as access PVID 100.

These three sentences describe **any** switch in a fleet. None of the existing
objects could store them: a socket profile ([[Profiles and groups]]) says what
**one** socket receives, never **which ones**.

## 1. Diagnosis: what groups really carried

The initial request was to do everything with [[Tags and credential scope|tags]] and to remove groups. The state of the code says otherwise, and it had to be
looked at before dismantling anything.

| What the group is supposed to carry | What is actually the case |
|---|---|
| a **device profile** (`device_groups.device_profile_id`) | **inert**. No Go code **interprets** `device_profiles.spec` — the `meshcraft_site_config` import/export carries it as is, as opaque JSON, and that is all. The inheritance described in §1 "Compile" of [[Reconciliation]] does not exist |
| the choice of **credential** (`site_credentials.group_name`) | **alive, but unreachable**. No screen assigns a device to a group: the only way is the `meshcraft_site_config` import or a direct API call |
| a fleet **grouping** | never displayed anywhere |

And on the tags side, everything is already there: `site_credentials.tag` has
existed since migration 0013, the `tag > group > site default` resolution is
written in a single place (`store.CredentialsForDevice`), rule-based automatic
tagging works, and the API already refuses a credential targeting a group **and**
a tag.

> [!warning] What would break if groups were ripped out
> `site_credentials.group_name` is **not** a foreign key: it is a match by NAME.
> Deleting the `device_groups` table would therefore break nothing at compile time
> — and that is precisely the danger. Credentials targeting a group would
> silently stop applying, and the devices concerned would fall back to the site's
> default credential. On a range without a serial port, getting the password
> wrong is repaired by a factory reset.

### The chosen path, in stages

1. **Shipped** — the groups screen disappears, replaced by the switch profiles
   screen; the old URL redirects to it. The credential form now presents the tag
   **before** the group, and says the group is legacy. The model, the API and the
   import/export of groups remain **intact**.
2. **Next** — a migration that, for each credential targeting a group, sets the
   corresponding tag on that group's devices and switches the credential over to
   the tag. It must be done with a **preview first**, like `tag-rules/apply`,
   because it moves passwords.
3. **Finally, and only then** — remove `devices.group_id`, `device_groups` and
   `site_credentials.group_name`, with a migration that refuses to apply if any
   credential targeting a group remains.

Stage 2 is not in this batch: it touches the scope of secrets, and there is no
hurry as long as the "group" axis counts **no** credential in production.

## 2. What the rules operate on: the faceplate

`devices.port_groups` describes the front panel in homogeneous blocks:
`{prefix, from, count, speed, poe, label}`. It is the only description of the
device meshcraft has offline, and therefore the only one a profile can reason
about.

`portrules.Expand` unfolds it into ordered sockets, and **infers** for each one a
`media` the faceplate does not declare:

| Observation | Classification |
|---|---|
| neither the prefix nor the label contains "sfp" | `copper` |
| speed ≥ 10G, or label containing "SFP+" | `sfp_plus` |
| otherwise | `sfp` |

The prefix **and** the label are both queried because neither is enough: the
catalogue distinguishes by prefix (`ether` / `sfp-sfpplus`), the Instant On
connector names **everything** `GE` and only distinguishes by label ("1–48 · PoE"
versus "SFP+"). Looking at only one would pass half the fleet off as copper —
silently, and "all SFP+ as trunks" would no longer designate anything.

## 3. Selectors

A selector works in **two steps**, and that is what makes it portable.

**A filter**, socket by socket:

| Criterion | Values | Note |
|---|---|---|
| `media` | `any` / `copper` / `sfp` / `sfp_plus` | `sfp` **also** takes SFP+: that is what one means by "the fibre ports" |
| `poe` | `any` / `yes` / `no` | the socket's **capability**, not the intent set on it |
| `group` | substring of the faceplate group's label | case-insensitive |

**Then a slice**, over the set thus obtained: `all`, `first N`, `last N`, or
`range N..M`.

> [!important] The slice operates on RANK, never on the port number
> Numbers do not compare from one faceplate to another: the catalogue restarts
> at 1 per prefix (`ether1…48`, `sfp-sfpplus1…4`) whereas an Instant On numbers
> its SFPs continuing from the copper (`GE49…52`). A rule written on numbers would
> designate two different sets on two models — which is exactly what a profile
> must not do.

Bounds are **clipped**, not refused: "the last 4" on a faceplate that has only 2
designates 2, "ranks 1 to 44" on a 24-port switch designates 24. Refusing would be
defensible on a form; on a profile meant for a heterogeneous fleet, it would amount
to having no profile at all.

Substring `group` matching follows the doctrine of tagging rules (§4 of
[[Tags and credential scope]]): labels are not normalised from one model to
another, and equality would make the rule silent on the second model. The
counterpart is stated on screen — targeting a label ties the rule to a range.

### What was ruled out

- **regular expressions** on the port name: a wrong regex does not say it is wrong,
  it designates nothing;
- **selection on observed state** (active link, negotiated speed, LLDP neighbour):
  telemetry is not an intent. A profile whose result changes depending on whether a
  cable is plugged in would not be replayable, and the preview would be worth
  nothing;
- **per-port exceptions in the profile**: that is the role of per-port touch-ups,
  which already exist in the switch view.

## 4. Conflicts: the first rule wins

Conflict is certain — a catch-all rule necessarily overlaps others. Three
arbitrations were possible.

| Arbitration | Verdict |
|---|---|
| **specificity** (the most precise rule wins) | ruled out. It requires a total order on criteria combinations, which the operator does not see and must learn. Is "the last 4 PoE RJ45" more precise than "the SFP+"? Any answer is arbitrary, and an invisible arbitration is discovered in production |
| **last rule** (CSS-style) | consistent, but forces writing the catch-all **first**, against the way intent is stated |
| **first rule** (access-control-list style) | **chosen**: it literally matches the statement, where the catch-all is written last and must lose |

Two corollaries that matter as much as the rule itself:

- **a socket receives the configuration of exactly ONE rule.** No per-field merge
  between rules: "who set PoE to false on this socket?" must have a one-word
  answer, and a four-level merge has none;
- **selectors are evaluated on the whole faceplate**, not on what the previous
  rules left. "The last 4 RJ45" designates the same set whatever the rule's
  position; only the fact of **winning** depends on the order. The other
  convention would make a rule's meaning depend on everything preceding it: moving
  a rule would silently change the set designated by the following ones.

The screen displays, per rule, **what it designates** and **what it obtains**. The
two numbers differ as soon as a rule placed higher has gone first: a rule that
designates thirty sockets and wins none is not wrong, it is **shadowed** — and that
is indistinguishable if only one of the two is displayed.

## 5. What a rule sets

The four fields of the [[Standard data model]], and nothing more: `mode`
(`access` / `trunk` / `libre`), `pvid`, `tagged` (list or `all`), `poe`. Adding
fields no connector writes would give the preview the air of announcing a
configuration that would never arrive.

A rule can also **point to an existing socket profile** rather than carry its own
setting. That is the raison d'être of socket profiles, and a rule that copied their
content would drift away from them at the first change without anything signalling
it. A deleted socket profile is **not** compensated by a fallback: the rule is
declared faulty, it takes no socket, and the whole apply is refused, naming the
faulty rule.

> [!warning] "all" is never expanded by evaluation
> The site's VLANs are known, though. But `all` is a **floor**, not an
> enumeration ([[Reconciliation]], "the all trunk is a floor, not a list"):
> expanding it here would make it an exact set, which would allow the apply to
> **remove** from a trunk any VLAN the site does not declare. On an uplink, that
> is an outage. Resolution belongs to `recon.Resolve`, in a single place.

A mismatch is corrected rather than refused: PoE requested on a socket that has
none is **ignored** — an "office" profile with PoE must be usable on the SFP cages
of the same device — but it is **said**, otherwise the safeguard would pass for a
bug.

## 6. Uncovered sockets stay untouched

A profile does not speak about everything. What it does not speak about is **not**
reset: it is the "absent = unmanaged" rule of the [[Standard data model]] applied
to profiles. Setting `libre` on undesignated sockets would silently destroy a
configuration nobody had asked to touch, and that cannot be undone. The operator
who wants the opposite adds a catch-all rule at the end — and **sees** it.

Same reason for merging on write: a port's object also carries the socket's
**label** — a meshcraft annotation the device does not know — and the latest
telemetry reading. Rewriting the whole object would erase forty-eight socket
descriptions for one profile application.

## 7. Preview

The preview is not an option one unfolds: it occupies the right panel permanently.
It shows, on a **real device chosen within the site**:

- the whole faceplate, coloured by VLAN as everywhere else
  ([[Port rendering]]) — hovering a rule dims the sockets it did not win;
- the **designated / obtained** count per rule;
- the **socket by socket** table: name, winning rule, produced configuration,
  possible warning;
- the number of sockets left unchanged, and what "all" amounts to on this site.

It is computed by the **controller** (`POST /api/devices/{id}/switch-profile/preview`),
never reimplemented in TypeScript. It is the same trade-off as for
`recon.Resolve`: two evaluations each deciding on their own would end up
diverging, and they would diverge exactly where nobody looks — on the faceplate of
a rare model. The preview accepts an **unsaved** spec, otherwise one would have to
save to see, hence save to find out one was wrong.

## 8. What the apply does, and does not do

`POST /api/devices/{id}/switch-profile/apply` writes into `devices.ports`, and
nothing else. Applying to the hardware follows its usual path: `noteDesiredChanged`
schedules it, grouped with the other changes of the same burst (automatic apply,
[[Reconciliation]] §5). A single `switch_profile.applied` event (or
`port.configured` depending on the path) is emitted for the whole gesture.

There is **no** device → profile foreign key, and that is deliberate. A profile is
**applied**: it produces the ports' intent, which the operator remains free to
touch up port by port. A hard link would turn any local touch-up into a "drift
from the profile" to arbitrate, whereas touching up is the normal way of working.

### Soft assignment by tag

Since migration `0030`, a switch profile can carry an **assignment tag**
(`switch_profiles.tag`, unique per site when non-empty). Setting this tag on a
device (`PUT /api/devices/{id}/tags/…`, or a tagging rule that adds it)
**evaluates and applies** the profile to that device — same path as the manual
apply, hence the same connector safeguards (management VLAN on up ports,
`Force: false`).

Accepted counterparts:

- **removing** the tag does **not** reset the ports (absent = unmanaged);
- **modifying** the profile does **not** re-apply it on its own to the already
  tagged fleet;
- the tag can stay set even if the apply is refused (faulty rule) — membership is
  recorded, the toast / API response says why nothing was written.

The profiles screen keeps a preview on a real device and an "Apply once" to test
a draft without a tag.

### What a rule cannot ask for, because `Config` does not carry it

`portrules.Config` has **four** fields — `mode`, `pvid`, `tagged`, `poe` — and its
comment says why: "adding fields no connector writes would give the preview screen
the air of announcing a configuration that would never arrive". The rule is good;
its consequence deserves to be written down, because these are mundane operator
requests:

| What an operator would ask for | Why no rule carries it |
|---|---|
| **shutting down** unused sockets | there is no "port disabled" intent in the [[Standard data model]]. Both MikroTik platforms have the field (`/link.b` `en`, `disabled`), no connector writes it |
| forcing a speed or disabling auto-negotiation | no intent. `/link.b` `an` / `dpxc` and `/interface/ethernet` `advertise` are **read** for capabilities, never written |
| jumbo frames on uplinks | no intent. RouterOS publishes its ceiling (`max-l2mtu: 10218` observed); SwOS has **no** setting — its `JumboFrames` capability is false and that is a platform fact |
| a trunk that **refuses** untagged traffic | inexpressible: a meshcraft trunk always has a native VLAN ([[Default VLAN]] §3) |
| isolating a group of sockets | no intent. Both platforms can do it (`/fwd.b` `fp1…fp26`, `horizon`), no connector writes it |
| an LACP aggregation over the two SFP+ | no intent, and no name: an aggregate is **one more socket** on the faceplate, which `Expand` cannot produce |

This table is not a to-do list for the `portrules` package: rule evaluation is
complete for what it describes. The gap is **upstream** — in `devices.ports`, hence
in the standard model — and filling it requires deciding three times: the stored
shape, the drift comparison, and what each connector does with it. An intent set in
the model and written by only one connector out of three is exactly what
`WritableExtras` exists to make visible, and `recon.ExtraIntents` already carries
four in that state (`poe`, `stp`, `dhcp_snooping`, `port_security`).

What the apply does, for its part, **beyond** the four fields: since this batch the
MikroTik connectors write the per-port VLAN mode (SwOS) and ingress filtering
(RouterOS), without any rule asking for them. It is not a hidden intent — it is
what makes the four fields **effective** on these platforms, where a VLAN
membership can be written and have no effect. The reasoning is in
[[Connectors]], §*The VLAN model of the two MikroTik platforms*.

## 9. Where it lives

| Element | Location |
|---|---|
| evaluation (pure, tested) | `internal/portrules/` |
| storage | `switch_profiles` table, migration `0021` (+ `tag` in `0030`) — same shape as the two other profiles for CRUD, plus the assignment tag |
| CRUD | `GET`/`POST /api/sites/{id}/profiles/switch`, `PATCH`/`DELETE /api/profiles/switch/{id}` — JSON body: `name`, `spec`, `tag` (switch) |
| preview / apply | `POST /api/devices/{id}/switch-profile/preview` and `/apply`; apply also triggered by `PUT …/tags/{tag}` if the tag matches a profile |
| screen | `web/src/pages/SwitchProfiles.tsx`, route `/{slug}/config/switch-profiles`; the old `/…/config/groupes` redirects to it |

### Decided / shipped / open

| Question | Status |
|---|---|
| Inferring `media` from the prefix **and** the label, with speed as arbiter | **shipped** — `portrules.mediaOf` |
| Two-step selector: filter then slice on **rank** | **shipped** — `Selector.matches` then `Selector.cut` |
| Bounds clipped, not refused | **shipped** — a "last 4" on a faceplate of 2 designates 2 |
| First rule wins, one socket = one rule, selectors on the whole faceplate | **shipped** — `portrules.Evaluate` |
| **Designated / obtained** count per rule | **shipped** — `RuleReport.Matched` / `Won` |
| `all` never expanded | **shipped** — `Config.TaggedAll`, resolution left to `recon.Resolve` |
| Uncovered sockets left untouched; key-by-key merge on write | **shipped** — `Plan.Uncovered`, `Config.Fields()` |
| Global refusal if a rule is faulty | **shipped** — non-empty `Plan.Issues` ⇒ **nothing** is applied |
| PoE requested on a socket that has none: corrected **and said** | **shipped** — `portrules.fit`, `Assignment.Warn` |
| Preview computed by the controller, on an **unsaved** spec | **shipped** — never reimplemented in TypeScript |
| Stage 2: migrating group credentials to tags | **decided, not built** — it moves passwords, hence preview first |
| Stage 3: removing `devices.group_id`, `device_groups`, `site_credentials.group_name` | **decided, not built** — the migration will have to **refuse to apply** if any credential targeting a group remains |
| Persistent device → profile link | **ruled out**, not postponed (§8) — but addable later, whereas the reverse is not |
| Device profile (STP, SNMP, syslog, services) | **decided, not built** — it is the other half of what a group promised, and it remains to be done |
