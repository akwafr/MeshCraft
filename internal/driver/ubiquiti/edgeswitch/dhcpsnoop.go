package edgeswitch

// DHCP snooping — [driver.DHCPSnoopReader] and [driver.DHCPSnoopApplier].
//
// # Status: THE READ IS CAPTURED, THE WRITES ARE PROVEN, THE CAPABILITY IS
// DECLARED
//
// Said before anything else, because it decides what this package advertises:
// [Driver.WritesExtra] answers true for "dhcp_snooping" since 2026-08-08
// (cf. contract.go), alongside "poe", "port_power_cycle", "port_admin" and
// "storm_control".
//
// UPDATED 2026-08-08. "show ip dhcp snooping" HAS now been run on the lab
// EdgePoint S16 (192.0.2.4, EP-S16, firmware 1.9.3) and its output is in
// testdata/show_ip_dhcp_snooping.txt. The parser below was written from the CLI
// reference and needed no change against it — the wording, the capital-D
// "Disabled", the "enabled on the following VLANs" header and the
// Interface / Trusted / Log Invalid Pkts columns are all as deduced.
//
// # ALL FOUR GESTURES ARE PROVEN LIVE (2026-08-08, lab EP-S16, firmware 1.9.3)
//
// Exercised through [Client.ApplyDHCPSnooping] itself rather than a shell — so
// the contract's own verify re-read was part of the experiment — and reverted
// afterwards, verified off:
//
//   - "ip dhcp snooping" — global enable. Reads back Enabled;
//   - "ip dhcp snooping vlan <id>" — per-VLAN. Reads back in the VLAN list;
//   - "ip dhcp snooping trust" — per-port. Reads back as a row in the trust
//     table, and the apply confirmed it in PortsWritten with nothing Skipped;
//   - trust REMOVAL through the same applier (Trusted:false). Reads back
//     untrusted, and the feature stays ON — the applier never disables it.
//
// testdata/show_ip_dhcp_snooping_enabled.txt is that state.
//
// # THE PORT MUST PARTICIPATE IN THE SNOOPED VLAN, OR THE TABLE STAYS EMPTY
//
// Worth recording because it cost a wrong conclusion first. The initial exercise
// trusted a port in VLAN 100 while snooping VLAN 4002, and the trust table came
// back EMPTY — no row — even though "show running-config interface 0/2" showed
// "ip dhcp snooping trust" plainly. The write had taken and no status command
// would admit it.
//
// Two readings fitted: either this firmware never reports trust in a status
// command, in which case the read had to move to the running configuration; or it
// reports a port only when that port participates in a snooped VLAN. Those have
// OPPOSITE fixes — one rewrites a working read to dodge a firmware bug — so
// neither was acted on until the experiment ran. Putting the same port into the
// snooped VLAN made the row appear immediately:
//
//	Interface    Trusted     Log Invalid Pkts
//	0/2          Yes          No
//
// So it is the second reading, and THIS CONNECTOR NEEDED NO CHANGE. A trusted
// port outside every inspected VLAN is also not a configuration anyone wants —
// trust exists for the uplink toward the DHCP server, which is by definition on
// the VLAN being snooped — so the empty table was the switch describing an
// incoherent request, not hiding a successful one.
//
// The lesson generalises past this range: an apply that reports Skipped because a
// re-read cannot see what it wrote may be describing the PLAN, not the write.
//
// [verifyDHCPSnoopTrust] is therefore correct as written, and its refusal to
// treat "not observed" as "trusted as asked" is what surfaced the incoherence
// instead of burying it.
//
// The 2026-08-05 running-config dump (testdata/show_running_config.txt) carries
// no snooping line of any kind, which is what a device whose snooping was never
// touched looks like and is not evidence in either direction.
//
// # Why WritesExtra WAS flipped, and what is still unexercised behind it
//
// This range went through the same staging as PoE — the code shipped, the
// capability did not, and TODO.md carried the live pass it owed until
// 2026-08-05 discharged it. The equivalent pass has now been done here, so the
// evidence bar was met and the declaration was taken as a deliberate decision
// on 2026-08-08. It is the only capability in this package proven before it was
// declared rather than the other way round (cf. contract.go's WritesExtra
// comment, and driver/dhcpsnoop.go's header for the rule).
//
// TWO PATHS ARE STILL UNEXERCISED, and neither blocked the declaration because
// neither is the gesture: a REFUSAL — a port this platform declines to trust, so
// [driver.ApplyResult.Skipped] carries a branch no device has walked — and what
// the device does across a reboot. TODO.md carries both.
//
// # The forms, and the mode trap that is this platform's specialty
//
//   - "ip dhcp snooping" — GLOBAL CONFIG. Switch-wide enable;
//   - "ip dhcp snooping vlan <id>" — GLOBAL CONFIG. Per-VLAN enable;
//   - "ip dhcp snooping trust" — INTERFACE CONFIG. The per-port flag, the
//     only per-port control there is (cf. driver/dhcpsnoop.go: "enabling
//     snooping on a port" names nothing a connector could send);
//   - "show ip dhcp snooping" — the read.
//
// These are Config-mode commands, UNLIKE "network parms", "vlan database"
// and "write memory", which are privileged EXEC on FASTPATH (cf. the package
// doc comment's MODE TRAPS). Getting that backwards is the failure this range
// produces most often, and it fails LOUDLY — "% Invalid input detected at '^'
// marker", which [reCLIError] catches — rather than silently, which is the one
// mercy of it.
//
// # What this connector will NOT do
//
// It never sends "no ip dhcp snooping" in its GLOBAL or PER-VLAN scope. The
// contract forbids both; this file extends the same caution to the VLANs it
// finds already enabled, which it re-sends nothing for and removes nothing
// from. Protection somebody else installed is not ours to take away because our
// plan did not happen to mention it.
//
// The INTERFACE scope is the exception, and the only one: "no ip dhcp snooping
// trust" is how a port is untrusted, which is the protected state and a write
// the contract explicitly wants available (cf. driver/dhcpsnoop.go — "untrusting
// a port is a per-port write"). It is sent only when a plan asks for that port
// to be untrusted, never as a consequence of anything else.
//
// The binding table ("show ip dhcp snooping binding") is not read: it is
// telemetry, fast-moving, and belongs to a monitoring path rather than to a
// configuration contract that drift compares — driver/dhcpsnoop.go's header
// makes that call, and this file obeys it rather than re-litigating it.

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"meshcraft/internal/driver"
)

