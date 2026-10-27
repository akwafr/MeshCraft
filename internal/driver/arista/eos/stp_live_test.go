package eos

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"meshcraft/internal/driver"
)

// "show spanning-tree" EXACTLY as a DCS-7050SX-64-R running 4.28.13.1M
// printed it, kept verbatim including the trailing spaces on the port rows.
//
// The bridge-level half of this parser was written from documentation. This
// fixture is the first time a real Arista's output has been asserted against
// it, and it pins the two facts a wrong parse would invert: that this switch is
// NOT the root, and that the root is somebody else.
const eosShowSTPCapture = `MST0
  Spanning tree enabled protocol mstp
  Root ID    Priority    16384
             Address     d401.c3ab.32f4
             Cost        6000 (Ext) 0 (Int)
             Port        48 (Ethernet48)
             Hello Time  2.000 sec  Max Age 20 sec  Forward Delay 15 sec

  Bridge ID  Priority    32768  (priority 32768 sys-id-ext 0)
             Address     2899.3a9d.b765
             Hello Time  2.000 sec  Max Age 20 sec  Forward Delay 15 sec

Interface        Role       State      Cost      Prio.Nbr Type
---------------- ---------- ---------- --------- -------- --------------------
Et34             designated forwarding 2000      128.34   P2p Boundary                  
Et35             designated forwarding 2000      128.35   P2p Boundary                  
Et37             designated forwarding 2000      128.37   P2p Boundary                  
Et48             root       forwarding 2000      128.48   P2p Boundary                  
`

func TestParseShowSpanningTreeFromHardware(t *testing.T) {
	var st = parseShowSpanningTree(eosShowSTPCapture)

	if !st.Enabled || st.Mode != "mstp" {
		t.Fatalf("enabled=%v mode=%q", st.Enabled, st.Mode)
	}
	// THE TWO PRIORITIES MUST NOT BE SWAPPED. They differ here (16384 root,
	// 32768 local) precisely so that a parser reading the wrong block fails this
	// test instead of passing on a switch where they happen to be equal.
	if st.RootPriority != 16384 {
		t.Errorf("root priority = %d, want 16384", st.RootPriority)
	}
	if st.BridgePriority != 32768 {
		t.Errorf("bridge priority = %d, want 32768", st.BridgePriority)
	}
	// "This bridge is the root" is ABSENT from this capture, so the answer is
	// no. Reporting yes would put a root crown on a switch that is one hop below
	// the real root — and the root-bridge feature would then read the fleet as
	// already having the root it was asked to create.
	if st.IsRoot {
		t.Error("IsRoot on a switch whose output does not claim it")
	}
	if st.RootBridgeID != "d401.c3ab.32f4" {
		t.Errorf("root bridge = %q", st.RootBridgeID)
	}
	if st.BridgeID != "2899.3a9d.b765" {
		t.Errorf("local bridge = %q — the second Address line is this switch", st.BridgeID)
	}
	// Four ports, and the one that is not "designated" is the uplink.
	if len(st.Ports) != 4 {
		t.Fatalf("%d port rows parsed, want 4: %+v", len(st.Ports), st.Ports)
	}
	// Names are CANONICALISED on the way in (StandardPortName), and this driver's
	// standard form is "ET<n>" (StdPrefix) — NOT the device's own "Ethernet48",
	// which cliPortName reconstructs when a command has to go back out on the wire.
	// Asserting the canonical form is the point: the topology view joins these rows
	// to ports discovered by gNMI and LLDP, which pass through the same function,
	// and a parser that passed the device spelling straight through would produce
	// rows matching nothing and a spanning-tree table that is silently half empty.
	var byName = map[string]driver.SpanningTreePort{}
	var p int
	for p = range st.Ports {
		byName[st.Ports[p].Port] = st.Ports[p]
	}
	var uplink, ok = byName["ET48"]
	if !ok {
		t.Fatalf("ET48 absent; parsed ports = %v", keysOf(byName))
	}
	if uplink.Role != driver.STPRoleRoot {
		t.Errorf("ET48 role = %q, want root — it is the uplink toward the root",
			uplink.Role)
	}
	if uplink.State != driver.STPStateForwarding {
		t.Errorf("ET48 state = %q", uplink.State)
	}
	if byName["ET34"].Role != driver.STPRoleDesignated {
		t.Errorf("ET34 role = %q", byName["ET34"].Role)
	}
	if uplink.PathCost != 2000 {
		t.Errorf("ET48 cost = %d, want 2000", uplink.PathCost)
	}
}

func keysOf(m map[string]driver.SpanningTreePort) []string {
	var out []string
	var k string
	for k = range m {
		out = append(out, k)
	}
	return out
}

