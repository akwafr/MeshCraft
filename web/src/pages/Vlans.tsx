import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { useSite, useSiteId } from "../useSite";
import { del, get, patch, post, type Vlan } from "../api";
import { ActionButton, DeleteButton, ErrorMsg, Field, Modal } from "../components";

// Suggested palette for new VLANs (distinguishable colors).
const PALETTE = [
  "#4f7cff", "#46a758", "#e5484d", "#f5a623", "#a855f7",
  "#06b6d4", "#ec4899", "#84cc16", "#f97316", "#14b8a6",
];

const emptyForm = { vlan_id: 1, name: "", color: PALETTE[0], purpose: "" };

export default function Vlans() {
  const { t } = useTranslation();
  const { site } = useSite();
  const siteId = useSiteId();
  const siteSlug = site?.slug ?? "";
  const qc = useQueryClient();
  const [editing, setEditing] = useState<Vlan | "new" | null>(null);
  const [form, setForm] = useState(emptyForm);

  const { data: vlans, error } = useQuery({
    queryKey: ["vlans", siteId],
    queryFn: () => get<Vlan[]>(`/api/sites/${siteId}/vlans`),
    enabled: !!siteId,
  });

  const invalidate = () => qc.invalidateQueries({ queryKey: ["vlans", siteId] });

  const save = useMutation({
    mutationFn: () =>
      editing === "new"
        ? post<Vlan>(`/api/sites/${siteId}/vlans`, form)
        : patch<Vlan>(`/api/vlans/${(editing as Vlan).id}`, form),
    onSuccess: () => {
      invalidate();
      setEditing(null);
    },
  });

  const remove = useMutation({
    mutationFn: (id: string) => del(`/api/vlans/${id}`),
    onSuccess: invalidate,
  });

  const open = (v: Vlan | "new") => {
    setEditing(v);
    save.reset();
    if (v === "new") {
      const used = new Set((vlans ?? []).map((x) => x.color));
      setForm({
        ...emptyForm,
        color: PALETTE.find((c) => !used.has(c)) ?? PALETTE[0],
        vlan_id: Math.max(0, ...(vlans ?? []).map((x) => x.vlan_id)) + 10,
      });
    } else {
      setForm({ vlan_id: v.vlan_id, name: v.name, color: v.color, purpose: v.purpose });
    }
  };

  return (
    <>
      <div className="toolbar">
        <h1>
          {t("nav.vlans")}
          <span className="sub">{t("vlans.subtitle")}</span>
        </h1>
        <ActionButton kind="create" onClick={() => open("new")}>{t("vlans.createVlan")}</ActionButton>
      </div>
      <ErrorMsg error={error} />
      <div className="card">
        {(vlans ?? []).length === 0 ? (
          <div className="empty">{t("vlans.noneOnSite")}</div>
        ) : (
          <table>
            <thead>
              <tr>
                <th style={{ width: 70 }}>ID</th>
                <th>{t("common.name")}</th>
                <th>{t("vlans.usage")}</th>
                <th style={{ width: 200 }}></th>
              </tr>
            </thead>
            <tbody>
              {(vlans ?? []).map((v) => (
                <tr key={v.id}>
                  <td className="mono">{v.vlan_id}</td>
                  <td>
                    <span className="vlan-chip" style={{ background: v.color }} />
                    {v.name}
                  </td>
                  <td className="note">{v.purpose}</td>
                  <td style={{ textAlign: "right" }}>
                    <ActionButton kind="edit" className="ghost" onClick={() => open(v)}>
                      {t("common.edit")}
                    </ActionButton>
                    {/* VLAN 1 exists on the hardware whether it's wanted or
                        not, and acts as the implicit native VLAN for trunks:
                        erasing it from the site wouldn't remove it anywhere. */}
                    {v.vlan_id === 1 ? (
                      <span className="note" style={{ marginLeft: 8 }}>
                        {t("vlans.byDefault")}
                      </span>
                    ) : (
                      <DeleteButton onConfirm={() => remove.mutate(v.id)} />
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>

      {editing && (
        <Modal
          title={(editing === "new" ? t("vlans.createVlan") : t("vlans.editVlan")) as string}
          onClose={() => setEditing(null)}
        >
          <div className="row">
            <div style={{ width: 100 }}>
              <Field label={t("vlans.idLabel") as string}>
                <input
                  type="number"
                  min={1}
                  max={4094}
                  disabled={editing !== "new" && editing.vlan_id === 1}
                  value={form.vlan_id}
                  onChange={(e) =>
                    setForm({ ...form, vlan_id: Number(e.target.value) })
                  }
                />
              </Field>
            </div>
            <div style={{ flex: 1 }}>
              <Field label={t("common.name") as string}>
                <input
                  value={form.name}
                  onChange={(e) => setForm({ ...form, name: e.target.value })}
                  autoFocus
                />
              </Field>
            </div>
            <Field label={t("vlans.color") as string}>
              <input
                type="color"
                value={form.color}
                onChange={(e) => setForm({ ...form, color: e.target.value })}
              />
            </Field>
          </div>
          <Field label={t("vlans.usageFreeform") as string}>
            <input
              value={form.purpose}
              onChange={(e) => setForm({ ...form, purpose: e.target.value })}
            />
          </Field>
          <ErrorMsg error={save.error} />
          <div className="actions">
            <ActionButton kind="cancel" className="secondary" onClick={() => setEditing(null)}>
              {t("common.cancel")}
            </ActionButton>
            <ActionButton
              kind="save"
              disabled={!form.name || save.isPending}
              onClick={() => save.mutate()}
            >
              {t("common.save")}
            </ActionButton>
          </div>
        </Modal>
      )}
    </>
  );
}
