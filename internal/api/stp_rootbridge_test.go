package api

import (
	"testing"

	"meshcraft/internal/driver"
	"meshcraft/internal/model"
)

// THE RULE THAT TURNS A DESIGNATION INTO CONFIGURATION.
//
// Until 2026-08-12 the site's root-bridge plan had no reader outside its own two
// handlers: designating a switch stored a row and drew a screen, and the device
// was never told. These assertions are about the wiring that closed that, and
// each one guards a failure that is SILENT on every screen — a switch written to
// priority 0 that nobody asked for, or a designated root that was never written
// and whose tree is therefore decided by the lowest MAC address on the site.

const rootA = "11111111-1111-1111-1111-111111111111"
const rootB = "22222222-2222-2222-2222-222222222222"

func TestDesignatedDeviceBecomesRootAndOthersDoNot(t *testing.T) {
	var plan = driver.RootPlan{Roots: []driver.RootIntent{{DeviceID: rootA}}}

	var isRoot, instances, filter = rootDesignationFrom(plan, rootA, nil)
	if !isRoot {
		t.Error("the designated device must be made root — this is the whole feature")
	}
	// No instances named: the connector reads that as the whole region (cf.
	// SpanningTreePlan.RootsOf), which is what "root bridge" means to whoever
	// ticked it.
	if len(instances) != 0 {
		t.Errorf("instances = %v, want none carried through", instances)
	}
	// ONE root needs no BPDU filtering, and that is the property that keeps the
	// dangerous half of this feature inert on an ordinary site: filtering is what
	// can cause a broadcast storm rather than prevent one.
	if len(filter) != 0 {
		t.Errorf("a single root must filter nothing, got %v", filter)
	}

	isRoot, _, _ = rootDesignationFrom(plan, rootB, nil)
	if isRoot {
		t.Error("a device the site did not designate must never be written to " +
			"priority 0: it would win an election nobody asked it to win")
	}
}

// A designation that NAMES instances is carried through verbatim — that is how
// an operator says "root of instance 2 only" on a region that load-balances.
func TestNamedInstancesTravelToTheConnector(t *testing.T) {
	var plan = driver.RootPlan{Roots: []driver.RootIntent{
		{DeviceID: rootA, Instances: []int{0, 2}},
	}}
	var _, instances, _ = rootDesignationFrom(plan, rootA, nil)
	if len(instances) != 2 || instances[0] != 0 || instances[1] != 2 {
		t.Errorf("instances = %v, want [0 2]", instances)
	}
}

// TWO ROOTS ON ONE INSTANCE is the case BPDU filtering exists for, and the ports
// must reach the connector — which writes them BEFORE any root priority and
// abandons the plan if they fail (driver.RootApplyOrder). A designation that
// contested an instance and carried no filter ports would put two bridges at
// priority 0 on one tree with BPDUs still flowing.
func TestContestedInstanceCarriesTheFilterPorts(t *testing.T) {
	var plan = driver.RootPlan{Roots: []driver.RootIntent{
		{DeviceID: rootA}, {DeviceID: rootB},
	}}
	var links = []driver.Link{
		{ADevice: rootA, APort: "ET48", BDevice: rootB, BPort: "ET1"},
	}
	var isRoot, _, filter = rootDesignationFrom(plan, rootA, links)
	if !isRoot {
		t.Fatal("still a root when contested")
	}
	if len(filter) != 1 || filter[0] != "ET48" {
		t.Errorf("filter ports = %v, want the local end of the inter-root path", filter)
	}
	// Both ends are cut, which is what separates the two trees rather than half
	// of one.
	_, _, filter = rootDesignationFrom(plan, rootB, links)
	if len(filter) != 1 || filter[0] != "ET1" {
		t.Errorf("far side filter = %v, want ET1", filter)
	}
}

// THE SAFEGUARD THAT CAME OUT OF A FLEET-WIDE INCIDENT (2026-08-12).
//
// Importing a CRS305's configuration created seven VLANs the site lacked, and
// every OTHER switch immediately went to `drift` — "configuration applied
// incompletely" across the whole site. Nothing was broken: `tagged: "all"` means
// "every VLAN the site declares", so seven new VLANs redefined every full trunk
// at once. Eight devices, forty-eight full trunks on core01 alone, from one
// adoption.
//
// What is pinned here is the COUNT that decides it. A site where no other device
// carries a full trunk can gain VLANs safely; one where they do must not have
// them added behind the operator's back.
func TestFullTrunkPortsElsewhereCountsOnlyOtherDevices(t *testing.T) {
	var full = map[string]any{"mode": "trunk", "tagged": "all"}
	var listed = map[string]any{"mode": "trunk", "tagged": []int{42, 99}}
	var access = map[string]any{"mode": "access", "pvid": 100}

	var me = dev(rootA, "ok", map[string]any{"GE1": full, "GE2": full})
	var other = dev(rootB, "ok", map[string]any{"XG1": full, "GE3": listed, "GE4": access})

	// The device being imported is EXCLUDED: its own trunks are about to be
	// replaced by what it actually carries, so they cannot be surprised.
	var n = countFullTrunks([]model.Device{me, other}, rootA)
	if n != 1 {
		t.Errorf("counted %d full trunks elsewhere, want 1 (only the other device's "+
			"XG1 — a listed trunk and an access port are unaffected by a new VLAN)", n)
	}
	// With nothing else on the site, creation is safe and must stay allowed —
	// this is adoption into a fresh site, which is what the feature is for.
	if n = countFullTrunks([]model.Device{me}, rootA); n != 0 {
		t.Errorf("counted %d on a site holding only the imported device, want 0", n)
	}
}