// # THE TWO CAPTURES TODO.md WAS STILL OWED, TAKEN 2026-08-10
//
// core01.lab, DCS-7050SX-64-R, EOS 4.28.13.1M, reached over the controller's
// SOCKS route. CAPTURED, NOT CONSTRUCTED — the bytes in testdata/ are the
// device's own, harness banner and echoed command line stripped and nothing else
// touched. They are FILES rather than Go string constants for one reason that
// matters: the switch terminates its lines with CRLF, a Go raw literal cannot
// carry a `\r` without spelling it out, and every fixture this connector had
// until now was therefore LF-only — i.e. none of them had ever proven the
// patterns survive the line endings the wire actually delivers. Nothing in
// ssh.go strips the CR.
//
//   - testdata/show_spanning_tree_mst_multi.txt — "show spanning-tree" with
//     THREE MST instances. core01 normally has one, so the two extra instances
//     were mapped at the CLI for the duration of the capture and reverted after
//     it (the switch is back to `Instances configured 1`, `0  1-4094`,
//     `Name []`, revision 0).
//   - testdata/show_spanning_tree_mst_configuration_wrapped.txt — the region
//     table with instance VLAN lists long enough to OVERFLOW THE COLUMN and
//     continue on unnumbered, ~10-space-indented lines.
//
// Both exist to test one claim each that had never met hardware: that bridge
// facts do not leak between MST instances (fixed by inspection on 2026-08-07,
// with nothing to check it against), and that [reMSTInst] does the right thing
// with a genuinely wrapped list.
func loadSTPCapture(t *testing.T, name string) string {
	t.Helper()
	var b, err = os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	if !strings.Contains(string(b), "\r\n") {
		t.Fatalf("%s has lost its CRLF line endings — it is no longer the capture, "+
			"and the half of these tests that is about line endings is now vacuous", name)
	}
	return string(b)
}

// A WRAPPED VLAN LIST, ON THE HARDWARE, AND THE PARSER LOSES MOST OF IT.
//
// [reMSTInst]'s header states the gap as a known one and says it is left
// unhandled on purpose until a capture arrives, because "every one of those is a
// property of output nobody in this repository has seen". This is that capture,
// and it settles the shape: a continuation carries NO instance number and is
// indented about ten spaces under the column the list started in.
//
// The half that was right is right: not one continuation line is read as an
// instance of its own, so no VLAN mapping is invented. The half that was
// admittedly wrong is worse on real output than the constructed fixture
// suggested — instance 1 here wraps over SIX lines, so 83 of its 100 VLANs were
// dropped and the region digest computed from that map would disagree with a
// site intent that matches the switch exactly.
//
// This test asserts the WHOLE list. It is the fixture that decides, not the
// parser: if the merge rule is ever loosened, this fails.
func TestParseMSTRegionOnWrappedCapture(t *testing.T) {
	var text = loadSTPCapture(t, "show_spanning_tree_mst_configuration_wrapped.txt")
	var r, read = parseMSTRegion(text)
	if !read {
		t.Fatal("the captured region table was not recognised as an MST configuration")
	}
	if r.Name != "" {
		t.Errorf("name = %q, want empty: \"Name []\" is how EOS renders no region name", r.Name)
	}
	if r.Revision != 0 {
		t.Errorf("revision = %d, want 0", r.Revision)
	}

	// Instance 1 holds every EVEN VLAN from 100 to 298 — a hundred of them, laid
	// out over one numbered line and five continuations.
	var want1 []int
	var v int
	for v = 100; v <= 298; v += 2 {
		want1 = append(want1, v)
	}
	if !reflect.DeepEqual(r.InstanceVLANs[1], want1) {
		t.Errorf("instance 1 has %d VLANs, want %d — a wrapped list must be merged into "+
			"the instance whose row it continues, or the region digest reports a mismatch "+
			"that does not exist.\n got %v",
			len(r.InstanceVLANs[1]), len(want1), r.InstanceVLANs[1])
	}

	// Instance 2 fits on its numbered line and must be unaffected by the merging.
	var want2 []int
	for v = 3000; v <= 3100; v++ {
		want2 = append(want2, v)
	}
	if !reflect.DeepEqual(r.InstanceVLANs[2], want2) {
		t.Errorf("instance 2 has %d VLANs, want %d: %v",
			len(r.InstanceVLANs[2]), len(want2), r.InstanceVLANs[2])
	}

	// NO INVENTED INSTANCES. This is the dangerous failure and the one the
	// constructed fixture already covered; here it is on the real shape, where
	// continuations begin "133,135,…" and "3101-4094" and "270,272,…".
	// Two instances, 1 and 2 — instance 0 is the implicit CIST.
	if len(r.InstanceVLANs) != 2 {
		t.Fatalf("instance map has %d entries, want 2 (1 and 2): a continuation line "+
			"became an instance, which invents a mapping the switch does not carry: %v",
			len(r.InstanceVLANs), keysOfIntMap(r.InstanceVLANs))
	}
	var _, hasZero = r.InstanceVLANs[0]
	if hasZero {
		t.Error("instance 0 recorded: it is the implicit CIST")
	}
	// The specific numbers a naive line-merge would conjure, named so a failure
	// says which rule broke.
	var ghost int
	for _, ghost = range []int{133, 134, 168, 202, 270, 3101} {
		var _, bad = r.InstanceVLANs[ghost]
		if bad {
			t.Errorf("instance %d exists: that is a continuation line read as a row", ghost)
		}
	}
	// And instance 1 must not have swallowed instance 2's or the CIST's ranges.
	var vl int
	for _, vl = range r.InstanceVLANs[1] {
		if vl >= 3000 || vl%2 == 1 {
			t.Fatalf("instance 1 carries VLAN %d, which belongs to another instance: "+
				"the merge crossed a row boundary", vl)
		}
	}
}

