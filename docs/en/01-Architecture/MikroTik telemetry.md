---
tags: [architecture, monitoring, connectors, mikrotik]
---

# MikroTik telemetry — CSS326 and CRS326

Extends [[Monitoring]] and [[Unified monitoring and pipelines]] to the two MikroTik
platforms of the reference fleet, and answers a question those two pages raised without
settling it: **through which channel do we get what, on devices that share neither
firmware nor sensors**, even though they come from the same vendor?

The two devices are in the same lab, they carry the same chassis reference give or
take one prefix, and **everything that follows sets them apart**:

| | `lab-css326` — CSS326-24G-2S+ | `lab-crs326` — CRS326-24G-2S+ |
|---|---|---|
| Firmware | **SwOS** 2.17 | **RouterOS** 7.21beta3 |
| Connector | `internal/driver/mikrotik/swos` | `internal/driver/mikrotik/routeros` |
| Telemetry channel | scraping of `*.b` pages (HTTP) | REST, **binary API**, SNMP |
| Polled over SNMP today | yes, since the survey of 29/07/2026 (§4) | yes — but **silent** until 29/07/2026, the agent refusing the proxy's source (§4.6) |
| CPU load, memory | **never**: the firmware does not publish them | yes |
| Optical diagnostics | yes, through a simple GET | yes, through a **command** (§2) |
| Negotiated link speed | yes, `/link.b` | **not through REST** (§2) |
| **LLDP neighbours** | **never**: the firmware does not speak 802.1AB — walk of `1.0.8802` **empty**, measured on 29/07/2026 | yes, full LLDP-MIB over SNMP ([[LLDP neighbours]]) |

What the catalogue declares, in passing, because it is a recurring question:
`internal/catalog` knows **neither** of these two models. It only carries
`Generic-*` templates, and its comment says why — it is a mock-up catalogue, to be fed
some day by the driver registry. The real faceplate of a MikroTik therefore comes from its
connector, which **reads** it on the device (`parseDesignation` for RouterOS,
`geometryOf` for SwOS) instead of looking it up in a table.

## 1. RouterOS's three channels, and why three are needed

This is the starting point, and it had not been written down: **none** of the three channels of this
platform returns everything. They are not three ways of doing the same thing.

| | SNMP (161/udp) | REST (80) | **Binary API** (8728 / 8729) |
|---|---|---|---|
| link state | yes | yes | yes |
| **NEGOTIATED link speed** | yes (`ifHighSpeed`) | **NO** | yes, one request per port |
| octets / packets | yes | yes | yes |
| detailed errors | partial (`ifInErrors`) | yes, about ten counters | yes |
| link-down counter | no | yes (`link-downs`) | yes |
| CPU / memory / uptime | yes | yes | yes |
| board temperature | yes | yes | yes |
| **SFP optical diagnostics** | yes (`mtxrOpticalTable`) | **NO** | yes |
| run by | the **proxy** | the controller | the controller |

Two cells decide the channel, and both are **absences from REST**:

- **negotiated speed is not there.** It was observed on the captures: the 87 fields of
  `/interface/ethernet` carry no `speed`, and `/interface` carries none
  either. There is only `advertise`, which says what the port *offers* and not what it has
  *negotiated*. A port that fell back to 100 Mbit/s because of a cut pair is therefore **invisible through
  REST** — the most insidious failure mode of a copper network, since everything
  works, ten times slower. A test locks this absence in: if a firmware
  added a speed field, the justification of the binary API channel would change
  with it;
- **optical diagnostics are not there either**, and that was already written in the
  connector: "`monitor` is a POST, out of reach of a tool restricted to GET".

Both are missing for the **same** reason, and that is what makes the conclusion simple:
`/interface/ethernet/monitor` is a **command**, not a menu. RouterOS's REST serves
menus very well; commands are executed by the binary API.

### 1.1 Three protocols not to confuse, one of which we do not implement

This is the most frequent confusion on this product line, and it is costly because all
three answer on neighbouring ports, on the same device, with the same account:

| Port | What it is | Implemented |
|---|---|---|
| 80 / 443 | the **REST**. HTTP, JSON, Basic on every request | yes, `rest.go` |
| **8728 / 8729** | the **binary API**. A *sentence*-based protocol, words prefixed by their length | yes, `api.go` |
| 8291 | **Winbox**. A *yet different* protocol, encrypted, undocumented, whose payload is a serialisation specific to the graphical tool | **no**, and it is not planned |

Saying "the API Winbox uses" in practice means **the binary API**, because it is
the only one of the two specified by the vendor. The 8291 protocol has
no public specification; reimplementing it would be endless reverse engineering
for data that 8728 already serves.

The three capabilities already existed and had been surveyed (`/ip/service`: `www` on 80,
`api` on 8728, `api-ssl` on 8729, `winbox` on 8291, none disabled). The only thing
that changed is the comment on `BinaryAPI`, which said "which this connector does not
use".

### 1.2 The protocol, in one page — what you need to know to re-read it

An exchange is a sequence of **sentences**. A sentence is a sequence of **words**, each
preceded by its length, and terminated by an **empty** word. There is no other end
marker: forgetting it does not produce an error, it produces a silent session that no
timeout on our side unblocks, since the device waits patiently for the rest.

