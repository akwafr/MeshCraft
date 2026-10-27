package mdpv1

import "sort"

// Faceplate order — ONE comparator, taking each platform's own name split as a
// parameter.
//
// # WHAT IS SHARED HERE, AND WHAT DELIBERATELY IS NOT
//
// Every connector that hands a caller a list of ports owes it faceplate order:
// "GE2" before "GE10", never the lexical order that files GE10 between GE1
// and GE2, and never Go's map order, which would redraw a fifty-two socket panel
// differently on every read. Half a dozen connectors had written that comparator
// out by hand, and the copies had begun to disagree with one another in ways
// nothing in the suite would have caught.
//
// The disagreement was never about the ORDER. It was about what a port name
// LOOKS LIKE, and THAT difference is a fact about the hardware:
//
//   - the FS S3400 faceplate is 48 copper sockets and 4 SFP+ cages, so its names
//     are GE<n> AND XG<n>, and the two are different kinds of thing that must not
//     interleave;
//   - the EdgeSwitch faceplate this fleet models has one kind, GE<n>;
//   - the FS S3900 storm-control path takes its index off the trailing digits and
//     names no kind at all.
//
// So the SPLIT is a parameter. It encodes a platform fact, it stays in the
// platform's own package next to the CLI names it was derived from, and
// collapsing three of them into one "obviously equivalent" reader is exactly the
// false unity stormcontrol.go's header argues against at length — a connector
// that starts accepting a name its device cannot utter writes to a port that does
// not exist.
//
// The ORDERING is not a platform fact. Group by kind, then by number, and fall
// back to string order for a name the split does not recognize: nobody disagreed
// about that, and nobody was ever going to. Written six times it was six chances
// to drift; written once it is one.
//
// # THE FALLBACK IS PART OF THE CONTRACT, NOT AN EDGE CASE
//
// A name the split refuses is ordered as a STRING against everything else —
// never dropped, never bunched at one end, never compared as a zero. Sorting must
// not lose a name: these slices carry ports an operator asked for, and a name
// this code does not understand is the one they most need to find in the result.
// It also keeps the relation usable by [sort], which needs a consistent answer;
// deciding an unparseable pair by their strings gives one for every pair, in
// both directions.
//
// The sorts are STABLE. Two names the split cannot tell apart — and on a
// single-kind platform that is any two names it refuses the same way — keep the
// order they arrived in, so the same read produces the same list twice and a
// caller comparing two applies is comparing the gesture rather than the shuffle.

// PortNameSplit reads a standard port name into its index, its kind, and whether
// the platform recognizes the name at all. It is the shape each connector's own
// splitStandard already has.
//
// kind is compared as a string and used ONLY for grouping, so a platform with a
// single kind of socket may return the same value — "" included — for all of
// them, and its ports then order by index alone.
type PortNameSplit func(name string) (index int, kind string, ok bool)

// PortNameLess reports whether a comes before b on the faceplate: by kind first,
// then by index, and by plain string order when either name is one the platform
// does not recognize (cf. the file header on why that fallback is load-bearing).
func PortNameLess(a, b string, split PortNameSplit) bool {
	var na, ka, oka = split(a)
	var nb, kb, okb = split(b)
	if !oka || !okb {
		return a < b
	}
	if ka != kb {
		return ka < kb
	}
	return na < nb
}

// SortPortNames puts a slice of standard port names in faceplate order, in place.
func SortPortNames(names []string, split PortNameSplit) {
	sort.SliceStable(names, func(i, j int) bool {
		return PortNameLess(names[i], names[j], split)
	})
}

// SortByPortName puts any slice whose elements CARRY a port name in faceplate
// order, in place — [StormControlPortState], [DHCPSnoopPortState], their intent
// siblings, a connector's own per-port struct. port extracts the name from one
// element.
//
// Generic rather than one function per element type: the element type is the only
// thing that ever differed between the copies this replaces, and a comparator
// duplicated once per struct is the same drift in a new costume.
func SortByPortName[T any](items []T, port func(T) string, split PortNameSplit) {
	sort.SliceStable(items, func(i, j int) bool {
		return PortNameLess(port(items[i]), port(items[j]), split)
	})
}
