package edgeswitch

import "testing"

// The driver.LLDPReader adapter, replayed on the CAPTURED lab table: 17 ports
// with no neighbour (local interface alone on the row) and one real entry —
// the S3400 on 0/18. The mapping is what the read-state gesture consumes, so
// it is locked value for value.
func TestLLDPObservationFromCapture(t *testing.T) {
	var rows = parseLLDPRemote(mustRead(t, "testdata/show_lldp_remote_device_all.txt"))
	var obs = lldpObservationFrom(rows)
	if !obs.Enabled {
		t.Error("enabled=false: the summary answered, which is the only evidence this platform offers")
	}
	if len(obs.Neighbors) != 1 {
		t.Fatalf("neighbours=%+v — the 17 empty rows are ports with nobody in front, not entries",
			obs.Neighbors)
	}
	var n = obs.Neighbors[0]
	if n.LocalPort != "GE18" {
		t.Errorf("local port %q, expected the standard-model name GE18 for \"0/18\"", n.LocalPort)
	}
	// The parser lowercases the chassis; canonical "0x" spelling is the API
	// layer's decision (api.lldpCanonChassis), not the connector's.
	if n.ChassisID != "64:9d:99:22:7b:95" {
		t.Errorf("chassis %q", n.ChassisID)
	}
	if n.PortID != "TGi0/1" || n.SysName != "poefs3400lab" {
		t.Errorf("port id %q, sys name %q", n.PortID, n.SysName)
	}
}
