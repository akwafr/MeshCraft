---
tags: [frontend, spec]
---

# Port rendering — visual specification

Each port of a device is a **small square** in the device's card ([[Web interface]]). Goal: read at a glance the physical state (up/down) and the VLAN configuration of an entire faceplate, even on a 48-port switch, even on mobile.

## Where this page stands

It is a **specification**, and the shipped rendering departs from it in technique more than in intent. What it describes remains the target; the table says where each feature stands.

| Feature | Status |
|---|---|
| Square coloured by VLAN, full faceplate readable at a glance | **shipped** — `PortSquare` (`web/src/netui.tsx`), visual spec in `web/src/network.ts` |
| Trunk as **stripes** of member VLANs, max 4 colours | **shipped**, but as a CSS `repeating-linear-gradient`, not an SVG `pattern` |
| Layout in 2 interleaved rows beyond 16 ports | **shipped on the switch page**; flat on the other screens |
| Number written in the square when zoomed | **shipped**, but it is the **port** number, not the VLAN |
| Visual filtering by VLAN from the legend | **shipped on hover**; click to lock the filter is not wired |
| PoE flagged on the square | **shipped**, as a small square — no lightning bolt, no colour by consumption |
| Per-port **drift blinking** (`pending` / `drift`) | **shipped** — a feature absent from this spec, described below |
| **SVG**: one `<svg>` per faceplate, one `<g>` per port, memoised `pattern`s | **decided, not built** — rendering is in DOM (§ *Implementation*) |
| Border encoding physical state (up/down/admin-down/unknown) | **decided, not built** — it is the **fill** that encodes the link |
| Central dot for the native VLAN | **decided, not built** — a trunk's native VLAN is currently **invisible** |
| Traffic overlays, port alerts, LLDP chevron, toolbar toggles | **decided, not built** — but the LLDP neighbour is now **in the square's tooltip** ([[LLDP neighbours]]) |
| Palette generated in **OKLCH** with constrained luminance | **decided, not built** — 16 fixed hues, the first 8 checked for CVD |
| Configurable sizes (12 / 16 / 24–32 px), faceplate zoom, pinch | **decided, not built** — three hard-coded sizes: 38 / 13 / 9 px |
| Virtualisation of the card list | **decided, not built** |

## The square

Base size **16×16 px** (dense grid: 12 px; zoom/touch: 24–32 px), 2 px rounded corners, 3 px spacing.

> [!note] Size is not a setting, it is a per-screen constant
> Three hard-coded values, chosen for what each screen must answer:
> **38 px** on the switch page (where one clicks and selects), **13 px** on the
> inventory cards (where one scans a fleet), **9 px** in a [[Weathermap]] node
> (where the faceplate is only a hint of shape). The height is
> `0.92 × width`, the border 1 px and the radius 3 px.
>
> Faceplate zoom and pinch are not built, and that is what makes the switch page
> **unusable by finger**: multiple selection relies entirely on ⇧ and Ctrl
> ([[Web interface]]). A touch zoom is therefore not a comfort, it is the
> prerequisite for mobile.

Layout faithful to the real faceplate: `rows` from the model catalogue ([[Connectors]]) — 2 interleaved rows (1,3,5… / 2,4,6…) as on the hardware, SFP ports separated by a small gap and with square corners to distinguish them.

**Shipped for interleaving**, on the switch page: a group of **16 ports or more** is split into odd on top / even below, like the hardware. That is what makes the faceplate recognisable, and it is also what makes **range selection** treacherous — hence the rule in §*Selection* of [[Web interface]]: the range is measured on the **physical** order, never on the displayed rows. Inventory cards and plan nodes, for their part, lay the squares out **flat**: at 13 and 9 px, two rows would cost height without making the faceplate more readable.

The distinction of **SFPs**, on the other hand, is almost absent: the faceplate's groups are well separated (each group has its heading), but the square itself has neither a dedicated gap nor square corners. The only hint is the number, prefixed with an `S` (`sfp2` → `S2`). On a switch whose last four cages are optical, the faceplate does not show it.

### Physical state → border

