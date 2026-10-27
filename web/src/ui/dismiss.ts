// The dismissal stack — UI-GUIDE §8.1.
//
// Escape closes the TOPMOST dismissable layer, and only it; outside-click
// follows the same stack. This module owns the document listeners, the
// ordering, and the busy guard, so no component ever registers a raw keydown
// listener again — the seven historical hand-rolled sites (three different
// targets, one false comment) are what this replaces.

import { useEffect, useRef } from "react";

/**
 * How a layer was dismissed. Most layers close the same way either way; a
 * free-text field does NOT — Escape abandons the draft, while clicking
 * elsewhere means "moving on" and commits it (the blur-commit contract the
 * pill fields have always had). The stack tells; the layer decides.
 */
export type DismissReason = "escape" | "outside";

type Layer = {
  id: number;
  onClose: (reason: DismissReason) => void;
  busy: () => boolean;
  contains: (node: Node) => boolean;
};

const stack: Layer[] = [];
let nextID = 1;
let installed = false;

function top(): Layer | undefined {
  return stack[stack.length - 1];
}

function install() {
  if (installed) return;
  installed = true;
  // Keydown in CAPTURE phase: a layer must win over screen-level shortcuts
  // registered on the same document.
  document.addEventListener(
    "keydown",
    (e) => {
      if (e.key !== "Escape") return;
      const layer = top();
      if (!layer) return;
      e.stopPropagation();
      e.preventDefault();
      if (!layer.busy()) layer.onClose("escape");
    },
    true,
  );
  // mousedown, not click: a text selection that ENDS outside the panel is a
  // click outside but not a mousedown outside — the drag-safe rule the
  // console overlay proved and the old Modal got wrong.
  document.addEventListener("mousedown", (e) => {
    const layer = top();
    if (!layer) return;
    if (e.target instanceof Node && layer.contains(e.target)) return;
    if (!layer.busy()) layer.onClose("outside");
  });
}

/**
 * Registers a dismissable layer while `active`. `insideRef` bounds the layer:
 * a mousedown outside it dismisses (omit it to opt out of outside-click and
 * keep only Escape). `busy` refuses dismissal while a write is in flight.
 */
export function useDismissable(
  active: boolean,
  onClose: (reason: DismissReason) => void,
  opts?: {
    insideRef?: React.RefObject<HTMLElement | null>;
    busy?: boolean;
  },
) {
  // Refs so the layer entry stays stable while callbacks/props move.
  const closeRef = useRef(onClose);
  closeRef.current = onClose;
  const busyRef = useRef(!!opts?.busy);
  busyRef.current = !!opts?.busy;
  const insideRef = opts?.insideRef;

  useEffect(() => {
    if (!active) return;
    install();
    const layer: Layer = {
      id: nextID++,
      onClose: (reason) => closeRef.current(reason),
      busy: () => busyRef.current,
      contains: (node) =>
        insideRef?.current ? insideRef.current.contains(node) : true,
    };
    stack.push(layer);
    return () => {
      const i = stack.indexOf(layer);
      if (i >= 0) stack.splice(i, 1);
    };
  }, [active, insideRef]);
}

/**
 * Whether focus sits in an editable element — the guard every SCREEN-level
 * shortcut needs (UI-GUIDE §8.4), owned here once instead of per-screen
 * tagName lists.
 */
export function focusInEditable(): boolean {
  const el = document.activeElement;
  if (!el || !(el instanceof HTMLElement)) return false;
  if (el.isContentEditable) return true;
  const tag = el.tagName;
  return tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT";
}

/**
 * A SCREEN-level shortcut — UI-GUIDE §8.4. Fires only when no dismissable
 * layer is open (Escape belongs to the layer), focus is not in an editable
 * element, and no HelpTip bubble is up (a transitional check by class until
 * HelpTip joins the stack). The one lawful way to bind a key at page scope.
 */
export function useGlobalKey(key: string, handler: () => void) {
  const ref = useRef(handler);
  ref.current = handler;
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== key) return;
      if (stack.length > 0) return;
      if (focusInEditable()) return;
      if (document.querySelector(".helptip-btn.on")) return;
      ref.current();
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [key]);
}
