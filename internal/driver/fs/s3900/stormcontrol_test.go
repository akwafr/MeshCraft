package s3900

import (
	"strings"
	"testing"

	"meshcraft/internal/driver"
)

// TWO independent reasons keep this undeclared, and either alone would be enough:
// not one of these statements has been sent to an S3900, and the unit of
// "packet-rate" is not settled on this lineage.
func TestWritesExtraStormControlStaysFalse(t *testing.T) {
	var d = &Driver{}
	if d.WritesExtra("storm_control") {
		t.Fatal("WritesExtra announces storm_control while the unit of \"packet-rate\" is " +
			"still ambiguous on this lineage — a limit written now could be eight thousand " +
			"times tighter than intended")
	}
	if !d.WritesExtra("port_admin") {
		t.Fatal("the capability earned on the lab unit was lost")
	}
}

// All three classes ARE named as themselves here — "unknown-unicast" is spelled
// out, unlike Cisco's "unicast" — and the capability answer must still carry the
// unit caveat, because that is the fact an operator has to read before typing a
// number.
func TestStormControlCapsDeclareThreeClassesAndTheUnitCaveat(t *testing.T) {
	var caps = (&Driver{}).StormControlCaps()
	var class driver.StormClass
	for _, class = range driver.StormClasses {
		if !caps.Expresses(class) {
			t.Errorf("%s is not declared on a lineage that spells it out", class)
		}
	}
	if !caps.Accepts(driver.StormRatePPS) {
		t.Error("packets per second is the unit this connector sends")
	}
	if caps.Accepts(driver.StormRatePercent) || caps.Accepts(driver.StormRateBPS) {
		t.Error("a unit this connector refuses to convert into is declared as accepted")
	}
	if !strings.Contains(strings.Join(caps.Why, " "), "NOT SETTLED") {
		t.Error("the capability answer hides that the unit is unsettled — that is the one " +
			"fact an operator must read before typing a threshold")
	}
}

// The wire form, pinned. "unknown-unicast" is the word that makes this lineage
// different from cisco/ios, and shortening it would silently change the class.
func TestStormCmdsWireForm(t *testing.T) {
	var cmds = stormCmds("ethernet 1/11", []driver.StormLimit{
		{Class: driver.StormBroadcast, Enabled: true, Rate: 500, Unit: driver.StormRatePPS},
		{Class: driver.StormUnknownUnicast, Enabled: false},
	})
	var want = []string{
		"interface ethernet 1/11",
		"switchport broadcast packet-rate 500",
		"no switchport unknown-unicast packet-rate",
		"exit",
	}
	if len(cmds) != len(want) {
		t.Fatalf("got %v", cmds)
	}
	var i int
	for i = range want {
		if cmds[i] != want[i] {
			t.Errorf("command %d = %q, want %q", i, cmds[i], want[i])
		}
	}
}

// A removal carries NO number. "packet-rate 0" on this lineage is a limiter
// that drops the whole class, not the absence of one.
func TestStormCmdsRemovalCarriesNoRate(t *testing.T) {
	var cmds = stormCmds("ethernet 1/1", []driver.StormLimit{
		{Class: driver.StormMulticast, Enabled: false},
	})
	var line string
	for _, line = range cmds {
		if strings.Contains(line, "packet-rate 0") {
			t.Fatalf("a removal was rendered as a zero threshold: %q", line)
		}
	}
}

// The read: an interface block with two statements, one without any, and a VLAN
// block that must not be mistaken for a port.
func TestParseStormControlReadsEthernetBlocksOnly(t *testing.T) {
	var cfg = `
interface ethernet 1/1
 switchport broadcast packet-rate 500
 switchport unknown-unicast packet-rate 100
!
interface ethernet 1/2
 switchport mode access
!
interface vlan 99
 ip address 10.0.0.9 255.255.255.0
!
`
	var ports = parseStormControl(cfg)
	if len(ports) != 2 {
		t.Fatalf("got %d ports: %+v", len(ports), ports)
	}
	var bc, _ = ports[0].Limit(driver.StormBroadcast)
	if !bc.Enabled || bc.Rate != 500 || bc.Unit != driver.StormRatePPS {
		t.Errorf("GE1 broadcast = %+v", bc)
	}
	var uu, _ = ports[0].Limit(driver.StormUnknownUnicast)
	if !uu.Enabled || uu.Rate != 100 {
		t.Errorf("GE1 unknown-unicast = %+v", uu)
	}
	// Silence inside a block that WAS read is an observation on this CLI: no
	// multicast statement means no multicast limiter, not an unread class.
	var mc, read = ports[0].Limit(driver.StormMulticast)
	if !read || mc.Enabled {
		t.Errorf("GE1 multicast = %+v (read=%v), want a read absence", mc, read)
	}
	// A port block with nothing at all: three explicit absences.
	if len(ports[1].Limits) != 3 {
		t.Fatalf("GE2 reports %d classes, want 3", len(ports[1].Limits))
	}
}

// "interface vlan 99" must never contribute a limiter: one attributed to a
// faceplate port that does not carry it is a setting the operator cannot find.
func TestParseStormControlIgnoresNonEthernetBlocks(t *testing.T) {
	var ports = parseStormControl(
		"interface vlan 99\n switchport broadcast packet-rate 500\n!\n")
	if len(ports) != 0 {
		t.Fatalf("a VLAN block produced port state: %+v", ports)
	}
}

// The faceplate order, numerically: a panel listing fifty ports must not put GE10
// between GE1 and GE2.
func TestSortStormPortsIsNumeric(t *testing.T) {
	var ps = []driver.StormControlPortState{{Port: "GE10"}, {Port: "GE2"}, {Port: "GE1"}}
	driver.SortByPortName(ps, stormPortName, stormSplitPort)
	if ps[0].Port != "GE1" || ps[1].Port != "GE2" || ps[2].Port != "GE10" {
		t.Fatalf("order = %s %s %s", ps[0].Port, ps[1].Port, ps[2].Port)
	}
}
