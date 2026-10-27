// THE FLOATING PORT TIP: what one interface is carrying, and how much light it
// has, without leaving the page you are on.
//
// # Why one component for two pages
//
// The topology draws links and the equipment page draws faceplates, but the
// question a mouse asks by resting on either is the same one — "what is this
// interface doing?" — and the answer must not differ between them. Two tips
// would drift: one would gain the optical chart, the other would keep showing
// bandwidth alone, and an operator would learn that the level graph "only works
// on the topology". So the rendering lives here and the two pages supply the
// port.
//
// # The optical half is CONDITIONAL, and its absence is not drawn
//
// A copper jack has no light, and most ports on most switches are copper. A tip
// that reserved space for a dBm chart on every port would put an empty frame
// under 46 of a 48-port faceplate — the `.info-banner` failure mode, applied to
// a hover. The chart appears only when the series exist, which by construction
// means a cage with a module the collector could read.
//
// # One query, shared
//
// Both tips read `GET /api/devices/{id}/telemetry`, which is per DEVICE and
// already cached by react-query under the same key the device page uses. Hovering
// along a row of ports therefore costs ONE request for the whole switch, not one
// per square, and moving back to a port already visited costs none.
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";

import { get } from "./api";
import type { XTick } from "./chartTicks";
import { LevelChart } from "./LevelChart";
import { fmtMbps } from "./network";
import { PortClassChart } from "./PortClassChart";
import {
  portClasses,
  portLevels,
  portTraffic,
  type DeviceTelemetry,
  type TrafficClassCaps,
} from "./telemetry";
import { LineChart, SERIES } from "./viz";

/** 24 h, the same window the device page opens on. */
export const PORT_TIP_WINDOW_MINUTES = 1440;

/** One configuration line's height, for the placement maths below. */
const FACT_LINE_PX = 17;

/**
 * The per-class section's contributions to the tip height, all measured in a
 * browser at 320 px rather than read off the stylesheet — the stacked chart
 * carries a legend whose height the sheet does not state.
 *
 * `GAP_LINE_PX` is deliberately a wrapped line (two rows) and not a single
 * one: which sentences wrap depends on the locale, and over-stating only
 * raises the tip while under-stating clips it.
 */
const CLASS_KICKER_PX = 22;
const CLASS_CHART_PX = 176;
const GAP_LINE_PX = 40;

/** Where the pointer is, and which port it is resting on. */
export type PortHover = {
  /** Stable id so a mouse MOVE does not remount the query. */
  key: string;
  deviceId: string;
  /** Faceplate port name, as the telemetry route reports it. */
  port: string;
  /** What to write above the charts. */
  title: string;
  subtitle?: string;
  /**
   * WHAT THIS PORT IS CONFIGURED TO CARRY — one short line each, already
   * phrased by `portConfigLines` (network.ts): the VLANs, and the port profile
   * the jack follows.
   *
   * # Why they are repeated here at all
   *
   * `PortSquare` puts the same lines in its own `QuickTip`, and on a faceplate
   * the operator never sees them: both overlays go through `hoverExclusive`,
   * and the floating tip claims the slot a beat after the QuickTip opens, so
   * the bubble is dismissed before it can be read. Verified in a real browser
   * — hovering a square on the inventory leaves exactly one overlay in the
   * DOM, this one. The text tip still serves the screens that draw squares
   * WITHOUT a floating chart (the topology's mini-faceplates, the port table,
   * the profile preview), which is why it keeps its copy rather than losing
   * it; both call the same function, so the two cannot word it differently.
   *
   * Empty or absent renders nothing: a screen that has no VLAN vocabulary to
   * resolve says nothing instead of reserving a blank row.
   */
  facts?: string[];
  /**
   * Which traffic classes this DEVICE's platform can count, straight off the
   * device payload's `traffic_class_caps` (ADR-0028).
   *
   * Carried on the hover rather than fetched here, because the caller already
   * holds the device record and a tip must not open a second request per
   * square — the whole reason this component shares one per-device telemetry
   * query. Absent (an older caller, or a device with no connector resolved)
   * renders the "we cannot tell" sentence, never "the platform says no".
   */
  classCaps?: TrafficClassCaps;
  clientX: number;
  clientY: number;
};

