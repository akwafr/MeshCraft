package s3900

// Shutting a port and bringing it back — "shutdown" / "no shutdown"
// (driver/portadmin.go).
//
// # An ACTION, so it is deliberately NOT persisted
//
// [Client.ApplyPorts] ends in [Client.save] ("copy running-config
// startup-config", answering "startup1.cfg" to the filename prompt — captured):
// a VLAN membership a reboot undoes is worse than one that was never written. This
// gesture is the exception, and the asymmetry is the point of the contract it
// implements. A shut port is an action taken NOW, nothing diffs it and nothing
// reconciles it; copying it into the startup configuration would turn "bounce this
// port" into "this port is down for ever", including after the interrupted run
// nobody came back to. [driver.ApplyResult.Notes] says exactly that, and it is also
// why no [driver.ApplyResult.Backup] copy is taken here as ApplyPorts takes one:
// the way back from this write is a reboot, or the same command with the other
// verb — not a configuration to restore.
//
// # The read side was already there, and it is the fact this file needs most
//
// [briefPort.AdminUp] carries the "Admin" column of "show interfaces brief",
// distinct from Link-Status: a port an operator shut reads "Down Down", a port
// with nothing plugged in reads "Up Down". Reading the two as one would make
// this applier believe it had already shut an unused port and skip the write it
// was asked for — and would make its post-write verification pass on a port that
// never moved. The column ORDER (Admin, then Link-Status) is established by the
// captured header, not assumed.
//
// BOTH ROWS ARE CAPTURED since 2026-08-10 (portadmin_live_test.go): the lab
// switch with ethernet 1/1 shut printed "Eth 1/ 1 … Down Down" beside
// "Eth 1/ 2 … Up Down", two dark ports separated by the Admin column alone.
//
// # STATUS OF THE FORMS: TYPED ONCE, on 2026-08-10, on a firmware that is about
// # to move
//
// "shutdown" / "no shutdown" inside "interface ethernet 1/N" is the Accton
// family form, and until 2026-08-10 it had never been typed on this hardware: the
// lab survey (OpCode 1.7.0) and the post-upgrade capture (1.7.7) between them
// proved "vlan database", "vlan N name X [media ethernet]", "switchport mode
// trunk", "switchport trunk allowed vlan add/remove" and the save sequence —
// nothing about administrative state.
//
// On 2026-08-10 a shut → capture → revert exercise on the lab S3900-24T4S
// (s3900lab, FSOS; the transcript does not record the OpCode version) accepted
// "conf t" → "interface ethernet 1/1" → "shutdown" → "exit" → "exit",
// the prompt walking Switch-1# → Switch-1(config)#, on the DARK port 1/1 — and
// "show interfaces brief" then printed the admin-down ROW this connector had
// never seen (portadmin_live_test.go carries it verbatim). Plain "configure"
// was REFUSED on the way there ("% Invalid input detected at '^' marker.");
// [Client.runConfigBlock] sends "conf t", which is the word this CLI takes.
//
// WHAT IS STILL UNPROVEN is this applier: the commands were typed at the CLI BY
// HAND, not driven through [Client.ApplyPortAdmin], so the guardrail below, the
// "already where the plan wants it" skip and the post-write verification remain
// exercised by constructed tests only.
//
// This package has already been bitten once by exactly this kind of gap, on this
// exact firmware boundary: "media ethernet" is REQUIRED on 1.7.0 and REFUSED on
// 1.7.7 (apply.go's vlanCreateCmds spells out the capture, caret included), and
// the S3900 fleet's pending upgrade may swap the whole CLI for the BDCOM-lineage
// S3900-R dialect, on which writes are refused outright (requireWritableDialect).
// So this file ships ONE form rather than the ordered candidates apply.go uses for
// its deduced writes: there is no second spelling of "shutdown" in either
// lineage to fall back to, and trying a made-up one would be worse than refusing.
//
// [Driver.WritesExtra] DOES declare "port_admin" since 2026-08-06 — an operator
// decision to have the gesture fleet-wide, taken when this connector's evidence was
// the thinnest of the eight (contract.go spells out why it was survivable even
// then: the dialect check refuses an unrecognised CLI before sending anything, a
// rejected verb comes back as a caret line reported per port, and nothing is
// persisted so a reboot undoes it). The 2026-08-10 capture strengthens that
// declaration; it did not change it, and it still needs re-verifying after the
// pending upgrade, on which every form in this package is up for renegotiation.
//
// # The guardrail, and why zero cannot refuse the whole gesture here
//
// A port carrying a LIVE LINK in the management VLAN is not shut without
// [driver.PortAdminPlan.Force]: on a switch reached over its own network that is
// the jack the session rides, and the recourse is the console port, on site.
//
// [driver.PortAdminPlan] carries NO management VLAN — unlike [driver.PortPlan],
// whose plan-side value ApplyPorts protects alongside the device's own — so the
// only source here is the device ([runConfig.MgmtVLAN], the SVI that carries an
// address). ApplyPorts REFUSES to write at all when neither source names a VLAN;
// this applier must not, because that would also refuse to bring a port back UP,
// which cuts nothing and is the gesture an operator needs most when something has
// already gone wrong. It spares every LINKED port instead, and names the reason so
// Force can lift it knowingly. A guardrail that fails open is not a guardrail; one
// that blocks the safe direction too is just an outage nobody can end.
//
// # PoE power cycling is NOT implemented here: the missing piece is a READ
//
// This connector reads no PoE at all — no [driver.PoEReader], no "show power
// inline" equivalent, nothing in testdata/ — and [driver.PortPowerCycler] must
// restore the exact class a port was configured with. Cutting the volts with
// nothing to put back is an outage rather than a reboot, so this connector must
// not answer [driver.AsPortPowerCycler]. The prerequisite is a PoE survey on this
// range and a reader.

