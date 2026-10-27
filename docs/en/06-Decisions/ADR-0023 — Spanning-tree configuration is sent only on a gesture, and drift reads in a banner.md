---
Status: Accepted
Date: 2026-10-25
---

# ADR-0023 — Spanning-tree configuration is sent only on a gesture, and drift reads in a banner

## Context

ADR-0021 made the MST region identity **computable and visible at site level**.
ADR-0022 made the ability to set it **declared** by the connector. Neither touched
the **moment** when meshcraft writes spanning-tree, and that is where the danger
lives.

Today, `internal/api/autoapply.go` calls `applyDeviceSTP` **inside `runApply`**,
that is to say inside the gesture "apply this device's ports". The comment on
`runApply` says so bluntly: *"Runs even when ApplyPorts failed: VLAN drift must not
block edge / mode push."* So a VLAN change on an access port leads to a write of the
global mode, the bridge priority **and the 802.1s triplet** on the device. Nobody
asked for this write, nobody sees it go by, and no screen says it happened.

### Three demonstrations, all from the night of 2026-10-24 to 2026-10-25

1. **A site rename ejected seven Instant On switches from their region, with no
   symptom** (ADR-0021 and its addendum). Seven devices on `site-exec`, only one —
   ArubB — on `arena`, because only ArubB had been re-applied after the rename. The
   split held for weeks. The vector was not an STP gesture: it was a ports apply,
   which carried the region along with it.

2. **`core01-arena` is CIST root at priority 0 with ten boundary ports.** Making it
   join the region moves VLANs 10, 11, 42, 60, 98, 99 and 100 from the CIST to
   instance 1. Measure of the gesture, written in ADR-0022: topology recomputation on
   the site's core. A ports apply on this Arista would have been enough to trigger
   it, and nothing on the ports screen announces it.

3. **`sw01-arena` (FS S3400) declared `WritesExtra("stp")`, received a plan carrying
   a region, wrote its bridge and said nothing** — a successful `apply` on top of a
   switch outside its site's tree. That is what ADR-0022 closes on the *speech* side;
   this ADR closes the **gesture** that caused it.

### What the three have in common

In all three cases, the quantity written is **derived from site data** — the region
name comes from the plan or the UUID, the VLAN→instance table comes from the VLAN
list, the mode comes from the preference list. **Nobody ever named that particular
device.** A write nobody named on a quantity nobody saw: the two halves of the same
failure.

And the quantity written is precisely the one whose modification **recomputes a
topology**. The existing measurements: 2 to 5 s of unreachability and about 45 s of
recovery on the FS during a mode change (captured 2026-08-11, header of
`internal/driver/fs/s3400/stp.go`), the ports going back through listening/learning
at every move of an MST boundary.

## Decision

**No spanning-tree write travels on another gesture. The controller only writes
spanning-tree on a gesture that names it — and when the configuration carried
diverges from the one the site wants, a banner above the device states it and offers
"Conform".**

Four pieces.

### 1. Automatic apply only confirms what is identical

`applyDeviceSTP` — the callee of `runApply` — re-reads the tree **before** deciding,
compares, and writes **only if the device already carries the identity the site
wants**: same resolved mode, and same region triplet when the site has one. Any
divergence stops the write; nothing is sent, and a note names what diverges.

The re-read is done on the session already opened by `adminFor`, and its result is
persisted in `extra.stp_observed` — so the banner reads an observation the apply has
just refreshed, and not a snapshot from the day before.

What therefore remains automatic is a **re-confirmation of what is already in
place**, and it is verifiable rather than assumed: the re-emitted mode is the mode
running, the re-emitted region is the region carried, the MD5 digest does not move,
so no MST boundary moves. The four connectors that can write a region are already
idempotent on this path (`procurve` emits a differential, `cisco/ios` has an "already
as planned" path, `fs-s3400` compares before writing the mode, and `arista-eos` like
`instanton` rewrite the same value).

### 2. "First setting" and "conforming" cannot be told apart from the controller

