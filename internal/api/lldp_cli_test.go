package api

import (
	"encoding/json"
	"testing"

	"meshcraft/internal/driver"
	"meshcraft/internal/model"
	"meshcraft/internal/snmppoll"
)

// CLI-collected LLDP snapshots, exercised WITHOUT a database — the package
// convention (cf. lldp_test.go, whose fleet fixtures these tests reuse).
// Values come from the 2026-08-05 survey of `poefs3400lab`: the S3400's
// captured `show lldp neighbor` table (fs/s3400 parse_test.go) and the
// EdgeSwitch's captured `show lldp remote-device all` (ubiquiti/edgeswitch
// testdata) are the two real shapes the translation must carry.

// deviceEdge reproduces the EdgeSwitch row: 18-port GE facade, connector
// declared, spanning-tree bridge identifier observed at read-state.
func deviceEdge() model.Device {
	return model.Device{
		ID: idEdge, SiteID: idSite, Name: "unifimoule1", Model: "EdgeSwitch",
		PortGroups: json.RawMessage(`[{"prefix":"GE","from":1,"count":18,"speed":"1G"}]`),
		Extra: json.RawMessage(`{"driver":"ubiquiti-edgeswitch","stp_observed":{
			"bridge_id":"9c:05:d6:f5:f1:37"}}`),
	}
}

// TestLLDPCanonChassis: the CLI and SNMP paths must write ONE spelling under
// the primary key, or each write deletes the other's row and resets
// first_seen. Both spellings below are measured on the same chassis.
func TestLLDPCanonChassis(t *testing.T) {
	var cases = map[string]string{
		"64:9D:99:22:7B:95": "0x649d99227b95", // EdgeSwitch CLI spelling
		"0x649d99227b95":    "0x649d99227b95", // SNMP OctetText spelling, unchanged
		"649d.9922.7b95":    "0x649d99227b95",
		"room-b-rack-12":    "room-b-rack-12", // a local(7) chassis is NOT a MAC
		"45":                "45",
		"":                  "",
	}
	var in, want string
	for in, want = range cases {
		var got = lldpCanonChassis(in)
		if got != want {
			t.Errorf("canon(%q)=%q, expected %q", in, got, want)
		}
	}
}

// TestCLISnapshotS3400: the translation of the S3400's own table. The XG2 row
// is the lab capture verbatim (empty Device-ID, S3900 opposite); the XG1 row
// is the shape the same table produces for the EdgeSwitch — the neighbour's
// ANNOUNCED name in Device-ID, its lldpLocPortId in Port-ID.
func TestCLISnapshotS3400(t *testing.T) {
	var obs = driver.LLDPObservation{Enabled: true, Neighbors: []driver.LLDPNeighborObservation{
		{LocalPort: "XG1", PortID: "0/18", SysName: "UBNT EdgeSwitch"},
		{LocalPort: "XG2", PortID: "Eth 1/25", SysName: ""},
	}}
	var self, rows, dropped = lldpCLISnapshot(deviceS3400(), model.LLDPSelf{}, false, obs)
	if dropped != 0 {
		t.Fatalf("%d rows dropped: standard names must resolve in the facade by exact match", dropped)
	}
	if self.ChassisID != "0x649d99227b95" {
		t.Errorf("self chassis %q, expected the observed bridge MAC in canonical spelling: "+
			"this is what lets buildLLDPIdentity resolve the S3400 natively", self.ChassisID)
	}
	if self.SysName != "" {
		t.Errorf("self sys_name %q: the CLI read never sees what the device ANNOUNCES, and "+
			"the meshcraft name must never enter the sys_name index", self.SysName)
	}
	if !self.Served || self.Source != lldpSourceCLI {
		t.Errorf("served=%v source=%q, expected a served CLI snapshot", self.Served, self.Source)
	}
	if len(rows) != 2 {
		t.Fatalf("rows=%+v", rows)
	}
	if rows[0].LocalPort != "XG1" || rows[0].ChassisID != "" || rows[0].PortID != "0/18" ||
		rows[0].SysName != "UBNT EdgeSwitch" {
		t.Errorf("XG1 row=%+v: the summary table serves NO chassis column, and inventing "+
			"one would be worse than the sys_name last resort", rows[0])
	}
	if rows[1].LocalPort != "XG2" || rows[1].PortID != "Eth 1/25" || rows[1].SysName != "" {
		t.Errorf("XG2 row=%+v — the captured S3900 entry, Device-ID blank", rows[1])
	}
}

