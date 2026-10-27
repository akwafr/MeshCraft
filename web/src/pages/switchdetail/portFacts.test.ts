/**
 * Lightweight checks for portFacts — run with:
 *   node --experimental-strip-types src/pages/switchdetail/portFacts.test.ts
 * (from web/), or `npm test` to run every file at once.
 *
 * These five turn a raw observation into the sentence an operator reads, and
 * three of them share one rule worth asserting on its own: A RAW TOKEN SHOWN
 * BEATS A HIDDEN STATE. A connector that one day reports a sixth link state or
 * a PoE class this list does not know must show it verbatim, not degrade to
 * "down" or "no power" — that silent degrading is exactly the confusion
 * (err-disabled read as a plain down port) these rows exist to remove.
 */
import assert from "node:assert/strict";
// Node --experimental-strip-types needs the .ts suffix; tsc excludes this file.
import {
  common,
  linkStateLabel,
  poeClassLabel,
  rangeLabel,
  vlanMeta,
} from "./portFacts.ts";

// A `t` that returns its key, plus the interpolations: the assertions are about
// WHICH sentence is chosen and WHAT is put in it, never about the French or
// English wording, which lives in the locale files.
const t = (key: string, opts?: Record<string, unknown>) =>
  opts ? key + "(" + JSON.stringify(opts) + ")" : key;

// THE FOUR KNOWN LINK STATES map to their own sentence...
{
  assert.equal(linkStateLabel("down", t), "switchDetailPage.linkStateDown");
  assert.equal(linkStateLabel("disabled", t), "switchDetailPage.linkStateDisabled");
  assert.equal(linkStateLabel("unplugged", t), "switchDetailPage.linkStateUnplugged");
  assert.equal(linkStateLabel("err-disabled", t), "switchDetailPage.linkStateErrDisabled");
}

// ...and ANYTHING ELSE is shown as the platform spelled it. `err-disabled` and a
// plain `down` are different problems and must never collapse into each other,
// so an unrecognised sixth spelling is passed through rather than guessed at.
{
  assert.equal(linkStateLabel("up", t), "up");
  assert.equal(linkStateLabel("loop-protect-shutdown", t), "loop-protect-shutdown");
  assert.equal(linkStateLabel("", t), "");
}

// THE FOLDED VLAN SUMMARY says the mode AND the number that goes with it. On an
// access port that is the PVID; a missing one is an em dash and not a silent
// "VLAN 1", which would be a claim about the port we cannot make.
{
  assert.equal(vlanMeta("access", 30, t), 'switchDetailPage.vlanMetaAccess({"pvid":30})');
  assert.equal(vlanMeta("access", null, t), 'switchDetailPage.vlanMetaAccess({"pvid":"—"})');
}

// ON A TRUNK the number is the NATIVE vlan, and a trunk with none says just
// "trunk" rather than inventing an untagged lane for it.
{
  assert.equal(vlanMeta("trunk", 1, t), 'switchDetailPage.vlanMetaTrunkNative({"pvid":1})');
  assert.equal(vlanMeta("trunk", null, t), "switchDetailPage.modeTrunkLabel");
  assert.equal(vlanMeta("trunk", undefined, t), "switchDetailPage.modeTrunkLabel");
}

// A port in NO mode is free, whatever number happens to be recorded on it.
{
  assert.equal(vlanMeta(undefined, 12, t), "switchDetailPage.modeFreeLabel");
  assert.equal(vlanMeta("libre", 12, t), "switchDetailPage.modeFreeLabel");
}

// THE OBSERVED PoE CLASS wins when there is one, including the observation that
// says the jack delivers nothing.
{
  assert.equal(poeClassLabel("none", true, t), "switchDetailPage.poeClassNone");
  assert.equal(poeClassLabel("24v", false, t), "switchDetailPage.poeClass24v");
  assert.equal(poeClassLabel("54v", false, t), "switchDetailPage.poeClass54v");
  assert.equal(poeClassLabel("af", false, t), "af");
}

// WITH NO OBSERVATION the stored intent answers instead — a device never re-read
// still has something honest to say — and "we intend power here" is reported as
// an unknown class, never as a specific voltage nobody measured.
{
  assert.equal(poeClassLabel(undefined, true, t), "switchDetailPage.poeClassUnknownOn");
  assert.equal(poeClassLabel(undefined, false, t), "switchDetailPage.poeClassNone");
  assert.equal(poeClassLabel(undefined, undefined, t), "switchDetailPage.poeClassNone");
}

// `rangeLabel` compacts a selection so it can be VERIFIED at a glance. All the
// cases below are about the same failure: a panel that prints "1, 2, 3, 4, 5, 6,
// 7, 8" makes you count again to check you selected what you meant to.
const all = ["GE1", "GE2", "GE3", "GE4", "GE5", "GE6", "GE7", "GE8"].map((name) => ({ name })) as never[];
const pick = (...i: number[]) => i.map((n) => all[n]);

// A RUN OF THREE OR MORE becomes a dash range; isolated ports stay listed.
{
  assert.equal(rangeLabel(pick(0, 1, 2, 3), all), "GE1–GE4");
  assert.equal(rangeLabel(pick(0, 2, 4), all), "GE1, GE3, GE5");
  assert.equal(rangeLabel(pick(0, 1, 2, 5, 6, 7), all), "GE1–GE3, GE6–GE8");
}

// A PAIR is written out with a comma, not as a two-element range: "GE1–GE2" is
// longer to read than "GE1, GE2" and says nothing more.
{
  assert.equal(rangeLabel(pick(0, 1), all), "GE1, GE2");
  assert.equal(rangeLabel(pick(3), all), "GE4");
}

// ORDER IS PHYSICAL, not click order. The selection is built by clicking, and an
// operator who picked 5 then 3 then 4 selected a range whether they know it or
// not — the label must say so.
{
  assert.equal(rangeLabel(pick(4, 2, 3), all), "GE3–GE5");
}

// A PORT NOT IN `all` is DISCARDED rather than crashing the render: an
// incomplete label is a display glitch, an exception here blanks the page. With
// nothing left to place, the names are listed raw.
{
  const ghost = { name: "GE99" } as never;
  assert.equal(rangeLabel([ghost, all[0], all[1]], all), "GE1, GE2");
  assert.equal(rangeLabel([ghost], all), "GE99");
  assert.equal(rangeLabel([], all), "");
}

// `common` answers the value only when EVERY port agrees. `undefined` is the
// third answer the multi-port panel renders as "mixed" — reducing a
// disagreement to the first port's value would make the screen lie about what
// an apply is going to overwrite.
{
  assert.equal(common(pick(0, 1, 2), (p: { name: string }) => p.name.slice(0, 2)), "GE");
  assert.equal(common(pick(0, 1, 2), (p: { name: string }) => p.name), undefined);
  assert.equal(common(pick(3), (p: { name: string }) => p.name), "GE4");
}

// It compares by ===, so a value that agrees must be a PRIMITIVE. Two equal
// objects read as a disagreement — which is why the panel folds an observation
// to a token before `common` ever sees it.
{
  assert.equal(common(pick(0, 1), () => ({ v: 1 })), undefined);
  assert.equal(common(pick(0, 1), () => null), null);
  assert.equal(common(pick(0, 1), () => undefined), undefined);
}

console.log("portFacts: OK");
