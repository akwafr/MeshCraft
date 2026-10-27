---
Status: Accepted
Date: 2026-10-25
---

# ADR-0030 — The traffic ramp has one colour engine, and the SVG gradient samples it

## Context

ADR-0003 fixes the ramp (logarithmic, `K = 50`, six stops, read against the line
speed) and ADR-0026 extended it to the two halves of a topology link. The rule is
therefore settled. Yet the request "the colour of links must follow the colour logic
of the graphs" came back **after** ADR-0026 was shipped: on screen, the two did not
quite overlap.

The three measurements made before writing this, on the instance and on the ARENA
site:

1. **The primitives were already shared.** `trafficLevel`, `trafficRateAt` and
   `TRAFFIC_STOPS` lived in `network.ts` and served both sides. **No** private ramp
   remained: `loadColor` did die with ADR-0026. So it was not "two ramps".

2. **The INPUTS agree.** The link half reads `tx_mbps` from the inventory
   (`portrates.go`: smoothed over two minutes, refreshed every 30 s), the graph reads
   the telemetry series (60 s step, 24 h window). Surveyed on the 15 loaded ports of
   `core01-arena`: 43.0 versus 49.3 — 57.2 versus 57.7 — 146.8 versus 147.5 Mb/s. That
   is **at worst a 3 % position gap on the ramp**. That is not where it plays out.

3. **It was the RENDERING that diverged, and there were two of them.**
   - `trafficColor` returns a single colour: `color-mix(in oklab, …)` between the two
     surrounding stops, interpolated linearly in **`t`** — the ramp's parameter;
   - `TrafficGradient` places the **six** stops at offset `trafficRateAt(i/5)/
     capacity`, and SVG interpolates between two stops linearly in **offset**, that
     is, linearly in **rate**, and in **sRGB**.

   The two coincide exactly *on* the six stops and drift between them, because the
   ramp is convex where the gradient is straight. Measured on a 1 G port, by sweeping
   from 0 to 1 Gb/s: maximum gap **ΔE(oklab)×100 = 1.60** in dark theme, **1.40** in
   light, at worst around 10 Mb/s.

That 1.6 is at the limit of perceptible, and on its own it does not explain the
initial impression: on ARENA, `core01-arena` is a 10 G, so 147 Mb/s is 1.5 % of the
line and **all twelve link halves of the site fit in the first band**
(`--traffic-0` → `--traffic-1`, from 0 % to 72 %). A line pushing 147 Mb/s and a line
pushing 0.3 are two blues. It is the behaviour ADR-0003 wanted — "real utilisation
lives in the first few percent" — and not a defect to fix here.

The fact remains that a product computing the same colour **twice** through two
different paths will end up making them diverge for real. Today's 1.6 is the symptom,
not the disease.

## Decision

**There is a single traffic colour engine, and it is `trafficColorAt(t)`.** Everything
else depends on it:

1. **`trafficColorAt(t)` is the authority.** One position `t` on the ramp, one colour.
   `trafficColor(mbps, capacity)` is now merely a composition:
   `trafficColorAt(trafficLevel(mbps, capacity))`.

2. **The SVG gradient is no longer a second definition, it is a sampling of the
   first.** `trafficRampStops(capacity)` returns the stops by calling
   `trafficColorAt`, and **subdivides each band into 4**: 21 stops instead of 6. SVG's
   sRGB-linear-in-rate interpolation now only works over a quarter of a band, where
   the ramp is nearly straight. Maximum gap brought down to **ΔE 0.17** (dark) and
   **0.15** (light), i.e. **ten times less**.

3. **A single capacity reading for the ramp.** `linkCapacityMbps` in
   `topologyLinks.ts` is no longer a parser: it delegates to `speedMbps`. The two
   agreed on the whole real fleet (`""`, `1G`, `10G`, `10M`, `100M`), but not in
   theory — on a unitless string, `linkCapacityMbps` guessed the unit from the
   magnitude (`n >= 10 ? "g" : "m"`), so `"1000"` was worth **1 Tb/s** for a link and
   1 Gb/s for a graph. A factor of 1000 on the ramp's anchor would have pinned the link
   half to the bottom of the blue for ever. `speedMbps` now reads a bare number as
   Mb/s, which is the convention of `link` fields.

