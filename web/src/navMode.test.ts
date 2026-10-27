/**
 * The navigation mode — ADR-0013. Run with:
 *   node --experimental-strip-types src/navMode.test.ts
 * (from web/), or `npm test` to run every file at once.
 *
 * Written as the TRAPS it fences off. A mode that hides half the navigation
 * has exactly two ways to be harmful, and neither is cosmetic:
 *
 *   - a deep link that opens a page whose tab is invisible, or worse returns
 *     a 404, so a URL pasted into a ticket is broken for everyone whose mode
 *     is the other one;
 *   - a toggle that lands somewhere senseless, so changing which bar you
 *     want costs you the page you were reading.
 *
 * Everything below is the fence around those two.
 */
import assert from "node:assert/strict";
// Node --experimental-strip-types needs the .ts suffix; tsc excludes this file.
import {
  MODE_TABS,
  destinationOnSwitch,
  defaultSegment,
  modeForPath,
  otherMode,
  parseMode,
  tabsForMode,
} from "./navMode.ts";

// --- the two bars ---------------------------------------------------------

// The exact bars, in bar order — which is also the swipe order, so a change
// here changes what "the next page" means.
//
// Switching carries SIX since the Wi-Fi screen landed (ADR-0016 §4: an access
// point bridges onto a VLAN, so it belongs beside the ports it is plugged
// into). That is one more than the five this list was written with, and it is
// deliberate rather than drift — which is why the ceiling below is asserted
// separately from the contents.
assert.deepEqual(
  tabsForMode("switching").map((t) => t.key),
  ["dashboard", "equipment", "ports", "topology", "clients", "wifi"],
);
assert.deepEqual(
  tabsForMode("routing").map((t) => t.key),
  ["dashboard", "routing", "firewall", "dns", "dhcp"],
);

// THE CEILING, which is the part that actually matters. The mode exists
// because one row of eight does not fit a laptop or a phone's thumb bar
// (ADR-0013, Contexte); eight is precisely what it refuses. Pinning the
// contents above tells you WHAT changed, this tells you whether the change
// gave back the reason the mode was introduced for.
for (const mode of ["switching", "routing"] as const) {
  const n = tabsForMode(mode).length;
  assert.ok(n < 8, mode + " bar has " + n + " entries: that is the overflow the mode exists to prevent");
}

// Every entry declares at least one mode. An entry with none is an entry no
// bar renders, which is the "screen nobody finds" the ADR names.
for (const t of MODE_TABS) {
  assert.ok(t.modes.length >= 1, t.key + " declares no mode");
}
// Keys are unique — they index the icons and the active test.
assert.equal(new Set(MODE_TABS.map((t) => t.key)).size, MODE_TABS.length);
// Every entry names a label key under `nav.`, which is the namespace both
// reference locales carry. An entry with no label is an entry rendered blank.
for (const t of MODE_TABS) {
  assert.match(t.labelKey, /^nav\.[a-zA-Z]+$/, t.key + " has no nav label key");
}

// --- the URL is authoritative (§2) ---------------------------------------

// A deep link into routing says "routing", so the page opens WITH its tab.
assert.equal(modeForPath("/lab/firewall"), "routing");
assert.equal(modeForPath("/lab/routing"), "routing");
assert.equal(modeForPath("/lab/dns"), "routing");
assert.equal(modeForPath("/lab/dhcp"), "routing");
// And symmetrically: a switching link pulls the selector back, so the same
// rule protects a link to Ports pasted by someone who works in routing.
assert.equal(modeForPath("/lab/ports"), "switching");
assert.equal(modeForPath("/lab/clients"), "switching");
assert.equal(modeForPath("/lab/topology"), "switching");
// A device detail page is still the equipment list's mode: the owning segment
// is the first one, not the whole path.
assert.equal(modeForPath("/lab/equipment/sw1--0e4c1b1a-0000-4000-8000-000000000000"), "switching");
// A trailing slash is the same address.
assert.equal(modeForPath("/lab/ports/"), "switching");

