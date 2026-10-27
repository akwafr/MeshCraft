/**
 * Lightweight checks for hostnameConformity — run with:
 *   node --experimental-strip-types src/pages/hostnameConformity.test.ts
 * (from web/), or `npm test` to run every file at once.
 *
 * These assertions are written as the CONFUSIONS they prevent rather than as
 * coverage. The hostname is reconciled by the backend and this panel has no
 * button left, so the only thing it can get wrong is which of five sentences it
 * tells — and two of those mistakes are expensive:
 *
 *   - `unsupported` rendered as `drift` promises a repair that will NEVER
 *     arrive, on a platform meshcraft has no write for at all;
 *   - `not_observed` rendered as `drift` claims an observation nobody made,
 *     and sends someone to check a device that may be perfectly fine.
 */
import assert from "node:assert/strict";
// Node --experimental-strip-types needs the .ts suffix; tsc excludes this file.
import { hostnameSourceKey, hostnameVerdict } from "./hostnameConformity.ts";

// OK — a quiet confirmation. Dim on purpose: a badge here would compete for
// attention with the rows that actually carry a decision. Nothing is queued,
// nothing is asked, and the source still rides along because "where did you
// read that?" is asked about agreeing devices too.
{
  const v = hostnameVerdict("ok")!;
  assert.equal(v.sentenceKey, "hostnameOk");
  assert.equal(v.color, "var(--text-dim)");
  assert.equal(v.repairComing, false);
  assert.equal(v.needsOperator, false);
  assert.equal(v.showSource, true);
  // The controller's sentence does NOT go on the panel for an agreeing
  // device — 2026-10-24. A tolerant match (same name up to letter case and
  // DNS domain) still deserves explaining, or the screen carries a green
  // verdict over two visibly different strings; but it is an EXPLANATION of
  // a verdict nobody has to act on, and it printed three lines under a
  // one-line verdict on every conforming device in the fleet. HostnameRow
  // attaches it as the hover text instead, so it is one hover away rather
  // than permanently on screen.
  assert.equal(v.showBecause, false);
}

// DRIFT — the equipment carries another name AND a repair is already on its
// way. `repairComing` is the flag that keeps the sentence from reading as a
// request for action, and `needsOperator` must stay false: the button that used
// to fix this no longer exists, so asking for a gesture would send the operator
// hunting for a control that is gone.
{
  const v = hostnameVerdict("drift")!;
  assert.equal(v.sentenceKey, "hostnameDrift");
  assert.equal(v.repairComing, true);
  assert.equal(v.needsOperator, false);
  // Both names belong in ONE sentence: the comparison IS the verdict, and a
  // separate "it answers to X" fragment would read as an unrelated fact.
  assert.equal(v.observedFragment, false);
  assert.equal(v.showSource, true);
  // The ONLY verdict that suppresses the controller's sentence, because that
  // sentence says what this one already says — the two names and the coming
  // repair. Printed twice, in a panel this dense, both stop being read.
  assert.equal(v.showBecause, false);
}

// NOT OBSERVED — an UNKNOWN, not a fault. It must not borrow drift's colour or
// its "a repair is coming" promise: hostname.go repairs nothing on the strength
// of an absent reading, so claiming a queued write here would be a lie about
// every unreachable device.
{
  const v = hostnameVerdict("not_observed")!;
  const drift = hostnameVerdict("drift")!;
  assert.equal(v.sentenceKey, "hostnameNotObserved");
  assert.notEqual(v.sentenceKey, drift.sentenceKey);
  assert.notEqual(v.color, drift.color);
  assert.equal(v.repairComing, false);
  assert.equal(v.needsOperator, false);
  // No reading happened: naming a source would invent provenance.
  assert.equal(v.showSource, false);
}

// UNSUPPORTED IS NOT DRIFT — the assertion this module exists for. The platform
// has no HostnameSetter, so the difference can never close; a verdict that
// shared drift's wording or colour would promise a repair that will never come.
{
  const v = hostnameVerdict("unsupported")!;
  const drift = hostnameVerdict("drift")!;
  assert.equal(v.sentenceKey, "hostnameUnsupported");
  assert.notEqual(v.sentenceKey, drift.sentenceKey);
  assert.notEqual(v.color, drift.color);
  assert.equal(v.repairComing, false);
  // And it is not the operator's job either: the fix is a connector that proves
  // the write on hardware, not a click on this page.
  assert.equal(v.needsOperator, false);
  // The name it does answer to is still printed, as its OWN fragment — never
  // folded into a "wants X, has Y" comparison, which is the exact phrasing that
  // turns a permanent limit into a pending repair.
  assert.equal(v.observedFragment, true);
  assert.equal(v.showBecause, true);
}

// UNNAMEABLE — the ONE state a human must act on: `devices.name` cannot become
// a hostname, so nothing was written at all. It is the only verdict with
// `needsOperator`, and the controller's sentence must be shown because it names
// the offending character — the only detail that makes the rename possible.
{
  const v = hostnameVerdict("unnameable")!;
  assert.equal(v.sentenceKey, "hostnameUnnameable");
  assert.equal(v.needsOperator, true);
  assert.equal(v.showBecause, true);
  assert.equal(v.repairComing, false);
  // Nothing was compared, so there is no observed name and no source to name.
  assert.equal(v.observedFragment, false);
  assert.equal(v.showSource, false);
}

// EXACTLY ONE state asks for a human, and exactly one announces a repair. Both
// counted across the five at once: a second `needsOperator` would resurrect the
// gesture this redesign removed, and a second `repairComing` would promise
// convergence somewhere it cannot happen.
{
  const states = ["ok", "drift", "not_observed", "unsupported", "unnameable"];
  const verdicts = states.map((s) => hostnameVerdict(s)!);
  assert.equal(verdicts.filter((v) => v.needsOperator).length, 1);
  assert.equal(verdicts.filter((v) => v.repairComing).length, 1);
  // Five states, five distinct sentences: two states sharing a key would be two
  // situations the operator can no longer tell apart.
  assert.equal(new Set(verdicts.map((v) => v.sentenceKey)).size, 5);
}

// A SIXTH WORD from a future controller answers null — the panel stays silent
// rather than forcing an unknown state into one of the five. Silence is what a
// reviewer notices; a wrong sentence is what an operator believes.
{
  assert.equal(hostnameVerdict("renamed_by_something_new"), null);
  assert.equal(hostnameVerdict(""), null);
  assert.equal(hostnameVerdict(null), null);
  assert.equal(hostnameVerdict(undefined), null);
  // Not case-folded, not trimmed: a state we do not recognise EXACTLY is a
  // state we do not recognise.
  assert.equal(hostnameVerdict("OK"), null);
  assert.equal(hostnameVerdict(" drift"), null);
}

// --- hostnameSourceKey: provenance, or nothing ------------------------------

// The two sources the controller can name are told apart — "on every SNMP poll,
// for free" and "the connector opened a session" are different facts for
// someone judging how fresh the reading is.
{
  assert.equal(hostnameSourceKey("lldp"), "hostnameSourceLldp");
  assert.equal(hostnameSourceKey("read_state"), "hostnameSourceReadState");
}

// An absent or unknown source yields null, and the panel then says nothing
// about provenance. A generic "read from the device" would be a claim about
// where a value came from, which is the one thing this suffix exists to answer.
{
  assert.equal(hostnameSourceKey(undefined), null);
  assert.equal(hostnameSourceKey(null), null);
  assert.equal(hostnameSourceKey(""), null);
  assert.equal(hostnameSourceKey("snmp"), null);
}

console.log("hostnameConformity.test.ts: ok");
