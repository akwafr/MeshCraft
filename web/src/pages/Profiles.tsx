import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { useSite, useSiteId } from "../useSite";
import { del, get, patch, post, type Profile, type Vlan } from "../api";
import { ActionButton, DeleteButton, ErrorMsg, Field, Modal } from "../components";

// Profile editor: structured form for main standard-model fields + advanced JSON spec.
// kind = "port" | "device".

interface PortSpec {
  mode: "access" | "trunk";
  pvid: number | null;
  tagged: number[] | "all";
  poe: boolean;
  stp_edge: boolean;
  bpdu_guard: boolean;
  description: string;
}

const defaultPortSpec: PortSpec = {
  mode: "access",
  pvid: null,
  tagged: [],
  poe: true,
  stp_edge: true,
  bpdu_guard: true,
  description: "",
};

export default function Profiles({ kind }: { kind: "port" | "device" }) {
  const { t } = useTranslation();
  const { site } = useSite();
  const siteId = useSiteId();
  const siteSlug = site?.slug ?? "";
  const qc = useQueryClient();
  const key = ["profiles", kind, siteId];
  const title = kind === "port" ? t("profilesPage.titlePort") : t("profilesPage.titleDevice");

  const [editing, setEditing] = useState<Profile | "new" | null>(null);
  const [name, setName] = useState("");
  const [portSpec, setPortSpec] = useState<PortSpec>(defaultPortSpec);
  const [rawSpec, setRawSpec] = useState("{}");
  const [rawMode, setRawMode] = useState(false);

  const { data: profiles, error } = useQuery({
    queryKey: key,
    queryFn: () => get<Profile[]>(`/api/sites/${siteId}/profiles/${kind}`),
    enabled: !!siteId,
  });
  const { data: vlans } = useQuery({
    queryKey: ["vlans", siteId],
    queryFn: () => get<Vlan[]>(`/api/sites/${siteId}/vlans`),
    enabled: !!siteId,
  });

  const buildSpec = (): unknown => {
    if (kind === "device" || rawMode) return JSON.parse(rawSpec);
    return portSpec;
  };

  const save = useMutation({
    mutationFn: () => {
      const body = { name, spec: buildSpec() };
      return editing === "new"
        ? post<Profile>(`/api/sites/${siteId}/profiles/${kind}`, body)
        : patch<Profile>(`/api/profiles/${kind}/${(editing as Profile).id}`, body);
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: key });
      setEditing(null);
    },
  });

  const remove = useMutation({
    mutationFn: (id: string) => del(`/api/profiles/${kind}/${id}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: key }),
  });

  const open = (p: Profile | "new") => {
    setEditing(p);
    save.reset();
    setRawMode(kind === "device");
    if (p === "new") {
      setName("");
      setPortSpec(defaultPortSpec);
      setRawSpec(
        kind === "device"
          ? JSON.stringify(
              {
                stp: { mode: "rstp", priority: 32768 },
                management: { ssh: true, http: false, telnet: false },
                igmp_snooping: true,
              },
              null,
              2,
            )
          : "{}",
      );
    } else {
      setName(p.name);
      setRawSpec(JSON.stringify(p.spec, null, 2));
      if (kind === "port") {
        setPortSpec({ ...defaultPortSpec, ...(p.spec as Partial<PortSpec>) });
      }
    }
  };

  const vlanName = (id: number) =>
    vlans?.find((v) => v.vlan_id === id)?.name ?? t("profilesPage.vlanPrefix", { id });

  const summary = (p: Profile): string => {
    if (kind === "device") return Object.keys(p.spec).join(", ");
    const s = p.spec as Partial<PortSpec>;
    const parts: string[] = [s.mode ?? "?"];
    if (s.pvid) parts.push(t("profilesPage.pvidPrefix", { name: vlanName(s.pvid) }));
    if (Array.isArray(s.tagged) && s.tagged.length > 0)
      parts.push(t("profilesPage.taggedPrefix", { list: s.tagged.map(vlanName).join(", ") }));
    if (s.tagged === "all") parts.push(t("profilesPage.taggedAll"));
    if (s.poe) parts.push("PoE");
    return parts.join(" · ");
  };

  let specValid = true;
  try {
    if (rawMode || kind === "device") JSON.parse(rawSpec);
  } catch {
    specValid = false;
  }

  return (
    <>
      <div className="toolbar">
        <h1>{title}</h1>
        <ActionButton kind="create" onClick={() => open("new")}>{t("profilesPage.createProfile")}</ActionButton>
      </div>
      <ErrorMsg error={error} />
      <div className="card">
        {(profiles ?? []).length === 0 ? (
          <div className="empty">{t("profilesPage.noProfiles")}</div>
        ) : (
          <table>
            <thead>
              <tr>
                <th>{t("profilesPage.nameCol")}</th>
                <th>{t("profilesPage.summaryCol")}</th>
                <th style={{ width: 200 }}></th>
              </tr>
            </thead>
            <tbody>
              {(profiles ?? []).map((p) => (
                <tr key={p.id}>
                  <td>{p.name}</td>
                  <td className="note">{summary(p)}</td>
                  <td style={{ textAlign: "right" }}>
                    <ActionButton kind="edit" className="ghost" onClick={() => open(p)}>
                      {t("profilesPage.edit")}
                    </ActionButton>
                    <DeleteButton onConfirm={() => remove.mutate(p.id)} />
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>

      {editing && (
        <Modal
          title={
            editing === "new"
              ? t("profilesPage.createModal", { title })
              : t("profilesPage.editModal", { name })
          }
          onClose={() => setEditing(null)}
        >
          <Field label={t("profilesPage.profileName")}>
            <input value={name} onChange={(e) => setName(e.target.value)} autoFocus />
          </Field>

          {kind === "port" && !rawMode && (
            <>
              <div className="row">
                <div style={{ flex: 1 }}>
                  <Field label={t("profilesPage.mode")}>
                    <select
                      value={portSpec.mode}
                      onChange={(e) =>
                        setPortSpec({
                          ...portSpec,
                          mode: e.target.value as PortSpec["mode"],
                        })
                      }
                    >
                      <option value="access">access</option>
                      <option value="trunk">trunk</option>
                    </select>
                  </Field>
                </div>
                <div style={{ flex: 1 }}>
                  <Field
                    label={
                      portSpec.mode === "access"
                        ? t("profilesPage.vlanPvid")
                        : t("profilesPage.nativeVlanPvid")
                    }
                  >
                    <select
                      value={portSpec.pvid ?? ""}
                      onChange={(e) =>
                        setPortSpec({
                          ...portSpec,
                          pvid: e.target.value ? Number(e.target.value) : null,
                        })
                      }
                    >
                      <option value="">—</option>
                      {(vlans ?? []).map((v) => (
                        <option key={v.id} value={v.vlan_id}>
                          {v.vlan_id} — {v.name}
                        </option>
                      ))}
                    </select>
                  </Field>
                </div>
              </div>
              {portSpec.mode === "trunk" && (
                <Field label={t("profilesPage.taggedVlans")}>
                  <div className="row">
                    <label className="row" style={{ marginBottom: 0 }}>
                      <input
                        type="checkbox"
                        checked={portSpec.tagged === "all"}
                        onChange={(e) =>
                          setPortSpec({
                            ...portSpec,
                            tagged: e.target.checked ? "all" : [],
                          })
                        }
                      />
                      {t("profilesPage.allVlans")}
                    </label>
                    {portSpec.tagged !== "all" &&
                      (vlans ?? []).map((v) => (
                        <label
                          key={v.id}
                          className="row"
                          style={{ marginBottom: 0 }}
                        >
                          <input
                            type="checkbox"
                            checked={(portSpec.tagged as number[]).includes(
                              v.vlan_id,
                            )}
                            onChange={(e) => {
                              const cur = portSpec.tagged as number[];
                              setPortSpec({
                                ...portSpec,
                                tagged: e.target.checked
                                  ? [...cur, v.vlan_id].sort((a, b) => a - b)
                                  : cur.filter((x) => x !== v.vlan_id),
                              });
                            }}
                          />
                          <span className="vlan-chip" style={{ background: v.color }} />
                          {v.name}
                        </label>
                      ))}
                  </div>
                </Field>
              )}
              <div className="row">
                {(
                  [
                    ["poe", "PoE"],
                    ["stp_edge", "STP edge"],
                    ["bpdu_guard", "BPDU guard"],
                  ] as const
                ).map(([k, label]) => (
                  <label key={k} className="row" style={{ marginBottom: 8 }}>
                    <input
                      type="checkbox"
                      checked={portSpec[k]}
                      onChange={(e) =>
                        setPortSpec({ ...portSpec, [k]: e.target.checked })
                      }
                    />
                    {label}
                  </label>
                ))}
              </div>
              <Field label={t("common.description")}>
                <input
                  value={portSpec.description}
                  onChange={(e) =>
                    setPortSpec({ ...portSpec, description: e.target.value })
                  }
                />
              </Field>
              <ActionButton kind="edit" className="ghost" onClick={() => {
                setRawSpec(JSON.stringify(portSpec, null, 2));
                setRawMode(true);
              }}>
                {t("profilesPage.editAdvancedJson")}
              </ActionButton>
            </>
          )}

          {(kind === "device" || rawMode) && (
            <Field label={t("profilesPage.specJson")}>
              <textarea
                rows={10}
                className="mono"
                value={rawSpec}
                onChange={(e) => setRawSpec(e.target.value)}
                style={{ borderColor: specValid ? undefined : "var(--danger)" }}
              />
            </Field>
          )}

          <ErrorMsg error={save.error} />
          <div className="actions">
            <ActionButton kind="cancel" className="secondary" onClick={() => setEditing(null)}>
              {t("common.cancel")}
            </ActionButton>
            <ActionButton
              kind="save"
              disabled={!name || !specValid || save.isPending}
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
