// THE API REFERENCE, AS DATA. The page (ApiDocs.tsx) only renders this.
//
// ──────────────────────────────────────────────────────────────────────────
// WHAT THIS IS, AND HOW MUCH TO TRUST IT
// ──────────────────────────────────────────────────────────────────────────
//
// meshcraft has two kinds of REST boundary and they are documented in two
// completely different ways.
//
//   THE CONTRACTS ARE MACHINE-SERVED. MDP v1 (the driver protocol) and Agent
//   v1 (the controller↔proxy lifeline) live under `contracts/`, have a
//   hand-written `openapi.yaml`, a GENERATED `schemas.json`, and are served
//   by the product itself at `GET /api/v1/contracts/{mdp,agent}`. They are
//   contracts because the code on the other side is code this repository does
//   not compile. Nothing below restates them; the page links to them.
//
//   THE FRONTEND API HAS NO SPECIFICATION. `internal/api/api.go` is the only
//   statement of it: one `Router()`, chi, ~200 routes, and the authorization
//   gate of each is a line inside its handler. That is the boundary ADR-0011
//   just opened to a second kind of caller, and a boundary somebody is told
//   to write a script against needs a document.
//
// So THIS TABLE IS HAND-WRITTEN, READ OFF `internal/api/api.go` AND ITS
// HANDLERS. It is not generated and it cannot be checked by a build step —
// which is the honest state of affairs and is said on the page itself, in the
// first block, rather than left for a reader to discover when a path has
// moved. Three consequences worth stating in the same breath:
//
//   * It is a SELECTION, not the whole router. The ~45 per-device action
//     routes and the discovery subtree are summarised by their group and
//     their gate rather than enumerated: enumerating them would double the
//     page and age twice as fast.
//   * The token group was written against ADR-0011 before its handler
//     existed, and carried an "assumed" chip on screen for exactly as long
//     as that was true. IT IS SERVED NOW, the chip and its machinery are
//     gone, and the group is read off `internal/api/apitoken_handlers.go`
//     like every other — whose header comment is, unusually, the whole
//     contract of its three routes written out in prose. A warning nobody
//     removes when it stops being true is worse than no warning: it teaches
//     the reader that the warnings on this page do not mean anything.
//   * Error texts are the server's own sentences, copied verbatim. They are
//     deliberately NOT translated — same rule as everywhere else in this UI:
//     a sentence produced by the backend is displayed as it was produced.

export type Method = "GET" | "POST" | "PUT" | "PATCH" | "DELETE";

/**
 * Who may call it. There are only three real gates in the product — the
 * session middleware, `RequireGlobalAdmin`, and `CanSite(ctx, site, minRole)`
 * — plus the two self-authenticating families (anonymous, and the proxy
 * credential pair). Everything else is one of those with the site resolved
 * from a different row.
 */
export type Gate =
  | "anon"
  | "session"
  | "self"
  | "cookie-only"
  | "global-admin"
  | "site-viewer"
  | "site-operator"
  | "site-admin"
  | "device-site"
  | "coverage"
  | "proxy-cred";

export interface RouteErr {
  status: number;
  /** The server's own sentence, verbatim. Never translated. */
  text: string;
}

export interface Route {
  m: Method;
  /** The path as the router declares it, `{param}` placeholders included. */
  p: string;
  gate: Gate;
  /** Minimum site role, when the gate is a site one — shown beside it. */
  role?: "viewer" | "operator" | "site_admin";
  /** i18n key under `apiDocs.r.` — absent for a row the group explains. */
  d?: string;
  /** Example request body. Verbatim JSON, not prose, so not translated. */
  req?: string;
  /** Example response body, abridged where the real one is enormous. */
  res?: string;
  errs?: RouteErr[];
}

export interface Group {
  /** Stable id: the anchor, and the i18n key under `apiDocs.g.`. */
  id: string;
  routes: Route[];
}

