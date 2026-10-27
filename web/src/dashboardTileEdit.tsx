/**
 * THE ONE EXCEPTION TO AN INERT TILE — ADR-0032.
 *
 * In edit mode on the plane, a tile's content stops receiving pointer events so
 * the whole tile is one drag target (`dashboardGrid.tsx` has the reasoning, and
 * it is a good reason: a chart that was both draggable and clickable would turn
 * every click into a one-pixel drag). The cost was that a tile could not carry
 * a FORM in edit mode — a picker reachable only there is a picker nobody can
 * open — which is why the port-traffic tile shipped with its selectors visible
 * on every ordinary viewing of the dashboard.
 *
 * `TileSettings` is the declared exception. A tile wraps its edit-mode settings
 * in it and gets back the three things a form needs, which are three different
 * mechanisms and not one:
 *
 *   1. POINTER EVENTS, by `pointer-events: auto` in the stylesheet, which
 *      cancels the ancestor's `none` locally.
 *   2. THE DRAG, by swallowing `pointerdown` here. Without it the event reaches
 *      the tile root, whose handler starts a move: the click that opens a
 *      picker would shift the tile by a cell. The tile's rename/remove pair
 *      already does exactly this — the live region generalises their pattern.
 *   3. THE KEYBOARD, by `inLiveTileRegion`, which the grid's key handler asks
 *      before acting. In edit mode the arrows move the tile, Shift+arrows
 *      resize it and Delete REMOVES it; inside a search field those same keys
 *      mean move the caret, select, and erase a character. Delete is the one
 *      that matters: correcting a typo in a picker's search box would have
 *      deleted the tile.
 *
 * This module exists rather than two string literals in two files because the
 * class name IS the contract: the stylesheet, the grid's keyboard guard and the
 * tiles all have to agree on it, and three copies of `"dash-tile-live"` is how
 * they stop agreeing.
 *
 * The bounds are in the ADR and worth repeating in one line each: a live region
 * carries SETTINGS and never content, so the curve stays inert; one per tile,
 * at the top of the body, clear of the resize handle in the bottom-right corner
 * and the tools in the top-right; and it is still clipped by the cell, because
 * a tile that overflowed its carreau for a dropdown would end the discipline
 * the whole plane rests on.
 */
import type React from "react";

/** The class the stylesheet, the grid and the tiles all key on. */
export const LIVE_TILE_REGION = "dash-tile-live";

/**
 * Whether an event came from inside a tile's live region — the question the
 * grid's key handler asks before it moves, resizes or removes a tile.
 *
 * Takes the event TARGET and not the focused element: `focusInEditable()` would
 * have been the obvious reuse and is wrong here, because a picker's trigger and
 * its option rows are button elements, which that guard waves through.
 * Arrow-down on a closed picker opens it, and Delete on a focused option would
 * still have removed the tile.
 */
export function inLiveTileRegion(target: EventTarget | null): boolean {
  if (!(target instanceof Element)) return false;
  return !!target.closest("." + LIVE_TILE_REGION);
}

/**
 * A tile's edit-mode settings panel.
 *
 * Renders nothing at all when not editing — that is the other half of the
 * request this serves: a dashboard being READ shows measurements, not forms.
 */
export function TileSettings({
  editing,
  className,
  children,
}: {
  editing: boolean;
  /** Extra class for the panel's own layout. The live-region class is added. */
  className?: string;
  children: React.ReactNode;
}) {
  if (!editing) return null;
  return (
    <div
      className={LIVE_TILE_REGION + (className ? " " + className : "")}
      // See (2) above. Pointerdown only: outside-click dismissal listens on
      // `mousedown` (ui/dismiss.ts), a different event, so an open picker in
      // here still closes when the next one is pressed.
      onPointerDown={(e) => e.stopPropagation()}
    >
      {children}
    </div>
  );
}
