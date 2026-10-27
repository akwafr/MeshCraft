---
tags: [frontend, topology]
---

# Weathermap — LLDP topology

Second major page: the site's devices placed freely on a plan, connected by their real links discovered via **LLDP**, with the state (and eventually the load) of the links — a weathermap in the classic sense.

## Where this page stands

The screen exists (**Topology** tab, `web/src/pages/Plan.tsx`), it is usable, and it is **not** a weathermap: the links it shows are **entered by hand**, not discovered.

| Question | Status |
|---|---|
| Plan screen: placed nodes, drawn links, zoom, level of detail | **shipped** — `web/src/pages/Plan.tsx` |
| Positions persisted per site | **shipped** — columns `devices.pos_x` / `pos_y` (migration `0002`), no dedicated table |
| Links: creation, deletion, state, note, speed | **shipped, as MANUAL ENTRY** — `POST /api/sites/{id}/links`, `DELETE /api/links/{id}` |
| Grouping frames on the plan | **shipped, but derived from the *place* field** of each device, not from a division of the plan |
| "Link diagnostics" panel | **shipped** — lists links that are not `up` or that carry a note |
| Port faceplate in the node at high zoom | **shipped** — the node switches to compact view below 60% |
| **LLDP discovery** of links, correlation of both ends | **SHIPPED** since 29/07/2026 — collection, storage and drawing, distinct from entered links ([[LLDP neighbours]]) |
| Link load (the real weathermap function) | **shipped** since 29/07/2026 — throughputs come from the counters (`internal/api/portrates.go`), and `simulated: false` says so. Still simulated in the single case where series storage is absent from the instance |
| Ghost neighbours (one-click **adoption**), LACP aggregates, proxy as a special node | **decided, not built** — the ghost neighbour is now *listed* with its chassis, but there is no adoption gesture |
| Background image per site, automatic layout, PNG/SVG export, wall mode | **decided, not built** |
| Real-time updates | **shipped, differently** — no WebSocket: an SSE stream announces "this site has changed" and the screen reloads ([[Web interface]]) |

> [!warning] Two kinds of links coexist on this page, and they must be read differently
> This is the gap to know before any other, because it changes what each line
> **means**:
>
> - an **ENTERED** link (`device_links`, thick line, state colour) is an
>   **operator's declaration**. It survives unplugging, and that is its whole
>   point: it is what reality is compared against;
> - an **OBSERVED** link (derived from LLDP neighbours, light line offset by 5 px) is a
>   **dated observation**, at most a minute old. It disappears when the cable goes.
>
> Mixing them would make an entered link disappear because a switch was unplugged for ten
> seconds. Nothing in collection therefore writes to `device_links`, and there is **no
> write route** for an observation: it is pushed by the collector or it does not exist.
>
> **What remains true of entered links**, and does not change with this batch: there is **no
> update route** for a link, so state and note are **frozen at creation**. A
> link entered as `up` will stay green even if the port goes down — the colour does not say what the
> network is doing, it says what was typed. Changing a link is done by deleting and
> recreating it. It is the port transitions (`port.up` / `port.down`) that carry the truth
> of the state, and they are real ([[Monitoring]]).
>
> **What has changed**: a link missing from the map is no longer necessarily a missing
> entry — neighbour data can show it, as a light line, without anyone having typed it.

> [!important] A link seen from ONE SIDE ONLY is the NORMAL case, and it is drawn
> The original design phrased correlation like this: *A sees B and B sees A → confirmed
> (solid line); seen from one side only → presumed (dotted)*, and added that a link seen from one
> side only "is the most frequent symptom of a real problem". **That last sentence
> is false on the reference fleet**, and the survey proves it: an entire platform
> does not implement 802.1AB (MikroTik on SwOS — walk of `1.0.8802` **empty**, measured). Any
> link touching a CSS326 will **only ever** be seen from the far end, whatever we do. Add
> to that neighbours outside the inventory, which say nothing by construction.
>
> The screen therefore presents one-sidedness as a **degree of certainty**, not as a
> defect: the line is dimmed, the tooltip names the two ordinary causes, and nothing
> suggests going to look for a fault. See [[LLDP neighbours]] §1.1 and §5.

> [!note] Why neither colour nor long dashes could carry the distinction
> The two obvious axes were **already taken** by entered links: **colour** carries
> state (`--danger`, `--warn`, `--border`) and the **"6 5" dash pattern** carries the `down` state.
> Reusing either would have made an observed link indistinguishable from a broken entered
> link — the exact confusion this whole separation exists to prevent.
>
> The third axis chosen is the **accent tint**, a **short** dash pattern ("2 3") and a
> thinner stroke. Plus a **perpendicular offset of 5 px**, without which an observation that
> confirms a declaration would be superimposed to the pixel — both connect the centres of the
> same nodes — and one of the two would be invisible. Observed links are drawn **before**
> entered links, hence underneath: the declared plan stays in the foreground.

