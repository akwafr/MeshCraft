package s3900

// DHCP snooping — the parser, the command builders and the applier held to
// driver/dhcpsnoop.go. No hardware: the apply LOGIC is proven on states and on
// a scripted CLI, the house style of this package (cf. apply_test.go, and
// routeros/fixtures_test.go: "apply LOGIC on these real states, not the
// device's reaction").
//
// # FIXTURES: what is CAPTURED and what is CONSTRUCTED
//
// CAPTURED — three things, and none of them is a running-config with snooping on:
//
//   - labRun (parse_test.go), the "show run" of the lab S3900-24T4S on OpCode
//     1.7.0 (2026-08-01/02 serial session). It proves a NEGATIVE: no
//     DHCP-snooping line at all, because the lab switch runs with the feature
//     off, and an interface block for only THREE of the 28 ethernet interfaces;
//   - testdata/show_dhcp_snooping_config.txt — the status command with the
//     feature DISABLED (FSOS 1.7.7, 2026-08-08), written by the live-capture
//     harness (live_capture_test.go);
//   - testdata/show_dhcp_snooping_config_enabled.txt — the same command with the
//     feature ENABLED (FSOS 1.7.7, 2026-08-10), transcribed from the session that
//     typed "dhcp snooping" on the device. TRUNCATED at the rate-limit line:
//     the Circuit-ID table that follows it was not captured, which is why the two
//     files cannot simply be diffed end to end.
//
// CONSTRUCTED — every "show run" fixture below that carries a snooping line,
// and every answer the fake device gives. They are built from the ONE captured
// write form ("dhcp snooping") and from the two dhcpsnoop.go still deduces
// ("dhcp snooping vlan N", "dhcp snooping trust"), in both the no-"ip" and
// the "ip" spelling — because what a running-config PRINTS once snooping is on
// has never been captured either way. They therefore prove what this connector
// DOES with such output, never that a device would print it: the day the forms
// are typed on an S3900, the captures replace these literals and any difference
// is a bug found.
//
// The refusal wording the fake answers ("% Invalid input detected at '^'
// marker.") IS captured, from the 1.7.0 lab (cf. ssh.go, reCLIError).

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"meshcraft/internal/driver"
)

// --- CONSTRUCTED fixtures (see the file header: none of these is captured) ---

// snoopRunAllThreeLevels: CONSTRUCTED, not captured. A running-config with
// snooping on globally, on a VLAN LIST and an "add" form (shapes a
// hand-configured device may carry even though this connector writes one VLAN
// per command), option 82 riding along, and two interface blocks of which one
// is trusted.
const snoopRunAllThreeLevels = `Building running configuration. Please wait...
!
hostname test-fs-s3900
!
vlan database
 VLAN 1 name DefaultVlan media ethernet
 VLAN 99 name LAN99_Management media ethernet
!
ip dhcp snooping
ip dhcp snooping vlan 10,20-22
ip dhcp snooping vlan add 99
ip dhcp snooping information option
!
interface ethernet 1/5
 switchport mode access
!
interface ethernet 1/28
 ip dhcp snooping trust
!
end
`

// snoopRunNegatedForms: CONSTRUCTED, not captured. The negated spellings a
// device carries after someone turned the feature back off. Reading any of
// them as "enabled" or "trusted" would invent protection that is not there.
const snoopRunNegatedForms = `Building running configuration. Please wait...
!
no ip dhcp snooping
no ip dhcp snooping vlan 99
!
interface ethernet 1/28
 no ip dhcp snooping trust
!
end
`

// snoopRunEnabledNoVLAN: CONSTRUCTED, not captured. Global on, no VLAN — the
// state where the feature inspects nothing and the reader must say so.
const snoopRunEnabledNoVLAN = `Building running configuration. Please wait...
!
ip dhcp snooping
!
interface ethernet 1/1
!
end
`

// snoopRunTrustAtTopLevel: CONSTRUCTED, not captured. A trust line outside any
// interface block (a truncated read, a paste). No port may be invented from it,
// and it must not read as a global enable either.
const snoopRunTrustAtTopLevel = `Building running configuration. Please wait...
!
ip dhcp snooping trust
!
end
`

// snoopRunCapturedSpelling: CONSTRUCTED, not captured — but written in the ONE
// spelling this range is known to accept ("dhcp snooping", no "ip" prefix,
// s3900lab FSOS 1.7.7 2026-08-10), which is what a device configured BY THIS
// CONNECTOR would plausibly print back. Whether the running-config echoes the
// short form or the family's long one is unobserved, so the reader tolerates
// both; this fixture holds the half that used to be missed.
const snoopRunCapturedSpelling = `Building running configuration. Please wait...
!
dhcp snooping
dhcp snooping vlan 99
dhcp snooping information option
!
interface ethernet 1/5
!
interface ethernet 1/28
 dhcp snooping trust
!
end
`

// --- the parser: the three levels, and "not read" ≠ "untrusted" ---

// TestParseDHCPSnoopOnTheCapture replays the ONE captured "show run" this
// package owns (labRun, OpCode 1.7.0). Two things are pinned:
//
//   - the lab switch has snooping OFF, so the reading is empty and the reader
//     must SAY that "off" and "this firmware has no such feature" look
//     identical from a running-config;
//   - the capture prints an interface block for GE1, GE25 and GE28 and for NO
//     OTHER port. GE2..GE24 are therefore NOT READ, and must be ABSENT from
//     Ports rather than reported as untrusted (driver/dhcpsnoop.go: "A port
//     absent from this list was not observed, which is not the same as
//     untrusted").
func TestParseDHCPSnoopOnTheCapture(t *testing.T) {
	var st = parseDHCPSnoop(labRun)
	if st.Enabled {
		t.Error("the lab capture carries no snooping line: Enabled must stay false")
	}
	if len(st.VLANs) != 0 {
		t.Errorf("VLANs=%v, want none", st.VLANs)
	}
	var want = []string{"GE1", "GE25", "GE28"}
	if len(st.Ports) != len(want) {
		t.Fatalf("ports=%+v — only the interface blocks the capture PRINTS may appear", st.Ports)
	}
	var i int
	for i = range want {
		if st.Ports[i].Port != want[i] {
			t.Errorf("Ports[%d]=%q, want %q (order of appearance)", i, st.Ports[i].Port, want[i])
		}
		if st.Ports[i].Trusted {
			t.Errorf("%s: no trust line in the capture, so Trusted must be false", want[i])
		}
	}
	// The heart of it: a port the read never saw is not a port read untrusted.
	var _, known = trustOf(st, "GE7")
	if known {
		t.Error("GE7 has no block in the capture: it must NOT be reported at all")
	}
	if !hasNote(st.Notes, "global DHCP snooping is OFF") {
		t.Errorf("an empty reading must be explained, not left to read as \"none\": %v", st.Notes)
	}
}

// TestParseDHCPSnoopUnreadPortsAreNeverDefaultedToUntrusted: a reading with no
// interface block at all reports NO port. A connector that filled Ports with
// every faceplate port defaulted to Trusted:false would put a definite,
// reassuring "untrusted" on 28 ports it never observed.
func TestParseDHCPSnoopUnreadPortsAreNeverDefaultedToUntrusted(t *testing.T) {
	var st = parseDHCPSnoop(snoopRunEnabledNoVLAN)
	if len(st.Ports) != 1 || st.Ports[0].Port != "GE1" {
		t.Fatalf("ports=%+v, want GE1 only (the only block printed)", st.Ports)
	}
	st = parseDHCPSnoop("Building running configuration. Please wait...\n!\nhostname x\n!\nend\n")
	if len(st.Ports) != 0 {
		t.Errorf("no interface block was read: Ports must be empty, got %+v", st.Ports)
	}
	// Truncated output: nothing is deduced, nothing is invented.
	st = parseDHCPSnoop("")
	if st.Enabled || len(st.Ports) != 0 || len(st.VLANs) != 0 {
		t.Errorf("an empty read must yield the zero state, got %+v", st)
	}
}

func TestParseDHCPSnoopReadsTheThreeLevels(t *testing.T) {
	var st = parseDHCPSnoop(snoopRunAllThreeLevels)
	if !st.Enabled {
		t.Error("\"ip dhcp snooping\" at top level is the global enable")
	}
	// Sorted and deduplicated, list forms expanded: 10, 20-22, and 99 via "add".
	var want = []int{10, 20, 21, 22, 99}
	if !sameInts(st.VLANs, want) {
		t.Errorf("VLANs=%v, want %v (sorted, deduplicated, ranges expanded)", st.VLANs, want)
	}
	var trusted, known = trustOf(st, "GE28")
	if !known || !trusted {
		t.Errorf("GE28 carries \"ip dhcp snooping trust\": trusted=%v known=%v", trusted, known)
	}
	trusted, known = trustOf(st, "GE5")
	if !known || trusted {
		t.Errorf("GE5 has a block without a trust line: read as untrusted, got %v/%v", trusted, known)
	}
	if !hasNote(st.Notes, "option 82") {
		t.Errorf("option 82 insertion is observed and not modelled: it belongs in Notes: %v", st.Notes)
	}
	if hasNote(st.Notes, "covers NO VLAN") {
		t.Errorf("this device covers five VLANs: %v", st.Notes)
	}
}

