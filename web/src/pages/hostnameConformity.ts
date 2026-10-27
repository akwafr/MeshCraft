/**
 * What the switch-summary panel says about the name the EQUIPMENT carries,
 * kept out of React so each verdict can be asserted directly (cf.
 * portAdmin.ts and poeOverride.ts — same pattern, same reason).
 *
 * # There is no gesture left here, only a sentence
 *
 * The hostname used to be a "push the name" button. It is now RECONCILED
 * (internal/driver/hostname.go): the operator renames the device, the
 * controller writes that name onto the equipment as a consequence, and a
 * backend loop repairs drift on its own with a per-device cooldown. So this
 * screen has exactly one job — say whether the equipment agrees — and no
 * button to offer.
 *
 * # Why five verdicts and not two
 *
 * "The equipment does not carry the name" hides five different situations, and
 * collapsing any two of them costs something real:
 *
 *   ok           — the equipment answers to it. Nothing to say loudly.
 *   drift        — a repair is ALREADY on its way; the operator does nothing.
 *   not_observed — no source has reported: UNKNOWN, not a fault. Shown as
 *                  drift, it would invent an observation nobody made — and
 *                  hostname.go is explicit that nothing is repaired on the
 *                  strength of an absent reading.
 *   unsupported  — the platform has no HostnameSetter, so it can NEVER
 *                  converge. This is THE one that must not be dressed as a
 *                  pending repair: "this range cannot be renamed by
 *                  meshcraft" and "this device is waiting to be renamed"
 *                  are different sentences, and showing the second for the
 *                  first promises a repair that will never come.
 *   unnameable   — `devices.name` cannot become a valid hostname at all
 *                  (accents, punctuation; spaces normalize away fine). The
 *                  ONLY state where a human has to act, so it must not look
 *                  like the four that resolve themselves.
 *
 * # Dependency-free on purpose
 *
 * No i18n, no api.ts: the test file runs under `node
 * --experimental-strip-types` against this module alone. The verdict therefore
 * carries i18n KEY NAMES and a CSS variable, and the panel resolves both —
 * same shape as DiscoveryCredentials' `labelKey`.
 *
 * The COLOUR travels with the verdict rather than being mapped at the call
 * site, because "unsupported must not read as a pending repair" is a visual
 * promise as much as a textual one: a tone table living in the panel would
 * make that promise untestable, and the assertion that these two never share
 * a colour is the cheapest guard against re-introducing the confusion.
 */

/** The five verdicts the controller computes (`hostname_conformity.state`). */
export type HostnameConformityState =
  | "ok"
  | "drift"
  | "not_observed"
  | "unsupported"
  | "unnameable";

/**
 * The controller's verdict on the equipment's own name, served on the device
 * DTO as `hostname_conformity`.
 *
 * Defined HERE and referenced from api.ts (not the other way round) so there
 * is one definition: this module may not import api.ts — it has to stay
 * loadable by a bare node process — and two structural copies of a contract
 * a backend is still shaping would drift apart on the first change.
 */
export interface HostnameConformity {
  /**
   * The name meshcraft intends the equipment to carry: `devices.name`,
   * normalized.
   *
   * EMPTY on `unnameable`, and that is the controller's contract, not an
   * omission (api.hostnameConformityOf): there is nothing meshcraft would push,
   * so a name here would be a name the screen then has to explain away. The
   * consequence for this UI is direct — the `unnameable` sentence must not
   * interpolate it, and it does not.
   */
  desired: string;
  /**
   * The name the equipment actually announces. ABSENT means no source has
   * reported — never "it has no name": an empty string rendered as a value
   * would be a claim we cannot make.
   */
  observed?: string;
  /**
   * WHERE the reading came from. `lldp` is the free one — `lldpLocSysName`
   * arrives on every SNMP poll and opens no session; `read_state` is the
   * connector's own identity read, used where LLDP or SNMP is mute.
   *
   * Worth showing: an operator asking "why does meshcraft think this switch is
   * called that?" is asking about the source, and a verdict with no provenance
   * sends them to the wrong tool.
   */
  source?: "lldp" | "read_state";
  state: HostnameConformityState;
  /**
   * The controller's own sentence, when the state has a reason this screen
   * cannot reconstruct — which character of the name is illegal, which
   * platform cannot be written. Shown VERBATIM: it names the offending
   * character, and paraphrasing it here would lose the only actionable detail.
   */
  because?: string;
}

