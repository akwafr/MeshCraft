/**
 * Topology link strokes: one palette for declared and LLDP links.
 *
 * Offline → black with red glow. Online forwarding → green→red by load
 * (non-linear). Spanning-tree blocking / discarding → accent blue.
 *
 * Capacity always comes from the device (negotiated link, else port facade
 * speed from the driver/catalog) — never from a hand-entered link.speed.
 */

export type LinkStroke = {
  stroke: string;
  strokeWidth: number;
  strokeDasharray?: string;
  opacity?: number;
  filter?: string;
};

/**
 * Capacity in Mbps from driver observation.
 * Prefer negotiated `port.link`; fall back to the port group's facade speed.
 */
export function linkCapacityMbps(
  observedLink: string | undefined | null,
  facadeSpeed: string | undefined,
): number {
  const raw = (observedLink || facadeSpeed || "").toLowerCase().replace(/\s+/g, "");
  if (!raw) return 1000;
  const m = raw.match(/^([\d.]+)\s*(g|m|gbps|mbps)?/);
  if (!m) return 1000;
  const n = parseFloat(m[1]);
  if (!Number.isFinite(n) || n <= 0) return 1000;
  const unit = m[2] || (n >= 10 ? "g" : "m");
  if (unit.startsWith("g")) return n * 1000;
  return n;
}

/** Label for mid-link speed: negotiated first, else facade. */
export function linkSpeedLabel(
  observedLink: string | undefined | null,
  facadeSpeed: string | undefined,
): string {
  const obs = (observedLink || "").trim();
  if (obs) return obs;
  return (facadeSpeed || "").trim();
}

/** Non-linear green→red (power curve so mid load already warns). */
export function loadColor(util: number): string {
  const t = Math.pow(Math.min(1, Math.max(0, util)), 0.45);
  return lerpHex("#46a758", "#e5484d", t);
}

function lerpHex(a: string, b: string, t: number): string {
  const pa = hexRGB(a);
  const pb = hexRGB(b);
  const r = Math.round(pa[0] + (pb[0] - pa[0]) * t);
  const g = Math.round(pa[1] + (pb[1] - pa[1]) * t);
  const bl = Math.round(pa[2] + (pb[2] - pa[2]) * t);
  return `rgb(${r},${g},${bl})`;
}

function hexRGB(h: string): [number, number, number] {
  const s = h.replace("#", "");
  return [
    parseInt(s.slice(0, 2), 16),
    parseInt(s.slice(2, 4), 16),
    parseInt(s.slice(4, 6), 16),
  ];
}

export function topologyLinkStroke(opts: {
  online: boolean;
  blocking: boolean;
  /** Max of both directions / capacity, 0..1+. */
  util: number;
  /** LLDP one-sided certainty — dash only, same palette. */
  uncertain?: boolean;
  /** Thinner stroke for LLDP overlay. */
  lldp?: boolean;
}): LinkStroke {
  const width = opts.lldp ? 1.6 : 2.2;
  if (!opts.online) {
    return {
      stroke: "#0a0a0a",
      strokeWidth: width,
      strokeDasharray: "6 5",
      filter: "url(#topo-link-offline)",
      opacity: opts.lldp ? 0.9 : 1,
    };
  }
  if (opts.blocking) {
    return {
      stroke: "var(--accent)",
      strokeWidth: width,
      strokeDasharray: opts.uncertain ? "2 3" : undefined,
      opacity: opts.lldp ? 0.9 : 1,
    };
  }
  return {
    stroke: loadColor(opts.util),
    strokeWidth: width,
    strokeDasharray: opts.uncertain ? "2 3" : undefined,
    opacity: opts.lldp ? 0.9 : 1,
  };
}

/** Thin solid green for managed→unknown/unmanaged LLDP peer links. */
export function ghostPeerLinkStroke(online: boolean): LinkStroke {
  if (!online) {
    return {
      stroke: "#0a0a0a",
      strokeWidth: 1.2,
      strokeDasharray: "6 5",
      filter: "url(#topo-link-offline)",
      opacity: 0.9,
    };
  }
  return {
    stroke: "#46a758",
    strokeWidth: 1.2,
    opacity: 1,
  };
}
