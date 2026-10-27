package s3400

// Per-port storm control — driver.StormControlReader / driver.StormControlApplier.
//
// # THE STATUS OF EVERY COMMAND IN THIS FILE, SAID BEFORE ANYTHING ELSE
//
// EXERCISED ON HARDWARE, 2026-08-08 — lab S3400 at 192.0.2.3, OpCode 2.0.2J,
// on g0/1, reverted, nothing saved. This header said the opposite ("NOT ONE
// of the forms below has been run on an S3400") for the rest of that day,
// which is the failure mode this package's own s3400.go header warns about:
// "a connector's notes are only worth what their evidence is worth". What the
// session settled is recorded at each site it changed — [stormForms],
// [stormClearCmd], [stormVerifiedNote] — and summarised here:
//
//	storm-control broadcast 500            → "Unknown command"
//	storm-control broadcast threshold 500  → accepted
//	no storm-control multicast threshold   → accepted
//	no storm-control multicast             → "Unknown command"
//
// The rate prints back verbatim, with no suffix and no re-spelling.
// [Driver.WritesExtra] DOES declare "storm_control" on the strength of that
// run, and of the applier-level exercise that followed it the same day.
//
// # TWO CANDIDATE SPELLINGS — AND DELIBERATELY NEVER TWO CANDIDATE UNITS
//
// This CLI is the BDCOM lineage (cf. s3400.go: "config" not "conf t",
// "switchport pvid" not "switchport access vlan"). That lineage files storm
// control under an interface-level `storm-control` statement, and the threshold
// argument appears in the documentation in two shapes:
//
//	storm-control broadcast threshold 500    (with the keyword — the one this
//	                                          firmware accepts)
//	storm-control broadcast 500              (bare threshold — refused here)
//
// Both are still tried, THRESHOLD FIRST since the capture, exactly as
// [snoopFamilies] handles the two snooping spellings — a candidate list is how
// this package handles a lineage whose trains have differed before, and an
// unknown command on this CLI configures nothing, prints a caret and a
// sentence, and is caught by reCLIError. The bare form is kept last rather than
// deleted for that reason alone; on THIS firmware it does not exist.
//
// WHAT IS NOT A CANDIDATE IS THE UNIT, and the distinction is the whole safety
// argument of this file. A wrong SPELLING is refused by the device and costs one
// round trip. A wrong UNIT is ACCEPTED by the device and configures a threshold
// the operator never chose — "500" landing as five hundred packets a second or
// as five hundred percent of nothing, with no error either way. So this connector
// commits to ONE unit, packets per second, refuses percent and bits per second
// rather than converting (driver/stormcontrol.go's rule), and the two candidates
// differ only in syntax.
//
// # UNKNOWN UNICAST IS REFUSED, for the reason cisco/ios refuses it
//
// This lineage's third keyword is "unicast", not "unknown-unicast". That is
// the same word Cisco IOS uses for a limiter its command reference defines as ALL
// unicast, and nothing in this platform's documentation narrows it to the
// flooding class.
//
// THE CAPTURE DID NOT CHANGE THIS, and it is worth saying why: 2026-08-08
// established that "storm-control unicast threshold 300" is ACCEPTED by the
// firmware. Acceptance is a fact about the SYNTAX and says nothing about which
// traffic the limiter polices, which is the only question here — and the
// device reports back exactly the line that was written, so no re-read can
// settle it either. Assuming the narrower reading would still be a guess whose
// failure mode is a rate limiter quietly installed on established flows.
//
// So the class is refused with [driver.RefuseStormClass], naming the keyword,
// while broadcast and multicast in the same plan are still written. The S3900
// connector next door DOES write that class, and the difference is not
// inconsistency: that lineage spells the keyword "unknown-unicast" in full.
//
// # Why the read comes out of "show running-config"
//
// No "show storm-control" form has been observed answering on this firmware,
// and inventing one would produce an empty read that looks like "nothing is
// limited" — the same dangerous wrong answer dhcpsnoop.go's header names. "show
// running-config" is the command this connector has read hundreds of times on
// this platform, and on a CLI that prints only non-default lines the presence of
// the statement IS the state.

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"meshcraft/internal/driver"
)

