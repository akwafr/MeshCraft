// THE FLOATING ROW TIP OF THE "LOW-BANDWIDTH PORTS" TILE: what a port that
// negotiated slow is actually carrying, and which machine is sitting behind it.
//
// # WHY A FLOATING TIP, AND NOT AN EXPANDING PANEL
//
// The tile is a DASHBOARD tile, which in this product means a fixed-height box
// that already scrolls: `dashboardGrid.tsx` wraps every tile in
// `.dash-grid-tile-clip` (`overflow: hidden`) around a `.card` with
// `overflow: auto`, three grid rows tall — 276 px, once the gaps are counted.
// An inline expansion would therefore be clipped at the tile's edge, would
// make an already small scrolling box smaller, and would push every row below
// it down. The last one disqualifies it: on a dashboard somebody is SCANNING,
// a row that grows under the pointer moves the row they were about to read.
//
// A `position: fixed` overlay has none of those problems. Its containing block
// is the viewport, so no ancestor's `overflow` clips it — which is the whole
// reason it can live inside a clipped tile and still be 320 px wide — and it
// displaces nothing. It is also the pattern the product already has twice,
// `PortHoverTip` on a faceplate and `LinkTrafficTip` on the topology, and this
// file deliberately borrows their placement helper, their query and their four
// loading/error/absent phrases: a third tip that answered "what is this
// interface doing?" in its own words would be a third thing to learn.
//
// `QuickTip` was the other candidate and cannot serve. It carries a STRING,
// and a traffic chart is not a string. What it does give is the shape to copy,
// which UI-GUIDE §3 states for both of the sanctioned tips: positioned
// `fixed`, placed FROM THE TRIGGER'S RECTANGLE, and required to escape a
// container that is already in `overflow: auto`. All three are true here — and
// the second one is why this tip is anchored on the row rather than on the
// pointer, where its two older siblings track the cursor.
//
// # IT IS NOT HOVER-ONLY, WHICH IS THE PART OF §3 THAT BINDS
//
// "Hover must never be required to read content." The tile's own rows — device,
// port, negotiated speed, and the rate the sparkline carries — are all still on
// the screen with nothing hovered; this tip adds the history and the far end.
// And it is reachable three ways, not one: hover, FOCUS (the device link in the
// row is already a tab stop, so the keyboard path costs no new one), and TAP on
// a coarse pointer. A product used on a tablet in a technical room, sometimes
// keyboard-only, cannot have a detail that exists on `:hover` alone.
//
// # WHAT IT COSTS: nothing the dashboard was not already paying
//
// Two queries, both shared BY KEY with screens the operator already opens:
//
//   - `usePortTelemetry(deviceId)` is per DEVICE and keyed exactly as the
//     faceplate and the topology key it, so running the pointer down six rows
//     of the same switch costs ONE request — and a switch already hovered on
//     another page costs none;
//   - the client ledger is `["clients", siteId]`, the Clients page's own key,
//     and it is `enabled` only once a row has been opened. A dashboard nobody
//     hovers never asks for it.
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";

import { get, type SiteClients } from "./api";
import { clientsBehindPort } from "./lowBandwidth";
import { fmtMbps } from "./network";
import { tipPosition, usePortTelemetry, useTipXLabel } from "./PortHoverTip";
import { portTraffic } from "./telemetry";
import { LineChart, SERIES } from "./viz";

/**
 * How many MACs the tip lists before it says how many it is not listing.
 *
 * Six, because the tip is an overlay and not a page: a 100 Mbit/s port is
 * nearly always an access port with one machine on it, and the case that
 * overflows this — a trunk that negotiated slow — is one where the COUNT is
 * the finding and the individual addresses are not. The Clients page lists
 * them all, filtered by switch and port, and that is where that question goes.
 */
const BEHIND_MAX = 6;

/** Which row is open, and where to draw its tip. */
export type LowBandwidthHover = {
  /** Stable per row, so moving between rows does not remount the queries. */
  key: string;
  deviceId: string;
  /** The site of THAT device: the client ledger is a site resource. */
  siteId: string;
  device: string;
  port: string;
  /** The negotiated label, verbatim as the device reported it. */
  link: string;
  /**
   * The ROW's rectangle in viewport coordinates — its right edge and its top.
   *
   * Anchored on the row and not on the pointer, unlike the two sibling tips,
   * and for a reason that is specific to a LIST: a tip that followed the
   * pointer would slide across the screen while somebody reads down the rows,
   * where one anchored per row steps cleanly from one to the next. It is also
   * the only anchor a tip opened by KEYBOARD can supply, so one rule serves
   * both gestures instead of two rules disagreeing about where the tip goes.
   */
  anchorX: number;
  anchorY: number;
};

