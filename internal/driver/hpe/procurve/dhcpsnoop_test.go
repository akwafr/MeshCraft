package procurve

// DHCP snooping on the 2610, held to driver/dhcpsnoop.go.
//
// # Which fixtures are CAPTURED and which are CONSTRUCTED
//
// The primary fixture is CAPTURED: testdata/show_running_config.txt is verbatim
// output of SW-PLAYER-79 (ProCurve 2610-48 / J9088A, R.11.121, 2026-08-03), and
// it happens to carry a live snooping configuration — the global
// "dhcp-snooping", eight "interface N / dhcp-snooping trust" stanzas on
// 45-52, and NO "dhcp-snooping vlan" line at all. Everything the parser must
// get right about the shapes this device produces is asserted against that file.
//
// The remaining fixtures in this file are CONSTRUCTED, and each says so in
// words above itself. They exist because the device has never been observed
// producing the forms in question: it was never seen carrying a
// "dhcp-snooping vlan" line, never seen with an "interface 45-52" range
// stanza, and never seen printing "no dhcp-snooping trust". Those forms come
// from the command grammar dhcpsnoop.go's own header names, and a test written
// against them proves the PARSER survives them — it proves nothing about the
// hardware. That is exactly why Driver.WritesExtra("dhcp_snooping") is still
// false (cf. TestDriverDeclaresOnlyTheExtrasItWrites).
//
// # What cannot be tested here, and why
//
// Client.cli is a *cliConn holding a real *ssh.Session, so ReadDHCPSnooping and
// ApplyDHCPSnooping cannot be driven end to end without a switch. The behaviour
// the contract cares about is therefore held in the pure functions those two
// methods are built out of — snoopPortStates, snoopGlobalCmds, snoopPortPlan and
// snoopCoverageNote — which is the same shape cisco/ios and fs/s3900 use for
// this contract.

import (
	"context"
	"strings"
	"testing"

	"meshcraft/internal/driver"
)

// ---------------------------------------------------------------------------
// The parser, against the CAPTURED configuration
// ---------------------------------------------------------------------------

// The reference unit is the whole reason this connector reads three levels: it
// carries the global switch and eight trusted uplinks, and it inspects NOTHING
// because no VLAN is named. Every number below is what that file says.
func TestParseDHCPSnoopingFromTheCapturedRunningConfig(t *testing.T) {
	var snoop = parseDHCPSnooping(fixture(t, "show_running_config.txt"))

	if !snoop.Enabled {
		t.Fatal("global snooping read as OFF: the capture carries a bare \"dhcp-snooping\" line")
	}
	// "no dhcp-relay" sits on the line ABOVE it and must not be read as a
	// removal of snooping.
	if len(snoop.VLANs) != 0 {
		t.Errorf("VLANs = %v, want none: the capture has no \"dhcp-snooping vlan\" line at all",
			snoop.VLANs)
	}
	if len(snoop.Trusted) != 8 {
		t.Fatalf("read %d trust flags, want the 8 uplink stanzas: %v",
			len(snoop.Trusted), snoop.Trusted)
	}
	var n int
	for n = 45; n <= 52; n++ {
		var trusted, stated = snoop.Trusted[n]
		if !stated {
			t.Errorf("port %d absent from the trust flags: its \"interface %d / "+
				"dhcp-snooping trust\" stanza was not read", n, n)
		}
		if !trusted {
			t.Errorf("port %d read as untrusted", n)
		}
	}
	// THE DISTINCTION THE CONTRACT ASKS FOR, at the parser's level: the
	// configuration says nothing about ports 1-44, and the map must say nothing
	// about them either. A parser that pre-filled them with false would hand
	// ReadDHCPSnooping forty-four readings it never made.
	for n = 1; n <= 44; n++ {
		var _, stated = snoop.Trusted[n]
		if stated {
			t.Fatalf("port %d carries a trust flag: the configuration does not mention it, "+
				"and inventing one turns 'not stated' into a reading", n)
		}
	}
	// "interface 1 / disable / exit" is in the capture. An interface stanza
	// without a trust line must not produce a flag.
	var _, one = snoop.Trusted[1]
	if one {
		t.Error("\"interface 1 / disable\" produced a trust flag")
	}
}

