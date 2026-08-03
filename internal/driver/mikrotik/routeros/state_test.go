package routeros

import (
	"reflect"
	"testing"
)

// TestReferenceDeviceState replays real captures and locks what the connector derives
// from them.
//
// This test is not structural paraphrase: it freezes the translation from RouterOS's
// THREE-menu model to meshcraft's per-port model (cf. the header of state.go). Each
// assertion corresponds to a line in the captures, therefore to an equipment response.
func TestReferenceDeviceState(t *testing.T) {
	var st = stateFromFixtures(t)

	if st.BridgeName != "bridge" {
		t.Errorf("bridge = %q, expected 'bridge'", st.BridgeName)
	}
	// Filtering is ACTIVE on this device, and that is what makes every write error
	// immediately effective: there is no grace period.
	if !st.VLANFiltering {
		t.Error("VLAN filtering is active on the reference device " +
			"(observed `vlan-filtering: true`): losing it would change the gravity of " +
			"every write from this connector")
	}
	// 26 switching ports in the bridge: 24 copper + 2 SFP+.
	if len(st.Ports) != 26 {
		t.Errorf("%d ports in the bridge, 26 expected", len(st.Ports))
	}
}

// TestPVIDIsNotDerivedFromMembership locks the most costly difference between this
// platform and the Instant On line.
//
// On a 1930, a port's PVID IS DERIVED from its untagged membership — that is even what
// its UI does. Here the two are INDEPENDENT, and the reference device gives the
// illustration: its 24 copper ports carry `pvid: 100` while VLAN 100 has an EMPTY
// `untagged` list. They therefore tag ingress traffic into VLAN 100 without being
// egress members of VLAN 100.
//
// That is incomplete configuration, perfectly accepted by the device, and silent. A
// connector that derived PVID from membership would read 'access on VLAN 1' where the
// device does something else — and an apply would compare against an imaginary state.
// This test exists so nobody 'simplifies' the read by reintroducing derivation.
func TestPVIDIsNotDerivedFromMembership(t *testing.T) {
	var st = stateFromFixtures(t)

	var ether1 = st.Ports["ether1"]
	if ether1.PVID != 100 {
		t.Errorf("ether1 PVID = %d, expected 100 (observed `pvid: 100` in "+
			"/interface/bridge/port)", ether1.PVID)
	}
	// And yet NO membership: that is the whole point.
	if len(ether1.Untagged) != 0 {
		t.Errorf("ether1 is an untagged member of %v, yet VLAN 100 has an EMPTY "+
			"`untagged` list in the capture — PVID was confused with membership",
			ether1.Untagged)
	}
	if len(ether1.Tagged) != 0 {
		t.Errorf("ether1 tagged = %v, expected none", ether1.Tagged)
	}
	// Mode stays 'access': PVID alone says the port is managed.
	if ether1.Mode != "access" {
		t.Errorf("ether1 mode = %q, expected 'access'", ether1.Mode)
	}
	// Hardware offload is observed active on all ports.
	if !ether1.HWOffload {
		t.Error("ether1: hardware offload expected active (observed `hw-offload: true`)")
	}
}

// TestUplinkTrunkAndTaggedVLANs: sfp-sfpplus2 is the reference device's uplink port,
// and everything arrives through it.
//
// Observed: it is tagged in VLANs 99 and 100, and untagged in VLAN 1. It is therefore
// a trunk, and the port no apply must break.
func TestUplinkTrunkAndTaggedVLANs(t *testing.T) {
	var st = stateFromFixtures(t)

	var uplink = st.Ports["sfp-sfpplus2"]
	if uplink.Mode != "trunk" {
		t.Errorf("sfp-sfpplus2 mode = %q, expected 'trunk'", uplink.Mode)
	}
	// Sorted: unstable order would make a port that did not change blink.
	if !reflect.DeepEqual(uplink.Tagged, []int{99, 100}) {
		t.Errorf("sfp-sfpplus2 tagged = %v, expected [99 100] sorted", uplink.Tagged)
	}
	if !reflect.DeepEqual(uplink.Untagged, []int{1}) {
		t.Errorf("sfp-sfpplus2 untagged = %v, expected [1]", uplink.Untagged)
	}
	// It is the ONLY port carrying a link on this device — therefore the only one the
	// apply safeguard will protect.
	if !st.LinkUp["sfp-sfpplus2"] {
		t.Error("sfp-sfpplus2 carries a link on the reference device " +
			"(observed `running: true`): that is what arms the apply safeguard")
	}
	if st.LinkUp["ether1"] {
		t.Error("ether1 carries NO link (observed `running: false`): the " +
			"safeguard must not apply there, otherwise a new device would be " +
			"impossible to configure")
	}
}

