---
Status: Accepted
Date: 2026-10-25
---

# ADR-0032 — A tile may declare a live zone in edit mode

## Context

The dashboard grid (`web/src/dashboardGrid.tsx`) has **two modes**, and
the boundary between them is a one-word rule: in edit mode, a tile's
content **no longer receives pointer events**.

```css
.dash-grid-tile--arrangeable .dash-grid-tile-clip {
  pointer-events: none;
}
```

The reason is good and it must stay written down: the move gesture is a
`pointerdown` on the **whole tile**. If the content stayed live, every
click on a curve, on a device link, on a window chip would
become a one-pixel move — and conversely, every drag start
from the middle of a tile would be swallowed by the control sitting
there. Both cannot be true in the same place at the same time. The
rule is scoped to `--arrangeable`, so when stacked (below 720 px, where there is no
plan and hence no dragging) the content stays live: it is not a
rule about the *mode*, it is a rule about the *gesture*.

A direct consequence, accepted a few hours ago by ADR-0027: the
"Port traffic" tile shows its two pickers **outside** edit mode. That was
mandatory, not aesthetic — a picker that only exists in edit mode is a
picker nobody can open. The component's comment says it
exactly like that.

And it is **the wrong product**. A dashboard one consults displays
measurements, not forms; the "Port traffic" tile today devotes
seventy pixels of every display to two drop-down lists
that are opened once in the tile's lifetime. Three other tiles carry the
same defect on a smaller scale (the window chips of site traffic, the log's
severity filter). The request — "make it a tile setting during
edit mode, but hide the form in plain display" — is the
right ergonomics, and it is **incompatible with the current rule as
written**.

The question is therefore not "where to put a button". It is: **is the grid
allowed to leave part of a tile live while the plan is being arranged,
and if so, which part?**

## Decision

**Inertness remains the default. A tile may declare ONE exception to it, and that
exception is a settings zone.**

The contract is called `dash-tile-live` (`web/src/dashboardTileEdit.tsx`, which
carries the class, the component that applies it and the predicate the grid
queries). A tile that uses it gets three things, and all three are needed —
if one is missing the form is unusable or steals the gesture:

1. **Pointer events come back**, through a
   `pointer-events: auto` on the zone, which locally cancels the ancestor's
   `none`. That is the easy half.
2. **The zone swallows `pointerdown`.** Without this the event bubbles up to the
   tile's root, whose `onPointerDown` starts a move: the click
   opening the picker would shift the tile by one cell. That is exactly
   what the two "rename" and "remove" buttons already do, for the same
   reason — the live zone merely generalises a pattern the grid
   already practised on its own controls.
3. **The grid's keyboard steps back.** In edit mode, the arrows move the
   tile, Shift+arrows resize it and `Delete` **deletes** it. In a
   search field, those three keys mean moving the cursor,
   selecting, and erasing a character. `Delete` is the serious case: typing then
   correcting in a picker's search field **would erase the tile**.
   The grid therefore yields any key whose target is inside a live
   zone.

Three rules bound this right, and they are the decision as much as the
mechanism:

- **A live zone carries settings, never content.** It only exists
  in edit mode and disappears in display. That is what makes the exception
  safe: we do not reopen the door to "click on a curve = one-pixel
  move", because the curve is not in the zone.
- **Only one per tile, at the top of the body.** Not on the right edge nor in
  the bottom-right corner, which belong to the resize handle and to the
  tool pair. A zone covering the
  handle would remove mouse resizing, and it would be silent.
- **It stays clipped by the cell.** A live zone is tile
  content; `overflow: hidden` keeps applying. A drop-down list taller
  than what remains of the cell is cut off — the card scrolls, as it
  already does for a table that is too long. We do not make a tile overflow
  its slot for a form.

## Consequences

- **The setting is visible while being set.** That is the gain that made
  this solution preferable to a dialog: the zone is above the
  chart, in the same tile, so inverting IN/OUT or changing the window
  redraws the curve before the operator's eyes. A setting whose effect cannot
  be seen is a setting one applies twice to understand it.
- **`Delete` no longer destroys a tile while typing.** It is a bug that
  did not exist yet — nothing was focusable in edit mode — and that would have
  arrived with the first field. The guard is placed in the grid, once,
  for all present and future tiles.
- **The three other tiles with settings can follow, and that is not done
  here.** The site traffic window chips and the log's severity filter
  remain visible outside edit mode. Moving them is now possible and
  remains a per-tile choice: the site traffic window is a reading
  changed several times per visit, not a setting set once.
  **The rule does not say "every setting goes into edit mode"**, it says "a setting
  *may* go there".
- **Edit mode gains content, hence height.** The live zone
  takes room in an already sized cell. The tile's chart
  therefore becomes elastic rather than fixed-height, otherwise the zone
  pushes it out of the slot. It is an independent improvement, which this
  decision makes mandatory.
- **Nothing to migrate, nothing on the wire.** Settings travel in
  `dashboard_tiles.options` through the existing route (ADR-0027), and a live
  zone is a CSS class plus a keyboard guard. An old dashboard
  displays identically, simply without its forms.
- **Escape still belongs to the layer.** A picker opened in a live
  zone registers in the `useDismissable` stack: Escape closes the
  picker, not edit mode. No new rule, but it is the first
  time a layer opens *inside* a tile in edit mode, and it must be
  verified rather than assumed.

## Rejected alternatives

**Keeping the pickers outside edit mode, as in ADR-0027.** The status quo, and it has
the merit of being already written and already tested. Rejected because it makes
**every consultation** pay the price of a gesture made once: on a screen
wall, the "Port traffic" tile permanently shows two lists that
nobody will open again. A dashboard that displays its forms is a
dashboard that cannot be shown.

**A configuration `Modal`, opened from the tile's tool pair.**
It was the easiest solution and it touches no rule: the rename dialog
already does exactly that, outside the plan, outside the clipping, without
any gesture conflict — and it would have solved the clipping of drop-down
lists along the way. Rejected for two reasons. First, **the effect of the setting disappears
while it is being set**: the dialog covers the tile, so "Invert
IN/OUT" and the window change are judged from memory, after closing.
Second, it **creates a second place where a tile's settings live**:
renaming in one dialog, the target in another, the window chips
in the tile — three conventions for the same mode. The live zone creates
no new one, it generalises that of the tile tools.

**Removing `pointer-events: none` outright.** The naive reading of
the request, and the one that must be named in order to reject it: the rule does not exist
only for dragging. It also prevents a click on a device
link in the fleet table from **leaving the page** in the middle of a
rearrangement, with an unsaved layout. A tile's content
is not inert out of timidity.

**A movement threshold in pixels** — a `pointerdown` only becomes a
drag beyond four or five pixels, below that it is a click that
goes to the control. It is the solution drag-and-drop libraries
use, it is honest, and it would make *all* content live in
edit mode. Rejected for three cumulative reasons: it requires a gesture state
machine where there are today three stateless lines; it does not protect
against the page departure described just above, since a short click would remain a
click; and it steals the first pixels of any scroll inside a tile,
hence precisely the gesture made in a drop-down list. We would pay
general complexity to get less than the declared zone.

**Letting the tile overflow while a picker is open**
(conditional `overflow: visible`, via `:has()`, plus a `z-index` on the
tile). Tempting, because the only real remaining defect is a list cut by
the slot. Rejected as a door not to open: the plan's discipline rests
entirely on a tile never overflowing its cell, and an exception
"only when a layer is open" is an exception the next
tile will extend. The card already scrolls; it is the behaviour a table that is too
long has always had.
