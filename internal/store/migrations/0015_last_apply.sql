-- The result of the LAST configuration push.
--
-- A push can succeed while leaving things aside: the guardrail refuses to move
-- a port that carries administration, and it is right to do so. But if that
-- information lives only in an event's message, the operator sees an "applied"
-- configuration and a screen showing something else, without ever knowing why.
-- That is the worst of both worlds: the guardrail protects, and passes for a
-- bug.
--
-- So the detail is kept, attached to the device, to be able to explain it where
-- the question arises.
alter table devices
  add column if not exists last_apply jsonb not null default '{}'::jsonb;
