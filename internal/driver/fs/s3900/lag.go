package s3900

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"meshcraft/internal/driver"
)

// Link aggregation on the S3900 (Edge-corE ECS firmware): port-channel
// groups, created GROUP-FIRST and joined from the member's context.
//
// # The capture that anchors both directions (s3900lab, 2026-08-13)
//
//	configure terminal ; interface port-channel 1 ; exit
//	interface ethernet 1/5 ; channel-group 1 ; end
//
//	show interfaces status port-channel 1
//	 Group Type             : Static
//	 ...
//	 Member Ports           : Eth1/5
//	 Active Member Ports    : None
//
// and the two rules the device answered itself:
//
//   - "channel-group 1" on a member whose group does NOT exist yet is
//     refused ("Failed to add ethernet 1/5 to channel-group 1") — the
//     group interface comes first;
//   - "channel-group 1 mode active" is a SYNTAX error on this train: the
//     bare form attaches statically, and LACP on ECS is the per-port `lacp`
//     command whose groups form DYNAMICALLY with an id the device chooses.
//     A pinned LACP group is therefore not expressible here, and the writer
//     refuses mode "lacp" with that sentence instead of writing something
//     that only resembles it.
//
// # Where the read comes from
//
// The running config, which ReadState already fetches: group existence from
// "interface port-channel N" sections (whose body carries the group's own
// switchport lines — the VLAN half), membership from "channel-group N"
// under the member's ethernet section. One status query per DISCOVERED group
// adds the Running/type facts; a fleet with no aggregates pays nothing.

// reS3900PoSection matches a port-channel section of the running config.
var reS3900PoSection = regexp.MustCompile(
	`(?ms)^interface port-channel (\d+)\r?\n(.*?)(?:^!|\z)`)

// reS3900EthSection matches an ethernet member section.
var reS3900EthSection = regexp.MustCompile(
	`(?ms)^interface ethernet (1/\d+)\r?\n(.*?)(?:^!|\z)`)

// reS3900ChannelGroup finds the membership line inside an ethernet section.
var reS3900ChannelGroup = regexp.MustCompile(`(?im)^\s*channel-group (\d+)`)

// reS3900StatusType / Members / Link read the status query.
var reS3900StatusType = regexp.MustCompile(`(?im)^\s*Group Type\s*:\s*(\S+)`)
var reS3900StatusLink = regexp.MustCompile(`(?im)^\s*Link Status\s*:\s*(\S+)`)

// parseS3900LAGs extracts the aggregates from the running config. PURE.
func parseS3900LAGs(runOut string) []driver.LAGState {
	var byNum = map[int]*driver.LAGState{}
	var sections = reS3900PoSection.FindAllStringSubmatch(runOut, -1)
	var i int
	for i = range sections {
		var n, err = strconv.Atoi(sections[i][1])
		if err != nil {
			continue
		}
		var lag = driver.LAGState{
			Name: driver.StandardLAGName(n), Raw: "port-channel " + sections[i][1],
			// The mode is refined by the status query; static is what the
			// captured write produces and the honest default from config alone.
			Mode: driver.LAGModeStatic,
			VLAN: portStateOfBody(sections[i][2]),
		}
		byNum[n] = &lag
	}
	var members = reS3900EthSection.FindAllStringSubmatch(runOut, -1)
	for i = range members {
		var group = reS3900ChannelGroup.FindStringSubmatch(members[i][2])
		if group == nil {
			continue
		}
		var n, err = strconv.Atoi(group[1])
		if err != nil {
			continue
		}
		var lag, exists = byNum[n]
		if !exists {
			var fresh = driver.LAGState{
				Name: driver.StandardLAGName(n),
				Raw:  "port-channel " + group[1], Mode: driver.LAGModeStatic,
			}
			byNum[n] = &fresh
			lag = &fresh
		}
		var std, ok = (&Driver{}).StandardPortName("Eth" + members[i][1])
		if !ok {
			continue
		}
		lag.Members = append(lag.Members, std)
	}
	var out []driver.LAGState
	var n int
	for n = range byNum {
		out = append(out, *byNum[n])
	}
	driver.SortLAGs(out)
	return out
}

