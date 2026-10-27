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

/**
 * The device detail URL is keyed on the device ID, with the name as a
 * readable slug: /{site}/equipment/{name}--{uuid}.
 *
 * The name-only form (/{site}/equipment/{name}) orphaned the page on every
 * rename the page did not perform itself: our own rename moves the URL
 * along (deviceDetailPathAfterRename), but a CONCURRENT rename by another
 * operator rewrites the list under an open page and the old segment matches
 * nothing. The ID is the part that cannot be renamed, so it is the key; the
 * name rides along only for humans reading the address bar.
 */
const DEVICE_SEGMENT_ID =
  /--([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})$/i;

/** Canonical detail path for a device: name slug + "--" + ID. */
export function deviceDetailPath(
  siteSlug: string,
  dev: { id: string; name: string },
): string {
  return sitePath(siteSlug, "equipment", dev.name + "--" + dev.id);
}

/**
 * Split a detail-URL segment into its ID (when the segment carries one) and
 * its name part. Old name-only bookmarks have no ID: `id` comes back null
 * and `name` is the whole decoded segment.
 */
export function parseDeviceSegment(seg: string): {
  id: string | null;
  name: string;
} {
  const decoded = decodeDevicePathSegment(seg);
  const m = decoded.match(DEVICE_SEGMENT_ID);
  if (m) return { id: m[1], name: decoded.slice(0, -m[0].length) };
  return { id: null, name: decoded };
}

/**
 * Resolve a detail-URL segment against the device list: ID first (survives
 * any rename), then name (old bookmarks), then the raw decoded segment as a
 * name — the pathological device whose NAME itself ends in "--{uuid}" would
 * otherwise be split apart and lost.
 */
export function findDeviceBySegment<T extends { id: string; name: string }>(
  devices: T[],
  seg: string,
): T | undefined {
  const parsed = parseDeviceSegment(seg);
  if (parsed.id) {
    const byId = devices.find((d) => d.id === parsed.id);
    if (byId) return byId;
  }
  return (
    devices.find((d) => d.name === parsed.name) ??
    devices.find((d) => d.name === decodeDevicePathSegment(seg)) ??
    // Bare-UUID segment (legacy /s/{site}/sw/{uuid} rewrites): the ID with
    // no slug at all is still an address worth honoring.
    devices.find((d) => d.id === parsed.name)
  );
}

/**
 * Where the device detail page must move when the device NAME changes.
 *
 * The ID keeps the page alive through any rename, but the slug half of the
 * URL would go stale — and an old name-only URL would still be an orphan
 * candidate — so our own rename keeps moving the URL: optimistic move,
 * rollback, server-normalized spelling (see saveDevice in SwitchDetail).
 *
 * Returns the path to navigate to, or null when no move is needed (no
 * rename, or the URL already carries this name AND this ID — a name-only
 * URL is upgraded to the ID-keyed form on the way).
 */
export function deviceDetailPathAfterRename(
  siteSlug: string,
  currentSegment: string,
  deviceId: string,
  newName: string | undefined,
): string | null {
  if (!newName) return null;
  const cur = parseDeviceSegment(currentSegment);
  if (cur.id === deviceId && cur.name === newName) return null;
  return deviceDetailPath(siteSlug, { id: deviceId, name: newName });
}
