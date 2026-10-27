/**
 * Layout arithmetic for the dashboard grid — run with:
 *   node --experimental-strip-types src/dashboardLayout.test.ts
 * (from web/), or `npm test` to run every file at once.
 *
 * The assertions are written as the FAILURES they prevent, not as coverage of
 * the exported surface: a tile that overlaps its neighbour, a drag that lands
 * one column off, a hole that closes itself under the operator's hands.
 */
import assert from "node:assert/strict";
// Node --experimental-strip-types needs the .ts suffix; tsc excludes this file.
import {
  GRID_COLUMNS,
  MAX_ROW,
  MAX_TILE_H,
  MAX_TILE_TITLE,
  addTile,
  cellDelta,
  cellFromPointer,
  clampTile,
  gridRows,
  moveTile,
  nextFreeSpot,
  overlaps,
  removeTile,
  resizeTile,
  sameLayout,
  tileTitle,
  withTileTitle,
  type Tile,
} from "./dashboardLayout.ts";

const tile = (type: string, x: number, y: number, w: number, h: number): Tile => ({
  type,
  x,
  y,
  w,
  h,
});

/** No pair of tiles may share a cell. The invariant the whole module exists for. */
function assertNoOverlap(tiles: Tile[], what: string) {
  for (let i = 0; i < tiles.length; i++) {
    for (let j = i + 1; j < tiles.length; j++) {
      assert.ok(
        !overlaps(tiles[i], tiles[j]),
        `${what}: tiles ${i} (${JSON.stringify(tiles[i])}) and ${j} ` +
          `(${JSON.stringify(tiles[j])}) overlap`,
      );
    }
  }
}

// --- Clamping: nothing leaves the grid, whatever it is handed ---

{
  const wide = clampTile(tile("t", 0, 0, 99, 1));
  assert.equal(wide.w, GRID_COLUMNS, "a tile wider than the grid is cut to the grid");

  const offRight = clampTile(tile("t", 11, 0, 4, 1));
  assert.equal(
    offRight.x + offRight.w <= GRID_COLUMNS,
    true,
    "a tile pushed past the right edge is brought back inside",
  );

  const negative = clampTile(tile("t", -5, -3, 2, 2));
  assert.equal(negative.x, 0);
  assert.equal(negative.y, 0);

  const tall = clampTile(tile("t", 0, 0, 2, 999));
  assert.equal(tall.h, MAX_TILE_H, "height is capped: a tile is not a scrollbar");

  const fractional = clampTile({ type: "t", x: 1.6, y: 2.4, w: 3.5, h: 1.2 });
  assert.deepEqual(
    [fractional.x, fractional.y, fractional.w, fractional.h],
    [2, 2, 4, 1],
    "cells are integers — a fractional position cannot reach storage",
  );

  const deep = clampTile(tile("t", 0, MAX_ROW + 50, 2, 2));
  assert.ok(deep.y + deep.h <= MAX_ROW, "the row ceiling holds");
}

// --- Moving: the tile the operator holds wins, the others move down ---

{
  // Two half-width cards side by side, as the default dashboard has them.
  const start = [tile("a", 0, 0, 6, 3), tile("b", 6, 0, 6, 3)];

  // Drop A exactly onto B.
  const after = moveTile(start, 0, 6, 0);
  assertNoOverlap(after, "A dropped onto B");
  assert.deepEqual(
    [after[0].x, after[0].y],
    [6, 0],
    "the tile being dragged lands exactly where it was dropped",
  );
  assert.equal(after[1].y, 3, "the tile underneath moves DOWN, clear of the anchor");
  assert.equal(after[1].x, 6, "and it does not drift sideways while doing so");

  // The caller's array is never mutated: React state depends on that.
  assert.deepEqual(start[0], tile("a", 0, 0, 6, 3), "moveTile does not mutate its input");
}

{
  // A push that cascades: three stacked tiles, the top one dropped on the middle.
  const start = [
    tile("a", 0, 0, 4, 1),
    tile("b", 0, 1, 4, 1),
    tile("c", 0, 2, 4, 1),
  ];
  const after = moveTile(start, 0, 0, 1);
  assertNoOverlap(after, "cascading push");
  assert.equal(after[0].y, 1, "the anchor stays where it was dropped");
  assert.ok(after[1].y >= 2, "the tile it landed on moved down");
  assert.ok(after[2].y >= 3, "and pushed the next one in turn");
}

