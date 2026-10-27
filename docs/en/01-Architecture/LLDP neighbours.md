---
tags: [architecture, monitoring, topology, lldp]
---

# LLDP neighbours

Extends [[Monitoring]] and [[Unified monitoring and pipelines]] on a question no page
covered: **how meshcraft learns what is plugged in opposite each port**, and what it
is entitled to conclude from it.

Until 29 July 2026 the answer was "it does not learn it". Connectors *declared* a
capability and an `lldp` channel — RouterOS and Instant On — that **nothing read**;
the `device_links` table only carried links **entered by hand**, and the "Neighbour"
column of the Ports page was derived from them. This batch ships the collection, its
storage, the derived links and their display.

> [!important] The sentence to remember before everything else
> **An LLDP neighbour is a dated, refutable FINDING; a `device_links` link is an
> INTENT.** Confusing them would make a hand-entered link disappear because a switch
> was unplugged for ten seconds — or, the other way round, would keep alive a cable
> nobody sees any more. That is why nothing is written to `device_links`, and why the
> two are drawn differently on the [[Weathermap]].

## 1. The survey, before the code

Three devices of the reference fleet, walked **read-only** through
`proxy-lab` (`POST /api/devices/{id}/snmp-walk`), on **29 July 2026**. The
LLDP-MIB is under `1.0.8802` — an **IEEE** MIB adopted as is, not an enterprise
branch.

| | `lab-crs326` (RouterOS 7.21beta3) | `lab-css326` (SwOS 2.17) | `sw-labo-1930` (InstantOn 3.4.0.0) |
|---|---|---|---|
| `1.0.8802` serves something | **yes** | **NO — zero rows, no error** | **yes** |
| `lldpLocChassisId` | `B8:69:F4:5B:A0:D2` — **as text** | — | six raw bytes → `0x02005e1061a7` |
| `lldpLocSysName` | `lan-r4` | — | `CN0EXAMPL1` (its serial number) |
| `lldpLocPortTable` | **2 entries** (out of 29 interfaces) | — | **52 entries** |
| `lldpLocPortIdSubtype` | `interfaceName(5)` everywhere | — | `5` except **ports 50 and 52: `macAddress(3)`** |
| `lldpRemChassisId` (col. 5) | yes, **as text** | — | yes, **raw bytes** |
| `lldpRemPortId` (col. 7) | yes | — | yes |
| `lldpRemPortDesc` (col. 8) | **ABSENT — empty walk** | — | yes |
| `lldpRemSysName` / `SysDesc` (9, 10) | yes | — | yes |
| neighbours seen | **5, of which 3 on a single port** | — | 3 |
| `lldpStatistics` | not surveyed | — | yes (inserts 11, deletes 8, drops 0, ageouts 4) |

### 1.1 SwOS serves NOTHING, and that is final

The walk of the whole of `1.0.8802` on `lab-css326` returns **zero rows, no error** — so
not a timeout, not a community refusal: the agent answers and has nothing under this
branch. Same for `1.0.8802.1.1.2.1.3` and `1.0.8802.1.1.2.1.4.1.1` queried separately.

It is the **confirmation by measurement** of what the `swos` connector already declared
by enumerating its web interface (`MetricUnavailable{Metric: "lldp"}`): this firmware only
knows MNDP, MikroTik's proprietary protocol. The design consequence was written in the
connector before being shipped — *"at best a neighbour will see it, it will see
nobody"* — and it is **the reason why a link seen from one side only is the NORMAL
case** and not an anomaly.

### 1.2 The same chassis, served in two encodings

The survey fact with the heaviest consequences, and it cannot be guessed:

```
chassis B8:69:F4:5B:A0:D2  (the CRS326)
  seen by itself   lldpLocChassisId  =  "B8:69:F4:5B:A0:D2"   17 PRINTABLE bytes
  seen by the 1930 lldpRemChassisId  =  b8 69 f4 5b a0 d2        six RAW bytes
```

The MIB says `OctetString`: a vendor is perfectly entitled to put readable text in it.
Comparing the values as they are would therefore **never** match these two findings.
Hence `api.normalizeChassisID`, and two corollaries:

