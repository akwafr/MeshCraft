/**
 * Lightweight checks for the low-bandwidth port tile — run with:
 *   node --experimental-strip-types src/lowBandwidth.test.ts
 * (from web/), or `npm test` to run every file at once.
 *
 * This tile points an operator at a cable. A false row costs them a trip to a
 * rack, so the assertions are written as the two ways of being wrong: listing a
 * port that is fine, and missing one that is not.
 */
import assert from "node:assert/strict";
// Node --experimental-strip-types needs the .ts suffix; tsc excludes this file.
import { clientsBehindPort, lowBandwidthPorts, speedMbps } from "./lowBandwidth.ts";

// The labels the fleet actually reports. Sampled from the live database rather
// than imagined: 1G, 10G and 100M are what the eight ranges in this product put
// in `link` today.
{
  assert.equal(speedMbps("100M"), 100);
  assert.equal(speedMbps("10M"), 10);
  assert.equal(speedMbps("1G"), 1000);
  assert.equal(speedMbps("10G"), 10000);
  assert.equal(speedMbps("2.5G"), 2500);
  assert.equal(speedMbps(" 100m "), 100);
}

// AN UNREADABLE LABEL IS NOT LOW. Guessing would put a port on the list on the
// strength of a string nobody parsed — and the whole value of the tile is that
// every row is worth walking to.
{
  for (const bad of ["", null, undefined, "auto", "up", "1", "G", "fast", "-1G"]) {
    assert.equal(speedMbps(bad as string), null, `speedMbps(${String(bad)})`);
  }
}

// THE CASE THE TILE EXISTS FOR: a gigabit access port that came up at 100 M.
{
  const rows = lowBandwidthPorts([
    {
      id: "d1",
      name: "sw-access-1",
      ports: {
        GE1: { link: "1G" },
        GE2: { link: "100M" },
        GE3: { link: "10M" },
        GE4: {},
      },
    },
  ]);
  assert.equal(rows.length, 2);
  // Slowest first: the 10 M port is the likeliest to be a fault rather than a
  // choice, so it is what an operator should read first.
  assert.equal(rows[0].port, "GE3");
  assert.equal(rows[0].mbps, 10);
  assert.equal(rows[1].port, "GE2");
  // The label travels verbatim — the operator compares it against what the
  // switch itself says, and a re-rendered "100 Mbit/s" would not match.
  assert.equal(rows[1].link, "100M");
}

// A PORT WITH NO LINK IS NOT LISTED. An unplugged 100 M jack is not a fault, and
// a tile full of empty ports would bury the two that matter.
{
  const rows = lowBandwidthPorts([
    { id: "d1", name: "sw", ports: { GE1: { link: null }, GE2: { link: "" } } },
  ]);
  assert.equal(rows.length, 0);
}

// A PORT THE SWITCH SHUT ITSELF is excluded even if it reports a speed.
// err-disabled is a fault the alert tile already owns; listing it here too would
// spread one problem across two tiles that then disagree on the count.
{
  const rows = lowBandwidthPorts([
    {
      id: "d1",
      name: "sw",
      ports: {
        GE1: { link: "100M", link_state: "err-disabled" },
        GE2: { link: "100M", link_state: "disabled" },
        GE3: { link: "100M", link_state: "up" },
      },
    },
  ]);
  assert.equal(rows.length, 1);
  assert.equal(rows[0].port, "GE3");
}

// UPLINKS ARE NOT FLAGGED. A 10 G cage running at 1 G is usually an SFP doing
// exactly what it is meant to; a rule keyed on "below the port's capability"
// would fill this tile with correct configuration and nobody would read it.
{
  const rows = lowBandwidthPorts([
    { id: "d1", name: "sw", ports: { XE1: { link: "1G" }, XE2: { link: "10G" } } },
  ]);
  assert.equal(rows.length, 0);
}

// Degenerate input must not throw: a device with no ports at all is ordinary
// (one just adopted), and the dashboard renders before any read has happened.
{
  assert.equal(lowBandwidthPorts([]).length, 0);
  assert.equal(lowBandwidthPorts([{ id: "d", name: "n" }]).length, 0);
  assert.equal(lowBandwidthPorts([{ id: "d", name: "n", ports: null }]).length, 0);
}

