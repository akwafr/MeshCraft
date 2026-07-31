package api

import (
	"encoding/json"
	"testing"
	"time"

	"meshcraft/internal/driver"
	"meshcraft/internal/model"
	"meshcraft/internal/recon"
)

// deviceObserving builds a device whose observed state matches the description.
// A nil linkUp mimics a poll BEFORE link state was preserved: the degraded case
// that matters most, because it is the majority on a fleet already in service
// at update time.
func deviceObserving(t *testing.T, ports map[string]driver.PortState,
	linkUp map[string]bool) model.Device {
	t.Helper()
	var raw, err = json.Marshal(observedState{Ports: ports, LinkUp: linkUp})
	if err != nil {
		t.Fatalf("observedState: %v", err)
	}
	var at = time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC)
	return model.Device{
		ID: "d1", SiteID: "s1", Status: "ok", MgmtIP: "10.0.0.9",
		Observed: raw, ObservedAt: &at,
	}
}

// planTagged: management VLAN is expected TAGGED on an uplink.
var planTagged = model.MgmtPlan{Configured: true, VlanID: 99, SubnetV4: "10.9.0.0/24"}

// TestMgmtVLANPathAcceptsAccessOrTrunk: operator-picked VLAN may ride as native
// on an up access port OR tagged on an up trunk — either proves a path.
func TestMgmtVLANPathAcceptsAccessOrTrunk(t *testing.T) {
	var access = deviceObserving(t,
		map[string]driver.PortState{
			"ether1": {Mode: "access", PVID: 40},
		},
		map[string]bool{"ether1": true},
	)
	var state, reason = mgmtVLANPathOf(access, 40, false)
	if state != "ready" {
		t.Fatalf("access up = %q (%s), want ready", state, reason)
	}

	var trunk = deviceObserving(t,
		map[string]driver.PortState{
			"sfp1": {Mode: "trunk", PVID: 1, Tagged: []int{40, 99}},
		},
		map[string]bool{"sfp1": true},
	)
	state, reason = mgmtVLANPathOf(trunk, 40, false)
	if state != "ready" {
		t.Fatalf("trunk up = %q (%s), want ready", state, reason)
	}

	var down = deviceObserving(t,
		map[string]driver.PortState{
			"sfp1": {Mode: "trunk", PVID: 1, Tagged: []int{40}},
		},
		map[string]bool{"sfp1": false},
	)
	state, _ = mgmtVLANPathOf(down, 40, false)
	if state != "absent" {
		t.Fatalf("link down = %q, want absent", state)
	}
}

// TestMgmtPathAbsentSansLien: core safeguard. Management VLAN is configured on a
// port, but that port has no cable — it proves no path. Switching there would
// silence the device.
func TestMgmtPathAbsentSansLien(t *testing.T) {
	var d = deviceObserving(t,
		map[string]driver.PortState{
			"GE1": {Mode: "trunk", PVID: 1, Tagged: []int{99}},
		},
		map[string]bool{}, // no active link
	)
	var state, reason = mgmtPathOf(d, planTagged, false)
	if state != "absent" {
		t.Fatalf("VLAN on an uncabled port proves no path: %s — %s", state, reason)
	}
	if reason == "" {
		t.Error("a refusal must say why, otherwise it looks like a bug")
	}
}

// TestMgmtPathReadyAvecLien: an active uplink carrying the target VLAN is
// exactly what we want.
func TestMgmtPathReadyAvecLien(t *testing.T) {
	var d = deviceObserving(t,
		map[string]driver.PortState{
			"GE1":  {Mode: "access", PVID: 1},
			"GE48": {Mode: "trunk", PVID: 1, Tagged: []int{20, 99}},
		},
		map[string]bool{"GE48": true},
	)
	var state, _ = mgmtPathOf(d, planTagged, false)
	if state != "ready" {
		t.Errorf("active uplink carrying tagged VLAN 99 is a path: %s", state)
	}
}

