import { useTranslation } from "react-i18next";
import { QuickTip } from "../components";
import { usePicker } from "../ui/picker";
import { SUPPORTED_LANGUAGES } from "./index";

/**
 * Themed language menu: flag alone in the chrome, flag + native name in the
 * list. A native <select> cannot show a flag-only closed state with a richer
 * open list.
 *
 * The interaction (open state, ↑/↓/Enter, Escape through the dismissal
 * stack, focus restore) is usePicker's — this file owns only the markup.
 */
export default function LanguageSelector({
  className,
  labelled,
}: {
  className?: string;
  /** Flag + native name + chevron in the trigger: the account menu's row,
   *  where the name is the whole point and a flag alone reads as a stray
   *  emoji. The bare flag stays the chrome form (login card). */
  labelled?: boolean;
}) {
  const { i18n, t } = useTranslation();
  const currentCode = (i18n.resolvedLanguage ?? i18n.language ?? "en").split("-")[0];
  const current =
    SUPPORTED_LANGUAGES.find((l) => l.code === currentCode) ?? SUPPORTED_LANGUAGES[0];

  const picker = usePicker({
    items: SUPPORTED_LANGUAGES,
    initialCursor: Math.max(
      0,
      SUPPORTED_LANGUAGES.findIndex((l) => l.code === current.code),
    ),
    onPick: (l) => void i18n.changeLanguage(l.code),
  });

  const trigger = (
    <button
      ref={picker.triggerRef}
      type="button"
      className={"langsel-btn" + (labelled ? " langsel-btn--labelled" : "") + (picker.open ? " on" : "")}
      aria-haspopup="listbox"
      aria-expanded={picker.open}
      aria-label={t("common.language") + " — " + current.label}
      onClick={() => (picker.open ? picker.close() : picker.openAt())}
      onKeyDown={picker.onKeyDown}
    >
      <span className="langsel-flag" aria-hidden="true">{current.flag}</span>
      {labelled && (
        <>
          <span className="langsel-label" aria-hidden="true">{current.label}</span>
          <svg className="langsel-caret" width="12" height="12" viewBox="0 0 24 24" fill="none"
            stroke="currentColor" strokeWidth="2.5" strokeLinecap="round" strokeLinejoin="round" aria-hidden>
            <path d="M6 9l6 6 6-6" />
          </svg>
        </>
      )}
    </button>
  );

  return (
    <div className={"langsel" + (className ? " " + className : "")} ref={picker.boxRef}>
      {/* A tip would only repeat a name that is already written. */}
      {labelled ? (
        trigger
      ) : (
        <QuickTip text={t("common.language") + " — " + current.label} className="quicktip-inline">
          {trigger}
        </QuickTip>
      )}
      {picker.open && (
        <div className="langsel-pop" role="listbox" aria-label={t("common.language")}>
          {SUPPORTED_LANGUAGES.map((l, i) => (
            <button
              key={l.code}
              type="button"
              role="option"
              aria-selected={l.code === current.code}
              className={
                "langsel-opt" +
                (i === picker.cursor ? " cursor" : "") +
                (l.code === current.code ? " sel" : "")
              }
              onMouseEnter={() => picker.setCursor(i)}
              onClick={() => picker.pick(i)}
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
