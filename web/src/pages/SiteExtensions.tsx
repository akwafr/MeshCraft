// Site extensions: what this site is backed by, and which of the foreign
// controller's sites it is paired with (ADR-0015).
//
// THE TOKEN IS A VAULT SECRET, and this screen treats it as one — the same
// regime as site credentials. It is write-only from here: the field is empty on
// an edit and leaving it empty keeps the stored one, because the screen cannot
// read it back and therefore cannot resubmit it. Reading it is a separate,
// explicit, audited gesture that says so before it fires.
//
// PAIRING IS A CHOICE FROM A LIST, never a pasted identifier: the controller is
// asked which sites it has, the operator picks one by its NAME, and what gets
// stored is the stable id behind it (ADR-0015 §6). That is what makes "lab
// Events #04 - ARENA" survive being renamed.
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";

import {
  del,
  get,
  post,
  put,
  type ExtRemoteSite,
  type ExtSyncReport,
  type SiteExtension,
} from "../api";
import { useSiteRole } from "../auth";
import { ActionButton, ErrorMsg, Field, HelpTip, Modal, useToast } from "../components";
import { sinceLabel } from "../network";
import { Banner } from "../ui/Banner";
import { Form } from "../ui/Form";
import { IconDeleteButton } from "../ui/IconAction";
import { useSiteId } from "../useSite";
import { extensionLabel } from "./wifi";

/** The only kind this frontend knows how to configure today. */
const UNIFI_OS = "unifi_os";

