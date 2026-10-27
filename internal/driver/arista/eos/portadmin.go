package eos

// Shutting a port and bringing it back — "shutdown" / "no shutdown"
// (driver/portadmin.go).
//
// # This is an ACTION, so it is deliberately NOT persisted
//
// Everything else this connector writes ends with [Client.save]: a VLAN
// membership or a trap destination that a reboot undoes is worse than one that
// was never written. This gesture is the exception, and the asymmetry is the
// point of the contract it implements — a shut port is an action taken NOW,
// nothing diffs it, and nothing reconciles it. Copying it into the startup
// configuration would turn "bounce this port" into "this port is down for ever",
// including after the interrupted run nobody came back to. So the write stays in
// the running configuration and [ApplyResult.Notes] says exactly that.
//
// # The guardrail, and the one case where it fires without knowing anything
//
// A port that carries a LINK in the management VLAN is not shut without
// [PortAdminPlan.Force]: on a switch reached over its own network, that is the
// jack the session is riding, and "shutdown" on it ends the conversation with
// nothing left to type the "no shutdown" into.
//
// The recourse on this platform is better than most and is worth stating
// precisely, because it decides how hard the guardrail has to push: every switch
// in this range has a console port AND a dedicated out-of-band Management1 port.
// If Management1 is cabled and addressed, a mis-shut uplink is a nuisance rather
// than a site visit. This connector does NOT read that as permission to relax —
// it cannot know whether that port is patched, and a guardrail that assumed the
// good case would be no guardrail at all.
//
// The membership test is the running configuration's, not a guess: the management
// VLAN is the one whose SVI carries an address that is administratively up
// (runConfig.MgmtVLAN), and a port carries it when it is its access VLAN, its
// native VLAN, in its tagged list, or when it is a trunk with NO allowed-vlan
// line — which on EOS means ALL VLANs.
//
// AND WHEN THE MANAGEMENT VLAN CANNOT BE READ, every port with a link is spared.
// A guardrail that fails open is not a guardrail; the refusal names the reason so
// an operator can lift it with Force, having been told what is unknown.
//
// "Cannot be read" covers TWO cases and not one. The obvious one is no SVI with
// an address that is administratively up. The other is a running-config that
// announced an SVI while carrying no "interface" block at all: the VLAN and the
// membership come out of ONE text, EOS prints the SVIs after the Ethernet blocks
// (cf. runFixture, and the ordering the whole range uses), so a read that lost
// its HEAD keeps "interface Vlan99" and loses every "interface Ethernet".
// carriesMgmtVLAN would then take its "no block = factory default, access VLAN 1"
// branch for EVERY port — the branch that is right for a port genuinely absent
// from a COMPLETE configuration — and wave the trunked uplink straight through
// while the notes claimed the management VLAN was known. That is the guardrail
// silently disarming itself. Both cases land on the same answer, and
// ApplyPortAdmin says which one applied.
//
// # Err-disabled ports: "no shutdown" alone does not clear one
//
// A port the SWITCH shut (errdisabled — a refused transceiver, a BPDU guard trip)
// is not administratively down, so bringing it "back up" from here changes
// nothing at all. On EOS the recovery is either the errdisable RECOVERY TIMER —
// which [UnsupportedTransceiverCmds] arms for the transceiver cause, and which is
// the reason provisioning writes that second line at all — or the manual bounce.
// This applier performs neither on its own: the plan said "enable", and silently
// shutting a port to un-shut it is a link flap nobody asked for. It reports the
// condition instead, with the cause the switch gave.
//
// # PoE power cycling is NOT implemented here
//
// [driver.PortPowerCycler] requires restoring the class the port was configured
// with. This package cannot read a port's PoE class at all — the faceplate
// refuses to even guess the `poe` flag (cf. inspect.go), and only the 720XP line
// in this range has PSE hardware. A cycler built on that would cut the volts and
// have nothing to put back, which is half a power cycle and therefore an outage.
//
// # STATUS OF THE FORMS: CAPTURED, DECLARED, AND DRIVEN THROUGH THIS APPLIER
//
// [Driver.WritesExtra] returns true for "port_admin" since 2026-08-10. The three
// things this file depends on were read off a real switch that day — core01.lab,
// DCS-7050SX-64-R, EOS 4.28.13.1M — on Ethernet1, a cage with no transceiver
// fitted: "shutdown" was accepted, "show interfaces status" answered
// "disabled" (the word the re-read below keys on, and the one that had to be
// told apart from the "notconnect" the same port printed a minute before), and
// "no shutdown" put it back. contract.go carries the full statement.
//
// THAT CAPTURE WAS TYPED AT A CLI, and the gap it left is now closed:
// TestLiveWritePortAdmin was RUN against core01 on 2026-08-10 with
// EOS_LIVE_WRITE=1, on Ethernet1, and every leg passed. ApplyPortAdmin drove the
// port DOWN and reported PortsWritten=[ET1] with nothing skipped; an independent
// "show interfaces status" agreed on "disabled"; a SECOND identical plan wrote
// nothing, so the gesture converges instead of flapping a port on every automatic
// pass; and the applier brought the port back to "notconnect", where it was
// found. The three pre-write reads, the guardrail and the re-read that fills
// PortsWritten are therefore exercised against hardware and not only by the fake
// CLI in portadmin_test.go.
//
// WHAT THAT RUN DID NOT EXERCISE, because no lab port can: the management-VLAN
// REFUSAL below. It fires on a port that has a LIVE LINK *and* carries the
// management VLAN, and the only ports on core01 that carry a link are the four
// uplinks the session itself arrives over. That branch stays fake-CLI only, and
// deliberately — proving it on hardware means shutting the jack the prover is
// sitting on.

