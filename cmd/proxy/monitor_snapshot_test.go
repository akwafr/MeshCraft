package main

import (
	"testing"

	"meshcraft/internal/tunnel"
)

// What this file tests: what the re-announcement snapshot SAYS, and above
// all what it STAYS SILENT about.
//
// The snapshot exists because a lost transition was never replayed (cf.
// tunnel.Event.Resync). But re-announcing too much would be worse than not
// re-announcing anything: a target that was never pinged, presented as
// "online", would write to the controller a finding that nobody made, and on
// a platform with no fallback console that's the kind of status that makes
// someone cancel a site visit.

// TestSnapshotSilentWhenUnobserved: a target with no verdict produces nothing.
//
// `up == nil` means "not pinged yet". Neither online nor unreachable: there
// is no finding to re-announce, and inventing one would assert more than
// monitoring actually knows.
func TestSnapshotSilentWhenUnobserved(t *testing.T) {
	var m = NewMonitor()
	m.SetConfig(tunnel.WatchConfig{Targets: []tunnel.WatchTarget{
		{DeviceID: "dev-never", IP: "10.0.0.1", Name: "never-pinged"},
	}})

	var snap = m.Snapshot()
	if len(snap) != 0 {
		t.Fatalf("snapshot = %d event(s), expected 0: a target with no verdict "+
			"is not re-announced", len(snap))
	}
}

// TestSnapshotReturnsCurrentStateInOrder: one event per target with a
// verdict, sorted by address, marked `Resync`.
//
// The stable order isn't cosmetic: two snapshots of the same state must
// produce the same byte sequence, otherwise comparing two reconnection
// streams stops meaning anything — and a Go map iteration order varies on
// every run.
func TestSnapshotReturnsCurrentStateInOrder(t *testing.T) {
	var m = NewMonitor()
	m.SetConfig(tunnel.WatchConfig{Targets: []tunnel.WatchTarget{
		{DeviceID: "dev-c", IP: "10.0.0.30", Name: "c"},
		{DeviceID: "dev-a", IP: "10.0.0.10", Name: "a"},
		{DeviceID: "dev-b", IP: "10.0.0.20", Name: "b"},
		// No DeviceID: the controller would have nothing to do with it.
		{DeviceID: "", IP: "10.0.0.40", Name: "anonymous"},
	}})

	var yes = true
	var non = false
	m.states["10.0.0.10"].up = &yes
	m.states["10.0.0.20"].up = &non
	m.states["10.0.0.20"].misses = 7
	m.states["10.0.0.30"].up = &yes
	m.states["10.0.0.40"].up = &yes

	var snap = m.Snapshot()
	if len(snap) != 3 {
		t.Fatalf("snapshot = %d event(s), expected 3 (the anonymous one was skipped)", len(snap))
	}

	var expected = []struct {
		ip     string
		typ    string
		misses int
	}{
		{"10.0.0.10", tunnel.EventDeviceUp, 0},
		{"10.0.0.20", tunnel.EventDeviceDown, 7},
		{"10.0.0.30", tunnel.EventDeviceUp, 0},
	}
	var i int
	for i = range expected {
		if snap[i].IP != expected[i].ip {
			t.Errorf("rank %d: address %s, expected %s — the snapshot must be sorted",
				i+1, snap[i].IP, expected[i].ip)
		}
		if snap[i].Type != expected[i].typ {
			t.Errorf("%s: type %s, expected %s", snap[i].IP, snap[i].Type, expected[i].typ)
		}
		if snap[i].Misses != expected[i].misses {
			t.Errorf("%s: misses %d, expected %d", snap[i].IP, snap[i].Misses,
				expected[i].misses)
		}
		if !snap[i].Resync {
			t.Errorf("%s: not marked Resync — the controller would mistake it for a "+
				"fresh flip and log an outage that never happened",
				snap[i].IP)
		}
		if snap[i].TS.IsZero() {
			t.Errorf("%s: empty timestamp", snap[i].IP)
		}
	}
}

// TestSnapshotDropsRemovedTarget: a target removed from the config
// disappears from the snapshot.
//
// `SetConfig` forgets targets absent from the new config, and the snapshot
// must follow suit: re-announcing the state of a device we no longer
// monitor would write a status that nothing will ever refresh.
func TestSnapshotDropsRemovedTarget(t *testing.T) {
	var m = NewMonitor()
	m.SetConfig(tunnel.WatchConfig{Targets: []tunnel.WatchTarget{
		{DeviceID: "dev-a", IP: "10.0.0.10"},
		{DeviceID: "dev-b", IP: "10.0.0.20"},
	}})
	var yes = true
	m.states["10.0.0.10"].up = &yes
	m.states["10.0.0.20"].up = &yes

	m.SetConfig(tunnel.WatchConfig{Targets: []tunnel.WatchTarget{
		{DeviceID: "dev-a", IP: "10.0.0.10"},
	}})

	var snap = m.Snapshot()
	if len(snap) != 1 || snap[0].DeviceID != "dev-a" {
		t.Fatalf("snapshot = %+v, expected only dev-a", snap)
	}
}

// A DEVICE REPLACED AT THE SAME ADDRESS MUST EARN A FRESH VERDICT.
//
// The monitor keys reachability by IP, which is right until the device behind
// the address changes. Carrying `up` across that change is how a re-adopted
// switch keeps a status nobody ever measured for it: the ping agrees with the
// stale verdict, no flip is announced, and the controller — which has never
// heard of the new device id — shows `unknown` indefinitely. Measured on a
// CRS305 re-adopted at 198.51.100.100 on 2026-08-12: six hours of `unknown`
// while SNMP, port rates and spanning tree were all live.
func TestReplacingTheDeviceAtAnAddressClearsTheVerdict(t *testing.T) {
	var m = NewMonitor()
	m.SetConfig(tunnel.WatchConfig{Targets: []tunnel.WatchTarget{
		{DeviceID: "dev-old", IP: "198.51.100.100", Name: "CRS305-import"},
	}})
	// The first device is seen up.
	m.onReply("198.51.100.100")
	var snap = m.Snapshot()
	if len(snap) != 1 || snap[0].DeviceID != "dev-old" ||
		snap[0].Type != tunnel.EventDeviceUp {
		t.Fatalf("first device did not settle as up: %+v", snap)
	}

	// Same address, DIFFERENT device: the verdict must not be inherited.
	m.SetConfig(tunnel.WatchConfig{Targets: []tunnel.WatchTarget{
		{DeviceID: "dev-new", IP: "198.51.100.100", Name: "CRS305"},
	}})
	if snap = m.Snapshot(); len(snap) != 0 {
		t.Errorf("a snapshot claimed %+v about a device nothing has pinged yet — "+
			"the previous occupant's verdict was inherited", snap)
	}

	// And the next reply announces it, which is what reaches the controller.
	m.onReply("198.51.100.100")
	if snap = m.Snapshot(); len(snap) != 1 || snap[0].DeviceID != "dev-new" {
		t.Fatalf("the new device produced no verdict of its own: %+v", snap)
	}
}