export const GROUPS: Group[] = [
  // ---------------------------------------------------------------- tokens
  {
    id: "apiTokens",
    routes: [
      {
        m: "GET",
        p: "/api/auth/tokens",
        gate: "self",
        d: "listTokens",
        res: `[
  {
    "id": "8f3c…",
    "name": "ansible",
    "prefix": "mct_1a2b3c4d",
    "read_only": true,
    "site_ids": ["0b1e…"],
    "created_at": "2026-10-24T09:12:00Z",
    "expires_at": "2027-01-22T09:12:00Z",
    "last_used_at": "2026-10-24T11:40:12Z"
  }
]`,
      },
      {
        m: "POST",
        p: "/api/auth/tokens",
        gate: "cookie-only",
        d: "createToken",
        req: `{
  "name": "ansible",
  "read_only": true,
  "site_ids": ["0b1e…"],
  "expires_in_days": 90
}`,
        res: `{
  "id": "8f3c…", "name": "ansible", "prefix": "mct_1a2b3c4d",
  "read_only": true, "site_ids": ["0b1e…"],
  "created_at": "…", "expires_at": "…", "last_used_at": null,
  "token": "mct_1a2b3c4d_9f8e…"
}`,
        errs: [
          {
            status: 400,
            text: "name is required: a token you cannot name is a token you dare not revoke",
          },
          { status: 400, text: "name is too long" },
          { status: 400, text: "expires_in_days must be between 0 (never) and 3650" },
          { status: 400, text: "site_ids must be site identifiers (uuid)" },
          {
            status: 409,
            text: "you already hold the maximum number of API tokens: revoke one first",
          },
          {
            status: 403,
            text: "this gesture requires a signed-in session, not an API token",
          },
        ],
      },
      {
        m: "DELETE",
        p: "/api/auth/tokens/{tokenID}",
        gate: "cookie-only",
        d: "revokeToken",
        errs: [
          {
            status: 403,
            text: "this gesture requires a signed-in session, not an API token",
          },
          { status: 404, text: "not found" },
        ],
      },
    ],
  },

  // --------------------------------------------------------------- session
  {
    id: "session",
    routes: [
      {
        m: "POST",
        p: "/api/auth/login",
        gate: "anon",
        d: "login",
        req: `{ "email": "you@example.net", "password": "…" }`,
        res: `{
  "id": "…", "email": "you@example.net", "display_name": "You",
  "is_global_admin": false, "created_at": "2026-01-04T08:00:00Z",
  "roles": [{ "site_id": "0b1e…", "site_name": "Lyon", "role": "operator" }]
}`,
        errs: [{ status: 401, text: "invalid credentials" }],
      },
      { m: "POST", p: "/api/auth/logout", gate: "session", d: "logout" },
      { m: "GET", p: "/api/auth/me", gate: "session", d: "me" },
      { m: "GET", p: "/api/auth/socks-token", gate: "self", d: "socksGet" },
      { m: "POST", p: "/api/auth/socks-token", gate: "self", d: "socksPost" },
      { m: "DELETE", p: "/api/auth/socks-token", gate: "self", d: "socksDelete" },
      { m: "GET", p: "/api/version", gate: "session", d: "version", res: `{ "version": "1.4.0", "build": "2026-10-24T07:41:02Z" }` },
      { m: "GET", p: "/api/healthz", gate: "anon", d: "healthz", res: `{ "status": "ok" }`, errs: [{ status: 503, text: "database unavailable" }] },
    ],
  },

  // ----------------------------------------------------------------- sites
  {
    id: "sites",
    routes: [
      {
        m: "GET",
        p: "/api/sites",
        gate: "session",
        d: "listSites",
        res: `[
  { "id": "0b1e…", "name": "Lyon", "slug": "lyon",
    "created_at": "2026-02-11T10:00:00Z", "local_domain": "lyon.example.net" }
]`,
      },
      {
        m: "POST",
        p: "/api/sites",
        gate: "global-admin",
        d: "createSite",
        req: `{ "name": "Lyon" }`,
        errs: [{ status: 400, text: "name required" }],
      },
      {
        m: "PATCH",
        p: "/api/sites/{siteID}",
        gate: "site-admin",
        role: "site_admin",
        d: "patchSite",
        req: `{ "name": "Lyon", "slug": "lyon", "local_domain": "lyon.example.net" }`,
        errs: [{ status: 400, text: "name, slug or local_domain required" }],
      },
      { m: "DELETE", p: "/api/sites/{siteID}", gate: "global-admin", d: "deleteSite" },
    ],
  },

  // ----------------------------------------------------------------- vlans
  {
    id: "vlans",
    routes: [
      {
        m: "GET",
        p: "/api/sites/{siteID}/vlans",
        gate: "site-viewer",
        role: "viewer",
        d: "listVlans",
        res: `[
  { "id": "7a2…", "site_id": "0b1e…", "vlan_id": 42,
    "name": "lab", "color": "#7c8cf8", "purpose": "" }
]`,
      },
      {
        m: "POST",
        p: "/api/sites/{siteID}/vlans",
        gate: "site-admin",
        role: "site_admin",
        d: "createVlan",
        req: `{ "vlan_id": 42, "name": "lab", "color": "#7c8cf8", "purpose": "" }`,
        errs: [
          { status: 400, text: "vlan_id (1-4094) and name required" },
          { status: 409, text: "already exists (duplicate)" },
        ],
      },
      {
        m: "PATCH",
        p: "/api/vlans/{id}",
        gate: "site-admin",
        role: "site_admin",
        d: "patchVlan",
        errs: [
          {
            status: 409,
            text: "VLAN 1 is the site's default VLAN: its ID cannot change",
          },
        ],
      },
      {
        m: "DELETE",
        p: "/api/vlans/{id}",
        gate: "site-admin",
        role: "site_admin",
        d: "deleteVlan",
        errs: [
          {
            status: 409,
            text: "VLAN 1 is the site's default VLAN: it cannot be deleted",
          },
        ],
      },
    ],
  },

  // --------------------------------------------------------------- devices
  {
    id: "devices",
    routes: [
      {
        m: "GET",
        p: "/api/sites/{siteID}/devices",
        gate: "site-viewer",
        role: "viewer",
        d: "listDevices",
      },
      {
        m: "POST",
        p: "/api/sites/{siteID}/devices",
        gate: "site-admin",
        role: "site_admin",
        d: "createDevice",
        req: `{ "name": "sw-lab-01", "model": "2530-48G", "vendor": "hpe",
  "mgmt_ip": "192.0.2.12", "location": "rack B" }`,
        errs: [{ status: 400, text: "name required" }],
      },
      { m: "PATCH", p: "/api/devices/{id}", gate: "device-site", role: "operator", d: "patchDevice" },
      { m: "DELETE", p: "/api/devices/{id}", gate: "device-site", role: "site_admin", d: "deleteDevice" },
      {
        m: "POST",
        p: "/api/devices/{id}/move",
        gate: "device-site",
        role: "site_admin",
        d: "moveDevice",
        req: `{ "site_id": "0b1e…" }`,
        errs: [
          { status: 400, text: "destination site_id required and different from current site" },
          { status: 403, text: "insufficient rights on the destination site" },
        ],
      },
      { m: "GET", p: "/api/devices/{id}/telemetry", gate: "device-site", role: "viewer", d: "telemetry" },
    ],
  },

  // ------------------------------------------------------------ port intent
  {
    id: "ports",
    routes: [
      {
        m: "PUT",
        p: "/api/devices/{id}/ports/{port}",
        gate: "device-site",
        role: "operator",
        d: "putPort",
        req: `{ "mode": "access", "pvid": 42, "description": "cam-03" }`,
        errs: [{ status: 400, text: "port: invalid JSON" }],
      },
      {
        m: "PUT",
        p: "/api/devices/{id}/ports",
        gate: "device-site",
        role: "operator",
        d: "putPorts",
        req: `{ "ports": {
  "1": { "mode": "access", "pvid": 42 },
  "2": { "mode": "access", "pvid": 42 }
} }`,
        errs: [
          { status: 400, text: "no ports in selection" },
          { status: 400, text: "selection too large" },
        ],
      },
      { m: "POST", p: "/api/devices/{id}/config/apply", gate: "device-site", role: "site_admin", d: "configApply" },
      { m: "POST", p: "/api/devices/{id}/ports/admin", gate: "device-site", role: "site_admin", d: "portsAdmin" },
      { m: "POST", p: "/api/devices/{id}/ports/power-cycle", gate: "device-site", role: "site_admin", d: "powerCycle" },
      { m: "POST", p: "/api/devices/{id}/read-state", gate: "device-site", role: "operator", d: "readState" },
    ],
  },

  // ------------------------------------------------------------- site reads
  {
    id: "siteReads",
    routes: [
      { m: "GET", p: "/api/sites/{siteID}/lldp", gate: "site-viewer", role: "viewer", d: "lldp" },
      { m: "GET", p: "/api/sites/{siteID}/clients", gate: "site-viewer", role: "viewer", d: "clients" },
      { m: "GET", p: "/api/sites/{siteID}/traffic", gate: "site-viewer", role: "viewer", d: "traffic" },
      { m: "GET", p: "/api/sites/{siteID}/events", gate: "site-viewer", role: "viewer", d: "siteEvents" },
      { m: "GET", p: "/api/sites/{siteID}/stream", gate: "site-viewer", role: "viewer", d: "stream" },
      { m: "GET", p: "/api/sites/{siteID}/export", gate: "site-operator", role: "operator", d: "export" },
      { m: "POST", p: "/api/sites/{siteID}/import", gate: "site-admin", role: "site_admin", d: "import" },
      { m: "GET", p: "/api/sites/{siteID}/credentials", gate: "site-operator", role: "operator", d: "siteCreds" },
    ],
  },

  // ----------------------------------------------------------------- users
  {
    id: "users",
    routes: [
      { m: "GET", p: "/api/users", gate: "global-admin", d: "listUsers" },
      {
        m: "POST",
        p: "/api/users",
        gate: "global-admin",
        d: "createUser",
        req: `{ "email": "new@example.net", "display_name": "New",
  "password": "at-least-8", "is_global_admin": false }`,
        errs: [{ status: 400, text: "email required and password of at least 8 characters" }],
      },
      {
        m: "PATCH",
        p: "/api/users/{id}",
        gate: "global-admin",
        d: "patchUser",
        errs: [
          { status: 400, text: "password of at least 8 characters" },
          { status: 400, text: "cannot remove your own global administrator status" },
        ],
      },
      {
        m: "DELETE",
        p: "/api/users/{id}",
        gate: "global-admin",
        d: "deleteUser",
        errs: [{ status: 400, text: "cannot delete your own account" }],
      },
      {
        m: "PUT",
        p: "/api/users/{id}/roles",
        gate: "global-admin",
        d: "putRoles",
        req: `[{ "site_id": "0b1e…", "role": "operator" }]`,
        errs: [{ status: 400, text: "unknown role: …" }],
      },
    ],
  },

  // --------------------------------------------------------------- instance
  {
    id: "instance",
    routes: [
      { m: "GET", p: "/api/events", gate: "global-admin", d: "events" },
      { m: "GET", p: "/api/proxies", gate: "session", d: "proxies" },
      { m: "GET", p: "/api/discovery/candidates", gate: "session", d: "candidates" },
      {
        m: "POST",
        p: "/api/discovery/candidates/{id}/adopt",
        gate: "site-operator",
        role: "operator",
        d: "adopt",
        req: `{ "site_id": "0b1e…" }`,
      },
      { m: "GET", p: "/api/driver-repos", gate: "global-admin", d: "driverRepos" },
      { m: "GET", p: "/api/agent-repos", gate: "global-admin", d: "agentRepos" },
    ],
  },

  // -------------------------------------------------------------- contracts
  {
    id: "contracts",
    routes: [
      { m: "GET", p: "/api/v1/contracts/mdp", gate: "anon", d: "contractMdp" },
      { m: "GET", p: "/api/v1/contracts/mdp-schemas", gate: "anon", d: "contractMdpSchemas" },
      { m: "GET", p: "/api/v1/contracts/agent", gate: "anon", d: "contractAgent" },
      { m: "GET", p: "/api/v1/contracts/agent-schemas", gate: "anon", d: "contractAgentSchemas" },
      { m: "POST", p: "/api/v1/agent/enroll", gate: "proxy-cred", d: "agentEnroll" },
      { m: "POST", p: "/api/v1/agent/heartbeat", gate: "proxy-cred", d: "agentHeartbeat" },
    ],
  },

  // ------------------------------------------------------------- anonymous
  {
    id: "anon",
    routes: [
      { m: "GET", p: "/api/healthz", gate: "anon", d: "healthz" },
      { m: "GET", p: "/api/v1/contracts/{name}", gate: "anon", d: "contractAny" },
      { m: "GET", p: "/api/public/dashboards/{token}", gate: "anon", d: "publicDashboard" },
      { m: "GET", p: "/downloads/proxy/{name}", gate: "anon", d: "downloadProxy" },
      { m: "GET", p: "/v2/*", gate: "anon", d: "registry" },
    ],
  },
];