import (
	"context"
	"fmt"
	"sort"

	"meshcraft/internal/driver"
)

// Compile-time: this Admin shuts and un-shuts ports.
//
// [driver.PortPowerCycler] is DELIBERATELY ABSENT (cf. the header): no PoE read
// exists in this package, so the restore half cannot be promised.
var _ driver.PortAdminApplier = (*Client)(nil)

// portAdminCmds is the write: one interface block per port.
//
// Per port and not per range, even where the CLI would accept a list: this
// platform reports refusals command by command through [Client.runConfigBlock],
// and the port an operator most needs named is the one that did NOT move.
func portAdminCmds(cliName string, enabled bool) []string {
	if enabled {
		return []string{"interface " + cliName, "no shutdown", "exit"}
	}
	return []string{"interface " + cliName, "shutdown", "exit"}
}

// portAdminGuardBlocks decides whether "shutdown" is sent on this port.
//
// LINK-GATED like every other guardrail in this codebase: a port with no cable
// cannot carry the session that would be cut, and refusing it anyway would only
// stop an operator shutting an unused jack — the safest write on the device.
//
// mgmt <= 0 means IGNORANCE, never permission (cf. the header): no SVI could be
// NAMED — either none carries an address, or several do and nothing told them
// apart (cf. resolveMgmtSVI) — and meshcraft is nonetheless talking to the switch
// over IP, so the read is incomplete rather than the switch being VLAN-less.
func portAdminGuardBlocks(have PortState, mgmt int, linked, force bool) (block bool, reason string) {
	if force || !linked {
		return false, ""
	}
	if mgmt <= 0 {
		return true, "it carries an ACTIVE link and no management SVI could be named on this " +
			"device, so meshcraft cannot tell which VLAN administration arrives on and cannot " +
			"rule out that this is the port the session is riding"
	}
	if carriesVLAN(have, mgmt) {
		return true, fmt.Sprintf("it carries an ACTIVE link and the MANAGEMENT VLAN %d — as its "+
			"native VLAN or in its allowed list — so this is very likely the port this session "+
			"arrives on", mgmt)
	}
	return false, ""
}

