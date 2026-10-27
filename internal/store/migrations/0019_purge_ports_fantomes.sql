-- Purge of GHOST ports created by SNMP telemetry.
--
-- Telemetry names a port "prefix + ifIndex". Yet a device exposes far more
-- interfaces over SNMP than its faceplate has ports: VLAN interfaces,
-- aggregates, loopbacks — seen on a 1930 with indexes at 1000, 3000, 20000 and
-- 100000. Written as is, they showed up in the inventory as ports
-- ("GE100041"), next to the real ones.
--
-- They were harmless for reconciliation — without a `mode` they count as
-- unmanaged and the diff ignores them — but they bloated the inventory: 93
-- entries for 52 real ports on the switch that served to flush them out.
--
-- The source is plugged on the controller side (see applyPortTelemetry:
-- telemetry never CREATES a port any more, it only updates the faceplate's).
-- What remains is the residue left by previous versions, which nothing cleaned
-- up: a telemetry merge adds keys, it removes none.
--
-- Three precautions, because a migration that deletes ports by mistake deletes
-- somebody's configuration work:
--
--   1. the faceplate is authoritative — a port is deleted only if its name falls
--      into NO range declared by `port_groups`;
--   2. a device whose faceplate is unknown or unreadable is not touched: we do
--      not delete on the strength of a list we could not read;
--   3. any entry carrying an INTENT (`mode`, `label`, `pvid`, `tagged`) is
--      spared, even outside a range. A configured entry is not a ghost,
--      whatever its name — and if its name is surprising, that is an anomaly
--      to look at, not to erase silently.
with plages as (
    select d.id,
           g->>'prefix'      as prefix,
           (g->>'from')::int as depart,
           (g->>'count')::int as nombre
    from devices d,
         lateral jsonb_array_elements(d.port_groups) g
    where jsonb_typeof(d.port_groups) = 'array'
),
noms as (
    select p.id, p.prefix || s.n::text as nom
    from plages p
    cross join lateral generate_series(p.depart, p.depart + p.nombre - 1) as s(n)
    where p.prefix is not null
      and p.depart >= 0
      and p.nombre between 1 and 4096
),
fantomes as (
    select d.id, k.key
    from devices d,
         lateral jsonb_object_keys(d.ports) k(key)
    where jsonb_typeof(d.ports) = 'object'
      -- (2) faceplate known, otherwise abstain
      and exists (select 1 from noms n where n.id = d.id)
      -- (1) outside every range of the faceplate
      and not exists (select 1 from noms n where n.id = d.id and n.nom = k.key)
      -- (3) no intent carried
      and coalesce(d.ports -> k.key ->> 'mode', '') = ''
      and coalesce(d.ports -> k.key ->> 'label', '') = ''
      and not (d.ports -> k.key ? 'pvid')
      and not (d.ports -> k.key ? 'tagged')
)
update devices d
set ports = coalesce(
        (select jsonb_object_agg(k.key, d.ports -> k.key)
         from lateral jsonb_object_keys(d.ports) k(key)
         where not exists (
             select 1 from fantomes f where f.id = d.id and f.key = k.key
         )),
        '{}'::jsonb
    )
where exists (select 1 from fantomes f where f.id = d.id);
