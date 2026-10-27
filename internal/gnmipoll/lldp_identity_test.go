package gnmipoll

import "testing"

// THE AGEOUT-THEN-RELEARN DEFECT: a neighbour that comes back must come back
// NAMED.
//
// Reproduced 3/3 on core01.lab (EOS 4.28.13.1M) on 2026-08-11 by toggling
// the far end's `lldp transmit`, and the residue was always the same shape:
// `port-description`, `system-name` and `system-description` present,
// `chassis-id` and `port-id` EMPTY, for the whole life of the subscription. A
// proxy restart cleared it — the replayed initial dump carries every leaf — and
// the next ageout brought it back.
//
// The sequence below is that reproduction, and it costs nothing to replay: the
// device deletes the list entry when the neighbour ages out, and re-creates it
// on the relearn with only the leaves it considers to have CHANGED. The identity
// of a neighbour that came back on the same port has not changed, so the device
// — having already published it — never sends it again.
//
// What that residue costs is not cosmetic: `identifyPeer` falls through chassis
// → port-id → system-name and comes out with nothing, so one cable is drawn as
// TWO one-sided links, and the row still OCCUPIES the local port, which is what
// makes that endpoint permanently unarbitrable.
func TestAgeoutAndRelearnKeepTheNeighbourIdentity(t *testing.T) {
	var tbl = NewTable("core01")
	var base = "/lldp/interfaces/interface[name=Ethernet35]/neighbors/" +
		"neighbor[id=1]"

	// The initial dump, which carries every leaf.
	tbl.Fold(Notification{Update: []Update{
		{Path: mustPath(base + "/state/chassis-id"),
			Value: StringValue("64:9d:99:22:7b:95")},
		{Path: mustPath(base + "/state/port-id"), Value: StringValue("TGi0/2")},
		{Path: mustPath(base + "/state/port-description"),
			Value: StringValue("TGigaEthernet0/2")},
		{Path: mustPath(base + "/state/system-name"),
			Value: StringValue("poefs3400lab")},
		{Path: mustPath(base + "/state/system-description"),
			Value: StringValue("FS S3400-48T4SP")},
	}})

	// The ageout: the device drops the list entry, on its own, 120 s after the
	// far end stopped transmitting. Honouring it is correct and stays correct.
	tbl.Fold(Notification{Delete: []Path{mustPath(base)}})
	if len(tbl.Report().Neighbors) != 0 {
		t.Fatal("the neighbour survived its own ageout: an unplugged cable would " +
			"be re-pushed for ever")
	}

	// The relearn. ON_CHANGE, so only what the device thinks is new: the identity
	// is not repeated.
	tbl.Fold(Notification{Update: []Update{
		{Path: mustPath(base + "/state/port-description"),
			Value: StringValue("TGigaEthernet0/2")},
		{Path: mustPath(base + "/state/system-name"),
			Value: StringValue("poefs3400lab")},
		{Path: mustPath(base + "/state/system-description"),
			Value: StringValue("FS S3400-48T4SP")},
	}})

	var report = tbl.Report()
	if len(report.Neighbors) != 1 {
		t.Fatalf("%d neighbours after the relearn, want 1", len(report.Neighbors))
	}
	var nb = report.Neighbors[0]
	if nb.ChassisID != "64:9d:99:22:7b:95" {
		t.Errorf("chassis-id = %q, want the one the entry carried before the "+
			"ageout: the peer is unidentifiable without it and the cable is drawn "+
			"as two one-sided links", nb.ChassisID)
	}
	if nb.PortID != "TGi0/2" {
		t.Errorf("port-id = %q, want the one the entry carried before the ageout",
			nb.PortID)
	}
	if nb.SysName != "poefs3400lab" || nb.LocalPort != "Ethernet35" {
		t.Errorf("the leaves the device DID repeat were mangled: %+v", nb)
	}
}

