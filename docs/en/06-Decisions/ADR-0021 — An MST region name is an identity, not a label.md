---
Status: Accepted
Date: 2026-10-25
---

# ADR-0021 — An MST region name is an identity, not a label

## Context

`internal/api/stp_apply.go` derives each device's MST region from the **site slug**:
`siteMSTRegion` reads `sites.slug`, passes it to `mdpv1.SiteMSTRegion`, and the region
name that goes out on the wire is that slug, truncated to 32 characters.

The comment of this function already names the danger, and explains the choice of
the slug by that danger:

> A region needs a stable name and the slug is the only one guaranteed unique
> per site. Without it, deriving from the site name would produce a region that
> changes the day someone renames the site — which silently evicts every device
> not re-pushed.

The reasoning is right. Its premise is wrong: **the slug is not stable.**
`PATCH /api/sites/{id}` accepts a `slug` field and `Store.RenameSite` writes it. The
slug can be modified by the operator, exactly like the name.

### What a rename costs, and why it is not cosmetic

802.1s does not decide membership of a region by asking for it. Each bridge announces
a **triple**: the region name, the revision number, and an MD5 digest computed over the
complete table of the 4096 VLAN→instance associations. Two bridges are in the same
region **if and only if** all three match, to the bit.

A renamed device is not "a device with a different label". It is **out of its
region**: it becomes its own one-member region, MST boundaries move, and the topology
is recomputed. On a production fabric that is an outage.

And this failure is **silent**. Nothing raises an error, no link goes down, no log line
appears: the neighbour is simply treated as an ordinary RSTP bridge, and the only
symptom is a blocking port where nobody expects one. `mdpv1.MSTRegion` already carries
this comment; it describes a risk that has materialised.

### The survey of 2026-10-25: the ARENA fabric is already split

Twelve Aruba Instant On 1930 on ARENA, `192.0.2.200`→`212`, read back read-only
through the reconnaissance route `GET /api/devices/{id}/wcd/*` (sections `MSTP`,
`SpanningTreeGlobalParam`, `MSTPInstanceList`, `MSTPVLANList`). Eight answer, four are
offline (`.209`, `.210`, `.211`, `.212`: *no route to host*). The eight reachable ones
are all in MSTP (`enabled=1`, `STPOperationMode=3`), revision 1, `maxHops=20`.

And they carry **two distinct region identities**:

| Identity | Name | Revision | Instance 1 | Members |
|---|---|---|---|---|
| 1 | `site-exec` | 1 | `42,98-100` | ArubL, ArubA, ArubC, ArubD, ArubF, ArubG, ArubH (7) |
| 2 | `arena` | 1 | `10-11,42,60,98-100` | ArubB (1) |

The site's VLANs are today 1, 10, 11, 42, 60, 98, 99, 100. Excluding VLAN 1 — which
belongs to the CIST on every platform — the expected table is `10-11,42,60,98-100`.
**ArubB is the only device compliant with the current plan.** The seven others carry
an earlier state, and the divergence is twofold:

1. **The name**: `site-exec` is an old slug of the site, `arena` is the current slug.
   The site was renamed, and only ArubB has been re-applied since.
2. **The VLAN→instance table**: VLANs 10, 11 and 60 were added to the site, and only
   ArubB has been re-applied since.

Either of the two, alone, is enough to split the region. ArubB is therefore a
one-member region, isolated from the seven others, and the MD5 digest it announces
matches that of no neighbour. Nobody saw it, because **the region name is displayed
nowhere in the interface**: neither `SpanningTreeConfig.tsx` nor
`SpanningTreeDevice.tsx` mention it. A quantity the topology depends on, which no
screen shows.

That is the second fact that forces this ADR: deriving from a modifiable identifier is
not a theoretical risk, it has already produced a silently split fabric.

## Decision

**The name of an MST region is never derived from data someone can rename, and a name
already set on hardware does not change because the code changed.**

Three pieces, and it is the third that carries the guarantee.

1. **The generic name is derived from the site identifier, which is immutable.**
   `mdpv1.GenericMSTRegionName(siteID)` returns `mst-` followed by the first twelve
   hexadecimal characters of the site's UUID, i.e. sixteen characters — well below the
   32 limit. The UUID is the primary key of `sites`: no route modifies it, and none
   will without it being a deliberate data migration. The name is therefore stable
   **by construction**, not by convention.

   It is generic in the sense the user asks for: it carries no readable text, so nobody
   has a reason to want to "correct" it. A name that says nothing is a name nobody
   renames.

