---
tags: [frontend]
---

# Web interface

**React + TypeScript** SPA, served by the [[Controller]] binary (`go:embed all:webdist`), mobile-first — the choice against Flutter is argued in [[Technical stack]]. Real time through an event stream, data through a typed REST API.

## Where this page stands

| Question | Status |
|---|---|
| React + TS SPA embedded in the controller binary | **shipped** — `internal/api/spa.go` |
| Current site in the URL (`/s/{site}/…`), shareable links, back/forward | **shipped** — `web/src/App.tsx` |
| Site picker permanently in the header, limited to authorised sites | **shipped** — `ListSites` filters in SQL by `user_roles` |
| Equipment page with port faceplate coloured by VLAN | **shipped** — [[Port rendering]] |
| Multiple port selection (click / ⇧ / Ctrl / ⇧+Ctrl), "mixed", compacted ranges, protected PoE | **shipped**, and it is the most polished part of the screen |
| Applying a selection in **one** transactional request, **one** log line | **shipped** — `PUT /api/devices/{id}/ports` |
| Settings screens: VLANs, site management, proxies & ranges, adding a device, credentials | **shipped** |
| Occupancy of the management range shown in plain words | **shipped** |
| **Client generated from OpenAPI** | **decided, not built** — there is no OpenAPI spec; the client is written by hand and typed by hand |
| **WebSocket `/ws/ui`** with deltas applied to the cache | **replaced** — it is an SSE stream that announces the change, and the client reloads (§ *Real time*) |
| Installable **PWA**: manifest, service worker, push | **decided, not built** — none of that exists |
| Dedicated **Log** tab | **replaced** — the log lives in **Monitoring** and on the **Dashboard** |
| Port selection **across devices** | **decided, not built** |
| Tabbed device page (Config / Ports / Statistics / Events / **Backups**) | **partial** — a single page, without tabs; there is **no configuration backup** at all |
| Bottom tabs on mobile, pinch to zoom the faceplate | **decided, not built** — see § *Mobile* |

## Navigation

The actual navigation is in **horizontal tabs**, and the side menu is reserved for two sections:

```
┌────────────────────────────────────────────────────────────────────────┐
│ meshcraft  Dashboard Equipment Ports Topology Monitoring Configuration │
│                              [Site: Lyon Workshop ▾]  nicolas  [⇥]     │
├──────────┬─────────────────────────────────────────────────────────────┤
│ (side    │                                                             │
│  menu    │              page content                                   │
│  only in │                                                             │
│ Config & Add)│                                                         │
└──────────┴─────────────────────────────────────────────────────────────┘
```

- **Site picker** permanently in the header: list limited to authorised sites ([[RBAC]], filtered in SQL). It is a dropdown **with a search field** and keyboard navigation (type to filter, ↑/↓, Enter, Escape) rather than a `<select>`: a multi-site instance quickly has too many for a native menu. Changing site **keeps the current screen**.

  Two points of the design that are not there: the **"All sites"** entry for global roles (there must always be a current site), and **site creation from the header** — it lives in *Configuration → Sites*. Nor is the notification bell. What is there in addition: the user's name, a **global admin** badge when they are one, and a sign-out button.
- The current site is in the URL (`/s/{site}/...`): shareable links, correct back/forward. **Shipped**, with two refinements learnt through use: the last site is remembered in `localStorage` so that instance screens keep a context, and an **unknown URL falls back to the dashboard** of the current site — not to the site list, which gives the impression of having been signed out.
- Actual tabs: **Dashboard**, **Equipment** ([[Port rendering]]), **Ports**, **Topology** ([[Weathermap]]), **Monitoring**, **Configuration**.

