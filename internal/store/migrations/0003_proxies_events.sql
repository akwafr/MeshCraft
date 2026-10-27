-- Enrolled proxies: durable identity + migration order towards another
-- instance (see docs/en/01-Architecture/Proxy.md and Proxy security).
create table proxies (
    id            uuid primary key default gen_random_uuid(),
    site_id       uuid not null references sites(id) on delete cascade,
    name          text not null default '',
    secret_hash   bytea not null,
    version       text not null default '',
    internal      boolean not null default false,
    ifaces        jsonb not null default '[]'::jsonb,
    revoked       boolean not null default false,
    created_at    timestamptz not null default now(),
    last_seen     timestamptz,
    -- Migration: an order dropped by the current instance, consumed by the proxy
    -- on its next heartbeat.
    migrate_url   text not null default '',
    migrate_token text not null default '',
    migrated_at   timestamptz
);
create index proxies_site_idx on proxies (site_id);

-- Event log (see docs/en/01-Architecture/Event log.md).
-- A simple table at this stage: the monthly partitioning described in the vault
-- will come with the real monitoring volume (traps, syslog, polling).
create table events (
    id        bigint generated always as identity primary key,
    ts        timestamptz not null default now(),
    site_id   uuid references sites(id) on delete cascade,
    device_id uuid references devices(id) on delete set null,
    port_name text not null default '',
    type      text not null,
    severity  smallint not null default 1,
    message   text not null,
    actor     text not null default '',
    payload   jsonb not null default '{}'::jsonb
);
create index events_site_ts_idx on events (site_id, ts desc);
create index events_device_ts_idx on events (device_id, ts desc);
create index events_type_ts_idx on events (type, ts desc);
