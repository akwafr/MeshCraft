# meshcraft web — UI guide

What the interface actually does, derived from the source, not from an ideal we
wish we had built. Every rule below is followed by where it lives; every rule
that pages break is listed in the audit at the end.

The stylesheet is `web/src/styles.css` (2977 lines) and it is the authority on
tokens. The shared primitives are `web/src/components.tsx`, `web/src/netui.tsx`
and `web/src/ActionButton.tsx`. The colour *decisions* about device state live
in `web/src/network.ts` (`statusColor`, `deviceDot`, `portVisual`) — not in CSS,
because they are logic, and because sharing them is the only thing stopping two
screens from disagreeing about the same switch.

---

## 1. The palette

**The palette lives in `web/src/ui/tokens.css`, and nowhere else.** `styles.css`
declares no colour; it consumes tokens. That file is imported *after*
`styles.css` in `main.tsx`, and the order is load-bearing.

### Two themes, as of 2026-10-24

There used to be one. The guide said so at length, and said "do not guard
anything on colour scheme". That is no longer true, and the replacement rule is
*still* "do not guard anything on colour scheme" — for a different reason.

Each colour is declared twice:

```css
--bg: #0d1014;                        /* fallback */
--bg: light-dark(#eef1f6, #0d1014);   /* the real one */
```

A browser that does not know `light-dark()` drops the second declaration at
parse time and keeps the first, landing on the DARK palette — the theme
meshcraft shipped for its whole life. The failure mode on an old tablet in a
technical room is "this looks like last year", not "this has no colours".

So a page still never writes a `prefers-color-scheme` query: the token already
knows both answers. Write `var(--bg)` and it is correct in both themes. The one
thing that *does* need guarding is a hard-coded colour in a `.tsx` inline
style — those were all written against a dark ground and several are now
wrong on white. See the audit.

The theme is selected by `data-theme` on `<html>`: `"light"`, `"dark"`, or
**absent** for "follow the system", which is the default. `ui/theme.ts` owns the
attribute; an inline script in `index.html` sets it before first paint, so
someone who chose *against* their system preference does not get a full-screen
flash of the wrong theme on every load. Two media-scoped `<meta name=
"theme-color">` tags cover the browser chrome before any script runs, and
`theme.ts` rewrites them after.

### The tokens

| Token | Light | Dark | What it means |
| --- | --- | --- | --- |
| `--bg` | `#eef1f6` | `#0d1014` | Page ground; also the *recessed* surface inside a panel (inputs, `pre.code`) |
| `--bg-panel` | `#ffffff` | `#151a21` | A raised surface: card, modal, sidebar, popover |
| `--bg-hover` | `#e4e9f1` | `#1e242e` | Row/box hover, resting fill of secondary controls |
| `--border` | `#d3dae5` | `#273040` | Hairlines — now used far less; see §10 |
| `--text` | `#161b24` | `#dfe5ee` | Body ink |
| `--text-dim` | `#5a6475` | `#8a94a6` | Secondary ink, and "we have never looked at this" |
| `--accent` | `#2d5fe0` | `#4f7cff` | Interactive INK and edges: link, focus ring, selection, the pending-write dot |
| `--accent-fill` | `#2d5fe0` | `#2d5fe0` | A filled interactive SURFACE: primary button, active pill |
| `--accent-ink` | `#ffffff` | `#ffffff` | What sits on `--accent-fill` |
| `--danger` | `#c62a2f` | `#e5484d` | A fault |
| `--ok` | `#1b7a35` | `#46a758` | Confirmed good, observed |
| `--warn` | `#bf5b04` | `#f5a623` | An alert — we can see the device and something is wrong |
| `--indeterminate` | `#8a7a16` | `#e8d44d` | We cannot see — no observer on something that exists |
| `--mode-routing` | `#af1d57` | `#e73c80` | The navigation mode, NOT a fault — ADR-0013 §4, see §2 *Colours that are not status* |

**`--accent` split into two, and the reason is measurable.** White on `#4f7cff`
is 3.71:1 — the primary button had been failing WCAG AA for body text for as
long as the dark theme existed. Darkening `--accent` fixes the button and breaks
the link, which needs the *bright* blue to clear 4.5:1 against a near-black
ground. So: `--accent` is ink, `--accent-fill` is a ground. One fill serves both
themes (white on it is 5.49:1 either way). Hover is the half that cannot be
shared — dark brightens, light darkens, because darkening a fill on a near-black
panel reads as the button receding into it.

The light values are **not** a mechanical darkening of the dark ones; see §2.

| Token | Value | What it means | Where it is used |
| --- | --- | --- | --- |
| `--bg` | `#0f1218` | Page ground; also the *recessed* surface inside a panel (inputs, `pre.code`, `.plan-node`) | 26× CSS |
| `--bg-panel` | `#161b24` | A raised surface: card, modal, sidebar, popover, toast | 22× CSS |
| `--bg-hover` | `#1d2430` | Row/box hover, and the resting fill of secondary buttons, badges and chips | 26× CSS |
| `--border` | `#2a3342` | Every hairline: card edges, table rules, input frames, separators | 74× CSS |
| `--text` | `#dbe2ee` | Body ink | 27× CSS |
| `--text-dim` | `#8b96a8` | Secondary ink: `.note`, `.kicker`, table headers, labels, "we have never looked at this" | 51× CSS |
| `--accent` | `#4f7cff` | The product's blue. Two jobs: **interactive** (focus, selection, links, primary fill) and **pending** (meshcraft has a write that has not landed) | 62× CSS |
| `--accent-hover` | `#6890ff` | Hover on a blue fill, and one text case (`.stp-col--preferred .stp-col-count`) | 2× CSS |
| `--danger` | `#e5484d` | A fault. Unreachable, err-disabled, apply failed, destructive gestures | 12× CSS |
| `--ok` | `#46a758` | Confirmed good, observed | 7× CSS |
| `--warn` | `#f5a623` | An alert — **we can see the device and something is wrong with it** | 18× CSS |
| `--indeterminate` | `#e8d44d` | **We cannot see** — no observer, no vantage point on something that exists. Whatever it is may be perfectly fine. A GENERAL rule since 2026-08-08, field-level as well as device-level | 3× CSS, 4× TS |
| `--radius` | `8px` | The one corner radius | 42× CSS |

Counts are `var(--token)` occurrences; inline-styled uses in `.tsx`/`.ts` are
counted separately in the audit.

---

## 2. The spine: `--warn` vs `--indeterminate` vs `--danger`

This three-way split is the one genuine product decision encoded in the palette,
and the comment that introduces it (now `ui/tokens.css`) is worth reading in
full rather than paraphrasing:

> `--indeterminate`: "nobody can see this device right now". Deliberately a
> YELLOW distinct from the orange of `--warn`: an alert says the device has a
> problem, this says meshcraft has lost the vantage point and the device may be
> perfectly fine. Conflating them sends people to the wrong rack.

That last sentence is the operational stake. It is not an aesthetic preference.

### Keeping the split alive in light mode

`#e8d44d` on white is invisible, and the obvious fix — darken both inks — is a
trap: it collapses the orange and the yellow into one muddy brown and silently
deletes the distinction the paragraph above exists to protect. So the light
values re-separate them by **hue** instead of by brightness. `--warn` moves
toward red (28°), `--indeterminate` toward olive (52°), opening the gap to 24°
where dark holds only 13°.

What travels between the themes is the **perceptual distance**, not the
coordinates. If you ever retune either ink, the thing to re-measure is the gap,
not the swatch. `scratchpad/contrast.mjs` in the 2026-10-24 work was a throwaway;
the check it ran is not, and it is the one worth rebuilding if these move:
every ink against both grounds at 4.5:1 for text and 3:1 for a swatch, plus the
hue gap between those two.

All thirteen inks pass in both themes as of that date.

### `--indeterminate` is a general rule (2026-08-08)

The comment says "device", because the device dot is where it was first needed.
It is **not** scoped there. Yellow means **we have no vantage point on
something that exists**, wherever that occurs — a device, a proxy, a protocol
pill, a field in a side panel. The audit in §7 found four sites describing that
exact state and each had chosen a different answer; the ruling settles it in one
direction and the §7 entry records what moved.

The value of the token is entirely in what it *excludes*, so the boundaries
matter more than the rule:

| Not this | It is | Because |
| --- | --- | --- |
| "we have never looked" | `--text-dim` | Nothing was ever claimed, in either direction. Yellow would claim a vantage point was lost that was never held |
| "there is nothing to say" — empty, N/A, no single value | `--text-dim` | A question with no answer is not an observation problem |
| "this platform cannot do it" | `.info-banner` | Permanent, calls for no action; painting it teaches people to ignore the colour |
| "something is wrong" | `--warn` | We CAN see it. That is the opposite claim |

The two unseeable states are the pair that is easiest to merge and the most
expensive to merge: `--text-dim` sends nobody anywhere, `--indeterminate` points
at the observer. They are separated by a date, never by a status — see the
`neverObserved` / `blindNow` pair below.

The rule is implemented once, in `network.ts:242-252`:

```
off      → --danger          the device is down, and we know it
warn     → --warn            the device answered and reported a problem
ok       → --ok              the device answered and is healthy
unknown  → --indeterminate   no proxy able to watch this device is connected
(other)  → --text-dim        never observed at all
```

`unknown` and "never observed" are separated on purpose: `reachability()`
(`network.ts:353-374`) refuses to fabricate a duration for a device with no
`status_at`, and says `status.neverObserved` instead. Grey means *we have not
looked yet*; yellow means *we looked and the window is shut*.

One correction to the table, established while settling the §7 question below:
the last line is the INTENT, not what a device does. "Never observed" is not a
distinct status — it is `unknown` with no `status_at`, and `statusColor` is
handed the status alone, so a never-observed device is painted yellow like any
other `unknown`. The grey fall-through is a defensive default that
`Device.status`'s closed union (`api.ts:361`) keeps unreachable. Only
`reachability()` and `neverObserved()` (`network.ts:280-309`) — the two
functions that see the date — can tell the two apart.

The predicate now has a sibling, `blindNow()` (`network.ts:311-331`, decided
2026-08-08), and the pair is the canonical way to ask this question anywhere in
the frontend:

```
neverObserved(dev)   status "unknown", NO  status_at   →  --text-dim
blindNow(dev)        status "unknown", WITH status_at  →  --indeterminate
```

They partition `unknown` — never both true, never both false for an `unknown`
device — which is what lets a count use them as buckets and an ink use them as
a choice without the two drifting apart. `siteRates.test.ts` pins the partition.
Write these predicates rather than `status === "unknown" && …` at a call site;
that expression is where the two states get merged back together.

