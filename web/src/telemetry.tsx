// Device telemetry panel: metrics that inventory fields cannot carry.
//
// The port facade already shows link state, negotiated rate, and per-port
// instantaneous rate. Seven series families had no screen until this panel.
//
// Three absences are distinct:
//   - no series storage on the instance → one sentence, no frame;
//   - platform does not publish the metric → « ! » tile with immediate QuickTip;
//   - device simply not collected → "no measurement", not zero.
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";

import { get, type Device, type MetricUnavailable, type SiteLldp } from "./api";
import { QuickTip } from "./components";
import { monitoringUnavailableCopy } from "./monitoringUnavailable";
import { deviceRates } from "./network";
import { LineChart, SERIES, StatTile } from "./viz";

/** One series from GET /api/devices/{id}/telemetry. */
export interface TeleSeries {
  metric: string;
  unit: string;
  sensor?: string;
  port?: string;
  last?: number;
  points: { ts: string; value: number }[];
}

export interface DeviceTelemetry {
  available: boolean;
  why?: string;
  window_seconds: number;
  step_seconds: number;
  device: TeleSeries[];
  ports: TeleSeries[];
}

/** Metric family names as internal/metricbuf defines them. */
export const M = {
  temp: "meshcraft_temperature_celsius",
  cpu: "meshcraft_cpu_load_percent",
  memFree: "meshcraft_memory_free_bytes",
  memTotal: "meshcraft_memory_total_bytes",
  uptime: "meshcraft_uptime_seconds",
  errIn: "meshcraft_if_in_errors_total",
  errOut: "meshcraft_if_out_errors_total",
  octIn: "meshcraft_if_in_octets_total",
  octOut: "meshcraft_if_out_octets_total",
};

export function useTelemetry(
  deviceId: string,
  minutes: number,
  refetchInterval?: number | false,
  enabled = true,
) {
  return useQuery({
    queryKey: ["telemetry", deviceId, minutes],
    queryFn: () =>
      get<DeviceTelemetry>(`/api/devices/${deviceId}/telemetry?minutes=${minutes}`),
    refetchInterval,
    enabled: !!deviceId && enabled,
  });
}

/** First series for a metric family (sensors are sorted). */
export function seriesOf(t: DeviceTelemetry | undefined, metric: string): TeleSeries | undefined {
  return t?.device.find((s) => s.metric === metric);
}

/** Both traffic directions for one port, as drawn on the device page. */
export function portTraffic(t: DeviceTelemetry | undefined, port: string) {
  const pick = (metric: string) =>
    t?.ports.find((s) => s.metric === metric && s.port === port)?.points.map((p) => p.value) ?? [];
  return { rx: pick(M.octIn), tx: pick(M.octOut) };
}

/** Human-readable uptime (rounded to largest meaningful step). */
export function fmtUptime(
  sec: number,
  t: (k: string, o?: Record<string, unknown>) => string,
): string {
  if (!isFinite(sec) || sec < 0) return "—";
  const d = Math.floor(sec / 86400);
  const h = Math.floor((sec % 86400) / 3600);
  const m = Math.floor((sec % 3600) / 60);
  if (d > 0) return t("telemetry.uptimeDaysHours", { d, h });
  if (h > 0) return t("telemetry.uptimeHoursMinutes", { h, m });
  return t("telemetry.uptimeMinutes", { m });
}

function fmtBytes(b: number): string {
  if (!isFinite(b) || b <= 0) return "—";
  if (b >= 1 << 30) return (b / (1 << 30)).toFixed(1).replace(".", ",") + " Gio";
  if (b >= 1 << 20) return Math.round(b / (1 << 20)) + " Mio";
  return Math.round(b / 1024) + " Kio";
}

function fmtNum(v: number, digits = 0): string {
  return v.toFixed(digits).replace(".", ",");
}

/** Unavailability declaration from the connector for one capability key. */
function gapOf(dev: Device, ...keys: string[]): MetricUnavailable | undefined {
  const declared = dev.monitoring_unavailable ?? [];
  for (const k of keys) {
    const hit = declared.find((u) => u.metric === k);
    if (hit) return hit;
  }
  return undefined;
}

