package api

import (
	"testing"

	"meshcraft/internal/model"
	"meshcraft/internal/tunnel"
)

// Device ↔ proxy attachment, from the target-building side.
//
// What these tests lock down is ARBITRATION, and it is asymmetric: attachment
// restricts SNMP collection and does NOT restrict ping. The two halves break
// separately, and both breaks are silent — filtering too much loses reachability
// failover (a device attached to a down proxy would stop being pinged by others,
// so it would stay green), filtering too little brings back duplicate port
// transitions and an indeterminate trap destination. Neither makes anything
// else fail.
//
// No database, like the rest of the package. Tests call `selectWatch`, which IS
// production code — not a transcription of its flow: wiring both lists is
// included, and that is exactly where the bug would be invisible. What a test
// in this package cannot cover: that `DevicesWatchedBy` projects `Collector`
// correctly (its decision table is replayed in store/devices_test.go), the
// inside of snmpWatchesFor, which reads the database and vault, and the few
// watchConfigFor assignments that connect the two.

const (
	// thisProxy: the RECIPIENT proxy of the list, the one we compute for.
	thisProxy = "proxy-agency-a"
	// otherProxy: a second proxy covering the same site — the ordinary case as
	// soon as an instance "Local" coexists with a remote proxy.
	otherProxy = "proxy-local"
)

// watchedFor builds a row as `DevicesWatchedBy` returns it to proxy `dest`.
//
// `Collector` is TRANSCRIBED, not recomputed by the layer under test: the query
// projects it (`d.proxy_id is null or d.proxy_id = p.id`), and what we exercise
// here is what the API DOES with it. Empty `attached` = no attachment, i.e.
// the whole fleet before migration 0022.
func watchedFor(dest, id, ip, attached string) model.WatchedDevice {
	return model.WatchedDevice{
		ID: id, SiteID: "site-a", Name: id, MgmtIP: ip,
		ProxyID:   attached,
		Collector: attached == "" || attached == dest,
	}
}

// icmpIDs reduces ICMP targets to their identifiers, for readable comparisons.
func icmpIDs(targets []tunnel.WatchTarget) map[string]bool {
	var out = map[string]bool{}
	var t tunnel.WatchTarget
	for _, t = range targets {
		out[t.DeviceID] = true
	}
	return out
}

// collectIDs: the same for collection targets.
func collectIDs(devices []model.WatchedDevice) map[string]bool {
	var out = map[string]bool{}
	var d model.WatchedDevice
	for _, d = range devices {
		out[d.ID] = true
	}
	return out
}

// TestCollectionUnchangedWithoutAttachment: THE non-regression test for this change.
//
// No attachment exists in a fleet that never declared one — that is every
// fleet's state at migration 0022 — and it must keep being collected by every
// proxy that covers it, exactly as before. A filter placed one line too high
// (on ROWS instead of SNMP targets) would shut off collection for an entire
// fleet on first deploy, with no message.
func TestCollectionUnchangedWithoutAttachment(t *testing.T) {
	var dest string
	for _, dest = range []string{thisProxy, otherProxy} {
		t.Run("seen from "+dest, func(t *testing.T) {
			var eligible = []model.WatchedDevice{
				watchedFor(dest, "sw-1", "10.0.0.1", ""),
				watchedFor(dest, "sw-2", "10.0.0.2", ""),
			}
			var sel = selectWatch(eligible, nil)
			if len(sel.targets) != 2 {
				t.Errorf("both devices must be pinged, got %d", len(sel.targets))
			}
			if len(sel.collect) != 2 {
				t.Fatalf("both devices must be collected by every covering proxy, got %d",
					len(sel.collect))
			}
			if sel.delegated != 0 {
				t.Errorf("nothing delegated without attachment, got %d", sel.delegated)
			}
		})
	}
}

// TestCollectionAttachedToThisProxy: attachment designates this proxy, it collects.
// This is the useful meaning of the gesture, and verifying it separately from
// "attached elsewhere" avoids an inverted filter passing the other two tests.
func TestCollectionAttachedToThisProxy(t *testing.T) {
	var eligible = []model.WatchedDevice{
		watchedFor(thisProxy, "sw-1", "10.0.0.1", thisProxy),
	}
	var sel = selectWatch(eligible, nil)
	if len(sel.targets) != 1 || sel.targets[0].DeviceID != "sw-1" {
		t.Errorf("an attached device stays an ICMP target for its collector: %+v", sel.targets)
	}
	if len(sel.collect) != 1 || sel.collect[0].ID != "sw-1" {
		t.Fatalf("the designated collector must poll its device: %+v", sel.collect)
	}
	if sel.delegated != 0 {
		t.Errorf("nothing delegated when this proxy IS the collector, got %d", sel.delegated)
	}
}

