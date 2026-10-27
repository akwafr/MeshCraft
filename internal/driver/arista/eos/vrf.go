package eos

// Moving management into a VRF, which on this platform is FIVE things or none.
//
// # Why this exists at all
//
// A 7050SX is a router. Its management SVI sits in the default routing instance
// alongside everything the switch forwards, which means the management address
// is reachable from any VLAN that has a route to it, and a default route written
// for management is a default route for the whole box. Putting management in its
// own VRF is what separates the two — and it is the configuration an operator
// running this hardware as a router expects to find.
//
// # THE TRAP THAT STRANDS THE SWITCH, first, because everything else follows
//
// On EOS, assigning a VRF to an interface DELETES ITS IP ADDRESS. Not "moves
// it" — the address is a property of the interface within a routing instance,
// so changing the instance discards it. `vrf VRF-MANAGEMENT` on a live
// management SVI therefore removes the address the session is arriving on,
// immediately, and the next command never lands.
//
// So the address MUST be re-applied in the same block, after the vrf line, and
// the whole thing must be sent as ONE transaction. [MgmtVRFCmds] emits them in
// that order and a caller must not reorder or split them. This is also why the
// function refuses without an address: a caller who does not know the address
// cannot restore it, and would leave the switch reachable only by console.
//
// # AND FOUR MORE THINGS THAT MOVE WITH IT
//
// A VRF is not a property of the interface alone. Everything that reaches
// management through the network has to be told, or it keeps looking in the
// default instance and silently finds nothing:
//
//  1. THE DEFAULT ROUTE. `ip route 0.0.0.0/0 <gw>` in the default instance no
//     longer serves an SVI that has left it. The VRF gets its own route, and the
//     old one is NOT removed — it is the production routing table's, it was
//     there before meshcraft, and removing somebody else's default route to
//     tidy up after ourselves would be the worst possible service;
//  2. SSH. `management ssh` listens in the default VRF unless told otherwise. A
//     switch whose management moved and whose SSH did not is reachable by
//     nothing but its console — which is precisely the outcome this file exists
//     to avoid, and the single most likely way to get it wrong;
//  3. gNMI. The transport binds in an instance; in the wrong one it binds
//     SUCCESSFULLY and is reachable from nowhere. [GNMIOpts.VRF] already carries
//     this and already says so;
//  4. THE NAME SERVERS. `ip name-server vrf default` resolves nothing for a
//     management plane that has left the default instance.
//
// Miss any one of them and the switch keeps forwarding traffic perfectly while
// becoming administratively unreachable in a way no status page shows.
//
// # STATUS: DOCUMENTED, NOT CAPTURED
//
// Nothing here has been run on an Arista. The forms come from the 4.2x command
// reference; `vrf instance` in particular replaced `vrf definition` around 4.22
// and both spellings are in the wild. [Driver.WritesExtra] does not cover this
// gesture, and the caller is expected to have a console open — see
// [MgmtVRFConsoleWarning], which says so in the words an operator reads.

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

// DefaultMgmtVRF is the name this product uses when a caller names none.
//
// Chosen to be obvious in a running configuration rather than short: somebody
// reading `show running-config` on a switch they did not provision should not
// have to guess what "mgmt" is for.
const DefaultMgmtVRF = "VRF-MANAGEMENT"

// MgmtVRFConsoleWarning is what a caller must show before running this.
//
// A constant for the same reason [CertificateOutlivesAddress] is one: it has to
// reach an operator, in the same words, in the API response and in the
// transcript. Whoever presses this is about to change the reachability of the
// address they are pressing it through.
const MgmtVRFConsoleWarning = "moving management into a VRF is applied THROUGH the address it " +
	"moves: the SSH session running it will drop, and if any part of the block is refused " +
	"the switch stays reachable only by its console. Have console access before starting"

// MgmtVRFOpts is what the move needs. Every field is required except the DNS
// servers, and the refusals below say why.
type MgmtVRFOpts struct {
	// Name is the VRF. Empty means [DefaultMgmtVRF].
	Name string
	// Interface is the management SVI ("Vlan99"), as read from the device rather
	// than guessed — writing this onto the wrong interface strands the switch.
	Interface string
	// IP / PrefixLen: the address to RE-APPLY after the vrf line deletes it. See
	// the file header; this is the field whose absence cannot be recovered from.
	IP        string
	PrefixLen int
	// Gateway is the next hop for the VRF's own default route.
	Gateway string
	// DNS are name servers to re-declare inside the VRF. Optional: a management
	// plane that resolves nothing is degraded, not stranded.
	DNS []string
	// GNMI: also move the gNMI transport. False leaves it bound in the default
	// instance, where it will bind successfully and serve nobody.
	GNMI bool
}