/**
 * THE REFUSALS A TOKEN MEETS, with the server's own sentence.
 *
 * Copied verbatim out of `internal/api/apitoken_handlers.go` and
 * `internal/auth/apitoken.go` — a client matches on these strings, or at
 * least shows them, so a paraphrase here would be a paraphrase of the thing
 * the reader is holding in their terminal.
 *
 * The first three are DELIBERATELY ONE SENTENCE on the server: malformed,
 * unknown and wrong-secret all answer "invalid token", because telling them
 * apart tells somebody probing which half of their guess was right. The page
 * says so rather than quietly listing one of them.
 */
export interface Refusal {
  status: number;
  /** Verbatim. Never translated. */
  text: string;
  /** i18n key for the gloss beside it. */
  d: string;
}

export const REFUSALS: Refusal[] = [
  { status: 401, text: "invalid token", d: "apiDocs.refusalInvalid" },
  { status: 401, text: "token revoked", d: "apiDocs.refusalRevoked" },
  { status: 401, text: "token expired", d: "apiDocs.refusalExpired" },
  {
    status: 403,
    text: "read-only token: this token may not POST",
    d: "apiDocs.refusalReadOnly",
  },
  {
    status: 403,
    text:
      "this token is restricted to a list of sites: " +
      "instance-wide administration is out of its scope",
    d: "apiDocs.refusalSites",
  },
  {
    status: 403,
    text: "this gesture requires a signed-in session, not an API token",
    d: "apiDocs.refusalCookieOnly",
  },
  {
    status: 409,
    text: "you already hold the maximum number of API tokens: revoke one first",
    d: "apiDocs.refusalCap",
  },
];

