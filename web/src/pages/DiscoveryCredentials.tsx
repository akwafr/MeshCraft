import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { del, get, patch, post, type CredKind, type KnownCredential } from "../api";
import { ActionButton, DeleteButton, ErrorMsg, Field, HelpTip, useToast } from "../components";

/**
 * Discovery credentials: how to connect to a device meshcraft does not manage yet.
 *
 * They belong to the INSTANCE, not a site — discovery precedes site choice — and
 * that is why this screen lives in global configuration, beside users and proxies.
 * It once lived under a site's « Add a device » menu: nobody looked there, and
 * the operator re-entered their password on every probe.
 *
 * Do not confuse with a site's « Credentials » screen (pages/SiteCredentials),
 * which manages credentials APPLIED to already-adopted devices. This one is for
 * reaching an unknown.
 */

/**
 * What is tried, kind by kind. This table is the heart of the screen: a kind
 * stored but never attempted is exactly what wasted time — the credential existed,
 * displayed, and was never presented to anyone.
 */
const KINDS_KEYS: { kind: CredKind; labelKey?: string; label?: string; tried: boolean; howKey: string }[] = [
  { kind: "any", labelKey: "kindAll", tried: true, howKey: "kindHowAny" },
  { kind: "ssh", label: "ssh", tried: true, howKey: "kindHowSsh" },
  { kind: "http", label: "http", tried: true, howKey: "kindHowHttp" },
  { kind: "telnet", label: "telnet", tried: false, howKey: "kindHowTelnet" },
];

