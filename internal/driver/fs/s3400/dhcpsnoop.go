package s3400

// DHCP snooping — driver.DHCPSnoopReader / driver.DHCPSnoopApplier.
//
// # THE STATUS OF EVERY COMMAND IN THIS FILE, SAID BEFORE ANYTHING ELSE
//
// Until 2026-08-10 this section read "NOT ONE of the write forms below has been
// run on an S3400". That is no longer true, and what replaced it is more useful
// than a disclaimer, because the run answered two different questions:
//
//   - the GLOBAL and PER-VLAN statements were sent to the lab unit through these
//     same builders, were accepted, and were read back out of the running
//     configuration. The vocabulary of those two is settled (§ below);
//   - the PER-PORT one was answered "Unknown command", and the CLI's own
//     completion showed the level is missing rather than misspelled. That form is
//     gone from this file entirely, and every trust intent is refused.
//
// [Driver.WritesExtra] still does NOT declare "dhcp_snooping". The bar the
// contract sets is the gesture being proven, and what ran on 2026-08-10 was a
// harness driving the builders one at a time — not [Client.ApplyDHCPSnooping],
// with its order, its idempotence and its verifying re-read. That is the same
// distinction eos/contract.go draws for "port_admin" between a capture typed at
// a CLI and a capture taken through the applier, and it is why the button stays
// off until the applier itself has run (TODO.md names it).
//
// # Which spelling, and why there are two candidates rather than one guess
//
// This CLI is the BDCOM lineage (cf. s3400.go: "config" not "conf t",
// "switchport pvid" not "switchport access vlan", "ip sshd enable" not
// "ip ssh server enable"). That lineage files DHCP snooping under its
// dhcp-relay family:
//
//	ip dhcp-relay snooping                 (global)
//	ip dhcp-relay snooping vlan 99         (per VLAN)
//	interface g0/1
//	 ip dhcp-relay snooping trust          (per port — DOES NOT EXIST HERE, § below)
//
// which is what snoopFamilyRelay carries and what this connector TRIES FIRST for
// the two statements that turned out to exist.
//
// TWO OF THOSE THREE ARE NOW CAPTURED AND THE THIRD DOES NOT EXIST — 2026-08-10,
// lab S3400 192.0.2.3, OpCode 2.0.2J, driven by TestLiveCaptureSnoopingEnabledShape
// through this connector's own builders (live_snoopcapture_test.go):
//
//	ip dhcp-relay snooping            →  ACCEPTED  (snoopGlobalCmd)
//	ip dhcp-relay snooping vlan 4094  →  ACCEPTED  (snoopVlanCmd), and the running
//	                                     configuration prints it back as
//	                                     "ip dhcp-relay snooping vlan  4094"
//	                                     — note the DOUBLE SPACE, which reSnoopVlan's
//	                                     \s+ absorbs
//	ip dhcp-relay snooping trust      →  "Unknown command"  (the form this file
//	                                     used to build, deleted 2026-08-10)
//
// THE PER-PORT TRUST STATEMENT IS ABSENT FROM THIS PLATFORM, not misspelled, and
// the CLI's own completion is what says so rather than one refusal:
//
//   - inside an "interface" block, "ip ?" offers ACCESS-GROUP and
//     IGMP-SNOOPING and nothing else — there is no "dhcp-relay" under it at all;
//   - at config level, "ip dhcp-relay snooping ?" lists database-agent, db-file,
//     write-time, write-immediately, information, vlan, log, upper_filter and
//     rapid-refresh-bind. No "trust";
//   - "ip dhcp-relay snooping vlan <id> ?" offers only "max-client".
//
// So the third of the contract's three levels cannot be honoured on an S3400
// running 2.0.2J, and this file no longer has a builder for it in EITHER
// direction — a "no … trust" is the same absent command facing the other way.
// Every trust intent in a plan is an ORDINARY REFUSAL in
// [driver.ApplyResult.Skipped] naming the platform fact (cf. [snoopTrustRefusal],
// and instanton/stormcontrol.go for the same shape of "this hardware does not
// have this level" as opposed to "we have not proven it").
//
// The nearest thing this lineage has is the global "ip dhcp-relay snooping
// upper_filter" ("Filter packet port from upper interface"): a switch-wide flag
// that decides the server side by DIRECTION rather than per port. It is NOT a
// per-port exemption, this connector does not write it, and it must not be made
// to pretend it is one — it is named in the refusal only so that an operator who
// needs an exemption knows where this firmware keeps its nearest equivalent.
//
// The IOS-shaped spelling ("ip dhcp snooping …") is kept as a second candidate
// for the two levels that DO exist, because this platform is IOS-like in almost
// every other respect and the cost of being wrong about which of the two it is,
// is one refused command — an unknown command on this CLI configures nothing,
// prints a caret and a sentence, and is caught by reCLIError.
//
// # ARMING THE FEATURE FOR A PLAN THAT WANTED A TRUSTED PORT IS REFUSED
//
// The per-port refusal is not the whole answer, because one case does harm
// rather than nothing. A plan that asks for a TRUSTED port and would also have
// to turn inspection ON — the global enable, or a VLAN this device does not yet
// snoop — is asking for two halves that are not separable: the caller derives
// the plan's VLANs from the membership of the ports it names
// (api/dhcpsnoop_apply.go, dhcpSnoopVLANsFor), so the VLANs about to be armed are
// the ones crossing the very uplink it wanted exempted. Arming them while no port
// can be exempted drops DHCP OFFER/ACK on the path the server answers over: not
// the protection asked for, an outage in its place. That gesture writes NOTHING
// (cf. [snoopArmRefusal]).
//
// It is narrow on purpose. A plan asking only for UNTRUSTED ports does not trip
// it — untrusted is the protected default, and the two levels above it are what
// the operator is asking for — and neither does a plan on a switch that already
// snoops those VLANs, where there is nothing to arm.
//
// A candidate list is how this package already handles a form it cannot prove
// (cf. MgmtOpts.Gateway, which carried ordered candidates until the 2026-08-03
// session settled "ip route default GW"). It is not a licence to guess: the
// apply SAYS which form the device accepted, and VERIFIES BY RE-READING the
// running configuration afterwards, so a form that is accepted and does
// nothing is caught rather than reported as applied.
//
// # Why the read comes out of "show running-config" and not a "show" of its own
//
// CORRECTED 2026-08-08, on the lab S3400 (192.0.2.3, OpCode 2.0.2J): a
// "show ip dhcp-relay snooping" form DOES answer here, and the family question
// this file's header hedged is now settled — THE SPELLING IS `dhcp-relay`:
//
//	show ip dhcp-relay snooping  → answers, printing
//	                               "ip dhcp-relay snooping db-file /dhcpr-database"
//	show ip dhcp snooping        → "Ambiguous Command - It may be one of the
//	                               following : dhcp-relay dhcpd"
//
// So the IOS-shaped spelling is not merely a second-choice candidate on this
// lineage, it cannot resolve at all: `dhcp` is a prefix of both `dhcp-relay` and
// `dhcpd`, and the CLI refuses rather than picking. snoopFamilies keeps both for
// now because the WRITE forms are still unexercised and the plain family costs
// one refused round trip, but the order is now a fact rather than a preference.
//
// The read still comes out of "show running-config" rather than that status
// command. Two reasons, and the second is the one that matters: the status form
// has only been seen with the feature OFF, where it printed a single db-file
// line and nothing about global state, VLANs or per-port trust — so what it
// prints when snooping is ON is still unobserved, and a parser written against
// the off-state shape would be inventing the rest. Inventing it would produce an
// empty read that looks like "the feature is off", the single most dangerous
// wrong answer this contract can give (driver/dhcpsnoop.go, § on the trap).
// "show running-config" is the command this connector has read hundreds of
// times on this platform, and on a CLI that prints only non-default lines, the
// presence of the statement IS the state.
//
// One consequence, and it decides the shape of the read: an UNTRUSTED port
// carries no line at all, exactly like a factory access port carries no
// "switchport" line. So "untrusted" and "never looked at" are the same
// silence in the configuration. This connector therefore reports per-port trust
// ONLY when it recognized the feature as enabled — at that point the vocabulary
// has demonstrably matched this firmware and a missing line really does mean
// untrusted. With the feature off, [driver.DHCPSnoopState.Ports] stays EMPTY
// and a note says why, rather than asserting fifty-two untrusted ports on the
// strength of regexes nothing has confirmed.
//
// CONFIRMED 2026-08-08 on the lab unit, with the feature off and every port
// untrusted: the complete running configuration — 272 lines, pager disabled with
// "terminal length 0" so nothing was cut — carries NO dhcp, snooping or trust
// line whatsoever. The silence is real, and so is the reason this connector
// refuses to read trust out of it.
//
// Worth recording how nearly this went wrong: the first capture was taken
// WITHOUT "terminal length 0" and stopped at a "--More--" after 34 lines. It
// contained no dhcp line either, and would have read as the same confirmation
// while having seen almost none of the file. A pager is the cheapest way to
// mistake a fragment for a fact.

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"meshcraft/internal/driver"
)

