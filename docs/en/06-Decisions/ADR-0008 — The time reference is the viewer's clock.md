---
Status: Accepted
Date: 2026-10-24
Supersedes: ADR-0006
---

# ADR-0008 — The time reference is the viewer's clock

## Context

[[ADR-0006 — Freshness is measured on the server clock]] settled the right problem
with the wrong reference.

It correctly established that computing `Date.now() - last_seen` compares two
unrelated clocks, and that this is what displayed a healthy proxy as "silent for
6 min". It then decided to measure everything **on the controller's clock**.

Two defects appeared with use:

1. **Ages became right again, displayed times did not.** Fifteen places render a
   server timestamp as an absolute time — "10:48", the date of a credential
   rotation, the time of an event. They stayed in the controller's reference. An
   operator whose workstation is six minutes ahead reads "last event at 10:48" while
   their own watch says 10:54: the age is right and the time is not, which is perhaps
   worse than both being wrong together.
2. **The controller is no more reliable than the workstation.** ADR-0006 moved the
   dependency without removing it. The controller host drifts too — that is exactly
   what its own log reported about the proxy.

The product must depend on the accuracy of **neither** clock.

## Decision

**The viewer's clock is the reference.** Every timestamp coming from the server is
**translated into this reference** before being used, for an age as for a display.

The offset is still learned from the `Date` header of each response. It no longer
serves to say which clock is right — a question nothing allows us to answer — but
only to **cancel out the gap** between the two.

```
t_viewed = t_server − offset        (offset = t_server_now − t_client_now)
age      = Date.now() − t_viewed
```

The age becomes identical to ADR-0006's — it is the same subtraction — but the
displayed time now falls on the operator's watch. The two readings become consistent
with each other again, **without either clock needing to be right**.

**The disagreement banner is no longer on every page.** It is a system message: it
lives in the **configuration**. A clock gap no longer makes anything on screen
wrong — so there is no longer any reason to interrupt someone looking at a port
faceplate.

## Consequences

- Every time reading is now consistent **with the operator's watch**, which is the
  only landmark they can cross-check without opening anything.
- The displayed timestamps will not match those of the **server-side logs** when the
  clocks diverge. That is the price, and it is the right direction for the trade: the
  screen serves the operator, the log serves the investigation — and the banner, in
  the configuration, is where one learns that the two differ.
- There remains **one** input function (`noteServerDate`) and **two** output ones
  (`viewerTime`, `ageMs`). `serverNow()` disappears: it invited reasoning in the
  server's reference, which is no longer the reference.
- A workstation whose clock jumps during a session sees its times move all at once.
  Correct: they follow its reference, and it is the reference that moved.
- `Date.now()` remains right — and mandatory — for everything local: timers,
  animations, serial procedure deadlines.

## Rejected alternatives

**Stay on ADR-0006** and also translate absolute displays into the controller's
reference. Consistent, and it leaves the screen displaying times that match no clock
the operator can consult.

**Display everything in UTC.** Removes the ambiguity and the readability with it:
nobody reads a port faceplate in UTC.

**Display both times.** Would double every timestamp of the interface for an abnormal
situation.

**Fix nothing and repair NTP.** Already rejected in ADR-0006, for the same reason:
meshcraft is consulted from workstations it does not administer.

## Addendum of 2026-10-24 — the measurement, after the fact

Point 2 of the context assumed the controller host drifted. It has since been
measured, and it is worse than assumed:

```
timedatectl    System clock synchronized: no
               NTP service: n/a          (no daemon installed)
measured gap   −6 min 06 s from real time
RTC            6 min AHEAD of the system clock
```

The direction of the RTC/system gap deserves to be read in the right order: the RTC
was **~2 s from true time**. So it was not the one that had drifted, it was the
system clock that had moved away from it — 46.77 ppm slow, i.e. 4.04 s per day over 90
days of uptime, which accounts for the 366 s to the second. A reboot would have
*corrected* the time, not the opposite.

The host **was six minutes slow and had no synchronisation at all**. The six minutes
of the founding anecdote — "silent for 6 min" — therefore came neither from the
operator's workstation nor from the proxy: **they came from here**. The controller log
announcing the proxy "4 m 15 ahead" was in fact describing its own lag, seen in
reverse. The operator's workstation had been right from the start.

What it does not change: **the decision**. A correct server clock would have made none
of the fifteen absolute displays correct, since they would have stayed in a reference
the operator cannot cross-check.

What it confirms is the rejection above: NTP was deployed on this host the same day,
and that deployment **does not replace** the reference translation. It removes one
cause of drift, on the only machine we administer — the workstations that consult
remain out of reach, and they are still what the operator looks at.

The lesson of method: three clocks that **agree with each other** are not three
correct clocks. The controller, the database and the containers agreed perfectly —
they all read the same kernel clock. What was missing was an *external* reference, and
it cost only one request:

```bash
docker run --rm --network host curlimages/curl:latest -sI https://www.cloudflare.com | grep -i '^date:'
```
