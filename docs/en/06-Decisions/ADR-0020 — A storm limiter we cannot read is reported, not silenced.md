---
Status: Accepted
Date: 2026-10-25
---

# ADR-0020 — A storm limiter we cannot read is reported, not silenced

## Context

Storm control **already exists per interface** in meshcraft, and it carries its unit:
since 2026-08, `wire/mdpv1/stormcontrol.go` rules that `StormLimit` always carries an
explicit `StormRateUnit`, and that **no connector converts** — because
`percent → pps` requires a frame size and `percent → bps` a line rate that changes
under the configuration. Three classes (`broadcast`, `multicast`, `unknown_unicast`),
three units (`percent`, `pps`, `bps`), nine connectors expressing what they can of it.

On 2026-10-25, a **read-only** survey on `core01-arena` (DCS-7050SX-64-R, EOS
4.28.13M, production core of the ARENA site) asked the device for its own grammar
rather than a documentation. `show cli commands all-modes` states it in nineteen
lines; here are the ones that matter, verbatim:

```
IntfConfigMode: [no|default] storm-control all             level ( LEVEL | ( pps PPS ) | ( rate RATE RATE_UNIT ) )
IntfConfigMode: [no|default] storm-control broadcast       level ( LEVEL | ( pps PPS ) | ( rate RATE RATE_UNIT ) )
IntfConfigMode: [no|default] storm-control multicast       level ( LEVEL | ( pps PPS ) | ( rate RATE RATE_UNIT ) )
IntfConfigMode: [no|default] storm-control unknown-unicast level ( LEVEL | ( pps PPS ) | ( rate RATE RATE_UNIT ) )
IntfConfigMode: [no|default] logging event stormcontrol discards [ use-global ]
GlobalConfigMode: [no|default] storm-control bum aggregate traffic-class TRAFFIC_CLASS level pps PPS
GlobalConfigMode: [no|default] storm-control burst time TIME UNITS
```

Four facts come out of it, and they do not carry the same weight.

1. **There is a fourth per-interface class, `all`.** `show running-config all`
   returns it on each of the chassis's sixty interfaces (`no storm-control all`),
   next to the three others. It is not the union of the three: it polices **the
   whole** frame, established unicast included. No other platform of the fleet has
   it — neither Cisco IOS, nor FASTPATH, nor FS, nor ProCurve, nor RouterOS, nor
   SwOS.

2. **The unit is not only a percentage.** The `arista/eos` connector declares
   `percent` and **only** `percent`, and its header explains that the `pps` and
   `kbps` variants "exist on some ASICs of the range and not on others". The grammar
   of **this** device lists the three forms unconditionally: `level LEVEL`,
   `level pps PPS`, `level rate RATE RATE_UNIT`. The connector's caution on
   **writing** remains justified — the grammar states what the parser accepts, not
   what the forwarding agent programs, and `show platform trident storm-control`
   shows that the chip is programmed in **kbps** whatever was typed. But this caution
   was applied to **reading**, and there it protects against nothing.

3. **Reading silently ignores what it does not understand, and that is a defect.**
   `reStormLine` is anchored:

   ```
   ^\s*(no\s+)?storm-control\s+(broadcast|multicast|unknown-unicast)(?:\s+level\s+([0-9.]+))?\s*$
   ```

   `storm-control broadcast level pps 100` does not match — `[0-9.]+` does not eat
   `pps`, and `\s*$` does not eat the rest. The line is therefore skipped. Yet
   `parseStormControl` then applies the rule "silence in a block we read **is** the
   absence of a limiter" and returns the class as `Enabled: false`. **A port limited to
   100 packets/s is reported as unprotected**, and `storm-control all level 5` is too.
   It is exactly the lie this file was written to prevent, in the place nobody had
   looked for it: the silence rule is right, its domain of application was too broad.

   The same hole exists in `cisco/ios`, which already works around it by hand: its
   parser collects the ports carrying `storm-control unicast` into a local
   `[]string` and returns them as `StormControlState.Notes`, in prose. The constraint
   is therefore shared by at least two connectors, and each solved it in its own
   corner.

4. **The state is much richer than the configuration, and it states its unit.**
   `show storm-control` is a **state** command, and it exists:

   ```
   Port       Type             Level    Units Rate(Mbps) Status        Drops Reason
   Et22       unknown-unicast  0.01     %     1          active       235275
              broadcast        0.01     %     1          active            0
              multicast        0.01     %     1          active            0
   ```

   The device names the **unit** itself (`Units` column), resolves the percentage into
   an absolute rate against the port's nominal speed (`Rate(Mbps)` — 0.01 % of 10 G =
   1 Mbit/s), counts dropped frames (`Drops`) and says whether the configured limiter
   is actually **in force** (`Status`, and `reason` when it is not; in JSON, `active`
   per port and `dormant` per class). None of this has an equivalent in
   `StormControlPortState`, which only carries `{Class, Enabled, Rate, Unit}`.

   A trap hides in it: the `| json` output of this same command returns `"level": 1`
   where the text returns `0.01` and where the configuration says `0.01`. The ten
   policed ports of the chassis all carry the same value, so the scale of this field —
   hundredths of a percent, or a copy of the `rate` in Mbit/s, both worth 1 here —
   **cannot be settled without writing**. The text and the configuration, on the other
   hand, agree.

## Decision

**A `storm-control` instruction that is observed and not understood becomes contract
data; the rule "silence means absence" only applies to a block all of whose
instructions were understood.**

