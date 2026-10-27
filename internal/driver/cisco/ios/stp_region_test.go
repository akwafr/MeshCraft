package ios

// The MST region on IOS, and the read-compare that rides on it.
//
// EVERY FIXTURE HERE IS A FILE, not a Go literal, and that is not tidiness. The
// captures come off the wire as CRLF and nothing between `cliConn.run` and the
// parsers strips the CR — an LF-only literal proves a pattern against line
// endings the device never sends. That lesson was paid for on arista/eos; these
// two files are the CiscoLabFirst1 bytes, CRs included.

import (
	"os"
	"strings"
	"testing"

	"meshcraft/internal/driver"
)

func readFixture(t *testing.T, name string) string {
	t.Helper()
	var b, err = os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("fixture %s: %v", name, err)
	}
	if !strings.Contains(string(b), "\r\n") {
		t.Fatalf("fixture %s has no CRLF: it is not what the wire delivers", name)
	}
	return string(b)
}

// THE COMMAND THAT ANSWERS. `show current` was sent from privileged EXEC for as
// long as this connector has existed and IOS does not have it there — measured on
// CiscoLabFirst1 (15.2(6)E, 2026-08-11), "% Invalid input detected at '^' marker".
// This pins the replacement so the wrong one cannot come back.
func TestRegionIsReadWithTheCommandIOSActuallyHas(t *testing.T) {
	if mstRegionCmd != "show spanning-tree mst configuration" {
		t.Fatalf("mstRegionCmd = %q", mstRegionCmd)
	}
	if strings.Contains(mstRegionCmd, "show current") {
		t.Fatal("\"show current\" is valid only inside the mst configuration sub-mode")
	}
}

// The EXEC shape, replayed from the live capture. The old patterns could read
// NEITHER field of it: the name is bracketed and the revision is followed by
// "Instances configured N" on the same line.
func TestParseMSTRegionFromTheExecCapture(t *testing.T) {
	var r = parseMSTRegion(readFixture(t, "show_spanning_tree_mst_configuration.txt"))
	if r.Name != "site-lab2" {
		t.Errorf("name = %q, want site-lab2 (the brackets are the CLI's, not the name's)", r.Name)
	}
	if r.Revision != 1 {
		t.Errorf("revision = %d, want 1", r.Revision)
	}
	if !r.Configured() {
		t.Error("the live region did not count as configured")
	}
	// Instance 0 is the CIST and is implicit in the contract's map: the device
	// prints it, the model must not carry it.
	var _, hasZero = r.InstanceVLANs[0]
	if hasZero {
		t.Error("instance 0 leaked into InstanceVLANs; the CIST is implicit")
	}
	var want = []int{42, 98, 99, 100}
	if !sameIntSet(r.InstanceVLANs[1], want) {
		t.Errorf("instance 1 = %v, want %v", r.InstanceVLANs[1], want)
	}
}

// The CONFIGURATION shape still reads, because it is what this connector WRITES
// and what a running-config capture carries.
func TestParseMSTRegionStillReadsTheConfigurationShape(t *testing.T) {
	var r = parseMSTRegion("\nname site-lab2\nrevision 1\ninstance 1 vlan 42, 98-100\n")
	if r.Name != "site-lab2" || r.Revision != 1 {
		t.Errorf("region = %+v", r)
	}
}

// A VLAN list that WRAPS must not lose its tail. This is the arista/eos defect
// held against the IOS parser: there, 83 of 100 VLANs were dropped silently
// because only the numbered line was kept.
func TestAWrappedInstanceRowKeepsEveryVLAN(t *testing.T) {
	// Instance 0 is dropped on the way in (the CIST is implicit), so the wrap is
	// exercised on instance 2 where the result can be seen at all.
	var text = "Instance  Vlans mapped\r\n" +
		"--------  ------------------------------------------------\r\n" +
		"2         1-41,43-97\r\n" +
		"          101-200\r\n" +
		"1         42,98-100\r\n" +
		"------------------------------------------------\r\n"
	var m = parseMSTInstanceRows(text)
	var got = m[2]
	if len(got) != 41-1+1+(97-43+1)+(200-101+1) {
		t.Fatalf("instance 2 kept %d VLANs; the continuation line was dropped", len(got))
	}
	var has = map[int]bool{}
	var v int
	for _, v = range got {
		has[v] = true
	}
	if !has[150] {
		t.Error("VLAN 150 from the continuation line is missing")
	}
	if has[42] {
		t.Error("VLAN 42 belongs to instance 1 and leaked into 2")
	}
	// The dashed rule must never be read as VLANs: it contains only characters a
	// VLAN list may also contain, which is why a continuation must start with a
	// DIGIT.
	if has[0] {
		t.Error("the table's dashed rule was parsed as a VLAN")
	}
}

