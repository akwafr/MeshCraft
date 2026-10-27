---
tags: [security]
---

# Credentials — the device credentials vault

The user supplies what is needed to connect to devices; meshcraft keeps it encrypted and uses it at the right moment ([[Discovery]], [[Reconciliation]], [[Monitoring]]).

## Where this page stands

| Question | Status |
|---|---|
| Secrets sealed with **AES-256-GCM**, nonce per value, never returned by lists | **shipped** — `internal/secretbox`; lists only expose a "a secret exists" boolean |
| Kinds `admin`, `snmp_v2c`, `snmp_v3`, a **closed** list on the API side | **shipped** — `checkCredScope`, `internal/api/sitecreds_handlers.go` |
| Scope by tag > group > site, with priority, and **ambiguity reporting** | **shipped** — `CredentialsForDevice`, `credential.scope_ambiguous` event |
| Stop after 2 failures per account so as not to trigger an AAA lockout | **shipped** — `triesPerAccount`, with a slot reserved for the factory account |
| Revealing a secret: `site_admin`, site membership checked, traced by name | **shipped** — `POST …/reveal`, `credential.revealed` event |
| Generating a site credential when the site is created, and catch-up at start-up | **shipped** — `EnsureSiteAdminCredential`, `BackfillSiteCredentials`, `internal/pwgen` |
| **Envelope** encryption KEK → DEK per site, DEK rotation site by site | **decided, not built** — there is a **single** key for the whole instance |
| Kinds `ssh_password`, `ssh_key`, `http_login` | **decided, not built** — the only write transport today is the device's web interface |
| Remembered `device.credential_id`, restriction by subnet | **decided, not built** — scope replaces association, and there is no subnet filtering |
| Master key **outside the database** by default | **partial** — two regimes, see § *Storage* |
| Break-glass access per device or per site | **decided, not built** — the only way a secret leaves is `reveal`, one at a time |

## Types

The vault carries **three** kinds, and the list is closed: any other value is refused with a 400. The columns below distinguish what exists from what remains to be built, because both used to be presented side by side and read like an inventory.

| Type | Fields | Use | Status |
|---|---|---|---|
| `admin` | user, password | the device's administrator access, whatever its transport | **shipped** |
| `snmp_v2c` | community, **read-only** | polling, discovery | **shipped**, site scope enforced |
| `snmp_v3` | user, authentication passphrase, privacy passphrase | same, preferred | **shipped**, tag scope enforced — see [[SNMP identity]] |
| `ssh_password` | user, password, optional enable/secret | SSH (and Telnet) CLI | **decided, not built** |
| `ssh_key` | user, private key (ed25519/rsa), optional passphrase | SSH CLI — "SSH certificate" in the broad sense, including signed OpenSSH certs | **decided, not built** |
| `http_login` | user, password (+ driver hints: realm, path) | REST API / webform of HTTP devices | **decided, not built** |

Why `admin` alone rather than three kinds per transport: the first supported product line has **neither SSH nor a serial port** ([[Connectors]]), and its only door is its web interface. One kind per transport would therefore have asked the operator to choose a transport before knowing which one the device offers — when that is precisely what the connector knows and they do not. The kind will state the transport on the day a connector offers several; until then, `admin` means "administrator access", and the connector decides how it is used.

> [!note] Not to be confused with discovery's standard credentials
> **Another** table exists, `known_credentials`, with its own kinds
> (`ssh`, `telnet`, `http`) and its own routes reserved to `global-admin`:
> these are the factory passwords tried **blindly** before a device
> belongs to a site ([[Discovery]]). It is sealed by the same key, and
> that is the only thing it shares with the site vault. Mixing them
> would have `admin/admin` tried on a production fleet.

## Scope and trial order

