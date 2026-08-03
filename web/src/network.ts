// Network rendering helpers: port cells colored by VLAN, formats, and the sum
// of a device's MEASURED throughput.
//
// This file used to carry a "traffic simulation (demo — replaced by real
// monitoring later)". Real monitoring now exists: per-port throughput comes
// from SNMP counters, derived by the controller, and there is nothing left
// to simulate here.
import type { Device, PortGroup, PortIntent, Vlan } from "./api";
import i18n from "./i18n";

/**
 * VLAN palette. The first 8 hues are validated for color vision (CVD
 * separation of neighboring pairs checked on a dark background): a site with
 * up to 8 VLANs is readable by construction. Beyond that, identity also
 * relies on the number written in the cell, the tooltip and the legend —
 * color is never the only cue.
 */
export const PALETTE = [
  "#3987e5", "#d95926", "#199e70", "#c98500", "#d55181", "#008300", "#9085e9", "#e66767",
  "#6da7ec", "#8a5a2b", "#31c39a", "#b47cd0", "#e0a3bd", "#5bb85b", "#5f5aa8", "#a33c3c",
];

export type VlanMap = Map<number, Vlan>;
export const vlanMap = (vlans: Vlan[] | undefined): VlanMap =>
  new Map((vlans ?? []).map((v) => [v.vlan_id, v]));

export interface PortRef {
  /** Standard-model name — what the backend manipulates (API calls, diffs). */
  name: string;
  /**
   * What the operator should READ: the hardware's silkscreen number when the
   * connector declares one (`group.silk`), otherwise the standard name. Use
   * this everywhere a port is shown; use `name` everywhere one is addressed.
   */
  display: string;
  groupLabel: string;
  poeCap: boolean;
  speed: string;
  p: PortIntent;
}

/** Expands port_groups + the ports record into an ordered list of ports. */
export function expandPorts(dev: Device): PortRef[] {
  const out: PortRef[] = [];
  for (const g of dev.port_groups ?? ([] as PortGroup[])) {
    const start = g.from ?? 1;
    for (let k = start; k < start + g.count; k++) {
      const name = g.prefix + k;
      out.push({
        name,
        display: g.silk != null ? g.silk + k : name,
        groupLabel: g.label ?? g.prefix,
        poeCap: !!g.poe,
        speed: g.speed ?? "1G",
        p: dev.ports?.[name] ?? { mode: "libre" },
      });
    }
  }
  return out;
}

/**
 * The port's effective native VLAN. A trunk that doesn't designate a native
 * one isn't a trunk without a native VLAN: the hardware places VLAN 1 there,
 * and that's what rereading the device will always report.
 */
export function nativeVlan(p: PortIntent): number | null {
  if (p.pvid != null && p.pvid > 0) return p.pvid;
  return p.mode === "trunk" ? 1 : null;
}

/** Does the port carry this VLAN? (access, trunk member, or native) */
export function serves(p: PortIntent, vid: number): boolean {
  if (p.mode === "access") return p.pvid === vid;
  if (p.mode === "trunk")
    return (
      p.tagged === "all" ||
      (Array.isArray(p.tagged) && p.tagged.includes(vid)) ||
      nativeVlan(p) === vid
    );
  return false;
}

const INK_ON_COLOR = "#10131a";
/**
 * An ACTIVE port that meshcraft doesn't manage: solid slate gray.
 *
 * Chosen OUTSIDE the VLAN palette, deliberately — a port with no intent
 * carries no VLAN to show, and giving it a palette hue would suggest a
 * configuration that doesn't exist. The contrast with a free port (panel
 * background, separator border) is that of solid versus empty, which is the
 * distinction this whole spec uses for "the link is active".
 */
const UNMANAGED_UP = "#64748b";
const UNMANAGED_UP_BORDER = "#94a3b8";

function stripeGradient(colors: string[], seg: number): string {
  let cs = colors.slice(0, 4);
  if (cs.length === 1) cs = [cs[0], "color-mix(in srgb, " + cs[0] + " 35%, var(--bg))"];
  const stops = cs.map((c, i) => `${c} ${i * seg}px ${(i + 1) * seg}px`).join(", ");
  return `repeating-linear-gradient(135deg, ${stops})`;
}

export interface PortVisual {
  background: string;
  borderColor: string;
  ink: string;
  opacity: number;
  poeDot: boolean;
}