// The same loss, compressed into ONE notification — the re-creation shape gNMI
// mandates be read deletes-first ([Table.Fold]). The ordering fix put the row
// back; it did not put the identity back, and that is the difference between
// that bug and this one.
func TestARecreationInOneNotificationKeepsTheIdentity(t *testing.T) {
	var tbl = NewTable("core01")
	var base = "/lldp/interfaces/interface[name=Ethernet37]/neighbors/" +
		"neighbor[id=1]"
	tbl.Fold(Notification{Update: []Update{
		{Path: mustPath(base + "/state/chassis-id"),
			Value: StringValue("9c:05:d6:f5:f1:37")},
		{Path: mustPath(base + "/state/port-id"), Value: StringValue("0/18")},
	}})

	tbl.Fold(Notification{
		Delete: []Path{mustPath(base)},
		Update: []Update{
			{Path: mustPath(base + "/state/system-name"),
				Value: StringValue("unifimoule1")},
		},
	})

	var report = tbl.Report()
	if len(report.Neighbors) != 1 {
		t.Fatalf("%d neighbours, want 1", len(report.Neighbors))
	}
	if report.Neighbors[0].ChassisID != "9c:05:d6:f5:f1:37" ||
		report.Neighbors[0].PortID != "0/18" {
		t.Errorf("the re-created entry lost its identity: %+v", report.Neighbors[0])
	}
}

// A LEAF THIS TABLE DOES NOT RECORD MUST NOT CREATE THE ROW.
//
// `age`, `ttl`, `last-update` and `chassis-id-type` tick on their own, so after a
// delete the very next housekeeping update used to resurrect the neighbour as an
// EMPTY row — one with no chassis id, no port id and no name — which then held
// the local port against every arbitration rule until the proxy was restarted.
// That is the same visible fault as the residue above, reached without any
// relearn at all.
func TestAnUnrecordedLeafDoesNotResurrectANeighbour(t *testing.T) {
	var tbl = NewTable("core01")
	var base = "/lldp/interfaces/interface[name=Ethernet48]/neighbors/" +
		"neighbor[id=2]"
	tbl.Fold(Notification{Delete: []Path{mustPath(base)}})

	tbl.Fold(Notification{Update: []Update{
		{Path: mustPath(base + "/state/chassis-id-type"),
			Value: StringValue("MAC_ADDRESS")},
	}})

	if len(tbl.Report().Neighbors) != 0 {
		t.Errorf("a leaf this table does not record created a nameless neighbour: "+
			"%+v", tbl.Report().Neighbors)
	}
}

// AN EMPTY STRING IS NOT AN OBSERVATION. Chassis id and port id are LLDP's two
// mandatory TLVs, so no device ever observed a neighbour without them; a device
// says a neighbour is gone with a delete. Letting "" through would erase the only
// thing that names the peer, which is the second way the residue can be born.
func TestAnEmptyLeafDoesNotEraseAKnownIdentity(t *testing.T) {
	var tbl = NewTable("core01")
	var base = "/lldp/interfaces/interface[name=Ethernet34]/neighbors/" +
		"neighbor[id=1]"
	tbl.Fold(Notification{Update: []Update{
		{Path: mustPath(base + "/state/chassis-id"),
			Value: StringValue("bc:c4:93:e5:7f:80")},
		{Path: mustPath(base + "/state/port-id"), Value: StringValue("Te1/0/2")},
	}})

	if tbl.Fold(Notification{Update: []Update{
		{Path: mustPath(base + "/state/chassis-id"), Value: StringValue("")},
		{Path: mustPath(base + "/state/port-id"), Value: StringValue("")},
	}}) {
		t.Error("blanking a mandatory TLV was reported as a change")
	}
	var nb = tbl.Report().Neighbors[0]
	if nb.ChassisID != "bc:c4:93:e5:7f:80" || nb.PortID != "Te1/0/2" {
		t.Errorf("an empty update erased the identity: %+v", nb)
	}
}

// The same rule on the device's OWN identity, which `buildLLDPIdentity` indexes
// the whole fleet on.
func TestAnEmptySelfLeafDoesNotEraseTheDeviceIdentity(t *testing.T) {
	var tbl = NewTable("core01")
	tbl.Fold(Notification{Update: []Update{
		{Path: mustPath("/lldp/state/chassis-id"),
			Value: StringValue("28:99:3a:9d:b7:65")},
		{Path: mustPath("/lldp/state/system-name"),
			Value: StringValue("core01-lab")},
	}})
	tbl.Fold(Notification{Update: []Update{
		{Path: mustPath("/lldp/state/chassis-id"), Value: StringValue("")},
		{Path: mustPath("/lldp/state/system-name"), Value: StringValue("")},
	}})
	var report = tbl.Report()
	if report.ChassisID != "28:99:3a:9d:b7:65" || report.SysName != "core01-lab" {
		t.Errorf("the device unnamed itself: chassis=%q sysname=%q",
			report.ChassisID, report.SysName)
	}
}