// TestCollectionAttachedElsewhereStaysOnICMP: arbitration asymmetry, in one
// test because it is one fact.
//
// Seen from this proxy, the device leaves collection — another proxy polls it,
// and two collectors would duplicate every port transition with no way to write
// a trap destination. It stays pinged: two pings cost nothing, and restricting
// ping to the collector would hide the device's reachability the day that
// collector goes down — i.e. the day you need it.
func TestCollectionAttachedElsewhereStaysOnICMP(t *testing.T) {
	var eligible = []model.WatchedDevice{
		watchedFor(thisProxy, "sw-1", "10.0.0.1", otherProxy),
		watchedFor(thisProxy, "sw-2", "10.0.0.2", ""),
	}
	var sel = selectWatch(eligible, nil)

	if !icmpIDs(sel.targets)["sw-1"] {
		t.Errorf("a device attached elsewhere must REMAIN an ICMP target: %+v", sel.targets)
	}
	if len(sel.targets) != 2 {
		t.Errorf("attachment must not restrict ping: want 2 targets, got %d", len(sel.targets))
	}
	if collectIDs(sel.collect)["sw-1"] {
		t.Errorf("a device attached elsewhere must NOT be collected here: %+v", sel.collect)
	}
	if !collectIDs(sel.collect)["sw-2"] {
		t.Errorf("the unattached device must stay collected: %+v", sel.collect)
	}
	if sel.delegated != 1 {
		t.Errorf("one delegated collection expected in the tally, got %d", sel.delegated)
	}
}

// TestNetscopeChainPreservesAttachment: the case that motivated this change.
//
// The old path rebuilt a `model.WatchedDevice` from the retained netscope
// candidate, and `netscope.WatchCandidate` does not carry attachment:
// `Collector` therefore fell back to false for everyone after netscope, and
// no device was collected anymore. The test crosses both sorts, because the
// crossing was wrong.
//
// The setup is netscope's: two devices from two different sites at the SAME
// address, only one with its address domain declared on this proxy. That one
// wins ping — and it is attached to this proxy, so it must also win collection.
func TestNetscopeChainPreservesAttachment(t *testing.T) {
	var winner = watchedFor(thisProxy, "sw-here", "192.168.1.10", thisProxy)
	winner.AddressDomainID = "dom-a"
	var loser = watchedFor(thisProxy, "sw-there", "192.168.1.10", "")
	loser.AddressDomainID = "dom-b"

	var sel = selectWatch([]model.WatchedDevice{winner, loser},
		map[string]bool{"dom-a": true})

	if len(sel.targets) != 1 || sel.targets[0].DeviceID != "sw-here" {
		t.Fatalf("netscope must keep the only legitimate device: %+v", sel.targets)
	}
	if len(sel.collect) != 1 || sel.collect[0].ID != "sw-here" {
		t.Fatalf("attachment must survive address sorting: %+v", sel.collect)
	}
	// The returned row is the store row, not a reconstruction: fields that
	// `WatchCandidate` does not carry are present. That is what makes the path
	// insensitive to the next field added to `WatchedDevice`.
	if sel.collect[0].SiteID != "site-a" || sel.collect[0].ProxyID != thisProxy ||
		!sel.collect[0].Collector {
		t.Errorf("the original row must be returned as-is: %+v", sel.collect[0])
	}
	// The loser is rejected for an ADDRESS reason, not policy: it appears neither
	// in ICMP targets nor in the delegated collection counter, otherwise the
	// journal tally would no longer be a correct subtraction.
	if sel.delegated != 0 {
		t.Errorf("an address rejection is not delegated collection, got %d", sel.delegated)
	}
	// And it is rejected WITH its reason: that reason feeds the device page banner
	// (cf. watchexcl.go), and attachment sorting must not get in the way.
	if len(sel.rejected) != 1 || sel.rejected[0].DeviceID != "sw-there" ||
		sel.rejected[0].Reason == "" {
		t.Errorf("the loser must be rejected with its reason: %+v", sel.rejected)
	}
}

// TestWatchTallySubtracts: the invariant the PushWatchTo journal line asserts —
// devices = snmp_targets + delegated_collection + without_snmp_identity.
//
// Only its first two terms are verified here, the only ones that need neither
// database nor vault: len(ICMP) = len(collection) + delegated. The third is
// taken by difference in watchConfigFor, so it holds by construction once this
// one does. That is what lets the operator read a journal line as a subtraction
// rather than three numbers that do not overlap.
func TestWatchTallySubtracts(t *testing.T) {
	var eligible = []model.WatchedDevice{
		watchedFor(thisProxy, "sw-1", "10.0.0.1", ""),
		watchedFor(thisProxy, "sw-2", "10.0.0.2", otherProxy),
		watchedFor(thisProxy, "sw-3", "10.0.0.3", thisProxy),
		watchedFor(thisProxy, "sw-4", "10.0.0.4", otherProxy),
	}
	var sel = selectWatch(eligible, nil)
	if len(sel.targets) != 4 {
		t.Fatalf("all four devices are pinged, got %d", len(sel.targets))
	}
	if len(sel.collect)+sel.delegated != len(sel.targets) {
		t.Errorf("incoherent tally: %d pinged, %d collected, %d delegated",
			len(sel.targets), len(sel.collect), sel.delegated)
	}
	if sel.delegated != 2 {
		t.Errorf("two devices are collected elsewhere, got %d", sel.delegated)
	}
}
