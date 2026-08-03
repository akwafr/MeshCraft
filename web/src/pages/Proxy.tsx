import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import {
  del,
  get,
  patch,
  post,
  put,
  type Artifact,
  type ComposeResult,
  type Event,
  type InstallResult,
  type ProxyInfo,
  type Site,
} from "../api";
import { useAdminSiteIDs, useAuth } from "../auth";
import { ActionButton, ActionIcon, DeleteButton, ErrorMsg, Field, HelpTip, Modal, QuickTip, useToast } from "../components";
import { ProxyPrefixesModal } from "./ProxyPrefixes";

function human(size: number): string {
  if (size > 1 << 20) return (size / (1 << 20)).toFixed(1) + " Mio";
  if (size > 1 << 10) return (size / (1 << 10)).toFixed(0) + " Kio";
  return size + " o";
}

function lastSeenLabel(p: ProxyInfo, t: (k: string, o?: Record<string, unknown>) => string): { text: string; color: string } {
  if (p.migrated_at)
    return { text: t("proxyPage.migratedElsewhere"), color: "var(--text-dim)" };
  if (p.revoked) return { text: t("proxyPage.revoked"), color: "var(--danger)" };
  // Local (internal) proxy is the controller itself — never heartbeats over WSS.
  if (p.internal) return { text: t("proxyPage.thisInstance"), color: "var(--ok)" };
  if (!p.last_seen) return { text: t("proxyPage.neverSeen"), color: "var(--warn)" };
  const age = Date.now() - new Date(p.last_seen).getTime();
  if (age < 90_000) return { text: t("proxyPage.online"), color: "var(--ok)" };
  const mins = Math.round(age / 60000);
  return {
    text: mins < 60 ? t("proxyPage.silentSince", { mins }) : t("proxyPage.offlineSince", { hours: Math.round(mins / 60) }),
    color: "var(--warn)",
  };
}

function download(name: string, text: string) {
  const url = URL.createObjectURL(new Blob([text], { type: "text/plain" }));
  const a = document.createElement("a");
  a.href = url;
  a.download = name;
  a.click();
  URL.revokeObjectURL(url);
}

/** Generated code block: display, copy, and download when it is a file. */
function CodeBlock({
  label,
  text,
  filename,
}: {
  label?: string;
  text: string;
  filename?: string;
}) {
  const { t } = useTranslation();
  const [copied, setCopied] = useState(false);
  const copy = async () => {
    await navigator.clipboard.writeText(text);
    setCopied(true);
    setTimeout(() => setCopied(false), 1500);
  };
  return (
    <>
      {label && <div className="kicker" style={{ marginTop: 12 }}>{label}</div>}
      <pre className="code">{text}</pre>
      <div className="row">
        <ActionButton kind="copy" className="secondary" onClick={copy}>
          {copied ? t("proxyPage.copied") : t("proxyPage.copyBtn")}
        </ActionButton>
        {filename && (
          <ActionButton kind="download" className="ghost" onClick={() => download(filename, text)}>
            {t("proxyPage.downloadFile", { filename })}
          </ActionButton>
        )}
      </div>
    </>
  );
}

/**
 * Choice of proxy coverage to deploy. An instance administrator can turn
 * it into an instance proxy; a site-admin only sees their own sites here,
 * and must check at least one — an empty coverage would cover nothing
 * (see docs/01-Architecture/Proxy).
 */
function CoveragePicker({
  admin,
  sites,
  global,
  onGlobal,
  selected,
  onSelected,
}: {
  admin: boolean;
  sites: Site[];
  global: boolean;
  onGlobal: (v: boolean) => void;
  selected: string[];
  onSelected: (v: string[]) => void;
}) {
  const { t } = useTranslation();
  const toggle = (id: string) =>
    onSelected(selected.includes(id) ? selected.filter((x) => x !== id) : [...selected, id]);
  return (
    <div style={{ marginBottom: 12 }}>
      <Field label={t("proxyPage.proxyCoverage")}>
        <>
          {admin && (
            <label className="row" style={{ gap: 8, marginBottom: 6 }}>
              <input
                type="checkbox"
                checked={global}
                onChange={(e) => onGlobal(e.target.checked)}
              />
              <span>{t("proxyPage.instanceProxy")}</span>
            </label>
          )}
          {!global && (
            <div className="vlan-pick" style={{ maxHeight: 180 }}>
              {sites.length === 0 ? (
                <div className="empty">{t("proxyPage.noAdminSites")}</div>
              ) : (
                sites.map((s) => (
                  <button
                    key={s.id}
                    type="button"
                    className={"vlan-row" + (selected.includes(s.id) ? " sel" : "")}
                    onClick={() => toggle(s.id)}
                  >
                    <span
                      className="vlan-chip"
                      style={{
                        margin: 0,
                        background: selected.includes(s.id) ? "var(--accent)" : "transparent",
                        border: "1px solid var(--border)",
                      }}
                    />
                    <span style={{ fontWeight: 600 }}>{s.name}</span>
                  </button>
                ))
              )}
            </div>
          )}
          {!global && selected.length === 0 && (
            <div className="note" style={{ color: "var(--warn)" }}>
              {t("proxyPage.pickOneSite")}
            </div>
          )}
        </>
      </Field>
    </div>
  );
}