1. **`StormControlPortState` gains `Foreign []StormForeignLimit`.** Each entry
   carries the targeted class when there is one (`Class` empty when the platform names
   something no class covers), the device's **verbatim line**, and a sentence saying
   why it is not modelled. It is a third state, and it was missing: until now "class
   absent from `Limits`" meant "not read" and "present with `Enabled: false`" meant "no
   limiter". There was no way to say "there is a limiter here and I will not pretend
   to understand it".

2. **A parser never skips a `storm-control` line.** What it cannot reduce to a
   `StormLimit` goes into `Foreign`. The property therefore does not depend on the list
   of forms known today: an EOS variant this survey did not see will report itself
   instead of disappearing.

3. **The `all` class does not enter the contract.** It is specific to EOS, and above
   all **it is not the union of the three**: an operator reading "all" as "the three
   together" would be wrong about established unicast. It is reported in `Foreign` with
   an empty `Class`.

4. **The `logging event stormcontrol discards` option does not enter the contract.**
   Specific to EOS, and it is a **logging** option, not a limiter.

5. **Reading a unit does not oblige writing it.** `level pps PPS` is read as
   `StormRatePPS` — the unit already exists in the contract, and reporting "100
   packets/s" is the truth. `StormControlCaps.Units` stays `percent` alone, because it
   answers another question: what the connector agrees to **write** without
   converting. The two surfaces are separate and may legitimately differ.

6. **`level rate RATE RATE_UNIT` is `Foreign`.** The `RATE_UNIT` token is not expanded
   by `show cli commands`, and the only way to know its vocabulary is to write one.
   Reporting a rate without its unit would be this file's founding fault; reporting a
   guessed unit would be the same fault with aplomb. The line therefore goes out
   verbatim, on screen, for a human.

7. **A write does not replace a foreign limiter silently.** `ApplyStormControl`
   consulted the read-back state to know whether it had something to do; a `Foreign`
   class is not an absent class, so the gesture runs when the plan names that class —
   the operator asked for it — but `ApplyResult.Notes` says which line was replaced.

## Consequences

- **`Foreign` is a field eight connectors out of nine will leave empty**, and that is
  intended: it does not describe a platform capability, it describes an encounter.
  Filling it is the parser's job, not the capability table's — there is therefore
  nothing to add to `StormControlCaps`.
- **The screen must display `Foreign`, otherwise the field is useless.** A port
  carrying `storm-control all level 5` stops being presented as unprotected; in
  exchange, the storm control section of an EOS device can now show a line meshcraft
  cannot edit. That is the price, and it is paid in the right direction: a greyed-out
  control with the device's line beside it is better than a screen asserting "no
  limiter".
- **The survey did not establish the exact form `show running-config` gives the `pps`
  and `rate` variants.** Obtaining it would require configuring them, hence writing on
  a production core. The parser is written against the grammar the device stated, and
  rule 2 means that getting the rendering wrong costs a line in `Foreign` instead of a
  port falsely declared free.
- **`Drops`, `Status`/`dormant`, `Rate(Mbps)` remain outside the contract.** Counters
  are telemetry and the header of `stormcontrol.go` already sends them there.
  "Configured but dormant" would deserve to exist — it is the difference between a port
  the screen says is protected and a port that is — but no other platform of the fleet
  was surveyed on it, and a field for which we do not know whether a single other
  connector can fill it is a field that will wait until it has two carriers. Noted, not
  modelled.
- **The `| json` output of `show storm-control` must not be read** as long as the scale
  of its `level` field has not been settled. The connector keeps reading
  `show running-config`, which costs no extra round trip since the apply already
  rereads it.

## Rejected alternatives

**Adding `StormAll` as a fourth class.** The naive reading of the request, and the most
tempting since EOS names it next to the three others. Rejected twice. First because
only one connector out of nine could express it: the checkbox would appear on every
screen and be refused everywhere except on the Arista, which is precisely the defect
`StormControlCaps` was created to avoid. Then, and this is the decisive reason, because
`all` does not mean what its name suggests: it polices unicast that has reached its
destination. An operator ticking "all" believing they tick the three gets a rate limit
nobody asked for — the same defect, word for word, as the one that makes `cisco/ios`
refuse `storm-control unicast`.

**Reading `level pps 100` as 100 % so as not to lose the line.** Never phrased that
way, but it is what a regex widened to `level\s+\S+\s+([0-9.]+)` would do. Five orders
of magnitude, accepted without a word.

**Letting the read ignore these lines and settling for a sentence in `Notes`.** The
current state, and what `cisco/ios` already does. Rejected because `Notes` is a
`[]string` at the **device** level: nothing links a sentence to a port in it, so
nothing lets a screen grey out the right line nor an apply know it is about to
overwrite something. Prose is fine for explaining an absence, not for carrying a
per-port observation — and the proof is that `cisco/ios` had to invent a parallel
`[]string` to get by.

**Converting `level rate 10 mbps` into bits per second.** The arithmetic would be
exact, and the contract already allows an exact change of multiple within the same
unit (`StormRateInKbps`). Rejected because the vocabulary of `RATE_UNIT` was not read:
converting assumes knowing that `mbps` is a member of it and that it is worth 10⁶ bit/s
rather than 2²⁰, and neither assumption is a reading. The day a control write
establishes the vocabulary, this alternative becomes the right one again — and the
change will be a parser line, not a data shape, because `StormRateBPS` already exists.

**Writing on `core01-arena` to settle `RATE_UNIT` and the scale of the JSON `level`
field.** Two open questions, a single configuration command to close them. Rejected
without hesitation: it is a production core, the gesture that informs is the gesture
that cuts, and `Foreign` exists precisely so that ignorance has an honest
representation rather than a price paid in service outage.
