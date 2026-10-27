/**
 * Swipe navigation — the event half. ADR-0009.
 *
 * THE RECOGNISER IS PURELY OBSERVATIONAL: it never calls `preventDefault()`
 * and never sets `touch-action`. That is what makes it safe to layer over an
 * interface whose horizontal scrollers ARE the content (the faceplate, the
 * 860px ports table) — a gesture it reads wrong still does exactly what the
 * browser would have done with it.
 *
 * Touch only. A trackpad's two-finger horizontal gesture arrives as `wheel`
 * with `deltaX`, bit-for-bit the same event as scrolling a wide table, so no
 * `wheel` is listened to at all.
 *
 * No keyboard binding lives here: the tab bar and the sidebar are real
 * focusable links and they remain the keyboard route. The swipe is additive.
 */

import { useEffect, useRef, useState } from "react";
import {
  AXIS_LOCK_PX,
  FLICK_WINDOW_MS,
  axisLock,
  horizontalClaimed,
  scrollEdges,
  swipeArmed,
  swipeDirection,
  swipeReleased,
  swipeThreshold,
  verticalAllows,
  type SwipeAxis,
  type SwipeDir,
} from "./swipeDecide";

/** Where the cue sits — the edge the destination lies towards. */
export type CueEdge = "start" | "end" | "top" | "bottom";

export type SwipeCue = { axis: SwipeAxis; dir: SwipeDir; label: string; edge: CueEdge };

export type SwipeNavOptions = {
  /** The element gestures are recognised on — the page content, never the chrome. */
  rootRef: React.RefObject<HTMLElement | null>;
  /** Minimum deliberate travel, from the breakpoint (see swipeThreshold). */
  floorPx: number;
  /** The destination's label, or null when there is nothing that way. */
  destination: (axis: SwipeAxis, dir: SwipeDir) => string | null;
  /** Commit. Called once, on release. */
  go: (axis: SwipeAxis, dir: SwipeDir) => void;
};

function scrollsOnAxis(el: HTMLElement, axis: SwipeAxis): boolean {
  const room =
    axis === "x" ? el.scrollWidth - el.clientWidth : el.scrollHeight - el.clientHeight;
  if (room <= 2) return false;
  const style = getComputedStyle(el);
  const ov = axis === "x" ? style.overflowX : style.overflowY;
  return ov === "auto" || ov === "scroll" || ov === "overlay";
}

/** Ancestors from the touched node up to and including the root. */
function chain(from: EventTarget | null, root: HTMLElement): HTMLElement[] {
  const out: HTMLElement[] = [];
  let el: HTMLElement | null =
    from instanceof HTMLElement ? from : from instanceof Node ? (from.parentElement as HTMLElement | null) : null;
  while (el) {
    out.push(el);
    if (el === root) break;
    el = el.parentElement;
  }
  return out;
}

function edgeFor(axis: SwipeAxis, dir: SwipeDir): CueEdge {
  if (axis === "x") return dir === "prev" ? "start" : "end";
  return dir === "prev" ? "top" : "bottom";
}

type Sample = { x: number; y: number; t: number };

type Live = {
  pointerId: number;
  start: Sample;
  samples: Sample[];
  /** Locked axis, or null while still under AXIS_LOCK_PX. */
  axis: SwipeAxis | null;
  dead: boolean;
  /** A sideways scroller owned the horizontal axis from the first pixel. */
  xClaimed: boolean;
  /** Vertical edges of the nearest vertical scroller, read at pointerdown. */
  atTop: boolean;
  atBottom: boolean;
};

/**
 * A navigation that happened on a link the finger started on would otherwise
 * be followed by that link's own click. The gesture wins; the click is
 * swallowed once, in the capture phase.
 *
 * It used to be exported, because the mode selector had the same problem in a
 * sharper form: its knob WAS a link — the way back to the dashboard — and a
 * drag that moved it must not also navigate home. ADR-0031 removed both the
 * drag and the link, so the page swipe is the only client again.
 */
function swallowNextClick() {
  const kill = (e: MouseEvent) => {
    e.preventDefault();
    e.stopPropagation();
    done();
  };
  const done = () => {
    window.removeEventListener("click", kill, true);
    window.clearTimeout(timer);
  };
  const timer = window.setTimeout(done, 500);
  window.addEventListener("click", kill, true);
}

/**
 * Recognises navigation swipes on `rootRef` and returns the live cue — the
 * glass pill naming where releasing will go, or null.
 */
