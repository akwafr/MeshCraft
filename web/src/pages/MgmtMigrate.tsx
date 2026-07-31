import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { post, type Device } from "../api";
import { ActionButton, ErrorMsg, Field, Modal } from "../components";
import { vlanPathPreview } from "../network";

/** What a migration step renders. `ok` false is not an API failure: the device refused, and its reason is in `error`. */
type MigrateResult = {
  ok: boolean;
  error?: string;
  vlan?: number;
  address?: string;
  note?: string;
};

function Verdict({ r }: { r: MigrateResult }) {
  const { t } = useTranslation();
  return (
    <p style={{ color: r.ok ? "var(--ok)" : "var(--danger)", fontSize: 12 }}>
      {r.ok
        ? (r.address ? t("mgmtMigrate.addressObtained", { address: r.address }) : "") +
          (r.note ?? t("mgmtMigrate.done"))
        : t("mgmtMigrate.deviceRefused", { error: r.error })}
    </p>
  );
}

/**
 * Conform management addressing to the site plan (dual-path migrate).
 * Opens a guided preflight — never a one-click write.
 */
export function MgmtMigrateModal({
  device,
  onClose,
  targetVlan,
}: {
  device: Device;
  onClose: () => void;
  /** Override site-plan VLAN when the operator picked one in Management network. */
  targetVlan?: number;
}) {
  const { t } = useTranslation();
  const m = device.mgmt;
  const vlan = targetVlan && targetVlan > 0 ? targetVlan : m?.target_vlan;
  const gateway = (m?.target_subnet ?? "").split("/")[0].replace(/\.\d+$/, ".1");
  const [host, setHost] = useState(gateway);
  const body = vlan && vlan > 0 ? { vlan_id: vlan } : {};
  // Operator-picked VLAN uses access|trunk path preview; plan conform keeps controller path_state.
  const pathState =
    targetVlan && targetVlan > 0
      ? vlanPathPreview(device, targetVlan)
      : m?.path_state;
  const pathReason =
    targetVlan && targetVlan > 0
      ? pathState === "absent"
        ? t("mgmtAddressing.vlanPathAbsent", { vlan: targetVlan })
        : pathState === "unknown"
          ? t("mgmtAddressing.vlanPathUnknown", { vlan: targetVlan })
          : t("mgmtAddressing.vlanPathReady", { vlan: targetVlan })
      : m?.path_reason;

  const check = useMutation({
    mutationFn: () =>
      post<{ reachable: boolean; route_exists: boolean; verdict: string }>(
        "/api/discovery/reach-check", { host: host.trim() }),
  });

  const qc = useQueryClient();
  const refresh = () => qc.invalidateQueries({ queryKey: ["devices", device.site_id] });
  const prepare = useMutation({
    mutationFn: () => post<MigrateResult>(`/api/devices/${device.id}/mgmt-migrate/prepare`, body),
    onSuccess: refresh,
  });
  const commit = useMutation({
    mutationFn: () => post<MigrateResult>(`/api/devices/${device.id}/mgmt-migrate/commit`, body),
    onSuccess: refresh,
  });
  const persist = useMutation({
    mutationFn: () => post<MigrateResult>(`/api/devices/${device.id}/mgmt-migrate/persist`, body),
    onSuccess: refresh,
  });
  const busy = prepare.isPending || commit.isPending || persist.isPending;

  const taggedLabel = m?.target_tagged ? t("mgmtMigrate.tagged") : t("mgmtMigrate.native");
  const sourceLabel = m?.target_source === "meshcraft"
    ? t("mgmtMigrate.allocatedAddress")
    : t("mgmtMigrate.externalDhcp");

  return (
    <Modal title={t("conformMgmt.modalTitle", { name: device.name })} onClose={onClose}>
      <table style={{ marginBottom: 12 }}>
        <tbody>
          <tr><td className="note">{t("mgmtMigrate.currently")}</td>
            <td className="mono">{device.mgmt_ip || "—"}</td></tr>
          <tr><td className="note">{t("mgmtMigrate.sitePlan")}</td>
            <td className="mono">
              {t("mgmtMigrate.planSummary", {
                vlan: vlan,
                tagged: taggedLabel,
                subnet: m?.target_subnet,
                source: sourceLabel,
              })}
            </td></tr>
          <tr><td className="note">{t("mgmtMigrate.drift")}</td>
            <td style={{ color: "var(--warn)" }}>{m?.reason}</td></tr>
        </tbody>
      </table>

      <div className="card">
        <div className="kicker">{t("mgmtMigrate.step1Title")}</div>
        <p className="note">{t("mgmtMigrate.step1Help")}</p>
        <div className="row">
          <div style={{ width: 200 }}>
            <Field label={t("mgmtMigrate.testAddress")}>
              <input value={host} onChange={(e) => setHost(e.target.value)} className="mono" />
            </Field>
          </div>
          <ActionButton kind="check" className="secondary" disabled={!host.trim() || check.isPending}
            onClick={() => check.mutate()}>
            {t("mgmtMigrate.verify")}
          </ActionButton>
        </div>
        <ErrorMsg error={check.error} />
        {check.data && (
          <p style={{ color: check.data.reachable ? "var(--ok)" : "var(--danger)" }}>
            {check.data.verdict}
          </p>
        )}
      </div>

      {pathState && pathState !== "ready" && (
        <div
          className="drift-banner"
          style={
            pathState === "absent" ? undefined : { borderColor: "var(--border)" }
          }
        >
          <div className="drift-text">
            <div
              className="drift-title"
              style={pathState === "absent" ? undefined : { color: "var(--text)" }}
            >
              {pathState === "absent"
                ? t("mgmtMigrate.noUplinkVlan")
                : t("mgmtMigrate.uplinkIndeterminate")}
            </div>
            <div className="note">{pathReason}</div>
            <div className="note" style={{ marginTop: 6 }}>
              {t("mgmtMigrate.uplinkHint", {
                vlan: vlan,
                mode: m?.target_tagged ? t("mgmtMigrate.taggedMode") : t("mgmtMigrate.nativeMode"),
              })}
            </div>
          </div>
        </div>
      )}

      <div className="card">
        <div className="kicker">{t("mgmtMigrate.step2Title")}</div>
        <p className="note">{t("mgmtMigrate.step2Help", { vlan: vlan })}</p>
        <ActionButton
          kind="migrate"
          className="secondary"
          disabled={busy || pathState === "absent"}
          title={
            pathState === "absent"
              ? t("mgmtMigrate.prepareBlockedTitle")
              : t("mgmtMigrate.prepareTitle")
          }
          onClick={() => prepare.mutate()}
        >
          {t("mgmtMigrate.prepare")}
        </ActionButton>
        <ErrorMsg error={prepare.error} />
        {prepare.data && <Verdict r={prepare.data} />}
      </div>

      <div className="card">
        <div className="kicker">{t("mgmtMigrate.step3Title")}</div>
        <p className="note">{t("mgmtMigrate.step3Help", { vlan: vlan })}</p>
        <ActionButton
          kind="migrate"
          className="secondary"
          disabled={busy}
          title={t("mgmtMigrate.commitTitle")}
          onClick={() => commit.mutate()}
        >
          {t("mgmtMigrate.commit")}
        </ActionButton>
        <ErrorMsg error={commit.error} />
        {commit.data && <Verdict r={commit.data} />}
      </div>

      <div className="card">
        <div className="kicker">{t("mgmtMigrate.step4Title")}</div>
        <p className="note">{t("mgmtMigrate.step4Help")}</p>
        <ActionButton
          kind="finish"
          className="danger"
          disabled={busy}
          title={t("mgmtMigrate.persistTitle")}
          onClick={() => persist.mutate()}
        >
          {t("mgmtMigrate.persist")}
        </ActionButton>
        <ErrorMsg error={persist.error} />
        {persist.data && <Verdict r={persist.data} />}
      </div>

      <div className="actions">
        <ActionButton kind="close" className="secondary" onClick={onClose}>
          {t("common.closeAction")}
        </ActionButton>
      </div>
    </Modal>
  );
}
