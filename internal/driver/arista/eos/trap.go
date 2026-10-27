package eos

// Trap destination — "snmp-server host", v2c only, decided by read-compare
// against the running configuration.
//
// # The trap channel is TWO commands, and forgetting the second is silent
//
// "snmp-server host A version 2c C" declares the recipient; "snmp-server
// enable traps snmp linkdown linkup …" decides which events generate a trap at
// all. A device carrying the host line alone is configured and mute — the exact
// failure mode the trap batch exists to avoid (cf. api/trapdest.go) — so both are
// read, both are compared, and conformance requires both.
//
// # The entry key is host + community, and that is why Replaced exists here
//
// EOS does not replace a "snmp-server host" line whose community differs: it
// ADDS one. A stale line naming OUR collector with a rotated community would
// therefore survive every apply, inert — its traps rejected by the proxy filter —
// while occupying a slot in a table whose ceiling is documented nowhere. Such a
// line is removed before the pose and reported as Replaced. A line naming ANY
// OTHER address is a third party's supervision and is never touched, only counted
// (cf. driver.TrapPusher).
//
// # Ordered forms, HERE and not in apply.go
//
// apply.go states that this platform has one trunk syntax and therefore no
// fallbacks. Traps are the one place that reasoning does not carry over: the
// optional "traps" keyword on the host line is printed back by some trains and
// omitted by others, and the per-event keywords on "enable traps snmp" are
// refused wholesale by a parser that only knows the bare class form. Both are
// genuinely two spellings of one intent — unlike a rejected trunk line, which on
// EOS means the command was wrong — so they are tried in order and the accepted
// one is recorded in the transcript.

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"meshcraft/internal/driver"
)

// Compile-time: this Admin can declare a trap recipient.
var _ driver.TrapPusher = (*Client)(nil)

// linkTrapEvents are the "snmp-server enable traps snmp" keywords this
// connector asks for. linkdown/linkup are what port-flap correlation consumes;
// coldstart/warmstart say the device rebooted, which polling only discovers a
// cycle later. AUTHENTICATION IS NOT ADDED: enabling event classes nobody asked
// for is how a third party's carefully pruned trap configuration gets noisier on
// every apply.
var linkTrapEvents = []string{"linkdown", "linkup", "coldstart", "warmstart"}

// trapPortOf: 0 means the standard port (cf. driver.TrapDestination.Port).
func trapPortOf(p int) int {
	if p == 0 {
		return 162
	}
	return p
}

// trapEntryConforms: is this running-config line EXACTLY the recipient asked for?
// Version must be spelled "2c": an empty version is v1 on this platform, and a
// v1 trap does not carry the fields a v2c collector reads.
func trapEntryConforms(r trapReceiver, want driver.TrapDestination) bool {
	return r.Host == want.Address &&
		!r.Informs &&
		r.Version == "2c" &&
		r.Community == want.Community &&
		trapPortOf(r.Port) == trapPortOf(want.Port)
}

// staleTrapEntries: the lines that name OUR collector address without being the
// wanted entry. Lines toward any other address never appear here.
func staleTrapEntries(have []trapReceiver, want driver.TrapDestination) []trapReceiver {
	var out []trapReceiver
	var r trapReceiver
	for _, r = range have {
		if r.Host == want.Address && !trapEntryConforms(r, want) {
			out = append(out, r)
		}
	}
	return out
}

// thirdPartyTrapCount: recipients pointing elsewhere. Counted, never removed.
func thirdPartyTrapCount(have []trapReceiver, want driver.TrapDestination) int {
	var n = 0
	var r trapReceiver
	for _, r = range have {
		if r.Host != want.Address {
			n++
		}
	}
	return n
}

// trapLinkEventsCovered: does "snmp-server enable traps snmp …" already carry
// linkdown AND linkup? Those two are the floor — a recipient that never hears a
// flap is polling with extra steps. coldstart/warmstart are asked for on write
// but not REQUIRED here: a configuration that covers the links must not be
// rewritten for the reboot niceties alone.
func trapLinkEventsCovered(events []string) bool {
	var up, down = false, false
	var e string
	for _, e = range events {
		switch e {
		case "linkup":
			up = true
		case "linkdown":
			down = true
		}
	}
	return up && down
}

// trapHostLine renders the recipient as the command this connector poses.
func trapHostLine(want driver.TrapDestination, withTrapsKeyword bool) string {
	var b strings.Builder
	b.WriteString("snmp-server host " + want.Address)
	if withTrapsKeyword {
		b.WriteString(" traps")
	}
	b.WriteString(" version 2c " + want.Community)
	if trapPortOf(want.Port) != 162 {
		b.WriteString(" udp-port " + strconv.Itoa(want.Port))
	}
	return b.String()
}

func trapHostCandidates(want driver.TrapDestination) [][]string {
	return [][]string{
		{trapHostLine(want, false)},
		{trapHostLine(want, true)},
	}
}

