package edgeswitch

// Shutting a port, bringing it back, and cutting the volts so a powered device
// reboots (driver/portadmin.go).
//
// This is the ONLY package in this codebase that ships both halves of that file,
// and the reason is not that this platform is better understood than the others:
// it is that this is the only connector that WRITES PoE (poe.go, proven live
// 2026-08-05), so it is the only one that can honestly promise the RESTORE half
// of a power cycle. Everywhere else the class cannot be read back and re-applied,
// and "half a power cycle" is an outage rather than a reboot.
//
// # Two gestures, deliberately not one button
//
//   - [Client.ApplyPortAdmin] drops the ETHERNET LINK: "shutdown" in Interface
//     Config. Whatever hangs off the cable keeps running, it just loses its
//     network. Right for a stuck negotiation;
//   - [Client.CyclePortPower] cuts the VOLTS: "poe opmode shutdown", a wait,
//     then the class the port was found with. The device at the far end REBOOTS.
//     Right for a hung camera, and expensive on a room full of access points that
//     were merely mis-negotiating.
//
// Neither is persisted. This connector's other writes end in
// "write memory confirm" (apply.go, poe.go) because a VLAN membership that a
// reboot undoes is worse than one never written; these two are the opposite case.
// A shut port copied into the startup configuration becomes "this port is down
// for ever, including after the run nobody came back to", and a power cycle has
// no steady state to save at all — it is over by the time the panel repaints.
// [driver.ApplyResult.Notes] says so explicitly, rather than leaving an operator
// to discover it at the next power outage.
//
// # STATUS OF THE FORMS
//
//   - "shutdown" / "no shutdown" inside "interface 0/N": DEDUCED from this
//     CLI's family grammar (FASTPATH-derived, cf. the package doc comment) and
//     from the "Admin Mode" column of "show port all", which this file reads
//     back. NOT CAPTURED: every port of every capture in testdata/ reads Admin
//     Mode "Enable" — the word "Disable" has never been seen in that column
//     on this hardware, and no session has ever sent "shutdown" to a physical
//     jack here. [Driver.WritesExtra] DECLARES "port_admin" all the same since
//     2026-08-06, by operator decision, so the gesture exists across the fleet;
//     contract.go's WritesExtra comment records on what terms, and what this file
//     owes in exchange is what it already does — name the form as deduced in every
//     refusal, and never claim a port moved on a return code. TODO.md names the
//     capture that would retire the deduced status;
//   - the PoE forms the cycler uses are CAPTURED and were exercised live on both
//     a capable port (0/5) and a non-capable one (0/1). What has NOT been
//     exercised is the ROUND TRIP with a real powered device on the far end: the
//     survey ran with nothing plugged in anywhere, so every port read
//     "Shutdown" and a cycle of a port actually delivering power — the case
//     where the restore matters — is still owed a live run (TODO.md).
//
// # The admin column is read ONCE, in parse.go, and this file no longer re-parses it
//
// [parseShowPortAll] used to set AdminUp with `strings.Contains(low, "enable")`
// over the WHOLE rest of the row. Every row on this platform carries "Enable"
// twice more — Link Trap and LACP Mode — so that field read true even on a port
// whose Admin Mode said "Disable". It was NOT harmless for the faceplate either,
// which is how it was found: [LinkStates] keys [driver.LinkDisabled] on that
// field, so a shut EdgeSwitch port could only ever be shown as unplugged. This
// file used to carry a second parser of the same rows to work around it, and a
// guardrail built on the shared one would have reported a shut port as up while
// an applier called "shutdown" a job already done.
//
// The repair is now in parse.go — the Admin Mode COLUMN, the FIRST Enable/Disable
// token after the interface name, the Type column between them being blank on
// this platform and never carrying either word — and [parsePortAdminRows] is a
// reshaping of that one reading.

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"meshcraft/internal/driver"
)

