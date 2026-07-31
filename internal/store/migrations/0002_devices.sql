-- Plan du site (zones du synoptique, dimensions) : jsonb libre versionné
-- par le format site-config.
alter table sites add column plan jsonb not null default '{}'::jsonb;

create table devices (
    id           uuid primary key default gen_random_uuid(),
    site_id      uuid not null references sites(id) on delete cascade,
    group_id     uuid references device_groups(id) on delete set null,
    name         text not null,
    model        text not null default '',
    vendor       text not null default '',
    location     text not null default '',
    mgmt_ip      text not null default '',
    status       text not null default 'unknown', -- ok | warn | off | unknown
    status_note  text not null default '',
    poe_budget_w int  not null default 0,
    pos_x        double precision not null default 0,
    pos_y        double precision not null default 0,
    -- Catalogue physique : [{prefix, count, poe, speed, label}]
    port_groups  jsonb not null default '[]'::jsonb,
    -- Intent + dernier état observé par port :
    -- {"ether1": {label, mode, pvid, tagged, poe, link, poe_w, rx_mbps, tx_mbps}}
    ports        jsonb not null default '{}'::jsonb,
    -- Métadonnées libres (uptime, version logicielle…)
    extra        jsonb not null default '{}'::jsonb,
    unique (site_id, name)
);

create table device_links (
    id       uuid primary key default gen_random_uuid(),
    site_id  uuid not null references sites(id) on delete cascade,
    a_device uuid not null references devices(id) on delete cascade,
    a_port   text not null default '',
    b_device uuid not null references devices(id) on delete cascade,
    b_port   text not null default '',
    speed    text not null default '',
    status   text not null default 'up', -- up | warn | down
    note     text not null default ''
);

create index devices_site_idx on devices (site_id);
create index device_links_site_idx on device_links (site_id);
