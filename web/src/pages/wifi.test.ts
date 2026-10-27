/**
 * Checks for the Wi-Fi screen's pure decisions — run with:
 *   node --experimental-strip-types src/pages/wifi.test.ts
 * (from web/), or `npm test` to run every file at once.
 *
 * The assertions are written as the FAILURE each one prevents, because every
 * one of them is a way the screen could quietly say something false about
 * somebody's radio plan: a 6 GHz radio shown as 5 GHz, a band tag printed in
 * the vendor's vocabulary beside the same band in ours, an access point
 * declared down because we did not recognise a word we never polled for.
 */
import assert from "node:assert/strict";
// Node --experimental-strip-types needs the .ts suffix; tsc excludes this file.
import type { ExtDevice, ExtSSID, ExtSSIDSettings, SiteExtension } from "../api.ts";
import {
  bandOf,
  bandsOf,
  emptyReason,
  isOnline,
  labelOf,
  networkLabel,
  orphanedExtensions,
  sameBands,
  sortAccessPoints,
  ssidBands,
  ssidEditPayload,
  ssidSwitchKnown,
  uplinkOf,
} from "./wifi.ts";

function ap(over: Partial<ExtDevice> = {}): ExtDevice {
  return {
    id: "1",
    extension_id: "e",
    site_id: "s",
    external_id: "x",
    class: "access_point",
    name: "",
    model: "",
    firmware: "",
    state: "online",
    mgmt_ip: "",
    mac: "",
    radios: [],
    ports: [],
    seen_at: "2026-10-25T00:00:00Z",
    ...over,
  };
}

function ext(over: Partial<SiteExtension> = {}): SiteExtension {
  return {
    id: "e",
    site_id: "s",
    kind: "unifi_os",
    label: "",
    base_url: "https://uos.example",
    has_token: true,
    verify_tls: false,
    transport: "direct",
    enabled: true,
    remote_site_id: "",
    remote_site_ref: "",
    remote_site_name: "",
    paired_at: null,
    orphaned_at: null,
    last_sync_at: null,
    last_error: "",
    created_at: "2026-10-25T00:00:00Z",
    updated_at: "2026-10-25T00:00:00Z",
    ...over,
  };
}

// --- bandOf -----------------------------------------------------------------

// The two values a real AC Pro reports.
assert.equal(bandOf(2.4), "2.4", "2.4 GHz must bucket as 2.4");
assert.equal(bandOf(5), "5", "5 GHz must bucket as 5");
assert.equal(bandOf(6), "6", "6 GHz must bucket as 6");

// THE CASE THAT MATTERS. An unrecognised frequency must NOT be rounded into a
// neighbouring band: showing a 6 GHz radio as 5 GHz would be a quiet lie on the
// screen somebody uses to pick a channel.
assert.equal(bandOf(60), "other", "60 GHz must not be filed as a known band");
assert.equal(bandOf(0), "other", "a zero frequency says nothing, not 2.4");
assert.equal(bandOf(undefined), "other", "an absent frequency must not become a band");

// The 5/6 boundary is the one place this can be wrong and still look plausible.
assert.equal(bandOf(5.8), "5", "5.8 GHz is still the 5 GHz band");
assert.equal(bandOf(5.95), "6", "5.95 GHz belongs to 6 GHz, not 5");

// --- bandsOf ----------------------------------------------------------------

{
  // Each band once, ascending, whatever order the radios arrive in.
  const a = ap({
    radios: [
      { band_ghz: 5, channel: 36 },
      { band_ghz: 2.4, channel: 1 },
      { band_ghz: 5, channel: 44 },
    ],
  });
  assert.deepEqual(bandsOf(a), ["2.4", "5"], "duplicate bands must collapse and sort");
  assert.deepEqual(bandsOf(ap()), [], "a box with no radio radiates on no band");
}

// --- ssidBands --------------------------------------------------------------