{
  // NO GRAVITY. This is the property the module promises and the one a future
  // "let's just compact it" refactor would silently break.
  const spaced = [tile("a", 0, 0, 4, 1), tile("b", 0, 5, 4, 1)];
  const after = moveTile(spaced, 0, 4, 0);
  assert.equal(after[1].y, 5, "a deliberate gap survives a move that does not touch it");

  const holed = removeTile([tile("a", 0, 0, 4, 1), tile("b", 0, 3, 4, 1)], 0);
  assert.equal(holed.length, 1);
  assert.equal(holed[0].y, 3, "removing a tile does not suck the rest of the page up");
}

// --- Resizing: from the top-left corner, and never out of the grid ---

{
  const start = [tile("a", 0, 0, 3, 1), tile("b", 0, 1, 3, 1)];
  const after = resizeTile(start, 0, 6, 3);
  assert.deepEqual([after[0].x, after[0].y], [0, 0], "resizing does not move the corner");
  assert.deepEqual([after[0].w, after[0].h], [6, 3]);
  assertNoOverlap(after, "grown tile");
  assert.equal(after[1].y, 3, "the tile below is pushed clear of the new height");

  const atEdge = resizeTile([tile("a", 9, 0, 3, 1)], 0, 8, 1);
  assert.equal(atEdge[0].x, 9, "a tile at the right edge is not slid left to grow");
  assert.equal(atEdge[0].w, 3, "its width is capped by the edge instead");
}

// --- Adding: into a real gap, never on top of something ---

{
  const start = [tile("a", 0, 0, 6, 2), tile("b", 6, 0, 3, 2)];
  const spot = nextFreeSpot(start, 3, 2);
  assert.deepEqual(spot, { x: 9, y: 0 }, "the gap on the first row is found first");

  const full = [tile("a", 0, 0, GRID_COLUMNS, 2)];
  assert.deepEqual(
    nextFreeSpot(full, 6, 2),
    { x: 0, y: 2 },
    "a full row sends the new tile to the row below",
  );

  const added = addTile(start, "stat.rx", 3, 2);
  assert.equal(added.length, 3);
  assertNoOverlap(added, "added tile");
  assert.equal(added[2].type, "stat.rx");
}

// --- Rows ---

{
  assert.equal(gridRows([]), 0, "an empty dashboard needs no rows at all");
  assert.equal(gridRows([tile("a", 0, 0, 2, 1), tile("b", 0, 4, 2, 3)]), 7);
}

// --- Pointer → cell ---

{
  // A grid 1288px wide with 8px gaps: 12×100px of column plus 11×8px of gap, so
  // a column step is 108px. Rows are 84px + 8px = 92px.
  const metrics = { left: 100, top: 50, width: 1288, rowHeight: 84, gap: 8 };

  assert.deepEqual(
    cellFromPointer(metrics, 100, 50),
    { x: 0, y: 0 },
    "the top-left corner is cell (0,0)",
  );
  assert.deepEqual(
    cellFromPointer(metrics, 100 + 107, 50 + 91),
    { x: 0, y: 0 },
    "a pointer still inside the first cell stays in the first cell — floor, not " +
      "round-to-nearest, or a tile would jump before the drag began",
  );
  assert.deepEqual(cellFromPointer(metrics, 100 + 108, 50 + 92), { x: 1, y: 1 });
  assert.deepEqual(
    cellFromPointer(metrics, 100 + 108 * 11 + 50, 50),
    { x: 11, y: 0 },
    "the last column is reachable",
  );
  assert.deepEqual(
    cellFromPointer(metrics, 99999, 99999).x,
    GRID_COLUMNS - 1,
    "a pointer past the right edge is clamped to the last column",
  );
  assert.deepEqual(
    cellFromPointer(metrics, -500, -500),
    { x: 0, y: 0 },
    "and a pointer above and left of the grid to the first cell",
  );

  // Delta, which is what dragging actually uses.
  assert.deepEqual(
    cellDelta(metrics, 300, 300, 300, 300),
    { dx: 0, dy: 0 },
    "a drag that ends where it started moves nothing",
  );
  assert.deepEqual(
    cellDelta(metrics, 300, 300, 300 + 108 * 2, 300 + 92),
    { dx: 2, dy: 1 },
    "the tile follows the hand by the number of cells the hand moved",
  );
  assert.deepEqual(
    cellDelta(metrics, 300, 300, 300 - 108 * 3, 300),
    { dx: -3, dy: 0 },
    "and backwards too",
  );
}

// --- sameLayout: what decides whether a save is sent at all ---

{
  const a = [tile("x", 0, 0, 2, 1)];
  assert.ok(sameLayout(a, [tile("x", 0, 0, 2, 1)]), "identical layouts compare equal");
  assert.ok(!sameLayout(a, [tile("x", 1, 0, 2, 1)]), "a moved tile is a different layout");
  assert.ok(!sameLayout(a, []), "a removed tile is a different layout");
  assert.ok(
    !sameLayout(
      [{ ...a[0], options: { hours: 24 } }],
      [{ ...a[0], options: { hours: 168 } }],
    ),
    "a changed tile option is a different layout — otherwise the traffic window " +
      "an operator just picked would never be saved",
  );
  assert.ok(
    sameLayout([{ ...a[0], options: undefined }], [{ ...a[0], options: {} }]),
    "absent options and empty options are the same thing, and must not provoke a write",
  );
}

