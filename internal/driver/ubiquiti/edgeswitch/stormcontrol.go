package edgeswitch

// Per-port storm control — read and write (driver/stormcontrol.go).
//
// # All three classes, and the unknown-unicast mapping rests on a WORDING
//
// This is FASTPATH grammar, and it carries three per-interface limiters:
//
//	interface 0/5
//	storm-control broadcast level 5
//	storm-control multicast level 5
//	storm-control unicast level 5
//	no storm-control broadcast
//
// The third one is the one to be careful with, because the keyword is "unicast"
// — the same keyword Cisco IOS uses for a limiter that covers ALL unicast, and
// which cisco/ios/stormcontrol.go therefore refuses to map onto the contract's
// unknown-unicast class.
//
// This connector DOES map it, and the whole difference is what the two vendors'
// command references say the keyword means. Cisco's defines it as unicast
// traffic. This family's defines "storm-control unicast" as UNKNOWN unicast
// storm recovery — the flooding class — which is the same set of frames
// [driver.StormUnknownUnicast] names.
//
// THAT IS A DOCUMENTARY DISTINCTION, NOT A CAPTURED ONE, and it is stated here
// because it is exactly the kind of thing that ages badly: a firmware whose
// "unicast" limiter turned out to cover established flows would be throttling
// traffic nobody asked about, and the device would report back precisely the line
// that was written, so no re-read could catch it. So the FIRST live exercise of
// this file has one job beyond "does the command take": put a sustained known
// unicast flow through a port with the limiter on, and confirm it is NOT policed.
// TODO.md carries that as the acceptance criterion, not merely "capture the
// form".
//
// # Two units, and the keyword tells them apart
//
//	storm-control broadcast level 5      a PERCENTAGE of line rate
//	storm-control broadcast rate 1000    a PACKET RATE, packets per second
//
// Both are accepted here and each is written with its own keyword. A rate in bits
// per second is refused rather than converted — this family has no bps form, and
// deriving one would need a frame size nobody chose (driver/stormcontrol.go).
//
// # Where the read comes from
//
// "show storm-control all", CAPTURED 2026-08-08 on the lab EP-S16 (1.9.3) —
// testdata/show_storm_control_all.txt. A device that does not answer it is
// reported as [driver.NotSupported] rather than as a switch with no limiters:
// "the feature is absent" and "the read failed" are different sentences and
// only one of them is an instruction to stop looking.
//
// THAT DISTINCTION EARNED ITS KEEP THE DAY THE SHAPE WAS CAPTURED. This parser
// was written expecting one row per (port, class); the device serves one row per
// PORT with all three classes side by side, so the pattern matched nothing and
// the read came back with zero ports. Because a failed parse is reported as
// UNREAD rather than as "no port is limited", nothing downstream ever claimed
// this switch was unprotected on the strength of a parse that had failed — the
// note is what made the mismatch visible instead of silent. The parser was then
// rewritten against the capture (cf. reStormRow).
//
// # STATUS: DEDUCED, NOT CAPTURED, AND NOT DECLARED
//
// [Driver.WritesExtra] declares "poe", "port_power_cycle" and "port_admin".
// It does NOT declare "storm_control": no storm-control line has been sent to
// an EdgeSwitch, no capture in testdata/ contains the string, and the row shape
// the read below parses has never been seen on this hardware. The staging rule
// [driver.StormControlApplier] states is the one applied.

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"meshcraft/internal/driver"
)

// Compile-time: this Admin reads and writes per-port storm control.
var (
	_ driver.StormControlReader  = (*Client)(nil)
	_ driver.StormControlApplier = (*Client)(nil)
)

const stormConnector = "ubiquiti-edgeswitch"

// stormKeyword maps a contract class to this family's keyword.
//
// "unicast" for the flooding class, on the vendor's own wording — cf. the file
// header, and the acceptance criterion it names for the first live run.
var stormKeyword = map[driver.StormClass]string{
	driver.StormBroadcast:      "broadcast",
	driver.StormMulticast:      "multicast",
	driver.StormUnknownUnicast: "unicast",
}

