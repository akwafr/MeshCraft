package procurve

// Per-port storm control on the 2600 range — and the honest answer is that TWO
// OF THE THREE CLASSES DO NOT EXIST HERE.
//
// # What this range has
//
// One per-interface statement, and one only:
//
//	interface 5
//	   broadcast-limit 5
//	   exit
//
// A percentage of the port's bandwidth, applied to BROADCAST frames. There is no
// multicast counterpart and no unknown-unicast counterpart on this family: the
// per-class "rate-limit bcast / mcast / unknown-unicast" set that later
// ArubaOS-Switch models carry is not this platform's command language.
//
// # Why that absence is REPORTED rather than approximated
//
// The temptation on a switch like this one is to write `broadcast-limit` when a
// screen asked for a multicast limit, on the grounds that it is the nearest
// thing available. That would be a limiter installed on the WRONG SET OF FRAMES:
// the multicast storm the operator was protecting against would pass untouched,
// and the broadcast traffic they said nothing about would be throttled. The
// device would report `broadcast-limit 5` — exactly what was written — so
// nothing downstream would ever contradict the screen.
//
// So the two missing classes are refused with [driver.RefuseStormClass], per
// port, into [driver.ApplyResult.Skipped]. The broadcast limit in the same plan
// is still written: refusing the whole gesture would cost an operator the one
// limiter this hardware genuinely has.
//
// # The unit is a PERCENTAGE, and pps is refused rather than converted
//
// `broadcast-limit` takes a percentage of port bandwidth. A plan arriving in
// packets per second is refused with the sentence
// [driver.RefuseStormUnit] writes, because converting it needs a frame size that
// nobody chose and that no screen would ever report having chosen
// (driver/stormcontrol.go, § on the unit).
//
// # Where the read comes from
//
// "show running-config", the command this connector has read hundreds of times
// on this platform, and the same source apply.go, snmp.go and dhcpsnoop.go treat
// as authoritative. On a CLI that prints only departures from its defaults, the
// ABSENCE of a `broadcast-limit` line inside a port's stanza is the absence of
// the limiter — that is the argument this whole package is built on.
//
// A short read is REFUSED rather than reported as "no limiters anywhere", the
// same refusal snoopConfig makes and for the same reason: a desynchronised CLI
// read that came back as state would tell an operator their protection was gone.
//
// # STATUS: NOT CAPTURED, AND NOT DECLARED
//
// The 2610-48 survey (SW-PLAYER-79, R.11.121, 2026-08-03) captured this range's
// DHCP snooping vocabulary because the unit was already running the feature. It
// carried NO `broadcast-limit` statement, so nothing here is a captured line —
// the command comes from the family's command reference, not from a switch
// printing it back at us. [Driver.WritesExtra] therefore does not declare
// "storm_control", and TODO.md names the capture that would change that.

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

const stormConnector = "hpe-procurve"

// stormUnits: a percentage of port bandwidth, and nothing else.
var stormUnits = []driver.StormRateUnit{driver.StormRatePercent}

// missingClassReason is the sentence handed to an operator for the two classes
// this range does not carry. Written once so the capability answer and the write
// refusal cannot become two different accounts of one hardware fact.
const missingClassReason = "this range has ONE per-port storm limiter, \"broadcast-limit\", " +
	"and it applies to broadcast frames only. There is no multicast and no unknown-unicast " +
	"counterpart in this family's command language. Writing \"broadcast-limit\" here would " +
	"throttle a class nobody asked about while leaving the one that was asked about " +
	"untouched — and the switch would report exactly what was written, so nothing " +
	"afterwards could contradict the screen. Nothing was written for this class"

// reBcastLimit matches the statement inside a port stanza. The "no" form is
// accepted because a configuration written from a template may carry it.
var reBcastLimit = regexp.MustCompile(`(?i)^\s*(no\s+)?broadcast-limit(?:\s+(\d+))?\s*$`)

// reStormIface matches a port stanza head: "interface 5", "interface 45".
//
// Anchored on a BARE NUMBER because that is all this CLI utters. "interface
// loopback 0" and the VLAN stanzas do not match, and must not: a broadcast
// limit read out of a loopback block would be attributed to a faceplate port.
var reStormIface = regexp.MustCompile(`(?i)^\s*interface\s+(\d+)\s*$`)

// StormControlCaps declares what this PLATFORM can express, without a session.
//
// The Classes list is the honest one: ONE entry. A UI reading it greys out the
// other two controls and shows Why beside them, which is the difference between
// "this switch cannot" and "meshcraft has not proven it yet" —
// [Driver.WritesExtra] answers the second, and on this connector it is still no.
func (d *Driver) StormControlCaps() driver.StormControlCaps {
	return driver.StormControlCaps{
		Classes: []driver.StormClass{driver.StormBroadcast},
		Units:   stormUnits,
		Why: []string{
			missingClassReason + ".",
			"The threshold is a PERCENTAGE of the port's bandwidth. A rate in packets per " +
				"second is refused rather than converted: the conversion needs a frame size " +
				"nobody chose.",
		},
	}
}

