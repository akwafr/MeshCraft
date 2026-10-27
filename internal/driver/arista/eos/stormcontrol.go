package eos

// Per-port storm control — read and write (driver/stormcontrol.go).
//
// # THIS IS THE ONE PLATFORM IN THE FLEET THAT NAMES ALL THREE CLASSES AS
// THEMSELVES
//
// EOS spells the three keywords the contract asks for, and means by each of them
// exactly the set of frames the contract means:
//
//	interface Ethernet1
//	   storm-control broadcast level 5
//	   storm-control multicast level 5
//	   storm-control unknown-unicast level 5
//	   no storm-control broadcast
//
// "unknown-unicast" is the keyword that matters. Cisco IOS, which this command
// set otherwise resembles line for line, spells its unicast limiter
// "storm-control unicast" and defines it as ALL unicast — so the connector next
// door refuses that class rather than substitute it (cisco/ios/stormcontrol.go).
// Here nothing has to be refused, and the reason is a keyword rather than a
// judgement call.
//
// # THE UNIT: percent is written, and MORE THAN PERCENT IS READ
//
// EOS's bare `level` argument is a percentage of the port's line rate, and that
// is the only form this connector WRITES. [Driver.StormControlCaps] says so, and
// it is a decision rather than an omission: the `pps` and `rate` variants are
// platform-dependent within the range — they exist on some forwarding hardware
// and not on others, the same way DHCP snooping does here (dhcpsnoop.go's second
// section) — and a connector that declared them would put a pps box on a screen
// for a switch whose CLI might reject the line. The refusal an operator gets
// instead ([driver.RefuseStormUnit]) names what this connector does take, which
// is actionable; a rejected command halfway through a config block is not.
//
// THE READER IS NOT BOUND BY THAT. A read-only survey of core01-arena on
// 2026-10-25 asked the device for its own grammar — `show cli commands
// all-modes`, which needs no configuration mode — and it answers, for every
// class including a fourth one:
//
//	storm-control { all | broadcast | multicast | unknown-unicast } \
//	    level ( LEVEL | ( pps PPS ) | ( rate RATE RATE_UNIT ) )
//
// So a port in the field can carry a form this connector will not write, and
// before ADR-0020 the parser SKIPPED those lines and then reported the class as
// carrying no limiter. The reader now covers what it can state honestly —
// `level <n>` as percent, `level pps <n>` as packets per second — and reports
// the rest verbatim (cf. reStormAny). Reading a unit is not claiming to write
// it; driver/stormcontrol.go's StormControlCaps.Units says which question each
// surface answers.
//
// NOT claimed on either side: `level rate <n> <unit>`. `show cli commands` does
// not expand its RATE_UNIT token, and the one way to learn the vocabulary is to
// configure one — which on a production core is not a price this answer is
// worth.
//
// NOTHING IS EVER CONVERTED. A plan arriving in packets per second is refused,
// not divided by a frame size nobody chose. driver/stormcontrol.go's header
// argues that at length and this file simply obeys it.
//
// # The re-read is not a nicety here
//
// A percentage is a number the CLI will accept in a great many shapes, and the
// one thing that distinguishes "the device took 5" from "the device took 5 and
// stored 5.00 on a different class" is reading the configuration back. The apply
// therefore re-reads and reports, per class, what the switch actually carries.
//
// # STATUS: PROVEN LIVE AND DECLARED (2026-08-08)
//
// [Driver.WritesExtra] answers true for "storm_control", and this is the first
// range in the fleet where any storm-control command has reached a device at all.
//
// The exercise, on the lab core01.lab (DCS-7050SX-64-R, EOS 4.28.13.1M),
// through [Client.ApplyStormControl] rather than a shell — so the applier's own
// build-and-verify path is what was tested, not the CLI's willingness to accept a
// line somebody typed:
//
//   - all three classes written to Ethernet1 and read back at the rates asked
//     for, 0.5 % included;
//   - a SECOND identical apply wrote nothing. That is the convergence the rate
//     spelling threatened — EOS answers "5" to a rate sent as "5.00", so a
//     connector comparing text would have rewritten three lines on every pass
//     for ever, and this is the run that proves it does not;
//   - a removal through the same applier took broadcast away and LEFT the other
//     two, which is the contract's rule that a class absent from a plan is not
//     managed;
//   - the port was left carrying nothing.
//
// Ethernet1 has no transceiver fitted, which is a stronger safety property than
// "the port is down": a limiter polices traffic crossing the port it sits on, and
// nothing crosses an empty cage — no cable can appear and change that mid-run.
//
// The harness is stormcontrol_live_test.go, gated on EOS_LIVE_WRITE=1, and it
// refuses to start if any port is already policing something, so its revert can
// never remove a limiter it did not install.

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"meshcraft/internal/driver"
)

