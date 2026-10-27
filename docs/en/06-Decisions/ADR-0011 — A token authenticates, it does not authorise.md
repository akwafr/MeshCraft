---
Status: Accepted
Date: 2026-10-24
---

# ADR-0011 — An API token authenticates, it does not authorise

## Context

Per-user API tokens are needed, giving access to what the frontend can do, and an MCP
server so that an agent can drive the product.

What already exists and decides almost everything:

- **Authentication** is a session cookie (`meshcraft_session`), whose token is **hashed
  in the database** (`auth.HashToken`). The middleware loads the user into the context,
  or returns 401.
- **Authorisation** is entirely elsewhere: `RequireGlobalAdmin` for the instance
  subtrees, and above all `CanSite(ctx, siteID, minRole)` with `model.RoleLevel` for
  everything else. Each handler calls it itself.

The main risk of this request is therefore not cryptographic. It is that a token
**reimplements a second permission model** alongside the first. Two permission models
always end up diverging, and the day they diverge, it is the most permissive door that
wins.

## Decision

**An API token is a second way of proving *who* you are. It never changes *what you are
allowed to do*.**

1. **Same identity, same authorisation.** The middleware accepts
   `Authorization: Bearer <token>` and places **exactly the same `model.User`** in the
   context as a cookie does. All existing `CanSite` and `RequireGlobalAdmin` calls apply
   without being touched. No handler learns that tokens exist.
2. **A token can only REDUCE.** It carries an optional scope — read-only, list of sites,
   expiry date — which applies **on top of** the user's rights, never in their place. A
   `viewer`'s token does not become administrator because it asks for the right.
3. **Hashed at rest, shown once.** Same treatment as sessions. The cleartext value only
   exists at creation, in the response. It is neither stored, nor logged, nor
   retrievable.
4. **Never in a cookie.** The token only travels in the `Authorization` header. That is
   what makes it **immune to CSRF**: a browser does not send a header on its own.
5. **The frontend does not change door.** It stays on the cookie. (See the alternatives:
   the idea "the frontend should run on tokens" is tempting and it is rejected.)

## Consequences

- A token is worth exactly its bearer's rights **at the time of the call**: removing a
  role from someone closes their tokens within the second, without having to revoke them
  one by one. That is the direct benefit of having only one model.
- **One more authentication surface to audit.** The middleware must be the only place
  that knows both forms, and a test must check that an expired, revoked, or read-only
  token on a write fails — not merely that a valid token passes.
- A lost token is as serious as its bearer's password, **minus** the scope it gave
  itself. Hence expiry and read-only by default: the default must be the least
  powerful, not the most convenient.
- **Logging** becomes mandatory: every use updates a `last_used_at`, and the profile
  list shows it. A token nobody knows is in use is a token nobody dares revoke.
- MCP has **no privilege of its own**. It carries the token of the user who configured
  it and can do nothing that user cannot do. It is not a trusted component, it is a
  client.

## Rejected alternatives

**Run the frontend on tokens**, as the request suggests. Tempting — a single door, so a
single thing to secure. Rejected because storing a long-lived token in a browser is
**strictly worse** than the current cookie: an `HttpOnly` cookie is out of reach of
JavaScript, a token in `localStorage` is readable by any XSS, and it does not expire by
itself. The right direction of the exchange is the reverse of the one proposed.

What the request really wants is still delivered, and it is point 1: **the same API
surface serves both**. There is no API "for agents" alongside an API "for the
frontend". If a page can do something, a token can too, because it is the same handler.

**Token-specific permissions** (choosing gestures one by one). That is the second
permission model, and the defect this whole ADR exists to avoid.

**A JWT.** Self-contained, hence not revocable without a revocation list — either one
accepts that a stolen token stays valid until expiry, or one checks the database on
every call and has paid for the complexity for nothing. A random value hashed in the
database does the same job, can be revoked, and is compared in constant time.

**One token per instance rather than per user.** A shared secret identifies nobody: the
log would say "someone deleted this VLAN".
