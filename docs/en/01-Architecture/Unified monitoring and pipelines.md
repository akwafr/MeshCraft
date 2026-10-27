---
tags: [architecture, monitoring, orchestration]
---

# Unified monitoring and pipelines

[[Monitoring]] describes *how* meshcraft collects: SNMP, traps, syslog. This note
decides *at what cadence*, *through which path*, *who is capable of it* — and
above all **where to look when it does not work**.

The starting point is a constraint, not a wish: a multi-vendor fleet does not have
a single level of observability. An entry-level web-only switch gives an HTML page
every thirty seconds; a modern chassis pushes gNMI continuously. The product must
make both readable **on the same screen**, without pretending the first is worth
the second.

## 1. Three rhythms, decided by the capability

The available access determines the rhythm. It is not a setting: nobody should
have to know, device by device, what their fleet supports — it is the same logic
as the SNMP v2c/v3 resolution (§2.1 of [[Service identity and rotation]]).

| Channel | **Floor** cadence | What goes in it |
|---|---|---|
| **API / web page** | 30 s | the state, nothing more: a web-only switch's page is expensive and is not to be scanned |
| **SNMP** | 5 s for interface state **and counters**, 30 s for the rest | `ifOperStatus`, the state bits and traffic counters can go down to 5 s; health, PoE and tables stay at 30 s or more (§1.1) |
| **Streaming telemetry** | 5 s | whatever the device is willing to push |

> [!important] These are FLOORS, not targets
> "At most every 5 seconds" means *never more often*. A fleet of two hundred
> devices polled at 5 s is forty requests per second, permanently — most of
> which never change. The real rhythm is the slowest one that satisfies the
> need; the floor is only there to prevent going faster.

Two reasons not to go below it, and they are physical: the management CPU of an
access switch is modest, and an overly frequent SNMP walk degrades the control
plane before it degrades the metric. A device that is over-monitored is a device
that gets damaged.

> [!note] What the code did, and no longer does
> The controller sent the channel's `MinInterval` in `snmp[]` as the cadence —
> hence 5 s on an Instant On 1930, the floor itself. "Never more often than 5 s"
> had become "every 5 s", and an entire fleet ended up polled at the maximum
> rhythm the platform tolerates, exactly what the callout above forbids in black
> and white. It is fixed: `MinInterval` **is a floor again**, and an operations
> policy chooses the cadence (§1.2).

### 1.1 The capability decides, the driver declares it

The connector does not only say *what* it can do, but *at what cadence it is
reasonable* and *through which channel*:

```go
// internal/driver/instanton — the actual declaration of the 1930 range
Monitoring: driver.MonitoringCapabilities{
    Channels: []driver.MonitorChannel{
        {Kind: "snmp", Metrics: []string{"if_state"},
         MinInterval: 5 * time.Second},
        {Kind: "snmp", Metrics: []string{"if_counters"},
         MinInterval: 5 * time.Second},
        {Kind: "snmp", Metrics: []string{"health", "lldp", "poe"},
         MinInterval: 30 * time.Second},
        {Kind: "web",  Metrics: []string{"if_state"},
         MinInterval: 30 * time.Second},
    },
    Traps:     true,
    Syslog:    true,
    Streaming: false,
}
```

What the platform cannot do is not worked around: the device appears with the
channel it has, and the UI says which. A half-monitored fleet without anyone
seeing it would be the worst outcome — it is rule 5 of the
[[Standard data model]] applied to collection.

**One channel per cost, not one channel per connector.** The declaration above
used to count two SNMP channels instead of three: `if_state` and `if_counters`
shared a line, and `health` / `lldp` / `poe` were grouped with them at 30 s. The
30 s came from no measurement — it came from the **grouping**, where four metrics
declared together all inherit the floor of the most expensive one. Nothing in the
range's documentation imposes 30 s on `ifHCInOctets`; the 1930's "fragile under
bursts" caveat concerns its **web** channel and its five sessions
([[Connectors]]), not SNMP, which is read-only and opens no administration
session.

The channel was therefore **split**: `if_counters` alone, with its own 5 s floor
proven on a 48-port 1930 — a six-column walk over 52 interfaces every five seconds
does not move its management CPU. This is not the safeguard being bypassed, it is
**the declaration becoming accurate**: it finally says what each metric costs,
instead of making all of them pay the price of the most expensive.

> [!important] The split is what protects `health`, `lldp` and `poe`
> These three metrics **keep 30 s**, and that is the whole reason for the split.
> No collection reads them yet. The day it does, having left them in the counters
> channel would have them scanned at **5 s** — the exact opposite of the design,
> which puts health and PoE at 5 min and network tables at 5-15 min
> ([[Monitoring]] §1). A 30 s floor already leaves them six times too much
> margin; lowering the whole channel to speed up traffic would have dragged along
> three metrics that asked for nothing.

