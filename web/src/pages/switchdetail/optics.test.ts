/**
 * Checks for optics.ts — run with:
 *   node --experimental-strip-types src/pages/switchdetail/optics.test.ts
 * (from web/), or `npm test` to run every file at once.
 *
 * THE WHOLE SUITE IS ABOUT ONE MISTAKE: rendering an absent reading as a
 * number. A port with no transceiver, a transceiver that cannot measure, a
 * cage the connector failed to read and a genuine measurement of 0.00 dBm are
 * four different facts, and every `?? 0` in a panel collapses at least two of
 * them into "0.00 dBm" — a value the device never reported, on a screen an
 * operator uses to decide whether to go and clean a fibre.
 *
 * The fixtures are the shapes the FS S3400 actually produces
 * (`internal/driver/fs/s3400/optics.go`, captured 2026-08-05), including the
 * exact spelling of the partial-failure note, which this module parses.
 */
import assert from "node:assert/strict";
// Node --experimental-strip-types needs the .ts suffix; tsc excludes this file.
import type { OpticsPortState, OpticsState } from "../../api.ts";
import {
  opticsCage,
  opticsCageInk,
  opticsHasReading,
  opticsMeta,
  opticsReadFailure,
  opticsReading,
  opticsReadingInk,
} from "./optics.ts";

const t = (key: string, opts?: Record<string, unknown>) =>
  opts ? key + "(" + JSON.stringify(opts) + ")" : key;

// A module as the lab 48T4SP reports it in XG1.
const full: OpticsPortState = {
  port: "XG1",
  vendor: "FS",
  part_number: "SFP-10G-BX",
  serial: "G2210006755",
  ddm: true,
  tx_power_dbm: -2.08,
  rx_power_dbm: -1.85,
  temperature_c: 46,
  voltage_v: 3.26,
  bias_current_ma: 35.61,
};

/* --- The four cage states are four different answers ------------------- */

// NO OBSERVATION AT ALL. Absent `optics_state` is "never read, or this
// connector cannot read optics" — the wire does not separate them, so neither
// does this. It is emphatically NOT "the cage is empty".
{
  assert.deepEqual(opticsCage(undefined, "XG1"), { kind: "unread" });
  assert.notEqual(opticsCage(undefined, "XG1").kind, "empty");
}

// AN OBSERVATION EXISTS AND THIS PORT IS NOT IN IT → empty cage. `ports` lists
// only cages a module was seen in, so absence from the list is a finding.
{
  const st: OpticsState = { ports: [full] };
  assert.deepEqual(opticsCage(st, "XG2"), { kind: "empty" });
  assert.equal(opticsCage(st, "XG1").kind, "module");
}

// A STATE WITH NEITHER PORTS NOR NOTES still means "read, nothing found" —
// the API turns a genuinely empty observation into `null` before it ships
// (opticsObservedOf), so anything that arrives here was worth shipping.
{
  assert.deepEqual(opticsCage({}, "XG1"), { kind: "empty" });
}

// A CAGE WHOSE READ FAILED must never read as empty. This is the note
// `ReadOptics` builds for a partial failure, spelled exactly as Go spells it.
{
  const st: OpticsState = {
    ports: [full],
    notes: ["not read: XG3: dial tcp 192.0.2.9:22: i/o timeout ; XG4: EOF"],
  };
  assert.deepEqual(opticsCage(st, "XG3"), {
    kind: "blind",
    reason: "dial tcp 192.0.2.9:22: i/o timeout",
  });
  assert.deepEqual(opticsCage(st, "XG4"), { kind: "blind", reason: "EOF" });
  // ...while a cage that is not named in the note is still plainly empty.
  assert.deepEqual(opticsCage(st, "XG2"), { kind: "empty" });
}

// THE PORT NAME IS MATCHED AS A WHOLE, colon included: `XG1` must not claim
// the failure of `XG10`. Off-by-one here would paint a working cage yellow
// and a broken one empty, in the same panel.
{
  const notes = ["not read: XG10: EOF"];
  assert.equal(opticsReadFailure(notes, "XG1"), null);
  assert.equal(opticsReadFailure(notes, "XG10"), "EOF");
}

// A failure the connector gave no words for is STILL a failure: "" and null
// are different answers, and only null means "this cage was read".
{
  assert.equal(opticsReadFailure(["not read: XG2: "], "XG2"), "");
  assert.deepEqual(opticsCage({ notes: ["not read: XG2: "] }, "XG2"), {
    kind: "blind",
    reason: "",
  });
}

// Notes that are not failure reports are left alone — the connector also
// writes "no optical cage on this model (…): nothing to read", and reading a
// port name out of a sentence like that is exactly what the prefix guard
// prevents.
{
  const notes = ["no optical cage on this model (S3400-24T4FP): nothing to read"];
  assert.equal(opticsReadFailure(notes, "XG1"), null);
  assert.deepEqual(opticsCage({ notes }, "XG1"), { kind: "empty" });
}

/* --- Readings: absence is never a number ------------------------------- */

// A GENUINE ZERO IS A READING. 0.0 dBm is a legal transmit power, which is the
// whole reason the Go side uses pointers; if this ever returns null for 0 the
// panel starts hiding real measurements.
{
  assert.equal(opticsReading(0, "dBm", "."), "0 dBm");
  assert.equal(opticsReading(0, "dBm", ","), "0 dBm");
  assert.notEqual(opticsReading(0, "dBm", "."), null);
}

