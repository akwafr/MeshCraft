// Shared visualization building blocks (Dashboard, Monitoring).
//
// Rules: single Y scale (never dual axis), thin constant-width strokes
// (vector-effect), discrete grid, legend when two or more series, hover
// crosshair + tooltip, values in text ink never series color.
import { useRef, useState, useId, useEffect, useMemo } from "react";
import { useTranslation } from "react-i18next";
import { QuickTip } from "./components";
import { claimHoverTip, registerHoverTip, releaseHoverTip } from "./hoverExclusive";
import { PLOT_POINTS, resampleMean, xTickAt } from "./chartTicks";
import type { XTick } from "./chartTicks";
import { trafficRampStops } from "./trafficRamp";

/** Series colors validated (CVD separation checked). */
export const SERIES = {
  in: "#3987e5",
  out: "#d95926",
  third: "#199e70",
};

export function StatTile({
  label,
  value,
  sub,
  dotColor,
  accent,
}: {
  label: string;
  value: string;
  sub?: string;
  dotColor?: string;
  accent?: string;
}) {
  return (
    <div className="tile">
      <div className="tile-label">{label}</div>
      {/* A tile's figure is set in a tabular mono, which is right for
          "289 Mb/s" and wrong for "no measurement": prose in a display mono
          reads as a code dump, and at that size it wrapped out of the tile.
          A value with no digit in it is not a figure. */}
      <div
        className={"tile-value" + (/\d/.test(value) ? "" : " tile-value--prose")}
        style={accent ? { color: accent } : undefined}
      >
        {dotColor && <span className="dot" style={{ background: dotColor }} />}
        {value}
      </div>
      {sub && <div className="tile-sub">{sub}</div>}
    </div>
  );
}

export interface Series {
  name: string;
  color: string;
  /** null = no sample at this slot — the stroke breaks (empty area). */
  points: (number | null)[];
}

/**
 * MONOTONE CUBIC TANGENTS (Fritsch–Carlson).
 *
 * The smoothing has one hard requirement that rules out the easy curves: it
 * must never overshoot. A Catmull-Rom or a plain cardinal spline through
 * noisy samples bulges past them between knots, and on a RATE chart that
 * bulge is a lie — it draws a throughput the site never reached, and below
 * zero it draws a negative one. Fritsch–Carlson clamps the tangents so the
 * interpolant stays monotone on every interval: the curve is bounded by the
 * two samples it joins, always.
 *
 * So the line is only eased, never invented. A peak stays exactly as tall as
 * it measured, and a return to zero touches zero.
 */
function monotoneTangents(ys: number[]): number[] {
  var n = ys.length;
  if (n < 2) return [0];
  var d: number[] = []; // secant slope per unit index
  var i = 0;
  for (i = 0; i < n - 1; i++) d.push(ys[i + 1] - ys[i]);
  var m: number[] = new Array(n);
  m[0] = d[0];
  m[n - 1] = d[n - 2];
  for (i = 1; i < n - 1; i++) {
    // A local extremum gets a flat tangent — that is what stops the curve
    // from sailing past a peak.
    if (d[i - 1] * d[i] <= 0) m[i] = 0;
    else m[i] = (d[i - 1] + d[i]) / 2;
  }
  for (i = 0; i < n - 1; i++) {
    if (d[i] === 0) {
      m[i] = 0;
      m[i + 1] = 0;
      continue;
    }
    var a = m[i] / d[i];
    var b = m[i + 1] / d[i];
    var s = a * a + b * b;
    if (s > 9) {
      var tau = 3 / Math.sqrt(s);
      m[i] = tau * a * d[i];
      m[i + 1] = tau * b * d[i];
    }
  }
  return m;
}

type Seg = { line: string; area: string };

/**
 * One SVG path per contiguous run of samples. A null breaks the run rather
 * than being bridged: a gap in the series is a gap in what was measured, and
 * drawing through it would invent the measurement.
 *
 * `area` closes the same curve onto the zero line, so a bipolar chart fills
 * upward for incoming and downward for outgoing from the shared axis.
 */
