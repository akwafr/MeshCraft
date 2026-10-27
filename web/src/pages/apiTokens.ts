// The decisions behind the API-token screen, out of the component so they can
// be tested — ADR-0011 is a set of rules about what a token may do, and a rule
// that only exists inside JSX is a rule nobody can check.
//
// THE SHAPE IS NOT DECLARED HERE. `ApiToken`, `ApiTokenCreated` and
// `ApiTokenRequest` live in `web/src/api.ts` beside every other mirror of
// `internal/model`, and the full contract of the three routes is written out
// at the head of `internal/api/apitoken_handlers.go`. This file re-exports the
// types rather than restating them: a screen that keeps its own copy of a wire
// shape is a screen that drifts from it silently.
//
//   GET    /api/auth/tokens        → 200 ApiToken[]        (a bare array)
//   POST   /api/auth/tokens        → 201 ApiTokenCreated   (the ONLY `token`)
//   DELETE /api/auth/tokens/{id}   → 204
//
// They sit under `/api/auth/` with the session and the SOCKS token, which is
// where a credential belongs. The list is readable by a token; CREATE AND
// REVOKE ARE NOT — `auth.CookieOnly` refuses them, because a token able to
// mint another one outlives its own expiry and its own revocation.

import type { ApiToken, ApiTokenCreated, ApiTokenRequest } from "../api";

export type { ApiToken, ApiTokenCreated, ApiTokenRequest };

/** Where the three routes live. One spelling, so a rename is one edit. */
export const TOKENS_PATH = "/api/auth/tokens";

/** What the create form holds, before it becomes an `ApiTokenRequest`. */
export interface TokenDraft {
  name: string;
  readOnly: boolean;
  /** Null = never expires. The wire spells that `0`, not `null`. */
  expiresInDays: number | null;
  /** Empty = no site restriction, which is the WIDEST a token gets. */
  siteIDs: string[];
}

/**
 * Draft → request body.
 *
 * `0` is the wire's word for "never", and it has to be sent explicitly: the
 * field is optional and an omitted one means 90 days, so dropping it for a
 * never-expiring token would quietly produce the opposite.
 */
export function createBody(d: TokenDraft): ApiTokenRequest {
  return {
    name: d.name.trim(),
    read_only: d.readOnly,
    expires_in_days: d.expiresInDays === null ? 0 : d.expiresInDays,
    site_ids: d.siteIDs,
  };
}

/**
 * THE DEFAULT IS THE LEAST POWERFUL TOKEN THAT IS STILL USEFUL — ADR-0011,
 * "the default must be the least powerful, not the most convenient".
 *
 * Read-only, ninety days, and scoped to the site the top bar is currently
 * naming. The first two mirror what the server already does when a client says
 * nothing (`defaultTokenExpiryDays`), so the form shows the truth rather than a
 * second opinion the handler would overrule.
 *
 * The third is the one that needs defending: there is no restrictive value for
 * "which sites" the way there is for read-only, and leaving the field empty
 * means NO SITE RESTRICTION — the widest token the product mints, arrived at by
 * not making a choice. Seeding the current site is the restrictive end of the
 * same axis and is what somebody writing a script against the screen they are
 * looking at wanted anyway. Widening it stays one gesture, and it is a gesture.
 */
export function defaultDraft(currentSiteID: string | null): TokenDraft {
  return {
    name: "",
    readOnly: true,
    expiresInDays: 90,
    siteIDs: currentSiteID ? [currentSiteID] : [],
  };
}

/**
 * The expiries offered, shortest first. `null` is "never" and is last.
 *
 * 90 is in the middle rather than at the head because it is the SERVER's
 * default and the form opens on it; the shorter ones above it are the ones a
 * careful person reaches for.
 */
export const EXPIRY_DAYS: (number | null)[] = [7, 30, 90, 365, null];

/** The server's ceiling (`maxTokenExpiryDays`), restated for the form. */
export const MAX_EXPIRY_DAYS = 3650;

/** The server's cap on active tokens (`maxTokensPerUser`). */
export const MAX_TOKENS = 20;

/** The server's cap on the name (`maxTokenNameLen`). */
export const MAX_NAME_LEN = 80;

export type TokenState = "active" | "expired";

export function tokenState(tok: ApiToken, now: Date): TokenState {
  if (!tok.expires_at) return "active";
  const exp = Date.parse(tok.expires_at);
  if (Number.isNaN(exp)) return "active";
  return exp <= now.getTime() ? "expired" : "active";
}

/**
 * Days until expiry, negative once past, null for a token with no expiry.
 * Rounded UP: a token with eleven hours left has one day left, not zero.
 */
export function daysLeft(tok: ApiToken, now: Date): number | null {
  if (!tok.expires_at) return null;
  const exp = Date.parse(tok.expires_at);
  if (Number.isNaN(exp)) return null;
  return Math.ceil((exp - now.getTime()) / 86_400_000);
}

/** Within a week of expiring and not yet expired — worth saying out loud. */
export function expiresSoon(tok: ApiToken, now: Date): boolean {
  const d = daysLeft(tok, now);
  return d !== null && d > 0 && d <= 7;
}

/**
 * The scope, as the ordered list of facts a reader needs — i18n keys with
 * their parameters, never assembled prose: word order does not survive
 * translation.
 *
 * Ordered by how much it reduces: what it may DO, then WHERE, then UNTIL WHEN.
 * The permissive end of each axis is stated too ("read and write", "all your
 * sites"), because a scope that only mentions its restrictions reads as
 * restricted when it is not.
 */
export interface ScopeFact {
  key: string;
  params?: Record<string, string | number>;
}

export function scopeFacts(
  tok: Pick<ApiToken, "read_only" | "site_ids" | "expires_at">,
  siteName: (id: string) => string,
): ScopeFact[] {
  const out: ScopeFact[] = [];
  out.push({ key: tok.read_only ? "profilePage.scopeReadOnly" : "profilePage.scopeReadWrite" });
  const sites = tok.site_ids ?? [];
  if (sites.length === 0) {
    out.push({ key: "profilePage.scopeAllSites" });
  } else {
    out.push({
      key: "profilePage.scopeSites",
      params: { count: sites.length, sites: sites.map(siteName).join(", ") },
    });
  }
  out.push(
    tok.expires_at
      ? { key: "profilePage.scopeExpires" }
      : { key: "profilePage.scopeNoExpiry" },
  );
  return out;
}

/**
 * A `curl` that actually works, for this host and this token — the example on
 * the API page is generic, the one beside a freshly minted secret is not.
 *
 * The secret goes in the HEADER and the command reads the value out of the
 * environment rather than carrying it inline: a literal token on a command
 * line lands in the shell history of whoever pastes it, which is the single
 * most common way one of these leaks.
 *
 * The read-only variant shows no write ON PURPOSE. A read-write token's second
 * example is a real write, because that is what the holder asked for; offering
 * one to a read-only token would be teaching a command that answers 403.
 */
export function curlExample(origin: string, readOnly: boolean): string {
  const base = `export MESHCRAFT_TOKEN='…'   # paste the value here, do not commit it
curl -H "Authorization: Bearer $MESHCRAFT_TOKEN" \\
     ${origin}/api/sites`;
  if (readOnly) return base;
  return `${base}

# A write needs the same header and nothing more:
curl -X POST -H "Authorization: Bearer $MESHCRAFT_TOKEN" \\
     -H "Content-Type: application/json" \\
     -d '{"vlan_id":42,"name":"lab"}' \\
     ${origin}/api/sites/<site-uuid>/vlans`;
}