/** Map a telemetry series family to capability keys the connector may declare. */
function whyMissing(dev: Device, metric: string): MetricUnavailable | undefined {
  if (metric === M.cpu) return gapOf(dev, "cpu", "health");
  if (metric === M.memFree || metric === M.memTotal) return gapOf(dev, "memory", "health");
  if (metric === M.temp) return gapOf(dev, "temperature", "health");
  if (metric === M.errIn || metric === M.errOut) return gapOf(dev, "if_counters");
  return undefined;
}

/** Panel below the port facade. */
export function TelemetryPanel({
  dev,
  tel,
  minutes,
  onMinutes,
  loading,
}: {
  dev: Device;
  tel?: DeviceTelemetry;
  minutes: number;
  onMinutes: (m: number) => void;
  loading?: boolean;
}) {
  const { t } = useTranslation();

  const { data: lldp } = useQuery({
    queryKey: ["lldp", dev.site_id],
    queryFn: () => get<SiteLldp>(`/api/sites/${dev.site_id}/lldp`),
  });

  const cpu = seriesOf(tel, M.cpu);
  const memFree = seriesOf(tel, M.memFree);
  const memTotal = seriesOf(tel, M.memTotal);
  const uptime = seriesOf(tel, M.uptime);
  const errIn = seriesOf(tel, M.errIn);
  const errOut = seriesOf(tel, M.errOut);
  const temps = (tel?.device ?? []).filter((s) => s.metric === M.temp);

  const memUsedPct =
    memFree?.last != null && memTotal?.last && memTotal.last > 0
      ? ((memTotal.last - memFree.last) / memTotal.last) * 100
      : undefined;

  const errRate = (errIn?.last ?? 0) + (errOut?.last ?? 0);
  const hasErrSeries = errIn != null || errOut != null;

  const rates = deviceRates(dev);
  const lldpGap = gapOf(dev, "lldp");
  const poeGap = gapOf(dev, "poe");
  const peerCount = (lldp?.neighbors ?? []).filter((n) => n.device_id === dev.id).length;
  const hasPoeHardware = (dev.poe_budget_w ?? 0) > 0 ||
    (dev.port_groups ?? []).some((g) => g.poe);

  const storageGone = tel != null && !tel.available;
  const nothing =
    tel != null && tel.available && tel.device.length === 0 && tel.ports.length === 0;

  return (
    <div className="tele-block">
      <div className="row" style={{ justifyContent: "space-between", alignItems: "baseline" }}>
        <span className="kicker">
          {loading ? t("telemetry.refreshing") : t("telemetry.title")}
        </span>
        <div className="row" style={{ gap: 4, flex: "none" }}>
          {(
            [
              { m: 1440, label: "24 h" },
              { m: 4320, label: "3 d" },
              { m: 10080, label: "7 d" },
              { m: 43200, label: "30 d" },
            ] as const
          ).map((o) => (
            <button
              key={o.m}
              className={"chip" + (o.m === minutes ? " on" : "")}
              onClick={() => onMinutes(o.m)}
            >
              {o.label}
            </button>
          ))}
        </div>
      </div>

      {storageGone && (
        <div className="note" style={{ marginTop: 6 }}>{tel?.why}</div>
      )}

      {nothing && !lldpGap && !poeGap && !gapOf(dev, "cpu", "memory", "health") && (
        <div className="note" style={{ marginTop: 6 }}>
          {t("telemetry.noMeasurements")}
        </div>
      )}

      <div className="tile-row" style={{ marginTop: 10, marginBottom: 12 }}>
        {temps.length > 0 ? (
          temps.map((s) => (
            <StatTile
              key={s.sensor || "temp"}
              label={
                s.sensor
                  ? t("telemetry.temperatureSensor", { sensor: s.sensor })
                  : t("telemetry.temperature")
              }
              value={
                s.last != null
                  ? fmtNum(s.last, 1) + " °C"
                  : t("telemetry.noMeasurement")
              }
              accent={
                s.last != null && s.last >= 80
                  ? "var(--danger)"
                  : s.last != null && s.last >= 70
                    ? "var(--warn)"
                    : undefined
              }
              sub={
                s.last != null && s.last >= 70
                  ? t("telemetry.above70c")
                  : t("telemetry.boardSensor")
              }
            />
          ))
        ) : (
          <CapTile label={t("telemetry.temperature")} why={whyMissing(dev, M.temp)} />
        )}
        {cpu && !whyMissing(dev, M.cpu) ? (
          <StatTile
            label={t("telemetry.cpuLoad")}
            value={
              cpu.last != null
                ? fmtNum(cpu.last) + " %"
                : t("telemetry.noMeasurement")
            }
            accent={cpu.last != null && cpu.last >= 85 ? "var(--warn)" : undefined}
          />
        ) : (
          <CapTile label={t("telemetry.cpuLoad")} why={whyMissing(dev, M.cpu)} />
        )}
        {memUsedPct != null && !whyMissing(dev, M.memFree) ? (
          <StatTile
            label={t("telemetry.memoryUsed")}
            value={fmtNum(memUsedPct) + " %"}
            sub={t("telemetry.memoryOf", {
              used: fmtBytes((memTotal?.last ?? 0) - (memFree?.last ?? 0)),
              total: fmtBytes(memTotal?.last ?? 0),
            })}
            accent={memUsedPct >= 90 ? "var(--warn)" : undefined}
          />
        ) : (
          <CapTile label={t("telemetry.memory")} why={whyMissing(dev, M.memFree)} />
        )}
        {uptime ? (
          <StatTile
            label={t("telemetry.uptime")}
            value={
              uptime.last != null
                ? fmtUptime(uptime.last, t)
                : t("telemetry.noMeasurement")
            }
            sub={t("telemetry.sinceLastReboot")}
          />
        ) : (
          <CapTile label={t("telemetry.uptime")} why={undefined} />
        )}
        {hasErrSeries ? (
          <StatTile
            label={t("telemetry.frameErrors")}
            value={fmtNum(errRate, 2) + " /s"}
            sub={t("telemetry.allPortsInOut")}
            accent={errRate > 0 ? "var(--warn)" : "var(--ok)"}
          />
        ) : (
          <CapTile label={t("telemetry.frameErrors")} why={whyMissing(dev, M.errIn)} />
        )}
        {lldpGap ? (
          <CapTile label={t("telemetry.lldpPeers")} why={lldpGap} />
        ) : (
          <StatTile
            label={t("telemetry.lldpPeers")}
            value={String(peerCount)}
            sub={t("telemetry.lldpPeersSub")}
          />
        )}
        {poeGap || !hasPoeHardware ? (
          <CapTile
            label={t("telemetry.poe")}
            why={poeGap ?? { metric: "poe", observed: true }}
          />
        ) : (
          <div className="tile">
            <div className="tile-label">{t("telemetry.poe")}</div>
            <div className="tile-value tile-poe-stack">
              <span>
                <span className="tile-poe-dir">{t("telemetry.poeOut")}</span>
                {" "}
                {fmtNum(rates.poe, rates.poe >= 10 ? 0 : 1)} W
              </span>
              <span>
                <span className="tile-poe-dir">{t("telemetry.poeIn")}</span>
                {" "}
                {(dev.poe_budget_w ?? 0) > 0
                  ? Math.round(dev.poe_budget_w) + " W"
                  : "—"}
              </span>
            </div>
            {(dev.poe_budget_w ?? 0) > 0 && (
              <div className="tile-sub">{t("telemetry.poeBudgetSub")}</div>
            )}
          </div>
        )}
      </div>

      {!storageGone && !nothing && (
        <div className="tele-charts">
          {temps.length > 0 && (
            <TeleChart
              title={t("telemetry.temperature")}
              unit="°C"
              series={temps.map((s, i) => ({
                name: s.sensor || t("telemetry.board"),
                color: i === 0 ? SERIES.out : SERIES.third,
                points: s.points.map((p) => p.value),
              }))}
              stepSeconds={tel?.step_seconds ?? 0}
              digits={1}
            />
          )}
          {(cpu || memUsedPct != null) && (
            <TeleChart
              title={t("telemetry.cpuAndMemory")}
              unit="%"
              series={[
                ...(cpu
                  ? [{
                      name: t("telemetry.processor"),
                      color: SERIES.in,
                      points: cpu.points.map((p) => p.value),
                    }]
                  : []),
                ...(memFree && memTotal && memTotal.points.length === memFree.points.length
                  ? [{
                      name: t("telemetry.memory"),
                      color: SERIES.third,
                      points: memFree.points.map((p, i) => {
                        const tot = memTotal.points[i]?.value ?? 0;
                        return tot > 0 ? ((tot - p.value) / tot) * 100 : 0;
                      }),
                    }]
                  : []),
              ]}
              stepSeconds={tel?.step_seconds ?? 0}
            />
          )}
          {hasErrSeries && (
            <TeleChart
              title={t("telemetry.frameErrorsAllPorts")}
              unit="/s"
              series={[
                ...(errIn
                  ? [{ name: t("telemetry.incoming"), color: SERIES.in, points: errIn.points.map((p) => p.value) }]
                  : []),
                ...(errOut
                  ? [{ name: t("telemetry.outgoing"), color: SERIES.out, points: errOut.points.map((p) => p.value) }]
                  : []),
              ]}
              stepSeconds={tel?.step_seconds ?? 0}
              digits={2}
            />
          )}
        </div>
      )}
    </div>
  );
}