// Compile-time assertions, kept beside the implementation rather than in
// contract.go: a capability that lives in one file should fail to build in
// that same file.
var (
	_ driver.DHCPSnoopReader  = (*Client)(nil)
	_ driver.DHCPSnoopApplier = (*Client)(nil)
)

// The two candidate spellings. The prefix is the whole statement for the
// global enable, and the stem of the per-VLAN and per-port forms.
const (
	// snoopFamilyRelay: the BDCOM lineage's own spelling (cf. the header).
	// Tried first.
	snoopFamilyRelay = "ip dhcp-relay snooping"
	// snoopFamilyPlain: the IOS-shaped spelling, second candidate.
	snoopFamilyPlain = "ip dhcp snooping"
)

// snoopFamilies is the ORDER the apply tries. Never reordered at runtime: a
// device that accepted one of them once is re-read, and the family it is
// already configured with wins over this list (cf. ApplyDHCPSnooping).
var snoopFamilies = []string{snoopFamilyRelay, snoopFamilyPlain}

// snoopUnverifiedNote rides every apply result. It is a NOTE and not a Skipped
// entry on purpose: nothing was refused, the commands were really sent — what is
// qualified is how much has been PROVEN about them (cf.
// driver.ApplyResult.Skipped, "not to be confused with Notes").
//
// It was rewritten on 2026-08-10, when the live probe stopped it being a
// blanket disclaimer: the global and per-VLAN forms are captured and accepted on
// OpCode 2.0.2J, and the per-port one was captured being REFUSED. What is left
// unproven is narrower and worth saying exactly, because a note that overstates
// its own doubt is read once and skipped ever after.
const snoopUnverifiedNote = "DHCP snooping forms on this platform: \"ip dhcp-relay snooping\" " +
	"and \"… vlan N\" were sent to a lab S3400 (OpCode 2.0.2J) on 2026-08-10 through these " +
	"same builders, were accepted, and read back from the running configuration. What has " +
	"never run against a device is this APPLIER as a whole — its order, its idempotence and " +
	"its verifying re-read — so what was sent is reported below and re-read, and a silent " +
	"success is worth checking. The per-port trust level is not unproven but ABSENT: see the " +
	"refusals in Skipped if this plan named any port"

