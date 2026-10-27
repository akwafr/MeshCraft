package s3900

// DHCP snooping on the FSOS/OpCode CLI — read from the running-config, written
// with ONE form that has been typed on this hardware and TWO that have not.
//
// # What is CAPTURED here, form by form
//
//   - the command that carries the reading, "show run", and the shape of its
//     interface blocks ("interface ethernet 1/25" followed by indented per-port
//     statements, terminated by "!") — lab OpCode 1.7.0, 2026-08-01/02 serial
//     session, replayed by labRun in parse_test.go. That is what
//     [parseDHCPSnoop] walks;
//   - the status command "show dhcp snooping config" and its DISABLED shape —
//     lab S3900 192.0.2.71 on FSOS 1.7.7, 2026-08-08
//     (testdata/show_dhcp_snooping_config.txt);
//   - THE GLOBAL ENABLE, "dhcp snooping", and the ENABLED shape of that same
//     status output — same switch (s3900lab), FSOS 1.7.7, 2026-08-10
//     (testdata/show_dhcp_snooping_config_enabled.txt);
//   - THE PER-VLAN STATEMENT "dhcp snooping vlan N" AND THE PER-PORT FLAG
//     "dhcp snooping trust" — same switch, same firmware, later the same day,
//     on VLAN 777 (undefined on that switch) and Eth 1/1 (a dark port). See the
//     § below; both were reverted and the status output returned to its disabled
//     shape.
//
// So all three WRITE forms are now typed. What remains unproven is the APPLIER
// around them — [ApplyDHCPSnooping]'s ordering, its gate and its three re-reads
// have still only been driven by the fake CLI in dhcpsnoop_test.go.
//
// THE DEDUCED GLOBAL ENABLE WAS WRONG, AND THE DEVICE SAID SO — 2026-08-10.
//
// The family below was deduced from the Accton/FSOS lineage the 1.7 CLI belongs
// to: "ip dhcp snooping", "ip dhcp snooping vlan N", "ip dhcp snooping
// trust". The first of those has now been typed on an S3900 (s3900lab, FSOS
// 1.7.7) and it is REFUSED:
//
//	ip dhcp snooping          →  % Invalid input detected at '^' marker.
//	ip dhcp snooping enable   →  % Invalid input detected at '^' marker.
//	dhcp snooping             →  ACCEPTED — "Global DHCP Snooping Status: enabled"
//
// THE ACCEPTED SPELLING DROPS THE "ip" PREFIX. That is the same asymmetry this
// file already recorded for the READ — the status command is "show dhcp
// snooping config", not "show ip dhcp snooping" — and the deduction simply
// failed to carry it across to the write. So [ApplyDHCPSnooping] would have been
// refused at its very first command on this firmware.
//
// THE EXTRAPOLATION HELD, AND IT WAS STILL WORTH TYPING — 2026-08-10, later the
// same day. The per-VLAN and per-port forms were predicted to be "dhcp snooping
// vlan N" and "dhcp snooping trust" by the same rule, and the device accepted
// both:
//
//	dhcp snooping vlan 777                       →  ACCEPTED
//	(in interface ethernet 1/1) dhcp snooping trust  →  ACCEPTED
//	(in interface ethernet 1/1) ip dhcp snooping trust
//	                                             →  % Invalid input detected at '^' marker.
//
// The CLI's own completion closes the question rather than leaving it to a lucky
// guess. At config level "dhcp snooping ?" offers information / limit / verify /
// vlan / <cr>, and "dhcp snooping vlan ?" offers "<1-4094>  VLAN ID" — so the
// family's "vlan add N" variant cannot parse here at all. Inside an interface
// block "dhcp snooping ?" offers information / max-number / trust.
//
// A prediction that turns out right is not the same as a proven form, which is
// exactly why it was typed: borrowing a neighbour's spelling is what produced the
// arista "no enable secret" defect, which failed on every run for weeks because
// nobody had typed it. Two of the four spellings this file used to ship as
// fallbacks could never have parsed.
//
// # ONE captured form does not make three, and the code says which is which
//
// Three things carry the distinction, so that neither a reader nor a caller has
// to take this comment's word for it:
//
//   - the NAMES. [dhcpSnoopGlobalCmds] returns ONE command set, the captured one,
//     the way portadmin.go ships a single "shutdown". [dhcpSnoopVlanCandidates]
//     and [dhcpSnoopTrustCandidates] return ORDERED CANDIDATES, which is what this
//     package reserves for forms nobody has typed (apply.go, portCmdCandidates:
//     "Captured forms carry only one alternative; deduced forms carry several");
//   - the CONSTANTS snoopGlobalCaptured / snoopVlanCaptured / snoopTrustCaptured,
//     each dated, which TestDHCPSnoopFormEvidenceMatchesTheBuilders holds the
//     builders to: a captured form ships ONE spelling, a deduced one ships
//     several. Promoting a form is one line here and a failing test until the
//     builder follows;
//   - [ApplyResult.Notes], on every apply: snoopFormsNote names the captured form
//     and the two that are not, rather than describing the whole family as
//     unverified — which is what it said until 2026-08-10, and it is no longer
//     true.
//
// The deduced candidates still carry every spelling they carried before, and the
// no-"ip" ones now go FIRST. That is an ordering, not a claim: each is still
// sent until one is accepted, and the transcript records which. The capture only
// moved which guess is tried first.
//
// On the READ side the "ip" prefix became OPTIONAL for the same reason, and
// that one is not a guess at all — tolerating a second spelling of a line that
// already means "snooping is on" cannot misconfigure anything, while being strict
// would have made the applier's own re-read deny a write that had landed (cf. the
// regexp block).
//
// The consequences of the remaining deductions are carried honestly rather than
// hidden:
//
//   - [Driver.WritesExtra]("dhcp_snooping") stays FALSE. The code ships, the
//     button does not — same staging as PoE on EdgeSwitch and the S3400 writes
//     (cf. driver/dhcpsnoop.go, § on the staging rule);
//   - the two DEDUCED writes go through ORDERED CANDIDATE FORMS (cf. apply.go), so
//     a refused spelling falls back instead of being reported as a success, and the
//     transcript records which form the device accepted — the capture that is
//     missing, made by the tool itself. The captured global enable does NOT: it
//     ships one form, and a spelling the device has been observed REFUSING is not
//     kept as a fallback for the sake of symmetry;
//   - [ApplyResult.Notes] says out loud, on every single apply, which form is
//     captured and which two are not.
//
// # A CAPTURED ENABLE OVER TWO DEDUCED WRITES: the applier now ARMS LAST
//
// Correcting the global form makes this applier LIVE where it used to die on its
// very first command. That is the dangerous half of the fix: the enable lands now,
// and the two writes behind it are still guesses. The state that combination
// invites is a device ARMED WITHOUT AN EXEMPTION — snooping inspecting a VLAN
// with no trusted port — which drops every DHCP OFFER/ACK crossing the switch,
// the uplink toward the real server included.
//
// So the gesture is no longer global → VLAN → trust. It is global → TRUST →
// re-read → VLAN, and it REFUSES TO ARM A VLAN unless that re-read shows at least
// one trusted port on the device. A deduced trust form that gets refused — or
// accepted and ignored — now costs the VLAN its arming instead of costing the
// site its DHCP. Each refusal lands in [ApplyResult.Skipped] naming the reason,
// which is what driver/dhcpsnoop.go calls an ORDINARY REFUSAL.
//
// That gate is NOT a staging flag with a date on it. It stays right after both
// forms are captured, because it also catches the case a proven command set still
// produces: every per-port trust write refused on its own merits, and the VLAN
// armed anyway.
//
// It is deliberately NOT a blanket refusal of the whole gesture either. The way
// the global form was captured is the way the other two will be — the tool sends
// its ordered candidates at a lab switch and the transcript records which one the
// device took. An applier that refuses to send anything can never earn that, and
// WritesExtra stays false meanwhile, so no operator reaches this code by clicking.
//
// # The 1.7.0 → newer firmware question applies to all of it
//
// The whole command set of this connector was surveyed on OpCode 1.7.0, and the
// lab switch has since moved to 1.7.7 — where one captured form ALREADY CHANGED
// ("media ethernet" on VLAN creation went from required to rejected; cf.
// vlanCreateCmds). A firmware upgrade is planned on the rest of the fleet. So even
// the reading here is version-bound: the interface-block shape must be
// re-verified afterwards, and the write forms must be captured on whatever
// firmware they are first accepted by, not assumed to carry backwards.
//
// # Why the reading comes from "show run" and not from a status command
//
// The family documents a "show ip dhcp snooping" status command. THAT COMMAND
// DOES NOT EXIST ON THIS FIRMWARE, established on the lab S3900 (192.0.2.71,
// FSOS 1.7.7) on 2026-08-08: it answers "% Invalid input", as do
// "show ip dhcp-relay snooping" and "show ip dhcp snooping binding". The
// bare "show dhcp snooping" answers "% Incomplete command" — the prefix is
// real — and the status lives at "show dhcp snooping config"
// (testdata/show_dhcp_snooping_config.txt).
//
// This is exactly the gap the caution was about, and it landed on the side that
// was feared: a parser written from the family's documented spelling would have
// been a parser for a command this switch refuses. Note what the refusal would
// have looked like to a caller — an error, not an empty read — so it would have
// surfaced; the DANGEROUS version of this mistake is the one that returns
// nothing and reads as "snooping is off".
//
// The read is NOT moved onto that command, deliberately. Its output is a third
// shape to parse (a "Global DHCP Snooping Status" line, a VLAN list, and a
// per-interface table whose ports are spelled "Eth 1/1" rather than this
// connector's facade names). The running-config, by contrast, is the reading this
// connector already trusts for port modes and management addressing, for the
// reason state.go gives: the interface blocks carry the intent exactly.
//
// What that command IS used for is one decisive question — did the captured
// global enable land? — and [parseSnoopConfigStatus] reads THE STATUS LINE AND
// NOTHING ELSE from it. Both shapes are captured, and over the lines they share
// they differ by that first line alone, which is precisely what makes the line
// safe to read and the rest of the output not:
//
//   - the VLAN list is PRESENT AND EMPTY in both captures — "DHCP Snooping is
//     configured on the following VLANs:" followed by a BLANK LINE, including in
//     the capture taken with the feature ON. A parser that read that as "the
//     section is missing" would be wrong, and one that learned the POPULATED shape
//     from these two files would be inventing it: no capture has ever carried a
//     VLAN there. TestSnoopConfigVLANSectionIsPresentAndEmpty pins both halves;
//   - no capture FILE shows a "Yes" in the Trusted column, so the per-port half
//     of that table is unobserved in exactly the same way. (The 2026-08-10
//     session did SEE "Eth 1/1   Yes" on screen after the trust write — cf.
//     `snoopTrustCaptured` — but the transcript was not kept as a fixture, and a
//     remembered line is not a parser's evidence.)
//
// THE OUTPUT ENDS IN A CIRCUIT-ID TABLE, and it is captured in the DISABLED
// shape, which serves it in full (2026-08-11, re-confirmed read-only):
//
//	                              Circuit-ID       Circuit-ID    Circuit-ID
//	Interface Trusted Max-Number  mode             Value         TR101 VLAN Field
//	--------- ------- ----------  ---------------  ------------  ----------------
//	Eth 1/1   No      16          VLAN-Unit-Port   ---           enabled
//
// 28 rows, one per faceplate port, identical but for the interface name. The
// ENABLED capture stops at the rate-limit line above it — the session was cut
// there — which is the only reason the two fixtures differ in length. Nothing
// parses this table; it is pinned so that a future reader knows the command's
// output does not end where the enabled fixture does.

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"meshcraft/internal/driver"
)