// Without this normalisation the screen prints "2g" next to "2.4 GHz" for the
// same thing, and a reader reasonably wonders whether those are two bands.
assert.deepEqual(
  ssidBands({ bands: ["2g", "5g"] } as ExtSSID),
  ["2.4", "5"],
  "the vendor's band tags must be translated into the radios' vocabulary",
);
assert.deepEqual(
  ssidBands({ bands: [" 5G ", "6"] } as ExtSSID),
  ["5", "6"],
  "case and padding must not produce a second band",
);
assert.deepEqual(
  ssidBands({ bands: ["60g"] } as ExtSSID),
  ["other"],
  "an unknown tag stays visible as `other` rather than vanishing",
);
assert.deepEqual(ssidBands({} as ExtSSID), [], "a missing bands array must not throw");

// --- isOnline ---------------------------------------------------------------

// We do not poll these boxes, so only the affirmative case is interpreted;
// everything else is relayed, not judged.
assert.equal(isOnline(ap({ state: "ONLINE" })), true, "the vendor's case must not matter");
assert.equal(isOnline(ap({ state: "online" })), true);
assert.equal(
  isOnline(ap({ state: "adopting" })),
  false,
  "an unrecognised state must not be claimed as up",
);
assert.equal(isOnline(ap({ state: "" })), false, "no state is not up");

// --- uplinkOf ---------------------------------------------------------------

{
  const a = ap({
    ports: [
      { index: 2, state: "down" },
      { index: 1, state: "up", speed_mbps: 1000 },
    ],
  });
  assert.equal(uplinkOf(a)?.index, 1, "the uplink is the lowest port that is actually up");
  assert.equal(
    uplinkOf(ap({ ports: [{ index: 1, state: "down" }] })),
    undefined,
    "nothing up means no uplink, not port 1",
  );
}

// --- sortAccessPoints -------------------------------------------------------

{
  // Natural order, so "AP 10" does not sort between "AP 1" and "AP 2" — the
  // most common complaint about any list of numbered equipment.
  const names = sortAccessPoints([
    ap({ id: "a", name: "AP 10" }),
    ap({ id: "b", name: "AP 2" }),
    ap({ id: "c", name: "AP 1" }),
  ]).map((x) => x.name);
  assert.deepEqual(names, ["AP 1", "AP 2", "AP 10"], "numbers must sort as a human reads them");

  const input = [ap({ id: "a", name: "B" }), ap({ id: "b", name: "A" })];
  sortAccessPoints(input);
  assert.equal(input[0].name, "B", "sorting must not mutate the query cache's array");
}

// --- labelOf ----------------------------------------------------------------

// An unnamed row at the top of a list reads as a bug, so the fallback chain
// always produces something identifiable.
assert.equal(labelOf(ap({ name: "Lobby" })), "Lobby");
assert.equal(labelOf(ap({ name: "  ", model: "AC Pro" })), "AC Pro", "blank name falls back");
assert.equal(labelOf(ap({ mac: "68:d7:9a:dc:ba:7b" })), "68:d7:9a:dc:ba:7b");
assert.equal(labelOf(ap({ external_id: "uuid" })), "uuid", "the import key is the last resort");

// --- emptyReason ------------------------------------------------------------

// "Nothing to show" has four causes, and an empty table explaining none of them
// reads as a broken page. This is the whole reason the extensions travel inside
// the Wi-Fi payload.
assert.equal(emptyReason([ext()], [ap()]), null, "a populated list has no empty reason");
assert.equal(emptyReason([], []), "no-extension");
assert.equal(emptyReason([ext()], []), "not-paired");
assert.equal(emptyReason([ext({ remote_site_id: "r" })], []), "never-synced");
assert.equal(
  emptyReason([ext({ remote_site_id: "r", last_sync_at: "2026-10-25T00:00:00Z" })], []),
  "empty",
  "paired and synced with no access point is a genuine empty, not a setup problem",
);
assert.equal(
  emptyReason(undefined, undefined),
  "no-extension",
  "data still loading must not crash the screen",
);

// --- orphanedExtensions -----------------------------------------------------

// The mirror of an orphaned pairing stays on screen (ADR-0015 §7), so something
// must say it describes a site the controller no longer lists.
{
  const stale = ext({ id: "stale", orphaned_at: "2026-10-25T00:00:00Z" });
  assert.deepEqual(
    orphanedExtensions([ext(), stale]).map((e) => e.id),
    ["stale"],
    "only stale pairings must be surfaced",
  );
  assert.deepEqual(orphanedExtensions(undefined), [], "undefined must not throw");
}