// The global anchor is a BARE line, and three neighbouring forms would each
// match a looser one. CONSTRUCTED from the command grammar dhcpsnoop.go's header
// names — NOT captured from hardware.
func TestSnoopGlobalAnchorIsNotFooledByTheNeighbouringForms(t *testing.T) {
	// CONSTRUCTED, not captured: a device with snooping explicitly removed.
	var off = "no dhcp-relay \nno dhcp-snooping \nspanning-tree\n"
	if parseDHCPSnooping(off).Enabled {
		t.Error("\"no dhcp-snooping\" read as ON: the removal was taken for an assignment")
	}

	// CONSTRUCTED, not captured: coverage stated without the global line. On this
	// range that is not a state the device produces, but a parser that read the
	// VLAN line as the global switch would report protection that is not on.
	var vlanOnly = "dhcp-snooping vlan 10 \nspanning-tree\n"
	if parseDHCPSnooping(vlanOnly).Enabled {
		t.Error("\"dhcp-snooping vlan 10\" was read as the global switch")
	}

	// CONSTRUCTED, not captured: trust without the global line — the per-port
	// flag must not turn the switch-wide feature on either.
	var trustOnly = "interface 45\n   dhcp-snooping trust\n   exit\n"
	if parseDHCPSnooping(trustOnly).Enabled {
		t.Error("\"dhcp-snooping trust\" was read as the global switch")
	}
	if !parseDHCPSnooping(trustOnly).Trusted[45] {
		t.Error("the trust flag itself went missing")
	}
}

// snoopConfigLineEndings is CONSTRUCTED, not captured — but the BYTE PAIRING is
// captured, and that is the point. This device terminates its configuration
// lines with LF then CR (cf. crlf_test.go), so every line arrives with the
// previous terminator's stray carriage return glued to its front, and it pads
// some lines with a trailing blank and not others. "interface 45" in the real
// capture has no trailing space and "interface 1" has one, so both spellings
// are here.
//
// The snooping LINES are constructed: "dhcp-snooping vlan" and "no
// dhcp-snooping trust" have never been seen on this hardware.
const snoopConfigLineEndings = "no dhcp-relay \n\r" +
	"dhcp-snooping\n\r" +
	"dhcp-snooping vlan 1-4 99 \n\r" +
	"interface 45\n\r   dhcp-snooping trust\n\r   exit\n\r" +
	"interface 46 \n\r   no dhcp-snooping trust\n\r   exit \n\r" +
	"spanning-tree\n\r"

func TestParseDHCPSnoopingSurvivesTheDevicesLineEndings(t *testing.T) {
	var snoop = parseDHCPSnooping(snoopConfigLineEndings)

	if !snoop.Enabled {
		t.Error("global snooping read as OFF: the anchor did not survive the leading CR")
	}
	if strings.Join(intsToStrings(snoop.VLANs), ",") != "1,2,3,4,99" {
		t.Errorf("VLANs = %v, want 1-4 and 99: the VLAN anchor did not survive the "+
			"leading CR or the trailing space", snoop.VLANs)
	}
	var trusted, stated = snoop.Trusted[45]
	if !stated || !trusted {
		t.Errorf("port 45 = %v,%v — the stanza with NO trailing space was missed",
			trusted, stated)
	}
	// "interface 46" carries a trailing space, and its "exit" does too.
	trusted, stated = snoop.Trusted[46]
	if !stated {
		t.Error("port 46 absent: the stanza WITH a trailing space was missed")
	}
	if trusted {
		t.Error("\"no dhcp-snooping trust\" was read as trust: the removal inverted")
	}
}

// An explicit removal is a READING, not a silence: the port must be in the map
// with false, so ReadDHCPSnooping reports it and the applier knows not to
// re-send. CONSTRUCTED from the grammar — the device has never been seen
// printing this form.
func TestParseDHCPSnoopingSeparatesExplicitUntrustFromSilence(t *testing.T) {
	// CONSTRUCTED, not captured.
	var cfg = "dhcp-snooping\n" +
		"interface 45\n   no dhcp-snooping trust\n   exit\n" +
		"interface 46\n   dhcp-snooping trust\n   exit\n"
	var snoop = parseDHCPSnooping(cfg)

	var trusted, stated = snoop.Trusted[45]
	if !stated {
		t.Fatal("an explicitly untrusted port is absent from the map: the applier would " +
			"treat it as never read")
	}
	if trusted {
		t.Error("port 45 read as trusted")
	}
	if !snoop.Trusted[46] {
		t.Error("port 46 read as untrusted: the following stanza was contaminated by the \"no\"")
	}
	var _, ghost = snoop.Trusted[47]
	if ghost {
		t.Error("a port no stanza mentions acquired a flag")
	}
}

