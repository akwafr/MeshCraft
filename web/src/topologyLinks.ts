/**
 * Topology link strokes: one palette for declared and LLDP links.
 *
 * Offline → black with red glow. Spanning-tree blocking / discarding → accent
 * blue. Online forwarding → TWO HALVES, each on the traffic ramp of its own
 * end (ADR-0026) and ending in a point that meets the other half's at the
 * exact midpoint.
 *
 * Capacity always comes from the device (negotiated link, else port facade
 * speed from the driver/catalog) — never from a hand-entered link.speed.
 *
 * # THIS MODULE IMPORTS ONLY IMPORT-FREE MODULES, AND THAT IS LOAD-BEARING
 *
 * Same reason as `topologyLinkLabels.ts` and `lowBandwidth.ts`: the judgement
 * here is exactly the kind that should be asserted rather than read, and the
 * framework-free node suite (`node --experimental-strip-types`) can only run a
 * file that pulls in no React and no i18n.
 *
 * It long imported NOTHING AT ALL, because the ADR-0003 ramp lived in
 * `network.ts`, which imports `./i18n`. The cost of that was a second capacity
 * parser kept right here — and ADR-0030 measured where the two readings of one
 * number had already parted company. The ramp now lives in `./trafficRamp`,
 * which imports nothing either, so the parser can be shared without putting
 * i18n in the suite's path. Anything added to this file's imports must keep
 * that property.
 *
 * The ink itself still arrives from the caller: Plan.tsx resolves each half
 * with `trafficColor` and hands it to [topologyLinkPaint], which owns
 * everything else (which ink wins, how wide, whether there are points at all).
 */
// Explicit `.ts`, like every other module the framework-free suite loads:
// node's ESM resolver does not guess the extension (tsconfig allows it, and
// vite/esbuild strip it).
import { speedMbps } from "./trafficRamp.ts";

export type LinkStroke = {
  stroke: string;
  strokeWidth: number;
  strokeDasharray?: string;
  opacity?: number;
  filter?: string;
};

/**
 * Ink of a link half whose egress is NOT a reading (ADR-0026).
 *
 * Grey, and deliberately not the `--indeterminate` yellow the rest of the
 * product uses for "we cannot see" (UI-GUIDE §2). That yellow (#e8d44d) is
 * 14° from `--traffic-5` (#c6e018), the TOP of the ramp this very stroke is
 * painted with — a half nobody can measure would read as a saturated one,
 * which is the single misreading this rendering exists to prevent. The yellow
 * is not lost: the node's own status dot at that end still carries it
 * (`deviceDot` / `blindNow`), and saying "we lost the vantage point on this
 * switch" is that dot's job. The half only has to say "this is not a reading",
 * which grey and the `?` on its pill say without competing with the ramp.
 */
export const LINK_UNKNOWN_INK = "var(--text-dim)";

/* ----------------------------------------------------------------------
 * THE TWO MEETING POINTS
 * ----------------------------------------------------------------------
 *
 * Each half is a shaft (a stroked line) plus a triangle whose APEX IS THE
 * MIDPOINT ITSELF. "No overflow" is therefore structural and not a tuning: the
 * apex is the literal midpoint coordinate and every other vertex sits BACK
 * along the half's own direction, so nothing can cross into the other half
 * whatever the geometry does.
 */

/** Full length of a point, in plan units, when the half has room for it. */
export const LINK_HEAD_LEN = 11;

/** Full half-width of a point at its base. */
export const LINK_HEAD_HALF_W = 5.4;

/**
 * The most of its own half a point may ever take.
 *
 * This is what makes a SHORT link behave. Two fixed 11 px points on a 15 px
 * link would each run past the midpoint and into the other — the exact
 * "arrowhead that never overshoots" the request names. Capped at 45 %, a point always
 * leaves the majority of its half as shaft, at every length.
 */
const HEAD_MAX_SHARE = 0.45;

/**
 * Below this, a point is dropped rather than drawn tiny.
 *
 * 4.5 px of length means the half is under 10 px, i.e. the whole link is under
 * 20 px — two boxes almost touching. At that size a triangle is three pixels
 * of noise and the two-colour split is still perfectly readable without it, so
 * the link degrades to a plain two-tone bar.
 */
const HEAD_MIN_LEN = 4.5;

export type LinkHalfShape = {
  /** The shaft: from the device end to the base of the point (or to the
   *  midpoint when there is no point, so the two halves still meet). */
  x1: number;
  y1: number;
  x2: number;
  y2: number;
  /** `points` for a <polygon>, apex on the midpoint — null when dropped. */
  head: string | null;
};

