package procurve

// Shutting a port and bringing it back (driver/portadmin.go).
//
// # "disable", NOT "shutdown"
//
// This range does not speak IOS. The administrative state of a port is set from
// inside an interface block with "disable" / "enable", and "shutdown" is
// not a word this CLI knows — it answers "% Invalid input detected at '^'
// marker", which [cliRefusal] reads and this applier reports verbatim rather
// than swallowing.
//
// THE DISABLE FORM IS CAPTURED, and from the strongest source this platform has:
// the running configuration of the reference unit (SW-PLAYER-79, a 2610-48 /
// J9088A on R.11.121, read 2026-08-03, testdata/show_running_config.txt) carries
//
//	interface 1
//	   disable
//	exit
//
// verbatim, on a port a human had shut. On this range the configuration file IS
// the command language — apply.go's header states the rule and relies on it for
// every membership command — so that is a captured form, not a deduced one. Its
// READ-BACK is captured too, which is rarer still: the same unit's
// "show interfaces brief" prints "Enabled No" for port 1 and "Yes" for the
// other fifty-one, so BOTH values of the column this file reads exist in
// testdata/ (cf. [parseInterfacesBrief], whose whole reason to exist is that an
// administratively disabled port and an unplugged one both print "Down").
//
// "enable" is the DEDUCED half: a configuration only ever records a DEPARTURE
// from the defaults on this platform (cf. reNoLLDP's comment), and an enabled port
// is the default — so the word can never appear in a capture, however many
// switches are read. It is the documented inverse and nothing here has sent it.
//
// # WritesExtra DOES declare "port_admin", and it is declared on a DEDUCED VERB
//
// Since 2026-08-06, by operator decision, so the gesture exists across the whole
// fleet (contract.go's WritesExtra comment carries the full reasoning). The part
// that must not be lost in the wiring: the two halves ship as ONE capability, and
// the "enable" half has never been sent — not here, not anywhere. It cannot be
// closed by a further read either, only by an exercise on hardware, and THE LAB
// 2610 IS CURRENTLY OFFLINE, so that exercise is not available today. This package
// also has no live_*_test.go at all, unlike fs/s3400 or ubiquiti/edgeswitch.
//
// The asymmetry points at the RECOVERY direction: a port shut by a captured verb
// that will not come back up by a deduced one. A refusal is at least loud —
// "% Invalid input detected at '^' marker", read by [cliRefusal] and reported per
// port below naming "enable" as the deduced half — and "write memory" is
// deliberately not sent, so a reboot restores what the startup configuration says.
// When the unit is back, mind the FOUR-session budget: saturated, it authenticates
// and then refuses the channel, which reads exactly like a bug (cf.
// testdata/show_ip_ssh.txt and contract.go's Close), and a cut session goes on
// holding one of the four until the switch times it out. TODO.md names the
// exercise.
//
// # The guardrail, and why zero is IGNORANCE on this range
//
// A port that carries a LIVE LINK in the management VLAN is not disabled without
// [driver.PortAdminPlan.Force]. This range has a serial console, so the recourse
// is a person in the rack rather than a paperclip — better than Instant On or
// SwOS, and still an outage plus a drive, made worse by the session budget: the
// cut session keeps holding its slot until the device times it out, so the next
// operator may not even get a door to try.
//
// [runningConfig.MgmtVLAN] is "the first VLAN carrying a STATIC address", and its
// zero does NOT mean "administration arrives on no VLAN". The factory default of
// this range is "ip address dhcp-bootp" on VLAN 1 — deliberately not counted as
// management (parseRunningConfig says why: naming it management would point
// provisioning at the wrong VLAN) — so a switch reached by DHCP reads zero here
// while administration is genuinely arriving on VLAN 1. Every LINKED port is
// therefore spared when the management VLAN is unknown, and the refusal names
// which of the two reasons applied. A guardrail that fails open is not a guardrail.
//
// # PoE power cycling is NOT implemented here: the missing piece is a READ
//
// [driver.PortPowerCycler] must restore the class the port was configured with,
// and this package cannot read a PoE class at all — contract.go's WritesExtra
// comment says it in as many words ("PoE and port security are writable on this
// platform and neither is implemented"), there is no PoEReader here, and no
// "show power-over-ethernet" capture in testdata/. A cycler built on that would
// cut the volts with nothing to put back, which is not a reboot but an outage.
// The prerequisite is a "show power-over-ethernet brief" survey and a
// [driver.PoEReader]; until then this connector must not answer
// [driver.AsPortPowerCycler].

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"meshcraft/internal/driver"
)

// Compile-time: this Admin shuts and un-shuts ports.
//
// [driver.PortPowerCycler] is DELIBERATELY ABSENT (cf. the header): there is no
// PoE read in this package, so the restore half cannot be promised.
var _ driver.PortAdminApplier = (*Client)(nil)

