import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
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
} from "../../api";
import { useAuth } from "../../auth";
import { ActionButton, ErrorMsg, Field, useToast } from "../../components";
import AdoptModal from "./AdoptModal";

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

  const { data: proxies } = useQuery({
    queryKey: ["discovery-proxies", siteId],
    queryFn: () => get<ProxyInfo[]>(`/api/sites/${siteId}/discovery-proxies`),
    enabled: !!siteId,
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

  const probe = useMutation({
    mutationFn: () =>
      post<{ candidate: Discovered; probe: ProbeResult }>("/api/discovery/probe", {
        ip: ip.trim(),
        proxy_id: proxyID,
        // Only one of the two is sent: the typed credential, or the one
        // chosen among saved ones. Sending both would leave the controller
        // to arbitrate, and the choice was already made here.
        username: manual ? username.trim() : "",
        password: manual ? password : "",
        credential_id: manual ? "" : credChoice,
        driver,
        fetch_config: fetchConfig,
      }),
    onSuccess: (r) => {
      setResult(r);
      qc.invalidateQueries({ queryKey: ["discovered"] });
      showToast(t("manualIp.reachableToast", { ip: r.probe.ip, driver: r.probe.driver }));
    },
  });

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
          <div style={{ flex: 1 }}>
            <Field label={t("manualIp.ipAddress") as string}>
              <input
                value={ip}
                onChange={(e) => setIp(e.target.value)}
                placeholder="10.0.10.2 ou 2001:db8::2"
                autoFocus
              />
            </Field>
          </div>
          <div style={{ width: 220 }}>
            <Field label={t("manualIp.proxyUsed") as string}>
              <select value={proxyID} onChange={(e) => setProxyID(e.target.value)}>
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
        <div className="row">
          <div style={{ flex: 1 }}>
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
          <div style={{ width: 220 }}>
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
            <div style={{ flex: 1 }}>
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
            <div style={{ flex: 1 }}>
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
        <label className="note row" style={{ gap: 6 }}>
          <input
            type="checkbox"
            checked={fetchConfig}
            onChange={(e) => setFetchConfig(e.target.checked)}
          />
          {t("manualIp.alsoReadConfig")}
        </label>
        <ErrorMsg error={probe.error} />
        <div className="actions">
          {/* A one-off credential WITHOUT a username would silently fall back
              to "all instance credentials": that wouldn't be what was asked,
              and the typed password would go to waste. */}
          <ActionButton
            kind="connect"
            disabled={!ip.trim() || (manual && !username.trim()) || probe.isPending}
            onClick={() => probe.mutate()}
          >
            {probe.isPending ? t("manualIp.connecting") : t("manualIp.testAndIdentify")}
          </ActionButton>
        </div>
        {/* The only fact that isn't explained, it's OBSERVED: the secret just
            typed will not be kept. The rest lives in the field's help. */}
        {manual && (
          <div className="note">{t("manualIp.notSaved")}</div>
        )}
      </div>

      {result && (
        <div className="card" style={{ marginTop: 12 }}>
          <div className="kicker">{t("manualIp.result")}</div>
          <table className="ports-table" style={{ marginTop: 8 }}>
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
            <details style={{ marginTop: 8 }}>
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
      <td className="note" style={{ width: 200 }}>
        {label}
      </td>
      <td style={{ wordBreak: "break-word" }}>{value}</td>
    </tr>
  );
}