export default function SiteExtensions() {
  const { t } = useTranslation();
  const siteId = useSiteId();
  const role = useSiteRole(siteId);
  const qc = useQueryClient();
  const [toast, showToast] = useToast();
  const [editing, setEditing] = useState(false);
  const [pairing, setPairing] = useState<SiteExtension | null>(null);
  const [revealed, setRevealed] = useState<string | null>(null);

  const { data: extensions, error } = useQuery({
    queryKey: ["siteExtensions", siteId],
    queryFn: () => get<SiteExtension[]>(`/api/sites/${siteId}/extensions`),
    enabled: !!siteId,
  });

  const invalidate = () => {
    qc.invalidateQueries({ queryKey: ["siteExtensions", siteId] });
    qc.invalidateQueries({ queryKey: ["wifi", siteId] });
  };

  const sync = useMutation({
    mutationFn: (extID: string) =>
      post<ExtSyncReport>(`/api/sites/${siteId}/extensions/${extID}/sync`),
    onSuccess: (rep) => {
      invalidate();
      // The counts per class, not a total: "12 imported" is useless when the
      // question is whether the switches arrived.
      showToast(
        t("siteExtPage.importedCounts", {
          aps: rep.access_points,
          switches: rep.switches,
          ssids: rep.ssids,
        }),
      );
    },
  });

  const remove = useMutation({
    mutationFn: (extID: string) => del(`/api/sites/${siteId}/extensions/${extID}`),
    onSuccess: () => {
      invalidate();
      showToast(t("siteExtPage.removed"));
    },
  });

  const reveal = useMutation({
    mutationFn: (extID: string) =>
      post<{ token: string }>(`/api/sites/${siteId}/extensions/${extID}/reveal`),
    onSuccess: (r) => setRevealed(r.token),
  });

  const unifi = (extensions ?? []).find((e) => e.kind === UNIFI_OS);
  const admin = role === "ing";

  return (
    <>
      <div className="toolbar">
        <h1>
          {t("siteExtPage.title")}
          <span className="sub">{t("siteExtPage.subtitle")}</span>
        </h1>
        {admin && !unifi ? (
          <ActionButton kind="create" onClick={() => setEditing(true)}>
            {t("siteExtPage.connectUnifi")}
          </ActionButton>
        ) : null}
      </div>

      <ErrorMsg error={error} />
      <ErrorMsg error={sync.error} />
      <ErrorMsg error={remove.error} />
      <ErrorMsg error={reveal.error} />

      {/* Stated here rather than only in the ADR, because this is the screen
          where somebody decides to connect a controller: what they are buying
          is a mirror plus a remote control, NOT a second way to configure
          their switches.

          This used to say "an extension reads, it never writes". ADR-0025
          spent that promise — three gestures now go out to the controller's
          API — so the sentence changed with it. What has not changed, and is
          the part worth reading before pasting a token, is that meshcraft
          stores no configuration for these boxes and can push none. */}
      <Banner
        tone="info"
        title={t("siteExtPage.authorityTitle")}
        note={t("siteExtPage.authorityNote")}
      />

      {!unifi ? (
        <div className="card">
          <div className="empty">{t("siteExtPage.none")}</div>
        </div>
      ) : (
        <div className="card">
          <div className="card-head">
            <h2>{extensionLabel(unifi)}</h2>
            <HelpTip label={t("siteExtPage.helpLabel") as string}>
              {t("siteExtPage.help")}
            </HelpTip>
          </div>

          <div className="row">
            <span className="kicker">{t("siteExtPage.controller")}</span>
            <span className="mono">{unifi.base_url}</span>
          </div>
          <div className="row">
            <span className="kicker">{t("siteExtPage.transport")}</span>
            <span>
              {unifi.transport === "proxy"
                ? t("siteExtPage.transportProxy")
                : t("siteExtPage.transportDirect")}
            </span>
            <span className={unifi.verify_tls ? "badge ok" : "badge warn"}>
              {unifi.verify_tls ? t("siteExtPage.tlsVerified") : t("siteExtPage.tlsSkipped")}
            </span>
            <span className="badge">
              {unifi.has_token ? t("siteExtPage.tokenStored") : t("siteExtPage.tokenMissing")}
            </span>
          </div>
          <div className="row">
            <span className="kicker">{t("siteExtPage.pairedSite")}</span>
            {unifi.remote_site_id ? (
              <>
                {/* The NAME is shown because that is what the operator
                    recognises; the id beside it is what is actually stored and
                    matched on. Showing only the name would hide the very thing
                    that makes a rename harmless. */}
                <span>{unifi.remote_site_name || t("siteExtPage.unnamedSite")}</span>
                <span className="note mono">{unifi.remote_site_id}</span>
              </>
            ) : (
              <span className="note">{t("siteExtPage.notPaired")}</span>
            )}
          </div>
          <div className="row note">
            <span className="kicker">{t("siteExtPage.lastImport")}</span>
            <span>
              {unifi.last_sync_at
                ? sinceLabel(unifi.last_sync_at)
                : t("siteExtPage.neverImported")}
            </span>
          </div>

          {/* An orphaned pairing. The mirror is kept (ADR-0015 §7) and this is
              the banner that stops the kept data from being read as current. */}
          {unifi.orphaned_at ? (
            <Banner
              tone="act"
              title={t("siteExtPage.orphanedTitle", {
                name: unifi.remote_site_name || unifi.remote_site_id,
              })}
              note={t("siteExtPage.orphanedNote")}
            />
          ) : null}

          {/* The controller's own sentence, verbatim — never rewritten here. */}
          {unifi.last_error ? (
            <Banner
              tone="act"
              title={t("siteExtPage.lastErrorTitle")}
              items={[unifi.last_error]}
            />
          ) : null}

          {admin ? (
            <div className="actions">
              <ActionButton kind="edit" onClick={() => setEditing(true)}>
                {t("siteExtPage.edit")}
              </ActionButton>
              <ActionButton kind="link" onClick={() => setPairing(unifi)}>
                {unifi.remote_site_id ? t("siteExtPage.repair") : t("siteExtPage.pair")}
              </ActionButton>
              <ActionButton
                kind="refresh"
                onClick={() => sync.mutate(unifi.id)}
                disabled={sync.isPending || !unifi.remote_site_id}
              >
                {t("siteExtPage.importNow")}
              </ActionButton>
              <ActionButton
                kind="check"
                onClick={() => reveal.mutate(unifi.id)}
                disabled={reveal.isPending || !unifi.has_token}
              >
                {t("siteExtPage.revealToken")}
              </ActionButton>
              <IconDeleteButton
                label={t("siteExtPage.deleteExtension") as string}
                onConfirm={() => remove.mutate(unifi.id)}
              />
            </div>
          ) : null}
        </div>
      )}

      {editing ? (
        <ExtensionModal
          siteId={siteId}
          existing={unifi}
          onClose={() => setEditing(false)}
          onSaved={() => {
            setEditing(false);
            invalidate();
            showToast(t("siteExtPage.saved"));
          }}
        />
      ) : null}

      {pairing ? (
        <PairModal
          siteId={siteId}
          extension={pairing}
          onClose={() => setPairing(null)}
          onPaired={(name) => {
            setPairing(null);
            invalidate();
            showToast(t("siteExtPage.paired", { name }));
          }}
        />
      ) : null}

      {revealed !== null ? (
        <Modal title={t("siteExtPage.revealTitle")} onClose={() => setRevealed(null)}>
          {/* Said FIRST, because it already happened: the audit line was
              written before this modal opened. */}
          <p className="note">{t("siteExtPage.revealLogged")}</p>
          <pre className="code">{revealed}</pre>
          <div className="actions">
            <ActionButton
              kind="copy"
              className="secondary"
              onClick={() => navigator.clipboard.writeText(revealed)}
            >
              {t("siteExtPage.copyToken")}
            </ActionButton>
            <ActionButton kind="close" onClick={() => setRevealed(null)}>
              {t("common.closeAction")}
            </ActionButton>
          </div>
        </Modal>
      ) : null}

      {toast}
    </>
  );
}

/**
 * Create or edit the extension.
 *
 * The token field is EMPTY on an edit and that is the contract, not an
 * oversight: the screen never receives the stored token, so an empty field
 * means "keep what is stored" and the placeholder says so. Pre-filling it with
 * dots would invite somebody to "clear" it and destroy the credential.
 */
