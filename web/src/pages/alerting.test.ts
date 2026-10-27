/**
 * The alerting screen's decisions — ADR-0024. Run with:
 *   node --experimental-strip-types src/pages/alerting.test.ts
 * (from web/), or `npm test` to run every file at once.
 *
 * Written as the question the screen has to answer: a channel that has sent
 * nothing is either healthy and idle or quietly broken, and from the outside
 * the two look identical. ADR-0024 refuses to disambiguate that with a
 * heartbeat nobody reads, so it has to be disambiguated from facts already on
 * hand — which is what `channelHealth` does and what the assertions below
 * fence in.
 */
import assert from "node:assert/strict";
// Node --experimental-strip-types needs the .ts suffix; tsc excludes this file.
import type { NotifyChannel } from "../api.ts";
import {
  ALERT_CLASSES,
  channelHealth,
  channelWouldStaySilent,
  editableHere,
  healthTone,
  missingClasses,
  sortChannels,
} from "./alerting.ts";

function chan(over: Partial<NotifyChannel> = {}): NotifyChannel {
  return {
    id: "c1",
    site_id: "s1",
    kind: "telegram",
    label: "ops",
    enabled: true,
    target: "-1001",
    has_token: true,
    classes: ["device", "interco", "described"],
    min_severity: 2,
    last_ok_at: "2026-10-25T09:00:00Z",
    last_error: "",
    consecutive_failures: 0,
    dropped_uninteresting: 0,
    pending: 0,
    failed: 0,
    created_at: "2026-10-01T00:00:00Z",
    updated_at: "2026-10-01T00:00:00Z",
    ...over,
  };
}

// --- the two silences -----------------------------------------------------

// Worked before, nothing to say now. The only reading of silence that is
// actually good news.
assert.equal(channelHealth(chan()), "idle");

// Never a single success. It may be configured perfectly; nothing has shown
// that it is, and that is a different sentence from "quiet".
assert.equal(channelHealth(chan({ last_ok_at: null })), "untested");
assert.equal(channelHealth(chan({ has_token: false, last_ok_at: null })), "untested");

// A channel can be both failing and idle; "failing" is the fact that needs
// acting on, so it wins.
assert.equal(channelHealth(chan({ consecutive_failures: 3 })), "broken");
// Slips that exhausted their retries count too: the alert EXISTED and was
// never delivered, which is the whole reason the ledger is kept.
assert.equal(channelHealth(chan({ failed: 2 })), "broken");

// Held slips are the anti-flap doing its job, not a fault.
assert.equal(channelHealth(chan({ pending: 4 })), "waiting");

// Disabled by an operator: the silence is intended, and it outranks a stale
// failure counter from before it was switched off.
assert.equal(channelHealth(chan({ enabled: false, consecutive_failures: 9 })), "off");

// --- the palette ----------------------------------------------------------

// "Nobody has tested this yet" is NOT a problem. Painting it orange would
// teach operators to ignore orange, which is UI-GUIDE §2's whole point.
assert.equal(healthTone("untested"), "unknown");
assert.equal(healthTone("broken"), "warn");
assert.equal(healthTone("waiting"), "ok");
assert.equal(healthTone("idle"), "ok");
assert.equal(healthTone("off"), "off");

// --- warning before the incident, not after -------------------------------

// A channel subscribed to nothing is configured and mute, and it will look
// configured forever unless something says so.
assert.equal(channelWouldStaySilent(chan({ classes: [] })), true);

// Nothing in the six alertable types reaches SevError (4): port events top out
// at SevWarning (port.flapping, a flap cleared while still down) and so do
// device events (device.unreachable). A floor above 3 is a mute channel.
assert.equal(channelWouldStaySilent(chan({ min_severity: 4 })), true);
assert.equal(channelWouldStaySilent(chan({ min_severity: 3 })), false);

// A disabled or token-less channel has a DIFFERENT problem, said elsewhere on
// the screen. Reporting both at once would stack two banners saying the same
// thing badly.
assert.equal(channelWouldStaySilent(chan({ enabled: false, classes: [] })), false);
assert.equal(channelWouldStaySilent(chan({ has_token: false, classes: [] })), false);

// --- the gap, named ------------------------------------------------------

// Phrased as what will NOT arrive: "interco port outages will not reach this
// channel" is the sentence an operator can act on; "covers devices" is not.
assert.deepEqual(missingClasses(chan({ classes: ["device"] })), ["interco", "described"]);
assert.deepEqual(missingClasses(chan()), []);

// The three classes, in the order the screen offers them.
assert.deepEqual(ALERT_CLASSES, ["device", "interco", "described"]);

// --- ordering ------------------------------------------------------------

// Broken first, then untested, then the rest by name. A list ordered by
// creation date buries the one failing channel under nine healthy ones.
assert.deepEqual(
  sortChannels([
    chan({ id: "d", label: "zeta" }),
    chan({ id: "a", label: "alpha", consecutive_failures: 1 }),
    chan({ id: "c", label: "beta", last_ok_at: null }),
    chan({ id: "b", label: "aaa" }),
  ]).map((c) => c.id),
  ["a", "c", "b", "d"],
);

// Does not mutate its input: the array comes from react-query's cache, and
// sorting it in place would reorder what every other consumer sees.
const original = [chan({ id: "x", label: "z" }), chan({ id: "y", label: "a" })];
sortChannels(original);
assert.deepEqual(
  original.map((c) => c.id),
  ["x", "y"],
);

// --- tenancy -------------------------------------------------------------

// A site channel of this site is the site admin's to change.
assert.equal(editableHere(chan({ site_id: "s1" }), "s1"), true);

// An instance-wide channel is SHOWN — so nobody creates a duplicate without
// noticing one already covers every site — but not editable: the backend
// refuses it with a 404, and offering the gesture would be a lie.
assert.equal(editableHere(chan({ site_id: undefined }), "s1"), false);

// Another site's channel, reachable only by guessing a UUID, is refused on
// both sides.
assert.equal(editableHere(chan({ site_id: "s2" }), "s1"), false);

console.log("alerting.test.ts: OK");
