---
tags: [security]
---

# RBAC — authentication and authorisation

## Where this page stands

| Question | Status |
|---|---|
| Site roles `viewer` / `operator` / `site_admin` + instance boolean | **shipped** — `user_roles` table (migration `0001_init`), `model.RoleLevel`, `users.is_global_admin` |
| Local password with argon2id | **shipped** — `internal/auth/password.go`, RFC 9106 profile (t=1, m=64 MiB, p=4) |
| Opaque cookie session, only the SHA-256 in the database | **shipped** — `internal/auth/auth.go`, `sessions` table |
| Per-site scope checked on fleet routes | **shipped, but handler by handler** — `requireSite`, `deviceForWrite`, `Auth.CanSite`; it is **not** a parameterised middleware, and there are holes (§ *The check is written by hand*) |
| Full rights matrix on a proxy | **shipped** — `canSeeProxy`, `canAdminWholeProxy`, `requestedCoverage` |
| Adoption = `operator` on the chosen site | **shipped** — `internal/api/discovery_handlers.go` |
| TOTP 2FA | **decided, not built** — no column, no screen |
| OIDC / IdP group mapping | **decided, not built** — no dependency, no code |
| API tokens (PAT) | **decided, not built** — the only existing tokens are machine-to-machine (proxy) |
| `user.*` log of every mutating action, with IP | **almost nothing** — only `user.login`, without IP (§ *Audit*) |
| [[Web pass-through access]] as a distinct right | **decided, not built** — the feature itself is a study |
| `Secure` cookie behind a TLS terminator, CSRF protection | **open** — see the callout at the end of § *Authentication* |

## Authentication

- **Local**: email + password. **Shipped**, with argon2id (`internal/auth/password.go`): 64 MiB of memory and 4 lanes of parallelism, that is, the profile that makes a GPU attack expensive. A plain bcrypt would have been shorter to write and would have left the user database crackable at the first dump theft. The hash carries its own parameters (`$argon2id$v=19$m=…,t=…,p=…$…`), which will allow hardening them without invalidating existing passwords. Constant-time comparison, and a dummy hash verified even when the email is unknown, so that response time does not reveal whether an account exists.
- Optional **TOTP 2FA**, strongly recommended for admins — **decided, not built**.
- **OIDC**: delegation to an IdP (Keycloak, Entra, Google) for enterprise deployments, with IdP group → meshcraft role mapping — **decided, not built**. It is the feature that decides whether meshcraft enters an IT department or remains a team tool: keeping it on the plan is the only way not to paint the authorisation model into a corner the IdP could no longer reach (hence named roles and an assignment table, rather than rights inferred from a boolean).
- Sessions: **shipped**. Cookie `meshcraft_session` `HttpOnly` + `SameSite=Lax`, 7 days, 32 random bytes of which **only the SHA-256 is stored** — stealing the `sessions` table yields no usable session. The API and the SPA are same-origin since the frontend is served by the [[Controller]], which avoids having to choose between `SameSite=None` and a second token mechanism.
- The [[Web interface]]'s real-time stream does reuse the session, but it is **not** a WebSocket: it is **SSE** (`GET /api/sites/{id}/stream`), precisely for this reason. A WebSocket leaves the HTTP pipeline at upgrade and forces the rights check to be redone by hand — "exactly the kind of place where a right gets forgotten". SSE goes through the session middleware then `requireSite(…, "viewer")` like any other route.
- API tokens (PAT) with identical scoping, for automation — **decided, not built**. The only non-session tokens that exist are machine-to-machine and carry no user rights: a proxy's enrollment token and the tunnel's `(proxy_id, secret)` pair ([[Proxy security]]).

> [!warning] Two known holes in session transport
> They are neither choices nor targets: they are **fixes to make**.
>
> - The cookie only carries `Secure` if the controller terminates TLS itself
>   (`r.TLS != nil`). Behind the reverse proxy that is the recommended deployment
>   ([[Docker deployment and IPv6]]), it goes out **without** `Secure`, even though
>   `X-Forwarded-Proto` is there to say so. The symptom never appears in
>   development, which is precisely what makes the oversight last.
> - No **CSRF** protection: neither token nor `Origin` check. The only
>   barrier is `SameSite=Lax`. Since mutations are all JSON and
>   POST/PUT/PATCH/DELETE, the surface is narrow in practice — but it is
>   narrow by accident, not by decision, and a future `POST` route
>   accepting a form would reopen it without anything signalling it.

