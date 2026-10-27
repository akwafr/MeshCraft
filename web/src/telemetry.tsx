// Device telemetry panel: metrics that inventory fields cannot carry.
//
// The port facade already shows link state, negotiated rate, and per-port
// instantaneous rate. Seven series families had no screen until this panel.
//
// Three absences are distinct:
//   - no series storage on the instance → one sentence, no frame;
//   - platform does not publish the metric → "!" tile with immediate QuickTip;
//   - device simply not collected → "no measurement", not zero.
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";

import { get, type Device, type MetricUnavailable, type SiteLldp } from "./api";
import i18n from "./i18n";
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
  /** Transceiver channel on optical families ("0"‥"3"); absent = single reading. */
  lane?: string;
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
  mcastIn: "meshcraft_if_in_multicast_packets_total",
  mcastOut: "meshcraft_if_out_multicast_packets_total",
  bcastIn: "meshcraft_if_in_broadcast_packets_total",
  bcastOut: "meshcraft_if_out_broadcast_packets_total",
  sfpRx: "meshcraft_sfp_rx_power_dbm",
  sfpTx: "meshcraft_sfp_tx_power_dbm",
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

/** Align sparse timestamped samples onto a fixed chart grid; null = empty slot. */
export function densifyPoints(
  points: { ts: string; value: number }[],
  stepSeconds: number,
  windowSeconds: number,
): (number | null)[] {
  if (stepSeconds <= 0 || windowSeconds <= 0) {
    return points.map((p) => p.value);
  }
  var n = Math.floor(windowSeconds / stepSeconds) + 1;
  if (n < 2) n = 2;
  var endMs = Date.now();
  if (points.length > 0) {
    var last = Date.parse(points[points.length - 1].ts);
    if (!Number.isNaN(last)) endMs = last;
  }
  var halfMs = (stepSeconds * 1000) / 2;
  var samples = points
    .map((p) => ({ t: Date.parse(p.ts), v: p.value }))
    .filter((p) => !Number.isNaN(p.t));
  var out: (number | null)[] = [];
  var i = 0;
  for (i = 0; i < n; i++) {
    var want = endMs - (n - 1 - i) * stepSeconds * 1000;
    var best: number | null = null;
    var bestDist = halfMs + 1;
    var j = 0;
    for (j = 0; j < samples.length; j++) {
      var d = Math.abs(samples[j].t - want);
      if (d <= halfMs && d < bestDist) {
        bestDist = d;
        best = samples[j].v;
      }
    }
    out.push(best);
  }
  return out;
}

/** Both traffic directions for one port, as drawn on the device page. */
export function portTraffic(t: DeviceTelemetry | undefined, port: string) {
  var step = t?.step_seconds ?? 0;
  var window = t?.window_seconds ?? 0;
  var densify = (metric: string) => {
    var raw = t?.ports.find((s) => s.metric === metric && s.port === port)?.points ?? [];
    return densifyPoints(raw, step, window);
  };
  return { rx: densify(M.octIn), tx: densify(M.octOut) };
}

/**
 * The traffic classes the port overlay stacks, in stack order (bottom first).
 *
 * Same spelling as the wire — `mdpv1.StormClass`, the classes the storm
 * limiter polices — because this graph is that function's observation side and
 * a second spelling of "broadcast" is how the two drift apart.
 */
export const TRAFFIC_CLASSES = ["multicast", "broadcast", "unknown_unicast"] as const;
export type TrafficClassKey = (typeof TRAFFIC_CLASSES)[number];

/** `traffic_class_caps` as the device payload carries it (ADR-0028). */
export interface TrafficClassCaps {
  known: boolean;
  multicast: boolean;
  broadcast: boolean;
  unknown_unicast: boolean;
}

/**
 * WHY A BAND HAS NOTHING TO DRAW, which is the distinction this whole feature
 * turns on (ADR-0028). Three different silences, and collapsing any two of
 * them puts a lie on the screen:
 *
 *   ok          — samples arrived, draw the band;
 *   pending     — the platform DECLARES this class and nothing has arrived
 *                 yet: young collection, or a genuinely silent port. May fill
 *                 in at any moment;
 *   undeclared  — the platform cannot count this class, ever. Permanent;
 *   capsUnknown — no connector resolved, so we cannot even say which of the
 *                 two it is. Never render this as `undeclared`: "we cannot
 *                 tell" is not "the platform says no".
 */
