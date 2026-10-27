package eos

import (
	"strings"
	"testing"

	"meshcraft/internal/driver"
)

// THE TEST THAT MUST NOT GO GREEN BY BEING DELETED.
//
// The staging rule (driver/stormcontrol.go, § on StormControlApplier) is that a
// connector whose forms come from documentation ships the code and leaves the
// capability undeclared, so that the first live exercise happened on a lab switch
// somebody chose rather than on a customer's uplink because a button existed.
// That exercise has now happened (2026-08-08, cf. stormcontrol.go's header and
// stormcontrol_live_test.go), so this assertion has been inverted: it now guards
// the declaration rather than the silence. What it protects is unchanged — that
// the flag and the evidence move together.
func TestWritesExtraStormControlIsDeclaredOnLiveProof(t *testing.T) {
	var d = &Driver{}
	if !d.WritesExtra("storm_control") {
		t.Fatal("\"storm_control\" was proven live on 2026-08-08 — all three classes " +
			"written through ApplyStormControl, read back, a second identical apply " +
			"writing nothing, and a removal leaving the unnamed classes alone — and " +
			"must stay declared unless that evidence is withdrawn")
	}
	// The declaration is per intent and never a blanket yes. These two have
	// implementations in this package too, and neither has been exercised on an
	// Arista: a screen must not offer them because a sibling earned its button.
	//
	// "port_admin" left this list on 2026-08-10, on its OWN capture and not on
	// this one — "shutdown" on Ethernet1 of core01.lab, "disabled" read
	// back, "no shutdown" reverting it. It is pinned UP in eos_test.go and in
	// portadmin_test.go, so dropping it here does not leave it unguarded.
	// "stp" left this list on 2026-08-12 on its OWN capture — the applier wrote
	// the root bridge to core01.lab and the device read it back. Pinned up in
	// eos_test.go, so dropping it here leaves it guarded.
	var name string
	for _, name = range []string{"dhcp_snooping"} {
		if d.WritesExtra(name) {
			t.Errorf("declares %q writable on a form nothing has exercised here", name)
		}
	}
}

// The CAPABILITY answer is a different question from WritesExtra, and this is the
// platform where the difference is visible: EOS expresses all three classes, and
// meshcraft still has not proven the form. A screen must be able to say "not
// verified on this range yet" rather than "this switch cannot".
func TestStormControlCapsDeclareAllThreeClasses(t *testing.T) {
	var caps = (&Driver{}).StormControlCaps()
	var class driver.StormClass
	for _, class = range driver.StormClasses {
		if !caps.Expresses(class) {
			t.Errorf("EOS names %s as itself and the caps do not declare it", class)
		}
	}
	if !caps.Accepts(driver.StormRatePercent) {
		t.Error("EOS's level argument is a percentage and the caps do not declare it")
	}
	// pps and kbps are NOT claimed, and the file header says why: they exist on
	// some forwarding hardware in this range and not on others, so declaring them
	// would put a box on screen for a switch that would reject the line.
	if caps.Accepts(driver.StormRatePPS) || caps.Accepts(driver.StormRateBPS) {
		t.Error("a unit this connector does not write is declared as accepted")
	}
	if caps.SharedRate {
		t.Error("EOS carries one rate PER CLASS, not one shared per port")
	}
}

// The wire form, pinned. "unknown-unicast" is the word that distinguishes this
// platform from cisco/ios, and a refactor that shortened it to "unicast" would
// silently install a limiter on established flows.
func TestStormCmdsWireForm(t *testing.T) {
	var cmds = stormCmds("Ethernet3", []driver.StormLimit{
		{Class: driver.StormBroadcast, Enabled: true, Rate: 5, Unit: driver.StormRatePercent},
		{Class: driver.StormUnknownUnicast, Enabled: false},
	})
	var want = []string{
		"interface Ethernet3",
		"storm-control broadcast level 5",
		"no storm-control unknown-unicast",
		"exit",
	}
	if len(cmds) != len(want) {
		t.Fatalf("got %d commands, want %d: %v", len(cmds), len(want), cmds)
	}
	var i int
	for i = range want {
		if cmds[i] != want[i] {
			t.Errorf("command %d = %q, want %q", i, cmds[i], want[i])
		}
	}
}

