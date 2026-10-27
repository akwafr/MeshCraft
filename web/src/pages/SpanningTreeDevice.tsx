import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router-dom";
import { post, type Device } from "../api";
import { ActionButton, QuickTip } from "../components";
import { sitePath } from "../paths";
import {
  DEFAULT_BRIDGE_PRIORITY,
  SUPER_ROOT_PRIORITIES,
  superRootVerdict,
} from "./stpSuperRoot";
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
  /**
   * What the connector REFUSED. Shown with the same weight as `notes`, because
   * Super Root is the field most likely to land here: six of the seven
   * connectors cannot carry it at all, and on the seventh the extra EOS keyword
   * is unverified. A skipped line that only reaches a log turns "ticked and
   * nothing happened" into a silent success.
   */
  skipped?: string[];
  super_root?: boolean;
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
  const storedSuperRoot = dev.stp_super_root === true;
  const [superRoot, setSuperRoot] = useState(storedSuperRoot);
  // The priority to put back when the box is CLEARED. There is no recorded
  // previous value (cf. stpSuperRoot.ts), so this starts at the 802.1D default
  // and is a deliberate choice the operator makes, not a restore.
  const [restorePrio, setRestorePrio] = useState(DEFAULT_BRIDGE_PRIORITY);
  useEffect(() => {
    if (observedMode) setMode(observedMode);
    setSuperRoot(storedSuperRoot);
    setRestorePrio(DEFAULT_BRIDGE_PRIORITY);
  }, [observedMode, storedSuperRoot, dev.id]);

  // GATED ON `write` AND NOT ON `modes.length`: knowing which protocols a range
  // runs is not being able to configure them. arista-eos lists its modes and has
  // never had its STP write path reach a device, so the old test offered an
  // Apply button the endpoint answered with 422.
  const writable = canEdit && modes.length > 0 && dev.stp_caps?.write === true;
  const sr = superRootVerdict({
    mode,
    modes,
    canEdit,
    writable: dev.stp_caps?.write === true,
    implementsSuperRoot: dev.stp_caps?.super_root === true,
    stored: storedSuperRoot,
    checked: superRoot,
    observed: stp,
  });

  const apply = useMutation({
    mutationFn: (m: string) =>
      post<ApplyReply>(`/api/devices/${dev.id}/stp/apply`, {
        mode: m,
        super_root: superRoot,
        // Sent ONLY on the clear, and the controller requires it there: it is
        // what the switch gets instead of priority 0.
        ...(sr.needsPriority ? { bridge_priority: restorePrio } : {}),
      }),
    onSuccess: (r) => {
      qc.invalidateQueries({ queryKey: ["devices", dev.site_id] });
      const said = [...(r.notes ?? []), ...(r.skipped ?? [])];
      onToast(
        said.length
          ? said.join(" · ")
          : t("switchDetailPage.stpAppliedToast", {
              mode: MODE_LABEL[r.mode || mode] || r.mode || mode,
            }),
      );
    },
    onError: (e: Error) =>
      onToast(e.message || t("switchDetailPage.stpApplyFailed")),
  });

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

          {/* SUPER ROOT — MST only. The tip names the COST rather than the
              feature, because the cost is what nobody discovers until it bites:
              at priority 0 the tie-break is the MAC address, so a second super
              root in the same region is settled by hardware and not by the
              operator, and the root can no longer be moved by raising anyone
              else's priority. */}
          {sr.show && (
            <>
              <span className="k">{t("switchDetailPage.stpSuperRoot")}</span>
              <span>
                <QuickTip
                  text={t("switchDetailPage.stpSuperRootTitle")}
                  className="quicktip-inline"
                >
                  <label style={{ display: "inline-flex", gap: 6, alignItems: "center" }}>
                    <input
                      type="checkbox"
                      checked={superRoot}
                      disabled={sr.disabled || apply.isPending}
                      onChange={(e) => setSuperRoot(e.target.checked)}
                    />
                    <span style={{ fontSize: 12 }}>
                      {t("switchDetailPage.stpSuperRootLabel")}
                    </span>
                  </label>
                </QuickTip>
              </span>
              {sr.warnKey && (
                <span className="note" style={{ gridColumn: "1 / -1" }}>
                  {t("switchDetailPage." + sr.warnKey)}
                </span>
              )}
              {/* THE UNDO, and it is a choice rather than a restore: meshcraft
                  never recorded what this bridge carried before it wrote 0, so
                  the operator names the priority to put back. The controller
                  refuses the clear without one (400), and asking here saves the
                  round trip. */}
              {sr.needsPriority && (
                <span className="note" style={{ gridColumn: "1 / -1" }}>
                  {t("switchDetailPage.stpSuperRootClear")}{" "}
                  <select
                    value={restorePrio}
                    disabled={apply.isPending}
                    style={{ minHeight: 24, fontSize: 12 }}
                    onChange={(e) => setRestorePrio(Number(e.target.value))}
                  >
                    {SUPER_ROOT_PRIORITIES.map((p) => (
                      <option key={p} value={p}>
                        {p}
                        {p === DEFAULT_BRIDGE_PRIORITY ? " ★" : ""}
                      </option>
                    ))}
                  </select>
                </span>
              )}
            </>
          )}

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
          {/* The two sentences are NOT interchangeable, and the test used to be
              on `modes` alone: "read-only on this site" tells the operator to go
              find someone with rights, which is useless advice when the
              CONNECTOR is what cannot write. Platform first, permission second. */}
          {modes.length === 0 || dev.stp_caps?.write !== true
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