// --- read -----------------------------------------------------------------

// parseStormControl reads every broadcast limit out of a running configuration.
// PURE, keyed by port NUMBER so the caller can name them with the faceplate
// prefix it already uses.
//
// A port stanza with no `broadcast-limit` line maps to zero, and the caller turns
// that into an explicit "no limiter" — legitimate on this CLI, which records
// departures from its defaults rather than its defaults (cf. lldp.go, which
// leans on the identical argument for "LLDP is running unless told otherwise").
func parseStormControl(config string) map[int]int {
	var out = map[int]int{}
	var cur = 0
	var line string
	for _, line = range strings.Split(config, "\n") {
		line = strings.TrimRight(line, " \t\r")
		var mi = reStormIface.FindStringSubmatch(line)
		if mi != nil {
			var n, err = strconv.Atoi(mi[1])
			if err == nil && n >= 1 {
				cur = n
				var _, seen = out[cur]
				if !seen {
					out[cur] = 0
				}
			} else {
				cur = 0
			}
			continue
		}
		if strings.TrimSpace(line) == "exit" {
			cur = 0
			continue
		}
		if cur == 0 {
			continue
		}
		var m = reBcastLimit.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		if strings.TrimSpace(m[1]) != "" {
			out[cur] = 0
			continue
		}
		var pct, err = strconv.Atoi(m[2])
		if err != nil || pct <= 0 {
			continue
		}
		out[cur] = pct
	}
	return out
}

// stormConfig reads the running configuration and refuses a short one.
func (c *Client) stormConfig(ctx context.Context) (map[int]int, error) {
	var text, err = c.cli.run(ctx, "show running-config", showRunTimeout)
	if err != nil && strings.TrimSpace(text) == "" {
		return nil, fmt.Errorf("show running-config on %s: %v", c.tgt.Host, firstLine(err.Error()))
	}
	if len(strings.TrimSpace(text)) < minRunningConfig {
		// Identical refusal to snoopConfig's: a short read on this CLI is a
		// desynchronisation, not a switch with no configuration. Reporting it as
		// state would tell an operator their broadcast limiters are gone.
		return nil, fmt.Errorf("running-config read returned %d characters (%q): the CLI "+
			"read desynchronised, and reporting the storm limiters as absent from this "+
			"would be a claim about a device we did not read", len(text), text)
	}
	return parseStormControl(text), nil
}

// ReadStormControl reports the broadcast limiter on every port stanza read.
//
// ONLY BROADCAST IS REPORTED. The other two classes are absent from every port's
// limit list rather than present-and-disabled, because "no multicast limiter"
// on this range is not an observation about the configuration — it is an
// observation about the hardware, and putting it in the same field as a
// configuration fact would let a screen offer to change it.
func (c *Client) ReadStormControl(ctx context.Context) (driver.StormControlState, error) {
	var out driver.StormControlState
	if c.cli == nil {
		return out, errNoSession
	}
	var limits, err = c.stormConfig(ctx)
	if err != nil {
		return out, err
	}
	var nums []int
	var n int
	for n = range limits {
		nums = append(nums, n)
	}
	sortInts(nums)
	for _, n = range nums {
		var l = driver.StormLimit{Class: driver.StormBroadcast}
		if limits[n] > 0 {
			l.Enabled = true
			l.Rate = float64(limits[n])
			l.Unit = driver.StormRatePercent
		}
		out.Ports = append(out.Ports, driver.StormControlPortState{
			Port:   defaultPortPrefix + strconv.Itoa(n),
			Limits: []driver.StormLimit{l},
		})
	}
	out.Notes = append(out.Notes,
		"this range carries ONE per-port storm limiter, \"broadcast-limit\", as a "+
			"percentage of port bandwidth. Multicast and unknown-unicast are absent from "+
			"every port below because they are absent from the hardware, not because they "+
			"read as unconfigured")
	c.step(true, "storm control read",
		fmt.Sprintf("%d port stanza(s) examined", len(out.Ports)))
	return out, nil
}

// --- write ----------------------------------------------------------------

// stormCmds builds the stanza for ONE port. PURE, so the wire form is asserted
// without a device.
//
// The removal is "no broadcast-limit", never "broadcast-limit 0". On this
// family a limit of zero is not "no limit" — it is the floor of the range, and
// writing it in place of a removal would throttle a port to nothing on a gesture
// whose label said "remove the limiter".
func stormCmds(num int, l driver.StormLimit) []string {
	var stanza = "interface " + strconv.Itoa(num)
	if !l.Enabled {
		return []string{stanza, "no broadcast-limit", "exit"}
	}
	return []string{
		stanza,
		"broadcast-limit " + strconv.Itoa(int(l.Rate)),
		"exit",
	}
}

