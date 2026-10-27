import { useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { usePicker } from "./ui/picker";

/**
 * Choose SEVERAL items from a list, with the chosen ones shown as pills inside
 * the field.
 *
 * # Why this replaced a column of checkboxes
 *
 * The root-bridge picker was a checkbox per device in a table of the whole
 * fleet. That reads well when the answer is "most of them" and badly when it is
 * "one, occasionally two" — which is what root bridges are. An operator had to
 * scan forty rows to see that one box was ticked, and the answer they came for
 * was never visible without reading everything.
 *
 * A pill field inverts that: the selection IS the display. What was chosen is
 * the only thing occupying space, and the fleet appears only while somebody is
 * choosing.
 *
 * # Filtering by typing, not a scrollbar
 *
 * The list is every device on the site, which on a real one is more than fits a
 * dropdown. Typing narrows it; nothing else does. There is deliberately no
 * "select all" — this control exists for a choice whose whole risk is making it
 * twice (cf. driver.RootWarning), and a button that selects the fleet would be
 * a loaded gun for exactly that.
 */

export interface PillOption {
  id: string;
  label: string;
  /** Optional second line in the suggestion list — a model, an address. */
  hint?: string;
}

export function PillMultiSelect({
  options,
  selected,
  onChange,
  placeholder,
  disabled,
  ariaLabel,
}: {
  options: PillOption[];
  selected: string[];
  onChange: (ids: string[]) => void;
  placeholder?: string;
  disabled?: boolean;
  ariaLabel?: string;
}) {
  const { t } = useTranslation();
  const [query, setQuery] = useState("");
  const inputRef = useRef<HTMLInputElement>(null);

  const byId = useMemo(() => {
    const m = new Map<string, PillOption>();
    for (const o of options) m.set(o.id, o);
    return m;
  }, [options]);

  const matches = useMemo(() => {
    const q = query.trim().toLowerCase();
    return options
      .filter((o) => !selected.includes(o.id))
      .filter((o) => !q || o.label.toLowerCase().includes(q))
      .slice(0, 40);
  }, [options, selected, query]);

  // keepOpen: choosing two in a row is the case that matters, and a control
  // that closed itself after one would make the second choice cost a reopen.
  const picker = usePicker({
    items: matches,
    freeText: true,
    keepOpen: true,
    onPick: (o) => add(o.id),
  });

  const add = (id: string) => {
    if (disabled || selected.includes(id)) return;
    onChange([...selected, id]);
    setQuery("");
    // Focus stays in the field, for the same two-in-a-row reason.
    inputRef.current?.focus();
  };
  const remove = (id: string) => {
    if (disabled) return;
    onChange(selected.filter((x) => x !== id));
  };

  return (
    <div className="pillbox" ref={picker.boxRef}>
      <div
        className="pillbox-field"
        onClick={() => !disabled && inputRef.current?.focus()}
      >
        {selected.map((id) => (
          <span key={id} className="chip sel">
            {/* An id with no option behind it is a device that was deleted
                while it was still designated. Shown as the raw id rather than
                dropped: silently removing it would change the configuration
                because a screen could not render it. */}
            {byId.get(id)?.label ?? id}
            <button
              type="button"
              onClick={(e) => {
                e.stopPropagation();
                remove(id);
              }}
              disabled={disabled}
              aria-label={t("common.remove") as string}
            >
              ×
            </button>
          </span>
        ))}
        <input
          ref={inputRef}
          value={query}
          disabled={disabled}
          placeholder={selected.length === 0 ? placeholder : ""}
          aria-label={ariaLabel}
          onChange={(e) => {
            setQuery(e.target.value);
            picker.setCursor(0);
            picker.setOpen(true);
          }}
          onFocus={() => picker.openAt()}
          onKeyDown={(e) => {
            picker.onKeyDown(e);
            if (e.defaultPrevented) return;
            // Backspace on an EMPTY field removes the last pill — the behaviour
            // every tag field has, and the only way to undo a choice without
            // reaching for the mouse.
            if (e.key === "Backspace" && query === "" && selected.length > 0) {
              remove(selected[selected.length - 1]);
            }
          }}
        />
      </div>
      {picker.open && !disabled && matches.length > 0 && (
        <div className="pill-suggest" role="listbox">
          {matches.map((o, i) => (
            <button
              key={o.id}
              type="button"
              role="option"
              className={i === picker.cursor ? "on" : undefined}
              onMouseEnter={() => picker.setCursor(i)}
              onMouseDown={(e) => e.preventDefault()}
              onClick={() => picker.pick(i)}
            >
              {o.label}
              {o.hint ? <span className="note"> — {o.hint}</span> : null}
            </button>
          ))}
        </div>
      )}
    </div>
  );
}
