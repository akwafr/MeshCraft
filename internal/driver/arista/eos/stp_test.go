package eos

import (
	"reflect"
	"strings"
	"testing"

	"meshcraft/internal/driver"
)

// EOS carries plain single-instance RSTP under its own name, so the fleet's
// vocabulary maps one-to-one — unlike the Cisco connector, which has to
// approximate it as "rapid-pvst", a per-VLAN protocol that does not
// interoperate the same way with a non-Cisco neighbour.
func TestEOSSTPModeMapsOneToOne(t *testing.T) {
	var mode, ok = eosSTPMode("rstp")
	if !ok || mode != "rstp" {
		t.Errorf("rstp -> %q,%v", mode, ok)
	}
	if mode, ok = eosSTPMode("mstp"); !ok || mode != "mstp" {
		t.Errorf("mstp -> %q,%v", mode, ok)
	}
	// Legacy 802.1D has no mode here, and pretending otherwise would write a
	// command the CLI refuses.
	if _, ok = eosSTPMode("stp"); ok {
		t.Error("\"stp\" was accepted: there is no \"spanning-tree mode stp\" on EOS")
	}
}

// The step of 4096 is the protocol's, not a preference: 802.1D encodes the
// priority in the top four bits. Rounding DOWN keeps a device at least as likely
// to be root as the operator asked.
func TestClampBridgePriorityRoundsDownToTheProtocolStep(t *testing.T) {
	if clampBridgePriority(9000) != 8192 {
		t.Errorf("9000 -> %d, want 8192", clampBridgePriority(9000))
	}
	if clampBridgePriority(99999) != 61440 {
		t.Errorf("out-of-range -> %d, want 61440", clampBridgePriority(99999))
	}
	if clampBridgePriority(-5) != 0 {
		t.Errorf("negative -> %d, want 0", clampBridgePriority(-5))
	}
}

// The region name is part of the MST DIGEST: two switches whose names differ by
// one character are in two different regions, and nothing reports that. So the
// sanitiser must be DETERMINISTIC — the same site name must produce the same
// region name on every device, for ever.
func TestSanitizeRegionNameIsDeterministicAndBounded(t *testing.T) {
	var a = sanitizeRegionName("Site Nord.2")
	var b = sanitizeRegionName("Site Nord.2")
	if a != b {
		t.Fatalf("two calls gave %q and %q — a region name that varies puts a switch "+
			"in a region of its own, silently", a, b)
	}
	if a != "Site-Nord-2" {
		t.Errorf("got %q", a)
	}
	if len(sanitizeRegionName(
		"aaaaaaaaaabbbbbbbbbbccccccccccddddddddddeeeeeeeeee")) != 32 {
		t.Error("the 32-character ceiling was not applied")
	}
	// AN UNUSABLE NAME YIELDS "", AND THE CALLER REFUSES THE PLAN (ADR-0022).
	//
	// This assertion is inverted from what it was, and the inversion is the point.
	// It used to require a fallback to the fixed string "meshcraft", on the
	// reasoning that some name is better than none. It is not: a region name is one
	// third of an 802.1s identity, so a FIXED name shared by every site that trips
	// this case is precisely the "one name for the whole instance" alternative
	// ADR-0021 examined and rejected — membership would then rest on the VLAN table
	// alone, and two sites merge into one region the day their VLAN lists coincide.
	// Reaching that state by accident, from two unrelated unsanitisable names, is
	// worse than choosing it.
	if sanitizeRegionName("!!!") != "" {
		t.Errorf("a name with nothing usable in it must come back empty so the applier "+
			"can refuse and SAY so, not be replaced by an invented name every site "+
			"would share, got %q", sanitizeRegionName("!!!"))
	}
}

// --- the three lines a switch would have refused ---------------------------
//
// All three guard the SAME failure mode, which is why they sit together: the
// region and the mode travel in ONE config block, and [Client.runConfigBlock]
// abandons a block at the first line the device refuses. So a bad region line
// does not merely fail to write the region — it takes the mode line down with
// it, and the switch is left in whatever region it already had. On a factory
// Arista that is a region named after the bridge MAC, i.e. a region of one.