The length encoding is the part where an error **does not show right away**:
a badly framed word does not produce a syntax error, it **shifts the stream** —
the device reads the end of one word as the start of the next, and the session goes
`!fatal` several commands later, with no apparent connection to the cause.

| Length | Bytes | Encoding |
|---|---|---|
| 0 … 0x7F | 1 | `len` |
| 0x80 … 0x3FFF | 2 | `len \| 0x8000` |
| 0x4000 … 0x1FFFFF | 3 | `len \| 0xC00000` |
| 0x200000 … 0xFFFFFFF | 4 | `len \| 0xE0000000` |
| ≥ 0x10000000 | 5 | `0xF0` then `len` on four bytes |

> [!note] Two protocols from the same vendor, two OPPOSITE byte conventions
> The length here is **big-endian** (most significant byte first). SwOS's IP addresses,
> on the other hand, are **little-endian** — `0x240200c0` reads as 192.0.2.36, and the
> neighbouring connector devotes an entire paragraph to this trap. Inverting either one
> produces perfectly valid bytes and a wrong value, which is the only
> category of error no message reports. Hence, on both sides, a round-trip
> test.

Replies carry their type as the first word: `!re` a record, `!done` the
closing, `!trap` a refusal **with** its message, `!fatal` a session closed by
the device, `!empty` an explicit "there is nothing" (RouterOS ≥ 7.18).

Two design decisions are worth writing down here, because they go against
common practice:

- **the `.tag` marker is used to DETECT, not to multiplex.** This connector never sends
  two commands in flight; a reply from another turn can therefore only mean one thing
  — the stream is shifted, and what we are reading is not the reply to what we asked. It
  is therefore **refused**, the session abandoned, rather than sorted: sorting silently
  would return one record's values for another, that is, credible and wrong
  telemetry — exactly what the SNMP poller's strict indexing exists to
  avoid;
- **a `!trap` is an error, never an empty result.** On a read command, a
  refusal read as "no data" would lead to concluding "this menu is empty" where
  the device said "you are not allowed". The two are repaired by opposite
  gestures: looking for a missing feature, or fixing a group policy.

### 1.3 Opening a session: two protocols, and the order that makes them free

MikroTik changed the login in 6.43, and both forms coexist in the fleet:

| Firmware | Form | What it is worth |
|---|---|---|
| ≥ 6.43 | `/login =name=… =password=…` in **one** sentence | the password travels **in clear** |
| < 6.43 | two sentences: bare `/login` returns `=ret=<challenge>`, the client answers `=response=00<md5(0x00 ‖ secret ‖ challenge)>` | the secret does not travel, but unsalted MD5 does not authenticate the **server** |

Neither is "safe": the first exposes the secret, the second proves nothing
about the device's identity. It is the **proxy tunnel** that protects the exchange, as
it already protects REST's HTTP Basic — and that is written here rather than implied.

**The modern form is tried first, and that costs nobody anything**: a legacy
firmware *ignores* attributes it does not understand and replies with its challenge. The presence
of a `=ret=` in the `!done` is therefore both "here is the challenge" and "I am a firmware
from before 6.43" — a single discriminant, no version detection, no round-trip
spent probing. One sentence in the modern case, two in the legacy case, never
a wasted attempt.

Trying the other way round would cost everyone a sentence and send a **bare**
`/login` — hence without an account — to modern firmwares that refuse it, which would produce a
refusal to explain for a perfectly healthy device.

> [!warning] Three ways to get the challenge computation wrong, and they give the SAME message
> "cannot log in" on a secret that is nonetheless right, and nothing to say which one:
> the leading null byte is in the **hash** whereas the leading `00` is in the
> **returned string**; the challenge is decoded from hexadecimal **before** entering the hash
> (hashing it as is gives a valid and wrong digest); and the order is null, secret,
> challenge. The test recomputes the digest from the specification rather than copying a
> constant — a constant copied from the code would check that the code agrees with
> itself.

And a fourth trap, which is not in the computation: on this platform, "cannot log
in" has **two** causes the device does not distinguish — a wrong secret, or a valid secret
in a group that does not carry the `api` policy. The connector's error message
names the second, otherwise you hunt for a password for an hour.

### 1.4 TLS on 8729: the port listens, and that is not enough

This is the most counter-intuitive limit of this channel, and it is **structural** rather
than temporary.

Survey: `/ip/service` serves `api-ssl` on 8729, **not disabled** — unlike
`www-ssl`, which is `disabled` **and** `invalid` for lack of a certificate. One could
conclude that 8729 is usable as is. That is probably wrong: RouterOS has
historically served `api-ssl` **without a certificate** by negotiating an **anonymous**
Diffie-Hellman suite (`ADH-…`), and anonymous suites are **absent** from Go's TLS
library — by design decision of that library, since they authenticate neither
party.

Practical consequence: on a RouterOS where nobody has installed a certificate, requesting
TLS will fail at negotiation. The TLS path therefore exists for devices on which
the operator has **installed a certificate**, and the error message names the fallback (the API in
clear, through the tunnel). Where a certificate exists, it is **self-signed** and
meshcraft has no authority to validate it: verification is disabled, which
brings TLS down to encryption without authentication — hence the same level of
trust as clear REST, that of the tunnel.

