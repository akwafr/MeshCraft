package eos

import (
	"strings"
	"testing"
)

// CAPTURED ON HARDWARE — core01.lab, DCS-7050SX-64-R, EOS 4.28.13.1M,
// 2026-08-10, read-only over the controller's SOCKS route as `mcadmin`.
//
// TODO.md asked for one thing first on this range: "EOS's DHCP snooping depends
// on the forwarding ASIC as much as on the release, so the first thing to
// establish is whether the LAB MODEL carries the command at all". It does. The
// command answers, and this is the whole of what it printed with the feature
// off — two lines, no VLAN list, no per-port table.
//
// That matters in both directions. It means `readSnooping` on this model does
// NOT take the [driver.NotSupported] branch, so the absence wording in
// [reSnoopAbsent] is still uncaptured on Arista and stays a defensive guess. And
// it means the OFF shape is now real rather than invented.
//
// The ENABLED shape was captured the same day — see [snoopCapturedOn] below,
// and the two things it settled that this comment used to owe.
const snoopCapturedOff = "show ip dhcp snooping\n" +
	"DHCP Snooping is disabled\n" +
	"Insertion of Option-82 is disabled\n"

// CAPTURED ON HARDWARE — core01.lab, DCS-7050SX-64-R, EOS 4.28.13.1M,
// 2026-08-10, over the controller's SOCKS route as `mcadmin`.
//
// # How it was taken without risking the lab
//
// "show vlan" on this switch lists 1, 42, 98, 99 (management) and 100. The
// probe armed snooping on VLAN 777 — an id the switch does not carry, on a
// range where every faceplate port trunks 1,42,98-100 — so the feature inspected
// a VLAN no frame belongs to while still printing the per-VLAN section the
// parser had never seen. Reverted with "no ip dhcp snooping vlan 777" and
// "no ip dhcp snooping"; the output returned to [snoopCapturedOff] exactly.
//
// # TWO THINGS IT SETTLES, and one of them is a defect
//
// FIRST, the VLAN list is NOT on the header line. [reSnoopVLANs] was written
// expecting "configured on the following VLANs: 10,20"; this firmware prints
// the header, a newline, and the ids INDENTED ON THE NEXT LINE. Until
// 2026-08-11 one regexp reached them by accident; [snoopConfiguredVLANs] now
// reads the shape deliberately, and TestCapturedSnoopingOnReadsItsVLANList is
// what makes it a tested property.
//
// SECOND, THERE IS NO PER-PORT TRUST TABLE AT ALL. Not an empty one — none.
// [reSnoopPortRow] expects "Ethernet49  Yes" rows, and this output has no such
// section in either state. That is not a paging artefact: the CLI's own
// completion says so. "show ip dhcp snooping ?" offers COUNTERS and HARDWARE
// and nothing else, and inside an "interface" block
// "ip dhcp snooping ?" offers only "interface", whose sole leaf is
// "information option" — Option-82. "ip dhcp snooping trust" is answered
// "% Invalid input", and so is "show ip dhcp snooping trust".
//
// So [snoopTrustCmds] — "interface Et1 / ip dhcp snooping trust" — emits a
// command this platform does not have, and [verifySnoopTrust] compares against a
// table it never prints. The third of dhcpsnoop.go's "three levels" is not
// merely unproven on this range, it is ABSENT, and no spelling fixes that.
const snoopCapturedOn = "show ip dhcp snooping\n" +
	"DHCP Snooping is enabled\n" +
	"DHCP Snooping is not operational\n" +
	"DHCP Snooping is configured on following VLANs:\n" +
	"  777\n" +
	"DHCP Snooping is operational on following VLANs:\n" +
	"  None\n" +
	"Insertion of Option-82 is disabled\n"

// snoopCapturedHardware is "show ip dhcp snooping hardware" taken in the SAME
// session as [snoopCapturedOn] — with the feature configured and enabled.
//
// It is the whole explanation of that capture's second line. The forwarding
// hardware never programmed the feature, which is precisely the ASIC dependency
// dhcpsnoop.go's header describes, arriving in a form the header did not
// anticipate: not "the command is absent" but "the command is accepted, the
// configuration is stored, and nothing inspects anything".
const snoopCapturedHardware = "show ip dhcp snooping hardware\n" +
	"DHCP Snooping is disabled\n"

