/**
 * Lightweight checks for topology link label placement — run with:
 *   node --experimental-strip-types src/topologyLinkLabels.test.ts
 * (from web/)
 */
import assert from "node:assert/strict";
import {
  buildLinkLabelSpecs,
  resolveLinkLabels,
} from "./topologyLinkLabels.ts";

function layersOf(x1: number, y1: number, x2: number, y2: number) {
  const specs = buildLinkLabelSpecs({
    portA: "ether1",
    portB: "ether24",
    speedText: "1G",
    bwA: "◂ 12 Mbit/s",
    bwB: "▸ 8 Mbit/s",
  });
  return new Set(resolveLinkLabels(x1, y1, x2, y2, specs).map((p) => p.layer));
}

// Long horizontal link: all layers.
{
  const layers = layersOf(0, 0, 320, 0);
  assert.ok(layers.has("ports"), "long: ports");
  assert.ok(layers.has("bw"), "long: bw");
  assert.ok(layers.has("speed"), "long: speed");
}

// Medium: drop ports first.
{
  const layers = layersOf(0, 0, 130, 0);
  assert.equal(layers.has("ports"), false, "medium: no ports");
  assert.ok(layers.has("bw") || layers.has("speed"), "medium: keeps bw or speed");
}

// Short: no ports, no bw — speed may remain.
{
  const layers = layersOf(0, 0, 80, 0);
  assert.equal(layers.has("ports"), false, "short: no ports");
  assert.equal(layers.has("bw"), false, "short: no bw");
}

// Tiny: everything gone or speed only.
{
  const layers = layersOf(0, 0, 30, 0);
  assert.equal(layers.has("ports"), false);
  assert.equal(layers.has("bw"), false);
  assert.equal(layers.has("speed"), false);
}

// No AABB overlap among survivors on a busy medium segment.
{
  const specs = buildLinkLabelSpecs({
    portA: "sfp-sfpplus1",
    portB: "sfp-sfpplus2",
    speedText: "10G",
    bwA: "◂ 1.2 Gbit/s",
    bwB: "▸ 900 Mbit/s",
  });
  const placed = resolveLinkLabels(0, 0, 140, 40, specs);
  for (let i = 0; i < placed.length; i++) {
    for (let j = i + 1; j < placed.length; j++) {
      const a = placed[i];
      const b = placed[j];
      const overlap = !(
        a.x + a.w / 2 + 4 < b.x - b.w / 2 ||
        b.x + b.w / 2 + 4 < a.x - a.w / 2 ||
        a.y + a.h / 2 + 4 < b.y - b.h / 2 ||
        b.y + b.h / 2 + 4 < a.y - a.h / 2
      );
      assert.equal(overlap, false, `overlap ${a.layer}/${b.layer}`);
    }
  }
}

/*
 * NO MID PILL MAY SIT ON THE STROKE (ADR-0026).
 *
 * The two meeting points of a link live at its midpoint, reaching ±5.4 px off
 * the line — exactly where the speed pill used to be placed, with an opaque
 * `--bg-panel` backing that would have hidden them. The pills whose preferred
 * position is the middle must therefore clear the stroke by more than that
 * half-width plus their own half-height.
 *
 * Asserted on the PLACED geometry rather than on the constant, so it also
 * catches the collision resolver nudging a pill back onto the line.
 */
{
  const HEAD_HALF_W = 5.4;
  const COLLIDE_PAD = 4;
  for (const spec of [
    { speedText: "10G" },
    { speedText: "10G", extraText: "forwarding / discarding" },
    { extraText: "forwarding / discarding" },
    {
      portA: "XG3",
      portB: "GE49",
      speedText: "10G",
      bwA: "▸ 1.2 Gbit/s",
      bwB: "◂ ?",
      extraText: "root / designated",
    },
  ]) {
    const placed = resolveLinkLabels(0, 0, 320, 0, buildLinkLabelSpecs(spec));
    for (const p of placed) {
      if (p.layer !== "speed" && p.layer !== "extra") continue;
      const clear = Math.abs(p.y) - p.h / 2;
      assert.ok(
        clear >= HEAD_HALF_W + COLLIDE_PAD,
        `${p.layer} pill crowds the points: clears ${clear.toFixed(1)} px`,
      );
    }
  }
}

console.log("topologyLinkLabels.test.ts: ok");