// This CLI accepts "interface 45-52" even though it never writes it back, and
// a configuration edited by hand carries it. CONSTRUCTED from the grammar
// dhcpsnoop.go names — NOT captured.
func TestParseDHCPSnoopingAcceptsAnInterfaceRangeStanza(t *testing.T) {
	// CONSTRUCTED, not captured.
	var cfg = "dhcp-snooping\ninterface 45-48,50\n   dhcp-snooping trust\n   exit\n"
	var snoop = parseDHCPSnooping(cfg)

	if len(snoop.Trusted) != 5 {
		t.Fatalf("read %d flags from \"interface 45-48,50\", want 5: %v",
			len(snoop.Trusted), snoop.Trusted)
	}
	var n int
	for _, n = range []int{45, 46, 47, 48, 50} {
		if !snoop.Trusted[n] {
			t.Errorf("port %d not trusted", n)
		}
	}
	// The off-by-one guard: 44 and 49 are OUTSIDE the range and must not appear
	// at all — not as false, not as anything.
	for _, n = range []int{44, 49} {
		var _, stated = snoop.Trusted[n]
		if stated {
			t.Errorf("port %d picked up a flag it is not in the range for", n)
		}
	}
}

// Coverage may be stated across several lines and in either delimiter, and what
// comes out must be sorted and deduplicated so two reads of one device compare
// equal. CONSTRUCTED from the grammar — NOT captured.
func TestParseDHCPSnoopingVLANsAreSortedAndDeduplicated(t *testing.T) {
	// CONSTRUCTED, not captured.
	var cfg = "dhcp-snooping\n" +
		"dhcp-snooping vlan 99 10\n" +
		"dhcp-snooping vlan 10,3-5\n" +
		"dhcp-snooping vlan 1\n"
	var got = strings.Join(intsToStrings(parseDHCPSnooping(cfg).VLANs), ",")
	if got != "1,3,4,5,10,99" {
		t.Errorf("VLANs = %s, want 1,3,4,5,10,99 sorted and deduplicated", got)
	}
}

// The two delimiters this platform mixes: its VLAN lists use spaces where its
// port lists use commas, and both must expand through the same grammar.
func TestParseNumberListAcceptsBothDelimiters(t *testing.T) {
	if strings.Join(intsToStrings(parseNumberList("1-4 10")), ",") != "1,2,3,4,10" {
		t.Errorf("space-delimited list = %v", parseNumberList("1-4 10"))
	}
	if strings.Join(intsToStrings(parseNumberList("10,1")), ",") != "1,10" {
		t.Errorf("comma-delimited list = %v", parseNumberList("10,1"))
	}
	if parseNumberList("   ") != nil {
		t.Error("whitespace produced numbers")
	}
}

func TestDedupSortedIsSortedDeduplicatedAndNilWhenEmpty(t *testing.T) {
	if dedupSorted(nil) != nil {
		t.Error("an empty list produced a non-nil slice: DHCPSnoopState.VLANs is omitempty")
	}
	var got = strings.Join(intsToStrings(dedupSorted([]int{20, 10, 20, 1, 10})), ",")
	if got != "1,10,20" {
		t.Errorf("dedupSorted = %s, want 1,10,20", got)
	}
}

// ---------------------------------------------------------------------------
// Rendering the state: "not read" is not "untrusted"
// ---------------------------------------------------------------------------

