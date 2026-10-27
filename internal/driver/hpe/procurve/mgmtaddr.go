package procurve

// Management addressing: reading it, and writing it on the SAME VLAN.
//
// # Where the address lives on this range
//
// Under the VLAN, and the two views that describe it disagree about how to name
// that VLAN. CAPTURED on SW-PLAYER-79 (2026-08-03):
//
//	# show ip
//	  VLAN                 | IP Config  IP Address      Subnet Mask
//	  -------------------- + ---------- --------------- ---------------
//	  Default              | DHCP/Bootp
//	  LAN_Management       | Manual     203.0.113.79    255.255.255.0
//
//	# show running-config
//	vlan 99
//	   name "LAN_Management"
//	   ip address 203.0.113.79 255.255.255.0
//
// "show ip" keys its rows by NAME and never prints an id; the configuration
// keys by id. So the status view is authoritative for the MODE — it is the only
// place that distinguishes a manual address from a lease — and the two are
// joined through the VLAN name. A connector that read only the configuration
// would report "ip address dhcp-bootp" as an address.
//
// # The address is written as IP + MASK, never as a prefix
//
// meshcraft carries "203.0.113.79/24" because that is what an operator types.
// This CLI takes "ip address 203.0.113.79 255.255.255.0" and refuses the
// prefix form outright. The conversion is [driver.SplitAddrMask], shared with
// the Cisco and FS connectors so that one notation reaches every platform.

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"meshcraft/internal/driver"
)

// ReadMgmtAddressing reports how this device obtained its address.
func (c *Client) ReadMgmtAddressing(ctx context.Context) (driver.MgmtAddrState, error) {
	var out driver.MgmtAddrState
	if c.cli == nil {
		return out, errNoSession
	}
	var ipText, err = c.cli.run(ctx, "show ip", cmdTimeout)
	if err != nil && strings.TrimSpace(ipText) == "" {
		return out, fmt.Errorf("show ip on %s: %v", c.tgt.Host, firstLine(err.Error()))
	}
	var vlanText string
	vlanText, err = c.cli.run(ctx, "show vlans", cmdTimeout)
	if err != nil && strings.TrimSpace(vlanText) == "" {
		return out, fmt.Errorf("show vlans on %s: %v", c.tgt.Host, firstLine(err.Error()))
	}
	return mgmtAddrFrom(parseIPService(ipText), vlanNames(vlanText)), nil
}

// mgmtAddrFrom joins the two views.
//
// THE VLAN THAT CARRIES AN ADDRESS WINS, and a lease on VLAN 1 does not count as
// one. "Default | DHCP/Bootp" with no address is the factory state of every
// unit of this range: naming it the management interface would point
// provisioning at a VLAN the switch is not reachable on.
func mgmtAddrFrom(ip ipService, names map[int]string) driver.MgmtAddrState {
	var out driver.MgmtAddrState
	out.Gateway = ip.Gateway

	var byName = map[string]int{}
	var id int
	var name string
	for id, name = range names {
		byName[name] = id
	}

	var best int
	var bestRow ipRow
	var row ipRow
	for name, row = range ip.ByVLANName {
		if row.Address == "" {
			continue
		}
		var vid = byName[name]
		if best == 0 || vid < best {
			best, bestRow = vid, row
		}
	}
	if best == 0 {
		// No VLAN carries an address. Report the lease if one is even configured,
		// so the caller can tell "waiting for DHCP" from "no addressing at all".
		for name, row = range ip.ByVLANName {
			if strings.HasPrefix(strings.ToLower(row.Config), "dhcp") {
				out.Mode = driver.MgmtAddrModeDHCP
				out.Interface = "vlan" + strconv.Itoa(byName[name])
				out.Notes = append(out.Notes,
					"VLAN \""+name+"\" is set to DHCP/Bootp and carries no address yet")
				return out
			}
		}
		out.Notes = append(out.Notes,
			"no VLAN on this switch carries a management address")
		return out
	}

	out.Interface = "vlan" + strconv.Itoa(best)
	if strings.HasPrefix(strings.ToLower(bestRow.Config), "dhcp") {
		out.Mode = driver.MgmtAddrModeDHCP
	} else {
		out.Mode = driver.MgmtAddrModeStatic
	}
	// The prefix form is what meshcraft carries everywhere else; the mask is what
	// the device printed.
	var cidr, err = driver.JoinAddrMask(bestRow.Address, bestRow.Mask)
	if err != nil {
		out.Address = bestRow.Address
		out.Notes = append(out.Notes, "mask \""+bestRow.Mask+"\" was not understood: "+
			firstLine(err.Error()))
	} else {
		out.Address = cidr
	}
	// DNS is NOT read, and that is a platform fact rather than a gap: this range
	// resolves nothing on its own behalf and "show ip" carries no resolver.
	out.Notes = append(out.Notes,
		"this range carries no DNS resolver of its own: the field is empty because "+
			"there is nothing to read, not because it is unset")
	return out
}

