-- The state ACTUALLY read from the device, and what is needed to attribute a gap.
-- See docs/en/01-Architecture/Reconciliation.md §2 and §6.
--
-- Until now `devices.ports` carried ONE configuration, without saying whether it
-- was what meshcraft wants or what the device carries. The two do not coincide:
-- between a change made in meshcraft and its push, and between a change made by
-- hand on the switch and its detection, the fleet lives with two truths.
-- Conflating them means never being able to say which one is behind.
alter table devices
  add column if not exists observed jsonb not null default '{}'::jsonb;

-- When the device was read back. NULL = never: we do not pretend to know.
alter table devices
  add column if not exists observed_at timestamptz;

-- When the intent changed IN meshcraft, and when it was pushed.
--
-- These two dates are what lets a gap be attributed without guessing: if the
-- intent has not moved since the last push and the device differs anyway, then
-- it is the DEVICE that changed. That is the only case worth interrupting the
-- operator for.
alter table devices
  add column if not exists desired_changed_at timestamptz;

alter table devices
  add column if not exists applied_at timestamptz;
