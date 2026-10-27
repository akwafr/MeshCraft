-- The account name ACTUALLY set on the device.
--
-- meshcraft wants to set `mcadmin` everywhere, so that direct access is
-- predictable. Many entry-level switches only allow one account, `admin`,
-- whose password alone changes. Recording the desired name rather than the one
-- set would send the administrator to log in with an account that does not
-- exist on the device.
alter table device_credential_state
  add column if not exists installed_username text not null default '';