// dhcpSnoopShow is the read command. Named once, so the error messages and the
// TODO line that still owes a capture all spell it the same way.
const dhcpSnoopShow = "show ip dhcp snooping"

// --- show ip dhcp snooping ----------------------------------------------
//
// The shape, now CAPTURED from the lab unit (testdata/show_ip_dhcp_snooping.txt
// for the off state, …_enabled.txt for the on state). The illustration below is
// the reference's, kept because it shows both a trusted and an untrusted row,
// which the lab unit has never printed at the same time:
//
//	DHCP snooping is Disabled
//	DHCP snooping source MAC verification is enabled
//	DHCP snooping is enabled on the following VLANs:
//	 1, 10 - 20
//
//	Interface   Trusted    Log Invalid Pkts
//	----------  ---------  ----------------
//	0/1         Yes        No
//	0/2         No         Yes
//
// THE TWO "DHCP snooping is …" LINES ARE DIFFERENT SENTENCES, and a reader
// that does not anchor on the end of the line reads the VLAN header as the
// global state — "DHCP snooping is enabled on the following VLANs:" starts
// with the same six words as "DHCP snooping is Enabled". A device with the
// feature OFF but a VLAN list left over from an earlier configuration would
// then report as protected. Hence the `$` on [reSnoopGlobal] and nowhere else.

