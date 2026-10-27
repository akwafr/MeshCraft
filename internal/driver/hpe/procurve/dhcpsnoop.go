package procurve

// DHCP snooping on the 2600 range.
//
// # The vocabulary is CAPTURED, and it is not the shape the documentation
// leads you to expect
//
// The reference unit was already running snooping when it was surveyed, which
// is the best kind of evidence this package can have: the forms below are not
// deduced from a 2007 PDF, they are lines the switch printed back at us.
// Verbatim from SW-PLAYER-79 (2610-48, R.11.121, 2026-08-03), in
// testdata/show_running_config.txt:
//
//	no dhcp-relay
//	dhcp-snooping
//	…
//	interface 45
//	   dhcp-snooping trust
//	   exit
//	interface 46
//	   dhcp-snooping trust
//	   exit
//
// TRUST IS AN INTERFACE-LEVEL COMMAND HERE, and that is the fact to carry away
// from this file. The ProVision documentation for this family also describes a
// GLOBAL form — "dhcp-snooping trust <port-list>", one statement carrying a
// port list, which is a real shape difference from the IOS-like platforms where
// trust only ever exists under an interface. This firmware accepts the global
// spelling and does NOT print it back: what it writes into its own
// configuration is one "interface N" stanza per trusted port. Since on this
// platform the running configuration IS the command language — the same
// argument apply.go and snmp.go ship on — the interface form is the CAPTURED
// one and it is the one this connector writes. The global form is left alone
// precisely because nothing here has ever seen the device produce it.
//
// # The reference unit was protecting nothing, and that is why this file reads
// all three levels
//
// Its configuration carries the global "dhcp-snooping", eight trusted uplink
// ports (45-52), and NO "dhcp-snooping vlan" line at all. On this range that
// combination inspects zero traffic: snooping only looks at the VLANs it has
// been told to look at. A screen showing eight green "trusted" flags on that
// switch would be describing a protection that does not exist — which is
// exactly the failure driver/dhcpsnoop.go's header predicts, found in the field
// on the first ProCurve meshcraft ever surveyed.
//
// So [Client.ReadDHCPSnooping] reports the global switch, the VLAN list and the
// per-port flags together, and says so in Notes when the middle one is empty.
//
// # What is NOT read, and why
//
// "show dhcp-snooping" and "show dhcp-snooping binding" exist on this
// family and have NEVER been captured on this hardware. Their table shapes are
// therefore unknown, and this package does not parse tables it has not seen —
// table.go's whole existence is the story of what happens when a column moves.
// The configuration is read instead: it is authoritative for what was
// CONFIGURED, which is what this contract compares. The binding table is
// telemetry and belongs to a monitoring path anyway (cf. driver/dhcpsnoop.go).

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"meshcraft/internal/driver"
)

// The lines this platform writes, as it writes them.
//
// EVERY ANCHOR TOLERATES A LEADING CARRIAGE RETURN AND A TRAILING SPACE, for
// the reason parse.go documents at length: this device terminates lines with
// LF then CR, so every line begins with the previous terminator's stray `\r`,
// and it pads some lines with a trailing blank and not others. In the capture,
// "interface 1" carries a trailing space and "interface 45" does not — one
// anchor written without the tolerance would read half the stanzas.
var (
	// reSnoopGlobal: the global switch, which is a BARE line. The `$` anchor is
	// what keeps it from also matching "dhcp-snooping trust" and
	// "dhcp-snooping vlan 10", and the absence of a `no` alternative is what
	// makes "no dhcp-snooping" read as off.
	reSnoopGlobal = regexp.MustCompile(`(?m)^[ \t\r]*dhcp-snooping[ \t\r]*$`)
	// reSnoopVLAN: DOCUMENTED, not captured — the reference unit has no such
	// line (cf. the header). Read anyway: a device someone else configured
	// properly must not be reported as covering nothing.
	reSnoopVLAN = regexp.MustCompile(`(?m)^[ \t\r]*dhcp-snooping vlan\s+([0-9,\- \t]+?)[ \t\r]*$`)
	// reInterfaceOpen: an interface stanza. The capture only ever opens one
	// port at a time, but this CLI accepts a list — "interface 45-52" — and a
	// configuration written by hand may well carry it, so the port range is
	// parsed rather than assumed to be a single number.
	reInterfaceOpen = regexp.MustCompile(`(?m)^[ \t\r]*interface\s+([0-9,\-]+)[ \t\r]*$`)
	// reSnoopTrust: the per-port flag, CAPTURED. The optional "no" is read for
	// the same reason parse.go reads "no untagged": the device spells removals
	// out, and treating one as an assignment inverts the meaning.
	reSnoopTrust = regexp.MustCompile(`(?m)^[ \t\r]*(no\s+)?dhcp-snooping trust[ \t\r]*$`)
)

