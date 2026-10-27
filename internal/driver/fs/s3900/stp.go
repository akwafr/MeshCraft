package s3900

// Spanning-tree READ for the FSOS/OpCode CLI.
//
// CAPTURED on the lab S3900-24T4S (FSOS 1.7.7, 2026-08-05,
// testdata/show_spanning_tree.txt). "show spanning-tree" is the ONE working
// form on this firmware: "show spanning-tree summary" is refused
// ("% Invalid input detected at '^' marker."), "show spanning-tree brief"
// prints its banner line and truncates, and "show spanning-tree mst
// configuration" answers nothing — the MST REGION is therefore NOT readable
// on this CLI, and SpanningTreeState.Region stays empty rather than guessed
// (802.1s treats a mismatched region as an ordinary RSTP neighbour, so an
// invented region would hide exactly the divergence the field exists to
// catch).
//
// The output is per MST instance ("Spanning tree brief for instance N");
// the global state is read from INSTANCE 0 (the CIST — the tree the rest of
// the fleet's readers report), and later instances are ignored. Each
// instance carries the same shape:
//
//	Spanning tree brief for instance 0
//	Spanning Tree Mode             : MSTP
//	Spanning Tree Status           : Enabled
//	 Root ID     Priority    0
//	             Address    D4:01:C3:AB:32:F4
//	 Bridge ID   Priority    32768
//	             Address    64:9D:99:16:34:10
//	Interface Role Sts   Bridge ID                    Port ID    Prio  Cost     STP
//	--------- ---- ----- ---------------------------- ---------- ----- -------- ----
//	Eth 1/28  ROOT FWD   32768.0.02:00:5e:10:61:A7    128.51       128     2000 EN
//
// The root path cost is NOT printed anywhere in this output; it stays 0
// (unknown) rather than borrowing the root port's own path cost, which is a
// different number.

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"meshcraft/internal/driver"
)

// ReadSpanningTree reports the bridge as instance 0 (the CIST) sees it.
func (c *Client) ReadSpanningTree(ctx context.Context) (driver.SpanningTreeState, error) {
	if c.cli == nil {
		return driver.SpanningTreeState{}, fmt.Errorf("no open session on this access")
	}
	var out, err = c.cli.run(ctx, "show spanning-tree", cmdTimeout)
	if err != nil && strings.TrimSpace(out) == "" {
		return driver.SpanningTreeState{}, fmt.Errorf("show spanning-tree on %s: %v", c.host, err)
	}
	var st = parseSpanningTree(out)
	c.step(true, "spanning tree read",
		fmt.Sprintf("%s, %d port(s) in the table", st.Mode, len(st.Ports)))
	return st, nil
}

var (
	reSTPInstance = regexp.MustCompile(`(?i)^Spanning tree brief for instance\s+(\d+)`)
	reSTPMode     = regexp.MustCompile(`(?i)^Spanning Tree Mode\s*:\s*(\S+)`)
	reSTPStatus   = regexp.MustCompile(`(?i)^Spanning Tree Status\s*:\s*(\S+)`)
	reSTPPriority = regexp.MustCompile(`(?i)^\s*(Root|Bridge) ID\s+Priority\s+(\d+)`)
	reSTPAddress  = regexp.MustCompile(`(?i)^\s*Address\s+([0-9A-Fa-f:]{17})`)
	// One port row: "Eth 1/ 1  DISB BLK   32768.0.64:9D:99:16:34:10    128.1   128    20000 EN" —
	// the unit/port pair may carry a space after the slash on single-digit
	// ports, and the trailing STP column reads EN/DIS.
	reSTPPortRow = regexp.MustCompile(`(?i)^Eth\s+(\d+)/\s*(\d+)\s+(\S+)\s+(\S+)\s+\S+\s+\S+\s+\d+\s+(\d+)\s+(EN|DIS)\s*$`)
)

// stpRoleWords: this CLI's four-letter role column, translated to the shared
// vocabulary (driver.STPRole*). An unknown word passes through lowercased
// rather than being dropped: a new firmware's word is better shown verbatim
// than silently blanked.
var stpRoleWords = map[string]string{
	"DISB": driver.STPRoleDisabled,
	"ALTN": driver.STPRoleAlternate,
	"ROOT": driver.STPRoleRoot,
	"DESG": driver.STPRoleDesignated,
	"BACK": driver.STPRoleBackup,
	"MAST": driver.STPRoleMaster,
}

// stpStateWords: the Sts column.
var stpStateWords = map[string]string{
	"BLK": driver.STPStateDiscarding,
	"DIS": driver.STPStateDiscarding,
	"LRN": driver.STPStateLearning,
	"FWD": driver.STPStateForwarding,
}

// parseSpanningTree reads the instance-0 section of "show spanning-tree".
// Pure, so the captured output is replayable in tests.
func parseSpanningTree(out string) driver.SpanningTreeState {
	var st driver.SpanningTreeState
	var inZero = false
	var seenZero = false
	// Which "ID" block the next Address line belongs to: "Root" or "Bridge".
	var idBlock string
	var line string
	for _, line = range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r ")
		var m = reSTPInstance.FindStringSubmatch(strings.TrimSpace(line))
		if m != nil {
			inZero = m[1] == "0"
			if inZero {
				seenZero = true
			}
			continue
		}
		if !inZero {
			continue
		}
		var t = strings.TrimSpace(line)
		if m = reSTPMode.FindStringSubmatch(t); m != nil {
			st.Mode = strings.ToLower(m[1])
			continue
		}
		if m = reSTPStatus.FindStringSubmatch(t); m != nil {
			st.Enabled = strings.EqualFold(m[1], "Enabled")
			continue
		}
		if m = reSTPPriority.FindStringSubmatch(line); m != nil {
			idBlock = m[1]
			var prio, _ = strconv.Atoi(m[2])
			if strings.EqualFold(idBlock, "Bridge") {
				st.BridgePriority = prio
			} else {
				st.RootPriority = prio
			}
			continue
		}
		if m = reSTPAddress.FindStringSubmatch(line); m != nil {
			var mac = strings.ToLower(m[1])
			if strings.EqualFold(idBlock, "Bridge") {
				st.BridgeID = mac
			} else if strings.EqualFold(idBlock, "Root") {
				st.RootBridgeID = mac
			}
			continue
		}
		if m = reSTPPortRow.FindStringSubmatch(t); m != nil {
			var unit, _ = strconv.Atoi(m[1])
			var port, _ = strconv.Atoi(m[2])
			var std = standardPortName(unit, port)
			if std == "" {
				continue
			}
			var role, known = stpRoleWords[strings.ToUpper(m[3])]
			if !known {
				role = strings.ToLower(m[3])
			}
			var state, knownState = stpStateWords[strings.ToUpper(m[4])]
			if !knownState {
				state = strings.ToLower(m[4])
			}
			var cost, _ = strconv.Atoi(m[5])
			st.Ports = append(st.Ports, driver.SpanningTreePort{
				Port: std, Role: role, State: state, PathCost: cost,
			})
			continue
		}
	}
	if !seenZero {
		return driver.SpanningTreeState{}
	}
	st.IsRoot = st.BridgeID != "" && st.BridgeID == st.RootBridgeID
	st.Notes = append(st.Notes,
		"MST region not readable on this CLI (\"show spanning-tree mst configuration\" answers nothing on FSOS 1.7.7)")
	return st
}