function r2(v: number): number {
  return Math.round(v * 100) / 100;
}

/**
 * One half of a link: the device end `(ex, ey)` reaching the midpoint
 * `(mx, my)`, where its point meets the other half's.
 *
 * `pointed` is false for a link that carries nothing — down, or discarding —
 * and then the shaft simply runs to the midpoint: the two halves butt-join and
 * the link reads as the single uniform stroke it was before ADR-0026.
 */
export function linkHalfShape(
  ex: number,
  ey: number,
  mx: number,
  my: number,
  width: number,
  pointed: boolean,
): LinkHalfShape {
  const flat: LinkHalfShape = { x1: ex, y1: ey, x2: mx, y2: my, head: null };
  if (!pointed) return flat;
  const dx = mx - ex;
  const dy = my - ey;
  const len = Math.hypot(dx, dy);
  if (!(len > 0)) return flat;
  const hl = Math.min(LINK_HEAD_LEN, len * HEAD_MAX_SHARE);
  if (hl < HEAD_MIN_LEN) return flat;
  const ux = dx / len;
  const uy = dy / len;
  // Normal to the half, for the base corners.
  const nx = -uy;
  const ny = ux;
  // Never narrower than the shaft it grows out of, or it stops reading as a
  // point and becomes a bulge.
  const hw = Math.max(width / 2 + 1, LINK_HEAD_HALF_W * (hl / LINK_HEAD_LEN));
  const bx = mx - ux * hl;
  const by = my - uy * hl;
  return {
    x1: ex,
    y1: ey,
    x2: r2(bx),
    y2: r2(by),
    head:
      `${r2(bx + nx * hw)},${r2(by + ny * hw)} ` +
      `${r2(mx)},${r2(my)} ` +
      `${r2(bx - nx * hw)},${r2(by - ny * hw)}`,
  };
}

/**
 * Capacity in Mbps from driver observation.
 * Prefer negotiated `port.link`; fall back to the port group's facade speed.
 *
 * WHICH END'S CAPACITY this is asked for is ADR-0026 §2; this only reads a
 * label. It no longer parses one: `speedMbps` does, and it is the same parser
 * the chart ramp reads against (ADR-0030 §3).
 *
 * This function used to do its own parsing, and guessed a missing unit from
 * the magnitude — `n >= 10 ? "G" : "M"`. On the fleet as it stands the two
 * agreed on every label present (`""`, `1G`, `10G`, `10M`, `100M`), so nothing
 * on screen was wrong; in theory a bare `"1000"` was a TERABIT here and a
 * gigabit on the chart of the same port, and a factor 1000 on the ramp's
 * anchor pins a link half to the bottom of the blue permanently. Two readings
 * of one number is the divergence, whether or not today's data exercises it.
 */
export function linkCapacityMbps(
  observedLink: string | undefined | null,
  facadeSpeed: string | undefined,
): number {
  return speedMbps(observedLink || facadeSpeed);
}

/** Label for mid-link speed: negotiated first, else facade. */
export function linkSpeedLabel(
  observedLink: string | undefined | null,
  facadeSpeed: string | undefined,
): string {
  const obs = (observedLink || "").trim();
  if (obs) return obs;
  return (facadeSpeed || "").trim();
}

/**
 * Everything needed to paint a link: the shaft attributes both halves share,
 * one ink per half, and whether the two points are drawn.
 */
export type TopologyLinkPaint = {
  strokeWidth: number;
  strokeDasharray?: string;
  opacity?: number;
  filter?: string;
  /** Ink of the half next to end A / end B. Equal on a link that is down or
   *  discarding, which is what makes those read as one uniform stroke. */
  inkA: string;
  inkB: string;
  /**
   * Whether the halves end in points.
   *
   * ONLY a forwarding link has them, and that is the one cue separating a link
   * at rest from a link the spanning tree has blocked: `--traffic-0` (#2b5bd7)
   * and `--accent` (#4f7cff) are two neighbouring blues, so hue alone cannot
   * do it. Points also happen to be the honest mark — a discarding port emits
   * nothing by design, so there is no egress to point anywhere.
   */
  heads: boolean;
};

/**
 * Widths. Thicker than the 2.2 / 1.6 that preceded ADR-0026: a half has to
 * carry a colour of its own, and a 2 px stroke split in two reads as a
 * gradient artefact rather than as two statements.
 */