// A region comparison is 802.1s's comparison: name, revision AND the map. Two
// regions agreeing on the first two and disagreeing on one VLAN are two
// different regions, and each device is alone in its own.
func TestRegionMatchesComparesTheWholeDigestInput(t *testing.T) {
	var base = driver.MSTRegion{
		Name: "site-lab2", Revision: 1,
		InstanceVLANs: map[int][]int{1: {42, 98, 99, 100}},
	}
	var same = driver.MSTRegion{
		Name: "site-lab2", Revision: 1,
		InstanceVLANs: map[int][]int{1: {100, 42, 99, 98}}, // order is not a fact
	}
	if !regionMatches(base, same) {
		t.Error("the same region compared unequal because the VLANs arrived in another order")
	}
	var cases = map[string]driver.MSTRegion{
		"a different name": {Name: "other", Revision: 1,
			InstanceVLANs: map[int][]int{1: {42, 98, 99, 100}}},
		"a different revision": {Name: "site-lab2", Revision: 2,
			InstanceVLANs: map[int][]int{1: {42, 98, 99, 100}}},
		"one VLAN moved": {Name: "site-lab2", Revision: 1,
			InstanceVLANs: map[int][]int{1: {42, 98, 99}}},
		"an extra instance the plan never named": {Name: "site-lab2", Revision: 1,
			InstanceVLANs: map[int][]int{1: {42, 98, 99, 100}, 2: {200}}},
		"no map at all": {Name: "site-lab2", Revision: 1},
	}
	var why string
	var r driver.MSTRegion
	for why, r = range cases {
		if regionMatches(r, base) {
			t.Errorf("regionMatches said equal despite %s", why)
		}
	}
}

// The configured priority is the PARENTHETICAL, not the headline: IOS adds the
// sys-id-ext into the number it prints first, so comparing the headline would
// report a mismatch on a device that matches.
func TestConfiguredPriorityComesFromTheParenthetical(t *testing.T) {
	var raw = readFixture(t, "show_spanning_tree_mst.txt")
	var got, ok = iosConfiguredPriority(raw)
	if !ok {
		t.Fatal("no parenthetical found in the live capture")
	}
	if got != 32768 {
		t.Errorf("configured priority = %d, want 32768", got)
	}
	// The headline of MST1 in the same capture is 32769. If that were what was
	// read, a plan asking for 32768 would rewrite a device that already matches.
	var st = parseShowSpanningTree(raw)
	if st.BridgePriority == got {
		t.Log("note: on MST0 the headline and the configured value coincide")
	}
	// No parenthetical: the answer must be "cannot tell", never a guess.
	_, ok = iosConfiguredPriority("Bridge ID  Priority    32769\r\n")
	if ok {
		t.Error("a priority was reported from a line carrying no parenthetical")
	}
}

// The live MST capture must still parse as MST, with the region left to the
// second command.
func TestLiveMSTCaptureReadsAsMSTP(t *testing.T) {
	var st = parseShowSpanningTree(readFixture(t, "show_spanning_tree_mst.txt"))
	if st.Mode != "mstp" {
		t.Errorf("mode = %q, want mstp", st.Mode)
	}
	if !st.Enabled {
		t.Error("spanning tree read as disabled on a switch running two MST instances")
	}
	if st.BridgeID != "bcc4.93e5.7f80" {
		t.Errorf("bridge id = %q", st.BridgeID)
	}
	// The bridge facts come from the FIRST section (MST0), where this switch is
	// NOT the root — MST1, where it is, must not leak in.
	if st.IsRoot {
		t.Error("IsRoot leaked from MST1 into the MST0 facts")
	}
}

// --- The read-compare no-op path -------------------------------------------
//
// THE DANGEROUS OUTCOME OF THIS FUNCTION IS A WRONG TRUE, and a wrong true is
// invisible on hardware: nothing is sent, the apply reports success, and the
// Catalyst keeps whatever mode it had. So the table below is mostly the ways of
// being UNSURE, and every one of them must come back false.