func TestParseDHCPSnoopIgnoresNegatedForms(t *testing.T) {
	var st = parseDHCPSnoop(snoopRunNegatedForms)
	if st.Enabled {
		t.Error("\"no ip dhcp snooping\" must NEVER read as enabled")
	}
	if len(st.VLANs) != 0 {
		t.Errorf("\"no ip dhcp snooping vlan 99\" must not enable VLAN 99: %v", st.VLANs)
	}
	var trusted, known = trustOf(st, "GE28")
	if !known {
		t.Fatal("the block was printed: GE28 was read")
	}
	if trusted {
		t.Error("\"no ip dhcp snooping trust\" must read as UNTRUSTED")
	}
}

func TestParseDHCPSnoopEnabledWithoutVLANIsExplained(t *testing.T) {
	var st = parseDHCPSnoop(snoopRunEnabledNoVLAN)
	if !st.Enabled || len(st.VLANs) != 0 {
		t.Fatalf("state=%+v", st)
	}
	// driver.DHCPSnoopState.VLANs: empty must never be left for a screen to read
	// as "none" without knowing which "none" it is.
	if !hasNote(st.Notes, "covers NO VLAN") {
		t.Errorf("an empty VLAN list under an enabled feature must be explained: %v", st.Notes)
	}
}

// TestParseDHCPSnoopReadsTheCapturedSpellingToo: the "ip" prefix is OPTIONAL
// on the read since 2026-08-10. The device accepts "dhcp snooping" and refuses
// "ip dhcp snooping", so a reader that only matched the long form would report
// "snooping off" on a switch this connector had just turned it on — and the
// applier's own post-write re-read would abort a write that had landed.
func TestParseDHCPSnoopReadsTheCapturedSpellingToo(t *testing.T) {
	var st = parseDHCPSnoop(snoopRunCapturedSpelling)
	if !st.Enabled {
		t.Error("\"dhcp snooping\" at top level IS the global enable on this range")
	}
	if !sameInts(st.VLANs, []int{99}) {
		t.Errorf("VLANs=%v, want [99]", st.VLANs)
	}
	var trusted, known = trustOf(st, "GE28")
	if !known || !trusted {
		t.Errorf("\"dhcp snooping trust\" must read as trusted: trusted=%v known=%v", trusted, known)
	}
	if trusted, known = trustOf(st, "GE5"); !known || trusted {
		t.Errorf("GE5 has a block without a trust line: %v/%v", trusted, known)
	}
	if !hasNote(st.Notes, "option 82") {
		t.Errorf("the option-82 line is spelled without the prefix too: %v", st.Notes)
	}
	// And the negation still never reads as protection, in either spelling.
	st = parseDHCPSnoop("Building running configuration. Please wait...\n!\nno dhcp snooping\n" +
		"!\ninterface ethernet 1/28\n no dhcp snooping trust\n!\nend\n")
	if st.Enabled {
		t.Error("\"no dhcp snooping\" must NEVER read as enabled")
	}
	if trusted, known = trustOf(st, "GE28"); !known || trusted {
		t.Errorf("\"no dhcp snooping trust\" must read as UNTRUSTED: %v/%v", trusted, known)
	}
}

func TestParseDHCPSnoopTrustOutsideAnInterfaceInventsNothing(t *testing.T) {
	var st = parseDHCPSnoop(snoopRunTrustAtTopLevel)
	if len(st.Ports) != 0 {
		t.Errorf("a trust line with no interface block above it names no port: %+v", st.Ports)
	}
	if st.Enabled {
		t.Error("\"ip dhcp snooping trust\" is not the global enable")
	}
}

// --- "show dhcp snooping config": the two CAPTURED shapes ---

// snoopConfigCapture loads one of the two captured status outputs.
func snoopConfigCapture(t *testing.T, name string) string {
	t.Helper()
	var raw, err = os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("reading the capture: %v", err)
	}
	return string(raw)
}

// snoopCaptureLines: one capture, CRLF normalised, without the trailing empty
// element the final newline produces.
func snoopCaptureLines(t *testing.T, name string) []string {
	t.Helper()
	return strings.Split(strings.TrimRight(
		strings.ReplaceAll(snoopConfigCapture(t, name), "\r", ""), "\n"), "\n")
}

// TestParseSnoopConfigStatusOnBothCaptures pins the ONE line this connector
// reads out of that command, on the two shapes hardware has actually printed.
//
// The third case is the one that matters most: a firmware that does not carry
// the command answers "% Invalid input", and that MUST come back as "not
// known", never as "disabled". A refusal read as a negative is how a connector
// concludes a feature is off on a device that told it nothing — and here it would
// abort an apply whose global enable had in fact landed.
func TestParseSnoopConfigStatusOnBothCaptures(t *testing.T) {
	var on, known = parseSnoopConfigStatus(
		snoopConfigCapture(t, "show_dhcp_snooping_config_enabled.txt"))
	if !known || !on {
		t.Errorf("the ENABLED capture reads (on=%v, known=%v), want (true, true)", on, known)
	}
	if on, known = parseSnoopConfigStatus(
		snoopConfigCapture(t, "show_dhcp_snooping_config.txt")); !known || on {
		t.Errorf("the DISABLED capture reads (on=%v, known=%v), want (false, true)", on, known)
	}
	if on, known = parseSnoopConfigStatus(
		"show dhcp snooping config\n% Invalid input detected at '^' marker.\n"); known || on {
		t.Errorf("a refused command reads (on=%v, known=%v), want (false, FALSE): a caret line "+
			"is not a device saying \"disabled\"", on, known)
	}
	if on, known = parseSnoopConfigStatus(""); known || on {
		t.Errorf("an empty read reads (on=%v, known=%v), want (false, false)", on, known)
	}
}

// TestSnoopConfigEnabledDiffersByTheStatusLineAlone: the two captures are the
// same output twice, and the feature's state lives in ONE line of it. Anything
// else this package ever learns to read out of that command is a NEW shape, not
// a variation of this one.
//
// The enabled capture stops at the rate-limit line — the Circuit-ID table that
// follows it was not transcribed — so only the header block is comparable, which
// is exactly the block being compared.
func TestSnoopConfigEnabledDiffersByTheStatusLineAlone(t *testing.T) {
	// Both captures carry the device's own CRLF line endings, as an SSH capture
	// does; nothing here is about that.
	var enabled = snoopCaptureLines(t, "show_dhcp_snooping_config_enabled.txt")
	var disabled = snoopCaptureLines(t, "show_dhcp_snooping_config.txt")
	if len(disabled) <= len(enabled) {
		t.Fatalf("the disabled capture carries the Circuit-ID table and must be the longer "+
			"of the two: %d vs %d lines", len(disabled), len(enabled))
	}
	var diffs []int
	var i int
	for i = range enabled {
		if enabled[i] != disabled[i] {
			diffs = append(diffs, i)
		}
	}
	if len(diffs) != 1 || diffs[0] != 1 {
		t.Fatalf("the two shapes differ on lines %v, want line 1 alone (\"Global DHCP "+
			"Snooping Status\")", diffs)
	}
	if !strings.HasPrefix(enabled[1], "Global DHCP Snooping Status:") {
		t.Errorf("line 1 = %q", enabled[1])
	}
}

// TestSnoopConfigEndsInTheCircuitIDTable pins where that command's output
// actually STOPS, which is not where the enabled fixture stops.
//
// The disabled capture serves the table IN FULL — 28 rows, one per faceplate
// port, all identical but for the interface name — and it is the only capture of
// it anywhere. Nothing parses it; it is pinned because a reader who took the
// enabled fixture for the whole output would write a parser that runs off the
// end of a real device's answer.
func TestSnoopConfigEndsInTheCircuitIDTable(t *testing.T) {
	var lines = snoopCaptureLines(t, "show_dhcp_snooping_config.txt")
	var head = -1
	var i int
	for i = range lines {
		if strings.HasPrefix(lines[i], "Interface Trusted Max-Number") {
			head = i
			break
		}
	}
	if head < 0 {
		t.Fatal("the disabled capture no longer carries the Circuit-ID table header: " +
			"it is the only capture of that table this repository has")
	}
	var col string
	for _, col = range []string{"Circuit-ID", "mode", "Value", "TR101 VLAN Field"} {
		if !strings.Contains(lines[head-1]+lines[head], col) {
			t.Errorf("the table header no longer names %q", col)
		}
	}
	var rows int
	for i = head + 2; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "Eth 1/") {
			rows++
			if !strings.Contains(lines[i], "No      16          VLAN-Unit-Port   ---") {
				t.Errorf("row %q is not the captured shape", lines[i])
			}
		}
	}
	if rows != 28 {
		t.Errorf("the table carries %d rows, want 28 — one per faceplate port", rows)
	}
}