The question deserved to be asked: a device just adopted has no tree to break, a
device in service does. **The distinction is real in the world and illusory in the
code.**

`core01-arena` is the proof. Its region is **empty** — exactly what a factory-fresh
device carries — and it is the most dangerous switch to write on the whole site: CIST
root at priority 0, ten boundary ports. In this product, adoption is a **discovery**
of devices already in service, not an installation: the ARENA fleet was discovered.
So "this device does not carry a region yet" does not mean "this device is new", and
no read can settle it.

There is therefore **no automatic first-setting case**. A device that does not
already carry the site's identity waits for the gesture, whether it is on a
workbench or at the heart of production.

### 3. `POST /api/devices/{id}/stp/apply` becomes the conforming gesture, without changing shape

It was already the only explicit write path, per device, with its mode named in the
body. It remains so, and it is what "Conform" calls. **We do not distinguish
"propose" and "set" with two routes**, because the proposal is already a **read**: the
triplet the site wants is `GET /api/sites/{siteID}/stp-mst-region` (ADR-0021), what
the device carries is `stp_observed`, and the comparison of the two is served on the
device view — in `deviceView.stp_region`, next to `stp` and `stp_caps`, without a
session on any device. A third route that would "propose" would be a third place
where the triplet is computed, and ADR-0021 paid to learn what two places of truth
cost on this quantity.

The confirmation is **in the interface**, not in the route: a `ConfirmDialog` that
states the price before calling. The route does not require a `confirmed` token — cf.
Consequences.

### 4. The banner states the three components, and offers the button only where it leads somewhere

Above the device, in the `DeviceBanners` stack, when the site wants a region (its
preference contains `mstp`) and the device does not demonstrably carry it:

- it names **the expected name against the carried name, the expected revision
  against the carried revision, and the expected VLAN→instance table against the
  carried one** — the three components of the 802.1s identity, never "the
  configuration differs". That is exactly what nobody saw;
- it marks which of the three diverges, because a single one is enough to split the
  region and the three are not repaired the same way;
- when the connector can join a region (`mdpv1.FeatMSTRegion`), it is in
  `tone="act"` and carries **"Conform"**, which asks for confirmation and states the
  price: topology recomputation, ports going back through listening/learning, 2 to
  5 s of unreachability and about 45 s of recovery;
- when the connector cannot (`fs-s3400`), it says so and **does not offer the
  button** — and it switches to `tone="info"`, because UI-GUIDE §2 reserves orange for
  what calls for a decision and an orange banner with no gesture for months is the
  definition of what teaches people to ignore orange.

**The comparison is state to state**, never configuration to state. Both sides are
structured values: the intent comes out of the database, the observation comes out of
`ReadSpanningTree`. Two normalisations are mandatory and are the reason the comparison
lives in the controller and not in the browser:

- **instance 0 is implicit and is not compared.** EOS announces `Instance 0  1-4094`,
  so does the FS; `mdpv1.MSTRegion.InstanceVLANs` never carries it. Comparing the raw
  tables would show a permanent drift across the whole fleet;
- **a region name equal to the device's own bridge address is not a name.** It is the
  factory default on every platform captured — the FS carried `649D9929F706`, its own
  MAC — and it means "this device is its own region", not "it is in region
  649D9929F706".

### 5. `FeatMSTRegion` is declared wherever it is true

ADR-0022 only declared the capability on `arista-eos` and only refused it on
`fs-s3400`. The three other connectors that emit the full triplet — `instanton`,
`hpe/procurve`, `cisco/ios` — did not declare it, by omission and not by decision.

Without this piece, the decision above would be inert: the banner would offer
"Conform" on no ARENA device except the Arista, that is to say not on the seven
Instant On switches that ADR-0021's addendum designates by name as the ones to put
back into the region. **A door that opens nowhere is not a door.**

The declaration promises nothing new — the capability means "this CODE emits a
region", and all three emit it: name, revision and `instance N vlan …`. On
`instanton` it is moreover proven on hardware: eight of ARENA's twelve Arubas carry
`mst-7f0d238df180`, revision 1, instance 1 = `10-11,42,60,98-100`, written by this
connector.

