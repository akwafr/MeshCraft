/**
 * The login backdrop's arithmetic — run with:
 *   node --experimental-strip-types src/loginRain.test.ts
 * (from web/)
 *
 * Decoration, but the pointer effect has two promises worth pinning: a wire
 * never reaches past LINK_REACH (a negative `globalAlpha` is IGNORED by the
 * canvas, so the wire would be drawn at whatever alpha came before), and the
 * nearest heads come first, so the brightest wire is the shortest.
 */
import assert from "node:assert/strict";
import {
  LINK_REACH,
  linkAlpha,
  nearestWithin,
  newDrop,
  stepDrop,
  withAlpha,
} from "./loginRain.ts";

const pts = [
  { x: 0, y: 0 },
  { x: 100, y: 0 },
  { x: 10, y: 0 },
  { x: LINK_REACH + 1, y: 0 },
  { x: 50, y: 0 },
];
const near = nearestWithin(pts, { x: 0, y: 0 });
assert.deepEqual(
  near.map((n) => n.index),
  [0, 2, 4],
  "nearest first, three at most",
);
assert.ok(
  nearestWithin(pts, { x: 0, y: 0 }, 10).every((n) => n.dist <= LINK_REACH),
  "nothing past the reach, however many are asked for",
);

assert.equal(linkAlpha(0), 0.5);
assert.equal(linkAlpha(LINK_REACH), 0);
assert.equal(linkAlpha(LINK_REACH * 3), 0, "never negative");

assert.equal(withAlpha("rgb(79, 124, 255)", 0.1), "rgba(79, 124, 255, 0.1)");
assert.equal(withAlpha("rgba(1, 2, 3, 0.5)", 1), "rgba(1, 2, 3, 1)");
assert.equal(withAlpha("rgb(79 124 255)", 0.2), "rgba(79, 124, 255, 0.2)");
assert.equal(withAlpha("color(srgb 1 0 0)", 0.3), "color(srgb 1 0 0)");

// A first screen does not start with every column at the top.
const fixed = () => 0.5;
assert.ok(newDrop(40, fixed).y <= 0);
// A head past the bottom restarts only on its draw, and then at the top.
assert.equal(stepDrop({ y: 41, speed: 1 }, 40, () => 0.1).y, 42);
assert.equal(stepDrop({ y: 41, speed: 1 }, 40, () => 0.99).y, 0);
// On screen, it only falls.
assert.equal(stepDrop({ y: 3, speed: 0.75 }, 40, () => 0.99).y, 3.75);

console.log("loginRain.test.ts: ok");
