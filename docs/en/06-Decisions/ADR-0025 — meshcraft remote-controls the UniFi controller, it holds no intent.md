---
Status: Refined by ADR-0029
Date: 2026-10-25
---

> **One sentence of point 2 has been reopened.** "No imported setting field
> becomes editable" is no longer accurate, and the ruled-out alternative "an
> explicit middle ground, field by field" was taken up through the very door it
> named itself: [[ADR-0029 — The SSID is editable, the access point is not, and only what reads back is offered]] makes **SSID settings** editable. Everything
> else in this document is authoritative and is the foundation of ADR-0029 — in
> particular §3 (no stored intent, hence no converger), §4 (a single write door,
> closed at compile time) and §6 (`rc:ok` is not proof). **Per-access-point**
> settings remain non-editable, and ADR-0029 says why that is now a measurement
> and not a choice.

# ADR-0025 — meshcraft remote-controls the UniFi controller, it holds no intent

## Context

[[ADR-0015 — The site extension lives in the controller, and its inventory is a read-only mirror]] §5 forbids three things by name: "no adoption, no
configuration change, no access point restart". The `internal/unifi` package has
no write method, and its header says why: that is where the promise is kept, not
in a comment.

The Wi-Fi screen displays this promise in so many words. An operator read it and
replied: "I'd like meshcraft to be able to do the basic actions: adopt, remove,
reconfigure".

**ADR-0015's objection does not cover this request.** It targeted *two write
authorities on the same device* — the symptom being not an error but a
configuration that comes back on its own, minutes later, without a trace. That
argument holds for a write **directly to the access point**, over SSH or SNMP,
behind the controller's back. It does not hold if meshcraft goes through the
**UniFi controller's API**: it is then a **client** of UniFi, just like UniFi's
own web interface, and there is still only one master.

There remains the question ADR-0015 did not have to ask: **who holds the
intent?** "Reconfigure" assumes knowing what the configuration *should* be. Yet
ADR-0015 established that the imported inventory is a **mirror**: meshcraft
reflects, it does not describe.

### What the controller really offers for writing

Observed on the production controller, application `10.6.109`, before deciding.
It is the fact that settles it, and it was not guessable:

- **The v1 integration API has only one action**, and it lists it itself when
  asked for another: `POST /sites/{id}/devices/{id}/actions` answers
  `Invalid $.action value (valid values: 'RESTART')`. The only gesture under
  contract is therefore restart — precisely the one nobody asked for.
- **`PATCH` and `PUT` on the device resource answer `405`.** The integration API
  offers **no** configuration write: no renaming, no channel, nothing.
- **`DELETE /sites/{id}/devices/{id}` answers `200` and removes the access point
  from the site.** This verb is documented nowhere, appears in no enumeration,
  asks for no confirmation and has **no inert form**: the identifier *is* the
  whole payload, so the request that would serve to check the route exists is
  already the request that destroys. Observed in production on an access point
  in service, which had to be re-adopted and renamed by hand — seven minutes of
  unavailability. The fact is recorded here because it is the reason for point 5.
- **Adopting and reprovisioning only exist on the legacy API**,
  `POST /proxy/network/api/s/{ref}/cmd/devmgr`, the one ADR-0015 had already
  isolated for SSIDs by declaring it "under no contract". It accepts the same
  `X-API-KEY` header, without an anti-CSRF token. Recognised commands,
  established by aiming them at a MAC that does not exist: `adopt`,
  `force-provision`, `restart`, `set-locate`, `unset-locate`, `upgrade`.
- **This API fails open.** `{"cmd":"an-invented-command"}` answers
  `{"meta":{"rc":"ok"}}`, and `delete-device` on a non-existent MAC answers
  `rc:ok` too. So `rc:ok` **is not proof** that a gesture took place: a typo in a
  command name succeeds silently and does nothing.
- **The token's permissions can be read.** `GET /proxy/network/api/self` returns
  the record of the administrator behind the key, with `is_super`, `is_owner`
  and `super_site_permissions`. A read-only token is therefore discernible
  **before** using it, and not at the worst moment.