// Compile-time: this Admin reads and writes per-port storm control.
var (
	_ driver.StormControlReader  = (*Client)(nil)
	_ driver.StormControlApplier = (*Client)(nil)
)

// connectorName is what the capability refusals sign themselves with. An
// operator comparing two switches needs to know WHICH connector said no.
const stormConnector = "arista-eos"

// stormKeyword maps a contract class to this platform's own keyword.
//
// A map rather than a switch so the compiler cannot be satisfied by a default
// branch: a class added to the contract and not to this table produces an empty
// keyword, which the write refuses, rather than a command line missing a word.
var stormKeyword = map[driver.StormClass]string{
	driver.StormBroadcast:      "broadcast",
	driver.StormMulticast:      "multicast",
	driver.StormUnknownUnicast: "unknown-unicast",
}

// stormUnits is what this platform's `level` argument takes. One entry, and the
// file header says why the other two are not claimed.
var stormUnits = []driver.StormRateUnit{driver.StormRatePercent}

// reStormAny matches ANY per-interface storm-control statement, understood or
// not. IT IS TESTED FIRST, AND THAT ORDER IS THE WHOLE SAFETY PROPERTY OF
// ADR-0020.
//
// What it exists to stop: this file used to carry only the regex below, which
// is anchored on `\s*$` right after an optional numeric level. The device's own
// grammar — read off core01-arena on 2026-10-25 with `show cli commands
// all-modes` — says the CLI also takes
//
//	storm-control broadcast level pps 100
//	storm-control broadcast level rate 10 <unit>
//	storm-control all level 5
//
// and none of the three matched. The line was therefore SKIPPED, and
// [parseStormControl] then applied "silence inside a block that was read IS
// the absence of a limiter" and reported the class as carrying nothing. A port
// policed at 100 packets a second came back as unprotected — the exact
// silent-wrong-config this feature exists to prevent, on the read side where
// nobody had looked for it.
//
// Anything this matches and [reStormLine] does not becomes a
// [driver.StormForeignLimit], verbatim. That makes the property independent of
// the list of forms known today: an EOS spelling this survey did not see
// announces itself instead of disappearing.
var reStormAny = regexp.MustCompile(`(?i)^\s*(?:no\s+|default\s+)?storm-control\s+\S`)

// reStormNegated matches a storm-control statement that asserts an ABSENCE.
//
// Such a line can never hide a limiter, so it is skipped rather than reported as
// unreadable — and it must be, because `show running-config all` prints "no
// storm-control all" on every interface of the chassis. Classing those sixty
// lines as foreign would put every port of every EOS device into doubt on the
// strength of a default.
var reStormNegated = regexp.MustCompile(`(?i)^\s*(?:no|default)\s+storm-control\s`)