// THREE MST INSTANCES THAT DISAGREE, WHICH IS THE WHOLE POINT.
//
// parseShowSpanningTree takes its bridge facts from the FIRST section only, and
// that restriction was written on 2026-08-07 by inspection, with no
// multi-instance output in the building. This capture is the hardware, and it
// was chosen so a leak cannot pass unnoticed: MST0's root is ANOTHER bridge
// (d401.c3ab.32f4, reached through Et48) while MST1 and MST2 both say "This
// bridge is the root", and all three priorities differ.
//
// A parser scanning the whole text would come back IsRoot=true — a root crown on
// a switch that is one hop below the real root, on the instance every VLAN rides
// when the map is not load-balanced.
func TestParseShowSpanningTreeMultiInstanceDoesNotLeak(t *testing.T) {
	var text = loadSTPCapture(t, "show_spanning_tree_mst_multi.txt")
	var st = parseShowSpanningTree(text)

	if !st.Enabled || st.Mode != "mstp" {
		t.Fatalf("enabled=%v mode=%q", st.Enabled, st.Mode)
	}
	// MST0's numbers, and NOT MST1's (32769) or MST2's (32770).
	if st.RootPriority != 16384 {
		t.Errorf("root priority = %d, want MST0's 16384 — %d would be MST1's Root ID "+
			"leaking into the CIST answer", st.RootPriority, st.RootPriority)
	}
	if st.BridgePriority != 32768 {
		t.Errorf("bridge priority = %d, want MST0's 32768", st.BridgePriority)
	}
	if st.IsRoot {
		t.Error("IsRoot: MST0's section does not claim it — \"This bridge is the root\" " +
			"belongs to MST1 and MST2, and folding it in puts a root crown on a switch " +
			"that sits one hop below the real root of the CIST")
	}
	if st.RootBridgeID != "d401.c3ab.32f4" {
		t.Errorf("root bridge = %q, want d401.c3ab.32f4 — MST1's root is this switch "+
			"itself and must not appear here", st.RootBridgeID)
	}
	if st.BridgeID != "2899.3a9d.b765" {
		t.Errorf("local bridge = %q", st.BridgeID)
	}
	// MST0 HAS A ROOT PORT and therefore a root path cost; the "(Ext) / (Int)"
	// parenthetical must not be mistaken for the number.
	if st.RootPathCost != 6000 {
		t.Errorf("root path cost = %d, want 6000", st.RootPathCost)
	}
	// The other two instances are NAMED, not folded in.
	var note string
	var named bool
	for _, note = range st.Notes {
		if strings.Contains(note, "3 spanning-tree instances") {
			named = true
		}
	}
	if !named {
		t.Errorf("no note says the switch prints three instances; notes = %v", st.Notes)
	}

	// THE ROOT-PORT DISTINCTION, per instance. Reading MST1's section on its own
	// proves the fixture genuinely disagrees with MST0 and that the head-slicing
	// above is what keeps them apart — not luck about which line comes first.
	var at = strings.Index(text, "MST1")
	if at < 0 {
		t.Fatal("the capture no longer contains an MST1 section")
	}
	var one = parseShowSpanningTree(text[at:])
	if !one.IsRoot {
		t.Error("MST1 read alone is not the root, but its section says \"This bridge is " +
			"the root\"")
	}
	// 32769 is Root ID / Bridge ID; 32768 is the sys-id-ext BASE in the
	// parenthetical. Reading the parenthetical would report the same priority for
	// all three instances, which is exactly the leak in another dress.
	if one.BridgePriority != 32769 || one.RootPriority != 32769 {
		t.Errorf("MST1 priorities = bridge %d / root %d, want 32769 each: the number in "+
			"\"(priority 32768 sys-id-ext 1)\" is the base, not the instance's priority",
			one.BridgePriority, one.RootPriority)
	}
	if one.RootPathCost != 0 {
		t.Errorf("MST1 root path cost = %d: a bridge that IS the root has no cost to "+
			"itself, and 6000 there would be MST0's Cost line leaking backwards",
			one.RootPathCost)
	}

	// PORTS come from the whole output on purpose, first occurrence winning, so
	// four ports and not eight — Et34/35/37/48 each appear in MST0 and again in
	// MST1. Et48 keeps the CIST's "root".
	if len(st.Ports) != 4 {
		t.Fatalf("%d port rows, want 4: a port must not be listed once per instance "+
			"it participates in: %+v", len(st.Ports), st.Ports)
	}
	var byName = map[string]driver.SpanningTreePort{}
	var p int
	for p = range st.Ports {
		byName[st.Ports[p].Port] = st.Ports[p]
	}
	if byName["ET48"].Role != driver.STPRoleRoot {
		t.Errorf("ET48 role = %q, want root: MST0's row is the CIST's and comes first; "+
			"MST1 calls the same port \"master\"", byName["ET48"].Role)
	}
	if byName["ET34"].Role != driver.STPRoleDesignated {
		t.Errorf("ET34 role = %q", byName["ET34"].Role)
	}
}