A first global-admin account is created at start-up if the table is empty (`cmd/controller/main.go`): password taken from `MESHCRAFT_ADMIN_PASSWORD` or drawn at random and **logged only once**. The opposite default — a documented fixed password — produces instances that people forget to harden.

## Authorisation model

Scope = **the site**. A user has `(role, site)` assignments or a global role. **Shipped as is**: `user_roles(user_id, site_id, role)` with `role in ('viewer','operator','site_admin')`, plus the `users.is_global_admin` boolean.

| Role | Rights |
|---|---|
| **viewer** | read: devices, topology, log, profiles |
| **operator** | viewer + edit intents/profiles, deploy, adopt devices |
| **site_admin** | operator + manage VLANs, **the site's management addressing plan**, site credentials, site users, **deploy proxies covering their sites** |
| **global-admin** | everything, all sites + site creation, **instance proxies**, connectors, updates, instance management |

Two clarifications the code imposes and that are better read here:

- `global-admin` is **not** a `role` value: it is a boolean on the user, and it short-circuits `CanSite`. Looking for it in `user_roles` will never find it.
- The primary key `(user_id, site_id)` imposes **a single role per site per user**. Holding both `operator` and `site_admin` on the same site is therefore impossible by construction — and makes no sense, the roles being ordered (`RoleLevel`).
- The [[Web interface]] speaks another vocabulary — *engineer* / *technician* / *read-only* (`useSiteRole`: `ing` / `tech` / `viewer`) — because "operator" and "site admin" mean nothing to the operations staff the screen is shown to. The translation is done in a single place; it is the same model.

- **Proxies are framed by their site coverage**, which is their rights boundary ([[Proxy]]). **Shipped, and faithfully**:

| Action on a proxy | Right required | Implementation |
|---|---|---|
| see it in the inventory | a role on a covered site (instance proxy: everyone) | `canSeeProxy`; the list is filtered **in SQL** (`p.global or proxy_sites ∈ …`) |
| deploy it | `site_admin` of at least one site — the created proxy's coverage is **bounded to their sites** | `requestedCoverage`: 403 without any admin site, `global:true` refused to a non-global-admin, each requested site checked |
| add/remove a site from its coverage | `site_admin` of **that** site, and of that one only | `handleSetProxySiteMember`, plus three refusals with 409: instance proxy, internal proxy, **last** site |
| **rename** it, revoke it, delete it, migrate it, declare scan ranges on it | `site_admin` of **all** covered sites, or `global-admin` | `canAdminWholeProxy`, via `proxyForAdmin` |
| make it an instance proxy (or the reverse) | `global-admin` | `handleSetProxyCoverage`, behind the `RequireGlobalAdmin` middleware |

  The logic is one of partitioning: a site-admin must not be able to manufacture or hijack a proxy that would see another site's network. Hence the "all covered sites" rule as soon as one acts on the proxy itself, and the refusal to remove the **last** covered site — a proxy with no site is no longer visible to anyone, hence unmanageable without a `global-admin`.

  Two refinements in the code that extend the same intent:

  - `proxyForAdmin` answers **404 when the proxy is not visible** and 403 only when it is. Answering 403 in both cases would turn the matrix into an existence oracle: a site-admin would learn the number of proxies of other sites by enumerating identifiers.
  - a site-admin cannot attach one of their scan ranges to the **addressing domain** of a proxy they do not administer (`checkDomainChoice`). Without this safeguard, merging discoveries offered an escalation path the matrix above did not close: one does not act on the proxy, one acts on what its results attach to.
- The [[Web interface]]'s site selector only shows assigned sites — **shipped** (`ListSites` joins `user_roles`). The "all sites" entry for global roles remains **decided, not built**: the selector currently requires a current site.
- **Adoption** ([[Discovery]]): adopting requires the `operator` right **on the chosen site** — **shipped**, and checked *before* any read of the candidate, because it is this action, and it alone, that brings a device into a site. What the docs additionally announced remains **open**: the list of offered sites is not filtered to those where the user is `operator`, only to those where they have a role. A `viewer` therefore sees the site in the choice and gets a 403 on validation — a correct refusal, at the worst moment.

