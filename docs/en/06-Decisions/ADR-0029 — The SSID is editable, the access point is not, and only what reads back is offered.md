---
Status: Accepted
Date: 2026-10-25
---

# ADR-0029 — The SSID is editable, the access point is not, and only what reads back is offered

## Context

The request is to **view and modify the settings of Wi-Fi access points and
SSIDs** from meshcraft, through the UniFi controller.

[[ADR-0025 — meshcraft remote-controls the UniFi controller, it holds no intent]] §2 says the opposite in one sentence: "No imported setting field
becomes editable". And its rejected alternative no. 2, "an explicit in-between,
field by field", refuses precisely what is asked for here — while leaving a door
open, named: "if a precise need arrives — a single setting, named, with its
reason — it is an ADR to be written at that moment, against that need". The
present document goes through that door. It reopens nothing else of ADR-0025:
the eight points on gestures, the single write door, the mandatory read-back and
the absence of stored intent are the foundation on which it rests.

### What was measured, and what could not be

Captured **read-only** on the production controller, application `10.6.109`, on
a day when four AC Pro access points were serving players. No write was issued:
that same morning, an access point dropped off the network for eleven minutes
because a capture was looking for which HTTP verbs the API accepted, and its
per-AP settings were lost. The ban on writing is therefore a constraint of this
capture, not modesty — and it leaves unknowns, listed below, which are the price
of not having done it again.

- **`GET /proxy/network/api/s/{ref}/rest/wlanconf` returns 67 fields per SSID**,
  and **`GET …/rest/wlanconf/{id}` returns the same object individually**. That is
  the fact that makes this work possible: an SSID setting that has been set can be
  **read back individually** and compared field by field.
- **`GET …/rest/wlanconf/{nonexistent-id}` answers `rc:ok` with `data: []`.** The
  legacy API's failure-to-success therefore does not spare reads either. A
  read-back that does not check that `data` is non-empty confirms the existence of
  an object that does not exist.
- **`GET …/rest/device` and `GET …/rest/device/{id}` answer `404
  api.err.NotFound`.** The per-device REST resource — the one through which
  historical UniFi controllers accepted a `PUT` of settings — **does not exist on
  this application**. There is therefore, on this controller, no known route to a
  per-AP setting.
- **An access point's configuration can only be read through stats**:
  `stat/device`, `stat/device/{mac}`, `v2/api/site/{ref}/device`. These are
  statistics, not resources, and above all **they omit fields left at their
  default** — `led_override` is simply absent from an AP that never touched it.
  So even if a write route appeared, the absence of a field on read-back could not
  be told apart from "not applied".
- **`x_passphrase` comes back in cleartext**, both in `rest/wlanconf` and in
  `v2/api/site/{ref}/wlan/enriched-configuration`. A production network's
  passphrase is therefore readable by anyone holding the token.
- **The choices of a reference field are readable**: `GET …/rest/networkconf`
  gives the networks (and their VLAN) that `networkconf_id` designates, `GET
  /proxy/network/v2/api/site/{ref}/apgroups` gives the AP groups.
- **`PUT` on `rest/wlanconf/{id}` has not been verified**, and could not be:
  verifying it is a write. Its existence is inferred from the shape of the
  resource — collection and element on `GET`, unknown identifier returned as an
  empty set — which is that of UniFi's generic REST handler. It is an inference,
  it is written as such, and the design below is made so that it can be wrong
  without damage.

## Decision

**meshcraft is an SSID editor. It does not edit access points. A setting that
does not read back is not offered.**

1. **Editor, not converger, and that is what makes it compatible with
   ADR-0025 §3.** meshcraft shows what the controller holds, lets it be
   modified, pushes, reads back — and **keeps nothing between two edits**. No
   desired-state column, no drift detection, no convergence loop. An edit's
   "intent" lives for the duration of an HTTP request and dies with it. There
   are therefore never two deciders: if someone changes the same SSID in the
   UniFi interface, meshcraft has nothing to reassert, and the symptom ADR-0015
   refused — a configuration that comes back by itself — cannot occur.

