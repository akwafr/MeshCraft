-- The Clients page's two tables: what the fleet's bridges have LEARNED, and
-- the per-MAC ledger that survives the learning.
--
-- Two tables and not one, because they answer two different questions with two
-- different lifetimes:
--
--   * mac_sightings mirrors each device's CURRENT forwarding table — one row
--     per (device, port, vlan, mac), replaced snapshot by snapshot exactly
--     like lldp_neighbors. A MAC the bridge aged out leaves the table, because
--     the bridge is the witness and it no longer testifies. first_seen
--     survives replacement (upsert, not delete+insert) for the same reason
--     LLDP's does: "this laptop appeared thirty seconds ago" must not be the
--     answer for a machine plugged in since June.
--
--   * clients is the LEDGER: one row per MAC per site, never emptied by a
--     snapshot. This is what "waiting for the MAC to be rediscovered" means —
--     the bandwidth bucket accumulated on a port keeps belonging to the MAC
--     across unplugs, moves and reboots, and re-attaches when the FDB shows it
--     again. rx_bytes counts toward the client (the port's egress), tx_bytes
--     from it (the port's ingress).

create table mac_sightings (
    device_id  uuid not null references devices(id) on delete cascade,
    site_id    uuid not null references sites(id) on delete cascade,
    -- Facade port name, resolved by the controller at ingest (same rule as
    -- lldp_neighbors.local_port: a port we cannot name is dropped and counted,
    -- never guessed).
    port       text not null,
    -- 0 = the platform did not say (flat BRIDGE-MIB walk — SwOS), and screens
    -- must render that as unknown, never as "VLAN 0".
    vlan       integer not null default 0,
    -- Canonical spelling: lowercase colon-separated hex, produced by the
    -- collector itself (it decodes OID arcs; there is no vendor spelling to
    -- preserve, unlike LLDP chassis ids).
    mac        text not null,
    first_seen timestamptz not null default now(),
    last_seen  timestamptz not null default now(),
    primary key (device_id, port, vlan, mac)
);

create index mac_sightings_site_idx on mac_sightings(site_id);
create index mac_sightings_site_mac_idx on mac_sightings(site_id, mac);

create table clients (
    site_id    uuid not null references sites(id) on delete cascade,
    mac        text not null,
    first_seen timestamptz not null default now(),
    last_seen  timestamptz not null default now(),
    -- The bandwidth bucket: bytes attributed while the MAC was present on a
    -- non-uplink port, from the port's own octet counters. rx = toward the
    -- client (port egress), tx = from it (port ingress). bigint holds ~9.2 EB;
    -- nobody outgrows it.
    rx_bytes   bigint not null default 0,
    tx_bytes   bigint not null default 0,
    -- When the bucket last moved — what lets a screen say "counted until the
    -- port went down" honestly.
    bucket_at  timestamptz,
    primary key (site_id, mac)
);
