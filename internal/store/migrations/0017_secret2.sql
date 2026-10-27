-- A SECOND secret in the vault: SNMPv3 derives TWO keys from it, not one.
--
-- The problem the single column let through: RFC 3414 derives the
-- authentication key and the encryption key with the SAME algorithm, from a
-- passphrase and the engineID. Fed with the same phrase, both derivations get
-- exactly the same input — the encryption key is then literally the prefix of
-- the authentication key. Whoever gets one gets the other, and encryption adds
-- strictly nothing.
--
-- This is not theoretical at our scale: a v3 identity is shared by a whole
-- cryptographic family (that is the role of the `snmpv3-…` tag, see
-- docs/en/03-Security/SNMP identity.md §1). A single compromised device would
-- therefore hand over the whole family's monitoring in clear.
--
-- NULLABLE, and that is the point: only `snmp_v3` has two phrases. An
-- administration password or a v2c community has only one, and forcing them to
-- invent a second would create a secret nothing consumes — hence a secret that
-- is never rotated and gets forgotten. Credentials already in the database
-- (three `admin`, one community) stay intact, column at NULL.
alter table site_credentials
    add column if not exists secret2_enc bytea;

-- The two phrases rotate TOGETHER, so the generation carries the PAIR.
--
-- Separating them would be the mistake: a generation carrying only the
-- authentication phrase would leave, after a rotation, the question unanswered
-- — which encryption phrase goes with this one? On a device left behind,
-- getting the pair wrong does not give a readable authentication error, it
-- gives silence: the v3 agent drops the packet it cannot decrypt.
alter table credential_generations
    add column if not exists secret2_enc bytea;
