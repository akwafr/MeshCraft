package s3900

// Per-port storm control — read and write (driver/stormcontrol.go).
//
// # This lineage names all three classes, and that part is unambiguous
//
// The S3900 CLI is the Accton/Edgecore lineage (cf. parse.go's "Style hybride
// Accton" on the VLAN forms), and that family files storm control under
// `switchport`, one statement per class:
//
//	interface ethernet 1/11
//	 switchport broadcast packet-rate 500
//	 switchport multicast packet-rate 500
//	 switchport unknown-unicast packet-rate 500
//	 no switchport broadcast packet-rate
//
// The third keyword is "unknown-unicast", spelled out — the flooding class,
// not the "unicast" that cisco/ios refuses to substitute for it. So unlike
// Cisco and unlike the S3400 next door, no class has to be refused here.
//
// # AND THEN THERE IS THE UNIT, WHICH IS THE OPEN QUESTION OF THIS FILE
//
// The keyword is "packet-rate". Across the trains of this lineage the vendor's
// own documentation has attached TWO DIFFERENT UNITS to that one word:
//
//   - packets per second, on some releases;
//   - kilobits per second, on others — the same keyword, a different quantity.
//
// At 64-byte frames those readings differ by a factor of about eight thousand.
// "switchport broadcast packet-rate 500" is either a limit that lets 500 frames
// a second through, or one that lets roughly four megabits a second through, and
// nothing in the command itself distinguishes them.
//
// This connector therefore accepts ONE unit — packets per second — and refuses
// percent and bits per second rather than converting into a keyword whose meaning
// it cannot pin (driver/stormcontrol.go's rule, applied to a case where even the
// destination unit is uncertain). AND IT SAYS SO, in
// [driver.ApplyResult.Notes] and [driver.StormControlState.Notes], on every read
// and every write. A loud note is not a substitute for knowing; it is what makes
// the not-knowing visible on the screen where the number was typed.
//
// THE FIRMWARE CAVEAT HAS ALREADY CASHED IN ONCE, so read it as a fact rather
// than a formality: this package's command set was surveyed on OpCode 1.7.0, the
// lab unit has since moved to FSOS 1.7.7 (192.0.2.71, probed 2026-08-08 — cf.
// dhcpsnoop.go), and one captured form changed across that step ("media
// ethernet" on VLAN creation went from required to rejected; cf. vlanCreateCmds).
// So whatever settles the unit must be settled on the firmware it will be written
// from, and re-checked after the next upgrade.
//
// AND NO CLI READING CAN SETTLE IT AT ALL, which is why this is not merely an
// uncaptured form. The switch prints back the number it was given whichever
// quantity it means, so a capture of "switchport broadcast packet-rate 500"
// being accepted and read back would look exactly like success and prove nothing.
// The experiment that closes it is small and specific, and TODO.md carries it:
// set a limit, put a KNOWN LOAD through the port, and see which of the two
// quantities the switch is actually counting. The lab has no traffic generator
// (confirmed 2026-08-08), so it cannot be done from here.
//
// # Where the read comes from
//
// "show run", the command this connector reads for everything else. On a CLI
// that prints only non-default lines, the absence of a `switchport … packet-rate`
// statement inside an interface block is the absence of the limiter — the same
// argument dhcpsnoop.go and portadmin.go lean on here.
//
// # STATUS: DOCUMENTED, NOT CAPTURED, AND NOT DECLARED
//
// [Driver.WritesExtra] declares "port_admin" — earned on the lab unit. It does
// NOT declare "storm_control": not one of these statements has been sent to an
// S3900, no capture in testdata/ contains "packet-rate", and the unit above is
// unsettled. Two independent reasons, either of which alone would be enough.

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

const stormConnector = "fs-s3900"

// stormKeyword maps a contract class to this lineage's keyword. All three are
// named as themselves here.
var stormKeyword = map[driver.StormClass]string{
	driver.StormBroadcast:      "broadcast",
	driver.StormMulticast:      "multicast",
	driver.StormUnknownUnicast: "unknown-unicast",
}

// stormUnits: packets per second, and the file header explains why that is a
// choice made under uncertainty rather than a fact.
var stormUnits = []driver.StormRateUnit{driver.StormRatePPS}

const packetRateCaveat = "THE UNIT OF \"packet-rate\" IS NOT SETTLED ON THIS FIRMWARE: across " +
	"the trains of this lineage the vendor's documentation has attached that one keyword to " +
	"packets per second on some releases and to kilobits per second on others — quantities " +
	"about eight thousand times apart at 64-byte frames. meshcraft sends the number as " +
	"PACKETS PER SECOND and converts nothing. Before relying on a limit written here, set " +
	"one from the CLI, put a known load through the port, and see which quantity the switch " +
	"is counting"

var (
	// reStormLine matches a storm-control statement inside an interface block.
	reStormLine = regexp.MustCompile(
		`(?i)^\s*(no\s+)?switchport\s+(broadcast|multicast|unknown-unicast)\s+packet-rate` +
			`(?:\s+(\d+))?\s*$`)
	// reStormIface matches an ethernet interface block head. VLAN and craft
	// blocks are deliberately excluded — a limiter read out of "interface vlan
	// 99" would be attributed to a faceplate port that does not carry it.
	reStormIface = regexp.MustCompile(`(?i)^\s*interface\s+(ethernet\s+\d+/\d+)\s*$`)
)