var (
	// reSnoopGlobal: the switch-wide state, and ONLY that line (see above).
	reSnoopGlobal = regexp.MustCompile(`(?im)^\s*DHCP\s+snooping\s+is\s+(Enabled|Disabled)\s*$`)
	// reSnoopVLANHeader introduces the VLAN list, which the firmware prints on
	// the FOLLOWING line(s) rather than on this one.
	reSnoopVLANHeader = regexp.MustCompile(`(?i)DHCP\s+snooping\s+is\s+enabled\s+on\s+the\s+following\s+VLANs\s*:`)
	// reSnoopVLANList: a continuation line made of nothing but ids, commas,
	// ranges and spaces. "1, 10 - 20" — the same coalescing the running
	// config uses for VLAN membership, with spaces around the dash this time,
	// which is why [expandVlanList] is fed a space-stripped copy.
	reSnoopVLANList = regexp.MustCompile(`^[\s0-9,\-]+$`)
	// reSnoopPortRow: one row of the per-interface table. Both flags are
	// Yes/No words, so the row is pinned on them rather than on column
	// positions — this platform pads its tables differently from one command
	// to the next (cf. parse.go's "show port all" comment).
	reSnoopPortRow = regexp.MustCompile(`(?m)^\s*(\d+/\d+)\s+(Yes|No)\s+(Yes|No)\s*$`)
)

// Compile-time: this Client both reads and writes DHCP snooping trust.
//
// Co-located here rather than in contract.go's block, alongside the assertions
// portadmin.go and hostname.go make for their own capabilities: the declaration
// belongs next to the code that has to keep satisfying it, where an edit that
// breaks it fails in the same file.
//
// NOTE what this does NOT decide: satisfying the interface says "this code
// exists", while `WritesExtra("dhcp_snooping")` (contract.go) says "a click will
// reach the hardware". Only the second is a promise to an operator, and only
// evidence may flip it. On this connector both are now true — the interface
// since the code shipped, the declaration since the 2026-08-08 live exercise —
// but they remain two separate statements, and a future range will satisfy the
// first long before it earns the second.
var (
	_ driver.DHCPSnoopReader  = (*Client)(nil)
	_ driver.DHCPSnoopApplier = (*Client)(nil)
)

// parseDHCPSnooping reads "show ip dhcp snooping".
//
// The bool is NOT "did anything parse": it is "was the GLOBAL STATE LINE
// found". Everything else in this state is meaningless without it — a port
// list with no global flag would let a caller display trust flags on a device
// that inspects nothing, the precise failure driver/dhcpsnoop.go exists to
// prevent — and a command this firmware refused outright still returns text
// (the "% Invalid input" line itself), so "output was non-empty" cannot be
// the test.
func parseDHCPSnooping(out string) (driver.DHCPSnoopState, bool) {
	var st driver.DHCPSnoopState
	var g = reSnoopGlobal.FindStringSubmatch(out)
	if g == nil {
		return st, false
	}
	st.Enabled = strings.EqualFold(g[1], "Enabled")
	st.VLANs = parseSnoopVLANList(out)

	var d = &Driver{}
	var rows = reSnoopPortRow.FindAllStringSubmatch(out, -1)
	var logging []string
	var i int
	for i = range rows {
		var std, ok = d.StandardPortName(rows[i][1])
		if !ok {
			continue // a LAG row in slot 3, or the CPU interface: not a socket
		}
		st.Ports = append(st.Ports, driver.DHCPSnoopPortState{
			Port:    std,
			Trusted: strings.EqualFold(rows[i][2], "Yes"),
		})
		if strings.EqualFold(rows[i][3], "Yes") {
			logging = append(logging, std)
		}
	}
	// "GE2" before "GE10", numerically and not lexically. The ORDER comes from
	// driver/portsort.go; the SPLIT stays here because it is what this platform's
	// faceplate looks like — one kind of socket, GE<n> (cf. state.go).
	driver.SortPortNames(logging, splitStandard)
	driver.SortByPortName(st.Ports, snoopPortName, splitStandard)

	// The per-VLAN dimension EXISTS here, and saying so is the difference
	// between "snooping covers nothing" and "this platform has no VLAN
	// dimension to cover anything with" — two facts an empty VLANs slice
	// cannot tell apart (cf. driver.DHCPSnoopState.VLANs).
	st.Notes = append(st.Notes,
		"this platform enables snooping per VLAN (\"ip dhcp snooping vlan <id>\") on top "+
			"of the global switch: an empty VLAN list means the feature inspects nothing, "+
			"not that the dimension is absent")
	if len(logging) > 0 {
		// "Log Invalid Pkts" is an adjacent feature riding on the same
		// tables — reported rather than modelled, per the contract's header.
		st.Notes = append(st.Notes, fmt.Sprintf(
			"invalid-packet logging is on for %s — a setting this connector reads but never writes",
			strings.Join(logging, " ")))
	}
	return st, true
}