// A REMOVAL must be "no storm-control X", never "level 0". On this fleet a
// level of zero drops every frame of the class, and on broadcast that takes ARP
// with it — the port stops passing anything at all.
func TestStormCmdsNeverRenderDisableAsZero(t *testing.T) {
	var cmds = stormCmds("Ethernet1", []driver.StormLimit{
		{Class: driver.StormMulticast, Enabled: false},
	})
	var line string
	for _, line = range cmds {
		if strings.Contains(line, "level 0") {
			t.Fatalf("a removal was rendered as a zero threshold: %q", line)
		}
	}
}

// The rate keeps the operator's own spelling and does not gain six decimals. The
// transcript is read by a human, and on an unproven command set it is the
// artefact the next survey compares against.
func TestStormCmdsRateSpelling(t *testing.T) {
	var cmds = stormCmds("Ethernet1", []driver.StormLimit{
		{Class: driver.StormBroadcast, Enabled: true, Rate: 0.5, Unit: driver.StormRatePercent},
	})
	if cmds[1] != "storm-control broadcast level 0.5" {
		t.Fatalf("rate rendered as %q", cmds[1])
	}
}

// Reading a running configuration: the three classes, one of them absent.
//
// SILENCE INSIDE A BLOCK THAT WAS READ IS AN OBSERVATION on this CLI, because it
// prints only non-default lines — so a port whose block carries a broadcast limit
// and nothing else genuinely has no multicast limiter, and reporting that class
// as unread would make the apply rewrite a line already in place on every pass.
func TestParseStormControlReadsAndCompletesTheBlock(t *testing.T) {
	var cfg = `
interface Ethernet1
   description uplink
   storm-control broadcast level 5.00
   storm-control unknown-unicast level 1
!
interface Ethernet2
   switchport access vlan 20
!
interface Management1
   ip address 10.0.0.2/24
!
`
	var ports = parseStormControl(cfg)
	if len(ports) != 2 {
		t.Fatalf("got %d ports, want 2 (Management1 is not a faceplate port): %+v", len(ports), ports)
	}
	if ports[0].Port != "ET1" || ports[1].Port != "ET2" {
		t.Fatalf("ports out of faceplate order: %s, %s", ports[0].Port, ports[1].Port)
	}

	var bc, ok = ports[0].Limit(driver.StormBroadcast)
	if !ok || !bc.Enabled || bc.Rate != 5 || bc.Unit != driver.StormRatePercent {
		t.Errorf("ET1 broadcast = %+v, want 5 percent enabled", bc)
	}
	// "5.00" and "5" are the same threshold. A parser that kept them apart
	// would report drift for ever against a device carrying exactly what was
	// asked.
	var mc, _ = ports[0].Limit(driver.StormMulticast)
	if mc.Enabled {
		t.Error("ET1 multicast has no statement and must read as no limiter")
	}
	var uu, _ = ports[0].Limit(driver.StormUnknownUnicast)
	if !uu.Enabled || uu.Rate != 1 {
		t.Errorf("ET1 unknown-unicast = %+v, want 1 percent enabled", uu)
	}

	// A port block with no storm-control line at all: three explicit absences,
	// not an empty list.
	if len(ports[1].Limits) != 3 {
		t.Fatalf("ET2 reports %d classes, want 3", len(ports[1].Limits))
	}
	var i int
	for i = range ports[1].Limits {
		if ports[1].Limits[i].Enabled {
			t.Errorf("ET2 %s reads as limited", ports[1].Limits[i].Class)
		}
	}
}

// An explicit "no storm-control …" line must not be read as a limiter. EOS does
// not normally print one, but a device configured from a template carries it, and
// inverting that flag would show a protection nobody installed.
func TestParseStormControlHonoursExplicitNo(t *testing.T) {
	var ports = parseStormControl("interface Ethernet1\n   no storm-control multicast\n!\n")
	var mc, ok = ports[0].Limit(driver.StormMulticast)
	if !ok || mc.Enabled {
		t.Fatalf("\"no storm-control multicast\" read as %+v", mc)
	}
}

