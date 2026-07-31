import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { useSite, useSiteId } from "../useSite";
import { get, put, type MgmtPlan, type MgmtPoolUsage, type Vlan } from "../api";
import { ActionButton, ErrorMsg, Field, HelpTip, Switch, VlanCombo, useToast } from "../components";
import { useSiteRole } from "../auth";

/** Native VLAN is VLAN 1: industry default and what the spec retains. */
const NATIVE_VLAN = 1;

/** Site management addressing plan — how adopted devices become reachable for administration. */
export default function MgmtPlanPage() {
  const { t } = useTranslation();
  const { site } = useSite();
  const siteId = useSiteId();
  const siteSlug = site?.slug ?? "";
  const qc = useQueryClient();
  const [toast, showToast] = useToast();
  const canEdit = useSiteRole(siteId) === "ing";

  const { data: vlans } = useQuery({
    queryKey: ["vlans", siteId],
    queryFn: () => get<Vlan[]>(`/api/sites/${siteId}/vlans`),
    enabled: !!siteId,
  });

  const { data, error } = useQuery({
    queryKey: ["mgmt-plan", siteId],
    queryFn: () => get<{ plan: MgmtPlan; pool: MgmtPoolUsage }>(`/api/sites/${siteId}/mgmt-plan`),
    enabled: !!siteId,
  });

  const [form, setForm] = useState<MgmtPlan | null>(null);
  useEffect(() => {
    if (data?.plan) setForm(data.plan);
  }, [data?.plan]);

  const save = useMutation({
    mutationFn: () => put<MgmtPlan>(`/api/sites/${siteId}/mgmt-plan`, form),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["mgmt-plan", siteId] });
      showToast(t("mgmtPlanPage.saved"));
    },
  });

  if (!siteId) return <div className="empty">{t("mgmtPlanPage.chooseSite")}</div>;
  if (error) return <ErrorMsg error={error} />;
  if (!form) return <div className="empty">{t("common.loading")}</div>;

  const set = <K extends keyof MgmtPlan>(k: K, v: MgmtPlan[K]) =>
    setForm({ ...form, [k]: v });
  const byMeshcraft = form.source === "meshcraft";
  const pool = data?.pool;

  return (
    <>
      <div className="toolbar">
        <h1>
          {t("mgmtPlanPage.title")}
          <span className="sub">{t("mgmtPlanPage.subtitle")}</span>
        </h1>
      </div>

      {!form.configured && (
        <p className="note" style={{ marginTop: 0 }}>
          {t("mgmtPlanPage.notConfigured")}
        </p>
      )}

      <div className="card">
        <div className="card-head">
          <div className="kicker">
            {t("mgmtPlanPage.adminVlan")}
            <HelpTip label={t("mgmtPlanPage.adminVlanHelpTitle")}>
              <p>{t("mgmtPlanPage.adminVlanHelp1")}</p>
              <p>{t("mgmtPlanPage.adminVlanHelp2")}</p>
            </HelpTip>
          </div>
        </div>
        <div className="form-grid">
          <Switch
            checked={form.untagged}
            disabled={!canEdit}
            onChange={(v) =>
              setForm({ ...form, untagged: v, vlan_id: v ? NATIVE_VLAN : form.vlan_id })
            }
            label={t("mgmtPlanPage.untaggedAdmin")}
            hint={t("mgmtPlanPage.untaggedHint")}
          />
          <Field label={t("mgmtPlanPage.mgmtVlan")}>
            <VlanCombo
              value={form.vlan_id}
              vlans={vlans ?? []}
              disabled={!canEdit || form.untagged}
              onChange={(id) => set("vlan_id", id)}
            />
          </Field>
        </div>
      </div>

      <div className="card">
        <div className="card-head">
          <div className="kicker">{t("mgmtPlanPage.subnetsGateway")}</div>
        </div>
        <div className="form-grid cols-2">
          <Field label={t("mgmtPlanPage.subnetV4")}>
            <input
              value={form.subnet_v4}
              disabled={!canEdit}
              placeholder="10.10.0.0/24"
              onChange={(e) => set("subnet_v4", e.target.value)}
            />
          </Field>
          <Field label={t("mgmtPlanPage.gatewayV4")}>
            <input
              value={form.gateway_v4}
              disabled={!canEdit}
              placeholder="10.10.0.1"
              onChange={(e) => set("gateway_v4", e.target.value)}
            />
          </Field>
          <Field label={t("mgmtPlanPage.subnetV6")}>
            <input
              value={form.subnet_v6}
              disabled={!canEdit}
              placeholder="2001:db8:1::/64"
              onChange={(e) => set("subnet_v6", e.target.value)}
            />
          </Field>
          <Field label={t("mgmtPlanPage.gatewayV6")}>
            <input
              value={form.gateway_v6}
              disabled={!canEdit}
              placeholder="2001:db8:1::1"
              onChange={(e) => set("gateway_v6", e.target.value)}
            />
          </Field>
          <Field label={t("mgmtPlanPage.dns")}>
            <input
              value={(form.dns ?? []).join(", ")}
              disabled={!canEdit}
              placeholder="10.10.0.1, 1.1.1.1"
              onChange={(e) =>
                set("dns", e.target.value.split(",").map((x) => x.trim()).filter(Boolean))
              }
            />
          </Field>
          <Field label={t("mgmtPlanPage.ntp")}>
            <input
              value={(form.ntp ?? []).join(", ")}
              disabled={!canEdit}
              placeholder="10.10.0.1"
              onChange={(e) =>
                set("ntp", e.target.value.split(",").map((x) => x.trim()).filter(Boolean))
              }
            />
          </Field>
        </div>
      </div>

      <div className="card">
        <div className="card-head">
          <div className="kicker">{t("mgmtPlanPage.addressSource")}</div>
        </div>
        <label className={form.source === "dhcp_external" ? "choice active" : "choice"}>
          <input
            type="radio"
            checked={form.source === "dhcp_external"}
            disabled={!canEdit}
            onChange={() => set("source", "dhcp_external")}
          />
          <div>
            <b>{t("mgmtPlanPage.externalDhcp")}</b>
            <div className="note">{t("mgmtPlanPage.externalDhcpNote")}</div>
          </div>
        </label>
        <label className={byMeshcraft ? "choice active" : "choice"}>
          <input
            type="radio"
            checked={byMeshcraft}
            disabled={!canEdit}
            onChange={() => set("source", "meshcraft")}
          />
          <div>
            <b>{t("mgmtPlanPage.meshcraftManaged")}</b>
            <div className="note">{t("mgmtPlanPage.meshcraftManagedNote")}</div>
          </div>
        </label>

        {byMeshcraft && (
          <>
            <div className="row" style={{ marginTop: 12 }}>
              <div style={{ width: 200 }}>
                <Field label={t("mgmtPlanPage.poolStart")}>
                  <input
                    value={form.pool_start}
                    disabled={!canEdit}
                    placeholder="10.10.0.50"
                    onChange={(e) => set("pool_start", e.target.value)}
                  />
                </Field>
              </div>
              <div style={{ width: 200 }}>
                <Field label={t("mgmtPlanPage.poolEnd")}>
                  <input
                    value={form.pool_end}
                    disabled={!canEdit}
                    placeholder="10.10.0.199"
                    onChange={(e) => set("pool_end", e.target.value)}
                  />
                </Field>
              </div>
            </div>
            {pool && (
              <p className="note">
                {t("mgmtPlanPage.poolUsage", {
                  assigned: pool.assigned,
                  reserved: pool.reserved,
                  free: pool.free,
                  size: pool.size,
                })}
                {pool.size === 0 && t("mgmtPlanPage.poolEmpty")}
              </p>
            )}
          </>
        )}
      </div>

      <div className="card">
        <div className="card-head">
          <div className="kicker">
            {t("mgmtPlanPage.conformity")}
            <HelpTip label={t("mgmtPlanPage.conformityHelpTitle")}>
              <p>{t("mgmtPlanPage.conformityHelp")}</p>
            </HelpTip>
          </div>
        </div>
        <label className="row" style={{ gap: 8 }}>
          <input
            type="checkbox"
            checked={form.strict_plan}
            disabled={!canEdit}
            onChange={(e) => set("strict_plan", e.target.checked)}
          />
          <span>{t("mgmtPlanPage.strictPlan")}</span>
        </label>
      </div>

      <ErrorMsg error={save.error} />
      {canEdit && (
        <div className="row">
          <ActionButton kind="save" disabled={save.isPending} onClick={() => save.mutate()}>
            {t("mgmtPlanPage.savePlan")}
          </ActionButton>
          <span className="note">{t("mgmtPlanPage.saveNote")}</span>
        </div>
      )}
      {toast}
    </>
  );
}
