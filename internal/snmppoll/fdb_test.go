package snmppoll

import (
	"strconv"
	"testing"

	"github.com/gosnmp/gosnmp"
)

// The FDB assembly, replayed on the LAB'S REAL SHAPES (walked 2026-08-13
// through the product's diagnostic route). Attaching a MAC to the wrong port
// draws a credible false client — the same failure class as LLDP's, on a page
// whose whole purpose is "where is this machine plugged in".

// TestQBridgeRowsCarryTheirVLAN replays the s3900lab walk: Q-BRIDGE index is
// `vlan.mac6`, and the VLAN must survive into the entry.
func TestQBridgeRowsCarryTheirVLAN(t *testing.T) {
	var walk = map[string]gosnmp.SnmpPDU{
		// Real rows from s3900lab: vlan 1, MACs on bridge ports 26 and 28.
		"1.3.6.1.2.1.17.7.1.2.2.1.2.1.4.244.28.29.116.88":      pduInt(28),
		"1.3.6.1.2.1.17.7.1.2.2.1.2.1.40.153.58.157.183.149":   pduInt(26),
		"1.3.6.1.2.1.17.7.1.2.2.1.2.99.184.105.244.91.160.235": pduInt(28),
	}
	var rows = fdbRows(walk, oidDot1qTpFdbPort, true, 0)
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3", len(rows))
	}
	if rows["1|04:f4:1c:1d:74:58"] != 28 {
		t.Errorf("vlan1 MAC on port %d, want 28", rows["1|04:f4:1c:1d:74:58"])
	}
	if rows["99|b8:69:f4:5b:a0:eb"] != 28 {
		t.Error("the vlan arc must key the row: same MAC in another VLAN is another row")
	}
}

// TestFlatBridgeRowsHaveNoVLAN replays the CSS326 (SwOS) regime: dot1d only,
// VLAN honestly unknown — 0, never a guess.
func TestFlatBridgeRowsHaveNoVLAN(t *testing.T) {
	var walk = map[string]gosnmp.SnmpPDU{
		"1.3.6.1.2.1.17.4.3.1.2.212.1.195.171.50.246": pduInt(5),
	}
	var rows = fdbRows(walk, oidDot1dTpFdbPort, false, 0)
	if rows["0|d4:01:c3:ab:32:f6"] != 5 {
		t.Errorf("rows = %v, want the MAC under VLAN 0", rows)
	}
}

// TestMulticastAndPortZeroNeverBecomeClients: a bridge holds group addresses
// and its own MACs (port 0); neither is a machine anyone can go unplug.
func TestMulticastAndPortZeroNeverBecomeClients(t *testing.T) {
	var walk = map[string]gosnmp.SnmpPDU{
		// 01:80:c2:00:00:00 — the STP group address, multicast bit set.
		"1.3.6.1.2.1.17.4.3.1.2.1.128.194.0.0.0": pduInt(3),
		// The device's own address: bridge port 0.
		"1.3.6.1.2.1.17.4.3.1.2.100.1.2.3.4.5": pduInt(0),
	}
	var rows = fdbRows(walk, oidDot1dTpFdbPort, false, 0)
	if len(rows) != 0 {
		t.Errorf("rows = %v, want none", rows)
	}
}

// TestAssemblyResolvesThroughBridgePortAndIfDescr pins the two-hop resolution:
// entry → bridge port → ifIndex → device port name. A row whose hop is missing
// is dropped AND counted — a silent drop is a client nobody can explain.
func TestAssemblyResolvesThroughBridgePortAndIfDescr(t *testing.T) {
	var rows = map[string]int{
		"1|04:f4:1c:1d:74:58":  28,
		"99|aa:bb:cc:dd:ee:01": 7, // bridge port with no ifIndex mapping
	}
	var portMap = map[int]int{28: 10128}
	var names = map[int]string{10128: "TGigaEthernet0/4"}

	var r = FDBReportFrom("dev", rows, portMap, names, true)
	if len(r.Entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(r.Entries))
	}
	var e = r.Entries[0]
	if e.Port != "TGigaEthernet0/4" || e.VLAN != 1 || e.MAC != "04:f4:1c:1d:74:58" {
		t.Errorf("entry = %+v", e)
	}
	if r.Dropped != 1 {
		t.Errorf("dropped = %d, want 1 (the unmapped bridge port)", r.Dropped)
	}
	if !r.Served {
		t.Error("served must carry through")
	}
}

// TestVLANEnumerationSkipsCiscoReserved replays the C2960X's vtpVlanState
// walk: 9 rows, of which 1002-1005 are FDDI/Token Ring defaults no Ethernet
// client can inhabit.
func TestVLANEnumerationSkipsCiscoReserved(t *testing.T) {
	var walk = map[string]gosnmp.SnmpPDU{}
	var vlans = []int{1, 42, 98, 99, 100, 1002, 1003, 1004, 1005}
	var v int
	for _, v = range vlans {
		walk["1.3.6.1.4.1.9.9.46.1.3.1.1.2.1."+strconv.Itoa(v)] = pduInt(1)
	}
	var got = operationalVLANs(walk)
	if len(got) != 5 {
		t.Fatalf("vlans = %v, want the five real ones", got)
	}
	if got[0] != 1 || got[4] != 100 {
		t.Errorf("vlans = %v, want [1 42 98 99 100]", got)
	}
}
