/**
 * Lightweight checks for portConfig — run with:
 *   node --experimental-strip-types src/portConfig.test.ts
 * (from web/), or `npm test` to run every file at once.
 *
 * `portConfigLines` is what a hover over a port square answers, and it is
 * asserted here rather than eyeballed because the interesting half is the
 * ABSENCES. Three of them mean three different things — "this screen has no
 * profile list" (say nothing), "this port follows no profile" (say so), "this
 * port's profile was deleted" (say THAT, not the previous one) — and getting
 * the middle one where the first belongs is how a freshly loaded page comes to
 * claim that every jack on the switch is unassigned.
 */
import assert from "node:assert/strict";
// Node --experimental-strip-types needs the .ts suffix; tsc excludes this file.
import { portConfigLines, profileNameMap, TIP_TAGGED_MAX } from "./portConfig.ts";

// A `t` that returns its key plus the interpolations: the assertions are about
// WHICH sentence is chosen and WHAT is put in it, never about the French or
// English wording, which lives in the locale files.
const t = (key: string, opts?: Record<string, unknown>) =>
  opts ? key + "(" + JSON.stringify(opts) + ")" : key;

const vm = new Map([
  [20, { vlan_id: 20, name: "Voix", color: "#111" }],
  [30, { vlan_id: 30, name: "", color: "#222" }],
]) as never;
const none = new Map<string, string>();
const known = profileNameMap([{ id: "p1", name: "Bureau" }]);

// AN ACCESS PORT NAMES ITS VLAN, and names it by name when the site gave it
// one — the number alone is what the square's colour already encodes.
{
  assert.deepEqual(
    portConfigLines({ mode: "access", pvid: 20 }, vm, undefined, t),
    ['netui.tipVlanAccessNamed({"pvid":20,"name":"Voix"})'],
  );
  // A VLAN carrying an EMPTY name falls back to the bare number rather than
  // rendering "VLAN 30 ()" — an empty parenthesis reads as a bug.
  assert.deepEqual(
    portConfigLines({ mode: "access", pvid: 30 }, vm, undefined, t),
    ['netui.tipVlanAccess({"pvid":30})'],
  );
  // A VLAN the site never defined: the number is still the answer.
  assert.deepEqual(
    portConfigLines({ mode: "access", pvid: 99 }, vm, undefined, t),
    ['netui.tipVlanAccess({"pvid":99})'],
  );
  // Access with NO pvid is a real recorded state, and it is not "VLAN null".
  assert.deepEqual(
    portConfigLines({ mode: "access", pvid: null }, vm, undefined, t),
    ["netui.tipVlanAccessNone"],
  );
}

// A TRUNK SAYS ITS NATIVE VLAN AND WHAT IS TAGGED ON IT, which is the pair the
// user asked the overlay for: "the pvid, and the tagged VLANs if it is a trunk".
{
  assert.deepEqual(
    portConfigLines({ mode: "trunk", pvid: 1, tagged: [30, 10, 20] }, vm, undefined, t),
    [
      'netui.tipVlanTrunkNative({"pvid":1})',
      'netui.tipTagged({"list":"10, 20, 30"})',
    ],
  );
  // SORTED, always: the stored order is whatever the last write happened to
  // produce, and "30, 10, 20" makes a reader check twice for a VLAN that is
  // right there.
  assert.ok(
    portConfigLines({ mode: "trunk", pvid: 1, tagged: [7, 3] }, vm, undefined, t)[1]
      .includes('"3, 7"'),
  );
  // A trunk with no native VLAN names the mode and stops.
  assert.deepEqual(
    portConfigLines({ mode: "trunk", pvid: null }, vm, undefined, t),
    ["netui.tipVlanTrunk"],
  );
  // An EMPTY tagged list adds no line — a trunk with nothing tagged yet has
  // nothing to list, and "Tagged: " would read as a rendering fault.
  assert.deepEqual(
    portConfigLines({ mode: "trunk", pvid: 1, tagged: [] }, vm, undefined, t),
    ['netui.tipVlanTrunkNative({"pvid":1})'],
  );
}