// THE CONTRACT POINT THIS FILE EXISTS FOR (driver/dhcpsnoop.go: "A port absent
// from this list was not observed, which is not the same as untrusted").
//
// A port no source mentions — not the faceplate, not the configuration — must be
// ABSENT from Ports. Reporting it as Trusted:false would put a reading on screen
// that nobody made.
func TestSnoopPortStatesLeaveUnreadPortsOutRatherThanCallingThemUntrusted(t *testing.T) {
	// The faceplate read came back with three ports; the configuration trusts 45.
	var faceplate = map[int]portStatus{1: {}, 2: {}, 3: {}}
	var snoop = dhcpSnoop{Enabled: true, Trusted: map[int]bool{45: true}}
	var got = snoopPortStates(snoop, faceplate, "GE")

	var names = portNamesOf(got)
	if strings.Join(names, ",") != "GE1,GE2,GE3,GE45" {
		t.Fatalf("Ports = %v, want exactly the ports the two reads name", names)
	}
	var st driver.DHCPSnoopPortState
	for _, st = range got {
		if st.Port == "GE45" && !st.Trusted {
			t.Error("GE45 is trusted in the configuration and reported untrusted")
		}
		if st.Port != "GE45" && st.Trusted {
			t.Errorf("%s reported trusted; the configuration does not say so", st.Port)
		}
	}
	// Ports 4-44 were never read. They must not be in the list AT ALL — this is
	// the assertion that fails if a connector defaults an unread port to
	// Trusted:false.
	var n int
	for n = 4; n <= 44; n++ {
		if hasPort(got, "GE"+itoa(n)) {
			t.Fatalf("GE%d is in Ports: neither the faceplate nor the configuration "+
				"mentions it, so reporting it — even as untrusted — is a claim about a "+
				"port that was not read", n)
		}
	}
}

// THE REGRESSION. The first version of this file enumerated the FACEPLATE only,
// so a "show interfaces brief" that came back short silently deleted the
// trusted ports from the state: eight trusted uplinks on the reference unit
// become zero, and the screen says no port on this switch is trusted.
func TestSnoopPortStatesNeverDropATrustedPortWhenTheFaceplateIsShort(t *testing.T) {
	// A truncated faceplate: the brief stopped at port 3.
	var faceplate = map[int]portStatus{1: {}, 2: {}, 3: {}}
	var trusted = map[int]bool{}
	var n int
	for n = 45; n <= 52; n++ {
		trusted[n] = true
	}
	var got = snoopPortStates(dhcpSnoop{Enabled: true, Trusted: trusted}, faceplate, "GE")

	for n = 45; n <= 52; n++ {
		var name = "GE" + itoa(n)
		if !hasPort(got, name) {
			t.Fatalf("%s vanished from the state: the configuration says it is TRUSTED and "+
				"a short faceplate read must not delete that", name)
		}
	}
	if strings.Join(portNamesOf(got), ",") !=
		"GE1,GE2,GE3,GE45,GE46,GE47,GE48,GE49,GE50,GE51,GE52" {
		t.Errorf("Ports = %v", portNamesOf(got))
	}
}

// Two reads of one switch must produce the same list in the same order, or drift
// compares a switch against itself and finds differences. Both inputs are maps,
// so this is the assertion that catches map iteration order leaking out.
func TestSnoopPortStatesAreSortedNumericallyAndStable(t *testing.T) {
	var faceplate = map[int]portStatus{2: {}, 11: {}, 1: {}, 45: {}, 3: {}, 10: {}}
	var snoop = dhcpSnoop{Enabled: true, Trusted: map[int]bool{45: true, 2: false}}
	var want = "GE1,GE2,GE3,GE10,GE11,GE45"

	var i int
	for i = 0; i < 20; i++ {
		var got = strings.Join(portNamesOf(snoopPortStates(snoop, faceplate, "GE")), ",")
		if got != want {
			t.Fatalf("Ports = %s, want %s — sorted by NUMBER and not by name, and not "+
				"by whatever order the map handed back", got, want)
		}
	}
}

// The faceplate prefix is the inventory's, not a constant: a site that names its
// ports differently must see its own names here.
func TestSnoopPortStatesUseTheInventoryPortPrefix(t *testing.T) {
	var got = snoopPortStates(dhcpSnoop{Trusted: map[int]bool{7: true}},
		map[int]portStatus{7: {}}, "ether")
	if len(got) != 1 || got[0].Port != "ether7" {
		t.Errorf("Ports = %v, want ether7", portNamesOf(got))
	}
}

