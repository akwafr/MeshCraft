/**
 * The decisions the per-port storm-control panel has to get right, kept OUT of
 * React so they can be asserted directly (cf. portAdmin.ts and poeOverride.ts,
 * same pattern and same reason: web/src/network.ts imports i18n and cannot be
 * loaded by the framework-free node test runner).
 *
 * # Everything here exists because a THRESHOLD WITHOUT ITS UNIT IS AMBIGUOUS
 *
 * internal/driver/stormcontrol.go argues it at length: "10" is ten percent of a
 * gigabit on one switch in a rack and ten packets a second on the one beside it,
 * and no connector converts between the two because none of the conversions can
 * be done honestly. The consequences for a SCREEN are three, and all three live
 * in this file:
 *
 *  1. the unit is never implicit. `draftToLimit` refuses a draft whose unit is
 *     not one the platform accepts, rather than defaulting to percent — a
 *     default here would put the ambiguity back in the one place the operator
 *     would never see it;
 *  2. the rate box is never offered for a class the platform cannot express.
 *     `classAvailability` answers that BEFORE a click, from the connector's own
 *     capability declaration, so the operator is not led to type a number that
 *     can only earn a refusal;
 *  3. on a platform with ONE shared rate per port, three rate boxes would let an
 *     operator type three numbers of which two are silently discarded.
 *     `sharedRateConflict` catches the disagreement in the form, before a
 *     request exists.
 */

/** The three traffic classes the contract carries, in canonical order. */
export const STORM_CLASSES = ["broadcast", "multicast", "unknown_unicast"] as const;
export type StormClass = (typeof STORM_CLASSES)[number];

/**
 * What the number next to a limit MEANS.
 *
 * `bps` is BITS per second, not bytes and not kilobits — the same base unit the
 * Go contract uses, so nothing has to be scaled on the way across.
 */
export type StormRateUnit = "percent" | "pps" | "bps";

export interface StormLimit {
  class: StormClass;
  /** false REMOVES the limiter. Never "rate 0", which drops the whole class. */
  enabled: boolean;
  rate?: number;
  unit?: StormRateUnit;
}

/** GET /api/devices/{id}/storm-control → `caps`. */
export interface StormCaps {
  /** The connector was resolved at all. False is "we cannot tell". */
  known: boolean;
  /** Classes the PLATFORM expresses as themselves. Absent = the hardware cannot. */
  classes?: StormClass[];
  /** Units the connector takes without converting. */
  units?: StormRateUnit[];
  /** One rate per port, shared by every class limited on it. */
  shared_rate?: boolean;
  /** The connector declares WritesExtra("storm_control"). */
  write?: boolean;
  why?: string[];
}

/**
 * A limiter the device carries that the contract does NOT model. ADR-0020.
 *
 * It exists because a connector meeting a statement it cannot read had, until
 * now, only bad options — and the one that got written was to skip the line.
 * On a CLI that prints only non-default lines, skipping makes the port report
 * "no limiter configured": a port policed at 100 packets a second was shown as
 * unprotected. This is the third state that was missing, and the panel's job is
 * to put `spelling` in front of the operator rather than a reassuring silence.
 */
export interface StormForeignLimit {
  /**
   * The class this statement touches, set only when it touches EXACTLY one.
   *
   * Absent when it touches more — Arista's "storm-control all" polices every
   * frame on the port, so naming any one class would be as misleading as naming
   * another.
   */
  class?: StormClass;
  /** The device's own line, verbatim. Never paraphrased. */
  spelling: string;
  /** One sentence on why it is not modelled, written by the connector. */
  because: string;
}

/** GET /api/devices/{id}/storm-control → `state`. */
export interface StormState {
  ports?: {
    port: string;
    limits?: StormLimit[];
    foreign?: StormForeignLimit[];
  }[];
  notes?: string[];
}

