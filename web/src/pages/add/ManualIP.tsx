import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { useSite, useSiteId } from "../../useSite";
import {
  get,
  post,
  type Discovered,
  type DriverRef,
  type KnownCredential,
  type ProbeResult,
  type ProxyInfo,
  type ScanPrefix,
} from "../../api";
import { useAuth } from "../../auth";
import { ActionButton, ErrorMsg, Field, useToast } from "../../components";
import AdoptModal from "./AdoptModal";
import { resolveAutoProxy } from "./autoProxy";

/**
 * Manual add by IP address.
 *
 * A real connection is attempted: opening administration ports, reading the
 * SSH banner and the web page, then — with the chosen credential or the
 * instance's discovery credentials — opening an SSH session, or presenting
 * the credential to the web interface, to ask the device what it is and
 * possibly its configuration.
 *
 * The credential choice is a SELECTOR and not two input fields: a password
 * already entrusted to the instance shouldn't be retyped to probe an
 * address. One-off entry remains possible — that's the case of a device
 * being discovered with a password one doesn't want to save.
 */

/** ALL = all saved credentials; MANUAL = one-off entry. */
const ALL_CREDS = "";
const MANUAL_CRED = "manual";

/**
 * "Try every path": probe the address from EVERY path the site has, and let
 * the operator pick among the ones that answered.
 *
 * IT USED TO BE CALLED "Automatic", and so does the serial wizard's option,
 * which does the opposite thing: that one resolves ONE proxy from the declared
 * prefixes and is now "By declared range". Two gestures under one word, on
 * two screens of the same flow, and the only way to tell them apart was to run
 * them. Here the address is typed by hand, precisely in the case where nobody
 * knows which proxy sees it — so resolving from a declaration would answer a
 * question the operator has not been able to answer either. The declaration is
 * still read, but only to ANNOTATE the rows: it is information for the choice,
 * not the choice itself.
 *
 * The VALUE stays "auto": it is the select's option id, never shown, and
 * changing it would be churn with no reader.
 */
const AUTO_PROXY = "auto";

/** One path tried by the automatic sweep, and what came back. */
type SweepRow = {
  /** What `/api/discovery/probe` receives — "" is the local instance. */
  proxyId: string;
  proxyName: string;
  /** A declared prefix of this proxy that contains the address, if any. */
  declared?: string;
  probe?: ProbeResult;
  candidate?: Discovered;
  /** The failure, shown verbatim: a 403 on one proxy is a fact, not a bug. */
  error?: string;
};

/** Something answered on an administration port — cf. Prober.Probe. */
function functional(r: SweepRow): boolean {
  return !!r.probe && r.probe.reachable;
}

