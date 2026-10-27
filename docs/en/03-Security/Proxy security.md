---
tags: [security, proxy]
---

# Proxy security

The [[Proxy]] is a foothold in the customer's network: it is the most sensitive asset of the system after the [[Credentials]] vault.

## Where this page stands

| Question | Status |
|---|---|
| Enrollment token: single use, 1 h TTL, coverage **frozen at issuance** | **shipped** — atomic consumption (`UseEnrollToken`), `enrollTokenTTL`, `requestedCoverage` |
| Site coverage as a rights boundary, instance proxy, refusal of the last removed site | **shipped** — see the [[RBAC]] matrix |
| No SNMP write path, in either direction | **shipped, and guaranteed** — no `snmpset` in the repository; the proxy only exposes `BulkWalk` |
| One-off questions from the controller (`ask`): read-only SNMP walk, source address | **shipped** — § *What the proxy agrees to do* |
| A trap's content is never a source of state | **shipped** — the trap only **triggers a poll** ([[Monitoring]]) |
| The proxy cannot **assign** anything to a site | **shipped** — `host.seen` is ignored, a candidate is born without a site |
| systemd hardening (non-root, capabilities, `ProtectSystem=strict`) | **shipped** — unit **generated** by `internal/distrib/install.go` |
| Automatic rollback on a binary that does not start | **shipped** — `cmd/proxy/upgrade.go`, 2 attempts then fallback to the image's binary |
| Proxy authentication by **ed25519 key pair** and signed challenge | **decided, not built** — today it is a shared secret (§ *Enrollment*) |
| Revocation that **drops the live session** | **shipped** — `Hub.CloseSession`, called by revocation, **deletion** and migration, plus a periodic safety net (`revalidateTunnels`, every minute) |
| Dial allowlist in `proxy.yaml` | **partially shipped** — environment variable, and **open by default** |
| Per-proxy ingestion quotas, fuzzed parsers | **not built** |
| **Signed** binary, CA pinning, local status `127.0.0.1:9464` | **not built** |
| The controller distrusts the proxy **up to its coverage** | **shipped** — `internal/api/proxyscope.go`: a fact is only accepted on a device the controller asked **this** proxy to monitor |

## Enrollment

1. A `site_admin` or a `global-admin` generates an **enrollment token** in the UI: single use, short TTL (1 h), **carrying the site coverage** of the future proxy ([[Proxy]]). A site-admin can only put in the sites they administer — that is where partitioning is decided: they cannot manufacture a proxy that would see another site's network. Only a `global-admin` issues an instance proxy token ([[RBAC]]). Coverage is **frozen at issuance**, never re-read at enrollment: the issuer's rights may change in between. **Shipped**, including atomic consumption — invalidating the token and reading its coverage are a single `UPDATE … RETURNING`, which prevents a token from being used twice by two simultaneous enrollments.
2. `meshcraft-proxy enroll --controller wss://ctrl.example --token XXX`. What the proxy does next is **weaker than the design**: it posts `{token, name}`, and the controller **sends back a secret** of 32 bytes of which it keeps only the SHA-256.
3. The controller registers the proxy (`proxy_id`, secret fingerprint, coverage) and invalidates the token. **Shipped.**
4. Subsequent connections: the proxy **replays that secret** on every call — in the JSON body of the heartbeat and metrics, in the `X-Meshcraft-Proxy-Id` / `X-Meshcraft-Proxy-Secret` headers before the tunnel upgrade. No mTLS required (corporate TLS middleboxes break it) — authentication is indeed application-level, inside the WSS, as decided.

> [!warning] The ed25519 key pair is decided, not built — today it is a bearer token
> There is **no ed25519 in the proxy's or the controller's code**, and no
> challenge. Authentication is a **shared secret**, comparable to a bearer
> token, with four consequences an operator must know:
>
> - it is **replayable**: anyone who reads it can impersonate this proxy.
>   A private key signing a challenge cannot be replayed, and that is the whole point
>   of the decided scheme;
> - it is **written in clear** in `/var/lib/meshcraft/state.json` (file `0600`,
>   directory `0750`). Reading that file is enough; with a private key, one would
>   still have to get it to sign;
> - it has **neither expiry nor rotation**: the `proxies` table has no
>   expiry column and no code renews it;
> - it travels **in full** with every request, so anyone terminating TLS
>   sees it — whereas a signature never reveals the secret that produced it.
>
> The design remains the right target, and it is cheap to reach:
> `enroll` carries a public key instead of a name, and the tunnel preamble
> carries a signature instead of a secret. Keeping it written here is what prevents
> the bearer token from being considered the final state.

