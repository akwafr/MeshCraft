import type { DriftChange } from "./api";
import {
  type ButtonHTMLAttributes,
  type ReactNode,
  useCallback,
  useEffect,
  useId,
  useLayoutEffect,
  useRef,
  useState,
} from "react";
import { useTranslation } from "react-i18next";
import { useDismissable } from "./ui/dismiss";
import { usePicker } from "./ui/picker";
import { Banner } from "./ui/Banner";
import {
  ActionButton as ActionButtonBase,
  ActionIcon,
  ActionLink,
  type ActionKind,
} from "./ActionButton";
import { claimHoverTip, registerHoverTip, releaseHoverTip } from "./hoverExclusive";

export { ActionIcon, ActionLink };
export type { ActionKind };

/** Minimal toast: [node to render, show(message)]. */
export function useToast(): [ReactNode, (msg: string) => void] {
  const [msg, setMsg] = useState<string | null>(null);
  const timer = useRef<number>();
  const show = useCallback((m: string) => {
    setMsg(m);
    window.clearTimeout(timer.current);
    timer.current = window.setTimeout(() => setMsg(null), 3200);
  }, []);
  const node = msg ? (
    <div className="toast">
      <span className="toast-dot" />
      {msg}
    </div>
  ) : null;
  return [node, show];
}

/**
 * Inline help: the explanatory text lives BEHIND this icon, not on the page.
 *
 * # What this component separates
 *
 * An operations screen carries two kinds of text that nothing used to
 * distinguish, both rendered in gray `.note`: what meshcraft OBSERVES about a
 * device — a drift, a skipped port, an address that wasn't written — and what
 * it EXPLAINS — why it is so, what shortcuts exist, what nuance of vocabulary.
 * The first is read every time; the second is read once and clutters the next
 * thousand. Mixed together, they cancel out: both stop being read. This
 * component takes ONLY the second kind.
 *
 * An observation must therefore never go behind this icon, however long:
 * hiding "addressing was not written to the device" costs a site visit.
 *
 * # Why a click and not a hover
 *
 * This product is used on a tablet, in a technical room, sometimes with the
 * keyboard alone. Help that only exists on `:hover` doesn't exist there. The
 * click opens and closes, Escape and an outside click close it, and it's the
 * SAME gesture on every screen — replacing verbosity with an interaction
 * vocabulary to learn would not be a gain.
 *
 * Deliberately no hover IN ADDITION to the click: the bubble would close as
 * soon as the pointer leaves, right after a click had just pinned it, and the
 * gesture would stop having a predictable result.
 *
 * # Why a computed position rather than `position: absolute`
 *
 * These texts live mostly in the side panel of a device sheet, a few hundred
 * pixels wide, itself already in `overflow: auto`. A bubble positioned
 * relative to its parent would get clipped by the panel, or would shift the
 * content. It is therefore placed `fixed`, based on the button's rectangle,
 * brought back within the window, and flipped above the button when there's
 * not enough room below.
 */
export function HelpTip({
  children,
  label,
  /** Aligns the bubble on the right edge of the button — for an icon at the end of a line. */
  align = "left",
  /**
   * The glyph on the button. "?" (default) announces an explanation; "i"
   * announces a STATE the operator should know about (the port panel's
   * "this setting overrides its profile" indicator). Same bubble, same
   * gestures — only the promise on the button differs.
   */
  icon = "?",
  /** Extra class on the button, for state-colored variants (e.g. warn tint). */
  btnClassName,
}: {
  children: ReactNode;
  /** What the help explains, told to the screen reader: "Help — tags". */
  label?: string;
  align?: "left" | "right";
  icon?: ReactNode;
  btnClassName?: string;
}) {
  const { t } = useTranslation();
  const effectiveLabel = label ?? t("common.help");
  const [open, setOpen] = useState(false);
  const [pos, setPos] = useState<{ top: number; left: number; width: number } | null>(null);
  const btnRef = useRef<HTMLButtonElement>(null);
  const bubbleRef = useRef<HTMLDivElement>(null);
  const id = useId();

  useEffect(() => {
    return registerHoverTip(id, () => setOpen(false));
  }, [id]);

  useEffect(() => {
    if (open) claimHoverTip(id);
    else releaseHoverTip(id);
  }, [open, id]);

  const place = useCallback(() => {
    const b = btnRef.current?.getBoundingClientRect();
    if (!b) return;
    const margin = 8;
    const width = Math.min(320, window.innerWidth - 2 * margin);
    const h = bubbleRef.current?.offsetHeight ?? 0;
    let left = align === "right" ? b.right - width : b.left - 4;
    left = Math.max(margin, Math.min(left, window.innerWidth - width - margin));
    // Default: below the button; flip above when the bottom of the viewport is tight —
    // a bubble clipped by the bottom edge is help you cannot read.
    const below = b.bottom + 6;
    const top = h > 0 && below + h > window.innerHeight - margin && b.top - h - 6 > margin
      ? b.top - h - 6
      : below;
    setPos({ top, left, width });
  }, [align]);

  // Two passes: the first render is used to MEASURE the bubble (its content is
  // free-form, its height unknown), the second to position it accordingly.
  useLayoutEffect(() => {
    if (!open) return setPos(null);
    place();
  }, [open, place]);

  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        setOpen(false);
        btnRef.current?.focus();
      }
    };
    const onDoc = (e: MouseEvent) => {
      const t = e.target as Node;
      if (!btnRef.current?.contains(t) && !bubbleRef.current?.contains(t)) setOpen(false);
    };
    // The page scrolls under a `fixed` bubble: repositioning it beats letting
    // it float far from its icon.
    const onMove = () => place();
    document.addEventListener("keydown", onKey);
    document.addEventListener("mousedown", onDoc);
    window.addEventListener("resize", onMove);
    window.addEventListener("scroll", onMove, true);
    return () => {
      document.removeEventListener("keydown", onKey);
      document.removeEventListener("mousedown", onDoc);
      window.removeEventListener("resize", onMove);
      window.removeEventListener("scroll", onMove, true);
    };
  }, [open, place]);

  return (
    <span className="helptip">
      <button
        ref={btnRef}
        type="button"
        className={"helptip-btn" + (open ? " on" : "") + (btnClassName ? " " + btnClassName : "")}
        aria-label={effectiveLabel}
        aria-expanded={open}
        aria-describedby={open ? id : undefined}
        onClick={(e) => {
          // These icons are often placed inside a `<label>` or a clickable
          // row: without this, requesting help would check the box next to it.
          e.preventDefault();
          e.stopPropagation();
          setOpen((v) => !v);
        }}
      >
        {icon}
      </button>
      {open && (
        <div
          ref={bubbleRef}
          id={id}
          role="tooltip"
          className="helptip-bubble"
          style={
            pos
              ? { top: pos.top, left: pos.left, width: pos.width }
              // Before measuring: invisible rather than in the wrong place,
              // otherwise the bubble visibly jumps from one position to another.
              : {
                  visibility: "hidden",
                  top: 0,
                  left: 0,
                  // The SAME width that will be kept: measuring the height at
                  // another width would measure a different bubble.
                  width: Math.min(320, window.innerWidth - 16),
                }
          }
        >
          {children}
        </div>
      )}
      </span>
  );
}

