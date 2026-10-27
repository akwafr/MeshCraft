---
Status: Proposed
Date: 2026-10-24
---

# ADR-0016 — A Wi-Fi access point is a device, but it only shows in Wi-Fi and on the topology

## Context

Wi-Fi is modelled **nowhere** in meshcraft. The data model starts from a switching
device: a faceplate, ports, per-port VLANs, STP, PoE, storm control, a connector
that can write all that.

The UniFi import brings access points. The four observed on the `ARENA` site are
AC Pros (`U7PG2`), and here is what the UniFi controller actually says about them:

- a complete identity: name, model, firmware, IP address, MAC, state. And a
  **configuration**, which the UniFi controller holds and whose readings
  meshcraft reads;
- two RJ45 ports (`idx` 1 and 2, only one of which is `UP`): ports, then, but two
  and not forty-eight, and with no VLAN or profile;
- two **radios**: `802.11n` at 2.4 GHz, channel 1, 20 MHz width; `802.11ac` at
  5 GHz, channel 36, 40 MHz width;
- statistics that exist on no switch: `txRetriesPct` per radio, and an access
  point CPU/memory load;
- a site SSID (`ARENA`, WPA2-PSK, bands `2g` + `5g`) that belongs to **no**
  access point in particular: all of them broadcast it;
- eleven associated **clients**, attached to an access point by `uplinkDeviceId`.

An access point is therefore, technically, a **device**: a box with an identity,
an address, a configuration and an uplink port plugged into the network. What
distinguishes it from a switch is **what it carries** and **who drives it**. It
has radios, bands, channels, SSIDs and associated clients, attributes an access
port does not have. And its configuration belongs to the UniFi controller, not to
meshcraft
([[ADR-0015 — The site extension lives in the controller, and its inventory is a read-only mirror]]).

Today, an access point that speaks LLDP already appears on the topology, but as a
**ghost peer**: an unknown neighbour, drawn as a thin link, with no model name and
no link to anything. Yet meshcraft knows exactly what it is.

## Decision

**The Wi-Fi access point is a device, and the product says so. It only shows in
two places: the Wi-Fi screen, which is its page, and the topology, which is its
place in the network. The SSID belongs to the site, not to the access point.**

1. **A device, not a driven device.** In the product's vocabulary, an access point
   is a device: it has a name, a model, a firmware, an address, a state, a
   configuration. But it does not enter `devices`, whose invariant is "something
   meshcraft configures through a connector" (ADR-0015 §5). It lives in the
   extension's mirror inventory, under the `access_point` class. The boundary is
   about who holds the configuration, not about the nature of the object.

2. **Three levels, not one.** The imported model distinguishes:
   - the **access point**: identity, model, firmware, state, address, the few
     copper ports;
   - the **radio**: band, standard, channel, width. It is a child of the access
     point, and never a port;
   - the **SSID**: an object of the site, with its bands and its security mode,
     and not a child of an access point.

   The associated client is not a fourth level: it is attached by MAC to the
   clients meshcraft already models.

3. **It appears on the Wi-Fi screen and on the topology, and nowhere else.**
   - **The Wi-Fi screen is its page.** Identity, radios, configuration read, SSIDs
     broadcast, associated clients. That is where one clicks to see it.
   - **The topology shows it as an access point, no longer as a ghost.** The
     unknown LLDP peer whose MAC matches an access point in the mirror becomes a
     named node, with its type and state, connected to the port where it is seen.
     Clicking it leads to the Wi-Fi screen, not to a device page.
   - **It stays absent from the Equipment list**, from the switch pages, the
     faceplates, the port gestures, PoE, STP and the device pickers. These screens
     are those of what meshcraft drives, and an access point would answer "none"
     to every question there.

4. **Correlation with the switched world is done by MAC, not by merging models.**
   An access point is plugged into a port of a switch that meshcraft may already
   drive, so LLDP and the FDB table know its address. The two inventories are
   linked by that address, at read time, first through LLDP, then through the FDB
   as a fallback. That is what places the access point on the topology and answers
   "which port is this access point on" without making it a driven device.

5. **Wi-Fi is a screen of the Switching mode.** The screen **declares** its mode
   ([[ADR-0019 — The switcher carries only the brand, the mode reads in the bar]]),
   and that mode is Switching: an access point bridges frames onto a VLAN, it does
   not route. The mode only filters navigation, so this declaration restricts
   neither data nor permissions.

6. **The units are those of radio, not those of copper.** The band is stated in
   GHz, the width in MHz, the channel is a unitless integer, the retry rate a
   percentage. We do **not** reuse the traffic ramp of
   [[ADR-0003 — The traffic ramp is logarithmic and relative to capacity]] for a
   radio quality: a radio capacity is not a port capacity, and the same ramp would
   say something false.

