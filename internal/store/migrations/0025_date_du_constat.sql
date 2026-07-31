-- Depuis QUAND l'équipement est dans l'état que `status` annonce.
--
-- `status` seul ne se lit pas. « unknown » y mélangeait deux situations qui
-- n'appellent pas la même réaction : un équipement JAMAIS observé — adopté il y a
-- cinq minutes, pas encore pingué — et un équipement dont la surveillance a bien
-- établi un constat que le contrôleur n'a jamais reçu. Le second cas s'est produit :
-- un CSS326 est resté « unknown » dix-neuf heures alors qu'il répondait sans faute,
-- sa seule transition ayant disparu dans une coupure de tunnel (réparé par la
-- réannonce à la reconnexion, cf. tunnel.Event.Resync — cette colonne rend le trou
-- VISIBLE si jamais il se reformait ailleurs).
--
-- NULL = jamais observé. C'est la même convention que `observed_at` juste au-dessus,
-- et pour la même raison : on ne prétend pas savoir.
alter table devices
  add column if not exists status_at timestamptz;

-- Ce que cette date est, exactement — et ce qu'elle N'EST PAS.
--
-- Elle marque le moment où le constat courant a été ÉTABLI, pas le moment où
-- l'équipement a été vu pour la dernière fois. La nuance est réelle et il faut la
-- tenir : la surveillance ne remonte que des TRANSITIONS, donc un équipement en
-- ligne depuis trois mois porte une date vieille de trois mois sans que rien ne soit
-- en retard. Lire cette colonne comme une fraîcheur ferait donc alerter sur le parc
-- le plus sain.
--
-- Ce qu'elle permet, en revanche : dire « injoignable depuis 2 h » plutôt
-- qu'« injoignable », et distinguer « jamais observé » de tout le reste. Une vraie
-- mesure de fraîcheur demanderait un battement périodique du proxy — non construit,
-- et c'est un ajout au protocole, pas une lecture de cette colonne.
comment on column devices.status_at is
  'Date d''établissement du constat de joignabilité (NULL = jamais observé). '
  'PAS une date de dernière vue : la surveillance ne remonte que des transitions.';
