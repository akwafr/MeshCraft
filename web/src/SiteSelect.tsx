import { useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import type { Site } from "./api";

/**
 * Site selector: button + dropdown list with an integrated search field
 * (multi-site instances quickly grow too large for a <select>).
 * Keyboard: type to filter, ↑/↓ to browse, Enter to pick, Escape to close.
 *
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
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [cursor, setCursor] = useState(0);
  const boxRef = useRef<HTMLDivElement>(null);
  const inputRef = useRef<HTMLInputElement>(null);

  const current = sites.find((s) => s.slug === value || s.id === value);
  const matches = useMemo(() => {
    const q = query.trim().toLowerCase();
    if (!q) return sites;
    return sites.filter(
      (s) =>
        s.name.toLowerCase().includes(q) ||
        (s.slug ?? "").toLowerCase().includes(q),
    );
  }, [sites, query]);

  useEffect(() => {
    if (!open) return;
    inputRef.current?.focus();
    const onDown = (e: MouseEvent) => {
      if (!boxRef.current?.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener("mousedown", onDown);
    return () => document.removeEventListener("mousedown", onDown);
  }, [open]);

  useEffect(() => setCursor(0), [query]);

  const pick = (slug: string) => {
    onChange(slug);
    setOpen(false);
    setQuery("");
  };

  return (
    <div className="sitesel" ref={boxRef}>
      <button
        type="button"
        className="sitesel-btn"
        aria-haspopup="listbox"
        aria-expanded={open}
        onClick={() => setOpen((o) => !o)}
      >
        <span className="sitesel-name">{current?.name ?? t("siteSelect.chooseASite")}</span>
        <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5" strokeLinecap="round" strokeLinejoin="round">
          <path d="M6 9l6 6 6-6" />
        </svg>
      </button>
      {open && (
        <div className="sitesel-pop">
          <input
            ref={inputRef}
            className="sitesel-search"
            value={query}
            placeholder={t("siteSelect.searchPlaceholder") as string}
            onChange={(e) => setQuery(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Escape") setOpen(false);
              else if (e.key === "ArrowDown")
                setCursor((c) => Math.min(c + 1, matches.length - 1));
              else if (e.key === "ArrowUp") setCursor((c) => Math.max(c - 1, 0));
              else if (e.key === "Enter" && matches[cursor]) pick(matches[cursor].slug);
            }}
          />
          <div className="sitesel-list" role="listbox">
            {matches.map((s, i) => (
              <button
                key={s.id}
                type="button"
                role="option"
                aria-selected={s.slug === value}
                className={
                  "sitesel-opt" + (i === cursor ? " cursor" : "") + (s.slug === value ? " sel" : "")
                }
                onMouseEnter={() => setCursor(i)}
                onClick={() => pick(s.slug)}
              >
                <span>{s.name}</span>
                <span className="note" style={{ marginLeft: 8 }}>{s.slug}</span>
                {s.slug === value && <span className="note">{t("siteSelect.current")}</span>}
              </button>
            ))}
            {matches.length === 0 && <div className="note sitesel-empty">{t("siteSelect.noSiteFound")}</div>}
          </div>
        </div>
      )}
    </div>
  );
}