// A device with the feature on and no VLAN named inspects nothing, and the
// reference unit is in exactly that state. An empty DHCPSnoopState.VLANs must
// never be left to read as "none" (driver/dhcpsnoop.go).
func TestSnoopCoverageNoteExplainsAnEmptyVLANList(t *testing.T) {
	var off = snoopCoverageNote(dhcpSnoop{Enabled: false})
	if len(off) != 1 || !strings.Contains(off[0], "OFF switch-wide") {
		t.Fatalf("notes for a device with snooping off = %v, want one saying the trust "+
			"flags are inert", off)
	}

	var bare = snoopCoverageNote(dhcpSnoop{Enabled: true})
	if len(bare) != 1 {
		t.Fatalf("notes for ON-with-no-VLAN = %v, want exactly one", bare)
	}
	if !strings.Contains(bare[0], "NO VLAN") {
		t.Errorf("note = %q, want it to say the coverage is empty rather than leaving "+
			"an empty VLAN list to read as \"none\"", bare[0])
	}
	// It must name the mechanism, not just the fact: an operator has to know this
	// range needs "dhcp-snooping vlan" before a trust flag means anything.
	if !strings.Contains(bare[0], "dhcp-snooping vlan") {
		t.Errorf("note = %q, want it to name the command that fixes it", bare[0])
	}

	var covered = snoopCoverageNote(dhcpSnoop{Enabled: true, VLANs: []int{10}})
	if len(covered) != 0 {
		t.Errorf("notes on a properly covered device = %v, want none", covered)
	}
}

func TestCoveredBeforeWordsAnEmptyListAsAFact(t *testing.T) {
	if coveredBefore(nil) != "none" {
		t.Errorf("coveredBefore(nil) = %q, want \"none\" and not a blank in a sentence",
			coveredBefore(nil))
	}
	if coveredBefore([]int{10, 11, 12, 99}) != "10-12,99" {
		t.Errorf("coveredBefore = %q", coveredBefore([]int{10, 11, 12, 99}))
	}
}

// ---------------------------------------------------------------------------
// The switch-wide half of the write
// ---------------------------------------------------------------------------

// IT NEVER TURNS THE FEATURE OFF, and it never drops a VLAN it found. There is
// no plan — not an empty one, not one naming fewer VLANs than the device carries
// — that produces a removal (driver/dhcpsnoop.go).
func TestSnoopGlobalCmdsNeverDisableAnything(t *testing.T) {
	var before = dhcpSnoop{Enabled: true, VLANs: []int{10, 20}}
	var plans = [][]int{nil, {}, {10}, {20}, {10, 20}, {30}}
	var p []int
	for _, p = range plans {
		var cmds, union, _ = snoopGlobalCmds(before, p)
		var cmd string
		for _, cmd = range cmds {
			if strings.HasPrefix(strings.TrimSpace(cmd), "no ") {
				t.Errorf("plan %v produced %q: this applier must never turn snooping off "+
					"or remove coverage", p, cmd)
			}
		}
		// Whatever the plan says, the device's own VLANs survive into the union.
		if !containsInt(union, 10) || !containsInt(union, 20) {
			t.Errorf("plan %v produced union %v: VLAN 10 or 20 was dropped, and "+
				"protection meshcraft did not install must not be taken away", p, union)
		}
	}
	// A plan that asks for nothing new asks for nothing at all.
	var cmds, _, adds = snoopGlobalCmds(before, nil)
	if len(cmds) != 0 || adds {
		t.Errorf("an empty plan on a covered device produced %v (adds=%v)", cmds, adds)
	}
}

// THE UNION, NOT THE REPLACEMENT. "dhcp-snooping vlan <list>" has never been
// captured on this hardware and the documentation does not settle whether it
// accumulates or replaces; sending only the missing VLAN is correct under one
// reading and silently strips the switch under the other.
func TestSnoopGlobalCmdsSendTheUnionAndNotOnlyTheAddition(t *testing.T) {
	var before = dhcpSnoop{Enabled: true, VLANs: []int{10, 20}}
	var cmds, union, adds = snoopGlobalCmds(before, []int{30})

	if !adds {
		t.Fatal("adding VLAN 30 to a device covering 10 and 20 was read as no change")
	}
	if strings.Join(intsToStrings(union), ",") != "10,20,30" {
		t.Errorf("union = %v, want 10,20,30", union)
	}
	if strings.Join(cmds, "|") != "dhcp-snooping vlan 10,20,30" {
		t.Fatalf("commands = %v, want the FULL list. \"dhcp-snooping vlan 30\" would be "+
			"correct only if this form accumulates, and would strip VLANs 10 and 20 if "+
			"it replaces", cmds)
	}
}