var (
	_ driver.DHCPSnoopReader  = (*Client)(nil)
	_ driver.DHCPSnoopApplier = (*Client)(nil)
)

// Which of the three write forms has been TYPED ON AN S3900, and when. The
// applier and the tests branch on these rather than on this file's prose, so a
// form that gets captured is promoted in one place and a form that turns out
// wrong is demoted in one place.
const (
	// "dhcp snooping" — s3900lab, FSOS 1.7.7, 2026-08-10. Accepted, and
	// "show dhcp snooping config" then read "Global DHCP Snooping Status:
	// enabled"; reverted with "no dhcp snooping".
	snoopGlobalCaptured = true
	// "dhcp snooping vlan N" — s3900lab, FSOS 1.7.7, 2026-08-10. Accepted on
	// VLAN 777, an id this switch does not define; "show dhcp snooping config"
	// then printed "777" under "… configured on the following VLANs:" and
	// "show run" carried "dhcp snooping vlan 777" at top level. Reverted with
	// "no dhcp snooping vlan 777". The CLI's own grammar says there is no other
	// spelling: "dhcp snooping vlan ?" offers "<1-4094>  VLAN ID" and nothing
	// else, so the family's "vlan add N" variant does not exist here.
	snoopVlanCaptured = true
	// "dhcp snooping trust" — s3900lab, FSOS 1.7.7, 2026-08-10. Accepted inside
	// "interface ethernet 1/1" (a dark port); "show dhcp snooping config" then
	// printed "Eth 1/1   Yes" in the Trusted column and "show run" carried
	// "dhcp snooping trust" indented in that interface's block. Reverted with
	// "no dhcp snooping trust".
	//
	// THE "ip" SPELLING IS REFUSED HERE TOO: "ip dhcp snooping trust" in the
	// same interface answers "% Invalid input detected at '^' marker.", and
	// "dhcp snooping ?" inside an interface block offers exactly
	// "information / max-number / trust". So the second candidate this builder
	// used to carry is not a fallback, it is a guaranteed caret line.
	snoopTrustCaptured = true
)

