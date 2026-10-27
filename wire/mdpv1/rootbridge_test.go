package mdpv1

// The inter-root BPDU filter computation.
//
// Every assertion here is written as the failure it prevents, because this is
// the one calculation in the product whose wrong answer is a port that stops
// participating in spanning tree — which nothing on any screen shows, and which
// is discovered when a loop forms.

import (
	"reflect"
	"testing"
)

// chain builds A—B—C—… with predictable port names: the port on X facing Y is
// "to-Y".
func chain(names ...string) []Link {
	var out []Link
	var i int
	for i = 0; i+1 < len(names); i++ {
		out = append(out, Link{
			ADevice: names[i], APort: "to-" + names[i+1],
			BDevice: names[i+1], BPort: "to-" + names[i],
		})
	}
	return out
}

// ONE ROOT FILTERS NOTHING. The ordinary case must stay free of every risk this
// file describes: with one root there is one tree, it wins it, and no BPDU
// anywhere needs stopping.
func TestOneRootFiltersNothing(t *testing.T) {
	var got = InterRootFilterPorts(chain("a", "b", "c"), []string{"a"})
	if len(got) != 0 {
		t.Fatalf("a single root produced filtering: %v", got)
	}
	// And no roots at all, likewise.
	if len(InterRootFilterPorts(chain("a", "b", "c"), nil)) != 0 {
		t.Error("no roots produced filtering")
	}
}

