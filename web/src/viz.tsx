// Shared visualization building blocks (Dashboard, Monitoring).
//
// Rules: single Y scale (never dual axis), thin constant-width strokes
// (vector-effect), discrete grid, legend when two or more series, hover
// crosshair + tooltip, values in text ink never series color.
import { useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { QuickTip } from "./components";

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
      <div className="tile-value" style={accent ? { color: accent } : undefined}>
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
  points: number[];
}

const VB_W = 600;
const VB_H = 150;
const PAD_L = 4;
const PAD_B = 14;
const PAD_T = 4;

/**
 * Rate curves. `format` drives axis labels and tooltip; `xLabel` names a point
 * (e.g. "2 min ago").
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
}: {
  series: Series[];
  format: (v: number) => string;
  xLabel?: (i: number, total: number) => string;
  height?: number;
}) {
  const ref = useRef<HTMLDivElement>(null);
  const [hover, setHover] = useState<{ i: number; x: number } | null>(null);

  const n = Math.max(...series.map((s) => s.points.length), 2);
  const all = series.flatMap((s) => s.points);
  const bipolar = all.some((v) => v < 0);
  const rawPeak = Math.max(...all.map((v) => Math.abs(v)), 1);
  const mag = Math.pow(10, Math.floor(Math.log10(rawPeak)));
  const max = Math.ceil(rawPeak / mag) * mag;
  const min = bipolar ? -max : 0;
  const span = max - min || 1;
  const plotTop = bipolar ? PAD_T : 0;
  const plotH = VB_H - PAD_B - plotTop;

  const px = (i: number) => PAD_L + (i * (VB_W - PAD_L * 2)) / (n - 1);
  const py = (v: number) => plotTop + plotH * (1 - (v - min) / span);

  const grid = bipolar ? [-1, -0.5, 0, 0.5, 1] : [0, 0.5, 1];

  const onMove = (e: React.MouseEvent) => {
    const box = ref.current?.getBoundingClientRect();
    if (!box) return;
    const ratio = Math.min(1, Math.max(0, (e.clientX - box.left) / box.width));
    const i = Math.round(ratio * (n - 1));
    setHover({ i, x: ratio * box.width });
  };

  return (
    <div
      className={"chart" + (bipolar ? " chart-bipolar" : "")}
      ref={ref}
      style={{ height }}
      onMouseMove={onMove}
      onMouseLeave={() => setHover(null)}
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
        {series.map((s) => (
          <polyline
            key={s.name}
            points={s.points.map((v, i) => `${px(i)},${py(v)}`).join(" ")}
            fill="none"
            stroke={s.color}
            strokeWidth={2}
            strokeLinejoin="round"
            vectorEffect="non-scaling-stroke"
          />
        ))}
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
            {series.map((s) => {
              const v = s.points[hover.i];
              if (v == null) return null;
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
      <span className="chart-ymax">{format(max)}</span>
      {bipolar && <span className="chart-ymin">−{format(max)}</span>}
      {hover && (
        <div
          className="chart-tip"
          style={{
            left: Math.min(Math.max(hover.x, 8), (ref.current?.clientWidth ?? 300) - 150),
          }}
        >
          {xLabel && <div className="chart-tip-x">{xLabel(hover.i, n)}</div>}
          {series.map((s) => (
            <div key={s.name} className="chart-tip-row">
              <span className="dot" style={{ background: s.color }} />
              <span>{s.name}</span>
              <b>{format(Math.abs(s.points[hover.i] ?? 0))}</b>
            </div>
          ))}
        </div>
      )}
      <div className="chart-legend">
        {series.map((s) => (
          <span key={s.name}>
            <i style={{ background: s.color }} />
            {s.name}
          </span>
        ))}
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
