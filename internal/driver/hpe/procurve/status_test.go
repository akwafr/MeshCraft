package procurve

// Every test here runs against testdata/, which is VERBATIM output from
// SW-PLAYER-79 (ProCurve 2610-48 / J9088A, R.11.121) captured on 2026-08-03.
//
// That provenance is the whole value. The parsers in this package were written
// twice: once against the vendor guide, and once against these files — and the
// two did not agree. A fixture a capture pipeline has "cleaned" would have hidden
// the difference, which is exactly how the first live read of this range returned
// 4647 characters and zero VLANs (cf. parse.go on the reversed line endings).

import (
	"os"
	"path/filepath"
	"testing"

	"meshcraft/internal/driver"
)

func fixture(t *testing.T, name string) string {
	t.Helper()
	var b, err = os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("reading the capture: %v", err)
	}
	return string(b)
}

// An EMPTY optical cage prints a blank Type and a blank Mode, and every field
// after them shifts if the row is split on whitespace. Ports 51 and 52 of the
// reference unit are exactly that, and they are why table.go reads by ruler.
func TestInterfacesBriefReadsEmptyCagesWithoutShiftingColumns(t *testing.T) {
	var ports = parseInterfacesBrief(fixture(t, "show_interfaces_brief.txt"))
	if len(ports) != 52 {
		t.Fatalf("read %d ports, want 52", len(ports))
	}
	if ports[1].Kind != "10/100TX" || ports[49].Kind != "100/1000T" {
		t.Errorf("port types: 1=%q 49=%q", ports[1].Kind, ports[49].Kind)
	}
	if ports[51].Kind != "" {
		t.Errorf("port 51 Type = %q, want empty: nothing is plugged into that cage",
			ports[51].Kind)
	}
	// The row that proves the point: port 51's remaining cells must still be read
	// from their own columns.
	if !ports[51].Enabled || ports[51].Up {
		t.Errorf("port 51 = %+v, want enabled and down", ports[51])
	}
}

// A disabled port and an unplugged one both print "Down". Only the Enabled
// column separates them, and meshcraft draws them differently.
func TestLinkStatesSeparateDisabledFromDown(t *testing.T) {
	var links = linkStates(parseInterfacesBrief(fixture(t, "show_interfaces_brief.txt")), "GE")
	if links["GE1"] != driver.LinkDisabled {
		t.Errorf("GE1 = %q, want %q: \"interface 1 / disable\" is in the configuration",
			links["GE1"], driver.LinkDisabled)
	}
	if links["GE47"] != driver.LinkUpState {
		t.Errorf("GE47 = %q, want up: it is the uplink", links["GE47"])
	}
	if links["GE2"] != driver.LinkDown {
		t.Errorf("GE2 = %q, want down", links["GE2"])
	}
}

// The bridge priority sits in the SECOND label:value pair of its line. Reading
// only the first is how the value the apply path needs goes missing.
func TestSpanningTreeReadsPriorityFromASharedLine(t *testing.T) {
	var st = parseSpanningTree(fixture(t, "show_spanning_tree.txt"))
	if !st.Enabled {
		t.Fatal("STP read as disabled; the capture says \"STP Enabled : Yes\"")
	}
	// "Multiple Spanning Tree (MST) Information" is the BANNER. The mode is
	// Force Version, and this switch runs RSTP.
	if st.Mode != "rstp" {
		t.Errorf("mode = %q, want rstp: the MST banner is not the operating mode", st.Mode)
	}
	if st.BridgePriority != 32768 {
		t.Errorf("bridge priority = %d, want 32768", st.BridgePriority)
	}
	if !st.IsRoot {
		t.Error("IsRoot = false; the capture says \"This switch is root\"")
	}
	if st.BridgeID != "001ffe-007980" {
		t.Errorf("bridge id = %q", st.BridgeID)
	}
	if len(st.Ports) != 52 {
		t.Fatalf("read %d STP ports, want 52", len(st.Ports))
	}
}

