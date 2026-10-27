package ios

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
// including after the interrupted run that nobody came back to. So the write
// stays in the running configuration and [ApplyResult.Notes] says exactly that:
// a reload brings the port back. An operator who wants the shutdown to survive a
// reboot saves the configuration deliberately, which is a different decision from
// the one this button carries.
//
// # The guardrail, and the one case where it fires without knowing anything
//
// A port that carries a LINK in the management VLAN is not shut without
// [PortAdminPlan.Force]: on a switch reached over its own network, that is the
// jack the session is riding, and "shutdown" on it ends the conversation with
// nothing left to type the "no shutdown" into. A Catalyst has a console port,
// so the recourse is a person in the room rather than a paperclip — better than
// the Instant On and SwOS ranges, and still an outage plus a drive.
//
// The membership test is the running configuration's, not a guess: the
// management VLAN is the one whose SVI carries an address that is administratively
// up (runConfig.MgmtVLAN), and a port carries it when it is its access VLAN, its
// native VLAN, in its tagged list, or when it is a trunk with NO allowed-vlan
// line — on IOS that means ALL VLANs, which includes the management one (the trap
// apply.go was built around).
//
// AND WHEN THE MANAGEMENT VLAN CANNOT BE READ, every port with a link is spared.
// A guardrail that fails open is not a guardrail; the refusal names the reason so
// an operator can lift it with Force, having been told what is unknown.
//
// "Cannot be read" covers two cases and not one. The obvious one is no SVI with
// an address that is administratively up. The other is a running-config that
// announced an SVI while carrying no "interface" block: the VLAN and the
// membership come from ONE text, IOS prints the SVIs after the physical ports, so
// a read that lost its head keeps the first and loses the second — and every port
// then looks like a factory-default access-VLAN-1 port to carriesMgmtVLAN, uplink
// included. Both cases land on the same answer, and ApplyPortAdmin says which one
// applied.
//
// # Err-disabled ports: "no shutdown" alone does not clear one
//
// A port the SWITCH shut (err-disabled — a refused transceiver, a BPDU guard
// trip, a port-security violation) is not administratively down, so bringing it
// "back up" from here changes nothing at all. The recovery is the BOUNCE —
// shutdown then no shutdown — which is already written down in
// [UnsupportedTransceiverNote] for the gbic-invalid case, the most common one on
// this platform. This applier does not perform that bounce on its own: the plan
// said "enable", and silently shutting a port to un-shut it is a link flap nobody
// asked for. It reports the condition instead, with the reason the switch gave.
//
// # PoE power cycling is NOT implemented here, and the reason is a missing READ
//
// [driver.PortPowerCycler] requires restoring the class the port was configured
// with — "a jack delivering passive 24V that came back at 802.3at would damage
// the device it feeds". This package cannot read a port's PoE class at all:
// inspect.go states it outright (""show power inline" answers this and is not
// read here"), the faceplate refuses to even guess the `poe` flag, and TODO.md
// records Cisco IOS as "writable on this platform and unimplemented". A cycler
// built on that would cut the volts and have nothing to put back — half a power
// cycle, which is an outage on a camera or an access point. The prerequisite is a
// "show power inline" survey and a PoE reader; until then this connector must
// not answer [driver.AsPortPowerCycler].
//
// # STATUS OF THE FORMS: DOCUMENTED, NOT CAPTURED — and DECLARED anyway
//
// "no shutdown" inside an interface block IS exercised on hardware by this
// connector — on an SVI, in [Client.ApplyMgmtAddressing], 2026-08-03 on the lab
// 2960-X. "shutdown" on a PHYSICAL port has never been sent by it, and the
// "disabled" status word this file reads back has never been seen in a real
// capture.
//
// [Driver.WritesExtra] DOES declare "port_admin" nonetheless, since 2026-08-06:
// an operator decision, taken with the form named explicitly, so that the gesture
// exists across the whole fleet rather than on one connector. contract.go's
// WritesExtra comment carries the reasoning; the part that belongs here is what
// this file must therefore go on doing, and it is doing it:
//
//   - a refused command is reported per port in [driver.ApplyResult.Skipped],
//     naming the release this command set was written against, so an operator sees
//     "the CLI would not take it" rather than silence;
//   - nothing is claimed from a return code — the "show interfaces status"
//     re-read below is what decides a port moved, and a port that did not move is
//     said so with the status word the device actually printed;
//   - the guardrail is what makes the declaration survivable, and it is the
//     section above this one.
//
// TODO.md still names the capture that closes it: "shutdown" on a spare port
// with the "disabled" word read back from this hardware.

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

// adminDown reports whether an operator (or a previous apply) shut this port.
//
// "disabled" is the word "show interfaces status" prints for a port carrying
// "shutdown", and it is NOT the same as "notconnect" (nothing plugged in) or
// "err-disabled" (the switch shut it itself). Reading the three as one would
// make this applier report a cable that is not there as a job done.
func (p statusPort) adminDown() bool { return p.Status == "disabled" }

// portAdminCmds is the write, in the only form this platform has.
func portAdminCmds(cliName string, enabled bool) []string {
	if enabled {
		return []string{"interface " + cliName, "no shutdown", "exit"}
	}
	return []string{"interface " + cliName, "shutdown", "exit"}
}