- the conversion must be **lossless on both sides** — `json.Marshal` silently replaces
  any non-UTF-8 byte with U+FFFD, and a MAC transformed that way would be
  irreconcilable *without any error occurring*. That is why the `0x`-prefixed
  hexadecimal conversion of `cmd/proxy/rpc.go` **moved** into `snmppoll.OctetText`:
  two callers, one implementation;
- normalisation is **conditional**. An `lldpChassisId` is not necessarily a MAC (the
  MIB enumerates seven subtypes, including `local(7)`, a free string). Blindly reducing
  to hexadecimal digits would turn `salle-b-baie-12` into `bab12`, and two devices whose
  names only share their letters a-f would be declared identical. The MAC reduction
  therefore applies **only if the result has that shape**.

### 1.3 The `lldpRemTable` index: the central trap

```
1.0.8802.1.1.2.1.4.1.1.7 . 0 . 26 . 8
└──── column (portId) ───┘  │    │    └── lldpRemIndex
                            │    └─────── lldpRemLocalPortNum   ← THE port
                            └──────────── lldpRemTimeMark
```

**Three arcs, and the first one is not data.** `lldpRemTimeMark` is a TimeFilter
(RFC 2021): a synthetic index that lets you ask only for rows modified since a given
instant. An ordinary walk therefore sees it at **0** — surveyed that way on both
platforms, on **all** rows.

Two ways of getting it wrong, and they do not show:

- **reading two arcs** (what `suffixIndex`, the decoding of the rest of `snmppoll`,
  does) would attribute all the fleet's neighbours to local port **0**. `decodeRemIndex`
  therefore refuses anything that does not have exactly three arcs, and refuses a zero
  local port — that is precisely the value a two-arc read would produce;
- **keeping only the last arc** — `lldpRemIndex` — would overwrite one another the
  neighbours of different ports carrying the same remote index. That is why LLDP
  collection walks with **full OID names** (`snmppoll.TableClient`) and does not go
  through `pollClient`, whose table is indexed by the last arc.

An agent is entitled to return **several timestamps** for the same row. None of those
in the fleet does — **deduced** from the survey, all rows being at 0 — but the MIB
allows it, and an agent doing so would duplicate each neighbour. Assembly therefore
keeps the row with the most **recent** timestamp, and **resets** it along the way:
mixing two timestamps would produce a composite neighbour, whose name would come from
one finding and its port from another.

### 1.4 `lldpLocPortNum` is NOT `ifIndex`, even when it looks like it

Survey:

```
CRS326   lldpLocPortNum 26 = "sfp-sfpplus2"   ifDescr 26 = "sfp-sfpplus2"   ✓ matches
1930     lldpLocPortNum 50 = "50"             ifDescr 50 = "50"             ✓ matches
```

The two match on both platforms. **We still do not rely on it**, and it is the same
lesson `snmppoll.physical` already cost: the index bound worked on the 1930 "because
this range numbers its ports from 1 to 52 — that is a vendor coincidence, not a
property of the protocol". The MIB makes `lldpLocPortNum` an index specific to the LLDP
subsystem. Resolution therefore **always** goes through `lldpLocPortTable`.

### 1.5 The local port name: no column suffices on its own

The survey settles it, and it settles it in **opposite** directions:

| | `lldpLocPortIdSubtype` | `lldpLocPortId` | `lldpLocPortDesc` | what to take |
|---|---|---|---|---|
| CRS326, port 26 | `interfaceName(5)` | `sfp-sfpplus2` | `bridge/sfp-sfpplus2` | **the portId** |
| 1930, port 50 | `macAddress(3)` | `0x02005e1061d9` | `50` | **the portDesc** |

Taking `portDesc` everywhere would break the MikroTik: `bridge/sfp-sfpplus2` is not a
port of its faceplate — it is the name of a **bridge membership** — and `resolve` would
discard it, rightly. Taking `portId` everywhere would break the 1930 on its **only two
ports that see something**. Hence the rule: **the subtype decides, `portDesc` is the
fallback** (`snmppoll.localPortName`).

### 1.6 The chassis-id a device SENDS is not the one it DECLARES

The most surprising fact of the survey, and without it the fleet's only bilateral link
would appear unilateral:

