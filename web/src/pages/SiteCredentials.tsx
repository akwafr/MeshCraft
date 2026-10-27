import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { viewerDate } from "../ui/clock";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { useSite, useSiteId } from "../useSite";
import { del, get, post, put, type KnownCredential, type SiteCredential } from "../api";
import { useAuth, useSiteRole } from "../auth";
import {
  ActionButton,
  ErrorMsg,
  Field,
  HelpTip,
  Modal,
  QuickTip,
  isReservedTagName,
  useToast,
} from "../components";
import { IconAction, IconDeleteButton } from "../ui/IconAction";
import CredentialDeploy from "./CredentialDeploy";
import { deployableKind } from "./credDeploy";

/**
 * {t("siteCredsPage.title")}: what meshcraft installs on the equipment, and
 * what an administrator uses to connect to it directly.
 *
 * The list never displays a secret — only its presence and rotation date.
 * Revealing it is a distinct action, reserved for the site-admin, and
 * logged in the audit trail.
 *
 * Three types coexist (docs/en/03-Security/SNMP identity §2), and the screen
 * must make them impossible to confuse: an administration password opens
 * the door to equipment, a v2c community only reads, and a v3 identity is
 * only valid for one cryptographic family. Displaying them all "the same
 * way" would risk revealing one while believing you're reading the other.
 */

/** Kinds, such as `site_credentials.kind` carry them server-side. */
/** The account name a new admin credential proposes. Data, never a label. */
const DEFAULT_ADMIN_USERNAME = "admin";

const KIND_ADMIN = "admin";
const KIND_V2C = "snmp_v2c";
const KIND_V3 = "snmp_v3";
/**
 * STREAMING TELEMETRY (`internal/api/gnmicred.go`). The fourth kind, and the
 * only one on this page that is neither pushed onto equipment nor read back
 * from it: it is an account that ALREADY EXISTS on the switch, which meshcraft
 * merely uses to open a gNMI subscription.
 *
 * That difference is why it earns its own kind rather than riding the fleet
 * admin password. A v2c community, leaked, reads a MIB; this one authenticates
 * against the switch's ordinary AAA stack and therefore CONFIGURES IT. Handing
 * that to every proxy that watches an Arista, as a side effect of a monitoring
 * change, is the trade the separate kind refuses.
 *
 * Until this screen offered it, the credential could only be created through
 * the raw API — and `gnmiWatchesFor` skips, silently and per device, any device
 * with no credential. A lab Arista streamed perfectly while the product
 * collected nothing, with nothing on any screen to say why.
 */
const KIND_GNMI = "gnmi";

/**
 * Transport posture of a gNMI subscription (`site_credentials.transport_tls`,
 * migration 0039). Three values, never two booleans: "plaintext AND
 * skip-verify" means nothing, and a pair of checkboxes would let it be typed.
 */
const TLS_VERIFY = "verify";
const TLS_SKIP_VERIFY = "skip-verify";
const TLS_PLAINTEXT = "plaintext";
const TLS_MODES = [TLS_VERIFY, TLS_SKIP_VERIFY, TLS_PLAINTEXT];

/**
 * `tag` and `priority` travel alongside the identifier in the API payload:
 * a v3 identity is identified ONLY by its label, and resubmitting it on
 * save is essential — an omitted `tag` would target the "site" scope and
 * create a second identifier instead of modifying the one being edited.
 *
 * `transport_tls` and `ca_pem` are here for a different reason: they are
 * returned by the list endpoint, unlike every secret on this page, because a
 * transport mode and a CA are NOT secrets. That is what lets the edit window
 * prefill them — and it has to, since the server REPLACES `ca_pem` with
 * whatever a save carries. Editing a note without resending the CA would drop
 * it, and verification would silently fall back to a system pool that holds no
 * switch's issuer.
 */
