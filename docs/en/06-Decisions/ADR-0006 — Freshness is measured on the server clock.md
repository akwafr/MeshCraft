---
Status: Superseded by ADR-0008
Date: 2026-10-24
---

# ADR-0006 — Freshness is measured on the server clock, not on the browser's

## Context

An operator reported proxies displayed as "silent for 6 min". The
measurement, taken at the time of the report:

- `last_seen` of both proxies: **6 seconds**.
- Host, controller and database clocks: **in agreement**.
- Proxy heartbeat: 30 s, tunnel yamux keepalive: 20 s. Nothing abnormal.
- And in the controller log, on every trap received from `proxy-lab`:

  > `the proxy timestamp is AHEAD by 4m15.71s compared to the controller — no
  > transport delay can explain it: this proxy's clock and the controller's
  > diverge`

The proxy was fine. What was wrong was **the clock of the machine
doing the looking**.

The interface computes freshness everywhere like this:

```ts
const age = Date.now() - new Date(p.last_seen).getTime();
```

`last_seen` is written by the **controller**. `Date.now()` is the
**browser**'s clock. The two have no reason to agree, and on this site's
network they do not. A workstation six minutes ahead displays the whole
fleet as six minutes stale — proxies, devices, readings, logs.

The repository has **131 calls to `Date.now()`** in the frontend. All those that
subtract a timestamp coming from the server carry this defect.

What makes the bug costly is not the display: it is that the symptom
blames **the wrong component**. One goes off to check a proxy that is fine.

## Decision

The freshness of a server timestamp is measured against the **server's
clock**, never against the browser's.

The offset is learnt from the `Date` header that **every HTTP response already
carries** — no extra round-trip, no new endpoint. The
client updates it on every response and exposes `serverNow()`.

When the offset exceeds a clear threshold, the interface **says so**: a banner
naming the local clock as the culprit, not the fleet.

## Consequences

- Transport delay enters the measurement: the `Date` reading is accurate to
  within a round-trip, typically a few tens of milliseconds. Irrelevant
  for thresholds that live in tens of seconds.
- An HTTP `Date` has a resolution of **one second**. Sufficient here, and not to be
  reused to time anything fine-grained.
- `Date.now()` remains correct — and mandatory — for everything that is **local**:
  a timer, an animation duration, a debounce. The rule only applies
  to the difference with a timestamp **coming from the server**.
- Two clocks now exist on the client side. That is real complexity, and
  the name (`serverNow`) is what must make it obvious at the call site.
- The banner may appear for someone whose clock is right and whose
  **server** is drifting. It therefore names a disagreement, not a culprit.

## Rejected alternatives

**Doing nothing and fixing the site's NTP.** Necessary, and insufficient:
meshcraft is used from workstations it does not control, and the next
badly set machine will reproduce exactly this report. A monitoring
product cannot make its diagnosis depend on the clock of whoever is
looking at it.

**Lengthening the keepalive / shortening the heartbeat**, which was the initial
request. Would have changed nothing: the heartbeat arrived every 30 s and
`last_seen` was 6 seconds old. Would have added traffic and left the bug intact.

**Returning the age already computed by the server** (`age_seconds` next to
`last_seen`). Correct, and it freezes the value at the instant of the response: a
table left open for two minutes would show an age that does not grow older.
The offset ages correctly.

**A `/api/time` endpoint.** A round-trip for information that
every response already carries in its header.

**Trusting the `Date` of a single response, at start-up.** A workstation that
resynchronises during the session, or wakes from sleep, would keep a
stale offset. Every response refreshes it at zero cost.