// dhcpSnoop is what the running configuration says about snooping.
//
// Trusted holds ONLY the ports whose flag the configuration actually states.
// Absence means "this configuration does not mention that port", which on a
// platform that records departures from its defaults means untrusted — but the
// two are kept apart here and joined in [Client.ReadDHCPSnooping], where the
// faceplate says which ports exist at all. Guessing the port list from the
// configuration would report a 48-port switch as having eight ports.
type dhcpSnoop struct {
	Enabled bool
	VLANs   []int
	Trusted map[int]bool
}

// parseDHCPSnooping reads the three levels out of "show running-config".
func parseDHCPSnooping(config string) dhcpSnoop {
	var out = dhcpSnoop{Trusted: map[int]bool{}}
	out.Enabled = reSnoopGlobal.MatchString(config)

	var m []string
	for _, m = range reSnoopVLAN.FindAllStringSubmatch(config, -1) {
		// Several lines may each name part of the coverage; they accumulate.
		out.VLANs = append(out.VLANs, parseNumberList(m[1])...)
	}
	out.VLANs = dedupSorted(out.VLANs)

	// Interface stanzas, scanned the way parse.go scans VLAN stanzas: from one
	// opener to the next, cut at the stanza's own "exit". Splitting on "exit"
	// instead would swallow the VLAN blocks, which use the same keyword.
	var locs = reInterfaceOpen.FindAllStringSubmatchIndex(config, -1)
	var i int
	for i = 0; i < len(locs); i++ {
		var ports = parsePortRange(config[locs[i][2]:locs[i][3]])
		if len(ports) == 0 {
			continue
		}
		var end = len(config)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		var body = config[locs[i][1]:end]
		var cut = reExitLine.FindStringIndex(body)
		if cut != nil {
			body = body[:cut[0]]
		}
		var hit = reSnoopTrust.FindStringSubmatch(body)
		if hit == nil {
			continue
		}
		var trusted = strings.TrimSpace(hit[1]) == ""
		var p int
		for _, p = range ports {
			out.Trusted[p] = trusted
		}
	}
	return out
}

// parseNumberList expands this platform's compact numeric list, accepting the
// SPACE separator its VLAN lists use alongside the comma its port lists use
// ("dhcp-snooping vlan 1-4 10" and "untagged 45-52" are the same grammar
// with a different delimiter). Ranges are handled by [parsePortRange], which is
// where that grammar is already tested against the device's own output.
func parseNumberList(s string) []int {
	return parsePortRange(strings.Join(strings.Fields(s), ","))
}

