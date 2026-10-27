package eos

// The faceplate grouping, pinned on the shape that broke it: core01.lab
// (DCS-7050SX-64, 48 SFP+ + 4 QSFP+) drawn as FOUR groups with three labels —
// "SFP 1G", "SFP+", a copper 1G, "SFP+" — because a 1000BASE-T module in
// Et17 negotiated a-1G and the grouper read the MODULE's speed as the socket's
// nature. Cf. portGroupsOf, cageClass and isCage.

import (
	"strings"
	"testing"
)

// statusRow builds one "show interfaces status" line.
func statusRow(port, status, speed, typ string) string {
	return port + "               " + status + "   trunk    full   " + speed + "    " + typ
}

// core01Status is the incident's table, reduced: 48 SFP+ cages of which one
// carries a copper transceiver at a-1G (Et17) and one an unidentified module
// (Et9 "Unknown"), plus the four QSFP cages split into 4×10G lanes and the
// non-faceplate rows the parser must keep ignoring.
func core01Status() string {
	var rows = []string{"Port       Name   Status       Vlan     Duplex Speed  Type         Flags Encapsulation"}
	var i int
	for i = 1; i <= 48; i++ {
		var port = "Et" + itoa(i)
		switch i {
		case 17:
			rows = append(rows, statusRow(port, "connected", "a-1G", "1000BASE-T"))
		case 9:
			rows = append(rows, statusRow(port, "connected", "10G", "Unknown"))
		default:
			rows = append(rows, statusRow(port, "notconnect", "10G", "Not Present"))
		}
	}
	for i = 49; i <= 52; i++ {
		var lane int
		for lane = 1; lane <= 4; lane++ {
			rows = append(rows, statusRow("Et"+itoa(i)+"/"+itoa(lane), "notconnect", "10G", "Not Present"))
		}
	}
	rows = append(rows,
		statusRow("Ma1", "notconnect", "auto", "10/100/1000"),
		statusRow("Po11", "connected", "20G", "N/A"))
	return strings.Join(rows, "\n") + "\n"
}

func itoa(n int) string {
	var digits = "0123456789"
	if n < 10 {
		return string(digits[n])
	}
	return string(digits[n/10]) + string(digits[n%10])
}

func TestFaceplateIsGroupedByCageNotByModuleSpeed(t *testing.T) {
	var groups = portGroupsOf(parseInterfacesStatus(core01Status()))
	if len(groups) != 1 {
		t.Fatalf("want ONE group of 48 SFP+ cages, got %d: %v", len(groups), groups)
	}
	var g = groups[0]
	if g["from"] != 1 || g["count"] != 48 {
		t.Errorf("group covers from=%v count=%v, want 1..48", g["from"], g["count"])
	}
	if g["label"] != "SFP+" || g["speed"] != "10G" {
		t.Errorf("group is %v %v, want SFP+ 10G — the a-1G copper module in Et17 "+
			"must not rename the socket", g["label"], g["speed"])
	}
}

// An UNSPLIT QSFP+ block stays its own group: 40G is a different form factor,
// not a faster module in the same cage.
func TestUnsplitQSFPBlockIsItsOwnGroup(t *testing.T) {
	var rows = []string{"Port       Name   Status       Vlan     Duplex Speed  Type         Flags Encapsulation"}
	var i int
	for i = 1; i <= 4; i++ {
		rows = append(rows, statusRow("Et"+itoa(i), "notconnect", "10G", "Not Present"))
	}
	for i = 5; i <= 8; i++ {
		rows = append(rows, statusRow("Et"+itoa(i), "notconnect", "40G", "Not Present"))
	}
	var groups = portGroupsOf(parseInterfacesStatus(strings.Join(rows, "\n") + "\n"))
	if len(groups) != 2 {
		t.Fatalf("want SFP+ block + QSFP+ block, got %d: %v", len(groups), groups)
	}
	if groups[0]["label"] != "SFP+" || groups[0]["count"] != 4 {
		t.Errorf("first group = %v", groups[0])
	}
	if groups[1]["label"] != "QSFP+" || groups[1]["count"] != 4 || groups[1]["from"] != 5 {
		t.Errorf("second group = %v, want QSFP+ ×4 from 5", groups[1])
	}
}

// Built-in copper announces its PHY ladder ("10/100/1000") and stays copper —
// and two copper blocks at different speeds stay two blocks, because on a
// multigig faceplate the speed IS the hardware difference.
func TestCopperLadderStaysCopperAndSplitsBySpeed(t *testing.T) {
	var rows = []string{"Port       Name   Status       Vlan     Duplex Speed  Type         Flags Encapsulation"}
	var i int
	for i = 1; i <= 2; i++ {
		rows = append(rows, statusRow("Et"+itoa(i), "notconnect", "1G", "10/100/1000"))
	}
	for i = 3; i <= 4; i++ {
		rows = append(rows, statusRow("Et"+itoa(i), "notconnect", "2.5G", "100/1000/2.5G"))
	}
	var groups = portGroupsOf(parseInterfacesStatus(strings.Join(rows, "\n") + "\n"))
	if len(groups) != 2 {
		t.Fatalf("want two copper blocks, got %d: %v", len(groups), groups)
	}
	if groups[0]["label"] != "" || groups[1]["label"] != "" {
		t.Errorf("copper groups carry no cage label: %v / %v", groups[0]["label"], groups[1]["label"])
	}
}

// The split cages feed a NOTE, never a port: the refusal of lane names is a
// numbering decision (cf. rePortEOS), and this is where its cost stops being
// invisible.
func TestSplitCagesAreCountedAndSaid(t *testing.T) {
	var lanes = parseBreakoutCages(core01Status())
	if len(lanes) != 4 || lanes[49] != 4 || lanes[52] != 4 {
		t.Fatalf("lanes = %v, want 4 cages × 4 lanes", lanes)
	}
	var note = breakoutNote(lanes)
	if !strings.Contains(note, "Et49 (4 lanes)") || !strings.Contains(note, "Et52 (4 lanes)") {
		t.Errorf("note does not name the cages: %q", note)
	}
	if breakoutNote(nil) != "" {
		t.Error("no split, no note")
	}
}
