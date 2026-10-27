-- Pins the MST region name of every EXISTING site to its current slug.
-- See ADR-0021 — An MST region name is an identity, not a label.
--
-- # This migration changes nothing, and that is its whole purpose
--
-- Until now `internal/api/stp_apply.go` derived the MST region name from the
-- site's slug. The slug is editable (`PATCH /api/sites/{id}` accepts a `slug`
-- field), so the region name was too — and in MSTP the region name is not a
-- label: together with the revision and the MD5 digest of the 4096-entry
-- VLAN→instance table, it forms the region's IDENTITY. Two bridges are in the
-- same region if and only if all three match bit for bit.
--
-- Renaming a device's region EJECTS it from its region. It becomes its own
-- single-member region, the MST boundaries move, the topology is recomputed, and
-- nothing signals it: the neighbour is simply treated as an ordinary RSTP
-- bridge. On a production fabric that is an interruption.
--
-- The fix derives the names from an immutable identifier (the site's UUID)
-- instead of the slug. Applied alone, it would RENAME every region already set —
-- exactly the accident it exists to prevent. Hence this migration: by writing
-- the current slug into the site plan, it freezes the value the old code
-- produced the day before. After it runs, the region computed for every
-- pre-existing site is identical BYTE FOR BYTE to the previous one. The generic
-- name therefore only concerns sites created afterwards, which by construction
-- have no hardware carrying a region.
--
-- # Why the site plan, and why not a column
--
-- `sites.plan` is a free-form `jsonb` that already carries `stp_mst_revision`,
-- the other adjustable third of the same identity. The two values are read and
-- decided together; separating them into two different surfaces would invite
-- changing one without seeing the other, whereas changing either produces the
-- same accident.
--
-- # Why the slug and not what the devices actually carry
--
-- Because an SQL migration does not talk to devices, and making it able to would
-- mean opening a session on every switch of every site during a `migrate`.
-- Moreover "the majority region" makes no sense when the fleet is half offline
-- (four of ARENA's twelve Arubas did not answer the 2026-10-25 survey). Aligning
-- a site on a name its hardware already carries is an OPERATOR gesture, informed
-- by a survey, through `PUT /api/sites/{id}/plan` — not an automatic deduction.
--
-- # Idempotence
--
-- The clause on an empty or absent `plan->>'stp_mst_region'` guarantees that a
-- name already pinned — by this migration, or deliberately by an operator — is
-- never overwritten. A site without a slug is not pinned: it had no region
-- before (the old code returned `MSTRegion{}` for an empty slug), so it will
-- take the generic name, and that renames nothing.

update sites
   set plan = coalesce(plan, '{}'::jsonb)
            || jsonb_build_object('stp_mst_region', slug)
 where coalesce(slug, '') <> ''
   and coalesce(plan->>'stp_mst_region', '') = '';
