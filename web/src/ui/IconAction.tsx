import { useState, type ButtonHTMLAttributes } from "react";
import { useTranslation } from "react-i18next";
import { ActionIcon, type ActionKind } from "../ActionButton";
import { QuickTip } from "../components";

/**
 * THE ICON-ONLY ROW ACTION, for a table whose action column had become the
 * widest thing on the page.
 *
 * A /config row used to spell every verb out — "Sites", "Ranges",
 * "Migrate", "Revoke", "Delete" on one proxy line — and five
 * labelled buttons left the columns that actually vary (a proxy's sites, a
 * credential's rotation date) squeezed into what was left. The verb moves
 * into the glyph; it does NOT leave the product.
 *
 * UI-GUIDE §3 fixes where it goes instead, and both halves are mandatory:
 *
 *   - `aria-label` — the accessible name. A screen reader announces
 *     "Delete, button", exactly as before.
 *   - `QuickTip` — the sighted reader's label, shown with NO delay. The
 *     native `title=` is forbidden here precisely because its ~1 s pause on
 *     a row of five glyphs reads as an interface that has stopped
 *     responding.
 *
 * It lives in `ui/` because that is the one directory the bare-button
 * ratchet exempts: an icon-only control has no `<span>` label to wrap, so it
 * is a real `<button className="icon-btn">` and the rule has to be written
 * somewhere. Everywhere else, use this.
 *
 * WHEN NOT TO USE IT. An icon compresses a verb; it cannot compress a fact.
 * Keep the words when the control shows a STATE rather than an action (a
 * button reading "Active" / "Disabled"), when its label changes with the
 * role ("Edit" for an engineer, "View" for a viewer — there is no glyph
 * for "consult without changing", and a pencil would promise an edit the
 * role refuses), when it is the page's primary gesture, or when it is alone
 * in its cell with no neighbouring icon to give it context.
 */
export function IconAction({
  kind,
  label,
  className = "ghost",
  type = "button",
  ...rest
}: {
  kind: ActionKind;
  /** The verb. Goes to `aria-label` AND to the tip — never to the screen. */
  label: string;
} & ButtonHTMLAttributes<HTMLButtonElement>) {
  const cls = className.includes("icon-btn") ? className : `${className} icon-btn`.trim();
  return (
    <QuickTip text={label} className="quicktip-inline">
      <button type={type} aria-label={label} className={cls} {...rest}>
        <ActionIcon kind={kind} size={15} />
      </button>
    </QuickTip>
  );
}

/**
 * The two-step delete, as icons — `DeleteButton`'s shape for a row that has
 * gone iconic.
 *
 * The armed step swaps the GLYPH (trash → check) and not merely the colour,
 * because a destructive confirmation that announces itself in red alone is
 * invisible to the operator who cannot see red. Both steps carry their verb
 * in `aria-label` and in the tip, so "Confirmer" is still read — and the
 * cell stays two icons wide instead of growing by the width of two words
 * the instant a row is armed.
 */
export function IconDeleteButton({
  onConfirm,
  label,
}: {
  onConfirm: () => void;
  label?: string;
}) {
  const { t } = useTranslation();
  const [armed, setArmed] = useState(false);
  if (!armed) {
    return (
      <IconAction
        kind="delete"
        label={label ?? (t("common.delete") as string)}
        onClick={() => setArmed(true)}
      />
    );
  }
  return (
    <>
      <IconAction
        kind="confirm"
        className="danger"
        label={t("common.confirm") as string}
        onClick={onConfirm}
      />
      <IconAction
        kind="cancel"
        label={t("common.cancel") as string}
        onClick={() => setArmed(false)}
      />
    </>
  );
}
