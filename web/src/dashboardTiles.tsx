/**
 * The tile registry: every placeable tile, and what it draws.
 *
 * # Where these came from
 *
 * Until this file existed there were two hard-coded pages — a "Dashboard" and
 * a "Monitoring" that said almost the same thing in a different order — and
 * neither could be changed by the person looking at it. Every card and every
 * stat tile from both is here, one entry each, and nothing was dropped on the
 * way: that was the requirement, and it is checked server-side too
 * (TestDefaultDashboardsCoverTheWholeCatalogue).
 *
 * # The registry is a MAP, not a switch
 *
 * A `switch (tile.type)` in the renderer would have compiled fine with a tile
 * type nobody handles, and the tile picker would have had to repeat the list.
 * Here one entry carries everything about a tile: its name in the picker, the
 * size it gets when added, whether it draws its own frame, the datasets it
 * reads, and the component. Adding a tile is one object; forgetting half of it
 * is a type error.
 *
 * # `needs` mirrors the server's catalogue, and the server is the authority
 *
 * `needs` decides what the page FETCHES. The same table exists in Go
 * (store.TileCatalog) where it decides what a PUBLIC link is allowed to obtain.
 * They are two copies on purpose: this one is a performance decision — do not
 * ask for events nobody displays — and that one is a security boundary. If they
 * ever disagree, the server wins and the tile shows its empty state, which is a
 * visible, harmless mismatch. The reverse arrangement — the browser telling the
 * server what to fetch — is the one that would be a hole.
 */
import { useEffect, useId, useRef, useState } from "react";
import { ageMs, viewerDate } from "./ui/clock";
import { useTranslation } from "react-i18next";
import { Link, useNavigate } from "react-router-dom";

import type {
  Device,
  DeviceLink,
  Event,
  OpticsAlerts,
  ProxyInfo,
  TrafficSeries,
  Vlan,
} from "./api";
import { ActionButton } from "./ActionButton";
import { HelpTip, QuickTip } from "./components";
import {
  claimHoverTip,
  registerHoverTip,
  releaseHoverTip,
} from "./hoverExclusive";
import { TileSettings } from "./dashboardTileEdit";
import { lowBandwidthPorts, type LowBandwidthPort } from "./lowBandwidth";
import { LowBandwidthTip, type LowBandwidthHover } from "./LowBandwidthTip";
import { modelOf, modelShort } from "./modelLabel";
import { StatusDot } from "./netui";
import {
  agoLabel,
  decimalSeparator,
  deviceRates,
  expandPorts,
  fmtMbps,
  reachability,
  serves,
  siteRates,
  speedMbps,
  statusColor,
  vlanMap,
} from "./network";
import { deviceDetailPath, sitePath } from "./paths";
import { usePortTelemetry, useTipXLabel } from "./PortHoverTip";
import { PortTrafficCell } from "./PortTraffic";
import { M, portTraffic, type DeviceTelemetry } from "./telemetry";
import { useDismissable } from "./ui/dismiss";
import SearchSelect, { type SearchOption } from "./ui/SearchSelect";
import { BarList, LineChart, SERIES, StatTile } from "./viz";

/**
 * Everything the tiles read, in one bag.
 *
 * Tiles do not fetch. Two reasons, and the second is the one that matters: a
 * dashboard can hold six tiles that all need the device list, and six copies of
 * the same query would be six copies of the same answer; and the PUBLIC page
 * gets its data from one route in one response, so a tile that fetched on its
 * own could not be shown there at all.
 */
export interface TileData {
  devices: Device[];
  vlans: Vlan[];
  links: DeviceLink[];
  events: Event[];
  /**
   * Proxies — an INSTANCE resource, not a site one. Undefined on a public page,
   * and that is deliberate rather than incidental: serving the list of
   * collectors and their health to whoever holds a share link would describe
   * the deployment to someone who was given one site's dashboard. The alert
   * tile shows device and link alerts there and says so.
   */
  proxies?: ProxyInfo[];
  /**
   * The traffic series for one window. A function and not a field because two
   * traffic tiles on one dashboard can be set to two different windows.
   * Undefined = not loaded (or, on a public page, not the window that was
   * served).
   */
  trafficFor: (hours: number) => TrafficSeries | undefined;
  /**
   * The low receive-level list. Undefined = not loaded (no tile asked, or the
   * answer has not arrived), which must render as loading, never as the good
   * news an EMPTY rows list means.
   */
  optics?: OpticsAlerts;
}

export interface TileProps {
  data: TileData;
  /**
   * What to draw in this tile's header — ALREADY RESOLVED.
   *
   * The operator's own title when the tile carries one (`options.title`), the
   * catalogue's translated `labelKey` otherwise. Resolved once, in
   * `dashboardGrid`, rather than eighteen times here: a tile that called
   * `t(labelKey)` itself would silently ignore a rename, and the next tile
   * added would too.
   *
   * A custom title is DATA — not an i18n key, never translated (ADR-0027). So
   * a tile prints this string and does not reach for the catalogue.
   *
   * Named `heading` and not `title` on purpose. The native-tooltip ratchet
   * (web/scripts/uilint.mjs) matches a `title` ATTRIBUTE on a JSX line, blunt
   * and line-based by design, and it cannot tell a tooltip from a component
   * prop unless the value is a `t(…)` call — so eight tile headers carrying a
   * resolved string would have read as eight new native tooltips. The STORED
   * option keeps the name `title`: that is what it is on the wire, and a
   * lint heuristic has no business renaming a field in the database.
   */
  heading: string;
  /** Site slug, for in-app links. EMPTY on a public page — see `publicView`. */
  siteSlug: string;
  /**
   * This tile is on a shared, unauthenticated page. Links become plain text:
   * every screen they point at requires a session, so offering them would send
   * a visitor to a login form they cannot pass.
   */
  publicView: boolean;
  /**
   * The dashboard is in WRITABLE edit mode — the arrangement is being changed
   * and `setOption` will be saved by the Save button the operator already uses.
   *
   * A tile shows its configuration here and nowhere else (ADR-0032): a
   * dashboard being read shows measurements, not forms. The panel must go
   * inside `TileSettings` (dashboardTileEdit.tsx) or it will be inert, because
   * edit mode takes pointer events away from tile content.
   */
  editing: boolean;
  options: Record<string, unknown>;
  /** Merge a patch into this tile's options. */
  setOption: (patch: Record<string, unknown>) => void;
}

export interface TileDef {
  /** i18n key of the tile's name, shown in the picker and the edit header. */
  labelKey: string;
  /** Size in grid cells when the tile is added. */
  w: number;
  h: number;
  /**
   * False when the tile draws its own container (the stat tiles use `.tile`,
   * which already has a background and a border). Wrapping those in a `.card`
   * would frame them twice.
   */
  framed: boolean;
  /** Datasets this tile reads. Mirrors store.TileCatalog — see the header. */
  needs: Dataset[];
  Component: (p: TileProps) => JSX.Element;
}

export type Dataset = "devices" | "vlans" | "links" | "events" | "traffic" | "optics";

// --- Small shared helpers ---

function numberOption(v: unknown, fallback: number): number {
  return typeof v === "number" && Number.isFinite(v) ? v : fallback;
}

function stringOption(v: unknown, fallback: string): string {
  return typeof v === "string" ? v : fallback;
}

function boolOption(v: unknown, fallback: boolean): boolean {
  return typeof v === "boolean" ? v : fallback;
}

/**
 * A box's own width and height, live.
 *
 * NOT `useBreakpoint`, and the difference is the whole point: a tile is sized
 * by the ARRANGEMENT, not by the viewport. Three columns of a 4K wall display
 * is a narrow tile on a wide screen, and twelve columns of a laptop is a wide
 * one on a narrow screen — a media query answers the wrong question for
 * anything inside a resizable cell.
 *
 * Sub-pixel changes are swallowed. A fractional grid column makes the observer
 * fire with widths that differ in the fourth decimal, and committing those to
 * state is a render on every scrollbar appearance for no visible change.
 */
