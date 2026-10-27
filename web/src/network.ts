// Network rendering helpers: port cells colored by VLAN, formats, and the sum
// of a device's MEASURED throughput.
//
// This file used to carry a "traffic simulation (demo — replaced by real
// monitoring later)". Real monitoring now exists: per-port throughput comes
// from SNMP counters, derived by the controller, and there is nothing left
// to simulate here.
import type { Device, PortGroup, PortIntent, Vlan } from "./api";
import { ageMs } from "./ui/clock";
// The elapsed-time ladder lives in its own import-free module so it can be
// asserted by a bare node process (freshness.test.ts). Both labels below read
// their thresholds from it rather than each keeping a copy: two ladders would
// eventually describe the same instant two different ways.
import { elapsedBucket } from "./freshness";
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
 * Is this expanded port an SFP/SFP+ cage? The [PortRef] flavour of
 * [isSFPPort] below, for callers that already hold the expanded list — same
 * rule: read from the GROUP, not the port name alone (EdgeSwitch cages are
 * named GE17/GE18, the media lives in the group's "SFP+" label), with the
 * name-prefix check covering RouterOS's "sfp-sfpplus…" ports whose group
 * carries no label.
 */
export function isSFPRef(pr: PortRef): boolean {
  return (
    pr.groupLabel.toUpperCase().includes("SFP") ||
    pr.name.toLowerCase().startsWith("sfp")
  );
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
export const UNMANAGED_UP = "#64748b";
export const UNMANAGED_UP_BORDER = "#94a3b8";

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
 * Visual spec for a port cell (see docs/en/02-Frontend/Port rendering):
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
  // A port SOMEONE SHUT reads at 25% — three-quarters transparent.
  //
  // It keeps its VLAN colour rather than being greyed out, and that is the
  // point: the configuration is still there and still what will come back the
  // moment the port is re-enabled, so painting it neutral would erase the very
  // fact an operator needs while deciding whether to bring it up. Fading it says
  // "this is configured and switched off", which no other state on the faceplate
  // says.
  //
  // Only `disabled` — the administratively shut state. NOT `down` (enabled,
  // nothing plugged in), NOT `unplugged`, and above all NOT `err-disabled`: the
  // switch shut that one ITSELF, it is a fault, and it already draws the eye with
  // a red frame and a blinking fill (cf. netui.tsx). Fading a fault would hide
  // the one state on this faceplate that always deserves attention.
  //
  // `Math.min` so it composes with the two rules above instead of overriding
  // them: on an unreachable device, or under a VLAN hover that excludes this
  // port, the dimmer of the two wins.
  if (p.link_state === "disabled") opacity = Math.min(opacity, 0.25);
  return { background, borderColor, ink, opacity, poeDot: linked && (p.poe_w ?? 0) > 0 };
}

/**
 * Is this port an SFP/SFP+ cage, rather than a copper jack?
 *
 * Mirrors the backend's `portrules.mediaOf` (internal/portrules/portrules.go):
 * checked on the GROUP's own `label`/`speed`, never on the port's name alone
 * — some connectors name every socket the same prefix ("GE") and carry the
 * media distinction only in the group (cf. `ubiquiti-edgeswitch`'s "SFP+"
 * label + "10G" speed group, split from its copper group by
 * `internal/driver/ubiquiti/edgeswitch/state.go`'s `portGroups`).
 */
export function isSFPPort(dev: Device, portName: string): boolean {
  for (const g of dev.port_groups ?? ([] as PortGroup[])) {
    const start = g.from ?? 1;
    if (!portName.startsWith(g.prefix)) continue;
    const n = Number(portName.slice(g.prefix.length));
    if (!Number.isFinite(n) || n < start || n >= start + g.count) continue;
    const hay = `${g.prefix} ${g.label ?? ""}`.toLowerCase();
    return hay.includes("sfp");
  }
  return false;
}

export function portShortNum(name: string): string {
  // Structured "unit/port" index (FS "ethernet 1/11", Cisco "Gi1/0/1"):
  // keep the slashed form — it is how the port is silkscreened on the faceplate,
  // and flattening it to digits printed "ethernet 1/11" as "111".
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
  const e = elapsedBucket(ageMs(iso) ?? 0);
  if (!e) return "";
  if (e.unit === "now") return i18n.t("status.justNow");
  if (e.unit === "min") return i18n.t("status.sinceMinutes", { count: e.count });
  if (e.unit === "h") return i18n.t("status.sinceHours", { count: e.count });
  return i18n.t("status.sinceDays", { count: e.count });
}

/**
 * The same elapsed duration said the OTHER way round: "12 min ago", not
 * "for 12 min".
 *
 * Two phrasings for one ladder, because the two facts they attach to are
 * different in kind. A STATE has a duration — the device has been unreachable
 * *for* two hours, and that duration is the thing worth knowing. An EVENT has
 * an age — the configuration was read two hours *ago*, and it is the distance
 * back to that instant that matters. Feeding an event date into `sinceLabel`
 * produces "configuration read for 2 h", which reads as a two-hour-long read.
 *
 * English hides how far apart these are; the locales this product ships do not.
 * Hence separate keys rather than one string reused in both places.
 */
export function agoLabel(iso: string): string {
  const e = elapsedBucket(ageMs(iso) ?? 0);
  if (!e) return "";
  if (e.unit === "now") return i18n.t("status.justNow");
  if (e.unit === "min") return i18n.t("status.agoMinutes", { count: e.count });
  if (e.unit === "h") return i18n.t("status.agoHours", { count: e.count });
  return i18n.t("status.agoDays", { count: e.count });
}

/**
 * Has monitoring EVER spoken about this device?
 *
 * This is the predicate behind the `status.neverObserved` sentence
 * [reachability] prints, extracted so that a count can ask the same question a
 * label asks — the two must not be allowed to drift apart.
 *
 * The distinction it draws, and why a `status === …` test at the call site
 * cannot draw it:
 *
 *	status "unknown" WITH a `status_at`    we looked, and right now no proxy
 *	                                       able to watch this device is
 *	                                       connected. The device may be
 *	                                       perfectly healthy;
 *	status "unknown" WITHOUT a `status_at` monitoring has never spoken about
 *	                                       this device. Nothing is claimed,
 *	                                       in either direction.
 *
 * Both carry the same `status`, so [statusColor] — which is handed the status
 * alone — cannot tell them apart; only `status_at` can, and that is exactly
 * the test [reachability] makes. An observed status with no date (`ok` from a
 * row predating the column) is NOT this case: it is an observation whose
 * duration we lost, not an observation we never made.
 */
export function neverObserved(dev: {
  status: string;
  status_at?: string;
}): boolean {
  return dev.status === "unknown" && !dev.status_at;
}

/**
 * Can anyone see this device RIGHT NOW? — the other half of [neverObserved].
 *
 * `status === "unknown"` WITH a `status_at`: monitoring did speak about this
 * device once, and at this instant no proxy able to watch it is connected. This
 * is the state `--indeterminate` was minted for — meshcraft has lost the
 * vantage point and the device may be perfectly healthy.
 *
 * The two predicates partition `unknown` and never overlap, which is what lets
 * a count use them as buckets:
 *
 *	neverObserved  status "unknown", no date   grey, --text-dim
 *	blindNow       status "unknown", a date    yellow, --indeterminate
 *
 * Named rather than written out at the call site for the same reason
 * [neverObserved] is: the tile that counts these devices and the ink that
 * paints them must ask one question, or they will eventually answer two.
 */
export function blindNow(dev: { status: string; status_at?: string }): boolean {
  return dev.status === "unknown" && !!dev.status_at;
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
      detail: neverObserved(dev)
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
 *
 * # ONLINE MEANS VOUCHED FOR (decided 2026-08-08, extended the same day)
 *
 * `online` used to be "everything that is not `off`". The first ruling took out
 * the devices monitoring has NEVER spoken about; the second took out the ones
 * observed before that no connected proxy can watch right now. The principle
 * behind both is one sentence: **a device meshcraft cannot currently vouch for
 * must not inflate the headline number.** Three buckets, never overlapping:
 *
 *	online       we can see it, and it answers            --ok / --warn
 *	blind        seen before, nobody can see it now       --indeterminate
 *	unobserved   monitoring has never spoken about it     --text-dim
 *
 *	online + blind + unobserved + (status "off") = the fleet
 *
 * # WHY blind AND unobserved ARE TWO FIGURES AND NOT ONE
 *
 * They leave the numerator for the same reason, so one merged "unverifiable"
 * count would serve the arithmetic. They are kept apart because the arithmetic
 * is not the only consumer: the palette gives them DIFFERENT INKS (§2 of
 * UI-GUIDE — yellow "we lost the vantage point" versus grey "we have never
 * looked"), and a tile that must pick one of those cannot re-derive the split
 * without reimplementing [neverObserved] at the call site — which is the exact
 * drift that predicate was extracted to prevent. They also differ in what an
 * operator does: a blind device points at the proxy, an unobserved one points
 * at a device that was added and never watched.
 *
 * Both are RETURNED rather than merely subtracted, because a device that has
 * left the numerator must not also leave the denominator: a fleet that shrinks
 * when a device stops being verifiable hides it, which is the opposite of what
 * the ink is for. Callers show the whole fleet as the total and use these two
 * to say why the two ends do not meet.
 *
 * `off` is deliberately NOT one of them. "Down, and we know it" is a fact we
 * observed; the sub-line that says "N offline" must count those alone, or the
 * dashboard asserts outages nobody has seen.
 */
export function siteRates(devices: Device[] | undefined) {
  let ports = 0,
    up = 0,
    rx = 0,
    tx = 0,
    poe = 0,
    poeBudget = 0,
    online = 0,
    blind = 0,
    unobserved = 0;
  (devices ?? []).forEach((dev) => {
    const all = expandPorts(dev);
    ports += all.length;
    poeBudget += dev.poe_budget_w;
    if (dev.status === "off") return;
    if (neverObserved(dev)) unobserved++;
    else if (blindNow(dev)) blind++;
    else online++;
    // Its PORTS are still counted, deliberately: neither "never observed" nor
    // "nobody can see it now" is about the ports — `status` is ping monitoring,
    // and a device adopted with a read profile can carry links and counters
    // meanwhile. Only the reachability claim is withheld.
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
  return { ports, up, rx, tx, poe, poeBudget, online, blind, unobserved };
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
  last_apply?: { skipped?: string[]; not_applied?: { kind: string }[]; error?: string };
}): string {
  if (deviceNeedsAck(dev)) {
    if (dev.last_apply?.error) return i18n.t("status.applyFailed");
    if (
      (dev.last_apply?.skipped?.length ?? 0) + (dev.last_apply?.not_applied?.length ?? 0) >
      0
    )
      return i18n.t("status.applySkipped");
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
    not_applied?: { kind: string }[];
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
  /**
   * When monitoring last spoke about this device. ABSENT means it never has —
   * and that is a different dot from `unknown` with a date (cf. [neverObserved]
   * and the fall-through at the end of [deviceDot]). Optional so a caller that
   * genuinely has no date is not forced to invent one; the effect of omitting it
   * on a device that HAS been observed is a yellow dot instead of a grey one,
   * i.e. the older, more alarming reading, never a falsely reassuring one.
   */
  status_at?: string;
};

/**
 * True when the screen shows a banner that will NOT clear on its own — the
 * operator must force, retry, or import. Distinct from a fresh `pending`
 * intent that auto-apply will pick up within seconds.
 */
export function deviceNeedsAck(dev: DeviceAckInput): boolean {
  if (dev.last_apply?.error) return true;
  if (
    (dev.last_apply?.skipped?.length ?? 0) + (dev.last_apply?.not_applied?.length ?? 0) >
    0
  )
    return true;
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
      } else if (
        (dev.last_apply?.skipped?.length ?? 0) + (dev.last_apply?.not_applied?.length ?? 0) >
        0
      ) {
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
  // NEVER OBSERVED IS GREY, not the yellow of `unknown` (decided 2026-08-08).
  //
  // The two share a status and only `status_at` separates them, so statusColor —
  // handed the status alone — cannot make this call and its grey branch was
  // unreachable for real devices. Here the whole device is in hand, so it can.
  //
  // Why it belongs at the FALL-THROUGH and not at the top: a device meshcraft
  // has never seen can still have a write queued for it, and blue "pending" is
  // the true thing to say about that. Grey answers "what do we know about its
  // reachability" — nothing — which is the last question this function asks.
  if (neverObserved(dev)) {
    return { color: "var(--text-dim)", title: i18n.t("status.neverObserved") };
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

/* ======================================================================
 * THE TRAFFIC RAMP — how much of this link is in use
 * ======================================================================
 *
 * MOVED OUT, and re-exported here so no caller had to move with it.
 *
 * The ramp now lives in `./trafficRamp`, which imports NOTHING. That is the
 * point: this file imports `./i18n`, and `topologyLinks.ts` cannot import a
 * file that does (the framework-free node suite loads neither React nor
 * i18n). So the topology kept a second capacity parser of its own, and the
 * two readings of one number had already drifted — a bare "1000" was a
 * terabit to a link half and a gigabit to a chart.
 *
 * `trafficRamp.ts` has the full account; ADR-0030 has the measurements and the
 * reason the SVG gradient now SAMPLES `trafficColorAt` instead of being a
 * second definition of the same ramp.
 */
export {
  TRAFFIC_STOPS,
  speedMbps,
  trafficColor,
  trafficColorAt,
  trafficLevel,
  trafficRampStops,
  trafficRateAt,
} from "./trafficRamp";
