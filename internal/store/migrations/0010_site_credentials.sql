-- Credentials meshcraft installs and uses on a site's devices.
--
-- OPERATIONAL DECISION: one password per SITE, not per device. The spec
-- (docs/en/03-Security/Service identity and rotation.md) recommended one secret
-- per device — a single compromised device then not handing over the fleet.
-- Operations decided otherwise: an administrator must be able to log in to any
-- device of their site without looking up a list of 200 passwords. The
-- trade-off is accepted and documented.
--
-- The nuance that makes it workable: password policies differ from one model to
-- another (length, forbidden characters). A site may therefore define several
-- credentials, one per device GROUP, the empty group being the site's default.
create table if not exists site_credentials (
    id         uuid primary key default gen_random_uuid(),
    site_id    uuid not null references sites(id) on delete cascade,
    -- empty group_name = the site's default credential.
    group_name text not null default '',
    -- kind: what this credential is for (web, ssh, snmp…).
    kind       text not null default 'admin',
    username   text not null default 'admin',
    secret_enc bytea not null,
    note       text not null default '',
    -- rotated_at: the last time the secret changed. A password that has not moved
    -- in two years must show.
    rotated_at timestamptz not null default now(),
    created_at timestamptz not null default now(),
    updated_by uuid references users(id) on delete set null,
    unique (site_id, group_name, kind)
);
create index if not exists site_credentials_site_idx on site_credentials (site_id);
