-- VLAN 1 exists on every device straight out of the factory and serves as the
-- implicit native VLAN of trunks. A site that does not describe it describes a
-- fleet that does not exist: it is backfilled on existing sites, and
-- store.CreateSite now sets it in the same transaction as the site.
insert into vlans (site_id, vlan_id, name, color, purpose)
select s.id, 1, 'Défaut', '#94a3b8', 'VLAN par défaut du site'
from sites s
where not exists (
    select 1 from vlans v where v.site_id = s.id and v.vlan_id = 1
);