## Consequences

- **A Wi-Fi screen has to be written instead of reusing one.** No faceplate, no
  port table. That is the direct cost, and it is paid once.
- **The topology gains a second node type**, which does not come from `devices`.
  It must read the mirror in addition to the driven inventory and be able to draw
  a node it can neither configure nor open in place. Any topology code that
  assumes "one node = one `devices` row" becomes wrong.
- **The link to an access point has only one half measured by meshcraft.**
  [[ADR-0026 — A topology link has two halves, each coloured by its end's egress]]
  colours each half by the egress of its end. For the access point's half, the
  access point's egress is the ingress of the switch port opposite: it is the same
  cable. It is read there, and UniFi is not asked for counters for the topology.
  If the access point is plugged into a switch meshcraft does not drive, the link
  has no measurement, and it is drawn as such.
- **An access point invisible in LLDP and absent from the FDB is not on the
  topology.** It stays on the Wi-Fi screen. It is not hooked on "somewhere" to fill
  the drawing.
- **Counting "a site's devices" now requires saying whether access points are
  included.** The Equipment list counts what meshcraft drives. The topology shows
  everything that is plugged in. The two figures differ, and that is intended.
- The day a second Wi-Fi vendor arrives, it is this model it will have to fill. It
  is therefore public and must stay neutral: the fields are those of 802.11, not
  those of UniFi. `wlanStandard`, `frequencyGHz`, `channelWidthMHz` translate;
  `configurationId` does not translate and stays opaque vendor data.
- An SSID carried by the site, and not by the access point, rules out from the
  start displaying "this access point's SSIDs" as a local truth. It is the right
  model, and it goes against intuition: the operator does see an SSID in the air
  next to an access point. The screen will therefore have to make the attachment
  explicitly, and not leave it to be guessed.
- Radios are an instantaneous reading, not a configuration. We do not promise to
  see them change live, and a displayed channel may be stale. Freshness is read
  according to
  [[ADR-0008 — The time reference is the viewer's clock]].

## Alternatives ruled out

**Putting access points into `devices` with `brand = "UniFi"`.** The option that
costs zero lines of model and makes all the existing display free, topology
included. Ruled out because it confuses "is a device" with "is driven by
meshcraft". The first thing someone does when clicking a device in the list is
open its ports. Two RJ45 ports on a faceplate page designed for forty-eight, with
no VLAN, no PoE and no neighbour, is not a degradation: it looks like a bug. And
every port gesture would have to learn an exception.

**Keeping the access point off the topology**, which is what the first draft of
this ADR said ("no free topology"). Ruled out: the topology answers "what is
plugged in where", and an access point is plugged in somewhere. Leaving it as a
ghost peer shows an unknown neighbour where meshcraft knows there is a named AC
Pro, which is correct information made false by omission. It is also on the
topology that one looks for an access point when Wi-Fi clients complain about a
VLAN.

**Listing it in Equipment with an "access points" filter**, so that it is
everywhere devices are. Ruled out: every screen that consumes the list (pickers,
bulk gestures, management conformity) would have to know how to exclude the
class, and the first omission offers a port gesture on an access point. Two
precise places are better than an exclusion to repeat everywhere.

**Modelling the radio as a port**, with the band as the connector. Attractive,
because [[ADR-0007 — The aggregate is a port on the faceplate]] has already
widened the notion of port once. Ruled out: the aggregate *behaves* like a port,
with a speed, VLAN members, counters, a neighbour. A radio has none of that.
Passing it off as a port would contaminate all port rules (profiles, storm
control, root bridge) with an "unless it is a radio" case.

**A single generic "network node" model**, of which switch and access point would
be two specialisations. The clean architecture, in theory. Ruled out because it
requires recasting `devices`, the most central and most written table of the
product, to accommodate four read-only access points. The ratio of risk to gain is
not there today. If a second Wi-Fi vendor arrives and a third object model comes
up, that is the ADR to write at that time.

**Attaching the SSID to the access point**, since it is the access point that
emits it. Ruled out: the SSID is configured at the UniFi site level and broadcast
by all access points. Duplicating it per access point would create N copies to
reconcile, and the first inconsistency between two copies would be
indistinguishable from a real configuration deviation.

**Putting the Wi-Fi screen in Routing mode**, on the grounds that Wi-Fi "is not
switching". Ruled out in point 5: an access point bridges frames onto a VLAN, it
does not route. Putting it in routing would move it away from precisely the ports
it is plugged into.