// stormUnits: percent and packets per second. No bps form exists on this family.
var stormUnits = []driver.StormRateUnit{driver.StormRatePercent, driver.StormRatePPS}

const showStormCmd = "show storm-control all"

var (
	// reStormRow matches one row of "show storm-control all".
	//
	// CAPTURED 2026-08-08 on the lab EP-S16 (1.9.3) — testdata/show_storm_control_all.txt.
	// The shape is ONE ROW PER PORT carrying all three classes side by side, not
	// one row per (port, class) as this pattern assumed until then:
	//
	//	       Bcast   Bcast    Bcast      Mcast   Mcast    Mcast      Ucast   Ucast    Ucast      Flow
	//	 Intf  Mode    Level    Action     Mode    Level    Action     Mode    Level    Action     Mode
	//	------ ------- -------- ---------- ------- -------- ---------- ------- -------- ---------- ------
	//	0/1    Disable 5%       None       Disable 5%       None       Disable 5%       None       Disable
	//
	// THE "Mode" COLUMN IS THE ENABLE FLAG AND "Level" IS NOT. Every port on a
	// factory unit prints "Disable 5%": the threshold is a stored default that
	// exists whether or not the limiter runs. Reading the level alone would report
	// the entire switch as policed at 5 %, which is the false-protection reading
	// driver/stormcontrol.go exists to prevent — so a class is only reported
	// enabled when its own Mode column says so.
	//
	// Tolerant on the separator because this CLI's column widths move between
	// trains; anchored on the three (mode, level, action) triples so a row that
	// does not carry all three is not half-read.
	reStormRow = regexp.MustCompile(
		`(?im)^\s*(\d+/\d+)\s+` +
			`(\S+)\s+(\S+)\s+(\S+)\s+` + // broadcast: mode, level, action
			`(\S+)\s+(\S+)\s+(\S+)\s+` + // multicast
			`(\S+)\s+(\S+)\s+(\S+)` + // unknown unicast
			`(?:\s+\S+)?\s*$`) // flow control mode, present on this train
	// reStormLevel splits a "Level" cell into its number and its unit marker.
	// "5%" is a percentage, "1000 pps" (or "1000pps") a packet count, and a
	// bare number is neither until the device says which — cf. the contract's rule
	// that nothing converts.
	reStormLevel = regexp.MustCompile(`(?i)^(\d+(?:\.\d+)?)\s*(%|pps)?$`)
	// reStormAbsent: the shapes this CLI answers with when it does not carry the
	// command at all.
	reStormAbsent = regexp.MustCompile(
		`(?i)invalid input|unrecognized command|incomplete command|command not found|% *error`)
)

// StormControlCaps declares what this PLATFORM can express, without a session.
func (d *Driver) StormControlCaps() driver.StormControlCaps {
	return driver.StormControlCaps{
		// TWO CLASSES, NOT THREE. The hardware has a third limiter and this
		// connector can drive it; what cannot be established is WHICH FRAMES it
		// covers, and Classes is the list of classes the platform expresses AS
		// ITSELF (cf. driver/stormcontrol.go). Declaring unknown-unicast on the
		// strength of the vendor's wording would be the substitution that file
		// forbids — Cisco spells its all-unicast limiter with the same word.
		//
		// It is withheld until a KNOWN unicast flow has been pushed through a
		// limited port and seen NOT to be policed, which needs a traffic generator
		// the lab does not have (2026-08-08). The Why below says so, so a screen
		// can tell "not verified yet" from "this hardware cannot".
		Classes: []driver.StormClass{
			driver.StormBroadcast, driver.StormMulticast,
		},
		Units: stormUnits,
		Why: []string{
			"This family words the flooding limiter \"storm-control unicast\" and its command " +
				"reference defines it as UNKNOWN unicast storm recovery — unlike Cisco IOS, " +
				"whose identically-spelled keyword covers all unicast and is therefore refused " +
				"by that connector. The mapping here rests on the vendor's wording and has not " +
				"been exercised: the first live run must confirm that a known unicast flow " +
				"through a limited port is NOT policed.",
			"The threshold is a PERCENTAGE with \"level\" and a PACKET RATE with \"rate\". " +
				"There is no bits-per-second form on this family, and a rate given in bits " +
				"per second is refused rather than converted.",
		},
	}
}