function smoothSegments(
  points: (number | null)[],
  px: (i: number) => number,
  py: (v: number) => number,
): Seg[] {
  var segs: Seg[] = [];
  var runIdx: number[] = [];
  var runVal: number[] = [];
  var i = 0;

  function flush() {
    if (runVal.length === 0) return;
    if (runVal.length === 1) {
      runIdx = [];
      runVal = [];
      return;
    }
    var m = monotoneTangents(runVal);
    var dx = px(1) - px(0);
    var line = "M" + px(runIdx[0]) + "," + py(runVal[0]);
    var k = 0;
    for (k = 0; k < runVal.length - 1; k++) {
      var x0 = px(runIdx[k]);
      var x1 = px(runIdx[k + 1]);
      // Control points a third of the way along, offset by the tangent —
      // the standard cubic Hermite → Bézier conversion.
      line +=
        " C" + (x0 + dx / 3) + "," + py(runVal[k] + m[k] / 3) +
        " " + (x1 - dx / 3) + "," + py(runVal[k + 1] - m[k + 1] / 3) +
        " " + x1 + "," + py(runVal[k + 1]);
    }
    var base = py(0);
    var area =
      line +
      " L" + px(runIdx[runIdx.length - 1]) + "," + base +
      " L" + px(runIdx[0]) + "," + base +
      " Z";
    segs.push({ line: line, area: area });
    runIdx = [];
    runVal = [];
  }

  for (i = 0; i < points.length; i++) {
    var v = points[i];
    if (v == null || Number.isNaN(v)) {
      flush();
      continue;
    }
    runIdx.push(i);
    runVal.push(v);
  }
  flush();
  return segs;
}

const VB_W = 600;
const VB_H = 150;
const PAD_L = 4;
const PAD_B = 14;
const PAD_T = 4;

/**
 * RUNNING TOTALS, for a stacked chart — band k's top edge is the sum of bands
 * 0..k, so the topmost curve IS the total and the gap between two curves is
 * one band's own contribution.
 *
 * A NULL ANYWHERE BELOW BREAKS EVERY BAND ABOVE IT, and that is the whole
 * reason this is a function and not a `reduce`. If one class is missing a
 * sample at a slot, the total at that slot is not known — and a stack whose
 * top edge is drawn anyway would quietly show the sum of the classes that DID
 * report as if it were the sum of all of them. The reader has no way to tell:
 * the curve looks like a measurement. So the hole propagates upward, the
 * stroke breaks, and the gap reads as a gap (same rule as `smoothSegments`,
 * one dimension over).
 *
 * In practice the classes of one port come from one poll cycle and go null
 * together, so this is rarely visible — which is exactly why it has to be
 * right by construction rather than by luck.
 *
 * Negative samples are floored at zero. A rate cannot be negative; `rate()`
 * does not produce one and the handler's round3 already clamps what a
 * misread counter reset might. But a stray negative in a RUNNING TOTAL does
 * not merely draw one band wrong, it drags every band above it down and the
 * total with them, so the floor is kept here too.
 */
function runningTotals(series: Series[]): Series[] {
  var out: Series[] = [];
  var n = Math.max(...series.map((s) => s.points.length), 0);
  var cum: (number | null)[] = new Array(n).fill(0);
  var k = 0;
  for (k = 0; k < series.length; k++) {
    var next: (number | null)[] = new Array(n);
    var i = 0;
    for (i = 0; i < n; i++) {
      var below = cum[i];
      var v = series[k].points[i];
      if (below == null || v == null || Number.isNaN(v)) next[i] = null;
      else next[i] = below + Math.max(0, v);
    }
    out.push({ ...series[k], points: next });
    cum = next;
  }
  return out;
}

/**
 * Rate curves. `format` drives axis labels and tooltip; `xLabel` names a point
 * (e.g. "2 min ago") from the `XTick` it is handed — see `chartTicks.ts` for
 * why that is an object and not an index pair.
 *
 * When any series has negative samples (e.g. download +, upload −), the chart
 * is bipolar: zero sits in the middle, positive up / negative down, same
 * absolute scale both ways — so the two directions are not superimposed.
 */