// Compile-time assertions, kept beside the implementation rather than in
// contract.go: a capability that lives in one file should fail to build in that
// same file.
var (
	_ driver.StormControlReader  = (*Client)(nil)
	_ driver.StormControlApplier = (*Client)(nil)
)

const stormConnector = "fs-s3400"

// The two candidate SPELLINGS. Both take the same argument in the same unit —
// cf. the file header on why that is not negotiable.
const (
	// stormFormBare: the threshold straight after the class keyword.
	stormFormBare = "bare"
	// stormFormThreshold: with the "threshold" keyword.
	stormFormThreshold = "threshold"
)

// stormForms is the ORDER the apply tries. Never reordered at runtime: a device
// already configured with one of them wins over this preference (cf.
// ApplyStormControl), the same rule snoopFamilies follows.
//
// THRESHOLD FIRST, and that order is now a fact rather than a guess. Captured on
// the lab S3400 (192.0.2.3, OpCode 2.0.2J, 2026-08-08):
//
//	storm-control broadcast 500            → "Unknown command"
//	storm-control broadcast threshold 500  → accepted
//	storm-control multicast threshold 400  → accepted
//	storm-control unicast threshold 300    → accepted
//
// The bare form is not a second-choice spelling on this firmware, it does not
// exist. It is KEPT in the list, last, because the two-candidate design costs one
// refused round trip when it is wrong and this connector serves a lineage whose
// trains have differed before — but trying it first, as this list did until the
// capture, meant paying that round trip on every port of every apply.
var stormForms = []string{stormFormThreshold, stormFormBare}

// stormKeyword maps the classes this lineage expresses AS THEMSELVES.
// StormUnknownUnicast is deliberately absent — cf. the file header.
var stormKeyword = map[driver.StormClass]string{
	driver.StormBroadcast: "broadcast",
	driver.StormMulticast: "multicast",
}

// stormUnits: packets per second, and one unit only.
var stormUnits = []driver.StormRateUnit{driver.StormRatePPS}

const unknownUnicastRefusal = "this lineage's third storm keyword is \"unicast\", not " +
	"\"unknown-unicast\". That is the same word Cisco IOS uses for a limiter its command " +
	"reference defines as ALL unicast, and nothing in this platform's documentation narrows " +
	"it to the flooding class. The 2026-08-08 lab session established that the firmware " +
	"ACCEPTS \"storm-control unicast threshold N\" — which settles the syntax and not the " +
	"meaning: taking the narrower reading would still be a guess whose failure is a rate " +
	"limiter quietly installed on established flows, and the switch reports back exactly the " +
	"line that was written, so no re-read could catch it. Nothing was written for this class"

// stormVerifiedNote rides every apply, and says what the exercise established
// rather than what it feared.
//
// It replaces a stormUnverifiedNote warning that no S3400 session had ever
// configured the feature. One has (2026-08-08), and it found two defects that
// warning would not have caught — the bare spelling does not exist here, and the
// removal form was wrong — so the note now carries the fact that keeps an
// operator oriented rather than a caveat that is no longer true.
const stormVerifiedNote = "storm-control forms are PROVEN on this platform (lab S3400, " +
	"OpCode 2.0.2J, 2026-08-08): \"storm-control <class> threshold <pps>\" to set, " +
	"\"no storm-control <class> threshold\" to remove. The bare spelling this connector " +
	"once tried first does not exist on this firmware"

// reStormLine matches a storm-control statement inside an interface block, in
// EITHER candidate spelling. The "threshold" keyword is optional in the pattern
// so the read recognises whichever form the device is already carrying.
var reStormLine = regexp.MustCompile(
	`(?i)^\s*(no\s+)?storm-control\s+(broadcast|multicast|unicast)` +
		`(?:\s+(threshold))?(?:\s+(\d+))?\s*$`)

