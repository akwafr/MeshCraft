---
Status: Accepted
Date: 2026-10-25
---

# ADR-0018 — A port's name at write time is an observation, not a constant

## Context

The `fs-s3400` connector translates in both directions between meshcraft's standard
model (`GE1..GE48`, `XG1..XG4`) and the names the CLI speaks:

- **on read**, `Driver.StandardPortName`: `TGigaEthernet0/51` → `XG3`;
- **on write**, `cliPortName`: `XG3` → `interface tg0/3`.

Two builds of the **same** 2.0.2J firmware are in service, and they do not number
the SFP+ cages the same way. The name is identical (`TGigaEthernet0/N`), only the N
moves: Build 95262 (the lab) restarts the series at 1, Build 130907 (`sw01-arena`,
production, ARENA site) continues it after the copper and numbers them **49 to 52**.

The fix of 2026-10-25 (`sfpCageNumber`) taught **reading** to accept both
conventions, and made the word "Series" optional in the banner — which reopens
`looksLikeS3400CLI`, hence **reopens writes** on this device. Writing, however, has
not moved: `cliPortName` still emits `tg0/1..4`.

Three facts follow one another, and it is the sequence that decides this ADR.

1. **A readable refusal has become a late failure.** Before, every write on
   `sw01-arena` was refused by `requireWritable` with a sentence that said what to
   do. Now it is allowed, and `interface tg0/1` goes out to a chassis that has no
   `tg0/1`. Functionally it is progress — SNMP writes go through, and the site's
   monitoring depends on them. In readability it is a regression: the diagnosis goes
   from a sentence to a CLI message.

2. **The message obtained blames the wrong thing.** `ApplyPortAdmin` words its
   failure like this: "`shutdown` under `interface g0/N` was accepted by hand on the
   lab's S3400 on 2026-08-10, so a refusal here means that THIS device's firmware
   differs and its forms must be captured". The command form is right; it is the
   **name** that is invented. That is exactly the fourth defect of the previous fix,
   the one that cost an hour: a refusal that states an observation never made.

3. **The configuration block does not stop at the refused line.**
   `Client.configBlock` sends all its lines and collects the failures; it is not an
   oversight, it is what makes it possible to attribute a refusal to the line that
   earned it. But if `interface tg0/1` is refused, the prompt stays
   `Switch_config#` and the following lines — `switchport mode access`, `shutdown` —
   are typed **in the enclosing context** instead of on the port. What this CLI does
   with a global `shutdown` has not been captured, and that is precisely the reason
   not to discover it on a production device.

`StandardPortName` is a method of `Driver`, which is unique for the whole fleet: it
has no session context and cannot know which convention the device on the other end
carries. The information, on the other hand, **is already paid for**: the
`show interface`, the `show vlan` and the `show running-config` that every write path
reads **before** writing all speak the device's cage names.

## Decision

**The name we write comes from what the session has observed, and a session that has
observed nothing refuses to name a cage.**

1. **The state lives on `Client`**, next to `host`, `facts` and `unknownWhy`: a
   `Client` is **one** SSH session open on **one** host, which is exactly the scope of
   the fact. The field is a type, `cageNumbering`, not an integer: it carries its own
   `known`, so "not yet observed" cannot be represented by a zero that would be
   confused with the `tg0/1..4` convention.

2. **The observation is free and it is not sought.** `noteCageNumbering` is called on
   the output of the reads the connector already pays for, at the place where those
   same outputs are parsed to find port names in them: both forms of `readLinks`,
   `ReadState`'s `show running-config` and `show vlan`, `portGroups`'s
   `show interface | include protocol`, `readStormControl`'s `show running-config`,
   `ReadSpanningTree`'s `show spanning-tree` and its `show running-config`. No round
   trip is added.

   The choice of these points is not arbitrary: **it is the set of pre-reads of the
   four write paths**. `ApplyPorts` and `ApplyPortAdmin` read `ReadState`,
   `ApplyStormControl` reads `readStormControl`, `ApplySpanningTree` reads
   `ReadSpanningTree`. No port loop is reachable without an observation having taken
   place, and it is that property that is tested.

3. **The observation votes, and a contradiction elects nobody.**
   `observeCageNumbering` captures every `tg*0/N` in an output, passes them through
   `sfpCageNumber` — the **same** window as reading, not a second definition — and
   keeps the offset `N − cage`. If two different offsets appear in the same output,
   the result is "unknown": a device that spoke both conventions is not a device whose
   numbering we guess. The first **known** observation wins and is not replaced: the
   device does not change build during a session, and a stable value is a value a
   transcript can be re-read against.

4. **A read that has no pre-read pays for one itself.** `ensureCageNumbering`
   establishes the numbering in **one** read when the session has seen nothing. A
   single caller justifies it: the optics sampler (`internal/api/opticssample.go`)
   opens a session and calls `ReadOptics` **alone**, and `ReadOptics` is made of cage
   names. The four write paths do not call it, because it would be a round trip that
   can never fire — and because a write is not the place to insert ninety seconds of
   unexpected pager.

   The command is `show interface | include protocol`, the one that **names** the
   sockets, and not a per-cage probe: `show interface tg0/1` carries the command's
   **echo**, so a device that refuses it still returns the string "tg0/1" — a session
   that would read its own question as the answer.

5. **Unknown convention: a write targeting a cage is refused, and the refusal says
   what to do.** `cageNumbering.cliPortName` returns an error rather than a name. The
   refusal names both conventions, the build where each was captured, and the gesture
   that lifts it (read the state once). It arrives **before** `configBlock` opens
   anything, so no line goes out in the wrong context.

   **Copper is not concerned**: `g0/N` is identical under both conventions. The
   refusal therefore applies to the four cages, never to the forty-eight copper ports,
   and a session that has observed nothing still writes copper.

