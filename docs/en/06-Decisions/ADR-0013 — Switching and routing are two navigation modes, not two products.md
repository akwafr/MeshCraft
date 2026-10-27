---
Status: Superseded by ADR-0019
Date: 2026-10-24
---

# ADR-0013 — Switching and routing are two navigation modes, not two products

## Context

The product is opening up to L3: routing, firewall, DNS, DHCP. The current navigation
fits on one line — Devices, Ports, Topology, Clients — and four more entries would make
it overflow, on a laptop screen as on a phone, where the bottom tab bar is already full.

The request is a **switcher carried by the logo**: "Switching" on the left, "Routing"
on the right, the logo turning red in routing.

The trap is to take this for a cosmetic request. A mode that hides half of the
navigation is an **architecture decision**: every future screen will have to declare
its side, and someone will arrive in good faith asking why they cannot see the
firewall from the ports page.

## Decision

**The mode filters NAVIGATION. It filters nothing else.**

1. **Same data, same API, same rights.** The mode is neither a permission, nor a data
   partition, nor a site setting. It is a preference of the viewer, just like the
   theme. A `viewer` in routing mode gains nothing; an administrator in switching mode
   loses nothing. Authorisation remains entirely `CanSite` / `RequireGlobalAdmin`, as
   laid down by [[ADR-0011 — A token authenticates, it does not authorise]].

2. **The URL is authoritative, the switcher is only a shortcut.** A deep link to
   `/routing/firewall` opens the firewall **and switches the mode**. It does not return
   a 404, and it does not display a page whose tab is invisible. That is the rule that
   prevents the mode from becoming a trap: a link pasted into a ticket must work for
   someone whose mode is the other one.

3. **The mode is persistent per visitor**, like the theme: one finds it again on
   returning. But rule 2 always takes precedence over the saved preference.

4. **The routing mode's red is a DEDICATED token, not `--danger`.** This is the point
   we were going to miss.
   [[ADR-0001 — Two themes, and the status palette that must survive both]] makes red
   the colour of failure, across the whole surface of the product. Reusing `--danger`
   to say "you are in routing" would make the same colour say two incompatible things,
   and the day a firewall fails, the screen is red for two unrelated reasons. The mode
   therefore gets its own token, declined in both themes, chosen to be **distinct**
   from the status red.

5. **Colour never carries the mode on its own.** The switcher is a real control: it
   has a text label ("Switching" / "Routing"), it is reachable from the keyboard, it
   announces its state. A logo that changes hue is not information for someone who
   cannot distinguish that hue.

## Consequences

- **Every future screen declares its mode.** It is data in the application's routing
  table, not a tacit convention — a screen without a mode is a screen nobody finds.
- A screen can legitimately belong **to both** (devices carry L3 interfaces as much as
  ports). The model must allow it from the start; forcing a single side would make us
  duplicate pages.
- **Switching is not a navigation.** Going to routing from the clients page must lead
  somewhere sensible, not display an empty page: the mode has a default destination.
- On mobile, the bottom tab bar changes content all at once. It is the benefit sought
  — four entries instead of eight — and it is also what surprises most: hence the
  visible label, not just the colour.
- The switcher occupies the logo, which is **also** the way back to the dashboard. Two
  gestures on the same object: swiping and clicking must remain distinguishable, with
  a finger as with a mouse.

## Rejected alternatives

**A single bar with eight entries.** The honest option, and probably the right one if
the product were to stop there. Rejected because the list does not stop at eight: L3
brings routing, firewall, DNS, DHCP, and what follows will bring something else. A bar
that overflows cannot be repaired, it gets truncated.

**Two applications, or one subdomain per mode.** Solves navigation by creating two
products, two sessions, two ways of getting the window wrong. And it contradicts
reality: it is the same device, often the same port.

**A site setting, or an instance setting.** Would make the mode a property of the data
instead of a reading preference. Two people looking at the same site must be able to
look at it differently.

**Hiding according to rights** rather than according to a chosen mode. Attractive —
only what is permitted is shown — and it is exactly the confusion ADR-0011 exists to
avoid: authorisation is one model, navigation is another, and making them coincide
makes them diverge. We would also hide from a legitimate reader the proof that the
feature exists.

**Reusing `--danger` for routing mode**, since it is "the product's red". Rejected at
point 4: one colour, one meaning.
