package eos

// Management addressing — the WRITE side.
//
// The read side lives in state.go. This gesture is deliberately narrow:
//
//   - it re-addresses the SVI THAT ALREADY CARRIES MANAGEMENT, and never another
//     one. Moving management to a different VLAN is [driver.MgmtMover] /
//     [driver.MgmtJumper], a different contract with a dual path and a backup,
//     and pretending to do it from here would strand switches;
//   - it refuses an intent it cannot express exactly, rather than applying the
//     half of it that fits;
//   - it NEVER writes Management1. That port is out-of-band, on a cable that is
//     not the switching fabric, and an address written there is unreachable from
//     the network meshcraft is administering over.
//
// # The address form, which is the one place a shared builder would go wrong
//
// meshcraft carries the address as a PREFIX ("192.0.2.36/24"), and so does EOS:
// "ip address 192.0.2.36/24" is exactly the line to send. NO CONVERSION AT ALL.
// This is the opposite of cisco/ios, which must split the prefix into a dotted
// mask, and it is the reason [driver.SplitAddrMask] is still used below — not to
// convert, but to VALIDATE. An address that reached the device unchecked would be
// a line EOS rejects mid-block, which is a far worse failure than a refusal here.
//
// # The gateway: a ROUTE, and routes accumulate
//
// There is no "ip default-gateway" on this platform (the IOS reflex produces a
// rejected line). The gateway is "ip route 0.0.0.0/0 <next-hop>", and that
// command ADDS a route rather than replacing one. Sending a new gateway without
// removing the old leaves the switch with TWO default routes and EOS will happily
// load-share its own management traffic across both — half of it toward a
// next-hop that no longer exists. The symptom is management that answers
// intermittently, which reads like a flaky cable and is not.
//
// So the old route is removed BEFORE the new one is added, in the same block, and
// only when it differs from the one being written. A gateway that is already
// correct is left alone rather than removed and re-added, which would blackhole
// the session for the duration of the block.
//
// # THE BLOCK IS NOT ATOMIC, and the route lines are on the far side of the cut
//
// Worth stating precisely, because "same block" reads like a guarantee and is
// not one. [Client.runConfigBlock] sends one line at a time and waits for the
// prompt after each; the lines below are ordered interface / address / exit and
// THEN the two route lines. The address line is the one that can sever this
// session, and anything after it in the block is on the far side of that. A cut
// there leaves the switch at its NEW address with its OLD default route — which
// is the two-default-routes symptom's quieter cousin: reachable from its own
// subnet, unreachable from anywhere else, with every status green.
//
// The order is nonetheless deliberate, and the alternative is worse. Moving the
// route lines in FRONT of the address would mean removing the working default
// route while the switch is still on the old address: a cut there strands it at
// the OLD address with a gateway that does not serve it, which is the same
// outage with the device in the place meshcraft is no longer looking for it.
// Address-first at least leaves it where the caller has been told to verify.
//
// The only evidence either way is cisco/ios/mgmtaddr.go, which has the identical
// shape and whose same-VLAN re-address WAS exercised on a live 2960-X — the
// block completed, so the session outlived its own address change there. Nothing
// says an EOS session does. That is precisely what the capture TODO.md asks for
// under `Mgmt.ApplySameVLAN` has to settle, and it is why the flag is false.

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
		return res, driver.NotSupported("arista-eos", "change the management address",
			"no SVI on this device carries an address that is administratively up, so there "+
				"is nothing to re-address. Management is reaching it another way — the "+
				"console, the out-of-band Management1 port, or an SVI left shut down — and "+
				"choosing an interface here would be guessing which one should become the "+
				"management path")
	}

	// DOES THIS SWITCH CARRY A gNMI CERTIFICATE? Asked BEFORE the write, and that
	// ordering is the whole point: the command block below usually severs this
	// session, so anything not known by now cannot be reported at all.
	//
	// The certificate's subject alternative name carries the management ADDRESS —
	// it has to, because the collector subscribes by address and TLS verifies
	// against it (cf. pki.go). Moving the address therefore invalidates the
	// certificate, and nothing about moving an address touches it. The result is
	// the failure this connector documents everywhere: telemetry stops, the ssl
	// profile still reads "valid", `show management api gnmi` still says
	// "Enabled: yes", SSH still works, and every status stays green.
	//
	// One extra `show running-config` on a gesture that is about to renumber a
	// switch is a cheap price for the operator being told, at the moment they do
	// it, that they now owe the device a new certificate.
	var certified bool
	var certDNSName string
	var runOut, runErr = c.cli.run(ctx, "show running-config", showRunTimeout)
	if runErr == nil || runOut != "" {
		certified = HasGNMICertificate(runOut)
		// The hostname comes out of the configuration ALREADY IN HAND, not from a
		// second command: it is the DNS SAN of the certificate the note below will
		// spell out, and spending another round trip on a device about to be
		// renumbered to learn a name that is three lines up would be indefensible.
		//
		// Qualified WHEN THE DEVICE ITSELF SAYS SO, bare otherwise. The site's
		// local domain (model.Site.LocalDomain, cf. MgmtOpts.DomainName) still
		// never reaches a connector session, so the domain used here is the
		// device's own `ip domain-name` — out of the SAME configuration already in
		// hand, no second round trip. That is also the more correct source: the
		// SAN has to match what a collector RESOLVES, which is the name the device
		// believes it has, not the name the controller filed it under.
		//
		// A device declaring no domain gets the bare hostname, exactly as before
		// this field existed — and on core01 that is not a fallback but the
		// observed case: `show running-config | include domain` is EMPTY there and
		// `show hostname` answers "FQDN: core01-lab" (2026-08-11). See
		// runConfig.DomainName: the absence is captured, the rendering of a PRESENT
		// `ip domain-name` line still is not.
		var rc = parseRunningConfig(runOut, c.host)
		certDNSName = FQDN(rc.Hostname, rc.DomainName)
	}

	// newIP: the address the certificate would have to name, bare and without its
	// prefix length. Empty under DHCP, where nobody knows it yet — and the note
	// below degrades to the warning alone rather than inventing one.
	var newIP string
	var cmds []string
	switch strings.ToLower(strings.TrimSpace(want.Mode)) {
	case driver.MgmtAddrModeDHCP:
		cmds = []string{
			"interface " + svi,
			"ip address dhcp",
			// An SVI that was shut down stays shut down through a re-address: the
			// lease would be taken and the interface still dark.
			"no shutdown",
			"exit",
		}
	case driver.MgmtAddrModeStatic, "":
		// SplitAddrMask VALIDATES here; the mask it produces is deliberately
		// unused, because EOS takes the prefix form directly (cf. the header).
		var ip, _, bits, splitErr = driver.SplitAddrMask(want.Address)
		if splitErr != nil {
			return res, fmt.Errorf("management address: %w", splitErr)
		}
		newIP = ip
		cmds = []string{
			"interface " + svi,
			fmt.Sprintf("ip address %s/%d", ip, bits),
			"no shutdown",
			"exit",
		}
	default:
		return res, fmt.Errorf("management mode %q is neither static nor dhcp", want.Mode)
	}

	// The gateway, as a route, with the old one removed first when it differs.
	var gw = strings.TrimSpace(want.Gateway)
	if gw != "" {
		var old = strings.TrimSpace(before.Gateway)
		if old != "" && old != gw {
			cmds = append(cmds, "no ip route 0.0.0.0/0 "+old)
		}
		if old != gw {
			cmds = append(cmds, "ip route 0.0.0.0/0 "+gw)
		}
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
			// One command takes the whole list on EOS. It ADDS to whatever the
			// device already carries rather than replacing it — the note below says
			// so, because a resolver somebody else set is not ours to remove.
			cmds = append(cmds, "ip name-server vrf default "+strings.Join(servers, " "))
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
		"the address this session came in through has just moved: verify through the NEW "+
			"address, not this one")
	if certified {
		// FIRST among the consequences an operator has to act on, so it is not
		// read last after three paragraphs about routes and resolvers. See
		// [CertificateOutlivesAddress] for why this is a named constant rather
		// than a sentence written here, and [CertificateRenewalNote] for why the
		// warning now carries the commands that answer it: nothing in this product
		// regenerates a certificate, so "re-run the gesture" named a gesture that
		// does not exist.
		var note = CertificateRenewalNote(newIP, certDNSName)
		res.Notes = append(res.Notes, note)
		c.step(false, "gNMI certificate no longer matches this device's address", note)
	}
	if gw != "" && strings.TrimSpace(before.Gateway) != "" &&
		strings.TrimSpace(before.Gateway) != gw {
		res.Notes = append(res.Notes,
			"the previous default route (via "+strings.TrimSpace(before.Gateway)+") was "+
				"REMOVED before the new one was added: on this platform \"ip route\" adds "+
				"rather than replaces, and two default routes would make the switch "+
				"load-share its own management traffic across a next-hop that no longer exists")
	}
	if len(want.DNS) > 0 {
		res.Notes = append(res.Notes,
			"the resolvers requested were ADDED: any resolver already configured on this "+
				"device is still there. meshcraft does not remove a name server it did not "+
				"set — cutting somebody else's resolution to install our own would be the "+
				"worst possible service")
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
