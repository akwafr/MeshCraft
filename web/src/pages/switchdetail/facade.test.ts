/**
 * Lightweight checks for the faceplate's two shape decisions — run with:
 *   node --experimental-strip-types src/pages/switchdetail/facade.test.ts
 * (from web/), or `npm test` to run every file at once.
 *
 * Both functions decide what an operator SEES on the front of the box, and both
 * have a way of being wrong that no type catches: a faceplate split into two
 * labelled blocks where the hardware has one continuous row sends someone
 * looking for a second module that does not exist, and a port blinking
 * "provisioning" that nothing is writing turns the animation into noise the day
 * something really is being written.
 */
import assert from "node:assert/strict";
// Node --experimental-strip-types needs the .ts suffix; tsc excludes this file.
import { buildFacade, driftMap } from "./facade.ts";

// Ports are compared by identity below: `buildFacade` slices, it never rebuilds.
const ports = Array.from({ length: 26 }, (_, i) => ({ name: "GE" + (i + 1) })) as never[];

// NO GROUPS is not an error and not a fabricated block. A connector that
// declares no faceplate gets no faceplate — inventing one row of "every port we
// know about" would draw hardware layout we were never told.
{
  assert.deepEqual(buildFacade(undefined, ports), []);
  assert.deepEqual(buildFacade([], ports), []);
}

// A SMALL BLOCK stays on ONE row, and takes exactly its `count` ports from where
// the previous group stopped. The label falls back to the standard prefix when
// the connector named none.
{
  const f = buildFacade(
    [{ prefix: "GE", count: 8 }, { prefix: "SFP", count: 2, label: "SFP+" }],
    ports,
  );
  assert.equal(f.length, 2);
  assert.equal(f[0].label, "GE");
  assert.equal(f[0].rows.length, 1);
  assert.deepEqual(f[0].rows[0], ports.slice(0, 8));
  assert.equal(f[1].label, "SFP+");
  assert.deepEqual(f[1].rows[0], ports.slice(8, 10));
}

// SIXTEEN OR MORE ports interleave into two rows, odd above and even below, as
// they are stamped on real hardware. Getting this backwards (a naive first-half
// / second-half split) puts port 13 under port 12 instead of under port 11, and
// every ⇧-range an operator draws by eye then takes the wrong sockets.
{
  const f = buildFacade([{ prefix: "GE", count: 24 }], ports);
  assert.equal(f[0].rows.length, 2);
  assert.deepEqual(f[0].rows[0].map((p: { name: string }) => p.name).slice(0, 3), ["GE1", "GE3", "GE5"]);
  assert.deepEqual(f[0].rows[1].map((p: { name: string }) => p.name).slice(0, 3), ["GE2", "GE4", "GE6"]);
  assert.equal(f[0].rows[0].length, 12);
  assert.equal(f[0].rows[1].length, 12);
}

// GROUPS SHARING A `row` MERGE into one block — the EdgePoint S16 case: 16 RJ45
// then 2 SFP+ cages, numbered continuously on ONE physical row, split into two
// groups only because their PoE and media differ. The merged block takes the
// FIRST group's label, and being ≥16 ports it then interleaves as one row pair.
{
  const f = buildFacade(
    [
      { prefix: "GE", count: 16, label: "Front", row: 0 },
      { prefix: "SFP", count: 2, label: "SFP+", row: 0 },
    ],
    ports,
  );
  assert.equal(f.length, 1);
  assert.equal(f[0].label, "Front");
  assert.equal(f[0].rows[0].length + f[0].rows[1].length, 18);
}

// A group WITHOUT `row` keeps its own block even next to one that has it: the
// absent field is every connector written before it existed, and it must keep
// meaning "separate hardware block", never "merge with whatever is nearby".
{
  const f = buildFacade(
    [
      { prefix: "GE", count: 8, row: 1 },
      { prefix: "SFP", count: 2 },
      { prefix: "XG", count: 2, row: 1 },
    ],
    ports,
  );
  assert.deepEqual(f.map((b) => b.label), ["GE", "SFP"]);
  // The two `row: 1` groups landed in one block; the ungrouped one is its own.
  assert.equal(f[0].rows[0].length, 10);
  assert.equal(f[1].rows[0].length, 2);
}

// NOTHING BLINKS on a device in sync, even when it carries a stale drift list.
// The list is only read once `sync` says there is something to read.
{
  const m = driftMap({ sync: "in_sync", drift: [{ port: "GE1" }] } as never);
  assert.equal(m.size, 0);
  assert.equal(driftMap(undefined).size, 0);
}

// PENDING and DRIFT each stamp their own animation, and only on the ports the
// device actually named. Flashing the whole faceplate would cost minutes of
// hunting for the port that is wrong.
{
  const m = driftMap({ sync: "drift", drift: [{ port: "GE1" }, { port: "GE7" }] } as never);
  assert.deepEqual([...m.entries()], [["GE1", "drift"], ["GE7", "drift"]]);
  assert.equal(driftMap({ sync: "pending", drift: [{ port: "GE1" }] } as never).get("GE1"), "pending");
}

// AN APPLY IN FLIGHT OVERRIDES both: the same ports switch from the slow
// "pending" fade to the stronger "provisioning" one. `applying` wins whatever
// `sync` said, because a write actually running is the more urgent fact.
{
  const m = driftMap({ sync: "pending", applying: true, drift: [{ port: "GE1" }] } as never);
  assert.equal(m.get("GE1"), "applying");
}

console.log("facade: OK");
