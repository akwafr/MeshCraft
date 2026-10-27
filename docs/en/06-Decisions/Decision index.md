# Decision index (ADR)

An **ADR** — *Architecture Decision Record* — records a choice someone will
want to understand, challenge or redo later. It does not explain how the code
works: the code already says that, and the comments in this repository say it at
length. It explains **why this choice rather than another**, and what we agreed
to lose by making it.

## What deserves an ADR

Three criteria, any one is enough:

- **Expensive to reverse.** A data shape, a wire contract, a default that lands
  on existing users.
- **Counter-intuitive.** A competent person will arrive and propose the opposite,
  in good faith, because the reason is not visible from the code.
- **Arbitrated between two evils.** There was no good answer, just a less bad
  one, and the price paid must stay written down.

What does not deserve one: a variable name, an obvious bug fix, a choice the next
person would make identically without thinking.

## The rule

**The ADR is written BEFORE the work, not after.** That is what makes it useful:
it forces the alternatives to be named while they are still open, instead of
justifying after the fact whatever was typed first. An ADR written at the end is
a report, not a decision.

The commit that implements an ADR cites it by number.

**An accepted ADR is not modified.** It is superseded: a new ADR, a new number,
and the old one becomes `Superseded by ADR-XXXX`. Rewriting a decision erases the
record of the moment we thought otherwise, which is exactly what people come back
to read.

## Format

One file per decision: `ADR-NNNNN — short title.md`, where `NNNNN` is an
identifier of **five randomly drawn digits**, never reused
([[ADR-74818 — An ADR number is drawn at random, not taken as the next one]]).
ADR-0001 to ADR-0032, which predate this rule, keep their four-digit sequential
number. The draw rules out any identifier already present on any branch:

```bash
used=$(git log --all --name-only --format= | grep -o 'ADR-[0-9]\+' | sort -u)
while :; do n=$(shuf -i 10000-99999 -n 1); echo "$used" | grep -qx "ADR-$n" || break; done
echo "ADR-$n"
```

Four sections, in this order:

| Section | What it contains |
|---|---|
| **Context** | What was true before, and what forced the question. Facts, not intentions. |
| **Decision** | What we do, in the present indicative. One sentence must be enough to state it. |
| **Consequences** | What it costs, including what we now forbid ourselves. An ADR without a negative consequence is an ADR that lies. |
| **Alternatives ruled out** | The real options, and why each one lost. It is the section people come to read. |

Header: `Status` (Proposed · Accepted · Superseded by ADR-XXXX · Refined by
ADR-XXXX · Abandoned) and `Date`.

`Refined by` exists for **partial** supersession: an ADR one point of which out
of seven was reopened, the other six remaining in force. `Superseded by` would
send the reader elsewhere while the document is still authoritative; rewriting
the point would erase the record of the moment we thought otherwise. The new ADR
says which one it reopens, and does not repeat the rest.

## The decisions

Ordered by header date: up to ADR-0032 the number follows the date, after that it
no longer says anything about it.

| No. | Title | Status |
|---|---|---|
| [[ADR-0001 — Two themes, and the status palette that must survive both]] | Two themes | Accepted |
| [[ADR-0002 — Glass is chrome, never content]] | Material | Accepted |
| [[ADR-0003 — The traffic ramp is logarithmic and relative to capacity]] | Traffic ramp | Accepted |
| [[ADR-0004 — Junos provisioning without commit confirmed]] | Junos | Accepted |
| [[ADR-0005 — Brand then model picker for serial procedures]] | Serial picker | Accepted |
| [[ADR-0006 — Freshness is measured on the server clock]] | Clock | Superseded by ADR-0008 |
| [[ADR-0007 — The aggregate is a port on the faceplate]] | Aggregates | Accepted |
| [[ADR-0008 — The time reference is the viewer's clock]] | Clock | Accepted |
| [[ADR-0009 — A swipe never steals a scroll gesture]] | Swipe navigation | Accepted |
| [[ADR-0010 — The reset screen lists everything, and the brand is a field]] | Reset list | Accepted |
| [[ADR-0011 — A token authenticates, it does not authorise]] | API tokens | Accepted |
| [[ADR-0012 — The tool surface is not the API surface]] | MCP server | Accepted |
| [[ADR-0013 — Switching and routing are two navigation modes, not two products]] | Navigation modes | Superseded by ADR-0019 |
| [[ADR-0014 — The declared label is evidence, identity is derived]] | Model identity | Accepted |
| [[ADR-0015 — The site extension lives in the controller, and its inventory is a read-only mirror]] | Site extensions | Refined by ADR-0025 |
| [[ADR-0016 — A Wi-Fi access point is a device, but it only shows in Wi-Fi and on the topology]] | Wi-Fi model | Proposed |
| [[ADR-0017 — Rejecting a short read belongs to absorption]] | Short read | Accepted |
| [[ADR-0018 — A port's name at write time is an observation, not a constant]] | Port naming | Accepted |
| [[ADR-0019 — The switcher carries only the brand, the mode reads in the bar]] | Mode switcher | Refined by ADR-0031 |
| [[ADR-0020 — A storm limiter we cannot read is reported, not silenced]] | Storm control | Accepted |
| [[ADR-0021 — An MST region name is an identity, not a label]] | MSTP region | Accepted |
| [[ADR-0022 — Joining an MST region is a declared capability, not a consequence of "can write STP"]] | MSTP region | Accepted |
| [[ADR-0023 — Spanning-tree configuration is sent only on a gesture, and drift reads in a banner]] | STP writes | Accepted |
| [[ADR-0024 — An alert is earned - the log decides, the description and the interconnect qualify]] | Alerting | Accepted |
| [[ADR-0025 — meshcraft remote-controls the UniFi controller, it holds no intent]] | UniFi gestures | Refined by ADR-0029 |
| [[ADR-0026 — A topology link has two halves, each coloured by its end's egress]] | Link rendering | Accepted |
| [[ADR-0027 — A tile's title is data, its target is an unguaranteed reference]] | Dashboard tiles | Accepted |
| [[ADR-0028 — Per-class traffic breakdown is a declared capability, and an undeclared class says so on screen]] | Per-port monitoring | Accepted |
| [[ADR-0029 — The SSID is editable, the access point is not, and only what reads back is offered]] | Wi-Fi editing | Accepted |
| [[ADR-0030 — The traffic ramp has one colour engine, and the SVG gradient samples it]] | Traffic ramp | Accepted |
| [[ADR-0031 — The mode switcher is a toggle, and the brand is no longer a door]] | Mode switcher | Accepted |
| [[ADR-0032 — A tile may declare a live zone in edit mode]] | Dashboard grid | Accepted |
| [[ADR-80600 — Ctrl+F searches the site, the second press hands back to the browser]] | Global search | Accepted |
| [[ADR-74818 — An ADR number is drawn at random, not taken as the next one]] | ADR numbering | Accepted |
| [[ADR-46584 — Identity is an icon, and sign-out lives behind it]] | Account menu | Accepted |
| [[ADR-73885 — The page background takes the mode's tint]] | Per-mode background | Accepted |
