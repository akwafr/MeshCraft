-- Switch profiles: describing a device's faceplate through RULES.
--
-- The problem. Port profiles (`port_profiles`) say what ONE jack receives;
-- nothing says which jacks receive it. Configuring a 48-port switch therefore
-- means selecting ranges by hand, device by device. That is redoing forty-eight
-- times a gesture whose intent fits in three sentences: "the SFP+ in trunk, the
-- last four RJ45 in trunk PVID 100, everything else in access PVID 100".
--
-- What was missing is not a place to store a configuration — the model already
-- has three — but a place to store RULES, i.e. an intent that designates SETS
-- of jacks instead of naming them. A profile naming "GE45" only works for one
-- faceplate; a profile saying "the last four RJ45" works for a 48, a 24 and an
-- 8-port switch. That is the whole difference between a profile and a
-- copy-paste.
--
-- Why one more table, and not a column on `device_profiles`. The device profile
-- carries the common services (STP, SNMP, syslog): it is an object that applies
-- to a WHOLE device and whose implementation remains to be done. The switch
-- profile only talks about ports, and it is the only one of the two that is
-- executable today. Merging them would force deciding now how the two fit
-- together — the order of application, which one wins — without having the
-- second at hand to judge.
--
-- The shape, on the other hand, is STRICTLY that of the two other profiles
-- (id, site_id, name, spec jsonb, unique per site). That is not a decorative
-- coincidence: `store.profileTable` then accepts a third `kind`, and the whole
-- CRUD, the `/api/sites/{id}/profiles/{kind}` routes, the events and the
-- real-time stream's site resolution work without one more line. A table of a
-- different shape would have cost a second CRUD, which would have drifted.
--
-- Contents of `spec` (see internal/portrules):
--
--   {"description": "...",
--    "rules": [
--      {"label": "SFP+ as trunk",
--       "select": {"media": "sfp_plus"},
--       "config": {"mode": "trunk", "pvid": 1, "tagged": "all", "poe": false}},
--      {"label": "last 4 RJ45",
--       "select": {"media": "copper", "slice": "last", "count": 4},
--       "port_profile_id": "…"}
--    ]}
--
-- The spec is not constrained by the schema, like the two others: validation
-- lives in `internal/portrules`, next to the code that evaluates it. An SQL
-- constraint on jsonb would say "constraint violation" where the operator needs
-- "the rule "uplinks" designates no jack".

create table switch_profiles (
    id      uuid primary key default gen_random_uuid(),
    site_id uuid not null references sites(id) on delete cascade,
    name    text not null,
    spec    jsonb not null default '{}'::jsonb,
    unique (site_id, name)
);

-- No device REFERENCES its switch profile, and that is deliberate.
--
-- A switch profile is APPLIED: it produces the ports' intent, which then lives
-- in `devices.ports` like any other configuration, and which the operator stays
-- free to touch up port by port. Keeping a device → profile link would turn
-- every local touch-up into a "drift from the profile" to be arbitrated, whereas
-- touching up is the normal way of working (see
-- docs/en/01-Architecture/Reconciliation.md: drift is about the gap between
-- meshcraft and the DEVICE, not between two meshcraft intents).
--
-- The counterpart is accepted: changing a profile re-applies nothing on its
-- own. The day a persistent link is wanted, it will be added — the reverse,
-- removing a link the whole fleet depends on, cannot be improvised.