export function LineChart({
  series,
  format,
  xLabel,
  height = 170,
  ramp,
  stack,
  totalLabel,
  rampCapacity,
}: {
  series: Series[];
  format: (v: number) => string;
  /**
   * Name the hovered point. The chart resamples before drawing, so the only
   * index a caller can safely reason about is the RAW one `XTick` carries —
   * `at.rawIndex` indexes the array passed in `series`, whatever the plot had
   * room for.
   */
  xLabel?: (at: XTick) => string;
  height?: number;
  /**
   * Colour the curves by the TRAFFIC RAMP instead of by series identity.
   *
   * On a rate chart the series colour is nearly redundant — the legend and
   * the side of the axis already say which direction is which — while the
   * LEVEL is the thing being read and nothing encodes it. With the ramp on,
   * height and colour say the same thing twice, which is what makes a busy
   * hour legible at a glance instead of requiring the axis to be read.
   *
   * There is no port line rate here: a site's cumulative throughput has no
   * single capacity. The axis ceiling stands in for it, so the ramp spans
   * exactly the range drawn — "hot" means "the top of what this site has
   * done in this window", which is the honest claim available.
   */
  ramp?: boolean;
  /**
   * STACK the series instead of superimposing them: each one sits on top of
   * the sum of those before it, so the topmost edge is the TOTAL.
   *
   * This is the first stacked chart in the product, and the two places that
   * deliberately refused to stack are not reversed by it — they were right.
   * The dashboard's traffic tile feeds outgoing NEGATIVE to mirror it under
   * the axis, and a port row sums rx+tx into one curve; in both cases the
   * series are DIRECTIONS, and in+out has no physical meaning as a sum (a
   * port doing 100 in and 100 out is not doing 200 of anything).
   *
   * Here they are DESTINATION CLASSES of one direction, and the sum is the
   * real quantity: multicast plus broadcast plus unicast is every frame the
   * port carried. Stacking is what makes the proportion readable — the
   * question being asked of this chart is "how much of this port's traffic is
   * flooding", which superimposed curves force the reader to subtract by eye.
   *
   * Requires non-negative samples; see `runningTotals` for what it does with
   * a negative and with a hole.
   */
  stack?: boolean;
  /**
   * Name for the total row the tooltip adds under the bands in `stack` mode.
   * Omitted, the tooltip lists the bands alone.
   */
  totalLabel?: string;
  /**
   * The rate the ramp is laid out over, in Mb/s. Absent = the axis ceiling,
   * which is the site-traffic case `ramp` was written for and is explained
   * just above.
   *
   * Pass it whenever the curve HAS a line rate, and ADR-0003 then applies in
   * full: the colour is read against the port's negotiated speed rather than
   * against its own peak, so a quiet gigabit port stays blue instead of being
   * stretched to lime because a megabit happened to be its maximum — and a 100
   * Mbit/s link carrying 80 reads as saturated, which is the whole point.
   * `Sparkline` has always worked this way; this is the same gradient, over a
   * chart with axes.
   */
  rampCapacity?: number;
}) {
  const rampId = useId().replace(/:/g, "");
  const ref = useRef<HTMLDivElement>(null);
  const [hover, setHover] = useState<{ i: number; x: number } | null>(null);
  const tipId = useId();

  useEffect(() => {
    return registerHoverTip(tipId, () => setHover(null));
  }, [tipId]);

  // Aggregated ONCE, up front: the axis maximum, the curve and the hover
  // readout must all describe the same numbers. Computing the scale from the
  // raw samples and the curve from the aggregate would draw a chart that
  // never reaches its own ceiling.
  // `bands` is what the TOOLTIP reads — each class's own value. `plotted` is
  // what gets DRAWN, which in stack mode is the running totals. Both are
  // resampled, and resampling before accumulating is safe because the mean is
  // linear: the mean of a sum is the sum of the means, over the same buckets.
  const bands = useMemo(
    () => series.map((s) => ({ ...s, points: resampleMean(s.points, PLOT_POINTS) })),
    [series],
  );
  const plotted = useMemo(() => (stack ? runningTotals(bands) : bands), [bands, stack]);
  const n = Math.max(...plotted.map((s) => s.points.length), 2);
  // The length BEFORE resampling, which is the only numbering a caller's
  // timestamps and sample step are expressed in. `n` is how many points there
  // was room to draw; the two differ by a factor of nine on a 24 h window, and
  // handing `n` to a label is the defect `chartTicks.ts` exists to prevent.
  const rawTotal = Math.max(...series.map((s) => s.points.length), 2);
  const all = plotted.flatMap((s) => s.points).filter((v): v is number => v != null && !Number.isNaN(v));
  const bipolar = all.some((v) => v < 0);
  const rawPeak = Math.max(...all.map((v) => Math.abs(v)), 1);
  const mag = Math.pow(10, Math.floor(Math.log10(rawPeak)));
  const max = Math.ceil(rawPeak / mag) * mag;
  const min = bipolar ? -max : 0;
  const span = max - min || 1;
  const plotTop = bipolar ? PAD_T : 0;
  const plotH = VB_H - PAD_B - plotTop;
  const empty = all.length === 0;

  const px = (i: number) => PAD_L + (i * (VB_W - PAD_L * 2)) / (n - 1);
  const py = (v: number) => plotTop + plotH * (1 - (v - min) / span);

  const grid = bipolar ? [-1, -0.5, 0, 0.5, 1] : [0, 0.5, 1];

  // What the ramp spans. `userSpaceOnUse` lets this sit ABOVE the plot when the
  // line rate is above the peak drawn — which is the common case on a quiet
  // port, and exactly what keeps its curve in the cold end of the ramp instead
  // of normalising it to its own maximum. A non-positive capacity would divide
  // by zero in the gradient stops, so it falls back to the ceiling.
  const rampSpan = rampCapacity && rampCapacity > 0 ? rampCapacity : max;

  const onMove = (e: React.MouseEvent) => {
    const box = ref.current?.getBoundingClientRect();
    if (!box) return;
    const ratio = Math.min(1, Math.max(0, (e.clientX - box.left) / box.width));
    const i = Math.round(ratio * (n - 1));
    claimHoverTip(tipId);
    setHover({ i, x: ratio * box.width });
  };

  return (
    /**
     * TWO BOXES, AND THE OUTER ONE IS WHY.
     *
     * `.chart` is a FIXED-HEIGHT box — `height` is the plot, and the axis
     * labels are absolutely positioned against it, so it cannot be allowed to
     * grow. The legend used to render inside it anyway, in flow under an SVG
     * that already filled it: every chart on every page therefore overflowed
     * its own box by the height of its legend, and painted those words over
     * whatever followed. Two call sites carry a hand-measured top margin to
     * clear it (`.lowbw-behind`, PortHoverTip's optical chart) and name this
     * bug in their comments; on a stacked dashboard, where the card ends at
     * the chart, there was nothing to clear into and the legend was simply
     * cut in half.
     *
     * The legend moves OUT of the fixed box and into a wrapper. The plot keeps
     * its exact geometry — same height, same anchors for `.chart-ymax` and
     * `.chart-ymin` — and the block a caller lays out is now as tall as what
     * it draws.
     */
    <div className={"chart-block" + (bipolar ? " chart-bipolar" : "")}>
      <div
        className="chart"
        ref={ref}
        style={{ height }}
        onMouseMove={onMove}
        onMouseLeave={() => {
          setHover(null);
          releaseHoverTip(tipId);
        }}
      >
        <svg viewBox={`0 0 ${VB_W} ${VB_H}`} preserveAspectRatio="none" width="100%" height="100%">
          {grid.map((f) => (
            <line
              key={f}
              x1={0}
              x2={VB_W}
              y1={py(max * f)}
              y2={py(max * f)}
              stroke="var(--border)"
              strokeWidth={f === 0 && bipolar ? 1.4 : 1}
              opacity={f === 0 && bipolar ? 1 : undefined}
              vectorEffect="non-scaling-stroke"
            />
          ))}
          {ramp && (
            <defs>
              {/* One gradient per DIRECTION: the mirrored half runs the ramp
                  downward from the same zero line, so both halves read hot at
                  their extremes rather than one of them inverting. */}
              <TrafficGradient id={"up" + rampId} py={py} capacity={rampSpan} sign={1} />
              {bipolar && (
                <TrafficGradient id={"dn" + rampId} py={py} capacity={rampSpan} sign={-1} />
              )}
            </defs>
          )}
          {/* Areas first, then strokes: every fill sits behind every line, so
              where incoming and outgoing overlap near the axis the two strokes
              stay readable instead of one tinting the other.

              IN STACK MODE THE ORDER REVERSES AND THE FILL TURNS OPAQUE, which
              is what turns running totals into bands. Every area still closes
              onto zero — `smoothSegments` is untouched — so painting the
              LARGEST cumulative first and letting each smaller one cover it
              leaves exactly the slice between two curves showing. That reuses
              the monotone smoothing already trusted everywhere else instead of
              introducing a second path builder that closes onto a curve, which
              would have to re-derive the same tangents to stay aligned with
              the band below it. */}
          {!empty &&
            (stack ? [...plotted].reverse() : plotted).map((s) => {
              const neg = (s.points.find((v) => v != null && !Number.isNaN(v)) ?? 0) < 0;
              const paint = ramp
                ? `url(#${neg && bipolar ? "dn" : "up"}${rampId})`
                : s.color;
              return smoothSegments(s.points, px, py).map((seg, si) => (
                <path
                  key={s.name + "-a" + si}
                  d={seg.area}
                  fill={paint}
                  opacity={stack ? 0.62 : 0.13}
                  stroke="none"
                />
              ));
            })}
          {!empty &&
            plotted.map((s) => {
              const neg = (s.points.find((v) => v != null && !Number.isNaN(v)) ?? 0) < 0;
              const paint = ramp
                ? `url(#${neg && bipolar ? "dn" : "up"}${rampId})`
                : s.color;
              return smoothSegments(s.points, px, py).map((seg, si) => (
                <path
                  key={s.name + "-l" + si}
                  d={seg.line}
                  fill="none"
                  stroke={paint}
                  strokeWidth={2}
                  strokeLinejoin="round"
                  strokeLinecap="round"
                  vectorEffect="non-scaling-stroke"
                />
              ));
            })}
          {hover && (
            <>
              <line
                x1={px(hover.i)}
                x2={px(hover.i)}
                y1={plotTop}
                y2={VB_H - PAD_B}
                stroke="var(--text-dim)"
                strokeWidth={1}
                vectorEffect="non-scaling-stroke"
              />
              {plotted.map((s) => {
                const v = s.points[hover.i];
                if (v == null || Number.isNaN(v)) return null;
                return (
                  <circle
                    key={s.name}
                    cx={px(hover.i)}
                    cy={py(v)}
                    r={3}
                    fill={s.color}
                    stroke="var(--bg-panel)"
                    strokeWidth={2}
                    vectorEffect="non-scaling-stroke"
                  />
                );
              })}
            </>
          )}
        </svg>
      {/* THE BOTTOM HALF IS NOT A NEGATIVE SCALE.
          The lower series is fed as negative numbers because that is how the
          geometry puts it under the axis, but nobody measures −300 Mb/s. The
          axis therefore reads the same magnitude both ways and says DIRECTION
          with an arrow instead of a sign — the same ↓/↑ the device cards and
          the port tooltips already use for in and out. */}
        <span className="chart-ymax">{bipolar ? "↓ " : ""}{format(max)}</span>
        {bipolar && <span className="chart-ymin">↑ {format(max)}</span>}
        {hover && (
          <div
            className="chart-tip"
            style={{
              left: Math.min(Math.max(hover.x, 8), (ref.current?.clientWidth ?? 300) - 150),
            }}
          >
            {xLabel && (
              <div className="chart-tip-x">{xLabel(xTickAt(hover.i, n, rawTotal))}</div>
            )}
            {/* `bands`, not `plotted`: in stack mode the drawn series are
                running totals, and a tooltip reading them out would say
                "broadcast 1 200 pkt/s" for a port doing 200 of broadcast on
                top of 1 000 of multicast. The reader is asking what each
                class contributes. */}
            {bands.map((s) => {
              const v = s.points[hover.i];
              return (
                <div key={s.name} className="chart-tip-row">
                  <span className="dot" style={{ background: s.color }} />
                  <span>{s.name}</span>
                  <b>{v == null || Number.isNaN(v) ? "—" : format(Math.abs(v))}</b>
                </div>
              );
            })}
            {/* The total is the one number the stack's geometry already shows
                and the rows do not. It is read off the TOP running total
                rather than re-summed here, so it is the same value the top
                edge was drawn at — including its holes: one class missing a
                sample makes the total unknown, and it says so. */}
            {stack && totalLabel && plotted.length > 1 && (
              <div className="chart-tip-row chart-tip-total">
                <span className="dot dot-hollow" />
                <span>{totalLabel}</span>
                <b>
                  {(() => {
                    const t = plotted[plotted.length - 1].points[hover.i];
                    return t == null || Number.isNaN(t) ? "—" : format(t);
                  })()}
                </b>
              </div>
            )}
          </div>
        )}
      </div>
      <div className="chart-legend">
        {series.map((s) => {
          // WITH THE RAMP ON, A COLOURED SWATCH IS A LIE. The curve is no
          // longer painted in the series colour — it runs cold to hot with
          // the level — so a chip of the old blue beside "Incoming" names a
          // colour that appears nowhere on the chart. The direction arrow
          // says the same thing truthfully, and it is the one the axis
          // labels already use.
          const neg = (s.points.find((v) => v != null && !Number.isNaN(v)) ?? 0) < 0;
          return (
            <span key={s.name}>
              {ramp ? (
                <i className="chart-legend-dir" aria-hidden>
                  {neg && bipolar ? "↑" : "↓"}
                </i>
              ) : (
                <i style={{ background: s.color }} />
              )}
              {s.name}
            </span>
          );
        })}
      </div>
    </div>
  );
}