/**
 * Immediate tip bubble — replaces the native HTML `title` attribute for hover
 * help (browser delay ≈ 1 s feels broken on dense UIs).
 *
 * Opens on pointer enter with no delay. Closes when the pointer leaves.
 * On touch / no-hover devices, a click toggles the bubble (same Escape /
 * outside-click close as HelpTip) so the reason remains readable.
 *
 * ALSO OPENS ON KEYBOARD FOCUS — `:focus-visible` on any control inside the
 * wrapper, closing again when focus leaves. UI-GUIDE §3 ("keyboard
 * equivalence is not optional"), and the precondition for a label that is
 * only a tip: ADR-0019 took the mode selector's two words out of the view,
 * so Tab has to be able to put them back. A mouse click is not keyboard
 * focus and a form field is left alone — see the handler.
 *
 * If `text` is empty / null / undefined, children render without a tip
 * (still wrapped when `className` is set, so layout classes apply).
 *
 * The wrapper IS the interactive surface (pass layout classes on `className`);
 * `display: contents` would break anchoring. Prefer `quicktip-inline` /
 * `quicktip-block` / `port-square-tip`.
 */
export function QuickTip({
  text,
  className,
  children,
}: {
  text?: string | null;
  className?: string;
  children: ReactNode;
}) {
  const tip = (text ?? "").trim();
  const [open, setOpen] = useState(false);
  const [pinned, setPinned] = useState(false);
  const [pos, setPos] = useState<{ top: number; left: number; width: number } | null>(null);
  const wrapRef = useRef<HTMLDivElement>(null);
  const bubbleRef = useRef<HTMLDivElement>(null);
  // Whether the bubble that is open was opened by the KEYBOARD. Only then
  // does losing focus close it — otherwise clicking a button inside a tip
  // would shut the bubble while the pointer is still sitting on it.
  const viaFocus = useRef(false);
  const id = useId();

  useEffect(() => {
    return registerHoverTip(id, () => {
      setOpen(false);
      setPinned(false);
    });
  }, [id]);

  const place = useCallback(() => {
    const b = wrapRef.current?.getBoundingClientRect();
    if (!b) return;
    const margin = 8;
    // A CEILING, not a width: the bubble shrinks to its text, so "Routing"
    // gets a bubble the size of "Routing" and not a 320px slab. Clamping
    // against the edge uses the measured width, so a short tip is not pushed
    // left by room it does not take.
    const width = Math.min(320, window.innerWidth - 2 * margin);
    const w = Math.min(bubbleRef.current?.offsetWidth ?? width, width);
    const h = bubbleRef.current?.offsetHeight ?? 0;
    let left = b.left;
    left = Math.max(margin, Math.min(left, window.innerWidth - w - margin));
    const below = b.bottom + 6;
    const top = h > 0 && below + h > window.innerHeight - margin && b.top - h - 6 > margin
      ? b.top - h - 6
      : below;
    setPos({ top, left, width });
  }, []);

  useLayoutEffect(() => {
    if (!open || !tip) return setPos(null);
    place();
  }, [open, place, tip]);

  useEffect(() => {
    if (!open || !tip) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        setOpen(false);
        setPinned(false);
        releaseHoverTip(id);
      }
    };
    const onDoc = (e: MouseEvent) => {
      if (!pinned) return;
      const t = e.target as Node;
      if (!wrapRef.current?.contains(t) && !bubbleRef.current?.contains(t)) {
        setOpen(false);
        setPinned(false);
        releaseHoverTip(id);
      }
    };
    const onMove = () => place();
    document.addEventListener("keydown", onKey);
    document.addEventListener("mousedown", onDoc);
    window.addEventListener("resize", onMove);
    window.addEventListener("scroll", onMove, true);
    return () => {
      document.removeEventListener("keydown", onKey);
      document.removeEventListener("mousedown", onDoc);
      window.removeEventListener("resize", onMove);
      window.removeEventListener("scroll", onMove, true);
    };
  }, [open, pinned, place, tip, id]);

  if (!tip) {
    if (className) return <div className={className}>{children}</div>;
    return <>{children}</>;
  }

  const show = () => {
    claimHoverTip(id);
    setOpen(true);
  };
  const hide = () => {
    if (pinned) return;
    setOpen(false);
    releaseHoverTip(id);
  };

  return (
    <div
      ref={wrapRef}
      className={className}
      data-quicktip={id}
      aria-describedby={open ? id : undefined}
      onPointerEnter={(e) => {
        if (e.pointerType === "touch") return;
        // Nested QuickTips (topology STP card): only the deepest tip under the
        // pointer opens — otherwise parent + child both fire and stack.
        const deepest = (e.target as Element | null)?.closest?.("[data-quicktip]");
        if (deepest && deepest !== wrapRef.current) return;
        show();
      }}
      onPointerLeave={(e) => {
        if (e.pointerType === "touch") return;
        hide();
      }}
      // KEYBOARD EQUIVALENCE IS NOT OPTIONAL — UI-GUIDE §3, and the reason
      // ADR-0019 can take a visible label away: a tip a pointer can read has
      // to be readable by Tab, or the keyboard loses what the mouse gets.
      //
      // Capture, so the focus of any control INSIDE the wrapper counts (the
      // wrapper itself is a div and never takes focus). Gated on
      // `:focus-visible`, so a plain mouse click — which focuses too — does
      // not open a second time over the hover that already handled it. And
      // never for a field: a `<select>`'s description belongs to its label,
      // and a bubble under an open option list is in the way.
      onFocusCapture={(e) => {
        const el = e.target as HTMLElement | null;
        if (!el || typeof el.matches !== "function") return;
        if (el.isContentEditable) return;
        if (el.tagName === "INPUT" || el.tagName === "TEXTAREA" || el.tagName === "SELECT") return;
        let keyboard = false;
        try {
          keyboard = el.matches(":focus-visible");
        } catch {
          /* :focus-visible is unknown to some test DOMs — then no tip, which
             is the same thing that happened before this handler existed */
        }
        if (!keyboard) return;
        viaFocus.current = true;
        show();
      }}
      onBlurCapture={() => {
        if (!viaFocus.current) return;
        viaFocus.current = false;
        hide();
      }}
      onClick={(e) => {
        // Touch / coarse pointer: pin the bubble — hover never fires there.
        if (!window.matchMedia("(hover: hover)").matches) {
          e.preventDefault();
          setPinned((p) => {
            const next = !p;
            if (next) claimHoverTip(id);
            else releaseHoverTip(id);
            setOpen(next);
            return next;
          });
        }
      }}
    >
      {children}
      {open && (
        <div
          ref={bubbleRef}
          id={id}
          role="tooltip"
          className="helptip-bubble quicktip-bubble"
          style={
            pos
              ? { top: pos.top, left: pos.left, maxWidth: pos.width, whiteSpace: "pre-line" }
              : {
                  visibility: "hidden",
                  top: 0,
                  left: 0,
                  maxWidth: Math.min(320, window.innerWidth - 16),
                  whiteSpace: "pre-line",
                }
          }
        >
          {tip}
        </div>
      )}
    </div>
  );
}

