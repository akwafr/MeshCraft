-- Générations de secrets : on n'écrase JAMAIS un mot de passe, on en ajoute un.
--
-- Le problème que le modèle naïf ne voit pas : changer un mot de passe dans
-- l'admin ne le change pas sur les équipements. Entre l'instant du changement et
-- la fin du déploiement — qui peut durer des jours, un équipement éteint, un site
-- coupé — le parc porte PLUSIEURS secrets à la fois. Écraser le secret en base,
-- c'est perdre l'accès à tout ce qui n'a pas encore été mis à jour.
--
-- Cf. docs/03-Sécurité/Identité de service et rotation §3.

-- Un site peut demander un secret DISTINCT PAR ÉQUIPEMENT plutôt qu'un secret
-- partagé. Plus sûr (un équipement compromis ne livre pas le parc), moins
-- commode (aucun mot de passe à retenir, il faut passer par meshcraft).
alter table site_credentials add column if not exists per_device boolean not null default false;

create table if not exists credential_generations (
    id            uuid primary key default gen_random_uuid(),
    credential_id uuid not null references site_credentials(id) on delete cascade,
    -- device_id NULL = le secret partagé de la portée. Renseigné quand le
    -- credential est en mode « par équipement ».
    device_id     uuid references devices(id) on delete cascade,
    generation    int not null,
    secret_enc    bytea not null,

    -- État du SECRET lui-même. L'état par ÉQUIPEMENT vit dans
    -- device_credential_state : les deux ne disent pas la même chose.
    --   pending  : créé, pas encore poussé nulle part
    --   active   : c'est la génération que l'on pousse désormais
    --   retiring : remplacée, mais des équipements la portent encore — donc
    --              toujours valide pour se connecter
    --   retired  : plus aucun équipement connu ne la porte
    --   compromised : à retirer sans délai
    state         text not null default 'pending',

    created_at    timestamptz not null default now(),
    activated_at  timestamptz,
    retired_at    timestamptz,
    created_by    uuid references users(id) on delete set null,
    unique (credential_id, device_id, generation)
);
create index if not exists credential_generations_cred_idx
    on credential_generations (credential_id, generation desc);

-- Ce qu'un ÉQUIPEMENT porte réellement. Donnée OBSERVÉE : corrigée par ce que
-- les sessions constatent, jamais décrétée.
create table if not exists device_credential_state (
    device_id            uuid not null references devices(id) on delete cascade,
    credential_id        uuid not null references site_credentials(id) on delete cascade,
    installed_generation int,
    installed_at         timestamptz,
    last_verified_at     timestamptz,
    last_error           text not null default '',
    primary key (device_id, credential_id)
);

-- Les identifiants déjà saisis deviennent la génération 1, active : sans ça ils
-- seraient invisibles au nouveau mécanisme et le parc perdrait son mot de passe.
insert into credential_generations (credential_id, device_id, generation, secret_enc,
                                    state, activated_at)
select id, null, 1, secret_enc, 'active', rotated_at
from site_credentials
where octet_length(secret_enc) > 0
on conflict do nothing;