export default function ManualIP() {
  const { t } = useTranslation();
  const { site } = useSite();
  const siteId = useSiteId();
  const siteSlug = site?.slug ?? "";
  const { user } = useAuth();
  const qc = useQueryClient();
  const [ip, setIp] = useState("");
  const [proxyID, setProxyID] = useState("");
  const [credChoice, setCredChoice] = useState(ALL_CREDS);
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [driver, setDriver] = useState("");
  const [fetchConfig, setFetchConfig] = useState(true);
  const [result, setResult] = useState<{ candidate: Discovered; probe: ProbeResult } | null>(null);
  const [adopting, setAdopting] = useState<Discovered | null>(null);
  const [toast, showToast] = useToast();
  // The automatic sweep: null = never run, [] = running with nothing back yet.
  const [sweep, setSweep] = useState<SweepRow[] | null>(null);
  const [sweeping, setSweeping] = useState(false);
  const [sweptIp, setSweptIp] = useState("");
  const [sweepError, setSweepError] = useState<unknown>(null);

  const { data: proxies } = useQuery({
    queryKey: ["discovery-proxies", siteId],
    queryFn: () => get<ProxyInfo[]>(`/api/sites/${siteId}/discovery-proxies`),
    enabled: !!siteId,
  });
  // Read only to annotate the sweep's rows with "this proxy declares a range
  // containing the address". Same query key as the serial wizard: one fetch.
  const { data: scanPrefixes } = useQuery({
    queryKey: ["scan-prefixes"],
    queryFn: () => get<ScanPrefix[]>("/api/discovery/prefixes"),
  });
  const { data: drivers } = useQuery({
    queryKey: ["drivers"],
    queryFn: () => get<DriverRef[]>("/api/discovery/drivers"),
  });
  // The discovery credentials list is reserved to the global admin: for
  // others, the selector reduces to "all saved credentials" or "one-off
  // entry". They don't need to know the instance's accounts to use them —
  // the controller is what tries them, never the browser.
  const canListCreds = !!user?.is_global_admin;
  const { data: creds } = useQuery({
    queryKey: ["known-credentials"],
    enabled: canListCreds,
    queryFn: () =>
      get<{ credentials: KnownCredential[]; unreadable_secrets: string[] }>(
        "/api/discovery/credentials",
      ),
  });
  const usableCreds = (creds?.credentials ?? []).filter(
    (c) => c.enabled && !(creds?.unreadable_secrets ?? []).includes(c.id),
  );
  const manual = credChoice === MANUAL_CRED;
  const auto = proxyID === AUTO_PROXY;

  /** One probe, through one proxy. The only call this screen makes. */
  function probeOnce(proxy: string, withConfig: boolean) {
    return post<{ candidate: Discovered; probe: ProbeResult }>("/api/discovery/probe", {
      ip: ip.trim(),
      proxy_id: proxy,
      // Only one of the two is sent: the typed credential, or the one
      // chosen among saved ones. Sending both would leave the controller
      // to arbitrate, and the choice was already made here.
      username: manual ? username.trim() : "",
      password: manual ? password : "",
      credential_id: manual ? "" : credChoice,
      driver,
      fetch_config: withConfig,
    });
  }

  const probe = useMutation({
    mutationFn: (v: { proxy: string; withConfig: boolean }) =>
      probeOnce(v.proxy, v.withConfig),
    onSuccess: (r) => {
      setResult(r);
      setSweep(null);
      qc.invalidateQueries({ queryKey: ["discovered"] });
      showToast(t("manualIp.reachableToast", { ip: r.probe.ip, driver: r.probe.driver }));
    },
  });

  // Every path the sweep will try: the local instance first — it is a path
  // like any other, and leaving it out would make "automatic" weaker than
  // the default it replaces — then each proxy covering the site.
  const localProxy = (proxies ?? []).find((p) => p.internal);
  const autoTargets = useMemo(
    () => [
      {
        proxyId: "",
        lookupId: localProxy?.id ?? "",
        name: t("common.localInstance") as string,
      },
      ...(proxies ?? [])
        .filter((p) => !p.internal)
        .map((p) => ({
          proxyId: p.id,
          lookupId: p.id,
          name: p.name || p.id.slice(0, 8),
        })),
    ],
    [proxies, localProxy, t],
  );

  /**
   * Probe the address through every path, IN SEQUENCE and WITHOUT reading the
   * configuration.
   *
   * Sequence, not Promise.all: this fans one address out onto N SSH logins,
   * and several ranges in the fleet refuse the channel past a handful of
   * sessions (a ProCurve 2610 authenticates then hangs up at the fifth). The
   * cost is small anyway — a proxy that does not see the address gives up on
   * the port scan, long before any login.
   *
   * No configuration read either: that is the slow half, it leaves a trace in
   * the device's log, and reading it N times to keep one would be rude to the
   * device for nothing. It is read once, through the proxy finally chosen.
   */
  async function runSweep() {
    setResult(null);
    setSweepError(null);
    setSweptIp(ip.trim());
    setSweeping(true);
    setSweep([]);
    const rows: SweepRow[] = [];
    try {
      for (const target of autoTargets) {
        const declared = resolveAutoProxy(
          ip.trim(),
          scanPrefixes ?? [],
          new Set([target.lookupId]),
        );
        let row: SweepRow = {
          proxyId: target.proxyId,
          proxyName: target.name,
          declared: declared?.prefix,
        };
        try {
          const r = await probeOnce(target.proxyId, false);
          row = { ...row, probe: r.probe, candidate: r.candidate };
        } catch (e) {
          // One path failing is a result, not the end of the sweep: a proxy
          // the operator may not administer answers 403, and the next one
          // may still be the right answer.
          row = { ...row, error: e instanceof Error ? e.message : String(e) };
        }
        rows.push(row);
        setSweep([...rows]);
      }
    } catch (e) {
      setSweepError(e);
    }
    setSweeping(false);

    // ONE path answered: there is nothing to choose, so do not ask. Several,
    // and the choice belongs to the operator — the controller cannot know
    // which proxy should carry this device from here on.
    const answered = rows.filter(functional);
    if (answered.length === 1) {
      pickSweepRow(answered[0]);
    }
  }

  /**
   * Take a swept row as the answer. The identification is already in hand; a
   * configuration, if one was asked for, is read now — through the chosen
   * proxy, because that is the path this device will be reached by.
   */
  function pickSweepRow(row: SweepRow) {
    setProxyID(row.proxyId);
    if (fetchConfig) {
      probe.mutate({ proxy: row.proxyId, withConfig: true });
      return;
    }
    if (row.probe && row.candidate) {
      setResult({ candidate: row.candidate, probe: row.probe });
      setSweep(null);
      showToast(
        t("manualIp.reachableToast", { ip: row.probe.ip, driver: row.probe.driver }),
      );
    }
  }

  const busy = probe.isPending || sweeping;

  return (
    <div>
      <div className="toolbar">
        <h1>
          {t("nav.manualIpAdd")}
          <span className="sub">{t("manualIp.subtitle")}</span>
        </h1>
      </div>

      <div className="card">
        <div className="row">
          <div className="addfield">
            <Field label={t("manualIp.ipAddress") as string}>
              <input
                value={ip}
                onChange={(e) => setIp(e.target.value)}
                placeholder="10.0.10.2 ou 2001:db8::2"
                autoFocus
              />
            </Field>
          </div>
          <div className="addfield-sm">
            <Field
              label={t("manualIp.proxyUsed") as string}
              help={<p>{t("manualIp.proxyEveryPathHelp")}</p>}
            >
              <select value={proxyID} onChange={(e) => setProxyID(e.target.value)}>
                <option value={AUTO_PROXY}>{t("manualIp.proxyEveryPath")}</option>
                <option value="">{t("common.localInstance")}</option>
                {(proxies ?? [])
                  .filter((p) => !p.internal)
                  .map((p) => (
                    <option key={p.id} value={p.id}>
                      {p.name || p.id.slice(0, 8)}
                    </option>
                  ))}
              </select>
            </Field>
          </div>
        </div>
        {/* What the sweep will cost, BEFORE the click: one probe per path, and
            the operator is the one who knows whether this device tolerates
            being knocked on from four directions. */}
        {auto && (
          <div className="note">
            {t("manualIp.proxyEveryPathCount", { count: autoTargets.length })}
          </div>
        )}
        <div className="row">
          <div className="addfield">
            <Field
              label={t("manualIp.credentialTried") as string}
              help={
                <>
                  <p>{t("manualIp.credHelp1")}</p>
                  <p>{t("manualIp.credHelp2")}</p>
                  <p>{t("manualIp.credHelp3")}</p>
                  {!canListCreds && <p>{t("manualIp.credHelp4")}</p>}
                </>
              }
            >
              <select
                value={credChoice}
                onChange={(e) => setCredChoice(e.target.value)}
              >
                <option value={ALL_CREDS}>{t("manualIp.instanceCredsOption")}</option>
                {usableCreds.map((c) => (
                  <option key={c.id} value={c.id}>
                    {c.name} — {c.username} ({c.kind === "any" ? t("manualIp.kindAny") : c.kind})
                  </option>
                ))}
                <option value={MANUAL_CRED}>{t("manualIp.oneOffOption")}</option>
              </select>
            </Field>
          </div>
          <div className="addfield-sm">
            <Field label={t("manualIp.deviceType") as string}>
              <select value={driver} onChange={(e) => setDriver(e.target.value)}>
                <option value="">{t("manualIp.autoDetect")}</option>
                {(drivers ?? []).map((d) => (
                  <option key={d.driver} value={d.driver}>
                    {d.driver} {d.vendor ? `(${d.vendor})` : ""}
                  </option>
                ))}
              </select>
            </Field>
          </div>
        </div>
        {manual && (
          <div className="row">
            <div className="addfield">
              <Field label={t("manualIp.username") as string}>
                <input
                  value={username}
                  onChange={(e) => setUsername(e.target.value)}
                  placeholder="admin"
                  autoComplete="off"
                  autoFocus
                />
              </Field>
            </div>
            <div className="addfield">
              <Field label={t("manualIp.passwordLabel") as string}>
                <input
                  type="password"
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                  autoComplete="new-password"
                />
              </Field>
            </div>
          </div>
        )}
        <label className="note row addcheck">
          <input
            type="checkbox"
            checked={fetchConfig}
            onChange={(e) => setFetchConfig(e.target.checked)}
          />
          {t("manualIp.alsoReadConfig")}
        </label>
        <ErrorMsg error={probe.error} />
        <ErrorMsg error={sweepError} />
        <div className="actions">
          {/* A one-off credential WITHOUT a username would silently fall back
              to "all instance credentials": that wouldn't be what was asked,
              and the typed password would go to waste.

              And "automatic" before the proxy list has arrived would sweep
              the local instance alone, then report that one path answered. */}
          <ActionButton
            kind="connect"
            disabled={
              !ip.trim() || (manual && !username.trim()) || busy || (auto && !proxies)
            }
            onClick={() => (auto ? runSweep() : probe.mutate({ proxy: proxyID, withConfig: fetchConfig }))}
          >
            {busy ? t("manualIp.connecting") : t("manualIp.testAndIdentify")}
          </ActionButton>
        </div>
        {/* The only fact that isn't explained, it's OBSERVED: the secret just
            typed will not be kept. The rest lives in the field's help. */}
        {manual && (
          <div className="note">{t("manualIp.notSaved")}</div>
        )}
      </div>

      {/* THE CHOICE IS NOT MADE HERE. Several proxies reaching the device is
          not a tie to break by latency: the right one is the one that will
          still see this device when it is adopted, and that is a fact about
          the network only the operator holds. The rows carry what there is to
          decide on, in the order the paths were tried — a list that reordered
          itself as answers arrived would be unreadable while it ran. */}
      {sweep !== null && (
        <div className="card addcard-gap">
          <div className="kicker">{t("manualIp.sweepTitle", { ip: sweptIp })}</div>
          <p className="note">
            {sweeping
              ? t("manualIp.sweepRunning", {
                  done: sweep.length,
                  total: autoTargets.length,
                })
              : t("manualIp.sweepPickOne")}
          </p>
          <div className="sweep-list">
            {sweep.map((row) => (
              <div
                key={row.proxyId || "local"}
                className={"sweep-row" + (functional(row) ? " ok" : "")}
              >
                <div className="sweep-head">
                  <span className="sweep-proxy">{row.proxyName}</span>
                  {row.declared && (
                    <span className="chip">
                      {t("manualIp.sweepDeclared", { prefix: row.declared })}
                    </span>
                  )}
                </div>
                {/* Server text verbatim, failure included. */}
                <div className="note sweep-facts">
                  {row.error
                    ? row.error
                    : !row.probe
                      ? t("common.searching")
                      : !row.probe.reachable
                        ? t("manualIp.sweepNoAnswer")
                        : t("manualIp.sweepAnswered", {
                            ports: row.probe.open_ports.join(", "),
                            latency: row.probe.latency_ms,
                            driver: row.probe.driver,
                            confidence: row.probe.confidence,
                          })}
                </div>
                {row.probe?.reachable && (
                  <div className="note sweep-facts">
                    {row.probe.login_ok
                      ? t("manualIp.authSuccessful", {
                          cred:
                            row.probe.credential_name ||
                            t("manualIp.manuallyEntered"),
                        })
                      : t("manualIp.authNotEstablished")}
                  </div>
                )}
                {functional(row) && (
                  <div className="actions">
                    <ActionButton
                      kind="connect"
                      className="secondary"
                      disabled={busy}
                      onClick={() => pickSweepRow(row)}
                    >
                      {t("manualIp.sweepUseThis")}
                    </ActionButton>
                  </div>
                )}
              </div>
            ))}
          </div>
          {!sweeping && sweep.every((r) => !functional(r)) && (
            <div className="empty">{t("manualIp.sweepNowhere", { ip: sweptIp })}</div>
          )}
          {!sweeping && (
            <div className="actions">
              <ActionButton kind="close" className="secondary" onClick={() => setSweep(null)}>
                {t("manualIp.sweepClose")}
              </ActionButton>
            </div>
          )}
        </div>
      )}

      {result && (
        <div className="card addcard-gap">
          <div className="kicker">{t("manualIp.result")}</div>
          <table className="ports-table addtable">
            <tbody>
              <Row label={t("common.address") as string} value={result.probe.ip} />
              <Row
                label={t("manualIp.openPorts") as string}
                value={result.probe.open_ports.join(", ") || t("manualIp.none")}
              />
              <Row label={t("discover.transportsCol") as string} value={result.probe.transports.join(", ")} />
              <Row
                label={t("discover.identificationCol") as string}
                value={`${result.probe.driver} — ${result.probe.evidence} (${t("discover.confidence", { value: result.probe.confidence })})`}
              />
              <Row label={t("manualIp.vendorModel") as string}
                value={[result.probe.vendor, result.probe.model].filter(Boolean).join(" ") || "—"} />
              <Row label={t("manualIp.hostname") as string} value={result.probe.hostname || "—"} />
              <Row label={t("manualIp.serialNumber") as string} value={result.probe.serial || "—"} />
              {/* WHICH credential worked: it's the question asked when facing
                  a probe result, and it only has an answer if it's named.
                  "successful" alone forced guessing which saved credential
                  had opened the door. */}
              <Row
                label={t("manualIp.authentication") as string}
                value={
                  result.probe.login_ok
                    ? t("manualIp.authSuccessful", {
                        cred: result.probe.credential_name || t("manualIp.manuallyEntered"),
                      })
                    : t("manualIp.authNotEstablished")
                }
              />
              {result.probe.web_auth && (
                <Row
                  label={t("manualIp.webInterface") as string}
                  value={`${webAuthLabel(result.probe.web_auth, t)}${
                    result.probe.web_auth_detail
                      ? ` — ${result.probe.web_auth_detail}`
                      : ""
                  }`}
                />
              )}
              {result.probe.ssh_banner && (
                <Row label={t("manualIp.sshBanner") as string} value={result.probe.ssh_banner} />
              )}
              {(result.probe.http_server || result.probe.http_title) && (
                <Row
                  label="Web"
                  value={[result.probe.http_server, result.probe.http_title]
                    .filter(Boolean)
                    .join(" — ")}
                />
              )}
              {result.probe.notes && <Row label={t("manualIp.notes") as string} value={result.probe.notes} />}
              <Row
                label={t("manualIp.configReadLabel") as string}
                value={
                  result.candidate.has_config
                    ? t("manualIp.yesImportable")
                    : t("manualIp.noIdentificationOnly")
                }
              />
            </tbody>
          </table>
          {result.probe.shell_output && (
            <details className="addraw">
              <summary className="note">{t("manualIp.rawOutput")}</summary>
              <pre className="raw-output">{result.probe.shell_output}</pre>
            </details>
          )}
          <div className="actions">
            <ActionButton kind="adopt" onClick={() => setAdopting(result.candidate)}>
              {t("discover.adoptEllipsis")}
            </ActionButton>
          </div>
        </div>
      )}

      {adopting && (
        <AdoptModal
          candidate={adopting}
          onClose={() => setAdopting(null)}
          onDone={(deviceName) => {
            setAdopting(null);
            setResult(null);
            qc.invalidateQueries({ queryKey: ["discovered"] });
            qc.invalidateQueries({ queryKey: ["devices"] });
            showToast(t("discover.adoptedToast", { name: deviceName }));
          }}
        />
      )}
      {toast}
    </div>
  );
}

/**
 * The three outcomes of the web probe, named for what they ask of the
 * operator. "refused" is the only case where there's a password to look
 * for: confusing the other two with a refusal sends someone hunting for
 * hours for a password nothing ever rejected.
 */
function webAuthLabel(code: NonNullable<ProbeResult["web_auth"]>, t: (key: string) => string): string {
  switch (code) {
    case "ok":
      return t("manualIp.webAuthOk");
    case "refused":
      return t("manualIp.webAuthRefused");
    case "no-http-auth":
      return t("manualIp.webAuthNoHttp");
    case "error":
      return t("manualIp.webAuthError");
  }
}

function Row({ label, value }: { label: string; value: string }) {
  return (
    <tr>
      <td className="note addrow-label">
        {label}
      </td>
      <td className="addrow-value">{value}</td>
    </tr>
  );
}