Revocation from the UI (stolen/compromised proxy): `POST /api/proxies/{id}/revoke`, reversible. **Shipped**, and it **cuts the live session** — here is exactly what the gesture does.

`revoked` is still checked at **authentication**: the heartbeat, metric uploads and the **opening** of a tunnel get a 401. But `handleRevokeProxy` additionally calls `Hub.CloseSession` (`internal/tunnel/hub.go`), which **removes the session from the `Hub`'s table then closes the yamux multiplexing**: `Dial`, `Ask` and `PushWatch` no longer find a session to talk to. It is the network position that is cut, not just the front door. That is the scenario the button exists for — on a stolen proxy, the attacker controls the process, they will not reopen their connection, they will **keep** it.

The reason for the cut goes to **two** logs: `tunnel cut` on the application side (with how long the session had lasted) and, in the [[Event log]], the message of `proxy.revoked` itself, which gains "— live tunnel cut" **when the cut actually took place** (`CloseSession` returns false if there was nothing to cut). Without that mention, nothing in the log would distinguish a gesture that reached the live session from a gesture that only touched the row in the database. And the cut proxy reconnects straight away — its heartbeat keeps running, by design — so its own log will show "tunnel interrupted": without the controller-side line, the operator would go hunting for a WAN outage for a cut they asked for.

> [!warning] Order is the security: the database first, the session next
> Cutting before having written the revocation would not offer a revocation but a
> **disconnection**: the proxy reconnects within the second (its backoff restarts from
> zero after a session that held) and authentication would still accept it, against
> a database that does not yet know it is revoked. The result would be a spectacular
> and useless cut — the worst of both worlds, since the operator would have
> seen something happen.
>
> This order is **locked by a test**, not by a convention: it compares
> the **positions in the source** of the database write and of the call to
> `CloseSession`, in the three handlers concerned, by walking the package's AST.
> A functional test could not do it — both orders give the same observable
> result as long as the proxy does not reconnect fast enough —, and that is precisely
> why the lock is syntactic. It also fails if one of the three handlers
> **stops** calling `CloseSession`.
>
> Re-enabling cuts nothing, obviously: it is the inverse gesture.

Cutting on click is not enough, however, and the gap is not theoretical: a revocation made **directly in SQL**, pronounced by **another instance**, or occurring while this controller was **restarting** would leave a revoked proxy connected indefinitely. And revocation is precisely the gesture made in a hurry, when one does not want to depend on any interface. Hence `revalidateTunnels` (`internal/api/proxy_handlers.go`), started by `Router()` for the life of the process, which asks the database again whether established tunnels still have the right to exist.

> [!note] The safety net's rate, and why it cuts nothing when it does not know
> `tunnelRevalidate` is **one minute**, and the trade-off reads both ways.
> The upper bound is an **exposure window**: the time during which a proxy
> revoked outside this API keeps its connections and the monitoring already received. The
> lower bound is the **cost**: one primary-key read per connected proxy,
> when the heartbeat of those same proxies already touches the same row three times every
> thirty seconds. Going lower would buy a few seconds at the price of a
> loop that learns nothing almost all of the time. A full pass is bounded
> by `tunnelRevalidateBudget` (30 s): it would be replayed on the next tick anyway,
> and a database that does not answer within thirty seconds will say nothing useful in
> five minutes.
>
> **A read error cuts nothing** — "I don't know" is not "it is
> revoked". This is not a tolerance added after the fact: `tunnelKeep`, the
> "keep the tunnel" verdict, is the type's **zero value**, deliberately, so that any
> future code gap reads that way. The opposite default would disconnect the whole fleet at
> the first second of PostgreSQL unavailability — a cure far worse than the
> disease. The refusal is logged, the proxy left alone.
>
> The safety net also catches the **deleted** proxy (`tunnelGone`: no more row in the
> database). What it cuts, it announces in the [[Event log]] with an
> instance event of its own, `proxy.tunnel_cut` (severity **warning**, actor
> `meshcraft`), which says explicitly "observed by periodic revalidation, not
> a user action": nobody clicked, and that is precisely the information.
> Without it, the operator would see a proxy drop with no visible cause.

