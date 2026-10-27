---
Status: Accepted
Date: 2026-10-25
---

# ADR-0027 — A tile's title is data, its target is an unguaranteed reference

## Context

The tile catalogue (`store.TileCatalog` on the server side,
`web/src/dashboardTiles.tsx` on the browser side) is **made entirely of i18n
keys**. Each tile declares a `labelKey`, and `make test-web` checks that this key
exists in English and in French. That is true of the eighteen existing tiles
without exception, and it has become a reading habit: "a tile label is something
you translate".

Two requests arrive at the same time and attack this habit from both ends:

1. **Renaming a tile.** Two "Site traffic" tiles on the same dashboard, set to two
   different windows, carry the same title; the operator wants to read "Traffic —
   7 days". That text comes from them.
2. **A per-port traffic tile.** It means nothing until it has been pointed at a
   device and a port. It therefore carries a **reference** to a row of `devices` —
   and on a 724-port site, that reference is the heart of the tile.

The `dashboard_tiles.options` column already exists (migration 0041) and carries
per-tile settings: the traffic chart's window, the log's minimum severity. Until
now these are **closed enumerations** — four windows, five severities —, validated
on the server side because the public route uses them to bound what it asks of the
series store. None is free text, and none designates another row of the database.

The question is therefore not "where to put two fields", it is: **is `options`
allowed to carry user data and a reference the database does not guarantee?**

## Decision

**Yes, and both are ordinary data stored in `options`.**

**The title is data, not an i18n string.** `options.title` carries the text as the
operator typed it. It goes into no locale, passes through no `t()`, and survives a
language change intact. Three rules frame it, and they are the decision as much as
the sentence above:

- **A tile without `title` keeps its translated label.** The catalogue's
  `labelKey` remains the default, resolved at display time. We **never** store the
  translated label at renaming time: writing "Site traffic" into `options.title`
  because the interface was in English would freeze a tile's language merely by
  having renamed it once.
- **An empty title removes the key, it does not store it empty.** Renaming with a
  blank string **reverts to the default label**. The key is *deleted* from
  `options`, not set to `""`: "no custom title" must be a single state, otherwise
  `sameLayout` sees a change where the screen has not moved, and the "Save" button
  appears for a cancelled gesture.
- **A title is bounded** — 60 characters, refused on the server side, not only on
  the browser side. A tile header is at least two grid columns wide; beyond that,
  the CSS truncates, and the bound exists so that storage does not carry a novel.

**The target is a reference nothing guarantees.** `options.device_id` and
`options.port` designate a device and a port, and **no foreign-key constraint
protects them**: neither the database, nor the devices' `on delete cascade`.
Deleting a device leaves behind tiles that still point to it, and that is
**accepted rather than endured**:

> A tile whose target has disappeared **says so plainly and stays in place**.

Three disappearances, three distinct sentences — device deleted, port absent from
the faceplate, port without measurement — and in all three cases the tile keeps its
pickers so it can be re-pointed. It does not delete itself, and it **never** draws a
flat curve: zero is a measurement, the absence of measurement is not one.

## Consequences

- **No migration**, and that is the direct gain: `options` is already `jsonb`,
  opaque to the store. The title and the target travel with the layout, through the
  route that exists (`PUT /api/dashboards/{id}/tiles`), and are saved by the "Save"
  button the operator already uses after moving a tile. No new route, no new
  gesture.
- **The server now validates the content of `options`, which it did not do.**
  `options` was checked as "valid JSON" and nothing else, hence without a size
  bound. As soon as one of its keys is **displayed**, not bounding it would be a
  hole: the 60-character bound and a global cap on the block's size are set in
  `tileInput.valid`.
- **The comment on `store.DashboardTile` becomes half wrong and must be fixed**:
  `options` is no longer "settings, the rest is the interface's business". It
  carries a reference whose lifetime is not that of the row it designates.
- **A targeted tile cannot be seeded.** The two default dashboards place every tile
  of the catalogue — it is a tested requirement
  (`TestDefaultDashboardsCoverTheWholeCatalogue`), and it exists so that no tile is
  invisible to someone who never opens the picker. A tile without a target has
  nothing to show on a new site: the catalogue therefore gains a `Targeted bool`,
  and the seeding requirement only applies to tiles that are not. The price is real
  and accepted: **the "Port traffic" tile can only be discovered by opening the tile
  picker.**
- **A custom title is not translated, by construction.** A French-speaking operator
  who renames a tile imposes their text on the colleague who reads the interface in
  English. That is the intended behaviour — a dashboard belongs to the site
  (migration 0041), and a name given by someone is a name, not an interface label.
  The same trade-off as for the name of a dashboard, a VLAN or a port profile, which
  are not translated either.
- **A public link displays a custom title as is.** It already carried the names of
  the dashboard and of the site; a tile title is of the same nature, and the public
  projection therefore lets it through. Worth knowing when renaming: what one writes
  in a tile title is readable by whoever holds the link.

## Alternatives ruled out

**A `title text` column on `dashboard_tiles`.** The database would have bounded the
length itself, which is its job, and the field would have been visible in the
schema instead of hidden in a bag. Ruled out because it charges a migration for a
field whose validation fits in three lines, and above all because it would have
settled nothing for the **target**: a second column would have been needed, then a
third for the next configurable tile, and `options` exists precisely so that the
number of columns does not follow the number of tiles. A mostly empty table is the
pattern migration 0041 already refused.

**A `device_id uuid references devices(id) on delete cascade` in a column.**
Technically the right way to never have a dangling reference — and the wrong
product: deleting a device would make **the tile disappear** from everyone's
dashboard, leaving a hole in the layout without a word of explanation. That is
exactly what `dashboardGrid` already refuses to do for an unknown tile type, and
for the same reason: a hole in a layout reads as data loss. With `on delete set
null`, one gets what this decision keeps — a tile that survives and says so — at the
price of a column and a migration.

**Treating the title as an i18n key and adding it to the locales.** The natural
slope given the current catalogue, and it must be named to be ruled out: a key added
at renaming time breaks en+fr parity at run time (the freeze of the nine other
locales makes it absent everywhere else), and an operator's text has no translation
to look up anyway. A more insidious variant, ruled out by the same rule: storing the
catalogue's **translated** label as the initial title in the form field. The field
therefore opens on the custom title if there is one, and **empty** otherwise, with
the default label as `placeholder`.

**An empty title stored as is, displayed as is.** Literal and defensible: the
operator asked for an empty title. Ruled out because an empty header cannot be told
apart from a display bug, and "clearing the name" means "going back to the original
one" in every piece of software where that gesture exists. Reverting to the default
is moreover the only way to undo a renaming without retyping the exact translation
by hand.

**Automatically deleting a tile whose target has disappeared.** Avoids the degraded
state by deleting the information. A replaced device often comes back under the
same name, and a tile erased during a hardware replacement is a lost setting nobody
knows they have lost.
