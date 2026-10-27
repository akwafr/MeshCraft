import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { useSite, useSiteId } from "../useSite";
import {
  del,
  get,
  patch,
  post,
  type Device,
  type PortIntent,
  type Profile,
  type SwitchProfilePreview,
  type SwitchProfileSpec,
  type SwitchRule,
  type SwitchRuleConfig,
  type Vlan,
} from "../api";
import { useSiteRole } from "../auth";
import {
  ActionButton,
  ErrorMsg,
  Field,
  HelpTip,
  QuickTip,
  useToast,
} from "../components";
import { IconDeleteButton } from "../ui/IconAction";
import { PortSquare } from "../netui";
import { vlanMap } from "../network";

/**
 * Switch profiles: describe the facade of equipment using RULES.
 *
 * This screen replaces the equipment-groups screen
 * (docs/en/01-Architecture/Switch profiles). It is organized into three
 * zones, and the order is not decorative:
 *
 *   1. the site's list of profiles;
 *   2. the rule editor, ORDERED — the first rule that claims a port wins,
 *      so a rule must be movable without having to be rewritten;
 *   3. a PREVIEW on a real device, permanently visible on the right.
 *
 * The preview is not an option we unfold on demand: applying a set of
 * rules to forty-eight ports without showing the result would go against
 * everything else in the product. It is computed by the CONTROLLER
 * (`switch-profile/preview`) and not here — redoing the evaluation in
 * TypeScript would produce a second arbitration that would diverge from
 * the first exactly where no one is looking.
 */

function mediaLabel(t: (k: string) => string, key: string): string {
  const m: Record<string, string> = {
    any: t("switchProfilesPage.mediaAll"),
    copper: t("switchProfilesPage.mediaCopper"),
    sfp: t("switchProfilesPage.mediaSfp"),
    sfp_plus: t("switchProfilesPage.mediaSfpPlus"),
  };
  return m[key] ?? key;
}

function poeLabel(t: (k: string) => string, key: string): string {
  const m: Record<string, string> = {
    any: t("switchProfilesPage.poeAny"),
    yes: t("switchProfilesPage.poeYes"),
    no: t("switchProfilesPage.poeNo"),
  };
  return m[key] ?? key;
}

function sliceLabel(t: (k: string) => string, key: string): string {
  const m: Record<string, string> = {
    all: t("switchProfilesPage.sliceAll"),
    first: t("switchProfilesPage.sliceFirst"),
    last: t("switchProfilesPage.sliceLast"),
    range: t("switchProfilesPage.sliceRange"),
  };
  return m[key] ?? key;
}

/**
 * Rule-identification tints. They carry no meaning of their own — they
 * only exist to connect an editor row to the ports it has claimed on the
 * facade. Distinct from the VLAN palette, which does carry meaning.
 */
const RULE_TINTS = [
  "#3987e5", "#d95926", "#199e70", "#c98500",
  "#d55181", "#9085e9", "#5bb85b", "#8a5a2b",
];
const tintOf = (i: number) => RULE_TINTS[i % RULE_TINTS.length];

const emptyRule = (): SwitchRule => ({
  label: "",
  select: { media: "any", poe: "any", slice: "all" },
  config: { mode: "access", pvid: null, tagged: [], poe: false },
});

/** Human-readable summary of a selector: this is what gets reread, not the raw JSON. */
function selectorText(t: (k: string, o?: Record<string, unknown>) => string, r: SwitchRule): string {
  const s = r.select;
  const parts: string[] = [];
  parts.push(mediaLabel(t, s.media ?? "any") ?? t("switchProfilesPage.mediaAll"));
  if (s.poe && s.poe !== "any") parts.push(poeLabel(t, s.poe));
  if (s.group) parts.push(t("switchProfilesPage.groupContains", { name: s.group }));
  let head = "";
  if (s.slice === "first") head = t("switchProfilesPage.sliceFirstCount", { count: s.count ?? 0 }) + " · ";
  if (s.slice === "last") head = t("switchProfilesPage.sliceLastCount", { count: s.count ?? 0 }) + " · ";
  if (s.slice === "range") head = t("switchProfilesPage.sliceRangeCount", { from: s.from ?? 1, to: s.to || t("switchProfilesPage.sliceEnd") }) + " · ";
  return head + parts.join(" · ");
}

