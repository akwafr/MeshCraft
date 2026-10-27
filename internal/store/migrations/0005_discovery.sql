-- Device discovery and adoption
-- (see docs/en/01-Architecture/Discovery.md and docs/en/01-Architecture/Proxy.md).
--
-- Structuring rule: a discovered candidate has NO site. The site is chosen at
-- adoption, by a user entitled on that site. A range to scan belongs to the
-- proxy that can reach it, not to a site.

-- The "Local" proxy is the meshcraft instance itself: it scans and reaches
-- devices directly, without a remote proxy. It belongs to no site, hence
-- site_id now nullable.
alter table proxies alter column site_id drop not null;

insert into proxies (id, site_id, name, secret_hash, internal, version)
values ('00000000-0000-0000-0000-000000000001', null, 'Local', ''::bytea, true, 'intégré')
on conflict (id) do nothing;

-- The instance's "well-known standard" credentials, tried in priority order
-- during discovery. The secret is encrypted by the application (AES-GCM, see
-- internal/secretbox) and is never read back by the API.
create table known_credentials (
    id         uuid primary key default gen_random_uuid(),
    name       text not null,
    kind       text not null default 'ssh',   -- ssh | telnet | http
    username   text not null default '',
    secret_enc bytea not null,
    priority   int not null default 100,
    enabled    boolean not null default true,
    created_at timestamptz not null default now()
);
create index known_credentials_order_idx on known_credentials (priority, created_at);

-- IPv4/IPv6 ranges to scan, declared ON A PROXY.
create table scan_prefixes (
    id          uuid primary key default gen_random_uuid(),
    proxy_id    uuid not null references proxies(id) on delete cascade,
    prefix      text not null,
    enabled     boolean not null default true,
    note        text not null default '',
    last_run_at timestamptz,
    last_result text not null default '',
    created_at  timestamptz not null default now(),
    unique (proxy_id, prefix)
);

-- Candidates produced by discovery: without a site by construction.
create table discovered (
    id            uuid primary key default gen_random_uuid(),
    source        text not null default 'scan',   -- scan | manual | serial
    proxy_id      uuid references proxies(id) on delete set null,
    ip            text not null default '',
    mac           text not null default '',
    serial        text not null default '',
    hostname      text not null default '',
    vendor        text not null default '',
    model         text not null default '',
    driver        text not null default '',
    confidence    int  not null default 0,
    transports    text not null default '',      -- liste lisible : "ssh,https"
    fingerprint   jsonb not null default '{}'::jsonb,
    -- Configuration read from the device (SSH or pasted from the serial console):
    -- used by the "local configuration" import at adoption.
    raw_config    text not null default '',
    credential_id uuid references known_credentials(id) on delete set null,
    note          text not null default '',
    first_seen    timestamptz not null default now(),
    last_seen     timestamptz not null default now(),
    -- Filled in at adoption. An adopted candidate stays in the database for audit.
    adopted_device_id uuid references devices(id) on delete set null,
    adopted_site_id   uuid references sites(id) on delete set null,
    adopted_at        timestamptz
);
create index discovered_open_idx on discovered (adopted_device_id, last_seen desc);

-- Merge key across proxies: the serial if known, otherwise the MAC, otherwise
-- the (proxy, IP) pair — two distinct private networks may reuse the same IP,
-- so they are not conflated.
create unique index discovered_key_idx on discovered (
    coalesce(nullif(serial, ''), nullif(mac, ''), coalesce(proxy_id::text, 'local') || '|' || ip)
);

-- A candidate seen by several proxies is the normal case, not a conflict.
create table discovery_sightings (
    discovered_id uuid not null references discovered(id) on delete cascade,
    proxy_id      uuid not null references proxies(id) on delete cascade,
    ip            text not null,
    latency_ms    int not null default 0,
    last_seen     timestamptz not null default now(),
    primary key (discovered_id, proxy_id, ip)
);

-- Instance encryption key, used when MESHCRAFT_SECRET_KEY is absent.
-- See internal/secretbox: storing it here does not protect against a database
-- leak, which the UI says explicitly.
create table instance_secrets (
    name       text primary key,
    value      bytea not null,
    created_at timestamptz not null default now()
);