export default function DiscoveryCredentials() {
  const { t } = useTranslation();
  const KINDS = KINDS_KEYS.map((k) => ({
    kind: k.kind,
    label: k.labelKey ? t(`discoveryCredsPage.${k.labelKey}`) : k.label!,
    tried: k.tried,
    how: t(`discoveryCredsPage.${k.howKey}`),
  }));
  const qc = useQueryClient();
  const [name, setName] = useState("");
  const [username, setUsername] = useState("");
  const [secret, setSecret] = useState("");
  const [kind, setKind] = useState<CredKind>("any");
  const [priority, setPriority] = useState(100);
  const [toast, showToast] = useToast();

  const { data, error } = useQuery({
    queryKey: ["known-credentials"],
    queryFn: () =>
      get<{
        credentials: KnownCredential[];
        key_external: boolean;
        unreadable_secrets: string[];
      }>("/api/discovery/credentials"),
  });
  const unreadable = new Set(data?.unreadable_secrets ?? []);

  const create = useMutation({
    mutationFn: () =>
      post<KnownCredential>("/api/discovery/credentials", {
        name: name.trim(),
        kind,
        username: username.trim(),
        secret,
        priority,
        enabled: true,
      }),
    onSuccess: (c) => {
      setName("");
      setUsername("");
      setSecret("");
      qc.invalidateQueries({ queryKey: ["known-credentials"] });
      showToast(t("discoveryCredsPage.addedToast", { name: c.name }));
    },
  });

  const toggle = useMutation({
    mutationFn: (c: KnownCredential) =>
      patch<KnownCredential>(`/api/discovery/credentials/${c.id}`, {
        name: c.name,
        kind: c.kind,
        username: c.username,
        secret: "",
        priority: c.priority,
        enabled: !c.enabled,
      }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["known-credentials"] }),
  });

  const remove = useMutation({
    mutationFn: (id: string) => del(`/api/discovery/credentials/${id}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["known-credentials"] }),
  });

  return (
    <div>
      <div className="toolbar">
        <h1>
          {t("discoveryCredsPage.title")}
          <HelpTip label={t("discoveryCredsPage.helpLabel")}>
            <p>{t("discoveryCredsPage.help1")}</p>
            <p>{t("discoveryCredsPage.help2")}</p>
          </HelpTip>
          <span className="sub">
            {t("discoveryCredsPage.subtitle")}
          </span>
        </h1>
      </div>

      <div className="card">
        <div className="row" style={{ alignItems: "flex-end" }}>
          <div style={{ flex: 1 }}>
            <Field label={t("discoveryCredsPage.label")}>
              <input
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder={t("discoveryCredsPage.labelPlaceholder")}
              />
            </Field>
          </div>
          <div style={{ width: 160 }}>
            <Field label={t("discoveryCredsPage.username")}>
              <input
                value={username}
                onChange={(e) => setUsername(e.target.value)}
                autoComplete="off"
              />
            </Field>
          </div>
          <div style={{ width: 160 }}>
            <Field
              label={t("discoveryCredsPage.password")}
              help={<p>{t("discoveryCredsPage.passwordHelp")}</p>}
            >
              <input
                type="password"
                value={secret}
                onChange={(e) => setSecret(e.target.value)}
                autoComplete="new-password"
              />
            </Field>
          </div>
          <div style={{ width: 140 }}>
            <Field label={t("discoveryCredsPage.transport")}>
              <select
                value={kind}
                onChange={(e) => setKind(e.target.value as CredKind)}
              >
                {KINDS.map((k) => (
                  <option key={k.kind} value={k.kind}>
                    {k.label}
                    {k.tried ? "" : t("discoveryCredsPage.notTriedSuffix")}
                  </option>
                ))}
              </select>
            </Field>
          </div>
          <div style={{ width: 100 }}>
            <Field label={t("discoveryCredsPage.priority")}>
              <input
                type="number"
                value={priority}
                onChange={(e) => setPriority(Number(e.target.value))}
              />
            </Field>
          </div>
          <ActionButton
            kind="add"
            disabled={!name.trim() || !username.trim() || create.isPending}
            onClick={() => create.mutate()}
          >
            {t("common.add")}
          </ActionButton>
        </div>
        <ErrorMsg error={create.error} />
      </div>

      <div className="card" style={{ marginTop: 12 }}>
        <div className="kicker">
          {t("discoveryCredsPage.whatIsTried")}
          <HelpTip label={t("discoveryCredsPage.webAuthHelpLabel")}>
            <p>{t("discoveryCredsPage.webAuthHelp1")}</p>
            <p>{t("discoveryCredsPage.webAuthHelp2")}</p>
          </HelpTip>
        </div>
        <table className="ports-table" style={{ marginTop: 8 }}>
          <tbody>
            {KINDS.map((k) => (
              <tr key={k.kind}>
                <td style={{ width: 80 }}>
                  <span className="chip">{k.label}</span>
                </td>
                <td style={{ width: 120 }}>
                  <span
                    className="dot"
                    style={{
                      background: k.tried ? "var(--ok)" : "var(--warn)",
                      marginRight: 6,
                    }}
                  />
                  {k.tried ? t("discoveryCredsPage.tried") : t("discoveryCredsPage.notTried")}
                </td>
                <td className="note" style={{ lineHeight: 1.5 }}>
                  {k.how}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      {unreadable.size > 0 && (
        <div className="card" style={{ marginTop: 12 }}>
          <div className="row" style={{ gap: 8 }}>
            <span className="dot" style={{ background: "var(--danger)" }} />
            <span style={{ fontSize: 12, lineHeight: 1.5 }}>
              <b>{t("discoveryCredsPage.secretsUndecryptable", { count: unreadable.size })}</b>{" "}
              {t("discoveryCredsPage.undecryptableNote")}
            </span>
          </div>
        </div>
      )}

      {data && !data.key_external && (
        <div className="card" style={{ marginTop: 12 }}>
          <div className="row" style={{ gap: 8 }}>
            <span className="dot" style={{ background: "var(--warn)" }} />
            <span style={{ fontSize: 12, lineHeight: 1.5 }}>
              <b>{t("discoveryCredsPage.keyInDbWarning")}</b>, {t("discoveryCredsPage.keyInDbDetail")}
            </span>
          </div>
        </div>
      )}

      <ErrorMsg error={error} />

      {(data?.credentials ?? []).length === 0 ? (
        <div className="card" style={{ marginTop: 12 }}>
          <div className="empty">
            {t("discoveryCredsPage.noCredentials")}
          </div>
        </div>
      ) : (
        <div className="card" style={{ marginTop: 12, padding: 0, overflowX: "auto" }}>
          <table className="ports-table">
            <thead>
              <tr>
                <th>{t("discoveryCredsPage.priority")}</th>
                <th>{t("discoveryCredsPage.label")}</th>
                <th>{t("discoveryCredsPage.username")}</th>
                <th>{t("discoveryCredsPage.transport")}</th>
                <th>{t("discoveryCredsPage.secret")}</th>
                <th>{t("common.stateCol")}</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {(data?.credentials ?? []).map((c) => (
                <tr key={c.id}>
                  <td style={{ fontVariantNumeric: "tabular-nums" }}>{c.priority}</td>
                  <td style={{ fontWeight: 700 }}>{c.name}</td>
                  <td>{c.username}</td>
                  <td>
                    <span className="chip">{c.kind === "any" ? t("discoveryCredsPage.kindAll") : c.kind}</span>
                    {c.kind === "telnet" && (
                      <span className="note" style={{ marginLeft: 6 }}>
                        {t("discoveryCredsPage.notTried")}
                      </span>
                    )}
                  </td>
                  <td className="note">
                    {unreadable.has(c.id) ? (
                      <span style={{ color: "var(--danger)", fontWeight: 700 }}>
                        {t("discoveryCredsPage.unreadable")}
                      </span>
                    ) : c.has_secret ? (
                      t("discoveryCredsPage.stored")
                    ) : (
                      t("discoveryCredsPage.empty")
                    )}
                  </td>
                  <td>
                    <ActionButton kind="apply" className="secondary" onClick={() => toggle.mutate(c)}>
                      {c.enabled ? t("common.active") : t("common.disabled")}
                    </ActionButton>
                  </td>
                  <td style={{ textAlign: "right" }}>
                    <DeleteButton onConfirm={() => remove.mutate(c.id)} />
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {toast}
    </div>
  );
}