2. **The boundary is a class of object, not a list of fields.** This is the
   answer to ADR-0025's objection against the "field-by-field in-between", and it
   is the deciding point. What was held against the in-between was that "two
   editable fields out of fifteen does not teach the rule, it teaches that you
   have to try to find out". Here the rule fits in one sentence readable on
   screen: **the SSID is editable, the access point is not**. It is not a matter
   of taste — it is the measurement: the SSID has a REST resource that reads back
   individually, the access point has none.

3. **What reads back is deliverable; what does not read back is not offered.**
   That is the cut, and it replaces "all possible settings" with "all those we can
   set **and** verify". A field only enters the screen if
   `GET …/rest/wlanconf/{id}` returns it, hence if a read-back can compare it to
   what was sent. The excluded fields are excluded by name, with their reason, in
   the Consequences — not by omission.

4. **`rc:ok` is never the judge: the read-back is.** ADR-0025 §6 laid down the
   rule for gestures; here it is the whole mechanism. An edit is "confirmed" when,
   and only when, a read-back of the object shows **non-empty `data`** and every
   modified field equal to what was requested. Otherwise it is reported as
   accepted-not-confirmed, exactly like a gesture.

5. **Read-merge-write the whole object, never a partial body.** This is the
   arbitration between two bad options and the price must stay written. UniFi's
   legacy REST handler has historically wanted the complete object, and a partial
   body has erased fields there. Erasing a field we did not want to touch is
   precisely the disaster this file has already suffered — per-AP settings lost
   one morning — and precisely the danger for the passphrase. So: we re-read the
   complete object, apply only the changed keys to it, push the whole thing back.
   Everything we did not touch goes back **bit for bit** as the controller gave
   it. The price: a field the controller computes and refuses as input would make
   **every** edit fail. That failure is loud and immediate, hence acceptable; the
   reverse — a partial `PUT` that goes through and blanks three fields — is silent
   and discovered at the next LAN.

6. **The guard is a precondition on the replaced values, not a typed name.** The
   client sends, for each field it changes, **what it saw** (`was`) and **what it
   wants** (`want`). The server re-reads from UniFi, at the moment of writing, and
   **refuses the whole set** if a single one of the targeted fields no longer
   carries the value seen. Nothing is sent in that case. It is ADR-0025 §5's idiom
   moved from the exact name to the replaced values, and it is strictly stronger
   for an edit: the typed name only protects against picking the wrong object, the
   precondition also protects against the concurrent editor — the one who changed
   this same SSID in the UniFi interface while the form was being filled in. It
   costs no friction: the screen has already read the values, it does not have to
   have them retyped.

7. **The passphrase is written, not read, and not erased by distraction.** Three
   properties decide it, and none can be omitted:
   - the controller returns it **in cleartext**, so it is *re-readable*, so a
     passphrase write is **confirmable**;
   - [[ADR-0015 — The site extension lives in the controller, and its inventory is a read-only mirror]] forbids importing it, so it is **never
     stored** and **never sent to the browser**;
   - the merge of point 5 sends it back as is, so an edit that does not touch it
     **cannot blank it**.

   The field is therefore **write-only**: empty means "unchanged", and the screen
   says so **in words**, never with dots that would look like a value is present.
   Non-empty means "replace". The confirmation compares the read-back with what was
   sent **on the server side**, and reports only "matching" or "not matching": the
   value never crosses back to the browser and enters no log line. **There is no
   "reveal current passphrase" button** — it would be an import under another name,
   through the only surface of the product that has no vault.

8. **The write door remains that of ADR-0025 §4, widened by one gesture and not a
   millimetre more.** `Client.Do` remains the only writing method, its argument
   remains a `Gesture` that cannot be constructed outside the package, and the set
   of writes remains **closed at compile time** — it is now counted by counting
   **four** `var`s. The reflective test that enumerates the exported surface is
   updated in the same diff, so a fifth write remains an act visible in review.
   Editing does **not** have its own route to the controller: it goes through the
   same door, with the same mandatory read-back.

9. **The security mode is not offered, and it is not an oversight.** It is
   re-readable, though, so point 3 does not exclude it. It is excluded because the
   consistent combination of keys WPA3 requires — `security`, `wpa_mode`,
   `wpa_enc`, `wpa3_support`, `wpa3_transition`, `pmf_mode`, `sae_psk` — cannot be
   **enumerated** by reading a controller that uses only one of them
   (`wpapsk` / `wpa2` / `ccmp`). Setting a guessed combination on an SSID in
   service is exactly the gesture that takes a venue off the air. The real need
   behind "change the security" — rotating the passphrase — is served by point 7,
   at constant security mode.

