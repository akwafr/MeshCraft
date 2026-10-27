package routeros

import (
	"context"
	"fmt"

	"meshcraft/internal/driver"
)

var _ driver.DHCPSnoopApplier = (*Client)(nil)

// Writing DHCP snooping. Read dhcpsnoop.go's header first: the three levels the
// contract describes are not the three levels this platform has, and the apply is
// shaped by that.
//
// ┌─────────────────────────────────────────────────────────────────────────────┐
// │ UNVERIFIED ON HARDWARE. The field names and the two menus are corroborated   │
// │ by REAL CAPTURES of the reference CRS326-24G-2S+ on 7.21beta3               │
// │ (testdata/interface_bridge.json carries "dhcp-snooping":"false";             │
// │ testdata/interface_bridge_port.json carries "trusted":"false" on all 26      │
// │ rows). What has NOT been exercised is the device's reaction to the `set`.    │
// └─────────────────────────────────────────────────────────────────────────────┘
//
// THE STATED REASON FOR THAT USED TO BE THE CHANNEL, AND IT IS NO LONGER TRUE.
// This box said until 2026-08-09 that the write was unexercised because the route
// to the lab CRS326 was restricted to GET/HEAD, and that every write in this
// connector was in the same position. Both were overtaken the same day:
// [Client.ApplyStormControl] issued real `set` requests to 192.0.2.54 over this
// same REST client and this same SOCKS route, they were confirmed from a second
// session, and "storm_control" is declared on this range (live_stormcontrol_test.go,
// stormcontrol.go's header). The transport is not the blocker.
//
// WHAT IS THE BLOCKER IS THE BLAST RADIUS OF THE ENABLE, and it is the paragraph
// below rather than a missing permission. `dhcp-snooping=true` is a BRIDGE
// property: a storm limiter on a dark port polices nothing, so it could be
// exercised on ether2 and cost nothing, whereas this write starts dropping DHCP
// OFFER/ACK on every untrusted port of the bridge the instant it lands — the lab
// unit's other twenty-five ports included. cisco/ios met the same problem and
// solved it with a READ-ONLY reconnaissance pass first (live_snoopprobe_test.go)
// which established what was snooped, what was cabled and which VLAN was empty
// before any write was planned. That is what this range owes, not a channel.
//
// THE RECONNAISSANCE HALF NOW EXISTS HERE TOO: live_snoopprobe_test.go in this
// package, read-only, gated on ROS_LIVE_HOST alone. It answers the four
// questions a write plan needs — which bridge filters, which jacks are cabled,
// whether this device's own address is a LEASE, and what is already trusted —
// and prints the blast radius as a count of cabled untrusted ports. What is
// still missing is a run of it and, from that run, a write plan whose trusted
// port is the site's real uplink. No write harness is written until then: one
// that guesses its target is the artefact the cisco exercise proved you should
// not start from.
//
// WHAT THIS APPLY TURNS ON, and why it says so loudly. The contract requires the
// connector to enable the feature rather than leave the caller with trust flags on a
// device that inspects nothing. On RouterOS that enable is `dhcp-snooping=true` on
// the BRIDGE, and it is not a narrow change: from that instant every port on that
// bridge is inspected, and every port that is not `trusted` drops DHCP OFFER/ACK —
// including the twenty-odd ports meshcraft was never asked about. An operator who
// clicked "trust this port" must read afterwards that the whole bridge moved. Hence
// the note, and hence the fact that it is emitted from the PURE planner, where a test
// can hold it to that promise.
//
// IT NEVER TURNS THE FEATURE OFF. Untrusting a port is a per-port write and stays
// one; clearing `dhcp-snooping` on the bridge because the last trusted port went away
// would strip protection from every port on it, and no screen asked for that.
//
// NOTHING TO SAVE, and no safety net: this platform is [driver.CommitImmediate].
// What is written is live, and it is live in flash. The mitigation is the same one
// admin.go and apply.go name — serial console and MAC-winbox, both OBSERVED — and it
// is a mitigation, not an excuse.

// trustWrite is one `/interface/bridge/port` `trusted` write, resolved to the row
// identifier the device just handed us.
//
// The `.id` is carried here rather than looked up at write time for the reason
// rest.go's Set states: RouterOS numbers its own records (`*0`, `*1B`…), those
// numbers do not survive a reboot, and a cached one writes into a different row —
// which on this menu means trusting a different port than the one on screen.
type trustWrite struct {
	Port    string
	RowID   string
	Trusted bool
}