And today's database capture adds a fact ADR-0021's addendum could not see: **four
Instant On switches still diverge** — ArubE, ArubI and ArubM on `site-exec` with the
right table, and **ArubK** on `site-exec` with, in addition, the old table
`42,98-100`. These are exactly the devices the button must reach, and without the
declaration it would not reach them.

## Consequences

- **Per-port STP intents stop being automatic, and that is the main price.**
  `portfast`/edge, BPDU guard and path cost travel on the same `SpanningTreePlan` as
  the mode and the region (which is already what `stpPreferenceEmptyNote` states
  about the per-site switch). On a **conforming** device, they keep arriving, since
  piece 1 allows re-confirmation of what is identical. On a **diverging** device, they
  wait for the gesture, so an access port can remain without BPDU guard as long as
  nobody clicks.

  Accepted, and the trade-off is explicit: the alternative is letting the mode and
  the region go out to get the BPDU guard through, that is to say cutting a tree to
  protect a port. And the delay is **visible** — that is the banner's reason for
  being — whereas the silent write was not.

- **A banner that is ignored is a drift that lasts. That is the direct price of this
  choice and it is accepted.** Three provisions make it bearable without making it
  nagging, and none of the three is a reminder:

  1. **There is no "ignore" button.** Ever. A banner that can be hidden is a drift
     that will never be seen again, that is to say exactly the state before
     ADR-0021 with one more box ticked.
  2. **It only appears where there is something to decide**: only on devices that
     diverge, only when the site wants a region. On a healthy fleet the banner stack
     stays empty, so the banner's presence carries information.
  3. **It informs instead of demanding.** It names the three components and their
     gap. Reading it is enough to decide to wait — and waiting knowingly is a
     legitimate operational choice, of which ADR-0021's addendum gives the real case:
     "no STP apply on ArubL, ArubA, ArubC, ArubD, ArubF, ArubG, ArubH without having
     planned it".

- **The write route does not require a `confirmed` token.** The confirmation is in
  the interface. The reason is a trade-off and not a convenience:
  `POST /api/devices/{id}/stp/apply` is already called by the STP panel and by
  `cmd/mcp` (`tools_write.go`), and requiring a new field would break both of them
  for a guarantee the `ConfirmDialog` already provides. The accepted consequence is
  that a direct call to the API writes without confirmation — which is true of every
  write route in this product, and a direct call to a route named `stp/apply` is a
  gesture that names itself.

- **The guard covers neither the bridge priority nor the Super Root flag, and it is
  an accepted limit.** The principle is "no write nobody named", not "no write":
  these two are per-device intents — the flag stored on the device, or the site's
  root designation, which names devices one by one and already carries its own
  warning about contested roots and its order of application. They therefore keep
  travelling on the apply, **including the write of priority 0**, which moves a root.
  Someone who finds this inconsistent will have an argument: it is written here so
  that they find it written rather than have to discover it.

- **An apply costs one more read.** `applyDeviceSTP` re-reads the tree before
  deciding, on the session already open. It is one extra round trip per apply, and it
  is paid deliberately: deciding on the persisted observation would have allowed the
  precise case we are closing — a stale observation saying "conforming" above a
  device that has drifted since, hence a re-emission of the site's identity on a
  diverging device, hence the rename.

- **The banner depends on an observation, so it can be silent.** A device never read,
  or whose connector does not read the region, produces no verdict: the banner does
  not show, and its absence does not prove conformity. It is the flip side of the
  state-to-state rule, and it is preferable to the opposite: showing "drift" on an
  absence of reading teaches people to stop believing the banner.

- **Conforming remains dangerous, and the product does not make it safe — it makes it
  chosen.** "Conform" on one of the seven Instant On switches of `site-exec` renames
  it to `mst-7f0d238df180` and takes it out of the region of the six others. The
  banner shows the expected name against the carried name, so the information is
  there, but **the order of gestures on a split fleet is not a banner per device**: it
  is a campaign, and it is not in this ADR. The reverse convergence — pinning
  `site-exec` through `PUT /api/sites/{id}/plan` — remains the single-gesture path
  described by ADR-0021's addendum.