import (
	"context"
	"fmt"
	"sort"

	"meshcraft/internal/driver"
)

// Compile-time: this Admin shuts and un-shuts ports.
//
// [driver.PortPowerCycler] is DELIBERATELY ABSENT (cf. the header): no PoE read
// exists in this package, so the restore half of a power cycle cannot be
// promised.
var _ driver.PortAdminApplier = (*Client)(nil)

// portAdminCmds is the write, in the only form this platform has.
func portAdminCmds(cliName string, enabled bool) []string {
	if enabled {
		return []string{"interface " + cliName, "no shutdown", "exit"}
	}
	return []string{"interface " + cliName, "shutdown", "exit"}
}

// carriesMgmtVLAN answers the guardrail's question: does this port carry the
// management VLAN, in any of the four ways EOS lets it?
//
// `allowedAll` is the one that is easy to miss and is the dangerous case: a trunk
// with no "switchport trunk allowed vlan" line carries EVERY VLAN, so it
// carries the management one — reading its empty tagged list as "carries nothing"
// would hand the operator a shutdown on the uplink with no warning at all.
func carriesMgmtVLAN(name string, rc runConfig, mgmtVLAN int) bool {
	if mgmtVLAN <= 0 {
		return false
	}
	var p, known = rc.Ports[name]
	if !known {
		// A port with no block in the running configuration is at its factory
		// default: access VLAN 1.
		return mgmtVLAN == 1
	}
	if p.PVID == mgmtVLAN {
		return true
	}
	if p.Mode != "trunk" {
		return false
	}
	if rc.AllowedAll[name] {
		return true
	}
	var v int
	for _, v = range p.Tagged {
		if v == mgmtVLAN {
			return true
		}
	}
	return false
}