| State | Border |
|---|---|
| **up** (ifOperStatus=up) | 2 px **white** (dark theme) — slight halo |
| **down** | 2 px **grey** `#555`, darkened fill (60%) |
| **disabled** (admin down) | grey + discreet diagonal pattern |
| **unknown** (device unreachable) | dotted border |

> [!warning] It is not the border that encodes the state, it is the FILL
> The shipped rendering decides otherwise, and one must know it to read a
> faceplate:
>
> | Real case | Rendering |
> |---|---|
> | configured **and** link present | **filled** square (colour or stripes) |
> | configured, **no** link | **transparent** square, border in the VLAN's colour |
> | unmanaged / free | neutral background, neutral border |
> | device unreachable | the whole faceplate at **35% opacity** |
>
> In other words: *filled = it is alive*, *outline = it is planned but nothing is
> plugged in*. That is more readable than the spec in the most frequent case —
> spotting the dead sockets in a room — because the filled/empty difference shows
> at 13 px whereas a 2 px border does not.
>
> What the spec had in addition, and is genuinely missing: **admin-down** and
> **unknown** have no distinct rendering. A deliberately disabled port presents
> itself as a merely unplugged port, which is the costliest confusion on the list
> — one goes looking for a cable for a port that was cut on purpose. The spec's
> diagonal pattern is the right answer and remains to be done.
>
> The source of the fill is the port's `link` field, and it is indeed
> **observed** data: `if_state` polling writes it from `ifOperStatus`, and the
> transitions produce real `port.up` / `port.down` / `port.flapping` events
> ([[Monitoring]]). On this point, the square tells the truth of the network.

### ACTIVE is not MANAGED — the defect that showed an entire faceplate as dark

This is the most visible correction on this page, and it concerned the first screen
an operator opens after an adoption.

The fill is decided on the **intent** (access, trunk); the link state only chooses
between *filled* and *outline*. A port **with no intent** therefore fell into no
branch and came out looking like a free port — **whatever its link**. On a freshly
adopted switch, none of whose ports has an intent yet, the faceplate was therefore
displayed entirely dark with a 10G uplink plugged in.

> [!bug] Observed on the fleet, 29 July 2026
> ```
> lab-crs326   sfp-sfpplus2  link=10G  mode=∅   → shown FREE
> lab-css326   SFP2          link=10G  mode=∅   → shown FREE
> sw-labo-1930 GE49…GE52     link=10G  mode=trunk → shown active
> ```
> The two MikroTiks had just been adopted in "push" mode: `devices.ports` only
> carried what telemetry had written there (`link`), no intent. The 1930,
> configured, displayed correctly — which made the defect all the more misleading:
> the screen seemed to work.

The confusion was between two notions the same square carries:

| | Nature | Source |
|---|---|---|
| **managed** | an **intent** of meshcraft | `devices.ports[].mode` |
| **active** | an **observation** | `ifOperStatus` + `ifHighSpeed`, written into `devices.ports[].link` by collection |

A fourth square state therefore now exists: **link active, unmanaged** — square
**filled in slate grey**, taken OUTSIDE the VLAN palette. Giving it a palette hue
would make one read a configuration that does not exist; the filled square says
what it must say, "the link is active", in the same language as everywhere else.
The tooltip names it in full, and the faceplate legend carries the entry.

The same confusion was in three other places, all fixed along with it:

- the **active port count** (`deviceRates`) — it required an intent, so it
  announced "0 active ports" on both MikroTiks;
- the **ranking of the busiest ports** in [[Monitoring]] — empty on any
  unconfigured device;
- the controller-side **fallback traffic curve** (`portRates`), for the same reason.

"All VLANs" uplinks remain excluded from **rates** (their traffic is already counted
on the access ports that feed it), and that exclusion is indeed read on the intent —
for lack of another source. This is accepted and written in the code: an unmanaged
port cannot be recognised as an aggregation link, so it is counted.

### A DEVICE's throughput is not a SITE's

Second half of the same confusion, found while fixing the first. The rate
calculation discarded "all VLANs" trunks — the aggregation links — on the grounds
that "their traffic is already counted on the access ports that feed it".

The reasoning is right for a **site total** and wrong for **one device**, and the
fleet showed it: the only four active ports of the lab 1930 are its four SFP+
cages, **all** full trunks. Its displayed throughput was therefore zero while the
metric existed and was being reported — which reads as broken collection.

