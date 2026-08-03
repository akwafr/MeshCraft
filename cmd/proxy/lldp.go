package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"meshcraft/internal/snmppoll"
)

// LLDP neighbor reporting, PROXY side.
//
// See docs/01-Architecture/Voisinage LLDP.md. The controller says which devices
// to poll and at what cadence (`tunnel.SNMPWatch.LLDPIntervalS`); the proxy reads the
// LLDP-MIB and pushes the SNAPSHOT.
//
// # Why this path reuses neither the metrics buffer nor the event channel
//
// All three options were weighed, and the conclusion comes down to the NATURE of
// what travels:
//
//   - the metrics buffer (`metricbuf`) carries tagged floats. A neighbor is a
//     table of strings; forcing it in would mean encoding each system name into a
//     series tag, i.e. creating one time series per cable — which VictoriaMetrics
//     would keep forever, even after the cable is unplugged. And "since when has
//     this neighbor been seen" would then have to be read off the shape of a flat
//     curve, which isn't an answer;
//   - the tunnel's event channel carries only TRANSITIONS, never a repeated
//     current state (cf. docs/01-Architecture/Protocole controleur-proxy). Pushing
//     an entire table every thirty seconds would violate that invariant, and its
//     queue of 256 entries is sized for rare flips, not for a periodic snapshot.
//     Fabricating `neighbor.new` / `neighbor.gone` transitions would have made
//     every dropped event permanent — the same failure mode that `resync` cost a
//     lot to fix for reachability;
//   - `ask` is explicitly "NOT a collection path" (same doc). It's one-off,
//     controller-initiated, and has no cadence.
//
// What's left is the HTTP route, for exactly the reason that already moved
// metrics out of the tunnel: this is periodic volume, it authenticates via the
// (proxy_id, secret) pair, and the controller's scope control already applies to
// it (`internal/api/proxyscope.go`).
//
// # And why there is NO buffering
//
// This is the most useful difference between this path and the metrics one, and
// it favors simplicity. A lost measurement point is lost forever: it sampled a
// moment that will never come back, hence `metricbuf`, its 150,000 entries and
// its 57 MiB worst case. A lost neighbor snapshot is RE-SENT thirty seconds
// later, fresher than the one that would have been replayed — a device's LLDP
// table changes on the timescale of an unplugged cable, not of a second.
// Buffering would therefore have spent memory to re-push STALE information, and
// would additionally have required deciding what to do with two snapshots of the
// same device sitting in the queue.
//
// The price is named: a ten-minute tunnel outage leaves the controller's
// `last_seen` ten minutes behind. That's visible (the date is displayed) and
// self-healing on the first cycle that comes back up.

// lldpPushTimeout bounds the push. Shorter than the metrics client (20 s)
// because there is nothing to save behind it: a snapshot that didn't go out
// won't be replayed, it will be redone. Retrying harder would only buy a
// blocked goroutine.
const lldpPushTimeout = 10 * time.Second

// lldpErrEvery throttles the log of push failures, PER DEVICE.
//
// Same trade-off as `dropReportEvery` and `scopeRefusalEvery`: the first line
// is logged right away, the following ones are coalesced. Without this
// throttling, an unreachable controller would produce two lines per minute per
// device — across a fleet of a hundred, that's the log drowned by the very
// mechanism meant to raise the alarm.
const lldpErrEvery = 5 * time.Minute

// lldpJob builds the work of one neighbor poll.
//
// A `job` separate from the metrics one, rather than a fifth branch of
// `job()`: that one returns `metricbuf.Sample` values and buffers them, which
// makes no sense here. The two share what they must — the target, the
// connection conventions of `snmppoll.Dial` — and nothing more.
func (c *SNMPCollector) lldpJob(ctx context.Context, t snmppoll.Target) func() {
	return func() {
		var p = &snmppoll.Poller{Target: t}
		var report, err = p.PollLLDP(ctx)
		if err != nil {
			// A silent device is an ordinary operational fact, same as for the
			// metrics poll: DEBUG, not WARN. And above all we push NOTHING —
			// pushing an empty snapshot would erase, on the controller side, the
			// neighbors of a device whose only fault was to lose a UDP datagram.
			// A snapshot is only written once the MIB has actually been read.
			slog.Debug("LLDP neighbor poll got no response", "device", t.DeviceID, "ip", t.IP,
				"err", err.Error())
			return
		}
		c.pushLLDP(ctx, report)
	}
}

// pushLLDP pushes a snapshot to the controller.
//
// Returns nothing: there is no decision to make on failure, unlike `flush`
// whose return value arbitrates replay and bursting. See the file header for
// the reason — a snapshot is not replayed, it is redone.
func (c *SNMPCollector) pushLLDP(ctx context.Context, report snmppoll.LLDPReport) {
	var body, err = json.Marshal(map[string]any{
		"proxy_id": c.proxyID, "secret": c.secret, "report": report,
	})
	if err != nil {
		c.sayLLDPError(report.DeviceID, "unreadable snapshot: "+err.Error())
		return
	}
	// A timeout of its OWN, not the one from the collection cycle: a slow push
	// must not hold back the polling of other devices, and `pollDue` waits for
	// all of its jobs to finish.
	var pushCtx, cancel = context.WithTimeout(ctx, lldpPushTimeout)
	defer cancel()
	var req *http.Request
	req, err = http.NewRequestWithContext(pushCtx, http.MethodPost,
		c.controllerURL+"/api/proxy/lldp", bytes.NewReader(body))
	if err != nil {
		c.sayLLDPError(report.DeviceID, err.Error())
		return
	}
	req.Header.Set("Content-Type", "application/json")
	var resp *http.Response
	if resp, err = c.client.Do(req); err != nil {
		c.sayLLDPError(report.DeviceID, err.Error())
		return
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// The controller's reason is READ, same as on the metrics drop path:
		// it's the only place where we can learn that a scope refusal targets
		// us, and "status 403" without the sentence would send someone off
		// chasing a network fault instead.
		c.sayLLDPError(report.DeviceID,
			"controller refused (status "+resp.Status+"): "+refusalReason(resp.Body))
		return
	}
}

// sayLLDPError logs a push failure, at most once per device per lldpErrEvery.
//
// At `Warn` level: what's lost isn't a measurement but the FRESHNESS of a
// finding, and an operator looking at an LLDP link dated an hour ago must be
// able to learn why. Silent, this path would make a misconfigured controller
// look like a fleet with no neighbors.
func (c *SNMPCollector) sayLLDPError(deviceID, detail string) {
	var now = time.Now()
	c.mu.Lock()
	if c.lldpSaid == nil {
		c.lldpSaid = map[string]time.Time{}
	}
	var saidAt = c.lldpSaid[deviceID]
	if !saidAt.IsZero() && now.Sub(saidAt) < lldpErrEvery {
		c.mu.Unlock()
		return
	}
	c.lldpSaid[deviceID] = now
	c.mu.Unlock()
	slog.Warn("LLDP neighbor snapshot not pushed: this snapshot is lost, the "+
		"next cycle will redo it (no replay, see cmd/proxy/lldp.go)",
		"device", deviceID, "detail", detail, "next_attempt_in", lldpErrEvery.String())
}