2. **A name pinned in the site plan is authoritative.**
   `sites.plan` is a free `jsonb` that already carries `stp_mst_revision`. The
   `stp_mst_region` key joins it. When it is present and non-empty, `siteMSTRegion`
   uses it **verbatim** and derives nothing. When it is absent, the generic name of
   point 1 applies.

   Pinning is therefore the mechanism by which an existing name survives the code
   change, and also the one by which an operator can deliberately align a site on a
   name that hardware already carries.

3. **Migration 0047 pins the current slug of ALL existing sites.**
   That is the no-rename guarantee, and it is mechanical, not declarative:

   ```sql
   update sites
      set plan = plan || jsonb_build_object('stp_mst_region', slug)
    where coalesce(slug, '') <> ''
      and coalesce(plan->>'stp_mst_region', '') = '';
   ```

   After this migration, every site that existed carries a pinned name **equal to the
   slug**, that is, exactly what the code produced the day before. The region computed
   for each of these sites is therefore **byte-for-byte identical** to the one before
   the change. No device already set up can be renamed by this commit, because the
   value does not move.

   The generic name only applies to sites **created after** the migration, which by
   definition have no hardware carrying a region.

4. **The naming rule does not decide the VLAN→instance table.** It remains that of
   `SiteMSTRegion`: all the site's VLANs except 1 in instance 1. This ADR only touches
   the name.

5. **The region identity becomes visible.**
   `GET /api/sites/{siteID}/stp-mst-region` returns the triple the controller would
   apply — name, revision, table per instance — and `SpanningTreeConfig` displays it
   read-only, under a `HelpTip` stating the rule: the three values form an identity, a
   device on which one differs leaves the region without anything signalling it, and
   modifying the site's VLAN list requires re-applying spanning-tree on the whole site.

   The route is read-only and touches no device: it reads the database and recomputes,
   as `siteMSTRegion` does. It is not a hardware survey.

## Consequences

- **Existing sites keep a name derived from a slug, hence a name that "talks".** It
  is the direct price of point 3: genericity is only obtained on new sites. ARENA will
  stay pinned on `arena` until someone decides otherwise. Accepted without reservation:
  an ugly, stable name is strictly better than a clean, moving one, and it is the
  movement that cuts traffic.

- **This commit does not repair ARENA, and must not claim to.** After migration, the
  pinned name is `arena` — ArubB's, not the seven others'. A re-application of
  spanning-tree on one of the seven would rename it from `site-exec` to `arena`.
  **That re-application would already rename before this commit**: the current code
  derives `arena` from the current slug for all the site's devices. The change is
  therefore behaviour-neutral, and the pre-existing split is neither caused nor cured
  by it.

  What it does bring is a cheaper way to repair it. Pinning `stp_mst_region` to
  `site-exec` via `PUT /api/sites/{id}/plan` realigns the intent on what **seven**
  devices already carry, and reduces bringing the region back together to a single
  gesture on ArubB. Without pinning, convergence required renaming seven devices in
  production. The choice remains the operator's, and it must be planned: the route
  does not do it on its own.

- **The danger of renaming is now reduced, not removed.** `stp_mst_region` is a plan
  key, and `PUT /api/sites/{id}/plan` writes it. An operator can still rename a region
  and eject their fleet. That is deliberate: it must be possible, precisely for the
  ARENA case above. What changes is that a rename becomes an **explicit gesture on a
  named key**, and no longer the side effect of a slug change undertaken for an
  unrelated reason.

- **The generic name is opaque to troubleshooting.** Faced with an `mst-7f0d238df180`
  read on a switch, it takes a query to know which site it is. It is the exact flip side
  of the property that makes it safe, and the reason for point 5: if the name no longer
  reads, the screen must show it. The `mst-` prefix is kept so that a human at least
  sees it is a region managed by meshcraft, and not a factory name derived from the
  MAC.

- **A name collision remains theoretically possible.** Twelve hexadecimal characters
  is 48 bits; two colliding sites would carry the same name. It is not a safety
  problem: two distinct sites have distinct VLAN lists, hence distinct digests, hence
  distinct regions despite the common name. And two sites sharing both the name and the
  VLAN table are two sites it would be correct to merge into one region.

