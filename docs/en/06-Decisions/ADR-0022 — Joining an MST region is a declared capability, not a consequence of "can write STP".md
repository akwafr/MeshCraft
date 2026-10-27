---
Status: Accepted
Date: 2026-10-25
---

# ADR-0022 — Joining an MST region is a declared capability, not a consequence of "can write STP"

## Context

ADR-0021 made the region identity **computable and visible**: `siteMSTRegion`
derives the triplet (name, revision, VLAN→instance table) from the site alone, and
`GET /api/sites/{siteID}/stp-mst-region` displays it. What has never been
modelled is **which connectors know how to set it**.

Today, two separate facts are conflated into one:

- `Connector.WritesExtra("stp")` says "this connector's spanning-tree writes
  are declared";
- `Capabilities().STPModes` says "here are the protocols this product line
  runs".

Neither says "this connector knows how to write a name, a revision and a
VLAN→instance table". And `internal/api/stp_apply.go` fills
`SpanningTreePlan.Region` **unconditionally**, for every device of a
site, without looking at what the connector will do with it.

### What this produces, measured on 2026-10-25 on ARENA

The eight reachable Arubas carry the expected region — `mst-7f0d238df180`,
revision 1, instance 1 = `10-11,42,60,98-100`. The two other devices of the
same L2, whose region had never been read back, carry this:

| | `core01-arena` (Arista EOS 4.28.13M) | `sw01-arena` (FS S3400-48T4SP) |
|---|---|---|
| mode | `mstp` | **`RSTP(2004)`** |
| region name | `[]` (empty) | **`649D9929F706`** — its own MAC |
| revision | 0 | 0 |
| instance 1 | *none* — `0 1-4094` | *none* — `0 1-4094` |

Both are therefore **single-member regions**, and the Arista prints it
itself: its ten ports towards the Aruba fleet are `P2p Boundary` ports.

The FS is even further off: it is not running MSTP at all. The "MST
Region" its CLI displays is inert, and its default name is its MAC — the
exact case `MSTRegion.Configured` describes as "each device is its
own region".

**And the FS has received applies.** It declares `WritesExtra("stp") = true` and
`STPModes = ["rstp"]`; the site resolver therefore chooses `rstp`, the plan arrives
**with a configured region**, the connector writes the bridge, and says **nothing**
about the region. `ApplyResult` comes back clean. A device outside the region, with a
successful apply behind it and nothing on screen to contradict it: it is the silent
failure of ADR-0021, obtained this time not by a rename but by a
**capability never declared nor refused**.

### Why we cannot simply switch the S3400 to MSTP

Because the gesture has been measured, and it is bad. Observed on 2026-08-11 and written
in the header of `internal/driver/fs/s3400/stp.go`: `spanning-tree mode <m>`
**closes the SSH session before the CLI answers**, the tree reconverges, and for
~45 s `show spanning-tree` answers "No spanning tree instance exists.". The
connector would return an error on top of a device it **has** changed, and
would verify its work against an empty tree.

On top of that, the **region write grammar has not been captured** on
this platform. Reading has been since today — `show spanning-tree mst
region`, a form specific to this product line: `show spanning-tree mst configuration` is
answered with "Too many parameters". Capturing the write side requires entering
configuration mode on a production device, which has not been done.

## Decision

**A connector DECLARES that it knows how to join a region, and a connector that
receives a region it cannot set SAYS SO, on every path.**

Three pieces.

1. **`mdpv1.FeatMSTRegion` (`"MSTRegion"`) joins the vocabulary of
   `Capabilities.Features`.** It means exactly "this connector writes
   `SpanningTreePlan.Region`: the name, the revision and the table". It is
   **separate** from `WritesExtra("stp")` and separate from `STPModes`, for the reason
   `FeatSTPSuperRoot` already carries in its comment: knowing that a product line runs
   MSTP and knowing how to write a region to it are two different facts, and
   the one that matters to the operator is the second.

   Additive: one more key in a `map[string]bool` already on the wire. A
   v1 driver that does not know it leaves it absent, which is the right default
   answer — "I do not know how to set a region" is the safe assumption.

2. **A configured region that is not set produces an
   `ApplyResult.Skipped` line, by name.** Not a silence, not a generic note:
   the name of the requested region, the platform reason, and the consequence in
   plain words — this device remains its own region and switches outside the
   site's tree. EOS already did this for the "region supplied under rstp" case; the rule
   becomes general and applies to the S3400, which said nothing.

3. **`arista-eos` declares the capability; `fs-s3400` refuses it.** The Arista already
   writes the full triplet (`spanning-tree mst configuration` / `name` /
   `revision` / `instance N vlan …`), verified on capture and on a real apply on
   2026-08-12: the declaration promises nothing new, it makes readable what
   was already true. The FS refuses, with the two reasons above written in the
   line the operator reads.

