import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { useSite, useSiteId } from "../useSite";
import {
  del,
  get,
  patch,
  post,
  type Device,
  type PortProfileSpec,
  type Profile,
  type Vlan,
} from "../api";
import { useSiteRole } from "../auth";
import { ActionButton, DeleteButton, ErrorMsg, Field, Modal, useToast } from "../components";
import { expandPorts, PALETTE, serves, vlanMap } from "../network";

/** Daily "Profiles" page: port profiles + editable VLAN plan in the side panel (engineer). */
export default function Profils() {
  const { t } = useTranslation();
  const { site } = useSite();
  const siteId = useSiteId();
  const siteSlug = site?.slug ?? "";
  const role = useSiteRole(siteId);
  const ing = role === "ing";
  const qc = useQueryClient();
  const [editing, setEditing] = useState<Profile | "new" | null>(null);
  const [edVlan, setEdVlan] = useState<string | null>(null);
  const [toast, showToast] = useToast();

  const { data: profiles, error } = useQuery({
    queryKey: ["profiles", "port", siteId],
    queryFn: () => get<Profile[]>(`/api/sites/${siteId}/profiles/port`),
    enabled: !!siteId,
  });
  const { data: vlans } = useQuery({
    queryKey: ["vlans", siteId],
    queryFn: () => get<Vlan[]>(`/api/sites/${siteId}/vlans`),
    enabled: !!siteId,
  });
  const { data: devices } = useQuery({
    queryKey: ["devices", siteId],
    queryFn: () => get<Device[]>(`/api/sites/${siteId}/devices`),
    enabled: !!siteId,
  });
  const vm = vlanMap(vlans);

  const countOf = (vid: number) => {
    let n = 0;
    (devices ?? []).forEach((d) =>
      expandPorts(d).forEach((pr) => {
        if (pr.p.mode && pr.p.mode !== "libre" && serves(pr.p, vid)) n++;
      }),
    );
    return n;
  };

  const invalidateVlans = () => qc.invalidateQueries({ queryKey: ["vlans", siteId] });
  const updateVlan = useMutation({
    mutationFn: (v: Vlan) =>
      patch<Vlan>(`/api/vlans/${v.id}`, {
        vlan_id: v.vlan_id, name: v.name, color: v.color, purpose: v.purpose,
      }),
    onSuccess: invalidateVlans,
  });

  const removeProfile = useMutation({
    mutationFn: (id: string) => del(`/api/profiles/port/${id}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["profiles", "port", siteId] }),
  });

  const cfgText = (spec: PortProfileSpec) => {
    if (spec.mode === "access") {
      const v = spec.pvid != null ? vm.get(spec.pvid) : undefined;
      return t("profilsPage.accessVlan", {
        vlan: spec.pvid ?? "—",
        name: v ? " · " + v.name : "",
      });
    }
    const members =
      spec.tagged === "all"
        ? t("profilsPage.fullTrunk")
        : t("profilsPage.trunkVlans", { list: ((spec.tagged ?? []) as number[]).join(" · ") });
    return members + (spec.pvid != null ? t("profilsPage.nativeSuffix", { vlan: spec.pvid }) : "");
  };

  return (
    <div className="page2col wide-panel">
      <main>
        <div className="toolbar">
          <h1>
            {t("profilsPage.title")}
            <span className="sub">
              {t("profilsPage.subtitle", { count: (profiles ?? []).length })}
            </span>
          </h1>
          {ing && (
            <ActionButton kind="create" onClick={() => setEditing("new")}>
              {t("profilsPage.newProfile")}
            </ActionButton>
          )}
        </div>
        {!ing && (
          <div className="note" style={{ margin: "0 0 12px" }}>
            {t("profilsPage.readOnly")}
          </div>
        )}
        <ErrorMsg error={error} />
        <div className="card">
          <table>
            <thead>
              <tr>
                <th>{t("profilsPage.profileCol")}</th>
                <th>{t("profilsPage.configCol")}</th>
                <th>PoE</th>
                <th>STP</th>
                <th style={{ width: 140 }}></th>
              </tr>
            </thead>
            <tbody>
              {(profiles ?? []).map((p) => {
                const spec = p.spec as unknown as PortProfileSpec;
                const swatchIds =
                  spec.mode === "access"
                    ? [spec.pvid]
                    : spec.tagged === "all"
                      ? [spec.pvid]
                      : ((spec.tagged ?? []) as number[]).slice(0, 4);
                return (
                  <tr key={p.id}>
                    <td style={{ maxWidth: 340 }}>
                      <div className="row" style={{ gap: 8 }}>
                        <span style={{ display: "flex", gap: 2, flex: "none" }}>
                          {swatchIds.map((id, i) => (
                            <span
                              key={i}
                              style={{
                                width: 9, height: 9, borderRadius: 2, display: "block",
                                background: id != null && vm.get(id) ? vm.get(id)!.color : "#888",
                              }}
                            />
                          ))}
                        </span>
                        <span style={{ fontWeight: 700, fontSize: 13 }}>{p.name}</span>
                        {spec.eng_only && (
                          <span className="badge admin">{t("profilsPage.engineerBadge")}</span>
                        )}
                      </div>
                      <div className="note" style={{ marginTop: 2 }}>{spec.description}</div>
                    </td>
                    <td className="note" style={{ fontSize: 12 }}>{cfgText(spec)}</td>
                    <td>
                      <span className="note" style={{ fontWeight: 600, color: spec.poe ? "var(--text)" : undefined }}>
                        {spec.poe ? "PoE" : "—"}
                      </span>
                    </td>
                    <td>
                      <span className="note" style={{ fontWeight: 600, color: spec.stp_enabled === false ? "var(--warn)" : "var(--text)" }}>
                        {spec.stp_enabled === false
                          ? t("profilsPage.stpEnabledOff")
                          : t("profilsPage.stpEnabledOn")}
                        {spec.stp_edge ? " · edge" : ""}
                        {spec.bpdu_guard ? " · bpdu" : ""}
                      </span>
                    </td>
                    <td style={{ textAlign: "right", whiteSpace: "nowrap" }}>
                      {ing && (
                        <>
                          <ActionButton kind="edit" className="ghost" onClick={() => setEditing(p)}>
                            {t("common.edit")}
                          </ActionButton>
                          <DeleteButton onConfirm={() => removeProfile.mutate(p.id)} />
                        </>
                      )}
                    </td>
                  </tr>
                );
              })}
              {(profiles ?? []).length === 0 && (
                <tr><td colSpan={4} className="empty">{t("profilsPage.noProfiles")}</td></tr>
              )}
            </tbody>
          </table>
        </div>
      </main>

      <aside className="side-panel">
        <div className="kicker">{t("profilsPage.vlanPlan")}</div>
        <div className="note" style={{ margin: "2px 0 10px" }}>
          {ing ? t("profilsPage.vlanPlanEdit") : t("profilsPage.vlanPlanReadOnly")}
        </div>
        <div>
          {(vlans ?? []).map((v) => (
            <div key={v.id} style={{ borderBottom: "1px solid var(--border)", padding: "7px 2px" }}>
              <div
                className="row"
                style={{ gap: 9, cursor: ing ? "pointer" : "default", fontSize: 12 }}
                onClick={ing ? () => setEdVlan(edVlan === v.id ? null : v.id) : undefined}
              >
                <span className="vlan-chip" style={{ background: v.color, margin: 0 }} />
                <span style={{ fontWeight: 600 }}>{v.name}</span>
                <span className="note">{v.vlan_id}</span>
                <span className="note" style={{ marginLeft: "auto" }}>
                  {t("profilsPage.portCount", { count: countOf(v.vlan_id) })}
                </span>
              </div>
              {v.purpose && (
                <div className="note" style={{ fontSize: 10, margin: "2px 0 0 21px" }}>{v.purpose}</div>
              )}
              {ing && edVlan === v.id && (
                <div style={{ margin: "8px 0 4px 21px" }}>
                  <input
                    defaultValue={v.name}
                    style={{ fontSize: 12, minHeight: 30 }}
                    onBlur={(e) => {
                      if (e.target.value && e.target.value !== v.name)
                        updateVlan.mutate({ ...v, name: e.target.value });
                    }}
                  />
                  <div style={{ display: "grid", gridTemplateColumns: "repeat(8,1fr)", gap: 4, marginTop: 6 }}>
                    {PALETTE.map((c) => (
                      <button
                        key={c}
                        style={{
                          height: 18, padding: 0, background: c,
                          border: "1px solid var(--border)", borderRadius: 3,
                          boxShadow: c === v.color ? "inset 0 0 0 2px var(--text)" : "none",
                        }}
                        onClick={() => updateVlan.mutate({ ...v, color: c })}
                      />
                    ))}
                  </div>
                </div>
              )}
            </div>
          ))}
        </div>
      </aside>

      {editing && (
        <ProfileEditor
          siteId={siteId!}
          profile={editing === "new" ? null : editing}
          vlans={vlans ?? []}
          onClose={() => setEditing(null)}
          onDone={(name) => {
            setEditing(null);
            qc.invalidateQueries({ queryKey: ["profiles", "port", siteId] });
            showToast(t("profilsPage.savedToast", { name }));
          }}
        />
      )}
      {toast}
    </div>
  );
}

function ProfileEditor({
  siteId,
  profile,
  vlans,
  onClose,
  onDone,
}: {
  siteId: string;
  profile: Profile | null;
  vlans: Vlan[];
  onClose: () => void;
  onDone: (name: string) => void;
}) {
  const { t } = useTranslation();
  const init = (profile?.spec ?? {}) as unknown as PortProfileSpec;
  const [name, setName] = useState(profile?.name ?? "");
  const [spec, setSpec] = useState<PortProfileSpec>({
    description: init.description ?? "",
    mode: init.mode ?? "access",
    pvid: init.pvid ?? vlans[0]?.vlan_id ?? null,
    tagged: init.tagged ?? [],
    poe: !!init.poe,
    stp_edge: !!init.stp_edge,
    // Default on for new profiles; keep false when the saved spec says so.
    stp_enabled: init.stp_enabled !== false,
    bpdu_guard: !!init.bpdu_guard,
    eng_only: !!init.eng_only,
  });

  const save = useMutation({
    mutationFn: () => {
      const body = { name, spec };
      return profile
        ? patch<Profile>(`/api/profiles/port/${profile.id}`, body)
        : post<Profile>(`/api/sites/${siteId}/profiles/port`, body);
    },
    onSuccess: () => onDone(name),
  });

  const tagged = spec.tagged ?? [];
  const toggleMember = (vid: number) => {
    let next: number[] =
      tagged === "all" ? vlans.map((v) => v.vlan_id) : (tagged as number[]).slice();
    const i = next.indexOf(vid);
    if (i > -1) next.splice(i, 1);
    else next.push(vid);
    next.sort((a, b) => a - b);
    setSpec({ ...spec, tagged: next });
  };

  return (
    <Modal
      title={profile ? t("profilsPage.editProfile") : t("profilsPage.newProfileModal")}
      onClose={onClose}
    >
      <Field label={t("profilsPage.profileName")}>
        <input
          value={name}
          onChange={(e) => setName(e.target.value)}
          autoFocus
          placeholder={t("profilsPage.namePlaceholder")}
        />
      </Field>
      <Field label={t("profilsPage.descriptionLabel")}>
        <input
          value={spec.description}
          onChange={(e) => setSpec({ ...spec, description: e.target.value })}
        />
      </Field>
      <div className="kicker" style={{ marginBottom: 5 }}>{t("profilsPage.mode")}</div>
      <div className="seg3">
        <button
          className={spec.mode === "access" ? "on" : ""}
          onClick={() => setSpec({ ...spec, mode: "access" })}
        >
          {t("profilsPage.accessMode")}
        </button>
        <button
          className={spec.mode === "trunk" ? "on" : ""}
          onClick={() =>
            setSpec({
              ...spec, mode: "trunk",
              tagged: tagged === "all" || (Array.isArray(tagged) && tagged.length)
                ? tagged
                : spec.pvid != null ? [spec.pvid] : [],
            })
          }
        >
          {t("profilsPage.trunkMode")}
        </button>
      </div>

      {spec.mode === "access" && (
        <div className="vlan-pick" style={{ maxHeight: 250 }}>
          {vlans.map((v) => (
            <button
              key={v.id}
              className={"vlan-row" + (spec.pvid === v.vlan_id ? " sel" : "")}
              onClick={() => setSpec({ ...spec, pvid: v.vlan_id })}
            >
              <span className="vlan-chip" style={{ background: v.color, margin: 0 }} />
              <span style={{ fontWeight: 600 }}>{v.name}</span>
              <span className="note">{v.vlan_id}</span>
            </button>
          ))}
        </div>
      )}

      {spec.mode === "trunk" && (
        <>
          <div className="kicker" style={{ margin: "4px 0 6px" }}>{t("profilsPage.taggedMembers")}</div>
          <div className="vlan-pick" style={{ maxHeight: 220 }}>
            <button
              className="vlan-row"
              onClick={() =>
                setSpec({ ...spec, tagged: tagged === "all" ? [] : "all" })
              }
            >
              <span
                className="vlan-chip"
                style={{ background: tagged === "all" ? "var(--accent)" : "transparent", border: "1px solid var(--border)", margin: 0 }}
              />
              <span style={{ fontWeight: 600 }}>{t("profilsPage.allVlansTrunk")}</span>
            </button>
            {vlans.map((v) => {
              const member = tagged === "all" || (Array.isArray(tagged) && tagged.includes(v.vlan_id));
              return (
                <button key={v.id} className="vlan-row" onClick={() => toggleMember(v.vlan_id)}>
                  <span
                    className="vlan-chip"
                    style={{ background: member ? v.color : "transparent", border: `1px solid ${v.color}`, margin: 0 }}
                  />
                  <span style={{ fontWeight: 600, color: member ? "var(--text)" : "var(--text-dim)" }}>
                    {v.name}
                  </span>
                  <span className="note">{v.vlan_id}</span>
                </button>
              );
            })}
          </div>
          <div className="kicker" style={{ margin: "10px 0 5px" }}>{t("profilsPage.nativeVlan")}</div>
          <select
            value={spec.pvid ?? ""}
            onChange={(e) => setSpec({ ...spec, pvid: e.target.value ? Number(e.target.value) : null })}
          >
            <option value="">—</option>
            {vlans.map((v) => (
              <option key={v.id} value={v.vlan_id}>{v.vlan_id} — {v.name}</option>
            ))}
          </select>
        </>
      )}

      <div className="row" style={{ marginTop: 14, gap: 20, flexWrap: "wrap" }}>
        <label className="row" style={{ marginBottom: 0 }}>
          <input
            type="checkbox"
            checked={!!spec.poe}
            onChange={(e) => setSpec({ ...spec, poe: e.target.checked })}
          />
          {t("profilsPage.poeEnabled")}
        </label>
        <label className="row" style={{ marginBottom: 0 }}>
          <input
            type="checkbox"
            checked={!!spec.stp_edge}
            onChange={(e) => setSpec({ ...spec, stp_edge: e.target.checked })}
          />
          {t("profilsPage.stpEdge")}
        </label>
        <label className="row" style={{ marginBottom: 0 }}>
          <input
            type="checkbox"
            checked={!!spec.eng_only}
            onChange={(e) => setSpec({ ...spec, eng_only: e.target.checked })}
          />
          {t("profilsPage.engineerOnlyApply")}
        </label>
      </div>

      <div style={{ marginTop: 14 }}>
        <div className="kicker" style={{ marginBottom: 6 }}>{t("profilsPage.stpEnabledLabel")}</div>
        <div className="seg3" style={{ maxWidth: 280, marginBottom: 0 }}>
          <button
            type="button"
            className={spec.stp_enabled !== false ? "on" : ""}
            onClick={() => setSpec({ ...spec, stp_enabled: true })}
          >
            {t("profilsPage.stpEnabledOn")}
          </button>
          <button
            type="button"
            className={spec.stp_enabled === false ? "on" : ""}
            onClick={() => setSpec({ ...spec, stp_enabled: false })}
          >
            {t("profilsPage.stpEnabledOff")}
          </button>
        </div>
        <div className="note" style={{ marginTop: 6 }}>{t("profilsPage.stpEnabledHelp")}</div>
      </div>

      <div style={{ marginTop: 14 }}>
        <div className="kicker" style={{ marginBottom: 6 }}>{t("profilsPage.bpduGuardLabel")}</div>
        <div className="seg3" style={{ maxWidth: 280, marginBottom: 0 }}>
          <button
            type="button"
            className={spec.bpdu_guard === true ? "on" : ""}
            onClick={() => setSpec({ ...spec, bpdu_guard: true })}
          >
            {t("profilsPage.bpduGuardOn")}
          </button>
          <button
            type="button"
            className={spec.bpdu_guard !== true ? "on" : ""}
            onClick={() => setSpec({ ...spec, bpdu_guard: false })}
          >
            {t("profilsPage.bpduGuardOff")}
          </button>
        </div>
        <div className="note" style={{ marginTop: 6 }}>{t("profilsPage.bpduGuardHelp")}</div>
      </div>

      <ErrorMsg error={save.error} />
      <div className="actions">
        <ActionButton kind="cancel" className="secondary" onClick={onClose}>
          {t("common.cancel")}
        </ActionButton>
        <ActionButton kind="save" disabled={!name.trim() || save.isPending} onClick={() => save.mutate()}>
          {t("common.save")}
        </ActionButton>
      </div>
    </Modal>
  );
}