/** Horizontal bars: one metric, identity from swatch + name. */
export function BarList({
  rows,
  format,
}: {
  rows: { key: string; label: string; sub?: string; value: number; color?: string }[];
  format: (v: number) => string;
}) {
  const { t } = useTranslation();
  const max = Math.max(...rows.map((r) => r.value), 1);
  if (rows.length === 0) return <div className="empty">{t("common.noData")}</div>;
  return (
    <div className="barlist">
      {rows.map((r) => (
        <QuickTip
          key={r.key}
          text={`${r.label} — ${format(r.value)}`}
          className="quicktip-block"
        >
          <div className="barlist-row">
            <span className="barlist-label">
              {r.color && <span className="vlan-chip" style={{ background: r.color, margin: 0 }} />}
              <span className="barlist-name">{r.label}</span>
              {r.sub && <span className="note">{r.sub}</span>}
            </span>
            <span className="barlist-track">
              <span
                className="barlist-fill"
                style={{
                  width: Math.max(2, (r.value / max) * 100) + "%",
                  background: r.color ?? SERIES.in,
                }}
              />
            </span>
            <span className="barlist-value">{format(r.value)}</span>
          </div>
        </QuickTip>
      ))}
    </div>
  );
}

/* ======================================================================
 * THE TRAFFIC GRADIENT, and the sparkline that wears it
 * ====================================================================== */

