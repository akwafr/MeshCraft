import { useQuery, useQueryClient, useMutation } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { del, get, post } from "../api";
import {
  ActionButton,
  DeleteButton,
  ErrorMsg,
  Field,
  Modal,
  useToast,
} from "../components";
import { Banner } from "../ui/Banner";
import { Form } from "../ui/Form";
import { PageHeader } from "../ui/PageHeader";

/**
 * External driver repositories — the operator surface of the git→build→run
 * pipeline (internal/driverhost). Global-admin only, like the API beneath it:
 * registering a repository means BUILDING AND RUNNING third-party code on the
 * controller host.
 *
 * THE REFERENCE SCREEN for the UI contract: built on PageHeader, Form and the
 * upgraded Modal from day one, exactly as the new-screen skill prescribes —
 * when a rule and this file disagree, one of the two has a bug worth filing.
 */

type DriverRepo = {
  id: string;
  url: string;
  subdir?: string;
  ref?: string;
  driver_name?: string;
  enabled: boolean;
};

type DriverBuild = {
  id: string;
  state: "queued" | "running" | "ok" | "failed";
  git_commit?: string;
  error?: string;
  log?: string;
  finished_at?: string;
};

type DriverInstance = {
  repo_id: string;
  driver_name: string;
  protocol: number;
  addr?: string;
  state: "starting" | "ready" | "failed" | "stopped";
  error?: string;
  supports: { name: string; gestures: string[] }[];
};

type DriversAnswer = {
  repos: DriverRepo[];
  builds: Record<string, DriverBuild>;
  instances: DriverInstance[];
  enabled: boolean;
};

export default function DriversPage() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const [toast, showToast] = useToast();
  const [adding, setAdding] = useState(false);
  const [logRepo, setLogRepo] = useState<string | null>(null);

  const q = useQuery({
    queryKey: ["driver-repos"],
    queryFn: () => get<DriversAnswer>("/api/driver-repos"),
    // A build in flight changes state server-side without a user gesture:
    // poll gently while the page is open, this is an admin screen.
    refetchInterval: 5000,
  });

  const invalidate = () => qc.invalidateQueries({ queryKey: ["driver-repos"] });
  const act = useMutation({
    mutationFn: (a: { path: string; method?: "post" | "del" }) =>
      a.method === "del" ? del<unknown>(a.path) : post<unknown>(a.path),
    onSuccess: invalidate,
    onError: (err) =>
      showToast(err instanceof Error ? err.message : t("driversPage.actionFailed")),
  });

  const data = q.data;
  const instByRepo = new Map(
    (data?.instances ?? []).map((i) => [i.repo_id, i] as const),
  );

  return (
    <div className="page">
      <PageHeader title={t("driversPage.title")} subtitle={t("driversPage.subtitle")}>
        <ActionButton kind="add" onClick={() => setAdding(true)}>
          {t("driversPage.addRepo")}
        </ActionButton>
      </PageHeader>
      {toast}
      {q.error && <ErrorMsg error={q.error} />}

      {data && !data.enabled && (
        <Banner
          tone="info"
          title={t("driversPage.disabledTitle")}
          note={t("driversPage.disabledNote")}
        />
      )}

      {data && data.repos.length === 0 && (
        <p className="note">{t("driversPage.empty")}</p>
      )}

      {data?.repos.map((repo) => {
        const build = data.builds[repo.id];
        const inst = instByRepo.get(repo.id);
        return (
          <div key={repo.id} className="card">
            <div className="card-head">
              <h3>
                {repo.driver_name || repo.url}
                {!repo.enabled && (
                  <span className="sub">{t("driversPage.disabledChip")}</span>
                )}
              </h3>
              <div className="row">
                <ActionButton
                  kind="refresh"
                  className="secondary"
                  disabled={act.isPending || !repo.enabled}
                  title={t("driversPage.rebuildTip") as string}
                  onClick={() => act.mutate({ path: `/api/driver-repos/${repo.id}/build` })}
                >
                  {t("driversPage.rebuild")}
                </ActionButton>
                {repo.enabled ? (
                  <ActionButton
                    kind="pause"
                    className="ghost"
                    disabled={act.isPending}
                    onClick={() => act.mutate({ path: `/api/driver-repos/${repo.id}/stop` })}
                  >
                    {t("driversPage.stop")}
                  </ActionButton>
                ) : (
                  <ActionButton
                    kind="play"
                    className="ghost"
                    disabled={act.isPending}
                    onClick={() => act.mutate({ path: `/api/driver-repos/${repo.id}/start` })}
                  >
                    {t("driversPage.start")}
                  </ActionButton>
                )}
                <ActionButton
                  kind="terminal"
                  className="ghost"
                  disabled={!build}
                  onClick={() => setLogRepo(repo.id)}
                >
                  {t("driversPage.buildLog")}
                </ActionButton>
                <DeleteButton
                  onConfirm={() =>
                    act.mutate({ path: `/api/driver-repos/${repo.id}`, method: "del" })
                  }
                />
              </div>
            </div>

            <p className="note">
              {repo.url}
              {repo.subdir ? ` · ${repo.subdir}` : ""}
              {repo.ref ? ` @ ${repo.ref}` : ""}
            </p>

            <div className="drivers-badges">
              <span className={"badge " + buildTone(build?.state)}>
                {t("driversPage.buildState")}: {build ? t(`driversPage.build_${build.state}`) : t("driversPage.buildNone")}
              </span>
              <span className={"badge " + instTone(inst?.state)}>
                {t("driversPage.instState")}: {inst ? t(`driversPage.inst_${inst.state}`) : "—"}
              </span>
              {inst?.state === "ready" && (
                <span className="badge">MDP v{inst.protocol}</span>
              )}
            </div>

            {build?.state === "failed" && build.error && (
              <p className="error">{build.error}</p>
            )}
            {inst?.state === "failed" && inst.error && (
              <p className="error">{inst.error}</p>
            )}

            {inst?.supports?.map((c) => (
              <p key={c.name} className="note">
                <strong>{c.name}</strong> — {c.gestures.length}{" "}
                {t("driversPage.gestures")}: {c.gestures.join(", ")}
              </p>
            ))}
          </div>
        );
      })}

      {adding && (
        <AddRepoModal
          onClose={() => setAdding(false)}
          onAdded={() => {
            setAdding(false);
            invalidate();
            showToast(t("driversPage.addedToast"));
          }}
        />
      )}
      {logRepo !== null && (
        <BuildLogModal repoID={logRepo} onClose={() => setLogRepo(null)} />
      )}
    </div>
  );
}