> [!note] Two more tabs, one fewer tab than the design
> - **Dashboard** answers "where does my fleet stand?", which is the question one
>   asks on arrival. That is also why it is the fallback for unknown URLs.
> - **Ports** is a **site-wide cross-cutting** view: all the ports of all the
>   devices in a searchable table (port, description, VLAN, neighbour). The
>   faceplate answers "what does this switch do?", the table "where is that
>   plugged in?" — and the second question cannot be answered by opening forty
>   device pages.
> - **Log** does not exist as a tab: the filterable [[Event log]] is **in
>   Monitoring**, next to the detailed state it explains, and its last six lines
>   are on the Dashboard. Separating the log from the state forced back-and-forth
>   trips to connect an alert to its cause.
> - **Profiles** is not a tab either: socket, device and switch profiles are in
>   *Configuration* ([[Profiles and groups]]), where VLANs, tags and credentials
>   live — these are objects one defines, not screens one monitors.
>
> The side menu therefore only appears in **Configuration** and in **Add
> equipment**, the latter having its own menu while keeping the Equipment tab
> active: one gets there from the inventory, and comes back to it.

## Equipment page (main)

Each device = a **card**: hostname, model, **place** within the site, status dot, and the **port faceplate** as small squares coloured by VLAN — full specification in [[Port rendering]]. The card footer gives the number of active ports, the throughput and the PoE drawn.

Two departures on the card: there is **no vendor logo**, and the **management IP** is not on it (it is on the device page). And the displayed status is not `up`/`degraded`/`unreachable` but four labels — **ONLINE**, **ALERT**, **UNREACHABLE**, **UNKNOWN** — plus a fifth that was not planned and that matters: **OUT OF PLAN**.

> [!important] Reachability and conformity to the plan are two axes, not one
> A device that answers perfectly but whose management addressing does not follow
> the site's plan is not broken: it is **to be migrated**
> ([[Management addressing]]). Red is therefore reserved for what no longer
> answers, and the deviation from the plan is a warning.
>
> The dot also carries a **third** axis, synchronisation: blinking blue for
> "configuration in progress, not yet applied", blinking orange for "the device
> was modified outside meshcraft". The arbitration between the axes is written
> once and shared by all screens: an **unreachable device keeps its red dot** —
> announcing "configuration in progress" on a silent switch would be false,
> nothing is in progress; otherwise a configuration deviation takes precedence
> over green, because an "online" device whose configuration is not what one
> believes is not nominal. Copying this rule into each page would guarantee that
> one screen ends up saying green where another says orange, and the operator
> would believe the most reassuring one.

- Sorting/grouping by location, model or status; instant search. **Not built on this page**: the card grid has neither sorting nor search. Cross-cutting search is on the **Ports** tab, and grouping by **place** is on the [[Weathermap]], as frames.
- Click on a port → side panel: details and editing (socket profile or overrides); multi-selection of ports. **Shipped** on the switch page. The panel carries state, speed, PoE and neighbour; the **seen MACs** and the **LLDP neighbour** do not exist (no BRIDGE-MIB or LLDP collection — [[Monitoring]]), and the neighbour shown elsewhere comes from the **entered** topology ([[Weathermap]]). Multi-selection **across devices** — the UniFi gesture — is not built: the selection is that of one faceplate.
- Click on the device → device page. **Shipped, as a single page** rather than tabs: summary, faceplate, port or selection panel, PoE budget, tags, and the status banners. What is missing: a **Statistics** view (traffic curves are still reconstructed, see [[Weathermap]]), a per-device **Events** view, and above all **Config backups** — there is **no** configuration backup in the product at all, neither table nor route. That is worth knowing before announcing a hardening to a customer: the "a fresh backup before a `disruptive` change" rule in [[Service identity and rotation]] has nothing to rely on.
- An "undeployed changes" banner appears as soon as an intent differs from the applied state → **Deploy** button.