// carriesVLAN: does this port state carry that VLAN, natively or tagged?
func carriesVLAN(ps PortState, vlan int) bool {
	if vlan <= 0 {
		return false
	}
	if ps.PVID == vlan {
		return true
	}
	var id int
	for _, id = range ps.Tagged {
		if id == vlan {
			return true
		}
	}
	return false
}

// ApplyPortAdmin shuts and un-shuts ports, NOW, and does not persist.
//
// Two reads before anything is written, the same pair [Client.ApplyPorts] takes
// and for the same reasons: "show run" for the management VLAN and the
// membership that decides which ports carry it, "show interfaces brief" for the
// Admin column that says what is already done and the Link-Status column the
// guardrail protects.
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
	if len(plan.Ports) == 0 {
		return out, nil
	}

	var runOut, err = c.cli.run(ctx, "show run", showRunTimeout)
	if err != nil && runOut == "" {
		return out, fmt.Errorf("pre-write read failed: %v — the guardrail cannot tell which "+
			"port carries administration, so nothing is shut", err)
	}
	var rc = parseRunningConfig(runOut, c.host)

	var briefOut string
	briefOut, err = c.cli.run(ctx, "show interfaces brief", cmdTimeout)
	if err != nil && briefOut == "" {
		return out, fmt.Errorf("link read failed: %v — the guardrail cannot tell an active port "+
			"from a dead one, so nothing is shut", err)
	}
	var brief = map[string]briefPort{}
	var rows = parseInterfacesBrief(briefOut)
	var row briefPort
	for _, row = range rows {
		brief[row.Name] = row
	}
	if len(brief) == 0 {
		return out, fmt.Errorf("\"show interfaces brief\" listed no port on this device: with " +
			"neither the Admin nor the Link-Status column, nothing can be protected and nothing " +
			"is shut")
	}

	// AN EMPTY MEMBERSHIP TABLE IS IGNORANCE, and reading it as "no port carries
	// the management VLAN" is how this guardrail fails OPEN.
	//
	// The read above is accepted whenever the COMMAND succeeded, and a command can
	// succeed and still be short: an undisabled pager, a transport closing
	// mid-output, a firmware that prints its sections in another order. `MgmtVLAN`
	// then comes back perfectly readable from an SVI while `rc.Ports` is empty, so
	// portAdminGuardBlocks skips its own mgmt<=0 branch and asks
	// carries(zero, 99) — false for every port on the switch, including the
	// trunked uplink this session arrives on.
	//
	// Found on arista/eos on 2026-08-09, where SVIs print AFTER the interface
	// blocks so a truncated read keeps `interface Vlan99` and loses every port.
	// hpe/procurve solves the same problem the other way, by refusing a
	// running-config shorter than minRunningConfig — either is fine, having
	// neither is not.
	if len(rc.Ports) == 0 {
		out.Notes = append(out.Notes, "the running configuration carried no interface block at "+
			"all: the VLAN membership could not be read, so the management VLAN is treated as "+
			"UNKNOWN and every port with a LINK is spared unless the plan forces it. A "+
			"successful command can still be short — this is not the same as the device having "+
			"no membership")
		rc.MgmtVLAN = driver.MgmtVLANWhenMembershipRead(rc.MgmtVLAN, len(rc.Ports))
	}
	if rc.MgmtVLAN <= 0 {
		out.Notes = append(out.Notes, "meshcraft cannot tell which VLAN administration arrives "+
			"on: every port with a LINK is spared unless the plan forces it")
		// WHICH of the two causes it was — no addressed SVI at all, or several and
		// no way to choose between them. The sentence above deliberately no longer
		// claims the first: since resolveMgmtSVI, a configuration with four
		// addressed SVIs and nothing to disambiguate them lands here too, and an
		// operator told "no SVI carries an address" about it would go looking for
		// a broken read instead of settling the ambiguity.
		out.Notes = append(out.Notes, rc.MgmtSVINotes...)
	}

	var intents = make([]driver.PortAdminIntent, len(plan.Ports))
	copy(intents, plan.Ports)
	sort.Slice(intents, func(i, j int) bool { return intents[i].Port < intents[j].Port })

	var attempted []driver.PortAdminIntent
	var it driver.PortAdminIntent
	for _, it = range intents {
		var cliName = cliPortName(it.Port)
		if cliName == "" {
			out.Skipped = append(out.Skipped, fmt.Sprintf(
				"%s not touched: this name is outside the standard-model mapping this connector "+
					"translates (unit 1 only) — nothing was sent to the device", it.Port))
			continue
		}
		var now, known = brief[it.Port]
		if !known {
			out.Skipped = append(out.Skipped, fmt.Sprintf(
				"%s not touched: the device does not report this port", it.Port))
			continue
		}

		// THE GUARDRAIL. Disabling only — bringing a port back up cuts nothing.
		if !it.Enabled {
			var block, why = portAdminGuardBlocks(rc.Ports[it.Port], rc.MgmtVLAN,
				now.LinkUp, plan.Force)
			if block {
				out.Skipped = append(out.Skipped, fmt.Sprintf(
					"%s NOT shut: %s, and \"shutdown\" on it ends the conversation with nothing "+
						"left to type \"no shutdown\" into — the way back is the console port, on "+
						"site. Force only knowing why", it.Port, why))
				continue
			}
		}

		// Already where the plan wants it: nothing is sent, and the port does not
		// appear in PortsWritten (contract).
		if now.AdminUp == it.Enabled {
			continue
		}

		var cerr = c.runConfigBlock(ctx, portAdminCmds(cliName, it.Enabled))
		if cerr != nil {
			out.Skipped = append(out.Skipped, fmt.Sprintf(
				"%s not %s: the CLI refused the command (%s) — \"shutdown\" under \"interface "+
					"ethernet 1/N\" was accepted by hand on the lab S3900 on 2026-08-10, and this "+
					"connector's command set was surveyed on OpCode %s.x, so a refusal here means "+
					"THIS device's firmware differs and its forms need surveying", it.Port,
				adminVerb(it.Enabled), firstLine(cerr.Error()), VerifiedOpCode))
			continue
		}
		attempted = append(attempted, it)
		c.step(true, it.Port+" "+adminVerb(it.Enabled), cliName)
	}

	if len(attempted) == 0 {
		return out, nil
	}

	// The re-read is what says the ports moved — never the return codes. On this
	// platform that is not theoretical: "vlan 4001 name X media ethernet" was
	// captured being refused with a caret while the same session accepted the bare
	// form, and a connector that trusted its own send would have reported a VLAN
	// that does not exist.
	briefOut, err = c.cli.run(ctx, "show interfaces brief", cmdTimeout)
	if err != nil && briefOut == "" {
		out.Unconfirmed("administrative state", driver.PortAdminIntentPorts(attempted),
			firstLine(err.Error()))
	} else {
		var after = map[string]briefPort{}
		for _, row = range parseInterfacesBrief(briefOut) {
			after[row.Name] = row
		}
		for _, it = range attempted {
			var now, known = after[it.Port]
			if known && now.AdminUp == it.Enabled {
				out.PortsWritten = append(out.PortsWritten, it.Port)
				continue
			}
			out.Notes = append(out.Notes, fmt.Sprintf(
				"%s: the CLI accepted the command but the Admin column still reads \"%s\" — a "+
					"return code that changes nothing is what this re-read exists to catch",
				it.Port, upDown(now.AdminUp)))
		}
	}

	out.Notes = append(out.Notes, "this is a RUNNING-configuration change and it was deliberately "+
		"NOT persisted: a reboot brings these ports back to what the startup configuration says. "+
		"Shutting a port is an action, not an intent meshcraft stores or reconciles")
	return out, nil
}

func adminVerb(enabled bool) string {
	if enabled {
		return "brought up (\"no shutdown\")"
	}
	return "shut (\"shutdown\")"
}

func upDown(up bool) string {
	if up {
		return "Up"
	}
	return "Down"
}
