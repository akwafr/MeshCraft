-- SOCKS tokens: the credential a BROWSER presents to reach a device's own web
-- interface through the meshcraft proxy that can see it.
--
-- # Why a second credential, and not the session cookie
--
-- The route is SOCKS5, which authenticates with a username and password
-- (RFC 1929) and knows nothing about cookies. A browser configured to use a
-- SOCKS proxy presents those to the proxy, not to the site — so the session that
-- authenticates the meshcraft UI cannot authenticate the route.
--
-- PER USER, deliberately. A SOCKS session opens a path into a customer's
-- management network, and the audit line for it has to name a person. Per-user
-- also means the obvious revocation works: removing someone's meshcraft account
-- removes their route, with no second list to remember (the cascade below).
--
-- HASHED, like every other secret meshcraft holds on behalf of a person. The
-- token is shown once when generated and never again — the same rule as the
-- session token (cf. internal/auth.NewSessionToken), and for the same reason: a
-- value the database can return is a value a database leak returns.
create table if not exists socks_tokens (
  user_id     uuid        not null primary key references users(id) on delete cascade,
  token_hash  bytea       not null,
  created_at  timestamptz not null default now(),
  -- last_used_at answers the only question worth asking about a standing
  -- credential: is anyone still using it? A token nobody has presented in months
  -- is one to revoke, and without this column that judgement needs the logs.
  last_used_at timestamptz
);

comment on table socks_tokens is
  'Per-user credential for the SOCKS5 route to device web interfaces. Hashed; '
  'shown once at generation. Deleted with the user.';