// The global line comes FIRST, because a coverage statement on a device whose
// feature is off configures nothing.
func TestSnoopGlobalCmdsTurnTheFeatureOnBeforeStatingCoverage(t *testing.T) {
	var cmds, _, _ = snoopGlobalCmds(dhcpSnoop{Enabled: false}, []int{99, 10})
	if strings.Join(cmds, "|") != "dhcp-snooping|dhcp-snooping vlan 10,99" {
		t.Fatalf("commands = %v, want the global switch then the coverage", cmds)
	}
}

// A plan with no VLANs on a device with the feature off turns the feature on and
// nothing else — the contract's "the connector then turns on the global feature
// only" (driver/dhcpsnoop.go, DHCPSnoopPlan.VLANs).
func TestSnoopGlobalCmdsOnAPlanWithNoVLANs(t *testing.T) {
	var cmds, union, adds = snoopGlobalCmds(dhcpSnoop{Enabled: false}, nil)
	if strings.Join(cmds, "|") != "dhcp-snooping" {
		t.Errorf("commands = %v, want the global switch alone", cmds)
	}
	if adds || len(union) != 0 {
		t.Errorf("union = %v, adds = %v: nothing was asked for", union, adds)
	}
	// And the operator must be told the feature covers nothing. That sentence
	// comes from snoopCoverageNote, on the state AFTER the write.
	if len(snoopCoverageNote(dhcpSnoop{Enabled: true})) == 0 {
		t.Error("no note for a switch that is on and covers nothing")
	}
}

// Two identical gestures must produce two identical requests, whatever order the
// caller listed its VLANs in and however many times it repeated one.
func TestSnoopGlobalCmdsAreIdenticalForEquivalentPlans(t *testing.T) {
	var before = dhcpSnoop{Enabled: true, VLANs: []int{10}}
	var a, _, _ = snoopGlobalCmds(before, []int{99, 30, 99, 10})
	var b, _, _ = snoopGlobalCmds(before, []int{10, 30, 99})
	if strings.Join(a, "|") != strings.Join(b, "|") {
		t.Fatalf("two equivalent plans produced %v and %v", a, b)
	}
	if strings.Join(a, "|") != "dhcp-snooping vlan 10,30,99" {
		t.Errorf("commands = %v, want a sorted deduplicated list", a)
	}
}

// ---------------------------------------------------------------------------
// The per-port half of the write
// ---------------------------------------------------------------------------

// Exact command order, from a plan deliberately handed over out of order and
// with one socket named twice under two spellings. Anything but this exact
// sequence means two identical gestures can produce two different requests.
func TestSnoopPortPlanIsOrderedAndDeduplicated(t *testing.T) {
	var before = dhcpSnoop{Enabled: true, Trusted: map[int]bool{45: true}}
	var writes, skipped = snoopPortPlan(before, []driver.DHCPSnoopPortIntent{
		{Port: "GE10", Trusted: true},
		{Port: "GE2", Trusted: true},
		{Port: "GE45", Trusted: false},
		{Port: "2", Trusted: true}, // the SAME socket as GE2 on this platform
	})

	if len(skipped) != 0 {
		t.Fatalf("skipped = %v, want none: every name translates", skipped)
	}
	if len(writes) != 3 {
		t.Fatalf("planned %d writes, want 3 sockets: %v", len(writes), flatten(writes))
	}
	var got = strings.Join(flatten(writes), "|")
	var want = "interface 2|dhcp-snooping trust|exit|" +
		"interface 10|dhcp-snooping trust|exit|" +
		"interface 45|no dhcp-snooping trust|exit"
	if got != want {
		t.Fatalf("commands =\n  %s\nwant\n  %s", got, want)
	}
	// Bare numbers on the faceplate, and the number is the number: "interface 2"
	// and not "interface 1", not "interface GE2", not "interface 3".
	if strings.Contains(got, "GE") {
		t.Error("a facade name leaked into a CLI command")
	}
	// ApplyResult still speaks the caller's vocabulary: the name reported for a
	// socket named twice is the LAST spelling the plan used, which is the same
	// intent that won.
	if writes[0].Port != "2" || writes[1].Port != "GE10" || writes[2].Port != "GE45" {
		t.Errorf("reported names = %v, want the caller's own spellings",
			[]string{writes[0].Port, writes[1].Port, writes[2].Port})
	}
}