// TestSnoopConfigVLANSectionIsPresentAndEmpty is the trap this capture exists to
// pin. "DHCP Snooping is configured on the following VLANs:" is followed by a
// BLANK LINE on a device with no VLAN mapped — in BOTH captures, including the
// one taken with the feature ON. A parser keying on "the section is missing"
// would read a present-and-empty list as an unreadable output, and one that
// learned the POPULATED shape from these files would be inventing it: no capture
// has ever carried a VLAN there, so nothing here parses that section at all.
func TestSnoopConfigVLANSectionIsPresentAndEmpty(t *testing.T) {
	var name string
	for _, name = range []string{
		"show_dhcp_snooping_config.txt", "show_dhcp_snooping_config_enabled.txt",
	} {
		var lines = snoopCaptureLines(t, name)
		var at = -1
		var i int
		for i = range lines {
			if strings.HasPrefix(lines[i], "DHCP Snooping is configured on the following VLANs:") {
				at = i
				break
			}
		}
		if at < 0 {
			t.Fatalf("%s: the VLAN section header is PRESENT in both captures", name)
		}
		if strings.TrimSpace(lines[at]) != "DHCP Snooping is configured on the following VLANs:" {
			t.Errorf("%s: the header line carries no VLAN of its own: %q", name, lines[at])
		}
		if at+1 >= len(lines) || strings.TrimSpace(lines[at+1]) != "" {
			t.Errorf("%s: the line after the header is BLANK on a device with no VLAN mapped, "+
				"got %q", name, lines[at+1])
		}
	}
}

// TestSnoopConfigOutputIsNotARunningConfig: feeding that command's output to the
// running-config parser yields NOTHING — not a global enable, not a port. The
// enabled capture says "Global DHCP Snooping Status: enabled" and parseDHCPSnoop
// reads it as OFF; the disabled one prints a "Trusted" row per interface and
// parseDHCPSnoop reads no port at all. Both failures would be silent, which is
// why they are pinned rather than left to be discovered on a device.
func TestSnoopConfigOutputIsNotARunningConfig(t *testing.T) {
	var name string
	for _, name = range []string{
		"show_dhcp_snooping_config.txt", "show_dhcp_snooping_config_enabled.txt",
	} {
		var st = parseDHCPSnoop(snoopConfigCapture(t, name))
		if st.Enabled {
			t.Errorf("%s: parseDHCPSnoop reads a running-config and must not claim to have "+
				"read this one", name)
		}
		if len(st.Ports) != 0 || len(st.VLANs) != 0 {
			t.Errorf("%s: state=%+v — the \"Eth 1/1\" rows of that table are not interface "+
				"blocks", name, st)
		}
	}
}

// --- command builders: nothing here may ever turn the feature off ---

// TestDHCPSnoopCandidatesNeverDisableTheFeature is the contract's flat
// prohibition (driver/dhcpsnoop.go: "It never turns the feature OFF"). The ONLY
// negation this connector is allowed to emit is the per-port one, because
// untrusting a port IS a per-port write.
func TestDHCPSnoopCandidatesNeverDisableTheFeature(t *testing.T) {
	var alts [][]string
	var alt []string
	var cmd string

	var global = dhcpSnoopGlobalCmds()
	if len(global) != 1 || global[0] != "dhcp snooping" {
		t.Fatalf("global command set=%v — ONE captured form, and it drops the \"ip\" prefix", global)
	}
	for _, cmd = range global {
		if strings.HasPrefix(strings.ToLower(cmd), "no ") {
			t.Errorf("the global enable emitted a disable form: %q", cmd)
		}
	}
	// CAPTURED since 2026-08-10: ONE spelling. The three that used to follow it
	// are gone, and two of them could never have parsed — "dhcp snooping vlan ?"
	// on the device offers "<1-4094>  VLAN ID" with no "add" keyword.
	alts = dhcpSnoopVlanCandidates(99)
	if len(alts) != 1 || len(alts[0]) != 1 || alts[0][0] != "dhcp snooping vlan 99" {
		t.Fatalf("vlan candidates=%v — a captured form ships ONE spelling", alts)
	}
	for _, alt = range alts {
		for _, cmd = range alt {
			if strings.HasPrefix(strings.ToLower(cmd), "no ") ||
				strings.Contains(strings.ToLower(cmd), "remove") {
				t.Errorf("a VLAN was disabled or removed: %q", cmd)
			}
		}
	}
	// Per-port trust: one form per direction, and the untrust form touches
	// nothing but the port.
	alts = dhcpSnoopTrustCandidates("ethernet 1/28", true)
	if len(alts) != 1 {
		t.Fatalf("trust candidates=%v — CAPTURED since 2026-08-10, so ONE spelling; the "+
			"\"ip\"-prefixed alternative was typed on the lab switch and refused", alts)
	}
	for _, alt = range alts {
		for _, cmd = range alt {
			if strings.HasPrefix(strings.ToLower(cmd), "no ") {
				t.Errorf("trusting a port emitted a negation: %q", cmd)
			}
		}
	}
	alts = dhcpSnoopTrustCandidates("ethernet 1/28", false)
	for _, alt = range alts {
		if len(alt) != 3 || alt[0] != "interface ethernet 1/28" || alt[2] != "exit" {
			t.Fatalf("the untrust form must enter and LEAVE the interface: %v", alt)
		}
		if !strings.Contains(alt[1], "snooping trust") ||
			!strings.HasPrefix(alt[1], "no ") {
			t.Errorf("untrusting must negate the TRUST statement only: %q", alt[1])
		}
	}
}

// TestDHCPSnoopTrustCandidatesEnterTheCLIPortName pins the exact wire form, which
// is now the CAPTURED one and the only one.
//
// "ip dhcp snooping trust" is deliberately absent: it was typed inside
// "interface ethernet 1/1" on s3900lab (FSOS 1.7.7, 2026-08-10) and answered
// "% Invalid input detected at '^' marker.". A spelling observed being refused
// is not kept as a fallback — it is a guaranteed caret line in every transcript.
func TestDHCPSnoopTrustCandidatesEnterTheCLIPortName(t *testing.T) {
	var alts = dhcpSnoopTrustCandidates(cliPortName("GE28"), true)
	if len(alts) != 1 {
		t.Fatalf("trust candidates=%v — a captured form ships ONE spelling", alts)
	}
	var joined = strings.Join(alts[0], " ; ")
	if joined != "interface ethernet 1/28 ; dhcp snooping trust ; exit" {
		t.Errorf("form = %q — the captured spelling drops the \"ip\" prefix", joined)
	}
	var alt []string
	for _, alt = range alts {
		var cmd string
		for _, cmd = range alt {
			if strings.HasPrefix(strings.ToLower(cmd), "ip ") {
				t.Errorf("\"%s\" is the form the device answers \"%% Invalid input\" to", cmd)
			}
		}
	}
}

// TestDHCPSnoopFormEvidenceMatchesTheBuilders binds the dated snoop*Captured
// constants to the shape of the builders, so the two cannot drift: a CAPTURED
// form ships ONE spelling (there is nothing to fall back to and no reason to
// guess), a DEDUCED one ships several and lets the transcript say which the
// device took. Promoting a form means flipping a constant, and this test fails
// until the builder follows.
func TestDHCPSnoopFormEvidenceMatchesTheBuilders(t *testing.T) {
	if !snoopGlobalCaptured {
		t.Error("\"dhcp snooping\" was accepted on s3900lab (FSOS 1.7.7, 2026-08-10)")
	}
	if len(dhcpSnoopGlobalCmds()) != 1 {
		t.Errorf("a captured form ships one spelling: %v", dhcpSnoopGlobalCmds())
	}
	// And the spelling the device REFUSED is not kept as a fallback.
	var cmd string
	for _, cmd = range dhcpSnoopGlobalCmds() {
		if strings.HasPrefix(strings.ToLower(cmd), "ip ") {
			t.Errorf("\"%s\" is the form the device answers \"%% Invalid input\" to", cmd)
		}
	}
	if snoopVlanCaptured != (len(dhcpSnoopVlanCandidates(99)) == 1) {
		t.Errorf("snoopVlanCaptured=%v but the builder ships %d spelling(s)",
			snoopVlanCaptured, len(dhcpSnoopVlanCandidates(99)))
	}
	if snoopTrustCaptured != (len(dhcpSnoopTrustCandidates("ethernet 1/28", true)) == 1) {
		t.Errorf("snoopTrustCaptured=%v but the builder ships %d spelling(s)",
			snoopTrustCaptured, len(dhcpSnoopTrustCandidates("ethernet 1/28", true)))
	}
}

