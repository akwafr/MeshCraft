/**
 * FLEET CREDENTIAL DEPLOYMENT — the decisions the screen makes, kept out of
 * React and out of i18n so they can be asserted directly (same pattern and same
 * reason as pages/portAdmin.ts, pages/hostnameConformity.ts, freshness.ts).
 * This module imports NOTHING, which is what lets its test run under a bare
 * `node --experimental-strip-types`.
 *
 * The mechanism it renders lives in `internal/api/creddeploy.go` and
 * `internal/store/deployment.go`; read those first. What matters here is the
 * part of it an operator can get wrong:
 *
 *  1. A DEPLOYMENT IS TWO GESTURES, not one. `POST …/deploy` without
 *     `"write": true` is a REHEARSAL: it opens the real session on every queued
 *     device, applies every gate up to and including the account check, and
 *     stops before the switch changes. The write is a second, explicit gesture.
 *     The API defaults to the rehearsal — a missing field must never produce the
 *     fleet-wide write — and this screen must not undo that default by offering
 *     one button that does both. [writeGate] is where that is enforced.
 *  2. PARTIAL IS THE NORMAL STATE. `carrying` / `behind` / `unreached` / `ahead`
 *     are all expected to be non-empty at some point in a rotation, and the only
 *     forbidden move is promoting while `behind` or `unreached` holds anyone.
 *     The screen shows four buckets, never a percentage that hides which.
 *  3. A PUSH IS NOT A PROMOTION, and that is the other phase boundary the
 *     screen has to state. Neither gesture above retires anything: the previous
 *     generation stays `retiring` and is still offered on every login for as
 *     long as any device is recorded as carrying it, which is what keeps a
 *     half-deployed fleet reachable. `safe_to_promote` is the verdict on that
 *     separate step, it is computed server-side with its reason attached, and it
 *     waits on EVERY device in scope — one row in `unreached` withholds it. The
 *     screen prints the server's sentence rather than recomputing the rule: two
 *     copies of it would eventually let the screen say "ready" about a promotion
 *     the store refuses.
 *  4. A ROW WITH NO NAME IS THE DANGEROUS ONE. `store.PlanDeployment` folds the
 *     caller's SCOPE into the observation, and a device that is in scope and was
 *     never observed arrives as `{device_id}` with no name, no date and no
 *     error — see [deviceLabel]. Rendering `""` there would erase precisely the
 *     devices the vault is about to make claims about.
 */

/** One device's OBSERVED state for a credential (`store.DeviceGeneration`). */
export interface DeviceGeneration {
  device_id: string;
  device_name?: string;
  /** 0 means "never observed under this credential", never "generation zero". */
  generation: number;
  installed_username?: string;
  installed_at?: string | null;
  last_verified_at?: string | null;
  last_error?: string;
}

/** `store.DeploymentPlan` as `GET …/deployment` serves it. */
export interface DeploymentPlan {
  target: number;
  carrying: DeviceGeneration[];
  behind: DeviceGeneration[];
  unreached: DeviceGeneration[];
  ahead: DeviceGeneration[];
}

/** The whole `GET …/deployment` body. */
export interface DeploymentView {
  credential_id: string;
  plan: DeploymentPlan;
  outstanding: number;
  safe_to_promote: boolean;
  blocked_because?: string;
}

/** One line of a connector transcript (`driver.Step`). */
export interface DeployStep {
  what: string;
  detail?: string;
  ok: boolean;
}

export interface DeployTarget {
  device_id: string;
  device_name: string;
  status: string;
  mgmt_ip: string;
  installed_generation: number;
  installed_username: string;
}

export interface DeploySkip {
  device_id: string;
  device_name: string;
  reason: string;
}

export interface DeployResult {
  device_id: string;
  device_name: string;
  outcome: string;
  detail?: string;
  account?: string;
  generation?: number;
  at: string;
  steps?: DeployStep[];
}

/** `credDeployStatus` — the run as `GET …/deploy` serves it. */
export interface DeployStatus {
  credential_id: string;
  label: string;
  target: number;
  write: boolean;
  actor: string;
  started_at: string;
  finished_at?: string;
  running: boolean;
  stopped?: string;
  queued: DeployTarget[];
  skipped: DeploySkip[];
  results: DeployResult[];
  remaining: number;
}

/** Outcomes, spelled as `internal/api/creddeploy.go` spells them. */
export const OUTCOME_INSTALLED = "installed";
export const OUTCOME_REHEARSED = "rehearsed";
export const OUTCOME_SKIPPED = "skipped";
export const OUTCOME_FAILED = "failed";
export const OUTCOME_UNCONFIRMED = "unconfirmed";

/** The four buckets, in the order the screen states them. */
export type Bucket = "behind" | "unreached" | "carrying" | "ahead";