## 2. What each platform returns, metric by metric

The series vocabulary is that of `internal/metricbuf` (file `names.go`), and it
is now **written in a single place**. It was not: the names lived hard-coded
in `internal/snmppoll`, the product's only producer of samples — which was perfectly
fine while it was the only one. It no longer is, and two producers of the same series are
exactly the situation in which a copied name ends up diverging by one letter. The cost would not
be a compilation error but **two neighbouring series**, each populated
for half the fleet: no screen would report anything, it would show a curve
interrupted where the device changes product line.

| Series | CSS326 (scraping) | CRS326 (binary API) | CRS326 (SNMP) |
|---|---|---|---|
| `meshcraft_if_oper_status` | `/link.b` `lnk` | `running` | `ifOperStatus` |
| `meshcraft_if_speed_mbps` | `/link.b` `spd` | `monitor` `rate` | `ifHighSpeed` |
| `meshcraft_if_in_octets_total` | `/stats.b` `rb`+`rbh` (64 bits) | `rx-byte` | `ifHCInOctets` |
| `meshcraft_if_in_packets_total` | `/stats.b` `rtp` | `rx-packet` | — |
| `meshcraft_if_in_errors_total` | `/stats.b` `rte` | enumerated sum (§2.2) | `ifInErrors` |
| `meshcraft_if_in_discards_total` | — | `rx-overflow` | — |
| `meshcraft_if_in_bits_per_second` | **`/stats.b` `rrb`** | — | — |
| `meshcraft_if_link_downs_total` | — | `link-downs` | — |
| `meshcraft_uptime_seconds` | `/sys.b` `upt` (hundredths) | `uptime` (`1d17h38m11s`) | `sysUpTime` (hundredths) |
| `meshcraft_cpu_load_percent` | **never** | `cpu-load` | `hrProcessorLoad` |
| `meshcraft_memory_free_bytes` | **never** | `free-memory` | `hrStorage` |
| `meshcraft_temperature_celsius` | `/sys.b` `temp` | `/system/health` | `mtxrHealth` |
| `meshcraft_sfp_rx_power_dbm` | `/sfp.b` `rpw` | `monitor` `sfp-rx-power` | `mtxrOpticalRxPower` |

### 2.1 Three channels, three scales for the same physical quantity

This is the most useful fact on this page, and it explains why conversion lives next to
each read and not in the series vocabulary. For **optical power**:

| Channel | What the device serves | What has to be done |
|---|---|---|
| Binary API | `-3.2` — **already in dBm** | nothing |
| SNMP | `-3197` — thousandths of dBm, **signed** integer | divide by 1,000 |
| SwOS | `4790` — tenths of a **microwatt** | `10·log₁₀(raw / 10,000)` |

The vocabulary therefore only imposes a **unit** — dBm, because an optical budget
is written in dB, attenuations add up in it, and a transceiver vendor's
thresholds are given that way — and lets each channel make its own way
there. Publishing milliwatts would force every alert to redo the logarithm.

> [!important] Zero has no value in dBm, and it is the case that matters
> `log₁₀(0)` does not exist. A powered cage with no light — unplugged fibre,
> far-end device switched off — therefore produces **no** sample, rather than minus
> infinity. This is not mathematical fussiness: a point at `-Inf` in
> VictoriaMetrics crushes the scale of the whole graph, so that a **single**
> unplugged fibre would make the curves of every working link unreadable.

And an **empty** cage produces nothing either, with a different sentinel per channel —
RouterOS omits the `sfp-*` fields, SwOS returns **-128 °C**, SNMP does not write the row.
Publishing zeros would show two dead transceivers on every switch whose
cages are free, that is, a permanent and false alert on most of the fleet.

### 2.2 Errors and discards: the distinction that decides the gesture

RouterOS separates what MIB-II merges, and it is one of the rare things this channel gives
**better** than SNMP:

- an **error** points at the cable, the connector or the transceiver: the frame arrived
  damaged;
- a **discard** points at sizing: the frame was fine, there was no more
  room.

Adding them up would have people hunting for a physical defect in front of a link that is simply saturated. What
goes into each total is **enumerated** in the code, because a total whose content is unknown
is not a measurement: `rx-fcs-error`, `rx-fragment`, `rx-jabber`,
`rx-too-long`, `rx-too-short` for receive errors; `rx-overflow` for
discards; `tx-collision`, `tx-late-collision`, `tx-underrun` for transmit errors;
`tx-drop-packet` **plus** `tx-queue-drop` for transmit discards — the first is the
chip's discard, the second that of the software queue, both exist and taking only
one would leave a loss mode invisible.

> [!note] `rx-error-events` is deliberately left OUT
> It is an aggregate counter of the Marvell chip: it covers all or part of the five
> receive counters, and nothing in the surveys says which. Adding it would
> count some frames **twice**, and the symptom would be a plausible and exaggerated
> error curve — that is, the worst of results, since it would get a healthy
> patch cord replaced.

