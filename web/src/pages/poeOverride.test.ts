/**
 * Lightweight checks for poeOverrides — run with:
 *   node --experimental-strip-types src/pages/poeOverride.test.ts
 * (from web/), or rely on tsc --noEmit covering types.
 */
import assert from "node:assert/strict";
// Node --experimental-strip-types needs the .ts suffix; tsc excludes this file.
import { poeOverrides } from "./poeOverride.ts";

const groups = [
  { id: "g-bureaux", name: "Bureaux", poe: true },
  { id: "g-cameras", name: "Cameras", poe: true },
  { id: "g-uplink", name: "Uplink", poe: false },
];

// A selection MIXING groups: each port is compared against ITS OWN group,
// both override directions are caught, aligned ports stay silent.
{
  const rows = poeOverrides(
    [
      // follows Bureaux (poe on), operator cut it -> row
      { name: "GE1", poe: false, profile_id: "g-bureaux" },
      // follows Bureaux, aligned -> no row
      { name: "GE2", poe: true, profile_id: "g-bureaux" },
      // follows Uplink (poe off), operator powered it -> row
      { name: "GE9", poe: true, profile_id: "g-uplink" },
      // follows Cameras, aligned -> no row
      { name: "GE10", poe: true, profile_id: "g-cameras" },
    ],
    groups,
  );
  assert.equal(rows.length, 2);
  assert.deepEqual(rows[0], {
    port: "GE1", portPoE: false,
    groupId: "g-bureaux", groupName: "Bureaux", groupPoE: true,
  });
  assert.deepEqual(rows[1], {
    port: "GE9", portPoE: true,
    groupId: "g-uplink", groupName: "Uplink", groupPoE: false,
  });
}

// No follow, no row: a manual port has nothing to be compared against —
// whatever its PoE value.
{
  const rows = poeOverrides(
    [
      { name: "GE1", poe: true },
      { name: "GE2", poe: false, profile_id: null },
    ],
    groups,
  );
  assert.equal(rows.length, 0);
}

// A DANGLING follow (group deleted) yields no row either: the panel already
// tells that story on the VLAN lane; a PoE row would offer a repair toward a
// value that no longer exists.
{
  const rows = poeOverrides(
    [{ name: "GE1", poe: true, profile_id: "g-disparu" }],
    groups,
  );
  assert.equal(rows.length, 0);
}

// Absent poe on the port compares as false: a group prescribing power flags
// it, a group prescribing none does not.
{
  const rows = poeOverrides(
    [
      { name: "GE1", profile_id: "g-bureaux" },
      { name: "GE2", profile_id: "g-uplink" },
    ],
    groups,
  );
  assert.equal(rows.length, 1);
  assert.equal(rows[0].port, "GE1");
  assert.equal(rows[0].portPoE, false);
}

console.log("poeOverride.test.ts: ok");
