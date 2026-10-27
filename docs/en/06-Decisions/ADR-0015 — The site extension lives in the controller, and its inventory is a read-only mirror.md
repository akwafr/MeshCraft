---
Status: Refined by ADR-0025
Date: 2026-10-24
---

> **Point 5 has been reopened.** The sentence "no adoption, no configuration change,
> no access point reboot" is no longer accurate:
> [[ADR-0025 — meshcraft remote-controls the UniFi controller, it holds no intent]] allows three gestures going through the controller's API. The rest of
> this document — including "an imported device never enters `devices`" — remains in
> force. The text below is not corrected: it is the record of what had been decided,
> and that is what people come here to re-read.

# ADR-0015 — The site extension lives in the controller, and its inventory is a read-only mirror

## Context

`CLAUDE.md` already uses the word **extension**: "An agent (a proxy or an extension)
is never run here". The sentence describes the *product of a build pipeline* —
`internal/agenthost` makes an OCI layout that the internal registry serves, and
something remote will pull it. That is all the word designated. **No extension exists
in the code**, and nothing says what an extension *does*.

The request that forces the question: a meshcraft site must be able to be backed by a
**UniFi site**, to import its switches and Wi-Fi access points from it. The `ARENA`
site corresponds to the UniFi site named "lab Events #04 - ARENA".

Facts captured in the field before deciding:

- The UniFi controller is reachable **directly** from the meshcraft controller's host,
  over HTTPS, on a public name: `200` in ~1 s. It is not an RFC1918 address deep
  inside a site.
- It exposes an **integration API v1** authenticated by an `X-API-KEY` header:
  `/info`, `/sites`, `/sites/{id}/devices`, a device's detail, `/statistics/latest`,
  `/clients`. Application version captured: `10.6.109`.
- SSIDs are **not** there: `/sites/{id}/wlans` answers `404`. One has to fall back on
  the legacy API `/proxy/network/api/s/{internalReference}/rest/wlanconf`, which is
  indexed by the site's **short reference**, not by its UUID.
- A UniFi device carries `adoptedAt`, `provisionedAt` and a `configurationId`. The
  UniFi controller **is** its configuration authority, and it says so in its own API.
- The test secret arrives through `UOS_URL` / `UOS_TOKEN` in `.env.local`.

A UniFi controller is therefore not one more device: it is a **peer controller**, which
itself holds a fleet.

## Decision

**A site extension is an integration that runs in the meshcraft controller, configured
per site, and whose imported inventory is a read-only mirror that never enters
`devices`.**

1. **In the controller, not in a container.** MDP v1 models *one driver, one device*:
   `/reach`, `/state`, port gestures, a SOCKS dialer. A UniFi controller does not fit
   that mould — one would have to invent the multi-device driver, which MDP v1 does not
   have, to wrap an HTTP client of three calls. The extension is therefore controller
   code.

2. **"Site extension" and "agent extension" are two different words, and we
   distinguish them explicitly.** This one is a local integration. The original word
   (`agent_repos`, `internal/agenthost`) remains reserved for what must **run
   elsewhere**. The day an extension needs to run inside a site's network, it will
   become an agent extension and reuse `agenthost` — the present decision does not
   close that door, it only refuses to open it for an HTTP client.

3. **Reachability is a configuration, not an architecture.** This is the point we were
   about to miss by choosing the container "in case the controller is remote". An
   extension declares a **transport**: `direct`, or *via site X's proxy*. meshcraft
   already has `internal/socks` and `internal/tunnel`; a UniFi controller locked inside
   a LAN is reached through the proxy already placed there, with no extra container.

4. **The secret belongs in the vault, not in `.env.local`.** An extension configured
   *per site* has nothing to do in the controller's environment file: such a file is
   global, it is unique, it cannot be revealed, it is not logged and it does not
   survive two sites backed by two distinct UniFi controllers. The token is therefore
   encrypted by `internal/secretbox` and follows exactly the regime of the existing site
   credentials: stored sealed, revealed by an explicit gesture, and **every reveal
   logged**. `UOS_URL` / `UOS_TOKEN` in `.env.local` are a **development bootstrap**:
   enough to test against the real UniFi, nothing more, and no production path reads
   them.

