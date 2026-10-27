// Floating 24 h chart shown while hovering a topology link: bandwidth, and the
// OPTICAL LEVELS of the end that owns the counters when that end is a fibre.
// Reuses device telemetry + LineChart — no per-link API, and the query is shared
// with the faceplate tip (PortHoverTip), which is why hovering the same switch
// on either page costs one request between them.
import { useTranslation } from "react-i18next";

import { LevelChart } from "./LevelChart";
import { fmtMbps } from "./network";
import {
  tipPosition,
  usePortTelemetry,
  useTipXLabel,
} from "./PortHoverTip";
import { portLevels, portTraffic } from "./telemetry";
import { LineChart, SERIES } from "./viz";

export type LinkTrafficHover = {
  /** Stable id so move updates do not remount the query. */
  key: string;
  deviceId: string;
  port: string;
  /** Which end owns the counters — maps rx/tx onto the egress of A / of B. */
  from: "A" | "B";
  nameA: string;
  nameB: string;
  capacity?: string;
  clientX: number;
  clientY: number;
};

export function LinkTrafficTip({ hover }: { hover: LinkTrafficHover | null }) {
  const { t } = useTranslation();
  // Shared with the faceplate tip (PortHoverTip): same route, same key, so a
  // switch already hovered on one screen costs no request on the other.
  const { data: tele, isFetching, isError } = usePortTelemetry(hover?.deviceId);
  const xLabel = useTipXLabel(tele?.step_seconds ?? 0);

  if (!hover) return null;

  const traffic = portTraffic(tele, hover.port);
  // EGRESS PER END, not "toward X" (ADR-0026). The two numbers are the same
  // two they always were — what leaves A is what travels toward B — but the
  // map now names them by the switch that SENDS, and a tip that named them by
  // the switch that receives would tell the same story backwards on the same
  // screen. `tx` of the end owning the counters is its own egress; that end's
  // `rx` is the other end's egress, minus whatever was lost on the way.
  const outOfA = hover.from === "A" ? traffic.tx : traffic.rx;
  const outOfB = hover.from === "A" ? traffic.rx : traffic.tx;
  // THE LEVELS OF THE END THAT OWNS THE COUNTERS, which is the end this tip is
  // already reporting bandwidth for. A fibre has a level at BOTH ends and they
  // are different numbers — what leaves one is not what reaches the other — so
  // labelling one end's reading with the link's name would be a quiet lie. The
  // subtitle already names whose counters these are; the levels are that same
  // device's.
  const levels = portLevels(tele, hover.port);
  const hasSeries =
    outOfA.some((v) => v != null) || outOfB.some((v) => v != null);
  const { left, top, width } = tipPosition(
    hover.clientX, hover.clientY, levels.length > 0 ? 380 : 210);
  const tipW = width;

  return (
    <div
      className="plan-link-traffic"
      style={{ left, top, width: tipW }}
      aria-hidden
    >
      <div className="plan-link-traffic-head">
        <div className="plan-link-traffic-title">
          {hover.nameA} ↔ {hover.nameB}
        </div>
        <div className="note">
          {t("planPage.linkTraffic24h")}
          {hover.capacity ? ` · ${hover.capacity}` : ""}
          {` · ${t("planPage.countersOn", { device: hover.from === "A" ? hover.nameA : hover.nameB })}`}
        </div>
      </div>
      {!tele && isFetching ? (
        <div className="note">{t("planPage.linkTrafficLoading")}</div>
      ) : isError ? (
        <div className="note">{t("planPage.linkTrafficError")}</div>
      ) : tele && !tele.available ? (
        <div className="note">{tele.why || t("planPage.linkTrafficUnavailable")}</div>
      ) : !hasSeries ? (
        <div className="note">{t("planPage.linkTrafficEmpty")}</div>
      ) : (
        <LineChart
          height={140}
          series={[
            {
              name: t("planPage.egressOf", { device: hover.nameA }),
              color: SERIES.out,
              points: outOfA,
            },
            {
              name: t("planPage.egressOf", { device: hover.nameB }),
              color: SERIES.in,
              points: outOfB,
            },
          ]}
          format={fmtMbps}
          xLabel={xLabel}
        />
      )}
      {levels.length > 0 && (
        <div style={{ marginTop: 24 }}>
          <div className="kicker" style={{ marginBottom: 4 }}>
            {t("switchDetailPage.opticsLevelsTitle")}
          </div>
          <LevelChart lines={levels} stepSeconds={tele?.step_seconds ?? 0} />
        </div>
      )}
    </div>
  );
}
