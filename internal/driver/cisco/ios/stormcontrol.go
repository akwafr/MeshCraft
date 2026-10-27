package ios

// Per-port storm control — read and write (driver/stormcontrol.go).
//
// # THE UNKNOWN-UNICAST CLASS IS REFUSED ON THIS PLATFORM, and that is the whole
// point of this file
//
// IOS spells its three limiters:
//
//	interface GigabitEthernet0/1
//	 storm-control broadcast level 10.00
//	 storm-control multicast level 10.00
//	 storm-control unicast level 10.00
//	 no storm-control broadcast level
//
// The first two mean what the contract means. THE THIRD DOES NOT. Cisco's
// command reference for this train defines "unicast" as the unicast traffic
// storm control level — unicast, all of it, not the unknown-unicast flooding
// class [driver.StormUnknownUnicast] names. Arista, whose command set this one
// otherwise resembles line for line, spells the flooding class
// "unknown-unicast" and means only that (arista/eos/stormcontrol.go).
//
// Substituting one for the other would install a rate limiter on ESTABLISHED
// FLOWS — a file transfer, a backup, an iSCSI session — on the strength of a
// screen that said "unknown unicast". And it would be invisible: the device
// would report back exactly the configuration this connector wrote, so no
// re-read, no drift comparison and no audit would ever catch it. The only
// mechanism that catches it is refusing at the moment of translation, which is
// what [driver.RefuseStormClass] is for.
//
// The refusal NAMES the keyword and says which way it differs, so an operator
// who does want a unicast limiter knows what to ask for and what it would cost.
// It is an ORDINARY refusal: it lands in [driver.ApplyResult.Skipped] and the
// broadcast and multicast limits in the same plan are still written. Killing the
// gesture over one class would cost the operator two limits they can have.
//
// A "storm-control unicast" statement already on the device is READ and
// reported in [driver.StormControlState.Notes], never as an unknown-unicast
// limit and never rewritten: it is somebody else's configuration and this
// connector neither manages nor removes it.
//
// # THIS IS THE ONE PLATFORM IN THE FLEET THAT SPEAKS ALL THREE UNITS
//
// "level 10", "level pps 10" and "level bps 10" are three configurations
// five orders of magnitude apart, distinguished by one keyword that is easy to
// leave out. That is the concrete case driver/stormcontrol.go's header is
// written around, and it is why the contract carries a unit rather than a
// number. Here all three are accepted and each is written with its OWN keyword;
// nothing is converted, because on the platforms that take only one unit
// conversion is a fiction (frame size, line rate) and a connector that converted
// here would be teaching the fleet a habit the others cannot honour.
//
// # STATUS: CAPTURED AND DECLARED, since 2026-08-08
//
// This header said "DOCUMENTED, NOT CAPTURED, AND NOT DECLARED" until the
// capture arrived, and left it saying so for a while afterwards — which is the
// worse kind of wrong note, because it reads as evidence.
//
// What actually happened: [Client.ApplyStormControl] was exercised on the lab
// CiscoLabFirst1 (WS-C2960X-24TD-L) on 2026-08-08 through
// live_stormcontrol_test.go — apply, apply again for idempotence, remove one
// class, revert, each step confirmed by re-read — and the device's own
// re-spellings of the threshold are recorded verbatim under [parseStormRate]
// below. [Driver.WritesExtra] declares "storm_control" on the strength of
// that run, alongside "stp" and "port_admin".

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

const stormConnector = "cisco-ios"

// stormKeyword maps the classes this platform expresses AS THEMSELVES.
//
// [driver.StormUnknownUnicast] is deliberately absent — cf. the file header. Its
// absence here is what makes the write refuse rather than substitute: there is no
// entry to fall back on.
var stormKeyword = map[driver.StormClass]string{
	driver.StormBroadcast: "broadcast",
	driver.StormMulticast: "multicast",
}

// stormUnits: all three, and this is the only connector in the fleet that can
// say so.
var stormUnits = []driver.StormRateUnit{
	driver.StormRatePercent, driver.StormRatePPS, driver.StormRateBPS,
}

