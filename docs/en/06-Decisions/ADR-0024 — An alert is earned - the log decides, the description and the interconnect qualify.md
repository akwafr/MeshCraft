---
Status: Accepted
Date: 2026-10-25
---

# ADR-0024 — An alert is earned: the log decides, the description and the interconnect qualify

## Context

The product already observes a lot, and warns nobody. Three mechanisms exist,
measured and shipped:

**A device becoming unreachable.** The proxy pings at 1 Hz, requires three
consecutive losses before concluding (`DefaultDownAfter = 3`,
`internal/tunnel/control.go:551`), and only reports **transitions**. The
controller aggregates the verdicts per proxy (`judgeReach`,
`internal/api/reachability.go:103`) and writes `devices.status`. Detection delay:
**~2.75 s to 3.75 s**. Recovery is instantaneous and asymmetric — a single echo
reply is enough. The log receives `device.unreachable` (`SevWarning`) and
`device.recovered` (`SevNotice`); `internal/api/watch_handlers.go:1204`. The
`unknown` verdict is never logged, deliberately: filing as `device.recovered` a
device nobody has looked at yet would put a recovery in the log for an ignorance.

**Port flapping, already correlated.** `internal/api/portevents.go` is a complete
correlator, in memory, with an injected clock, Monte-Carlo tested:
`flapThreshold = 5` transitions within `flapWindow = 5 min` open an episode,
`flapCalm = 3 min` of calm closes it, `dupWindow = 2 min` deduplicates across
proxies. It produces `port.up` / `port.down` (`SevInfo`), and above all
`port.flapping` (`SevWarning`) then `port.flap_cleared`. **Grouping transitions
into "this port is flapping" is therefore already written**, and its header says
why the severities do not follow intuition: a port going down is not a failure, it
is the daily life of a fleet of two thousand ports.

**An interest filter, already in place.** `portManaged`
(`internal/api/watch_handlers.go:1586`) refuses to write to the log any port the
inventory says is "free". The reasoning is written on the spot: logging every laptop
plug-in means hundreds of lines a day, and drowning what matters.

### What does not exist