// MgmtVRFCmds returns the whole move as ONE ordered configuration block.
//
// The order is load-bearing throughout and is not a style choice:
//
//	vrf instance …          the instance must exist before anything joins it
//	interface <svi>         …
//	  vrf …                 THIS DELETES THE ADDRESS
//	  ip address …          …so it is restored on the very next line
//	  no shutdown           an SVI that changed instance can come back shut
//	ip route vrf …          the VRF's own default route
//	ip name-server vrf …    resolution inside the instance
//	management ssh          THE WAY BACK IN — before gNMI, because if the block
//	                        is truncated this is the line that must have landed
//	management api gnmi     telemetry last: losing it costs graphs, not access
//
// Returns an error rather than a partial block when it cannot be done safely.
// A partial VRF move is the stranding case, so there is no useful "best effort".
func MgmtVRFCmds(opts MgmtVRFOpts) ([]string, error) {
	var name = strings.TrimSpace(opts.Name)
	if name == "" {
		name = DefaultMgmtVRF
	}
	var svi = strings.TrimSpace(opts.Interface)
	if svi == "" {
		return nil, fmt.Errorf("no management interface named: this block would have to " +
			"guess which SVI carries management, and writing it onto the wrong one leaves " +
			"the switch reachable only by console")
	}
	var ip = strings.TrimSpace(opts.IP)
	if net.ParseIP(ip) == nil || opts.PrefixLen < 1 || opts.PrefixLen > 32 {
		// THE REFUSAL THAT MATTERS MOST. Assigning a VRF deletes the address, so
		// a caller that cannot supply it cannot restore it — and the switch comes
		// back with a management SVI that has no address at all.
		return nil, fmt.Errorf("no usable management address (%q/%d): assigning a VRF DELETES "+
			"the interface's address on EOS, so the address must be re-applied in the same "+
			"block. Without it this move strands the switch", opts.IP, opts.PrefixLen)
	}
	var gw = strings.TrimSpace(opts.Gateway)
	if net.ParseIP(gw) == nil {
		return nil, fmt.Errorf("no usable gateway (%q): the VRF starts with an empty routing "+
			"table, so management would be reachable only from its own subnet", opts.Gateway)
	}

	var out = []string{
		// `vrf instance` — CONFIRMED as the Arista spelling by the operator, and
		// the only one emitted. Older trains say `vrf definition`; sending both
		// would put a refused line in the middle of a block that must not be
		// interrupted, so this targets the modern train (cf. DocumentedRelease).
		"vrf instance " + name,
		"exit",
		// ROUTING MUST BE ENABLED **IN THE VRF**, and this line was missing from
		// the first version of this builder.
		//
		// It was missing because the global case hides it: a switch that is
		// already routing has `ip routing` set, and a reader assumes the VRF
		// inherits it. It does not — each instance has its own switch — and the
		// lab 7050SX that motivated this feature is running `no ip routing`
		// outright, as an L2 switch with a management SVI.
		//
		// Without it the VRF holds an interface with an address and a default
		// route that installs nowhere: management answers inside its own subnet
		// and is unreachable from anywhere else. That is the stranding case
		// again, arriving through a line nobody wrote rather than one that was
		// refused.
		"ip routing vrf " + name,
		"interface " + svi,
		// Deletes the address. The next line puts it back; nothing may come
		// between them.
		"vrf " + name,
		fmt.Sprintf("ip address %s/%d", ip, opts.PrefixLen),
		"no shutdown",
		"exit",
		// The VRF's own default route. The default instance's route is left
		// alone — see the file header.
		fmt.Sprintf("ip route vrf %s 0.0.0.0/0 %s", name, gw),
	}

	var servers []string
	var d string
	for _, d = range opts.DNS {
		d = strings.TrimSpace(d)
		if d != "" {
			servers = append(servers, d)
		}
	}
	if len(servers) > 0 {
		out = append(out, "ip name-server vrf "+name+" "+strings.Join(servers, " "))
	}

	// THE WAY BACK IN, and deliberately before telemetry: if the block is
	// truncated by a dropped session, this is the line that must already have
	// landed. Losing gNMI costs graphs; losing SSH costs a trip to the rack.
	out = append(out,
		"management ssh",
		"vrf "+name,
		"no shutdown",
		"exit",
		"exit")

	if opts.GNMI {
		// Rebinds the transport into the instance. A transport in the wrong VRF
		// binds successfully and is reachable from nowhere (cf. GNMIOpts.VRF).
		out = append(out,
			"management api gnmi",
			"transport grpc default",
			"vrf "+name,
			"exit",
			"exit")
	}
	return out, nil
}

// MgmtVRFVerifyCmds are the reads that prove the move landed.
//
// Separate from the write, and EXEC rather than configuration mode, because they
// are what a caller runs FROM THE CONSOLE after the network session has dropped.
// That is the moment this list is for: the SSH session is gone by design, and
// the only way to know whether that was success or stranding is to look.
func MgmtVRFVerifyCmds(name string) []string {
	var n = strings.TrimSpace(name)
	if n == "" {
		n = DefaultMgmtVRF
	}
	return []string{
		"show vrf " + n,
		"show ip interface brief vrf " + n,
		"show ip route vrf " + n + " 0.0.0.0/0",
		"show management ssh",
		"show management api gnmi",
	}
}

// PrefixLenOf renders a prefix length for callers that hold a mask. Small, and
// it exists so a caller never hand-rolls the conversion this connector
// deliberately does not do on the write side (cf. the package header: EOS takes
// CIDR and refuses the dotted mask).
func PrefixLenOf(bits int) string { return strconv.Itoa(bits) }
