import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { useSite, useSiteId } from "../useSite";
import { del, get, post, put } from "../api";
import { useSiteRole } from "../auth";
import {
  ActionButton,
  ErrorMsg,
  Field,
  HelpTip,
  Modal,
  Switch,
  useToast,
} from "../components";
import { IconAction, IconDeleteButton } from "../ui/IconAction";

/**
 * Site tags and automatic tagging.
 * See docs/en/01-Architecture/Tags and credential scope.
 *
 * A tag isn't decorative: an administration credential can target it, and a
 * tagged device then changes its expected password. The screen says so
 * rather than leaving the surprise to the operator.
 */

interface TagMatch {
  vendor?: string;
  model?: string;
  location?: string;
  ip_prefix?: string;
}

interface TagRule {
  id: string;
  site_id: string;
  tag: string;
  priority: number;
  enabled: boolean;
  match: TagMatch;
  note: string;
  problems?: string[];
}

interface TagChange {
  device_id: string;
  name: string;
  add?: { tag: string; source: string }[];
  remove?: { tag: string; source: string }[];
}

export default function TagsPage() {
  const { t } = useTranslation();
  const { site } = useSite();
  const siteId = useSiteId();
  const siteSlug = site?.slug ?? "";
  const qc = useQueryClient();
  const [toast, showToast] = useToast();
  const canEdit = useSiteRole(siteId) === "ing";
  const [editing, setEditing] = useState<TagRule | "new" | null>(null);
  const [preview, setPreview] = useState<TagChange[] | null>(null);

  const CRITERIA: { key: keyof TagMatch; label: string; hint: string }[] = [
    { key: "vendor", label: t("tagsPage.vendorContains"), hint: "aruba" },
    { key: "model", label: t("tagsPage.modelContains"), hint: "1930" },
    { key: "location", label: t("tagsPage.locationContains"), hint: t("tagsPage.locationHint") },
    { key: "ip_prefix", label: t("tagsPage.inRangeCidr"), hint: "10.100.0.0/24" },
  ];

  const summary = (m: TagMatch): string => {
    const parts = CRITERIA.filter((c) => (m[c.key] ?? "").trim() !== "").map(
      (c) => t("tagsPage.criterionPart", { label: c.label.toLowerCase(), value: m[c.key] }),
    );
    return parts.length ? parts.join(` ${t("tagsPage.and")} `) : t("tagsPage.noCriterion");
  };

  const { data, error } = useQuery({
    queryKey: ["tag-rules", siteId],
    queryFn: () => get<{ rules: TagRule[] }>(`/api/sites/${siteId}/tag-rules`),
    enabled: !!siteId,
  });
  const { data: tags } = useQuery({
    queryKey: ["site-tags", siteId],
    queryFn: () => get<{ tags: string[] }>(`/api/sites/${siteId}/tags`),
    enabled: !!siteId,
  });

  const invalidate = () => {
    qc.invalidateQueries({ queryKey: ["tag-rules", siteId] });
    qc.invalidateQueries({ queryKey: ["site-tags", siteId] });
    qc.invalidateQueries({ queryKey: ["devices", siteId] });
  };

  const save = useMutation({
    mutationFn: (r: Partial<TagRule>) => put<TagRule>(`/api/sites/${siteId}/tag-rules`, r),
    onSuccess: () => {
      invalidate();
      setEditing(null);
      showToast(t("tagsPage.ruleSavedToast"));
    },
  });
  const remove = useMutation({
    mutationFn: (r: TagRule) => del(`/api/sites/${siteId}/tag-rules/${r.id}`),
    onSuccess: () => {
      invalidate();
      showToast(t("tagsPage.ruleDeletedToast"));
    },
  });
  const evaluate = useMutation({
    mutationFn: (apply: boolean) =>
      post<{ changes: TagChange[]; added?: number; removed?: number }>(
        `/api/sites/${siteId}/tag-rules/apply`, { apply }),
    onSuccess: (res, apply) => {
      if (!apply) {
        setPreview(res.changes);
        if (res.changes.length === 0) showToast(t("tagsPage.nothingToChangeToast"));
        return;
      }
      setPreview(null);
      invalidate();
      showToast(t("tagsPage.appliedToast", { added: res.added ?? 0, removed: res.removed ?? 0 }));
    },
  });

  if (!siteId) return <div className="empty">{t("pipelines.chooseASite")}</div>;
  if (error) return <ErrorMsg error={error} />;
  const rules = data?.rules ?? [];

  return (
    <>
      <div className="toolbar">
        <h1>
          {t("nav.tags")}
          <HelpTip label={t("tagsPage.helpLabel") as string}>
            <p>{t("tagsPage.help1")}</p>
            <p>{t("tagsPage.help2")}</p>
            <p>{t("tagsPage.help3")}</p>
          </HelpTip>
          <span className="sub">{t("tagsPage.subtitle")}</span>
        </h1>
        {canEdit && (
          <ActionButton kind="create" onClick={() => setEditing("new")}>
            {t("tagsPage.newRule")}
          </ActionButton>
        )}
      </div>

      <div className="card">
        <div className="card-head">
          <div className="kicker">{t("tagsPage.autoTaggingRules")}</div>
          <span className="row" style={{ gap: 8 }}>
            <ActionButton
              kind="scan"
              className="secondary"
              disabled={evaluate.isPending || rules.length === 0}
              onClick={() => evaluate.mutate(false)}
            >
              {t("tagsPage.preview")}
            </ActionButton>
            {canEdit && (
              <ActionButton
                kind="apply"
                disabled={evaluate.isPending || !preview || preview.length === 0}
                title={
                  (preview
                    ? t("tagsPage.applyShownChanges")
                    : t("tagsPage.previewFirst")) as string
                }
                onClick={() => evaluate.mutate(true)}
              >
                {t("tagsPage.apply")}
              </ActionButton>
            )}
          </span>
        </div>

        {rules.length === 0 ? (
          <div className="empty">{t("tagsPage.noRules")}</div>
        ) : (
          <table>
            <thead>
              <tr>
                <th>{t("nav.tags")}</th>
                <th>{t("tagsPage.criteriaCol")}</th>
                <th>{t("tagsPage.priorityCol")}</th>
                <th>{t("common.stateCol")}</th>
                {canEdit && <th className="col-actions" />}
              </tr>
            </thead>
            <tbody>
              {rules.map((r) => (
                <tr key={r.id}>
                  <td>
                    <span className="badge">{r.tag}</span>
                  </td>
                  <td>
                    {summary(r.match)}
                    {(r.problems ?? []).map((p, i) => (
                      <div key={i} className="note" style={{ color: "var(--warn)" }}>
                        {p}
                      </div>
                    ))}
                  </td>
                  <td>{r.priority}</td>
                  <td>{r.enabled ? t("common.active") : <span className="note">{t("common.disabled")}</span>}</td>
                  {canEdit && (
                    <td className="col-actions">
                      <div className="icon-actions">
                        <IconAction
                          kind="edit"
                          label={t("common.edit") as string}
                          onClick={() => setEditing(r)}
                        />
                        <IconDeleteButton onConfirm={() => remove.mutate(r)} />
                      </div>
                    </td>
                  )}
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>

      {preview && (
        <div className="card">
          <div className="card-head">
            <div className="kicker">{t("tagsPage.previewHeading", { count: preview.length })}</div>
          </div>
          {preview.length === 0 ? (
            <div className="empty">{t("tagsPage.fleetAlreadyCompliant")}</div>
          ) : (
            <table>
              <thead>
                <tr>
                  <th>{t("common.device")}</th>
                  <th>{t("tagsPage.toAdd")}</th>
                  <th>{t("tagsPage.toRemove")}</th>
                </tr>
              </thead>
              <tbody>
                {preview.map((c) => (
                  <tr key={c.device_id}>
                    <td>{c.name}</td>
                    <td>{(c.add ?? []).map((tg) => tg.tag).join(", ") || "—"}</td>
                    <td>{(c.remove ?? []).map((tg) => tg.tag).join(", ") || "—"}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
      )}

      {(tags?.tags ?? []).length > 0 && (
        <div className="card">
          <div className="card-head">
            <div className="kicker">{t("tagsPage.usedOnThisSite")}</div>
          </div>
          <div className="row" style={{ gap: 6 }}>
            {(tags?.tags ?? []).map((tg) => (
              <span key={tg} className="badge">
                {tg}
              </span>
            ))}
          </div>
        </div>
      )}

      {editing && (
        <RuleModal
          rule={editing === "new" ? null : editing}
          criteria={CRITERIA}
          suggestions={tags?.tags ?? []}
          busy={save.isPending}
          error={save.error}
          onClose={() => setEditing(null)}
          onSave={(r) => save.mutate(r)}
        />
      )}
      {toast}
    </>
  );
}

function RuleModal({
  rule,
  criteria,
  suggestions,
  busy,
  error,
  onClose,
  onSave,
}: {
  rule: TagRule | null;
  criteria: { key: keyof TagMatch; label: string; hint: string }[];
  suggestions: string[];
  busy: boolean;
  error: unknown;
  onClose: () => void;
  onSave: (r: Partial<TagRule>) => void;
}) {
  const { t } = useTranslation();
  const [tag, setTag] = useState(rule?.tag ?? "");
  const [priority, setPriority] = useState(rule?.priority ?? 0);
  const [enabled, setEnabled] = useState(rule?.enabled ?? true);
  const [match, setMatch] = useState<TagMatch>(rule?.match ?? {});

  const empty = criteria.every((c) => (match[c.key] ?? "").trim() === "");

  return (
    <Modal
      title={(rule ? t("tagsPage.editRule") : t("tagsPage.newTaggingRule")) as string}
      onClose={onClose}
      help={<p>{t("tagsPage.criteriaHelp")}</p>}
    >
      <Field label={t("tagsPage.tagToApply") as string}>
        <input
          value={tag}
          list="tag-suggestions"
          placeholder={t("tagsPage.tagPlaceholder") as string}
          onChange={(e) => setTag(e.target.value)}
        />
      </Field>
      <datalist id="tag-suggestions">
        {suggestions.map((s) => (
          <option key={s} value={s} />
        ))}
      </datalist>

      <div className="form-grid cols-2">
        {criteria.map((c) => (
          <Field key={c.key} label={c.label}>
            <input
              value={match[c.key] ?? ""}
              placeholder={c.hint}
              onChange={(e) => setMatch({ ...match, [c.key]: e.target.value })}
            />
          </Field>
        ))}
      </div>

      <div className="form-grid cols-2">
        <Field label={t("tagsPage.priorityLabel") as string}>
          <input
            type="number"
            value={priority}
            onChange={(e) => setPriority(Number(e.target.value) || 0)}
          />
        </Field>
        <div style={{ paddingTop: 18 }}>
          <Switch
            checked={enabled}
            onChange={setEnabled}
            label={t("tagsPage.activeRule") as string}
            hint={t("tagsPage.activeRuleHint") as string}
          />
        </div>
      </div>

      {empty && (
        <p className="note" style={{ color: "var(--warn)" }}>
          {t("tagsPage.emptyRuleWarning")}
        </p>
      )}
      <ErrorMsg error={error} />
      <div className="row" style={{ justifyContent: "flex-end", marginTop: 12 }}>
        <ActionButton kind="cancel" className="ghost" onClick={onClose}>
          {t("common.cancel")}
        </ActionButton>
        <ActionButton
          kind="save"
          disabled={busy || empty || tag.trim() === ""}
          onClick={() => onSave({ id: rule?.id, tag, priority, enabled, match })}
        >
          {t("common.save")}
        </ActionButton>
      </div>
    </Modal>
  );
}
