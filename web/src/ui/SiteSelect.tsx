// The site selector: the one control on the top bar that changes what every
// other screen is about.
//
// IT IS NOT `SearchSelect`, AND THE TWO ARE DELIBERATELY NOT MERGED. That one
// is a FORM CONTROL — labelled, full-width, sized to be the main thing on a
// step of a wizard. This one is a piece of CHROME: fixed width in the bar,
// keyed on a site's URL slug rather than on a UUID, and it owns the site
// vocabulary. What they share is the part that is genuinely hard and now
// genuinely shared: `usePicker` for the interaction (open state, the
// ↑/↓/Home/End/Enter vocabulary, Escape through the dismissal stack, focus
// restore) and `searchMatch` for the filtering rule. What stays separate is
// the markup and the sizing, which is the part that actually differs.
//
// It LOOKS like SearchSelect now, which it did not: this popover was a flat
// `--bg-panel` box with a hardcoded `rgba(0,0,0,0.5)` shadow and pixel
// paddings, floating under a glass bar it visibly did not belong to.
//
// Lives in ui/ with the other primitives — like AccountMenu, and for the same
// reason: the bare <button>s below are the implementation of a control, not a
// page reaching past one.

import { useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import type { Site } from "../api";
import { usePicker } from "./picker";
import { filterFolded, foldRows } from "./searchMatch";

function CheckIcon() {
  return (
    <svg
      width="14"
      height="14"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="3"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden
    >
      <path d="M20 6L9 17l-5-5" />
    </svg>
  );
}

/**
 * `value` / `onChange` use the site **slug** (URL identity), not the UUID.
 */
export default function SiteSelect({
  sites,
  value,
  onChange,
}: {
  sites: Site[];
  value: string;
  onChange: (slug: string) => void;
}) {
  const { t } = useTranslation();
  const [query, setQuery] = useState("");
  const inputRef = useRef<HTMLInputElement>(null);
  const listRef = useRef<HTMLDivElement>(null);

  const current = sites.find((s) => s.slug === value || s.id === value);

  // THE SEARCH HAS TO BE INSTANT, AND THAT IS A MEMOISATION QUESTION.
  //
  // Folding is memoised on the SITE LIST — it changes when a site is created,
  // which is to say never while somebody is typing. A keystroke then folds
  // only the query and runs n substring scans over strings that are already
  // lowercase and already stripped of their accents. The shape that folds
  // every site on every keystroke is the one that makes a large instance feel
  // like it is thinking.
  const rows = useMemo(
    () => foldRows(sites, (s) => `${s.name} ${s.slug ?? ""}`),
    [sites],
  );
  const matches = useMemo(() => filterFolded(rows, query), [rows, query]);

  const picker = usePicker({
    items: matches,
    initialCursor: Math.max(
      0,
      matches.findIndex((s) => s.slug === value || s.id === value),
    ),
    onPick: (s) => {
      onChange(s.slug);
      setQuery("");
    },
  });

  // The search field takes focus when the list opens, and the query is the
  // LAYER's state rather than the field's: a list reopened still holding the
  // last search would hide the site somebody is looking at.
  useEffect(() => {
    if (!picker.open) return;
    setQuery("");
    const h = window.setTimeout(() => inputRef.current?.focus(), 0);
    return () => window.clearTimeout(h);
  }, [picker.open]);

  // A NEW result set restarts the cursor at the top.
  useEffect(() => picker.setCursor(0), [query]); // eslint-disable-line react-hooks/exhaustive-deps

  // ↑/↓ past the edge of the visible window must bring the row with it. An
  // instance with forty sites scrolls, and a cursor that walks out of view
  // leaves the keyboard driving a list nobody can see.
  useEffect(() => {
    if (!picker.open) return;
    const row = listRef.current?.children[picker.cursor];
    if (row instanceof HTMLElement) row.scrollIntoView({ block: "nearest" });
  }, [picker.cursor, picker.open]);

  return (
    <div className="sitesel" ref={picker.boxRef}>
      <button
        ref={picker.triggerRef}
        type="button"
        className={"sitesel-btn" + (picker.open ? " on" : "")}
        aria-haspopup="listbox"
        aria-expanded={picker.open}
        onClick={() => (picker.open ? picker.close() : picker.openAt())}
        onKeyDown={picker.onKeyDown}
      >
        <span className={current ? "sitesel-name" : "sitesel-name is-empty"}>
          {current?.name ?? t("siteSelect.chooseASite")}
        </span>
        <svg
          className="sitesel-chevron"
          width="16"
          height="16"
          viewBox="0 0 24 24"
          fill="none"
          stroke="currentColor"
          strokeWidth="2"
          strokeLinecap="round"
          strokeLinejoin="round"
          aria-hidden
        >
          <path d="M6 9l6 6 6-6" />
        </svg>
      </button>
      {picker.open && (
        <div
          className="sitesel-pop glass"
          role="listbox"
          aria-label={t("siteSelect.listLabel") as string}
        >
          <input
            ref={inputRef}
            className="sitesel-search"
            value={query}
            autoComplete="off"
            spellCheck={false}
            aria-label={t("siteSelect.searchPlaceholder") as string}
            placeholder={t("siteSelect.searchPlaceholder") as string}
            onChange={(e) => setQuery(e.target.value)}
            onKeyDown={picker.onKeyDown}
          />
          <div className="sitesel-list" ref={listRef}>
            {matches.length === 0 ? (
              <div className="sitesel-empty">{t("siteSelect.noSiteFound")}</div>
            ) : (
              matches.map((s, i) => {
                const sel = s.slug === value || s.id === value;
                return (
                  <button
                    key={s.id}
                    type="button"
                    role="option"
                    aria-selected={sel}
                    className={
                      "sitesel-opt" +
                      (i === picker.cursor ? " cursor" : "") +
                      (sel ? " sel" : "")
                    }
                    onMouseEnter={() => picker.setCursor(i)}
                    onClick={() => picker.pick(i)}
                  >
                    <span className="sitesel-opt-text">
                      <span className="sitesel-opt-label">{s.name}</span>
                      <span className="sitesel-opt-detail mono">{s.slug}</span>
                    </span>
                    {/* THE MARK ALONE SAYS "THIS ONE", AND IT SAYS IT TWICE.
                        A word next to the tick ("current") was redundant ink:
                        the row is already washed in `--accent-wash` by `.sel`
                        and already carries `aria-selected`, which is what a
                        screen reader announces. The tick survives because it
                        is the only part that survives a theme, a zoom and a
                        colour-blind eye; the label added nothing any of those
                        three could not already see. */}
                    {sel && (
                      <span className="sitesel-opt-mark">
                        <CheckIcon />
                      </span>
                    )}
                  </button>
                );
              })
            )}
          </div>
        </div>
      )}
    </div>
  );
}
