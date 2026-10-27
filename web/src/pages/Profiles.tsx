import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { useSite, useSiteId } from "../useSite";
import { del, get, patch, post, type Profile, type Vlan } from "../api";
import { ActionButton, ErrorMsg, Field, Modal } from "../components";
import { IconAction, IconDeleteButton } from "../ui/IconAction";

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
  /**
   * Storm control, as declared INTENT rather than a live gesture.
   *
   * It lands at `security.storm_control` in the spec, which is where the
   * standard data model already reserved it — the per-port endpoint's own header
   * names that path as the intent's home before this existed.
   *
   * ONE RATE, three classes, and that is not a simplification for the form's
   * sake: it is the shape of the hardware. RouterOS and SwOS carry a single
   * `storm-rate` field per port with the classes as booleans beside it
   * (CAPTURED on a CRS326: one field, three flags), so a profile offering three
   * independent rates would describe a port those platforms cannot produce, and
   * the difference would only surface as a refusal at apply time.
   */
  storm_control?: StormControlSpec;
}

/** The declared storm-control intent for a port. */
interface StormControlSpec {
  broadcast: boolean;
  multicast: boolean;
  unknown_unicast: boolean;
  /**
   * The rate, in the unit below. Shared by every class switched on above.
   */
  rate: number;
  /**
   * `percent` or `pps`.
   *
   * PERCENT IS THE PORTABLE ONE and the default: a percentage means the same
   * share of the link on every platform, and the two ranges whose field is
   * packets per second (the FS S3400 and S3900) convert it using the port's own
   * line rate and a 64-byte frame. Writing `pps` in a profile pins a number that
   * means a different share of a 1 G port than of a 10 G one — occasionally what
   * an operator wants, and never what they want by accident.
   */
  unit: "percent" | "pps";
}

const defaultPortSpec: PortSpec = {
  mode: "access",
  pvid: null,
  tagged: [],
  poe: true,
  stp_edge: true,
  bpdu_guard: true,
  description: "",
  // ABSENT BY DEFAULT, not "present and disabled". A profile that carried an
  // explicit `enabled: false` would assert that meshcraft wants no limiter, and
  // an apply would then have to REMOVE one an operator set on the switch by
  // hand. Omission means "this profile has no opinion", which is what every
  // existing profile means today and must keep meaning after this field exists.
  storm_control: undefined,
};

/**
 * A checkbox drawn by this stylesheet rather than by the browser.
 *
 * The NATIVE input stays in the DOM and stays focusable — it carries the state,
 * the keyboard behaviour and the accessibility tree; only its painting is
 * replaced (see `.check` in styles.css). Hiding it with `display:none` would
 * take all three away, which is why it is clipped instead.
 */
function Check({
  checked,
  onChange,
  label,
  disabled,
}: {
  checked: boolean;
  onChange: (v: boolean) => void;
  label: React.ReactNode;
  disabled?: boolean;
}) {
  return (
    <label className="check">
      <input
        type="checkbox"
        checked={checked}
        disabled={disabled}
        onChange={(e) => onChange(e.target.checked)}
      />
      <span className="box" aria-hidden="true" />
      <span>{label}</span>
    </label>
  );
}

/** A titled group of related options. */
function OptGroup({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <div className="optgroup">
      <div className="optgroup-head">{title}</div>
      {children}
    </div>
  );
}

