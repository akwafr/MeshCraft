#!/usr/bin/env node
// The frontend ratchet — the UI-GUIDE's counts, enforced the way
// scripts/shortdecl enforces rule #1: a baseline that MAY ONLY EVER BE
// REDUCED. Clean a screen, run this, it prints the new number; put it in
// BASELINES. Raising a number to make a build pass defeats the whole thing.
//
// Heuristics are deliberately blunt: a fixed baseline makes a REGRESSION
// legible ("you added 3") even when the matcher over-counts the legacy.

import { readdirSync, readFileSync, statSync } from "node:fs";
import { join } from "node:path";

// 2026-08-17 baselines, counted at introduction (post Phase 0-2 work).
// inline-style 846 → 833 with the Banner merge (2026-08-18).
// 2026-10-24, the responsive/material pass:
//   bare-button  89 → 88   logout moved into ui/AccountMenu
//   inline-style 833 → 774, native-title 25 → 24, bare-button 89 → 86 over
//                          the responsive/material pass and the parallel
//                          agents that followed it. The biggest single drop
//                          was the config action columns: the inline styles
//                          WERE the column widths.
//   ten `style={{overflowX:"auto"}}` wrappers became
//                          `.scroll-x .scroll-cue`, plus the faceplate's
//                          hand-built flex rows became classes
// 2026-10-24, the Clients screen's search + broom:
//   bare-button  88 → 87   the filters' "clear" chip became an ActionButton
//                          when the MAC search joined what it clears
// 2026-10-24, /config row actions became icons (ui/IconAction.tsx):
//   bare-button  87 → 86   the nameless ✕ that dropped a role row in the
//                          Users modal became an IconAction, which means it
//                          finally has an accessible name
//   inline-style 816 → 794 twenty-two of them were the action columns
//                          themselves — `style={{ width: 300 }}` on a header,
//                          `style={{ textAlign: "right" }}` on every cell —
//                          now one `.col-actions` class that asks the table
//                          for the minimum instead of reserving 300 px
// 2026-10-24, ADR-0007 — the aggregate became a port on the facade:
//   bare-button  86 → 82   the deleted LagPanel tile held six bare buttons
//                          (new, edit, dissolve, a member chip, apply,
//                          cancel); the facade's aggregate cell is one, for
//                          the same reason PortSquare is one, and everything
//                          that is a VERB is now an ActionButton
//   inline-style 774 → 771 what is left of the tile's six is the dynamic
//                          aggregate colour, which no class can carry — the
//                          cell's overline, the builder's swatch, the
//                          panel's swatch. Everything else is `--sp-*`
//   native-title  24 → 23  the tile's four `title=` attributes became
//                          QuickTip on the cell and prose in the panel
// inline-style 764 → 763 banked, not earned: the mobile Clients/dashboard
// merges left the counter one under its baseline. A ratchet that is not
// lowered when it passes has already given the gain back.
// 2026-10-25, ADR-0011 — the profile, the API reference, the site selector:
//   bare-button  81 → 79   SiteSelect moved from src/ to src/ui/, where it
//                          belongs: it is a CONTROL (the top bar's site
//                          picker), so its trigger and its option rows are
//                          the implementation of a primitive rather than a
//                          page reaching past one — the same argument that
//                          moved the logout button into ui/AccountMenu. The
//                          two new screens add none: every action on them is
//                          an ActionButton, ActionLink or IconDeleteButton.
// 2026-10-25, the page headings came off five screens:
//   inline-style 763 → 762 VlanLegend's hover hint was its last inline
//                          margin, and the hint went with the panel title
//                          the Devices side panel no longer needs.
// 2026-10-26, the login form moves onto the primitives:
//   bare-button  79 → 78   its submit is an ActionButton kind="connect".
//   inline-style 762 → 760 the brand's accent and the submit's width are
//                          classes now.
const BASELINES = {
  "bare-button": 78,
  "inline-style": 760,
  // The audit found 7 REAL native tooltips; the counter reads 25 because
  // the line-based matcher still sees some component props (untranslated
  // title expressions). Blunt, fixed, ratcheting: additions still show.
  "native-title": 23,
  "raw-keydown": 3,
  "window-confirm": 0,
};

const ROOT = new URL("../src", import.meta.url).pathname;

// Files that IMPLEMENT the primitives the rules point at.
const EXEMPT = new Set(["ActionButton.tsx", "components.tsx"]);

function* walk(dir) {
  for (const name of readdirSync(dir)) {
    const p = join(dir, name);
    if (statSync(p).isDirectory()) {
      if (name === "ui") continue; // the primitives package
      yield* walk(p);
    } else if (/\.(tsx|ts)$/.test(name) && !/\.test\.tsx?$/.test(name)) {
      yield p;
    }
  }
}

function countAll(src, re) {
  return (src.match(re) ?? []).length;
}

const counts = Object.fromEntries(Object.keys(BASELINES).map((k) => [k, 0]));
const offenders = {};
function add(rule, file, n) {
  if (n <= 0) return;
  counts[rule] += n;
  (offenders[rule] ??= []).push(`${file} (${n})`);
}

for (const path of walk(ROOT)) {
  const rel = path.slice(ROOT.length + 1);
  const base = rel.split("/").pop();
  const src = readFileSync(path, "utf8");
  if (!EXEMPT.has(base)) {
    add("bare-button", rel, countAll(src, /<button\b/g));
  }
  add("inline-style", rel, countAll(src, /style=\{\{/g));
  // Native tooltips: title= outside the components that legitimately consume
  // it (ActionButton upgrades it to QuickTip). Blunt on purpose — see header.
  if (!EXEMPT.has(base)) {
    const lines = src.split("\n").filter(
      (l) =>
        /\btitle=/.test(l) &&
        // title={t(…)} is a translated PROP flowing to a component's API
        // (Modal, ConfirmDialog, PageHeader…) — a native tooltip would be a
        // literal or an untranslated expression. Line-based and blunt, like
        // everything here; the fixed baseline does the real work.
        !/ActionButton|ActionLink|QuickTip|HelpTip|aria-|<title|document\.title|title=\{t\(/.test(l),
    );
    add("native-title", rel, lines.length);
  }
  add("raw-keydown", rel, countAll(src, /addEventListener\(\s*["']keydown/g));
  add("window-confirm", rel, countAll(src, /window\.confirm\(/g));
}

let failed = false;
for (const [rule, baseline] of Object.entries(BASELINES)) {
  const n = counts[rule];
  const verdict = n > baseline ? "OVER" : "ok";
  if (n > baseline) failed = true;
  console.log(
    `uilint: ${rule.padEnd(15)} ${String(n).padStart(4)} (baseline ${baseline}) ${verdict}`,
  );
  if (n > baseline) {
    for (const o of offenders[rule] ?? []) console.log(`  ${o}`);
  }
}
if (failed) {
  console.error(
    "\nuilint: over baseline — use the primitive the UI-GUIDE names " +
      "(ActionButton, tokens, useDismissable, QuickTip, ConfirmDialog) " +
      "instead of lowering the bar.",
  );
  process.exit(1);
}
