package main

import (
	"strings"
	"testing"
	"time"

	"meshcraft/internal/tunnel"
)

// What a device LEAVING the watch list must leave behind: nothing.
//
// `SetWatches` already purged the port states, the neighbour log damping and
// the SNMP reachability. The poll cadences (`c.last`) were the one map that
// stayed, and this file pins the two properties that cost.
//
// The cadences are seeded through `due` rather than through `pollDue`: pollDue
// would actually send the polls it schedules, and these tests are about the
// bookkeeping, not about a wire.

// watchFor builds a watch whose only interesting field is its cadence.
func watchFor(id string, everyS int) tunnel.SNMPWatch {
	return tunnel.SNMPWatch{
		DeviceID:          id,
		IP:                "192.0.2.1",
		Community:         "public",
		StateIntervalS:    everyS,
		CountersIntervalS: everyS,
	}
}

// pollAt records a poll of both MIB-II metrics, as pollDue would have.
func pollAt(c *SNMPCollector, id string, at time.Time, everyS int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.due(id+"|state", at, everyS)
	c.due(id+"|counters", at, everyS)
}

// lastKeysFor counts the cadence entries held for one device.
func lastKeysFor(c *SNMPCollector, id string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	var n int
	var key string
	for key = range c.last {
		if strings.HasPrefix(key, id+"|") {
			n++
		}
	}
	return n
}

// TestWithdrawnDeviceLeavesNoCadence
//
// The SIZE property. `c.last` is keyed by (device, metric), so without a purge
// it is bounded by the devices EVER watched — a controller that moves devices
// between proxies grows it and never shrinks it. Nothing here reads a
// timestamp: the point is only that the entries are gone.
func TestWithdrawnDeviceLeavesNoCadence(t *testing.T) {
	var c = NewSNMPCollector("http://example.invalid", "px-1", "s")
	var now = time.Now()

	c.SetWatches([]tunnel.SNMPWatch{watchFor("dev-a", 30), watchFor("dev-b", 30)})
	pollAt(c, "dev-a", now, 30)
	pollAt(c, "dev-b", now, 30)
	if lastKeysFor(c, "dev-a") == 0 || lastKeysFor(c, "dev-b") == 0 {
		t.Fatal("both watched devices must have recorded a cadence")
	}

	// dev-b leaves the watch list.
	c.SetWatches([]tunnel.SNMPWatch{watchFor("dev-a", 30)})

	if lastKeysFor(c, "dev-b") != 0 {
		t.Error("a device removed from the watch list must leave no cadence " +
			"entry: the map would then be bounded by the devices ever watched, " +
			"not by those watched now")
	}
	if lastKeysFor(c, "dev-a") == 0 {
		t.Error("the purge must not touch a device that is STILL watched: it " +
			"would re-poll the whole fleet on every WatchConfig")
	}
}

// TestReaddedDevicePollsPromptly
//
// The FRESHNESS property, and it is narrower than the size one — which is why
// it is pinned rather than merely asserted in a comment. `due` compares
// `now - prev` against the cadence, so a stale timestamp OLDER than the cadence
// delays nothing: a device gone an hour re-polls at once whatever the map
// holds. What bites is leaving and returning INSIDE the cadence — a corrected
// watch, a controller restart — where the surviving timestamp reads "already
// polled" and the returning device stays mute for up to a full period.
//
// The clock is passed in rather than slept through: 30 s is exactly the fleet's
// `if_state` cadence, and no test may take 30 s to observe it.
func TestReaddedDevicePollsPromptly(t *testing.T) {
	var c = NewSNMPCollector("http://example.invalid", "px-1", "s")
	var t0 = time.Now()

	c.SetWatches([]tunnel.SNMPWatch{watchFor("dev-a", 30)})
	pollAt(c, "dev-a", t0, 30)

	// Two seconds later — WELL inside the 30 s period — the device leaves and
	// comes straight back.
	var t1 = t0.Add(2 * time.Second)
	c.SetWatches(nil)
	c.SetWatches([]tunnel.SNMPWatch{watchFor("dev-a", 30)})

	c.mu.Lock()
	var fires = c.due("dev-a|state", t1, 30)
	c.mu.Unlock()
	if !fires {
		t.Error("a device re-added to the watch list must poll at once: it " +
			"comes back from a world where the controller's configuration may " +
			"have changed, and a stale timestamp would keep it mute for up to " +
			"one full period — precisely the seconds one wants to see")
	}
}