There is a fifth status ink, and it is `--accent`. Blue does not mean "good" or
"bad" — it means **meshcraft owes this device a write**. `deviceDot`
(`network.ts:761-821`) arbitrates all five in one place, and its precedence order
is itself the specification:

1. Unreachable (`off`) beats everything. Announcing "configuration in progress"
   on a silent switch would be false.
2. `applying` — the connector is writing right now → `--accent`, fast pulse.
3. Needs acknowledgement (skipped ports, stalled apply, apply error) → `--warn`,
   slow pulse. This is the "nothing will move on its own" state.
4. `sync === "pending"` → `--accent`, slow pulse. Auto-apply will clear it.
5. `sync === "drift"` → `--warn`, slow pulse. A human must arbitrate.
6. Otherwise, plain `statusColor(status)`.

**Never reimplement this on a page.** Render `<StatusDot dev={d} />`
(`netui.tsx:258-277`). The component's own docstring says why:

> Copying the color rule into each page would guarantee one screen eventually
> says green where another says orange — and the operator would then believe
> the more reassuring screen.

### Deciding which token to use

Ask, in this order:

1. **Is something broken, and do we know it?** → `--danger`.
2. **Is something wrong, and do we know it, and can a human do something about
   it?** → `--warn`.
3. **Does this thing exist, and do we have no vantage point on it right now?**
   → `--indeterminate`. Ask it of anything, not only of a device: a proxy that
   has never checked in, a device no proxy is allowed to watch, a reading that
   has stopped arriving. If the honest sentence is "meshcraft cannot see this",
   it is yellow. If it is "meshcraft has not looked at this", go to 5.
4. **Is meshcraft mid-flight — a write queued or landing?** → `--accent`.
5. **Have we simply never looked, or is there nothing to say?** → `--text-dim`.
6. **Is it confirmed good?** → `--ok`.

Steps 3 and 5 are the pair that gets merged. The test that separates them is
never the status string: it is whether an observation ever happened. Where the
subject is a device, `blindNow` and `neverObserved` ask it for you.

Two anti-patterns the codebase has already written down and should keep:

- **Do not paint a platform limitation orange.** `.info-banner`
  (`styles.css`) exists precisely so that "meshcraft cannot write this
  on this range" gets a neutral banner: *"it calls for no action, and painting
  it orange would teach people to ignore orange."* `UnsupportedBanner` and
  `ConnectorNotesBanner` are two of the consumers. Every state banner renders
  through ONE layout, `Banner` (`web/src/ui/Banner.tsx`), whose `tone` prop is
  this exact distinction: `act` calls for a decision, `info` states a fact and
  offers no gesture. The named variants in `components.tsx` keep the
  operational rationale; the markup lives once.
- **Do not paint "no single value" orange.** A multi-selection whose ports
  disagree is not an alert; it is a question with no one answer.

`--warn` is the most over-drawn token in the product. Counted as occurrences of
the string `var(--warn)` (not lines): 17 in the stylesheet, **65 in `.tsx`/`.ts`
inline styles** — 72 before the 2026-08-08 mixed-selection fixes in §6 took
six out of the two multi-port panels and a seventh out of the group lane. That is more
than `--ok` (29) and `--danger` (28) combined, and against **seven** uses of
`--indeterminate` in the entire frontend — three in `styles.css`, all in the one
`.badge.proto-blind` rule, and four in `.ts`/`.tsx` (`network.ts:250`,
`dashboardTiles.tsx:255`, `hostnameConformity.ts:201`, `Proxy.tsx:41`; test
files excluded). It was two before the 2026-08-08 ruling. If you are about to
write `color: "var(--warn)"` inline, the odds are you want `--text-dim`,
`--indeterminate` or a neutral `.info-banner`.

### Colours that are not status

Two palettes carry identity, not judgement, and must never be read as state:

- **VLAN palette**, `network.ts:18-21`. Sixteen hues; the first eight are
  CVD-validated against a dark ground. Colour is never the only cue — the cell
  also carries a number, a tooltip and a legend entry.
- **Rule tints**, `SwitchProfiles.tsx:86-89`. Eight hues, drawn from the VLAN
  palette, whose only job is to connect an editor row to the ports it claimed.
  The comment says so explicitly, which is the right way to introduce a
  meaning-free palette.

Two more literals are load-bearing and deliberately outside every palette
(`network.ts:99-110`): `INK_ON_COLOR` `#10131a`, the dark ink laid on a filled
port cell, and `UNMANAGED_UP` `#64748b` / `UNMANAGED_UP_BORDER` `#94a3b8`, the
slate of *an active link meshcraft does not manage*. Slate was chosen outside the
VLAN palette so it cannot be misread as a configuration.

**`--mode-routing` is the third, and it is a red** — ADR-0013 §4. The navigation
mode (switching ↔ routing) tints the brand mark, and reusing `--danger` would
make one colour mean both "you are in routing" and "something failed", so the
mode has its own token: `#af1d57` light / `#e73c80` dark, with
`--mode-routing-lit` as the mark's second gradient stop. Two things keep it from
being read as a fault:

- **distance**, measured the same way §2 separates warn from indeterminate. It
  is ΔE\*ab 31.4 (dark) / 32.5 (light) from `--danger` — in light mode that is
  *further apart* than `--warn` and `--danger` are from each other (32.7), the
  separation this guide already treats as sufficient. Contrast on the page
  ground is 5.04:1 dark / 5.91:1 light, well past the 3:1 a graphical object
  owes its surface. The warm side of red was unavailable: moving that way
  collides with `--warn`, so the mode went the other way, into crimson.
- **containment**, the same argument that keeps the traffic ramp out of the
  status spine: it only ever colours the brand mark and the mark's own selector,
  both in the top bar. Status red inks dots, pills, borders and text and never
  touches the mark. They never appear as the same kind of object.

And **the colour is never the only cue**: the selector carries both labels in
full, as radios in a radiogroup, so the mode is readable and operable without
seeing either red (`ui/ModeSwitch.tsx`).

### A colour and its text travel together

`styles.css:396-404` states the rule and why it was needed: a blanket hover once
turned any button blue while leaving `--text-dim` on it. A filled blue control
forces `#fff`. Ghost, danger and secondary buttons override the hover with their
own pair.

---

## 3. The hover contract

**Hover may reveal an affordance. Hover must never be required to read
content.** This is written into the stylesheet at `styles.css:177-180`, next to
a regression it caused: an over-broad selector hid the proxy *version* — content
— along with the pencil icon — an affordance. Only elements marked
`aria-hidden` are allowed to fade in on hover (`button.rename span[aria-hidden]`).

### Three tip mechanisms, three jobs

| | Trigger | Use it for |
| --- | --- | --- |
| `HelpTip` (`components.tsx:79`) | **Click**, never hover | Explanation. Read once, then clutter forever |
| `QuickTip` (`components.tsx:231`) | Hover, no delay; **`:focus-visible`** on anything inside it; click pins on coarse pointers | Observation, detail, reason. Read every time |
| native `title=` | browser, ~1 s | **Do not.** Reserved for `<iframe title>`, which is an accessibility name, not a tooltip |

`HelpTip` is click-only on purpose (`components.tsx:58-68`): the product is used
on a tablet, in a technical room, sometimes keyboard-only, and *"help that only
exists on `:hover` doesn't exist there"*. There is deliberately no hover in
addition to the click — the bubble would close the instant the pointer left,
right after a click pinned it.

The line between the two is a content rule, not a widget preference
(`components.tsx:45-56`): meshcraft's screens carry what it **observes** (a
drift, a skipped port, an address that was not written) and what it
**explains**. The first is read every time; the second is read once. `HelpTip`
takes only the second. *An observation must never go behind the `?` icon,
however long — hiding "addressing was not written to the device" costs a site
visit.*

`QuickTip` replaces the native `title` because a ~1 s browser delay reads as
broken on a dense port facade. It falls through transparently: an empty `text`
renders the children with no wrapper behaviour. Give it a layout class —
`quicktip-inline`, `quicktip-block`, or `port-square-tip` for facades — because
the wrapper *is* the interactive surface and `display: contents` would break
anchoring.

Both are positioned `fixed` and placed from the trigger's rectangle, flipping
above when the viewport bottom is tight. They must escape the side panel, which
is narrow and already in `overflow: auto`.

### One overlay at a time

`hoverExclusive.ts` is a module-global "whoever claims last wins" registry.
Anything that opens a floating layer registers a dismisser on mount and claims
on open. `HelpTip`, `QuickTip`, `LineChart`'s crosshair tooltip and Plan's
link-traffic panel all participate — the last one via its owner,
`Plan.tsx:200-203` and `Plan.tsx:767`, because `LinkTrafficTip` itself is
`pointer-events: none` and cannot claim for itself. Nested `QuickTip`s resolve
to the deepest one under the pointer (`components.tsx:329-333`).

If you add a floating layer, register it. Two bubbles on screen at once read as
a rendering bug.

### Hover as state, not as help

Three visual grammars, and they do not overlap:

- **Row hover → `--bg-hover` background.** `tr:hover td`, `.device-card:hover`,
  `.alert-row:hover`, `.prof-row:hover`, `.vlan-row:hover`, `.sitesel-opt.cursor`,
  `.langsel-opt:hover`, `.debug-line:hover`, `.pill-suggest button:hover`.
- **Box hover → `--accent` border.** `.sitesel-btn:hover`, `.plan-node:hover`,
  `button.chip:hover`, `.stp-list:hover` (at 35% mix, so hover is visibly
  weaker than focus at 100%).
- **Borderless input hover → `--border` appears, focus → `--accent`.**
  `.desc-input`, `.title-input`. A field that looks like text until you approach
  it; the frame is the invitation.

Keyboard equivalence is not optional. **`QuickTip` itself obeys this**: it
opens on `:focus-visible` anywhere inside its wrapper and closes when focus
leaves, which is what lets a label be a tip and nothing else — ADR-0019 took
the mode selector's "Switching" / "Routing" out of the view, so Tab has
to be able to put them back. Fields are the exception (`<input>`,
`<textarea>`, `<select>`): their description belongs to a label, and a bubble
over an open option list is in the way. Beyond the tip, `:focus-visible` gets
the same treatment as `:hover` on `button.rename`, `.helptip-btn`,
`.modesw-seg`, and — critically — on the
elements whose real input is clipped: `.switch input:focus-visible +
.switch-track` and `.check input:focus-visible + .box` both draw a 2px accent
outline on the *painted* proxy, because the input carrying focus is invisible
(`styles.css:2878-2885`).

### Hover to correlate