On a single device, there is nothing to correct, because the two directions are
counted **separately**: a frame entering through GE1 and leaving through the uplink
counts once on the way in and once on the way out, never twice in the same sum.
`rx` is "everything entering this switch", `tx` "everything leaving it".

Two functions, then, for two questions:

| Question | Function | Rule |
|---|---|---|
| how much goes through **this switch**? | `deviceRates` | all active ports, both directions separate |
| how much goes over **this site**? | `siteRates` | without **inter-switch** links, otherwise a frame crossing two devices is counted twice |

The controller's traffic curve applies the same rule on the series — the two must
tell the same story, otherwise the tile and the curve on the same screen
contradict each other.

> [!warning] The exclusion is read on the INTENT, and that is its weakness
> "Inter-switch link" is approximated by `trunk` + `tagged: all`, for lack of
> another source. That has two consequences to know:
>
> - an aggregation link nobody declared as such is **counted**;
> - a site's **ingress** link — the one going up to the router, whose traffic is
>   precisely what a site total should count — is a full trunk too, hence
>   **excluded**. On the reference fleet, where all active ports are full trunks,
>   the site total is therefore **zero**: it is consistent with the rule, and it
>   is not what one wants to read.
>
> The right discriminator has existed for a short while and is not used here
> yet: **LLDP neighbours** ([[LLDP neighbours]]). A port whose neighbour is
> another device **of the same site** is an internal link — to exclude; a full
> trunk whose neighbour is unknown to meshcraft is an **ingress** link — to
> count. That is the next step, and it is better read here than found by
> recalibrating a constant while hunting for why a total is wrong.

### VLAN configuration → fill

Each VLAN's colour is defined site-wide in the settings ([[Web interface]]).

| Case | Rendering | Status |
|---|---|---|
| Access (a single VLAN) | square **filled** with the VLAN's colour | **shipped** |
| Multi-VLAN trunk | **diagonal stripes** alternating the colours of the tagged VLANs (max 4 visible) | **shipped** — 135°, 3 px stripes (4 px on the large faceplate); the `+n` in the tooltip is not done |
| PVID / native (trunk with untagged) | stripes of the tagged ones + **central dot** (Ø 6 px, 1 px dark rim) in the native VLAN's colour | **not built** |
| "all vlans" trunk | neutral grey/white stripes | **shipped differently** — the stripes take the **native VLAN**'s colour, and the neutral grey `#94a3b8` only if that native is unknown to the site |
| Not managed by meshcraft | empty square, border only | **shipped** |

The central dot always means "this port's untagged VLAN": on an access port it can be omitted (the whole square already expresses it).

> [!warning] Without the central dot, a trunk's native VLAN shows nowhere
> The native logic **exists** and is even finer than the spec: a trunk that
> designates no native is not a trunk without a native — the hardware puts
> VLAN 1 there, and that is what reading back the device will always report. The
> code knows it (`nativeVlan`) and uses it to say whether a port *carries* a
> VLAN.
>
> But nothing **draws** it. On a trunk `20, 30, 99` with native `10`, VLAN 10 is
> invisible: the stripes only show the tagged ones. Yet a trunk's native is
> precisely what one comes to check when something does not get through, and it
> is also what is most easily overwritten without being seen.
>
> It is the most important gap on this page — more than SVG, more than the
> overlays: a square can today **keep quiet** about part of the configuration it
> claims to show.

A shipped detail the spec had not foreseen, which settles an awkward case: a trunk carrying **only one** VLAN would receive a single-colour stripe, hence indistinguishable from an access. A second hue is therefore **derived** from the first (35% mix with the background) so that the square stays striped. A port's mode must be visible without reading the tooltip.

### Optional overlays (toggles in the toolbar)

