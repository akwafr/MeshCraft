-- REACHABILITY IS NOW PER PROXY, and the device's single status becomes a verdict
-- COMPUTED from several observations instead of the last one written.
--
-- # The failure this repairs, observed on 2026-08-02
--
-- A newly enrolled instance proxy connected at 15:35:35 and, 3.7 seconds later,
-- declared four devices of another site unreachable — every one of them alive and
-- being polled successfully by their own collector at that very moment. The new
-- proxy sat on a different network (172.16.99.0/24), was handed the whole
-- inventory because an instance proxy covers every site, pinged addresses that do
-- not exist there, and missed three rounds. `applyDeviceState` wrote its verdict
-- because nothing distinguished it from any other.
--
-- Two properties of the old design made that possible and both are addressed here:
--
--   * ONE COLUMN, LAST WRITER WINS. `devices.status` held a single value with no
--     record of who established it, so a proxy that could not reach a device
--     overwrote the verdict of one that could;
--   * NOTHING TO OUTRANK IT WITH. The proxy actually polling the device knew it
--     by SNMP; that knowledge existed nowhere the status decision could consult.
--
-- The correcting rule, in one sentence:
--
--     a device is online if a proxy that has PROVEN which device it is talking to
--     sees it, whatever any other proxy says.
--
-- # Why proof, and not network topology
--
-- The obvious fix was to stop giving a proxy devices outside its address domain.
-- That is topology — a declaration an operator makes, and therefore something
-- that can be wrong, missing, or out of date, which is exactly the state the
-- fleet was in. Identity is EVIDENCE: sysDescr and sysObjectID read from the
-- device itself, matched against the vendor and model in the inventory. A proxy
-- that has produced that match is talking to the right equipment, and no
-- declaration is needed to establish it.
--
-- Address domains keep their job (disambiguating two devices at one address);
-- they simply stop being the only thing standing between a misplaced proxy and
-- the fleet's status column.

create table if not exists device_proxy_reach (
  device_id      uuid        not null references devices(id) on delete cascade,
  proxy_id       uuid        not null references proxies(id) on delete cascade,
  -- reachable: this proxy's OWN verdict. NULL = it watches the device but has
  -- not concluded yet, which is distinct from "it says down" and must not be
  -- aggregated as one.
  reachable      boolean,
  reachable_at   timestamptz,
  misses         integer     not null default 0,
  latency_ms     integer     not null default 0,
  -- confirmed_at: when this proxy last PROVED the device's identity by SNMP.
  --
  -- Sticky by design — it records that proof happened, not that it is currently
  -- happening. A device that stops answering SNMP (community rotated, agent
  -- wedged, or simply switched off) must keep the trust its proxy earned, or the
  -- outage itself would demote the only observer able to report it.
  confirmed_at   timestamptz,
  -- confirmed_by: what actually matched, in words a screen can show
  -- ("sysObjectID → MikroTik, sysDescr → CSS326-24G-2S+"). The reason is kept
  -- with the fact: "confirmed" alone leaves nobody able to judge how much the
  -- match was worth.
  confirmed_by   text        not null default '',
  primary key (device_id, proxy_id)
);

-- The aggregation reads every row of one device on each transition: that is the
-- hot path, and it is the only one.
create index if not exists device_proxy_reach_device_idx
  on device_proxy_reach (device_id);

comment on table device_proxy_reach is
  'One reachability verdict per (device, proxy), plus whether that proxy has ever '
  'proven the device''s identity by SNMP. devices.status is computed from these '
  'rows (cf. internal/api/reachability.go), never written directly by one proxy.';
comment on column device_proxy_reach.reachable is
  'This proxy''s own verdict: true seen, false missed, NULL not concluded yet.';
comment on column device_proxy_reach.confirmed_at is
  'When this proxy last matched sysDescr/sysObjectID against the inventory. '
  'Sticky: an outage must not demote the observer that can report it.';

-- status_note now carries WHICH proxy established the verdict, so a false red
-- from a misattached collector is diagnosable from the device sheet rather than
-- from controller logs. The column already exists; only its meaning widens.
comment on column devices.status_note is
  'Stable description of the current ICMP verdict, naming the proxy that '
  'established it and, when it lost a device it had confirmed, when it last '
  'proved that device''s identity.';
