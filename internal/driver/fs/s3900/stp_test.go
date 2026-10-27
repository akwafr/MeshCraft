package s3900

import (
	"os"
	"testing"

	"meshcraft/internal/driver"
)

// TestParseSpanningTreeFromCapture replays the REAL "show spanning-tree"
// captured on the lab S3900-24T4S (FSOS 1.7.7, 2026-08-05): the parser must
// read the CIST (instance 0) and stop there — instance 1 repeats the same
// table with different roles (Eth 1/28 is MAST there, ROOT in the CIST) and
// folding both in would double every port.
func TestParseSpanningTreeFromCapture(t *testing.T) {
	var raw, err = os.ReadFile("testdata/show_spanning_tree.txt")
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	var st = parseSpanningTree(string(raw))

	if st.Mode != "mstp" || !st.Enabled {
		t.Errorf("mode/enabled = %q/%v, want mstp/true", st.Mode, st.Enabled)
	}
	if st.BridgePriority != 32768 || st.BridgeID != "64:9d:99:16:34:10" {
		t.Errorf("bridge = %d/%q", st.BridgePriority, st.BridgeID)
	}
	if st.RootPriority != 0 || st.RootBridgeID != "d4:01:c3:ab:32:f4" {
		t.Errorf("root = %d/%q", st.RootPriority, st.RootBridgeID)
	}
	if st.IsRoot {
		t.Error("this bridge is not the root and must not claim to be")
	}
	if len(st.Ports) != 28 {
		t.Fatalf("ports = %d, want 28 (instance 0 only — 56 means instance 1 leaked in)", len(st.Ports))
	}
	var byName = map[string]driver.SpanningTreePort{}
	var p driver.SpanningTreePort
	for _, p = range st.Ports {
		byName[p.Port] = p
	}
	var up = byName["GE28"]
	if up.Role != driver.STPRoleRoot || up.State != driver.STPStateForwarding || up.PathCost != 2000 {
		t.Errorf("GE28 = %+v, want root/forwarding/2000", up)
	}
	var alt = byName["GE2"]
	if alt.Role != driver.STPRoleAlternate || alt.State != driver.STPStateDiscarding {
		t.Errorf("GE2 = %+v, want alternate/discarding", alt)
	}
	var idle = byName["GE1"]
	if idle.Role != driver.STPRoleDisabled || idle.State != driver.STPStateDiscarding {
		t.Errorf("GE1 = %+v, want disabled/discarding", idle)
	}
}

func TestParseSpanningTreeEmptyWhenNoInstanceZero(t *testing.T) {
	var st = parseSpanningTree("show spanning-tree\n% something unexpected\n#")
	if st.Enabled || st.Mode != "" || len(st.Ports) != 0 {
		t.Errorf("an unrecognized output must parse to the zero state, got %+v", st)
	}
}
