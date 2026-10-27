/**
 * Lightweight checks for stormControl — run with:
 *   node --experimental-strip-types src/pages/stormControl.test.ts
 * (from web/), or `npm test` to run every file at once.
 *
 * Written as the failures they prevent rather than as coverage. Every case below
 * is a threshold that would have reached a switch meaning something other than
 * what the operator typed, or a control offered on hardware that cannot honour
 * it.
 */
import assert from "node:assert/strict";
// Node --experimental-strip-types needs the .ts suffix; tsc excludes this file.
import {
  buildStormBody,
  classAvailability,
  defaultUnit,
  draftToLimit,
  formatStormRate,
  observedForeign,
  observedLimit,
  portForeign,
  sharedRateConflict,
  type StormCaps,
  type StormDraft,
} from "./stormControl.ts";

const allThree: StormCaps = {
  known: true,
  classes: ["broadcast", "multicast", "unknown_unicast"],
  units: ["percent"],
  write: true,
};

// --- classAvailability: FOUR answers, and the two middle ones are the point ---

// "The hardware cannot" is permanent. A ProCurve 2610 has one broadcast limiter
// and no multicast counterpart; leaving that control enabled would promise
// hardware that does not exist.
{
  const procurve: StormCaps = {
    known: true, classes: ["broadcast"], units: ["percent"], write: true,
  };
  assert.equal(classAvailability(procurve, "broadcast"), "available");
  assert.equal(classAvailability(procurve, "multicast"), "cannot");
  assert.equal(classAvailability(procurve, "unknown_unicast"), "cannot");
}

// "Not verified yet" is temporary and means something else entirely: the switch
// has the limiter, meshcraft has never run the command form on this range. That
// is the state EVERY connector is in today, and a panel that showed it as
// "cannot" would tell the operator to stop waiting for something that is coming.
{
  const notYet: StormCaps = { ...allThree, write: false };
  for (const cls of ["broadcast", "multicast", "unknown_unicast"] as const) {
    assert.equal(classAvailability(notYet, cls), "not_verified", cls);
  }
}

// An unresolved connector answers NOTHING, and must not be rendered as a refusal:
// a device meshcraft could not identify might well support all three.
{
  assert.equal(classAvailability(undefined, "broadcast"), "unknown");
  assert.equal(classAvailability({ known: false }, "broadcast"), "unknown");
  // `classes` ABSENT is not an empty list. A connector that declared no
  // capability has told us nothing, and "this switch cannot" would be a claim
  // about hardware nobody surveyed.
  assert.equal(classAvailability({ known: true, write: true }, "broadcast"), "unknown");
  // An EXPLICIT empty list is a real answer: the Instant On declares it, because
  // its storm control is device-wide with no per-port dimension at all.
  assert.equal(
    classAvailability({ known: true, classes: [], write: false }, "broadcast"),
    "cannot",
  );
}

// --- draftToLimit: the unit is never implicit ---------------------------------

// A unit the platform does not accept is REFUSED, never converted and never
// quietly swapped for one that is. Percent, packets per second and bits per
// second are not derivable from one another without a frame size or a line rate
// that can change under the configuration.
{
  const pctOnly: StormCaps = { known: true, classes: ["broadcast"], units: ["percent"], write: true };
  const draft: StormDraft = { class: "broadcast", enabled: true, rate: "1000", unit: "pps" };
  const { limit, error } = draftToLimit(draft, pctOnly);
  assert.equal(limit, undefined);
  assert.equal(error, "unit:pps");
}

// A REMOVAL carries no rate and no unit, and is never rendered as "rate 0": on
// this fleet a threshold of zero drops every frame of the class, and on broadcast
// that takes ARP with it — the port stops passing anything at all.
{
  const draft: StormDraft = { class: "broadcast", enabled: false, rate: "", unit: "percent" };
  const { limit, error } = draftToLimit(draft, allThree);
  assert.equal(error, undefined);
  assert.deepEqual(limit, { class: "broadcast", enabled: false });
}

// An empty box, a zero and a negative are all "no threshold typed", not zero.
{
  for (const rate of ["", "  ", "0", "-5"]) {
    const { error } = draftToLimit(
      { class: "broadcast", enabled: true, rate, unit: "percent" }, allThree);
    assert.equal(error, "rate:positive", rate);
  }
}

