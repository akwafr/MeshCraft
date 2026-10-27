---
Status: Accepted
Date: 2026-10-25
---

# ADR-80600 — Ctrl+F searches the site, the second press hands back to the browser

## Context

An ordinary-sized site for this product — ARENA, measured on 2026-10-25 —
contains **14 devices, 724 ports and 111 clients**. These three vocabularies are
already served in full: `GET /api/sites/{id}/devices` carries the devices *and*
their ports (`Device.ports`, `port_groups`), `GET /api/sites/{id}/clients`
carries the 111 located MACs. Two responses, 849 nameable rows.

No screen shows more than one slice of them at a time. The Ports page paginates
per device, a switch's faceplate shows one box, the Clients page shows as many
as the window holds. **The browser's find therefore only searches the displayed
slice**: out of 724 ports, `Ctrl+F` "GE27" finds what is already in front of you
and misses the other 700. It is the general case, not an edge case — it is the
normal state of every page of this product.

What is missing, then, is a search covering the whole site, and the shortcut
everyone knows for "find" is already taken by the one that is useless here.

Two in-house facts frame the question:

- **`focusInEditable()`** (`web/src/ui/dismiss.ts`) has existed since chapter 8
  of the guide, because a screen shortcut that fires inside the screen's own
  search box is the precise defect this safeguard once fixed.
- **The top bar has no more room.** The comment in `styles.css` around
  `.topbar .tabs` says so in numbers: the children are frozen one by one, the
  only remaining give is the site picker's 112 px floor, and the 134 px returned
  by ADR-0019 "pay off a deficit older than the picker rather than buy room for a
  seventh child".

## Decision

**1. `Ctrl+F` and `⌘F` open meshcraft's search. The second press belongs to the
browser.**

The shortcut is hijacked once, and only once. Three refusals, in this order, and
each refusal lets the event through without `preventDefault()` — so the
browser's find bar opens normally:

| Situation | Who takes `Ctrl+F` |
|---|---|
| No layer open, focus outside any input field | meshcraft |
| A dismissal layer is open (the palette itself, a modal) | the browser |
| Focus is in an `input`, `textarea` or `contenteditable` | the browser |

The second row is the escape hatch, and it is not a side effect: the palette *is*
a dismissal layer (`useDismissable` via `usePicker`), and its search field *is* an
input field. Both refusals therefore fire together as soon as it is open. Whoever
wanted the browser's find presses `Ctrl+F` twice and gets it, without having to
learn anything — and the palette's empty state says so in one line, because an
escape hatch nobody knows about is not one.

The shortcut is carried by a primitive in `ui/dismiss.ts` (`useGlobalModKey`), in
the same place and with the same safeguards as `useGlobalKey`. No component adds a
`keydown` listener.

**2. The three vocabularies coexist in groups, in the order devices → clients →
ports.**

A single list, three headings, each row marked by a glyph and by a detail line
specific to its kind. The order is that of **increasing cardinality**, which is
also that of **decreasing discriminating power**:

- 14 devices: naming a device almost always designates a single one;
- 111 clients: a MAC is unique, but it is a hexadecimal fragment one recognises
  more than one names;
- 724 ports: `1` exists on all fourteen boxes, and `GE4` on twelve. A port name is
  the least discriminating token in the product.

Putting ports first would drown the other two groups on every short query. Each
group is therefore capped at **five rows**, and its heading carries `shown /
found`: the cap is visible, not silent.

Within a group, matches whose **label starts** with the first typed word come
before those that merely contain it, then those that were only found through
non-displayed text (the model, the management IP, the joined-up MAC). At equal
score the source order is kept — which keeps the ports of a given box in their
**physical** order, the one `expandPorts` produces and the one read on the front
panel.

**3. A MAC is searched in its five spellings, and from four digits on.**

`parseMacQuery` (`pages/macSearch.ts`) reduces the query to its hexadecimal
digits; if it keeps **at least four**, they are also compared with each client's
joined-up MAC. Below four, no: `1` and `ab` appear in almost every MAC on the
site and would bury the name matches. Matching by MAC is **added** to matching by
name, it does not replace it.

**4. No new route, and no new URL. A result leads to the box.**

A site's two payloads are already cached under `["devices", siteId]` and
`["clients", siteId]`; the palette repeats these keys and pays nothing when a page
has already read them. A server-side filter would redraw more slowly to filter
the same rows.

The three kinds of result lead to the same kind of destination: the **detail page
of the device concerned** — the device itself, the switch carrying the port, the
switch where the client is seen. `deviceDetailPath` exists, it is stable under
renaming, and it is the only deep address this product has: there is **no URL
designating a port** nor a MAC (`useSearchParams` appears nowhere in `web/src`).
The result row therefore itself carries what the search found — the port and its
box, the MAC with its switch and port — because out of 724 ports, "where is GE27 /
User?" is a question the list answers without navigating.

