import { useTranslation } from "react-i18next";
import { decimalSeparator } from "./network";
import type { PortLevelLine } from "./telemetry";
import { LineChart, SERIES } from "./viz";

/**
 * The optical level history of ONE port: receive and transmit power in dBm,
 * one line per direction and per lane.
 *
 * Shared between the port panel's Optics section and the port action overlay,
 * because the two must never disagree about the same fibre — one component,
 * one colour rule (RX blue like incoming traffic, TX orange like outgoing),
 * one axis wording.
 *
 * Renders NOTHING when there is no history: the caller decides what the
 * absence means on its screen (the panel says why, the overlay simply omits
 * the section). A chart of empty axes would read as "collection broke".
 */
export function LevelChart({
  lines,
  stepSeconds,
}: {
  lines: PortLevelLine[];
  stepSeconds: number;
}) {
  const { t } = useTranslation();
  const dsep = decimalSeparator();
  if (lines.length === 0) return null;
  // Lane suffixes only when a second lane exists: "RX" on every SFP, and
  // "RX 0"‥"RX 3" the day a QSFP shows up. "" and "0" are the same single-lane
  // case — the CLI producers label with neither.
  const multiLane = new Set(lines.map((l) => l.lane || "0")).size > 1;
  const name = (l: PortLevelLine) =>
    (l.dir === "rx"
      ? t("switchDetailPage.opticsChartRx")
      : t("switchDetailPage.opticsChartTx")) +
    (multiLane ? " " + (l.lane || "0") : "");
  return (
    <LineChart
      height={132}
      series={lines.map((l) => ({
        name: name(l),
        color: l.dir === "rx" ? SERIES.in : SERIES.out,
        points: l.points,
      }))}
      format={(v) => v.toFixed(1).replace(".", dsep) + " dBm"}
      xLabel={({ rawIndex, rawTotal, newest }) => {
        // `stepSeconds` is the RAW sample interval, so the age must be counted
        // in raw indices — `XTick` carries no other kind.
        if (newest) return t("switchDetailPage.chartNow");
        const back = (rawTotal - 1 - rawIndex) * stepSeconds;
        if (back < 90) return t("switchDetailPage.chartSecondsAgo", { n: Math.round(back) });
        if (back < 5400) return t("switchDetailPage.chartMinutesAgo", { n: Math.round(back / 60) });
        if (back < 48 * 3600) return t("switchDetailPage.chartHoursAgo", { n: Math.round(back / 3600) });
        return t("switchDetailPage.chartDaysAgo", { n: Math.round(back / 86400) });
      }}
    />
  );
}
