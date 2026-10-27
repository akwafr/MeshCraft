/**
 * HOW OLD IS WHAT THE SCREEN IS SHOWING — the two decisions behind that
 * question, kept OUT of React and out of i18n so they can be asserted directly
 * (cf. pages/portAdmin.ts, pages/hostnameConformity.ts: same pattern, same
 * reason). This module imports NOTHING, which is what lets its test run under a
 * bare `node --experimental-strip-types`.
 *
 * It answers for the CONFIGURATION, not for reachability. The two dates are
 * different in kind and must not be conflated:
 *
 *   status_at    when the device last CHANGED reachability state. Monitoring
 *                reports transitions only, so an old date on a healthy device
 *                is normal and means nothing about freshness — network.ts
 *                `reachability` says so in as many words.
 *   observed_at  when a connector last READ the device's configuration
 *                (store.SaveObserved stamps it on every read-state). This one
 *                IS a freshness date: it advances every time a reading lands,
 *                so it going quiet means readings have stopped.
 *
 * That distinction is the whole reason this file exists. `observed_at` has been
 * on the wire since the column was added and no screen has ever rendered it,
 * which is how "I clicked refresh and the port state changed — was I looking at
 * an hour-old screen before?" became unanswerable.
 */

/** Elapsed time carved into the units the UI is willing to say out loud. */
export interface Elapsed {
  unit: "now" | "min" | "h" | "d";
  count: number;
}

/**
 * Bucket an elapsed duration, rounded DOWN and without seconds beyond the
 * minute: precision adds nothing to "read 2 h ago", and it would make a line
 * that repaints every second out of one that repaints every minute.
 *
 * `null` for a duration that cannot be believed — not a number, or negative.
 * A negative age means the browser clock and the controller clock disagree, and
 * a screen that answers "read in -3 min" has told the operator less than a
 * screen that says nothing.
 *
 * The 48 h hinge before switching to days is inherited from `sinceLabel`, which
 * now shares this function rather than carrying a second copy of the
 * thresholds: two ladders would eventually disagree about the same instant, and
 * the same date would read "for 47 h" in one corner and "1 d ago" in another.
 */
export function elapsedBucket(ms: number): Elapsed | null {
  if (!Number.isFinite(ms) || ms < 0) return null;
  const min = Math.floor(ms / 60000);
  if (min < 1) return { unit: "now", count: 0 };
  if (min < 60) return { unit: "min", count: min };
  const h = Math.floor(min / 60);
  if (h < 48) return { unit: "h", count: h };
  return { unit: "d", count: Math.floor(h / 24) };
}

/**
 * What the freshness line is entitled to claim about a device's configuration.
 *
 *   never  no `observed_at` at all — no connector has ever read this device.
 *          `--text-dim`, per UI-GUIDE §2: "we have never looked" is not an
 *          observation problem, nothing was claimed in either direction, and
 *          yellow would announce a vantage point lost that was never held.
 *          The caller renders NO age for this — `syncLabel` already prints
 *          "Never reread" beside it, and two sentences for one fact read as
 *          two facts.
 *
 *   read   a reading landed, and meshcraft still has a way to take the next
 *          one. `--text-dim`: the age is context, not a verdict.
 *
 *   blind  a reading landed, and RIGHT NOW no proxy able to watch this device
 *          is connected. This is precisely "a reading that has stopped
 *          arriving" — the sentence UI-GUIDE §2 gives as an example of what
 *          `--indeterminate` is for — so the age turns yellow: it is not
 *          merely old, it is not going to advance.
 *
 * # What this deliberately does NOT do
 *
 * There is no staleness THRESHOLD here — no "older than N minutes is yellow".
 * Two reasons, and the second is the binding one:
 *
 *   - meshcraft has no fixed re-read cadence to measure against. A threshold
 *     picked in the frontend would be a guess about the controller's schedule,
 *     and the frontend would start crying stale on a fleet that is being read
 *     exactly as designed;
 *   - `--warn` is the most over-drawn token in this product (UI-GUIDE §2), and
 *     the failure mode of painting healthy devices is that operators learn to
 *     ignore the colour. An age the operator can read is enough to answer "is
 *     this live?"; a colour that fires on a schedule nobody agreed on is not.
 *
 * Whether an age alone is enough, or whether the product wants a real
 * "unacceptably stale" threshold once the controller's re-read loop has a
 * published period, is a product decision — not one to smuggle in as a
 * constant here.
 */
export type FreshnessKind = "never" | "read" | "blind";

export interface Freshness {
  kind: FreshnessKind;
  /** The token to ink the line with. A `var(--…)` string, ready to inline. */
  color: string;
}

/**
 * The device fields this verdict reads, spelled structurally rather than
 * imported from `api.ts`: importing that file would pull React through it and
 * this module would stop being runnable by a bare node process.
 *
 * `status` / `status_at` are read only to reproduce network.ts `blindNow` —
 * the canonical "nobody can see this device right now" predicate. It is
 * REPRODUCED and not imported for the same no-imports reason, and the test
 * pins the two to the same truth table so they cannot drift.
 */
export function configFreshness(dev: {
  observed_at?: string;
  status: string;
  status_at?: string;
}): Freshness {
  if (!dev.observed_at) return { kind: "never", color: "var(--text-dim)" };
  // blindNow(dev), in the one form this module is allowed to write it.
  if (dev.status === "unknown" && !!dev.status_at)
    return { kind: "blind", color: "var(--indeterminate)" };
  return { kind: "read", color: "var(--text-dim)" };
}
