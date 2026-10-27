/**
 * OPTICAL CAGES: turning `optics_state` into what one port's panel may claim.
 *
 * Out of React so the distinctions below can be asserted directly
 * (optics.test.ts), the same reason `portFacts.ts` and `facade.ts` are plain
 * modules. These are not formatting helpers — they are the four answers this
 * feature is allowed to give, and merging any two of them puts a reading on
 * screen that the device never made.
 *
 * # The four states, and why none of them is "0.00 dBm"
 *
 * The API keeps three absences apart and this module keeps them apart on
 * screen (cf. `internal/driver/optics.go`, `internal/api/optics.go`):
 *
 *   1. NO OBSERVATION AT ALL — `optics_state` is absent. The device was never
 *      read, or its connector cannot read transceivers; the wire does not
 *      separate those and neither do we. Nothing was claimed about any cage,
 *      so this is "we have never looked" → `--text-dim`, and the panel does
 *      not render the section at all rather than print a permanent blank on
 *      every SFP port of every device in the fleet.
 *   2. AN EMPTY CAGE — an observation exists and this port is not in it.
 *      `ports` lists only cages a module was seen in, so this is a positive
 *      finding: there is no transceiver here. A plain fact, no colour.
 *   3. A CAGE THAT COULD NOT BE READ — the connector's per-port command
 *      failed, it kept the partial result and named the port in `notes`. This
 *      is the one state `--indeterminate` was minted for: the cage exists, we
 *      have no vantage point on it, and what is in it may be perfectly fine.
 *      It looks EXACTLY like an empty cage in `ports`, which is why the note
 *      is parsed rather than ignored — reporting "no module" about a port we
 *      failed to read would be inventing the emptiest possible reading.
 *   4. A MODULE — and then `ddm` splits again: a module that declares no
 *      diagnostics measures nothing and never will while it sits in that
 *      cage, which is a property of the transceiver, permanent, and calls for
 *      no action. Neutral, per UI-GUIDE §2's "this platform cannot do it"
 *      row, NOT yellow. A module that DOES declare diagnostics and still
 *      served no value for one reading is yellow: §2 names "a reading that
 *      has stopped arriving" as the token's own example.
 *
 * And the trap the whole file exists for: **the readings are optional because
 * 0.0 dBm is a legal power**. `rx_power_dbm ?? 0` would render an unserved
 * reading and a genuine zero identically. Nothing here defaults a reading.
 *
 * # No threshold colour, deliberately
 *
 * The module also publishes alarm/warning bounds — the "DDM Thresholds"
 * table — and `fs/s3400/optics.go` says in its header that it deliberately
 * does not parse them. The API therefore cannot tell anyone whether a reading
 * is in range, so no function here returns `--ok`, `--warn` or `--danger` for
 * a VALUE. Showing a raw number with its unit is honest; painting it green
 * against a limit we do not have is not.
 */
import type { OpticsPortState, OpticsState } from "../../api";

/** What one port's optical cage is, as far as this device view can say. */
export type OpticsCage =
  /** No observation exists on this device — never read, or unreadable here. */
  | { kind: "unread" }
  /** Read, and no module was found in this cage. */
  | { kind: "empty" }
  /** Read FAILED for this cage. `reason` is the connector's own words, "" if it gave none. */
  | { kind: "blind"; reason: string }
  /** A module is in the cage. */
  | { kind: "module"; module: OpticsPortState };

/**
 * The prefix `driver.ReadOptics` puts on its partial-failure note, verbatim:
 * `"not read: " + strings.Join(failures, " ; ")`, each failure being
 * `fmt.Sprintf("%s: %v", port, err)` with the FACADE port name.
 *
 * Matching the shape rather than searching for the port name anywhere in the
 * text is what keeps `XG1` from matching a note about `XG10`, and what keeps
 * an error message that happens to mention another port from being read as a
 * failure of that port.
 */
const NOT_READ_PREFIX = "not read:";

/**
 * The connector's reason for failing to read THIS cage, or null when it did
 * not report one — which, given the note's shape, means the cage was read
 * successfully and is simply empty.
 *
 * Returns "" for a failure with an empty reason: that is still a failure, and
 * the caller must not confuse `""` with `null`.
 */
export function opticsReadFailure(
  notes: string[] | undefined,
  port: string,
): string | null {
  for (const note of notes ?? []) {
    const text = note.trim();
    if (!text.toLowerCase().startsWith(NOT_READ_PREFIX)) continue;
    for (const entry of text.slice(NOT_READ_PREFIX.length).split(" ; ")) {
      const one = entry.trim();
      if (one.startsWith(port + ":")) {
        return one.slice(port.length + 1).trim();
      }
    }
  }
  return null;
}

