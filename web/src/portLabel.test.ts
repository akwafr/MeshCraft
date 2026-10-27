/**
 * Lightweight checks for the port-label sizing — run with:
 *   node --experimental-strip-types src/portLabel.test.ts
 * (from web/), or `npm test` to run every file at once.
 *
 * `portLabelFontSize` exists to stop a port number being drawn outside its own
 * jack, so the assertions below are written as that failure rather than as
 * coverage. The square it serves is 38px on the equipment detail screen, with
 * 3px of padding each side — 32px of usable room — and those are the numbers
 * used throughout so a regression shows up as the real case and not an abstract
 * one.
 */
import assert from "node:assert/strict";
// Node --experimental-strip-types needs the .ts suffix; tsc excludes this file.
import { portLabelFontSize } from "./portLabel.ts";

/** Usable room inside a PORT_SIZE square on the equipment detail screen. */
const ROOM = 38 - 3 * 2;

/**
 * The same estimate the module uses, restated here on purpose.
 *
 * A test that imported the module's own width function would assert that the
 * code agrees with itself and would pass with the table set to zero. This is a
 * SECOND opinion — deliberately independent, deliberately cruder — and every
 * assertion below is that the rendered string stays inside the box by this
 * reckoning, not by the one under test.
 */
function widthPx(text: string, fontSize: number): number {
  let em = 0;
  for (const c of text) {
    if (c === "/") em += 0.34;
    else if (c >= "0" && c <= "9") em += 0.6;
    else em += 0.68;
  }
  return em * fontSize;
}

// THE CASE THAT WAS BROKEN. A stacked switch's silkscreen carries a unit, and
// "10/0/48" at the old fixed 9px sets wider than the square it names — the
// last glyph landed on the neighbouring port. Whatever size comes back, the
// string must fit.
{
  const s = portLabelFontSize("10/0/48", ROOM);
  assert.ok(s < 9, `a 7-character name must shrink, got ${s}`);
  assert.ok(
    widthPx("10/0/48", s) <= ROOM,
    `"10/0/48" still overflows at ${s}px: ${widthPx("10/0/48", s)} > ${ROOM}`,
  );
}

// AND THE CASE THAT MUST NOT REGRESS. The names most of the fleet carries
// already fit, and shrinking them would cost legibility on the facade's only
// legend for an overflow that never happens.
for (const name of ["1", "24", "48", "S2", "1/11", "1/0/24"]) {
  const s = portLabelFontSize(name, ROOM);
  assert.equal(s, 9, `"${name}" fits at 9px and must be left alone, got ${s}`);
  assert.ok(widthPx(name, s) <= ROOM, `"${name}" does not fit at 9px`);
}

// The shrink has a FLOOR, and past it the caller's box is simply too small for a
// legend — that is `showNum`'s decision, not something to paper over with
// unreadable type. What matters here is only that the floor holds.
{
  const s = portLabelFontSize("100/0/100", 8);
  assert.equal(s, 6, `the floor must hold, got ${s}`);
}

// Degenerate inputs must not produce NaN or Infinity: they reach a React
// `style.fontSize`, where either one silently drops the declaration and the
// label comes back at the browser default — far LARGER than 9px, i.e. the exact
// overflow this function exists to prevent, arriving by the failure path.
for (const [text, room] of [["", ROOM], ["24", 0], ["", 0]] as const) {
  const s = portLabelFontSize(text, room);
  assert.ok(Number.isFinite(s), `size must stay finite for (${text}, ${room})`);
  assert.ok(s > 0, `size must stay positive for (${text}, ${room})`);
}

// EVERY name a real faceplate in this fleet can produce, at the size it is
// actually drawn. The list is the point: the fix is only worth anything if it
// holds for the whole range, and a single hand-picked example would have passed
// against the broken version too.
//
// `portShortNum` is what feeds these when a platform declares no silkscreen
// name, and its output is written out literally rather than imported — that
// function lives in network.ts, behind the i18n import this suite cannot load.
for (const name of [
  "1", "9", "24", "48", "52",        // flat numbering (Instant On, SwOS)
  "S1", "S2", "S18",                 // RouterOS cages, via portShortNum
  "1/11", "1/24",                    // FS S3900 silkscreen
  "1/0/1", "1/0/24", "1/0/48",       // Cisco / EdgeSwitch
  "10/0/48", "11/0/48",              // a stacked unit, the case that broke
  "49/1", "49/4",                    // Arista breakout
]) {
  const s = portLabelFontSize(name, ROOM);
  const w = widthPx(name, s);
  assert.ok(w <= ROOM, `"${name}" overflows at ${s}px: ${w} > ${ROOM}`);
  assert.ok(s >= 6 && s <= 9, `"${name}" sized outside the range: ${s}`);
}

console.log("portLabel: OK");