export type PortClassState = "ok" | "pending" | "undeclared" | "capsUnknown";

export interface PortClassBand {
  key: TrafficClassKey;
  state: PortClassState;
  /** Densified samples; empty unless `state === "ok"`. */
  points: (number | null)[];
}

/**
 * One band per traffic class for one port, INBOUND.
 *
 * Inbound and not both directions, deliberately. The question the stack
 * answers is "how much of what this port RECEIVES is flooding", which is the
 * same quantity a storm limiter acts on — ingress is what the limiter polices
 * (cf. wire/mdpv1/stormcontrol.go). The egress series are collected too and
 * are what would answer the different question "is this switch SOURCING a
 * storm", but putting both on one stack would double every band and make the
 * proportion — the thing being read — unreadable.
 *
 * A class is reported as `undeclared` from the CAPABILITY, never from the
 * absence of samples. That is the entire point: an empty series cannot tell
 * "collection just started" from "this platform will never produce it", and
 * guessing turns a permanent platform limit into a transient-looking gap.
 */
export function portClasses(
  t: DeviceTelemetry | undefined,
  port: string,
  caps: TrafficClassCaps | undefined,
): PortClassBand[] {
  var step = t?.step_seconds ?? 0;
  var window = t?.window_seconds ?? 0;
  var metricFor: Record<TrafficClassKey, string | null> = {
    multicast: M.mcastIn,
    broadcast: M.bcastIn,
    // No series exists, and that is not an oversight — no platform counts
    // FORWARDED unknown unicast (ADR-0028). The band is still listed so the
    // overlay can say so out loud.
    unknown_unicast: null,
  };
  var out: PortClassBand[] = [];
  var k = 0;
  for (k = 0; k < TRAFFIC_CLASSES.length; k++) {
    var key = TRAFFIC_CLASSES[k];
    if (!caps || !caps.known) {
      out.push({ key, state: "capsUnknown", points: [] });
      continue;
    }
    if (!caps[key]) {
      out.push({ key, state: "undeclared", points: [] });
      continue;
    }
    var metric = metricFor[key];
    var raw = metric
      ? (t?.ports.find((s) => s.metric === metric && s.port === port)?.points ?? [])
      : [];
    var pts = densifyPoints(raw, step, window);
    // Declared, but nothing in the window. NOT "undeclared" — the platform
    // said it can, so this may fill in.
    if (pts.length === 0 || pts.every((v) => v == null)) {
      out.push({ key, state: "pending", points: [] });
      continue;
    }
    out.push({ key, state: "ok", points: pts });
  }
  return out;
}

/** One optical level line, densified onto the chart grid. */
export interface PortLevelLine {
  /** RX or TX. */
  dir: "rx" | "tx";
  /** Transceiver channel — "" and "0" are both the single-lane case. */
  lane: string;
  points: (number | null)[];
}

/**
 * The optical levels of one port, one line per (direction, lane).
 *
 * Lanes are kept apart for the same reason the collector labels them: a
 * four-lane QSFP is four fibres, and folding them would average a dead one
 * into three healthy ones. On the overwhelmingly common single-lane module
 * this returns exactly two lines (RX, TX).
 */
export function portLevels(
  t: DeviceTelemetry | undefined,
  port: string,
): PortLevelLine[] {
  var step = t?.step_seconds ?? 0;
  var window = t?.window_seconds ?? 0;
  var out: PortLevelLine[] = [];
  var collect = (metric: string, dir: "rx" | "tx") => {
    for (var s of t?.ports ?? []) {
      if (s.metric !== metric || s.port !== port) continue;
      var pts = densifyPoints(s.points, step, window);
      if (pts.every((v) => v == null)) continue;
      out.push({ dir, lane: s.lane ?? "", points: pts });
    }
  };
  collect(M.sfpRx, "rx");
  collect(M.sfpTx, "tx");
  return out;
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
  if (b >= 1 << 30) return fmtNum(b / (1 << 30), 1) + " GiB";
  if (b >= 1 << 20) return fmtNum(b / (1 << 20)) + " MiB";
  return fmtNum(b / 1024) + " KiB";
}