export function LowBandwidthTip({ hover }: { hover: LowBandwidthHover | null }) {
  const { t } = useTranslation();
  const { data: tele, isFetching, isError } = usePortTelemetry(hover?.deviceId);
  const xLabel = useTipXLabel(tele?.step_seconds ?? 0);
  const siteId = hover?.siteId ?? "";
  // The Clients page's key and cadence, so the two share one cache entry.
  const { data: ledger, isPending: ledgerPending } = useQuery({
    queryKey: ["clients", siteId],
    queryFn: () => get<SiteClients>(`/api/sites/${siteId}/clients`),
    enabled: !!siteId,
    staleTime: 30_000,
  });

  if (!hover) return null;

  const traffic = portTraffic(tele, hover.port);
  const hasTraffic =
    traffic.rx.some((v) => v != null) || traffic.tx.some((v) => v != null);

  const behind = clientsBehindPort(ledger?.clients, hover.deviceId, hover.port);
  const shown = behind.slice(0, BEHIND_MAX);
  const hidden = behind.length - shown.length;

  // The tip's height drives its placement, and it is not constant — the list
  // below the chart is nought to BEHIND_MAX rows long. Estimated from the row
  // count rather than measured, which is why `tipPosition` takes a number at
  // all: a measured height needs a render to measure, and that first render is
  // the one that would hang off the bottom of the screen.
  const estHeight = 220 + 20 * (Math.max(shown.length, 1) + (hidden > 0 ? 1 : 0));
  const pos = tipPosition(hover.anchorX, hover.anchorY, estHeight);

  return (
    <div className="plan-link-traffic" style={pos} aria-hidden>
      <div className="plan-link-traffic-head">
        <div className="plan-link-traffic-title">
          {hover.device} · {hover.port}
        </div>
        {/* The negotiated label, again and verbatim: the tip can be read with
            the list scrolled out from under it, and then it is the only thing
            saying which speed this port is stuck at. */}
        <div className="note">
          {hover.link} · {t("planPage.linkTraffic24h")}
        </div>
      </div>

      {!tele && isFetching ? (
        <div className="note">{t("planPage.linkTrafficLoading")}</div>
      ) : isError ? (
        <div className="note">{t("planPage.linkTrafficError")}</div>
      ) : tele && !tele.available ? (
        <div className="note">{tele.why || t("planPage.linkTrafficUnavailable")}</div>
      ) : !hasTraffic ? (
        <div className="note">{t("planPage.linkTrafficEmpty")}</div>
      ) : (
        <LineChart
          height={120}
          series={[
            {
              name: t("switchDetailPage.chartIncoming"),
              color: SERIES.in,
              points: traffic.rx,
            },
            {
              name: t("switchDetailPage.chartOutgoing"),
              color: SERIES.out,
              points: traffic.tx,
            },
          ]}
          format={fmtMbps}
          xLabel={xLabel}
        />
      )}

      {/* WHAT IS BEHIND THE PORT, which is the half of this tip the traffic
          chart cannot answer. A slow negotiation is a fault of the LINK, and
          the link has two ends: the curve says whether the limit hurts, and
          this says whose cable to go and look at. */}
      <div className="lowbw-behind">
        <div className="kicker">{t("dashboard.lowBandwidthBehind")}</div>
        {/* `isPending` is also true for a query that is DISABLED, which is
            what an empty site id would produce — so it is guarded, or a device
            row carrying no site would promise a list that never arrives. */}
        {hover.siteId && ledgerPending ? (
          <div className="note">{t("planPage.linkTrafficLoading")}</div>
        ) : shown.length === 0 ? (
          // "Currently seen", not "none": the ledger is a record of what
          // bridges have LEARNED, and a port can carry a machine that has sent
          // nothing inside the freshness window. Writing "nothing is plugged
          // in" here would be this tip's one costly lie.
          <div className="note">{t("dashboard.lowBandwidthBehindNone")}</div>
        ) : (
          <ul className="lowbw-clients">
            {shown.map((c) => (
              <li key={c.mac}>
                <div className="lowbw-client-line">
                  {/* The announced name when the machine speaks LLDP, the OUI
                      registrant when it does not, and NEITHER invented when
                      the registry has never heard of the prefix — the MAC
                      alone is then the honest answer, and it is what an
                      operator searches the Clients page with anyway. The
                      identity is what gets ellipsised, never the address: a
                      truncated hostname is still recognisable, where half a
                      MAC is not an identifier at all. */}
                  {c.name ? (
                    <span className="lowbw-client-id">{c.name}</span>
                  ) : c.vendor ? (
                    <span className="lowbw-client-id note">{c.vendor}</span>
                  ) : null}
                  <span className="mono lowbw-client-mac">{c.mac}</span>
                </div>
                {c.elsewhere ? (
                  // The location is a HEURISTIC — the ledger picks the port
                  // with the fewest neighbours — so when the same MAC also
                  // answers elsewhere the tip has to say so. Without this line
                  // it would point at a cable on the strength of an
                  // arbitration it never showed.
                  <div className="note lowbw-client-hedge">
                    {t("dashboard.lowBandwidthElsewhere")}
                  </div>
                ) : null}
              </li>
            ))}
            {hidden > 0 && (
              <li className="note">
                {t("dashboard.lowBandwidthBehindMore", { count: hidden })}
              </li>
            )}
          </ul>
        )}
      </div>
    </div>
  );
}
