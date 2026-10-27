import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { viewerDate } from "../ui/clock";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { ApiError, del, get, post, type Device } from "../api";
import { ActionButton, ErrorMsg, HelpTip, Modal } from "../components";
import {
  bucketInk,
  bucketRows,
  deviceLabel,
  namesById,
  outcomeCounts,
  outcomeInk,
  runEndedBadly,
  runProgress,
  writeGate,
  type Bucket,
  type DeployResult,
  type DeploymentView,
  type DeployStatus,
} from "./credDeploy";

/**
 * FLEET CREDENTIAL DEPLOYMENT — the screen for the runner in
 * `internal/api/creddeploy.go`.
 *
 * # What this screen exists to end
 *
 * Rotating a site credential stored a new generation and pushed it to NOTHING:
 * the vault changed and the fleet did not, and the operator's only way to find
 * out which switches had actually taken the new secret was to log into them one
 * by one. That is what happened on lab/site-lab2 on 2026-08-07 and what was
 * repaired by hand, eight devices at a time, on 2026-08-08.
 *
 * # Two gestures, never one button
 *
 * `POST …/deploy` without `"write": true` is a REHEARSAL: it opens the real
 * session on every queued device, applies every gate up to and including the
 * account check, and stops before the switch changes. The write is a second,
 * explicit gesture, and this screen refuses to offer it until a rehearsal of the
 * same generation has finished cleanly ([writeGate]). The API does not impose
 * that — it makes the harmless run the DEFAULT, which is a different guarantee —
 * so the discipline lives here, stated on the screen rather than assumed.
 *
 * # Pushing is not promoting, and the screen says so in words
 *
 * The two gestures above are both PUSHES. Neither of them promotes anything:
 * this screen places a generation on devices and records what it read back, and
 * the previous generation stays `retiring` — still offered on every login by
 * `SecretsForCredential` — for as long as any device is recorded as carrying
 * it. That is the whole reason a half-deployed fleet stays reachable, and it is
 * also the thing an operator most easily assumes has happened: a green line
 * reading "everyone carries generation 4" beside a button labelled "write" is
 * read as "the rotation is finished" unless the screen states otherwise.
 * `safe_to_promote` is the verdict on that separate step, it comes from the
 * server with its reason, and it waits on EVERY device in scope — one device
 * never reached withholds it, which is exactly what the `unreached` bucket is
 * for.
 *
 * # Everything shown is OBSERVED
 *
 * The four buckets come from `store.PlanDeployment`, which classifies what
 * meshcraft has READ BACK from devices, never what it intended. "Partial" is the
 * normal state of a fleet mid-rotation, so all four are expected to be non-empty
 * at some point; the only forbidden move is promoting while `behind` or
 * `unreached` still holds anyone, and that verdict comes from the server
 * (`safe_to_promote`) rather than being recomputed here — two copies of that
 * rule would eventually let the screen say "ready" about a promotion the store
 * refuses.
 */

/** Poll cadence while a run is in flight. A run's steps are minutes apart. */
const RUN_POLL_MS = 3000;