// portAdminCmds is the write, in the form the device's own configuration prints.
//
// ONE BLOCK PER PORT, even though this CLI accepts the compact range grammar
// ("interface 1-4" would be legal, and formatPortRange already builds it for
// membership). Deliberate: this platform reports a refusal in the OUTPUT and not
// through a status, so a ranged command that half-worked would leave one "%"
// line to be attributed among four ports — and the port an operator most needs
// named is the one that did NOT move.
func portAdminCmds(num int, enabled bool) []string {
	var verb = "disable"
	if enabled {
		verb = "enable"
	}
	return []string{"interface " + strconv.Itoa(num), verb, "exit"}
}

// carriesMgmtVLAN answers the guardrail's question against the VLAN-CENTRIC
// tables this platform actually publishes: a port carries the management VLAN
// when that VLAN lists it untagged (its PVID) or tagged (an uplink).
//
// Both are checked, and the tagged case is the dangerous one: on the reference
// unit the management VLAN 99 is "tagged 45-52" — the uplinks — while every
// faceplate port is untagged in VLAN 200. A guardrail that only looked at the
// PVID would happily disable the uplink the session arrives on.
func carriesMgmtVLAN(rc runningConfig, num, mgmtVLAN int) bool {
	if mgmtVLAN <= 0 {
		return false
	}
	return containsInt(rc.Untagged[mgmtVLAN], num) || containsInt(rc.Tagged[mgmtVLAN], num)
}

// portAdminGuardBlocks decides whether "disable" is sent on this port.
//
// LINK-GATED like every other guardrail in this codebase (cf. mgmtGuardBlocks): a
// port with nothing plugged in cannot carry the session that would be cut.
// mgmtVLAN <= 0 means IGNORANCE on this range, not permission — see the header
// for the dhcp-bootp case that produces it — so a linked port is spared and the
// reason says so.
func portAdminGuardBlocks(rc runningConfig, num, mgmtVLAN int,
	linked, force bool) (block bool, reason string) {

	if force || !linked {
		return false, ""
	}
	if mgmtVLAN <= 0 {
		return true, "it carries an ACTIVE link and no VLAN on this switch holds a static " +
			"address, so meshcraft cannot tell which VLAN administration arrives on (a unit " +
			"addressed by DHCP reads exactly like this) — it cannot rule out that this is the " +
			"port the session is riding"
	}
	if carriesMgmtVLAN(rc, num, mgmtVLAN) {
		return true, fmt.Sprintf("it carries an ACTIVE link and the MANAGEMENT VLAN %d "+
			"(untagged or tagged — on this range the uplinks are tagged into it) — this is very "+
			"likely the port this session arrives on", mgmtVLAN)
	}
	return false, ""
}

