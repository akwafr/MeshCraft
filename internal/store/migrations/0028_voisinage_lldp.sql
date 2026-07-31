-- Le voisinage LLDP OBSERVÉ, et sa séparation d'avec les liens SAISIS.
--
-- Voir docs/01-Architecture/Voisinage LLDP.md, qui porte le relevé sur matériel dont
-- toute la forme de ces deux tables découle.
--
-- # Pourquoi ce n'est pas `device_links`
--
-- La question mérite d'être tranchée ici plutôt que redécouverte, parce que la
-- tentation est réelle : `device_links` porte déjà (a_device, a_port, b_device,
-- b_port), c'est-à-dire exactement la forme d'un lien. Trois raisons l'excluent, et
-- chacune suffirait :
--
--   1. **une intention et un constat ne se réfutent pas pareil.** Un lien de
--      `device_links` est ce qu'un exploitant a DÉCLARÉ : il survit à la panne, au
--      débranchement, au switch éteint — c'est même tout son intérêt, puisque c'est
--      contre lui qu'on compare le réel. Un voisin LLDP est ce qu'un équipement a VU
--      il y a trente secondes. Les mêler ferait DISPARAÎTRE un lien saisi parce qu'un
--      switch a été débranché dix secondes, ou l'inverse : ferait survivre un voisin
--      que plus personne ne voit ;
--   2. **les deux extrémités de `device_links` sont `not null` avec une clé étrangère
--      vers `devices`.** Un voisin dont on ne reconnaît pas le châssis — un routeur du
--      fournisseur, un point d'accès qui n'est pas dans meshcraft, le S3900 relevé sur
--      le parc — est structurellement INEXPRIMABLE dans cette table. Or c'est le cas
--      le plus fréquent en exploitation, et le taire serait perdre l'essentiel de ce
--      que LLDP apporte ;
--   3. **un port peut porter PLUSIEURS voisins.** Mesuré, pas supposé : le port
--      `sfp-sfpplus2` du CRS326 en voit TROIS, parce qu'un commutateur intermédiaire
--      qui ne consomme pas LLDP en inonde les trames au lieu de les absorber. La clé
--      primaire de ces tables doit donc admettre plusieurs lignes par port, ce qu'un
--      lien de topologie n'admet pas.
--
-- # Ce que le vault avait esquissé, et ce qui a changé
--
-- `docs/04-Données/Schéma base de données.md` esquissait une table `lldp_links`
-- portant `confirmed bool`. Ce n'est PAS ce qui est livré, et l'écart est délibéré :
-- `confirmed` — « les deux côtés se voient » — est une DÉRIVATION de deux constats,
-- pas un fait. Stockée, elle se périme dès que l'un des deux côtés cesse de voir
-- l'autre, et il faudrait alors un chemin pour la défaire ; il resterait donc à
-- décider qui, du prochain cliché de A ou de celui de B, a le droit de l'effacer.
-- Elle est calculée à la LECTURE par une fonction pure (`api.deriveLLDPLinks`), et
-- rien ne peut la faire mentir.
--
-- # Deux tables et non une
--
-- `lldp_neighbors` porte ce qu'un équipement a VU. `lldp_self` porte ce qu'il
-- ANNONCE. Les séparer n'est pas une élégance : c'est ce qui rend le rapprochement
-- possible du tout, et le relevé le démontre par un cas qu'on n'aurait pas deviné —
-- le 1930 déclare `lldpLocChassisId = 0x8825101761a7` mais émet, par son port 50, des
-- trames dont le chassis-id est `0x8825101761d9`, la MAC de ce port. Le CRS326
-- enregistre donc un voisin dont l'identifiant ne figure nulle part dans l'identité
-- déclarée du 1930. Sans `lldp_self.port_ids`, le seul lien bilatéral du parc de
-- référence se présenterait comme vu d'un seul côté.

