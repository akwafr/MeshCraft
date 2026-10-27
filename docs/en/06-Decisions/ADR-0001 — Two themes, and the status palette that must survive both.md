---
Status: Accepted
Date: 2026-10-24
Note: Recorded after the fact — the decision was taken during the UI overhaul of
  2026-10-24, before the ADR convention existed. The following ones are written
  before the work.
---

# ADR-0001 — Two themes, and the status palette that must survive both

## Context

meshcraft had been **dark only** since its origin: thirteen properties declared
once on `:root`, no light redefinition anywhere, and the UI guide said in so many
words "do not condition anything on the colour scheme".

The palette carries a **product** decision, not an aesthetic one, documented in §2
of the guide: orange `--warn` says "we can see the device and something is wrong";
yellow `--indeterminate` says "meshcraft has lost its point of view, and the device
may be perfectly fine". Confusing them sends someone to the wrong rack.

Yet `--indeterminate` is `#e8d44d`. On a white background, it is **invisible**.

## Decision

Two themes, dark remaining the **default**. Each colour is declared twice: a
fallback value, then the real one in `light-dark()`.

```css
--bg: #080a11;                        /* fallback */
--bg: light-dark(#eef1f6, #080a11);   /* the real one */
```

The light values of the status inks are **not** a mechanical darkening: `--warn`
moves towards red (28°), `--indeterminate` towards olive (52°). The gap goes to 24°
where the dark theme only holds 13.

## Consequences

- A browser that ignores `light-dark()` drops the second declaration and keeps the
  first: it lands on the **dark theme**, the one the product has always served. The
  degraded mode of an old tablet in a wiring closet is "it looks like last year",
  not "it has no colours any more".
- What travels between themes is the **perceptual distance**, not the
  coordinates. Whoever retouches one of these two inks must re-measure the gap, not
  the hue.
- Any colour hard-coded in a `.tsx` `style={{}}` is now a potential defect: they
  were all written against a dark background.
- Dark remains the default **against** modern practice (following the system).
  Switching existing installations to light on the first light OS would have been a
  visible change nobody asked for. "Auto" is one gesture away.

## Alternatives ruled out

**Staying dark only.** Tenable, and it is what the guide prescribed. Ruled out on
explicit request.

**Mechanically darkening the inks for light.** That is the trap: it collapses orange
and yellow into a single brown and silently removes the distinction that the whole
of §2 exists to protect.

**Duplicating the palette in two `@media` / `[data-theme]` blocks.** Two
declarations per colour instead of one, and above all two places to maintain — the
guarantee that they diverge. `light-dark()` holds both in one line.