- **Declaring `FeatMSTRegion` on three more connectors changes the interface's gating
  on installations in service.** It is additive and triggers no write: the key
  decides no apply behaviour, only what a screen offers. On `hpe/procurve` and
  `cisco/ios` it rests on reading the code and not on an apply observed in the lab;
  that is the level of proof the key asks for — "does this code emit the triplet?" —
  but the inventory of regions actually set by these two ranges has never been
  captured.

## Rejected alternatives

**Automate nothing at all: remove `applyDeviceSTP` from `runApply`, full stop.** The
shortest and the easiest to defend in one sentence. Rejected because it drops per-port
STP intents — BPDU guard on access ports — across the **whole** fleet, including on
perfectly conforming devices where re-emission is demonstrably a non-event. We would
have paid everyone's protection for the risk of a few, and a BPDU guard that stops
arriving is also a silent degradation: it is the very shape of the failure this ADR
fights, turned around.

**Exclude only the region, and let the mode and the rest roll on.** Tempting: the
region is the quantity in all three demonstrations. Rejected on the measurement of
2026-08-11: on the FS, `spanning-tree mode <m>` **closes the SSH session before the
CLI answers** and leaves the tree unreadable for about forty seconds. The mode cuts at
least as much as the region, and a rule that protects one quantity out of two is a
rule that will be believed protective.

**A per-site flag "allow automatic STP writes".** Rejected because the switch already
exists and its history is instructive: an empty mode preference disarms the site's
whole STP apply (`stpPreferenceEmptyNote`), and that thirty-line comment exists
because nobody guessed the **extent** of what the switch cut off. A second switch, of
different granularity, on the same quantity, would contradict the first at the first
combination, and it is the operator who would discover which one wins.

**A dismissible banner, or one with a deferred reminder ("remind me in 30 days").**
The natural answer to "a banner that gets ignored". Rejected: ADR-0021 measured what an
invisible drift costs, and hiding is a return to invisibility with the illusion of a
trace. If someone wants to track gaps over time, that is a fleet inventory — a
sortable list of all the site's devices outside the region — not a checkbox on a
device. It would be a screen, and it would deserve its own decision.

**Do the comparison in the browser, from `GET /api/sites/{siteID}/stp-mst-region` and
`dev.stp.region`.** It is the shortest path to a banner: both pieces of data are
already served. Rejected on the two normalisations of piece 4. Excluding instance 0
and recognising a MAC-name as "no name" are 802.1s and platform rules, not
presentation; `IonRegionIsDefault` and `arista-eos`'s `Instance == 0` filter already
exist on the Go side. Transcribing them into TypeScript creates a second set of rules
that will drift, and the drift will show as a banner that is wrong — which one does
not forgive a banner whose whole purpose is to be believed.

**A per-device comparison route, `GET /api/devices/{id}/stp/region-conformity`.**
Honest, and it was the other serious option. Rejected because there is no
`GET /api/devices/{id}`: `withConformity` is the only place where a device record is
published (its own comment says so, and warns whoever adds the single-item GET).
Adding the route would force the banner to make a second request per device on a page
that already has the list, and would leave the list view — hence any future fleet
view — unable to say who diverges. The verdict travels on the payload that already
carries `stp`, `stp_caps` and `stp_super_root`, and it costs no session: the site plan
is read once per site, like the addressing plan and the VLANs next to it.

**Infer "conforming" from the absence of a `Skipped` line in the last apply.** The data
exists, in `last_apply`. Rejected because it answers another question: `Skipped` says
what the connector refused to write **the last time we wrote**, and the drift at stake
here arises precisely when we do not write — a VLAN added to the site moves the region
of every device without any apply taking place. A verdict based on the last apply
would be green on an entire fleet the site has just left behind.