> [!important] Applying is automatic — the banner only appears if it failed
> An operational decision that departs from the design, and that is owned in the
> code (`internal/api/autoapply.go`): **configuring forty-eight ports in the
> interface IS the approval**. Asking for a second gesture on a product whose
> promise is to administer the fleet amounts to asking twice for the same thing,
> and to leaving a fleet durably different from what the screen shows.
>
> Changes are therefore grouped (a few seconds, otherwise changing forty-eight
> ports would launch forty-eight applies on a device that accepts only five
> sessions), serialised per device, and applied. What is **not** allowed for all
> that: the write safeguards remain intact (the management VLAN is never
> rewritten), and a failure is **never replayed in a loop** — one attempt per
> change, one event, and the device stays "in progress", visible and re-appliable
> by hand.
>
> The banner does exist, then, but it signals a **stuck** apply, not a pending
> intent; and it lists the ports concerned. The neighbouring banners follow the
> same logic: last apply error with a retry button, **ignored** ports with the
> option to force, settings **not supported** by the platform, device no longer
> monitored, device off plan. Every refusal carries its repair gesture — that is
> the product's pattern, and it is better than the *Deploy* button it replaces.
> The plan before confirmation, for its part, stays in [[Reconciliation]] for
> bulk gestures.

## Explanatory text lives behind a help affordance, not in the page

An interface decision taken after the fact, on a product that wrote a lot and well: the screens had become **unreadable from being so explicit**. Under every title, under every field, a grey paragraph (`.note`) said why it was so. The result is not that one reads too much: it is that **one no longer reads anything**, including the sentences that did need to be read.

The split is made on the **nature** of the text, not its length:

| Nature | Where it lives | Why |
|---|---|---|
| **Observation** — what a device carries, a drift, a spared port, an incomplete apply, a gap | **on screen**, in plain words | it changes from one page to the next, and is read every time |
| **What meshcraft will not do** although it has just been asked to | **on screen**, in plain words | hidden, the operator believes their order went out and goes hunting for a fault on a switch that never received the instruction |
| **Explanation** — the why, the teaching, keyboard shortcuts, vocabulary nuances | **behind the help** | read once, clutters the next thousand times |
| **Restating what the screen already shows** | **removed** | "grouped by device" under a list obviously grouped by device teaches nothing |

Three texts have thus **gone for good**: the "grouped by device" subtitle of the Ports tab, "rates measured on the ports of each link" in the Topology title, and the footer legend of the [[Weathermap]] (ten lines describing arrows, frames, zoom and the compact view). The others have moved, not disappeared.

> [!important] A long observation stays visible; a short explanation goes behind the help
> That is the meaning of the rule, and it is easily got wrong. The note issued
> from `mgmt_addressing.push_blocked` ([[Management addressing]]) is three lines
> long and stays **in plain view**: it says that the entered addressing is not
> written to the device. The connector notes (`observed.notes`,
> `last_apply.notes`, `skipped`, `unsupported`) stay in plain view too — they are
> sentences written **about real hardware** ([[Port rendering]] § *The question the square
> does not even ask*), not teaching. And the banners (`drift-banner`, `alert-banner`,
> `mgmt-warn-banner`) are not subtexts: they are not touched.
>
> The whole management switchover modal ([[Management addressing]]) is left
> intact for the same reason: each of its four paragraphs describes what an
> irreversible gesture is about to do to the hardware, at the moment one is about
> to do it.

### The `HelpTip` component

A single component, in `web/src/components.tsx`, placed **next to the title or label** it explains — never in the middle of a paragraph. A 15 px circled `?`, discreet at rest, which becomes more prominent on hover and focus.

