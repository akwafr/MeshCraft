-- Purge des ports FANTÔMES créés par la télémétrie SNMP.
--
-- La télémétrie nomme un port « préfixe + ifIndex ». Or un équipement expose par
-- SNMP bien plus d'interfaces que sa façade n'a de ports : interfaces de VLAN,
-- agrégats, boucles — relevés sur un 1930 avec des index à 1000, 3000, 20000 et
-- 100000. Écrites telles quelles, elles apparaissaient dans l'inventaire comme
-- des ports (« GE100041 »), à côté des vrais.
--
-- Elles étaient inoffensives pour la réconciliation — sans `mode` elles comptent
-- comme non gérées et le diff les ignore — mais elles gonflaient l'inventaire :
-- 93 entrées pour 52 ports réels sur le switch qui a servi à les débusquer.
--
-- La source est colmatée côté contrôleur (cf. applyPortTelemetry : la télémétrie
-- ne CRÉE plus jamais un port, elle ne met à jour que ceux de la façade). Reste
-- le dépôt laissé par les versions précédentes, que rien ne nettoyait : une
-- fusion de télémétrie ajoute des clés, elle n'en retire aucune.
--
-- Trois précautions, parce qu'une migration qui efface des ports par erreur
-- efface le travail de configuration de quelqu'un :
--
--   1. la façade fait foi — un port n'est supprimé que si son nom ne tombe dans
--      AUCUNE plage déclarée par `port_groups` ;
--   2. un équipement dont la façade est inconnue ou illisible n'est pas touché :
--      on ne supprime pas sur la foi d'une liste qu'on n'a pas su lire ;
--   3. toute entrée portant une INTENTION (`mode`, `label`, `pvid`, `tagged`)
--      est épargnée, même hors plage. Une entrée configurée n'est pas un
--      fantôme, quel que soit son nom — et si son nom surprend, c'est une
--      anomalie à regarder, pas à effacer en silence.
with plages as (
    select d.id,
           g->>'prefix'      as prefix,
           (g->>'from')::int as depart,
           (g->>'count')::int as nombre
    from devices d,
         lateral jsonb_array_elements(d.port_groups) g
    where jsonb_typeof(d.port_groups) = 'array'
),
noms as (
    select p.id, p.prefix || s.n::text as nom
    from plages p
    cross join lateral generate_series(p.depart, p.depart + p.nombre - 1) as s(n)
    where p.prefix is not null
      and p.depart >= 0
      and p.nombre between 1 and 4096
),
fantomes as (
    select d.id, k.key
    from devices d,
         lateral jsonb_object_keys(d.ports) k(key)
    where jsonb_typeof(d.ports) = 'object'
      -- (2) façade connue, sinon on s'abstient
      and exists (select 1 from noms n where n.id = d.id)
      -- (1) hors de toute plage de la façade
      and not exists (select 1 from noms n where n.id = d.id and n.nom = k.key)
      -- (3) aucune intention portée
      and coalesce(d.ports -> k.key ->> 'mode', '') = ''
      and coalesce(d.ports -> k.key ->> 'label', '') = ''
      and not (d.ports -> k.key ? 'pvid')
      and not (d.ports -> k.key ? 'tagged')
)
update devices d
set ports = coalesce(
        (select jsonb_object_agg(k.key, d.ports -> k.key)
         from lateral jsonb_object_keys(d.ports) k(key)
         where not exists (
             select 1 from fantomes f where f.id = d.id and f.key = k.key
         )),
        '{}'::jsonb
    )
where exists (select 1 from fantomes f where f.id = d.id);
