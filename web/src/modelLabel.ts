/**
 * Short display name for dense UI tiles.
 *
 * Full sysDescr-style models (especially Instant On) overflow equipment
 * cards. The switch detail page keeps the stored `model` string; lists and
 * topology nodes show this compact form, with the full string in `title`.
 */

/**
 * The model as a screen must NAME a device: the canonical identity the
 * controller derived (ADR-0014), falling back to the raw declared label when it
 * derived none — an older payload, or a device with no model at all.
 *
 * Distinct from `modelShort` below, and the distinction is the whole point.
 * This one decides WHICH label; that one only makes a label FIT. They compose:
 * `modelShort(modelOf(d), d.vendor)`. Overloading the shortener with identity
 * would break both — it only fires past a length, and it reduces two of the
 * three Instant On forms to "Aruba 1930" but the third to "Aruba JL685A".
 */
export function modelOf(d: { model?: string; model_label?: string }): string {
  return (d.model_label ?? "") || (d.model ?? "");
}

const SHORT_MAX = 28;

/** HPE/Aruba Instant On part numbers: JL685A, JL682A, … */
const RE_ARUBA_SKU = /\b(JL\d{3,4}A|J\d{4}A)\b/i;
/** Compact MikroTik / similar product refs already short enough alone. */
const RE_COMPACT_SKU =
  /\b((?:CRS|CSS|RB|CCR|C[A-Z]{2})\d[\w.+-]*)\b/i;

function brandOf(model: string, vendor?: string): string {
  if (/Instant\s*On/i.test(model)) return "Aruba";
  const v = (vendor ?? "").trim();
  if (v) {
    // "Aruba (HPE)" → "Aruba"; keep a single short token.
    const head = v.split(/[(/]/)[0].trim();
    if (head && head.length <= 16) return head;
  }
  const first = model.trim().split(/\s+/)[0];
  if (first && !/^(HPE|HP|Networking|Switch)$/i.test(first)) return first;
  return "";
}

function skuOf(model: string): string {
  const aruba = model.match(RE_ARUBA_SKU);
  if (aruba) return aruba[1].toUpperCase();
  const compact = model.match(RE_COMPACT_SKU);
  if (compact) return compact[1];
  return "";
}

/**
 * Compact model label for tiles and dense lists.
 * Returns the original string when it already fits or cannot be shortened safely.
 */
export function modelShort(model: string, vendor?: string): string {
  const m = (model ?? "").trim();
  if (!m) return "";
  if (m.length <= SHORT_MAX) return m;

  const brand = brandOf(m, vendor);
  const sku = skuOf(m);
  if (brand && sku) return `${brand} ${sku}`;
  if (sku) return sku;
  if (brand) {
    // Fall back: brand + series number if present (e.g. 1930).
    const series = m.match(/\b(\d{3,4})\b/);
    if (series) return `${brand} ${series[1]}`;
    return brand;
  }
  // Last resort: truncate without inventing a brand.
  return m.slice(0, SHORT_MAX - 1).trimEnd() + "…";
}