```
the 1930 DECLARES      lldpLocChassisId       = 0x02005e1061a7
the CRS326 RECORDED    lldpRemChassisId       = 02:00:5e:10:61:D9     ← that is not a7!
                       …and "…61:d9" is the lldpLocPortId of the 1930's port 50.
```

In other words, this firmware announces in its frames the **MAC of the sending port**
as chassis identifier. The CRS326 therefore records a neighbour whose identifier appears
**nowhere** in the 1930's declared identity.

That is what justifies the `lldp_self` table and its `port_ids` column: the controller
indexes, for each device, its chassis **and** those of its `lldpLocPortId`s that have
the shape of a MAC. The corresponding recognition path is named separately
(`peer_via: "port_id"`) rather than folded into "recognised by its chassis", because an
operator reading that phrase understands they are looking at a firmware that announces
itself oddly.

### 1.7 Three neighbours on a single port — and what it forbids concluding

Surveyed on `lab-crs326`, local port 26 (`sfp-sfpplus2`):

| `lldpRemIndex` | chassis | remote port | system name |
|---|---|---|---|
| 8 | `04:F4:1C:1D:74:5F` | `bridge/sfp-sfpplus1` | `RB5009UG` |
| 9 | `D4:01:C3:AB:32:F6` | `bridge/sfp-sfpplus2` | `CRS305` |
| 11 | `02:00:5e:10:61:D9` | `50` | `CN0EXAMPL1` (the 1930) |

**An LLDP neighbour is therefore not proof of a direct cable.** The explanation is
**deduced** — it was not measured on the path — but it is the only one consistent with
the rest of the survey: an intermediate switch that **does not consume** LLDP
**floods** its frames instead of absorbing them, and that is exactly the behaviour of a
CSS326 running SwOS (§1.1).

Three design consequences, all shipped:

1. the `lldp_neighbors` key carries the **neighbour** and not only the port
   (`device_id, local_port, chassis_id, port_id`). A `(device_id, local_port)` key would
   have overwritten two findings out of three, and the screen would have shown a
   neighbour **picked at random**;
2. a multi-neighbour port **confirms no reciprocity**. Saying "this port faces that one"
   when it sees three would be choosing at random: the three findings stay
   **unilateral** links, which is exactly what they are;
3. the tooltip of such a port **states** the number of neighbours and what it means. It
   is the only place where an operator can learn that their port does not lead
   directly to what they think.

### 1.8 "No neighbour" does not mean "nothing is plugged in"

Three causes of silence, measured or surveyed, which are fixed **differently**:

| Cause | Survey | What we do with it |
|---|---|---|
| the **connector declares** that the platform does not speak LLDP | CSS326: walk of `1.0.8802` **empty**, and `swos` announces it with its proof | `silent: platform_unsupported`, **with the connector's reason shown as is**. Cannot be fixed |
| the device was **queried** and returned nothing | — (no case in the fleet) | `lldp_self.served = false` → `silent: not_served`. A connector declaration to correct, or LLDP disabled on the device |
| no snapshot has arrived yet | no `lldp_self` row | `silent: never_polled`. A collector is missing, or the first cycle has not run: that can be fixed |
| LLDP only runs on some interfaces | CRS326: **2** entries in `lldpLocPortTable` for **29** interfaces | nothing to flag per port — but the screen must not conclude from the absence of a tooltip that there is no cable |

> [!warning] The first two causes nearly got confused, and the misreading was serious
> `platform_unsupported` was added by rereading the actual path rather than assuming
> it. `optionalIntervalFor` only turns collection on if the connector declares an SNMP
> `lldp` channel: a CSS326 is therefore **never queried**, and it would **never** have
> an `lldp_self` row. With only two causes, it would have shown up as `never_polled` —
> "it is missing a collector" — that is, an operator sent looking for a setting that
> does not exist, on the only device of the fleet for which we know for certain there
> is nothing to set.
>
> The distinction is read from the **connector's declaration** (`WhyUnavailable("lldp")`),
> and it comes **before** the absence of a snapshot in `buildLLDPView`. The order is the
> message.

