import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Link, useNavigate } from "react-router-dom";
import { sitePath } from "../paths";
import { useSite, useSiteId } from "../useSite";
import {
  useSiteLive,
  get,
  type Device,
  type DeviceLink,
  type Event,
  type ProxyInfo,
  type TrafficSeries,
  type Vlan,
} from "../api";
import {
  expandPorts,
  fmtMbps,
  serves,
  siteRates,
  statusColor,
} from "../network";
import { BarList, LineChart, SERIES, StatTile } from "../viz";

export default function Dashboard() {
  const { t, i18n } = useTranslation();
  const { site } = useSite();
  const siteId = useSiteId();
  const siteSlug = site?.slug ?? "";
  const navigate = useNavigate();
  // Real-time: the dashboard is a home screen kept open.
  const refetchInterval = useSiteLive(siteId);

  const { data: devices } = useQuery({
    queryKey: ["devices", siteId],
    queryFn: () => get<Device[]>(`/api/sites/${siteId}/devices`),
    enabled: !!siteId,
    refetchInterval,
  });
  const { data: vlans } = useQuery({
    queryKey: ["vlans", siteId],
    queryFn: () => get<Vlan[]>(`/api/sites/${siteId}/vlans`),
    enabled: !!siteId,
  });
  const { data: links } = useQuery({
    queryKey: ["links", siteId],
    queryFn: () => get<DeviceLink[]>(`/api/sites/${siteId}/links`),
    enabled: !!siteId,
    refetchInterval,
  });
  const { data: events } = useQuery({
    queryKey: ["events", siteId, "recent"],
    queryFn: () => get<Event[]>(`/api/sites/${siteId}/events?limit=6`),
    enabled: !!siteId,
    refetchInterval,
  });
  // Proxies are an instance resource: no filtering by site.
  const { data: proxies } = useQuery({
    queryKey: ["proxies"],
    queryFn: () => get<ProxyInfo[]>("/api/proxies"),
  });

  // Site aggregates.
  let online = 0,
    ports = 0,
    portsUp = 0,
    rx = 0,
    tx = 0,
    poe = 0,
    poeBudget = 0;
  {
    // Site totals go through siteRates rather than a sum of deviceRates: at
    // the SITE level, inter-switch links must be excluded from the sum,
    // otherwise a frame crossing two devices is counted twice. On a device
    // SHEET, on the contrary, they count (see the comments of both functions).
    const rates = siteRates(devices);
    ports = rates.ports;
    portsUp = rates.up;
    rx = rates.rx;
    tx = rates.tx;
    poe = rates.poe;
    poeBudget = rates.poeBudget;
    online = rates.online;
  }

  // Sliding traffic window — default 24 h. Point density is chosen server-side
  // from the window (1 min / 5 min / 10 min / 15 min) so every chip stays dense.
  const [trafficHours, setTrafficHours] = useState(24);
  const { data: traffic } = useQuery({
    queryKey: ["traffic", siteId, trafficHours],
    queryFn: () =>
      get<TrafficSeries>(
        `/api/sites/${siteId}/traffic?hours=${trafficHours}`,
      ),
    enabled: !!siteId,
    refetchInterval: 60000,
  });
  const tp = traffic?.points ?? [];
  const hasSamples = tp.some((p) => p.rx_mbps != null || p.tx_mbps != null);
  // Keep the full time grid even when values are null — empty stretches stay empty
  // instead of compressing later samples across the whole width.
  const rxHist: (number | null)[] = tp.map((p) => p.rx_mbps);
  const txHist: (number | null)[] = tp.map((p) => p.tx_mbps);
  const hourLabel = (i: number) => {
    const p = tp[i];
    if (!p) return "";
    const d = new Date(p.ts);
    if (trafficHours <= 24) {
      return d.toLocaleTimeString(i18n.language, { hour: "2-digit", minute: "2-digit" });
    }
    return d.toLocaleDateString(i18n.language, { month: "short", day: "numeric" });
  };

  // Alerts: degraded devices + faulty links + offline proxies.
  const byId = new Map((devices ?? []).map((d) => [d.id, d]));
  const alerts: { color: string; text: string; onOpen?: () => void }[] = [];
  (devices ?? []).forEach((d) => {
    if (d.status === "off" || d.status === "warn")
      alerts.push({
        color: statusColor(d.status),
        text:
          d.name +
          (d.status === "off" ? ` ${t("dashboard.unreachableSuffix")}` : ` ${t("dashboard.alertSuffix")}`) +
          (d.status_note ? " — " + d.status_note : ""),
        onOpen: () => navigate(sitePath(siteSlug, "equipment", d.name)),
      });
  });
  (links ?? []).forEach((l) => {
    if (l.status !== "up" || l.note) {
      const a = byId.get(l.a_device)?.name ?? "?";
      const b = byId.get(l.b_device)?.name ?? "?";
      alerts.push({
        color: l.status === "down" ? "var(--danger)" : "var(--warn)",
        text: t("dashboard.linkAlert", { a, b, cut: l.status === "down" ? t("dashboard.cutSuffix") : "", note: l.note ? " — " + l.note : "" }),
        onOpen: () => navigate(sitePath(siteSlug, "topology")),
      });
    }
  });
  (proxies ?? []).forEach((p) => {
    if (p.internal) return;
    const stale = !p.last_seen || Date.now() - new Date(p.last_seen).getTime() > 120000;
    if (!p.migrated_at && !p.revoked && stale)
      alerts.push({
        color: "var(--warn)",
        text: t("dashboard.proxyNoHeartbeat", { name: p.name }),
        onOpen: () => navigate("/config/proxy"),
      });
  });

  // Configured ports per VLAN (top 8).
  const vlanRows = (vlans ?? [])
    .map((v) => {
      let n = 0;
      (devices ?? []).forEach((d) =>
        expandPorts(d).forEach((pr) => {
          if (pr.p.mode && pr.p.mode !== "libre" && serves(pr.p, v.vlan_id)) n++;
        }),
      );
      return { key: v.id, label: v.name, sub: String(v.vlan_id), value: n, color: v.color };
    })
    .filter((r) => r.value > 0)
    .sort((a, b) => b.value - a.value)
    .slice(0, 8);

  const total = (devices ?? []).length;
  const poePct = poeBudget ? Math.round((poe / poeBudget) * 100) : 0;

  return (
    <>
      <div className="toolbar">
        <h1>
          {t("nav.dashboard")}
          <span className="sub">{t("dashboard.subtitle")}</span>
        </h1>
      </div>

      <div className="tile-row">
        <StatTile
          label={t("dashboard.devicesOnline") as string}
          value={`${online} / ${total}`}
          dotColor={online === total ? "var(--ok)" : online === 0 ? "var(--danger)" : "var(--warn)"}
          sub={total ? t("dashboard.offlineCount", { count: total - online }) as string : t("dashboard.noDevice") as string}
        />
        <StatTile label={t("dashboard.activePorts") as string} value={String(portsUp)} sub={t("dashboard.outOfPorts", { count: ports }) as string} />
        <StatTile label={t("dashboard.rxRate") as string} value={fmtMbps(rx)} sub={t("dashboard.siteCumulative") as string} />
        <StatTile label={t("dashboard.txRate") as string} value={fmtMbps(tx)} sub={t("dashboard.siteCumulative") as string} />
        <StatTile
          label={t("dashboard.poeDistributed") as string}
          value={Math.round(poe) + " W"}
          sub={poeBudget ? (t("dashboard.poePercentOf", { pct: poePct, budget: poeBudget }) as string) : (t("dashboard.noBudgetDeclared") as string)}
          accent={poePct >= 85 ? "var(--warn)" : undefined}
        />
        <StatTile
          label={t("dashboard.activeAlerts") as string}
          value={String(alerts.length)}
          accent={alerts.length ? "var(--danger)" : "var(--ok)"}
          sub={alerts.length ? (t("dashboard.toHandle") as string) : (t("dashboard.allNominal") as string)}
        />
      </div>

      <div className="dash-grid">
        <div className="card">
          <div className="card-head">
            <div className="kicker">{t("dashboard.siteTraffic")}</div>
            <div className="row" style={{ gap: 4, flex: "none" }}>
              {(
                [
                  { h: 24, label: "24 h" },
                  { h: 72, label: "3 d" },
                  { h: 168, label: "7 d" },
                  { h: 720, label: "30 d" },
                ] as const
              ).map((o) => (
                <button
                  key={o.h}
                  className={"chip" + (o.h === trafficHours ? " on" : "")}
                  onClick={() => setTrafficHours(o.h)}
                >
                  {o.label}
                </button>
              ))}
            </div>
          </div>
          <div className="note" style={{ marginBottom: 8 }}>
            {hasSamples ? t("dashboard.measured") : t("dashboard.noHistoryInWindow")}
          </div>
          <LineChart
            series={[
              { name: t("dashboard.incoming"), color: SERIES.in, points: rxHist },
              { name: t("dashboard.outgoing"), color: SERIES.out, points: txHist },
            ]}
            format={fmtMbps}
            xLabel={(i, n) => (i === n - 1 ? t("dashboard.now") : hourLabel(i))}
          />        </div>
        <div className="card">
          <div className="card-head">
            <div className="kicker">{t("dashboard.configuredPortsByVlan")}</div>
            <span className="note">{t("dashboard.topEight")}</span>
          </div>
          <BarList rows={vlanRows} format={(v) => t("dashboard.portsCount", { count: v }) as string} />
        </div>
      </div>

      <div className="dash-grid">
        <div className="card">
          <div className="card-head">
            <div className="kicker">{t("dashboard.activeAlerts")}</div>
            <Link className="note" to={sitePath(siteSlug, "monitoring")}>
              {t("nav.monitoring")} →
            </Link>
          </div>
          {alerts.length === 0 ? (
            <div className="empty">{t("dashboard.noAlerts")}</div>
          ) : (
            <div className="alert-list" style={{ margin: 0 }}>
              {alerts.map((al, i) => (
                <button key={i} className="alert-row" onClick={al.onOpen}>
                  <span className="dot" style={{ background: al.color }} />
                  <span>{al.text}</span>
                  <span className="note" style={{ marginLeft: "auto", letterSpacing: ".06em" }}>
                    {t("dashboard.viewArrow")}
                  </span>
                </button>
              ))}
            </div>
          )}
        </div>
        <div className="card">
          <div className="card-head">
            <div className="kicker">{t("dashboard.recentEvents")}</div>
            <Link className="note" to={sitePath(siteSlug, "monitoring")}>
              {t("dashboard.fullLog")} →
            </Link>
          </div>
          {(events ?? []).length === 0 ? (
            <div className="empty">{t("dashboard.emptyLog")}</div>
          ) : (
            <div className="ev-compact">
              {(events ?? []).map((ev) => (
                <div key={ev.id} className="ev-compact-row">
                  <span className="note ev-time">
                    {new Date(ev.ts).toLocaleTimeString(i18n.language, {
                      hour: "2-digit",
                      minute: "2-digit",
                    })}
                  </span>
                  <span>{ev.message}</span>
                </div>
              ))}
            </div>
          )}
        </div>
      </div>
    </>
  );
}