/**
 * ActionButton with optional hover tip: when `title` is set, wrap in QuickTip
 * instead of the native HTML attribute (immediate tip, no ~1 s browser delay).
 */
export function ActionButton({
  kind,
  children,
  className = "",
  type = "button",
  title,
  ...rest
}: {
  kind: ActionKind;
  children: ReactNode;
} & ButtonHTMLAttributes<HTMLButtonElement>) {
  const btn = (
    <ActionButtonBase kind={kind} className={className} type={type} {...rest}>
      {children}
    </ActionButtonBase>
  );
  if (title == null || title === "") return btn;
  return (
    <QuickTip text={String(title)} className="quicktip-inline">
      {btn}
    </QuickTip>
  );
}

export function Modal({
  title,
  onClose,
  help,
  wide,
  busy,
  children,
}: {
  title: string;
  onClose: () => void;
  /**
   * What one needs to know to use this modal, behind the title's help. A
   * modal is a form: the paragraph explaining the mechanism is read once and
   * re-read on every opening.
   */
  help?: ReactNode;
  /**
   * A REPORT rather than a form. The 480 px default is sized for a column of
   * labelled inputs; a modal that lists devices with a reason on each row wraps
   * every sentence twice at that width, and the reasons are the content. Used
   * by the fleet credential deployment screen and meant to stay rare — if a
   * modal needs this AND scrolls, it wanted a page.
   */
  wide?: boolean;
  /**
   * A write in flight: Escape, backdrop and any dismissal are REFUSED until
   * it lands. Closing a dialog mid-apply orphans the outcome the operator
   * must read — the guard the port-action overlay carried alone for months.
   */
  busy?: boolean;
  children: ReactNode;
}) {
  const { t } = useTranslation();
  const panelRef = useRef<HTMLDivElement>(null);
  // The keyboard contract (UI-GUIDE §8.3), for every call site at once:
  // Escape and drag-safe outside-mousedown through the dismissal stack…
  useDismissable(true, onClose, { insideRef: panelRef, busy });
  // …and focus: moved in on open, TRAPPED while open, restored on close.
  useEffect(() => {
    const before = document.activeElement;
    const panel = panelRef.current;
    if (panel) {
      const first = panel.querySelector<HTMLElement>(
        "[autofocus], input, select, textarea, button",
      );
      (first ?? panel).focus();
    }
    return () => {
      if (before instanceof HTMLElement) before.focus();
    };
  }, []);
  return (
    <div className="modal-backdrop">
      <div
        ref={panelRef}
        role="dialog"
        aria-modal="true"
        aria-label={title}
        tabIndex={-1}
        className={wide ? "modal modal-wide" : "modal"}
        onKeyDown={(e) => {
          if (e.key !== "Tab") return;
          const panel = panelRef.current;
          if (!panel) return;
          const focusables = panel.querySelectorAll<HTMLElement>(
            "a[href], button:not(:disabled), input:not(:disabled), " +
              "select:not(:disabled), textarea:not(:disabled), [tabindex]:not([tabindex='-1'])",
          );
          if (focusables.length === 0) return;
          const first = focusables[0];
          const last = focusables[focusables.length - 1];
          if (e.shiftKey && document.activeElement === first) {
            e.preventDefault();
            last.focus();
          } else if (!e.shiftKey && document.activeElement === last) {
            e.preventDefault();
            first.focus();
          }
        }}
      >
        <h2>
          {title}
          {help && <HelpTip label={t("common.helpFor", { title }) as string}>{help}</HelpTip>}
        </h2>
        {children}
      </div>
    </div>
  );
}

export function Field({
  label,
  help,
  children,
}: {
  label: string;
  /**
   * What this field decides, and what it doesn't — behind the label's help.
   * This is the right place: a paragraph placed BELOW the field is read
   * after the input, once the question no longer applies.
   */
  help?: ReactNode;
  children: ReactNode;
}) {
  const { t } = useTranslation();
  return (
    <label>
      <span className="lbl">
        {label}
        {help && <HelpTip label={t("common.helpFor", { title: label }) as string}>{help}</HelpTip>}
      </span>
      {children}
    </label>
  );
}

/**
 * Toggle for a setting that CONTROLS others.
 *
 * A checkbox says "this option is active". A toggle says "this mode is
 * engaged", and that's what's needed when enabling it locks other fields: the
 * user must see the switch before wondering why the field next to it is
 * greyed out.
 *
 * `hint` describes the effect, not the state. "The management VLAN doesn't
 * apply" reads well; "untagged = true" doesn't.
 */
export function Switch({
  checked,
  onChange,
  label,
  hint,
  disabled,
}: {
  checked: boolean;
  onChange: (v: boolean) => void;
  label: string;
  hint?: string;
  disabled?: boolean;
}) {
  return (
    <label className={"switch" + (disabled ? " is-disabled" : "")}>
      <input
        type="checkbox"
        role="switch"
        checked={checked}
        disabled={disabled}
        onChange={(e) => onChange(e.target.checked)}
      />
      <span className="switch-track" aria-hidden="true">
        <span className="switch-thumb" />
      </span>
      <span className="switch-text">
        <span className="switch-label">{label}</span>
        {hint && <span className="switch-hint">{hint}</span>}
      </span>
    </label>
  );
}

/** Delete button with two-step confirmation. */
export function DeleteButton({
  onConfirm,
  label,
}: {
  onConfirm: () => void;
  label?: ReactNode;
}) {
  const { t } = useTranslation();
  const [armed, setArmed] = useState(false);
  if (!armed) {
    return (
      <ActionButton kind="delete" className="ghost" onClick={() => setArmed(true)}>
        {label ?? t("common.delete")}
      </ActionButton>
    );
  }
  return (
    <span className="row" style={{ display: "inline-flex", gap: 6 }}>
      <ActionButton kind="confirm" className="danger" onClick={onConfirm}>
        {t("common.confirm")}
      </ActionButton>
      <ActionButton kind="cancel" className="ghost" onClick={() => setArmed(false)}>
        {t("common.cancel")}
      </ActionButton>
    </span>
  );
}

