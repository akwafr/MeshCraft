/**
 * Topology link decorations: port names, live bandwidth, speed.
 *
 * Pills are placed dynamically along the segment. When the segment shortens
 * (or labels would collide), layers disappear in this order:
 *   1. interface names
 *   2. realtime bandwidth
 *   3. link speed
 */

export type LinkLabelLayer = "ports" | "bw" | "speed" | "extra";

export type LinkLabelSpec = {
  layer: LinkLabelLayer;
  text: string;
  fill?: string;
  fontSize?: number;
  letterSpacing?: number;
  /** Preferred position along the segment [0..1]. */
  t: number;
  /** Signed offset along the segment normal (px). */
  offset: number;
};

export type PlacedLinkLabel = LinkLabelSpec & {
  x: number;
  y: number;
  w: number;
  h: number;
};

const PAD_X = 5;
const PAD_Y = 2.5;
const COLLIDE_PAD = 4;

/** Hide order when space is tight (first = drops first). */
const HIDE_ORDER: LinkLabelLayer[] = ["ports", "bw", "extra", "speed"];

export function estimatePillSize(text: string, fontSize = 9): { w: number; h: number } {
  const w = Math.max(20, text.length * fontSize * 0.58 + PAD_X * 2);
  const h = fontSize + PAD_Y * 2;
  return { w, h };
}

function rectsOverlap(
  a: { x: number; y: number; w: number; h: number },
  b: { x: number; y: number; w: number; h: number },
  pad = COLLIDE_PAD,
): boolean {
  return !(
    a.x + a.w / 2 + pad < b.x - b.w / 2 ||
    b.x + b.w / 2 + pad < a.x - a.w / 2 ||
    a.y + a.h / 2 + pad < b.y - b.h / 2 ||
    b.y + b.h / 2 + pad < a.y - a.h / 2
  );
}

function anyOverlap(placed: PlacedLinkLabel[]): boolean {
  for (let i = 0; i < placed.length; i++) {
    for (let j = i + 1; j < placed.length; j++) {
      if (rectsOverlap(placed[i], placed[j])) return true;
    }
  }
  return false;
}

/** Soft length gates — collision resolution may drop more layers. */
function layerBudget(len: number): Set<LinkLabelLayer> {
  const keep = new Set<LinkLabelLayer>(["ports", "bw", "extra", "speed"]);
  if (len < 170) keep.delete("ports");
  if (len < 115) keep.delete("bw");
  if (len < 90) keep.delete("extra");
  if (len < 42) keep.delete("speed");
  return keep;
}

/**
 * After dropping a layer, spread remaining pills so they use the free space.
 */
function reflowSpecs(specs: LinkLabelSpec[]): LinkLabelSpec[] {
  const ports = specs.filter((s) => s.layer === "ports");
  const bw = specs.filter((s) => s.layer === "bw");
  const speed = specs.filter((s) => s.layer === "speed");
  const extra = specs.filter((s) => s.layer === "extra");
  const out: LinkLabelSpec[] = [];

  if (ports.length === 2) {
    out.push({ ...ports[0], t: 0.12, offset: -12 });
    out.push({ ...ports[1], t: 0.88, offset: -12 });
  } else if (ports.length === 1) {
    out.push({ ...ports[0], t: 0.15, offset: -12 });
  }

  if (bw.length === 2) {
    // Without ports, pull bandwidth toward the ends; with ports, stay mid-thirds.
    const lo = ports.length ? 0.32 : 0.22;
    const hi = ports.length ? 0.68 : 0.78;
    out.push({ ...bw[0], t: lo, offset: ports.length ? 13 : 11 });
    out.push({ ...bw[1], t: hi, offset: ports.length ? 13 : 11 });
  } else if (bw.length === 1) {
    out.push({ ...bw[0], t: 0.35, offset: 12 });
  }

  if (speed.length) {
    const offset = bw.length || extra.length ? -8 : 0;
    out.push({ ...speed[0], t: 0.5, offset });
  }

  if (extra.length) {
    out.push({ ...extra[0], t: 0.5, offset: speed.length ? 14 : 0 });
  }

  return out;
}

