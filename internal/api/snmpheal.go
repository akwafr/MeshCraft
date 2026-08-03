package api

// SNMP provisioning catch-up — provisionSNMP's promise that "the push
// retries", finally kept.
//
// # The operational defect that gave birth to this loop (2026-08-02)
//
// An FS S3900 adopted the day before the connector that can write to it was
// deployed: at adoption, "connector can DESCRIBE this platform but not yet
// reach it", snmp.apply_failed at severity 3 — then NOTHING MORE, ever. The
// proxy polled with the planned community, the agent did not carry it,
// snmp_status stayed "off / no longer responds to SNMP", and the only remedy
// was knowing about the "re-push SNMP" button on the device page. A write
// whose only chance to succeed is the instant of adoption makes fleet
// supervision depend on THE ORDER OF DEPLOYMENTS, which is not a tenable
// contract.
//
// # What the loop does, and above all what it does not
//
// It replays provisionSNMP on devices whose supervision SHOULD be in place
// and is not: supervised plan + pushable identity + resolvable connector, and
// snmp_status other than "ok". The replayed gesture is IDEMPOTENT (ApplySNMP
// re-reads before writing: "already conforming" does not touch the flash) and
// already as loud as it needs to be (snmp.applied / snmp.apply_failed +
// pipeline).
//
// It guesses nothing: a device without a plan, without a generated identity,
// or without a reachable connector is skipped SILENTLY — those states are
// already explained where one looks for them. And it does not hammer: one
// pass at startup (exactly the "the capability just arrived in this binary"
// case), then a per-device cooldown that doubles on every failure — a
// durably unreachable device ends up costing one attempt a day, not a
// drowned log.

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"meshcraft/internal/driver"
	"meshcraft/internal/model"
)

const (
	// snmpHealStartupDelay: let the proxies' tunnels come back up before the
	// first pass — a push through a tunnel that is not up yet would fail for a
	// reason that disappears on its own.
	snmpHealStartupDelay = 90 * time.Second
	snmpHealEvery        = 15 * time.Minute
	// Initial cooldown after a failure, doubled on each subsequent failure.
	snmpHealBackoffMin = 1 * time.Hour
	snmpHealBackoffMax = 24 * time.Hour
)

// snmpHealState: memory of attempts, IN MEMORY ONLY. A restart clears it, and
// that is intended: a fresh binary deserves an immediate retry.
type snmpHealState struct {
	mu   sync.Mutex
	next map[string]time.Time     // device -> not before this instant
	wait map[string]time.Duration // device -> current cooldown
}

func (h *snmpHealState) due(id string, now time.Time) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.next == nil {
		return true
	}
	return now.After(h.next[id])
}

func (h *snmpHealState) failed(id string, now time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.next == nil {
		h.next = map[string]time.Time{}
		h.wait = map[string]time.Duration{}
	}
	var w = h.wait[id]
	if w == 0 {
		w = snmpHealBackoffMin
	} else {
		w *= 2
		if w > snmpHealBackoffMax {
			w = snmpHealBackoffMax
		}
	}
	h.wait[id] = w
	h.next[id] = now.Add(w)
}

func (h *snmpHealState) succeeded(id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.next, id)
	delete(h.wait, id)
}

// RunSNMPHeal runs the catch-up until the controller stops.
func (s *Server) RunSNMPHeal(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(snmpHealStartupDelay):
	}
	var t = time.NewTicker(snmpHealEvery)
	defer t.Stop()
	for {
		s.snmpHealPass(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// snmpHealPass runs ONE pass. One device's failures stop neither the pass
// nor the loop.
func (s *Server) snmpHealPass(ctx context.Context) {
	var devices, err = s.allDevices(ctx)
	if err != nil {
		slog.Warn("SNMP heal: device list unreadable", "err", err)
		return
	}
	var now = time.Now()
	var retried = 0
	var d model.Device
	for _, d = range devices {
		if ctx.Err() != nil {
			return
		}
		if !s.snmpHeal.due(d.ID, now) {
			continue
		}
		if !s.snmpHealEligible(ctx, d) {
			continue
		}
		s.healOneDeviceSNMP(ctx, d, now)
		retried++
	}
	if retried > 0 {
		slog.Info("SNMP heal: pass done", "retried", retried)
	}
}

// healOneDeviceSNMP replays the push on ONE device and updates its backoff.
// Used by the periodic pass AND by the manual trigger (refresh).
func (s *Server) healOneDeviceSNMP(ctx context.Context, d model.Device, now time.Time) {
	slog.Info("SNMP heal: re-pushing supervision identity",
		"device", d.Name, "ip", d.MgmtIP)
	s.provisionSNMP(ctx, d)

	// provisionSNMP returns nothing: the re-read state says whether it took.
	var after, readErr = s.Store.GetDevice(ctx, d.ID)
	if readErr == nil && after.SnmpStatus == "ok" {
		s.snmpHeal.succeeded(d.ID)
		return
	}
	// Push done but the poll has not come round yet: the next pass will see
	// "ok" and clear it; meanwhile the backoff applies, which also bounds the
	// genuine failures.
	s.snmpHeal.failed(d.ID, now)
}

// KickSNMPHeal triggers a MANUAL catch-up for one device — the device page's
// refresh. In the background (the push talks to the device through the proxy,
// ~seconds) and on a fresh context (the request's dies with the response). The
// manual gesture CLEARS the cooldown: the operator explicitly asked for another
// try, and must not hit the backoff of a previous failure. Does nothing when
// supervision is already "ok" or is not pushable.
func (s *Server) KickSNMPHeal(d model.Device) {
	go func() {
		var ctx, cancel = context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		var fresh, err = s.Store.GetDevice(ctx, d.ID)
		if err != nil {
			return
		}
		if fresh.SnmpStatus == "ok" || !s.snmpHealEligible(ctx, fresh) {
			return
		}
		s.snmpHeal.succeeded(fresh.ID) // clear the cooldown before the manual try
		s.healOneDeviceSNMP(ctx, fresh, time.Now())
	}()
}

// snmpHealEligible: only replay what SHOULD work and does not. The cooldown
// (due) is tested SEPARATELY by the callers: the manual gesture ignores it.
func (s *Server) snmpHealEligible(ctx context.Context, d model.Device) bool {
	if d.SnmpStatus == "ok" {
		return false
	}
	if d.MgmtIP == "" {
		return false
	}
	// Pushable identity: supervised plan AND readable secret. The "nothing to
	// push" states are already explained on the device page — silence here.
	var _, ok = s.snmpIdentityToPose(ctx, d)
	if !ok {
		return false
	}
	// REACHABLE connector: a purely descriptive driver is not replayed, its
	// refusal will not change by itself... except in the next binary, and a
	// restart is precisely what resets the counters.
	var _, name = s.deviceIdentity(ctx, d)
	var _, connErr = driver.ConnectorFor(name)
	return connErr == nil
}
