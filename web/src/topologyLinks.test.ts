/**
 * Link-diagnostics ink — run with:
 *   node --experimental-strip-types src/topologyLinks.test.ts
 * (from web/)
 *
 * REGRESSION, 2026-08-08. `linkDiagColor` lived on `Plan.tsx` and had been
 * written by copying the OFFLINE STROKE in this module, `#0a0a0a`. On the map
 * that near-black is only the carrier for `url(#topo-link-offline)` and the
 * filter's red glow is the signal; the diagnostics row is a plain HTML span
 * with no filter, so the copy painted a cut link near-black on a #0f1218 panel
 * — the one row in the list an operator could not see.
 *
 * The pin is deliberately "no raw colour literal", not "down is exactly this
 * red": what must never come back is a hex on this dot, whichever hex it is.
 */
import assert from "node:assert/strict";
import {
  LINK_HEAD_LEN,
  LINK_UNKNOWN_INK,
  ghostPeerLinkStroke,
  linkDiagColor,
  linkHalfShape,
  topologyLinkPaint,
} from "./topologyLinks.ts";

// The bug itself: `down` must not be the invisible near-black it was copied
// from, and must be the danger token — the same red as the glow the map draws
// for the same link, so the two views cannot disagree about one cable.
assert.equal(linkDiagColor("down"), "var(--danger)");
assert.notEqual(linkDiagColor("down"), "#0a0a0a");

// The other two were already tokens; they are pinned so a later edit cannot
// re-introduce the hex on the way past.
assert.equal(linkDiagColor("warn"), "var(--warn)");
assert.equal(linkDiagColor("up"), "var(--ok)");

// The general form of the defect: any hex on this dot is a colour that has
// left the palette and can drift from it (UI-GUIDE §2).
for (const status of ["up", "warn", "down"] as const) {
  const ink = linkDiagColor(status);
  assert.match(
    ink,
    /^var\(--[a-z-]+\)$/,
    `link diagnostics ink for "${status}" must be a token, got ${ink}`,
  );
}

// The three answers are distinct: the dot is the row's only status cue.
const inks = new Set((["up", "warn", "down"] as const).map(linkDiagColor));
assert.equal(inks.size, 3);

// THE SAME DEFECT, one function down. `ghostPeerLinkStroke`'s online branch
// carried `#46a758` — `--ok` written out — copied from `loadColor`, which is
// the ONE place in this module entitled to a literal because it interpolates
// between two of them and interpolation cannot read a `var()`. The copy took
// the hex without the reason.
assert.equal(ghostPeerLinkStroke(true).stroke, "var(--ok)");
assert.notEqual(ghostPeerLinkStroke(true).stroke, "#46a758");

// The OFFLINE branch keeps its near-black, and that is not an oversight: it is
// a carrier for `url(#topo-link-offline)`, whose red flood is the whole of the
// signal. Pinned so the two branches are not "fixed" into agreeing.
assert.equal(ghostPeerLinkStroke(false).stroke, "#0a0a0a");
assert.equal(ghostPeerLinkStroke(false).filter, "url(#topo-link-offline)");

/* ======================================================================
 * ADR-0026 — THE TWO POINTS THAT MEET WITHOUT CROSSING
 * ======================================================================
 *
 * "An arrowhead that never overshoots" is the one detail the request insisted on naming,
 * so it is the one asserted hardest. The invariant is not "the apex is near
 * the middle" — it is that NO VERTEX of a half lies past the midpoint along
 * that half's own direction. That holds at every length, including the ones
 * where a fixed-size point would have run through into the other half.
 */

function parsePoints(points: string): [number, number][] {
  return points
    .trim()
    .split(/\s+/)
    .map((p) => {
      const [x, y] = p.split(",").map(Number);
      return [x, y] as [number, number];
    });
}

/** Signed distance past the midpoint, along the half's own direction. */
function overshoot(
  ex: number,
  ey: number,
  mx: number,
  my: number,
  x: number,
  y: number,
): number {
  const len = Math.hypot(mx - ex, my - ey) || 1;
  const ux = (mx - ex) / len;
  const uy = (my - ey) / len;
  return (x - mx) * ux + (y - my) * uy;
}

