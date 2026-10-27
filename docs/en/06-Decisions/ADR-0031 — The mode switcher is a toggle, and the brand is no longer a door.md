---
Status: Accepted
Date: 2026-10-25
Refines: ADR-0013, ADR-0019
---

# ADR-0031 — The mode switcher is a toggle, and the brand is no longer a door

## Context

[[ADR-0013 — Switching and routing are two navigation modes, not two products]] had named the risk, in so many words, in its consequences:

> The switcher occupies the logo, which is **also** the way back to the dashboard.
> Two gestures on the same object: dragging and clicking must remain
> distinguishable, by finger as by mouse.

[[ADR-0019 — The switcher carries only the brand, the mode reads in the bar]] carried this point over as is, and it is its §4: "The gesture does not
change: dragging toggles, clicking goes back to the dashboard, with the threshold and
axis dominance already measured."

**The risk materialised.** The disambiguation was careful and it was measured: a
24 px threshold and an axis dominance of 1.7 (`modeFromDrag`), pointer capture to
follow a finger that leaves a 26 px button, `draggable={false}` to kill the link's
native drag-and-drop, `touch-action: pan-y` to leave the vertical axis to the
browser, and the click following a gesture swallowed once (`swallowNextClick`). In
the lab, it works. In use, the feedback is: "the routing/switching switcher is still
buggy".