// snoopConfigCmd: the status command, captured (cf. the header). Used for ONE
// question — did the global enable land — and never for the ordinary read.
const snoopConfigCmd = "show dhcp snooping config"

// The running-config statements, as regexps for the read and as builders for the
// write. Kept side by side deliberately: a form that turns out to be wrong must
// be fixed in one place, not in a parser and a builder that drifted apart.
//
// THE "ip" PREFIX IS OPTIONAL ON THE READ, since 2026-08-10. The write form
// this device accepts drops it, and nothing in any capture says which spelling
// the running-config prints back — a switch that takes a short form and stores
// the canonical long one is ordinary. Tolerating both here cannot half-configure
// anything (a top-level "dhcp snooping" means the feature is on whichever way
// it is spelled), while being strict would make the post-write re-read of a
// captured, accepted, landed enable report "not enabled" and abort the gesture.
// "no …" still never matches: the anchor is the whole trimmed line.
var (
	// "ip dhcp snooping" / "dhcp snooping" alone, at top level. The anchor
	// matters: the same words prefixed by "no" must NOT read as enabled, and
	// the same words followed by "vlan" or "information" are different
	// statements.
	reSnoopGlobal = regexp.MustCompile(`(?i)^(?:ip\s+)?dhcp\s+snooping$`)
	// "ip dhcp snooping vlan 99", "… vlan add 99", "… vlan 10,20-22".
	// The list forms are tolerated on READ even though the write emits one VLAN
	// per command: a device configured by hand is not obliged to have been
	// configured the way meshcraft writes.
	reSnoopVlan = regexp.MustCompile(`(?i)^(?:ip\s+)?dhcp\s+snooping\s+vlan\s+(?:add\s+)?([\d,\-]+)`)
	// "ip dhcp snooping trust", inside an interface block.
	reSnoopTrust = regexp.MustCompile(`(?i)^(?:ip\s+)?dhcp\s+snooping\s+trust$`)
	// Option 82 insertion rides on snooping and changes what downstream relays
	// see. Not modelled — reported in Notes (cf. driver/dhcpsnoop.go, § What is
	// NOT here).
	reSnoopOption = regexp.MustCompile(`(?i)^(?:ip\s+)?dhcp\s+snooping\s+information\s+option`)
	// The first line of "show dhcp snooping config", and the ONLY part of that
	// output anything here reads (cf. the header).
	reSnoopConfigStatus = regexp.MustCompile(`(?i)^Global\s+DHCP\s+Snooping\s+Status:\s*(\S+)`)
)

// parseSnoopConfigStatus reads the global status line of "show dhcp snooping
// config", and returns whether it says enabled and whether it was found at all.
//
// The second return is the whole point. This command does not exist on every
// firmware in this range — it was found by exhausting candidates on 1.7.7
// (live_capture_test.go) and was never tried on the 1.7.0 the rest of the command
// set was surveyed on. "% Invalid input" therefore has to come back as "no
// answer", not as "disabled": a refusal read as a negative is how a connector
// concludes a feature is off on a device that never told it anything.
//
// Nothing else in that output is parsed — the VLAN list is present-and-empty in
// both captures and its populated shape is unobserved, and so is a "Yes" in the
// Trusted column.
func parseSnoopConfigStatus(out string) (enabled bool, known bool) {
	var line string
	for _, line = range strings.Split(strings.ReplaceAll(out, "\r", ""), "\n") {
		var m = reSnoopConfigStatus.FindStringSubmatch(strings.TrimSpace(line))
		if m != nil {
			return strings.EqualFold(m[1], "enabled"), true
		}
	}
	return false, false
}