// portStateOfBody reads the switchport half of a section body — the same
// vocabulary the port parser reads on ethernet sections.
func portStateOfBody(body string) PortState {
	var out PortState
	if regexp.MustCompile(`(?im)^\s*switchport mode access`).MatchString(body) {
		out.Mode = "access"
	} else if regexp.MustCompile(`(?im)^\s*switchport mode trunk`).MatchString(body) {
		out.Mode = "trunk"
	}
	var native = regexp.MustCompile(`(?im)^\s*switchport (?:trunk )?native vlan (\d+)`).FindStringSubmatch(body)
	if native != nil {
		out.PVID, _ = strconv.Atoi(native[1])
	}
	var allowed = regexp.MustCompile(`(?im)^\s*switchport (?:trunk )?allowed vlan add ([\d,\-]+)( tagged)?`).FindAllStringSubmatch(body, -1)
	var i int
	for i = range allowed {
		var v int
		var part string
		for _, part = range strings.Split(allowed[i][1], ",") {
			v, _ = strconv.Atoi(strings.TrimSpace(part))
			if v > 0 {
				out.Tagged = append(out.Tagged, v)
			}
		}
	}
	return out
}

// readLAGs parses the run output and refines each discovered group with one
// status query. Called from ReadState with the run text it already holds.
func (c *Client) readLAGs(ctx context.Context, runOut string) []driver.LAGState {
	var lags = parseS3900LAGs(runOut)
	var i int
	for i = range lags {
		var n, _ = driver.LAGNumber(lags[i].Name)
		var text, err = c.cli.run(ctx,
			"show interfaces status port-channel "+strconv.Itoa(n), cmdTimeout)
		if err != nil {
			continue
		}
		var typ = reS3900StatusType.FindStringSubmatch(text)
		if typ != nil && !strings.EqualFold(typ[1], "Static") {
			lags[i].Mode = driver.LAGModeLACP
		}
		var link = reS3900StatusLink.FindStringSubmatch(text)
		lags[i].Running = link != nil && strings.EqualFold(link[1], "Up")
	}
	return lags
}

// ApplyLAGs writes STATIC aggregates — the captured regime. Replace semantics.
func (c *Client) ApplyLAGs(ctx context.Context, desired []driver.LAGState) (ApplyResult, error) {
	var out ApplyResult
	var err = driver.ValidateLAGs(desired)
	if err != nil {
		return out, err
	}
	var i int
	for i = range desired {
		if desired[i].Mode == driver.LAGModeLACP {
			return out, fmt.Errorf("aggregate %s: this train cannot PIN an LACP "+
				"group — \"channel-group N mode active\" is a syntax error "+
				"(captured 2026-08-13), and ECS's per-port \"lacp\" forms groups "+
				"dynamically with an id the device chooses. Use mode static, or "+
				"leave the far end static too", desired[i].Name)
		}
	}
	var runOut string
	if runOut, err = c.cli.run(ctx, "show run", showRunTimeout); err != nil && runOut == "" {
		return out, fmt.Errorf("state unreadable before writing aggregates: %v", err)
	}
	var have = parseS3900LAGs(runOut)

	var cmds []string
	var wantNames = map[string]bool{}
	for i = range desired {
		var want = desired[i]
		wantNames[want.Name] = true
		var n, ok = driver.LAGNumber(want.Name)
		if !ok {
			return out, fmt.Errorf("aggregate %q: not a writable standard name", want.Name)
		}
		// GROUP FIRST — the platform's own rule, answered live.
		cmds = append(cmds, "interface port-channel "+strconv.Itoa(n))
		if want.VLAN.Mode == "trunk" {
			// The port applier's own forms (apply.go): allowed-vlan-add and
			// "switchport trunk native vlan" — the candidate this train accepts.
			cmds = append(cmds, "switchport mode trunk")
			var t int
			for _, t = range want.VLAN.Tagged {
				cmds = append(cmds, "switchport trunk allowed vlan add "+strconv.Itoa(t))
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
		var m string
		for _, m = range want.Members {
			var ifName = cliPortName(m)
			if ifName == "" {
				return out, fmt.Errorf("aggregate %s: member %q is outside the "+
					"naming scheme", want.Name, m)
			}
			cmds = append(cmds, "interface "+ifName, "channel-group "+strconv.Itoa(n), "exit")
		}
		out.PortsWritten = append(out.PortsWritten, want.Name)
	}
	for i = range have {
		if wantNames[have[i].Name] {
			continue
		}
		var n, _ = driver.LAGNumber(have[i].Name)
		var m string
		for _, m = range have[i].Members {
			cmds = append(cmds, "interface "+cliPortName(m), "no channel-group", "exit")
		}
		cmds = append(cmds, "no interface port-channel "+strconv.Itoa(n))
		out.PortsWritten = append(out.PortsWritten, have[i].Name)
		out.Notes = append(out.Notes, "aggregate "+have[i].Name+" dissolved: its "+
			"members keep their own interface configuration")
	}
	if len(cmds) == 0 {
		return out, nil
	}
	if err = c.runConfigBlock(ctx, cmds); err != nil {
		return out, err
	}
	return out, nil
}
