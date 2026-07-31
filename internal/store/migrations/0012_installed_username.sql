-- Le nom de compte RÉELLEMENT posé sur l'équipement.
--
-- meshcraft souhaite poser `mcadmin` partout, pour que l'accès direct soit
-- prévisible. Beaucoup de switchs d'entrée de gamme n'admettent qu'un seul
-- compte, `admin`, dont seul le mot de passe change. Enregistrer le nom
-- souhaité plutôt que le nom posé enverrait l'administrateur se connecter avec
-- un compte qui n'existe pas sur l'équipement.
alter table device_credential_state
  add column if not exists installed_username text not null default '';
