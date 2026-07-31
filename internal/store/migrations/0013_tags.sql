-- Étiquettes d'équipement, et portée des identifiants par étiquette.
-- Voir docs/01-Architecture/Étiquettes et portée des identifiants.
--
-- Le groupe (devices.group_id) ne suffit pas : il est UNIQUE par équipement, et
-- il porte déjà le profil de configuration. Or les raisons de donner un mot de
-- passe particulier ne s'excluent pas — une gamme qui plafonne les mots de passe
-- à 16 caractères, un lieu confié à un prestataire, une plage d'adresses isolée.
-- Un équipement peut relever des trois à la fois.

-- Étiquettes posées sur un équipement.
create table if not exists device_tags (
    device_id  uuid not null references devices(id) on delete cascade,
    tag        text not null,
    -- source : « manual », ou « rule:<uuid> » quand une règle l'a posée.
    --
    -- Rejouer les règles doit pouvoir RETIRER ce qu'une règle avait posé et qui
    -- ne s'applique plus, sans jamais toucher à ce qu'un humain a décidé. Sans
    -- cette colonne, la seule réévaluation sûre serait de ne rien retirer, et le
    -- parc accumulerait des étiquettes périmées que personne n'oserait nettoyer.
    source     text not null default 'manual',
    created_at timestamptz not null default now(),
    primary key (device_id, tag)
);

create index if not exists device_tags_tag_idx on device_tags (tag);

-- Règles d'étiquetage automatique, par site.
create table if not exists tag_rules (
    id         uuid primary key default gen_random_uuid(),
    site_id    uuid not null references sites(id) on delete cascade,
    tag        text not null,
    -- priority départage deux identifiants d'étiquette qui viseraient le même
    -- équipement : la plus haute gagne.
    priority   int not null default 0,
    enabled    boolean not null default true,
    -- match : critères combinés en ET. Clés reconnues — vendor, model, location
    -- (sous-chaîne insensible à la casse) et ip_prefix (CIDR).
    --
    -- Une règle SANS critère ne s'applique à rien. Le contraire ferait d'une
    -- ligne à moitié saisie une règle qui prend tout le parc.
    match      jsonb not null default '{}'::jsonb,
    note       text not null default '',
    created_at timestamptz not null default now(),
    updated_by uuid references users(id) on delete set null,
    unique (site_id, tag, match)
);

create index if not exists tag_rules_site_idx on tag_rules (site_id);

-- Un identifiant peut viser une ÉTIQUETTE au lieu d'un groupe.
--
-- Vide = l'identifiant ne vise pas d'étiquette (défaut du site, ou identifiant
-- de groupe). La contrainte d'unicité historique portait sur
-- (site_id, group_name, kind) : elle est élargie à l'étiquette, sinon un site ne
-- pourrait pas avoir deux identifiants d'étiquette distincts.
alter table site_credentials
  add column if not exists tag text not null default '';

alter table site_credentials
  add column if not exists priority int not null default 0;

alter table site_credentials
  drop constraint if exists site_credentials_site_id_group_name_kind_key;

create unique index if not exists site_credentials_scope_key
  on site_credentials (site_id, group_name, tag, kind);