4. **The ramp moves into `trafficRamp.ts`, which imports nothing.** That is the
   condition for `topologyLinks.ts` — whose absence of imports is load-bearing, since
   the `node --experimental-strip-types` suite can load neither React nor i18n — to
   share the parser rather than keep a copy of it. `network.ts` re-exports, so no
   caller moves.

**What legitimately remains different is the INPUT, not the function.** A link half
colours an instant (the inventory smoothed over 2 min), a curve colours a series
(60 s buckets). Both go through `trafficColorAt`, and that is the only remaining gap —
measured at 3 %, and accepted: erasing it would take one telemetry query per device on
the site plan, which hovering already pays for one device at a time.

## Consequences

- **The price is in DOM nodes: 21 `<stop>` per gradient instead of 6.** On the Ports
  page, 52 curve thumbnails take 312 nodes to 1,092. It is the accepted cost of
  unification; the subdivision is capped at 4 for that reason and not for a colour
  reason — beyond it, the gain stalls (ΔE 0.13 at S=6) because the rest comes from
  `color-mix`'s rounding to the whole percent.
- **This rounding is now shared.** `Math.round(frac * 100)` was in `trafficColor`; it
  is in `trafficColorAt`, so both sides quantise identically instead of each rounding
  on its own.
- **ADR-0003 and ADR-0026 are not re-judged.** The scale, `K = 50`, the six stops, the
  lemon stop, reading against the negotiated speed, the two halves, the grey `?` of a
  half without measurement, arrowheads reserved for links that carry traffic: it all
  holds. This ADR only talks about the path by which a position on the ramp becomes a
  colour.
- **`lowBandwidth.ts` keeps ITS `speedMbps`, and that is not an oversight.** It returns
  `null` on an unreadable string where the ramp's falls back to 1 G, and its comment
  says why: "an unknown speed must not be flagged as low", because the tile sends
  someone to a rack. Merging the two would trade a false cold on a curve for a false
  site visit. Two uses, two contracts, and the difference is written on both sides.
- **The gradient stays anchored in user space on capacity.** The subdivision changes
  the stop density, not the anchor: a curve peaking at 5 % stays blue, and two ports
  side by side remain comparable.

## Rejected alternatives

**Do nothing: 1.6 ΔE is invisible.** True today, and that is exactly why this ADR does
not claim to fix a visible bug. But the product contained two definitions of the same
colour, only one of which was cited by the ADRs; the next tweak to one of them would
have separated them for good, and nobody would have known which was the rule.

**Make `trafficColor` interpolate linearly in rate, so that it matches the gradient.**
Aligns the two at the price of the ramp itself: the position on the ramp is `t`, and a
colour that advances linearly in rate between two stops re-linearises piecewise the
scale ADR-0003 made logarithmic on purpose. It is aligning the rule on its
approximation.

**Keep six stops and accept the drift.** It is the previous state, and it is the
alternative a hurried reader will propose on seeing 21 stops in the DOM. It leaves the
gap where it is and, above all, it leaves the gradient **defining** a colour instead of
sampling it — hence two authorities.

**Subdivide much more (S = 12, 61 stops).** The gain stops (ΔE 0.12 versus 0.17 at
S=4) because the floor is no longer the interpolation but the rounding to the percent.
Three times the nodes for 0.05 ΔE.

**Render the colour in JS and paint the curve in segments.** Would remove the gradient
and hence any approximation. But a smoothed curve then becomes N paths instead of one,
`smoothSegments` loses its continuous tangents at the joins, and we trade 21 `<stop>`
for dozens of `<path>`.

**Move the ramp into `network.ts` and have `topologyLinks.ts` import `network.ts`.**
The shortest diff, and it breaks the test suite: `network.ts` imports `./i18n`, which
`node --experimental-strip-types` does not load. That is precisely the reason why
`topologyLinks.ts` imported nothing.
