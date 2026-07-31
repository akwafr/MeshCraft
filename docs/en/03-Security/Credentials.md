---
tags: [security]
---

# Credentials

Operators supply device access material; meshcraft stores it encrypted and uses
it for discovery, apply, and monitoring.

## Status (summary)

| Topic | Status |
|-------|--------|
| AES-256-GCM sealed secrets, never listed in clear | **shipped** (`internal/secretbox`) |
| Closed kinds: `admin`, `snmp_v2c`, `snmp_v3` | **shipped** |
| Scope: tag > group > site, ambiguity signalled | **shipped** |
| Stop after 2 failures per account (AAA lockout) | **shipped** |
| Reveal audited for `site_admin` | **shipped** |
| Envelope KEK→DEK per site | decided, not built (single instance key today) |
| Transport-specific kinds (`ssh_password`, …) | decided, not built |

Do not confuse **site credentials** with **instance known credentials**
(`known_credentials`) used blindly during discovery (factory defaults).

French deep dive: [Credentials](../../fr/03-Sécurité/Credentials.md).
