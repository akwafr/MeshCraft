-- Un SECOND secret dans le coffre : SNMPv3 en dérive DEUX clés, pas une.
--
-- Le problème que la colonne unique laissait passer : la RFC 3414 dérive la clé
-- d'authentification et la clé de chiffrement par le MÊME algorithme, à partir
-- d'une phrase secrète et de l'engineID. Alimentées par la même phrase, les deux
-- dérivations reçoivent exactement la même entrée — la clé de chiffrement est
-- alors littéralement le préfixe de la clé d'authentification. Qui obtient l'une
-- obtient l'autre, et le chiffrement n'ajoute strictement rien.
--
-- Ce n'est pas théorique à notre échelle : une identité v3 est partagée par
-- toute une famille cryptographique (c'est le rôle de l'étiquette `snmpv3-…`,
-- cf. docs/03-Sécurité/Identité SNMP §1). Un seul équipement compromis livrerait
-- donc la supervision de la famille entière en clair.
--
-- NULLABLE, et c'est le point : seul `snmp_v3` a deux phrases. Un mot de passe
-- d'administration ou une communauté v2c n'en ont qu'une, et les forcer à en
-- inventer une seconde créerait un secret que rien ne consomme — donc un secret
-- qu'on ne fait pas tourner et qu'on oublie. Les identifiants déjà en base
-- (trois `admin`, une communauté) restent intacts, colonne à NULL.
alter table site_credentials
    add column if not exists secret2_enc bytea;

-- Les deux phrases tournent ENSEMBLE, donc la génération porte la PAIRE.
--
-- Les séparer serait la faute : une génération qui ne porterait que la phrase
-- d'authentification laisserait, après une rotation, la question sans réponse —
-- quelle phrase de chiffrement va avec celle-ci ? Sur un équipement resté en
-- arrière, se tromper de paire ne rend pas une erreur d'authentification lisible,
-- ça rend un silence : l'agent v3 jette le paquet qu'il ne sait pas déchiffrer.
alter table credential_generations
    add column if not exists secret2_enc bytea;
