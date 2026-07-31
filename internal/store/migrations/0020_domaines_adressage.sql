-- Domaines d'adressage : donner un SENS à une adresse IP.
--
-- Le problème. `192.168.1.10` n'identifie personne. L'espace RFC 1918 se
-- recoupe : la même plage déclarée sur deux proxys peut désigner deux réseaux
-- totalement étrangers l'un à l'autre — ou un seul réseau atteint par deux
-- chemins. Rien, dans ce que meshcraft observe, ne permet de trancher : deux
-- proxys posés dans deux agences en 192.168.1.0/24 voient exactement la même
-- chose que deux proxys posés dans une seule agence.
--
-- Ce que le schéma faisait jusqu'ici : la clé de fusion des candidats repliait
-- sur le couple (proxy, IP). C'est la moitié prudente de la réponse — deux
-- réseaux distincts restaient distincts — mais l'autre moitié manquait : deux
-- proxys sur UN MÊME réseau produisaient deux candidats pour un seul
-- équipement muet, et rien ne pouvait plus jamais les réunir.
--
-- La réponse : un objet de première classe, le DOMAINE D'ADRESSAGE — l'espace
-- dans lequel une adresse désigne une machine et une seule. Il est DÉCLARÉ, pas
-- déduit. Une déduction se tromperait, et se tromperait du mauvais côté :
-- fusionner à tort deux réseaux fond deux équipements en une seule fiche, ce qui
-- ne se rattrape pas, alors qu'un doublon se supprime d'un clic.
--
-- Cette migration est à SÉMANTIQUE CONSTANTE. Chaque proxy reçoit son propre
-- domaine, donc la clé de fusion (domaine, IP) vaut exactement l'ancienne
-- (proxy, IP). Rien ne fusionne tout seul : le regroupement de deux proxys dans
-- un même domaine est un geste explicite de l'opérateur.

create table address_domains (
    id         uuid primary key default gen_random_uuid(),
    name       text not null,
    note       text not null default '',
    created_at timestamptz not null default now()
);

-- Le domaine PROPRE d'un proxy : celui de tout ce qu'il observe hors des plages
-- qu'il déclare (sonde manuelle d'une adresse quelconque, console série). Sans
-- ce repli, ces observations n'auraient pas de domaine et retomberaient dans un
-- espace commun où toutes les adresses privées se confondraient.
alter table proxies add column address_domain_id uuid references address_domains(id) on delete set null;

-- Une plage appartient toujours au proxy qui sait l'atteindre — c'est
-- inchangé — et désormais AUSSI au réseau qu'elle décrit. Les deux notions sont
-- distinctes : le proxy est un chemin, le domaine est un espace d'adresses.
--
-- `on delete restrict` : supprimer un domaine encore utilisé changerait en
-- silence l'identité de tous les candidats qu'il porte. On l'interdit et on
-- laisse l'opérateur déplacer ses plages d'abord.
alter table scan_prefixes add column address_domain_id uuid references address_domains(id) on delete restrict;

alter table discovered add column address_domain_id uuid references address_domains(id) on delete set null;

-- Sur l'équipement adopté, le domaine sert à la SURVEILLANCE : il dit quel
-- proxy est légitime pour répondre de son adresse. Nullable, parce qu'un
-- équipement saisi à la main n'en a pas, et qu'exiger un domaine couperait la
-- surveillance de tout le parc antérieur à ce modèle.
alter table devices add column address_domain_id uuid references address_domains(id) on delete set null;

-- Un domaine par proxy existant, nommé d'après lui. C'est ce qui rend la
-- migration à sémantique constante.
--
-- L'identifiant du domaine REPREND celui du proxy : ce n'est pas un lien de
-- nature entre les deux objets, seulement ce qui rend les reprises ci-dessous
-- exactes sans jointure ni curseur. Les domaines créés plus tard auront leur
-- propre identifiant, sans rapport avec aucun proxy.
insert into address_domains (id, name, note)
select p.id, 'Réseau de ' || p.name,
       'Domaine créé automatiquement à la migration 0020 : le réseau propre à ce proxy. '
       || 'Renommez-le, ou rattachez-y les plages d''un autre proxy si les deux regardent le même réseau.'
from proxies p;

update proxies p set address_domain_id = p.id;

update scan_prefixes sp set address_domain_id = sp.proxy_id;
alter table scan_prefixes alter column address_domain_id set not null;

-- Les candidats déjà en file : leur domaine est celui de leur proxy témoin. Un
-- candidat sans proxy vient du proxy « Local » (cf. migration 0005).
update discovered d
   set address_domain_id = coalesce(d.proxy_id, '00000000-0000-0000-0000-000000000001'::uuid);

-- Les équipements adoptés depuis la découverte savent de quel candidat ils
-- viennent (`extra->>'discovered_id'`, posé à l'adoption) : on remonte le fil
-- pour retrouver leur domaine. Ceux qui n'en viennent pas restent à NULL, et
-- c'est la vérité — personne n'a jamais déclaré dans quel réseau ils vivent.
update devices dev
   set address_domain_id = d.address_domain_id
  from discovered d
 where d.id::text = dev.extra->>'discovered_id'
   and d.address_domain_id is not null;

-- La clé de fusion bascule de (proxy, IP) vers (domaine, IP).
--
-- Aucun doublon ne peut apparaître ici : les domaines fraîchement créés sont en
-- bijection avec les proxys, donc l'ensemble des clés est le même qu'avant.
drop index if exists discovered_key_idx;
create unique index discovered_key_idx on discovered (
    coalesce(nullif(serial, ''), nullif(mac, ''),
             coalesce(address_domain_id::text, 'sans-domaine') || '|' || ip)
);

create index scan_prefixes_domain_idx on scan_prefixes (address_domain_id);
create index devices_domain_idx on devices (address_domain_id);
