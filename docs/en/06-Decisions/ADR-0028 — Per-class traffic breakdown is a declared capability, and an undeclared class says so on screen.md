---
Status: Accepted
Date: 2026-10-25
---

# ADR-0028 — Per-class traffic breakdown is a declared capability, and an undeclared class says so on screen

## Context

The request is a **packets per second** chart on the port overlay,
"showing on several lines summing up multicast, broadcast and unknown
unicast". It is the **observation** counterpart of the storm limiter: the three
classes requested are exactly those `StormClass` polices
(`StormBroadcast`, `StormMulticast`, `StormUnknownUnicast`), and the header of
`wire/mdpv1/stormcontrol.go` already explicitly defers observed counters
"to a monitoring path". That path did not exist. ADR-0020 left it
open by refusing to model the `Drops` column of the Arista `show storm-control`
for the same reason.

Before stacking three series, we had to establish which ones exist.

### What the series vocabulary carries today

`wire/metrics/names.go` knows **no** per-class breakdown. Per
port, it carries `meshcraft_if_in_packets_total` / `out`, octets, errors
and discards — and nothing else. Verified on the storage side on 2026-10-25:
VictoriaMetrics contains 19 series names, none of which names a class.

### What each collection path actually returns

Two paths produce per-port counters, and they do not return the same
thing.

**gNMI (Arista EOS) — read-only survey on `core01-arena`
(192.0.2.1), 2026-10-25.** `internal/gnmipoll` subscribes to the **container**
`/interfaces/interface[name=*]/state/counters`, so everything the device
serves already arrives there. The raw survey gives fifteen leaves, and here they are
in full:

| leaf served | today | class |
|---|---|---|
| `in-octets`, `out-octets` | mapped | — |
| `in-pkts`, `out-pkts` | mapped | total |
| `in-errors`, `out-errors`, `in-discards`, `out-discards` | mapped | — |
| `in-fcs-errors` | **ignored** | — |
| `in-multicast-pkts`, `out-multicast-pkts` | **ignored** | **multicast** |
| `in-broadcast-pkts`, `out-broadcast-pkts` | **ignored** | **broadcast** |
| `in-unicast-pkts`, `out-unicast-pkts` | **ignored** | **total** unicast |

Multicast and broadcast are therefore **already on the wire**, paid for, and thrown away by the
default branch of `counterLeaves` in `leafSample`. Mapping them does not cost the device
a single extra byte.

**Unknown unicast is not there.** Not under another name, not elsewhere: neither
`in-unknown-protos`, nor anything close. The fifteen leaves above
are the complete list. And `in-unicast-pkts` does not give it by subtraction:
it is **total** unicast, known and flooded together, and the flooded share cannot
be derived from it.

**SNMP (twelve Aruba Instant On 1930, FS S3400).** `internal/snmppoll` reads eight
OIDs, including `ifHCInOctets` (`1.3.6.1.2.1.31.1.1.1.6`) and `ifHighSpeed` (`.15`) —
hence **from the ifXTable, and it works on these devices** (their octet
series are populated with `ifindex`). The four per-class counters of the
same table are not polled:

| OID | object | read? |
|---|---|---|
| `1.3.6.1.2.1.31.1.1.1.2` | `ifInMulticastPkts` | no |
| `1.3.6.1.2.1.31.1.1.1.3` | `ifInBroadcastPkts` | no |
| `1.3.6.1.2.1.31.1.1.1.4` | `ifOutMulticastPkts` | no |
| `1.3.6.1.2.1.31.1.1.1.5` | `ifOutBroadcastPkts` | no |

Unknown unicast, for its part, **is not in the standard MIB**. `ifInUnknownProtos`
(`1.3.6.1.2.1.2.2.1.15`) is another object — frames discarded for an unsupported
protocol, not flooded unicast — and confusing it with the class storm
control polices would be precisely inventing the data.

**The only unknown unicast counter observed on the fleet** is the `Drops` column
of the Arista `show storm-control`, quoted verbatim in ADR-0020. It is not the
same quantity: these are frames **dropped by the limiter**, not forwarded
frames. Stacking it with two forwarded rates would produce a total that is
the total of nothing.

## Decision

**1. Two real classes, not three of which one is invented.** The vocabulary gains
four series — `meshcraft_if_in_multicast_packets_total`,
`…_in_broadcast_packets_total` and their `out` twins. Unknown unicast
**gets no series** as long as no platform can produce it: a
series name without a producer is a promise the graph would keep by
painting zero.