export default function CredentialDeploy({
  siteId,
  credId,
  label,
  perDevice,
  canDeploy,
  onClose,
}: {
  siteId: string;
  credId: string;
  /** The credential as the list names it — used in the title, never as data. */
  label: string;
  perDevice: boolean;
  /** site_admin. An operator may WATCH a run; only site_admin may start one. */
  canDeploy: boolean;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const [openTranscript, setOpenTranscript] = useState<string | null>(null);

  const view = useQuery({
    queryKey: ["cred-deployment", siteId, credId],
    queryFn: () =>
      get<DeploymentView>(`/api/sites/${siteId}/credentials/${credId}/deployment`),
  });

  // 404 IS AN ANSWER HERE, not a failure: it is what the controller says when no
  // run has ever been started for this credential, and the registry is in memory
  // so a restart puts every credential back in that state. Letting react-query
  // treat it as an error would paint a red banner over the ordinary first visit.
  const status = useQuery({
    queryKey: ["cred-deploy-run", siteId, credId],
    queryFn: async () => {
      try {
        return await get<DeployStatus>(`/api/sites/${siteId}/credentials/${credId}/deploy`);
      } catch (e) {
        if (e instanceof ApiError && e.status === 404) return null;
        throw e;
      }
    },
    refetchInterval: (q) => (q.state.data?.running ? RUN_POLL_MS : false),
  });

  // THE SITE'S DEVICE LIST, loaded for one purpose: to give a NAME to the rows
  // the plan could not name. `PlanDeployment` synthesises `{device_id}` — no
  // name, no date, no error — for every in-scope device never observed under
  // this credential, and those are both the rows that block promotion and the
  // rows an operator has to go and act on. A line reading `4f1c-…` names nothing
  // in a rack. Failure here is not an error on this screen: [deviceLabel] falls
  // back to the id, which is what was shown before this query existed.
  const devices = useQuery({
    queryKey: ["devices", siteId],
    queryFn: () => get<Device[]>(`/api/sites/${siteId}/devices`),
    enabled: !!siteId,
  });
  const names = namesById(devices.data);

  const target = view.data?.plan?.target ?? 0;
  const last = status.data ?? undefined;
  const gate = writeGate({ target, last, perDevice });
  const running = !!last?.running;

  const start = useMutation({
    mutationFn: (write: boolean) =>
      post<DeployStatus>(`/api/sites/${siteId}/credentials/${credId}/deploy`, {
        generation: target,
        // Sent explicitly in BOTH directions. The server's default is the
        // rehearsal and a missing field must stay harmless, but a screen that
        // relied on the omission would be one refactor away from posting an
        // empty body next to a button labelled "write".
        write,
      }),
    onSuccess: (r) => {
      qc.setQueryData(["cred-deploy-run", siteId, credId], r);
      void status.refetch();
    },
  });

  const stop = useMutation({
    mutationFn: () => del(`/api/sites/${siteId}/credentials/${credId}/deploy`),
    onSuccess: () => void status.refetch(),
  });

  // The buckets are re-read when a run ends: every confirmed installation the
  // run recorded is a row that just moved from `behind` to `carrying`, and a
  // screen still showing the pre-run picture is the exact confusion this whole
  // feature exists to end.
  const runKey = `${last?.started_at ?? ""}:${last?.finished_at ?? ""}`;
  const [seenRun, setSeenRun] = useState("");
  if (last && !last.running && runKey !== seenRun) {
    setSeenRun(runKey);
    void view.refetch();
  }

  return (
    <Modal
      title={t("credDeployPage.title", { label })}
      onClose={onClose}
      wide
      help={
        <>
          <p>{t("credDeployPage.help1")}</p>
          <p>{t("credDeployPage.help2")}</p>
          <p>{t("credDeployPage.help3")}</p>
          <p>{t("credDeployPage.help4")}</p>
        </>
      }
    >
      {/* THE TWO-PHASE NATURE, said before anything else on the screen and not
          behind the title's help: it is what the two buttons at the bottom
          mean, and a paragraph read after the click explains nothing. */}
      <p className="note" style={{ marginTop: 0 }}>
        {t("credDeployPage.twoPhase")}
      </p>

      <ErrorMsg error={view.error} />
      {view.isLoading && <div className="empty">{t("common.loading")}</div>}

      {view.data && (
        <>
          <div className="row" style={{ gap: 12, alignItems: "baseline" }}>
            <b>
              {target > 0
                ? t("credDeployPage.targetGeneration", { n: target })
                : t("credDeployPage.noActiveGeneration")}
            </b>
            <span className="note">
              {t("credDeployPage.outstanding", { count: view.data.outstanding })}
            </span>
          </div>

          {/* The promotion verdict comes from the server, sentence included.
              `safe_to_promote` true is NOT an invitation to press anything —
              nothing on this screen promotes — so it is stated as a fact about
              the fleet rather than as a next step. */}
          <p
            className="note"
            style={{ color: view.data.safe_to_promote ? "var(--ok)" : "var(--warn)" }}
          >
            {view.data.safe_to_promote
              ? t("credDeployPage.everyoneCarriesIt", { n: target })
              : view.data.blocked_because || t("credDeployPage.notEverywhereYet")}
          </p>

          {/* THE OTHER HALF OF THE TWO-PHASE RULE, and it belongs HERE rather
              than in the help: a green "everyone carries generation 4" beside a
              button labelled "write" is read as "the rotation is finished"
              unless the screen says what this gesture did and did not do.
              Pushing places a secret; it never retires the previous
              generation, which stays valid on every login — and the verdict
              printed just above waits on EVERY device in scope, one unreached
              device being enough to withhold it. Said in both states, because
              the misreading is available in both. */}
          <p className="note">{t("credDeployPage.pushIsNotPromotion")}</p>

          {bucketRows(view.data.plan).map(({ bucket, rows }) => (
            <div key={bucket} style={{ marginTop: 10 }}>
              <div className="kicker" style={{ color: bucketInk(bucket) }}>
                {bucketTitle(bucket, t)} — {rows.length}
              </div>
              <div className="note">{bucketMeaning(bucket, t)}</div>
              {rows.length > 0 && (
                <ul className="deploy-list">
                  {rows.map((g) => (
                    <li key={g.device_id}>
                      <span className="mono">{deviceLabel(g, names)}</span>{" "}
                      {g.generation > 0 && (
                        <span className="note">
                          {t("credDeployPage.genLabel", { n: g.generation })}
                          {g.installed_username
                            ? ` · ${t("credDeployPage.asAccount", { account: g.installed_username })}`
                            : ""}
                        </span>
                      )}
                      {/* The recorded CAUSE of the last failure. Before
                          `NoteCredentialFailure` existed nothing created this
                          row, so the reason a device stayed behind was simply
                          lost between one screen and the next. */}
                      {g.last_error && (
                        <div className="note" style={{ color: "var(--warn)" }}>
                          {g.last_error}
                        </div>
                      )}
                    </li>
                  ))}
                </ul>
              )}
            </div>
          ))}
        </>
      )}

      {last && <RunReport st={last} names={names} onTranscript={setOpenTranscript} />}

      {!gate.armed && (
        <p className="note" style={{ marginTop: 10 }}>
          {t(`credDeployPage.gate.${gate.why}`)}
        </p>
      )}

      <ErrorMsg error={start.error} />
      <ErrorMsg error={stop.error} />
      <ErrorMsg error={status.error} />

      <div className="actions">
        <ActionButton kind="close" className="secondary" onClick={onClose}>
          {t("common.closeAction")}
        </ActionButton>
        {canDeploy && (
          <>
            <ActionButton
              kind="play"
              className="secondary"
              disabled={running || start.isPending || target <= 0 || perDevice}
              onClick={() => start.mutate(false)}
              title={t("credDeployPage.rehearseTip")}
            >
              {t("credDeployPage.rehearse")}
            </ActionButton>
            {/* DANGER, and dressed as one: this writes an administration
                password onto every queued switch in the site. */}
            <ActionButton
              kind="apply"
              className="danger"
              disabled={!gate.armed || start.isPending}
              onClick={() => start.mutate(true)}
              title={t("credDeployPage.writeTip")}
            >
              {t("credDeployPage.write", { n: target })}
            </ActionButton>
            {running && (
              <ActionButton kind="pause" className="ghost" disabled={stop.isPending}
                onClick={() => stop.mutate()}>
                {t("credDeployPage.stop")}
              </ActionButton>
            )}
          </>
        )}
      </div>
      {running && <p className="note">{t("credDeployPage.stopsBetweenDevices")}</p>}

      {openTranscript && last && (
        <TranscriptModal
          res={last.results.find((r) => r.device_id === openTranscript)}
          names={names}
          onClose={() => setOpenTranscript(null)}
        />
      )}
    </Modal>
  );
}

/** Bucket headings. Split out so the four strings sit beside each other. */
function bucketTitle(b: Bucket, t: (k: string) => string): string {
  if (b === "carrying") return t("credDeployPage.bucketCarrying");
  if (b === "behind") return t("credDeployPage.bucketBehind");
  if (b === "unreached") return t("credDeployPage.bucketUnreached");
  return t("credDeployPage.bucketAhead");
}

function bucketMeaning(b: Bucket, t: (k: string) => string): string {
  if (b === "carrying") return t("credDeployPage.bucketCarryingNote");
  if (b === "behind") return t("credDeployPage.bucketBehindNote");
  if (b === "unreached") return t("credDeployPage.bucketUnreachedNote");
  return t("credDeployPage.bucketAheadNote");
}

/**
 * The last run, device by device.
 *
 * PER-DEVICE AND NEVER SUMMARISED AWAY. The hand run of 2026-08-08 hit one
 * refusal, on `core01`, and what made it diagnosable in minutes rather than a
 * site visit was the per-step transcript. A report that said "7 ok, 1 failed"
 * would have lost exactly that, which is why the runner keeps `Steps` per device
 * and why this list offers every one of them.
 */
function RunReport({
  st,
  names,
  onTranscript,
}: {
  st: DeployStatus;
  /** id → name, for the rows the run reports by id alone. See [deviceLabel]. */
  names: Record<string, string>;
  onTranscript: (deviceId: string) => void;
}) {
  const { t } = useTranslation();
  const prog = runProgress(st);
  const counts = outcomeCounts(st);
  return (
    <div style={{ marginTop: 14, borderTop: "1px solid var(--border)", paddingTop: 10 }}>
      <div className="kicker">
        {st.write
          ? t("credDeployPage.lastRunWrite", { n: st.target })
          : t("credDeployPage.lastRunRehearsal", { n: st.target })}
      </div>
      <div className="note">
        {t("credDeployPage.runBy", {
          actor: st.actor,
          when: (viewerDate(st.started_at) ?? new Date(st.started_at)).toLocaleString(),
        })}
        {" · "}
        {st.running
          ? t("credDeployPage.runProgress", { done: prog.done, total: prog.total })
          : t("credDeployPage.runFinished", { done: prog.done, total: prog.total })}
      </div>

      {/* The controller's own sentence for an early end — cancelled, tripped or
          halted. It is empty when the run walked its whole queue, so a finished
          run that shows one is not a success with a footnote. */}
      {runEndedBadly(st) && (
        <p
          className="note"
          style={{ color: counts.unconfirmed > 0 ? "var(--danger)" : "var(--warn)" }}
        >
          {st.stopped}
        </p>
      )}

      {st.results.length > 0 && (
        <ul className="deploy-list">
          {st.results.map((r) => (
            <li key={r.device_id + r.at}>
              <span style={{ color: outcomeInk(r.outcome) }}>●</span>{" "}
              <span className="mono">{deviceLabel(r, names)}</span>{" "}
              <span className="badge" style={{ color: outcomeInk(r.outcome) }}>
                {outcomeLabel(r.outcome, t)}
              </span>
              {r.detail && <div className="note">{r.detail}</div>}
              {(r.steps?.length ?? 0) > 0 && (
                <ActionButton
                  kind="terminal"
                  className="ghost"
                  style={{ padding: "0 4px", fontSize: 11 }}
                  onClick={() => onTranscript(r.device_id)}
                >
                  {t("credDeployPage.transcript")}
                </ActionButton>
              )}
            </li>
          ))}
        </ul>
      )}

      {/* EVERY DECLINED DEVICE IS NAMED WITH ITS REASON. A fleet-wide gesture
          that silently omitted devices would put the operator back in front of
          the surprise this feature exists to end. */}
      {st.skipped.length > 0 && (
        <div style={{ marginTop: 8 }}>
          <div className="kicker" style={{ color: "var(--indeterminate)" }}>
            {t("credDeployPage.notQueued")} — {st.skipped.length}
            <HelpTip label={t("credDeployPage.notQueuedHelpLabel")}>
              <p>{t("credDeployPage.notQueuedHelp")}</p>
            </HelpTip>
          </div>
          <ul className="deploy-list">
            {st.skipped.map((s) => (
              <li key={s.device_id}>
                <span className="mono">{deviceLabel(s, names)}</span>
                <div className="note">{s.reason}</div>
              </li>
            ))}
          </ul>
        </div>
      )}
    </div>
  );
}

function outcomeLabel(outcome: string, t: (k: string) => string): string {
  switch (outcome) {
    case "installed":
      return t("credDeployPage.outcomeInstalled");
    case "rehearsed":
      return t("credDeployPage.outcomeRehearsed");
    case "skipped":
      return t("credDeployPage.outcomeSkipped");
    case "failed":
      return t("credDeployPage.outcomeFailed");
    case "unconfirmed":
      return t("credDeployPage.outcomeUnconfirmed");
    default:
      // An outcome this build does not know is shown AS THE SERVER SPELLED IT
      // rather than translated into the nearest known one.
      return outcome;
  }
}

/** One device's connector transcript, exactly as the run recorded it. */
function TranscriptModal({
  res,
  names,
  onClose,
}: {
  res?: DeployResult;
  names: Record<string, string>;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  if (!res) return null;
  return (
    <Modal
      title={t("credDeployPage.transcriptTitle", { device: deviceLabel(res, names) })}
      onClose={onClose}
    >
      <table>
        <tbody>
          {(res.steps ?? []).map((s, i) => (
            <tr key={i}>
              <td style={{ width: 28 }}>
                <span
                  className="dot"
                  style={{ background: s.ok ? "var(--ok)" : "var(--danger)" }}
                />
              </td>
              <td>
                {s.what}
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
      <div className="actions">
        <ActionButton kind="close" onClick={onClose}>
          {t("common.closeAction")}
        </ActionButton>
      </div>
    </Modal>
  );
}