var (
	// The global statement, alone on its line. "… snooping vlan 99" does not
	// match it: the line is anchored at both ends.
	reSnoopGlobal = regexp.MustCompile(`(?i)^\s*ip\s+dhcp(-relay)?\s+snooping\s*$`)
	// The per-VLAN statement. The argument is taken as a LIST ("99", "1-10,99")
	// and expanded by the package's own expandVlanList, because every other
	// VLAN-list argument on this CLI accepts that form.
	reSnoopVlan = regexp.MustCompile(`(?i)^\s*ip\s+dhcp(-relay)?\s+snooping\s+vlan\s+(\S+)\s*$`)
	// The per-port trust flag, inside an "interface …" block.
	reSnoopTrust = regexp.MustCompile(`(?i)^\s*ip\s+dhcp(-relay)?\s+snooping\s+trust\s*$`)
)

// snoopFamilyOf maps the regexes' first group ("-relay" or "") back to the
// family constant, so the apply reuses the spelling the DEVICE already uses
// rather than the one this file prefers.
func snoopFamilyOf(suffix string) string {
	if strings.EqualFold(strings.TrimSpace(suffix), "-relay") {
		return snoopFamilyRelay
	}
	return snoopFamilyPlain
}

// --- command builders (pure, so the wire form is asserted in a test) -------