// TestCLISnapshotDisabled: "LLDP is not enabled" is a CONSTAT, not a
// failure — served false, zero rows, and the identity the device is not
// announcing is not written either.
func TestCLISnapshotDisabled(t *testing.T) {
	var self, rows, _ = lldpCLISnapshot(deviceS3400(), model.LLDPSelf{}, false,
		driver.LLDPObservation{Enabled: false})
	if self.Served {
		t.Error("served=true for a device whose LLDP is off: false and \"zero neighbours\" " +
			"must stay distinguishable")
	}
	if self.ChassisID != "" || len(rows) != 0 {
		t.Errorf("chassis=%q rows=%d: a protocol that is off announces nothing", self.ChassisID, len(rows))
	}
}

// TestCLISnapshotEdgeSwitch: the EdgeSwitch capture, and the property the
// canonical spelling exists for — the row lands under EXACTLY the key the
// SNMP path already stored in production, so first_seen survives the
// alternation between a poll cycle and a read-state.
func TestCLISnapshotEdgeSwitch(t *testing.T) {
	var obs = driver.LLDPObservation{Enabled: true, Neighbors: []driver.LLDPNeighborObservation{
		// testdata/show_lldp_remote_device_all.txt, the only populated row.
		{LocalPort: "GE18", ChassisID: "64:9D:99:22:7B:95", PortID: "TGi0/1",
			SysName: "poefs3400lab"},
	}}
	var self, rows, dropped = lldpCLISnapshot(deviceEdge(), model.LLDPSelf{}, false, obs)
	if dropped != 0 || len(rows) != 1 {
		t.Fatalf("rows=%+v dropped=%d", rows, dropped)
	}
	if rows[0].ChassisID != "0x649d99227b95" {
		t.Errorf("chassis %q, expected \"0x649d99227b95\" — the exact string the SNMP path "+
			"wrote for this same neighbour (production row, GE18)", rows[0].ChassisID)
	}
	if rows[0].LocalPort != "GE18" || rows[0].PortID != "TGi0/1" {
		t.Errorf("row=%+v", rows[0])
	}
	if self.ChassisID != "0x9c05d6f5f137" {
		t.Errorf("self chassis %q, expected the observed bridge MAC", self.ChassisID)
	}
}

// TestCLISnapshotPreservesPriorIdentity: what the CLI read never looked at,
// it must not refute. The EdgeSwitch's SNMP snapshot knows its announced
// name and port identifiers; a CLI snapshot carries none of that, and
// overwriting them with blanks would break sys_name recognition of every
// neighbour that names this device — for the thirty seconds until the next
// poll cycle, on every read-state.
func TestCLISnapshotPreservesPriorIdentity(t *testing.T) {
	var prior = model.LLDPSelf{
		DeviceID: idEdge, SiteID: idSite,
		ChassisID: "0x9c05d6f5f137", SysName: "UBNT EdgeSwitch",
		PortIDs: []string{"0/1", "0/18"}, Served: true, Source: lldpSourceSNMP,
	}
	var self, _, _ = lldpCLISnapshot(deviceEdge(), prior, true,
		driver.LLDPObservation{Enabled: true})
	if self.SysName != "UBNT EdgeSwitch" {
		t.Errorf("sys_name %q: the prior announced name must survive a read that never saw it",
			self.SysName)
	}
	if len(self.PortIDs) != 2 {
		t.Errorf("port_ids %v: preserved for the same reason", self.PortIDs)
	}
	if self.Source != lldpSourceCLI {
		t.Errorf("source %q: the snapshot is still the CLI's", self.Source)
	}
}

