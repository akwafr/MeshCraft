-- Normalise les notes de joignabilité écrites AVANT que le compte de pertes n'en
-- sorte.
--
-- `status_note` portait « ne répond plus au ping (3 pertes consécutives) ». Ce compte
-- croît tant que l'équipement est muet, donc une note qui le porte change à chaque
-- observation — ce qui empêchait toute déduplication (cf. store.SetDeviceStatus). Il
-- vit désormais dans le message du journal, où il est informatif, et la note décrit un
-- état stable.
--
-- POURQUOI UNE MIGRATION DE DONNÉES, ET PAS SEULEMENT DE FORME. Sans elle, la première
-- réannonce de chaque équipement déjà injoignable verrait une note différente, en
-- conclurait « ça a changé » et produirait deux mensonges d'un coup : une ligne de
-- journal annonçant une bascule resynchronisée qui n'a pas eu lieu, et un `status_at`
-- daté de la mise à jour — donc « injoignable depuis 0 min » sur un équipement tombé
-- depuis trois jours. Neuf équipements sont dans ce cas sur l'installation de
-- référence.
--
-- `status_at` reste volontairement NULL : on ne sait pas depuis quand ces constats
-- tiennent, et le fabriquer serait pire que de l'admettre. L'écran retombe alors sur la
-- note seule, ce qui est exactement le comportement voulu pour une ligne antérieure à
-- la colonne.
update devices
   set status_note = 'ne répond plus au ping'
 where status_note like 'ne répond plus au ping (%pertes consécutives)';