// ReadDHCPSnooping reports the three levels — global, per-VLAN, per-port trust —
// as the running-config declares them.
func (c *Client) ReadDHCPSnooping(ctx context.Context) (driver.DHCPSnoopState, error) {
	if c.cli == nil {
		return driver.DHCPSnoopState{}, fmt.Errorf("no open session on this access")
	}
	var runOut, err = c.cli.run(ctx, "show run", showRunTimeout)
	if err != nil && strings.TrimSpace(runOut) == "" {
		return driver.DHCPSnoopState{}, fmt.Errorf("show run on %s: %v", c.host, err)
	}
	var st = parseDHCPSnoop(runOut)
	c.step(true, "DHCP snooping read",
		fmt.Sprintf("global %v, %d VLAN(s), %d port(s) observed",
			st.Enabled, len(st.VLANs), len(st.Ports)))
	return st, nil
}

// parseDHCPSnoop walks a running-config for the three levels. Pure, so a
// constructed configuration is replayable in tests.
//
// A port appears in Ports as soon as its INTERFACE BLOCK was seen, and ONLY
// then. What the capture actually shows (labRun, replayed in dhcpsnoop_test.go)
// is a block for THREE of the 28 ethernet interfaces — the ones carrying a
// statement, plus "interface ethernet 1/1" with an empty body: this firmware
// does not print a block per interface, so most ports are simply absent from a
// reading. That absence is NOT "untrusted", it is "not read", and
// [driver.DHCPSnoopState.Ports] exists to keep the two apart.
//
// A block that IS printed and carries no "ip dhcp snooping trust" line reads
// as UNTRUSTED: on a running-config, a non-default setting is printed, and trust
// is the non-default. Untrusted is also the platform's default and the protected
// state, so the inference errs on the safe side.
func parseDHCPSnoop(out string) driver.DHCPSnoopState {
	var st driver.DHCPSnoopState
	var trusted = map[string]bool{}
	var order []string
	var vlans = map[int]bool{}
	var curIface = ""
	var sawOption = false

	var line string
	for _, line = range strings.Split(strings.ReplaceAll(out, "\r", ""), "\n") {
		var trimmed = strings.TrimSpace(line)
		if trimmed == "!" {
			curIface = ""
			continue
		}
		var m = reRunIface.FindStringSubmatch(line)
		if m != nil {
			curIface = ""
			var what = strings.ToLower(strings.Join(strings.Fields(m[1]), " "))
			if strings.HasPrefix(what, "ethernet") {
				var std, ok = (&Driver{}).StandardPortName(what)
				if ok {
					curIface = std
					var _, seen = trusted[std]
					if !seen {
						trusted[std] = false
						order = append(order, std)
					}
				}
			}
			continue
		}
		if curIface != "" {
			if reSnoopTrust.MatchString(trimmed) {
				trusted[curIface] = true
			}
			continue
		}
		if reSnoopOption.MatchString(trimmed) {
			sawOption = true
			continue
		}
		if reSnoopGlobal.MatchString(trimmed) {
			st.Enabled = true
			continue
		}
		if m = reSnoopVlan.FindStringSubmatch(trimmed); m != nil {
			var ids, err = expandVlanList(m[1])
			if err == nil {
				var v int
				for _, v = range ids {
					vlans[v] = true
				}
			}
			continue
		}
	}

	var v int
	for v = range vlans {
		st.VLANs = append(st.VLANs, v)
	}
	sort.Ints(st.VLANs)

	var name string
	for _, name = range order {
		st.Ports = append(st.Ports, driver.DHCPSnoopPortState{Port: name, Trusted: trusted[name]})
	}

	st.Notes = append(st.Notes,
		"read from the running-config: an interface block with no \"dhcp snooping trust\" "+
			"line is UNTRUSTED, which is the protected state and this platform's default")
	if !st.Enabled {
		// The one thing this reading genuinely cannot tell apart, said plainly
		// rather than left for a screen to guess at. NARROWED on 2026-08-10: the
		// feature's existence is no longer in doubt on this firmware — "dhcp
		// snooping" was accepted on the lab S3900 (FSOS 1.7.7) — but what "show
		// run" prints once it is on has still never been captured.
		st.Notes = append(st.Notes,
			"global DHCP snooping is OFF, or this running-config does not print the statement — "+
				"FSOS 1.7.7 does carry the feature (\"dhcp snooping\" was accepted on the lab "+
				"switch on 2026-08-10), but no capture yet shows a running-config with it ON, so "+
				"a firmware that stores the setting without printing it would look identical. "+
				"\"show dhcp snooping config\" answers that question directly")
	}
	if st.Enabled && len(st.VLANs) == 0 {
		st.Notes = append(st.Notes,
			"snooping is on globally but covers NO VLAN: it inspects nothing until a VLAN is enabled")
	}
	if sawOption {
		st.Notes = append(st.Notes,
			"\"dhcp snooping information option\" is set (option 82 insertion) — observed, "+
				"not modelled, and this connector neither sets nor removes it")
	}
	return st
}

