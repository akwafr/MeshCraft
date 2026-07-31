-- Un proxy est un point de présence réseau de l'INSTANCE : il n'a ni site de
-- rattachement, ni liste de sites couverts
-- (cf. docs/01-Architecture/Proxy — « Un proxy n'est lié ni à un site, ni à un
-- équipement »).
--
-- La couverture ne décidait rien : elle ne restreignait ni les scans, ni le
-- site des équipements découverts, ni les chemins d'accès. Elle obligeait
-- seulement l'opérateur à répondre, au déploiement, à une question dont il n'a
-- pas la réponse. Ce qu'un proxy joint est OBSERVÉ (device_paths) ; le site
-- d'un équipement vient de l'adoption.
drop table if exists proxy_sites;
alter table proxies drop column if exists site_id;

-- Conséquence directe : le token d'enrollment devient de portée instance.
-- Enrôler un point de présence dans l'instance n'est plus une prérogative
-- d'administrateur de site (cf. docs/03-Sécurité/RBAC).
alter table enroll_tokens drop column if exists site_id;