/** Which of the four states this port's cage is in. */
export function opticsCage(
  state: OpticsState | undefined,
  port: string,
): OpticsCage {
  if (!state) return { kind: "unread" };
  const module = (state.ports ?? []).find((x) => x.port === port);
  if (module) return { kind: "module", module };
  const failure = opticsReadFailure(state.notes, port);
  if (failure !== null) return { kind: "blind", reason: failure };
  return { kind: "empty" };
}

/**
 * The ink on the cage's own line (UI-GUIDE §2's ladder, walked in order).
 *
 *	unread  --text-dim        we have never looked. Nothing was claimed;
 *	blind   --indeterminate   the cage exists and we have no vantage point
 *	                          on it — the one state yellow is for;
 *	empty   no colour         a positive observation: there is no module
 *	                          here. "Nothing in the cage" is an answer, not
 *	                          an absence of one;
 *	module  no colour         a fact. The identity carries itself.
 *
 * `undefined` rather than `var(--text)` for the two uncoloured cases: the
 * caller spreads this into an inline style, and no colour is what an
 * unremarkable fact looks like on this page (same convention as `trustObsInk`).
 */
export function opticsCageInk(kind: OpticsCage["kind"]): string | undefined {
  if (kind === "unread") return "var(--text-dim)";
  if (kind === "blind") return "var(--indeterminate)";
  return undefined;
}

/**
 * The ink on ONE MISSING reading, which depends entirely on whether the module
 * promised it.
 *
 * A module that declares DDM and then serves no receive power is a reading
 * that stopped arriving — UI-GUIDE §2 names that case as `--indeterminate`
 * outright. A module that answered "DDM:NO" never promised anything: its
 * silence is a property of the hardware, permanent while it sits in that cage,
 * and painting it yellow would say meshcraft lost a vantage point it never had.
 *
 * Returns `undefined` for a reading that IS there — a value needs no ink, and
 * the one ink it must never get is a verdict about being in range, which
 * nothing in this product can compute (see the file header).
 */
export function opticsReadingInk(
  value: number | null | undefined,
  ddm: boolean,
): string | undefined {
  if (value != null) return undefined;
  return ddm ? "var(--indeterminate)" : "var(--text-dim)";
}

/**
 * A reading with its unit, or null when there is no reading.
 *
 * NULL AND NOT "0", and not "—" either: the caller has to choose a sentence
 * for the absence, and returning a string here is how "not served" would
 * quietly become a value. The number is printed as the device reported it,
 * digit for digit — no rounding, no padding to two decimals — so a genuine
 * `0` reads as `0 dBm` and cannot be mistaken for a formatted placeholder.
 *
 * `sep` is the decimal separator, passed in rather than read from i18n so this
 * module stays importable by the plain-node test suite (`decimalSeparator()`
 * lives in network.ts, which pulls in i18next).
 */
export function opticsReading(
  value: number | null | undefined,
  unit: string,
  sep: string,
): string | null {
  if (value == null) return null;
  return String(value).replace(".", sep) + " " + unit;
}

/** Does this module carry any DDM value at all, whatever its `ddm` flag says? */
export function opticsHasReading(m: OpticsPortState): boolean {
  return (
    m.tx_power_dbm != null ||
    m.rx_power_dbm != null ||
    m.temperature_c != null ||
    m.voltage_v != null ||
    m.bias_current_ma != null
  );
}

/**
 * The folded section's one-line summary — the panel's promise that a closed
 * section is still a readable answer.
 *
 * Receive power is the line, when there is one: it is the number an operator
 * opens an optics panel to read, and the only one that says anything about the
 * fibre rather than about the module. Everything else is a state word, and
 * each of the four states gets its own — a collapsed section must never say
 * the same thing about an empty cage and a cage nobody could read.
 */
export function opticsMeta(
  cage: OpticsCage,
  t: (key: string, opts?: Record<string, unknown>) => string,
  sep: string,
): string {
  if (cage.kind === "unread") return t("switchDetailPage.opticsMetaUnread");
  if (cage.kind === "empty") return t("switchDetailPage.opticsMetaEmpty");
  if (cage.kind === "blind") return t("switchDetailPage.opticsMetaBlind");
  const rx = opticsReading(cage.module.rx_power_dbm, "dBm", sep);
  if (rx) return t("switchDetailPage.opticsMetaRx", { rx });
  // No receive power. Say WHY there is none rather than falling back to the
  // part number: "module, no diagnostics" is the answer to the question the
  // missing number just raised.
  if (!cage.module.ddm && !opticsHasReading(cage.module)) {
    return t("switchDetailPage.opticsMetaNoDdm");
  }
  const name = cage.module.part_number || cage.module.vendor;
  return name ? name : t("switchDetailPage.opticsMetaModule");
}