// TestSNMPSnapshotOverrides replays the arbitration's decision table.
func TestSNMPSnapshotOverrides(t *testing.T) {
	var cliPrior = model.LLDPSelf{Source: lldpSourceCLI}
	var snmpPrior = model.LLDPSelf{Source: lldpSourceSNMP}
	// The S3400's real report: Served TRUE (the walk batch answered sysDescr)
	// with zero LLDP content — the measured over-count the content test
	// exists to see through.
	var empty = snmppoll.LLDPReport{DeviceID: idS34, Served: true,
		SysDescr: "S3400-48T4SP", SysObjectID: ".1.3.6.1.4.1.52642.1.445.0"}
	if snmpSnapshotOverrides(cliPrior, empty) {
		t.Error("an SNMP snapshot with no LLDP content erased a CLI snapshot: the poller " +
			"would wipe the read-state observation thirty seconds after every read")
	}
	if !snmpSnapshotOverrides(snmpPrior, empty) {
		t.Error("SNMP-owned rows must keep last-writer-wins byte-identical, including " +
			"\"LLDP went dark\" emptying the tables")
	}
	var withNeighbors = empty
	withNeighbors.Neighbors = []snmppoll.Neighbor{{LocalPort: "0/18"}}
	if !snmpSnapshotOverrides(cliPrior, withNeighbors) {
		t.Error("an agent that starts serving neighbours is the better witness again")
	}
	var withChassis = empty
	withChassis.ChassisID = "0x649d99227b95"
	if !snmpSnapshotOverrides(cliPrior, withChassis) {
		t.Error("a served local chassis is LLDP content too")
	}
}

// TestS3400BothSidesResolves is the production scenario ONE STEP FURTHER than
// TestS3400ResolvesInTopology: that test locks the links at one-sided while
// only others observe the S3400; this one adds what the read-state gesture
// now produces — the S3400's own CLI snapshot — and the GE18↔XG1 link must
// close to both_sides. The EdgeSwitch half is the production SNMP row
// verbatim; the S3400 half is its CLI table translated by lldpCLISnapshot.
func TestS3400BothSidesResolves(t *testing.T) {
	var s34Self, s34Rows, dropped = lldpCLISnapshot(deviceS3400(), selfS3400(), true,
		driver.LLDPObservation{Enabled: true, Neighbors: []driver.LLDPNeighborObservation{
			{LocalPort: "XG1", PortID: "0/18", SysName: "UBNT EdgeSwitch"},
		}})
	if dropped != 0 {
		t.Fatalf("%d rows dropped", dropped)
	}
	var neighbors = append(s34Rows, model.LLDPNeighbor{
		// lldp_neighbors, production, written by the SNMP path.
		DeviceID: idEdge, SiteID: idSite, LocalPort: "GE18",
		ChassisID: "0x649d99227b95", PortID: "TGi0/1", SysName: "Switch",
	})
	var selves = []model.LLDPSelf{s34Self, {
		// lldp_self, production: the EdgeSwitch's SNMP-announced identity.
		DeviceID: idEdge, SiteID: idSite, ChassisID: "0x9c05d6f5f137",
		SysName: "UBNT EdgeSwitch", PortIDs: []string{"0/1", "0/18"},
		Served: true, Source: lldpSourceSNMP,
	}}
	var view = buildLLDPView(neighbors, selves, []model.Device{deviceS3400(), deviceEdge()})
	if len(view.Links) != 1 {
		t.Fatalf("%d links, expected the two halves to merge into ONE: %+v",
			len(view.Links), view.Links)
	}
	var l = view.Links[0]
	if !l.BothSides {
		t.Fatalf("both_sides=false: reciprocity now has its witness — the S3400 claims "+
			"(%s, GE18) from XG1 and the EdgeSwitch claims (%s, XG1) from GE18", idEdge, idS34)
	}
	var a, b = l.ADevice + ":" + l.APort, l.BDevice + ":" + l.BPort
	if a != idS34+":XG1" && a != idEdge+":GE18" {
		t.Errorf("link ends %s ↔ %s", a, b)
	}
	if b != idS34+":XG1" && b != idEdge+":GE18" {
		t.Errorf("link ends %s ↔ %s", a, b)
	}
	// The S3400's observation identified its peer by the LAST-RESORT path:
	// its table serves no chassis, only the announced name.
	var i int
	for i = range view.Neighbors {
		var n = view.Neighbors[i]
		if n.DeviceID == idS34 && n.LocalPort == "XG1" {
			if n.PeerDevice != idEdge || n.PeerVia != lldpViaSysName || n.PeerPort != "GE18" {
				t.Errorf("S3400 observation resolved %q via %q port %q — expected the "+
					"EdgeSwitch via sys_name on GE18", n.PeerDevice, n.PeerVia, n.PeerPort)
			}
		}
	}
}
