// The alerting screen: which channels exist, what they cover, and whether they
// actually work (ADR-0024).
//
// NAVIGATION MODE (ADR-0013 / ADR-0019): this is a SITE CONFIGURATION page. It
// lives in the `config` mode, under the site subtree, beside credentials and
// extensions — not in `switching` or `routing`, because a notification channel
// is not a property of either fabric. The mode is declared by where the route
// and the nav entry sit, and that placement is the declaration.
//
// THE TOKEN IS A VAULT SECRET, and this screen treats it as one: write-only, an
// empty field on an edit means "keep the stored one", and reading it back is a
// separate audited gesture that says so before it fires. Same regime as a site
// credential or an extension token.
//
// THE SCREEN'S REAL JOB IS THE THIRD TRAP. A channel that has sent nothing is
// either healthy and idle or quietly broken, and the whole point of the health
// column, the pending/failed counts and the test button is that an operator can
// tell the two apart WITHOUT waiting for an incident to find out.
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";

import {
  del,
  get,
  post,
  put,
  type AlertClass,
  type NotifyChannel,
  type NotifyTestResult,
} from "../api";
import { useSiteRole } from "../auth";
import { ActionButton, ErrorMsg, Field, HelpTip, Modal, useToast } from "../components";
import { sinceLabel } from "../network";
import { Banner } from "../ui/Banner";
import { Form } from "../ui/Form";
import { IconDeleteButton } from "../ui/IconAction";
import { useSiteId } from "../useSite";
import {
  ALERT_CLASSES,
  channelHealth,
  channelWouldStaySilent,
  editableHere,
  healthTone,
  missingClasses,
  sortChannels,
} from "./alerting";

