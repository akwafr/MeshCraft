/**
 * THE TRAFFIC RAMP — how much of this link is in use (ADR-0003, ADR-0030).
 *
 * A magnitude scale, not a status one. It answers "how loaded is this?" and
 * it is read AGAINST THE LINK'S OWN LINE RATE, so 300 Mb/s lands mid-ramp on
 * a 1 G port and barely off the floor on a 10 G one. That relativity is the
 * whole point: an absolute colour would paint every access port cold and
 * every uplink hot, which is the thing an operator already knows.
 *
 * # THIS MODULE IMPORTS NOTHING, AND THAT IS LOAD-BEARING
 *
 * The ramp used to live in `network.ts`, which imports `./i18n`. That put it
 * out of reach of `topologyLinks.ts`, whose own import-freedom is load-bearing
 * for the framework-free node suite (`node --experimental-strip-types` can
 * only run a file that pulls in no React and no i18n) — so the topology kept
 * its OWN capacity parser instead, and a second reading of the same number is
 * a divergence waiting to happen (ADR-0030 §3 measures the one it already had:
 * a factor 1000 on an unit-less speed string).
 *
 * Living here, with no imports at all, the ramp is shared by all three of
 * `network.ts` (which re-exports it, so no caller moved), `viz.tsx` (the
 * gradient) and `topologyLinks.ts` (the link halves).
 *
 * # ONE ENGINE, AND THE GRADIENT SAMPLES IT
 *
 * `trafficColorAt(t)` is the single authority: a position on the ramp in, a
 * colour out. `trafficColor` composes it with `trafficLevel`, and
 * `trafficRampStops` — the SVG gradient — SAMPLES it rather than defining a
 * second ramp of its own. ADR-0030 has the measurements; the short version is
 * that an SVG linear gradient interpolates linearly in OFFSET (so, in rate)
 * and in sRGB, while the ramp advances in `t` and mixes in oklab. Six stops
 * therefore agreed only ON the stops and drifted between them.
 */

/**
 * The scale is LOGARITHMIC, and it has to be. Real link utilisation lives in
 * the bottom few percent almost all the time — a linear ramp would leave the
 * entire fleet in the first colour and reserve the other five for an
 * incident. `K` sets how hard the bottom is stretched; at 50 the first
 * percent of a gigabit link is already visibly off the floor, and the top
 * half of the ramp still has the top half of the capacity to describe.
 *
 * Reference points on a 1 G port, which is how K was chosen:
 *      1 Mb/s → 0.01    13 Mb/s → 0.13    69 Mb/s → 0.38
 *    124 Mb/s → 0.50   387 Mb/s → 0.77     1 Gb/s → 1.00
 */
const TRAFFIC_K = 50;

/** 0..1 position on the ramp for `mbps` against a `capacity` in Mb/s. */
export function trafficLevel(mbps: number, capacityMbps: number): number {
  if (!(capacityMbps > 0) || !(mbps > 0)) return 0;
  const ratio = Math.min(1, mbps / capacityMbps);
  return Math.log1p(ratio * TRAFFIC_K) / Math.log1p(TRAFFIC_K);
}

/** The inverse: the rate that sits at `t` on the ramp. Used to place the
 *  gradient stops at the right HEIGHT on a chart whose axis is linear. */
export function trafficRateAt(t: number, capacityMbps: number): number {
  if (!(capacityMbps > 0)) return 0;
  return (capacityMbps * (Math.expm1(t * Math.log1p(TRAFFIC_K)))) / TRAFFIC_K;
}

/** Six stops, cold to hot. Values live in ui/tokens.css so both themes get
 *  their own; this is only the order. */
export const TRAFFIC_STOPS = [
  "var(--traffic-0)",
  "var(--traffic-1)",
  "var(--traffic-2)",
  "var(--traffic-3)",
  "var(--traffic-4)",
  "var(--traffic-5)",
];

/**
 * THE ONE PLACE A POSITION ON THE RAMP BECOMES A COLOUR (ADR-0030).
 *
 * `color-mix` between the two bracketing stops keeps the ramp continuous while
 * leaving the palette in CSS, so it still follows the theme — and it resolves
 * in an SVG presentation attribute (`stroke`, `fill`) and in `stop-color`
 * alike, which is what lets the link halves and the chart gradient share this
 * one function.
 *
 * The mix percentage is ROUNDED to a whole percent, and that rounding is now
 * shared rather than private to one caller: both sides quantise identically,
 * so they cannot disagree by their own arithmetic. It is also the floor on how
 * closely the gradient can track this (ADR-0030 §Consequences).
 */
