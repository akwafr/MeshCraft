/**
 * The traffic ramp has ONE colour engine — run with:
 *   node --experimental-strip-types src/trafficRamp.test.ts
 * (from web/)
 *
 * ADR-0030. The ramp (ADR-0003) is consulted from two places that paint very
 * different things: a single colour for one rate (a sparkline label, and since
 * ADR-0026 each half of a topology link) and an SVG gradient down a chart's
 * curve. Those were two renderings of one rule, agreeing only on the six stops
 * and drifting between them.
 *
 * What is pinned here is not a palette — the tokens are free to change — but
 * the two properties that stop them being two rules again:
 *
 *   1. the gradient is built from the SAME function the single colour is, so
 *      every stop it emits is a colour `trafficColorAt` could have returned;
 *   2. the gradient's stops are dense enough that sampling it at a rate lands
 *      within a sliver of `trafficColor` for that rate.
 *
 * Property 2 is checked in RAMP-POSITION space rather than in colour space, so
 * the assertion needs no colour maths and no browser: a stop's offset says
 * which rate it sits at, so the `t` that rate maps to must be within half a
 * sub-band of the `t` the stop was built from.
 */
import assert from "node:assert/strict";
import {
  TRAFFIC_STOPS,
  speedMbps,
  trafficColor,
  trafficColorAt,
  trafficLevel,
  trafficRampStops,
  trafficRateAt,
} from "./trafficRamp.ts";

/* ----------------------------------------------------------------------
 * ONE ENGINE
 * ---------------------------------------------------------------------- */

// `trafficColor` is a composition, not a second implementation. If someone
// re-derives the mix inside it, this parts immediately.
for (const [mbps, cap] of [
  [0, 1000], [1, 1000], [13, 1000], [124, 1000], [387, 1000], [1000, 1000],
  [147, 10000], [0.007, 1000], [5000, 1000],
] as [number, number][]) {
  assert.equal(
    trafficColor(mbps, cap),
    trafficColorAt(trafficLevel(mbps, cap)),
    `trafficColor(${mbps}, ${cap}) must BE trafficColorAt(trafficLevel(...))`,
  );
}

// Every gradient stop is a colour the engine produces. This is what makes the
// gradient a sampling of the ramp instead of a definition of it.
const engineInks = new Set<string>();
for (let i = 0; i <= 1000; i += 1) engineInks.add(trafficColorAt(i / 1000));
for (const cap of [100, 1000, 10000]) {
  for (const s of trafficRampStops(cap)) {
    assert.ok(
      engineInks.has(s.color),
      `stop ink ${s.color} is not something trafficColorAt returns`,
    );
  }
}

// The ends are the ends: bottom of ramp at zero, top at line rate. ADR-0026
// leans on `--traffic-0` being what "at rest" looks like and `--traffic-5` the
// top, so neither may quietly become a mix.
assert.equal(trafficColorAt(0), trafficColor(0, 1000));
assert.ok(trafficColorAt(0).includes("0%"), "t=0 must be the first stop unmixed");
assert.ok(trafficColorAt(1).includes("100%"), "t=1 must be the last stop unmixed");
assert.equal(trafficColorAt(-5), trafficColorAt(0), "t clamps below");
assert.equal(trafficColorAt(5), trafficColorAt(1), "t clamps above");

/* ----------------------------------------------------------------------
 * DENSE ENOUGH THAT THE GRADIENT TRACKS THE ENGINE
 * ---------------------------------------------------------------------- */

for (const cap of [100, 1000, 10000]) {
  const stops = trafficRampStops(cap);

  // Subdivision is what shrank the gap: six stops were one per band.
  assert.ok(
    stops.length >= (TRAFFIC_STOPS.length - 1) * 2 + 1,
    `each band must be subdivided, got ${stops.length} stops`,
  );

  // Offsets are a proper 0..1 ramp, monotonically increasing — SVG ignores a
  // stop that goes backwards, which would silently drop a sub-band.
  assert.equal(stops[0].offset, 0);
  assert.ok(Math.abs(stops[stops.length - 1].offset - 1) < 1e-12);
  for (let i = 1; i < stops.length; i += 1) {
    assert.ok(
      stops[i].offset > stops[i - 1].offset,
      `offsets must increase (stop ${i} at ${stops[i].offset})`,
    );
  }

  // THE PROPERTY THAT MATTERS. Walk the real rate range; for each rate find
  // the sub-band the gradient puts it in, and check the ramp position the
  // gradient is working from is within half a sub-band of the true one. That
  // bounds how far the painted colour can be from `trafficColor`'s.
  const subBand = 1 / (stops.length - 1);
  let worst = 0;
  for (let i = 0; i <= 2000; i += 1) {
    const mbps = (cap * i) / 2000;
    const want = trafficLevel(mbps, cap);
    const off = mbps / cap;
    let k = 0;
    while (k < stops.length - 2 && stops[k + 1].offset < off) k += 1;
    // the two ramp positions this rate is interpolated between
    const lo = k / (stops.length - 1);
    const hi = (k + 1) / (stops.length - 1);
    assert.ok(
      want >= lo - 1e-9 && want <= hi + 1e-9,
      `rate ${mbps} (t=${want}) fell outside its sub-band [${lo}, ${hi}]`,
    );
    worst = Math.max(worst, Math.min(want - lo, hi - want));
  }
  assert.ok(
    worst <= subBand,
    `worst ramp-position error ${worst} exceeds one sub-band ${subBand}`,
  );
}

/* ----------------------------------------------------------------------
 * ONE CAPACITY PARSER
 * ---------------------------------------------------------------------- */

// The labels the fleet actually carries.
assert.equal(speedMbps("1G"), 1000);
assert.equal(speedMbps("10G"), 10000);
assert.equal(speedMbps("100M"), 100);
assert.equal(speedMbps("10M"), 10);
assert.equal(speedMbps("2.5G"), 2500);
assert.equal(speedMbps("1T"), 1000000);
assert.equal(speedMbps(" 1 Gbps "), 1000);

// A BARE NUMBER IS Mb/s. The topology's own parser used to guess the unit from
// the magnitude, so "1000" meant a terabit on a link half and a gigabit on the
// chart of the same port — a factor 1000 on the ramp's anchor (ADR-0030 §3).
assert.equal(speedMbps("1000"), 1000);
assert.equal(speedMbps("100"), 100);
assert.equal(speedMbps("10"), 10);

// Nothing readable falls back to a gigabit, never to zero: zero capacity would
// make every port read as saturated.
for (const bad of ["", null, undefined, "auto", "unknown", "-1", "0"]) {
  assert.equal(speedMbps(bad as string), 1000, `speedMbps(${String(bad)})`);
}

// And the inverse really is the inverse — the gradient's offsets depend on it.
for (const t of [0, 0.13, 0.5, 0.77, 1]) {
  const r = trafficRateAt(t, 1000);
  assert.ok(
    Math.abs(trafficLevel(r, 1000) - t) < 1e-9,
    `trafficRateAt/trafficLevel must round-trip at t=${t}`,
  );
}

console.log("trafficRamp: OK");
