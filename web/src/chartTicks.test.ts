/**
 * Checks on the two index spaces of a chart — run with:
 *   node --experimental-strip-types src/chartTicks.test.ts
 * (from web/), or `npm test` to run every file at once.
 *
 * WHAT IS BEING PINNED: an axis label names the instant the drawn point was
 * actually computed from. That sounds tautological and was false for every
 * chart in the product.
 *
 * `LineChart` resamples to PLOT_POINTS before drawing, so a hover yields an
 * index in DRAWN space while every label is written in RAW space — from
 * `points[i]`, or from `i * step_seconds`. Each caller used the drawn index as
 * if it were raw. On the site traffic tile that is 1441 raw points over 150
 * drawn: hovering the middle of the chart announced 01:15 of a window running
 * 00:00→24:00, an error of nine and a half hours.
 *
 * It survived review because all of those callers special-cased the newest
 * point as "now", and the newest point is the ONE index where the two spaces
 * coincide — so the right edge, which is where anybody checks, was always
 * right. And because `resampleMean` returns its input untouched below the
 * target, nothing at all was wrong under 150 points, which made a fully
 * deterministic defect look like it came and went.
 *
 * Both of those are regression traps, so both are asserted below: the newest
 * tick (the case that masked it) and a short series (the case that hid it).
 */
import assert from "node:assert/strict";
import { PLOT_POINTS, resampleMean, xTickAt } from "./chartTicks.ts";

// --- The defect itself: a 24 h window at one sample a minute ---------------
//
// 1441 points, the exact shape the dashboard's "site throughput" tile feeds
// over 24 h, stamped one minute apart from midnight.
const RAW = 1441;
const STEP = 60;
const START = Date.UTC(2026, 9, 24, 0, 0, 0);
const stampOf = (i: number) => new Date(START + i * STEP * 1000);

// The series carries its own index as its value, so a resampled point tells
// you exactly which raw indices went into it: the mean of a run of
// consecutive integers is the midpoint of that run.
const series: (number | null)[] = Array.from({ length: RAW }, (_, i) => i);
const drawnPoints = resampleMean(series, PLOT_POINTS);
assert.equal(drawnPoints.length, PLOT_POINTS, "a 1441-point series is drawn as 150");

// THE CORE ASSERTION. For every drawn point, the tick's raw index must fall
// inside the bucket of raw samples that drawn point is the mean of — and be
// the last of them, the instant the bucket ends.
//
// This is what ties the label to the curve. It is checked against
// `resampleMean` itself rather than against a recomputed bucket size, so the
// day one of the two changes, the pair fails here instead of drifting apart
// silently the way the label and the curve already did once.
var d = 0;
for (d = 0; d < PLOT_POINTS; d++) {
  const tick = xTickAt(d, PLOT_POINTS, RAW);
  const mean = drawnPoints[d];
  assert.ok(mean != null, "bucket " + d + " has samples");
  // `mean` is the midpoint of the raw run; the run's last index is therefore
  // at most half a bucket above it, and the tick must be exactly that.
  const bucket = RAW / PLOT_POINTS;
  // `resampleMean` averages raw [floor(d*bucket), floor((d+1)*bucket)), so the
  // last sample of the run is one below that upper bound. The final bucket is
  // stated rather than computed, for the same floating-point reason `xTickAt`
  // states it: `drawn * (total / drawn)` is not reliably `total`.
  const lastOfRun = d === PLOT_POINTS - 1 ? RAW - 1 : Math.floor((d + 1) * bucket) - 1;
  assert.equal(
    tick.rawIndex,
    lastOfRun,
    "drawn point " + d + " must be named by the last raw sample it averaged",
  );
  assert.ok(
    tick.rawIndex >= (mean as number) && tick.rawIndex - (mean as number) <= bucket,
    "drawn point " + d + " names an instant inside its own bucket",
  );
  assert.equal(tick.rawTotal, RAW, "the total handed out is the RAW length");
}

// The middle of the chart names the middle of the window. This is the user-
// visible claim, stated in clock time rather than in indices.
//
// Before the fix the label was built from the drawn index, 75, which
// `hourLabel` resolved to points[75] — 01:15. Nine and a half hours early,
// on the most-read chart in the product.
const mid = xTickAt(Math.floor(PLOT_POINTS / 2), PLOT_POINTS, RAW);
assert.notEqual(mid.rawIndex, 75, "the drawn index must not be passed through");
assert.equal(mid.rawIndex, 729);
assert.equal(stampOf(mid.rawIndex).toISOString(), "2026-10-24T12:09:00.000Z");
// ...and the age form, which is how the port tips write the same label:
// `(rawTotal - 1 - rawIndex) * step_seconds` seconds ago.
assert.equal((mid.rawTotal - 1 - mid.rawIndex) * STEP, 42660); // 11 h 51 ago, i.e. mid-window
assert.equal(mid.newest, false);

// A quarter of the way along, same claim at another point: no off-by-a-bucket
// that happens to cancel at the centre.
const quarter = xTickAt(Math.floor(PLOT_POINTS / 4), PLOT_POINTS, RAW);
assert.equal(stampOf(quarter.rawIndex).toISOString(), "2026-10-24T06:04:00.000Z");