10. **The authority banner disappears; the two useful facts it carried move down
    to where they serve.** The banner "The controller is the configuration
    authority" explained an architecture to someone who came to administer their
    Wi-Fi, and its second sentence — "meshcraft stores none of it and modifies
    none of it" — becomes **false** at point 1: meshcraft will indeed modify,
    through the controller. It is removed, i18n keys included, in every locale
    where they exist. But it carried two facts that still matter to the operator,
    and each **survives where the question arises** rather than at the top of the
    page:
    - **meshcraft holds nothing, so it monitors nothing.** A setting changed
      elsewhere does not "drift" here: it is what it is at the last capture. This
      fact is stated **in the edit form**, at the moment someone is about to set a
      value — it is there, and not at the top of a list, that one might believe
      meshcraft remembers what was just typed.
    - **the mirror's freshness.** What is listed dates from the last import. This
      fact is stated **on the SSID card**, next to the list it qualifies. It
      becomes more necessary with editing, not less — and it has an exact
      counterpart: the **form**, for its part, does not read the mirror. It reads
      the controller on opening (point 6 requires it, otherwise the precondition
      would apply to an hour-old truth). So the list is dated and the form is
      fresh, and it is this asymmetry the screen must make readable instead of a
      general sentence about authority.

    The general rule behind this removal: **an administration page is not the
    place to teach the data model.** A fact about authority only deserves the
    screen where it changes the next gesture of whoever reads it.

11. **Import becomes a 30 s background loop, and the absence of overlap is
    structural, not guarded.** The Wi-Fi screen's "Import now" button disappears:
    refreshing a list is not an operator decision. The requested cadence is 30 s
    and the ceiling of a complete pass is 90 s, so three passes could overlap — and
    it is on the day the controller is slow, that is to say the day it matters,
    that it would show. The guard is **not** a lock:
    - **a single goroutine**, which goes through *all* paired extensions of *all*
      sites **sequentially**. There is therefore no second runner to exclude. One
      loop per extension would have required a lock per extension, and above all
      would have sent N requests at the same instant to N controllers at first
      startup;
    - **the delay runs from the END of the previous pass.** So a `sleep` after the
      pass, never a `Ticker`: a ticker fires on the wall clock and its ticks pile
      up behind a slow pass, which is exactly the defect to avoid. The controller
      sees at most one pass at a time, however slow it is.

    One concurrent reader remains, named rather than hidden: the re-import that
    follows a gesture (ADR-0025, Consequences). It is triggered by a human, hence
    bounded by them, and its write to the mirror is atomic — at worst one pass
    loses to the other and the next one corrects it thirty seconds later.

12. **A controller that does not answer is not hammered, and the product says
    so.** Progressive backoff per extension — one minute, doubled on each failure,
    capped — and the extension in backoff is simply **skipped** by the pass. Two
    intended consequences:
    - **fixing the extension cancels the backoff.** That is what makes the button
      unnecessary rather than missed: after pasting a fresh token, the next pass is
      less than thirty seconds away, not a quarter of an hour. A manual "just in
      case" gesture next to a loop would be two mechanisms for one question.
    - **the button on the extensions configuration page stays**, and it is not an
      inconsistency: it answers "is my token right?" at the moment it is typed,
      which a loop cannot do *there, right now*, next to the field. One mechanism
      per question: the loop maintains a list, this button validates an input.

13. **The loop keeps quiet, and that is the most important rule of this point.**
    A pass every 30 s that wrote a log line would make 2,880 lines per day per
    site, and would drown the log that humans read. So the log only receives
    **transitions**:
    - `extension.imported` **only if the pass changed something** (rows removed, or
      counts that moved since the last successful pass). A pass that confirms the
      state writes nothing;
    - `extension.unreachable` (warning) **once**, on going from "it worked" to "it
      no longer works", stating the backoff;
    - `extension.recovered` once, on return;
    - `extension.orphaned` as today: it is already a transition.

    **And the log is the input of alerting** ([[ADR-0024 — An alert is earned - the log decides, the description and the interconnect qualify]]), so it
    had to be checked that a product does not alarm itself over what it has just
    read. Checked: `alertableTypes` is an **exhaustive allow-list of six types**,
    and no `extension.*` is in it. Alerting is therefore structurally deaf to
    synchronisation, by ADR-0024's design and not by luck. A test now asserts it,
    so that adding an `extension.*` to that list is an act that must answer the
    question instead of discovering it.