// unknownUnicastRefusal is the sentence handed to an operator who asked for a
// class this platform words more broadly. Written once so the read note, the
// capability answer and the write refusal cannot drift into three different
// accounts of one platform fact.
const unknownUnicastRefusal = "IOS spells its unicast limiter \"storm-control unicast\" " +
	"and its command reference defines it as UNICAST TRAFFIC — all of it, not the " +
	"unknown-unicast flooding class. Installing it here would rate-limit established " +
	"flows (a backup, an iSCSI session) that were never asked about, and the device " +
	"would report back exactly what was written, so no re-read would ever catch the " +
	"substitution. Nothing was written for this class. If a limiter on ALL unicast is " +
	"genuinely wanted, it has to be asked for as that"

// reStormLine matches a storm-control statement inside an interface block.
//
// The unit keyword is OPTIONAL in the grammar and that is exactly the hazard: a
// line with no keyword is a percentage, and one with "pps" is a packet count.
// The capture group distinguishes them; a regex that skipped it would read
// "level pps 1000" as 1000 % of line rate.
var reStormLine = regexp.MustCompile(
	`(?i)^\s*(no\s+)?storm-control\s+(broadcast|multicast|unicast)\s+level` +
		`(?:\s+(pps|bps))?(?:\s+([0-9.]+[kmg]?))?\s*$`)

// reStormIface matches the head of an interface block.
var reStormIface = regexp.MustCompile(`(?i)^\s*interface\s+(\S+)\s*$`)

// parseStormRate reads an IOS threshold argument, including the k/m/g multipliers
// the CLI prints back on pps and bps values.
//
// "1k" is 1 000, not 1. A parser that took the digits and dropped the suffix
// would report a limiter a thousand times tighter than the device carries, and
// the apply would then "correct" the device to that wrong value on the next
// pass — a read defect turning into a write defect, which is the worst shape a
// parser bug takes in this codebase.
//
// CONFIRMED ON HARDWARE 2026-08-08 (lab CiscoLabFirst1, WS-C2960X-24TD-L), and
// the device re-spells more aggressively than the caution above assumed. Sent to
// Gi1/0/1 — an access port with nothing plugged in — then removed:
//
//	level 5.00        → printed "level 5.00"       decimals KEPT
//	level pps 1000    → printed "level pps 1k"
//	level bps 2000000 → printed "level bps 2m"
//	level pps 1500    → printed "level pps 1.5k"   FRACTIONAL suffix
//
// The last line is why this function parses a float before applying the
// multiplier rather than an integer. Anticipating "1k" and still reading the
// digits with Atoi would have worked on three of those four and silently divided
// the fourth by a thousand.
//
// Worth knowing next to arista/eos: that platform NORMALISES "5.00" to "5",
// this one keeps it. Two vendors, two opposite spellings of the same number,
// which is why the idempotence check compares numbers and never text.
func parseStormRate(raw string) (float64, bool) {
	raw = strings.TrimSpace(strings.ToLower(raw))
	if raw == "" {
		return 0, false
	}
	var mult = 1.0
	switch raw[len(raw)-1] {
	case 'k':
		mult, raw = 1e3, raw[:len(raw)-1]
	case 'm':
		mult, raw = 1e6, raw[:len(raw)-1]
	case 'g':
		mult, raw = 1e9, raw[:len(raw)-1]
	}
	var v, err = strconv.ParseFloat(raw, 64)
	if err != nil || v <= 0 {
		return 0, false
	}
	return v * mult, true
}

// StormControlCaps declares what this PLATFORM can express, without a session.
func (d *Driver) StormControlCaps() driver.StormControlCaps {
	return driver.StormControlCaps{
		Classes: []driver.StormClass{driver.StormBroadcast, driver.StormMulticast},
		Units:   stormUnits,
		Why: []string{
			unknownUnicastRefusal + ".",
			"This platform takes a threshold in ANY of the three units — \"level 10\" is " +
				"ten percent, \"level pps 10\" is ten packets a second, \"level bps 10\" is " +
				"ten bits a second. It is the only one in the fleet that does, and it is why " +
				"the contract carries a unit rather than a number.",
		},
	}
}

// --- read -----------------------------------------------------------------

