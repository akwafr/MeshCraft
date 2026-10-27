import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { post, put, type Device, type Vlan } from "../api";
import { ActionButton, ErrorMsg, QuickTip } from "../components";
import { vlanPathPreview } from "../network";
import { MgmtMigrateModal } from "./MgmtMigrate";

type ApplyResult = {
  ok: boolean;
  error?: string;
  notes?: string[];
  note?: string;
  backup_bytes?: number;
};

/**
 * Management addressing for ONE device: DHCP or static, with address, gateway, DNS,
 * and management VLAN (when the platform can migrate or jump).
 * See migration 0027 and docs on management addressing.
 */
export function MgmtAddressingCard({
  dev,
  canEdit,
  onToast,
  embedded,
  vlans = [],
}: {
  dev: Device;
  canEdit: boolean;
  onToast: (msg: string) => void;
  /** Title lives on the side-panel group when true. */
  embedded?: boolean;
  /** Site VLAN catalogue — feeds the management VLAN dropdown. */
  vlans?: Vlan[];
}) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const view = dev.mgmt_addressing;
  const intent = dev.mgmt_addr;
  const observed = dev.mgmt_addr_observed;
  const caps = view?.caps;
  const currentVlan = view?.current_vlan ?? 0;

  const [mode, setMode] = useState(intent?.mode ?? "inherit");
  const [ip, setIp] = useState(intent?.static_ip || observed?.address || "");
  const [gw, setGw] = useState(intent?.static_gateway || observed?.gateway || "");
  const [dns, setDns] = useState(
    (intent?.static_dns ?? observed?.dns ?? []).join(", "),
  );
  const [vlanId, setVlanId] = useState(currentVlan || (dev.mgmt?.target_vlan ?? 0));
  const [jumpConfirm, setJumpConfirm] = useState(false);
  const [applyConfirm, setApplyConfirm] = useState(false);
  const [migrateOpen, setMigrateOpen] = useState(false);

  useEffect(() => {
    setMode(intent?.mode ?? "inherit");
    setIp(intent?.static_ip || observed?.address || "");
    setGw(intent?.static_gateway || observed?.gateway || "");
    setDns((intent?.static_dns ?? observed?.dns ?? []).join(", "));
    setVlanId(view?.current_vlan || (dev.mgmt?.target_vlan ?? 0));
    setJumpConfirm(false);
    setApplyConfirm(false);
    setMigrateOpen(false);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [dev.id, view?.current_vlan]);

  const refresh = () => qc.invalidateQueries({ queryKey: ["devices", dev.site_id] });

  const save = useMutation({
    mutationFn: () =>
      put<Device>(`/api/devices/${dev.id}/mgmt-addressing`, {
        mode,
        static_ip: ip.trim(),
        static_gateway: gw.trim(),
        static_dns: dns
          .split(",")
          .map((s) => s.trim())
          .filter(Boolean),
      }),
    onSuccess: () => {
      refresh();
      setApplyConfirm(false);
      onToast(t("mgmtAddressing.savedToast"));
    },
  });

  const apply = useMutation({
    mutationFn: () =>
      post<ApplyResult>(`/api/devices/${dev.id}/mgmt-addressing/apply`, {
        confirmed: !!view?.apply_confirm,
      }),
    onSuccess: (r) => {
      refresh();
      setApplyConfirm(false);
      if (r.ok) onToast(t("mgmtAddressing.appliedToast"));
      else onToast(r.error || t("mgmtAddressing.applyFailed"));
    },
  });

  const jump = useMutation({
    mutationFn: () =>
      post<ApplyResult>(`/api/devices/${dev.id}/mgmt-addressing/jump`, {
        confirmed: true,
        vlan_id: vlanId > 0 ? vlanId : undefined,
        use_plan_vlan: !(vlanId > 0),
      }),
    onSuccess: (r) => {
      refresh();
      setJumpConfirm(false);
      if (r.ok) {
        onToast(
          t("conformMgmt.jumpedToast", {
            bytes: r.backup_bytes ?? 0,
          }),
        );
      } else {
        onToast(r.error || t("mgmtAddressing.jumpFailed"));
      }
    },
  });

  const dirty =
    mode !== (intent?.mode ?? "inherit") ||
    ip.trim() !== (intent?.static_ip ?? "") ||
    gw.trim() !== (intent?.static_gateway ?? "") ||
    dns.trim() !== (intent?.static_dns ?? []).join(", ");

  const showStatic = mode === "static" || (mode === "inherit" && view?.effective === "static");
  const canApply =
    canEdit &&
    view?.pushable &&
    !dirty &&
    (intent?.mode === "dhcp" || intent?.mode === "static");
  const canChangeVlan = canEdit && !!(caps?.migrate_vlan || caps?.jump_vlan);
  const vlanDirty = vlanId > 0 && vlanId !== currentVlan;
  const pathPreview = vlanId > 0 ? vlanPathPreview(dev, vlanId) : "unknown";
  const canJump = canEdit && !!caps?.jump_vlan;
  const busy = save.isPending || apply.isPending || jump.isPending;

  const vlanOptions = (() => {
    const byId = new Map(vlans.map((v) => [v.vlan_id, v]));
    if (currentVlan > 0 && !byId.has(currentVlan)) {
      byId.set(currentVlan, {
        id: "",
        site_id: dev.site_id,
        vlan_id: currentVlan,
        name: t("mgmtAddressing.vlanObservedName", { id: currentVlan }),
        color: "var(--text-dim)",
        purpose: "",
      });
    }
    return [...byId.values()].sort((a, b) => a.vlan_id - b.vlan_id);
  })();

  return (
    <div>
      {!embedded ? (
        <div className="row" style={{ justifyContent: "space-between", fontSize: 11 }}>
          <span className="kicker">{t("mgmtAddressing.title")}</span>
          <span className="note">{effectiveLabel(view?.effective, t)}</span>
        </div>
      ) : null}

      {view?.why && (
        <div className="note" style={{ marginTop: 4 }}>
          {view.why}
        </div>
      )}

      {view?.drift && (
        <div className="note" style={{ marginTop: 8, color: "var(--text)" }}>
          {view.drift === "addr_plan_mismatch"
            ? t("mgmtAddressing.planMismatch")
            : view.drift}
          {view.apply_confirm && (
            <div className="note" style={{ marginTop: 6 }}>
              {view.apply_confirm}
            </div>
          )}
        </div>
      )}
      {!view?.drift && view?.apply_confirm && (
        <div className="note" style={{ marginTop: 8 }}>
          {view.apply_confirm}
        </div>
      )}

      {canEdit ? (
        <div style={{ marginTop: 8, display: "grid", gap: 6 }}>
          {canChangeVlan && (
            <>
              <QuickTip text={t("mgmtAddressing.vlanSelectTitle")} className="quicktip-inline">
                <select
                  value={vlanId || ""}
                  disabled={busy}
                  style={{ minHeight: 28, fontSize: 12 }}
                  onChange={(e) => setVlanId(Number(e.target.value) || 0)}
                >
                  <option value="">{t("mgmtAddressing.vlanSelectPlaceholder")}</option>
                  {vlanOptions.map((v) => {
                    const path = vlanPathPreview(dev, v.vlan_id);
                    const suffix =
                      path === "ready"
                        ? t("mgmtAddressing.vlanPathReadyShort")
                        : path === "absent"
                          ? t("mgmtAddressing.vlanPathAbsentShort")
                          : t("mgmtAddressing.vlanPathUnknownShort");
                    const current =
                      v.vlan_id === currentVlan
                        ? ` — ${t("mgmtAddressing.vlanCurrent")}`
                        : "";
                    return (
                      <option key={v.vlan_id} value={v.vlan_id} disabled={path === "absent"}>
                        {v.vlan_id} — {v.name}
                        {current} ({suffix})
                      </option>
                    );
                  })}
                </select>
              </QuickTip>
              {vlanId > 0 && (
                <div
                  className="note"
                  style={{
                    color:
                      pathPreview === "absent"
                        ? "var(--danger)"
                        : pathPreview === "ready"
                          ? "var(--ok)"
                          : undefined,
                  }}
                >
                  {pathPreview === "ready"
                    ? t("mgmtAddressing.vlanPathReady", { vlan: vlanId })
                    : pathPreview === "absent"
                      ? t("mgmtAddressing.vlanPathAbsent", { vlan: vlanId })
                      : t("mgmtAddressing.vlanPathUnknown", { vlan: vlanId })}
                </div>
              )}
              {vlanDirty && caps?.migrate_vlan && (
                <ActionButton
                  kind="migrate"
                  className="secondary"
                  disabled={busy || dirty || pathPreview === "absent"}
                  onClick={() => setMigrateOpen(true)}
                  style={{ fontSize: 12 }}
                  title={
                    pathPreview === "absent"
                      ? t("mgmtAddressing.vlanBlockedTitle")
                      : t("mgmtAddressing.vlanMigrateTitle")
                  }
                >
                  {t("mgmtAddressing.vlanMigrate")}
                </ActionButton>
              )}
              {vlanDirty && canJump && !caps?.migrate_vlan && (
                <div className="row" style={{ gap: 8, flexWrap: "wrap" }}>
                  {!jumpConfirm ? (
                    <ActionButton
                      kind="migrate"
                      className="secondary"
                      disabled={busy || dirty || pathPreview === "absent"}
                      onClick={() => setJumpConfirm(true)}
                      style={{ fontSize: 12 }}
                      title={
                        pathPreview === "absent"
                          ? t("mgmtAddressing.vlanBlockedTitle")
                          : t("mgmtAddressing.vlanJumpTitle")
                      }
                    >
                      {t("mgmtAddressing.vlanJump")}
                    </ActionButton>
                  ) : (
                    <>
                      <ActionButton
                        kind="confirm"
                        disabled={busy || pathPreview === "absent"}
                        onClick={() => jump.mutate()}
                        style={{ fontSize: 12 }}
                      >
                        {jump.isPending
                          ? t("conformMgmt.conforming")
                          : t("mgmtAddressing.vlanJumpConfirm")}
                      </ActionButton>
                      <ActionButton
                        kind="cancel"
                        className="ghost"
                        disabled={busy}
                        onClick={() => setJumpConfirm(false)}
                        style={{ fontSize: 12 }}
                      >
                        {t("common.cancel")}
                      </ActionButton>
                    </>
                  )}
                </div>
              )}
            </>
          )}

          <QuickTip text={t("mgmtAddressing.modeSelectTitle")} className="quicktip-inline">
            <select
              value={mode}
              disabled={busy}
              style={{ minHeight: 28, fontSize: 12 }}
              onChange={(e) => setMode(e.target.value as typeof mode)}
            >
              <option value="inherit">{t("mgmtAddressing.inherit")}</option>
              {(!caps?.known || caps.dhcp) && <option value="dhcp">{t("mgmtAddressing.dhcp")}</option>}
              {(!caps?.known || caps.static) && <option value="static">{t("mgmtAddressing.static")}</option>}
            </select>
          </QuickTip>

          {showStatic && (
            <>
              <QuickTip text={t("mgmtAddressing.ipTitle")} className="quicktip-inline">
                <input
                  value={ip}
                  placeholder={t("mgmtAddressing.ipPlaceholder")}
                  disabled={busy}
                  style={{ minHeight: 28, fontSize: 12 }}
                  onChange={(e) => setIp(e.target.value)}
                />
              </QuickTip>
              <QuickTip
                text={
                  caps?.known && !caps.gateway
                    ? t("mgmtAddressing.gatewayUnsupportedTitle")
                    : t("mgmtAddressing.gatewayTitle")
                }
                className="quicktip-inline"
              >
                <input
                  value={gw}
                  placeholder={
                    caps?.known && !caps.gateway
                      ? t("mgmtAddressing.notSupportedGateway")
                      : t("mgmtAddressing.gatewayPlaceholder")
                  }
                  disabled={busy || (caps?.known && !caps.gateway)}
                  style={{ minHeight: 28, fontSize: 12 }}
                  onChange={(e) => setGw(e.target.value)}
                />
              </QuickTip>
              <QuickTip
                text={
                  caps?.known && !caps.dns
                    ? t("mgmtAddressing.dnsUnsupportedTitle")
                    : t("mgmtAddressing.dnsTitle")
                }
                className="quicktip-inline"
              >
                <input
                  value={dns}
                  placeholder={
                    caps?.known && !caps.dns
                      ? t("mgmtAddressing.notSupportedDns")
                      : t("mgmtAddressing.dnsPlaceholder")
                  }
                  disabled={busy || (caps?.known && !caps.dns)}
                  style={{ minHeight: 28, fontSize: 12 }}
                  onChange={(e) => setDns(e.target.value)}
                />
              </QuickTip>
            </>
          )}

          <div className="row" style={{ gap: 8, flexWrap: "wrap" }}>
            {dirty && (
              <ActionButton kind="save" disabled={busy} onClick={() => save.mutate()} style={{ fontSize: 12 }}>
                {save.isPending ? t("mgmtAddressing.saving") : t("mgmtAddressing.save")}
              </ActionButton>
            )}
            {canApply && !view?.apply_confirm && (
              <ActionButton
                kind="apply"
                disabled={busy}
                onClick={() => apply.mutate()}
                style={{ fontSize: 12 }}
                title={t("mgmtAddressing.applyTitle")}
              >
                {apply.isPending ? t("mgmtAddressing.applying") : t("mgmtAddressing.apply")}
              </ActionButton>
            )}
            {canApply && view?.apply_confirm && !applyConfirm && (
              <ActionButton
                kind="apply"
                className="secondary"
                disabled={busy}
                onClick={() => setApplyConfirm(true)}
                style={{ fontSize: 12 }}
                title={t("mgmtAddressing.applyOverrideTitle")}
              >
                {t("mgmtAddressing.apply")}
              </ActionButton>
            )}
            {canApply && view?.apply_confirm && applyConfirm && (
              <>
                <ActionButton
                  kind="confirm"
                  disabled={busy}
                  onClick={() => apply.mutate()}
                  style={{ fontSize: 12 }}
                  title={t("mgmtAddressing.applyConfirmTitle")}
                >
                  {apply.isPending
                    ? t("mgmtAddressing.applying")
                    : t("mgmtAddressing.applyConfirm")}
                </ActionButton>
                <ActionButton
                  kind="cancel"
                  className="ghost"
                  disabled={busy}
                  onClick={() => setApplyConfirm(false)}
                  style={{ fontSize: 12 }}
                >
                  {t("common.cancel")}
                </ActionButton>
              </>
            )}
          </div>
          <ErrorMsg error={save.error || apply.error || jump.error} />
          {apply.data && !apply.data.ok && apply.data.error && (
            <div className="note" style={{ color: "var(--danger)" }}>{apply.data.error}</div>
          )}
          {jump.data && !jump.data.ok && jump.data.error && (
            <div className="note" style={{ color: "var(--danger)" }}>{jump.data.error}</div>
          )}
        </div>
      ) : (
        <div className="kv" style={{ marginTop: 8, fontSize: 12 }}>
          <span className="k">{t("mgmtAddressing.vlan")}</span>
          <span>{currentVlan > 0 ? currentVlan : "—"}</span>
          <span className="k">{t("common.address")}</span>
          <span>{observed?.address || intent?.static_ip || dev.mgmt_ip || "—"}</span>
          <span className="k">{t("mgmtAddressing.gateway")}</span>
          <span>{gatewayLabel(observed?.gateway || intent?.static_gateway, caps, t)}</span>
          <span className="k">DNS</span>
          <span>{dnsLabel(observed?.dns ?? intent?.static_dns, caps, t)}</span>
        </div>
      )}

      {caps?.known && (
        <QuickTip text={t("mgmtAddressing.capsTitle")} className="quicktip-block">
          <div className="mgmt-caps" style={{ marginTop: 8 }}>
            <div className="kicker" style={{ marginBottom: 4 }}>{t("mgmtAddressing.capsTitle")}</div>
            <div className="mgmt-caps-list">
              <span><b>MgmtGateway</b>: {caps.gateway ? t("mgmtAddressing.capYes") : t("mgmtAddressing.capNo")}</span>
              <span><b>MgmtDNS</b>: {caps.dns ? t("mgmtAddressing.capYes") : t("mgmtAddressing.capNo")}</span>
              <span><b>MgmtRouted</b>: {caps.routed ? t("mgmtAddressing.capYes") : t("mgmtAddressing.capNo")}</span>
            <span><b>MgmtIPARP</b>: {caps.ip_arp ? t("mgmtAddressing.capYes") : t("mgmtAddressing.capNo")}</span>
          </div>
          {!caps.routed && (
            <div className="note" style={{ marginTop: 6 }}>
              {t("mgmtAddressing.notRoutedHelp")}
            </div>
          )}
          {caps.ip_arp && !caps.routed && (
            <div className="note" style={{ marginTop: 4 }}>
              {t("mgmtAddressing.ipArpHelp")}
            </div>
          )}
          </div>
        </QuickTip>
      )}

      {view?.push_blocked && (
        <div className="note" style={{ marginTop: 8 }}>
          {view.push_blocked}
        </div>
      )}

      {view && !view.readable && (
        <div className="note" style={{ marginTop: 6 }}>
          {t("mgmtAddressing.unreadable")}
        </div>
      )}
      {view?.readable && !dev.mgmt_addr_observed_at && (
        <div className="note" style={{ marginTop: 6 }}>
          {t("mgmtAddressing.notYetRead")}
        </div>
      )}

      {(observed?.notes ?? [])
        .filter((n) => !isPlatformCapabilityNote(n))
        .map((n, i) => (
        <div key={i} className="note" style={{ marginTop: 6 }}>
          {n}
        </div>
      ))}

      {migrateOpen && (
        <MgmtMigrateModal
          device={dev}
          targetVlan={vlanId}
          onClose={() => setMigrateOpen(false)}
        />
      )}
    </div>
  );
}