/** The error table of §conventions: the codes a client has to handle. */
export const STATUS_CODES: number[] = [
  200, 201, 202, 204, 400, 401, 403, 404, 409, 413, 422, 429, 500, 502, 503,
];

/** The query parameters that exist in the whole API — there are no others. */
export interface QueryParam {
  name: string;
  /** Where it applies. A path fragment, not prose. */
  on: string;
}

export const QUERY_PARAMS: QueryParam[] = [
  { name: "limit", on: "/api/events, /api/sites/{siteID}/events, …/pipelines" },
  { name: "severity_min", on: "/api/events, /api/sites/{siteID}/events" },
  { name: "type", on: "/api/events, /api/sites/{siteID}/events" },
  { name: "device_id", on: "/api/sites/{siteID}/events" },
  { name: "hours", on: "/api/sites/{siteID}/traffic" },
  { name: "minutes", on: "/api/devices/{id}/telemetry" },
  { name: "days", on: "DELETE /api/sites/{siteID}/clients" },
  { name: "proxy", on: "/api/discovery/prefixes" },
  { name: "adopted", on: "/api/discovery/candidates" },
  { name: "q", on: "/api/discovery/reset-procedures" },
  { name: "tab", on: "/api/sites/{siteID}/stream" },
];

/**
 * Filter the table by a typed query. Matches the METHOD, the PATH and the
 * gate name — the three things somebody actually types ("post vlans",
 * "global-admin", "devices"). Word-wise AND, same rule as every other search
 * in this UI; the group survives only if something in it matched.
 *
 * Case-folded here rather than through `searchMatch`: a route path has no
 * diacritics by construction, and the strings are already ASCII.
 */
