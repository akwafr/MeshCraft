-- Optional enrollment tag for switch profiles: devices carrying this tag
-- receive the profile (apply on tag assign). Empty/null = no auto-enroll.
-- Soft link only — no device→profile FK; untag does not wipe ports.

alter table switch_profiles add column tag text;

-- Normalize empties to null so uniqueness ignores non-enrolling profiles.
update switch_profiles set tag = null where tag is not null and btrim(tag) = '';

create unique index switch_profiles_site_tag_key
  on switch_profiles (site_id, lower(tag))
  where tag is not null and tag <> '';