export function ErrorMsg({ error }: { error: unknown }) {
  if (!error) return null;
  return <p className="error">{error instanceof Error ? error.message : String(error)}</p>;
}

/**
 * VLAN selector with search, fed by the site's declared VLANs.
 *
 * The site's list is a CONVENIENCE, not a constraint: a management VLAN may
 * legitimately not appear in it (the default VLAN 1, for example). A free
 * value is therefore accepted, flagged as not declared — rather than
 * forbidding what the device itself accepts.
 */
export function VlanCombo({
  value,
  vlans,
  disabled,
  onChange,
}: {
  value: number;
  vlans: { vlan_id: number; name: string }[];
  disabled?: boolean;
  onChange: (id: number) => void;
}) {
  const { t } = useTranslation();
  const [query, setQuery] = useState("");

  const known = vlans.find((v) => v.vlan_id === value);
  const label = known ? `${known.vlan_id} — ${known.name}` : String(value || "");
  const q = query.trim().toLowerCase();
  const shown = q
    ? vlans.filter(
        (v) => String(v.vlan_id).includes(q) || v.name.toLowerCase().includes(q),
      )
    : vlans;
  // A typed number that isn't in the list can still be proposed — as a real
  // OPTION at the end of the list, so the arrows reach it like any other.
  const typed = Number(query.trim());
  const freeValue =
    Number.isInteger(typed) && typed >= 1 && typed <= 4094 &&
    !vlans.some((v) => v.vlan_id === typed)
      ? typed
      : null;
  const entries = shown.map((v) => v.vlan_id);
  if (freeValue !== null) entries.push(freeValue);

  const picker = usePicker({
    items: entries,
    freeText: true,
    onPick: (id) => {
      onChange(id);
      setQuery("");
    },
  });

  return (
    <div className="vlan-combo" ref={picker.boxRef}>
      <input
        value={picker.open ? query : label}
        placeholder={t("common.searchVlanPlaceholder") as string}
        disabled={disabled}
        onFocus={() => {
          setQuery("");
          picker.openAt();
        }}
        onChange={(e) => {
          setQuery(e.target.value);
          picker.setCursor(0);
          picker.setOpen(true);
        }}
        onKeyDown={(e) => {
          picker.onKeyDown(e);
          if (e.defaultPrevented) return;
          // Enter with nothing under the cursor keeps its historical meaning:
          // a single remaining match, or the free value, is accepted.
          if (e.key === "Enter") {
            e.preventDefault();
            if (shown.length === 1) picker.pick(0);
            else if (freeValue !== null) picker.pick(entries.length - 1);
          }
        }}
      />
      {picker.open && (
        <div className="vlan-combo-list" role="listbox">
          {shown.map((v, i) => (
            <button
              key={v.vlan_id}
              type="button"
              role="option"
              aria-selected={v.vlan_id === value}
              className={
                (v.vlan_id === value ? "sel" : "") + (i === picker.cursor ? " cursor" : "")
              }
              onMouseEnter={() => picker.setCursor(i)}
              onClick={() => picker.pick(i)}
            >
              <b>{v.vlan_id}</b> {v.name}
            </button>
          ))}
          {freeValue !== null && (
            <button
              type="button"
              role="option"
              className={"free" + (picker.cursor === entries.length - 1 ? " cursor" : "")}
              onMouseEnter={() => picker.setCursor(entries.length - 1)}
              onClick={() => picker.pick(entries.length - 1)}
            >
              <b>{freeValue}</b> — {t("common.notDeclaredOnSite")}
            </button>
          )}
          {shown.length === 0 && freeValue === null && (
            <div className="note empty">{t("common.noVlanMatch")}</div>
          )}
        </div>
      )}
      {!known && value > 0 && (
        <div className="note warn-text">
          {t("common.vlanNotDeclared", { vlan: value })}
        </div>
      )}
    </div>
  );
}

/**
 * Arbitration banner for a configuration drift.
 * See docs/en/01-Architecture/Reconciliation §6.
 *
 * Appears ONLY on a drift — the device was modified outside of meshcraft. A
 * meshcraft change not yet applied (blue) calls for no decision: it will
 * resolve on the next apply. Showing a banner on every edit would train
 * people to stop reading it.
 *
 * The two outcomes are deliberately symmetrical: neither one is "the right
 * one". Whether meshcraft or the switch is right is an operational question —
 * the answer belongs to the operator, not the tool.
 */
export function DriftBanner({
  changes,
  onForce,
  onImport,
  busy,
  forceDisabledReason,
}: {
  changes: DriftChange[];
  onForce?: () => void;
  onImport: () => void;
  busy?: boolean;
  /** Set when the connector doesn't yet know how to WRITE the configuration. */
  forceDisabledReason?: string;
}) {
  const { t } = useTranslation();
  const shown = changes.slice(0, 6);
  return (
    <Banner
      tone="act"
      title={t("banners.driftTitle", { count: changes.length })}
      note={t("banners.driftNote")}
      items={[
        ...shown.map((c) => (
          <>
            port.{c.port}.{c.field} : {c.desired} → {c.observed}
          </>
        )),
        ...(changes.length > shown.length
          ? [t("banners.andMore", { count: changes.length - shown.length })]
          : []),
      ]}
      actions={
        <>
          <ActionButton
            kind="apply"
            className="secondary"
            disabled={busy || !!forceDisabledReason}
            title={forceDisabledReason ?? (t("banners.forceConfigTitle") as string)}
            onClick={onForce}
          >
            {t("banners.forceConfig")}
          </ActionButton>
          <ActionButton
            kind="import"
            disabled={busy}
            title={t("banners.importFromDeviceFullTitle") as string}
            onClick={onImport}
          >
            {t("banners.importFromDevice")}
          </ActionButton>
        </>
      }
    />
  );
}

/**
 * A tag placed on a device, with its PROVENANCE
 * (see docs/en/01-Architecture/Tags and credential scope §2.1).
 *
 * `source` is `manual`, `rule:<uuid>`, or one of the TECHNICAL sources the
 * controller reserves for itself (`snmp`, `pwpolicy`). The controller keeps
 * the rule's identifier rather than a plain boolean; on screen, only the
 * family is read, but the type isn't reduced for that.
 */
export interface DeviceTag {
  tag: string;
  source: string;
}

/** Deduced from a rule — therefore recomputed, therefore liable to come back on its own. */
export function isRuleTag(t: DeviceTag): boolean {
  return t.source.startsWith("rule:");
}

/**
 * Deduced from a rule — therefore recomputed, therefore liable to come back
 * on its own. There are now TWO families, and they don't read the same way:
 * one picks the monitoring identity, the other the administration password.
 * Presenting them under a common label would make one look for the wrong
 * cause when either one raises a problem.
 */
