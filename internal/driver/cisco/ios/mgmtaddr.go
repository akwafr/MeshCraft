package ios

// Management addressing — the WRITE side.
//
// The read side lives in state.go and has been there since this connector was
// born; what was missing is the gesture that changes the address. It is
// deliberately narrow:
//
//   - it re-addresses the SVI THAT ALREADY CARRIES MANAGEMENT, and never
//     another one. Moving management to a different VLAN is [driver.MgmtMover]
//     / [driver.MgmtJumper], a different contract with a dual path and a
//     backup, and pretending to do it from here would strand switches;
//   - it refuses an intent it cannot express exactly, rather than applying the
//     half of it that fits.
//
// # The address form, which is where this goes wrong quietly
//
// meshcraft carries the address as a PREFIX ("192.0.2.36/24"). IOS does not
// take that: "ip address" wants two arguments and a DOTTED mask. The
// conversion is [driver.SplitAddrMask], shared with the FS connectors that
// have the same need — a mask off by one bit configures cleanly, answers ping
// from its own subnet, and only fails the day something routes to it.
//
// # A 2960-X has no routing table
//
// The gateway is "ip default-gateway", NOT "ip route 0.0.0.0". On a switch
// running "ip routing" the two differ, and the default-gateway line is then
// ignored — so the fact is reported rather than silently assumed.

import (
	"context"
	"fmt"
	"strings"

	"meshcraft/internal/driver"
)

// ApplyMgmtAddressing writes the management address on the CURRENT management
// VLAN.
//
// The session is cut by its own success when the address it came in through
// moves — that is inherent, not a defect, and the note says so. The caller
// verifies through the NEW address.
func (c *Client) ApplyMgmtAddressing(ctx context.Context,
	want driver.MgmtAddrIntent) (driver.MgmtAddrResult, error) {

	var res driver.MgmtAddrResult
	var err = c.requireWritableDialect()
	if err != nil {
		return res, err
	}

	// Which SVI carries management RIGHT NOW. Read, never assumed: writing an
	// address onto a guessed interface is how a switch goes quiet.
	var before driver.MgmtAddrState
	before, err = c.ReadMgmtAddressing(ctx)
	if err != nil {
		return res, fmt.Errorf("reading management addressing before writing: %w", err)
	}
	var svi = strings.TrimSpace(before.Interface)
	if svi == "" {
		return res, driver.NotSupported("cisco-ios", "change the management address",
			"no SVI on this device carries an address that is administratively up, so "+
				"there is nothing to re-address. Management is reaching it another way "+
				"(console, or an SVI left shut down), and choosing an interface here "+
				"would be guessing which one should become the management path")
	}

	var cmds []string
	switch strings.ToLower(strings.TrimSpace(want.Mode)) {
	case driver.MgmtAddrModeDHCP:
		cmds = []string{
			"interface " + svi,
			"ip address dhcp",
			// An SVI that was shut down stays shut down through a re-address:
			// the lease would be taken and the interface still dark.
			"no shutdown",
			"exit",
		}
	case driver.MgmtAddrModeStatic, "":
		var ip, mask, _, splitErr = driver.SplitAddrMask(want.Address)
		if splitErr != nil {
			return res, fmt.Errorf("management address: %w", splitErr)
		}
		cmds = []string{
			"interface " + svi,
			fmt.Sprintf("ip address %s %s", ip, mask),
			"no shutdown",
			"exit",
		}
	default:
		return res, fmt.Errorf("management mode %q is neither static nor dhcp", want.Mode)
	}

	// Gateway and DNS are global on IOS, outside the interface block.
	var gw = strings.TrimSpace(want.Gateway)
	if gw != "" {
		cmds = append(cmds, "ip default-gateway "+gw)
	}
	if len(want.DNS) > 0 {
		var servers []string
		var d string
		for _, d = range want.DNS {
			d = strings.TrimSpace(d)
			if d != "" {
				servers = append(servers, d)
			}
		}
		if len(servers) > 0 {
			// One line carries the whole list on IOS; sending them separately
			// APPENDS, which would leave a decommissioned resolver in place.
			cmds = append(cmds, "ip name-server "+strings.Join(servers, " "))
		}
	}

	// The command that moves the address is the one that can cut this session.
	// Everything is sent as ONE block so a severed session cannot leave the
	// gateway pointing at the old network.
	err = c.runConfigBlock(ctx, cmds)
	if err != nil {
		c.step(false, "management addressing refused", firstLine(err.Error()))
		return res, fmt.Errorf("writing management addressing on %s: %w", c.host, err)
	}
	c.step(true, "management addressing written on "+svi, strings.Join(cmds, " ; "))

	res.Notes = append(res.Notes,
		"applied on "+svi+" — \"write memory\" is what makes it survive a reload",
		"the address this session came in through has just moved: verify through the "+
			"NEW address, not this one")
	if gw != "" {
		res.Notes = append(res.Notes,
			"gateway written as \"ip default-gateway\", which a switch running \"ip "+
				"routing\" IGNORES in favour of its routing table — check \"show ip route\" "+
				"if off-subnet traffic does not leave")
	}

	// Re-read when the session survived. It usually has not, and that is not a
	// failure: an empty Observed with the note above is the honest answer.
	var after, readErr = c.ReadMgmtAddressing(ctx)
	if readErr == nil {
		res.Observed = after
	} else {
		res.Notes = append(res.Notes,
			"could not re-read after the write (expected when the address moved): "+
				firstLine(readErr.Error()))
	}
	return res, nil
}