// snoopFormsNote is repeated on every apply result, on purpose. An operator
// reading a successful-looking apply of a half-verified command set must be told
// so by the result itself, not only by this file's comments.
//
// It names the ONE captured form and the two that are not, rather than describing
// the family as uniformly unverified — which is what it said until 2026-08-10,
// and which is no longer true. A note that undersells the evidence is corrected
// the same day as one that oversells it: an operator who stops believing the
// caveats reads none of them.
const snoopFormsNote = "all three DHCP snooping write forms are CAPTURED on this range (lab " +
	"S3900, FSOS 1.7.7, 2026-08-10): \"dhcp snooping\", \"dhcp snooping vlan N\" and, inside " +
	"an interface block, \"dhcp snooping trust\". The \"ip\" prefix the family documents is " +
	"refused on every one of them. What is NOT captured is this connector's APPLIER driving " +
	"them — the forms were typed at a CLI and the plan/verify path around them has only ever " +
	"been exercised by the fake CLI in dhcpsnoop_test.go. Re-verify after the planned firmware " +
	"upgrade — the connector's command set was surveyed on OpCode " + VerifiedOpCode + ".0 and one " +
	"form has already changed since (VLAN creation, 1.7.7)"

// ApplyDHCPSnooping sets per-port TRUST, and turns the feature on above it.
//
// The global and per-VLAN enable are part of this gesture and not the caller's
// business (cf. driver/dhcpsnoop.go): a trust flag written on a device whose
// global snooping is off is an exact configuration with zero protection. Both are
// reported in Notes, because they change the behaviour of EVERY port on the
// device and not only the ports this plan names.
//
// NOTHING IS EVER TURNED OFF here — not the global feature, not a VLAN. A VLAN
// found already enabled is protection someone else installed, and removing it
// because this plan did not mention it would strip it on the strength of a click
// nobody made.
//
// NOTHING IS REPORTED AS WRITTEN ON A RETURN CODE either: the global enable is
// re-read before any trust flag is sent, the trust flags are re-read before any
// VLAN is armed, and the VLANs are re-read after. Only what the running-config
// carries lands in [ApplyResult.PortsWritten]; the rest goes to
// [ApplyResult.Skipped] with the reason. On two forms no S3900 has ever accepted,
// an echo proves nothing — this file's own opening paragraph says a snooping VLAN
// line can be accepted and do nothing, and that is exactly what the re-read is for.
//
// THE ORDER IS global → trust → VLAN, and the last step is GATED (cf. the header,
// § the applier now arms last): a VLAN is armed only over a trusted port the
// re-read actually found. Arming inspection on a switch where nothing is trusted
// drops every DHCP OFFER/ACK it forwards, and that is not a state to reach by
// way of two deduced spellings — nor by way of two captured ones that happened to
// be refused port by port.
func (c *Client) ApplyDHCPSnooping(ctx context.Context, plan driver.DHCPSnoopPlan) (ApplyResult, error) {
	var out = ApplyResult{VLANsCreated: []int{}, PortsWritten: []string{}}
	if c.cli == nil {
		return out, fmt.Errorf("no open session on this access")
	}
	var werr = c.requireWritableDialect()
	if werr != nil {
		return out, werr
	}
	out.Notes = append(out.Notes, snoopFormsNote)

	// A write never re-reads a cache: the device comes first. The same read is
	// the safety copy handed back to the caller.
	var runOut, err = c.cli.run(ctx, "show run", showRunTimeout)
	if err != nil && strings.TrimSpace(runOut) == "" {
		return out, fmt.Errorf("pre-write read failed: %v — nothing was sent to the device", err)
	}
	out.Backup = runOut
	var before = parseDHCPSnoop(runOut)
	var rc = parseRunningConfig(runOut, c.host)

	// 1. Global enable, first: on this CLI family the per-VLAN statement is a
	// sub-clause of a feature that must already exist, and a VLAN line sent to a
	// device with snooping off is the kind of command that is accepted and does
	// nothing.
	if !before.Enabled {
		// ONE form, the captured one. Until 2026-08-10 this sent "ip dhcp
		// snooping" first, which the device is now known to REFUSE — keeping it
		// as a candidate would spend a caret error on every single apply to try a
		// spelling this range has been observed rejecting.
		var applied, detail = c.runWithFallbacks(ctx, []cmdGroup{{
			label:        "DHCP snooping enabled globally",
			alternatives: [][]string{dhcpSnoopGlobalCmds()},
		}})
		if !applied {
			// Refusing to go further is the point: writing trust flags under a
			// feature that is off would leave the device looking configured and
			// being unprotected, which is the exact failure this contract exists
			// to prevent.
			return out, fmt.Errorf("global DHCP snooping refused (%s) — no trust flag was "+
				"written, because a trusted port on a device that inspects nothing is a "+
				"configuration with no protection behind it. \"dhcp snooping\" is the form "+
				"captured on FSOS 1.7.7 (2026-08-10); a refusal here means this firmware "+
				"spells it differently again, and it must be surveyed, not guessed", detail)
		}
		// Confirmed by a RE-READ before anything else is sent. An accepted
		// spelling that leaves the configuration untouched is what an unproven
		// form looks like from here, and the failure it would produce is the one
		// this contract exists to prevent: trust flags in place, feature asleep.
		var chkOut, chkErr = c.cli.run(ctx, "show run", showRunTimeout)
		if chkErr != nil && strings.TrimSpace(chkOut) == "" {
			return out, fmt.Errorf("the global DHCP snooping enable could not be read back "+
				"(%v) — no trust flag was written: this connector does not build on a write "+
				"it was unable to verify", chkErr)
		}
		if !parseDHCPSnoop(chkOut).Enabled {
			// SECOND OPINION, on the captured status command. The write form is
			// captured; what a running-config PRINTS once the feature is on never
			// was (cf. parseDHCPSnoop's note), so an empty "show run" here is as
			// likely to mean "this firmware does not print the statement" as
			// "the command did nothing". Asking the one command whose enabled
			// shape IS captured settles it instead of aborting on an assumption.
			var stOut, stErr = c.cli.run(ctx, snoopConfigCmd, cmdTimeout)
			var on, known = parseSnoopConfigStatus(stOut)
			if stErr != nil && strings.TrimSpace(stOut) == "" {
				known = false
			}
			if !known || !on {
				c.step(false, "global DHCP snooping absent after write",
					"the CLI accepted the command and neither \"show run\" nor \""+
						snoopConfigCmd+"\" reports the feature on")
				return out, fmt.Errorf("the CLI accepted \"%s\" and the device does not report "+
					"the feature on afterwards — no trust flag was written, because a trusted "+
					"port on a device that inspects nothing is a configuration with no "+
					"protection behind it. The form is the one captured on FSOS 1.7.7: survey "+
					"this firmware rather than trying spellings against a live switch",
					strings.Join(dhcpSnoopGlobalCmds(), " ; "))
			}
			c.step(true, "global DHCP snooping confirmed by \""+snoopConfigCmd+"\"",
				"the running-config does not print the statement on this firmware")
			out.Notes = append(out.Notes,
				"this firmware does NOT print the global snooping statement in its "+
					"running-config: the enable was confirmed by \""+snoopConfigCmd+"\" instead, "+
					"and an ordinary read of this device will keep reporting snooping as off")
		}
		out.Notes = append(out.Notes,
			"global DHCP snooping was OFF on this device and is now ON: this affects EVERY port, "+
				"not only the ports named in this plan. Ports left untrusted now drop DHCP server "+
				"replies on the VLANs below")
	}

	// 2. Per-port trust — BEFORE any VLAN is armed (cf. the header). The
	// exemption goes in first and gets verified; the drop is switched on over it
	// or not at all.
	var intents = sortedIntents(plan.Ports)
	// written: the intents the CLI accepted. They become PortsWritten only once
	// the re-read of step 3 finds them in the configuration.
	var written []driver.DHCPSnoopPortIntent
	var in driver.DHCPSnoopPortIntent
	for _, in = range intents {
		if in.Port == "" {
			// A blank name is a caller mistake, and a caller mistake that
			// disappears without a trace is an apply reported as complete for a
			// plan entry nothing was done about.
			out.Skipped = append(out.Skipped,
				"an intent with an EMPTY port name was not applied: nothing was sent to the "+
					"device, and nothing was guessed about which port it meant")
			continue
		}
		var cli = cliPortName(in.Port)
		if cli == "" {
			out.Skipped = append(out.Skipped, fmt.Sprintf(
				"%s not modified: this name is outside the standard-model mapping this connector "+
					"translates (unit 1 only) — nothing was sent to the device", in.Port))
			continue
		}
		var now, known = trustOf(before, in.Port)
		if known && now == in.Trusted {
			continue // already there: sending nothing beats a round trip that changes nothing
		}
		var applied, detail = c.runWithFallbacks(ctx, []cmdGroup{{
			label:        fmt.Sprintf("%s: DHCP snooping trust=%v", in.Port, in.Trusted),
			alternatives: dhcpSnoopTrustCandidates(cli, in.Trusted),
		}})
		if !applied {
			out.Skipped = append(out.Skipped, fmt.Sprintf(
				"%s trust not written: the CLI refused every known form (%s) — the per-port "+
					"form is DEDUCED and the command set was surveyed on OpCode %s.x, so this "+
					"is a spelling to survey, not a device fault",
				in.Port, detail, VerifiedOpCode))
			continue
		}
		written = append(written, in)
	}

	// 3. VERIFICATION BY RE-READ of the trust flags, before anything is armed.
	// Nothing above is taken as applied on the strength of a return code — the
	// rule the SNMP write already follows (snmp.go: "Nothing is taken as written
	// on the strength of a return code: re-read"), and the rule that matters most
	// here, where the per-port form has never been proven on this hardware.
	var after = before
	var trustVerified = false
	if len(written) > 0 {
		var reOut, reErr = c.cli.run(ctx, "show run", showRunTimeout)
		if reErr != nil && strings.TrimSpace(reOut) == "" {
			out.Unconfirmed("DHCP snooping trust", driver.SnoopIntentPorts(written),
				firstLine(reErr.Error()))
			c.step(false, "post-write re-read failed", firstLine(reErr.Error()))
		} else {
			after = parseDHCPSnoop(reOut)
			trustVerified = true
			c.step(true, "DHCP snooping trust re-read after write",
				fmt.Sprintf("global %v, %d VLAN(s), %d port(s) observed",
					after.Enabled, len(after.VLANs), len(after.Ports)))
		}
	}
	if trustVerified {
		for _, in = range written {
			var now, known = trustOf(after, in.Port)
			switch {
			case known && now == in.Trusted:
				out.PortsWritten = append(out.PortsWritten, in.Port)
			case !known:
				out.Skipped = append(out.Skipped, fmt.Sprintf(
					"%s trust NOT CONFIRMED: the CLI accepted the command and the re-read "+
						"prints no interface block for this port, so nothing proves the flag "+
						"landed", in.Port))
			default:
				out.Skipped = append(out.Skipped, fmt.Sprintf(
					"%s trust not written: the CLI accepted the command and the running-config "+
						"still reads trusted=%v afterwards — the form is DEDUCED, survey it on "+
						"this firmware", in.Port, now))
			}
		}
	}

	// 4. VLANs — armed LAST, added, never removed, and NEVER over a switch with
	// nothing trusted on it. `after` is the state the re-read above found, or the
	// pre-write state when no trust flag needed writing; a trust write that could
	// not be verified counts as not trusted, which is the direction that costs a
	// VLAN its arming rather than a site its DHCP.
	var wantVlans = sortedUniqueInts(plan.VLANs)
	var exempt = anyTrustedPort(after)
	var haveVlan = map[int]bool{}
	var v int
	for _, v = range before.VLANs {
		haveVlan[v] = true
	}
	var addedVlans []int
	var alreadyVlans []int
	for _, v = range wantVlans {
		if v < 1 || v > 4094 {
			out.Skipped = append(out.Skipped, fmt.Sprintf(
				"VLAN %d not enabled for snooping: outside the 1..4094 range this platform accepts", v))
			continue
		}
		if haveVlan[v] {
			alreadyVlans = append(alreadyVlans, v)
			continue
		}
		if !exempt {
			// THE GATE. Not a report after the fact: the command is not sent.
			out.Skipped = append(out.Skipped, fmt.Sprintf(
				"VLAN %d NOT enabled for snooping: NO port on this device is trusted, so arming "+
					"inspection on it would drop every DHCP OFFER/ACK crossing this switch — "+
					"including the uplink toward the real server. Trust the uplink port and "+
					"apply again; nothing was sent for this VLAN", v))
			continue
		}
		var applied, detail = c.runWithFallbacks(ctx, []cmdGroup{{
			label:        fmt.Sprintf("DHCP snooping enabled on VLAN %d", v),
			alternatives: dhcpSnoopVlanCandidates(v),
		}})
		if !applied {
			out.Skipped = append(out.Skipped, fmt.Sprintf(
				"VLAN %d not enabled for snooping: the CLI refused every known form (%s)", v, detail))
			continue
		}
		addedVlans = append(addedVlans, v)
		if !vlanDefined(rc.VLANs, v) {
			out.Notes = append(out.Notes, fmt.Sprintf(
				"VLAN %d is not defined on this device: snooping is enabled on it but has no "+
					"traffic to inspect until the VLAN exists", v))
		}
	}
	// The note that says which VLANs are now covered is emitted after the
	// verifying re-read below: it is a claim about the device, not about what the
	// CLI echoed back.
	if len(alreadyVlans) > 0 {
		out.Notes = append(out.Notes,
			"VLAN(s) "+joinInts(alreadyVlans)+" already had snooping enabled: left untouched")
	}
	if len(before.VLANs) > 0 {
		var extra = subtractInts(before.VLANs, wantVlans)
		if len(extra) > 0 {
			out.Notes = append(out.Notes,
				"VLAN(s) "+joinInts(extra)+" carry snooping on this device without being in the "+
					"plan: NOT removed — protection this connector did not install is not "+
					"protection it takes away")
		}
	}
	if len(wantVlans) == 0 {
		out.Notes = append(out.Notes,
			"the plan names no VLAN: global snooping is on but covers nothing yet, so a port "+
				"reading \"trusted\" is trusted for traffic nothing inspects")
	}

	// 5. VERIFICATION BY RE-READ of the arming, on the same terms.
	var confirmedVlans []int
	if len(addedVlans) > 0 {
		var reOut, reErr = c.cli.run(ctx, "show run", showRunTimeout)
		if reErr != nil && strings.TrimSpace(reOut) == "" {
			// NOTHING is claimed here. Until 2026-08-09 this branch filled
			// PortsWritten from the accepted commands and announced "DHCP snooping
			// enabled on VLAN(s) …" on the same evidence, on the one platform in
			// the fleet where not a single command form had ever been proven.
			c.step(false, "post-arming re-read failed", firstLine(reErr.Error()))
			out.Notes = append(out.Notes, "VLAN(s) "+joinInts(addedVlans)+" were sent \""+
				"dhcp snooping vlan\" and NOT confirmed: the re-read failed, so nothing "+
				"says the feature inspects them")
		} else {
			after = parseDHCPSnoop(reOut)
			c.step(true, "DHCP snooping re-read after arming",
				fmt.Sprintf("global %v, %d VLAN(s), %d port(s) observed",
					after.Enabled, len(after.VLANs), len(after.Ports)))
			for _, v = range addedVlans {
				if containsInt(after.VLANs, v) {
					confirmedVlans = append(confirmedVlans, v)
					continue
				}
				out.Skipped = append(out.Skipped, fmt.Sprintf(
					"VLAN %d not enabled for snooping: the CLI accepted the command and the "+
						"running-config does not carry it afterwards — the form is DEDUCED, and "+
						"this is what an unproven spelling looks like from here", v))
			}
		}
	}
	if len(confirmedVlans) > 0 {
		out.Notes = append(out.Notes, "DHCP snooping enabled on VLAN(s) "+joinInts(confirmedVlans))
	}

	// The one thing an operator can get catastrophically wrong with this feature,
	// and the reason it is a Note and not a refusal: meshcraft did exactly what
	// was asked, and what was asked may still black-hole every DHCP lease on the
	// switch.
	//
	// Read on the state the device is in AFTER the gesture, never on the intent —
	// on the VLANs the DEVICE says are armed, not on the ones the plan named. The
	// gate above means this plan cannot itself arm a VLAN over an untrusted
	// switch; what it cannot undo is an untrust that took away the last trusted
	// port there was, or a VLAN somebody else armed before meshcraft arrived.
	if len(after.VLANs) > 0 && !anyTrustedPort(after) {
		out.Notes = append(out.Notes,
			"NO port on this device is trusted: with snooping active on VLAN(s) "+
				joinInts(after.VLANs)+", every DHCP OFFER/ACK arriving on this switch — including "+
				"the uplink toward the real server — will be dropped. Trust the uplink port")
	}

	// Persistence: this is CONFIGURATION, and the platform distinguishes running
	// from startup (Commit = apply_then_save). A trust flag a reboot undoes would
	// be worse than one never written, because it was observed as in place.
	var perr = c.save(ctx)
	if perr != nil {
		out.Notes = append(out.Notes,
			"applied but NOT persisted: "+firstLine(perr.Error())+
				" — a reboot would undo this DHCP snooping configuration")
		c.step(false, "startup-config save failed", firstLine(perr.Error()))
		return out, nil
	}
	c.step(true, "configuration persisted", "copy running-config startup-config → "+StartupConfigFile)
	return out, nil
}

