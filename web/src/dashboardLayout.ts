/**
 * Dashboard layout: where a tile sits, and what happens to its neighbours when
 * it moves.
 *
 * # Why CSS grid with discrete cells, and not free pixel positioning
 *
 * The obvious alternative was absolute positioning with a drag library. Two
 * reasons it is not that.
 *
 * The first is a project rule: no third-party drag/resize dependency. That
 * leaves writing one, and a hand-written free-positioning engine is where the
 * hard cases live — sub-pixel drift, overlapping tiles, layouts that look
 * different on a narrower screen than the one they were drawn on.
 *
 * The second is that free positioning would be a WORSE product here. These
 * tiles are charts and tables: they need to line up, and a dashboard shown on a
 * 4K wall display and on a laptop must be the same dashboard. A 12-column grid
 * with integer cells gives that by construction — a tile is "four columns wide
 * starting at column three", which means the same thing at any width, and the
 * browser does the arithmetic in CSS.
 *
 * So: placement is discrete. Dragging and resizing are still pointer-driven —
 * the operator grabs a tile and moves it — but what the pointer produces is a
 * CELL, computed by `cellFromPointer`, and every position this module ever
 * stores is an integer. Nothing here knows about pixels except that one
 * function, which is exactly the seam a test can hold.
 *
 * # No gravity, deliberately
 *
 * Grid systems usually "compact" upward: tiles fall to fill any hole above
 * them. This one does not, and that is a decision rather than an omission.
 * Compaction fights the person arranging the screen — an operator who leaves a
 * blank row between the alert tiles and the log, because the wall display is
 * read from six metres away, would have that gap closed on every save with
 * nothing to do about it. What this module does instead is the minimum that
 * keeps a layout coherent: a tile never overlaps another. Move one onto its
 * neighbours and they move DOWN, out of the way, and stay where they land.
 *
 * This file imports NOTHING. That is a constraint, not a coincidence: the
 * framework-free test runner (node --experimental-strip-types) cannot load a
 * module that reaches i18n or React, which is why the layout arithmetic lives
 * here in a leaf and the components import it rather than the reverse. Same
 * reason as portLabel.ts and pages/portAdmin.ts.
 */

/**
 * The grid is twelve columns wide.
 *
 * Twelve because it divides by 2, 3, 4 and 6: the old screens wanted six stat
 * tiles in a row, two half-width cards, and a 7/5 split, and all three are
 * expressible without a fraction. This constant is duplicated in the API's
 * input check and in the SQL constraint of migration 0041 — see the comment
 * there for why the duplication is on purpose.
 */
export const GRID_COLUMNS = 12;

/** A tile is at least one cell in each direction. Below that it is invisible. */
export const MIN_TILE_W = 1;
export const MIN_TILE_H = 1;

/**
 * Height ceiling, in rows. Matches the API's `dashboardMaxTileHeight`: a tile
 * taller than the screen is not a tile, it is a scrollbar with a border.
 */
export const MAX_TILE_H = 24;

/**
 * Row ceiling. A dashboard cannot grow past this, which is what stops a
 * cascade of pushes from sending a tile to row 10^6 and leaving the operator
 * with a page that scrolls for a minute into nothing.
 */
export const MAX_ROW = 200;

/** How many settle passes before we stop trying. See `settle`. */
const SETTLE_PASSES = 8;

export interface Tile {
  /** Catalogue key — see the tile registry, and store.TileCatalog server-side. */
  type: string;
  x: number;
  y: number;
  w: number;
  h: number;
  /**
   * Per-tile settings (traffic window, log severity…), the operator's own
   * `title`, and a targeted tile's `device_id` / `port`. Opaque here except
   * for the title, whose rule is three functions below — see ADR-0027.
   */
  options?: Record<string, unknown>;
}