// Compile-time: this Admin shuts ports AND power-cycles them.
var (
	_ driver.PortAdminApplier = (*Client)(nil)
	_ driver.PortPowerCycler  = (*Client)(nil)
)

// --- reading the administrative state ------------------------------------

// parsePortAdminRows keys "show port all" by STANDARD name, dropping the slot-3
// LAG rows the way every other reader in this package does (StandardPortName
// refuses them, and inventing six jacks that are not on the front panel is a
// worse answer than omitting them).
//
// It is a RESHAPING of [parseShowPortAll], not a second reading of the rows. It
// used to be the second reading, because that parser set AdminUp from "the row
// contains the word enable somewhere" and every row carries "Enable" twice
// more; the repair landed in parse.go (cf. [PortLine.AdminUp]) and the duplicate
// went with it. Two parsers of one table is how the applier and the faceplate end
// up disagreeing about whether a port is shut.
func parsePortAdminRows(out string) map[string]PortLine {
	var rows = map[string]PortLine{}
	var lines = parseShowPortAll(out)
	var i int
	for i = range lines {
		if lines[i].Name == "" {
			continue // LAG row, or a shape StandardPortName does not recognise
		}
		rows[lines[i].Name] = lines[i]
	}
	return rows
}

// portAdminCmds is the write, in the only form this CLI family has.
//
// A SEPARATE Interface Config block per port, entered and left explicitly, the
// same discipline apply.go's PortCmds uses: this CLI's sub-mode prompt changes
// ("(Interface 0/5)#"), and a block left open would make the next command land
// on the wrong port.
func portAdminCmds(cliName string, enabled bool) []string {
	if enabled {
		return []string{"interface " + cliName, "no shutdown", "exit"}
	}
	return []string{"interface " + cliName, "shutdown", "exit"}
}

// --- the guardrail --------------------------------------------------------

// portAdminGuardBlocks answers the one question that decides whether a
// "shutdown" is sent: could this be the jack the session is riding?
//
// LINK-GATED first, like [mgmtGuardBlocks]: a port with no cable cannot carry
// the session, so it is never protected — refusing it would only stop an
// operator shutting an unused jack, which is the safest write on the device.
//
// MGMT == 0 MEANS IGNORANCE HERE, NOT PERMISSION. "show network" on this
// platform always prints a "Management VLAN ID" line (captured: 99 on the lab
// unit, 1 on a factory-default one), so a zero is meshcraft having failed to read
// or parse it — never the device saying "any VLAN will do", which is what the
// same zero legitimately means on SwOS. A guardrail that fails open is not a
// guardrail, so every LINKED port is spared when the management VLAN is unknown,
// and the refusal says which of the two reasons applied.
func portAdminGuardBlocks(have PortState, mgmt int, linked, force bool) (block bool, reason string) {
	if force || !linked {
		return false, ""
	}
	if mgmt <= 0 {
		return true, "it carries an ACTIVE link and meshcraft could not read this device's " +
			"management VLAN from \"show network\", so it cannot rule out that this is the port " +
			"the session is riding"
	}
	if carries(have, mgmt) {
		return true, fmt.Sprintf("it carries an ACTIVE link and the management VLAN (%d) — this "+
			"is very likely the port this session arrives on", mgmt)
	}
	return false, ""
}

