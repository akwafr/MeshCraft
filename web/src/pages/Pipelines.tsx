import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { useSite, useSiteId } from "../useSite";
import { get } from "../api";
import { ActionButton, ErrorMsg, Modal } from "../components";

/**
 * Pipelines: what meshcraft does on the fleet, and above all what's stuck.
 * See docs/01-Architecture/Unified Monitoring and pipelines §5.
 *
 * The screen answers ONE question — "what's blocked, and why?" — and it must
 * answer it without scrolling. That's why states calling for action rise to
 * the top regardless of age: a pipeline blocked for two days matters more
 * than a success from two minutes ago.
 */

interface Step {
  seq: number;
  name: string;
  state: "done" | "failed" | "skipped";
  detail?: string;
}

interface Pipeline {
  id: string;
  kind: string;
  device_name?: string;
  state: "pending" | "running" | "blocked" | "done" | "failed";
  label: string;
  blocked_reason?: string;
  created_at: string;
  steps?: Step[];
}

export default function PipelinesPage() {
  const { t, i18n } = useTranslation();
  const { site } = useSite();
  const siteId = useSiteId();
  const siteSlug = site?.slug ?? "";
  const [open, setOpen] = useState<string | null>(null);

  /** State labels. "Blocked" is not "failed", and the screen must say so. */
  const STATES: Record<string, { text: string; color: string }> = {
    pending: { text: t("pipelines.statePending"), color: "var(--text-dim)" },
    running: { text: t("pipelines.stateRunning"), color: "var(--accent)" },
    blocked: { text: t("pipelines.stateBlocked"), color: "var(--warn)" },
    done: { text: t("pipelines.stateDone"), color: "var(--ok)" },
    failed: { text: t("pipelines.stateFailed"), color: "var(--danger)" },
  };

  const KINDS: Record<string, string> = {
    config: t("nav.configuration"),
    credential: t("pipelines.kindCredential"),
    monitoring: t("pipelines.kindMonitoring"),
    import: t("pipelines.kindImport"),
  };

  const { data, error } = useQuery({
    queryKey: ["pipelines", siteId],
    queryFn: () => get<{ pipelines: Pipeline[] }>(`/api/sites/${siteId}/pipelines`),
    enabled: !!siteId,
    // An in-progress operation must be seen advancing without reloading the page.
    refetchInterval: 5000,
  });
  const { data: detail } = useQuery({
    queryKey: ["pipeline", open],
    queryFn: () => get<Pipeline>(`/api/pipelines/${open}`),
    enabled: !!open,
  });

  if (!siteId) return <div className="empty">{t("pipelines.chooseASite")}</div>;
  if (error) return <ErrorMsg error={error} />;
  const list = data?.pipelines ?? [];
  const stuck = list.filter((p) => p.state === "blocked" || p.state === "failed");

  return (
    <>
      <div className="toolbar">
        <h1>
          {t("nav.pipelines")}
          <span className="sub">{t("pipelines.subtitle")}</span>
        </h1>
      </div>

      {stuck.length > 0 && (
        <p className="note" style={{ marginTop: 0, color: "var(--warn)" }}>
          {t("pipelines.needsAttention", { count: stuck.length })}
        </p>
      )}

      <div className="card">
        {list.length === 0 ? (
          <div className="empty">{t("pipelines.noneRecorded")}</div>
        ) : (
          <table>
            <thead>
              <tr>
                <th>{t("common.stateCol")}</th>
                <th>{t("pipelines.operationCol")}</th>
                <th>{t("common.device")}</th>
                <th>{t("pipelines.whenCol")}</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {list.map((p) => {
                const e = STATES[p.state] ?? STATES.pending;
                return (
                  <tr key={p.id}>
                    <td style={{ whiteSpace: "nowrap" }}>
                      <span className="dot" style={{ background: e.color }} /> {e.text}
                    </td>
                    <td>
                      {p.label || KINDS[p.kind] || p.kind}
                      {p.blocked_reason && (
                        <div className="note" style={{ color: "var(--warn)" }}>
                          {p.blocked_reason}
                        </div>
                      )}
                    </td>
                    <td>{p.device_name || <span className="note">{t("pipelines.fleet")}</span>}</td>
                    <td className="note" style={{ whiteSpace: "nowrap" }}>
                      {new Date(p.created_at).toLocaleString(i18n.language)}
                    </td>
                    <td style={{ textAlign: "right" }}>
                      <ActionButton kind="play" className="ghost" onClick={() => setOpen(p.id)}>
                        {t("pipelines.transcript")}
                      </ActionButton>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        )}
      </div>

      {open && (
        <Modal title={detail?.label ?? t("pipelines.transcript")} onClose={() => setOpen(null)}>
          {!detail ? (
            <div className="empty">{t("common.loading")}</div>
          ) : (
            <>
              {detail.blocked_reason && (
                <p className="note" style={{ color: "var(--warn)" }}>
                  {detail.blocked_reason}
                </p>
              )}
              <table>
                <tbody>
                  {(detail.steps ?? []).map((s) => (
                    <tr key={s.seq}>
                      <td style={{ width: 28 }}>
                        <span
                          className="dot"
                          style={{
                            background:
                              s.state === "failed"
                                ? "var(--danger)"
                                : s.state === "skipped"
                                  ? "var(--warn)"
                                  : "var(--ok)",
                          }}
                        />
                      </td>
                      <td>
                        {s.name}
                        {s.detail && (
                          <div className="note" style={{ wordBreak: "break-word" }}>
                            {s.detail}
                          </div>
                        )}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
              {(detail.steps ?? []).length === 0 && (
                <div className="empty">{t("pipelines.noStepsRecorded")}</div>
              )}
            </>
          )}
        </Modal>
      )}
    </>
  );
}