export default function Alerting() {
  const { t } = useTranslation();
  const siteId = useSiteId();
  const role = useSiteRole(siteId);
  const qc = useQueryClient();
  const [toast, showToast] = useToast();
  const [editing, setEditing] = useState<NotifyChannel | "new" | null>(null);
  const [revealed, setRevealed] = useState<string | null>(null);

  const { data: channels, error } = useQuery({
    queryKey: ["notifyChannels", siteId],
    queryFn: () => get<NotifyChannel[]>(`/api/sites/${siteId}/notify-channels`),
    enabled: !!siteId,
  });

  const invalidate = () =>
    qc.invalidateQueries({ queryKey: ["notifyChannels", siteId] });

  const remove = useMutation({
    mutationFn: (id: string) => del(`/api/sites/${siteId}/notify-channels/${id}`),
    onSuccess: () => {
      invalidate();
      showToast(t("alertingPage.removed"));
    },
  });

  const reveal = useMutation({
    mutationFn: (id: string) =>
      post<{ token: string }>(`/api/sites/${siteId}/notify-channels/${id}/reveal`),
    onSuccess: (r) => setRevealed(r.token),
  });

  // The test. Its answer is shown verbatim, because the far end's own words are
  // the diagnostic: "chat not found" and "no_service" send an operator to two
  // different fixes, and flattening both to "failed" sends them nowhere.
  const test = useMutation({
    mutationFn: (id: string) =>
      post<NotifyTestResult>(`/api/sites/${siteId}/notify-channels/test`, { id }),
    onSuccess: (r) => {
      invalidate();
      showToast(r.ok ? t("alertingPage.testSent") : t("alertingPage.testFailed", { error: r.error }));
    },
  });

  const admin = role === "ing";
  const list = sortChannels(channels ?? []);

  return (
    <>
      <div className="toolbar">
        <h1>
          {t("alertingPage.title")}
          <span className="sub">{t("alertingPage.subtitle")}</span>
        </h1>
        {admin ? (
          <ActionButton kind="create" onClick={() => setEditing("new")}>
            {t("alertingPage.addChannel")}
          </ActionButton>
        ) : null}
      </div>

      <ErrorMsg error={error} />
      <ErrorMsg error={remove.error} />
      <ErrorMsg error={reveal.error} />
      <ErrorMsg error={test.error} />

      {/* The exclusion rule, stated on the screen where somebody configures a
          channel and not only in the ADR. It is the counter-intuitive half of
          the policy, and a newcomer will propose reverting it in good faith
          unless the reasoning is where the decision gets made. */}
      <Banner
        tone="info"
        title={t("alertingPage.policyTitle")}
        note={t("alertingPage.policyNote")}
      />

      {list.length === 0 ? (
        <div className="card">
          <div className="empty">{t("alertingPage.none")}</div>
        </div>
      ) : (
        list.map((c) => (
          <ChannelCard
            key={c.id}
            channel={c}
            siteId={siteId}
            admin={admin}
            busy={test.isPending || reveal.isPending}
            onEdit={() => setEditing(c)}
            onTest={() => test.mutate(c.id)}
            onReveal={() => reveal.mutate(c.id)}
            onDelete={() => remove.mutate(c.id)}
          />
        ))
      )}

      {editing ? (
        <ChannelModal
          siteId={siteId}
          existing={editing === "new" ? undefined : editing}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null);
            invalidate();
            showToast(t("alertingPage.saved"));
          }}
        />
      ) : null}

      {revealed !== null ? (
        <Modal title={t("alertingPage.revealTitle")} onClose={() => setRevealed(null)}>
          {/* Said FIRST, because it already happened: the audit line was
              written before this modal opened. */}
          <p className="note">{t("alertingPage.revealLogged")}</p>
          <pre className="code">{revealed}</pre>
          <div className="actions">
            <ActionButton
              kind="copy"
              className="secondary"
              onClick={() => navigator.clipboard.writeText(revealed)}
            >
              {t("alertingPage.copyToken")}
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

/** One channel, its coverage, and the evidence that it works. */
function ChannelCard({
  channel,
  siteId,
  admin,
  busy,
  onEdit,
  onTest,
  onReveal,
  onDelete,
}: {
  channel: NotifyChannel;
  siteId: string;
  admin: boolean;
  busy: boolean;
  onEdit: () => void;
  onTest: () => void;
  onReveal: () => void;
  onDelete: () => void;
}) {
  const { t } = useTranslation();
  const health = channelHealth(channel);
  const mine = editableHere(channel, siteId);
  const gaps = missingClasses(channel);

  return (
    <div className="card">
      <div className="card-head">
        <h2>
          {channel.label || t(`alertingPage.kind_${channel.kind}`)}
          {!channel.site_id ? (
            <span className="badge">{t("alertingPage.instanceWide")}</span>
          ) : null}
        </h2>
        <HelpTip label={t("alertingPage.helpLabel") as string}>
          {t("alertingPage.help")}
        </HelpTip>
      </div>

      <div className="row">
        <span className="kicker">{t("alertingPage.channel")}</span>
        <span>{t(`alertingPage.kind_${channel.kind}`)}</span>
        {/* Telegram addresses by chat id, Slack by a URL that is itself the
            secret — so there is nothing to show for Slack, and saying so is
            better than an empty cell that reads as missing configuration. */}
        {channel.kind === "telegram" ? (
          <span className="mono">{channel.target}</span>
        ) : (
          <span className="note">{t("alertingPage.slackTargetInUrl")}</span>
        )}
        <span className="badge">
          {channel.has_token
            ? t("alertingPage.tokenStored")
            : t("alertingPage.tokenMissing")}
        </span>
      </div>

      <div className="row">
        <span className="kicker">{t("alertingPage.health")}</span>
        <span className={`badge ${healthTone(health)}`}>
          {t(`alertingPage.health_${health}`)}
        </span>
        <span className="note">
          {channel.last_ok_at
            ? t("alertingPage.lastOk", { when: sinceLabel(channel.last_ok_at) })
            : t("alertingPage.neverSucceeded")}
        </span>
      </div>

      <div className="row">
        <span className="kicker">{t("alertingPage.covers")}</span>
        {channel.classes.map((k) => (
          <span key={k} className="badge ok">
            {t(`alertingPage.class_${k}`)}
          </span>
        ))}
        {gaps.length > 0 ? (
          <span className="note">
            {t("alertingPage.notCovered", {
              classes: gaps.map((k) => t(`alertingPage.class_${k}`)).join(", "),
            })}
          </span>
        ) : null}
      </div>

      {/* The ledger. `pending` is the anti-flap holding transitions back — not a
          fault. `failed` is the sentence that matters: the alert EXISTED and was
          never delivered, which is exactly what a channel-blind product could
          not tell you. */}
      <div className="row note">
        <span className="kicker">{t("alertingPage.queue")}</span>
        <span>{t("alertingPage.pendingCount", { count: channel.pending })}</span>
        <span>{t("alertingPage.failedCount", { count: channel.failed })}</span>
      </div>

      {/* ADR-0024's blind spot, as a number. A port nobody described and facing
          nothing we know falls silently; this is the only honest mitigation, and
          hiding it would make the silence total. */}
      {channel.dropped_uninteresting > 0 ? (
        <div className="row note">
          <span className="kicker">{t("alertingPage.excluded")}</span>
          <span>
            {t("alertingPage.droppedCount", { count: channel.dropped_uninteresting })}
          </span>
          <HelpTip label={t("alertingPage.whyExcludedLabel") as string}>
            {t("alertingPage.whyExcluded")}
          </HelpTip>
        </div>
      ) : null}

      {/* Said before the incident, not after it. */}
      {channelWouldStaySilent(channel) ? (
        <Banner
          tone="act"
          title={t("alertingPage.wouldStaySilentTitle")}
          note={t("alertingPage.wouldStaySilentNote")}
        />
      ) : null}

      {health === "untested" && channel.has_token ? (
        <Banner
          tone="act"
          title={t("alertingPage.untestedTitle")}
          note={t("alertingPage.untestedNote")}
        />
      ) : null}

      {/* The far end's own words, verbatim — never rewritten here. */}
      {channel.last_error ? (
        <Banner
          tone="act"
          title={t("alertingPage.lastErrorTitle", {
            count: channel.consecutive_failures,
          })}
          items={[channel.last_error]}
        />
      ) : null}

      {admin && mine ? (
        <div className="actions">
          <ActionButton kind="edit" onClick={onEdit}>
            {t("alertingPage.edit")}
          </ActionButton>
          {/* The gesture this screen exists for: an integration you cannot try
              before the incident is one whose misconfiguration you discover
              during it. */}
          <ActionButton
            kind="play"
            onClick={onTest}
            disabled={busy || !channel.has_token}
          >
            {t("alertingPage.sendTest")}
          </ActionButton>
          <ActionButton
            kind="check"
            onClick={onReveal}
            disabled={busy || !channel.has_token}
          >
            {t("alertingPage.revealToken")}
          </ActionButton>
          <IconDeleteButton
            label={t("alertingPage.deleteChannel") as string}
            onConfirm={onDelete}
          />
        </div>
      ) : null}
    </div>
  );
}

/**
 * Create or edit a channel.
 *
 * The token field is EMPTY on an edit and that is the contract, not an
 * oversight: the screen never receives the stored token, so an empty field
 * means "keep what is stored" and the placeholder says so.
 */
function ChannelModal({
  siteId,
  existing,
  onClose,
  onSaved,
}: {
  siteId: string;
  existing?: NotifyChannel;
  onClose: () => void;
  onSaved: () => void;
}) {
  const { t } = useTranslation();
  const [kind, setKind] = useState<"telegram" | "slack">(existing?.kind ?? "telegram");
  const [label, setLabel] = useState(existing?.label ?? "");
  const [target, setTarget] = useState(existing?.target ?? "");
  const [token, setToken] = useState("");
  const [classes, setClasses] = useState<AlertClass[]>(
    existing?.classes ?? [...ALERT_CLASSES],
  );
  const [minSeverity, setMinSeverity] = useState(existing?.min_severity ?? 2);
  const [enabled, setEnabled] = useState(existing?.enabled ?? true);
  const [tested, setTested] = useState<NotifyTestResult | null>(null);

  const body = () => ({
    ...(existing ? { id: existing.id } : {}),
    kind,
    label,
    // Slack carries no target: its webhook URL is addressing and credential at
    // once, and the backend refuses a target on a Slack channel rather than
    // storing a second unsealed copy of half the secret.
    target: kind === "telegram" ? target.trim() : "",
    // Absent rather than empty: an empty string is a token, an absent key means
    // "leave the stored one alone".
    ...(token.trim() ? { token: token.trim() } : {}),
    classes,
    min_severity: minSeverity,
    enabled,
  });

  const save = useMutation({
    mutationFn: () =>
      put<NotifyChannel>(`/api/sites/${siteId}/notify-channels`, body()),
    onSuccess: onSaved,
  });

  // Testing BEFORE saving is the order an operator actually works in: paste the
  // token, check it reaches the right chat, then keep it.
  const test = useMutation({
    mutationFn: () =>
      post<NotifyTestResult>(`/api/sites/${siteId}/notify-channels/test`, body()),
    onSuccess: (r) => setTested(r),
  });

  const toggle = (k: AlertClass) =>
    setClasses((cur) => (cur.includes(k) ? cur.filter((x) => x !== k) : [...cur, k]));

  return (
    <Modal
      // The KEY is chosen, then translated — not two translated strings picked
      // between, which keeps the `title={t(…)}` shape the UI lint recognises as
      // a component prop rather than a native tooltip.
      title={t(existing ? "alertingPage.editTitle" : "alertingPage.createTitle")}
      onClose={onClose}
      busy={save.isPending}
    >
      <Form onSubmit={() => save.mutate()}>
        <ErrorMsg error={save.error} />
        <ErrorMsg error={test.error} />

        <Field label={t("alertingPage.fieldKind")} help={t("alertingPage.helpKind")}>
          <select
            value={kind}
            onChange={(e) => setKind(e.target.value as "telegram" | "slack")}
          >
            <option value="telegram">{t("alertingPage.kind_telegram")}</option>
            <option value="slack">{t("alertingPage.kind_slack")}</option>
          </select>
        </Field>

        <Field label={t("alertingPage.fieldLabel")}>
          <input value={label} onChange={(e) => setLabel(e.target.value)} autoFocus />
        </Field>

        {kind === "telegram" ? (
          <Field
            label={t("alertingPage.fieldChatId")}
            help={t("alertingPage.helpChatId")}
          >
            <input
              value={target}
              onChange={(e) => setTarget(e.target.value)}
              placeholder="-1001234567890"
            />
          </Field>
        ) : null}

        <Field
          label={t(kind === "telegram" ? "alertingPage.fieldBotToken" : "alertingPage.fieldWebhook")}
          help={t(kind === "telegram" ? "alertingPage.helpBotToken" : "alertingPage.helpWebhook")}
        >
          <input
            type="password"
            value={token}
            onChange={(e) => setToken(e.target.value)}
            placeholder={
              existing?.has_token
                ? (t("alertingPage.tokenKeepPlaceholder") as string)
                : undefined
            }
            autoComplete="new-password"
          />
        </Field>

        <Field label={t("alertingPage.fieldClasses")} help={t("alertingPage.helpClasses")}>
          <div className="check-group">
            {ALERT_CLASSES.map((k) => (
              <label key={k}>
                <input
                  type="checkbox"
                  checked={classes.includes(k)}
                  onChange={() => toggle(k)}
                />
                {t(`alertingPage.class_${k}`)}
              </label>
            ))}
          </div>
        </Field>

        <Field
          label={t("alertingPage.fieldMinSeverity")}
          help={t("alertingPage.helpMinSeverity")}
        >
          <select
            value={String(minSeverity)}
            onChange={(e) => setMinSeverity(Number(e.target.value))}
          >
            <option value="1">{t("alertingPage.sevAll")}</option>
            <option value="2">{t("alertingPage.sevNotice")}</option>
            <option value="3">{t("alertingPage.sevWarning")}</option>
          </select>
        </Field>

        <Field label={t("alertingPage.fieldEnabled")}>
          <input
            type="checkbox"
            checked={enabled}
            onChange={(e) => setEnabled(e.target.checked)}
          />
        </Field>

        {/* The far end's answer, verbatim. A test that failed is as worth
            showing as one that worked — it is what turns "misconfigured" into
            "misconfigured, and here is which part". */}
        {tested ? (
          <Banner
            tone={tested.ok ? "info" : "act"}
            title={t(tested.ok ? "alertingPage.testSent" : "alertingPage.testFailedTitle")}
            items={tested.ok ? undefined : [tested.error ?? ""]}
          />
        ) : null}

        <div className="actions">
          <ActionButton kind="cancel" type="button" onClick={onClose}>
            {t("common.cancel")}
          </ActionButton>
          <ActionButton
            kind="play"
            type="button"
            className="secondary"
            onClick={() => test.mutate()}
            disabled={test.isPending || (!token.trim() && !existing?.has_token)}
          >
            {t("alertingPage.testBeforeSaving")}
          </ActionButton>
          <ActionButton kind="save" type="submit" disabled={save.isPending}>
            {t("common.save")}
          </ActionButton>
        </div>
      </Form>
    </Modal>
  );
}