// Port 47 is the only one in the tree; the other 51 are "Disabled", which is a
// ROLE and not a forwarding state.
func TestSpanningTreePortRolesAndCosts(t *testing.T) {
	var st = parseSpanningTree(fixture(t, "show_spanning_tree.txt"))
	var byName = map[string]driver.SpanningTreePort{}
	var p driver.SpanningTreePort
	for _, p = range st.Ports {
		byName[p.Port] = p
	}
	var up = byName["GE47"]
	if up.State != driver.STPStateForwarding {
		t.Errorf("GE47 state = %q, want forwarding", up.State)
	}
	if up.PathCost != 200000 {
		t.Errorf("GE47 path cost = %d, want 200000", up.PathCost)
	}
	if up.Role != driver.STPRoleDesignated {
		t.Errorf("GE47 role = %q, want designated: its designated bridge is this "+
			"switch's own MAC", up.Role)
	}
	var off = byName["GE1"]
	if off.Role != driver.STPRoleDisabled {
		t.Errorf("GE1 role = %q, want disabled", off.Role)
	}
	if off.State != "" {
		t.Errorf("GE1 state = %q, want empty: a port outside the tree has no "+
			"forwarding state to report", off.State)
	}
	// "Auto" is not a number and must not become one: a cost of 0 would read as
	// a configured cost of zero.
	if off.PathCost != 0 {
		t.Errorf("GE1 path cost = %d, want 0 for \"Auto\"", off.PathCost)
	}
}

// The region name defaults to the switch's own MAC, which puts every unit of a
// fleet in a region of its own. That has to be visible, not silently reported as
// a configured region.
func TestSTPRegionRecognisesTheMACDerivedDefault(t *testing.T) {
	var region, own = parseSTPRegion(fixture(t, "show_spanning_tree_config.txt"))
	if region.Name != "001ffe007980" {
		t.Errorf("region name = %q", region.Name)
	}
	if region.Revision != 0 {
		t.Errorf("region revision = %d, want 0", region.Revision)
	}
	if !own {
		t.Error("the MAC-derived default region was not recognised as one")
	}
}

// "show ip" keys its rows by VLAN NAME and never prints an id; "show vlans"
// is the only place the two meet.
func TestMgmtAddressingJoinsTheTwoViews(t *testing.T) {
	var ip = parseIPService(fixture(t, "show_ip.txt"))
	var names = vlanNames(fixture(t, "show_vlans.txt"))
	var st = mgmtAddrFrom(ip, names)

	if st.Mode != driver.MgmtAddrModeStatic {
		t.Errorf("mode = %q, want static: \"LAN_Management | Manual\"", st.Mode)
	}
	if st.Address != "203.0.113.79/24" {
		t.Errorf("address = %q, want 203.0.113.79/24 — the mask is converted to a "+
			"prefix so one notation reaches every platform", st.Address)
	}
	if st.Interface != "vlan99" {
		t.Errorf("interface = %q, want vlan99", st.Interface)
	}
	if st.Gateway != "203.0.113.1" {
		t.Errorf("gateway = %q", st.Gateway)
	}
}

// VLAN 1 carries "DHCP/Bootp" with NO address — the factory state of every
// unit of this range. Naming it the management interface would point
// provisioning at a VLAN the switch is not reachable on.
func TestMgmtAddressingIgnoresTheAddresslessDefaultVLAN(t *testing.T) {
	var st = mgmtAddrFrom(parseIPService(fixture(t, "show_ip.txt")),
		vlanNames(fixture(t, "show_vlans.txt")))
	if st.Interface == "vlan1" {
		t.Fatal("VLAN 1's empty DHCP row was taken for the management interface")
	}
}

func TestVLANNamesAndTheirIDs(t *testing.T) {
	var names = vlanNames(fixture(t, "show_vlans.txt"))
	if len(names) != 10 {
		t.Fatalf("read %d VLANs, want 10", len(names))
	}
	if names[99] != "LAN_Management" || names[200] != "joueurs" || names[1] != "Default" {
		t.Errorf("names = %v", names)
	}
}

// The summary view abbreviates. Passing "ge-0/0..." off as a neighbour's port
// name would put a value in the topology that does not exist on the other device.
func TestLLDPNeighborsReportTheirOwnTruncation(t *testing.T) {
	var list = parseLLDPRemote(fixture(t, "show_lldp_info_remote_device.txt"))
	if len(list) != 1 {
		t.Fatalf("read %d neighbours, want 1", len(list))
	}
	var n = list[0]
	if n.LocalPort != "47" || n.SysName != "salon-sw01" {
		t.Errorf("neighbour = %+v", n)
	}
	if n.ChassisID != "44f477b5a780" {
		t.Errorf("chassis id = %q, want the octets joined", n.ChassisID)
	}
	if !n.Truncated {
		t.Error("\"ge-0/0...\" was not reported as truncated")
	}
}

// "show version" on this range carries NO model and no part number. The
// system description LLDP advertises is the only place a session sees both.
func TestModelComesFromTheLLDPSystemDescription(t *testing.T) {
	var model, part = parseLocalDevice(fixture(t, "show_lldp_info_local_device.txt"))
	if model != "ProCurve 2610-48" {
		t.Errorf("model = %q", model)
	}
	if part != "J9088A" {
		t.Errorf("part number = %q, want J9088A", part)
	}
}

