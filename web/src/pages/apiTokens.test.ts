/**
 * The API-token decisions — run with:
 *   node --experimental-strip-types src/pages/apiTokens.test.ts
 * (from web/)
 *
 * ADR-0011 is a set of rules about what a token may do. A rule that only
 * exists inside JSX is a rule nobody can check, so the three that matter most
 * are asserted here: the default is the LEAST powerful token, an expiry is
 * read on the instant rather than on the day, and the scope is always stated
 * on all three axes — including the permissive end of each, because a scope
 * that mentions only its restrictions reads as restricted when it is not.
 */
import assert from "node:assert/strict";
import {
  createBody,
  curlExample,
  daysLeft,
  defaultDraft,
  expiresSoon,
  MAX_EXPIRY_DAYS,
  scopeFacts,
  tokenState,
  TOKENS_PATH,
  type ApiToken,
} from "./apiTokens.ts";

const NOW = new Date("2026-10-24T12:00:00Z");

function tok(over: Partial<ApiToken> = {}): ApiToken {
  return {
    id: "t1",
    name: "ansible",
    prefix: "mct_1a2b3c4d",
    read_only: true,
    site_ids: [],
    created_at: "2026-10-01T09:00:00Z",
    expires_at: null,
    last_used_at: null,
    ...over,
  };
}

// --- the routes, as the handler serves them -------------------------------
// Under /api/auth/ with the session and the SOCKS token, where a credential
// belongs — and NOT under a path that takes a user id, because no such path
// exists for anybody, global administrator included.
assert.equal(TOKENS_PATH, "/api/auth/tokens");
assert.ok(!TOKENS_PATH.includes("users"));

// --- the default IS the rule ----------------------------------------------
// "the default must be the least powerful, not the most convenient". If one of
// these three ever flips, the rule was relaxed — not the test.
assert.equal(defaultDraft("site-a").readOnly, true);
// 90, which is the SERVER's own default (`defaultTokenExpiryDays`). A form
// showing a different number would be a second opinion the handler overrules.
assert.equal(defaultDraft("site-a").expiresInDays, 90);
assert.deepEqual(defaultDraft("site-a").siteIDs, ["site-a"]);
// A fresh install with no site at all: there is nothing to narrow to, so an
// empty list is the only coherent value rather than a deliberate widening.
assert.deepEqual(defaultDraft(null).siteIDs, []);

// --- the wire body --------------------------------------------------------
assert.deepEqual(
  createBody({ name: "  ansible  ", readOnly: false, expiresInDays: 7, siteIDs: ["s1"] }),
  { name: "ansible", read_only: false, expires_in_days: 7, site_ids: ["s1"] },
);

// "NEVER" IS SPELLED `0` ON THE WIRE, NOT `null`. The field is optional and
// an omitted one means ninety days, so sending null — or dropping it — would
// quietly produce the opposite of what was asked for.
const never = createBody({ name: "x", readOnly: true, expiresInDays: null, siteIDs: [] });
assert.equal(never.expires_in_days, 0);
assert.notEqual(never.expires_in_days, null);

// The ceiling the server enforces (`maxTokenExpiryDays`), restated here so
// the form and the handler cannot drift apart in silence.
assert.equal(MAX_EXPIRY_DAYS, 3650);

// --- expiry ---------------------------------------------------------------
assert.equal(tokenState(tok({ expires_at: null }), NOW), "active");
assert.equal(daysLeft(tok({ expires_at: null }), NOW), null);
assert.equal(expiresSoon(tok({ expires_at: null }), NOW), false);

// On the instant, not the day after.
assert.equal(tokenState(tok({ expires_at: "2026-10-24T11:59:59Z" }), NOW), "expired");
assert.equal(tokenState(tok({ expires_at: "2026-10-24T12:00:01Z" }), NOW), "active");

// Rounded UP. Eleven hours left is one day left; rounding down would tell
// somebody with most of a day that their token has none.
assert.equal(daysLeft(tok({ expires_at: "2026-10-24T23:00:00Z" }), NOW), 1);
assert.equal(daysLeft(tok({ expires_at: "2026-10-31T12:00:00Z" }), NOW), 7);
assert.equal(daysLeft(tok({ expires_at: "2026-10-22T12:00:00Z" }), NOW), -2);

// "Soon" is inside a week and not yet past — an already-dead token is not
// "about to expire", it has a different thing to say.
assert.equal(expiresSoon(tok({ expires_at: "2026-10-31T12:00:00Z" }), NOW), true);
assert.equal(expiresSoon(tok({ expires_at: "2026-11-01T12:00:00Z" }), NOW), false);
assert.equal(expiresSoon(tok({ expires_at: "2026-10-22T12:00:00Z" }), NOW), false);

// An unparseable date must not be read as "this token is dead": the screen
// would offer to revoke something that still works.
assert.equal(tokenState(tok({ expires_at: "not a date" }), NOW), "active");
assert.equal(daysLeft(tok({ expires_at: "not a date" }), NOW), null);

// --- the scope, as facts --------------------------------------------------
const name = (id: string) => ({ s1: "Lyon", s2: "Siège" })[id] ?? id;

// All three axes, permissive end included.
assert.deepEqual(
  scopeFacts({ read_only: false, site_ids: [], expires_at: null }, name).map((f) => f.key),
  ["profilePage.scopeReadWrite", "profilePage.scopeAllSites", "profilePage.scopeNoExpiry"],
);

const narrowed = scopeFacts(
  { read_only: true, site_ids: ["s1", "s2"], expires_at: "2026-12-01T00:00:00Z" },
  name,
);
assert.deepEqual(narrowed.map((f) => f.key), [
  "profilePage.scopeReadOnly",
  "profilePage.scopeSites",
  "profilePage.scopeExpires",
]);
// `count` drives the plural, `sites` the sentence — interpolated, never
// concatenated: word order does not survive translation.
assert.deepEqual(narrowed[1].params, { count: 2, sites: "Lyon, Siège" });

// A null site list means EVERY site, exactly as the server reads it. Treating
// null as "no sites" would describe the widest token as the narrowest.
assert.equal(
  scopeFacts({ read_only: true, site_ids: null, expires_at: null }, name)[1].key,
  "profilePage.scopeAllSites",
);

// --- the example command --------------------------------------------------
// The secret is read from the environment and never written into the command:
// a literal token on a command line is in the shell history of whoever pasted
// it, which is the commonest way one of these leaks.
const ro = curlExample("https://mesh.example.net", true);
assert.ok(ro.includes("Authorization: Bearer $MESHCRAFT_TOKEN"));
assert.ok(!ro.includes("POST"));
assert.ok(curlExample("https://mesh.example.net", false).includes("POST"));
assert.ok(!ro.toLowerCase().includes("cookie"));

console.log("apiTokens.test.ts: ok");
