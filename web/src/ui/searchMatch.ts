// The one matcher behind every searchable list in this UI.
//
// Two selectors needed the same three decisions — strip the accents, split the
// query into words, require every word somewhere in the row — and each had its
// own half of them: `SearchSelect` folded diacritics and matched word-wise,
// `SiteSelect` did neither (a raw `toLowerCase().includes()` on the whole
// query, so "Saint Étienne" was unreachable from a keyboard without accents
// and "etienne nord" matched nothing at all). The rule is a PRODUCT
// behaviour, not a component's, so it lives once.
//
// WHY THE HAYSTACK IS PREPARED SEPARATELY.
//
// Folding is the expensive half: `normalize("NFD")` allocates a new string and
// walks it, and the naive shape folds every row on every keystroke — a
// 400-site instance pays 400 normalisations per character typed, which is
// exactly when the list has to feel instant. `foldRows` is memoised on the
// LIST, `filterFolded` only folds the query. Typing then costs n substring
// scans over strings that are already lowercase and already stripped.

/**
 * Lowercase and strip diacritics, so a keyboard without accents still finds a
 * French name.
 */
export function fold(s: string): string {
  return s
    .normalize("NFD")
    .replace(/[\u0300-\u036f]/g, "")
    .toLowerCase();
}

/** A row with its searchable text already folded. Build with `foldRows`. */
export interface FoldedRow<T> {
  item: T;
  hay: string;
}

/**
 * Fold each row's searchable text once. Call inside a `useMemo` keyed on the
 * list — never on the query, which is the whole point.
 *
 * `hayOf` returns everything a word may match, joined by spaces: the visible
 * label, the detail line, and any text that is searchable WITHOUT being shown
 * (ADR-0010 — the reset procedures' `Aliases`, the numbers somebody reads off
 * a lid; a site's slug, which is its URL identity).
 */
export function foldRows<T>(items: T[], hayOf: (item: T) => string): FoldedRow<T>[] {
  return items.map((item) => ({ item, hay: fold(hayOf(item)) }));
}

/** The query, folded and split. Empty when nothing was typed. */
export function queryWords(query: string): string[] {
  return fold(query).split(/\s+/).filter(Boolean);
}

/**
 * Word-wise AND over the prepared haystacks — the same rule as `resetMatches`
 * in `internal/discovery/reset.go`, because the reset screen now filters in
 * the browser what the server used to filter (ADR-0010). Substring-on-the-
 * whole-query matched nothing for "cisco 2960", which is how a person types.
 *
 * An empty query returns the original items, same order, no copy of the row
 * objects — the common case costs one map.
 */
export function filterFolded<T>(rows: FoldedRow<T>[], query: string): T[] {
  const words = queryWords(query);
  if (words.length === 0) return rows.map((r) => r.item);
  const out: T[] = [];
  for (const r of rows) {
    if (words.every((w) => r.hay.includes(w))) out.push(r.item);
  }
  return out;
}
