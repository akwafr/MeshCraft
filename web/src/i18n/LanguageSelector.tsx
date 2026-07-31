import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { QuickTip } from "../components";
import { SUPPORTED_LANGUAGES } from "./index";

/**
 * Themed language menu: flag alone in the chrome, flag + native name in the list.
 *
 * Same interaction vocabulary as SiteSelect (outside click, Escape, ↑/↓/Enter) —
 * a native <select> cannot show a flag-only closed state with a richer open list.
 */
export default function LanguageSelector({ className }: { className?: string }) {
  const { i18n, t } = useTranslation();
  const currentCode = (i18n.resolvedLanguage ?? i18n.language ?? "en").split("-")[0];
  const current =
    SUPPORTED_LANGUAGES.find((l) => l.code === currentCode) ?? SUPPORTED_LANGUAGES[0];

  const [open, setOpen] = useState(false);
  const [cursor, setCursor] = useState(
    Math.max(0, SUPPORTED_LANGUAGES.findIndex((l) => l.code === current.code)),
  );
  const boxRef = useRef<HTMLDivElement>(null);
  const btnRef = useRef<HTMLButtonElement>(null);

  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => {
      if (!boxRef.current?.contains(e.target as Node)) setOpen(false);
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        setOpen(false);
        btnRef.current?.focus();
      }
    };
    document.addEventListener("mousedown", onDown);
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("mousedown", onDown);
      document.removeEventListener("keydown", onKey);
    };
  }, [open]);

  useEffect(() => {
    setCursor(Math.max(0, SUPPORTED_LANGUAGES.findIndex((l) => l.code === current.code)));
  }, [current.code, open]);

  const pick = (code: string) => {
    void i18n.changeLanguage(code);
    setOpen(false);
    btnRef.current?.focus();
  };

  return (
    <div className={"langsel" + (className ? " " + className : "")} ref={boxRef}>
      <QuickTip text={t("common.language") + " — " + current.label} className="quicktip-inline">
        <button
          ref={btnRef}
          type="button"
          className={"langsel-btn" + (open ? " on" : "")}
          aria-haspopup="listbox"
          aria-expanded={open}
          aria-label={t("common.language") + " — " + current.label}
          onClick={() => setOpen((o) => !o)}
          onKeyDown={(e) => {
            if (!open && (e.key === "ArrowDown" || e.key === "Enter" || e.key === " ")) {
              e.preventDefault();
              setOpen(true);
              return;
            }
            if (!open) return;
            if (e.key === "ArrowDown") {
              e.preventDefault();
              setCursor((c) => Math.min(c + 1, SUPPORTED_LANGUAGES.length - 1));
            } else if (e.key === "ArrowUp") {
              e.preventDefault();
              setCursor((c) => Math.max(c - 1, 0));
            } else if (e.key === "Enter") {
              e.preventDefault();
              pick(SUPPORTED_LANGUAGES[cursor].code);
            }
          }}
        >
          <span className="langsel-flag" aria-hidden="true">{current.flag}</span>
        </button>
      </QuickTip>
      {open && (
        <div className="langsel-pop" role="listbox" aria-label={t("common.language")}>
          {SUPPORTED_LANGUAGES.map((l, i) => (
            <button
              key={l.code}
              type="button"
              role="option"
              aria-selected={l.code === current.code}
              className={
                "langsel-opt" +
                (i === cursor ? " cursor" : "") +
                (l.code === current.code ? " sel" : "")
              }
              onMouseEnter={() => setCursor(i)}
              onClick={() => pick(l.code)}
            >
              <span className="langsel-flag" aria-hidden="true">{l.flag}</span>
              <span className="langsel-label">{l.label}</span>
            </button>
          ))}
        </div>
      )}
    </div>
  );
}
