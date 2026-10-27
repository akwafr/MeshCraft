// The assisted hardware reset as a LIVE CHECKLIST — one numbered line per step,
// ticked by what the serial line actually says, never by optimism.
//
// ONE RENDERER FOR EVERY PLATFORM. `StepList` paints the states; each platform
// contributes only its step list and the stage→status mapping (in
// hardResetStepsModel.ts). The steps differ because the hands do — a Catalyst
// reset has a MODE button to hold and a Junos reset has a boot window to catch
// — but the screen must not, or an operator who has done one learns the other
// from scratch.
//
// Catalyst note: the operator's three gestures (unplug, hold MODE, replug) are
// listed as separate lines because that is how hands read instructions — but
// the wire can only prove them WHOLESALE: the first boot-loader chatter means
// power is back AND the button is being held right, so the three tick together
// at the "power-restored" stage. Until then they pulse as the active block.
import { useTranslation } from "react-i18next";

import type { HardResetStage } from "./ciscoIosHardReset";
import type { JuniperExStage } from "./juniperExSoftReset";
import {
  hardStepStatuses,
  junosStepStatuses,
  JUNOS_STEP_ORDER,
  STEP_ORDER,
  type HardStepStatus,
} from "./hardResetStepsModel";

type StepItem = {
  key: string;
  status: HardStepStatus;
  /** The step's own sentence. */
  label: string;
  /** LOUD: the operator has to do something, right now. */
  loud?: boolean;
  /** A bold aside that changes with the wire ("RELEASE", "keep holding"). */
  hint?: string | null;
  /** An italic aside stating a verdict the step produced. */
  verdict?: string | null;
};

/**
 * The shared renderer.
 *
 * A real `<ol>`, so a screen reader announces "4 of 12" on its own — but the
 * number is ALSO printed, because `.hard-steps` suppresses the list marker and
 * the operator asked in so many words for "the steps, numbered, executed one
 * after the other". An unnumbered column of identical dots does not tell you
 * how much is left.
 *
 * The number says WHERE in the run a step sits and the dot says WHAT STATE it
 * is in; they are different questions and they get different glyphs.
 */
function StepList({ items }: { items: StepItem[] }) {
  return (
    <ol className="hard-steps">
      {items.map((it, i) => (
        <li
          key={it.key}
          className={`hard-step ${it.status}${it.status === "active" && it.loud ? " loud" : ""}`}
        >
          <span className="hard-step-n" aria-hidden>
            {i + 1}.
          </span>
          <span className="hard-step-dot" aria-hidden>
            {it.status === "done" ? "✓" : it.status === "active" ? "●" : "○"}
          </span>
          <span>
            {it.label}
            {it.hint && <b> — {it.hint}</b>}
            {it.verdict && <i> — {it.verdict}</i>}
          </span>
        </li>
      ))}
    </ol>
  );
}

export function HardResetSteps({
  connected,
  baudLocked,
  lineHeard,
  running,
  stage,
  verified,
}: {
  connected: boolean;
  baudLocked: boolean;
  /** Automatic line test: null = not concluded, true = answered, false = silent. */
  lineHeard: boolean | null;
  running: boolean;
  stage: HardResetStage | null;
  verified: boolean;
}) {
  const { t } = useTranslation();
  const statuses = hardStepStatuses({
    connected,
    baudLocked,
    lineTested: lineHeard !== null,
    running,
    stage,
    verified,
  });
  // The one hint that changes with the wire: the release line first says
  // "keep holding" (power back, prompt not yet up), then says "RELEASE".
  const releaseHint =
    stage === "power-restored"
      ? t("serialAdd.hardSteps.releaseKeepHolding")
      : stage === "bootloader"
        ? t("serialAdd.hardSteps.releaseNow")
        : null;
  return (
    <StepList
      items={STEP_ORDER.map((key) => ({
        key,
        status: statuses[key],
        label: t(`serialAdd.hardSteps.${key}`),
        loud:
          key === "unplug" || key === "hold-mode" || key === "replug" || key === "release",
        hint: key === "release" ? releaseHint : null,
        verdict:
          key === "line-check" && statuses[key] === "done" ? lineVerdict(t, lineHeard) : null,
      }))}
    />
  );
}

/**
 * The Junos EX assisted reset, same screen furniture, its own steps.
 *
 * The operator's single gesture is the power cycle, and the loud line says so
 * plus the one thing that would lose the window: the loader's own prompt is
 * "Hit [Enter] to boot immediately", so an Enter pressed to "help" is the
 * worst possible keystroke.
 */
export function JunosResetSteps({
  connected,
  baudLocked,
  lineHeard,
  running,
  stage,
  verified,
}: {
  connected: boolean;
  baudLocked: boolean;
  lineHeard: boolean | null;
  running: boolean;
  stage: JuniperExStage | null;
  verified: boolean;
}) {
  const { t } = useTranslation();
  const statuses = junosStepStatuses({
    connected,
    baudLocked,
    lineTested: lineHeard !== null,
    running,
    stage,
    verified,
  });
  return (
    <StepList
      items={JUNOS_STEP_ORDER.map((key) => ({
        key,
        status: statuses[key],
        label: t(`juniperSerial.steps.${key}`),
        loud: key === "power-cycle",
        // Only while it is the operator's turn. The warning about Enter is the
        // one that loses the window, and a line that shouts it from the moment
        // the page opens is a line nobody reads at the moment it matters.
        hint:
          key === "power-cycle" && statuses[key] === "active"
            ? t("juniperSerial.steps.powerCycleHint")
            : null,
        verdict:
          key === "line-check" && statuses[key] === "done" ? lineVerdict(t, lineHeard) : null,
      }))}
    />
  );
}

/**
 * The line test's verdict, spelled out: "silent" ticks the step too (a
 * powered-off switch says nothing, and powering it off is the very next step),
 * but the operator deserves to know which of the two happened.
 */
function lineVerdict(t: (k: string) => string, lineHeard: boolean | null): string | null {
  if (lineHeard === true) return t("serialAdd.hardSteps.lineAlive");
  if (lineHeard === false) return t("serialAdd.hardSteps.lineSilent");
  return null;
}
