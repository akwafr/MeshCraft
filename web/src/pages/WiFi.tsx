// The Wi-Fi screen: the access points imported from a site extension, and the
// SSIDs the site broadcasts.
//
// WHY THIS IS NOT THE EQUIPMENT PAGE. An access point is not a switch
// (ADR-0016): it has radios, bands, channels and associated clients, and an
// access port has none of those five. Forcing these boxes onto the equipment
// page would make the faceplate, the port gestures, PoE and STP all answer
// "nothing" about an object that never had those properties — and forty-eight
// empty cells do not read as "this box has no ports", they read as a failed
// collection.
//
// WHAT IS EDITABLE HERE, AND WHAT IS NOT (ADR-0029). The SSIDs of the site are
// edited on this screen; the access points are not. That frontier is a
// MEASUREMENT and not a policy: wlanconf re-reads one service set at a time, so
// an SSID setting can be pushed and then verified, while `/rest/device` answers
// 404 on this controller application — there is no reachable route to a per-box
// setting, and the statistics reads that remain omit any field sitting at its
// default, so even a successful write could not be told from one that did
// nothing. Each card says its own half of that in one sentence.
//
// WHAT THIS SCREEN NO LONGER CARRIES, and both removals are ADR-0029 §10-§11:
// the page-top "the controller is the configuration authority" banner, whose
// second sentence became false the moment settings existed and which explained
// a data model to somebody who came to administer their Wi-Fi; and the "import
// now" button, because refreshing a list is not a decision — a background pass
// does it every 30 s. The two facts that banner carried usefully did not go
// with it: what meshcraft does NOT hold is said in the edit form, where
// somebody might think it remembers what they typed, and the mirror's age is
// said on the cards it qualifies.
//
// Its navigation mode is declared in `wifi.ts` (WIFI_SCREEN_MODE), per
// ADR-0013 — switching, because an access point bridges onto a VLAN.
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";

import {
  get,
  post,
  put,
  type ExtCandidate,
  type ExtDevice,
  type ExtRights,
  type ExtSSID,
  type ExtSSIDForm,
  type ExtSSIDSettings,
  type GestureResult,
  type SiteExtension,
  type SiteWiFi,
} from "../api";
import { useSiteRole } from "../auth";
import {
  ActionButton,
  ActionLink,
  ErrorMsg,
  Field,
  HelpTip,
  Modal,
  QuickTip,
  Switch,
  useToast,
} from "../components";
import { agoLabel, sinceLabel } from "../network";
import { Banner } from "../ui/Banner";
import { Form } from "../ui/Form";
import { IconAction } from "../ui/IconAction";
import ScrollX from "../ui/ScrollX";
import { useSite, useSiteId } from "../useSite";
import {
  bandsOf,
  emptyReason,
  isOnline,
  labelOf,
  networkLabel,
  orphanedExtensions,
  sortAccessPoints,
  ssidBands,
  ssidEditPayload,
  ssidSwitchKnown,
  SSID_SWITCHES,
  uplinkOf,
  type BandKey,
} from "./wifi";

