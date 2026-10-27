/**
 * Whether the SUPER ROOT checkbox may be shown, whether it may be clicked, and
 * what has to be said next to it — kept out of React so each verdict can be
 * asserted directly (cf. poeOverride.ts and hostnameConformity.ts, same pattern
 * and same reason).
 *
 * # What Super Root actually is
 *
 * Priority 0 on MST instance 0: the lowest value 802.1D can encode, so this
 * switch wins the root election of its region and nothing can outrank it. On
 * Arista EOS the connector additionally sends "spanning-tree root super", a
 * form nobody has yet run against hardware.
 *
 * # The mistakes this module exists to prevent
 *
 *   - OFFERING IT OUTSIDE MSTP. The contract scopes the field to mstp
 *     (driver.SpanningTreePlan.SuperRoot) and every connector answers a
 *     non-MST plan with a Skipped line. A checkbox visible under rstp is a
 *     control that promises a write the connector will refuse — and a refusal
 *     that lands in a Skipped list nobody opens reads on screen as success.
 *   - OFFERING IT WHERE APPLY CANNOT REACH THE DEVICE. `modes` says which
 *     protocols the RANGE runs; `write` says the connector declares
 *     WritesExtra("stp"). arista-eos — the only platform that implements Super
 *     Root at all — has the first and not the second, so the checkbox must
 *     render disabled there rather than next to a button that answers 422.
 *   - CLEARING IT WITHOUT A PRIORITY. There is NO recorded undo: meshcraft
 *     never stored what the bridge priority was before it wrote 0, and no site
 *     plan carries one. Un-ticking the box alone would leave the switch at
 *     priority 0 while the screen claimed it was an ordinary bridge. So the
 *     clear demands a priority to put back, and the controller enforces the
 *     same rule with a 400 (internal/api/stp_apply.go) — this flag is what
 *     lets the panel ask before the round trip rather than after it.
 *   - READING THE BOX OFF THE TREE. The checkbox renders `stp_super_root`, an
 *     INTENT. An observed priority of 0 with the intent false means somebody
 *     set it by hand, which is worth a warning and is NOT worth ticking the
 *     box: doing so would make meshcraft claim a decision it never took, and
 *     the next clear would "restore" a value it never chose.
 *
 * # Dependency-free on purpose
 *
 * No i18n, no api.ts: the test runs under `node --experimental-strip-types`
 * against this module alone. The verdict therefore carries i18n KEY NAMES and
 * the panel resolves them — same shape as hostnameConformity's `sentenceKey`.
 */

/** The few observed spanning-tree fields the verdict reads. */
export interface SuperRootObserved {
  enabled?: boolean;
  mode?: string;
  bridge_priority?: number;
  is_root?: boolean;
}

/** Everything the panel knows when it draws the row. */
export interface SuperRootInput {
  /** The mode currently selected in the mode `<select>`, not the observed one. */
  mode: string;
  /** Connector Capabilities.STPModes, as served on `stp_caps.modes`. */
  modes: string[];
  /** The operator holds site-engineer rights on this site. */
  canEdit: boolean;
  /** The connector declares WritesExtra("stp") — `stp_caps.write`. */
  writable: boolean;
  /**
   * The connector IMPLEMENTS Super Root — `stp_caps.super_root`
   * (driver.FeatSTPSuperRoot).
   *
   * SEPARATE FROM `writable`, and the separation is the whole point. `writable`
   * means "this connector's STP writes are declared"; this means "this connector
   * implements THIS field". cisco/ios has the first and not the second — it
   * declares stp and deliberately refuses Super Root with a Skipped line — so a
   * gate on `writable` alone RENDERED THE CHECKBOX ENABLED ON A LIVE CATALYST.
   * Ticking it there did nothing; UN-ticking it then went through the clear
   * path, which demands a replacement priority and which cisco/ios DOES honour —
   * so undoing a no-op would have written a bridge priority to a production
   * switch and quietly cost it a root election it was holding on purpose.
   */
  implementsSuperRoot: boolean;
  /** The stored INTENT (`device.stp_super_root`), never inferred. */
  stored: boolean;
  /** The checkbox's current value, which may differ from `stored`. */
  checked: boolean;
  /** Last observation, or undefined when the device has never been read. */
  observed?: SuperRootObserved;
}

export interface SuperRootVerdict {
  /** Draw the row at all. */
  show: boolean;
  /** Draw it greyed: visible so the capability is discoverable, not clickable. */
  disabled: boolean;
  /**
   * The clear needs an explicit bridge priority before Apply may be pressed —
   * see the header. True only on the transition from stored-true to
   * unchecked.
   */
  needsPriority: boolean;
  /** i18n key of a sentence to show under the box, or "" when there is none. */
  warnKey: string;
}

/** Priorities the clear-side selector offers: the 802.1D step, nothing else. */
export const SUPER_ROOT_PRIORITIES = [
  4096, 8192, 16384, 24576, 32768, 40960, 49152,
];

/** The 802.1D default, and the value the clear-side selector starts on. */
export const DEFAULT_BRIDGE_PRIORITY = 32768;

export function superRootVerdict(input: SuperRootInput): SuperRootVerdict {
  const hidden: SuperRootVerdict = {
    show: false,
    disabled: true,
    needsPriority: false,
    warnKey: "",
  };
  // MSTP AND NOTHING ELSE, on both counts: the mode about to be applied must be
  // mstp, and the platform must actually offer mstp. A platform that does not
  // list it can never be put in a state where the field means anything, so the
  // row would be permanently decorative.
  if (input.mode !== "mstp") return hidden;
  if (!input.modes.includes("mstp")) return hidden;
  // A CONNECTOR THAT DOES NOT IMPLEMENT THE FIELD NEVER SHOWS THE ROW — not
  // even greyed. Greying it out would still leave the stored intent reachable
  // through the clear path on a platform that never set anything.
  if (!input.implementsSuperRoot) return hidden;

  // BOTH capabilities, and in this order: the connector must implement the field
  // AND its STP writes must be declared. Today arista-eos is the only
  // implementer and its WritesExtra("stp") is false, so the row renders visible
  // and disabled — which is the honest state, and it flips the day a capture
  // lifts that declaration.
  const disabled = !input.canEdit || !input.writable;
  const needsPriority = input.stored && !input.checked;

  // WARNINGS DO NOT HIDE THE ROW. Both cases below are situations an operator
  // opens this panel to resolve, and a control that disappears exactly when the
  // topology is surprising is the opposite of useful.
  let warnKey = "";
  const obs = input.observed;
  if (obs && obs.enabled !== false) {
    if (!input.stored && obs.bridge_priority === 0) {
      // Priority 0 with no meshcraft intent behind it: somebody set it by hand.
      // Said, not adopted — ticking the box here would record a decision
      // meshcraft never took.
      warnKey = "stpSuperRootWarnManual";
    } else if (input.checked && obs.is_root === false) {
      // The intent says root and the switch says it is not. Under MST that is
      // usually another bridge already at priority 0, where the tie-break is
      // the MAC address and there is no knob left (cf. the contract).
      warnKey = "stpSuperRootWarn";
    }
  }

  return { show: true, disabled, needsPriority, warnKey };
}
