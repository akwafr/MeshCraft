-- Le résultat de la DERNIÈRE pose de configuration.
--
-- Une pose peut réussir en laissant des choses de côté : le garde-fou refuse de
-- déplacer un port qui porte l'administration, et il a raison de le faire. Mais
-- si cette information ne vit que dans le message d'un événement, l'exploitant
-- voit une configuration « posée » et un écran qui affiche autre chose, sans
-- jamais savoir pourquoi. C'est le pire des deux mondes : le garde-fou protège,
-- et passe pour un bug.
--
-- On garde donc le détail, attaché à l'équipement, pour pouvoir l'expliquer là
-- où la question se pose.
alter table devices
  add column if not exists last_apply jsonb not null default '{}'::jsonb;