// --- helpers the applier's determinism rests on ---

func TestSortedUniqueIntsIsSortedAndDeduplicated(t *testing.T) {
	var got = sortedUniqueInts([]int{99, 10, 99, 1, 10})
	if !sameInts(got, []int{1, 10, 99}) {
		t.Errorf("got %v", got)
	}
	if len(sortedUniqueInts(nil)) != 0 {
		t.Error("nil in, nothing out")
	}
}

func TestSortedIntentsIsDeterministicAndLastWins(t *testing.T) {
	var in = []driver.DHCPSnoopPortIntent{
		{Port: "GE9", Trusted: false},
		{Port: "GE10", Trusted: true},
		{Port: "GE9", Trusted: true}, // repeated: the LAST answer is the answer
		{Port: " GE2 ", Trusted: false},
	}
	var got = sortedIntents(in)
	var names []string
	var it driver.DHCPSnoopPortIntent
	for _, it = range got {
		names = append(names, it.Port)
	}
	// Lexicographic, like ApplyPorts' own port ordering (apply.go, sort.Strings):
	// what matters for the contract is that it is TOTAL and STABLE, so two
	// identical gestures produce two identical transcripts.
	if strings.Join(names, ",") != "GE10,GE2,GE9" {
		t.Fatalf("order=%v", names)
	}
	if !got[2].Trusted {
		t.Error("a port named twice keeps its LAST intent")
	}
}

func TestTrustOfSeparatesUnreadFromUntrusted(t *testing.T) {
	var st = driver.DHCPSnoopState{Ports: []driver.DHCPSnoopPortState{
		{Port: "GE1", Trusted: false},
		{Port: "GE28", Trusted: true},
	}}
	var v, known = trustOf(st, "GE1")
	if v || !known {
		t.Errorf("GE1: read untrusted => (false, true), got (%v, %v)", v, known)
	}
	if v, known = trustOf(st, "GE28"); !v || !known {
		t.Errorf("GE28: (%v, %v)", v, known)
	}
	// The distinction the whole contract turns on: not read is not untrusted.
	if v, known = trustOf(st, "GE7"); v || known {
		t.Errorf("GE7 was never read => (false, FALSE), got (%v, %v)", v, known)
	}
	if !anyTrustedPort(st) {
		t.Error("GE28 is trusted")
	}
	if anyTrustedPort(driver.DHCPSnoopState{}) {
		t.Error("an empty reading trusts nothing")
	}
}

// --- the applier, on a scripted CLI ---

// fakeSnoopDevice is a CONSTRUCTED device: it answers the forms dhcpsnoop.go
// deduces, and renders a "show run" from what it has been told. It is not a
// capture and does not pretend to be one — its value is that it lets the
// applier's gesture be observed EXACTLY: which commands, in which order, and
// what the device answers when it refuses, or when it accepts a command and
// stores nothing.
//
// It plugs into the real cliConn: stdin is this writer, and every reply is
// pushed into the same byte channel the SSH reader goroutine would feed. So the
// prompt handling, the CLI-error detection (reCLIError) and the save dialogue
// under test are the production ones.
type fakeSnoopDevice struct {
	out chan byte
	// sent: every line the connector wrote, in order. The transcript under test.
	sent []string
	// refuse: substrings. A command containing one is answered with the 1.7.0
	// lab's own refusal wording (captured — cf. ssh.go, reCLIError).
	refuse []string
	// acceptsAndForgets: the device answers a clean prompt to every
	// configuration command and stores NOTHING. Not a hypothetical: dhcpsnoop.go
	// itself says "a VLAN line sent to a device with snooping off is the kind of
	// command that is accepted and does nothing", and the whole snooping command
	// set is unproven on this platform.
	acceptsAndForgets bool
	// saveFails: the session dies during the flash write. Modelled as a closed
	// stream because that is the ONLY save failure the production code detects
	// quickly — a device that answers the save prompt and then says nothing
	// costs saveTimeout (60 s) before the connector gives up, and a unit test
	// must not buy that.
	saveFails bool
	// runHidesGlobal: the firmware stores the global enable and does NOT print it
	// in "show run". Nothing rules that out — no capture shows a running-config
	// with snooping on — and it is the case the applier's second opinion on
	// "show dhcp snooping config" exists for.
	runHidesGlobal bool

	global  bool
	snoop   map[int]bool
	trust   map[int]bool
	ports   []int // the ethernet interfaces "show run" prints a block for
	defined []int // the VLANs the vlan database declares
	curPort int
}

const fakePrompt = "\ntest-fs-s3900#"

var (
	reFakeEnterIface = regexp.MustCompile(`^interface\s+ethernet\s+1/(\d+)$`)
	reFakeSnoopVlan  = regexp.MustCompile(`^(?:ip\s+)?dhcp\s+snooping\s+vlan\s+(?:add\s+)?(\d+)$`)
)

func newFakeSnoopClient(dev *fakeSnoopDevice) *Client {
	if dev.out == nil {
		dev.out = make(chan byte, 1<<20)
	}
	if dev.snoop == nil {
		dev.snoop = map[int]bool{}
	}
	if dev.trust == nil {
		dev.trust = map[int]bool{}
	}
	if dev.ports == nil {
		dev.ports = fakeAllPorts()
	}
	if dev.defined == nil {
		dev.defined = []int{1, 99}
	}
	return &Client{
		host:    "192.0.2.90",
		cli:     &cliConn{stdin: dev, out: dev.out},
		dialect: dialectFSOS17,
		facts:   versionFacts{Serial: "S3900ABC12345", OpCode: "1.7.0.20", Ports: 28},
	}
}

func fakeAllPorts() []int {
	var out []int
	var i int
	for i = 1; i <= 28; i++ {
		out = append(out, i)
	}
	return out
}

func (d *fakeSnoopDevice) Close() error { return nil }

func (d *fakeSnoopDevice) Write(p []byte) (int, error) {
	var line = strings.TrimRight(string(p), "\r\n")
	d.sent = append(d.sent, line)
	d.react(strings.ToLower(strings.TrimSpace(line)))
	return len(p), nil
}

func (d *fakeSnoopDevice) emit(s string) {
	var i int
	for i = 0; i < len(s); i++ {
		d.out <- s[i]
	}
}

func (d *fakeSnoopDevice) react(cmd string) {
	var pat string
	for _, pat = range d.refuse {
		if strings.Contains(cmd, strings.ToLower(pat)) {
			d.emit("\n% Invalid input detected at '^' marker." + fakePrompt)
			return
		}
	}
	switch cmd {
	case "show run":
		d.emit("\n" + d.showRun() + fakePrompt)
		return
	case strings.ToLower(snoopConfigCmd):
		d.emit("\n" + d.showSnoopConfig() + fakePrompt)
		return
	case "copy running-config startup-config":
		if d.saveFails {
			close(d.out) // the session drops mid-write: nothing more is readable
			return
		}
		d.emit("\nStartup configuration file name: ")
		return
	case strings.ToLower(StartupConfigFile):
		d.emit("\nWriting to FLASH...\nFlash programming completed.\nSuccess." + fakePrompt)
		return
	}
	if !d.acceptsAndForgets {
		d.model(cmd)
	}
	d.emit(fakePrompt)
}

// model applies a configuration command — the DEDUCED forms only.
func (d *fakeSnoopDevice) model(cmd string) {
	var m = reFakeEnterIface.FindStringSubmatch(cmd)
	if m != nil {
		d.curPort, _ = strconv.Atoi(m[1])
		return
	}
	if cmd == "exit" || cmd == "end" || cmd == "conf t" {
		d.curPort = 0
		return
	}
	if cmd == "ip dhcp snooping" || cmd == "dhcp snooping" {
		d.global = true
		return
	}
	if m = reFakeSnoopVlan.FindStringSubmatch(cmd); m != nil {
		var v, _ = strconv.Atoi(m[1])
		d.snoop[v] = true
		return
	}
	if cmd == "ip dhcp snooping trust" || cmd == "dhcp snooping trust" {
		if d.curPort > 0 {
			d.trust[d.curPort] = true
		}
		return
	}
	if cmd == "no ip dhcp snooping trust" || cmd == "no dhcp snooping trust" {
		if d.curPort > 0 {
			delete(d.trust, d.curPort)
		}
	}
}