## Data

- **Nodes**: the site's devices. Position `x,y` persisted — not in a `device_positions` table but in the `devices.pos_x` / `pos_y` columns, written by the ordinary device `PATCH`. The site's [[Proxy]] as a special node and the **background image** (building plan, uploaded per site) remain **decided, not built**: `sites.plan` does carry a jsonb, but it contains only dimensions, and the screen reads only its width and height.
- **Links**: **two sources**, never mixed. **Entered** ones come from `device_links` ("Add a link" modal). **Observed** ones come from `GET /api/sites/{id}/lldp`, derived from LLDP neighbours collected during polling — `lldpRemTable`, decoded by `internal/snmppoll/lldp.go`. **Correlation of both ends** is shipped and its reciprocity is **exact**: A/pa must claim B/pb *and* B/pb must claim A/pa. The broader rule "A and B see each other" would have caught a few more cases and got the one that matters wrong — two devices connected by **two cables**, where it would have mutually confirmed the two wrong halves. The nuance does **not** live in `device_links`, whose schema remains unchanged: it lives in `lldp_neighbors` and `lldp_self` (migration 0028), and bilaterality is **computed on read** rather than stored — stored, it would go stale as soon as one side stops seeing the other. See [[LLDP neighbours]] §3.
- **Ghost neighbours**: a non-adopted LLDP neighbour (chassis unknown to the inventory) appears as a greyed node → click = adoption proposal ([[Discovery]]). **Still not built**, but no longer impossible: the observation now exists in the database and comes up in `links` with an empty `b_device`, its chassis and its `sysDescr`. It is **listed** in the "Observed LLDP neighbours" panel, which gives enough to create the device by hand. What is missing is the **drawing** (a ghost has no position on the plan, and inventing one would amount to drawing a device at random) and the **adoption gesture**. On the reference fleet, three neighbours are in this situation — an RB5009UG, a CRS305 and an S3900 — so it is not an edge case.
- LACP aggregates: parallel links between the same two devices grouped into one thick line with a `×2` badge. **Decided, not built**: two **entered** A↔B links are still drawn as two lines **superimposed to the pixel**, since they start from the same node centre. An aggregate is therefore indistinguishable from a single link — and it is the case where it most needs to be seen. The 5 px offset introduced for observed links does **not** solve this case: it separates observed from entered, not two links of the same kind. LLDP derivation, on the other hand, does distinguish two cables between the same devices (exact reciprocity, see above): the information exists, it is the rendering that is missing.

## Rendering

- Pan/zoom canvas. **Shipped, without a library**: there is *no* graphics dependency in `web/package.json` — no `@xyflow/react`, no `elkjs`, no `d3`. The plan is hand-made: nodes are absolutely positioned `<div>`s, links a **single overlaid `<svg>`**, and zoom a `transform: scale()` on the layer, persisted in `localStorage`. Panning uses the container's native scrolling; Ctrl/⌘ + wheel zoom **keeps the point under the cursor fixed**, and a "Fit" button frames the content.

  Why not React Flow: the page needed three things — place, connect, zoom — and a fourth that is not in graph libraries, namely **drawing the port faceplate** inside the node. A 200 KB library for three primitives, in a binary we want small and served offline by the [[Controller]], costs without giving anything back. What this choice **lacks** is real and known: no **minimap**, no dragging the background to pan, and no **automatic layout** — a new device is born at `(0,0)`, so they all pile up in the top-left corner until placed manually. On a site with fifty switches, the first opening of the plan is a heap.
- A node = compact version of the device card: logo, hostname, status; **semantic zoom** — **shipped**: below 60% zoom the node only shows its name, above it shows its full faceplate ([[Port rendering]], 9 px cells). The threshold is not cosmetic: below it, the cells are no longer readable, and drawing them would pay for rendering several thousand elements for nothing. What remains **not built**: links **do not anchor on the real port**, they start from the node's centre. That is what will make the faceplate in the node really useful — knowing *where* a link goes through — and it is also what will visually separate two parallel links.
- Links coloured by state: green (up), red (one end down), grey (device unreachable). **Shipped**, but the state comes from **manual entry** (see the callout), not from observation. The colour gradient by **load** (throughput/speed ratio from SNMP counters — the real weathermap function), with a `1G · 34 %` label, remains **decided, not built** — but its two ingredients finally exist: the measured throughput per port (`rx_mbps` / `tx_mbps`, derived from the counters) and the negotiated link speed (`link`). All that is missing is the ratio and the colour scale.