/**
 * A vertical gradient carrying the traffic ramp, laid out over the port's
 * LINE RATE rather than over the drawn range.
 *
 * That distinction is the whole feature. `gradientUnits="userSpaceOnUse"`
 * anchors the colour to the capacity axis, so a curve that only ever reaches
 * 5 % of a gigabit stays entirely blue instead of being stretched to lime
 * because 5 % happened to be its own maximum. Two ports side by side are
 * then comparable at a glance, which a per-chart normalisation can never be.
 *
 * The stops come from `trafficRampStops`, which builds them out of
 * `trafficColorAt` — the SAME function a topology link half is painted with
 * (ADR-0026, ADR-0030). This gradient therefore SAMPLES the ramp rather than
 * re-deriving it: a link and the curve of its own port at the same rate can no
 * longer disagree about which ramp they consulted, only about how finely this
 * samples it (measured at ΔE ≤ 0.17, down from 1.60 when there were six stops
 * and SVG interpolated a whole band at a time).
 *
 * The offsets are in RATE space, because the y axis is linear in rate while
 * the ramp is logarithmic in it. Spacing them evenly would silently linearise
 * the scale.
 */
function TrafficGradient({
  id,
  py,
  capacity,
  sign = 1,
}: {
  id: string;
  py: (v: number) => number;
  capacity: number;
  sign?: 1 | -1;
}) {
  return (
    <linearGradient
      id={id}
      gradientUnits="userSpaceOnUse"
      x1={0}
      y1={py(0)}
      x2={0}
      y2={py(sign * capacity)}
    >
      {trafficRampStops(capacity).map((s, i) => (
        <stop key={i} offset={s.offset} stopColor={s.color} />
      ))}
    </linearGradient>
  );
}