function useBoxSize(ref: React.RefObject<HTMLElement | null>) {
  const [box, setBox] = useState({ w: 0, h: 0 });
  useEffect(() => {
    const el = ref.current;
    if (!el || typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver((entries) => {
      const r = entries[entries.length - 1]?.contentRect;
      if (!r) return;
      setBox((prev) =>
        Math.abs(prev.w - r.width) < 1 && Math.abs(prev.h - r.height) < 1
          ? prev
          : { w: r.width, h: r.height },
      );
    });
    ro.observe(el);
    return () => ro.disconnect();
  }, [ref]);
  return box;
}

/** Milliseconds in 24 h, named so the arithmetic below reads as what it is. */
const DAY_MS = 86400000;

/**
 * The alert list, computed once and used by both the list tile and the count
 * tile. Two independent computations would eventually disagree, and a "3
 * alerts" headline above a list of two is the kind of thing that destroys
 * confidence in a screen.
 *
 * `proxies` is optional and its absence is not an empty fleet: on a public page
 * proxies are not served at all (see TileData).
 */
interface Alert {
  color: string;
  text: string;
  to?: string;
}

/** The `t` shape these helpers need, without dragging i18next's generics in. */
type Translate = (key: string, opts?: Record<string, unknown>) => string;

function buildAlerts(data: TileData, siteSlug: string, t: Translate): Alert[] {
  const byId = new Map(data.devices.map((d) => [d.id, d]));
  const out: Alert[] = [];
  data.devices.forEach((d) => {
    if (d.status === "off" || d.status === "warn") {
      out.push({
        color: statusColor(d.status),
        text:
          d.name +
          (d.status === "off"
            ? ` ${t("dashboard.unreachableSuffix")}`
            : ` ${t("dashboard.alertSuffix")}`) +
          (d.status_note ? " — " + d.status_note : ""),
        to: siteSlug ? deviceDetailPath(siteSlug, d) : undefined,
      });
    }
  });
  data.links.forEach((l) => {
    if (l.status !== "up" || l.note) {
      const a = byId.get(l.a_device)?.name ?? "?";
      const b = byId.get(l.b_device)?.name ?? "?";
      out.push({
        color: l.status === "down" ? "var(--danger)" : "var(--warn)",
        text: t("dashboard.linkAlert", {
          a,
          b,
          cut: l.status === "down" ? t("dashboard.cutSuffix") : "",
          note: l.note ? " — " + l.note : "",
        }),
        to: siteSlug ? sitePath(siteSlug, "topology") : undefined,
      });
    }
  });
  (data.proxies ?? []).forEach((p) => {
    if (p.internal) return;
    const stale =
      !p.last_seen || (ageMs(p.last_seen) ?? Infinity) > 120000;
    if (!p.migrated_at && !p.revoked && stale) {
      out.push({
        color: "var(--warn)",
        text: t("dashboard.proxyNoHeartbeat", { name: p.name }),
        to: "/config/proxy",
      });
    }
  });
  return out;
}

/** The card header every framed tile shares: a kicker and an optional aside. */
function TileHead({ heading, aside }: { heading: string; aside?: JSX.Element }) {
  return (
    <div className="card-head">
      <div className="kicker">{heading}</div>
      {aside}
    </div>
  );
}

// --- Stat tiles, from the old Dashboard page ---

/**
 * The headline counter — and the one tile where "how many are up?" has to be
 * answered honestly about the devices meshcraft cannot answer for.
 *
 * ONLY DEVICES MESHCRAFT CAN CURRENTLY VOUCH FOR COUNT (decided 2026-08-08,
 * see UI-GUIDE §7). Neither a device monitoring has never observed nor one it
 * observed before and can no longer see is online. Consequences, all deliberate:
 *
 *   - the DENOMINATOR is still the whole fleet. Dropping those devices from the
 *     total as well would read "3 / 3" and make them disappear; the point of the
 *     ink is that they stay visible. It reads "3 / 5" and the gap is real;
 *   - the shortfall is NOT called offline. `offlineCount` gets the devices we
 *     know are down, not the ones we cannot see — the sub-line would otherwise
 *     assert an outage nobody has observed;
 *   - the dot answers the shortfall in the palette's own words, and the order
 *     of the branches IS the specification, mirroring `deviceDot`: a fault we
 *     KNOW about outranks anything we merely cannot see.
 *
 *	offline > 0                     --danger if nothing is up, else --warn
 *	only blind devices missing      --indeterminate — we lost the vantage point
 *	only unobserved ones missing    --text-dim — we have never looked
 *
 *     A mix of blind and unobserved is yellow, not grey: grey claims nothing
 *     was ever looked at, which would be false of the blind half, and yellow is
 *     the more actionable of the two — it points at a proxy. Neither is orange:
 *     painting "we cannot see" as an alert is the exact confusion
 *     `--indeterminate` and `--text-dim` exist to prevent.
 */
function DevicesOnlineTile({ data, heading }: TileProps) {
  const { t } = useTranslation();
  const r = siteRates(data.devices);
  const total = data.devices.length;
  const missing = total - r.online;
  const offline = missing - r.unobserved - r.blind;
  return (
    <StatTile
      label={heading}
      value={`${r.online} / ${total}`}
      dotColor={
        missing === 0
          ? "var(--ok)"
          : offline > 0
            ? r.online === 0
              ? "var(--danger)"
              : "var(--warn)"
            : r.blind > 0
              ? "var(--indeterminate)"
              : "var(--text-dim)"
      }
      sub={
        total
          ? (t("dashboard.offlineCount", { count: offline }) as string)
          : (t("dashboard.noDevice") as string)
      }
    />
  );
}

function ActivePortsTile({ data, heading }: TileProps) {
  const { t } = useTranslation();
  const r = siteRates(data.devices);
  return (
    <StatTile
      label={heading}
      value={String(r.up)}
      sub={t("dashboard.outOfPorts", { count: r.ports }) as string}
    />
  );
}

function RxTile({ data, heading }: TileProps) {
  const { t } = useTranslation();
  return (
    <StatTile
      label={heading}
      value={fmtMbps(siteRates(data.devices).rx)}
      sub={t("dashboard.siteCumulative") as string}
    />
  );
}

function TxTile({ data, heading }: TileProps) {
  const { t } = useTranslation();
  return (
    <StatTile
      label={heading}
      value={fmtMbps(siteRates(data.devices).tx)}
      sub={t("dashboard.siteCumulative") as string}
    />
  );
}

function PoeTile({ data, heading }: TileProps) {
  const { t } = useTranslation();
  const r = siteRates(data.devices);
  const pct = r.poeBudget ? Math.round((r.poe / r.poeBudget) * 100) : 0;
  return (
    <StatTile
      label={heading}
      value={Math.round(r.poe) + " W"}
      sub={
        r.poeBudget
          ? (t("dashboard.poePercentOf", { pct, budget: r.poeBudget }) as string)
          : (t("dashboard.noBudgetDeclared") as string)
      }
      accent={pct >= 85 ? "var(--warn)" : undefined}
    />
  );
}

function AlertCountTile({ data, siteSlug, heading }: TileProps) {
  const { t } = useTranslation();
  const alerts = buildAlerts(data, siteSlug, t as unknown as Translate);
  return (
    <StatTile
      label={heading}
      value={String(alerts.length)}
      accent={alerts.length ? "var(--danger)" : "var(--ok)"}
      sub={
        alerts.length
          ? (t("dashboard.toHandle") as string)
          : (t("dashboard.allNominal") as string)
      }
    />
  );
}

// --- Charts and lists, from the old Dashboard page ---

/** The four windows the traffic chart offers — and the only four the public route serves. */
const TRAFFIC_WINDOWS = [
  { h: 24, label: "24 h" },
  { h: 72, label: "3 d" },
  { h: 168, label: "7 d" },
  { h: 720, label: "30 d" },
] as const;

function TrafficTile({ data, options, setOption, heading }: TileProps) {
  const { t, i18n } = useTranslation();
  const hours = numberOption(options.hours, 24);
  const series = data.trafficFor(hours);
  const points = series?.points ?? [];
  const hasSamples = points.some((p) => p.rx_mbps != null || p.tx_mbps != null);
  // The full time grid is kept even where values are null: an empty stretch
  // stays empty instead of compressing later samples across the whole width.
  const rx = points.map((p) => p.rx_mbps);
  const tx = points.map((p) => p.tx_mbps);
  const hourLabel = (i: number) => {
    const p = points[i];
    if (!p) return "";
    // ADR-0008: a server timestamp is translated into the VIEWER's frame before
    // it is shown. This axis was the one place in this file still calling the
    // raw constructor, so every other time on the dashboard sat on the
    // operator's watch and this one sat on the controller's -- the two
    // disagreeing by whatever the clocks disagree by, which was six minutes
    // until NTP landed on the host and is unbounded again the moment a proxy
    // drifts. The samples are stamped by the PROXY, not by the controller,
    // which is exactly why this cannot be assumed small.
    const d = viewerDate(p.ts) ?? new Date(p.ts);
    if (hours <= 24) {
      return d.toLocaleTimeString(i18n.language, { hour: "2-digit", minute: "2-digit" });
    }
    return d.toLocaleDateString(i18n.language, { month: "short", day: "numeric" });
  };
  return (
    <>
      <TileHead
        heading={heading}
        aside={
          <div className="row" style={{ gap: 4, flex: "none" }}>
            {TRAFFIC_WINDOWS.map((o) => (
              <button
                key={o.h}
                className={"chip" + (o.h === hours ? " on" : "")}
                onClick={() => setOption({ hours: o.h })}
              >
                {o.label}
              </button>
            ))}
          </div>
        }
      />
      <div className="note" style={{ marginBottom: 8 }}>
        {hasSamples ? t("dashboard.measured") : t("dashboard.noHistoryInWindow")}
      </div>
      <LineChart
        series={[
          { name: t("dashboard.incoming"), color: SERIES.in, points: rx },
          // OUTGOING IS FED NEGATIVE so the chart mirrors it under the axis
          // instead of stacking it on top of incoming. The two directions
          // used to be superimposed on one half, which made the larger one
          // hide the smaller for most of the window. The sign is geometry,
          // not a measurement: LineChart labels and the tooltip both read
          // the absolute value (see the note on the axis labels).
          {
            name: t("dashboard.outgoing"),
            color: SERIES.out,
            points: tx.map((v) => (v == null || Number.isNaN(v) ? null : -v)),
          },
        ]}
        format={fmtMbps}
        ramp
        // `rawIndex`, because `hourLabel` reads `points[i]` — the array fed to
        // the chart, not the 150 points the chart had room to draw. A 24 h
        // window is 1441 samples, so the drawn index named a time nine times
        // too early in the window and only agreed with reality at the right
        // edge, where "now" was answering anyway.
        xLabel={({ rawIndex, newest }) =>
          newest ? t("dashboard.now") : hourLabel(rawIndex)
        }
      />
    </>
  );
}

function VlanPortsTile({ data, heading }: TileProps) {
  const { t } = useTranslation();
  const rows = data.vlans
    .map((v) => {
      let n = 0;
      data.devices.forEach((d) =>
        expandPorts(d).forEach((pr) => {
          if (pr.p.mode && pr.p.mode !== "libre" && serves(pr.p, v.vlan_id)) n++;
        }),
      );
      return { key: v.id, label: v.name, sub: String(v.vlan_id), value: n, color: v.color };
    })
    .filter((r) => r.value > 0)
    .sort((a, b) => b.value - a.value)
    .slice(0, 8);
  return (
    <>
      <TileHead
        heading={heading}
        aside={<span className="note">{t("dashboard.topEight")}</span>}
      />
      <BarList rows={rows} format={(v) => t("dashboard.portsCount", { count: v }) as string} />
    </>
  );
}

function AlertListTile({ data, siteSlug, publicView, heading }: TileProps) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const alerts = buildAlerts(data, publicView ? "" : siteSlug, t as unknown as Translate);
  return (
    <>
      <TileHead
        heading={heading}
        aside={
          // On a shared page there is nowhere to send the reader — every screen
          // behind this link needs a session — and the proxy alerts are not even
          // computed there. Saying so beats a link that lands on a login form.
          publicView ? (
            <span className="note">{t("dashboards.publicDeviceAndLinkOnly")}</span>
          ) : undefined
        }
      />
      {alerts.length === 0 ? (
        <div className="empty">{t("dashboard.noAlerts")}</div>
      ) : (
        <div className="alert-list" style={{ margin: 0 }}>
          {alerts.map((al, i) =>
            al.to ? (
              <button key={i} className="alert-row" onClick={() => navigate(al.to as string)}>
                <span className="dot" style={{ background: al.color }} />
                <span>{al.text}</span>
                <span className="note" style={{ marginLeft: "auto", letterSpacing: ".06em" }}>
                  {t("dashboard.viewArrow")}
                </span>
              </button>
            ) : (
              <div key={i} className="alert-row" style={{ cursor: "default" }}>
                <span className="dot" style={{ background: al.color }} />
                <span>{al.text}</span>
              </div>
            ),
          )}
        </div>
      )}
    </>
  );
}

