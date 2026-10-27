// The JS half of the breakpoint convention — UI-GUIDE §9.1. Same two values
// as the CSS comments (/* bp:narrow */, /* bp:mid */); reading
// window.innerWidth ad hoc is the defect this replaces.

import { useSyncExternalStore } from "react";

export const BP_NARROW = 720;
export const BP_MID = 960;

export type Breakpoint = "narrow" | "mid" | "wide";

function current(): Breakpoint {
  if (typeof window === "undefined") return "wide";
  const w = window.innerWidth;
  if (w <= BP_NARROW) return "narrow";
  if (w <= BP_MID) return "mid";
  return "wide";
}

function subscribe(cb: () => void): () => void {
  window.addEventListener("resize", cb);
  return () => window.removeEventListener("resize", cb);
}

/** The active breakpoint, live across resizes. */
export function useBreakpoint(): Breakpoint {
  return useSyncExternalStore(subscribe, current, () => "wide" as Breakpoint);
}