/**
 * A rate curve small enough to live in a table row.
 *
 * No axes, no grid, no hover: the row it sits in already carries the number
 * and the name. It answers one question — what has this link been doing —
 * and it answers it in the time it takes to run an eye down a column, which
 * is the only reason to put a chart in a table at all.
 */
export function Sparkline({
  points,
  capacity,
  width = 104,
  height = 26,
}: {
  points: (number | null)[];
  /** Line rate in Mb/s. The gradient is laid out over THIS, not over the peak. */
  capacity: number;
  width?: number;
  height?: number;
}) {
  const gid = useId().replace(/:/g, "");
  const plotted = useMemo(() => resampleMean(points, 48), [points]);
  const n = Math.max(plotted.length, 2);
  const real = plotted.filter((v): v is number => v != null && !Number.isNaN(v));
  if (real.length === 0) return <span className="spark-empty">—</span>;

  // The curve is drawn against ITS OWN peak so a quiet link still shows its
  // shape; the COLOUR stays absolute against the line rate. Height says
  // "what did this link do", colour says "how much of the pipe was that" —
  // two questions, two channels, and neither one crowds the other out.
  //
  // There is deliberately NO floor tied to capacity. A first attempt used
  // `max(real, capacity * 0.02)`, which flattened every access port against
  // the bottom of its box: a megabit on a gigabit link is 0.1 % of that
  // floor, so the shape — the thing the sparkline exists to show — was gone,
  // and the colour was already saying the link was quiet anyway.
  const peak = Math.max(...real) || 1;
  const px = (i: number) => (i * (width - 2)) / (n - 1) + 1;
  const py = (v: number) => height - 1 - ((v / peak) * (height - 3));
  const segs = smoothSegments(plotted, px, py);

  return (
    <svg
      className="spark"
      width={width}
      height={height}
      viewBox={`0 0 ${width} ${height}`}
      aria-hidden
    >
      <defs>
        {/* The gradient spans the capacity, but the curve is scaled to its
            own peak, so the gradient must be mapped through the SAME scale
            the curve uses — hence py, not a second one. */}
        <TrafficGradient id={"g" + gid} py={py} capacity={capacity} />
      </defs>
      {segs.map((s, i) => (
        <path key={"a" + i} d={s.area} fill={`url(#g${gid})`} opacity={0.16} stroke="none" />
      ))}
      {segs.map((s, i) => (
        <path
          key={"l" + i}
          d={s.line}
          fill="none"
          stroke={`url(#g${gid})`}
          strokeWidth={1.5}
          strokeLinejoin="round"
          strokeLinecap="round"
        />
      ))}
    </svg>
  );
}