// TestCapturedSnoopingOffReadsAsDisabled pins the real off-state output against
// the parser that has only ever seen invented fixtures.
func TestCapturedSnoopingOffReadsAsDisabled(t *testing.T) {
	if !reSnoopEnabled.MatchString(snoopCapturedOff) {
		t.Fatal("the captured output does not match reSnoopEnabled — the one line " +
			"the reader depends on to know the feature exists at all")
	}
	var m = reSnoopEnabled.FindStringSubmatch(snoopCapturedOff)
	if len(m) < 2 || m[1] != "disabled" {
		t.Fatalf("captured state read as %v, want \"disabled\"", m)
	}
}

// TestTheCapturedOutputIsNotMistakenForAnAbsentFeature is the assertion that
// earns this capture its place.
//
// `readSnooping` turns [reSnoopAbsent] into [driver.NotSupported] — "this
// platform does not do DHCP snooping" — and that verdict is reported to an
// operator as a property of their hardware. A model that DOES carry the command
// must never reach it. Before this capture nothing proved the real output stays
// clear of those five wordings.
func TestTheCapturedOutputIsNotMistakenForAnAbsentFeature(t *testing.T) {
	if reSnoopAbsent.MatchString(snoopCapturedOff) {
		t.Fatal("the captured output of a model that HAS the command matches " +
			"reSnoopAbsent — readSnooping would report the feature unsupported " +
			"on hardware that supports it")
	}
}

// TestOptionEightyTwoDisabledIsNotReadAsEnabled guards a regex that looks for
// "enabled" on a line that, here, says "disabled".
//
// `reSnoopOpt82` is `Option-?82[^\n]*\b(enabled)\b`. The captured line is
// "Insertion of Option-82 is disabled", and the only thing keeping that from
// matching is that "disabled" does not contain "enabled" as a word. That is
// true but it is a coincidence of spelling rather than a design, so it is worth a
// test: a laxer regex here would report Option-82 insertion active on every
// Arista in the fleet.
func TestOptionEightyTwoDisabledIsNotReadAsEnabled(t *testing.T) {
	if reSnoopOpt82.MatchString(snoopCapturedOff) {
		t.Fatal("\"Insertion of Option-82 is disabled\" was read as ENABLED")
	}
}

// TestTheCapturedOffStateCarriesNoVLANsOrPorts records what the off shape does
// NOT contain, so the enabled-shape capture has something to be compared against.
func TestTheCapturedOffStateCarriesNoVLANsOrPorts(t *testing.T) {
	if reSnoopVLANs.MatchString(snoopCapturedOff) {
		t.Fatal("the off state matched the VLAN-list regex; the capture shows no such line")
	}
	if reSnoopPortRow.MatchString(snoopCapturedOff) {
		t.Fatal("the off state matched a per-port trust row; the capture shows no table")
	}
}

// TestCapturedSnoopingOnReadsItsVLANList is the assertion the enabled capture was
// taken for.
//
// [reSnoopVLANs] was written for "configured on the following VLANs: 10,20" —
// ids on the HEADER LINE. The device puts them on the NEXT line, indented, and
// the original pattern reached them only because `\s` spans newlines and its
// character class stopped at the "D" of the line after. [snoopConfiguredVLANs]
// replaced that accident on 2026-08-11 with a scanner whose continuation rule is
// explicit (indented AND starting with a digit), so this test now guards a
// decision instead of a coincidence. What it must never become is a read that
// reports NO VLAN — snoopStateOf turns that into "every port trust flag below is
// inert".
func TestCapturedSnoopingOnReadsItsVLANList(t *testing.T) {
	var r = parseShowDHCPSnooping(snoopCapturedOn)
	if !r.Enabled {
		t.Fatal("the captured ENABLED output does not read as enabled")
	}
	if len(r.VLANs) != 1 || r.VLANs[0] != 777 {
		t.Fatalf("captured VLAN list read as %v, want [777] — the ids sit on the line "+
			"BELOW the header, and this is the read that proves the regexp reaches them",
			r.VLANs)
	}
}

// TestTheOperationalVLANLineIsNotMistakenForTheConfiguredOne guards the OTHER
// list in the same output.
//
// The capture carries two "… on following VLANs:" sections — "configured"
// and "operational" — and on this device the second one says "None". A
// pattern that matched either would, on a switch where snooping is configured but
// not yet programmed, read the coverage as empty. [reSnoopVLANs] requires the
// word "configured", and that requirement is load-bearing rather than
// decorative.
func TestTheOperationalVLANLineIsNotMistakenForTheConfiguredOne(t *testing.T) {
	var m = reSnoopVLANs.FindStringSubmatch(snoopCapturedOn)
	if m == nil {
		t.Fatal("the enabled capture matched no VLAN list header at all")
	}
	if strings.Contains(strings.ToLower(m[0]), "operational") {
		t.Fatalf("the VLAN-list regexp latched onto the \"operational\" section: %q", m[0])
	}
	// And the scanner must stop before that section rather than absorb it: on
	// this device the operational list says "None", so a scanner that ran on
	// would either fail the expansion or, on a device where it said a NUMBER,
	// quietly merge two different lists into one answer.
	var spec = snoopConfiguredVLANs(snoopCapturedOn)
	if spec != "777" {
		t.Fatalf("snoopConfiguredVLANs read %q, want \"777\" — the section ends at the "+
			"next header, and the header is not indented", spec)
	}
}

