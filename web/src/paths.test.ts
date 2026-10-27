/**
 * URL ↔ device contract of the detail page:
 *   node --experimental-strip-types src/paths.test.ts
 *
 * The device detail URL is keyed on the device ID with the name as a
 * readable slug ("{name}--{uuid}"). The name-only form orphaned the page on
 * every rename the page did not perform itself — observed live on the
 * device named by its bare IP ("192.0.2.4" → "unifimoule1"): the PATCH
 * succeeded, the screen vanished. Our own rename still moves the slug
 * (deviceDetailPathAfterRename: optimistic move, rollback, server-normalized
 * spelling); a CONCURRENT rename by another operator is survived by the ID
 * (findDeviceBySegment). Old name-only bookmarks must keep resolving.
 */
import assert from "node:assert/strict";
import {
  decodeDevicePathSegment,
  deviceDetailPath,
  deviceDetailPathAfterRename,
  devicePathSegment,
  findDeviceBySegment,
  parseDeviceSegment,
  sitePath,
} from "./paths.ts";

const ID = "0b7c5c5e-9a1f-4b6e-8f2d-3c4d5e6f7a8b";
const OTHER = "11111111-2222-4333-8444-555555555555";

// --- deviceDetailPath: the canonical form -------------------------------

assert.equal(
  deviceDetailPath("lab", { id: ID, name: "unifimoule1" }),
  `/lab/equipment/unifimoule1--${ID}`,
);
// Names with characters that matter in a URL are encoded exactly like every
// other link in the app (sitePath encodes each segment).
assert.equal(
  deviceDetailPath("lab", { id: ID, name: "core sw/1" }),
  sitePath("lab", "equipment", `core sw/1--${ID}`),
);

// --- parseDeviceSegment: both URL generations ---------------------------

// New form: ID split off the tail, name intact — including a name that
// itself contains "--".
assert.deepEqual(parseDeviceSegment(`unifimoule1--${ID}`), {
  id: ID,
  name: "unifimoule1",
});
assert.deepEqual(parseDeviceSegment(`a--b--${ID}`), { id: ID, name: "a--b" });
// Old name-only bookmark: no ID, whole segment is the name.
assert.deepEqual(parseDeviceSegment("192.0.2.4"), {
  id: null,
  name: "192.0.2.4",
});
// Encoded segment (what useParams actually hands over).
assert.deepEqual(parseDeviceSegment(encodeURIComponent(`core sw/1--${ID}`)), {
  id: ID,
  name: "core sw/1",
});

// --- findDeviceBySegment: resolution order ------------------------------

const devices = [
  { id: ID, name: "unifimoule1" },
  { id: OTHER, name: "poefs3400lab" },
];

// ID wins even when the slug is STALE — the concurrent-rename case: another
// operator renamed the device while this page was open, the list moved, the
// URL did not.
assert.equal(
  findDeviceBySegment(devices, `old-stale-name--${ID}`)?.name,
  "unifimoule1",
);
// Old name-only URL still resolves.
assert.equal(
  findDeviceBySegment(devices, encodeURIComponent("poefs3400lab"))?.id,
  OTHER,
);
// A deleted device resolves to nothing — the "not found" sheet is honest.
assert.equal(
  findDeviceBySegment(devices, `gone--${"9".repeat(8)}-9999-4999-8999-${"9".repeat(12)}`),
  undefined,
);
// Pathological old bookmark: a device NAME that itself ends in "--{uuid}".
// The split finds no such ID, the full-segment fallback still matches.
const weird = [{ id: OTHER, name: `weird--${ID}` }];
assert.equal(findDeviceBySegment(weird, encodeURIComponent(`weird--${ID}`))?.id, OTHER);
// Bare-UUID segment (legacy /s/{site}/sw/{uuid} rewrites).
assert.equal(findDeviceBySegment(devices, ID)?.name, "unifimoule1");

// --- deviceDetailPathAfterRename: the slug follows our own rename --------

// The bug as reported: device named by its bare IP gets a real name — the
// page must follow the rename, not evaporate. The move also carries the ID.
assert.equal(
  deviceDetailPathAfterRename("lab", `192.0.2.4--${ID}`, ID, "unifimoule1"),
  `/lab/equipment/unifimoule1--${ID}`,
);

// Saving without touching the name (location, mgmt_ip, position…): no move.
assert.equal(
  deviceDetailPathAfterRename("lab", `unifimoule1--${ID}`, ID, undefined),
  null,
);

// Renaming to the name the URL already carries: no move either — a
// gratuitous navigate would abort in-flight queries for nothing.
assert.equal(
  deviceDetailPathAfterRename("lab", encodeURIComponent(`unifimoule1--${ID}`), ID, "unifimoule1"),
  null,
);

// Empty new name never navigates (the input refuses it upstream, but the
// helper must not build a /equipment/ path pointing at the list).
assert.equal(deviceDetailPathAfterRename("lab", `x--${ID}`, ID, ""), null);

// A name-only URL is upgraded to the ID-keyed form even when the name does
// not change: the rename flow is the one moment we rewrite the URL anyway.
assert.equal(
  deviceDetailPathAfterRename("lab", "unifimoule1", ID, "unifimoule1"),
  `/lab/equipment/unifimoule1--${ID}`,
);

// Round-trip of the path segment itself: what the rename writes into the
// URL is what the detail page reads back to find the device.
assert.equal(decodeDevicePathSegment(devicePathSegment("core sw/1")), "core sw/1");
assert.equal(decodeDevicePathSegment(devicePathSegment("192.0.2.4")), "192.0.2.4");

console.log("paths.test.ts OK");