5. **The imported inventory is a read-only mirror.** meshcraft configures nothing
   through UniFi: no adoption, no configuration change, no access point reboot. And,
   above all, **an imported device is not inserted into `devices`**: that table carries
   the invariant "something meshcraft configures, with a connector and credentials". A
   UniFi switch already has a master, which re-provisions it at its own pace; two write
   authorities on the same device do not produce an error, they produce a configuration
   that reverts by itself, minutes later, with nothing in any log. We **explicitly
   refuse** it rather than discover it.

6. **Pairing is done on the UniFi site's identifier, the name is only a display.** We
   store the `id` (stable UUID), **and** the `internalReference` — because the legacy
   SSID API accepts only that — and the `name` at last contact, as a cached label,
   never as a key. It is the same rule as [[ADR-0014 — The declared label is evidence, identity is derived]]: "lab Events #04 - ARENA" will be
   renamed one day, and the pairing must survive it.

7. **A UniFi site that has disappeared is orphaned, not deleted.** If `/sites` answers
   and the paired `id` is no longer there, the pairing moves to `orphaned` with the
   timestamp of the observation; the mirror stays visible, stamped stale. Nothing is
   deleted. And this case is distinguished from "controller unreachable": only the
   first orphans, because a ten-minute network outage must not erase someone's
   correlation work.

## Consequences

- **There are now two inventories on a site**: the `devices` meshcraft drives, and the
  extension mirror it only reads. Every "what is on this site?" question must choose
  its answer, and the dashboard counters must say which one they count. That is the
  main price, and it is permanent.
- **A UniFi switch is not configurable from meshcraft.** Someone will come along asking
  why, since meshcraft can talk to switches. The answer is point 5, and the way out is
  real: unadopt it from UniFi, and it becomes an ordinary device with a connector
  again. No half-measure is offered.
- The interface must **say** "read-only", not merely omit the buttons. An absent button
  reads as a missing right.
- The meshcraft controller acquires an outbound dependency on a third-party service on
  its own request path. Every UniFi call therefore carries a hard timeout, and the
  failure of an extension is never the failure of a page.
- The legacy `wlanconf` API is not under contract: it may break at a UniFi update. It
  is isolated behind a named function, and the absence of SSIDs degrades the screen
  instead of emptying it.
- The registry of extension types is data, not a `switch`: UniFi OS is the first
  incarnation, not the only one planned.

## Rejected alternatives

**A containerised agent speaking MDP v1.** The option faithful to the letter of
`CLAUDE.md`, and the one we were going to take for consistency. It buys isolation — an
extension that crashes does not crash the controller — and an independent delivery
cadence. Rejected on a fact: the UniFi controller answers the host directly, so the
container solves no access problem here, and MDP v1 would have to be twisted to fit a
peer controller holding 4 devices. The price of this refusal is accepted in point 3 (a
configurable transport replaces network isolation) and in the consequences (a timeout
to replace failure isolation).

**Import UniFi devices into `devices`, with a `read_only` flag.** Very tempting: all
the existing display comes for free — faceplate, ports, topology, MAC search. Rejected
because the flag would lie by omission everywhere it is not read, and there are many
such places: a port gesture, a credential deployment, a renumbering plan, a reset
procedure. An invariant held in the type is held; an invariant held in a boolean to be
checked in thirty calls is a leak waiting to happen.

**Make UniFi switches drivable, since meshcraft can do it.** Rejected at point 5. It is
not a technical limit, it is a refusal of co-authority: the symptom of a double write
authority is not an error, it is a silent drift, and it is diagnosed weeks later.

**Keep the token in `.env.local`.** Works immediately, and that is in fact how it was
tested. Rejected at point 4: a per-site setting in a global file does not hold for two
sites, and a secret that can be neither revealed nor logged is not administrable.

**Pair on the UniFi site's name**, which is what the operator reads and recognises.
Rejected at point 6: a name changes, and the pairing would break without saying
anything — or, worse, attach to the wrong site after a name is reused.

**Delete the mirror when the UniFi site disappears**, so as not to leave dead data.
Rejected at point 7: it is indistinguishable from an outage at the moment it happens,
and deletion is the only one of the two options that cannot be repaired.