// StormControlCaps declares what this PLATFORM can express, without a session.
func (d *Driver) StormControlCaps() driver.StormControlCaps {
	return driver.StormControlCaps{
		Classes: []driver.StormClass{driver.StormBroadcast, driver.StormMulticast},
		Units:   stormUnits,
		Why: []string{
			unknownUnicastRefusal + ".",
			"The threshold is a PACKET RATE. Two candidate SPELLINGS are tried — with and " +
				"without the \"threshold\" keyword — but never two candidate units: a wrong " +
				"spelling is refused by the device and costs a round trip, while a wrong unit " +
				"is accepted and configures a threshold nobody chose.",
		},
	}
}

// --- command builders (pure, so the wire form is asserted in a test) -------

// stormSetCmd renders one class's statement in the named spelling.
func stormSetCmd(form, keyword string, rate int) string {
	if form == stormFormThreshold {
		return fmt.Sprintf("storm-control %s threshold %d", keyword, rate)
	}
	return fmt.Sprintf("storm-control %s %d", keyword, rate)
}

// stormClearCmd removes a limiter, in the spelling the device speaks.
//
// Never a threshold of zero: on this lineage as on the rest of the fleet, zero is
// a limiter that drops the whole class rather than the absence of one.
//
// THE REMOVAL CARRIES THE "threshold" KEYWORD AND NO RATE, captured on the lab
// S3400 (192.0.2.3, OpCode 2.0.2J) on 2026-08-08:
//
//	no storm-control multicast threshold      → accepted
//	no storm-control multicast                → "Unknown command"
//	no storm-control multicast threshold 400  → "Too many parameters"
//
// This function used to emit the middle one, for every removal, on every port.
// The CLI refuses it loudly and reCLIError catches that, so a removal failed
// visibly rather than silently — but it failed, and the operator's untrust never
// reached the device.
func stormClearCmd(form, keyword string) string {
	if form == stormFormBare {
		return "no storm-control " + keyword
	}
	return "no storm-control " + keyword + " threshold"
}

// stormPortCmds wraps one port's statements in its interface block.
//
// Pure, and the cage numbering arrives as a FACT rather than as a session, so
// the wire form stays assertable under either convention (ADR-0018).
func stormPortCmds(cages cageNumbering, form, standard string,
	limits []driver.StormLimit) ([]string, error) {

	var dev, nerr = cages.cliPortName(standard)
	if nerr != nil {
		return nil, nerr
	}
	var out = []string{"interface " + dev}
	var i int
	for i = range limits {
		var kw = stormKeyword[limits[i].Class]
		if kw == "" {
			continue
		}
		if !limits[i].Enabled {
			out = append(out, stormClearCmd(form, kw))
			continue
		}
		out = append(out, stormSetCmd(form, kw, int(limits[i].Rate)))
	}
	return append(out, "exit"), nil
}

// --- read -----------------------------------------------------------------