// showRun renders the running-config in the shape the 1.7.0 capture has (blocks
// terminated by "!"), carrying the snooping statements in the ONE spelling
// this range is known to accept. Which spelling a running-config PRINTS is
// unobserved — reSnoopGlobal and friends read both, and
// TestParseDHCPSnoopReadsTheThreeLevels replays the "ip" one.
func (d *fakeSnoopDevice) showRun() string {
	var b strings.Builder
	b.WriteString("Building running configuration. Please wait...\n!\n")
	b.WriteString("hostname test-fs-s3900\n!\nvlan database\n")
	var v int
	for _, v = range d.defined {
		b.WriteString(fmt.Sprintf(" VLAN %d name VLAN%04d media ethernet\n", v, v))
	}
	b.WriteString("!\n")
	if d.global && !d.runHidesGlobal {
		b.WriteString("dhcp snooping\n")
	}
	var enabled []int
	for v = range d.snoop {
		enabled = append(enabled, v)
	}
	sort.Ints(enabled)
	for _, v = range enabled {
		b.WriteString(fmt.Sprintf("dhcp snooping vlan %d\n", v))
	}
	b.WriteString("!\n")
	var p int
	for _, p = range d.ports {
		b.WriteString(fmt.Sprintf("interface ethernet 1/%d\n", p))
		if d.trust[p] {
			b.WriteString(" dhcp snooping trust\n")
		}
		b.WriteString("!\n")
	}
	b.WriteString("interface vlan 99\n ip add 192.0.2.240 255.255.0.0\n!\nend\n")
	return b.String()
}

// showSnoopConfig renders "show dhcp snooping config" in the CAPTURED shape,
// which is the disabled capture with its first line switched — that is exactly
// the difference the two testdata files carry
// (TestSnoopConfigEnabledDiffersByTheStatusLineAlone).
//
// The VLAN section stays PRESENT AND EMPTY whatever d.snoop holds: no capture
// has ever shown a VLAN printed there, so a fake that invented that shape would
// be teaching this package a format nobody has seen. Nothing reads it either —
// parseSnoopConfigStatus takes the status line and stops.
func (d *fakeSnoopDevice) showSnoopConfig() string {
	var status = "disabled"
	if d.global {
		status = "enabled"
	}
	return snoopConfigCmd + "\n" +
		"Global DHCP Snooping Status: " + status + "\n" +
		"Option82 Status: disabled\n" +
		"Option82 Sub-option Format: extra subtype included\n" +
		"Option82 Remote ID: MAC Address (hex encoded)\n" +
		"Option82 Remote ID TR101 VLAN Field: enabled\n" +
		"Option82 TR101 Board ID: none\n" +
		"DHCP Snooping Information Policy: replace\n" +
		"DHCP Snooping is configured on the following VLANs:\n\n" +
		"Verify Source MAC-Address: enabled\n" +
		"DHCP Snooping Rate Limit: unlimited\n"
}

func TestReadDHCPSnoopingGoesThroughShowRun(t *testing.T) {
	var dev = &fakeSnoopDevice{global: true, snoop: map[int]bool{99: true},
		trust: map[int]bool{28: true}, ports: []int{1, 28}}
	var c = newFakeSnoopClient(dev)
	var st, err = c.ReadDHCPSnooping(t.Context())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if strings.Join(dev.sent, "|") != "show run" {
		t.Errorf("the reading is \"show run\" and nothing else: %v", dev.sent)
	}
	if !st.Enabled || !sameInts(st.VLANs, []int{99}) || len(st.Ports) != 2 {
		t.Fatalf("state=%+v", st)
	}
	var trusted, known = trustOf(st, "GE28")
	if !trusted || !known {
		t.Error("GE28 is trusted on this device")
	}
	if len(c.Steps) == 0 || !c.Steps[len(c.Steps)-1].OK {
		t.Errorf("the read must leave a transcript step: %+v", c.Steps)
	}
}

func TestReadDHCPSnoopingRefusesWithoutASession(t *testing.T) {
	var c = &Client{}
	var _, err = c.ReadDHCPSnooping(t.Context())
	if err == nil {
		t.Fatal("a read without an open session must fail, not return an empty state")
	}
}