/**
 * Longest title an operator may give a tile. Mirrors `dashboardMaxTileTitle`
 * in internal/api/dashboard_handlers.go, which refuses anything past it: this
 * copy stops the field from accepting what the save would reject.
 *
 * It is NOT a promise that sixty characters fit. A tile can be two columns
 * wide; the header truncates with CSS, and the bound only keeps the stored
 * value a title rather than a document.
 */
export const MAX_TILE_TITLE = 60;

/**
 * The operator's own title for a tile, or "" when it has none.
 *
 * "" is the ONE answer for "no custom title", whatever the stored shape — key
 * absent, null, a number a buggy client wrote, or whitespace. The caller falls
 * back to the catalogue's translated label on "", so a tile that was never
 * renamed keeps a title that follows the interface language (ADR-0027).
 */
export function tileTitle(t: Tile): string {
  const raw = t.options?.title;
  return typeof raw === "string" ? raw.trim() : "";
}

/**
 * Set — or CLEAR — a tile's title. Returns a new tile; the argument is
 * untouched.
 *
 * An empty or blank title DELETES the key instead of storing "". That is the
 * decision, not an implementation detail (ADR-0027), and it buys two things:
 *
 *   - clearing the field brings back the translated default, in every language,
 *     which is the only way to undo a rename without retyping a translation;
 *   - "no custom title" stays a single state. `sameLayout` compares options by
 *     JSON, so a tile carrying `title: ""` would differ from the one the server
 *     sent and raise the Save button for a rename the operator cancelled.
 *
 * Truncation is at MAX_TILE_TITLE rather than a refusal: the field bounds the
 * input already, so anything arriving longer came from a paste, and cutting a
 * paste is what every bounded field does.
 */
export function withTileTitle(t: Tile, title: string): Tile {
  const next = { ...(t.options ?? {}) };
  // Cut by CODE POINT, not by code unit: the server counts runes, and
  // `"…".slice(60)` on a string of emoji splits a surrogate pair in half —
  // storing half a character to respect a bound it was already under.
  const clean = Array.from(title.trim()).slice(0, MAX_TILE_TITLE).join("");
  if (clean === "") delete next.title;
  else next.title = clean;
  return { ...t, options: next };
}

/** Bring one tile inside the grid. Pure; never mutates its argument. */
export function clampTile(t: Tile): Tile {
  const w = Math.min(GRID_COLUMNS, Math.max(MIN_TILE_W, Math.round(t.w)));
  const h = Math.min(MAX_TILE_H, Math.max(MIN_TILE_H, Math.round(t.h)));
  const x = Math.min(GRID_COLUMNS - w, Math.max(0, Math.round(t.x)));
  const y = Math.min(MAX_ROW - h, Math.max(0, Math.round(t.y)));
  return { ...t, x, y, w, h };
}

/** Do two tiles share at least one cell? */
export function overlaps(a: Tile, b: Tile): boolean {
  return (
    a.x < b.x + b.w && b.x < a.x + a.w && a.y < b.y + b.h && b.y < a.y + a.h
  );
}

/**
 * Reading order: top row first, then left to right. Used to decide which of two
 * overlapping tiles yields — the lower one does, because pushing the upper one
 * down would move the tile the operator was NOT touching further than the one
 * they were.
 */
function readingOrder(a: Tile, b: Tile): number {
  if (a.y !== b.y) return a.y - b.y;
  if (a.x !== b.x) return a.x - b.x;
  return 0;
}

/**
 * Resolve overlaps around an anchor tile, by pushing the others DOWN.
 *
 * The anchor is the tile the operator is holding: it stays exactly where it was
 * put, and everything it lands on moves out from under it. Tiles pushed that
 * way can in turn land on others, so this repeats until nothing moves — bounded
 * by SETTLE_PASSES, because a pathological layout (tiles jammed against
 * MAX_ROW, unable to move further) would otherwise loop forever. Hitting the
 * bound leaves a visible overlap, which is the honest failure: the operator can
 * see it and move something, where a silent reshuffle of their whole screen
 * would leave them with no idea what happened.
 *
 * `anchor` may be -1, meaning "no tile is privileged" — that is what removal
 * and bulk loading use.
 */
