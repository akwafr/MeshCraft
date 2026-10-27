package ouidb

import "testing"

// Assignments checked against the very MACs the lab's FDB serves today, so a
// wrong column on the Clients page is caught here and not on screen.
func TestVendorKnowsTheLabsRealCards(t *testing.T) {
	var cases = []struct{ mac, wantSub string }{
		// core01.lab's chassis — an Arista switch.
		{"28:99:3a:9d:b7:65", "Arista"},
		// The CRS305's bridge MAC — MikroTik registers as "Routerboard.com",
		// which is itself a fact the page will show and this test pins.
		{"D4:01:C3:AB:32:F4", "Routerboard"},
		// Cisco's dotted spelling, as "show mac address-table" prints it.
		{"bcc4.93e5.7f9c", "Cisco"},
	}
	var c struct{ mac, wantSub string }
	for _, c = range cases {
		var name, known = Vendor(c.mac)
		if !known {
			t.Errorf("Vendor(%q) unknown, want a %s entry", c.mac, c.wantSub)
			continue
		}
		if !containsFold(name, c.wantSub) {
			t.Errorf("Vendor(%q) = %q, want it to name %s", c.mac, name, c.wantSub)
		}
	}
}

func TestRandomizedAddressesAreNamedNotBlank(t *testing.T) {
	// x2 in the first byte: U/L bit set — every phone OS's privacy default.
	if !LocallyAdministered("d2:00:11:22:33:44") {
		t.Error("U/L bit not recognised")
	}
	if LocallyAdministered("d0:00:11:22:33:44") {
		t.Error("a globally-unique address read as randomized")
	}
	var _, known = Vendor("d2:00:11:22:33:44")
	if known {
		t.Error("a locally-administered address cannot have a registry entry")
	}
}

func containsFold(s, sub string) bool {
	var i int
	for i = 0; i+len(sub) <= len(s); i++ {
		var ok = true
		var j int
		for j = 0; j < len(sub); j++ {
			var a, b = s[i+j], sub[j]
			if a >= 'A' && a <= 'Z' {
				a += 32
			}
			if b >= 'A' && b <= 'Z' {
				b += 32
			}
			if a != b {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}
