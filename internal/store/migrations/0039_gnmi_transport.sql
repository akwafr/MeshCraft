-- Transport security for a credential used against a STREAMING TELEMETRY target.
--
-- # Why a column, and not a default in the code
--
-- A gNMI subscription authenticates against the device's AAA stack: the
-- `kind = 'gnmi'` therefore carries a DEVICE CREDENTIAL — a username and a
-- password that configure the switch — where `snmp_v2c` only carries a
-- read-only community. That is not a nuance: a leaked community reads a MIB,
-- that password reconfigures the fleet.
--
-- Yet `transport grpc default` without an `ssl profile` — what the EOS
-- connector writes when no profile is asked of it — is PLAINTEXT gRPC: the
-- password travels in the gRPC metadata, in clear, on the administration VLAN.
--
-- Putting this choice in the code would have made it a property of the binary,
-- that nobody decided and nothing records. Here, it is attached to a NAMED
-- credential: enabling plaintext is an explicit, traced gesture, read on the
-- same page as the secret it exposes.
--
-- # Three states and not two booleans
--
-- Two booleans (`plaintext`, `skip_verify`) allow representing "plaintext AND
-- without verification", which means nothing, and "plaintext" would silently
-- win. A single text with three values makes the invalid state inexpressible:
--
--   verify       — TLS, certificate verified. The default, and the only safe
--                  state against someone on the path;
--   skip-verify  — TLS, certificate NOT verified. Exists because these
--                  switches leave the factory with a self-signed certificate
--                  and a site without an authority has nothing to compare
--                  against. Refusing outright would push everyone to
--                  `plaintext`, which is STRICTLY WORSE: an unverified TLS
--                  session still resists passive eavesdropping, plaintext
--                  resists nothing;
--   plaintext    — no TLS at all. The password goes out in clear.
--
-- The default is `verify` for existing rows as for new ones. None is of kind
-- `gnmi` today, so this value changes the behaviour of no credential already in
-- the database: the column is read only by `gnmiWatchesFor`.
alter table site_credentials
    add column if not exists transport_tls text not null default 'verify';

alter table site_credentials
    drop constraint if exists site_credentials_transport_tls_ck;
alter table site_credentials
    add constraint site_credentials_transport_tls_ck
    check (transport_tls in ('verify', 'skip-verify', 'plaintext'));

-- The authority the target's certificate must chain to, in PEM.
--
-- Empty means "the system store", which on a proxy image contains the issuer of
-- no switch — that is precisely why `skip-verify` exists. Filled in, it makes
-- `skip-verify` useless, and it is the only setting in this table that really
-- protects against someone on the path.
--
-- IN CLEAR and not encrypted, deliberately: an authority certificate is public
-- by construction — it is what gets distributed in order to be verified.
-- Routing it through the vault would cost a decrypted read on every monitoring
-- computation to protect data that is not a secret.
alter table site_credentials
    add column if not exists ca_pem text not null default '';