// snoopGlobalCmd is the switch-wide enable. There is deliberately no "no"
// counterpart anywhere in this file: the contract forbids turning the feature
// off (driver/dhcpsnoop.go — "disabling global snooping because the last
// trusted port went away would strip protection from the whole device on the
// strength of one click").
func snoopGlobalCmd(family string) string { return family }

// snoopVlanCmd enables snooping on ONE VLAN. One command per VLAN rather than
// a compacted list: a refusal then names the VLAN it refused, instead of
// failing a whole range because one id in it is unknown to the device.
func snoopVlanCmd(family string, id int) string {
	return fmt.Sprintf("%s vlan %d", family, id)
}

// THERE IS NO PER-PORT BUILDER HERE, and its absence is the point.
//
// A snoopTrustCmds sat at this spot until 2026-08-10. It built
// "interface g0/1 / ip dhcp-relay snooping trust", the live probe sent it, and
// the firmware answered "Unknown command" — with the CLI's own completion
// showing the level is missing rather than misspelled (cf. the file header).
// Deleting the builder rather than leaving it unused is what makes the absence a
// property of the code: there is nothing to call by accident, in either
// direction, and TestNoPlanCanStillEmitThePerPortTrustCommand asserts the string
// never reaches the wire.

// snoopTrustRefusal is the platform fact, written once so the per-port refusal,
// the arming refusal and the capability declaration cannot become three
// different accounts of one piece of hardware.
//
// It names what was OBSERVED — the refusal, the completion, the nearest thing
// the platform does have — rather than reporting a failure, because a port trust
// that cannot exist is not a device that misbehaved (cf.
// instanton/stormcontrol.go, stormRefusal).
const snoopTrustRefusal = "this platform has NO PER-PORT DHCP SNOOPING TRUST LEVEL AT ALL. " +
	"Captured on the lab S3400 (192.0.2.3, OpCode 2.0.2J) on 2026-08-10, through this " +
	"connector's own builders: \"ip dhcp-relay snooping trust\" inside an interface block is " +
	"answered \"Unknown command\", and the CLI's completion says why it always will — inside " +
	"\"interface\", \"ip ?\" offers only ACCESS-GROUP and IGMP-SNOOPING, at config level " +
	"\"ip dhcp-relay snooping ?\" lists nine sub-commands with no \"trust\" among them, and " +
	"\"… vlan <id> ?\" offers only \"max-client\". The level is ABSENT from this firmware, not " +
	"misspelled — there is no command to send, nothing was sent, and no \"force\" would change " +
	"it. The nearest thing this lineage carries is the SWITCH-WIDE \"ip dhcp-relay snooping " +
	"upper_filter\", which decides the server side by direction rather than per port; meshcraft " +
	"does not write it, and it is not the exemption this intent asked for"

// snoopArmRefusal is the second half of the same fact, and it is the one that
// stops an apply doing harm rather than merely doing nothing (cf. the header).
const snoopArmRefusal = "DHCP snooping was NOT armed on this device and nothing was written: " +
	"the plan asks for at least one TRUSTED port, this platform has no per-port trust level, " +
	"and the VLANs this plan would have started inspecting are the ones that cross the very " +
	"port it wanted exempted. Turning inspection on there while no port can be exempted drops " +
	"DHCP OFFER/ACK on the path the server answers over — an outage in place of the protection " +
	"asked for. The device is left exactly as it was found. Arm snooping on the VLANs that " +
	"carry no DHCP server, or accept that this switch cannot exempt an uplink"

