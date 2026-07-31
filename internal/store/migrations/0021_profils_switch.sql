-- Profils de switchs : décrire la façade d'un équipement par des RÈGLES.
--
-- Le problème. Les profils de prise (`port_profiles`) disent ce que reçoit UNE
-- prise ; rien ne dit quelles prises la reçoivent. Configurer un 48 ports
-- revient donc à sélectionner des plages à la main, équipement par équipement.
-- C'est refaire quarante-huit fois un geste dont l'intention tient en trois
-- phrases : « les SFP+ en trunk, les quatre derniers RJ45 en trunk PVID 100,
-- tout le reste en accès PVID 100 ».
--
-- Ce qui manquait n'est pas un endroit où ranger une configuration — le modèle
-- en a déjà trois — mais un endroit où ranger des RÈGLES, c'est-à-dire une
-- intention qui désigne des ENSEMBLES de prises au lieu de les nommer. Un profil
-- qui nomme « GE45 » ne vaut que pour une façade ; un profil qui dit « les
-- quatre derniers RJ45 » vaut pour un 48, un 24 et un 8 ports. C'est toute la
-- différence entre un profil et un copier-coller.
--
-- Pourquoi une table de plus, et pas une colonne sur `device_profiles`. Le
-- profil d'équipement porte les services communs (STP, SNMP, syslog) : c'est un
-- objet qui s'applique à un équipement ENTIER et dont la mise en œuvre reste à
-- faire. Le profil de switch ne parle que des ports, et il est le seul des deux
-- qui soit exécutable aujourd'hui. Les fondre obligerait à décider maintenant de
-- l'articulation entre les deux — l'ordre d'application, ce qui l'emporte — sans
-- avoir le second sous la main pour en juger.
--
-- La forme est en revanche RIGOUREUSEMENT celle des deux autres profils
-- (id, site_id, name, spec jsonb, unique par site). Ce n'est pas une coïncidence
-- décorative : `store.profileTable` accepte alors un troisième `kind` et tout le
-- CRUD, les routes `/api/sites/{id}/profiles/{kind}`, les événements et la
-- résolution de site du flux temps réel fonctionnent sans une ligne de plus. Une
-- table à la forme différente aurait coûté un second CRUD, qui aurait dérivé.
--
-- Contenu de `spec` (cf. internal/portrules) :
--
--   {"description": "...",
--    "rules": [
--      {"label": "SFP+ en trunk",
--       "select": {"media": "sfp_plus"},
--       "config": {"mode": "trunk", "pvid": 1, "tagged": "all", "poe": false}},
--      {"label": "4 derniers RJ45",
--       "select": {"media": "copper", "slice": "last", "count": 4},
--       "port_profile_id": "…"}
--    ]}
--
-- La spec n'est pas contrainte par le schéma, comme les deux autres : la
-- validation vit dans `internal/portrules`, à côté du code qui l'évalue. Une
-- contrainte SQL sur du jsonb dirait « violation de contrainte » là où
-- l'exploitant a besoin de « la règle « uplinks » ne désigne aucune prise ».

create table switch_profiles (
    id      uuid primary key default gen_random_uuid(),
    site_id uuid not null references sites(id) on delete cascade,
    name    text not null,
    spec    jsonb not null default '{}'::jsonb,
    unique (site_id, name)
);

-- Aucun équipement ne RÉFÉRENCE son profil de switch, et c'est délibéré.
--
-- Un profil de switch se POSE : il produit l'intention des ports, qui vit
-- ensuite dans `devices.ports` comme n'importe quelle configuration, et que
-- l'exploitant reste libre de retoucher port par port. Garder un lien
-- équipement → profil ferait de toute retouche locale une « dérive au profil »
-- qu'il faudrait arbitrer, alors que la retouche est le mode de travail normal
-- (cf. docs/01-Architecture/Réconciliation : la dérive concerne l'écart entre
-- meshcraft et l'ÉQUIPEMENT, pas entre deux intentions de meshcraft).
--
-- La contrepartie est assumée : modifier un profil ne repose rien tout seul. Le
-- jour où un lien persistant sera voulu, il s'ajoutera — l'inverse, retirer un
-- lien dont tout le parc dépend, ne s'improvise pas.
