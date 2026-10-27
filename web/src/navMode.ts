/**
 * Switching ↔ Routing: the navigation mode — ADR-0013.
 *
 * THE MODE FILTERS THE NAVIGATION AND NOTHING ELSE. It is not a permission,
 * not a data partition, not a site setting: it is a preference of whoever is
 * looking, like the theme. Authorisation stays entirely server-side
 * (ADR-0011), and every screen below is reachable by URL in either mode.
 *
 * No DOM, no React, no sibling imports — the whole decision table is plain
 * data and plain functions so navMode.test.ts can run it under
 * `node --experimental-strip-types` (which strips TYPE imports but would
 * need an explicit extension for a real one).
 */

export type NavMode = "switching" | "routing";

/** Per visitor, like the theme (ADR-0013 §3). */
export const MODE_KEY = "meshcraft.navMode";

/**
 * What someone who has never touched the selector gets. Switching, because
 * that is the product that exists: a routing mode opening on four honest
 * placeholders would be a worse first screen than the one we have.
 */
const DEFAULT: NavMode = "switching";

/**
 * THE ROUTING TABLE OF THE APPLICATION, mode included — ADR-0013's first
 * consequence, as data rather than as a tacit convention. `segment` is the
 * path segment after the site slug; `modes` is which bar the entry appears
 * in, and a screen may legitimately belong to BOTH (the dashboard does, and
 * the equipment list will the day it grows L3 interfaces).
 *
 * A screen absent from this table is a screen no bar leads to. That is the
 * whole point of keeping it in one place.
 */
export type ModeTab = {
  /** Stable key: the icon lookup (ui/NavIcons.tsx) and the active test. */
  key: string;
  /** The path segment under /{siteSlug}/. */
  segment: string;
  /** Written out in full so a translator can grep the key. */
  labelKey: string;
  modes: readonly NavMode[];
};

export const MODE_TABS: readonly ModeTab[] = [
  // The dashboard is in both bars on purpose: it is the site's overview and
  // the brand mark's own destination, so the way home must not depend on
  // which mode the mark is sitting in.
  {
    key: "dashboard",
    segment: "dashboard",
    labelKey: "nav.dashboard",
    modes: ["switching", "routing"],
  },
  { key: "equipment", segment: "equipment", labelKey: "nav.equipment", modes: ["switching"] },
  { key: "ports", segment: "ports", labelKey: "nav.ports", modes: ["switching"] },
  { key: "topology", segment: "topology", labelKey: "nav.topology", modes: ["switching"] },
  { key: "clients", segment: "clients", labelKey: "nav.clients", modes: ["switching"] },
  // Wi-Fi is SWITCHING, and only switching (ADR-0016 §4): an access point
  // bridges frames onto a VLAN, it does not route. Filing it under routing
  // would move it away from the very ports it is plugged into — and since this
  // entry belongs to exactly one mode, `modeForPath` will also flip the
  // selector for anyone who follows a /wifi link pasted into a ticket.
  { key: "wifi", segment: "wifi", labelKey: "nav.wifi", modes: ["switching"] },
  { key: "routing", segment: "routing", labelKey: "nav.routing", modes: ["routing"] },
  { key: "firewall", segment: "firewall", labelKey: "nav.firewall", modes: ["routing"] },
  { key: "dns", segment: "dns", labelKey: "nav.dns", modes: ["routing"] },
  { key: "dhcp", segment: "dhcp", labelKey: "nav.dhcp", modes: ["routing"] },
];

/** The bar for a mode, in bar order — which is also the swipe order. */
export function tabsForMode(mode: NavMode): ModeTab[] {
  return MODE_TABS.filter((t) => t.modes.includes(mode));
}

/**
 * THE URL IS AUTHORITATIVE (ADR-0013 §2). Which mode OWNS this path, or null
 * when the path belongs to both modes or to neither.
 *
 * Null is the common answer and it means "do not touch the mode": the
 * dashboard, every /config and /add screen, the login form and a share link
 * are all mode-agnostic, and a visitor arriving on one of them keeps the
 * mode they had. Only a path a single mode owns moves the selector, which is
 * what makes a link pasted into a ticket open its own tab rather than a page
 * with no tab at all.
 */
export function modeForPath(pathname: string): NavMode | null {
  const parts = pathname.split("/").filter(Boolean);
  // parts[0] is the site slug (or a reserved root like "config"); the segment
  // that names a screen is the second one. A reserved root never has a screen
  // segment that collides with the table below — "config/sites",
  // "share/dashboard", "s/{uuid}/…" all fall through to null, and the legacy
  // /s/ redirect re-navigates to a canonical path which is then read here.
  const seg = parts[1];
  if (!seg) return null;
  const hit = MODE_TABS.find((t) => t.segment === seg);
  if (!hit || hit.modes.length !== 1) return null;
  return hit.modes[0];
}

/**
 * Where a mode lands when the current page does not exist in it. Switching
 * goes home; routing opens on its own first screen, because routing has no
 * dashboard of its own yet and landing on the shared one would make the
 * toggle look like it did nothing.
 */
export function defaultSegment(mode: NavMode): string {
  return mode === "routing" ? "routing" : "dashboard";
}

/**
 * THE TOGGLE HAS A DESTINATION. Returns the segment to navigate to when
 * switching to `next` from `pathname`, or null to stay exactly where we are.
 *
 * Staying is the right answer far more often than it looks: every shared
 * screen (dashboard, configuration, add) is legitimate in both modes, and
 * yanking someone off the VLAN table because they changed which bar they
 * want would be the mode filtering more than the navigation.
 */
export function destinationOnSwitch(
  pathname: string,
  next: NavMode,
): string | null {
  const owner = modeForPath(pathname);
  if (owner === null || owner === next) return null;
  return defaultSegment(next);
}

/* ---------------------------------------------------------------------- *
 * The toggle                                                             *
 * ---------------------------------------------------------------------- *
 * THERE IS NO DRAG ANY MORE — ADR-0031. There used to be: the mark was the
 * knob of a two-position selector AND the way back to the dashboard, so a
 * threshold (24px) and an axis dominance (1.7) decided when a pointer
 * movement had stopped being a click. It was measured, it worked, and the
 * verdict from use was "the switcher is still buggy" — because the only way
 * to change mode was a gesture nothing announced, on an object whose click
 * did something else.
 *
 * So the mark no longer leads anywhere and pressing the selector toggles.
 * Two positions means a toggle has exactly one destination, which is all
 * this function is. The day the mode has three, this is a different control
 * and a different ADR.
 */

export function otherMode(mode: NavMode): NavMode {
  return mode === "routing" ? "switching" : "routing";
}

/* ---------------------------------------------------------------------- *
 * Persistence                                                            *
 * ---------------------------------------------------------------------- */

/** A stored value that is not one of the two modes is not a mode. */
export function parseMode(raw: string | null): NavMode {
  return raw === "routing" || raw === "switching" ? raw : DEFAULT;
}

export function storedMode(): NavMode {
  try {
    return parseMode(localStorage.getItem(MODE_KEY));
  } catch {
    /* private mode, blocked storage — the default is a fine answer */
    return DEFAULT;
  }
}

export function persistMode(mode: NavMode) {
  try {
    localStorage.setItem(MODE_KEY, mode);
  } catch {
    /* the mode is already applied; only the memory of it is lost */
  }
}
