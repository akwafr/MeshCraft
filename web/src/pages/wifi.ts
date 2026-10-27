// Pure decisions of the Wi-Fi screen, out of the component so they can be
// tested without rendering (ADR-0016 is the model; this is its vocabulary).
//
// The whole file exists because of one rule: the units here are the RADIO's,
// not the copper's. A band is GHz, a width is MHz, a channel is a bare integer,
// and none of it goes through the traffic ramp of ADR-0003 — a radio capacity
// is not a port capacity, and the same ramp would state something false.

import type {
  ExtDevice,
  ExtRadio,
  ExtSSID,
  ExtSSIDEdit,
  ExtSSIDSettings,
  SiteExtension,
} from "../api";
import type { NavMode } from "../navMode";

/**
 * The navigation mode this screen belongs to — ADR-0013: every screen DECLARES
 * its mode, and a screen without one is a screen nobody finds.
 *
 * Wi-Fi is **switching**, and only switching: an access point bridges frames
 * onto a VLAN, it does not route (ADR-0016 §4). The authoritative entry is the
 * `wifi` row of `MODE_TABS` in `navMode.ts`, which is what actually builds the
 * bar; this constant is the screen's own side of that statement, typed so the
 * two cannot drift to a value `NavMode` does not have.
 */
export const WIFI_SCREEN_MODE: NavMode = "switching";

/** A band, as the screen groups by: the label is derived, never stored. */
export type BandKey = "2.4" | "5" | "6" | "other";

/**
 * Bucket a radio's frequency into a band.
 *
 * Tolerant on purpose: vendors report 2.4 as `2.4`, and a 6 GHz radio as `6`.
 * A frequency nobody recognises becomes `other` rather than being rounded into
 * a neighbouring band — showing a 6 GHz radio as 5 GHz would be a quiet lie on
 * a screen somebody uses to pick a channel.
 */
export function bandOf(ghz: number | undefined): BandKey {
  if (ghz === undefined || ghz === null) return "other";
  if (ghz >= 2 && ghz < 3) return "2.4";
  if (ghz >= 4.9 && ghz < 5.9) return "5";
  if (ghz >= 5.9 && ghz < 7.2) return "6";
  return "other";
}

/** The bands an access point actually radiates on, in ascending order. */
export function bandsOf(ap: ExtDevice): BandKey[] {
  const seen = new Set<BandKey>();
  (ap.radios ?? []).forEach((r) => seen.add(bandOf(r.band_ghz)));
  return (["2.4", "5", "6", "other"] as BandKey[]).filter((b) => seen.has(b));
}

/**
 * Normalise the band tags a controller puts on an SSID (`2g`, `5g`, `6g`) onto
 * the same BandKey the radios use.
 *
 * Without this the screen would show "2g" beside "2.4 GHz" for the same thing,
 * and the reader would reasonably wonder whether they are two different bands.
 */
export function ssidBands(ssid: ExtSSID): BandKey[] {
  const out = new Set<BandKey>();
  (ssid.bands ?? []).forEach((raw) => {
    const b = raw.trim().toLowerCase();
    if (b === "2g" || b === "2.4" || b === "2.4g") out.add("2.4");
    else if (b === "5g" || b === "5") out.add("5");
    else if (b === "6g" || b === "6") out.add("6");
    else out.add("other");
  });
  return (["2.4", "5", "6", "other"] as BandKey[]).filter((b) => out.has(b));
}

/**
 * Is this access point reporting itself as up?
 *
 * The foreign controller's word is kept verbatim in `state`, so this is the one
 * place it is interpreted — and it interprets only the affirmative case.
 * Anything that is not plainly "online" is NOT declared down: we do not poll
 * this box, so "disconnected", "pending" and "adopting" are states we relay
 * rather than judge.
 */
export function isOnline(ap: ExtDevice): boolean {
  return (ap.state ?? "").toLowerCase() === "online";
}

/** The uplink port of an access point: the one that is up, lowest index first. */
export function uplinkOf(ap: ExtDevice) {
  return (ap.ports ?? [])
    .filter((p) => (p.state ?? "").toLowerCase() === "up")
    .sort((a, b) => a.index - b.index)[0];
}

/**
 * Sort access points the way somebody looks for one: by name, naturally, so
 * "AP 2" comes before "AP 10". A nameless box falls back to its model and then
 * to its MAC, because an unnamed row at the top of a list reads as a bug.
 */
export function sortAccessPoints(aps: ExtDevice[]): ExtDevice[] {
  return [...aps].sort((a, b) =>
    labelOf(a).localeCompare(labelOf(b), undefined, { numeric: true, sensitivity: "base" }),
  );
}

/** What to call an imported box on screen. */
export function labelOf(d: ExtDevice): string {
  return d.name?.trim() || d.model?.trim() || d.mac || d.external_id;
}

/**
 * Why the Wi-Fi screen has nothing to show — because "nothing" has four
 * different causes and an empty table that explains none of them reads as a
 * broken page.
 *
 *  - `no-extension`: nothing is configured on this site.
 *  - `not-paired`:   an extension exists but no remote site was chosen.
 *  - `never-synced`: paired, never imported.
 *  - `empty`:        imported, and the remote site genuinely has no access point.
 */
