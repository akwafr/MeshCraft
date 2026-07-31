-- Identifiants que meshcraft installe et utilise sur les équipements d'un site.
--
-- DÉCISION D'EXPLOITATION : un mot de passe par SITE, et non par équipement.
-- La spec (docs/03-Sécurité/Identité de service et rotation) recommandait un
-- secret par équipement — un seul équipement compromis ne livrant alors pas le
-- parc. L'exploitation a tranché autrement : un administrateur doit pouvoir se
-- connecter à n'importe quel équipement de son site sans consulter une liste de
-- 200 mots de passe. Le compromis est assumé et documenté.
--
-- Nuance qui rend la chose praticable : les politiques de mot de passe diffèrent
-- d'un modèle à l'autre (longueur, caractères interdits). Un site peut donc
-- définir plusieurs identifiants, un par GROUPE d'équipements, le groupe vide
-- étant le défaut du site.
create table if not exists site_credentials (
    id         uuid primary key default gen_random_uuid(),
    site_id    uuid not null references sites(id) on delete cascade,
    -- group_name vide = identifiant par défaut du site.
    group_name text not null default '',
    -- kind : à quoi sert cet identifiant (web, ssh, snmp…).
    kind       text not null default 'admin',
    username   text not null default 'admin',
    secret_enc bytea not null,
    note       text not null default '',
    -- rotated_at : dernière fois que le secret a changé. Un mot de passe qui n'a
    -- pas bougé depuis deux ans doit se voir.
    rotated_at timestamptz not null default now(),
    created_at timestamptz not null default now(),
    updated_by uuid references users(id) on delete set null,
    unique (site_id, group_name, kind)
);
create index if not exists site_credentials_site_idx on site_credentials (site_id);
