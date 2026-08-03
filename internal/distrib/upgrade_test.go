package distrib

import "testing"

// Proxy version comparison, and the bug it used to carry.
//
// This package had no test. This one exists for a precise reason: the single
// decision line of the entire upgrade cycle — `candidate > current` — got it
// wrong on the most common case in a development fleet, and got it wrong in
// the direction that BLOCKS. A proxy reporting `dev` was "newer" than any
// build timestamp, and therefore up to date forever.
//
// This wasn't visible: the UI showed "up_to_date", which is exactly what
// you want to read. The reference proxy stayed that way until someone
// looked into why it was reporting `dev`.

// TestIsNewerNeLaisseAucunProxyBloque
//
// THE CASE THAT MATTERS is the third line: `dev` → a timestamp. Without it,
// the controller has no way left to recover a mis-stamped proxy, and the
// architecture rule "no intervention on the proxy's machine, ever" is dead.
func TestIsNewerNeLaisseAucunProxyBloque(t *testing.T) {
	var cas = []struct {
		candidate, current string
		attend             bool
		pourquoi           string
	}{
		{"20260729.213000", "20260726.181502", true,
			"two timestamps: lexicographic comparison IS chronology"},
		{"20260726.181502", "20260729.213000", false,
			"we never downgrade a version without an explicit pin"},
		{"20260729.213000", "20260729.213000", false,
			"the same version isn't an upgrade"},

		{"20260729.213000", "dev", true,
			"THE BUG THAT WAS FIXED: \"dev\" sorts AFTER digits, so the old " +
				"`candidate > current` returned false — a proxy stamped \"dev\" was no " +
				"longer reachable by any upgrade, and could only be fixed on its own machine"},
		{"20260729.213000", "hors-build", true,
			"the current build-outside-the-pipeline default: same handling, and that's the point"},
		{"20260729.213000", "0.1.0", true,
			"a version some build tool placed there by mistake has no position in time"},
		{"20260729.213000", "", true,
			"an empty current isn't comparable either — the gate that rejects it " +
				"is elsewhere (upgradeOffer expects the proxy to identify itself)"},

		{"dev", "20260726.181502", false,
			"we never OFFER the unknown: that's the same rule already applied to an " +
				"empty target version"},
		{"", "20260726.181502", false, "empty target: nothing to offer"},
		{"latest", "dev", false,
			"two non-comparable values don't make an upgrade, otherwise a " +
				"badly packaged controller would push anything in a loop"},

		{"2026072.213000", "dev", false, "seven date digits: not a timestamp"},
		{"20260729.21300", "dev", false, "five time digits: not a timestamp"},
		{"20260729213000", "dev", false, "the dot is the separator, it's required"},
		{"20260729.213000 ", "dev", false,
			"a trailing space comes from a badly read file, not from a build"},
		{"v20260729.213000", "dev", false, "a prefix isn't part of the format"},
	}
	var c = cas[0]
	for _, c = range cas {
		var got = IsNewer(c.candidate, c.current)
		if got != c.attend {
			t.Errorf("IsNewer(%q, %q) = %v, expected %v — %s",
				c.candidate, c.current, got, c.attend, c.pourquoi)
		}
	}
}

// TestIsNewerEstAntisymetrique: two distinct timestamps cannot each be newer
// than the other.
//
// A consistency check, and it catches the mistake one would make by
// "fixing" this function too hastily — returning true as soon as the two
// differ would make a fleet oscillate between two versions, each heartbeat
// offering the other one.
func TestIsNewerEstAntisymetrique(t *testing.T) {
	var a, b = "20260726.181502", "20260729.213000"
	if IsNewer(a, b) && IsNewer(b, a) {
		t.Error("both directions are true: a fleet would oscillate between two versions")
	}
	if !IsNewer(b, a) {
		t.Error("the newer build must be offered")
	}
}