// "level pps <n>" is READ as packets per second, and above all never as a
// percentage — 1000 % of line rate is not a threshold, and the digits are the
// same either way.
//
// Reading the form is not claiming to write it: [Driver.StormControlCaps] still
// offers percent alone, because that is the one form proven on this forwarding
// hardware. ADR-0020, and driver.StormControlCaps.Units on why the two surfaces
// are allowed to differ.
func TestParseStormControlReadsPacketsPerSecondAsItself(t *testing.T) {
	var ports = parseStormControl(
		"interface Ethernet1\n   storm-control broadcast level pps 1000\n!\n")
	var bc, ok = ports[0].Limit(driver.StormBroadcast)
	if !ok || !bc.Enabled {
		t.Fatalf("a pps limiter read as absent — the port would be shown unprotected, "+
			"got %+v (%v)", bc, ok)
	}
	if bc.Unit != driver.StormRatePPS {
		t.Fatalf("read in %q rather than packets per second — as a percentage this is "+
			"1000 %% of line rate", bc.Unit)
	}
	if bc.Rate != 1000 {
		t.Errorf("rate %v, want 1000", bc.Rate)
	}
}

// EOS's "level rate <n> <unit>" form carries a real threshold whose unit
// vocabulary the device's grammar dump does not spell out. It is reported
// VERBATIM, and the class it names is left NOT READ — because the one thing this
// contract will not do is publish a rate without its unit.
//
// What this test really guards is the step before: the statement must not be
// SKIPPED. A skipped line falls under "silence in a block that was read means
// no limiter", and the port comes back declared unprotected while it is policed.
func TestParseStormControlReportsTheRateFormVerbatim(t *testing.T) {
	const cfg = "interface Ethernet1\n" +
		"   storm-control broadcast level rate 10 mbps\n" +
		"   storm-control multicast level 5\n!\n"
	var ports = parseStormControl(cfg)
	var _, read = ports[0].Limit(driver.StormBroadcast)
	if read {
		t.Error("the \"rate\" form was reduced to a limiter — its unit was never read")
	}
	var f, ok = ports[0].ForeignFor(driver.StormBroadcast)
	if !ok {
		t.Fatalf("the statement was dropped: %+v — the port now claims to carry no "+
			"broadcast limiter while the device is policing it", ports[0])
	}
	if f.Spelling != "storm-control broadcast level rate 10 mbps" {
		t.Errorf("the device's own line was not kept verbatim: %q", f.Spelling)
	}
	// The classes that WERE understood are untouched: one unreadable statement
	// casts doubt on its own class, not on the block.
	var mc, mok = ports[0].Limit(driver.StormMulticast)
	if !mok || !mc.Enabled || mc.Unit != driver.StormRatePercent || mc.Rate != 5 {
		t.Errorf("an unreadable broadcast line disturbed multicast: %+v (%v)", mc, mok)
	}
}

// "storm-control all" is EOS's fourth class and it polices EVERY frame on the
// port. No class of this contract describes it, so it is reported with an empty
// Class — and that emptiness suspends the silence rule for the WHOLE port.
//
// Three lines reading "no limiter configured" beside a statement that is
// policing broadcast, multicast and unknown unicast would be three lies, and the
// screen would invite an operator to go on believing the port is open.
func TestParseStormControlAllClassBlindsTheWholePort(t *testing.T) {
	var ports = parseStormControl(
		"interface Ethernet1\n   storm-control all level 5\n!\n")
	if len(ports[0].Foreign) != 1 || ports[0].Foreign[0].Class != "" {
		t.Fatalf("\"all\" was not reported as a classless observation: %+v", ports[0].Foreign)
	}
	if len(ports[0].Limits) != 0 {
		t.Fatalf("the port still reports per-class limits beside \"all\": %+v",
			ports[0].Limits)
	}
	// And it must not be matched to a class by the helper either.
	var _, ok = ports[0].ForeignFor(driver.StormBroadcast)
	if ok {
		t.Error("\"all\" was attributed to broadcast")
	}
}