// AN ABSENT READING IS NULL, and not "0", not "—", not "0.00". The caller is
// forced to choose a sentence for it.
{
  assert.equal(opticsReading(undefined, "dBm", "."), null);
  assert.equal(opticsReading(null, "dBm", "."), null);
}

// The value is printed as the device reported it, with the locale's separator
// and nothing else added — no rounding, no padding to two decimals.
{
  assert.equal(opticsReading(-1.85, "dBm", ","), "-1,85 dBm");
  assert.equal(opticsReading(-1.85, "dBm", "."), "-1.85 dBm");
  assert.equal(opticsReading(3.26, "V", ","), "3,26 V");
  assert.equal(opticsReading(46, "°C", ","), "46 °C");
}

/* --- Ink: the ladder, and the two states that must not merge ----------- */

// "We have never looked" is grey and "we cannot see" is yellow — UI-GUIDE §2's
// most expensive pair to merge. An unread device and an unreadable cage are on
// opposite sides of it.
{
  assert.equal(opticsCageInk("unread"), "var(--text-dim)");
  assert.equal(opticsCageInk("blind"), "var(--indeterminate)");
  assert.notEqual(opticsCageInk("unread"), opticsCageInk("blind"));
}

// An empty cage and a present module are both plain facts: no ink at all.
// Neither is a problem, neither is an absence of observation.
{
  assert.equal(opticsCageInk("empty"), undefined);
  assert.equal(opticsCageInk("module"), undefined);
}

// NO STATE IS AN ALERT. The device is answering, and nothing on this panel is
// compared against a threshold — the DDM bounds are not parsed at all, so a
// verdict colour would be a limit we do not have.
{
  const inks = (["unread", "empty", "blind", "module"] as const).map(opticsCageInk);
  assert.equal(inks.includes("var(--warn)"), false);
  assert.equal(inks.includes("var(--danger)"), false);
  assert.equal(inks.includes("var(--ok)"), false);
}

// A MISSING READING FROM A MODULE THAT PROMISED ONE is a reading that stopped
// arriving — §2 names that case as `--indeterminate` outright.
{
  assert.equal(opticsReadingInk(undefined, true), "var(--indeterminate)");
}

// A MISSING READING FROM A "DDM:NO" MODULE is not: it is a property of the
// transceiver, permanent while that module sits in that cage, and calls for no
// action. Yellow there would claim a vantage point was lost that never existed.
{
  assert.equal(opticsReadingInk(undefined, false), "var(--text-dim)");
  assert.notEqual(opticsReadingInk(undefined, false), opticsReadingInk(undefined, true));
}

// A reading that IS there gets no ink — including a zero, which must not be
// styled as an absence.
{
  assert.equal(opticsReadingInk(-1.85, true), undefined);
  assert.equal(opticsReadingInk(0, true), undefined);
  assert.equal(opticsReadingInk(0, false), undefined);
}

/* --- hasReading: the flag is a declaration, the values are the truth --- */
{
  assert.equal(opticsHasReading(full), true);
  assert.equal(opticsHasReading({ port: "XG2", ddm: false }), false);
  // A zero counts. This is the same trap as above, one level up.
  assert.equal(opticsHasReading({ port: "XG2", ddm: true, tx_power_dbm: 0 }), true);
}

/* --- The folded summary says which of the four states it is ------------ */

// Receive power when there is one: the number the panel is opened for.
{
  assert.equal(
    opticsMeta({ kind: "module", module: full }, t, ","),
    'switchDetailPage.opticsMetaRx({"rx":"-1,85 dBm"})',
  );
}

// A zero receive power is still the summary — losing it to a falsy test would
// hide the one reading that says the fibre went dark.
{
  const dark: OpticsPortState = { port: "XG1", ddm: true, rx_power_dbm: 0 };
  assert.equal(
    opticsMeta({ kind: "module", module: dark }, t, ","),
    'switchDetailPage.opticsMetaRx({"rx":"0 dBm"})',
  );
}

// A module that cannot measure says so, rather than showing a blank where a
// number goes.
{
  const noDdm: OpticsPortState = { port: "XG1", ddm: false, part_number: "SFP-1G-T" };
  assert.equal(
    opticsMeta({ kind: "module", module: noDdm }, t, ","),
    "switchDetailPage.opticsMetaNoDdm",
  );
}

// A DDM module that served nothing at all falls back to naming itself: the
// per-reading yellow inside the section carries the "not served" part, and
// repeating it in the summary would say it twice.
{
  const silent: OpticsPortState = { port: "XG1", ddm: true, part_number: "SFP-10G-BX" };
  assert.equal(opticsMeta({ kind: "module", module: silent }, t, ","), "SFP-10G-BX");
  const anon: OpticsPortState = { port: "XG1", ddm: true };
  assert.equal(
    opticsMeta({ kind: "module", module: anon }, t, ","),
    "switchDetailPage.opticsMetaModule",
  );
}

// THE FOUR STATES GET FOUR DIFFERENT SENTENCES. A collapsed section that said
// the same thing about an empty cage and a cage nobody could read would be the
// whole defect, restated in one line of text.
{
  const metas = [
    opticsMeta({ kind: "unread" }, t, ","),
    opticsMeta({ kind: "empty" }, t, ","),
    opticsMeta({ kind: "blind", reason: "EOF" }, t, ","),
    opticsMeta({ kind: "module", module: full }, t, ","),
  ];
  assert.equal(new Set(metas).size, 4);
}

console.log("optics.test.ts OK");