function technicalTagWhyKey(source: string): string {
  if (source === "snmp") return "devices.tagWhySnmp";
  if (source === "pwpolicy") return "devices.tagWhyPwpolicy";
  return "devices.tagWhyDefault";
}

/**
 * TECHNICAL tag: set by meshcraft, neither by hand nor by a rule.
 *
 * The predicate is about what the source is NOT, deliberately: the
 * controller may reserve one more source (it already has, twice), and a
 * closed list on screen would present the new one as a freely editable tag —
 * i.e. would invite removing it.
 */
export function isTechnicalTag(t: DeviceTag): boolean {
  return t.source !== "manual" && !isRuleTag(t);
}

/**
 * RESERVED namespaces, as judged by the controller (`snmpv3.ParseTag`,
 * `pwpolicy.IsTechnicalTag`). Knowing it on screen doesn't move the
 * decision — the server remains the sole judge and refuses these scopes on
 * input — but it lets us avoid PROPOSING what will be refused, and mark the
 * identifiers meshcraft manages itself.
 */
export const RESERVED_TAG_PREFIXES = ["snmpv3-", "pwpolicy-"];

/** Does the name belong to a reserved namespace? */
export function isReservedTagName(tag: string): boolean {
  return RESERVED_TAG_PREFIXES.some((p) => tag.startsWith(p));
}

/**
 * Displayed tag, distinguishing THREE provenances — and three were needed.
 *
 * The dashed border isn't a stylistic flourish: a deduced tag removed by hand
 * comes back on the next rule re-evaluation. Without a visible mark or
 * tooltip, that return reads as a bug and the operator starts over.
 *
 * The TECHNICAL tag, meanwhile, offers no removal, and this is the only case
 * where this panel withholds a gesture. The reason isn't reversibility —
 * meshcraft re-applies it on its own — but what happens IN BETWEEN: the
 * identifier this tag targets is no longer applied to anything, so the
 * device falls back to a secret its platform refuses, or to v2c monitoring.
 * Presenting it as a freely editable item invites breaking it.
 */
export function TagBadge({
  tag,
  onRemove,
  busy,
}: {
  tag: DeviceTag;
  /** Absent = read-only (role without write access on the site). */
  onRemove?: () => void;
  busy?: boolean;
}) {
  const { t } = useTranslation();
  const rule = isRuleTag(tag);
  const technical = isTechnicalTag(tag);
  return (
    <QuickTip
      text={
        technical
          ? (t("devices.tagTechnicalTitle", { why: t(technicalTagWhyKey(tag.source)) }) as string)
          : rule
            ? (t("devices.tagRuleTitle") as string)
            : (t("devices.tagManualTitle") as string)
      }
      className="quicktip-inline"
    >
      <span
        className={"badge tag" + (rule ? " tag-rule" : "") + (technical ? " tag-technical" : "")}
      >
        {technical && (
          // The lock says at a glance what the tooltip explains. Without it,
          // the absence of a removal cross would read as a lack of permissions.
          <span aria-hidden="true">🔒</span>
        )}
        {tag.tag}
        {onRemove && !technical && (
          <button
            type="button"
            className="tag-drop"
            disabled={busy}
            aria-label={t("devices.removeTag", { tag: tag.tag }) as string}
            onClick={onRemove}
          >
            ×
          </button>
        )}
      </span>
    </QuickTip>
  );
}

/**
 * Normalizes a tag the way the controller does (`api.normalizeTag`).
 *
 * Doing it client-side too lets us detect a duplicate BEFORE the round trip,
 * and display the tag as it will actually be saved: typing "Régie" and
 * seeing "régie" appear with no explanation would look like the input was
 * lost.
 */
export function normalizeTag(raw: string): string {
  return raw.trim().toLowerCase().replace(/\s+/g, " ");
}

/**
 * Tag input field: applied tags are tokens INSIDE the field, and typing
 * suggests existing ones.
 *
 * Why a token field rather than a list plus an "Apply" button: tags are
 * handled in batches, and a button forces a hand round trip to the mouse for
 * each one. Here everything is done from the keyboard — you type, Enter
 * applies, Backspace removes the last one — and the tokens stay where you
 * read them, instead of living in a list separate from the field that
 * creates them.
 *
 * Autocompletion isn't a comfort feature: suggesting the existing ones is
 * the only thing that prevents "regie" from coexisting with "foh", i.e.
 * two sets where the operator believed they were naming only one.
 */
export function TagInput({
  tags,
  suggestions,
  onAdd,
  onRemove,
  busy,
  readOnly,
  placeholder,
}: {
  tags: DeviceTag[];
  /** Tags known on the site (devices, rules, switch-profile enrollment), for autocomplete. */
  suggestions: string[];
  onAdd: (tag: string) => void;
  onRemove: (tag: DeviceTag) => void;
  busy?: boolean;
  readOnly?: boolean;
  placeholder?: string;
}) {
  const { t } = useTranslation();
  const [draft, setDraft] = useState("");
  const inputRef = useRef<HTMLInputElement>(null);

  const wanted = normalizeTag(draft);
  const has = (tg: string) => tags.some((x) => x.tag === tg);
  // Never suggest what the device already carries: a suggestion that can't be
  // picked is noise. Nor anything belonging to a RESERVED namespace: those
  // tags come from other devices on the site, and suggesting them would
  // invite manually setting a technical directive meshcraft never decided —
  // i.e. filing a device under a secret nothing says its platform accepts.
  const shown = suggestions
    .filter((tg) => !has(tg) && !isReservedTagName(tg) && (wanted === "" || tg.includes(wanted)))
    .slice(0, 8);
  const exact = wanted !== "" && !has(wanted) && !isReservedTagName(wanted);

  const picker = usePicker({
    items: shown,
    freeText: true,
    onPick: (tg) => commit(tg),
  });

  const commit = (tag: string) => {
    const t = normalizeTag(tag);
    if (t === "" || has(t)) return;
    onAdd(t);
    setDraft("");
    picker.setOpen(false);
    inputRef.current?.focus();
  };

  if (readOnly) {
    return (
      <div className="tag-list">
        {tags.map((tg) => (
          <TagBadge key={tg.tag} tag={tg} />
        ))}
        {tags.length === 0 && <span className="note">{t("devices.noTags")}</span>}
      </div>
    );
  }

  return (
    <div className="tag-combo" ref={picker.boxRef}>
      <div
        className="tag-input"
        // Clicking anywhere in the frame gives focus to the input: the field
        // behaves like the field it imitates.
        onClick={() => inputRef.current?.focus()}
      >
        {tags.map((tg) => (
          <TagBadge key={tg.tag} tag={tg} busy={busy} onRemove={() => onRemove(tg)} />
        ))}
        <input
          ref={inputRef}
          value={draft}
          placeholder={tags.length === 0 ? (placeholder ?? (t("devices.addTagPlaceholder") as string)) : ""}
          onFocus={() => picker.openAt()}
          onChange={(e) => {
            setDraft(e.target.value);
            picker.setCursor(0);
            picker.setOpen(true);
          }}
          onKeyDown={(e) => {
            picker.onKeyDown(e);
            if (e.defaultPrevented) return;
            // Enter with nothing under the cursor commits the DRAFT — the
            // create-the-typed-tag half of the field (↑ past the top of the
            // list deliberately parks the cursor there).
            if (e.key === "Enter") {
              e.preventDefault();
              commit(draft);
              return;
            }
            // Backspace on empty input: remove the last token, as any token
            // field does — but only among REMOVABLE tokens. A technical tag
            // erased by reflex would unplug the identifier it targets, with
            // no screen having warned: the keyboard must not be able to do
            // what the removal cross doesn't offer.
            if (e.key === "Backspace" && draft === "" && tags.length > 0) {
              const last = tags.filter((t) => !isTechnicalTag(t)).pop();
              if (last) {
                e.preventDefault();
                onRemove(last);
              }
            }
          }}
        />
      </div>

      {picker.open && (shown.length > 0 || exact) && (
        <div className="vlan-combo-list" role="listbox">
          {shown.map((tg, i) => (
            <button
              key={tg}
              type="button"
              role="option"
              className={i === picker.cursor ? "sel" : ""}
              onMouseEnter={() => picker.setCursor(i)}
              // mousedown rather than click: the field's blur would close the
              // list before the click completes.
              onMouseDown={(e) => {
                e.preventDefault();
                commit(tg);
              }}
            >
              {tg}
            </button>
          ))}
          {exact && !shown.includes(wanted) && (
            <ActionButton
              kind="add"
              type="button"
              className="free"
              onMouseDown={(e) => {
                e.preventDefault();
                commit(wanted);
              }}
            >
              {t("devices.createTag", { tag: wanted })}
            </ActionButton>
          )}
        </div>
      )}
    </div>
  );
}