// --- command builders ---
//
// The NAME says the evidence: ...Cmds returns the single CAPTURED form, the way
// portadmin.go ships one "shutdown"; ...Candidates returns ordered guesses, the
// way apply.go builds its deduced writes. Anything promoted from the second shape
// to the first must arrive with a date and a transcript.

// dhcpSnoopGlobalCmds: the switch-wide enable — CAPTURED, s3900lab, FSOS 1.7.7,
// 2026-08-10 (snoopGlobalCaptured).
//
// ONE form, no fallback. Until 2026-08-10 this returned two candidates with
// "ip dhcp snooping" first, on the strength of the Accton/FSOS family
// documentation; the device answers "% Invalid input detected at '^' marker."
// to that spelling and to "ip dhcp snooping enable", and takes this one. A
// spelling that has been observed being refused is not a fallback, it is a
// guaranteed caret line in every transcript, so it is gone rather than demoted.
func dhcpSnoopGlobalCmds() []string {
	return []string{"dhcp snooping"}
}

// dhcpSnoopVlanCandidates: snooping on ONE VLAN, one command per VLAN. CAPTURED
// since 2026-08-10 (snoopVlanCaptured) — ONE spelling, no fallback.
//
// It stays a ...Candidates function returning a slice of one rather than becoming
// a ...Cmds: the applier drives it through runWithFallbacks, and a captured form
// is a candidate list with nothing to fall back TO. The evidence lives in
// snoopVlanCaptured and TestDHCPSnoopFormEvidenceMatchesTheBuilders holds the two
// together.
//
// The three spellings that used to sit behind this one are gone rather than
// demoted: "dhcp snooping vlan ?" on the device offers "<1-4094>  VLAN ID"
// alone, so "vlan add N" cannot parse, and the "ip" prefix is refused on
// every statement in this family.
//
// Per VLAN and not per list, so a refusal names the VLAN it refused instead of
// failing a whole batch — the plan's VLANs come from a site declaration, and one
// unknown VLAN in it must not cost the protection of the others. The CLI would
// take a list ("<1-4094>" is documented as a range too), and this deliberately
// does not use it.
func dhcpSnoopVlanCandidates(vlan int) [][]string {
	return [][]string{{"dhcp snooping vlan " + strconv.Itoa(vlan)}}
}

