/**
 * Pure decisions of the alerting screen (ADR-0024).
 *
 * Out of the component because they are the part worth testing: whether a
 * channel is about to be useful, and what to say about one that has never
 * spoken. The component renders; this file judges.
 */
import type { AlertClass, NotifyChannel } from "../api";

/** The three classes, in the order the screen offers them. */
export const ALERT_CLASSES: AlertClass[] = ["device", "interco", "described"];

/**
 * What a channel's silence means.
 *
 * THIS IS THE ANSWER TO ADR-0024's THIRD TRAP. A channel that has sent nothing
 * is either healthy and idle or quietly broken, and from the outside the two
 * look identical. Rather than send a heartbeat nobody reads, the screen tells
 * them apart from facts it already has:
 *
 * - `broken`    — consecutive failures, or slips that exhausted their retries.
 *                 Something was meant to be delivered and was not.
 * - `untested`  — no token, or never a single success. It may be perfectly
 *                 configured; nobody has ever shown that it is. This is the
 *                 state a "send test" button exists to leave.
 * - `waiting`   — slips held, nothing wrong. The anti-flap is doing its job.
 * - `idle`      — worked before, nothing to say now. The only reading of
 *                 silence that is actually good news.
 * - `off`       — disabled by an operator. Silence is intended.
 */
export type ChannelHealth = "broken" | "untested" | "waiting" | "idle" | "off";

export function channelHealth(c: NotifyChannel): ChannelHealth {
  if (!c.enabled) return "off";
  // Failures first: a channel can be both failing and idle, and "failing" is
  // the fact that needs acting on.
  if (c.consecutive_failures > 0 || c.failed > 0) return "broken";
  if (!c.has_token || !c.last_ok_at) return "untested";
  if (c.pending > 0) return "waiting";
  return "idle";
}

/**
 * Maps health to the status palette the rest of the product uses (UI-GUIDE §1).
 *
 * `untested` is deliberately NOT a problem colour. Nothing is wrong with a
 * channel nobody has tested yet; it is an invitation, and painting it orange
 * would teach operators to ignore orange.
 */
export function healthTone(h: ChannelHealth): "ok" | "warn" | "off" | "unknown" {
  switch (h) {
    case "broken":
      return "warn";
    case "idle":
    case "waiting":
      return "ok";
    case "off":
      return "off";
    default:
      return "unknown";
  }
}

/**
 * Whether this channel can deliver anything at all right now.
 *
 * Used to warn BEFORE an incident rather than after. A channel subscribed to no
 * class, or whose severity floor sits above every severity its classes can
 * produce, is configured and mute — and it will look configured on this screen
 * forever unless something says so.
 */
export function channelWouldStaySilent(c: NotifyChannel): boolean {
  if (!c.enabled || !c.has_token) return false; // a different problem, said elsewhere
  if (c.classes.length === 0) return true;
  // The highest severity each class can carry. Port events top out at
  // SevWarning (3) — `port.flapping` and a flap that cleared while down —
  // and device events likewise (`device.unreachable`). Nothing in the
  // alertable vocabulary reaches SevError (4), so a floor above 3 is a
  // channel that will never speak.
  return c.min_severity > 3;
}

/**
 * The classes a channel does NOT cover, so the screen can name the gap.
 *
 * Phrased as a gap rather than a list of what is covered, because the useful
 * sentence for an operator is "interco port outages will not reach this
 * channel", not "this channel covers devices".
 */
export function missingClasses(c: NotifyChannel): AlertClass[] {
  return ALERT_CLASSES.filter((k) => !c.classes.includes(k));
}

/**
 * Sorts channels so what needs attention is on top.
 *
 * Broken first, then untested, then the rest alphabetically. A list ordered by
 * creation date buries the one failing channel under nine healthy ones.
 */
export function sortChannels(list: NotifyChannel[]): NotifyChannel[] {
  const rank: Record<ChannelHealth, number> = {
    broken: 0,
    untested: 1,
    waiting: 2,
    idle: 3,
    off: 4,
  };
  return [...list].sort((a, b) => {
    const d = rank[channelHealth(a)] - rank[channelHealth(b)];
    if (d !== 0) return d;
    return (a.label || a.kind).localeCompare(b.label || b.kind);
  });
}

/**
 * Whether the operator may edit this channel from a site screen.
 *
 * An instance-wide channel is shown here — so nobody creates a duplicate
 * without noticing one already covers every site — but it is not a site
 * admin's to change. The backend refuses it with a 404; the screen must not
 * offer the gesture in the first place.
 */
export function editableHere(c: NotifyChannel, siteId: string): boolean {
  return !!c.site_id && c.site_id === siteId;
}