// The LAST intent for a socket wins, because that is the state sending both
// commands in the caller's order would have left on the device.
func TestSnoopPortPlanResolvesConflictingIntentsToTheLastOne(t *testing.T) {
	var writes, _ = snoopPortPlan(dhcpSnoop{Enabled: true, Trusted: map[int]bool{}},
		[]driver.DHCPSnoopPortIntent{
			{Port: "GE45", Trusted: true},
			{Port: "GE45", Trusted: false},
		})
	if len(writes) != 1 {
		t.Fatalf("planned %d writes for one socket: %v", len(writes), flatten(writes))
	}
	if writes[0].Trusted {
		t.Error("the FIRST intent won: the device would have ended untrusted")
	}
	// Untrusted is this platform's default and its silence, so nothing to send.
	if len(writes[0].Cmds) != 0 {
		t.Errorf("commands = %v, want none: the port is not stated as trusted", writes[0].Cmds)
	}
}

// A name this platform cannot translate is an ORDINARY REFUSAL: it lands in
// Skipped and costs the rest of the plan nothing (driver/dhcpsnoop.go).
func TestSnoopPortPlanPutsUntranslatableNamesInSkipped(t *testing.T) {
	var writes, skipped = snoopPortPlan(dhcpSnoop{Trusted: map[int]bool{}},
		[]driver.DHCPSnoopPortIntent{
			{Port: "craft", Trusted: true},
			{Port: "GE0", Trusted: true},
			{Port: "GE1/0/2", Trusted: true},
			{Port: "GE7", Trusted: true},
		})

	if len(skipped) != 3 {
		t.Fatalf("skipped = %v, want the three names this CLI has no number for", skipped)
	}
	var s string
	for _, s = range skipped {
		if !strings.Contains(s, "not modified") {
			t.Errorf("skip reason = %q, want it to say the port was not changed", s)
		}
	}
	// And the usable port was still planned: one bad name must not void a gesture.
	if strings.Join(flatten(writes), "|") != "interface 7|dhcp-snooping trust|exit" {
		t.Errorf("writes = %v, want GE7 planned anyway", flatten(writes))
	}
}

// A port that already carries the intent gets NO commands — and is still
// returned, so the verifying re-read checks it too. "It was already trusted" is
// a claim about the device, and this connector confirms it rather than assuming.
func TestSnoopPortPlanWritesNothingForPortsThatAlreadyCarryTheIntent(t *testing.T) {
	var before = dhcpSnoop{Enabled: true, Trusted: map[int]bool{45: true, 46: false}}
	var writes, _ = snoopPortPlan(before, []driver.DHCPSnoopPortIntent{
		{Port: "GE45", Trusted: true},  // stated as trusted already
		{Port: "GE46", Trusted: false}, // stated as untrusted already
		{Port: "GE7", Trusted: false},  // not stated: untrusted is the default
	})

	if len(writes) != 3 {
		t.Fatalf("planned %d entries, want all three carried into the verification set",
			len(writes))
	}
	var w snoopPortWrite
	for _, w = range writes {
		if len(w.Cmds) != 0 {
			t.Errorf("%s produced %v: nothing needed writing", w.Port, w.Cmds)
		}
	}
	// Sorted by number, and each intent carried so the re-read verifies it.
	if writes[0].Num != 7 || writes[0].Trusted {
		t.Errorf("entry 0 = %+v, want port 7 untrusted", writes[0])
	}
	if writes[1].Num != 45 || !writes[1].Trusted {
		t.Errorf("entry 1 = %+v, want port 45 trusted", writes[1])
	}
	if writes[2].Num != 46 || writes[2].Trusted {
		t.Errorf("entry 2 = %+v, want port 46 untrusted", writes[2])
	}
}

