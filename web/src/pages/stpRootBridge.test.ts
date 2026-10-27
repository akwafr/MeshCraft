/**
 * Lightweight checks for the root-bridge verdict — run with:
 *   node --experimental-strip-types src/pages/stpRootBridge.test.ts
 * (from web/), or `npm test` to run every file at once.
 *
 * This function decides whether an operator sees a storm warning, so the
 * assertions are written as the two ways of being wrong: warning on a safe
 * topology teaches people to dismiss it, and staying quiet on a contested one
 * lets BPDU filtering land with nobody having agreed to it.
 */
import assert from "node:assert/strict";
// Node --experimental-strip-types needs the .ts suffix; tsc excludes this file.
import { instanceLabel, rootVerdict } from "./stpRootBridge.ts";

// NO ROOTS, and ONE root, are ordinary configuration. Neither may raise
// anything: a warning shown for the safe case is a warning nobody reads on the
// day it matters.
{
  assert.equal(rootVerdict(undefined).kind, "quiet");
  assert.equal(rootVerdict({ roots: [] }).kind, "quiet");
  assert.equal(rootVerdict({ roots: [{ device_id: "a" }] }).kind, "quiet");
}

// SEVERAL ROOTS ON DIFFERENT INSTANCES is normal, correct MST — two trees, two
// roots, nothing filtered. The server reports no contention, so the screen must
// not invent one. This is the case a naive `roots.length > 1` gets wrong.
{
  const v = rootVerdict({
    roots: [
      { device_id: "a", instances: [1] },
      { device_id: "b", instances: [2] },
    ],
  });
  assert.equal(v.kind, "multi-instance");
  if (v.kind === "multi-instance") assert.equal(v.roots, 2);
}

// CONTESTED: the red flag, with the warning passed through VERBATIM and the
// exact count of interfaces that stop taking part in spanning tree.
{
  const v = rootVerdict({
    roots: [{ device_id: "a" }, { device_id: "b" }],
    contested: [0],
    warning: "a SECOND root bridge means two SEPARATE spanning trees…",
    apply_order: "BPDU filtering is written … BEFORE any switch is made root",
    filter_ports: { a: ["Et1"], b: ["Et2", "Et3"] },
  });
  assert.equal(v.kind, "contested");
  if (v.kind === "contested") {
    assert.deepEqual(v.instances, [0]);
    assert.equal(v.filteredPorts, 3);
    assert.equal(v.filteredDevices, 2);
    assert.equal(v.noKnownPath, false);
    // Verbatim: rephrasing the sentence in the frontend is how three slightly
    // different versions of one safety warning end up in circulation.
    assert.ok(v.warning.startsWith("a SECOND root bridge"));
    assert.ok(v.applyOrder.includes("BEFORE"));
  }
}

// CONTESTED WITH NO KNOWN PATH is its own state, not "0 ports".
//
// It means meshcraft can see no path between the roots, so it will filter
// NOTHING and the two switches may still be merged into one tree by a cable it
// cannot see. Collapsed into a zero, that reads as "nothing to do" — the
// opposite of what it means.
{
  const v = rootVerdict({
    roots: [{ device_id: "a" }, { device_id: "b" }],
    contested: [0],
    warning: "…",
  });
  assert.equal(v.kind, "contested");
  if (v.kind === "contested") {
    assert.equal(v.noKnownPath, true);
    assert.equal(v.filteredPorts, 0);
  }
}

// Contention is read from the SERVER and never re-derived: only it holds the
// topology. Here two roots share instance 2 while also holding others, and the
// screen must report exactly what it was told.
{
  const v = rootVerdict({
    roots: [
      { device_id: "a", instances: [1, 2] },
      { device_id: "b", instances: [2, 3] },
    ],
    contested: [2],
    warning: "…",
    filter_ports: { a: ["Et1"], b: ["Et1"] },
  });
  if (v.kind === "contested") assert.deepEqual(v.instances, [2]);
  else assert.fail("shared instance 2 was not reported as contested");
}

// Instance 0 is the CIST and is named, because "0" is the one id whose meaning
// is not obvious from the number — and it is the default, so the most common.
{
  assert.equal(instanceLabel(undefined), "CIST");
  assert.equal(instanceLabel([]), "CIST");
  assert.equal(instanceLabel([0]), "CIST");
  assert.equal(instanceLabel([0, 10]), "CIST, MST 10");
  assert.equal(instanceLabel([3]), "MST 3");
}

console.log("stpRootBridge: OK");