// --- The case that MASKED the defect: the newest point --------------------
//
// The right edge must still land exactly on the last raw sample, so every
// caller's "now" keeps meaning now. This is why a tick names the END of its
// bucket and not its midpoint: a midpoint would have put the newest label
// four minutes into the past on this very series.
const last = xTickAt(PLOT_POINTS - 1, PLOT_POINTS, RAW);
assert.equal(last.rawIndex, RAW - 1, "the newest drawn point is the newest raw sample");
assert.equal(last.newest, true);
assert.equal(stampOf(last.rawIndex).toISOString(), "2026-10-25T00:00:00.000Z");
// `newest` is a flag precisely so no caller spells it `i === n - 1` again,
// but a caller that compares raw indices must not be betrayed either.
assert.equal(last.rawIndex === last.rawTotal - 1, true);

// Nothing before the right edge claims to be the newest: the "now" label must
// appear once, at one end.
var seen = 0;
for (d = 0; d < PLOT_POINTS; d++) {
  if (xTickAt(d, PLOT_POINTS, RAW).newest) seen++;
}
assert.equal(seen, 1, "exactly one drawn point is the newest");

// Ticks walk forward with the pointer. A non-monotone mapping would let the
// tooltip go backwards in time while the mouse moves right.
var prev = -1;
for (d = 0; d < PLOT_POINTS; d++) {
  const raw = xTickAt(d, PLOT_POINTS, RAW).rawIndex;
  assert.ok(raw > prev, "tick " + d + " moves forward");
  prev = raw;
}

// --- The case that HID the defect: a series shorter than PLOT_POINTS ------
//
// `resampleMean` is a no-op below its target, so the two spaces are one and
// nothing may change here. This is the shape of a port tip on a short
// history, and it is why the bug read as intermittent.
const SHORT = 100;
const shortSeries: (number | null)[] = Array.from({ length: SHORT }, (_, i) => i);
assert.equal(resampleMean(shortSeries, PLOT_POINTS).length, SHORT, "no resampling under the target");
for (d = 0; d < SHORT; d++) {
  const tick = xTickAt(d, SHORT, SHORT);
  assert.equal(tick.rawIndex, d, "short series: the drawn index IS the raw index");
  assert.equal(tick.rawTotal, SHORT);
  assert.equal(tick.newest, d === SHORT - 1);
}

// The boundary itself: exactly PLOT_POINTS points is still a no-op.
const edge = resampleMean(
  Array.from({ length: PLOT_POINTS }, (_, i) => i),
  PLOT_POINTS,
);
assert.equal(edge.length, PLOT_POINTS);
assert.equal(xTickAt(40, PLOT_POINTS, PLOT_POINTS).rawIndex, 40);

// One point over, and the mapping engages — the first index where the old
// pass-through stopped being harmless.
const over = xTickAt(0, PLOT_POINTS, PLOT_POINTS + 1);
assert.equal(over.rawIndex, 0);
assert.equal(xTickAt(PLOT_POINTS - 1, PLOT_POINTS, PLOT_POINTS + 1).rawIndex, PLOT_POINTS);

// --- Degenerate series ----------------------------------------------------
//
// `LineChart` floors its point count at 2, so a one-sample or empty series
// still hovers. A tick must stay inside the array either way: an out-of-range
// `rawIndex` is an undefined timestamp, which is how an axis label becomes a
// crash in a tooltip.
assert.deepEqual(xTickAt(0, 2, 1), { rawIndex: 0, rawTotal: 1, newest: true });
assert.deepEqual(xTickAt(1, 2, 1), { rawIndex: 0, rawTotal: 1, newest: true });
assert.deepEqual(xTickAt(1, 2, 2), { rawIndex: 1, rawTotal: 2, newest: true });
assert.deepEqual(xTickAt(0, 2, 0), { rawIndex: 0, rawTotal: 1, newest: true });
// A pointer somehow past the right edge clamps rather than reading past the
// end of the series.
assert.equal(xTickAt(400, PLOT_POINTS, RAW).rawIndex, RAW - 1);
assert.equal(xTickAt(-3, PLOT_POINTS, RAW).rawIndex, 0);

// --- The other windows the traffic tile offers ----------------------------
//
// 3 d, 7 d and 30 d go through the same route at a coarser step. Each must
// land its middle tick in the middle of its own window, which is the property
// that was wrong by a factor of `raw / 150` on all four at once.
const windows = [
  { raw: 1441, step: 60 },
  { raw: 1081, step: 240 },
  { raw: 1009, step: 600 },
  { raw: 1441, step: 1800 },
];
var w = 0;
for (w = 0; w < windows.length; w++) {
  const win = windows[w];
  const tick = xTickAt(Math.floor(PLOT_POINTS / 2), PLOT_POINTS, win.raw);
  const fraction = tick.rawIndex / (win.raw - 1);
  assert.ok(
    Math.abs(fraction - 0.5) < 0.01,
    "window of " + win.raw + " points: the middle tick is mid-window, not at " + fraction,
  );
  // And the age it yields is half the window, not a 150th of it.
  const back = (tick.rawTotal - 1 - tick.rawIndex) * win.step;
  const whole = (win.raw - 1) * win.step;
  assert.ok(Math.abs(back / whole - 0.5) < 0.01, "window of " + win.raw + ": age is mid-window");
}

console.log("chartTicks: OK");