The last one is the one that has **no** marker on screen, and it is an accepted limit:
the LLDP-MIB does not say "LLDP is off on this port", it merely does not list the port.
So for a given port we cannot distinguish "LLDP is running and nobody is opposite" from
"LLDP is not running here". The "LLDP?" marker on the Devices page only covers the first
three causes, those that concern the whole device.

## 2. Through which channel, and why not the other three

This is the batch's architecture decision, and it deserves to be written down because
three existing paths looked suitable.

| Path | What it carries | Why it does not fit |
|---|---|---|
| metrics batch (`POST /api/proxy/metrics`) | **labelled floats** | a neighbour is a table of strings. Forcing it in would require encoding each system name in a series label — hence **one time series per cable**, which VictoriaMetrics would keep forever, including after unplugging. And "since when has this neighbour been seen" would read from the shape of a flat curve |
| tunnel event channel | **transitions**, never a repeated current state | a periodic snapshot would violate its invariant, and its 256-entry queue is sized for rare switches. Manufacturing `neighbor.new` / `neighbor.gone` would make every event loss **permanent** — the defect `resync` cost dearly to repair for reachability |
| `ask` (`tunnel.OpAsk`) | a **one-off question** | [[Controller-proxy protocol]] says it in black and white: *"`ask` is not a collection path"*. It is initiated by the controller and has no cadence |

**Chosen: a separate HTTP route, `POST /api/proxy/lldp`**, for the exact reason that
already took metrics out of the tunnel — it is periodic volume, it authenticates with the
`(proxy_id, secret)` pair, and the controller's **scope check** already applies to it.

> [!note] And why there is NO buffer
> This is the most useful difference between this path and the metrics one. A lost
> measurement point is lost forever: it samples an instant that will not come back,
> hence `metricbuf`, its 150,000 entries and its 57 MiB worst case. A **lost
> neighbourhood snapshot is re-sent a minute later, fresher than the one we would have
> replayed** — a device's LLDP table changes at the scale of an unplugged cable, not of
> the second.
>
> Buffering would therefore have cost memory to push back **stale** information, and on
> top of that we would have had to decide what to do with two snapshots of the same
> device in the queue. The price is named: a ten-minute tunnel outage leaves ten minutes
> of lagging `last_seen`. It is **visible** (the date is displayed) and self-repairing at
> the first cycle that goes through.

### 2.1 The scope check

This route **writes inventory**: what it stores in the database ends up drawn on a
weathermap. The snapshot's `device_id` is chosen by the **proxy**, like the `DeviceID` of
a tunnel event or the `device` label of a sample.

The authority required is that of **SNMP collection** (`authorityPoll`, see
`internal/api/proxyscope.go`) and not of ping, for the reason of `applyPortTelemetry`:
these findings come out of polling, and only a device's declared collector polls it.

An accepted difference with `/api/proxy/metrics`, which answers **200** while dropping
out-of-scope samples: here the refusal is a **plain 403**. There, a batch mixes several
devices, and refusing the whole would lose the legitimate measurements and then replay
the same batch indefinitely; here one request = **one** device, there is nothing
legitimate to save, and the proxy has no buffer to unblock. The plain refusal is
therefore free, and it has a value of its own — it shows up in the **proxy's** log,
which is the misconfigured machine.

### 2.2 The cadence, and where it comes from

**60 s**, and the reasoning starts from the **source**:
`/ip/neighbor/discovery-settings` announces `discover-interval: 30s` on RouterOS — so
the device only rebuilds its own table at that pace. It is the best-founded floor in the
repository: neither negotiated nor precautionary, **imposed by how the source works**.
Polling faster rereads a table the device has not touched.

Why 60 and not 30, that is, why we do not stick to the floor: the operational value of
this data is not measured in seconds. A neighbour changes when someone unplugs a cable —
a physical gesture — and the link drop that comes with it is already reported in
**250 ms** by the trap channel. Doubling the floor halves the load of **seven walks** per
device, for a loss of reactivity that nothing observes.

Like `health` and `optical_power`, collection is **only turned on if the connector
declares an SNMP `lldp` channel** (`optionalIntervalFor`) — and the reason is stronger
here: the LLDP-MIB is optional, and an entire platform of the fleet does not serve a
single row of it. Turning it on everywhere would have meant seven silent walks per minute
per CSS326, indefinitely, for an answer we already know.