// ApplyStormControl writes the broadcast limiters the plan names, and refuses the
// two classes this range does not carry.
func (c *Client) ApplyStormControl(ctx context.Context,
	plan driver.StormControlPlan) (driver.ApplyResult, error) {

	var out driver.ApplyResult
	if c.cli == nil {
		return out, errNoSession
	}
	var verr = plan.Validate()
	if verr != nil {
		return out, verr
	}

	// A write NEVER acts on a cached state — the rule every applier in this
	// package follows.
	var before, rerr = c.stormConfig(ctx)
	if rerr != nil {
		return out, fmt.Errorf("reading the storm limiters before writing: %v", rerr)
	}

	var intents = make([]driver.StormControlPortIntent, len(plan.Ports))
	copy(intents, plan.Ports)
	var nums []int
	var byNum = map[int]driver.StormControlPortIntent{}
	var it driver.StormControlPortIntent
	for _, it = range intents {
		var num, ok = portNumber(it.Port)
		if !ok {
			// AN ORDINARY REFUSAL: one unusable name must not cost the rest of the
			// plan (cf. snoopPortPlan, same shape).
			out.Skipped = append(out.Skipped, it.Port+
				" not modified: this name does not translate to a port number on this platform")
			continue
		}
		var _, dup = byNum[num]
		if !dup {
			nums = append(nums, num)
		}
		byNum[num] = it
	}
	sortInts(nums)

	var attempted = map[int]driver.StormLimit{}
	var num int
	for _, num = range nums {
		it = byNum[num]
		var limits = append([]driver.StormLimit{}, it.Limits...)
		driver.SortStormLimits(limits)

		var want driver.StormLimit
		var haveWant bool
		var i int
		for i = range limits {
			var l = limits[i]
			if l.Class != driver.StormBroadcast {
				// THE REFUSAL THIS FILE EXISTS FOR. Per port, as a platform refusal,
				// with the reason — and the broadcast limit below is still written.
				out.NotApplied = append(out.NotApplied, driver.PlatformRefusal(
					it.Port, driver.RefuseStormClass(
						stormConnector, l.Class, it.Port, missingClassReason).Error()))
				continue
			}
			if l.Enabled && l.Unit != driver.StormRatePercent {
				out.NotApplied = append(out.NotApplied, driver.PlatformRefusal(
					it.Port, driver.RefuseStormUnit(
						stormConnector, l.Class, it.Port, l.Unit, stormUnits).Error()))
				continue
			}
			if l.Enabled && l.Rate != float64(int(l.Rate)) {
				// This CLI's argument is a whole percentage. Rounding 5.5 to 5 or 6
				// would be the connector choosing a threshold the operator did not
				// type — the same reinterpretation the unit rules forbid, in
				// miniature.
				out.Skipped = append(out.Skipped, fmt.Sprintf(
					"%s: \"broadcast-limit\" takes a WHOLE percentage on this range and the "+
						"plan asks for %g %%. Nothing was rounded and nothing was written",
					it.Port, l.Rate))
				continue
			}
			want, haveWant = l, true
		}
		if !haveWant {
			continue
		}

		var now = before[num]
		if (now > 0) == want.Enabled && (!want.Enabled || float64(now) == want.Rate) {
			// Already there: no round trip, no flash churn (the discipline
			// snoopPortPlan and applyPortSTP share).
			continue
		}
		var cerr = c.runConfigBlock(ctx, stormCmds(num, want))
		if cerr != nil {
			out.Skipped = append(out.Skipped, it.Port+": "+firstLine(cerr.Error()))
			continue
		}
		attempted[num] = want
		out.PortsWritten = append(out.PortsWritten, it.Port)
		c.step(true, it.Port+" storm control written", want.Describe())
	}

	if len(attempted) == 0 {
		if len(out.Skipped) > 0 {
			c.step(false, "storm control NOT written: every change was refused",
				strings.Join(out.Skipped, " ; "))
		} else {
			c.step(true, "storm control already carries the intent", "nothing written")
		}
		return out, nil
	}

	// VERIFY BY RE-READING. On a command this connector has never seen a device
	// print back, a return code proves nothing at all.
	var after, aerr = c.stormConfig(ctx)
	if aerr != nil {
		out.Unconfirmed("storm control", out.PortsWritten, firstLine(aerr.Error()))
		out.PortsWritten = nil
	} else {
		var confirmed []string
		var name string
		for _, name = range out.PortsWritten {
			var n, _ = portNumber(name)
			var want = attempted[n]
			if (after[n] > 0) == want.Enabled && (!want.Enabled || float64(after[n]) == want.Rate) {
				confirmed = append(confirmed, name)
				continue
			}
			out.Skipped = append(out.Skipped, name+
				": \"broadcast-limit\" was accepted and the running configuration does not "+
				"show it — the command form is unverified on this range and this is what "+
				"that costs")
		}
		out.PortsWritten = confirmed
	}

	// "write memory" is what makes it survive a reboot on this range — the same
	// last step every other applier here takes.
	var perr = c.persist(ctx)
	if perr != nil {
		out.Notes = append(out.Notes, "applied but NOT persisted: "+firstLine(perr.Error())+
			" — a reboot would take these limiters with it")
		c.step(false, "\"write memory\" refused", firstLine(perr.Error()))
		return out, nil
	}
	c.step(true, "configuration persisted", "write memory")
	return out, nil
}