// An untrusted intent on a port the configuration does not mention needs no
// write — but an untrusted intent on a port it states as TRUSTED does, and that
// write is the only "no" form in this file. It removes trust from ONE port and
// nothing else.
func TestSnoopPortPlanEmitsNoDisableFormBeyondPerPortUntrust(t *testing.T) {
	var befores = []dhcpSnoop{
		{Enabled: false, Trusted: map[int]bool{}},
		{Enabled: true, Trusted: map[int]bool{}},
		{Enabled: true, VLANs: []int{10}, Trusted: map[int]bool{45: true, 46: false}},
	}
	var before dhcpSnoop
	for _, before = range befores {
		var trusted bool
		for _, trusted = range []bool{true, false} {
			var writes, _ = snoopPortPlan(before, []driver.DHCPSnoopPortIntent{
				{Port: "GE45", Trusted: trusted},
				{Port: "GE46", Trusted: trusted},
			})
			var cmd string
			for _, cmd = range flatten(writes) {
				switch {
				case cmd == "no dhcp-snooping trust":
					// The per-port removal the contract explicitly allows.
				case strings.HasPrefix(cmd, "no "):
					t.Errorf("before=%+v trusted=%v produced %q: only per-port untrust "+
						"may ever be a removal", before, trusted, cmd)
				}
			}
		}
	}
}

// ---------------------------------------------------------------------------
// The session guard
// ---------------------------------------------------------------------------

// Both halves of the contract need a shell and say so rather than reporting an
// empty state as a reading, or an empty result as a write.
func TestDHCPSnoopingRefusesWithoutASession(t *testing.T) {
	var c = &Client{}
	var _, err = c.ReadDHCPSnooping(context.Background())
	if err == nil {
		t.Error("ReadDHCPSnooping returned a state with no session open")
	}
	var _, aerr = c.ApplyDHCPSnooping(context.Background(), driver.DHCPSnoopPlan{
		Ports: []driver.DHCPSnoopPortIntent{{Port: "GE45", Trusted: true}},
	})
	if aerr == nil {
		t.Error("ApplyDHCPSnooping reported success with no session open")
	}
}

// ---------------------------------------------------------------------------
// The small helpers the result messages rest on
// ---------------------------------------------------------------------------

func TestWithoutStringsRemovesInPlaceOrder(t *testing.T) {
	var got = withoutStrings([]string{"GE1", "GE2", "GE3"}, []string{"GE2"})
	if strings.Join(got, ",") != "GE1,GE3" {
		t.Errorf("PortsWritten after a failed verification = %v, want GE1,GE3", got)
	}
	if strings.Join(withoutStrings([]string{"GE1"}, nil), ",") != "GE1" {
		t.Error("an empty removal list changed the input")
	}
	if len(withoutStrings([]string{"GE1"}, []string{"GE1"})) != 0 {
		t.Error("removing the only entry left something behind")
	}
}

// A message that reorders itself between two reads of the same failure reads as
// a different failure.
func TestSortStringsIsStable(t *testing.T) {
	var v = []string{"GE9", "GE10", "GE1"}
	sortStrings(v)
	if strings.Join(v, ",") != "GE1,GE10,GE9" {
		t.Errorf("sorted = %v", v)
	}
	var empty []string
	sortStrings(empty) // must not panic
}

// ---------------------------------------------------------------------------
// Test-local helpers
// ---------------------------------------------------------------------------

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	var v = n
	for v > 0 {
		digits = append([]byte{byte('0' + v%10)}, digits...)
		v /= 10
	}
	return string(digits)
}

func intsToStrings(v []int) []string {
	var out []string
	var n int
	for _, n = range v {
		out = append(out, itoa(n))
	}
	return out
}

func portNamesOf(ps []driver.DHCPSnoopPortState) []string {
	var out []string
	var p driver.DHCPSnoopPortState
	for _, p = range ps {
		out = append(out, p.Port)
	}
	return out
}

func hasPort(ps []driver.DHCPSnoopPortState, name string) bool {
	var p driver.DHCPSnoopPortState
	for _, p = range ps {
		if p.Port == name {
			return true
		}
	}
	return false
}

// flatten is what the device would receive, in order.
func flatten(writes []snoopPortWrite) []string {
	var out []string
	var w snoopPortWrite
	for _, w = range writes {
		out = append(out, w.Cmds...)
	}
	return out
}
