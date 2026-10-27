// Link aggregation, screen side: the types, the merge rule, and the colours.
import type { Device } from "./api";

/** One aggregate, as the API speaks it (driver.LAGState). */
export interface Lag {
  name: string;
  /** Device's own spelling ("bonding1") — read side only. */
  raw?: string;
  mode: "lacp" | "static";
  members: string[];
  vlan: { mode?: string; pvid?: number; tagged?: number[] };
  running?: boolean;
  /**
   * LACP FALLBACK: the untagged VLAN a member forwards in while LACP has not
   * converged. Absent or 0 is no fallback — members stay down until the far
   * end negotiates.
   *
   * A VLAN and not a boolean (ADR-0007): a server whose bond is not
   * configured yet must land in the SAME VLAN the aggregate carries, a port
   * an installer may plug anything into must land in a quarantined one, and a
   * boolean can only express one of the two.
   *
   * Only sent where the connector declares it (`lag_caps.fallback`). The
   * write path REFUSES an aggregate carrying it otherwise, rather than
   * trimming the field — see internal/api/lag_handlers.go.
   */
  fallback?: number;
}

/**
 * The display list: OBSERVED aggregates (devices.observed.lags — what the
 * device carries) merged with the INTENT (devices.extra.lags — what an
 * operator asked), intent winning by name. Observed-only entries are how an
 * IMPORTED aggregate appears before anyone edits it — the CRS305's bonding1
 * shows up located and coloured without a single meshcraft gesture.
 */
export function lagsOf(dev: Device): { list: Lag[]; observedOnly: Set<string> } {
  const observed =
    ((dev as unknown as { observed?: { lags?: Lag[] } }).observed?.lags ?? []) as Lag[];
  const intent =
    ((dev.extra as unknown as { lags?: Lag[] })?.lags ?? []) as Lag[];
  const byName = new Map<string, Lag>();
  observed.forEach((l) => byName.set(l.name, l));
  const observedOnly = new Set(byName.keys());
  intent.forEach((l) => {
    // The intent has no `running`: keep the observed answer when the device
    // gave one, so editing a LAG does not blank its convergence dot.
    const seen = byName.get(l.name);
    byName.set(l.name, { ...l, running: seen?.running, raw: seen?.raw });
    observedOnly.delete(l.name);
  });
  const list = [...byName.values()].sort((a, b) => a.name.localeCompare(b.name));
  return { list, observedOnly };
}

/**
 * The palette. Indexed by the aggregate's NUMBER, not its list position, so
 * LAG2 keeps its colour when LAG1 is dissolved — a colour that migrated
 * between aggregates on a delete would repaint members that did not change.
 * Hues chosen away from the faceplate's meaning-bearing colours (VLAN greens,
 * drift orange, fault red).
 */
export function lagColor(name: string): string {
  const n = parseInt(name.replace(/^\D+/, ""), 10) || 0;
  const hues = [265, 200, 320, 170, 35, 220, 290, 140];
  return `hsl(${hues[n % hues.length]} 85% 60%)`;
}

/** Member port → its aggregate, for the faceplate overlines. */
export function lagByMember(lags: Lag[]): Map<string, { name: string; color: string }> {
  const out = new Map<string, { name: string; color: string }>();
  lags.forEach((l) =>
    l.members.forEach((m) => out.set(m, { name: l.name, color: lagColor(l.name) })),
  );
  return out;
}

/**
 * The members of ONE aggregate, as a set — what the faceplate emphasises when
 * an aggregate is the selection (ADR-0007, decision 1).
 *
 * Separate from `lagByMember` on purpose: that map answers "which aggregate
 * owns this port" for every port at once (the overlines, always drawn), this
 * one answers "is this port in the aggregate currently selected" (the
 * emphasis, drawn for one). Reusing the first for the second would have meant
 * comparing a name on every square on every render.
 */
export function lagMemberSet(lags: Lag[], name: string | null): Set<string> {
  if (!name) return new Set();
  return new Set(lags.find((l) => l.name === name)?.members ?? []);
}

/** What travels to the API: never the view-only fields (`raw`, `running`). */
export function stripLagView(l: Lag): Lag {
  const out: Lag = { name: l.name, mode: l.mode, members: l.members, vlan: l.vlan ?? {} };
  // The fallback rides only when it is actually set: a `fallback: 0` in the
  // body would be an intent carrying the field, and a connector without the
  // gesture refuses the whole list on sight of it.
  if (l.fallback) out.fallback = l.fallback;
  return out;
}

/** Next free standard name: LAG1, LAG2… skipping the taken numbers. */
export function nextLagName(lags: Lag[]): string {
  const taken = new Set(lags.map((l) => parseInt(l.name.replace(/^\D+/, ""), 10) || 0));
  let n = 1;
  while (taken.has(n)) n++;
  return "LAG" + n;
}