func TestSystemInformation(t *testing.T) {
	var info = parseSystemInfo(fixture(t, "show_system_information.txt"))
	if info.Name != "SW-PLAYER-79" {
		t.Errorf("name = %q", info.Name)
	}
	if info.Serial != "CN0EXAMPL2" {
		t.Errorf("serial = %q", info.Serial)
	}
	// The MAC sits in the SECOND pair of a shared line, like the bridge priority.
	if info.MAC != "001ffe-007980" {
		t.Errorf("MAC = %q, want the value from the second label on its line", info.MAC)
	}
	if info.Revision != "R.11.121" {
		t.Errorf("revision = %q", info.Revision)
	}
}

// The community meshcraft polls with is already on this switch. ApplySNMP must
// see it and leave the flash alone.
func TestSNMPCommunitiesAreRead(t *testing.T) {
	var list = parseSNMPServer(fixture(t, "show_snmp_server.txt"))
	if len(list) != 2 {
		t.Fatalf("read %d communities, want 2: %+v", len(list), list)
	}
	var found bool
	var c community
	for _, c = range list {
		if c.Name == "meshcraft-ro" {
			found = true
			if c.View != "Operator" {
				t.Errorf("meshcraft-ro is %q, want Operator (read-only)", c.View)
			}
		}
	}
	if !found {
		t.Error("meshcraft-ro was not found in the capture")
	}
	if !hasCommunity(fixture(t, "show_running_config.txt"), "meshcraft-ro") {
		t.Error("the configuration reader did not find meshcraft-ro either")
	}
}

// The faceplate follows the hardware: 48 fast-ethernet, 2 dual-personality
// gigabit, 2 empty optical cages.
func TestFaceplateFollowsTheSocketTypes(t *testing.T) {
	var groups = faceplateGroups(parseInterfacesBrief(fixture(t, "show_interfaces_brief.txt")))
	if len(groups) != 3 {
		t.Fatalf("built %d groups, want 3: %+v", len(groups), groups)
	}
	var want = []struct {
		from, count int
		label       string
	}{{1, 48, "10/100TX"}, {49, 2, "100/1000T"}, {51, 2, ""}}
	var i int
	for i = range want {
		if groups[i]["from"] != want[i].from || groups[i]["count"] != want[i].count {
			t.Errorf("group %d = %+v, want from=%d count=%d", i, groups[i],
				want[i].from, want[i].count)
		}
		var label, _ = groups[i]["label"].(string)
		if label != want[i].label {
			t.Errorf("group %d label = %q, want %q", i, label, want[i].label)
		}
	}
}

// The whole point of the running-config read, against the real thing rather
// than a fixture someone tidied.
func TestRunningConfigOfTheProductionSwitch(t *testing.T) {
	var rc = parseRunningConfig(fixture(t, "show_running_config.txt"))
	if rc.Hostname != "SW-PLAYER-79" {
		t.Errorf("hostname = %q", rc.Hostname)
	}
	if len(rc.VLANs) != 10 {
		t.Errorf("read %d VLANs, want 10: %v", len(rc.VLANs), rc.VLANs)
	}
	if rc.MgmtVLAN != 99 {
		t.Errorf("management VLAN = %d, want 99 — VLAN 1's \"dhcp-bootp\" is not an "+
			"address", rc.MgmtVLAN)
	}
	if rc.MaxVLANs != 30 {
		t.Errorf("max-vlans = %d, want 30", rc.MaxVLANs)
	}
	if rc.Gateway != "203.0.113.1" {
		t.Errorf("gateway = %q", rc.Gateway)
	}
	// LLDP is a factory default on this range: the configuration records only a
	// departure from it, and there is none here.
	if rc.LLDPOff {
		t.Error("LLDP read as off; the configuration carries no \"no lldp run\"")
	}
	// "no untagged 1-52" under VLAN 1 is a REMOVAL. Read as an assignment it
	// would put every port of the switch into VLAN 1.
	if len(rc.Untagged[1]) != 0 {
		t.Errorf("VLAN 1 untagged = %v, want none: \"no untagged\" is a removal",
			rc.Untagged[1])
	}
	if len(rc.Untagged[200]) != 51 {
		t.Errorf("VLAN 200 untagged = %d ports, want 51 (2-52)", len(rc.Untagged[200]))
	}
	if len(rc.Tagged[99]) != 8 {
		t.Errorf("VLAN 99 tagged = %v, want ports 45-52", rc.Tagged[99])
	}
}
