// The theme choice — UI-GUIDE §1.
//
// Three states, not two. "system" is the default and is the ABSENCE of the
// `data-theme` attribute, which is what lets tokens.css resolve the palette
// through `light-dark()` and the browser's own `prefers-color-scheme`. An
// explicit choice stamps the attribute and pins `color-scheme`, so the
// native widgets (scrollbars, date pickers, autofill) follow the page
// instead of contradicting it.
//
// Why a module and not a context: the attribute lives on <html>, outside the
// React root, and the FIRST paint has to be right. `applyStored()` runs from
// main.tsx before render; a provider could not.

import { useCallback, useEffect, useState } from "react";

export type ThemeChoice = "system" | "light" | "dark";

const KEY = "meshcraft.theme";

/**
 * What an operator who has never touched the setting gets.
 *
 * DARK, not "system" — and that is a product decision, not an oversight.
 * meshcraft was dark-only for its whole life; defaulting to "system" would
 * silently flip every existing installation to light on the first machine
 * whose OS says light, which is a visible change nobody asked for. "Auto"
 * stays one tap away for anyone who wants it.
 */
const DEFAULT: ThemeChoice = "dark";

/** What the browser will actually paint, given a choice. */
export type Resolved = "light" | "dark";

export function resolve(choice: ThemeChoice): Resolved {
  if (choice !== "system") return choice;
  if (typeof window === "undefined" || !window.matchMedia) return "dark";
  return window.matchMedia("(prefers-color-scheme: dark)").matches
    ? "dark"
    : "light";
}

export function stored(): ThemeChoice {
  try {
    const v = localStorage.getItem(KEY);
    if (v === "light" || v === "dark" || v === "system") return v;
  } catch {
    /* private mode, blocked storage — the default is a fine answer */
  }
  return DEFAULT;
}

// The two grounds, written out. They have to be literals: this sets the
// browser-chrome colour on mobile, and a `var()` would not resolve in a
// <meta> attribute. Keep in step with --bg in tokens.css.
const GROUND: Record<Resolved, string> = {
  dark: "#0d1014",
  light: "#eef1f6",
};

export function apply(choice: ThemeChoice) {
  const root = document.documentElement;
  if (choice === "system") root.removeAttribute("data-theme");
  else root.setAttribute("data-theme", choice);

  // The address bar and the task-switcher card are part of the window on a
  // phone. Left pinned to the old dark value they frame a light page in a
  // black bezel, which reads as the page having failed to load its top.
  const meta = document.querySelector('meta[name="theme-color"]');
  if (meta) meta.setAttribute("content", GROUND[resolve(choice)]);
}

/** Called once from main.tsx, before the first render. */
export function applyStored() {
  apply(stored());
}

/**
 * The choice, and a setter that persists it. Re-renders when the SYSTEM
 * preference changes too, so a "system" toggle label stays truthful when the
 * OS flips at sunset while the tab is open.
 */
export function useTheme(): {
  choice: ThemeChoice;
  resolved: Resolved;
  setChoice: (c: ThemeChoice) => void;
} {
  const [choice, setChoiceState] = useState<ThemeChoice>(stored);
  const [resolved, setResolved] = useState<Resolved>(() => resolve(stored()));

  useEffect(() => {
    if (!window.matchMedia) return;
    const mq = window.matchMedia("(prefers-color-scheme: dark)");
    const onChange = () => {
      setResolved(resolve(choice));
      if (choice === "system") apply("system");
    };
    mq.addEventListener("change", onChange);
    return () => mq.removeEventListener("change", onChange);
  }, [choice]);

  const setChoice = useCallback((c: ThemeChoice) => {
    setChoiceState(c);
    setResolved(resolve(c));
    apply(c);
    try {
      localStorage.setItem(KEY, c);
    } catch {
      /* the attribute is already set; only persistence is lost */
    }
  }, []);

  return { choice, resolved, setChoice };
}
