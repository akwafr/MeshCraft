// THE TWO INDEX SPACES OF A CHART, AND THE ONE MODULE THAT KNOWS BOTH.
//
// A chart is handed a series of RAW samples and draws a smaller number of
// points, because more samples than pixels is noise (see `resampleMean`).
// That leaves two numberings of the same curve:
//
//   raw space    — the caller's array, 1441 points for a 24 h window at one
//                  sample a minute. The caller owns the timestamps, the step
//                  between samples, and therefore every label it can write.
//   drawn space  — what the SVG has room for, PLOT_POINTS of them. Where the
//                  pointer lands, and nothing else.
//
// A hover gives a position in DRAWN space; a label has to be written in RAW
// space. Converting between the two is the job of this file, and it lives
// here rather than in the chart or in a caller because the conversion is only
// correct if it uses the SAME buckets the resampling used — so the function
// that buckets and the function that names a bucket must be read together.
//
// They were not, for a while. Every caller wrote its label from the drawn
// index as if it were a raw one: on the site traffic tile that is 1441 raw
// points over 150 drawn, so hovering the middle of the chart announced the
// time an hour and a half into a twenty-four hour window. It survived because
// every one of those callers special-cased the newest point as "now", which is
// the one index where both spaces agree and the one place anybody checks.
//
// Hence `XTick`: the chart hands the caller a NAMED raw index, and there is no
// longer a drawn index in scope for a label to be written from by mistake.

/**
 * MORE SAMPLES THAN PIXELS IS NOISE, NOT DETAIL.
 *
 * A 24 h window at one sample a minute is 1440 points drawn across roughly
 * 770 of viewBox width. Every curve through them is a hairball, and
 * smoothing the CURVE cannot fix it — the spline is faithful, the data is
 * simply denser than the display. The fix is to aggregate before drawing.
 *
 * The aggregate is the MEAN, and that is the honest one for a rate: the mean
 * of a rate over a bucket is the rate over that bucket, so the area under
 * the curve still reads as bytes moved. A max would keep every spike and
 * claim the site sustained it; a min would hide outages.
 *
 * A bucket with no sample at all stays null — a gap in the measurement is
 * not a zero, and must keep breaking the line.
 */
export function resampleMean(points: (number | null)[], target: number): (number | null)[] {
  if (points.length <= target) return points;
  var out: (number | null)[] = [];
  var bucket = points.length / target;
  var i = 0;
  for (i = 0; i < target; i++) {
    var from = Math.floor(i * bucket);
    var to = Math.min(points.length, Math.floor((i + 1) * bucket));
    var sum = 0;
    var n = 0;
    var k = 0;
    for (k = from; k < to; k++) {
      var v = points[k];
      if (v == null || Number.isNaN(v)) continue;
      sum += v;
      n++;
    }
    out.push(n === 0 ? null : sum / n);
  }
  return out;
}

/** One point per ~5px of the 770px plot: smooth to the eye, still shaped. */
export const PLOT_POINTS = 150;

/**
 * WHAT A HOVERED POINT IS, said in the caller's own numbering.
 *
 * Everything here is RAW: the index is an index into the array the caller
 * passed to the chart, and the total is that array's length. A label is
 * therefore `points[rawIndex]`, or `(rawTotal - 1 - rawIndex) * stepSeconds`
 * seconds ago — both correct, with no knowledge of how many points were
 * actually drawn.
 *
 * The field names are the contract. An `(i, total)` pair cannot say which of
 * the two spaces it is in, and that ambiguity is precisely what went wrong;
 * `rawIndex` can only be read one way.
 */
export type XTick = {
  /**
   * Index into the series AS THE CALLER PASSED IT.
   *
   * When several raw samples were averaged into the drawn point, this is the
   * LAST of them — the instant the bucket ends. That is the honest stamp for
   * a mean over an interval (the curve says "this was the rate up to here"),
   * and it is also what keeps the newest drawn point landing exactly on the
   * newest raw sample, so the tooltip still says "now" at the right edge.
   */
  rawIndex: number;
  /** Length of the series as the caller passed it, before any resampling. */
  rawTotal: number;
  /**
   * Is this the newest sample — the right edge of the chart.
   *
   * Offered as a flag because every caller wants it and every caller used to
   * spell it `i === n - 1`, an index comparison that silently kept working
   * while the index it compared was in the wrong space. Nothing is gained by
   * making each of them write it again.
   */
  newest: boolean;
};

/**
 * Translate a position in DRAWN space into the raw tick it stands for.
 *
 * `drawn` is how many points the chart actually plotted, `rawTotal` the
 * length of the longest series it was given. The bucket arithmetic is
 * `resampleMean`'s, deliberately duplicated as the inverse rather than
 * derived: if one of the two ever changes, the test in this folder fails on
 * the pair, which is the only way to notice.
 *
 * Series of unequal length share one mapping, the longest one's. A chart
 * mixing a 1441-point series with a 40-point one already drew them over a
 * common axis before this existed; the label names the long one, which is
 * the series that set the axis.
 */
export function xTickAt(drawnIndex: number, drawn: number, rawTotal: number): XTick {
  var total = Math.max(1, rawTotal);
  var raw: number;
  if (total <= drawn) {
    // No resampling happened — `resampleMean` returns its input untouched
    // below the target, so the two spaces are the same space. This is the
    // case under 150 points, which is why the defect looked intermittent.
    raw = drawnIndex;
  } else if (drawnIndex >= drawn - 1) {
    // THE RIGHT EDGE IS THE NEWEST SAMPLE BY DEFINITION, not by arithmetic.
    // `drawn * (total / drawn)` is not exactly `total` in binary floating
    // point — 1441 over 150 comes back a hair under — and a floor of that
    // would name the second-to-last sample, quietly costing every chart its
    // "now" label. Stating the identity is both exact and the contract.
    raw = total - 1;
  } else {
    var bucket = total / drawn;
    raw = Math.floor((drawnIndex + 1) * bucket) - 1;
  }
  if (raw < 0) raw = 0;
  if (raw > total - 1) raw = total - 1;
  return { rawIndex: raw, rawTotal: total, newest: raw === total - 1 };
}