A fourth use, and the only one that changes the page rather than adding to it:
hovering a VLAN legend row dims every port that does not serve it
(`netui.tsx:228-229` → `portVisual`, `network.ts:185-186`, `opacity: 0.12`).
`SwitchProfiles.tsx:412` does the same from a rule row. Note how the dimming
composes: `Math.min` against the other opacity rules
(`network.ts:187`, `network.ts:206`), so an unreachable device or a shut port
stays at least as dim as it already was rather than being brightened back up by
a correlation pass.

---

## 4. Animation semantics

Nine keyframes exist. Two are transient (`toastin`, `spin`); the rest encode
state, and the encoding uses three independent axes — **colour**, **property**
and **period**. Read them together.

| Class | Keyframes | Colour | Property | Period | Says |
| --- | --- | --- | --- | --- | --- |
| `.port-pending` / `.dot.dot-pending` | `styles.css:2132`, `:2374` | accent | outer ring | 1.6 s | meshcraft has a write the device does not have yet |
| `.port-drift` / `.dot.dot-drift` | `styles.css:2136`, `:2378` | warn | outer ring | 1.6 s | the device was changed outside meshcraft; arbitrate |
| `.port-applying` / `.dot.dot-applying` | `styles.css:2149`, `:2389` | accent | outer ring, thicker, opaque | 0.85 s | being written **right now** |
| `.mgmt-warn-banner` | `styles.css:2461` | warn | border + ring | 1.6 s | outside the management plan |
| `.port-errdisabled` | `styles.css:2815` | **danger** | **background fill** | 1.0 s | **the switch shut this port itself — a fault** |
| `.icon-btn .spin` | `styles.css:2487` | — | rotation | 0.9 s | a request is in flight |

The TODO's assertion holds, and it holds three times over. `port-errdisabled-pulse`
is the only state animation that pulses the **fill** rather than a ring, the only
one in `--danger`, and the only one at a 1.0 s period. `styles.css:2812-2814`
states the intent: *"Kept distinct from the drift animations — this is a fault,
not a pending change."* `netui.tsx:79-81` and `netui.tsx:91` complete the
treatment: a 2px `--danger` frame replaces the normal 1px border, and
`network.ts:196-206` deliberately refuses to fade an err-disabled port the way
it fades an administratively shut one, *"Fading a fault would hide the one state
on this faceplate that always deserves attention."*

The period grammar, stated plainly:

- **1.6 s** — a standing condition. Something or someone is being waited on.
- **0.85 s** — work happening this second.
- **1.0 s, on the fill** — a fault.

Two more rules:

**Fade, do not blink.** `styles.css:2129-2131`: *"a 48-port facade pulsing on and
off is unreadable and quickly becomes something people disable."* Every ring
animation goes 0 → n px, never opacity 1 → 0 on the element itself, so the port
number stays readable through the cycle.

**Animate only the ports concerned.** `netui.tsx:52-54`: blinking a whole facade
for one differing port turns the signal into a search.

**Motion is never the only carrier.** `styles.css:2173-2174`: *"the color
remains, the movement stops. The information never depends on blinking alone."*
Under `prefers-reduced-motion: reduce`, the port classes freeze on their 50%
keyframe (`styles.css:2175-2184`); the dot classes stop dead and rely on the base
`background` the `deviceDot` colour already gave them (`styles.css:2396-2404`) —
an asymmetry, but a reasoned one, since a 9 px dot's ring is a decoration on a
colour that is already correct.

If you add a state animation: pick an existing period rather than a new one,
guard it under `prefers-reduced-motion`, and make sure the colour alone still
tells the story.

**One decorative animation, and only one place for it.** The login screen's
backdrop (`LoginBackdrop.tsx`, `loginRain.ts`) carries no state: falling
glyphs, and a halo that wires the pointer to the nearest heads. It is
allowed there because the login screen shows nothing that motion could be
mistaken for. Inside the shell, every animation is read as a state, and a
decorative one would be read as one too. It is `aria-hidden`, painted once
and frozen under `prefers-reduced-motion`, without the pointer layer on a
coarse pointer, and absent under `forced-colors`.

---

## 5. Boxes and spacing

### One radius

`--radius: 8px` everywhere a box has corners. The exceptions are shapes, not
sizes, and they are consistent:

- **Pill / `999px`** — anything whose content is a word or a count: `.badge`,
  `.chip`, `.sev`, `.stp-col-count`, `.progress`, `.toast-dot`.
- **Circle / `50%`** — `.dot` (9 px), `.switch-thumb`, `.helptip-btn`,
  `.plan-ghost-dot`.
