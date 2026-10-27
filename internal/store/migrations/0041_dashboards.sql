-- Dashboards: several per site, laid out by the user, and shareable through a
-- PUBLIC link.
--
-- # Why a table, when two pages were enough until now
--
-- There were two fixed screens — "Dashboard" and "Monitoring" — and they said
-- almost the same thing: summary tiles, two charts, a log. Frozen in the binary,
-- they imposed the same trade-off on everybody: the operator who wants the event
-- log big on a wall screen and the one who wants the VLAN breakdown in front of
-- them all day had the same page, and neither was served.
--
-- The layout therefore becomes DATA, and there is only one concept left: the
-- dashboard. "Monitoring" survives as one of the two default dashboards,
-- editable like the others.
--
-- # Scope: the SITE, not the user
--
-- A dashboard belongs to the site, not to the person who created it. It is the
-- only choice consistent with the share link: a public link points to "the
-- site's dashboard", shown to someone who has no account here, and a dashboard
-- that existed only in its author's account would disappear with them — taking
-- the workshop's wall screen with it. It is also what lets two operators say
-- "look at the Monitoring dashboard" and talk about the same thing.
--
-- Accepted consequence: rearranging the tiles changes everybody's view. The
-- rights take it into account — reading is at `viewer` level, editing at
-- `operator` level, and SHARING at `site_admin` level (see
-- internal/api/dashboard_handlers.go).
create table if not exists dashboards (
    id       uuid primary key default gen_random_uuid(),
    site_id  uuid not null references sites(id) on delete cascade,
    name     text not null,
    -- position: the order of the tabs, as the operator arranged them. An
    -- `order by name` would have been enough to display something, but would have
    -- made it impossible to put first the dashboard one opens every morning without
    -- renaming it "1. …".
    position int not null default 0,

    -- --- The share link ---
    --
    -- share_token_hash: the token's SHA-256, never the token. Same rule as sessions
    -- (internal/auth.HashToken), SOCKS tokens (migration 0036) and the proxies'
    -- secret: a value the database can return is a value a database leak returns.
    -- The clear token exists only in the HTTP response that mints it, and in the
    -- clipboard of the person who asked for it.
    --
    -- NULL means "not shared", and that is the default: sharing is a GESTURE, not a
    -- default state. A dashboard created today is accessible only to accounts with
    -- a role on the site.
    --
    -- Rotation is a plain UPDATE of this column: the old token then matches no row,
    -- so the old link is dead at the instant of the commit. There is deliberately NO
    -- token history table — a revoked token kept "for traceability" would be a
    -- revoked token a query mistake can make valid again.
    share_token_hash   bytea,
    share_created_at   timestamptz,
    share_created_by   uuid references users(id) on delete set null,
    -- share_last_used_at answers the only question worth asking about a permanent
    -- link: is anybody still using it? A link nobody has opened in six months is a
    -- link to revoke, and without this column that judgement requires the logs.
    share_last_used_at timestamptz,

    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now()
);

-- A UNIQUE and PARTIAL index. Unique because a lookup by token must designate at
-- most one row: without it, a collision — or a faulty insert — would make the
-- dashboard a public link shows depend on an implicit `order by`. Partial
-- because "not shared" is the normal situation and NULL is not comparable:
-- without the `where`, PostgreSQL would accept multiple NULLs anyway, but the
-- index would carry them for nothing.
create unique index if not exists dashboards_share_token_hash_uk
    on dashboards (share_token_hash) where share_token_hash is not null;

create index if not exists dashboards_site_position_idx
    on dashboards (site_id, position, name);

-- A dashboard's tiles, with their geometry.
--
-- # One row per tile, and not a "layout" jsonb on the dashboard
--
-- The temptation was to store the whole layout in a jsonb column. One row per
-- tile costs a join and gives back two things in exchange: an SQL constraint on
-- the geometry (below), which prevents writing a tile outside the grid even from
-- a faulty client; and the ability to count or look up the tiles of a given type
-- the day a type is retired.
--
-- # The grid is 12 columns, here as in the browser
--
-- The 12 in the constraint is the same as `GRID_COLUMNS` in
-- web/src/dashboardLayout.ts. It is a duplication, and it is deliberate: the
-- constraint exists so the database refuses what a client — ours after a
-- regression, or another one — would not have known to refuse. A constraint that
-- trusted the caller for the width would constrain nothing. If this number has
-- to change, it changes in both places, and a migration must then re-fit the
-- existing tiles.
create table if not exists dashboard_tiles (
    id           uuid primary key default gen_random_uuid(),
    dashboard_id uuid not null references dashboards(id) on delete cascade,
    -- tile_type: the catalogue key (see store.TileCatalog). Text and not a
    -- PostgreSQL enum: the catalogue moves with every tile added, and an enum would
    -- require a migration for each. Validation is done on write by the API, which
    -- refuses an unknown type — which matters, because this field decides what the
    -- PUBLIC route goes and fetches from the database.
    tile_type    text not null,
    x int  not null default 0,
    y int  not null default 0,
    w int  not null default 3,
    h int  not null default 2,
    -- options: a tile's own settings — the traffic chart's window, the log's
    -- minimum severity. jsonb because these settings differ per type and one column
    -- per setting would make a mostly empty table.
    options jsonb not null default '{}'::jsonb,
    constraint dashboard_tiles_geometry_ck check (
        x >= 0 and y >= 0 and w >= 1 and h >= 1 and x + w <= 12
    )
);

create index if not exists dashboard_tiles_dashboard_idx
    on dashboard_tiles (dashboard_id, y, x);

-- Memory of the SEEDING of the two default dashboards, one site at a time.
--
-- The two default dashboards reproduce the fixed screens of before, and they are
-- editable: they are ordinary data from the first second. Yet "editable"
-- includes "deletable", and that is exactly what this table protects. Without
-- it, the only way to seed a site created tomorrow would have been "if this site
-- has no dashboard, create two" — hence making the two defaults reappear for the
-- operator who just deliberately deleted them, every time the page opens, with
-- no gesture able to stop it.
--
-- A row here means "this site received its defaults, once, and it will not be
-- done again". The seeding itself is written in Go (store.EnsureSiteDashboards)
-- and not in SQL here: the default layout names tile types, and those names must
-- come from the catalogue that validates them, not from a frozen copy in a
-- migration nobody will re-read.
create table if not exists dashboard_seeds (
    site_id   uuid primary key references sites(id) on delete cascade,
    seeded_at timestamptz not null default now()
);

comment on table dashboards is
  'Tableaux de bord d''un site, disposés par l''utilisateur. share_token_hash : '
  'SHA-256 du jeton de partage public, NULL tant que le tableau n''est pas partagé.';
comment on table dashboard_tiles is
  'Tuiles d''un tableau et leur géométrie dans une grille de 12 colonnes.';
comment on table dashboard_seeds is
  'Sites ayant déjà reçu les deux tableaux par défaut. Empêche leur '
  'réapparition après suppression volontaire.';
