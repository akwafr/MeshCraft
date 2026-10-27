package edgeswitch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"meshcraft/internal/driver"
)

// Declared 2026-08-08. The exercise that earned it found TWO defects, which is
// why this assertion is worth keeping rather than deleting: the builder set a
// threshold without ever enabling the limiter, and it left the session in Global
// Config, which then broke the caller's next gesture, its persist and its revert.
//
// If this ever fails because someone flipped it back, the question is whether
// live_write_test.go's TestLiveWriteStormControl still passes (ES_LIVE_WRITE=1).
func TestWritesExtraStormControlIsDeclaredOnLiveProof(t *testing.T) {
	var d = &Driver{}
	if !d.WritesExtra("storm_control") {
		t.Fatal("\"storm_control\" was proven live on 2026-08-08 — both claimable " +
			"classes written through ApplyStormControl, read back, a second identical " +
			"apply writing nothing, a removal leaving the unnamed class alone, and the " +
			"revert saved — and must stay declared unless that evidence is withdrawn")
	}
	// The three earned on hardware must survive.
	var name string
	for _, name = range []string{"poe", "port_power_cycle", "port_admin"} {
		if !d.WritesExtra(name) {
			t.Errorf("the %q capability, earned on hardware, was lost", name)
		}
	}
}

// TWO classes, not three, and the reason is the whole point of Classes being
// separate from WritesExtra.
//
// The hardware has a third limiter and this connector can drive it. What cannot
// be established without a traffic generator is WHICH FRAMES the vendor's
// "unicast" keyword covers — and Cisco spells its ALL-unicast limiter with the
// same word, which is why that connector refuses the class outright. Declaring
// it here on the strength of wording would be exactly the substitution
// driver/stormcontrol.go forbids.
//
// Withheld until a known unicast flow has been pushed through a limited port and
// seen not to be policed. The Why must say so, so a screen can tell "not
// verified yet" from "this hardware cannot".
func TestStormControlCapsWithholdUnknownUnicastUntilTrafficProvesIt(t *testing.T) {
	var caps = (&Driver{}).StormControlCaps()
	if !caps.Expresses(driver.StormBroadcast) || !caps.Expresses(driver.StormMulticast) {
		t.Error("broadcast and multicast are proven live and must be declared")
	}
	if caps.Expresses(driver.StormUnknownUnicast) {
		t.Error("unknown-unicast is declared on the vendor's wording alone — it needs " +
			"a known unicast flow through a limited port, confirmed NOT policed")
	}
	// The absence must be EXPLAINED, or a screen greys the control out for the
	// wrong reason.
	if len(caps.Why) == 0 {
		t.Error("a withheld class with no sentence saying why")
	}
	if !caps.Accepts(driver.StormRatePercent) || !caps.Accepts(driver.StormRatePPS) {
		t.Error("\"level\" and \"rate\" are both available on this family")
	}
	// No bits-per-second form exists here, and a bps rate must be refused rather
	// than converted — the conversion needs a frame size nobody chose.
	if caps.Accepts(driver.StormRateBPS) {
		t.Error("a bits-per-second form is claimed on a family that has none")
	}
	var joined = strings.Join(caps.Why, " ")
	if !strings.Contains(joined, "UNKNOWN unicast storm recovery") {
		t.Error("the capability answer does not say WHY \"unicast\" is mapped to the " +
			"flooding class here and refused on cisco/ios")
	}
	if !strings.Contains(joined, "has not been exercised") {
		t.Error("the capability answer does not admit the mapping is documentary")
	}
}