function RecentEventsTile({ data, heading }: TileProps) {
  const { t, i18n } = useTranslation();
  // The tile opens on six and grows by six. It does NOT grow to everything at
  // once: the tile is a fixed grid cell with its own scroll, and replacing a
  // six-line list with a three-hundred-line one inside it turns a glance into
  // a scroll with no way back. `data.events` is already the window the
  // dashboard fetched — this reveals what was fetched, it does not fetch more.
  const [shown, setShown] = useState(6);
  const rows = data.events.slice(0, shown);
  const more = data.events.length - rows.length;
  return (
    <>
      {/* No "full log" link any more, and its absence is the point: the full
          log is a TILE now (table.event-log), which the operator can place on
          this very dashboard. A link to another page would send them away from
          the screen they arranged. */}
      <TileHead heading={heading} />
      {rows.length === 0 ? (
        <div className="empty">{t("dashboard.emptyLog")}</div>
      ) : (
        <div className="ev-compact">
          {rows.map((ev) => (
            <div key={ev.id} className="ev-compact-row">
              <span className="note ev-time">
                {(viewerDate(ev.ts) ?? new Date(ev.ts)).toLocaleTimeString(i18n.language, {
                  hour: "2-digit",
                  minute: "2-digit",
                })}
              </span>
              <span>{ev.message ?? ev.label}</span>
            </div>
          ))}
          {more > 0 && (
            <ActionButton
              kind="more"
              className="ghost ev-compact-more"
              onClick={() => setShown((v) => v + 6)}
            >
              {t("dashboard.showMore")}
            </ActionButton>
          )}
        </div>
      )}
    </>
  );
}

// --- Stat tiles, from the old Monitoring page ---

function UnreachableTile({ data, heading }: TileProps) {
  const { t } = useTranslation();
  const n = data.devices.filter((d) => d.status === "off").length;
  return (
    <StatTile
      label={heading}
      value={String(n)}
      accent={n ? "var(--danger)" : "var(--ok)"}
      sub={t("monitoringPage.supervisedOf", { count: data.devices.length })}
    />
  );
}

function Events24hTile({ data, heading }: TileProps) {
  const { t } = useTranslation();
  const last24 = data.events.filter((e) => (ageMs(e.ts) ?? Infinity) < DAY_MS);
  return (
    <StatTile
      label={heading}
      value={String(last24.length)}
      sub={t("dashboards.overLastEvents", { count: data.events.length })}
    />
  );
}

function WarningsTile({ data, heading }: TileProps) {
  const { t } = useTranslation();
  const warn = data.events.filter(
    (e) => (ageMs(e.ts) ?? Infinity) < DAY_MS && e.severity >= 3,
  ).length;
  return (
    <StatTile
      label={heading}
      value={String(warn)}
      accent={warn ? "var(--warn)" : undefined}
      sub={t("monitoringPage.severityGteWarning")}
    />
  );
}

function LastEventTile({ data, heading }: TileProps) {
  const { t, i18n } = useTranslation();
  const first = data.events[0];
  return (
    <StatTile
      label={heading}
      value={
        first
          ? (viewerDate(first.ts) ?? new Date(first.ts)).toLocaleTimeString(i18n.language, {
              hour: "2-digit",
              minute: "2-digit",
              second: "2-digit",
            })
          : "—"
      }
      sub={first ? first.type : t("monitoringPage.emptyLog")}
    />
  );
}

// --- Tables and chart, from the old Monitoring page ---