// TestMgmtPathNatif: a "native" plan expects the VLAN as PVID, not in tagged
// VLANs. Looking in the wrong place would refuse a perfectly safe switch.
func TestMgmtPathNatif(t *testing.T) {
	var plan = model.MgmtPlan{Configured: true, VlanID: 99, Untagged: true}
	var d = deviceObserving(t,
		map[string]driver.PortState{"GE48": {Mode: "access", PVID: 99}},
		map[string]bool{"GE48": true},
	)
	var state, _ = mgmtPathOf(d, plan, false)
	if state != "ready" {
		t.Errorf("VLAN 99 native on an active port is a path: %s", state)
	}
	// Same port, but the plan expects tagged: that is NOT a path.
	state, _ = mgmtPathOf(d, planTagged, false)
	if state != "absent" {
		t.Errorf("native does not satisfy a tagged plan: %s", state)
	}
}

// TestMgmtPathInconnu: not knowing is not the same as knowing there is nothing.
// Turning ignorance into a block would lock down an entire fleet not yet re-read.
func TestMgmtPathInconnu(t *testing.T) {
	var neverRead = model.Device{ID: "d1", SiteID: "s1", Status: "ok"}
	var state, reason = mgmtPathOf(neverRead, planTagged, false)
	if state != "unknown" || reason == "" {
		t.Errorf("never-read device is indeterminate: %s — %s", state, reason)
	}

	// Poll before link preservation: link_up absent.
	var older = deviceObserving(t,
		map[string]driver.PortState{"GE48": {Mode: "trunk", Tagged: []int{99}}}, nil)
	state, _ = mgmtPathOf(older, planTagged, false)
	if state != "unknown" {
		t.Errorf("without link state, do not conclude: %s", state)
	}
}

// TestMigratableSuitLeChemin: an ABSENT path removes the migration offer; an
// INDETERMINATE path leaves it. This is the boolean form of "certainty blocks,
// ignorance does not".
func TestMigratableSuitLeChemin(t *testing.T) {
	// Outside the plan subnet: the case that proposes migration.
	var noPath = deviceObserving(t,
		map[string]driver.PortState{"GE1": {Mode: "trunk", Tagged: []int{99}}},
		map[string]bool{},
	)
	if conformityWith(noPath, planTagged).Migratable {
		t.Error("without uplink carrying target VLAN, migration must not be offered")
	}

	var withPath = deviceObserving(t,
		map[string]driver.PortState{"GE48": {Mode: "trunk", Tagged: []int{99}}},
		map[string]bool{"GE48": true},
	)
	if !conformityWith(withPath, planTagged).Migratable {
		t.Error("with active uplink carrying target VLAN, migration is offerable")
	}

	var indeterminate = model.Device{ID: "d1", SiteID: "s1", Status: "ok", MgmtIP: "10.0.0.9"}
	if !conformityWith(indeterminate, planTagged).Migratable {
		t.Error("indeterminate path must not forbid: that would block an entire fleet")
	}
}

// stalledDevice builds a device whose last apply is at applyAt and whose last
// intent change is at changedAt.
func stalledDevice(t *testing.T, applyAt, changedAt time.Time) model.Device {
	t.Helper()
	var raw, err = json.Marshal(map[string]any{"at": applyAt, "remaining": 8})
	if err != nil {
		t.Fatalf("last_apply: %v", err)
	}
	return model.Device{ID: "d1", SiteID: "s1", LastApply: raw, DesiredChangedAt: &changedAt}
}

// TestApplyStalled: distinguish "not yet applied" from "applied, but incomplete".
//
// Both show blue "in progress". The first resolves on its own in seconds; the
// second never will, because automatic apply only runs on an intent change.
// Confusing them leaves a half-configured device with no UI signal.
func TestApplyStalled(t *testing.T) {
	var t0 = time.Date(2026, 7, 27, 17, 52, 0, 0, time.UTC)
	var gaps = []recon.Change{{Port: "GE49", Field: "mode"}}

	// Apply ran AFTER the change and left gaps: stalled.
	var stalled = stalledDevice(t, t0.Add(30*time.Second), t0)
	if !applyStalled(stalled, gaps) {
		t.Error("apply that left gaps after intent change will not heal on its own")
	}

	// Intent changed AFTER apply: automatic apply will retry.
	var pending = stalledDevice(t, t0, t0.Add(30*time.Second))
	if applyStalled(pending, gaps) {
		t.Error("change after apply is a delay, not a stall")
	}

	// No gaps: nothing to report, whatever the timeline.
	if applyStalled(stalled, nil) {
		t.Error("with no gaps, nothing to report")
	}

	// Never applied: automatic apply has not run yet.
	var neverApplied = model.Device{ID: "d1", SiteID: "s1", DesiredChangedAt: &t0}
	if applyStalled(neverApplied, gaps) {
		t.Error("with no prior apply, there is no stall to report")
	}
}

