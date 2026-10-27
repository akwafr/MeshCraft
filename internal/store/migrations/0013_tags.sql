-- Device tags, and credential scope by tag.
-- See docs/en/01-Architecture/Tags and credential scope.md.
--
-- The group (devices.group_id) is not enough: it is UNIQUE per device, and it
-- already carries the configuration profile. Yet the reasons for giving a
-- particular password do not exclude one another — a product line that caps
-- passwords at 16 characters, a location entrusted to a contractor, an isolated
-- address range. A device may fall under all three at once.

-- Tags set on a device.
create table if not exists device_tags (
    device_id  uuid not null references devices(id) on delete cascade,
    tag        text not null,
    -- source: "manual", or "rule:<uuid>" when a rule set it.
    --
    -- Replaying the rules must be able to REMOVE what a rule had set and no longer
    -- applies, without ever touching what a human decided. Without this column,
    -- the only safe re-evaluation would be to remove nothing, and the fleet would
    -- accumulate stale tags nobody would dare clean up.
    source     text not null default 'manual',
    created_at timestamptz not null default now(),
    primary key (device_id, tag)
);

create index if not exists device_tags_tag_idx on device_tags (tag);

-- Automatic tagging rules, per site.
create table if not exists tag_rules (
    id         uuid primary key default gen_random_uuid(),
    site_id    uuid not null references sites(id) on delete cascade,
    tag        text not null,
    -- priority breaks the tie between two tag credentials that would target the
    -- same device: the highest wins.
    priority   int not null default 0,
    enabled    boolean not null default true,
    -- match: criteria combined with AND. Recognised keys — vendor, model, location
    -- (case-insensitive substring) and ip_prefix (CIDR).
    --
    -- A rule WITHOUT criteria applies to nothing. The opposite would turn a
    -- half-typed line into a rule that takes the whole fleet.
    match      jsonb not null default '{}'::jsonb,
    note       text not null default '',
    created_at timestamptz not null default now(),
    updated_by uuid references users(id) on delete set null,
    unique (site_id, tag, match)
);

create index if not exists tag_rules_site_idx on tag_rules (site_id);

-- A credential may target a TAG instead of a group.
--
-- Empty = the credential targets no tag (site default, or group credential).
-- The historical uniqueness constraint was on (site_id, group_name, kind): it is
-- widened to the tag, otherwise a site could not have two distinct tag
-- credentials.
alter table site_credentials
  add column if not exists tag text not null default '';

alter table site_credentials
  add column if not exists priority int not null default 0;

alter table site_credentials
  drop constraint if exists site_credentials_site_id_group_name_kind_key;

create unique index if not exists site_credentials_scope_key
  on site_credentials (site_id, group_name, tag, kind);