The SwOS connector applies the same caution in the other direction: `/stats.b` carries `rte`
("receive MAC errors") **and** `rfcs` (frame check sequence errors), and if
`rte` is a total, adding them would double count. Only `rte` therefore goes into the
series; `rfcs` stays visible for diagnostics, until a traffic injection
settles which of the two contains the other.

### 2.3 SwOS scales, and a corroboration worth more than a code reading

SwOS's scale factors are taken from the JavaScript of its embedded
interface, which is the platform's only reference. For **instantaneous throughput**
(`rrb`, `trb`), the survey says "divide by 100 for bits per second" — and that
value is **corroborated by the counters of the same capture**, which is more solid
than a code reading:

```
octets received    rb  = 0x018f2a86 = 26 159 750
uptime             upt = 0x00e4cf41 = 14 995 265 hundredths = 149 952 s
AVERAGE throughput                   ≈ 1 396 bit/s
instantaneous      rrb = 0x0000145f = 5 215 → /100 = 52.15 bit/s
```

The instantaneous value is indeed **below** the average, which is consistent with a lab link
that is quiet after an adoption campaign. The competing hypothesis — a ×100 factor, hence
521,500 bit/s — would imply **9.8 GB** transferred over the uptime, against 26 MB
actually counted: it is ruled out by three orders of magnitude. A test checks this
corroboration instead of quoting it.

What is **not** established for all that: that this throughput is averaged over the same window
as the interface's. No traffic injection has been done. The figure is right to
within a window, and no tight alert threshold should be built on it before it has
been compared against a known throughput.

What this field does change, on the other hand, and it is what makes a 15 s floor acceptable
on this platform where it would not be elsewhere: **meshcraft does not need to
derive two counter reads** to draw a throughput curve. A wider sampling
therefore does not smooth it.

### 2.4 SwOS counters are 64 bits, in two halves

`rb` carries the low 32 bits, `rbh` the high 32. Reading `rb` alone would wrap the counter
to zero every 4 GiB — **on a 10G link, every three and a half seconds**, which
would produce an entirely false traffic curve. This connector monitors
10G cages: it is not a theoretical precaution.

The reference capture has its high half at zero and therefore cannot prove the
recomposition; the test forces it on a fabricated page, and says so — believing a test
covers something when it does not is more dangerous than not having it.

### 2.5 The indexing of `/sfp.b`: the only real trap on this page

Its arrays are indexed by **cage**, not by port: two entries on a
CSS326-24G-2S+, whereas `/stats.b` carries twenty-six. The cage of rank *i* is the port
of index `sfpo + i` — surveyed `sfpo: 0x18`, so the first cage is the port **numbered
25**. Stitching the two indexings together assuming they coincide would attribute the
optical diagnostics of SFP1 to port 1, and the screen would display **optical power on
a copper jack**.

## 3. What collection really does, and what channels do not decide

> [!warning] The channel declared by the connector does NOT choose the transport — it bounds the rate
> This is the defect this batch found while writing, and it must be understood before reading
> anyone's floors.
>
> **The product's periodic collector runs on the proxy and speaks ONLY SNMP**
> (`internal/snmppoll`, `cmd/proxy/snmp.go`). Yet the controller resolved a
> poll's rate with `MonitoringCapabilities.Best(metric)`, which returns the fastest channel **across all
> kinds**. On the RouterOS connector, which declares a `web` channel at 5 s and an
> `snmp` channel at 60 s, `Best` therefore returned **5 s** — applied to an SNMP walk, that is,
> to the channel whose neighbouring line announced "never more often than 60 s". One
> channel's floor was being applied against **another** channel, which is precisely what a
> floor is not.
>
> The most instructive part is **why** those 60 s existed: the connector's comment
> said so frankly, they were high "so that `Best` always returns the REST
> channel", hence to **deliberately lose an arbitration race**, a never-tested channel not
> being supposed to become a fleet's main path. The intent was right. But the
> race did not exist: the REST channel won nothing, it was **never** taken;
> it only lent its floor.
>
> **Shipped**: `MonitoringCapabilities.BestOfKind(kind, metric)` queries the channel **by
> kind**, and scheduling an SNMP poll now reads the SNMP floor and that alone.
> The original intent is preserved by the appropriate mechanism — a channel can no longer
> win a race that does not exist — and the workaround could be removed: RouterOS's SNMP floor
> goes from 60 s to **10 s**, a value *inferred by comparison* with the 1930 (5 s
> **measured** on a walk twice as long, on a less capable processor) and not
> measured here. `Best` remains what a screen must query to answer "how fast
> can this device be observed".

The fallback is left to the **caller** and is not done in `BestOfKind`: a platform
that declares no channel of the requested kind must be handled explicitly, and hiding that
choice behind a silent fallback would have us poll over SNMP a platform that has
never announced the capability. The two treatments retained:

| Metric | No `snmp` channel declared | Why |
|---|---|---|
| `if_state`, `if_counters` | the **unknown** floor (60 s), and we collect | they fit entirely within MIB-II: every SNMP agent serves them |
| `health`, `optical_power` | **we do not collect** (rate 0) | their base is **proprietary**; asking a device of another brand for them would only return "no such object" |