// ApplyPortAdmin shuts and un-shuts ports, NOW, and does not persist.
//
// Three reads before anything is written, and each one is load-bearing:
// "show port all" for the link the guardrail protects and the administrative
// state that decides what is already done, "show running-config" for the VLAN
// membership (this platform's "show vlan" carries none — cf.
// parseRunningConfigPorts), and "show network" for the management VLAN itself.
// A failure on any of the three does NOT fall through to writing blind: with the
// links unknown nothing can be protected, and with the membership unknown the
// protection would be arbitrary.
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

	var portOut string
	portOut, err = c.cli.run(ctx, "show port all", showTimeout)
	if err != nil && strings.TrimSpace(portOut) == "" {
		return res, fmt.Errorf("show port all on %s: %v — the guardrail cannot tell an active "+
			"port from a dead one, so nothing is shut", c.host, err)
	}
	var rows = parsePortAdminRows(portOut)

	var runOut string
	runOut, err = c.cli.run(ctx, "show running-config", showTimeout)
	if err != nil && strings.TrimSpace(runOut) == "" {
		return res, fmt.Errorf("show running-config on %s: %v — the guardrail cannot tell which "+
			"port carries the management VLAN, so nothing is shut", c.host, err)
	}
	var membership = parseRunningConfigPorts(runOut)

	var netOut string
	netOut, err = c.cli.run(ctx, "show network", cmdTimeout)
	if err != nil && strings.TrimSpace(netOut) == "" {
		return res, fmt.Errorf("show network on %s: %v — the management VLAN is what the "+
			"guardrail protects, so nothing is shut", c.host, err)
	}
	var mgmt = MgmtVLANOf(netOut)
	if mgmt <= 0 {
		res.Notes = append(res.Notes, "\"show network\" carried no readable \"Management VLAN ID\": "+
			"every port with a LINK is spared unless the plan forces it")
	}

	// AN EMPTY MEMBERSHIP IS IGNORANCE TOO, and treating it as "no port carries
	// the management VLAN" is how this guardrail fails OPEN.
	//
	// The read above is accepted when the command SUCCEEDED, and a command can
	// succeed and still be short — a pager that was not disabled, a transport that
	// closed mid-output, a firmware whose section order puts the interface blocks
	// last. `mgmt` then comes back perfectly readable from "show network" while
	// `membership` is empty, so [portAdminGuardBlocks] skips its own ignorance
	// branch (mgmt > 0) and asks `carries(zero, 99)`, which is false for every
	// port including the trunked uplink. The session's own jack gets shut, with
	// notes claiming the management VLAN was known.
	//
	// Found on arista/eos on 2026-08-09, where SVIs print AFTER the Ethernet
	// blocks so a truncated read keeps `interface Vlan99` and loses every port;
	// the same shape reaches here by any means that shortens the output. Routed
	// to the ignorance branch by forcing `mgmt` unknown, which spares every LINKED
	// port — cf. cisco/ios, which has carried this test the longest.
	if len(membership) == 0 {
		res.Notes = append(res.Notes, "\"show running-config\" carried no interface block at "+
			"all: the VLAN membership could not be read, so the management VLAN is treated "+
			"as UNKNOWN and every port with a LINK is spared unless the plan forces it. A "+
			"successful command can still be short — this is not the same as the device "+
			"having no membership")
		mgmt = driver.MgmtVLANWhenMembershipRead(mgmt, len(membership))
	}

	var intents = make([]driver.PortAdminIntent, len(plan.Ports))
	copy(intents, plan.Ports)
	sort.Slice(intents, func(i, j int) bool { return portLess(intents[i].Port, intents[j].Port) })

	var attempted []driver.PortAdminIntent
	var it driver.PortAdminIntent
	for _, it = range intents {
		var now, known = rows[it.Port]
		if !known {
			res.Skipped = append(res.Skipped, fmt.Sprintf(
				"%s not touched: the device does not list this port in \"show port all\"", it.Port))
			continue
		}
		if !now.AdminSeen {
			res.Skipped = append(res.Skipped, fmt.Sprintf(
				"%s not touched: its \"show port all\" row carried no readable Admin Mode column, "+
					"so meshcraft cannot tell what state it is in", it.Port))
			continue
		}

		// THE GUARDRAIL. Disabling only — bringing a port back up cuts nothing.
		if !it.Enabled {
			var block, why = portAdminGuardBlocks(membership[it.Port], mgmt, now.LinkUp, plan.Force)
			if block {
				res.Skipped = append(res.Skipped, fmt.Sprintf(
					"%s NOT shut: %s, and shutting it ends the conversation with nothing left to "+
						"type \"no shutdown\" into — this range is reached over SSH and nothing "+
						"else, so the way back is a person on site with the reset button. Force "+
						"only knowing why", it.Port, why))
				continue
			}
		}

		// Already where the plan wants it: nothing is sent, and the port does not
		// appear in PortsWritten (contract).
		if now.AdminUp == it.Enabled {
			continue
		}

		var cfgErr = c.runBlock(ctx, append([]string{"configure"},
			append(portAdminCmds(cliPortName(it.Port), it.Enabled), "exit")...))
		if cfgErr != nil {
			res.Skipped = append(res.Skipped, fmt.Sprintf(
				"%s not %s: the CLI refused the command (%s) — \"shutdown\" on a physical port is "+
					"DEDUCED from this CLI family's grammar and has never been captured on this "+
					"hardware, so survey this device's forms", it.Port, adminVerb(it.Enabled),
				firstLine(cfgErr.Error())))
			continue
		}
		attempted = append(attempted, it)
		c.step(true, it.Port+" "+adminVerb(it.Enabled), cliPortName(it.Port))
	}

	if len(attempted) == 0 {
		return res, nil
	}

	// The re-read is what says the ports moved — never the return codes. This CLI
	// has already been caught executing a command perfectly well while the session
	// read it as refused (cf. ssh.go's rePrompt), and the inverse — a code that
	// changes nothing — is exactly what this catches.
	portOut, err = c.cli.run(ctx, "show port all", showTimeout)
	if err != nil && strings.TrimSpace(portOut) == "" {
		res.Unconfirmed("administrative state", driver.PortAdminIntentPorts(attempted),
			firstLine(err.Error()))
	} else {
		var after = parsePortAdminRows(portOut)
		for _, it = range attempted {
			var now, known = after[it.Port]
			if known && now.AdminSeen && now.AdminUp == it.Enabled {
				res.PortsWritten = append(res.PortsWritten, it.Port)
				continue
			}
			res.Notes = append(res.Notes, it.Port+": the CLI accepted the command but the port's "+
				"Admin Mode still reads \""+adminWord(now.AdminUp)+"\" — a return code that "+
				"changes nothing is what this re-read exists to catch")
		}
	}

	res.Notes = append(res.Notes, "this is a RUNNING-configuration change and it was deliberately "+
		"NOT saved with \"write memory confirm\": a reboot brings these ports back to what the "+
		"startup configuration says. Shutting a port is an action, not an intent meshcraft stores "+
		"or reconciles")
	return res, nil
}