// Above 100 % is a typo that reads as "no limit" and would be written as one.
{
  const { error } = draftToLimit(
    { class: "broadcast", enabled: true, rate: "150", unit: "percent" }, allThree);
  assert.equal(error, "rate:percent");
}

// Fractional PACKETS and fractional BITS are refused rather than rounded —
// rounding here would be the frontend choosing a threshold nobody typed. A
// fractional PERCENTAGE is legitimate: 0.5 % of a ten-gigabit port is a real
// setting.
{
  const ppsCaps: StormCaps = { known: true, classes: ["broadcast"], units: ["pps"], write: true };
  assert.equal(
    draftToLimit({ class: "broadcast", enabled: true, rate: "1500.5", unit: "pps" }, ppsCaps).error,
    "rate:whole",
  );
  const half = draftToLimit(
    { class: "broadcast", enabled: true, rate: "0.5", unit: "percent" }, allThree);
  assert.equal(half.error, undefined);
  assert.equal(half.limit?.rate, 0.5);
}

// --- defaultUnit: no invented fallback ---------------------------------------

// A platform that declares no unit gets NO rate box. Falling back to percent here
// would be the assumption the whole contract exists to remove: invisible, wrong
// on most of the fleet, and undiscoverable by the operator.
{
  assert.equal(defaultUnit(allThree), "percent");
  assert.equal(defaultUnit({ known: true, classes: [] }), undefined);
  assert.equal(defaultUnit(undefined), undefined);
  // The FIRST declared unit wins, so a platform that speaks several starts on the
  // one its connector listed first.
  assert.equal(defaultUnit({ known: true, units: ["pps", "percent"] }), "pps");
}

// --- sharedRateConflict: caught in the form, not by the device ----------------

// RouterOS and SwOS store ONE rate per port. Two enabled classes naming two
// different numbers cannot both be honoured, and the operator must learn that
// while their cursor is still in the box — not from a skipped line after a round
// trip.
{
  const shared: StormCaps = { ...allThree, shared_rate: true };
  const drafts: StormDraft[] = [
    { class: "broadcast", enabled: true, rate: "5", unit: "percent" },
    { class: "unknown_unicast", enabled: true, rate: "20", unit: "percent" },
  ];
  assert.deepEqual(sharedRateConflict(drafts, shared), [5, 20]);
  // The same number twice is not a conflict.
  drafts[1].rate = "5";
  assert.equal(sharedRateConflict(drafts, shared), undefined);
  // A platform with per-class rates never conflicts, whatever is typed.
  drafts[1].rate = "20";
  assert.equal(sharedRateConflict(drafts, allThree), undefined);
}

// A REMOVAL constrains nothing: turning broadcast off while limiting
// unknown-unicast at 20 is a coherent gesture on a shared-rate platform.
{
  const shared: StormCaps = { ...allThree, shared_rate: true };
  const drafts: StormDraft[] = [
    { class: "broadcast", enabled: false, rate: "5", unit: "percent" },
    { class: "unknown_unicast", enabled: true, rate: "20", unit: "percent" },
  ];
  assert.equal(sharedRateConflict(drafts, shared), undefined);
}

// --- buildStormBody -----------------------------------------------------------

// The happy path: only the classes the platform can express travel, each with its
// unit attached.
{
  const drafts: StormDraft[] = [
    { class: "broadcast", enabled: true, rate: "5", unit: "percent" },
    { class: "multicast", enabled: false, rate: "", unit: "percent" },
  ];
  const { body, error } = buildStormBody("GE5", drafts, allThree);
  assert.equal(error, undefined);
  assert.deepEqual(body, {
    ports: {
      GE5: [
        { class: "broadcast", enabled: true, rate: 5, unit: "percent" },
        { class: "multicast", enabled: false },
      ],
    },
  });
}

// A draft for a class the platform CANNOT express is dropped rather than sent: it
// is stale state from a device the operator switched away from, and sending it
// would earn a skipped line explaining nothing about the gesture just made.
{
  const procurve: StormCaps = {
    known: true, classes: ["broadcast"], units: ["percent"], write: true,
  };
  const drafts: StormDraft[] = [
    { class: "multicast", enabled: true, rate: "5", unit: "percent" },
    { class: "broadcast", enabled: true, rate: "5", unit: "percent" },
  ];
  const { body } = buildStormBody("GE1", drafts, procurve);
  assert.deepEqual(body?.ports.GE1, [
    { class: "broadcast", enabled: true, rate: 5, unit: "percent" },
  ]);
}