/**
 * Banner for ports that the apply deliberately skipped — and THE QUESTION it raises.
 *
 * The connector's guard rail refuses to move a port carrying administration:
 * it's right to, but if it does so silently, the operator sees a "applied"
 * configuration and a screen showing something else. The guard rail
 * protects, and looks like a bug. So it's explained right where the question
 * comes up, with its TWO ways out.
 *
 * # WHY TWO OUTCOMES AND NOT ONE
 *
 * A refusal leaves the operator facing an intent the device didn't take.
 * There are only two ways out, and meshcraft can't choose in their place:
 *
 *	FORCE     — they know their topology better than meshcraft and accept the
 *	            risk. With a second confirmation step: the cost of a mistake
 *	            is a site visit, this range having neither SSH nor a serial
 *	            port.
 *	GIVE UP   — the intent will never be applied to this device, and we take
 *	            back what it carries. It's the same gesture as an apply
 *	            failure (see FailedApplyBanner, "give it up by taking back
 *	            what it carries"), and the same word must name it everywhere.
 *
 * Without the second, the banner stayed shown indefinitely: the apply
 * doesn't replay itself, so the gap never resolved, and the only offered
 * gesture was the scary one. A screen that offers only the risky gesture
 * pushes people to take it.
 *
 * # WHY IT LIVES ELSEWHERE THAN ON THE DEVICE SHEET
 *
 * `device` is for pages that show SEVERAL devices. This banner used to live
 * only on the device sheet, while the change is also triggered from the
 * ports page and from a switch profile's preview: the operator would write
 * an intent there, the automatic apply would refuse it a second later, and
 * the question wasn't raised on ANY screen they were looking at. They
 * concluded — rightly — that "it doesn't work".
 */
export function SkippedBanner({
  skipped,
  device,
  onForce,
  onCancel,
  busy,
}: {
  skipped: string[];
  /** The device name, on a page showing several. */
  device?: string;
  onForce?: () => void;
  /** Give up: take back the device's configuration into meshcraft. */
  onCancel?: () => void;
  busy?: boolean;
}) {
  const { t } = useTranslation();
  // Only one gesture armed at a time: two confirmations open side by side,
  // one forcing and the other abandoning, is the surest way to click the
  // wrong one.
  const [armed, setArmed] = useState<"" | "force" | "cancel">("");
  if (skipped.length === 0) return null;
  return (
    <Banner
      tone="act"
      title={
        <>
          {device ? t("banners.deviceQuoted", { device }) + " — " : ""}
          {t("banners.skippedTitle", { count: skipped.length })}
        </>
      }
      note={t("banners.skippedNote")}
      items={skipped}
      actions={
        (onForce || onCancel) && (
        <>
          {armed === "" && onForce && (
            <ActionButton kind="confirm" className="secondary" onClick={() => setArmed("force")}>
              {t("banners.forceAnyway")}
            </ActionButton>
          )}
          {armed === "" && onCancel && (
            <ActionButton
              kind="cancel"
              className="ghost"
              title={t("banners.giveUpChangeTitle") as string}
              onClick={() => setArmed("cancel")}
            >
              {t("banners.giveUpChange")}
            </ActionButton>
          )}
          {armed === "force" && onForce && (
            <>
              <ActionButton kind="confirm" className="danger" disabled={busy} onClick={onForce}>
                {t("banners.confirmKnowTopology")}
              </ActionButton>
              <ActionButton kind="cancel" className="ghost" onClick={() => setArmed("")}>
                {t("common.goBack")}
              </ActionButton>
            </>
          )}
          {armed === "cancel" && onCancel && (
            <>
              <ActionButton kind="confirm" className="secondary" disabled={busy} onClick={onCancel}>
                {t("banners.confirmRevertDevice")}
              </ActionButton>
              <ActionButton kind="cancel" className="ghost" onClick={() => setArmed("")}>
                {t("common.goBack")}
              </ActionButton>
            </>
          )}
        </>
        )
      }
    />
  );
}

