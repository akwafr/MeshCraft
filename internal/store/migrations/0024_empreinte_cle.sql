-- Empreinte de la clé qui a scellé le coffre à identifiants.
--
-- LE PIÈGE QU'ELLE DÉSARME. Jusqu'ici, définir MESHCRAFT_SECRET_KEY sur une
-- instance en service faisait basculer le contrôleur sur cette clé sans jamais
-- regarder celle rangée en base : tout le coffre devenait illisible, et EN
-- SILENCE. Le produit continuait de démarrer, de servir ses pages et de
-- répondre à son healthcheck. La supervision, elle, s'arrêtait sans un mot —
-- snmpWatchesFor écarte l'équipement dont la communauté ne se déchiffre pas
-- (internal/api/watch_handlers.go) — et les identifiants d'accès au parc
-- devenaient inaccessibles. La panne se découvrait écran par écran, des jours
-- plus tard, souvent après que les sauvegardes utiles aient tourné.
--
-- Le contrôleur compare désormais, à chaque démarrage, l'empreinte de la clé
-- qu'on lui donne à celle enregistrée ici, et REFUSE de démarrer si elles
-- diffèrent. Un refus bruyant au démarrage est infiniment préférable à un
-- fonctionnement apparent qui perd le coffre.
--
-- Le cas inverse est aussi dangereux et la même comparaison l'attrape : une
-- instance basculée vers la variable d'environnement puis redémarrée SANS elle
-- tirerait une clé neuve et rescellerait par-dessus.
--
-- CE N'EST PAS UN SECRET. HMAC-SHA-256 est à sens unique et la clé fait 32
-- octets tirés au hasard : il n'y a aucun dictionnaire à essayer. La ranger
-- dans la même base que les secrets qu'elle décrit n'aide donc en rien celui
-- qui obtient le dump. C'est ce qui permet de la stocker ici plutôt que de
-- demander à l'exploitant de la garder ailleurs.
--
-- Table à UNE SEULE LIGNE, et la contrainte le dit au lieu de l'espérer : il
-- n'existe qu'une clé de coffre par instance, et deux lignes ici voudraient
-- dire deux vérités contradictoires sur ce qui a scellé la base.
--
-- Volontairement PAS rangée dans instance_secrets : cette table contient la
-- clé elle-même, que la bascule doit finir par SUPPRIMER. Mélanger la clé et
-- son empreinte dans la même table, c'est prendre le risque qu'un nettoyage
-- emporte les deux — et perdre l'empreinte, c'est reperdre le garde-fou.
create table if not exists credentials_key_fingerprint (
    -- one : toujours true, unique clé primaire possible => une seule ligne.
    one         boolean primary key default true check (one),
    fingerprint bytea not null,
    -- algo : nomme la dérivation, pour qu'un changement futur de dérivation se
    -- reconnaisse au lieu d'être pris pour une clé différente.
    algo        text not null default 'hmac-sha256-v1',
    updated_at  timestamptz not null default now()
);

-- Aucune ligne insérée ici, et c'est délibéré : la migration ne connaît pas la
-- clé active. Le premier démarrage après cette migration enregistre l'empreinte
-- de la clé qui sert RÉELLEMENT, après avoir vérifié qu'elle ouvre bien le
-- coffre existant (cf. cmd/controller/keyguard.go).
