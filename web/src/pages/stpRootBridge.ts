/**
 * Root-bridge designation: the facts the screen states, kept out of React.
 *
 * A leaf module with NO imports, for the reason `portLabel.ts` is one:
 * `network.ts` pulls in the i18n bundle, so anything living there cannot be
 * exercised by the framework-free node suite (`npm test`). What is here is
 * exactly the reasoning a reviewer would want asserted — when the red flag
 * appears, and how many interfaces stop taking part in spanning tree — and none
 * of it should depend on a component rendering.
 */

/** One device designated as root, as the API carries it. */
export interface RootIntent {
  device_id: string;
  /** MST instance ids. Empty = the single tree of a non-MST mode, or the CIST. */
  instances?: number[];
}

/** The API's answer, from GET/PUT /api/sites/{id}/stp-root-bridges. */
export interface RootBridgeReply {
  roots: RootIntent[];
  /** Instance ids claimed by more than one device. Non-empty ⇒ BPDU filtering. */
  contested?: number[];
  /** driver.RootWarning, displayed VERBATIM. */
  warning?: string;
  /** driver.RootApplyOrder. */
  apply_order?: string;
  /** device id → ports that will stop taking part in spanning tree. */
  filter_ports?: Record<string, string[]>;
}

/**
 * What the screen must show, decided in one place.
 *
 * The three states are NOT "0 / 1 / many roots", and that distinction is the
 * whole reason this function exists rather than a `roots.length > 1` at the call
 * site. Two roots on DIFFERENT MST instances are ordinary, correct MST — two
 * trees, two roots, nothing filtered — and putting a storm warning in front of
 * that would teach an operator to dismiss the warning before the day it means
 * something. The condition is CONTENTION, which only the server can compute,
 * so it is read from the reply and never re-derived here.
 */
export type RootVerdict =
  /** No root designated, or one. Ordinary configuration. */
  | { kind: "quiet" }
  /** Several roots, none contesting the same tree. Normal multi-instance MST. */
  | { kind: "multi-instance"; roots: number }
  /**
   * Two or more roots on ONE tree. The red flag: BPDUs will be filtered, and the
   * operator is asserting there is no other physical path between them.
   */
  | {
      kind: "contested";
      instances: number[];
      warning: string;
      applyOrder: string;
      /** Total interfaces that will stop participating in spanning tree. */
      filteredPorts: number;
      /** Devices carrying at least one filtered port. */
      filteredDevices: number;
      /**
       * True when the server flagged contention but could name no ports.
       *
       * THE CASE MOST EASILY MISREAD, and it must reach the screen as its own
       * state rather than as "0 ports". It means meshcraft can see no path
       * between the roots in the topology it knows — so it will filter NOTHING,
       * and the two switches may well still be merged into one tree by a cable
       * it cannot see. "Nothing to do" and "we do not know what to do" look
       * identical as a zero.
       */
      noKnownPath: boolean;
    };

export function rootVerdict(reply: RootBridgeReply | undefined): RootVerdict {
  const roots = reply?.roots ?? [];
  const contested = reply?.contested ?? [];
  if (contested.length > 0) {
    const filter = reply?.filter_ports ?? {};
    const devices = Object.keys(filter);
    let ports = 0;
    for (const d of devices) ports += (filter[d] ?? []).length;
    return {
      kind: "contested",
      instances: contested,
      // Displayed verbatim from the server. The sentence states an assumption
      // about physical cabling that the operator is accepting, and it must read
      // identically here, in the API response and in the apply transcript —
      // rephrasing it in the frontend is how three slightly different warnings
      // end up in circulation.
      warning: reply?.warning ?? "",
      applyOrder: reply?.apply_order ?? "",
      filteredPorts: ports,
      filteredDevices: devices.length,
      noKnownPath: ports === 0,
    };
  }
  if (roots.length > 1) return { kind: "multi-instance", roots: roots.length };
  return { kind: "quiet" };
}

/**
 * Render an instance list the way an operator reads it.
 *
 * Instance 0 is the CIST and is named as such: "0" alone is the one id whose
 * meaning is not obvious from the number, and it is also the default, so it is
 * the one that will appear most often.
 */
export function instanceLabel(instances: number[] | undefined): string {
  if (!instances || instances.length === 0) return "CIST";
  return instances.map((i) => (i === 0 ? "CIST" : `MST ${i}`)).join(", ");
}