// parseStormControl reads the whole running configuration. PURE.
//
// Returns the port states, the ports carrying a "storm-control unicast"
// statement (reported, never mapped onto a contract class), and the SPELLING the
// device is already using — which is what lets an apply speak the device's own
// dialect instead of imposing this file's preference, exactly as
// parseDHCPSnooping returns its family.
func parseStormControl(cfg string) ([]driver.StormControlPortState, []string, string) {
	var byPort = map[string]map[driver.StormClass]driver.StormLimit{}
	var order []string
	var unicastPorts []string
	var form string
	var d = &Driver{}
	var cur string

	var line string
	for _, line = range strings.Split(cfg, "\n") {
		line = strings.TrimRight(line, " \t\r")
		if strings.TrimSpace(line) == "!" {
			cur = ""
			continue
		}
		var mi = reCfgIface.FindStringSubmatch(line)
		if mi != nil {
			cur = ""
			var std, ok = d.StandardPortName(mi[1])
			if ok {
				cur = std
				if byPort[cur] == nil {
					byPort[cur] = map[driver.StormClass]driver.StormLimit{}
					order = append(order, cur)
				}
			}
			continue
		}
		if cur == "" {
			continue
		}
		var m = reStormLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		if strings.EqualFold(m[2], "unicast") {
			// NOT unknown-unicast on this lineage (cf. the file header). Recorded so
			// the operator learns a limiter they did not ask for is in place, and
			// never folded into a contract class.
			if strings.TrimSpace(m[1]) == "" {
				unicastPorts = append(unicastPorts, cur)
			}
			continue
		}
		var class driver.StormClass
		if strings.EqualFold(m[2], "broadcast") {
			class = driver.StormBroadcast
		} else {
			class = driver.StormMulticast
		}
		if strings.TrimSpace(m[1]) != "" {
			byPort[cur][class] = driver.StormLimit{Class: class, Enabled: false}
			continue
		}
		var rate, err = strconv.ParseFloat(m[4], 64)
		if err != nil || rate <= 0 {
			continue
		}
		// The spelling the DEVICE uses wins over this file's preference.
		if strings.TrimSpace(m[3]) != "" {
			form = stormFormThreshold
		} else if form == "" {
			form = stormFormBare
		}
		byPort[cur][class] = driver.StormLimit{
			Class: class, Enabled: true, Rate: rate, Unit: driver.StormRatePPS,
		}
	}

	var out []driver.StormControlPortState
	var name string
	for _, name = range order {
		var st = driver.StormControlPortState{Port: name}
		// ONLY the two classes this lineage expresses. The third is not reported as
		// "no limiter": a false absence on screen promises a control that could be
		// added.
		var class driver.StormClass
		for _, class = range []driver.StormClass{driver.StormBroadcast, driver.StormMulticast} {
			var l, seen = byPort[name][class]
			if !seen {
				l = driver.StormLimit{Class: class, Enabled: false}
			}
			st.Limits = append(st.Limits, l)
		}
		out = append(out, st)
	}
	// GE before XG and numerically inside each: a UI listing fifty-two ports must
	// not get them in map order. The ORDER is driver.SortByPortName's; the SPLIT is
	// this platform's, and stays here (cf. driver/portsort.go).
	driver.SortByPortName(out, stormPortName, splitStandard)
	driver.SortPortNames(unicastPorts, splitStandard)
	return out, unicastPorts, form
}

// stormPortName / stormIntentPort name the field the faceplate order is read
// from, once each, so the sorts below read as sorts rather than as closures.
func stormPortName(p driver.StormControlPortState) string { return p.Port }

func stormIntentPort(it driver.StormControlPortIntent) string { return it.Port }

// readStormControl is the read plus the observed spelling, which the apply needs
// and the contract's signature has no room for.
func (c *Client) readStormControl(ctx context.Context) (driver.StormControlState, string, error) {
	var out, err = c.cli.runPaged(ctx, "show running-config", 90*time.Second)
	if err != nil && strings.TrimSpace(out) == "" {
		return driver.StormControlState{}, "", fmt.Errorf(
			"show running-config on %s: %w", c.host, err)
	}
	// This is ApplyStormControl's one pre-read, so it is where that path's
	// session learns which numbering the cages answer to (ADR-0018): the
	// configuration names them in their "interface TGigaEthernet0/N" stanzas.
	c.noteCageNumbering(out)
	var ports, unicastPorts, form = parseStormControl(out)
	var st = driver.StormControlState{Ports: ports}
	st.Notes = append(st.Notes,
		"read from \"show running-config\" — this firmware serves no surveyed \"show "+
			"storm-control\" of its own, and on a CLI that prints only non-default lines the "+
			"presence of the statement IS the state")
	st.Notes = append(st.Notes,
		"only BROADCAST and MULTICAST are reported: this lineage's third keyword is "+
			"\"unicast\", whose breadth is unestablished here, and reporting an "+
			"unknown-unicast class as \"no limiter\" would promise a control that cannot be "+
			"offered")
	if len(unicastPorts) > 0 {
		st.Notes = append(st.Notes,
			"a \"storm-control unicast\" statement is in place on "+
				strings.Join(unicastPorts, ", ")+". meshcraft neither wrote it nor removes "+
				"it, and does not report it as an unknown-unicast limit — on this lineage the "+
				"keyword's breadth is unestablished, so it may be throttling established "+
				"flows as well")
	}
	return st, form, nil
}