// snoopTrustSkipped turns a plan's per-port intents into the refusals they are
// on this platform. PURE, and sorted into faceplate order so two identical plans
// produce two identical results.
//
// The second return says whether any intent asked for TRUST — the one case the
// arming guard cares about. An intent asking for UNTRUSTED is refused just the
// same (it is still a level this firmware does not carry, and the contract
// forbids accepting an intent and reporting success), but its refusal is
// harmless to grant in effect: untrusted is what every port here permanently is.
func snoopTrustSkipped(ports []driver.DHCPSnoopPortIntent) (skipped []string, wantsTrust bool) {
	var intents = make([]driver.DHCPSnoopPortIntent, len(ports))
	copy(intents, ports)
	driver.SortByPortName(intents, snoopIntentPort, splitStandard)
	var it driver.DHCPSnoopPortIntent
	for _, it = range intents {
		if it.Trusted {
			wantsTrust = true
		}
		skipped = append(skipped, fmt.Sprintf("%s: NOT set %s — %s",
			it.Port, trustWord(it.Trusted), snoopTrustRefusal))
	}
	return skipped, wantsTrust
}

// snoopIntentPort names the field the faceplate order is read from.
func snoopIntentPort(p driver.DHCPSnoopPortIntent) string { return p.Port }

// --- read -----------------------------------------------------------------

// parseDHCPSnooping reads the whole running configuration. PURE.
//
// Returns the state and the FAMILY the device is configured with ("" when no
// snooping statement of either spelling appears), which is what lets an apply
// speak the device's own dialect instead of imposing this file's preference.
func parseDHCPSnooping(cfg string) (driver.DHCPSnoopState, string) {
	var st driver.DHCPSnoopState
	var family string
	var d = &Driver{}
	var trusted = map[string]bool{}
	var ports []string
	var vlans = map[int]bool{}
	var curPort string

	var line string
	for _, line = range strings.Split(cfg, "\n") {
		line = strings.TrimRight(line, " \t\r")
		if strings.TrimSpace(line) == "!" {
			curPort = ""
			continue
		}
		var mi = reCfgIface.FindStringSubmatch(line)
		if mi != nil {
			// A new block: the SVIs and Null0 land here too and simply leave
			// curPort empty, exactly as ParseRunningConfig treats them.
			curPort = ""
			var name, ok = d.StandardPortName(mi[1])
			if ok {
				curPort = name
				ports = append(ports, name)
			}
			continue
		}
		if curPort != "" {
			var mt = reSnoopTrust.FindStringSubmatch(line)
			if mt != nil {
				trusted[curPort] = true
				family = snoopFamilyOf(mt[1])
			}
			continue
		}
		var mv = reSnoopVlan.FindStringSubmatch(line)
		if mv != nil {
			var id int
			for _, id = range expandVlanList(mv[2]) {
				vlans[id] = true
			}
			family = snoopFamilyOf(mv[1])
			continue
		}
		var mg = reSnoopGlobal.FindStringSubmatch(line)
		if mg != nil {
			st.Enabled = true
			family = snoopFamilyOf(mg[1])
		}
	}

	var id int
	for id = range vlans {
		st.VLANs = append(st.VLANs, id)
	}
	sort.Ints(st.VLANs)

	if !st.Enabled {
		// Ports stay EMPTY on purpose — cf. the file header: with the feature
		// off, "untrusted" and "not observed" are the same silence, and the
		// contract says an unobserved port must be absent rather than reported
		// as untrusted.
		st.Notes = append(st.Notes,
			"no global DHCP snooping statement appears in the running configuration "+
				"(\""+snoopFamilyRelay+"\" / \""+snoopFamilyPlain+"\" both searched for): "+
				"the feature reads as OFF, and no per-port trust flag is reported — on this "+
				"platform an untrusted port carries no line at all, so \"untrusted\" and "+
				"\"not observed\" would be the same silence")
		if len(st.VLANs) > 0 {
			st.Notes = append(st.Notes,
				"per-VLAN snooping lines ARE present without a global one: on this platform "+
					"the two may be the same fact, and that is exactly what the unrun live "+
					"survey would settle — reported as read rather than resolved by guessing")
		}
		return st, family
	}

	var name string
	for _, name = range ports {
		st.Ports = append(st.Ports, driver.DHCPSnoopPortState{
			Port: name, Trusted: trusted[name],
		})
	}
	// GE before XG and numerically inside each — a UI listing fifty-two ports must
	// not get them in map order. Order from driver.SortByPortName, split from this
	// platform (cf. driver/portsort.go).
	driver.SortByPortName(st.Ports, snoopPortName, splitStandard)
	// The trust column below will read "untrusted" for every socket, and that is
	// not this connector failing to find the flags: THE FLAG DOES NOT EXIST HERE
	// (cf. the file header and [snoopTrustRefusal]). Said in Notes rather than by
	// dropping the ports, because "no port was read" and "every port is in the
	// firmware's permanent untrusted state" are different sentences and this one is
	// the true one — the parser still recognises a trust line, so a firmware that
	// grows the level would show up here instead of being hidden by an empty list.
	st.Notes = append(st.Notes,
		"this firmware has NO PER-PORT TRUST STATEMENT at all (\"ip dhcp-relay snooping "+
			"trust\" is answered \"Unknown command\" on OpCode 2.0.2J, and the CLI's own "+
			"completion offers no such leaf): every port below is untrusted and cannot be "+
			"anything else, and a plan asking to trust one is refused rather than written")
	st.Notes = append(st.Notes,
		"read from \"show running-config\" — this firmware does serve \"show ip dhcp-relay "+
			"snooping\", but that command has only ever been seen with the feature OFF, so "+
			"what it prints when snooping is ON is unobserved; on a CLI that prints only "+
			"non-default lines the presence of the statement IS the state")
	if len(st.VLANs) == 0 {
		st.Notes = append(st.Notes,
			"snooping is enabled globally but covers NO VLAN: it inspects nothing, whatever "+
				"any port's trust flag says")
	}
	return st, family
}

