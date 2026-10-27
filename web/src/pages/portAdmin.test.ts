/**
 * Lightweight checks for portAdmin — run with:
 *   node --experimental-strip-types src/pages/portAdmin.test.ts
 * (from web/), or `npm test` to run every file at once.
 *
 * These two functions decide what a DESTRUCTIVE button says and whether it is
 * armed, so the assertions below are written as the failures they prevent
 * rather than as coverage: a wrong `enable` shuts a port with a request that
 * looks like it enabled one, and a wrong `needsForce` drops the link carrying
 * the administration session with no confirmation in the way.
 */
import assert from "node:assert/strict";
// Node --experimental-strip-types needs the .ts suffix; tsc excludes this file.
import { adminGesture, observedTrust, trustObsInk, trustToken } from "./portAdmin.ts";

// A LIVE PORT: the gesture shuts, the contract bool is false, and the overlay
// must demand an explicit force — this is the single case with no undo from the
// screen on the ranges that have no serial port.
{
  const g = adminGesture("up");
  assert.deepEqual(g, { shut: true, enable: false, needsForce: true });
}

// ADMINISTRATIVELY SHUT is the ONLY state that reverses the gesture. `enable`
// flips to true with it: the polarity is carried by this function alone, and a
// second negation at the call site is how a shut becomes a no-shut.
{
  const g = adminGesture("disabled");
  assert.deepEqual(g, { shut: false, enable: true, needsForce: false });
}

// ERR-DISABLED: the switch shut it itself, but the CONFIGURATION still reads
// "no shutdown" — so the gesture on offer is still a shut, and the recovery
// belongs to whatever tripped it. Offering "bring back up" here would be a
// button that writes a value already in place and changes nothing on the wire.
{
  const g = adminGesture("err-disabled");
  assert.equal(g.shut, true);
  assert.equal(g.enable, false);
  // No live link to protect: the force checkbox would be noise.
  assert.equal(g.needsForce, false);
}

// Enabled with nothing plugged in, and an empty cage: both are ports the
// operator can still shut, and neither carries traffic to warn about.
{
  for (const state of ["down", "unplugged"]) {
    const g = adminGesture(state);
    assert.deepEqual(g, { shut: true, enable: false, needsForce: false }, state);
  }
}

// NEVER READ (null / undefined / empty) degrades to "not administratively
// shut": the honest answer for a device meshcraft has not re-read is that its
// configuration says nothing about a shutdown, and a "bring back up" offered
// there would promise to lift something nobody observed.
{
  for (const state of [null, undefined, ""]) {
    const g = adminGesture(state);
    assert.deepEqual(g, { shut: true, enable: false, needsForce: false },
      String(state));
  }
}

// A SIXTH WORD from a future connector must degrade, not crash a faceplate —
// and must not be mistaken for "disabled".
{
  const g = adminGesture("suspended-by-something-new");
  assert.deepEqual(g, { shut: true, enable: false, needsForce: false });
}

// --- observedTrust: the three-way answer, kept three-way ------------------

// Read and TRUSTED, read and UNTRUSTED: both are facts, told apart.
{
  const state = {
    ports: [
      { port: "GE1", trusted: true },
      { port: "GE2", trusted: false },
    ],
  };
  assert.equal(observedTrust(state, "GE1"), true);
  assert.equal(observedTrust(state, "GE2"), false);
}

// A port ABSENT from the read is `undefined`, NOT false. This is the whole
// point: "untrusted" means protected, "not read" means we do not know, and a
// panel that collapsed the second into the first would claim a protection
// nobody observed.
{
  const state = { ports: [{ port: "GE1", trusted: true }] };
  assert.equal(observedTrust(state, "GE2"), undefined);
}

// No observation at all — device never re-read, or a connector that does not
// read snooping — answers `undefined` for every port, never false.
{
  assert.equal(observedTrust(undefined, "GE1"), undefined);
  assert.equal(observedTrust({}, "GE1"), undefined);
  assert.equal(observedTrust({ ports: [] }, "GE1"), undefined);
}

