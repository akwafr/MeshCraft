import { useEffect, useState, useRef } from "react";
import { useTranslation } from "react-i18next";
import { useDismissable } from "../../ui/dismiss";
import { ApiError, type Device } from "../../api";
import { ActionButton, ErrorMsg } from "../../components";
import { fmtMbps, type PortRef } from "../../network";
import type { PortClassBand, PortLevelLine } from "../../telemetry";
import { LineChart, SERIES } from "../../viz";
import { PortClassChart } from "../../PortClassChart";
// The one fact the overlay's shut/no-shut button is allowed to state, kept out
// of React so it can be asserted directly (portAdmin.test.ts). Never re-derived
// inline: a "Shut" label computed a second time at a call site is how a shut
// becomes a no-shut.
import { adminGesture } from "../portAdmin";
import { LevelChart } from "../../LevelChart";
import { StormControlSection } from "./StormControlSection";

/**
 * How long the power cycle leaves the volts off, in whole seconds.
 *
 * FOUR, which is the driver's own default (driver.DefaultPowerCycleOff) written
 * out rather than left to the server's `0 means you decide`: the overlay tells
 * the operator how long the far-end device will be dark, and a number on screen
 * that came from somewhere else would eventually stop matching what happens.
 * The point of the cut is that a powered device's supply COLLAPSES, which takes
 * seconds — longer is not safer, it is just a longer outage, and the server caps
 * it at sixty for that reason.
 */
export const POWER_CYCLE_OFF_SECONDS = 4;

/**
 * THE CONFIRMATION OVERLAY for the two gestures that reach the hardware and are
 * not stored anywhere: shut / no-shut, and a PoE power cycle.
 *
 * # Why a modal at all, on a page whose every other control acts on the click
 *
 * Because these two are the only ones on this screen with no undo and no
 * intermediate state to inspect. Every other button here writes an INTENT that
 * a re-read compares and an apply can replay; these leave no trace to look at
 * afterwards (cf. internal/api/portadmin_apply.go). A misclick on the faceplate
 * costs a stored value somebody can see and fix. A misclick here costs a
 * customer's link, or reboots the access point serving a floor.
 *
 * # Why the two buttons are far apart and worded differently
 *
 * They get confused for each other, and that confusion is expensive in one
 * direction: a shut drops ethernet and the far-end device keeps running, a power
 * cycle cuts the volts and the far-end device REBOOTS. The driver contract
 * refuses to let a caller make that trade by accident, and this overlay refuses
 * to let an operator make it by reading too fast — hence one section each, the
 * consequence spelled out in the section rather than on the button, and no
 * shared "apply" that would ever be ambiguous about which one it just did.
 *
 * # The force checkbox
 *
 * Only when the port carries a LIVE LINK (adminGesture().needsForce). On the
 * ranges with neither SSH nor a serial port, shutting the jack that carries the
 * administration session ends the conversation permanently and the recourse is a
 * paperclip on site. The connector refuses that case unless `force` says the
 * operator accepts the trip, and the honest way to earn that flag is a separate,
 * unticked box — a caller that always sent it would make this whole screen
 * decorative.
 */
