package procurve

// Captures from a ProCurve 2810-24G (J9021A), software N.11.15, read on
// 2026-11-06 over SSH through a meshcraft proxy — testdata/2810_24g_n11/. They
// pin that the 2810 reads with the SAME parsers as the 2610 reference unit:
// the neighbouring range this connector now claims in its fingerprint.
//
// Transformation applied: stripControl (cli.go), then serial, MACs, contact,
// SNMP communities, IP addresses and VLAN names replaced. Nothing else.

import (
	"path/filepath"
	"testing"
)

func fixture2810(t *testing.T, name string) string {
	t.Helper()
	return fixture(t, filepath.Join("2810_24g_n11", name))
}

func Test2810ModelAndFamily(t *testing.T) {
	var model, part = parseLocalDevice(fixture2810(t, "show_lldp_info_local_device.txt"))
	if part != "J9021A" {
		t.Errorf("part number = %q, want J9021A", part)
	}
	if model == "" {
		t.Fatalf("no model read from %q", "show lldp info local-device")
	}
	if familyOf(model) != "ProCurve 2800" {
		t.Errorf("family of %q = %q, want ProCurve 2800", model, familyOf(model))
	}
	if familyOf("ProCurve 2610-48") != "ProCurve 2600" {
		t.Errorf("the 2610 must stay in the 2600 family")
	}
}

func Test2810SystemInformation(t *testing.T) {
	var info = parseSystemInfo(fixture2810(t, "show_system_information.txt"))
	if info.Name != "SW-PLAYER-90" || info.Serial != "CN0EXAMPL3" ||
		info.MAC != "b439d6-009020" || info.Revision != "N.11.15" {
		t.Errorf("got %+v", info)
	}
}

// 24 copper gigabit ports and no cage: port 23 is the only link (the uplink).
func Test2810Faceplate(t *testing.T) {
	var ports = parseInterfacesBrief(fixture2810(t, "show_interfaces_brief.txt"))
	if len(ports) != 24 {
		t.Fatalf("read %d ports, want 24", len(ports))
	}
	if !ports[23].Up || ports[22].Up {
		t.Errorf("link state: 23 up=%v, 22 up=%v", ports[23].Up, ports[22].Up)
	}
	if ports[1].Kind != "100/1000T" {
		t.Errorf("type of port 1 = %q", ports[1].Kind)
	}
	var groups = faceplateGroups(ports)
	if len(groups) != 1 || groups[0]["count"] != 24 {
		t.Errorf("faceplate = %v, want one group of 24", groups)
	}
}

// The player-switch template: VLAN 200 untagged on every port, the rest tagged on
// the 21-24 uplinks, management on VLAN 99.
func Test2810RunningConfig(t *testing.T) {
	var rc = parseRunningConfig(fixture2810(t, "show_running_config.txt"))
	if rc.Hostname != "SW-PLAYER-90" {
		t.Errorf("hostname = %q", rc.Hostname)
	}
	if rc.MaxVLANs != 256 {
		t.Errorf("max-vlans = %d, want 256", rc.MaxVLANs)
	}
	var id int
	for _, id = range []int{1, 2, 3, 9, 10, 99, 101, 102, 103, 104, 200} {
		var _, ok = rc.VLANs[id]
		if !ok {
			t.Errorf("VLAN %d missing", id)
		}
	}
	if rc.MgmtVLAN != 99 {
		t.Errorf("management VLAN = %d, want 99", rc.MgmtVLAN)
	}
	if len(rc.Untagged[200]) != 24 {
		t.Errorf("VLAN 200 untagged on %v, want 1-24", rc.Untagged[200])
	}
	if len(rc.Tagged[101]) != 4 || rc.Tagged[101][0] != 21 {
		t.Errorf("VLAN 101 tagged on %v, want 21-24", rc.Tagged[101])
	}
}

func Test2810SpanningTree(t *testing.T) {
	var st = parseSpanningTree(fixture2810(t, "show_spanning_tree.txt"))
	if st.Mode != "rstp" {
		t.Errorf("mode = %q, want rstp (Force Version : RSTP-operation)", st.Mode)
	}
}
