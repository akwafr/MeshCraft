---
Status: Accepted
Date: 2026-10-24
Note: Recorded after the fact, like ADR-0001.
---

# ADR-0004 — Junos provisioning without `commit confirmed`

## Context

Serial provisioning of Juniper EX pushes an initial configuration then
commits it. Junos offers `commit confirmed`: the configuration is applied and
**rolls back automatically** if it is not confirmed within the delay. Juniper
presents it precisely for this case, and it is the usual recommendation.

## Decision

Use a **plain `commit`, preceded by a `commit check`**. No
`commit confirmed`.

## Consequences

- The usual safeguard is lost: a configuration that cuts access stays
  in place. Acceptable here, and only here, because the pass runs on the
  **console** — the change cannot cut the session carrying it.
- In exchange, we avoid a failure mode that `commit confirmed` **would add**:
  a pass that dies after applying and before confirming (tab
  closed, adapter unplugged, cancellation) leaves a provisioned switch,
  verified green, which **returns to factory five minutes later** while
  every meshcraft record says it is adopted.
- `commit check` carries the verification: a failure triggers `rollback 0` and a
  new attempt in the other dialect, without the device having moved.
- To be revisited if Junos provisioning ever goes **over the network** rather than
  through the console. The argument then falls entirely.

## Rejected alternatives

**`commit confirmed 5`.** Juniper's recommendation, rejected for the reason
above: on a console, it trades a non-existent risk for a real
one.

**`commit` alone, without `commit check`.** Lets the device refuse a
half-entered transaction and forces reading the refusal on the fly.