export default function WiFi() {
  const { t } = useTranslation();
  const { site } = useSite();
  const siteId = useSiteId();
  const siteSlug = site?.slug ?? "";
  const role = useSiteRole(siteId);
  const qc = useQueryClient();
  const [toast, showToast] = useToast();

  // One read for the whole screen: access points and SSIDs are useless apart —
  // a box with no SSID list, or SSIDs with nothing broadcasting them.
  const { data, error } = useQuery({
    queryKey: ["wifi", siteId],
    queryFn: () => get<SiteWiFi>(`/api/sites/${siteId}/wifi`),
    enabled: !!siteId,
  });

  const extensions = data?.extensions ?? [];
  // In practice there is one paired extension; the array is kept so a second
  // extension kind does not need this screen rewritten.
  const paired = extensions.filter((e) => e.remote_site_id && e.enabled);
  const stale = orphanedExtensions(extensions);

  const aps = sortAccessPoints(data?.access_points ?? []);
  const ssids = data?.ssids ?? [];
  const why = emptyReason(extensions, data?.access_points);

  return (
    <>
      {/* No page heading: the tab bar names this page. And no toolbar: the
          "import now" button that used to live here is gone (ADR-0029 §11).
          Refreshing a list is not an operator's decision, and a button was the
          only way to know how old the list was — which made the staleness a
          thing you had to remember rather than a thing the screen tells you.
          A background pass runs every 30 s; each card carries its own age. */}
      <ErrorMsg error={error} />

      {/* NO page-top authority banner any more (ADR-0029 §10). It explained a
          data model to somebody who came to administer their Wi-Fi, and its
          second sentence — "meshcraft stores nothing and changes nothing" —
          became plainly false the moment the SSID editor existed. The two
          facts worth keeping moved to where they change the next gesture:
          what meshcraft does not retain is said in the edit form, and the
          mirror's age is said on the card it describes. */}

      {/* An orphaned pairing keeps its mirror on screen (ADR-0015 §7), so
          something has to say that what follows describes a site the
          controller no longer lists. This one IS a decision: pair again. */}
      {stale.map((e) => (
        <Banner
          key={e.id}
          tone="act"
          title={t("wifiPage.orphanedTitle", { name: e.remote_site_name || e.kind })}
          note={t("wifiPage.orphanedNote")}
          actions={
            role === "ing" ? (
              <ActionLink kind="edit" to={`/${siteSlug}/config/extensions`}>
                {t("wifiPage.goToExtension")}
              </ActionLink>
            ) : null
          }
        />
      ))}

      {/* The verbatim failure of the last contact. Backend sentences are shown
          as they are, never rewritten client-side. */}
      {extensions
        .filter((e) => e.last_error && !e.orphaned_at)
        .map((e) => (
          <Banner
            key={e.id}
            tone="act"
            title={t("wifiPage.lastErrorTitle")}
            items={[e.last_error]}
          />
        ))}

      {why ? (
        <div className="card">
          <div className="empty">
            {t(`wifiPage.empty.${why}`)}
            {role === "ing" && (why === "no-extension" || why === "not-paired") ? (
              <div className="chips">
                <ActionLink kind="create" to={`/${siteSlug}/config/extensions`}>
                  {t("wifiPage.configureExtension")}
                </ActionLink>
              </div>
            ) : null}
          </div>
        </div>
      ) : (
        <>
          {/* Boxes awaiting adoption come BEFORE the inventory: an operator
              who opened this screen because something is missing should see
              the thing they can act on without scrolling past what is already
              working. */}
          <AdoptableCard ext={paired[0]} canAct={role === "ing"} />
          <SSIDCard
            ssids={ssids}
            ext={paired[0]}
            canAct={role === "ing"}
            onDone={(msg) => {
              qc.invalidateQueries({ queryKey: ["wifi", siteId] });
              showToast(msg);
            }}
          />
          <AccessPointCard
            aps={aps}
            ext={paired[0]}
            canAct={role === "ing"}
            onDone={(msg) => {
              qc.invalidateQueries({ queryKey: ["wifi", siteId] });
              showToast(msg);
            }}
          />
        </>
      )}
      {toast}
    </>
  );
}

/**
 * The boxes the controller can see and has not taken in, with the gesture that
 * takes them in (ADR-0025).
 *
 * THE WARNING IS NOT DECORATION. A pending box belongs to no site on UniFi, so
 * the controller offers the same candidates whichever site you ask — these are
 * not "this site's hardware waiting to be set up", they are everything the
 * controller can currently see anywhere. Adopting is what attaches one to the
 * paired site. Without that sentence an operator at a venue would adopt a box
 * standing in another venue, and the gesture is not free to undo.
 *
 * Absent entirely when there is nothing to adopt: a card that says "nothing is
 * waiting" is a card that teaches people to stop reading cards.
 */
