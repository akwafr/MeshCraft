package eos

// gNMI — the device-side half of streaming telemetry.
//
// # What this file is, and what it deliberately is not
//
// It is the CONFIGURATION that makes an EOS switch willing to serve gNMI: the
// `management api gnmi` block, and nothing else. It is not a collector, and it
// is not in the default provisioning push. The collector half lives in
// internal/gnmipoll — the OpenConfig paths, the translation into samples, and a
// real gRPC transport — and cmd/proxy/gnmi.go runs it.
//
// The two halves were separated on purpose, and the separation was the point:
// the device-side form is knowledge worth writing down while it is fresh,
// whereas the channel declaration is a PROMISE to an operator that samples
// arrive. This file shipped first and the promise was withheld until something
// could keep it. IT IS NO LONGER WITHHELD — [monitoring] in eos.go declares four
// `gnmi` channels and `Streaming: true`, on the strength of the captures below
// plus a proven subscription. Cf. internal/driver/gnmi.go, which states the
// rule, and internal/api/gnmi_declaration_test.go, which enforces the pairing
// across the whole registry in both directions.
//
// # WHY THE BLOCK IS OPT-IN AND NOT WRITTEN BY DEFAULT
//
// Not caution for its own sake. `transport grpc default` with no `ssl profile`
// is PLAINTEXT gRPC: EOS authenticates the subscriber with an ordinary AAA
// account whose username and password travel in gRPC metadata, in clear, on the
// management network — the same account `MgmtPushCmds` has just created with
// `role network-admin`. Turning that on for every provisioned switch would hand
// anyone with a port mirror on the management VLAN a privileged device login, in
// exchange for telemetry nothing currently collects. That is a security
// regression dressed as a feature, and it would land on every switch in the
// fleet before a single sample existed.
//
// The certificate work that would fix it is designed and unbuilt
// (docs/en/03-Security/Service identity and rotation.md, "decided, not
// built"). Until it exists, the block is written only when a caller sets
// [MgmtOpts.GNMI] — that is, never, in the provisioning path as it stands.
//
// # STATUS: CAPTURED 2026-08-07 on a DCS-7050SX-64-R running EOS 4.28.13.1M
//
// The block below was applied to that switch, line for line as
// [GNMIEnableCmds] emits it, and every submode line was accepted. The three
// things this header used to list as DEDUCED are now settled, and all three
// came out in favour of the form already written:
//
//  1. WHICH FORM ENABLES THE SERVICE — configuring a transport is enough.
//     "no shutdown" under `management api gnmi` is REJECTED on this train
//     ("% Incomplete command"), so writing it would have put a refused line
//     in the middle of a provisioning run. It is still not written, and now for
//     an observed reason rather than a cautious one.
//
//     THE TRAP THAT NEARLY INVERTED THIS CONCLUSION, recorded because the next
//     reader will hit it too: immediately after the transport is configured,
//     `show management api gnmi` reports "Enabled: no / Server: not yet
//     running". That is a TRANSIENT STARTUP STATE, not a missing line — a few
//     seconds later the same command reports "Enabled: yes / Server: running
//     on port 6030, in default VRF". Read once, straight after the write, it
//     looks exactly like a block that did not take, and the obvious fix — adding
//     "no shutdown" — is the wrong one. A connector that verifies this write by
//     immediate re-read MUST tolerate the pending state;
//  2. 6030 LISTENS IN PLAINTEXT. Confirmed by subscribing to it: "SSL profile:
//     none", "Listen addresses: ::", and internal/gnmipoll's transport
//     completes a Subscribe over an unencrypted connection and streams samples;
//  3. NO AAA AUTHORIZATION LINE IS NEEDED. The switch reports "Authorization
//     required: no" and its running configuration carries only "no aaa root"
//     — no `aaa authorization exec` at all — and the subscription authenticates
//     and streams regardless. The line stays unwritten, which was always the
//     right call for an independent reason: it changes how EVERY administrative
//     session on the box is authorized, including the SSH session meshcraft
//     adopts the device with.
//
// What was NOT exercised: the `ssl profile` line. Nothing in this product issues
// a certificate yet, so no profile existed on the switch to reference.
//
// # THE RE-READ TRAP, and it would bite the first person to verify this write
//
// After `write memory`, the switch's stored configuration reads:
//
//	management api gnmi
//	   transport grpc default
//
// and NOTHING ELSE. The `port 6030` line this builder emits is gone — accepted,
// in force ("Server: running on port 6030"), and absent from the configuration,
// because EOS omits values equal to the default. [DefaultGNMIPort] is written
// explicitly on purpose, for the reason stated at that constant, and that
// decision is unaffected; what changes is how the write may be VERIFIED.
//
// So a connector confirming this block by looking for its own lines in
// `show running-config` would find one of the three missing and report a failed
// write on a switch that is streaming perfectly. The verification that actually
// works is `show management api gnmi`, which reports the EFFECTIVE state —
// transport, enabled, port and VRF — rather than the stored text.
//
// # The channel this file used to say it did not declare
//
// This header once carried the declaration that [monitoring] should eventually
// make, and argued for `if_counters` ALONE: port up/down events were derived on
// the proxy from the SNMP `if_state` poll, and moving `if_state` to gNMI without
// teaching the transition detector to read gNMI samples would have stopped
// port.up / port.down for every Arista — richer telemetry, quieter fleet log,
// nothing saying why.
//
// The declaration has since been made, and it declares FOUR channels rather than
// one. The `if_state` objection was not worked around but removed: the proxy's
// transition detector now keys on the PORT NAME, which is what a gNMI sample
// carries. eos.go's [monitoring] holds that argument and the capture notes for
// all four; it is not repeated here, because a channel list documented in two
// places is a channel list that disagrees with itself.