// TestApplyDHCPSnoopingWholeGesture pins the ENTIRE conversation of one apply
// on a device that arrives with the feature off. Asserting the exact sequence is
// the point: it proves the global enable comes first and in its CAPTURED
// spelling, that the trust flag is written and VERIFIED BEFORE any VLAN is armed
// (cf. dhcpsnoop.go, § the applier now arms last), that the VLANs are sorted and
// deduplicated (99,10,10,99 in the plan -> 10 then 99 on the wire), that nothing
// is confirmed by the CLI's return code, and that two identical gestures would
// produce two identical requests.
func TestApplyDHCPSnoopingWholeGesture(t *testing.T) {
	var dev = &fakeSnoopDevice{}
	var c = newFakeSnoopClient(dev)
	var res, err = c.ApplyDHCPSnooping(t.Context(), driver.DHCPSnoopPlan{
		VLANs: []int{99, 10, 10, 99},
		Ports: []driver.DHCPSnoopPortIntent{{Port: "GE28", Trusted: true}},
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	var want = []string{
		"show run", // pre-write read, and the safety copy
		"conf t", "dhcp snooping", "end",
		"show run", // the global enable is CONFIRMED before any trust flag is written
		"conf t", "interface ethernet 1/28", "dhcp snooping trust", "exit", "end",
		"show run", // the EXEMPTION is confirmed before the drop is armed
		"conf t", "dhcp snooping vlan 10", "end",
		"conf t", "dhcp snooping vlan 99", "end",
		"show run", // verifying re-read: nothing is "written" on a return code
		"copy running-config startup-config", StartupConfigFile, "",
	}
	assertSent(t, dev.sent, want)

	if strings.Contains(strings.Join(dev.sent, "|"), "ip dhcp snooping") {
		t.Errorf("\"ip dhcp snooping\" is REFUSED on this range and must never be sent as the "+
			"global enable: %v", dev.sent)
	}
	if strings.Join(res.PortsWritten, ",") != "GE28" {
		t.Errorf("PortsWritten=%v", res.PortsWritten)
	}
	if len(res.Skipped) != 0 {
		t.Errorf("nothing was refused: Skipped=%v", res.Skipped)
	}
	if !strings.Contains(res.Backup, "Building running configuration") {
		t.Error("Backup must carry the pre-write configuration exactly as read")
	}
	// Since 2026-08-10 all three FORMS are captured, so the note no longer says
	// "two of them are deduced". It must still say what remains unproven — the
	// applier that sends them — because that is now the whole of the caveat and an
	// apply that carried no caveat at all would read as a finished capability.
	if !hasNote(res.Notes, "all three DHCP snooping write forms are CAPTURED") ||
		!hasNote(res.Notes, "What is NOT captured is this connector's APPLIER") {
		t.Errorf("every apply must say which forms are proven and what is still not: %v", res.Notes)
	}
	if !hasNote(res.Notes, "affects EVERY port") {
		t.Errorf("turning the global feature on changes every port: it must be reported: %v", res.Notes)
	}
	if !hasNote(res.Notes, "enabled on VLAN(s) 10,99") {
		t.Errorf("the per-VLAN enable is part of the gesture and must be reported: %v", res.Notes)
	}
	// VLAN 10 does not exist on this device (defined: 1, 99) — enabling snooping
	// on it is exact and useless, and the operator hears about it.
	if !hasNote(res.Notes, "VLAN 10 is not defined") {
		t.Errorf("notes=%v", res.Notes)
	}
	if hasNote(res.Notes, "NO port on this device is trusted") {
		t.Errorf("GE28 was trusted: the black-hole warning must not fire: %v", res.Notes)
	}
	// The device really did end up in the asked-for state.
	if !dev.global || !dev.snoop[10] || !dev.snoop[99] || !dev.trust[28] {
		t.Errorf("device=%+v", dev)
	}
}

// TestApplyDHCPSnoopingIsIdempotentOnTheWire: the same gesture, twice, on two
// devices in the same state, sends exactly the same lines. Map iteration order
// leaking into the command order would break here.
func TestApplyDHCPSnoopingIsIdempotentOnTheWire(t *testing.T) {
	var plan = driver.DHCPSnoopPlan{
		VLANs: []int{300, 99, 10, 300},
		Ports: []driver.DHCPSnoopPortIntent{
			{Port: "GE9", Trusted: true}, {Port: "GE10", Trusted: true},
			{Port: "GE2", Trusted: true},
		},
	}
	var first, second []string
	var run int
	for run = 0; run < 2; run++ {
		var dev = &fakeSnoopDevice{global: true}
		var c = newFakeSnoopClient(dev)
		var _, err = c.ApplyDHCPSnooping(t.Context(), plan)
		if err != nil {
			t.Fatalf("run %d: %v", run, err)
		}
		if run == 0 {
			first = dev.sent
		} else {
			second = dev.sent
		}
	}
	if strings.Join(first, "|") != strings.Join(second, "|") {
		t.Fatalf("two identical gestures sent different requests:\n%v\n%v", first, second)
	}
	// And the order is the sorted one, ports first, VLANs after.
	var joined = strings.Join(first, "|")
	if !strings.Contains(joined, "vlan 10|end|conf t|dhcp snooping vlan 99|end|conf t|dhcp snooping vlan 300") {
		t.Errorf("VLANs must go out sorted and deduplicated: %v", first)
	}
	if strings.Index(joined, "interface ethernet") > strings.Index(joined, "snooping vlan") {
		t.Errorf("the trust flags go out BEFORE the VLANs are armed: %v", first)
	}
	if strings.Index(joined, "1/10") > strings.Index(joined, "1/2") ||
		strings.Index(joined, "1/2") > strings.Index(joined, "1/9") {
		t.Errorf("port order must be the total, stable one: %v", first)
	}
}

// TestApplyDHCPSnoopingNeverRemovesAVLANItFound: the VLAN set is a UNION with
// what the device carries, never a replacement (driver/dhcpsnoop.go: "a VLAN
// someone else enabled snooping on is protection we did not install and must
// not take away").
func TestApplyDHCPSnoopingNeverRemovesAVLANItFound(t *testing.T) {
	var dev = &fakeSnoopDevice{global: true, snoop: map[int]bool{20: true, 99: true},
		trust: map[int]bool{28: true}}
	var c = newFakeSnoopClient(dev)
	var res, err = c.ApplyDHCPSnooping(t.Context(), driver.DHCPSnoopPlan{
		VLANs: []int{10, 99},
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	var joined = strings.Join(dev.sent, "|")
	if strings.Contains(joined, "no ip dhcp snooping") || strings.Contains(joined, "no dhcp snooping") {
		t.Fatalf("a disable form was sent: %v", dev.sent)
	}
	if strings.Contains(joined, "vlan remove") || strings.Contains(joined, "vlan 20") {
		t.Fatalf("VLAN 20 was not in the plan and must be left ALONE: %v", dev.sent)
	}
	if !dev.snoop[20] || !dev.global {
		t.Error("the device lost protection this connector did not install")
	}
	if !hasNote(res.Notes, "NOT removed") {
		t.Errorf("the VLAN left in place must be reported: %v", res.Notes)
	}
	if !hasNote(res.Notes, "already had snooping enabled") {
		t.Errorf("VLAN 99 was already enabled and untouched: %v", res.Notes)
	}
	if strings.Count(joined, "dhcp snooping vlan") != 1 {
		t.Errorf("only the missing VLAN 10 needed a command: %v", dev.sent)
	}
}

// TestApplyDHCPSnoopingConfirmsByReRead: the device accepts every command and
// stores nothing — the exact failure mode an unverified command set invites, and
// the one a return code cannot see. Nothing may be reported as written.
//
// GE28 arrives ALREADY trusted, so the arming gate is open and the VLAN write
// really is sent: what this test is about is the accepted-and-forgotten write,
// not the gate (TestApplyDHCPSnoopingArmsNothingWithoutATrustedPort has that).
func TestApplyDHCPSnoopingConfirmsByReRead(t *testing.T) {
	var dev = &fakeSnoopDevice{global: true, trust: map[int]bool{28: true},
		acceptsAndForgets: true}
	var c = newFakeSnoopClient(dev)
	var res, err = c.ApplyDHCPSnooping(t.Context(), driver.DHCPSnoopPlan{
		VLANs: []int{99},
		Ports: []driver.DHCPSnoopPortIntent{{Port: "GE9", Trusted: true}},
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(res.PortsWritten) != 0 {
		t.Errorf("the running-config does not carry the trust flag: PortsWritten=%v", res.PortsWritten)
	}
	if !anySkipped(res.Skipped, "GE9") {
		t.Errorf("an accepted-but-absent write belongs in Skipped: %v", res.Skipped)
	}
	if !anySkipped(res.Skipped, "VLAN 99") {
		t.Errorf("a VLAN the config does not carry after the write belongs in Skipped: %v", res.Skipped)
	}
	if hasNote(res.Notes, "enabled on VLAN(s) 99") {
		t.Errorf("nothing landed: no note may claim VLAN 99 is covered: %v", res.Notes)
	}
	// And the re-read really happened after the writes: one for the trust flags,
	// one for the arming.
	var last = strings.Join(dev.sent, "|")
	if strings.Count(last, "show run") != 3 {
		t.Errorf("each write step must be verified by its own re-read: %v", dev.sent)
	}
}

// TestApplyDHCPSnoopingArmsNothingWithoutATrustedPort is the gate, and the
// reason correcting the global enable did not simply make this applier live on
// two deduced spellings. The plan asks for a trusted uplink, the platform
// refuses the port, and snooping must NOT be switched on over VLAN 99 — a switch
// inspecting a VLAN with nothing trusted drops every DHCP OFFER/ACK it forwards,
// the uplink toward the real server included.
func TestApplyDHCPSnoopingArmsNothingWithoutATrustedPort(t *testing.T) {
	var dev = &fakeSnoopDevice{global: true, refuse: []string{"interface ethernet 1/28"}}
	var c = newFakeSnoopClient(dev)
	var res, err = c.ApplyDHCPSnooping(t.Context(), driver.DHCPSnoopPlan{
		VLANs: []int{99},
		Ports: []driver.DHCPSnoopPortIntent{{Port: "GE28", Trusted: true}},
	})
	if err != nil {
		t.Fatalf("a refused port is not an error: %v", err)
	}
	if dev.snoop[99] {
		t.Fatal("VLAN 99 was ARMED on a switch with no trusted port")
	}
	if strings.Contains(strings.Join(dev.sent, "|"), "snooping vlan") {
		t.Errorf("the VLAN command must not be SENT, not merely regretted afterwards: %v", dev.sent)
	}
	if !anySkipped(res.Skipped, "NO port on this device is trusted") {
		t.Errorf("the refusal must name what it protected the site from: %v", res.Skipped)
	}
	if !anySkipped(res.Skipped, "nothing was sent for this VLAN") {
		t.Errorf("Skipped=%v", res.Skipped)
	}
	// The global feature IS on — it inspects nothing, which is the harmless half.
	if !dev.global {
		t.Error("the captured global enable still landed")
	}
}

// TestApplyDHCPSnoopingArmsOverATrustedPortItDidNotWrite: the exemption does not
// have to come from THIS plan. A device that already carries a trusted uplink is
// a device that can be armed, and the gate reads the state, not the intent.
func TestApplyDHCPSnoopingArmsOverATrustedPortItDidNotWrite(t *testing.T) {
	var dev = &fakeSnoopDevice{global: true, trust: map[int]bool{28: true}}
	var c = newFakeSnoopClient(dev)
	var _, err = c.ApplyDHCPSnooping(t.Context(), driver.DHCPSnoopPlan{VLANs: []int{99}})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !dev.snoop[99] {
		t.Error("GE28 is trusted on this device: VLAN 99 could be armed")
	}
	// No trust flag needed writing, so no re-read was needed before arming either.
	if strings.Contains(strings.Join(dev.sent, "|"), "interface ethernet") {
		t.Errorf("nothing had to be written per port: %v", dev.sent)
	}
}

// TestApplyDHCPSnoopingConfirmsTheGlobalEnableOnTheStatusCommand: the firmware
// stores the enable and does not print it in "show run". That is not ruled out
// by anything captured — no running-config with snooping ON exists — so the
// applier asks the ONE command whose enabled shape IS captured instead of
// aborting a write that landed.
func TestApplyDHCPSnoopingConfirmsTheGlobalEnableOnTheStatusCommand(t *testing.T) {
	var dev = &fakeSnoopDevice{runHidesGlobal: true, trust: map[int]bool{28: true}}
	var c = newFakeSnoopClient(dev)
	var res, err = c.ApplyDHCPSnooping(t.Context(), driver.DHCPSnoopPlan{VLANs: []int{99}})
	if err != nil {
		t.Fatalf("the enable landed and the device says so: %v", err)
	}
	if !hasNote(res.Notes, "does NOT print the global snooping statement") {
		t.Errorf("an operator must be told an ordinary read will keep saying \"off\": %v", res.Notes)
	}
	if !strings.Contains(strings.Join(dev.sent, "|"), snoopConfigCmd) {
		t.Errorf("the captured status command is what settles it: %v", dev.sent)
	}
	if !dev.snoop[99] {
		t.Error("the gesture must go on: the enable was confirmed, GE28 is trusted")
	}
}

// TestApplyDHCPSnoopingGlobalEnableIsConfirmedBeforeAnyTrustFlag: a device that
// swallows the global enable must not receive trust flags. A trusted port under
// a feature that is asleep is an exact configuration with zero protection.
func TestApplyDHCPSnoopingGlobalEnableIsConfirmedBeforeAnyTrustFlag(t *testing.T) {
	var dev = &fakeSnoopDevice{acceptsAndForgets: true}
	var c = newFakeSnoopClient(dev)
	var _, err = c.ApplyDHCPSnooping(t.Context(), driver.DHCPSnoopPlan{
		VLANs: []int{99},
		Ports: []driver.DHCPSnoopPortIntent{{Port: "GE28", Trusted: true}},
	})
	if err == nil {
		t.Fatal("the global enable did not land: the apply must not report success")
	}
	var joined = strings.Join(dev.sent, "|")
	if strings.Contains(joined, "trust") {
		t.Errorf("a trust flag was written under a feature that is off: %v", dev.sent)
	}
	if strings.Contains(joined, "vlan 99") {
		t.Errorf("a VLAN was enabled under a feature that is off: %v", dev.sent)
	}
}

// TestApplyDHCPSnoopingRefusedGlobalFormStopsTheGesture: the captured spelling
// refused. Same conclusion, reached from the CLI's own words this time — and it
// is tried ONCE, because there is no second spelling left to try: the family's
// "ip dhcp snooping" is not an untested alternative, it is a form this range
// has been observed rejecting.
func TestApplyDHCPSnoopingRefusedGlobalFormStopsTheGesture(t *testing.T) {
	var dev = &fakeSnoopDevice{refuse: []string{"dhcp snooping"}}
	var c = newFakeSnoopClient(dev)
	var _, err = c.ApplyDHCPSnooping(t.Context(), driver.DHCPSnoopPlan{
		VLANs: []int{99},
		Ports: []driver.DHCPSnoopPortIntent{{Port: "GE28", Trusted: true}},
	})
	if err == nil {
		t.Fatal("a globally refused feature must not yield a successful apply")
	}
	if !strings.Contains(err.Error(), "no trust flag was written") {
		t.Errorf("the refusal must say what was NOT done: %v", err)
	}
	if !strings.Contains(err.Error(), "surveyed, not guessed") {
		t.Errorf("a refusal of the CAPTURED form is a survey to redo, not a spelling to try "+
			"against a live switch: %v", err)
	}
	var joined = strings.Join(dev.sent, "|")
	if strings.Count(joined, "|dhcp snooping|") != 1 {
		t.Errorf("the captured form is sent once: %v", dev.sent)
	}
	if strings.Contains(joined, "ip dhcp snooping") {
		t.Errorf("a spelling observed being REFUSED is not a fallback: %v", dev.sent)
	}
}

// TestApplyDHCPSnoopingRefusedPortLandsInSkipped: an ORDINARY REFUSAL — never a
// Go error, never silently dropped (driver/dhcpsnoop.go).
func TestApplyDHCPSnoopingRefusedPortLandsInSkipped(t *testing.T) {
	var dev = &fakeSnoopDevice{global: true, snoop: map[int]bool{99: true},
		trust: map[int]bool{28: true}, refuse: []string{"interface ethernet 1/7"}}
	var c = newFakeSnoopClient(dev)
	var res, err = c.ApplyDHCPSnooping(t.Context(), driver.DHCPSnoopPlan{
		VLANs: []int{99},
		Ports: []driver.DHCPSnoopPortIntent{
			{Port: "GE7", Trusted: true}, {Port: "GE9", Trusted: true},
		},
	})
	if err != nil {
		t.Fatalf("a refused port is not an error: %v", err)
	}
	if !anySkipped(res.Skipped, "GE7") {
		t.Fatalf("Skipped=%v", res.Skipped)
	}
	if !anySkipped(res.Skipped, "Invalid input") {
		t.Errorf("the CLI's own words must be carried: %v", res.Skipped)
	}
	if strings.Join(res.PortsWritten, ",") != "GE9" {
		t.Errorf("GE9 landed and GE7 did not: %v", res.PortsWritten)
	}
}

// TestApplyDHCPSnoopingSkipsNamesItCannotTranslate: nothing is ever sent as a
// literal "interface".
func TestApplyDHCPSnoopingSkipsNamesItCannotTranslate(t *testing.T) {
	var dev = &fakeSnoopDevice{global: true, snoop: map[int]bool{99: true}, trust: map[int]bool{28: true}}
	var c = newFakeSnoopClient(dev)
	var res, err = c.ApplyDHCPSnooping(t.Context(), driver.DHCPSnoopPlan{
		VLANs: []int{99},
		Ports: []driver.DHCPSnoopPortIntent{{Port: "stack2-port5", Trusted: true}},
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !anySkipped(res.Skipped, "stack2-port5") {
		t.Errorf("Skipped=%v", res.Skipped)
	}
	if strings.Contains(strings.Join(dev.sent, "|"), "interface") {
		t.Errorf("nothing may be sent for an untranslatable name: %v", dev.sent)
	}
}

// TestApplyDHCPSnoopingEmptyPortNameIsReportedNotDropped: an intent with a
// blank name is a caller mistake, and a caller mistake that vanishes without a
// trace is reported as a complete apply of a plan that was not executed.
func TestApplyDHCPSnoopingEmptyPortNameIsReportedNotDropped(t *testing.T) {
	var dev = &fakeSnoopDevice{global: true, snoop: map[int]bool{99: true}, trust: map[int]bool{28: true}}
	var c = newFakeSnoopClient(dev)
	var res, err = c.ApplyDHCPSnooping(t.Context(), driver.DHCPSnoopPlan{
		VLANs: []int{99},
		Ports: []driver.DHCPSnoopPortIntent{{Port: "   ", Trusted: true}},
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(res.Skipped) == 0 {
		t.Fatal("an intent that was not executed must leave a trace in Skipped")
	}
	if len(res.PortsWritten) != 0 {
		t.Errorf("PortsWritten=%v", res.PortsWritten)
	}
}

// TestApplyDHCPSnoopingWarnsWhenNoPortEndsUpTrusted covers the catastrophic
// case, and covers it on the state the device is ACTUALLY in afterwards: VLAN 99
// was ALREADY armed when meshcraft arrived, the plan asked for a trusted uplink,
// and the platform refused it. The gate cannot help here — the drop was switched
// on before this connector ever saw the device — so the operator gets the
// warning, on the device's own armed VLANs rather than on the plan's list.
func TestApplyDHCPSnoopingWarnsWhenNoPortEndsUpTrusted(t *testing.T) {
	var dev = &fakeSnoopDevice{global: true, snoop: map[int]bool{99: true},
		refuse: []string{"interface ethernet 1/28"}}
	var c = newFakeSnoopClient(dev)
	var res, err = c.ApplyDHCPSnooping(t.Context(), driver.DHCPSnoopPlan{
		VLANs: []int{99},
		Ports: []driver.DHCPSnoopPortIntent{{Port: "GE28", Trusted: true}},
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !anySkipped(res.Skipped, "GE28") {
		t.Fatalf("Skipped=%v", res.Skipped)
	}
	if !hasNote(res.Notes, "NO port on this device is trusted") {
		t.Errorf("the plan ASKED for a trusted port and did not get one — the warning must "+
			"follow the device's state, not the intent: %v", res.Notes)
	}
}

// TestApplyDHCPSnoopingWarnsWhenItUntrustsTheLastTrustedPort: same warning,
// reached the other way round — the write itself is what empties the trusted
// set. Comparing against the state BEFORE the write cannot see this.
func TestApplyDHCPSnoopingWarnsWhenItUntrustsTheLastTrustedPort(t *testing.T) {
	var dev = &fakeSnoopDevice{global: true, snoop: map[int]bool{99: true},
		trust: map[int]bool{28: true}}
	var c = newFakeSnoopClient(dev)
	var res, err = c.ApplyDHCPSnooping(t.Context(), driver.DHCPSnoopPlan{
		VLANs: []int{99},
		Ports: []driver.DHCPSnoopPortIntent{{Port: "GE28", Trusted: false}},
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if dev.trust[28] {
		t.Fatal("the untrust was asked for and must have landed")
	}
	if !hasNote(res.Notes, "NO port on this device is trusted") {
		t.Errorf("this apply left the switch with no trusted port: %v", res.Notes)
	}
}

// TestApplyDHCPSnoopingLeavesAConvergedPortAlone: sending nothing beats a round
// trip that changes nothing — and a port already in the wanted state must not
// appear in PortsWritten (driver.ApplyResult.PortsWritten).
func TestApplyDHCPSnoopingLeavesAConvergedPortAlone(t *testing.T) {
	var dev = &fakeSnoopDevice{global: true, snoop: map[int]bool{99: true},
		trust: map[int]bool{28: true}}
	var c = newFakeSnoopClient(dev)
	var res, err = c.ApplyDHCPSnooping(t.Context(), driver.DHCPSnoopPlan{
		VLANs: []int{99},
		Ports: []driver.DHCPSnoopPortIntent{
			{Port: "GE28", Trusted: true}, // already trusted
			{Port: "GE5", Trusted: false}, // already untrusted (block read, no trust line)
		},
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(res.PortsWritten) != 0 {
		t.Errorf("nothing needed writing: %v", res.PortsWritten)
	}
	if strings.Contains(strings.Join(dev.sent, "|"), "interface") {
		t.Errorf("a converged port must cost no command: %v", dev.sent)
	}
	// No re-read either: there is nothing to verify.
	if strings.Count(strings.Join(dev.sent, "|"), "show run") != 1 {
		t.Errorf("a gesture that wrote nothing needs no verifying re-read: %v", dev.sent)
	}
}

// TestApplyDHCPSnoopingWritesAPortItNeverRead: absent from the reading is NOT
// converged. A port whose block "show run" did not print must be written.
func TestApplyDHCPSnoopingWritesAPortItNeverRead(t *testing.T) {
	var dev = &fakeSnoopDevice{global: true, snoop: map[int]bool{99: true},
		trust: map[int]bool{28: true}, ports: []int{1, 28}}
	var c = newFakeSnoopClient(dev)
	var res, err = c.ApplyDHCPSnooping(t.Context(), driver.DHCPSnoopPlan{
		VLANs: []int{99},
		// GE7 has no block in this device's "show run": unread, not untrusted.
		Ports: []driver.DHCPSnoopPortIntent{{Port: "GE7", Trusted: false}},
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !strings.Contains(strings.Join(dev.sent, "|"), "no dhcp snooping trust") {
		t.Errorf("an unread port must be written, not assumed converged: %v", dev.sent)
	}
	// The write cannot be confirmed either — the device prints no block for GE7,
	// so it must not be claimed as written.
	if len(res.PortsWritten) != 0 && !anySkipped(res.Skipped, "GE7") {
		t.Errorf("an unverifiable write must not be reported as done: written=%v skipped=%v",
			res.PortsWritten, res.Skipped)
	}
}

func TestApplyDHCPSnoopingSkipsVLANsOutsideTheRange(t *testing.T) {
	var dev = &fakeSnoopDevice{global: true, trust: map[int]bool{28: true}}
	var c = newFakeSnoopClient(dev)
	var res, err = c.ApplyDHCPSnooping(t.Context(), driver.DHCPSnoopPlan{
		VLANs: []int{0, 4095, 99},
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !anySkipped(res.Skipped, "VLAN 0") || !anySkipped(res.Skipped, "VLAN 4095") {
		t.Errorf("Skipped=%v", res.Skipped)
	}
	if !dev.snoop[99] {
		t.Error("one impossible VLAN must not cost the others their protection")
	}
}

// TestApplyDHCPSnoopingWithNoVLANSaysSnoopingCoversNothing: the plan's empty
// VLAN list means the site declares none — the global feature goes on and the
// caller is told the trust flag protects nothing yet.
func TestApplyDHCPSnoopingWithNoVLANSaysSnoopingCoversNothing(t *testing.T) {
	var dev = &fakeSnoopDevice{}
	var c = newFakeSnoopClient(dev)
	var res, err = c.ApplyDHCPSnooping(t.Context(), driver.DHCPSnoopPlan{
		Ports: []driver.DHCPSnoopPortIntent{{Port: "GE28", Trusted: true}},
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !hasNote(res.Notes, "the plan names no VLAN") {
		t.Errorf("notes=%v", res.Notes)
	}
	if strings.Contains(strings.Join(dev.sent, "|"), "snooping vlan") {
		t.Errorf("no VLAN in the plan means no VLAN command: %v", dev.sent)
	}
	if !dev.global {
		t.Error("the global feature is still turned on")
	}
}

func TestApplyDHCPSnoopingReportsAFailedPersist(t *testing.T) {
	var dev = &fakeSnoopDevice{global: true, snoop: map[int]bool{99: true}, saveFails: true}
	var c = newFakeSnoopClient(dev)
	var res, err = c.ApplyDHCPSnooping(t.Context(), driver.DHCPSnoopPlan{
		VLANs: []int{99},
		Ports: []driver.DHCPSnoopPortIntent{{Port: "GE28", Trusted: true}},
	})
	// A persistence failure does not cancel the write (driver/connector.go).
	if err != nil {
		t.Fatalf("a save failure is not an apply failure: %v", err)
	}
	if !hasNote(res.Notes, "NOT persisted") {
		t.Errorf("a reboot would undo this: it must be said: %v", res.Notes)
	}
	if strings.Join(res.PortsWritten, ",") != "GE28" {
		t.Errorf("the write IS in place: %v", res.PortsWritten)
	}
}

func TestApplyDHCPSnoopingRefusalsBeforeTouchingTheDevice(t *testing.T) {
	var c = &Client{}
	var _, err = c.ApplyDHCPSnooping(t.Context(), driver.DHCPSnoopPlan{})
	if err == nil {
		t.Fatal("no session: the write must be refused")
	}
	var dev = &fakeSnoopDevice{}
	c = newFakeSnoopClient(dev)
	c.dialect = dialectUnknown
	if _, err = c.ApplyDHCPSnooping(t.Context(), driver.DHCPSnoopPlan{VLANs: []int{99}}); err == nil {
		t.Fatal("unknown dialect: the write must be refused")
	}
	if len(dev.sent) != 0 {
		t.Errorf("nothing may reach an unsurveyed CLI: %v", dev.sent)
	}
}

// TestDHCPSnoopStagingIsHonest: the staging is FINISHED, and this test now pins
// the other end of it.
//
// It used to assert the button stayed OFF, because all three write FORMS were
// proven (2026-08-10) while the applier that sends them had only ever been driven
// by the fake CLI below — forms are not appliers, which is the distinction the
// arista port_admin case established. On 2026-08-11 the applier was driven end to
// end against s3900lab (FSOS 1.7.7) by live_snoop_test.go: global enable
// confirmed by re-read, GE26 — the LLDP-confirmed uplink — returned in
// PortsWritten, an independent ReadDHCPSnooping agreeing, a second identical
// apply writing nothing, and a teardown that restored "show dhcp snooping
// config" byte for byte. So the declaration is EARNED and the assertion flips.
//
// It stays a test rather than disappearing: the day someone removes the applier
// or the live harness, the declaration must not be left behind pointing at
// nothing (cf. contract.go's WritesExtra note for what the run did and did not
// establish).
func TestDHCPSnoopStagingIsHonest(t *testing.T) {
	if !(&Driver{}).WritesExtra("dhcp_snooping") {
		t.Error("ApplyDHCPSnooping was exercised against hardware on 2026-08-11 " +
			"(live_snoop_test.go): the capability is earned and the API layer would " +
			"otherwise refuse what this connector has been proven to write")
	}
	if !strings.Contains(snoopFormsNote, VerifiedOpCode) {
		t.Errorf("the note must name the firmware the command set was surveyed on: %q",
			snoopFormsNote)
	}
	// It must name what IS proven and spell the forms, and it must name what is
	// NOT — an operator who reads a caveat that overstates OR understates the
	// evidence stops reading the caveats.
	if !strings.Contains(snoopFormsNote, "CAPTURED") ||
		!strings.Contains(snoopFormsNote, "dhcp snooping vlan N") ||
		!strings.Contains(snoopFormsNote, "dhcp snooping trust") {
		t.Errorf("the note must name the forms that ARE proven, and spell them: %q", snoopFormsNote)
	}
	if !strings.Contains(snoopFormsNote, "APPLIER") {
		t.Errorf("the note must say that the applier driving those forms is still unproven: %q",
			snoopFormsNote)
	}
	var _, isReader = driver.AsDHCPSnoopReader(&Client{})
	var _, isApplier = driver.AsDHCPSnoopApplier(&Client{})
	if !isReader || !isApplier {
		t.Error("the connector must be discoverable through the contract's own probes")
	}
}

// --- test helpers ---

func hasNote(notes []string, needle string) bool {
	var n string
	for _, n = range notes {
		if strings.Contains(n, needle) {
			return true
		}
	}
	return false
}

func anySkipped(skipped []string, needle string) bool { return hasNote(skipped, needle) }

func sameInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	var i int
	for i = range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func assertSent(t *testing.T, got, want []string) {
	t.Helper()
	var i int
	for i = 0; i < len(got) && i < len(want); i++ {
		if got[i] != want[i] {
			t.Errorf("line %d: sent %q, want %q", i, got[i], want[i])
		}
	}
	if len(got) != len(want) {
		t.Errorf("sent %d lines, want %d:\ngot  %v\nwant %v", len(got), len(want), got, want)
	}
}
