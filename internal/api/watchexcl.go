package api

import (
	"sync"

	"meshcraft/internal/netscope"
)

// Visibility of devices EXCLUDED from monitoring.
//
// `netscope.SelectWatchTargets` refuses to monitor a device whose address is
// ambiguous for a proxy, and that is the right call: a false state is worse
// than no state. But as long as that refusal lived only in controller logs, it
// produced exactly what it was meant to avoid — a device with no indicator and
// no explanation, looking like a meshcraft outage.
//
// Two gaps, and that is the whole point of this file:
//
//   - an EVENT, emitted on TRANSITION only. The monitoring configuration is
//     recomputed on every proxy connection and every fleet change; logging each
//     pass would flood the journal and teach people not to read it. Same rule
//     as port transitions (cf. portevents.go) and ping debouncing;
//   - READABLE state on the device screen, so the banner can explain why the
//     indicator disappeared.
//
// In memory, never in the database. A controller restart replays events once
// when proxies reconnect, which is acceptable — and even useful, since the
// underlying problem is still there.

// watchExclusions tracks, per proxy, who is monitored and who was excluded.
type watchExclusions struct {
	mu sync.Mutex
	// rejected[proxyID][deviceID] = reason.
	rejected map[string]map[string]string
	// watched[proxyID][deviceID]: this proxy does monitor that device.
	//
	// Essential to avoid false alerts: a device excluded by one proxy may be
	// perfectly monitored by another, which is normal operation in a multi-proxy
	// fleet.
	watched map[string]map[string]bool
}

func newWatchExclusions() *watchExclusions {
	return &watchExclusions{
		rejected: map[string]map[string]string{},
		watched:  map[string]map[string]bool{},
	}
}

// record stores the result of a monitoring calculation for a proxy, and returns
// NEW rejections — those that deserve an event.
//
// "New" is judged fleet-wide, not per proxy: a device that moves from one proxy
// to another never stopped being monitored, and announcing it as excluded would
// be a false positive.
func (w *watchExclusions) record(proxyID string, kept []netscope.WatchCandidate,
	rejected []netscope.Rejected) []netscope.Rejected {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()

	var wasExcluded = map[string]bool{}
	var rej netscope.Rejected
	for _, rej = range rejected {
		wasExcluded[rej.DeviceID] = w.excludedLocked(rej.DeviceID) != ""
	}

	var keptSet = map[string]bool{}
	var c netscope.WatchCandidate
	for _, c = range kept {
		keptSet[c.DeviceID] = true
	}
	var rejSet = map[string]string{}
	for _, rej = range rejected {
		rejSet[rej.DeviceID] = rej.Reason
	}
	w.watched[proxyID] = keptSet
	w.rejected[proxyID] = rejSet

	var out []netscope.Rejected
	for _, rej = range rejected {
		// Was the device already excluded fleet-wide before this calculation?
		// If so, nothing new. Otherwise it just lost monitoring.
		if !wasExcluded[rej.DeviceID] && w.excludedLocked(rej.DeviceID) != "" {
			out = append(out, rej)
		}
	}
	return out
}

// forget drops a disconnected proxy. Its rejections no longer matter: it
// monitors nobody, and leaving its reasons in memory would show "not monitored"
// with no relation to reality.
func (w *watchExclusions) forget(proxyID string) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.watched, proxyID)
	delete(w.rejected, proxyID)
}

// excludedLocked: no proxy monitors this device, and at least one explicitly
// excluded it. Called with the lock held.
func (w *watchExclusions) excludedLocked(deviceID string) string {
	var set map[string]bool
	for _, set = range w.watched {
		if set[deviceID] {
			return ""
		}
	}
	var reasons map[string]string
	for _, reasons = range w.rejected {
		if reasons[deviceID] != "" {
			return reasons[deviceID]
		}
	}
	return ""
}

// reasonFor returns an device's exclusion reason, or "" if at least one proxy
// monitors it.
func (w *watchExclusions) reasonFor(deviceID string) string {
	if w == nil {
		return ""
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.excludedLocked(deviceID)
}
