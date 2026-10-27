-- Addressing domains: giving an IP address a MEANING.
--
-- The problem. `192.168.1.10` identifies nobody. RFC 1918 space overlaps: the
-- same range declared on two proxies may designate two networks entirely
-- foreign to each other — or a single network reached by two paths. Nothing in
-- what meshcraft observes can settle it: two proxies placed in two branches in
-- 192.168.1.0/24 see exactly the same thing as two proxies placed in a single
-- branch.
--
-- What the schema did until now: the candidates' merge key fell back on the
-- (proxy, IP) pair. That is the cautious half of the answer — two distinct
-- networks stayed distinct — but the other half was missing: two proxies on ONE
-- SAME network produced two candidates for a single silent device, and nothing
-- could ever reunite them again.
--
-- The answer: a first-class object, the ADDRESSING DOMAIN — the space in which an
-- address designates one machine and only one. It is DECLARED, not deduced. A
-- deduction would get it wrong, and wrong on the bad side: wrongly merging two
-- networks fuses two devices into a single record, which cannot be undone,
-- whereas a duplicate is deleted in one click.
--
-- This migration KEEPS SEMANTICS CONSTANT. Each proxy gets its own domain, so
-- the (domain, IP) merge key is exactly worth the old (proxy, IP). Nothing
-- merges on its own: grouping two proxies into one domain is an explicit
-- operator gesture.

create table address_domains (
    id         uuid primary key default gen_random_uuid(),
    name       text not null,
    note       text not null default '',
    created_at timestamptz not null default now()
);

-- A proxy's OWN domain: that of everything it observes outside the ranges it
-- declares (manual probe of an arbitrary address, serial console). Without this
-- fallback, those observations would have no domain and would fall into a
-- common space where every private address would be confused.
alter table proxies add column address_domain_id uuid references address_domains(id) on delete set null;

-- A range still belongs to the proxy that can reach it — unchanged — and now
-- ALSO to the network it describes. The two notions are distinct: the proxy is
-- a path, the domain is an address space.
--
-- `on delete restrict`: deleting a domain still in use would silently change
-- the identity of every candidate it carries. It is forbidden, and the operator
-- moves their ranges first.
alter table scan_prefixes add column address_domain_id uuid references address_domains(id) on delete restrict;

alter table discovered add column address_domain_id uuid references address_domains(id) on delete set null;

-- On the adopted device, the domain serves MONITORING: it says which proxy is
-- legitimate to answer for its address. Nullable, because a device entered by
-- hand has none, and requiring a domain would cut off monitoring for the whole
-- fleet predating this model.
alter table devices add column address_domain_id uuid references address_domains(id) on delete set null;

-- One domain per existing proxy, named after it. That is what keeps the
-- migration's semantics constant.
--
-- The domain's identifier REUSES the proxy's: this is not a link of nature
-- between the two objects, only what makes the backfills below exact without a
-- join or a cursor. Domains created later will have their own identifier,
-- unrelated to any proxy.
insert into address_domains (id, name, note)
select p.id, 'Réseau de ' || p.name,
       'Domaine créé automatiquement à la migration 0020 : le réseau propre à ce proxy. '
       || 'Renommez-le, ou rattachez-y les plages d''un autre proxy si les deux regardent le même réseau.'
from proxies p;

update proxies p set address_domain_id = p.id;

update scan_prefixes sp set address_domain_id = sp.proxy_id;
alter table scan_prefixes alter column address_domain_id set not null;

-- Candidates already queued: their domain is that of their witness proxy. A
-- candidate without a proxy comes from the "Local" proxy (see migration 0005).
update discovered d
   set address_domain_id = coalesce(d.proxy_id, '00000000-0000-0000-0000-000000000001'::uuid);

-- Devices adopted from discovery know which candidate they come from
-- (`extra->>'discovered_id'`, set at adoption): the thread is followed back to
-- find their domain. Those that do not come from one stay NULL, and that is the
-- truth — nobody ever declared which network they live in.
update devices dev
   set address_domain_id = d.address_domain_id
  from discovered d
 where d.id::text = dev.extra->>'discovered_id'
   and d.address_domain_id is not null;

-- The merge key switches from (proxy, IP) to (domain, IP).
--
-- No duplicate can appear here: the freshly created domains are in bijection
-- with the proxies, so the set of keys is the same as before.
drop index if exists discovered_key_idx;
create unique index discovered_key_idx on discovered (
    coalesce(nullif(serial, ''), nullif(mac, ''),
             coalesce(address_domain_id::text, 'sans-domaine') || '|' || ip)
);

create index scan_prefixes_domain_idx on scan_prefixes (address_domain_id);
create index devices_domain_idx on devices (address_domain_id);