type Cred = SiteCredential & {
  tag?: string;
  priority?: number;
  transport_tls?: string;
  ca_pem?: string;
};

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
  if (kind === KIND_GNMI)
    return (
      <span className="badge" style={{ color: "var(--ok)", borderColor: "var(--ok)" }}>
        {t("siteCredsPage.gnmiBadge")}
      </span>
    );
  // Inherited or unknown kind: show it as-is rather than disguising it as
  // something known — the server will refuse it on save.
  return <span className="badge">{kind}</span>;
}

/**
 * The transport posture, ON THE LIST, and only when it is worth a second look.
 *
 * `verify` says nothing here on purpose: it is the default and the safe state,
 * and a badge on every row would train the eye to skip the column — which is
 * exactly where the one row that matters would then hide.
 *
 * `plaintext` is a WARNING and not a label. Somebody chose it, possibly months
 * ago, possibly on a credential whose scope has since grown to cover devices
 * they never had in mind. Nothing else on any screen states it: the
 * subscription works, the graphs fill in, and the password is on the wire.
 */
function TransportBadge({ cred }: { cred: Cred }) {
  const { t } = useTranslation();
  if (cred.kind !== KIND_GNMI) return null;
  const mode = cred.transport_tls || TLS_VERIFY;
  if (mode === TLS_PLAINTEXT)
    return (
      <QuickTip text={t("siteCredsPage.gnmiPlaintextBadgeTip")} className="quicktip-inline">
        <span className="badge warn">⚠ {t("siteCredsPage.gnmiPlaintextBadge")}</span>
      </QuickTip>
    );
  if (mode === TLS_SKIP_VERIFY)
    return (
      <QuickTip text={t("siteCredsPage.gnmiSkipVerifyBadgeTip")} className="quicktip-inline">
        <span className="badge">{t("siteCredsPage.gnmiSkipVerifyBadge")}</span>
      </QuickTip>
    );
  return null;
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
  const role = useSiteRole(siteId);
  const canEdit = role === "ing";
  // WATCHING A DEPLOYMENT IS NOT EDITING ONE. `GET …/deployment` and
  // `GET …/deploy` are `operator`, the two that START and STOP a run are
  // `site_admin` — so an operator gets the screen and no buttons, which is the
  // difference between "who may look at where the fleet stands" and "who may
  // change the password of every switch in it".
  const canWatchDeploy = role !== "viewer";
  const [editing, setEditing] = useState<Cred | "new" | null>(null);
  // `secret2` is only present for a v3 identity: the server omits the key
  // rather than returning an empty string, otherwise the window would show
  // an empty "passphrase" field where there simply isn't one.
  const [revealed, setRevealed] = useState<
    { id?: string; username?: string; secret: string; secret2?: string } | null>(null);
  const [rotating, setRotating] = useState<Cred | null>(null);
  const [deploying, setDeploying] = useState<Cred | null>(null);
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
                      <TransportBadge cred={c} />
                      {c.kind === KIND_V2C && (
                        <HelpTip label={t("siteCredsPage.v2cReadHelpLabel")}>
                          <p>{t("siteCredsPage.v2cReadNote")}</p>
                        </HelpTip>
                      )}
                      {c.kind === KIND_GNMI && (
                        <HelpTip label={t("siteCredsPage.gnmiHelpLabel")}>
                          <p>{t("siteCredsPage.gnmiWhatItIs")}</p>
                          <p>{t("siteCredsPage.gnmiNotPushed")}</p>
                        </HelpTip>
                      )}
                    </span>
                  </td>
                  <td className="note creds-col-rotation">
                    {(viewerDate(c.rotated_at) ?? new Date(c.rotated_at)).toLocaleDateString()}
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
                    {/* UP TO FIVE LABELLED VERBS, STACKED VERTICALLY — which
                        is what "stack them so the other columns keep their
                        width" actually costs: the width comes back as five
                        lines of row height. As glyphs they fit on one or two
                        lines in a narrower cell, and the column stops having
                        to trade height for width at all. */}
                    <div className="icon-actions">
                      {/* WHERE THE ROTATION ACTUALLY REACHES THE FLEET. A new
                          generation changes the vault and nothing else; until
                          this screen existed there was no way to see which
                          switches had taken it, and no way to push it other than
                          one device at a time. Offered on `admin` credentials
                          only — see `deployableKind`: the runner resolves each
                          device's credential as kind `admin`, so a run started on
                          a community or a gNMI account would skip every device. */}
                      {canWatchDeploy && deployableKind(c.kind) && (
                        <IconAction
                          kind="upload"
                          label={t("siteCredsPage.deployment") as string}
                          onClick={() => setDeploying(c)}
                        />
                      )}
                      {canEdit && (
                        <>
                          {/* ROTATION stays available, even on credentials held by
                              meshcraft: a whole product line's secret must be
                              rotatable. The server then constrains the new secret
                              to whatever the tag names — an out-of-bounds password
                              would be rejected by the connector before it's ever
                              sent, rather than producing a credential that looks
                              active but is inert. */}
                          <IconAction
                            kind="refresh"
                            label={t("siteCredsPage.newGeneration") as string}
                            onClick={() => setRotating(c)}
                          />
                          <IconAction
                            kind="download"
                            label={t("siteCredsPage.reveal") as string}
                            disabled={reveal.isPending}
                            onClick={() => reveal.mutate(c)}
                          />
                          {!isManaged(c) && (
                            <>
                              <IconAction
                                kind="edit"
                                label={t("siteCredsPage.editCredential") as string}
                                onClick={() => setEditing(c)}
                              />
                              <IconDeleteButton onConfirm={() => remove.mutate(c)} />
                            </>
                          )}
                        </>
                      )}
                    </div>
                    {/* NOT AN ACTION — it says why edit and delete are absent.
                        A badge stating a fact never becomes a glyph (§3: an
                        observation is read every time). */}
                    {canEdit && isManaged(c) && (
                      <QuickTip text={t("siteCredsPage.managedScopeTitle")} className="quicktip-inline">
                        <span className="badge">
                          🔒 {t("siteCredsPage.managedByMeshcraft")}
                        </span>
                      </QuickTip>
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
      {deploying && (
        <CredentialDeploy
          siteId={siteId}
          credId={deploying.id}
          label={credWho(deploying, t)}
          perDevice={deploying.per_device}
          canDeploy={canEdit}
          onClose={() => {
            setDeploying(null);
            // The generations' `installed_on` counts are what a run moves, and
            // they are served by the credential LIST rather than by the
            // deployment view — leaving them stale would have the row under the
            // modal contradict the modal that just closed.
            qc.invalidateQueries({ queryKey: ["site-credentials", siteId] });
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
  // "admin" as a plain constant, NOT t("siteCredsPage.adminBadge").
  //
  // That badge is a DISPLAY label for the credential kind, and using it as a
  // data default meant a translated UI string became the account name written
  // onto switches. It happens to read "admin" in all eleven locales today, so
  // nothing has broken yet — but the first translator who renders that badge as
  // "administrateur" would have meshcraft creating French accounts on
  // customer hardware, and nothing in the type system would notice.
  const [username, setUsername] = useState(cred?.username ?? DEFAULT_ADMIN_USERNAME);
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

  // Transport posture. Prefilled from the row rather than reset to the default,
  // because these two are the only non-secret fields on this form and the
  // server REPLACES `ca_pem` with what a save carries: reopening a credential
  // on `verify` when it is stored as `skip-verify`, then saving a note, would
  // quietly break its subscription — and reopening with an empty CA would erase
  // the only setting on this page that actually resists someone on the path.
  const [tlsMode, setTlsMode] = useState(cred?.transport_tls || TLS_VERIFY);
  const [caPem, setCaPem] = useState(cred?.ca_pem ?? "");

  const isV2c = kind === KIND_V2C;
  const isV3 = kind === KIND_V3;
  const isGnmi = kind === KIND_GNMI;
  // The scope actually sent: composed for v3, empty for v2c, and for
  // admin and gnmi whatever the screen displays. Resubmitting it is essential —
  // (site, group, tag, kind) is the KEY to the identifier: an omitted field
  // would not modify the row being edited, it would create another one.
  const scopeTag = isV3 ? `snmpv3-${auth}-${priv}` : isV2c ? "" : tag.trim();
  const scopeGroup = isV2c || isV3 ? "" : group.trim();

  // A CA is meaningless without a TLS session, and the server refuses the pair
  // outright rather than storing a row that reads as verified-with-a-CA while
  // the wire carries the password in clear. The field is hidden under
  // `plaintext`, so drop what it holds too: keeping a hidden value and posting
  // it would produce a 400 pointing at a field the operator cannot see.
  const sentCaPem = isGnmi && tlsMode !== TLS_PLAINTEXT ? caPem.trim() : "";

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
        // Same rule for the transport pair, and the server enforces it: only a
        // `gnmi` credential has a transport to set. An SNMP community has no
        // TLS session and an admin password rides SSH, so a mode sent on those
        // would describe a protection that does not exist.
        transport_tls: isGnmi ? tlsMode : "", ca_pem: sentCaPem,
      }),
    onSuccess: onDone,
  });

  // RESERVED namespace, stated before the click too. The server is the
  // sole judge (`pwpolicy.IsTechnicalTag`, `snmpv3.ParseTag`) and will
  // refuse it anyway: what matters here is explaining WHY, rather than
  // letting people read "refused" on an ordinary-looking label.
  const reservedScope =
    (kind === KIND_ADMIN || isGnmi) && isReservedTagName(scopeTag)
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
  // `gnmi` gets NO default, and that is the point: this account is not created
  // by meshcraft, it already exists on the switch. Prefilling "admin" would
  // propose exactly the account this credential kind exists to keep off every
  // proxy, and it would be accepted without anyone deciding anything.
  function defaultUser(k: string) {
    if (k === KIND_GNMI) return "";
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
      : isGnmi
        ? t("siteCredsPage.gnmiPasswordLabel")
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
          <option value={KIND_GNMI}>{t("siteCredsPage.kindGnmi")}</option>
          {cred && ![KIND_ADMIN, KIND_V2C, KIND_V3, KIND_GNMI].includes(kind) && (
            <option value={kind}>{t("siteCredsPage.kindInherited", { kind })}</option>
          )}
        </select>
      </Field>

      {isGnmi && (
        // Said BEFORE the fields, because it changes what the two below mean.
        // Every other kind on this page is a secret meshcraft PUSHES onto
        // equipment; this one is an account that must already exist there. An
        // operator who invents a username here files a credential that looks
        // complete and subscribes to nothing.
        <p className="note" style={{ marginTop: 0 }}>
          {t("siteCredsPage.gnmiIntro")}
          <HelpTip label={t("siteCredsPage.gnmiHelpLabel")}>
            <p>{t("siteCredsPage.gnmiWhatItIs")}</p>
            <p>{t("siteCredsPage.gnmiNotPushed")}</p>
            <p>{t("siteCredsPage.gnmiLeastPrivilegeUnverified")}</p>
          </HelpTip>
        </p>
      )}

      {(kind === KIND_ADMIN || isGnmi) && (
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
        <Field
          label={isV3
            ? t("siteCredsPage.snmpV3User")
            : isGnmi
              ? t("siteCredsPage.gnmiUserLabel")
              : t("siteCredsPage.usernameCol")}
          help={isGnmi ? <p>{t("siteCredsPage.gnmiLeastPrivilegeUnverified")}</p> : undefined}
        >
          <input value={username} onChange={(e) => setUsername(e.target.value)} className="mono"
            placeholder={isGnmi ? t("siteCredsPage.gnmiUserPlaceholder") : undefined} />
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
      {isGnmi && (
        <>
          {/* THE TRANSPORT CHOICE SITS DIRECTLY UNDER THE PASSWORD IT EXPOSES,
              and it is a list of three exclusive options rather than a pair of
              checkboxes. Two booleans would let "in clear AND unverified" be
              expressed, which means nothing and would silently resolve to the
              worse of the two; migration 0039 makes that state inexpressible in
              the column, and this control has to match, or the screen would
              offer what the database refuses.

              Each option carries its CONSEQUENCE on the same line, not behind a
              tooltip. The one thing that must not happen is for somebody to pick
              `plaintext` because it is the option that makes a handshake error
              go away, and never learn what they traded for it. */}
          <div className="kicker" style={{ marginTop: 14 }}>
            {t("siteCredsPage.gnmiTransportTitle")}
          </div>
          <div className="choice-list">
            {TLS_MODES.map((m) => (
              <label key={m} className={tlsMode === m ? "choice active" : "choice"}>
                <input type="radio" checked={tlsMode === m} onChange={() => setTlsMode(m)} />
                <span>
                  <b>
                    {m === TLS_VERIFY
                      ? t("siteCredsPage.tlsVerify")
                      : m === TLS_SKIP_VERIFY
                        ? t("siteCredsPage.tlsSkipVerify")
                        : t("siteCredsPage.tlsPlaintext")}
                  </b>
                  <span
                    className="note"
                    style={m === TLS_PLAINTEXT ? { color: "var(--warn)" } : undefined}
                  >
                    {m === TLS_VERIFY
                      ? t("siteCredsPage.tlsVerifyNote")
                      : m === TLS_SKIP_VERIFY
                        ? t("siteCredsPage.tlsSkipVerifyNote")
                        : t("siteCredsPage.tlsPlaintextNote")}
                  </span>
                </span>
              </label>
            ))}
          </div>
          {/* Stated AGAIN, in full, once the choice is made — because the line
              inside a radio option is read while comparing three of them, and
              the one that matters is the one that survives the comparison. This
              paragraph names what leaks, where, and how far it reaches. */}
          {tlsMode === TLS_PLAINTEXT && (
            <p className="error" style={{ marginTop: 8 }}>
              {t("siteCredsPage.tlsPlaintextWarning")}
            </p>
          )}
          {tlsMode === TLS_SKIP_VERIFY && (
            <p className="note" style={{ color: "var(--warn)" }}>
              {t("siteCredsPage.tlsSkipVerifyWarning")}
            </p>
          )}
          {/* Hidden under `plaintext`: there is no certificate to check when
              there is no TLS session, and a CA field sitting there filled in
              would read as protection to whoever audits this page next. */}
          {tlsMode !== TLS_PLAINTEXT && (
            <Field
              label={t("siteCredsPage.caPemLabel")}
              help={<p>{t("siteCredsPage.caPemHelp")}</p>}
            >
              <textarea
                rows={5}
                value={caPem}
                onChange={(e) => setCaPem(e.target.value)}
                className="mono"
                placeholder={"-----BEGIN CERTIFICATE-----\n…\n-----END CERTIFICATE-----"}
              />
              {/* NOT A SECRET, and said ON THE PAGE rather than only behind the
                  label's help. Every other field on this form goes through the
                  vault and is never redisplayed; this one is stored in clear and
                  comes back on every list. Someone who assumed the page's rule
                  applied uniformly would be wrong — harmlessly here, since a CA
                  certificate is public by construction, but it is the assumption
                  that has to be corrected, before it is carried to a field where
                  being wrong costs something. */}
              <span className="note">{t("siteCredsPage.caPemNotSealed")}</span>
            </Field>
          )}
        </>
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
        {/* THE ONE KIND MESHCRAFT DOES NOT PUSH. Every other rotation on this
            page ends with the new secret being written onto equipment; this one
            records a password that must ALREADY have been changed on the switch,
            by hand or by whatever manages its AAA. Rotating here first would
            leave every subscription failing authentication — which surfaces as
            "the Aristas stopped streaming", days from the gesture that caused
            it. */}
        {cred.kind === KIND_GNMI && (
          <> {t("siteCredsPage.gnmiRotateNote")}</>
        )}
      </p>
      <Field label={isV3
        ? t("siteCredsPage.newAuthPhrase")
        : cred.kind === KIND_ADMIN || cred.kind === KIND_GNMI
          ? t("siteCredsPage.newPassword")
          : t("siteCredsPage.newSecret")}>
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

