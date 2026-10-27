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

// Forwarding-table (learned MAC) reporting, PROXY side.
//
// The exact shape of cmd/proxy/lldp.go, for the exact reasons its header
// argues and which are not repeated: a table of strings is a SNAPSHOT, it
// travels by its own HTTP route (`POST /api/proxy/fdb`), it is neither
// buffered nor replayed — a snapshot that did not go out is not lost history,
// it is remade fresher on the next cycle. The controller decides which devices
// and at what cadence (`tunnel.SNMPWatch.FDBIntervalS`); the proxy executes.

// fdbPushTimeout bounds the push, same figure and same reasoning as LLDP's.
const fdbPushTimeout = 10 * time.Second

// fdbErrEvery throttles the push-failure log, per device, like lldpErrEvery.
const fdbErrEvery = 5 * time.Minute

// fdbJob builds the work of one forwarding-table poll.
func (c *SNMPCollector) fdbJob(ctx context.Context, t snmppoll.Target) func() {
	return func() {
		var p = &snmppoll.Poller{Target: t}
		var report, err = p.PollFDB(ctx)
		if err != nil {
			// A silent device is an ordinary operational fact: DEBUG, and above
			// all NOTHING is pushed — an empty snapshot would erase, controller
			// side, the sightings of a device whose only fault was to lose one
			// UDP datagram.
			slog.Debug("FDB poll got no response", "device", t.DeviceID, "ip", t.IP,
				"err", err.Error())
			return
		}
		c.pushFDB(ctx, report)
	}
}

// pushFDB pushes one snapshot to the controller. Returns nothing: there is no
// replay decision to make (see file header).
func (c *SNMPCollector) pushFDB(ctx context.Context, report snmppoll.FDBReport) {
	var body, err = json.Marshal(map[string]any{
		"proxy_id": c.proxyID, "secret": c.secret, "report": report,
	})
	if err != nil {
		c.sayFDBError(report.DeviceID, "unreadable snapshot: "+err.Error())
		return
	}
	var pushCtx, cancel = context.WithTimeout(ctx, fdbPushTimeout)
	defer cancel()
	var req *http.Request
	req, err = http.NewRequestWithContext(pushCtx, http.MethodPost,
		c.controllerURL+"/api/proxy/fdb", bytes.NewReader(body))
	if err != nil {
		c.sayFDBError(report.DeviceID, err.Error())
		return
	}
	req.Header.Set("Content-Type", "application/json")
	var resp *http.Response
	if resp, err = c.client.Do(req); err != nil {
		c.sayFDBError(report.DeviceID, err.Error())
		return
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		c.sayFDBError(report.DeviceID,
			"controller refused (status "+resp.Status+"): "+refusalReason(resp.Body))
		return
	}
}

// sayFDBError logs a push failure, at most once per device per fdbErrEvery —
// same throttle and same level as sayLLDPError, same reason: an operator
// looking at a client last seen an hour ago must be able to learn why.
func (c *SNMPCollector) sayFDBError(deviceID, detail string) {
	var now = time.Now()
	c.mu.Lock()
	if c.fdbSaid == nil {
		c.fdbSaid = map[string]time.Time{}
	}
	var saidAt = c.fdbSaid[deviceID]
	if !saidAt.IsZero() && now.Sub(saidAt) < fdbErrEvery {
		c.mu.Unlock()
		return
	}
	c.fdbSaid[deviceID] = now
	c.mu.Unlock()
	slog.Warn("FDB snapshot not pushed: this snapshot is lost, the next cycle "+
		"will redo it (no replay, see cmd/proxy/fdb.go)",
		"device", deviceID, "detail", detail, "next_attempt_in", fdbErrEvery.String())
}
