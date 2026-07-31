// Package renumber tracks candidate management IPs while a device is moving.
//
// After apply / jump / migrate, meshcraft may know one or more addresses the
// device SHOULD answer on, before `devices.mgmt_ip` is trusted there. The
// controller pushes those candidates into the proxy WatchConfig (same
// device_id, extra IPs). The proxy only pings; when `device.up` arrives on a
// candidate, the controller verifies SERIAL (via relocate Identify) and only
// then promotes `mgmt_ip`.
//
// ICMP alone must never promote: a recycled DHCP lease can be another device.
package renumber

import (
	"sync"
	"time"
)

// DefaultTTL: how long a candidate stays on the watch list without confirmation.
const DefaultTTL = 15 * time.Minute

// Candidate is one extra IP the proxy should ping for a device.
type Candidate struct {
	DeviceID string
	SiteID   string
	Name     string
	IP       string
	Since    time.Time
}

type entry struct {
	deviceID string
	siteID   string
	name     string
	ips      map[string]time.Time
}

// Tracker holds pending renumber candidates in memory (like relocate.Tracker).
type Tracker struct {
	mu   sync.Mutex
	byID map[string]*entry
	ttl  time.Duration
}

// NewTracker builds an empty tracker.
func NewTracker() *Tracker {
	return &Tracker{byID: map[string]*entry{}, ttl: DefaultTTL}
}

// Remember adds candidate IPs for a device. Empty IPs and the current primary
// (caller filters that) are ignored. Extends TTL on re-add.
func (t *Tracker) Remember(deviceID, siteID, name string, ips ...string) {
	if t == nil || deviceID == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pruneLocked(time.Now())
	var e, ok = t.byID[deviceID]
	if !ok {
		e = &entry{
			deviceID: deviceID, siteID: siteID, name: name,
			ips: map[string]time.Time{},
		}
		t.byID[deviceID] = e
	}
	e.siteID, e.name = siteID, name
	var now = time.Now()
	var ip string
	for _, ip = range ips {
		if ip == "" {
			continue
		}
		e.ips[ip] = now
	}
	if len(e.ips) == 0 {
		delete(t.byID, deviceID)
	}
}

// Forget clears all candidates for a device (after successful promote).
func (t *Tracker) Forget(deviceID string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.byID, deviceID)
}

// ForgetIP clears one candidate IP after it was rejected (serial mismatch).
func (t *Tracker) ForgetIP(deviceID, ip string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	var e, ok = t.byID[deviceID]
	if !ok {
		return
	}
	delete(e.ips, ip)
	if len(e.ips) == 0 {
		delete(t.byID, deviceID)
	}
}

// Has reports whether ip is a live candidate for deviceID.
func (t *Tracker) Has(deviceID, ip string) bool {
	if t == nil {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pruneLocked(time.Now())
	var e, ok = t.byID[deviceID]
	if !ok {
		return false
	}
	var _, hit = e.ips[ip]
	return hit
}

// All returns every live candidate (for merging into WatchConfig).
func (t *Tracker) All() []Candidate {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pruneLocked(time.Now())
	var out []Candidate
	var e *entry
	for _, e = range t.byID {
		var ip string
		var since time.Time
		for ip, since = range e.ips {
			out = append(out, Candidate{
				DeviceID: e.deviceID, SiteID: e.siteID, Name: e.name,
				IP: ip, Since: since,
			})
		}
	}
	return out
}

func (t *Tracker) pruneLocked(now time.Time) {
	var id string
	var e *entry
	for id, e = range t.byID {
		var ip string
		var since time.Time
		for ip, since = range e.ips {
			if now.Sub(since) > t.ttl {
				delete(e.ips, ip)
			}
		}
		if len(e.ips) == 0 {
			delete(t.byID, id)
		}
	}
}
