package s3400

import (
	"os"
	"testing"
)

// The driver.LLDPReader adapter, replayed on the CAPTURED lab table
// (labLLDPNeighbor, parse_test.go): one entry whose Device-ID column is
// EMPTY, with no detail answer for it — the degraded path.
func TestLLDPObservationFromCapture(t *testing.T) {
	var rows = parseLLDPNeighbors(labLLDPNeighbor)
	var obs = lldpObservationFrom(rows, nil, true)
	if !obs.Enabled {
		t.Error("enabled=false for a table the device served")
	}
	if len(obs.Neighbors) != 1 {
		t.Fatalf("neighbours=%+v", obs.Neighbors)
	}
	var n = obs.Neighbors[0]
	if n.LocalPort != "XG2" || n.PortID != "Eth 1/25" {
		t.Errorf("local %q, port id %q — want the capture's XG2 / \"Eth 1/25\"",
			n.LocalPort, n.PortID)
	}
	if n.SysName != "" {
		t.Errorf("sys name %q: the capture's Device-ID column is blank, and inventing a "+
			"name would feed the recognition index a lie", n.SysName)
	}
	if n.ChassisID != "" {
		t.Errorf("chassis %q: no detail answer for this port, and the summary table "+
			"serves NO chassis column", n.ChassisID)
	}
}

// TestLLDPObservationDisabled: "LLDP is not enabled" is a constat carried
// as Enabled=false with a note, never an error — an operator asking "who is
// on the other end" deserves the reason, not a failed read.
func TestLLDPObservationDisabled(t *testing.T) {
	var obs = lldpObservationFrom(nil, nil, false)
	if obs.Enabled || len(obs.Neighbors) != 0 {
		t.Errorf("obs=%+v", obs)
	}
	if len(obs.Notes) == 0 {
		t.Error("no note: the screen must be able to say WHY the table is empty")
	}
}

// TestUnswapNibbles pins the display bug with BOTH live proofs (2026-08-05):
// two neighbours whose real base MACs are known from their own connectors,
// each displayed by this firmware with the nibbles swapped in every byte.
func TestUnswapNibbles(t *testing.T) {
	var got = unswapNibbles("c950.6d5f.1f73")
	if got != "9c:05:d6:f5:f1:37" {
		t.Errorf("EdgeSwitch: got %q", got)
	}
	got = unswapNibbles("46d9.9961.4301")
	if got != "64:9d:99:16:34:10" {
		t.Errorf("S3900: got %q", got)
	}
	got = unswapNibbles("salon-sw01")
	if got != "" {
		t.Errorf("a non-MAC token must pass through untouched, got %q", got)
	}
}

// TestLLDPObservationFromDetailCapture replays the REAL two-step read: the
// compact table (both rows) plus the real per-interface detail answers
// captured on the lab unit. The chassis identities that leave the connector
// must be the neighbours' REAL addresses — unswapped — and the swapped MAC
// masquerading as a Device-ID must not survive as a system name.
func TestLLDPObservationFromDetailCapture(t *testing.T) {
	var raw, err = os.ReadFile("testdata/show_lldp_neighbor.txt")
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	var detail1, err1 = os.ReadFile("testdata/show_lldp_neighbor_tg01.txt")
	var detailAll, err2 = os.ReadFile("testdata/show_lldp_neighbor_detail.txt")
	if err1 != nil || err2 != nil {
		t.Fatalf("detail captures: %v / %v", err1, err2)
	}
	var rows = parseLLDPNeighbors(string(raw))
	if len(rows) != 2 {
		t.Fatalf("rows=%+v, want the 2 captured neighbours", rows)
	}
	// XG1 gets its own per-interface answer; XG2 reuses the second entry of
	// the all-neighbours detail (same field shape — parseLLDPNeighborDetail
	// keeps the LAST value of duplicated fields, so feed it only XG2's slice).
	var xg2Detail = string(detailAll)
	var cut = len(xg2Detail)
	// Named "second", not "i": the neighbour loop below declares its own i.
	var second = indexOfSecondEntry(xg2Detail)
	if second > 0 {
		xg2Detail = xg2Detail[second:cut]
	}
	var obs = lldpObservationFrom(rows, map[string]string{
		"XG1": string(detail1),
		"XG2": xg2Detail,
	}, true)
	var byPort = map[string]int{}
	var i int
	for i = range obs.Neighbors {
		byPort[obs.Neighbors[i].LocalPort] = i
	}
	var xg1 = obs.Neighbors[byPort["XG1"]]
	if xg1.ChassisID != "9c:05:d6:f5:f1:37" {
		t.Errorf("XG1 chassis %q, want the EdgeSwitch's REAL MAC", xg1.ChassisID)
	}
	if xg1.SysName != "" {
		t.Errorf("XG1 sys name %q: the compact Device-ID was the swapped chassis, not a name", xg1.SysName)
	}
	var xg2 = obs.Neighbors[byPort["XG2"]]
	if xg2.ChassisID != "64:9d:99:16:34:10" {
		t.Errorf("XG2 chassis %q, want the S3900's REAL MAC", xg2.ChassisID)
	}
	if xg2.SysDesc != "S3900-24T4S" {
		t.Errorf("XG2 sys desc %q", xg2.SysDesc)
	}
}

// indexOfSecondEntry finds the start of the second detail entry (after the
// first entry separator line of dashes).
func indexOfSecondEntry(s string) int {
	var sep = "------------------------------------------"
	var i = indexOf(s, sep)
	if i < 0 {
		return -1
	}
	return i + len(sep)
}

func indexOf(s, sub string) int {
	var i int
	for i = 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
