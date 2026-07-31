package routeros

import (
	"os"
	"path/filepath"
	"testing"
)

// testdata/ captures are REAL RESPONSES from the reference hardware.
//
// Collected 28 July 2026 on a MikroTik CRS326-24G-2S+,
// RouterOS 7.21beta3 (testing), serial 945509EF00B0, reached through a meshcraft proxy
// tunnel. Not hand-written examples to pass tests: proof of what the connector claims.
//
// Hence their role in this package: every numeric Capabilities value is annotated with
// an observation, and these files are that observation. A test that replays them verifies
// two things at once — that the code reads correctly, and that what it claims to have seen
// is what the device answered.
//
// What they do NOT prove, and you must know when reading them: no WRITE appears. The only
// channel to this device from the development host is restricted to GET/HEAD. apply.go and
// admin.go tests therefore prove apply LOGIC on these real states, not the device's reaction
// to apply.

// referenceModel is the hardware on which all observations were made.
const referenceModel = "CRS326-24G-2S+"

// fixture loads a capture and decodes it as the client would.
func fixture(t *testing.T, name string) []Row {
	t.Helper()
	var raw, err = os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("capture %s unreadable: %v", name, err)
	}
	var rows []Row
	rows, err = decodeRows(string(raw))
	if err != nil {
		t.Fatalf("capture %s undecodable: %v", name, err)
	}
	return rows
}

// fixtureOne loads a capture that returns a single object.
func fixtureOne(t *testing.T, name string) Row {
	t.Helper()
	var rows = fixture(t, name)
	if len(rows) != 1 {
		t.Fatalf("capture %s: %d records, 1 expected", name, len(rows))
	}
	return rows[0]
}

// stateFromFixtures rebuilds reference device state from captures, exactly as ReadState
// would on hardware.
//
// Entry point for all state and apply tests: they work on the REAL configuration of a
// switch in service, with its quirks — not a textbook case that smoothed them away.
func stateFromFixtures(t *testing.T) DeviceState {
	t.Helper()
	var bridges = fixture(t, "interface_bridge.json")
	if len(bridges) != 1 {
		t.Fatalf("expected one bridge in capture, found %d", len(bridges))
	}
	var out = DeviceState{
		BridgeName:    bridges[0].Get("name"),
		VLANFiltering: bridges[0].Bool("vlan-filtering"),
		LinkUp:        map[string]bool{},
	}
	out.VLANs, out.Ports, out.CPUTagged = foldVLANs(
		fixture(t, "interface_bridge_vlan.json"),
		fixture(t, "interface_bridge_port.json"),
		out.BridgeName)

	var r Row
	for _, r = range fixture(t, "interface.json") {
		if r.Bool("running") {
			out.LinkUp[r.Get("name")] = true
		}
	}
	foldMgmt(fixture(t, "ip_address.json"), fixture(t, "interface_vlan.json"), &out)
	return out
}
