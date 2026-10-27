---
Status: Accepted
Date: 2026-10-24
Depends on: ADR-0011
---

# ADR-0012 — The tool surface is not the API surface

> **A departure from the convention, written here rather than kept quiet.** This ADR
> comes *after* the code (`cmd/mcp`), which `CLAUDE.md` explicitly forbids. It is
> therefore not a justification: the choices below were re-examined against their
> alternatives before merging, and point 4 was **verified in the code and in a test**
> rather than taken on trust, because it is what makes the other three real or
> cosmetic. What this departure cost is stated in "Consequences".

## Context

[[ADR-0011 — A token authenticates, it does not authorise]] settles *who* an MCP client
is: nobody in particular. It carries a user's token and can do nothing that user
cannot. Authorisation is therefore settled.

There remains the question ADR-0011 does not address, and which is entirely different:
**which gestures should an agent even be able to *formulate***.

The controller exposes 198 handlers. Two facts make them non-transposable as they are:

1. **Every tool schema is in the agent's context on *every* request.** A tool surface
   is not a catalogue one picks from, it is a permanent cost. 198 schemas would drown
   the question asked.
2. **An agent that gets it wrong does not get it wrong the way a human does.** An
   operator who clicks "migrate the management address" has gone through a screen, a
   warning and a confirmation, and knows which building the device is in. An agent
   produces the same call from an inference on a route name.

## Decision

**The tool surface is drawn from the product's questions, not from the controller's
routes.** 198 handlers become 17, 29 or 35 tools depending on the mode.

1. **A tool has to earn its place.** It exists if it *composes* several routes into a
   real question, if it *guards* a gesture, or if it *teaches* vocabulary. Otherwise it
   does not exist and the rest goes through the escape hatch (point 4). Non-negotiable
   example: `get_device` projects from the **list**, because the compliance verdict is
   computed on the list — a per-device read would answer `absent` where the truth is
   `unknown`.

2. **The descriptions carry the pitfalls, not the syntax.** That a collector not
   covering its site **stops collection while leaving the light green**; that the
   per-port DHCP-snooping flag is *trust*, so a wrong value **removes** protection;
   that a storm-control rate without its unit is off by five orders of magnitude. That
   is what a non-Go author cannot read out of a compiler.

3. **Dangerous gestures get three different answers, chosen on the FAILURE MODE and
   not on the level of danger.**
   - **Absent** when failure means nobody can reach the device any more, or when it
     touches what bounds the client itself: management migration and address hop,
     dashboard sharing links, administration of users and roles, factory reset,
     credential reveal.
   - **A single door, guarded by the name** for every deletion: `delete_object`
     requires the object's exact name, **re-read at call time**. Fifteen deletion tools
     would be fifteen names to misread.
   - **Harmless by default**, copying the product's idiom: a credential deployment
     **rehearses** as long as one does not ask to write, a profile **previews**.

   The principle that orders the three: **a confirmation an agent types proves it has
   read the object, it is not consent.** It is the right guard when the damage is a
   lost row that re-entry undoes. It is the wrong one when the damage is that someone
   with a console cable has to go to a building.

4. **The escape hatch is held by the same list.** `api_call` covers the long tail, but
   refuses `DELETE` in every mode and goes through the same exclusions as the dedicated
   tools. **Without this clause, point 3 would be decorative**: excluding migration
   from the tool list is worth nothing if an arbitrary route reaches it. A test checks
   that a refused call **never reaches the controller**, not merely that it returns an
   error.

5. **The default is read-only, and `tools/list` is FILTERED.** A write tool is not
   refused, it is **invisible** — an agent does not plan around a tool it will never see
   succeed. Same reason as ADR-0011: the default must be the least powerful, not the
   most convenient.

## Consequences

- **A surface to maintain that is not generated.** A new route does not appear by
  itself: it falls into the escape hatch until someone judges it deserves a tool. That
  is the accepted price of point 1.
- **The exclusion list is now a security object.** It must move in the same commit as
  the route it names, otherwise a dangerous gesture becomes reachable through
  `api_call` without anyone having decided it.
- **What the departure cost, concretely**: the alternative "1:1 generation from
  OpenAPI" was never evaluated on equal terms, since at the time of writing the manual
  work already existed. It is rejected below on argument, not on measurement. If
  someone comes back with a generated prototype whose context cost is acceptable, **this
  ADR must be superseded, not defended**.
- The server has **no privilege of its own** and does not become a trusted component:
  everything above bounds what an agent can *formulate*, never what it has the *right*
  to do, which remains entirely ADR-0011.

## Rejected alternatives

**Generate one tool per route from OpenAPI.** The obvious option, and the one that will
be proposed in good faith. Rejected on two grounds: the context cost is permanent and
paid on every request; and above all a 1:1 surface teaches the agent **the structure of
the controller's routes** instead of the product's vocabulary — it would learn that
there is a device list, not that a compliance verdict only makes sense on that list.

**Confirm instead of excluding.** Tempting because uniform: everything is possible,
everything asks for confirmation. Rejected because it treats a deleted row and an
unreachable device as the same risk. A confirmation is not consent, and for management
migration the commit's response is *lost anyway* — even "did it work" cannot be known
from here.

**No escape hatch at all.** The safest on paper. Rejected because the long tail would
make the server unusable for real work, and the blocked user does not give up: they
switch to `curl` with their token, where there is **no** guard at all. A bounded escape
hatch is safer than no escape hatch, bypassed.

**A privileged service account for MCP.** Directly contradicts ADR-0011. The log would
say "MCP deleted this VLAN".

**An MCP SDK in Node/TypeScript.** Would add a second toolchain to a repository that
tolerates only Docker, and a module tree in the `go.sum` of **every** build of the
controller and proxy. The useful protocol fits in `initialize` / `tools/list` /
`tools/call` / `ping`.
