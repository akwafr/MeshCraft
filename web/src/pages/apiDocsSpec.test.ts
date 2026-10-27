/**
 * The API reference's table — run with:
 *   node --experimental-strip-types src/pages/apiDocsSpec.test.ts
 * (from web/)
 *
 * The table is hand-written and no build step can check it against
 * `internal/api/api.go`. What CAN be checked is that it never says something
 * dangerous by omission, and that is what this file pins: every row declares
 * who may call it, and the subtrees whose gate IS a security property keep
 * it. A row with a missing gate teaches a reader that the route is open.
 */
import assert from "node:assert/strict";
import {
  countRoutes,
  filterGroups,
  GROUPS,
  quickstart,
  REFUSALS,
} from "./apiDocsSpec.ts";

const all = GROUPS.flatMap((g) => g.routes);

// --- no row may be silent about its gate ----------------------------------
assert.deepEqual(all.filter((r) => !r.gate), []);

// A site gate with no minimum role says "a site" and not "which right" —
// which is the half the reader came for.
const siteGates = ["site-viewer", "site-operator", "site-admin", "device-site"];
assert.deepEqual(
  all.filter((r) => siteGates.includes(r.gate) && !r.role).map((r) => `${r.m} ${r.p}`),
  [],
);

// --- the two gates that are security properties ---------------------------
// User administration is global-admin, all of it.
const users = all.filter((r) => r.p.startsWith("/api/users"));
assert.ok(users.length > 0);
assert.ok(users.every((r) => r.gate === "global-admin"));

// ADR-0011: the token routes live under /api/auth/ with the other
// credentials, and no route takes a user id — there is no way to reach
// somebody else's tokens, for anybody.
const tokenRoutes = all.filter((r) => r.p.startsWith("/api/auth/tokens"));
assert.equal(tokenRoutes.length, 3);
assert.deepEqual(all.filter((r) => /\/tokens/.test(r.p) && /\{userID|\/users\//.test(r.p)), []);

// THE READ IS OPEN TO A TOKEN, THE TWO WRITES ARE NOT. `auth.CookieOnly`
// guards create and revoke, because a token able to mint another one outlives
// its own expiry and its own revocation. A document that showed all three the
// same way would be hiding the one refusal a script author must design around.
assert.equal(tokenRoutes.find((r) => r.m === "GET")?.gate, "self");
assert.deepEqual(
  tokenRoutes.filter((r) => r.m !== "GET").map((r) => r.gate),
  ["cookie-only", "cookie-only"],
);

// Nothing is left under the path this group used to be documented at, back
// when its handler did not exist and the shape was written from the ADR.
assert.deepEqual(all.filter((r) => r.p.startsWith("/api/me/")), []);

// Anchors are ids: a duplicate would make one section unreachable from the
// index at the top of the page.
const ids = GROUPS.map((g) => g.id);
assert.equal(new Set(ids).size, ids.length);

// --- the search -----------------------------------------------------------
// An empty query is the table itself, untouched and un-copied.
assert.equal(filterGroups(GROUPS, ""), GROUPS);
assert.equal(countRoutes(filterGroups(GROUPS, "   ")), countRoutes(GROUPS));

// The three things somebody actually types: a verb, a path fragment, a gate.
const deletes = filterGroups(GROUPS, "delete").flatMap((g) => g.routes);
assert.ok(deletes.length > 0);
assert.ok(deletes.every((r) => r.m === "DELETE"));

const vlans = filterGroups(GROUPS, "VLANS").flatMap((g) => g.routes);
assert.ok(vlans.length > 0);
assert.ok(vlans.every((r) => r.p.includes("vlans")));

const admin = filterGroups(GROUPS, "global-admin").flatMap((g) => g.routes);
assert.ok(admin.length > 0);
assert.ok(admin.every((r) => r.gate === "global-admin"));

// The words AND across the method and the path together — the query that
// narrows to one row.
const one = filterGroups(GROUPS, "post vlans").flatMap((g) => g.routes);
assert.equal(one.length, 1);
assert.equal(one[0].p, "/api/sites/{siteID}/vlans");

// A group whose routes all failed the filter disappears entirely, rather than
// rendering as an empty card.
assert.deepEqual(filterGroups(GROUPS, "post vlans").map((g) => g.id), ["vlans"]);
assert.deepEqual(filterGroups(GROUPS, "zzzznope"), []);

// --- the refusals ---------------------------------------------------------
// These strings are matched (or at least shown) by a client, so they are the
// server's verbatim sentences and a paraphrase here would be a paraphrase of
// what the reader has in their terminal.
assert.ok(REFUSALS.length >= 7);
assert.ok(REFUSALS.every((r) => r.text.length > 0 && r.d.startsWith("apiDocs.refusal")));
// A 401 says "get a new credential", a 403 says "this will never work" — the
// split is the whole point of listing them, so no refusal may be outside it.
assert.ok(REFUSALS.every((r) => [401, 403, 409].includes(r.status)));
// The three indistinguishable 401s are listed as the ONE sentence the server
// actually sends; if this ever becomes three different texts, the server
// started leaking which half of a guess was right.
assert.deepEqual(
  REFUSALS.filter((r) => r.status === 401).map((r) => r.text),
  ["invalid token", "token revoked", "token expired"],
);

// --- the quickstart -------------------------------------------------------
const qs = quickstart("https://mesh.example.net");
assert.ok(qs.includes("$MESHCRAFT_TOKEN"));
assert.ok(qs.includes("Authorization: Bearer"));
// It must be copy-pasteable against the instance the reader is looking at.
assert.ok(qs.includes("https://mesh.example.net/api/auth/me"));
// And it must never SEND the one thing ADR-0011 forbids. (It is allowed to
// mention cookies in a comment — it says the token needs none — so what is
// pinned is the absence of the mechanism, not of the word: no `Cookie:`
// header, and neither of curl's two cookie flags.)
assert.ok(!/Cookie:/i.test(qs));
assert.ok(!/(^|\s)(-b|--cookie)(\s|=)/.test(qs));

console.log("apiDocsSpec.test.ts: ok");
