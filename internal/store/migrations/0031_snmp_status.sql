-- SNMP reachability, DISTINCT from ICMP ping status.
--
-- `status` is established by ping alone (cf. docs/01-Architecture/Monitoring.md).
-- A device can answer ping while SNMP collection is dead (wrong community, agent
-- down, collector detached) — and the reverse also happens when ICMP is filtered.
-- One indicator conflated the two and made the collector-failure case invisible:
-- the ping light stayed green while curves went flat.
--
-- Same vocabulary and NULL convention as `status` / `status_at`: unknown until
-- observed, date moves only on a real change (see SetDeviceSNMPStatus).
alter table devices
  add column if not exists snmp_status text not null default 'unknown',
  add column if not exists snmp_status_note text not null default '',
  add column if not exists snmp_status_at timestamptz;

comment on column devices.snmp_status is
  'SNMP reachability as collection establishes it (ok | off | unknown). '
  'Distinct from status (ICMP).';
comment on column devices.snmp_status_at is
  'When the current SNMP observation was established (NULL = never observed).';
