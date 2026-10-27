import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { del, get, put } from "../../api";
import {
  ErrorMsg,
  HelpTip,
  TagInput,
  isRuleTag,
  isTechnicalTag,
  type DeviceTag,
} from "../../components";

/**
 * Normalize the same way the controller does (api.normalizeTag): lowercase,
 * spaces compacted. Duplicating the rule here isn't distrust of the
 * server — it's what lets us say "already exists" BEFORE the round trip,
 * and show the label in the toast exactly as it will actually be stored.
 */
/**
 * Equipment labels.
 * See docs/en/01-Architecture/Tags and credential scope.
 *
 * A label is not harmless: a site administration credential can target
 * it, and the equipment then falls under a different EXPECTED password.
 * Hence the help icon — adding a label writes nothing to the equipment
 * itself, and the operator must be able to verify that without leaving
 * the form.
 *
 * The panel lives next to the equipment identity, not the ports: a label
 * qualifies the machine, never a single port.
 */
export function DeviceTags({
  deviceId,
  siteId,
  canEdit,
  onToast,
  embedded,
}: {
  deviceId: string;
  siteId: string;
  canEdit: boolean;
  onToast: (msg: string) => void;
  /** Title lives on the side-panel group when true. */
  embedded?: boolean;
}) {
  const { t } = useTranslation();

  const qc = useQueryClient();

  const { data, error } = useQuery({
    queryKey: ["device-tags", deviceId],
    queryFn: () => get<{ tags: DeviceTag[] }>(`/api/devices/${deviceId}/tags`),
  });
  // Labels already in use on the site. Suggesting what already exists is
  // the only thing that keeps "regie" from coexisting with "régie": two
  // separate sets where the operator believed they were designating just
  // one, and a tag that ends up covering only half the intended fleet.
  const { data: siteTags } = useQuery({
    queryKey: ["site-tags", siteId],
    queryFn: () => get<{ tags: string[] }>(`/api/sites/${siteId}/tags`),
    enabled: canEdit,
  });

  // Placing the first occurrence of a label, or removing the last, changes
  // also the site list — which the Labels page and this entry reread.
  const invalidate = () => {
    qc.invalidateQueries({ queryKey: ["device-tags", deviceId] });
    qc.invalidateQueries({ queryKey: ["site-tags", siteId] });
  };

  const add = useMutation({
    // The tag travels in the PATH: it must be encoded — a raw "/" or
    // accent would break the route there.
    mutationFn: (tag: string) =>
      put<{
        tag: string;
        switch_profile_apply?: {
          ok?: boolean;
          profile_name?: string;
          applied?: number;
          issues?: string[];
          error?: string;
        };
      }>(`/api/devices/${deviceId}/tags/${encodeURIComponent(tag)}`),
    onSuccess: (res, tag) => {
      invalidate();
      qc.invalidateQueries({ queryKey: ["devices", siteId] });
      const apply = res?.switch_profile_apply;
      if (apply?.ok) {
        onToast(
          t("switchDetailPage.tagAppliedProfileToast", {
            tag: res?.tag ?? tag,
            profile: apply.profile_name ?? "",
            count: apply.applied ?? 0,
          }),
        );
      } else if (apply && apply.ok === false) {
        onToast(
          t("switchDetailPage.tagAppliedProfileBlockedToast", {
            tag: res?.tag ?? tag,
            profile: apply.profile_name ?? "",
            reason: apply.issues?.[0] ?? apply.error ?? "",
          }),
        );
      } else {
        onToast(t("switchDetailPage.tagAppliedToast", { tag: res?.tag ?? tag }));
      }
    },
  });
  const remove = useMutation({
    mutationFn: (t: DeviceTag) =>
      del(`/api/devices/${deviceId}/tags/${encodeURIComponent(t.tag)}`),
    onSuccess: (_res, removed) => {
      invalidate();
      // The toast repeats the consequence at the exact moment it happens:
      // for an inferred tag, the removal only lasts until the next
      // reassessment, and now is the moment to find out.
      onToast(
        isRuleTag(removed)
          ? t("switchDetailPage.tagRemovedRuleToast", { tag: removed.tag })
          : t("switchDetailPage.tagRemovedToast", { tag: removed.tag }),
      );
    },
  });

  const tags = data?.tags ?? [];
  const hasRule = tags.some(isRuleTag);
  // Two populations of labels coexist on the same equipment, and they are
  // not treated the same way (see the closing note of §3.1 of the labels
  // page): operational tags, arbitrary and removable, and TECHNICAL tags,
  // placed by meshcraft, which determine an identifier. The second family
  // has two mechanisms — the SNMPv3 identity and the password tied to the
  // platform's password policy — hence a help paragraph that says what to
  // look for, and where, shown only when the equipment carries one.
  const technical = tags.filter(isTechnicalTag);

  return (
    <div>
      {!embedded && (
        <div className="kicker">
          {t("switchDetailPage.tagsTitle")}
          <HelpTip label={t("switchDetailPage.tagsHelpLabel")}>
            <p>{t("switchDetailPage.tagsHelp1")}</p>
            {technical.length > 0 && (
              <p>{t("switchDetailPage.tagsHelpTechnical")}</p>
            )}
            {hasRule && (
              <p>{t("switchDetailPage.tagsHelpRule")}</p>
            )}
          </HelpTip>
        </div>
      )}
      <div style={{ marginTop: embedded ? 0 : 8 }}>
        <TagInput
          tags={tags}
          suggestions={siteTags?.tags ?? []}
          readOnly={!canEdit}
          busy={add.isPending || remove.isPending}
          onAdd={(t) => add.mutate(t)}
          onRemove={(t) => remove.mutate(t)}
        />
      </div>

      <ErrorMsg error={error ?? add.error ?? remove.error} />
    </div>
  );
}
