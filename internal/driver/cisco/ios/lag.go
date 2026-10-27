package ios

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"meshcraft/internal/driver"
)

// Link aggregation on IOS: EtherChannel, read from "show etherchannel
// summary" and written with the channel-group forms typed live.
//
// # The capture that anchors both directions (C2960X, 2026-08-13)
//
//	Group  Port-channel  Protocol    Ports
//	------+-------------+-----------+------------------------------------------
//	5      Po5(SD)         LACP      Gi1/0/5(D)
//
// built with "interface Gi1/0/5 ; channel-group 5 mode active" (the Po
// interface springs into existence with the first member), torn down with
// "no channel-group" + "no interface Port-channel5", table back to zero.
// The account this connector runs under lands in privileged EXEC directly,
// so the summary needs no elevation.

// reEtherChannelRow matches one group row: number, Po-name(flags),
// protocol, member list.
var reEtherChannelRow = regexp.MustCompile(
	`(?m)^(\d+)\s+Po(\d+)\(([^)]*)\)\s+(\S+)\s*(.*)$`)

// reEcMember picks "Gi1/0/5(D)" out of the member list.
var reEcMember = regexp.MustCompile(`([A-Za-z][\w/]*?)\(([^)]*)\)`)

// parseEtherChannelSummary extracts the aggregates. PURE, replayed on the
// capture. IOS flags: S/R layer, U in use, D down — running is U.
func parseEtherChannelSummary(text string) []driver.LAGState {
	var out []driver.LAGState
	var rows = reEtherChannelRow.FindAllStringSubmatch(text, -1)
	var i int
	for i = range rows {
		var n, err = strconv.Atoi(rows[i][2])
		if err != nil {
			continue
		}
		var mode = driver.LAGModeStatic
		if strings.EqualFold(rows[i][4], "LACP") {
			mode = driver.LAGModeLACP
		}
		var lag = driver.LAGState{
			Name: driver.StandardLAGName(n), Raw: "Port-channel" + rows[i][2],
			Mode:    mode,
			Running: strings.Contains(rows[i][3], "U"),
		}
		var members = reEcMember.FindAllStringSubmatch(rows[i][5], -1)
		var m int
		for m = range members {
			var std, ok = (&Driver{}).StandardPortName(members[m][1])
			if !ok {
				continue
			}
			lag.Members = append(lag.Members, std)
		}
		out = append(out, lag)
	}
	driver.SortLAGs(out)
	return out
}

// rePoSectionIOS captures a Port-channel interface block of the running
// config (IOS spells it "interface Port-channelN", lowercase c).
var rePoSectionIOS = regexp.MustCompile(
	`(?ms)^interface (Port-channel\d+)\r?\n(.*?)(?:^!|\z)`)

// lagVLANFromRunIOS reads one aggregate's switchport half.
func lagVLANFromRunIOS(runOut, poName string) PortState {
	var out PortState
	var blocks = rePoSectionIOS.FindAllStringSubmatch(runOut, -1)
	var i int
	for i = range blocks {
		if !strings.EqualFold(blocks[i][1], poName) {
			continue
		}
		var body = blocks[i][2]
		if regexp.MustCompile(`(?im)^\s*switchport mode access`).MatchString(body) {
			out.Mode = "access"
		} else if regexp.MustCompile(`(?im)^\s*switchport mode trunk`).MatchString(body) {
			out.Mode = "trunk"
		}
		var access = regexp.MustCompile(`(?im)^\s*switchport access vlan (\d+)`).FindStringSubmatch(body)
		if access != nil && out.Mode == "access" {
			out.PVID, _ = strconv.Atoi(access[1])
		}
		var native = regexp.MustCompile(`(?im)^\s*switchport trunk native vlan (\d+)`).FindStringSubmatch(body)
		if native != nil {
			out.PVID, _ = strconv.Atoi(native[1])
		}
		var allowed = regexp.MustCompile(`(?im)^\s*switchport trunk allowed vlan ([\d,\-]+)`).FindStringSubmatch(body)
		if allowed != nil {
			out.Tagged = expandIOSVlanList(allowed[1])
		}
	}
	return out
}

// expandIOSVlanList expands "10,42,98-100" — the allowed-vlan spelling.
func expandIOSVlanList(spec string) []int {
	var out []int
	var part string
	for _, part = range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		var lo, hi, found = strings.Cut(part, "-")
		var a, errA = strconv.Atoi(lo)
		if errA != nil {
			continue
		}
		if !found {
			out = append(out, a)
			continue
		}
		var b, errB = strconv.Atoi(hi)
		if errB != nil || b < a {
			continue
		}
		var v int
		for v = a; v <= b; v++ {
			out = append(out, v)
		}
	}
	return out
}

// readLAGs runs the summary and joins the VLAN half from the running config
// the caller already fetched.
func (c *Client) readLAGs(ctx context.Context, runOut string) ([]driver.LAGState, error) {
	var text, err = c.cli.run(ctx, "show etherchannel summary", cmdTimeout)
	if err != nil && text == "" {
		return nil, err
	}
	var lags = parseEtherChannelSummary(text)
	var i int
	for i = range lags {
		lags[i].VLAN = lagVLANFromRunIOS(runOut, lags[i].Raw)
	}
	return lags, nil
}

