package routeros

import (
	"context"
	"strings"

	"meshcraft/internal/driver"
)

// Reading RouterOS management addressing: by what means this device obtained the
// address meshcraft talks to it on.
//
// Writes live in mgmtwrite.go ([driver.MgmtAddrWriter], [driver.MgmtMover]).
// This file only reads `/ip/address`, `/ip/route`, and `/ip/dns`.
//
// # THE OBSERVATION THAT MAKES THIS READ POSSIBLE: `dynamic`
//
// `/ip/address` carries, for each address, a `dynamic` field — and that is exactly the
// answer to the question asked. An address obtained by DHCP lease is created by the DHCP
// client, so `dynamic: "true"`; an address entered by an administrator is
// `dynamic: "false"`. Both values are in the real capture from the reference device
// (testdata/ip_address.json):
//
//	192.168.88.1/24 on bridge, comment defconf, dynamic false
//	10.99.0.54/24   on vlan99,                  dynamic false
//
// This CRS326 is therefore on a STATIC address, in the site's management subnet, on a
// site whose plan says 'external DHCP'. Same finding as on both CSS326 units, and the
// case that motivated per-device addressing mode
// (cf. docs/01-Architecture/Adressage de management, migration 0027).
//
// # WHAT IS OBSERVED AND WHAT IS NOT
//
// Address and its dynamic character: OBSERVED, capture attached. Gateway (`/ip/route`)
// and resolvers (`/ip/dns`): NOT OBSERVED on this device — the exploration campaign did
// not capture those two menus. Their field names come from RouterOS documentation, a
// strong source but not an observation. The consequence is held here: their read is
// OPTIONAL, failure does not fail the call, and absence is stated in a note rather than
// rendered as 'not configured', which would be an assertion.

// Compile-time check when the contract moves (cf. contract.go).
var _ driver.MgmtAddrReader = (*Client)(nil)

// ReadMgmtAddressing reports how this device obtained its administration address.
//
// THREE reads at most, and only one is REQUIRED. `/ip/address` carries the answer to
// the question; `/ip/route` and `/ip/dns` carry display complement only, and their
// failure must not deprive the operator of the essentials. On a platform where each
// request costs two HTTP round trips (cf. [Driver.Authenticate]), this also bounds the
// cost of a device detail screen to what it brings.
func (c *Client) ReadMgmtAddressing(ctx context.Context) (driver.MgmtAddrState, error) {
	var addrs, err = c.List(ctx, "ip/address")
	if err != nil {
		return driver.MgmtAddrState{}, err
	}

	// Gateway and resolvers are OPTIONAL: cf. file header. Their error is recorded in a
	// note, not returned — otherwise a missing menu on a given firmware would also erase
	// addressing mode, the only thing this screen exists to say.
	var routes, routeErr = c.List(ctx, "ip/route")
	var dns, dnsErr = c.List(ctx, "ip/dns")

	var out = MgmtAddrOf(addrs, routes, dns)
	if routeErr != nil {
		out.Notes = append(out.Notes, "gateway not readable: /ip/route refused read ("+
			routeErr.Error()+"). Addressing mode and address, however, are from the device.")
	}
	if dnsErr != nil {
		out.Notes = append(out.Notes, "resolvers not readable: /ip/dns refused read ("+
			dnsErr.Error()+").")
	}
	c.step(true, "management addressing re-read",
		"/ip/address, /ip/route and /ip/dns: mode '"+addrModeLabel(out.Mode)+
			"', address "+orUnknown(out.Address))
	return out, nil
}

