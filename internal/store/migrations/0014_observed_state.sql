-- L'état RÉELLEMENT lu sur l'équipement, et de quoi attribuer un écart.
-- Voir docs/01-Architecture/Réconciliation §2 et §6.
--
-- Jusqu'ici `devices.ports` portait UNE configuration, sans dire s'il
-- s'agissait de ce que meshcraft veut ou de ce que l'équipement porte. Les deux
-- ne coïncident pas : entre une modification faite dans meshcraft et sa pose, et
-- entre une modification faite à la main sur le switch et sa détection, le parc
-- vit avec deux vérités. Les confondre, c'est ne jamais pouvoir dire laquelle
-- est en retard.
alter table devices
  add column if not exists observed jsonb not null default '{}'::jsonb;

-- Quand l'équipement a été relu. NULL = jamais : on ne prétend pas savoir.
alter table devices
  add column if not exists observed_at timestamptz;

-- Quand l'intention a changé DANS meshcraft, et quand elle a été posée.
--
-- Ces deux dates sont ce qui permet d'attribuer un écart sans le deviner : si
-- l'intention n'a pas bougé depuis la dernière pose et que l'équipement diffère
-- quand même, alors c'est l'ÉQUIPEMENT qui a changé. C'est le seul cas qui
-- mérite d'interrompre l'exploitant.
alter table devices
  add column if not exists desired_changed_at timestamptz;

alter table devices
  add column if not exists applied_at timestamptz;
