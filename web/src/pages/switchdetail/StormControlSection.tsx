import { useQuery } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { get, patch } from "../../api";
import { ActionButton, ErrorMsg } from "../../components";
import { Banner } from "../../ui/Banner";
import {
  STORM_CLASSES,
  buildStormBody,
  classAvailability,
  defaultUnit,
  formatStormRate,
  observedForeign,
  observedLimit,
  portForeign,
  sharedRateConflict,
  type StormCaps,
  type StormClass,
  type StormDraft,
  type StormRateUnit,
  type StormState,
} from "../stormControl";

/**
 * PER-PORT STORM CONTROL, inside the port action overlay.
 *
 * # Why it is LOADED ON DEMAND rather than with the rest of the panel
 *
 * The read reaches the switch. There is no stored observation to repaint from
 * (internal/api/stormcontrol.go says why: a snapshot old enough to disagree with
 * the device at the moment somebody looks at it is worse than no snapshot), so
 * knowing what a port carries costs a session — one of five on the Instant On and
 * SwOS ranges, one of four on the ProCurve 2610. Opening this overlay must not
 * spend one on a gesture the operator was not going to make, so the section
 * starts idle and says what pressing the button will cost.
 *
 * # Why the rate box does not exist until the platform has declared a unit
 *
 * `defaultUnit` returns undefined for a connector that declares none, and this
 * section then offers no threshold field at all. Falling back to percent would be
 * the assumption the whole feature exists to remove — invisible, wrong on most of
 * the fleet, and impossible for the operator to discover.
 *
 * # Why each class is judged separately
 *
 * `classAvailability` has FOUR answers and this section renders four different
 * things, because "this switch has no multicast limiter" (permanent) and
 * "meshcraft has not run the command form on this range yet" (waiting on a lab
 * capture) send an operator in opposite directions. Today every connector is in
 * the second state, so this section is mostly a set of honest explanations — and
 * that is the correct thing for it to be until the captures exist.
 */
