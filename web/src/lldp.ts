import type { Device, LldpLink, LldpNeighbor, LldpSilence } from "./api";
import i18n from "./i18n";
import { sinceLabel } from "./network";

/**
 * LLDP neighborhood presentation — pure logic, no component, no HTTP call.
 *
 * See docs/en/01-Architecture/LLDP neighbours.md and docs/en/02-Frontend/Weathermap.md.
 *
 * # A separate file, and why
 *
 * These functions could have joined `network.ts`, which already carries
 * network presentation. They aren't there because this module speaks ONLY of
 * observations — what devices have seen — whereas `network.ts` speaks of
 * what meshcraft WANTS (a port's intent, the facade, VLANs). It's the same
 * boundary that separates `lldp_neighbors` from `device_links` in the
 * database, and keeping it visible in the file tree prevents a "link" helper
 * from eventually serving both.
 *
 * # THE RULE OF THIS FILE: never fill a silence
 *
 * All these functions return `null` or an empty string when they don't know,
 * and never a dash nor "no neighbor". A port with no LLDP neighbor is NOT a
 * port with no cable: the data shows it in three ways — an entire platform
 * in the fleet doesn't speak 802.1AB (SwOS), a device may only run LLDP on
 * two of its twenty-nine interfaces (the CRS326), and a neighbor that
 * doesn't speak LLDP stays silent no matter what. Writing "no neighbor"
 * where "we don't know" should be read would be this screen's costliest lie.
 */

/** The key under which a port finds its neighbors. */
export function portKey(deviceId: string, port: string): string {
  return `${deviceId}:${port}`;
}

/**
 * Indexes observations by port.
 *
 * Returns an ARRAY per port and not a single neighbor, unlike `Ports.tsx`'s
 * map which overwrites by key. The measured fact requires it: port
 * `sfp-sfpplus2` on the lab CRS326 sees THREE neighbors, because an
 * intermediate switch that doesn't consume LLDP floods the frames instead of
 * absorbing them. Keeping a single neighbor per port would have shown one at
 * random — and given the impression of a direct cable where there's a
 * broadcast domain.
 */
export function lldpByPort(
  neighbors: LldpNeighbor[] | undefined,
): Map<string, LldpNeighbor[]> {
  const out = new Map<string, LldpNeighbor[]>();
  (neighbors ?? []).forEach((n) => {
    const k = portKey(n.device_id, n.local_port);
    const list = out.get(k);
    if (list) list.push(n);
    else out.set(k, [n]);
  });
  return out;
}

/** Silence reasons, by device. */
export function lldpSilenceByDevice(
  silent: LldpSilence[] | undefined,
): Map<string, LldpSilence> {
  return new Map((silent ?? []).map((s) => [s.device_id, s]));
}

/**
 * The name to display for a neighbor.
 *
 * The fleet's recognized device first, because that's the name the operator
 * uses; the name the neighbor gave itself second. The two do NOT coincide on
 * the reference fleet — `lab-crs326` announces itself as "lan-r4" and
 * `sw-labo-1930` announces itself by its serial number "CN0EXAMPL1" — and
 * that's precisely why the tooltip shows BOTH when they differ: it's the
 * only way to understand why meshcraft matched this observation to this
 * device.
 */
export function neighborName(
  n: LldpNeighbor,
  byId: Map<string, Device>,
): string {
  const known = n.peer_device ? byId.get(n.peer_device) : undefined;
  if (known) {
    return n.sys_name && n.sys_name !== known.name
      ? i18n.t("lldp.announcedAs", { name: known.name, sysName: n.sys_name })
      : known.name;
  }
  return n.sys_name || n.chassis_id || i18n.t("lldp.unnamedNeighbor");
}

/** The remote port to display: the identifier if readable, else the description. */
export function neighborPort(n: LldpNeighbor): string {
  if (n.peer_port) return n.peer_port;
  if (n.port_id && !n.port_id.startsWith("0x")) return n.port_id;
  return n.port_desc || n.port_id || "";
}

/** What the matching path means, in plain words. */
function viaLabel(via: string): string {
  if (via === "chassis") return i18n.t("lldp.viaChassis");
  // Named apart on purpose: an operator reading this phrase understands
  // they're looking at a firmware that announces its port's MAC as the
  // chassis identifier, which is the case of the 1930 and what "recognized
  // by its chassis" would have hidden from them.
  if (via === "port_id") return i18n.t("lldp.viaPortId");
  // The weak path: a system name is a string an operator typed, and two
  // devices can share it. Saying so avoids reading this link as proof.
  if (via === "sys_name") return i18n.t("lldp.viaSysName");
  return "";
}

