---
Status: Accepted
Date: 2026-10-24
---

# ADR-0010 — The reset screen lists everything, and the brand is a field

## Context

Two lists describe the platforms, they are not the same size, and one screen consults
both.

- `internal/discovery/reset.go` carries **21 reset procedures** (plus `genericReset`,
  returned for an unidentified device and never listed). Each carries `Driver`,
  `Vendor`, `Family`, `Aliases` and a `Verified` flag.
- `web/src/pages/add/serialCatalog.ts` carries **6 platforms**: those whose **serial**
  procedure is written in the browser.
- **15 reset procedures are outside the catalogue, 7 of them marked
  `Verified: true`**: `hpe-procurve`, `mikrotik-routeros`, `ubiquiti`,
  `extreme-exos`, `huawei-vrp`, `h3c-comware`, `cumulus-sonic`.

`/add/reset` currently gets by with an "Another platform…" sentinel that switches the
screen into a free search over the whole set served by the controller. It works. The
price is twofold: **two grammars on the same screen** — a brand/model pair on one
side, a text field on the other — and most of the procedures, verified ones included,
filed behind a door whose label says "the rest".

[[ADR-0005 — Brand then model picker for serial procedures]] created the catalogue and
accepted to **hide platforms whose serial procedure does not exist**: "a catalogue
entry is a promise kept". That rule is right, and it is about `/add/serial`, where an
entry leads to a wizard that must have something to send. On `/add/reset`, there is
nothing to send: the procedure **exists**, it is **verified**, and the screen merely
displays it. Hiding correct knowledge because another screen cannot play it is not the
same decision.

The proposal on the table was to derive the list of brands from `reset.go` and to use
`serialCatalog.ts` only to enrich the rows it covers, grouping on the `Vendor` string.
**Its arithmetic is wrong, and that is instructive**: grouping the 21 procedures on
`Vendor` gives **18 groups, not 22**, and three of them are HPE — `"HPE"` (ProCurve),
`"Aruba (HPE)"` (ArubaOS-CX), `"H3C / HPE Comware"`. A fourth, `"Linux network"`
(Cumulus / SONiC), is not a vendor. The "Ubiquiti" duplicate the proposal reproached
itself for is not a problem: it is exactly what grouping solves — one brand, two
families.

The real defect is elsewhere. `Vendor` is currently a **label**: it is displayed in a
chip, and nobody ever treated it as an identity. Making it a grouping key gives scope
to a wording tweak: writing `"HPE (ProCurve)"` instead of `"HPE"` would split a brand
in the interface, silently, from a commit that thought it was improving a sentence.

## Decision

**The reset screen lists all the procedures the server carries**, grouped by brand,
with no sentinel and no mode switch. Grouping is done on an **explicit field**,
`ResetProcedure.Brand`, and not on the displayed string. `serialCatalog.ts` no longer
decides what is listed: it **enriches** the rows it covers, joined by `driver`, which
was already the only key between the two.

Three points, which are the decision and not its implementation:

**1. `Brand` is the identity, `Vendor` remains the label.** `Brand` is a stable slug;
`Vendor` is the displayed phrase. A Go test pins that every procedure carries a
`Brand`, and that two procedures sharing a `Brand` carry the **same** `Vendor`. The
constraint the proposal created without saying so becomes explicit, verified, and
impossible to break by rewording.

**2. The HPE merge is done once, on purpose.** `hpe-procurve` and `aruba-cx` become a
single brand `hpe`, labelled **"HPE / Aruba"**: it is the same product line, and the
lid says HP, HPE or Aruba depending on the year. `h3c-comware` **stays separate**:
Comware is the 3Com/H3C line, and someone holding a 5130 looks for H3C, not HPE. These
two arbitrations are decisions, not consequences of string equality.

**3. Search reaches the aliases, without displaying them.** The `Aliases` (`s5720`,
`2530`, `gs1900`…) are exactly the numbers an operator has in front of them.
`ui/SearchSelect.tsx` receives a non-displayed `keywords` field, and its matching
becomes **word-based** (every word of the query must appear), folded on accents — the
same rule as `resetMatches` on the Go side. That is what makes it possible to remove
the free search field without losing what it could do: typing `s5720` in the Brand
field finds Huawei.

