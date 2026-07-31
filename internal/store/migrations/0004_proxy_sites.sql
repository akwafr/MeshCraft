-- Un proxy peut couvrir plusieurs sites : le site porté par `proxies.site_id`
-- reste son site de rattachement (celui de l'enrollment, qui gouverne les
-- droits d'administration), et proxy_sites liste l'ensemble des sites servis.
create table proxy_sites (
    proxy_id uuid not null references proxies(id) on delete cascade,
    site_id  uuid not null references sites(id) on delete cascade,
    primary key (proxy_id, site_id)
);
create index proxy_sites_site_idx on proxy_sites (site_id);

-- Reprise de l'existant : chaque proxy couvre au moins son site d'enrollment.
insert into proxy_sites (proxy_id, site_id) select id, site_id from proxies;