// TestTheVLANListSurvivesAWrappedContinuationLine is the half of
// [snoopConfiguredVLANs] core01 could not exercise, and it is the failure mode
// that has already been paid for once on this connector.
//
// `parseMSTRegion` lost 83 of 100 VLANs by keeping only the first line of a
// wrapped list — silently, producing a partial map and a false region mismatch.
// The snooping VLAN list wraps the same way on a switch inspecting more than a
// handful, and the old single-regexp read would have joined the continuation by
// accident (its `\s` class spanned newlines) while ALSO being one tidy-up away
// from dropping it. The scanner now does it on purpose, and this pins both the
// join and the two ways a wrap can fall: after a comma, and inside a range.
func TestTheVLANListSurvivesAWrappedContinuationLine(t *testing.T) {
	var wrapped = "show ip dhcp snooping\n" +
		"DHCP Snooping is enabled\n" +
		"DHCP Snooping is configured on following VLANs:\n" +
		"  1,42,98,\n" +
		"  99,100-102\n" +
		"DHCP Snooping is operational on following VLANs:\n" +
		"  None\n"
	var r = parseShowDHCPSnooping(wrapped)
	var want = []int{1, 42, 98, 99, 100, 101, 102}
	if len(r.VLANs) != len(want) {
		t.Fatalf("wrapped VLAN list read as %v, want %v — a continuation line was dropped, "+
			"which is the parseMSTRegion defect in a second place", r.VLANs, want)
	}
	var i int
	for i = range want {
		if r.VLANs[i] != want[i] {
			t.Fatalf("wrapped VLAN list read as %v, want %v", r.VLANs, want)
		}
	}

	// A range split ACROSS the break. EOS wraps between items rather than inside
	// one, so this is defensive — but the cost of getting it wrong is the whole
	// list, because expandVlanList refuses "3000-" and parseShowDHCPSnooping
	// then keeps nothing at all.
	var split = "DHCP Snooping is enabled\n" +
		"DHCP Snooping is configured on following VLANs:\n" +
		"  3000-\n" +
		"  3002\n"
	var s = parseShowDHCPSnooping(split)
	if len(s.VLANs) != 3 || s.VLANs[0] != 3000 || s.VLANs[2] != 3002 {
		t.Fatalf("a range split across the wrap read as %v, want [3000 3001 3002]", s.VLANs)
	}

	// And a continuation that is NOT indented is a different section, never a
	// continuation — the rule that keeps "DHCP Snooping is operational …" out.
	var flush = "DHCP Snooping is configured on following VLANs:\n" +
		"  777\n" +
		"12345 is not a VLAN list\n"
	var got = snoopConfiguredVLANs(flush)
	if got != "777" {
		t.Fatalf("an unindented following line was absorbed: %q", got)
	}
}

// TestTheEnabledCaptureHasNoPerPortTrustTable pins the absence that matters most,
// and it is deliberately an assertion about the DEVICE rather than about a regexp.
//
// [reSnoopPortRow] exists to read a "Interface / Trusted" grid, and
// [verifySnoopTrust] decides [driver.ApplyResult.PortsWritten] by looking ports up
// in what that grid produced. On this platform the grid does not exist in either
// state, and the interface command that would populate it — "ip dhcp snooping
// trust" — is answered "% Invalid input" (cf. the doc comment on
// [snoopCapturedOn]).
//
// The consequence, spelled out because it is not obvious from the code: an apply
// naming any port would send a refused command, land the port in Skipped, and —
// had the command somehow been accepted — still find no row and report the write
// UNCONFIRMED forever. That is the behaviour the connector has today, and this
// test is what keeps a future reader from "fixing" the empty table by loosening
// the regexp when the real answer is that the feature's third level is absent from
// this hardware.
func TestTheEnabledCaptureHasNoPerPortTrustTable(t *testing.T) {
	if reSnoopPortRow.MatchString(snoopCapturedOn) {
		t.Fatal("a per-port trust row matched the ENABLED capture — the capture shows no " +
			"such table, so either the fixture or the regexp has drifted")
	}
	var r = parseShowDHCPSnooping(snoopCapturedOn)
	if len(r.Trust) != 0 {
		t.Fatalf("parseShowDHCPSnooping found %d trust entr(y|ies) in an output that "+
			"carries no trust table: %+v", len(r.Trust), r.Trust)
	}
	// And the state handed to a caller must not claim otherwise.
	var st = snoopStateOf(r)
	if len(st.Ports) != 0 {
		t.Fatalf("snoopStateOf reported %d port(s) from a capture with no per-port "+
			"section: %+v", len(st.Ports), st.Ports)
	}
}