// "ALL" IS ITS OWN SENTENCE and is never expanded. The intent is "every VLAN,
// including ones not created yet"; printing today's numbers would freeze a
// membership that is deliberately open.
{
  assert.deepEqual(
    portConfigLines({ mode: "trunk", pvid: 1, tagged: "all" }, vm, undefined, t),
    ['netui.tipVlanTrunkNative({"pvid":1})', "netui.tipTaggedAll"],
  );
}

// A LONG TRUNK IS TRUNCATED, because a hover must not become a paragraph. The
// head of the list plus a count stays on one line; the port panel carries the
// full membership.
{
  const many = Array.from({ length: TIP_TAGGED_MAX + 5 }, (_, i) => i + 1);
  const lines = portConfigLines({ mode: "trunk", pvid: 1, tagged: many }, vm, undefined, t);
  assert.equal(lines.length, 2);
  assert.ok(lines[1].startsWith("netui.tipTaggedMore"));
  assert.ok(lines[1].includes('"count":5'));
  // Exactly at the cap it is NOT truncated — an off-by-one here would print
  // "+0 more".
  const exact = Array.from({ length: TIP_TAGGED_MAX }, (_, i) => i + 1);
  assert.ok(
    portConfigLines({ mode: "trunk", pvid: 1, tagged: exact }, vm, undefined, t)[1]
      .startsWith("netui.tipTagged("),
  );
}

// A FREE PORT GETS NO VLAN LINE AT ALL. The tip's first line already says
// "free", or "active link not managed by meshcraft" on a jack passing traffic
// anyway; repeating it here would read as a VLAN the port is in.
{
  assert.deepEqual(portConfigLines({ mode: "libre", pvid: 20 }, vm, undefined, t), []);
  assert.deepEqual(portConfigLines({}, vm, undefined, t), []);
  // Even a free port still reports its profile when a vocabulary exists: PoE
  // and STP overrides live on ports with no VLAN intent.
  assert.deepEqual(portConfigLines({ mode: "libre" }, vm, none, t), ["netui.tipProfileNone"]);
}

// THE THREE ABSENCES, which is the whole reason this function is tested.
{
  // No vocabulary: SAY NOTHING about profiles. This is the state of a screen
  // whose profiles query has not landed, and of screens that never fetch one.
  assert.deepEqual(portConfigLines({ profile_id: "p1" }, vm, undefined, t), []);
  // Vocabulary in hand and no profile_id: the port follows none, said plainly.
  // Never "default" — no such profile exists.
  assert.deepEqual(portConfigLines({}, vm, known, t), ["netui.tipProfileNone"]);
  assert.deepEqual(portConfigLines({ profile_id: null }, vm, known, t), [
    "netui.tipProfileNone",
  ]);
  // Resolved.
  assert.deepEqual(portConfigLines({ profile_id: "p1" }, vm, known, t), [
    'netui.tipProfile({"name":"Bureau"})',
  ]);
  // DANGLING — a profile_id whose profile was deleted. Its own sentence, and
  // emphatically not "no profile": the stamped configuration is still on the
  // port, only the provenance is gone, which is exactly what the port panel
  // says about the same state.
  assert.deepEqual(portConfigLines({ profile_id: "gone" }, vm, known, t), [
    "netui.tipProfileDeleted",
  ]);
}

// VLAN FIRST, PROFILE SECOND, on a port that has both — the order the overlay
// renders and the order the operator asked for ("the port profile or vlan").
{
  assert.deepEqual(
    portConfigLines({ mode: "access", pvid: 20, profile_id: "p1" }, vm, known, t),
    [
      'netui.tipVlanAccessNamed({"pvid":20,"name":"Voix"})',
      'netui.tipProfile({"name":"Bureau"})',
    ],
  );
}

// profileNameMap tolerates an absent list, like vlanMap does: a query that has
// not answered must not be a crash.
{
  assert.equal(profileNameMap(undefined).size, 0);
  assert.equal(profileNameMap([]).size, 0);
}

console.log("portConfig.test.ts OK");