// "no storm-control all" is what "show running-config all" prints on EVERY
// interface of the chassis. Classing those sixty lines as unreadable would put
// every port of every EOS device into doubt on the strength of a default.
func TestParseStormControlIgnoresNegatedStatements(t *testing.T) {
	const cfg = "interface Ethernet1\n" +
		"   no storm-control broadcast\n" +
		"   no storm-control multicast\n" +
		"   no storm-control unknown-unicast\n" +
		"   no storm-control all\n" +
		"   logging event storm-control discards use-global\n!\n"
	var ports = parseStormControl(cfg)
	if len(ports[0].Foreign) != 0 {
		t.Fatalf("a default was reported as an unreadable limiter: %+v", ports[0].Foreign)
	}
	if len(ports[0].Limits) != 3 {
		t.Fatalf("the port reports %d classes, want 3 — all three were established",
			len(ports[0].Limits))
	}
}

// The GLOBAL "storm-control bum aggregate" statements belong to no port. They
// sit at column zero, and before the block-closing rule they would have been
// attributed to whichever interface was read last — reported as an unreadable
// limiter on a port that carries nothing.
func TestParseStormControlDoesNotAttributeGlobalStatementsToAPort(t *testing.T) {
	const cfg = "interface Ethernet1\n" +
		"   storm-control broadcast level 5\n" +
		"!\n" +
		"storm-control bum aggregate traffic-class 3 level pps 100\n"
	var ports = parseStormControl(cfg)
	if len(ports[0].Foreign) != 0 {
		t.Fatalf("a global statement landed on %s: %+v", ports[0].Port, ports[0].Foreign)
	}
}

// TestParseStormControlOnCapturedHardwareRateSpelling holds the parser to what a
// real 7050SX prints back, rather than to what the command reference implies.
//
// Captured 2026-08-08 on the lab core01.lab (DCS-7050SX-64-R, EOS
// 4.28.13.1M) through meshcraft's own SOCKS route: three limiters set on
// Ethernet1 — a port with no transceiver, so nothing could be policed — then
// read back from "show running-config interfaces Ethernet1" and removed.
//
// THE POINT IS THE RATE SPELLING, and the device is stricter than "prints an
// integer": it normalises to the shortest exact form. "5.00" comes back "5",
// and "0.5" comes back "0.5". A parser that accepted only one of those two
// shapes would read a configured limiter as absent, and the apply would then
// rewrite a line already in place on every single pass.
//
// The fraction half is the one with teeth: rounding a read to an integer would
// turn a 0.5 % limiter on a ten-gigabit port — 50 Mbit/s, a real setting — into
// 0 % or 1 %.
func TestParseStormControlOnCapturedHardwareRateSpelling(t *testing.T) {
	// Verbatim, including EOS's three-space block indent.
	const captured = `interface Ethernet1
   switchport trunk allowed vlan 1,42,98-100
   switchport mode trunk
   storm-control broadcast level 5
   storm-control multicast level 0.5
   storm-control unknown-unicast level 10
`
	var ports = parseStormControl(captured)
	if len(ports) != 1 {
		t.Fatalf("expected one port, got %d: %+v", len(ports), ports)
	}
	var want = map[driver.StormClass]float64{
		driver.StormBroadcast:      5,
		driver.StormMulticast:      0.5,
		driver.StormUnknownUnicast: 10,
	}
	var class driver.StormClass
	for class = range want {
		var got, read = ports[0].Limit(class)
		if !read {
			t.Errorf("%s was printed by the device and the parser did not read it", class)
			continue
		}
		if !got.Enabled {
			t.Errorf("%s reads as disabled on a line that sets a level", class)
		}
		if got.Rate != want[class] {
			t.Errorf("%s rate = %v, want %v", class, got.Rate, want[class])
		}
		if got.Unit != driver.StormRatePercent {
			t.Errorf("%s unit = %q, want percent: EOS's \"level\" is a percentage of "+
				"line rate and reading it as anything else is a limiter off by orders "+
				"of magnitude", class, got.Unit)
		}
	}

	// The idempotence property this capture exists to protect: what the device
	// prints for a rate we SENT as "5.00" must compare equal to 5, or every
	// apply pass rewrites a line that is already correct.
	var b, _ = ports[0].Limit(driver.StormBroadcast)
	if b.Rate != 5.00 {
		t.Errorf("\"level 5\" read back as %v — a rate sent as 5.00 must compare "+
			"equal to it, otherwise the apply never converges", b.Rate)
	}
}
