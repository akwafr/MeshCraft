package eos

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"meshcraft/internal/driver"
)

// Link aggregation on EOS: Port-Channel interfaces, read from
// "show port-channel dense" and written with the same channel-group forms
// the CLI accepted live.
//
// # The capture that anchors both directions (core01.lab, 2026-08-13)
//
// "show port-channel summary" answers "deprecated by 'show port-channel
// dense'" on this train, and dense reads, with one aggregate present:
//
//	   Port-Channel       Protocol    Ports
//	------------------ -------------- -------
//	   Po999(D)           LACP(a)     Et1(D)
//
// created through this exact sequence and torn down the same way:
//
//	configure ; interface Port-Channel999 ; exit
//	interface Ethernet1 ; channel-group 999 mode active ; end        (build)
//	interface Ethernet1 ; no channel-group ; exit
//	no interface Port-Channel999                                     (teardown)
//
// The VLAN half of an aggregate is the Po interface's own switchport
// configuration — the same vocabulary the port applier writes on Ethernet
// interfaces — read from the running config's Port-Channel section.

// rePoDenseRow matches one aggregate row of the dense table:
// name(flags), protocol(flags), then the member list.
var rePoDenseRow = regexp.MustCompile(
	`(?m)^\s+Po(\d+)\(([^)]*)\)\s+(\S+)\s*(.*)$`)

// rePoMember picks members out of the trailing list: "Et1(D) Et49/1(P)".
var rePoMember = regexp.MustCompile(`([A-Za-z][\w/]*?)\(([^)]*)\)`)

// parsePortChannelDense extracts the aggregates. PURE, replayed on the
// capture. Running is derived from the aggregate's own flag: U (in use) is
// up, D is down — the per-member flags describe members, not the bundle.
func parsePortChannelDense(text string) []driver.LAGState {
	var out []driver.LAGState
	var rows = rePoDenseRow.FindAllStringSubmatch(text, -1)
	var i int
	for i = range rows {
		var n, err = strconv.Atoi(rows[i][1])
		if err != nil {
			continue
		}
		var mode = driver.LAGModeStatic
		if strings.HasPrefix(strings.ToUpper(rows[i][3]), "LACP") {
			mode = driver.LAGModeLACP
		}
		var lag = driver.LAGState{
			Name: driver.StandardLAGName(n), Raw: "Port-Channel" + rows[i][1],
			Mode:    mode,
			Running: strings.Contains(rows[i][2], "U"),
		}
		var members = rePoMember.FindAllStringSubmatch(rows[i][4], -1)
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

// rePoSection captures one Port-Channel interface block of the running
// config; the same switchport vocabulary the Ethernet blocks carry.
var rePoSection = regexp.MustCompile(
	`(?ms)^interface (Port-Channel\d+)\n(.*?)(?:^!|\z)`)

// lagVLANFromRun reads one aggregate's switchport half out of the running
// config.
func lagVLANFromRun(runOut, poName string) PortState {
	var out PortState
	var blocks = rePoSection.FindAllStringSubmatch(runOut, -1)
	var i int
	for i = range blocks {
		if !strings.EqualFold(blocks[i][1], poName) {
			continue
		}
		var body = blocks[i][2]
		// NOT reRunMode: that pattern is unanchored-multiline only where its own
		// caller feeds it lines — inside a section body it needs (?im) of its own.
		var mode = regexp.MustCompile(`(?im)^\s*switchport\s+mode\s+(access|trunk)`).FindStringSubmatch(body)
		if mode != nil {
			if strings.EqualFold(mode[1], "access") {
				out.Mode = "access"
			} else {
				out.Mode = "trunk"
			}
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
			out.Tagged, _ = expandVlanList(allowed[1])
		}
	}
	return out
}

// readLAGs runs the dense table and joins the VLAN half. Called from
// ReadState with the running config it already fetched.
func (c *Client) readLAGs(ctx context.Context, runOut string) ([]driver.LAGState, error) {
	var text, err = c.cli.run(ctx, "show port-channel dense", cmdTimeout)
	if err != nil && text == "" {
		return nil, err
	}
	var lags = parsePortChannelDense(text)
	var i int
	for i = range lags {
		lags[i].VLAN = lagVLANFromRun(runOut, lags[i].Raw)
	}
	return lags, nil
}

// ApplyLAGs writes the desired aggregate list — [driver.LAGWriter], replace
// semantics. Every command form below is the captured one.
func (c *Client) ApplyLAGs(ctx context.Context, desired []driver.LAGState) (ApplyResult, error) {
	var out ApplyResult
	var err = driver.ValidateLAGs(desired)
	if err != nil {
		return out, err
	}
	// LACP fallback is REFUSED, not trimmed. EOS has the keyword
	// (`port-channel lacp fallback individual`), but no write of it has been
	// proven on hardware, so this connector does not declare
	// GestureLAGFallback — and the house rule is that an unproven write is
	// not offered. Writing the aggregate without the fallback would leave the
	// operator believing a member forwards while LACP is down.
	var fb, wantsFB = driver.LAGFallbackWanted(desired)
	if wantsFB {
		return out, driver.RefuseLAGFallback("arista-eos", fb.Name)
	}
	// Fresh read: which aggregates exist, and who their members are, so the
	// teardown half releases exactly the right ports.
	var text string
	if text, err = c.cli.run(ctx, "show port-channel dense", cmdTimeout); err != nil && text == "" {
		return out, fmt.Errorf("aggregates unreadable before writing: %v", err)
	}
	var have = parsePortChannelDense(text)

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
		var poIf = "interface Port-Channel" + strconv.Itoa(n)
		cmds = append(cmds, poIf)
		if want.VLAN.Mode == "trunk" {
			cmds = append(cmds, "switchport mode trunk")
			if len(want.VLAN.Tagged) > 0 {
				cmds = append(cmds, "switchport trunk allowed vlan "+joinInts(want.VLAN.Tagged))
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
		var groupMode = "on"
		if want.Mode == driver.LAGModeLACP {
			groupMode = "active"
		}
		var m string
		for _, m = range want.Members {
			cmds = append(cmds, "interface "+cliPortName(m),
				"channel-group "+strconv.Itoa(n)+" mode "+groupMode, "exit")
		}
		out.PortsWritten = append(out.PortsWritten, want.Name)
	}
	// Aggregates no longer wanted: members released FIRST (the captured
	// teardown order), then the Po interface removed.
	for i = range have {
		if wantNames[have[i].Name] {
			continue
		}
		var n, _ = driver.LAGNumber(have[i].Name)
		var m string
		for _, m = range have[i].Members {
			cmds = append(cmds, "interface "+cliPortName(m), "no channel-group", "exit")
		}
		cmds = append(cmds, "no interface Port-Channel"+strconv.Itoa(n))
		out.PortsWritten = append(out.PortsWritten, have[i].Name)
		out.Notes = append(out.Notes, "aggregate "+have[i].Name+" dissolved: its members "+
			"keep their own interface configuration")
	}
	if len(cmds) == 0 {
		return out, nil
	}
	if err = c.runConfigBlock(ctx, cmds); err != nil {
		return out, err
	}
	return out, nil
}