export function StormControlSection({
  deviceId,
  port,
  busy,
  error,
  result,
  onApply,
}: {
  deviceId: string;
  port: string;
  busy: boolean;
  error: unknown;
  result?: { skipped?: string[]; not_applied?: { port?: string; kind: "guardrail" | "platform" | "device"; text: string }[]; notes?: string[] };
  onApply: (body: { ports: Record<string, unknown[]> }) => void;
}) {
  const { t } = useTranslation();
  // `enabled: false` until the operator asks: the query is the session cost.
  const [armed, setArmed] = useState(false);
  const read = useQuery({
    queryKey: ["storm-control", deviceId],
    queryFn: () => get<{ caps?: StormCaps; state?: StormState; note?: string }>(
      `/api/devices/${deviceId}/storm-control`),
    enabled: armed,
    // Never refetched behind the operator's back: every refetch is another
    // session on a device that may only grant a handful.
    refetchOnWindowFocus: false,
    staleTime: Infinity,
    retry: false,
  });

  const caps = read.data?.caps;
  const state = read.data?.state;
  const unit = defaultUnit(caps);

  // The drafts are seeded from what the device reports the FIRST time an
  // observation arrives, so the form opens on the port's real configuration
  // rather than on an invented default. Keyed by the port through the overlay's
  // own `key`, so nothing survives from one jack to the next.
  const [drafts, setDrafts] = useState<StormDraft[] | null>(null);
  useEffect(() => {
    if (!read.data || drafts !== null) return;
    setDrafts(STORM_CLASSES.map((cls) => {
      const seen = observedLimit(state, port, cls);
      return {
        class: cls,
        enabled: !!seen?.enabled,
        rate: seen?.enabled && seen.rate !== undefined ? String(seen.rate) : "",
        // The observed unit wins over the platform default: a port already
        // limited in packets per second must not have its box relabelled.
        unit: (seen?.unit ?? unit ?? "percent") as StormRateUnit,
      };
    }));
  }, [read.data, drafts, state, port, unit]);

  const setDraft = (cls: StormClass, patch: Partial<StormDraft>) =>
    setDrafts((prev) => (prev ?? []).map((d) => (d.class === cls ? { ...d, ...patch } : d)));

  const conflict = drafts ? sharedRateConflict(drafts, caps) : undefined;
  const built = drafts ? buildStormBody(port, drafts, caps) : { error: "empty" };
  // Whether ANY class can be written at all. The apply button is not offered
  // otherwise: a button whose only possible outcome is a 422 is the "button that
  // leads nowhere" the capability model exists to prevent.
  const anyAvailable = STORM_CLASSES.some((c) => classAvailability(caps, c) === "available");

  const facts = [
    ...(result?.skipped ?? []),
    ...(result?.not_applied ?? []).map((n) => n.text),
    ...(result?.notes ?? []),
    ...(state?.notes ?? []),
    ...(caps?.why ?? []),
  ];

  return (
    <div style={{ borderTop: "1px solid var(--border)", paddingTop: 12, marginTop: 14 }}>
      <div className="kicker" style={{ marginBottom: 6 }}>
        {t("switchDetailPage.stormTitle")}
      </div>
      <p className="note" style={{ marginTop: 0 }}>
        {t("switchDetailPage.stormIntro")}
      </p>

      {!armed ? (
        <>
          {/* THE SESSION COST IS STATED BEFORE IT IS SPENT. On the ranges with
              four or five sessions in total, a read triggered by opening a
              dialog is a way to run out of them. */}
          <div className="note" style={{ marginBottom: 8 }}>
            {t("switchDetailPage.stormReadCost")}
          </div>
          <ActionButton
            kind="refresh"
            className="secondary"
            style={{ width: "100%" }}
            onClick={() => setArmed(true)}
          >
            {t("switchDetailPage.stormReadBtn")}
          </ActionButton>
        </>
      ) : read.isPending ? (
        <div className="note">{t("switchDetailPage.stormReading")}</div>
      ) : read.error ? (
        <ErrorMsg error={read.error} />
      ) : (
        <>
          {read.data?.note && <div className="note">{read.data.note}</div>}
          {/* A LIMITER ACTING ON EVERY CLASS AT ONCE — Arista's "storm-control
              all". It belongs to no class, so it cannot be shown beside one,
              and it must be shown somewhere: a port carrying it is a port whose
              three class lines would otherwise read as if nothing were policing
              anything. ADR-0020.

              tone="info" and not "act": there is no gesture to offer. meshcraft
              does not edit this line, and painting it orange would teach people
              to ignore orange (UI-GUIDE §2). */}
          {portForeign(state, port).map((f) => (
            <Banner
              key={f.spelling}
              tone="info"
              title={t("switchDetailPage.stormForeignPortTitle")}
              note={t("switchDetailPage.stormForeignPortNote")}
              items={[<code>{f.spelling}</code>, f.because]}
            />
          ))}
          {STORM_CLASSES.map((cls) => {
            const avail = classAvailability(caps, cls);
            const draft = drafts?.find((d) => d.class === cls);
            const seen = observedLimit(state, port, cls);
            const foreign = observedForeign(state, port, cls);
            return (
              <div key={cls} style={{ marginTop: 10 }}>
                <div style={{ fontWeight: 700, fontSize: 12 }}>
                  {t(`switchDetailPage.stormClass_${cls}`)}
                </div>
                {/* WHAT THE DEVICE CARRIES, told in three states and not two:
                    limited, not limited, and NOT READ. The connectors omit a
                    class they could not establish rather than call it
                    unlimited, and flattening that here would show a protection
                    nobody observed. */}
                <div className="note" style={{ marginTop: 2 }}>
                  {seen !== undefined ? (
                    seen.enabled && seen.rate !== undefined && seen.unit ? (
                      t("switchDetailPage.stormObservedLimited", {
                        rate: formatStormRate(seen.rate, seen.unit),
                      })
                    ) : (
                      t("switchDetailPage.stormObservedNone")
                    )
                  ) : foreign ? (
                    /* NOT READ, and we know WHY and WHAT. The bare "not read"
                       sentence below is the honest answer when the connector
                       established nothing; here it established that a limiter is
                       in place in a form it will not reduce to a rate, and the
                       device's own line is the only thing that can tell the
                       operator what is policing this class. ADR-0020. */
                    <>
                      {t("switchDetailPage.stormForeignClass")} <code>{foreign.spelling}</code>
                      <div className="note">{foreign.because}</div>
                    </>
                  ) : (
                    t("switchDetailPage.stormObservedUnread")
                  )}
                </div>

                {avail === "unknown" ? (
                  <div className="note">{t("switchDetailPage.stormCapsUnknown")}</div>
                ) : avail === "cannot" ? (
                  // PERMANENT. Nothing is coming, and the operator must not be
                  // left waiting for a capture that would change nothing.
                  <div className="note" style={{ color: "var(--warn)" }}>
                    {t("switchDetailPage.stormClassUnsupported")}
                  </div>
                ) : avail === "not_verified" ? (
                  // TEMPORARY, and a different sentence entirely: the hardware
                  // has the limiter and meshcraft has not yet run the command
                  // form on this range.
                  <div className="note" style={{ color: "var(--warn)" }}>
                    {t("switchDetailPage.stormNotVerified")}
                  </div>
                ) : draft ? (
                  <div style={{ display: "flex", gap: 8, alignItems: "center", marginTop: 4 }}>
                    <label style={{ display: "flex", gap: 6, alignItems: "center" }}>
                      <input
                        type="checkbox"
                        checked={draft.enabled}
                        disabled={busy}
                        onChange={(e) => setDraft(cls, { enabled: e.target.checked })}
                      />
                      <span>{t("switchDetailPage.stormEnable")}</span>
                    </label>
                    {draft.enabled && unit !== undefined && (
                      <>
                        <input
                          type="text"
                          inputMode="decimal"
                          value={draft.rate}
                          disabled={busy}
                          style={{ width: 90 }}
                          placeholder={t("switchDetailPage.stormRatePlaceholder")}
                          onChange={(e) => setDraft(cls, { rate: e.target.value })}
                        />
                        {/* THE UNIT IS ALWAYS ON SCREEN, and it is only ever one
                            the connector declared. A select with a single option
                            is deliberate: it states the platform's unit rather
                            than leaving a bare number next to a box. */}
                        <select
                          value={draft.unit}
                          disabled={busy || (caps?.units?.length ?? 0) < 2}
                          onChange={(e) =>
                            setDraft(cls, { unit: e.target.value as StormRateUnit })}
                        >
                          {(caps?.units ?? []).map((u) => (
                            <option key={u} value={u}>
                              {t(`switchDetailPage.stormUnit_${u}`)}
                            </option>
                          ))}
                        </select>
                      </>
                    )}
                    {draft.enabled && unit === undefined && (
                      // A platform that declared no unit gets no rate box at
                      // all: the frontend does not invent one.
                      <span className="note">{t("switchDetailPage.stormNoUnit")}</span>
                    )}
                  </div>
                ) : null}
              </div>
            );
          })}

          {caps?.shared_rate && (
            // Said BEFORE the operator types two numbers, not after a refusal.
            <div className="note" style={{ marginTop: 8 }}>
              {t("switchDetailPage.stormSharedRate")}
            </div>
          )}
          {conflict && (
            <div className="note" style={{ color: "var(--warn)", marginTop: 4 }}>
              {t("switchDetailPage.stormSharedConflict", { a: conflict[0], b: conflict[1] })}
            </div>
          )}

          {anyAvailable && (
            <>
              <ActionButton
                kind="check"
                className="secondary"
                style={{ width: "100%", marginTop: 10 }}
                // The button WAITS for a body that would be accepted. Every
                // refusal below it is one the operator can fix without a round
                // trip, so earning it from the server would only be slower.
                disabled={busy || !!built.error}
                onClick={() => built.body && onApply(built.body as { ports: Record<string, unknown[]> })}
              >
                {busy
                  ? t("switchDetailPage.stormApplying")
                  : t("switchDetailPage.stormApplyBtn")}
              </ActionButton>
              {built.error && built.error !== "empty" && (
                <div className="note" style={{ color: "var(--warn)", marginTop: 4 }}>
                  {t("switchDetailPage.stormDraftInvalid")}
                </div>
              )}
            </>
          )}
          <ErrorMsg error={error} />
          {facts.length > 0 && (
            // The connector's own sentences — a refused class, a rate unit it
            // could not settle, a shape caveat. They are composed for a human and
            // a toast that fades in three seconds is the wrong container for
            // them.
            <ul className="note" style={{ margin: "6px 0 0", paddingLeft: 18 }}>
              {facts.map((line, i) => (
                <li key={i}>{line}</li>
              ))}
            </ul>
          )}
        </>
      )}
    </div>
  );
}
