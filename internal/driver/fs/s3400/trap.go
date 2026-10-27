package s3400

// Trap destination — "snmp-server host", v2c only.
//
// # This range has NO trap enable switch, and that is the whole shape of the file
//
// CAPTURED on poefs3400lab (Version 2.0.2J) on 2026-08-10, and all of it new —
// nothing here had ever been exercised on an S3400 before that day:
//
//   - there is NO "snmp-server enable traps". "snmp-server enable ?" answers
//     "Unknown command". Traps are enabled BY DECLARING A HOST, so the one
//     command below is the whole gesture — the second half the Cisco connector
//     has to remember (cisco/ios/trap.go: a host line without "enable traps"
//     is a device configured and mute) does not exist here to be forgotten;
//   - "snmp-server host <A.B.C.D> version {v1|v2c|v3} <community>" — VERSION
//     PRECEDES THE COMMUNITY, and the token is "v2c", not the Cisco "2c";
//   - "snmp-server keep-alive <1-100000>" (seconds) is a periodic keepalive
//     trap — the unconditional emitter, needing no cabled port, that made the
//     probe measurable. It is deliberately NOT written by this connector: it
//     adds periodic traffic nobody asked for, and the link traps this channel
//     exists for are emitted without it. The form is recorded here because it is
//     the only way to make an S3400 emit on demand during a future survey;
//   - REMOVAL NEEDS THE FULL FORM: "no snmp-server host <ip> version v2c
//     <community>". The short "no snmp-server host <ip>" answers "Incomplete
//     command" AND SILENTLY LEAVES THE HOST IN PLACE. See below.
//
// The emission itself was measured, not assumed: the device's trap counter moved
// 0 → 3 → 7 PDUs, the controller logged "trap accepted outside stored
// destination" for poefs3400lab, and after the destination was removed the
// counter froze at 7 across a six-minute recheck. So this platform emits, and the
// product's collector accepts what it emits.
//
// # THE SILENT LEAVE, which is the one failure this file is built around
//
// A removal in the short form is not a removal. The CLI prints "Incomplete
// command" — which [reCLIError] catches, so [Client.configBlock] does return an
// error — and the host STAYS DECLARED. Two ways that becomes a false success:
// sending the short form and ignoring the return, or sending it, seeing the
// error, and reporting "removed" anyway because the wanted entry was written
// right after. Both put "destination replaced" on screen over a device still
// emitting to the old one.
//
// So: the short form is NEVER SENT (trapRemoveLine builds the full one or builds
// nothing), and removal is judged ONLY by the re-read — [driver.TrapResult.Replaced]
// is set from what the device shows afterwards, never from what the CLI answered.
// A line this connector cannot spell in full is left alone and NAMED, because the
// alternative is sending a command that is refused in a way that reads like
// success.
//
// # WHAT IS CAPTURED
//
// CAPTURED 2026-08-10: the four forms above; that this platform emits and that
// the collector accepts it; that the short removal leaves the host.
//
// CAPTURED 2026-08-11, and it used to be the one DEDUCTION this file rested on:
// HOW "show running-config" RENDERS the host line. A host was posed on
// poefs3400lab and the configuration read back through [cliConn.runPaged] —
// testdata/show_running_config_snmpv3_traphost.txt, replayed by
// TestParseTrapHostFromCapture:
//
//	typed:  snmp-server host 10.42.0.67 version v2c mcprobe
//	stored: snmp-server host 10.42.0.67 version v2c mcprobe
//
// The tokens come back IN THE ORDER TYPED, which is exactly what
// [parseTrapHostLine] assumed. One trailing space is added, like every other
// "snmp-server" line; unlike the community line, no case is changed and NO
// encryption-type digit is inserted (the v3 user line on the same capture does
// get one: "auth sha <pass>" is stored "auth sha 0 <pass>"). So the tail is
// still read token by token — that costs nothing and keeps a firmware that
// reorders from being misread — but it is no longer a guess.
//
// The failure mode remains the SAFE one. If a future firmware renders it
// differently, the re-read below does not recognize the line, the write is
// reported as NOT CONFIRMED, and this function returns an ERROR — so nothing is
// recorded, the operator sees which read to capture, and the entry re-opens. It
// cannot produce a false success; it can only produce a false failure over a
// device that is in fact configured.
//
// # Why an unconfirmed write is an ERROR here and a NOTE on Cisco
//
// cisco/ios/trap.go returns a nil error with a note when its re-read does not
// carry the line. Here a nil error makes api.applyTrapDestToDevice call
// SetTrapDestination, which RECORDS the destination — "a stored destination the
// device did not retain is exactly the lie this batch exists to avoid"
// (api/trapdest.go). The rendering is captured now, so the strictness is no
// longer covering a guess; it is covering the case where a firmware revision
// changes the rendering under a fleet already configured, and the answer there
// is still "say it failed", not "record it anyway".
//
// # What is NOT expressible on this range, said out loud
//
// A UDP PORT. The captured form has no "udp-port" keyword and none was
// surveyed, so a destination on any port but 162 is REFUSED rather than written
// to the standard port — accepting a port that will not be written would leave
// the collector listening on a port the device never speaks to (cf.
// driver.TrapDestination.Port, which asks for exactly this refusal).
//
// A V3 DESTINATION. "version v3" is in the captured grammar, but what its last
// argument means there — a user, a community, a security level — is not, and
// neither is what a v3 host line renders back as. [Client.ApplySNMP] still
// refuses to WRITE a v3 identity on this range (snmp.go, and TODO.md says why:
// this CLI has no separate privacy passphrase), so the refusal here rests on two
// gaps at once and not one.

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"meshcraft/internal/driver"
)

