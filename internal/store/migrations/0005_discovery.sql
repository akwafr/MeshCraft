-- Découverte et adoption des équipements
-- (cf. docs/01-Architecture/Discovery et docs/01-Architecture/Proxy).
--
-- Règle structurante : un candidat découvert n'a PAS de site. Le site est
-- choisi à l'adoption, par un utilisateur habilité sur ce site. Une plage à
-- scanner appartient au proxy qui sait l'atteindre, pas à un site.

-- Le proxy « Local » est l'instance meshcraft elle-même : elle scanne et joint
-- les équipements directement, sans proxy distant. Il n'appartient à aucun
-- site, d'où site_id désormais nullable.
alter table proxies alter column site_id drop not null;

insert into proxies (id, site_id, name, secret_hash, internal, version)
values ('00000000-0000-0000-0000-000000000001', null, 'Local', ''::bytea, true, 'intégré')
on conflict (id) do nothing;

-- Identifiants « standards connus » de l'instance, essayés par ordre de
-- priorité pendant la découverte. Le secret est chiffré applicativement
-- (AES-GCM, cf. internal/secretbox) et n'est jamais relu par l'API.
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

-- Plages IPv4/IPv6 à scanner, déclarées SUR UN PROXY.
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

-- Candidats issus de la découverte : sans site par construction.
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
    -- Configuration lue sur l'équipement (SSH ou collée depuis la console
    -- série) : sert à l'import « configuration locale » lors de l'adoption.
    raw_config    text not null default '',
    credential_id uuid references known_credentials(id) on delete set null,
    note          text not null default '',
    first_seen    timestamptz not null default now(),
    last_seen     timestamptz not null default now(),
    -- Renseignés à l'adoption. Un candidat adopté reste en base pour l'audit.
    adopted_device_id uuid references devices(id) on delete set null,
    adopted_site_id   uuid references sites(id) on delete set null,
    adopted_at        timestamptz
);
create index discovered_open_idx on discovered (adopted_device_id, last_seen desc);

-- Clé de fusion entre proxys : le serial s'il est connu, sinon la MAC, sinon
-- le couple (proxy, IP) — deux réseaux privés distincts peuvent réutiliser la
-- même IP, on ne les confond donc pas.
create unique index discovered_key_idx on discovered (
    coalesce(nullif(serial, ''), nullif(mac, ''), coalesce(proxy_id::text, 'local') || '|' || ip)
);

-- Un candidat vu par plusieurs proxys est le cas normal, pas un conflit.
create table discovery_sightings (
    discovered_id uuid not null references discovered(id) on delete cascade,
    proxy_id      uuid not null references proxies(id) on delete cascade,
    ip            text not null,
    latency_ms    int not null default 0,
    last_seen     timestamptz not null default now(),
    primary key (discovered_id, proxy_id, ip)
);

-- Clé de chiffrement d'instance, utilisée faute de MESHCRAFT_SECRET_KEY.
-- Voir internal/secretbox : la stocker ici ne protège pas d'une fuite de la
-- base, ce que l'UI dit explicitement.
create table instance_secrets (
    name       text primary key,
    value      bytea not null,
    created_at timestamptz not null default now()
);