> [!warning] The RouterOS connector declared `lldp` on the `web` channel ONLY
> So `BestOfKind("snmp", "lldp")` returned nothing, and collection would have stayed off
> on this platform even though its agent serves the MIB richly. An `snmp` channel for
> `lldp` was added to it, **dated and measured** (§1). It is the same defect as the one
> fixed for `if_state`: a channel does not lend its floor to another, and it does not
> lend its metric either. The `web` channel remains declared alongside — it really
> exists, via `/ip/neighbor` — it is simply used by nobody, periodic collection running
> on the proxy which only speaks SNMP.

### 2.3 Seven narrow walks rather than one walk of the root

A walk of `1.0.8802` would return everything in one pass, and would also return what we
have no use for: `lldpStatistics` (ten rows measured on the 1930), `lldpConfiguration`
with one row per port and per TLV, and the two capability columns of each neighbour. On a
48-port device, `lldpPortConfigTable` alone is four columns × 52 ports — **more rows
than everything else combined**, for values no screen shows.

Ten narrow roots cost nine more round trips per cycle and per device, every sixty
seconds. It is the right trade: the LLDP-MIB is wide and hollow, and the useful columns
are scattered between two subtrees.

## 3. Storage: two tables, no derivation

Migration `0028_voisinage_lldp.sql`.

```
lldp_self       (device_id pk, site_id, chassis_id, sys_name, port_ids[], served, seen_at)
lldp_neighbors  (device_id, site_id, local_port, local_port_num,
                 chassis_id, port_id, port_desc, sys_name, sys_desc,
                 first_seen, last_seen,
                 pk (device_id, local_port, chassis_id, port_id))
```

### 3.1 Why not `device_links`

Three reasons, any one of which would suffice:

1. **an intent and a finding are not refuted the same way** (the opening box);
2. both ends of `device_links` are `not null` with a **foreign key** to `devices`. A
   neighbour outside the inventory — the provider's router, an access point, the S3900
   surveyed in the fleet — is **structurally inexpressible** there, whereas it is the
   most frequent case in operation;
3. **a port can carry several neighbours** (§1.7), which a topology link does not
   allow.

### 3.2 Gap with what the vault had sketched

[[Database schema]] sketched an `lldp_links` table carrying `confirmed bool`. That is
**not** what is shipped:

- the name chosen is `lldp_neighbors`, because the table carries **directed findings**
  ("this port saw this") and not links. The link is what is **derived** from them;
- `confirmed` — "both sides see each other" — is a **derivation from two findings**, not
  a fact. Stored, it goes stale as soon as one of the two sides stops seeing the other,
  and one would then have to decide **who**, A's next snapshot or B's, has the right to
  erase it. It is computed on read by a pure function (`api.deriveLLDPLinks`), and
  nothing can make it lie;
- likewise, `peer_device` / `peer_port` **are not stored**. Matching depends on the
  inventory, which moves: the day the operator adopts the device opposite, all the
  findings that mentioned it must recognise it **without** rewriting a table.

### 3.3 A snapshot REPLACES, and `first_seen` survives

One transaction per snapshot: `upsert` of `lldp_self`, deletion of neighbours **absent**
from the snapshot, `upsert` of those present. Complete replacement is what makes the
finding refutable — it is the rule of `tunnel.WatchConfig`, and here a delta would leave
an unplugged neighbour displayed indefinitely.

The `upsert` **does not touch** `first_seen`: that is half the operational value of this
table, since it is what answers "since when has this cable been there". A neighbour that
disappears was **deleted** at the previous snapshot: if it comes back, it starts from
zero — the cable moved, the age of the previous finding no longer says anything.

Both timestamps are the **controller's** (`now()` in the database), never the proxy's.
It is the rule the traps batch cost dearly to establish
([[Controller-proxy protocol]], *"Event.TS is a diagnostic fact"*): the gap between the
two clocks reached **eleven seconds**, and here the consequence would be directly
visible — "seen for 3 min" shown when hovering a port.

## 4. Recognising a neighbour: three paths, in this order