// snoopPortName names the field the faceplate order is read from.
func snoopPortName(p driver.DHCPSnoopPortState) string { return p.Port }

// readSnooping is the read plus the observed family, which the apply needs and
// the contract's signature has no room for.
func (c *Client) readSnooping(ctx context.Context) (driver.DHCPSnoopState, string, error) {
	var out, err = c.cli.runPaged(ctx, "show running-config", 90*time.Second)
	if err != nil && strings.TrimSpace(out) == "" {
		return driver.DHCPSnoopState{}, "", fmt.Errorf("show running-config on %s: %w", c.host, err)
	}
	var st, family = parseDHCPSnooping(out)
	return st, family, nil
}

// ReadDHCPSnooping implements driver.DHCPSnoopReader.
func (c *Client) ReadDHCPSnooping(ctx context.Context) (driver.DHCPSnoopState, error) {
	var st, _, err = c.readSnooping(ctx)
	return st, err
}

// --- write ----------------------------------------------------------------

// ApplyDHCPSnooping implements driver.DHCPSnoopApplier.
//
// TWO LEVELS, not three: the feature is turned on GLOBALLY and on the plan's
// VLANs, and the per-port trust intents are REFUSED — the statement does not
// exist on this firmware (cf. the header, and [snoopTrustRefusal]). Nothing
// per-port is sent, in either direction, so [driver.ApplyResult.PortsWritten]
// stays empty: the field means CONFIRMED, and there is no write to confirm.
//
// Nothing here ever turns the feature off, and no VLAN found already enabled is
// ever removed — snooping this connector did not install is protection it must
// not take away.
//
// AND IT ENDS IN "write" (cf. step 5): this is configuration, the platform
// distinguishes running from startup, and an inspection a reboot undoes is worse
// than one that was refused — it was observed as in place, and the screen will
// keep saying so.
func (c *Client) ApplyDHCPSnooping(ctx context.Context, plan driver.DHCPSnoopPlan) (ApplyResult, error) {
	var res ApplyResult

	// THE PER-PORT REFUSAL IS FIRST, before the session is even checked, so that a
	// plan carrying an impossible intent says so even when the gesture goes on to
	// fail for some ordinary reason. It costs no round trip: the fact is about this
	// firmware, not about this device's mood.
	var refusals, wantsTrust = snoopTrustSkipped(plan.Ports)
	res.Skipped = append(res.Skipped, refusals...)
	if len(refusals) > 0 {
		c.step(false, fmt.Sprintf("per-port DHCP snooping trust refused on %d port(s)",
			len(refusals)), snoopTrustRefusal)
	}

	var err = c.requireWritable()
	if err != nil {
		return res, err
	}

	var before, family, readErr = c.readSnooping(ctx)
	if readErr != nil {
		return res, fmt.Errorf("reading DHCP snooping before applying: %w", readErr)
	}
	res.Notes = append(res.Notes, snoopUnverifiedNote)

	// ARMING FOR A PLAN THAT WANTED A TRUSTED PORT: refused, and nothing written.
	// "Arming" is the narrow thing it says — the global enable this apply would
	// have done, or a VLAN this device does not yet inspect. A switch already
	// snooping those VLANs is not armed by this gesture and is left to run.
	if wantsTrust && (!before.Enabled || snoopWouldAddVLAN(plan.VLANs, before.VLANs)) {
		res.Skipped = append(res.Skipped, snoopArmRefusal)
		c.step(false, "DHCP snooping NOT armed", snoopArmRefusal)
		return res, nil
	}

	// 1) GLOBAL. Already on: nothing is rewritten, and the family the device
	// already uses is the one everything below speaks.
	if before.Enabled {
		if family == "" {
			family = snoopFamilyRelay
		}
		c.step(true, "DHCP snooping already enabled globally",
			"\""+family+"\" already in the running configuration — nothing rewritten")
	} else {
		var accepted string
		var tried []string
		var candidate string
		for _, candidate = range snoopFamilies {
			tried = append(tried, "\""+candidate+"\"")
			var cerr = c.configBlock(ctx, []string{snoopGlobalCmd(candidate)})
			if cerr == nil {
				accepted = candidate
				break
			}
		}
		if accepted == "" {
			// Everything below would be inert: the VLAN statements are refused with
			// it rather than written onto a device that inspects nothing. The ports
			// are already accounted for at the top of this function, on a ground that
			// does not depend on how this went.
			res.Skipped = append(res.Skipped,
				"global DHCP snooping not enabled: no surveyed form was accepted ("+
					strings.Join(tried, ", ")+")")
			c.step(false, "DHCP snooping could not be enabled", strings.Join(tried, ", "))
			return res, fmt.Errorf("DHCP snooping on %s: none of the surveyed global forms "+
				"was accepted (%s) — the vocabulary of this firmware is still unknown",
				c.host, strings.Join(tried, ", "))
		}
		family = accepted
		res.Notes = append(res.Notes,
			"DHCP snooping was OFF on this device and has been ENABLED globally "+
				"(\""+family+"\"): that changes the behaviour of EVERY port, not only the "+
				"ones named in this plan")
		c.step(true, "DHCP snooping enabled globally", "\""+family+"\"")
	}

	// 2) VLANs. Additive only.
	var covered = map[int]bool{}
	var id int
	for _, id = range before.VLANs {
		covered[id] = true
	}
	var added []int
	for _, id = range sortedUniqueVLANs(plan.VLANs) {
		if id < 1 || id > 4094 {
			res.Skipped = append(res.Skipped,
				fmt.Sprintf("vlan %d: outside the 1-4094 range this platform accepts", id))
			continue
		}
		if covered[id] {
			continue
		}
		var verr = c.configBlock(ctx, []string{snoopVlanCmd(family, id)})
		if verr != nil {
			res.Skipped = append(res.Skipped, fmt.Sprintf("vlan %d: %v", id, verr))
			continue
		}
		covered[id] = true
		added = append(added, id)
	}
	if len(added) > 0 {
		res.Notes = append(res.Notes,
			"snooping now inspects VLAN(s) "+compactVlanList(added)+" (\""+family+" vlan N\")")
		c.step(true, "DHCP snooping VLANs added", compactVlanList(added))
	}
	if len(before.VLANs) > 0 {
		res.Notes = append(res.Notes,
			"VLAN(s) "+compactVlanList(before.VLANs)+" were already covered and were left "+
				"alone: snooping this connector did not install is never removed")
	}
	if len(plan.VLANs) == 0 {
		res.Notes = append(res.Notes,
			"the plan names no VLAN: the feature is on, and inspects NOTHING until a VLAN "+
				"is declared")
	}

	// 3) THERE IS NO PER-PORT LEVEL ON THIS PLATFORM. The intents were refused at
	//    the top of this function and nothing per-port is sent from here —
	//    deliberately not even a "no … trust", which is the same absent command
	//    in the other direction. res.PortsWritten is therefore never appended to.

	// Whether this gesture actually SENT anything. It decides whether step 5
	// saves: a plan the device already carried costs no flash write, the same
	// reason the VLAN loop above skips an id already covered.
	var sentSomething = !before.Enabled || len(added) > 0

	// 4) VERIFY BY RE-READING. On a platform where the write forms were proven by
	// a single live probe, this is not a nicety: a command this CLI accepts and
	// ignores would otherwise be reported as an applied configuration.
	var after, _, aerr = c.readSnooping(ctx)
	if aerr != nil {
		// Nothing per-port was sent, so the ports list is empty and the note is
		// about the two levels that went out (cf. driver.ApplyResult.Unconfirmed,
		// which writes its sentence for a call with no ports too).
		res.Unconfirmed("DHCP snooping", nil, firstLine(aerr.Error()))
		return res, nil
	}
	if !after.Enabled {
		res.Skipped = append(res.Skipped,
			"the running configuration carries no global snooping statement after the write: "+
				"\""+family+"\" was accepted and configured nothing")
		c.step(false, "DHCP snooping did not take", "\""+family+"\" accepted, absent on re-read")
		return res, fmt.Errorf("DHCP snooping on %s: the global statement is absent from the "+
			"running configuration after a write the device accepted", c.host)
	}
	var uncovered []int
	for _, id = range sortedUniqueVLANs(plan.VLANs) {
		if id < 1 || id > 4094 {
			// Already refused above, with the reason. Naming it again as "absent
			// from the running configuration after the write" would tell an
			// operator it was tried.
			continue
		}
		if !hasVLAN(after.VLANs, id) {
			uncovered = append(uncovered, id)
		}
	}
	if len(uncovered) > 0 {
		res.Skipped = append(res.Skipped,
			"vlan "+compactVlanList(uncovered)+": absent from the running configuration after "+
				"the write — snooping does not inspect them")
	}

	// 5) PERSIST, and LAST — after the verification above and never before it.
	//
	// Protection a reboot forgets is protection nobody has: inspection that only
	// lives in the running configuration reads on every screen exactly like the
	// real thing, right up until the switch is power-cycled. Saving BEFORE the
	// re-read would be the opposite mistake — baking into flash a configuration
	// nothing had confirmed.
	//
	// A FAILED SAVE DOES NOT FAIL THE APPLY. The write genuinely landed and the
	// re-read just confirmed it; returning an error here would claim otherwise.
	// It becomes a Note naming what the operator loses, plus the failed transcript
	// step [Client.Persist] emits itself — the same shape ApplyHostname next door
	// uses, minus the error, because there the save is the gesture and here it is
	// its epilogue.
	if !sentSomething {
		return res, nil // nothing was sent, so there is nothing to save
	}
	var perr = c.Persist(ctx)
	if perr != nil {
		res.Notes = append(res.Notes,
			"applied but NOT persisted: "+firstLine(perr.Error())+
				" — a reboot would take this DHCP snooping configuration with it, and the "+
				"switch would come back inspecting whatever its startup configuration says")
		return res, nil
	}
	return res, nil
}

func trustWord(trusted bool) string {
	if trusted {
		return "trusted"
	}
	return "untrusted"
}

// sortedUniqueVLANs sorts and de-duplicates a plan's VLAN list, so the same id
// named twice does not produce two commands and two notes.
func sortedUniqueVLANs(ids []int) []int {
	var s = append([]int{}, ids...)
	sort.Ints(s)
	var out []int
	var i int
	for i = range s {
		if i == 0 || s[i] != s[i-1] {
			out = append(out, s[i])
		}
	}
	return out
}

// snoopWouldAddVLAN: does this plan name a VLAN the device does not already
// inspect? It is the second half of "would this apply ARM the feature", and it
// deliberately ignores ids outside 1-4094 — those are refused before any command
// is built, so they would arm nothing.
func snoopWouldAddVLAN(want, have []int) bool {
	var id int
	for _, id = range want {
		if id >= 1 && id <= 4094 && !hasVLAN(have, id) {
			return true
		}
	}
	return false
}

func hasVLAN(ids []int, want int) bool {
	var id int
	for _, id = range ids {
		if id == want {
			return true
		}
	}
	return false
}
