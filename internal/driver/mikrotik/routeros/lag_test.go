package routeros

import (
	"testing"

	"meshcraft/internal/driver"
)

// The CRS305's own bonding row, captured 2026-08-13 — the aggregate that
// carries pve01 and anchored the whole LAG model.
func TestFoldBondingsOnTheCRS305Capture(t *testing.T) {
	var rows = []Row{{
		".id": "*8", "name": "bonding1", "mode": "802.3ad",
		"lacp-mode": "active", "lacp-rate": "1sec",
		"slaves": "sfp-sfpplus4", "running": "true",
		"transmit-hash-policy": "layer-3-and-4",
	}}
	var got = foldBondings(rows)
	if len(got) != 1 {
		t.Fatalf("bondings = %d, want 1", len(got))
	}
	var b = got[0]
	if b.Name != "bonding1" || b.ModeRaw != "802.3ad" || !b.Running {
		t.Errorf("bonding = %+v", b)
	}
	if len(b.Slaves) != 1 || b.Slaves[0] != "sfp-sfpplus4" {
		t.Errorf("slaves = %v, want the captured single member", b.Slaves)
	}
}

// The contract view: standard name, lacp mode, and the VLAN half read from
// the bond's OWN bridge-port entry — the fact that made the model cheap
// (the bond is an interface to the bridge tables, indistinguishable from a
// port; measured pvid 1 + eleven tagged VLANs on the reference device).
func TestStandardLAGsJoinBondAndBridgeHalves(t *testing.T) {
	var s = DeviceState{
		Bondings: []Bonding{{
			RowID: "*8", Name: "bonding1", ModeRaw: "802.3ad",
			Slaves: []string{"sfp-sfpplus4"}, Running: true,
		}},
		Ports: map[string]BridgePort{
			"bonding1":     {Mode: "trunk", PVID: 1, Tagged: []int{10, 42, 99}},
			"sfp-sfpplus3": {Mode: "trunk", PVID: 1},
		},
	}
	var lags = standardLAGs(s)
	if len(lags) != 1 {
		t.Fatalf("lags = %d, want 1", len(lags))
	}
	var l = lags[0]
	if l.Name != "LAG1" || l.Raw != "bonding1" || l.Mode != driver.LAGModeLACP {
		t.Errorf("lag = %+v, want LAG1/bonding1/lacp", l)
	}
	if l.VLAN.Mode != "trunk" || l.VLAN.PVID != 1 || len(l.VLAN.Tagged) != 3 {
		t.Errorf("VLAN half not joined from the bridge row: %+v", l.VLAN)
	}
	if !l.Running {
		t.Error("running must carry through — the screen says whether LACP converged")
	}

	// And the Standard() view must MOVE the bond out of the port map: a
	// "bonding1" port would read as an unmanaged jack to every consumer.
	var std = s.Standard()
	var _, stillAPort = std.Ports["bonding1"]
	if stillAPort {
		t.Error("bonding1 must leave State.Ports — it is an aggregate, not a jack")
	}
	if len(std.LAGs) != 1 {
		t.Errorf("State.LAGs = %d, want the aggregate", len(std.LAGs))
	}
}

// A non-negotiating mode is "static" in the standard vocabulary, and a bond
// named outside the bondingN shape keeps its own name on a READ (writes of
// such names are refused by validation — the right asymmetry).
func TestNonLACPAndCustomNamesStayHonest(t *testing.T) {
	var s = DeviceState{Bondings: []Bonding{
		{Name: "bonding2", ModeRaw: "balance-xor", Slaves: []string{"ether1", "ether2"}},
		{Name: "uplink-bond", ModeRaw: "802.3ad", Slaves: []string{"ether3"}},
	}}
	var lags = standardLAGs(s)
	if lags[0].Name != "LAG2" || lags[0].Mode != driver.LAGModeStatic {
		t.Errorf("lags[0] = %+v, want LAG2/static", lags[0])
	}
	if lags[1].Name != "uplink-bond" {
		t.Errorf("a custom bond name must not be renamed on read: %+v", lags[1])
	}
}