> [!success] The throughputs displayed on links are MEASURED — since 29 July 2026
> This callout used to say the opposite, and it was right: the throughput labels came from a
> deterministic jitter (`jitAt`) around the `rx_mbps` / `tx_mbps` values that only a **configuration
> import** had set. The collector did read `if_counters`, and **nothing**
> converted those counters into per-port throughput — hence zero on every discovered device,
> and a number that moved without coming from the fleet on the others.
>
> The derivation now exists: `internal/api/portrates.go` queries the series
> database every 30 s (`rate()` over a 2 min window) and writes the result into
> `devices.ports[].rx_mbps` / `tx_mbps`. The three screens that already read those fields — the
> links on this page, the top ports of [[Monitoring]], the sparklines on the device
> page — therefore show measured values, **without a single line of their code changing**. And
> `GET /api/sites/{id}/traffic` returns `simulated: false`.
>
> **`jitAt` has been REMOVED**, along with the `useTick` clock that kept it going. It made
> the labels oscillate by ±18% every 2.2 seconds: an operator read 43 Mb/s where
> the port carried 37, and saw the number change without anything moving on the
> network. That was acceptable as long as the underlying value was fictitious; on a
> measurement, it is noise added to correct data — and it is on this kind of number that
> capacity decisions are made. Values therefore now only change at the pace of the device page's
> real refresh. A calmer interface, which tells the truth.
>
> The FALLBACK curve lost its variation for the same reason: when the measurement is not
> available, it is now FLAT, at the inventory value, with its flag. A
> straight line claims nothing; an invented curve claims everything.
>
> The `simulated: true` flag remains for three legitimate cases, and the screen must keep
> reading it: no metrics storage configured, an instance without history, or a
> storage momentarily unreachable. The curve then goes back to being derived from the inventory.

- First placement: automatic layout (tree from the STP/gateway root, otherwise elkjs force-directed), then the user adjusts and positions are saved. **Decided, not built.** What exists instead, and makes the initial heap bearable: **dashed frames derived from the *place* field** of each device. One zone per *place* value, whose frame is **measured** on the real height of the nodes (a switch loaded with ports far exceeds a guessed height, and a frame too short lets the device overflow its own place — yet the frame is the only thing that says "these switches are in the same place"). Moving a device into a place makes it change frame. It is not a layout, but it is the **structure** a layout should respect, and it comes from data the operator already keeps up to date.
- Real-time updates: **shipped, but not by the mechanism described**. There is no WebSocket pushing deltas: an **SSE** stream announces "this site has changed" and the screen reloads through the API ([[Web interface]], § *Real time*). A refinement specific to this screen: the stream **suppresses the echo of its own writes** (tab header), without which a node drag-and-drop would have triggered a reload at every pixel.

> [!note] The footer legend has been removed
> The screen carried under the plan a ten-line paragraph that described everything:
> the meaning of the arrows and on which port counters are read, what the dashed
> frames surround, hovering a link, dragging a switch, the
> Ctrl/⌘ + wheel zoom, the "Fit" button, and the switch to compact view
> below 60%. It was **deleted**, not moved ([[Web interface]], § *Explanatory
> text lives behind a help*).
>
> It is not a loss of information but an observation about what the page already
> shows: the arrows are drawn, the frames carry the name of the place, the link
> tooltip gives the detail on hover, the zoom buttons are there with their
> `title`, and the compact view can be seen by zooming. What that paragraph really
> brought — **on which port counters are read** — is in any case a
> detail the footer legend carried poorly. Now that throughputs
> come from counters, that sentence will have to come back — behind a help, and
> not at the bottom of the page.

## Interactions

- Drag to place: **shipped** (pointer events, temporary position during the drag, save on release, and a click without movement navigates to the switch's page). **Locking** a position remains not built.
- Click on link → detail (ports on both sides, speed, errors, link event history): **partial** — both ends, speed and note are in the link tooltip and in the diagnostics panel; there is no per-link history, for lack of link events other than creation and deletion.
- Click on node → same panel as the devices page: **shipped**, in the form of navigation to the switch's page rather than a side panel.
- PNG/SVG export of the map (operations documentation): **decided, not built**. There is no export in the screen.
- Full-screen "wall" mode without chrome for NOC display: **decided, not built**. The two reasons for postponing it have been lifted since 29/07/2026 — LLDP discovery and measured load — and that is what now makes it conceivable: it is the only use where nobody is there to doubt what is displayed, hence the only one that required that nothing in it be invented.
