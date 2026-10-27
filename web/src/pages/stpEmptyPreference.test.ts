/**
 * The per-site STP off switch, as the SCREEN presents it — run with:
 *   node --experimental-strip-types src/pages/stpEmptyPreference.test.ts
 * (from web/), or `npm test` to run every file at once.
 *
 * There is no function to test here, which is why this file reads the locale
 * bundles directly. The defect it pins was entirely in the words.
 *
 * An empty "Preferred" list is a DELIBERATE SETTING: api/stp_apply.go returns
 * early and sends nothing spanning-tree related to the site. The screen used to
 * label that state "(none — double-click a mode in Available to add)", which
 * presents a switch somebody threw as an absence to fill — and said nothing at
 * all about what else it turns off. Two of those things have no symptom on the
 * equipment:
 *
 *   - per-port intents (portfast/edge, BPDU guard, path cost) ride the same
 *     plan as the mode, so emptying the list to protect one Catalyst's global
 *     mode also stops BPDU guard reaching every access port on the site;
 *   - the MST region stops being re-homogenised when the site's VLAN list
 *     changes. 802.1s treats a mismatched region as an ordinary RSTP
 *     neighbour, so an evicted device keeps forwarding and keeps reading `ok`.
 *
 * The strings are the only place either is told. Assertions are on the
 * SUBSTANCE, not the phrasing — rewrite the sentences freely, but a rewrite
 * that drops one of these has dropped the warning.
 */
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

const load = (lang: string) =>
  JSON.parse(
    readFileSync(
      fileURLToPath(new URL(`../i18n/locales/${lang}.json`, import.meta.url)),
      "utf8",
    ),
  ).spanningTreePage;

for (const lang of ["en", "fr"]) {
  const page = load(lang);

  // The label must not read as a hole to fill.
  assert.ok(
    typeof page.emptyPreferred === "string" && page.emptyPreferred.length > 0,
    `${lang}: spanningTreePage.emptyPreferred is missing`,
  );
  assert.ok(
    !/double-?cli/i.test(page.emptyPreferred),
    `${lang}: the empty state still reads as an instruction to add a mode. ` +
      `An empty Preferred list is the off switch, not an unfinished form: ` +
      `${page.emptyPreferred}`,
  );

  const warn = page.emptyPreferredWarning;
  assert.ok(
    typeof warn === "string" && warn.length > 0,
    `${lang}: spanningTreePage.emptyPreferredWarning is missing — the empty ` +
      `state has no warning at all, and neither of its two invisible costs is ` +
      `told anywhere else in the product`,
  );
  const lower = warn.toLowerCase();
  for (const [needle, why] of [
    ["bpdu guard", "per-port intents stop too, and this is the one whose absence is dangerous and silent"],
    ["mst", "the MST region stops being pushed and stops being re-homogenised"],
    ["rstp", "802.1s demotes an evicted device to an ordinary RSTP neighbour"],
    ["ok", "the eviction has no symptom: the device keeps forwarding and keeps reading ok"],
  ] as const) {
    assert.ok(
      lower.includes(needle),
      `${lang}: the empty-preference warning no longer mentions "${needle}" — ${why}`,
    );
  }
}

console.log("stpEmptyPreference: OK");