// dhcpSnoopTrustCandidates: the per-port trust flag. CAPTURED since 2026-08-10
// (snoopTrustCaptured) — ONE spelling, in both directions.
//
// The "ip"-prefixed alternative that used to follow it was typed on the same
// port in the same session and answered "% Invalid input detected at '^'
// marker.". A spelling observed being refused is not kept as a fallback (the
// rule dhcpSnoopGlobalCmds already states).
//
// "exit" leaves the interface sub-mode even though runConfigBlock always sends
// "end": a block that leaves the session in a sub-mode it did not enter is how
// a later gesture ends up sending a global command to an interface.
func dhcpSnoopTrustCandidates(cliPort string, trusted bool) [][]string {
	var enter = "interface " + cliPort
	if trusted {
		return [][]string{{enter, "dhcp snooping trust", "exit"}}
	}
	return [][]string{{enter, "no dhcp snooping trust", "exit"}}
}

// --- helpers ---

// trustOf: the trust flag observed for a port, and whether it was observed at
// all. The second return is what keeps "not read" from collapsing into
// "untrusted" — a port never seen must still be written, not assumed converged.
func trustOf(st driver.DHCPSnoopState, port string) (bool, bool) {
	var p driver.DHCPSnoopPortState
	for _, p = range st.Ports {
		if p.Port == port {
			return p.Trusted, true
		}
	}
	return false, false
}