function settle(tiles: Tile[], anchor: number): Tile[] {
  const out = tiles.map(clampTile);
  let pass = 0;
  for (pass = 0; pass < SETTLE_PASSES; pass++) {
    // Order: the anchor first (it never yields), then reading order. Rebuilt
    // every pass because pushing tiles changes their order.
    const order = out.map((_, i) => i);
    order.sort((i, j) => {
      if (i === anchor) return -1;
      if (j === anchor) return 1;
      const byPos = readingOrder(out[i], out[j]);
      return byPos !== 0 ? byPos : i - j;
    });

    let moved = false;
    let k = 0;
    for (k = 0; k < order.length; k++) {
      let m = 0;
      for (m = k + 1; m < order.length; m++) {
        const i = order[k];
        const j = order[m];
        if (!overlaps(out[i], out[j])) continue;
        // `j` comes later in the order, so `j` yields.
        const pushedTo = out[i].y + out[i].h;
        if (pushedTo === out[j].y) continue; // already clear; nothing to do
        out[j] = clampTile({ ...out[j], y: pushedTo });
        moved = true;
      }
    }
    if (!moved) break;
  }
  return out;
}

/**
 * Move a tile to a cell. Returns a NEW array; the caller's is untouched.
 *
 * Out-of-range targets are clamped rather than refused: a drag that leaves the
 * grid by two columns means "as far right as it goes", and refusing would
 * make the tile snap back to where it started for a gesture the operator
 * considers successful.
 */
export function moveTile(tiles: Tile[], index: number, x: number, y: number): Tile[] {
  if (index < 0 || index >= tiles.length) return tiles;
  const next = tiles.slice();
  next[index] = clampTile({ ...next[index], x, y });
  return settle(next, index);
}

/**
 * Resize a tile from its top-left corner: x and y stay, w and h change.
 *
 * Growing past the right edge is clamped to the edge, not refused, for the same
 * reason as moving — and clamping the WIDTH rather than sliding the tile left,
 * because a resize that moved the tile would be a resize the operator did not
 * ask for.
 */
export function resizeTile(tiles: Tile[], index: number, w: number, h: number): Tile[] {
  if (index < 0 || index >= tiles.length) return tiles;
  const cur = tiles[index];
  const next = tiles.slice();
  next[index] = clampTile({
    ...cur,
    w: Math.min(w, GRID_COLUMNS - cur.x),
    h,
  });
  return settle(next, index);
}

/**
 * Remove a tile. The hole it leaves STAYS a hole — see the "no gravity" note
 * at the top of this file. Removing a tile is not permission to rearrange the
 * rest of the screen.
 */
export function removeTile(tiles: Tile[], index: number): Tile[] {
  if (index < 0 || index >= tiles.length) return tiles;
  return tiles.filter((_, i) => i !== index);
}

/**
 * First free cell that fits a w×h tile, scanning in reading order.
 *
 * Used when adding a tile: dropping it at (0,0) would land it on top of
 * whatever is already there and push half the dashboard down, which is a
 * violent answer to "add a tile". Scanning finds the first genuine gap
 * instead, and falls back to the row below everything when there is none.
 */
export function nextFreeSpot(tiles: Tile[], w: number, h: number): { x: number; y: number } {
  const width = Math.min(GRID_COLUMNS, Math.max(MIN_TILE_W, w));
  const height = Math.max(MIN_TILE_H, h);
  const bottom = gridRows(tiles);
  let y = 0;
  for (y = 0; y <= bottom; y++) {
    let x = 0;
    for (x = 0; x + width <= GRID_COLUMNS; x++) {
      const probe: Tile = { type: "", x, y, w: width, h: height };
      if (!tiles.some((t) => overlaps(t, probe))) return { x, y };
    }
  }
  return { x: 0, y: Math.min(bottom, MAX_ROW - height) };
}