function AdoptableCard({ ext, canAct }: { ext?: SiteExtension; canAct: boolean }) {
  const { t } = useTranslation();
  const siteId = useSiteId();
  const qc = useQueryClient();
  const [toast, showToast] = useToast();

  // Only fetched for somebody who could act on it: the list uses the token
  // against the foreign controller, so it is site_admin on the server too.
  const { data } = useQuery({
    queryKey: ["extAdoptable", siteId, ext?.id],
    queryFn: () => get<ExtCandidate[]>(`/api/sites/${siteId}/extensions/${ext!.id}/adoptable`),
    enabled: !!siteId && !!ext && canAct,
    // Live hardware appears and disappears from this list on its own, and a
    // stale candidate is a gesture that will fail. Short, and only while the
    // screen is open.
    refetchInterval: 30_000,
  });

  const adopt = useMutation({
    mutationFn: (mac: string) =>
      post<GestureResult>(`/api/sites/${siteId}/extensions/${ext!.id}/adopt`, { mac }),
    onSuccess: (res) => {
      qc.invalidateQueries({ queryKey: ["extAdoptable", siteId, ext?.id] });
      qc.invalidateQueries({ queryKey: ["wifi", siteId] });
      // An accepted-but-unconfirmed gesture is NOT a success. The server's own
      // sentence is shown verbatim, because "it may or may not have happened"
      // is the whole message and rewording it would soften it.
      showToast(res.confirmed ? t("wifiPage.adopted") : (res.because ?? ""));
    },
  });

  const candidates = data ?? [];
  if (!ext || !canAct || candidates.length === 0) return null;

  return (
    <div className="card">
      <div className="card-head">
        <h2>{t("wifiPage.adoptableTitle", { count: candidates.length })}</h2>
        <HelpTip label={t("wifiPage.adoptableHelpLabel") as string}>
          {t("wifiPage.adoptableHelp")}
        </HelpTip>
      </div>
      <Banner tone="act" title={t("wifiPage.adoptableScopeTitle")} note={t("wifiPage.adoptableScopeNote")} />
      <ErrorMsg error={adopt.error} />
      <ScrollX>
        <table className="ports-table">
          <thead>
            <tr>
              <th>{t("wifiPage.colMac")}</th>
              <th>{t("wifiPage.colModel")}</th>
              <th className="col-actions" />
            </tr>
          </thead>
          <tbody>
            {candidates.map((c) => (
              <tr key={c.mac}>
                <td>{c.mac}</td>
                <td>
                  {c.model}
                  {c.kind ? <span className="note">{c.kind}</span> : null}
                </td>
                <td className="col-actions">
                  <div className="icon-actions">
                    <IconAction
                      kind="adopt"
                      label={t("wifiPage.adopt", { name: c.mac }) as string}
                      disabled={adopt.isPending}
                      onClick={() => adopt.mutate(c.mac)}
                    />
                  </div>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </ScrollX>
      {toast}
    </div>
  );
}

/**
 * The irreversible one, behind the product's existing door: the box's exact
 * current name, typed, re-read on the server at the moment of the call
 * (ADR-0012's idiom, ADR-0025 §5).
 *
 * NOT `DeleteButton`'s two-step and not a checkbox. Both are values the caller
 * produces without having read anything, so they guard against the stray click
 * and not against the mistake that matters here — acting on the wrong box. The
 * typed name is the only confirmation that cannot be given by accident, and
 * forgetting a box is the gesture in this product that a second click cannot
 * take back.
 *
 * The comparison is deliberately NOT done here. The server re-reads the name
 * from the controller and compares it there, so a name that changed between
 * this screen's last import and the click still fails closed — confirming
 * against the browser's stale copy would be a gate that opens on an object
 * nobody read.
 */
function ForgetModal({
  ap,
  onClose,
  onDone,
}: {
  ap: ExtDevice;
  onClose: () => void;
  onDone: (msg: string) => void;
}) {
  const { t } = useTranslation();
  const siteId = useSiteId();
  const [typed, setTyped] = useState("");

  const forget = useMutation({
    mutationFn: () =>
      post<GestureResult>(`/api/sites/${siteId}/ext-devices/${ap.id}/forget`, {
        confirm_name: typed,
      }),
    onSuccess: (res) => {
      // On THIS gesture the distinction matters most: "removed" and "may or
      // may not have been removed" send the operator to different places.
      onDone(
        res.confirmed
          ? t("wifiPage.forgotten", { name: ap.name || ap.mac })
          : (res.because ?? ""),
      );
      onClose();
    },
  });

  return (
    <Modal title={t("wifiPage.forgetTitle")} onClose={onClose} busy={forget.isPending}>
      <Form onSubmit={() => forget.mutate()}>
        <ErrorMsg error={forget.error} />

        {/* `act`, because this one IS a decision, and the consequences are
            spelled out rather than summarised as "are you sure?". */}
        <Banner
          tone="act"
          title={t("wifiPage.forgetWarnTitle", { name: ap.name || ap.mac })}
          note={t("wifiPage.forgetWarnNote")}
        />

        <Field label={t("wifiPage.forgetField")} help={t("wifiPage.forgetFieldHelp")}>
          <input
            value={typed}
            onChange={(e) => setTyped(e.target.value)}
            placeholder={ap.name}
            autoFocus
            autoComplete="off"
          />
        </Field>

        <div className="actions">
          <ActionButton kind="cancel" type="button" onClick={onClose}>
            {t("common.cancel")}
          </ActionButton>
          {/* Disabled until something is typed — a cheap client-side nudge, not
              the gate: the gate is the server comparing against the
              controller. Never compare here, or a renamed box would pass. */}
          <ActionButton
            kind="delete"
            type="submit"
            disabled={forget.isPending || typed.trim() === ""}
          >
            {t("wifiPage.forgetConfirm")}
          </ActionButton>
        </div>
      </Form>
    </Modal>
  );
}

/**
 * The SSIDs of the SITE. The card title says "of the site" on purpose: UniFi
 * configures them at site level and every access point broadcasts them, so
 * "the SSIDs of this access point" is not a thing that exists (ADR-0016). The
 * reader does see a signal next to a box, hence the explicit note instead of
 * leaving them to infer the relationship.
 */
function SSIDCard({
  ssids,
  ext,
  canAct,
  onDone,
}: {
  ssids: ExtSSID[];
  ext?: SiteExtension;
  canAct: boolean;
  onDone: (msg: string) => void;
}) {
  const { t } = useTranslation();
  const [editing, setEditing] = useState<ExtSSID | null>(null);
  if (ssids.length === 0) return null;
  const editable = canAct && !!ext;
  return (
    <div className="card">
      <div className="card-head">
        <h2>{t("wifiPage.ssidsTitle")}</h2>
        <HelpTip label={t("wifiPage.ssidsHelpLabel") as string}>
          {t("wifiPage.ssidsHelp")}
        </HelpTip>
        {/* THE MIRROR'S AGE, next to the list it qualifies (ADR-0029 §14).
            This used to be implicit in the "import now" button — you knew how
            old the list was because you remembered clicking. With a background
            pass nobody clicks, so the screen has to say it, and it matters
            MORE here than anywhere: this is the list somebody is about to edit
            from. The form itself does not read this copy — it re-reads the
            controller — which is exactly why the two ages are stated apart. */}
        <MirrorAge ext={ext} />
      </div>
      <ScrollX>
        <table className="ports-table">
          <thead>
            <tr>
              <th>{t("wifiPage.colSsid")}</th>
              <th>{t("wifiPage.colBands")}</th>
              <th>{t("wifiPage.colSecurity")}</th>
              <th>{t("wifiPage.colState")}</th>
              {editable ? <th className="col-actions" /> : null}
            </tr>
          </thead>
          <tbody>
            {ssids.map((s) => (
              <tr key={s.id}>
                <td>{s.name}</td>
                <td>
                  <BandPills bands={ssidBands(s)} />
                </td>
                <td>{securityLabel(s)}</td>
                <td>
                  <span className={s.enabled ? "badge ok" : "badge"}>
                    {s.enabled ? t("wifiPage.ssidEnabled") : t("wifiPage.ssidDisabled")}
                  </span>
                </td>
                {editable ? (
                  <td className="col-actions">
                    <div className="icon-actions">
                      {/* Opens the form; it changes nothing itself. The form
                          re-reads the controller before showing a value, so
                          this row's contents are never what gets edited. */}
                      <IconAction
                        kind="edit"
                        label={t("wifiPage.editSsid", { name: s.name }) as string}
                        onClick={() => setEditing(s)}
                      />
                    </div>
                  </td>
                ) : null}
              </tr>
            ))}
          </tbody>
        </table>
      </ScrollX>
      {editing ? (
        <SSIDEditModal ssid={editing} onClose={() => setEditing(null)} onDone={onDone} />
      ) : null}
    </div>
  );
}

/**
 * How old the mirror is, in one line (ADR-0029 §14).
 *
 * Three states, because "nothing to say" is not one of them:
 *  - a successful read, with its age on the VIEWER's clock (ADR-0008);
 *  - never read at all, which is different from "read and empty";
 *  - a read that is failing, which is the state the removed banner would no
 *    longer have told anybody about. It carries the controller's own sentence,
 *    verbatim, because that sentence is the operator's next move.
 *
 * The failure does NOT hide the age: a list that stopped refreshing an hour ago
 * is still a list, and knowing when it was last true is the point.
 */
function MirrorAge({ ext }: { ext?: SiteExtension }) {
  const { t } = useTranslation();
  if (!ext) return null;
  if (!ext.last_sync_at) {
    return <span className="note">{t("wifiPage.mirrorNeverRead")}</span>;
  }
  // `agoLabel` and NOT `sinceLabel`: a read is an EVENT with an age, and
  // network.ts warns in so many words that feeding an event date to sinceLabel
  // produces "read FOR two hours", which reads as a two-hour-long read.
  const age = t("wifiPage.mirrorAge", { since: agoLabel(ext.last_sync_at) }) as string;
  if (!ext.last_error) {
    return (
      <QuickTip text={t("wifiPage.mirrorAgeTip") as string}>
        <span className="note">{age}</span>
      </QuickTip>
    );
  }
  return (
    <QuickTip text={ext.last_error}>
      <span className="badge warn">{t("wifiPage.mirrorStalling", { age })}</span>
    </QuickTip>
  );
}

/**
 * The SSID editor (ADR-0029).
 *
 * FOUR THINGS ABOUT IT ARE DECISIONS, not implementation:
 *
 *  1. IT READS THE CONTROLLER, not the row that opened it. The mirror is at
 *     most thirty seconds old and that is still too old: the precondition's job
 *     is to catch somebody editing this same SSID in the UniFi interface right
 *     now, and comparing our own copy against itself would pass every time.
 *
 *  2. IT SENDS ONLY WHAT MOVED, with where it moved FROM. The server re-reads
 *     at the instant of the write and refuses the whole edit if any of those
 *     fields no longer holds its `was` value — a 409 whose sentence names the
 *     live value. That is why there is no "are you sure" and no typed name: the
 *     guard is a precondition, which costs no friction and catches more.
 *
 *  3. THE PASSPHRASE BOX IS EMPTY AND SAYS WHY. Not masked dots that look like
 *     a value — those are what a distracted save sends back, or blanks. Empty
 *     means "leave it alone", in words, and leaving it alone is genuinely safe
 *     because the server re-sends the whole object it just read.
 *
 *  4. A SETTING THE CONTROLLER DOES NOT REPORT IS DISABLED. UniFi omits a key
 *     sitting at its default, so such a value was never read and no
 *     precondition can honestly be stated about it.
 *
 * What is NOT offered, and the card has to say so rather than leave it to be
 * discovered: the security mode. It is re-readable, so the rule that governs
 * this form does not exclude it — what excludes it is that the coherent set of
 * keys WPA3 needs could not be enumerated from a controller using exactly one
 * of them, and a guessed combination on a live SSID takes a venue off the air.
 * Rotating the passphrase, which is what people actually want, works at a
 * constant security mode.
 */
function SSIDEditModal({
  ssid,
  onClose,
  onDone,
}: {
  ssid: ExtSSID;
  onClose: () => void;
  onDone: (msg: string) => void;
}) {
  const { t } = useTranslation();
  const siteId = useSiteId();
  const [draft, setDraft] = useState<ExtSSIDSettings | null>(null);
  const [passphrase, setPassphrase] = useState("");

  // Live, from the controller. `staleTime: 0` is the point of the whole dialog.
  const { data, error, isLoading } = useQuery({
    queryKey: ["extSSID", siteId, ssid.id],
    queryFn: () => get<ExtSSIDForm>(`/api/sites/${siteId}/ext-ssids/${ssid.id}`),
    enabled: !!siteId,
    staleTime: 0,
    gcTime: 0,
  });

  const live = data?.live.settings ?? {};
  // Seeded once, from the live read — never from the mirror row.
  const current = draft ?? live;

  const save = useMutation({
    mutationFn: () => {
      const payload = ssidEditPayload(live, current, passphrase);
      if (!payload) throw new Error(t("wifiPage.ssidNoChange") as string);
      return put<GestureResult>(`/api/sites/${siteId}/ext-ssids/${ssid.id}`, payload);
    },
    onSuccess: (res) => {
      // An accepted-but-unconfirmed edit is NOT a success, and the server's own
      // sentence is shown verbatim: "it may or may not have landed" is the
      // whole message, and rewording it would soften it.
      onDone(res.confirmed ? t("wifiPage.ssidSaved", { name: ssid.name }) : (res.because ?? ""));
      onClose();
    },
  });

  const set = (patch: ExtSSIDSettings) => setDraft({ ...current, ...patch });
  const nothingToSend = !data || ssidEditPayload(live, current, passphrase) === null;

  return (
    <Modal
      title={t("wifiPage.ssidEditTitle", { name: ssid.name })}
      onClose={onClose}
      busy={save.isPending}
    >
      <Form onSubmit={() => save.mutate()}>
        <ErrorMsg error={error} />
        <ErrorMsg error={save.error} />

        {/* WHAT MESHCRAFT DOES NOT HOLD — the half of the removed banner that
            still matters, said here because here is where somebody could
            believe otherwise (ADR-0029 §10). It is `info`: it states how this
            form works, it does not ask for a decision. */}
        <Banner tone="info" title={t("wifiPage.ssidEditHowTitle")} note={t("wifiPage.ssidEditHowNote")} />

        {isLoading ? <p className="note">{t("wifiPage.ssidEditLoading")}</p> : null}

        {data ? (
          <>
            <Field label={t("wifiPage.ssidName")} help={t("wifiPage.ssidNameHelp")}>
              <input
                value={current.name ?? ""}
                onChange={(e) => set({ name: e.target.value })}
                autoFocus
                autoComplete="off"
              />
            </Field>

            {/* The security mode: shown, not editable, and the help says why
                rather than leaving a greyed field to be wondered about. */}
            <Field label={t("wifiPage.colSecurity")} help={t("wifiPage.ssidSecurityFrozen")}>
              <input value={securityOf(data)} readOnly disabled />
            </Field>

            <Field label={t("wifiPage.ssidPassphrase")} help={t("wifiPage.ssidPassphraseHelp")}>
              {/* DELIBERATELY EMPTY, never prefilled with dots. The box shows
                  nothing because the browser is told nothing — only whether a
                  key is set at all. */}
              <input
                type="password"
                value={passphrase}
                onChange={(e) => setPassphrase(e.target.value)}
                placeholder={
                  data.live.has_passphrase
                    ? (t("wifiPage.ssidPassphraseKeep") as string)
                    : (t("wifiPage.ssidPassphraseNone") as string)
                }
                autoComplete="new-password"
              />
            </Field>

            <Field label={t("wifiPage.ssidNetwork")} help={t("wifiPage.ssidNetworkHelp")}>
              {data.networks && data.networks.length > 0 ? (
                <select
                  value={current.network_id ?? ""}
                  onChange={(e) => set({ network_id: e.target.value })}
                >
                  {data.networks.map((n) => (
                    <option key={n.id} value={n.id}>
                      {networkLabel(n.name, n.vlan, t("wifiPage.untagged") as string)}
                    </option>
                  ))}
                </select>
              ) : (
                // Degraded rather than absent: the list did not answer, and the
                // rest of the form still works.
                <span className="note">{t("wifiPage.ssidNetworkUnavailable")}</span>
              )}
            </Field>

            <Field label={t("wifiPage.colBands")} help={t("wifiPage.ssidBandsHelp")}>
              {/* TWO reasons the editor can be shut, and they need the same
                  treatment: the service set carries a band this build cannot
                  write (6 GHz), or the controller did not report its bands at
                  all. In both cases there is no value to state a precondition
                  against, so editing would send a guess — and the server would
                  refuse it with a message about a field the operator never
                  touched. */}
              {data.live.bands_editable && live.bands !== undefined ? (
                <span className="chips">
                  {(["2.4", "5"] as const).map((b) => (
                    <Switch
                      key={b}
                      label={t("wifiPage.bandGhz", { band: b }) as string}
                      checked={(current.bands ?? []).includes(b)}
                      onChange={(on) =>
                        set({
                          bands: on
                            ? [...(current.bands ?? []), b]
                            : (current.bands ?? []).filter((x) => x !== b),
                        })
                      }
                    />
                  ))}
                </span>
              ) : (
                <span className="note">{t("wifiPage.ssidBandsLocked")}</span>
              )}
            </Field>

            {SSID_SWITCHES.map((key) => (
              <Switch
                key={key}
                label={t(`wifiPage.ssidSwitch.${key}`) as string}
                hint={
                  ssidSwitchKnown(live, key)
                    ? (t(`wifiPage.ssidSwitchHint.${key}`) as string)
                    : (t("wifiPage.ssidSwitchUnknown") as string)
                }
                checked={current[key] === true}
                disabled={!ssidSwitchKnown(live, key)}
                onChange={(on) => set({ [key]: on } as ExtSSIDSettings)}
              />
            ))}
          </>
        ) : null}

        <div className="actions">
          <ActionButton kind="cancel" type="button" onClick={onClose}>
            {t("common.cancel")}
          </ActionButton>
          {/* Disabled while nothing has moved: a PUT that changes nothing is
              still a write at a live venue, and this is the cheap place to
              refuse it. The server refuses it too — this is a nudge, not the
              gate. */}
          <ActionButton kind="save" type="submit" disabled={save.isPending || nothingToSend}>
            {t("wifiPage.ssidSave")}
          </ActionButton>
        </div>
      </Form>
    </Modal>
  );
}

/**
 * The controller's two security words, joined and not interpreted.
 *
 * Same rule as the table's column: "wpapsk / wpa2" is what the controller said,
 * and translating it into our own vocabulary would risk saying something subtly
 * different about somebody's production network.
 */
function securityOf(form: ExtSSIDForm): string {
  return [form.live.security, form.live.wpa_mode].filter(Boolean).join(" / ") || "—";
}

/**
 * The access points, one row each, with their radios.
 *
 * The radio columns are the radio's units — a band in GHz, a width in MHz, a
 * bare channel number. Nothing here goes through the traffic ramp: a radio
 * capacity is not a port capacity (ADR-0016 §5).
 */
function AccessPointCard({
  aps,
  ext,
  canAct,
  onDone,
}: {
  aps: ExtDevice[];
  ext?: SiteExtension;
  canAct: boolean;
  onDone: (msg: string) => void;
}) {
  const { t } = useTranslation();
  const siteId = useSiteId();
  const [forgetting, setForgetting] = useState<ExtDevice | null>(null);

  // What the token may actually do, read rather than assumed (ADR-0025 §7).
  // A read-only key must be known here and not discovered by a failed gesture.
  const { data: rights } = useQuery({
    queryKey: ["extRights", siteId, ext?.id],
    queryFn: () => get<ExtRights>(`/api/sites/${siteId}/extensions/${ext!.id}/rights`),
    enabled: !!siteId && !!ext && canAct,
  });

  const reprovision = useMutation({
    mutationFn: (ap: ExtDevice) =>
      post<GestureResult>(`/api/sites/${siteId}/ext-devices/${ap.id}/reprovision`),
    onSuccess: (res) =>
      onDone(res.confirmed ? t("wifiPage.reprovisioned") : (res.because ?? "")),
  });

  const gestures = canAct && !!ext;

  return (
    <div className="card">
      <div className="card-head">
        <h2>{t("wifiPage.apsTitle", { count: aps.length })}</h2>
        {/* WHY THERE ARE NO SETTINGS ON THIS CARD, in one sentence with the
            detail behind a HelpTip (ADR-0029 §2). It has to be said: the card
            above now edits SSIDs, so the absence of an edit action here reads
            as a missing right unless something names the real reason. And the
            reason is a measurement — the controller exposes no per-box
            resource — not a policy somebody could be argued out of. */}
        <HelpTip label={t("wifiPage.apsReadOnlyHelpLabel") as string}>
          {t("wifiPage.apsReadOnlyHelp")}
        </HelpTip>
        <MirrorAge ext={ext} />
        {/* The rights caveat, only when there is one to make. UniFi does not
            describe a non-owner token's per-site rights, so the honest line is
            "this may be refused" — not a disabled button, which would claim a
            certainty we do not have. */}
        {gestures && rights?.writes === "unproven" ? (
          <QuickTip text={t("wifiPage.rightsUnprovenTip", { admin: rights.admin }) as string}>
            <span className="badge warn">{t("wifiPage.rightsUnproven")}</span>
          </QuickTip>
        ) : null}
      </div>
      <ErrorMsg error={reprovision.error} />
      <ScrollX>
        <table className="ports-table">
          <thead>
            <tr>
              <th>{t("wifiPage.colAp")}</th>
              <th>{t("wifiPage.colModel")}</th>
              <th>{t("wifiPage.colState")}</th>
              <th>{t("wifiPage.colBands")}</th>
              <th>{t("wifiPage.colRadios")}</th>
              <th>{t("wifiPage.colUplink")}</th>
              <th>{t("wifiPage.colAddress")}</th>
              <th>{t("wifiPage.colSeen")}</th>
              {gestures ? <th className="col-actions" /> : null}
            </tr>
          </thead>
          <tbody>
            {aps.map((ap) => {
              const up = uplinkOf(ap);
              return (
                <tr key={ap.id}>
                  <td>
                    {labelOf(ap)}
                    {/* The MAC is the only bridge to the switched world, so it
                        is shown: it is what somebody searches on to find which
                        port this box is plugged into. */}
                    {ap.mac ? <span className="note">{ap.mac}</span> : null}
                  </td>
                  <td>
                    {ap.model}
                    {ap.firmware ? <span className="note">{ap.firmware}</span> : null}
                  </td>
                  <td>
                    {/* The controller's own word, relayed rather than judged —
                        we do not poll this box. */}
                    <span className={isOnline(ap) ? "badge ok" : "badge warn"}>{ap.state}</span>
                  </td>
                  <td>
                    <BandPills bands={bandsOf(ap)} />
                  </td>
                  <td>
                    {ap.radios.length === 0 ? (
                      <span className="note">{t("wifiPage.noRadio")}</span>
                    ) : (
                      ap.radios.map((r, i) => (
                        <div key={i} className="note">
                          {t("wifiPage.radioLine", {
                            standard: r.standard ?? "—",
                            channel: r.channel ?? "—",
                            width: r.width_mhz ?? "—",
                          })}
                        </div>
                      ))
                    )}
                  </td>
                  <td>
                    {up ? (
                      <QuickTip text={t("wifiPage.uplinkTip", { index: up.index }) as string}>
                        <span>
                          {up.speed_mbps
                            ? t("wifiPage.speedMbps", { mbps: up.speed_mbps })
                            : (up.connector ?? "—")}
                        </span>
                      </QuickTip>
                    ) : (
                      <span className="note">—</span>
                    )}
                  </td>
                  <td>{ap.mgmt_ip || <span className="note">—</span>}</td>
                  <td>
                    {/* Freshness against the viewer's clock (ADR-0008): a
                        channel shown here is a snapshot and may be stale. */}
                    <span className="note">{sinceLabel(ap.seen_at)}</span>
                  </td>
                  {gestures ? (
                    <td className="col-actions">
                      <div className="icon-actions">
                        {/* "Reconfigure", and it carries no configuration: it
                            asks the controller to apply ITS OWN again
                            (ADR-0025 §2). `refresh` and not `apply`, because
                            nothing of ours is being applied. */}
                        <IconAction
                          kind="refresh"
                          label={t("wifiPage.reprovision", { name: labelOf(ap) }) as string}
                          disabled={reprovision.isPending}
                          onClick={() => reprovision.mutate(ap)}
                        />
                        {/* Opens the typed-name door; it does not forget
                            anything itself. */}
                        <IconAction
                          kind="delete"
                          className="ghost danger"
                          label={t("wifiPage.forget", { name: labelOf(ap) }) as string}
                          onClick={() => setForgetting(ap)}
                        />
                      </div>
                    </td>
                  ) : null}
                </tr>
              );
            })}
          </tbody>
        </table>
      </ScrollX>
      {forgetting ? (
        <ForgetModal
          ap={forgetting}
          onClose={() => setForgetting(null)}
          onDone={onDone}
        />
      ) : null}
    </div>
  );
}

/**
 * The bands, as badges, in the radios' vocabulary — never the vendor's tags
 * (`2g` beside `2.4 GHz` for the same thing makes a reader wonder whether those
 * are two bands).
 *
 * `.badge` and not `.pill`: a pill is an interactive control in this UI, and a
 * band is a fact, not a button.
 */
function BandPills({ bands }: { bands: BandKey[] }) {
  const { t } = useTranslation();
  if (bands.length === 0) return <span className="note">—</span>;
  return (
    <span className="chips">
      {bands.map((b) => (
        <span key={b} className="badge">
          {b === "other" ? t("wifiPage.bandOther") : t("wifiPage.bandGhz", { band: b })}
        </span>
      ))}
    </span>
  );
}

/**
 * The security of an SSID, from the two words the controller gives. Shown
 * joined rather than interpreted: "wpapsk / wpa2" is what the controller said,
 * and translating it into our own vocabulary would risk saying something
 * subtly different about somebody's production network.
 */
function securityLabel(s: ExtSSID): string {
  return [s.security, s.wpa_mode].filter(Boolean).join(" / ") || "—";
}
