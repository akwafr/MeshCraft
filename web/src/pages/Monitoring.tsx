import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router-dom";
import { sitePath } from "../paths";
import { useSite, useSiteId } from "../useSite";
import { useSiteLive, get, type Device, type Event, type Vlan } from "../api";
import { ErrorMsg, HelpTip, QuickTip } from "../components";
import {
  deviceRates,
  expandPorts,
  fmtMbps,
  reachability,
  vlanMap,
} from "../network";
import { modelShort } from "../modelLabel";
import { StatusDot } from "../netui";
import { BarList, StatTile } from "../viz";

export default function Monitoring() {
  const { t, i18n } = useTranslation();
  const { site } = useSite();
  const siteId = useSiteId();
  const siteSlug = site?.slug ?? "";
  const [sevMin, setSevMin] = useState(0);
  const [type, setType] = useState("");
  // Real-time: this screen stays open on a wall display. It must follow the
  // fleet without being asked, without hammering the controller every ten
  // seconds when nothing moves for hours.
  const refetchInterval = useSiteLive(siteId);

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
  const {
    data: events,
    error,
    isFetching,
  } = useQuery({
    queryKey: ["events", siteId, sevMin, type],
    queryFn: () =>
      get<Event[]>(
        `/api/sites/${siteId}/events?limit=300&severity_min=${sevMin}` +
          (type ? `&type=${encodeURIComponent(type)}` : ""),
      ),
    enabled: !!siteId,
    // The log too: a line inserted by the controller — reachability transition,
    // config push — triggers a site notification.
    refetchInterval,
  });
  const vm = vlanMap(vlans);
  const byId = new Map((devices ?? []).map((d) => [d.id, d]));

  // Top ports by incoming rate; full-trunk uplinks excluded — their traffic is
  // already counted on the access ports facing them.
  const topPorts = (devices ?? [])
    .flatMap((d, di) =>
      expandPorts(d)
        .map((pr, pi) => ({ d, pr, di, pi }))
        .filter(
          (x) =>
            x.pr.p.link &&
            !(x.pr.p.mode === "trunk" && x.pr.p.tagged === "all") &&
            x.d.status !== "off",
        ),
    )
    .map((x) => {
      const vid = x.pr.p.mode === "access" ? x.pr.p.pvid : x.pr.p.pvid;
      return {
        key: x.d.id + ":" + x.pr.name,
        label: (x.pr.p.label || x.pr.name) + " · " + x.d.name,
        sub: x.pr.name,
        value: x.pr.p.rx_mbps ?? 0,
        color: vid != null ? vm.get(vid)?.color : undefined,
      };
    })
    .filter((r) => r.value > 0)
    .sort((a, b) => b.value - a.value)
    .slice(0, 10);

  const last24 = (events ?? []).filter(
    (e) => Date.now() - new Date(e.ts).getTime() < 86400000,
  );
  const warnCount = last24.filter((e) => e.severity >= 3).length;
  const unreachable = (devices ?? []).filter((d) => d.status === "off").length;

  return (
    <>
      <div className="toolbar">
        <h1>
          {t("monitoringPage.title")}
          <span className="sub">
            {isFetching
              ? t("monitoringPage.subtitleRefreshing")
              : t("monitoringPage.subtitleLive")}
          </span>
        </h1>
      </div>

      <div className="tile-row">
        <StatTile
          label={t("monitoringPage.unreachableDevices")}
          value={String(unreachable)}
          accent={unreachable ? "var(--danger)" : "var(--ok)"}
          sub={t("monitoringPage.supervisedOf", { count: (devices ?? []).length })}
        />
        <StatTile
          label={t("monitoringPage.events24h")}
          value={String(last24.length)}
          sub={t("monitoringPage.inCurrentFilter")}
        />
        <StatTile
          label={t("monitoringPage.warningsCount")}
          value={String(warnCount)}
          accent={warnCount ? "var(--warn)" : undefined}
          sub={t("monitoringPage.severityGteWarning")}
        />
        <StatTile
          label={t("monitoringPage.lastEvent")}
          value={
            events && events.length
              ? new Date(events[0].ts).toLocaleTimeString(i18n.language, {
                  hour: "2-digit",
                  minute: "2-digit",
                  second: "2-digit",
                })
              : "—"
          }
          sub={
            events && events.length
              ? events[0].type
              : t("monitoringPage.emptyLog")
          }
        />
      </div>

      <div className="dash-grid">
        <div className="card">
          <div className="card-head">
            <div className="kicker">{t("monitoringPage.fleetStatus")}</div>
            <span className="note">{t("monitoringPage.snmpPollingNote")}</span>
          </div>
          <div style={{ overflowX: "auto" }}>
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
                {(devices ?? []).map((d) => {
                  const r = deviceRates(d);
                  const off = d.status === "off";
                  const pct = d.poe_budget_w
                    ? Math.min(100, Math.round((r.poe / d.poe_budget_w) * 100))
                    : 0;
                  return (
                    <tr key={d.id}>
                      <td>
                        <Link to={sitePath(siteSlug, "equipment", d.name)}>{d.name}</Link>
                        <QuickTip
                          text={!d.mgmt_ip && d.model ? d.model : undefined}
                          className="quicktip-block"
                        >
                          <div className="note">
                            {d.mgmt_ip || modelShort(d.model, d.vendor)}
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
                {(devices ?? []).length === 0 && (
                  <tr>
                    <td colSpan={5} className="empty">
                      {t("monitoringPage.noDevices")}
                    </td>
                  </tr>
                )}
              </tbody>
            </table>
          </div>
        </div>

        <div className="card">
          <div className="card-head">
            <div className="kicker">{t("monitoringPage.topLoadedPorts")}</div>
            <span className="note">{t("monitoringPage.incomingRate")}</span>
          </div>
          <BarList rows={topPorts} format={fmtMbps} />
        </div>
      </div>

      <div className="card">
        <div className="card-head">
          <div className="kicker">
            {t("monitoringPage.eventLog")}
            <HelpTip label={t("monitoringPage.helpLabel")}>
              <p>{t("monitoringPage.help1")}</p>
              <p>{t("monitoringPage.help2")}</p>
            </HelpTip>
          </div>
          <div className="row" style={{ gap: 8 }}>
            <select
              style={{ width: 170 }}
              value={type}
              onChange={(e) => setType(e.target.value)}
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
              onChange={(e) => setSevMin(Number(e.target.value))}
            >
              <option value={0}>{t("monitoringPage.allSeverities")}</option>
              <option value={2}>{t("monitoringPage.noticeAndAbove")}</option>
              <option value={3}>{t("monitoringPage.warningsAndAbove")}</option>
              <option value={4}>{t("monitoringPage.errorsOnly")}</option>
            </select>
          </div>
        </div>
        <ErrorMsg error={error} />
        <div style={{ overflowX: "auto" }}>
          <table className="journal">
            <thead>
              <tr>
                <th style={{ width: 130 }}>{t("monitoringPage.timestampCol")}</th>
                <th style={{ width: 90 }}>{t("monitoringPage.severityCol")}</th>
                <th style={{ width: 150 }}>{t("monitoringPage.typeCol")}</th>
                <th>{t("monitoringPage.messageCol")}</th>
                <th style={{ width: 170 }}>{t("monitoringPage.authorCol")}</th>
              </tr>
            </thead>
            <tbody>
              {(events ?? []).map((ev) => {
                const sev = SEVERITIES[ev.severity] ?? SEVERITIES[1];
                const dev = ev.device_id ? byId.get(ev.device_id) : undefined;
                return (
                  <tr key={ev.id}>
                    <td className="note" style={{ whiteSpace: "nowrap" }}>
                      {new Date(ev.ts).toLocaleString(i18n.language, {
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
                      {dev ? (
                        <Link to={sitePath(siteSlug, "equipment", dev.name)}>{ev.message}</Link>
                      ) : (
                        ev.message
                      )}
                    </td>
                    <td className="note">{ev.actor || t("monitoringPage.systemActor")}</td>
                  </tr>
                );
              })}
              {(events ?? []).length === 0 && (
                <tr>
                  <td colSpan={5} className="empty">
                    {t("monitoringPage.noEventsForFilter")}
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </div>
      </div>
    </>
  );
}
