/**
 * The aggregate helpers the faceplate reads — run with:
 *   node --experimental-strip-types src/lags.test.ts
 * (from web/), or `npm test` to run every file at once.
 *
 * Two of these decide what the operator sees and one decides what is WRITTEN,
 * and the third is the one worth a test: `stripLagView` is the last thing
 * between a draft and a body the connector may refuse whole. A `fallback: 0`
 * left in that body is an intent carrying the field, and a connector without
 * the `lag-fallback` gesture refuses the entire aggregate list on sight of it
 * (ADR-0007) — so an operator who never touched the field would be told their
 * unrelated VLAN change was impossible.
 */
import assert from "node:assert/strict";
// Node --experimental-strip-types needs the .ts suffix; tsc excludes this file.
import { lagColor, lagMemberSet, nextLagName, stripLagView, type Lag } from "./lags.ts";

const lag = (name: string, members: string[], extra: Partial<Lag> = {}): Lag => ({
  name,
  mode: "lacp",
  members,
  vlan: {},
  ...extra,
});

// --- lagMemberSet: the emphasis the faceplate paints on a selected aggregate.

const lags = [lag("LAG1", ["GE1", "GE2"]), lag("LAG2", ["GE47", "GE48"])];

assert.deepEqual([...lagMemberSet(lags, "LAG2")].sort(), ["GE47", "GE48"]);
// NO SELECTION IS AN EMPTY SET, never every member: a null name reaching this
// function would otherwise light up the whole facade at once.
assert.equal(lagMemberSet(lags, null).size, 0);
// An aggregate that vanished between two polls (dissolved elsewhere, or a
// read that no longer reports it) emphasises nothing rather than throwing —
// the page resolves the name every render precisely so this can happen.
assert.equal(lagMemberSet(lags, "LAG9").size, 0);

// --- stripLagView: what travels.

const observed = lag("LAG1", ["GE1"], {
  raw: "bonding1",
  running: true,
  vlan: { mode: "access", pvid: 10 },
});
const body = stripLagView(observed);
// The observations do not travel: `running` is never part of an intent, and
// `raw` is the DEVICE's spelling, which the connector re-derives on a read.
assert.equal("running" in body, false);
assert.equal("raw" in body, false);
assert.deepEqual(body.vlan, { mode: "access", pvid: 10 });

// A FALLBACK THAT IS SET TRAVELS…
assert.equal(stripLagView(lag("LAG1", ["GE1"], { fallback: 99 })).fallback, 99);
// …and one that is OFF is ABSENT, not zero. Both say "no fallback" to a
// connector that honours the field; only the absence is safe with one that
// does not, and clearing the field must not turn an ordinary edit into a
// refusal.
assert.equal("fallback" in stripLagView(lag("LAG1", ["GE1"], { fallback: 0 })), false);
assert.equal("fallback" in stripLagView(lag("LAG1", ["GE1"])), false);

// --- The colour is keyed on the NUMBER, not the list position: dissolving
// LAG1 must not repaint LAG2's members.
assert.equal(lagColor("LAG2"), lagColor("LAG2"));
assert.notEqual(lagColor("LAG1"), lagColor("LAG2"));

// --- nextLagName fills the hole rather than appending past the highest.
assert.equal(nextLagName([lag("LAG1", ["GE1"]), lag("LAG3", ["GE3"])]), "LAG2");
assert.equal(nextLagName([]), "LAG1");

console.log("lags.test.ts OK");