## Decision

**meshcraft remote-controls the UniFi controller: it triggers gestures and holds
no intent. The mirror remains a mirror.**

1. **Every gesture goes to the controller's API, never to the device.** This is
   the structuring rule, and it is what makes the thing compatible with
   ADR-0015: meshcraft becomes a client of UniFi, not a second master. A session
   opened on an access point — SSH, SNMP, HTTP — is outside this decision and
   remains forbidden.

2. **No intent is stored, and "reconfigure" comes down to `force-provision`.**
   This is the heart of the matter. meshcraft never says what an access point's
   configuration *should* be; it asks the controller to **reassert its own**. The
   gesture is therefore exactly a remote control: the intent stays with UniFi,
   which is its authority, and meshcraft has no column that could contradict it.
   No imported setting field becomes editable.

3. **Being a client only saves the gestures, not the intent.** This is the
   counter-intuitive point, and it is for it that this ADR exists. Going through
   UniFi's API removes the second *write authority*; it would not remove the
   second *decision authority*. If meshcraft remembered "the channel must be 36"
   and pushed it, and someone set 44 in the UniFi interface, there would be two
   deciders even with a single writer — and the configuration would come back on
   its own, through meshcraft instead of through the controller. The symptom
   ADR-0015 refused would be strictly the same. The client/master distinction is
   therefore not a pass to convergence: it allows imperatives, not a desired
   state.

4. **A single write door, and its argument is unfalsifiable.** ADR-0015's
   guarantee — "this package has no write method, a reviewer checks it by reading
   the list of methods" — is replaced by a guarantee of the same nature, upheld
   by the type and not by a comment: `Client` has only one method that writes,
   and it takes a `Gesture` whose field is unexported and has no literal form. No
   caller outside the package can fabricate one; the set of writes meshcraft can
   request is therefore **closed at compile time**, and is counted by counting
   the three `var`s that declare them. The zero `Gesture` is unusable, so the
   door refuses by default.

5. **Forgetting is guarded by the exact name, reread at UniFi.** It is the idiom
   of [[ADR-0012 — The tool surface is not the API surface]] and of
   `delete_object`: a value the caller could only obtain **by reading the object
   it is about to destroy**, compared with what the authority holds at the
   instant of the call. And the name is reread **at UniFi, not in the mirror**:
   confirming against a copy that may be hours old is a door that opens onto an
   object that is no longer the one that was read. Given what `DELETE` does
   without warning, it is the only confirmation that holds.

6. **`rc:ok` is not proof: every gesture is verified by a read-back.** Since the
   legacy API answers `ok` to a command it does not know, a gesture is only
   deemed done when a read confirms it — the access point is present and adopted
   after `adopt`, absent after forgetting. An unconfirmed gesture is reported as
   unconfirmed, never as successful.

7. **The token's permissions are read before being exercised, and the product
   says so.** The extension exposes what the token really allows, read from
   `self`. A read-only token gives a screen that explains it cannot write; it
   does not give three buttons that will fail at the moment they are needed.

8. **Forgetting goes through the integration API, adopting and reprovisioning
   through the legacy API** — for lack of anything better, and it is written down
   so that nobody believes the three gestures have the same solidity. The
   integration `DELETE` answers with a real status code, where `delete-device`
   fails open: for an equal gesture, we take the route that can say no.

## Consequences

- **The screen can no longer say "meshcraft modifies nothing". That sentence
  becomes false** and changes in the same commit, en+fr. It is replaced by the
  real distinction, which is harder to phrase and must be phrased anyway:
  meshcraft triggers gestures at the controller, and decides no setting.
- **Changing a setting remains a visit to the UniFi interface.** That is the main
  price and it is permanent. Someone will come asking why "reconfigure" does not
  offer to choose a channel or an SSID: the answer is point 2, and the way out is
  ADR-0015's — unadopt the access point from UniFi, and it becomes an ordinary
  device with a connector again.