// --- read -----------------------------------------------------------------

// parseStormControl reads "show storm-control all". PURE.
//
// A row whose limiter is ENABLED but whose threshold column is unreadable leaves
// the class UNREAD rather than reporting it at zero: "every frame of this class
// is dropped" is a claim about a live port that no device made.
func parseStormControl(out string) []driver.StormControlPortState {
	var d = &Driver{}
	// The three class column-groups, in the order the header prints them.
	var columns = []driver.StormClass{
		driver.StormBroadcast, driver.StormMulticast, driver.StormUnknownUnicast,
	}

	var states []driver.StormControlPortState
	var rows = reStormRow.FindAllStringSubmatch(out, -1)
	var i int
	for i = range rows {
		var std, ok = d.StandardPortName(rows[i][1])
		if !ok {
			// The header rule of dashes and the "Intf" line cannot reach here —
			// neither parses as a port — but a LAG or CPU row could, and those are
			// not faceplate ports.
			continue
		}
		var limits []driver.StormLimit
		var col int
		for col = range columns {
			// Group c occupies capture groups 2+3c … 4+3c.
			var mode = rows[i][2+3*col]
			var level = rows[i][3+3*col]
			var l = driver.StormLimit{Class: columns[col]}
			// MODE decides, never the level: a disabled class still prints the
			// threshold it would use (cf. reStormRow).
			l.Enabled = strings.HasPrefix(strings.ToLower(mode), "enable")
			if !l.Enabled {
				limits = append(limits, l)
				continue
			}
			var m = reStormLevel.FindStringSubmatch(level)
			if m == nil {
				// Enabled with a threshold this connector cannot read. Reporting it
				// as "no limiter" would be worse than saying nothing, so the class
				// is left out of the list entirely — absent means NOT READ.
				continue
			}
			var rate, perr = strconv.ParseFloat(m[1], 64)
			if perr != nil || rate <= 0 {
				continue
			}
			l.Rate = rate
			// The cell's own unit marker decides. "pps" is a packet count;
			// "%" and the bare form are a percentage of line rate on this family.
			// Guessing would be the five-orders-of-magnitude mistake the contract
			// exists to stop.
			l.Unit = driver.StormRatePercent
			if strings.EqualFold(m[2], "pps") {
				l.Unit = driver.StormRatePPS
			}
			limits = append(limits, l)
		}
		driver.SortStormLimits(limits)
		states = append(states, driver.StormControlPortState{Port: std, Limits: limits})
	}

	// Faceplate order: driver/portsort.go's, with this platform's own split.
	driver.SortByPortName(states, stormPortName, splitStandard)
	return states
}

// stormPortName / stormIntentPort name the field the faceplate order is read
// from (driver.SortByPortName).
func stormPortName(p driver.StormControlPortState) string { return p.Port }

func stormIntentPort(it driver.StormControlPortIntent) string { return it.Port }

