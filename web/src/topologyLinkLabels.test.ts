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

console.log("topologyLinkLabels.test.ts: ok");