func trapEnableCandidates() [][]string {
	return [][]string{
		{"snmp-server enable traps snmp " + strings.Join(linkTrapEvents, " ")},
		{"snmp-server enable traps snmp"},
	}
}

// trapRemoveCandidates: the ordered "no" forms for one stale entry. The full
// spelling first — "no" wants the line as it stands — then the community-only
// form, then the bare address, which removes every entry for that host:
// acceptable precisely because every entry this function is ever given names OUR
// collector.
func trapRemoveCandidates(r trapReceiver) [][]string {
	var full = "no snmp-server host " + r.Host
	if r.Version != "" {
		full += " version " + r.Version
	}
	full += " " + r.Community
	if r.Port != 0 {
		full += " udp-port " + strconv.Itoa(r.Port)
	}
	return [][]string{
		{full},
		{"no snmp-server host " + r.Host + " " + r.Community},
		{"no snmp-server host " + r.Host},
	}
}

// runAlternatives runs ordered command sets: the first whose commands ALL pass
// wins. Returns (applied?, refusal detail).
//
// Local to this file on purpose. apply.go deliberately carries no fallback
// machinery — one trunk syntax, one form — and putting a general-purpose
// candidate runner in the shared part of the package would invite the next
// gesture to guess at forms instead of knowing them. The two places that
// genuinely have two spellings are both in this file.
func (c *Client) runAlternatives(ctx context.Context, label string, alts [][]string) (bool, string) {
	var refusals []string
	var alt []string
	var i int
	for i, alt = range alts {
		var err = c.runConfigBlock(ctx, alt)
		if err == nil {
			var how = ""
			if i > 0 {
				how = fmt.Sprintf(" (fallback form %d)", i+1)
			}
			c.step(true, label+how)
			return true, ""
		}
		refusals = append(refusals, firstLine(err.Error()))
		c.step(false, label+": form rejected", firstLine(err.Error()))
	}
	return false, strings.Join(refusals, " ; ")
}

