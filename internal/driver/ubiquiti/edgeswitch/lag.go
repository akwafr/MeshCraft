package edgeswitch

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"meshcraft/internal/driver"
)

// Link aggregation on EdgeSwitch (FASTPATH): the platform PRE-PROVISIONS its
// aggregates — "show port-channel all" lists 3/1 … 3/6 on the lab EP-S16
// out of the box, Dynamic, Down, memberless — and membership is written from
// the MEMBER's interface context.
//
// # The capture that anchors both directions (unifimoule1, 2026-08-13)
//
//	Log.    Channel                     Adm.      Mbr    Device/       Port      Port
//	Intf    Name            Min  Link   Mode Type Ports  Timeout       Speed     Active
//	------- --------------- ---- ------ ---- ---- ------ ------------- --------- -------
//	3/1                     1    Down   En.  Dyn. 0/5    actor/long    Auto      False
//	                                                     partner/long
//	3/2                     1    Down   En.  Dyn.
//
// built with "configure ; interface 0/5 ; addport 3/1", removed with
// "deleteport 3/1" — read back before and after.
//
// # What this connector does NOT write, and why the refusal is typed
//
// The aggregate's VLAN mapping and its Dynamic/Static toggle have not been
// exercised on this hardware; per the house rule an unproven form is refused
// with its reason rather than sent hopefully. Membership — the captured half —
// is written; a desired list carrying a VLAN mapping gets it back in Notes.

// reEdgeLAGRow matches one aggregate row: "3/1", optional name, min-links,
// link, admin mode, type, then the optional first member.
var reEdgeLAGRow = regexp.MustCompile(
	`(?m)^(3/(\d+))\s+(\S*)\s*(\d+)\s+(Up|Down)\s+(\S+)\s+(Dyn\.|St\.)\s*(\d+/\d+)?`)

// reEdgeMemberCont catches additional member lines ("0/7  actor/long …")
// between aggregate rows.
var reEdgeMemberCont = regexp.MustCompile(`^\s+(\d+/\d+)\s+actor`)

// parsePortChannelAll extracts the aggregates. PURE, replayed on the capture.
// Memberless pre-provisioned aggregates are NOT reported: they are the
// platform's empty slots, and twenty-four screens listing six skeleton LAGs
// per EdgeSwitch would bury the real ones.
func parsePortChannelAll(text string) []driver.LAGState {
	var out []driver.LAGState
	var current *driver.LAGState
	var line string
	for _, line = range strings.Split(text, "\n") {
		var row = reEdgeLAGRow.FindStringSubmatch(line)
		if row != nil {
			if current != nil && len(current.Members) > 0 {
				out = append(out, *current)
			}
			var n, _ = strconv.Atoi(row[2])
			var mode = driver.LAGModeStatic
			if row[7] == "Dyn." {
				mode = driver.LAGModeLACP
			}
			current = &driver.LAGState{
				Name: driver.StandardLAGName(n), Raw: row[1], Mode: mode,
				Running: row[5] == "Up",
			}
			if row[8] != "" {
				var std, ok = (&Driver{}).StandardPortName(row[8])
				if ok {
					current.Members = append(current.Members, std)
				}
			}
			continue
		}
		if current == nil {
			continue
		}
		var cont = reEdgeMemberCont.FindStringSubmatch(line)
		if cont != nil {
			var std, ok = (&Driver{}).StandardPortName(cont[1])
			if ok {
				current.Members = append(current.Members, std)
			}
		}
	}
	if current != nil && len(current.Members) > 0 {
		out = append(out, *current)
	}
	driver.SortLAGs(out)
	return out
}

// readLAGs runs the table. Called from ReadState.
func (c *Client) readLAGs(ctx context.Context) ([]driver.LAGState, error) {
	var text, err = c.cli.run(ctx, "show port-channel all", cmdTimeout)
	if err != nil && text == "" {
		return nil, err
	}
	return parsePortChannelAll(text), nil
}