// The decimal separator follows the UI language, not the author's.
function fmtNum(v: number, digits = 0): string {
  return v.toLocaleString(i18n.language, { minimumFractionDigits: digits, maximumFractionDigits: digits });
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
                points: s.points,
              }))}
              stepSeconds={tel?.step_seconds ?? 0}
              windowSeconds={tel?.window_seconds ?? 0}
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
                      points: cpu.points,
                    }]
                  : []),
                ...(memFree && memTotal
                  ? [{
                      name: t("telemetry.memory"),
                      color: SERIES.third,
                      // Pair by timestamp on the densified grid via combined series:
                      // build % used points from free/total at matching times.
                      points: (() => {
                        const step = tel?.step_seconds ?? 0;
                        const win = tel?.window_seconds ?? 0;
                        const free = densifyPoints(memFree.points, step, win);
                        const tot = densifyPoints(memTotal.points, step, win);
                        const out: { ts: string; value: number }[] = [];
                        const end = Date.now();
                        free.forEach((f, i) => {
                          const tval = tot[i];
                          if (f == null || tval == null || tval <= 0) return;
                          const ts = new Date(end - (free.length - 1 - i) * step * 1000).toISOString();
                          out.push({ ts, value: ((tval - f) / tval) * 100 });
                        });
                        return out;
                      })(),
                    }]
                  : []),
              ]}
              stepSeconds={tel?.step_seconds ?? 0}
              windowSeconds={tel?.window_seconds ?? 0}
            />
          )}
          {hasErrSeries && (
            <TeleChart
              title={t("telemetry.frameErrorsAllPorts")}
              unit="/s"
              series={[
                ...(errIn
                  ? [{ name: t("telemetry.incoming"), color: SERIES.in, points: errIn.points }]
                  : []),
                ...(errOut
                  ? [{ name: t("telemetry.outgoing"), color: SERIES.out, points: errOut.points }]
                  : []),
              ]}
              stepSeconds={tel?.step_seconds ?? 0}
              windowSeconds={tel?.window_seconds ?? 0}
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
 * box shows "!" and the localized reason opens immediately via QuickTip —
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
  // NOTHING ARRIVED, AND THE REASON IS AN EXPLANATION — so it goes behind
  // the hover, not onto the tile. It used to print two lines of prose in a
  // 150px box ("No series arrived for this metric, and the connector does
  // not say whether the platform publishes it"), which wrapped four times
  // and said, at length, that there was nothing to say.
  //
  // The glyph is a QUESTION MARK and the ink is --text-dim, not
  // --indeterminate: UI-GUIDE §2 reserves the yellow for "we had a vantage
  // point and lost it". This is "we have never been told", which is the
  // grey case — the one the guide names explicitly.
  return (
    <QuickTip text={t("telemetry.noSeriesReason")} className="tile tile-missing">
      <div className="tile-label">{label}</div>
      <div className="tile-value tile-unknown" aria-label={t("telemetry.noMeasurement")}>
        ?
      </div>
    </QuickTip>
  );
}

function TeleChart({
  title,
  unit,
  series,
  stepSeconds,
  windowSeconds,
  digits = 0,
}: {
  title: string;
  unit: string;
  series: { name: string; color: string; points: { ts: string; value: number }[] }[];
  stepSeconds: number;
  windowSeconds: number;
  digits?: number;
}) {
  const { t } = useTranslation();
  const densified = series.map((s) => ({
    name: s.name,
    color: s.color,
    points: densifyPoints(s.points, stepSeconds, windowSeconds),
  }));
  if (densified.every((s) => s.points.every((v) => v == null))) return null;
  return (
    <div>
      <div className="kicker" style={{ marginBottom: 4 }}>{title}</div>
      <LineChart
        height={132}
        series={densified}
        format={(v) => fmtNum(v, digits) + " " + unit}
        xLabel={({ rawIndex, rawTotal, newest }) => {
          // Raw indices only: `densifyPoints` lays one point per `stepSeconds`
          // across the window, and the chart draws at most PLOT_POINTS of them.
          if (newest) return t("telemetry.now");
          const back = (rawTotal - 1 - rawIndex) * stepSeconds;
          if (back < 90) return t("telemetry.secondsAgo", { n: Math.round(back) });
          if (back < 5400) return t("telemetry.minutesAgo", { n: Math.round(back / 60) });
          if (back < 48 * 3600) return t("telemetry.hoursAgo", { n: Math.round(back / 3600) });
          return t("telemetry.daysAgo", { n: Math.round(back / 86400) });
        }}
      />
    </div>
  );
}
