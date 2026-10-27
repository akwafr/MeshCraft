---
Status: Accepted
Date: 2026-10-24
Note: Recorded after the fact, like ADR-0001.
---

# ADR-0003 — The traffic ramp is logarithmic and relative to capacity

## Context

The "Traffic" column of the Ports page displayed `12.4 Mb/s / 3.1 Mb/s`: two
numbers, no history, and **no way to tell a loaded gigabit
from an idle ten-gigabit**.

## Decision

A six-stop **magnitude ramp**, from blue to lime, read against the
**port's line speed**, on a **logarithmic** scale:

```
t = log1p(throughput / capacity × 50) / log1p(50)
```

It colours a **curve** and nothing else. Never a badge, a border,
a status text.

Calibrated on real lines of a gigabit fleet: 13 Mb/s → 0.13 ·
124 Mb/s → 0.50 · 387 Mb/s → 0.77.

## Consequences

- The ramp **stops at lime**, not at red. Red and orange
  belong to `--danger` and `--warn`, and a saturated uplink is not a
  failure: it is a link doing its job.
- It is a **third non-status palette**, after VLANs and rule
  tints. What separates it from the backbone is **where** it appears:
  status inks colour badges, borders, text; this one
  only colours a trace.
- The gradient is anchored in **user space on capacity**, not on the
  drawn range: a curve that tops out at 5% stays blue instead of being
  stretched to lime because 5% was its own maximum. Two ports side
  by side remain comparable.
- The curve's **height** remains relative to its own peak. Height = shape,
  colour = load. A capacity-linked floor was tried then removed: it
  crushed every access port against the bottom of its cell.
- On the dashboard there is no line speed — the axis ceiling
  stands in for it. "Hot" there means "the top of what this site did over
  this window", which is the only honest claim available.

## Rejected alternatives

**A linear scale.** Real utilisation lives in the first few percent
almost all the time: the whole fleet would be in the first colour and the
other five reserved for an incident.

**An absolute colour in Mb/s.** Would paint every access port cold and
every uplink hot — which the operator already knows.

**Normalising per graph.** Each curve would reach lime at its own
peak, and two ports would no longer be comparable.