// ApplyTrapReceiver declares the collector as the trap destination
// (driver.TrapPusher), verifies by re-read, then PERSISTS — a destination that
// disappears on reboot silently ends reactivity, which is this write's whole
// point.
func (c *Client) ApplyTrapReceiver(ctx context.Context,
	want driver.TrapDestination) (driver.TrapResult, error) {

	var out = driver.TrapResult{
		Version: "v2c", Address: want.Address, Port: trapPortOf(want.Port),
	}

	// --- Refusals, BEFORE contacting the device ---

	if strings.TrimSpace(want.User) != "" {
		return out, driver.NotSupported("arista-eos", "declare an SNMPv3 trap destination",
			"this connector declares v1/v2c only (cf. snmp.go): no v3 user has ever been "+
				"posed by it, and a \"snmp-server host … version 3\" naming an absent user "+
				"registers without error and emits nothing authenticable. The collector would "+
				"drop a v3 trap anyway — it carries no community")
	}
	if strings.TrimSpace(want.Address) == "" {
		return out, fmt.Errorf("no collector address supplied: it is ASKED of the collector " +
			"proxy and never deduced — only its kernel has the routing table that says by " +
			"which address it reaches this device")
	}
	if want.Community == "" {
		return out, fmt.Errorf("no trap community supplied: it must be the one the proxy " +
			"POLLS with, otherwise the collector rejects every trap silently — the constraint " +
			"comes from the collector, not from EOS, whose host-line community is free text")
	}
	if strings.ContainsAny(want.Address, " \t\r\n") ||
		strings.ContainsAny(want.Community, " \t\r\n") {
		return out, fmt.Errorf("address or community contains whitespace: on a " +
			"space-separated CLI line that would silently change the command's meaning, so it " +
			"is refused rather than quoted-and-hoped")
	}
	if want.Port < 0 || want.Port > 65535 {
		return out, fmt.Errorf("trap port %d is not a UDP port", want.Port)
	}
	if c.cli == nil {
		return out, fmt.Errorf("no open session on this access")
	}
	var werr = c.requireWritableDialect()
	if werr != nil {
		return out, werr
	}

	// --- Current state ---

	var runOut, err = c.cli.run(ctx, "show running-config", showRunTimeout)
	if err != nil && runOut == "" {
		return out, fmt.Errorf("pre-write read failed: %v", err)
	}
	var rc = parseRunningConfig(runOut, c.host)
	out.Known = len(rc.TrapReceivers)

	if len(rc.Communities) == 0 {
		// The agent is off until a community exists (cf. ApplySNMP). A recipient
		// posed on a silent agent is an exact configuration and total silence — the
		// order between the two writes is not negotiable.
		return out, fmt.Errorf("SNMP agent OFF on this device (no \"snmp-server community\" " +
			"line): a trap recipient would be set exactly there and emit nothing. GESTURE TO " +
			"MAKE FIRST: pose the SNMP identity (ApplySNMP), which turns the agent on along " +
			"the way")
	}

	var third = thirdPartyTrapCount(rc.TrapReceivers, want)
	if third > 0 {
		out.Notes = append(out.Notes, fmt.Sprintf("%d recipient(s) pointing at another "+
			"address are present and were not touched: cutting a third party's supervision "+
			"to install one's own would be the worst possible service.", third))
	}

	var haveEntry = false
	var r trapReceiver
	for _, r = range rc.TrapReceivers {
		if trapEntryConforms(r, want) {
			haveEntry = true
		}
	}
	var eventsOK = trapLinkEventsCovered(rc.SNMPTrapEvents)
	var stale = staleTrapEntries(rc.TrapReceivers, want)

	// --- Already conforming? Then NOTHING is written (cf. driver.TrapResult.Set) ---

	if haveEntry && eventsOK && len(stale) == 0 {
		c.step(true, "trap destination already declared",
			trapHostLine(want, false)+" present, linkdown/linkup traps enabled — nothing to write")
		out.Notes = append(out.Notes, "The device already carried this recipient: nothing was "+
			"rewritten. That does not prove its traps ARRIVE — the only proof is the first "+
			"trap received by the collector.")
		return out, nil
	}

	// --- Stale entries toward OUR address: removed, best-effort ---
	//
	// Best-effort because a removal the CLI refuses leaves an INERT line (its
	// community is rejected by the collector's filter), whereas failing the whole
	// gesture would leave the device with no working recipient at all.
	var removedAny = false
	for _, r = range stale {
		var okRm, detail = c.runAlternatives(ctx,
			"stale recipient removed: "+r.Host+" (community mismatch)", trapRemoveCandidates(r))
		if okRm {
			removedAny = true
			continue
		}
		out.Notes = append(out.Notes, "a stale recipient toward "+r.Host+" could not be "+
			"removed ("+detail+"): it stays in the device, inert — its community is not the "+
			"polling one, so the collector rejects its traps.")
	}

	// --- The two writes ---

	if !haveEntry {
		var okHost, detail = c.runAlternatives(ctx,
			"trap recipient declared: "+want.Address, trapHostCandidates(want))
		if !okHost {
			return out, fmt.Errorf("the CLI refused every known form of \"snmp-server host\" "+
				"(%s) — command set written against EOS %s from documentation, survey this "+
				"device's forms", detail, DocumentedRelease)
		}
	}
	if !eventsOK {
		var okEv, detail = c.runAlternatives(ctx,
			"link traps enabled: "+strings.Join(linkTrapEvents, " "), trapEnableCandidates())
		if !okEv {
			return out, fmt.Errorf("recipient declared but \"snmp-server enable traps snmp\" "+
				"was refused in every known form (%s): the line is in the device and no port "+
				"flap will ever use it — that is a mute supervision, said rather than implied",
				detail)
		}
	}

	// --- The re-read is authoritative, not the return codes ---

	runOut, err = c.cli.run(ctx, "show running-config", showRunTimeout)
	if err != nil && runOut == "" {
		out.Notes = append(out.Notes, "write accepted but NOT RE-READ ("+firstLine(err.Error())+
			"): meshcraft cannot confirm the device retained it.")
		return out, nil
	}
	var after = parseRunningConfig(runOut, c.host)
	var confirmed = false
	for _, r = range after.TrapReceivers {
		if trapEntryConforms(r, want) {
			confirmed = true
		}
	}
	if !confirmed || !trapLinkEventsCovered(after.SNMPTrapEvents) {
		c.step(false, "trap destination absent after write",
			"the CLI accepted the commands but the running configuration does not carry them")
		out.Notes = append(out.Notes, "WRITE ACCEPTED BUT NOT CONFIRMED by the re-read: a "+
			"return code that changes nothing is the silent failure this re-read exists to "+
			"catch. Nothing was persisted.")
		return out, nil
	}
	out.Set = true
	out.Replaced = removedAny && len(staleTrapEntries(after.TrapReceivers, want)) == 0
	c.step(true, "trap destination written and re-read",
		trapHostLine(want, false)+" ; linkdown/linkup/coldstart/warmstart enabled")
	out.Notes = append(out.Notes, "The line is in the device, which does NOT prove its traps "+
		"arrive: a firewall, an asymmetric route or an ACL on the management VLAN is enough "+
		"to emit into the void. The proof is the first trap received.")

	// --- Persist: a destination a reboot forgets is supervision that ends silently ---

	var perr = c.save(ctx)
	if perr != nil {
		out.Notes = append(out.Notes, "recipient applied but NOT persisted: "+
			firstLine(perr.Error())+" — a reboot would silence the trap channel")
		c.step(false, "startup-config save failed", firstLine(perr.Error()))
		return out, nil
	}
	c.step(true, "configuration persisted", SaveCmd)
	return out, nil
}
