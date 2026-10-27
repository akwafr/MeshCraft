-- A proxy is a network point of presence of the INSTANCE: it has neither a home
-- site nor a list of covered sites
-- (see docs/en/01-Architecture/Proxy.md — "A proxy is bound neither to a site,
-- nor to a device").
--
-- Coverage decided nothing: it restricted neither scans, nor the site of
-- discovered devices, nor access paths. It only forced the operator to answer,
-- at deployment, a question they do not have the answer to. What a proxy
-- reaches is OBSERVED (device_paths); a device's site comes from adoption.
drop table if exists proxy_sites;
alter table proxies drop column if exists site_id;

-- Direct consequence: the enrollment token becomes instance-scoped. Enrolling a
-- point of presence into the instance is no longer a site administrator's
-- prerogative (see docs/en/03-Security/RBAC.md).
alter table enroll_tokens drop column if exists site_id;