// WHAT MUST NOT HAPPEN: the memory must never put a row back by itself, and a
// DIFFERENT neighbour taking the port over must not inherit the old one's
// identity. Both mandatory TLVs have genuinely changed in that case, so the
// device states them and they overwrite what was remembered.
func TestARememberedIdentityNeitherResurrectsNorSticks(t *testing.T) {
	var tbl = NewTable("core01")
	var base = "/lldp/interfaces/interface[name=Ethernet35]/neighbors/" +
		"neighbor[id=1]"
	tbl.Fold(Notification{Update: []Update{
		{Path: mustPath(base + "/state/chassis-id"),
			Value: StringValue("64:9d:99:22:7b:95")},
		{Path: mustPath(base + "/state/port-id"), Value: StringValue("TGi0/2")},
	}})
	tbl.Fold(Notification{Delete: []Path{mustPath(base)}})

	// Nothing arrives. The cable is out and it must stay out, however many push
	// cycles go by.
	if len(tbl.Report().Neighbors) != 0 {
		t.Fatal("a remembered neighbour came back on its own: that is the " +
			"unplugged cable re-asserted for ever that Table.Delete exists to stop")
	}

	// A different device is patched in and states its own identity.
	tbl.Fold(Notification{Update: []Update{
		{Path: mustPath(base + "/state/chassis-id"),
			Value: StringValue("bc:c4:93:e5:7f:80")},
		{Path: mustPath(base + "/state/port-id"), Value: StringValue("Te1/0/2")},
	}})
	var nb = tbl.Report().Neighbors[0]
	if nb.ChassisID != "bc:c4:93:e5:7f:80" || nb.PortID != "Te1/0/2" {
		t.Errorf("the new occupant inherited the old one's identity: %+v", nb)
	}
}

// A resubscribe throws the memory away with everything else: the replayed initial
// dump is the one moment the two views are known to agree, and a remembered value
// surviving it could refill a row the dump has just contradicted.
func TestResetForgetsTheDepartedNeighbours(t *testing.T) {
	var tbl = NewTable("core01")
	var base = "/lldp/interfaces/interface[name=Ethernet35]/neighbors/" +
		"neighbor[id=1]"
	tbl.Fold(Notification{Update: []Update{
		{Path: mustPath(base + "/state/chassis-id"),
			Value: StringValue("64:9d:99:22:7b:95")},
	}})
	tbl.Fold(Notification{Delete: []Path{mustPath(base)}})
	tbl.Reset()

	tbl.Fold(Notification{Update: []Update{
		{Path: mustPath(base + "/state/system-name"),
			Value: StringValue("poefs3400lab")},
	}})
	if tbl.Report().Neighbors[0].ChassisID != "" {
		t.Error("a chassis id survived a resubscribe and refilled a row the " +
			"replayed initial state had not restated")
	}
}

// A delete of the whole INTERFACE — what arrives when a port goes down — takes
// every neighbour on it, and remembers each, so that a port coming back up does
// not have to re-state identities the device considers unchanged.
func TestAnInterfaceDeleteRemembersEveryNeighbourOnIt(t *testing.T) {
	var tbl = NewTable("core01")
	var iface = "/lldp/interfaces/interface[name=Ethernet35]"
	var one = iface + "/neighbors/neighbor[id=1]"
	var two = iface + "/neighbors/neighbor[id=2]"
	tbl.Fold(Notification{Update: []Update{
		{Path: mustPath(one + "/state/chassis-id"), Value: StringValue("aa:aa")},
		{Path: mustPath(two + "/state/chassis-id"), Value: StringValue("bb:bb")},
	}})
	tbl.Fold(Notification{Delete: []Path{mustPath(iface)}})
	if len(tbl.Report().Neighbors) != 0 {
		t.Fatal("an interface delete left neighbours behind")
	}

	tbl.Fold(Notification{Update: []Update{
		{Path: mustPath(one + "/state/system-name"), Value: StringValue("a")},
		{Path: mustPath(two + "/state/system-name"), Value: StringValue("b")},
	}})
	var report = tbl.Report()
	if len(report.Neighbors) != 2 {
		t.Fatalf("%d neighbours back, want 2", len(report.Neighbors))
	}
	if report.Neighbors[0].ChassisID != "aa:aa" ||
		report.Neighbors[1].ChassisID != "bb:bb" {
		t.Errorf("the two neighbours lost their identities: %+v", report.Neighbors)
	}
}