// Ordering is STABLE across devices at the same speed, so the list does not
// reshuffle under somebody reading it.
{
  const rows = lowBandwidthPorts([
    { id: "b", name: "sw-b", ports: { GE9: { link: "100M" }, GE1: { link: "100M" } } },
    { id: "a", name: "sw-a", ports: { GE1: { link: "100M" } } },
  ]);
  assert.deepEqual(
    rows.map((r) => `${r.device}/${r.port}`),
    ["sw-a/GE1", "sw-b/GE1", "sw-b/GE9"],
  );
}

// --- clientsBehindPort: who the tip says is behind the cable --------------
//
// Same stake as the rows above, one step further down the wire. The tile sends
// somebody to a patch panel; this names the machine they will find there, and
// naming the wrong one — or one that left in June — costs the same trip.

const LEDGER = [
  // The port under test, three sightings: two live, one long gone.
  { mac: "00:11:22:33:44:01", name: "ap-hall", device_id: "d1", port: "GE1", online: true, last_seen: "2026-10-24T09:00:00Z" },
  { mac: "00:11:22:33:44:02", vendor: "Hewlett Packard", device_id: "d1", port: "GE1", online: true, last_seen: "2026-10-24T10:00:00Z" },
  { mac: "00:11:22:33:44:03", name: "laptop-gone", device_id: "d1", port: "GE1", online: false, last_seen: "2026-06-01T10:00:00Z" },
  // Same MAC grammar, other places: a different port, and a different device.
  { mac: "00:11:22:33:44:04", device_id: "d1", port: "GE2", online: true, last_seen: "2026-10-24T10:00:00Z" },
  { mac: "00:11:22:33:44:05", device_id: "d2", port: "GE1", online: true, last_seen: "2026-10-24T10:00:00Z" },
];

// THE LOCATION IS (device, port), BOTH HALVES. A filter on the port alone
// would drag in every GE1 in the fleet — which on a fleet of identically
// named front panels is most of it.
{
  const behind = clientsBehindPort(LEDGER, "d1", "GE1");
  assert.deepEqual(
    behind.map((c) => c.mac),
    // Freshest first; the offline row is not here at all.
    ["00:11:22:33:44:02", "00:11:22:33:44:01"],
  );
}

// AN OFFLINE SIGHTING IS NOT A CLIENT. The ledger is append-only, so a MAC
// that passed through once is in it forever; listing it under "behind this
// port" would send somebody looking for a machine that has left.
{
  const behind = clientsBehindPort(LEDGER, "d1", "GE1");
  assert.equal(behind.some((c) => c.name === "laptop-gone"), false);
}

// Ties break on the MAC, so two sightings recorded in the same second cannot
// swap places between two refreshes of the tip.
{
  const same = [
    { mac: "00:00:00:00:00:0b", device_id: "d", port: "p", online: true, last_seen: "2026-10-24T10:00:00Z" },
    { mac: "00:00:00:00:00:0a", device_id: "d", port: "p", online: true, last_seen: "2026-10-24T10:00:00Z" },
  ];
  assert.deepEqual(
    clientsBehindPort(same, "d", "p").map((c) => c.mac),
    ["00:00:00:00:00:0a", "00:00:00:00:00:0b"],
  );
}

// No ledger yet is not an empty ledger, but it renders the same way: the tip
// distinguishes them by whether the query is still pending, not here.
{
  assert.equal(clientsBehindPort(undefined, "d1", "GE1").length, 0);
  assert.equal(clientsBehindPort([], "d1", "GE1").length, 0);
}

// The input is not mutated — the array handed in is react-query's cached
// payload, shared with the Clients page, and sorting it in place would reorder
// that page's table from under it.
{
  const order = LEDGER.map((c) => c.mac);
  clientsBehindPort(LEDGER, "d1", "GE1");
  assert.deepEqual(LEDGER.map((c) => c.mac), order);
}

console.log("lowBandwidth: OK");
