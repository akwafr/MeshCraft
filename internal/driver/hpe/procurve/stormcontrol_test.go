package procurve

import (
	"strings"
	"testing"

	"meshcraft/internal/driver"
)

// The 2610-48 survey captured this range's DHCP snooping vocabulary because the
// unit was already running it. It carried no "broadcast-limit" statement, so
// nothing in stormcontrol.go is a line a switch printed back at us — and the
// staging rule (driver/stormcontrol.go) keeps the button off until one does.
func TestWritesExtraStormControlStaysFalse(t *testing.T) {
	var d = &Driver{}
	if d.WritesExtra("storm_control") {
		t.Fatal("WritesExtra announces storm_control on a command form no ProCurve has " +
			"ever printed back")
	}
	if !d.WritesExtra("stp") || !d.WritesExtra("port_admin") {
		t.Fatal("the two capabilities earned on hardware were lost")
	}
}

// THE POINT OF THIS CONNECTOR'S STORM CONTROL: two of the three classes do not
// exist on this hardware, and the capability answer says so BEFORE a click.
//
// The alternative — writing "broadcast-limit" when a screen asked for a
// multicast limit — would throttle a class nobody asked about while leaving the
// one that was asked about untouched, and the switch would report exactly what
// was written.
func TestStormControlCapsDeclareBroadcastOnly(t *testing.T) {
	var caps = (&Driver{}).StormControlCaps()
	if !caps.Expresses(driver.StormBroadcast) {
		t.Error("the one limiter this range HAS is not declared")
	}
	if caps.Expresses(driver.StormMulticast) || caps.Expresses(driver.StormUnknownUnicast) {
		t.Fatal("a class this hardware does not carry is declared: a screen reading this " +
			"would offer a control that can only be answered by writing the wrong one")
	}
	if !caps.Accepts(driver.StormRatePercent) || caps.Accepts(driver.StormRatePPS) {
		t.Error("\"broadcast-limit\" is a percentage of port bandwidth and nothing else")
	}
	if !strings.Contains(strings.Join(caps.Why, " "), "broadcast-limit") {
		t.Error("the capability answer must name the one statement this range has")
	}
}

// The wire form, pinned — including the removal, which must be "no
// broadcast-limit" and never "broadcast-limit 0". Zero is the floor of the
// range on this family, not the absence of a limit: writing it in place of a
// removal would throttle a port to nothing on a gesture labelled "remove".
func TestStormCmdsWireFormAndRemoval(t *testing.T) {
	var set = stormCmds(5, driver.StormLimit{
		Class: driver.StormBroadcast, Enabled: true, Rate: 5, Unit: driver.StormRatePercent,
	})
	var want = []string{"interface 5", "broadcast-limit 5", "exit"}
	var i int
	if len(set) != len(want) {
		t.Fatalf("got %v", set)
	}
	for i = range want {
		if set[i] != want[i] {
			t.Errorf("command %d = %q, want %q", i, set[i], want[i])
		}
	}

	var clear = stormCmds(5, driver.StormLimit{Class: driver.StormBroadcast})
	if clear[1] != "no broadcast-limit" {
		t.Fatalf("a removal was rendered as %q — on this family a zero is the floor of "+
			"the range, not the absence of a limit", clear[1])
	}
}

// Reading: a stanza with a limit, a stanza without one, and a VLAN block that
// must not be mistaken for a port.
//
// The "no limiter" answer is legitimate on this CLI because it records
// departures from its defaults rather than its defaults — the same argument
// lldp.go leans on for "LLDP is running unless told otherwise".
func TestParseStormControlReadsPortStanzas(t *testing.T) {
	var cfg = `
hostname "SW-PLAYER-79"
interface 5
   broadcast-limit 5
   exit
interface 6
   name "desk"
   exit
vlan 99
   ip address 203.0.113.79 255.255.255.0
   exit
`
	var got = parseStormControl(cfg)
	if got[5] != 5 {
		t.Errorf("port 5 = %d, want 5", got[5])
	}
	var v, seen = got[6]
	if !seen || v != 0 {
		t.Errorf("port 6 = %d (seen=%v), want a read stanza with no limiter", v, seen)
	}
	// "vlan 99" must not become port 99. A broadcast limit attributed to a
	// faceplate jack that does not exist is a limiter the operator cannot find.
	var _, wrong = got[99]
	if wrong {
		t.Error("a vlan block was read as a port stanza")
	}
}

// An explicit "no broadcast-limit" must read as no limiter, not as a limiter
// with an unreadable rate.
func TestParseStormControlHonoursExplicitNo(t *testing.T) {
	var got = parseStormControl("interface 7\n   no broadcast-limit\n   exit\n")
	if got[7] != 0 {
		t.Fatalf("\"no broadcast-limit\" read as %d", got[7])
	}
}

// A stanza's "exit" closes it. Without that, a `broadcast-limit` line appearing
// after the last port stanza — in a template's trailing block — would be
// attributed to whichever port happened to be read last.
func TestParseStormControlClosesStanzaOnExit(t *testing.T) {
	var got = parseStormControl("interface 3\n   exit\nbroadcast-limit 90\n")
	if got[3] != 0 {
		t.Fatalf("a line outside the stanza was attributed to port 3 (%d)", got[3])
	}
}
