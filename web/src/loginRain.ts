/**
 * The login screen's backdrop: glyphs falling in columns, and a pointer that
 * lights the heads near it and wires itself to the closest ones.
 *
 * DECORATIVE, and the only motion in the product allowed to be: everywhere
 * else an animation carries a state (UI-GUIDE §4), and the login screen is
 * the one screen that shows none. LoginBackdrop.tsx holds the canvas; the
 * arithmetic lives here, without the DOM, so it can be tested:
 *   node --experimental-strip-types src/loginRain.test.ts   (from web/)
 */

/** Hex and the punctuation of addresses, because this is a network tool.
 *  Not the katakana the falling-code look usually borrows: a machine with
 *  no CJK font draws them as empty boxes (measured, on headless Chromium),
 *  and every monospace font has these. */
export const RAIN_GLYPHS = "0123456789ABCDEF0123456789abcdef:./<>=+*#";

/** The pointer's reach, in CSS pixels: the halo lights heads within
 *  HALO_RADIUS, and at most LINKS heads within LINK_REACH get a wire. */
export const HALO_RADIUS = 130;
export const LINK_REACH = 280;
export const LINKS = 3;

/** A column's falling head. `y` counts rows, and goes negative so the first
 *  screen does not start with every column at the top at once. */
export interface Drop {
  y: number;
  speed: number;
}

export interface Point {
  x: number;
  y: number;
}

function speedFrom(rand: () => number): number {
  return 0.5 + rand() * 0.7;
}

export function newDrop(rows: number, rand: () => number): Drop {
  return { y: -Math.floor(rand() * rows), speed: speedFrom(rand) };
}

/** One tick of fall. A head past the bottom lingers off screen and restarts
 *  at random, which is what staggers the columns after the first pass. */
export function stepDrop(d: Drop, rows: number, rand: () => number): Drop {
  const y = d.y + d.speed;
  if (y > rows && rand() > 0.975) return { y: 0, speed: speedFrom(rand) };
  return { y, speed: d.speed };
}

/** The `k` points closest to `at`, nearest first, none farther than `reach`. */
export function nearestWithin(
  points: readonly Point[],
  at: Point,
  k = LINKS,
  reach = LINK_REACH,
): { index: number; dist: number }[] {
  return points
    .map((p, index) => ({ index, dist: Math.hypot(p.x - at.x, p.y - at.y) }))
    .filter((c) => c.dist <= reach)
    .sort((a, b) => a.dist - b.dist)
    .slice(0, k);
}

/** A wire fades with length, and is gone exactly at LINK_REACH. */
export function linkAlpha(dist: number): number {
  return Math.max(0, 0.5 - dist / (2 * LINK_REACH));
}

/**
 * `rgb(r, g, b)` → `rgba(r, g, b, a)`. The colours come from the tokens
 * through getComputedStyle, which resolves `light-dark()` to rgb; a canvas
 * gradient needs the alpha IN the colour, because fading to `transparent`
 * fades through black on the way. Anything unparsed is returned as is.
 */
export function withAlpha(color: string, a: number): string {
  const m = /^rgba?\(\s*([\d.]+)[\s,]+([\d.]+)[\s,]+([\d.]+)/.exec(color);
  if (!m) return color;
  return `rgba(${m[1]}, ${m[2]}, ${m[3]}, ${a})`;
}