**5. The palette belongs to no navigation mode** (ADR-0013, ADR-0019). Like the
gear, it appears in neither of the two bars and does not change with them. A
result leading to `/{site}/equipment/…` switches the mode through the "the URL
wins" rule already in place in `Layout.tsx`; the palette declares nothing.

## Consequences

- **Whoever presses `Ctrl+F` to search the page gets something else.** That is
  the price, it is real, and it is not refunded: a second press is needed. We
  accept this cost because the browser's find only sees a slice of 724 ports, and
  it remains reachable.
- **`Ctrl+K` is hijacked too, and it is also a browser shortcut** (the address bar
  in search mode). It is taken because it is the palette idiom and a synonym costs
  nothing; it falls under the same rule and the same two-press escape hatch,
  without any extra code.
- **The browser's find bar opens on top of the palette** when requested, and it is
  then the one that takes `Escape` as long as it has focus. The palette then
  closes with `Escape` or a click outside. We do not try to fix this stacking: it
  is not under our control.
- **Seven children in the top bar, and +40 px.** Measured with ADR-0019's probe,
  in French, on ARENA, after ADR-0031's switch:

  | | above 960 | below 960 |
  |---|---|---|
  | switching bar | 1151 → **1191 px** | 767 → **803 px** |
  | routing bar | 1046 → **1086 px** | 661 → **697 px** |

  +40 px and +36 px, that is the 30 px glyph plus the gap of the moment
  (`--sp-4`, then `--sp-2`): there is no third figure, since an icon does not
  shrink. The give remains the earlier one — the site picker's 112 px floor. An
  **icon**, then, never a permanently expanded field: a usable field would cost
  180.
- **The bar has a discontinuity at 960 px** and any future measurement must read
  both sides: the identity group folds into `.topbar-more` (398 px of children
  become 30), so the bar wants ~380 px less just below than just above. A single
  figure would be true over a third of the range.
- **A port found is not a port selected.** One lands on its faceplate, not on its
  panel. Fabricating a port URL is a decision that belongs to whoever owns the
  faceplate, and it is deliberately left open.
- **A client's full detail stays on the Clients page** (volumes, VLAN, other
  locations). The palette says where it is, not everything known about it.
- **The two requests are only sent on opening.** A page that has never read
  `["clients", siteId]` pays a round trip the first time the palette opens. We
  prefer that to having every screen in the product read the clients table for a
  shortcut that may never be pressed.
- **A client without an LLDP name has only its MAC and its vendor.** `ClientRow`
  carries neither hostname nor IP; "searching client names" means what the product
  knows about a client, not what one would like it to know.

## Alternatives ruled out

**Leaving `Ctrl+F` to the browser and taking `Ctrl+K`.** The honest option, and
the one someone will propose in good faith. It loses for one reason and one only:
`Ctrl+K` has to be learnt. The gesture an operator looking for a port makes is
`Ctrl+F`, they will make it anyway, and today they get a search that does not look
at the data. Handing the shortcut back to the browser means keeping this silent
miss and betting on discovery. We therefore hijacked `Ctrl+F` **and** kept
`Ctrl+K` as a synonym, so that whoever knows the palette idiom is not forced to
learn the opposite.

**Hijacking `Ctrl+F` with no escape hatch at all.** Less code and a simpler rule to
state. Ruled out: the browser's find does things this one never will — searching a
status note, a refusal message displayed verbatim, the API page. Removing a
universal gesture without replacing it on its own ground is the kind of decision
that turns up in a ticket six months later.

**A third press that closes and hands back.** One more notch in the same
direction, and one more state to hold ("how many times was it pressed"). Ruled out
because the chosen escape hatch costs no state: it falls out of the safeguards
`useGlobalKey` already applies.

**A relevance ranking, the three kinds mixed.** What a search engine would do.
Ruled out on the numbers: with 724 ports against 14 devices, any short query
brings up a column of ports and the device being looked for goes off screen.
Groups are what guarantee that one kind stays visible even when another has a
thousand matches.

**The requested order — ports, clients, devices.** It is the order in which the
request was phrased, and it is the reverse order of discriminating power. See
above.

**A single global cap of twenty rows.** Simpler, and it brings back the previous
problem: twenty rows of ports and nothing else. The cap is per group because it is
the group that is being protected.

**A `GET /api/sites/{id}/search?q=…` route.** It would have its place if the data
were not already there. It is: the two payloads are served in full, cached, and
invalidated by the site's SSE stream. A round trip per keystroke to filter rows
already present would be slower on screen and heavier on the server — and it would
break offline typing, which works today because filtering is local.

**A search field permanently expanded in the bar.** What the request almost
describes, and what the bar refuses: it was already overflowing between 721 and
773 px with six frozen children. A usable field costs 180 px, an icon about
thirty. The icon opens a popover, which is constrained by nothing.

**Making the port and the MAC addressable by URL so that results are deep
links.** The right goal, the wrong moment: it touches the faceplate and the
Clients page, that is two entire screens, for a feature whose main value —
answering without navigating — does not wait for it.
