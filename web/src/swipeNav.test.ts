/**
 * The arithmetic of swipe navigation — run with:
 *   node --experimental-strip-types src/swipeNav.test.ts
 * (from web/), or `npm test` to run every file at once.
 *
 * Written as the CONFUSIONS it prevents (ADR-0009). A navigation gesture laid
 * over this interface has exactly two ways to be harmful, and neither is
 * cosmetic:
 *
 *   - stealing a HORIZONTAL gesture from the faceplate or a 860px ports
 *     table, which does not degrade the screen but removes the only thing it
 *     does;
 *   - firing on a VERTICAL gesture that was a scroll, so a page people read
 *     by flicking upwards walks through the menu under them.
 *
 * Everything below is the fence around those two.
 */
import assert from "node:assert/strict";
// Node --experimental-strip-types needs the .ts suffix; tsc excludes this file.
import {
  AXIS_LOCK_PX,
  axisLock,
  horizontalClaimed,
  scrollEdges,
  stepIndex,
  swipeArmed,
  swipeDirection,
  swipeReleased,
  swipeThreshold,
  verticalAllows,
} from "./ui/swipeDecide.ts";

// --- the threshold ---------------------------------------------------------

// A fraction of the axis, floored. On a 393px phone the fraction (47px) loses
// to the 64px floor; on a 1024px tablet the fraction wins, because a 70px
// accidental slide is unremarkable on a big pane of glass.
assert.equal(swipeThreshold(393, 64), 64);
assert.equal(swipeThreshold(1024, 96), 1024 * 0.12);
// Never below the floor, whatever the viewport claims.
assert.equal(swipeThreshold(0, 64), 64);

// --- the axis lock --------------------------------------------------------

// Nothing is a gesture until something has moved. This is what keeps a tap
// with a pixel of tremor from being read as a flick.
assert.equal(axisLock("x", 4, 2), "pending");
assert.equal(axisLock("x", AXIS_LOCK_PX - 1, 0), "pending");

// A clean horizontal drag locks; the same travel at 45° is a hesitation and
// is LOST, not left pending — a finger left drifting must not eventually
// qualify just because it kept moving.
assert.equal(axisLock("x", 40, 3), "locked");
assert.equal(axisLock("x", 40, 40), "lost");
assert.equal(axisLock("x", 40, 30), "lost"); // 30 * 1.7 = 51 > 40
assert.equal(axisLock("x", 40, 20), "locked"); // 20 * 1.7 = 34 < 40
// Vertical is the same rule with the axes swapped, not a looser one.
assert.equal(axisLock("y", 3, 40), "locked");
assert.equal(axisLock("y", 40, 40), "lost");

// Direction is about the page LIST, not about the finger: dragging right or
// down goes BACK through the menu, the way a sheet of paper moves.
assert.equal(swipeDirection(60), "prev");
assert.equal(swipeDirection(-60), "next");

// --- arming and release ---------------------------------------------------

// Armed means "releasing now navigates", which is exactly when the cue is
// allowed to appear. Below the threshold it must not: a cue that showed up
// early would promise a navigation that is not going to happen.
assert.equal(swipeArmed(63, 64), false);
assert.equal(swipeArmed(-64, 64), true);

// The flick path. 40px is under any threshold, but 40px in 50ms is 0.8px/ms
// and unmistakably a throw.
assert.equal(swipeReleased(-40, 64, { along: -40, ms: 50 }), true);
// The same distance taken slowly is a drag that stopped short — nothing.
assert.equal(swipeReleased(-40, 64, { along: -40, ms: 400 }), false);
// Fast but tiny stays nothing: 20px is inside the slop of a tap on a moving
// train, and this is a product used standing in front of a rack.
assert.equal(swipeReleased(-20, 64, { along: -20, ms: 10 }), false);
// Past the threshold, speed is irrelevant — a deliberate slow drag counts.
assert.equal(swipeReleased(-120, 64, { along: -2, ms: 300 }), true);
// A finger that came BACK towards the start is an abandoned gesture, however
// fast the tail was. This is the cancel that replaces "the page follows the
// finger".
assert.equal(swipeReleased(-40, 64, { along: 40, ms: 50 }), false);
// No time elapsed: no velocity can be computed, so no flick is claimed.
assert.equal(swipeReleased(-40, 64, { along: -40, ms: 0 }), false);

// --- who owns the axis ----------------------------------------------------

// THE HORIZONTAL VETO IS BY ANCESTRY. One sideways scroller anywhere between
// the touched node and the content box takes the axis for the whole gesture,
// whatever its scroll position: the faceplate must behave the same at its
// left edge as in its middle, or the same swipe at the same place does two
// different things depending on invisible state.
assert.equal(horizontalClaimed([false, false, false]), false);
assert.equal(horizontalClaimed([false, true, false]), true);
assert.equal(horizontalClaimed([]), false);

// THE VERTICAL RULE IS BY POSITION, because every page scrolls vertically and
// an ancestry veto would mean the feature never fires. Pushing down needs the
// top; pushing up needs the bottom.
assert.equal(verticalAllows("prev", true, false), true);
assert.equal(verticalAllows("prev", false, false), false);
assert.equal(verticalAllows("next", false, true), true);
assert.equal(verticalAllows("next", false, false), false);

// A page that does not scroll at all is against BOTH edges, so both
// directions are live from the first pixel. Deliberate — a short page under a
// sidebar is the case the vertical gesture was asked for.
assert.deepEqual(scrollEdges(0, 400, 400), { atTop: true, atBottom: true });
assert.deepEqual(scrollEdges(0, 2000, 400), { atTop: true, atBottom: false });
assert.deepEqual(scrollEdges(1600, 2000, 400), { atTop: false, atBottom: true });
assert.deepEqual(scrollEdges(800, 2000, 400), { atTop: false, atBottom: false });
// One pixel of slop at each end: a scroller parked by a previous flick often
// lands at 0.5 or at max - 0.5, and "nearly at the edge" is at the edge.
assert.deepEqual(scrollEdges(1, 2000, 400), { atTop: true, atBottom: false });
assert.deepEqual(scrollEdges(1599, 2000, 400), { atTop: false, atBottom: true });

// --- stepping the list ----------------------------------------------------

// No wrap-around. Clients → Dashboard on an accidental flick would teleport
// across the product, and the tab bar it mirrors has no wrap either.
assert.equal(stepIndex(0, "next", 5), 1);
assert.equal(stepIndex(4, "next", 5), null);
assert.equal(stepIndex(0, "prev", 5), null);
assert.equal(stepIndex(3, "prev", 5), 2);
// An unknown current page (no tab selected — /config is reached by the gear,
// not by a tab) has no neighbours, so the axis is simply inert there.
assert.equal(stepIndex(-1, "next", 5), null);
assert.equal(stepIndex(0, "next", 0), null);

console.log("swipeNav: ok");