// "MASTER" IS A REAL ROLE WORD ON THIS RANGE, and the connector already knows
// it — this pins the evidence rather than reporting a defect.
//
// It appears on Et48 in MST1: the port toward the CIST root, seen from a
// non-CIST instance. 802.1s calls that role Master, and EOS spells it out.
// [reSTPPortRow] carries it in the role alternation and [eosSTPRole] maps it to
// driver.STPRoleMaster, both written from documentation on 2026-08-07; this is
// the first output containing the word.
//
// The stake is not the label. A role the row pattern does not recognise fails
// the alternation and the WHOLE LINE is skipped — the port disappears from the
// topology view rather than appearing with a blank role. That is the failure
// mode the "disabled" rows already hit once (cf. [reSTPPortRow]). Here it
// would only bite where the master row comes FIRST, which is any read of a
// switch whose CIST section does not list the port; the dedup in
// parseShowSpanningTree hides it on this particular capture.
func TestMasterRoleWordFromHardware(t *testing.T) {
	var text = loadSTPCapture(t, "show_spanning_tree_mst_multi.txt")
	if !strings.Contains(text, "master") {
		t.Fatal("the capture no longer contains the \"master\" role word")
	}
	// The row pattern must MATCH the line, or the port is dropped whole.
	var rows = reSTPPortRow.FindAllStringSubmatch(text, -1)
	if len(rows) != 8 {
		t.Fatalf("%d port rows matched, want 8 (four ports in MST0, four in MST1; MST2's "+
			"table is empty): a role word the pattern does not know drops its line "+
			"entirely rather than degrading it", len(rows))
	}
	var found bool
	var i int
	for i = range rows {
		if rows[i][2] == "master" {
			found = true
			if rows[i][1] != "Et48" {
				t.Errorf("the master row is on %q, want Et48", rows[i][1])
			}
			if eosSTPRole(rows[i][2]) != driver.STPRoleMaster {
				t.Errorf("eosSTPRole(%q) = %q, want %q — an unmapped role reads on screen "+
					"as \"spanning tree is not running\"",
					rows[i][2], eosSTPRole(rows[i][2]), driver.STPRoleMaster)
			}
		}
	}
	if !found {
		t.Error("no row matched with role \"master\": reSTPPortRow skipped the line, and " +
			"a skipped line is a port missing from the topology view")
	}
	// And the same word arriving FIRST must still produce a master port — the
	// case the dedup hides above.
	var at = strings.Index(text, "MST1")
	var one = parseShowSpanningTree(text[at:])
	var p int
	var seen bool
	for p = range one.Ports {
		if one.Ports[p].Port == "ET48" {
			seen = true
			if one.Ports[p].Role != driver.STPRoleMaster {
				t.Errorf("ET48 role in MST1 = %q, want master", one.Ports[p].Role)
			}
		}
	}
	if !seen {
		t.Errorf("ET48 absent from MST1's ports: %+v", one.Ports)
	}
}

func keysOfIntMap(m map[int][]int) []int {
	var out []int
	var k int
	for k = range m {
		out = append(out, k)
	}
	return out
}