// regionFakeClient is paFakeClient with no running-config and no interface
// status: an STP apply that never reaches the per-port stage needs neither.
func regionFakeClient() (*Client, *portAdminFakeCLI) { return paFakeClient("") }

// A region whose name sanitises away is REFUSED, and the refusal is visible.
//
// The failure guarded is the quiet one: an invented name is accepted by the
// switch, so the apply succeeds, the device reports a region, and it is a region
// no other device of the site is in.
func TestApplySpanningTreeRefusesUnusableRegionName(t *testing.T) {
	var c, fake = regionFakeClient()
	var out, err = c.ApplySpanningTree(t.Context(), driver.SpanningTreePlan{
		Enabled: true,
		Mode:    "mstp",
		Region:  driver.MSTRegion{Name: "!!!", Revision: 1},
	})
	if err != nil {
		t.Fatalf("a refusal must be a Skipped line, not an error: %v", err)
	}
	if len(out.Skipped) != 1 || !strings.Contains(out.Skipped[0], "NOT written") {
		t.Fatalf("expected one Skipped line naming the refusal, got %#v", out.Skipped)
	}
	// NOTHING may have been sent: writing the mode here would put the switch into
	// mstp still carrying its default region, which is worse than not writing.
	if len(fake.sent) != 0 {
		t.Errorf("a refused region must send nothing, sent %#v", fake.sent)
	}
}