// StormControlCaps declares what this PLATFORM can express, without a session.
func (d *Driver) StormControlCaps() driver.StormControlCaps {
	return driver.StormControlCaps{
		Classes: []driver.StormClass{
			driver.StormBroadcast, driver.StormMulticast, driver.StormUnknownUnicast,
		},
		Units: stormUnits,
		Why: []string{
			"This lineage spells the flooding class \"unknown-unicast\", so all three " +
				"classes are named as themselves and none has to be approximated.",
			packetRateCaveat + ".",
		},
	}
}

// --- read -----------------------------------------------------------------

// parseStormControl reads every per-port limiter out of "show run". PURE.
//
// A port block with no statement carries no limiter, and that IS an observation
// on this CLI: it prints only non-default lines, so silence inside a block that
// was read is the absence of the setting.
func parseStormControl(cfg string) []driver.StormControlPortState {
	var byPort = map[string]map[driver.StormClass]driver.StormLimit{}
	var order []string
	var d = &Driver{}
	var cur string

	var line string
	for _, line = range strings.Split(cfg, "\n") {
		line = strings.TrimRight(line, " \t\r")
		if strings.TrimSpace(line) == "!" {
			cur = ""
			continue
		}
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
		var class, known = stormClassOf(m[2])
		if !known {
			continue
		}
		if strings.TrimSpace(m[1]) != "" {
			byPort[cur][class] = driver.StormLimit{Class: class, Enabled: false}
			continue
		}
		var rate, err = strconv.ParseFloat(m[3], 64)
		if err != nil || rate <= 0 {
			continue
		}
		byPort[cur][class] = driver.StormLimit{
			Class: class, Enabled: true, Rate: rate, Unit: driver.StormRatePPS,
		}
	}

	var out []driver.StormControlPortState
	var name string
	for _, name = range order {
		var st = driver.StormControlPortState{Port: name}
		var class driver.StormClass
		for _, class = range driver.StormClasses {
			var l, seen = byPort[name][class]
			if !seen {
				l = driver.StormLimit{Class: class, Enabled: false}
			}
			st.Limits = append(st.Limits, l)
		}
		out = append(out, st)
	}
	driver.SortByPortName(out, stormPortName, stormSplitPort)
	return out
}

// stormClassOf maps this lineage's keyword back to a contract class.
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

// The faceplate order, drawn the way the panel draws it: numerically, so GE10
// does not land between GE1 and GE2. The ordering itself lives in
// driver/portsort.go; what is local here is the SPLIT.
//
// AND THIS PLATFORM'S SPLIT NAMES NO KIND, which is not an oversight. The S3900
// this connector models has one row of sockets and standardPortName can only
// produce GE<n> (parse.go), so there is no second kind for a name to be grouped
// away from — and stormPortIndex reads its index off the trailing digit run
// without looking at the prefix at all, which is the behaviour that was here
// before the comparator was shared and is preserved exactly by returning the same
// kind for every name it accepts.
func stormPortName(p driver.StormControlPortState) string { return p.Port }

func stormIntentPort(it driver.StormControlPortIntent) string { return it.Port }

// stormSplitPort adapts stormPortIndex to driver.PortNameSplit.
func stormSplitPort(name string) (int, string, bool) {
	var n = stormPortIndex(name)
	if n < 0 {
		return 0, "", false
	}
	return n, "", true
}

func stormPortIndex(name string) int {
	var i = len(name)
	for i > 0 && name[i-1] >= '0' && name[i-1] <= '9' {
		i--
	}
	if i == len(name) {
		return -1
	}
	var n, err = strconv.Atoi(name[i:])
	if err != nil {
		return -1
	}
	return n
}

// ReadStormControl reports the per-port limiters this device carries.
func (c *Client) ReadStormControl(ctx context.Context) (driver.StormControlState, error) {
	var out driver.StormControlState
	if c.cli == nil {
		return out, fmt.Errorf("no open session on this access")
	}
	var runOut, err = c.cli.run(ctx, "show run", showRunTimeout)
	if err != nil && strings.TrimSpace(runOut) == "" {
		return out, fmt.Errorf("show run on %s: %v", c.host, firstLine(err.Error()))
	}
	out.Ports = parseStormControl(runOut)
	out.Notes = append(out.Notes,
		"read from \"show run\": on a CLI that prints only non-default lines, the absence "+
			"of a \"switchport … packet-rate\" statement inside a port's block IS the absence "+
			"of the limiter")
	out.Notes = append(out.Notes, packetRateCaveat)
	c.step(true, "storm control read",
		fmt.Sprintf("%d port block(s) examined", len(out.Ports)))
	return out, nil
}

// --- write ----------------------------------------------------------------

