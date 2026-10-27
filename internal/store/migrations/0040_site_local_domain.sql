-- The local domain name, PER SITE.
--
-- # What was wrong before
--
-- "meshcraft.local" was a connector constant (`cisco/ios.DefaultDomainName`),
-- pushed onto every switch a site provisions. It is the name the device then
-- carries in its own FQDN — the lab fleet's LLDP neighbourhood shows it as is:
-- "CiscoLabFirst1.meshcraft.local". In other words, a naming choice belonging
-- to the operator was frozen in the binary, identical for every site, and
-- impossible to correct without recompiling.
--
-- A site that already has a domain — because it resolves one, because its
-- certificates carry one, because its naming convention requires it — ended up
-- with devices announcing a domain that exists nowhere on its side.
--
-- # Why a column and not a `plan` entry
--
-- `sites.plan` carries the addressing PLAN: intents meshcraft compares with the
-- observed state and reconciles. The domain is not of that nature — it is not
-- reconciled, it is written — and storing it there would have brought it into
-- drift computations it has no business in.
--
-- # The default
--
-- "meshcraft.local": exactly what the code already pushed, so the migration
-- changes the behaviour of NO existing site. `.local` is reserved for mDNS (RFC
-- 6762), which is debatable in itself — but fixing it here would change the
-- FQDN of devices already provisioned, and that is not for a migration to
-- decide. An operator who wants better sets it now.
--
-- Empty is ACCEPTED and means "this site has no domain": connectors that need one
-- (IOS, whose "crypto key generate rsa" requires it) then fall back on their own
-- default, rather than writing an empty line that would make SSH silently
-- impossible.
alter table sites
    add column if not exists local_domain text not null default 'meshcraft.local';

-- No space, no slash, no leading or trailing dot: this text goes into "ip
-- domain-name", into an FQDN and into a certificate SAN, where each of these
-- forms produces a refused line or an invalid certificate. The empty string
-- stays allowed — it is "no domain", not a mistake.
alter table sites
    drop constraint if exists sites_local_domain_ck;
alter table sites
    add constraint sites_local_domain_ck
    check (local_domain = '' or local_domain ~ '^[a-zA-Z0-9]([a-zA-Z0-9.-]{0,251}[a-zA-Z0-9])?$');