14. **Freshness, now implicit, must become visible.** With a button, the operator
    knew when they had imported; with a loop they no longer know anything, unless
    the screen says so. It is the same need as point 10 and it gets worse here. The
    screen therefore shows **the time of the last successful capture**
    (`last_sync_at`, on the viewer's clock — [[ADR-0008 — The time reference is the viewer's clock]]) and, where relevant, **the fact that a
    failure is lasting**: a capture that no longer goes through is not an empty
    screen, it is a dated screen with a reason next to it.

## Consequences

- **The Wi-Fi screen stops saying there are no settings anywhere.** ADR-0025's
  sentence — "there are no settings on this screen because there are no settings
  anywhere" — becomes false and changes in the same commit, en+fr. The distinction
  that replaces it is that of point 2, and it must be stated in so many words: this
  site's SSIDs are edited here, the access points are not, and the reason is that
  the controller offers no per-AP resource.
- **The authority banner is removed, and with it two i18n keys in all eleven
  locales.** Point 10 says where the two facts it carried go. What is lost for
  good, and accepted: the top-of-page explanation of *why* the controller decides.
  Someone will arrive wondering why access points cannot be edited — the answer is
  next to the list of access points, in one sentence, and its development is here.
- **The SSID settings built** are those the read-back can verify: broadcast
  enabled, name, hidden SSID, bands, network (hence VLAN), guest network, client
  isolation, fast roaming, BSS transition, UAPSD power saving, multicast
  optimisation, proxy ARP — and the passphrase under the regime of point 7.
- **The excluded settings, and why**:
  - *all per-AP settings* (name, channel, width, power, LED): `rest/device` answers
    404, no write route reachable, and the stats omit defaults so absence is not
    proof;
  - *the security mode and the WPA3 family*: point 9;
  - *`mac_filter_*`, `private_preshared_keys`, `schedule*`, `radius_*`,
    `hotspot2conf`*: lists and structured objects. The read-back comparison can be
    defined for them, but their invariants cannot be read on a controller that does
    not use them — and private pre-shared keys carry passphrases, which ADR-0015
    forbids importing;
  - *`minrate_*`, `dtim_*`, `group_rekey`, `pmf_mode`*: re-readable, hence
    admissible under point 3, but their safe ranges depend on the hardware and
    nobody asked for them. They are the next natural candidates, and adding them
    will not require an ADR: point 3 already covers them;
  - *creating and deleting an SSID*: `POST` and `DELETE` on `wlanconf` are not
    verified, and a deletion has no inert form — that is ADR-0025's lesson on the
    integration `DELETE`, and it holds here without having paid for it twice.
- **What ships unvalidated on hardware, and it is accepted.** `PUT` on
  `rest/wlanconf/{id}` has never been issued to a controller. The two outcomes of a
  wrong inference are held by the design: a refused verb becomes a loud refusal
  (`405` → gesture refused), a verb accepted without effect becomes a **not
  confirmed** edit. In both cases nothing is announced as set that is not. What
  remains unknown is whether the controller **accepts the whole object** as input:
  if it refuses a field it computes, every edit fails at the first attempt,
  visibly, and the list of fields to remove from the body will be readable in its
  message.
- **A `PUT` makes the mirror stale the moment it succeeds**, like a gesture. It
  therefore triggers the same re-import, and the screen must cope with seeing an
  SSID change name underneath it.
- **A production network's passphrase is readable by anyone holding the token**,
  and this fact does not depend on us: it depends on UniFi. Noting it here is the
  only thing this document can do about it, and it is an argument for treating an
  extension token as a first-rank secret — which `site_extensions.token_enc` and
  `SealedColumns()` already do.
- **We forbid ourselves from adding a per-AP setting without a new ADR**, even on
  the day a UniFi update makes `rest/device` reappear. Point 2 would then be
  reopened, and the boundary "the SSID is editable, the access point is not" would
  stop being a teachable sentence: it is exactly the kind of decision that must be
  revisited explicitly rather than allowed to erode under the good news of a route
  that works again.

