-- Management addressing plan, per site
-- (see docs/en/01-Architecture/Management addressing.md).
--
-- The plan says HOW an adopted device becomes reachable for administration:
-- which VLAN, which subnet, which gateway, and whether the address comes from
-- an external DHCP or from meshcraft.
--
-- Reminder of the structuring rule: "managed by meshcraft" means meshcraft
-- ALLOCATES an address in the range and pushes it as STATIC configuration. It
-- emits no DHCP packet — it is not a DHCP server.
create table if not exists site_mgmt_plan (
    site_id    uuid primary key references sites(id) on delete cascade,
    -- Administration VLAN. untagged = management on the native VLAN, which is the
    -- only possible mode on some devices.
    vlan_id    int not null default 1,
    untagged   boolean not null default true,

    subnet_v4  text not null default '',
    gateway_v4 text not null default '',
    subnet_v6  text not null default '',
    gateway_v6 text not null default '',
    dns        text[] not null default '{}',
    ntp        text[] not null default '{}',

    -- dhcp_external : meshcraft configures the DHCP client and allocates nothing.
    -- meshcraft     : meshcraft picks the address from the range below.
    source     text not null default 'dhcp_external',
    pool_start text not null default '',
    pool_end   text not null default '',

    -- strict_plan: refuse the adoption of a device unable to carry this plan,
    -- instead of accepting it outside the plan. On by default.
    strict_plan boolean not null default true,

    updated_by uuid references users(id) on delete set null,
    updated_at timestamptz not null default now()
);

-- IPAM: the management addresses allocated by meshcraft.
--
-- Uniqueness is carried by the DATABASE and not by the code: two simultaneous
-- adoptions cannot pick the same address. An address is never reused while a
-- device may still carry it — a switch that was off comes back with its old
-- configuration.
create table if not exists mgmt_addresses (
    site_id     uuid not null references sites(id) on delete cascade,
    address     text not null,
    device_id   uuid references devices(id) on delete set null,
    state       text not null default 'reserved', -- reserved|assigned|released
    allocated_at timestamptz not null default now(),
    released_at timestamptz,
    primary key (site_id, address)
);
create index if not exists mgmt_addresses_device_idx on mgmt_addresses (device_id);
