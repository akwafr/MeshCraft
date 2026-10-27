import { useQuery, useQueryClient, useMutation } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { del, get, post, put } from "../api";
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
import { buildTone, pullRef, type AgentBuildState } from "./agentRepos";

/**
 * Agent image repositories — the operator surface of the git→OCI-layout
 * pipeline (internal/agenthost). Global-admin only, like the API beneath it:
 * registering a repository builds third-party code on the controller host and
 * publishes the result in the registry every proxy pulls from.
 *
 * Sibling to the Drivers page, and deliberately close to it — the difference
 * is the product: a driver is a container the controller RUNS, an agent is a
 * layout a remote proxy PULLS. So there is no container state and no
 * start/stop, only build, enable, and the pullable reference.
 */

type AgentRepo = {
  id: string;
  url: string;
  subdir?: string;
  ref?: string;
  agent_name: string;
  current_version?: string;
  enabled: boolean;
};

type AgentBuild = {
  id: string;
  state: AgentBuildState;
  git_commit?: string;
  version?: string;
  error?: string;
  log?: string;
  finished_at?: string;
};

type AgentsAnswer = {
  repos: AgentRepo[];
  builds: Record<string, AgentBuild>;
  enabled: boolean;
};

export default function AgentsPage() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const [toast, showToast] = useToast();
  const [adding, setAdding] = useState(false);
  const [logRepo, setLogRepo] = useState<string | null>(null);

  const q = useQuery({
    queryKey: ["agent-repos"],
    queryFn: () => get<AgentsAnswer>("/api/agent-repos"),
    // A build in flight changes state server-side without a user gesture:
    // poll gently while the page is open, this is an admin screen.
    refetchInterval: 5000,
  });

  const invalidate = () => qc.invalidateQueries({ queryKey: ["agent-repos"] });
  const act = useMutation({
    mutationFn: (a: { path: string; method?: "post" | "del" | "put"; body?: unknown }) =>
      a.method === "del"
        ? del<unknown>(a.path)
        : a.method === "put"
          ? put<unknown>(a.path, a.body)
          : post<unknown>(a.path),
    onSuccess: invalidate,
    onError: (err) =>
      showToast(err instanceof Error ? err.message : t("agentsPage.actionFailed")),
  });

  const data = q.data;

  return (
    <div className="page">
      <PageHeader title={t("agentsPage.title")} subtitle={t("agentsPage.subtitle")}>
        <ActionButton kind="add" onClick={() => setAdding(true)}>
          {t("agentsPage.addRepo")}
        </ActionButton>
      </PageHeader>
      {toast}
      {q.error && <ErrorMsg error={q.error} />}

      {data && !data.enabled && (
        <Banner
          tone="info"
          title={t("agentsPage.disabledTitle")}
          note={t("agentsPage.disabledNote")}
        />
      )}

      {data && data.repos.length === 0 && (
        <p className="note">{t("agentsPage.empty")}</p>
      )}

      {data?.repos.map((repo) => {
        const build = data.builds[repo.id];
        const ref = pullRef(window.location.host, repo.agent_name, repo.current_version ?? "");
        return (
          <div key={repo.id} className="card">
            <div className="card-head">
              <h3>
                {repo.agent_name}
                {!repo.enabled && (
                  <span className="sub">{t("agentsPage.disabledChip")}</span>
                )}
              </h3>
              <div className="row">
                <ActionButton
                  kind="refresh"
                  className="secondary"
                  disabled={act.isPending || !repo.enabled || !data.enabled}
                  title={t("agentsPage.rebuildTip") as string}
                  onClick={() => act.mutate({ path: `/api/agent-repos/${repo.id}/build` })}
                >
                  {t("agentsPage.rebuild")}
                </ActionButton>
                {repo.enabled ? (
                  <ActionButton
                    kind="pause"
                    className="ghost"
                    disabled={act.isPending}
                    title={t("agentsPage.disableTip") as string}
                    onClick={() =>
                      act.mutate({
                        path: `/api/agent-repos/${repo.id}/enabled`,
                        method: "put",
                        body: { enabled: false },
                      })
                    }
                  >
                    {t("agentsPage.disable")}
                  </ActionButton>
                ) : (
                  <ActionButton
                    kind="play"
                    className="ghost"
                    disabled={act.isPending || !data.enabled}
                    onClick={() =>
                      act.mutate({
                        path: `/api/agent-repos/${repo.id}/enabled`,
                        method: "put",
                        body: { enabled: true },
                      })
                    }
                  >
                    {t("agentsPage.enable")}
                  </ActionButton>
                )}
                <ActionButton
                  kind="terminal"
                  className="ghost"
                  disabled={!build}
                  onClick={() => setLogRepo(repo.id)}
                >
                  {t("agentsPage.buildLog")}
                </ActionButton>
                <DeleteButton
                  onConfirm={() =>
                    act.mutate({ path: `/api/agent-repos/${repo.id}`, method: "del" })
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
                {t("agentsPage.buildState")}:{" "}
                {build ? t(`agentsPage.build_${build.state}`) : t("agentsPage.buildNone")}
              </span>
              {repo.current_version && (
                <span className="badge ok">
                  {t("agentsPage.version")}: {repo.current_version}
                </span>
              )}
            </div>

            {ref ? (
              <div className="row agents-pullref">
                <code className="scroll-x">{ref}</code>
                <ActionButton
                  kind="copy"
                  className="secondary"
                  onClick={() => {
                    void navigator.clipboard.writeText(ref);
                    showToast(t("agentsPage.refCopied"));
                  }}
                >
                  {t("agentsPage.copyRef")}
                </ActionButton>
              </div>
            ) : (
              <p className="note">{t("agentsPage.notBuiltYet")}</p>
            )}

            {build?.state === "failed" && build.error && (
              <p className="error">{build.error}</p>
            )}
          </div>
        );
      })}

      {adding && (
        <AddRepoModal
          onClose={() => setAdding(false)}
          onAdded={() => {
            setAdding(false);
            invalidate();
            showToast(t("agentsPage.addedToast"));
          }}
        />
      )}
      {logRepo !== null && (
        <BuildLogModal repoID={logRepo} onClose={() => setLogRepo(null)} />
      )}
    </div>
  );
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
  const [agentName, setAgentName] = useState("");
  const add = useMutation({
    mutationFn: () =>
      post<AgentRepo>("/api/agent-repos", {
        url,
        subdir,
        ref,
        agent_name: agentName,
      }),
    onSuccess: onAdded,
  });

  return (
    <Modal
      title={t("agentsPage.addTitle")}
      onClose={onClose}
      busy={add.isPending}
      help={t("agentsPage.addHelp")}
    >
      <Form onSubmit={() => add.mutate()} busy={add.isPending}>
        <Field label={t("agentsPage.nameLabel")} help={t("agentsPage.nameHelp")}>
          <input
            autoFocus
            value={agentName}
            onChange={(e) => setAgentName(e.target.value)}
            placeholder="meshcraft/agent-foo"
          />
        </Field>
        <Field label={t("agentsPage.urlLabel")}>
          <input
            value={url}
            onChange={(e) => setUrl(e.target.value)}
            placeholder="https://github.com/…"
          />
        </Field>
        <Field label={t("agentsPage.subdirLabel")} help={t("agentsPage.subdirHelp")}>
          <input
            value={subdir}
            onChange={(e) => setSubdir(e.target.value)}
            placeholder="cmd/agent-foo"
          />
        </Field>
        <Field label={t("agentsPage.refLabel")} help={t("agentsPage.refHelp")}>
          <input value={ref} onChange={(e) => setRef(e.target.value)} placeholder="main" />
        </Field>
        {add.error && <ErrorMsg error={add.error} />}
        <div className="actions">
          <ActionButton
            kind="cancel"
            className="ghost"
            type="button"
            onClick={onClose}
            disabled={add.isPending}
          >
            {t("common.cancel")}
          </ActionButton>
          <ActionButton
            kind="add"
            type="submit"
            disabled={add.isPending || !url.trim() || !agentName.trim()}
          >
            {t("agentsPage.addAndBuild")}
          </ActionButton>
        </div>
      </Form>
    </Modal>
  );
}

function BuildLogModal({ repoID, onClose }: { repoID: string; onClose: () => void }) {
  const { t } = useTranslation();
  const q = useQuery({
    queryKey: ["agent-build-log", repoID],
    queryFn: () => get<{ builds: AgentBuild[] }>(`/api/agent-repos/${repoID}/log`),
    refetchInterval: 3000,
  });
  const latest = q.data?.builds?.[0];
  return (
    <Modal title={t("agentsPage.buildLog")} onClose={onClose} wide>
      {q.error && <ErrorMsg error={q.error} />}
      {latest && (
        <>
          <p className="note">
            {t(`agentsPage.build_${latest.state}`)}
            {latest.version ? ` · ${latest.version}` : ""}
            {latest.git_commit ? ` · ${latest.git_commit.slice(0, 12)}` : ""}
          </p>
          <pre className="scroll-x drivers-log">
            {latest.log || t("agentsPage.logEmpty")}
          </pre>
        </>
      )}
      {q.data && !latest && <p className="note">{t("agentsPage.logEmpty")}</p>}
    </Modal>
  );
}
