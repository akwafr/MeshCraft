-- Secret generations: a password is NEVER overwritten, one is added.
--
-- The problem the naive model does not see: changing a password in the admin
-- does not change it on the devices. Between the moment of the change and the
-- end of the deployment — which may take days, a device switched off, a site
-- cut off — the fleet carries SEVERAL secrets at once. Overwriting the secret
-- in the database means losing access to everything not yet updated.
--
-- See docs/en/03-Security/Service identity and rotation.md §3.

-- A site may ask for a DISTINCT SECRET PER DEVICE rather than a shared secret.
-- Safer (a compromised device does not hand over the fleet), less convenient
-- (no password to remember, one has to go through meshcraft).
alter table site_credentials add column if not exists per_device boolean not null default false;

create table if not exists credential_generations (
    id            uuid primary key default gen_random_uuid(),
    credential_id uuid not null references site_credentials(id) on delete cascade,
    -- device_id NULL = the scope's shared secret. Filled in when the credential is
    -- in "per device" mode.
    device_id     uuid references devices(id) on delete cascade,
    generation    int not null,
    secret_enc    bytea not null,

    -- State of the SECRET itself. The per-DEVICE state lives in
    -- device_credential_state: the two do not say the same thing.
    --   pending  : created, not pushed anywhere yet
    --   active   : this is the generation now being pushed
    --   retiring : replaced, but devices still carry it — hence still valid to
    --              log in
    --   retired  : no known device carries it any more
    --   compromised : to be removed without delay
    state         text not null default 'pending',

    created_at    timestamptz not null default now(),
    activated_at  timestamptz,
    retired_at    timestamptz,
    created_by    uuid references users(id) on delete set null,
    unique (credential_id, device_id, generation)
);
create index if not exists credential_generations_cred_idx
    on credential_generations (credential_id, generation desc);

-- What a DEVICE actually carries. OBSERVED data: corrected by what sessions
-- find, never decreed.
create table if not exists device_credential_state (
    device_id            uuid not null references devices(id) on delete cascade,
    credential_id        uuid not null references site_credentials(id) on delete cascade,
    installed_generation int,
    installed_at         timestamptz,
    last_verified_at     timestamptz,
    last_error           text not null default '',
    primary key (device_id, credential_id)
);

-- Credentials already entered become generation 1, active: without that they
-- would be invisible to the new mechanism and the fleet would lose its password.
insert into credential_generations (credential_id, device_id, generation, secret_enc,
                                    state, activated_at)
select id, null, 1, secret_enc, 'active', rotated_at
from site_credentials
where octet_length(secret_enc) > 0
on conflict do nothing;
