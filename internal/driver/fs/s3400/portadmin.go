package s3400

// Shutting a port and bringing it back — "shutdown" / "no shutdown"
// (driver/portadmin.go).
//
// # An ACTION, so it is deliberately NOT saved
//
// Every other write in this package ends in [Client.Persist] ("write", which
// answers "OK!" — captured), because a VLAN membership this platform forgets at
// the next reboot is worse than one never written. This gesture is the exception,
// and the asymmetry is the point of the contract it implements: a shut port is an
// action taken NOW, nothing diffs it, nothing reconciles it. Copying it into flash
// would turn "bounce this port" into "this port is down for ever", including after
// the interrupted run nobody came back to. So the write stays in the running
// configuration and [driver.ApplyResult.Notes] says exactly that — an operator who
// wants the shutdown to survive a reboot sends "write" deliberately, which is a
// different decision from the one this button carries.
//
// # STATUS OF THE FORMS: CAPTURED on 2026-08-10 — the verb AND the read-back word
//
// This used to be the one part of the package its live work had not reached: the
// 2026-08-03 hand survey and the 2026-08-05 connector-driven exercise
// (live_write_test.go) between them proved "config", "interface g0/N",
// "switchport pvid", "switchport mode trunk|access", "switchport trunk
// vlan-allowed", "no vlan N" and "write" — every form in apply.go — on the lab
// S3400-48T4SP (Version 2.0.2J), and none of them was "shutdown".
//
// A shut → capture → revert exercise on the lab unit (poefs3400lab, Version
// 2.0.2J) on the DARK port g0/1, on 2026-08-10, closed both halves. "config" →
// "interface g0/1" → "shutdown" → "exit" was accepted, the prompt walking
// Switch_config# → Switch_config_g0/1#, and "show interface g0/1" answered:
//
//	GigaEthernet0/1 is administratively down, line protocol is down   (shut)
//	GigaEthernet0/1 is down, line protocol is down                    ("no shutdown")
//
// So [ParseLinkStates] mapping "administratively down" to [driver.LinkDisabled]
// is hardware-backed at last — portadmin_live_test.go replays both states from
// testdata/. Note what the pair shows, because it is the read side's whole
// difficulty: the ONE word "administratively" separates a port an operator shut
// from a port with no cable, and BOTH say "line protocol is down", so anything
// keying on the protocol clause alone cannot tell them apart.
//
// WHAT THE CAPTURE DOES NOT PROVE is this file's own code path. The commands were
// typed at the CLI BY HAND, not driven through [Client.ApplyPortAdmin]: the
// guardrail, the "already where the plan wants it" skip and the post-write
// re-read below are still exercised by constructed tests only. The capture
// retires "the verb is deduced", not "the applier has been driven".
//
// [Driver.WritesExtra] has DECLARED "port_admin" since 2026-08-06, before any of
// this — an operator decision to have the gesture across the whole fleet, with
// the then-deduced status recorded rather than papered over (contract.go carries
// the reasoning). What this file owes in exchange is unchanged by the capture:
// the refusal below reports the CLI's own words when a port does not take the
// command, the post-write re-read says so again when "administratively down"
// does not come back, and neither path lets a port pass for shut on a return
// code.
//
// # The guardrail
//
// A port carrying a LIVE LINK in the management VLAN is not shut without
// [driver.PortAdminPlan.Force]. This unit is reached over its own network like
// every other, and the recourse after cutting it is the console port, on site.
// [State.MgmtVLAN] comes from the SVI that carries an address — STATIC OR DHCP,
// both are recorded (cf. ParseRunningConfig) — so a zero here does not mean
// "administration arrives untagged" the way it legitimately can on SwOS: it means
// no SVI on this switch has an address at all, which for a device meshcraft is
// talking to over IP is a desynchronised read rather than a fact. Every LINKED
// port is spared in that case, and the refusal names it.
//
// # PoE power cycling is NOT implemented here, and the missing piece is a READ
//
// The lab unit is a PoE+ model (the CLI itself is described as "the S3400 PoE+
// CLI" in requireWritable) and this connector still cannot read a port's PoE
// class: there is no [driver.PoEReader] in this package, no "show power inline"
// or FS equivalent anywhere in it, and nothing in testdata/. [driver.PortPowerCycler]
// requires putting back the exact class a port was configured with, and a cycler
// built on no read would cut the volts with nothing to restore — an outage, not a
// reboot. The prerequisite is a PoE survey on this range and a reader; until then
// this connector must not answer [driver.AsPortPowerCycler].

