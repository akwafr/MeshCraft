// THE SELECTED AGGREGATE, in the side panel — where the port panel goes when
// a socket is clicked, because an aggregate IS an interface (ADR-0007).
//
// This is the half of the old `LagPanel` tile that had to survive the move:
// which members, which mode, converged or not, and what VLAN mapping the
// aggregate carries. The ADR says so in as many words — "its diagnostic
// content must reappear in the side panel, otherwise the redesign loses
// information". The half that did NOT survive is the permanent empty
// card on the majority of devices that have no aggregate at all.
//
// It holds no draft and sends nothing: editing opens the builder, dissolving
// asks the page to apply the list without this aggregate. One write path for
// aggregates, and it is the page's.
import { useState } from "react";
import { useTranslation } from "react-i18next";

import { ActionButton } from "../../components";
import { ConfirmDialog } from "../../ui/ConfirmDialog";
import { lagColor, type Lag } from "../../lags";

export function LagSidePanel({
  lag,
  /** Read on the device and not yet adopted as an intent. */
  imported,
  canWrite,
  busy,
  onEdit,
  onDissolve,
  onClear,
}: {
  lag: Lag;
  imported: boolean;
  canWrite: boolean;
  busy: boolean;
  onEdit: () => void;
  onDissolve: () => void;
  onClear: () => void;
}) {
  const { t } = useTranslation();
  const [confirming, setConfirming] = useState(false);
  return (
    <div className="lag-side">
      <div className="lag-side-head">
        <span className="lag-swatch" style={{ background: lagColor(lag.name) }} />
        <strong>{lag.name}</strong>
        <span className="chip">{lag.mode === "lacp" ? "LACP" : t("lag.modeStatic")}</span>
        {imported && <span className="chip">{t("lag.imported")}</span>}
      </div>

      {/* CONVERGENCE, stated and never inferred. `running` absent means the
          device was not read for it — which is not "down". The sentence says
          which of the three it is rather than painting a dot that cannot
          distinguish them. */}
      <div className="kv">
        <span className="k">{t("lag.state")}</span>
        <span>
          {lag.running === undefined
            ? t("lag.notObserved")
            : lag.running
              ? t("lag.converged")
              : t("lag.notConverged")}
        </span>
      </div>

      <div className="kv">
        <span className="k">{t("lag.members")}</span>
        <span className="lag-side-members">
          {lag.members.map((m) => (
            <span key={m} className="chip">
              {m}
            </span>
          ))}
        </span>
      </div>

      <div className="kv">
        <span className="k">VLAN</span>
        <span>
          {lag.vlan?.mode
            ? lag.vlan.mode === "trunk"
              ? `trunk · PVID ${lag.vlan.pvid ?? 1} · ${(lag.vlan.tagged ?? []).join(", ") || "—"}`
              : `access · VLAN ${lag.vlan.pvid ?? 1}`
            : t("lag.vlanUnmanaged")}
        </span>
      </div>

      {/* THE FALLBACK IS SHOWN ONLY WHEN IT IS SET. Its absence here is not
          "fallback off" — a platform that cannot do it never carries the
          field, and a row reading "fallback: none" on such a device would
          suggest a switch that could be flipped. */}
      {!!lag.fallback && (
        <div className="kv">
          <span className="k">{t("lag.fallback")}</span>
          <span>{t("lag.fallbackOn", { vlan: lag.fallback })}</span>
        </div>
      )}

      {lag.raw && (
        <div className="kv">
          <span className="k">{t("lag.deviceName")}</span>
          <span className="mono">{lag.raw}</span>
        </div>
      )}

      <div className="lag-side-actions">
        {canWrite && (
          <>
            <ActionButton kind="edit" onClick={onEdit} disabled={busy}>
              {t("common.edit")}
            </ActionButton>
            <ActionButton kind="delete" onClick={() => setConfirming(true)} disabled={busy}>
              {t("lag.dissolve")}
            </ActionButton>
          </>
        )}
        <ActionButton kind="close" onClick={onClear}>
          {t("lag.clearSelection")}
        </ActionButton>
      </div>

      {confirming && (
        <ConfirmDialog
          title={t("lag.dissolveTitle")}
          body={t("lag.dissolveConfirm", { name: lag.name })}
          confirmLabel={t("lag.dissolve")}
          busy={busy}
          onConfirm={() => {
            setConfirming(false);
            onDissolve();
          }}
          onCancel={() => setConfirming(false)}
        />
      )}
    </div>
  );
}