/**
 * Visual spec for a port cell (see docs "Rendu des ports"):
 * access + link = solid VLAN color · trunk = stripes of member VLANs ·
 * configured without a link = outline only · free = off.
 */
export function portVisual(
  p: PortIntent,
  vm: VlanMap,
  opts: { size: number; off?: boolean; hoverVlan?: number | null },
): PortVisual {
  const linked = !!p.link;
  const managed = p.mode != null && p.mode !== "libre";
  let background = "var(--bg-hover)";
  let borderColor = "var(--border)";
  let ink = "var(--text-dim)";

  // A PORT CARRYING A LINK WITHOUT BEING MANAGED IS VISIBLE, and this is a fix.
  //
  // The three branches below only paint what meshcraft CONFIGURES: an access
  // mode, a trunk. A port with no intent therefore fell through and came out
  // with a free port's background — regardless of its link. On a freshly
  // adopted switch, where NO port has an intent yet, the entire facade
  // therefore displayed as off even though a 10G uplink was plugged in. This
  // is the moment the operator most needs to see what's plugged in, and it's
  // the one where the screen showed nothing.
  //
  // The confusion was between MANAGED and ACTIVE: the former is an intent,
  // the latter an OBSERVATION from SNMP counters. Both are read on the same
  // cell and don't mean the same thing.
  //
  // The color is NEUTRAL, not a VLAN hue: there's no VLAN to show, and
  // borrowing a VLAN's color would suggest a configuration. Solid cell =
  // active link, as everywhere else in this visual language.
  if (linked && !managed) {
    background = UNMANAGED_UP;
    borderColor = UNMANAGED_UP_BORDER;
    ink = INK_ON_COLOR;
  }

  if (p.mode === "access" && p.pvid != null && vm.get(p.pvid)) {
    const c = vm.get(p.pvid)!.color;
    if (linked) [background, borderColor, ink] = [c, c, INK_ON_COLOR];
    else [background, borderColor, ink] = ["transparent", c, c];
  } else if (p.mode === "trunk") {
    const nat = nativeVlan(p);
    const cols =
      p.tagged === "all"
        ? [nat != null && vm.get(nat) ? vm.get(nat)!.color : "#94a3b8"]
        : ((p.tagged ?? []) as number[]).map((id) => vm.get(id)?.color ?? "#888");
    const base = cols[0] ?? "#94a3b8";
    if (linked) {
      background = stripeGradient(cols.length ? cols : [base], Math.max(3, Math.round(opts.size / 8)));
      borderColor = base;
      ink = INK_ON_COLOR;
    } else [background, borderColor, ink] = ["transparent", base, base];
  }

  let opacity = 1;
  if (opts.hoverVlan != null && !(p.mode !== "libre" && p.mode != null && serves(p, opts.hoverVlan)))
    opacity = 0.12;
  if (opts.off) opacity = Math.min(opacity, 0.35);
  return { background, borderColor, ink, opacity, poeDot: linked && (p.poe_w ?? 0) > 0 };
}

export function portShortNum(name: string): string {
  // Structured « unit/port » index (FS « ethernet 1/11 », Cisco « Gi1/0/1 »):
  // keep the slashed form — it is how the port is silkscreened on the faceplate,
  // and flattening it to digits printed « ethernet 1/11 » as « 111 ».
  const slashed = name.match(/(\d+(?:\/\d+)+)\s*$/);
  if (slashed) return slashed[1];
  const digits = name.replace(/\D+/g, "");
  return name.startsWith("sfp") ? "S" + digits : digits;
}

export function statusColor(status: string): string {
  if (status === "off") return "var(--danger)";
  if (status === "warn") return "var(--warn)";
  if (status === "ok") return "var(--ok)";
  // "unknown" is NOT the same as "never seen": it is what the controller reports
  // when no proxy able to watch this device is connected. Yellow rather than the
  // grey of a device nobody has looked at yet, and deliberately not the orange of
  // an alert — the device may be perfectly healthy and the proxy is what broke.
  if (status === "unknown") return "var(--indeterminate)";
  return "var(--text-dim)";
}

export function statusLabel(status: string): string {
  if (status === "off") return i18n.t("status.unreachable");
  if (status === "warn") return i18n.t("status.alert");
  if (status === "ok") return i18n.t("status.online");
  if (status === "unknown") return i18n.t("status.indeterminate");
  return i18n.t("status.unknown");
}

