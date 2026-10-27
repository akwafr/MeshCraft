---
Status: Accepted
Date: 2026-10-24
---

# ADR-0014 — The declared label is evidence, identity is derived

## Context

A user reports "two strictly identical models in the software". There are **three** of
them, and they designate the **same 12 physical switches**, all with
`vendor = "Aruba (HPE)"`:

| occurrences | stored `devices.model` |
|---|---|
| 8 | `Aruba Instant On 1930 48G 4SFP/SFP+ Switch` |
| 3 | `HPE Networking Instant On Switch 48p Gigabit 4p SFP+ 10G 1930` |
| 1 | `HPE Networking Instant On Switch 48p Gigabit 4p SFP+ 1930 JL685A` |

The cause is not a bug: **this vendor publishes two label grammars**, and they come in
through two different discovery paths. `internal/driver/instanton/inspect.go` knows it
and already handles it — the sysDescr form (login page, SNMP, sometimes the catalogue
reference `JL685A`) and the provisioning API's form, which counts without the "p"
("48G 4SFP/SFP+"). Both **must** remain recognised at parse time: without the second,
no regex matches, the 4 SFP+ cages are adopted as copper and the faceplate falls back
flat to 52 ports (it is written in `parsePorts`'s comment, and `inspect_test.go` pins
it).

What is missing is therefore not a read, it is an **identity**. Nothing in the product
says that these three strings name the same hardware.

### The real extent, measured

The problem is **structurally general and currently local**. Four independent
producers write `devices.model`:

1. a connector's `Identify` (the vendor's own grammar);
2. `discovery.ExtractFacts` (`internal/discovery/fingerprint.go`), generic, for all
   vendors;
3. the catalogue and adoption (`internal/catalog`, `cand.Model`);
4. an operator, by hand (`PATCH /api/devices/{id}`, site configuration import) — a path
   with no grammar at all.

But on the real inventory — 37 devices, 6 sites — **only the Instant On 1930 family
shows the collision**. Checked: MikroTik, Cisco and FS have one grammar and only one
per family, and the pairs that come close (`CRS326-24G-2S+` / `CSS326-24G-2S+`,
`S3400-48T4SP` / `S3900-24T4S`) are genuinely different hardware. The reason lies in a
bound: the generic `modelRe` only captures a **single token without spaces of at most
32 characters**, so it cannot produce a sysDescr-style phrase. It has bitten nowhere
else because it is not capable of it.

Latent risks spotted in the code, with no witness in the inventory: MikroTik RouterOS
reads the model in two places (`/system/resource board-name`, then
`/system/routerboard model`, which overwrites it if it exists — the routerboard
reference can carry an enclosure suffix), and SwOS in two others (Digest realm,
`/sys.b`). Same shape of trap, only one grammar for now.

Finally, `matchSNMPIdentity` (`internal/api/reachability.go`) has **already** met the
problem and solved it locally, for itself only: it never compares two labels, it looks
for a distinctive token (four characters, one digit). The comment literally quotes the
`JL685A` form. The decision below generalises that reasoning instead of inventing it.

## Decision

**The label the device declared is never rewritten. The hardware's identity is derived
at read time, and it is what the interface shows.**

1. **`devices.model` stays raw, and no migration touches it.** It is the evidence: the
   day a faceplate is wrong, the first question is "which grammar arrived?", and it only
   has an answer if the string is intact.

2. **Identity is derived, never stored.** `internal/modelid` answers
   `Of(vendor, model) → {Key, Label, SKU}`. It joins `Sync`, `Drift`, `CollectKind`,
   `WatchExcluded` on the shelf of `model.Device` fields computed at read time, and for
   the same reason: a copy in the database would freeze the derivation of the day it was
   written, whereas a computed derivation improves for **all** existing rows the day it
   learns one more grammar.

3. **The canonical identity is: brand + family + faceplate geometry**, and the geometry
   is read **from the label itself**, not from a catalogue. For the three forms above:
   Aruba, Instant On 1930, 48 copper + 4 SFP+ — all three say it, each in its grammar.
   The canonical label is recomposed from that
   (`Aruba Instant On 1930 48G 4SFP/SFP+ Switch`), which invents no name: it is the form
   the vendor's provisioning API emits, and the one 8 of the 12 rows already carry.