// Every length a plan can produce, including the degenerate ones, and both
// orientations so the normal's sign cannot hide a bug on one diagonal.
for (const total of [0, 1, 6, 15, 19, 20, 21, 24, 25, 40, 120, 400, 1600]) {
  for (const angle of [0, 0.4, 1.2, 2.6, -0.9, Math.PI / 2]) {
    const ax = 100;
    const ay = 100;
    const bx = ax + Math.cos(angle) * total;
    const by = ay + Math.sin(angle) * total;
    const mx = (ax + bx) / 2;
    const my = (ay + by) / 2;
    const where = `len=${total} angle=${angle}`;

    for (const [ex, ey] of [
      [ax, ay],
      [bx, by],
    ]) {
      const s = linkHalfShape(ex, ey, mx, my, 4.2, true);

      // The shaft never crosses the midpoint either — it stops AT the point's
      // base, or at the midpoint itself when there is no point.
      assert.ok(
        overshoot(ex, ey, mx, my, s.x2, s.y2) <= 1e-9,
        `shaft crosses the middle (${where})`,
      );

      if (s.head === null) continue;

      const verts = parsePoints(s.head);
      assert.equal(verts.length, 3, `a point is a triangle (${where})`);

      // THE APEX IS THE MIDPOINT, to the rounding the path string carries —
      // not computed to land near it, but written from it.
      const apex = verts[1];
      assert.ok(
        Math.hypot(apex[0] - mx, apex[1] - my) < 0.02,
        `apex is not the midpoint (${where}): ${apex} vs ${[mx, my]}`,
      );

      // NOTHING OVERFLOWS. The two base corners sit back along the half.
      for (const v of verts) {
        assert.ok(
          overshoot(ex, ey, mx, my, v[0], v[1]) <= 0.02,
          `vertex past the middle (${where}): ${v}`,
        );
      }

      // And the point stays inside its OWN half: it may not reach back past
      // the device end either.
      for (const v of verts) {
        assert.ok(
          overshoot(ex, ey, mx, my, v[0], v[1]) >= -(total / 2) - 0.02,
          `vertex behind its own end (${where}): ${v}`,
        );
      }
    }
  }
}

// A SHORT link loses its points rather than wearing two that overlap. 15 px
// total is 7.5 px per half, under the floor; 400 px is comfortably over it.
assert.equal(linkHalfShape(0, 0, 7.5, 0, 4.2, true).head, null, "short: no point");
assert.notEqual(linkHalfShape(0, 0, 200, 0, 4.2, true).head, null, "long: a point");

// Between the floor and full size the point SHRINKS — it is never clipped,
// which is what a fixed length plus a max() would have produced.
const midLen = linkHalfShape(0, 0, 14, 0, 4.2, true);
assert.notEqual(midLen.head, null);
assert.ok(
  14 - midLen.x2 < LINK_HEAD_LEN,
  "a point on a middling half is shorter than a full one",
);
assert.equal(linkHalfShape(0, 0, 200, 0, 4.2, true).x2, 200 - LINK_HEAD_LEN);

// `pointed: false` is a link that carries nothing: the shaft runs all the way
// to the midpoint so the two halves still butt-join into one stroke.
const flat = linkHalfShape(0, 0, 100, 0, 4.2, false);
assert.equal(flat.head, null);
assert.deepEqual([flat.x2, flat.y2], [100, 0]);

/* ======================================================================
 * Which ink wins, and when there are points at all
 * ====================================================================== */

// A HALF WITH NO READING IS NEVER THE BOTTOM OF THE RAMP. Painting it
// `--traffic-0` would say "idle" about a link nobody measured, which is the
// defect ADR-0026 was written against.
const halfUnknown = topologyLinkPaint({
  online: true,
  blocking: false,
  loadInkA: "var(--traffic-3)",
  loadInkB: null,
});
assert.equal(halfUnknown.inkA, "var(--traffic-3)");
assert.equal(halfUnknown.inkB, LINK_UNKNOWN_INK);
assert.notEqual(halfUnknown.inkB, "var(--traffic-0)");

// The unknown ink is a TOKEN, like every other ink this module hands out
// except the offline carrier (see above) — a hex here would leave the palette.
assert.match(LINK_UNKNOWN_INK, /^var\(--[a-z-]+\)$/);

// …and it is NOT `--indeterminate`, on purpose: that yellow is a neighbour of
// `--traffic-5`, the top of the very ramp painted on this stroke, so a half
// nobody can see would read as a saturated one. The reasoning is on
// LINK_UNKNOWN_INK; this pins the outcome so a later "make it consistent with
// UI-GUIDE §2" sweep has to read it first.
assert.notEqual(LINK_UNKNOWN_INK, "var(--indeterminate)");

// POINTS ONLY ON A FORWARDING LINK — the one cue that separates a link at rest
// from a blocked one, `--traffic-0` and `--accent` being two neighbouring blues.
assert.equal(halfUnknown.heads, true);
const blocked = topologyLinkPaint({
  online: true,
  blocking: true,
  loadInkA: "var(--traffic-0)",
  loadInkB: "var(--traffic-0)",
});
assert.equal(blocked.heads, false);
assert.equal(blocked.inkA, "var(--accent)");
assert.equal(blocked.inkA, blocked.inkB, "a blocked link is one uniform stroke");

const down = topologyLinkPaint({
  online: false,
  blocking: false,
  loadInkA: "var(--traffic-5)",
  loadInkB: "var(--traffic-5)",
});
assert.equal(down.heads, false);
assert.equal(down.inkA, down.inkB);
assert.equal(down.filter, "url(#topo-link-offline)");
// Offline outranks load: a rate measured before the device went away must not
// keep colouring its link.
assert.notEqual(down.inkA, "var(--traffic-5)");

// Thicker than the 2.2 px that preceded ADR-0026, and the LLDP overlay stays
// the thinner of the two.
assert.ok(halfUnknown.strokeWidth > 2.2);
assert.ok(
  topologyLinkPaint({
    online: true,
    blocking: false,
    loadInkA: null,
    loadInkB: null,
    lldp: true,
  }).strokeWidth < halfUnknown.strokeWidth,
);

console.log("topologyLinks.test.ts: ok");