// parseSnoopVLANList collects the ids printed UNDER the "enabled on the
// following VLANs:" header. Returns them sorted and deduplicated.
//
// The list continues over as many lines as the firmware needs; the run ends at
// the first line that is not made of ids, commas, ranges and spaces — the blank
// line before the interface table, in every shape the reference prints.
func parseSnoopVLANList(out string) []int {
	var loc = reSnoopVLANHeader.FindStringIndex(out)
	if loc == nil {
		return nil
	}
	var seen = map[int]bool{}
	var ids []int
	var lines = strings.Split(strings.ReplaceAll(out[loc[1]:], "\r", ""), "\n")
	var i int
	for i = 1; i < len(lines); i++ { // lines[0] is the header's own tail
		if strings.TrimSpace(lines[i]) == "" {
			// A blank line right after the header is not the end of the list on
			// every firmware — but a blank line AFTER ids have been collected
			// is. Before any id, keep looking one line further.
			if len(ids) > 0 {
				break
			}
			continue
		}
		if !reSnoopVLANList.MatchString(lines[i]) {
			break
		}
		var id int
		for _, id = range expandVlanList(strings.ReplaceAll(lines[i], " ", "")) {
			if !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
	}
	sort.Ints(ids)
	return ids
}

// snoopPortName names the field the faceplate order is read from
// (driver.SortByPortName).
func snoopPortName(p driver.DHCPSnoopPortState) string { return p.Port }

// ReadDHCPSnooping reports the three levels: global, per VLAN, per port.
//
// FAILS rather than reporting a device as unprotected when the read is not
// understood. "show ip dhcp snooping" on a firmware that does not carry the
// feature answers "% Invalid input detected at '^' marker", which is text —
// a reader that returned the zero value on it would put "snooping: off" on
// screen for a device nobody has any information about, and off is precisely
// the reading an operator would then act on.
func (c *Client) ReadDHCPSnooping(ctx context.Context) (driver.DHCPSnoopState, error) {
	var out, err = c.cli.run(ctx, dhcpSnoopShow, showTimeout)
	var st, ok = parseDHCPSnooping(out)
	if ok {
		return st, nil
	}
	if err != nil {
		return driver.DHCPSnoopState{}, fmt.Errorf("%s on %s: %w", dhcpSnoopShow, c.host, err)
	}
	return driver.DHCPSnoopState{}, fmt.Errorf(
		"%s on %s: no \"DHCP snooping is Enabled/Disabled\" line in the answer — "+
			"reporting the feature as off on that basis would be a guess about a "+
			"protection, and this connector does not make those", dhcpSnoopShow, c.host)
}

// ApplyDHCPSnooping sets per-port trust, and turns the feature on above it.
//
// THE GLOBAL AND PER-VLAN ENABLE ARE PART OF THE GESTURE (cf.
// driver/dhcpsnoop.go): a port trusted on a device whose global snooping is off
// is an exact configuration with zero protection behind it, and the screen
// would show the flag with nothing to say the feature above it is asleep. Both
// enables are reported in [driver.ApplyResult.Notes], because they change the
// device for every port and not only for the ones that were clicked.
//
// It NEVER turns anything off — not the global switch, not a VLAN, whether or
// not the plan mentions it. The only thing that goes down here is one port's
// trust flag, when the plan explicitly asks for untrusted.
//
// PROVEN LIVE: cf. the file header. All four gestures — global enable, per-VLAN
// enable, trust and trust removal — were driven through THIS method on the lab
// EP-S16 (1.9.3) on 2026-08-08, each confirmed by the verify re-read below, and
// reverted afterwards. What has never been observed is a REFUSAL: no port on
// that unit declined to be trusted, so the [driver.ApplyResult.Skipped] branches
// are still reasoned rather than witnessed.
func (c *Client) ApplyDHCPSnooping(ctx context.Context, plan driver.DHCPSnoopPlan) (driver.ApplyResult, error) {
	var res driver.ApplyResult
	var err = c.requireWritable()
	if err != nil {
		return res, err
	}
	if len(plan.Ports) == 0 && len(plan.VLANs) == 0 {
		return res, nil
	}

	// The pre-read decides two things nothing else can: whether the global
	// enable has to be sent (and therefore REPORTED), and which VLANs are
	// already covered so this write adds to them instead of restating them.
	// Same rule as ApplyPoE and ApplyPorts: a write that could not read first
	// does not proceed.
	var before driver.DHCPSnoopState
	before, err = c.ReadDHCPSnooping(ctx)
	if err != nil {
		return res, fmt.Errorf("reading DHCP snooping before applying: %w", err)
	}
	var haveVLAN = map[int]bool{}
	var id int
	for _, id = range before.VLANs {
		haveVLAN[id] = true
	}
	var haveTrust = map[string]bool{}
	var known = map[string]bool{}
	var ps driver.DHCPSnoopPortState
	for _, ps = range before.Ports {
		haveTrust[ps.Port] = ps.Trusted
		known[ps.Port] = true
	}

	var _, cfgErr = c.cli.run(ctx, "configure", cmdTimeout)
	if cfgErr != nil {
		return res, fmt.Errorf("entering Global Config on %s: %w", c.host, cfgErr)
	}

	// 1) The global switch, only when it is off.
	if !before.Enabled {
		var _, gErr = c.cli.run(ctx, "ip dhcp snooping", cmdTimeout)
		if gErr != nil {
			_, _ = c.cli.run(ctx, "exit", cmdTimeout)
			return res, fmt.Errorf("enabling DHCP snooping globally on %s: %w", c.host, gErr)
		}
		res.Notes = append(res.Notes,
			"DHCP snooping was OFF on this device and has been turned ON globally — that "+
				"affects EVERY port, not only the ones in this plan: every untrusted port "+
				"now drops DHCP server replies")
		c.step(true, "DHCP snooping enabled globally", "\"ip dhcp snooping\"")
	}

	// 2) The VLANs, adding to what is there. Never removing.
	//
	// SORTED AND DEDUPLICATED FIRST: the plan is a set, and a caller that
	// happens to spell it "20, 10, 10" must produce the same three commands as
	// one that spells it "10, 20". Without that, the same gesture sent twice
	// emits a different command list each time — VLAN 10 configured twice, the
	// Notes naming the VLANs in the caller's incidental order — and nothing
	// downstream can compare two applies.
	var added []int
	for _, id = range sortedSnoopVLANs(plan.VLANs) {
		if id < 1 || id > 4093 {
			// cf. Capabilities.VlanRange — this platform's space is 1-4093, not
			// the 1-4094 most of the fleet uses.
			res.Skipped = append(res.Skipped, fmt.Sprintf(
				"VLAN %d: outside this platform's VLAN space (1-4093) — snooping not enabled on it", id))
			continue
		}
		if haveVLAN[id] {
			continue // already covered, by us or by somebody else
		}
		var _, vErr = c.cli.run(ctx, fmt.Sprintf("ip dhcp snooping vlan %d", id), cmdTimeout)
		if vErr != nil {
			res.Skipped = append(res.Skipped, fmt.Sprintf("VLAN %d: %v", id, vErr))
			continue
		}
		added = append(added, id)
	}
	if len(added) > 0 {
		res.Notes = append(res.Notes, fmt.Sprintf(
			"DHCP snooping now inspects VLAN(s) %s — added to what the device already covered, "+
				"nothing was removed", intsWords(added)))
		c.step(true, fmt.Sprintf("DHCP snooping enabled on %d VLAN(s)", len(added)), intsWords(added))
	}

	// 3) The trust flags, one interface block per port.
	var wrote []string
	var want = map[string]bool{}
	var pi driver.DHCPSnoopPortIntent
	for _, pi = range plan.Ports {
		var _, _, okName = splitStandard(pi.Port)
		if !okName {
			res.Skipped = append(res.Skipped, fmt.Sprintf(
				"%s: not a port name this connector recognizes on this platform", pi.Port))
			continue
		}
		want[pi.Port] = pi.Trusted
		if known[pi.Port] && haveTrust[pi.Port] == pi.Trusted {
			continue // already carries the intent
		}
		var _, ifErr = c.cli.run(ctx, "interface "+cliPortName(pi.Port), cmdTimeout)
		if ifErr != nil {
			res.Skipped = append(res.Skipped, fmt.Sprintf("%s: %v", pi.Port, ifErr))
			continue
		}
		var cmd = "no ip dhcp snooping trust"
		if pi.Trusted {
			cmd = "ip dhcp snooping trust"
		}
		var _, tErr = c.cli.run(ctx, cmd, cmdTimeout)
		_, _ = c.cli.run(ctx, "exit", cmdTimeout)
		if tErr != nil {
			res.Skipped = append(res.Skipped, fmt.Sprintf("%s: %v", pi.Port, tErr))
			continue
		}
		// The device now carries the intent, so a plan naming the same port
		// twice opens ONE interface block rather than two — same reason the VLAN
		// list is deduplicated above, and a second identical write is only a
		// second chance to fail.
		haveTrust[pi.Port] = pi.Trusted
		known[pi.Port] = true
		wrote = append(wrote, pi.Port)
	}
	_, _ = c.cli.run(ctx, "exit", cmdTimeout) // back out of Global Config

	if len(plan.VLANs) == 0 {
		if len(before.VLANs) == 0 {
			res.Notes = append(res.Notes,
				"no VLAN could be derived from the ports this plan names, and the site declares "+
					"none either: the feature is on and inspects NOTHING yet — a trusted port "+
					"here is a flag with no protection behind it")
		} else {
			// Saying "inspects nothing" on THIS device would be false: it
			// carried VLANs before the gesture and this connector removed none
			// of them. Notes is where an operator decides whether the protection
			// is real, and a false sentence there is worse than no sentence.
			res.Notes = append(res.Notes, fmt.Sprintf(
				"this plan names no VLAN to inspect: the feature keeps inspecting the VLAN(s) "+
					"the device already carried (%s), which this connector never removes",
				intsWords(before.VLANs)))
		}
	}
	if len(wrote) == 0 && len(added) == 0 && before.Enabled {
		return res, nil // nothing was sent, so there is nothing to verify
	}

	// Verify. A snooping setting is a protection: nothing here is taken as done
	// on the strength of a command that did not complain (the discipline
	// ApplyPoE states at length).
	//
	// THE GLOBAL AND PER-VLAN ENABLES ARE RE-READ TOO, not only the trust flags.
	// They are the levels every trust flag is inert without, so a gesture that
	// touched ONLY them — a plan with VLANs and no port — still owes the caller
	// a re-read: "a device left with the feature off after a successful-looking
	// apply would be the one failure this contract exists to prevent"
	// (driver/dhcpsnoop.go).
	var after, readErr = c.ReadDHCPSnooping(ctx)
	if readErr != nil {
		res.Skipped = append(res.Skipped, fmt.Sprintf(
			"the configuration was sent but could not be read back (%v) — treat it as unverified",
			readErr))
		// AND NOT SAVED, deliberately: "write memory confirm" below is only sent
		// once the re-read has confirmed what it would be baking into flash. An
		// unverified write left in the running configuration is one a power cycle
		// still undoes; the same write in flash is one nobody can undo by accident.
		res.Notes = append(res.Notes,
			"unverified, and therefore NOT saved: \"write memory confirm\" was not sent, so a "+
				"reboot restores whatever the startup configuration says")
		return res, nil
	}
	if !before.Enabled && !after.Enabled {
		res.Skipped = append(res.Skipped, fmt.Sprintf(
			"\"ip dhcp snooping\" was sent but \"%s\" still reports the feature Disabled — "+
				"every trust flag on this device is inert", dhcpSnoopShow))
	}
	var coversAfter = map[int]bool{}
	for _, id = range after.VLANs {
		coversAfter[id] = true
	}
	for _, id = range added {
		if !coversAfter[id] {
			res.Skipped = append(res.Skipped, fmt.Sprintf(
				"VLAN %d: \"ip dhcp snooping vlan %d\" was sent but the VLAN is absent from "+
					"\"%s\" afterwards — snooping does not inspect it", id, id, dhcpSnoopShow))
		}
	}

	// PortsWritten is what the DEVICE CONFIRMS, not what was sent: a port whose
	// re-read disagrees was not reconfigured (cf.
	// driver.ApplyResult.PortsWritten, "the ports actually reconfigured"), and
	// reporting it as written is how a screen ends up showing a protection that
	// is not in place. The disagreement goes to Skipped, which is where a
	// partial apply has to be legible.
	var confirmed, mismatched = verifyDHCPSnoopTrust(after, wrote, want)
	res.Skipped = append(res.Skipped, mismatched...)
	if len(confirmed) > 0 {
		res.PortsWritten = confirmed
		c.step(true, fmt.Sprintf("DHCP snooping trust set on %d port(s)", len(confirmed)),
			strings.Join(confirmed, " "))
	}
	if len(mismatched) > 0 {
		c.step(false, fmt.Sprintf("DHCP snooping trust NOT confirmed on %d port(s)", len(mismatched)),
			strings.Join(mismatched, " ; "))
	}

	// PERSIST, and last: protection a reboot forgets is protection nobody has.
	//
	// LAST because it comes after the verification above and not before it — a
	// "write memory confirm" sent first would bake into flash a configuration
	// nothing had confirmed. And a failed save does NOT fail the apply: the write
	// genuinely landed in the running configuration, and returning an error here
	// would claim it did not. It becomes a Note and a failed transcript step
	// instead — [Client.Persist] emits the step itself, which is why nothing is
	// added on the success path (the shape ApplyStormControl already uses).
	var perr = c.Persist(ctx)
	if perr != nil {
		res.Notes = append(res.Notes, "applied but NOT persisted: "+firstLine(perr.Error())+
			" — a reboot would take this snooping configuration with it, and the device would "+
			"come back inspecting whatever it inspected before")
		return res, nil
	}
	return res, nil
}

// sortedSnoopVLANs is the plan's VLAN list turned into a set: sorted, with
// duplicates removed. Ids outside this platform's space are left in — refusing
// them is [Client.ApplyDHCPSnooping]'s job, and it REPORTS each one in
// [driver.ApplyResult.Skipped] rather than dropping it here where nothing would
// say so.
func sortedSnoopVLANs(ids []int) []int {
	var seen = map[int]bool{}
	var out = make([]int, 0, len(ids))
	var id int
	for _, id = range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	sort.Ints(out)
	return out
}

// verifyDHCPSnoopTrust compares what was written against what the device says
// afterwards. It returns the ports the re-read CONFIRMS — the only ones a caller
// may be told were written — and a phrased mismatch for each of the others.
//
// A port that vanished from the re-read is a mismatch too, and returning it as
// confirmed would throw away the distinction the whole read side is built on:
// "not observed" is not "trusted as asked" (cf.
// driver.DHCPSnoopPortState.Trusted).
func verifyDHCPSnoopTrust(after driver.DHCPSnoopState, wrote []string,
	want map[string]bool) (confirmed []string, mismatched []string) {

	var seen = map[string]bool{}
	var trusted = map[string]bool{}
	var ps driver.DHCPSnoopPortState
	for _, ps = range after.Ports {
		seen[ps.Port] = true
		trusted[ps.Port] = ps.Trusted
	}
	var name string
	for _, name = range wrote {
		if !seen[name] {
			mismatched = append(mismatched, fmt.Sprintf(
				"%s: trust was sent but the port does not appear in \"%s\" afterwards",
				name, dhcpSnoopShow))
			continue
		}
		if trusted[name] != want[name] {
			mismatched = append(mismatched, fmt.Sprintf(
				"%s: sent trusted=%v, reads back trusted=%v in \"%s\"",
				name, want[name], trusted[name], dhcpSnoopShow))
			continue
		}
		confirmed = append(confirmed, name)
	}
	return confirmed, mismatched
}

// intsWords renders a VLAN list for an operator's eyes: "42 98 99".
func intsWords(ids []int) string {
	var parts = make([]string, 0, len(ids))
	var id int
	for _, id = range ids {
		parts = append(parts, fmt.Sprint(id))
	}
	return strings.Join(parts, " ")
}