// ReadStormControl implements driver.StormControlReader.
func (c *Client) ReadStormControl(ctx context.Context) (driver.StormControlState, error) {
	var st, _, err = c.readStormControl(ctx)
	return st, err
}

// --- write ----------------------------------------------------------------

// ApplyStormControl implements driver.StormControlApplier.
//
// THE SPELLING IS SETTLED ONCE, on the first port that needs a write, and every
// port after it uses the one the device accepted. Trying both candidates per port
// would double the round trips and — worse — could leave a device carrying two
// spellings of the same feature, which the next read would have to reconcile.
func (c *Client) ApplyStormControl(ctx context.Context,
	plan driver.StormControlPlan) (ApplyResult, error) {

	var res ApplyResult
	var err = c.requireWritable()
	if err != nil {
		return res, err
	}
	var verr = plan.Validate()
	if verr != nil {
		return res, verr
	}

	var before, form, readErr = c.readStormControl(ctx)
	if readErr != nil {
		return res, fmt.Errorf("reading storm control before applying: %w", readErr)
	}
	res.Notes = append(res.Notes, stormVerifiedNote)
	var observed = map[string]driver.StormControlPortState{}
	var i int
	for i = range before.Ports {
		observed[before.Ports[i].Port] = before.Ports[i]
	}

	var intents = make([]driver.StormControlPortIntent, len(plan.Ports))
	copy(intents, plan.Ports)
	driver.SortByPortName(intents, stormIntentPort, splitStandard)

	var attempted []driver.StormControlPortIntent
	var it driver.StormControlPortIntent
	for _, it = range intents {
		var _, _, ok = splitStandard(it.Port)
		if !ok {
			res.Skipped = append(res.Skipped, it.Port+
				": not a socket name this platform utters (GE<n> for the copper ports, "+
				"XG<n> for the SFP+ cages)")
			continue
		}
		var limits = append([]driver.StormLimit{}, it.Limits...)
		driver.SortStormLimits(limits)

		var todo []driver.StormLimit
		var j int
		for j = range limits {
			var l = limits[j]
			if stormKeyword[l.Class] == "" {
				res.NotApplied = append(res.NotApplied, driver.PlatformRefusal(
					it.Port, driver.RefuseStormClass(
						stormConnector, l.Class, it.Port, unknownUnicastRefusal).Error()))
				continue
			}
			// A PERCENTAGE IS CONVERTED — same rule and same reasoning as the
			// S3900 sibling: this platform's field is packets per second, the
			// operator's intent is a share of the link, and the conversion is only
			// honest because the port's line rate is SUPPLIED (from the caller's
			// faceplate) rather than assumed. 64-byte frames, the minimum Ethernet
			// frame and what a storm is made of. See driver.StormPPSFromPercent.
			if l.Enabled && l.Unit == driver.StormRatePercent {
				var pps, ok = driver.StormPPSFromPercent(l.Rate, it.SpeedMbps)
				if !ok {
					res.Skipped = append(res.Skipped, fmt.Sprintf(
						"%s on %s: %g%% could not be converted to packets per second — this "+
							"platform's field is pps and the port's line rate is not known. "+
							"Supply the port speed, or give the rate in pps directly",
						l.Class, it.Port, l.Rate))
					continue
				}
				res.Notes = append(res.Notes, fmt.Sprintf(
					"%s on %s: %g%% of %d Mbit/s converted to %d pps, assuming 64-byte frames",
					l.Class, it.Port, l.Rate, it.SpeedMbps, pps))
				l.Rate = float64(pps)
				l.Unit = driver.StormRatePPS
			}
			if l.Enabled && l.Unit != driver.StormRatePPS {
				res.NotApplied = append(res.NotApplied, driver.PlatformRefusal(
					it.Port, driver.RefuseStormUnit(
						stormConnector, l.Class, it.Port, l.Unit, stormUnits).Error()))
				continue
			}
			var now, read = observed[it.Port].Limit(l.Class)
			if read && now.Enabled == l.Enabled && (!l.Enabled || now.Rate == l.Rate) {
				// Idempotent by observation, the discipline of ApplySNMP and
				// ApplyDHCPSnooping next door: a limiter already in place is not
				// rewritten.
				continue
			}
			todo = append(todo, l)
		}
		if len(todo) == 0 {
			continue
		}

		var accepted, cerr = c.sendStormBlock(ctx, form, it.Port, todo)
		if cerr != nil {
			res.Skipped = append(res.Skipped, fmt.Sprintf("%s: %v", it.Port, cerr))
			continue
		}
		form = accepted
		attempted = append(attempted, driver.StormControlPortIntent{Port: it.Port, Limits: todo})
		c.step(true, it.Port+": storm control written",
			"\""+stormSetCmd(form, "broadcast", 0)+"\" shape")
	}

	if len(attempted) == 0 {
		return res, nil
	}
	res.Notes = append(res.Notes,
		"the spelling this device accepted is the \""+form+"\" form; every port in this "+
			"gesture used it")

	// VERIFY BY RE-READING. On forms nothing has proven, this is not a nicety: a
	// command this CLI accepts and ignores would otherwise be reported as an
	// applied configuration.
	var after, _, aerr = c.readStormControl(ctx)
	if aerr != nil {
		res.Unconfirmed("storm control", driver.StormIntentPorts(attempted),
			firstLine(aerr.Error()))
		return res, nil
	}
	var now = map[string]driver.StormControlPortState{}
	for i = range after.Ports {
		now[after.Ports[i].Port] = after.Ports[i]
	}
	for _, it = range attempted {
		var landed = true
		var j int
		for j = range it.Limits {
			var want = it.Limits[j]
			var got, read = now[it.Port].Limit(want.Class)
			if read && got.Enabled == want.Enabled && (!want.Enabled || got.Rate == want.Rate) {
				continue
			}
			landed = false
			res.Skipped = append(res.Skipped, it.Port+
				": \""+want.Describe()+"\" was accepted and the running configuration does "+
				"not show it — the command form is unverified on this platform and this is "+
				"what that costs")
		}
		if landed {
			res.PortsWritten = append(res.PortsWritten, it.Port)
		}
	}

	if len(res.PortsWritten) > 0 {
		res.Notes = append(res.Notes,
			"applied to the running configuration; \"write\" is what makes it survive a reboot")
	}
	return res, nil
}