/** Summary of a socket configuration, in the operator's words. */
function configText(t: (k: string, o?: Record<string, unknown>) => string, c: PortIntent | SwitchRuleConfig | undefined): string {
  if (!c || !c.mode) return "—";
  if (c.mode === "libre") return t("switchProfilesPage.freeUnmanaged");
  if (c.mode === "access") return t("switchProfilesPage.accessVlan", { id: c.pvid ?? "—" });
  const members =
    c.tagged === "all" ? t("switchProfilesPage.allSiteVlans") : ((c.tagged ?? []) as number[]).join(" · ") || t("switchProfilesPage.noneTagged");
  return t("switchProfilesPage.trunkNative", { id: c.pvid ?? 1, members });
}

export default function SwitchProfiles() {
  const { t, i18n } = useTranslation();
  const { site } = useSite();
  const siteId = useSiteId();
  const siteSlug = site?.slug ?? "";
  const ing = useSiteRole(siteId) === "ing";
  const qc = useQueryClient();
  const [toast, showToast] = useToast();

  const [selected, setSelected] = useState<string | null>(null);
  const [draft, setDraft] = useState<{
    name: string;
    tag: string;
    spec: SwitchProfileSpec;
  } | null>(null);
  const [deviceId, setDeviceId] = useState<string>("");
  const [hoverRule, setHoverRule] = useState<number | null>(null);

  const key = ["profiles", "switch", siteId];
  const { data: profiles, error } = useQuery({
    queryKey: key,
    queryFn: () => get<Profile[]>(`/api/sites/${siteId}/profiles/switch`),
    enabled: !!siteId,
  });
  const { data: portProfiles } = useQuery({
    queryKey: ["profiles", "port", siteId],
    queryFn: () => get<Profile[]>(`/api/sites/${siteId}/profiles/port`),
    enabled: !!siteId,
  });
  const { data: devices } = useQuery({
    queryKey: ["devices", siteId],
    queryFn: () => get<Device[]>(`/api/sites/${siteId}/devices`),
    enabled: !!siteId,
  });
  const { data: vlans } = useQuery({
    queryKey: ["vlans", siteId],
    queryFn: () => get<Vlan[]>(`/api/sites/${siteId}/vlans`),
    enabled: !!siteId,
  });

  // The preview needs a device: we select one automatically rather than
  // showing a blank screen that would suggest the feature is broken.
  useEffect(() => {
    if (!deviceId && devices && devices.length > 0) setDeviceId(devices[0].id);
  }, [devices, deviceId]);

  const invalidate = () => qc.invalidateQueries({ queryKey: key });

  const save = useMutation({
    mutationFn: () => {
      const body = { name: draft!.name, spec: draft!.spec, tag: draft!.tag };
      return selected
        ? patch<Profile>(`/api/profiles/switch/${selected}`, body)
        : post<Profile>(`/api/sites/${siteId}/profiles/switch`, body);
    },
    onSuccess: (p) => {
      invalidate();
      // Enrollment tag feeds device-page autocomplete: without this, the
      // suggestion list stays stale until the next full site-tags fetch.
      qc.invalidateQueries({ queryKey: ["site-tags", siteId] });
      setSelected(p.id);
      setDraft({
        name: p.name,
        tag: p.tag ?? "",
        spec: (p.spec ?? {}) as unknown as SwitchProfileSpec,
      });
      showToast(t("switchProfilesPage.savedToast", { name: p.name }));
    },
  });

  const remove = useMutation({
    mutationFn: (id: string) => del(`/api/profiles/switch/${id}`),
    onSuccess: () => {
      invalidate();
      qc.invalidateQueries({ queryKey: ["site-tags", siteId] });
      setSelected(null);
      setDraft(null);
    },
  });

  const openProfile = (p: Profile | null) => {
    save.reset();
    setSelected(p?.id ?? null);
    const spec = (p?.spec ?? {}) as unknown as SwitchProfileSpec;
    setDraft({
      name: p?.name ?? "",
      tag: p?.tag ?? "",
      spec: { description: spec.description ?? "", rules: spec.rules ?? [] },
    });
  };

  return (
    <div className="page2col wide-panel">
      <main>
        <div className="toolbar">
          <h1>
            {t("switchProfilesPage.title")}
            <span className="sub">{t("switchProfilesPage.subtitle")}</span>
          </h1>
          {ing && (
            <ActionButton kind="create" onClick={() => openProfile(null)}>
              {t("switchProfilesPage.newProfile")}
            </ActionButton>
          )}
        </div>
        {!ing && (
          <div className="note" style={{ margin: "0 0 12px" }}>
            {t("switchProfilesPage.readOnly")}
          </div>
        )}
        <ErrorMsg error={error} />

        <div className="card">
          <table>
            <thead>
              <tr>
                <th>{t("switchProfilesPage.profileCol")}</th>
                <th>{t("switchProfilesPage.tagCol")}</th>
                <th>{t("switchProfilesPage.rulesCol")}</th>
                <th className="col-actions"></th>
              </tr>
            </thead>
            <tbody>
              {(profiles ?? []).map((p) => {
                const spec = p.spec as unknown as SwitchProfileSpec;
                return (
                  <tr key={p.id} style={{ background: p.id === selected ? "var(--bg-hover)" : undefined }}>
                    <td>
                      <div style={{ fontWeight: 700, fontSize: 13 }}>{p.name}</div>
                      <div className="note">{spec.description}</div>
                    </td>
                    <td className="mono note">{p.tag || "—"}</td>
                    <td className="note">{(spec.rules ?? []).length} {t("switchProfilesPage.rulesCol").toLowerCase()}</td>
                    <td className="col-actions">
                      <div className="icon-actions">
                        {/* THE ONE LABEL THAT STAYS A WORD on this screen: it
                            says "Edit" to an engineer and "View" to a
                            viewer, and there is no glyph for "consult without
                            changing". A pencil would promise an edit the role
                            refuses — the icon would not compress the label, it
                            would contradict it. */}
                        <ActionButton kind="edit" className="ghost" onClick={() => openProfile(p)}>
                          {ing ? t("common.edit") : t("switchProfilesPage.view")}
                        </ActionButton>
                        {ing && <IconDeleteButton onConfirm={() => remove.mutate(p.id)} />}
                      </div>
                    </td>
                  </tr>
                );
              })}
              {(profiles ?? []).length === 0 && (
                <tr>
                  <td colSpan={4} className="empty">
                    {t("switchProfilesPage.noProfiles")}
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </div>

        {draft && (
          <RuleEditor
            draft={draft}
            setDraft={setDraft}
            portProfiles={portProfiles ?? []}
            vlans={vlans ?? []}
            readOnly={!ing}
            hoverRule={hoverRule}
            onHoverRule={setHoverRule}
            onClose={() => {
              setDraft(null);
              setSelected(null);
            }}
            onSave={() => save.mutate()}
            saving={save.isPending}
            saveError={save.error}
          />
        )}
      </main>

      <aside className="side-panel">
        <Preview
          siteId={siteId!}
          devices={devices ?? []}
          deviceId={deviceId}
          onDevice={setDeviceId}
          spec={draft?.spec ?? null}
          profileId={draft ? null : selected}
          vlans={vlans ?? []}
          hoverRule={hoverRule}
          onHoverRule={setHoverRule}
          canApply={ing}
          onApplied={(n) => showToast(t("switchProfilesPage.portsConfiguredToast", { count: n }))}
        />
      </aside>
      {toast}
    </div>
  );
}

// --- Rules editor ---

function RuleEditor({
  draft,
  setDraft,
  portProfiles,
  vlans,
  readOnly,
  hoverRule,
  onHoverRule,
  onClose,
  onSave,
  saving,
  saveError,
}: {
  draft: { name: string; tag: string; spec: SwitchProfileSpec };
  setDraft: (d: { name: string; tag: string; spec: SwitchProfileSpec }) => void;
  portProfiles: Profile[];
  vlans: Vlan[];
  readOnly: boolean;
  hoverRule: number | null;
  onHoverRule: (i: number | null) => void;
  onClose: () => void;
  onSave: () => void;
  saving: boolean;
  saveError: unknown;
}) {
  const { t } = useTranslation();
  const rules = draft.spec.rules;
  const setRules = (next: SwitchRule[]) =>
    setDraft({ ...draft, spec: { ...draft.spec, rules: next } });

  const patchRule = (i: number, next: Partial<SwitchRule>) =>
    setRules(rules.map((r, k) => (k === i ? { ...r, ...next } : r)));

  /** Moves a rule: ORDER is the conflict-resolution mechanism, so order is what gets edited. */
  const move = (i: number, delta: number) => {
    const j = i + delta;
    if (j < 0 || j >= rules.length) return;
    const next = rules.slice();
    [next[i], next[j]] = [next[j], next[i]];
    setRules(next);
  };

  return (
    <div className="card" style={{ marginTop: 14 }}>
      <div className="row" style={{ gap: 10, alignItems: "flex-end" }}>
        <div style={{ flex: 1 }}>
          <Field label={t("switchProfilesPage.profileName") as string}>
            <input
              value={draft.name}
              disabled={readOnly}
              onChange={(e) => setDraft({ ...draft, name: e.target.value })}
              placeholder={t("switchProfilesPage.namePlaceholder")}
            />
          </Field>
        </div>
        <div style={{ flex: 1 }}>
          <Field
            label={t("switchProfilesPage.tagLabel") as string}
            help={<p>{t("switchProfilesPage.tagHelp")}</p>}
          >
            <input
              className="mono"
              value={draft.tag}
              disabled={readOnly}
              onChange={(e) =>
                setDraft({ ...draft, tag: e.target.value.toLowerCase() })
              }
              placeholder={t("switchProfilesPage.tagPlaceholder") as string}
            />
          </Field>
        </div>
        <div style={{ flex: 2 }}>
          <Field label={t("common.description") as string}>
            <input
              value={draft.spec.description ?? ""}
              disabled={readOnly}
              onChange={(e) =>
                setDraft({ ...draft, spec: { ...draft.spec, description: e.target.value } })
              }
            />
          </Field>
        </div>
      </div>

      <div className="kicker" style={{ margin: "12px 0 10px" }}>
        {t("switchProfilesPage.rulesFirstWins")}
        <HelpTip label={t("switchProfilesPage.rulesOrderHelpLabel")}>
          <p>{t("switchProfilesPage.rulesOrderHelp1")}</p>
          <p>{t("switchProfilesPage.rulesOrderHelp2")}</p>
        </HelpTip>
      </div>

      {rules.map((r, i) => (
        <div
          key={i}
          onMouseEnter={() => onHoverRule(i)}
          onMouseLeave={() => onHoverRule(null)}
          style={{
            borderLeft: `3px solid ${tintOf(i)}`,
            background: hoverRule === i ? "var(--bg-hover)" : undefined,
            padding: "8px 10px",
            marginBottom: 8,
            borderRadius: 3,
          }}
        >
          <div className="row" style={{ gap: 8, marginBottom: 6 }}>
            <span className="note" style={{ width: 18, fontWeight: 700 }}>
              {i + 1}
            </span>
            <input
              style={{ flex: 1, minHeight: 30, fontSize: 12 }}
              value={r.label}
              disabled={readOnly}
              placeholder={t("switchProfilesPage.ruleNamePlaceholder")}
              onChange={(e) => patchRule(i, { label: e.target.value })}
            />
            {!readOnly && (
              <>
                <QuickTip text={t("switchProfilesPage.moveUp")} className="quicktip-inline">
                  <button className="ghost" onClick={() => move(i, -1)}>
                    ▲
                  </button>
                </QuickTip>
                <QuickTip text={t("switchProfilesPage.moveDown")} className="quicktip-inline">
                  <button className="ghost" onClick={() => move(i, 1)}>
                    ▼
                  </button>
                </QuickTip>
                <QuickTip text={t("switchProfilesPage.deleteRule")} className="quicktip-inline">
                  <button
                    className="ghost"
                    onClick={() => setRules(rules.filter((_, k) => k !== i))}
                  >
                    ✕
                  </button>
                </QuickTip>
              </>
            )}
          </div>

          <div className="row" style={{ gap: 8, flexWrap: "wrap" }}>
            <select
              disabled={readOnly}
              value={r.select.media ?? "any"}
              onChange={(e) => patchRule(i, { select: { ...r.select, media: e.target.value as never } })}
            >
              {(["any","copper","sfp","sfp_plus"] as const).map((v) => (
                <option key={v} value={v}>
                  {mediaLabel(t, v)}
                </option>
              ))}
            </select>
            <select
              disabled={readOnly}
              value={r.select.poe ?? "any"}
              onChange={(e) => patchRule(i, { select: { ...r.select, poe: e.target.value as never } })}
            >
              {(["any","yes","no"] as const).map((v) => (
                <option key={v} value={v}>
                  {poeLabel(t, v)}
                </option>
              ))}
            </select>
            <QuickTip
              text={t("switchProfilesPage.facadeHint1") + t("switchProfilesPage.facadeHint2")}
              className="quicktip-inline"
            >
              <input
                style={{ width: 150, minHeight: 30, fontSize: 12 }}
                disabled={readOnly}
                value={r.select.group ?? ""}
                placeholder={t("switchProfilesPage.facadeGroupPlaceholder")}
                onChange={(e) => patchRule(i, { select: { ...r.select, group: e.target.value } })}
              />
            </QuickTip>
            <select
              disabled={readOnly}
              value={r.select.slice ?? "all"}
              onChange={(e) => patchRule(i, { select: { ...r.select, slice: e.target.value as never } })}
            >
              {(["all","first","last","range"] as const).map((v) => (
                <option key={v} value={v}>
                  {sliceLabel(t, v)}
                </option>
              ))}
            </select>
            {(r.select.slice === "first" || r.select.slice === "last") && (
              <input
                type="number"
                min={1}
                style={{ width: 70, minHeight: 30, fontSize: 12 }}
                disabled={readOnly}
                value={r.select.count ?? 1}
                onChange={(e) =>
                  patchRule(i, { select: { ...r.select, count: Number(e.target.value) } })
                }
              />
            )}
            {r.select.slice === "range" && (
              <>
                <input
                  type="number"
                  min={1}
                  style={{ width: 70, minHeight: 30, fontSize: 12 }}
                  disabled={readOnly}
                  value={r.select.from ?? 1}
                  onChange={(e) =>
                    patchRule(i, { select: { ...r.select, from: Number(e.target.value) } })
                  }
                />
                <QuickTip text={t("switchProfilesPage.sliceEndTitle")} className="quicktip-inline">
                  <input
                    type="number"
                    min={0}
                    style={{ width: 70, minHeight: 30, fontSize: 12 }}
                    disabled={readOnly}
                    value={r.select.to ?? 0}
                    onChange={(e) =>
                      patchRule(i, { select: { ...r.select, to: Number(e.target.value) } })
                    }
                  />
                </QuickTip>
              </>
            )}
          </div>

          <div className="row" style={{ gap: 8, marginTop: 6, flexWrap: "wrap" }}>
            <span className="note" style={{ width: 60 }}>
              {t("switchProfilesPage.receives")}
            </span>
            <select
              disabled={readOnly}
              value={r.port_profile_id ?? ""}
              onChange={(e) =>
                patchRule(i, {
                  port_profile_id: e.target.value || undefined,
                  config: e.target.value ? undefined : (r.config ?? emptyRule().config),
                })
              }
            >
              <option value="">{t("switchProfilesPage.inlineConfig")}</option>
              {portProfiles.map((p) => (
                <option key={p.id} value={p.id}>
                  {t("switchProfilesPage.portProfileOption", { name: p.name })}
                </option>
              ))}
            </select>
            {!r.port_profile_id && (
              <InlineConfig
                value={r.config ?? emptyRule().config!}
                vlans={vlans}
                readOnly={readOnly}
                onChange={(c) => patchRule(i, { config: c })}
              />
            )}
          </div>
          <div className="note" style={{ marginTop: 4, fontSize: 11 }}>
            {selectorText(t, r)}
          </div>
        </div>
      ))}

      {!readOnly && (
        <ActionButton kind="add" className="ghost" onClick={() => setRules([...rules, emptyRule()])}>
          {t("switchProfilesPage.addRule")}
        </ActionButton>
      )}

      <ErrorMsg error={saveError} />
      <div className="actions">
        <ActionButton kind="close" className="secondary" onClick={onClose}>
          {t("common.closeAction")}
        </ActionButton>
        {!readOnly && (
          <ActionButton kind="save" disabled={!draft.name.trim() || saving} onClick={onSave}>
            {t("common.save")}
          </ActionButton>
        )}
      </div>
    </div>
  );
}

/** Configuration written directly on a rule, when no port profile matches. */
function InlineConfig({
  value,
  vlans,
  readOnly,
  onChange,
}: {
  value: SwitchRuleConfig;
  vlans: Vlan[];
  readOnly: boolean;
  onChange: (c: SwitchRuleConfig) => void;
}) {
  const { t } = useTranslation();
  const isAll = value.tagged === "all";
  return (
    <>
      <select
        disabled={readOnly}
        value={value.mode}
        onChange={(e) => onChange({ ...value, mode: e.target.value as SwitchRuleConfig["mode"] })}
      >
        <option value="access">{t("switchProfilesPage.modeAccess")}</option>
        <option value="trunk">{t("switchProfilesPage.modeTrunk")}</option>
        <option value="libre">{t("switchProfilesPage.modeFree")}</option>
      </select>
      {value.mode !== "libre" && (
        <select
          disabled={readOnly}
          value={value.pvid ?? ""}
          onChange={(e) => onChange({ ...value, pvid: e.target.value ? Number(e.target.value) : null })}
        >
          <option value="">{t("switchProfilesPage.vlanDash")}</option>
          {vlans.map((v) => (
            <option key={v.id} value={v.vlan_id}>
              {v.vlan_id} — {v.name}
            </option>
          ))}
        </select>
      )}
      {value.mode === "trunk" && (
        <>
          <label className="row" style={{ marginBottom: 0, gap: 4 }}>
            <input
              type="checkbox"
              disabled={readOnly}
              checked={isAll}
              onChange={(e) => onChange({ ...value, tagged: e.target.checked ? "all" : [] })}
            />
            <span className="note">{t("switchProfilesPage.allVlansFloor")}</span>
          </label>
          {!isAll && (
            <span className="row" style={{ gap: 4, flexWrap: "wrap", marginBottom: 0 }}>
              {vlans.map((v) => {
                const on = ((value.tagged ?? []) as number[]).includes(v.vlan_id);
                return (
                  <button
                    key={v.id}
                    className="ghost"
                    disabled={readOnly}
                    style={{
                      padding: "1px 6px",
                      fontSize: 11,
                      borderColor: v.color,
                      color: on ? "var(--text)" : "var(--text-dim)",
                      background: on ? v.color + "33" : "transparent",
                    }}
                    onClick={() => {
                      const cur = ((value.tagged ?? []) as number[]).slice();
                      const k = cur.indexOf(v.vlan_id);
                      if (k > -1) cur.splice(k, 1);
                      else cur.push(v.vlan_id);
                      cur.sort((a, b) => a - b);
                      onChange({ ...value, tagged: cur });
                    }}
                  >
                    {v.vlan_id}
                  </button>
                );
              })}
            </span>
          )}
        </>
      )}
      <label className="row" style={{ marginBottom: 0, gap: 4 }}>
        <input
          type="checkbox"
          disabled={readOnly || value.mode === "libre"}
          checked={!!value.poe}
          onChange={(e) => onChange({ ...value, poe: e.target.checked })}
        />
        <span className="note">PoE</span>
      </label>
    </>
  );
}

// --- {t("switchProfilesPage.previewTitle")} ---

function Preview({
  siteId,
  devices,
  deviceId,
  onDevice,
  spec,
  profileId,
  vlans,
  hoverRule,
  onHoverRule,
  canApply,
  onApplied,
}: {
  siteId: string;
  devices: Device[];
  deviceId: string;
  onDevice: (id: string) => void;
  spec: SwitchProfileSpec | null;
  profileId: string | null;
  vlans: Vlan[];
  hoverRule: number | null;
  onHoverRule: (i: number | null) => void;
  canApply: boolean;
  onApplied: (n: number) => void;
}) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const vm = vlanMap(vlans);
  const live = useMemo(
    () => (spec ? { spec } : profileId ? { profile_id: profileId } : null),
    [spec, profileId],
  );

  /**
   * Rules sent to the preview, slightly debounced behind typing.
   *
   * Without this delay, typing a rule name would trigger a per-keystroke
   * evaluation. And this exact snapshot is also what gets applied — not the
   * latest state of the fields: applying something other than what the
   * preview is showing at the moment of the click would make the preview
   * meaningless, even for a quarter-second's difference.
   */
  const [body, setBody] = useState(live);
  useEffect(() => {
    const t = setTimeout(() => setBody(live), 250);
    return () => clearTimeout(t);
  }, [live]);

  const { data, error, isFetching } = useQuery({
    // The key carries the rules themselves: the preview must track the
    // edit in progress, not the last saved state. It's the only way to see
    // the result before saving — and so avoid saving only to discover we
    // got it wrong.
    queryKey: ["switch-preview", deviceId, JSON.stringify(body)],
    queryFn: () =>
      post<SwitchProfilePreview>(`/api/devices/${deviceId}/switch-profile/preview`, body),
    enabled: !!deviceId && !!body,
  });

  const apply = useMutation({
    mutationFn: () =>
      post<SwitchProfilePreview>(`/api/devices/${deviceId}/switch-profile/apply`, body),
    onSuccess: (res) => {
      qc.invalidateQueries({ queryKey: ["devices", siteId] });
      onApplied(res.applied);
    },
  });

  const byPort = useMemo(() => {
    const m = new Map<string, SwitchProfilePreview["plan"]["ports"][number]>();
    (data?.plan.ports ?? []).forEach((a) => m.set(a.port, a));
    return m;
  }, [data]);

  if (!body) {
    return (
      <div className="note">
        {t("switchProfilesPage.selectForPreview")}
      </div>
    );
  }

  const facade = data?.facade ?? [];
  const blocked = (data?.plan.issues ?? []).length > 0;

  return (
    <>
      <div className="kicker">
        {t("switchProfilesPage.previewTitle")}
        <HelpTip label={t("switchProfilesPage.previewHelpLabel")}>
          <p>{t("switchProfilesPage.previewHelp")}</p>
        </HelpTip>
      </div>
      <div className="note" style={{ margin: "2px 0 8px" }}>
        {t("switchProfilesPage.enrollViaTag")}
      </div>
      <select value={deviceId} onChange={(e) => onDevice(e.target.value)}>
        {devices.map((d) => (
          <option key={d.id} value={d.id}>
            {d.name} — {d.model || t("switchProfilesPage.unknownModel")}
          </option>
        ))}
      </select>
      <ErrorMsg error={error} />
      <ErrorMsg error={apply.error} />

      {facade.length === 0 && !isFetching && (
        <div className="empty" style={{ marginTop: 10 }}>
          {t("switchProfilesPage.noFacadePorts")}
        </div>
      )}

      {/* Facade colored by VLAN like everywhere else: operator reads a port cell
          the same way on every screen. */}
      <div style={{ display: "flex", flexWrap: "wrap", gap: 3, margin: "12px 0" }}>
        {facade.map((p) => {
          const a = byPort.get(p.name);
          const dimmed = hoverRule != null && a?.rule !== hoverRule;
          const intent: PortIntent = a?.config ?? { mode: "libre" };
          return (
            <QuickTip
              key={p.name}
              text={
                p.display +
                " · " +
                (a ? `${a.rule_label} → ${configText(t, a.config)}` : t("switchProfilesPage.noRuleUnchanged")) +
                (a?.warn ? "\n" + a.warn : "")
              }
              className="quicktip-inline"
            >
              <span
                style={{ opacity: dimmed ? 0.12 : 1, transition: "opacity .12s" }}
              >
                {/* 24, not 22: `PortSquare` gates its port NUMBER at
                    `size >= 24` (netui.tsx), and this call site was the only
                    one in the frontend sitting under the gate — two pixels
                    short, so this facade drew unnumbered squares while every
                    other facade numbered them. Raising the call site rather
                    than lowering the gate keeps ONE threshold for the whole
                    frontend: lowering it would have re-flowed all five
                    PortSquare callers to prove one page. (2026-08-09,
                    UI-GUIDE §7.) */}
                <PortSquare name={p.name} displayName={p.display} port={intent} vm={vm} size={24} />
              </span>
            </QuickTip>
          );
        })}
      </div>

      {blocked && (
        <div className="error" style={{ marginBottom: 8 }}>
          {(data?.plan.issues ?? []).map((m, i) => (
            <div key={i}>{m}</div>
          ))}
        </div>
      )}

      <div className="kicker" style={{ marginTop: 4 }}>{t("switchProfilesPage.whatEachRuleGets")}</div>
      <table style={{ fontSize: 11 }}>
        <tbody>
          {(data?.plan.rules ?? []).map((rr) => (
            <tr
              key={rr.index}
              onMouseEnter={() => onHoverRule(rr.index)}
              onMouseLeave={() => onHoverRule(null)}
            >
              <td style={{ width: 6, background: tintOf(rr.index), padding: 0 }} />
              <td>{rr.label}</td>
              <td className="note" style={{ whiteSpace: "nowrap" }}>
                {rr.problem ? (
                  <span style={{ color: "var(--danger)" }}>{rr.problem}</span>
                ) : rr.won === rr.matched ? (
                  t("switchProfilesPage.portsCount", { count: rr.won })
                ) : (
                  // The rule matched more ports than it won: a rule placed
                  // higher already claimed some. This is exactly the case
                  // that must be shown, otherwise the rule looks wrong even
                  // though the reason is hidden.
                  t("switchProfilesPage.wonOfMatched", { won: rr.won, matched: rr.matched })
                )}
              </td>
            </tr>
          ))}
        </tbody>
      </table>

      <div className="note" style={{ margin: "8px 0" }}>
        {t("switchProfilesPage.uncoveredPorts", { count: (data?.plan.uncovered ?? []).length })}
        {(data?.site_vlans ?? []).length === 0 && (
          <>
            {" "}{t("switchProfilesPage.noVlansTrunkWarning")}
          </>
        )}
      </div>

      <div className="kicker" style={{ marginTop: 8 }}>{t("switchProfilesPage.perPort")}</div>
      <div style={{ maxHeight: 260, overflow: "auto" }}>
        <table style={{ fontSize: 11 }}>
          <tbody>
            {facade.map((p) => {
              const a = byPort.get(p.name);
              return (
                <tr key={p.name} style={{ opacity: hoverRule != null && a?.rule !== hoverRule ? 0.3 : 1 }}>
                  <td style={{ fontWeight: 600, whiteSpace: "nowrap" }}>{p.name}</td>
                  <td className="note">{a?.rule_label ?? <em>{t("switchProfilesPage.noRule")}</em>}</td>
                  <td className="note">
                    {a ? configText(t, a.config) : t("switchProfilesPage.unchanged")}
                    {a?.warn && (
                      <div style={{ color: "var(--warn)" }}>{a.warn}</div>
                    )}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>

      {canApply && (
        <div className="actions" style={{ flexDirection: "column", alignItems: "stretch", gap: 6 }}>
          <div className="note">{t("switchProfilesPage.applyOnceHint")}</div>
          <ActionButton
            kind="apply"
            className="secondary"
            disabled={blocked || apply.isPending || (data?.plan.ports ?? []).length === 0}
            onClick={() => apply.mutate()}
          >
            {t("switchProfilesPage.applyOnce", { device: data?.device ?? t("switchProfilesPage.thisDevice") })}
          </ActionButton>
        </div>
      )}
    </>
  );
}













