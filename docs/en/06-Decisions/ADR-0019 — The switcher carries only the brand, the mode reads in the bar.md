---
Status: Refined by ADR-0031
Date: 2026-10-25
Supersedes: ADR-0013
---

# ADR-0019 — The switcher carries only the brand, the mode reads in the bar

## Context

[[ADR-0013 — Switching and routing are two navigation modes, not two products]]
remains right about everything except one point, and that point is its §5:

> **Colour never carries the mode on its own.** The switcher is a real control: it
> has a **text label** ("Switching" / "Routing"), it is reachable by keyboard, it
> announces its state.

Built that way, the switcher permanently displays both words on either side of the
brand. In use, two things:

1. **It is the largest object in the top bar**, in a bar that was already
   overflowing between 760 and 820 px before anything was added to it, and that had
   to be frozen child by child to hold from 721 to 1440.
2. **The label was not the only non-colour cue, nor even the main one.** That is
   the reasoning ADR-0013 lacked: when switching, *the tab bar changes its content
   entirely* — Equipment, Ports, Topology, Clients on one side; Routing, Firewall,
   DNS, DHCP on the other. Someone who cannot distinguish the brand's tint still
   reads, plainly and without hovering, which mode they are in.

ADR-0013 therefore protected a correct principle with a redundant means, and paid
for that means dearly on the most constrained screen of the product.

## Decision

**The switcher contains only the meshcraft brand. The two labels become hovers.**

1. **The principle of ADR-0013 §5 is kept, not abandoned**: colour never carries
   the mode on its own. What changes is **who** carries it — the **tab bar**, whose
   content is the information, and not two words stuck to a button.
2. **The labels remain reachable**, on hover and on keyboard focus, through
   `QuickTip` — never a native `title=`.
3. **The accessible names do not disappear.** The control keeps its `radiogroup`
   and its two `radio`s with their accessible names: a screen reader still
   announces "Switching" and "Routing" and which one is active. A label visually
   removed is not a label deleted.
4. **The gesture does not change**: dragging switches, clicking goes back to the
   dashboard, with the threshold and axis dominance already measured.

## Consequences

- The top bar recovers the width of the two words, on the screen where it was most
  lacking. That is the direct benefit and the only one measurable right away.
- **A user discovering the product no longer knows the switcher is a switcher**
  until they have hovered it. That is the price, and it is real: the affordance now
  rests on the shape of the control and on the fact that the bar changes, not on a
  word. To watch at the first observed onboarding.
- **Hover does not exist for a finger.** On mobile, the labels will never be read:
  only the switch of the bar informs. That is acceptable because it is precisely
  there that the tab bar is most visible — but it means that point 1 is not a safety
  net, it is **the** mechanism.
- ADR-0013 remains the reference for everything else: the URL is authoritative
  (§2), the mode is a visitor preference (§3), red is a dedicated token and never
  `--danger` (§4), each screen declares its mode.

## Alternatives ruled out

**Keeping the labels and shrinking elsewhere.** It was already done once — the bar
only holds from 721 to 1440 at the price of freezing child by child and of a single
element allowed to truncate. Taking room from something else to pay for two
redundant words is a bad trade.

**Displaying only the active mode's label.** Half the cost, half the benefit, and
the control stops announcing that it has two positions — which is the only thing
the label really brought.

**A label visible below the mobile threshold only.** Exactly backwards: hover does
not exist for a finger, so it would show the text where room is lacking and hide it
where room abounds.

**Changing nothing.** Defensible, and it is what ADR-0013 said. Ruled out because
its §5 treats the label as the guarantor of the principle, whereas the real
guarantor is the tab bar — and we pay for a redundant guarantor on the most
constrained screen.

## Addendum of 2026-10-25 — what measurement corrected

Four claims of this ADR were checked after the fact. **One was false, two were too
pessimistic, one was outdated.** They are corrected here rather than retouched
above.

**"It only holds from 721 to 1440 at the price of freezing child by child" —
FALSE. It did not hold.** Sweep every 4 px, switching bar in French: **74 widths out
of 120 overflowed**, up to **143 px at 721**. The switcher was not the cause, it was
an aggravating factor. After this change **19 out of 120** remain (721–773, ≤ 53 px),
and the routing bar goes from 21 to **0**. So this work **improves a pre-existing
defect, it does not close it** — and the real driver is the identity group plus six
tabs.

Intrinsic width, worst case (French, switching): **1314 → 1180 px**. The chip
itself: **208 → 74 px**.

**"Hover does not exist for a finger, the labels will never be read" — too
pessimistic.** `QuickTip` pins on a coarse pointer: a tap switches the mode *and*
leaves the label on screen until the next one. The point remains true that one
cannot *hover* to learn before acting.

**"Keeping the labels and shrinking elsewhere" — the alternative was ruled out for
the wrong reason.** The three relaxations were tried: restoring the display name
reopens 968–1152 (10 → 31 out of 90); restoring the `mid` gutter deepens 721 from
−50 to −70 px; and **`flex-shrink: 1` changes strictly nothing** — the automatic
minimum of a flex item is its min-content width, and all children are unbreakable
words. Freezing is therefore a statement of intent, not the mechanism: the only real
lever is the site picker's 112 px floor, already spent.

**The context's enumeration is outdated**: it is 6 against 5, not 4 against 4 — the
dashboard is in both modes and Wi-Fi has joined switching (ADR-0016). The argument
holds a fortiori.

Finally, a gap this work revealed and filled: **`QuickTip` had no focus trigger**.
Without it, replacing a permanent label with a hover would have been a regression
disguised as a width gain, and point 3 of this ADR would have been false for the
keyboard.