- **A click, not a hover.** This product is used on a tablet in a wiring closet, and sometimes with the keyboard alone: help that only exists on `:hover` does not exist there. The click opens and closes, Escape and an outside click close. No hover **on top of** the click — the bubble would close when the pointer leaves whereas a click has just pinned it, and the gesture would stop having a predictable result.
- **A real `<button>`**, reachable by keyboard, with `aria-label`, `aria-expanded`, and the bubble linked through `aria-describedby` as `role="tooltip"`. The touch target is enlarged by a pseudo-element that pushes nothing — and whose overflow downwards is **limited to 4 px**, the margin `label .lbl` leaves before its field: beyond that, asking for help would steal the click meant for input.
- **Structured content**, not a string: the component takes `children`. Half of these texts are three sentences, some a list.
- **Computed position, not `absolute`.** These texts live mostly in the device page's side panel, narrow and already `overflow: auto`: a bubble positioned relative to its parent would be clipped there. It is therefore `fixed`, placed from the button's rectangle, brought back within the window, and **flipped above** when the bottom lacks room. `max-height: 60vh` with clean scrolling for the longest ones.

Two existing components received the same hook, so that the gesture is the same everywhere: `Modal` accepts a `help` rendered next to its title, and `Field` a `help` rendered next to its label. That is the right place for "what this field decides" — a paragraph placed **under** the field is read after input, when the question no longer arises.

> [!note] What remains to be done
> The browser's `title` tooltips coexist with this component. They already do the
> job on secondary controls (the re-read button, the collector and domain
> `<select>`s, the tag tokens) and have not been doubled with an icon: two
> mechanisms for the same sentence would be worse than one. But `title` is
> invisible on touch, and the day touch is dealt with for real
> ([[Port rendering]], § *Accessibility*), these `title`s will have to become
> `HelpTip`s.

## Port selection on the faceplate

Configuring a switch port by port does not scale: a classroom is twelve identical
sockets. The faceplate therefore accepts **multiple selection**, with the
conventions everyone already knows. **Fully shipped.**

| Gesture | Effect |
| --- | --- |
| click | selects this port alone, and sets the **anchor** |
| ⇧ click | takes the **range** from the anchor to here, replacing the selection |
| Ctrl/⌘ click | **adds or removes** this port |
| ⇧ + Ctrl/⌘ click | **adds the range** to the existing selection |

These four gestures are read **behind the faceplate legend's help**, and no longer in a paragraph of the side panel (§ *Explanatory text lives behind a help affordance*): a reminder of shortcuts is the textbook case of text one reads once. The icon sits against the faceplate it drives — not in the panel, which shows the port as soon as one has been clicked.

Two details decide whether it is usable, and both are in place:

- the range is measured on the **physical** order of the ports, not on the displayed
  rows. A faceplate of 16 ports or more is drawn interleaved (odd on top, even
  below) like the real hardware: "from 1 to 8" must take 1…8, not every other
  row;
- the panel announces **"mixed"** when the selected ports differ, instead of
  showing the first one's value. Reducing a heterogeneous selection to its first
  element would make the screen lie about what one is about to overwrite.

The selection panel compacts names into ranges ("1/1–1/8, 1/12"): a list of eight
names would force a recount to check one took what one thought. Three distinct
cases, one of which shows up in use: two neighbouring ports are written "a, b" and
not "a–b", because a dash for two elements reads more slowly than the comma it
replaces.

PoE is **never** forced on a port that has none: a selection can mix PoE copper and
SFP, and writing `poe: true` on an SFP would set an intent the hardware can never
satisfy — hence a permanent deviation in [[Reconciliation]]. **Shipped**, and the
capability comes from the faceplate's physical group, not from input.

A shipped detail the design had not foreseen, without which selection would be
dangerous: the patch is **merged** with **each port's own** intent. Applying a VLAN
to a range therefore does not erase the ports' labels or PoE settings along the way
— otherwise "set VLAN 99 on twelve sockets" would also have erased twelve
descriptions.

On the API side, the selection goes out in **a single request** (`PUT
/api/devices/{id}/ports`, body `{ports: {name: intent}}`), applied as a block
**within a transaction** and logged in **one line**:
`sw-labo-1930 · 8 ports (1/1, 1/2, 1/3 … 1/8) → access VLAN 99`. **Shipped, to the
letter.** A single gesture applied halfway would be worse than nothing — one would
have to guess which ports took it — and forty-eight [[Event log]] lines for a
single click would drown what they are meant to make readable. Two more bounds: a
batch is capped (512 ports), and when the batch's intents **differ** the log writes
"mixed configurations" rather than summarising the batch by its first element —
the same honesty as the panel.

