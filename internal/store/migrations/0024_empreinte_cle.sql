-- Fingerprint of the key that sealed the credentials vault.
--
-- THE TRAP IT DEFUSES. Until now, setting MESHCRAFT_SECRET_KEY on a running
-- instance switched the controller to that key without ever looking at the one
-- stored in the database: the whole vault became unreadable, and SILENTLY. The
-- product kept starting, serving its pages and answering its healthcheck.
-- Monitoring stopped without a word — snmpWatchesFor drops the device whose
-- community cannot be decrypted (internal/api/watch_handlers.go) — and the
-- fleet access credentials became inaccessible. The outage was discovered
-- screen by screen, days later, often after the useful backups had rotated.
--
-- The controller now compares, at every startup, the fingerprint of the key it
-- is given with the one recorded here, and REFUSES to start if they differ. A
-- loud refusal at startup is infinitely preferable to apparent operation that
-- loses the vault.
--
-- The opposite case is just as dangerous and the same comparison catches it: an
-- instance switched to the environment variable then restarted WITHOUT it
-- would draw a fresh key and reseal on top.
--
-- THIS IS NOT A SECRET. HMAC-SHA-256 is one-way and the key is 32 random bytes:
-- there is no dictionary to try. Storing it in the same database as the secrets
-- it describes therefore does not help whoever obtains the dump. That is what
-- allows storing it here rather than asking the operator to keep it elsewhere.
--
-- A ONE-ROW table, and the constraint says so instead of hoping: there is only
-- one vault key per instance, and two rows here would mean two contradictory
-- truths about what sealed the database.
--
-- Deliberately NOT stored in instance_secrets: that table holds the key itself,
-- which the switch-over must eventually DELETE. Mixing the key and its
-- fingerprint in the same table risks a cleanup taking both away — and losing
-- the fingerprint means losing the guard again.
create table if not exists credentials_key_fingerprint (
    -- one: always true, the only possible primary key => a single row.
    one         boolean primary key default true check (one),
    fingerprint bytea not null,
    -- algo: names the derivation, so that a future change of derivation is
    -- recognised instead of being taken for a different key.
    algo        text not null default 'hmac-sha256-v1',
    updated_at  timestamptz not null default now()
);

-- No row inserted here, and that is deliberate: the migration does not know the
-- active key. The first startup after this migration records the fingerprint of
-- the key ACTUALLY in use, after checking that it does open the existing vault
-- (see cmd/controller/keyguard.go).