It is the only verdict that counts, and it does not say the disambiguation is badly
tuned. It says that **the only way to change mode was a drag that nothing
announces**. Worse: ADR-0019 had removed from view the two words that hinted there
was a switcher there, which it had itself recorded on the debit side ("A user
discovering the product no longer knows the switcher is a switcher"). What remained
on screen, then, was a logo. You press it — a reflex acquired on just about the whole
web — the page goes to the dashboard and the mode does not change. The user calls
that a bug. They are right.

The two half-positions already existed and responded to clicks. They paint nothing
(that is ADR-0019 §1), they measure 16 px, and one of the two sits **between** the
brand and the edge of the pill: aiming at the logo and landing two pixels off does
nothing at all. An invisible target next to the only visible object is not a target.

## Decision

**The switcher is a toggle. Any press on it changes mode and the brand slides to the
other side. It no longer goes back to the dashboard.**

1. **One gesture, only one.** Dragging is **removed**, not made optional:
   `modeFromDrag`, its threshold, its axis dominance, finger tracking and the
   swallowing of the next click leave the code. There are no longer two gestures on
   the same object, so nothing left to disambiguate — nor to retune the next time
   someone finds it catches.

2. **The whole pill toggles.** The brand is the thumb and occupies **exactly** the
   active half of the track; the other half is the empty slot of the position you are
   not in. These are the only two surfaces a pointer can reach, and both toggle:
   pressing the brand, pressing the empty slot, tapping anywhere on the pill with a
   finger gives the same result. There is no dead zone any more, because the active
   half-position is **covered** by the brand.

3. **Going back to the dashboard leaves the brand.** The "Dashboard" tab exists in
   both modes — it is data in `MODE_TABS` and `navMode.test.ts` asserts it for both
   bars — so at the top at 1440 px and in the thumb bar at 393 px. Before a site
   exists, the only reachable area is configuration, and it is the gear that leads
   there (`configTo`), not the brand. The function is not lost: it stops being
   duplicated on an object that already had a role.

4. **Hovering says what pressing will do.** The brand carries a `QuickTip` that names
   the **destination** — "Routing" when in switching, "Switching" when in routing —
   and it is the word the empty half-position already carries, since the only
   reachable half is always the one you are not in. Both surfaces therefore announce
   the same word and do the same thing. It is more than ADR-0019 left (two position
   names one had to hover one by one to learn), and it gives back part of the
   affordance it had lost.

5. **The keyboard keeps two named positions, and they remain selective.** ADR-0019 §3
   is intact: a `radiogroup`, two `radio`s, `aria-checked`, reachable with Tab,
   activated with Space, and their position label ("Switching" / "Routing") opens on
   focus — which is why `QuickTip` learnt to react to focus. With the keyboard you
   **choose** a position; with the pointer you **toggle**. Both lead to the same state
   and neither promises anything other than what it does.

## Consequences

- **The brand is no longer a link.** It becomes an inert element for assistive
  technologies (`aria-hidden`), out of the tab order, whose click is a **pointer
  shortcut**: everything it does, the two `radio`s do too, so nothing is reserved for
  the mouse. Someone looking for the dashboard by clicking the logo no longer finds it
  there. That is the price, it is real, and we prefer it to the ambiguity we have just
  measured.
- **With the pointer, one can no longer choose a position, only toggle.** With two
  positions it is the same thing. The day the mode has three, this choice is to be
  redone — and it will be another ADR, because a three-state toggle does not exist.
- **Dragging disappears**: whoever had learnt it loses it. We accept that because it
  was discoverable by nobody.
- `swallowNextClick` loses its second client and becomes private to `ui/swipe.ts`
  again ([[ADR-0009 — A swipe never steals a scroll gesture]]). The
  comment that explained why it was exported was becoming false.
- **The exception to ADR-0009 falls with the gesture**: no more `touch-action:
  pan-y` on the brand, the browser takes back both axes, and a thumb that starts from
  the logo scrolls the page as everywhere else. Check that swiping between tabs
  (`useSwipeNav`, bound to the content element only) stays indifferent to the top bar
  — it is by construction, it must remain so.
- **The pill does not grow, it slims down** — measured, not assumed. Two 34 px halves
  replace the 34 + 16 + 16 plus two gutters: **74 → 72 px**. Sweep from 721 to 1440 in
  steps of 8, in French, switching bar (ADR-0019's worst case): **6 widths out of 90
  overflow, by 27 px at worst**, against 10 out of 90 that ADR-0019 left; the routing
  bar stays at 0 out of 90. The top bar's budget is therefore not reopened, it is a
  little less bad. At `narrow` the pill goes from 94 to 84 px **and** the useful target
  goes from two 28 px strips to an 84 px object every press of which toggles.
- **The brand slides, in `--dur-fast`**, because the movement *is* the information
  asked for. `prefers-reduced-motion` already sets that token to 0 ms and so makes it
  jump: the transition is not a decoration to be cleaned up.
- The rest does not move: the URL is authoritative (ADR-0013 §2), the mode is a
  visitor preference (§3), routing's red remains a dedicated token and never
  `--danger` (§4), and it is the tab bar that tells the mode (ADR-0019 §1).

## Rejected alternatives

**Keep the click to the dashboard and distinguish the two gestures some other way.**
That is what has been done since ADR-0013, with a measured disambiguation, and the
verdict in use is "buggy". The remaining variants are worse: a long press to toggle is
invisible and conflicts with the context menu; a double click, nobody guesses it; "the
logo for the dashboard, the half-positions for the mode" is *exactly* the current
state, whose defect is that the half-positions paint nothing. All of them make the
control's main function rest on the least likely gesture.

**Take the switcher out of the logo.** Honest, and it solves the problem by
construction. Rejected because the original request (ADR-0013, Context) is a switcher
**carried by the logo**, because that is what gives the top bar one object fewer, and
because the brand changing sides is precisely the signal asked for: "a simple switcher
where the meshcraft logo moves from right to left".

**A real `role="switch"` rather than a `radiogroup`.** More faithful to what the
pointer now does, and that is its only advantage. A `switch` has one name and two
states: a screen reader announces "Routing, on" and no longer announces more than
**one** position. ADR-0019 §3 requires both, and a toggle between two named functions
is not a checkbox.

**Make the half-positions toggle too, without position logic** — any click on the pill
inverts the mode, and the code no longer even needs to know where you clicked. Simpler
by one line, and hovering becomes a liar: the left half would say "Switching" and give
routing. Rejected because no surface must promise anything other than what it does —
and because the simplification is illusory: since the reachable half is always the
inactive one, "select its position" and "toggle" are already the same gesture there.

**Keep dragging in addition to clicking.** It costs almost nothing to leave in place
and it no longer bothers anyone, since the brand is no longer a link: there is no click
left to compete with it. Rejected all the same, because a gesture that nothing
announces and that the click makes useless is code nobody tests and that the next
reader will have to understand for nothing. Two paths to the same state are one path
too many when one of the two is invisible.