// ApplyLAGs writes MEMBERSHIP — the captured half — with replace semantics
// over the pre-provisioned 3/N slots.
func (c *Client) ApplyLAGs(ctx context.Context, desired []driver.LAGState) (ApplyResult, error) {
	var out ApplyResult
	var err = driver.ValidateLAGs(desired)
	if err != nil {
		return out, err
	}
	// LACP fallback is REFUSED, not trimmed: this connector does not declare
	// GestureLAGFallback, and no FASTPATH fallback form has been captured.
	var fb, wantsFB = driver.LAGFallbackWanted(desired)
	if wantsFB {
		return out, driver.RefuseLAGFallback("ubiquiti-edgeswitch", fb.Name)
	}
	var text string
	if text, err = c.cli.run(ctx, "show port-channel all", cmdTimeout); err != nil && text == "" {
		return out, fmt.Errorf("aggregates unreadable before writing: %v", err)
	}
	var have = parsePortChannelAll(text)
	var haveMembers = map[string]map[string]bool{}
	var i int
	for i = range have {
		haveMembers[have[i].Name] = map[string]bool{}
		var m string
		for _, m = range have[i].Members {
			haveMembers[have[i].Name][m] = true
		}
	}

	var cmds []string
	var wantNames = map[string]bool{}
	for i = range desired {
		var want = desired[i]
		wantNames[want.Name] = true
		var n, ok = driver.LAGNumber(want.Name)
		if !ok || n < 1 || n > 6 {
			return out, fmt.Errorf("aggregate %q: this platform pre-provisions "+
				"3/1 through 3/6 — LAG1 to LAG6 are the only writable names", want.Name)
		}
		if want.Mode != driver.LAGModeLACP {
			return out, fmt.Errorf("aggregate %s: only the Dynamic (LACP) type has "+
				"been exercised on this hardware; the Static toggle is refused "+
				"until captured", want.Name)
		}
		if want.VLAN.Mode != "" {
			out.Notes = append(out.Notes, "aggregate "+want.Name+": the VLAN "+
				"mapping was NOT applied — the 3/"+strconv.Itoa(n)+" interface's "+
				"VLAN forms have not been captured on this hardware yet")
		}
		var slot = "3/" + strconv.Itoa(n)
		var m string
		for _, m = range want.Members {
			if haveMembers[want.Name][m] {
				continue
			}
			var ifName = cliPortName(m)
			if ifName == "" {
				return out, fmt.Errorf("aggregate %s: member %q is outside the "+
					"naming scheme", want.Name, m)
			}
			cmds = append(cmds, "interface "+ifName, "addport "+slot, "exit")
		}
		var wantSet = map[string]bool{}
		for _, m = range want.Members {
			wantSet[m] = true
		}
		for m = range haveMembers[want.Name] {
			if wantSet[m] {
				continue
			}
			cmds = append(cmds, "interface "+cliPortName(m), "deleteport "+slot, "exit")
		}
		out.PortsWritten = append(out.PortsWritten, want.Name)
	}
	// Aggregates no longer wanted: their members released (the slot itself is
	// the platform's and stays).
	for i = range have {
		if wantNames[have[i].Name] {
			continue
		}
		var n, _ = driver.LAGNumber(have[i].Name)
		var m string
		for _, m = range have[i].Members {
			cmds = append(cmds, "interface "+cliPortName(m),
				"deleteport 3/"+strconv.Itoa(n), "exit")
		}
		out.PortsWritten = append(out.PortsWritten, have[i].Name)
		out.Notes = append(out.Notes, "aggregate "+have[i].Name+" emptied: the 3/"+
			strconv.Itoa(n)+" slot itself is pre-provisioned by the platform and remains")
	}
	if len(cmds) == 0 {
		return out, nil
	}
	var block = append([]string{"configure"}, cmds...)
	block = append(block, "exit")
	if err = c.runBlock(ctx, block); err != nil {
		return out, err
	}
	return out, nil
}