// reStormLine matches the statements this connector UNDERSTANDS.
//
// The rate is captured as TEXT and parsed separately: EOS prints "5" where it
// was given "5.00", and a regex that only accepted one of the two spellings
// would report a limiter as absent on a device that carries it — which the apply
// would then "fix" by rewriting a line already in place, on every pass.
//
// The optional `pps` keyword is READ even though [stormUnits] does not offer to
// WRITE it. The two surfaces answer different questions (cf.
// driver.StormControlCaps.Units): refusing to read a form the CLI accepts would
// leave a real limiter to be reported as foreign at best and absent at worst,
// and "100 packets/s" is simply the truth about that port.
//
// `level rate <n> <unit>` is deliberately NOT matched. The rate is plain, but
// `show cli commands` does not expand the RATE_UNIT token and the only way to
// learn its vocabulary is to write one — so the unit is unknown, and a rate
// without its unit is the one thing driver/stormcontrol.go refuses to report.
// It goes to Foreign.
//
// CONFIRMED ON HARDWARE 2026-08-08 (lab core01.lab, DCS-7050SX-64-R, EOS
// 4.28.13.1M), and it is slightly better than the rule above assumed. Sent, on a
// port with no transceiver, then reverted:
//
//	storm-control broadcast level 5.00        → reads back "level 5"
//	storm-control multicast level 0.5         → reads back "level 0.5"
//	storm-control unknown-unicast level 10    → reads back "level 10"
//
// So EOS does not simply truncate to an integer: it normalises to the SHORTEST
// EXACT representation, dropping trailing zeros while preserving a real
// fraction. Both halves matter here. The first is why a string comparison would
// rewrite "5.00" forever; the second is why rounding to an integer on read
// would silently turn a 0.5 % limiter — a legitimate setting on a ten-gigabit
// port, cf. driver/stormcontrol.go on StormRatePercent — into 0 % or 1 %.
//
// Comparing NUMBERS, which the idempotence check does, is correct against both.
var reStormLine = regexp.MustCompile(
	`(?i)^\s*(no\s+)?storm-control\s+(broadcast|multicast|unknown-unicast)` +
		`(?:\s+level\s+(?:(pps)\s+)?([0-9.]+))?\s*$`)

// reStormIface matches the head of an interface block.
var reStormIface = regexp.MustCompile(`(?i)^\s*interface\s+(\S+)\s*$`)

// stormClassOf maps this platform's keyword back to a contract class.
func stormClassOf(keyword string) (driver.StormClass, bool) {
	var c driver.StormClass
	var k string
	for c, k = range stormKeyword {
		if strings.EqualFold(k, keyword) {
			return c, true
		}
	}
	return "", false
}

// StormControlCaps declares what this PLATFORM can express, without a session.
//
// Read next to [Driver.WritesExtra]: this says "the hardware has it", that says
// "meshcraft has proven the form". A screen showing one without the other greys
// a control out for the wrong reason (cf. driver.StormControlCaps).
func (d *Driver) StormControlCaps() driver.StormControlCaps {
	return driver.StormControlCaps{
		Classes: []driver.StormClass{
			driver.StormBroadcast, driver.StormMulticast, driver.StormUnknownUnicast,
		},
		Units: stormUnits,
		Why: []string{
			"EOS names all three classes as themselves — \"storm-control unknown-unicast\" " +
				"is the flooding class, not all unicast — so nothing has to be approximated here.",
			"The threshold is a PERCENTAGE of the port's line rate. The pps and kbps " +
				"variants exist on some forwarding hardware in this range and not on others, " +
				"so this connector does not claim them: a rate in packets per second is " +
				"refused with that sentence rather than converted.",
		},
	}
}

// --- read -----------------------------------------------------------------

// stormForeignAll is the sentence beside EOS's fourth class.
const stormForeignAll = "EOS carries a FOURTH per-interface class, \"storm-control all\", " +
	"which polices every frame on the port — unicast that reached its destination " +
	"included. It is not the three classes above taken together, so meshcraft reports " +
	"it rather than model it (ADR-0020)."