### 1.2 The policy chooses, the floor bounds — and a clamp is announced

Two notions, long conflated in the code, and separating them is the whole batch:

| | Who carries it | What it says |
|---|---|---|
| **Wanted cadence** | the operations policy (`internal/api/watch_handlers.go`) | at what rhythm the operator needs to see |
| **Floor** | the connector (`MinInterval`, §1.1) | "never more often than" — what the platform can take |

The policy **chooses**, the floor **bounds from below**. What is shipped:

| Metric | Wanted cadence | 1930 floor | Applied on a 1930 |
|---|---|---|---|
| `if_state` — port state and speed | **30 s** | 5 s | 30 s |
| `if_counters` — `ifHCInOctets`/`Out`, `ifInErrors`/`Out` | **5 s** | 5 s | 5 s |
| `health` — `sysUpTime`, CPU, memory, temperatures, voltages | **5 min** | 30 s | *nothing: this connector declares no `snmp` channel for it* |
| `optical_power` — transceiver diagnostics | **60 s** | — | *nothing, same reason* |
| `lldp` | **60 s** (`wantedLLDPInterval`) | 30 s — imposed by the device, `discover-interval` | does NOT come up through the metrics pipeline: it is a table, see [[LLDP neighbours]] §2 |

> [!important] `health` and `optical_power` are collected since [[MikroTik telemetry]], and NOT everywhere
> Both OID sets now exist in the poller (`internal/snmppoll/health.go`), and the
> table above says something to read carefully: the 1930 connector groups
> `health` with `lldp` and `poe` in a 30 s `snmp` channel, but the applied cadence
> remains *nothing* — because scheduling now queries the channel **by kind** and
> these three metrics are only switched on if an `snmp` channel carries them
> **by name**. On the 1930 they are there; what is missing is that the poller can
> read neither `lldp` nor `poe`, and that `optical_power` is not declared there at
> all — the range does not expose it, and that is written in [[Connectors]].
>
> This reversal is deliberate: there is **no standardised MIB** for a switch's
> health, so the foundation is proprietary. Asking a device of another brand for
> sixteen MikroTik branches would only return "no such object" — traffic for
> nothing, on every cycle and across the whole fleet. A cadence of **zero**
> therefore means "do not collect", and that is the normal regime, not a degraded
> case.