// TestCPUMemberOfMgmtVLANButNotAPort — the most important assertion in this file.
//
// The pseudo-port `bridge` in a VLAN's tagged list IS the CPU. Observed: VLAN 99 has
// `tagged: "bridge,sfp-sfpplus2"`, and that `bridge` is what lets tagged frames reach
// administration.
//
// Two things to lock, and they are symmetric:
//
//   - the CPU is recognized as a member of VLAN 99 (otherwise apply would not know
//     it must be preserved);
//   - the CPU is NOT counted as a facade port (otherwise meshcraft would draw a jack
//     that does not exist, and would end up writing on it).
func TestCPUMemberOfMgmtVLANButNotAPort(t *testing.T) {
	var st = stateFromFixtures(t)

	if !st.CPUCarries(99) {
		t.Errorf("the CPU must be recognized as a tagged member of VLAN 99 (observed "+
			"`tagged: \"bridge,sfp-sfpplus2\"`), CPUTagged = %v. Without that, apply "+
			"does not know it must be preserved and can close administration without "+
			"cutting traffic — the hardest fault to diagnose remotely on this platform",
			st.CPUTagged)
	}
	if st.CPUCarries(100) {
		t.Error("the CPU is NOT a member of VLAN 100 (observed `tagged: " +
			"\"sfp-sfpplus2\"` only): declaring it would invent an administration path " +
			"that does not exist")
	}
	// And it does not appear as a port.
	var _, present = st.Ports["bridge"]
	if present {
		t.Error("the pseudo-port `bridge` appeared in the port list: " +
			"that is the CPU, not a facade jack")
	}
}

// TestMgmtVLANDerivedFromAddress locks the derivation chain ALL apply safeguards depend on.
//
// The chain, entirely observed: `/ip/address` gives `10.99.0.54/24` on `vlan99`;
// `/interface/vlan` gives `vlan99` with identifier 99 on `bridge`; therefore management
// VLAN is 99.
//
// The trap this test protects: `/ip/address` ALSO carries `192.168.88.1/24` on the
// bridge, with `comment: defconf` — the address left by factory configuration. Keeping
// it would designate VLAN 1 as management VLAN, therefore protect the wrong VLAN and
// leave the one carrying the current session open to rewrite. That is exactly the kind
// of error that cuts access while believing it was protected.
func TestMgmtVLANDerivedFromAddress(t *testing.T) {
	var st = stateFromFixtures(t)

	if st.MgmtVLAN != 99 {
		t.Errorf("management VLAN = %d, expected 99. The capture carries two "+
			"addresses: 192.168.88.1/24 on the bridge (defconf, unused) and "+
			"10.99.0.54/24 on vlan99 (the real one). Keeping the first would protect the "+
			"wrong VLAN", st.MgmtVLAN)
	}
	if st.MgmtInterface != "vlan99" {
		t.Errorf("management interface = %q, expected 'vlan99'", st.MgmtInterface)
	}
	if st.MgmtAddress != "10.99.0.54/24" {
		t.Errorf("management address = %q, expected '10.99.0.54/24'",
			st.MgmtAddress)
	}
}

// TestFactoryAddressAloneDoesNotDesignateTaggedVLAN: the symmetric case of the previous test.
//
// When the device has ONLY its factory address on the bridge, administration arrives
// UNTAGGED and the management VLAN is the bridge's PVID. Returning 0 would be wrong,
// but inventing a tagged VLAN would be worse.
// PVID comes from [DeviceState.BridgePVID], read on `/interface/bridge`, and NOT from
// `Ports[BridgeName]`: the bridge is never a `/interface/bridge/port` record, so that
// map never carries its name. The previous version of this test fabricated that entry by
// hand — it validated a path the real read never takes, and MgmtVLAN stayed at 0 on
// every factory device, apply then refusing to write 'unknown management VLAN'.
func TestFactoryAddressAloneDoesNotDesignateTaggedVLAN(t *testing.T) {
	var st = DeviceState{
		BridgeName: "bridge",
		BridgePVID: 1,
		Ports:      map[string]BridgePort{},
	}
	var addrs = []Row{{
		"address": "192.168.88.1/24", "interface": "bridge",
		"actual-interface": "bridge", "comment": "defconf",
	}}
	foldMgmt(addrs, nil, &st)

	if st.MgmtInterface != "bridge" {
		t.Errorf("management interface = %q, expected 'bridge'", st.MgmtInterface)
	}
	// VLAN comes from the bridge PVID: untagged administration.
	if st.MgmtVLAN != 1 {
		t.Errorf("management VLAN = %d, expected 1 (bridge PVID) — "+
			"administration arrives UNTAGGED", st.MgmtVLAN)
	}
}