- **Small hardware shapes / 2–4 px** — `.vlan-chip` 3 px, `.port-legend .sw`
  2 px, `.check .box` 4 px, `.meter` and `.barlist-track` 3 px, and the port
  square itself at 3 px — **0 px when it is an SFP cage** (`netui.tsx:94`),
  because a cage is square-cornered in the metal. That is shape carrying
  meaning, which the codebase prefers to spending another colour
  (`netui.tsx:36-40`: *"Shape and not color — every color on a port cell already
  carries a meaning."*)

### The surface ladder

`--bg` (ground) → `--bg-panel` (raised: card, modal, popover) → `--bg-hover`
(interactive fill and hover). A field placed on a panel goes back down to
`--bg`, so it reads as recessed: inputs, `pre.code`, `.plan-node`, `.tag-input`,
`.pillbox-field`, `.chart-tip` all do this. Elevation is also carried by shadow,
and the depth scales with how far the layer floats: `0 8px 24px` for a tooltip
or suggestion list, `0 12px 32px` for a site/language popover and the toast.

### The spacing scale

There is no `--space-*` token, but the sheet is consistent enough to state the
scale it uses. In descending order of what it separates:

| Value | What it separates |
| --- | --- |
| `20px` | Page gutter (`.content`), the two panes of `.page2col` |
| `16px` | Card padding, card bottom margin, `h1` bottom margin, toolbar bottom margin, `.dash-grid` gap, modal padding |
| `12px` | Grid gaps between small boxes (`.tile-row`, `.device-grid`), `.form-grid` row gap, `label` bottom margin |
| `10px` | `.row` gap — the default for controls sitting side by side |
| `8px` | Table cell padding, sidebar link padding, tight rows |
| `6px` / `4px` | Icon-to-label, chip-to-chip, label-to-field |
| `2px` | Sibling lines inside one logical block |

When you need a gap inline, prefer a class (`.row`, `.chips`, `.form-grid`) over
`style={{ gap: n }}`. Where inline gaps do appear, `8`, `6`, `4`, `12` and `10`
account for the overwhelming majority; anything else should have a reason.

### Layout containers

- `.content` is the one page frame: `padding: 20px`, flex column, `overflow:
  hidden`. `.content-scroll` inside it is the **one scroll surface** for the page
  body. `styles.css:291-293` explains why it exists: fragment pages used to make
  each card its own overflow, and scrolling felt per-box.
- `.page2col` — `1fr / 300px` (`.wide-panel` → `348px`), each column scrolling
  independently, collapsing to one column at 960 px. Used by Devices, Plan,
  Switch and Profiles.
- `.dash-grid` — `1.4fr / 1fr`, `align-items: stretch` so a shorter card does not
  leave its column floating (`styles.css:790-794`).
- `.form-grid` — `auto-fit, minmax(220px, 1fr)`. Use `.cols-2` when fields go in
  semantic pairs (subnet / gateway); with `auto-fit` alone, "IPv6 gateway" lands
  under "IPv4 subnet" (`styles.css:2106-2108`).
- `.card` carries `margin-bottom: 16px` by default; grid children reset it
  (`.device-card { margin-bottom: 0 }`, `dashboardGrid.tsx:263`).

### Controls

Every control that *performs an action* takes a leading icon, via
`ActionButton` / `ActionLink` (`ActionButton.tsx:4-13`) — not a bare `<button>`
with text. Icon-only controls keep `className="icon-btn"` (30×30) and carry their
meaning in `aria-label` plus a `QuickTip`. Destructive gestures arm before they
fire (`DeleteButton`, `components.tsx:508`), and where two dangerous gestures sit
side by side, only one arms at a time (`SkippedBanner`, `components.tsx:1078-1081`).

**A dense table's action column goes iconic** — `IconAction` /
`IconDeleteButton` (`ui/IconAction.tsx`), which is the `icon-btn` +
`aria-label` + `QuickTip` triplet written once. A /config row carrying five
spelled-out verbs needed 300 px of fixed width, and the columns that actually
vary got what was left. Wrap them in `.icon-actions` inside a `.col-actions`
cell; the two CSS rules are a pair — `.col-actions`'s `width: 1%` means "as
wide as the icons" only because `.icon-actions` is `nowrap`, since a wrapping
flex container's min-content width is one item.

The label leaves the screen, never the product, and some labels cannot leave
at all. Keep the words when the control reports a **state** rather than a verb
(a button reading "Active" / "Disabled" — an icon would delete the
information, not compress it), when the label changes with the **role**
("Edit" for an engineer, "View" for a viewer: there is no glyph for
"consult without changing", and a pencil promises an edit the role refuses),
when it is the page's primary gesture, or when it is **alone in its cell**
with no neighbouring glyph to give it context. And a toggle that goes iconic
must change GLYPH, not only tooltip: `ban` ↔ `check`, never one `apply` tick
meaning both directions.

Native form controls are replaced only where the browser cannot be made to match
the surrounding density — the checkbox (`.check`) and the toggle (`.switch`). In
both cases the native input **stays in the DOM and stays focusable**; it is
clipped, not `display: none`, because it carries the state, the keyboard
behaviour and the accessibility tree (`styles.css:2823-2830`).

---

## 6. Audit

Findings are things the code actually does that depart from the rules above.
A fixed finding keeps its original description and gains a **FIXED** marker with
the date and what moved — deleting the entry would delete the reasoning that
made the fix the right one, and these are the paragraphs the next person reads
before writing the same line again.

### Colour

**A second VLAN palette shadowed the validated one. FIXED 2026-08-08.**
`Vlans.tsx:9-12` declared its own `PALETTE`, unrelated to `network.ts:18-21`,
and its first four entries were the literal values of four semantic tokens:
`#4f7cff` (`--accent`), `#46a758` (`--ok`), `#e5484d` (`--danger`), `#f5a623`
(`--warn`). Two consequences. Every VLAN created through the UI got a hue from
a list that was never CVD-checked, so the guarantee documented at
`network.ts:11-17` did not hold for any VLAN an operator actually made. And the
third suggested colour made a port cell exactly the red of a fault.
`SwitchProfiles.tsx:86-89` shows the correct way to introduce a second palette:
draw from the first, and say in a comment that it carries no meaning.

The local list is deleted and the page imports `PALETTE` from `network.ts`, the
same way `Profils.tsx:17` already did for its swatch grid. **No second palette
was introduced, and that is the resolution rather than a shortcut:** the rule
`SwitchProfiles` follows applies to a palette that must carry no meaning, and a
VLAN's colour is exactly the meaning `network.ts:PALETTE` was validated to
carry. There was nothing here to draw *from* the first palette — the first
palette was the answer. The picking order carries over intact: the suggestion
is `PALETTE.find(c => !used.has(c))`, which walks the list in order and so
exhausts the eight CVD-validated hues before reaching the rest, which is the
behaviour the "up to 8 VLANs readable by construction" sentence promises.

The `<input type="color">` in the create/edit modal is deliberately left: an
operator naming a hue by hand is a choice, not a suggestion the product made,
and the palette's guarantee was never about what a human types into a colour
picker.

**The Ports page drew its own device dot. FIXED 2026-08-08.** `Ports.tsx` used
to render `<span className="dot" style={{ background: statusColor(d.status) }} />`
instead of `<StatusDot dev={d} />`, the only page that did. The consequences were
exactly the two the `StatusDot` docstring predicted: a device with a stalled
apply or an unarbitrated drift showed plain green here while Equipements
(`Equipements.tsx:153`), Plan (`Plan.tsx:1274`), SwitchDetail
(`switchdetail/DeviceHeader.tsx:38`) and the dashboard (`dashboardTiles.tsx:572`) all showed
a pulsing orange — and there was no tooltip, so the row could not be interrogated.

It now renders `<StatusDot dev={d} />` and so gains all four states `deviceDot`
arbitrates — applying, needs-acknowledgement, pending, drift — plus the QuickTip
carrying the reason. Unreachable devices keep a red dot with no pulse, because
`deviceDot` short-circuits on `off`, which matters here since the page already
dims the whole row in that case.

**"Mixed selection" was painted as an alert, five times. FIXED 2026-08-08.**
`switchdetail/MultiPortPanel.tsx:236`, `:241`, `:516`, `:521`, `:581` rendered
`switchDetailPage.modeMixed` in `--warn`. A multi-port selection whose members
disagree is not a problem with the device; it is the panel honestly reporting
that there is no single value. By the palette's own reasoning
(`styles.css:14-17`) and by the `.info-banner` rule (`styles.css:2249-2251`),
orange here was the exact thing that teaches people to stop reading orange. This
was the most-repeated token misuse in the frontend.

All five are now `--text-dim`, which is §2's step 5 — "there is nothing to say"
— read literally. Four are the mode / VLAN rows of the two multi-port panels
(the editable one and the read-only one, which had to agree with each other as
well); the fifth is the DHCP-snooping row, and it now gets its ink from the
shared decision in the finding below rather than from a branch of its own.

Two things were deliberately NOT changed. The **italic** stays: `<i>` is what
separates a state word from a value on these rows, and it is the cue that
survives when the ink goes neutral. And the `--warn` on the device-wide
"snooping is disabled on this switch" note above the field
(`switchdetail/PortPanel.tsx:623`, `switchdetail/MultiPortPanel.tsx:567`) stays orange — that one is not "no single
value", it is "we can see the device and the protection you are about to
configure does not exist", which is what `--warn` is for. Six `var(--warn)`
occurrences left `.tsx`/`.ts` in total (72 → 66); the guarantee is that none of
them was an observation about a device.

**A sixth instance, found 2026-08-08 while re-reading this entry. FIXED.** The
sweep above searched for `modeMixed` and therefore missed
`switchdetail/MultiPortPanel.tsx`'s group-lane note, which is named `laneMixed`
and was orange for the same wrong reason. Its sentence
(`switchDetailPage.groupMixedNoteMulti`) is *"this selection mixes ports that
follow different groups — or none. Applying a group below attaches all N
selected ports to it"*: a description of the selection that names no fault and
tells the operator the panel still works. `--text-dim`, like the other five.

Its two neighbours in the same lane are deliberately untouched and the contrast
is the useful part: `groupDanglingNoteMulti` — the followed group no longer
exists — stays `--warn`, because that IS a fault and a human has to pick a new
group; `groupNoneNoteMulti` was already uncoloured. So the lane now reads
orange only where something is actually wrong. **The lesson for the next sweep
is that this misuse is found by SEMANTICS, not by identifier**: any note whose
sentence begins "this selection mixes…" is the same finding under whatever local
name the panel gave it.

**The same field was coloured by two contradictory rules on one page. FIXED
2026-08-08.** DHCP snooping "observed" state:

- Single-port panel, `switchdetail/PortPanel.tsx:640-646` — `undefined` (unread) →
  `--text-dim`, `trusted` → `--warn`, `untrusted` → `--ok`.
- Multi-port panel, `switchdetail/MultiPortPanel.tsx:579-586` — `undefined` (mixed) →
  `--warn`, and every real value including `unread` → no colour at all.

An operator who extended a selection from one port to two saw the colour
vocabulary invert under them: orange stopped meaning "trusted" and started
meaning "mixed", and the unread state lost the dim ink that told them nothing
had been read.

The decision now lives in one function, `trustObsInk` (`portAdmin.ts`, beside
`observedTrust`, which is where the trust vocabulary already lived), and both
panels call it. `trustToken` names the three answers of one port so the
single-port panel stops re-deriving them inline, and `mixed` — which only a
selection can produce — is the fourth. `portAdmin.test.ts` pins all four inks.

```
unread     --text-dim   the device never reported this port
mixed      --text-dim   the ports disagree; no single value
trusted    no colour    a deliberate configuration
untrusted  --ok         protection in place, and observed
```

**DECISION: `trusted` is not an alert, and is not `--ok` either.** This is the
judgement the resolution turns on, so it is recorded rather than absorbed.

Orange was defensible on its face — a trusted port is precisely where DHCP
snooping stops inspecting, so it is the state that switches the protection off.
What defeats it is that the state is *supposed* to occur: every switch running
snooping has at least one trusted jack, the uplink toward the real DHCP server
or toward the next switch, and a correctly configured fleet would therefore
show orange on every uplink of every switch. That is the `.info-banner`
failure mode stated in §2 — a colour that fires on the normal case teaches
people to stop reading it — and it is the same colour this page needs for
err-disabled, drift and stalled applies. The panel also cannot tell the two
cases apart: it does not know whether a trusted port is an uplink or an access
port somebody trusted by mistake, and a token that cannot distinguish them must
not assert the alarming reading.

`--ok` was rejected for the opposite reason: green would say "confirmed good"
about a configuration that is merely intended, and would then be saying it
about *both* values of the field, since `untrusted` is good too. So `trusted`
takes no colour at all. It is a fact, the word carries it, and nothing on the
page is claiming anything about it.

`untrusted` keeps `--ok` — the one branch the single-port panel already had
right. It is §2's step 6 exactly: the protected state, observed on the device,
not merely intended.

**What the ink deliberately does not restate.** `enabled: false` device-wide
makes every trust flag inert, and it is the panels' orange note above the field
that says so. Folding that into the per-port ink was considered and dropped: it
would have made the field say "untrusted is fine" and "nothing is inspected"
three words apart, and it would have put a device-wide fact inside a function
handed one port.

The §7 entry on `--indeterminate` predicted this outcome for the multi-port
`unread` case and it holds: never read is grey, not yellow. Nothing about the
DHCP-snooping field is a lost vantage point — the connector either read the
port or did not.

**A dashed orange border for a permanent platform limit.** `.tile-unavailable`
(`styles.css:2519-2530`) draws `--warn` on both the border and the `!` glyph
for "this platform does not expose this metric" (`telemetry.tsx:465-468`). Ten
lines of `styles.css` earlier in the file, `.info-banner`
(`styles.css:2249-2251`) refuses to do that for the exactly parallel case, "this
platform cannot write this", and says why: *"it calls for no action, and painting
it orange would teach people to ignore orange."* Two answers to one question,
both written down, in one stylesheet.

**And the storm-control panel gives the third answer, in orange, twice.** Found
2026-08-08, NOT fixed — recorded because it is the same question as the two
above and should be settled once rather than a third time.
`switchdetail/StormControlSection.tsx:183` paints
`switchDetailPage.stormClassUnsupported` — "this range's limiter has no class
for this traffic type" — in `--warn`, under a comment that says *"PERMANENT.
Nothing is coming"*. That is `.info-banner` by the rule in §2, and the comment
argues the case for its own violation: a thing that will never change calls for
no action.

`:190` is the harder one and is why this is filed rather than swapped. The
neighbouring `stormNotVerified` — the hardware HAS the limiter and meshcraft has
not yet run the command form on this range — is not a platform limit: it is
temporary, it is about meshcraft rather than the device, and something (a
capture) will eventually clear it. It is nonetheless not a fault either, and
`--warn` claims it is. Both probably want the neutral banner, with the
difference carried by the sentence; deciding that is a product call, and doing
it properly means a component change, not an ink swap — the same shape as
`.tile-unavailable`, which is why it is parked in the same place.

**A near-miss hard-coded orange in the topology diagnostics. FIXED 2026-08-08,
in two rounds.**
`Plan.tsx:65-66`, as first found:

```ts
const linkDiagColor = (status: DeviceLink["status"]) =>
  status === "down" ? "#0a0a0a" : status === "warn" ? "#f5a524" : "#46a758";
```

`#f5a524` is not `--warn`, which is `#f5a623` — one digit off, so this is a
literal that has already drifted from the token it copies. `#46a758` is
`--ok` exactly. `#0a0a0a` for `down` is neither `--danger` nor any token; it is
near-black, readable only on the dark ground, and it is the same literal the
offline link stroke uses (`topologyLinks.ts:85`, `:112`) where a red drop-shadow
filter supplies the actual signal — but here at `Plan.tsx:949` it is painted on a
bare `.dot` with no filter, so a cut link's dot is simply invisible.

The first round substituted the two tokens that had one — `#f5a524` → `--warn`,
`#46a758` → `--ok` — and left `down` alone with a comment saying that picking a
colour for it was a design decision rather than a cleanup. **That reservation is
now spent**: the decision is `--danger`, by §2 step 1, "something is broken and
we know it". A declared link reported `down` is not a lost vantage point (§2
would make that yellow) and it is not a question with no answer; it is the one
row in the diagnostics list that names a fault.