export function PillPicker({
  value,
  options,
  placeholder,
  onChange,
  disabled,
}: {
  value: string;
  options: string[];
  placeholder?: string;
  onChange: (v: string) => void;
  disabled?: boolean;
}) {
  const { t } = useTranslation();
  const [draft, setDraft] = useState("");

  // The deferred `blur` runs OUTSIDE the render that scheduled it: it can't
  // trust captured variables. Two reasons, and both bite.
  //
  // First, Enter: validating clears `draft` then triggers the save, which
  // sets the field to `disabled` — and the browser removes focus from a
  // field being disabled. The `blur` therefore fires AFTER the fact, and
  // React serves it with the PREVIOUS render's props (it updates the DOM
  // before reattaching the new props to it): the already-validated text is
  // still there, and it would get saved a second time — a multi-second
  // round trip for nothing.
  //
  // Then unmounting: as soon as the value arrives, the field gives way to
  // the chip. A timer still armed would set state on a component that no
  // longer renders a field.
  //
  // Refs hold the CURRENT state rather than that of a stale render.
  const draftRef = useRef("");
  const valueRef = useRef(value);
  valueRef.current = value;
  const blurTimer = useRef<number>();
  useEffect(() => () => window.clearTimeout(blurTimer.current), []);

  const matches = options
    .filter((o) => o && o.toLowerCase().includes(draft.trim().toLowerCase()))
    .slice(0, 8);

  const updateDraft = (v: string) => {
    draftRef.current = v;
    setDraft(v);
  };

  // The cursor opens at -1: no suggestion selected, so the Enter key
  // validates the typed text. The arrows alone move to a suggestion.
  const picker = usePicker({
    items: matches,
    freeText: true,
    initialCursor: -1,
    onPick: (o) => commit(o),
    // Escape abandons the draft; a click elsewhere, or Tab away, means
    // "moving on" and commits it. The blur timer below keeps covering the
    // focus loss that is neither — no mousedown, no new focus target.
    onDismiss: (reason) => {
      if (reason === "outside") {
        commit(draftRef.current);
      } else {
        window.clearTimeout(blurTimer.current);
        updateDraft("");
      }
    },
  });

  const commit = (v: string) => {
    // An explicit validation always takes priority over a pending `blur`.
    window.clearTimeout(blurTimer.current);
    const clean = v.trim();
    updateDraft("");
    picker.setOpen(false);
    picker.setCursor(-1);
    if (clean && clean !== valueRef.current) onChange(clean);
  };

  if (value) {
    return (
      // While saving, the chip already shows what was just typed, but it
      // isn't confirmed yet: dimming it says so, and avoids a missing "×"
      // looking like a malfunction.
      <span className="chip-row" style={disabled ? { opacity: 0.55 } : undefined}>
        <QuickTip
          text={disabled ? (t("banners.pillSaving") as string) : undefined}
          className="quicktip-inline"
        >
          <span
            className="chip"
            style={{ fontSize: 11, padding: "2px 8px" }}
          >
            {value}
          </span>
        </QuickTip>
        {!disabled && (
          <QuickTip text={t("banners.pillRemoveTitle") as string} className="quicktip-inline">
            <button
              type="button"
              className="ghost icon-btn"
              aria-label={t("banners.pillRemove", { value }) as string}
              style={{ minHeight: 20, padding: "0 6px", fontSize: 12 }}
              onClick={() => onChange("")}
            >
              ×
            </button>
          </QuickTip>
        )}
      </span>
    );
  }

  return (
    <div className="pill-anchor" ref={picker.boxRef}>
      <input
        value={draft}
        disabled={disabled}
        placeholder={placeholder}
        className="pill-input"
        onChange={(e) => {
          updateDraft(e.target.value);
          picker.setCursor(-1);
          picker.setOpen(true);
        }}
        onFocus={() => picker.openAt()}
        // `blur` remains a safety net — someone typing then clicking
        // elsewhere clearly meant to submit — but it's no longer the ONLY
        // path. Delayed, otherwise it would fire before a suggestion click.
        onBlur={() => {
          window.clearTimeout(blurTimer.current);
          blurTimer.current = window.setTimeout(
            () => commit(draftRef.current),
            120,
          );
        }}
        onKeyDown={(e) => {
          picker.onKeyDown(e);
          if (e.defaultPrevented) return;
          // Enter with no suggestion under the cursor validates the draft.
          if (e.key === "Enter") {
            e.preventDefault();
            commit(draft);
          }
        }}
      />
      {picker.open && matches.length > 0 && (
        <div className="pill-suggest" role="listbox">
          {matches.map((o, i) => (
            <button
              key={o}
              type="button"
              role="option"
              className={i === picker.cursor ? "on" : undefined}
              onMouseEnter={() => picker.setCursor(i)}
              // `onMouseDown` rather than `onClick`: the click arrives after
              // `blur`, which would already have closed the list.
              onMouseDown={(e) => {
                e.preventDefault();
                commit(o);
              }}
            >
              {o}
            </button>
          ))}
        </div>
      )}
    </div>
  );
}

/**
 * Banner for an INCOMPLETE apply: it did happen, and discrepancies remain.
 *
 * The status light used to stay blue ("in progress") for two situations that
 * nothing distinguished on screen. An intent just entered will be applied
 * within seconds — justified silence. An intent an apply has already gone
 * through, leaving discrepancies, will NEVER move again on its own: meshcraft
 * only re-applies after a change to the intent, and there won't be one.
 *
 * The orange blinking acknowledgement light now covers that second case (see
 * deviceDot / apply_stalled). This banner is the place to act.
 */
export function IncompleteApplyBanner({
  changes,
  onRetry,
  onImport,
  busy,
}: {
  changes: DriftChange[];
  onRetry: () => void;
  onImport: () => void;
  busy?: boolean;
}) {
  const { t } = useTranslation();
  const shown = changes.slice(0, 6);
  return (
    <Banner
      tone="act"
      title={t("banners.incompleteTitle", { count: changes.length })}
      note={t("banners.incompleteNote")}
      items={[
        ...shown.map((c) => (
          <>
            port.{c.port}.{c.field} : {c.desired} → {c.observed}
          </>
        )),
        ...(changes.length > shown.length
          ? [t("banners.andMore", { count: changes.length - shown.length })]
          : []),
      ]}
      actions={
        <>
          <ActionButton
            kind="refresh"
            className="secondary"
            disabled={busy}
            title={t("banners.forceConfigTitle") as string}
            onClick={onRetry}
          >
            {t("banners.reapplyConfig")}
          </ActionButton>
          <ActionButton
            kind="import"
            disabled={busy}
            title={t("banners.importFromDeviceTitle") as string}
            onClick={onImport}
          >
            {t("banners.importFromDevice")}
          </ActionButton>
        </>
      }
    />
  );
}

/**
 * Banner for a FAILED apply.
 *
 * meshcraft never replays an apply in a loop, and that's deliberate:
 * hammering away at a device that refuses is the best way to lock it out.
 * But the flip side of that caution is that there must be a place to take
 * back control — without it, the configuration stayed pending indefinitely,
 * the automatic apply having already run, and the screen offered NO gesture.
 *
 * Two outcomes, symmetrical as with drift, because a failure has two
 * possible causes that can't be told apart from here: a device momentarily
 * unreachable — we retry — or an intent this device will never accept — we
 * give it up by taking back what it carries. Choosing on the operator's
 * behalf would amount to guessing which of the two it is.
 */