/**
 * A port's tooltip: the LLDP neighbor, its port, and SINCE WHEN it's seen.
 *
 * Returns `null` when there's nothing to say, which the caller must treat as
 * "add nothing" and not as "display that there's no neighbor".
 *
 * The date is what makes the difference between information and decoration:
 * "seen for 3 min" on a link believed in place for six months says someone
 * just replugged a cable. It's computed with the BROWSER's clock against a
 * CONTROLLER timestamp — never a proxy's, whose measured drift against the
 * controller reached eleven seconds on the reference fleet.
 *
 * Several neighbors on the same port are ALL listed, with the phrase
 * explaining what that means. The case is measured and counter-intuitive:
 * it's the only place an operator can learn their port doesn't lead
 * directly to what they think.
 */
export function neighborTooltip(
  list: LldpNeighbor[] | undefined,
  byId: Map<string, Device>,
): string | null {
  if (!list || list.length === 0) return null;
  const lines = list.map((n) => {
    const port = neighborPort(n);
    const via = n.peer_via ? viaLabel(n.peer_via) : "";
    return (
      `LLDP: ${neighborName(n, byId)}` +
      (port ? ` · ${port}` : "") +
      (n.first_seen ? ` · ${i18n.t("lldp.seenSince", { since: sinceLabel(n.first_seen) })}` : "") +
      (via ? `\n  ${via}` : "") +
      // The neighbor's model is only shown if it's NOT in the fleet: for a
      // known device, the operator already has its sheet, and lengthening
      // the tooltip would hide the line that matters.
      (!n.peer_device && n.sys_desc ? `\n  ${n.sys_desc}` : "") +
      // A refuted observation stays in this list, and this line is the only
      // thing that explains why the map draws no cable for it. Without it,
      // the panel would show a neighbor the weathermap ignores, and the
      // operator would conclude the map is broken rather than the cable moved.
      (n.refuted
        ? `\n  ${i18n.t("lldp.refutedByFresher", {
            device: n.refuted_by ? (byId.get(n.refuted_by)?.name ?? n.refuted_by) : "",
          })}`
        : "")
    );
  });
  if (list.length > 1) {
    lines.push(i18n.t("lldp.multipleNeighborsNote", { count: list.length }));
  }
  return lines.join("\n");
}

/**
 * How an observed link is STATED, on the weathermap as in a panel.
 *
 * Three states and not two, because "seen from one side only" has two
 * causes that don't read the same way.
 */
export type LldpLinkKind = "both" | "one_side" | "ghost";

export function lldpLinkKind(l: LldpLink): LldpLinkKind {
  if (!l.b_device) return "ghost";
  return l.both_sides ? "both" : "one_side";
}

/**
 * The phrase qualifying an observed link.
 *
 * "Seen from one side only" is NOT presented as an anomaly, and that's this
 * file's most important wording decision. An entire platform in the
 * reference fleet doesn't speak 802.1AB: any link touching a CSS326 on SwOS
 * will never be seen except from the other end, no matter what. Presenting
 * it as a flaw would send someone looking for a fault in a fleet that
 * works — it's the firmware that lacks the feature, and no version of
 * meshcraft will change that.
 */
export function lldpLinkPhrase(l: LldpLink): string {
  switch (lldpLinkKind(l)) {
    case "both":
      return i18n.t("lldp.observedBothSides");
    case "one_side":
      return i18n.t("lldp.observedOneSide");
    default:
      return i18n.t("lldp.observedUnplaced");
  }
}

/**
 * Side-panel list colour for an observed LLDP row. Canvas strokes use
 * topologyLinkStroke (same online/offline/load/STP palette as declared links).
 */
export interface LldpStroke {
  stroke: string;
  strokeWidth: number;
  strokeDasharray?: string;
  opacity: number;
}

export function lldpStroke(l: LldpLink): LldpStroke {
  switch (lldpLinkKind(l)) {
    case "both":
      // Seen from both sides: the solid stroke of the "observed" family.
      // It's the only case where a dash would add nothing — reciprocity IS
      // the certainty.
      return { stroke: "var(--accent)", strokeWidth: 1.5, opacity: 0.85 };
    case "one_side":
      return {
        stroke: "var(--accent)",
        strokeWidth: 1.5,
        strokeDasharray: "2 3",
        opacity: 0.6,
      };
    default:
      // The neighbor is outside the inventory: the link has no second
      // endpoint to reach, so it isn't drawn on the plan (see Plan.tsx).
      // This entry exists for the diagnostic panel, which does list it.
      return {
        stroke: "var(--text-dim)",
        strokeWidth: 1,
        strokeDasharray: "1 4",
        opacity: 0.5,
      };
  }
}