6. **The refusal is not a `driver.NotSupported`.** That one is wrapped in
   `ErrNotImplemented` and means "this platform cannot do it" — final, nothing to
   retry. Here the connector knows perfectly well how to do it, it simply has not
   looked yet. Classifying a temporary ignorance as an absent capability is lying to
   the caller about repairability.

7. **The silkscreen is only asserted if it has been observed.** The `XG` group of
   `portGroups` only carries `"silk": "tg0/"` when the session has seen the
   `tg0/1..4` convention. Under the `49..52` convention — and when nothing has been
   observed — the field is **absent**, and the faceplate falls back to the standard
   name (`XG1`). The reason is mechanical: the frontend composes `silk + k` where `k`
   is the **standard** index (`web/src/network.ts`, `expandPorts`), so a prefix alone
   cannot express an offset of 48. Between showing `tg0/1`, a name the device refuses
   if the operator copies it into their CLI, and showing `XG1`, the name meshcraft uses
   everywhere else, the second asserts nothing false.

## Consequences

- **A write path that had no pre-read would refuse the four cages.** That is the
  price, and it is deliberately paid in this direction: the default of forgetting is a
  refusal with a sentence, not an invented command. A new write gesture that reads
  nothing will discover it at the first attempt on a cage, and it has two ways out —
  read the state, which all existing gestures already do, or call
  `ensureCageNumbering` as `ReadOptics` does.
- **`ReadOptics` pays one more read when it arrives alone.** That is the cost of point
  4, and it falls only on the optics sampler, which had no pre-read. In exchange, on a
  `49..52` chassis, it finally brings something back: before, its four commands
  queried four non-existent interfaces and the site whose only optics are its uplinks
  reported no DDM. Convention impossible to establish nonetheless: a `failures` line in
  its notes, in the per-cage mechanism that already existed.
- **A `49..52` chassis's faceplate loses its silkscreen** and shows `XG1..XG4`.
  Expressing `tg0/49..52` on screen would require an offset field in the `PortGroup`
  contract, shared by all connectors. That is not done here (cf. the alternatives), so
  this site's operator reads a standard name where the lab's operator reads the
  silkscreen. Accepted asymmetry.
- **The latitude of the constants is not closed.** `sfpCageNumber` still bounds on
  `copperPorts = 48` / `sfpPorts = 4` and not on the counts deduced from the model: on
  an S3400-**24**T4SP whose cages were numbered 25..28, the second window does not
  open. No capture says what that model does, and widening it blindly would invent
  inventory. What this decision changes is the **consequence** of that latitude: the
  observation goes through `sfpCageNumber`, so a numbering that reading refuses stays
  unobservable, so a write to a cage is **refused with a sentence** instead of going
  out on `tg0/1`. The gap is the same, its cost is no longer the same — and the day
  `sfpCageNumber` derives its windows from the model, both sides will follow together
  because there is only one definition.
- **A `Client` is not shared between devices.** Nothing did so, and nothing must start
  to: the convention remembered on a session would then be that of the last device
  read. That is the reason this field is not on `Driver`.

## Rejected alternatives

**Guess `tg0/1` and let the CLI decide.** The repository's current state, and thus the
default option. Rejected for reason 3 of the context: the refusal of the `interface`
line does not stop the block, and the following lines run in the enclosing context.
The cost of the bet is not "an error message", it is "a gesture whose effect we cannot
say". On top of that comes the message that blames the command form instead of the
name.

**Try both names and keep the one the CLI accepts.** Tempting: the session learns from
failure, without a pre-read. Rejected because a write is not a probe.
`interface tg0/1` on a `49..52` chassis opens a block that fails, and the next attempt
is made from a prompt whose state we no longer know. Probing with a **read** would be
acceptable, but the read that answers the question has already been done — paying a
round trip to ask again what was read thirty seconds earlier has no justification on a
platform whose pager costs ninety seconds.

**Put the convention on `Driver` and let it fill in at the first read.** One field
fewer to pass around. Rejected without hesitation: `Driver` is unique for the fleet, so
two S3400s of different builds would share the fact. Writing on one would depend on
which was read last — a non-reproducible, order-dependent bug, the worst category on a
write path.

**Make `cliPortName` a method of `*Client`.** The shortest. Rejected because the
command builders — `portConfigCmds`, `portAdminCmds`, `stormPortCmds` — are **pure**,
and that is what makes it possible to pin the exact wire form in a test with no session
or device. The parameter is therefore the **fact** (`cageNumbering`), not the session:
purity holds, and the convention becomes a test input like any other.

**Deduce the convention from the build read in the banner** (`Build 130907` ⇒
`49..52`). Rejected: it would be a table of builds to keep up to date against a
catalogue we do not control, and the third build would arrive without being in it. The
device **speaks** its numbering at every read; deducing what one can observe means
replacing a measurement with an assumption — the same mistake as having a decision
rest on the word "Series".

**Observe in `cliConn.runPaged`**, the bottleneck through which all output passes, so
that no read can be forgotten. It was the option most in line with ADR-0017's doctrine
("the rule goes where it cannot be bypassed") and it was seriously considered. Rejected
for a precise reason: **writes** go through the same bottleneck, and the CLI echoes the
command sent. A session would then start observing itself — `interface tg0/1` written
then read back as proof that the device numbers from 1. Safety would rest on "we never
emit a cage name before knowing it", an invariant true today and invisible from the
transport.

**Add `silk_from` to the `PortGroup` contract** to show `tg0/49..52`. Rejected here,
not refuted: it is the right shape if the question comes up again. It touches a
contract shared by all connectors, the frontend and its label tests, for a
**cosmetic** gain on a single build of a single model. The lie is removed now; its
exact replacement awaits a broader reason than this one.
