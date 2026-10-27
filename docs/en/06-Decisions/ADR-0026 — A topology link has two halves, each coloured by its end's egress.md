---
Status: Accepted
Date: 2026-10-25
---

# ADR-0026 — A topology link has two halves, each coloured by its end's egress

## Context

On the site plan, a link was **a uniform 2.2 px stroke**, coloured by
`loadColor` — a green→red ramp, specific to that file, read against
`max(toA, toB)` divided by the higher capacity of the two ends.

Three facts followed:

- **asymmetry was invisible.** An uplink pouring 4 Gb/s towards the core and
  receiving 30 Mb/s was painted exactly like its inverse. Yet direction is the
  first question one asks of a loaded link: "who is pushing?";
- **the link's ramp was not the product's.** `loadColor` went up to
  red, which ADR-0003 explicitly forbids ("the ramp stops at lime
  […] a saturated uplink is not a failure"). A blue Traffic column and a
  red link described the same load;
- **the absence of measurement was painted as rest.** `linkRatesBetween`
  returns `{toA: 0, toB: 0}` when neither end carries a counter, and
  `internal/api/portrates.go` already writes `0` rather than `null` into the
  inventory, saying so in its comment: "Zero is not "no
  measurement" — inventory cannot express ignorance on this field". A link
  about which nothing was known was therefore an idle link on screen.

The request was to see the egress on each side, in colour, with the values.

## Decision

A link in service is **two halves**. The half adjacent to switch X
is coloured by **X's egress on this link** — its port's `tx` — read on the
**ADR-0003 ramp against that port's negotiated speed**, and ends with
a **point whose apex is exactly the midpoint of the segment**.

Four points, in the order in which they are contested:

1. **The metric is the near port's `tx`, never a sum or an average.**
   Adding or averaging the two directions hides the asymmetry, which is the only
   thing this rendering adds. Failing the near `tx`, the far port's
   `rx`: it is the same quantity, give or take losses.
2. **Each half reads against ITS port.** Two ends may have negotiated
   different speeds (a 10G cage on a 1G module, a damaged pair that fell back
   to 100M); if so, the two halves of the same link do not have the same
   colour for the same throughput, and that is information, not a defect.
3. **A half without a measurement is GREY (`--text-dim`) and carries a `?`.** Never
   the blue of the bottom of the ramp, which would mean "at rest" on a link about which
   nothing is known.
4. **Points only exist on a link that is forwarding.** A link that is down or
   blocked by spanning-tree remains a uniform stroke without points.

## Consequences

- **The ramp now colours a link, not just a curve.** ADR-0003
  said "it only colours a trace", in contrast to status badges, borders
  and texts. A plan link **is** a trace, so the boundary
  holds — but it is now stretched, and the next person who wants to colour a
  badge with `trafficColor` will cite this precedent. Throughput labels
  keep their neutral ink for this exact reason.
- **`--traffic-0` (#2b5bd7) and `--accent` (#4f7cff) are two neighbouring blues**, and
  the second is the ink of STP blocking. An idle link and a blocked link
  could be confused by hue alone. It is the **presence of the points** that
  separates them: a forwarding link always has two apexes touching at
  its middle, a blocked link never does. We therefore accept that a reader who
  does not know this rule will hesitate once.
- **The yellow `--indeterminate` is not used for the unknown on this
  trace**, contrary to the general rule of 2026-08-08 (UI-GUIDE §2). It
  is 14° away from `--traffic-5` (#c6e018), the top of the ramp: a blind
  half would read as a saturated half, the exact misreading this rendering
  exists to prevent. The yellow is not lost — the **node's status
  badge** at that end already carries it (`deviceDot`, `blindNow`), and it is its job
  to say "we have lost our view of this switch". The link half
  only has to say "this number is not a reading", which grey and `?`
  say unambiguously. The price paid: on this trace alone, "never observed" and
  "no longer observable" are painted the same.
- **Throughput labels change direction.** The badge near A
  showed what arrived TOWARDS A; it now shows what LEAVES A,
  so that the number and the colour beneath it speak of the same quantity. An
  operator used to the old reading will read the reverse for a while. The
  glyph's arrow (`▸`/`◂`) points in the half's direction, and it is the only
  safeguard.
- **The points occupy the middle**, where the speed label used to live. Its
  offset goes from 7 to 15 px and that of the STP label from 14 to 16 px: on a
  short link, the badges therefore move a little further from the stroke than before, and
  drop one notch earlier in the abandonment order of `topologyLinkLabels.ts`.
- **Below 20 px of length, the points disappear.** They shrink
  first — capped at 45% of their half, which makes non-overflow
  structural rather than tuned — then vanish when there is no longer room
  to draw a readable one. Two devices placed right next to each other therefore have
  a two-coloured link without arrows.

## Rejected alternatives

**A single stroke, coloured by the maximum of the two directions.** That is the previous state.
Answers "is this link loaded" and never "in which direction", and it is the
second that leads to an action.

**A stroke, coloured by the sum of the two directions.** More accurate for "cable
load", but asymmetry disappears a second time, and a link at 500/500 would
read like a link at 1000/0 on a gigabit — two situations that do not call
for the same thing.

**Keeping the green→red `loadColor` and only cutting the link in two.** The
least work, and it leaves two contradictory ramps in the product for
the same quantity. Red on an uplink doing its job is precisely what
ADR-0003 refuses.

**Making the unknown a dashed stroke.** Dashes are already taken twice
on this trace: link out of service, and LLDP neighbour seen from one side only. A
third meaning on the same channel is no longer a meaning.

**Painting the unknown in `--indeterminate` as everywhere else.** Refused for
the hue collision with the top of the ramp, detailed under consequences. It is
the only departure of this ADR from a general product rule, and it is local
to this trace.

**Fixed-size points.** Two 11 px points on a 15 px link
overlap and each overshoots into the other — the exact "overflow" the
request names. The size is therefore relative to the half, and the apex is
built *on* the midpoint rather than computed to reach it.

**Showing both values only on hover.** Topology is the screen where
one looks for which link is pushing; having to hover fourteen links to
find it cancels the gain. Both values are therefore permanent, and
`topologyLinkLabels.ts` already removes them when the link becomes too short to
carry them. Hover keeps what it does best: the 24 h.