// stormForeignRate is the sentence beside the "level rate" form.
const stormForeignRate = "This limiter is set with EOS's \"level rate <n> <unit>\" form. " +
	"The device's own grammar does not spell out which units that form takes, and the " +
	"only way to find out is to write one — so meshcraft shows the device's line as it " +
	"stands rather than report a threshold whose unit it would be guessing."

// stormForeignUnknown is the sentence for a storm-control statement in a shape
// this connector has never seen.
const stormForeignUnknown = "This is a \"storm-control\" statement in a shape this " +
	"connector does not read. It is shown verbatim because the alternative — skipping " +
	"the line — would report the port as carrying no limiter at all."

// parseStormControl reads every per-port limiter out of a running configuration.
// PURE, so the shape is asserted without a device.
//
// # The silence rule, and the exact extent of it
//
// A port with an interface block and NO storm-control line carries no limiter,
// and this parser says so explicitly — three disabled limits — rather than
// leaving the port's class list empty. That is legitimate here and is NOT the
// "absent means unread" case the contract warns about: on a CLI that prints
// only non-default lines, the silence inside a block that WAS read is itself the
// observation. A port with no block at all is a different matter and is simply
// absent from the result.
//
// THE RULE HOLDS ONLY FOR A BLOCK EVERY STATEMENT OF WHICH WAS UNDERSTOOD
// (ADR-0020). Two suspensions, and they are not the same size:
//
//   - a statement naming ONE class in a form this connector cannot read
//     suspends the rule for THAT CLASS alone. The other two were genuinely
//     silent and their silence still means absence;
//   - a statement naming NO class of this contract — "storm-control all" —
//     suspends it for the WHOLE PORT. Every class is then left unread, because
//     "all" is policing broadcast, multicast and unknown unicast on that port
//     whatever the per-class statements say, and three lines reading "no
//     limiter configured" beside it would be three lies.
func parseStormControl(cfg string) []driver.StormControlPortState {
	var byPort = map[string]map[driver.StormClass]driver.StormLimit{}
	var foreign = map[string][]driver.StormForeignLimit{}
	var order []string
	var d = &Driver{}
	var cur string

	var line string
	for _, line = range strings.Split(cfg, "\n") {
		line = strings.TrimRight(line, " \t\r")
		var mi = reStormIface.FindStringSubmatch(line)
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
		// An UNINDENTED line closes the block. EOS indents everything inside an
		// interface block, and without this the global "storm-control bum
		// aggregate traffic-class N" statements — which the device does carry,
		// and which belong to no port — would be attributed to whichever
		// interface happened to be read last.
		if line != "" && !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			cur = ""
			continue
		}
		if cur == "" || !reStormAny.MatchString(line) {
			continue
		}
		var m = reStormLine.FindStringSubmatch(line)
		if m == nil {
			if reStormNegated.MatchString(line) {
				// An absence this connector does not need to understand further.
				continue
			}
			// Matched reStormAny and not this: a statement that IS a limiter and
			// is not one this connector can report as a rate. Verbatim, never
			// dropped (cf. reStormAny).
			foreign[cur] = append(foreign[cur], stormForeign(strings.TrimSpace(line)))
			continue
		}
		var class, _ = stormClassOf(m[2])
		if strings.TrimSpace(m[1]) != "" {
			// An explicit "no storm-control …" line. EOS does not normally print
			// one — the absence of the statement is the disabled state — but a
			// device configured through a template may carry it, and reading it as
			// a limiter would invert the flag.
			byPort[cur][class] = driver.StormLimit{Class: class, Enabled: false}
			continue
		}
		var unit = driver.StormRatePercent
		if strings.EqualFold(m[3], "pps") {
			unit = driver.StormRatePPS
		}
		var rate, err = strconv.ParseFloat(m[4], 64)
		if err != nil || rate <= 0 {
			// The statement is there and its argument is not a threshold this
			// connector recognises. Reporting "no limiter" would be a claim the
			// device did not make, so it is reported as it stands.
			foreign[cur] = append(foreign[cur], stormForeign(strings.TrimSpace(line)))
			continue
		}
		byPort[cur][class] = driver.StormLimit{
			Class: class, Enabled: true, Rate: rate, Unit: unit,
		}
	}

	var out []driver.StormControlPortState
	var name string
	for _, name = range order {
		var st = driver.StormControlPortState{Port: name, Foreign: foreign[name]}
		// A classless foreign statement suspends the silence rule for the whole
		// port (cf. the doc comment).
		var wholePort bool
		var i int
		for i = range st.Foreign {
			if st.Foreign[i].Class == "" {
				wholePort = true
			}
		}
		var class driver.StormClass
		for _, class = range driver.StormClasses {
			var l, seen = byPort[name][class]
			if seen {
				st.Limits = append(st.Limits, l)
				continue
			}
			var _, blind = st.ForeignFor(class)
			if wholePort || blind {
				// Not read, and deliberately absent from Limits: the contract's
				// own spelling for "I did not establish this".
				continue
			}
			// Silence inside a block that WAS read: no limiter.
			st.Limits = append(st.Limits, driver.StormLimit{Class: class, Enabled: false})
		}
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool { return stdPortLess(out[i].Port, out[j].Port) })
	return out
}