export function trafficColorAt(t: number): string {
  const clamped = Math.max(0, Math.min(1, t));
  const scaled = clamped * (TRAFFIC_STOPS.length - 1);
  const i = Math.min(TRAFFIC_STOPS.length - 2, Math.floor(scaled));
  const frac = Math.max(0, Math.min(1, scaled - i));
  return `color-mix(in oklab, ${TRAFFIC_STOPS[i + 1]} ${Math.round(frac * 100)}%, ${TRAFFIC_STOPS[i]})`;
}

/**
 * A single colour for a single rate — the label above a sparkline, a cell, one
 * half of a topology link (ADR-0026).
 */
export function trafficColor(mbps: number, capacityMbps: number): string {
  return trafficColorAt(trafficLevel(mbps, capacityMbps));
}

/**
 * How many times each of the five bands is subdivided when the ramp is handed
 * to an SVG gradient.
 *
 * FOUR, and the ceiling is DOM cost rather than colour. SVG interpolates
 * between two stops linearly in offset — which, the stops being placed by
 * rate, means linearly in RATE — and in sRGB; the ramp advances in `t` and
 * mixes in oklab. Across a whole band those two part company by ΔE(oklab)×100
 * = 1.60; across a quarter of one, by 0.17. Going further stops paying: S = 6
 * reaches 0.13 and S = 12 only 0.12, because what is left is no longer the
 * interpolation but the whole-percent rounding in `trafficColorAt`.
 *
 * The price is 21 stops per gradient instead of 6 — on the Ports page, 52
 * sparklines turn 312 nodes into 1092. That is the trade ADR-0030 accepts.
 */
const RAMP_SUBDIVISIONS = 4;

/**
 * The ramp as SVG gradient stops, for a gradient laid out over `capacityMbps`
 * in user space.
 *
 * `offset` is in RATE space — `trafficRateAt(t) / capacity` — because the y
 * axis of a chart is linear in rate while the ramp is logarithmic in it.
 * Spacing the stops evenly would silently linearise the scale.
 *
 * The colours come from `trafficColorAt`, so this is a SAMPLING of the ramp
 * and not a second definition of it. That is the whole point of ADR-0030: a
 * link half and the curve of the same port at the same rate now differ only by
 * how finely this samples, never by which ramp they consulted.
 */
export function trafficRampStops(
  capacityMbps: number,
): { offset: number; color: string }[] {
  const n = (TRAFFIC_STOPS.length - 1) * RAMP_SUBDIVISIONS;
  const out: { offset: number; color: string }[] = [];
  for (let j = 0; j <= n; j += 1) {
    const t = j / n;
    out.push({
      offset: capacityMbps > 0 ? trafficRateAt(t, capacityMbps) / capacityMbps : t,
      color: trafficColorAt(t),
    });
  }
  return out;
}

/**
 * Line rate in Mb/s from a speed label ("1G", "10G", "100M", "2.5G").
 *
 * A BARE NUMBER IS READ AS Mb/s, which is the convention of the `link` field a
 * connector fills. The topology used to parse these itself and guessed the
 * unit from the magnitude instead (`n >= 10 ? "G" : "M"`), so `"1000"` meant
 * 1 Tb/s on a link and 1 Gb/s on a chart — a factor 1000 on the ramp's anchor,
 * which would peg that link half to the bottom of the blue for ever. One
 * parser, and the plausible reading (ADR-0030 §3).
 *
 * Falls back to a gigabit when there is nothing to read: it is by far the most
 * common port on the fleet, and a wrong-but-plausible capacity colours a curve
 * slightly off, while a zero would make every port read as saturated.
 *
 * NOT to be merged with `lowBandwidth.ts`'s same-named parser, which returns
 * `null` for an unreadable label on purpose — there, an unknown speed must not
 * be reported as LOW, because that tile sends somebody to a rack. Two uses, two
 * contracts.
 */
export function speedMbps(speed: string | undefined | null): number {
  if (!speed) return 1000;
  const m = /^(\d+(?:\.\d+)?)\s*([MGT])?/i.exec(speed.trim());
  if (!m) return 1000;
  const n = parseFloat(m[1]);
  if (!Number.isFinite(n) || n <= 0) return 1000;
  const unit = (m[2] || "M").toUpperCase();
  if (unit === "T") return n * 1000000;
  if (unit === "G") return n * 1000;
  return n;
}
