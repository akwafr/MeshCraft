// The modal form of "are you sure?" — for destructive acts too heavy for
// DeleteButton's inline two-step (a whole LAG, a fleet gesture). Built on
// Modal, so Escape, focus and the busy refusal are the contract's, not
// this file's. window.confirm is the defect this retires: a native dialog
// the keyboard contract, the palette and i18n all cannot reach.

import { useTranslation } from "react-i18next";
import { ActionButton, Modal } from "../components";
import type { ActionKind } from "../components";

export function ConfirmDialog({
  title,
  body,
  confirmKind = "delete",
  confirmLabel,
  busy,
  onConfirm,
  onCancel,
}: {
  title: string;
  /** The consequence, stated: what will happen, to what, named. */
  body: React.ReactNode;
  confirmKind?: ActionKind;
  confirmLabel: string;
  busy?: boolean;
  onConfirm: () => void;
  onCancel: () => void;
}) {
  const { t } = useTranslation();
  return (
    <Modal title={title} onClose={onCancel} busy={busy}>
      <div className="note">{body}</div>
      <div className="actions">
        <ActionButton kind="cancel" className="ghost" onClick={onCancel} disabled={busy}>
          {t("common.cancel")}
        </ActionButton>
        <ActionButton kind={confirmKind} className="danger" onClick={onConfirm} disabled={busy} autoFocus>
          {confirmLabel}
        </ActionButton>
      </div>
    </Modal>
  );
}
