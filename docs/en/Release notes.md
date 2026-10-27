---
tags: [meshcraft, delivery]
---

# Release notes

The **product version** (`VERSION` file at the root) names releases and
documentation. It plays **no** part in a proxy's update decision, which is made on
the build timestamp — see [[Proxy versioning and updates]].

## 0.2.0 — 2026-10-25

The cycle that opens meshcraft to **L3**, gives it an **API usable by third
parties**, and turns a series of silences into information. Eighteen decisions come
with it ([[Decision index]], ADR-0011 to 0028).

### What is new

- **Per-user API tokens** and an **MCP server** — same identity, same rights, no
  second permission model (ADR-0011, ADR-0012).
- **Two navigation modes**, switching and routing, with the Routing, Firewall, DNS
  and DHCP screens (ADR-0013, ADR-0019).
- **Site extensions** and a first **UniFi** incarnation: inventory of access points
  and SSIDs, then the adopt / forget / reprovision gestures (ADR-0015, ADR-0016,
  ADR-0025).
- **Alerting** with **Telegram** and **Slack** channels — a subscriber of the log,
  not a second event model (ADR-0024).
- Renamable **dashboard tiles**, and a per-port traffic tile (ADR-0027).

### What the product had stopped saying

This is the least visible part of this release, and probably the most useful:
several screens asserted false things **without flagging it**.

- A **storm limiter** expressed in packets per second was not read, and the rule
  "silence means no limiter" then concluded *"port unprotected"* on a protected port
  (ADR-0020).
- The **management SVI** was the last block read from the running-config, not the
  one the session uses. On Cisco IOS, the same defect **directed a write** to a
  transit link. Fixed on EOS, IOS and fs-s3900.
- A **short read** silently became the intent: 48 ports out of 52 imported, with an
  `in_sync` on top, because the intent and the observation came out of the same
  truncated read (ADR-0017).
- The **name of an MSTP region** was displayed nowhere — enough for a site rename to
  split a fabric into two regions, without symptoms, for weeks (ADR-0021, ADR-0022,
  ADR-0023).
- The **SFP+ cages** of an FS S3400 disappeared from the model because of a
  numbering convention, taking the two active uplinks with them (ADR-0018).
- Three **vendor labels** of the same switch produced three distinct models in the
  inventory (ADR-0014).

### The rule that keeps coming back

A platform **declares** what it can do, and what it cannot do **is said on screen**
instead of being missing. Joining an MST region (ADR-0022), breaking traffic down by
class (ADR-0028): in both cases, an empty series does not distinguish "collection is
starting" from "this platform will never be able to", and it is the declared
capability that decides.

Its corollary: **an invariant held by a type is held; held by a comment, it has
already failed**. `absorbObserved` carried its safety rule in capitals in its
header, and the first new caller ignored it.

### Clocks

The time reference is that of **the viewer** (ADR-0008, previous cycle), but two
subtractions still mixed references — including the one that armed a deletion. NTP
was also set up on the controller host, which was six minutes slow with no
synchronisation.

### Known and unresolved

- Ports collected over **gNMI emit no port event**: transitions leave without
  `IfIndex` and are rejected downstream. No port alert on an Arista until this is
  fixed.
- `fs-s3400` keeps two management read paths with opposite tie-breaks; when they
  diverge, **the mask is silently lost**.
- `fs-s3900` has **no shutdown check** on its SVIs: a shut-down SVI can claim
  management. Closing this hole requires a hardware capture, not a guessed regular
  expression.
- **Unknown unicast** cannot be collected anywhere: the standard MIB does not expose
  it per port, and the only figure in the fleet counts *dropped* frames. Two true
  classes rather than three of which one is invented.
- The `events` table has **no retention**, and it is now on a hot path.