- **No notification code.** No webhook, no e-mail, no Telegram, no Slack.
  `grep -i 'webhook|telegram|slack|smtp|notifier'` over all the Go and TS only
  returns two false positives (the screens' wake-up bus, and an interface hook). It
  is bare ground.
- **No maintenance window, no silence, no mute.**
  `maintenance|quiet|mute|suppress` only returns prose in comments.
- **No port description read from the device.** And that is the central fact of
  this ADR.

### The port description is not what one thinks

`mdpv1.PortState` (`wire/mdpv1/state.go:68`) carries four fields — `Mode`, `PVID`,
`Tagged`, `TaggedAtLeast` — and its comment deliberately bounds the scope:
"deliberately limited to what is CONFIGURED". There is **no `Description`, no
`Alias`, no `Name`**. The MDP v1 contract cannot carry a port description.

On the connector side, the inventory is clear: **none** reads `ifAlias`
(1.3.6.1.2.1.31.1.1.1.18). The OID appears nowhere except in two test fixtures. The
nine external drivers walk `ifDescr` / `ifName`, and use them to **resolve a port
name**, never as a description. Only one place touches the column: the Arista
`show interfaces status` parser (`internal/driver/arista/eos/parse.go:243`) whose
regex **captures** the Name column — `Et49  core-uplink  connected` — and never
reads `m[2]`. The data is on the wire, it is matched, it is thrown away.

What the screen *calls* a description is something else:
`devices.ports.<port>.label`, an annotation **typed by a meshcraft operator**. The
Ports screen titles it "description" (`portsPage.describePlaceholder`,
`web/src/pages/Ports.tsx:273`). And the product **actively protects** it from device
reads: migration `0019_purge_ports_fantomes.sql:25` classifies it as INTENT and
never purges it; `internal/api/recon_handlers.go:1538` says "the configuration comes
from the device, the label stays with meshcraft"; `internal/store/devices.go:954`
leaves `label` intact when absorbing a drift.

There are therefore two candidates for "port with a description", and they do not
say the same thing. `ifAlias` says "someone described this port **on the device**" —
often a predecessor, often stale, sometimes an automatic inventory with no intent at
all. `label` says "someone described this port **in meshcraft**, in the screen meant
for doing so". The second is both the only one available and the better signal.

### What forced the question

The request arrived in one sentence: alert on devices UP/DOWN, on interconnect ports
UP/DOWN, on ports **with a description** UP/DOWN — "ports without a description, we
don't care". And during the night of the 24th to the 25th, eight switches of ARENA
had their MSTP region rewritten. Had an alerting been running, it would have rung on
every port transition caused by **our own gesture**.

## Decision

**Alerting is a subscriber of the event log, not a second event model**; and a port
alert is only emitted if the port **carries an operator description**
(`devices.ports.<port>.label` non-empty) **or faces a device of the fleet**.

Six rules, each fitting in one sentence.

**1. The log is the only source.** No new observation stream, no second detection. A
`notify_deliveries` references `events.id` by foreign key: an alert is the **delivery
slip** of a log line, never a competing fact. The alertable vocabulary is exactly six
types already emitted: `device.unreachable`, `device.recovered`, `port.down`,
`port.up`, `port.flapping`, `port.flap_cleared`.

**2. Three classes, and an exclusion rule.** A subscription chooses among `device`
(the device), `interco` (port facing a known device), `described` (port carrying a
label). A port event that is **neither** interco **nor** described generates no slip —
it stays in the log, visible on the port's record, and wakes nobody. A port that is
both interco and described counts as `interco`: the most consequential class wins.

**3. "Interco" is derived, never stored.** A port is interco if
`LLDPNeighbor.PeerDevice != ""` for that port (recognition done on read by
`identifyPeer`, `internal/api/lldp.go:289`) **or** if a `device_links` row touches
that port. It is the union `topologyLinks` (`internal/api/stp_rootbridge.go:316`)
already assembles: the declared first, then the observed whose far end is recognised.
No `is_uplink` column, no role enumeration: the day the operator adopts the device
opposite, the port becomes interco without rewriting a table.

**4. A key that keeps moving does not alert.** Every event is **held** before
delivery — `alertHold = 45 s` for a port, `alertDeviceHold = 20 s` for a device. A
second transition on the same key within the window **cancels the delivery and pushes
back the deadline**: as long as the key keeps moving, nothing goes out. Delivery
happens when the key has **gone quiet** for the window — a single message, carrying
the final state and the number of transitions seen — or when `port.flapping` arrives,
and it is then that message that goes out. Ceiling `alertHoldMax = 5 min` so that a
port flapping endlessly still speaks. Six round trips in two minutes produce **one**
message.

No class escapes the hold, and it took a second attempt. The first version exempted
the device, in the name of latency: its detection is already debounced where needed
(three consecutive ICMP losses at the proxy), and a device that is down is no less so
after 45 s. Both halves are true and the conclusion does not follow, because of the
**asymmetry**: three losses to go down, **a single echo reply to come back up**. A
device behind a flapping link therefore produces a down/up pair every few seconds,
each a real status change that `SetDeviceStatus` will not deduplicate — and, without a
hold, each a message. It was the storm of pitfall no. 1 coming in through the door left
open for latency. 20 s close it for a cost nobody perceives: nobody tells 3 s from
23 s on a phone notification.

**5. An alert attributable to our gesture is deferred, never lost.** A candidate is
suppressed if, within the preceding `selfActionWindow = 10 min`, the log carries for
this device a **gesture** event (`config.applied`, `device.port_admin`,
`device.stp_apply`, `mgmt.applied`, `credential.installed`, `device.adopted`,
`proxy.upgraded`…) or if `applyQueuedOrRunning(deviceID)`
(`internal/api/autoapply.go:77`) is true. Suppression only delays **bad news** — the
doctrine already proven by `holdReach` (`internal/api/reachability.go:284`) — and a
sweep **re-arms** the alert when the window closes if the state is still bad. The log
therefore serves us twice: it says what happened, and it says that it was us.

**6. Delivery is an effect, not a condition.** The slip is written to the database
before any outgoing call. An unreachable channel advances `attempts`, `last_error`,
`next_attempt_at` and ends in `failed` — the alert **existed**, the product can say
so, and detection never waited. Each channel carries `last_ok_at` / `last_error` /
`consecutive_failures`, and an explicit **test send** is offered on screen. Secrets
(Telegram bot token, Slack webhook URL) go into `internal/secretbox`, are registered
in `store.SealedColumns()`, are never returned by the list route — a `has_token` is
enough — and are only revealed by a route that logs first.

## Consequences

**A critical port nobody described stays silent.** It is the accepted blind spot, and
it must be named without softening it: the rule makes the description a *signal of
human interest*, and it therefore inherits all of human negligence. A UPS port, a
camera port, an access controller port — plugged in in a hurry, never described,
never facing a meshcraft device — go down without a word. Three mitigations, none of
them a solution:

- interco catches everything facing the fleet, without a description;
- the screen **counts and displays** what it kept quiet ("127 port transitions not
  alerted: no description and no known neighbour, on 14 ports"), which turns the
  blind spot into a **visible number** instead of a silence;
- describing a port is a one-field action, in a screen that already exists.

The price is accepted because the other direction costs more: alerting on every
managed port is, on ARENA and its 88 clients, a channel nobody reads after a week.

**Alerting lags behind detection, on purpose.** 20 s of hold on a device, 45 s on a
port, up to 5 min on an unstable port, 10 more minutes behind one of our gestures. The
product **detects** in 3 s and **alerts** much later. Whoever wants to-the-second
precision looks at the screen, whoever wants to sleep takes the channel. It is written
down, because someone will measure the gap and believe it is a bug.

**A gesture window can mask a real simultaneous failure.** Ten minutes after an
`apply`, a device going down for an unrelated reason is deferred. Re-arming catches it
— at worst ten minutes later — but the delay is real, and it is the price of not
ringing on our own writes.

**The log becomes a hot path, and it has no purge.** `internal/store/events.go`
declares itself append-only and the table grows without limit; no migration ever added
a TTL. Plugging alerting into it makes that absence more costly. We do not fix it here,
we record it.

**The log's emission points have to be touched.** `emit`
(`internal/api/events_handlers.go:16`) covers ~143 calls, but `emitDeviceBG`
(`autoapply.go:178`) and **twenty direct calls** to `Store.InsertEvent` bypass it —
including `writePortDecisions`, which is precisely the path of port events. The hook
must cover both, and it is mechanical but non-zero work.

**Ports collected over gNMI will emit no port alert.** A bug found while establishing
this inventory, and it predates this decision: `cmd/proxy/gnmi.go:933` emits its
transitions **without `IfIndex`**, and `portFlaps.observe`
(`internal/api/portevents.go:345`) rejects anything with `IfIndex <= 0`. No
`port.up` / `port.down` / `port.flapping` is therefore ever written for an Arista. The
`link` column is updated (that path matches by name), the screens are right, and the
log is silent. Alerting inherits the hole as is; `alertHoldMax` is what prevents an
unstable Arista port from *also* being held indefinitely, since the `port.flapping`
that should release it will never arrive. **To be fixed separately**, and it is
alerting that will make this hole visible.

**We henceforth forbid ourselves** to add an "alert" event type that is not first a
log line, and to evaluate a port's interest anywhere other than at the delivery
decision. An alert without a corresponding log line would be undebuggable.

## Rejected alternatives

**Reading `ifAlias` and alerting on the device's description.** It is the literal
reading of the request, and it loses for three cumulative reasons. First the cost:
`mdpv1.PortState` does not have the field, so it takes an optional field in
`wire/mdpv1`, regenerating `schemas.json`, writing `openapi.yaml` by hand, a
migration, and a read **per driver** across nine external drivers plus the legacy —
for a feature whose detection is already there. Then the semantics: `ifAlias` is often
a leftover, set by a predecessor or by an automatic inventory, and "this port carries a
text" does not mean there "someone cares about it today". Finally the collision: one
would have to arbitrate `ifAlias` against `label`, that is, decide who wins between
the device and the operator — exactly the observation/intent split that migration 0019
and `recon_handlers.go` already settled the other way. The `label` gives the same
service right away, with a cleaner signal. If `ifAlias` is ever read, it will be to
**display it next to** the label, and a new ADR will say which one qualifies.

**Alerting on all managed ports, and letting the operator filter.** `portManaged`
already exists and excludes "free" ports: the temptation is to stop there. But
"managed" means "the inventory gave it a mode", which the vast majority of a
configured site's access ports have. On ARENA, that is all the ports carrying the 88
clients. A recipient-side filter does not exist in Telegram, and a channel-side filter
nobody sets is a channel nobody reads. Exclusion must be the **default**, not an
option.

**An autonomous alert model, with its own probes.** The classic approach: an `alerts`
table, rules, an evaluator polling the state periodically. Rejected because it builds
a second truth beside the log. Two systems deciding separately whether a device is
down will diverge, and the day the channel says one thing and the screen another, it
is trust in both that goes. Anchoring on `events.id` makes every message **traceable**
to the line that caused it.

**A second flap correlator in alerting.** Tempting, because `portevents.go` is in
memory and its thresholds (5 in 5 min) are chosen for the log, not for a channel.
Rejected: two anti-flaps in series means two sets of thresholds to tune, and a
behaviour nobody can predict. The 45 s hold is not a second correlation, it is a
**delivery delay** — it does not decide whether the port is flapping, it waits for the
question to be settled, by the correlator or by silence.

**An explicit maintenance window, set by hand.** The usual tool, and it would have its
place one day. But it assumes someone thinks of opening it — and on the night of the
24th, nobody would have done so before rewriting eight MSTP regions. The log already
knows we acted, timestamped, per device: using it is both free and infallible against
forgetting. A manual window would remain useful for a gesture **outside** meshcraft (an
electrician, a rack move); it is an addition, not this foundation.

**A periodic heartbeat to the channel.** The reflex answer to ambiguous silence, and
it is rejected **as a default**: a daily "all is well" message is exactly what a reader
learns not to see any more, and it trains people to ignore the channel. Silence is
disambiguated otherwise, and without sending anything: the slip persists independently
of delivery, each channel displays `last_ok_at` and its last failure, the screen counts
pending and failed deliveries, and the test send makes it possible to check **before**
the incident. "The channel sent nothing" therefore becomes a question the screen
answers. An **optional** heartbeat, per channel, remains a legitimate addition for
whoever monitors their alerting from outside; it is not the foundation.

**Putting the tokens in `.env`.** Rejected for the reason of ADR-0015: a secret per
channel, editable on screen, belongs in the vault. And registration in
`SealedColumns()` is not optional — a sealed column missing from that inventory is
**destroyed at the next key rotation**, which the guard test
`TestInventaireDesColonnesScellees` (`internal/store/rekey_test.go:44`) catches in both
directions.
