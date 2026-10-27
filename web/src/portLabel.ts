/**
 * Sizing the port number that sits inside a port square.
 *
 * A LEAF MODULE with no imports, and that is what it is for: `network.ts`
 * pulls in the i18n bundle, so anything living there cannot be exercised by
 * the framework-free node suite (`npm test`). This calculation is exactly the
 * kind that must be — it is arithmetic whose failure is invisible in code
 * review and obvious in a screenshot nobody takes. Same reasoning as
 * `pages/portAdmin.ts` and `pages/hostnameConformity.ts`.
 */

/**
 * Approximate advance width of one character, in `em`, for the bold sans-serif
 * the port label is drawn in.
 *
 * A TABLE and not one average, because the strings this is asked about are
 * mostly slashes and digits and the two are nowhere near the same width: a flat
 * 0.6em average reads "1/0/24" as 3.6em when it actually sets at 3.04em, and
 * the label would then be shrunk by 15 % for an overflow that was never going to
 * happen. Shrinking text that already fits is not a safe error here — it is the
 * facade's only legend, at 9px, on a screen an operator reads at arm's length.
 *
 * The numbers are the usual proportions of a bold grotesque (Inter, Helvetica,
 * the -apple-system stack) and they are ESTIMATES. They do not have to be exact,
 * only never UNDER the truth: the caller uses them to decide how much to shrink,
 * so a value that is too small lets a glyph past the border, while one that is
 * slightly too large costs a fraction of a point of size and nothing else. Hence
 * the deliberately generous fallback for anything not listed.
 */
function charEm(c: string): number {
  if (c === "/") return 0.34;
  if (c === "." || c === "-") return 0.36;
  if (c >= "0" && c <= "9") return 0.6;
  if (c >= "A" && c <= "Z") return 0.68;
  return 0.58;
}

/**
 * The font size, in px, at which `text` fits within `width` px.
 *
 * Exists because the port label was drawn at a FIXED 9px inside a square whose
 * width is decided by the caller, with nothing relating the two. That holds for
 * the two-digit names most of the fleet has and breaks on the ones that carry a
 * unit: "10/0/48" sets at ~33px, which is wider than the 32px of usable room
 * inside a 38px square, so the last glyph crossed the border and sat on the
 * neighbouring port. Not a clipping artefact — real text, drawn outside its own
 * jack, on the screen where an operator picks which port to shut.
 *
 * Shrinking is the right answer rather than clipping or an ellipsis, and the
 * reason is what this string IS: a port number is not a label that can be
 * abbreviated. "10/0/4…" names no port, and a truncated number that happens to
 * name a DIFFERENT real port — "1/0/48" cut out of "11/0/48" — is worse than
 * unreadable, it is wrong. So the whole name is always shown, smaller.
 *
 * @param floor the size below which shrinking stops. Text this small is no
 *   longer legible, and a caller that hit the floor has a box too small for a
 *   legend at all — which is the caller's decision to make (`showNum`), not
 *   something to paper over with 4px type. The box clips as a last resort.
 */
export function portLabelFontSize(
  text: string,
  width: number,
  base = 9,
  floor = 6,
): number {
  let em = 0;
  for (const c of text) em += charEm(c);
  if (em <= 0 || width <= 0) return base;
  const fits = width / em;
  if (fits >= base) return base;
  // Quantised to a tenth so that two adjacent squares whose names differ by one
  // character do not sit at sizes separated by a sub-pixel tail, which reads as
  // a row that is not quite aligned.
  //
  // DOWNWARD, and that is not a detail: `Math.round` here undoes the whole
  // function about half the time. "10/0/48" needs 8.695px to fit 32px of room;
  // rounded to 8.7 it sets at 32.016px and crosses the border again — by a
  // sixtieth of a pixel, invisible in review, and exactly the overflow this
  // exists to prevent. Any quantisation of a value computed as an upper bound
  // has to go down.
  return Math.max(floor, Math.floor(fits * 10) / 10);
}