/**
 * What to call a device on a deployment row.
 *
 * NEVER THE EMPTY STRING, AND NEVER A BARE UUID IF ANYTHING BETTER EXISTS.
 * `PlanDeployment` synthesises `{device_id: id}` — no name, no date, no error —
 * for every device that is in the credential's scope and has never been observed
 * under it, and its own comment calls those the most dangerous rows in the plan
 * precisely because there is nothing to show for them. They are also the rows
 * that block promotion, so they are the ones an operator has to go and act on:
 * a line reading `4f1c-…` names something nobody can find in a rack.
 *
 * The fallback ladder is therefore: the name the plan carried, then the name the
 * site's device list has for that id, then the id itself. The middle rung is the
 * one that matters and it is why this takes a second argument; the last rung
 * stays because a device deleted between the two reads still has to appear.
 */
export function deviceLabel(
  g: { device_name?: string; device_id: string },
  names?: Record<string, string>,
): string {
  const own = (g.device_name ?? "").trim();
  if (own !== "") return own;
  const known = (names?.[g.device_id] ?? "").trim();
  if (known !== "") return known;
  return g.device_id;
}

/** id → name, from the site's device list, for [deviceLabel]'s middle rung. */
export function namesById(devices: { id: string; name: string }[] | undefined): Record<string, string> {
  const out: Record<string, string> = {};
  for (const d of devices ?? []) out[d.id] = d.name;
  return out;
}

/**
 * The buckets in the order they are shown, with their rows.
 *
 * ORDER IS AN ARGUMENT. `behind` and `unreached` come first because they are the
 * two that block promotion and the two an operator can act on; `carrying` and
 * `ahead` are the report. Empty buckets are kept in the list — the caller
 * decides whether to render a zero — because "0 unreached" is a fact worth
 * showing on a screen whose whole subject is which devices were forgotten.
 */
export function bucketRows(plan: DeploymentPlan | undefined): {
  bucket: Bucket;
  rows: DeviceGeneration[];
}[] {
  const p = plan;
  return [
    { bucket: "behind" as Bucket, rows: p?.behind ?? [] },
    { bucket: "unreached" as Bucket, rows: p?.unreached ?? [] },
    { bucket: "carrying" as Bucket, rows: p?.carrying ?? [] },
    { bucket: "ahead" as Bucket, rows: p?.ahead ?? [] },
  ];
}

/**
 * Ink for a bucket.
 *
 * `unreached` is `--indeterminate` and not `--warn`, under the general rule
 * (UI-GUIDE §2): meshcraft has no observation of that device under this
 * credential — it is not asserting that anything is wrong, it is saying it
 * cannot see. `behind` IS `--warn`: that one is observed, it is known to hold an
 * older secret, and it is what stands between the vault and a promotion.
 */
export function bucketInk(b: Bucket): string {
  switch (b) {
    case "carrying":
      return "var(--ok)";
    case "behind":
      return "var(--warn)";
    case "unreached":
      return "var(--indeterminate)";
    default:
      return "var(--text-dim)";
  }
}

/**
 * Ink for one device's outcome in a run.
 *
 * `unconfirmed` is the only `--danger` on this screen, and it earns it: the
 * device took a write that did not read back, which is the outcome that halts
 * the run. `skipped` is `--indeterminate` rather than `--warn` — nothing was
 * attempted, the device carries exactly what it carried, and the reason is
 * printed beside it.
 */
export function outcomeInk(outcome: string): string {
  switch (outcome) {
    case OUTCOME_INSTALLED:
      return "var(--ok)";
    case OUTCOME_REHEARSED:
      return "var(--accent)";
    case OUTCOME_FAILED:
      return "var(--warn)";
    case OUTCOME_UNCONFIRMED:
      return "var(--danger)";
    default:
      return "var(--indeterminate)";
  }
}

/** How far a run has got. `total` is the queue; skipped devices were never in it. */
export function runProgress(st: DeployStatus | undefined): {
  done: number;
  total: number;
} {
  if (!st) return { done: 0, total: 0 };
  const total = st.queued?.length ?? 0;
  const done = Math.min(st.results?.length ?? 0, total);
  return { done, total };
}

/**
 * Did this run end for a reason a human has to read?
 *
 * `stopped` is set by the controller on every early end — cancelled, tripped,
 * halted — and is EMPTY when the run walked its whole queue. A finished run with
 * a `stopped` sentence is not a success with a footnote.
 */
export function runEndedBadly(st: DeployStatus | undefined): boolean {
  return !!st && !st.running && !!st.stopped;
}

