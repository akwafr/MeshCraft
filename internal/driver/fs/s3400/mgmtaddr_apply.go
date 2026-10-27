package s3400

// Management addressing — the WRITE side. The read is in state.go.
//
// Same narrow contract as the Cisco connector next door: it re-addresses the
// SVI that ALREADY carries management and never moves management to another
// VLAN (that is [driver.MgmtMover] / [driver.MgmtJumper]).
//
// The address form is the reason this file exists rather than a two-line
// helper: meshcraft carries a PREFIX ("192.0.2.36/24") and this CLI wants
// "ip address 192.0.2.36 255.255.255.0" — two arguments, dotted mask, no
// CIDR form at all. That is not a guess: the lab unit holds exactly
//
//	interface VLAN99
//	 ip address 192.0.2.3 255.255.0.0
//
// (captured 2026-08-03). The conversion is [driver.SplitAddrMask], shared with
// Cisco, because a mask off by one bit configures cleanly and only fails when
// something routes to the switch.

import (
	"context"
	"fmt"
	"strings"

	"meshcraft/internal/driver"
)

// ApplyMgmtAddressing writes the management address on the CURRENT management
// VLAN.
func (c *Client) ApplyMgmtAddressing(ctx context.Context,
	want driver.MgmtAddrIntent) (driver.MgmtAddrResult, error) {

	var res driver.MgmtAddrResult
	var err = c.requireWritable()
	if err != nil {
		return res, err
	}

	var before driver.MgmtAddrState
	before, err = c.ReadMgmtAddressing(ctx)
	if err != nil {
		return res, fmt.Errorf("reading management addressing before writing: %w", err)
	}
	var svi = strings.TrimSpace(before.Interface)
	if svi == "" {
		return res, driver.NotSupported("fs-s3400", "change the management address",
			"no VLAN interface on this device carries an address, so there is nothing "+
				"to re-address — choosing one here would be guessing which VLAN should "+
				"become the management path")
	}

	var cmds []string
	switch strings.ToLower(strings.TrimSpace(want.Mode)) {
	case driver.MgmtAddrModeDHCP:
		// "ip address dhcp" on the SVI (CLI reference §40.3.1). Documented,
		// not captured: this connector has only ever seen a static address on
		// this platform, and the note says so rather than implying otherwise.
		cmds = []string{
			"interface " + svi,
			"ip address dhcp",
			"exit",
		}
		res.Notes = append(res.Notes,
			"DHCP mode uses \"ip address dhcp\", which is documented for this range but "+
				"has never been observed on it by this connector — verify the lease is "+
				"taken before relying on it")
	case driver.MgmtAddrModeStatic, "":
		var ip, mask, _, splitErr = driver.SplitAddrMask(want.Address)
		if splitErr != nil {
			return res, fmt.Errorf("management address: %w", splitErr)
		}
		cmds = []string{
			"interface " + svi,
			fmt.Sprintf("ip address %s %s", ip, mask),
			"exit",
		}
	default:
		return res, fmt.Errorf("management mode %q is neither static nor dhcp", want.Mode)
	}

	// The default route is global and mode-independent. "ip route default GW"
	// is the accepted form here (captured 2026-08-03; "ip default-gateway"
	// answers "Unknown command" on this platform).
	var gw = strings.TrimSpace(want.Gateway)
	if gw != "" {
		cmds = append(cmds, GatewayCmd(gw))
	}
	if len(want.DNS) > 0 {
		// Refused rather than approximated: no name-server form has been
		// observed on this platform, and sending a guessed one would report a
		// resolver as configured when nothing was written.
		res.Notes = append(res.Notes,
			"DNS was NOT written: no name-server command has been captured on this "+
				"platform, and writing a guessed form would report a resolver that is "+
				"not there")
	}

	err = c.configBlock(ctx, cmds)
	if err != nil {
		c.step(false, "management addressing refused", firstLine(err.Error()))
		return res, fmt.Errorf("writing management addressing on %s: %w", c.host, err)
	}
	c.step(true, "management addressing written on "+svi, strings.Join(cmds, " ; "))

	res.Notes = append(res.Notes,
		"applied on "+svi+" — \"write\" is what makes it survive a reboot",
		"the address this session came in through has just moved: verify through the "+
			"NEW address, not this one")

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