What made it look like a free choice was reading the map's offline link as
"black". It is not black: the near-black stroke is a carrier for
`url(#topo-link-offline)`, and the filter's red — `#e5484d`, `--danger` written
out — is the whole of the signal. The dot was copied from the half that does not
carry it. Painting it `--danger` therefore does not invent a colour for this
state, it restores the one the map has always used, and the two views now agree
about one cable.

The function moved to `topologyLinks.ts` beside the stroke it was copied from,
which is the part that keeps: a private arrow on the page is exactly how the
literal got there, and the module now shows the two side by side with the reason
the dot cannot take the stroke's value. `topologyLinks.test.ts` pins all three
inks as `var(…)` tokens — the pin is "no hex on this dot", not "this hex", since
the recurring defect is the literal and not the shade.

**The offline glow's `flood-color` was a literal. FIXED 2026-08-08 — and the
reason this entry gave for why it was hard was WRONG.** `Plan.tsx`'s
`<feDropShadow>` carried `floodColor="#e5484d"`, exactly `--danger` written out.

This document recorded it as the one colour substitution could not fix, on the
grounds that `flood-color` sits on an SVG PRESENTATION ATTRIBUTE, "where `var()`
is not permitted and would silently drop the colour" — so the honest fix was
said to be moving the property into a CSS rule. **That is not true in any engine
this product runs on, and it is worth stating how it was settled, because the
claim is a widespread piece of folklore and it will be proposed again.**

Measured, rather than reasoned about: a page with two identical filters — one
taking `flood-color="var(--danger)"` on the element, one taking it from a class
— rendered through headless Chromium 124 and headless Firefox 152, screenshot,
pixels read. Both engines, both routes: `rgb(229, 73, 77)`. The attribute
resolves the token. (`getComputedStyle` agrees, but the pixels are the claim.)

The evidence was also already in the tree, which is the part to learn from: this
frontend paints `stroke="var(--border)"` in `viz.tsx:150`,
`fill="var(--bg-panel)"` in `TopologyLinkLabels.tsx:40`, and
`stroke="var(--accent)"` in `Plan.tsx` itself. If presentation attributes really
dropped `var()`, every chart gridline and every link label backing plate in the
product would be unpainted. A rule that the codebase contradicts on sight is the
kind that should be tested before it is written down.

So the fix is the substitution the entry said to avoid:
`floodColor="var(--danger)"`, no new class, the filter stays self-contained.
The finding it was filed under — a literal duplicating a token — was real; only
the reason it was thought unfixable was not.

**Hard-coded token values in `topologyLinks.ts`.** The file mixes both
conventions in one function: `topologyLinkStroke` uses `var(--accent)` at line 94
and `loadColor()` at line 101, where `loadColor` (`:49-52`) interpolates between
the literals `#46a758` and `#e5484d` — `--ok` and `--danger` written out.
`ghostPeerLinkStroke` (`:120`) hard-codes `#46a758` again. The interpolation
genuinely needs numeric RGB and cannot take `var()`, which explains `loadColor`;
line 120 has no such excuse.

**`--text-dim` written out as a hex.** `MgmtAddressing.tsx:149` sets
`color: "#8b96a8"` on the synthetic "observed VLAN" entry — the literal value of
`--text-dim`.

**The facade legend hard-codes two private constants.** `switchdetail/Faceplate.tsx:104`
draws the "active, unmanaged" swatch with `background: "#64748b", border: "1px
solid #94a3b8"`. Those are `UNMANAGED_UP` and `UNMANAGED_UP_BORDER`
(`network.ts:109-110`), which are module-private and not exported. Every other
swatch in the same legend block (`:101-106`) uses `var()`. Change the constants
and the legend silently starts lying about the facade beside it.