// --- The tile title: data, and the empty case that restores the default ---
//
// Every assertion here is a sentence of ADR-0027 that a refactor could quietly
// break — the empty-clears rule above all, because storing `title: ""` instead
// of deleting the key compiles, renders a blank header, and raises the Save
// button for a rename the operator cancelled.

{
  const base = tile("chart.traffic", 0, 0, 7, 3);

  assert.equal(tileTitle(base), "", "a tile with no options has no custom title");
  assert.equal(
    tileTitle({ ...base, options: { hours: 24 } }),
    "",
    "options without a title still mean no custom title",
  );
  assert.equal(
    tileTitle({ ...base, options: { title: 42 } }),
    "",
    "a non-string title is no title — never rendered as '42'",
  );
  assert.equal(
    tileTitle({ ...base, options: { title: "  Uplink  " } }),
    "Uplink",
    "a stored title is trimmed on the way out",
  );

  const named = withTileTitle(base, "  Server room traffic ");
  assert.equal(tileTitle(named), "Server room traffic", "a title is trimmed on the way in");
  assert.equal(base.options, undefined, "withTileTitle does not mutate its argument");

  const keptOptions = withTileTitle({ ...base, options: { hours: 168 } }, "Semaine");
  assert.equal(
    keptOptions.options?.hours,
    168,
    "renaming a tile leaves its other options alone — a rename is not a reset",
  );

  // THE RULE. Clearing the field deletes the key, so the catalogue's translated
  // label comes back in every language, and `sameLayout` sees no change against
  // a server copy that never had the key.
  const cleared = withTileTitle(named, "   ");
  assert.equal(tileTitle(cleared), "", "a blank title restores the default");
  assert.ok(
    !("title" in (cleared.options ?? {})),
    "a blank title DELETES the key rather than storing an empty string",
  );
  assert.ok(
    sameLayout([cleared], [base]),
    "a tile renamed and then cleared is the layout it started as, so leaving " +
      "edit mode has nothing to save",
  );

  // A paste is cut, not refused, and cut by CODE POINT so the bound the server
  // enforces in runes is never exceeded and no surrogate pair is halved.
  const long = withTileTitle(base, "é".repeat(MAX_TILE_TITLE + 40));
  assert.equal(
    Array.from(tileTitle(long)).length,
    MAX_TILE_TITLE,
    "an over-long title is cut to the bound the API enforces",
  );
  const emoji = withTileTitle(base, "🛰".repeat(MAX_TILE_TITLE + 10));
  assert.equal(
    Array.from(tileTitle(emoji)).length,
    MAX_TILE_TITLE,
    "the cut counts code points, so a title of emoji is not halved mid-character",
  );
}

// --- A whole default dashboard survives a round of edits without overlapping ---

{
  // The seeded "Dashboard" layout, as store.DefaultDashboards writes it.
  let tiles: Tile[] = [
    tile("stat.devices-online", 0, 0, 2, 1),
    tile("stat.active-ports", 2, 0, 2, 1),
    tile("stat.rx", 4, 0, 2, 1),
    tile("stat.tx", 6, 0, 2, 1),
    tile("stat.poe", 8, 0, 2, 1),
    tile("stat.alerts", 10, 0, 2, 1),
    tile("chart.traffic", 0, 1, 7, 3),
    tile("chart.vlan-ports", 7, 1, 5, 3),
    tile("list.alerts", 0, 4, 6, 3),
    tile("list.events", 6, 4, 6, 3),
  ];
  assertNoOverlap(tiles, "the seeded default layout");

  tiles = moveTile(tiles, 6, 5, 1); // drag the traffic chart right, onto its neighbour
  assertNoOverlap(tiles, "after dragging the traffic chart");
  tiles = resizeTile(tiles, 8, 12, 4); // widen the alert list to the full row
  assertNoOverlap(tiles, "after widening the alert list");
  tiles = addTile(tiles, "table.fleet", 12, 4);
  assertNoOverlap(tiles, "after adding the fleet table");
  tiles = removeTile(tiles, 0);
  assertNoOverlap(tiles, "after removing a stat tile");
  assert.ok(
    tiles.every((t) => t.x >= 0 && t.x + t.w <= GRID_COLUMNS && t.y >= 0),
    "every tile is still inside the grid after a full round of edits",
  );
}

console.log("dashboardLayout: OK");