// TestWithoutAddressNoVLANIsAsserted: not knowing must be visible.
//
// A device reached via MAC-winbox or serial console does not necessarily have an IP
// address. The read must then assert NOTHING — that is what makes apply refuse (cf.
// ApplyPorts), and it is the right behavior: rewriting member lists without knowing
// how we got in is exactly the gesture that cuts access.
func TestWithoutAddressNoVLANIsAsserted(t *testing.T) {
	var st = DeviceState{BridgeName: "bridge", Ports: map[string]BridgePort{}}
	foldMgmt(nil, nil, &st)

	if st.MgmtVLAN != 0 || st.MgmtAddress != "" || st.MgmtInterface != "" {
		t.Errorf("without an address, nothing must be asserted: VLAN=%d, address=%q, "+
			"interface=%q", st.MgmtVLAN, st.MgmtAddress, st.MgmtInterface)
	}
}

// TestDynamicVLANDistinguishedFromStatic: RouterOS fabricates entries on its own.
//
// Observed: VLAN 1 on the reference device is `dynamic: true` with comment 'added by
// pvid' — the device created it because ports have that PVID. Such an entry has no
// stable `.id`, cannot be modified, and disappears when its reason disappears.
// Confusing it with an ordinary entry would fail writes without the message saying why.
func TestDynamicVLANDistinguishedFromStatic(t *testing.T) {
	var st = stateFromFixtures(t)

	var byID = map[int]BridgeVLAN{}
	var v BridgeVLAN
	for _, v = range st.VLANs {
		byID[v.ID] = v
	}
	if !byID[1].Dynamic {
		t.Error("VLAN 1 is DYNAMIC on the reference device " +
			"(observed `dynamic: true`, comment 'added by pvid'): believing it " +
			"static would attempt writes the device will refuse")
	}
	if byID[99].Dynamic || byID[100].Dynamic {
		t.Error("VLANs 99 and 100 are STATIC (observed `dynamic: false`): " +
			"those are what an apply can modify")
	}
	// vlanRowFor must never return a dynamic entry: it is not modifiable.
	var _, found = vlanRowFor(st, 1)
	if found {
		t.Error("vlanRowFor returned the DYNAMIC entry for VLAN 1: apply " +
			"would attempt to modify it")
	}
	var row BridgeVLAN
	row, found = vlanRowFor(st, 99)
	if !found || row.RowID == "" {
		t.Errorf("vlanRowFor(99) must return the static entry with its .id, "+
			"got %+v (found=%t)", row, found)
	}
}

func TestParseVLANIDs(t *testing.T) {
	var cases = []struct {
		name string
		spec string
		want []int
	}{
		{"empty", "", nil},
		{"single", "99", []int{99}},
		// Form observed on the reference device.
		{"list", "99,100", []int{99, 100}},
		// The device accepts RANGES in the same field: reading an integer would fail
		// silently on this form.
		{"range", "10-13", []int{10, 11, 12, 13}},
		{"mixed", "1,10-12,99", []int{1, 10, 11, 12, 99}},
		// Observed in /interface/bridge/msti: the full space.
		{"full bounded space", "1-4094", nil}, // checked by length below
		// Outside 802.1Q space: skipped rather than guessed.
		{"zero rejected", "0", nil},
		{"beyond 4094", "4095", nil},
		{"reversed range", "20-10", nil},
		{"unreadable token", "abc,99", []int{99}},
		{"merged duplicates", "99,99,100", []int{99, 100}},
	}
	var c = cases[0]
	for _, c = range cases {
		t.Run(c.name, func(t *testing.T) {
			var got = ParseVLANIDs(c.spec)
			if c.name == "full bounded space" {
				if len(got) != 4094 || got[0] != 1 || got[len(got)-1] != 4094 {
					t.Errorf("'1-4094' must expand to 4094 identifiers from 1 to 4094, "+
						"got %d (from %v to %v)", len(got), got[0], got[len(got)-1])
				}
				return
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("ParseVLANIDs(%q) = %v, expected %v", c.spec, got, c.want)
			}
		})
	}
}

// TestPortsFromOtherBridgeIgnored: a device may have several bridges.
//
// Writing a VLAN on the wrong one would produce nothing VISIBLE — no error, no effect —
// which is the worst failure mode. The read therefore filters by bridge, and this test
// locks that.
func TestPortsFromOtherBridgeIgnored(t *testing.T) {
	var ports = []Row{
		{".id": "*1", "bridge": "bridge", "interface": "ether1", "pvid": "10"},
		{".id": "*2", "bridge": "bridge-guests", "interface": "ether2", "pvid": "20"},
	}
	var vlans = []Row{
		{".id": "*A", "bridge": "bridge", "vlan-ids": "10", "tagged": "ether1"},
		{".id": "*B", "bridge": "bridge-guests", "vlan-ids": "20", "tagged": "ether2"},
	}
	var _, got, _ = foldVLANs(vlans, ports, "bridge")

	var _, present = got["ether2"]
	if present {
		t.Error("ether2 belongs to bridge 'bridge-guests': reading it in the " +
			"'bridge' table would compare a port against the wrong " +
			"configuration, and writing on it would have no visible effect")
	}
	if got["ether1"].PVID != 10 {
		t.Errorf("ether1 PVID = %d, expected 10", got["ether1"].PVID)
	}
}
