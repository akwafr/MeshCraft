-- Plan d'adressage de management, par site
-- (cf. docs/01-Architecture/Adressage de management).
--
-- Le plan dit COMMENT un équipement adopté devient joignable pour
-- l'administration : quel VLAN, quel sous-réseau, quelle passerelle, et si
-- l'adresse vient d'un DHCP externe ou de meshcraft.
--
-- Rappel de la règle structurante : « géré par meshcraft » signifie que meshcraft
-- ALLOUE une adresse dans la plage et la pousse en configuration STATIQUE. Il
-- n'émet aucun paquet DHCP — ce n'est pas un serveur DHCP.
create table if not exists site_mgmt_plan (
    site_id    uuid primary key references sites(id) on delete cascade,
    -- VLAN d'administration. untagged = management sur le VLAN natif, ce qui est
    -- le seul mode possible sur certains équipements.
    vlan_id    int not null default 1,
    untagged   boolean not null default true,

    subnet_v4  text not null default '',
    gateway_v4 text not null default '',
    subnet_v6  text not null default '',
    gateway_v6 text not null default '',
    dns        text[] not null default '{}',
    ntp        text[] not null default '{}',

    -- dhcp_external : meshcraft configure le client DHCP et n'alloue rien.
    -- meshcraft     : meshcraft choisit l'adresse dans la plage ci-dessous.
    source     text not null default 'dhcp_external',
    pool_start text not null default '',
    pool_end   text not null default '',

    -- strict_plan : refuser l'adoption d'un équipement incapable de porter ce
    -- plan, au lieu de l'accepter hors plan. Activé par défaut.
    strict_plan boolean not null default true,

    updated_by uuid references users(id) on delete set null,
    updated_at timestamptz not null default now()
);

-- IPAM : les adresses de management allouées par meshcraft.
--
-- L'unicité est portée par la BASE et non par le code : deux adoptions
-- simultanées ne peuvent pas choisir la même adresse. Une adresse n'est jamais
-- réutilisée tant qu'un équipement peut encore la porter — un switch éteint
-- revient avec son ancienne configuration.
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