func mstPlan(prio *int) driver.SpanningTreePlan {
	return driver.SpanningTreePlan{
		Enabled: true, Mode: "mstp", BridgePriority: prio,
		Region: driver.MSTRegion{
			Name: "site-lab2", Revision: 1,
			InstanceVLANs: map[int][]int{1: {42, 98, 99, 100}},
		},
	}
}

func liveState(t *testing.T) (driver.SpanningTreeState, string) {
	t.Helper()
	var raw = readFixture(t, "show_spanning_tree_mst.txt")
	var st = parseShowSpanningTree(raw)
	st.Region = parseMSTRegion(readFixture(t, "show_spanning_tree_mst_configuration.txt"))
	return st, raw
}

// The device this was captured from is already exactly where the plan wants it.
func TestConvergedDeviceIsLeftAlone(t *testing.T) {
	var st, raw = liveState(t)
	var p = 32768
	var ok, why = spanningTreeMatchesPlan(st, raw, mstPlan(&p), "mst")
	if !ok {
		t.Fatalf("a device already carrying the plan was going to be rewritten: %q", why)
	}
	if !strings.Contains(why, "nothing sent") {
		t.Errorf("the note does not say what was skipped: %q", why)
	}
}

func TestEveryDoubtWritesRatherThanSkips(t *testing.T) {
	var live, raw = liveState(t)
	var p32768 = 32768
	var p4096 = 4096

	var cases = []struct {
		why  string
		mut  func(*driver.SpanningTreeState, *string, *driver.SpanningTreePlan)
		mode string
		prio *int
	}{
		{why: "spanning tree reads as disabled",
			mut: func(s *driver.SpanningTreeState, _ *string, _ *driver.SpanningTreePlan) {
				s.Enabled = false
			}, mode: "mst", prio: &p32768},
		{why: "the mode did not parse at all",
			mut: func(s *driver.SpanningTreeState, _ *string, _ *driver.SpanningTreePlan) {
				s.Mode = ""
			}, mode: "mst", prio: &p32768},
		{why: "the device is in another mode",
			mut: func(s *driver.SpanningTreeState, _ *string, _ *driver.SpanningTreePlan) {
				s.Mode = "rstp"
			}, mode: "mst", prio: &p32768},
		{why: "THE REGION CAME BACK EMPTY — unread is not \"matching\"",
			mut: func(s *driver.SpanningTreeState, _ *string, _ *driver.SpanningTreePlan) {
				s.Region = driver.MSTRegion{}
			}, mode: "mst", prio: &p32768},
		{why: "the region name differs",
			mut: func(s *driver.SpanningTreeState, _ *string, _ *driver.SpanningTreePlan) {
				s.Region.Name = "somewhere-else"
			}, mode: "mst", prio: &p32768},
		{why: "the region revision differs",
			mut: func(s *driver.SpanningTreeState, _ *string, _ *driver.SpanningTreePlan) {
				s.Region.Revision = 2
			}, mode: "mst", prio: &p32768},
		{why: "one VLAN moved instance, so the digest differs",
			mut: func(s *driver.SpanningTreeState, _ *string, _ *driver.SpanningTreePlan) {
				s.Region.InstanceVLANs = map[int][]int{1: {42, 98, 99}}
			}, mode: "mst", prio: &p32768},
		{why: "the priority is not legible in the output",
			mut: func(_ *driver.SpanningTreeState, r *string, _ *driver.SpanningTreePlan) {
				*r = "MST0\r\n  Spanning tree enabled protocol mstp\r\n"
			}, mode: "mst", prio: &p32768},
		{why: "the wanted priority is a different one",
			mut:  func(_ *driver.SpanningTreeState, _ *string, _ *driver.SpanningTreePlan) {},
			mode: "mst", prio: &p4096},
	}

	var c = cases[0]
	for _, c = range cases {
		var st = live
		var r = raw
		var plan = mstPlan(c.prio)
		c.mut(&st, &r, &plan)
		var ok, _ = spanningTreeMatchesPlan(st, r, plan, c.mode)
		if ok {
			t.Errorf("NO-OP taken despite: %s", c.why)
		}
	}
}

// A plan with no priority and no region must still be able to no-op on the mode
// alone — otherwise the path never fires for the callers that have neither.
func TestModeOnlyPlanCanNoOp(t *testing.T) {
	var st, raw = liveState(t)
	var plan = driver.SpanningTreePlan{Enabled: true, Mode: "mstp"}
	var ok, why = spanningTreeMatchesPlan(st, raw, plan, "mst")
	if !ok {
		t.Fatalf("a mode-only plan did not no-op on a matching device: %q", why)
	}
}
