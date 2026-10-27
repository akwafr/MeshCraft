---
Status: Accepted
Date: 2026-10-27
---

# ADR-73885 — The page background takes the mode's tint

## Context

The application's background is blue. It is not the accent: it is the neutral
scale — `--bg`, `--bg-panel`, `--bg-hover`, `--border` — all at OKLCH hue ≈ 270°,
and the two light washes of `body::before` (`--wash-a` indigo, `--wash-b` teal).
This blue does not change with the mode.

The routing mode's red has existed since
[[ADR-0013 — Switching and routing are two navigation modes, not two products]]
§4: a dedicated token, `--mode-routing`, crimson, measured at ΔE 31 from
`--danger`. Today it only colours the brand, in the switcher. The ADRs do not
forbid it elsewhere, but the comment in `tokens.css` calls it "CONTAINED": status
red paints dots and text, the mode only paints the logo, so the two are not
confused. That reasoning was never a written decision.

The observation from use: one goes from switching to routing, the tab bar changes,
but the rest of the screen is identical. Only a 26 px logo changes colour. The
request: a background that turns red, in the same shades, at the same time as the
logo slides.

## Decision

**In routing mode, the background neutral scale and the light washes take the hue
of `--mode-routing`, at identical OKLCH lightness and chroma, and the switch is
animated over `--dur-fast`, the token of the switcher's slide.**

1. **What changes**: `--bg`, `--bg-panel`, `--bg-hover`, `--border`,
   `--glass-bg` (the bars' glass, blue too), `--wash-a`, `--wash-b`. In both
   themes.
2. **What does not change**: text, the blue accent (interaction, pending write),
   the whole status palette. `--danger` remains the only saturated red on screen.
3. **"Same shades"** means: L and C are kept, the hue H is replaced by that of
   `--mode-routing` (1.6°). The neutrals have 0.016 to 0.032 chroma: they are warm
   greys, not reds. Only the washes carry real colour, and they cap at 10% opacity.
4. **The mode is set on `<html data-mode>`** by `Layout`, as the theme is by
   `theme.ts`. The tokens are redefined under `:root[data-mode="routing"]`.
5. **The animation** goes through a single number registered with `@property`,
   `--mode-t`, which `:root` transitions from 0 to 1. Each token comes in two halves
   (`--bg-switching`, `--bg-routing`) and is worth their `color-mix` at ratio
   `--mode-t`. Every consumer of `var(--bg-panel)` follows without a transition of
   its own. The duration token is **the same** as that of the logo's slide: if one
   slows down, so does the other.

## Consequences

- Confining crimson to the brand, written in `tokens.css`, is abandoned. The token
  remains distinct from `--danger` (ADR-0013 §4 holds), but the argument "they never
  appear as the same kind of object" no longer holds for the background. An error
  banner now sits on a pinkish background in routing. It stays readable because the
  background is at 0.02 chroma and the banner at full saturation, but the distance
  is shorter than before.
- **Colour still does not carry the mode on its own** (ADR-0013 §5, ADR-0019 §1).
  The tab bar remains the main reading; the background is a reinforcement,
  invisible to whoever cannot distinguish the hue.
- Every new surface must consume the tokens and never a literal value, otherwise it
  stays blue in routing. The inline literal colours that already exist (760
  `style={{…}}` at the ratchet) surely contain some: they will stay blue until they
  are cleaned up.
- Animating `--mode-t` on `:root` recomputes the style of the whole tree on every
  frame for 120 ms. Measurable on an old phone, rare at the scale of a session.
- A browser without `@property` switches without animation; a browser without
  `light-dark()` keeps the dark fallback values, also declined in red.
- `prefers-reduced-motion` sets `--dur-fast` to 0: the switch is instantaneous.

## Alternatives ruled out

- **Registering the seven tokens as `<color>` and transitioning them directly.**
  Tried first: Chrome 124 keeps an unresolved `light-dark()` value in the computed
  value of a registered `<color>` property, and two unresolved values do not
  interpolate. The switch was instantaneous. A `<number>` always interpolates.

- **Cross-fading two pseudo-elements** (a red `body::after` whose opacity rises).
  Simple and cheap, but it only animates the washes: the panels, the borders and
  the background itself are tokens, and they would switch at once while the washes
  fade.
- **Tinting only the washes.** The observation is about "the background theme", and
  the neutral scale is what gives the background its colour. Red washes over blue
  panels give a mismatched background, not a switch.
- **Using `--danger`.** Ruled out by ADR-0013 §4 for the reasons it gives, which
  hold even more for a whole surface.
- **A duration of its own for the background, longer.** 120 ms is short for a
  background change, and a 320 ms fade would be softer. But the request is for the
  background to underline the switcher's gesture, and two different durations
  would make two events instead of one. If the background feels abrupt, both are
  slowed down together.
- **A saturated red background**, in the everyday sense of "turning red". That is
  what would make the mode indistinguishable from an alert, and it is also
  unreadable: the neutrals carry text.