## Real time

> [!important] It is not a WebSocket, and the client receives no data
> The design described "a single WebSocket `/ws/ui`" pushing `port_state`,
> `device_state`, `job_progress`… messages applied as deltas to the TanStack
> Query cache. What shipped is **an SSE stream** —
> `GET /api/sites/{siteID}/stream` — that announces **"this site changed"**,
> after which the client goes and fetches the data through the usual REST API.
>
> Only three messages: `ready` (application-level acknowledgement), `ping`
> (heartbeat, 20 s), and `change` — the latter carrying the list of affected
> categories (`port`, `device`, `sync`, `link`, `config`, `event`).
>
> **Why SSE rather than WebSocket**: a WebSocket leaves the HTTP pipeline on
> upgrade, and forces the permission check to be redone by hand — "exactly the
> kind of place where a permission gets forgotten" ([[RBAC]]). SSE is ordinary
> HTTP: same cookie, same middleware, and `requireSite(…, "viewer")` like any
> other route. And the browser **reconnects on its own**, with the delay the
> server dictates.
>
> **Why push nothing in the stream**: pushing the data would create a second
> serialisation path in parallel with REST; the two would diverge, and the screen
> would end up displaying what the API no longer returns. That is the only
> argument, and it is enough.
>
> **Why invalidate broadly**: the message does carry the categories, but the
> client does not read them — it invalidates **every query whose key contains
> the site's identifier**. A category → keys table would be a third copy of the
> model, to keep up to date with every added screen, and an omission there would
> produce a frozen screen — the hardest failure to attribute. The cost is nil in
> practice: react-query only reloads the queries actually mounted, and the server
> has already grouped the burst (400 ms).

Five mechanisms make this stream reliable, and each one answers an observed failure:

- **polling fallback** at 10 s as long as the first `ready` has not arrived. It is **not** the opening of the connection that counts: a buffering reverse proxy lets the stream open without letting anything through, and the screen would believe itself live in front of dead data. A display that lies is worse than a slow display;
- **50 s watchdog** — two missed heartbeats. An SSE stream can die without the browser seeing any error (a corporate NAT forgetting an idle connection); without this guard, the screen would stay frozen while believing itself alive;
- **echo suppression**: each tab identifies itself, and the server does not send it back its own writes. Without this, a drag-and-drop on the [[Weathermap]] triggered a reload on every pixel;
- **notification by middleware** rather than by a call in every handler: a handler that forgot to notify would produce a frozen screen, and the fault would be invisible on reading;
- **subscription cap** (256): beyond it, the server answers 429 and the client cleanly falls back to periodic polling. Refusing openly is better than accepting a connection one will not be able to feed.

The fallback cadence is aligned with the one at which data actually arrives — the proxy drains its buffer every 10 s — because polling faster would show nothing more while charging one request per open screen per second.

**Port events** (`port.up`, `port.down`, `port.flapping`), for their part, are very real: they are [[Event log]] events, produced by the controller's flap correlation from `ifOperStatus` ([[Monitoring]]). They simply do not travel *in* the UI stream: the stream says "something happened on this site", and the log says what.

## Mobile

- Responsive layout. **Partial.** Below 720 px, the side menu becomes a **horizontal bar at the top**; below 820 px, the tabs scroll horizontally; below 960 px, the side panels move under the content. There are **no bottom tabs**, no hamburger menu, no drawer — navigation stays at the top at all sizes.
- Port squares stay touch-friendly (target ≥ 32 px in port zoom, pinch to zoom the faceplate): **not built**. The square size is a per-screen constant ([[Port rendering]]), and above all **multiple selection relies entirely on ⇧ and Ctrl**, hence on a keyboard. The switch page — the main screen — is today **not usable by finger** for what it is meant for.
- PWA: manifest + service worker (offline shell), web push notifications for alerts ([[Monitoring]]). **Decided, not built**: there is no manifest, no service worker, no icons, no push. The only step in that direction is a theme colour in the `<head>`.
- Heavy actions (bulk deployment) remain confirmable on mobile but fine editing targets the desktop first. **That is the de facto state**, and it must be read as such rather than as a trade-off: mobile has not been dealt with, it has not been restricted.

