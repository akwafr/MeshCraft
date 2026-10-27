// A large select with an integrated search field — ADR-0005.
//
// `SiteSelect` already does this for sites, and the two are deliberately not
// merged: that one is a piece of chrome sized for the top bar, keyed on a URL
// slug, and it owns the site vocabulary. This one is a FORM CONTROL, sized to
// be the main thing on a step of a wizard, and it carries a label and a
// per-option detail line. What they share are the two parts that are
// genuinely hard: `usePicker` for the interaction — open state, the
// ↑/↓/Home/End/Enter vocabulary, Escape through the dismissal stack, focus
// restore — and `searchMatch` for the filtering rule, which is a product
// behaviour rather than a component's and so belongs to neither.
//
// A native <select> would have been free and is the right answer for three
// options. It is the wrong one here because it cannot carry a search field,
// and the list it replaces was a grid nobody could search either.

import { useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { usePicker } from "./picker";
import { filterFolded, foldRows } from "./searchMatch";

export interface SearchOption {
  id: string;
  label: string;
  /** One line under the name. Optional: a brand has none, a model does. */
  detail?: string;
  /**
   * Searchable text that is NOT shown — ADR-0010.
   *
   * The reset procedures carry `Aliases` (`s5720`, `2530`, `gs1900`…), which
   * are exactly the numbers somebody reads off a lid and the worst thing to
   * print: a dozen of them per row would bury the two words that name the
   * family. They go here, so typing the number finds the row anyway.
   */
  keywords?: string;
}

export default function SearchSelect({
  label,
  value,
  options,
  onChange,
  placeholder,
  disabled,
  emptyText,
  id,
}: {
  label: string;
  /** The selected option's id, or "" for none. */
  value: string;
  options: SearchOption[];
  onChange: (id: string) => void;
  placeholder: string;
  disabled?: boolean;
  /** Shown when the query matches nothing. */
  emptyText?: string;
  id?: string;
}) {
  const { t } = useTranslation();
  const [query, setQuery] = useState("");
  const inputRef = useRef<HTMLInputElement>(null);

  const current = options.find((o) => o.id === value);
  // Folding is memoised on the OPTION LIST, not on the query: a keystroke
  // folds the query and nothing else. The option's hidden `keywords` join the
  // haystack here (ADR-0010) — typing the number off a lid finds the row.
  const rows = useMemo(
    () => foldRows(options, (o) => `${o.label} ${o.detail ?? ""} ${o.keywords ?? ""}`),
    [options],
  );
  const matches = useMemo(() => filterFolded(rows, query), [rows, query]);

  const picker = usePicker({
    items: matches,
    initialCursor: Math.max(0, matches.findIndex((o) => o.id === value)),
    onPick: (o) => onChange(o.id),
  });

  // The query is the layer's state, not the field's: a list reopened still
  // holding the last search would hide the option somebody is looking at.
  useEffect(() => {
    if (picker.open) {
      setQuery("");
      // Focus the search, not the list: typing is the fast path, and the
      // arrow keys reach the list from here anyway (onKeyDown below).
      const h = window.setTimeout(() => inputRef.current?.focus(), 0);
      return () => window.clearTimeout(h);
    }
  }, [picker.open]);

  return (
    <div className="searchsel" ref={picker.boxRef}>
      <label className="searchsel-label" htmlFor={id}>
        {label}
      </label>
      <button
        id={id}
        ref={picker.triggerRef}
        type="button"
        className={"searchsel-btn" + (picker.open ? " on" : "")}
        disabled={disabled}
        aria-haspopup="listbox"
        aria-expanded={picker.open}
        onClick={() => (picker.open ? picker.close() : picker.openAt())}
        onKeyDown={picker.onKeyDown}
      >
        <span className={current ? "searchsel-value" : "searchsel-value is-empty"}>
          {current ? current.label : placeholder}
        </span>
        <svg
          className="searchsel-chevron"
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
        <div className="searchsel-pop glass" role="listbox" aria-label={label}>
          <input
            ref={inputRef}
            className="searchsel-search"
            value={query}
            placeholder={t("common.search") as string}
            onChange={(e) => {
              setQuery(e.target.value);
              picker.setCursor(0);
            }}
            onKeyDown={picker.onKeyDown}
          />
          <div className="searchsel-list">
            {matches.length === 0 ? (
              <div className="searchsel-empty">{emptyText ?? t("common.noData")}</div>
            ) : (
              matches.map((o, i) => (
                <button
                  key={o.id}
                  type="button"
                  role="option"
                  aria-selected={o.id === value}
                  className={
                    "searchsel-opt" +
                    (i === picker.cursor ? " cursor" : "") +
                    (o.id === value ? " sel" : "")
                  }
                  onMouseEnter={() => picker.setCursor(i)}
                  onClick={() => picker.pick(i)}
                >
                  <span className="searchsel-opt-label">{o.label}</span>
                  {o.detail && <span className="searchsel-opt-detail">{o.detail}</span>}
                </button>
              ))
            )}
          </div>
        </div>
      )}
    </div>
  );
}