/** Old observation notes that duplicated Gateway/DNS caps — hide them. */
function isPlatformCapabilityNote(n: string): boolean {
  const s = n.toLowerCase();
  return (
    s.includes("ne porte ni passerelle") ||
    s.includes("neither management gateway") ||
    s.includes("ni resolveur") ||
    s.includes("ni résolveur") ||
    (s.includes("gateway") && s.includes("dns") && s.includes("platform"))
  );
}

function effectiveLabel(
  effective: string | undefined,
  t: (key: string) => string,
) {
  if (effective === "static") return t("mgmtAddressing.effectiveStatic");
  if (effective === "dhcp") return t("mgmtAddressing.effectiveDhcp");
  return t("mgmtAddressing.effectiveUnknown");
}

function gatewayLabel(
  gw: string | undefined,
  caps: { known: boolean; gateway: boolean } | undefined,
  t: (key: string) => string,
) {
  if (gw) return gw;
  if (caps?.known && !caps.gateway) return t("mgmtAddressing.noGatewayPlatform");
  return "—";
}

function dnsLabel(
  dns: string[] | undefined,
  caps: { known: boolean; dns: boolean } | undefined,
  t: (key: string) => string,
) {
  if (dns && dns.length > 0) return dns.join(", ");
  if (caps?.known && !caps.dns) return t("mgmtAddressing.noDnsPlatform");
  return "—";
}