function FleetTile({ data, siteSlug, publicView, heading }: TileProps) {
  const { t } = useTranslation();
  return (
    <>
      <TileHead
        heading={heading}
        aside={<span className="note">{t("monitoringPage.snmpPollingNote")}</span>}
      />
      <div className="scroll-x scroll-cue">
        <table>
          <thead>
            <tr>
              <th>{t("monitoringPage.deviceCol")}</th>
              <th>{t("monitoringPage.stateCol")}</th>
              <th>{t("monitoringPage.portsCol")}</th>
              <th>{t("monitoringPage.rateCol")}</th>
              <th>{t("monitoringPage.poeCol")}</th>
            </tr>
          </thead>
          <tbody>
            {data.devices.map((d) => {
              const r = deviceRates(d);
              const off = d.status === "off";
              const pct = d.poe_budget_w
                ? Math.min(100, Math.round((r.poe / d.poe_budget_w) * 100))
                : 0;
              return (
                <tr key={d.id}>
                  <td>
                    {/* On a shared page the device name is text, and the second
                        line is the MODEL: a management address is a target, and
                        the public projection does not carry one at all. */}
                    {publicView || !siteSlug ? (
                      d.name
                    ) : (
                      <Link to={deviceDetailPath(siteSlug, d)}>{d.name}</Link>
                    )}
                    <QuickTip
                      text={!d.mgmt_ip && d.model ? d.model : undefined}
                      className="quicktip-block"
                    >
                      <div className="note">
                        {d.mgmt_ip || modelShort(modelOf(d), d.vendor)}
                      </div>
                    </QuickTip>
                  </td>
                  <td>
                    <span className="row" style={{ gap: 6 }}>
                      <StatusDot dev={d} />
                      <span style={{ fontSize: 11, fontWeight: 700 }}>
                        {reachability(d).label}
                      </span>
                    </span>
                    {reachability(d).detail && (
                      <div className="note">{reachability(d).detail}</div>
                    )}
                  </td>
                  <td className="note">
                    {r.up} / {r.total}
                  </td>
                  <td className="note" style={{ whiteSpace: "nowrap" }}>
                    {off ? "—" : `${fmtMbps(r.rx)} / ${fmtMbps(r.tx)}`}
                  </td>
                  <td style={{ minWidth: 110 }}>
                    {d.poe_budget_w ? (
                      <>
                        <div className="note">
                          {Math.round(r.poe)} / {d.poe_budget_w} W
                        </div>
                        <div className="meter" style={{ marginTop: 3 }}>
                          <div
                            style={{
                              width: pct + "%",
                              background: pct >= 85 ? "var(--warn)" : "var(--ok)",
                            }}
                          />
                        </div>
                      </>
                    ) : (
                      <span className="note">—</span>
                    )}
                  </td>
                </tr>
              );
            })}
            {data.devices.length === 0 && (
              <tr>
                <td colSpan={5} className="empty">
                  {t("monitoringPage.noDevices")}
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>
    </>
  );
}

function TopPortsTile({ data, heading }: TileProps) {
  const { t } = useTranslation();
  const vm = vlanMap(data.vlans);
  // Full-trunk uplinks are excluded: their traffic is already counted on the
  // access ports facing them.
  const rows = data.devices
    .flatMap((d) =>
      expandPorts(d)
        .map((pr) => ({ d, pr }))
        .filter(
          (x) =>
            x.pr.p.link &&
            !(x.pr.p.mode === "trunk" && x.pr.p.tagged === "all") &&
            x.d.status !== "off",
        ),
    )
    .map((x) => ({
      key: x.d.id + ":" + x.pr.name,
      label: (x.pr.p.label || x.pr.name) + " · " + x.d.name,
      sub: x.pr.name,
      value: x.pr.p.rx_mbps ?? 0,
      color: x.pr.p.pvid != null ? vm.get(x.pr.p.pvid)?.color : undefined,
    }))
    .filter((r) => r.value > 0)
    .sort((a, b) => b.value - a.value)
    .slice(0, 10);
  return (
    <>
      <TileHead
        heading={heading}
        aside={<span className="note">{t("monitoringPage.incomingRate")}</span>}
      />
      <BarList rows={rows} format={fmtMbps} />
    </>
  );
}

function EventLogTile({ data, options, setOption, siteSlug, publicView, heading }: TileProps) {
  const { t, i18n } = useTranslation();
  const sevMin = numberOption(options.severity_min, 0);
  const type = stringOption(options.type, "");

  const SEVERITIES: Record<number, { label: string; cls: string }> = {
    0: { label: t("monitoringPage.sevDebug"), cls: "sev-dim" },
    1: { label: t("monitoringPage.sevInfo"), cls: "sev-info" },
    2: { label: t("monitoringPage.sevNotice"), cls: "sev-notice" },
    3: { label: t("monitoringPage.sevWarning"), cls: "sev-warn" },
    4: { label: t("monitoringPage.sevError"), cls: "sev-error" },
    5: { label: t("monitoringPage.sevCritical"), cls: "sev-error" },
  };
  const TYPE_FILTERS = [
    { value: "", label: t("monitoringPage.allTypes") },
    { value: "port.", label: t("monitoringPage.typePorts") },
    { value: "device.", label: t("monitoringPage.typeDevices") },
    { value: "config.", label: t("monitoringPage.typeConfig") },
    { value: "vlan.", label: t("monitoringPage.typeVlans") },
    { value: "profile.", label: t("monitoringPage.typeProfiles") },
    { value: "link.", label: t("monitoringPage.typeLinks") },
    { value: "proxy.", label: t("monitoringPage.typeProxies") },
  ];

  // Filtering happens HERE and not in the query, unlike the old Monitoring page.
  // The dashboard fetches the log once for every tile that shows it, so a tile
  // with its own filter cannot have its own request without multiplying them —
  // and the public page gets one fixed window in one response, so a server-side
  // filter could not be honoured there at all. The window is stated below the
  // table rather than implied.
  const byId = new Map(data.devices.map((d) => [d.id, d]));
  const rows = data.events.filter(
    (e) => e.severity >= sevMin && (type === "" || e.type.startsWith(type)),
  );

  return (
    <>
      <div className="card-head">
        {/* NO HELP BUBBLE HERE ANY MORE (2026-10-25). What sat behind it was a
            ROADMAP — "the log receives configuration actions today; SNMP traps,
            syslog and polling will be added in phase 1" — and a roadmap ages
            into a lie on a screen nobody re-reads. The type filter beside the
            title already enumerates what the log carries, and it enumerates it
            from the data rather than from a plan. */}
        <div className="kicker">{heading}</div>
        <div className="row" style={{ gap: 8 }}>
          <select
            style={{ width: 170 }}
            value={type}
            onChange={(e) => setOption({ type: e.target.value })}
          >
            {TYPE_FILTERS.map((tf) => (
              <option key={tf.value} value={tf.value}>
                {tf.label}
              </option>
            ))}
          </select>
          <select
            style={{ width: 170 }}
            value={sevMin}
            onChange={(e) => setOption({ severity_min: Number(e.target.value) })}
          >
            <option value={0}>{t("monitoringPage.allSeverities")}</option>
            <option value={2}>{t("monitoringPage.noticeAndAbove")}</option>
            <option value={3}>{t("monitoringPage.warningsAndAbove")}</option>
            <option value={4}>{t("monitoringPage.errorsOnly")}</option>
          </select>
        </div>
      </div>
      <div className="scroll-x scroll-cue">
        <table className="journal">
          <thead>
            <tr>
              <th style={{ width: 130 }}>{t("monitoringPage.timestampCol")}</th>
              <th style={{ width: 90 }}>{t("monitoringPage.severityCol")}</th>
              <th style={{ width: 150 }}>{t("monitoringPage.typeCol")}</th>
              <th>{t("monitoringPage.messageCol")}</th>
              {/* No author column on a shared page: `actor` is an email address
                  and the public projection does not carry it (see
                  publicEvent, Go side). */}
              {!publicView && <th style={{ width: 170 }}>{t("monitoringPage.authorCol")}</th>}
            </tr>
          </thead>
          <tbody>
            {rows.map((ev) => {
              const sev = SEVERITIES[ev.severity] ?? SEVERITIES[1];
              const dev = ev.device_id ? byId.get(ev.device_id) : undefined;
              return (
                <tr key={ev.id}>
                  <td className="note" style={{ whiteSpace: "nowrap" }}>
                    {(viewerDate(ev.ts) ?? new Date(ev.ts)).toLocaleString(i18n.language, {
                      day: "2-digit",
                      month: "2-digit",
                      hour: "2-digit",
                      minute: "2-digit",
                      second: "2-digit",
                    })}
                  </td>
                  <td>
                    <span className={"sev " + sev.cls}>{sev.label}</span>
                  </td>
                  <td className="mono note">{ev.type}</td>
                  <td>
                    {dev && !publicView && siteSlug ? (
                      <Link to={deviceDetailPath(siteSlug, dev)}>{ev.message ?? ev.label}</Link>
                    ) : (
                      ev.message ?? ev.label
                    )}
                  </td>
                  {!publicView && (
                    <td className="note">{ev.actor || t("monitoringPage.systemActor")}</td>
                  )}
                </tr>
              );
            })}
            {rows.length === 0 && (
              <tr>
                <td colSpan={publicView ? 4 : 5} className="empty">
                  {t("monitoringPage.noEventsForFilter")}
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>
      <div className="note" style={{ marginTop: 8 }}>
        {t("dashboards.logWindow", { shown: rows.length, total: data.events.length })}
      </div>
    </>
  );
}

// --- The registry ---

/**
 * Ports that came UP at 10 or 100 Mbit/s, and what each one is carrying.
 *
 * The judgement — which speeds count, what "up" means, why an err-disabled port
 * is excluded, which clients count as being behind a port — lives in
 * `lowBandwidth.ts` so it can be tested outside React. This draws the rows and
 * owns one gesture.
 *
 * # THE LIST ALONE WAS NOT CONSULTABLE
 *
 * It named a device, a port and a speed, which is enough to know that something
 * is wrong and not enough to decide whether to care. Two facts settle that, and
 * neither needs a new endpoint:
 *
 *   - WHAT THE PORT IS CARRYING. A 100 Mbit/s negotiation on a port nobody uses
 *     is a work item; the same negotiation at 80 Mbit/s is an outage somebody is
 *     living with. The sparkline is in the row, always visible, because that is
 *     the number that sorts the list by urgency — and it is nearly free:
 *     `PortTrafficCell` queries per DEVICE, so a column of rows on one switch
 *     collapses onto a single request (see PortTraffic.tsx).
 *   - WHO IS BEHIND IT. A slow link has two ends, and the far one is what
 *     somebody has to walk to. That is a per-row lookup into the site's client
 *     ledger, so it waits for the row to be opened — in `LowBandwidthTip`,
 *     which also explains why the gesture is a floating tip and not a panel.
 *
 * Both are gated on `publicView`: the telemetry and client routes need a
 * session, so on a shared dashboard they would be a column of failed requests.
 * The list is then exactly what it was before.
 */
function LowBandwidthTile({ data, siteSlug, publicView, heading }: TileProps) {
  const { t } = useTranslation();
  const tipId = useId();
  const listRef = useRef<HTMLDivElement>(null);
  const [hover, setHover] = useState<LowBandwidthHover | null>(null);

  const rows = lowBandwidthPorts(
    (data.devices ?? []) as Parameters<typeof lowBandwidthPorts>[0],
  );
  // A row carries a device; the client ledger is a SITE resource, and the
  // device payload already says which site it belongs to. Read from there and
  // not from `useSite()`, which would fetch `/api/sites` — a route a public
  // dashboard cannot reach — to learn something already in hand.
  const byId = new Map((data.devices ?? []).map((d) => [d.id, d]));

  // One hover bubble on screen, app-wide (hoverExclusive.ts): opening ours
  // dismisses a pinned QuickTip elsewhere on the dashboard, and vice versa.
  useEffect(() => registerHoverTip(tipId, () => setHover(null)), [tipId]);

  const close = () => {
    releaseHoverTip(tipId);
    setHover(null);
  };
  const open = (el: HTMLElement, r: LowBandwidthPort) => {
    claimHoverTip(tipId);
    const b = el.getBoundingClientRect();
    setHover({
      key: r.deviceId + "/" + r.port,
      deviceId: r.deviceId,
      siteId: byId.get(r.deviceId)?.site_id ?? "",
      device: r.device,
      port: r.port,
      link: r.link,
      anchorX: b.right,
      anchorY: b.top,
    });
  };

  // Escape closes the tip, and so does a mousedown outside the list — the two
  // gestures a tip opened by TAP needs, since a touch screen never fires the
  // pointer-leave that closes it for a mouse. Through the dismissal stack, so
  // Escape still belongs to whatever modal may be open above us.
  useDismissable(!!hover, close, { insideRef: listRef });

  return (
    <>
      {/* THE HELP IS GONE, AND SO IS ITS "?" (2026-10-25).
          Two paragraphs sat behind this title: one listing the causes of a
          link that negotiated 10 or 100 Mbit/s, one explaining that the curve
          is read against the negotiated speed. Both described the tile's
          reasoning rather than what the operator is looking at, and the rows
          below — a port, its negotiated speed, its load — are the observation.
          The title keeps its place in the head; nothing replaced the bubble. */}
      <div className="card-head">
        <div className="kicker">{heading}</div>
      </div>
      {rows.length === 0 ? (
        // An EMPTY tile is the good news here, and it says so rather than
        // showing nothing: a blank panel reads as "not loaded yet", which is the
        // opposite of what an empty result means on this particular tile.
        <p className="note">{t("dashboard.lowBandwidthNone")}</p>
      ) : (
        <div ref={listRef}>
          <table className="table">
            <tbody>
              {rows.map((r) => (
                <tr
                  key={r.deviceId + "/" + r.port}
                  // Three input modes, one tip. Hover for a mouse; FOCUS for a
                  // keyboard, which costs no new tab stop because the device
                  // name in the first cell is already a link; and tap for a
                  // touch screen, where `pointerenter` never fires at all.
                  //
                  // In EDIT mode none of them can fire: `dashboardGrid` puts
                  // `pointer-events: none` on the tile's content so the whole
                  // tile is a drag target, which also means a drag can never
                  // summon a tip. That is why there is no `editing` guard here.
                  onPointerEnter={
                    publicView
                      ? undefined
                      : (e) => {
                          if (e.pointerType !== "touch") open(e.currentTarget, r);
                        }
                  }
                  onPointerLeave={
                    publicView
                      ? undefined
                      : (e) => {
                          if (e.pointerType !== "touch") close();
                        }
                  }
                  onFocus={publicView ? undefined : (e) => open(e.currentTarget, r)}
                  onBlur={publicView ? undefined : close}
                  onClick={
                    publicView
                      ? undefined
                      : (e) => {
                          // Coarse pointers only: on a mouse the hover already
                          // did it, and a click would close what it opened.
                          if (window.matchMedia("(hover: hover)").matches) return;
                          // A tap on the device name is a navigation, not a
                          // request for the tip.
                          if ((e.target as Element).closest("a")) return;
                          if (hover?.key === r.deviceId + "/" + r.port) close();
                          else open(e.currentTarget, r);
                        }
                  }
                >
                  <td>
                    {publicView ? (
                      r.device
                    ) : (
                      <Link to={deviceDetailPath(siteSlug, { id: r.deviceId, name: r.device })}>
                        {r.device}
                      </Link>
                    )}
                  </td>
                  <td className="mono">{r.port}</td>
                  {/* The label VERBATIM as the device reported it: an operator
                      compares this against what the switch itself says, and a
                      re-rendered "100 Mbit/s" would not match. */}
                  <td className="warn mono">{r.link}</td>
                  {!publicView && (
                    <td className="ports-traffic-cell">
                      {/* The ramp is read against the NEGOTIATED speed, not
                          against what the cage could do. That is the opposite
                          of the Ports page and it is the point of this tile: 80
                          Mb/s on a gigabit port that came up at 100 M is 8 % of
                          the hardware and 80 % of the wire, and only the second
                          number says the limit is hurting somebody. Colouring
                          against the capability would paint every row on this
                          tile as idle — which is the one reading that makes the
                          list safe to ignore. */}
                      <PortTrafficCell
                        deviceId={r.deviceId}
                        port={r.port}
                        speed={r.link}
                        off={byId.get(r.deviceId)?.status === "off"}
                      />
                    </td>
                  )}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {!publicView && <LowBandwidthTip hover={hover} />}
    </>
  );
}

/**
 * SFP cages receiving less light than their own history ever showed.
 *
 * The JUDGEMENT is entirely server-side (opticsalerts_handlers.go — the floor,
 * the dark cutoff, the margin), because it needs the series store; this draws
 * the rows and the rule's caption from the numbers the server returned. Three
 * states, kept apart the way the telemetry panel keeps them: no store (say
 * why), loading (say nothing yet), and empty rows — which is the GOOD news,
 * said out loud, since a blank fault list reads as "not loaded".
 */
function LowRxTile({ data, siteSlug, publicView, heading }: TileProps) {
  const { t } = useTranslation();
  const optics = data.optics;
  const dsep = decimalSeparator();
  const fmt = (v: number) => v.toFixed(1).replace(".", dsep) + " dBm";
  return (
    <>
      <TileHead heading={heading} />
      {optics == null ? (
        <p className="note">{t("common.loading")}</p>
      ) : !optics.available ? (
        <p className="note">{optics.why}</p>
      ) : optics.rows.length === 0 ? (
        <p className="note">
          {t("dashboard.lowRxNone", { days: optics.window_days })}
        </p>
      ) : (
        <>
          <table className="table">
            <tbody>
              {optics.rows.map((r) => (
                <tr key={r.device_id + "/" + r.port + "/" + (r.lane ?? "")}>
                  <td>
                    {publicView ? (
                      r.device
                    ) : (
                      <Link to={deviceDetailPath(siteSlug, { id: r.device_id, name: r.device })}>
                        {r.device}
                      </Link>
                    )}
                  </td>
                  <td className="mono">
                    {r.port}
                    {r.lane && r.lane !== "0" ? " · " + r.lane : ""}
                  </td>
                  {/* Current level in warning ink, the floor beside it in
                      plain: the pair is the whole claim — "it used to never
                      go below THIS, and it is at THAT". */}
                  <td className="warn mono">{fmt(r.rx_dbm)}</td>
                  <td className="mono" style={{ color: "var(--text-dim)" }}>
                    {t("dashboard.lowRxFloor", { floor: fmt(r.floor_dbm) })}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          <div className="note" style={{ marginTop: 8 }}>
            {t("dashboard.lowRxRule", {
              margin: String(optics.margin_db).replace(".", dsep),
              days: optics.window_days,
            })}
          </div>
        </>
      )}
    </>
  );
}

/**
 * ONE PORT'S TRAFFIC, the port being whichever one the operator pointed this
 * tile at.
 *
 * # The first tile that carries a TARGET, and what that costs
 *
 * Every other tile in this file is about the site: give it the datasets and it
 * draws. This one means nothing until `options.device_id` and `options.port`
 * name something, which makes it the first tile whose options hold a REFERENCE
 * no foreign key protects — see ADR-0027, and `store.TileCatalog` for why it is
 * therefore not seeded onto a new site.
 *
 * The consequence is the bulk of this component: **four ways for the target to
 * be gone, and four different sentences**, because they call for four different
 * actions.
 *
 *	no target yet         pick a device, then a port
 *	device not on site    it was deleted or moved — re-point the tile
 *	port not on faceplate the module was pulled, or the model was re-read
 *	no sample in window   the port exists and nothing is measuring it
 *
 * None of them draws a flat line. Zero is a measurement and the absence of one
 * is not, which is the rule the whole telemetry panel is built on — so the
 * missing cases wear the same `?` in `--text-dim` that `telemetry.tsx` uses,
 * and they say which of the four it is. All four keep the selectors, so the fix
 * is in the tile rather than "delete it and add a new one".
 *
 * # The curve does NOT come from TileData
 *
 * It comes from `GET /api/devices/{id}/telemetry`, keyed per DEVICE **and per
 * window** by react-query — so two port tiles on the same switch reading the
 * same window cost one request, and a third costs none (the same arrangement
 * `PortTrafficCell` relies on). That route needs a session, which is why a
 * public dashboard gets the target line and an explanation instead of an empty
 * chart.
 *
 * # Everything configurable is in the EDIT-MODE panel (ADR-0032)
 *
 * The two selectors used to be drawn on every viewing of the dashboard, which
 * was not a taste decision: edit mode takes pointer events away from tile
 * content, so a picker that only existed there could not be opened. ADR-0032
 * lifts that by letting a tile declare one live region, and the four settings —
 * device, port, window, direction — moved into it. A dashboard being read now
 * shows a curve and nothing else.
 *
 * Because the window is no longer on screen as a row of chips, the caption
 * under the curve SAYS which window is drawn. A thirty-day chart that looks
 * like a one-day chart is the failure mode that hiding the control creates, and
 * the caption is the whole mitigation.
 */
function PortTrafficTile({
  data,
  options,
  setOption,
  siteSlug,
  publicView,
  editing,
  heading,
}: TileProps) {
  const { t } = useTranslation();
  const deviceId = stringOption(options.device_id, "");
  const port = stringOption(options.port, "");
  // The window, in hours — the SAME option name, the same four values and the
  // same `setOption` routing as the site-traffic tile, so the two windows on
  // one dashboard are one thing to learn rather than two (and the same four the
  // server's `chartStep` has a sampling step for).
  const hours = numberOption(options.hours, 24);
  // Which direction is drawn on the upper half. See `.ptt-plot--swap`: the
  // samples are NOT touched, the plot is mirrored.
  const swap = boolOption(options.swap_dir, false);

  // The plot's own box, which is what the chart is sized against — see
  // `chartFit`. Measured on a region that is ALWAYS rendered, including when
  // the tile has no target yet: the width decides how the settings panel lays
  // itself out, and the first thing anybody does to a new tile is open a
  // picker in it.
  const plotRef = useRef<HTMLDivElement>(null);
  const fit = chartFit(useBoxSize(plotRef));
  // Radio `name`s have to be unique per GROUP, and a dashboard may hold several
  // port tiles: without this, choosing 7 d on one would unselect the window on
  // every other, because the browser would see one group of sixteen.
  const pickerId = useId();

  const device = data.devices.find((d) => d.id === deviceId);
  const ports = device ? expandPorts(device) : [];
  const ref = ports.find((pr) => pr.name === port);

  // The query is asked only when there is something to ask it about, and never
  // on a public page. `usePortTelemetry` takes undefined and stays disabled,
  // so this is a condition on the FETCH and not on the hook — the hook runs
  // every render, as it must.
  //
  // It is asked even when the device is unreachable, which is the opposite of
  // what `PortTrafficCell` does, and deliberately: that cell headlines a
  // CURRENT rate, which would be a stale number on a dead device, while this
  // tile shows a window of history — and the history of the hour a port went
  // quiet is exactly what somebody opens this for. The tail of the curve is
  // empty on its own, with nothing to invent.
  const want = !publicView && device && ref ? deviceId : undefined;
  const { data: tele, isError } = usePortTelemetry(want, hours * 60);
  // The axis vocabulary follows the window for free: the server picks a coarser
  // step for a wider window and `step_seconds` comes back with the answer, so
  // "4 min ago" becomes "6 d ago" without this tile deciding anything.
  const xLabel = useTipXLabel(tele?.step_seconds ?? 0);

  const deviceOptions: SearchOption[] = data.devices.map((d) => ({
    id: d.id,
    label: d.name,
    detail: d.mgmt_ip || modelShort(modelOf(d), d.vendor),
    // Searchable but not printed (ADR-0010): the model number is what somebody
    // reads off a lid, and printing it on every row would bury the names.
    keywords: [d.model, d.vendor].filter(Boolean).join(" "),
  }));

  const portOptions: SearchOption[] = ports.map((pr) => ({
    id: pr.name,
    // `display` is the silkscreen, `name` is what the telemetry route keys on.
    // The option is LABELLED by the first and IDENTIFIED by the second, which
    // is the rule everywhere a port is both shown and addressed.
    label: pr.display,
    detail: [pr.p.label, pr.p.link || t("portTraffic.noLink")]
      .filter(Boolean)
      .join(" · "),
    keywords: [pr.name, pr.groupLabel].join(" "),
  }));

  /**
   * The tile's settings — four of them, and visible ONLY in edit mode.
   *
   * `TileSettings` is what makes that possible rather than decorative: it
   * takes back the pointer events edit mode removes and swallows the drag, so
   * a picker opens instead of nudging the tile one cell to the left
   * (ADR-0032). Everything interactive in this tile lives inside it.
   */
  const settings = (
    <TileSettings
      editing={editing}
      className={"ptt-set" + (fit.narrow ? " ptt-set--narrow" : "")}
    >
      <div className="ptt-target">
        <SearchSelect
          label={t("portTraffic.device")}
          value={deviceId}
          options={deviceOptions}
          // Changing the device CLEARS the port. A port name is only meaningful
          // on the switch it was read from — keeping "XG3" across a change would
          // either silently point at a different port or, more often, at none,
          // and the tile would report a vanished port for a target the operator
          // just set.
          onChange={(id) => setOption({ device_id: id, port: "" })}
          placeholder={t("portTraffic.pickDevice") as string}
          emptyText={t("portTraffic.noDeviceMatch") as string}
        />
        <SearchSelect
          label={t("portTraffic.port")}
          value={port}
          options={portOptions}
          onChange={(name) => setOption({ port: name })}
          placeholder={t("portTraffic.pickPort") as string}
          emptyText={t("portTraffic.noPortMatch") as string}
          disabled={!device}
        />
      </div>
      <div className="ptt-set-row">
        <div className="ptt-set-group">
          <span className="ptt-set-lbl">{t("portTraffic.window")}</span>
          {/* Radio inputs in pill labels, not a row of buttons. Four exclusive
              values IS a radio group — the platform then carries the arrow-key
              vocabulary and the "one of these" announcement for free — and the
              pill is the same shape the clean-up window on the clients screen
              uses for the same shape of question. */}
          <div
            className="ptt-pills"
            role="radiogroup"
            aria-label={t("portTraffic.window") as string}
          >
            {TRAFFIC_WINDOWS.map((o) => (
              <label key={o.h} className="ptt-pill">
                <input
                  type="radio"
                  name={"ptt-window-" + pickerId}
                  checked={o.h === hours}
                  onChange={() => setOption({ hours: o.h })}
                />
                {/* The site-traffic tile's own labels, character for character.
                    A second vocabulary for the same four windows on the same
                    dashboard is the thing the shared constant exists to
                    prevent. */}
                <span>{o.label}</span>
              </label>
            ))}
          </div>
        </div>
        <div className="ptt-set-group">
          <span className="ptt-set-lbl">
            {t("portTraffic.direction")}
            <HelpTip
              label={
                t("common.helpFor", { title: t("portTraffic.direction") }) as string
              }
            >
              <p>{t("portTraffic.swapHelp")}</p>
            </HelpTip>
          </span>
          <div className="ptt-pills">
            {/* A checkbox and not a button: this is a STATE the tile keeps, and
                a button that fires would leave the operator reading the curve
                to find out which way round it now is. Lit when engaged, which
                is the same affordance as the selected window beside it. */}
            <label className="ptt-pill">
              <input
                type="checkbox"
                checked={swap}
                onChange={(e) => setOption({ swap_dir: e.target.checked })}
              />
              <span>{t("portTraffic.swapDirections")}</span>
            </label>
          </div>
        </div>
      </div>
    </TileSettings>
  );

  /** The `?` of `telemetry.tsx`: we were never told, which is the grey case. */
  const missing = (text: string) => (
    <div className="ptt-missing">
      <span className="ptt-missing-glyph" aria-hidden>
        ?
      </span>
      <span className="note">{text}</span>
    </div>
  );

  // The head's aside names the target once it resolves — and links to it when
  // there is a session to follow the link with. It is the tile's subject line,
  // and it has to survive a RENAME: a tile titled "Server room uplink" must
  // still say which port that is.
  let aside: JSX.Element | undefined;
  if (device && ref) {
    aside = siteSlug ? (
      <Link
        className="note ptt-subject"
        to={deviceDetailPath(siteSlug, { id: device.id, name: device.name })}
      >
        {device.name} · {ref.display}
      </Link>
    ) : (
      <span className="note ptt-subject">
        {device.name} · {ref.display}
      </span>
    );
  }

  // The curve's own region, and the caption under it. Split in two because the
  // caption must NOT be inside the measured, clipped plot box: it carries the
  // rate the colour is read against (ADR-0003) and the window on screen, and
  // those are the two sentences that make the picture readable at all. The
  // chart gives up height for them rather than the other way round.
  let body: JSX.Element;
  let caption: JSX.Element | undefined;
  if (!deviceId || !port) {
    body = <p className="note">{t("portTraffic.chooseATarget")}</p>;
  } else if (!device) {
    // The device id still in the options names nothing on this site. The id is
    // NOT printed: it says nothing to the person reading, and the port name is
    // the half they can recognise.
    body = missing(t("portTraffic.deviceGone", { port }));
  } else if (!ref) {
    body = missing(t("portTraffic.portGone", { port, device: device.name }));
  } else if (publicView) {
    body = <p className="note">{t("portTraffic.needsSession")}</p>;
  } else if (isError) {
    body = missing(t("portTraffic.readFailed"));
  } else if (!tele) {
    body = <p className="note">{t("common.loading")}</p>;
  } else if (!tele.available) {
    // The server's own sentence — why there is no series store, or why this
    // device is not collected. Displayed verbatim, never rewritten.
    body = <p className="note">{tele.why}</p>;
  } else {
    const traffic = portTraffic(tele, port);
    const hasSamples =
      traffic.rx.some((v) => v != null) || traffic.tx.some((v) => v != null);
    if (!hasSamples) {
      body = missing(t("telemetry.noMeasurements"));
    } else {
      // ADR-0003, and the one line of this tile that had to be got right: the
      // ramp is laid out over the NEGOTIATED rate, not over the curve's own
      // peak and not over what the cage could do. 80 Mb/s on a gigabit port
      // that came up at 100 M is 8 % of the hardware and 80 % of the wire, and
      // only the second number says somebody is being hurt by it.
      //
      // A port with no link has negotiated nothing, so there the group's
      // capability stands in — and the caption below says which of the two is
      // in force, because a colour whose reference is unstated is a colour
      // that cannot be read.
      const negotiated = ref.p.link ?? "";
      const capacity = speedMbps(negotiated || ref.speed);
      const last = lastSampleAt(tele, port);
      body = (
        <LineChart
          series={[
            { name: t("dashboard.incoming"), color: SERIES.in, points: traffic.rx },
            // Outgoing fed NEGATIVE so the chart mirrors it under the axis
            // rather than stacking it on incoming — the sign is geometry,
            // and LineChart reads the absolute value back for its labels.
            // Same arrangement as the site traffic tile above.
            //
            // THE SIGNS DO NOT MOVE WHEN THE OPERATOR SWAPS THE DIRECTIONS,
            // and that is the part to read before changing it. LineChart
            // derives its arrows from the sign — the positive series is the
            // one it marks ↓, on the axis label and in the legend — so
            // feeding outgoing positive would draw it on top and label it
            // "↓ Outgoing", which is a chart that lies about the thing being
            // asked of it. The swap is therefore a MIRROR of the plot
            // (`.ptt-plot--swap`), where every arrow stays attached to the
            // direction it names.
            {
              name: t("dashboard.outgoing"),
              color: SERIES.out,
              points: traffic.tx.map((v) =>
                v == null || Number.isNaN(v) ? null : -v,
              ),
            },
          ]}
          format={fmtMbps}
          ramp
          rampCapacity={capacity}
          xLabel={xLabel}
          // The one thing that makes the chart follow the tile: the plot's
          // pixel height, measured rather than assumed. The default 170 is a
          // reasonable guess about nothing in particular, and in a cell the
          // operator can stretch to nine rows it left two thirds of the tile
          // blank.
          height={fit.height}
        />
      );
      caption = (
        <div className="note ptt-caption">
          {t(negotiated ? "portTraffic.rampNegotiated" : "portTraffic.rampCapability", {
            rate: fmtMbps(capacity),
          })}
          {/* The window, in prose, because the chips that used to say it are
              now behind edit mode. Without this the only difference between a
              day and a month on screen is how smooth the curve looks. */}
          {" · " + windowLabel(t as unknown as Translate, hours)}
          {/* The age of the NEWEST sample, and only when it is old. A curve
              whose last hours are empty reads as "the port went quiet"; if
              the collector simply stopped, that reading is wrong, and this
              is the one line that tells the two apart. Through `ageMs`, so
              the distance is measured in the viewer's own frame (ADR-0008)
              rather than against the controller's clock. */}
          {last && (ageMs(last) ?? 0) > STALE_SAMPLE_MS
            ? " · " + t("portTraffic.lastSample", { ago: agoLabel(last) })
            : ""}
        </div>
      );
    }
  }

  return (
    <>
      <TileHead heading={heading} aside={aside} />
      {settings}
      {/* THE MEASURED REGION. It takes the height the card has left over after
          the head, the settings panel and the caption, and gives it to the
          chart — `flex: 1 1 0` in the stylesheet, so its own height never
          depends on what is inside it and the measurement cannot chase itself.

          Rendered for every state, not just the drawn one: the absence
          sentences live here too, so the box exists (and has a width) from the
          moment a tile is added and before any target is chosen. */}
      <div
        ref={plotRef}
        className={
          "ptt-plot" +
          (fit.legend ? "" : " ptt-plot--nolegend") +
          (swap ? " ptt-plot--swap" : "")
        }
      >
        {body}
      </div>
      {caption}
    </>
  );
}

/**
 * The window, as a sentence rather than as the chip's abbreviation.
 *
 * Two keys and not four, with i18next's count plural: the four windows are
 * 24 h, 3 d, 7 d and 30 d today, and a fifth should not need a new string.
 */
function windowLabel(t: Translate, hours: number): string {
  if (hours < 48) return t("portTraffic.windowHours", { count: hours });
  return t("portTraffic.windowDays", { count: Math.round(hours / 24) });
}

/**
 * HOW THE CHART SPENDS THE TILE IT IS GIVEN.
 *
 * A dashboard cell is 84px tall and the operator can drag a tile to any whole
 * number of them, at any width from one column to twelve. So the three shapes
 * below are not hypothetical, and each gets an answer rather than being left to
 * overflow:
 *
 *   - **FLAT** (a tile one or two rows tall). The legend goes. It costs 20px of
 *     a plot that has 40 left, and it is the one part of the picture that is
 *     said twice: the axis labels already carry "↓ 1 Gb/s" at one end and
 *     "↑ 1 Gb/s" at the other, so the directions are still on screen after
 *     the legend has gone. The plot keeps a 44px floor and the card scrolls
 *     below that, exactly as it does for a table too long for its cell —
 *     a curve squeezed to fifteen pixels is not a curve.
 *   - **NARROW** (under 300px, which is three columns of a laptop). The two
 *     pickers stop sharing a line, because at 140px each neither can show
 *     "core01-arena". Under 200px the legend goes too: `.chart-legend` is a
 *     flex row with no wrap, so two entries that do not fit do not wrap — they
 *     overflow the tile.
 *   - **LARGE.** Nothing is capped. The plot takes everything the cell has
 *     left; the SVG scales with `preserveAspectRatio="none"` and its strokes
 *     are `non-scaling-stroke`, so a 700px-tall chart is the same two-pixel
 *     curve drawn taller, not a blurred one.
 *
 * The thresholds are measured against the PLOT box, never the viewport — see
 * `useBoxSize`.
 */
const PLOT_LEGEND_PX = 20;
const PLOT_FLOOR_PX = 44;
const PLOT_UNMEASURED_PX = 170;
const PLOT_LEGEND_MIN_H = 110;
const PLOT_LEGEND_MIN_W = 200;
const PLOT_NARROW_W = 300;

function chartFit(box: { w: number; h: number }) {
  // Before the observer has fired there is no measurement, and guessing small
  // would flash a squashed chart on every mount. LineChart's own default is
  // the right guess for one frame.
  if (box.h <= 0) return { height: PLOT_UNMEASURED_PX, legend: true, narrow: false };
  const withLegend = box.h - PLOT_LEGEND_PX;
  const legend = box.w >= PLOT_LEGEND_MIN_W && withLegend >= PLOT_LEGEND_MIN_H;
  return {
    height: Math.max(PLOT_FLOOR_PX, legend ? withLegend : box.h),
    legend,
    narrow: box.w < PLOT_NARROW_W,
  };
}

/**
 * When the newest sample of a port's traffic stops counting as "current".
 *
 * Fifteen minutes is far looser than any collection interval the product runs
 * (the series step is minutes at worst), and loose on purpose: this gates a
 * SENTENCE, and a threshold set near the real interval would make that
 * sentence flicker on and off between two polls.
 */
const STALE_SAMPLE_MS = 900000;

/** Timestamp of the newest traffic sample for one port, across both directions. */
function lastSampleAt(tele: DeviceTelemetry, port: string): string {
  let best = "";
  tele.ports.forEach((s) => {
    if (s.port !== port) return;
    if (s.metric !== M.octIn && s.metric !== M.octOut) return;
    const p = s.points[s.points.length - 1];
    // ISO-8601 in UTC compares correctly as a string, which is the whole reason
    // the route emits it that way — no parsing, and so no clock frame entered
    // into here at all. `ageMs` does that once, at the point of use.
    if (p && p.ts > best) best = p.ts;
  });
  return best;
}

export const TILES: Record<string, TileDef> = {
  "stat.devices-online": {
    labelKey: "dashboard.devicesOnline",
    w: 2,
    h: 1,
    framed: false,
    needs: ["devices"],
    Component: DevicesOnlineTile,
  },
  "stat.active-ports": {
    labelKey: "dashboard.activePorts",
    w: 2,
    h: 1,
    framed: false,
    needs: ["devices"],
    Component: ActivePortsTile,
  },
  "stat.rx": {
    labelKey: "dashboard.rxRate",
    w: 2,
    h: 1,
    framed: false,
    needs: ["devices"],
    Component: RxTile,
  },
  "stat.tx": {
    labelKey: "dashboard.txRate",
    w: 2,
    h: 1,
    framed: false,
    needs: ["devices"],
    Component: TxTile,
  },
  "stat.poe": {
    labelKey: "dashboard.poeDistributed",
    w: 2,
    h: 1,
    framed: false,
    needs: ["devices"],
    Component: PoeTile,
  },
  "stat.alerts": {
    labelKey: "dashboard.activeAlerts",
    w: 2,
    h: 1,
    framed: false,
    needs: ["devices", "links"],
    Component: AlertCountTile,
  },
  "chart.traffic": {
    labelKey: "dashboard.siteTraffic",
    w: 7,
    h: 3,
    framed: true,
    needs: ["traffic"],
    Component: TrafficTile,
  },
  "chart.vlan-ports": {
    labelKey: "dashboard.configuredPortsByVlan",
    w: 5,
    h: 3,
    framed: true,
    needs: ["devices", "vlans"],
    Component: VlanPortsTile,
  },
  "list.low-bandwidth": {
    labelKey: "dashboard.lowBandwidth",
    w: 6,
    h: 3,
    framed: true,
    needs: ["devices"],
    Component: LowBandwidthTile,
  },
  "list.low-rx": {
    labelKey: "dashboard.lowRx",
    w: 6,
    h: 3,
    framed: true,
    needs: ["optics"],
    Component: LowRxTile,
  },
  // Four rows rather than three: this is the only tile that carries two form
  // controls ABOVE its chart, and at h:3 the curve was left 70px — a shape with
  // no vertical range, which is the one thing a rate chart has to have.
  //
  // `needs: ["devices"]` and the curve is not in it; the per-device telemetry
  // route is. See the component, and store.TileCatalog for the public-route
  // consequence.
  "chart.port-traffic": {
    labelKey: "portTraffic.tileName",
    w: 6,
    h: 4,
    framed: true,
    needs: ["devices"],
    Component: PortTrafficTile,
  },
  "list.alerts": {
    labelKey: "dashboard.activeAlerts",
    w: 6,
    h: 3,
    framed: true,
    needs: ["devices", "links"],
    Component: AlertListTile,
  },
  "list.events": {
    labelKey: "dashboard.recentEvents",
    w: 6,
    h: 3,
    framed: true,
    needs: ["events"],
    Component: RecentEventsTile,
  },
  "stat.unreachable": {
    labelKey: "monitoringPage.unreachableDevices",
    w: 3,
    h: 1,
    framed: false,
    needs: ["devices"],
    Component: UnreachableTile,
  },
  "stat.events-24h": {
    labelKey: "monitoringPage.events24h",
    w: 3,
    h: 1,
    framed: false,
    needs: ["events"],
    Component: Events24hTile,
  },
  "stat.warnings": {
    labelKey: "monitoringPage.warningsCount",
    w: 3,
    h: 1,
    framed: false,
    needs: ["events"],
    Component: WarningsTile,
  },
  "stat.last-event": {
    labelKey: "monitoringPage.lastEvent",
    w: 3,
    h: 1,
    framed: false,
    needs: ["events"],
    Component: LastEventTile,
  },
  "table.fleet": {
    labelKey: "monitoringPage.fleetStatus",
    w: 7,
    h: 4,
    framed: true,
    needs: ["devices"],
    Component: FleetTile,
  },
  "chart.top-ports": {
    labelKey: "monitoringPage.topLoadedPorts",
    w: 5,
    h: 4,
    framed: true,
    needs: ["devices", "vlans"],
    Component: TopPortsTile,
  },
  "table.event-log": {
    labelKey: "monitoringPage.eventLog",
    w: 12,
    h: 5,
    framed: true,
    needs: ["events", "devices"],
    Component: EventLogTile,
  },
};

/** Every tile type, in the order the picker offers them. */
export const TILE_TYPES = Object.keys(TILES);

/** The datasets a set of tiles reads. Mirrors store.DatasetsFor, Go side. */
export function datasetsFor(types: string[]): Set<Dataset> {
  const out = new Set<Dataset>();
  types.forEach((type) => TILES[type]?.needs.forEach((n) => out.add(n)));
  return out;
}