Two other gestures cut, for the same reason they close an access:

- **deletion** (`handleDeleteProxy`) — at least as strong as revocation: with the row gone, nobody can see or revoke this proxy any more, whereas its session would still work. Leaving it open manufactured a foothold in the network **outside any inventory**;
- **migration** (`handleProxyMigrated`) — a proxy that has moved to the neighbour no longer has any reason to receive *this* instance's monitoring, nor to open connections here. The migration sequence does not suffer from it: when the proxy posts here, it has already enrolled on the target and persisted its new state; this call goes over HTTP and not through the tunnel, and the proxy even treats its failure as inconsequential.

> [!note] What "revoke" was worth before this batch
> A refusal at the **next** authentication, and nothing more. The already
> established session went on opening connections to devices (`Dial`), writing
> events to the database (`AcceptEvents`), answering questions (`Ask`) and
> receiving the monitoring configuration (`PushWatch`) — hence the fleet's **SNMP
> communities in clear**. The `Hub` did not know about revocation and never revalidated:
> an established tunnel remained fully usable until it broke
> on its own. Revoking in the UI gave the feeling of having cut the foothold in the
> network when only the front door had been closed. It is written here because
> every deployment predating this batch still behaves that way.

Two corollaries, more minor and **unchanged** by this batch: "the key is blacklisted" means nothing as long as there is no key (the `secret_hash` stays intact, and un-revoking restores the **same** credential); and **scan ranges** do not become orphans — they stay attached to the revoked proxy. They are only erased on **deletion** of the proxy, by cascade. A revoked proxy also deliberately remains a candidate for access paths (`ProxiesCoveringSite` does not filter it out), so that the inventory does not lose the memory of who reached what.

Devices, for their part, are not attached to this proxy ([[Proxy]]): they remain drivable by any other proxy that reaches them, and those for which it was the telemetry collector see their trap/syslog target recomputed ([[Monitoring]]) — the recomputation itself remains **decided, not built** (see *The election* in [[Monitoring]]). On deletion of the proxy, `devices.proxy_id`, `discovered.proxy_id` and `devices.trap_dest_proxy_id` do go back to `null`.

## What the proxy agrees to do

The controller does not only push a monitoring configuration: it also asks the proxy **one-off questions** — one question, one answer, close. There are exactly **two**, and the brevity of this list is least privilege itself:

| Question | What it returns | Exposure |
|---|---|---|
| `snmp_walk` | a **read-only** SNMP walk (`BulkWalk`), numeric OID validated, hard bounds of 500/5000 rows and 15/60 s | `POST /api/devices/{id}/snmp-walk`, **global-admin** only |
| `source_addr` | "through which local address do you reach this IP?" — a UDP `connect` without sending, whose local address is read | **no** HTTP route: internal use, to set the trap destination ([[Monitoring]]) |

Three things are worth saying because they are decisions, not accidents:

- **there will be no write.** The proxy's SNMP interface comprises only `BulkWalk` and `Close`, and there is no `snmpset` in the repository. Adding a write verb would turn the proxy into a single point from which to reconfigure the fleet; configuration goes through the connectors, across the TCP relay, with the device's authentication ([[Connectors]]).
- **`source_addr` is not exposed over HTTP**, and deliberately so: it is an internal diagnostic primitive. Opening it would give anyone with an account a way to map the interfaces of the customer's network, one IP at a time.
- **the address is asked for, not inferred.** The proxy declares its interfaces, but which one is used to reach *this* device depends on the routing table. Guessing would have set in the devices a trap destination towards an address through which they do not answer — a silent outage.

The four stream verbs are, symmetrically, just as few: `dial` (TCP relay), `watch` (the controller pushes what to monitor), `ask` (above), and `events` — the only one that flows upwards. An unknown question is **refused with its name**, never silently absorbed.

## Least privilege, in both directions

**The proxy holds nothing** — almost. No configuration, no business data, no SSH or HTTP credential: the controller speaks these protocols itself through the relay, and the proxy only copies bytes. To be qualified on two points, because "nothing" is too simple:

