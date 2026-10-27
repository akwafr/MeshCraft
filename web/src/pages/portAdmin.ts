/**
 * The two facts the port confirmation overlay has to get right, kept OUT of
 * React so they can be asserted directly (cf. poeOverride.ts, same pattern).
 *
 * Both are read from what the connector last observed, never from what a
 * button label happens to say — a "Shut" button offered on an already-shut
 * port is not a cosmetic slip, it is a gesture that would silently do nothing
 * and let the operator believe the port went down.
 */

/**
 * The port's ADMIN state as the connector reports it (PortIntent.link_state).
 *
 *   up           — link is carrying traffic;
 *   down         — administratively enabled, nothing plugged / no link;
 *   disabled     — administratively SHUT: this is the only value that means
 *                  the operator's own "shutdown" is in place;
 *   unplugged    — no transceiver in the cage;
 *   err-disabled — the SWITCH shut it itself (loop, BPDU guard, security).
 *                  The configuration still says "no shutdown": the admin
 *                  gesture available here is still "shut", and the recovery
 *                  belongs to whatever tripped it.
 *
 * Typed as a plain string on purpose: the field crosses the wire from a
 * connector, and a platform that one day reports a sixth word must degrade to
 * "not administratively shut" rather than crash a faceplate.
 */
export type LinkState = string | null | undefined;

export interface AdminGesture {
  /** The gesture SHUTS the port (false = it brings it back up). */
  shut: boolean;
  /**
   * The `enabled` boolean to put in the body of
   * POST /api/devices/{id}/ports/admin — the contract sends ENABLED, so
   * shutting a port is `false`. Never derive it from `shut` at the call site:
   * inverting a boolean twice across two files is how a shut becomes a no-shut.
   */
  enable: boolean;
  /**
   * The port carries a LIVE LINK and the gesture would drop it. The overlay
   * makes the confirm button wait for an explicit force checkbox in that case:
   * on the ranges with no serial port, shutting the jack that carries the
   * administration session costs a site visit, and there is no undo from here.
   */
  needsForce: boolean;
}

/** What a shut/no-shut click on this port would actually do. */
export function adminGesture(state: LinkState): AdminGesture {
  // "disabled" is the ONLY administratively-shut value. Everything else —
  // including err-disabled, whose configuration still reads "no shutdown" —
  // is a port the operator can still shut.
  if (state === "disabled") {
    return { shut: false, enable: true, needsForce: false };
  }
  return { shut: true, enable: false, needsForce: state === "up" };
}

/**
 * OBSERVED DHCP snooping trust for one port, or `undefined` when the device
 * never reported that port.
 *
 * The three-way answer is the point. driver.DHCPSnoopState omits a port whose
 * flag could not be read rather than defaulting it to false, because
 * "untrusted" (protected) and "not read" (unknown) lead an operator to
 * opposite conclusions — and collapsing them here would throw away the exact
 * distinction the connector took care to preserve.
 */
export function observedTrust(
  state: { ports?: { port: string; trusted: boolean }[] } | undefined,
  port: string,
): boolean | undefined {
  const row = state?.ports?.find((x) => x.port === port);
  return row ? row.trusted : undefined;
}

/**
 * The four things the "observed trust" field can be saying. Three come from
 * one port ([observedTrust]'s three-way answer); `mixed` only exists over a
 * selection, where the ports disagree.
 *
 * They are kept as four names rather than `boolean | undefined` twice over
 * because the two `undefined`s mean opposite things — "the device never
 * reported this port" versus "the ports disagree" — and the single-port and
 * multi-port panels each used to reach for whichever one was in hand.
 */
export type TrustObs = "trusted" | "untrusted" | "unread" | "mixed";

/** [observedTrust]'s answer for ONE port, named. */
export function trustToken(obs: boolean | undefined): TrustObs {
  return obs === undefined ? "unread" : obs ? "trusted" : "untrusted";
}

/**
 * THE INK ON THE OBSERVED DHCP-SNOOPING TRUST, one rule for both panels
 * (decided 2026-08-08, UI-GUIDE §6).
 *
 * The two panels contradicted each other on the same field of the same page.
 * Single port: unread → dim, trusted → `--warn`, untrusted → `--ok`. Multi
 * port: mixed → `--warn`, and every real value including unread → no colour.
 * Extending a selection from one port to two inverted the vocabulary under the
 * operator — orange stopped meaning "trusted" and started meaning "mixed" —
 * which is why the decision lives in one function and not in two JSX branches.
 *
 *	unread     --text-dim   we have never looked. The device did not report
 *	                        this port, and nothing is claimed in either
 *	                        direction. Grey, per §2's ladder;
 *	mixed      --text-dim   there is no single value. A selection that
 *	                        disagrees is a question with no answer, not a
 *	                        problem with the device;
 *	trusted    no colour    a DELIBERATE configuration. Every switch has at
 *	                        least one trusted jack — the uplink toward the
 *	                        DHCP server — so painting it orange would paint a
 *	                        correct facade orange and teach people to stop
 *	                        reading orange. It IS the state that disables
 *	                        protection on that port, and the word "trusted"
 *	                        is what says so;
 *	untrusted  --ok         confirmed good, observed: the protection is in
 *	                        place on this jack and the device said so.
 *
 * What this ink deliberately does NOT restate: whether the FEATURE is on at
 * all. `enabled: false` device-wide makes every trust flag inert, and the
 * panels print that above the field, in `--warn`, where it belongs — a
 * per-port ink that tried to carry a device-wide fact would say "untrusted is
 * fine" and "nothing is inspected" in the same three words.
 *
 * `undefined` rather than `var(--text)` for `trusted`: the caller spreads this
 * straight into an inline style, and no colour is what an unremarkable fact
 * looks like on this page.
 */
export function trustObsInk(tok: TrustObs): string | undefined {
  if (tok === "unread" || tok === "mixed") return "var(--text-dim)";
  if (tok === "untrusted") return "var(--ok)";
  return undefined;
}