// Compile-time: this Admin can declare a trap recipient.
var _ driver.TrapPusher = (*Client)(nil)

// trapCfgTimeout is the budget for "show running-config" — the same 90 s
// ApplySNMP and ReadState give it, on a pager that answers a screen at a time.
const trapCfgTimeout = 90 * time.Second

// trapHostLine renders the recipient as the CAPTURED command.
//
// "version" before the community, and "v2c" spelled with its v: both are the
// corrections the 2026-08-10 survey made to the form this connector would
// otherwise have borrowed from the Cisco one.
func trapHostLine(want driver.TrapDestination) string {
	return "snmp-server host " + want.Address + " version v2c " + want.Community
}

// trapRemoveLine builds the FULL removal form for a line the device showed, or
// returns false when it cannot.
//
// Returning false is the point of the function. The short form silently leaves
// the host (see the file header), so a line whose version or community the
// re-read did not carry is a line meshcraft has no safe command for. The caller
// names it instead of sending something that is refused in a way that reads like
// success.
func trapRemoveLine(r TrapReceiver) (string, bool) {
	if r.Host == "" || r.Version == "" || r.Community == "" {
		return "", false
	}
	return "no snmp-server host " + r.Host + " version " + r.Version + " " + r.Community, true
}

// trapEntryConforms: is this line EXACTLY the recipient asked for?
//
// The version must read "v2c". An entry toward the right address with the
// right community but at v1 is NOT conformant: a v1 trap does not carry the
// fields a v2c collector reads, and it would be reported as a working channel.
func trapEntryConforms(r TrapReceiver, want driver.TrapDestination) bool {
	return r.Host == want.Address &&
		r.Version == "v2c" &&
		r.Community == want.Community
}

// staleTrapEntries: the lines naming OUR collector without being the wanted one.
//
// Lines toward any other address never appear here — they are somebody else's
// supervision, counted and never touched (cf. driver.TrapPusher).
func staleTrapEntries(have []TrapReceiver, want driver.TrapDestination) []TrapReceiver {
	var out []TrapReceiver
	var r TrapReceiver
	for _, r = range have {
		if r.Host == want.Address && !trapEntryConforms(r, want) {
			out = append(out, r)
		}
	}
	return out
}

// thirdPartyTrapCount: recipients pointing elsewhere. Counted, never removed.
func thirdPartyTrapCount(have []TrapReceiver, want driver.TrapDestination) int {
	var n = 0
	var r TrapReceiver
	for _, r = range have {
		if r.Host != want.Address {
			n++
		}
	}
	return n
}

