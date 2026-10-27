-- HEAD SECRET RESYNCHRONIZED WITH THE ACTIVE GENERATION — a data repair, no schema
-- change.
--
-- # The failure this repairs, observed on 2026-08-05
--
-- Head-reveal on credential `mcadmin@site` returned a secret every device refused,
-- while revealing generation 3 (`active`) returned the working one. The head row's
-- `rotated_at` equalled generation 1's `created_at` to the microsecond: rotations 2
-- and 3 were made by a code version whose AddGeneration created the generation row
-- WITHOUT advancing the denormalized copy in `site_credentials.secret_enc`. Current
-- AddGeneration advances it in the same transaction — but nothing ever went back to
-- repair rows diverged before that catch-up existed.
--
-- The stale head fed every consumer of RevealSiteCredential: the reveal screen, SNMP
-- push, trap destinations, the watch. Nothing on screen distinguished the stale
-- secret from the right one — it is well-formed, merely refused by the fleet.
--
-- RevealSiteCredential now reads the active generation directly (correct by
-- construction); this migration additionally repairs the stored copy so that nothing
-- reading the column raw — a future query, an operator at psql — is lied to.
--
-- Only SITE-LEVEL generations (`device_id is null`) qualify: per-device generations
-- answer "what does THIS device carry", never "what is the credential's current
-- pair". The second phrase follows AddGeneration's own coalesce rule: a generation
-- without one (every non-v3 credential) must not erase the head's.
update site_credentials c
   set secret_enc  = g.secret_enc,
       secret2_enc = coalesce(g.secret2_enc, c.secret2_enc)
  from (
    select distinct on (credential_id) credential_id, secret_enc, secret2_enc
      from credential_generations
     where device_id is null and state = 'active'
     order by credential_id, generation desc
  ) g
 where g.credential_id = c.id
   and (c.secret_enc is distinct from g.secret_enc
        or coalesce(g.secret2_enc, c.secret2_enc) is distinct from c.secret2_enc);
