package mdpv1

import "testing"

// A mask off by one bit configures cleanly, answers ping from its own subnet,
// and only fails the day something routes to the switch. So every form the UI
// can hand a connector is pinned here.
func TestSplitAddrMask(t *testing.T) {
	var cases = []struct {
		in, ip, mask string
		bits         int
	}{
		{"192.0.2.36/24", "192.0.2.36", "255.255.255.0", 24},
		{"192.0.2.3/16", "192.0.2.3", "255.255.0.0", 16},
		{"192.168.1.10/22", "192.168.1.10", "255.255.252.0", 22},
		{"172.16.5.4/32", "172.16.5.4", "255.255.255.255", 32},
		// Already split — a caller that did the work is not punished for it.
		{"192.0.2.3 255.255.0.0", "192.0.2.3", "255.255.0.0", 16},
		{"  10.0.0.1/8  ", "10.0.0.1", "255.0.0.0", 8},
	}
	var c = cases[0]
	for _, c = range cases {
		var ip, mask, bits, err = SplitAddrMask(c.in)
		if err != nil {
			t.Errorf("SplitAddrMask(%q): %v", c.in, err)
			continue
		}
		if ip != c.ip || mask != c.mask || bits != c.bits {
			t.Errorf("SplitAddrMask(%q) = %q %q /%d — want %q %q /%d",
				c.in, ip, mask, bits, c.ip, c.mask, c.bits)
		}
	}
}

func TestSplitAddrMaskRefusesWhatItCannotKnow(t *testing.T) {
	var bad = []string{
		"",
		// A bare address: assuming /24 is how a /22 network gets a switch that
		// can only see a quarter of itself.
		"192.0.2.36",
		"192.0.2.36/33",
		"192.0.2.36/-1",
		"not-an-ip/24",
		"192.0.2.36 255.255.0.1", // non-contiguous mask
		"2001:db8::1/64",         // IPv6 is not what these CLIs take here
	}
	var s string
	for _, s = range bad {
		var _, _, _, err = SplitAddrMask(s)
		if err == nil {
			t.Errorf("SplitAddrMask(%q) must refuse rather than guess", s)
		}
	}
}

func TestJoinAddrMaskRoundTrips(t *testing.T) {
	var got, err = JoinAddrMask("192.0.2.3", "255.255.0.0")
	if err != nil || got != "192.0.2.3/16" {
		t.Fatalf("JoinAddrMask = %q, %v", got, err)
	}
	var ip, mask, _, serr = SplitAddrMask(got)
	if serr != nil || ip != "192.0.2.3" || mask != "255.255.0.0" {
		t.Fatalf("round trip: %q %q %v", ip, mask, serr)
	}
}