/**
 * One device's telemetry, keyed per DEVICE so a column of port rows on one
 * switch collapses onto a single request.
 *
 * `minutes` is part of the key, not just the URL: the dashboard's port-traffic
 * tile lets its window be set per tile, and two tiles on two windows are two
 * different answers about the same device. Two tiles on the SAME window still
 * share one request, which is the sharing this hook exists for. The default is
 * the tip's 24 h, so every hover call site reads exactly as before.
 */
export function usePortTelemetry(
  deviceId: string | undefined,
  minutes: number = PORT_TIP_WINDOW_MINUTES,
) {
  return useQuery({
    queryKey: ["telemetry", deviceId ?? "", minutes],
    queryFn: () =>
      get<DeviceTelemetry>(`/api/devices/${deviceId}/telemetry?minutes=${minutes}`),
    enabled: !!deviceId,
    staleTime: 60_000,
  });
}

/**
 * Place the tip near the pointer without letting it leave the viewport.
 *
 * The height is passed in because a tip carrying an optical chart is taller
 * than one carrying bandwidth alone, and a fixed guess would let the taller one
 * hang off the bottom of the screen on exactly the ports that have most to say.
 */
export function tipPosition(clientX: number, clientY: number, h: number) {
  const pad = 14;
  const w = 320;
  return {
    left: Math.min(Math.max(8, clientX + pad), window.innerWidth - w - 8),
    top: Math.min(Math.max(8, clientY + pad), window.innerHeight - h - 8),
    width: w,
  };
}

/**
 * Axis labels shared by both charts, so the two read on one time vocabulary.
 *
 * `step` is the interval between RAW samples, which is why the age is computed
 * from `rawIndex` and `rawTotal` and never from where the pointer landed: the
 * chart draws at most PLOT_POINTS of them, and multiplying a drawn index by a
 * raw step is the same mistake in units as reading a map with the wrong scale.
 */
export function useTipXLabel(step: number) {
  const { t } = useTranslation();
  return ({ rawIndex, rawTotal, newest }: XTick) => {
    if (newest) return t("switchDetailPage.chartNow");
    const back = (rawTotal - 1 - rawIndex) * step;
    if (back < 90) return t("switchDetailPage.chartSecondsAgo", { n: Math.round(back) });
    if (back < 5400) return t("switchDetailPage.chartMinutesAgo", { n: Math.round(back / 60) });
    if (back < 48 * 3600) return t("switchDetailPage.chartHoursAgo", { n: Math.round(back / 3600) });
    return t("switchDetailPage.chartDaysAgo", { n: Math.round(back / 86400) });
  };
}

/**
 * The faceplate tip: bandwidth for the port, and its optical levels when the
 * cage has any.
 */
