---
Status: Accepted
Date: 2026-10-24
---

# ADR-0005 — Brand then model picker for serial procedures

## Context

The `/add/serial` screen chooses the platform through a **grid of tiles**. Six
tiles are written and translated (FS S3900, FS S3400, EdgeSwitch, Catalyst 2960-X,
Arista EOS, Juniper EX) and each carries a serial procedure scripted end to end.
Below them, the grid pours out everything else the `/api/drivers` API returns:
`cisco-nxos`, `aruba-cx`, `hpe-procurve`, `mikrotik-swos`, `mikrotik-routeros`,
`ubiquiti`, `extreme-exos`, `huawei-vrp`, `aruba-instant-on-1930` — raw identifiers
with a brand name, and **no procedure behind them**.

Three problems that only show in use:

1. **The grid promises what it does not deliver.** A `huawei-vrp` tile can be
   clicked. It leads to a wizard that has nothing to send.
2. **It does not scale.** Fifteen tiles today, and every added driver adds one
   more, with no hierarchy: brand and model are flattened onto the same line.
3. **It cannot be searched.** Finding "2960-X" requires scanning the grid by eye;
   there is no text input.

The same question arises identically on `/add/reset` ("Reset a device"), which
displays a procedure according to the platform.

## Decision

Replace the grid with **two dropdowns with built-in search, brand then model**,
followed by a "Start serial link" button. The brand list contains **"Automatic
discovery"** as its first entry; choosing it hides the model list.

The catalogue is a **shared pure module** (`serialCatalog.ts`), consumed by
`/add/serial` and by `/add/reset`. It contains **only the platforms whose procedure
is written**: an entry in the catalogue is a promise kept.

The picker itself is a `ui/SearchSelect.tsx` primitive, built on `usePicker` like
`SiteSelect`, so that the two screens do not diverge.

## Consequences

- **We lose the grid's discoverability.** A tile grid shows six platforms at a
  glance; a closed list shows none. That is the price, and it is accepted because
  the grid no longer showed anything useful past the sixth tile anyway.
- **Platforms without a procedure disappear from the screen.** An operator looking
  for `huawei-vrp` will no longer find it and may believe meshcraft does not know
  the brand. That is preferable to the current state, which lets them click and
  then fail — but a sentence will be needed saying that the list only contains the
  written procedures, and that automatic discovery remains open to everything else.
- **Adding a platform becomes two gestures**: write the procedure, then register it
  in the catalogue. Forgetting the second is silent. A test pins that every key of
  `SOFT_RESET_DRIVERS` has an entry in the catalogue.
- The catalogue duplicates information the API already knows (the list of
  drivers). The duplication is **deliberate**: the API lists what exists, the
  catalogue lists what is **workable over serial**, and these are two different
  sets.

## Alternatives ruled out

**Keeping the grid and hiding tiles without a procedure.** Solves problem 1 alone.
The grid stays flat and unsearchable, and the day fifteen procedures exist problem
2 comes back in full.

**A native brand + model `<select>`.** Free, accessible, and it is exactly what
`SiteSelect` already ruled out: a native `select` carries no search field, and the
project has a primitive that does.

**A single free search field on "brand model".** Faster for someone who knows what
they are looking for, unusable for someone who wants to know what is available —
and that is the majority case in front of a rack with an unknown switch in hand.

**Reading the catalogue from the API.** It would require the controller to declare
which platforms have a *serial* procedure, whereas the procedures live in the
frontend (`web/src/pages/add/*.ts`). The server does not have that information and
giving it would be a round trip for nothing.


## Addendum at implementation (2026-10-24)

The decision planned a test pinning that every key of `SOFT_RESET_DRIVERS` has an
entry in the catalogue. The implementation does better and makes the test
unnecessary: `SOFT_RESET_DRIVERS` and `TILED_DRIVERS` were **two hand-written arrays
carrying the same six keys**, and a catalogue next to them would have made three.
Both were removed in favour of `softResetDrivers()`, **derived** from the catalogue.
There is no longer any place where they can diverge.

What the test pins instead: that none of the platforms without a procedure
(`huawei-vrp`, `extreme-exos`, `aruba-cx`…) can silently come back into the list.
