-- Automatic proxy update
-- (see docs/en/01-Architecture/Proxy versioning and updates.md).
--
-- `version` used to carry the product version (0.1.0): it now carries the build
-- timestamp YYYYMMDD.HHMMSS, fixed-width, hence comparable as text. `protocol`
-- states tunnel compatibility, independently of the build date: a timestamp
-- cannot decide what we refuse.
alter table proxies add column if not exists protocol int not null default 0;
alter table proxies add column if not exists os text not null default '';
alter table proxies add column if not exists arch text not null default '';

-- Per-proxy policy. auto_upgrade true by default: a fleet someone forgets to
-- update is a vulnerable fleet, and the automatic rollback on the proxy side
-- makes the default safe.
alter table proxies add column if not exists auto_upgrade boolean not null default true;
-- pin_version: the exact version targeted (pinning or rollback). Takes
-- precedence over the instance's target.
alter table proxies add column if not exists pin_version text not null default '';
-- upgrade_requested: "Update now", which bypasses auto_upgrade WITHOUT
-- changing it. Consumed when the offer is issued.
alter table proxies add column if not exists upgrade_requested boolean not null default false;

-- The proxy's last report, shown as is in the UI: a silent failure would be
-- worth less than no update at all.
alter table proxies add column if not exists upgrade_state text not null default '';
alter table proxies add column if not exists upgrade_version text not null default '';
alter table proxies add column if not exists upgrade_error text not null default '';
alter table proxies add column if not exists upgrade_started_at timestamptz;
alter table proxies add column if not exists upgraded_at timestamptz;

-- Already enrolled proxies still announce a product version: we do not pretend
-- to know which build they run. Their next heartbeat will say.
update proxies set version = '' where version not like '________.______' and not internal;