// planDHCPSnoop decides everything: what to enable, what to write, what to refuse.
//
// PURE — no client, no context, no device. That is deliberate and it is the same
// reasoning as membershipFor in apply.go: the decisions that matter here are made
// before the first byte leaves, and a test can replay them against the reference
// device's real capture.
//
// `enableBridge` is true when the bridge feature is off and must be turned on. The
// caller writes it LAST, after the trust flags.
//
// THE ORDER IS LOAD-BEARING AND IT WAS BACKWARDS HERE. Turning `dhcp-snooping` on
// before the uplink carries `trusted=true` makes the bridge drop every DHCP OFFER and
// ACK crossing it for as long as the window lasts — and if the run dies inside that
// window, the window never closes. On a platform with no rollback, whose own
// management address may itself be a DHCP lease (cf. the last note below), that is not
// the safe half to have completed: it is an outage waiting for a lease timer. Trust
// flags written while the feature is still off change nothing on the wire; the feature
// then comes up with its exception already in place. Same reasoning, same order, as
// cisco/ios/dhcpsnoop.go.
func planDHCPSnoop(plan driver.DHCPSnoopPlan, br Row, portRows []Row) (
	enableBridge bool, writes []trustWrite, out driver.ApplyResult) {

	var bridgeName = br.Get("name")
	var wasEnabled = br.Bool("dhcp-snooping")
	enableBridge = !wasEnabled

	// Index the bridge's own ports, and remember which are trusted TODAY: the
	// end-state check at the bottom needs both.
	var byIface = map[string]Row{}
	var trustedAfter = map[string]bool{}
	var order []string
	var p Row
	for _, p = range portRows {
		if p.Get("bridge") != "" && bridgeName != "" && p.Get("bridge") != bridgeName {
			continue
		}
		var name = p.Get("interface")
		if name == "" {
			continue
		}
		byIface[name] = p
		order = append(order, name)
		trustedAfter[name] = p.Bool("trusted")
	}

	var want driver.DHCPSnoopPortIntent
	for _, want = range plan.Ports {
		var row, member = byIface[want.Port]
		if !member {
			// Not a refusal of ours: the platform has nowhere to put the flag. Say
			// WHERE it would have gone, so the fix ("add it to the bridge") is legible.
			out.Skipped = append(out.Skipped, fmt.Sprintf(
				"%s: not a member of bridge %q — on RouterOS the DHCP-snooping trust flag "+
					"lives on the bridge PORT object, so a port outside the bridge has no "+
					"flag to set and nothing about it is inspected either.",
				want.Port, bridgeName))
			continue
		}
		var id = row.ID()
		if id == "" {
			out.Skipped = append(out.Skipped, fmt.Sprintf(
				"%s: bridge-port record has no .id — trust left unchanged. A `set` without "+
					"an identifier would apply to a different port.", want.Port))
			continue
		}
		trustedAfter[want.Port] = want.Trusted
		if row.Bool("trusted") == want.Trusted {
			// Already there. Sending nothing beats a round trip that changes nothing,
			// and PortsWritten stays honest: the contract says it lists the ports
			// actually reconfigured.
			continue
		}
		writes = append(writes, trustWrite{Port: want.Port, RowID: id, Trusted: want.Trusted})
	}

	// --- What the operator must read afterwards ---

	if enableBridge {
		out.Notes = append(out.Notes, fmt.Sprintf(
			"DHCP snooping was OFF on bridge %q and this apply TURNED IT ON. That is a "+
				"bridge-wide change, not a per-port one: every port on %q is now inspected, "+
				"and every port that is not trusted will have DHCP server replies dropped — "+
				"including the ports this apply was never asked about.",
			bridgeName, bridgeName))
	} else {
		out.Notes = append(out.Notes, fmt.Sprintf(
			"DHCP snooping was already on for bridge %q: only the per-port trust flags "+
				"below were touched.", bridgeName))
	}

	// The per-VLAN level the contract asks for, and the honest answer.
	//
	// The contract tells a connector to say, on an empty VLAN list, that "snooping
	// covers nothing yet". Saying that here would be FALSE: RouterOS has no per-VLAN
	// enable, so the bridge feature covers every VLAN that bridge switches whether the
	// site declares one VLAN, twelve, or none. The sentence is inverted deliberately —
	// telling an operator their protection is empty when it is total is worse than
	// saying nothing.
	if len(plan.VLANs) > 0 {
		out.Notes = append(out.Notes, fmt.Sprintf(
			"The %d VLAN(s) the site declares were NOT written anywhere: RouterOS has no "+
				"per-VLAN DHCP snooping. This is not a gap in the apply — bridge-level "+
				"snooping already inspects every VLAN bridge %q switches, so those VLANs "+
				"are covered without a per-VLAN write.", len(plan.VLANs), bridgeName))
	} else {
		out.Notes = append(out.Notes, fmt.Sprintf(
			"The site declares no VLAN for snooping. On RouterOS that changes nothing: "+
				"there is no per-VLAN enable, and bridge %q inspects every VLAN it switches "+
				"as soon as the feature is on.", bridgeName))
	}

	// THE FAILURE THIS LAST NOTE EXISTS TO CATCH, and it is the one that hurts:
	// snooping on with not a single trusted port means every DHCP OFFER/ACK crossing
	// this bridge is dropped. Every host behind it stops getting a lease — and if this
	// device's own management address is a DHCP lease, it loses it at the next renewal,
	// on a platform with no rollback. Untrusted is the safe DEFAULT for a port; a
	// bridge with no trusted port at all is not a default, it is an outage waiting for
	// a lease timer.
	var anyTrusted bool
	var name string
	for _, name = range order {
		if trustedAfter[name] {
			anyTrusted = true
			break
		}
	}
	if !anyTrusted {
		out.Notes = append(out.Notes,
			"NO port on this bridge is trusted after this apply. Snooping will drop every "+
				"DHCP server reply crossing it, including the ones this device's own DHCP "+
				"client needs if it is addressed dynamically. The uplink toward the real DHCP "+
				"server is the port that must be trusted.")
	}
	return enableBridge, writes, out
}

