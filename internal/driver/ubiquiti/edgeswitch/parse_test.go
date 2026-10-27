package edgeswitch

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"meshcraft/internal/driver"
)

func mustRead(t *testing.T, path string) string {
	t.Helper()
	var b, err = os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(b)
}

// The regression this guards: 0/17 and 0/18 (the two SFP+ cages) must be
// told apart from the sixteen copper sockets by their "Auto D" Physical
// Mode — replayed against the REAL capture, not a synthetic fixture, so a
// future edit that stops recognizing the actual device output fails here.
func TestParseShowPortAllFlagsSFPCages(t *testing.T) {
	var out = mustRead(t, "testdata/show_port_all.txt")
	var rows = parseShowPortAll(out)
	var byName = map[string]PortLine{}
	var p PortLine
	for _, p = range rows {
		if p.Name != "" {
			byName[p.Name] = p
		}
	}
	if len(byName) != 18 {
		t.Fatalf("expected 18 faceplate rows (16 copper + 2 SFP+), got %d: %+v", len(byName), byName)
	}
	if byName["GE17"].SFPCage != true || byName["GE18"].SFPCage != true {
		t.Errorf("GE17/GE18 must be flagged as SFP+ cages: %+v / %+v", byName["GE17"], byName["GE18"])
	}
	var name string
	for name = range byName {
		if name == "GE17" || name == "GE18" {
			continue
		}
		if byName[name].SFPCage {
			t.Errorf("%s: a copper socket must not be flagged as an SFP+ cage", name)
		}
	}
	if byName["GE18"].Physical != "10G Full" {
		t.Errorf("GE18 (up in the capture) must read its negotiated 10G Full: %+v", byName["GE18"])
	}
}

func TestSfpTailCount(t *testing.T) {
	var got = sfpTailCount(18, map[int]bool{17: true, 18: true})
	if got != 2 {
		t.Errorf("18 ports, last two SFP: got %d, want 2", got)
	}
	got = sfpTailCount(18, map[int]bool{})
	if got != 0 {
		t.Errorf("no SFP flags: got %d, want 0", got)
	}
	// A gap breaks the contiguous-from-the-top rule: only the trailing run
	// counts, so a stray flag in the middle must not extend the SFP group
	// past the actual cages.
	got = sfpTailCount(18, map[int]bool{5: true, 17: true, 18: true})
	if got != 2 {
		t.Errorf("non-contiguous flag must not extend the tail: got %d, want 2", got)
	}
	// The model this connector has never met: cages in the MIDDLE of the
	// numbering, nothing SFP at the top. No trailing run means zero, which is
	// what makes portGroups fall back to ONE uniform group — the faceplate is
	// then drawn plainly rather than with a two-socket hole in it. Pinned
	// because that fallback is the whole reason an unsurveyed layout is safe.
	got = sfpTailCount(18, map[int]bool{5: true, 6: true})
	if got != 0 {
		t.Errorf("cages away from the top must count zero, not a middle run: got %d, want 0", got)
	}
}