export type WiFiEmptyReason = "no-extension" | "not-paired" | "never-synced" | "empty" | null;

export function emptyReason(
  extensions: SiteExtension[] | undefined,
  accessPoints: ExtDevice[] | undefined,
): WiFiEmptyReason {
  if ((accessPoints?.length ?? 0) > 0) return null;
  const exts = extensions ?? [];
  if (exts.length === 0) return "no-extension";
  if (!exts.some((e) => e.remote_site_id)) return "not-paired";
  if (!exts.some((e) => e.last_sync_at)) return "never-synced";
  return "empty";
}

/**
 * An extension whose pairing went stale. Surfaced rather than hidden: the
 * mirror is still shown (ADR-0015 §7), so something has to say that what is on
 * screen describes a site the controller no longer lists.
 */
export function orphanedExtensions(extensions: SiteExtension[] | undefined): SiteExtension[] {
  return (extensions ?? []).filter((e) => !!e.orphaned_at);
}

/** The label to show for an extension: the operator's, else the kind. */
export function extensionLabel(e: SiteExtension): string {
  return e.label?.trim() || e.kind;
}

/* --- The SSID editor's pure decisions (ADR-0029) --- */

/**
 * The boolean settings, in the order the form shows them.
 *
 * A list rather than a dozen hand-written rows: the set is closed on the server
 * (`ssidFields` in internal/unifi/ssidedit.go) and the two have to stay in
 * step, so one of them being enumerable makes the mismatch visible instead of
 * leaving a control that silently never sends anything.
 */
export const SSID_SWITCHES = [
  "broadcasting",
  "hidden",
  "guest",
  "client_isolation",
  "fast_roaming",
  "bss_transition",
  "power_save",
  "multicast_enhance",
  "proxy_arp",
] as const;

export type SSIDSwitchKey = (typeof SSID_SWITCHES)[number];

/**
 * Is this setting editable at all?
 *
 * FALSE WHEN THE CONTROLLER DOES NOT REPORT IT, and that is the whole point.
 * UniFi omits a key sitting at its default, so an absent field is a value
 * nobody read — and an edit has to state what it replaces (ADR-0029 §6). A
 * control offered on an unread value would send a precondition that is a guess,
 * and the edit would refuse for a reason the operator cannot see. So the
 * control is disabled and the form says why.
 */
export function ssidSwitchKnown(live: ExtSSIDSettings, key: SSIDSwitchKey): boolean {
  return live[key] !== undefined;
}

/** Two band lists are the same set, whatever the order the controller sent. */
export function sameBands(a: string[] | undefined, b: string[] | undefined): boolean {
  const x = [...(a ?? [])].sort();
  const y = [...(b ?? [])].sort();
  return x.length === y.length && x.every((v, i) => v === y[i]);
}

/**
 * Build the edit to send: for each setting that MOVED, where it was and where
 * it should go.
 *
 * Returns null when nothing changed, so the form can refuse to submit rather
 * than issuing a PUT that does nothing. A write at a live venue is never free.
 *
 * Only the changed fields travel. Sending the whole draft would make `was`
 * assert a precondition on nine settings nobody touched, and any one of them
 * drifting would then refuse an edit that had nothing to do with it.
 *
 * The passphrase is NOT part of the comparison: there is nothing to compare it
 * with, because the browser is never told the current one. A non-empty box
 * means "replace"; an empty box is omitted entirely, and omitted is what keeps
 * the current one (ADR-0029 §7).
 */
export function ssidEditPayload(
  live: ExtSSIDSettings,
  draft: ExtSSIDSettings,
  passphrase: string,
): ExtSSIDEdit | null {
  const was: ExtSSIDSettings = {};
  const want: ExtSSIDSettings = {};
  let moved = false;

  SSID_SWITCHES.forEach((key) => {
    // Never state a precondition on a value the controller did not report.
    if (!ssidSwitchKnown(live, key)) return;
    if (draft[key] === undefined || draft[key] === live[key]) return;
    was[key] = live[key];
    want[key] = draft[key];
    moved = true;
  });

  if (draft.name !== undefined && live.name !== undefined && draft.name !== live.name) {
    was.name = live.name;
    want.name = draft.name;
    moved = true;
  }
  if (
    draft.network_id !== undefined &&
    live.network_id !== undefined &&
    draft.network_id !== live.network_id
  ) {
    was.network_id = live.network_id;
    want.network_id = draft.network_id;
    moved = true;
  }
  if (draft.bands !== undefined && !sameBands(draft.bands, live.bands)) {
    was.bands = live.bands;
    want.bands = draft.bands;
    moved = true;
  }

  const secret = passphrase.trim();
  if (!moved && secret === "") return null;
  return secret === "" ? { was, want } : { was, want, passphrase: secret };
}

/**
 * How a network reads in the choice list: its name, and the VLAN it carries.
 *
 * The VLAN is shown because it is the thing an operator is actually choosing —
 * "Default" and "IoT" say nothing about where the traffic lands. Zero means
 * untagged, which is how UniFi expresses it, and it must read as a word rather
 * than as the number 0.
 */
export function networkLabel(name: string, vlan: number, untagged: string): string {
  const where = vlan > 0 ? `VLAN ${vlan}` : untagged;
  return `${name} — ${where}`;
}
