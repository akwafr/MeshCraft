---
Status: Accepted
Date: 2026-10-26
---

# ADR-46584 — Identity is an icon, and sign-out lives behind it

## Context

The top bar carried, on the right, an **identity group** of five controls: the user's
name (link to the profile since
[[ADR-0011 — A token authenticates, it does not authorise]]), the "global admin"
badge, the language flag, the three appearance segments, and a sign-out icon.

Measured on the switching bar, in French: this group weighs **398 px** on a bar that
wants 1180 and has 960. It was the widest object on the bar, ahead of the five tabs.
It took two fallbacks to make it fit: the name disappears between 960 and ~1140 px
(only the glyph remains), and below 960 px the whole group goes behind a `…` button.
The result: three presentations of the same thing depending on width, and a bar that
overflowed anyway at 25 of the widths tested between 721 and 1440 px
([[ADR-80600 — Ctrl+F searches the site, the second press hands back to the browser]]
records the discontinuity at 960 px that this fallback causes).

These five controls answer questions one asks once per session. The tabs and the site
selector, next to them, serve at every click.

The request: **replace the sign-out icon with a user icon**, which opens a panel
giving the name, the permission, the link to the profile and sign-out, and **remove
the name from the bar**.

## Decision

**A single person icon, at every width. Behind it: the identity, the permission in
force, the profile, appearance, language and sign-out.**

1. **The bar no longer carries the name nor sign-out.** A person icon takes the place
   of the sign-out icon, in the same spot, at every width. The `…` fallback below
   960 px and the name fallback between 960 and 1140 px disappear, since there is
   nothing left to fold.

2. **The first line of the panel is the identity, and it is the link to the
   profile.** The name, the permission, and the destination stated in plain words
   ("My profile"). ADR-0011 made the name the door to the profile; it remains so, it
   has simply left the bar.

3. **The permission displayed is the one that applies to the open screen.** A global
   admin sees their badge. Others see their role **on the current site** (viewer,
   operator, site administrator), and nothing when no site is selected. Showing the
   list of all roles would answer "what can I do in general", whereas the question one
   asks before a gesture is "what can I do here". The full list remains on the profile
   page.

4. **The panel opens on hover, on click and from the keyboard, and it is the click
   that pins it.** Hover alone would make sign-out impossible with a finger and
   haphazard from the keyboard. A panel opened on hover closes when the pointer
   leaves; opened or clicked, it stays until `Escape` or a click outside
   (`usePicker`, UI-GUIDE §8.1).

5. **It is a disclosure, not a `role="menu"`.** The panel contains a link, a radio
   group, a selector and a button. In menu mode, a screen reader only reads the
   `menuitem`s: the appearance segments and the language list would become mute.
   `aria-expanded` + `aria-controls` describe what it really is.

## Consequences

- **Signing out costs one more gesture**: open the panel, then click. That is the
  price paid, and it is accepted: one signs out once per session, one navigates a
  hundred times.
- **The user's name is no longer permanently visible.** On a shared workstation,
  knowing under which account one acts requires opening the panel. The icon carries
  the name in its `aria-label` ("Account — Ada Byron"), but a sighted reader does not
  read it at a glance.
- **The bar gains about 380 px**, and there is only one presentation of the identity
  left to maintain. The rule "the account menu reads no breakpoint" enters UI-GUIDE
  §9.4.
- **A displayed role is a role read from `/api/auth/me`**, when the session loads. A
  role changed by an admin during the session only appears there on reload, as
  everywhere else in the interface.
- Language and appearance, settings of the person, follow them into the panel.
  Changing them also costs one more gesture; one changes them once a month.

## Rejected alternatives

**Keep the name in the bar and only move sign-out.** It is half of the request, and it
leaves in place most of the 398 px, hence the two fallbacks and the three
presentations. The name was precisely the element the bar could not keep between 960
and 1140 px.

**A real `role="menu"` with `menuitem`s.** The usual "account menu" pattern, and it
would make two of the panel's four controls mute for a screen reader. Rejected at
point 5.

**Opening on hover only**, as on many sites. Rejected at point 4: sign-out now lives in
this panel, and a finger never hovers.

**Display all of the user's roles in the panel.** Correct, but a list of twelve sites
answers the wrong question at that spot. It is on the profile page, one click away,
from the first line of the panel.
