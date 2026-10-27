/**
 * The arithmetic of swipe navigation — ADR-0009, no DOM, no React.
 *
 * Everything that decides whether a finger movement is a NAVIGATION lives
 * here as plain functions, so the thresholds are testable without a browser
 * (src/swipeNav.test.ts) and so `swipe.ts` is left with nothing but event
 * plumbing. The rules themselves and why they are asymmetric between the two
 * axes are in the ADR; the comments here only say what each number is for.
 */

export type SwipeAxis = "x" | "y";

/**
 * Which way through the ORDERED list of pages, never which way the finger
 * went. A finger travelling in the positive direction (right, or down) moves
 * the page list backwards, the way dragging a sheet of paper does.
 */
export type SwipeDir = "prev" | "next";

/** Movement before the axis is decided. Below this, nothing is a swipe yet. */
export const AXIS_LOCK_PX = 10;

/**
 * How much the dominant axis must beat the other one. A diagonal is a
 * hesitation, not a gesture: 1.7 is roughly 30° either side of the axis.
 */
export const AXIS_DOMINANCE = 1.7;

/** The flick path: short but fast. */
export const FLICK_MIN_PX = 32;
export const FLICK_MIN_SPEED = 0.5; // px per ms
/** Velocity is measured over the tail of the gesture, not over its whole life. */
export const FLICK_WINDOW_MS = 120;

/** The distance path, as a fraction of the axis — see `swipeThreshold`. */
export const AXIS_FRACTION = 0.12;

/** A scroll offset this close to an edge counts as being ON the edge. */
export const EDGE_SLOP_PX = 1;

/**
 * The distance a deliberate drag has to cover: a fraction of the axis, but
 * never less than the floor. The floor comes from the breakpoint — an
 * accidental 70px slide is unremarkable on a tablet and impossible on a
 * phone held in one hand.
 */
export function swipeThreshold(axisSizePx: number, floorPx: number): number {
  return Math.max(floorPx, axisSizePx * AXIS_FRACTION);
}

/** Which way through the page list this travel points. */
export function swipeDirection(along: number): SwipeDir {
  return along > 0 ? "prev" : "next";
}

/**
 * The axis lock. `pending` means keep watching; `lost` means this gesture can
 * never navigate, and the recogniser must stop considering it rather than
 * re-testing a drifting finger until it happens to qualify.
 */
export function axisLock(
  axis: SwipeAxis,
  dx: number,
  dy: number,
): "pending" | "locked" | "lost" {
  const along = axis === "x" ? dx : dy;
  const across = axis === "x" ? dy : dx;
  const a = Math.abs(along);
  const b = Math.abs(across);
  if (Math.max(a, b) < AXIS_LOCK_PX) return "pending";
  return a >= b * AXIS_DOMINANCE ? "locked" : "lost";
}

/**
 * Armed = releasing now would navigate, so the cue is shown. The distance
 * path only; the flick path is a release-time judgement (`swipeReleased`)
 * because a fast short gesture is over before a cue could be read.
 */
export function swipeArmed(along: number, thresholdPx: number): boolean {
  return Math.abs(along) >= thresholdPx;
}

/** Travel over the tail of the gesture, used for velocity. */
export type Tail = { along: number; ms: number };

export function swipeReleased(
  along: number,
  thresholdPx: number,
  tail: Tail,
): boolean {
  if (swipeArmed(along, thresholdPx)) return true;
  if (Math.abs(along) < FLICK_MIN_PX) return false;
  if (tail.ms <= 0) return false;
  // Same sign: a finger that came back towards the start is an abandoned
  // gesture, however fast it travelled.
  if (Math.sign(tail.along) !== Math.sign(along)) return false;
  return Math.abs(tail.along) / tail.ms >= FLICK_MIN_SPEED;
}

/**
 * The horizontal veto, by ANCESTRY: if the gesture began inside anything that
 * can scroll sideways, that element owns the axis for the whole gesture —
 * whatever its scroll position. The faceplate and the wide tables are the
 * reason (ADR-0009, rule 1).
 */
export function horizontalClaimed(scrollableAncestors: boolean[]): boolean {
  return scrollableAncestors.some(Boolean);
}

/**
 * The vertical rule, by POSITION: the nearest vertical scroller must already
 * be against the edge the finger is pushing. Measured at `pointerdown`, so
 * scrolling to the bottom and continuing in one movement does not navigate.
 */
export function verticalAllows(
  dir: SwipeDir,
  atTop: boolean,
  atBottom: boolean,
): boolean {
  return dir === "prev" ? atTop : atBottom;
}

/** `scrollTop`-style reading → the two edge flags, slop included. */
export function scrollEdges(
  offset: number,
  scrollSize: number,
  clientSize: number,
): { atTop: boolean; atBottom: boolean } {
  const max = scrollSize - clientSize;
  if (max <= EDGE_SLOP_PX) return { atTop: true, atBottom: true };
  return {
    atTop: offset <= EDGE_SLOP_PX,
    atBottom: offset >= max - EDGE_SLOP_PX,
  };
}

/**
 * Step through an ordered page list without wrapping. Wrapping would turn the
 * last tab into the first one on a gesture people make by accident, and the
 * tab bar it mirrors has no wrap either.
 */
export function stepIndex(
  index: number,
  dir: SwipeDir,
  length: number,
): number | null {
  if (index < 0) return null;
  const next = dir === "prev" ? index - 1 : index + 1;
  if (next < 0 || next >= length) return null;
  return next;
}