- **PoE**: 6 px ⚡ lightning bolt in the top-right corner if the port delivers PoE (colour = consumption vs budget). **Shipped, more discreetly**: a 5 px square at the bottom right, in the ink colour, shown when the port has a link **and** draws power. No lightning bolt, no encoding of consumption, and no toggle — it is always displayed. Consequence to know: a port **configured** for PoE but with no device plugged in shows nothing. The square says "PoE comes out of here", not "PoE is planned here".
- **Traffic**: opacity/pulse proportional to throughput ([[Monitoring]] data). **Decided, not built** — and the prerequisite is missing: per-port rates are not yet derived from SNMP counters (see the callout on simulated rates in [[Weathermap]]).
- **Alerts**: blinking red dot if there is a recent event on the port (flap, err-disable) — linked to the [[Event log]]. **Decided, not built — but the data now exists.** Flap correlation is shipped and proven on hardware: a flapping port produces a `port.flapping`, and its stabilisation a `port.flap_cleared` ([[Monitoring]]). It is therefore the cheapest overlay to build, and the most useful: flapping is exactly the defect one does not find by looking at a faceplate, because the port is *up* half the time.
- **LLDP uplink**: ▲ chevron if a neighbour is known ([[Weathermap]]). **Decided, not built** — but its prerequisite is: collection has existed since 29/07/2026 ([[LLDP neighbours]]), and the neighbour shows **on hover** of the square (system name, remote port, since when it has been seen, and what the match relies on).

  Why the tooltip first and the chevron later: the square is 13 px on the Equipment page and already carries four pieces of information — the VLAN by colour, the link state, PoE by a dot, drift by blinking. Adding a marker requires deciding its **priority** against the other four, and that is not a collection decision. The tooltip, for its part, competes with nothing.

  What the tooltip does NOT do, and what one must know before concluding anything from a silent square: **the absence of a neighbour is not the absence of a cable**. Three ordinary causes, all observed on the fleet — the device opposite does not speak 802.1AB (MikroTiks running SwOS), this one runs LLDP only on some of its interfaces (**two out of twenty-nine** on the lab CRS326), or no snapshot has arrived yet. The first two causes are flagged by an "LLDP ?" marker on the device's card; the third cannot be, since the MIB does not say *why* it does not list a port.

### A shipped overlay the spec had not foreseen: drift

Two blinking classes apply to **the port**, not to the device:

- **`pending`** (blue): meshcraft holds a configuration the device does not have yet;
- **`drift`** (orange): the device was modified outside meshcraft.

The blinking applies **only to the ports concerned**. Making the whole faceplate blink for one differing port would mean a long search — and it is the same reasoning as the device status dot, where reachability takes precedence over synchronisation ([[Reconciliation]]). A static fallback is provided under `prefers-reduced-motion`: information must not disappear because animations were turned off.

It is the most useful of the existing overlays, because it answers the question no VLAN colour answers: *is what I see what the device carries?*

### The question the square does not even ask: *does it TAKE EFFECT?*

Drift blinking answers "is what I see what the device carries?". There is a
question underneath, and it is the most unpleasant one: **does what the device
carries actually act on the traffic?**

On both MikroTik platforms, the answer is sometimes no, and a filled square then
lies twice — no drift, no `pending`, a clear colour, and nothing flowing:

| Real case | What the square draws |
|---|---|
| SwOS, port in VLAN mode `disabled` | the colour of its native VLAN — which the port ignores, since it does not consult the table |
| SwOS, port in "Force VLAN ID" | trunk **stripes** — whereas the port puts all its inbound traffic into its single native VLAN |
| RouterOS, bridge without `vlan-filtering` | the full segmentation of a faceplate that lets everything through everywhere |
| RouterOS, `ingress-filtering: false` (the **default**) | a watertight segmentation, whereas it is only so on egress |
| RouterOS, access port with `frame-types: admit-all` | an access port — out of which a workstation can exit by tagging its own traffic |

**Shipped, but not in the square**: these facts come out of the connectors as
**sentences** (`driver.State.Notes`), are recorded in `last_apply.notes`, and are
displayed by `ConnectorNotesBanner` at the bottom of the switch page's banners —
after the spared ports and the unsupported intents, because it calls for no
decision.

