// The inline traffic cell — a rate and its recent shape, in one table row.
//
// WHY THIS IS CHEAP DESPITE APPEARANCES. Every cell calls
// `usePortTelemetry(deviceId)`, which looks like one request per port and is
// not: react-query keys that query on the DEVICE, so the fifty-two cells of
// one switch resolve to a single in-flight request and a single cache entry.
// The window is the one the hover tip already uses, so a row that has been
// hovered has already paid for its neighbours.
//
// It also means the traffic column costs nothing on a page where it is not
// rendered: the query lives with the cell, not with the page.

import { useTranslation } from "react-i18next";
import { usePortTelemetry } from "./PortHoverTip";
import { portTraffic } from "./telemetry";
import { fmtMbps, speedMbps, trafficColor } from "./network";
import { Sparkline } from "./viz";

export function PortTrafficCell({
  deviceId,
  port,
  speed,
  off,
}: {
  deviceId: string;
  port: string;
  /** The group's speed label ("1G", "10G") — the ramp is read against it. */
  speed?: string;
  /** The device is unreachable: draw nothing rather than a stale curve. */
  off?: boolean;
}) {
  const { t } = useTranslation();
  const { data } = usePortTelemetry(off ? undefined : deviceId);
  const capacity = speedMbps(speed);

  if (off) return <span className="spark-empty">—</span>;
  if (!data) return <span className="spark-empty" aria-hidden>·</span>;

  const { rx, tx } = portTraffic(data, port);
  // ONE curve, both directions summed. A table row has room for a shape, not
  // for two; and the question this column answers — "is this link busy" —
  // is about the load on the wire, which is what the sum is. The per-direction
  // split stays one hover away, on the chart the port tip already draws.
  const total: (number | null)[] = rx.map((v, i) => {
    const a = v == null || Number.isNaN(v) ? null : v;
    const b = tx[i] == null || Number.isNaN(tx[i]!) ? null : tx[i]!;
    if (a == null && b == null) return null;
    return (a ?? 0) + (b ?? 0);
  });

  const real = total.filter((v): v is number => v != null);
  if (real.length === 0) return <span className="spark-empty">—</span>;
  const last = real[real.length - 1];

  return (
    <div className="spark-cell">
      <span className="spark-rate" style={{ color: trafficColor(last, capacity) }}>
        {fmtMbps(last)}
      </span>
      <Sparkline points={total} capacity={capacity} />
      <span className="sr-only">
        {t("portsPage.trafficCol")} {fmtMbps(last)}
      </span>
    </div>
  );
}