// stpModeOf is the ONE place a firmware's wording of the spanning-tree
// version reaches the shared vocabulary.
//
// THREE wordings are now CAPTURED rather than assumed: "IEEE 802.1s"
// (1.9.3, testdata/show_spanning_tree_summary.txt) and, since 2026-08-10,
// "IEEE 802.1w" and "IEEE 802.1d" — taken from the same unit driven
// through "spanning-tree mode rstp" then "... stp" and restored to mst.
// TestParseSpanningTreePairRSTPAndSTPCaptures holds the pair of reads each
// one came from. "mstp" / "rstp" / "stp" are the detail read's own
// spelling and are captured alongside them.
//
// What is still the mapping's DEFENSIVE half, and the only part of this table
// no capture supports: "Rapid Spanning Tree" — no firmware in this fleet
// spells it that way — and the verbatim fallback.
//
// The last case is the contract that matters: an unrecognised wording passes
// through VERBATIM, lowercased. It is never rounded to a mode — a connector
// that answered "rstp" to a version line it did not understand would put a
// mode on the topology view that no capture supports.
func TestStpModeOf(t *testing.T) {
	var cases = []struct {
		in   string
		want string
	}{
		{"IEEE 802.1s", "mstp"},         // captured, firmware 1.9.3
		{"IEEE 802.1w", "rstp"},         // captured 2026-08-10, same unit forced to rstp
		{"IEEE 802.1d", "stp"},          // captured 2026-08-10, same unit forced to stp
		{"Rapid Spanning Tree", "rstp"}, // still defensive: nothing spells it this way
		{"mstp", "mstp"},                // the detail read's own spelling
		{"rstp", "rstp"},                // captured 2026-08-10, show_spanning_tree_rstp.txt
		{"stp", "stp"},                  // captured 2026-08-10, show_spanning_tree_stp.txt
		{"", ""},
		{"IEEE 802.1Q-2018", "ieee 802.1q-2018"}, // unknown: verbatim, not guessed
	}
	var c struct {
		in   string
		want string
	}
	for _, c = range cases {
		var got = stpModeOf(c.in)
		if got != c.want {
			t.Errorf("stpModeOf(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// The "MST0" header is NOT what mergeSpanningTreeDetail keys on.
//
// SYNTHETIC, and it was written when no FASTPATH unit forced to 802.1d/802.1w
// had been captured. One has since: TestParseSpanningTreePairRSTPAndSTPCaptures
// replays the real thing, and it confirms the shape guessed here — outside MST
// the detail read prints no "MST0" header at all. This case is kept because
// it is NARROWER than the captures: it strips the header while keeping fields
// the real dumps also carry, so the parser's shape-independence is asserted on
// its own rather than riding on one firmware's layout.
//
// What it DOES prove is the parser's own shape-independence —
// dottedFields matches "Label...... value" wherever it appears, so a detail
// read with no MST instance header at all still yields the identifiers. That
// narrows the open question from "does the parser need MST0" (it does not) to
// "does that firmware spell these labels the same way", which only a capture
// can answer.
func TestMergeSpanningTreeDetailWithoutMSTHeader(t *testing.T) {
	var st = driver.SpanningTreeState{Enabled: true}
	mergeSpanningTreeDetail(`
Spanning Tree Protocol......................... rstp
Bridge Priority................................ 28672
Bridge Identifier.............................. 70:00:9C:05:D6:F5:F1:37
Time Since Topology Change..................... 0 day 4 hr 12 min 7 sec
Designated Root................................ 00:00:D4:01:C3:AB:32:F4
Root Path Cost................................. 20000
Root Port Identifier........................... 0/18

Interface   State           Role
--------- ----------------- ----------
0/18      Forwarding        Root
`, &st)
	if st.Mode != "rstp" {
		t.Errorf("mode=%q — the summary said nothing, so the detail's own line must fill it", st.Mode)
	}
	if st.BridgePriority != 28672 || st.BridgeID != "9c:05:d6:f5:f1:37" {
		t.Errorf("bridge=%d/%q — want 28672 / 9c:05:d6:f5:f1:37 with no MST0 header present",
			st.BridgePriority, st.BridgeID)
	}
	if st.RootBridgeID != "d4:01:c3:ab:32:f4" || st.RootPriority != 0 || st.RootPathCost != 20000 {
		t.Errorf("root=%q prio=%d cost=%d — want d4:01:c3:ab:32:f4 / 0 / 20000",
			st.RootBridgeID, st.RootPriority, st.RootPathCost)
	}
	if st.IsRoot {
		t.Error("the identifiers differ: this bridge is not root")
	}
	if len(st.Ports) != 1 || st.Ports[0].Port != "GE18" {
		t.Errorf("ports=%+v — the interface table is read here too when the summary gave none", st.Ports)
	}
}

// TestPoECopperSplit pins the ONE shape the 2026-08-05 EdgePoint S16 survey
// actually observed (ports 1-4 no PSE hardware, 5-16 real PSE hardware, cf.
// poe.go) plus the boundary cases, and refuses to fabricate a split for a
// pattern that isn't a single boundary.
func TestPoECopperSplit(t *testing.T) {
	var notCapable, ok = poeCopperSplit(16, map[int]bool{
		5: true, 6: true, 7: true, 8: true, 9: true, 10: true, 11: true,
		12: true, 13: true, 14: true, 15: true, 16: true,
	})
	if !ok || notCapable != 4 {
		t.Errorf("the lab shape (1-4 none, 5-16 capable): got notCapable=%d ok=%v, want 4 true", notCapable, ok)
	}

	notCapable, ok = poeCopperSplit(16, map[int]bool{})
	if !ok || notCapable != 16 {
		t.Errorf("no capable port at all: got %d %v, want 16 true", notCapable, ok)
	}

	var allCapable = map[int]bool{}
	var n int
	for n = 1; n <= 16; n++ {
		allCapable[n] = true
	}
	notCapable, ok = poeCopperSplit(16, allCapable)
	if !ok || notCapable != 0 {
		t.Errorf("every port capable: got %d %v, want 0 true", notCapable, ok)
	}

	// A capable port BEFORE a non-capable one is not a single boundary — must
	// not be forced into a group split.
	_, ok = poeCopperSplit(16, map[int]bool{2: true, 10: false, 11: true})
	if ok {
		t.Error("an irregular capability pattern must not be reported as a clean split")
	}
}

// parseRunningConfigPorts — SYNTHETIC fixture, built from the exact grammar
// PortCmds (apply.go) writes, one VLAN per line. Kept alongside the REAL
// capture (TestParseRunningConfigPortsRealCapture below) because the dump
// coalesces those lines into list syntax the moment the config is saved —
// but the single-id form remains the degenerate case of the list form, and
// this fixture pins that it still parses.
const sampleRunningConfig = `
!Current Configuration:
!
interface 0/1
vlan pvid 1
vlan participation include 1
exit
!
interface 0/15
vlan pvid 1
vlan participation include 1
vlan participation include 42
vlan tagging 42
vlan participation include 98
vlan tagging 98
vlan participation include 99
vlan tagging 99
exit
!
interface 0/18
vlan pvid 1
vlan participation include 1
vlan participation include 42
vlan tagging 42
exit
!
interface 3/1
vlan pvid 50
exit
`

func TestParseRunningConfigPorts(t *testing.T) {
	var ports = parseRunningConfigPorts(sampleRunningConfig)

	if ports["GE1"].Mode != "access" || ports["GE1"].PVID != 1 {
		t.Errorf("GE1: expected plain access/pvid1, got %+v", ports["GE1"])
	}

	var ge15 = ports["GE15"]
	if ge15.Mode != "trunk" {
		t.Errorf("GE15: carries \"vlan tagging\" lines, must read as trunk, got %+v", ge15)
	}
	if ge15.PVID != 1 {
		t.Errorf("GE15: pvid must be 1, got %d", ge15.PVID)
	}
	if len(ge15.Tagged) != 3 || ge15.Tagged[0] != 42 || ge15.Tagged[1] != 98 || ge15.Tagged[2] != 99 {
		t.Errorf("GE15: tagged = %v, want [42 98 99] sorted", ge15.Tagged)
	}

	var ge18 = ports["GE18"]
	if ge18.Mode != "trunk" || len(ge18.Tagged) != 1 || ge18.Tagged[0] != 42 {
		t.Errorf("GE18 (SFP+ uplink): expected trunk carrying [42], got %+v", ge18)
	}

	// Slot-3 LAG interfaces are not faceplate sockets: StandardPortName
	// refuses "3/1", so nothing must appear under any GE name for it.
	var _, sawLAG = ports["GE50"]
	if sawLAG {
		t.Error("a slot-3 block must never surface as a faceplate port")
	}
}

// The GE17/GE18 "permanent drift" regression, replayed against the REAL
// capture (lab EdgePoint S16, firmware 1.9.3, 2026-08-05 — secrets redacted,
// structure verbatim). The dump COALESCES per-port membership into list
// syntax ("vlan tagging 42,98-100"), which the parser's original single-id
// grammar silently missed: both SFP+ cages — correctly configured as trunks
// on the device, the write had taken — read back as bare access forever,
// and the stale trunk tagging on 0/15-0/16 was invisible, so it compared
// EQUAL to the desired plain-access state and never got cleaned.
func TestParseRunningConfigPortsRealCapture(t *testing.T) {
	var out = mustRead(t, "testdata/show_running_config.txt")
	var ports = parseRunningConfigPorts(out)

	// 0/17 and 0/18 (the SFP+ cages): trunks tagged 42,98,99,100, no "vlan
	// pvid" line in their block — the pvid stays the platform default 1.
	var name string
	for _, name = range []string{"GE17", "GE18"} {
		var ps = ports[name]
		if ps.Mode != "trunk" {
			t.Errorf("%s: the device carries \"vlan tagging 42,98-100\", must read as trunk, got %+v", name, ps)
		}
		if ps.PVID != 1 {
			t.Errorf("%s: no pvid line in the block, want default 1, got %d", name, ps.PVID)
		}
		if !sameInts(ps.Tagged, []int{42, 98, 99, 100}) {
			t.Errorf("%s: tagged = %v, want [42 98 99 100]", name, ps.Tagged)
		}
	}

	// 0/15 and 0/16: pvid 100 plus coalesced tagging 42,98-99. Before the
	// list fix these read as plain access/pvid 100 — identical to their
	// DESIRED state, masking membership the site plan wants removed.
	for _, name = range []string{"GE15", "GE16"} {
		var ps = ports[name]
		if ps.Mode != "trunk" || ps.PVID != 100 || !sameInts(ps.Tagged, []int{42, 98, 99}) {
			t.Errorf("%s: want trunk/pvid 100/tagged [42 98 99], got %+v", name, ps)
		}
	}

	// 0/1..0/14: pvid 100, "vlan participation exclude 99" ignored, access.
	var n int
	for n = 1; n <= 14; n++ {
		var ps = ports[fmt.Sprintf("GE%d", n)]
		if ps.Mode != "access" || ps.PVID != 100 || len(ps.Tagged) != 0 {
			t.Errorf("GE%d: want access/pvid 100/no tags, got %+v", n, ps)
		}
	}

	// "interface lag N" blocks carry tagging too and must not surface as
	// faceplate ports — nor bleed their VLANs into the previous block.
	if len(ports) != 18 {
		t.Errorf("want exactly the 18 faceplate sockets, got %d: %+v", len(ports), ports)
	}
}

// expandVlanList — the dump's coalescing grammar, including what must NOT
// expand.
func TestExpandVlanList(t *testing.T) {
	var cases = []struct {
		in   string
		want []int
	}{
		{"42", []int{42}},
		{"42,98-100", []int{42, 98, 99, 100}},
		{"98-100", []int{98, 99, 100}},
		{"1,3,5", []int{1, 3, 5}},
		{"100-98", nil},            // backwards range: invented ids are worse than none
		{"0-2", nil},               // below the VLAN space
		{"42,x,99", []int{42, 99}}, // a bad piece must not poison its neighbours
	}
	var c struct {
		in   string
		want []int
	}
	for _, c = range cases {
		var got = expandVlanList(c.in)
		if !sameInts(got, c.want) {
			t.Errorf("expandVlanList(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestParseRunningConfigPortsIgnoresTextOutsideInterfaceBlocks(t *testing.T) {
	var ports = parseRunningConfigPorts("vlan database\nvlan 42\nvlan name 42 Cameras\nexit\n")
	if len(ports) != 0 {
		t.Errorf("no interface block present: expected nothing read, got %+v", ports)
	}
}

// The spanning-tree pair, replayed against the REAL 2026-08-05 captures from
// the lab EdgePoint S16 (firmware 1.9.3). What this guards: the summary
// alone carries NO bridge or root identifier on this platform, so the state
// is only whole after mergeSpanningTreeDetail — a regression in either half
// turns the topology view's STP panel back into "MSTP" and nothing else.
func TestParseSpanningTreePairFromCaptures(t *testing.T) {
	var st = parseSpanningTreeSummary(mustRead(t, "testdata/show_spanning_tree_summary.txt"))

	// What the summary alone knows.
	if !st.Enabled || st.Mode != "mstp" {
		t.Errorf("summary: enabled=%v mode=%q — want enabled mstp", st.Enabled, st.Mode)
	}
	if st.Region.Name != "9C-05-D6-F5-F1-37" || st.Region.Revision != 0 {
		t.Errorf("region=%+v — want name 9C-05-D6-F5-F1-37 revision 0", st.Region)
	}
	if len(st.Ports) != 1 || st.Ports[0].Port != "GE18" ||
		st.Ports[0].Role != "root" || st.Ports[0].State != "forwarding" {
		t.Errorf("summary ports=%+v — want GE18 root/forwarding", st.Ports)
	}
	if st.BridgeID != "" || st.RootBridgeID != "" {
		t.Errorf("the summary CANNOT know identifiers, yet bridge=%q root=%q — "+
			"if the firmware started printing them here, move the read",
			st.BridgeID, st.RootBridgeID)
	}

	// And what only the plain "show spanning-tree" adds.
	mergeSpanningTreeDetail(mustRead(t, "testdata/show_spanning_tree.txt"), &st)
	if st.BridgePriority != 32768 {
		t.Errorf("bridge priority=%d — want 32768", st.BridgePriority)
	}
	if st.BridgeID != "9c:05:d6:f5:f1:37" {
		t.Errorf("bridge id=%q — want the MAC in the product's lowercase-colon form", st.BridgeID)
	}
	if st.RootBridgeID != "d4:01:c3:ab:32:f4" {
		t.Errorf("root id=%q — want d4:01:c3:ab:32:f4 (the lab's Catalyst)", st.RootBridgeID)
	}
	if st.RootPriority != 0 {
		t.Errorf("root priority=%d — the capture says 00:00, i.e. 0", st.RootPriority)
	}
	if st.RootPathCost != 8000 {
		t.Errorf("root path cost=%d — want 8000", st.RootPathCost)
	}
	if st.IsRoot {
		t.Error("this bridge is NOT root: its identifier differs from the designated root's")
	}
}

// The SAME PAIR of reads on the SAME unit, forced through the other two
// protocol versions. Captured 2026-08-10 on the lab EdgePoint S16 (firmware
// 1.9.3) by driving the mode to rstp then stp and restoring mst afterwards.
//
// # THE COMMAND IS "spanning-tree mode", NOT "spanning-tree forceversion"
//
// The FASTPATH wording every manual gives is "spanning-tree forceversion
// 802.1w". This firmware REJECTS it — "% Invalid input detected at '^'
// marker" — and `spanning-tree ?` lists no forceversion at all. What it
// offers is "spanning-tree mode", whose own `?` answers exactly three
// words: mst, rstp, stp. Those three produced these fixtures, and a
// connector that ever writes this setting must send that form.
//
// # What the captures SETTLE
//
// [stpModeOf] mapped "802.1w" and "802.1d" on nothing but a reasonable
// guess about how a firmware nobody had forced would spell them. It guessed
// right: the summary prints "IEEE 802.1w" and "IEEE 802.1d", the detail
// prints "rstp" and "stp". No branch of that mapping is speculative on
// this platform any more — which is precisely what makes the VERBATIM
// fallback (still exercised in TestStpModeOf) the only remaining unknown.
//
// # What they also settle, and it was the open question
//
// Outside MST the detail read prints NO "MST0" header: the bridge fields
// hang directly off the top level. The summary meanwhile DROPS "MST
// Instance ID" and "No MST instances is root bridge" entirely, while
// keeping Configuration Name and Revision — so the MST region identity is
// still reported by a bridge that is not running MSTP, and a caller must not
// read its presence as "this bridge is in MST mode".
func TestParseSpanningTreePairRSTPAndSTPCaptures(t *testing.T) {
	var cases = []struct {
		what        string
		summaryFile string
		detailFile  string
		wantMode    string
	}{
		{
			what:        "forced to rstp",
			summaryFile: "testdata/show_spanning_tree_summary_rstp.txt",
			detailFile:  "testdata/show_spanning_tree_rstp.txt",
			wantMode:    "rstp",
		},
		{
			what:        "forced to stp",
			summaryFile: "testdata/show_spanning_tree_summary_stp.txt",
			detailFile:  "testdata/show_spanning_tree_stp.txt",
			wantMode:    "stp",
		},
	}
	var c struct {
		what        string
		summaryFile string
		detailFile  string
		wantMode    string
	}
	for _, c = range cases {
		var st = parseSpanningTreeSummary(mustRead(t, c.summaryFile))
		if !st.Enabled || st.Mode != c.wantMode {
			t.Errorf("%s: summary enabled=%v mode=%q — want enabled %q",
				c.what, st.Enabled, st.Mode, c.wantMode)
		}
		// The MST region is still printed by a bridge that is NOT in MST mode.
		if st.Region.Name != "9C-05-D6-F5-F1-37" || st.Region.Revision != 0 {
			t.Errorf("%s: region=%+v — the firmware keeps printing Configuration "+
				"Name/Revision outside MST, and the read must keep carrying them",
				c.what, st.Region)
		}
		// "No MST instances is root bridge" is ABSENT from these captures, so
		// the note the MSTP read produces must not appear here — a note is what
		// the operator reads, and one about MST instances on an RSTP bridge is
		// an invented fact.
		var n string
		for _, n = range st.Notes {
			if strings.Contains(n, "MST instance") {
				t.Errorf("%s: note %q — that line is not in this capture", c.what, n)
			}
		}
		if len(st.Ports) != 1 || st.Ports[0].Port != "GE18" ||
			st.Ports[0].Role != "root" || st.Ports[0].State != "forwarding" {
			t.Errorf("%s: summary ports=%+v — want GE18 root/forwarding", c.what, st.Ports)
		}
		if st.BridgeID != "" || st.RootBridgeID != "" {
			t.Errorf("%s: the summary CANNOT know identifiers, yet bridge=%q root=%q",
				c.what, st.BridgeID, st.RootBridgeID)
		}

		// The detail half, with no MST0 header to key on.
		mergeSpanningTreeDetail(mustRead(t, c.detailFile), &st)
		if st.Mode != c.wantMode {
			t.Errorf("%s: the merge must not overwrite a mode the summary already read: %q",
				c.what, st.Mode)
		}
		if st.BridgePriority != 32768 || st.BridgeID != "9c:05:d6:f5:f1:37" {
			t.Errorf("%s: bridge priority=%d id=%q — want 32768 / 9c:05:d6:f5:f1:37",
				c.what, st.BridgePriority, st.BridgeID)
		}
		if st.RootBridgeID != "d4:01:c3:ab:32:f4" {
			t.Errorf("%s: root id=%q — want d4:01:c3:ab:32:f4", c.what, st.RootBridgeID)
		}
		// 40:00, i.e. 16384 — and NOT the 0 the 2026-08-05 MSTP capture carries.
		// The root's priority moved between the two captures; reading it from the
		// identifier rather than assuming a default is what makes that visible.
		if st.RootPriority != 16384 {
			t.Errorf("%s: root priority=%d — the capture says 40:00, i.e. 16384",
				c.what, st.RootPriority)
		}
		if st.RootPathCost != 8000 {
			t.Errorf("%s: root path cost=%d — want 8000", c.what, st.RootPathCost)
		}
		if st.IsRoot {
			t.Errorf("%s: this bridge is NOT root — its identifier differs from "+
				"the designated root's", c.what)
		}
	}
}

// A bridge that IS root prints its own identifier as Designated Root; the
// merge must then say so even though the summary said "root for no MST
// instance" — the CIST and the MST instances are different questions.
func TestMergeSpanningTreeDetailSelfRoot(t *testing.T) {
	var st = driver.SpanningTreeState{Enabled: true, Mode: "mstp"}
	mergeSpanningTreeDetail(`
Spanning Tree Protocol......................... mstp
MST0
Bridge Priority................................ 4096
Bridge Identifier.............................. 10:00:9C:05:D6:F5:F1:37
Designated Root................................ 10:00:9C:05:D6:F5:F1:37
Root Path Cost................................. 0
`, &st)
	if !st.IsRoot {
		t.Error("same identifier on both lines: this bridge is root")
	}
	if st.BridgePriority != 4096 || st.RootPriority != 4096 {
		t.Errorf("priorities=%d/%d — want 4096 for both", st.BridgePriority, st.RootPriority)
	}
}

func TestSplitBridgeIdentifier(t *testing.T) {
	var cases = []struct {
		in   string
		prio int
		mac  string
		ok   bool
	}{
		{"80:00:9C:05:D6:F5:F1:37", 32768, "9c:05:d6:f5:f1:37", true},
		{"00:00:D4:01:C3:AB:32:F4", 0, "d4:01:c3:ab:32:f4", true},
		// The dotted spelling other FASTPATH firmware uses.
		{"8000.9c05.d6f5.f137", 32768, "9c:05:d6:f5:f1:37", true},
		{"", 0, "", false},
		{"9C:05:D6:F5:F1:37", 0, "", false}, // a bare MAC is not a bridge id
	}
	var c struct {
		in   string
		prio int
		mac  string
		ok   bool
	}
	for _, c = range cases {
		var prio, mac, ok = splitBridgeIdentifier(c.in)
		if prio != c.prio || mac != c.mac || ok != c.ok {
			t.Errorf("splitBridgeIdentifier(%q) = %d %q %v, want %d %q %v",
				c.in, prio, mac, ok, c.prio, c.mac, c.ok)
		}
	}
}