// NULL MEANS "DO NOT TOUCH THE MODE", and it is the common answer. The
// dashboard belongs to both bars; configuration and add belong to neither,
// and arriving there must not silently rewrite a preference.
assert.equal(modeForPath("/lab/dashboard"), null);
assert.equal(modeForPath("/lab/config/vlans"), null);
assert.equal(modeForPath("/lab/add/discover"), null);
assert.equal(modeForPath("/config/sites"), null);
assert.equal(modeForPath("/config/users"), null);
// A share link is answered before the session exists — it has no mode at all.
assert.equal(modeForPath("/share/dashboard/sometoken"), null);
// The site root, the app root, and a legacy /s/{uuid}/… bookmark (which the
// router rewrites to a canonical path that IS read here).
assert.equal(modeForPath("/lab"), null);
assert.equal(modeForPath("/"), null);
assert.equal(modeForPath(""), null);
assert.equal(modeForPath("/s/0e4c1b1a-0000-4000-8000-000000000000/ports"), null);
// An unknown segment is not a mode decision either — the catch-all route
// sends it home, and home is mode-agnostic.
assert.equal(modeForPath("/lab/nonsense"), null);
// A site whose SLUG happens to be a segment name does not flip anything: the
// mode is read from the second segment, never the first.
assert.equal(modeForPath("/routing/dashboard"), null);
assert.equal(modeForPath("/firewall/ports"), "switching");

// --- the toggle has a destination ---------------------------------------

// Switching to routing from a switching-only page lands on Routing, not on a
// blank screen (ADR-0013, Consequences).
assert.equal(destinationOnSwitch("/lab/clients", "routing"), "routing");
assert.equal(destinationOnSwitch("/lab/ports", "routing"), "routing");
// And back the other way lands home.
assert.equal(destinationOnSwitch("/lab/dns", "switching"), "dashboard");
assert.equal(defaultSegment("switching"), "dashboard");
assert.equal(defaultSegment("routing"), "routing");

// STAYING PUT is the answer on every shared screen. Yanking someone off the
// VLAN table because they changed which bar they want would be the mode
// filtering more than the navigation, which §1 forbids.
assert.equal(destinationOnSwitch("/lab/dashboard", "routing"), null);
assert.equal(destinationOnSwitch("/lab/config/vlans", "routing"), null);
assert.equal(destinationOnSwitch("/lab/add/discover", "routing"), null);
assert.equal(destinationOnSwitch("/config/sites", "routing"), null);
// Already in the target mode: nothing to do, whatever the page.
assert.equal(destinationOnSwitch("/lab/dns", "routing"), null);
assert.equal(destinationOnSwitch("/lab/ports", "switching"), null);

// --- the toggle ----------------------------------------------------------

// There is no drag left to tell apart from a click (ADR-0031): the selector
// has one gesture and a two-position selector therefore has one destination.
assert.equal(otherMode("switching"), "routing");
assert.equal(otherMode("routing"), "switching");
// An involution — pressing twice is where you started, which is the whole
// contract of a toggle and the reason the mark may stop being a link.
for (const mode of ["switching", "routing"] as const) {
  assert.equal(otherMode(otherMode(mode)), mode);
}

// --- the way home is in both bars ---------------------------------------
//
// LOAD-BEARING SINCE ADR-0031, which took the dashboard link off the brand
// mark on the grounds that the tab already carries it in either mode. If this
// entry ever stops declaring both modes, one mode loses its way home
// entirely and the selector has to become a link again.
const home = MODE_TABS.find((t) => t.key === "dashboard");
assert.ok(home, "no dashboard entry: the mark no longer leads home either");
assert.deepEqual([...home.modes].sort(), ["routing", "switching"]);

// --- persistence ---------------------------------------------------------

assert.equal(parseMode("routing"), "routing");
assert.equal(parseMode("switching"), "switching");
// Anything else is not a mode: a stale key, a hand-edited value, a null from
// a browser with storage switched off. The default answers all three.
assert.equal(parseMode(null), "switching");
assert.equal(parseMode(""), "switching");
assert.equal(parseMode("ROUTING"), "switching");
assert.equal(parseMode("l3"), "switching");

console.log("navMode: ok");