function ExtensionModal({
  siteId,
  existing,
  onClose,
  onSaved,
}: {
  siteId: string;
  existing?: SiteExtension;
  onClose: () => void;
  onSaved: () => void;
}) {
  const { t } = useTranslation();
  const [baseURL, setBaseURL] = useState(existing?.base_url ?? "");
  const [label, setLabel] = useState(existing?.label ?? "");
  const [token, setToken] = useState("");
  // Default FALSE for a new extension: on-premise UniFi OS ships a self-signed
  // certificate, so defaulting to strict verification would make the first
  // attempt fail for the normal case and teach nothing useful.
  const [verifyTLS, setVerifyTLS] = useState(existing?.verify_tls ?? false);

  const save = useMutation({
    mutationFn: () =>
      put<SiteExtension>(`/api/sites/${siteId}/extensions`, {
        kind: UNIFI_OS,
        label,
        base_url: baseURL,
        // Absent rather than empty: an empty string is a token, an absent key
        // means "leave the stored one alone".
        ...(token.trim() ? { token: token.trim() } : {}),
        verify_tls: verifyTLS,
        transport: "direct",
        enabled: true,
      }),
    onSuccess: onSaved,
  });

  return (
    <Modal
      // The KEY is chosen, then translated — not two translated strings picked
      // between. Same result, and it keeps the `title={t(…)}` shape the UI
      // lint recognises as a component prop rather than a native tooltip.
      title={t(existing ? "siteExtPage.editTitle" : "siteExtPage.createTitle")}
      onClose={onClose}
      busy={save.isPending}
    >
      <Form onSubmit={() => save.mutate()}>
        <ErrorMsg error={save.error} />

        <Field label={t("siteExtPage.fieldBaseUrl")} help={t("siteExtPage.helpBaseUrl")}>
          <input
            value={baseURL}
            onChange={(e) => setBaseURL(e.target.value)}
            placeholder="https://unifi.example.net:443"
            autoFocus
          />
        </Field>

        <Field label={t("siteExtPage.fieldLabel")}>
          <input value={label} onChange={(e) => setLabel(e.target.value)} />
        </Field>

        <Field label={t("siteExtPage.fieldToken")} help={t("siteExtPage.helpToken")}>
          <input
            type="password"
            value={token}
            onChange={(e) => setToken(e.target.value)}
            placeholder={
              existing?.has_token
                ? (t("siteExtPage.tokenKeepPlaceholder") as string)
                : undefined
            }
            autoComplete="new-password"
          />
        </Field>

        <Field label={t("siteExtPage.fieldVerifyTls")} help={t("siteExtPage.helpVerifyTls")}>
          <input
            type="checkbox"
            checked={verifyTLS}
            onChange={(e) => setVerifyTLS(e.target.checked)}
          />
        </Field>

        <div className="actions">
          <ActionButton kind="cancel" type="button" onClick={onClose}>
            {t("common.cancel")}
          </ActionButton>
          <ActionButton kind="save" type="submit" disabled={save.isPending}>
            {t("common.save")}
          </ActionButton>
        </div>
      </Form>
    </Modal>
  );
}

/**
 * Pick the remote site to pair with.
 *
 * A list and not a text field, because the thing that must be stored — a UUID —
 * is not something anybody should be asked to transcribe, and because offering
 * the names is what lets the operator recognise "lab Events #04 - ARENA"
 * while the pairing is keyed on the id behind it.
 */
function PairModal({
  siteId,
  extension,
  onClose,
  onPaired,
}: {
  siteId: string;
  extension: SiteExtension;
  onClose: () => void;
  onPaired: (name: string) => void;
}) {
  const { t } = useTranslation();
  const [chosen, setChosen] = useState(extension.remote_site_id);

  const { data: sites, error } = useQuery({
    queryKey: ["extRemoteSites", siteId, extension.id],
    queryFn: () =>
      get<ExtRemoteSite[]>(`/api/sites/${siteId}/extensions/${extension.id}/remote-sites`),
  });

  const pair = useMutation({
    mutationFn: () =>
      post<SiteExtension>(`/api/sites/${siteId}/extensions/${extension.id}/pair`, {
        remote_site_id: chosen,
      }),
    onSuccess: (e) => onPaired(e.remote_site_name),
  });

  return (
    <Modal title={t("siteExtPage.pairTitle")} onClose={onClose} busy={pair.isPending}>
      <Form onSubmit={() => chosen && pair.mutate()}>
        {/* The listing itself proves the token works, so its failure is the
            most useful error on this screen — shown verbatim. */}
        <ErrorMsg error={error} />
        <ErrorMsg error={pair.error} />

        <Field label={t("siteExtPage.fieldRemoteSite")} help={t("siteExtPage.helpRemoteSite")}>
          <select value={chosen} onChange={(e) => setChosen(e.target.value)}>
            <option value="">{t("siteExtPage.chooseSite")}</option>
            {(sites ?? []).map((s) => (
              <option key={s.id} value={s.id}>
                {s.name}
              </option>
            ))}
          </select>
        </Field>

        <div className="actions">
          <ActionButton kind="cancel" type="button" onClick={onClose}>
            {t("common.cancel")}
          </ActionButton>
          <ActionButton kind="link" type="submit" disabled={!chosen || pair.isPending}>
            {t("siteExtPage.pair")}
          </ActionButton>
        </div>
      </Form>
    </Modal>
  );
}
