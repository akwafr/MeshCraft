package gnmipoll

import "testing"

// Round trip. The textual form is what a human writes in a subscription and what
// a device echoes back in a notification; if parse and format disagree, every
// comparison in the translation table is against a string nobody can predict.
func TestParsePathRoundTrip(t *testing.T) {
	var s string
	for _, s = range []string{
		"/interfaces/interface[name=Ethernet49]/state/counters/in-octets",
		"/interfaces/interface[name=*]/state/oper-status",
		"/interfaces",
		"",
	} {
		var p, err = ParsePath(s)
		if err != nil {
			t.Fatalf("ParsePath(%q) errored: %v", s, err)
		}
		if p.String() != s {
			t.Errorf("round trip of %q gave %q", s, p.String())
		}
	}
}

// THE BREAKOUT-PORT CASE, and the reason this is a scanner and not a Split. A
// 40G cage split into four gives ports named Ethernet49/1; a parser that split
// on "/" would attribute their counters to a port called "Ethernet49" — which,
// on the very same switch, usually also exists.
func TestParsePathKeepsSlashInsideAKey(t *testing.T) {
	var p, err = ParsePath("/interfaces/interface[name=Ethernet49/1]/state/oper-status")
	if err != nil {
		t.Fatalf("errored: %v", err)
	}
	if p.Key("interface", "name") != "Ethernet49/1" {
		t.Fatalf("port key = %q, want Ethernet49/1", p.Key("interface", "name"))
	}
	var names = p.Names()
	if len(names) != 4 {
		t.Errorf("%d elements in %v, want 4 — a slash inside a key was read as a separator",
			len(names), names)
	}
}

func TestParsePathMultipleKeys(t *testing.T) {
	var p, err = ParsePath("/a/b[x=1][y=2]/c")
	if err != nil {
		t.Fatalf("errored: %v", err)
	}
	if p.Key("b", "x") != "1" || p.Key("b", "y") != "2" {
		t.Errorf("keys not both read: %v", p)
	}
	// Sorted output, because Go map iteration is not: without it a path is equal
	// to itself only some of the time.
	if p.String() != "/a/b[x=1][y=2]/c" {
		t.Errorf("String() = %q, want the keys in sorted order", p.String())
	}
}

// A malformed path returns an ERROR, never a partial path. A half-parsed path
// drops the element carrying the port key, and the sample it produces is
// attributed to whatever survived — a failure with no symptom.
func TestParsePathRefusesMalformed(t *testing.T) {
	var s string
	for _, s = range []string{
		"/interfaces/interface[name=Ethernet49/state/oper-status", // unclosed bracket
		"/interfaces//interface",                                  // empty element
		"/interfaces/interface[name]/state",                       // key with no value
		"/interfaces/interface[=x]/state",                         // value with no key
		"/[name=x]",                                               // key on no element
		"/interfaces/",                                            // trailing slash
	} {
		var p, err = ParsePath(s)
		if err == nil {
			t.Errorf("ParsePath(%q) accepted it and returned %v", s, p)
		}
		if p != nil {
			t.Errorf("ParsePath(%q) returned a path alongside its error: %v", s, p)
		}
	}
}

// Join is the function whose wrong answer is plausible: the port lives in the
// prefix, the leaf lives in the update, and neither half alone produces an error.
func TestJoinPrefixAndUpdate(t *testing.T) {
	var prefix, err = ParsePath("/interfaces/interface[name=Ethernet1]")
	if err != nil {
		t.Fatal(err)
	}
	var rel Path
	rel, err = ParsePath("state/counters/in-octets")
	if err != nil {
		t.Fatal(err)
	}
	var full = Join(prefix, rel)
	var want = "/interfaces/interface[name=Ethernet1]/state/counters/in-octets"
	if full.String() != want {
		t.Errorf("Join = %q, want %q", full.String(), want)
	}
	if full.Key("interface", "name") != "Ethernet1" {
		t.Error("the port key did not survive the join")
	}
}

