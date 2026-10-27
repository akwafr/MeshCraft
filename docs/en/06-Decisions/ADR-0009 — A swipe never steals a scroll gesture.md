---
Status: Accepted
Date: 2026-10-24
---
Note: Written under number 0007, renumbered 0009 at the merge — 0007 was
  already taken by the decision on aggregates, written in parallel. A number
  is never reused.
---

# ADR-0009 — A swipe never steals a scroll gesture

## Context

Below 720 px the shell inverts (UI-GUIDE §9.4): the five everyday destinations
move into a bottom bar, the sidebar becomes a drawer. Changing page then requires
aiming at a 44 px target at the bottom of the screen, or opening the drawer,
choosing, and letting it close. On a phone held in one hand in front of a rack,
the lateral swipe is the gesture everyone tries first.

What makes the question non-trivial: **this interface already scrolls in both
axes, and horizontal scrolling here is content, not decoration.**

- `.scroll-cue` / `.scroll-x` (UI-GUIDE §9.2): wide tables keep their real width
  and scroll — `.ports-table` is 860 px minimum, `.clients-table` 900 px, on a
  393 px screen.
- The equipment page's faceplate is a **full-screen** horizontal scroller that
  never wraps (UI-GUIDE §9.6): its rows are the physical rows of the box, and range
  selection is measured on the physical order. A swipe that takes its gesture does
  not degrade the screen: it breaks the only thing this screen knows how to do.
- The vertical axis is even worse: `.content-scroll` is the single scroll surface
  of **all** pages.

There is therefore no place in the application where an axis is free by
construction. The question is not "how to recognise a swipe" but "who owns the
gesture".

Two other facts frame the answer. A touchpad emits two-finger horizontal gestures,
which arrive as `wheel` with `deltaX` — **indistinguishable from scrolling a wide
table**, and already claimed by Safari's and Chrome's history back-navigation. And
the frontend ratchet forbids `addEventListener("keydown")` outside `ui/`: every
keyboard shortcut goes through `useGlobalKey`, which does not pass the event on and
therefore cannot filter a modifier.

## Decision

**An element that scrolls in an axis owns that axis.** The recogniser
(`web/src/ui/swipeDecide.ts`, wired by `web/src/ui/swipe.ts`) is purely an
observer: it never calls `preventDefault()`, so a gesture it misreads still does
what the browser would have done.

The horizontal swipe goes through the five tabs in the bar's order; the vertical
swipe goes through the entries of the active sidebar (configuration, add) in the
menu's order. Both are recognised **on the content area only**, never on the
sidebar nor on the navigation bars.

The four arbitrations, by name:

**1. The horizontal axis is lost by ancestry, the vertical axis by position.**
If the gesture *starts* inside an ancestor that can scroll horizontally,
horizontal navigation is dead for the whole gesture — whatever the scroller's
position. Vertically, it is the opposite: navigation is only recognised if the
nearest vertical scroller is **already at the edge being pushed** (`scrollTop ===
0` for downward, at the stop for upward).

The asymmetry is deliberate. A horizontal edge is crossed while reading a 48-port
table; triggering navigation there would make the same gesture, in the same place,
do two different things depending on the reading position. A vertical edge is
reached on purpose, and the ancestry rule would forbid the feature everywhere
since all pages scroll vertically.

**2. Threshold and velocity.** Axis lock first: at 10 px of movement, the
dominant axis must exceed the other by a factor of 1.7, otherwise the gesture is
abandoned — a diagonal is a hesitation. Then two validation paths: a **distance**
of `max(64 px on a narrow screen | 96 px beyond, 12% of the axis)`, or a **flick**
of 32 px at 0.5 px/ms measured over the last 120 milliseconds. A second finger
cancels: a pinch is not a navigation.

**3. The page does not follow the finger.** When the threshold is crossed, a glass
chip appears on the targeted edge and **names the destination**; releasing goes
there, going back under the threshold cancels it. What people want from a page
that follows the finger is to be able to cancel and to know where they are going:
the chip gives both without transforming the page.