- it receives **read-only SNMPv2c communities**, necessarily — without them it cannot poll. They come from the vault, stay **in memory only**, are never written to disk, and an explicit rule in the code forbids logging them (respected: a walk's trace carries only the IP, the OID and a row count);
- it persists **its own** identity secret in clear in its state file (see the enrollment callout);
- and a **non-encrypted** relayed protocol (HTTP, telnet) crosses the proxy process in clear. It is not a *stored* secret, it is a secret *seen in flight* — the distinction matters for risk analysis, not for the operator.

Its compromise therefore yields a network position (serious), this proxy's identity, and the read communities of the devices it monitors — but no administrator access to the fleet (see [[Credentials]]).

**The controller distrusts the proxy**: everything that flows up (scan results, syslog, traps, RPC results) is treated as untrusted input. What is shipped of this distrust:

- **a fact only counts on a device entrusted to this proxy** — the scope check below, and the piece that was missing the longest;
- **a trap's content is never a source of state.** A trap only *triggers* a poll, whose result is what counts ([[Monitoring]]). It is the most important hardening of the batch, because a trap is an unauthenticated UDP datagram that a third party on the network can forge;
- **identity by source IP**: a proxy only processes a trap if the source appears in **its** watch list, and the community must match. A trap from an unwatched source is dropped and counted;
- **anti-amplification**: 250 ms coalescing and a 2 s floor between two triggered polls, so a port flapping in a loop does not produce one walk per flap;
- **anti-flap** in the log (`portevents.go`): an unstable port produces **one** `port.flapping`, not fifty lines;
- **load bounds** on metrics: 20,000 samples and 8 MiB per batch, explicit refusal with 413 — and a test locks the two bounds together, because a batch accepted by one and refused by the other caused an infinite replay;
- `host.seen` is **ignored**: a proxy that sees a host creates nothing.

What is **not** built, and must not be believed to be in place: **per-proxy ingestion quotas** (no rate-limit anywhere; a rogue proxy can flood the [[Event log]] with `device.up`/`down`, anti-flap only covering ports — the scope check below now bounds the **target** of that flood to the devices entrusted to it, not its **rate**), and **fuzzed parsers** (no `func Fuzz` in the repository).

**The scope check — which device a proxy has the RIGHT to talk about** was the most serious gap on this page; it is **shipped** (`internal/api/proxyscope.go`, consulted from `handleProxyEvent` and `applyPortTelemetry`). The invariant fits in one sentence:

> [!important] The controller only accepts a fact about a device if it asked **this** proxy to monitor it
> The `DeviceID` of a tunnel event — like the `device` label of a
> metric sample — is chosen by the **proxy**. It is now checked against
> the set that the **last monitoring push** entrusted to this proxy
> (`PushWatchTo`, kept in memory by `recordProxyScope`), **before** the fact
> writes anything — device status, port transition in the
> [[Event log]], link state in the inventory (`devices.ports`).
> When refused, the fact is not silently absorbed: it goes to the log as a
> **warning**, with the proxy, the device, the missing authority and the age of the
> instructions — but **damped to one line per proxy per minute**, because a looping proxy
> must not be able to drown the one thing that would make it possible to
> diagnose it.

What was open before this batch, and is worth knowing because every earlier deployment behaves that way: an authenticated proxy of site A could mark site B's devices unreachable, fabricate port transitions there and write telemetry there. It could not configure them or read their secrets — the barrier held where it matters most —, but it could **lie about the whole fleet** whereas this entire page says it can only lie about what it sees. And coverage is not a decorative boundary: it is the same one [[RBAC]] defends site by site, and **the only** reason a site-admin cannot manufacture a proxy that would see elsewhere. Leaving it open for writes made partitioning partial — we prevented *seeing* another site's network, not *writing* false monitoring into it.

Two traits of the chosen form matter as much as the check itself:

- **the same source of truth as the pushed instructions.** The rule is not a coverage check redone by hand: it is a lookup of what **actually went out** through the tunnel. A second coverage computation, written elsewhere, would sooner or later say something different from the first — and a divergence between the two would show up as legitimate facts being refused, that is, as a monitoring outage nobody would attribute to a security rule;
- **an in-memory lookup, hence free.** This path sees **all** the fleet's port transitions go by, the most frequent event in the product. A database query per transition would have put SQL load where none is needed, and the first reaction to that load would have been to want to bypass it.

> [!warning] Two granularities, and confusing them would be a false positive or a hole
> The monitoring push produces **two** sets, and the authority required depends
> on what the fact derives from ([[Proxy]], § *The trade-off*):
>
> - **ICMP** targets are by **site coverage** — several proxies may
>   legitimately ping the same device. It is this set that authorises
>   `device.up` / `device.down`;
> - **SNMP** targets are restricted to the **declared collector**. It is this
>   set that authorises port transitions, `trap.seen` and port
>   telemetry, since all three derive from polling.
>
> A counter-intuitive and intended consequence: a proxy reporting a port
> transition for a device it **pings but does not poll** is at fault, even if
> its coverage is impeccable — it has no way of knowing its ports. The
> refusal has its own verdict ("pinged but not collected"), distinct from "never
> entrusted": the first is repaired by fixing an attachment or waiting for a
> change to propagate, the second means a proxy is talking about a site it does not
> cover. Flattening them into a boolean would make the log unusable at the precise
> moment one must decide whether it is a defect or an attack.
>
> Requiring only coverage would have let the most rewarding attack through — writing
> into the port inventory and the fleet log of a device for which one is not
> the collector; requiring only collection would have cut reachability for the whole
> fleet attached elsewhere.

> [!note] The retained set is NOT purged when the proxy disconnects, and that is a choice
> Purging it would lose, on every reconnection, exactly the transitions
> accumulated **during the outage** — the only ones nobody has seen, hence the most
> valuable. The proxy keeps its configuration and its event queue across
> the tunnel break, and it drains that queue as soon as the session is restored; the controller's memory
> must reflect **what the proxy holds**, and it still holds the old
> instructions. The first batch of a reconnection is therefore judged against the very instructions
> the proxy worked on, and the push that follows replaces them entirely.
>
> That opens nothing: a retained entry only serves to judge the facts of a proxy that
> **talks**, hence of a connected and authenticated proxy. An orphaned entry is inert,
> and it disappears at the next connection of any of its neighbours.

There remains **one window**, written here rather than hidden: at **controller** start-up, memory is empty and **each proxy's first batch is refused** until the first monitoring computation. It is the only case where nothing has really been entrusted — refusing is then the only correct answer — and it is **self-healing**: the push happens at connection, hence once per proxy and per start-up. Accepting "while things settle" would not be a security fix. The refusal carries its own verdict ("no instructions pushed"), which distinguishes it from a real overflow: if it **persists**, it is no longer a start-up race, it is the monitoring computation that is failing.

Since a proxy has no site and discovery can come from any of them ([[Discovery]]), the safeguard is not "one proxy = one site" but: **a proxy cannot assign anything**. **Shipped**: what it reports is only a *siteless candidate* — the table carries it by construction, the site only appears at adoption — and attachment is a human gesture subject to [[RBAC]] on the target site. A compromised proxy can therefore lie about what it sees; it cannot bring a device into a site, nor get a site's credentials entrusted to it: those are only tried after adoption, on the decision of an authorised user.

**The customer distrusts the controller**: the controller can open arbitrary TCP connections via `dial` — that is its function. Limitation: a **CIDR allowlist** the proxy agrees to dial towards. **Partially shipped**, and the gaps change the posture:

- it is configured by the `MESHCRAFT_DIAL_ALLOW` environment variable, **not** by a `proxy.yaml` — that file exists nowhere in the code. The proxy reads only four variables in all. The intent "out of the controller's reach" is held: it is indeed a local configuration, set at installation;
- it is **empty by default, hence fully open**. The proxy's log announces it ("all destinations"), but a one-click deployment sets none. The safe default would have been the opposite; the opposite would have made the product unusable at installation, and that is the trade-off that was made. It is worth knowing rather than assuming;
- it is read **only once at start-up**: tightening it requires a proxy restart;
- it covers **only `dial`**. The SNMP walk, `source_addr`, polling, monitoring pings and range sweeps do not consult it — the code assumes this explicitly. The reason is consistent: those paths do not carry arbitrary streams, they execute a bounded operation decided by the controller. But "the proxy only reaches the allowed CIDRs" would be false.
- one point where it is more solid than planned: a domain name must resolve **entirely** within the allowed perimeter, otherwise it is refused. Otherwise, an accomplice DNS was enough to bypass it. And an **unreadable** allowlist falls back to `127.0.0.255/32` — that is, nothing: a syntax error must not open the network.

Documented honestly: installing the proxy = trusting the controller's operator.

## Runtime hardening

- Non-root process and targeted Linux capabilities: **shipped for the systemd deployment**. The unit is not a file in the repository, it is **generated** (`internal/distrib/install.go`) and served by `GET …/install-script` — a static file would have diverged from the script that installs it. It carries `User=meshcraft`, `AmbientCapabilities=CAP_NET_RAW CAP_NET_BIND_SERVICE` (ARP scan and ports 514/162) with the matching `CapabilityBoundingSet`, `NoNewPrivileges`, `ProtectSystem=strict`, `StateDirectoryMode=0750`, plus `ProtectHome`, `PrivateTmp`, `ProtectKernelTunables`/`Modules`/`ControlGroups`, `RestrictNamespaces`, `RestrictSUIDSGID`, `LockPersonality`, `RestrictAddressFamilies` limited to INET/INET6/NETLINK/PACKET and `SystemCallFilter=@system-service`. The script creates a system account without a shell, checks the SHA-256 **before** installing, and puts the token in `/etc/meshcraft/proxy.env` as `0600` — **never** in the unit, which is world-readable.

> [!warning] The generated Docker deployment, for its part, runs as root
> The `docker-compose.yml` produced by the button requests `network_mode: host` and
> `cap_add: NET_RAW, NET_BIND_SERVICE` — necessary — but **carries no
> `user:`**, and the image built by `cmd/mkoci` declares no `User`. The
> recommended path ([[Docker deployment and IPv6]]) is therefore the **less** hardened
> of the two, which is the opposite of what we want: it is the one everybody
> takes. The controller, for its part, does run as `USER meshcraft`.
>
> With `network_mode: host`, capabilities are enough: root in the container
> brings nothing more than `NET_RAW` on the host, and a dedicated `user:` plus
> `cap_add` gives the same functional result. There is therefore no trade-off to
> make, only a line to write.

- Auto-update (opt-in): **shipped, except for signing**. The binary pulled from the controller is checked by **SHA-256** in-stream during download *and* re-read at start-up before any `exec`; the URL must be on the same host and scheme as the registered controller; the authorisation to execute comes from the **registration in `state.json`**, not from the presence of the file — a binary dropped into the volume by a third party has neither an entry nor an expected SHA; and a binary that does not start triggers an **automatic rollback** to the image's binary after two attempts, with an anti-loop safeguard. On the controller side, deployment is progressive (one proxy in flight), an unanswered offer goes `stalled` after 15 min, and a failure **suspends** offers until a human gesture — see [[Proxy versioning and updates]].

> [!note] "Signed binary" is not yet true, and the trust anchor is missing
> There is **no signature verification**: only SHA-256. Yet
> the expected fingerprint arrives through the **same channel** as the binary (the
> heartbeat's offer), and `GET /downloads/proxy/{name}` is **anonymous**. The SHA therefore protects
> against transfer corruption and against a file dropped
> locally — which is already most of the everyday risk — but **not**
> against a compromised controller, which would sign its own binary with its own
> hash.
>
> A signature is only worth something if its public key arrives by a different route than the
> channel it protects: embedded in the initially installed binary, or set
> at installation. That is what the mechanism will have to provide, otherwise it
> will add ceremony without adding any guarantee.

- TLS: **strict** verification of the controller's certificate — **shipped**, by Go's default behaviour, without any `InsecureSkipVerify` in the proxy path (those in the repository target **devices**, whose certificates are self-signed). **CA pinning** remains **decided, not built**, for lack of a configuration file to put it in. Also worth knowing: clear text is accepted and normalised (`ws://` → `http://`), and a compose generated from a controller reached over HTTP will embed a `MESHCRAFT_CONTROLLER` over HTTP — the tunnel is then not encrypted, and the shared secret travels through it.
- Minimal local logging (no payloads): **shipped**. The status exposed locally on `127.0.0.1:9464` (healthz + Prometheus metrics of the proxy itself) is, however, **decided, not built**: there is **no HTTP server in the proxy**, its only listening sockets are the trap UDP. What exists instead: trap statistics in the log every 5 minutes, and the **controller**'s `/api/healthz`. Diagnosing a silent proxy therefore goes through its logs today, not through a probe — which is precisely what that point wanted to avoid.
