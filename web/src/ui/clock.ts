// Time, in the frame of the person looking at the screen — ADR-0008.
//
// (Supersedes ADR-0006, which measured in the CONTROLLER's frame. That fixed
// ages and left the fifteen absolute times in the product reading on a clock
// the operator cannot consult: "last event at 10:48" while their own watch
// says 10:54. The age was right and the hour was not, which is arguably worse
// than both being wrong together.)
//
// THE RULE. The viewer's clock is the reference. Every server timestamp is
// translated into it before being used — for an age, and for a displayed
// hour. We do NOT decide which clock is correct, because nothing here can:
// the controller's host drifts exactly as a workstation does, which is what
// its own log was reporting about the proxy that started all this.
//
//     offset  = serverNow − clientNow        (learned from the `Date` header)
//     tViewer = tServer − offset
//     age     = Date.now() − tViewer
//
// The age comes out identical to ADR-0006's — it is the same subtraction —
// but the displayed hour now lands on the operator's watch. Both readings
// agree with each other WITHOUT either clock having to be right.
//
// The cost, recorded in the ADR: displayed times will not match the
// server-side logs when the clocks disagree. That is the right side of the
// trade — the screen serves the operator, the log serves the investigation —
// and the banner in Configuration is where one learns the two differ.

/** serverNow − clientNow, in ms. Positive = our clock is BEHIND the server. */
let offsetMs = 0;
let seen = false;

/**
 * Feed a response's `Date` header. Called by the api layer on every response,
 * so a machine that resyncs mid-session or wakes from sleep corrects itself
 * instead of holding a stale offset all day. A missing or unparsable header
 * is ignored rather than treated as zero.
 *
 * The round trip lands inside this measurement, and the header has one-second
 * resolution. Both are noise at the tens-of-seconds thresholds this serves.
 * Do not reuse it to time anything fine.
 */
export function noteServerDate(header: string | null): void {
  if (!header) return;
  const t = Date.parse(header);
  if (Number.isNaN(t)) return;
  offsetMs = t - Date.now();
  seen = true;
}

/**
 * A server timestamp, moved into the viewer's frame. Returns ms since epoch
 * ON THE VIEWER'S CLOCK — feed it to `new Date(...)` to display.
 */
export function viewerMs(iso: string | null | undefined): number | null {
  if (!iso) return null;
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return null;
  return t - offsetMs;
}

/** The same, as a Date, for the fifteen places that format an absolute time. */
export function viewerDate(iso: string | null | undefined): Date | null {
  const ms = viewerMs(iso);
  return ms == null ? null : new Date(ms);
}

/**
 * Age of a server timestamp, in ms, in the viewer's frame.
 *
 * This is the one to use for every freshness reading. `Date.now()` stays
 * correct — and required — for anything LOCAL: a timer, an animation, a
 * debounce, the serial procedures' deadlines.
 */
export function ageMs(iso: string | null | undefined): number | null {
  const ms = viewerMs(iso);
  return ms == null ? null : Date.now() - ms;
}

/** The measured disagreement, for the banner. 0 until a response has landed. */
export function clockSkewMs(): number {
  return seen ? offsetMs : 0;
}

/**
 * Past this, the two clocks disagree by more than any plausible round trip.
 *
 * It no longer makes anything on screen wrong — that is the whole point of
 * ADR-0008 — so this only decides whether the Configuration banner appears.
 * 90 seconds because that is the cut the proxy list uses to call a proxy
 * online: below it the disagreement could not have flipped a verdict even
 * under the old scheme.
 */
export const SKEW_WARN_MS = 90_000;

export function clockSkewIsSignificant(): boolean {
  return seen && Math.abs(offsetMs) >= SKEW_WARN_MS;
}