/* --- The SSID editor's payload (ADR-0029) --- */

const live: ExtSSIDSettings = {
  broadcasting: true,
  name: "ARENA",
  hidden: false,
  bands: ["2.4", "5"],
  network_id: "net-lan",
  guest: false,
  client_isolation: false,
  fast_roaming: false,
  bss_transition: true,
  power_save: false,
  multicast_enhance: false,
  proxy_arp: false,
};

// ONLY WHAT MOVED TRAVELS. Sending the whole draft would make `was` assert a
// precondition on eleven settings nobody touched, and any one of them drifting
// would then refuse an edit that had nothing to do with it.
{
  const out = ssidEditPayload(live, { ...live, hidden: true }, "");
  assert.ok(out, "a changed setting must produce a payload");
  assert.deepEqual(out.want, { hidden: true }, "only the changed field may be sent");
  assert.deepEqual(out.was, { hidden: false }, "the precondition must carry the replaced value");
  assert.equal("passphrase" in out, false, "an empty passphrase box must be omitted entirely");
}

// Nothing changed: no payload, so the form can refuse to issue a PUT. A write
// at a live venue is never free.
assert.equal(
  ssidEditPayload(live, { ...live }, ""),
  null,
  "an unchanged form must not produce a write",
);

// THE PASSPHRASE TRAVELS ALONE, with no `was`: the browser is never told the
// current one, so it cannot state a precondition about it. And a passphrase on
// its own is a real edit even when nothing else moved.
{
  const out = ssidEditPayload(live, { ...live }, "  a-new-key  ");
  assert.ok(out, "a passphrase alone must produce a payload");
  assert.equal(out.passphrase, "a-new-key", "the passphrase must be trimmed");
  assert.deepEqual(out.want, {}, "a passphrase-only edit must not claim other changes");
}

// A box of spaces is an empty box: "leave it alone", never "set it to blank".
assert.equal(
  ssidEditPayload(live, { ...live }, "   "),
  null,
  "whitespace must read as an empty passphrase box, not as a value to write",
);

// A SETTING THE CONTROLLER DOES NOT REPORT IS NOT EDITABLE. UniFi omits a key
// sitting at its default, so such a value was never read — and an edit must
// state what it replaces. Offering it would send a guessed precondition and the
// edit would refuse for a reason nobody can see.
{
  const partial: ExtSSIDSettings = { ...live };
  delete partial.proxy_arp;
  assert.equal(ssidSwitchKnown(partial, "proxy_arp"), false, "an absent key is not editable");
  assert.equal(ssidSwitchKnown(partial, "hidden"), true, "a reported key is editable");
  assert.equal(
    ssidEditPayload(partial, { ...partial, proxy_arp: true }, ""),
    null,
    "a setting the controller never reported must not be sent",
  );
}

// Bands compare as a SET: the controller does not promise an order, and an edit
// must not be issued — or refused — because of one.
assert.equal(sameBands(["5", "2.4"], ["2.4", "5"]), true, "band order must not matter");
assert.equal(sameBands(["2.4"], ["2.4", "5"]), false, "a missing band is a difference");
assert.equal(sameBands(undefined, []), true, "undefined and empty must compare equal");
{
  const out = ssidEditPayload(live, { ...live, bands: ["5", "2.4"] }, "");
  assert.equal(out, null, "the same bands in another order is not a change");
  const narrowed = ssidEditPayload(live, { ...live, bands: ["5"] }, "");
  assert.ok(narrowed, "dropping a band is a change");
  assert.deepEqual(narrowed.want.bands, ["5"]);
  assert.deepEqual(narrowed.was.bands, ["2.4", "5"], "the precondition carries the old set");
}

// A network choice reads as what it decides — the VLAN — rather than as a name
// that says nothing about where the traffic lands.
assert.equal(networkLabel("IoT", 42, "untagged"), "IoT — VLAN 42");
assert.equal(
  networkLabel("Default", 0, "untagged"),
  "Default — untagged",
  "VLAN 0 must read as a word, never as the number 0",
);

console.log("wifi: OK");