// Instance 0 is the CIST and is implicit (cf. driver.MSTRegion.InstanceVLANs):
// "instance 0 vlan …" is a line EOS refuses.
func TestApplySpanningTreeNeverEmitsInstanceZero(t *testing.T) {
	var c, fake = regionFakeClient()
	var out, err = c.ApplySpanningTree(t.Context(), driver.SpanningTreePlan{
		Enabled: true,
		Mode:    "mstp",
		Region: driver.MSTRegion{Name: "mst-abc", Revision: 1, InstanceVLANs: map[int][]int{
			0: {7, 8},
			1: {42, 99},
		}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var ln string
	for _, ln = range fake.sent {
		if strings.HasPrefix(strings.TrimSpace(ln), "instance 0 ") {
			t.Fatalf("instance 0 was emitted: %q", ln)
		}
	}
	if !strings.Contains(strings.Join(fake.sent, "\n"), "instance 1 vlan 42,99") {
		t.Errorf("instance 1 was not written; sent %#v", fake.sent)
	}
	var said bool
	for _, ln = range out.Skipped {
		if strings.Contains(ln, "MST instance 0") {
			said = true
		}
	}
	if !said {
		t.Errorf("dropping instance 0 must be SAID, not silent; got %#v", out.Skipped)
	}
}

// A revision outside 16 bits is refused BEFORE anything is sent.
func TestApplySpanningTreeRefusesOutOfRangeRevision(t *testing.T) {
	var c, fake = regionFakeClient()
	var _, err = c.ApplySpanningTree(t.Context(), driver.SpanningTreePlan{
		Enabled: true,
		Mode:    "mstp",
		Region:  driver.MSTRegion{Name: "mst-abc", Revision: 70000},
	})
	if err == nil {
		t.Fatal("a revision of 70000 must be refused: 802.1s encodes it in 16 bits")
	}
	if len(fake.sent) != 0 {
		t.Errorf("the refusal must precede any write, sent %#v", fake.sent)
	}
}

// THE WHOLE TRIPLET REACHES THE WIRE, in the order the header requires (region
// before mode), from nothing but what SiteMSTRegion produces.
//
// This is the test that says "arista-eos can join a region" — the claim
// Capabilities now makes through driver.FeatMSTRegion, which is otherwise just a
// boolean somebody typed.
func TestApplySpanningTreeWritesTheRegionTripletFromSiteMSTRegion(t *testing.T) {
	var c, fake = regionFakeClient()
	// Exactly the ARENA intent: the site's own name, revision and VLAN list, with
	// VLAN 1 present to prove it is excluded rather than mapped.
	var region = driver.SiteMSTRegion("mst-7f0d238df180", 1,
		[]int{1, 10, 11, 42, 60, 98, 99, 100})
	var _, err = c.ApplySpanningTree(t.Context(), driver.SpanningTreePlan{
		Enabled: true, Mode: "mstp", Region: region,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var want = []string{
		"spanning-tree mst configuration",
		"name mst-7f0d238df180",
		"revision 1",
		// THE WRITTEN FORM IS THE EXPLICIT LIST, where the device RENDERS ranges
		// back ("10-11,42,60,98-100" in `show spanning-tree mst configuration`).
		// The two are the same table and the digest is computed over the table, not
		// over the text, so the asymmetry is harmless — but it is asserted here so
		// that nobody "fixes" the writer to emit ranges in order to match a capture.
		"instance 1 vlan 10,11,42,60,98,99,100",
		"exit",
		"spanning-tree mode mstp",
	}
	var got = strings.Join(fake.sent, "\n")
	var prev = -1
	var w string
	for _, w = range want {
		var at = indexOfLine(fake.sent, w)
		if at < 0 {
			t.Fatalf("%q never reached the device; sent:\n%s", w, got)
		}
		if at <= prev {
			t.Errorf("%q landed out of order: the region must be written BEFORE the "+
				"mode, or the switch joins a region of one and reconverges twice", w)
		}
		prev = at
	}
	if strings.Contains(got, "vlan 1,") || strings.Contains(got, ",1,") {
		t.Error("VLAN 1 belongs to the CIST and must not be mapped into an instance")
	}
}

// indexOfLine finds a sent line by exact content, ignoring surrounding space.
func indexOfLine(sent []string, want string) int {
	var i int
	for i = range sent {
		if strings.TrimSpace(sent[i]) == want {
			return i
		}
	}
	return -1
}

// EOS spells the role and state words OUT, where IOS abbreviates them. Both are
// accepted: a train that borrowed the abbreviated table would otherwise report
// every port with an empty role, which reads on screen as "spanning tree is not
// running" — the one conclusion that would be wrong.
const stpFixture = `MST0
  Spanning tree enabled protocol mstp
  Root ID    Priority    24576
             Address     2899.3a0e.0001
             Cost        500

  Bridge ID  Priority    32768  (priority 32768 sys-id-ext 0)
             Address     2899.3a0e.1f6f

Interface        Role       State      Cost      Prio.Nbr Type
---------------- ---------- ---------- --------- -------- --------------------
Et1              designated forwarding 2000      128.1    P2P
Et49             root       forwarding 500       128.49   P2P
Et50             alternate  discarding 500       128.50   P2P
`

func TestParseShowSpanningTree(t *testing.T) {
	var st = parseShowSpanningTree(stpFixture)
	if !st.Enabled || st.Mode != "mstp" {
		t.Errorf("enabled=%v mode=%q", st.Enabled, st.Mode)
	}
	if st.BridgePriority != 32768 || st.RootPriority != 24576 {
		t.Errorf("bridge=%d root=%d", st.BridgePriority, st.RootPriority)
	}
	if st.IsRoot {
		t.Error("IsRoot is true on a switch whose Root ID is another bridge")
	}
	// The FIRST Address belongs to the Root ID block, the second to the Bridge ID
	// block. Taking them in order is what keeps "who is root" and "who am I"
	// apart on a switch that is not the root.
	if st.RootBridgeID != "2899.3a0e.0001" || st.BridgeID != "2899.3a0e.1f6f" {
		t.Errorf("root=%q bridge=%q — the two Address lines were confused",
			st.RootBridgeID, st.BridgeID)
	}
	if st.RootPathCost != 500 {
		t.Errorf("root path cost = %d", st.RootPathCost)
	}
	if len(st.Ports) != 3 {
		t.Fatalf("ports = %+v", st.Ports)
	}
	if st.Ports[0].Port != "ET1" || st.Ports[0].Role != driver.STPRoleDesignated ||
		st.Ports[0].State != driver.STPStateForwarding || st.Ports[0].PathCost != 2000 {
		t.Errorf("ET1 = %+v", st.Ports[0])
	}
	if st.Ports[2].Role != driver.STPRoleAlternate ||
		st.Ports[2].State != driver.STPStateDiscarding {
		t.Errorf("ET50 = %+v", st.Ports[2])
	}
}

// A device printing several per-VLAN sections is running a per-VLAN variant
// whatever the protocol word says, and that does not interoperate with a
// single-instance RSTP neighbour the way the mode word suggests.
func TestParseShowSpanningTreeFlagsPerVLANSections(t *testing.T) {
	var text = `VLAN0001
  Spanning tree enabled protocol rstp
VLAN0099
  Spanning tree enabled protocol rstp
`
	var st = parseShowSpanningTree(text)
	if len(st.Notes) == 0 {
		t.Error("two per-VLAN sections produced no note: the operator has no way to see " +
			"that this switch is not running the single-instance protocol the mode says")
	}
}

// THE REGRESSION THAT MATTERED MOST. "show spanning-tree" prints one section
// per instance, each with its OWN Root ID, Bridge ID and "This bridge is the
// root". Scanning the whole output for those mixed instances together: a switch
// that is root of MST1 and not of MST0 came back IsRoot=true carrying MST0's
// root address, which reads on screen as "this switch is the root" when it is
// not — and root-election mistakes are exactly what an operator opens this view
// to check.
func TestBridgeFactsDoNotLeakBetweenInstances(t *testing.T) {
	var text = `MST0
  Spanning tree enabled protocol mstp
  Root ID    Priority    24576
             Address     2899.3a0e.0001
             Cost        500

  Bridge ID  Priority    32768  (priority 32768 sys-id-ext 0)
             Address     2899.3a0e.1f6f

Interface        Role       State      Cost      Prio.Nbr Type
---------------- ---------- ---------- --------- -------- ------------------
Et1              designated forwarding 2000      128.1    P2P

MST1
  Spanning tree enabled protocol mstp
  Root ID    Priority    4096
             Address     2899.3a0e.1f6f
             This bridge is the root

  Bridge ID  Priority    4096   (priority 4096 sys-id-ext 1)
             Address     2899.3a0e.1f6f
`
	var st = parseShowSpanningTree(text)

	if st.IsRoot {
		t.Error("IsRoot came back true: MST1's \"This bridge is the root\" leaked into " +
			"the CIST's facts, and the screen would claim a root election this switch lost")
	}
	if st.RootBridgeID != "2899.3a0e.0001" {
		t.Errorf("root bridge = %q, want MST0's — MST1's root leaked in", st.RootBridgeID)
	}
	if st.RootPriority != 24576 {
		t.Errorf("root priority = %d, want 24576 (MST0's)", st.RootPriority)
	}
	if st.BridgePriority != 32768 {
		t.Errorf("bridge priority = %d, want 32768 (MST0's)", st.BridgePriority)
	}
	if st.RootPathCost != 500 {
		t.Errorf("root cost = %d, want 500 (MST0's)", st.RootPathCost)
	}
	// And the operator must be TOLD that only the first instance is described,
	// rather than being left to assume the answer covers the whole switch.
	var joined = strings.Join(st.Notes, " | ")
	if !strings.Contains(joined, "FIRST") {
		t.Errorf("no note says which instance the bridge facts describe: %q", joined)
	}
	// The port table is still read whole: a port appears once per instance and
	// restricting it to the first section would drop trains that print it once.
	if len(st.Ports) != 1 || st.Ports[0].Port != "ET1" {
		t.Errorf("ports = %+v", st.Ports)
	}
}

// A port that is administratively down prints "Desg DIS 4" on an IOS-shaped
// table. The STATE alternation had no "dis", so the row matched the ROLE and
// failed the STATE — and the regex skipped the LINE ENTIRELY. The port vanished
// from the topology view instead of appearing as disabled, which is the worst of
// the three possible outcomes.
func TestDisabledPortRowIsNotDropped(t *testing.T) {
	var text = `MST0
  Spanning tree enabled protocol mstp
Interface        Role       State      Cost      Prio.Nbr Type
---------------- ---------- ---------- --------- -------- ------------------
Et1              designated forwarding 2000      128.1    P2P
Et2              disabled   disabled   2000      128.2    P2P
Et3              Desg       DIS        4         128.3    P2p
`
	var st = parseShowSpanningTree(text)
	if len(st.Ports) != 3 {
		t.Fatalf("parsed %d port rows, want 3 — a disabled port was dropped: %+v",
			len(st.Ports), st.Ports)
	}
	if st.Ports[1].Role != driver.STPRoleDisabled {
		t.Errorf("ET2 role = %q", st.Ports[1].Role)
	}
	// The shared vocabulary has no "disabled" STATE, so discarding is the
	// honest mapping — the ROLE is where "out of the tree" is reported.
	if st.Ports[1].State != driver.STPStateDiscarding {
		t.Errorf("ET2 state = %q, want discarding", st.Ports[1].State)
	}
	if st.Ports[2].Port != "ET3" || st.Ports[2].State != driver.STPStateDiscarding {
		t.Errorf("the IOS-shaped \"Desg DIS\" row = %+v", st.Ports[2])
	}
}

// "rapid-pvst" is what EOS prints for RSTP per VLAN. The shared vocabulary knew
// "rstp-pvst" and not this spelling, so the mode fell through VERBATIM and the
// device reported a mode string no UI label and no drift check matches.
func TestRapidPVSTNormalisesToRSTP(t *testing.T) {
	if driver.NormalizeSTPMode("rapid-pvst") != "rstp" {
		t.Fatalf("NormalizeSTPMode(rapid-pvst) = %q — an unrecognised mode is returned "+
			"verbatim, and no screen matches it", driver.NormalizeSTPMode("rapid-pvst"))
	}
	var st = parseShowSpanningTree("VLAN0001\n  Spanning tree enabled protocol rapid-pvst\n")
	if st.Mode != "rstp" {
		t.Errorf("mode = %q, want rstp", st.Mode)
	}
}

// THE COMMAND DEPENDS ON THE MODE, and sending the mst form everywhere meant the
// priority silently never landed outside mstp while the mode command right
// before it succeeded — a half-applied block, which is worse than a refused one.
func TestBridgePriorityCommandFollowsTheMode(t *testing.T) {
	var cmds, after, skip = eosPriorityCmds("mstp", intp(8192), false, nil)
	if len(cmds) != 1 || cmds[0] != "spanning-tree mst 0 priority 8192" {
		t.Errorf("mstp -> %q", cmds)
	}
	// THE NEGATION RIDES AN MSTP PRIORITY WRITE, and this expectation changed on
	// 2026-08-12 — it used to require an empty `after` here. The reason is that an
	// explicit priority is FALSE while "spanning-tree root super" stands: the
	// device accepts the number, keeps winning the election, and the screen shows
	// the priority that was asked for. Clearing the keyword is what makes the
	// write true, and this function cannot tell a clear from a first-ever priority
	// (the plan carries no previous state), so it always names it. On a switch
	// that never carried the keyword the line is a no-op.
	if len(after) != 1 || after[0] != "no spanning-tree root super" || skip != "" {
		t.Errorf("an mstp priority must clear the super-root keyword: after=%q skip=%q",
			after, skip)
	}
	// RSTP is the CIST with no additional instances, so the mst form is kept —
	// named as a branch rather than an accident. TODO.md carries it as the first
	// capture to take.
	cmds, _, _ = eosPriorityCmds("rstp", intp(8192), false, nil)
	if len(cmds) != 1 || cmds[0] != "spanning-tree mst 0 priority 8192" {
		t.Errorf("rstp -> %q", cmds)
	}
	// EOS spells the per-VLAN form "spanning-tree vlan-id", NOT the IOS
	// "spanning-tree vlan" — the kind of one-word difference this package
	// exists to keep straight.
	cmds, _, _ = eosPriorityCmds("rapid-pvst", intp(4096), false, nil)
	if len(cmds) != 1 || cmds[0] != "spanning-tree vlan-id 1-4094 priority 4096" {
		t.Fatalf("rapid-pvst -> %q", cmds)
	}
	if strings.Contains(cmds[0], "spanning-tree vlan ") {
		t.Error("the IOS spelling \"spanning-tree vlan\" was used")
	}
}

// SUPER ROOT IS TWO HALVES AND ONLY ONE OF THEM IS VERIFIABLE. Priority 0 is the
// half that decides the election — nothing sorts below it — and it goes in the
// main block. "spanning-tree root super" is a command this repository cannot
// confirm exists, so it MUST come back separately: runConfigBlock abandons a
// block at the first refusal, and an unknown keyword sitting next to the mode
// and the region would take both down with it. The separation is the whole
// safety property, so it is asserted rather than assumed.
func TestSuperRootForcesPriorityZeroInItsOwnBlock(t *testing.T) {
	var cmds, after, skip = eosPriorityCmds("mstp", intp(32768), true, nil)
	if len(cmds) != 1 || cmds[0] != "spanning-tree mst 0 priority 0" {
		t.Fatalf("cmds = %q — super root must override the priority it was given, "+
			"since the two cannot both be honoured", cmds)
	}
	if len(after) != 1 || after[0] != "spanning-tree root super" {
		t.Fatalf("after = %q, want the keyword alone in its own block", after)
	}
	var c string
	for _, c = range cmds {
		if strings.Contains(c, "root super") {
			t.Error("\"spanning-tree root super\" was put in the MAIN block: a refusal " +
				"there aborts the whole block and the priority never lands, which is the " +
				"exact failure the split exists to prevent")
		}
	}
	if skip != "" {
		t.Errorf("mstp refused super root: %q", skip)
	}
}

// Outside mstp there is no MST instance 0 to be root of, and "priority 0 on
// every VLAN" is a much larger change than the box the operator ticked. The
// contract's rule for a field a connector cannot carry is to SAY so, so a
// non-empty skip is the assertion — and the priority must still be written, at
// the value that was asked for and not at 0.
func TestSuperRootIsRefusedOutsideMSTPAndSaysSo(t *testing.T) {
	var cmds, after, skip = eosPriorityCmds("rstp", intp(8192), true, nil)
	if skip == "" {
		t.Error("super root under rstp was dropped silently: a ticked box that produces " +
			"nothing is indistinguishable from one that worked")
	}
	if len(after) != 0 {
		t.Errorf("the keyword was emitted outside mstp: %q", after)
	}
	if len(cmds) != 1 || cmds[0] != "spanning-tree mst 0 priority 8192" {
		t.Errorf("cmds = %q — the priority the operator asked for must survive the "+
			"refusal of the flag", cmds)
	}
	cmds, after, skip = eosPriorityCmds("rapid-pvst", intp(8192), true, nil)
	if skip == "" || len(after) != 0 {
		t.Errorf("rapid-pvst: skip=%q after=%q", skip, after)
	}
	if len(cmds) != 1 || cmds[0] != "spanning-tree vlan-id 1-4094 priority 8192" {
		t.Errorf("rapid-pvst cmds = %q", cmds)
	}
}

func TestParseMSTRegion(t *testing.T) {
	var text = `Name: Site-Nord
Revision: 3   Instances configured: 2

Instance  Vlans mapped
--------- ---------------------------------
0         1-9,21-4094
1         10-20
`
	var r, read = parseMSTRegion(text)
	if !read {
		t.Fatal("a well-formed MST configuration was not recognised as one")
	}
	if r.Name != "Site-Nord" || r.Revision != 3 {
		t.Errorf("name=%q revision=%d", r.Name, r.Revision)
	}
	// Instance 0 is the CIST and is IMPLICIT (cf. driver.MSTRegion): it holds
	// every VLAN not named elsewhere, so recording it would make two identical
	// regions compare unequal.
	var _, hasZero = r.InstanceVLANs[0]
	if hasZero {
		t.Error("instance 0 was recorded: it is implicit, and keeping it would make two " +
			"identical regions compare unequal")
	}
	if !reflect.DeepEqual(r.InstanceVLANs[1], []int{10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20}) {
		t.Errorf("instance 1 = %v", r.InstanceVLANs[1])
	}
}

// intp is the pointer form the builder takes: "no priority requested" has to be
// representable, and 0 is the one value that must not double as absent.
func intp(v int) *int { return &v }

// THE REFUSAL MUST WRITE NOTHING. The first version returned a skip line saying
// "Nothing was written for it" AND a "spanning-tree mst 0 priority 0"
// command in the same breath: the connector performed exactly the root election
// it announced it was not performing, and the transcript said the opposite. On
// the auto-apply path (which always passes a nil priority) that meant a device
// carrying stp_super_root=true, on a site resolving to rstp, had priority 0
// pushed on every pipeline run.
func TestRefusedSuperRootWritesNothing(t *testing.T) {
	var cmds, after, skipped = eosPriorityCmds("rstp", nil, true, nil)
	if len(cmds) != 0 {
		t.Errorf("commands were emitted for a REFUSED super root: %v", cmds)
	}
	if len(after) != 0 {
		t.Errorf("the keyword was emitted for a refused super root: %v", after)
	}
	if skipped == "" {
		t.Error("a refusal must be reported")
	}
	// And an explicit priority alongside a refused SuperRoot is still honoured:
	// the operator asked for that number on its own merits.
	cmds, _, skipped = eosPriorityCmds("rstp", intp(8192), true, nil)
	if len(cmds) != 1 || cmds[0] != "spanning-tree mst 0 priority 8192" {
		t.Errorf("an explicit priority must survive the refusal: %v", cmds)
	}
	if skipped == "" {
		t.Error("the super-root refusal must still be reported")
	}
	// Nothing requested at all: nothing written, nothing claimed.
	cmds, after, skipped = eosPriorityCmds("mstp", nil, false, nil)
	if len(cmds) != 0 || len(after) != 0 || skipped != "" {
		t.Errorf("an empty intent produced %v / %v / %q", cmds, after, skipped)
	}
}

// PER-INSTANCE ROOT: one priority line per MST instance the plan names.
//
// Instance 0 alone used to be the only possibility, because the intent was a
// boolean and a boolean has nowhere to put an instance id. That made root
// designation meaningless on exactly the topologies MST exists for: a region
// with three instances has three trees and three roots, and writing the priority
// on the CIST alone leaves the other two elected by MAC address — i.e. by
// whichever switch happens to have the lower one.
func TestPriorityIsWrittenPerMSTInstance(t *testing.T) {
	var cmds, _, _ = eosPriorityCmds("mstp", nil, true, []int{0, 10, 20})
	if len(cmds) != 3 {
		t.Fatalf("%d priority commands for three instances: %v", len(cmds), cmds)
	}
	var want = []string{
		"spanning-tree mst 0 priority 0",
		"spanning-tree mst 10 priority 0",
		"spanning-tree mst 20 priority 0",
	}
	var i int
	for i = range want {
		if cmds[i] != want[i] {
			t.Errorf("command %d = %q, want %q", i, cmds[i], want[i])
		}
	}
}

// No instances named keeps the old behaviour exactly: the CIST alone. Anything
// already setting the boolean must not change what it writes.
func TestNoInstancesMeansTheCISTAlone(t *testing.T) {
	var cmds, _, _ = eosPriorityCmds("mstp", nil, true, nil)
	if len(cmds) != 1 || cmds[0] != "spanning-tree mst 0 priority 0" {
		t.Errorf("cmds = %v, want the CIST alone", cmds)
	}
}

// THE SHAPE EOS ACTUALLY PRINTS, captured on a DCS-7050SX-64-R running
// 4.28.13.1M. The fixture above it was written from another platform's
// punctuation — "Name: value" — and no Arista prints that: there is no colon,
// and the value is bracketed. The region name therefore never parsed on any EOS
// switch in the fleet.
//
// The failure was worse than a blank field. An empty name made the caller
// announce that the region "could not be read", when it had been read exactly
// and the switch simply has no region name — the opposite conclusion, on the one
// platform where a region mismatch is invisible by design.
func TestParseMSTRegionEOSSpelling(t *testing.T) {
	var text = `Name      []
Revision  0    Instances configured 1

Instance  Vlans mapped
-------- ------------------------------------------------------------------
0         1-4094
--------------------------------------------------------------------------
`
	var r, read = parseMSTRegion(text)
	if !read {
		t.Fatal("the captured EOS output was not recognised as an MST configuration: " +
			"the caller cannot then tell \"unnamed\" from \"unreadable\"")
	}
	if r.Name != "" {
		t.Errorf("name = %q, want empty: \"[]\" is how EOS renders no region name", r.Name)
	}
	if r.Revision != 0 {
		t.Errorf("revision = %d, want 0", r.Revision)
	}
	// Unnamed is NOT configured — every unnamed switch is its own region.
	if r.Configured() {
		t.Error("an unnamed region reported itself as configured: applying it would " +
			"claim this switch shares a region with its neighbours when it shares none")
	}
	if len(r.InstanceVLANs) != 0 {
		t.Errorf("instance map = %v, want empty: instance 0 is the implicit CIST",
			r.InstanceVLANs)
	}
}

// A VLAN list that OVERFLOWS ONTO A SECOND LINE, on a CONSTRUCTED fixture.
//
// Until 2026-08-10 this test asserted the opposite of what it asserts now: that
// the continuation was LOST, which was the documented gap at [reMSTInst], and
// that it was at least not mis-read as a row of its own. Only the second half
// still stands on its own — a line "3000-3010," or "50-60" picked up as
// instance 3000 or instance 50 invents a mapping the switch does not have, and
// the region digest would then disagree with the site's intent for a reason
// nobody could find in the output. Losing the tail is visible; inventing an
// instance is not.
//
// The tail is no longer lost. The capture that settled the shape is
// testdata/show_spanning_tree_mst_configuration_wrapped.txt and the test that
// decides is TestParseMSTRegionOnWrappedCapture; this one is KEPT because it
// covers what the hardware does not print — a trailing comma at the break, and
// a list wrapped after only two items — and because it holds the merge to the
// rule at [reMSTInstCont] rather than to the one switch it came from.
func TestParseMSTRegionDoesNotInventInstancesFromWrappedLines(t *testing.T) {
	var text = `Name      Site-Nord
Revision  3    Instances configured 2

Instance  Vlans mapped
-------- ------------------------------------------------------------------
0         1-9,31-4094
1         10-20, 21-25,
          26-30
--------------------------------------------------------------------------
`
	var r, read = parseMSTRegion(text)
	if !read {
		t.Fatal("not recognised as an MST configuration")
	}
	// The numbered line keeps everything it carried, spaces included — that is
	// what the widened character class exists for — AND the continuation is
	// merged onto it. The trailing comma at the break is absorbed rather than
	// producing an empty token the range parser would reject.
	if !reflect.DeepEqual(r.InstanceVLANs[1],
		[]int{10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30}) {
		t.Errorf("instance 1 = %v — the numbered line and its continuation are ONE "+
			"instance; a partial map makes the region digest report a mismatch that "+
			"does not exist", r.InstanceVLANs[1])
	}
	// The continuation must never become instances 26 or 3000.
	if len(r.InstanceVLANs) != 1 {
		t.Errorf("instance map = %v — a continuation line was read as an instance of "+
			"its own, which invents a VLAN mapping the switch does not carry",
			r.InstanceVLANs)
	}
	// And the separator rules of the table must not become instances either.
	var _, dashes = r.InstanceVLANs[0]
	if dashes {
		t.Error("instance 0 recorded: it is the implicit CIST")
	}
}

// Output that is NOT an MST configuration must report so, rather than coming
// back as a nameless region — which reads identically and means the opposite.
func TestParseMSTRegionRejectsNonMSTOutput(t *testing.T) {
	var cases = []string{"", "% Invalid input", "core01-lab#"}
	var c string
	for _, c = range cases {
		var _, read = parseMSTRegion(c)
		if read {
			t.Errorf("%q was accepted as an MST configuration", c)
		}
	}
}