// ApplyPortAdmin disables and re-enables ports, NOW, and deliberately does NOT
// persist.
//
// # "write memory" is NOT sent, and that is a departure from every other write
// # in this package
//
// [Client.ApplyPorts] and [Client.ApplySpanningTree] both end in
// [Client.persist], because a VLAN membership this range forgets at the next
// reboot is worse than one that was never written ([driver.CommitApplyThenSave]).
// This gesture is the exception, for the reason the contract it implements gives:
// a shut port is an ACTION taken now, nothing diffs it and nothing reconciles it,
// and copying it into flash would turn "bounce this port" into "this port is down
// for ever" — including after the interrupted run nobody came back to, on a switch
// in a rack nobody is watching. [driver.ApplyResult.Notes] says so, so an operator
// who WANTS a shutdown to survive a reboot knows to save the configuration
// deliberately: a different decision from the one this button carries.
//
// Two reads before anything is written, and both are load-bearing: the running
// configuration for the management VLAN and the membership that decides which
// ports carry it, and "show interfaces brief" for the link the guardrail
// protects and the Enabled column that says what is already done.
func (c *Client) ApplyPortAdmin(ctx context.Context,
	plan driver.PortAdminPlan) (driver.ApplyResult, error) {

	var out = driver.ApplyResult{VLANsCreated: []int{}, PortsWritten: []string{}}
	if c.cli == nil {
		return out, errors.New("no open session on this access")
	}
	if len(plan.Ports) == 0 {
		return out, nil
	}

	var text, err = c.cli.run(ctx, "show running-config", showRunTimeout)
	if err != nil && text == "" {
		return out, fmt.Errorf("pre-write read failed: %v — the guardrail cannot tell which "+
			"port carries administration, so nothing is disabled", firstLine(err.Error()))
	}
	// The same "too short to be real" refusal ReadState makes, for the same reason
	// and with more at stake: a desynchronised read reports an empty membership
	// table, and an empty table makes carriesMgmtVLAN answer "no" for every port
	// on the switch — i.e. it turns the guardrail off silently.
	if len(strings.TrimSpace(text)) < minRunningConfig {
		return out, fmt.Errorf("running-config read returned %d characters (%q): the CLI read "+
			"desynchronised, and an empty membership table here would silently disarm the "+
			"management-VLAN guardrail", len(text), text)
	}
	var rc = parseRunningConfig(text)

	var brief string
	brief, err = c.cli.run(ctx, "show interfaces brief", showRunTimeout)
	if err != nil && strings.TrimSpace(brief) == "" {
		return out, fmt.Errorf("link read failed: %v — the guardrail cannot tell an active port "+
			"from a dead one, so nothing is disabled", firstLine(err.Error()))
	}
	var status = parseInterfacesBrief(brief)
	if len(status) == 0 {
		return out, errors.New("\"show interfaces brief\" listed no port: with neither link state " +
			"nor the Enabled column, nothing can be protected and nothing is disabled")
	}

	if rc.MgmtVLAN <= 0 {
		out.Notes = append(out.Notes, "no VLAN on this switch carries a static address, so "+
			"meshcraft cannot tell which VLAN administration arrives on: every port with a LINK "+
			"is spared unless the plan forces it")
	}

	var intents = make([]driver.PortAdminIntent, len(plan.Ports))
	copy(intents, plan.Ports)
	sort.Slice(intents, func(i, j int) bool {
		var a, oka = portNumber(intents[i].Port)
		var b, okb = portNumber(intents[j].Port)
		if !oka || !okb {
			return intents[i].Port < intents[j].Port
		}
		return a < b
	})

	var attempted []driver.PortAdminIntent
	var it driver.PortAdminIntent
	for _, it = range intents {
		var num, ok = portNumber(it.Port)
		if !ok {
			out.Skipped = append(out.Skipped, it.Port+
				" not touched: this name does not translate to a port number on this platform")
			continue
		}
		var now, known = status[num]
		if !known {
			out.Skipped = append(out.Skipped, fmt.Sprintf(
				"%s not touched: the device does not list this port in \"show interfaces brief\"",
				it.Port))
			continue
		}

		// THE GUARDRAIL. Disabling only — bringing a port back up cuts nothing.
		if !it.Enabled {
			var block, why = portAdminGuardBlocks(rc, num, rc.MgmtVLAN, now.Up, plan.Force)
			if block {
				out.Skipped = append(out.Skipped, fmt.Sprintf(
					"%s NOT disabled: %s, and \"disable\" on it ends the conversation with nothing "+
						"left to type \"enable\" into. The way back is the serial console, on site "+
						"— and the cut session goes on holding one of this range's four slots "+
						"until the switch times it out. Force only knowing why", it.Port, why))
				continue
			}
		}

		// Already where the plan wants it: nothing is sent, and the port does not
		// appear in PortsWritten (contract).
		if now.Enabled == it.Enabled {
			continue
		}

		var cerr = c.runConfigBlock(ctx, portAdminCmds(num, it.Enabled))
		if cerr != nil {
			out.Skipped = append(out.Skipped, fmt.Sprintf(
				"%s not %s: the CLI refused the command (%s) — \"enable\" is the DEDUCED half of "+
					"this pair (an enabled port is this platform's default and never appears in a "+
					"configuration), so survey this device's forms", it.Port,
				adminVerb(it.Enabled), firstLine(cerr.Error())))
			continue
		}
		attempted = append(attempted, it)
		c.step(true, it.Port+" "+adminVerb(it.Enabled), "interface "+strconv.Itoa(num))
	}

	if len(attempted) == 0 {
		return out, nil
	}

	// The re-read is what says the ports moved — never the return codes. On this
	// platform that is not a formality: a refusal arrives as a "%" line in the
	// output, and a command whose refusal was worded differently would otherwise
	// pass for a success.
	brief, err = c.cli.run(ctx, "show interfaces brief", showRunTimeout)
	if err != nil && strings.TrimSpace(brief) == "" {
		out.Unconfirmed("administrative state", driver.PortAdminIntentPorts(attempted),
			firstLine(err.Error()))
	} else {
		var after = parseInterfacesBrief(brief)
		for _, it = range attempted {
			var num, _ = portNumber(it.Port)
			var now, known = after[num]
			if known && now.Enabled == it.Enabled {
				out.PortsWritten = append(out.PortsWritten, it.Port)
				continue
			}
			out.Notes = append(out.Notes, fmt.Sprintf(
				"%s: the CLI accepted the command but \"show interfaces brief\" still reports "+
					"Enabled %s — a return code that changes nothing is what this re-read exists "+
					"to catch", it.Port, yesNo(now.Enabled)))
		}
	}

	out.Notes = append(out.Notes, "this is a RUNNING-configuration change and \"write memory\" "+
		"was deliberately NOT sent: a reboot brings these ports back to what the startup "+
		"configuration says. Disabling a port is an action, not an intent meshcraft stores or "+
		"reconciles — an operator who wants it to survive a reboot saves the configuration "+
		"deliberately")
	return out, nil
}

func adminVerb(enabled bool) string {
	if enabled {
		return "enabled (\"enable\")"
	}
	return "disabled (\"disable\")"
}

func yesNo(b bool) string {
	if b {
		return "Yes"
	}
	return "No"
}