// stormForeign classifies one unreadable statement and attaches the sentence an
// operator reads beside a control that will not edit it.
//
// Negated statements never arrive here (cf. reStormNegated), so the prefix tests
// have one shape to consider rather than three.
func stormForeign(spelling string) driver.StormForeignLimit {
	var low = strings.ToLower(spelling)
	var out = driver.StormForeignLimit{Spelling: spelling, Because: stormForeignUnknown}
	// The class, when the statement names one this contract carries. LEFT EMPTY
	// for "all" — that emptiness is what tells the caller the whole port is in
	// doubt, so it is a result and not a gap.
	var c driver.StormClass
	for _, c = range driver.StormClasses {
		if strings.HasPrefix(low, "storm-control "+stormKeyword[c]+" ") {
			out.Class = c
			break
		}
	}
	switch {
	case strings.HasPrefix(low, "storm-control all"):
		out.Because = stormForeignAll
	case strings.Contains(low, " level rate "):
		out.Because = stormForeignRate
	}
	return out
}

// ReadStormControl reports the per-port limiters this device carries.
func (c *Client) ReadStormControl(ctx context.Context) (driver.StormControlState, error) {
	var out driver.StormControlState
	if c.cli == nil {
		return out, fmt.Errorf("no open session on this access")
	}
	var runOut, err = c.cli.run(ctx, "show running-config", showRunTimeout)
	if err != nil && strings.TrimSpace(runOut) == "" {
		return out, fmt.Errorf("show running-config on %s: %v", c.host, err)
	}
	out.Ports = parseStormControl(runOut)
	out.Notes = append(out.Notes,
		"read from \"show running-config\": on a CLI that prints only non-default lines, "+
			"the absence of a \"storm-control\" statement inside a port's block IS the "+
			"absence of a limiter — but only in a block whose every statement was "+
			"understood. A \"level\" with no unit keyword is a PERCENTAGE of the port's "+
			"line rate; \"level pps\" is read as packets per second even though this "+
			"connector does not offer to write that form")
	var nf int
	var i int
	for i = range out.Ports {
		nf += len(out.Ports[i].Foreign)
	}
	if nf > 0 {
		out.Notes = append(out.Notes,
			"some ports carry a \"storm-control\" statement this connector does not read as "+
				"a rate — EOS's fourth class \"all\", or its \"level rate <n> <unit>\" form. "+
				"Each is shown verbatim against its port: the classes it touches are reported "+
				"as NOT READ rather than as unprotected")
	}
	c.step(true, "storm control read",
		fmt.Sprintf("%d port block(s) examined", len(out.Ports)))
	return out, nil
}