func anyTrustedPort(st driver.DHCPSnoopState) bool {
	var p driver.DHCPSnoopPortState
	for _, p = range st.Ports {
		if p.Trusted {
			return true
		}
	}
	return false
}

func vlanDefined(vlans []VLAN, id int) bool {
	var v VLAN
	for _, v = range vlans {
		if v.ID == id {
			return true
		}
	}
	return false
}

// containsInt: membership, for the post-write verification of a VLAN list.
func containsInt(list []int, v int) bool {
	var x int
	for _, x = range list {
		if x == v {
			return true
		}
	}
	return false
}

func sortedUniqueInts(in []int) []int {
	var seen = map[int]bool{}
	var out []int
	var v int
	for _, v = range in {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.Ints(out)
	return out
}

// sortedIntents orders the plan's ports by name and drops duplicates, keeping
// the LAST intent for a repeated port: a plan that says a port twice has one
// answer, and a stable order makes the transcript comparable between runs.
//
// An intent whose name is BLANK is kept, deliberately: the applier reports it in
// [ApplyResult.Skipped]. Dropping it here would turn a plan entry nobody acted
// on into an apply that looks complete.
func sortedIntents(in []driver.DHCPSnoopPortIntent) []driver.DHCPSnoopPortIntent {
	var byName = map[string]driver.DHCPSnoopPortIntent{}
	var names []string
	var it driver.DHCPSnoopPortIntent
	for _, it = range in {
		var name = strings.TrimSpace(it.Port)
		var _, seen = byName[name]
		if !seen {
			names = append(names, name)
		}
		it.Port = name
		byName[name] = it
	}
	sort.Strings(names)
	var out []driver.DHCPSnoopPortIntent
	var n string
	for _, n = range names {
		out = append(out, byName[n])
	}
	return out
}