const LINK_WIDTH = 4.2;
const LINK_WIDTH_LLDP = 3;

export function topologyLinkPaint(opts: {
  online: boolean;
  blocking: boolean;
  /**
   * Ramp ink for each half — `trafficColor(egress, that end's own line rate)`,
   * or `null` when that end has no reading. Resolved by the caller because the
   * ramp lives in `network.ts`, which this module may not import (see header).
   */
  loadInkA: string | null;
  loadInkB: string | null;
  /** LLDP one-sided certainty — dash only, same palette. */
  uncertain?: boolean;
  /** Thinner stroke for LLDP overlay. */
  lldp?: boolean;
}): TopologyLinkPaint {
  const width = opts.lldp ? LINK_WIDTH_LLDP : LINK_WIDTH;
  const opacity = opts.lldp ? 0.9 : 1;
  if (!opts.online) {
    return {
      strokeWidth: width,
      strokeDasharray: "6 5",
      filter: "url(#topo-link-offline)",
      opacity,
      inkA: "#0a0a0a",
      inkB: "#0a0a0a",
      heads: false,
    };
  }
  if (opts.blocking) {
    return {
      strokeWidth: width,
      strokeDasharray: opts.uncertain ? "2 3" : undefined,
      opacity,
      inkA: "var(--accent)",
      inkB: "var(--accent)",
      heads: false,
    };
  }
  return {
    strokeWidth: width,
    strokeDasharray: opts.uncertain ? "2 3" : undefined,
    opacity,
    inkA: opts.loadInkA ?? LINK_UNKNOWN_INK,
    inkB: opts.loadInkB ?? LINK_UNKNOWN_INK,
    heads: true,
  };
}

/**
 * Ink of the `.dot` on a link-diagnostics row (Plan's diagnostics list).
 *
 * # WHY THIS LIVES HERE AND NOT ON THE PAGE
 *
 * It used to be a private arrow on `Plan.tsx`, and it was written by copying
 * the OFFLINE STROKE above — including its `#0a0a0a`. That copy dropped the
 * half that carries the signal: on the map the near-black is only a carrier for
 * `url(#topo-link-offline)`, and it is the filter's RED GLOW that an operator
 * actually sees. The diagnostics row is a plain HTML span with no filter, so
 * the copy produced a near-black dot on a near-black panel — a cut link was the
 * one row in the list you could not see. Keeping the two side by side is what
 * stops the literal being copied a third time.
 *
 * `down` is therefore `--danger` and not the stroke's literal: §2 of
 * UI-GUIDE.md step 1, "something is broken and we know it". It also makes the
 * two views agree — the glow on the map and the dot in the list are now the
 * same red, drawn from the same token.
 */
export function linkDiagColor(status: "up" | "warn" | "down"): string {
  if (status === "down") return "var(--danger)";
  if (status === "warn") return "var(--warn)";
  return "var(--ok)";
}

/**
 * Thin solid green for managed→unknown/unmanaged LLDP peer links.
 *
 * The online stroke is `var(--ok)` and not the `#46a758` it used to be
 * (UI-GUIDE §6, "hard-coded token values in topologyLinks.ts"). It was copied
 * out of the old `loadColor`, which interpolated between two literals and so
 * had a reason to hold one — a reason this function never inherited.
 * `loadColor` itself is gone with ADR-0026: the ramp it hand-rolled was the
 * product's second answer to "how loaded is this", and `trafficColor`
 * (network.ts, ADR-0003) is the first. The offline branch keeps its `#0a0a0a`
 * for the reason stated on `linkDiagColor` above: it is a carrier for the red
 * glow filter, not a colour anybody sees.
 *
 * `var()` on an SVG presentation attribute resolves — measured in both
 * engines, and this same file's link paint has been shipping `var(--accent)`
 * through the identical `stroke={…}` prop all along.
 *
 * It stays THIN, and thinner than ever relative to a real link: ADR-0026 took
 * managed links from 2.2 to 4.2 px, and a ghost peer is precisely the thing
 * that should not look like a link meshcraft can speak about. 1.6 keeps it
 * legible beside the new weight without claiming the same standing.
 */
export function ghostPeerLinkStroke(online: boolean): LinkStroke {
  if (!online) {
    return {
      stroke: "#0a0a0a",
      strokeWidth: 1.6,
      strokeDasharray: "6 5",
      filter: "url(#topo-link-offline)",
      opacity: 0.9,
    };
  }
  return {
    stroke: "var(--ok)",
    strokeWidth: 1.6,
    opacity: 1,
  };
}
