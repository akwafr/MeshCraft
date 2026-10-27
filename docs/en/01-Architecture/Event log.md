---
tags: [architecture, monitoring]
---

# Event log

The single chronological thread of everything that happens on the fleet, fed by [[Monitoring]] and by the system's own actions. Visible per site (filtered by [[RBAC]]), in real time in the [[Web interface]].

## The catalogue, arranged by prefix family

For a long time this page arranged the types **by source**, and that is what made it
wrong. Two reasons, and neither is cosmetic:

- **the source is not a property of the type.** `config.applied` is written by an
  operator's hand *and* by automatic apply; `trap.dest_applied` by background
  provisioning *and* by an explicit gesture (with two different severities, cf. below);
  `proxy.coverage_changed` by two routes, one of which is site-level and the other
  instance-level. A "source" column forces you to pick one, hence to lie about the
  others;
- **the log filter is a prefix**, literally: `EventFilter.Type` becomes
  `type like 'config.%'` (`internal/store/events.go`). The **family** is therefore the
  real unit of reading — it is what you type in a search field, it is what decides
  what you see together, and it is what brings up rows you did not expect (cf. *The
  old rows*, below).

Two axes replace the source, and each carries a decision:

- **scope** — non-null `site_id` (the site's log, filtered by [[RBAC]]) or null
  (the **instance** log, reserved for the `global-admin`: cf. the callout below);
- **`actor`** — the address of whoever acted, or `surveillance` / `proxy` / `meshcraft`
  for what nobody asked for. It is what separates **audit** from **observation**, and
  it is the only field that does so.

> [!note] What follows was captured by `grep` in `internal/`, not copied from an intention
> Every type in the first table has a producer named in the code. Every type in the
> second has **none**, and that is said row by row. The distinction is this page's main
> value: without it, you spend an hour looking for the code that writes a type that has
> never been written — and you end up believing it is the code that has a defect.
>
> The following are **not** log types: `device.up`, `device.down`, `port.up`,
> `port.down`, `host.seen`, `sweep.done`, `trap.seen`. They are the event names of the
> proxy → controller **protocol** (`internal/tunnel/control.go`), and the resemblance
> of two of them to log types is a trap: `tunnel.EventPortUp` is what the proxy
> *reports*, `port.up` is what the controller *writes after correlation*, and it
> happens that the first does not produce the second ([[Controller-proxy protocol]]).

### Emitted by the code — 88 types, 19 families

| Family | Scope | Types | What the family logs |
|---|---|---|---|
| `config.` | site | `config.applied` · `config.apply_failed` · `config.drift` · `config.absorbed` · `config.forced` · `config.imported` · `config.import_failed` | the cycle of the desired configuration: apply, failure, drift observed at read-back, absorption of the drift into the intent ([[Reconciliation]]). `config.forced` marks the crossing of a guard, `config.import_failed` is the only **error** type in the catalogue |
| `credential.` | site, except `credential.created` (instance) | `credential.created` · `credential.generated` · `credential.updated` · `credential.deleted` · `credential.rotated` · `credential.revealed` · `credential.mode_changed` · `credential.installed` · `credential.apply_failed` · `credential.apply_unconfirmed` · `credential.generate_failed` · `credential.scope_ambiguous` · `credential.stale_account` | everything to do with credentials, **without ever carrying a secret** ([[Credentials]]). `credential.revealed` and `credential.rotated` are **warnings**: they are not alerts, they are audit we want to see go by. `credential.created` is instance-level because a standard credential belongs to no site ([[Tags and credential scope]]) |
| `device.` | site, except `device.onboarded`, `device.onboard_failed`, `device.page_read` (instance) | `device.created` · `device.adopted` · `device.deleted` · `device.moved_in` · `device.moved_out` · `device.collector` · `device.address_domain` · `device.relocate_started` · `device.relocated` · `device.unreachable` · `device.recovered` · `device.snmp_walk` · `device.web_probed` · `device.onboarded` · `device.onboard_failed` · `device.page_read` | a device's life, from entering the fleet to a change of collector ([[Proxy]]) or of addressing domain ([[Addressing domains]]). `device.unreachable` / `device.recovered` are the **only** reachability rows, and the only source of the displayed status — a claim the code **contradicted** until this batch, since the API accepted `status` on write and let a device-page save rewrite an observation without either of these rows appearing ([[Monitoring]], § *A status is OBSERVED*). It has been true since that path was closed. A re-announcement on a proxy's reconnection only writes a row if the observation has **changed**: confirming what the database already carries logs nothing. The three instance types predate the site: an enrolment of a device or the reading of a page have no fleet to file under yet |
| `discovery.` | **instance** | `discovery.scanned` · `discovery.probed` · `discovery.serial` | what [[Discovery]] captured. Instance-level **by construction**: a candidate has no site, the site is chosen at adoption |
| `group.` | site | `group.created` · `group.deleted` | device groups, inherited ([[Profiles and groups]]) |
| `link.` | site | `link.created` · `link.deleted` | links entered between devices ([[Weathermap]]) |
| `mgmt.` | site | `mgmt.plan_changed` · `mgmt.migration_prepared` · `mgmt.migration_persisted` · `mgmt.migrated` | the management plan and the administration VLAN switchover ([[Management addressing]]). The three stages of the switchover are **three types** because they are not repaired the same way: `prepared` is not in flash (a power cut cancels it), `persisted` is, `migrated` has changed the access path |
| `netscope.` | **instance** | `netscope.domain_created` | declaration of an addressing domain — an instance object ([[Addressing domains]]) |
| `plan.` | site | `plan.updated` | saving of the site plan |
| `port.` | site | `port.up` · `port.down` · `port.flapping` · `port.flap_cleared` · `port.configured` | the four monitoring events (dedicated section below) plus the setting of a port configuration ([[Switch profiles]]). A port meshcraft does not manage produces **nothing** here, deliberately |
| `profile.` | site | `profile.created` · `profile.updated` · `profile.deleted` | port, device and switch profiles |
| `proxy.` | **instance**, except `proxy.coverage_changed` through the "single site" route | `proxy.enrolled` · `proxy.enroll_token_created` · `proxy.connected` · `proxy.disconnected` · `proxy.renamed` · `proxy.revoked` · `proxy.deleted` · `proxy.coverage_changed` · `proxy.migration_ordered` · `proxy.migration_cancelled` · `proxy.migrated` · `proxy.upgrade_offered` · `proxy.upgraded` · `proxy.upgrade_failed` · `proxy.upgrade_stalled` · `proxy.upgrade_policy` | the life cycle of a box ([[Proxy]], [[Proxy versioning and updates]]). Instance-level because a proxy belongs to no site, even when it covers some. **The exception is meaningful**: adding or removing *one* site from a coverage is a decision that belongs to *that* site and is written there; replacing the whole coverage is an instance act |
| `site.` | site | `site.created` | creation of a site |
| `snmp.` | site | `snmp.applied` · `snmp.apply_failed` | setting the SNMP identity on a device ([[SNMP identity]]) |
| `tag.` | site | `tag.assigned` · `tag.removed` · `tag.rule_changed` · `tag.rules_applied` | tags set by hand and the rules that set them ([[Tags and credential scope]]). The last two types separate "the rule changed" from "the rule was run over the fleet" — without which we would not know whether a fleet is up to date with its own rules |
| `trap.` | site | `trap.dest_applied` · `trap.dest_failed` · `trap.path_confirmed` | the trap destination written into the device, its failure, and the **proof** that the return path exists. See the requalification below: these three types do not break the rule "no traps in this log" |
| `user.` | **instance** | `user.login` | login audit. Instance-level, and it is *why* the instance log is reserved for the `global-admin` |
| `vlan.` | site | `vlan.created` · `vlan.updated` · `vlan.deleted` | a site's VLANs ([[Default VLAN]]) |
| `watch.` | site | `watch.excluded` | a device has **lost** its monitoring, with the reason. Emitted only on loss — not on every cycle — and it is the only trail that leads the operator to declare their addressing domains. A device silently removed from monitoring would pass for a meshcraft failure |

> [!note] Severities are not a property of the type
> Five types in the catalogue are written with **two** severities depending on
> context, and it is deliberate: `config.applied` (info when automatic, notice when a
> human asked), `snmp.applied`, `trap.dest_applied` (same reason), and
> `port.flap_cleared`, whose two values carry two opposite pieces of news (dedicated
> section below). The principle: **severity says how much it is worth looking at, the
> type says what it is about.** A gesture someone asked for on a production device
> deserves to be seen; the same gesture made a thousand times by background
> provisioning must not drown the log.

### Announced by the vault, no producer in the code

Nothing that follows has ever been written to the log. These rows stay because they
carry an intention dated and argued elsewhere in the vault — deleting them would lose
the decision, leaving them unmarked would have someone look for a producer that does
not exist.

| Type(s) | Announced by | Real state |
|---|---|---|
| `stp.topology_change`, `poe.fault`, `login.failed`, `auth.failure` | this page ("syslog" row) | **no source.** There is no 514/udp listener: only the groundwork is laid (`network_mode: host` and `NET_BIND_SERVICE` in the `docker-compose` that `internal/distrib` generates, commented "syslog 514/udp, traps 162/udp"). `auth.failure` in particular can come neither from a syslog we do not read nor from a trap whose catalogue we do not decode ([[Monitoring]] §2) |
| `device.rebooted` (on `sysUpTime`), `port.speed_changed` | this page | polling intentions never written. The reboot **shows** in [[Unified monitoring and pipelines]] without producing a log row |
| `device.discovered`, `neighbor.new` | [[Discovery]], this page | never written. The discovery capture is written under `discovery.scanned` / `discovery.probed` / `discovery.serial`; entry into the fleet under `device.created` then `device.adopted`. LLDP neighbours produce **no** event: `link.created` / `link.deleted` are **entered** links, not discovered ones |
| `config.rolled_back` | this page | no automatic rollback. An apply failure is written `config.apply_failed` and leaves the device where it is; it is a choice, not a gap |
| `svc.policy_changed`, `svc.generation_created`, `svc.installed`, `svc.install_failed`, `svc.retired`, `svc.compromised`, `svc.breakglass_exported` | [[Service identity and rotation]] | the subsystem is not built. **To be reconciled before writing it**: the shipped family that already covers part of this ground is `credential.*` (`installed`, `rotated`, `apply_failed`…). Two vocabularies for the same fact is exactly what makes a log unreadable — the question to settle is which of the two families absorbs the other, not which one arrives second |
| `mgmt.address_allocated`, `mgmt.address_released`, `mgmt.address_drift`, `mgmt.plan_violation`, `mgmt.exception_granted` | [[Management addressing]] | no producer. The shipped `mgmt.*` family carries only the plan (`plan_changed`) and the three stages of the administration VLAN switchover. IPAM allocates in the database without writing to the log |
| `proxy.upgrade_rolled_back` | [[Proxy versioning and updates]] | no producer. Failure is written `proxy.upgrade_failed`, getting stuck `proxy.upgrade_stalled` |

> [!warning] A type this page misnamed, not an intention
> `config.failed` never existed under that name: the real type is
> **`config.apply_failed`**. This is not a detail — an alerting rule or a query written
> on `config.failed` returns **nothing**, silently, and nothing in the product flags
> it. It is the costliest defect a type catalogue can carry: not an absence, an
> **institutionalised typo**. The `config.` prefix remains the safe way to see
> everything.

### Present in the database, no producer any more

Three types can still be read in the log of a running installation even though not a
single line of code writes them any more. A log is **append-only** — history is not
rewritten to make it look like the present:

| Type | Replaced by | Where it is explained |
|---|---|---|
| `port.link_up`, `port.link_down` | `port.up` / `port.down` | callout *The old rows*, below — with the three differences in behaviour that a filter straddling the change brings out |
| `proxy.sites_updated` | `proxy.coverage_changed` | the replacement says the right thing: what changes is not a **list of sites**, it is a **coverage**, and coverage is the proxy's rights boundary ([[Proxy]]) |

## Traps are not a source, and three `trap.*` types change nothing about that

> [!warning] Nothing is ever written *from the content* of a trap
> For a long time this page attributed `port.up` / `port.down` to traps. That is wrong,
> and not a matter of vocabulary. A v2c trap is an unauthenticated UDP datagram,
> forgeable and losable; making it a source of state would give anyone on the path the
> power to **shut a port down on screen** without touching the network. What a trap
> does, and nothing else: **bring forward** the `ifOperStatus` poll that would have
> happened anyway ([[Monitoring]] §2). The log row comes out of the poll, not out of the
> datagram — the source is therefore polling, and the trap a **latency accelerator**.
>
> This is a structural property, and it decides what is read here: a failed trap
> channel — listener not started, 162/udp already taken, device that never received a
> receiver — does not make this log **wrong**, it makes it **slower** (the polling
> cadence instead of ~1 s). There is therefore no arbitration between two sources that
> would contradict each other, and no degraded mode to describe: it is the same path,
> more slowly.

### The rule, requalified: what is excluded is `trigger`

This page stated that "*trap* is protocol vocabulary, not fleet information". The
formula was convenient and it became wrong: three `trap.*` types are written today in
the fleet log. This is not a breach of the rule, it is proof that the rule was badly
aimed — it banned a **word** where it should have banned a **category of
information**.

The right rule fits in one sentence: **the operations log never says by what mechanism
meshcraft learnt a state; it says what happened to the fleet.**

The discriminant is a test, and it is applied to every row:

> *If the trap channel did not exist at all, would this row still have something to
> describe?*

- **`trigger`** (`"poll"` / `"trap"`): no. The port transition would have been written
  anyway, at the polling cadence, with the same type, the same severity and the same
  sentence. The field therefore does not describe the fleet, it describes **meshcraft**
  — precisely what the operator does not need to know and what whoever is diagnosing
  meshcraft cannot do without. It stays in the **application** log, where it is the only
  way to observe that a fleet really receives its traps: without it, a fleet where no
  device emits would be indistinguishable from a reactive fleet — the same events,
  thirty seconds later, and nobody would ever notice that 162/udp serves no purpose
  ([[Monitoring]] §*Observability of the fast path*);
- **`trap.dest_applied` / `trap.dest_failed`**: yes, but the other way round — these
  rows would not exist, because the **gesture** they describe would not take place.
  They do not speak of a learning mechanism: they say that **a configuration was
  written into a production device**, or that it failed. It is the same kind of fact as
  `snmp.applied` or `port.configured`, and it is logged for the same reason: someone
  asked for it, or something did it on their behalf, and the device is no longer in the
  state it was left in;
- **`trap.path_confirmed`**: yes, and it is the most interesting case. This row is the
  only place from which the operator learns that the **claim** ("the address is
  written") has become **proof** ("a trap arrived"). Without it, the state "address
  written, never confirmed" — exactly the one in which you go looking for a firewall or
  an ACL on the management VLAN — would be invisible. It is not protocol vocabulary, it
  is an **infrastructure fact** that nothing else provides.

What the requalification preserves, and what was the real content of the old formula:
**none of these three types is a device state derived from the content of a trap.**
`trap.path_confirmed` asserts nothing more than "a datagram attributable to this device
reached the address we wrote". It does not say a port went down, it does not say a
device rebooted — a trap still cannot do that.

Two properties of these rows are worth knowing, because they are easily misread
otherwise:

- **`trap.path_confirmed` is written at most once per destination set.** The bound is
  taken **in the database**, in the `where` of the `update` (`store.ConfirmTrapPath`),
  and not by a read followed by a test: the proxy resets its counter on every tunnel
  reconnection, so counting on the sender's parsimony would produce a "return path
  established" row per proxy restart, **across the whole fleet at once**
  ([[Controller-proxy protocol]] §`trap.seen`);
- **a trap that arrived at an address other than the one written confirms nothing**,
  and produces no fleet row: it goes to the application log as a warning, with its
  **two** possible causes named — a receiver meshcraft did not set, or address
  translation in front of the proxy. They are repaired in opposite ways, and naming
  only one would send someone off to dismantle the wrong half of the installation.

> [!important] Events without a site need their own view
> A proxy's life cycle attaches to no site — even when the proxy carries a coverage,
> enrolling or renaming it is an instance act ([[Proxy]]). A [[Discovery]] candidate
> has no site either, nor does a `user.login`: these events have a null `site_id`,
> so **no site log can show them**. They are exposed by a separate instance log
> (`GET /api/events`), reserved for the `global-admin` since it carries the login audit
> and the instance credentials ([[RBAC]]). An event written but shown nowhere would be
> worse than no event at all.

## The four port monitoring events, and who computes them

The port monitoring batch is the one where the proxy / controller split shows best:
**the proxy establishes the fact, the controller correlates it**. Both halves write to
this log, but they do not answer the same question.

| Event | Computed by | How | Severity |
|---|---|---|---|
| `port.up` / `port.down` | the **proxy** | diff of the captured `ifOperStatus` against its last observation, at every poll, whatever its trigger | **info** |
| `port.flapping` | the **controller** | correlation of the transitions reported by the proxies: 5 in 5 min, deduplicated first | **warning** |
| `port.flap_cleared` | the **controller** | end of flapping, observed by the **absence** of transitions for 3 min | notice **or** warning, depending on the final state |

Two consequences the table does not say on its own:

- **port events work without traps**, at the polling cadence. The trap does not create
  them, it brings them forward;
- anti-flap sits in the controller **because** its windows are a product policy: setting
  them in the proxy would mean redeploying a binary into every customer network to go
  from five transitions to six ([[Monitoring]]).

### Severities do not follow intuition

| Event | Severity | Why |
|---|---|---|
| `port.up` / `port.down` | **info** | across two thousand sockets, the workstation unplugged when leaving in the evening counts in the hundreds every day. An alert colour put on the ordinary stops being looked at, and the real incident becomes the six-hundredth row of the day |
| `port.flapping` | **warning** | five round trips in five minutes on the same socket are no longer a human gesture: it is hardware — dubious crimping, optics at end of life, duplex mismatch, loop. It is exactly what single transitions buried |
| `port.flap_cleared`, stabilised **up** | notice | the link came back and it holds. It is read, it is not watched |
| `port.flap_cleared`, stabilised **down** | **warning** | the flapping stopped for lack of a link. The port is silent, not repaired: only the noise has stopped, the failure is intact |

What gives the batch its interest is this reversal: the information is not in the
transition, it is in its **repetition** — and in the way it stops.

### Why a single type carries two opposite situations

`port.flap_cleared` is the same type for the good and the bad news. It is not a saving
on vocabulary, it is what makes both readable.

A flapping episode is a **story** opened by `port.flapping`: the operator's question is
"is it still flapping?", and the answer must be read in the same place whatever it is.
With two types — a `port.repaired`, a `port.dead` — every reader (screen, query,
alerting rule) would have to know **both** names to know that a flapping episode is
over; one that knew only one would see endless flapping, and that is the worst possible
defect for a log from which one expects the closure of an incident.

The two useful readings then come out of the same rows, because the log filter combines
a **type prefix** and a **severity floor** ([[Web interface]]):

- filtering on `port.flap` returns the complete cycle — start, end, and both outcomes.
  It is the view of someone following a port or investigating an incident;
- filtering "warnings and above" lets through the start of flapping **and** the end
  down, and drops the end up. It is the view of someone triaging: only the situations
  where there is still something to do remain.

It is therefore the **severity** that separates repaired from dead, not the type.
Filing the good news under a distinct type would have made it invisible to someone
following a port, and visible to nobody else.

> [!note] The old rows do not carry these types, and they are not rewritten
> Events already recorded speak `port.link_up` / `port.link_down`: it is an **earlier
> logging path**, of which not a single line of code remains — `grep` on these two
> strings returns nothing in `internal/`. It derived from the same polling, but the diff
> was done in the controller at ingestion of telemetry samples, hence at the buffer's
> pace (flushed every 10 s) and not on the real-time event channel. A log is
> **append-only**: rewriting its old rows to make them look like the new ones would
> falsify what was observed, and make the timestamp on which the rest of this page
> depends lie.
>
> Three visible differences, for anyone wondering why their old rows do not behave like
> the new ones:
>
> - **the severities do not match**: the old path recorded a link down as a *warning*
>   and a link restored as a *notice*, whereas `port.down` and `port.up` are *info*. A
>   "warnings and above" filter over a period straddling the change therefore mixes
>   perfectly ordinary old rows with real flapping;
> - **the scope was not the same**: the old path stayed silent about ports meshcraft
>   does not manage ("free" mode), the new one logs the transitions of the observed
>   faceplate;
> - **the type filter is a prefix**: `port.` brings up `port.link_down` as well as
>   `port.down`, whereas a search on `port.down` sees only the after. That is why these
>   two types **must** appear in the catalogue: they show up under the most natural
>   filter one can type, and a catalogue that kept quiet about them would suggest an
>   anomaly. Rows from the new path also carry `actor = "surveillance"`, which is the
>   only safe way to tell them apart.

## Schema

```
events(
  id            bigint (identity),
  ts            timestamptz default now(),  -- the CONTROLLER's clock, cf. below
  site_id       uuid null,          -- null = INSTANCE event (cf. above)
  device_id     uuid null,
  port_name     text,               -- '' when the faceplate did not recognise the port
  type          text,               -- taxonomy above
  severity      smallint,           -- 0 debug … 5 critical
  message       text,               -- readable, already localisable
  actor         text,               -- who writes: "surveillance", a user…
  payload       jsonb               -- diff, detail of a state, context
)
```

Indexes: `events_site_ts_idx (site_id, ts desc)`, `events_device_ts_idx (device_id,
ts desc)`, `events_type_ts_idx (type, ts desc)`. Details in
[[Database schema]].

Three points where the table is simpler than what this page described, and it is
accepted:

- **no partitioning.** Monthly partitions with purge by `DROP` remain the target — it is
  the only way to purge twelve months of log without a `DELETE` that rewrites the table
  — but they arrive with monitoring's real volume, not before. Partitioning a table that
  fits in an index is migration complexity paid in advance. The table created in
  `0003_proxies_events` is a simple table, and its own comment says so;
- **no `dedup_key` or `count`.** Correlation lives **in memory** in the controller
  ([[Monitoring]]): it therefore has nothing to retrieve from the database, and the
  number of transitions of a flapping episode is carried by the **sentence** of
  `port.flapping` (and by that of `port.flap_cleared`, with the duration). The price to
  know: this counter cannot be queried in SQL — you read "7 state changes", you do not
  average the quarter's flapping. The day we want that, it will be a column, not a
  counter incremented on an existing row;
- **`ts` is not a parameter.** `store.InsertEvent` does not write it: the column takes
  its `default now()`, hence PostgreSQL's clock. It is not an oversight, it is the same
  decision as for `trap_dest_at` and `trap_seen_at` — see the rule below.

## Rules

- **Append-only**: an event is never edited — neither to adjust a type nor to increment a counter. Flapping therefore does not aggregate *by update*: it aggregates **by silence**, writing one `port.flapping` row in place of the dozens of transitions we refrain from logging, then a closing row. Persisting the correlation state would cost a write per flapping episode at the very moment the fleet is unwell.
- **A single clock, the controller's.** For a long time this page promised a "source timestamp", events replayed by a proxy being inserted at their real date. That is **abandoned**, and the opposite is now guaranteed by the signature: `InsertEvent` accepts no date, just as `ConfirmTrapPath` no longer accepts one. The reason was measured on real hardware — a proxy deployed at a customer's, without NTP, was **eleven seconds** behind, which was enough to make a proof appear before what it proves ([[Controller-proxy protocol]], §*`Event.TS` is a DIAGNOSTIC fact*). A chronological thread mixing two clocks is no more honest than a shifted thread: it is **unorderable**. The date set by a proxy keeps exactly one use — measuring the drift itself, as a warning in the application log, with asymmetric thresholds (5 s behind, 1 s ahead: no transport makes time go backwards).
- **Two levels of reading**: the log (normalised events, stable taxonomy) and — *when syslog exists* — the raw stream, with short retention on a separate diagnostic screen.
- **Retention**: normalised events 12 months by default (configurable per site); 7 days for raw syslog, the day there is some.
- **Real time**: each inserted event is published on the WebSocket hub (`site:{id}`) → toast/badge in the UI, update of [[Port rendering]] without refresh. It is `emit` itself that notifies, not only writes that went through the API: without that, everything that logs without writing — monitoring, background provisioning — would only appear at the next manual refresh.
- **The log never blocks the action**: an insertion error is traced with `slog.Error` and the business action succeeds anyway. The log is an observer, not a lock — the reverse would make a configuration apply fail because we could not tell its story.
- **Audit included**: human actions (login, approved apply, modified credential) are events like any other, `actor` = the author's address, filled by default from the session. A single place to look for "who did what when".

## Decided / shipped / open

| Question | Status |
|---|---|
| Catalogue arranged by prefix family, explicit scope and `actor` | **shipped** — 88 types, 19 families, all checked by `grep` in `internal/` |
| Separation "emitted by the code" / "vault intention" | **shipped**, and it is the property not to lose in the next batch |
| Filter by type prefix + severity floor | **shipped** — `store.EventFilter`, `type like '<prefix>%'` |
| Separate instance log (null `site_id`), reserved for the `global-admin` | **shipped** — `GET /api/events` |
| Single timestamp, the controller's clock | **shipped** — no date parameter in `InsertEvent` |
| Reconciliation of `svc.*` / `credential.*` | **open**, and blocking for [[Service identity and rotation]]: two families for the same fact |
| Syslog events (`stp.*`, `poe.*`, `auth.*`) | **open** — subordinate to a 514/udp listener that does not exist |
| Monthly partitioning, `DROP PARTITION` for purging | **decided, not built** — awaits real volume |
| `count` / `dedup_key` in the database for flapping | **not planned**: correlation is in memory, the count is in the sentence |
| Rewriting historical types (`port.link_*`, `proxy.sites_updated`) | **refused, final** — an append-only log is not rewritten |