// TestFacadeOfFiltreLesIfIndex: filter that keeps SNMP telemetry from inventing ports.
//
// Indexes 1000 and 100041 are real — seen on a 1930; they are VLAN interfaces and
// aggregates. "GE" + index used to become ports.
func TestFacadeOfFiltreLesIfIndex(t *testing.T) {
	var d = model.Device{
		PortGroups: []byte(`[{"prefix":"GE","from":1,"count":48},
		                     {"prefix":"GE","from":49,"count":4}]`),
	}
	var f = facadeOf(d)
	if len(f.ports) != 52 {
		t.Fatalf("facade counts 52 ports, not %d", len(f.ports))
	}
	if !f.ports["GE1"] || !f.ports["GE48"] || !f.ports["GE52"] {
		t.Error("real facade ports must be accepted")
	}
	var ghost string
	for _, ghost = range []string{"GE1000", "GE100041", "GE20000", "GE53", "GE0"} {
		if f.ports[ghost] {
			t.Errorf("%s is not a facade port", ghost)
		}
	}
}

// TestFacadeOfSansFacade: unreadable facade falls back to known ports. Keep
// refreshing what exists; never create new ports.
func TestFacadeOfSansFacade(t *testing.T) {
	var d = model.Device{
		PortGroups: []byte(`nimportequoi`),
		Ports:      []byte(`{"GE1":{"mode":"access"},"GE2":{"mode":"libre"}}`),
	}
	var f = facadeOf(d)
	if !f.ports["GE1"] || !f.ports["GE2"] {
		t.Error("without readable facade, known ports remain refreshable")
	}
	if f.ports["GE100041"] {
		t.Error("unknown port must never be created by telemetry")
	}
}

// TestFacadeResolveParLibelle: SNMP label to front-panel port mapping, replacing
// "prefix + ifIndex" arithmetic.
func TestFacadeResolveParLibelle(t *testing.T) {
	// Instant On: ifDescr is a bare number; the facade names "GE1".
	var instantOn = facadeOf(model.Device{
		PortGroups: []byte(`[{"prefix":"GE","from":1,"count":52}]`),
	})
	if instantOn.resolve("1") != "GE1" || instantOn.resolve("52") != "GE52" {
		t.Errorf("bare number must attach to prefixed port: %q", instantOn.resolve("1"))
	}
	if instantOn.resolve("TRK1") != "" || instantOn.resolve("100") != "" {
		t.Error("aggregate or out-of-facade port must not attach")
	}

	// MikroTik: ifDescr IS the front-panel name, including multiple prefixes.
	var mikrotik = facadeOf(model.Device{
		PortGroups: []byte(`[{"prefix":"ether","count":1},
		                     {"prefix":"sfp-sfpplus","count":16}]`),
	})
	if mikrotik.resolve("ether1") != "ether1" {
		t.Errorf("exact match expected: %q", mikrotik.resolve("ether1"))
	}
	if mikrotik.resolve("sfp-sfpplus9") != "sfp-sfpplus9" {
		t.Errorf("second prefix not recognized: %q", mikrotik.resolve("sfp-sfpplus9"))
	}
	// Punctuation and case vary across firmware versions.
	if mikrotik.resolve("SFP-SFPplus 9") != "sfp-sfpplus9" {
		t.Errorf("normalized form must match case and punctuation: %q",
			mikrotik.resolve("SFP-SFPplus 9"))
	}
	if mikrotik.resolve("") != "" || mikrotik.resolve("bridge") != "" {
		t.Error("unknown label must be dropped, never invented")
	}
}

// TestFacadeResolveAmbigu: two ports that normalization conflates can no longer
// be disambiguated. Random matching is worse than no match.
func TestFacadeResolveAmbigu(t *testing.T) {
	var f = facadeOf(model.Device{
		Ports: []byte(`{"ether-1":{"mode":"access"},"ether 1":{"mode":"access"}}`),
	})
	if f.resolve("ETHER1") != "" {
		t.Errorf("ambiguous normalized form must attach to nothing: %q", f.resolve("ETHER1"))
	}
	// EXACT equality remains possible: it carries no risk.
	if f.resolve("ether-1") != "ether-1" {
		t.Error("exact equality must remain valid despite normalized ambiguity")
	}
}