/**
 * Capability tile: when the connector declares the metric unavailable, the whole
 * box shows « ! » and the localized reason opens immediately via QuickTip —
 * native `title` waited ~1 s and felt broken. Without a declaration, a soft
 * empty state (collection simply missing).
 */
function CapTile({
  label,
  why,
}: {
  label: string;
  why?: MetricUnavailable;
}) {
  const { t } = useTranslation();
  if (why) {
    const tip = monitoringUnavailableCopy(t, why.metric) +
      (why.observed === false ? " " + t("telemetry.deducedNotObserved") : "");
    return (
      <QuickTip text={tip} className="tile tile-unavailable">
        <div className="tile-label">{label}</div>
        <div className="tile-value tile-bang" aria-label={tip}>!</div>
      </QuickTip>
    );
  }
  return (
    <div className="tile tile-missing">
      <div className="tile-label">{label}</div>
      <div className="tile-value" style={{ fontSize: 15, color: "var(--text-dim)" }}>
        {t("telemetry.noMeasurement")}
      </div>
      <div className="tile-sub">{t("telemetry.noSeriesReason")}</div>
    </div>
  );
}

function TeleChart({
  title,
  unit,
  series,
  stepSeconds,
  digits = 0,
}: {
  title: string;
  unit: string;
  series: { name: string; color: string; points: number[] }[];
  stepSeconds: number;
  digits?: number;
}) {
  const { t } = useTranslation();
  if (series.every((s) => s.points.length === 0)) return null;
  return (
    <div>
      <div className="kicker" style={{ marginBottom: 4 }}>{title}</div>
      <LineChart
        height={132}
        series={series}
        format={(v) => fmtNum(v, digits) + " " + unit}
        xLabel={(i, n) => {
          if (i === n - 1) return t("telemetry.now");
          const back = (n - 1 - i) * stepSeconds;
          if (back < 90) return t("telemetry.secondsAgo", { n: Math.round(back) });
          if (back < 5400) return t("telemetry.minutesAgo", { n: Math.round(back / 60) });
          if (back < 48 * 3600) return t("telemetry.hoursAgo", { n: Math.round(back / 3600) });
          return t("telemetry.daysAgo", { n: Math.round(back / 86400) });
        }}
      />
    </div>
  );
}
