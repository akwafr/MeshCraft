import {
  buildLinkLabelSpecs,
  resolveLinkLabels,
  type LinkLabelSpec,
} from "./topologyLinkLabels";

/** Rounded label behind topology link text — readable over crossing strokes. */
export function LinkLabelPill({
  x,
  y,
  text,
  fill = "var(--text-dim)",
  fontSize = 9,
  letterSpacing,
  w,
  h,
}: {
  x: number;
  y: number;
  text: string;
  fill?: string;
  fontSize?: number;
  letterSpacing?: number;
  w?: number;
  h?: number;
}) {
  if (!text) return null;
  const padX = 5;
  const padY = 2.5;
  const width = w ?? Math.max(20, text.length * fontSize * 0.58 + padX * 2);
  const height = h ?? fontSize + padY * 2;
  return (
    <g pointerEvents="none">
      <rect
        x={x - width / 2}
        y={y - height / 2}
        width={width}
        height={height}
        rx={height / 2}
        fill="var(--bg-panel)"
        stroke="var(--border)"
        strokeWidth={0.8}
        opacity={0.94}
      />
      <text
        x={x}
        y={y + fontSize * 0.35}
        fill={fill}
        fontSize={fontSize}
        letterSpacing={letterSpacing}
        textAnchor="middle"
      >
        {text}
      </text>
    </g>
  );
}

/** Place port / bandwidth / speed pills on a link segment without collisions. */
export function TopologyLinkLabels({
  x1,
  y1,
  x2,
  y2,
  portA,
  portB,
  speedText,
  speedFill,
  speedLetterSpacing,
  bwA,
  bwB,
  extraText,
}: {
  x1: number;
  y1: number;
  x2: number;
  y2: number;
  portA?: string | null;
  portB?: string | null;
  speedText?: string | null;
  speedFill?: string;
  speedLetterSpacing?: number;
  bwA?: string | null;
  bwB?: string | null;
  extraText?: string | null;
}) {
  const specs: LinkLabelSpec[] = buildLinkLabelSpecs({
    portA,
    portB,
    speedText,
    speedFill,
    speedLetterSpacing,
    bwA,
    bwB,
    extraText,
  });
  const placed = resolveLinkLabels(x1, y1, x2, y2, specs);
  return (
    <>
      {placed.map((p, i) => (
        <LinkLabelPill
          key={`${p.layer}-${i}-${p.text}`}
          x={p.x}
          y={p.y}
          text={p.text}
          fill={p.fill}
          fontSize={p.fontSize}
          letterSpacing={p.letterSpacing}
          w={p.w}
          h={p.h}
        />
      ))}
    </>
  );
}
