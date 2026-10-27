// THE PROFILE — who I am, and the API tokens that are mine. ADR-0011.
//
// WHY THERE IS A NEW ROUTE INSTEAD OF A SECTION IN CONFIGURATION.
//
// Configuration (`/config/…`) is where somebody acts on the INSTANCE: the
// sites, the users, the drivers, the proxies. Every screen in that subtree is
// about objects other people share, and half of it is global-admin only. An
// API token is the opposite kind of object: it is mine, nobody else can see or
// revoke it — not even a global admin, which is the first rule of ADR-0011 —
// and it travels with my identity rather than with this deployment.
//
// Putting it in Configuration, next to Users, would have been the cheap
// answer and it teaches exactly the wrong thing: that tokens are something an
// administrator manages. The first person to look for "the other users'
// tokens" on that screen would be looking for a page that must never exist.
//
// So it lives where the identity already lives — behind the person glyph at
// the end of the top bar (ui/AccountMenu.tsx), whose panel opens on the name
// itself, the same way the gear sits against the site selector because it
// configures the site the selector names. The control sits against the thing
// it acts on. It is NOT under `/:siteSlug/`: a token is not a property of a
// site, even when its scope names some.

import { useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { del, get, post, type Site } from "../api";
import { useAuth } from "../auth";
import {
  ActionButton,
  ActionLink,
  ErrorMsg,
  Field,
  Modal,
  Switch,
  useToast,
} from "../components";
import i18n from "../i18n";
import { LAST_SITE_KEY } from "../paths";
import { PillMultiSelect } from "../PillMultiSelect";
import { Banner } from "../ui/Banner";
import { Form } from "../ui/Form";
import { IconDeleteButton } from "../ui/IconAction";
import { PageHeader } from "../ui/PageHeader";
import { viewerDate } from "../ui/clock";
import {
  createBody,
  curlExample,
  daysLeft,
  defaultDraft,
  EXPIRY_DAYS,
  expiresSoon,
  MAX_NAME_LEN,
  MAX_TOKENS,
  scopeFacts,
  tokenState,
  TOKENS_PATH,
  type ApiToken,
  type ApiTokenCreated,
  type TokenDraft,
} from "./apiTokens";

function fmt(iso: string | null | undefined): string | null {
  if (!iso) return null;
  const d = viewerDate(iso) ?? new Date(iso);
  return Number.isNaN(d.getTime()) ? null : d.toLocaleString(i18n.language);
}

export default function Profile() {
  const { t } = useTranslation();
  const { user } = useAuth();
  const qc = useQueryClient();
  const [toast, showToast] = useToast();
  const [creating, setCreating] = useState(false);
  // THE CLEAR VALUE LIVES IN LOCAL STATE AND NOWHERE ELSE — not in the query
  // cache, not in the mutation's `data`, which both outlive the dialog. One
  // `setState(null)` on close and it is gone from the tab.
  const [minted, setMinted] = useState<ApiTokenCreated | null>(null);

  const { data: sites } = useQuery({
    queryKey: ["sites"],
    queryFn: () => get<Site[]>("/api/sites"),
  });

  // A BARE ARRAY, not an envelope — the route answers `[]` for somebody who
  // has never minted one, and that empty array is a fact rather than a
  // failure. Readable by a token, unlike the two writes below.
  const tokens = useQuery({
    queryKey: ["api-tokens"],
    queryFn: () => get<ApiToken[]>(TOKENS_PATH),
  });

  const revoke = useMutation({
    mutationFn: (id: string) => del(`${TOKENS_PATH}/${id}`),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["api-tokens"] });
      showToast(t("profilePage.revokedToast"));
    },
    // THE SERVER'S SENTENCE, VERBATIM. `ApiError.message` carries the
    // `error` field of the envelope, and those sentences are written to be
    // read: the 403 from `auth.CookieOnly` says why this gesture needs a
    // session, and a 404 deliberately cannot tell "no such token" from "not
    // yours". Replacing either with a house phrase loses the reason.
    onError: (e) =>
      showToast(e instanceof Error ? e.message : t("profilePage.revokeFailed")),
  });

  const siteName = (id: string) =>
    (sites ?? []).find((s) => s.id === id)?.name ?? id;

  // The site the top bar is naming, for the create form's default scope.
  // `/profile` carries no `:siteSlug`, so it is resolved the same way the
  // shell resolves it (Layout's `resolveSlug`): the remembered slug, and
  // failing that the first site — which is exactly what the selector shows
  // when nothing has been remembered yet. Falling through to `null` here
  // would make the default scope EMPTY, and empty means every site: the one
  // outcome this default exists to avoid.
  const currentSiteID = useMemo(() => {
    if (!sites || sites.length === 0) return null;
    let slug = "";
    try {
      slug = localStorage.getItem(LAST_SITE_KEY) ?? "";
    } catch {
      slug = "";
    }
    return (slug && sites.find((s) => s.slug === slug)?.id) || sites[0].id;
  }, [sites]);

  const now = new Date();
  const list = tokens.data ?? [];

  return (
    <div className="page">
      <PageHeader
        title={t("profilePage.title")}
        subtitle={user?.email ?? ""}
      />
      {toast}

      <div className="card">
        <div className="card-head">
          <h3>{t("profilePage.identityTitle")}</h3>
        </div>
        <dl className="profile-facts">
          <dt>{t("common.name")}</dt>
          <dd>{user?.display_name || "—"}</dd>
          <dt>{t("profilePage.emailLabel")}</dt>
          <dd className="mono">{user?.email}</dd>
          <dt>{t("profilePage.accessLabel")}</dt>
          <dd>
            {user?.is_global_admin ? (
              <span className="badge admin">{t("common.globalAdmin")}</span>
            ) : (user?.roles ?? []).length === 0 ? (
              <span className="note">{t("profilePage.noSiteRole")}</span>
            ) : (
              <span className="profile-roles">
                {(user?.roles ?? []).map((r) => (
                  <span key={r.site_id} className="badge">
                    {r.site_name ?? siteName(r.site_id)} · {t(`common.role_${r.role}`)}
                  </span>
                ))}
              </span>
            )}
          </dd>
        </dl>
        {/* The account itself is not editable here, and saying so is better
            than an edit button that 403s: there is no self-service route for a
            password or a display name — a global admin changes them through
            /config/users (PATCH /api/users/{id}). */}
        <p className="note">{t("profilePage.identityReadOnly")}</p>
      </div>

      <div className="card">
        <div className="card-head">
          <h3>{t("profilePage.tokensTitle")}</h3>
          <ActionButton kind="create" onClick={() => setCreating(true)}>
            {t("profilePage.newToken")}
          </ActionButton>
        </div>

        <Banner
          tone="info"
          title={t("profilePage.ruleTitle")}
          note={t("profilePage.ruleNote")}
          actions={
            <ActionLink kind="link" to="/config/api" className="ghost">
              {t("profilePage.readApiDocs")}
            </ActionLink>
          }
        >
          {/* MINTING AND REVOKING NEED THE SESSION, NOT A TOKEN
              (`auth.CookieOnly`). The browser interface always carries the
              cookie, so this never bites here — but it is a property of the
              product rather than an accident of this page, and a reader who
              is about to script against the API has to know the two gestures
              their token will be refused. If it ever does happen, the 403
              arrives as the server's own sentence: the create dialog shows it
              in place, the revoke shows it in a toast, neither rewritten. */}
          <p className="note">{t("profilePage.sessionOnlyNote")}</p>
        </Banner>

        {tokens.error && <ErrorMsg error={tokens.error} />}

        {!tokens.error && list.length === 0 && (
          <p className="note">{t("profilePage.noTokens")}</p>
        )}

        {/* THE CAP IS ANNOUNCED BEFORE IT IS HIT. The server refuses the
            twenty-first with a 409 — a real limit, reached by real people who
            never revoke — and a refusal at the moment of creation is the worst
            time to learn the list is the revocation tool. */}
        {list.length >= MAX_TOKENS - 3 && (
          <Banner
            tone={list.length >= MAX_TOKENS ? "act" : "info"}
            title={t("profilePage.capTitle", { count: list.length, max: MAX_TOKENS })}
            note={t("profilePage.capNote")}
          />
        )}

        {list.length > 0 && (
          <div className="scroll-x">
            <table className="profile-tokens">
              <thead>
                <tr>
                  <th>{t("common.name")}</th>
                  <th>{t("profilePage.scopeCol")}</th>
                  <th>{t("profilePage.lastUsedCol")}</th>
                  <th>{t("profilePage.expiresCol")}</th>
                  <th className="col-actions"></th>
                </tr>
              </thead>
              <tbody>
                {list.map((tk) => {
                  const state = tokenState(tk, now);
                  const left = daysLeft(tk, now);
                  const lastUsed = fmt(tk.last_used_at);
                  return (
                    <tr key={tk.id} className={state === "expired" ? "is-expired" : undefined}>
                      {/* THE PREFIX IS WHAT MAKES THE LIST USABLE. It is the
                          visible head of the value and NOT a credential — it
                          cannot authenticate anything — and it is the only
                          way to tell which row is the token a log line, a CI
                          secret or a 401 is talking about. Without it two
                          tokens called "ansible" are indistinguishable and
                          neither gets revoked. */}
                      <td>
                        <span className="profile-tok-name">{tk.name}</span>
                        <span className="mono profile-tok-prefix">{tk.prefix}</span>
                        <span className="note profile-tok-sub">
                          {t("profilePage.createdOn", { when: fmt(tk.created_at) ?? "—" })}
                        </span>
                      </td>
                      <td>
                        <span className="profile-scope">
                          {scopeFacts(tk, siteName).map((f) => (
                            <span key={f.key} className="badge">
                              {t(f.key, f.params)}
                            </span>
                          ))}
                        </span>
                      </td>
                      {/* NEVER USED IS A FACT, NOT A BLANK. ADR-0011 asks for
                          this column so that a token nobody dares revoke stops
                          existing; a dash would leave the same doubt. */}
                      <td>
                        {lastUsed ?? (
                          <span className="note">{t("profilePage.neverUsed")}</span>
                        )}
                      </td>
                      <td>
                        {state === "expired" ? (
                          <span className="badge danger">{t("profilePage.expired")}</span>
                        ) : left === null ? (
                          <span className="note">{t("profilePage.noExpiry")}</span>
                        ) : (
                          <span className={expiresSoon(tk, now) ? "badge warn" : undefined}>
                            {t("profilePage.inDays", { count: left })}
                          </span>
                        )}
                      </td>
                      <td className="col-actions">
                        <div className="icon-actions">
                          <IconDeleteButton
                            label={t("profilePage.revoke") as string}
                            onConfirm={() => revoke.mutate(tk.id)}
                          />
                        </div>
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
      </div>

      {creating && (
        <CreateTokenModal
          sites={sites ?? []}
          currentSiteID={currentSiteID}
          onClose={() => setCreating(false)}
          onCreated={(c) => {
            setCreating(false);
            setMinted(c);
            qc.invalidateQueries({ queryKey: ["api-tokens"] });
          }}
        />
      )}
      {minted && (
        <SecretOnceModal created={minted} onDone={() => setMinted(null)} />
      )}
    </div>
  );
}

function CreateTokenModal({
  sites,
  currentSiteID,
  onClose,
  onCreated,
}: {
  sites: Site[];
  currentSiteID: string | null;
  onClose: () => void;
  onCreated: (c: ApiTokenCreated) => void;
}) {
  const { t } = useTranslation();
  const [draft, setDraft] = useState<TokenDraft>(() => defaultDraft(currentSiteID));
  const set = (p: Partial<TokenDraft>) => setDraft((d) => ({ ...d, ...p }));

  const create = useMutation({
    mutationFn: () => post<ApiTokenCreated>(TOKENS_PATH, createBody(draft)),
    onSuccess: onCreated,
  });

  const options = sites.map((s) => ({ id: s.id, label: s.name, hint: s.slug }));
  // The live reading of the scope, in the same words the list will use. A
  // form that only shows controls makes the reader assemble the sentence
  // themselves, and that is the step where somebody mints a token wider than
  // they meant to.
  const facts = scopeFacts(
    {
      read_only: draft.readOnly,
      site_ids: draft.siteIDs,
      expires_at: draft.expiresInDays === null ? null : "later",
    },
    (id) => sites.find((s) => s.id === id)?.name ?? id,
  );

  return (
    <Modal
      title={t("profilePage.createTitle")}
      onClose={onClose}
      busy={create.isPending}
      help={t("profilePage.createHelp")}
    >
      <Form onSubmit={() => create.mutate()} busy={create.isPending}>
        <Field label={t("profilePage.nameLabel")} help={t("profilePage.nameHelp")}>
          {/* The cap is the server's own (`maxTokenNameLen`), enforced here
              so it is a field that stops accepting rather than a 400 after
              the fact. */}
          <input
            autoFocus
            maxLength={MAX_NAME_LEN}
            value={draft.name}
            onChange={(e) => set({ name: e.target.value })}
            placeholder={t("profilePage.namePlaceholder") as string}
          />
        </Field>

        <Switch
          checked={draft.readOnly}
          onChange={(v) => set({ readOnly: v })}
          label={t("profilePage.readOnlyLabel")}
          hint={t("profilePage.readOnlyHint")}
        />

        <Field label={t("profilePage.expiryLabel")} help={t("profilePage.expiryHelp")}>
          <select
            value={draft.expiresInDays === null ? "never" : String(draft.expiresInDays)}
            onChange={(e) =>
              set({
                expiresInDays: e.target.value === "never" ? null : Number(e.target.value),
              })
            }
          >
            {EXPIRY_DAYS.map((d) => (
              <option key={d ?? "never"} value={d === null ? "never" : String(d)}>
                {d === null
                  ? t("profilePage.expiryNever")
                  : t("profilePage.expiryDays", { count: d })}
              </option>
            ))}
          </select>
        </Field>

        <Field label={t("profilePage.sitesLabel")} help={t("profilePage.sitesHelp")}>
          <PillMultiSelect
            options={options}
            selected={draft.siteIDs}
            onChange={(ids) => set({ siteIDs: ids })}
            placeholder={t("profilePage.sitesPlaceholder") as string}
            ariaLabel={t("profilePage.sitesLabel") as string}
          />
        </Field>

        {/* Leaving the field empty is the WIDEST token this product mints, and
            it is reached by not choosing. It is allowed — a fleet-wide script
            is a legitimate thing to want — and it is announced. */}
        {draft.siteIDs.length === 0 && (
          <Banner tone="act" title={t("profilePage.allSitesWarnTitle")}>
            {t("profilePage.allSitesWarnBody")}
          </Banner>
        )}

        <div className="profile-preview">
          <span className="profile-preview-lbl">{t("profilePage.willBeAbleTo")}</span>
          <span className="profile-scope">
            {facts.map((f) => (
              <span key={f.key} className="badge">
                {t(f.key, f.params)}
              </span>
            ))}
          </span>
        </div>

        {create.error && <ErrorMsg error={create.error} />}
        <div className="actions">
          <ActionButton
            kind="cancel"
            className="ghost"
            type="button"
            onClick={onClose}
            disabled={create.isPending}
          >
            {t("common.cancel")}
          </ActionButton>
          <ActionButton
            kind="create"
            type="submit"
            disabled={create.isPending || draft.name.trim() === ""}
          >
            {t("profilePage.createAction")}
          </ActionButton>
        </div>
      </Form>
    </Modal>
  );
}

/**
 * The one and only sighting of the clear value — ADR-0011 §3.
 *
 * THIS DIALOG REFUSES EVERY ACCIDENTAL DISMISSAL. `busy` is set until the
 * operator presses the button that acknowledges they have the value: Escape,
 * a backdrop click and a stray outside-mousedown would each destroy something
 * that cannot be recovered, which is exactly the case `Modal`'s `busy` guard
 * exists for ("closing a dialog mid-apply orphans the outcome the operator
 * must read"). The same button is what wipes it, so the value cannot linger
 * on screen either.
 */
function SecretOnceModal({
  created,
  onDone,
}: {
  created: ApiTokenCreated;
  onDone: () => void;
}) {
  const { t } = useTranslation();
  const [copied, setCopied] = useState(false);
  // `ApiTokenCreated` IS the token row, with one extra field. The clear value
  // is `token`, flat beside `prefix` and `read_only` — there is no nested
  // object, and `prefix` below is the head of this very value, which is what
  // will let the holder find this row in the list afterwards.
  const curl = curlExample(window.location.origin, created.read_only);

  return (
    <Modal
      title={t("profilePage.secretTitle")}
      onClose={onDone}
      busy={!copied}
      wide
    >
      <Banner tone="act" title={t("profilePage.secretWarnTitle")}>
        {t("profilePage.secretWarnBody")}
      </Banner>

      <div className="profile-secret">
        {/* NOT `.scroll-x`. A ~100-character value in a 480px dialog would
            hide its last third behind a horizontal drag, and this is the one
            value nobody can come back and read again — see the rule in
            styles.css. It wraps. */}
        <code>{created.token}</code>
        <ActionButton
          kind="copy"
          onClick={() => {
            void navigator.clipboard.writeText(created.token);
            setCopied(true);
          }}
        >
          {t("profilePage.copySecret")}
        </ActionButton>
      </div>
      {/* The head of the value, named as such. It is the row this token will
          be in the list, so somebody who copied the secret into a vault can
          still answer "which of these four is it" a month from now. */}
      <p className="note">
        {t("profilePage.secretPrefix")} <span className="mono">{created.prefix}</span>
      </p>
      {/* The clipboard is not the only way out: a browser that refuses it
          (no permission, no secure context) must not trap the dialog. */}
      {!copied && (
        <ActionButton kind="check" className="ghost" onClick={() => setCopied(true)}>
          {t("profilePage.savedByHand")}
        </ActionButton>
      )}

      <p className="note">{t("profilePage.secretHeaderOnly")}</p>
      <pre className="scroll-x profile-curl">{curl}</pre>

      <div className="actions">
        <ActionButton kind="finish" type="button" disabled={!copied} onClick={onDone}>
          {t("profilePage.secretDone")}
        </ActionButton>
      </div>
    </Modal>
  );
}
