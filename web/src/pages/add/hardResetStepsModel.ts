// The checklist MODEL — pure, node-testable (no JSX). The components in
// HardResetSteps.tsx render it; the test replays it against the runner's
// stage sequence: a checklist that lies is worse than none.
//
// ONE RENDERER, ONE STEP STATUS VOCABULARY, ONE PER-PLATFORM STEP LIST. The
// steps themselves cannot be shared — the Catalyst's are its physical gestures
// (unplug, HOLD MODE, replug, release) and a Junos reset has none of them — but
// everything around them is, and two reset screens that do not look alike are
// two things to learn.
import type { HardResetStage } from "./ciscoIosHardReset.ts";
import type { JuniperExStage } from "./juniperExSoftReset.ts";

export type HardStepKey =
  | "connect"
  | "baud"
  | "line-check"
  | "unplug"
  | "hold-mode"
  | "replug"
  | "release"
  | "wiping"
  | "booting"
  | "provisioning"
  | "verify";

export const STEP_ORDER: HardStepKey[] = [
  "connect",
  "baud",
  "line-check",
  "unplug",
  "hold-mode",
  "replug",
  "release",
  "wiping",
  "booting",
  "provisioning",
  "verify",
];

/** Rank of the runner stage on the checklist axis. */
function stageRank(stage: HardResetStage | null): number {
  switch (stage) {
    case "line-check":
      return 2; // the line-check step is ACTIVE
    case "await-power-cycle":
      return 3; // unplug/hold/replug active as a block
    case "power-restored":
      return 6; // the three gestures proven; release pending the prompt
    case "bootloader":
      return 6; // release ACTIVE — loud
    case "wiping":
      return 7;
    case "booting":
      return 8;
    case "provisioning":
      return 9;
    default:
      return -1;
  }
}

export type HardStepStatus = "pending" | "active" | "done";

/**
 * PURE: the whole checklist state from the wizard's facts. Exported for the
 * dialogue test's sibling — a checklist that lies is worse than none.
 */
export function hardStepStatuses(facts: {
  connected: boolean;
  baudLocked: boolean;
  /**
   * The AUTOMATIC line test has concluded (answered or silent — both are
   * conclusions: silence is what a powered-off switch says). It runs on its
   * own the moment the speed locks; before it existed, "test the line" only
   * ran inside the assisted run, and an operator who had connected and
   * detected watched this step sit unticked forever — the third "blocked at
   * test the line" field report, and the first one that was a DESIGN hole
   * rather than a bug.
   */
  lineTested?: boolean;
  running: boolean;
  stage: HardResetStage | null;
  verified: boolean;
}): Record<HardStepKey, HardStepStatus> {
  const out = {} as Record<HardStepKey, HardStepStatus>;
  const rank = stageRank(facts.stage);
  STEP_ORDER.forEach((key, i) => {
    if (key === "connect") {
      out[key] = facts.connected ? "done" : "active";
      return;
    }
    if (key === "baud") {
      out[key] = facts.baudLocked ? "done" : facts.connected ? "active" : "pending";
      return;
    }
    if (key === "verify") {
      out[key] = facts.verified ? "done" : "pending";
      return;
    }
    if (rank < 0) {
      // Outside a run, the line test lives its own life: it fires
      // automatically on baud lock (active while in flight, done when it has
      // concluded) instead of waiting for the start button.
      if (key === "line-check") {
        out[key] = facts.lineTested
          ? "done"
          : facts.connected && facts.baudLocked
            ? "active"
            : "pending";
        return;
      }
      out[key] = "pending";
      return;
    }
    // The operator's three gestures are ONE block on the wire: all active
    // while the wizard waits, all proven together by the first boot chatter.
    if (facts.stage === "await-power-cycle" && (i === 3 || i === 4 || i === 5)) {
      out[key] = "active";
      return;
    }
    out[key] = i < rank ? "done" : i === rank ? "active" : "pending";
  });
  return out;
}

/* ───────────────────── the same checklist, for Junos EX ──────────────────── */

export type JunosStepKey =
  | "connect"
  | "baud"
  | "line-check"
  | "power-cycle"
  | "loader"
  | "recovery"
  | "zeroize"
  | "booting"
  | "amnesiac"
  | "ztp-off"
  | "provisioning"
  | "verify";

/**
 * The Junos EX assisted reset, step by step.
 *
 * ONE operator gesture, not three: there is no MODE button on this platform to
 * hold, only a boot window of about a second that nobody can aim at by hand, so
 * `power-cycle` is the only loud line on the list and everything after it is the
 * wizard's. That is exactly why the Catalyst's step list could not be reused as
 * it stands — it would show two buttons that do not exist.
 */
export const JUNOS_STEP_ORDER: JunosStepKey[] = [
  "connect",
  "baud",
  "line-check",
  "power-cycle",
  "loader",
  "recovery",
  "zeroize",
  "booting",
  "amnesiac",
  "ztp-off",
  "provisioning",
  "verify",
];

/** Rank of a Junos runner stage on the checklist axis above. */
function junosStageRank(stage: JuniperExStage | null): number {
  switch (stage) {
    case "line-check":
      return 2;
    case "await-power-cycle":
      return 3; // the operator's one gesture — ACTIVE and loud
    case "loader":
      return 4; // a door answered: loader prompt, or the Path-B boot menu
    case "single-user":
      return 5; // `boot -s` → `recovery`, or the menu's [M]ore → [C]LI
    case "zeroize":
      return 6;
    case "booting":
      return 7;
    case "amnesiac":
      return 8;
    case "ztp-off":
      return 9;
    case "provisioning":
      return 10;
    case "verify":
      return 11;
    default:
      return -1;
  }
}

/**
 * PURE, same contract as `hardStepStatuses` and same reason: the checklist is
 * read as a claim about the switch, so it is computed from the wizard's facts
 * and never from optimism.
 */
export function junosStepStatuses(facts: {
  connected: boolean;
  baudLocked: boolean;
  /** The automatic line test has CONCLUDED — answered or silent, both are
   * conclusions (silence is what a powered-off switch says, and powering it off
   * is the very next step). */
  lineTested?: boolean;
  running: boolean;
  stage: JuniperExStage | null;
  verified: boolean;
}): Record<JunosStepKey, HardStepStatus> {
  const out = {} as Record<JunosStepKey, HardStepStatus>;
  const rank = junosStageRank(facts.stage);
  JUNOS_STEP_ORDER.forEach((key, i) => {
    if (key === "connect") {
      out[key] = facts.connected ? "done" : "active";
      return;
    }
    if (key === "baud") {
      out[key] = facts.baudLocked ? "done" : facts.connected ? "active" : "pending";
      return;
    }
    if (key === "verify") {
      // The network probe is the only step the serial line cannot prove, so it
      // ticks on the candidate being found and on nothing else.
      out[key] = facts.verified ? "done" : rank === 11 ? "active" : "pending";
      return;
    }
    if (rank < 0) {
      if (key === "line-check") {
        out[key] = facts.lineTested
          ? "done"
          : facts.connected && facts.baudLocked
            ? "active"
            : "pending";
        return;
      }
      out[key] = "pending";
      return;
    }
    out[key] = i < rank ? "done" : i === rank ? "active" : "pending";
  });
  return out;
}

