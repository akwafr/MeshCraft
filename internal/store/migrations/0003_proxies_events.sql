-- Proxys enrôlés : identité durable + ordre de migration vers une autre
-- instance (cf. docs/01-Architecture/Proxy et Sécurité du proxy).
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
    -- Migration : ordre déposé par l'instance actuelle, consommé par le proxy
    -- à son prochain heartbeat.
    migrate_url   text not null default '',
    migrate_token text not null default '',
    migrated_at   timestamptz
);
create index proxies_site_idx on proxies (site_id);

-- Journal d'événements (cf. docs/01-Architecture/Journal d'événements).
-- Table simple à ce stade : le partitionnement mensuel décrit dans le vault
-- arrivera avec le volume réel du monitoring (traps, syslog, polling).
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
