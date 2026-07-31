-- Mise à jour automatique du proxy
-- (cf. docs/01-Architecture/Versionnage et mise à jour du proxy).
--
-- `version` portait jusqu'ici la version produit (0.1.0) : elle porte désormais
-- l'horodatage de build AAAAMMJJ.HHMMSS, à largeur fixe, donc comparable en
-- texte. `protocol` dit la compatibilité du tunnel, indépendamment de la date de
-- build : un horodatage ne peut pas décider ce qu'on refuse.
alter table proxies add column if not exists protocol int not null default 0;
alter table proxies add column if not exists os text not null default '';
alter table proxies add column if not exists arch text not null default '';

-- Politique par proxy. auto_upgrade à vrai par défaut : un parc qu'on oublie de
-- mettre à jour est un parc vulnérable, et le rollback automatique côté proxy
-- rend le défaut sûr.
alter table proxies add column if not exists auto_upgrade boolean not null default true;
-- pin_version : version exacte visée (épinglage ou retour arrière). Prime sur la
-- cible de l'instance.
alter table proxies add column if not exists pin_version text not null default '';
-- upgrade_requested : « Mettre à jour maintenant », qui contourne auto_upgrade
-- SANS le modifier. Consommé à l'émission de l'offre.
alter table proxies add column if not exists upgrade_requested boolean not null default false;

-- Dernier rapport du proxy, affiché tel quel dans l'UI : un échec muet vaudrait
-- moins que pas de mise à jour du tout.
alter table proxies add column if not exists upgrade_state text not null default '';
alter table proxies add column if not exists upgrade_version text not null default '';
alter table proxies add column if not exists upgrade_error text not null default '';
alter table proxies add column if not exists upgrade_started_at timestamptz;
alter table proxies add column if not exists upgraded_at timestamptz;

-- Les proxys déjà enrôlés annoncent encore une version produit : on ne prétend
-- pas savoir sur quel build ils tournent. Leur prochain heartbeat le dira.
update proxies set version = '' where version not like '________.______' and not internal;
