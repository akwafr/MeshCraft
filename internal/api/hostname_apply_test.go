package api

import (
	"strings"
	"testing"
)

// Deriving a system name from meshcraft's own device name, and — more
// importantly — REFUSING to derive one when the result would not be the name the
// inventory carries.
//
// The rule this pins is stated in internal/driver/hostname.go: a hostname is
// never silently mangled, because the whole point of pushing it is that the
// device's console banner, its LLDP system-name TLV, and its syslog lines all
// answer with a name meshcraft can recognize. A normalization that quietly
// produced something else would break exactly that, in the direction nobody
// inspects.

// TestHostnameFromDeviceNameAccepts: the separators an operator actually types
// become hyphens, and runs collapse. These are the shapes the lab's own device
// names take.
func TestHostnameFromDeviceNameAccepts(t *testing.T) {
	var cases = []struct{ in, want string }{
		{"sw-lab-01", "sw-lab-01"},
		{"sw lab 01", "sw-lab-01"},
		{"sw_lab_01", "sw-lab-01"},
		{"sw.lab.01", "sw-lab-01"},
		{"poefs3400lab", "poefs3400lab"},
		// Runs of separators collapse rather than producing "sw--lab", which
		// is legal but is not what anyone means.
		{"sw  lab", "sw-lab"},
		{"sw _ lab", "sw-lab"},
		// Leading and trailing separators are trimmed: RFC 1123 forbids a
		// hyphen at either end, and "-sw" is a refusal an operator would find
		// baffling for a name they wrote as "sw".
		{"  sw-lab  ", "sw-lab"},
		{"_sw-lab_", "sw-lab"},
		{"Switch1", "Switch1"},
		// A slash is a separator like the others: "rack1/sw2" is a name people
		// genuinely write, and it normalizes rather than being refused.
		{"rack1/sw2", "rack1-sw2"},
		// A hyphen at either end is TRIMMED, not refused. The operator wrote a
		// name, not a violation — refusing "sw-lab-" for a character they
		// probably did not notice typing would be pedantry, and the trimmed form
		// is unambiguously what they meant.
		{"sw-lab-", "sw-lab"},
		{"-sw-lab", "sw-lab"},
	}
	var c struct{ in, want string }
	for _, c = range cases {
		var got, refusal = hostnameFromDeviceName(c.in)
		if refusal != "" {
			t.Errorf("hostnameFromDeviceName(%q) refused: %s", c.in, refusal)
			continue
		}
		if got != c.want {
			t.Errorf("hostnameFromDeviceName(%q) = %q, expected %q", c.in, got, c.want)
		}
	}
}

// TestHostnameFromDeviceNameRefuses is the half that matters. Every case here
// must come back with an EMPTY name and a non-empty explanation — never a
// best-effort guess.
func TestHostnameFromDeviceNameRefuses(t *testing.T) {
	var cases = map[string]string{
		"empty":           "",
		"blank":           "   ",
		"only separators": " _ . ",
		"accented":        "zürich-lab",
		"colon":           "sw:lab",
		"parenthesis":     "sw (lab)",
		"plus sign":       "sw+lab",
		// 64 characters after normalization: one over the DNS label limit, and
		// the case that used to be silently truncated.
		"too long": strings.Repeat("a", 64),
	}
	var label, in string
	for label, in = range cases {
		var got, refusal = hostnameFromDeviceName(in)
		if refusal == "" {
			t.Errorf("%s: hostnameFromDeviceName(%q) = %q, expected a refusal",
				label, in, got)
		}
		if got != "" {
			t.Errorf("%s: refused but still returned a name %q — a refusal must "+
				"not hand back a guess", label, got)
		}
	}
}

// TestHostnameNotTruncated: a name one character over the limit is REFUSED, and
// the refusal must say why in terms an operator can act on. This is a
// regression guard on a real defect — the derivation used to cut to 63
// characters and return the stump, which is the documented anti-pattern.
func TestHostnameNotTruncated(t *testing.T) {
	var long = strings.Repeat("a", 64)
	var got, refusal = hostnameFromDeviceName(long)
	if got != "" {
		t.Fatalf("a 64-character name produced %q — truncation is exactly what "+
			"internal/driver/hostname.go forbids", got)
	}
	if refusal == "" {
		t.Fatal("no refusal for a 64-character name")
	}
	// The message must point at the decision the operator has to make, not
	// merely state a rule.
	var want = []string{"63", "hostname"}
	var w string
	for _, w = range want {
		if !strings.Contains(refusal, w) {
			t.Errorf("refusal does not mention %q: %s", w, refusal)
		}
	}
	// Exactly 63 must still pass: the guard is "longer than", not "close to".
	var ok, refused = hostnameFromDeviceName(strings.Repeat("a", 63))
	if refused != "" || len(ok) != 63 {
		t.Errorf("a 63-character name must be accepted, got %q / %q", ok, refused)
	}
}

// TestHostnameRefusalNamesTheCharacter: "invalid hostname" sends the operator
// guessing. The refusal must quote the byte that actually broke the rule,
// because it is nearly always one character pasted in from a label.
func TestHostnameRefusalNamesTheCharacter(t *testing.T) {
	var msg = hostnameRefusal("sw:lab")
	if !strings.Contains(msg, ":") {
		t.Errorf("refusal for \"sw:lab\" does not name the \":\": %s", msg)
	}
	msg = hostnameRefusal("zürich")
	if !strings.Contains(msg, "ü") {
		t.Errorf("refusal for \"zürich\" does not name the \"ü\": %s", msg)
	}
	// A hyphen at the edge is a distinct diagnosis from a bad character, and
	// must read as such.
	msg = hostnameRefusal("-sw")
	if !strings.Contains(msg, "hyphen") {
		t.Errorf("refusal for \"-sw\" does not mention the hyphen rule: %s", msg)
	}
	msg = hostnameRefusal("")
	if msg == "" {
		t.Error("an empty name must still produce an explanation")
	}
}

// TestCollapseHyphens pins the helper on its own: a run becomes one, and both
// ends are trimmed, which is where RFC 1123 forbids them.
func TestCollapseHyphens(t *testing.T) {
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
		var got = collapseHyphens(c.in)
		if got != c.want {
			t.Errorf("collapseHyphens(%q) = %q, expected %q", c.in, got, c.want)
		}
	}
}
