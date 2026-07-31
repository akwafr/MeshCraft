create table users (
    id              uuid primary key default gen_random_uuid(),
    email           text not null unique,
    pw_hash         text not null,
    display_name    text not null default '',
    is_global_admin boolean not null default false,
    created_at      timestamptz not null default now()
);

create table sessions (
    token_hash bytea primary key,
    user_id    uuid not null references users(id) on delete cascade,
    created_at timestamptz not null default now(),
    expires_at timestamptz not null
);

create table sites (
    id         uuid primary key default gen_random_uuid(),
    name       text not null unique,
    created_at timestamptz not null default now()
);

create table user_roles (
    user_id uuid not null references users(id) on delete cascade,
    site_id uuid not null references sites(id) on delete cascade,
    role    text not null check (role in ('viewer', 'operator', 'site_admin')),
    primary key (user_id, site_id)
);

create table vlans (
    id      uuid primary key default gen_random_uuid(),
    site_id uuid not null references sites(id) on delete cascade,
    vlan_id int  not null check (vlan_id between 1 and 4094),
    name    text not null,
    color   text not null default '#7c8cf8',
    purpose text not null default '',
    unique (site_id, vlan_id)
);

create table port_profiles (
    id      uuid primary key default gen_random_uuid(),
    site_id uuid not null references sites(id) on delete cascade,
    name    text not null,
    spec    jsonb not null default '{}'::jsonb,
    unique (site_id, name)
);

create table device_profiles (
    id      uuid primary key default gen_random_uuid(),
    site_id uuid not null references sites(id) on delete cascade,
    name    text not null,
    spec    jsonb not null default '{}'::jsonb,
    unique (site_id, name)
);

create table device_groups (
    id                uuid primary key default gen_random_uuid(),
    site_id           uuid not null references sites(id) on delete cascade,
    name              text not null,
    device_profile_id uuid references device_profiles(id) on delete set null,
    unique (site_id, name)
);

create table enroll_tokens (
    id         uuid primary key default gen_random_uuid(),
    site_id    uuid not null references sites(id) on delete cascade,
    token_hash bytea not null unique,
    created_by uuid references users(id) on delete set null,
    created_at timestamptz not null default now(),
    expires_at timestamptz not null,
    used_at    timestamptz
);
