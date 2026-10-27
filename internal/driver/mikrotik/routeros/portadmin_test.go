package routeros

import (
	"strings"
	"testing"
)

// foldInterfaces on the REAL capture: 29 records, and the three that are not ports are
// exactly the ones this applier must refuse (`bridge`, `lo`, `vlan99`).
func TestFoldInterfacesFromCapture(t *testing.T) {
	var ifaces = foldInterfaces(fixture(t, "interface.json"))
	if len(ifaces) != 29 {
		t.Fatalf("29 interfaces in the capture, folded %d", len(ifaces))
	}
	var ether1, ok = ifaces["ether1"]
	if !ok {
		t.Fatal("ether1 must be present")
	}
	if ether1.Type != etherType {
		t.Errorf("ether1 type = %q, want %q", ether1.Type, etherType)
	}
	if ether1.Disabled {
		t.Error("every row of the capture carries `disabled: false`: nothing is shut on the " +
			"reference device")
	}
	if ether1.ID == "" {
		t.Error("the `.id` is what a write names the row with, and it must come from the read")
	}
	// The interfaces that are NOT physical ports, captured on the same device.
	var name string
	for _, name = range []string{"bridge", "lo", "vlan99"} {
		var row = ifaces[name]
		if row.Type == etherType {
			t.Errorf("%s reads as an ethernet port (%q) — disabling it would be far wider than "+
				"shutting a port", name, row.Type)
		}
	}
	// `running` is the link, and only one physical port has one on this device.
	if !ifaces["sfp-sfpplus2"].Running {
		t.Error("sfp-sfpplus2 is the one linked port of the capture")
	}
	if ifaces["ether1"].Running {
		t.Error("ether1 has no cable in the capture")
	}
}

// A row with no `.id` cannot be written, so it is dropped rather than carried: keeping
// it would let the applier treat a port it cannot touch as already in the wanted state.
// CONSTRUCTED — every row of the real capture has an `.id`.
func TestFoldInterfacesDropsRowsWithoutID(t *testing.T) {
	var rows = []Row{
		{"name": "ether1", ".id": "*1", "type": "ether"},
		{"name": "ether2", "type": "ether"},
	}
	var ifaces = foldInterfaces(rows)
	var _, kept = ifaces["ether2"]
	if kept {
		t.Error("a record with no `.id` must not be reported as writable")
	}
	if len(ifaces) != 1 {
		t.Errorf("folded %d", len(ifaces))
	}
}

// resolveMgmtPath on the REAL capture: the address is on `vlan99`, a VLAN interface
// over the BRIDGE, VLAN 99. So no single physical port IS administration — the
// membership test is what protects the uplink.
func TestResolveMgmtPathVLANOverBridge(t *testing.T) {
	var st = stateFromFixtures(t)
	var ifaces = foldInterfaces(fixture(t, "interface.json"))
	var path = resolveMgmtPath(st, fixture(t, "interface_vlan.json"), ifaces)

	if path.Interface != "vlan99" || path.VLAN != 99 {
		t.Fatalf("path = %+v — the capture has 192.0.2.54/24 on vlan99, vlan-id 99", path)
	}
	if path.PhysicalPort != "" {
		t.Errorf("vlan99's parent is the bridge, not a port: PhysicalPort = %q", path.PhysicalPort)
	}
	if path.AnyBridgePort {
		t.Error("the VLAN is known (99): the guardrail must use membership, not spare everything")
	}
	if path.Unknown {
		t.Error("administration was identified: nothing is unknown here")
	}
}

