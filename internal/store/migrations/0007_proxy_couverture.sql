-- A proxy's site coverage comes back, with a NEW role: it is no longer a
-- decorative fact (migration 0004's former proxy_sites decided nothing), it is
-- the proxy's RIGHTS BOUNDARY.
--
--   - who sees the proxy      : anyone with a role on a covered site;
--   - who administers it      : site_admin of a covered site (their site, and
--                               their site only);
--   - for which sites it      : the covered sites.
--     may be used
--
-- Two DISTINCT notions, which must not be confused:
--
--   global = true  → instance proxy, usable everywhere, administered by
--                    global-admins only.
--   global = false → proxy restricted to the sites in proxy_sites. An EMPTY
--                    coverage therefore does NOT mean "everywhere": it means
--                    "nowhere". Without this distinction, a site-admin removing
--                    their site from a proxy would promote it to a global proxy
--                    — a privilege escalation.
--
-- What stays unchanged: coverage does not say what the proxy CAN reach (that is
-- observed, see device_paths), nor the site of a discovered device (adoption
-- chooses it).
alter table proxies add column if not exists global boolean not null default true;

create table if not exists proxy_sites (
    proxy_id uuid not null references proxies(id) on delete cascade,
    site_id  uuid not null references sites(id) on delete cascade,
    primary key (proxy_id, site_id)
);
create index if not exists proxy_sites_site_idx on proxy_sites (site_id);

-- The intended coverage is decided when the token is ISSUED: the proxy itself
-- only exists when the token is consumed. The intent is therefore frozen at
-- issuance rather than re-reading the issuer's rights later — they may have
-- changed in the meantime.
alter table enroll_tokens add column if not exists global boolean not null default false;
alter table enroll_tokens add column if not exists site_ids uuid[] not null default '{}';