// verifySnoopSnapshot holds the two GETs the verification re-read needs.
type verifySnoopSnapshot struct {
	Bridge Row
	Ports  []Row
}

// verifySnoopWrites compares what was asked against what the device says AFTERWARDS.
//
// PURE, and it exists because a 2xx is not a confirmation. RouterOS answers `set` with
// the row it believes it wrote, and this connector used to discard that answer and
// report every accepted write as a written port. A field the firmware does not carry —
// `trusted` on a bridge port of an older release, `dhcp-snooping` on a bridge that is
// not hardware-capable — is accepted and dropped, and the operator then reads a
// trusted uplink that is not trusted.
//
// `confirmed` is what may honestly go into [driver.ApplyResult.PortsWritten]; anything
// the re-read contradicts is named in `notes` instead, and stays OUT of PortsWritten.
func verifySnoopWrites(enable bool, writes []trustWrite,
	after verifySnoopSnapshot) (confirmed []string, notes []string) {

	var bridgeName = after.Bridge.Get("name")
	if enable && !after.Bridge.Bool("dhcp-snooping") {
		notes = append(notes, fmt.Sprintf(
			"The device ACCEPTED `dhcp-snooping=yes` on bridge %q and the re-read still "+
				"reports it off. Nothing on this bridge is being inspected, so every trust "+
				"flag below is inert — a return code that changes nothing is exactly what this "+
				"re-read exists to catch.", bridgeName))
	}

	var trustedNow = map[string]bool{}
	var seen = map[string]bool{}
	var p Row
	for _, p = range after.Ports {
		if p.Get("bridge") != "" && bridgeName != "" && p.Get("bridge") != bridgeName {
			continue
		}
		var name = p.Get("interface")
		if name == "" {
			continue
		}
		seen[name] = true
		trustedNow[name] = p.Bool("trusted")
	}

	var w trustWrite
	for _, w = range writes {
		if !seen[w.Port] {
			// The row vanished between the write and the re-read: the port was removed
			// from the bridge, or the `.id` addressed something else. Either way this is
			// not a confirmation, and it must not be reported as one.
			notes = append(notes, fmt.Sprintf(
				"%s: written, then absent from `/interface/bridge/port` on the re-read — "+
					"meshcraft cannot confirm the trust flag landed on the port it meant.", w.Port))
			continue
		}
		if trustedNow[w.Port] == w.Trusted {
			confirmed = append(confirmed, w.Port)
			continue
		}
		notes = append(notes, fmt.Sprintf(
			"%s: the device accepted `trusted=%s` and the re-read reports `trusted=%s`. "+
				"The write did not stick, and this port is NOT %s.",
			w.Port, boolWord(w.Trusted), boolWord(trustedNow[w.Port]), trustWordFor(w.Trusted)))
	}
	return confirmed, notes
}

func trustWordFor(trusted bool) string {
	if trusted {
		return "trusted"
	}
	return "untrusted"
}