- A credential belongs to a **site**, and can be restricted to a **device group** or to a **tag** ([[Tags and credential scope]]) — never both at once, and the refusal is explicit. **Shipped.** Restriction to **subnets** or to **specific devices** remains **decided, not built**: there is no subnet filtering in the code.
- Resolution is ordered by **specificity** then by **priority**: tag (3) before group (2) before site (1), and at equal specificity the priority declared by the operator. The **whole** list is returned to the caller, deliberately: two tags at the same priority are an **ambiguity**, which comes up on screen and in the log (`credential.scope_ambiguous`) instead of being settled silently by an `order by` — because the arbitrary choice would have worked, and would have started picking the other one on the day of a rename.
- At adoption, **ordered** trial on the detected transport; **stop after 2 failures per account** so as not to trigger an AAA lockout ([[Discovery]]). **Shipped** (`triesPerAccount`), with a refinement the design had not foreseen: a **slot is reserved in advance** for the factory account, otherwise a fleet already taken over consumed the trial budget and a new device became impossible to adopt.
- Once the device↔credential association is established, it is remembered: **replaced by something else**, and that needs saying because the column does not exist. There is **no** `devices.credential_id`. What is recorded is more useful: the **observed** state (`device_credential_state`) — which credential, which generation and which **account name** actually worked on that device. The association is **inferred** from scope, the state records what the device carries. A frozen pointer would have lied at the first tag change; scope follows, and state corrects.
- A password change across the fleet = editing a single credential — **true**, and it is even the same code as "new generation" ([[Service identity and rotation]] §13.1).
- The intent model references credentials by opaque URI `cred://…`, never by value ([[Standard data model]]).

## Storage

```
instance key   ── env (MESHCRAFT_SECRET_KEY) or database
   └── secrets ── AES-256-GCM, 12-byte nonce prefixed to each value
```

What the design described was a two-tier **envelope**:

```
master key (KEK)             ── env/KMS/sealed file at start-up
   └── data keys (DEK) per site  ── encrypted by the KEK, in the database
          └── secrets            ── AES-256-GCM by the site's DEK
```

> [!important] The envelope is decided, not built: there is ONE key
> No KEK, no per-site DEK. A single 32-byte key seals everything the
> instance keeps: `site_credentials` (both fields, the second v3 passphrase
> included), `credential_generations`, and `known_credentials`.
>
> What the envelope would bring, and which must therefore not be believed to be in place:
> **rotating one site's key without touching the others**, and above all **a compromise
> radius bounded to the site**. Today, the single key yields the whole fleet of
> the whole instance.
>
> What the single tier gains, and which explains the order: there is only one
> secret to know how to protect, hence only one place to get it wrong. The envelope adds
> an indirection whose value is nil as long as the key above is not
> better guarded than the one below — and that is exactly the work in progress
> (see below). The envelope comes **after**, not before.

- The master key **should** be outside the database: environment variable, file, or external KMS. In practice there are **two regimes**, and one of them is not the one we want:
  - `MESHCRAFT_SECRET_KEY` set (64 hexadecimal characters) → the key lives outside the database, as planned;
  - variable absent → the key is **stored in the database**, in the same database as the secrets it protects. The controller **warns about it at start-up** in plain words ("a dump leak would expose the secrets"), and the *Configuration → Credentials* screen tells the operator too, because a warning that only comes out in the container logs is read by nobody.

  The default is that one because the alternative was to refuse to start without the variable, which would have made a trial instance impossible to launch — and because a vault encrypted with a badly stored key is still better than a vault in clear. But it is **not** an acceptable state in production: in this regime, a PostgreSQL dump is enough.

> [!note] Switching to an environment key is work in progress
> Moving an existing instance from the "key in database" regime to the "environment
> key" regime is not just a matter of setting the variable: it requires re-encrypting, and
> knowing *before starting* whether the key supplied is indeed the one that opens the vault.
> This mechanism is under construction at the time this page is written; the page does not
> describe it, because documenting work in motion produces a wrong page
> faster than a missing one. The only thing to remember here: **setting the
> variable alone switches nothing**, and the controller says so in its warning.

