// The appearance control — three states, because "system" is a real answer
// and not the absence of one.
//
// A two-state switch would force a choice the operator may not want to make:
// most people want the interface to do what the rest of their machine does,
// and the only way to say that is to have somewhere to say it. Picking
// "Auto" is also the only setting that stays correct when the OS flips at
// sunset, which matters for a product used in rooms with no windows and
// again on a phone outdoors an hour later.
//
// Rendered as a segment group and not a dropdown: three options, all three
// worth seeing, and the current one is read at a glance.

import { useTranslation } from "react-i18next";
import { useTheme, type ThemeChoice } from "./theme";

function SunIcon() {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.9"
      strokeLinecap="round" strokeLinejoin="round" aria-hidden>
      <circle cx="12" cy="12" r="4" />
      <path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4" />
    </svg>
  );
}
function MoonIcon() {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.9"
      strokeLinecap="round" strokeLinejoin="round" aria-hidden>
      <path d="M20 14.5A8.5 8.5 0 0 1 9.5 4a8.5 8.5 0 1 0 10.5 10.5z" />
    </svg>
  );
}
function AutoIcon() {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.9"
      strokeLinecap="round" strokeLinejoin="round" aria-hidden>
      <circle cx="12" cy="12" r="8.5" />
      {/* Half filled: the glyph says "whichever of the two applies". */}
      <path d="M12 3.5a8.5 8.5 0 0 1 0 17z" fill="currentColor" stroke="none" />
    </svg>
  );
}

const OPTIONS: { value: ThemeChoice; icon: () => JSX.Element; key: string }[] = [
  { value: "system", icon: AutoIcon, key: "common.themeSystem" },
  { value: "light", icon: SunIcon, key: "common.themeLight" },
  { value: "dark", icon: MoonIcon, key: "common.themeDark" },
];

export default function ThemeControl({ labelled }: { labelled?: boolean }) {
  const { t } = useTranslation();
  const { choice, setChoice } = useTheme();

  return (
    <div
      className={"themectl" + (labelled ? " themectl--labelled" : "")}
      role="radiogroup"
      aria-label={t("common.theme") as string}
    >
      {OPTIONS.map((o) => {
        const Icon = o.icon;
        const label = t(o.key) as string;
        return (
          <button
            key={o.value}
            type="button"
            role="radio"
            aria-checked={choice === o.value}
            aria-label={label}
            className={"themectl-seg" + (choice === o.value ? " on" : "")}
            onClick={() => setChoice(o.value)}
          >
            <Icon />
            {labelled && <span>{label}</span>}
          </button>
        );
      })}
    </div>
  );
}
