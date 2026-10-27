/**
 * The port panels' pure facts: the five decisions that turn a raw observation
 * into a sentence an operator reads. Out of React so they can be asserted
 * directly (portFacts.test.ts) — a label computed a second time at a call site
 * is how two panels start disagreeing about the same jack.
 */
import type { PortRef } from "../../network";

/**
 * Names an observed ADMIN state for the operator, falling back on the platform's
 * own token when it said something this list does not know.
 *
 * A raw word displayed beats a hidden state: a connector that one day reports a
 * sixth spelling must show it, not silently degrade to "down" — which is exactly
 * the confusion (err-disabled read as a plain down port) that this row exists to
 * remove.
 */
export function linkStateLabel(state: string, t: (key: string) => string): string {
  if (state === "down") return t("switchDetailPage.linkStateDown");
  if (state === "disabled") return t("switchDetailPage.linkStateDisabled");
  if (state === "unplugged") return t("switchDetailPage.linkStateUnplugged");
  if (state === "err-disabled") return t("switchDetailPage.linkStateErrDisabled");
  return state;
}

/**
 * The folded Vlan section's one-line summary.
 *
 * Says the MODE and the number that goes with it — the PVID on an access port,
 * the native VLAN on a trunk — because those are the two facts an operator opens
 * this section to check. Deliberately not the tagged list: it does not fit on a
 * summary line, and a truncated VLAN list is worse than none.
 */
export function vlanMeta(
  mode: string | undefined,
  pvid: number | null | undefined,
  t: (key: string, opts?: Record<string, unknown>) => string,
): string {
  if (mode === "access") {
    return t("switchDetailPage.vlanMetaAccess", { pvid: pvid ?? "—" });
  }
  if (mode === "trunk") {
    return pvid == null
      ? t("switchDetailPage.modeTrunkLabel")
      : t("switchDetailPage.vlanMetaTrunkNative", { pvid });
  }
  return t("switchDetailPage.modeFreeLabel");
}

/**
 * Names a PoE class for the operator. Falls back on the stored intent when no
 * observation exists yet (a device never re-read still has an answer), and on
 * the raw class token when the platform said something this list does not
 * know — a raw token displayed beats a hidden state.
 */
export function poeClassLabel(
  cls: string | undefined,
  intentOn: boolean | undefined,
  t: (key: string) => string,
): string {
  if (cls === "none") return t("switchDetailPage.poeClassNone");
  if (cls === "24v") return t("switchDetailPage.poeClass24v");
  if (cls === "54v") return t("switchDetailPage.poeClass54v");
  if (cls) return cls;
  return intentOn ? t("switchDetailPage.poeClassUnknownOn") : t("switchDetailPage.poeClassNone");
}

/**
 * Compacts a selection into ranges: "1-8, 12, 24-26". A panel that
 * announces "1, 2, 3, 4, 5, 6, 7, 8" forces you to count again to verify
 * that we actually selected what we thought we did.
 */
export function rangeLabel(sel: PortRef[], all: PortRef[]): string {
  // A port not found in `all` is discarded rather than crashing the
  // render: an incomplete label is a display glitch, an exception here
  // would blank out the whole page.
  const idx = sel.map((p) => all.indexOf(p)).filter((i) => i >= 0).sort((a, b) => a - b);
  if (idx.length === 0) return sel.map((p) => p.name).join(", ");
  const out: string[] = [];
  let i = 0;
  while (i < idx.length) {
    let j = i;
    while (j + 1 < idx.length && idx[j + 1] === idx[j] + 1) j++;
    const a = all[idx[i]].name;
    const b = all[idx[j]].name;
    out.push(i === j ? a : j === i + 1 ? `${a}, ${b}` : `${a}–${b}`);
    i = j + 1;
  }
  return out.join(", ");
}

/** Common value for the entire selection, or `undefined` if they differ. */
export function common<T>(sel: PortRef[], of: (p: PortRef) => T): T | undefined {
  const first = of(sel[0]);
  return sel.every((p) => of(p) === first) ? first : undefined;
}
