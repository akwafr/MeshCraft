import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { useSite, useSiteId } from "../useSite";
import { del, get, post, put, type KnownCredential, type SiteCredential } from "../api";
import { useAuth, useSiteRole } from "../auth";
import {
  ActionButton,
  DeleteButton,
  ErrorMsg,
  Field,
  HelpTip,
  Modal,
  QuickTip,
  isReservedTagName,
  useToast,
} from "../components";

/**
 * {t("siteCredsPage.title")}: what meshcraft installs on the equipment, and
 * what an administrator uses to connect to it directly.
 *
 * The list never displays a secret — only its presence and rotation date.
 * Revealing it is a distinct action, reserved for the site-admin, and
 * logged in the audit trail.
 *
 * Three types coexist (docs/03-Security/SNMP Identity §2), and the screen
 * must make them impossible to confuse: an administration password opens
 * the door to equipment, a v2c community only reads, and a v3 identity is
 * only valid for one cryptographic family. Displaying them all "the same
 * way" would risk revealing one while believing you're reading the other.
 */

/** Kinds, such as `site_credentials.kind` carry them server-side. */
const KIND_ADMIN = "admin";
const KIND_V2C = "snmp_v2c";
const KIND_V3 = "snmp_v3";

/**
 * `tag` and `priority` travel alongside the identifier in the API payload:
 * a v3 identity is identified ONLY by its label, and resubmitting it on
 * save is essential — an omitted `tag` would target the "site" scope and
 * create a second identifier instead of modifying the one being edited.
 */
type Cred = SiteCredential & { tag?: string; priority?: number };

/**
 * Nameable algorithms. This list merely avoids typing a label that nothing
 * could parse back afterward: the server remains the sole judge
 * (`snmpv3.ParseTag`).
 */
const V3_AUTH = ["md5", "sha", "sha224", "sha256", "sha384", "sha512"];
const V3_PRIV = ["aes256", "aes192", "aes128", "des", "nopriv"];

/** What gender changes for the operator, at a glance. */
function KindBadge({ kind }: { kind: string }) {
  const { t } = useTranslation();
  if (kind === KIND_V2C)
    return (
      <span className="badge warn">
        {t("siteCredsPage.snmpV2Read")}
      </span>
    );
  if (kind === KIND_V3)
    return (
      <span className="badge" style={{ color: "var(--ok)", borderColor: "var(--ok)" }}>
        SNMPv3
      </span>
    );
  if (kind === KIND_ADMIN) return <span className="badge admin">{t("siteCredsPage.adminBadge")}</span>;
  // Inherited or unknown kind: show it as-is rather than disguising it as
  // something known — the server will refuse it on save.
  return <span className="badge">{kind}</span>;
}

/** Readable name of an identifier: a community has no user. */
function credWho(c: Cred, t: (k: string) => string): string {
  return c.username || (c.kind === KIND_V2C ? t("siteCredsPage.snmpV2cCommunity") : c.kind);
}

/**
 * Identifier MAINTAINED BY MESHCRAFT: its scope targets a reserved namespace.
 *
 * Two families today, and the server refuses edits to both
 * (`checkCredScope`): the SNMPv3 identity, whose key derives from a pair of
 * algorithms, and the administration password of a platform password
 * policy. The screen should say so BEFORE the click — offering "Edit" or
 * "Delete" only to have it refused would send the user hunting for a
 * missing permission where there is none — there's simply no action to
 * take. And removing this from a policy would leave a whole product range
 * with no password that can be set.
 */
function isManaged(c: Cred): boolean {
  return isReservedTagName(c.tag ?? "");
}

/** Technical password policy label: `pwpolicy-len<N>`. */
function isPwPolicy(c: Cred): boolean {
  return (c.tag ?? "").startsWith("pwpolicy-");
}