- Per-site DEK rotation possible without re-encrypting the other sites — **decided, not built** (there is no DEK).
- Secrets do not come out of lists: the API returns the metadata (name, type, user, last rotation, presence of a secret) but **never the value** — write-only, atomic replacement. **Shipped**: the list query does not even select the sealed field, only `octet_length(secret_enc) > 0`. Seeing a credential is an `operator` right, reading it a `site_admin` right: *seeing* and *reading* are not the same permission.
- In memory: decrypted at use, minimal lifetime; never written to logs or transcripts. **Shipped, with a named hole**:
  - no secret is logged, and the debug log **automatically masks** any value whose attribute key contains `password`, `secret`, `token`, `cred`, `passwd` or `key` — a deliberately wide net, because a mask that lists the fields to mask forgets one;
  - the **transcript** returned to the operator only carries the account name tried and the **reason** for its position in the order, never the secret. The secret that worked is not stored in the transcript for all that: it is the **connector** that remembers the accepted candidate at the moment the device says yes, and returns it through `driver.Admin.Accepted()`. This wording replaces the one that described a secret "recovered by replaying the order of candidates": that mechanism did exist, but it **compared the candidate's reason with the transcript step's label** — two strings the code does not build identically — and therefore **always returned an empty value**. Inconsequential on the Instant On, which ignores the old password; fatal on a platform that validates it, like SwOS and its RC4 key. A secret inferred from a sentence written for a human is a secret a rewording silently breaks;
  - **the exception**: `POST /api/devices/{id}/web-probe` returns the **raw body** of a page of the device interface (up to 8 MiB) without any masking. If an admin page echoes a password in its HTML, it comes out in clear. It is a connector debugging probe, and it is doubly restricted (`site_admin` **and** `global-admin`) and logged — but it is not masking, and a page that should not have come out comes out.
  - **a second exception, open and not arbitrated**: the configuration backup the SwOS connector takes **just before writing** is returned as is in `ApplyResult.Backup`, hence in the HTTP response of the write. It carries the device's password block (`.pwd.b`) and its SNMP community — observed on a CSS326-24G-2S+. It is not a debugging probe reserved to `global-admin`: it is the ordinary write path, at `operator` level. The log, for its part, is clean: `last_apply.backup_bytes` only records the **size**. The difficulty is not to stop returning it, it is to decide **where it lives**: on this platform, `Commit` is `immediate` and this copy is the **only** rollback that exists — it cannot be reconstructed, and discarding it silently would be announcing a safety net that has been removed (see [[Reconciliation]]). The batch that will keep it (`device_backups`) is decided, not built.
- On the [[Proxy]] side: no secret persisted — **shipped**. SNMP communities do travel in the RPCs through the tunnel at the time of use and stay in memory; SSH/TLS streams cross it encrypted end to end ([[Controller-proxy protocol]]). Two nuances in [[Proxy security]]: the proxy persists **its own** identity secret, and a non-encrypted relayed protocol (HTTP, telnet) crosses its process in clear.

## What meshcraft installs itself

The vault described above contains what **the user supplies**. Meshcraft also pushes its **own** accesses onto devices — administration account, SNMP v3 user (or dedicated read-only community), streaming telemetry subscription — which it owns and whose rotation it takes on. It is a distinct family, with its own lifecycle: see [[Service identity and rotation]].

Status to know before reading on: **setting** the administration identity is shipped (`POST /api/devices/{id}/credentials/apply`, with verification through a real session), SNMP setting too ([[SNMP identity]]). The **service CLI account**, **streaming telemetry** and **automatic rotation** are not built.

Two rules of this note that correct a tempting simplification:

- the **policy** (account name, algorithms, rotation period, hardening) is defined at instance level and overridable per site — **decided, not built**: there is no policy table. What exists and stands in for it: SNMP algorithms have a **hard-coded floor** (`sha256`/`aes128`) and the desired account name is a constant (`mcadmin`), arbitrated by the connector;
- **secrets**, for their part, are generated **per device** — a service secret common to a whole fleet makes a single compromised device the key to all of it, whereas no human ever has to type it. **This trade-off was reversed**: operations settled on a secret **per site**, and the reason is solid — see [[Service identity and rotation]] §12. The per-device option exists in the database (`per_device`) but is **never exercised**: no code creates a per-device generation.
- a service secret is **never overwritten**: each change creates a **generation**, because between the change in the admin and the end of the rollout the fleet carries several secrets at once. **Shipped** — it is this history that makes a password change survivable on a device that stayed switched off, and [[Service identity and rotation]] §13 describes the verified detail.

The documented exception to "a secret never leaves the API" is **break-glass access**, reserved to `global-admin` and traced: without it, a meshcraft outage would make the customer's fleet unmanageable.

> [!note] Break-glass as described does not exist; what exists is narrower
> There is **no export** per device or per site, and nothing that requires
> re-authentication. What is shipped is **single reveal**:
> `POST …/reveal` for the current credential, `POST …/generations/{n}/reveal` for
> a specific generation — the one a device left behind still carries.
> `site_admin`, membership of the URL's site checked (otherwise it would be enough to
> guess a UUID), and name-based logging as `credential.revealed`, with
> severity *warning*.
>
> It is break-glass scaled down to the site, and it is a defensible choice:
> the gesture needed during an outage is "give me the password of THIS switch",
> not "export two hundred secrets for me". What is missing is therefore not the
> feature, it is what surrounds the exception — re-authentication,
> notifying the other administrators, and a dedicated non-deletable event.
> Meanwhile, a compromised `site_admin` reads the secrets of their sites, and the only
> thing that catches them is the log.