// hasTrapEntry: does the device carry the wanted recipient?
func hasTrapEntry(have []TrapReceiver, want driver.TrapDestination) bool {
	var r TrapReceiver
	for _, r = range have {
		if trapEntryConforms(r, want) {
			return true
		}
	}
	return false
}

// checkTrapDest refuses, BEFORE the device is contacted, everything this range
// cannot express. A half-written destination is worse than one never started.
func checkTrapDest(want driver.TrapDestination) error {
	if strings.TrimSpace(want.User) != "" {
		return driver.NotSupported("fs-s3400", "declare an SNMPv3 trap destination",
			"\"version v3\" appears in this range's captured \"snmp-server host\" grammar and "+
				"NOTHING ELSE about it does: what the last argument means there — a user, a "+
				"community, a security level — was never surveyed, and no capture shows how a v3 "+
				"host line reads back. A destination written from a guessed grammar registers "+
				"without error and emits nothing authenticable, which no screen tells apart from "+
				"a quiet network. The v2c destination is written and verified; this one waits for "+
				"a \"snmp-server host … version v3 …\" typed at a real S3400 and read back")
	}
	if strings.TrimSpace(want.Address) == "" {
		return fmt.Errorf("no collector address supplied: it is ASKED of the collector proxy " +
			"and never deduced — only its kernel has the routing table that says by which " +
			"address it reaches this device")
	}
	var ip = net.ParseIP(strings.TrimSpace(want.Address))
	if ip == nil || ip.To4() == nil {
		return fmt.Errorf("collector address %q is not an IPv4 address: the captured form is "+
			"\"snmp-server host <A.B.C.D> …\" and no other shape — a name or an IPv6 literal — "+
			"has ever been sent to this CLI", want.Address)
	}
	if want.Community == "" {
		return fmt.Errorf("no trap community supplied: it must be the one the proxy POLLS " +
			"with, otherwise the collector rejects every trap silently. The constraint comes " +
			"from the collector (classifyTrap), not from this CLI, whose host-line community " +
			"is free text")
	}
	if !validCommunity(want.Community) {
		return fmt.Errorf("trap community rejected: this CLI takes it as a bare word on a " +
			"space-separated line, so spaces and quotes cannot be sent safely")
	}
	if want.Port != 0 && want.Port != 162 {
		return driver.NotSupported("fs-s3400",
			fmt.Sprintf("send traps to UDP port %d", want.Port),
			"the captured \"snmp-server host\" form on this range carries no port keyword and "+
				"none was surveyed. Writing the line anyway would send traps to 162 while the "+
				"collector waited on another port — a supervision that is configured on both "+
				"ends and silent in between. 162 or nothing, until the form is captured")
	}
	return nil
}