/** How the panel renders one verdict. */
export interface HostnameVerdict {
  /** i18n key under `switchDetailPage` for the sentence. */
  sentenceKey: string;
  /**
   * CSS variable for the sentence's colour. Dim is not an oversight: `ok` is a
   * confirmation and must not compete with the rest of the panel for
   * attention, and `unsupported` is a permanent platform fact nobody can act
   * on. Those two share dim and are told apart by their sentence — what must
   * never be shared is drift's or unnameable's colour.
   */
  color: string;
  /**
   * Render the "it answers to X" fragment after the sentence. False for
   * `drift`, whose sentence already names BOTH names — that comparison is the
   * whole content of the drift verdict and splitting it in two would read as
   * two unrelated facts.
   */
  observedFragment: boolean;
  /** Append where the reading came from ("as announced over LLDP"). */
  showSource: boolean;
  /**
   * Show `because` when the controller sent one. True exactly where the reason
   * lives server-side: for `ok` and `drift` both names are already on screen
   * and there is nothing left to explain.
   */
  showBecause: boolean;
  /**
   * meshcraft will close this by itself. NEVER true for `unsupported`, which
   * has no write to close it with — the assertion this whole module exists for.
   */
  repairComing: boolean;
  /**
   * The OPERATOR has to do something, and the something is "rename the
   * device". True for `unnameable` alone: every other verdict either resolves
   * itself or cannot be resolved at all, and a screen that asked for action in
   * those cases would send someone looking for a button that is gone.
   */
  needsOperator: boolean;
}

/**
 * The verdict for one `hostname_conformity.state`, or `null` when there is
 * nothing to show.
 *
 * Takes the bare state string, like `adminGesture` takes a link state: the
 * field crosses the wire from a controller this frontend does not ship with,
 * and a SIXTH word must not be forced into one of the five. It answers `null`
 * — the panel stays silent — because inventing a sentence for a state we do
 * not know is worse than saying nothing, and silence is what a reviewer
 * notices when the union needs extending.
 */
export function hostnameVerdict(
  state: string | null | undefined,
): HostnameVerdict | null {
  switch (state) {
    case "ok":
      // Quiet confirmation. The source still rides along: it is the answer to
      // "where did you read that?", which is asked about agreeing devices too.
      return {
        sentenceKey: "hostnameOk",
        color: "var(--text-dim)",
        observedFragment: false,
        showSource: true,
        // NOT on the panel, and that is a change. The controller's sentence
        // on a tolerant match ("announces X — the same name as Y up to
        // letter case and DNS domain, which is not drift") is an
        // EXPLANATION of a verdict nobody has to act on, and UI-GUIDE §3
        // sends those behind a tip rather than onto the screen. It printed
        // three lines under a one-line green verdict.
        //
        // It is not dropped: HostnameRow attaches it as the hover text on
        // the verdict itself, so the operator who sees "SW-01.lan" in an
        // LLDP table and "sw-01" here still gets the reason — one hover
        // away instead of permanently, for every agreeing device on the
        // fleet.
        showBecause: false,
        // Nothing to repair: the two names already agree.
        repairComing: false,
        needsOperator: false,
      };
    case "drift":
      // Accent, the colour this app already uses for "meshcraft has a write
      // that has not landed yet" (sync: pending) — deliberately NOT the orange
      // of a drift banner, because unlike a port drift this one is nobody's
      // problem: the repair is queued and the operator is being informed, not
      // asked.
      return {
        sentenceKey: "hostnameDrift",
        color: "var(--accent)",
        observedFragment: false,
        showSource: true,
        showBecause: false,
        repairComing: true,
        needsOperator: false,
      };
    case "not_observed":
      // --indeterminate, whose documented meaning is exactly this: meshcraft
      // has lost the vantage point and the device may be perfectly fine.
      return {
        sentenceKey: "hostnameNotObserved",
        color: "var(--indeterminate)",
        observedFragment: false,
        // No reading happened, so there is no source to name.
        showSource: false,
        showBecause: true,
        // Nothing is queued: a repair on the strength of an absent reading
        // would queue a write on every unreachable device forever.
        repairComing: false,
        needsOperator: false,
      };
    case "unsupported":
      return {
        sentenceKey: "hostnameUnsupported",
        color: "var(--text-dim)",
        // The observed name is still worth printing when we have one: the
        // device answers to it and will keep doing so. It comes as its own
        // fragment, never folded into a "wants X, has Y" comparison — that
        // phrasing is what would turn a permanent limit into a pending repair.
        observedFragment: true,
        showSource: true,
        showBecause: true,
        repairComing: false,
        // Not actionable HERE. The fix, if any, is a connector that proves the
        // write on hardware — not a gesture on this page.
        needsOperator: false,
      };
    case "unnameable":
      return {
        sentenceKey: "hostnameUnnameable",
        color: "var(--warn)",
        // Nothing was written and nothing was compared: there is no observed
        // name to contrast, only a name to fix.
        observedFragment: false,
        showSource: false,
        showBecause: true,
        repairComing: false,
        needsOperator: true,
      };
    default:
      return null;
  }
}

/**
 * i18n key naming where the reading came from, or `null` when the controller
 * did not say.
 *
 * Null rather than a vague "read from the device": provenance is the point of
 * showing a source at all, and a made-up one would be worse than none.
 */
export function hostnameSourceKey(
  source: string | null | undefined,
): string | null {
  switch (source) {
    case "lldp":
      return "hostnameSourceLldp";
    case "read_state":
      return "hostnameSourceReadState";
    default:
      return null;
  }
}
