/**
 * The shared search matcher — run with:
 *   node --experimental-strip-types src/ui/searchMatch.test.ts
 * (from web/)
 *
 * WHY THIS FILE EXISTS. Two selectors held two different halves of the same
 * rule. `SearchSelect` folded diacritics and ANDed the words; `SiteSelect`
 * did neither, so "Saint-Étienne" was unreachable from a keyboard without
 * accents and "etienne nord" — which is how a person narrows two candidates
 * to one — matched nothing at all. The rule is now one module, and the two
 * behaviours that were missing are pinned below so a future "simplification"
 * back to `toLowerCase().includes(query)` fails here rather than in the field.
 */
import assert from "node:assert/strict";
import { filterFolded, fold, foldRows, queryWords } from "./searchMatch.ts";

interface Row {
  name: string;
  slug: string;
}

const sites: Row[] = [
  { name: "Saint-Étienne Nord", slug: "st-etienne-nord" },
  { name: "Saint-Étienne Sud", slug: "st-etienne-sud" },
  { name: "Lyon Part-Dieu", slug: "lyon-pd" },
  { name: "Siège", slug: "hq" },
];

const rows = foldRows(sites, (s) => `${s.name} ${s.slug}`);
const hits = (q: string) => filterFolded(rows, q).map((s) => s.slug);

// --- fold ------------------------------------------------------------------
assert.equal(fold("Saint-Étienne"), "saint-etienne");
assert.equal(fold("Siège"), "siege");
assert.equal(fold("Lyon Part-Dieu"), "lyon part-dieu");

// --- queryWords ------------------------------------------------------------
assert.deepEqual(queryWords("  etienne   nord "), ["etienne", "nord"]);
assert.deepEqual(queryWords("   "), []);

// --- an empty query is every row, in order, not a filtered subset ----------
assert.deepEqual(hits(""), ["st-etienne-nord", "st-etienne-sud", "lyon-pd", "hq"]);
assert.equal(filterFolded(rows, "   ").length, 4);

// THE FIRST MISSING HALF: an accented name, found from a keyboard that has no
// accents. This is the common case on a technical keyboard and it returned
// nothing.
assert.deepEqual(hits("etienne"), ["st-etienne-nord", "st-etienne-sud"]);
assert.deepEqual(hits("siege"), ["hq"]);
// …and still found when the accent IS typed.
assert.deepEqual(hits("Étienne"), ["st-etienne-nord", "st-etienne-sud"]);

// THE SECOND MISSING HALF: the words are ANDed, not matched as one substring.
// No single field contains "etienne nord", so a whole-query `includes` found
// nothing — and the word order must not matter either.
assert.deepEqual(hits("etienne nord"), ["st-etienne-nord"]);
assert.deepEqual(hits("nord etienne"), ["st-etienne-nord"]);

// Text that is searchable without being the visible label — a site's slug,
// which is its URL identity (and, on the reset screen, ADR-0010's aliases).
assert.deepEqual(hits("lyon-pd"), ["lyon-pd"]);
assert.deepEqual(hits("hq"), ["hq"]);

// One absent word is enough to reject the row: AND, not OR. An OR here would
// make every multi-word query WIDEN the list, which is the opposite of what
// typing more is for.
assert.deepEqual(hits("etienne marseille"), []);

// The prepared haystack is folded ONCE, at `foldRows` — that is the whole
// performance argument, so it is pinned: the row carries lowercase,
// accent-stripped text, not the original.
assert.equal(rows[0].hay, "saint-etienne nord st-etienne-nord");
assert.equal(rows[3].hay, "siege hq");

console.log("searchMatch.test.ts: ok");
