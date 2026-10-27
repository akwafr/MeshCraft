---
Status: Accepted
Date: 2026-10-24
Note: Recorded after the fact, like ADR-0001.
---

# ADR-0002 — Glass is chrome, never content

## Context

The request was for a "liquid glass" equivalent, responsive. Badly done
glassmorphism is itself a cliché — blurred purple backgrounds everywhere — and the
product displays **port faceplates** where every colour carries a meaning arbitrated
in a single place (`network.ts`).

## Decision

Glass applies to the **chrome and the controls**: top bar, tab bar, sidebar, drawer,
modal, popover, secondary buttons. **Never** to a card, a table, a faceplate.

Three layers, and the third is the one people forget: a translucent tint, a
`backdrop-filter: blur() saturate()`, and a **specular rim** — `inset 0 1px 0` on the
top edge. Blur alone gives fog, not glass.

The **primary button stays opaque**.

## Consequences

- A port square seen through `blur(24px)` at 82 % is no longer the `--ok` green: it is
  *a* green. Content stays opaque so that the colour keeps meaning what `network.ts`
  said.
- The tint is **higher than expected** (0.82 dark, 0.86 light). At 0.68 a 48-port
  faceplate could be read through the mobile tab bar: that is no longer depth, it is a
  rendering defect. Light needs **more** than dark, against intuition, because the
  saturated green showing through is much noisier on a white tint.
- Three mandatory fallbacks, which matter more than the effect:
  `@supports not (backdrop-filter)` → opaque; `prefers-reduced-transparency` → opaque;
  `forced-colors` → `Canvas`, with no blur or rim.
- Depth goes through elevation, so 1 px hairlines disappear from cards. A page of
  bordered rectangles reads as the *wireframe* of a page — that is most of what makes
  something look "generated".

## Rejected alternatives

**Glass everywhere, cards included.** It is the expected aesthetic, and it breaks the
one thing this product cannot break: the reliability of a status colour.

**Glass nowhere, just shadows.** Safe, and beside the point of the request.

**A marked gradient background so the glass refracts something.** Rejected in favour of
two radial washes at 4-7 %, below the threshold of "there is a gradient" and above that
of "this surface has depth".