/**
 * An elapsed duration, plain and short.
 *
 * Rounded down and without seconds beyond the minute: precision adds
 * nothing here, and "unreachable for 2 h" reads better than
 * "for 2 h 14 min 3 s".
 */
export function sinceLabel(iso: string): string {
  const ms = Date.now() - new Date(iso).getTime();
  if (!Number.isFinite(ms) || ms < 0) return "";
  const min = Math.floor(ms / 60000);
  if (min < 1) return i18n.t("status.justNow");
  if (min < 60) return i18n.t("status.sinceMinutes", { count: min });
  const h = Math.floor(min / 60);
  if (h < 48) return i18n.t("status.sinceHours", { count: h });
  return i18n.t("status.sinceDays", { count: Math.floor(h / 24) });
}

/**
 * Reachability in one sentence, DURATION INCLUDED.
 *
 * Why the duration matters as much as the state: "unreachable" doesn't say
 * whether a site visit is needed. Unreachable for two minutes may be a
 * reboot in progress; for three days, it's a device nobody noticed go down.
 * The same word covered both.
 *
 * A missing `status_at` is stated EXPLICITLY rather than hidden, and that's
 * the heart of the fix: "unknown" and "never observed" don't explain the
 * same thing. The former may come from an observation lost along the way,
 * the latter is the normal state of a device just added — and an operator
 * seeing "UNKNOWN" with no detail looks for a fault where there is only a
 * wait.
 *
 * What this sentence does NOT say: that the observation is fresh. Monitoring
 * only reports transitions, so an old date on an online device is normal.
 * There is no periodic heartbeat (not built), and presenting this duration
 * as freshness would raise alarms on the healthiest part of the fleet.
 */
export function reachability(dev: {
  status: string;
  status_note?: string;
  status_at?: string;
}): { label: string; detail: string } {
  const label = statusLabel(dev.status);
  // No date: either monitoring never reported on this device, or it's a row
  // predating the column. No duration is fabricated — saying "forever" would
  // be false, and the detail falls back to the note if there is one.
  if (!dev.status_at) {
    return {
      label,
      detail:
        dev.status === "unknown"
          ? i18n.t("status.neverObserved")
          : (dev.status_note ?? ""),
    };
  }
  const parts = [sinceLabel(dev.status_at), dev.status_note].filter(
    (p): p is string => !!p,
  );
  return { label, detail: parts.join(" — ") };
}

/**
 * Reachability and plan compliance are TWO distinct axes.
 *
 * A device that responds perfectly but whose management addressing doesn't
 * follow the site plan isn't down: it needs migrating. Red is reserved for
 * what no longer responds; a plan discrepancy is a warning.
 */
export type MgmtConformity = {
  checked: boolean;
  conforms: boolean;
  reason?: string;
  target_vlan?: number;
  target_tagged?: boolean;
  target_subnet?: string;
  target_source?: string;
  migratable: boolean;
  /**
   * Is there NOW an active uplink carrying the targeted management VLAN?
   * "ready" | "absent" | "unknown".
   *
   * Reachability doesn't answer this question: a device may respond
   * perfectly on its current VLAN while no link carries the target VLAN.
   * Switching in that case makes it silent immediately.
   */
  path_state?: "ready" | "absent" | "unknown";
  path_reason?: string;
};

/**
 * The management addressing mode of ONE device, as resolved by the
 * controller (migration 0027).
 *
 * An axis DISTINCT from plan compliance just above, and confusing them would
 * be a display mistake: a device with a fixed address in the right subnet
 * is perfectly plan-compliant — it isn't a discrepancy, it's a modality. The
 * three MikroTik of the reference install are in this case.
 */