**Black text on an orange fill, alone of its kind.** `Proxy.tsx:717` returns
`fg: "#000"` on a `--warn` background where the four sibling branches
(`:715`, `:719`, `:721`, `:723`) return `#fff` or `var(--text)`. Black on
`#f5a623` is the *better* contrast choice, so the pixel is right and the literal
is the problem: the rule at `styles.css:396-404` ("a colour and its text travel
together") has no token for the ink that belongs on orange, so this one call site
invented it.

**A proxy that has never checked in was an alert. FIXED 2026-08-08.**
`Proxy.tsx:33` coloured "never seen" `--warn`. Everywhere else in the product a
thing never observed is `--text-dim` (`network.ts:251`) and a thing we cannot
currently observe is `--indeterminate`. A proxy is *the observer*; its absence
is the archetype of "we cannot see", which is what `--indeterminate` was minted
for. It is now `--indeterminate` (`Proxy.tsx:41`), with the reasoning in a
comment beside it. The `offlineSince` / `silentSince` branch is deliberately
untouched and stays `--warn` — that is the separate §7 question below, and
answering it as a side effect of this one is how a token comes to mean two
things again.

**A drift marker in dim ink.** `Equipements.tsx:182` renders the management-
addressing drift `!` in `--text-dim`. The identical `!` glyph in
`.tile-bang` (`styles.css:2524-2526`) is `--warn`, and management drift is a real
divergence — the same class of thing `.drift-banner` and `.port-drift` both paint
orange. Two identical glyphs, two colours, opposite directions from the rule.

**An unrecognised server state renders as an alert.** `SiteCredentials.tsx:998`
falls through to `--warn` for any generation state that is neither `active` nor
`retiring`, and prints the raw string. A state the frontend does not know is not
a problem with the credential; it is a frontend that has not been extended.
`hostnameVerdict` (`hostnameConformity.ts:158-160`) handles the same situation
correctly, returning `null` and staying silent, and explains why in its
docstring. Separately, `retiring` — a normal lifecycle stage — is also `--warn`.

### Hover

**Seven native `title` tooltips survive**, against the rule in
`components.tsx:217-222` that `QuickTip` replaces them because the browser's
~1 s delay reads as broken:

- `SpanningTreeDevice.tsx:134` — the full root bridge ID behind a truncated one
- `Dashboard.tsx:222`, `:234`, `:242` — the public-dashboard dot, and the two
  reorder arrows
- `dashboardGrid.tsx:275`, `:297` — remove-tile and resize handle
- `DeviceConsole.tsx:348` — copy-username

(`DeviceConsole.tsx:381` is an `<iframe title>` and is correct — that is an
accessibility name, not a tooltip.)

The `SpanningTreeDevice.tsx:134` one is the most consequential: it is the only
way to read the untruncated bridge ID, so information the page deliberately
shortened is behind a delay the rest of the product removed.

### Boxes and spacing

**Three classes are declared twice, hundreds of lines apart**, so the second
declaration silently rewrites the first:

**RESOLVED 2026-08-08** — all three were merged into a single declaration each,
computing exactly what the pair computed before. Kept here because the shapes
are worth recognising, and because one of them corrected this document.

- `.topbar` — was `styles.css:70` (`padding: 10px 16px`, `gap: 12px`) and
  `styles.css:727` (`padding: 0 16px`, `height: 52px`, `gap: 10px`). The
  `max-width: 820px` override still follows the merged rule, so it still wins.
- `.icon-btn` — was `styles.css:1081` (`padding: 7px 9px`) and `styles.css:2477`
  (`padding: 0`, `width/height: 30px`). The first survived in nothing and was
  deleted outright.
- `.chip` — was `styles.css:1166` (`font-size: 11px`, `padding: 2px 8px`,
  `background: var(--bg-hover)`) and `styles.css:1925` (`font-size: 10px`,
  `padding: 1px 6px`, no background). The effective chip was a merge: 10 px
  text, 1×6 padding, and a hover-grey background leaking from the shadowed rule.

  **This document originally claimed `components.tsx:1213` was the evidence that
  the duplicate had bitten someone. That inference was wrong, and checking it
  before deleting anything is what caught it.** `PillPicker` sets
  `{ fontSize: 11, padding: "2px 8px" }` — the values of the SHADOWED rule, not
  of the effective one. It is therefore not a workaround the merge retires; it
  is a genuine local override that happens to restore the older sizing for that
  one call site. Removing it would shrink the pill from 11 px/2×8 to 10 px/1×6.
  It is left in place, and turning it into a modifier class is a deliberate
  visual decision rather than a cleanup.

**Two wrapper classes do the same job.** `.chips` (`styles.css:1161`) and
`.chip-row` (`styles.css:1919`) are both `display: flex; flex-wrap: wrap; gap:
4px`, differing only in `align-items: center`. `.chip-row` is used across the
add-device flow and `PillPicker`; `.chips` is used exactly once, at
`Proxy.tsx:327`.

**Dead CSS.** `.mgmt-warn` (`styles.css:2437-2453`) and `.mgmt-warn-cta`
(`:2454-2456`) — the pill form of the "outside management plan" warning — have no
consumer. The only live class from that block is `.mgmt-warn-banner`, applied at
`switchdetail/DeviceBanners.tsx:59`. The pill's rules and its 12-line rationale describe a
device-card element that no longer exists.

**`--radius` written out as `8px`** three times in the console block:
`.console-note` (`styles.css:2750`), `.console-term` (`:2764`),
`.console-frame` (`:2796`). Same value, so nothing renders wrong today.

**A comment that has become false.** `styles.css:2308-2310` justifies a
hard-coded `rgba(79, 124, 255, 0.14)` on the grounds that `color-mix` *"is not
used anywhere else in this stylesheet."* It is used eighteen other times
(`:547`, `:555`, `:637`, `:1488`, `:1549`, `:1591`, `:1619`, `:1623`, `:1632`,
`:1720`, `:1739`, `:1744`, `:1860`, `:2612`, `:2633`, `:2637`, `:2644`, `:2663`)
— including three lines that do exactly this mix at exactly this ratio.

**Two off-scale inline gaps.** `Plan.tsx:1273` and `:1371` set `gap: 7` on a
`.row` whose class default is 10.

**Four port facades, four wrapper gaps, no shared component.** `SwitchDetail`
uses `gap: 4` at `size={38}` (`switchdetail/Faceplate.tsx:51`, `:53`), `Ports`
`size={18}` (`:263`),
`Equipements` `gap: 3` at `size={13}` (`:188`, `:196`), `SwitchProfiles` `gap: 3`
at `size={22}` (`:812`, `:831`), `Plan` `gap: 2` at `size={9}` (`:1340`, `:1342`).
Scaling the gap with the square is reasonable; each page re-deriving the flex
wrapper from scratch is what makes them diverge.

### Pages that are clean

- `dashboardGrid.tsx` — tokens throughout, `--radius` via `var()`, no colour
  literals. Its two native `title` attributes are the only mark against it.
- `viz.tsx` — the shared chart primitives use `var(--border)`, `var(--text-dim)`,
  `var(--bg-panel)` for chrome and take every series colour from the caller. The
  one-scale / no-dual-axis rule in its header comment holds in `LineChart`.
- `components.tsx` — the shared banners, tips and fields carry no colour literals
  at all, and every hover behaviour goes through `hoverExclusive`.
- `SwitchProfiles.tsx` — apart from the shared facade-gap point, this is the page
  that gets a second palette right and documents why.
- `netui.tsx` — the shared `PortSquare` / `StatusDot` / `ProtocolPills` take all
  their colour from `network.ts` and `var()`, with shape (SFP corners, PoE dot)
  used where colour is already spent.
- `telemetry.tsx` — clean tokens; its only issue is the `--warn` choice inherited
  from `.tile-unavailable`, which is a stylesheet decision, not a page one.

---

## 7. Open questions

Places where two parts of the code disagree and the source does not say which is
intended. These are for someone with product context to settle, not for a
mechanical fix.

**Should `--indeterminate` be used more widely, or is it deliberately scoped to
the device dot? — ANSWERED 2026-08-08: it is a GENERAL rule.**

The question, as it stood: the token appeared twice in the whole frontend
(`network.ts:250` and `hostnameConformity.ts:201`). Four other sites described
what its comment defines and every one had chosen differently —
`vlanPathPreview === "unknown"` (`MgmtAddressing.tsx`, dim), the port link
column on an unreachable device (`Ports.tsx`, `.note`), a proxy never seen
(`Proxy.tsx`, `--warn`), `trustObsToken === "unread"` (`MultiPortPanel.tsx`,
uncoloured). The plausible reading of that spread was that yellow is reserved
for the *device-level* claim and field-level absences stay dim so a table does
not turn yellow all over. **That reading is rejected.** `hostnameConformity.ts`
was already using the token field-level, correctly and with a comment saying
why; scoping the rule to the dot would have made that call site the exception
rather than the precedent.

**The ruling: yellow is "we cannot see", wherever it occurs.** The general
statement, its boundary table and the amended ladder are in §2. The boundary is
the load-bearing half — the token is only worth having while it excludes "we
have never looked", "there is nothing to say", "this platform cannot do it" and
"something is wrong".

Applying it to the four sites is what showed the boundary is where the work is:
**only one of them turned out to be this state.**

- **`Proxy.tsx:33-41` — a proxy never seen. CHANGED to `--indeterminate`.** A proxy
  is the observer; one that has never checked in is the archetype of a missing
  vantage point. `--warn` said "something is wrong here" on a row whose most
  likely explanation is an install nobody finished. Note this is *not* the
  device rule's grey: a device never observed is grey because nothing was
  claimed about it, whereas an enrolled proxy that never connected is a
  vantage point we were promised and do not have.
- **`MgmtAddressing.tsx:227-243` — `vlanPathPreview === "unknown"`. NOT CHANGED,
  stays dim.** Reading `vlanPathPreview` (`network.ts:828-845`) settles it: it
  returns `"unknown"` when no port carries a link state at all, and the sentence
  it prints is *"re-read the device so meshcraft can see link state"*. The
  remedy is to look. That is "we have never looked" — grey, yesterday's ruling —
  not a lost vantage point. The other branch into `"unknown"`, `vlanId <= 0`, is
  "no VLAN chosen yet", which is "nothing to say". Both are dim; the site was
  already right.
- **`Ports.tsx:300` — the port link column on an unreachable device. NOT
  CHANGED, stays `.note`.** `off` is `d.status === "off"`: the device is down
  and **we know it**, which means the observer is working. The token's own
  definition (`styles.css:14-17`) ends *"and the device may be perfectly
  fine"* — that clause is false here. The row already states the fault in
  `--danger` on the `StatusDot`; adding yellow beside it would say meshcraft
  lost its vantage point when what it lost is the device, and that is precisely
  the "wrong rack" the comment warns about. The neighbouring cells in the same
  row print `—` for the same reason, so the dim is also internally consistent.
- **`switchdetail/MultiPortPanel.tsx:584` — `trustObsToken === "unread"`. NOT
  CHANGED, stays uncoloured.** `trustTokenOf` (`:150-158`) has a comment stating the meaning
  outright: `observedTrust()` answers `undefined` for *"the device never
  reported this port"*, and *"the first is a reason to re-read the device"*.
  Never read → `--text-dim`. The single-port panel already paints it dim
  (`:3080-3086`); the multi-port panel leaving it uncoloured is the §6 finding
  "the same field is coloured by two contradictory rules on one page", and that
  is a consistency fix, not this ruling. Painting it yellow would have made the
  token mean "unread" in one panel and "unseeable" everywhere else.

  *The consistency fix landed the same day and this held: both panels now take
  the field's ink from `trustObsInk`, and `unread` is `--text-dim` in each. See
  the §6 entry, which also settles what `trusted` is worth.*

Beyond the four, the sweep found one more site and it is the worst instance,
because it was merging the two unseeable states inside one widget:

- **`netui.tsx` `ProtocolPills`. CHANGED.** The ping and collect pills had three
  kinds — up/down/unknown — and the muted "unknown" absorbed both "not observed
  yet" and "observed, and nothing is arriving now", with a docstring that
  claimed it only meant the first. Both dates were already in the props. There
  is now a fourth kind, `blind` (`.badge.proto-blind`, `styles.css:566-573`),
  chosen by the same date test the device dot uses, and a matching
  `status.protoBlind` tooltip string so the sentence cannot contradict the
  colour. `watch_excluded` — "NO proxy monitors this device, and here is why"
  (`api.ts:456`, `internal/api/watchexcl.go:106`) — is yellow too: it is a
  vantage point the controller has determined does not exist, which is the
  definition, and the reason still travels in the tooltip.

  One boundary worth naming, because getting it wrong here is easy: the split
  is made on `unknown` **alone**, not on "anything that is not `ok` or `off`".
  `Device.status` also carries `warn`, which means the device ANSWERED and
  reported a problem. The pill has no colour for that and still falls through
  to muted, but it must never fall through to yellow — "nobody can see it" is
  the exact opposite of what is true about a device that just replied. This is
  the general shape of the mistake: yellow follows the *date*, but only once
  the state is already known to be an absence of observation.

Sites examined and deliberately left alone, because they are the boundary and
not the rule: `.tile-unavailable` / `monitoringUnavailable` (a metric the
platform does not publish — permanent, `.info-banner` territory, and already a
§6 finding for being orange), `mgmtAddressing.unreadable` (a product line whose
addressing mode cannot be read — same class), `mgmtAddressing.notYetRead` and
`switchDetailPage.stormObservedUnread` and `snoopNeverReadNote` (never read →
dim, already correct), `switchDetailPage.stormCapsUnknown` ("the connector could
not be resolved, so nothing can be said either way" — nothing to say),
`discoveryCredsPage.unreadable` ("key changed" — a real fault, `--danger`, and
correct).

**Is an offline proxy a fault or an alert? — ANSWERED 2026-08-08: a FAULT.**

The question, as it stood: `Proxy.tsx` used `--warn` for "offline since N
hours", while a device silent for hours is `--danger` (`network.ts` in
`statusColor`), and a proxy silent for hours blinds a whole site. It was not
clear whether the softer colour was a considered decision about who reads the
proxy page, or an accident of that page having been written separately.

**The ruling: `--danger`.** The argument that settled it is proportion. A proxy
does not merely fail on its own account — it takes the observability of every
device behind it with it, so it contains the device case rather than sitting
beside it. Something strictly worse than a `--danger` device cannot be painted
more gently than the thing it contains.

It is deliberately NOT `--indeterminate`, and that line is why the two branches
of the same function now read differently. A proxy that never checked in may
simply not be installed yet: nothing is broken and we have no vantage point,
which is exactly what yellow says. A proxy that answered and then stopped HAS
broken — and we can see that it is gone, which is knowledge rather than
blindness. "Never seen" is a vantage point never established; "offline since"
is one that existed and stopped, and only the second carries a transition
somebody could have watched happen.

**Should an indeterminate device count as online? — ANSWERED 2026-08-08, in two
steps on the same day.** This entry records step 1, which took out the devices
never observed; step 2, immediately below, took out the ones observed and now
unseeable, and states the principle that covers both.

The question, as it stood: `siteRates` counted every device whose status is not
`off` as online, so a device with no observer was tallied green in
`DevicesOnlineTile`. `buildAlerts` (`dashboardTiles.tsx:152-165`) took the
opposite view and raised nothing for `unknown`, which is consistent with "not an
alert". Both were defensible in isolation; together they meant the dashboard's
headline counter was optimistic about exactly the devices the palette went out
of its way to mark as unverified. No comment addressed it.

**The ruling: a device monitoring has NEVER observed does not count as
online.** At the time, the yellow `unknown` case — observed before, no proxy
able to watch it right now — still counted as online and was left open. It was
settled the same day; see the next entry.

What that required first, and it is the finding worth keeping: **the two cases
are not two statuses.** Both are `status === "unknown"`; `Device.status` is a
closed union of `ok | warn | off | unknown` (`api.ts:361`) and the column
defaults to `unknown` (`0002_devices.sql:14`), so the `(other) → --text-dim`
fall-through in `statusColor` (`network.ts:251`) is a defensive default no real
device reaches. **What separates them is `status_at`**: absent = monitoring has
never spoken (`api.ts:363-373`, `model.go:90-98`). `reachability`
(`network.ts:353-374`) is the only function that made the distinction, because
it is the only one handed the date; `statusColor` is handed the status alone and
*cannot* make it. So a never-observed device does not in fact render grey today
— it renders `--indeterminate` yellow like any other `unknown`, and the table in
§2 above describes the intent rather than what the device dot does. Changing
that is a separate decision and was not taken here.

The predicate is now named once, `neverObserved` (`network.ts:280-309`) —
`status === "unknown" && !status_at` — and `reachability` prints its
`status.neverObserved` sentence on that same call, so the count and the label
cannot drift apart. An observed status with no date (an `ok` row predating the
column) is deliberately NOT this case: the observation happened, only its
duration was lost.

`siteRates` returns `unobserved` alongside `online`, and the two never overlap.
**The denominator did not shrink.** `DevicesOnlineTile` still divides by
`data.devices.length`, so the tile reads "3 / 5" and the missing device is the
visible gap — dropping it from the total too would have read "3 / 3" and hidden
it, which is the opposite of what the grey ink is for. Two consequences followed
in the same tile: the sub-line's `offlineCount` now counts only devices known to
be down (calling a never-pinged device "offline" would assert an outage nobody
observed), and when the whole shortfall is unobserved devices the tile's dot is
`--text-dim` rather than `--warn` or `--danger` — otherwise the fix would have
turned "we have never looked" into an alert, which is the confusion §2 exists to
prevent.

Pinned by `siteRates.test.ts`, including the half that was not decided: a
device with a `status_at` and status `unknown` must still count as online, so
that settling it later is a deliberate act. That pin is what step 2 flipped.

**Should a device we cannot currently SEE count as online? — ANSWERED
2026-08-08: no.**

The question, as it stood: a device with status `unknown` and a `status_at` was
observed at some point, and no proxy able to watch it is connected now
(`--indeterminate`, yellow). It counted as online. The argument for keeping it
there was real and is worth preserving rather than deleting: the device is
probably fine and the proxy is what broke, so dropping it reports an outage that
is not one. The argument against: the counter then claims something nobody can
currently verify.

**The ruling: it does not count as online.** What decided it is that the tile
does not say "outage" — it says "N online", and a number nobody can vouch for
is the one thing a headline counter must not be. The pro-keeping argument was
answered rather than overruled: the device is *not* reported as an outage
either. It leaves the numerator without joining `offlineCount`.

The two rulings collapse into one sentence, now the header comment on
`siteRates` (`network.ts:598-632`): **a device meshcraft cannot currently vouch
for must not inflate the headline number.** Four buckets, never overlapping:

```
online       we can see it, and it answers          --ok / --warn
blind        seen before, nobody can see it now     --indeterminate
unobserved   monitoring has never spoken about it   --text-dim
off          we watched it go down                  --danger

online + blind + unobserved + off = the fleet
```

**Two buckets and not one, and this was the substantive decision.** `blind` and
`unobserved` leave the numerator for the same reason, so a single "unverifiable"
count would have served the arithmetic and been less code. They are kept apart
because arithmetic is not the only consumer: the palette gives them different
inks, and the tile's dot has to pick one. A merged figure would force
`DevicesOnlineTile` to re-derive the split from `data.devices`, reimplementing
`neverObserved` at the call site — the exact drift the predicate was extracted
to prevent. They also differ in what an operator does next: `blind` points at a
proxy, `unobserved` points at a device that was adopted and never watched.

`off` is deliberately not one of the two. It is an observation, not an absence
of one, and the sub-line that says "N offline" counts it alone.

**The denominator still does not shrink.** It reads "3 / 5" and the gap is
visible. `offlineCount` is now `total − online − unobserved − blind`.

The tile's dot gained a branch, and the ORDER of the branches is the
specification, mirroring `deviceDot`'s "off beats everything":

```
nothing missing              --ok
any known-offline device     --danger if nothing is up, else --warn
otherwise, any blind         --indeterminate
otherwise (all unobserved)   --text-dim
```

A mix of blind and never-observed is **yellow, not grey**: grey asserts nothing
was ever looked at, which is false of the blind half, and yellow is the more
actionable of the two. A fleet nobody can see is never red — no device was
observed to fail. All four rows are pinned in `siteRates.test.ts`, which
reproduces the tile's arithmetic because the tile itself is a React component
the plain-node suite cannot import.

**Should the reachability sentence be coloured? — ANSWERED 2026-08-09: no, and
the reason is that the colour was never the sentence's to give.**

The question, as it stood: `SwitchDetail.tsx` printed `reachability(dev).label`
in `statusColor(dev.status)` while `dashboardTiles.tsx` printed the same string
with weight and no colour, relying on the adjacent `StatusDot`. Framed that way
it looks like a taste call between two defensible screens.

It is not. **`statusColor` is handed the status and nothing else** — the same
finding the "does an indeterminate device count as online" entry above turns on.
`Device.status` is a four-value union and the distinction that matters here,
never-observed versus blind, lives in `status_at`, not in the status. So
`statusColor` renders both as one colour and *cannot* do otherwise; the four
states `deviceDot` arbitrates cannot survive the trip.

The concrete contradiction that settles it is on one screen: `DeviceHeader`
renders `<StatusDot dev={dev} />`, which IS handed the device and therefore the
date, so a switch with a stalled apply showed a pulsing orange dot at the top of
the page and a flat green sentence a few rows below. Two inks, one device, and
the wrong one is the one with more text next to it.

**The ruling: weight, no colour.** The sentence says what happened; the dot
beside it says how bad it is, from the only function that can tell. This is what
`dashboardTiles.tsx` already did, so the fix is one screen conforming to the
other rather than a new rule. The general form is worth keeping: *never paint a
status word from a function that sees less than the widget beside it.*

**Should port squares below 24 px show a number? — ANSWERED 2026-08-09: yes,
and the call site moves, not the gate.**

`PortSquare` gates its number at `size >= 24` (`netui.tsx`). Exactly one caller
in the frontend sat under that gate — `SwitchProfiles`, at 22 — so one facade
in the product drew unnumbered squares and every other facade numbered them.
Two pixels is not a design decision anybody would defend out loud; it is a
number nobody revisited after the sizes settled.

**The ruling: `SwitchProfiles` renders at 24.** Both directions produce the
numbers, and the choice between them is about how many things move. Lowering the
gate re-flows every `PortSquare` in the product to fix one page — five call
sites re-measured to prove one — and leaves the frontend with a threshold whose
value no longer means anything. Raising the one call site keeps a single
legibility threshold for the whole frontend and touches one line. When a
threshold and a single caller disagree, suspect the caller.

**Is there meant to be a light theme? — ANSWERED 2026-08-09: no, and the
stylesheet now says so by carrying nothing.**

`styles.css` held the frontend's only `@media (prefers-color-scheme: light)`
block: five lines adjusting the box-shadow of one tooltip. It set no token. A
light-preferring browser therefore got the dark page with a slightly different
bubble — the stylesheet ADVERTISED theme-awareness that did not exist, which is
the worst of the three available states, worse than either committing or not.

**The ruling: deleted.** A real light theme is the thirteen tokens at the top of
this file with light values, plus every inline colour literal in the pages, plus
a re-check of the CVD separations in `PALETTE` against a light ground — the
palette's guarantee is written for a dark background and does not transfer. That
is a project, and nobody has asked for it. Deleting the block asserts the honest
state, and a comment where it stood says what re-introducing one would cost, so
the next person does not restore the shadow and think they have started.

## 8. Keyboard

The single largest historical gap in this UI, and the reason this chapter
exists: the shared `Modal` shipped 27 call sites without an Escape handler
while two hand-rolled overlays each carried their own, one of them with a
comment claiming every overlay behaved that way. Enter-to-submit existed in
exactly two `<form>` elements; everywhere else it was faked four incompatible
ways or absent. Nothing here is aspirational — every rule names the primitive
that enforces it.

### 8.1 The dismissal stack

Escape closes the TOPMOST dismissable layer, and only it. A tooltip above a
modal above a page: the first Escape closes the tooltip, the second the modal.
Outside-click follows the same stack.

- The ONLY way to register a dismissable layer is `useDismissable`
  (`web/src/ui/dismiss.ts`). It owns the `document` listener, the stack
  order, the outside-click `mousedown` handling, and the busy guard.
- Raw `addEventListener("keydown")` in a component is a defect. The seven
  historical sites are the uilint baseline; the count only goes down.
- A layer that is `busy` (a write in flight) refuses to dismiss: closing a
  dialog mid-apply orphans the outcome the operator must read.

### 8.2 Enter

Enter means "the primary action of the innermost form", and it is NATIVE:
wrap fields in `<Form>` (`web/src/ui/Form.tsx`), which renders a real `form`
element and a real `type="submit"` primary button. No `onKeyDown` Enter
handlers on individual inputs.

Precedence, innermost wins:
- an OPEN picker (combobox, tag input) consumes Enter to commit its
  highlighted item — closed pickers let it through;
- a `textarea` keeps Enter for newlines; Ctrl+Enter submits;
- otherwise Enter submits the innermost `<Form>`.

Every dropdown, combobox and autocomplete is `usePicker`
(`web/src/ui/picker.ts`) — it owns open state, the cursor, the ↑/↓/Enter
vocabulary and the dismissal-stack registration; components own only their
markup. Free-text fields (`freeText: true`: VlanCombo, TagInput, PillPicker,
PillMultiSelect) add three rules: keys the caret needs (Space, Home/End) are
never stolen from the input; the cursor may rest at -1 — "the typed text" —
where Enter commits the DRAFT instead of an option; and on dismissal, Escape
abandons the draft while outside-click and Tab-away commit it (leaving the
field means "moving on", the contract the blur timers implemented before the
stack). Focus-out never refocuses the trigger: Tab asked to leave.

A modal whose body is a `<Form>` gets Enter=primary for free; the primary
button in the footer IS the submit button. "You must click Save" is a defect.

### 8.3 Modals

`Modal` (components.tsx) is the ONLY modal shell. It guarantees, for every
call site, past and future: `role="dialog"` + `aria-modal`, focus moved in on
open and RESTORED on close, a focus trap (Tab cycles inside), Escape via the
dismissal stack, drag-safe backdrop dismiss (`mousedown` on the backdrop
itself, so a text selection ending outside the panel does not close it), and
the `busy` refusal. Hand-rolled `.modal-backdrop` markup outside Modal is a
defect (the two historical ones are being absorbed).

### 8.4 Global shortcuts

A screen-level key binding (the dashboard grid's arrows, Plan's shortcuts)
must ignore events when focus sits in an editable element. That guard lives
ONCE in `ui/dismiss.ts` (`focusInEditable`) — reimplementing it by tagName
list is the defect that made one screen's shortcuts fire inside its own
search box.

## 9. Responsive

### 9.1 Breakpoints

Two named breakpoints, chosen where the fleet screens actually break:

    narrow   720px   (sidebar collapses, toolbars wrap, tables → cards)
    mid      960px   (two-column layouts fold to one)

CSS media queries cannot read custom properties, so the tokens are a
CONVENTION, marked in the stylesheet: every media query carries the comment
`/* bp:narrow */` or `/* bp:mid */` beside its literal. A width that appears
in neither list is a defect unless its comment argues the exception (the
dashboard grid's 1000px is one: tile math, not layout). JS reads the same
values through `useBreakpoint` (`web/src/ui/breakpoint.ts`) — never
`window.innerWidth` ad hoc.

The historical widths 620/820 fold into `narrow`; 1000 stays argued.

### 9.2 Overflow

Wide content — tables, faceplates, code blocks — scrolls INSIDE its own
container: wrap it in `.scroll-x` (tokens.css). The page body never scrolls
horizontally; `styles.css` enforces it on `.main`. An ad-hoc
`style={{overflowX:"auto"}}` is the inline-style defect wearing its overflow
hat.

### 9.3 Spacing tokens

The §5 scale, finally as custom properties (tokens.css):

    --sp-0: 2px   --sp-1: 4px   --sp-2: 6px   --sp-3: 8px
    --sp-4: 10px  --sp-5: 12px  --sp-6: 16px  --sp-7: 20px

New CSS uses the tokens; inline `style={{gap: N}}` is counted by uilint and
only goes down. An off-scale value (the audited `gap: 7`) has no token on
purpose — reach for the nearest step.

### 9.4 The mobile shell

**Under 720px the shell inverts.** The five day-to-day destinations move from
the top bar to a glass **tab bar at the bottom**, where a thumb is, and the
configuration sidebar becomes a **drawer**.

What it replaced: a top bar with seven children in one flex row. At 393px it
wrapped onto three rows and *still* clipped the last tab, spending 280px of an
852px screen before the first pixel of content — on exactly the screens that get
opened standing in front of a rack.

| | Wide (>720) | Narrow (≤720) |
| --- | --- | --- |
| Five main tabs | top bar, segmented pills | `.tabbar`, fixed bottom, glass, icon + label |
| Site selector | top bar | top bar, takes the room the tabs gave up |
| Gear (configuration) | top bar | top bar |
| User, role, profile, language, theme, sign out | person glyph at the end of the bar → `.accountmenu-pop` | identical |
| Config / Add sidebar | column, collapsible | off-canvas drawer + scrim |

Rules that fall out of it, and that a new screen inherits:

- **`.content-scroll` carries the bottom padding**, not the page. The tab bar is
  `fixed` and reserves no space; without it the last row of every list sits
  under the glass. The shell does this once — do not re-add it per page.
- **Everything insets for safe areas.** `--safe-top` / `--safe-bottom` from
  `env()`, which is 0 everywhere that is not a notched phone. `index.html`
  carries `viewport-fit=cover`, which is what makes the insets non-zero at all.
- **`dvh`, not `vh`**, for the shell and for sheets — `vh` ignores the mobile
  URL bar and puts the tab bar under the browser chrome for the first scroll of
  every session.
- **A modal becomes a bottom sheet** (`.modal` under `bp:narrow`): full width,
  rounded only at the top, `88dvh` cap, padded past `--safe-bottom` so the
  confirm button is not under the home indicator.
- **The breakpoint is read once, in `Layout.tsx`, via `useBreakpoint()`** —
  the tab bar is not rendered at all on a wide screen rather than being
  hidden by CSS.
- **The account menu reads no breakpoint at all any more**, and that is the
  better end state: one icon and one panel at every width means there is no
  second layout to keep correct, and nothing that only exists on a phone.
  A control that behaves identically everywhere beats two that agree.

### 9.5 Touch

Under `@media (pointer: coarse)`:

- Controls take `min-height: var(--touch)` (44px). The guide's 30px `icon-btn`
  is a *mouse* measurement.
- **Fields go to 16px.** This is not a preference: iOS Safari zooms the viewport
  when a field under 16px takes focus, so every tap on an input on an iPhone
  zoomed the page and left the operator scrolled sideways on a layout that had
  been fitting a moment earlier. It reads as the layout breaking on touch, and
  it was happening on every form in the product.

### 9.6 The faceplate does not wrap

`FacadeBlockRows` (`pages/switchdetail/Faceplate.tsx`) scrolls sideways; its
rows are the physical rows of the box and they are `flex-wrap: nowrap`.

They used to wrap, and the cost was not cosmetic. On a phone a 48-port switch
rendered as six rows of eight with the odd and even ranks interleaved — not a
faceplate, a grid of labels that happened to be the same colours as one. Range
selection is measured on *physical* order, so ⇧-click then selected a set that
looked wrong on screen and was right. The operator counts sockets from the left
on the real box; the drawing has to survive that.

One scroller wraps **both** rows. Scrolling them separately would let the top
and bottom rank disagree about which port you are above.

### 9.7 Swipe navigation

Touch navigation between pages exists: horizontally through the five tabs in
tab-bar order, vertically through the active sidebar's entries in menu order.
It lives in `web/src/ui/swipeDecide.ts` (the arithmetic, no DOM) and
`web/src/ui/swipe.ts` (the event plumbing), wired once in `Layout.tsx`. The
reasoning is ADR-0009; the rule is here.

**Whoever can scroll in an axis owns that axis.** This interface scrolls in
both, and both are content (§9.2, §9.6) — there is no free axis to claim.
Hence:

- **Horizontal is lost by ANCESTRY.** If the gesture *starts* inside anything
  that can scroll sideways, horizontal navigation is dead for the whole
  gesture, whatever that scroller's position. A wide table and the faceplate
  keep their own gesture everywhere inside themselves.
- **Vertical is lost by POSITION.** Navigation is recognised only if the
  nearest vertical scroller is **already against the edge being pushed**
  (`scrollTop === 0` to go back, at the end to go forward), read at
  `pointerdown`. The asymmetry is deliberate: every page scrolls vertically, so
  the ancestry rule would forbid the feature outright.

**The recogniser is purely observational: it never calls `preventDefault()`.**
A gesture it reads wrong still does what the browser would have done. (The one
thing it does suppress is the single `click` that would follow a navigation
started on a link.) Recognition happens on the content area only — never on the
sidebar or the navigation bars.

Thresholds, if you are writing a screen that must survive them:

    axis lock        10px of travel, dominant axis × 1.7 (a diagonal is a hesitation)
    distance path    max(64px narrow | 96px wider, 12% of the axis)
    flick path       32px at 0.5px/ms, measured over the last 120ms
    cancel           a second finger (a pinch is not a navigation)

Touch only (`pointerType === "touch"`); no `wheel`, so a trackpad's two-finger
swipe still scrolls a wide table. Crossing the threshold shows a glass pill on
the target edge **naming the destination** — the page never follows the finger.
Under `prefers-reduced-motion` the pill appears without transition (`--dur` is
0ms, §4) and nothing slides; the pill carries text, so motion was never the
only carrier.

**The keyboard route stays the one that exists** — the tab-bar and sidebar
links, in tab order. Swipe is strictly additive and adds no key binding, which
is the condition for it not having to be accessible itself. Two consequences a
new page inherits: a **short page that does not scroll is at both vertical
edges at once**, so both directions navigate from the first touch; and an entry
marked outbound (↗) is excluded from the vertical order, because a swipe must
not take someone out of a section by accident.

---

## 10. Material — the glass layer

### 10.1 The whole rule

**Glass is chrome. Never content.**

Top bar, tab bar, sidebar, drawer, modal, popover, sticky toolbar, overlay.
Never a card, never a table, never a faceplate.

The reason is not taste. A port square seen through `blur(24px)` at 82% opacity
is no longer `--ok` green, it is *some* green — and §2 spent a page making sure
two greens never mean two things. Content stays opaque so that colour keeps
meaning what `network.ts` said it means.

### 10.2 What makes it glass and not "a card with opacity"

Three layers, and the third is the one people leave out:

1. a translucent tint (`--glass-bg`),
2. `backdrop-filter: blur(var(--glass-blur)) saturate(var(--glass-saturate))` —
   the saturation matters, because the average of many colours is always duller
   than any of them, and without it a blurred backdrop goes grey,
3. **the specular rim**: `box-shadow: inset 0 1px 0 var(--glass-rim)`, a
   hairline of light along the top edge where a real bevelled pane catches the
   room. Blur alone looks like fog.

Use the `.glass` class rather than the tokens directly.

The ground is painted by `body::before`: two very large radial washes at 4–7%.
They exist *so that there is something to refract* — a translucent bar over a
flat fill is just a lighter flat fill. They are deliberately below the threshold
of "there is a gradient here".

### 10.3 The tint is higher than you expect

`0.82` dark, `0.86` light. At `0.68` a 48-port faceplate read straight through
the mobile tab bar: the labels stayed legible, but green port squares sliding
under "Topology" is not depth, it is a rendering fault. The content behind glass
should be felt as colour and movement and never resolved as content.

Light needs **more** than dark, which is the opposite of the intuition: the
bleed-through that matters here is saturated port green, far louder against a
white tint than against a near-black one.

### 10.4 Three fallbacks, and they matter more than the effect

- `@supports not (backdrop-filter: …)` → **opaque**. A 82%-transparent bar with
  no blur behind it is unreadable; page text shows straight through the nav.
- `prefers-reduced-transparency: reduce` → opaque. Someone asked their OS for
  this, usually for a reason that applies here more than most: the interface is
  read under bad light in technical rooms.
- `forced-colors: active` → `Canvas`, no blur, no rim. A blur over a two-colour
  palette only smears the two colours the user asked for.

### 10.5 Elevation replaced the hairline

Depth is carried by `--shadow-1/2/3` and by the surface ladder, not by drawing a
1px border around every box. `.card` is `border-color: transparent` plus
`--shadow-1`; the hairline comes back only under `forced-colors`, where there is
no elevation to read.

A page of bordered rectangles reads as a *wireframe* of a page — which, with
uniform radii and a single accent, is most of what makes an interface look
generated rather than designed.

### 10.6 Typography is part of the material

- **IBM Plex Sans** for prose, self-hosted from `web/public/fonts` (~176 KB
  total; `unicode-range` means a latin reader fetches ~60 KB). Self-hosted and
  not a CDN because the controller is a single binary that must come up on a
  network with no route to the internet.
- **IBM Plex Mono, with `tabular-nums` and a slashed zero, for every technical
  identifier** — port names, addresses, MACs, rates, VLAN ids, build stamps.
  `.mono` sets it.

That split is the typographic half of "this is instrumentation". It is also
functional: `tabular-nums` is what stops a column of rates from jittering as the
live stream updates it, and `GE1` beside `GE11` only aligns in a monospace.

**A value that is prose is not a figure.** `StatTile` checks for a digit and
drops to the body face for "no measurement" — a sentence set in a display mono
reads as a code dump, and at `--fs-display` it wrapped out of its tile.

### 10.7 Small caps are not a default

The old sheet reached for `font-size: 10px; text-transform: uppercase;
letter-spacing: 0.1em` for *every* label: tile labels, `.kicker`, side-panel
keys. A label shouting as loudly as the figure it labels is the house style of
generated dashboards.

It survives in exactly two places, where it is a true heading over a group and
the case change is what separates it from the data: `th` and `.sidebar .section`.
(The account menu had a third, `.accountmenu-sect`; its settings became
label/control rows, where a sentence-case label beside the control says the
same with less noise.) Plus the badge-shaped ones (`.sev`, `.console-state`),
which are words-as-tokens.

Everywhere else: sentence case, and let weight and colour carry the hierarchy.