/** Add a tile at the first free spot. */
export function addTile(
  tiles: Tile[],
  type: string,
  w: number,
  h: number,
  options?: Record<string, unknown>,
): Tile[] {
  const spot = nextFreeSpot(tiles, w, h);
  return tiles.concat(clampTile({ type, x: spot.x, y: spot.y, w, h, options }));
}

/**
 * How many rows the grid needs. Zero tiles means zero rows — the caller draws
 * its empty state rather than an empty grid, which would be a page of nothing
 * with no explanation.
 */
export function gridRows(tiles: Tile[]): number {
  return tiles.reduce((max, t) => Math.max(max, t.y + t.h), 0);
}

/** Geometry of the rendered grid, as the DOM reports it. */
export interface GridMetrics {
  /** Bounding box of the grid container, in client coordinates. */
  left: number;
  top: number;
  width: number;
  /** Height of one row, in pixels. */
  rowHeight: number;
  /** Gap between cells, in pixels — the same value in both directions. */
  gap: number;
}

/**
 * Which cell is under a pointer.
 *
 * THE ONLY function in this module that knows pixels exist, and it is a
 * function rather than inline arithmetic in a pointer handler precisely so it
 * can be tested: a drag that lands one column off is the kind of defect that is
 * obvious on screen and impossible to reproduce in a bug report.
 *
 * Rounding is FLOOR, not round-to-nearest: the cell a pointer is inside is the
 * cell it is inside. Round-to-nearest would put the boundary in the middle of a
 * cell, so a tile grabbed near its right edge would jump one column right on
 * pointer-down before the operator had moved at all.
 */
export function cellFromPointer(
  m: GridMetrics,
  clientX: number,
  clientY: number,
): { x: number; y: number } {
  const colWidth = (m.width - m.gap * (GRID_COLUMNS - 1)) / GRID_COLUMNS;
  const stepX = colWidth + m.gap;
  const stepY = m.rowHeight + m.gap;
  const x = stepX > 0 ? Math.floor((clientX - m.left) / stepX) : 0;
  const y = stepY > 0 ? Math.floor((clientY - m.top) / stepY) : 0;
  return {
    x: Math.min(GRID_COLUMNS - 1, Math.max(0, x)),
    y: Math.min(MAX_ROW - 1, Math.max(0, y)),
  };
}

/**
 * Cell DELTA between two pointer positions.
 *
 * Dragging uses this rather than `cellFromPointer` on the current position: a
 * tile is grabbed somewhere in its middle, and moving it to "the cell under
 * the pointer" would teleport its top-left corner under the cursor on the
 * first pixel of movement. What the operator expects is that the tile follows
 * their hand by the same number of cells they moved it.
 */
export function cellDelta(
  m: GridMetrics,
  fromX: number,
  fromY: number,
  toX: number,
  toY: number,
): { dx: number; dy: number } {
  const colWidth = (m.width - m.gap * (GRID_COLUMNS - 1)) / GRID_COLUMNS;
  const stepX = colWidth + m.gap;
  const stepY = m.rowHeight + m.gap;
  return {
    dx: stepX > 0 ? Math.round((toX - fromX) / stepX) : 0,
    dy: stepY > 0 ? Math.round((toY - fromY) / stepY) : 0,
  };
}

/**
 * Do two layouts describe the same screen?
 *
 * Used to decide whether leaving edit mode has anything to save. Comparing the
 * arrays by reference would ask the server to rewrite the layout after a drag
 * that ended where it started, and every such write is a row of tiles deleted
 * and reinserted (store.SetDashboardTiles).
 */
export function sameLayout(a: Tile[], b: Tile[]): boolean {
  if (a.length !== b.length) return false;
  return a.every((t, i) => {
    const o = b[i];
    return (
      t.type === o.type &&
      t.x === o.x &&
      t.y === o.y &&
      t.w === o.w &&
      t.h === o.h &&
      JSON.stringify(t.options ?? {}) === JSON.stringify(o.options ?? {})
    );
  });
}