## Rejected alternatives

**Hold an intent in the database for SSID settings, and converge** — the option
"settings" naturally suggests, and the one that would make the UniFi inventory
consistent with the rest of the product. Rejected for the reason of point 1, which
is that of ADR-0025 §3 and has not moved: the problem is not who writes, it is who
decides. Two deciders on the same SSID force an answer to "who wins?", and both
answers are bad — "UniFi wins" makes the intent decorative, "meshcraft wins" makes
the UniFi interface a trap for whoever uses it. And convergence would be built on
the API that answers `rc:ok` to an invented command: the product's most demanding
function on its least reliable foundation.

**Edit access points anyway, accepting that we cannot confirm.** That is the
literal reading of the request, and it has an honest defender: better a channel we
may be setting than no channel at all. Rejected because on this API, "maybe" is
indistinguishable from "no": `rc:ok` proves nothing, the stats omit defaults, and
no per-AP write route is even reachable. We would ship a screen where every save
would show "accepted, not confirmed" — which does not teach caution but teaches
people to ignore the warning, and sets up the time it mattered.

**A partial `PUT`, limited to the changed keys.** Simpler, smaller on the wire, and
what one would write spontaneously. Rejected at point 5: if the legacy handler
interprets a partial body as the whole object, the sixty absent fields are reset to
their default, passphrase included. The cost of being wrong is not symmetrical, and
the capture could not settle it since settling it was a write.

**Show the current passphrase, since the controller gives it in cleartext.**
Tempting: it would be useful, and the argument "it is already readable with the
token" is not wrong. Rejected at point 7. ADR-0015 does not only forbid storing the
passphrase, it forbids importing it; sending it across to a browser means putting it
in an HTTP cache, a developer-tools history and a support screenshot, that is to say
in three places that have no vault. And the need behind the request is to **change
it**, not to read it.

**A password field pre-filled with a masked value**, as many forms do. Rejected, and
it is the trap named in the request: a field that shows dots looks like a value is
present, so a distracted save sends it back — or sends the dots themselves back.
Empty plus a sentence that says "leave empty to keep unchanged" is less elegant and
unambiguous.

**Keep ADR-0025 §5's guard as is: have the SSID name retyped before every save.**
Consistent with forgetting an access point, and already implemented. Rejected at
point 6: an edit is reversible where a forget is not, so the friction is not worth
the same price; and above all the typed name does not see the concurrent editor,
whereas the precondition on replaced values does. The friction would have cost more
and protected less.

**A "one pass at a time" lock per extension, with a 30 s `Ticker`.** The form one
writes spontaneously, and it works. Rejected at point 11 because it answers the wrong
question: with a ticker, ticks keep arriving during a slow pass and the lock makes
them *drop* — so behaviour under load depends on a detail of silent rejection. A
single goroutine that sleeps after finishing has no ticks to reject, and its
behaviour under load is the same as when idle: slower, and that is all.

**One loop per extension**, which would allow different cadences per controller.
Rejected at point 11: at startup, N loops leave at the same instant towards N
controllers, and the cost that matters is not meshcraft's but that of the remote
controller and its token. The day an extension deserves its own cadence, it will be a
per-extension cadence within the single pass, not one more goroutine.

**Keep the "Import now" button next to the loop**, "just in case". Rejected at point
12: two mechanisms for one question, and the only legitimate reason to force a pass —
a token just fixed while the backoff is at its ceiling — is removed at its source by
cancelling the backoff when the extension is modified. A button whose reason for
being has disappeared ends up being clicked for something else.

**Emit `extension.imported` on every pass and let the log filter.** Rejected at
point 13: the log is not just a file one greps, it is the source of alerting
(ADR-0024) and the page someone opens to understand an incident. 2,880 lines per day
per site do not make synchronisation more traceable there, they make everything else
unreadable — and the traceability one thinks one gains is that of an event that, by
construction, says nothing.

**A second writing method on `Client`, next to `Do`**, since editing an SSID does not
look like a gesture on an access point. Rejected at point 8: ADR-0025 §4's guarantee
is that the set of writes can be counted by reading a list. A second door, even well
commented, replaces that with "you have to look", and that is exactly what ADR-0025
refused to leave to a comment.