// THE CASE A MEMBERSHIP-ONLY GUARDRAIL MISSES: the address sits DIRECTLY on a physical
// port. There is no VLAN to test, and the port itself is administration. CONSTRUCTED —
// the reference device does not have this shape, which is exactly why it needs a test.
func TestResolveMgmtPathAddressOnAPhysicalPort(t *testing.T) {
	var st = DeviceState{MgmtInterface: "ether1", BridgeName: "bridge"}
	var ifaces = foldInterfaces([]Row{
		{"name": "ether1", ".id": "*1", "type": "ether"},
		{"name": "bridge", ".id": "*2", "type": "bridge"},
	})
	var path = resolveMgmtPath(st, nil, ifaces)
	if path.PhysicalPort != "ether1" {
		t.Fatalf("PhysicalPort = %q, want ether1: the address is on the port itself", path.PhysicalPort)
	}
}

// A VLAN interface whose PARENT is a physical port: same hazard one level down.
// CONSTRUCTED.
func TestResolveMgmtPathVLANOverAPhysicalPort(t *testing.T) {
	var st = DeviceState{MgmtInterface: "vlan99", MgmtVLAN: 99, BridgeName: "bridge"}
	var ifaces = foldInterfaces([]Row{
		{"name": "ether5", ".id": "*5", "type": "ether"},
		{"name": "vlan99", ".id": "*9", "type": "vlan"},
		{"name": "bridge", ".id": "*2", "type": "bridge"},
	})
	var vlanRows = []Row{{"name": "vlan99", "interface": "ether5", "vlan-id": "99"}}
	var path = resolveMgmtPath(st, vlanRows, ifaces)
	if path.PhysicalPort != "ether5" {
		t.Fatalf("PhysicalPort = %q, want ether5 — shutting it takes the VLAN interface with it",
			path.PhysicalPort)
	}
}

// The address on the BRIDGE with no readable PVID: administration arrives untagged and
// ANY bridge port may carry it. CONSTRUCTED — on the real device state.go resolves the
// PVID into MgmtVLAN, which is the branch above.
func TestResolveMgmtPathUntaggedOnBridgeSparesEveryBridgePort(t *testing.T) {
	var st = DeviceState{MgmtInterface: "bridge", BridgeName: "bridge", MgmtVLAN: 0}
	var ifaces = foldInterfaces([]Row{{"name": "bridge", ".id": "*2", "type": "bridge"}})
	var path = resolveMgmtPath(st, nil, ifaces)
	if !path.AnyBridgePort {
		t.Fatal("with no VLAN to test, every bridge port is a candidate for the session")
	}
}

// No `/ip/address` at all — a device reached only over MAC-winbox: nothing can be ruled
// out, so every LINKED port is spared.
func TestResolveMgmtPathUnknownWhenNoAddressExists(t *testing.T) {
	var path = resolveMgmtPath(DeviceState{BridgeName: "bridge"}, nil, map[string]ifaceRow{})
	if !path.Unknown {
		t.Fatal("no administration address means the guardrail cannot rule anything out")
	}
	if !strings.Contains(path.Why, "MAC-winbox") {
		t.Errorf("the refusal must name how such a device is reached: %q", path.Why)
	}
}

// THE GUARDRAIL, on the REAL state: sfp-sfpplus2 is the only linked port AND a tagged
// member of the management VLAN 99 — it must be spared. Every other port is unlinked
// and must move.
func TestPortAdminGuardBlocksSparesTheUplinkOfTheReferenceDevice(t *testing.T) {
	var st = stateFromFixtures(t)
	var ifaces = foldInterfaces(fixture(t, "interface.json"))
	var path = resolveMgmtPath(st, fixture(t, "interface_vlan.json"), ifaces)

	var uplink = "sfp-sfpplus2"
	var port, isBridgePort = st.Ports[uplink]
	if !isBridgePort {
		t.Fatalf("%s must be a bridge port in the capture", uplink)
	}
	var block, reason = portAdminGuardBlocks(uplink, port, true, path, st.LinkUp[uplink], false)
	if !block {
		t.Fatalf("%s carries the link AND management VLAN 99: it must be spared", uplink)
	}
	if !strings.Contains(reason, "99") {
		t.Errorf("the refusal must name the management VLAN: %q", reason)
	}
	if block, _ = portAdminGuardBlocks(uplink, port, true, path, st.LinkUp[uplink], true); block {
		t.Error("Force must lift the refusal")
	}

	// ether1: no cable. A port with no link cannot carry the session, and refusing it
	// would mean refusing to shut an unused jack — the safest write on the device.
	var e1 = st.Ports["ether1"]
	if block, _ = portAdminGuardBlocks("ether1", e1, true, path, st.LinkUp["ether1"], false); block {
		t.Error("an unlinked port must not be spared")
	}
}

