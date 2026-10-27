/**
 * The grid that draws a dashboard, and lets it be rearranged.
 *
 * # No drag library, and no absolute positioning
 *
 * Placement is CSS grid with integer cells: a tile is `grid-column: 3 / span 4`,
 * and the browser does every pixel of the arithmetic. Dragging and resizing are
 * pointer events that produce a CELL DELTA (dashboardLayout.cellDelta) and
 * nothing else — the layout module never sees a pixel, and this file never
 * computes a position. The reasoning for choosing cells over free positioning is
 * at the top of dashboardLayout.ts.
 *
 * What that buys, concretely: the same dashboard on a 4K wall display and on a
 * laptop is the same dashboard, reflowed by CSS rather than by us; a tile can
 * never land at a fractional position; and there is no drag library to keep up
 * to date.
 *
 * What it costs, said plainly: a tile cannot be placed "between" two columns.
 *
 * # Under 720px the plane becomes a STACK
 *
 * Twelve columns on a 393px screen is six columns of sixty pixels: every label
 * broke in half and every figure was cut mid-digit ("311 Mb/s" rendered as
 * "31:"). So below the narrow breakpoint the grid stops being a plane and
 * becomes one full-width column, tiles following each other in READING ORDER —
 * the order of the arrangement, top row first and then left to right, which is
 * the only order that is recognisably the same dashboard.
 *
 * Three consequences, each decided here rather than left to CSS:
 *
 *   - **Height in cells stops meaning anything.** A cell is 84px of a plane
 *     that no longer exists; six cells of it is 504px of a phone screen for a
 *     tile that may hold three lines. Stacked, a tile is as tall as its
 *     CONTENT — charts carry their own pixel height, lists their own rows —
 *     with a floor of one cell so an empty tile is still a tile, and a ceiling
 *     of 80vh past which the card scrolls inside itself exactly as it does in
 *     a desktop cell. Without the ceiling, `table.event-log` alone would be
 *     the whole page.
 *   - **Nothing is clipped.** `.dash-grid-tile-clip` exists to keep content
 *     inside a fixed cell; with no fixed cell there is nothing to keep it
 *     inside, so stacked it stops clipping and lets the tile grow.
 *   - **Edit mode loses its spatial half** — see `stacked` below.
 *
 * The stack is driven by a CLASS set from `useBreakpoint`, not by a media
 * query, and deliberately: the reading order has to be computed in JS anyway
 * (DOM order is the array order, which is not the arrangement's order), and
 * two mechanisms reading the viewport would disagree by a scrollbar width at
 * exactly 720px — the one place a disagreement is guaranteed to be seen.
 *
 * # Editing is a MODE
 *
 * Outside edit mode the tiles are live and interactive: the traffic chips work,
 * the fleet table links to devices. Inside it, tile content stops receiving
 * pointer events and the whole tile becomes a drag target. That is the only way
 * both can be true at once — a tile that was both draggable and clickable would
 * make every click on a chart a one-pixel drag, and a click on a device link
 * would leave the page with the arrangement unsaved.
 *
 * ONE DECLARED EXCEPTION, AND ONLY ONE — `dashboardTileEdit.tsx`, ADR-0032. A
 * tile may wrap its edit-mode SETTINGS in `TileSettings`, which takes its
 * pointer events back, swallows the drag, and is skipped by the keyboard
 * handler below. Settings, never content: the curve above the panel stays as
 * inert as before, so the reason the rule exists is not reopened.
 *
 * Stacked, that trade has no reason to exist, because there is no drag: moving
 * and resizing are withdrawn and the content stays live. What a drag would
 * produce is a cell delta against a twelve-column plane the screen is not
 * showing, and on touch the drag gesture IS the scroll gesture. Removing a
 * tile, adding one, renaming — none of those are spatial — all stay.
 *
 * REORDERING STAYS OFF, and that is the one that deserves its reason written
 * down. The stack order is DERIVED from the arrangement; it is not stored.
 * "Move this tile up" would therefore have to rewrite x and y in the plane,
 * and since tiles have different widths there is no swap that leaves the
 * desktop layout recognisable — a thumb on a phone would silently reshape the
 * wall display the arrangement was drawn for. The phone reads the dashboard;
 * the plane is arranged where the plane is visible.
 */
import { useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";

import { ActionButton } from "./ActionButton";
import { Field, Modal } from "./components";
import { Form } from "./ui/Form";
import { IconAction } from "./ui/IconAction";
import { useBreakpoint } from "./ui/breakpoint";

import {
  GRID_COLUMNS,
  MAX_TILE_TITLE,
  cellDelta,
  gridRows,
  moveTile,
  removeTile,
  resizeTile,
  tileTitle,
  withTileTitle,
  type GridMetrics,
  type Tile,
} from "./dashboardLayout";
import { TILES, type TileData } from "./dashboardTiles";
import { inLiveTileRegion } from "./dashboardTileEdit";

/** Row height and gap, in pixels. The only two magic numbers in the file. */
const ROW_HEIGHT = 84;
const GRID_GAP = 12;

interface DragState {
  index: number;
  mode: "move" | "resize";
  startX: number;
  startY: number;
  /** The tile's geometry when the gesture began — deltas apply to THIS, not to
   *  the running preview, or a slow drag would accumulate rounding. */
  origin: Tile;
}

export interface DashboardGridProps {
  tiles: Tile[];
  data: TileData;
  siteSlug: string;
  publicView: boolean;
  /** Edit mode: tiles become draggable, resizable and removable. */
  editing: boolean;
  /** Called with the new layout after any change. Absent = read-only. */
  onChange?: (tiles: Tile[]) => void;
}

export default function DashboardGrid({
  tiles,
  data,
  siteSlug,
  publicView,
  editing,
  onChange,
}: DashboardGridProps) {
  const { t } = useTranslation();
  const container = useRef<HTMLDivElement>(null);
  const drag = useRef<DragState | null>(null);
  // Below the narrow breakpoint the plane is a stack — see the file header.
  const stacked = useBreakpoint() === "narrow";
  // Preview during a gesture. Kept out of the committed layout so an
  // interrupted drag (pointer cancelled by the browser, a tab switch) leaves
  // the saved arrangement exactly as it was.
  const [preview, setPreview] = useState<Tile[] | null>(null);
  // Which tile is being renamed, as an index into the caller's array, and the
  // draft. `null` closes the dialog — not an empty draft, which is a legitimate
  // value meaning "give this tile its default name back".
  const [renaming, setRenaming] = useState<number | null>(null);
  const [draft, setDraft] = useState("");

  const shown = preview ?? tiles;
  const rows = Math.max(gridRows(shown), 1);

  // Moving and resizing are plane gestures; stacked there is no plane. Removal,
  // and the per-tile options a tile routes through `setOption`, are not.
  const canArrange = editing && !!onChange && !stacked;
  // What a TILE calls edit mode: the mode, plus somewhere for its settings to
  // be saved. True stacked as well, where there is no drag to protect against
  // and the content is live anyway — the panel works there for free.
  const tileEditing = editing && !!onChange;

  /**
   * The order tiles are rendered in — indices into `shown`, so every callback
   * still addresses the tile by its position in the caller's array.
   *
   * On the plane this is the identity: explicit `grid-column` / `grid-row`
   * place each tile wherever the arrangement says, and DOM order is invisible.
   * Stacked, DOM order IS the layout, and the array order is the order tiles
   * were added — which, for a dashboard that has been rearranged even once, is
   * not the order anyone sees. Reading order (top row first, then left to
   * right) is what the stack has to follow for it to be the same dashboard.
   */
  const order = useMemo(() => {
    const idx = shown.map((_, i) => i);
    if (!stacked) return idx;
    return idx.sort(
      (a, b) => shown[a].y - shown[b].y || shown[a].x - shown[b].x || a - b,
    );
  }, [shown, stacked]);

  function metrics(): GridMetrics | null {
    const el = container.current;
    if (!el) return null;
    const box = el.getBoundingClientRect();
    return {
      left: box.left,
      top: box.top,
      width: box.width,
      rowHeight: ROW_HEIGHT,
      gap: GRID_GAP,
    };
  }

  function startGesture(
    e: React.PointerEvent,
    index: number,
    mode: "move" | "resize",
  ) {
    if (!canArrange) return;
    e.preventDefault();
    e.stopPropagation();
    (e.currentTarget as HTMLElement).setPointerCapture(e.pointerId);
    drag.current = {
      index,
      mode,
      startX: e.clientX,
      startY: e.clientY,
      origin: tiles[index],
    };
  }

  function onPointerMove(e: React.PointerEvent) {
    const g = drag.current;
    const m = metrics();
    if (!g || !m) return;
    const d = cellDelta(m, g.startX, g.startY, e.clientX, e.clientY);
    if (g.mode === "move") {
      setPreview(moveTile(tiles, g.index, g.origin.x + d.dx, g.origin.y + d.dy));
    } else {
      setPreview(resizeTile(tiles, g.index, g.origin.w + d.dx, g.origin.h + d.dy));
    }
  }

  function endGesture() {
    const g = drag.current;
    drag.current = null;
    if (!g) return;
    // Commit whatever the preview showed. Committing a recomputed layout
    // instead would risk showing one thing and saving another.
    if (preview && onChange) onChange(preview);
    setPreview(null);
  }

  /**
   * Keyboard editing, because a grid that can only be arranged with a mouse
   * cannot be arranged by everyone. Arrows move by one cell, Shift+arrows
   * resize, Delete removes — the same operations, through the same layout
   * functions, so they cannot behave differently from the pointer.
   *
   * Stacked, the arrows go quiet for the same reason the drag does: they move
   * a tile through a plane nobody is looking at. Delete stays, because a tile
   * is still a tile.
   *
   * THE GRID YIELDS THE WHOLE KEYBOARD INSIDE A LIVE REGION (ADR-0032). All
   * three bindings collide with what a form field means by the same key, and
   * Delete collides destructively: correcting a typo in a picker's search box
   * would remove the tile being configured. Checked on the event's TARGET, so
   * it covers a picker's trigger and its option rows — both button elements,
   * which `focusInEditable()` would have waved through.
   */
  function onKeyDown(e: React.KeyboardEvent, index: number) {
    if (!editing || !onChange) return;
    if (inLiveTileRegion(e.target)) return;
    if (!canArrange) {
      if (e.key === "Delete" || e.key === "Backspace") {
        e.preventDefault();
        onChange(removeTile(tiles, index));
      }
      return;
    }
    const cur = tiles[index];
    const step = (dx: number, dy: number) => {
      e.preventDefault();
      if (e.shiftKey) onChange(resizeTile(tiles, index, cur.w + dx, cur.h + dy));
      else onChange(moveTile(tiles, index, cur.x + dx, cur.y + dy));
    };
    if (e.key === "ArrowLeft") step(-1, 0);
    else if (e.key === "ArrowRight") step(1, 0);
    else if (e.key === "ArrowUp") step(0, -1);
    else if (e.key === "ArrowDown") step(0, 1);
    else if (e.key === "Delete" || e.key === "Backspace") {
      e.preventDefault();
      onChange(removeTile(tiles, index));
    }
  }

  function setOption(index: number, patch: Record<string, unknown>) {
    if (!onChange) return;
    const next = tiles.slice();
    next[index] = { ...next[index], options: { ...next[index].options, ...patch } };
    onChange(next);
  }

  /**
   * The name to draw in a tile's header: the operator's own, or the
   * catalogue's translated label.
   *
   * Resolved HERE, once, for the same reason `needs` is declared once — the
   * eighteen components would otherwise each decide, and the nineteenth would
   * forget. A custom name is data and is printed as it stands; the default is
   * a key and follows the interface language (ADR-0027).
   */
  function titleOf(tile: Tile): string {
    const own = tileTitle(tile);
    if (own) return own;
    const def = TILES[tile.type];
    return def ? (t(def.labelKey) as string) : tile.type;
  }

  /** Commit a rename. An empty draft restores the default — see withTileTitle. */
  function commitRename() {
    const index = renaming;
    setRenaming(null);
    if (index == null || !onChange || index >= tiles.length) return;
    const next = tiles.slice();
    next[index] = withTileTitle(next[index], draft);
    // Through the layout, not through a call of its own: a title rides with the
    // arrangement, so it is saved by the Save button the operator already uses
    // after moving a tile. One pending change, one gesture to commit it.
    onChange(next);
  }

  if (shown.length === 0) {
    return <div className="empty">{t("dashboards.emptyDashboard")}</div>;
  }

  // The plane itself, named rather than inlined into the return: the rename
  // dialog is a sibling of it, not a child (see below), and wrapping a
  // hundred-line JSX block in a fragment just to add that sibling would
  // re-indent all of it for nothing.
  const canvas = (
    <div
      ref={container}
      className={"dash-grid-canvas" + (stacked ? " dash-grid-canvas--stacked" : "")}
      onPointerMove={onPointerMove}
      onPointerUp={endGesture}
      onPointerCancel={endGesture}
      // The ONLY inline style left in this file, and it carries numbers rather
      // than rules: the row count the arrangement needs, and the two constants
      // the pointer arithmetic above also reads. Declaring the row height in
      // CSS and again here is how a drag lands one cell off.
      style={
        {
          "--dg-cols": GRID_COLUMNS,
          "--dg-rows": rows,
          "--dg-row-h": `${ROW_HEIGHT}px`,
          "--dg-gap": `${GRID_GAP}px`,
        } as React.CSSProperties
      }
    >
      {order.map((index) => {
        const tile = shown[index];
        const def = TILES[tile.type];
        return (
          <div
            key={index}
            className={
              "dash-grid-tile" +
              (editing ? " dash-grid-tile--editing" : "") +
              (canArrange ? " dash-grid-tile--arrangeable" : "") +
              (drag.current?.index === index ? " dash-grid-tile--dragging" : "")
            }
            tabIndex={editing ? 0 : -1}
            onKeyDown={(e) => onKeyDown(e, index)}
            onPointerDown={(e) => startGesture(e, index, "move")}
            style={
              {
                "--dg-x": tile.x + 1,
                "--dg-y": tile.y + 1,
                "--dg-w": tile.w,
                "--dg-h": tile.h,
              } as React.CSSProperties
            }
          >
            {/* Content. In edit mode on the plane it stops receiving pointer
                events so the whole tile is a drag target: a chart that was both
                draggable and clickable would turn every click into a one-pixel
                drag. Stacked there is no drag, so it stays live.

                `editing` is handed DOWN so a tile can show a settings panel
                there and nowhere else (ADR-0032) — and it is the writable
                edit mode, not the raw flag: with no `onChange` a panel would
                offer settings that go nowhere. */}
            <div className="dash-grid-tile-clip">
              {def ? (
                def.framed ? (
                  <div className="card">
                    <def.Component
                      data={data}
                      heading={titleOf(tile)}
                      siteSlug={siteSlug}
                      publicView={publicView}
                      editing={tileEditing}
                      options={tile.options ?? {}}
                      setOption={(patch) => setOption(index, patch)}
                    />
                  </div>
                ) : (
                  <def.Component
                    data={data}
                    heading={titleOf(tile)}
                    siteSlug={siteSlug}
                    publicView={publicView}
                    editing={tileEditing}
                    options={tile.options ?? {}}
                    setOption={(patch) => setOption(index, patch)}
                  />
                )
              ) : (
                // A tile type this build does not know: a dashboard saved by a
                // newer controller. Say so where the tile is, rather than
                // dropping it silently — a hole in a layout with no explanation
                // reads as data loss.
                <div className="card">
                  <div className="empty">
                    {t("dashboards.unknownTile", { type: tile.type })}
                  </div>
                </div>
              )}
            </div>

            {/* Renaming and removing are not plane gestures — the tile acted
                on is the tile touched, stacked or not — so both survive into
                the stack where the drag and the resize handle do not.

                Both swallow `pointerdown`: without it the first press of
                either would start a drag of the tile underneath, and the
                click that followed would land on a tile that had moved. */}
            {editing && (
              <div className="dash-grid-tile-tools">
                <IconAction
                  kind="rename"
                  className="secondary"
                  label={t("dashboards.renameTile") as string}
                  onPointerDown={(e) => e.stopPropagation()}
                  onClick={() => {
                    // The field opens on the CUSTOM title, and empty when
                    // there is none — never on the translated default. Seeding
                    // it with the default would make a blind Save freeze the
                    // tile in whatever language the operator happened to be
                    // reading (ADR-0027); the default goes in the placeholder,
                    // where it informs without being committed.
                    setDraft(tileTitle(tile));
                    setRenaming(index);
                  }}
                />
                <IconAction
                  kind="close"
                  className="secondary"
                  label={t("dashboards.removeTile") as string}
                  onPointerDown={(e) => e.stopPropagation()}
                  onClick={() => onChange && onChange(removeTile(tiles, index))}
                />
              </div>
            )}
            {/* Resize handle, bottom-right. A corner and not an edge: one
                handle that changes both dimensions is one gesture to learn,
                and the layout module clamps whatever comes out of it. */}
            {canArrange && (
              <div
                role="presentation"
                className="dash-grid-tile-resize"
                onPointerDown={(e) => startGesture(e, index, "resize")}
                title={t("dashboards.resizeTile") as string}
              />
            )}
          </div>
        );
      })}
    </div>
  );

  return (
    <>
      {canvas}
      {/* The dialog lives OUTSIDE the canvas, deliberately. Inside a tile it
          would be a child of a div that swallows pointer events in edit mode
          and clips its content to one grid cell; inside the canvas it would be
          an item of a twelve-column grid. It belongs to the screen, not to the
          plane. */}
      {renaming != null && tiles[renaming] && (
        <Modal
          title={t("dashboards.renameTile") as string}
          onClose={() => setRenaming(null)}
          help={<p>{t("dashboards.renameTileHelp")}</p>}
        >
          <Form onSubmit={commitRename}>
            <Field label={t("dashboards.tileTitle") as string}>
              <input
                autoFocus
                value={draft}
                maxLength={MAX_TILE_TITLE}
                // The default label, shown but NOT typed in. A placeholder says
                // "this is what you get if you leave it empty", which is
                // exactly the rule — an empty title restores the default.
                placeholder={titleOf({ ...tiles[renaming], options: {} })}
                onChange={(e) => setDraft(e.target.value)}
              />
            </Field>
            <div className="dash-rename-actions">
              <ActionButton
                kind="cancel"
                className="secondary"
                type="button"
                onClick={() => setRenaming(null)}
              >
                {t("common.cancel")}
              </ActionButton>
              {/* Never disabled on an empty field: empty is a VALID answer
                  here — it is how the default name comes back — and a greyed
                  Save would make clearing the title look forbidden. */}
              <ActionButton kind="save" type="submit">
                {t("common.save")}
              </ActionButton>
            </div>
          </Form>
        </Modal>
      )}
    </>
  );
}