function placeAttempt(
  x1: number,
  y1: number,
  x2: number,
  y2: number,
  specs: LinkLabelSpec[],
): PlacedLinkLabel[] {
  const dx = x2 - x1;
  const dy = y2 - y1;
  const len = Math.hypot(dx, dy) || 1;
  let nx = -dy / len;
  let ny = dx / len;
  // Prefer the normal that points upward on screen (negative Y).
  if (ny > 0) {
    nx = -nx;
    ny = -ny;
  }

  let placed: PlacedLinkLabel[] = specs.map((s) => {
    const fs = s.fontSize ?? 9;
    const size = estimatePillSize(s.text, fs);
    return {
      ...s,
      x: x1 + dx * s.t + nx * s.offset,
      y: y1 + dy * s.t + ny * s.offset,
      w: size.w,
      h: size.h,
    };
  });

  // Nudge colliding pills along the segment (±Δt) a few times.
  for (let round = 0; round < 10 && anyOverlap(placed); round++) {
    for (let i = 0; i < placed.length; i++) {
      for (let j = i + 1; j < placed.length; j++) {
        if (!rectsOverlap(placed[i], placed[j])) continue;
        const a = placed[i];
        const b = placed[j];
        const delta = 0.04;
        if (a.t <= b.t) {
          a.t = Math.max(0.08, a.t - delta);
          b.t = Math.min(0.92, b.t + delta);
        } else {
          b.t = Math.max(0.08, b.t - delta);
          a.t = Math.min(0.92, a.t + delta);
        }
        // Flip the smaller pill to the other side of the stroke when still packed.
        if (round >= 3) {
          if (a.w <= b.w) a.offset = -a.offset;
          else b.offset = -b.offset;
        }
      }
    }
    placed = placed.map((s) => ({
      ...s,
      x: x1 + dx * s.t + nx * s.offset,
      y: y1 + dy * s.t + ny * s.offset,
    }));
  }
  return placed;
}

/**
 * Resolve which pills to draw and where, for the current segment geometry.
 */
export function resolveLinkLabels(
  x1: number,
  y1: number,
  x2: number,
  y2: number,
  specs: LinkLabelSpec[],
): PlacedLinkLabel[] {
  const usable = specs.filter((s) => s.text && s.text.trim());
  if (usable.length === 0) return [];

  const len = Math.hypot(x2 - x1, y2 - y1);
  const allowed = layerBudget(len);
  let active = reflowSpecs(usable.filter((s) => allowed.has(s.layer)));

  for (let attempt = 0; attempt < HIDE_ORDER.length + 1; attempt++) {
    const placed = placeAttempt(x1, y1, x2, y2, active);
    if (!anyOverlap(placed)) return placed;

    let drop: LinkLabelLayer | null = null;
    for (let li = 0; li < HIDE_ORDER.length; li++) {
      const layer = HIDE_ORDER[li];
      if (active.some((s) => s.layer === layer)) {
        drop = layer;
        break;
      }
    }
    if (!drop) return placed;
    active = reflowSpecs(active.filter((s) => s.layer !== drop));
    if (active.length === 0) return [];
  }
  return placeAttempt(x1, y1, x2, y2, active);
}

/** Build the usual port / bandwidth / speed specs for a topology link. */
export function buildLinkLabelSpecs(opts: {
  portA?: string | null;
  portB?: string | null;
  speedText?: string | null;
  speedFill?: string;
  speedLetterSpacing?: number;
  bwA?: string | null;
  bwB?: string | null;
  extraText?: string | null;
}): LinkLabelSpec[] {
  const out: LinkLabelSpec[] = [];
  if (opts.portA) {
    out.push({
      layer: "ports",
      text: opts.portA,
      fontSize: 8,
      fill: "var(--text)",
      t: 0.12,
      offset: -12,
    });
  }
  if (opts.portB) {
    out.push({
      layer: "ports",
      text: opts.portB,
      fontSize: 8,
      fill: "var(--text)",
      t: 0.88,
      offset: -12,
    });
  }
  if (opts.bwA) {
    out.push({
      layer: "bw",
      text: opts.bwA,
      fontSize: 9,
      t: 0.3,
      offset: 13,
    });
  }
  if (opts.bwB) {
    out.push({
      layer: "bw",
      text: opts.bwB,
      fontSize: 9,
      t: 0.7,
      offset: 13,
    });
  }
  if (opts.speedText) {
    out.push({
      layer: "speed",
      text: opts.speedText,
      fill: opts.speedFill ?? "var(--text)",
      letterSpacing: opts.speedLetterSpacing,
      fontSize: 9,
      t: 0.5,
      offset: -7,
    });
  }
  if (opts.extraText) {
    out.push({
      layer: "extra",
      text: opts.extraText,
      fontSize: 9,
      t: 0.5,
      offset: 14,
    });
  }
  return out;
}