`api.identifyPeer`, pure and tested. **The order is the decision**: the first that
answers wins, and the following ones are not tried.

| Path | On what | Strength |
|---|---|---|
| `chassis` | the announced chassis = the `lldpLocChassisId` of a fleet device | **strong** — a MAC is unique in the world |
| `port_id` | the announced chassis = one of the `lldpLocPortId`s of a fleet device | **strong**, and it is the path that saves the fleet's only bilateral link (§1.6) |
| `sys_name` | the announced system name = the `lldpLocSysName` of a fleet device | **weak** — a name is a string an operator typed, and two devices can share it |

The path used goes all the way to the screen (`peer_via`): a link drawn on the strength
of a name deserves to be read as less certain, and presenting the two as equivalent
would be the kind of silence that costs an hour.

> [!important] It is the ANNOUNCED name that is compared, never the meshcraft name
> The survey is unambiguous: `lab-crs326` announces itself as **`lan-r4`**, and
> `sw-labo-1930` announces itself by its serial number **`CN0EXAMPL1`**. Comparing with
> the meshcraft name would have matched **no** device of the reference fleet.

Two refusals, both on the side of caution:

- an **ambiguous token is neutralised**. Two devices announcing the same system name —
  a model straight out of the factory, two switches never renamed — must not be
  attributed each other's neighbours. Same doctrine as `facade.norm`: matching at random
  is worse than not matching;
- an `lldpLocPortId` is only indexed if it has **the shape of a MAC**. An identifier
  worth `45` or `ether3` identifies nothing beyond its device, and indexing it would make
  any neighbour whose chassis is called `45` be recognised as the 1930.

### 4.1 The remote port reuses `resolve`

`facade.resolve` (`internal/api/proxymetrics_handlers.go`) is the product's **only** way
of matching an interface label to a faceplate port, and it covers the three forms
surveyed in the fleet: exact equality (`sfp-sfpplus2`), normalised equality (`SFP2`),
faceplate prefix + label (`50` → `GE50`). Writing a second version would have produced
two ways of naming the same port, hence a link whose two halves do not recognise each
other.

Two columns are tried, `PortID` then `PortDesc`, because neither is served everywhere
(§1). A label that cannot be attached returns `""`, and the link remains **seen from one
side**: it is an **accepted under-statement**; inventing a name would draw a cable on the
wrong port of the right faceplate.

## 5. Deriving a link from one or two findings

`api.deriveLLDPLinks`, pure.

**The reciprocity required is EXACT**: two findings are matched when the end that one
*claims* is the end the other *occupies*, in both directions. The broader rule — "A and B
see each other, so it is the same link" — would have caught a few more cases and been
wrong on the one that matters: **two devices connected by two cables**, which every
redundant uplink in the real world presents. It would then have mutually confirmed the
two wrong halves.

Three outcomes, and the first is the most frequent:

| Outcome | What it is | Drawn on the weathermap |
|---|---|---|
| **seen from one side** | a single finding, remote end recognised | **yes**, short faded dashes |
| **seen from both sides** | two exact reciprocal findings | **yes**, light solid line |
| **neighbour outside the inventory** | `b_device` empty | **no** — no second end to join. Listed in the diagnostic panel |

Dates merge when the link is bilateral: `first_seen` is the **oldest** of the two
findings (the link has existed since the first side saw it) and `last_seen` the **most
recent** (it was confirmed at the last snapshot).

Nothing in this function compares a date with `time.Now()`: freshness is judged **on
screen**, with the viewer's clock, because it is the only one that makes "seen 3 minutes
ago" true for them.

### 5.1 The finding that a fresher finding refutes

`api.markRefutedObservations`, pure, between recognition and derivation.

**What makes a finding forgotten, today, is one thing only**: a fresh snapshot of the
**same** device. There is **no** ageing, no TTL, no purge — `store.ReplaceLLDP` deletes
the rows of the device that is speaking, and nothing else (§3.3). Two ordinary situations
therefore freeze rows in place: a device the proxy no longer reaches — `cmd/proxy/lldp.go`
pushes **nothing** rather than an empty snapshot, on purpose, so as not to erase a
neighbourhood we merely failed to read — and a device in `source='cli'`, the FS S3400,
whose rows are only refreshed at read-state. Re-plug a cable on that side: the other
side is polled every thirty seconds and announces the **new** neighbour, while the frozen
side keeps claiming the old one. `deriveLLDPLinks` drew both, and the older of the two is
a cable that no longer exists.