// Nothing selected is "nothing selected", not a request the API will call
// malformed.
{
  const { body, error } = buildStormBody("GE1", [], allThree);
  assert.equal(body, undefined);
  assert.equal(error, "empty");
}

// The shared-rate conflict wins over everything else: it is the refusal that
// explains the most.
{
  const shared: StormCaps = { ...allThree, shared_rate: true };
  const { error } = buildStormBody("GE1", [
    { class: "broadcast", enabled: true, rate: "5", unit: "percent" },
    { class: "multicast", enabled: true, rate: "20", unit: "percent" },
  ], shared);
  assert.equal(error, "shared:5:20");
}

// --- observedLimit: the three-way answer, kept three-way ----------------------

// Read-and-limited, read-and-unlimited, and NOT READ are three different facts. A
// class absent from a port's list is `undefined`, never "no limiter": the
// connectors deliberately omit a class they could not establish, and collapsing
// that here would show "unlimited" for something nobody observed.
{
  const state = {
    ports: [{
      port: "GE1",
      limits: [
        { class: "broadcast" as const, enabled: true, rate: 5, unit: "percent" as const },
        { class: "multicast" as const, enabled: false },
      ],
    }],
  };
  assert.equal(observedLimit(state, "GE1", "broadcast")?.rate, 5);
  assert.equal(observedLimit(state, "GE1", "multicast")?.enabled, false);
  assert.equal(observedLimit(state, "GE1", "unknown_unicast"), undefined);
  assert.equal(observedLimit(state, "GE2", "broadcast"), undefined);
  assert.equal(observedLimit(undefined, "GE1", "broadcast"), undefined);
}

// Port names are matched EXACTLY, with no normalization: a fuzzy match would
// answer with a neighbouring jack's limiter (GE1 vs GE10).
{
  const state = { ports: [{ port: "GE10", limits: [{ class: "broadcast" as const, enabled: true }] }] };
  assert.equal(observedLimit(state, "GE1", "broadcast"), undefined);
  assert.equal(observedLimit(state, "ge10", "broadcast"), undefined);
  assert.equal(observedLimit(state, "GE10", "broadcast")?.enabled, true);
}

// --- formatStormRate: the unit is never dropped for display -------------------

// A bare number on a panel would reintroduce the ambiguity everything above
// exists to remove, in the one place nobody thinks of as configuration.
{
  assert.equal(formatStormRate(5, "percent"), "5 % of line rate");
  assert.equal(formatStormRate(1000, "pps"), "1000 packets/s");
  assert.equal(formatStormRate(64000, "bps"), "64000 bit/s");
}

// --- the limiters the contract does not model (ADR-0020) ---------------------

// A class whose only observation is a foreign statement reads as NOT READ, and
// the panel must find the device's own line instead of the reassuring "no
// limiter" sentence. Collapsing the two is how a policed port gets shown as open.
{
  const state = {
    ports: [
      {
        port: "ET7",
        foreign: [
          {
            class: "broadcast" as const,
            spelling: "storm-control broadcast level rate 10 mbps",
            because: "the unit vocabulary of that form was never established",
          },
          { spelling: "storm-control all level 5", because: "it polices every frame" },
        ],
      },
    ],
  };
  assert.equal(observedLimit(state, "ET7", "broadcast"), undefined);
  assert.equal(
    observedForeign(state, "ET7", "broadcast")?.spelling,
    "storm-control broadcast level rate 10 mbps",
  );
  // A classless entry matches NO class: "all" answering yes for broadcast
  // would re-make, inside an accessor, the conflation the empty class avoids.
  assert.equal(observedForeign(state, "ET7", "multicast"), undefined);
  // It is shown once for the port instead.
  assert.deepEqual(
    portForeign(state, "ET7").map((f) => f.spelling),
    ["storm-control all level 5"],
  );
  // And a per-class entry never leaks into the port-wide list, or the same
  // sentence would be printed twice in two different places.
  assert.equal(portForeign(state, "ET8").length, 0);
}

console.log("stormControl.test.ts: ok");
