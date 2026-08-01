import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router-dom";
import { post, type Device } from "../api";
import { ActionButton, QuickTip } from "../components";
import { sitePath } from "../paths";
import { useSite } from "../useSite";

const MODE_LABEL: Record<string, string> = {
  mstp: "MSTP",
  "rstp-pvst": "RSTP-PVST",
  rstp: "RSTP",
  stp: "STP",
  none: "none",
};

type ApplyReply = {
  ok: boolean;
  mode?: string;
  notes?: string[];
  error?: string;
};

/** Spanning-tree observation + mode apply for ONE device (side panel). */
export function SpanningTreeCard({
  dev,
  canEdit,
  onToast,
}: {
  dev: Device;
  canEdit: boolean;
  onToast: (msg: string) => void;
}) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const { site } = useSite();
  const siteSlug = site?.slug ?? "";
  const stp = dev.stp;
  const modes = (dev.stp_caps?.modes ?? []).filter(Boolean);
  const observedMode = !stp
    ? ""
    : !stp.enabled
      ? "none"
      : (stp.mode || "");

  const [mode, setMode] = useState(observedMode || modes[0] || "rstp");
  useEffect(() => {
    if (observedMode) setMode(observedMode);
  }, [observedMode, dev.id]);

  const apply = useMutation({
    mutationFn: (m: string) =>
      post<ApplyReply>(`/api/devices/${dev.id}/stp/apply`, { mode: m }),
    onSuccess: (r) => {
      qc.invalidateQueries({ queryKey: ["devices", dev.site_id] });
      onToast(
        r.notes?.length
          ? r.notes.join(" · ")
          : t("switchDetailPage.stpAppliedToast", {
              mode: MODE_LABEL[r.mode || mode] || r.mode || mode,
            }),
      );
    },
    onError: (e: Error) =>
      onToast(e.message || t("switchDetailPage.stpApplyFailed")),
  });

  const writable = canEdit && modes.length > 0;

  return (
    <div className="kv" style={{ marginTop: 2 }}>
      <span className="k">{t("switchDetailPage.stpObserved")}</span>
      <span>
        {!stp ? (
          <span className="note">{t("planPage.stpUnknown")}</span>
        ) : !stp.enabled ? (
          <span className="note">{t("planPage.stpOff")}</span>
        ) : (
          <>
            <b>{(MODE_LABEL[stp.mode || ""] || stp.mode || "STP").toUpperCase()}</b>
            {stp.bridge_priority != null && stp.bridge_priority > 0 && (
              <span className="note">
                {" · "}
                {t("planPage.stpPrio", { value: stp.bridge_priority })}
              </span>
            )}
            {stp.is_root ? (
              <span className="note"> · {t("planPage.stpIsRoot")}</span>
            ) : stp.root_bridge_id ? (
              <span className="note" title={stp.root_bridge_id}>
                {" · "}
                {t("planPage.stpRootId", { id: shortBridge(stp.root_bridge_id) })}
              </span>
            ) : null}
          </>
        )}
      </span>

      {writable ? (
        <>
          <span className="k">{t("switchDetailPage.stpMode")}</span>
          <QuickTip text={t("switchDetailPage.stpModeTitle")} className="quicktip-inline">
            <select
              value={modes.includes(mode) ? mode : modes[0]}
              disabled={apply.isPending}
              style={{ minHeight: 28, fontSize: 12 }}
              onChange={(e) => setMode(e.target.value)}
            >
              {modes.map((m) => (
                <option key={m} value={m}>
                  {MODE_LABEL[m] || m}
                </option>
              ))}
            </select>
          </QuickTip>
          <div style={{ gridColumn: "1 / -1", marginTop: 8 }}>
            <ActionButton
              kind="apply"
              disabled={apply.isPending || !mode}
              onClick={() => apply.mutate(mode)}
              style={{ fontSize: 12 }}
              title={t("switchDetailPage.stpApplyTitle")}
            >
              {apply.isPending
                ? t("switchDetailPage.stpApplying")
                : t("switchDetailPage.stpApply")}
            </ActionButton>
          </div>
        </>
      ) : (
        <span className="note" style={{ gridColumn: "1 / -1" }}>
          {modes.length === 0
            ? t("switchDetailPage.stpNotWritable")
            : t("switchDetailPage.stpReadOnly")}
        </span>
      )}

      {siteSlug && (
        <span className="note" style={{ gridColumn: "1 / -1", marginTop: 6 }}>
          <Link to={sitePath(siteSlug, "config", "spanning-tree")}>
            {t("switchDetailPage.stpSitePrefLink")}
          </Link>
        </span>
      )}
    </div>
  );
}

/** Compact meta for the side-panel group summary. */
export function stpEffectiveMeta(
  dev: Device,
  t: (key: string, opts?: Record<string, unknown>) => string,
): string | undefined {
  const stp = dev.stp;
  if (!stp) return t("planPage.stpUnknown");
  if (!stp.enabled) return t("planPage.stpOff");
  return (MODE_LABEL[stp.mode || ""] || stp.mode || "STP").toUpperCase();
}

function shortBridge(id: string): string {
  const s = id.trim();
  if (s.length <= 18) return s;
  return s.slice(0, 8) + "…" + s.slice(-6);
}