// dedupSorted returns the values sorted with duplicates removed.
func dedupSorted(v []int) []int {
	if len(v) == 0 {
		return nil
	}
	var seen = map[int]bool{}
	var out []int
	var n int
	for _, n = range v {
		if seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	sortInts(out)
	return out
}

// snoopConfig re-reads the device and parses the snooping configuration.
//
// One command, and it is one this connector already runs everywhere else: the
// running configuration answers all three levels at once. On a range that hands
// out FOUR management sessions in total and holds an abandoned one for two
// minutes (cf. cli.go), a read that needed a view of its own for each level
// would be a read an operator learns to avoid.
func (c *Client) snoopConfig(ctx context.Context) (dhcpSnoop, string, error) {
	var text, err = c.cli.run(ctx, "show running-config", showRunTimeout)
	if err != nil && strings.TrimSpace(text) == "" {
		return dhcpSnoop{}, "", fmt.Errorf("show running-config on %s: %v",
			c.tgt.Host, firstLine(err.Error()))
	}
	if len(strings.TrimSpace(text)) < minRunningConfig {
		// Same refusal as ReadState's, and for the same reason: a short read on
		// this CLI is a desynchronisation, not an empty switch. Reporting it as
		// state would say "snooping is off" about a device that is running it,
		// and an apply would then turn on a feature that was already on and
		// report that turn-on to the operator.
		return dhcpSnoop{}, "", fmt.Errorf("running-config read returned %d characters (%q): "+
			"the CLI read desynchronised, and reporting DHCP snooping as absent from "+
			"this would be a claim about a device we did not read", len(text), text)
	}
	return parseDHCPSnooping(text), text, nil
}

// ReadDHCPSnooping reports the global switch, the covered VLANs, and the trust
// flag of every port on the faceplate.
//
// TWO SOURCES, and the second is what makes "untrusted" mean something. The
// configuration only names the ports whose trust was CHANGED from the default;
// "show interfaces brief" is what says which sockets exist. Without it this
// method could only report the trusted ports, and a caller reading
// [driver.DHCPSnoopState.Ports] would see eight entries on a 48-port switch and
// have no way to tell the other forty from ports that were never read — which
// is the distinction the contract asks this field to preserve.
//
// A faceplate read that fails is NOT a failure of this read: the trusted ports
// are still authoritative, and the shortfall is stated in Notes.
func (c *Client) ReadDHCPSnooping(ctx context.Context) (driver.DHCPSnoopState, error) {
	var out driver.DHCPSnoopState
	if c.cli == nil {
		return out, errNoSession
	}
	var snoop, _, err = c.snoopConfig(ctx)
	if err != nil {
		return out, err
	}
	out.Enabled = snoop.Enabled
	out.VLANs = snoop.VLANs

	var prefix = c.portPrefix()
	var brief, bErr = c.cli.run(ctx, "show interfaces brief", showRunTimeout)
	var faceplate = parseInterfacesBrief(brief)
	if len(faceplate) == 0 {
		// THE SHORTFALL BELONGS TO AN EMPTY FACEPLATE, not to a read that
		// returned an error. A "show interfaces brief" that came back garbled
		// errors WITH text on the wire and parses to no rows, and the note used
		// to be attached only when the text was empty too — so the caller got
		// eight ports on a 48-port switch with nothing saying the other forty had
		// never been looked at, which is precisely the distinction
		// driver.DHCPSnoopState.Ports exists to preserve.
		var why = "the view parsed to no rows"
		if bErr != nil {
			why = firstLine(bErr.Error())
		}
		out.Notes = append(out.Notes,
			"the faceplate could not be read ("+why+"): only the ports the configuration "+
				"names are listed below. The others were NOT observed, which is not the "+
				"same as untrusted")
	}
	out.Ports = snoopPortStates(snoop, faceplate, prefix)

	out.Notes = append(out.Notes, snoopCoverageNote(snoop)...)
	out.Notes = append(out.Notes,
		"read from \"show running-config\": this range's \"show dhcp-snooping\" views have "+
			"never been captured on this hardware, so the binding table, any rate limit and "+
			"option-82 handling are NOT reported here — they are not absent from the device")
	return out, nil
}

// snoopPortStates joins the two reads into the per-port list the contract asks
// for, SORTED by port number.
//
// THE UNION OF BOTH SOURCES, and not the faceplate alone. The configuration is
// authoritative for TRUST and "show interfaces brief" is authoritative for
// which sockets EXIST — so a faceplate that came back short must not delete a
// trusted port from the report. Enumerating only the faceplate did exactly that:
// let the brief stop at port 44 on the reference unit and its eight trusted
// uplinks vanish from the state, leaving a screen that says no port on this
// switch is trusted when eight of them are.
//
// A port in NEITHER source is absent from the result, which is the contract's
// "not read" and not [driver.DHCPSnoopPortState] with Trusted false. A port the
// faceplate shows and the configuration does not mention is untrusted, and that
// is a reading of the device and not a default: this platform's running
// configuration records every departure from its defaults, so silence about a
// port that exists is the device saying "untrusted".
func snoopPortStates(snoop dhcpSnoop, faceplate map[int]portStatus,
	prefix string) []driver.DHCPSnoopPortState {
	var seen = map[int]bool{}
	var nums []int
	var n int
	for n = range faceplate {
		if !seen[n] {
			seen[n] = true
			nums = append(nums, n)
		}
	}
	for n = range snoop.Trusted {
		if !seen[n] {
			seen[n] = true
			nums = append(nums, n)
		}
	}
	sortInts(nums)
	var out []driver.DHCPSnoopPortState
	for _, n = range nums {
		out = append(out, driver.DHCPSnoopPortState{
			Port:    prefix + strconv.Itoa(n),
			Trusted: snoop.Trusted[n],
		})
	}
	return out
}

// snoopCoverageNote states the one thing a trust flag cannot say on its own.
//
// A device with the global feature on and no VLAN named inspects NOTHING, and
// the reference unit for this connector is in exactly that state (cf. the
// header). An operator looking at a green trust flag has to be told.
func snoopCoverageNote(s dhcpSnoop) []string {
	if !s.Enabled {
		return []string{
			"DHCP snooping is OFF switch-wide: every trust flag below is inert until " +
				"\"dhcp-snooping\" is set, whatever it says",
		}
	}
	if len(s.VLANs) == 0 {
		return []string{
			"DHCP snooping is ON switch-wide but covers NO VLAN: this range only inspects " +
				"the VLANs \"dhcp-snooping vlan <list>\" names, so nothing is being filtered " +
				"and the trust flags below have no effect yet. The 2610-48 surveyed for this " +
				"connector was configured exactly this way",
		}
	}
	return nil
}

// ApplyDHCPSnooping sets per-port trust, and turns the feature on around it.
//
// # It enables, and it never disables
//
// The contract (driver/dhcpsnoop.go) puts the global and per-VLAN enable HERE
// rather than on the caller, and the reason is the state this connector's own
// reference unit was found in: trust flags on a device that inspects nothing.
// So a plan that trusts a port also gets "dhcp-snooping", and gets the plan's
// VLANs added to whatever coverage the device already had — reported in Notes,
// because turning on a switch-wide filter affects every port and not only the
// ones that were clicked.
//
// Nothing is ever turned OFF here, and no VLAN already covered is removed. A
// VLAN someone else put under snooping is protection meshcraft did not install
// and has no business taking away, and disabling the feature because a plan
// stopped naming it would strip a whole switch on the strength of one click.
//
// # The VLAN list is sent as a UNION, deliberately
//
// "dhcp-snooping vlan <list>" is the one form in this file that has NEVER
// been captured on this hardware (cf. the header), and the documentation for
// this family does not settle whether the list ACCUMULATES or REPLACES.
// Sending the union of what the device already covers and what the plan asks
// for is correct under both readings: under accumulate it re-states VLANs that
// are already there, which costs nothing; under replace it preserves them,
// which is what the contract requires. Sending only the missing ones would be
// correct under accumulate and would silently strip the device under replace.
//
// EVERY WRITE IS VERIFIED BY RE-READING. On this CLI a refused command prints
// a "%" line and changes nothing — there is no exit status — and a snooping
// configuration that did not take is invisible until someone plugs a rogue DHCP
// server in.
func (c *Client) ApplyDHCPSnooping(ctx context.Context,
	plan driver.DHCPSnoopPlan) (driver.ApplyResult, error) {
	var out driver.ApplyResult
	if c.cli == nil {
		return out, errNoSession
	}

	// A write NEVER acts on a cached state.
	var before, _, err = c.snoopConfig(ctx)
	if err != nil {
		return out, fmt.Errorf("reading DHCP snooping before writing: %v", err)
	}

	// 1. The feature itself, and its coverage.
	var global, _, addsVLAN = snoopGlobalCmds(before, plan.VLANs)
	if len(global) > 0 {
		if err = c.runConfigBlock(ctx, global); err != nil {
			c.step(false, "DHCP snooping NOT enabled", firstLine(err.Error()))
			return out, fmt.Errorf("enabling DHCP snooping on %s: %v", c.tgt.Host,
				firstLine(err.Error()))
		}
		c.step(true, "DHCP snooping enabled", strings.Join(global, " ; "))
	}

	// 2. Per-port trust, ONE stanza per port.
	//
	// Per port and not as a range, even though this CLI accepts "interface
	// 45-52": the device writes one stanza per port in its own configuration,
	// so that is the captured form. Per port is also what lets a refusal on one
	// jack land in Skipped for that jack while the rest of the plan proceeds —
	// the same shape applyPortSTP uses.
	var writes, untranslatable = snoopPortPlan(before, plan.Ports)
	out.Skipped = append(out.Skipped, untranslatable...)
	var wanted = map[int]bool{}
	var w snoopPortWrite
	for _, w = range writes {
		wanted[w.Num] = w.Trusted
		if len(w.Cmds) == 0 {
			continue // already carries the intent: no round trip, no flash churn
		}
		if err = c.runConfigBlock(ctx, w.Cmds); err != nil {
			out.Skipped = append(out.Skipped, w.Port+": "+firstLine(err.Error()))
			// Dropped from the verification set BECAUSE THE CLI REFUSED IT. Left in,
			// the re-read below finds the flag absent and says "trust was accepted
			// by the CLI and is absent from the configuration afterwards" about a
			// port whose refusal is already two lines up — a second entry in Skipped
			// making the opposite claim about the same jack.
			delete(wanted, w.Num)
			continue
		}
		out.PortsWritten = append(out.PortsWritten, w.Port)
	}

	if len(global) == 0 && len(out.PortsWritten) == 0 {
		if len(out.Skipped) > 0 {
			// NOT "already carries the intent". Every write this plan asked for was
			// refused, and reporting that as an idempotent no-op puts a green step
			// under a gesture that changed nothing it was asked to change.
			c.step(false, "DHCP snooping NOT written: every change was refused",
				strings.Join(out.Skipped, " ; "))
		} else {
			c.step(true, "DHCP snooping already carries the intent", "nothing written")
		}
		out.Notes = append(out.Notes, snoopCoverageNote(before)...)
		return out, nil
	}

	// 3. VERIFY, then say what changed for the WHOLE switch and not just for the
	//    ports that were named.
	var after, _, verr = c.snoopConfig(ctx)
	if verr != nil {
		// The note said "written but NOT verified" and left every accepted port in
		// PortsWritten, which is the field claiming the opposite of the note (cf.
		// driver.ApplyResult.PortsWritten). Retracted, and the ports are named.
		out.Unconfirmed("DHCP snooping trust", out.PortsWritten, firstLine(verr.Error()))
		out.PortsWritten = nil
	} else {
		if !after.Enabled {
			// The one failure this contract exists to prevent: trust flags on a
			// device that inspects nothing, reported as a successful apply.
			return out, fmt.Errorf("\"dhcp-snooping\" was accepted by the CLI and the "+
				"configuration of %s still does not carry it: the feature is OFF, every "+
				"trust flag just written is inert, and this apply must not be reported as "+
				"protection", c.tgt.Host)
		}
		var num int
		var want bool
		var mismatched []string
		for num, want = range wanted {
			if after.Trusted[num] != want {
				mismatched = append(mismatched, c.portPrefix()+strconv.Itoa(num))
			}
		}
		if len(mismatched) > 0 {
			sortStrings(mismatched)
			out.Skipped = append(out.Skipped, "trust was accepted by the CLI and is absent "+
				"from the configuration afterwards on: "+strings.Join(mismatched, ", ")+
				" — the write did not take on those ports")
			out.PortsWritten = withoutStrings(out.PortsWritten, mismatched)
		}
		if !before.Enabled {
			out.Notes = append(out.Notes,
				"DHCP snooping was OFF on this switch and has been turned ON for the WHOLE "+
					"device: from now on every port that is not trusted drops the DHCP server "+
					"replies it sees. Nobody asked for that per port, and it affects ports this "+
					"plan does not name")
		}
		if addsVLAN {
			out.Notes = append(out.Notes, fmt.Sprintf(
				"DHCP snooping now covers VLAN(s) %s (it covered %s before)",
				formatPortRange(after.VLANs), coveredBefore(before.VLANs)))
		}
		out.Notes = append(out.Notes, snoopCoverageNote(after)...)
	}

	// 4. Persist. A filter that a reboot undoes is worse than one that was
	//    refused: it was observed as in place, and the screen will keep saying so.
	if err = c.persist(ctx); err != nil {
		out.Notes = append(out.Notes,
			"DHCP snooping applied but NOT saved: it will be lost at the next reboot ("+
				firstLine(err.Error())+")")
		return out, nil
	}
	c.step(true, "configuration persisted", "write memory")
	return out, nil
}

// snoopGlobalCmds is the switch-wide half of the gesture: what to send so the
// feature is ON and covers the plan's VLANs ON TOP of whatever the device
// already had. It returns the commands, the union it computed, and whether that
// union adds anything to the device's existing coverage.
//
// PURE, and split out from the session deliberately: the two things that must be
// true here are properties of the COMMAND LIST — that no disable form is ever in
// it, and that the VLAN list is the union and never the plan on its own — and a
// list is something a test can hold to them without a switch on the other end.
//
// NOTHING IS EVER TURNED OFF. There is no branch below that emits "no
// dhcp-snooping" or "no dhcp-snooping vlan": a plan that stopped naming a
// VLAN is not a request to strip a whole switch's protection (cf.
// driver/dhcpsnoop.go), and a VLAN someone else put under snooping is protection
// meshcraft did not install.
func snoopGlobalCmds(before dhcpSnoop, planVLANs []int) (cmds []string,
	union []int, addsVLAN bool) {
	if !before.Enabled {
		cmds = append(cmds, "dhcp-snooping")
	}
	union = dedupSorted(append(append([]int(nil), before.VLANs...), planVLANs...))
	// before.VLANs is itself sorted and deduplicated by parseDHCPSnooping, so a
	// longer union is exactly "the plan named a VLAN this device does not cover".
	addsVLAN = len(union) > len(before.VLANs)
	if addsVLAN {
		cmds = append(cmds, "dhcp-snooping vlan "+formatPortRange(union))
	}
	return cmds, union, addsVLAN
}

// snoopPortWrite is one socket's half of the gesture, resolved against what the
// device already carries. Cmds is EMPTY when the port already has the intent —
// the entry is still returned, so the verifying re-read confirms it rather than
// this connector assuming it.
type snoopPortWrite struct {
	Num     int
	Port    string
	Trusted bool
	Cmds    []string
}

// snoopPortPlan turns the caller's intents into an ORDERED, DEDUPLICATED list of
// interface stanzas, plus the names this platform cannot translate.
//
// SORTED BY PORT NUMBER and deduplicated on it, so two identical gestures send
// two identical sequences. The caller's slice order is not something a connector
// should be reproducing into a device's configuration, and on this platform the
// same socket has several spellings — "45", "GE45" and " 45 " are one jack — so
// a plan naming it twice would otherwise write it twice and list it twice in
// ApplyResult.PortsWritten. Where two intents disagree the LAST one wins, which
// is the state sending both in the caller's order would have left anyway.
//
// One stanza per port and not "interface 45-52", even though this CLI accepts
// the range: the device writes one stanza per port into its own configuration,
// so that is the captured form — and per port is what lets a refusal on one jack
// land in Skipped for that jack while the rest of the plan proceeds.
func snoopPortPlan(before dhcpSnoop,
	intents []driver.DHCPSnoopPortIntent) ([]snoopPortWrite, []string) {
	var untranslatable []string
	var want = map[int]bool{}
	var named = map[int]string{}
	var nums []int
	var intent driver.DHCPSnoopPortIntent
	for _, intent = range intents {
		var num, ok = portNumber(intent.Port)
		if !ok {
			// AN ORDINARY REFUSAL and not a Go error (cf. driver/dhcpsnoop.go): one
			// unusable name must not cost the rest of the plan.
			untranslatable = append(untranslatable, intent.Port+
				" not modified: this name does not translate to a port number on this platform")
			continue
		}
		var _, dup = named[num]
		if !dup {
			nums = append(nums, num)
		}
		want[num] = intent.Trusted
		named[num] = intent.Port
	}
	sortInts(nums)

	var out []snoopPortWrite
	var num int
	for _, num = range nums {
		var w = snoopPortWrite{Num: num, Port: named[num], Trusted: want[num]}
		var have, known = before.Trusted[num]
		switch {
		case known && have == want[num]:
			// Already stated in the configuration exactly as asked: no write.
		case !known && !want[num]:
			// Untrusted is this platform's default AND its silence: a port the
			// configuration does not mention is already what the plan wants.
		case want[num]:
			w.Cmds = []string{"interface " + strconv.Itoa(num), "dhcp-snooping trust", "exit"}
		default:
			// Per-port untrust, which is a write on ONE port. It is the only "no"
			// form this file emits, and it removes no protection from anything else.
			w.Cmds = []string{"interface " + strconv.Itoa(num), "no dhcp-snooping trust", "exit"}
		}
		out = append(out, w)
	}
	return out, untranslatable
}

// coveredBefore words an empty VLAN list as the fact it is, rather than as a
// blank in the middle of a sentence.
func coveredBefore(v []int) string {
	if len(v) == 0 {
		return "none"
	}
	return formatPortRange(v)
}

// withoutStrings removes `drop` from `list`, preserving order.
func withoutStrings(list, drop []string) []string {
	if len(drop) == 0 {
		return list
	}
	var gone = map[string]bool{}
	var s string
	for _, s = range drop {
		gone[s] = true
	}
	var out []string
	for _, s = range list {
		if !gone[s] {
			out = append(out, s)
		}
	}
	return out
}

// sortStrings keeps a message from reordering itself between two reads of the
// same failure — the same reason recon sorts its port lists.
func sortStrings(v []string) {
	var i, j int
	for i = 1; i < len(v); i++ {
		var x = v[i]
		for j = i; j > 0 && v[j-1] > x; j-- {
			v[j] = v[j-1]
		}
		v[j] = x
	}
}