export type MgmtAddressing = {
  /** The mode that applies: "dhcp", "static", or empty when unknown. */
  effective: "" | "dhcp" | "static";
  /**
   * Where this mode comes from. `observed` is the case that matters: "static
   * OBSERVED on the device, and kept as is", i.e. a modality we respect
   * rather than a decision we made.
   */
  origin: "device" | "observed" | "site_plan" | "unknown";
  /** The sentence to display, written by the controller. */
  why: string;
  /** The intent and the device diverge, and here's how. */
  drift?: string;
  /** What the PLATFORM admits. `known: false` = connector unknown to meshcraft. */
  caps: {
    known: boolean;
    static: boolean;
    dhcp: boolean;
    gateway: boolean;
    dns: boolean;
    /** Off-subnet L3 route for management (MgmtRouted). */
    routed?: boolean;
    /** Answers ARP/L2 without a gateway (MgmtIPARP). */
    ip_arp?: boolean;
    apply_same_vlan?: boolean;
    migrate_vlan?: boolean;
    jump_vlan?: boolean;
  };
  /** Management VLAN last observed on the device (0 = untagged / unknown). */
  current_vlan?: number;
  /**
   * Can meshcraft READ the mode on this range? False doesn't mean "no mode"
   * but "not read" — and the two call for opposite gestures: the former is
   * fixed with a click on "Reread", the latter isn't fixed at all.
   */
  readable: boolean;
  /**
   * Can meshcraft APPLY same-VLAN addressing ([MgmtAddrWriter])? VLAN migrate /
   * jump use caps.migrate_vlan / caps.jump_vlan separately.
   */
  pushable: boolean;
  push_blocked?: string;
  /** Non-empty: Apply needs a second confirmation (plan override or mode cutover). */
  apply_confirm?: string;
};

/** The addressing INTENT saved for this device. */
export type DeviceAddressing = {
  mode: "inherit" | "dhcp" | "static";
  static_ip?: string;
  static_gateway?: string;
  static_dns?: string[];
  by?: string;
  at?: string;
};

/** What the connector READ on the device (driver.MgmtAddrState). */
export type ObservedAddressing = {
  mode?: "dhcp" | "static";
  address?: string;
  gateway?: string;
  dns?: string[];
  interface?: string;
  notes?: string[];
};

export function effectiveStatus(
  status: string,
  mgmt?: MgmtConformity,
): { color: string; label: string; hint: string } {
  if (status === "off") {
    return { color: statusColor("off"), label: statusLabel("off"), hint: "" };
  }
  if (mgmt?.checked && !mgmt.conforms) {
    return {
      color: "var(--warn)",
      label: i18n.t("status.outOfPlan"),
      hint: mgmt.reason ?? i18n.t("status.outOfPlanHint"),
    };
  }
  return { color: statusColor(status), label: statusLabel(status), hint: "" };
}

// Locales using a comma as the decimal separator, per common convention.
const COMMA_DECIMAL_LANGS = new Set(["fr", "de", "it", "es", "pt", "ru", "nl"]);

export function decimalSeparator(): string {
  const lang = (i18n.resolvedLanguage ?? i18n.language ?? "en").split("-")[0];
  return COMMA_DECIMAL_LANGS.has(lang) ? "," : ".";
}

export function fmtMbps(m: number): string {
  if (!m) return "—";
  const sep = decimalSeparator();
  if (m >= 1000) return (m / 1000).toFixed(1).replace(".", sep) + " Gb/s";
  if (m >= 100) return Math.round(m) + " Mb/s";
  if (m >= 1) return m.toFixed(1).replace(".", sep) + " Mb/s";
  return Math.round(m * 1000) + " kb/s";
}

// `jitAt` and `useTick` have been REMOVED, and it's worth knowing what they did.
//
// `jitAt` was a deterministic jitter applied to the display: it made
// throughput oscillate ±18% around its value, every 2.2 seconds, to give
// life to an interface whose backend measured nothing. `useTick` was the
// clock driving it.
//
// It was honest as long as the UNDERLYING value was fictional — it only came
// from a configuration import. Since throughput is now derived from SNMP
// counters (`internal/api/portrates.go`), it had become noise added to a
// measurement: an operator would read 43 Mb/s where the port actually
// carried 37, and see the number change while looking at the screen with
// nothing moving on the network.
//
// What the removal implies and must be accepted: the numbers now only move
// at the pace of real refreshes (the site's SSE stream, or `useSiteLive`'s
// interval). A calmer interface, that tells the truth.