## Notable Settings screens

- **Site VLANs**: id, name, **colour** (decisive for [[Port rendering]]), usage. **Shipped**; the palette **suggests** the first hue not yet used on the site, and the choice remains free through a colour picker. **Contrast is not checked**: a hand-picked colour can be unreadable, and nothing says so ([[Port rendering]], § *Accessibility*).
- **Site management**: administration VLAN (tagged or native), v4/v6 subnets, gateway, and the origin of the address — **external DHCP** or **meshcraft**, in which case the range it allows itself to allocate. **Shipped**, including the range occupancy shown in plain words ("37 assigned, 2 reserved, 111 free out of 150"): a range that fills up must be visible before it is full ([[Management addressing]]). An empty or inconsistent range is said to be so rather than rendered as zeros.
- **Proxies & scan ranges**: list filtered to what the user can see — instance proxies and those covering one of their sites ([[Proxy]]) — with status, version (update badge), local IPs and **coverage as chips**. **Shipped**, with the coverage modal that distinguishes the two permissions: a `site_admin` ticks or unticks **their** sites one by one, a `global-admin` redefines the whole coverage and can promote the proxy to an instance proxy ([[RBAC]] carries the full matrix, which is faithfully implemented). Declaration of the IPv4/IPv6 prefixes to scan **on a proxy** (the "Local" proxy = the instance itself), and generation of the deployment in one button — `docker-compose` or a **systemd install script** for hosts without Docker, each embedding its enrolment token and the chosen coverage ([[Proxy security]], [[Docker deployment and IPv6]]).
- **Add equipment**: a dedicated section with its own side menu — queue of **automatic discoveries**, **manual add by IP**, **add through serial port**, plus **catalogue model**, the **reset** tool and the **scan ranges**. **Shipped.** Adoption asks two **independent** questions: the configuration mode (pushed by the platform or imported from the device) and the **management addressing** — keep the device's own, or apply the site's plan with the allocated address shown *before* validating ([[Management addressing]], [[Discovery]]).
- **Site credentials**: the site's vault ([[Credentials]]), with generations and traced reveal ([[Service identity and rotation]] §12–13).
- **Discovery credentials**: the username/password pairs tried on devices encountered during discovery, in priority order. An **instance** screen and not a site one, reserved to the global-admin — discovery precedes the choice of site, and these credentials only serve to get into what meshcraft does not yet manage. They used to live in a site's "Add equipment" section, which made them impossible to find for anyone looking for them in the configuration; the old URL redirects there. The screen says **which kinds are actually tried and by what means** — a kind stored but never attempted would be the worst of displays — and flags secrets that became unreadable after an encryption key change, the only place where that case can be seen ([[Discovery]]).
- **Tags**, site **Import / Export**, **Pipelines**, and the instance screens **Sites**, **Users**, **Debug**, **Proxies** — the last three reserved to the global-admin for the first two. The menu footer carries the **version and build timestamp**, which answers "is my fix deployed?".

> [!note] The REST client is written by hand, not generated
> There is no OpenAPI spec in the repository: `web/src/api.ts` carries the verbs
> and **the types** by hand, alongside the Go structures. It is a real cost — two
> definitions to keep in agreement, and nothing that breaks when they diverge —
> but also the reason why the front end's types carry comments no generator
> would ever write, about what a field means and about the decisions not to be
> confused (see `MetricUnavailable`, or `MgmtConformity`). The day the spec
> exists, these comments will have to live on the Go side.