export function PortHoverTip({ hover }: { hover: PortHover | null }) {
  const { t } = useTranslation();
  const { data: tele, isFetching, isError } = usePortTelemetry(hover?.deviceId);
  const xLabel = useTipXLabel(tele?.step_seconds ?? 0);

  if (!hover) return null;

  const traffic = portTraffic(tele, hover.port);
  const levels = portLevels(tele, hover.port);
  const classes = portClasses(tele, hover.port, hover.classCaps);
  const classDrawn = classes.some((b) => b.state === "ok");
  const classGaps = classes.filter((b) => b.state !== "ok").length;
  const hasTraffic =
    traffic.rx.some((v) => v != null) || traffic.tx.some((v) => v != null);
  // The optical chart makes the tip taller; the placement has to know. So do
  // the configuration lines — a trunk contributes three of them, and a tip
  // measured without them hangs off the bottom of the screen on exactly the
  // uplinks that have most to say.
  //
  // And so does the per-class stack, by its chart plus one line per class that
  // has nothing to draw. Forgetting to add a new section here is the standing
  // bug of this component: the section renders fine and the tip simply sits
  // too low to read, which looks like a placement glitch rather than a missing
  // term in this sum.
  //
  // THE TERMS ROUND UP, ON PURPOSE. `tipPosition` clamps the top to
  // `innerHeight - h - 8`, so over-stating the height only lifts the tip
  // higher, while under-stating it hangs the bottom off the screen — the
  // failure is one-sided, and so is the rounding. GAP_LINE_PX is 40 rather
  // than one line's 19 because at 320 px wide the longer sentences wrap to two
  // (measured in a browser: a wrapped line is 38 px, an unwrapped one 19), and
  // which ones wrap depends on the locale.
  const pos = tipPosition(
    hover.clientX,
    hover.clientY,
    (levels.length > 0 ? 380 : 210) +
      (hover.facts?.length ?? 0) * FACT_LINE_PX +
      (classDrawn || classGaps > 0 ? CLASS_KICKER_PX : 0) +
      (classDrawn ? CLASS_CHART_PX : 0) +
      classGaps * GAP_LINE_PX,
  );

  return (
    <div className="plan-link-traffic" style={pos} aria-hidden>
      <div className="plan-link-traffic-head">
        <div className="plan-link-traffic-title">{hover.title}</div>
        {/* The CONFIGURATION above the time window, and that order is the
            point: "VLAN 20 · Voix" is what the operator came for, "last 24
            hours" only says what the chart under it covers. */}
        {hover.facts && hover.facts.length > 0 && (
          <div className="porttip-facts">
            {hover.facts.map((f) => (
              <div key={f}>{f}</div>
            ))}
          </div>
        )}
        {hover.subtitle && <div className="note">{hover.subtitle}</div>}
      </div>
      {!tele && isFetching ? (
        <div className="note">{t("planPage.linkTrafficLoading")}</div>
      ) : isError ? (
        <div className="note">{t("planPage.linkTrafficError")}</div>
      ) : tele && !tele.available ? (
        <div className="note">{tele.why || t("planPage.linkTrafficUnavailable")}</div>
      ) : !hasTraffic && levels.length === 0 && !classDrawn && classGaps === 0 ? (
        <div className="note">{t("planPage.linkTrafficEmpty")}</div>
      ) : (
        <>
          {hasTraffic && (
            <LineChart
              height={120}
              series={[
                { name: t("switchDetailPage.chartIncoming"), color: SERIES.in, points: traffic.rx },
                { name: t("switchDetailPage.chartOutgoing"), color: SERIES.out, points: traffic.tx },
              ]}
              format={fmtMbps}
              xLabel={xLabel}
            />
          )}
          {levels.length > 0 && (
            <div className="porttip-levels">
              {/* This used to be an inline `marginTop: 24` with a paragraph
                 explaining that `.chart` was a fixed-height box whose legend
                 rendered inside it and overflowed onto whatever followed —
                 here, the kicker landed on top of "Inbound / Outbound".
                 Stacking two charts was the only place in the product that
                 exposed it.

                 The bug is fixed at the source now (`.chart-block` wraps the
                 legend, the plot keeps its height), so the gap is an ordinary
                 separation between two charts and belongs in the sheet. */}
              <div className="kicker" style={{ marginBottom: 4 }}>
                {t("switchDetailPage.opticsLevelsTitle")}
              </div>
              <LevelChart lines={levels} stepSeconds={tele?.step_seconds ?? 0} />
            </div>
          )}
          {/* THE PER-CLASS STACK LIVES HERE AND NOT IN THE SQUARE'S QuickTip,
              and that is a measured choice rather than a layout preference.
              On a faceplate both overlays claim hover exclusivity and this one
              takes the slot a beat after the bubble opens, so anything put in
              the QuickTip is dismissed before it can be read (see the `facts`
              field above, which documents the same race). A chart put there
              would be work nobody ever sees. */}
          {(classDrawn || classGaps > 0) && (
            <div className="porttip-levels">
              <div className="kicker porttip-kicker">
                {t("switchDetailPage.portClassTitle")}
              </div>
              <PortClassChart
                bands={classes}
                stepSeconds={tele?.step_seconds ?? 0}
                height={120}
              />
            </div>
          )}
        </>
      )}
    </div>
  );
}
