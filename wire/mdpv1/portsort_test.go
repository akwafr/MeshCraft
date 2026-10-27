package mdpv1

import (
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// splitGEXG is the S3400-shaped split: two kinds of socket, GE<n> for the copper
// and XG<n> for the cages. The connectors keep their own copy of this — cf.
// portsort.go on why — and the test needs one to exercise the comparator.
func splitGEXG(s string) (int, string, bool) {
	var up = strings.ToUpper(strings.TrimSpace(s))
	var kind string
	switch {
	case strings.HasPrefix(up, "XG"):
		kind = "XG"
	case strings.HasPrefix(up, "GE"):
		kind = "GE"
	default:
		return 0, "", false
	}
	var n, err = strconv.Atoi(up[2:])
	if err != nil || n < 1 {
		return 0, "", false
	}
	return n, kind, true
}

// splitGEOnly is the single-kind shape (EdgeSwitch, S3900): every recognized name
// reports the same kind, so the order is the index alone.
func splitGEOnly(s string) (int, string, bool) {
	var n, kind, ok = splitGEXG(s)
	if !ok || kind != "GE" {
		return 0, "", false
	}
	return n, "GE", true
}

// The two kinds do not interleave: a panel that drew XG1 between GE48 and GE49
// would be showing a cage where a copper socket is.
func TestSortPortNamesGroupsKindsBeforeNumbers(t *testing.T) {
	var names = []string{"XG2", "GE48", "GE2", "XG1", "GE12"}
	SortPortNames(names, splitGEXG)
	var want = []string{"GE2", "GE12", "GE48", "XG1", "XG2"}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("order = %v, want %v", names, want)
	}
}

// NUMERICALLY, not lexically. Lexical order puts GE10 before GE2, which is how a
// faceplate ends up drawn with its sockets shuffled and how a command list stops
// being reproducible between two runs of the same gesture.
func TestSortPortNamesIsNumericNotLexical(t *testing.T) {
	var names = []string{"GE10", "GE2", "GE1", "GE18", "GE9", "GE11"}
	SortPortNames(names, splitGEOnly)
	var got = strings.Join(names, " ")
	if got != "GE1 GE2 GE9 GE10 GE11 GE18" {
		t.Errorf("order = %q", got)
	}
	if got == "GE1 GE10 GE11 GE18 GE2 GE9" {
		t.Error("names were sorted as strings")
	}
}

// A name the platform does not recognize is ordered as a STRING and KEPT. Losing
// it, or bunching every unparseable name at one end, would hide from the operator
// the one entry they most need to see in the result.
func TestSortPortNamesFallsBackToStringOrder(t *testing.T) {
	var names = []string{"ether3", "GE2", "1/11", "GE1", "Uplink"}
	SortPortNames(names, splitGEXG)
	if len(names) != 5 {
		t.Fatalf("a name was lost: %v", names)
	}
	var seen = map[string]bool{}
	var n string
	for _, n = range names {
		seen[n] = true
	}
	var want string
	for _, want = range []string{"ether3", "GE2", "1/11", "GE1", "Uplink"} {
		if !seen[want] {
			t.Errorf("sorting dropped %q: %v", want, names)
		}
	}
	// Every pair involving an unrecognized name is decided by the strings, so the
	// whole slice comes out in string order here: "1/11" < "GE1" < "GE2" <
	// "Uplink" < "ether3" (upper case sorts before lower in ASCII).
	var got = strings.Join(names, " ")
	if got != "1/11 GE1 GE2 Uplink ether3" {
		t.Errorf("fallback order = %q", got)
	}
}

// The recognized names still sort numerically among themselves when no
// unrecognized name stands between them.
func TestSortPortNamesKeepsNumericOrderBesideAnUnparseableName(t *testing.T) {
	var names = []string{"GE10", "zzz", "GE2"}
	SortPortNames(names, splitGEOnly)
	var got = strings.Join(names, " ")
	// GE2 < GE10 numerically, and "zzz" loses to both as a string.
	if got != "GE2 GE10 zzz" {
		t.Errorf("order = %q, want %q", got, "GE2 GE10 zzz")
	}
}

type portRow struct {
	Port string
	Note string
}

// SortByPortName gives the connectors' own element types the same order without
// each of them restating the less function.
func TestSortByPortNameOrdersElementTypes(t *testing.T) {
	var rows = []portRow{
		{Port: "XG1"}, {Port: "GE10"}, {Port: "GE2"},
	}
	SortByPortName(rows, func(r portRow) string { return r.Port }, splitGEXG)
	var got []string
	var i int
	for i = range rows {
		got = append(got, rows[i].Port)
	}
	if !reflect.DeepEqual(got, []string{"GE2", "GE10", "XG1"}) {
		t.Errorf("order = %v", got)
	}
}

// STABLE: elements the split cannot tell apart keep the order they arrived in, so
// the same read twice produces the same list twice.
func TestSortByPortNameIsStableForEqualKeys(t *testing.T) {
	var rows = []portRow{
		{Port: "GE1", Note: "first"}, {Port: "GE1", Note: "second"}, {Port: "GE1", Note: "third"},
	}
	SortByPortName(rows, func(r portRow) string { return r.Port }, splitGEXG)
	if rows[0].Note != "first" || rows[1].Note != "second" || rows[2].Note != "third" {
		t.Errorf("stability lost: %+v", rows)
	}
}

// PortNameLess must answer consistently in both directions, including for a pair
// of names neither of which parses — sort relies on it and an inconsistent less
// function is how a sort silently corrupts a slice.
func TestPortNameLessIsConsistentBothWays(t *testing.T) {
	var pairs = [][2]string{
		{"GE1", "GE2"}, {"GE2", "XG1"}, {"ether1", "ether2"}, {"GE1", "ether1"},
	}
	var p [2]string
	for _, p = range pairs {
		if !PortNameLess(p[0], p[1], splitGEXG) {
			t.Errorf("%q should sort before %q", p[0], p[1])
		}
		if PortNameLess(p[1], p[0], splitGEXG) {
			t.Errorf("%q must not sort before %q", p[1], p[0])
		}
	}
	if PortNameLess("GE1", "GE1", splitGEXG) {
		t.Error("a name must not sort before itself")
	}
}