**2. The breakdown is a declared capability, PER CLASS.** Three flags
in `Capabilities.Features`, following the `FeatMSTRegion` pattern:
`FeatTrafficClassMulticast`, `FeatTrafficClassBroadcast`,
`FeatTrafficClassUnknownUnicast`. One flag per class and not a global "can
break down", because a platform may very well return multicast and
broadcast from the ifXTable and not unknown unicast — that is the case of the **whole**
fleet. A single capability would therefore be wrong everywhere, and wrong in the
dangerous direction: it would promise more than the platform delivers.

It is the lesson of ADR-0022, identically: nothing distinguished "can write
STP" from "can join a region", and a clean `apply` left
`sw01-arena` outside its site's tree. Here, nothing would distinguish "collection
is young" from "this class will never exist here".

**3. An undeclared class says so on screen, it is not just missing.** This is the
heart of the decision. The graph must distinguish three states that nothing
distinguished:

| situation | what the screen says |
|---|---|
| class declared, samples present | the band, stacked |
| class declared, empty series | "not observed yet" — transient |
| class **not declared** | "this platform does not report it" — permanent |

Silently omitting the band of an undeclared class would render a stack top
that reads as a total when it is not one, and nobody
could know. It is the failure ADR-0020 fixed on storm control,
where an unread line meant "port not protected".

**4. Where the capability is proven, and where it is inferred.** ADR-0022 was
declared on the Arista alone and nearly hid a button on four Instant Ons;
the instruction that comes out of it is to say what we rely on.

| connector | multicast / broadcast | on what basis |
|---|---|---|
| `arista/eos` | declared | **proven** — the four leaves observed on `core01-arena` on 2026-10-25 |
| platforms surveyed over SNMP | declared | **inferred** — the ifXTable already answers on these devices (`.6`, `.15` populate their octet series); the four per-class counters are from the same table. **Not verified on the device**: SNMP is UDP and the lab route is a TCP SOCKS5, so no `walk` could be attempted from this host. |
| all | unknown unicast | **declared nowhere** — no known source |

## Consequences

- Four series in the vocabulary, mapped in `internal/gnmipoll/translate.go`
  (free, the subscription exists) and four OIDs added to `internal/snmppoll`.
  The latter cost is real and is paid on every cycle on every device:
  the poller's header says so, and that is why only four OIDs are added and
  not the whole ifXTable.
- The graph displays **inbound**, and both directions are collected. What the
  stack answers is "what share of what this port RECEIVES is flooding"
  — and it is ingress that the storm limiter polices. The outbound series
  exist and answer the other question, "is this switch the SOURCE",
  but putting both directions on a single stack would double each band and make
  the proportion unreadable, which is precisely what one comes to read there.
- The graph is **stacked** and reads in packets per second. Counters are
  derived **server-side** by `rate()`, like the whole `_total` family of this
  product: VictoriaMetrics absorbs a restart's reset to zero, and
  `round3` already crushes the residual negative a badly re-read counter would produce.
  That is what makes the case of `core01-arena` (three restarts in its
  history) and `sw01-arena` (clock at `2000-1-1`) harmless to the stack.
  No derivation goes down into the browser.
- The time axis goes through `viewerDate` / `ageMs` (ADR-0008). These samples
  are timestamped **by the relay**, not by the controller, so the gap is
  unbounded and a raw `new Date(ts)` would display a time that is nobody's.
- Per-class `Drops` stays outside the contract, as ADR-0020 decided. If a
  second platform can count **forwarded** unknown unicast, the class
  gets its series and its flag, and the graph goes from two bands to three
  without changing anything on screen — that is the whole point of having declared per class.

## Rejected alternatives

**Inferring unknown unicast from `in-unicast-pkts` minus known unicast.** There
is no known-unicast counter. The subtraction has no second term,
and inventing it from the FDB table would give a number that varies with
the age of the entries rather than with traffic.

**Stacking storm control's `Drops` column as a third band.** Two
incompatible quantities in a stack whose top reads as a total:
forwarded frames and dropped frames. The top would be the total of
nothing. ADR-0020 has already put these counters elsewhere.

**A single "can break down traffic" flag.** Wrong on the whole of the
current fleet, since no platform delivers all three classes. It
would promise unknown unicast wherever it promises broadcast.

**Declaring nothing and inferring the capability from the absence of samples.** That is
exactly what this decision refuses, and the reason fits in one sentence: a
gap reads as a dip in traffic. A collection that has just started and a
platform that will never know produce the same emptiness.

**Omitting the unknown unicast band.** The graph would show a stack of
two bands indistinguishable from a complete stack, on a request that named
three. The reader would conclude there is no unknown unicast on this port,
whereas the truth is that we know nothing about it.