// Port names are matched EXACTLY, with no normalization: the standard-model
// name is what both sides of the wire carry, and a fuzzy match here would
// answer "trusted" for a neighbouring jack (GE1 vs GE10).
{
  const state = { ports: [{ port: "GE10", trusted: true }] };
  assert.equal(observedTrust(state, "GE1"), undefined);
  assert.equal(observedTrust(state, "ge10"), undefined);
  assert.equal(observedTrust(state, "GE10"), true);
}

// --- trustToken / trustObsInk: ONE ink rule for the two panels ------------
//
// What is pinned here is a product decision (2026-08-08, UI-GUIDE §6), not the
// bodies of two three-line functions. SwitchDetail showed the same field with
// two contradictory colour rules on one page — single port: unread dim,
// trusted `--warn`, untrusted `--ok`; multi port: mixed `--warn` and every
// real value uncoloured — so extending a selection from one port to two
// inverted the vocabulary under the operator. The assertions below are written
// as the sentences the ink must not say.

// NEVER LOOKED IS GREY, in both panels. `--text-dim`, never `--indeterminate`:
// the device did not report this port, which is "we have not looked", not "we
// have lost the vantage point" (UI-GUIDE §2's boundary table).
{
  assert.equal(trustToken(undefined), "unread");
  assert.equal(trustObsInk("unread"), "var(--text-dim)");
}

// NO SINGLE VALUE IS THE SAME GREY. A selection whose ports disagree is a
// question with no answer, not an alert about the device — the same ruling
// that took `--warn` off "mixed" everywhere else on this panel.
{
  assert.equal(trustObsInk("mixed"), "var(--text-dim)");
  assert.equal(trustObsInk("mixed"), trustObsInk("unread"));
}

// TRUSTED IS NOT AN ALERT, and this is the judgement call the whole change
// turns on. It disables snooping's protection on that jack, but every switch
// has at least one trusted port — the uplink toward the DHCP server — so
// `--warn` painted a correct facade orange. Explicitly NOT `--warn`, and not
// `--ok` either: it is neither wrong nor confirmed-good, it is what the
// operator asked for, and the word "trusted" carries it.
{
  assert.equal(trustToken(true), "trusted");
  assert.equal(trustObsInk("trusted"), undefined);
}

// UNTRUSTED IS CONFIRMED GOOD, OBSERVED: the protection is in place on this
// jack and the device said so. This is the one branch the single-port panel
// already had right, and it survives unchanged.
{
  assert.equal(trustToken(false), "untrusted");
  assert.equal(trustObsInk("untrusted"), "var(--ok)");
}

// THE FOUR INKS COLLAPSE TO THREE, and the pairing is the point: the two
// states that are not observations of this port share one ink, and the two
// that are do not.
{
  const inks = (["unread", "mixed", "trusted", "untrusted"] as const).map(trustObsInk);
  assert.deepEqual(inks, [
    "var(--text-dim)", "var(--text-dim)", undefined, "var(--ok)",
  ]);
  // Nothing on this field is orange. The device-wide "snooping is off" note
  // above it is, and it must stay the only orange in the control: a per-port
  // ink repeating it would say "untrusted is fine" and "nothing is inspected"
  // three words apart.
  assert.equal(inks.includes("var(--warn)"), false);
}

// The whole round trip, as the single-port panel makes it: observedTrust →
// trustToken → ink, with no `undefined` check re-invented in between.
{
  const state = { ports: [{ port: "GE1", trusted: true }, { port: "GE2", trusted: false }] };
  assert.equal(trustObsInk(trustToken(observedTrust(state, "GE1"))), undefined);
  assert.equal(trustObsInk(trustToken(observedTrust(state, "GE2"))), "var(--ok)");
  assert.equal(trustObsInk(trustToken(observedTrust(state, "GE3"))), "var(--text-dim)");
}

console.log("portAdmin.test.ts: ok");