Transforming the page would cost three things we refuse: owning its `transform`
and its `touch-action`, hence fighting the two scroll surfaces; mounting two
routes at once or snapshotting the outgoing one, on screens that poll the network
continuously; and animating a 48-port faceplate on every frame. The chip is a GPU
composite on a 150 px element.

**4. Touch only, and the keyboard stays what it is.** The recogniser filters on
`pointerType === "touch"`, listens to no `wheel` and adds no keyboard binding. The
keyboard and screen-reader path for changing page remains the one that exists: the
links of the tab bar and of the sidebar, in tab order. The swipe is strictly
**additive** — that is the condition for it not having to be accessible itself.

`prefers-reduced-motion`: the chip appears without transition (`--dur` is 0 ms
under that preference, tokens.css §9) and no page slides. Motion was not the
carrier of the information anyway — UI-GUIDE §4: *motion is never the only
carrier*. The chip carries text.

## Consequences

- **One cannot change tab by swiping from the faceplate** of the equipment page,
  nor from a wide table while the finger is on it. That is the exact price of rule
  1, and it is paid at the place where horizontal scrolling is the screen's
  function. The rest of the page stays responsive.
- **A downward vertical swipe at the top of a short page navigates.** A page that
  does not scroll is at the stop on both sides: both directions are available
  straight away. Intended, and to be remembered when designing a short page under a
  sidebar.
- **The horizontal swipe is inert in `/config`**: no tab is selected there (the
  gear owns configuration, not a tab), so there is no order to go through. Only the
  vertical axis lives there.
- **An entry marked "outgoing" (↗) is excluded from the vertical order.** The link
  to global credentials leaves the section; a swipe must not take one out of an
  area inadvertently. The glyph and the exclusion say the same thing.
- The recogniser reads `scrollWidth`/`scrollHeight` and one `getComputedStyle`
  per ancestor **on `pointerdown`**, never during the movement. One layout cost per
  gesture, not per frame.
- Two behaviours now exist for the same finger movement depending on the starting
  element. It is a real complexity; it is unavoidable as soon as a navigation
  gesture is layered over a scrolling interface, and the rule "whoever scrolls
  owns" is the only formulation that can be stated out loud.

## Alternatives ruled out

**Having the page follow the finger.** The expected form, and the most expensive:
it requires owning `touch-action` on the content area, which disarms the scrollers
we have just spent an ADR protecting; it requires mounting the neighbouring route
to have something to slide, on screens hooked up to network requests; and it
requires animating per frame a faceplate that is already the rendering hot spot.
The chip renders the only service tracking really rendered — "I know where I am
going, I can cancel" — for a hundredth of the cost.

**The edge test on both axes** (navigate when the horizontal scroller is already at
its stop). Attractive by symmetry, and it is what makes the same swipe, at the same
spot of the faceplate, scroll or navigate depending on the reading position. A
gesture whose effect depends on invisible state cannot be learnt.

**Requiring two fingers** to navigate. Solves the conflict in one stroke, and
nobody discovers it: a two-finger gesture has no affordance, and on iOS it is
already claimed by VoiceOver.

**Taking the touchpad too** (`wheel` with `deltaX`). A two-finger horizontal
scroll on a wide table is bit for bit the same event as a navigation swipe; we
would lose wide-table reading on the workstation to gain a gesture that, there, is
already done by clicking a visible tab. Safari and Chrome additionally claim that
axis for history.

**A keyboard shortcut `Alt+Arrow` or bare `Arrow`.** `useGlobalKey` does not pass
the event on: impossible to require a modifier. A binding on bare `ArrowLeft`
would collide with keyboard navigation of the faceplate and tables, and
`Alt+Arrow` is the browser's history. The tab bar and the sidebar are already
focusable links: the keyboard path exists, and one must not layer over it another
that breaks the others.

**Gating the swipe on `useBreakpoint() === "narrow"`.** A 1024 px touch tablet is
a real field workstation and has no reason to be deprived of it. Width serves
another purpose here: it sets the threshold floor (64 px narrow, 96 px beyond),
because an accidental 70 px slide is commonplace on a large panel.