// ApplyDHCPSnooping sets per-port trust and turns the bridge feature on.
//
// Writes:
//   - /interface/bridge      `dhcp-snooping` (turned ON only, never off)
//   - /interface/bridge/port `trusted`
//
// A WRITE NEVER DIFFS AGAINST A CACHED STATE: the bridge and its ports are re-read
// here, immediately before, because the `.id`s that address the rows come from that
// read and from nowhere else.
func (c *Client) ApplyDHCPSnooping(ctx context.Context, plan driver.DHCPSnoopPlan) (
	driver.ApplyResult, error) {

	var out driver.ApplyResult
	var bridges, err = c.List(ctx, "interface/bridge")
	if err != nil {
		return out, fmt.Errorf("list interface/bridge: %w", err)
	}
	if len(bridges) == 0 {
		out.Notes = append(out.Notes,
			"No bridge on this device: DHCP snooping is a bridge property on RouterOS, "+
				"so there is nothing to enable and no port that could carry a trust flag. "+
				"Nothing was written.")
		return out, nil
	}
	var br = pickBridge(bridges)
	var bridgeID = br.ID()
	if bridgeID == "" {
		return out, fmt.Errorf("bridge %q has no .id: refusing DHCP-snooping write",
			br.Get("name"))
	}

	var ports []Row
	if ports, err = c.List(ctx, "interface/bridge/port"); err != nil {
		return out, fmt.Errorf("list interface/bridge/port: %w", err)
	}

	var enable bool
	var writes []trustWrite
	enable, writes, out = planDHCPSnoop(plan, br, ports)

	// The trust flags FIRST, the feature LAST (cf. planDHCPSnoop): a trust flag written
	// while snooping is still off changes nothing on the wire, whereas snooping turned
	// on before the uplink is trusted drops every DHCP reply crossing the bridge for as
	// long as the window lasts — and an interrupted run never closes it.
	var attempted []trustWrite
	var w trustWrite
	for _, w = range writes {
		if _, err = c.Set(ctx, "interface/bridge/port", w.RowID,
			map[string]string{"trusted": boolWord(w.Trusted)}); err != nil {
			// The error stops the loop, and what was already sent STAYS on the
			// device — this platform persists immediately. An operator debugging a
			// half-applied trust list needs to know where it stopped, and until
			// 2026-08-09 that record was kept in PortsWritten. It is kept in a NOTE
			// now: no re-read ever happened on this path, so nothing about those
			// ports was confirmed, and PortsWritten means confirmed (cf.
			// driver.ApplyResult.PortsWritten). The information survives, the false
			// claim does not.
			out.Unconfirmed("DHCP snooping trust", portsOf(attempted), err.Error())
			return out, fmt.Errorf("set trusted on %s: %w", w.Port, err)
		}
		attempted = append(attempted, w)
	}

	if enable {
		if _, err = c.Set(ctx, "interface/bridge", bridgeID,
			map[string]string{"dhcp-snooping": boolWord(true)}); err != nil {
			// The note planDHCPSnoop wrote says this apply TURNED THE FEATURE ON. It
			// did not, and leaving that sentence to stand alone would be the one lie
			// this contract exists to prevent: trust flags in place, nothing inspected.
			out.Notes = append(out.Notes, fmt.Sprintf(
				"CORRECTION to the note above: `dhcp-snooping=yes` on bridge %q was REFUSED "+
					"by the device (%v). The feature is still OFF, so the trust flags this apply "+
					"wrote protect nothing at all.", br.Get("name"), err))
			out.Unconfirmed("DHCP snooping trust", portsOf(attempted), err.Error())
			return out, fmt.Errorf("enable dhcp-snooping on bridge %s: %w", br.Get("name"), err)
		}
		c.step(true, fmt.Sprintf("DHCP snooping enabled on bridge %q", br.Get("name")),
			"bridge-wide: every port on it is now inspected")
	}

	// --- The re-read is authoritative, not the 2xx ---
	//
	// Two GETs, the same two the read path makes, on the channel this connector's
	// captures were taken through. Without them PortsWritten is a list of accepted
	// requests, not a list of trusted ports.
	var after verifySnoopSnapshot
	var afterBridges, rerr = c.List(ctx, "interface/bridge")
	if rerr == nil {
		after.Bridge = pickBridge(afterBridges)
		after.Ports, rerr = c.List(ctx, "interface/bridge/port")
	}
	if rerr != nil {
		out.Unconfirmed("DHCP snooping trust", portsOf(attempted), rerr.Error())
		return out, nil
	}
	var confirmed, vnotes = verifySnoopWrites(enable, attempted, after)
	out.PortsWritten = append(out.PortsWritten, confirmed...)
	out.Notes = append(out.Notes, vnotes...)

	c.step(true, "DHCP snooping applied",
		fmt.Sprintf("bridge %q, feature turned on=%v, %d port trust flag(s) confirmed by re-read",
			br.Get("name"), enable, len(out.PortsWritten)))
	return out, nil
}

func portsOf(writes []trustWrite) []string {
	var out []string
	var w trustWrite
	for _, w = range writes {
		out = append(out, w.Port)
	}
	return out
}
