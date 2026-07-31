-- La couverture de sites d'un proxy revient, avec un rôle NOUVEAU : ce n'est
-- plus un fait décoratif (l'ancienne proxy_sites de la migration 0004 ne
-- décidait rien), c'est la FRONTIÈRE DE DROITS du proxy.
--
--   - qui voit le proxy    : quiconque a un rôle sur un site couvert ;
--   - qui l'administre     : site_admin d'un site couvert (son site, et son
--                            site seulement) ;
--   - pour quels sites il  : les sites couverts.
--     peut être utilisé
--
-- Deux notions DISTINCTES, et il ne faut pas les confondre :
--
--   global = true  → proxy d'instance, utilisable partout, administré par les
--                    seuls global-admins.
--   global = false → proxy restreint aux sites de proxy_sites. Une couverture
--                    VIDE ne signifie donc PAS « partout » : elle signifie
--                    « nulle part ». Sans cette distinction, un site-admin
--                    retirant son site d'un proxy le promouvrait en proxy
--                    global — une escalade de privilèges.
--
-- Ce qui reste inchangé : la couverture ne dit pas ce que le proxy PEUT
-- joindre (c'est observé, cf. device_paths), ni le site d'un équipement
-- découvert (c'est l'adoption qui le choisit).
alter table proxies add column if not exists global boolean not null default true;

create table if not exists proxy_sites (
    proxy_id uuid not null references proxies(id) on delete cascade,
    site_id  uuid not null references sites(id) on delete cascade,
    primary key (proxy_id, site_id)
);
create index if not exists proxy_sites_site_idx on proxy_sites (site_id);

-- La couverture voulue est décidée à l'ÉMISSION du token : le proxy, lui,
-- n'existe qu'au moment où le token est consommé. On fige donc l'intention à
-- l'émission plutôt que de relire les droits de l'émetteur plus tard — ils
-- peuvent avoir changé entre-temps.
alter table enroll_tokens add column if not exists global boolean not null default false;
alter table enroll_tokens add column if not exists site_ids uuid[] not null default '{}';