export function filterGroups(groups: Group[], query: string): Group[] {
  const words = query.toLowerCase().split(/\s+/).filter(Boolean);
  if (words.length === 0) return groups;
  const out: Group[] = [];
  for (const g of groups) {
    const routes = g.routes.filter((r) => {
      const hay = `${r.m} ${r.p} ${r.gate} ${r.role ?? ""} ${g.id}`.toLowerCase();
      return words.every((w) => hay.includes(w));
    });
    if (routes.length > 0) out.push({ ...g, routes });
  }
  return out;
}

/** How many routes a (possibly filtered) list holds. */
export function countRoutes(groups: Group[]): number {
  return groups.reduce((n, g) => n + g.routes.length, 0);
}

/**
 * The quickstart, for this host. Three calls that chain: prove the token
 * works, find a site, read its VLANs.
 *
 * The token is read from the environment, never typed into the command. A
 * literal secret on a command line is in the shell history of whoever pasted
 * it, and that is the commonest way one of these leaks.
 */
export function quickstart(origin: string): string {
  return `# 1. Mint it from your profile — NOT from here: creating a token needs a
#    signed-in session, deliberately. Then put it in the environment, never
#    in the command.
export MESHCRAFT_TOKEN='mct_1a2b3c4d_9f8e…'

# 2. Who am I? (works with the token alone — no cookie, no login call)
curl -s -H "Authorization: Bearer $MESHCRAFT_TOKEN" \\
     ${origin}/api/auth/me

# 2b. And what does this token look like in the list? Readable by a token,
#     unlike creating or revoking one. Match on "prefix".
curl -s -H "Authorization: Bearer $MESHCRAFT_TOKEN" \\
     ${origin}/api/auth/tokens

# 3. Which sites can this token reach?
curl -s -H "Authorization: Bearer $MESHCRAFT_TOKEN" \\
     ${origin}/api/sites

# 4. The VLANs of one of them (site id from step 3, a UUID — not the slug).
SITE=0b1e…
curl -s -H "Authorization: Bearer $MESHCRAFT_TOKEN" \\
     ${origin}/api/sites/$SITE/vlans

# 5. A write. Refused with 403 if the token is read-only, whatever your role.
curl -s -X POST \\
     -H "Authorization: Bearer $MESHCRAFT_TOKEN" \\
     -H "Content-Type: application/json" \\
     -H "X-Meshcraft-Tab: my-script" \\
     -d '{"vlan_id":42,"name":"lab"}' \\
     ${origin}/api/sites/$SITE/vlans`;
}