// MgmtAddrOf translates raw records into observed management addressing.
//
// PURE, like [foldMgmt] and for the same reason: a deduction the screen uses to assert
// something about a device in service configuration, replayable on real captures.
//
// ADDRESS CHOICE IS NOT REDONE HERE: it is DELEGATED to [foldMgmt], the function state
// read already uses. Not convenience — the only way to avoid two different answers to
// the same question in one binary — a gap between 'the address that matters on the detail
// screen' and 'the address that matters for apply guardrails' would only show the day
// one protects a VLAN the other does not designate. The `comment: defconf` sort that
// drops factory address `192.168.88.1` is already there and need not be copied.
func MgmtAddrOf(addrs, routes, dns []Row) driver.MgmtAddrState {
	var out driver.MgmtAddrState

	// `nil` for VLAN interfaces: we are not looking for management VLAN here, State
	// already carries it. What we borrow from foldMgmt is its address CHOICE.
	var picked DeviceState
	foldMgmt(addrs, nil, &picked)
	out.Address = picked.MgmtAddress
	out.Interface = picked.MgmtInterface

	if out.Address == "" {
		out.Notes = append(out.Notes, "no active IPv4 address on this device: "+
			"/ip/address carries only disabled or invalid entries, or nothing at all.")
		return out
	}

	// Mode is read on the RETAINED line, and no other. A device can carry both a static
	// address and a lease on another interface; the mode to display is that of the
	// address through which meshcraft talks to it.
	var row = rowByAddress(addrs, out.Address)
	if row == nil {
		return out
	}
	if row.Bool("dynamic") {
		out.Mode = driver.MgmtAddrModeDHCP
	} else {
		out.Mode = driver.MgmtAddrModeStatic
	}

	out.Gateway = defaultGateway(routes)
	out.DNS = dnsServers(dns)
	return out
}

// rowByAddress finds the record for an already chosen address.
//
// By address not `.id`: `.id` is RouterOS data the contract does not carry (cf.
// contract.go), and the address is unique in this menu — two entries cannot carry the
// same prefix on the same interface.
func rowByAddress(addrs []Row, address string) Row {
	var r Row
	for _, r = range addrs {
		if r.Get("address") == address {
			return r
		}
	}
	return nil
}

// defaultGateway returns the gateway of the ACTIVE default route.
//
// 'Active' matters: RouterOS willingly keeps several default routes at different
// distances, only one of which is used. Showing a backup route as THE management gateway
// would look for a routing problem where there is none.
//
// Returns an empty string when there is none, a NORMAL state not a read defect: a switch
// administered from its own subnet needs no gateway. That is even the case for CSS326
// units in the same fleet, which carry none at all (cf. swos, MgmtCapabilities.Gateway =
// false).
func defaultGateway(routes []Row) string {
	var r Row
	for _, r = range routes {
		if r.Get("dst-address") != "0.0.0.0/0" {
			continue
		}
		if r.Bool("disabled") || r.Bool("inactive") {
			continue
		}
		var gw = r.Get("gateway")
		if gw != "" {
			return gw
		}
	}
	return ""
}

// dnsServers returns declared resolvers, including those obtained by DHCP.
//
// Both fields not just `servers`: RouterOS stores resolvers pushed by a DHCP lease in
// `dynamic-servers`, separately from those an administrator entered. Reading only one
// would show 'no DNS' on a device that resolves perfectly — and conclude incomplete
// configuration.
func dnsServers(dns []Row) []string {
	var out []string
	var r Row
	for _, r = range dns {
		out = appendSplit(out, r.Get("servers"))
		out = appendSplit(out, r.Get("dynamic-servers"))
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// appendSplit adds comma-separated list elements, deduplicated and skipping empties.
// RouterOS serves these fields as one string ('10.99.0.1,1.1.1.1').
func appendSplit(out []string, csv string) []string {
	if csv == "" {
		return out
	}
	var part string
	for _, part = range strings.Split(csv, ",") {
		part = strings.TrimSpace(part)
		if part == "" || contains(out, part) {
			continue
		}
		out = append(out, part)
	}
	return out
}

func contains(list []string, s string) bool {
	var v string
	for _, v = range list {
		if v == s {
			return true
		}
	}
	return false
}

func addrModeLabel(mode string) string {
	switch mode {
	case driver.MgmtAddrModeStatic:
		return "static"
	case driver.MgmtAddrModeDHCP:
		return "DHCP"
	}
	return "not observed"
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}
