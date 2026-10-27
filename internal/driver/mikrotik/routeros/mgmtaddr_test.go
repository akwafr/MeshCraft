package routeros

import (
	"testing"

	"meshcraft/internal/driver"
)

// Management addressing read, proven on REAL CAPTURES from the reference CRS326-24G-2S+.
//
// This test is also the OBSERVATION that justifies per-device addressing mode
// (migration 0027): it finds, on hardware, that a CRS326 adopted in the correct
// management subnet is on a FIXED address (`"dynamic":"false"`), while the site plan
// says 'external DHCP'. Same finding as on both CSS326 units, by a completely
// different read path.

// TestMgmtAddrReal: /ip/address as the device served it.
//
// What this test locks beyond mode: address CHOICE. The capture carries TWO active
// addresses, including factory address `192.168.88.1/24` marked `comment: defconf`.
// Retaining that one would show the wrong management network, exactly the error
// [foldMgmt] exists to avoid — hence the delegation, which this test proves by result.
func TestMgmtAddrReal(t *testing.T) {
	var got = MgmtAddrOf(fixture(t, "ip_address.json"), nil, nil)

	if got.Address != "192.0.2.54/24" {
		t.Errorf("address = %q, expected %q — factory address "+
			"192.168.88.1/24 (comment defconf) must NEVER win", got.Address, "192.0.2.54/24")
	}
	if got.Interface != "vlan99" {
		t.Errorf("interface = %q, expected %q", got.Interface, "vlan99")
	}
	if got.Mode != driver.MgmtAddrModeStatic {
		t.Errorf("mode = %q, expected %q: capture has "+
			"\"dynamic\":\"false\" on retained entry, and that is the whole point of the observation",
			got.Mode, driver.MgmtAddrModeStatic)
	}
}

// TestMgmtAddrDynamic: an address obtained by lease is rendered as DHCP.
//
// `dynamic` IS the field that answers the question: RouterOS marks addresses created by
// its DHCP client this way. Without it, there would be no way to distinguish a entered
// address from a leased one, and meshcraft would be reduced to believing the site plan.
func TestMgmtAddrDynamic(t *testing.T) {
	var addrs = []Row{{
		"address": "192.0.2.54/24", "interface": "vlan99",
		"actual-interface": "vlan99", "dynamic": "true",
		"disabled": "false", "invalid": "false",
	}}
	var got = MgmtAddrOf(addrs, nil, nil)
	if got.Mode != driver.MgmtAddrModeDHCP {
		t.Errorf("mode = %q, expected %q on a dynamic address", got.Mode, driver.MgmtAddrModeDHCP)
	}
}

// TestMgmtAddrGatewayAndDNS: ACTIVE default route and both resolver lists.
//
// Three traps in one test, each with its reason:
//
//   - an INACTIVE default route must not win. RouterOS willingly keeps several default
//     routes at different distances, only one of which is used; showing the backup route
//     as THE gateway would look for a routing problem where there is none;
//   - a route that is not the default route is not a management gateway;
//   - resolvers pushed by DHCP live in `dynamic-servers`, separately from those an
//     administrator entered. Reading only one would show 'no DNS' on a device that
//     resolves perfectly.
func TestMgmtAddrGatewayAndDNS(t *testing.T) {
	var addrs = []Row{{
		"address": "192.0.2.54/24", "actual-interface": "vlan99",
		"dynamic": "false", "disabled": "false", "invalid": "false",
	}}
	var routes = []Row{
		{"dst-address": "10.50.0.0/24", "gateway": "192.0.2.254", "inactive": "false"},
		{"dst-address": "0.0.0.0/0", "gateway": "192.0.2.9", "inactive": "true"},
		{"dst-address": "0.0.0.0/0", "gateway": "192.0.2.1", "inactive": "false"},
	}
	var dns = []Row{{"servers": "192.0.2.1", "dynamic-servers": "1.1.1.1,192.0.2.1"}}

	var got = MgmtAddrOf(addrs, routes, dns)
	if got.Gateway != "192.0.2.1" {
		t.Errorf("gateway = %q, expected %q: ACTIVE default route, "+
			"not backup route or specific route", got.Gateway, "192.0.2.1")
	}
	if len(got.DNS) != 2 || got.DNS[0] != "192.0.2.1" || got.DNS[1] != "1.1.1.1" {
		t.Errorf("DNS = %v, expected [192.0.2.1 1.1.1.1] — both lists merged, "+
			"deduplicated", got.DNS)
	}
}

// TestMgmtAddrWithoutGateway: no default route is a NORMAL state.
//
// A switch administered from its own subnet needs no gateway — that is even the case for
// all CSS326 units in the same fleet, which carry none at all. Returning anything other
// than empty would show an anomaly on a perfectly valid configuration.
func TestMgmtAddrWithoutGateway(t *testing.T) {
	var addrs = []Row{{
		"address": "192.0.2.54/24", "actual-interface": "vlan99",
		"dynamic": "false", "disabled": "false", "invalid": "false",
	}}
	var got = MgmtAddrOf(addrs, []Row{}, []Row{})
	if got.Gateway != "" {
		t.Errorf("gateway = %q on device with no default route", got.Gateway)
	}
	if got.DNS != nil {
		t.Errorf("DNS = %v, expected nil", got.DNS)
	}
}

// TestMgmtAddrNoAddress: no active address is not filled in, it is stated.
func TestMgmtAddrNoAddress(t *testing.T) {
	var addrs = []Row{{
		"address": "192.0.2.54/24", "disabled": "true", "invalid": "false",
	}}
	var got = MgmtAddrOf(addrs, nil, nil)
	if got.Address != "" || got.Mode != "" {
		t.Errorf("address = %q, mode = %q: a disabled entry proves nothing",
			got.Address, got.Mode)
	}
	if len(got.Notes) == 0 {
		t.Error("no note: absence of address must be STATED, not left silent")
	}
}