/** Verdict of [writeGate], with the reason the screen prints when refused. */
export type WriteGate =
  | { armed: true }
  | {
      armed: false;
      /** i18n key under `credDeployPage`, never a sentence. */
      why:
        | "rehearseFirst"
        | "rehearsalIsOfAnotherGeneration"
        | "rehearsalDidNotFinish"
        | "runInFlight"
        | "nothingToDeploy"
        | "haltedNeedsReview"
        | "perDevice";
    };

/**
 * MAY THE WRITE BUTTON BE PRESSED? — the two-phase rule, enforced here because
 * the API deliberately does not enforce it.
 *
 * `POST …/deploy` accepts `"write": true` on a credential nobody has rehearsed:
 * that is right for the API, whose job is to make the harmless run the DEFAULT
 * rather than to make the dangerous one impossible, and a scripted operator who
 * has already rehearsed by hand must not be blocked by a screen's memory. This
 * function is the screen's own discipline on top of it, and the rules are:
 *
 *   - a rehearsal of THE SAME GENERATION must have finished, cleanly. A
 *     rehearsal that was stopped, tripped or halted proved nothing about the
 *     devices it never reached;
 *   - a run in flight arms nothing — there is one run at a time controller-wide
 *     and the button would 409;
 *   - a WRITE run that finished cleanly re-arms, because a re-run is a resume:
 *     devices that took the secret are recognised from observed state and
 *     skipped (`credDeployEligible`), and a fleet always leaves someone behind;
 *   - a write run that HALTED does not re-arm. The halt means a write was
 *     accepted and did not read back, the next unit of that range will do the
 *     same thing, and the recovery is a human reading the transcript — not the
 *     same button again. Rehearse again, deliberately;
 *   - `per_device` credentials never arm: the runner refuses them outright
 *     (there is no single generation to deploy) and saying so before the click
 *     is better than a 422 the operator has to interpret.
 *
 * The run registry is IN MEMORY, so a controller restart forgets the rehearsal
 * and this gate asks for another one. That is the safe direction to be wrong in.
 */
export function writeGate(args: {
  target: number;
  last: DeployStatus | undefined;
  perDevice: boolean;
}): WriteGate {
  if (args.perDevice) return { armed: false, why: "perDevice" };
  if (args.target <= 0) return { armed: false, why: "nothingToDeploy" };
  const st = args.last;
  if (!st) return { armed: false, why: "rehearseFirst" };
  if (st.running) return { armed: false, why: "runInFlight" };
  if (st.target !== args.target)
    return { armed: false, why: "rehearsalIsOfAnotherGeneration" };
  if (st.stopped)
    return {
      armed: false,
      why: haltedOnUnconfirmed(st) ? "haltedNeedsReview" : "rehearsalDidNotFinish",
    };
  return { armed: true };
}

/**
 * Did this run stop because a write was accepted and did not read back?
 *
 * Read from the RESULTS rather than from the `stopped` sentence: that sentence
 * is prose written for a human, in one language, and a screen that keyed on its
 * wording would silently stop recognising the case the day it is reworded.
 */
export function haltedOnUnconfirmed(st: DeployStatus | undefined): boolean {
  return (st?.results ?? []).some((r) => r.outcome === OUTCOME_UNCONFIRMED);
}

/**
 * Count the outcomes of a run, for the one-line summary above the per-device
 * list. Unknown outcomes are counted under `other` rather than dropped: a new
 * outcome added to the Go side must show up as a number that does not add up
 * rather than vanish from the screen.
 */
export function outcomeCounts(st: DeployStatus | undefined): {
  installed: number;
  rehearsed: number;
  skipped: number;
  failed: number;
  unconfirmed: number;
  other: number;
} {
  const c = { installed: 0, rehearsed: 0, skipped: 0, failed: 0, unconfirmed: 0, other: 0 };
  for (const r of st?.results ?? []) {
    switch (r.outcome) {
      case OUTCOME_INSTALLED:
        c.installed++;
        break;
      case OUTCOME_REHEARSED:
        c.rehearsed++;
        break;
      case OUTCOME_SKIPPED:
        c.skipped++;
        break;
      case OUTCOME_FAILED:
        c.failed++;
        break;
      case OUTCOME_UNCONFIRMED:
        c.unconfirmed++;
        break;
      default:
        c.other++;
    }
  }
  return c;
}

/**
 * Is this credential one the fleet runner can deploy at all?
 *
 * ADMIN ONLY, and that is the runner's shape rather than a UI simplification.
 * `deployToDevice` resolves the device's credential through
 * `credentialForDevice`, which asks the store for kind `admin`; a run started on
 * an SNMP community or a `gnmi` account would therefore find every device
 * resolving to a different credential and skip all of them, after opening
 * nothing and explaining nothing useful. A `gnmi` credential is in addition one
 * meshcraft never pushes — the account already exists on the switch.
 */
export function deployableKind(kind: string): boolean {
  return kind === "admin";
}