/**
 * Sum of ONE device's throughput, and count of its ACTIVE ports.
 *
 * `up` COUNTS LINKS, not intents — and that was the first flaw. The
 * condition required `p.mode`, i.e. a meshcraft configuration: a freshly
 * adopted switch, with no port having an intent yet, announced "0 active
 * ports" with a 10G uplink plugged in. "Active" is an OBSERVATION from SNMP
 * counters; "managed" is an intent.
 *
 * # THROUGHPUT IS NO LONGER AMPUTATED OF UPLINKS, and that was the second flaw
 *
 * This function used to exclude "all VLANs" trunks — aggregation links — on
 * the grounds that "their traffic is already counted on the access ports
 * feeding it". That's right for a SITE TOTAL and wrong for ONE device, and
 * the reference fleet showed the cost: the lab 1930's only four active ports
 * are its four SFP+ cages, all full trunks. Its displayed throughput was
 * therefore ZERO even though the metric existed and was being reported —
 * which reads as broken collection.
 *
 * And on a single device, there's nothing to fix: both directions are
 * counted SEPARATELY. A frame entering through GE1 and leaving through the
 * uplink counts once on entry (GE1) and once on exit (the uplink) — never
 * twice in the same sum. `rx` is therefore "everything entering this
 * switch", `tx` "everything leaving it", and that's exactly what an
 * operator reads on a device sheet.
 *
 * Double counting is a SITE problem, not a device one: a frame crossing two
 * switches is counted in each one's `rx`. [siteRates] handles that, and the
 * controller's traffic chart applies the same rule to its series.
 */
export function deviceRates(dev: Device) {
  let up = 0,
    rx = 0,
    tx = 0,
    poe = 0;
  const off = dev.status === "off";
  expandPorts(dev).forEach((pr) => {
    const p = pr.p;
    if (!p.link) return;
    up++;
    if (off) return;
    rx += p.rx_mbps ?? 0;
    tx += p.tx_mbps ?? 0;
    poe += p.poe_w ?? 0;
  });
  return { up, rx, tx, poe, total: expandPorts(dev).length };
}

/**
 * Totals for a SITE: ports, throughput, PoE.
 *
 * Distinct from [deviceRates] for a reason that isn't convenience — the two
 * answer two questions whose answers differ:
 *
 *	"how much goes through THIS switch?"  → all its active ports, both
 *	                                         directions kept separate
 *	"how much goes through THIS SITE?"    → without INTER-SWITCH links,
 *	                                         otherwise a frame crossing two
 *	                                         devices is counted twice
 *
 * The exclusion is read from the intent (`trunk` + `all`), for lack of
 * another source, and this is accepted: an aggregation link nobody declared
 * as such is counted. It's the same trade-off as the controller-side traffic
 * chart, which excludes the same ports from its series sum — the two must
 * tell the same story, otherwise the tile and the chart on the same screen
 * would contradict each other.
 */
export function siteRates(devices: Device[] | undefined) {
  let ports = 0,
    up = 0,
    rx = 0,
    tx = 0,
    poe = 0,
    poeBudget = 0,
    online = 0;
  (devices ?? []).forEach((dev) => {
    const all = expandPorts(dev);
    ports += all.length;
    poeBudget += dev.poe_budget_w;
    if (dev.status === "off") return;
    online++;
    all.forEach((pr) => {
      const p = pr.p;
      if (!p.link) return;
      up++;
      if (p.mode === "trunk" && p.tagged === "all") return;
      rx += p.rx_mbps ?? 0;
      tx += p.tx_mbps ?? 0;
      poe += p.poe_w ?? 0;
    });
  });
  return { ports, up, rx, tx, poe, poeBudget, online };
}

/**
 * What the sync state means, in plain words.
 *
 * "unknown" is stated explicitly rather than hidden: a device never reread
 * isn't a compliant device, and letting that be assumed would be the worst
 * of the three possible mistakes.
 */
export function syncLabel(dev: {
  sync?: string;
  observed_at?: string;
  apply_stalled?: boolean;
  last_apply?: { skipped?: string[]; error?: string };
}): string {
  if (deviceNeedsAck(dev)) {
    if (dev.last_apply?.error) return i18n.t("status.applyFailed");
    if ((dev.last_apply?.skipped?.length ?? 0) > 0) return i18n.t("status.applySkipped");
    if (dev.apply_stalled) return i18n.t("status.applyStalled");
  }
  switch (dev.sync) {
    case "in_sync":
      return i18n.t("status.inSync");
    case "pending":
      return i18n.t("status.pending");
    case "drift":
      return i18n.t("status.drift");
    default:
      return i18n.t("status.neverReread");
  }
}