// ApplyLAGs writes the desired list — [driver.LAGWriter], replace semantics,
// every form the captured one.
func (c *Client) ApplyLAGs(ctx context.Context, desired []driver.LAGState) (ApplyResult, error) {
	var out ApplyResult
	var err = driver.ValidateLAGs(desired)
	if err != nil {
		return out, err
	}
	// LACP fallback is REFUSED, not trimmed: this connector does not declare
	// GestureLAGFallback (no write proven on hardware), and an aggregate
	// written without the fallback it was asked for is a lie the screen would
	// repeat.
	var fb, wantsFB = driver.LAGFallbackWanted(desired)
	if wantsFB {
		return out, driver.RefuseLAGFallback("cisco-ios", fb.Name)
	}
	var text string
	if text, err = c.cli.run(ctx, "show etherchannel summary", cmdTimeout); err != nil && text == "" {
		return out, fmt.Errorf("aggregates unreadable before writing: %v", err)
	}
	var have = parseEtherChannelSummary(text)

	var wantNames = map[string]bool{}
	var cmds = []string{}
	var i int
	for i = range desired {
		var want = desired[i]
		wantNames[want.Name] = true
		var n, ok = driver.LAGNumber(want.Name)
		if !ok {
			return out, fmt.Errorf("aggregate %q: not a writable standard name", want.Name)
		}
		var groupMode = "on"
		if want.Mode == driver.LAGModeLACP {
			groupMode = "active"
		}
		// Members first: on IOS the Po interface springs into existence with
		// the first channel-group (captured), and switchport config applied to
		// the Po propagates to members.
		var m string
		for _, m = range want.Members {
			var ifName = cliPortName(m)
			if ifName == "" {
				return out, fmt.Errorf("aggregate %s: member %q is outside the "+
					"naming scheme", want.Name, m)
			}
			cmds = append(cmds, "interface "+ifName,
				"channel-group "+strconv.Itoa(n)+" mode "+groupMode, "exit")
		}
		cmds = append(cmds, "interface Port-channel"+strconv.Itoa(n))
		if want.VLAN.Mode == "trunk" {
			// The encapsulation quirk is per RANGE (a 3560 requires the line, a
			// 2960X refuses it); the port applier owns that fallback dance. Here
			// the plain form is sent — the 2960X form — and a platform that
			// requires encapsulation answers with an error the operator reads.
			cmds = append(cmds, "switchport mode trunk")
			if len(want.VLAN.Tagged) > 0 {
				cmds = append(cmds, "switchport trunk allowed vlan "+joinIntsComma(want.VLAN.Tagged))
			}
			if want.VLAN.PVID > 0 {
				cmds = append(cmds, "switchport trunk native vlan "+strconv.Itoa(want.VLAN.PVID))
			}
		} else if want.VLAN.Mode == "access" {
			var pvid = want.VLAN.PVID
			if pvid == 0 {
				pvid = 1
			}
			cmds = append(cmds, "switchport mode access",
				"switchport access vlan "+strconv.Itoa(pvid))
		}
		cmds = append(cmds, "exit")
		out.PortsWritten = append(out.PortsWritten, want.Name)
	}
	for i = range have {
		if wantNames[have[i].Name] {
			continue
		}
		var n, _ = driver.LAGNumber(have[i].Name)
		var m string
		for _, m = range have[i].Members {
			var ifName = cliPortName(m)
			if ifName == "" {
				continue
			}
			cmds = append(cmds, "interface "+ifName, "no channel-group", "exit")
		}
		cmds = append(cmds, "no interface Port-channel"+strconv.Itoa(n))
		out.PortsWritten = append(out.PortsWritten, have[i].Name)
		// NOT "members keep their own configuration" — the opposite was
		// PROVEN on the lab C2960X (2026-08-13): switchport config written on
		// the Po PROPAGATES to bundled members and SURVIVES unbundling, so a
		// freed member carries the aggregate's trunk lines in place of its own
		// intent (Gi1/0/5 came back trunk/99 instead of access/100, and the
		// site read "drift" until a port apply repaired it). The note tells
		// the operator the exact repair.
		out.Notes = append(out.Notes, "aggregate "+have[i].Name+" dissolved: on IOS "+
			"its members RETAIN the aggregate's switchport configuration — re-apply "+
			"the device configuration (or the ports' intents) to restore them")
	}
	if len(cmds) == 0 {
		return out, nil
	}
	if err = c.runConfigBlock(ctx, cmds); err != nil {
		return out, err
	}
	return out, nil
}

// joinIntsComma renders a sorted VLAN list the way allowed-vlan wants it.
func joinIntsComma(list []int) string {
	var parts = make([]string, 0, len(list))
	var v int
	for _, v = range list {
		parts = append(parts, strconv.Itoa(v))
	}
	return strings.Join(parts, ",")
}