// ApplyPortAdmin shuts and un-shuts ports, NOW, and does not persist (cf. the
// header).
//
// Three reads before anything is written, and each one is load-bearing: the
// running configuration for the management VLAN and the ports that carry it,
// "show interfaces status" for the links the guardrail protects and for the
// administrative state that decides what is already done, and the errdisabled
// table for the ports where "enable" will change nothing.
func (c *Client) ApplyPortAdmin(ctx context.Context,
	plan driver.PortAdminPlan) (ApplyResult, error) {

	var out = ApplyResult{VLANsCreated: []int{}, PortsWritten: []string{}}
	if c.cli == nil {
		return out, fmt.Errorf("no open session on this access")
	}
	var werr = c.requireWritableDialect()
	if werr != nil {
		return out, werr
	}

	var runOut, err = c.cli.run(ctx, "show running-config", showRunTimeout)
	if err != nil && runOut == "" {
		return out, fmt.Errorf("pre-write read failed: %v — the guardrail cannot tell which "+
			"port carries administration, so nothing is shut", err)
	}
	var rc = parseRunningConfig(runOut, c.host)

	var statusOut string
	statusOut, err = c.cli.run(ctx, "show interfaces status", cmdTimeout)
	if err != nil && statusOut == "" {
		return out, fmt.Errorf("link read failed: %v — the guardrail cannot tell an active "+
			"port from a dead one, so nothing is shut", err)
	}
	var status = map[string]statusPort{}
	var rows = parseInterfacesStatus(statusOut)
	var row statusPort
	for _, row = range rows {
		status[row.Name] = row
	}

	// The errdisable causes, read once and only when there is something to read:
	// they are what makes the "enable will not help this port" note say WHY.
	var causes = map[string]string{}
	var anyErrDisabled = false
	for _, row = range rows {
		if row.errDisabled() {
			anyErrDisabled = true
			break
		}
	}
	if anyErrDisabled {
		var edOut, edErr = c.cli.run(ctx, "show interfaces status errdisabled", cmdTimeout)
		if edErr == nil || edOut != "" {
			causes = parseErrDisabled(edOut)
		}
	}

	if rc.MgmtVLAN <= 0 {
		out.Notes = append(out.Notes, "no SVI on this device carries an address that is "+
			"administratively up, so meshcraft cannot tell which VLAN administration arrives "+
			"on: every port with a LINK is spared unless the plan forces it")
	}

	// mgmtVLAN is what the guardrail is allowed to TRUST, and it is deliberately
	// not rc.MgmtVLAN on its own (cf. the header, § on the truncated read).
	//
	// Zero Ethernet blocks alongside a live SVI is a broken READ and never a
	// device: a "show running-config" carrying a configured management SVI
	// carries the interface blocks too. So the count is the test, and its answer
	// is IGNORANCE — routed to the branch that already exists for it, which spares
	// every LINKED port and leaves the un-shut direction alone. Refusing the whole
	// gesture instead would also block bringing a port back up, which cuts nothing
	// and is what an operator needs precisely when something is already wrong.
	var mgmtVLAN = rc.MgmtVLAN
	if mgmtVLAN > 0 && len(rc.Ports) == 0 {
		mgmtVLAN = 0
		out.Notes = append(out.Notes, fmt.Sprintf("\"show running-config\" announced VLAN %d as "+
			"the management VLAN but carried NO interface block at all: that is a truncated or "+
			"desynchronised read, not a device, and meshcraft cannot tell which ports carry "+
			"that VLAN. Every port with a LINK is spared unless the plan forces it", rc.MgmtVLAN))
	}

	var intents = make([]driver.PortAdminIntent, len(plan.Ports))
	copy(intents, plan.Ports)
	sort.Slice(intents, func(i, j int) bool { return stdPortLess(intents[i].Port, intents[j].Port) })

	var attempted []driver.PortAdminIntent
	var it driver.PortAdminIntent
	for _, it = range intents {
		var cliName = cliPortName(it.Port)
		if cliName == "" {
			out.Skipped = append(out.Skipped, fmt.Sprintf(
				"%s not touched: this name is outside the standard-model mapping this "+
					"connector translates (a breakout lane, or the out-of-band management "+
					"port) — nothing was sent to the device", it.Port))
			continue
		}
		var now, known = status[it.Port]
		if !known {
			out.Skipped = append(out.Skipped, fmt.Sprintf(
				"%s not touched: the device does not report this port", it.Port))
			continue
		}

		// THE GUARDRAIL. Disabling only — bringing a port back up cuts nothing.
		if !it.Enabled && !plan.Force && now.LinkUp {
			if mgmtVLAN <= 0 {
				out.Skipped = append(out.Skipped, fmt.Sprintf(
					"%s NOT shut: it carries an ACTIVE link and meshcraft could not determine "+
						"this device's management VLAN, so it cannot rule out that this is the "+
						"port the session is riding. Force only knowing why", it.Port))
				continue
			}
			if carriesMgmtVLAN(it.Port, rc, mgmtVLAN) {
				out.Skipped = append(out.Skipped, fmt.Sprintf(
					"%s NOT shut: it carries an ACTIVE link and the management VLAN (%d) — "+
						"this is very likely the port this session arrives on, and shutting it "+
						"ends the conversation with nothing left to type \"no shutdown\" into. "+
						"The way back would be the console port or a cabled Management1, on "+
						"site. Force only knowing why", it.Port, mgmtVLAN))
				continue
			}
		}

		// Already where the plan wants it: nothing is sent, and the port does not
		// appear in PortsWritten (contract).
		if now.adminDown() == !it.Enabled {
			if it.Enabled && now.errDisabled() {
				out.Notes = append(out.Notes, errDisabledEnableNote(it.Port, causes[it.Port]))
			}
			continue
		}

		var cerr = c.runConfigBlock(ctx, portAdminCmds(cliName, it.Enabled))
		if cerr != nil {
			out.Skipped = append(out.Skipped, fmt.Sprintf(
				"%s not %s: the CLI refused the command (%s) — command set written against "+
					"EOS %s from documentation, survey this device's forms",
				it.Port, enabledVerb(it.Enabled), firstLine(cerr.Error()), DocumentedRelease))
			continue
		}
		attempted = append(attempted, it)
		c.step(true, it.Port+" "+enabledVerb(it.Enabled), cliName)
		if it.Enabled && now.errDisabled() {
			out.Notes = append(out.Notes, errDisabledEnableNote(it.Port, causes[it.Port]))
		}
	}

	if len(attempted) == 0 {
		return out, nil
	}

	// The re-read is what says the ports moved — never the return codes.
	statusOut, err = c.cli.run(ctx, "show interfaces status", cmdTimeout)
	if err != nil && statusOut == "" {
		out.Unconfirmed("administrative state", driver.PortAdminIntentPorts(attempted),
			firstLine(err.Error()))
	} else {
		var after = map[string]statusPort{}
		for _, row = range parseInterfacesStatus(statusOut) {
			after[row.Name] = row
		}
		for _, it = range attempted {
			var now, known = after[it.Port]
			if known && now.adminDown() == !it.Enabled {
				out.PortsWritten = append(out.PortsWritten, it.Port)
				continue
			}
			if !known {
				// The port was in the FIRST read (it had to be, or nothing would have
				// been attempted on it) and is absent from the second. Reporting the
				// zero value's empty status here would print "" and read as a switch
				// answering nonsense, hiding the actual event.
				out.Notes = append(out.Notes, it.Port+": the CLI accepted the command but "+
					"the port is ABSENT from the re-read, having been present in the read "+
					"before it — meshcraft cannot confirm it moved, and a faceplate port "+
					"that stops being listed at all is worth looking at on the device")
				continue
			}
			out.Notes = append(out.Notes, it.Port+": the CLI accepted the command but the "+
				"port still reads \""+now.Status+"\" — a return code that changes nothing is "+
				"what this re-read exists to catch")
		}
	}

	out.Notes = append(out.Notes, "this is a RUNNING-configuration change and it was "+
		"deliberately NOT saved: a reload brings these ports back to what the startup "+
		"configuration says. Shutting a port is an action, not an intent meshcraft stores "+
		"or reconciles")
	return out, nil
}

// errDisabledEnableNote explains why "enable" will not help this port, using the
// cause the switch itself gave.
func errDisabledEnableNote(name, cause string) string {
	var head = name + " was shut down BY THE SWITCH (errdisabled), not administratively: it " +
		"is not carrying \"shutdown\", so bringing it up from here changes nothing. Clearing " +
		"it needs the errdisable RECOVERY timer for its cause, or the manual bounce — " +
		"shutdown then no shutdown — once the cause is gone. "
	if cause == ErrDisableXcvr {
		return head + UnsupportedTransceiverNote
	}
	if cause != "" {
		return head + "The switch gives the cause as \"" + cause + "\"."
	}
	return head + "\"show interfaces status errdisabled\" on the device names that cause."
}

func enabledVerb(enabled bool) string {
	if enabled {
		return "brought up (\"no shutdown\")"
	}
	return "shut (\"shutdown\")"
}
