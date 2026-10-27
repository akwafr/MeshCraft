-- SINCE WHEN the device has been in the state `status` announces.
--
-- `status` alone cannot be read. "unknown" mixed two situations that do not
-- call for the same reaction: a device NEVER observed — adopted five minutes
-- ago, not pinged yet — and a device whose monitoring did establish a finding
-- the controller never received. The second case happened: a CSS326 stayed
-- "unknown" for nineteen hours while it answered flawlessly, its only
-- transition having vanished in a tunnel outage (fixed by the re-announcement
-- on reconnection, see tunnel.Event.Resync — this column makes the hole VISIBLE
-- should it ever form again elsewhere).
--
-- NULL = never observed. Same convention as `observed_at` just above, and for
-- the same reason: we do not pretend to know.
alter table devices
  add column if not exists status_at timestamptz;

-- What this date is, exactly — and what it is NOT.
--
-- It marks the moment the current finding was ESTABLISHED, not the moment the
-- device was last seen. The nuance is real and must be held: monitoring only
-- reports TRANSITIONS, so a device online for three months carries a
-- three-month-old date without anything being late. Reading this column as a
-- freshness would therefore raise alerts on the healthiest fleet.
--
-- What it does allow: saying "unreachable for 2 h" rather than "unreachable",
-- and telling "never observed" from everything else. A real freshness measure
-- would require a periodic heartbeat from the proxy — not built, and that is an
-- addition to the protocol, not a reading of this column.
comment on column devices.status_at is
  'Date d''établissement du constat de joignabilité (NULL = jamais observé). '
  'PAS une date de dernière vue : la surveillance ne remonte que des transitions.';
