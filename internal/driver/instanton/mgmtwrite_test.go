package instanton

import "testing"

func TestMaskPrefixRoundTrip(t *testing.T) {
	var cases = []struct {
		prefix int
		mask   string
	}{
		{8, "255.0.0.0"},
		{16, "255.255.0.0"},
		{24, "255.255.255.0"},
		{32, "255.255.255.255"},
		{0, "0.0.0.0"},
	}
	var c struct {
		prefix int
		mask   string
	}
	for _, c = range cases {
		if prefixToMask(c.prefix) != c.mask {
			t.Errorf("prefixToMask(%d)=%q want %q", c.prefix, prefixToMask(c.prefix), c.mask)
		}
		if maskToPrefix(c.mask) != c.prefix {
			t.Errorf("maskToPrefix(%q)=%d want %d", c.mask, maskToPrefix(c.mask), c.prefix)
		}
	}
}

func TestSplitCIDR(t *testing.T) {
	var host, mask = splitCIDR("10.99.200.199/16")
	if host != "10.99.200.199" || mask != "255.255.0.0" {
		t.Fatalf("got %q %q", host, mask)
	}
	host, mask = splitCIDR("10.99.200.199")
	if host != "10.99.200.199" || mask != "" {
		t.Fatalf("got %q %q", host, mask)
	}
}

func TestFormatIOAddr(t *testing.T) {
	if formatIOAddr("10.99.200.101", "255.255.0.0") != "10.99.200.101/16" {
		t.Fatal(formatIOAddr("10.99.200.101", "255.255.0.0"))
	}
}

func TestDefaultRouteOf(t *testing.T) {
	var xml = `<IPv4RouteList><Entry>
<destinationIPv4Address>0.0.0.0</destinationIPv4Address>
<destinationPrefixLength>0</destinationPrefixLength>
<nextHopIPv4Address>10.99.0.1</nextHopIPv4Address>
</Entry></IPv4RouteList>`
	if defaultRouteOf(xml) != "10.99.0.1" {
		t.Fatal(defaultRouteOf(xml))
	}
}