export function FailedApplyBanner({
  error,
  onRetry,
  onImport,
  busy,
}: {
  error: string;
  onRetry: () => void;
  onImport: () => void;
  busy?: boolean;
}) {
  const { t } = useTranslation();
  return (
    <Banner
      tone="act"
      title={t("banners.failedTitle")}
      note={t("banners.failedNote")}
      items={[error]}
      actions={
        <>
          <ActionButton
            kind="refresh"
            className="secondary"
            disabled={busy}
            title={t("banners.retryApplyTitle") as string}
            onClick={onRetry}
          >
            {t("banners.retryApply")}
          </ActionButton>
          <ActionButton
            kind="import"
            disabled={busy}
            title={t("banners.abandonAndImportTitle") as string}
            onClick={onImport}
          >
            {t("banners.abandonAndImport")}
          </ActionButton>
        </>
      }
    />
  );
}

/**
 * Banner for intents the PLATFORM doesn't know how to write.
 *
 * INFORMATIVE tone, and that's the heart of it: nothing abnormal is
 * happening. Everything the connector knows how to apply is applied, the
 * device is up to date and counted as such — simply, part of what the
 * interface lets you enter has no written equivalent on this range.
 *
 * Why not a warning: an orange banner calls for an action, and there is none
 * to take. Why not nothing at all: without it, the operator sees
 * "configuration applied", checks PoE, and discovers months later on site
 * that the port was never powered.
 *
 * No "force" button, deliberately: a skipped port can be forced, an
 * unsupported intent gets implemented. Offering a button that would change
 * nothing would be worse than offering none.
 */
/**
 * What no force will change: the platform lacks the knob, or the device itself
 * declined the write on the verifying re-read. Deliberately has NO force
 * button — that is the entire difference with SkippedBanner, whose entries an
 * operator can lift. Offering force here would be the "button that leads
 * nowhere" the capability model exists to prevent.
 */
export function RefusedBanner({ notes }: { notes: string[] }) {
  const { t } = useTranslation();
  if (notes.length === 0) return null;
  return (
    <Banner
      tone="info"
      title={t("banners.refusedTitle", { count: notes.length })}
      note={t("banners.refusedNote")}
      items={notes}
    />
  );
}

export function UnsupportedBanner({ notes }: { notes: string[] }) {
  const { t } = useTranslation();
  if (notes.length === 0) return null;
  return (
    <Banner
      tone="info"
      title={t("banners.unsupportedTitle", { count: notes.length })}
      note={t("banners.unsupportedNote")}
      items={notes}
    />
  );
}

/**
 * Banner for the CONNECTOR'S NOTES: what it understood about the device by
 * REREADING it after the apply, and that the standard model can't say.
 *
 * THE BANNER THAT WAS MISSING, and its absence was the worst of this
 * screen's three cases of silence. The controller computed these sentences,
 * saved them in `last_apply.notes` — the comment on `rememberApply` even
 * says "the screen must be able to say it in the same place it says the
 * skipped ports" — and no screen read them. They were produced, stored, and
 * discarded.
 *
 * What they carry, on the two MikroTik platforms: a VLAN configuration can
 * be PRESENT AND INERT. On SwOS, a port whose VLAN mode is "disabled"
 * filters nothing — its native VLAN and membership are written and have no
 * effect on a single frame; a port in "Force VLAN ID" puts all its traffic
 * into its native VLAN, tags included, and is therefore not the trunk the
 * facade draws. On RouterOS, a bridge whose `vlan-filtering` is off lets
 * everything through everywhere, and `ingress-filtering: false` only
 * filters on egress.
 *
 * In other words: an apply can be technically successful, VERIFIED BY
 * REREADING, counted compliant, and have no effect at all. This is the only
 * place where that can still be said — after this, there's only the field.
 *
 * # WHAT A NOTE IS NOT, because this panel filled up with both
 *
 * The scarce thing here is the operator's attention, and the panel spends it on
 * whatever a connector appends. Two kinds of sentence were spending it for
 * nothing, and the Instant On spanning-tree path produced three of them in a row
 * on a nominal apply:
 *
 *   - an ECHO of the request — "SpanningTreeGlobalParam enabled=1
 *     STPOperationMode=3", "MSTP region name="site-exec" revision=1".
 *     Built from the plan's own fields, so it observes nothing, and already
 *     recorded as a step by the write itself. Two copies of the request, none of
 *     the device;
 *   - a NON-EVENT — "MST instance 1 already maps 10-11,42,60,98-100 — nothing
 *     written". The device agreed with the plan, which is the nominal outcome of
 *     every idempotent apply. Presenting it as something to read is what teaches
 *     an operator to skim the panel, and the lines worth reading are in it.
 *
 * The rule the connectors now follow: a note earns its place by carrying a fact
 * the step transcript cannot — what the device WAS carrying, or that a written
 * configuration is inert. "MST instance 1 VLANs set to 10-11,42,60,98-100
 * (device had 42,98-100)" qualifies, and says the device was in a different MST
 * region than the plan. "Nothing to do" does not.
 *
 * INFORMATIVE tone and not a warning, like the banner above: there's no
 * button to offer. None of these sentences are fixed by "forcing" — neither
 * enabling `vlan-filtering` on a bridge, nor waking up a VLAN mode a guard
 * rail refused to touch, is a gesture meshcraft takes in place of a human
 * looking at it. These are sentences written by the connector and displayed
 * as-is: the screen draws no decision from them, exactly like the API layer
 * draws none.
 */
export function ConnectorNotesBanner({
  notes,
  /**
   * The device is UNREACHABLE. These sentences describe what the connector
   * read at the last apply, on hardware nobody can reach now — so they are
   * folded rather than dropped.
   *
   * Dropped would be the easy call and the wrong one: on an unreachable
   * device this is the last thing known about it, and "VLAN filtering is off
   * on the bridge" is exactly the kind of sentence someone reads while
   * working out why it went dark. But left open it occupies the top of a
   * screen whose only news is that the device is gone — above the banner
   * that says so.
   *
   * Folded, the summary keeps the count and adds that the reading is old:
   * a fold that does not name what it hides is indistinguishable from a
   * banner that silently stopped rendering.
   */
  collapsed,
}: {
  notes: string[];
  collapsed?: boolean;
}) {
  const { t } = useTranslation();
  const shown = notes.filter(
    (n) => !n.startsWith("Administration observed at "),
  );
  if (shown.length === 0) return null;
  const title = t("banners.connectorNotesTitle", { count: shown.length });
  if (collapsed) {
    return (
      <details className="info-banner connector-notes-fold">
        <summary className="connector-notes-sum">
          <span className="info-title">{title}</span>
          <span className="note">{t("banners.connectorNotesStale")}</span>
        </summary>
        <div className="drift-text">
          <div className="note">{t("banners.connectorNotesNote")}</div>
          <ul>
            {shown.map((n, i) => (
              <li key={i}>{n}</li>
            ))}
          </ul>
        </div>
      </details>
    );
  }
  return (
    <Banner
      tone="info"
      title={title}
      note={t("banners.connectorNotesNote")}
      items={shown}
    />
  );
}