> [!important] A clamp to the floor is never silent
> When the wanted cadence goes below the floor, the floor wins — and the
> substitution is **logged** as a warning: the device, its model, the metric, the
> wanted cadence, the applied one, and **the origin of the floor** (the
> connector's channel, or the fact that the connector is unknown). An operator who
> asked for 5 s and gets 30 s must be able to learn it some other way than by
> comparing graphs. Silence, here, would turn an operational decision into a
> mystery.
>
> The line is emitted on every **recomputation** of monitoring — a proxy
> connecting, a fleet change — and not on every poll: that is what makes it
> bearable, and it is also what makes it reappear after a restart rather than get
> lost in a history nobody scrolls back through.

**The inversion of the two cadences is deliberate.** Read alone, the line
"`if_state` at 30 s" looks like a regression — it used to be 5 s. It is the
opposite:

- **port state SLOWS DOWN** because it no longer needs to be *scanned*.
  Responsiveness now comes from the trap channel: a `linkDown` triggers the poll
  **on the proxy** within 250 ms (§2.3). Periodic polling is no longer the source
  of responsiveness, it is the **safety net** for those that push nothing — and a
  net is checked every 30 s, not every 5 s;
- **traffic SPEEDS UP** because a counter serves to draw a **curve**. At one point
  per minute, a two-minute saturation does not show, it is guessed from two
  points. Fine grain is what makes the graph usable, and it is the only reason to
  pay for it.

In other words: we stopped paying 5 s for a boolean piece of information that
traps give in one second, and put that money where it buys something. The price,
for its part, is real and is quantified in §2.4 — *a cadence that does not get
reported is not a cadence*.

> [!warning] A channel's floor must bound ONLY that channel — confusing them was a defect
> The periodic collector runs on the proxy and speaks **only SNMP** (§2). Yet the
> resolution went through `MonitoringCapabilities.Best(metric)`, which returns the
> fastest channel **of any kind**: on a connector declaring a `web` channel at 5 s
> and an `snmp` channel at 60 s, the controller pushed 5 s… to an SNMP walk. One
> channel's floor was applied against **another** channel, which is exactly what a
> floor is not.
>
> It was not visible on the 1930, whose two `snmp` channels are the fastest; it
> was on RouterOS, and the most instructive part is **why** its SNMP floor was
> 60 s: to *lose on purpose* an arbitration race, since a never-proven channel
> must not become a fleet's main path. The intention was right, but the race did
> not exist — the REST channel was **never** used, it only lent its floor.
>
> **Shipped**: `BestOfKind(kind, metric)`, and the fallback stays with the caller
> rather than hidden in the method — a platform that declares no channel of the
> requested kind must be handled explicitly. Full detail in §3 of
> [[MikroTik telemetry]].

**Unknown connector: 60 s floor.** Unregistered connector, or a connector that
declares no `snmp` channel for this metric: in both cases we do not know what the
platform can take, and the clamp is announced like the others. This fallback
applies to `if_state` and `if_counters`, which fit in MIB-II; for the two
**sensor** sets, the absence of an `snmp` channel means "do not collect" (callout
above). Applying 5 s would be **betting on the control plane** of a device we do
not know — *a device that is over-monitored is a device that gets damaged*.
Collecting nothing would deprive precisely the least-known devices of monitoring.
One minute: slow enough to damage nobody, fast enough for a screen not to stay
empty. It is also the value this path already used as a fallback, which avoids
inflicting a load regression on an existing fleet as a side effect.

> [!warning] The trade-off: at 30 s, port responsiveness DEPENDS on traps
> It is an accepted trade-off, not an oversight. On a device whose trap recipient
> is not configured, a port going down now takes up to **30 s** to show, against
> 5 s before this batch.
>
> And it is not an edge case: setting the trap recipient in devices **is built
> nowhere today** (§7, step 6). The proxy's listener works, but it only receives
> what hand-configured devices are willing to send it. On a fresh fleet, the
> effective detection cadence of a downed port is therefore 30 s, full stop.
>
> What makes the trade-off tenable is what makes it **reversible**: the wanted
> cadence is a policy constant, not a property of the protocol. What makes it
> **honest** is the `trigger` field of port events (`"poll"` / `"trap"`), the only
> way to observe that a fleet really receives its traps — without it, a silent
> fleet and a responsive fleet produce the same events give or take thirty
> seconds, which nobody notices on a screen.

## 2. Where it runs: the proxy, not the controller

SNMP and streaming telemetry are **executed by the [[Proxy]]**, and traps are
*necessarily* received there: a trap is a UDP datagram sent to a concrete
address, there is nothing to relay and nothing to tunnel.

Having the controller poll through the tunnel would cost a WAN round trip per
metric and per device, and would turn every link outage into a generalised
monitoring gap. The proxy collects, aggregates, and reports only the result
([[Controller-proxy protocol]]).

Accepted consequence: **the proxy keeps a local queue**. If the tunnel goes down,
it keeps collecting and replays on reconnection, within the limit of its
retention. Monitoring that loses the five minutes of an outage is monitoring that
lies precisely at the most interesting moment. That limit is a number, not an
intention: it is quantified in §2.4, with the throughput the reporting drains and
what happens beyond it.

### 2.1 Attaching a measurement to the right port

An SNMP metric arrives indexed by `ifIndex`. One still has to know which
**faceplate port** it is about, and that is trickier than it looks. Three
approaches, two of them wrong.

**By arithmetic on the index** (`prefix + ifIndex`): that is what was done, and it
only worked by luck. An Instant On 1930 numbers its ports 1 to 52 in index order —
a vendor coincidence, not a property of the protocol. On a MikroTik whose
faceplate mixes `ether1` and `sfp-sfpplus1`, arithmetic designates ports that do
not exist. Worse, a device exposes many more interfaces than it has ports:
observed on a 1930, `TRK*` aggregates at index 1000, tunnels at 3000, VLAN
interfaces at 100000. Written as is, they created **ghost ports** in the
inventory — 93 entries for 52 real ports on the switch that served to flush them
out (purge: migration `0019`).

**By label alone** (`ifDescr`): worse still, because labels **collide**. On that
same 1930, the VLAN 42 interface is called "42", exactly like physical port 42.
Trusting the label would attribute a VLAN's traffic to a port: a credible and
wrong graph, more serious than a ghost port, which at least shows.

| ifIndex | ifDescr | nature |
|---|---|---|
| 42 | `42` | physical port |
| 100041 | `42` | VLAN 42 interface |
| 1000 | `TRK1` | aggregate |

**The right answer comes in two steps.** First `ifType` decides what is physical:
`ethernetCsmacd(6)` is the only vendor-neutral answer, and it is a **mandatory**
MIB-II column. It rules out aggregates, tunnels and VLAN interfaces in one go,
whatever the vendor. Only then does `ifDescr` serve to **name** the port, through
a ladder that goes from the safest to the most tolerant: exact equality (MikroTiks
name their interfaces like their faceplate), equality normalised for case and
punctuation, then faceplate prefix + label ("1" becomes "GE1"). A label that
cannot be attached is **discarded**, never invented — and two ports that
normalisation would conflate are not settled at random, they are refused.

> [!important] Telemetry never CREATES a port
> A measurement describes a port, it does not discover it. The controller requires
> the `iftype` label to touch the inventory: a proxy of an earlier version still
> feeds metrics but no longer modifies the faceplate. Closed by default, because
> the alternative would be guessing.

Filtering happens **on the proxy side**, so as not to pay for the cardinality of
series we do not want; attaching to the port happens **on the controller side**,
the only one that knows the faceplate.

### 2.2 Who is entitled to answer for an address

The proxy pings by **address**: its state table is indexed by IP. Sending it two
different devices at the same address therefore produces not two monitors but a
single one, the second silently overwriting the first — a green light on a
switched-off switch. The case arises as soon as an instance proxy covers two sites
that reuse the same private addressing plan.

The [[Addressing domains|addressing domain]] settles it: the device whose domain
is declared **on this proxy** wins. If none is, or if several are, all are
discarded with their reason — saying nothing is better than saying something
wrong. The exclusion appears as a banner on the device's page and emits a
`watch.excluded`, on the transition only.

The filter **also applies to traps** (§2.3): a device excluded from monitoring has
no `SNMPWatch` on this proxy, so its source IP is not recognised there and its
traps are ignored. That is the right consequence — attributing a `linkDown` to the
one of the two devices that did not move would be worse than receiving nothing.

### 2.3 The fast path: a trap brings a poll forward, it does not replace it

Three cadences coexist, and they must be told apart to understand where what goes:

| Path | Transport | Rhythm | What it carries |
|---|---|---|---|
| Metric samples | local buffer → controller | drained every **2 s**, up to 4 batches (§2.4) | time series (§3) |
| Transitions | tunnel event channel | **immediate** | `device.*`, `port.*` ([[Controller-proxy protocol]]) |
| Trap received | nothing crosses the WAN | — | a **poll trigger**, local to the proxy |

The trap is not a fourth data channel: it does not go up. It causes, **on the
proxy**, the `if_state` poll that was scheduled anyway, and it is the transition
derived from that poll that leaves on the event channel. The full justification is
in [[Monitoring]] §2; what belongs to this note is the consequence on rhythms:

- a port's responsiveness does **not** depend on the buffer drain. A status dot
  that waited for series ingestion would show the metrics pipeline's delay, not
  the state of the network;
- the connector's floor (`MinInterval`, §1.1) bounds **periodic** polling. The fast
  path has its own damper, and it is tighter: 250 ms of coalescing, at least 2 s
  between two trap-triggered polls on the same device. It is not an exception to
  the floor, it is its application to a different regime — *a device that is
  over-monitored is a device that gets damaged* remains true at one second as at
  thirty;
- an `if_state` brought forward by a trap **is** the cycle's poll, not an extra
  poll: it pushes the ordinary cadence back by as much. Without this shift, the
  regular cycle would reread the same table in the following second — two walks
  for a single piece of information, and on the very device that is in trouble.

> [!warning] The fast path is measured, otherwise it is imagined
> The `trigger` field of port events (`"poll"` / `"trap"`) is the only way to
> observe in operation that traps actually arrive. A fleet whose devices never
> received their destination configuration produces exactly the same events as a
> responsive fleet — give or take thirty seconds, which nobody notices on a
> screen.

### 2.4 What the reporting drains: the limits, in numbers

A cadence that does not get reported is not a cadence. Lowering `if_counters` to
5 s (§1.2) moves the problem from the device's control plane to the proxy's
**reporting throughput**, and that one can be computed.

**What a device produces.** The collector returns **four** samples per port for
`if_counters` (inbound and outbound octets, inbound and outbound errors) and
**two** for `if_state` (state, speed). A 48-port 1930 exposes 52 physical
interfaces — 48 copper plus 4 SFP:

| Metric | Samples | Cadence | Rate |
|---|---|---|---|
| `if_counters` | 52 × 4 = 208 | 5 s | 41.6 /s |
| `if_state` | 52 × 2 = 104 | 30 s | ≈ 3.5 /s |
| **total per device** | | | **≈ 45 /s** |

**What the reporting drains.** The buffer is drained every **2 s**, in bursts of
at most **4 batches** of **3000** samples each — that is **6000 samples per
second** in steady state, hence **133 48-port devices per proxy**. The previous
setting (5000 samples every 10 s) drained only 500/s, that is **eleven** devices:
the 5 s cadence would have overflowed from the twelfth. The burst is also what
lets a lagging buffer — back from a tunnel outage — catch up instead of chasing
collection forever. At the ceiling, this asks the controller for about 2 requests
per second of ~670 KiB, ≈ 1.3 MB/s — that is the stated price of the cadence, and
it is **linear** in the number of devices.

**What happens beyond.** The buffer **overflows and drops its oldest points**. It
is the failure mode the 5 s cadence makes possible, and it is perfectly silent
from the operator's point of view: the tunnel is up, requests succeed, only gaps
in the curves remain. It is therefore **logged** as a warning, distinctly from the
losses that accompany a send failure and, since this batch, from the
**abandonment** of a batch on a definitive refusal: three log lines for three
symptoms, because they are not the same gestures (see below). The line carries the
count for the **period** and not the cumulative total since startup ("42,000
points lost in the last minute" is something one decides on, "3,200,000 since
startup" is something one endures), and it carries the **sustainable
throughput**, because the first question of whoever reads it is "how much can I
take?". Damped to one line per minute: a saturation lasts, and a line every two
seconds would drown the log instead of informing it. The very first loss, though,
is reported immediately: damping must prevent a symptom from repeating, not delay
its appearance.

**What the buffer covers.** With throughput settled, the buffer no longer serves
to absorb a permanent deficit: it serves to cover a **tunnel outage**, which is
what it was written for (§2). Its size — 150,000 samples — therefore reads as a
duration of unavailability:

| Proxy fleet | Outage absorbed |
|---|---|
| at the throughput ceiling (133 devices) | 25 s |
| 20 48-port devices | 2 min 46 s |
| 12 48-port devices | 4 min 37 s |

The upper bound is not a number of samples, it is the **proxy's memory**, which
runs on a minimal VM or a Raspberry Pi ([[Proxy]]): measured, a sample takes 401
bytes on the heap — most of it being its label map, not its value. 150,000 points
therefore cap out at **~57 MiB**, and only during an outage. Doubling the capacity
would double that figure: **that is where the trade-off is**, not in the number of
samples.

> [!warning] Two ceilings bound a batch, and the telemetry route has its own
> This route decodes its body behind an **8 MiB** ceiling
> (`maxProxyMetricsBody`, `internal/api/proxymetrics_handlers.go`), whereas **the
> rest of the API** keeps the generic **1 MiB** ceiling (`internal/api.maxBody`,
> used by `decode`). It is not a privilege, it is a difference in nature: the
> generic ceiling is cut for what this API really receives — forms, credentials,
> VLAN lists — and a route whose payload **is a batch** is not tuned like a form.
> Its size is not an accident to bound out of caution, it is the throughput
> parameter itself.
>
> **What the old setting cost.** The route advertised a safeguard of **20,000**
> samples per batch (`maxSamplesPerBatch`) while reading its body with the generic
> 1 MiB decoder, i.e. ~5000 samples: the advertised safeguard was
> **unreachable**, one could not submit to it what it claimed to accept. And
> truncation answered in its place — syntax error in the decoder, **silent 400**
> to the proxy, perpetual replay (see *The failure mode*, below).
>
> **Loosen rather than lower the advertised number**, and it is not for comfort:
> a ceiling in **count** cannot be made exact by a ceiling in **bytes** it does
> not know, since a sample's weight depends on a port name the operator chooses.
> Lowering to ~4000 would have **moved the lie** without removing it, and
> sacrificed 75% of the advertised capacity to cover the worst naming case.
> Lowering would also have **refused** a proxy of another version that read
> "20,000" and sticks to it — that is, exactly the proxy that was getting stuck.
> Loosening unblocks it instead of opposing it a better-worded refusal.
>
> **Why 8 MiB and not something else.** The byte ceiling is derived from the count
> ceiling, in that direction and not the reverse: 20,000 × `worstBytesPerSample`
> (320 B, see below) = 6.1 MiB, and 8 MiB leaves 31% above. At the actually
> measured weight (229 B), a full batch weighs 4.4 MiB, i.e. 54% of the ceiling.
>
> The price is in **transient memory**: 20,000 decoded samples, each with its
> label map, hold about ten MB for the duration of a request. That is the price of
> the batch the route says it accepts, and it is precisely what
> `maxSamplesPerBatch` bounds. 8 MiB is not a new pattern in this code either: it
> is already the ceiling of the site configuration import.

**What a sample weighs: measured, with a single free variable.** The figure is not
an estimate — it comes from `snmppoll.Samples` on actually produced samples (52
ports, device identifier as a UUID), with their `device`, `ifindex`, `port`,
`iftype` labels, a metric name such as `meshcraft_if_in_octets_total`, an RFC 3339
timestamp **with its nanoseconds** and the POST's authentication envelope:

| Measured case | Weight of one counter in JSON |
|---|---|
| port with factory naming ("GE12") | **196 B** |
| port renamed by the operator ("Server room - rack B12 - uplink 12", 37 chars) | **229 B** |

It is the **counters** that size a batch, and that is a direct consequence of the
cadences in §1.2: `if_counters` is read every **5 s** while `if_state` is read
every **30 s**, so a batch is made of counters almost exclusively — and a counter
is the heaviest of a port's six samples, thirteen digits of value against one for
a state. Sizing on the average of the two metrics would underestimate every real
batch.

> [!important] The port name is THE whole variable — that is what to remember
> An older figure of **208 B** circulated, including on this page. It was not
> wrong, it was **dated**: it came from a prototype that named its ports
> "GigabitEthernet52", and the twelve bytes of difference with the factory
> naming's 196 B are exactly the difference in label length. Nothing else moves
> from one case to the other.
>
> Yet this label is **typed by the operator** and **no code bounds it**. The two
> values above are therefore landmarks, not invariants: what to remember is that
> the only unknown in sizing a batch is a character string the product does not
> control. Hence `worstBytesPerSample = 320`, which leaves 40% beyond the measured
> long case — room for a 128-character label — and the `TestSNMPSampleWeight`
> test, which locks it: the day a sample's shape grows, a test says so, not a
> proxy stuck in production.
>
> Not to be confused: the **208 samples** in the production table above are a
> number of points per poll of a 52-port switch (52 × 4). The coincidence with the
> old 208 B is fortuitous, and it has cost enough rereadings to deserve mention.

**The `maxPerFlush` wall has changed sides.** There are two walls, and the lower
one is what counts:

| Wall | Value | Maximum batch it allows |
|---|---|---|
| route body ceiling | 8 MiB | ~**36,000** samples at the long weight (229 B) |
| count safeguard | **20,000** samples | 20,000 |

As long as the route used the generic 1 MiB decoder, the **bytes** bit first: the
old batch of 5000 weighed 1015 KiB, i.e. **99%** of the ceiling — a block at the
first slightly longer label. Since the loosening, the wall is the **count
safeguard**, and it is far off. The batch is set at **3000**, i.e. **15% of the
wall** — and 8% of the body ceiling, a full batch at the long weight weighing
~672 KiB. The test no longer copies this wall, it **computes** it from the weight
it has just measured and fails if `maxPerFlush` exceeds 75% of its value
(`cmd/proxy/snmp_throughput_test.go`): a figure that is right by accident becomes
an opinion again at the first rereading. It is still the **frequency** that
carries throughput, not the batch size.

**The failure mode, repaired — and it is the real subject of this batch.** What
precedes changes a capacity; this changes a **behaviour on failure**, and one must
first understand what the old one cost. `metricbuf.Nack` returns the reserved batch
**to the head of the queue**: the next `Take` therefore picks up exactly the same
one. A batch refused for its **shape** comes back identical on the next flush, and
on all the following ones. It was therefore not a partial loss, it was **the halt
of all reporting** from that proxy: the buffer no longer drains, it overflows from
the top, and it is the most **recent** points — those collection has just
produced — that die, without bound, for a single batch the controller would never
take.

The boundary chosen (`isPermanentRefusal`, `cmd/proxy/snmp.go`):

| Verdict | Cases | What `flush` does with it |
|---|---|---|
| **definitive refusal** — the controller read the request and refuses it for its **shape** | `400`, `413`, `415`, `422` | **abandons** the batch (`Ack`), with a log carrying the number of points **sacrificed**, the batch size in bytes and the **reason returned by the controller** |
| **transient failure** | transport, `5xx`, `429`, `408`, `404`/`405` (route missing) | **keeps** the batch in the buffer (`Nack`) — that is the buffer's reason for being |
| `Retry-After` present | **whatever the code** | **transient**, and it is tested first |

`Retry-After` first, deliberately: a refusal that says *when to come back* is a
refusal waiting for us to come back. That is exactly what a somewhat broad
"`>= 400`" would file on the wrong side — a 429 or a maintenance 503 are requests
for patience, not disagreements about the batch.

The word that counts in the abandonment line is "**abandoned**": it is a number of
data points that will never exist again, and it has no other place to be said. The
controller's reason, for its part, is only read on that path, and that is
deliberate: "3000 points abandoned, status 413" lets one guess which setting is at
fault, whereas the controller's sentence **names** it ("reduce `maxPerFlush`, this
is not a content problem"). On the replay path, the same line would come back
every two seconds for nothing. An abandonment **also stops the burst**, even
though the buffer has moved on: if the refusal is systematic — a controller that
refuses everything this proxy sends — chaining would sacrifice four batches per
wake-up instead of one. *Losing slowly gives the log time to be read.*

> [!important] The list of definitive cases is CLOSED, and the asymmetry explains why
> The two classification errors do not cost the same. Believing **definitive**
> what was transient loses **one batch**: bounded, visible, quantified. Believing
> **transient** what was definitive **halts reporting**: unbounded. What cannot be
> classified is nonetheless treated as **transient** — hence on the expensive side,
> and one must say why this is not an inconsistency. An abandonment is
> **irreversible**: the points are destroyed. A block is **loud and repairable**:
> every refusal has its log line, `reportSaturation` repeats the loss every
> minute, and a deployment or a configuration fix lifts it without having lost
> anything more. Between destroying silently and blocking while shouting, we
> block.
>
> What makes the choice tenable is therefore not the fallback itself but the fact
> that the **transient list is enumerated too**, case by case, rather than left to
> a default: on this route, the refusals the controller actually produces are all
> classified by hand, on both sides. The fallback only covers codes that do not
> occur — and if one of them did, it would block reporting while saying so.

> [!warning] `401` / `403` classified TRANSIENT: the most debatable decision of the batch
> It rests on a fact verifiable in `handleProxyMetrics`: here, the controller
> holds nothing against **the batch** — it has not even looked at it, since
> authentication happens **after** reading the body. And a proxy's authorisation
> is something that **changes**: a secret gets rotated
> ([[Service identity and rotation]]), a revocation gets undone, a restarting
> controller may refuse for a second before accepting. Abandoning would throw away
> points that an up-to-date enrolment would have taken.
>
> **The objection is real, and it is not fixed**: a truly revoked proxy replays
> forever and reports nothing any more. What makes the decision acceptable is not
> that the objection is small, it is that it **cannot be fixed here** — abandoning
> on 403 would not report one more point, it would only erase its trace. The
> buffer is bounded and drops its oldest, `reportSaturation` says so every minute,
> every refusal has its line, and a revoked proxy no longer has a recipient for its
> metrics anyway. Better written here than discovered: if one day a revoked proxy
> must **stop** replaying, the decision will come from enrolment, not from this
> boundary.

> [!note] Why a refusal on SIZE is abandoned TOO
> This is the case where abandonment hurts most: the batch's content is fine, only
> its volume does not pass, and **re-splitting** it would be better than throwing
> it away. The `metricbuf` API does not naturally allow it: `Take` returns the
> reserved batch **as is**, ignoring its argument as long as it is neither
> acknowledged nor returned, and there is neither partial acknowledgement nor
> resizing. The only path would be `Nack` then a smaller `Take` on the next round
> — hence a reduced size to remember, a moment to raise it again, a coexistence
> with the burst to settle: an entire fallback policy for a case the controller
> **no longer produces**, a batch of 3000 weighing ~672 KiB under an 8 MiB ceiling.
> The logged abandonment is enough, and it has the advantage of being unable to
> loop.

> [!note] These are KNOWN limits, not promises
> None of these figures is a commercial guarantee: they say what the shipped
> constants imply, and they change with them — the sustainable throughput is in
> fact **computed** in the code rather than copied, so that a retouched setting
> does not leave the log announcing a capacity that no longer exists. What they
> bring lies elsewhere: a capacity discovered in production is an outage, a
> written capacity is an architectural choice. "This proxy handles 133 48-port
> switches" can be sized; "it should be fine" is found out on a Tuesday
> afternoon, in a graph full of holes.

## 3. Storage: VictoriaMetrics

[[Monitoring]] planned partitioned Postgres "for the MVP". The decision is made:
it is **VictoriaMetrics**, starting now.

The reason is not fashion, it is the shape of the data. The calculation in §2.4
gives it directly: ≈ 45 samples per second per 48-port device, hence **9000 per
second** on a fleet of two hundred devices — on the order of **thirty million
points per hour**. That is a write load Postgres accepts, but makes *everything
else* pay for, including the inventory and the log that live in the same
database. Separating the two prevents a metrics spike from slowing down the
administration screen.

> [!note] The same figure also says how many proxies are needed
> 9000 samples/s exceed what **one** proxy drains (6000/s, §2.4). A fleet of two
> hundred 48-port devices therefore requires **at least two proxies**, not for
> reachability but for reporting throughput. It is a sizing constraint, and it is
> read here rather than discovered in a saturation log.

What stays in Postgres: the inventory, the intents, the [[Event log]] and the
pipelines. **What goes to VictoriaMetrics: time series, and nothing else.** An
event is not a metric — conflating them would yield a log that cannot be reread
and metrics that cannot be aggregated.

- ingestion through the Prometheus protocol (`/api/v1/import/prometheus`), which
  the controller exposes to the proxy;
- default retention 90 days, degradation through `-downsampling`;
- no public exposure: VictoriaMetrics is reachable only by the controller, like
  the database.

## 4. Pushing the collection configuration

An onboarded device must be **configured to be monitored**: read-only SNMPv3 user
or v2c community, trap and syslog targets, gNMI subscription. It is intent like
the rest, hence [[Reconciliation]] — not a special path.

The trap target is **a concrete IP address**, and that is what makes the
collector question unavoidable here — and nowhere else. Two possible answers, and
it is the second one that is built:

| | Election | **Declaration** |
|---|---|---|
| Who decides | the controller, from `device_paths` (fastest valid path) | the **operator**, on the device's page |
| Where it is written | `device_collectors`, materialised from a computation | `devices.proxy_id` ([[Proxy]]) |
| Status | **decided, not built** ([[Monitoring]], § *Choosing the collection point*) | **shipped** — migration `0022` |

The relationship between the two is simple, and it is not invented: the
declaration is **the missing building block of the election**, not its
replacement. It already gives the single address that step 6 of §7 needs, without
the automatic recomputation — that is, without the risk the election carries,
which is to change the collector **on its own** and make wrong a trap destination
already written in the devices, without anyone touching anything. The day the
election exists, it will only be able to **propose** an attachment, never impose
it.

> [!note] Receiving a trap still requires no collector
> The proxy that receives the datagram recognises the source IP in its own watch
> list and resolves the device locally. Attachment changes nothing about this
> reception; it decides **who polls** over SNMP and **which address will be
> written** into the device. That is why step 5 of §7 can precede step 6, and not
> the reverse.

## 5. Pipelines: making visible what is stuck

This is the part that always gets forgotten, and it is the one that decides
whether the product is operable. A configuration deployment, a credential
rollout, a monitoring campaign: all of that **takes time, fails partially, and
gets stuck**. Today, a failing apply leaves an event and nothing else — one has to
guess.

A **pipeline** is therefore a first-class object:

```sql
pipeline(id pk, site_id fk, kind text,        -- config | credential | monitoring
         device_id fk nullable,               -- null = fleet campaign
         state text,                          -- pending|running|blocked|done|failed
         created_at, started_at, ended_at,
         blocked_reason text,                 -- IN PLAIN WORDS, not a code
         created_by fk users)

pipeline_step(id pk, pipeline_id fk, seq int, name text,
              state text, started_at, ended_at,
              detail text,                    -- what the device answered
              transcript jsonb)               -- the full trace
```

Three principles:

1. **`blocked` is a state, not a failure.** A switched-off device, a disconnected
   proxy, a maintenance window not yet open: the pipeline waits, and says what
   for. Confusing it with `failed` pushes people to relaunch what does not need
   to be.
2. **The reason is a sentence**, not a code. "Waiting: proxy `proxy-festival` has
   been disconnected for 4 min" can be read; `ERR_PROXY_DOWN` has to be looked up.
3. **The transcript is kept**, as for the connectors' web sessions. On a range
   where a mistake costs a trip on site, knowing *after the fact* what was sent is
   the only way to understand without going back on site.

The UI follows from this: a **Pipelines** view filterable by state, where
`blocked` and `failed` rise to the top. The question "what is stuck, and why?"
must be answered in one screen, without reading a log.

## 6. Debug area: the application's live log

Distinct from the [[Event log]], and the two must not be confused:

| | [[Event log]] | Debug area |
|---|---|---|
| Subject | the FLEET | the APPLICATION |
| Audience | the operator | whoever is diagnosing meshcraft |
| Example | "port 12 down" | "instanton connector: HTTP 200 with no status code" |
| Retention | long, in the database | short, in memory |

Concretely: an in-memory ring (a few thousand lines) fed by `slog`, exposed as a
real-time stream to the `global-admin`, filterable by level and by component.
Three requirements:

- **in memory, not in the database.** A debug log that writes to the database adds
  load at the precise moment something is going wrong;
- **never a secret.** The stream goes through the same filter as the transcripts:
  a password that passes through a debug log is a leaked password;
- **reserved to the `global-admin`** ([[RBAC]]). Internal errors describe the
  product's structure; that is not operator information.

## 7. Delivery order

None of this switches on at once, and the order is not indifferent — each stage
needs the previous one to be verifiable:

1. **VictoriaMetrics** deployed and reachable, with no producer: we check the
   storage before writing to it;
2. **the monitoring capability** declared by the connectors: without it, any
   scheduling would be guesswork;
3. **the pipelines**, with the existing configuration writes as first clients:
   that is what makes the rest diagnosable;
4. **the debug area**, which becomes useful as soon as there are pipelines to
   observe;
5. **proxy collection** (SNMP, then traps, then streaming), one channel at a time,
   each verified on a real device before the next. Traps come **after** polling,
   and not for convenience: without polling there would be nothing to speed up,
   and a listener delivered before it would have no source of state to bring
   forward (§2.3);
6. **pushing the collection configuration**, last: it is a write to the fleet,
   and it must only go out once we already know how to read. Until it is there,
   the trap listener is functional but **silent on most fleets** — only devices
   already configured by hand point to a proxy. A channel that receives nothing is
   not a broken channel: it is step 6 that is missing, and the `trigger` field is
   what makes it possible to tell the difference.