// carriesMgmtVLAN answers the guardrail's question: does this port carry the
// management VLAN, in any of the four ways IOS lets it?
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
// administrative state that decides what is already done, and the err-disabled
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
	// How this chassis spells an interface name, before anything is addressed to
	// one (cf. Client.learnPortStem).
	c.learnPortStem(runOut)
	c.learnPortStem(statusOut)

	var status = map[string]statusPort{}
	var rows = parseInterfacesStatus(statusOut)
	var row statusPort
	for _, row = range rows {
		status[row.Name] = row
	}

	if rc.MgmtVLAN <= 0 {
		out.Notes = append(out.Notes, "no SVI on this device carries an address that is "+
			"administratively up, so meshcraft cannot tell which VLAN administration arrives "+
			"on: every port with a LINK is spared unless the plan forces it")
	}

	// mgmtVLAN is what the guardrail is allowed to TRUST, and it is deliberately
	// not rc.MgmtVLAN on its own.
	//
	// The management VLAN and the membership that decides which ports carry it are
	// read from the SAME text, and a truncated read can keep one and lose the
	// other. IOS prints the SVIs AFTER the physical interfaces, so a
	// "show running-config" that lost its HEAD — the desynchronisation cliConn
	// can produce when a previous command's output was taken by an earlier read —
	// still carries "interface Vlan99" and carries no "interface
	// GigabitEthernet" block at all. carriesMgmtVLAN would then take its
	// "no block = factory default, access VLAN 1" branch for EVERY port (the branch
	// that is right for a port genuinely absent from a COMPLETE configuration) and
	// wave the trunked uplink straight through, while the notes claimed the
	// management VLAN was known. That is the guardrail silently disarming itself,
	// which is the one failure this file exists to prevent.
	//
	// Zero blocks is a broken read and never a device: IOS prints one block per
	// physical interface even when the port is at its factory default and the body
	// is empty (labRun's GE1 is exactly that). So the count is the test, and its
	// answer is IGNORANCE — routed to the branch that already exists for it, which
	// spares every LINKED port and leaves the un-shut direction alone. Refusing the
	// whole gesture instead would also block bringing a port back up, which cuts
	// nothing and is what an operator needs precisely when something is already
	// wrong.
	var mgmtVLAN = rc.MgmtVLAN
	if mgmtVLAN > 0 && len(rc.Ports) == 0 {
		mgmtVLAN = 0
		out.Notes = append(out.Notes, fmt.Sprintf("\"show running-config\" announced VLAN %d as "+
			"the management VLAN but carried NO interface block at all: that is a truncated or "+
			"desynchronised read, not a device (IOS prints a block per interface even at factory "+
			"default), and meshcraft cannot tell which ports carry that VLAN. Every port with a "+
			"LINK is spared unless the plan forces it", rc.MgmtVLAN))
	}

	var intents = make([]driver.PortAdminIntent, len(plan.Ports))
	copy(intents, plan.Ports)
	sort.Slice(intents, func(i, j int) bool { return stdPortLess(intents[i].Port, intents[j].Port) })

	var attempted []driver.PortAdminIntent
	var it driver.PortAdminIntent
	for _, it = range intents {
		var cliName = c.portName(it.Port)
		if cliName == "" {
			out.Skipped = append(out.Skipped, fmt.Sprintf(
				"%s not touched: this name is outside the standard-model mapping this "+
					"connector translates (stack member 1, module slot 0) — nothing was sent "+
					"to the device", it.Port))
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
						"The way back would be the console port, on site. Force only knowing why",
					it.Port, mgmtVLAN))
				continue
			}
		}

		// Already where the plan wants it: nothing is sent, and the port does not
		// appear in PortsWritten (contract).
		if now.adminDown() == !it.Enabled {
			if it.Enabled && now.errDisabled() {
				out.Notes = append(out.Notes, errDisabledEnableNote(it.Port, now))
			}
			continue
		}

		var cerr = c.runConfigBlock(ctx, portAdminCmds(cliName, it.Enabled))
		if cerr != nil {
			out.Skipped = append(out.Skipped, fmt.Sprintf(
				"%s not %s: the CLI refused the command (%s) — command set written against "+
					"IOS %s from documentation, survey this device's forms",
				it.Port, enabledVerb(it.Enabled), firstLine(cerr.Error()), DocumentedRelease))
			continue
		}
		attempted = append(attempted, it)
		c.step(true, it.Port+" "+enabledVerb(it.Enabled), cliName)
		if it.Enabled && now.errDisabled() {
			out.Notes = append(out.Notes, errDisabledEnableNote(it.Port, now))
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
// reason the switch itself gave. The gbic-invalid case gets the full remediation
// that already exists in parse.go rather than a second, drifting copy of it.
func errDisabledEnableNote(name string, p statusPort) string {
	return name + " was shut down BY THE SWITCH (err-disabled), not administratively: it is " +
		"not carrying \"shutdown\", so bringing it up from here changes nothing. Clearing it " +
		"needs the BOUNCE — shutdown then no shutdown — once the cause is gone; \"show " +
		"interfaces status err-disabled\" on the device names that cause. When it is " +
		ErrDisableGBIC + ": " + UnsupportedTransceiverNote
}

func enabledVerb(enabled bool) string {
	if enabled {
		return "brought up (\"no shutdown\")"
	}
	return "shut (\"shutdown\")"
}