import (
	"context"
	"fmt"

	"meshcraft/internal/driver"
)

// Compile-time: this Admin shuts and un-shuts ports.
//
// [driver.PortPowerCycler] is DELIBERATELY ABSENT (cf. the header): no PoE read
// exists in this package, so the restore half cannot be promised.
var _ driver.PortAdminApplier = (*Client)(nil)

// portAdminCmds is the write, in the form this CLI family uses.
//
// A block per port — "interface g0/12", the verb, "exit" — the same shape
// [portConfigCmds] emits and for the same reason: this CLI answers an error as
// three lines and [Client.configBlock] waits for each command's own prompt, so a
// refusal is attributed to the line that earned it.
// The cage numbering is a parameter and not a session, same reason as there:
// the function stays pure and the fact is testable (ADR-0018).
func portAdminCmds(cages cageNumbering, standard string, enabled bool) ([]string, error) {
	var dev, nerr = cages.cliPortName(standard)
	if nerr != nil {
		return nil, nerr
	}
	var verb = "shutdown"
	if enabled {
		verb = "no shutdown"
	}
	return []string{"interface " + dev, verb, "exit"}, nil
}

// portAdminGuardBlocks decides whether "shutdown" is sent on this port.
//
// LINK-GATED like [mgmtGuardBlocks]: a port with no cable cannot carry the session
// that would be cut. mgmt <= 0 is IGNORANCE on this platform, not permission (cf.
// the file header) — a linked port is spared and the reason says which case fired.
func portAdminGuardBlocks(have PortState, mgmt int, linked, force bool) (block bool, reason string) {
	if force || !linked {
		return false, ""
	}
	if mgmt <= 0 {
		return true, "it carries an ACTIVE link and no SVI on this switch reports an address, " +
			"so meshcraft cannot tell which VLAN administration arrives on and cannot rule out " +
			"that this is the port the session is riding"
	}
	if carriesMgmt(have, mgmt) {
		return true, fmt.Sprintf("it carries an ACTIVE link and the MANAGEMENT VLAN %d — natively "+
			"or tagged, and on this unit the uplinks are trunks that carry every VLAN — so this "+
			"is very likely the port this session arrives on", mgmt)
	}
	return false, ""
}

// membershipCoverage reports the membership count
// [driver.MgmtVLANWhenMembershipRead] should be asked to judge.
//
// It answers with the count that was read when the read covered the whole
// faceplate, and with ZERO — "nothing usable was read" — when it covered less.
// The collapse is the point: that function's question is "is this membership
// something we ACTUALLY READ?", and a map covering 23 of 52 sockets answers no
// just as firmly as an empty one does. Expressing a short read as zero keeps the
// fleet-wide rule in ONE place instead of growing a second, subtly different
// ignorance test on this connector.
//
// faceplate <= 0 means the faceplate is unknown, and then nothing can be
// concluded from the count's size — the read is taken at face value.
func membershipCoverage(read, faceplate int) int {
	if faceplate > 0 && read < faceplate {
		return 0
	}
	return read
}