function buildTone(state?: DriverBuild["state"]): string {
  if (state === "failed") return "danger";
  if (state === "ok") return "ok";
  return "";
}

function instTone(state?: DriverInstance["state"]): string {
  if (state === "failed") return "danger";
  if (state === "ready") return "ok";
  return "";
}

function AddRepoModal({
  onClose,
  onAdded,
}: {
  onClose: () => void;
  onAdded: () => void;
}) {
  const { t } = useTranslation();
  const [url, setUrl] = useState("");
  const [subdir, setSubdir] = useState("");
  const [ref, setRef] = useState("");
  const add = useMutation({
    mutationFn: () => post<DriverRepo>("/api/driver-repos", { url, subdir, ref }),
    onSuccess: onAdded,
  });

  return (
    <Modal title={t("driversPage.addTitle")} onClose={onClose} busy={add.isPending}
      help={t("driversPage.addHelp")}>
      <Form onSubmit={() => add.mutate()} busy={add.isPending}>
        <Field label={t("driversPage.urlLabel")}>
          <input
            autoFocus
            value={url}
            onChange={(e) => setUrl(e.target.value)}
            placeholder="https://github.com/…"
          />
        </Field>
        <Field label={t("driversPage.subdirLabel")} help={t("driversPage.subdirHelp")}>
          <input value={subdir} onChange={(e) => setSubdir(e.target.value)}
            placeholder="drivers/my-driver" />
        </Field>
        <Field label={t("driversPage.refLabel")} help={t("driversPage.refHelp")}>
          <input value={ref} onChange={(e) => setRef(e.target.value)} placeholder="main" />
        </Field>
        {add.error && <ErrorMsg error={add.error} />}
        <div className="actions">
          <ActionButton kind="cancel" className="ghost" type="button"
            onClick={onClose} disabled={add.isPending}>
            {t("common.cancel")}
          </ActionButton>
          <ActionButton kind="add" type="submit" disabled={add.isPending || !url.trim()}>
            {t("driversPage.addAndBuild")}
          </ActionButton>
        </div>
      </Form>
    </Modal>
  );
}

function BuildLogModal({ repoID, onClose }: { repoID: string; onClose: () => void }) {
  const { t } = useTranslation();
  const q = useQuery({
    queryKey: ["driver-build-log", repoID],
    queryFn: () => get<DriverBuild>(`/api/driver-repos/${repoID}/log`),
    refetchInterval: 3000,
  });
  return (
    <Modal title={t("driversPage.buildLog")} onClose={onClose} wide>
      {q.error && <ErrorMsg error={q.error} />}
      {q.data && (
        <>
          <p className="note">
            {t(`driversPage.build_${q.data.state}`)}
            {q.data.git_commit ? ` · ${q.data.git_commit.slice(0, 12)}` : ""}
          </p>
          <pre className="scroll-x drivers-log">
            {q.data.log || t("driversPage.logEmpty")}
          </pre>
        </>
      )}
    </Modal>
  );
}