export default function SiteCredentialsPage() {
  const { t, i18n } = useTranslation();
  const { site } = useSite();
  const siteId = useSiteId();
  const siteSlug = site?.slug ?? "";
  const qc = useQueryClient();
  const [toast, showToast] = useToast();
  const canEdit = useSiteRole(siteId) === "ing";
  const [editing, setEditing] = useState<Cred | "new" | null>(null);
  // `secret2` is only present for a v3 identity: the server omits the key
  // rather than returning an empty string, otherwise the window would show
  // an empty "passphrase" field where there simply isn't one.
  const [revealed, setRevealed] = useState<
    { id?: string; username?: string; secret: string; secret2?: string } | null>(null);
  const [rotating, setRotating] = useState<Cred | null>(null);
  // INSTANCE credentials are listed here too, and that is not a convenience.
  // They are what the serial soft-reset actually places on a device — the
  // browser has to type a secret on a UART the controller cannot open — so any
  // equipment provisioned over the console but not yet adopted is reachable
  // ONLY with one of these. Before they appeared on this page, the sole way to
  // read one again was to re-run the serial wizard.
  const isGlobalAdmin = !!useAuth().user?.is_global_admin;

  const { data, error } = useQuery({
    queryKey: ["site-credentials", siteId],
    queryFn: () =>
      get<{ credentials: Cred[]; key_external: boolean }>(
        `/api/sites/${siteId}/credentials`),
    enabled: !!siteId,
  });

  const { data: instance } = useQuery({
    queryKey: ["known-credentials"],
    queryFn: () => get<{ credentials: KnownCredential[] }>("/api/discovery/credentials"),
    enabled: isGlobalAdmin,
  });

  // Same modal, same audit sentence as the site reveal below: two reveal paths
  // that journalled differently would make the trail depend on which page
  // someone opened.
  const revealInstance = useMutation({
    mutationFn: (c: KnownCredential) =>
      post<{ id: string; name: string; username: string; secret: string }>(
        `/api/discovery/credentials/${c.id}/reveal`),
    onSuccess: (r) => setRevealed({ ...r, username: r.username || r.name }),
  });

  const reveal = useMutation({
    mutationFn: (c: Cred) =>
      post<{ id: string; username: string; secret: string; secret2?: string }>(
        `/api/sites/${siteId}/credentials/${c.id}/reveal`),
    // The server does not return a user for a community: without fallback,
    // the window would be titled “Password for “”” and we would no longer know
    // which secret has just been put on the screen — nor which one is being pasted.
    onSuccess: (r, c) => setRevealed({ ...r, username: r.username || credWho(c, t) }),
  });
  const mode = useMutation({
    mutationFn: (v: { c: Cred; per: boolean }) =>
      put(`/api/sites/${siteId}/credentials/${v.c.id}/mode`, { per_device: v.per }),
    onSuccess: (_d, v) => {
      qc.invalidateQueries({ queryKey: ["site-credentials", siteId] });
      showToast(v.per
        ? t("siteCredsPage.perDeviceToast")
        : t("siteCredsPage.sharedSecretToast"));
    },
  });
  const remove = useMutation({
    mutationFn: (c: Cred) => del(`/api/sites/${siteId}/credentials/${c.id}`),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["site-credentials", siteId] });
      showToast(t("siteCredsPage.deletedToast"));
    },
  });

  if (!siteId) return <div className="empty">{t("siteCredsPage.chooseSite")}</div>;

  const creds = data?.credentials ?? [];
  // "Default identifier" means: the one that equipment outside any group
  // and without a tag will receive for ADMINISTRATION. A site's SNMP
  // community does not fill this gap — counting it as if it did would
  // silence the warning while leaving the fleet without a password.
  const hasDefault = creds.some(
    (c) => c.kind === KIND_ADMIN && c.group_name === "" && !c.tag);

  return (
    <>
      <div className="toolbar">
        <h1>
          {t("siteCredsPage.title")}
          {/* Full vault doctrine tucked behind a single help tooltip: scope,
              password policies, SNMP. Three blocks that the screen used to
              show permanently, even though they're read once — and they
              buried the page's only real warning: a missing default
              credential. */}
          <HelpTip label={t("siteCredsPage.helpLabel")}>
            <p>{t("siteCredsPage.help1")}</p>
            <p>{t("siteCredsPage.help2")}</p>
            <p>{t("siteCredsPage.help3")}</p>
            <p>{t("siteCredsPage.help4")}</p>
            <p>{t("siteCredsPage.help5")}</p>
          </HelpTip>
          <span className="sub">{t("siteCredsPage.subtitle")}</span>
        </h1>
        {canEdit && (
          <ActionButton kind="add" onClick={() => setEditing("new")}>
            {t("siteCredsPage.addCredential")}
          </ActionButton>
        )}
      </div>

      {data && !data.key_external && (
        <p className="error">{t("siteCredsPage.keyInDbError")}</p>
      )}

      <ErrorMsg error={error} />
      <div className="card">
        {creds.length === 0 ? (
          <div className="empty">
            {t("siteCredsPage.noCredentials")}
          </div>
        ) : (
          <table className="creds-table">
            <thead>
              <tr>
                <th className="creds-col-scope">{t("siteCredsPage.scopeCol")}</th>
                <th className="creds-col-user">{t("siteCredsPage.usernameCol")}</th>
                <th className="creds-col-kind">{t("siteCredsPage.kindCol")}</th>
                <th className="creds-col-rotation">{t("siteCredsPage.lastRotationCol")}</th>
                <th className="creds-col-actions"></th>
              </tr>
            </thead>
            <tbody>
              {creds.map((c) => (
                <tr key={c.id}>
                  <td className="creds-col-scope">
                    {c.tag ? (
                      <>{t("siteCredsPage.tagPrefix")} <b className="mono">#{c.tag}</b></>
                    ) : c.group_name === "" ? (
                      <b>{t("siteCredsPage.wholeSite")}</b>
                    ) : (
                      <>{t("siteCredsPage.groupPrefix")} <b>{c.group_name}</b></>
                    )}
                    {c.kind === KIND_V3 && (
                      <div className="note">
                        {t("siteCredsPage.v3FamilyNote")}
                      </div>
                    )}
                    {isPwPolicy(c) && (
                      <div className="note">
                        {t("siteCredsPage.pwPolicyNote")}
                      </div>
                    )}
                    {c.note && <div className="note">{c.note}</div>}
                    <label className="row note" style={{ gap: 6, marginTop: 4 }}>
                      <input
                        type="checkbox"
                        checked={c.per_device}
                        // On an identifier held by meshcraft, this mode is
                        // not a user-facing setting: switching to "one
                        // secret per device" would pull this identifier out
                        // of the mechanism that maintains it, and the next
                        // provisioning run would just generate another one
                        // in its place.
                        disabled={!canEdit || mode.isPending || isManaged(c)}
                        onChange={(e) => mode.mutate({ c, per: e.target.checked })}
                      />
                      {t("siteCredsPage.perDeviceSecret")}
                    </label>
                  </td>
                  <td className="mono creds-col-user">
                    {c.username || <span className="note">{t("siteCredsPage.noUsername")}</span>}
                  </td>
                  <td className="creds-col-kind">
                    <span className="creds-kind-row">
                      <KindBadge kind={c.kind} />
                      {c.kind === KIND_V2C && (
                        <HelpTip label={t("siteCredsPage.v2cReadHelpLabel")}>
                          <p>{t("siteCredsPage.v2cReadNote")}</p>
                        </HelpTip>
                      )}
                    </span>
                  </td>
                  <td className="note creds-col-rotation">
                    {new Date(c.rotated_at).toLocaleDateString()}
                    {!c.has_secret && (
                      <span style={{ color: "var(--warn)" }}>{t("siteCredsPage.noSecret")}</span>
                    )}
                    {c.kind === KIND_V3 && c.has_secret && !c.has_secret2 && (
                      // A v3 identity without a passphrase authenticates but
                      // encrypts nothing. Nothing else on the screen would
                      // show this: monitoring would come back normally, in
                      // the clear.
                      <div style={{ color: "var(--warn)" }}>
                        {t("siteCredsPage.noPrivPhrase")}
                      </div>
                    )}
                    <Generations
                      cred={c}
                      siteId={siteId}
                      canEdit={canEdit}
                      onRevealed={setRevealed}
                    />
                  </td>
                  <td className="cred-actions-cell creds-col-actions">
                    {canEdit && (
                      <div className="cred-actions">
                        {/* ROTATION stays available, even on credentials held by
                            meshcraft: a whole product line's secret must be
                            rotatable. The server then constrains the new secret
                            to whatever the tag names — an out-of-bounds password
                            would be rejected by the connector before it's ever
                            sent, rather than producing a credential that looks
                            active but is inert. */}
                        <ActionButton kind="refresh" className="ghost" onClick={() => setRotating(c)}>
                          {t("siteCredsPage.newGeneration")}
                        </ActionButton>
                        <ActionButton kind="download" className="ghost" disabled={reveal.isPending}
                          onClick={() => reveal.mutate(c)}>
                          {t("siteCredsPage.reveal")}
                        </ActionButton>
                        {isManaged(c) ? (
                          <QuickTip text={t("siteCredsPage.managedScopeTitle")} className="quicktip-inline">
                            <span className="badge">
                              🔒 {t("siteCredsPage.managedByMeshcraft")}
                            </span>
                          </QuickTip>
                        ) : (
                          <>
                            <ActionButton kind="edit" className="ghost" onClick={() => setEditing(c)}>
                              {t("siteCredsPage.editCredential")}
                            </ActionButton>
                            <DeleteButton onConfirm={() => remove.mutate(c)} />
                          </>
                        )}
                      </div>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
        <ErrorMsg error={reveal.error} />
        {/* The only text this card keeps: a gap that leaves part of the fleet
            without an applicable password. It used to be buried among three
            explanation blocks. */}
        {!hasDefault && creds.length > 0 && (
          <p className="note" style={{ marginTop: 12, color: "var(--warn)" }}>
            {t("siteCredsPage.noDefaultWarning")}
          </p>
        )}
      </div>

      {isGlobalAdmin && (instance?.credentials?.length ?? 0) > 0 && (
        <>
          <div className="row" style={{ justifyContent: "space-between", marginTop: 20 }}>
            <h2 style={{ margin: 0, fontSize: 15 }}>{t("siteCredsPage.instanceTitle")}</h2>
          </div>
          <p className="note" style={{ marginTop: 4 }}>
            {t("siteCredsPage.instanceIntro")}
          </p>
          <div className="card">
            <table className="creds-table">
              <thead>
                <tr>
                  <th className="creds-col-scope">{t("siteCredsPage.instanceNameCol")}</th>
                  <th className="creds-col-user">{t("siteCredsPage.usernameCol")}</th>
                  <th className="creds-col-kind">{t("siteCredsPage.kindCol")}</th>
                  <th className="creds-col-actions"></th>
                </tr>
              </thead>
              <tbody>
                {(instance?.credentials ?? []).map((c) => (
                  <tr key={c.id}>
                    <td className="creds-col-scope">
                      <b>{c.name}</b>
                      {!c.enabled && (
                        // Disabled means "stop TRYING it during discovery", not
                        // "nobody needs to read it": it is still the password on
                        // every device it was ever applied to.
                        <div className="note">{t("siteCredsPage.instanceDisabled")}</div>
                      )}
                    </td>
                    <td className="creds-col-user"><span className="mono">{c.username}</span></td>
                    <td className="creds-col-kind"><span className="badge">{c.kind}</span></td>
                    <td className="creds-col-actions">
                      <div className="row" style={{ gap: 6, justifyContent: "flex-end" }}>
                        <ActionButton
                          kind="download"
                          className="ghost"
                          disabled={revealInstance.isPending || !c.has_secret}
                          onClick={() => revealInstance.mutate(c)}
                        >
                          {t("siteCredsPage.reveal")}
                        </ActionButton>
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
            <ErrorMsg error={revealInstance.error} />
          </div>
        </>
      )}

      {/* "Secret" rather than "password": it may be a community string or a v3
          passphrase, and half-correct naming on a vault screen leads you to
          paste the wrong value into the wrong field. */}
      {revealed && (
        <Modal
          title={t("siteCredsPage.revealTitle", { username: revealed.username })}
          onClose={() => setRevealed(null)}
          help={
            revealed.secret2 ? (
              <p>{t("siteCredsPage.v3RevealHelpFull")}</p>
            ) : undefined
          }
        >
          <p className="note" style={{ marginTop: 0 }}>
            {t("siteCredsPage.revealLogged")}
          </p>
          {/* Two phrases: each NAMED and copyable separately. Stacking them without
              labels would paste the encryption phrase into the auth field — and a v3
              agent that cannot decrypt does not error, it stays silent. */}
          {revealed.secret2 ? (
            <>
              <div className="note" style={{ marginBottom: 4 }}>
                <b>{t("siteCredsPage.authPhrase")}</b>
              </div>
              <pre className="code" style={{ fontSize: 15 }}>{revealed.secret}</pre>
              <div className="note" style={{ marginBottom: 4 }}>
                <b>{t("siteCredsPage.privPhrase")}</b>
              </div>
              <pre className="code" style={{ fontSize: 15 }}>{revealed.secret2}</pre>
              <div className="actions">
                <ActionButton kind="copy" className="secondary"
                  onClick={() => navigator.clipboard.writeText(revealed.secret)}>
                  {t("siteCredsPage.copyAuth")}
                </ActionButton>
                <ActionButton kind="copy" className="secondary"
                  onClick={() => navigator.clipboard.writeText(revealed.secret2 ?? "")}>
                  {t("siteCredsPage.copyPriv")}
                </ActionButton>
                <ActionButton kind="close" onClick={() => setRevealed(null)}>
                  {t("common.closeAction")}
                </ActionButton>
              </div>
            </>
          ) : (
            <>
              <pre className="code" style={{ fontSize: 15 }}>{revealed.secret}</pre>
              <div className="actions">
                <ActionButton kind="copy" className="secondary"
                  onClick={() => navigator.clipboard.writeText(revealed.secret)}>
                  {t("siteCredsPage.copy")}
                </ActionButton>
                <ActionButton kind="close" onClick={() => setRevealed(null)}>
                  {t("common.closeAction")}
                </ActionButton>
              </div>
            </>
          )}
        </Modal>
      )}

      {rotating && (
        <RotateModal
          siteId={siteId}
          cred={rotating}
          onClose={() => setRotating(null)}
          onDone={() => {
            setRotating(null);
            qc.invalidateQueries({ queryKey: ["site-credentials", siteId] });
            showToast(t("siteCredsPage.rotationCreated"));
          }}
        />
      )}
      {editing && (
        <CredentialModal
          siteId={siteId}
          cred={editing === "new" ? null : editing}
          onClose={() => setEditing(null)}
          onDone={() => {
            setEditing(null);
            qc.invalidateQueries({ queryKey: ["site-credentials", siteId] });
            showToast(t("siteCredsPage.savedToast"));
          }}
        />
      )}
      {toast}
    </>
  );
}

function CredentialModal({
  siteId,
  cred,
  onClose,
  onDone,
}: {
  siteId: string;
  cred: Cred | null;
  onClose: () => void;
  onDone: () => void;
}) {
  const { t } = useTranslation();
  const [group, setGroup] = useState(cred?.group_name ?? "");
  const [kind, setKind] = useState(cred?.kind ?? KIND_ADMIN);
  const [tag, setTag] = useState(cred?.tag ?? "");
  const [username, setUsername] = useState(cred?.username ?? t("siteCredsPage.adminBadge"));
  const [secret, setSecret] = useState("");
  // Second passphrase, specific to v3. Always empty when opened, like the
  // first: this page never redisplays a stored secret.
  const [secret2, setSecret2] = useState("");
  const [note, setNote] = useState(cred?.note ?? "");

  // The algorithm pair is composed automatically, not typed: a v3 label
  // entered by hand could be mistyped by anyone, and provisioning would
  // then never find the secret stored underneath it.
  const couple = (cred?.tag ?? "").startsWith("snmpv3-")
    ? (cred?.tag ?? "").slice("snmpv3-".length).split("-")
    : [];
  const [auth, setAuth] = useState(couple[0] ?? "sha256");
  const [priv, setPriv] = useState(couple[1] ?? "aes128");

  const isV2c = kind === KIND_V2C;
  const isV3 = kind === KIND_V3;
  // The scope actually sent: composed for v3, empty for v2c, and for
  // admin whatever the screen displays. Resubmitting it is essential —
  // (site, group, tag, kind) is the KEY to the identifier: an omitted field
  // would not modify the row being edited, it would create another one.
  const scopeTag = isV3 ? `snmpv3-${auth}-${priv}` : isV2c ? "" : tag.trim();
  const scopeGroup = isV2c || isV3 ? "" : group.trim();

  const save = useMutation({
    mutationFn: () =>
      put(`/api/sites/${siteId}/credentials`, {
        group_name: scopeGroup, tag: scopeTag, kind,
        username: isV2c ? "" : username.trim(),
        priority: cred?.priority ?? 0,
        // `secret2` is only sent for v3: the server rejects a second
        // passphrase on other kinds, and a field left filled in after
        // switching kind would make the save fail without the operator
        // seeing why.
        secret, secret2: isV3 ? secret2 : "", note,
      }),
    onSuccess: onDone,
  });

  // RESERVED namespace, stated before the click too. The server is the
  // sole judge (`pwpolicy.IsTechnicalTag`, `snmpv3.ParseTag`) and will
  // refuse it anyway: what matters here is explaining WHY, rather than
  // letting people read "refused" on an ordinary-looking label.
  const reservedScope =
    kind === KIND_ADMIN && isReservedTagName(scopeTag)
      ? t("siteCredsPage.reservedScopeErrorDetail", { tag: scopeTag })
      : "";

  // What the server will refuse is stated BEFORE the click (checkCredSecrets
  // on the Go side). Two password fields with no explanation invite you to
  // enter the same value in both: rejecting it on click, without saying
  // why, would just have the user retry the exact same thing.
  const v3Pair = isV3
    ? secret2 && secret === secret2
      ? t("siteCredsPage.phrasesMustDiffer")
      : (secret === "") !== (secret2 === "")
        ? t("siteCredsPage.v3FillBothOrNone")
        : ""
    : "";

  // Changing kind changes the expected account on the equipment:
  // re-offering "admin" for an SNMPv3 identity would create a v3 user with
  // the same name as the web account — two distinct things that cannot be
  // revoked together. Only a value still at its default gets replaced:
  // never a value the operator typed in.
  function defaultUser(k: string) {
    return k === KIND_V3 ? "mcadmin" : t("siteCredsPage.adminBadge");
  }
  function chooseKind(k: string) {
    if (!cred && username === defaultUser(kind)) setUsername(defaultUser(k));
    setKind(k);
  }

  // "v3 passphrase" didn't say which one. With two fields, a label that
  // doesn't announce its role is worse than useless: the two passphrases
  // are not interchangeable — swapping them makes the equipment go silent
  // rather than raise an error.
  const secretLabel = isV2c
    ? t("siteCredsPage.snmpV2cCommunity")
    : isV3
      ? t("siteCredsPage.authPhrase")
      : t("siteCredsPage.passwordLabel");

  return (
    <Modal title={cred ? t("siteCredsPage.editCredentialTitle") : t("siteCredsPage.newCredentialTitle")} onClose={onClose}>
      {/* Kind first: it decides the scopes below, and is frozen after creation
          since it is part of the key. */}
      <Field label={t("siteCredsPage.kindCol")}>
        <select value={kind} onChange={(e) => chooseKind(e.target.value)} disabled={!!cred}>
          <option value={KIND_ADMIN}>{t("siteCredsPage.kindAdmin")}</option>
          <option value={KIND_V2C}>{t("siteCredsPage.kindV2c")}</option>
          <option value={KIND_V3}>{t("siteCredsPage.kindV3")}</option>
          {cred && ![KIND_ADMIN, KIND_V2C, KIND_V3].includes(kind) && (
            <option value={kind}>{t("siteCredsPage.kindInherited", { kind })}</option>
          )}
        </select>
      </Field>

      {kind === KIND_ADMIN && (
        <>
          <div className="row">
            <div style={{ flex: 1 }}>
              <Field
                label={t("siteCredsPage.tagEmptyWholeSite")}
                help={<p>{t("siteCredsPage.scopeTagHelp")}</p>}
              >
                <input value={tag} onChange={(e) => setTag(e.target.value)} className="mono"
                  placeholder={t("siteCredsPage.tagEmptyWholeSite")} disabled={!!cred || !!group.trim()} />
              </Field>
            </div>
            <div style={{ flex: 1 }}>
              <Field label={t("siteCredsPage.orGroupLegacy")}>
                <input value={group} onChange={(e) => setGroup(e.target.value)}
                  placeholder="ex. instant-on-1930" disabled={!!cred || !!tag.trim()} />
              </Field>
            </div>
          </div>
          {/* Label goes IN FRONT of the group, and it is not cosmetic: a device has
              only one group, and no screen assigns it since groups gave way to switch
              profiles. A credential targeting a group therefore applies only to devices
              from a site configuration import. The field remains because those sites
              exist. */}
        </>
      )}


      {isV3 && (
        <>
          <div className="row">
            <div style={{ width: 160 }}>
              <Field label={t("siteCredsPage.authAlgo")}>
                <select value={auth} onChange={(e) => setAuth(e.target.value)} disabled={!!cred}>
                  {V3_AUTH.map((a) => <option key={a} value={a}>{a}</option>)}
                </select>
              </Field>
            </div>
            <div style={{ width: 160 }}>
              <Field label={t("siteCredsPage.privAlgo")}>
                <select value={priv} onChange={(e) => setPriv(e.target.value)} disabled={!!cred}>
                  {V3_PRIV.map((p) => (
                    <option key={p} value={p}>{p === "nopriv" ? t("siteCredsPage.nopriv") : p}</option>
                  ))}
                </select>
              </Field>
            </div>
            <div style={{ flex: 1 }}>
              <Field
                label={t("siteCredsPage.targetTag")}
                help={<p>{t("siteCredsPage.v3TargetTagHelp")}</p>}
              >
                <input value={scopeTag} className="mono" readOnly />
              </Field>
            </div>
          </div>
          {/* REST warning: describes what this choice will produce on the network,
              not how SNMPv3 works. */}
          {priv === "nopriv" && (
            <p className="note" style={{ color: "var(--warn)" }}>
              {t("siteCredsPage.noPrivWarning")}
            </p>
          )}
        </>
      )}

      {!isV2c && (
        <Field label={isV3 ? t("siteCredsPage.snmpV3User") : t("siteCredsPage.usernameCol")}>
          <input value={username} onChange={(e) => setUsername(e.target.value)} className="mono" />
        </Field>
      )}

      <Field label={cred ? `${secretLabel}${t("siteCredsPage.secretNewUnchanged")}` : secretLabel}>
        <input type="password" value={secret} onChange={(e) => setSecret(e.target.value)}
          className="mono" autoComplete="new-password" />
      </Field>
      {isV3 && (
        <Field label={cred ? t("siteCredsPage.privNewUnchanged") : t("siteCredsPage.privPhrase")}>
          <input type="password" value={secret2} onChange={(e) => setSecret2(e.target.value)}
            className="mono" autoComplete="new-password" />
        </Field>
      )}
      <Field label={t("siteCredsPage.noteField")}>
        <input value={note} onChange={(e) => setNote(e.target.value)}
          placeholder={t("siteCredsPage.noteMaxLenPlaceholder")} />
      </Field>
      <p className="note">
        {t("siteCredsPage.secretNeverReread")}
        {isV3 && (
          <>
            {" "}{t("siteCredsPage.v3PairTogether")}
            <HelpTip label={t("siteCredsPage.v3PairHelpLabel")}>
              <p>{t("siteCredsPage.v3RevealHelp")}</p>
              <p>{t("siteCredsPage.v3PairTogether")}</p>
            </HelpTip>
          </>
        )}
      </p>
      {v3Pair && <p className="error">{v3Pair}</p>}
      {reservedScope && <p className="error">{reservedScope}</p>}
      <ErrorMsg error={save.error} />
      <div className="actions">
        <ActionButton kind="cancel" className="secondary" onClick={onClose}>
          {t("common.cancel")}
        </ActionButton>
        <ActionButton
          kind="save"
          disabled={save.isPending || (!isV2c && !username.trim()) ||
            (!cred && !secret) || (!cred && isV3 && !secret2) || !!v3Pair ||
            !!reservedScope}
          onClick={() => save.mutate()}>
          {t("common.save")}
        </ActionButton>
      </div>
    </Modal>
  );
}

/**
 * History of generations of a secret.
 *
 * What this display should make obvious: a "retiring" generation is not
 * dead. As long as some equipment still carries it, it is the only
 * password that opens its door — and that's why we can reveal it
 * separately.
 */
function Generations({
  cred,
  siteId,
  canEdit,
  onRevealed,
}: {
  cred: Cred;
  siteId: string;
  canEdit: boolean;
  onRevealed: (r: { secret: string; username?: string }) => void;
}) {
  const { t } = useTranslation();
  const gens = cred.generations ?? [];
  const reveal = useMutation({
    mutationFn: (g: number) =>
      post<{ generation: number; secret: string }>(
        `/api/sites/${siteId}/credentials/${cred.id}/generations/${g}/reveal`),
    onSuccess: (r) => onRevealed({ secret: r.secret, username: credWho(cred, t) }),
  });
  if (gens.length <= 1) return null;
  return (
    <div style={{ marginTop: 6 }}>
      {gens.map((g) => (
        <div key={g.generation} className="note" style={{ display: "flex", gap: 6 }}>
          <span className="mono">{t("siteCredsPage.genLabel", { n: g.generation })}</span>
          <span style={{ color: g.state === "active" ? "var(--ok)" : "var(--warn)" }}>
            {g.state === "active" ? t("siteCredsPage.genCurrent") : g.state === "retiring" ? t("siteCredsPage.genRetiring") : g.state}
          </span>
          <span>
            {g.installed_on > 0
              ? t("siteCredsPage.genDevices", { count: g.installed_on })
              : g.state === "retiring" ? t("siteCredsPage.genPurgeable") : ""}
          </span>
          {canEdit && (
            <ActionButton kind="download" className="ghost" style={{ padding: "0 4px", fontSize: 11 }}
              onClick={() => reveal.mutate(g.generation)}>
              {t("siteCredsPage.revealGen")}
            </ActionButton>
          )}
        </div>
      ))}
    </div>
  );
}

function RotateModal({
  siteId,
  cred,
  onClose,
  onDone,
}: {
  siteId: string;
  cred: Cred;
  onClose: () => void;
  onDone: () => void;
}) {
  const { t } = useTranslation();
  const [secret, setSecret] = useState("");
  const [secret2, setSecret2] = useState("");
  const isV3 = cred.kind === KIND_V3;
  const rotate = useMutation({
    mutationFn: () =>
      // The two passphrases of a v3 identity travel TOGETHER, in the same
      // generation. Renewing only one would leave the other key derived
      // from the old passphrase, and nothing would indicate which pair is
      // actually in place.
      post(`/api/sites/${siteId}/credentials/${cred.id}/rotate`,
        { secret, secret2: isV3 ? secret2 : "" }),
    onSuccess: onDone,
  });
  const samePair = isV3 && !!secret2 && secret === secret2;
  return (
    <Modal
      title={t("siteCredsPage.rotateTitle", { who: credWho(cred, t) })}
      onClose={onClose}
      help={isV3 ? <p>{t("siteCredsPage.v3RevealHelp")}</p> : undefined}
    >
      <p className="note" style={{ marginTop: 0 }}>
        {t("siteCredsPage.rotateNote")}
        {cred.kind === KIND_V2C && (
          <> {t("siteCredsPage.v2cRotateNote")}</>
        )}
      </p>
      <Field label={isV3
        ? t("siteCredsPage.newAuthPhrase")
        : cred.kind === KIND_ADMIN ? t("siteCredsPage.newPassword") : t("siteCredsPage.newSecret")}>
        <input type="password" value={secret} onChange={(e) => setSecret(e.target.value)}
          className="mono" autoComplete="new-password" autoFocus />
      </Field>
      {isV3 && (
        <>
          <Field label={t("siteCredsPage.newPrivPhrase")}>
            <input type="password" value={secret2} onChange={(e) => setSecret2(e.target.value)}
              className="mono" autoComplete="new-password" />
          </Field>
          {samePair && <p className="error">{t("siteCredsPage.phrasesMustDiffer")}</p>}
        </>
      )}
      <p className="note">{t("siteCredsPage.rotateDeployNote")}</p>
      <ErrorMsg error={rotate.error} />
      <div className="actions">
        <ActionButton kind="cancel" className="secondary" onClick={onClose}>
          {t("common.cancel")}
        </ActionButton>
        <ActionButton kind="create" disabled={!secret || (isV3 && !secret2) || samePair || rotate.isPending}
          onClick={() => rotate.mutate()}>
          {t("siteCredsPage.createGeneration")}
        </ActionButton>
      </div>
    </Modal>
  );
}