// ApplyPortAdmin shuts and un-shuts ports, NOW, and does not persist.
//
// The state is RE-READ first, never taken from a cache: [Client.ReadState] gives
// the three facts this needs in one pass — the management VLAN, the per-port
// membership that decides which ports carry it, and [driver.State.Links], whose
// vocabulary carries administrative state and link state in a single value
// ([driver.LinkDisabled] can only mean "administratively down", since a shut
// port has no link).
func (c *Client) ApplyPortAdmin(ctx context.Context,
	plan driver.PortAdminPlan) (ApplyResult, error) {

	var res = ApplyResult{VLANsCreated: []int{}, PortsWritten: []string{}}
	var err = c.requireWritable()
	if err != nil {
		return res, err
	}
	if len(plan.Ports) == 0 {
		return res, nil
	}

	var before State
	before, err = c.ReadState(ctx)
	if err != nil {
		return res, fmt.Errorf("reading the state before shutting anything: %w — the guardrail "+
			"cannot tell which port carries administration, so nothing is shut", err)
	}
	if len(before.Links) == 0 {
		return res, fmt.Errorf("no port's operational state could be read on %s: the guardrail "+
			"cannot tell an active port from a dead one, so nothing is shut", c.host)
	}
	// AN EMPTY MEMBERSHIP MAP IS IGNORANCE, and reading it as "no port carries the
	// management VLAN" is how this guardrail fails OPEN.
	//
	// `before.Links` is already checked above, and that is the link half. This is
	// the MEMBERSHIP half, and the two come from different commands: a read can
	// return every port's link state and no VLAN membership at all. `MgmtVLAN`
	// then comes back readable from an SVI while `before.Ports` is empty, so
	// portAdminGuardBlocks skips its own mgmt<=0 branch and asks carries(zero, 99)
	// — false for every port, including the uplink this session arrives on.
	//
	// THIS RANGE IS THE ONE WHERE A SHORT READ IS ROUTINE. Its pager is FED, not
	// disabled: "terminal length 0" is accepted silently and changes nothing, and
	// an unfed pager eats the first character of the next command (ssh.go, § "The
	// pager is FED, not disabled"). A `--More--` already truncated a
	// running-config read here on 2026-08-08 and produced a confident wrong
	// conclusion; the same truncation on this path would produce a confident wrong
	// ACTION.
	// ZERO WAS THE WRONG THRESHOLD, and 23 is the number that proves it: on
	// 2026-08-11 a truncated "show running-config" on a 52-port S3400 produced a
	// membership map of 23 ports, which walks straight past a test for emptiness
	// while being no more trustworthy than an empty one. The half that arrived is
	// not the half this guardrail needs — the uplink trunk is at the END of the
	// faceplate, and a read that stops early stops before it.
	//
	// The FACEPLATE is the yardstick, from the model reference and not from this
	// read (cf. faceplatePorts), and the comparison is `<` only. An unknown model
	// totals 0 and the guard then declines to conclude anything from it, falling
	// back to the emptiness test it always had; a device reporting MORE ports than
	// its model declares is left alone, because reporting extra sockets is a
	// connector being generous, not a read being short.
	//
	// WHICH DIRECTION THIS FAILS, since this is a write path: firing here does not
	// abort the apply and returns no error. It routes the decision to the
	// IGNORANCE branch, which SPARES every port carrying a link unless the plan
	// forces it. So a false positive costs a port that stays up and a note saying
	// why; nothing that used to be shut becomes un-shuttable, and nothing that
	// used to work starts failing.
	var faceplate = faceplatePorts(c.facts.Model)
	var covered = membershipCoverage(len(before.Ports), faceplate)
	if covered == 0 {
		var why = "carried no port's VLAN membership at all"
		if len(before.Ports) > 0 {
			why = fmt.Sprintf("covered only %d of this model's %d sockets, so it is a PREFIX "+
				"of the switch and not a picture of it", len(before.Ports), faceplate)
		}
		res.Notes = append(res.Notes, "the pre-write read "+why+
			": the management VLAN is therefore treated as UNKNOWN and every port with a "+
			"LINK is spared unless the plan forces it. On this range a truncated read is a "+
			"live hazard — the pager is fed, not disabled")
		before.MgmtVLAN = driver.MgmtVLANWhenMembershipRead(before.MgmtVLAN, covered)
	}
	if before.MgmtVLAN <= 0 {
		res.Notes = append(res.Notes, "no SVI on this switch reports an address, so meshcraft "+
			"cannot tell which VLAN administration arrives on: every port with a LINK is spared "+
			"unless the plan forces it")
	}

	var intents = make([]driver.PortAdminIntent, len(plan.Ports))
	copy(intents, plan.Ports)
	driver.SortByPortName(intents, portAdminIntentPort, splitStandard)

	var attempted []driver.PortAdminIntent
	var it driver.PortAdminIntent
	for _, it = range intents {
		var _, _, ok = splitStandard(it.Port)
		if !ok {
			res.Skipped = append(res.Skipped, it.Port+
				" not touched: this name does not translate to a socket on this platform "+
				"(g0/N for copper, tg0/N for the SFP+ cages)")
			continue
		}
		var link, listed = before.Links[it.Port]
		if !listed {
			res.Skipped = append(res.Skipped, it.Port+
				" not touched: the device does not report this port in \"show interface\"")
			continue
		}

		// THE GUARDRAIL. Disabling only — bringing a port back up cuts nothing.
		if !it.Enabled {
			var block, why = portAdminGuardBlocks(before.Ports[it.Port], before.MgmtVLAN,
				link == driver.LinkUpState, plan.Force)
			if block {
				res.Skipped = append(res.Skipped, fmt.Sprintf(
					"%s NOT shut: %s, and \"shutdown\" on it ends the conversation with nothing "+
						"left to type \"no shutdown\" into — the way back is the console port, on "+
						"site. Force only knowing why", it.Port, why))
				continue
			}
		}

		// Already where the plan wants it: nothing is sent, and the port does not
		// appear in PortsWritten (contract).
		if (link == driver.LinkDisabled) == !it.Enabled {
			continue
		}

		var cmds, nerr = portAdminCmds(c.cages, it.Port, it.Enabled)
		if nerr != nil {
			// NOTHING IS SENT on a name this session cannot establish. The
			// alternative is the message right below, which blames the
			// firmware's command FORMS — the exact false lead ADR-0018 exists
			// to stop: the form is right, the name would have been invented.
			res.Skipped = append(res.Skipped, it.Port+" not touched: "+nerr.Error())
			continue
		}
		var cerr = c.configBlock(ctx, cmds)
		if cerr != nil {
			res.Skipped = append(res.Skipped, fmt.Sprintf(
				"%s not %s: the CLI refused the command (%s) — \"shutdown\" under \"interface "+
					"g0/N\" was accepted by hand on the lab S3400-48T4SP (Version 2.0.2J) on "+
					"2026-08-10, so a refusal here means THIS device's firmware differs and its "+
					"forms need surveying",
				it.Port, adminVerb(it.Enabled), firstLine(cerr.Error())))
			continue
		}
		attempted = append(attempted, it)
		c.step(true, it.Port+" "+adminVerb(it.Enabled), cmds[0])
	}

	if len(attempted) == 0 {
		return res, nil
	}

	// The re-read is what says the ports moved — never the return codes. Only the
	// links are re-read, not the whole state: nothing else can have changed, and
	// "show running-config" on this platform is some 2000 lines through a pager.
	var after = c.readLinks(ctx)
	if len(after) == 0 {
		res.Unconfirmed("administrative state", driver.PortAdminIntentPorts(attempted),
			"\"show interface\" came back with no port's state at all")
	} else {
		for _, it = range attempted {
			var link, listed = after[it.Port]
			if listed && (link == driver.LinkDisabled) == !it.Enabled {
				res.PortsWritten = append(res.PortsWritten, it.Port)
				continue
			}
			res.Notes = append(res.Notes, fmt.Sprintf(
				"%s: the CLI accepted the command but \"show interface\" still reports it as %q — "+
					"a return code that changes nothing is what this re-read exists to catch. A "+
					"shut port on this platform prints \"administratively down\" (captured "+
					"2026-08-10); a port that does not print it did not move",
				it.Port, string(link)))
		}
	}

	res.Notes = append(res.Notes, "this is a RUNNING-configuration change and \"write\" was "+
		"deliberately NOT sent: a reboot brings these ports back to what the saved configuration "+
		"says. Shutting a port is an action, not an intent meshcraft stores or reconciles")
	return res, nil
}

// portAdminIntentPort names the field the faceplate order is read from
// (driver.SortByPortName).
func portAdminIntentPort(it driver.PortAdminIntent) string { return it.Port }

func adminVerb(enabled bool) string {
	if enabled {
		return "brought up (\"no shutdown\")"
	}
	return "shut (\"shutdown\")"
}