-- lldp_self : l'identité LLDP qu'un équipement ANNONCE.
--
-- Une ligne par équipement interrogé, remplacée en entier à chaque cliché. Pas de
-- `first_seen` : ce n'est pas un constat sur le monde, c'est la carte de visite de
-- l'équipement, et « depuis quand annonce-t-il ce nom » n'intéresse personne.
create table lldp_self (
    device_id uuid primary key references devices(id) on delete cascade,
    -- site_id est DUPLIQUÉ depuis devices, et c'est assumé : toutes les lectures sont
    -- par site (une weathermap, une page d'équipements), et passer par une jointure sur
    -- `devices` à chaque fois coûterait plus que cette colonne. `on delete cascade` sur
    -- les deux : un site supprimé ou un équipement supprimé n'a pas de voisinage.
    site_id   uuid not null references sites(id) on delete cascade,
    -- chassis_id : TEL QUE L'ÉQUIPEMENT L'A DIT, à la conversion sans perte près. Pas
    -- normalisé en base, exprès. L'encodage n'est pas uniforme — le CRS326 rend le texte
    -- « B8:69:F4:5B:A0:D2 », le 1930 six octets bruts donc « 0xb869f45ba0d2 » pour le
    -- MÊME châssis — et la normalisation est une décision de rapprochement, qui vit dans
    -- une fonction pure et testée (`api.normalizeChassisID`). Stocker la forme normalisée
    -- rendrait impossible de dire à un exploitant ce que son équipement a réellement
    -- répondu, ce qui est la seule chose qui permette de comprendre un rapprochement
    -- manqué.
    chassis_id text not null default '',
    -- sys_name : le nom que l'équipement s'attribue, qui n'est PAS son nom meshcraft.
    -- Relevé : `lab-crs326` s'annonce « lan-r4 », `sw-labo-1930` s'annonce par son
    -- numéro de série « CN2AKPH86J ». C'est précisément pourquoi le rapprochement par le
    -- nom est un DERNIER recours et non le premier.
    sys_name   text not null default '',
    -- port_ids : les `lldpLocPortId` de cet équipement. Voir l'en-tête : c'est le champ
    -- qui rattrape un constructeur qui annonce la MAC de son port comme chassis-id.
    port_ids   text[] not null default '{}',
    -- served : cet équipement sert-il la LLDP-MIB ?
    --
    -- Faux et « zéro voisin » ne veulent pas dire la même chose, et les confondre serait
    -- la faute la plus coûteuse de ce lot. Zéro voisin est un CONSTAT : LLDP tourne,
    -- personne en face. Ne pas servir la MIB veut dire qu'on ne sait RIEN, et qu'aucune
    -- version de meshcraft n'y changera quoi que ce soit — mesuré sur le CSS326, dont le
    -- walk de `1.0.8802` entier rend zéro ligne sans erreur. L'écran doit dire « on ne
    -- sait pas », jamais « rien ».
    served     boolean not null default false,
    -- seen_at : quand ce cliché a été pris, à l'horloge du CONTRÔLEUR.
    --
    -- Jamais celle du proxy, et c'est la règle que le lot des traps a coûté cher à
    -- établir (cf. Protocole contrôleur-proxy, « Event.TS est un fait de diagnostic ») :
    -- une date fournie par un proxy dit ce que le proxy croit, et l'écart mesuré était
    -- de onze secondes. Ici la conséquence serait directement visible — « vu depuis
    -- 3 min » affiché au survol d'un port, calculé contre l'horloge du navigateur.
    seen_at    timestamptz not null default now()
);

create index lldp_self_site_idx on lldp_self (site_id);

-- lldp_neighbors : un CONSTAT daté. « À cet instant, sur ce port, cet équipement a
-- reçu une trame LLDP qui se présentait ainsi. »
create table lldp_neighbors (
    device_id uuid not null references devices(id) on delete cascade,
    site_id   uuid not null references sites(id) on delete cascade,
    -- local_port : le nom de PORT DE FAÇADE, résolu par le CONTRÔLEUR.
    --
    -- Le proxy envoie ce que l'équipement a dit (« sfp-sfpplus2 », « 50 ») ; c'est le
    -- contrôleur qui le rapproche de la façade avec `facadeOf`/`resolve`, parce que lui
    -- seul connaît `port_groups`. Un libellé qu'on ne sait pas rattacher est ÉCARTÉ et
    -- n'arrive jamais ici : un voisin posé sur le mauvais port dessine un lien crédible
    -- et faux, ce qui est pire qu'un trou.
    local_port text not null,
    -- local_port_num : le `lldpLocPortNum` brut, pour le DIAGNOSTIC. C'est ce qui reste
    -- pour comprendre quel port l'équipement désignait quand la résolution échoue —
    -- sauf que dans ce cas la ligne n'est pas écrite. Il sert donc surtout à relire un
    -- rattachement dont on doute.
    local_port_num integer not null default 0,
    -- Ce que le voisin a dit de lui-même, tel quel. `port_id` ET `port_desc` parce
    -- qu'aucun des deux n'est servi partout : le CRS326 ne sert AUCUNE colonne
    -- `lldpRemPortDesc` (walk vide, mesuré), le 1930 sert les deux.
    chassis_id text not null default '',
    port_id    text not null default '',
    port_desc  text not null default '',
    sys_name   text not null default '',
    sys_desc   text not null default '',
    -- first_seen : depuis quand ce voisin-là est vu sur ce port-là, SANS interruption.
    --
    -- C'est la moitié de la valeur d'exploitation de cette table, et c'est pour ça
    -- qu'elle est préservée à travers les clichés au lieu d'être réécrite (cf.
    -- store.ReplaceLLDP, dont l'`upsert` ne touche pas cette colonne). Un voisin qui
    -- disparaît puis revient repart à zéro, et c'est le comportement juste : le câble a
    -- bougé, l'ancienneté du constat précédent ne dit plus rien.
    first_seen timestamptz not null default now(),
    -- last_seen : le dernier cliché où ce voisin était là. À l'horloge du contrôleur,
    -- même raison que `lldp_self.seen_at`.
    last_seen  timestamptz not null default now(),
    -- La clé porte le VOISIN et pas seulement le port, parce qu'un port peut en voir
    -- plusieurs — mesuré, trois sur un seul port du CRS326. Une clé
    -- (device_id, local_port) aurait écrasé deux constats sur trois, et l'écran aurait
    -- affiché un voisin pris au hasard parmi ceux qui existent.
    --
    -- `port_id` fait partie de la clé en plus du châssis : deux ports d'un même voisin
    -- peuvent arriver sur le même port local à travers un commutateur transparent, et ce
    -- sont deux constats distincts.
    primary key (device_id, local_port, chassis_id, port_id)
);

-- Lecture par SITE (la weathermap, la page des équipements) et par ÉQUIPEMENT (le
-- survol d'un port). La clé primaire couvre déjà le second cas.
create index lldp_neighbors_site_idx on lldp_neighbors (site_id);