> [!warning] The banner existed on the server side from the start, and nothing displayed it
> `rememberApply` has written these sentences into `last_apply.notes` since the
> MikroTik connectors shipped, and its comment even says that "the screen must be
> able to say it in the same place it says the spared ports". No screen read
> them: they were produced, stored, and thrown away. It is the worst of the three
> silences on this page — the spec's gap (§*Physical state*) sends one looking
> for a cable for a port cut on purpose, this one lets one conclude "this switch
> is segmented" about a switch that is not.
>
> What remains to be done is **in the square**: nothing visually distinguishes a
> port whose configuration is inert. The diagonal pattern the spec reserves for
> admin-down is the right vocabulary, and the case is of the same family — "it is
> not what it looks like". A banner names the ports; a 48-square faceplate still
> does not say *which ones*.

*(Not to be confused with the three other kinds of silence, which answer one
another: `skipped` = meshcraft REFUSED, and the operator can force; `unsupported` =
the platform cannot write it, and there is nothing to force; `notes` = it is
written, and it does not take effect.)*

## Implementation

**SVG per device** (one `<svg>` per faceplate, one `<g>` per port), no DOM per sub-element:
- stripes = SVG `pattern`s generated per combination of VLANs (memoised — a given trunk profile reuses its pattern);
- hover/click events by delegation; rich tooltip (name, profile, named VLANs, speed, neighbour, MACs);
- real-time update: the WS `port_state` message only re-renders the `<g>` concerned (React memo per port);
- a 48-port switch = ~50 SVG nodes → hundreds of devices per page stay smooth; beyond that, virtualisation of the card list.

> [!warning] The shipped rendering is DOM, and the performance reasoning no longer holds
> Nothing in this section is built. The actual rendering:
>
> - **one DOM element per port** — a `<button>` if it is clickable, a `<span>`
>   otherwise — with inline styles. A 48-port faceplate = 48 elements, not one
>   `<g>` inside an `<svg>`;
> - the stripes are a **CSS `repeating-linear-gradient`** at 135°, recomputed on
>   every render, with no memoisation and no reused `pattern`;
> - hover and click are **per-element** handlers, not delegation;
> - the tooltip is a browser `title` attribute: it carries the name, the label and
>   the drift state. No profile, no named VLANs, no speed, no neighbour, no MACs —
>   the last two not existing yet anyway;
> - there is **no `React.memo` per port**, and there is no `port_state` message:
>   real time is an SSE stream announcing "this site changed", after which the
>   screen **reloads and re-renders the whole faceplate** ([[Web interface]]);
> - no **virtualisation** of the card list.
>
> The consequence is not visible today — a test site holds up easily — but the
> argument "hundreds of devices per page stay smooth" rested on SVG and per-port
> updates. With one DOM element per port and a full re-render on every site
> change, a fleet of two hundred 48-port switches makes ten thousand elements
> re-rendered on every port transition. The first symptom will be the inventory,
> not the device page.
>
> Choosing the DOM is not a mistake: it made it possible to get multiple
> selection, drift blinking and keyboard accessibility (a port is a real
> `<button>`) without reimplementing in SVG what the browser provides. It is the
> **load reasoning** that must be redone, and the order to follow is probably the
> reverse of the spec: memoise per port and virtualise the card list first —
> which can be done in DOM — and only move to SVG if that is not enough.

```
   up access V20    up trunk 20+30+99, native 10    down
   ┌─────────┐        ┌─────────┐                 ┌─────────┐
   │█████████│        │▨▨▨▨▨▨▨▨▨│ stripes         │         │ grey,
   │█████████│ green  │▨▨▨●▨▨▨▨▨│ + native dot    │         │ dark
   └─────────┘        └─────────┘                 └─────────┘
```

*(The central dot in the second case is the target; it is not drawn today.)*

## Under the faceplate: the TELEMETRY panel

What the port square cannot carry is displayed **under** the faceplate, on the
device page — and not in the right-hand panel, which describes the selected port:
these quantities describe the **device**, and putting them on the right would make
them disappear as soon as one clicks a socket.

| Tile | Source | Absent when |
|---|---|---|
| Temperature (one per sensor) | `meshcraft_temperature_celsius`, labelled `sensor` | the platform does not publish it |
| CPU load | `meshcraft_cpu_load_percent` | same — observed on SwOS: the firmware does not measure it |
| Memory used | `memory_total − memory_free`, in % | same |
| Uptime | `meshcraft_uptime_seconds` | rarely: every SNMP agent serves it |
| Frame errors | `rate(if_in_errors) + rate(if_out_errors)`, **summed over all ports** | the counters channel is not collected |