// The port that IS administration is spared even though no VLAN membership implicates
// it — the whole point of resolving the path rather than only reading memberships.
func TestPortAdminGuardBlocksSparesTheManagementPortItself(t *testing.T) {
	var path = mgmtPath{Interface: "ether1", PhysicalPort: "ether1"}
	// Not a bridge port at all, and carrying no VLAN: only the path resolution can
	// protect it.
	var block, reason = portAdminGuardBlocks("ether1", BridgePort{}, false, path, true, false)
	if !block {
		t.Fatal("the port the administration address sits on must be spared")
	}
	if !strings.Contains(reason, "IS administration") {
		t.Errorf("the reason must say why there is no membership to work around: %q", reason)
	}
}

// A port neither implicated by the path nor by membership, on a device where
// administration IS identified: it must be writable even with a live link. Otherwise
// no port with a cable could ever be shut.
func TestPortAdminGuardBlocksAllowsAnUnrelatedLinkedPort(t *testing.T) {
	var path = mgmtPath{Interface: "vlan99", VLAN: 99}
	var port = BridgePort{Mode: "access", PVID: 100, Untagged: []int{100}}
	var block, _ = portAdminGuardBlocks("ether7", port, true, path, true, false)
	if block {
		t.Error("a linked port carrying only VLAN 100 is not the session's path")
	}
}

// portCarriesVLAN consults all THREE lists, because on this platform they are
// independent: BridgePort.Untagged exists precisely because the reference device
// carries a port that tags into a VLAN it is not a member of.
func TestPortCarriesVLANReadsEveryList(t *testing.T) {
	if !portCarriesVLAN(BridgePort{PVID: 99}, 99) {
		t.Error("the PVID counts")
	}
	if !portCarriesVLAN(BridgePort{PVID: 1, Untagged: []int{99}}, 99) {
		t.Error("an untagged membership counts even when the PVID says otherwise")
	}
	if !portCarriesVLAN(BridgePort{PVID: 1, Tagged: []int{99}}, 99) {
		t.Error("a tagged membership counts — that is the uplink case")
	}
	if portCarriesVLAN(BridgePort{PVID: 1, Tagged: []int{10}}, 99) {
		t.Error("a port carrying neither does not carry it")
	}
	if portCarriesVLAN(BridgePort{PVID: 0}, 0) {
		t.Error("VLAN 0 is not a VLAN")
	}
}

// WritesExtra("port_admin") is a PROMISE that this connector applies the intent. It is
// declared because ApplyPortAdmin exists and writes the one field the capture carries;
// the intents with no apply code must stay false.
//
// `dhcp_snooping` LEFT THAT LIST ON 2026-08-10 and the reason is not that apply
// code appeared — ApplyDHCPSnooping had existed for a while — but that it was
// finally RUN: a live round trip on the lab CRS326 through the applier, verified
// from an independent session and torn back down to the opening snapshot
// (TestLiveDHCPSnoopingRoundTrip, and the write-up beside the map entry in
// capabilities.go). Existing code was never the bar on this range; a run was.
func TestWritableExtrasPortAdmin(t *testing.T) {
	if !WritesExtra("port_admin") {
		t.Error("ApplyPortAdmin exists and writes `disabled` on /interface: the UI would never " +
			"offer the gesture this connector implements")
	}
	var name string
	for _, name = range []string{"poe", "port_security"} {
		if WritesExtra(name) {
			t.Errorf("WritesExtra(%q) = true without apply code", name)
		}
	}
}
