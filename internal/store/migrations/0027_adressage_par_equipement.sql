-- Le mode d'adressage de management, PAR ÉQUIPEMENT.
--
-- Ce que le schéma savait déjà dire, et ce qu'il ne savait pas. `site_mgmt_plan.source`
-- (migration 0009) porte l'origine de l'adresse pour tout un SITE : « dhcp_external »
-- ou « meshcraft ». C'est une décision de site, et elle est juste à ce niveau — le
-- réseau de management d'un site a bien un régime dominant. Mais elle ne laissait
-- aucune place à l'équipement qui n'y est pas, et il en existe : sur l'installation de
-- référence, les DEUX CSS326 sont en adresse STATIQUE, dans le bon sous-réseau de
-- management, sur un site dont le plan dit « DHCP externe ».
--
-- Ce que produisait ce trou : rien de visible, et c'est le problème. Ces équipements
-- sont parfaitement conformes au plan — leur adresse est dans le sous-réseau, ils
-- répondent — et pourtant meshcraft ne pouvait ni dire par quel moyen ils l'avaient
-- obtenue, ni afficher les trois champs qu'un exploitant veut voir sur une
-- configuration statique (adresse, passerelle, DNS), ni enregistrer la décision de
-- l'y laisser. Un équipement statique et un équipement en bail DHCP se présentaient
-- identiquement, alors que ce qui se passe le jour où le serveur DHCP change n'a rien
-- de commun entre les deux.
--
-- # « inherit » est le défaut, et il veut dire GARDER CE QUE L'ÉQUIPEMENT A
--
-- Trois valeurs : `inherit`, `dhcp`, `static`. Le défaut est `inherit`, et il applique
-- la règle 3 du modèle de données standard — « absent = non géré ». Un équipement en
-- `inherit` ne se voit RIEN imposer : s'il est en statique, il reste en statique, et
-- c'est très exactement la demande (« si un équipement est adopté dans le bon LAN de
-- management mais en IP statique au lieu de DHCP, alors garder la configuration
-- statique »). Le mode EFFECTIF se résout donc à la lecture, en trois temps : ce que
-- l'équipement déclare porter d'abord, l'origine du plan du site ensuite, le défaut
-- prudent enfin (cf. api.effectiveAddrMode).
--
-- La conséquence de forme est ce qui rend cette migration sans risque : tout le parc
-- existant part en `inherit`, donc rien ne change de comportement, et aucune ligne
-- n'a besoin d'être réécrite. Le jour où un exploitant veut basculer un équipement en
-- DHCP, c'est un geste EXPLICITE qui pose `dhcp` — pas une déduction du plan de site.
--
-- # Pourquoi trois colonnes de statique et non un jsonb
--
-- `mgmt_static_ip`, `mgmt_static_gw`, `mgmt_static_dns` sont les trois champs que
-- l'écran affiche et que l'exploitant saisit. Les ranger dans un jsonb les rendrait
-- inatteignables par une requête (« quels équipements pointent vers cette
-- passerelle ? » est la question qu'on se posera le jour où la passerelle change) et
-- surtout impossibles à contraindre. Le jsonb, lui, est réservé à ce que l'ÉQUIPEMENT
-- déclare (`mgmt_addr_observed`) : là, la forme dépend de la plateforme et ne doit pas
-- être figée par une colonne.
--
-- En `text` et non en `inet`/`cidr`, pour la raison déjà écrite dans 0009 : ces champs
-- sont saisis dans un formulaire, et un type PostgreSQL strict transformerait une
-- faute de frappe en erreur de base illisible là où le contrôleur sait dire « la
-- passerelle 10.0.0.1 n'appartient pas au sous-réseau 10.99.0.0/24 ». La validation
-- vit dans `validateDeviceAddressing`, en un seul endroit, et elle est plus bavarde
-- qu'une contrainte.
--
-- # Ce que ces colonnes ne sont PAS
--
-- Ce n'est pas `devices.mgmt_ip`, qui reste l'adresse OBSERVÉE — celle à laquelle
-- meshcraft joint l'équipement, écrite par la relocalisation et par la découverte.
-- L'intention et le constat gardent deux colonnes distinctes, comme partout ailleurs
-- dans ce schéma (`ports` contre `observed`, `trap_dest_addr` contre `trap_seen_at`) :
-- quand les deux divergent, c'est une dérive à AFFICHER, pas une valeur à corriger en
-- silence.
--
-- Ce n'est pas non plus `devices.mgmt_mode` (keep | site_plan), toujours absent et
-- toujours en cible : celui-là dira si l'équipement entre dans le plan du site, ce qui
-- est une question de VLAN et de sous-réseau. Le mode d'adressage, lui, dit comment
-- l'adresse est obtenue DANS ce sous-réseau. Les deux axes sont indépendants — on peut
-- être dans le plan en statique, ou hors plan en DHCP.

alter table devices
    -- inherit : meshcraft n'impose rien et conserve ce que l'équipement porte.
    add column mgmt_addr_mode  text        not null default 'inherit',
    add column mgmt_static_ip  text        not null default '',
    add column mgmt_static_gw  text        not null default '',
    add column mgmt_static_dns text[]      not null default '{}',
    -- QUI a décidé, et QUAND. Une intention d'adressage est le genre de décision
    -- qu'on relit six mois plus tard en se demandant si elle est encore voulue ;
    -- sans auteur ni date, elle passe pour une valeur d'usine.
    add column mgmt_addr_by    uuid references users(id) on delete set null,
    add column mgmt_addr_at    timestamptz,
    -- Ce que l'ÉQUIPEMENT déclare porter, tel que le connecteur l'a lu. NULL = jamais
    -- relu, et c'est une information : on ne prétend pas connaître le mode d'un
    -- équipement qu'on n'a pas interrogé.
    add column mgmt_addr_observed    jsonb,
    add column mgmt_addr_observed_at timestamptz;

-- La contrainte est dans la BASE et pas seulement dans le code, parce que cette
-- colonne décide de ce qu'un futur chemin d'écriture posera sur un équipement. Une
-- valeur inconnue qui y arriverait par un chemin oublié — un import de site-config,
-- une reprise de sauvegarde — se traduirait en « mode non reconnu » au moment de la
-- pose, c'est-à-dire au pire moment.
alter table devices add constraint devices_mgmt_addr_mode_check
    check (mgmt_addr_mode in ('inherit', 'dhcp', 'static'));