import (
	"strconv"
	"strings"
)

// DefaultGNMIPort is the port EOS serves gNMI on out of the box, and the one
// gnmic assumes when a target names none.
//
// Written explicitly into the configuration even though it IS the default, for
// the same reason the trunk allowed-list is written explicitly: what the switch
// will listen on must be readable in what we sent, not inferred from the default
// of whatever train it happens to run.
const DefaultGNMIPort = 6030

// GNMIOpts is what the `management api gnmi` block needs from a caller.
type GNMIOpts struct {
	// Port: 0 means [DefaultGNMIPort].
	Port int
	// VRF: the routing instance the transport listens in. Empty means the
	// default VRF, which is where MgmtPushCmds puts management — the SVI and the
	// default route it writes are both in it. A site that moved management into a
	// named VRF must say so here, because a gNMI transport in the wrong instance
	// binds successfully and is reachable from nowhere.
	//
	// THE LAB SWITCH IS SUCH A SITE, and it is worth knowing before reading the
	// builder above as the fleet's shape. core01.lab was surveyed on
	// 2026-08-10 and its management has ALREADY been moved by hand:
	//
	//	show vrf                  →  VRF-MANAGEMENT   IPv4  routing   Vl99
	//	show ip interface brief   →  Vlan99  192.0.2.5/16  up  up
	//	show ip route vrf VRF-MANAGEMENT 0.0.0.0/0
	//	                          →  S  0.0.0.0/0 [1/0] via 192.0.2.1, Vlan99
	//	show management api gnmi  →  running on port 6030, in VRF-MANAGEMENT VRF
	//
	// So the SVI, the default route AND the gNMI transport are all in the named
	// instance and agree — which is exactly the condition this field exists to
	// keep true, and the reason telemetry from that switch is reachable at all.
	// A future MgmtPushCmds run against it with VRF empty would move the
	// transport back to the default instance and silence it.
	//
	// AND THE CONDITION BROKE, three days after that survey, from the OTHER
	// side. On 2026-08-13 the lab's VLANs were moved out of VRF-MANAGEMENT into
	// a new VRF-LAN; nobody re-pointed the transport, which kept binding in an
	// instance that no longer held a single interface. Telemetry was dark for
	// two days with every other channel healthy — the exact failure this
	// comment predicted, reached by moving the interfaces rather than the
	// transport. The durable half of the fix is in the READ path now:
	// parseRunningConfig reads both VRFs and configFacts (gnmiVRFFact) names
	// the disagreement on every state read, so the next re-architecture cannot
	// go quiet.
	VRF string
	// SSLProfile is the name of an EOS `management security ssl profile` the
	// transport presents. Empty means NO PROFILE, i.e. PLAINTEXT gRPC.
	//
	// # This field is the difference between telemetry and a leaked admin account
	//
	// A gNMI subscriber authenticates with an ordinary AAA account, and the
	// credentials travel as gRPC metadata. With no profile that metadata is in
	// clear on the management VLAN — anyone with a port mirror gets a device
	// login that configures the switch, not a community that reads a MIB.
	//
	// The profile itself is NOT created by this connector, and that is the
	// deliberate half. Creating one means generating or installing a key pair and
	// a certificate on the switch, deciding what signs it, and having something
	// that can rotate it — the certificate work docs/en/03-Security/Service
	// identity and rotation.md marks "decided, not built". Writing
	// `ssl profile X` for an X that does not exist would make EOS refuse the
	// transport and leave the switch serving nothing, which is a better failure
	// than plaintext but still a failure. So the name is REFERENCED and the
	// operator owns its existence.
	//
	// UNVERIFIED: whether this train spells it `ssl profile` under the transport
	// submode, as the 4.2x documentation has it. One `show management api gnmi`
	// settles it, and TODO.md carries the ask.
	SSLProfile string
}

// GNMIEnableCmds returns the configuration-mode lines that make an EOS switch
// serve gNMI.
//
// The nesting is real and the exits are not decoration: `management api gnmi`
// opens a submode, `transport grpc default` opens a SECOND one inside it, and
// `port` / `vrf` are commands of that inner submode. A caller that pushed these
// lines flat, or exited once, would leave the session in a submode and have the
// next command of the enclosing push — the credentials line, in the provisioning
// case — interpreted as a transport keyword. Hence one exit per level, always.
func GNMIEnableCmds(opts GNMIOpts) []string {
	var port = opts.Port
	if port < 1 || port > 65535 {
		port = DefaultGNMIPort
	}
	var out = []string{
		"management api gnmi",
		// The transport name is "default". It is a NAME, not a keyword meaning
		// "the default transport": EOS lets an operator define several named gRPC
		// transports, and the one this connector configures is called default so
		// that a second, TLS-protected transport can be added later beside it
		// without this block having to move.
		"transport grpc default",
		"port " + strconv.Itoa(port),
	}
	var vrf = strings.TrimSpace(opts.VRF)
	if vrf != "" {
		out = append(out, "vrf "+vrf)
	}
	// The ssl profile, when the caller named one. Emitted AFTER port and vrf
	// because those two decide where the transport listens and this decides how
	// it is protected — a line order that reads the way the config does.
	//
	// Absent, the transport is plaintext, and that is the whole reason this block
	// stays opt-in (cf. MgmtOpts.GNMI). Nothing here silently substitutes a
	// profile name: a profile this connector did not create would be a guess, and
	// a wrong one makes EOS refuse the transport.
	var profile = strings.TrimSpace(opts.SSLProfile)
	if profile != "" {
		out = append(out, "ssl profile "+profile)
	}
	// Two levels, two exits — see the function header.
	out = append(out, "exit", "exit")
	return out
}
