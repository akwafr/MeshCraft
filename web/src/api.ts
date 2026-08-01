// Minimal API client: fetch JSON with session cookie, typed errors.

import { useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import i18n from "./i18n";

export class ApiError extends Error {
  status: number;
  constructor(status: number, message: string) {
    super(message);
    this.status = status;
  }
}

/**
 * Identifier of THIS tab, drawn at random on load.
 *
 * It serves a single purpose: not being echoed its own writes by the
 * real-time stream. Whoever just wrote already has their HTTP response and
 * local invalidation; reloading a second time would show nothing new, and
 * during a drag-and-drop on the plan, that would be once per pixel.
 *
 * This is NOT an authentication identifier and it protects nothing: lying
 * about it only deprives oneself of the refresh.
 */
export const TAB_ID =
  globalThis.crypto?.randomUUID?.() ?? Math.random().toString(36).slice(2);

export async function api<T>(path: string, init?: RequestInit): Promise<T> {
  const method = init?.method ?? "GET";
  // Headers built here and set AFTER spreading `init`: spreading them before
  // would let an `init.headers` overwrite them, and the tab would stop
  // signing its writes without anything reporting it.
  const headers: Record<string, string> = {};
  if (init?.body) headers["Content-Type"] = "application/json";
  if (method !== "GET") headers["X-Meshcraft-Tab"] = TAB_ID;

  const res = await fetch(path, {
    credentials: "same-origin",
    ...init,
    headers,
  });
  if (res.status === 401 && !path.startsWith("/api/auth/")) {
    window.dispatchEvent(new Event("meshcraft:unauthorized"));
  }
  const text = await res.text();
  let data: unknown = null;
  try {
    data = text ? JSON.parse(text) : null;
  } catch {
    /* non-JSON response */
  }
  if (!res.ok) {
    const msg =
      (data as { error?: string } | null)?.error ?? i18n.t("api.httpError", { status: res.status });
    throw new ApiError(res.status, msg);
  }
  return data as T;
}

export const get = <T>(path: string) => api<T>(path);
export const post = <T>(path: string, body?: unknown) =>
  api<T>(path, { method: "POST", body: body ? JSON.stringify(body) : undefined });
export const patch = <T>(path: string, body: unknown) =>
  api<T>(path, { method: "PATCH", body: JSON.stringify(body) });
// Optional body: some routes only take their path as a parameter (adding a
// site to a proxy's coverage, for example).
export const put = <T>(path: string, body?: unknown) =>
  api<T>(path, body === undefined ? { method: "PUT" } : { method: "PUT", body: JSON.stringify(body) });
export const del = <T>(path: string) => api<T>(path, { method: "DELETE" });

// --- Model types (mirror of internal/model) ---

export interface Role {
  site_id: string;
  site_name?: string;
  role: "viewer" | "operator" | "site_admin";
}

export interface User {
  id: string;
  email: string;
  display_name: string;
  is_global_admin: boolean;
  created_at: string;
  roles?: Role[];
}

export interface Site {
  id: string;
  name: string;
  /** URL first segment (unique). API ids stay UUIDs. */
  slug: string;
  created_at: string;
}

export interface Vlan {
  id: string;
  site_id: string;
  vlan_id: number;
  name: string;
  color: string;
  purpose: string;
}

export interface Profile {
  id: string;
  site_id: string;
  name: string;
  spec: Record<string, unknown>;
  /** Switch profiles only: devices with this tag receive the profile. */
  tag?: string;
}

export interface DeviceGroup {
  id: string;
  site_id: string;
  name: string;
  device_profile_id: string | null;
  device_profile_name?: string | null;
}

export interface PortGroup {
  prefix: string;
  count: number;
  /** Index of the first port. Absent = 1 (catalog case); set by
   * configuration import, where numbering may start at 0. */
  from?: number;
  poe?: boolean;
  speed?: string;
  label?: string;
}

/** Per-port STP intent (edge / enable / bpdu guard) stored under ports[name].stp. */
export interface PortSTPIntent {
  /** "auto" | "yes" | "no" — spanning-tree edge policy. */
  edge?: "auto" | "yes" | "no" | boolean;
  /** Participate in spanning tree (SwOS ena / Instant On STP State). */
  enabled?: boolean;
  /** BPDU Guard (RouterOS bpdu-guard, Instant On CST — not SwOS). */
  bpdu_guard?: boolean;
  path_cost?: number;
}

/** Standard model of a port: intent + last observed state. */
export interface PortIntent {
  label?: string;
  mode?: "libre" | "access" | "trunk";
  pvid?: number | null;
  tagged?: number[] | "all";
  poe?: boolean;
  /** Per-port spanning-tree edge / enable. */
  stp?: PortSTPIntent;
  // observed state (demo: set by import, later by monitoring)
  link?: string | null;
  poe_w?: number;
  rx_mbps?: number;
  tx_mbps?: number;
}

/**
 * A supervision metric that the PLATFORM does not expose, with its reason.
 * Declared by the connector (driver.MetricUnavailable), served on the device page.
 *
 * Do not confuse with a metric meshcraft does not collect yet: both read the same
 * on screen — no chart — and lead to opposite decisions. A gap on our side is
 * filled by a release, and the operator is right to wait; what is declared here is
 * a fact of the device, and no release will change it.
 */
export interface MetricUnavailable {
  /** The metric, named as in the standard model (`optical_power`…). */
  metric: string;
  /**
   * Is the absence OBSERVED on hardware, or only assumed from product-line
   * documentation? Verified knowledge and assumed knowledge are not shown the same
   * way: conflating them would make an operator give up on an assumption that
   * newer firmware may disprove.
   */
  observed: boolean;
  /**
   * Where the knowledge comes from: the reading that establishes it (OIDs queried,
   * what they returned, firmware) when `observed`, the guide page otherwise. This
   * lets a skeptical operator redo the reading rather than trust us.
   * Technical evidence — operator copy is localized from `metric` in the UI.
   */
  source?: string;
}

export interface Device {
  /** Compliance with the site management plan (calculated by the controller). */
  mgmt?: import("./network").MgmtConformity;
  /**
   * RESOLVED management addressing mode, and where it comes from (migration 0027).
   * Calculated by the controller: effective mode depends on intent, what the
   * device carries AND the site plan, which the frontend does not have.
   */
  mgmt_addressing?: import("./network").MgmtAddressing;
  /** Last spanning-tree observation (from device re-read). */
  stp?: SpanningTreeState;
  /** Per-port STP edge / enable capabilities from the connector. */
  stp_caps?: STPCaps;
  /** THE INTENT recorded. Always present — “inherit” is one answer. */
  mgmt_addr?: import("./network").DeviceAddressing;
  /** What the connector read on the equipment. Absent = never read again. */
  mgmt_addr_observed?: import("./network").ObservedAddressing;
  mgmt_addr_observed_at?: string;
  id: string;
  site_id: string;
  group_id: string | null;
  name: string;
  model: string;
  vendor: string;
  location: string;
  mgmt_ip: string;
  /**
   * Reachability, established by ping monitoring — never by this screen.
   *
   * These three fields are READ ONLY: the API no longer accepts them as input.
   * Sending them in a PUT has no effect (the site plan still does this, and
   * republished the status read when the screen opened before the server stopped
   * listening).
   */
  status: "ok" | "warn" | "off" | "unknown";
  status_note: string;
  /**
   * Since when the device has been in this state. Absent = NEVER observed, which
   * is distinct from « unknown »: the former means monitoring has not spoken yet,
   * not that it does not know.
   *
   * This is NOT a last-seen date — monitoring only reports transitions, so a
   * device online for three months carries a three-month-old date without anything
   * being stale. Display as a state duration (« unreachable for 2 h »), never as
   * freshness.
   */
  status_at?: string;
  /**
   * SNMP collection reachability — distinct from ICMP `status`. A device can
   * answer ping while its agent is mute (wrong community, collector down).
   */
  snmp_status: "ok" | "off" | "unknown";
  snmp_status_note: string;
  snmp_status_at?: string;
  poe_budget_w: number;
  pos_x: number;
  pos_y: number;
  port_groups: PortGroup[];
  ports: Record<string, PortIntent>;
  extra: Record<string, string>;

  /**
   * Sync state, CALCULATED by the controller on every read
   * (see docs/01-Architecture/Reconciliation §6):
   *
   *   unknown — never re-read, we do not claim to know;
   *   in_sync — desired matches actual;
   *   pending — meshcraft has configuration not yet applied (blue);
   *   drift   — device was changed outside meshcraft (orange).
   */
  sync?: "unknown" | "in_sync" | "pending" | "drift";
  /**
   * What the last apply did — and especially what it deliberately left aside.
   * Without this trace, a protective guardrail looks like a bug.
   */
  last_apply?: {
    at?: string;
    ports_written?: string[];
    vlans_created?: number[];
    skipped?: string[];
    /**
     * What the PLATFORM cannot write — PoE on a port, for example.
     * Distinct from `skipped`: a spared port is a safety refusal the operator can
     * override by forcing; an unsupported intent cannot be forced. The device is
     * considered up to date for everything else.
     */
    unsupported?: string[];
    /**
     * What the connector understood from the device by RE-READING it after apply,
     * and that the standard model cannot express.
     *
     * Third category, not to be confused with `skipped` or `unsupported`: a VLAN
     * configuration can be PRESENT AND INERT. On SwOS, a port whose VLAN mode is
     * « disabled » filters nothing; on RouterOS, a bridge with inactive
     * `vlan-filtering` lets everything pass everywhere, and `ingress-filtering:
     * false` only filters on egress. An apply can therefore succeed, be verified by
     * re-read, counted conformant, and have no effect.
     *
     * These are PHRASES written by the connector: the screen displays them as-is
     * and draws no decision from them. There is no button to offer — enabling VLAN
     * filtering on a bridge is not a gesture meshcraft takes for a human who is
     * looking.
     */
    notes?: string[];
    remaining?: number;
    error?: string;
  };
  /** Details of discrepancies, port by port. */
  drift?: DriftChange[];
  /**
   * An apply ALREADY happened since the last change, and discrepancies remain.
   * Distinguishes the two « in progress » cases: one that resolves itself in the
   * coming seconds, and one that will never resolve without action — meshcraft
   * only re-applies after an intent change.
   */
  apply_stalled?: boolean;
  /**
   * NO proxy monitors this device, and here is why. The exclusion is deliberate —
   * a false state is worse than no state — but it must read on screen, otherwise
   * it looks like a meshcraft outage.
   */
  watch_excluded?: string;
  /**
   * What this device's PLATFORM does NOT expose for supervision — declared by
   * the connector for this model (metric key + observed + optional survey source).
   * Operator-facing explanations are localized in the UI from the metric key.
   *
   * Same family as `watch_excluded` just above: both answer « why did this
   * information disappear from my screen? ». The difference is what can be done —
   * a watch exclusion is fixed by clearing address ambiguity; a metric the device
   * does not publish cannot be fixed at all.
   *
   * ABSENT when the connector declares nothing, and that does NOT mean everything
   * else is available: it means nobody documented it. The screen must never turn
   * that absence into « no limitations ».
   */
  monitoring_unavailable?: MetricUnavailable[];
  /**
   * The network in which the management address makes sense. Empty for a manually
   * entered device: it stays monitored while alone on its address, and loses
   * arbitration against a device whose domain is declared.
   */
  address_domain_id?: string;
  /**
   * Reference COLLECTOR proxy: the one that polls this device via SNMP, and whose
   * address will be written into it as trap destination.
   *
   * Empty = COVERAGE mode, and that is normal operation, not a forgotten setting:
   * any proxy covering the site collects the device. Declaring a collector helps
   * when several proxies cover the same site — « several eligible proxies » does
   * not translate to one trap destination address, and two collections report each
   * port transition twice.
   *
   * Grants NO coverage: attachment to a proxy that does not cover the site does
   * not make it collect — it makes NOBODY collect. The screen must therefore
   * only offer proxies from /collector-proxies, and say when the current
   * attachment no longer appears there.
   */
  proxy_id?: string;
  /** The name of the collector, to display it without rereading the list of proxies. */
  proxy_name?: string;
  observed_at?: string;
}

/** A gap, as the spec formulates it: `port.GE4.pvid: 20 → 30`. */
export interface DriftChange {
  port: string;
  field: string;
  desired: string;
  observed: string;
}

export interface SpanningTreePort {
  port: string;
  role?: string;
  state?: string;
  path_cost?: number;
  root_path_cost?: number;
  edge?: boolean;
  /** Configured edge policy: auto | yes | no. */
  edge_mode?: "auto" | "yes" | "no" | string;
  bpdu_guard?: boolean;
}

export interface SpanningTreeState {
  mode?: string;
  enabled: boolean;
  bridge_priority?: number;
  bridge_id?: string;
  root_bridge_id?: string;
  root_priority?: number;
  is_root: boolean;
  root_path_cost?: number;
  ports?: SpanningTreePort[];
  notes?: string[];
}

/** Connector Capabilities.Features for per-port STP (served on deviceView). */
export interface STPCaps {
  known: boolean;
  edge: boolean;
  edge_auto: boolean;
  port_enable: boolean;
  bpdu_guard: boolean;
  /** Connector Capabilities.STPModes (none|stp|rstp|mstp). */
  modes?: string[];
}

export interface DeviceLink {
  id: string;
  site_id: string;
  a_device: string;
  a_port: string;
  b_device: string;
  b_port: string;
  speed: string;
  status: "up" | "warn" | "down";
  note: string;
}

/* --- LLDP neighborhood: the FINDING, alongside the intent above ---
 *
 * See docs/01-Architecture/LLDP neighborhood.md. A `DeviceLink` is an ENTERED
 * link: it survives unplugging, and that is its whole point since reality is
 * compared against it. What follows is what a device SAW a minute ago. Mixing
 * them would make an entered link disappear because a switch was unplugged ten
 * seconds ago.
 *
 * None of these shapes has a write route: an observation is not created by hand.
 */

/** A neighbor observed on a port, such as `GET /api/sites/{id}/lldp` renders it. */
export interface LldpNeighbor {
  device_id: string;
  site_id: string;
  /** FACADE port name, already resolved by the controller. */
  local_port: string;
  local_port_num?: number;
  chassis_id?: string;
  port_id?: string;
  port_desc?: string;
  sys_name?: string;
  sys_desc?: string;
  /** Since when has this neighbor been seen on this port, without interruption. */
  first_seen: string;
  last_seen: string;
  /** Park equipment recognized behind this neighbor. Blank = unknown, ordinary case. */
  peer_device?: string;
  peer_port?: string;
  /** What the reconciliation is based on: chassis | port_id | sys_name. */
  peer_via?: "chassis" | "port_id" | "sys_name";
}

/** A link DERIVED from one or two observations. Calculated on reading, never stored. */
export interface LldpLink {
  a_device: string;
  a_port: string;
  /** Empty = neighbor not in inventory: the link is still traceable. */
  b_device?: string;
  b_port?: string;
  b_label?: string;
  b_port_label?: string;
  b_chassis_id?: string;
  b_sys_desc?: string;
  /** Both ends are visible. FALSE is the NORMAL case, not an anomaly. */
  both_sides: boolean;
  first_seen: string;
  last_seen: string;
}

/** Equipment whose neighborhood is UNKNOWN, and why. */
export interface LldpSilence {
  device_id: string;
  /**
   * `platform_unsupported`: the connector declares, with manual reading, that this
   *   platform does not speak LLDP. It is therefore never queried, and no meshcraft
   *   release will change that (MikroTik on SwOS case).
   * `not_served`: it was queried and returned nothing under `1.0.8802`.
   * `never_polled`: no snapshot has arrived yet although the platform can render it —
   *   a collector is missing, or the first cycle has not run.
   *
   * All three show zero neighbors and are fixed by OPPOSITE actions: confusing them
   * sends you looking for a meshcraft fault where the firmware lacks the feature,
   * or the reverse.
   */
  reason: "platform_unsupported" | "not_served" | "never_polled";
  seen_at?: string;
  /** The reason that the connector declared, with its statement. Rendered as is. */
  detail?: string;
}

export interface SiteLldp {
  neighbors: LldpNeighbor[];
  links: LldpLink[];
  silent: LldpSilence[];
  silent_help: string;
  counts: { neighbors: number; links: number; silent: number };
}

export interface CatalogModel {
  model: string;
  vendor: string;
  desc: string;
  poe_budget_w: number;
  port_groups: PortGroup[];
}

export interface SitePlan {
  width?: number;
  height?: number;
  zones?: { x: number; y: number; w: number; h: number; label: string }[];
  /**
   * User-placed positions for unknown/unmanaged LLDP peers on the topology.
   * Keyed by a stable peer id (chassis / label / observing port).
   */
  lldp_peers?: Record<string, { x: number; y: number }>;
}

/** Canonical spec of a port profile (standard model). */
export interface PortProfileSpec {
  description?: string;
  mode: "access" | "trunk";
  pvid?: number | null;
  tagged?: number[] | "all";
  poe?: boolean;
  eng_only?: boolean;
  /**
   * Spanning-tree edge when the profile is applied.
   * true → edge yes; false → auto (safe default on access ports).
   */
  stp_edge?: boolean;
  /**
   * Per-port spanning-tree participation (SwOS ena / Instant On STP State).
   * true → enabled; false → disabled. Absent leaves the device value alone.
   */
  stp_enabled?: boolean;
  /** BPDU Guard when the profile is applied (RouterOS / Instant On). */
  bpdu_guard?: boolean;
}

// --- Switch profiles (see internal/portrules) ---

/**
 * Physical nature of a socket. It is not declared in `port_groups`: the controller
 * deduces it from prefix, label and speed, and deduces it once. The frontend never
 * re-does this classification — two deductions would eventually put the same socket
 * in two categories.
 */
export type PortMedia = "copper" | "sfp" | "sfp_plus";

/**
 * Rule selector: a FILTER on what the socket is, then a SLICE on the resulting
 * set. The slice is by rank in the filtered set, never by the number engraved on
 * the front panel — that is what makes a profile applicable to 48-, 24- and 8-port
 * switches.
 */
export interface SwitchRuleSelector {
  /** Substring of the facade group label, case insensitive. */
  group?: string;
  media?: "any" | PortMedia;
  /** PoE capacity of the socket, not the intention placed on it. */
  poe?: "any" | "yes" | "no";
  slice?: "all" | "first" | "last" | "range";
  count?: number;
  from?: number;
  to?: number;
}

/** What a rule requires: the four fields of the standard model, nothing more. */
export interface SwitchRuleConfig {
  mode: "access" | "trunk" | "libre";
  pvid?: number | null;
  tagged?: number[] | "all";
  poe?: boolean;
}

export interface SwitchRule {
  label: string;
  select: SwitchRuleSelector;
  /** The rule sets an existing grip profile rather than a written setting. */
  port_profile_id?: string;
  config?: SwitchRuleConfig;
}

export interface SwitchProfileSpec {
  description?: string;
  rules: SwitchRule[];
}

export interface FacadePort {
  name: string;
  group: string;
  media: PortMedia;
  poe_cap: boolean;
  speed: string;
}

/**
 * What a rule designated (`matched`) and what it obtained (`won`). They differ as
 * soon as a higher rule took the same sockets: a rule that designates thirty and
 * wins none is not wrong, it is masked — and that is indistinguishable if only one
 * of the two is shown.
 */
export interface SwitchRuleReport {
  index: number;
  label: string;
  matched: number;
  won: number;
  problem?: string;
}

export interface SwitchPortAssignment {
  port: string;
  group: string;
  media: PortMedia;
  poe_cap: boolean;
  rule: number;
  rule_label: string;
  config: PortIntent;
  /** What the rule required and which the socket cannot do. */
  warn?: string;
}

export interface SwitchProfilePlan {
  ports: SwitchPortAssignment[];
  rules: SwitchRuleReport[];
  /** Takes that no rule designates: they remain UNCHANGED. */
  uncovered: string[];
  /** Not empty = the installation is refused, en bloc. */
  issues: string[];
}

export interface SwitchProfilePreview {
  device: string;
  facade: FacadePort[];
  plan: SwitchProfilePlan;
  /** What “all” means on this site; empty = no VLAN declared. */
  site_vlans: number[];
  applied: number;
}

export interface Event {
  id: number;
  ts: string;
  site_id: string | null;
  device_id: string | null;
  port_name: string;
  type: string;
  severity: number;
  message: string;
  actor: string;
  payload: unknown;
}

/**
 * Fallback cadence of screens showing the fleet's LIVE state.
 *
 * This is no longer the normal mode — it's the SAFETY NET. The normal mode is
 * the site's event stream (useSiteLive below); this cadence only applies as
 * long as that stream isn't established.
 *
 * Aligned to the pace at which data actually arrives: the proxy flushes its
 * buffer every 10 s (see cmd/proxy/snmp.go). Polling faster would show
 * nothing more and would cost the controller one request per open screen per
 * second.
 *
 * react-query suspends this refresh when the tab isn't focused: a screen left
 * open in a background tab costs nothing.
 */
export const LIVE_REFRESH_MS = 10_000;

/**
 * Silence beyond which the stream is considered dead.
 *
 * The controller beats every 20 s (liveHeartbeat, internal/api/sitebus.go).
 * Two missed beats are enough to conclude: an SSE can die without the
 * browser seeing any error — a buffering reverse proxy, a corporate NAT that
 * drops an idle connection. Without this watchdog, the screen would stay
 * frozen while believing itself live, which is the worst of both worlds.
 */
const LIVE_SILENCE_MS = 50_000;

/**
 * Subscribes the screen to the site's change stream, and returns the value
 * to give to `refetchInterval`.
 *
 *   const refetchInterval = useSiteLive(siteId);
 *
 * `false` when the stream is established (no more periodic polling),
 * `LIVE_REFRESH_MS` otherwise.
 *
 * # What the stream carries
 *
 * Nothing useful, on purpose: it announces "this site changed". The client
 * is what fetches the data via the usual REST API. Pushing data through the
 * stream would create a second serialization path in parallel with REST, the
 * two would diverge, and the screen would end up showing what the API no
 * longer returns.
 *
 * # Why invalidation is BROAD
 *
 * The message does carry categories (`port`, `device`, `sync`…), but they
 * aren't read: any query whose key contains the site identifier is
 * invalidated. A category → keys table would be a third copy of the model,
 * to keep up to date with every screen added — and a miss there would
 * produce a frozen screen, the hardest failure to attribute. The cost is
 * nil in practice: react-query only reloads queries actually mounted, and
 * the controller has already batched the burst.
 *
 * # Fallback
 *
 * As long as the first `ready` message hasn't arrived, the screen polls
 * periodically. `onopen` isn't authoritative: a buffering reverse proxy lets
 * the connection open without letting anything through, and the screen
 * would believe itself live while facing dead data. A display that lies is
 * worse than a slow display.
 */
export function useSiteLive(siteId: string | undefined): number | false {
  const qc = useQueryClient();
  const [live, setLive] = useState(false);

  useEffect(() => {
    if (!siteId) return;
    let es: EventSource | null = null;
    let watchdog: ReturnType<typeof setTimeout> | undefined;
    let stopped = false;

    // Each sign of life resets the watchdog.
    const beat = () => {
      setLive(true);
      clearTimeout(watchdog);
      watchdog = setTimeout(() => {
        // The navigator saw nothing, but nothing more happens: we go back down
        // periodic polling AND we reopen, because an EventSource
        // silencer does not repair itself.
        setLive(false);
        connect();
      }, LIVE_SILENCE_MS);
    };

    const connect = () => {
      if (stopped) return;
      es?.close();
      es = new EventSource(
        `/api/sites/${siteId}/stream?tab=${encodeURIComponent(TAB_ID)}`,
      );
      es.addEventListener("ready", beat);
      es.addEventListener("ping", beat);
      es.addEventListener("change", () => {
        beat();
        qc.invalidateQueries({
          predicate: (q) => q.queryKey.includes(siteId),
        });
      });
      // The browser reconnects alone with the delay the server dictated.
      // We simply put the screen back on periodic polling while waiting: no need
      // to show an error — the user has nothing to do with this until the screen
      // is wrong again.
      es.onerror = () => setLive(false);
    };

    connect();
    return () => {
      stopped = true;
      clearTimeout(watchdog);
      es?.close();
      setLive(false);
    };
  }, [siteId, qc]);

  return live ? false : LIVE_REFRESH_MS;
}

export interface TrafficSeries {
  hours: number;
  /** true when no measured history — points may be an empty time grid. */
  simulated: boolean;
  points: { ts: string; rx_mbps: number | null; tx_mbps: number | null }[];
}

/**
 * A proxy is a network presence point. Its COVERAGE governs who sees it, who
 * administers it and for which sites it may serve
 * (see docs/01-Architecture/Proxy):
 *
 *   global = true  → instance proxy, usable everywhere (global-admin);
 *   global = false → restricted to `sites`. An empty list does not mean
 *                    « everywhere » but « nowhere ».
 *
 * Coverage does not say what the proxy can reach — that is observed — nor the
 * site of devices it discovers: that is chosen at adoption.
 */
export interface ProxyInfo {
  id: string;
  name: string;
  global: boolean;
  sites: { id: string; name: string }[];
  /** UTC build timestamp YYYYMMDD.HHMMSS. Empty = not yet checked. */
  protocol: number;
  os?: string;
  arch?: string;
  auto_upgrade: boolean;
  pin_version: string;
  upgrade_requested: boolean;
  /** '' | offered | downloading | applied | failed | up_to_date */
  upgrade_state: string;
  upgrade_version: string;
  upgrade_error: string;
  upgrade_started_at: string | null;
  upgraded_at: string | null;
  version: string;
  internal: boolean;
  revoked: boolean;
  created_at: string;
  last_seen: string | null;
  migrate_url: string;
  migrated_at: string | null;
}

/**
 * Site management addressing plan
 * (see docs/01-Architecture/Management addressing).
 *
 * source = "meshcraft" means meshcraft ALLOCATES the address and pushes it
 * statically — it is not a DHCP server.
 */
export interface MgmtPlan {
  site_id: string;
  vlan_id: number;
  untagged: boolean;
  subnet_v4: string;
  gateway_v4: string;
  subnet_v6: string;
  gateway_v6: string;
  dns: string[];
  ntp: string[];
  source: "dhcp_external" | "meshcraft";
  pool_start: string;
  pool_end: string;
  strict_plan: boolean;
  updated_at: string;
  /** false = the site inherits the cautious default, no explicit plan. */
  configured: boolean;
}

export interface MgmtPoolUsage {
  size: number;
  assigned: number;
  reserved: number;
  free: number;
}

/**
 * Search for a device that changed address (see internal/relocate).
 * `found_by` says who won: the scan or the operator.
 */
export interface Relocation {
  device_id: string;
  serial: string;
  prefix: string;
  state: "searching" | "found" | "not_found" | "cancelled" | "failed";
  scanned: number;
  total: number;
  found_ip?: string;
  found_by?: "scan" | "operator";
  error?: string;
  started_at: string;
  ended_at?: string | null;
}

/**
 * Site credential. The secret is never returned by the list: only `has_secret`
 * says it exists. Revealing it is a separate, audited call.
 */
export interface SiteCredential {
  id: string;
  site_id: string;
  /** Empty = default site identifier; otherwise, equipment group. */
  group_name: string;
  kind: string;
  username: string;
  has_secret: boolean;
  /**
   * The second secret phrase is set. Specific to `snmp_v3`, which derives TWO
   * keys — one to authenticate, one to encrypt. A single phrase for both would
   * feed the same value into both derivations: the encryption key would only be
   * the start of the authentication key.
   */
  has_secret2: boolean;
  note: string;
  rotated_at: string;
  created_at: string;
  /** A separate secret per device instead of a shared secret. */
  per_device: boolean;
  generations?: CredentialGeneration[];
}

/**
 * A version of a secret. `retiring` does not mean dead: devices still carry it,
 * and it is the only password that opens their door.
 */
export interface CredentialGeneration {
  id: string;
  credential_id: string;
  device_id?: string;
  generation: number;
  state: "pending" | "active" | "retiring" | "retired" | "compromised";
  created_at: string;
  activated_at: string | null;
  retired_at: string | null;
  /** Number of devices carrying this generation: this is what blocks a purge. */
  installed_on: number;
}

export interface Artifact {
  name: string;
  os: string;
  arch: string;
  size: number;
  sha256: string;
  url: string;
}

export interface ComposeResult {
  compose: string;
  image: string;
  insecure: string;
}

/** Installation without Docker: installation script + systemd unit only. */
export interface InstallResult {
  script: string;
  unit: string;
}

// --- Discovery and adoption ---
//
// A candidate does NOT have a site: the site is chosen for adoption
// (cf. docs/01-Architecture/Discovery).

/**
 * Transport on which a discovery credential makes sense.
 *
 * « any » applies to all TRIED transports (ssh and http): the same pair often
 * opens both, and entering it twice would make no sense.
 * « telnet » is stored but not tried — the discovery credentials screen says so,
 * because a kind stored but never attempted is the default that makes you hunt
 * for a password for hours.
 */
export type CredKind = "ssh" | "telnet" | "http" | "any";

export interface KnownCredential {
  id: string;
  name: string;
  kind: CredKind;
  username: string;
  priority: number;
  enabled: boolean;
  /** The secret is never returned; only its presence is known. */
  has_secret: boolean;
  created_at: string;
}

/**
 * Address domain: the space in which an IP address designates one machine and
 * only one.
 *
 * It exists because `192.168.1.10` identifies nobody: RFC 1918 space overlaps.
 * The same range behind two proxies may designate two unrelated networks, or one
 * network reached by two paths — and nothing meshcraft observes can decide. It is
 * therefore DECLARED.
 *
 * Two ranges attached to the same domain describe the same network: their
 * discoveries merge. Attached to two domains, they stay separate.
 */
export interface AddressDomain {
  id: string;
  name: string;
  note: string;
  /** Number of attached scan ranges. */
  prefixes: number;
  /** Names of proxies that declare a range in this domain. */
  proxies: string[];
  created_at: string;
}

export interface ScanPrefix {
  id: string;
  proxy_id: string;
  proxy_name?: string;
  /** The network that this range describes — the proxy only says where to go. */
  address_domain_id: string;
  domain_name?: string;
  prefix: string;
  enabled: boolean;
  note: string;
  last_run_at: string | null;
  last_result: string;
  created_at: string;
}

export interface Sighting {
  proxy_id: string;
  proxy_name: string;
  ip: string;
  latency_ms: number;
  last_seen: string;
}

export interface Discovered {
  id: string;
  source: "scan" | "manual" | "serial";
  proxy_id: string;
  proxy_name: string;
  /**
   * Network in which the address below makes sense. It, not the proxy, qualifies
   * the IP when neither serial nor MAC is known: two silent candidates at the same
   * address in two distinct networks remain two candidates, and one network seen
   * by two proxies does not make one.
   */
  address_domain_id: string;
  domain_name?: string;
  ip: string;
  mac: string;
  serial: string;
  hostname: string;
  vendor: string;
  model: string;
  driver: string;
  confidence: number;
  transports: string;
  fingerprint: Record<string, unknown>;
  has_config: boolean;
  credential_id: string | null;
  note: string;
  first_seen: string;
  last_seen: string;
  sightings: Sighting[];
  adopted_device_id: string | null;
  adopted_site_id: string | null;
  adopted_at: string | null;
}

export interface DriverRef {
  driver: string;
  vendor: string;
}

export interface ResetProcedure {
  driver: string;
  vendor: string;
  family: string;
  aliases: string[];
  software: string[];
  hardware: string[];
  erases: string;
  caveats: string[];
  /** false = the procedure varies depending on the model, to be confirmed on the product sheet. */
  verified: boolean;
}

export interface ParsedConfig {
  dialect: string;
  hostname?: string;
  vlans: { vlan_id: number; name: string }[];
  ports: Record<string, { label?: string; mode: string; pvid?: number; tagged?: number[] | "all" }>;
  port_groups: { prefix: string; from: number; to: number; speed_mbps: number; poe: boolean }[];
  warnings: string[];
}

export interface ProbeResult {
  ip: string;
  reachable: boolean;
  latency_ms: number;
  open_ports: number[];
  transports: string[];
  ssh_banner?: string;
  http_server?: string;
  http_title?: string;
  login_ok: boolean;
  credential_name?: string;
  /**
   * Result of trying credentials on the web interface. Three situations that must
   * not be confused: « refused » = a password to fix; « no-http-auth » = this
   * interface does not authenticate at HTTP level (form UI), so no credential was
   * presented to anyone; « error » = the attempt did not complete. Empty = attempt
   * not conducted.
   */
  web_auth?: "ok" | "refused" | "no-http-auth" | "error";
  web_auth_detail?: string;
  shell_output?: string;
  raw_config?: string;
  driver: string;
  vendor: string;
  model: string;
  hostname: string;
  serial: string;
  confidence: number;
  evidence: string;
  notes?: string;
}

