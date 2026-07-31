-- Pipelines : rendre visible ce qui est en cours, et surtout ce qui coince.
-- Voir docs/01-Architecture/Monitoring unifié et pipelines §5.
--
-- Jusqu'ici, une pose qui échouait laissait un événement et rien d'autre : il
-- fallait deviner. Un déploiement de configuration, une pose d'identifiant, une
-- campagne de supervision prennent du temps, échouent partiellement et restent
-- coincés — c'est la règle, pas l'exception, sur un parc réel.
create table if not exists pipelines (
    id         uuid primary key default gen_random_uuid(),
    site_id    uuid not null references sites(id) on delete cascade,
    -- kind : config | credential | monitoring | import
    kind       text not null,
    -- device_id nul = campagne de parc, qui n'appartient à aucun équipement.
    device_id  uuid references devices(id) on delete cascade,
    -- state : pending | running | blocked | done | failed
    --
    -- `blocked` n'est PAS un échec : un équipement éteint, un proxy déconnecté,
    -- une fenêtre de maintenance fermée. Le pipeline attend, et dit quoi. Les
    -- confondre pousse à relancer ce qui n'a pas besoin de l'être.
    state      text not null default 'pending',
    -- label : ce que le pipeline fait, en français, pour une liste lisible.
    label      text not null default '',
    -- blocked_reason est une PHRASE, pas un code. « En attente : le proxy
    -- `proxy-festival` est déconnecté depuis 4 min » se lit ; ERR_PROXY_DOWN se
    -- cherche.
    blocked_reason text not null default '',
    created_at timestamptz not null default now(),
    started_at timestamptz,
    ended_at   timestamptz,
    created_by uuid references users(id) on delete set null
);

create index if not exists pipelines_site_idx on pipelines (site_id, created_at desc);
-- Les états qui appellent une action se cherchent en premier.
create index if not exists pipelines_state_idx on pipelines (state, created_at desc);

create table if not exists pipeline_steps (
    id          uuid primary key default gen_random_uuid(),
    pipeline_id uuid not null references pipelines(id) on delete cascade,
    seq         int not null,
    name        text not null,
    -- state : done | failed | skipped
    --
    -- `skipped` existe parce qu'un garde-fou qui refuse d'agir n'est ni une
    -- réussite ni un échec : c'est une décision, et elle doit se lire comme telle.
    state       text not null default 'done',
    -- detail : ce que l'équipement a RÉELLEMENT répondu. Sur une gamme où une
    -- erreur se paie d'un déplacement, savoir après coup ce qui a été envoyé est
    -- la seule façon de comprendre sans retourner sur site.
    detail      text not null default '',
    created_at  timestamptz not null default now()
);

create index if not exists pipeline_steps_idx on pipeline_steps (pipeline_id, seq);
