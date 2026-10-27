package routeros

import (
	"context"
	"fmt"

	"meshcraft/internal/driver"
)

var _ driver.DHCPSnoopReader = (*Client)(nil)

// DHCP snooping on RouterOS, and the SHAPE DIFFERENCE that makes this platform
// worth reading twice before writing anything.
//
// # The contract's three levels do not map one-for-one, and one of them is MISSING
//
// internal/driver/dhcpsnoop.go describes snooping as three levels: a switch-wide
// enable, a per-VLAN enable, and a per-port TRUST flag. That is the CLI vendors'
// shape (`ip dhcp snooping`, `ip dhcp snooping vlan 20`, `ip dhcp snooping trust`).
// RouterOS does not have it. What it has, OBSERVED on the reference CRS326-24G-2S+
// running 7.21beta3 and captured in testdata/:
//
//	/interface/bridge       → "dhcp-snooping":"false"   ← the enable
//	/interface/bridge/port  → "trusted":"false"          ← the per-port trust
//
// Three consequences, in decreasing order of how badly they mislead a caller who
// assumes the CLI shape:
//
//  1. THE ENABLE IS A BRIDGE PROPERTY, not a box-wide statement. A device with two
//     bridges can inspect on one and not on the other, and the answer to "is
//     snooping on?" is only true OF A BRIDGE. [DHCPSnoopState.Enabled] therefore
//     reports the bridge this connector reasons about — the one that filters VLANs,
//     cf. pickBridge — and a Note names it, so nobody reads a single boolean as a
//     property of the chassis.
//
//  2. THERE IS NO PER-VLAN DIMENSION AT ALL. Not "not read yet", not "on another
//     menu": `/interface/bridge` carries one `dhcp-snooping` field and no VLAN list
//     anywhere near it, and `/interface/bridge/vlan` rows (captured) carry
//     `vlan-ids`, `tagged`, `untagged`, `mvrp-forbidden` and nothing about DHCP.
//     [DHCPSnoopState.VLANs] is therefore EMPTY on this platform in both directions,
//     and that empty list means "the dimension does not exist here", NOT "no VLAN is
//     covered" — the exact misreading the contract warns about. It is the Note that
//     carries the difference, and dropping the Note would turn a fully protected
//     bridge into a screen saying snooping covers nothing.
//
//  3. TRUST LIVES ON THE BRIDGE PORT, not on the interface. A physical port that is
//     not a member of the bridge has no `trusted` field to read or write — it is not
//     switched by the bridge, so there is nothing there for snooping to inspect.
//     Such a port is ABSENT from [DHCPSnoopState.Ports] rather than reported
//     untrusted, per the contract's own rule: "a port absent from this list was not
//     observed, which is not the same as untrusted."
//
// # What is NOT read here, and why
//
// The binding table (`/ip/dhcp-server/binding` and the bridge's learned entries) is
// telemetry and belongs to a monitoring path, exactly as the contract says. Option-82
// insertion did NOT appear as a field on the captured `/interface/bridge` object on
// 7.21beta3, so this connector claims nothing about it in either direction — an
// absence in one capture of one firmware is not a platform fact, and inventing a Note
// about it would be the kind of claim this package refuses to make.

// pickBridge returns the bridge this connector reasons about.
//
// The one that FILTERS, if any: it is the one carrying segmentation. Otherwise the
// first — a device with several non-filtering bridges has no VLAN plan to compare
// against anyway.
//
// A FUNCTION and not four inline lines, because the choice must be the SAME
// everywhere. [Client.ReadDeviceState] picks the bridge whose ports feed the apply
// guardrails, and DHCP snooping is a property OF A BRIDGE (cf. the file header): two
// readers disagreeing about which bridge they mean would produce a screen where the
// trust flags belong to one switching table and the enable flag to another, with
// nothing on it saying so.
//
// Callers must have checked that `bridges` is non-empty: there is no bridge to
// invent, and returning a zero Row would let the caller write to an empty `.id`.
func pickBridge(bridges []Row) Row {
	var b Row
	for _, b = range bridges {
		if b.Bool("vlan-filtering") {
			return b
		}
	}
	return bridges[0]
}