### 3.1 The rates shipped

| Metric | Wanted rate | RouterOS SNMP floor | Retained on the CRS326 |
|---|---|---|---|
| `if_state` | 30 s | 10 s | **30 s** |
| `if_counters` | 5 s | 10 s | **10 s** — raised, and logged |
| `health` | **5 min** | 60 s | **5 min** |
| `optical_power` | **60 s** | 30 s | **60 s** |

The two new rates are argued where they are written
(`internal/api/watch_handlers.go`); in summary:

- **health at 5 min**, which is the value the design had carried from the start
  ([[Monitoring]] §1, row *Health*) without anything reading it. This batch does not renegotiate it,
  it wires it in. A board temperature is a **thermal mass**: it is not the
  measurement rate that decides detection latency, it is the physics of the phenomenon.
  And CPU load as well as free memory are **instantaneous** samples:
  reading them every five seconds would give noise, not a curve;
- **optics at 60 s**, and the trade-off deserves to be stated because two phenomena of
  opposite speeds live in the same measurement. An optical budget drifts over
  **weeks**; a cut shows within a **second**. What settles it is that the second
  case is already covered elsewhere — by port transitions, which are real time — and that
  this series therefore does not have to detect it, only to **explain it after the fact**. The
  [[Roadmap]] announced the counters' rate (5 s) for this metric, "same kind
  of data"; this batch departs from it, and the reason is the **cost of the module's I²C bus** — the
  slowest on the board — rather than the nature of the curve.

### 3.2 What the sensors cost: the answer is "almost nothing"

| Metric | Samples | Rate | Throughput |
|---|---|---|---|
| `if_counters` | 52 ports × 4 | 5 s | 41.6 /s |
| `if_state` | 52 ports × 2 | 30 s | 3.5 /s |
| `health` | ~8 device series | 300 s | **0.03 /s** |
| `optical_power` | 4 cages × 8 | 60 s | **0.53 /s** |

The two new sets add **less than 1.3%** to the throughput of a 48-port device.
The ceiling of 133 devices per proxy (§2.4 of [[Unified monitoring and pipelines]]) does not
move, and there was no reason to touch the flush constants.

The cost is elsewhere, and it is on the **device**: `health` requires **sixteen distinct
walks**, because the MIKROTIK-MIB sensors are **scalars of the same
branch**. The poller indexes its tables by the last segment of the OID, and all the
scalars of a branch end in `.0`: a walk of the whole branch would overwrite them
one on top of the other and keep only one, **at random**. A temperature that would
sometimes be a voltage — the kind of defect no screen can report. Sixteen requests
every five minutes make one packet every twenty seconds: two orders of magnitude
below what traffic already costs. A test checks that this number of sixteen is indeed the one
the code produces, because two files announce it.

### 3.3 There is no standard MIB for switch health

That is the difference in nature from `if_state` and `if_counters`, which fit entirely
within MIB-II — and hence work everywhere without a per-vendor catalogue.

`ENTITY-SENSOR-MIB` (RFC 3433) exists, but MikroTik does not implement it, which leaves the
choice between a **proprietary branch** and **no curve at all**. The compromise retained
is to put the proprietary OIDs in the **poller** and not in the connectors: collection
runs on the proxy, which knows no connector, and an OID the
device does not serve simply returns nothing.