// ReadStormControl reports the per-port limiters this device carries.
//
// A CLI that does not carry the command answers [driver.NotSupported] — a
// capability answer with a reason — rather than an empty state, which would read
// on screen as "nothing is limited on this switch".
func (c *Client) ReadStormControl(ctx context.Context) (driver.StormControlState, error) {
	var out driver.StormControlState
	var text, err = c.cli.run(ctx, showStormCmd, showTimeout)
	if err != nil && strings.TrimSpace(text) == "" {
		return out, fmt.Errorf("%s on %s: %v", showStormCmd, c.host, firstLine(err.Error()))
	}
	if reStormAbsent.MatchString(text) {
		return out, driver.NotSupported(stormConnector, "read per-port storm control",
			"this firmware does not carry \""+showStormCmd+"\". Whether the per-interface "+
				"limiters exist under another spelling on this train is unknown — what is "+
				"certain is that meshcraft cannot read them, which is not the same as their "+
				"being absent")
	}
	out.Ports = parseStormControl(text)
	if len(out.Ports) == 0 {
		out.Notes = append(out.Notes,
			"\""+showStormCmd+"\" answered and no row matched the shape this connector "+
				"parses. The row layout on this train has never been captured, so this is "+
				"reported as UNREAD rather than as \"no port is limited\"")
	}
	out.Notes = append(out.Notes,
		"thresholds are read with the unit the device printed: \"pps\" is a packet count, "+
			"anything else is a percentage of line rate")
	c.step(true, "storm control read", fmt.Sprintf("%d port(s)", len(out.Ports)))
	return out, nil
}

// --- write ----------------------------------------------------------------

// stormCmds builds the interface block for ONE port. PURE.
//
// The removal is "no storm-control <class>" and carries no threshold: on this
// family, as on the rest of the fleet, a level of zero is a limiter that drops
// every frame of the class rather than the absence of one.
func stormCmds(cliName string, limits []driver.StormLimit) []string {
	var out = []string{"interface " + cliName}
	var i int
	for i = range limits {
		var l = limits[i]
		var kw = stormKeyword[l.Class]
		if kw == "" {
			continue
		}
		if !l.Enabled {
			out = append(out, "no storm-control "+kw)
			continue
		}
		var verb = "level"
		if l.Unit == driver.StormRatePPS {
			verb = "rate"
		}
		// TWO STATEMENTS, NOT ONE, and the exercise that established it is why.
		//
		// "storm-control <class> level <n>" sets the THRESHOLD and leaves the
		// limiter switched off. Sent alone on 2026-08-08 it moved the Level column
		// of 0/2 from 5% to 10% while its Mode column stayed "Disable" — the
		// apply's own re-read caught it and reported the write as unconfirmed,
		// which is the contract working, but the port was left carrying a
		// threshold nothing enforced.
		//
		// The bare keyword is the enable. It is sent FIRST so that a block cut in
		// half by a refusal leaves a limiter at its previous threshold rather than
		// a new threshold with no limiter — of the two half-states, the one that
		// still polices something is the safer place to stop.
		out = append(out, "storm-control "+kw)
		out = append(out, "storm-control "+kw+" "+verb+" "+
			strconv.FormatFloat(l.Rate, 'f', -1, 64))
	}
	// Leave the interface, then LEAVE GLOBAL CONFIG.
	//
	// The second exit is not tidiness. Without it the session stays in
	// "(Config)#" after the block, and everything the caller does next is
	// issued in the wrong mode: the following gesture's own "configure" answers
	// "% Invalid input", and so does "write memory confirm", which is
	// privileged EXEC. On 2026-08-08 that cascade turned one refused write into a
	// failed idempotence pass, a failed persist AND a failed revert, none of which
	// had anything wrong with them. dhcpsnoop.go's applier has always backed out;
	// this one did not.
	out = append(out, "exit", "exit")
	return out
}

