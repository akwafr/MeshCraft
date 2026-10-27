package mdpv1

// Percent → packets per second.
//
// The one calculation in storm control whose wrong answer is silent: a limit
// computed a third too loose still applies, still shows as configured, and only
// fails on the day it was supposed to stop a storm.

import "testing"

// THE CLASSIC LINE RATES. 1.488 Mpps for gigabit at 64-byte frames is the number
// every vendor datasheet quotes, and it is the check that the wire overhead is
// accounted for: without the 20 bytes of preamble and interframe gap the answer
// is 1,953,125 — 31 % high, and every converted limit a third looser than asked.
func TestLineRateMatchesTheDatasheetFigures(t *testing.T) {
	var cases = []struct {
		speed int
		want  int
	}{
		{100, 148809},     // 100 Mbit/s
		{1000, 1488095},   // 1 Gbit/s — the canonical 1.488 Mpps
		{10000, 14880952}, // 10 Gbit/s
	}
	var c struct {
		speed int
		want  int
	}
	for _, c = range cases {
		var got, ok = StormPPSFromPercent(100, c.speed)
		if !ok {
			t.Fatalf("100%% of %d Mbit/s was refused", c.speed)
		}
		// Within one packet: the arithmetic is float and the answer is truncated.
		if got < c.want-1 || got > c.want+1 {
			t.Errorf("100%% of %d Mbit/s = %d pps, want ~%d", c.speed, got, c.want)
		}
	}
}

// The percentage scales linearly, which is what makes a "1 %" on one platform
// mean the same traffic share as a "1 %" on a platform that takes percent
// directly.
func TestPercentScalesLinearly(t *testing.T) {
	var full, _ = StormPPSFromPercent(100, 1000)
	var one, ok = StormPPSFromPercent(1, 1000)
	if !ok {
		t.Fatal("1% of a gigabit was refused")
	}
	if one < full/100-2 || one > full/100+2 {
		t.Errorf("1%% = %d pps, want ~%d (1/100 of %d)", one, full/100, full)
	}
}

// AN UNKNOWN SPEED IS REFUSED, never defaulted.
//
// The whole reason this conversion is allowed at all is that the missing term —
// the capacity — is SUPPLIED rather than guessed. Defaulting it would put the
// guess straight back, and the caller would never know which ports were computed
// from a real speed and which from an assumption.
func TestUnknownSpeedIsRefused(t *testing.T) {
	var speeds = []int{0, -1}
	var s int
	for _, s = range speeds {
		var _, ok = StormPPSFromPercent(10, s)
		if ok {
			t.Errorf("a percentage was converted against speed %d", s)
		}
	}
}

// Absurd percentages are refused rather than clamped: clamping 500%% to 100%%
// would write a limiter the operator did not ask for and report success.
func TestAbsurdPercentagesAreRefused(t *testing.T) {
	var bad = []float64{0, -5, 100.1, 1000}
	var p float64
	for _, p = range bad {
		var _, ok = StormPPSFromPercent(p, 1000)
		if ok {
			t.Errorf("%.1f%% was accepted", p)
		}
	}
}

// A share that rounds to less than one packet per second is REFUSED.
//
// Rounding up to 1 pps would be a limiter far tighter than asked for — on a
// 100 Mbit/s port that is 0.0007 % of line rate — and rounding to 0 is how most
// platforms spell "unlimited". Both are wrong in a direction the operator cannot
// see, so neither is chosen.
func TestASubPacketShareIsRefused(t *testing.T) {
	var _, ok = StormPPSFromPercent(0.0001, 100)
	if ok {
		t.Error("a share below one packet per second produced a rate")
	}
}

// Faceplate labels, as the connectors actually carry them.
func TestStormSpeedMbpsReadsFaceplateLabels(t *testing.T) {
	var good = map[string]int{
		"1G": 1000, "10G": 10000, "100M": 100, "40G": 40000,
		"2.5G": 2500, "1g": 1000, " 10G ": 10000,
	}
	var label string
	var want int
	for label, want = range good {
		var got, ok = StormSpeedMbps(label)
		if !ok || got != want {
			t.Errorf("StormSpeedMbps(%q) = %d,%v — want %d", label, got, ok, want)
		}
	}
	// An unrecognised label is a port whose capacity is UNKNOWN. Guessing a
	// default here would defeat the refusal in StormPPSFromPercent, which is the
	// only thing keeping an invented capacity out of a live limiter.
	var bad = []string{"", "fast", "1", "G", "auto", "-1G"}
	var s string
	for _, s = range bad {
		var _, ok = StormSpeedMbps(s)
		if ok {
			t.Errorf("StormSpeedMbps(%q) invented a speed", s)
		}
	}
}