func adminVerb(enabled bool) string {
	if enabled {
		return "brought up (\"no shutdown\")"
	}
	return "shut (\"shutdown\")"
}

func adminWord(up bool) string {
	if up {
		return "Enable"
	}
	return "Disable"
}

// portLess orders GE2 before GE10 — the numeric order of the faceplate, not the
// lexical one. The relation is driver.PortNameLess's; what this connector
// supplies is its own reading of a port name (cf. driver/portsort.go).
func portLess(a, b string) bool { return driver.PortNameLess(a, b, splitStandard) }

// --- PoE power cycle ------------------------------------------------------

// CyclePortPower cuts PoE on the named ports, waits, and puts back the class
// each port was found with.
//
// # THE RESTORE IS THE CONTRACT
//
// The class is READ FIRST, per port, and restored VERBATIM — never a default.
// The lab unit delivers either 802.3af/at negotiation or PASSIVE 24V, and a jack
// feeding a 24V radio that came back at 54V would damage it, while coming back at
// nothing at all would turn an intended reboot into an outage nobody is watching.
// When a restore fails, the error names it AND [driver.ApplyResult.Notes] carries
// the port with the class it should be delivering, because the operator's next
// question is "which camera is dark and what does it need".
//
// # Refusals, all of them ordinary
//
//   - a port that is not [driver.PoEPortState.Capable]: sockets 0/1-0/4 of the
//     lab EdgePoint S16 have NO power-sourcing hardware at all and refuse every
//     opmode but "shutdown" (poe.go). There is nothing to cut, and asking
//     anyway would earn an "Unsupported Interfaces" that reads like a fault;
//   - a port already configured [driver.PoEClassNone]: the volts are already off,
//     so a "cycle" would cut nothing and then dutifully restore "none". The
//     powered device an operator means to reboot is not being powered by this
//     switch;
//   - a port the PoE tables do not list at all — the SFP+ cages, which carry no
//     PoE scope on this platform.
//
// # One wait for the whole batch
//
// The ports are all cut, ONE wait is served, then all are restored. Waiting per
// port would multiply the only physically meaningful part of the gesture by the
// number of jacks, and an operator cycling six cameras would be told a minute
// later that the last two are still coming back.
//
// # The wait does not abandon a port with its power off
//
// If the context dies during the wait — a deadline that was sized too tightly
// upstream — the restore is still attempted, on a context detached from it. A
// cancelled power cycle is not an undone power cycle: it is volts left off with
// nobody watching, which is the one outcome this function must not produce.
func (c *Client) CyclePortPower(ctx context.Context,
	plan driver.PortCyclePlan) (ApplyResult, error) {

	var res = ApplyResult{VLANsCreated: []int{}, PortsWritten: []string{}}
	var err = c.requireWritable()
	if err != nil {
		return res, err
	}
	if len(plan.Ports) == 0 {
		return res, nil
	}

	var before driver.PoEState
	before, err = c.ReadPoE(ctx)
	if err != nil {
		return res, fmt.Errorf("reading the PoE state before cutting power: %w — the class to "+
			"put back is not known, so nothing is cut", err)
	}
	var state = map[string]driver.PoEPortState{}
	var p driver.PoEPortState
	for _, p = range before.Ports {
		state[p.Port] = p
	}

	var names = append([]string{}, plan.Ports...)
	sort.Slice(names, func(i, j int) bool { return portLess(names[i], names[j]) })

	// 1) Decide, before touching anything, which ports are actually cyclable and
	// what each one must come back as.
	var restore = map[string]string{}
	var cyclable []string
	var name string
	for _, name = range names {
		var ps, known = state[name]
		if !known {
			res.Skipped = append(res.Skipped, fmt.Sprintf(
				"%s: this platform's PoE tables do not list this port at all (the SFP+ cages "+
					"never appear in them) — there is no power here to cycle", name))
			continue
		}
		if !ps.Capable {
			res.Skipped = append(res.Skipped, fmt.Sprintf(
				"%s: no power-sourcing hardware behind this jack (\"show poe status all\" prints "+
					"Temperature N/A) — the device refuses every opmode but \"shutdown\", so there "+
					"is nothing to cycle", name))
			continue
		}
		if ps.Class == driver.PoEClassNone {
			res.Skipped = append(res.Skipped, fmt.Sprintf(
				"%s: already configured to deliver nothing (OP Mode Shutdown) — cutting power "+
					"that is already off would reboot nothing, and whatever is on this cable is "+
					"not powered by this switch", name))
			continue
		}
		if ps.Class == "" {
			res.Skipped = append(res.Skipped, fmt.Sprintf(
				"%s: its OP Mode did not map onto a class meshcraft knows, so the class to put "+
					"back afterwards is unknown — cutting power here would be half a power cycle",
				name))
			continue
		}
		var _, _, sendable = opModeOfClass(ps.Class)
		if !sendable {
			res.Skipped = append(res.Skipped, fmt.Sprintf(
				"%s: it delivers %q, which this connector can READ but not SET — the restore "+
					"half could not be sent, so the cut is not made", name, ps.Class))
			continue
		}
		restore[name] = ps.Class
		cyclable = append(cyclable, name)
	}
	if len(cyclable) == 0 {
		return res, nil
	}

	// 2) Cut. Global Config once for the whole batch, the way ApplyPoE does.
	var _, cfgErr = c.cli.run(ctx, "configure", cmdTimeout)
	if cfgErr != nil {
		return res, fmt.Errorf("entering Global Config on %s: %w — no port was cut", c.host, cfgErr)
	}
	var cut []string
	for _, name = range cyclable {
		var setErr = c.setPortClass(ctx, name, driver.PoEClassNone)
		if setErr != nil {
			res.Skipped = append(res.Skipped, fmt.Sprintf(
				"%s: power was NOT cut (%s) — nothing to restore on this port", name,
				firstLine(setErr.Error())))
			continue
		}
		cut = append(cut, name)
	}
	if len(cut) == 0 {
		_, _ = c.cli.run(ctx, "exit", cmdTimeout)
		return res, nil
	}
	c.step(true, fmt.Sprintf("PoE cut on %d port(s)", len(cut)), strings.Join(cut, " "))

	// 3) Wait. Zero means the contract's default, which is chosen so a PD's supply
	// actually collapses instead of riding through on its capacitors.
	var offFor = plan.OffFor
	if offFor <= 0 {
		offFor = driver.DefaultPowerCycleOff
	}
	var timer = time.NewTimer(offFor)
	select {
	case <-timer.C:
	case <-ctx.Done():
		// Deliberately NOT a return: see the header. The restore runs below on a
		// detached context, because volts left off are worse than a late answer.
		res.Notes = append(res.Notes, fmt.Sprintf(
			"the context died during the %s off period (%s): the restore was carried out anyway, "+
				"on a context detached from it — abandoning a cut port is not the same thing as "+
				"undoing it", offFor, firstLine(ctx.Err().Error())))
	}
	timer.Stop()

	// 4) Restore. On a context that cannot already be dead: whatever happened to
	// the caller's, the ports that were cut must be given their class back.
	var rctx, rcancel = context.WithTimeout(context.WithoutCancel(ctx), restoreBudget)
	defer rcancel()

	// `restored` is what the CLI ACCEPTED, never what a caller is told was written:
	// it feeds the verification below and, when that verification cannot be taken
	// at all, the note that names the ports (cf. driver.ApplyResult.Unconfirmed).
	var restored []string
	var failed []string
	for _, name = range cut {
		var setErr = c.setPortClass(rctx, name, restore[name])
		if setErr == nil {
			restored = append(restored, name)
			continue
		}
		failed = append(failed, name)
		res.Notes = append(res.Notes, fmt.Sprintf(
			"%s IS STILL DARK: its PoE was cut and the restore to %q FAILED (%s). This port must "+
				"be set back to %q — by hand if need be — or whatever it feeds stays off",
			name, restore[name], firstLine(setErr.Error()), restore[name]))
	}
	_, _ = c.cli.run(rctx, "exit", cmdTimeout) // back out of Global Config

	// 5) Verify, because PoE has physical consequences and a return code is not an
	// observation. Same retried re-read ApplyPoE uses: this CLI can be caught
	// mid-settle on the very port just written.
	//
	// THE RE-READ OVERRULES THE RETURN CODES, and on this path that is the whole
	// safety property. `restored` holds the ports whose restore COMMAND came back
	// clean — which is not the same as a port delivering power again, and is why
	// nothing was put in res.PortsWritten from it. A jack whose class reads back
	// wrong, or that the re-read does not mention at all, is exactly the port an
	// operator has to walk to; reporting it as restored because the CLI said "ok"
	// is the failure driver.PortPowerCycler calls out ("a silent failure here
	// leaves a camera off with nothing on screen to explain it").
	//
	// Every port that was CUT is verified, including one whose restore command
	// errored: the device is the authority on whether the class came back, and a
	// command that reported a failure while the jack recovered is a better thing to
	// discover here than at the far end.
	var after, rerr = c.reReadPoE(rctx, poeVerifyAttempts)
	if rerr != nil {
		// The re-read never landed, which on THIS path is the most alarming of the
		// three non-confirmations: the volts were cut and the restore commands were
		// accepted, and meshcraft cannot say whether the class came back. It is
		// still not a refusal — Skipped would offer a "force" button for a lost
		// session — so it is said as a Note, naming the ports, the way
		// [driver.ApplyResult.Unconfirmed] does fleet-wide. PortsWritten held the
		// ports whose restore COMMAND came back clean; that is a return code, and a
		// return code is not an observation, so the list is retracted.
		res.PortsWritten = []string{}
		res.Unconfirmed("PoE class restored after the power cycle", restored,
			firstLine(rerr.Error()))
		res.Notes = append(res.Notes, "the volts WERE cut on "+strings.Join(cut, ", ")+
			" and the verifying re-read did not land: check these jacks physically before "+
			"assuming whatever they feed came back")
	} else {
		var confirmed, unmatched = verifyPoEClasses(after, cut, restore)
		res.PortsWritten = confirmed
		res.Skipped = append(res.Skipped, unmatched...)
		// Ports whose restore command already failed loudly are in `failed` and have
		// their own note naming the class owed. What this adds is the quieter case:
		// the command succeeded, and the device still does not report the class back.
		var unconfirmed string
		for _, unconfirmed = range unmatched {
			res.Notes = append(res.Notes,
				"unconfirmed after the power cycle — verify this jack physically: "+unconfirmed)
		}
	}

	res.Notes = append(res.Notes, fmt.Sprintf(
		"power was off for %s and each port was put back to the class it was found with; this is "+
			"a RUNNING-configuration change and it was deliberately NOT saved — a power cycle is "+
			"an action, and there is no steady state to persist", offFor))

	if len(failed) > 0 {
		return res, fmt.Errorf("PoE restored on %d port(s) but NOT on %s: %s",
			len(res.PortsWritten), strings.Join(failed, ", "),
			"see the notes for the class each one must be set back to")
	}
	return res, nil
}