/**
 * Proxies page — site coverage is the proxy rights boundary
 * (see docs/01-Architecture/Proxy and docs/01-Architecture/Docker and IPv6 deployment).
 */
export default function ProxyPage() {
  const { t, i18n } = useTranslation();
  const { user } = useAuth();
  const qc = useQueryClient();
  const admin = !!user?.is_global_admin;
  const adminSites = useAdminSiteIDs();
  // Deploy requires being an administrator of at least one site; the cover
  // of the created proxy will be limited to these sites (see docs/01-Architecture/Proxy).
  const canDeploy = admin || adminSites.length > 0;
  const [migrating, setMigrating] = useState<ProxyInfo | null>(null);
  const [covering, setCovering] = useState<ProxyInfo | null>(null);
  const [prefixing, setPrefixing] = useState<ProxyInfo | null>(null);
  const [renaming, setRenaming] = useState<ProxyInfo | null>(null);
  const [upgrading, setUpgrading] = useState<ProxyInfo | null>(null);
  const [toast, showToast] = useToast();

  /** Acting on the proxy itself requires rights to ALL of its covered sites. */
  const canAdminWhole = (p: ProxyInfo) =>
    admin ||
    (!p.global && !p.internal && p.sites.length > 0 &&
      p.sites.every((s) => adminSites.includes(s.id)));

  const { data: sites } = useQuery({
    queryKey: ["sites"],
    queryFn: () => get<Site[]>("/api/sites"),
  });
  const { data: proxies, error: proxiesError } = useQuery({
    queryKey: ["proxies"],
    queryFn: () => get<ProxyInfo[]>("/api/proxies"),
    refetchInterval: 20000,
  });

  const { data: artifacts, error } = useQuery({
    queryKey: ["artifacts"],
    queryFn: () => get<Artifact[]>("/api/proxy/artifacts"),
  });
  // The instance's build = the version the proxies must reach.
  const { data: instance } = useQuery({
    queryKey: ["version"],
    queryFn: () => get<{ version: string; build: string }>("/api/version"),
    staleTime: Infinity,
  });

  // Proxy events are instance events: without a site, they
  // do not appear in any site logs — hence this thread here.
  const { data: events } = useQuery({
    queryKey: ["instance-events", "proxy"],
    queryFn: () => get<Event[]>("/api/events?type=proxy.&limit=8"),
    enabled: admin,
    refetchInterval: 30000,
  });

  // Coverage requested for the next deployed proxy. A global-admin can
  // create an instance proxy; a site-admin chooses among his sites.
  const [newGlobal, setNewGlobal] = useState(admin);
  const [newSites, setNewSites] = useState<string[]>([]);
  const coverageBody = () =>
    newGlobal ? { global: true } : { global: false, site_ids: newSites };
  const deployableSites = (sites ?? []).filter((s) => admin || adminSites.includes(s.id));
  const coverageReady = newGlobal || newSites.length > 0;

  const compose = useMutation({
    mutationFn: () => post<ComposeResult>("/api/proxies/compose", coverageBody()),
  });
  const install = useMutation({
    mutationFn: () => post<InstallResult>("/api/proxies/install-script", coverageBody()),
  });
  const token = useMutation({
    mutationFn: () => post<{ token: string }>("/api/proxies/enroll-tokens", coverageBody()),
  });

  const invalidateProxies = () => qc.invalidateQueries({ queryKey: ["proxies"] });
  const revoke = useMutation({
    mutationFn: (p: ProxyInfo) => post(`/api/proxies/${p.id}/revoke`),
    onSuccess: invalidateProxies,
  });
  const cancelMigration = useMutation({
    mutationFn: (p: ProxyInfo) => post(`/api/proxies/${p.id}/cancel-migration`),
    onSuccess: () => {
      invalidateProxies();
      showToast(t("proxyPage.migrationCancelled"));
    },
  });
  const remove = useMutation({
    mutationFn: (p: ProxyInfo) => del(`/api/proxies/${p.id}`),
    onSuccess: invalidateProxies,
  });

  return (
    <>
      <div className="toolbar">
        <h1>
          {t("proxyPage.title")}
          <span className="sub">{t("proxyPage.subtitle")}</span>
        </h1>
      </div>

      <div className="card">
        <div className="card-head">
          <div className="kicker">
            {t("proxyPage.enrolledProxies")}
            <HelpTip label={t("proxyPage.coverageHelpLabel")}>
              <p>{t("proxyPage.coverageHelp1")}</p>
              <p>{t("proxyPage.coverageHelp2")}</p>
              {admin && <p>{t("proxyPage.coverageHelp3")}</p>}
            </HelpTip>
          </div>
        </div>
        <ErrorMsg error={proxiesError} />
        {(proxies ?? []).length === 0 ? (
          <div className="empty">
            {t("proxyPage.noProxies")}
          </div>
        ) : (
          <div style={{ overflowX: "auto" }}>
            <table>
              <thead>
                <tr>
                  <th>{t("common.proxy")}</th>
                  <th>{t("proxyPage.coverageCol")}</th>
                  <th>{t("common.stateCol")}</th>
                  <th>{t("proxyPage.versionCol")}</th>
                  <th style={{ width: 300 }}></th>
                </tr>
              </thead>
              <tbody>
                {(proxies ?? []).map((p) => {
                  const st = lastSeenLabel(p, t);
                  return (
                    <tr key={p.id}>
                      <td>
                        <div style={{ fontWeight: 600 }}>
                          {canAdminWhole(p) ? (
                            <QuickTip text={t("proxyPage.renameProxyTitle")} className="quicktip-inline">
                              <button
                                className="rename"
                                onClick={() => setRenaming(p)}
                              >
                                {p.name}
                                <span aria-hidden="true">✎</span>
                              </button>
                            </QuickTip>
                          ) : (
                            p.name
                          )}
                          {p.internal && (
                            <span className="badge" style={{ marginLeft: 8 }}>
                              instance
                            </span>
                          )}
                        </div>
                        <div className="note mono">{p.id.slice(0, 8)}…</div>
                      </td>
                      <td>
                        {p.global ? (
                          <span className="badge">{t("proxyPage.allSites")}</span>
                        ) : p.sites.length === 0 ? (
                          <span className="note" style={{ color: "var(--warn)" }}>
                            {t("proxyPage.noSitesUnusable")}
                          </span>
                        ) : (
                          <div className="chips">
                            {p.sites.map((s) => (
                              <QuickTip
                                key={s.id}
                                text={
                                  adminSites.includes(s.id)
                                    ? t("proxyPage.siteYouAdminTitle")
                                    : t("proxyPage.siteNotAdminTitle")
                                }
                                className="quicktip-inline"
                              >
                                <span
                                  className={"chip" + (adminSites.includes(s.id) ? " home" : "")}
                                >
                                  {s.name}
                                </span>
                              </QuickTip>
                            ))}
                          </div>
                        )}
                      </td>
                      <td>
                        <span className="row" style={{ gap: 6 }}>
                          <span className="dot" style={{ background: st.color }} />
                          <span style={{ fontSize: 11, fontWeight: 700 }}>{st.text}</span>
                        </span>
                        {p.migrate_url && !p.migrated_at && (
                          <div className="note" style={{ color: "var(--warn)" }}>
                            {t("proxyPage.migrationScheduled", { url: p.migrate_url })}
                          </div>
                        )}
                      </td>
                      <td>
                        <UpgradeCell
                          p={p}
                          target={instance?.build ?? ""}
                          editable={canAdminWhole(p)}
                          onEdit={() => setUpgrading(p)}
                        />
                      </td>
                      <td style={{ textAlign: "right", whiteSpace: "nowrap" }}>
                        {p.internal ? (
                          <span className="note">{t("proxyPage.followsController")}</span>
                        ) : (
                          <>
                            {(admin || p.sites.some((s) => adminSites.includes(s.id))) &&
                              !p.migrated_at && (
                                <ActionButton kind="sites" className="ghost" onClick={() => setCovering(p)}>
                                  {t("proxyPage.sitesButton")}
                                </ActionButton>
                              )}
                            {/* A range belongs to the PROXY, not a site: this is where
                                you ask what it scans. The « Add » flow screen was
                                impossible to find for someone looking at a proxy. */}
                            {(admin || p.sites.some((s) => adminSites.includes(s.id))) && (
                              <ActionButton kind="scan" className="ghost" onClick={() => setPrefixing(p)}>
                                {t("proxyPage.prefixesButton")}
                              </ActionButton>
                            )}
                            {canAdminWhole(p) && (
                              <>
                                {p.migrate_url && !p.migrated_at ? (
                                  <ActionButton
                                    kind="cancel"
                                    className="ghost"
                                    onClick={() => cancelMigration.mutate(p)}
                                  >
                                    {t("proxyPage.cancelMigration")}
                                  </ActionButton>
                                ) : (
                                  !p.migrated_at && (
                                    <ActionButton kind="migrate" className="ghost" onClick={() => setMigrating(p)}>
                                      {t("proxyPage.migrate")}
                                    </ActionButton>
                                  )
                                )}
                                {!p.migrated_at && (
                                  <ActionButton
                                    kind={p.revoked ? "check" : "ban"}
                                    className="ghost"
                                    onClick={() => revoke.mutate(p)}
                                  >
                                    {p.revoked ? t("proxyPage.reactivate") : t("proxyPage.revoke")}
                                  </ActionButton>
                                )}
                                <DeleteButton onConfirm={() => remove.mutate(p)} />
                              </>
                            )}
                          </>
                        )}
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
      </div>

      {canDeploy && (
        <div className="card">
          <div className="card-head">
            <div className="kicker">
              {t("proxyPage.deployDocker")}
              <HelpTip label={t("proxyPage.deployDockerHelpLabel")}>
                <p>{t("proxyPage.deployDockerHelp1")}</p>
              </HelpTip>
            </div>
          </div>
          <CoveragePicker
            admin={admin}
            sites={deployableSites}
            global={newGlobal}
            onGlobal={setNewGlobal}
            selected={newSites}
            onSelected={setNewSites}
          />
          <div className="row" style={{ marginBottom: 12 }}>
            <ActionButton
              kind="create"
              disabled={compose.isPending || !coverageReady}
              onClick={() => compose.mutate()}
            >
              {t("proxyPage.genCompose")}
            </ActionButton>
            <ActionButton
              kind="create"
              className="secondary"
              disabled={token.isPending || !coverageReady}
              onClick={() => token.mutate()}
            >
              {t("proxyPage.genTokenOnly")}
            </ActionButton>
          </div>
          <ErrorMsg error={compose.error} />
          <ErrorMsg error={token.error} />
          {token.data && (
            <div className="token-box">
              <div className="kicker">{t("proxyPage.enrollToken")}</div>
              <div className="mono" style={{ wordBreak: "break-all", marginTop: 4 }}>
                {token.data.token}
              </div>
              <div className="note" style={{ marginTop: 4 }}>
                {t("proxyPage.tokenHint")}
              </div>
            </div>
          )}
          {compose.data && (
            <>
              {compose.data.insecure && <p className="error">⚠ {compose.data.insecure}</p>}
              <CodeBlock text={compose.data.compose} filename="docker-compose.yml" />
              <p className="note">
                {t("proxyPage.thenComposeUp")}
              </p>
            </>
          )}
        </div>
      )}

      <div className="card">
        <div className="card-head">
          <div className="kicker">
            {t("proxyPage.binarySection")}
            <HelpTip label={t("proxyPage.binaryInstallHelpLabel")}>
              {canDeploy && (
                <>
                  <p>{t("proxyPage.binaryInstallHelp1")}</p>
                  <p>{t("proxyPage.binaryInstallHelp2")}</p>
                </>
              )}
              <p>{t("proxyPage.binaryInstallHelp3")}</p>
            </HelpTip>
          </div>
        </div>
        {canDeploy && (
          <>
            <div className="row" style={{ marginBottom: 12 }}>
              <ActionButton
                kind="create"
                disabled={install.isPending || !coverageReady}
                onClick={() => install.mutate()}
              >
                {t("proxyPage.genInstallScript")}
              </ActionButton>
            </div>
            <ErrorMsg error={install.error} />
            {install.data && (
              <>
                <CodeBlock
                  label={t("proxyPage.installScript")}
                  text={install.data.script}
                  filename="meshcraft-proxy-install.sh"
                />
                <p className="note">
                  {t("proxyPage.thenSudoInstall")}
                </p>
                <CodeBlock
                  label={t("proxyPage.systemdUnit")}
                  text={install.data.unit}
                  filename="meshcraft-proxy.service"
                />
              </>
            )}
          </>
        )}
        <ErrorMsg error={error} />
        {(artifacts ?? []).length === 0 ? (
          <div className="empty">{t("proxyPage.noBinaries")}</div>
        ) : (
          <table>
            <thead>
              <tr>
                <th>{t("proxyPage.platform")}</th>
                <th>{t("proxyPage.sizeCol")}</th>
                <th>{t("proxyPage.shaCol")}</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {(artifacts ?? []).map((a) => (
                <tr key={a.name}>
                  <td>
                    {a.os} / <b>{a.arch}</b>
                  </td>
                  <td className="note">{human(a.size)}</td>
                  <td className="mono note">
                    <QuickTip text={a.sha256} className="quicktip-inline">
                      <span>{a.sha256.slice(0, 16)}…</span>
                    </QuickTip>
                  </td>
                  <td style={{ textAlign: "right" }}>
                    <a className="btn action-btn" href={a.url} download>
                      <ActionIcon kind="download" />
                      <span>{t("proxyPage.download")}</span>
                    </a>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>

      {admin && (events ?? []).length > 0 && (
        <div className="card">
          <div className="card-head">
            <div className="kicker">{t("proxyPage.proxyJournal")}</div>
          </div>
          <table>
            <tbody>
              {(events ?? []).map((e) => (
                <tr key={e.id}>
                  <td className="note mono" style={{ whiteSpace: "nowrap", width: 150 }}>
                    {new Date(e.ts).toLocaleString(i18n.language)}
                  </td>
                  <td className="note mono" style={{ width: 190 }}>
                    {e.type}
                  </td>
                  <td>{e.message}</td>
                  <td className="note" style={{ textAlign: "right" }}>
                    {e.actor}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {upgrading && (
        <UpgradeModal
          proxy={upgrading}
          target={instance?.build ?? ""}
          onClose={() => setUpgrading(null)}
          onDone={(msg) => {
            setUpgrading(null);
            invalidateProxies();
            showToast(msg);
          }}
        />
      )}
      {renaming && (
        <RenameModal
          proxy={renaming}
          onClose={() => setRenaming(null)}
          onDone={(name) => {
            setRenaming(null);
            invalidateProxies();
            showToast(t("proxyPage.proxyRenamedToast", { name }));
          }}
        />
      )}
      {prefixing && (
        <ProxyPrefixesModal
          proxyID={prefixing.id}
          proxyName={prefixing.name}
          onClose={() => setPrefixing(null)}
        />
      )}
      {covering && (
        <CoverageModal
          proxy={covering}
          sites={sites ?? []}
          admin={admin}
          adminSites={adminSites}
          onClose={() => setCovering(null)}
          onDone={(msg) => {
            setCovering(null);
            invalidateProxies();
            showToast(msg);
          }}
        />
      )}
      {migrating && (
        <MigrateModal
          proxy={migrating}
          onClose={() => setMigrating(null)}
          onDone={(url) => {
            setMigrating(null);
            invalidateProxies();
            showToast(t("proxyPage.migrationScheduledToast", { url }));
          }}
        />
      )}
      {toast}
    </>
  );
}

/**
 * Version of a proxy, read at a glance. The badge states the gap from the
 * instance's version: a half-upgraded fleet must be visible, not guessed
 * at (see docs/01-Architecture/Version and update of the proxy).
 */
function UpgradeCell({
  p,
  target,
  editable,
  onEdit,
}: {
  p: ProxyInfo;
  target: string;
  editable: boolean;
  onEdit: () => void;
}) {
  const { t } = useTranslation();
  const state = upgradeBadge(p, target, t);
  const body = (
    <>
      <span className="mono" style={{ fontSize: 11 }}>
        {p.version || "—"}
      </span>
      {state && (
        <span className="badge" style={{ background: state.bg, color: state.fg }}>
          {state.label}
        </span>
      )}
    </>
  );
  return (
    <div className="upgrade-cell">
      {editable && !p.internal ? (
        <QuickTip text={t("proxyPage.upgradeTitle", { name: p.name })} className="quicktip-inline">
          <button className="rename" onClick={onEdit}>
            {body}
            <span aria-hidden="true">✎</span>
          </button>
        </QuickTip>
      ) : (
        body
      )}
      {p.upgrade_state === "failed" && p.upgrade_error && (
        <div className="note" style={{ color: "var(--danger)" }}>
          {p.upgrade_version} : {p.upgrade_error}
        </div>
      )}
    </div>
  );
}

function upgradeBadge(
  p: ProxyInfo,
  target: string,
  t: (k: string, o?: Record<string, unknown>) => string,
): { label: string; bg: string; fg: string } | null {
  if (p.internal) return null;
  if (p.upgrade_state === "failed")
    return { label: t("proxyPage.badgeFailed"), bg: "var(--danger)", fg: "#fff" };
  if (p.upgrade_state === "offered" || p.upgrade_state === "downloading")
    return { label: t("proxyPage.badgeInProgress"), bg: "var(--warn)", fg: "#000" };
  if (p.pin_version && p.pin_version !== p.version)
    return { label: t("proxyPage.badgePinned", { version: p.pin_version }), bg: "var(--bg-hover)", fg: "var(--text)" };
  if (!p.version || !target) return null;
  if (p.version === target) return { label: t("proxyPage.badgeUpToDate"), bg: "var(--ok)", fg: "#fff" };
  if (target > p.version)
    return { label: t("proxyPage.badgeUpgradeAvailable"), bg: "var(--accent)", fg: "#fff" };
  return null;
}

/**
 * Proxy update policy. "Update now" bypasses auto-upgrade without changing
 * it; pinning is used to roll back a version and to take precedence over
 * the instance's target version.
 */
function UpgradeModal({
  proxy,
  target,
  onClose,
  onDone,
}: {
  proxy: ProxyInfo;
  target: string;
  onClose: () => void;
  onDone: (msg: string) => void;
}) {
  const { t } = useTranslation();
  const [auto, setAuto] = useState(proxy.auto_upgrade);
  const [pin, setPin] = useState(proxy.pin_version);

  const save = useMutation({
    mutationFn: (now: boolean) =>
      put<ProxyInfo>(`/api/proxies/${proxy.id}/upgrade`, {
        auto_upgrade: auto,
        pin_version: pin.trim(),
        now,
      }),
    onSuccess: (_d, now) =>
      onDone(now ? t("proxyPage.upgradeRequestedToast") : t("proxyPage.policySavedToast")),
  });

  const behind = !!proxy.version && !!target && target > proxy.version;

  return (
    <Modal
      title={t("proxyPage.upgradeTitle", { name: proxy.name })}
      onClose={onClose}
      help={<p>{t("proxyPage.upgradeHelp1Full")}</p>}
    >
      <table style={{ marginBottom: 12 }}>
        <tbody>
          <tr>
            <td className="note">{t("proxyPage.proxyVersion")}</td>
            <td className="mono">{proxy.version || t("proxyPage.unknownVersion")}</td>
          </tr>
          <tr>
            <td className="note">{t("proxyPage.instanceBuild")}</td>
            <td className="mono">{target || t("proxyPage.unknownBuild")}</td>
          </tr>
          {proxy.os && (
            <tr>
              <td className="note">{t("proxyPage.platform")}</td>
              <td className="mono">
                {proxy.os}/{proxy.arch} · {t("proxyPage.protocolLabel")} {proxy.protocol}
              </td>
            </tr>
          )}
          {proxy.upgrade_state && (
            <tr>
              <td className="note">{t("proxyPage.lastState")}</td>
              <td>
                {proxy.upgrade_state} {proxy.upgrade_version && `(${proxy.upgrade_version})`}
                {proxy.upgrade_error && (
                  <div className="note" style={{ color: "var(--danger)" }}>
                    {proxy.upgrade_error}
                  </div>
                )}
              </td>
            </tr>
          )}
        </tbody>
      </table>

      <label className="row" style={{ gap: 8, marginBottom: 8 }}>
        <input type="checkbox" checked={auto} onChange={(e) => setAuto(e.target.checked)} />
        <span>{t("proxyPage.autoUpgrade")}</span>
      </label>
      <Field label={t("proxyPage.pinVersion")}>
        <input
          value={pin}
          onChange={(e) => setPin(e.target.value)}
          placeholder="20260726.181502"
          className="mono"
        />
      </Field>
      <ErrorMsg error={save.error} />
      <div className="actions">
        <ActionButton kind="cancel" className="secondary" onClick={onClose}>
          {t("common.cancel")}
        </ActionButton>
        <ActionButton
          kind="refresh"
          className="ghost"
          disabled={save.isPending || (!behind && !pin.trim())}
          onClick={() => save.mutate(true)}
          title={behind ? "" : t("proxyPage.noDifferentVersion")}
        >
          {t("proxyPage.upgradeNow")}
        </ActionButton>
        <ActionButton kind="save" disabled={save.isPending} onClick={() => save.mutate(false)}>
          {t("common.save")}
        </ActionButton>
      </div>
    </Modal>
  );
}

/**
 * Renaming: the name is just a tracking label (the identity remains the
 * UUID), but it's the name that gets read in logs and lists — so it must
 * be correctable. Same right as revoke or migrate: a proxy shared across
 * several sites cannot be renamed from just one of them.
 */
function RenameModal({
  proxy,
  onClose,
  onDone,
}: {
  proxy: ProxyInfo;
  onClose: () => void;
  onDone: (name: string) => void;
}) {
  const { t } = useTranslation();
  const [name, setName] = useState(proxy.name);
  const trimmed = name.trim();

  const rename = useMutation({
    mutationFn: () => patch<ProxyInfo>(`/api/proxies/${proxy.id}`, { name: trimmed }),
    onSuccess: (p) => onDone(p.name),
  });

  return (
    <Modal
      title={t("proxyPage.renameTitle", { name: proxy.name })}
      onClose={onClose}
      help={<p>{t("proxyPage.renameHelp1")}</p>}
    >
      <form
        onSubmit={(e) => {
          e.preventDefault();
          if (trimmed && trimmed !== proxy.name) rename.mutate();
        }}
      >
        <Field label={t("proxyPage.proxyName")}>
          <input
            value={name}
            onChange={(e) => setName(e.target.value)}
            maxLength={80}
            autoFocus
            placeholder={t("proxyPage.renamePlaceholder")}
          />
        </Field>
        <ErrorMsg error={rename.error} />
        <div className="actions">
          <ActionButton type="button" kind="cancel" className="secondary" onClick={onClose}>
            {t("common.cancel")}
          </ActionButton>
          <ActionButton type="submit" kind="rename" disabled={!trimmed || trimmed === proxy.name || rename.isPending}>
            {t("proxyPage.rename")}
          </ActionButton>
        </div>
      </form>
    </Modal>
  );
}

/**
 * Modifying the coverage of an existing proxy. Two behaviors, because
 * these are two different rights:
 *
 * - site-admin: they check/uncheck THEIR sites, one by one (`PUT`/`DELETE
 *   /api/proxies/{id}/sites/{siteID}`). The other covered sites are visible
 *   but locked — the proxy can be used by someone else.
 * - global-admin: they redefine the entire coverage and can promote the
 *   proxy to an instance proxy (`PUT /api/proxies/{id}/coverage`).
 */
function CoverageModal({
  proxy,
  sites,
  admin,
  adminSites,
  onClose,
  onDone,
}: {
  proxy: ProxyInfo;
  sites: Site[];
  admin: boolean;
  adminSites: string[];
  onClose: () => void;
  onDone: (msg: string) => void;
}) {
  const { t } = useTranslation();
  const [global, setGlobal] = useState(proxy.global);
  const [selected, setSelected] = useState<string[]>(proxy.sites.map((s) => s.id));
  const covered = new Set(proxy.sites.map((s) => s.id));

  // A site-admin acts site by site: each switch is a call, refused by
  // the controller if she would go beyond her rights or empty the cover.
  const one = useMutation({
    mutationFn: (v: { siteID: string; add: boolean }) =>
      v.add
        ? put(`/api/proxies/${proxy.id}/sites/${v.siteID}`)
        : del(`/api/proxies/${proxy.id}/sites/${v.siteID}`),
    onSuccess: (_d, v) =>
      onDone(v.add ? t("proxyPage.proxyAddedToSiteToast") : t("proxyPage.proxyRemovedFromSiteToast")),
  });
  const whole = useMutation({
    mutationFn: () =>
      put(`/api/proxies/${proxy.id}/coverage`, { global, site_ids: global ? [] : selected }),
    onSuccess: () => onDone(t("proxyPage.coverageUpdatedToast")),
  });

  const toggle = (id: string) =>
    setSelected((s) => (s.includes(id) ? s.filter((x) => x !== id) : [...s, id]));

  const visible = admin ? sites : sites.filter((s) => adminSites.includes(s.id) || covered.has(s.id));

  return (
    <Modal
      title={t("proxyPage.coverageTitle", { name: proxy.name })}
      onClose={onClose}
      help={
        <p>
          {t("proxyPage.coverageModalHelp1")}
          {!admin && t("proxyPage.coverageRightsNote")}
        </p>
      }
    >
      {admin && (
        <label className="row" style={{ gap: 8, marginBottom: 8 }}>
          <input
            type="checkbox"
            checked={global}
            onChange={(e) => setGlobal(e.target.checked)}
            disabled={proxy.internal}
          />
          <span>{t("proxyPage.instanceProxyAllSites")}</span>
        </label>
      )}
      {!global && (
        <div className="vlan-pick" style={{ maxHeight: 300 }}>
          {visible.map((s) => {
            const mine = admin || adminSites.includes(s.id);
            const on = admin ? selected.includes(s.id) : covered.has(s.id);
            return (
              <QuickTip
                key={s.id}
                text={mine ? undefined : t("proxyPage.siteNotAdminTitle")}
                className="quicktip-block"
              >
                <button
                  type="button"
                  className={"vlan-row" + (on ? " sel" : "")}
                  disabled={!mine || one.isPending}
                  onClick={() =>
                    admin ? toggle(s.id) : one.mutate({ siteID: s.id, add: !covered.has(s.id) })
                  }
                >
                  <span
                    className="vlan-chip"
                    style={{
                      margin: 0,
                      background: on ? "var(--accent)" : "transparent",
                      border: "1px solid var(--border)",
                    }}
                  />
                  <span style={{ fontWeight: 600 }}>{s.name}</span>
                  {!mine && <span className="note">{t("proxyPage.outOfRights")}</span>}
                </button>
              </QuickTip>
            );
          })}
        </div>
      )}
      <ErrorMsg error={one.error ?? whole.error} />
      <div className="actions">
        <ActionButton kind={admin ? "cancel" : "close"} className="secondary" onClick={onClose}>
          {admin ? t("common.cancel") : t("common.closeAction")}
        </ActionButton>
        {admin && (
          <ActionButton kind="save" disabled={whole.isPending} onClick={() => whole.mutate()}>
            {t("proxyPage.saveCoverage")}
          </ActionButton>
        )}
      </div>
    </Modal>
  );
}

function MigrateModal({
  proxy,
  onClose,
  onDone,
}: {
  proxy: ProxyInfo;
  onClose: () => void;
  onDone: (url: string) => void;
}) {
  const { t } = useTranslation();
  const [url, setUrl] = useState("https://");
  const [token, setToken] = useState("");

  const migrate = useMutation({
    mutationFn: () =>
      post(`/api/proxies/${proxy.id}/migrate`, {
        controller_url: url.trim(),
        enroll_token: token.trim(),
      }),
    onSuccess: () => onDone(url.trim()),
  });

  return (
    <Modal title={t("proxyPage.migrateTitle", { name: proxy.name })} onClose={onClose}>
      <p className="note" style={{ marginTop: 0 }}>
        {t("proxyPage.migrateHint")}
      </p>
      <Field label={t("proxyPage.newInstanceUrl")}>
        <input
          value={url}
          onChange={(e) => setUrl(e.target.value)}
          placeholder="https://meshcraft.exemple.net"
          autoFocus
        />
      </Field>
      <Field label={t("proxyPage.enrollTokenThere")}>
        <input
          value={token}
          onChange={(e) => setToken(e.target.value)}
          placeholder={t("proxyPage.pasteToken")}
          className="mono"
        />
      </Field>
      <p className="note">
        {t("proxyPage.migrateApplyNoteFull")}
      </p>
      <ErrorMsg error={migrate.error} />
      <div className="actions">
        <ActionButton kind="cancel" className="secondary" onClick={onClose}>
          {t("common.cancel")}
        </ActionButton>
        <ActionButton
          kind="migrate"
          disabled={!token.trim() || url.trim().length < 10 || migrate.isPending}
          onClick={() => migrate.mutate()}
        >
          Schedule migration
        </ActionButton>
      </div>
    </Modal>
  );
}