The screen loads the whole set once (21 rows) and filters in the browser. The `q`
parameter of `/api/discovery/reset-procedures` stays as it is, for `/add/serial` and
for API clients.

## Consequences

- **The 7 verified procedures outside the catalogue become reachable in two
  gestures** instead of being behind a sentinel. That is the goal.
- **Adding a procedure now requires choosing a brand.** Forgetting is not silent: the
  Go test refuses an empty `Brand`. On the other hand, the `Brand` → `Vendor`
  consistency is a **new** constraint on a field that had none, and someone will hit
  it while rewording a label.
- **The `Vendor` chip changes on screen** for `aruba-cx` and `hpe-procurve`:
  "Aruba (HPE)" and "HPE" become "HPE / Aruba". The `Family` keeps distinguishing the
  two lines.
- **A `Family` changes too**, and it is a defect the single list reveals: the two
  Ubiquiti procedures end up side by side under a single brand, and "UniFi /
  EdgeSwitch" facing "EdgeSwitch / UniFi Switch (FASTPATH)" required choosing between
  two anagrams. The first becomes "UniFi (adoption by the controller)": `Family` names
  the **path**, not the range. The lesson is general — two families of the same brand
  must read against each other, which the sentinel exempted from checking.
- **The `/add/reset` list no longer promises anything about serial.** An entry there
  says "this reset procedure exists", and nothing else. `/add/serial` keeps ADR-0005's
  rule intact, and the catalogue keeps its promise: it still alone decides what is
  **playable** over serial, and `softResetDrivers()` remains derived from it.
- **A brand carrying only one procedure selects it automatically**, and the Model
  field does not appear. We lose the model confirmation step; it is the rule
  `/add/serial` already applies ("a list with a single answer is a ceremony").
- **"Linux network" appears in a list of brands without being a brand.** Accepted:
  nobody holding a white box has a brand to look for, and the OS name is what they
  know.
- `SearchSelect` changes matching rule for **all** its callers, `/add/serial`
  included. The change is strictly more permissive on a multi-word query (`cisco 2960`
  matched nothing) and identical on one word.
- The free search field disappears. The keystrokes are the same, one decision fewer —
  but it is no longer the server's multi-word matching that answers, it is the
  browser's. The two must stay in agreement; they are written for that and it is all
  that holds them together.

## Rejected alternatives

**Keep the sentinel** (the status quo). It works, and it leaves two grammars on a
ten-line screen. Above all, it files most of the content behind a label that says
"other": with 21 procedures of which 15 are behind the door, it is no longer an
emergency exit, it is the main entrance badly named.

**Group on the `Vendor` string as is**, the proposal as stated. It gives 18 groups of
which three are HPE side by side, and above all it makes a display string
load-bearing with nothing to hold it: a copy tweak splits or merges a brand in the
interface. Rejected for that, not for the "Ubiquiti" duplicate — grouping is precisely
what solves it.

**Pad `serialCatalog.ts` with the 15 missing platforms.** A single list too, and it
would lie to the screen the catalogue was written for: `softResetDrivers()` is derived
from it, so `/add/serial` would start offering `huawei-vrp` again and lead to an empty
wizard. It is exactly the regression ADR-0005's test pins, and it is right.

**Move the serial catalogue to the server side** (a "serial procedure written" field
in `reset.go`), to have only one source. ADR-0005 already rejected it for the right
reason: serial procedures live in the browser, the server cannot know which ones
exist, and making it declare them would be a round trip that falls out of sync at the
first omission.

**A single free search field, without brand or model.** Already rejected by ADR-0005,
for a reason that has not changed: unusable for someone who wants to know what is
available, and that is the majority case in front of a rack with an unknown switch in
hand.

**Two screens, one per set.** Removes the question by duplicating navigation, and
leaves the operator choosing between two "reset" pages before having looked at their
hardware.