Three curves accompany the tiles (temperature, CPU + memory on the same percentage
scale, errors), over an adjustable window — 1 h, 6 h, 24 h. The window is state of
the page and not of the URL: it is a reading setting, not a place one comes back to.

> [!important] THREE ABSENCES ARE DISTINGUISHED, and an empty frame distinguishes none
> That is the rule of this panel, and it comes from an operational observation:
> "no curve" reads as "collection broken", so the screen must say **which** of
> the three causes applies.
>
> | What is missing | What the screen says |
> |---|---|
> | no series storage on the instance | a sentence, and no frame — `MESHCRAFT_METRICS_URL` is empty, collection runs but nothing is kept |
> | the platform does not publish the quantity | the **localised** explanation for the metric declared in `monitoring_unavailable` (`monitoring.unavailable.*`), with the mention *inferred* when the absence has not been observed on hardware |
> | the device is not collected | "no measurement" — no designated collector, silent agent, tunnel down. It is not a zero |
>
> The second row required WIDENING the granularity of the reasons: they were
> declared per channel (`health`, `if_counters`), and `health` covers both
> temperature and CPU load. Yet a CSS326 publishes the former and not the latter
> — declaring `health` unavailable would have made its temperature disappear.
> Connectors therefore now declare `cpu` and `memory` separately, and the screen
> looks from the most specific to the broadest.

A port's traffic curve, in the right-hand panel, comes from the **same** route: the
octet counters derived through `rate()`, one point every 15 s at the finest step. It
used to be manufactured by jitter around the current value — thirty points of a past
that had never been measured (see [[Weathermap]], the callout on `jitAt`).

## Faceplate legend

Under the faceplate, a strip of chips says what the colours and patterns mean: access on active link, trunk (striped), configured but link down, free, PoE active. It also carries, at the start, the **selection help** — the four gestures of ⇧ and Ctrl/⌘ ([[Web interface]], § *Port selection on the faceplate*).

It is deliberately **there** and not in the side panel, where these shortcuts used to live as three lines of grey text. Two reasons: the help for a gesture must sit against the object it manipulates, and the side panel only shows that paragraph when **nothing** is selected — that is, at the only moment one is not using it. The general principle — explanatory text lives behind a help affordance, the observation stays on screen — is written in [[Web interface]].

## Accessibility & readability

- Colour is never alone: systematic tooltip — **shipped** — and "labels" mode (number written in the square at zoom ≥ 24 px) — **shipped, with a difference**: the number appears automatically as soon as the square reaches 24 px, hence only on the switch page, and it is **not a mode** one switches on. And it is the **port** number, not the VLAN. The distinction matters: on a dense faceplate, the port number helps *name* what one clicks, the VLAN number would help *read* the configuration without hovering over each square. The second is the one missing.
- VLAN palette generated in OKLCH with constrained luminance — **decided, not built**. What is shipped: **16 fixed hues**, of which the **first 8** were checked by hand for colour vision (separation of neighbouring pairs on a dark background). A site with up to 8 VLANs is therefore readable by construction; beyond that, identity relies on the number in the square, the tooltip and the legend. A VLAN's colour is stored **per VLAN** in the database anyway: the palette only *proposes* the first unused hue, and the operator can choose any other — in which case no contrast guarantee applies. That is the strongest argument for OKLCH generation: it would make the guarantee independent of human choice.
- Collapsible legend of the site's VLANs at the bottom of the page, clickable to **filter**: highlight only the ports carrying a given VLAN — very effective to audit "where does VLAN 30 go". **Shipped on hover**: hovering a legend row drops to 12% opacity every port that does not serve that VLAN, across the whole page. The effect is exactly the one described, and it is immediate.

  What is missing is the **click**: the filter cannot be locked. The component already accepts a click handler on the row, but no screen passes it one. Three consequences: one cannot release the mouse to go and look elsewhere, the filter is unusable **from the keyboard**, and it is unusable **by touch** — where there is no hover at all. Wiring the click is one line per screen, and it is what makes the feature accessible rather than merely pretty.
