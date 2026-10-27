package procurve

import (
	"testing"
)

// The driver.LLDPReader adapter, against the CAPTURED neighbour table
// (testdata/show_lldp_info_remote_device.txt, SW-PLAYER-79, 2026-08-03).
//
// Only the pure translation is exercised: the CLI read it sits on is
// [Client.ReadLLDPNeighbors], already covered by
// TestLLDPNeighborsReportTheirOwnTruncation, and THE LAB 2610 IS OFFLINE — this
// package has no live test at all and this change did not add one.

func TestTheLLDPAdapterNamesLocalPortsInTheStandardModel(t *testing.T) {
	var rows = parseLLDPRemote(fixture(t, "show_lldp_info_remote_device.txt"))
	var obs = lldpObservationFrom(rows, defaultPortPrefix)

	if !obs.Enabled {
		t.Error("this range ships with LLDP on and the table carries no " +
			"\"disabled\" marker: the read having answered is all the evidence " +
			"there is, and Enabled must reflect that")
	}
	if len(obs.Neighbors) != 1 {
		t.Fatalf("read %d neighbours, want 1", len(obs.Neighbors))
	}
	var n = obs.Neighbors[0]
	// The CLI says "47"; what leaves the connector must be the facade name,
	// because api.lldpRowsFor resolves it by exact match.
	if n.LocalPort != "GE47" {
		t.Errorf("local port = %q, want GE47 — the contract asks for the "+
			"standard-model name, not the platform spelling", n.LocalPort)
	}
	if n.ChassisID != "44f477b5a780" {
		t.Errorf("chassis id = %q, want the joined octets", n.ChassisID)
	}
	if n.SysName != "salon-sw01" {
		t.Errorf("sys name = %q, want salon-sw01", n.SysName)
	}
	if n.PortID != "521" {
		t.Errorf("port id = %q, want 521 — the column was printed whole", n.PortID)
	}
}

// WHAT THE SWITCH CUT MUST NOT NAME A PORT. "ge-0/0..." is a prefix of
// "ge-0/0/9.0" and nothing else; handing it to api.resolvePeerPort would offer
// a mutilated string as the far end of a cable, and refutation is port-exact.
func TestTheLLDPAdapterDropsATruncatedFarPortDescription(t *testing.T) {
	var rows = parseLLDPRemote(fixture(t, "show_lldp_info_remote_device.txt"))
	var obs = lldpObservationFrom(rows, defaultPortPrefix)
	if len(obs.Neighbors) != 1 {
		t.Fatalf("read %d neighbours, want 1", len(obs.Neighbors))
	}
	if obs.Neighbors[0].PortDesc != "" {
		t.Errorf("port description = %q, want \"\" — the platform marked it cut",
			obs.Neighbors[0].PortDesc)
	}
	if len(obs.Notes) == 0 {
		t.Error("the truncation must be said, not merely acted on: the operator " +
			"has to know why the far port has no name")
	}
}

// A prefix comes from the inventory when the device carries one, exactly as
// [Client.ReadState] names its ports — otherwise the two reads would write two
// different names for one socket.
func TestTheLLDPAdapterHonoursTheDevicesOwnPrefix(t *testing.T) {
	var rows = parseLLDPRemote(fixture(t, "show_lldp_info_remote_device.txt"))
	var obs = lldpObservationFrom(rows, "ETH")
	if len(obs.Neighbors) != 1 || obs.Neighbors[0].LocalPort != "ETH47" {
		t.Fatalf("local port = %+v, want ETH47", obs.Neighbors)
	}
}

// A local port this CLI did not print as a bare number cannot be placed on the
// faceplate, so the row is not emitted at all — the same outcome as the API
// layer's drop, decided one step earlier.
func TestTheLLDPAdapterDropsAnUnplaceableLocalPort(t *testing.T) {
	var obs = lldpObservationFrom([]LLDPNeighbor{
		{LocalPort: "Trk1", SysName: "somebody"},
		{LocalPort: "4...", SysName: "cut-in-half"},
		{LocalPort: "12", SysName: "real", PortID: "Gi0/1"},
	}, defaultPortPrefix)
	if len(obs.Neighbors) != 1 {
		t.Fatalf("kept %d neighbours, want only the numbered one: %+v",
			len(obs.Neighbors), obs.Neighbors)
	}
	if obs.Neighbors[0].LocalPort != "GE12" {
		t.Errorf("local port = %q, want GE12", obs.Neighbors[0].LocalPort)
	}
}