4. **The catalogue reference (`JL685A`) is NOT the identity.** It is more precise, and
   that is exactly why it cannot serve: it is **absent from two of the three forms**. An
   identity based on it would re-split the group at the moment we need it — that is to
   say the bug, rewritten differently. General rule: **the canonical identity is the most
   precise thing ALL paths can say, not the most precise thing ONE path says.** The SKU
   is kept alongside (`ID.SKU`), optional, shown when it is there, never required.

5. **When the identity is not reachable, the raw label IS its identity.** If no family
   rule recognises the string (the case of MikroTik, Cisco, FS, Arista, ProCurve today),
   `Label` is the raw string unchanged and `Key` is the normalised raw string. Two
   prohibitions, which are the reason for this clause: never return an **empty** key
   (all unknowns would group into a single phantom model), and never **merge** two
   unknowns (different hardware would be declared identical). An unknown stays alone.

6. **We only rename where we have proof of two grammars.** A family rule is added on
   evidence — two labels observed for the same hardware — not in anticipation.
   Everywhere else, the raw string is already canonical.

## Consequences

- **One more field on the wire** (`model_key`, `model_label`), and **two derivation
  points not to forget**: `withSyncVLANs` (all device lists and the device page) and
  `publicDevicesOf` (shared dashboard). A third device payload added tomorrow without
  the call will make the duplicate reappear, silently. That is the price, it is
  accepted, and it is written here so that the next person sees it.
- **`model.Device.MarshalJSON` would be the guarantee, and it is forbidden to us**:
  `deviceView` embeds `model.Device`, so the method would be promoted and
  `json.Marshal(deviceView)` would only serialise half the payload. A classic Go trap,
  checked before writing: the derivation stays explicit.
- **The device page now shows the canonical label**; the raw one moves into a tooltip,
  where it already was in the lists. It is no longer readable at a glance — a real loss,
  offset by the fact that it remains intact in the database and on the API.
- **`modelShort` (`web/src/modelLabel.ts`) is not touched and does not become the
  identity.** It handles another problem — fitting a label into 28 characters in a
  tile — and it could not serve: it only kicks in beyond a certain length, and it
  reduces two of the three forms to "Aruba 1930" but the third to "Aruba JL685A". A
  display shortener is not an identity; overloading one with the other would break
  both.
- **Connector parsing is not touched.** `internal/modelid` re-reads the geometry with
  the same two grammars as `instanton/parsePorts`, without reusing its code: the two
  reads have different jobs (faceplate versus label), and the in-tree connector is
  leaving the repository (Phase 4). Two readers, accepted, each tested.
- **No grouping rule is set** for families where the key merges but the labels differ
  (MikroTik `…-2S+` versus `…-2S+RM`, if the case arises): the key serves to say "it is
  the same model", not to elect a name.

## Rejected alternatives

**Canonicalise at write time, plus a migration that converges the existing rows.** A
single string in the database, everything else becomes trivial — and it is the option
naturally proposed. Rejected for two reasons, only one of which would suffice. First, it
**overwrites what the device really declared**, that is to say the only trace that makes
it possible to diagnose a wrong faceplate. Second, a migration that rewrites
`devices.model` on installations in production is precisely what `CLAUDE.md` calls
expensive to reverse: the destroyed information does not come back, and 12 production
devices would have to be rediscovered to recover it. "It's simpler" does not buy that.

**Take the SKU (`JL685A`) as the identity.** The most precise of references, hence the
obvious candidate. Absent from two of the three forms: the identity would be `JL685A`
for 1 device and nothing for 11. We would have replaced three groups with two.

**Store the derived identity in a column, with a backfill.** Avoids forgetting it
somewhere — but freezes today's derivation, and imposes one more migration for every
grammar learnt. An identity is a **point of view on the data**, not data; it belongs to
the read.

**Fix it only on the frontend, in `modelLabel.ts`.** The least code, and the symptom
disappears from the screen. But the API keeps serving three models — including to the
MCP tool surface ([[ADR-0012 — The tool surface is not the API surface]]) and to
the public dashboard — and the next client would reimplement the derivation,
differently.

**A model catalogue, by vendor reference.** The clean answer, and a lot more work: it
has to be kept up to date for seven vendors, and a model absent from the catalogue no
longer has any identity at all. The geometry read from the label gives 90 % of the
result with nothing to maintain, and degrades to the raw string instead of degrading to
nothing.