// parseStormControl reads every per-port limiter out of a running configuration.
// PURE.
//
// Returns the port states AND the ports carrying a "storm-control unicast"
// statement, which are reported as a note rather than folded into the
// unknown-unicast class (cf. the file header).
func parseStormControl(cfg string) ([]driver.StormControlPortState, []string) {
	var byPort = map[string]map[driver.StormClass]driver.StormLimit{}
	var foreign = map[string][]driver.StormForeignLimit{}
	var order []string
	var unicastPorts []string
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
		if cur == "" {
			continue
		}
		var m = reStormLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		if strings.EqualFold(m[2], "unicast") {
			// NOT unknown-unicast. Recorded so the operator learns a limiter they
			// did not ask for is in place, and never mapped onto a contract class
			// as a LIMIT — it goes in Foreign instead, which is the contract's own
			// shape for "observed, not modelled" since ADR-0020.
			//
			// Class IS named, and unknown-unicast is the honest one: this
			// statement is broader than that class, not adjacent to it, so it is
			// the control an operator will be looking at when they want to know
			// why flooding is being policed.
			if strings.TrimSpace(m[1]) == "" {
				foreign[cur] = append(foreign[cur], driver.StormForeignLimit{
					Class:    driver.StormUnknownUnicast,
					Spelling: strings.TrimSpace(line),
					Because:  unknownUnicastRefusal + ".",
				})
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
		var unit = driver.StormRatePercent
		switch strings.ToLower(m[3]) {
		case "pps":
			unit = driver.StormRatePPS
		case "bps":
			unit = driver.StormRateBPS
		}
		var rate, ok = parseStormRate(m[4])
		if !ok {
			// The statement is there and its argument is not one this connector
			// reads. "No limiter" would be a claim the device never made, so the
			// class is left UNREAD.
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
		if len(st.Foreign) > 0 {
			unicastPorts = append(unicastPorts, name)
		}
		// ONLY the two classes this platform expresses. The third is not reported
		// as "no limiter": this connector cannot see one, cannot write one, and
		// a false absence on screen is a promise that a limiter could be added.
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
	sort.Slice(out, func(i, j int) bool { return stdPortLess(out[i].Port, out[j].Port) })
	sort.Slice(unicastPorts, func(i, j int) bool { return stdPortLess(unicastPorts[i], unicastPorts[j]) })
	return out, unicastPorts
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
	// The same running configuration says how this chassis spells an interface
	// name, and this read is the one ApplyStormControl performs before writing
	// (cf. Client.learnPortStem).
	c.learnPortStem(runOut)
	var unicastPorts []string
	out.Ports, unicastPorts = parseStormControl(runOut)
	out.Notes = append(out.Notes,
		"read from \"show running-config\". Only BROADCAST and MULTICAST are reported: "+
			"this platform has no unknown-unicast limiter, and reporting that class as "+
			"\"no limiter\" would promise a control that cannot be offered")
	if len(unicastPorts) > 0 {
		out.Notes = append(out.Notes,
			"a \"storm-control unicast\" statement is in place on "+
				strings.Join(unicastPorts, ", ")+". That limits ALL unicast, including "+
				"established flows — it is NOT the unknown-unicast flooding class. meshcraft "+
				"neither wrote it nor removes it; it is reported because it can throttle "+
				"traffic an operator is not looking for an explanation of. The statement is "+
				"also attached verbatim to each of those ports, so a screen can show it "+
				"beside the control it concerns")
	}
	c.step(true, "storm control read",
		fmt.Sprintf("%d port block(s) examined", len(out.Ports)))
	return out, nil
}

// --- write ----------------------------------------------------------------

// stormLevelArg renders the threshold and its unit keyword.
//
// The KEYWORD IS THE CONFIGURATION on this platform: omitting it turns ten
// packets a second into ten percent of line rate. It is produced here, once, from
// the contract's unit, so no call site can forget it.
func stormLevelArg(l driver.StormLimit) string {
	var n = strconv.FormatFloat(l.Rate, 'f', -1, 64)
	switch l.Unit {
	case driver.StormRatePPS:
		return "pps " + n
	case driver.StormRateBPS:
		return "bps " + n
	}
	return n
}

// stormCmds builds the interface block for ONE port. PURE.
func stormCmds(cliName string, limits []driver.StormLimit) []string {
	var out = []string{"interface " + cliName}
	var i int
	for i = range limits {
		var l = limits[i]
		var kw = stormKeyword[l.Class]
		if kw == "" {
			// A class this platform does not express. Reached only if a caller
			// bypassed the refusal above; emitting nothing is the safe answer,
			// because there is no near-equivalent keyword to fall back on.
			continue
		}
		if !l.Enabled {
			out = append(out, "no storm-control "+kw+" level")
			continue
		}
		out = append(out, "storm-control "+kw+" level "+stormLevelArg(l))
	}
	out = append(out, "exit")
	return out
}

// ApplyStormControl writes the per-port limiters the plan names.
//
// PERSISTED: a limiter is a CONFIGURATION a later read compares, unlike the shut
// gesture next door (portadmin.go) which is an action and is deliberately left
// out of the startup configuration.
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
	var verr = plan.Validate()
	if verr != nil {
		return out, verr
	}

	var before, rerr = c.ReadStormControl(ctx)
	if rerr != nil {
		return out, fmt.Errorf("pre-write read failed: %v — nothing was written", rerr)
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
		var cliName = c.portName(it.Port)
		if cliName == "" {
			out.Skipped = append(out.Skipped, fmt.Sprintf(
				"%s not touched: this name is outside the standard-model mapping this "+
					"connector translates", it.Port))
			continue
		}
		var limits = append([]driver.StormLimit{}, it.Limits...)
		driver.SortStormLimits(limits)

		var todo []driver.StormLimit
		var j int
		for j = range limits {
			var l = limits[j]
			if stormKeyword[l.Class] == "" {
				// THE SUBSTITUTION REFUSAL. Not a Go error: the other two classes in
				// this same plan are still written. A platform refusal: no force
				// grows this platform the missing keyword.
				out.NotApplied = append(out.NotApplied, driver.PlatformRefusal(
					it.Port, driver.RefuseStormClass(
						stormConnector, l.Class, it.Port, unknownUnicastRefusal).Error()))
				continue
			}
			var now, read = observed[it.Port].Limit(l.Class)
			if read && stormLimitSame(now, l) {
				continue
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
					"against IOS %s from documentation, survey this device's forms",
				it.Port, firstLine(cerr.Error()), DocumentedRelease))
			continue
		}
		attempted = append(attempted, driver.StormControlPortIntent{Port: it.Port, Limits: todo})
		c.step(true, it.Port+" storm control written", describeStormLimits(todo))
	}

	if len(attempted) == 0 {
		return out, nil
	}

	// The re-read is what says the limiters landed — never the return codes. It
	// earns its round trip twice over here, because the unit keyword is the one
	// thing a mistyped block would lose without any error.
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
			if read && stormLimitSame(got, want) {
				continue
			}
			landed = false
			out.Skipped = append(out.Skipped, fmt.Sprintf(
				"%s: the CLI accepted \"%s\" and the re-read does not show it — on this "+
					"platform the commonest cause is the unit keyword, which turns the same "+
					"digits into three configurations", it.Port, want.Describe()))
		}
		if landed {
			out.PortsWritten = append(out.PortsWritten, it.Port)
		}
	}

	var perr = c.save(ctx)
	if perr != nil {
		out.Notes = append(out.Notes, "applied but NOT persisted: "+firstLine(perr.Error())+
			" — a reload would take these limiters with it")
		c.step(false, "startup-config save failed", firstLine(perr.Error()))
		return out, nil
	}
	c.step(true, "configuration persisted", SaveCmd)
	return out, nil
}

// stormLimitSame answers "is the device already carrying this?".
//
// THE UNIT IS PART OF THE COMPARISON, and leaving it out is the subtle bug this
// helper exists to prevent: "10 pps" and "10 %" have the same Rate, and a
// comparison on the number alone would decide a device configured in percent
// already carried the packet-rate limit that was asked for — and write nothing.
func stormLimitSame(a, b driver.StormLimit) bool {
	if a.Enabled != b.Enabled {
		return false
	}
	if !a.Enabled {
		return true
	}
	return a.Rate == b.Rate && a.Unit == b.Unit
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
