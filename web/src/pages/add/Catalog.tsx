import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router-dom";
import { sitePath } from "../../paths";
import { useSite, useSiteId } from "../../useSite";
import { get, post, type CatalogModel, type Device } from "../../api";
import { useSiteRole } from "../../auth";
import { ActionButton, ErrorMsg, Field, useToast } from "../../components";

/**
 * Creation from the "Generic" model catalog.
 *
 * No real device behind it: it's a template for mocking up a site (port
 * facade, VLANs, plan) ahead of the connectors' arrival. The other three
 * menu entries, meanwhile, address existing hardware.
 */
export default function Catalog() {
  const { t } = useTranslation();
  const { site } = useSite();
  const siteId = useSiteId();
  const siteSlug = site?.slug ?? "";
  const navigate = useNavigate();
  const role = useSiteRole(siteId);
  const qc = useQueryClient();
  const [name, setName] = useState("");
  const [location, setLocation] = useState("");
  const [mgmtIp, setMgmtIp] = useState("");
  const [modelIdx, setModelIdx] = useState(0);
  const [toast, showToast] = useToast();

  const { data: models } = useQuery({
    queryKey: ["catalog"],
    queryFn: () => get<CatalogModel[]>("/api/catalog/models"),
  });

  const create = useMutation({
    mutationFn: () => {
      const m = (models ?? [])[modelIdx];
      return post<Device>(`/api/sites/${siteId}/devices`, {
        name,
        model: m?.model ?? "",
        vendor: m?.vendor ?? "generic",
        location,
        mgmt_ip: mgmtIp,
        status: "unknown",
        poe_budget_w: m?.poe_budget_w ?? 0,
        port_groups: m?.port_groups ?? [],
      });
    },
    onSuccess: (d) => {
      qc.invalidateQueries({ queryKey: ["devices", siteId] });
      showToast(t("catalog.created", { name: d.name }));
      navigate(sitePath(siteSlug, "equipment", d.name));
    },
  });

  if (role !== "ing") {
    return (
      <div className="card">
        <div className="empty">{t("catalog.roleRequired")}</div>
      </div>
    );
  }

  const selected = (models ?? [])[modelIdx];

  return (
    <div>
      <div className="toolbar">
        <h1>
          {t("nav.catalogModel")}
          <span className="sub">{t("catalog.subtitle")}</span>
        </h1>
      </div>

      <div className="card">
        <div className="row">
          <div style={{ flex: 1 }}>
            <Field label={t("catalog.hostname") as string}>
              <input value={name} onChange={(e) => setName(e.target.value)} autoFocus />
            </Field>
          </div>
          <div style={{ flex: 1 }}>
            <Field label={t("catalog.model") as string}>
              <select
                value={modelIdx}
                onChange={(e) => setModelIdx(Number(e.target.value))}
              >
                {(models ?? []).map((m, i) => (
                  <option key={m.model} value={i}>
                    {m.model} — {m.desc}
                  </option>
                ))}
              </select>
            </Field>
          </div>
        </div>
        <div className="row">
          <div style={{ flex: 1 }}>
            <Field label={t("catalog.locationInSite") as string}>
              <input value={location} onChange={(e) => setLocation(e.target.value)} />
            </Field>
          </div>
          <div style={{ width: 200 }}>
            <Field label={t("common.mgmtIp") as string}>
              <input value={mgmtIp} onChange={(e) => setMgmtIp(e.target.value)} />
            </Field>
          </div>
        </div>
        {selected && (
          <div className="note">
            {selected.desc} ·{" "}
            {selected.port_groups
              .map((g) => `${g.count}× ${g.speed ?? "1G"}`)
              .join(" + ")}
            {selected.poe_budget_w ? ` · PoE ${selected.poe_budget_w} W` : ""}
          </div>
        )}
        <ErrorMsg error={create.error} />
        <div className="actions">
          <ActionButton kind="create" disabled={!name.trim() || create.isPending} onClick={() => create.mutate()}>
            {t("catalog.createDevice")}
          </ActionButton>
        </div>
      </div>
      {toast}
    </div>
  );
}