**The rule**: a finding dies when the end it claims is occupied by **someone else**,
according to the device that **owns that port**, in a **strictly fresher** finding. A
physical port carries one cable: if A/p1 now says it faces C/p2, then B's frozen "I face
A/p1" talks about a port that answers to another, and it is B that is lagging. The
refuted finding **stays listed** — the device really reported it, and the Ports panel
must keep showing it — it simply no longer produces an edge.

**Four refusals, and what each one avoids**:

| Refusal | What it avoids |
|---|---|
| **silence** never refutes | a remote end with no row at all proves nothing: SwOS sees nobody and the S3400 does not have a single row (§1.1, §4). Treating silence as refutation would erase **all** links seen from one side, that is, the ordinary case |
| an **unrecognised remote neighbour** never refutes | the row occupying that remote port may **be** the claimant under a chassis spelling we failed to match — the 1930 announcing its port's MAC (§1.6) proves that this form exists |
| an **unresolved remote port** never refutes | `resolvePeerPort` returns `""` often and by construction (§4.1); comparing at port granularity then makes no sense |
| a witness of **equal or older age** never refutes | two sides polled in the same cycle that contradict each other are a broadcast domain (§1.7) or a real ambiguity: deciding would be drawing lots |

This last refusal is also the honest answer to "which side is authoritative?": **freshness**,
and **on a tie, nobody** — both findings stay drawn, each from one side.

**The function stays clockless**, and the property stated just above holds: it compares
two **rows with each other**, never a row with `time.Now()`. Both `last_seen` values are
set by the PostgreSQL server's `now()` — everything goes through `store.ReplaceLLDP`, SNMP
path and CLI path alike — so a single clock, and proxy drift (measured at 11 s) never
enters the arbitration. A future collection path carrying a client timestamp would break
this arbitration silently: it is written in the function's comment.

A neighbour that **disappears** needs none of this — its observer's next snapshot no
longer contains it, and the bounded deletion of `ReplaceLLDP` removes it. Only a
neighbour that **changes** creates a contradiction that survives a snapshot boundary,
because it does not live on the replaced rows: it lives on those of the **other**
device. And hand-**entered** links are not concerned: `device_links` is another table
(§3.1), written by the operator alone, and nothing here reads it — an intent survives the
unplugging of the cable, that is even the whole point of having entered it.

**Documented, not captured**: no re-plugging was done on the lab to watch the map redraw.
The mechanism is deduced from the two code paths cited and from the operator's report.
The real **convergence time** also depends on each platform's LLDP holdtime — how long a
removed neighbour keeps appearing in `lldpRemTable` — which is measured nowhere on this
fleet. During that window the port legitimately lists two neighbours and the broadcast
domain rule (§1.7) already removes the edge: the map then shows **nothing** rather than
the old link.

## 6. What the screen shows

- **Hovering a port cell** (Devices page): the neighbour's system name, remote port,
  **since when** it has been seen, and the recognition path. Several neighbours are all
  listed, with the sentence saying what it means. **Nothing is displayed if there is no
  neighbour** — see §1.8;
- **"LLDP?" marker** on the card of a silent device, whose tooltip states **which** of
  the two causes applies, because they are fixed in opposite ways;
- **weathermap**: observed links are drawn **under** entered links and **offset by 5 px
  perpendicularly**. Both join the centres of the same nodes: without an offset, a
  finding confirming a declaration would be superimposed to the pixel and one of the two
  would disappear. See [[Weathermap]] for the choice of stroke attributes, and why
  neither colour nor long dashes could be used;
- **"Observed LLDP neighbourhood" panel** under the plan, with the sentence that prevents
  the wrong reading: these links are findings, they do not replace entered links, and a
  link seen from one side is the ordinary case.

