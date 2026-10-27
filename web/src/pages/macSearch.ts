// MAC-address search, tolerant of every spelling the trade writes one in.
//
// THE PROBLEM. One address has at least five written forms in daily use, and
// which one an operator has in their clipboard depends on where it came from:
//
//     00d86156f55a            a DHCP lease file, a SIEM export
//     00:d8:61:56:f5:5a       Linux, IEEE canonical, our own API
//     00-d8-61-56-f5-5a       Windows ipconfig, 802 print form
//     00.d8.61.56.f5.5a       some NMS exports
//     00d8.6156.f55a          Cisco / FS / EdgeSwitch CLI output
//
// A search box that only matched the form the page happens to RENDER would
// therefore fail for anyone pasting from a switch console — which is most of
// the time one is looking a MAC up at all.
//
// THE RULE. Separators are punctuation, not information: strip them from both
// sides and the five forms collapse onto one key of twelve hex digits. Every
// tolerance then falls out of a single `includes` on that key — case, grouping
// width (2 or 4), separator choice, and PARTIAL input, which matters because
// the fragment a human remembers is a middle slice ("…6156f5…"), not a prefix.
//
// WHAT IS DELIBERATELY NOT TOLERATED: a query that still holds a non-hex
// character once the separators are gone. It cannot be any part of any MAC,
// so the honest answer is "no client matches" — see `MacQuery.impossible`.
// Reducing it to its hex digits instead (so "Apple" searched for "ae")
// would be a search box that quietly answers a different question.

/** Characters that only GROUP an address and never carry any of it. */
const SEPARATORS = /[\s.:_-]+/g;

const HEX_ONLY = /^[0-9a-f]*$/;

/**
 * The comparable form of a stored address: hex digits, lower case, no grouping.
 *
 * Not validated: whatever a bridge learned is what the ledger holds, and a
 * search must not be the place that rejects it.
 */
export function macKey(mac: string): string {
  return mac.replace(SEPARATORS, "").toLowerCase();
}

/**
 * What a typed query reduces to.
 *
 * Three cases and not two, because "nothing typed" and "nothing can match"
 * are opposite answers: the first shows every row, the second shows none.
 */
export type MacQuery =
  | { kind: "off" }
  | { kind: "hex"; term: string }
  | { kind: "impossible" };

/** Reduce raw field input to one of the three cases above. */
export function parseMacQuery(raw: string): MacQuery {
  const term = macKey(raw.trim());
  if (term === "") return { kind: "off" };
  if (!HEX_ONLY.test(term)) return { kind: "impossible" };
  return { kind: "hex", term };
}

/** Does this address satisfy the query? A partial term matches anywhere. */
export function macMatches(mac: string, q: MacQuery): boolean {
  if (q.kind === "off") return true;
  if (q.kind === "impossible") return false;
  return macKey(mac).includes(q.term);
}