// ApplyTrapReceiver declares the collector as the trap destination
// (driver.TrapPusher), CONFIRMS it by re-read, then persists — a destination a
// reboot forgets ends reactivity silently, which is this write's whole point.
func (c *Client) ApplyTrapReceiver(ctx context.Context,
	want driver.TrapDestination) (driver.TrapResult, error) {

	var out = driver.TrapResult{Version: "v2c", Address: want.Address, Port: 162}

	// --- Refusals, before contacting the device ---

	var err = checkTrapDest(want)
	if err != nil {
		return out, err
	}
	if c.cli == nil {
		return out, fmt.Errorf("no open session on this access")
	}
	err = c.requireWritable()
	if err != nil {
		return out, err
	}

	// --- Current state ---
	//
	// A TRUNCATED READ IS REFUSED, NOT PARSED, for the reason ReadState refuses
	// one: this pager cuts long output, the "snmp-server" block sits at the end
	// of the configuration, and a prefix that stops before it is indistinguishable
	// from a device carrying no destination at all. Deciding "nothing is
	// declared" from a read that never reached the declarations would miss a
	// stale entry AND report a Known of zero on a device with three.
	var cfgOut string
	cfgOut, err = c.cli.runPaged(ctx, "show running-config", trapCfgTimeout)
	if err != nil && (strings.TrimSpace(cfgOut) == "" || isTruncated(err)) {
		c.step(false, "show running-config failed before the trap write",
			firstLine(err.Error()))
		return out, fmt.Errorf("pre-write read on %s: %v — this read is where the existing "+
			"destinations are, and a PREFIX of it says nothing about the ones it never "+
			"reached", c.host, err)
	}
	var before = ParseRunningConfig(cfgOut)
	out.Known = len(before.TrapReceivers)

	// The agent's own state, and the reason this is a refusal rather than a note:
	// the collector accepts a trap only if the community it carries equals the one
	// it POLLS the device with (classifyTrap). A device with no community
	// configured is polled by nothing, so every trap this line produced would be
	// dropped by the collector — an exact configuration and total silence, which
	// is the case driver.TrapPusher tells connectors to refuse.
	if len(before.Communities) == 0 {
		return out, fmt.Errorf("no \"snmp-server community\" on this device: a trap " +
			"recipient set here would emit traps the collector drops, because it only " +
			"accepts a community it also POLLS with. GESTURE TO MAKE FIRST: pose the SNMP " +
			"identity (ApplySNMP)")
	}
	var known = false
	var comm string
	for _, comm = range before.Communities {
		if comm == want.Community {
			known = true
		}
	}
	if !known {
		out.Notes = append(out.Notes, fmt.Sprintf("the trap community %q is NOT among the "+
			"communities configured on this device (%s): the line is written as asked, but "+
			"the collector only accepts a trap whose community equals the one it polls with, "+
			"so unless the proxy polls with %q these traps will be dropped on arrival",
			want.Community, strings.Join(before.Communities, ", "), want.Community))
	}

	var third = thirdPartyTrapCount(before.TrapReceivers, want)
	if third > 0 {
		out.Notes = append(out.Notes, fmt.Sprintf("%d recipient(s) pointing at another "+
			"address are present and were not touched: cutting a third party's supervision "+
			"to install one's own would be the worst possible service.", third))
	}

	var stale = staleTrapEntries(before.TrapReceivers, want)
	var haveEntry = hasTrapEntry(before.TrapReceivers, want)

	// --- Already conforming, and nothing stale? Then NOTHING is written ---

	if haveEntry && len(stale) == 0 {
		c.step(true, "trap destination already declared",
			trapHostLine(want)+" present — nothing rewritten")
		out.Notes = append(out.Notes, "The device already carried this recipient: nothing was "+
			"rewritten, and no configuration was saved. That does not prove its traps ARRIVE — "+
			"the only proof is the first trap received by the collector.")
		return out, nil
	}

	// --- The writes: the stale removals first, then the host line ---
	//
	// ONE config block, because each command is waited for individually inside it
	// (configBlock) and a removal that fails must not stop the pose: a device left
	// with a stale line and no working one would be strictly worse than one
	// carrying both.
	var cmds []string
	var attempted []TrapReceiver
	var r TrapReceiver
	for _, r = range stale {
		var rm, ok = trapRemoveLine(r)
		if !ok {
			// THE CAPTURED TRAP, refused rather than attempted: the only command
			// this connector could build for such a line is the short form, which
			// answers "Incomplete command" and leaves the host declared.
			out.Notes = append(out.Notes, fmt.Sprintf("a divergent recipient toward %s was "+
				"left in place: the device rendered it \"%s\", which does not carry the "+
				"version and community the FULL removal form needs. The short form \"no "+
				"snmp-server host %s\" is answered \"Incomplete command\" and LEAVES THE HOST "+
				"DECLARED (captured 2026-08-10), so nothing was sent — a command refused in a "+
				"way that reads like success is worse than a line named and left alone",
				r.Host, r.Raw, r.Host))
			continue
		}
		cmds = append(cmds, rm)
		attempted = append(attempted, r)
	}
	if !haveEntry {
		cmds = append(cmds, trapHostLine(want))
	}
	if len(cmds) == 0 {
		// The wanted recipient is already there and every divergent line beside it
		// is one this connector has no safe command for (the notes above name each
		// of them). Sending nothing is the answer; entering configuration mode to
		// send an empty block would only put a write in the transcript.
		c.step(true, "trap destination already declared, divergent lines left alone",
			trapHostLine(want)+" present — see the notes for what could not be removed")
		return out, nil
	}

	var cerr = c.configBlock(ctx, cmds)
	if cerr != nil {
		// NOT returned yet: the block runs every command it was given, so the pose
		// may have landed even when a removal was refused. The re-read decides.
		c.step(false, "some trap command was refused", firstLine(cerr.Error()))
		out.Notes = append(out.Notes, "the CLI refused at least one of the trap commands ("+
			firstLine(cerr.Error())+"); what follows is what the device shows afterwards, "+
			"which is the only thing that decides")
	}

	// --- The re-read is authoritative, not the return codes ---

	cfgOut, err = c.cli.runPaged(ctx, "show running-config", trapCfgTimeout)
	if err != nil && (strings.TrimSpace(cfgOut) == "" || isTruncated(err)) {
		c.step(false, "trap destination SENT and NOT confirmed", firstLine(err.Error()))
		return out, fmt.Errorf("\"snmp-server host\" was sent to %s and the confirming read "+
			"did not run to the prompt (%v): nothing is reported as written — a return code "+
			"is not an observation — but the command DID go out, so re-read the device before "+
			"deciding this was a failed write rather than a failed verification", c.host, err)
	}
	var after = ParseRunningConfig(cfgOut)
	if !hasTrapEntry(after.TrapReceivers, want) {
		c.step(false, "trap destination absent after write",
			"the CLI accepted \""+trapHostLine(want)+"\" and the running configuration does "+
				"not carry it")
		return out, fmt.Errorf("the trap destination was accepted by the CLI and \"show "+
			"running-config\" on %s does not show it: either the command configured nothing, "+
			"or this firmware renders the line in a shape parseTrapHostLine does not read — "+
			"that rendering is DEDUCED on this range (trap.go). Nothing is reported as "+
			"written, and the running configuration is where to look", c.host)
	}

	// Removal is judged HERE and nowhere else: the CLI's answer to a "no
	// snmp-server host" is not evidence that the host is gone.
	var leftBehind = staleTrapEntries(after.TrapReceivers, want)
	out.Replaced = len(attempted) > 0 && len(leftBehind) == 0
	if len(attempted) > 0 && len(leftBehind) > 0 {
		var raws []string
		for _, r = range leftBehind {
			raws = append(raws, "\""+r.Raw+"\"")
		}
		out.Notes = append(out.Notes, fmt.Sprintf("the wanted recipient is declared, and %d "+
			"divergent line(s) toward the same address SURVIVED the removal: %s. On this range "+
			"a removal in the short form is answered \"Incomplete command\" and leaves the "+
			"host declared, so a survivor is reported rather than assumed gone. They stay "+
			"inert — their community is not the polling one, so the collector drops their "+
			"traps — but they occupy a place in a table whose ceiling is documented nowhere",
			len(leftBehind), strings.Join(raws, ", ")))
	}

	// Set means A WRITE HAPPENED AND WAS CONFIRMED, so it stays false when the
	// device already carried the recipient and only a stale neighbour was removed
	// (cf. driver.TrapResult.Set) — Replaced is what reports that case.
	out.Set = !haveEntry
	if out.Set {
		c.step(true, "trap destination written and re-read", trapHostLine(want))
	} else {
		c.step(true, "trap destination confirmed, divergent line(s) removed",
			trapHostLine(want)+" was already declared")
	}
	out.Notes = append(out.Notes, "The line is in the device, which does NOT prove its traps "+
		"arrive: a firewall, an asymmetric route or an ACL on the management VLAN is enough "+
		"to emit into the void. The proof is the first trap received (api.confirmTrapPath).")
	out.Notes = append(out.Notes, "This range has no \"snmp-server enable traps\" — declaring "+
		"a host IS what enables emission (captured 2026-08-10), so there is no second switch "+
		"left off. Per-interface link-trap suppression was not audited port by port.")

	// --- Persist: a destination a reboot forgets ends supervision silently ---

	var perr = c.Persist(ctx)
	if perr != nil {
		out.Notes = append(out.Notes, "recipient declared but NOT persisted: "+
			firstLine(perr.Error())+" — a reboot would silence the trap channel")
	}
	return out, nil
}
