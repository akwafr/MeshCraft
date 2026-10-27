package mdpv1

import (
	"strings"
	"testing"
)

// The ONE hostname normalizer, tested at the home it moved to on 2026-08-09.
//
// internal/api has its own suite over the same semantics (hostname_apply_test.go)
// and it still passes through the delegation, which is the point: there is one
// implementation and two names for it. THIS file tests something the api suite
// cannot, because api never had the problem — it pins the ABSENCE of the four
// `sanitizeHostname` doctrines that shipped inside the connectors, each named
// with the range it shipped on. A re-introduced mangler fails here rather than
// appearing on a console banner months later.

// TestNoConnectorDoctrineSurvives is the regression guard on the drift this
// function was created to end. Each case is a real spelling that shipped.
func TestNoConnectorDoctrineSurvives(t *testing.T) {
	// cisco/ios, fs/s3400, fs/s3900 and ubiquiti/edgeswitch DROPPED every
	// character outside their alphabet. "Switch Zürich 1" went onto the console
	// banner, into every syslog line and out over the LLDP system-name TLV as
	// `Switch-Zrich-1` — a name that looks deliberate, with nothing to connect it
	// back to the deleted letter.
	var got, refusal = HostnameFromDeviceName("Switch Zürich 1")
	if refusal == "" {
		t.Fatalf("an accented name produced %q instead of a refusal", got)
	}
	if got != "" {
		t.Errorf("a refusal handed back the guess %q", got)
	}
	if !strings.Contains(refusal, "ü") {
		t.Errorf("the refusal does not name the offending character: %s", refusal)
	}

	// Four of the five kept `_`, which ValidHostname forbids outright — so the
	// provisioned name and the name the healer would push could never agree. It
	// is a separator an operator plainly meant as one: it becomes a hyphen.
	got, refusal = HostnameFromDeviceName("core_01")
	if refusal != "" || got != "core-01" {
		t.Errorf("HostnameFromDeviceName(\"core_01\") = %q / %q, want \"core-01\"",
			got, refusal)
	}
	if strings.Contains(got, "_") {
		t.Error("the output carries an underscore: ValidHostname forbids it, so " +
			"the reconciled path can never emit this name")
	}

	// Four of the five never trimmed, so a name ending in a separator produced
	// the trailing hyphen RFC 1123 rejects. It is TRIMMED here, not refused —
	// the operator wrote a separator and meant one.
	got, _ = HostnameFromDeviceName("core01-")
	if got != "core01" {
		t.Errorf("a trailing hyphen was not trimmed: %q", got)
	}

	// ALL FIVE fell back to the literal `switch` on an empty result: a fleet of
	// identically-named devices, produced by the one gesture meant to give each
	// switch an identity. An empty result is a refusal.
	got, refusal = HostnameFromDeviceName(" _ - . ")
	if got != "" || refusal == "" {
		t.Errorf("a name that is nothing but separators produced %q / %q", got, refusal)
	}
	if got == "switch" {
		t.Error("the `switch` fallback is back")
	}
}

// TestNormalizerIsTheInjectionBoundary: on six CLI connectors this string is
// concatenated into a configuration command, where a newline would run whatever
// follows it as a SECOND command with the privileges of the session. The
// normalizer must REFUSE, not strip the newline and send the rest.
func TestNormalizerIsTheInjectionBoundary(t *testing.T) {
	var nasty = []string{
		"core01\nusername attacker privilege 15",
		"a;b",
		"a`whoami`",
		"a\rb",
		"a|b",
		"a$(id)",
	}
	var in string
	for _, in = range nasty {
		var got, refusal = HostnameFromDeviceName(in)
		if got != "" || refusal == "" {
			t.Errorf("%q yielded the sendable name %q", in, got)
		}
		if !ValidHostname(got) && got != "" {
			t.Errorf("%q yielded %q, which ValidHostname itself rejects", in, got)
		}
	}
}

// TestNormalizerRefusesTheCeilingRatherThanTruncating: a truncated hostname
// answers to a name the inventory does not carry, and its LLDP system name stops
// matching the device — the correlation this whole gesture exists to enable,
// broken in the one direction nobody looks at.
func TestNormalizerRefusesTheCeilingRatherThanTruncating(t *testing.T) {
	var got, refusal = HostnameFromDeviceName(strings.Repeat("a", 64))
	if got != "" {
		t.Fatalf("a 64-character name produced %q — truncation is the anti-pattern "+
			"this file names", got)
	}
	if !strings.Contains(refusal, "does not shorten it") {
		t.Errorf("the refusal must say meshcraft will not shorten the name: %s", refusal)
	}
	got, refusal = HostnameFromDeviceName(strings.Repeat("a", 63))
	if refusal != "" || len(got) != 63 {
		t.Errorf("63 characters must be accepted, got %q / %q", got, refusal)
	}
}

// TestEverythingItReturnsIsSendable is the invariant the connectors rely on now
// that they have no sanitizer of their own: whatever comes back non-empty is a
// name ValidHostname accepts, so a builder can concatenate it into a command
// line without a quoting rule.
func TestEverythingItReturnsIsSendable(t *testing.T) {
	var names = []string{
		"sw-lab-01", "sw lab 01", "sw_lab_01", "sw.lab.01", "rack1/sw2",
		"  sw-lab  ", "-sw-lab-", "Switch1", "a\tb", "zürich", "sw:lab", "",
		"   ", "!!!", strings.Repeat("x", 200),
	}
	var in string
	for _, in = range names {
		var got, refusal = HostnameFromDeviceName(in)
		if got == "" {
			if refusal == "" {
				t.Errorf("%q returned neither a name nor a reason", in)
			}
			continue
		}
		if refusal != "" {
			t.Errorf("%q returned both a name %q and a reason %q", in, got, refusal)
		}
		if !ValidHostname(got) {
			t.Errorf("%q produced %q, which ValidHostname rejects — a connector "+
				"would concatenate that straight into a command line", in, got)
		}
	}
}

// TestCollapseHostnameHyphens pins the helper on its own: a run becomes one, and
// both ends are trimmed, which is where RFC 1123 forbids them.
func TestCollapseHostnameHyphens(t *testing.T) {
	var cases = []struct{ in, want string }{
		{"a--b", "a-b"},
		{"a-----b", "a-b"},
		{"-a-b-", "a-b"},
		{"---", ""},
		{"ab", "ab"},
		{"", ""},
	}
	var c struct{ in, want string }
	for _, c = range cases {
		var got = CollapseHostnameHyphens(c.in)
		if got != c.want {
			t.Errorf("CollapseHostnameHyphens(%q) = %q, expected %q", c.in, got, c.want)
		}
	}
}

// TestHostnameRefusalNamesEveryOffenderOnce: "invalid hostname" sends an
// operator hunting through a name they believed was fine.
func TestHostnameRefusalNamesEveryOffenderOnce(t *testing.T) {
	var msg = HostnameRefusal("a#b#c@d")
	if strings.Count(msg, "\"#\"") != 1 {
		t.Errorf("a repeated character must be named exactly once: %s", msg)
	}
	if !strings.Contains(msg, "\"@\"") {
		t.Errorf("the second offender is missing: %s", msg)
	}
	if !strings.Contains(HostnameRefusal(""), "empty system name") {
		t.Error("an empty name must still produce an explanation")
	}
	if !strings.Contains(HostnameRefusal("-a-"), "RFC 1123") {
		t.Error("a hyphen at the edge is a distinct diagnosis and must read as one")
	}
}
