// Global “one hover overlay at a time” rule for the whole app.
//
// Topology (and dense pages) mount many QuickTips / floating tips. Without a
// single owner, bubbles stack and look shifted. Whoever claims last wins;
// the previous owner is told to dismiss.

type Dismiss = () => void;

var activeId: string | null = null;
var dismissers = new Map<string, Dismiss>();

/** Register a dismiss callback for this tip id (call from mount). */
export function registerHoverTip(id: string, dismiss: Dismiss): () => void {
  dismissers.set(id, dismiss);
  return () => {
    dismissers.delete(id);
    releaseHoverTip(id);
  };
}

/** Claim exclusive ownership — previous tip is dismissed. */
export function claimHoverTip(id: string): void {
  if (activeId === id) return;
  var prev = activeId;
  activeId = id;
  if (prev && prev !== id) {
    var d = dismissers.get(prev);
    if (d) d();
  }
}

/** Release if this id still owns the slot. */
export function releaseHoverTip(id: string): void {
  if (activeId === id) activeId = null;
}

export function activeHoverTipId(): string | null {
  return activeId;
}
