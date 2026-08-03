// Floating 24 h bandwidth chart shown while hovering a topology link.
// Reuses device telemetry + LineChart — no per-link API.
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";

import { get } from "./api";
import { fmtMbps } from "./network";
import {
  portTraffic,
  type DeviceTelemetry,
} from "./telemetry";
import { LineChart, SERIES } from "./viz";

const WINDOW_MINUTES = 1440;

export type LinkTrafficHover = {
  /** Stable id so move updates do not remount the query. */
  key: string;
  deviceId: string;
  port: string;
  /** Which end owns the counters — maps rx/tx onto "toward A/B". */
  from: "A" | "B";
  nameA: string;
  nameB: string;
  capacity?: string;
  clientX: number;
  clientY: number;
};

export function LinkTrafficTip({ hover }: { hover: LinkTrafficHover | null }) {
  const { t } = useTranslation();
  const { data: tele, isFetching, isError } = useQuery({
    queryKey: ["telemetry", hover?.deviceId ?? "", WINDOW_MINUTES],
    queryFn: () =>
      get<DeviceTelemetry>(
        `/api/devices/${hover!.deviceId}/telemetry?minutes=${WINDOW_MINUTES}`,
      ),
    enabled: !!hover?.deviceId,
    staleTime: 60_000,
  });

  if (!hover) return null;

  const traffic = portTraffic(tele, hover.port);
  const towardA = hover.from === "A" ? traffic.rx : traffic.tx;
  const towardB = hover.from === "A" ? traffic.tx : traffic.rx;
  const step = tele?.step_seconds ?? 0;
  const hasSeries =
    towardA.some((v) => v != null) || towardB.some((v) => v != null);

  const pad = 14;
  const tipW = 320;
  const tipH = 210;
  const left = Math.min(
    Math.max(8, hover.clientX + pad),
    window.innerWidth - tipW - 8,
  );
  const top = Math.min(
    Math.max(8, hover.clientY + pad),
    window.innerHeight - tipH - 8,
  );

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
              name: t("planPage.towardDevice", { device: hover.nameA }),
              color: SERIES.in,
              points: towardA,
            },
            {
              name: t("planPage.towardDevice", { device: hover.nameB }),
              color: SERIES.out,
              points: towardB,
            },
          ]}
          format={fmtMbps}
          xLabel={(i, n) => {
            if (i === n - 1) return t("switchDetailPage.chartNow");
            const back = (n - 1 - i) * step;
            if (back < 90) return t("switchDetailPage.chartSecondsAgo", { n: Math.round(back) });
            if (back < 5400) return t("switchDetailPage.chartMinutesAgo", { n: Math.round(back / 60) });
            if (back < 48 * 3600) return t("switchDetailPage.chartHoursAgo", { n: Math.round(back / 3600) });
            return t("switchDetailPage.chartDaysAgo", { n: Math.round(back / 86400) });
          }}
        />
      )}
    </div>
  );
}