> [!success] The temperature scale factor is OBSERVED on hardware — survey of 29 July 2026
> This callout used to say the opposite: "none of these OIDs has been read on the two reference
> devices", the only available channel being read-only HTTP. The proxy's diagnostic
> walk, however, speaks UDP — and it is through it that the gesture prescribed in §4 was made.
>
> **Two independent channels of the same device, read at the same instant.** On the
> reference CSS326-24G-2S+ (192.0.2.36), `/sys.b` returns `temp:0x45`, i.e. **69 °C**, and
> `mtxrHealth.11.0` returns **690**. The divisor of 10 is therefore right — it is no longer borrowed
> from Observium and LibreNMS, it is verified. The collected series does show 69 °C.
>
> **And the SIGN was a real defect, which this survey brought to light.** The SFP1 cage, EMPTY,
> returns an optical temperature of `4294967168` — that is, `0xFFFFFF80`, that is,
> **-128** in two's complement. The MIKROTIK-MIB declares these sensors as `Gauge32`, a type
> **unsigned** per RFC 2578, and yet the device writes negative values into it:
> it is the vendor stepping outside its type. Without correction, the series published
> **429,496,716 °C**.
>
> This is not an outlier an operator dismisses at a glance: it **crushes the
> scale** of every other curve in the same graph, and an alert threshold on an empty cage
> fires. The same fault hit **optical powers**, which are negative
> in dBm on every real link — hence wrong on *every* transceiver in the fleet, and not only
> on empty cages. The comment on the optical columns described this trap precisely;
> no line of code applied it. Fixed by `snmppoll.signed32`, with the sign
> applied **before** the scale.
>
> **What remains inferred**, and will remain so until a chassis that carries them enters the
> fleet: voltages (`mtxrHealth.8`), fans (`.17`, `.18`) and power supplies
> (`.15`, `.16`). Neither of the two MikroTiks of the reference fleet publishes them — the CSS326
> serves them at zero in `/sys.b` and its SNMP agent does not expose them at all.
>
> **The deciding gesture has been made, and it confirms the divisor on BOTH product lines** —
> survey of 29 July 2026 on the CRS326. The walk of the whole branch
> `1.3.6.1.4.1.14988.1.1.3` returns six values, and three of them teach something:
>
> | OID | Value | What it establishes |
> |---|---|---|
> | `mtxrHealth.11.0` | `740` | **74.0 °C** — the divisor of 10 is right here too. `/system/health` over REST returned `72` at another instant, `73` in the table below at the same instant |
> | `mtxrHealth.100.1.2.17` / `.3.17` | `cpu-temperature` / `73` | a **table of named sensors** exists on RouterOS, and its value is in **WHOLE degrees**: the divisor of `mtxrHealth.11` does NOT apply there. The poller does not read it — it is a resource, not a defect |
> | `mtxrHealth.14.0` | `800` | the **CPU frequency** in MHz, not a health quantity. Never to be filed among the scalars to divide: 80 V or 80 °C would be perfectly plausible on screen |
>
> The poller requests neither `.14` nor `.9` (which returns the string `n/a`, the device having no
> fan), and that is the right set — verified in `internal/snmppoll/health.go`.
>
> Worth noting, because it is the kind of detail that leads to concluding "not published": the CRS326
> serves **nothing** on `mtxrHealth.6` (`cpu`), the OID this callout prescribed walking.
> It is `.11` (`processor`) that it publishes. Walking the BRANCH rather than a guessed OID is what
> settled it.
>
> And a trap within the trap: a **transceiver**'s temperature does not have the same scale
> as the **board**'s in the same MIB. `mtxrOpticalTemperature` is a bare `Gauge32`,
> in whole degrees. Applying the `mtxrHealth` divisor to it would return 5 °C for a module
> at 50 °C.

One last point, and it is the only one in this file where getting it wrong **inverts an alarm**:
the MIKROTIK-MIB's `BoolValue` enumerates `true(1)` / `false(2)`. On a `raw != 0`, the value
2 — "power supply faulty" — would be read as "true", hence "healthy", and the series
would show a device in good health while its power supply is dead. The
translation is therefore explicit, and a value **outside the enumeration** counts as "not healthy": on
an alarm, the unknown is treated as a fault, never as an acknowledgement.

## 4. The CSS326's SNMP: the lock, and the survey that lifted it

This paragraph described a blockage and prescribed the gesture that would lift it. **The gesture was
made on 29 July 2026**, and what follows keeps both halves: the lock, because its
mechanics remain instructive, and the survey, because it is the authoritative record.

### 4.1 The lock, as it was

The CSS326 was **polled over SNMP by no one**. Not badly, not partially: not
at all. And it was a chain of consistent decisions, not an oversight:

```
swos.Capabilities().SNMPVersions = nil
        │
        ▼
snmpprov.For  →  Plan{Version: "", Reason: "SNMP not managed: this device
        │                              announces no SNMP version."}
        ▼
snmpWatchesFor  →  the device does NOT enter snmp[]
        │
        ▼
the proxy receives no target: no state, no counters, no sensors
```

> [!warning] The lock was CIRCULAR, and that is this batch's methodological lesson
> The diagnostic walk — the very instrument that was to establish the presence of an agent —
> **was refused** on the grounds that no agent was declared: `askCommunityFor` goes
> through `snmpprov.For`, which answered "SNMP not managed", and the API returned *"A walk is made
> on an SNMP agent; meshcraft does not know whether this device has one."*
>
> The measuring tool was therefore unavailable exactly when it was needed. The way out was to
> declare `v2c` as a **hypothesis to verify**, set the SNMP identity, then
> walk — the rule being: had the walk not answered, it is the declaration that would have
> had to be withdrawn. It is an open lead for the product: a read-only walk should
> be able to run as soon as a community exists in the vault, precisely **because** we do not
> yet know whether the agent answers.

### 4.2 The survey

Sequence run on the reference device, through the `proxy-lab` tunnel:

1. **read** `/snmp.b` — the agent is **active** (`en:0x01`) and carries a community;
2. **observation** that the device's community **differs** from the vault's (15 characters
   against 16, comparison by fingerprint, no secret printed). A walk with the vault's would
   have stayed **silent**, and silence does not distinguish "no agent" from "wrong
   community" — it is the trap `askFailure` already describes: *"an agent that refuses a
   community stays silent, it does not protest"*;
3. **setting** the SNMP identity (`snmp-apply`), verified by re-reading the page;
4. **walk**, which answers.

The proxy's walk speaks **hard-coded v2c** (`gosnmp.Version2c`), which makes every reply
obtained a proof of v2c and not of anything else:

| OID | Value observed | What it establishes |
|---|---|---|
| `sysDescr.0` | `CSS326-24G-2S+ SwOS v2.17` | the agent answers, and it is indeed this model |
| `sysObjectID.0` | `1.3.6.1.4.1.14988.2` | the MikroTik tree — and **`.2`**, not `.1` like RouterOS |
| `sysName.0` | `lan-m6` | **cross-checks** the `id` field of `/sys.b`, decoded |
| `ifNumber.0` | `26` | **cross-checks** the 26 ports of `/link.b` (`prt:0x1a`) |
| `ifDescr` | `Port1`…`SFP1`, `SFP2` | the names from `/link.b` — and, since then, **the connector's** ([[Connectors]] § Port names): an SNMP series and a configuration read over web designate the same jack by the same name |
| `ifOperStatus.26` | `1` | the SFP2 cage, the only one carrying a link |
| `ifHCInOctets.26` | `189 323 935` | live **64-bit** counters |
| `ifHighSpeed.26` | `10000` (Mbit/s) | the 10G link |
| `sysUpTime.0` | served | |
| `mtxrHealth.11.0` | `690` | the **only** health quantity published |

The two "cross-checks" rows are not decorative: they confront SNMP with what the
connector reads over **web** on the same device. An agent answering for some other
hardware — a real case when an address has been recycled — would not return that name and that
port count.

### 4.3 What the agent does NOT serve, and which decides the channel as much as the rest

- **`hrProcessorLoad` and `hrStorage`: no value.** No CPU, no memory over SNMP
  on this platform. The poller's `health` set requests them and gets nothing, which is
  harmless — an absent OID simply returns nothing — but they must not be promised.
- **The optical table stops at column 8 (`txBias`).** Neither `txPower` (column 9) nor
  `rxPower` (column 10): full walk of `1.3.6.1.4.1.14988.1.1.19`, ten values, columns
  2, 5, 6, 7 and 8. **`optical_power` is therefore NOT declared on SwOS's SNMP channel**,
  whereas the **web** channel serves it: powers are in `/sfp.b` (`tpw`, `rpw`) and
  nowhere else. It is the only place in the project where a metric is served by
  scraping and **not** by the SNMP agent of the same device.
- **The `14988.2` subtree contains nothing**: the identity OID is not a data
  branch.

> [!important] `ifSpeed` returns ZERO on a 10 Gbit/s link
> Observed on the active cage: `ifSpeed.26 = 0` where `ifHighSpeed.26 = 10000`. A collector
> reading `ifSpeed` would therefore show a **zero** speed on the fastest links of the
> fleet. The poller does read `ifHighSpeed`; it is noted because it is the kind of thing
> someone "fixes" one day in the wrong direction.

### 4.4 What is declared as a result of the survey

```go
Info.Transport            = []string{"web", "snmp"}   // web to WRITE, snmp to READ
Capabilities.SNMPVersions = []string{"v2c"}           // surveyed; v3 does not exist
MonitorChannel{Kind: "snmp", MinInterval: 10 * time.Second,
               Metrics: []string{"if_state", "if_counters", "health"}}
```

**v1 is not announced**, and the omission is deliberate: the agent probably serves it — SwOS
has no version selector — but nothing has shown it, and v2c is what the collector
uses. A capability is surveyed, not inferred from plausibility.

The **10 s** floor against 15 s for the counters over web is not a preference: a
GETBULK on six columns costs this device infinitely less than serialising the
12,694 bytes of `/stats.b`. The SNMP channel does not **replace** web for all that —
`/stats.b` carries the instantaneous throughputs computed by the device, which SNMP does not expose, and
`/sfp.b` carries the optical powers.

### 4.5 Collection, verified end to end

The series are in VictoriaMetrics, on the real device:

```
meshcraft_temperature_celsius{device=…, sensor=processor}        = 69
meshcraft_uptime_seconds{device=…}                               = 10110
meshcraft_if_speed_mbps{device=…, ifindex=26, port=SFP2}         = 10000
meshcraft_if_in_octets_total{device=…, ifindex=26, port=SFP2}    = 195 290 040
```

69 °C and not 690, and not 429,496,716: the whole chain — declaration, provisioning,
rate, collection, scale, sign — is verified on hardware, which was the case
for no sensor before this batch.

### 4.6 The CRS326, for its part, was DECLARED and silent — and it is the exact mirror image

The opening table of this page says "polled over SNMP today: yes" for the
CRS326. That was true of the **declaration** and false of the **fact**: no series ever
came up from this device until 29 July 2026.

The cause is not in its connector, it is in the **source bounds** of the
community meshcraft had written there — `addresses=192.0.2.0/24`, the site plan's
subnet — whereas the proxy polls it from `10.42.0.67`, because it is **routed** there.
A RouterOS agent that refuses a source stays silent. The full mechanism, the survey and the
fix (combined bounds, and collector chosen at adoption) are in [[Monitoring]]
§ *The DEFAULT at adoption*.

The two halves of this page therefore answer each other: on the **CSS326**, the lock was
upstream — no capability, hence no target pushed to the proxy, hence nothing to poll. On
the **CRS326**, everything was in place and it was the device that refused the packet. Both
presented themselves the same way: an empty curve and nothing in the logs.

```
meshcraft_temperature_celsius{device=lab-crs326, sensor=processor}  = 74
meshcraft_if_in_octets_total{device=lab-crs326, …}                  = 26 series
```

## 5. Page status: what is shipped, what is not