// TestTheCapturedEnabledStateSaysNothingInspectsIt is the INVERSION its own
// predecessor asked for.
//
// The device said three things at once: "enabled", "configured on … 777", and
// "NOT OPERATIONAL", with "show ip dhcp snooping hardware" answering
// "disabled" in the same session ([snoopCapturedHardware]). The forwarding
// hardware never programmed the feature.
//
// Until 2026-08-11 [snoopStateOf] read the first two and not the third: it
// reported Enabled=true with VLAN 777 covered and — because a VLAN IS named — did
// not even emit its "ON but covers NO VLAN" note, so an operator was told this
// switch inspects VLAN 777 when nothing on it did. A test pinned that gap and
// said the assertion should be inverted rather than deleted when it closed. This
// is that inversion.
//
// Enabled STAYS true and the VLAN list is still reported: both are what the
// device carries, and the note is what says they inspect nothing.
func TestTheCapturedEnabledStateSaysNothingInspectsIt(t *testing.T) {
	var st = snoopStateOf(parseShowDHCPSnooping(snoopCapturedOn))
	if !st.Enabled {
		t.Fatal("the enabled capture no longer reads as enabled: the configuration IS on " +
			"the device, and reporting it off would blame an operator who touched nothing")
	}
	if len(st.VLANs) != 1 || st.VLANs[0] != 777 {
		t.Fatalf("VLANs = %v, want [777]", st.VLANs)
	}
	var said bool
	var n string
	for _, n = range st.Notes {
		if strings.Contains(strings.ToLower(n), "not operational") {
			said = true
		}
	}
	if !said {
		t.Fatalf("no note mentions the operational line, so this state reads as a switch "+
			"inspecting VLAN 777 while the forwarding hardware programmed nothing: %+v",
			st.Notes)
	}
}

// TestAnOperationalSwitchIsNotAnnotated is the other side of the same read: the
// note must be a REPORT of what this device said, not a permanent caveat printed
// on every Arista. A switch that does not print the line gets no note.
func TestAnOperationalSwitchIsNotAnnotated(t *testing.T) {
	var healthy = "DHCP Snooping is enabled\n" +
		"DHCP Snooping is configured on following VLANs:\n" +
		"  777\n" +
		"DHCP Snooping is operational on following VLANs:\n" +
		"  777\n"
	var st = snoopStateOf(parseShowDHCPSnooping(healthy))
	var n string
	for _, n = range st.Notes {
		if strings.Contains(strings.ToLower(n), "not operational") {
			t.Fatalf("a switch whose snooping IS operational was annotated as not: %q", n)
		}
	}
	if len(st.VLANs) != 1 || st.VLANs[0] != 777 {
		t.Fatalf("VLANs = %v, want [777]", st.VLANs)
	}
}

// TestTheHardwareCaptureIsNotMistakenForTheFeatureBeingOff keeps the second
// capture from being read as a state it is not.
//
// "show ip dhcp snooping hardware" prints "DHCP Snooping is disabled" — a
// sentence IDENTICAL to the first line of [snoopCapturedOff] — while the feature
// was configured and enabled. Nothing in this package runs that command today,
// and this test is the guard on doing so casually: fed to [reSnoopEnabled] it
// reads "disabled", so a connector that ever reached for it as a second opinion
// on the global state would report snooping OFF on a switch carrying it.
func TestTheHardwareCaptureIsNotMistakenForTheFeatureBeingOff(t *testing.T) {
	var m = reSnoopEnabled.FindStringSubmatch(snoopCapturedHardware)
	if m == nil {
		t.Fatal("the hardware capture matched nothing at all")
	}
	if m[1] != "disabled" {
		t.Fatalf("hardware capture read as %q, want \"disabled\"", m[1])
	}
	if reSnoopAbsent.MatchString(snoopCapturedHardware) {
		t.Fatal("the hardware capture reads as an ABSENT feature")
	}
}
