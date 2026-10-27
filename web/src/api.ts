// Minimal API client: fetch JSON with session cookie, typed errors.

import { useQueryClient } from "@tanstack/react-query";
import { noteServerDate } from "./ui/clock";
import { useEffect, useState } from "react";
import i18n from "./i18n";

export class ApiError extends Error {
  status: number;
  /**
   * What the FAILED write still managed to say — `result.notes` and
   * `result.skipped` from the error body, when the endpoint sends them.
   *
   * THIS EXISTS FOR ONE FAILURE MODE, and it is the worst one in the product: a
   * PoE power cycle whose RESTORE half failed. The connector's contract
   * (internal/driver/portadmin.go) requires it to name, in `result.notes`, the
   * class it could not put back — i.e. WHICH PORT IS DARK and what it should be
   * carrying. The controller duly returns that alongside `error` on its 502
   * (internal/api/portadmin_apply.go), and until this field existed the client
   * threw all of it away and kept the one sentence: the operator was told the
   * cycle failed, and not which camera stayed off.
   *
   * Undefined when the body carried no `result`, which is the ordinary case —
   * most refusals are a sentence and nothing more.
   */
  result?: { notes?: string[]; skipped?: string[]; not_applied?: { port?: string; kind: "guardrail" | "platform" | "device"; text: string }[] };
  constructor(
    status: number,
    message: string,
    result?: { notes?: string[]; skipped?: string[]; not_applied?: { port?: string; kind: "guardrail" | "platform" | "device"; text: string }[] },
  ) {
    super(message);
    this.status = status;
    this.result = result;
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
  // EVERY response measures the gap between the two clocks — ADR-0008. It is
  // used to CANCEL that gap, not to decide which side is right: nothing here
  // can tell, and the controller's host drifts exactly as a workstation does.
  noteServerDate(res.headers.get("Date"));
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
    const body = data as {
      error?: string;
      result?: { notes?: string[]; skipped?: string[]; not_applied?: { port?: string; kind: "guardrail" | "platform" | "device"; text: string }[] };
    } | null;
    const msg = body?.error ?? i18n.t("api.httpError", { status: res.status });
    // The result rides along when the endpoint sent one: a failed write can
    // still have facts to report, and the power-cycle restore is the case that
    // makes dropping them unacceptable (see ApiError.result).
    throw new ApiError(res.status, msg, body?.result);
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

/**
 * One personal API token, AS ITS OWNER'S LIST SHOWS IT (ADR-0011).
 *
 * Mirror of `model.APIToken` — and note what is NOT in it: the token's value.
 * It is shown once, in the response to the creation call
 * (`ApiTokenCreated.token`), and the server keeps only a hash. There is no
 * route that returns it, so a screen that loses it has lost it for good and
 * must say so to the person in front of it.
 *
 * Nobody ever sees another person's tokens — not even a global admin. These
 * routes are always about the signed-in caller.
 */
export interface ApiToken {
  id: string;
  name: string;
  /** Visible head, e.g. `mct_1a2b3c4d`. Not a credential: it cannot authenticate. */
  prefix: string;
  /** Default at creation. A read-only token is refused every write, 403. */
  read_only: boolean;
  /** EMPTY MEANS "no site restriction", not "no site". */
  site_ids: string[];
  created_at: string;
  /** null = never expires. The API hands out 90 days unless asked otherwise. */
  expires_at: string | null;
  /** null = never presented. Written on every accepted call. */
  last_used_at: string | null;
}

/**
 * The creation response — the one and only time the clear value exists outside
 * the holder's hands.
 */
export interface ApiTokenCreated extends ApiToken {
  token: string;
}

/**
 * What POST /api/auth/tokens accepts. Everything but `name` is optional, and
 * every default is the LEAST powerful option: read-only, 90 days.
 *
 * The scope can only REDUCE. Asking for `read_only: false` does not give a
 * viewer an operator's rights, and naming sites the caller has no role on
 * grants nothing — a token is its owner's rights at call time, minus this.
 */
export interface ApiTokenRequest {
  name: string;
  /** Default `true`. Omit to get the safe default; send `false` deliberately. */
  read_only?: boolean;
  /** Default `[]` = no site restriction. */
  site_ids?: string[];
  /** Default 90. `0` means "never expires". Max 3650. */
  expires_in_days?: number;
}

export interface Site {
  id: string;
  name: string;
  /** URL first segment (unique). API ids stay UUIDs. */
  slug: string;
  created_at: string;
  /**
   * The site's local DNS domain, pushed onto equipment at provisioning.
   *
   * EMPTY IS LEGITIMATE and means "this site has no domain" — connectors that
   * require one fall back to their own default rather than writing an empty
   * line, which on IOS would make SSH silently impossible.
   */
  local_domain?: string;
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
  /**
   * The hardware's SILKSCREEN prefix — what is printed on the faceplate
   * ("1/" for a FS S3900, giving "1/11") — as opposed to `prefix`, the
   * standard-model name the backend manipulates ("GE"). Absent on platforms
   * whose standard name already matches what is printed (RouterOS, Instant
   * On): the screen then falls back to the standard name.
   */
  silk?: string;
  /**
   * Faceplate ROW this group belongs on. Groups sharing the same `row`
   * render as ONE continuous block of ports in the equipment view, instead
   * of each group getting its own labelled section — for a connector that
   * splits its faceplate into several `port_groups` purely to carry
   * per-group metadata (media, PoE) that isn't uniform across the whole
   * range, without that split implying a physically separate block on the
   * real hardware. Absent (the default, every connector before this field
   * existed) means each group keeps its own row — nothing changes for a
   * platform that never sets it, INCLUDING one that already emits several
   * groups because they genuinely occupy separate blocks on the faceplate
   * (an SFP cage row below the copper ports, mismatched silkscreen numbering
   * spaces): that distinction is real hardware layout, and must stay drawn.
   *
   * cf. `ubiquiti-edgeswitch`'s copper + SFP+ groups, which sit in ONE row
   * on the real EdgePoint S16 faceplate (16 RJ45 then 2 SFP+ cages,
   * numbered continuously) even though they carry different `poe`/`label`.
   */
  row?: number;
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
  /**
   * The port profile (VLAN group) this port FOLLOWS — the group half of the
   * port panel's group/manual selector. Applying a profile records it; the
   * first MANUAL VLAN edit clears it (the port leaves its group by changing
   * lane, not by flipping the selector). PoE and STP touches deliberately do
   * NOT clear it: those are per-port overrides on top of the group, and the
   * panel shows them as such. A `profile_id` whose profile was deleted is a
   * dangling reference the panel says so about, never an error.
   */
  profile_id?: string | null;
  /**
   * DHCP snooping TRUST for this port — and trust is the whole meaning of the
   * flag, not "snooping on/off here" (cf. internal/driver/dhcpsnoop.go).
   *
   *   true  — the port is TRUSTED: DHCP OFFER/ACK arriving on it are let
   *           through. That is the uplink toward the real DHCP server, or
   *           toward another switch — the exception, never the rule;
   *   false — UNTRUSTED: server replies arriving here are dropped. This is
   *           where the protection actually lives, and it is the safe default
   *           on every platform, so an absent flag reads as untrusted.
   *
   * The device-wide feature is NOT this flag's business: global snooping and
   * the per-VLAN enable are turned on by the CONNECTOR as part of the apply
   * (DHCPSnoopApplier), and the VLANs come from the site plan, resolved
   * server-side. A screen that showed a trust flag on a device whose global
   * snooping is off would show an exact configuration protecting nothing —
   * which is why the port panel reads `Device.dhcp_snoop_state.enabled` next
   * to this toggle rather than letting the flag speak alone.
   *
   * Same two halves as `poe`: this is the STORED intent, written through
   * PUT /api/devices/{id}/ports like every other intent, while
   * POST /api/devices/{id}/dhcp-snooping/apply is the immediate wire write.
   * Deliberately not one call — a device that refuses the write must not lose
   * the recorded decision (cf. internal/api/poe_apply.go).
   */
  dhcp_snooping?: boolean;
  /** Per-port spanning-tree edge / enable. */
  stp?: PortSTPIntent;
  // observed state (demo: set by import, later by monitoring)
  link?: string | null;
  /**
   * Operational state lifted from the last connector read (up / down / disabled
   * / unplugged / err-disabled). Distinct from `link`, which is the LIVE SNMP
   * speed label: SNMP cannot tell an err-disabled port from a plain down one, so
   * that distinction can only come from the read (cf. mergeLinkState server-side).
   */
  link_state?: string | null;
  /** The switch's own reason when it shut the port itself, verbatim. */
  err_reason?: string | null;
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
  /**
   * MST region conformity: what this device carries against what its site
   * wants. Drives the spanning-tree banner (ADR-0023). Absent means the
   * controller has nothing to say — NOT that the device conforms.
   */
  stp_region?: STPRegionConformity;
  /**
   * SUPER ROOT intent: this switch is to win the root election of its MST
   * region and keep winning it (priority 0 on instance 0).
   *
   * AN INTENT, NOT AN OBSERVATION — unlike everything in `stp` above it. The
   * checkbox renders THIS and must never be inferred from
   * `stp.bridge_priority === 0`: a switch somebody set to 0 by hand years ago
   * would tick a box meshcraft was never asked for, and clearing it would then
   * offer to "restore" a priority meshcraft never chose.
   *
   * Clearing it requires sending `bridge_priority` in the same apply — there is
   * no recorded previous value to put back (the controller answers 400 without
   * one).
   */
  stp_super_root?: boolean;
  /** Last PoE observation (from device re-read): per-port scope, class, draw. */
  poe_state?: PoEState;
  /** Whether the connector can WRITE PoE — gates the panel's immediate on/off. */
  poe_caps?: PoECaps;
  /**
   * Which traffic classes this device's platform can COUNT per port, for the
   * port overlay's stacked packets-per-second graph (ADR-0028).
   *
   * Read for what it can say about an EMPTY band, which the samples cannot: a
   * series with no points means either "collection just started" or "this
   * platform will never produce it", and those call for opposite reactions.
   * Absent, or `known: false`, means we cannot tell which — and the overlay
   * must say exactly that rather than "the platform says no".
   */
  traffic_class_caps?: TrafficClassCaps;
  /**
   * Last transceiver-diagnostics observation (from device re-read): module
   * identity and DDM readings, per OCCUPIED optical cage.
   *
   * Absent means "never read, or this connector cannot read optics" — the two
   * are not distinguished on the wire, and neither is "the cages are empty".
   * The port panel renders the section only when this is present, for exactly
   * that reason.
   */
  optics_state?: OpticsState;
  /**
   * Which per-port ACTIONS this device's platform can carry — shut/no-shut and
   * PoE power cycle. Gates the buttons of the double-click overlay.
   */
  port_caps?: PortActionCaps;
  /**
   * What this device's platform can do with AGGREGATES. Read by the
   * faceplate's aggregate block (ADR-0007): whether to draw the dashed "+"
   * at all, and whether the builder shows the LACP fallback field.
   */
  lag_caps?: LagCaps;
  /**
   * Last DHCP snooping observation (from device re-read). Absent means the
   * device was never read for it, or its connector does not read it — NOT
   * that snooping is off; the port panel says so rather than assuming.
   */
  dhcp_snoop_state?: DHCPSnoopState;
  /** THE INTENT recorded. Always present — “inherit” is one answer. */
  mgmt_addr?: import("./network").DeviceAddressing;
  /** What the connector read on the equipment. Absent = never read again. */
  mgmt_addr_observed?: import("./network").ObservedAddressing;
  mgmt_addr_observed_at?: string;
  id: string;
  site_id: string;
  group_id: string | null;
  name: string;
  /**
   * The model string EXACTLY as the device — or an operator — declared it.
   *
   * A piece of evidence, not a name: one vendor can publish two naming
   * grammars for one switch, and both reach us. Show `model_label` instead,
   * and keep this one for the tooltip and for diagnosis (ADR-0014).
   */
  model: string;
  /**
   * The canonical identity of the hardware, derived by the controller: the
   * agreed name when a family rule recognized the raw label, the raw label
   * itself otherwise. THIS is what a screen names the device.
   *
   * Absent on a device with no model at all. Use `modelOf()`
   * (web/src/modelLabel.ts) rather than reading it directly.
   */
  model_label?: string;
  /** Stable key of that identity: same key = same model, whichever grammar. */
  model_key?: string;
  /**
   * The vendor catalogue reference, when the raw label carried one ("JL685A").
   * More precise than the identity and deliberately not part of it — two of the
   * three Instant On 1930 forms do not carry it.
   */
  model_sku?: string;
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
   * is distinct from "unknown": the former means monitoring has not spoken yet,
   * not that it does not know.
   *
   * This is NOT a last-seen date — monitoring only reports transitions, so a
   * device online for three months carries a three-month-old date without anything
   * being stale. Display as a state duration ("unreachable for 2 h"), never as
   * freshness.
   */
  status_at?: string;
  /**
   * SNMP collection reachability — distinct from ICMP `status`. A device can
   * answer ping while its agent is mute (wrong community, collector down).
   */
  /**
   * COLLECTION reachability, whatever protocol collects. See `collect_kind` for
   * which one — the field is named `snmp_` for history, not for meaning.
   */
  snmp_status: "ok" | "off" | "unknown";
  /** "snmp" | "gnmi": which collector this device's connector declares. */
  collect_kind?: string;
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
   * (see docs/en/01-Architecture/Reconciliation §6):
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
     * Structured entries an apply did not land: which port, which kind of
     * "no", and the connector's sentence. `guardrail` invites a forced retry;
     * `platform` and `device` never do — the platform lacks the knob, or the
     * device itself declined the write on the verifying re-read. `skipped`
     * above is the not-yet-migrated legacy remainder (treated as guardrails).
     */
    not_applied?: { port?: string; kind: "guardrail" | "platform" | "device"; text: string }[];
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
     * "disabled" filters nothing; on RouterOS, a bridge with inactive
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
   * Distinguishes the two "in progress" cases: one that resolves itself in the
   * coming seconds, and one that will never resolve without action — meshcraft
   * only re-applies after an intent change.
   */
  apply_stalled?: boolean;
  /**
   * An automatic apply is writing to this device RIGHT NOW. Live runtime state,
   * not stored: the dot and the ports being written animate as "provisioning"
   * while it lasts, then settle when it ends.
   */
  applying?: boolean;
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
   * Same family as `watch_excluded` just above: both answer "why did this
   * information disappear from my screen?". The difference is what can be done —
   * a watch exclusion is fixed by clearing address ambiguity; a metric the device
   * does not publish cannot be fixed at all.
   *
   * ABSENT when the connector declares nothing, and that does NOT mean everything
   * else is available: it means nobody documented it. The screen must never turn
   * that absence into "no limitations".
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
   * when several proxies cover the same site — "several eligible proxies" does
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
  /**
   * Does the EQUIPMENT carry the name meshcraft gives it? Computed by the
   * controller on every read (api.deviceView.Hostname), which alone knows
   * whether the platform can be renamed at all and whether a repair is queued.
   *
   * ABSENT means the controller said nothing — an older controller, a payload
   * that does not carry conformity — and the panel then shows no verdict at
   * all. It does NOT mean "the names agree": an assumed conformity is the one
   * claim this field exists to stop the screen from making.
   *
   * NOT NAMED `hostname`, and the controller's tag matches this deliberately
   * (cf. api.deviceView): three other payloads in this API carry `hostname` as a
   * plain STRING — discovery candidate, parsed configuration, identity — so an
   * object living at that key would be the one field whose type nobody predicts
   * from its name. Both spellings were accepted for a while, which was the
   * correct hedge while the wire name was unsettled and the wrong thing to leave
   * behind: guessing wrong here fails SILENTLY, as a row that never renders on a
   * screen where "nothing shown" is legitimate.
   *
   * The shape lives in pages/hostnameConformity.ts with the mapping that reads
   * it: that module must stay importable by a bare node process for its test,
   * so it cannot import this file, and a second structural copy of the contract
   * here would drift from it on the first change.
   */
  hostname_conformity?: import("./pages/hostnameConformity").HostnameConformity;
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
  /**
   * The MST region the device REPORTS being in — read back because a region
   * that does not match is invisible any other way: 802.1s handles a mismatch
   * by silently treating the neighbour as an ordinary RSTP bridge.
   *
   * Do NOT compare it with the site's region here. The verdict is served
   * pre-computed as `Device.stp_region`, and the reason is two normalisations
   * that are 802.1s rules rather than presentation: instance 0 is the CIST and
   * implicit, and a name equal to the bridge's own address is the factory
   * default — "a region of one", not a region called that (ADR-0023).
   */
  region?: MSTRegionObserved;
  ports?: SpanningTreePort[];
  notes?: string[];
}

/** One bridge's MST region as its connector read it (driver.MSTRegion). */
export interface MSTRegionObserved {
  name?: string;
  revision: number;
  /** instance id → VLANs. Instance 0 (the CIST) is implicit and never listed. */
  instance_vlans?: Record<string, number[]>;
}

/**
 * How the MST region a device CARRIES compares with the one its site WANTS —
 * the whole input of the spanning-tree banner above the device sheet
 * (api.STPRegionView, ADR-0023).
 *
 * ABSENT MEANS "NOTHING TO SAY", NEVER "CONFORMANT". It is absent when the site
 * does not prefer MSTP, when the site has no region to want, and when this
 * device has never been read — and that last one matters: no banner over an
 * unread switch is not a clean bill of health.
 */
export interface STPRegionConformity {
  /**
   * `match` (no banner) · `drift` (carries a region, at least one component
   * differs) · `unregioned` (runs MSTP with no region — a region of one) ·
   * `not_mstp` (does not run MSTP at all, so it advertises no MST digest).
   *
   * The last two are deliberately not one verdict: they lead to different
   * gestures, and they are the two devices ARENA put side by side —
   * core01-arena with an empty region name, sw01-arena not in MSTP at all.
   */
  verdict: "match" | "drift" | "unregioned" | "not_mstp";
  /** Which of the three components disagree: "name" | "revision" | "instances". */
  differs?: string[];
  /**
   * The CONNECTOR emits a region at all (driver.FeatMSTRegion, ADR-0022). False
   * hides "Conformer": an action offered where it cannot land is worse than
   * no action, and fs-s3400 is the case — it declares STP writable and writes
   * no region.
   */
  can_join: boolean;
  want_name: string;
  want_revision: number;
  want_instances: { instance: number; vlans: number[] }[];
  have_name: string;
  have_revision: number;
  have_instances: { instance: number; vlans: number[] }[];
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
  /**
   * The connector declares WritesExtra("stp") — an Apply will reach the device.
   *
   * SEPARATE FROM `modes`, and the card must gate on this one. `modes` says
   * which protocols the RANGE runs, which is known for platforms meshcraft
   * cannot configure at all (arista-eos, whose STP write path has never touched
   * hardware): gating on `modes.length > 0` offered an Apply button that the
   * endpoint answered with 422.
   */
  write: boolean;
  /**
   * The connector IMPLEMENTS Super Root (driver.FeatSTPSuperRoot) — NOT the same
   * fact as `write`. `write` says its STP writes are declared; this says it
   * implements that one field. cisco/ios has `write` and not this, so a control
   * gated on `write` alone appeared on a live Catalyst.
   */
  super_root?: boolean;
  /**
   * The connector WRITES the MST region — name, revision and the VLAN-to-instance
   * table (driver.FeatMSTRegion). Again NOT the same fact as `write` or `modes`.
   *
   * The case that forces the distinction is fs-s3400: `write` is true (it declares
   * WritesExtra("stp")) and it writes no region at all, so sw01-arena sat in ARENA
   * carrying its own bridge MAC as a region name while the site's intended region
   * was displayed beside it. A screen that shows the site region per device must
   * gate on THIS, or it claims membership the connector never wrote (ADR-0022).
   */
  mst_region?: boolean;
}

/**
 * One port's PoE observation, as the connector last read it (driver.PoEPortState).
 * `capable` is the per-port truth — read from the device, never guessed from the
 * model name — and it is what gates the power controls: a mixed unit (EdgePoint
 * S16: 0/1-0/4 carry no PSE hardware, 0/5-0/16 do) makes the group-level
 * `port_groups[].poe` flag too coarse on its own.
 */
export interface PoEPortState {
  port: string;
  capable: boolean;
  /** "out" (feeds a device) or "in" (the switch is powered through this port). */
  role?: string;
  /** Configured class: "none" | "24v" | "54v". What the port OFFERS. */
  class?: string;
  /** A powered device is currently drawing. */
  delivering: boolean;
  power_w?: number;
  voltage_v?: number;
  current_ma?: number;
  temperature_c?: number | null;
  /** Platform's own wording for detection/fault state ("Open Circuit"…). */
  detection?: string;
  /** IEEE class the connected device negotiated — what the PD ASKED for. */
  negotiated_class?: string;
}

/** Device-wide PoE observation (Extra.poe_observed, refreshed on read-state). */
export interface PoEState {
  ports?: PoEPortState[];
  budget_w?: number;
  used_w?: number;
  notes?: string[];
}

/**
 * One OCCUPIED optical cage, as the connector last read it
 * (driver.OpticsPortState). Read-only: optics has no write path anywhere in
 * the product.
 *
 * THE PORT BEING IN THE LIST IS ITSELF THE OBSERVATION — it means a module was
 * seen in that cage. Every other field may be absent, and each absence means
 * something different (cf. `OpticsState` below).
 */
export interface OpticsPortState {
  /** Facade name ("XG1"), same vocabulary as the ports record. */
  port: string;
  /** What the module says about itself. Any may be empty. */
  vendor?: string;
  part_number?: string;
  serial?: string;
  /**
   * The module DECLARES diagnostics support. A module can be present and
   * answer "DDM:NO": it is still listed, with every reading absent, because
   * "a module that cannot measure" and "an empty cage" must not collapse into
   * one silence.
   */
  ddm: boolean;
  /**
   * The readings. OPTIONAL, and the Go side is explicit about why they are
   * pointers there and must stay optional here: **0.0 dBm is a legal transmit
   * power and a legal receive power**. An absent field is "the module did not
   * serve this reading"; a `0` is a measurement. A panel that printed `?? 0`
   * would turn the first into the second and invent data.
   */
  tx_power_dbm?: number;
  rx_power_dbm?: number;
  temperature_c?: number;
  voltage_v?: number;
  bias_current_ma?: number;
}

/**
 * Device-wide transceiver-diagnostics observation (Extra.optics_observed,
 * refreshed at read-state cadence), mirror of Go's driver.OpticsState.
 *
 * There is no `optics_caps` counterpart to `poe_caps`, deliberately: optics
 * has no write path, so the PRESENCE of this object is the whole capability
 * statement. Absent means one of two things the API does not separate — the
 * device was never read, or its connector cannot read transceivers at all —
 * and in both cases nothing was ever claimed about any cage.
 *
 * `ports` lists ONLY the cages a module was observed in. An empty cage serves
 * no transceiver block at all on the platform this was captured on, so a port
 * missing from the list is "no module", NOT "read and found nothing to say".
 * The one exception is a cage whose read FAILED: the connector keeps the
 * partial result and names the failure in `notes` (see `opticsCage`).
 */
export interface OpticsState {
  ports?: OpticsPortState[];
  /** Phrases for the operator, no decisions to draw. */
  notes?: string[];
}

/**
 * One port's OBSERVED DHCP snooping trust, read from the device.
 *
 * A port absent from `DHCPSnoopState.ports` was NOT READ, which is not the
 * same thing as untrusted: the first calls for "we do not know", the second
 * for "protected". The panel keeps them apart on screen for the same reason
 * the connector keeps them apart on the wire (driver.DHCPSnoopPortState).
 */
export interface DHCPSnoopPortState {
  port: string;
  /** DHCP server traffic is ACCEPTED here. False is the protected state. */
  trusted: boolean;
}

/**
 * Device-wide DHCP snooping observation (refreshed on read-state), mirror of
 * Go's driver.DHCPSnoopState.
 *
 * All three levels travel together on purpose. `enabled: false` means every
 * port's trust flag is INERT whatever it says — the switch inspects nothing —
 * and a screen that showed the port flag without that sentence would be
 * describing a protection that does not exist.
 */
export interface DHCPSnoopState {
  /** The switch-wide feature is on. */
  enabled: boolean;
  /** The VLANs snooping actually inspects, sorted. Empty on a platform with
   * no per-VLAN dimension — `notes` says which of the two it is. */
  vlans?: number[];
  /** One entry per port whose trust flag was read. Absent ≠ untrusted. */
  ports?: DHCPSnoopPortState[];
  /** What the connector understood that this type cannot express. */
  notes?: string[];
}

/**
 * What this device's PLATFORM can do with link aggregation, mirror of Go's
 * api.LAGCapsView (ADR-0007).
 *
 * `write` and `fallback` are SEPARATE because they gate different drawings:
 * `write` decides whether the faceplate offers the dashed "+" cell at all,
 * `fallback` decides whether the builder shows the LACP fallback VLAN field.
 *
 * And `fallback: false` means ABSENT, never disabled. Three states exist —
 * fallback set, fallback off, fallback impossible — and a greyed checkbox
 * collapses the third into the second, telling an operator a port falls back
 * when nothing will.
 */
export interface LagCaps {
  /** The connector was resolved. False = "we cannot tell", not "no". */
  known: boolean;
  /** This platform aggregates AND this connector has a proven write. */
  write: boolean;
  /** The connector honours an LACP fallback VLAN (gesture `lag-fallback`). */
  fallback: boolean;
  /** Platform bounds the write path enforces; 0 = none declared. */
  max_lags?: number;
  ports_per_lag?: number;
}

/** What this device's connector allows for PoE (served on deviceView). */
export interface PoECaps {
  known: boolean;
  /** The connector writes PoE: the panel's on/off reaches the device NOW. */
  write: boolean;
}

/**
 * Which traffic classes this device's PLATFORM can COUNT per port, mirror of
 * Go's api.TrafficClassCapsView (ADR-0028).
 *
 * ONE FLAG PER CLASS and not one "can decompose", because the fleet splits
 * exactly there: a platform serving the ifXTable per-class counters holds
 * multicast and broadcast and NOT unknown unicast, which no platform counts at
 * all. A single flag would promise the third wherever it promised the second.
 */
export interface TrafficClassCaps {
  /** A connector was resolved. False = "we cannot tell", not "no". */
  known: boolean;
  multicast: boolean;
  broadcast: boolean;
  /**
   * False on every connector today. The field exists so the overlay can SAY
   * the platform does not report this class, instead of dropping the band and
   * leaving a two-class stack looking like a complete one.
   */
  unknown_unicast: boolean;
}

/**
 * Which per-port ACTIONS this device's PLATFORM can carry, mirror of Go's
 * api.PortActionCapsView. Read by the double-click overlay so it offers only
 * buttons that will reach the hardware.
 *
 * Separate from PoECaps even though a power cycle rides on the PoE write path: a
 * connector can know how to SET a class without being able to promise the
 * restore half of a cycle, and half a power cycle is an outage
 * (cf. driver.PortPowerCycler). The UI must be able to offer one without the
 * other.
 *
 * There is no `read` field, for the same reason PoECaps has none: these are
 * actions, and what gates them is writability — known before any observation
 * exists.
 */
export interface PortActionCaps {
  /**
   * The connector was resolved at all. False means "we cannot tell", which is
   * NOT "the platform says no" — a device whose fingerprint is known but which
   * meshcraft cannot reach lands here.
   */
  known: boolean;
  /** Shut and un-shut will reach the device (WritesExtra("port_admin")). */
  admin: boolean;
  /**
   * The box could cycle PoE on SOME port (WritesExtra("poe")). NECESSARY BUT NOT
   * SUFFICIENT: the port must also be PoE-capable, which is a per-port fact read
   * from `poe_state` — a mixed unit (EdgePoint S16) has jacks with no PSE
   * hardware at all, and this flag cannot say which.
   */
  power_cycle: boolean;
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
 * See docs/en/01-Architecture/LLDP neighbours.md. A `DeviceLink` is an ENTERED
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
  /**
   * The far port this observation claims is held by SOMEONE ELSE, according to
   * a strictly fresher observation from that port's own device — a cable that
   * was moved while this side stopped being polled.
   *
   * A refuted observation is still LISTED: the device really did report it, and
   * hiding it would make a link vanish from the map with nothing on screen to
   * explain why. It simply no longer produces a topology edge. `refuted_by`
   * names the device now found on that far port.
   */
  refuted?: boolean;
  refuted_by?: string;
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
   * `lldp_disabled`: THE DEVICE SAID SO. Its neighbor table was read over the admin
   *   session and the device answered that LLDP is not enabled (the FS S3400 prints
   *   "LLDP is not enabled" verbatim). One command on the device repairs it, and
   *   meshcraft does not write it from this screen.
   * `not_served`: its SNMP agent was queried and returned nothing under `1.0.8802`.
   * `never_polled`: no snapshot has arrived yet although the platform can render it —
   *   a collector is missing, or the first cycle has not run.
   *
   * All four show zero neighbors and are fixed by OPPOSITE actions: confusing them
   * sends you looking for a meshcraft fault where the firmware lacks the feature,
   * or the reverse. `lldp_disabled` is split off `not_served` for exactly that
   * reason — the device's own refusal is the only one of the two an operator can
   * act on tonight, and reporting it as an empty `1.0.8802` subtree points at an
   * OID that was never queried.
   */
  reason:
    | "platform_unsupported"
    | "lldp_disabled"
    | "not_served"
    | "never_polled";
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

/** One row of the Clients page — a learned MAC, located and identified. */
export interface ClientRow {
  mac: string;
  /** IEEE OUI registrant; empty when the registry does not know. */
  vendor?: string;
  /** Locally-administered bit set: a randomized (privacy) address. */
  randomized?: boolean;
  /** What the machine announced over LLDP, when it speaks it. */
  name?: string;
  description?: string;
  device_id: string;
  device_name: string;
  port: string;
  /** Empty when the platform's table names no VLAN (flat BRIDGE-MIB). */
  vlans: number[];
  /** Other (device,port) pairs also sighting this MAC: the location hedge. */
  elsewhere?: number;
  speed_mbps?: number;
  link_up: boolean;
  online: boolean;
  first_seen: string;
  last_seen: string;
  /** The bandwidth bucket: port volume attributed while the MAC was there. */
  rx_bytes: number;
  tx_bytes: number;
  bucket_at?: string;
}

export interface SiteClients {
  clients: ClientRow[];
  counts: { clients: number; sightings: number; known: number };
  fresh_window_s: number;
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
  /** What the operator should read (hardware silkscreen number when the
   * connector declares one); same as `name` otherwise. */
  display: string;
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
  /** Not empty = the installation is refused as a whole. */
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
  /**
   * What an ANONYMOUS viewer is told instead of `message`.
   *
   * The public dashboard route omits the message body entirely — it is free
   * text composed by dozens of call sites and was observed carrying a
   * management address and an operator's email — and sends this closed-vocabulary
   * label in its place. Present only on the public payload; absent everywhere a
   * session exists, where `message` is the real thing.
   */
  label?: string;
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

/** One SFP cage receiving less light than its own history ever showed. */
export interface OpticsAlertRow {
  device_id: string;
  device: string;
  /** Faceplate port name, resolved server-side. */
  port: string;
  /** Transceiver channel; "" or "0" both mean the single-lane case. */
  lane?: string;
  rx_dbm: number;
  floor_dbm: number;
}

/** GET /api/sites/{id}/optics/rx-alerts — the low receive-level list. */
export interface OpticsAlerts {
  available: boolean;
  why?: string;
  /** The rule's parameters, so a caption never hard-codes a copy of the rule. */
  margin_db: number;
  window_days: number;
  rows: OpticsAlertRow[];
}

/**
 * A proxy is a network presence point. Its COVERAGE governs who sees it, who
 * administers it and for which sites it may serve
 * (see docs/en/01-Architecture/Proxy):
 *
 *   global = true  → instance proxy, usable everywhere (global-admin);
 *   global = false → restricted to `sites`. An empty list does not mean
 *                    "everywhere" but "nowhere".
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
 * (see docs/en/01-Architecture/Management addressing).
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
// (cf. docs/en/01-Architecture/Discovery).

/**
 * Transport on which a discovery credential makes sense.
 *
 * "any" applies to all TRIED transports (ssh and http): the same pair often
 * opens both, and entering it twice would make no sense.
 * "telnet" is stored but not tried — the discovery credentials screen says so,
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
  /**
   * The brand's grouping identity — a slug, never shown (ADR-0010). `vendor`
   * is the label: two procedures under one brand carry the same `vendor`
   * string, pinned by `internal/discovery/reset_brand_test.go`, so grouping
   * on this field and labelling from that one cannot disagree.
   */
  brand: string;
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
   * not be confused: "refused" = a password to fix; "no-http-auth" = this
   * interface does not authenticate at HTTP level (form UI), so no credential was
   * presented to anyone; "error" = the attempt did not complete. Empty = attempt
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

/* --- Site extensions and the inventory they mirror (ADR-0015, ADR-0016) --- */

/**
 * A foreign controller a site is backed by — UniFi OS being the first.
 *
 * The token never appears here: `has_token` says it exists, and reading it is a
 * separate, audited call, exactly like a site credential.
 */
export interface SiteExtension {
  id: string;
  site_id: string;
  /**
   * `unifi_os` today. A plain string and not an exhaustive union: a controller
   * may implement a kind this frontend does not know, and the list must still
   * render instead of failing to type-check the day one is added.
   */
  kind: string;
  label: string;
  base_url: string;
  has_token: boolean;
  verify_tls: boolean;
  /**
   * `direct` uses the controller's own egress; `proxy` goes out through the
   * proxy already posted on a site (ADR-0015 §3).
   */
  transport: "direct" | "proxy";
  proxy_id?: string;
  enabled: boolean;
  /** The STABLE identifier of the paired remote site. This is the key. */
  remote_site_id: string;
  /** UniFi's short reference. Needed because the SSID read is on the legacy API. */
  remote_site_ref: string;
  /**
   * The display name at the last contact. A LABEL — it will be renamed and the
   * pairing survives it. Never match on this.
   */
  remote_site_name: string;
  paired_at: string | null;
  /**
   * Set only when the controller ANSWERED and the paired site was absent from
   * its list. Distinct from unreachable: the mirror is kept, stamped stale.
   */
  orphaned_at: string | null;
  last_sync_at: string | null;
  /** Verbatim failure of the last attempt. Displayed as-is, never rewritten. */
  last_error: string;
  created_at: string;
  updated_at: string;
}

/** One 802.11 radio of an imported access point. A radio is not a port. */
export interface ExtRadio {
  standard?: string;
  /** Band in GHz (2.4, 5, 6) — a band, not a port speed. */
  band_ghz?: number;
  channel?: number;
  width_mhz?: number;
  tx_retries_pct?: number;
}

/** One copper port of an imported box. Two, on an AC Pro — not a faceplate. */
export interface ExtPort {
  index: number;
  state?: string;
  connector?: string;
  speed_mbps?: number;
  max_speed_mbps?: number;
}

/**
 * One row of the read-only mirror. NOT a `Device` and never becomes one:
 * `devices` holds what meshcraft configures, with a connector (ADR-0015 §5).
 */
export interface ExtDevice {
  id: string;
  extension_id: string;
  site_id: string;
  external_id: string;
  class: "access_point" | "switch" | "gateway" | "other";
  name: string;
  model: string;
  firmware: string;
  /**
   * The foreign controller's own word, lowercased. Deliberately not mapped onto
   * a meshcraft status: we do not poll this box, so we have no basis to judge.
   */
  state: string;
  mgmt_ip: string;
  /** The only bridge to the switched world: LLDP and the FDB know this MAC. */
  mac: string;
  radios: ExtRadio[];
  ports: ExtPort[];
  seen_at: string;
}

/**
 * A service set of the SITE, not of an access point: UniFi configures them at
 * site level and every access point broadcasts them (ADR-0016).
 *
 * There is no passphrase field, deliberately: reading a controller does not
 * mean copying a production network's PSKs into a second database.
 */
export interface ExtSSID {
  id: string;
  extension_id: string;
  site_id: string;
  external_id: string;
  name: string;
  enabled: boolean;
  security: string;
  wpa_mode: string;
  bands: string[];
  seen_at: string;
}

/**
 * The SSID settings meshcraft can edit (ADR-0029).
 *
 * EVERY FIELD IS OPTIONAL and the omission is meaningful: absent means "not
 * part of this edit", which is what lets a form change two settings out of
 * twelve without asserting anything about the other ten. On the way IN (the
 * `live` read) an absent field means the controller does not report that key —
 * UniFi omits a field sitting at its default — and the form must then leave the
 * control alone rather than showing a guessed `false`.
 *
 * The names are 802.11's, not UniFi's: this is the shape a second Wi-Fi vendor
 * would have to fill (ADR-0016, Consequences).
 *
 * There is no passphrase here. It is write-only and travels on its own — see
 * ExtSSIDEdit.
 */
export interface ExtSSIDSettings {
  broadcasting?: boolean;
  name?: string;
  hidden?: boolean;
  /** "2.4" and "5", the screen's vocabulary — never the controller's 2g/5g. */
  bands?: string[];
  /** The controller's network object, which carries the VLAN. Opaque. */
  network_id?: string;
  guest?: boolean;
  client_isolation?: boolean;
  fast_roaming?: boolean;
  bss_transition?: boolean;
  power_save?: boolean;
  multicast_enhance?: boolean;
  proxy_arp?: boolean;
}

/**
 * One service set as the CONTROLLER currently describes it.
 *
 * Read live, never from the mirror: the edit's precondition has to compare
 * against the authority, and a copy that is even thirty seconds old would let a
 * concurrent change through (ADR-0029 §6).
 */
export interface ExtSSIDDetail {
  id: string;
  settings: ExtSSIDSettings;
  /** Shown, not editable — ADR-0029 §9 says why. */
  security: string;
  wpa_mode: string;
  /**
   * Whether a pre-shared key is set. This is ALL the browser is ever told about
   * it: the value stays on the controller-facing side (ADR-0029 §7).
   */
  has_passphrase: boolean;
  /**
   * False when the service set carries a band this build cannot write (6 GHz).
   * Editing then would silently drop it, so the control is shut and the form
   * says why.
   */
  bands_editable: boolean;
}

/** A network of the controller — what an SSID is attached to, hence its VLAN. */
export interface ExtNetwork {
  id: string;
  name: string;
  /** 0 means untagged, which is how UniFi expresses it. */
  vlan: number;
  /** The controller's own word ("corporate", "guest"). Relayed, not read. */
  purpose: string;
}

/** What the SSID edit form is built from: the mirror row, the live state, the choices. */
export interface ExtSSIDForm {
  mirror: ExtSSID;
  live: ExtSSIDDetail;
  /** Null when the network list did not answer: the form degrades that field. */
  networks: ExtNetwork[] | null;
}

/**
 * One SSID edit: where each setting came from, and where it should go.
 *
 * `was` is NOT a convenience. It is the precondition: the server re-reads the
 * controller at the instant of the write and refuses the whole edit if any
 * field being changed no longer holds its `was` value. That is what catches
 * somebody editing the same SSID in the UniFi interface while this form was
 * open (ADR-0029 §6).
 *
 * `passphrase` is WRITE-ONLY and must be omitted entirely to leave the current
 * one alone. Sending an empty string is refused by the server rather than
 * silently blanking a production network's pre-shared key.
 */
export interface ExtSSIDEdit {
  was: ExtSSIDSettings;
  want: ExtSSIDSettings;
  passphrase?: string;
}

/** A site the foreign controller offers, for the pairing screen to choose from. */
export interface ExtRemoteSite {
  id: string;
  ref: string;
  name: string;
  paired: boolean;
}

/**
 * What one import pass did. Counted per class, because "12 imported" is useless
 * when the question is whether the switches arrived.
 */
export interface ExtSyncReport {
  remote_site_name: string;
  access_points: number;
  switches: number;
  others: number;
  ssids: number;
  removed: number;
  synced_at: string;
}

/**
 * What the extension's token is actually allowed to do (ADR-0025 §7).
 *
 * `writes` is never "denied": UniFi exposes `is_owner`/`is_super` plainly, and
 * a token that is neither has per-site rights the endpoint does not describe.
 * So the honest third value is "unproven" — the screen offers the gestures and
 * says the rights could not be established, rather than refusing a capability
 * it cannot disprove.
 */
export interface ExtRights {
  admin: string;
  writes: "granted" | "unproven";
  scoped: boolean;
}

/**
 * What one gesture did (ADR-0023 §6).
 *
 * `confirmed` is the field that matters and the reason this is not a bare 200.
 * The legacy half of UniFi's API acknowledges commands it has never heard of,
 * so the controller accepting a gesture is not evidence that it happened; the
 * server re-reads to check, and when the re-read cannot show the effect it says
 * so here rather than failing. A screen MUST branch on it: treating this as a
 * plain success is how an unconfirmed gesture becomes a confident toast.
 *
 * `because` is the server's sentence and is displayed verbatim.
 */
export interface GestureResult {
  gesture: string;
  ok: boolean;
  confirmed: boolean;
  because?: string;
}

/**
 * A box the controller can see and has not taken in.
 *
 * NOT part of the mirror, and not this site's inventory: a pending box belongs
 * to no site at all until adoption attaches it, so the same candidate is
 * offered by every site of the controller. The screen has to say that.
 */
export interface ExtCandidate {
  mac: string;
  model: string;
  /** UniFi's legacy type — "uap", "usw", "ugw". Raw, untranslated. */
  kind: string;
}

/**
 * The Wi-Fi screen's payload. The extensions travel with it so the screen can
 * say WHY it is empty — "none configured" and "never imported" are different
 * sentences, and an empty list that explains nothing reads as a bug.
 */
export interface SiteWiFi {
  access_points: ExtDevice[];
  ssids: ExtSSID[];
  extensions: SiteExtension[];
}

/* --- Alerting (ADR-0024) --- */

/**
 * The three alert classes a channel subscribes to.
 *
 * `described` is the counter-intuitive one, and the reason ADR-0024 exists: a
 * port alerts only if somebody typed a description for it on the Ports screen,
 * or if it faces a device the fleet knows. A port with neither wakes nobody —
 * the event still lands in the journal, it just does not leave the building.
 */
export type AlertClass = "device" | "interco" | "described";

/**
 * One notification destination.
 *
 * The token never appears here: `has_token` says it exists, and reading it is a
 * separate, audited call — the same regime as a site credential or an extension
 * token. Both kinds keep their secret in the vault, but they keep DIFFERENT
 * things there: Telegram's is a bot token paired with a `target` chat id, while
 * Slack's is the whole webhook URL, which is addressing and credential at once
 * and therefore leaves `target` empty.
 */
export interface NotifyChannel {
  id: string;
  /** Empty on an instance-wide channel, which covers every site. */
  site_id?: string;
  kind: "telegram" | "slack";
  label: string;
  enabled: boolean;
  /** Telegram chat id. Always empty for Slack. */
  target: string;
  has_token: boolean;
  classes: AlertClass[];
  /** Floor applied INSIDE the subscribed classes, 0-5 (model.Sev*). */
  min_severity: number;

  /**
   * Channel health. This is how "the channel has sent nothing" becomes an
   * answerable question instead of an ambiguity: a channel with a recent
   * `last_ok_at` and nothing pending is quiet because nothing happened, one
   * with failures is quiet because it is broken.
   */
  last_ok_at: string | null;
  last_error: string;
  consecutive_failures: number;
  /**
   * Port events refused for want of a description and of a known neighbour.
   * ADR-0024's accepted blind spot, turned into a number somebody can read —
   * which is the only honest mitigation of the exclusion rule.
   */
  dropped_uninteresting: number;

  /** Computed, never stored: slips waiting, and slips that gave up. */
  pending: number;
  failed: number;

  created_at: string;
  updated_at: string;
}

/** What the test route answers. `ok: false` carries the far end's own words. */
export interface NotifyTestResult {
  ok: boolean;
  error?: string;
}

