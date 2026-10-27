---
Status: Accepted
Date: 2026-10-26
---

# ADR-74818 — An ADR number is drawn at random, not taken as the next one

## Context

Up to ADR-0032, an ADR took **the next number**: read the last one
in the index, add one. The rule holds as long as only one person writes at
a time.

On 2026-10-25, two agents launched in parallel, each in its own worktree,
read the same index, saw the same last number (0031), and each wrote an
**ADR-0032**:

- `ADR-0032 — A tile may declare a live zone in edit mode`;
- `ADR-0032 — Ctrl+F searches the site, the second press hands back
  to the browser`.

Both moved to `Accepted` in their branch, and the collision only
shows at merge time, when the commit of at least one of them already cites
its number. This is structural, not an accident of discipline: a worktree
does not see the others, and parallel work is now the normal way
of moving this repository forward. The more branches are open at once, the more
likely the collision becomes.

Renumbering at merge time is exactly what the index forbids, for a
good reason: the number is cited in commit messages, in
code comments (`ADR-0016 §3` appears in four Go files) and in
other ADRs. A renumbering leaves those citations pointing to the wrong
document, or to none.

## Decision

**From this ADR onwards, a new ADR takes a five-digit identifier
drawn at random, between 10000 and 99999, and checked to be free on all
branches.** The number becomes an identifier, no longer a rank.

1. **The draw happens when the file is created**, with the command
   written in the index. It excludes any identifier already present in
   the history of **any** branch, agent worktrees included.
2. **ADR-0001 to ADR-0032 keep their number.** No renumbering: they
   are cited everywhere, and the immutability rule applies to their name as
   to their text. Width makes the difference between the two regimes:
   four digits for a sequential number, five for a drawn identifier,
   so no confusion between them is possible.
3. **Order is read from the date, no longer from the number.** The index lists
   new ADRs after the old ones, by header `Date`. Two ADRs from the
   same day are listed in the order in which they entered `main`.
4. **The collision of 2026-10-25 is settled by the new rule, not by a
   renumbering.** ADR-0032 remains the one about tiles, whose commit already cites
   that number. The Ctrl+F one had not yet been cited by any code merged
   into `main`, and it becomes
   [[ADR-80600 — Ctrl+F searches the site, the second press hands back to the browser]].

## Consequences

- **Two agents working in parallel can no longer tread on each
  other**, barring bad luck: out of 90,000 values, two independent draws
  land on the same identifier once in 90,000. Even with ten branches
  open at once, the risk stays below 1 in 1,000. The draw already excludes
  everything visible, so the only collision still possible pits
  two branches that cannot see each other, and it shows at merge time as
  today.
- **We lose the "0031 comes after 0030" reading.** Knowing that ADR-80600 is
  more recent than ADR-74818 requires reading the date. That is the real price, and
  it is paid on every reading of the index, not just once.
- **We also lose the count.** "32 decisions" can no longer be read from the
  last number.
- A five-digit identifier is not remembered like "number 7". That is
  accepted: ADRs are cited by link and by copy-paste, not from memory.
- The index remains **the only place** that orders ADRs. An ADR missing from
  the index can no longer be spotted through a gap in the numbering.

## Rejected alternatives

**Reserving the number in the index before starting the work.** It was the
first idea, and it amounts to demanding the very discipline that just failed.
A reservation only exists in the branch where it is written. For the
others to see it, it must be committed to `main` before creating the worktree,
and an agent that writes its ADR along the way (which happens) cannot
do so.

**Renumbering at merge time.** That is what the case of 2026-10-25 called for, and
it is what the immutability rule forbids: citations in
commits cannot be rewritten without rewriting history, and that history has
already been rewritten once, for another reason, at a cost we do not want to
pay again.

**A dated identifier (`ADR-2026-10-25-a`).** It keeps the order, which is its
only quality. But two agents launched on the same day land on the same
suffix, and parallelism happens precisely within the same day. The
collision changes shape, it does not disappear.

**A hash of the title.** Unique without a draw, but a title gets reworded
during drafting, and the identifier would change with it. An identifier
must be fixed before the text is.

**A branch-specific suffix (`ADR-0032-a068`).** Unique by
construction, but unreadable, and it keeps a sequential number that seems
to state an order it no longer guarantees: two `0033`s would have different
suffixes.