/**
 * Why a class's control is or is not offered.
 *
 * FOUR VALUES AND NOT TWO, because "the platform cannot" and "meshcraft has not
 * proven the command form yet" are different sentences that lead an operator to
 * opposite conclusions — one is permanent and one is waiting on a lab capture.
 * Collapsing them into a single disabled state is the defect the PoE power-cycle
 * button carried for a while (cf. driver/portadmin.go, § on the name).
 */
export type ClassAvailability =
  /** The connector was never resolved: no answer either way. */
  | "unknown"
  /** The hardware has no limiter for this class. Permanent. */
  | "cannot"
  /** The hardware has it; meshcraft has not run the command form on this range. */
  | "not_verified"
  /** Offer the control. */
  | "available";

/** What the panel may offer for ONE class on this device. */
export function classAvailability(
  caps: StormCaps | undefined,
  cls: StormClass,
): ClassAvailability {
  if (!caps || !caps.known) return "unknown";
  // `classes` absent is NOT an empty list: a connector that declares no
  // capability at all has told us nothing, and rendering that as "this switch
  // cannot" would be a claim about hardware nobody surveyed.
  if (caps.classes === undefined) return "unknown";
  if (!caps.classes.includes(cls)) return "cannot";
  if (!caps.write) return "not_verified";
  return "available";
}

/**
 * The OBSERVED limit for one port and class, or `undefined` when the device
 * never reported it.
 *
 * The three-way answer is the point, exactly as in `observedTrust`: a class
 * absent from a port's list was NOT READ, which is not "no limiter". A panel
 * that collapsed the second into the first would show "unlimited" for a class
 * the connector deliberately declined to guess at.
 */
export function observedLimit(
  state: StormState | undefined,
  port: string,
  cls: StormClass,
): StormLimit | undefined {
  const row = state?.ports?.find((p) => p.port === port);
  return row?.limits?.find((l) => l.class === cls);
}

/**
 * The unmodelled limiter touching ONE class on this port, if the device had one.
 *
 * Entries with no `class` match nothing here, on purpose and for the same reason
 * the Go helper does the same: "storm-control all" answering yes for broadcast
 * would re-make, inside an accessor, the conflation the empty class was chosen
 * to avoid. Those are read off `portForeign` and shown for the whole port.
 */
export function observedForeign(
  state: StormState | undefined,
  port: string,
  cls: StormClass,
): StormForeignLimit | undefined {
  const row = state?.ports?.find((p) => p.port === port);
  return row?.foreign?.find((f) => f.class === cls);
}

/**
 * The unmodelled limiters on this port that belong to NO single class — the ones
 * `observedForeign` deliberately will not return.
 *
 * They are shown once for the port rather than beside a class, because that is
 * exactly what they are: something acting on every class at once.
 */
export function portForeign(
  state: StormState | undefined,
  port: string,
): StormForeignLimit[] {
  const row = state?.ports?.find((p) => p.port === port);
  return (row?.foreign ?? []).filter((f) => !f.class);
}

/** What the operator has typed for one class, before it is judged. */
export interface StormDraft {
  class: StormClass;
  enabled: boolean;
  /** As typed. A string, because an empty box is not the number zero. */
  rate: string;
  unit: StormRateUnit;
}

/**
 * The first unit this platform accepts, for a form that must start somewhere.
 *
 * `undefined` when the platform declares none — and the caller must then offer
 * no rate box at all rather than fall back to percent. A default unit chosen by
 * the frontend is precisely the assumption the whole contract exists to remove:
 * it would be invisible, it would be wrong on two thirds of the fleet, and the
 * operator would have no way to discover it.
 */
export function defaultUnit(caps: StormCaps | undefined): StormRateUnit | undefined {
  return caps?.units?.[0];
}

/**
 * Judge one draft and turn it into the wire form.
 *
 * Mirrors driver.StormLimit.Validate deliberately — the boundary refuses the
 * same bodies with the same reasons — so that an operator meets the refusal in
 * the form, where the field is under their cursor, rather than as a 400 after a
 * round trip.
 */