// ReadDHCPSnooping reports what the device says about DHCP snooping.
//
// READ ONLY: replayable as often as needed, including on a production fleet. Two
// GETs — `/interface/bridge` for the enable, `/interface/bridge/port` for the trust
// flags — both of which this connector already makes elsewhere and whose captures
// are in testdata/.
func (c *Client) ReadDHCPSnooping(ctx context.Context) (driver.DHCPSnoopState, error) {
	var out driver.DHCPSnoopState
	var bridges, err = c.List(ctx, "interface/bridge")
	if err != nil {
		return out, fmt.Errorf("list interface/bridge: %w", err)
	}
	if len(bridges) == 0 {
		// No bridge means nothing is switched by this device, so there is no traffic
		// for snooping to inspect. Reporting Enabled=false with no explanation would
		// send an operator looking for a checkbox that is not merely off — it has
		// nothing to act on.
		out.Notes = append(out.Notes,
			"No bridge on this device: DHCP snooping is a BRIDGE property on RouterOS, "+
				"so there is nothing to enable and no port that could carry a trust flag.")
		return out, nil
	}
	var ports []Row
	if ports, err = c.List(ctx, "interface/bridge/port"); err != nil {
		return out, fmt.Errorf("list interface/bridge/port: %w", err)
	}
	out = foldDHCPSnoop(pickBridge(bridges), ports, len(bridges))
	c.step(true, "DHCP snooping read",
		fmt.Sprintf("bridge dhcp-snooping=%v, %d port(s) with a trust flag",
			out.Enabled, len(out.Ports)))
	return out, nil
}

// foldDHCPSnoop turns the two captured menus into the contract's view.
//
// PURE, and that is what lets the tests replay the reference device's real capture
// instead of a device: everything decided here is decided before a byte moves, and a
// mistake in it would show on screen as protection that is not there.
//
// PORT ORDER IS THE DEVICE'S, deliberately not alphabetical: `/interface/bridge/port`
// comes back in faceplate order (ether1…ether24, sfp-sfpplus1, sfp-sfpplus2 —
// observed), and sorting those names as strings would put ether10 between ether1 and
// ether2. A list an operator reads against a physical panel must follow the panel.
func foldDHCPSnoop(br Row, portRows []Row, bridgeCount int) driver.DHCPSnoopState {
	var out driver.DHCPSnoopState
	out.Enabled = br.Bool("dhcp-snooping")

	var bridgeName = br.Get("name")
	var p Row
	for _, p = range portRows {
		// Same filter as stp.go: a row naming another bridge belongs to another
		// switching table, and its trust flag says nothing about this one.
		if p.Get("bridge") != "" && bridgeName != "" && p.Get("bridge") != bridgeName {
			continue
		}
		var name = p.Get("interface")
		if name == "" {
			continue
		}
		// The field is read, not defaulted: a firmware that did not serve `trusted`
		// at all would make Row.Bool answer false, which is the SAFE reading (untrusted
		// is the protected state) and the one the device itself defaults to — observed
		// "trusted":"false" on all 26 bridge ports of the reference capture.
		out.Ports = append(out.Ports, driver.DHCPSnoopPortState{
			Port:    name,
			Trusted: p.Bool("trusted"),
		})
	}

	// THE NOTE THAT MUST NOT BE DROPPED. VLANs stays nil above, and the contract is
	// explicit that a caller may read an empty list as "no VLAN is covered". On this
	// platform that reading is exactly backwards.
	out.Notes = append(out.Notes,
		"RouterOS has NO per-VLAN DHCP snooping: the empty VLAN list means the "+
			"platform does not have that level, not that no VLAN is protected. When the "+
			"bridge feature is on, it inspects every VLAN that bridge switches.")

	if bridgeName != "" {
		out.Notes = append(out.Notes, fmt.Sprintf(
			"Snooping is enabled per BRIDGE on RouterOS, not per chassis: what is "+
				"reported here is bridge %q.", bridgeName))
	}
	if bridgeCount > 1 {
		out.Notes = append(out.Notes, fmt.Sprintf(
			"This device carries %d bridges. Only %q was read — ports belonging to the "+
				"others are absent from the list above, which is not the same as untrusted.",
			bridgeCount, bridgeName))
	}
	if !out.Enabled {
		out.Notes = append(out.Notes,
			"The bridge feature is OFF: every trust flag above is INERT. Nothing is "+
				"inspected and no DHCP reply is dropped, whatever a port says.")
	}
	// Ports that are not bridge members carry no trust flag at all. Said once, here,
	// rather than left for a caller to deduce from a shorter list than the faceplate.
	out.Notes = append(out.Notes,
		"Trust is a BRIDGE PORT property (`/interface/bridge/port` `trusted`): a "+
			"physical port that is not a member of the bridge has no trust flag, and is "+
			"absent from this list rather than reported untrusted.")
	return out
}