// THE ROOTS THEMSELVES ARE FILTERED, which is the requirement most easily lost.
//
// "all the switches between them AND themselves" — a computation that marked
// only the intermediate hops would leave both roots still emitting BPDUs into
// the path, and the two trees would merge again through the very link the filter
// was supposed to cut.
func TestBothRootsAndEveryHopAreFiltered(t *testing.T) {
	var got = InterRootFilterPorts(chain("a", "b", "c"), []string{"a", "c"})
	var want = map[string][]string{
		"a": {"to-b"},
		"b": {"to-a", "to-c"},
		"c": {"to-b"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("filter set = %v, want %v", got, want)
	}
}

// TWO DIRECTLY CABLED ROOTS: one hop, both ends filtered, nothing else.
func TestAdjacentRootsFilterOnlyTheirOwnLink(t *testing.T) {
	var links = append(chain("a", "b"), Link{
		ADevice: "b", APort: "to-z", BDevice: "z", BPort: "to-b",
	})
	var got = InterRootFilterPorts(links, []string{"a", "b"})
	var want = map[string][]string{"a": {"to-b"}, "b": {"to-a"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("filter set = %v, want %v — the spur to z must not be cut", got, want)
	}
}

// EVERY SHORTEST PATH IS CUT, not one of them.
//
// Two equal-length routes between the roots ARE the redundant path the operator
// asserted does not exist. Cutting one and leaving the other is the worst
// possible outcome: the trees silently merge again through the survivor, the
// filtering achieves nothing, and the configuration looks done. Cutting both
// also makes a wrong assertion visible as a surprisingly large filter list,
// rather than as a storm weeks later.
func TestEveryShortestPathIsCut(t *testing.T) {
	// a—b—d and a—c—d: two routes of equal length.
	var links = []Link{
		{ADevice: "a", APort: "to-b", BDevice: "b", BPort: "to-a"},
		{ADevice: "b", APort: "to-d", BDevice: "d", BPort: "to-b"},
		{ADevice: "a", APort: "to-c", BDevice: "c", BPort: "to-a"},
		{ADevice: "c", APort: "to-d", BDevice: "d", BPort: "to-c"},
	}
	var got = InterRootFilterPorts(links, []string{"a", "d"})
	var want = map[string][]string{
		"a": {"to-b", "to-c"},
		"b": {"to-a", "to-d"},
		"c": {"to-a", "to-d"},
		"d": {"to-b", "to-c"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("filter set = %v, want both routes cut: %v", got, want)
	}
}

// A LONGER DETOUR IS NOT CUT.
//
// Cutting non-shortest paths would mean filtering BPDUs across most of a site to
// separate two switches — a spectacular way to misread the instruction, and a
// configuration nobody wants.
func TestALongerDetourIsLeftAlone(t *testing.T) {
	// a—b (1 hop) and a—x—y—b (3 hops).
	var links = []Link{
		{ADevice: "a", APort: "to-b", BDevice: "b", BPort: "to-a"},
		{ADevice: "a", APort: "to-x", BDevice: "x", BPort: "to-a"},
		{ADevice: "x", APort: "to-y", BDevice: "y", BPort: "to-x"},
		{ADevice: "y", APort: "to-b", BDevice: "b", BPort: "to-y"},
	}
	var got = InterRootFilterPorts(links, []string{"a", "b"})
	var want = map[string][]string{"a": {"to-b"}, "b": {"to-a"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("filter set = %v, want only the direct link: %v", got, want)
	}
}

// UNCONNECTED ROOTS FILTER NOTHING, and nothing is invented.
//
// A path meshcraft cannot see is a path it cannot cut. Guessing one would filter
// BPDUs on a link chosen at random, which is worse than doing nothing — the
// operator would believe the trees were separated.
func TestUnconnectedRootsFilterNothing(t *testing.T) {
	var links = append(chain("a", "b"), chain("c", "d")...)
	var got = InterRootFilterPorts(links, []string{"a", "c"})
	if len(got) != 0 {
		t.Fatalf("roots with no known path between them produced filtering: %v", got)
	}
}

// THREE ROOTS: every PAIR is separated, not just the first two.
func TestThreeRootsSeparateEveryPair(t *testing.T) {
	var got = InterRootFilterPorts(chain("a", "b", "c"), []string{"a", "b", "c"})
	var want = map[string][]string{
		"a": {"to-b"},
		"b": {"to-a", "to-c"},
		"c": {"to-b"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("filter set = %v, want every pair separated: %v", got, want)
	}
}

// Degenerate topology input must not hang or panic: a self-link would make the
// search oscillate, and an empty endpoint names no port to filter.
func TestDegenerateLinksAreIgnored(t *testing.T) {
	var links = []Link{
		{ADevice: "a", APort: "lo", BDevice: "a", BPort: "lo"},
		{ADevice: "", APort: "x", BDevice: "b", BPort: "y"},
		{ADevice: "a", APort: "to-b", BDevice: "b", BPort: "to-a"},
	}
	var got = InterRootFilterPorts(links, []string{"a", "b"})
	var want = map[string][]string{"a": {"to-b"}, "b": {"to-a"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("filter set = %v, want %v", got, want)
	}
}

// --- Contested instances: WHEN filtering is required at all ---

// Two roots on DIFFERENT MST instances are the ordinary, safe use of MST.
// Separate trees already, no BPDUs to filter, and no assertion about cabling
// demanded of the operator. Treating this as contested would put a red flag in
// front of the exact topology MST exists to serve.
func TestDifferentInstancesAreNotContested(t *testing.T) {
	var p = RootPlan{Roots: []RootIntent{
		{DeviceID: "a", Instances: []int{1}},
		{DeviceID: "b", Instances: []int{2}},
	}}
	var got = p.Contested()
	if len(got) != 0 {
		t.Fatalf("instances %v reported contested: two roots on two trees is normal MST", got)
	}
}

// Two roots on the SAME instance are two claims on one tree — the case the
// warning and the filtering exist for.
func TestSameInstanceIsContested(t *testing.T) {
	var p = RootPlan{Roots: []RootIntent{
		{DeviceID: "a", Instances: []int{1, 2}},
		{DeviceID: "b", Instances: []int{2, 3}},
	}}
	var got = p.Contested()
	if !reflect.DeepEqual(got, []int{2}) {
		t.Fatalf("contested = %v, want [2]", got)
	}
}

// Outside MSTP there is ONE tree, so two roots always contest it — and the
// single-tree tally must not collide with instance 0, which is a legitimate MST
// instance id (the CIST).
func TestTwoRootsOutsideMSTPAlwaysContest(t *testing.T) {
	var p = RootPlan{Roots: []RootIntent{{DeviceID: "a"}, {DeviceID: "b"}}}
	if len(p.Contested()) == 0 {
		t.Fatal("two roots on a single-tree mode were not reported as contesting")
	}
	// Instance 0 named explicitly on one side must not be folded together with
	// the single-tree bucket: "root of the CIST" and "root of whatever tree
	// exists" are different claims.
	var mixed = RootPlan{Roots: []RootIntent{
		{DeviceID: "a"},
		{DeviceID: "b", Instances: []int{0}},
	}}
	if len(mixed.Contested()) != 0 {
		t.Error("the single-tree bucket collided with MST instance 0")
	}
}

// One root never contests anything, whatever it claims.
func TestOneRootNeverContests(t *testing.T) {
	var p = RootPlan{Roots: []RootIntent{{DeviceID: "a", Instances: []int{0, 1, 2}}}}
	var got = p.Contested()
	if len(got) != 0 {
		t.Fatalf("a single root contested %v with itself", got)
	}
}