- **Two gestures out of three rely on an API UniFi does not promise.** A
  controller update can break them without notice. They are isolated behind
  named functions, as SSIDs already are, and their failure is a gesture failure —
  never a page failure.
- **The "this package does not write" guarantee is permanently lost**, and
  replaced by "this package writes three things, named here". It is a real
  weakening: one can no longer conclude harmlessness by reading an empty list of
  methods. The counterpart is that the set remains enumerable and that extending
  it is a visible gesture in review.
- **A gesture makes the mirror stale the instant it succeeds.** Each gesture
  therefore triggers a re-import, and the screen must cope with seeing an access
  point disappear from the list — that is the normal case after forgetting, not a
  read error.
- **We forbid ourselves from adding a fourth gesture without saying so.**
  `restart`, `upgrade` and `set-locate` are recognised by the controller and
  deliberately absent: restarting is what ADR-0015 named, and nobody asked for it.
- The site log receives one entry per gesture, with the author. Forgetting is a
  `warning`: it is the only one of the three that cannot be repaired without a
  re-adoption, and re-adoption restores neither the name nor the access point's
  own settings.

## Alternatives ruled out

**meshcraft holds the intent for a named subset of settings, and converges as for
the other devices** (option 2 of the request). It is the powerful option, and the
one that would make the UniFi inventory homogeneous with the rest of the product.
Ruled out for two reasons, one of which is factual and closes the debate: the
integration API **offers no configuration write** (`405` on `PATCH` and `PUT`), so
there is nowhere under contract to push an intent — convergence would have to be
built on the legacy API, the one that answers `rc:ok` to an invented command. We
would have built the product's most demanding function on its least reliable
foundation. The second reason is point 3: double decision brings back exactly the
symptom ADR-0015 refused, and one would then have to answer "who wins?", which
neither of the two possible answers allows one to do honestly — "UniFi wins" makes
the intent decorative, "meshcraft wins" makes the UniFi interface a trap for
whoever uses it.

**An explicit middle ground, field by field** (option 3). Attractive because it
would allow starting small — the access point's name, for example, which is
harmless. Ruled out because the boundary would be readable nowhere: a screen where
two fields out of fifteen are editable does not teach the rule, it teaches that
one has to try to find out. And the first field added opens the discussion on the
second, without any criterion saying where to stop. If a precise need arrives — a
single setting, named, with its reason — it is an ADR to write at that time,
against that need.

**Writing directly to the access point, over SSH or SNMP**, since meshcraft can
already talk to Ubiquiti hardware and an EdgeSwitch connector exists. Ruled out in
point 1, and it is the only case where ADR-0015's objection applies as is: the
access point would be reconfigured behind its controller's back, which would
re-provision it at its own pace, erasing the gesture. The symptom would not be an
error but a drift, diagnosed weeks later.

**Forgetting with `delete-device` on the legacy API**, for consistency with the
two other gestures that have no choice. Ruled out in point 8: that command answers
`rc:ok` on an access point that does not exist, so it cannot distinguish
"forgotten" from "never seen", and that is exactly the distinction that matters
for an irreversible gesture.

**Keeping confirmation to a checkbox or a second click**, as `power_cycle_ports`
does with `confirm=true`. Ruled out in point 5: a boolean is a value the caller
produces without having read anything, so it does not protect against the error
that matters — getting the object wrong. `power_cycle_ports` can make do with it
because its gesture is repaired by waiting for a restart; forgetting cannot be
repaired.

**A `can_write` boolean set in the extension's configuration**, declared by the
operator when pasting the token. Ruled out: it is a declaration, not a
measurement, and it would be false as soon as the token is replaced by another.
Permissions can be read from `self`, so they are read — an invariant upheld by a
read is worth more than an invariant upheld by a box ticked six months ago.

**A global confirmation flag "I allow meshcraft to write to UniFi"**, armed once in
the site settings. Ruled out because a switch armed once is armed forever: it
protects the first gesture and none of the following, whereas it is the hundredth
that is done absent-mindedly.