export function PortActionOverlay({
  dev,
  pr,
  traffic,
  classes,
  levels,
  stepSeconds,
  adminBusy,
  cycleBusy,
  error,
  deviceId,
  stormBusy,
  stormError,
  stormResult,
  onStorm,
  onAdmin,
  onPowerCycle,
  onClose,
}: {
  dev: Device;
  pr: PortRef;
  /** Both traffic directions of THIS port (see ../telemetry, portTraffic). */
  traffic: { rx: (number | null)[]; tx: (number | null)[] };
  /**
   * Inbound traffic of THIS port by destination class (see ../telemetry,
   * portClasses). Carried here as well as in the panel because this overlay is
   * where a storm limiter is set: the stack is what says whether the class
   * about to be policed is the one actually flooding (ADR-0028).
   */
  classes: PortClassBand[];
  /** Optical level history of THIS port (see ../telemetry, portLevels). */
  levels: PortLevelLine[];
  /** The pitch of the served window, to name the points of the axes. */
  stepSeconds: number;
  adminBusy: boolean;
  cycleBusy: boolean;
  /** The server's refusal, kept ON SCREEN — see the note next to ErrorMsg. */
  error: unknown;
  deviceId: string;
  stormBusy: boolean;
  stormError: unknown;
  stormResult?: { skipped?: string[]; notes?: string[] };
  onStorm: (body: { ports: Record<string, unknown[]> }) => void;
  /** `enable` is the contract's ENABLED bool: false shuts. */
  onAdmin: (enable: boolean, force: boolean) => void;
  onPowerCycle: () => void;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const [force, setForce] = useState(false);
  const busy = adminBusy || cycleBusy;

  // What a shut/no-shut click on THIS port would actually do, read from the
  // observed link_state and computed in portAdmin.ts. Never from the button's
  // own label: a "Shut" offered on an already-shut port is not a cosmetic slip,
  // it is a gesture that does nothing while the operator believes the port went
  // down.
  const gesture = adminGesture(pr.p.link_state);
  // `known` first: it says whether the two flags below MEAN anything. False is
  // "the connector was never resolved", i.e. we have no answer at all — the
  // reason both sections below branch on it before looking at the flags.
  const capsKnown = !!dev.port_caps?.known;
  const canAdmin = !!dev.port_caps?.admin;
  // Power cycle needs BOTH answers: the box must be able to cycle at all
  // (connector declares PoE writable) AND this particular jack must source
  // power. Observed capability beats the faceplate flag — a mixed unit like the
  // EdgePoint S16 has jacks with no PSE hardware behind them, and the
  // group-level flag is too coarse to say which.
  const poeObs = dev.poe_state?.ports?.find((x) => x.port === pr.name);
  const poeCapable = poeObs ? poeObs.capable : pr.poeCap;
  const canCycle = !!dev.port_caps?.power_cycle && poeCapable;

  // What a FAILED gesture still reported. `skipped` before `notes`: skipped is
  // a decision the connector took (a jack it refused to cut), notes are facts
  // it observed (the class it could not restore) — an operator reads the
  // decision first, then its consequences. Empty for any error that carried no
  // result, which is most of them.
  const failureFacts =
    error instanceof ApiError
      ? [
          ...(error.result?.skipped ?? []),
          ...(error.result?.not_applied ?? []).map((n) => n.text),
          ...(error.result?.notes ?? []),
        ]
      : [];

  // CLOSING IS BLOCKED WHILE A GESTURE IS IN FLIGHT, and the power cycle is the
  // reason: its answer arrives only once power is BACK, and it carries the one
  // fact nobody else will ever tell the operator — whether the restore half
  // worked, i.e. whether the port is dark. Letting the overlay be dismissed
  // mid-cycle would not cancel anything (the server deliberately detaches the
  // gesture from the request), it would only throw the answer away.
  const close = () => {
    if (busy) return;
    onClose();
  };

  // Escape and outside-click go through the dismissal stack, which carries
  // the same `busy` refusal — the guard this overlay carried alone before the
  // contract existed (UI-GUIDE §8.3), now the shared one. The backdrop rule
  // is mousedown-based there, so a text selection released outside no longer
  // dismisses mid-read.
  const panelRef = useRef<HTMLDivElement>(null);
  useDismissable(true, onClose, { insideRef: panelRef, busy });

  return (
    <div className="modal-backdrop">
      <div className="modal" ref={panelRef} role="dialog" aria-modal="true">
        <h2>
          {t("switchDetailPage.portActionTitle", { device: dev.name, port: pr.name })}
        </h2>
        <p className="note" style={{ marginTop: 0 }}>
          {t("switchDetailPage.portActionIntro")}
        </p>

        {/* --- What the port is DOING, before what to do to it -----------------
            The two measurements an operator wants in view while deciding to cut
            a link: the traffic that would stop, and — on an optical port — the
            level history that may be the reason they are here at all. Same
            charts as the side panel (same helpers, same colours), so the modal
            never disagrees with the screen behind it. Rendered only when a
            curve exists: an empty frame above a destructive button is noise at
            the worst possible spot. */}
        {(traffic.rx.some((v) => v != null) || traffic.tx.some((v) => v != null)) && (
          <div style={{ marginTop: 4, marginBottom: 4 }}>
            <div className="kicker" style={{ marginBottom: 4 }}>
              {t("switchDetailPage.portTrafficTitle")}
            </div>
            <LineChart
              height={110}
              series={[
                { name: t("switchDetailPage.chartIncoming"), color: SERIES.in, points: traffic.rx },
                { name: t("switchDetailPage.chartOutgoing"), color: SERIES.out, points: traffic.tx },
              ]}
              format={fmtMbps}
              xLabel={({ rawIndex, rawTotal, newest }) =>
                newest
                  ? t("switchDetailPage.chartNow")
                  : t("switchDetailPage.chartMinutesAgo", {
                      n: Math.round(((rawTotal - 1 - rawIndex) * stepSeconds) / 60),
                    })
              }
            />
          </div>
        )}
        {levels.length > 0 && (
          <div style={{ marginTop: 24, marginBottom: 4 }}>
            <div className="kicker" style={{ marginBottom: 4 }}>
              {t("switchDetailPage.opticsLevelsTitle")}
            </div>
            <LevelChart lines={levels} stepSeconds={stepSeconds} />
          </div>
        )}
        {/* Directly ABOVE the storm-control section further down, and that is
            the order it earns: this overlay is where a limiter gets set, and
            the stack is the evidence for which class to set it on. A broadcast
            limit typed while the flooding is multicast polices nothing. */}
        <div className="portclass-block">
          <div className="kicker portclass-kicker">
            {t("switchDetailPage.portClassTitle")}
          </div>
          <PortClassChart bands={classes} stepSeconds={stepSeconds} />
        </div>

        {/* --- Shut / no-shut ------------------------------------------------ */}
        <div style={{ borderTop: "1px solid var(--border)", paddingTop: 12, marginTop: 12 }}>
          <div className="kicker" style={{ marginBottom: 6 }}>
            {t("switchDetailPage.portActionAdminTitle")}
          </div>
          <p className="note" style={{ marginTop: 0 }}>
            {gesture.shut
              ? t("switchDetailPage.portActionShutWhat")
              : t("switchDetailPage.portActionEnableWhat")}
          </p>
          {!capsKnown ? (
            // "We cannot tell" is NOT "the platform says no", and the DTO keeps
            // them apart (PortActionCaps.known) precisely so a screen does not
            // merge them: a device whose connector could not be resolved might
            // well support the gesture, and telling the operator the platform
            // refuses it would be a claim about hardware we never reached.
            <div className="note">
              {t("switchDetailPage.portActionCapsUnknown")}
            </div>
          ) : !canAdmin ? (
            // The platform does not declare the write. Said here, before the
            // click, rather than earned as a 422 — but the wording matches what
            // the server would have answered, so the two never contradict.
            <div className="note" style={{ color: "var(--warn)" }}>
              {t("switchDetailPage.portActionAdminUnsupported")}
            </div>
          ) : (
            <>
              {gesture.needsForce && (
                <div
                  className="note"
                  style={{
                    border: "1px solid var(--warn)", borderRadius: 8,
                    padding: "8px 10px", marginBottom: 10,
                  }}
                >
                  <div style={{ color: "var(--warn)", fontWeight: 700 }}>
                    {t("switchDetailPage.portActionLinkWarnTitle")}
                  </div>
                  <div style={{ marginTop: 4 }}>
                    {t("switchDetailPage.portActionLinkWarnBody")}
                  </div>
                  <label style={{ display: "flex", gap: 6, alignItems: "flex-start", marginTop: 8 }}>
                    <input
                      type="checkbox"
                      checked={force}
                      disabled={busy}
                      onChange={(e) => setForce(e.target.checked)}
                    />
                    <span>{t("switchDetailPage.portActionForceCheck")}</span>
                  </label>
                </div>
              )}
              <ActionButton
                kind={gesture.shut ? "ban" : "play"}
                className={gesture.shut ? "danger" : "secondary"}
                style={{ width: "100%" }}
                // The confirm button WAITS for the checkbox on a live port. Not
                // a warning next to an enabled button: the whole point is that
                // the gesture cannot happen while the operator has only read the
                // first half of the sentence.
                disabled={busy || (gesture.needsForce && !force)}
                onClick={() => onAdmin(gesture.enable, force)}
              >
                {adminBusy
                  ? t("switchDetailPage.portActionWorking")
                  : gesture.shut
                    ? t("switchDetailPage.portActionShutBtn")
                    : t("switchDetailPage.portActionEnableBtn")}
              </ActionButton>
              <div className="note" style={{ fontSize: 10, marginTop: 6 }}>
                {t("switchDetailPage.portAdminReadNote")}
              </div>
            </>
          )}
        </div>

        {/* --- PoE power cycle ---------------------------------------------- */}
        <div style={{ borderTop: "1px solid var(--border)", paddingTop: 12, marginTop: 14 }}>
          <div className="kicker" style={{ marginBottom: 6 }}>
            {t("switchDetailPage.portActionCycleTitle")}
          </div>
          {!capsKnown ? (
            <div className="note">
              {t("switchDetailPage.portActionCapsUnknown")}
            </div>
          ) : !dev.port_caps?.power_cycle ? (
            <div className="note" style={{ color: "var(--warn)" }}>
              {t("switchDetailPage.portActionCycleUnsupported")}
            </div>
          ) : !poeCapable ? (
            <div className="note">{t("switchDetailPage.portActionCycleNoPoe")}</div>
          ) : (
            <>
              {/* REBOOT, in those words. "Bounce" and "restart the port" are
                  what an operator reads when they are looking for a link fix,
                  and both are wrong here: the thing at the other end loses its
                  supply and comes back in its own time, taking however long its
                  firmware takes. */}
              <p className="note" style={{ marginTop: 0, color: "var(--warn)" }}>
                {t("switchDetailPage.portActionCycleBody", { seconds: POWER_CYCLE_OFF_SECONDS })}
              </p>
              <ActionButton
                kind="refresh"
                className="danger"
                style={{ width: "100%" }}
                disabled={busy || !canCycle}
                onClick={onPowerCycle}
              >
                {cycleBusy
                  ? t("switchDetailPage.portActionCyclePending")
                  : t("switchDetailPage.portActionCycleBtn")}
              </ActionButton>
              {cycleBusy && (
                // The request blocks for the whole off-duration by contract.
                // Without this line, four seconds of an unresponsive dialog on a
                // gesture that reboots hardware reads as a hung page — and a
                // second click is the last thing anyone wants here.
                <div className="note" style={{ fontSize: 10, marginTop: 6 }}>
                  {t("switchDetailPage.portActionCycleWaitNote")}
                </div>
              )}
            </>
          )}
        </div>

        {/* --- Per-port storm control --------------------------------------- */}
        <StormControlSection
          deviceId={deviceId}
          port={pr.name}
          busy={stormBusy}
          error={stormError}
          result={stormResult}
          onApply={onStorm}
        />

        {/* The server's refusal stays ON SCREEN, next to the button that earned
            it, and the overlay does NOT close on failure. These messages are
            composed for a human (driver.Unsupported): a toast that fades after
            three seconds is the wrong container for a sentence explaining that a
            connector knows how to set a PoE class but cannot promise the restore
            half of a cycle. */}
        <ErrorMsg error={error} />

        {/* A FAILED write can still have facts to report, and on this overlay
            that is not a nicety: when a power cycle cuts the volts and then
            fails to put them back, the connector names in `result.notes` the
            class it could not restore — which port is dark, and what it should
            be carrying (driver.PortPowerCycler: "a silent failure here leaves a
            camera off with nothing on screen to explain it"). The controller
            returns those notes beside `error` on its 502, so they are shown
            here, under the refusal, rather than being dropped as they were
            before ApiError carried them. */}
        {failureFacts.length > 0 && (
          <ul className="note" style={{ margin: "6px 0 0", paddingLeft: 18 }}>
            {failureFacts.map((line, i) => (
              <li key={i}>{line}</li>
            ))}
          </ul>
        )}

        <div className="actions">
          <ActionButton kind="close" className="ghost" disabled={busy} onClick={close}>
            {t("common.closeAction")}
          </ActionButton>
        </div>
      </div>
    </div>
  );
}
