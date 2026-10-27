/**
 * Lightweight checks for the Clients page's MAC search — run with:
 *   node --experimental-strip-types src/pages/macSearch.test.ts
 * (from web/), or `npm test` to run every file at once.
 *
 * The module exists so that an address pasted from ANY console finds its row,
 * so the assertions below are written as that failure: one real address from
 * the lab, spelled every way the trade spells it, each of which must find the
 * one row the page renders in IEEE colon form.
 */
import assert from "node:assert/strict";
// Node --experimental-strip-types needs the .ts suffix; tsc excludes this file.
import { macKey, macMatches, parseMacQuery } from "./macSearch.ts";

/** As the API renders it, which is the only form the row itself carries. */
const ROW = "00:d8:61:56:f5:5a";

/** Every spelling of ROW, as five different tools print it. */
const SPELLINGS = [
  "00d86156f55a", // attached — lease files, exports
  "00.d8.61.56.f5.5a", // dotted pairs
  "00:d8:61:56:f5:5a", // IEEE / Linux
  "00d8.6156.f55a", // Cisco, FS, EdgeSwitch CLI
  "00-d8-61-56-f5-5a", // Windows ipconfig
  "00 d8 61 56 f5 5a", // pasted out of a table
];

// THE WHOLE POINT. Each form must find the row, and the row's own form must
// find itself — a normalisation that only worked one way round would pass a
// test written on the query alone.
for (const typed of SPELLINGS) {
  assert.ok(
    macMatches(ROW, parseMacQuery(typed)),
    `"${typed}" must find ${ROW} — a console paste is the main way this box is used`,
  );
  assert.equal(macKey(typed), "00d86156f55a", `"${typed}" normalises wrong`);
}

// CASE. Vendors print both; the registry and our own API disagree already.
for (const typed of ["00D86156F55A", "00:D8:61:56:F5:5A", "00D8.6156.F55A"]) {
  assert.ok(macMatches(ROW, parseMacQuery(typed)), `"${typed}" must match case-insensitively`);
}
assert.ok(
  macMatches("00:D8:61:56:F5:5A", parseMacQuery("00d8.6156.f55a")),
  "an UPPER-case row must be found by a lower-case query",
);

// PARTIAL, and anywhere — not just a prefix. The fragment a human carries in
// their head out of a console is a middle slice, and the OUI half is shared by
// every device of a vendor, so a prefix-only search would be the useless half.
for (const fragment of [
  "6156f5", // the middle, as asked for
  "61:56:f5", // the same, still grouped
  "6156.f5", // and in Cisco grouping
  "00d8", // the OUI alone
  "f55a", // the tail alone
  "5a", // one byte
]) {
  assert.ok(
    macMatches(ROW, parseMacQuery(fragment)),
    `"${fragment}" must match as a partial term`,
  );
}

// AND IT MUST STILL DISCRIMINATE. A search that matched everything would pass
// every assertion above.
for (const other of ["00:d8:61:56:f5:5b", "04:f4:1c:1d:74:58", "b8:69:f4:5b:a0:d2"]) {
  assert.ok(
    !macMatches(other, parseMacQuery("00d86156f55a")),
    `${other} must NOT match a full, different address`,
  );
}
assert.ok(
  !macMatches("04:f4:1c:1d:74:58", parseMacQuery("6156f5")),
  "a partial term must not match an unrelated address",
);

// EMPTY IS NOT A FILTER. The field starts empty and is cleared constantly; if
// that read as a term, the page would go blank on every clear.
for (const blank of ["", "   ", ":", "-", ".", "::", " . : - "]) {
  assert.equal(parseMacQuery(blank).kind, "off", `"${blank}" must not filter at all`);
  assert.ok(macMatches(ROW, parseMacQuery(blank)), `"${blank}" must leave every row shown`);
}

// AND A NON-HEX QUERY IS NOT A FILTER-FREE ONE EITHER. "Apple" holds hex
// letters; dropping the rest would silently search for "ae" and show rows
// that have nothing to do with what was typed. No match is the honest answer.
for (const prose of ["Apple", "proxmox", "unknown", "00:d8:61:56:f5:5z", "port GE1/0/4"]) {
  assert.equal(
    parseMacQuery(prose).kind,
    "impossible",
    `"${prose}" cannot be part of any MAC and must be refused as a term`,
  );
  assert.ok(!macMatches(ROW, parseMacQuery(prose)), `"${prose}" must match no row`);
}

// WHAT CANNOT BE TOLD APART, stated so nobody later "fixes" it. A dotted
// IPv4 of pure digits ("192.168.1.1" → "19216811") reduces to a valid hex
// term: the module has no way to know it was not a Cisco-grouped fragment,
// and guessing would cost the tolerance that is the whole point. It simply
// matches no row, which is the same thing the operator sees either way.
assert.equal(parseMacQuery("192.168.1.1").kind, "hex", "an all-digit IPv4 reduces to hex");
assert.ok(!macMatches(ROW, parseMacQuery("192.168.1.1")), "…and still finds nothing");

// A query LONGER than an address matches nothing rather than throwing — the
// field accepts a double paste like any other text input.
assert.ok(!macMatches(ROW, parseMacQuery("00d86156f55a00d86156f55a")), "an over-long term matches nothing");

console.log("macSearch: OK");