// restoreBudget is how long the restore half is given, independent of whatever
// happened to the caller's context. Generous on purpose: this CLI's PoE writes
// involve a sub-mode change, a confirmation exchange on a passive class, and a
// stale-prompt drain (poe.go), and the alternative to waiting is a dark port.
const restoreBudget = 60 * time.Second

// setPortClass sets ONE port's PoE class from inside Global Config, answering
// the confirmation a passive mode asks and recognizing this command's own
// rejection vocabulary.
//
// Shares the exchange ApplyPoE uses ([Client.setOpmode], [rePoERejected]) rather
// than re-deriving it: the confirmation prompt and the two rejection forms were
// captured once, and a second copy of that knowledge would drift.
func (c *Client) setPortClass(ctx context.Context, port, class string) error {
	var mode, confirmRequired, known = opModeOfClass(class)
	if !known {
		return fmt.Errorf("class %q is not one this connector can set", class)
	}
	var _, ifErr = c.cli.run(ctx, "interface "+cliPortName(port), cmdTimeout)
	if ifErr != nil {
		return ifErr
	}
	var out, setErr = c.setOpmode(ctx, mode, confirmRequired)
	if setErr != nil {
		_, _ = c.cli.run(ctx, "exit", cmdTimeout)
		return setErr
	}
	var rej = rePoERejected.FindStringSubmatch(out)
	if rej != nil {
		_, _ = c.cli.run(ctx, "exit", cmdTimeout)
		return fmt.Errorf("refused (%s): this jack does not deliver %q on this hardware",
			rej[1], class)
	}
	var _, exErr = c.cli.run(ctx, "exit", cmdTimeout)
	return exErr
}
