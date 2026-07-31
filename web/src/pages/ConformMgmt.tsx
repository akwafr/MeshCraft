import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState, type CSSProperties } from "react";
import { useTranslation } from "react-i18next";
import { post, type Device } from "../api";
import { ActionButton, ErrorMsg, Modal } from "../components";
import { MgmtMigrateModal } from "./MgmtMigrate";

type JumpResult = {
  ok: boolean;
  error?: string;
  backup_bytes?: number;
};

/** Platforms that can move management toward the site plan (safe migrate or best-effort jump). */
export function canConformMgmt(dev: Device): boolean {
  const caps = dev.mgmt_addressing?.caps;
  return !!(caps?.migrate_vlan || caps?.jump_vlan);
}

/**
 * One operator gesture for devices outside the site addressing plan:
 * dual-path migrate when available, otherwise a single-address jump with backup.
 */
export function ConformMgmtButton({
  device,
  className,
  style,
  onToast,
}: {
  device: Device;
  className?: string;
  style?: CSSProperties;
  onToast?: (msg: string) => void;
}) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  if (!canConformMgmt(device)) return null;

  return (
    <>
      <ActionButton
        kind="migrate"
        className={className ?? "secondary"}
        style={style}
        title={t("conformMgmt.title")}
        onClick={() => setOpen(true)}
      >
        {t("conformMgmt.button")}
      </ActionButton>
      {open &&
        (device.mgmt_addressing?.caps?.migrate_vlan ? (
          <MgmtMigrateModal device={device} onClose={() => setOpen(false)} />
        ) : (
          <ConformJumpModal
            device={device}
            onClose={() => setOpen(false)}
            onToast={onToast}
          />
        ))}
    </>
  );
}

/** Best-effort conform on single-address platforms (SwOS-shaped). */
function ConformJumpModal({
  device,
  onClose,
  onToast,
}: {
  device: Device;
  onClose: () => void;
  onToast?: (msg: string) => void;
}) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const m = device.mgmt;

  const jump = useMutation({
    mutationFn: () =>
      post<JumpResult>(`/api/devices/${device.id}/mgmt-addressing/jump`, {
        confirmed: true,
        use_plan_vlan: true,
      }),
    onSuccess: (r) => {
      qc.invalidateQueries({ queryKey: ["devices", device.site_id] });
      if (r.ok) {
        onToast?.(
          t("conformMgmt.jumpedToast", { bytes: r.backup_bytes ?? 0 }),
        );
        onClose();
      }
    },
  });

  return (
    <Modal title={t("conformMgmt.modalTitle", { name: device.name })} onClose={onClose}>
      <table style={{ marginBottom: 12 }}>
        <tbody>
          <tr>
            <td className="note">{t("mgmtMigrate.currently")}</td>
            <td className="mono">{device.mgmt_ip || "—"}</td>
          </tr>
          <tr>
            <td className="note">{t("mgmtMigrate.sitePlan")}</td>
            <td className="mono">
              {t("conformMgmt.planSummary", {
                vlan: m?.target_vlan ?? "—",
                subnet: m?.target_subnet ?? "—",
              })}
            </td>
          </tr>
          {m?.reason && (
            <tr>
              <td className="note">{t("mgmtMigrate.drift")}</td>
              <td>{m.reason}</td>
            </tr>
          )}
        </tbody>
      </table>

      <p className="note" style={{ marginBottom: 12 }}>
        {t("conformMgmt.jumpHelp")}
      </p>

      <div className="row" style={{ gap: 8, flexWrap: "wrap" }}>
        <ActionButton
          kind="confirm"
          disabled={jump.isPending}
          title={t("conformMgmt.title")}
          onClick={() => jump.mutate()}
        >
          {jump.isPending ? t("conformMgmt.conforming") : t("conformMgmt.confirm")}
        </ActionButton>
        <ActionButton
          kind="cancel"
          className="ghost"
          disabled={jump.isPending}
          onClick={onClose}
        >
          {t("common.cancel")}
        </ActionButton>
      </div>
      <ErrorMsg error={jump.error} />
      {jump.data && !jump.data.ok && jump.data.error && (
        <p style={{ color: "var(--danger)", fontSize: 12, marginTop: 8 }}>
          {jump.data.error}
        </p>
      )}
    </Modal>
  );
}