No **event** is written to the [[Event log]], and that is the rule it already set:
*"the LLDP neighbourhood produces no event"*. `link.created` / `link.deleted` remain
reserved for entered links. No **site notification** either (`NotifySiteChanged`): the
screens concerned already have a periodic refresh, and waking up all of a site's browsers
twice a minute per switch would make them reread the faceplate to redraw exactly the same
picture.

## 7. Decided / shipped / open

| Question | State |
|---|---|
| LLDP-MIB survey on the fleet's three platforms, column by column | **shipped** — §1, read-only walks of 29/07/2026 |
| Decoding the three-arc index of `lldpRemTable` | **shipped**, pure and tested — `snmppoll.decodeRemIndex` |
| Resolving the local port through `lldpLocPortTable`, never by arithmetic | **shipped** — `snmppoll.localPortName` |
| Lossless conversion of `OctetString`s, a single implementation | **shipped** — `snmppoll.OctetText`, `cmd/proxy/rpc.go` delegates |
| Upload channel: separate HTTP route, no buffer | **decided and shipped** — §2, `POST /api/proxy/lldp` |
| Scope check (collection authority), plain 403 | **shipped** — `handleProxyLLDP` |
| 60 s cadence, turned on by the connector's declaration | **shipped** — `wantedLLDPInterval`, `optionalIntervalFor` |
| `snmp` / `lldp` channel on the RouterOS connector, **measured and dated** | **shipped** |
| Two tables distinct from `device_links`, replacing snapshot, `first_seen` preserved | **shipped** — migration 0028 |
| Recognising a neighbour: three paths, ambiguous token neutralised | **shipped**, pure and tested |
| Derived links, exact reciprocity, unilateral link drawn | **shipped**, pure and tested |
| **Invalidating a learned link when the neighbour changes side** | **shipped**, pure and tested — §5.1, `markRefutedObservations`. The rule ("occupied by another, in a strictly fresher finding, according to the device that owns the port") is **deduced** from the two paths that freeze rows: the proxy pushes nothing for an unreachable device, `ReplaceLLDP` only deletes those of the snapshot. **No re-plugging was captured on the lab**, and the fleet's LLDP holdtime remains unmeasured: it is what sets the convergence time |
| Port hover, silence marker, weathermap, diagnostic panel | **shipped** |
| **One-click adoption of a ghost neighbour** | **NOT DONE.** [[Weathermap]] plans it (greyed node → click → adoption). The link is listed with its chassis and its `sysDescr`, which gives what is needed to create it by hand, but there is no adoption gesture |
| **"Neighbour" column of the Ports page fed by LLDP** | **DONE**, and the arbitration left open was settled the other way on 30 July 2026: that is where the operator looks for "what is opposite?", and a dash on a switch speaking 802.1AB sent people looking for a broken collection. The **finding wins**, the **entry is the fallback** — it covers what LLDP does not see (SwOS, silent neighbour, LLDP off on a port) — and the cell **always says which of the two it shows** (`· LLDP` or `· entered`). Several neighbours on a port: the first, a `+n`, and the full tooltip. What remains not done is the COMPARISON of the two, next row |
| **"LLDP uplink" chevron on a port cell** | **NOT DONE.** [[Port rendering]] decides it; this batch only adds a tooltip. A visual marker requires deciding its priority against VLAN, drift and PoE, which already occupy the cell |
| **Entered ↔ observed comparison** ("this declared link is seen by nobody") | **NOT DONE.** It is the natural next step, and it is deliberately separate: it requires deciding what a disagreement MEANS, whereas this batch only establishes the two halves to compare |
| **LLDP neighbourhood through a channel other than SNMP** | **not planned.** RouterOS's `/ip/neighbor` and the 1930's web page could return it, but periodic collection runs on the proxy, which only speaks SNMP. A second path for the same data would require arbitrating between them |
| **`lldpStatistics`** (`ageouts`, `drops`) | **not collected.** Surveyed on the 1930 (§1) and it would be a real neighbourhood health metric — floats, hence fitting the existing pipeline. Nothing reads it |
| **LLDP over SNMPv3** | **out of scope by construction**: collection is v2c like the rest ([[Service identity and rotation]]) |
| **Distinguishing "LLDP off on this port" from "nobody opposite"** | **impossible as things stand** — §1.8. The MIB simply does not list the port, it does not say why |
