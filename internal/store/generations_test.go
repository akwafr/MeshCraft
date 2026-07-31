package store

import "testing"

// Secret try order is the heart of the rotation mechanism, and its logic is too
// easy to break without noticing: bad order only shows when a lagging device
// becomes unreachable.
//
// The SecretsForDevice SQL query sorts on three keys:
//
//	1. `is_installed` first — what the device is KNOWN to carry;
//	2. then state, `retiring` BEFORE `active` — a device not yet updated carries
//	   the old secret, the most likely case;
//	3. then descending generation.
//
// This test locks that ordering by replaying the same sort key as the database,
// without needing a database.

type row struct {
	gen         int
	state       string
	isInstalled bool
}

// sortKey reproduces `order by is_installed desc, case state …, generation desc`.
func sortKey(r row) (int, int, int) {
	var installed = 0
	if r.isInstalled {
		installed = 1
	}
	var stateRank int
	switch r.state {
	case "retiring":
		stateRank = 0
	case "active":
		stateRank = 1
	default:
		stateRank = 2
	}
	return -installed, stateRank, -r.gen
}

func less(a, b row) bool {
	var a1, a2, a3 = sortKey(a)
	var b1, b2, b3 = sortKey(b)
	if a1 != b1 {
		return a1 < b1
	}
	if a2 != b2 {
		return a2 < b2
	}
	return a3 < b3
}

func order(rows []row) []int {
	var sorted = append([]row{}, rows...)
	var i, j int
	for i = 1; i < len(sorted); i++ {
		for j = i; j > 0 && less(sorted[j], sorted[j-1]); j-- {
			sorted[j], sorted[j-1] = sorted[j-1], sorted[j]
		}
	}
	var out = make([]int, 0, len(sorted))
	for i = range sorted {
		out = append(out, sorted[i].gen)
	}
	return out
}

// Nominal case: the device is known to carry generation 2, generation 3 was
// just created. Try 2 first — pointless to fail on 3 when we know what it has.
func TestOrderPrefersInstalled(t *testing.T) {
	var got = order([]row{
		{gen: 3, state: "active"},
		{gen: 2, state: "retiring", isInstalled: true},
		{gen: 1, state: "retiring"},
	})
	if got[0] != 2 {
		t.Fatalf("installed generation must come first, got order: %v", got)
	}
}

// Device never observed (adopted yesterday, or lost state): try RETIRING
// generations first, because a device not yet updated necessarily carries an
// old one. Trying the newest first would fail across the whole lagging fleet.
func TestOrderPrefersRetiringWhenUnknown(t *testing.T) {
	var got = order([]row{
		{gen: 3, state: "active"},
		{gen: 2, state: "retiring"},
		{gen: 1, state: "retiring"},
	})
	if got[0] != 2 || got[1] != 1 || got[2] != 3 {
		t.Fatalf("want [2 1 3] (retiring newest to oldest, then active), got %v", got)
	}
}

// Among two retiring generations, newest first: that is the one the device is
// most likely to carry.
func TestOrderRetiringNewestFirst(t *testing.T) {
	var got = order([]row{
		{gen: 1, state: "retiring"},
		{gen: 4, state: "retiring"},
		{gen: 2, state: "retiring"},
	})
	if got[0] != 4 || got[1] != 2 || got[2] != 1 {
		t.Fatalf("want [4 2 1], got %v", got)
	}
}

// Regression: installed state wins even over a newer retiring generation. What
// we OBSERVED beats what we assume.
func TestOrderInstalledBeatsNewerRetiring(t *testing.T) {
	var got = order([]row{
		{gen: 5, state: "retiring"},
		{gen: 2, state: "retiring", isInstalled: true},
		{gen: 6, state: "active"},
	})
	if got[0] != 2 {
		t.Fatalf("observation must beat assumption, got %v", got)
	}
}

// A brand-new fleet has only one generation: order must not lose it.
func TestOrderSingleGeneration(t *testing.T) {
	var got = order([]row{{gen: 1, state: "active"}})
	if len(got) != 1 || got[0] != 1 {
		t.Fatalf("got %v", got)
	}
}
