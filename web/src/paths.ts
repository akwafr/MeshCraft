/** Readable SPA paths: /{siteSlug}/equipment/… — API still uses site UUIDs. */

export const LAST_SITE_KEY = "meshcraft.lastSite";

/** First-path segments that are never site slugs. */
export const RESERVED_SITE_SLUGS = new Set([
  "config",
  "login",
  "api",
  "assets",
  "static",
  "health",
  "metrics",
  "s",
  "admin",
  "auth",
  "well-known",
]);

/** Build a site-scoped path: sitePath("lab", "equipment") → "/lab/equipment". */
export function sitePath(slug: string, ...segments: string[]): string {
  const parts = [slug, ...segments]
    .filter((s) => s != null && s !== "")
    .map((s) => encodeURIComponent(s));
  return "/" + parts.join("/");
}

/** Encode a device name for the path (names are unique per site). */
export function devicePathSegment(name: string): string {
  return encodeURIComponent(name);
}

export function decodeDevicePathSegment(seg: string): string {
  try {
    return decodeURIComponent(seg);
  } catch {
    return seg;
  }
}