## Consequences

- **`sw01-arena` will stay outside the region until a separate operational
  decision**, and that is now said on every apply instead of being kept quiet. The price is
  real: a production switch that switches perfectly and is not
  in the tree the drawing describes. It is accepted because the alternative — switching
  it to MSTP in the course of an automatic apply — cuts the session mid-gesture
  and verifies its work against an empty tree.

- **Setting it on `core01-arena` remains a planned gesture, and the commit does not
  do it.** The Arista is CIST root at priority 0 with all 4094 VLANs, and ten of
  its ports are MST boundaries. Having it join the region moves
  VLANs 10, 11, 42, 60, 98, 99 and 100 from the CIST to instance 1: it is a
  topology recomputation on the site's core, not a setting.

- **Three latent defects of the EOS write path are closed in the same commit**,
  because they all have the same shape — a line the device refuses, in
  a block that `runConfigBlock` abandons at the first error, which takes the
  region **and** the mode down with it:

  - an `instance 0` entry in `InstanceVLANs` was emitted as is, whereas
    the CIST is implicit and mapping VLANs into it is refused;
  - the revision went out through `strconv.Itoa` without bounds, so a value outside
    0–65535 produced a refused line;
  - `sanitizeRegionName` fell back to the literal string `"meshcraft"` for any name
    of which no character survived the filter. That is **the alternative
    explicitly rejected by ADR-0021** — a single fixed name for the whole
    instance — reached by accident: two sites pinned to distinct
    non-ASCII names would have shared a region.

- **A `Features` without the key is not a refusal.** An absence means "this
  connector has not declared that it knows", which is the safe default, but the caller
  must not conclude from it that the device was warned. Only the
  `Skipped` line of point 2 is proof that the refusal was pronounced.

- **The key is not documented in `openapi.yaml`, and that is the existing
  convention.** No key of `Capabilities.Features` is: the map travels
  as a free `map[string]bool` in `schemas.json`, and the vocabulary lives in
  the comments of the `wire/mdpv1` constants. Adding a section for
  this one alone would have created two places of truth. `schemas.json` is therefore
  unchanged, which can be verified: generation reflects types, not
  constant values.

## Rejected alternatives

**Inferring the capability from `STPModes` containing `"mstp"`.** The shortest: no
key to add. Rejected because it repeats exactly the mistake that
`FeatSTPSuperRoot` was expensive to correct. `STPModes` answers "which protocols
this product line runs" — a fact about the hardware — and the question asked here
is "what can this code write", a fact about the connector. Instant On and EOS
both list `mstp` and set the region; nothing prevents a connector from
listing `mstp` and writing only the mode line, and that connector would then be
announced as able to join a region. The inference is true today
by coincidence, and an inference true by coincidence is an inference that
will break without a sound.

**Inferring the capability from `WritesExtra("stp")`.** Even shorter, and wrong
right now: the S3400 declares it. It is the confusion this ADR exists to
undo.

**Not sending a `Region` to connectors that cannot set it.** Clean
on the surface: `stp_apply.go` queries the capability and leaves the field empty.
Rejected because it **removes the refusal** — a plan without a region is a plan
that has nothing to refuse, the connector has nothing left to say, and the device
silently goes back to being outside the region. Yet the only deliverable that matters here is
that the operator SEES that this switch is not in the tree. The field goes out,
and the refusal comes back.

**Writing the S3400's region grammar behind the mode refusal, "ready for
later".** Tempting, and it is precisely the trap the contract names. The
write form has not been captured on this platform, and this product line has already
produced the demonstration that guessing is costly: `spanning-tree mstp instance 0
priority 36864` — the form inferred from a neighbour's grammar — is **silently
accepted and configures nothing**. Unverified code behind a refusal
becomes verified code on the day someone lifts the refusal believing they are lifting
only one thing.

**Switching the S3400 to MSTP and setting the region.** That is what the statement of the need
literally asks for. Rejected on the 2026-08-11 measurement cited in the context: the
mode change closes the session before the answer, and leaves ~45 s during
which the device is unreadable. It is not a command limit, it is
a gesture this connector does not know how to conduct — it would need a
reconnection, a wait for reconvergence and a re-read, which is a
mechanism to build and not a line to add.

**One flag per sub-part of the triplet** (can write the name / the revision / the
table). Rejected because the triplet is not divisible: 802.1s compares all
three or nothing. A connector that could write the name and not the table would
join no region, so the partial capability has no value to
expose, and three flags would invite declaring only one.