// ApplyMgmtAddressing writes the address on the CURRENT management VLAN.
//
// # This gesture moves the ground under the session that commands it
//
// There is no second path on this range. Changing the address the CLI is
// connected through cuts that session at the moment the command takes, and the
// switch is then reachable only at the new address — or, if the new address is
// wrong, only from the serial console. Two consequences are built in here:
//
//   - the VLAN is NEVER changed. That is [driver.MgmtMover]'s job and it needs a
//     dual path this platform does not have;
//   - "write memory" runs BEFORE the address is announced as applied, in the
//     same block. A new address that a reboot would forget puts the switch back
//     on an address meshcraft has already stopped using.
//
// DOCUMENTED, not captured: no address change has been run on this range. The
// command form itself IS captured — "ip address 203.0.113.79 255.255.255.0"
// appears verbatim in the reference unit's configuration — which is why the
// gesture is implemented; what has not been observed is the switch surviving it,
// and that is why [driver.MgmtCapabilities.ApplySameVLAN] stays false.
func (c *Client) ApplyMgmtAddressing(ctx context.Context,
	want driver.MgmtAddrIntent) (driver.MgmtAddrResult, error) {
	var out driver.MgmtAddrResult
	if c.cli == nil {
		return out, errNoSession
	}

	var current, err = c.ReadMgmtAddressing(ctx)
	if err != nil {
		return out, fmt.Errorf("reading management addressing before writing: %v", err)
	}
	var vlanID, ok = vlanIDOf(current.Interface)
	if !ok {
		return out, fmt.Errorf("this switch carries no management VLAN to write on: " +
			"an address must be placed on a VLAN, and none has one")
	}

	var cmds = []string{"vlan " + strconv.Itoa(vlanID)}
	switch strings.ToLower(strings.TrimSpace(want.Mode)) {
	case driver.MgmtAddrModeDHCP:
		cmds = append(cmds, "ip address dhcp-bootp")
	case driver.MgmtAddrModeStatic, "":
		var ip, mask, _, serr = driver.SplitAddrMask(want.Address)
		if serr != nil {
			return out, fmt.Errorf("management address %q: %v", want.Address, serr)
		}
		cmds = append(cmds, "ip address "+ip+" "+mask)
	default:
		return out, fmt.Errorf("addressing mode %q is not one this range has: "+
			"static or dhcp", want.Mode)
	}
	cmds = append(cmds, "exit")

	// The gateway is GLOBAL on this platform, not per-VLAN: "ip default-gateway"
	// sits at the top level of the configuration (CAPTURED). Writing it inside the
	// VLAN stanza is refused.
	if strings.TrimSpace(want.Gateway) != "" && want.Gateway != current.Gateway {
		cmds = append(cmds, "ip default-gateway "+strings.TrimSpace(want.Gateway))
	}
	if len(want.DNS) > 0 {
		out.Notes = append(out.Notes,
			"DNS servers not written: this range has no resolver to configure")
	}

	if err = c.runConfigBlock(ctx, cmds); err != nil {
		c.step(false, "management address NOT written", firstLine(err.Error()))
		return out, fmt.Errorf("management addressing on %s: %v", c.tgt.Host,
			firstLine(err.Error()))
	}
	c.step(true, "management addressing written", strings.Join(cmds, " ; "))

	// Persist FIRST (cf. the header): the session may not survive long enough to
	// do it afterwards.
	if err = c.persist(ctx); err != nil {
		out.Notes = append(out.Notes,
			"address applied but NOT saved: a reboot would bring the switch back to "+
				"the previous address ("+firstLine(err.Error())+")")
	}

	// The read-back is best-effort BY DESIGN: on a genuine address change the
	// session is already gone, and failing here would report a successful write as
	// an error.
	var after, rerr = c.ReadMgmtAddressing(ctx)
	if rerr != nil {
		out.Notes = append(out.Notes,
			"the new addressing could not be re-read from this session, which is the "+
				"expected outcome when the address actually moved: verify at "+want.Address)
		return out, nil
	}
	out.Observed = after
	return out, nil
}

// vlanIDOf reads the id back out of the "vlanN" interface name this connector
// builds.
func vlanIDOf(iface string) (int, bool) {
	var digits = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(iface)), "vlan")
	var n, err = strconv.Atoi(digits)
	if err != nil || n < 1 || n > 4094 {
		return 0, false
	}
	return n, true
}