- **`SiteMSTRegion` no longer decides the name.** Its first parameter stops being "the
  site slug" and becomes "the region name", and the function confines itself to
  validating and truncating it. The naming decision moves up into `internal/api`, where
  the plan and the site identifier live. `wire/mdpv1` remains what it should be: the
  shape on the wire, not the policy.

## Rejected alternatives

**Keep the slug and forbid changing it.** The shortest: refuse the `slug` field in
`PATCH /api/sites/{id}`. Rejected because it pays for the problem with an unrelated
feature — the slug serves as a readable identifier in URLs and exports, and freezing it
to protect MSTP makes the whole product carry a constraint born of a spanning-tree
protocol. And it repairs nothing: `site-exec` is already set on seven devices, the slug
is already `arena`, the split is already there.

**A single fixed name for the whole instance, for example `meshcraft`.** Maximum
genericity, and the simplest to read. Rejected because it makes the region identity
depend on the VLAN table alone. Two L2-adjacent sites — which happens with a site
extension — would carry the same name and the same revision, and their membership of
the same region would only hinge on the equality of their VLAN lists. The day they
coincide, two sites merge into one region without anybody having asked, and the
diagnosis is the worst there is: everything matches, and the topology is not the one in
the drawing. Deriving from the UUID keeps sites separate by construction.

**A name taken from the site's display name, simply "sanitised".** That is the origin
of the problem, only worse: the display name changes more often than the slug.

**Pin the name at the first `apply`, rather than by migration.** Attractive: nothing
to migrate, and the name freezes exactly when it touches hardware. Rejected for a
window reason. Between the deployment of the code and the first `apply` of an existing
site, the pinned name is absent, so the generic name applies, so **the first `apply` is
the rename** — precisely the event this ADR exists to prevent. The trap is that the code
would look correct: the "do not rename" rule would be written, and violated by the order
of operations. The migration closes the window before it exists.

**Pin, at migration, what the devices actually carry, rather than the slug.** The only
option that would have repaired ARENA automatically: read the fleet's majority region
and pin it. Firmly rejected. An SQL migration does not talk to devices; it would take a
task opening a session on every switch of every site at deployment, which is a read
campaign on a production fleet triggered by a `migrate`. And "the majority region" is a
notion that makes no sense when the fleet is half offline: four of ARENA's twelve
Arubas do not answer, and nothing says which region they will carry when they come
back. The choice of aligning a site on `site-exec` is an **operator gesture**, informed
by a survey, not an automatic deduction.

**Display the region name as an editable field on the spanning-tree screen.** Rejected
for now: a text field invites editing, and that is the gesture we want to make rare.
Read-only plus a `HelpTip` states the quantity and its risk without offering the
button; a deliberate rename goes through the site plan, which is already the surface
for a site's costly decisions. If the need for an assisted rename arises, it will
deserve its own screen, with the list of devices it will eject and in which order to
bring them back — which is a wizard, not a field.

**Display nothing, and document the rule in the vault.** That is the state before this
commit, and it produced a split fabric nobody saw for weeks. An invariant displayed
nowhere is not monitored.

## Addendum of 2026-10-25 — the arbitration on ARENA

The split observed on ARENA was brought to the user: seven Arubas in `site-exec`
(instance 1 = `42,98-100`), **ArubB alone** in `arena` (instance 1 =
`10-11,42,60,98-100`, the only one compliant with the site plan).

**Decision: keep `arena`.** It is what the pinning migration already retains, so this
choice triggers **no write** and behaviour is unchanged.

**The consequence is a trap, and that is why it is written here.** The site's intent
names `arena` while seven devices carry `site-exec`. So **applying spanning-tree on one
of these seven renames it**, ejects it from the region of the six others and moves an
MST boundary. It is not a regression introduced by this ADR — the previous code already
did exactly that — but the danger is now *known*, which changes what we are allowed to
ignore.

Resulting operating rule: **no STP apply on ArubL, ArubA, ArubC, ArubD, ArubF, ArubG,
ArubH without having planned it**. Seven writes, seven recomputations. The reverse
convergence — realigning on `site-exec` and touching only ArubB — remains available
through `PUT /api/sites/{id}/plan` if someone prefers it later.

Two unknowns remain: the **4 offline Arubas** may come back with a third identity, and
the region of the Arista `core01-arena` as well as that of the FS `sw01-arena`, which
are on the same L2, **has never been verified** — the `wcd` route is specific to Instant
On and `read-state` was out of bounds.