export function useSwipeNav(opts: SwipeNavOptions): SwipeCue | null {
  const [cue, setCue] = useState<SwipeCue | null>(null);
  // Refs so the listeners are installed once per root and never re-bound as
  // the route (and therefore `destination`/`go`) changes under them.
  const cfg = useRef(opts);
  cfg.current = opts;
  const rootRef = opts.rootRef;

  useEffect(() => {
    const root = rootRef.current;
    if (!root) return;
    let live: Live | null = null;
    let shown: SwipeCue | null = null;

    /**
     * The cue is React state and Layout renders the whole page under it, so a
     * naive setCue on every pointermove would re-render the page sixty times
     * a second during a gesture — on a 48-port faceplate, exactly the screen
     * that can least afford it. It changes at most twice per gesture, so
     * only an actual CHANGE is allowed through.
     */
    const show = (next: SwipeCue | null) => {
      const same =
        shown === next ||
        (!!shown &&
          !!next &&
          shown.axis === next.axis &&
          shown.dir === next.dir &&
          shown.edge === next.edge &&
          shown.label === next.label);
      if (same) return;
      shown = next;
      setCue(next);
    };

    const clear = () => {
      live = null;
      show(null);
    };

    const onDown = (e: PointerEvent) => {
      if (e.pointerType !== "touch") return;
      if (live) {
        // A second finger: a pinch or a two-finger scroll, never a navigation.
        live.dead = true;
        show(null);
        return;
      }
      // The one layout read of the whole gesture: scrollWidth/scrollHeight
      // and a computed style per ancestor, at pointerdown, never per frame.
      const ancestors = chain(e.target, root);
      const yScroller = ancestors.find((el) => scrollsOnAxis(el, "y"));
      const edges = yScroller
        ? scrollEdges(yScroller.scrollTop, yScroller.scrollHeight, yScroller.clientHeight)
        : { atTop: true, atBottom: true };
      live = {
        pointerId: e.pointerId,
        start: { x: e.clientX, y: e.clientY, t: e.timeStamp },
        samples: [],
        axis: null,
        dead: false,
        xClaimed: horizontalClaimed(ancestors.map((el) => scrollsOnAxis(el, "x"))),
        atTop: edges.atTop,
        atBottom: edges.atBottom,
      };
    };

    const axisSize = (axis: SwipeAxis) =>
      axis === "x" ? root.clientWidth : root.clientHeight;

    const onMove = (e: PointerEvent) => {
      if (!live || e.pointerId !== live.pointerId || live.dead) return;
      const dx = e.clientX - live.start.x;
      const dy = e.clientY - live.start.y;
      live.samples.push({ x: e.clientX, y: e.clientY, t: e.timeStamp });
      if (live.samples.length > 12) live.samples.shift();

      if (!live.axis) {
        if (Math.max(Math.abs(dx), Math.abs(dy)) < AXIS_LOCK_PX) return;
        // Try the axis the movement is actually on, once, and never again.
        const want: SwipeAxis = Math.abs(dx) >= Math.abs(dy) ? "x" : "y";
        if (axisLock(want, dx, dy) !== "locked") {
          live.dead = true;
          return;
        }
        if (want === "x" && live.xClaimed) {
          live.dead = true;
          return;
        }
        live.axis = want;
      }

      const axis = live.axis;
      const along = axis === "x" ? dx : dy;
      const dir = swipeDirection(along);
      if (axis === "y" && !verticalAllows(dir, live.atTop, live.atBottom)) {
        show(null);
        return;
      }
      const label = cfg.current.destination(axis, dir);
      if (!label) {
        show(null);
        return;
      }
      const armed = swipeArmed(along, swipeThreshold(axisSize(axis), cfg.current.floorPx));
      show(armed ? { axis, dir, label, edge: edgeFor(axis, dir) } : null);
    };

    const onUp = (e: PointerEvent) => {
      if (!live || e.pointerId !== live.pointerId) return;
      const g = live;
      clear();
      if (g.dead || !g.axis) return;
      const dx = e.clientX - g.start.x;
      const dy = e.clientY - g.start.y;
      const axis = g.axis;
      const along = axis === "x" ? dx : dy;
      const dir = swipeDirection(along);
      if (axis === "y" && !verticalAllows(dir, g.atTop, g.atBottom)) return;
      if (!cfg.current.destination(axis, dir)) return;

      // The tail: the oldest sample still inside the velocity window.
      const now = e.timeStamp;
      const ref =
        g.samples.find((s) => now - s.t <= FLICK_WINDOW_MS) ??
        g.samples[g.samples.length - 1] ??
        g.start;
      const tail = {
        along: axis === "x" ? e.clientX - ref.x : e.clientY - ref.y,
        ms: now - ref.t,
      };
      if (!swipeReleased(along, swipeThreshold(axisSize(axis), cfg.current.floorPx), tail))
        return;
      swallowNextClick();
      cfg.current.go(axis, dir);
    };

    root.addEventListener("pointerdown", onDown);
    // move/up on the window: a gesture that leaves the content box mid-flight
    // must still be seen through to its release.
    window.addEventListener("pointermove", onMove);
    window.addEventListener("pointerup", onUp);
    window.addEventListener("pointercancel", clear);
    return () => {
      root.removeEventListener("pointerdown", onDown);
      window.removeEventListener("pointermove", onMove);
      window.removeEventListener("pointerup", onUp);
      window.removeEventListener("pointercancel", clear);
    };
  }, [rootRef]);

  return cue;
}