// ApplyStormControl writes the per-port limiters the plan names.
//
// PERSISTED with "write memory confirm" — the confirm is not optional on this
// CLI (cf. Client.Persist: the plain form asks a question and a session that does
// not answer it hangs). A limiter is a configuration a later read compares, so
// unlike the shut gesture next door it must survive a reboot.
func (c *Client) ApplyStormControl(ctx context.Context,
	plan driver.StormControlPlan) (ApplyResult, error) {

	var res ApplyResult
	var werr = c.requireWritable()
	if werr != nil {
		return res, werr
	}
	var verr = plan.Validate()
	if verr != nil {
		return res, verr
	}

	var before, rerr = c.ReadStormControl(ctx)
	if rerr != nil {
		// A capability refusal rides straight out: there is nothing to write on a
		// firmware whose limiters meshcraft cannot even see.
		return res, rerr
	}
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
				": not a socket name this platform utters, nothing was sent")
			continue
		}
		var limits = append([]driver.StormLimit{}, it.Limits...)
		driver.SortStormLimits(limits)

		var todo []driver.StormLimit
		var j int
		for j = range limits {
			var l = limits[j]
			if l.Enabled && l.Unit == driver.StormRateBPS {
				res.NotApplied = append(res.NotApplied, driver.PlatformRefusal(
					it.Port, driver.RefuseStormUnit(
						stormConnector, l.Class, it.Port, l.Unit, stormUnits).Error()))
				continue
			}
			if l.Enabled && l.Unit == driver.StormRatePPS && l.Rate != float64(int(l.Rate)) {
				res.Skipped = append(res.Skipped, fmt.Sprintf(
					"%s: \"storm-control %s rate\" takes whole packets per second and the plan "+
						"asks for %g. Nothing was rounded and nothing was written",
					it.Port, stormKeyword[l.Class], l.Rate))
				continue
			}
			var now, read = observed[it.Port].Limit(l.Class)
			if read && now.Enabled == l.Enabled &&
				(!l.Enabled || (now.Rate == l.Rate && now.Unit == l.Unit)) {
				continue
			}
			todo = append(todo, l)
		}
		if len(todo) == 0 {
			continue
		}
		var cfgErr = c.runBlock(ctx, append([]string{"configure"},
			stormCmds(cliPortName(it.Port), todo)...))
		if cfgErr != nil {
			res.Skipped = append(res.Skipped, fmt.Sprintf(
				"%s: the CLI refused the storm-control block (%s) — these forms are DEDUCED "+
					"from this CLI family's grammar and have never been captured on this "+
					"hardware, so survey this device's forms",
				it.Port, firstLine(cfgErr.Error())))
			continue
		}
		attempted = append(attempted, driver.StormControlPortIntent{Port: it.Port, Limits: todo})
		c.step(true, it.Port+" storm control written", cliPortName(it.Port))
	}

	if len(attempted) == 0 {
		return res, nil
	}

	// The re-read is what says the limiters landed. This CLI has already been
	// caught executing a command perfectly well while the session read it as
	// refused (ssh.go's rePrompt); the inverse — a return code that changes
	// nothing — is exactly what this catches.
	var after, aerr = c.ReadStormControl(ctx)
	if aerr != nil {
		// The same rule ApplyPoE next door already follows, and for the same
		// reason it gives (verifyPoEClasses): what a failed re-read produces is a
		// list of accepted commands, not a list of configured ports.
		res.Unconfirmed("storm control", driver.StormIntentPorts(attempted),
			firstLine(aerr.Error()))
	} else {
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
				if read && got.Enabled == want.Enabled &&
					(!want.Enabled || (got.Rate == want.Rate && got.Unit == want.Unit)) {
					continue
				}
				landed = false
				res.Skipped = append(res.Skipped, fmt.Sprintf(
					"%s: the CLI accepted \"%s\" and the re-read does not show it",
					it.Port, want.Describe()))
			}
			if landed {
				res.PortsWritten = append(res.PortsWritten, it.Port)
			}
		}
	}

	var perr = c.Persist(ctx)
	if perr != nil {
		res.Notes = append(res.Notes, "applied but NOT persisted: "+firstLine(perr.Error())+
			" — a reboot would take these limiters with it, and the next read would report "+
			"them as drift")
		return res, nil
	}
	return res, nil
}