// stormCmds builds the interface block for ONE port. PURE, so the wire form is
// asserted in a test rather than discovered on a customer's switch.
//
// The removal is "no switchport <class> packet-rate" and carries no number: a
// packet rate of zero on this lineage is a limiter that drops the whole class,
// not the absence of one.
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
			out = append(out, "no switchport "+kw+" packet-rate")
			continue
		}
		out = append(out, "switchport "+kw+" packet-rate "+strconv.Itoa(int(l.Rate)))
	}
	out = append(out, "exit")
	return out
}

// ApplyStormControl writes the per-port limiters the plan names.
//
// PERSISTED with "copy running-config startup-config": a limiter is a
// configuration a later read compares, so unlike the shut gesture in portadmin.go
// it has to survive a reboot.
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
	out.Notes = append(out.Notes, packetRateCaveat)

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
	driver.SortByPortName(intents, stormIntentPort, stormSplitPort)

	var attempted []driver.StormControlPortIntent
	var it driver.StormControlPortIntent
	for _, it = range intents {
		var cliName = cliPortName(it.Port)
		if cliName == "" {
			out.Skipped = append(out.Skipped, it.Port+
				": not a socket name this platform utters (GE<n>), nothing was sent")
			continue
		}
		var limits = append([]driver.StormLimit{}, it.Limits...)
		driver.SortStormLimits(limits)

		var todo []driver.StormLimit
		var j int
		for j = range limits {
			var l = limits[j]
			// A PERCENTAGE IS CONVERTED, not refused — this platform's field is
			// packets per second and the operator's intent is a share of the link.
			//
			// The conversion is only honest because the missing term is supplied:
			// the port's own line rate comes from the caller's faceplate
			// (StormControlPortIntent.SpeedMbps) and the frame size is fixed at 64
			// bytes, which is both the minimum Ethernet frame and what a broadcast
			// storm is actually made of. See driver.StormPPSFromPercent for the
			// arithmetic and for what the assumption costs.
			//
			// An UNKNOWN speed still refuses. Assuming one would put the guess back
			// exactly where this design removed it, and a 10 G uplink computed as a
			// gigabit gets a limiter ten times looser than asked for.
			if l.Enabled && l.Unit == driver.StormRatePercent {
				var pps, ok = driver.StormPPSFromPercent(l.Rate, it.SpeedMbps)
				if !ok {
					out.Skipped = append(out.Skipped, fmt.Sprintf(
						"%s on %s: %g%% could not be converted to packets per second — this "+
							"platform's field is pps and the port's line rate is not known "+
							"(faceplate speed missing). Supply the port speed, or give the rate "+
							"in pps directly", l.Class, it.Port, l.Rate))
					continue
				}
				out.Notes = append(out.Notes, fmt.Sprintf(
					"%s on %s: %g%% of %d Mbit/s converted to %d pps, assuming 64-byte frames "+
						"(the minimum Ethernet frame, and what a broadcast storm is made of). "+
						"Larger frames at that packet rate carry more bandwidth, so the limiter "+
						"is looser in bit terms than the percentage — never tighter",
					l.Class, it.Port, l.Rate, it.SpeedMbps, pps))
				l.Rate = float64(pps)
				l.Unit = driver.StormRatePPS
			}
			if l.Enabled && l.Unit != driver.StormRatePPS {
				out.NotApplied = append(out.NotApplied, driver.PlatformRefusal(
					it.Port, driver.RefuseStormUnit(
						stormConnector, l.Class, it.Port, l.Unit, stormUnits).Error()))
				continue
			}
			var now, read = observed[it.Port].Limit(l.Class)
			if read && now.Enabled == l.Enabled && (!l.Enabled || now.Rate == l.Rate) {
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
				"%s: the CLI refused the storm-control block (%s) — these forms come from "+
					"this lineage's command reference and have never been sent to an S3900, "+
					"so survey this device's own vocabulary",
				it.Port, firstLine(cerr.Error())))
			continue
		}
		attempted = append(attempted, driver.StormControlPortIntent{Port: it.Port, Limits: todo})
		c.step(true, it.Port+" storm control written", cliName)
	}

	if len(attempted) == 0 {
		return out, nil
	}

	// VERIFY BY RE-READING. On forms nothing has proven, a command this CLI
	// accepts and ignores would otherwise be reported as an applied configuration.
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
			if read && got.Enabled == want.Enabled && (!want.Enabled || got.Rate == want.Rate) {
				continue
			}
			landed = false
			out.Skipped = append(out.Skipped, fmt.Sprintf(
				"%s: the CLI accepted \"%s\" and \"show run\" does not show it — the command "+
					"form is unverified on this platform and this is what that costs",
				it.Port, want.Describe()))
		}
		if landed {
			out.PortsWritten = append(out.PortsWritten, it.Port)
		}
	}

	var perr = c.save(ctx)
	if perr != nil {
		out.Notes = append(out.Notes, "applied but NOT persisted: "+firstLine(perr.Error())+
			" — a reboot would take these limiters with it")
		c.step(false, "startup-config save failed", firstLine(perr.Error()))
		return out, nil
	}
	c.step(true, "configuration persisted", "copy running-config startup-config")
	return out, nil
}