// --- write ----------------------------------------------------------------

// stormCmds builds the interface block for ONE port. PURE, so the wire form is
// asserted in a test rather than discovered on a customer's switch.
//
// The order inside the block is [driver.StormClasses]', fixed: two identical
// plans must produce two identical transcripts, and on a command set nothing has
// captured yet the transcript is how the form gets learned.
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
		// The rate is rendered with the shortest exact spelling — "5" rather than
		// "5.000000". EOS accepts both; the transcript is read by a human.
		out = append(out, "storm-control "+kw+" level "+
			strconv.FormatFloat(l.Rate, 'f', -1, 64))
	}
	out = append(out, "exit")
	return out
}

// ApplyStormControl writes the per-port limiters the plan names.
//
// PERSISTED, unlike [Client.ApplyPortAdmin]. The asymmetry is the contract's: a
// shut port is an ACTION whose whole value is being transient, while a storm
// limiter is a CONFIGURATION a later read compares — one that survived a reboot
// only in the running configuration would come back as drift on every pass.
func (c *Client) ApplyStormControl(ctx context.Context,
	plan driver.StormControlPlan) (ApplyResult, error) {

	var out = ApplyResult{VLANsCreated: []int{}, PortsWritten: []string{}}
	if c.cli == nil {
		return out, fmt.Errorf("no open session on this access")
	}
	var werr = c.requireWritableDialect()
	if werr != nil {
		return out, werr
	}
	// The plan is judged BEFORE the device is reached. A rate with no unit or a
	// class named twice is a caller defect, and spending a session to discover it
	// on a range that grants few of them helps nobody.
	var verr = plan.Validate()
	if verr != nil {
		return out, verr
	}

	var before, rerr = c.ReadStormControl(ctx)
	if rerr != nil {
		return out, fmt.Errorf("pre-write read failed: %v — nothing was written, because "+
			"this connector cannot tell an already-correct limiter from an absent one "+
			"without it", rerr)
	}
	var observed = map[string]driver.StormControlPortState{}
	var i int
	for i = range before.Ports {
		observed[before.Ports[i].Port] = before.Ports[i]
	}

	var intents = make([]driver.StormControlPortIntent, len(plan.Ports))
	copy(intents, plan.Ports)
	sort.Slice(intents, func(a, b int) bool { return stdPortLess(intents[a].Port, intents[b].Port) })

	var attempted []driver.StormControlPortIntent
	var it driver.StormControlPortIntent
	for _, it = range intents {
		var cliName = cliPortName(it.Port)
		if cliName == "" {
			out.Skipped = append(out.Skipped, fmt.Sprintf(
				"%s not touched: this name is outside the standard-model mapping this "+
					"connector translates (a breakout lane, or the out-of-band management port)",
				it.Port))
			continue
		}
		// "storm-control all" polices the three classes whatever the per-class
		// statements say, so a port carrying one is a port where the limit the
		// operator just set is not the only thing acting on these frames. The
		// write is not refused — nothing here conflicts — but the transcript must
		// not let the result be read as "broadcast is now limited at 5 %" full
		// stop.
		var fp driver.StormForeignLimit
		var k int
		for k = range observed[it.Port].Foreign {
			fp = observed[it.Port].Foreign[k]
			if fp.Class == "" {
				out.Notes = append(out.Notes, fmt.Sprintf(
					"%s also carries \"%s\", which polices every frame on the port and which "+
						"meshcraft neither reads as a rate nor edits — the limits written below "+
						"act alongside it", it.Port, fp.Spelling))
			}
		}
		var limits = append([]driver.StormLimit{}, it.Limits...)
		driver.SortStormLimits(limits)

		var todo []driver.StormLimit
		var j int
		for j = range limits {
			var l = limits[j]
			if l.Enabled && !l.Unit.Valid() {
				continue // Validate above already refused the plan; unreachable.
			}
			if l.Enabled && l.Unit != driver.StormRatePercent {
				out.NotApplied = append(out.NotApplied, driver.PlatformRefusal(
					it.Port, driver.RefuseStormUnit(
						stormConnector, l.Class, it.Port, l.Unit, stormUnits).Error()))
				continue
			}
			var now, read = observed[it.Port].Limit(l.Class)
			if read && now.Enabled == l.Enabled &&
				(!l.Enabled || (now.Rate == l.Rate && now.Unit == l.Unit)) {
				// Already there: nothing is sent, and the port does not count as
				// written (contract).
				//
				// THE UNIT IS PART OF THE COMPARISON. Since the reader learned to
				// report "level pps 100" as packets per second, a port carrying
				// 5 pps and a plan asking for 5 percent agree on the digits and
				// on nothing else — and the convergence check would have declared
				// a limiter 148,000 times too loose already in place.
				continue
			}
			// A class whose only observation is a statement this connector cannot
			// read is NOT a class carrying no limiter. The write goes ahead —
			// the operator named the class — but it says what it replaced, which
			// is the whole of the contract's rule here.
			var fl, isForeign = observed[it.Port].ForeignFor(l.Class)
			if isForeign {
				out.Notes = append(out.Notes, fmt.Sprintf(
					"%s carried \"%s\", which this connector does not read as a rate; "+
						"writing %s replaces it", it.Port, fl.Spelling, l.Describe()))
			}
			todo = append(todo, l)
		}
		if len(todo) == 0 {
			continue
		}
		var cerr = c.runConfigBlock(ctx, stormCmds(cliName, todo))
		if cerr != nil {
			out.Skipped = append(out.Skipped, fmt.Sprintf(
				"%s: the CLI refused the storm-control block (%s) — command set written "+
					"against EOS %s from documentation, survey this device's forms",
				it.Port, firstLine(cerr.Error()), DocumentedRelease))
			continue
		}
		attempted = append(attempted, driver.StormControlPortIntent{Port: it.Port, Limits: todo})
		c.step(true, it.Port+" storm control written", describeStormLimits(todo))
	}

	if len(attempted) == 0 {
		return out, nil
	}

	// THE RE-READ IS WHAT SAYS THE LIMITERS LANDED — never the return codes. A
	// command block that scrolled past without an error and configured nothing is
	// the failure this costs one round trip to catch.
	var after, aerr = c.ReadStormControl(ctx)
	if aerr != nil {
		out.Unconfirmed("storm control", driver.StormIntentPorts(attempted),
			firstLine(aerr.Error()))
		return out, nil
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
			if read && got.Enabled == want.Enabled &&
				(!want.Enabled || (got.Rate == want.Rate && got.Unit == want.Unit)) {
				continue
			}
			landed = false
			out.Skipped = append(out.Skipped, fmt.Sprintf(
				"%s: the CLI accepted \"%s\" and the re-read does not show it — a return "+
					"code that changes nothing is what this re-read exists to catch",
				it.Port, want.Describe()))
		}
		if landed {
			out.PortsWritten = append(out.PortsWritten, it.Port)
		}
	}

	var perr = c.save(ctx)
	if perr != nil {
		out.Notes = append(out.Notes, "applied but NOT persisted: "+firstLine(perr.Error())+
			" — a reload would take these limiters with it, and the next read would report "+
			"them as drift")
		c.step(false, "startup-config save failed", firstLine(perr.Error()))
		return out, nil
	}
	c.step(true, "configuration persisted", SaveCmd)
	return out, nil
}

// describeStormLimits renders a limit list for a transcript step.
func describeStormLimits(limits []driver.StormLimit) string {
	var parts []string
	var i int
	for i = range limits {
		parts = append(parts, limits[i].Describe())
	}
	return strings.Join(parts, ", ")
}