// Join must COPY. Sharing a backing array lets the second update of a
// notification overwrite the first's elements — a bug that appears only on
// notifications carrying more than one update, i.e. never in the first test
// anybody writes and always on a real device.
func TestJoinDoesNotAliasThePrefix(t *testing.T) {
	var prefix, err = ParsePath("/interfaces/interface[name=Ethernet1]")
	if err != nil {
		t.Fatal(err)
	}
	var a, b Path
	a, err = ParsePath("state/counters/in-octets")
	if err != nil {
		t.Fatal(err)
	}
	b, err = ParsePath("state/counters/out-octets")
	if err != nil {
		t.Fatal(err)
	}
	var first = Join(prefix, a)
	var second = Join(prefix, b)
	if first.String() != "/interfaces/interface[name=Ethernet1]/state/counters/in-octets" {
		t.Errorf("the first join was mutated by the second: %q", first.String())
	}
	if second.String() != "/interfaces/interface[name=Ethernet1]/state/counters/out-octets" {
		t.Errorf("second join = %q", second.String())
	}
	if len(prefix) != 2 {
		t.Errorf("the prefix itself grew to %d elements", len(prefix))
	}
}

// Key reads the INNERMOST keyed element: a path may carry several, and the
// deepest is the one describing what is being reported.
func TestKeyTakesTheInnermostElement(t *testing.T) {
	var p, err = ParsePath("/interfaces/interface[name=Et1]/subinterfaces/subinterface[index=0]")
	if err != nil {
		t.Fatal(err)
	}
	if p.Key("interface", "name") != "Et1" {
		t.Errorf("interface name = %q", p.Key("interface", "name"))
	}
	if p.Key("subinterface", "index") != "0" {
		t.Errorf("subinterface index = %q", p.Key("subinterface", "index"))
	}
	if p.Key("interface", "absent") != "" {
		t.Error("an absent key returned something")
	}
}

// The constant paths are parsed at package init through mustPath, which panics.
// This test is what makes that panic a build-time failure for whoever mistypes
// one, rather than a proxy that dies on its first subscription.
func TestConstantPathsParse(t *testing.T) {
	var p Path
	for _, p = range []Path{pathIfState, pathEthernetState, pathCounters} {
		if len(p) == 0 {
			t.Error("a constant subscription path is empty")
		}
		if p.Key("interface", "name") != "*" {
			t.Errorf("%q does not use the interface wildcard: one subscription is meant "+
				"to cover every port", p.String())
		}
	}
}

// JUNK AFTER A CLOSING BRACKET IS A MALFORMED PATH, and used to be folded into
// the element name instead.
//
// `/interfaces/interface[name=Et1]junk/state/counters` parsed to an element
// called "interfacejunk" with the key read correctly: structurally plausible,
// naming nothing. Every other malformation in this file is refused, and this was
// the single hole — the kind that surfaces three layers away as a leaf matching
// no table, i.e. as a series that is silently never published.
func TestParsePathRefusesJunkAfterAKey(t *testing.T) {
	var bad = []string{
		"/interfaces/interface[name=Et1]junk/state",
		"/interfaces/interface[name=Et1]junk",
		"/interfaces/interface[name=Et1] /state",
	}
	var s string
	for _, s = range bad {
		var p, err = ParsePath(s)
		if err == nil {
			t.Errorf("ParsePath(%q) accepted junk after the key and gave %s", s, p.String())
		}
	}
	// And the well-formed neighbours must still parse: a bracket immediately
	// followed by another bracket (two keys on one element) and by a slash are
	// both legal, and a fix that refused them would break every real path.
	var good = []string{
		"/interfaces/interface[name=Et1]/state/counters",
		"/lldp/interfaces/interface[name=Et1]/neighbors/neighbor[id=x]/state",
		"/interfaces/interface[name=Et1]",
	}
	for _, s = range good {
		var _, err = ParsePath(s)
		if err != nil {
			t.Errorf("ParsePath(%q) refused a well-formed path: %v", s, err)
		}
	}
}
