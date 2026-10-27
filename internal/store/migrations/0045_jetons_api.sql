-- Personal API tokens: a SECOND WAY TO PROVE WHO YOU ARE, and nothing else
-- (see docs/en/06-Decisions/ADR-0011 — A token authenticates, it does not authorise.md).
--
-- # What this table is NOT
--
-- It is not a permission table. There is no column here that grants anything.
-- A row names a person (user_id) and, optionally, a REDUCTION of what that
-- person can do (read_only, site_ids, expires_at). Authorization stays where it
-- already was — `auth.CanSite`, `auth.RequireGlobalAdmin`, `model.RoleLevel` —
-- and a token's holder gets exactly the rights of its owner AT THE MOMENT OF
-- THE CALL, minus whatever the scope below takes away. Removing someone's role
-- closes their tokens in the same second, with no second list to remember.
--
-- That is the whole reason the token's rights are not stored: two permission
-- models always diverge, and the day they do, the more permissive door wins.
--
-- # Why the secret is split in two halves
--
-- The session cookie is looked up BY ITS HASH (`sessions.token_hash`), which
-- works because that lookup never has to be constant-time: an index probe on a
-- 256-bit hash tells an attacker nothing they could not already try.
--
-- A token here is split anyway:
--
--   * `selector` is the PUBLIC half, stored in clear and uniquely indexed. It
--     answers "which row is this?" and is not a secret — it is in the token's
--     visible prefix, shown in the owner's list so two tokens can be told
--     apart.
--   * `verifier_hash` is sha256 of the SECRET half. It is compared with
--     `crypto/subtle.ConstantTimeCompare`, never with `=` in SQL.
--
-- The split is what makes that comparison possible at all: with a single hashed
-- value there is nothing to compare in constant time, because finding the row
-- IS the comparison. Cf. internal/auth/apitoken.go.
--
-- # Hashed at rest, shown once
--
-- Same rule as the session token (internal/auth.NewSessionToken) and the SOCKS
-- token (migration 0036), for the same reason: a value the database can return
-- is a value a database leak returns. The clear token exists in exactly one
-- place for one instant — the body of the response that created it.
create table if not exists api_tokens (
  id            uuid        primary key default gen_random_uuid(),
  -- PER USER, and deleted with the user. A shared instance-wide secret
  -- identifies nobody: the audit line would read "somebody deleted this VLAN".
  user_id       uuid        not null references users(id) on delete cascade,
  -- A label the owner chose ("laptop CLI", "the MCP on my workstation"). Its
  -- only job is to make revocation possible: a token you cannot name is a token
  -- you dare not revoke.
  name          text        not null,
  selector      text        not null unique,
  verifier_hash bytea       not null,
  -- THE SCOPE — three ways to REDUCE, never to grant.
  --
  -- read_only defaults to TRUE: the default must be the least powerful, not the
  -- most convenient. Enforced at the HTTP edge (an unsafe method is refused
  -- 403) and again in CanSite (the effective role is capped at `viewer`), so a
  -- route that writes on a GET is covered too.
  read_only     boolean     not null default true,
  -- site_ids: an empty array means "no site restriction" — the token reaches
  -- whatever its owner reaches. A non-empty one intersects with the owner's
  -- roles, and it intersects for a GLOBAL ADMIN too (checked before the
  -- global-admin shortcut, or a site-scoped admin token would reach the whole
  -- instance).
  --
  -- text[] and not uuid[], deliberately: the array is only ever consulted to
  -- SUBTRACT, so an id naming no site simply grants nothing — there is no
  -- integrity to protect here, and no sensible cascade (a deleted site leaving
  -- a stale id behind makes the token strictly weaker). The shape is validated
  -- in Go so a typo answers 400 instead of a database error.
  site_ids      text[]      not null default '{}',
  created_at    timestamptz not null default now(),
  -- NULL = never expires. Possible, never the default: the API hands out 90
  -- days unless asked otherwise.
  expires_at    timestamptz,
  -- last_used_at answers the only question worth asking about a standing
  -- credential: is anyone still using it? Written on EVERY accepted call —
  -- best-effort, because a bookkeeping write must not fail an API call.
  last_used_at  timestamptz,
  -- Soft revocation, on purpose. The row survives so that a revoked token
  -- answers "revoked" rather than "unknown", and so the trail of who held one
  -- does not vanish with the gesture that closed it.
  revoked_at    timestamptz
);

create index if not exists api_tokens_owner on api_tokens (user_id, created_at desc);

comment on table api_tokens is
  'Per-user API credential presented as `Authorization: Bearer`. Authenticates '
  'only: rights come from the owner''s roles at call time, and the scope '
  'columns can only reduce them. Hashed verifier, shown once at creation, '
  'never in a cookie (ADR-0011).';