/** The rate a newly enabled storm-control block starts at. */
const defaultStormControl: StormControlSpec = {
  broadcast: true,
  multicast: false,
  unknown_unicast: false,
  // 1 % of line rate: enough to pass ARP and discovery on any link in this
  // fleet, far below what a loop produces. It is also the value the operator
  // set by hand on the reference CRS326, so it is a number this fleet has
  // actually run rather than one chosen for looking round.
  rate: 1,
  unit: "percent",
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
                <th className="col-actions"></th>
              </tr>
            </thead>
            <tbody>
              {(profiles ?? []).map((p) => (
                <tr key={p.id}>
                  <td>{p.name}</td>
                  <td className="note">{summary(p)}</td>
                  <td className="col-actions">
                    <div className="icon-actions">
                      <IconAction
                        kind="edit"
                        label={t("profilesPage.edit") as string}
                        onClick={() => open(p)}
                      />
                      <IconDeleteButton onConfirm={() => remove.mutate(p.id)} />
                    </div>
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
              <OptGroup title={t("profilesPage.groupVlan") as string}>
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
              </OptGroup>
              {/* STORM CONTROL — declared intent, not a live gesture.
                  Absent means "this profile has no opinion": switching it off
                  removes the block entirely rather than writing an explicit
                  `false`, so a profile never asserts that a limiter somebody set
                  by hand should be taken away. */}
              <OptGroup title={t("profilesPage.groupStormControl") as string}>
                <Check
                  checked={!!portSpec.storm_control}
                  onChange={(v) =>
                    setPortSpec({
                      ...portSpec,
                      storm_control: v ? { ...defaultStormControl } : undefined,
                    })
                  }
                  label={t("profilesPage.stormControlEnable")}
                />
                {portSpec.storm_control && (
                  <div className="row" style={{ gap: 12, flexWrap: "wrap" }}>
                    {(
                      [
                        ["broadcast", "profilesPage.stormBroadcast"],
                        ["multicast", "profilesPage.stormMulticast"],
                        ["unknown_unicast", "profilesPage.stormUnknownUnicast"],
                      ] as const
                    ).map(([cls, key]) => (
                      <Check
                        key={cls}
                        checked={portSpec.storm_control![cls]}
                        onChange={(v) =>
                          setPortSpec({
                            ...portSpec,
                            storm_control: {
                              ...portSpec.storm_control!,
                              [cls]: v,
                            },
                          })
                        }
                        label={t(key)}
                      />
                    ))}
                    <input
                      type="number"
                      min={0}
                      step="any"
                      style={{ width: 90 }}
                      value={portSpec.storm_control.rate}
                      onChange={(e) =>
                        setPortSpec({
                          ...portSpec,
                          storm_control: {
                            ...portSpec.storm_control!,
                            rate: Number(e.target.value),
                          },
                        })
                      }
                      aria-label={t("profilesPage.stormRate") as string}
                    />
                    <select
                      value={portSpec.storm_control.unit}
                      onChange={(e) =>
                        setPortSpec({
                          ...portSpec,
                          storm_control: {
                            ...portSpec.storm_control!,
                            unit: e.target.value as "percent" | "pps",
                          },
                        })
                      }
                      aria-label={t("profilesPage.stormUnit") as string}
                    >
                      <option value="percent">%</option>
                      <option value="pps">pps</option>
                    </select>
                  </div>
                )}
                {portSpec.storm_control && (
                  <p className="note">
                    {portSpec.storm_control.unit === "percent"
                      ? t("profilesPage.stormPercentHelp")
                      : t("profilesPage.stormPpsHelp")}
                  </p>
                )}
              </OptGroup>
              {/* Grouped by WHO ANSWERS THEM rather than by type. Power, loop
                  protection and storm control are decided by different people at
                  different moments, and a flat row of checkboxes made an operator
                  read all of them to change one. */}
              <OptGroup title={t("profilesPage.groupPower") as string}>
                <div className="row">
                  <Check
                    checked={portSpec.poe}
                    onChange={(v) => setPortSpec({ ...portSpec, poe: v })}
                    label="PoE"
                  />
                </div>
              </OptGroup>
              <OptGroup title={t("profilesPage.groupSpanningTree") as string}>
                <div className="row">
                  <Check
                    checked={portSpec.stp_edge}
                    onChange={(v) => setPortSpec({ ...portSpec, stp_edge: v })}
                    label="STP edge"
                  />
                  <Check
                    checked={portSpec.bpdu_guard}
                    onChange={(v) => setPortSpec({ ...portSpec, bpdu_guard: v })}
                    label="BPDU guard"
                  />
                </div>
              </OptGroup>
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