// The two units are told apart by the VERB, and getting it wrong turns 1000
// packets a second into 1000 % of line rate.
func TestStormCmdsUseLevelForPercentAndRateForPPS(t *testing.T) {
	var cmds = stormCmds("0/5", []driver.StormLimit{
		{Class: driver.StormBroadcast, Enabled: true, Rate: 5, Unit: driver.StormRatePercent},
		{Class: driver.StormMulticast, Enabled: true, Rate: 1000, Unit: driver.StormRatePPS},
		{Class: driver.StormUnknownUnicast, Enabled: false},
	})
	// The ENABLE precedes the threshold for each class, and the block closes
	// BOTH the interface and Global Config — cf. stormCmds, where the exercise
	// that established each is written down. A level without its enable leaves a
	// threshold nothing enforces; a missing second exit leaves the session in
	// config mode and breaks the caller's next gesture, its persist and its
	// revert.
	var want = []string{
		"interface 0/5",
		"storm-control broadcast",
		"storm-control broadcast level 5",
		"storm-control multicast",
		"storm-control multicast rate 1000",
		"no storm-control unicast",
		"exit",
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

// A removal carries NO threshold. A "level 0" here would drop every frame of
// the class rather than remove the limiter.
func TestStormCmdsRemovalCarriesNoThreshold(t *testing.T) {
	var cmds = stormCmds("0/1", []driver.StormLimit{
		{Class: driver.StormBroadcast, Enabled: false},
	})
	if cmds[1] != "no storm-control broadcast" {
		t.Fatalf("removal rendered as %q", cmds[1])
	}
}

// The read: rows in both units, plus a disabled one. The unit MARKER the device
// printed is what decides — guessing would be the five-orders-of-magnitude
// mistake the contract exists to stop.
func TestParseStormControlKeepsThePrintedUnit(t *testing.T) {
	// The captured wide shape (cf. reStormRow), with the states varied so each
	// column-group is read independently. The "Disable" cells still carry a
	// level, exactly as the lab unit prints them.
	//
	// CONSTRUCTED for the enabled cases: the lab unit has only ever been seen
	// with every class disabled, so an enabled row and the pps spelling are
	// deduced from the column layout rather than captured. The captured fixture
	// is asserted separately in TestParseStormControlOnCapturedHardware.
	var out = `
       Bcast   Bcast    Bcast      Mcast   Mcast    Mcast      Ucast   Ucast    Ucast      Flow
 Intf  Mode    Level    Action     Mode    Level    Action     Mode    Level    Action     Mode
------ ------- -------- ---------- ------- -------- ---------- ------- -------- ---------- ------
0/1    Enable  5%       None       Disable 5%       None       Enable  1000pps  None       Disable
0/2    Enable  10%      None       Disable 5%       None       Disable 5%       None       Disable
`
	var ports = parseStormControl(out)
	if len(ports) != 2 {
		t.Fatalf("got %d ports: %+v", len(ports), ports)
	}
	if ports[0].Port != "GE1" || ports[1].Port != "GE2" {
		t.Fatalf("ports = %s, %s", ports[0].Port, ports[1].Port)
	}
	var bc, _ = ports[0].Limit(driver.StormBroadcast)
	if !bc.Enabled || bc.Rate != 5 || bc.Unit != driver.StormRatePercent {
		t.Errorf("GE1 broadcast = %+v, want 5 percent enabled", bc)
	}
	// A disabled class is READ and reported disabled — which is a different
	// claim from absent, and the level beside it must not make it enabled.
	var mc, ok = ports[0].Limit(driver.StormMulticast)
	if !ok || mc.Enabled {
		t.Errorf("GE1 multicast = %+v, want a read disabled limiter", mc)
	}
	var uu, _ = ports[0].Limit(driver.StormUnknownUnicast)
	if !uu.Enabled || uu.Rate != 1000 || uu.Unit != driver.StormRatePPS {
		t.Errorf("GE1 unicast = %+v, want 1000 pps", uu)
	}
	// The classes come back in the contract's canonical order, so two identical
	// reads produce two identical payloads.
	var i int
	for i = range ports[0].Limits {
		if ports[0].Limits[i].Class != driver.StormClasses[i] {
			t.Errorf("class %d is %s, want %s", i,
				ports[0].Limits[i].Class, driver.StormClasses[i])
		}
	}
}

// An ENABLED class whose threshold cell cannot be read must be left OUT of the
// list, not reported at zero. Absent means "not read"; zero would mean "policed
// at nothing", and on the broadcast class that reads as a limiter dropping every
// frame — the opposite of the truth.
func TestParseStormControlLeavesUnreadableThresholdUnread(t *testing.T) {
	var out = `
 Intf  Mode    Level    Action     Mode    Level    Action     Mode    Level    Action     Mode
0/1    Enable  ---      None       Disable 5%       None       Disable 5%       None       Disable
`
	var ports = parseStormControl(out)
	if len(ports) != 1 {
		t.Fatalf("got %d ports: %+v", len(ports), ports)
	}
	var bc, read = ports[0].Limit(driver.StormBroadcast)
	if read {
		t.Fatalf("an enabled limiter with an unreadable threshold was reported as "+
			"%+v; it must be absent from the list instead", bc)
	}
	// The two classes beside it are still read — one bad cell does not discard
	// the row.
	var mc, mread = ports[0].Limit(driver.StormMulticast)
	if !mread || mc.Enabled {
		t.Errorf("multicast = %+v (read=%v), want a read disabled limiter", mc, mread)
	}
}

// A firmware that does not carry the command must be told apart from a switch
// with no limiters — this regex is what does it, and an empty state on a read
// failure would read on screen as "nothing is limited here".
func TestStormAbsentRecognisesTheCLIsRefusals(t *testing.T) {
	var shape string
	for _, shape = range []string{
		"% Invalid input detected at '^' marker.",
		"Unrecognized command",
		"Incomplete command",
	} {
		if !reStormAbsent.MatchString(shape) {
			t.Errorf("%q is not recognised as \"this CLI has no such command\"", shape)
		}
	}
	if reStormAbsent.MatchString("0/1  broadcast  Enabled  5 %") {
		t.Error("an ordinary data row was read as a CLI refusal")
	}
}

// TestParseStormControlOnCapturedHardware holds the parser to the row shape the
// lab EP-S16 actually serves, which is NOT the one it was written against.
//
// Captured 2026-08-08 (EP-S16, 1.9.3) by the connector's own read path. Until
// then this parser expected one row per (port, class) — "0/1 broadcast Disable
// 5%". The device serves ONE ROW PER PORT with all three classes side by side,
// so the old pattern matched nothing at all and the read returned zero ports.
//
// That failure was the SAFE one, and it is worth saying why: the connector
// reported the result as UNREAD rather than as "no port is limited", so nothing
// downstream ever claimed a switch was unprotected on the strength of a parse
// that had failed. The note it emitted is what made the mismatch visible.
//
// THE ASSERTION THAT MATTERS MOST is the last one. Every port on this unit
// prints "Disable 5%": the level is a stored default that exists whether or
// not the limiter runs. A parser reading the level alone would report all 18
// ports policed at 5 % — a switch that looks protected and is not, which is the
// exact inversion driver/stormcontrol.go was written to prevent.
func TestParseStormControlOnCapturedHardware(t *testing.T) {
	var raw, err = os.ReadFile(filepath.Join("testdata", "show_storm_control_all.txt"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var ports = parseStormControl(string(raw))

	// 18 faceplate ports on the EP-S16, and the header rows must not become one.
	if len(ports) != 18 {
		t.Fatalf("read %d ports, want 18 — the \"Intf\" header and the rule of "+
			"dashes must not parse as ports: %+v", len(ports), ports)
	}
	if ports[0].Port != "GE1" {
		t.Errorf("first port = %q, want GE1 in faceplate order", ports[0].Port)
	}

	// All three classes are reported for every port, because the device prints a
	// column for each — absent would mean NOT READ, which is a different claim.
	var p driver.StormControlPortState
	for _, p = range ports {
		if len(p.Limits) != 3 {
			t.Fatalf("%s carries %d classes, want 3: %+v", p.Port, len(p.Limits), p.Limits)
		}
	}

	// THE ONE THAT GUARDS AGAINST FALSE PROTECTION.
	for _, p = range ports {
		var l driver.StormLimit
		for _, l = range p.Limits {
			if l.Enabled {
				t.Errorf("%s %s reads ENABLED from a \"Disable 5%%\" cell — the Level "+
					"column is a stored default and Mode is the enable flag; reading "+
					"the level alone reports an unprotected switch as policed",
					p.Port, l.Class)
			}
		}
	}
}