> [!warning] The check is written by hand, not declared per route
> This is the most important gap on this page, and it concerns code that
> exists: there is **no** `(permission, site)` middleware. There are two
> middlewares — "authenticated" and "global-admin" — and **everything else** is an
> explicit call in the handler: `requireSite(w, r, minRole)`,
> `deviceForWrite(w, r, minRole)`, or `Auth.CanSite` directly. About seventy
> calls, all correct where they are.
>
> The defect is not in what is written, it is that **nothing breaks when one
> forgets to write it**. A route added without its call is open to any
> authenticated user, and the test that would have caught it does not exist. Holes
> known to date, all on instance routes, hence not scoped by a site
> URL: `GET /api/proxies/tunnels`, `GET /api/discovery/prefixes`, and above all the
> discovery queue — `GET /api/discovery/candidates`, the **raw configuration**
> of a candidate, and `DELETE` of a candidate, which is not a read.
>
> The docs long claimed "every API route carries a `(permission,
> site)` requirement checked in middleware". That is the target, and it remains the right one: a
> check declared at route declaration can be re-read at a glance and tested
> exhaustively. But stating it in the present tense had anyone wanting to understand why a route was open
> looking for a non-existent middleware.
>
> A nuance not to lose: for routes under `/api/sites/{siteID}/…`, scoping
> by the **URL** plus `requireSite` is as solid as SQL filtering — the
> query cannot overflow the site it names. SQL filtering by the
> authorised sites is only essential for **global** lists, and there it is
> indeed in place: `ListSites` and `ListProxies` filter in the database. It is the rest of the
> instance lists that filters nothing.

- [[Credentials]] are **usable** by operator (deployment). Their secrets do not come out of list screens, but **they can be read back**: `POST /api/sites/{id}/credentials/{cred}/reveal` returns the value in clear to a `site_admin`, membership of the credential in the URL's site checked, and the call is logged by name as `credential.revealed` (severity *warning*). This is not a departure from the model, it is break-glass scaled down to the site — see [[Service identity and rotation]] §12 for why it is better that it exists and is traced.
- **[[Service identity and rotation]]**: the instance policy (service accounts, SNMP algorithms, telemetry, rotation periods) belongs to the `global-admin`, who can **lock** a field to make it a floor sites cannot lower; the site override belongs to the `site_admin`. Triggering a rotation is an `operator` action on the scope concerned, since it is a deployment like any other ([[Reconciliation]]). **Decided, not built**: there is no policy table, so none of these rights has an object to apply to yet. What is shipped instead, and does follow the rule above: site credentials and their generations are `site_admin`, revealing too.
- [[Web pass-through access]] is a distinct right (it is equivalent to direct admin access to the device): `operator` minimum + can be enabled per site. **Decided, not built** — the feature itself is still a study. What already exists and touches the device through its web interface is **more** restricted than the target, not less: the `POST /api/devices/{id}/web-probe` probe requires `site_admin`, and `POST /api/discovery/device-fetch` requires `global-admin`. That is the right direction for the error as long as the dedicated right does not exist.

## Audit

Every mutating action (login, adoption, apply, profile edit, token creation, pass-through access) emits a `user.*` event in the [[Event log]] with the identity, the IP and the object touched. No separate audit table: a single stream, filterable by `type=user.*`.

The single-stream choice is **shipped and used everywhere** — about a hundred event types, including `credential.revealed`, `proxy.coverage_changed`, `device.collector` — but the `user.*` family, for its part, lies fallow.

> [!warning] The `user.*` family contains only one event
> `user.login`, and nothing else. No `user.logout`, no `user.login_failed`, nor
> any of the account management gestures: **creating a user, giving them
> `site_admin`, promoting them to global-admin or deleting them leaves no
> trace**. And the **IP** promised above is nowhere: `InsertEvent` has
> no column for it, only `actor` (the session email).
>
> This is not a cosmetic gap. The single stream only has audit value if it
> is exhaustive on privilege gestures; incomplete, it is worse than absent,
> because one queries it believing it answers. A `type=user.*` returning a
> single row reads as "nobody did anything", not as "we do not write".
>
> What is missing is narrow and known: emit in the five handlers of
> `users_handlers.go` and in `handleLogout`, and add the IP (`middleware.RealIP`
> is already in place, the address is in the request). The priority order is
> `user.roles_changed` first: it is the only gesture that changes what someone
> can do.