/** The tooltip of an observed link on the plan. */
export function lldpLinkTooltip(
  l: LldpLink,
  byId: Map<string, Device>,
): string {
  const a = byId.get(l.a_device);
  const bName = l.b_device
    ? byId.get(l.b_device)?.name ?? l.b_label ?? "?"
    : l.b_label || l.b_chassis_id || i18n.t("lldp.unknownNeighbor");
  const bPort = l.b_port || l.b_port_label || "";
  return (
    `${a?.name ?? l.a_device} (${l.a_port}) → ${bName}` +
    (bPort ? ` (${bPort})` : "") +
    `\n${i18n.t("lldp.linkPrefix", { phrase: lldpLinkPhrase(l) })}` +
    (l.first_seen ? `\n${i18n.t("lldp.seenSince", { since: sinceLabel(l.first_seen) })}` : "") +
    (l.last_seen ? `, ${i18n.t("lldp.confirmedSince", { since: sinceLabel(l.last_seen) })}` : "") +
    // The chassis only goes in the tooltip of an UNKNOWN neighbor: it's
    // what an operator would copy to go look for the device, and it has no
    // value once meshcraft has already named the device on the other end.
    (!l.b_device && l.b_chassis_id ? `\n${i18n.t("lldp.announcedChassis", { chassis: l.b_chassis_id })}` : "") +
    (!l.b_device && l.b_sys_desc ? `\n${l.b_sys_desc}` : "")
  );
}

/**
 * A silence's phrase, for the screen.
 *
 * Four phrases and not one, because the four causes are fixed with
 * OPPOSITE actions. The first can't be fixed at all, and that's the most
 * important message to convey: an operator reading "a collector is missing"
 * on a CSS326 will go looking for a setting that doesn't exist. The second
 * is the opposite extreme — the device itself refused, and one command on it
 * repairs the whole hole.
 */
export function lldpSilencePhrase(s: LldpSilence | undefined): string | null {
  if (!s) return null;
  if (s.reason === "platform_unsupported") {
    return (
      i18n.t("lldp.silencePlatformUnsupported") +
      // The connector's survey Source carries the PROOF (OIDs, pages enumerated).
      // Operator-facing copy is localized above from Reason.
      (s.detail ? "\n\n" + s.detail : "")
    );
  }
  if (s.reason === "lldp_disabled") {
    return i18n.t("lldp.silenceDisabled");
  }
  if (s.reason === "not_served") {
    return i18n.t("lldp.silenceNotServed");
  }
  return i18n.t("lldp.silenceNeverPolled");
}

/**
 * The ONE-LINE badge for a device drawn with no observed link at all.
 *
 * Separate from `lldpSilencePhrase`, which is the paragraph behind the help
 * tip: a plan is read at a glance, and a switch sitting there with nothing
 * attached is a question the operator is asking right now. The badge answers
 * it in the space a plan has; the paragraph says why.
 *
 * Returns null when there is NOTHING TO SAY — and that is the important
 * case, not an oversight. A device with links drawn needs no badge, and a
 * device whose neighborhood is simply known-and-empty (an unplugged spare)
 * must not be decorated with a warning: absence of an observation is not a
 * fault (cf. the `neighbor` prop in netui.tsx).
 */
export function lldpIsolationBadge(
  s: LldpSilence | undefined,
): { label: string; tone: "info" | "act" } | null {
  if (!s) return null;
  switch (s.reason) {
    case "platform_unsupported":
      // Nothing to repair, ever. INFO, never a call to act: orange on a
      // permanent platform limit is the thing UI-GUIDE forbids.
      return { label: i18n.t("lldp.badgePlatformUnsupported"), tone: "info" };
    case "lldp_disabled":
      // The device refused, and a human can lift the refusal. This is the
      // only one of the four that names a gesture on the equipment.
      return { label: i18n.t("lldp.badgeDisabled"), tone: "act" };
    case "not_served":
      return { label: i18n.t("lldp.badgeNotServed"), tone: "act" };
    default:
      return { label: i18n.t("lldp.badgeNeverPolled"), tone: "info" };
  }
}