/**
 * Fields that decide whether an orange acknowledgement light is needed.
 * Reachability (`status`) is separate — see deviceDot.
 */
export type DeviceAckInput = {
  apply_stalled?: boolean;
  last_apply?: {
    skipped?: string[];
    error?: string;
  };
};

/**
 * Fields the status dot needs beyond reachability. Kept structural so list
 * and detail screens can pass a Device without importing circular types.
 */
export type DeviceDotInput = DeviceAckInput & {
  status: string;
  sync?: string;
  /** An automatic apply is writing to the device RIGHT NOW (live provisioning). */
  applying?: boolean;
};

/**
 * True when the screen shows a banner that will NOT clear on its own — the
 * operator must force, retry, or import. Distinct from a fresh `pending`
 * intent that auto-apply will pick up within seconds.
 */
export function deviceNeedsAck(dev: DeviceAckInput): boolean {
  if (dev.last_apply?.error) return true;
  if ((dev.last_apply?.skipped?.length ?? 0) > 0) return true;
  if (dev.apply_stalled) return true;
  return false;
}

/**
 * What a device's status dot must show.
 *
 * It carries two pieces of information of different natures — REACHABILITY
 * (green, orange, red) and SYNC / ACK state (blinking blue, orange) — and
 * they had to be arbitrated:
 *
 *   - an UNREACHABLE device keeps its red dot. Announcing "configuration in
 *     progress" on a switch that isn't responding would be false: nothing is
 *     in progress, it's simply silent. Reachability takes priority, because
 *     it explains everything else;
 *   - otherwise, a situation that needs acknowledgement (skipped ports,
 *     incomplete apply, apply failure, external drift) overrides green with
 *     an orange blink. Calling it green — or blue "in progress" — would hide
 *     a switch that will never move alone;
 *   - a fresh pending intent (not stalled) keeps the blue blink: auto-apply
 *     will clear it shortly.
 */
export function deviceDot(dev: DeviceDotInput): {
  color: string;
  blink?: "pending" | "drift" | "applying";
  title: string;
} {
  const base = statusLabel(dev.status);
  if (dev.status !== "off") {
    // Live provisioning takes precedence over a plain pending: the connector is
    // writing to the device right now, and the operator should see it move.
    if (dev.applying) {
      return {
        color: "var(--accent)",
        blink: "applying",
        title: base + " · " + i18n.t("status.applyingDotSuffix"),
      };
    }
    if (deviceNeedsAck(dev)) {
      let suffix = i18n.t("status.ackDotSuffix");
      if (dev.last_apply?.error) {
        suffix = i18n.t("status.failedDotSuffix");
      } else if ((dev.last_apply?.skipped?.length ?? 0) > 0) {
        suffix = i18n.t("status.skippedDotSuffix");
      } else if (dev.apply_stalled) {
        suffix = i18n.t("status.stalledDotSuffix");
      }
      return {
        color: "var(--warn)",
        blink: "drift",
        title: base + " · " + suffix,
      };
    }
    if (dev.sync === "pending") {
      return {
        color: "var(--accent)",
        blink: "pending",
        title: base + " · " + i18n.t("status.pendingDotSuffix"),
      };
    }
    if (dev.sync === "drift") {
      return {
        color: "var(--warn)",
        blink: "drift",
        title: base + " · " + i18n.t("status.driftDotSuffix"),
      };
    }
  }
  return { color: statusColor(dev.status), title: base };
}

/**
 * Best-effort preview: does an UP port already carry this VLAN as access
 * native or trunk tagged? Final refusal is always on the controller
 * ([mgmtVLANPathOf]); this only drives the dropdown hint.
 */
export function vlanPathPreview(
  dev: Device,
  vlanId: number,
): "ready" | "absent" | "unknown" {
  if (vlanId <= 0) return "unknown";
  const ports = expandPorts(dev);
  let sawLinkState = false;
  for (const pr of ports) {
    const link = pr.p.link;
    if (link !== undefined) sawLinkState = true;
    if (!link) continue;
    if (pr.p.pvid === vlanId) return "ready";
    const tagged = pr.p.tagged;
    if (tagged === "all") return "ready";
    if (Array.isArray(tagged) && tagged.includes(vlanId)) return "ready";
  }
  return sawLinkState ? "absent" : "unknown";
}