| Question | Status |
|---|---|
| Binary API client: framing, sentences, `!re`/`!done`/`!trap`/`!fatal`/`!empty`, tagging | **shipped** — `routeros/api.go`, tested against a simulated device |
| Session opening, **both** protocols (clear ≥ 6.43, challenge/response < 6.43) | **shipped**, with the challenge computation recomputed from the specification in the test |
| TLS on 8729 | **coded**, and **probably unusable** without an installed certificate (§1.4) |
| RouterOS telemetry through the binary API or REST, behind a single abstraction | **shipped** — `routeros/telemetry.go`, `TelemetrySource` |
| SwOS telemetry by scraping the four pages | **shipped** — `swos/telemetry.go`, tested on **real** captures |
| `health` and `optical_power` OID sets in the poller | **shipped** — `internal/snmppoll/health.go` |
| Sensor rates decided by the controller, executed by the proxy, 0 = do not collect | **shipped** — `tunnel.SNMPWatch`, `optionalIntervalFor`, `cmd/proxy/snmp.go` |
| Floor resolved **per channel kind** instead of the fastest across all kinds | **shipped** — `MonitoringCapabilities.BestOfKind` (§3) |
| Series vocabulary in a single place | **shipped** — `internal/metricbuf/names.go` |
| **Periodic collection through a connector channel** (binary API, scraping) | **not built** — see the callout below |
| **A single byte exchanged with hardware over the binary API** | **not done** — everything is tested on a simulator. Not to be confused with the CSS326's SNMP, which is (§4) |
| Scale factors of `mtxrHealth` **temperatures** confirmed | **done on BOTH product lines** — 69 °C over web against 690 over SNMP on the CSS326, 74 °C against 740 on the CRS326 (§3.3, §4.2) |
| SNMP collection actually obtained from the **CRS326** | **shipped** — it was not: the agent refused the proxy's source (§4.6) |
| Sign of temperatures and optical powers | **fixed** — `snmppoll.signed32`; it was a REAL defect, observed on an empty SFP cage (§3.3) |
| Scale factors of voltages, fans, power supplies | **still inferred** — no device in the fleet publishes them |
| SNMP on the CSS326 | **SHIPPED and verified on hardware** — v2c surveyed, channel declared, series collected (§4) |
| PoE | **not applicable** on these two models, and declared `Unavailable` with its survey |
| LLDP on SwOS | **impossible**: the firmware lacks the feature, declared `Unavailable` with its survey |
| Reading series back in the product | **shipped** — `tsdb.QueryInstant` / `QueryRange`; this package only had `Write`, hence eleven series families nothing read back |
| Per-port throughput derived from counters | **shipped** — `internal/api/portrates.go`. The three screens that already read `rx_mbps` / `tx_mbps` show measured values, and the site curve switches to `simulated: false` |
| Dedicated telemetry screen (temperature, CPU/memory, uptime, frame errors) | **shipped** — `GET /api/devices/{id}/telemetry` and `web/src/telemetry.tsx`, under the faceplate. Six of the seven families now have their cell |
| Optical budget on screen | **not done**, and deliberately: `queryable` excludes optical series, voltages, fans and power supplies — nobody in the fleet publishes them, and an empty cell would read as broken collection |

> [!warning] Connector telemetry is not collected PERIODICALLY, and it is not an oversight
> `routeros/telemetry.go` and `swos/telemetry.go` have neither loop, nor rate, nor buffer:
> they return a **snapshot**, and a pure function translates it into the series
> vocabulary. The product's periodic collection runs on the **proxy** and speaks only SNMP.
>
> Plugging a connector channel into that loop would require the **proxy** to hold the
> fleet's administration credentials. That is an architecture decision — the proxy is
> today deliberately devoid of device secrets, it only carries a read-only SNMP
> community — and not a matter of wiring. It **is not taken here**.
>
> What these reads actually serve, then, and it is already a lot: **on-demand
> diagnostics** from the controller — the screen that answers "why is this link
> slow?" (the negotiated speed, which REST does not give) and "is this transceiver at the end
> of its life?" (the optical budget) — and the tested foundation for the day connector
> collection exists. It is written here rather than discovered in front of an empty graph.

And two **platform** limits, which no version will close:

- **a CSS326 has no CPU load or memory series.** SwOS publishes neither
  one nor the other, in any page nor in its full configuration backup. The
  only available measure of "fatigue" is chip temperature. These sentences
  accompany **every** snapshot of this device, including complete ones: the absence of a
  curve reads exactly like broken collection, and an operator who opens this
  screen to understand a slowdown must learn in one sentence that there is nothing to
  expect;
- **SwOS does not distinguish "sensor absent" from "sensor at zero".** The fan
  and voltage fields of `/sys.b` are always present, at zero when the board lacks
  the electronics. Publishing them would make four "fan at 0 rpm" series across the whole
  fleet, hence a permanent and false alert; filtering them makes a fan that has
  **really stopped** indistinguishable from an absent fan. No code can lift
  this ambiguity — the page does not carry the information. What partially contains it
  is the **temperature**, which rises when the fan stops: that is the signal
  to watch on this platform.