// sendStormBlock writes ONE port's limiters, settling the spelling if it is not
// yet known.
//
// Returns the spelling that worked. A candidate is tried ONCE and the next is
// tried only when the CLI refused the first — an unknown command on this CLI
// configures nothing, prints a caret and a sentence, and is caught by reCLIError,
// so the cost of being wrong about the syntax is one round trip.
func (c *Client) sendStormBlock(ctx context.Context, form, port string,
	limits []driver.StormLimit) (string, error) {

	var candidates = stormForms
	if form != "" {
		// The device already speaks one of them: use it and do not probe.
		candidates = []string{form}
	}
	var tried []string
	var candidate string
	for _, candidate = range candidates {
		var cmds, nerr = stormPortCmds(c.cages, candidate, port, limits)
		if nerr != nil {
			// The NAME is what cannot be settled, not the spelling of the
			// statements — so the candidates are not tried at all and nothing
			// is sent (ADR-0018). Probing spellings against an interface name
			// this session cannot establish would blame the vocabulary for a
			// name we would have invented.
			return form, nerr
		}
		tried = append(tried, "\""+candidate+"\" form")
		var cerr = c.configBlock(ctx, cmds)
		if cerr == nil {
			return candidate, nil
		}
	}
	return form, fmt.Errorf("the CLI refused every surveyed storm-control spelling (%s) — "+
		"the vocabulary of this firmware is still unknown", strings.Join(tried, ", "))
}
