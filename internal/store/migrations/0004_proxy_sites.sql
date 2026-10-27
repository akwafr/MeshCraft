-- A proxy may cover several sites: the site carried by `proxies.site_id`
-- remains its home site (the enrollment one, which governs administration
-- rights), and proxy_sites lists every site served.
create table proxy_sites (
    proxy_id uuid not null references proxies(id) on delete cascade,
    site_id  uuid not null references sites(id) on delete cascade,
    primary key (proxy_id, site_id)
);
create index proxy_sites_site_idx on proxy_sites (site_id);

-- Backfill: every proxy covers at least its enrollment site.
insert into proxy_sites (proxy_id, site_id) select id, site_id from proxies;
