// The headless picker — the ONE implementation behind every dropdown,
// combobox and autocomplete in this UI.
//
// Six components used to re-implement the same five things with six sets of
// small differences: open state, an outside-click listener, a cursor index,
// the ↑/↓/Home/End/Enter vocabulary, and Escape — three of them with ARIA,
// three without, two restoring focus, four not. This hook owns the state and
// the vocabulary; the components keep owning their MARKUP, which is the part
// that genuinely differs (a flag button, a filtered list, a chip row).
//
// Escape and outside-click are NOT handled here by hand: the picker registers
// on the dismissal stack (useDismissable) while open, which is what makes a
// picker inside a modal close BEFORE the modal — the layering rule of
// UI-GUIDE §8.1, for free. The third way out — focus LEAVING the box (Tab
// away, a click into another field) — is the one thing handled here, because
// only a picker cares: a modal traps focus, an overlay owns the screen. It
// means what an outside click means ("moving on") and reports as one.
//
// TWO FAMILIES share this hook, and the difference is the trigger:
//
//   button-triggered (SiteSelect, LanguageSelector) — the default. Space and
//   Enter OPEN the closed list, Home/End jump, the cursor always sits on an
//   option.
//
//   free-text (`freeText: true` — VlanCombo, TagInput, PillPicker,
//   PillMultiSelect) — the trigger IS the field's own text input, so keys
//   that would fight the caret are left alone: Space types a space, Home/End
//   move the caret, and only ArrowDown reopens a closed list. The cursor may
//   rest at -1 — "the typed text", no suggestion highlighted — and Enter
//   there is deliberately NOT consumed: the field's own handler runs after
//   and commits the draft (create a tag, accept a free VLAN id).

import { useEffect, useRef, useState } from "react";
import { useDismissable, type DismissReason } from "./dismiss";

export function usePicker<T>({
  items,
  onPick,
  initialCursor,
  freeText,
  keepOpen,
  onDismiss,
}: {
  /** The VISIBLE options — pass the filtered list, the cursor follows it. */
  items: T[];
  onPick: (item: T, index: number) => void;
  /** Where the cursor opens (e.g. the selected entry; -1 in freeText mode
   *  puts nothing under it, so Enter validates the typed text). Default 0. */
  initialCursor?: number;
  /** The trigger is a text input — see the family note in the header. */
  freeText?: boolean;
  /** Multi-pick fields accumulate chips: a pick must NOT close the list,
   *  or choosing two in a row costs a reopen per choice. */
  keepOpen?: boolean;
  /** Extra teardown when the dismissal STACK closes the layer (never on a
   *  pick or a programmatic close). The reason is what lets a free-text
   *  field abandon its draft on Escape yet commit it on outside-click. */
  onDismiss?: (reason: DismissReason) => void;
}) {
  const [open, setOpenState] = useState(false);
  const [cursor, setCursor] = useState(0);
  /** Bounds the layer for outside-click; put it on the component's root. */
  const boxRef = useRef<HTMLDivElement>(null);
  /** The trigger; focus returns to it on close. */
  const triggerRef = useRef<HTMLButtonElement>(null);

  // `open` as the EVENTS see it, not as the last render did: an outside
  // mousedown on a focusable element is followed by its focusin in the same
  // tick, before React re-renders, and the second must find the layer already
  // closed or a free-text field would commit its draft twice.
  const openRef = useRef(false);
  const setOpen = (v: boolean) => {
    openRef.current = v;
    setOpenState(v);
  };
  const onDismissRef = useRef(onDismiss);
  onDismissRef.current = onDismiss;

  // Free-text fields may park the cursor BEFORE the first option.
  const minCursor = freeText ? -1 : 0;
  const clamp = (c: number) => Math.max(minCursor, Math.min(c, items.length - 1));

  const close = () => {
    setOpen(false);
    triggerRef.current?.focus();
  };
  useDismissable(
    open,
    (reason) => {
      if (!openRef.current) return;
      close();
      onDismissRef.current?.(reason);
    },
    { insideRef: boxRef },
  );

  // Focus leaving the box. Closed WITHOUT refocusing the trigger: pulling
  // focus back from the field the user just tabbed to would be the opposite
  // of what Tab asked for. Reported as "outside" — same meaning, same draft
  // contract (commit, don't abandon).
  useEffect(() => {
    if (!open) return;
    const onFocusIn = (e: FocusEvent) => {
      if (!openRef.current) return;
      if (e.target instanceof Node && boxRef.current?.contains(e.target)) return;
      setOpen(false);
      onDismissRef.current?.("outside");
    };
    document.addEventListener("focusin", onFocusIn);
    return () => document.removeEventListener("focusin", onFocusIn);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open]);

  // The cursor tracks the option list it points into: a filter that shrinks
  // the list must not leave the cursor past its end.
  useEffect(() => {
    setCursor((c) => clamp(c));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [items.length]);

  const openAt = () => {
    setCursor(clamp(initialCursor ?? 0));
    setOpen(true);
  };

  /**
   * The keyboard vocabulary, attached to whichever element holds focus while
   * the picker is open (the trigger, or the filter input). Escape is absent
   * on purpose: the dismissal stack already owns it. A key this handler did
   * not consume (check `e.defaultPrevented`) is the field's to handle.
   */
  const onKeyDown = (e: React.KeyboardEvent) => {
    if (!open) {
      const opens = freeText
        ? e.key === "ArrowDown"
        : e.key === "ArrowDown" || e.key === "Enter" || e.key === " ";
      if (opens) {
        e.preventDefault();
        openAt();
      }
      return;
    }
    if (e.key === "ArrowDown") {
      e.preventDefault();
      setCursor((c) => Math.min(c + 1, items.length - 1));
    } else if (e.key === "ArrowUp") {
      e.preventDefault();
      setCursor((c) => Math.max(c - 1, minCursor));
    } else if (e.key === "Home" && !freeText) {
      e.preventDefault();
      setCursor(0);
    } else if (e.key === "End" && !freeText) {
      e.preventDefault();
      setCursor(items.length - 1);
    } else if (e.key === "Enter") {
      // Consumed HERE, before it reaches a surrounding <Form>: an open picker
      // owns Enter (UI-GUIDE §8.2 precedence) — unless the cursor rests on
      // the typed text (freeText, -1), which is the field's own commit.
      if (freeText && (cursor < 0 || items[cursor] === undefined)) return;
      e.preventDefault();
      e.stopPropagation();
      if (items[cursor] !== undefined) pick(cursor);
    }
  };

  const pick = (index: number) => {
    const item = items[index];
    if (item === undefined) return;
    onPick(item, index);
    if (!keepOpen) close();
  };

  return { open, setOpen, openAt, close, cursor, setCursor, pick, boxRef, triggerRef, onKeyDown };
}
