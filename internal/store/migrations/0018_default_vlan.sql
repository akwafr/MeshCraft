-- Le VLAN 1 existe sur tout équipement dès la sortie d'usine et sert de natif
-- implicite aux trunks. Un site qui ne le décrit pas décrit un parc qui n'existe
-- pas : on le rattrape sur l'existant, et store.CreateSite le pose désormais
-- dans la même transaction que le site.
insert into vlans (site_id, vlan_id, name, color, purpose)
select s.id, 1, 'Défaut', '#94a3b8', 'VLAN par défaut du site'
from sites s
where not exists (
    select 1 from vlans v where v.site_id = s.id and v.vlan_id = 1
);