export function draftToLimit(
  draft: StormDraft,
  caps: StormCaps | undefined,
): { limit?: StormLimit; error?: string } {
  if (!draft.enabled) {
    // A removal carries no rate and no unit. It is a complete instruction, and
    // it must never be rendered as "rate 0": on this fleet a threshold of zero
    // drops every frame of the class, and on broadcast that takes ARP with it.
    return { limit: { class: draft.class, enabled: false } };
  }
  const units = caps?.units ?? [];
  if (!units.includes(draft.unit)) {
    return {
      error: `unit:${draft.unit}`,
    };
  }
  const rate = Number(draft.rate.trim());
  if (draft.rate.trim() === "" || !Number.isFinite(rate) || rate <= 0) {
    return { error: "rate:positive" };
  }
  if (draft.unit === "percent" && rate > 100) {
    return { error: "rate:percent" };
  }
  if (draft.unit !== "percent" && !Number.isInteger(rate)) {
    // Whole packets and whole bits. Rounding here would be the frontend
    // choosing a threshold the operator did not type — the same silent
    // reinterpretation, one layer up.
    return { error: "rate:whole" };
  }
  return { limit: { class: draft.class, enabled: true, rate, unit: draft.unit } };
}

/**
 * On a platform with ONE rate per port, do the enabled drafts disagree?
 *
 * Returns the two conflicting rates, or `undefined`. Caught HERE rather than in
 * the connector because the connector's refusal arrives after the operator has
 * typed two numbers and pressed a button — and a refusal that could have been a
 * disabled control is a refusal that should have been one.
 *
 * A REMOVAL constrains nothing: turning broadcast off while limiting
 * unknown-unicast at 20 is a coherent gesture, not a conflict.
 */
export function sharedRateConflict(
  drafts: StormDraft[],
  caps: StormCaps | undefined,
): [number, number] | undefined {
  if (!caps?.shared_rate) return undefined;
  let first: number | undefined;
  for (const d of drafts) {
    if (!d.enabled) continue;
    const rate = Number(d.rate.trim());
    if (!Number.isFinite(rate) || rate <= 0) continue;
    if (first === undefined) {
      first = rate;
      continue;
    }
    if (rate !== first) return [first, rate];
  }
  return undefined;
}

/**
 * Assemble the POST body for one port, or the first refusal to show.
 *
 * Drafts for classes the platform CANNOT express are dropped rather than sent:
 * the panel does not offer them, so one arriving here is a stale piece of state
 * from a device the operator switched away from, and sending it would earn a
 * skipped line that explains nothing about the gesture they just made.
 */
export function buildStormBody(
  port: string,
  drafts: StormDraft[],
  caps: StormCaps | undefined,
): { body?: { ports: Record<string, StormLimit[]> }; error?: string } {
  const conflict = sharedRateConflict(drafts, caps);
  if (conflict) return { error: `shared:${conflict[0]}:${conflict[1]}` };

  const limits: StormLimit[] = [];
  for (const d of drafts) {
    if (classAvailability(caps, d.class) !== "available") continue;
    const { limit, error } = draftToLimit(d, caps);
    if (error) return { error };
    if (limit) limits.push(limit);
  }
  if (limits.length === 0) {
    // A port named with no limit at all is refused by the API as malformed, so
    // there is nothing to gain by sending it — and "nothing was selected" is a
    // better sentence than a 400.
    return { error: "empty" };
  }
  return { body: { ports: { [port]: limits } } };
}

/**
 * Render an observed limit for display: "5 % of line rate", "1000 packets/s".
 *
 * The unit is SPELT OUT, matching driver.FormatStormRate. A bare number on a
 * panel is the ambiguity this whole feature is built to remove, and it would be
 * reintroduced by the one place nobody thinks of as configuration.
 */
export function formatStormRate(rate: number, unit: StormRateUnit): string {
  switch (unit) {
    case "percent":
      return `${rate} % of line rate`;
    case "pps":
      return `${rate} packets/s`;
    case "bps":
      return `${rate} bit/s`;
  }
}
