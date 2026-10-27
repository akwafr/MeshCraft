package procurve

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"meshcraft/internal/driver"
)

// Link aggregation on ProCurve: trunks (Trk1…), DOCUMENTED forms — the range
// is parked and no unit is adopted, so unlike every other connector's lag.go
// nothing here has been captured. The same status the whole connector was
// born under, announced the same way: the reader tolerates surprises and the
// writer's first live run must be watched (cf. the package header's rule —
// what has not been seen is announced as such).
//
// Documented vocabulary (ProCurve 2600 Management and Configuration Guide):
//
//	show trunks
//	 Load Balancing
//	  Port | Name        Type      | Group Type
//	  ---- + ----------- --------- + ----- -----
//	  47   |             10/100TX  | Trk1  LACP
//	  48   |             10/100TX  | Trk1  LACP
//
//	trunk 47-48 trk1 lacp        (create / extend)
//	no trunk 47-48               (release members)
//
// The trunk's VLAN mapping is the TrkN interface's own membership, written
// with the same "vlan N tagged/untagged TrkN" forms the port applier uses.

// reTrunkRow matches one member row of "show trunks": port | name type |
// group type.
var reTrunkRow = regexp.MustCompile(
	`(?m)^\s*(\d+)\s*\|.*\|\s*Trk(\d+)\s+(\S+)\s*$`)

// parseShowTrunks folds member rows into aggregates. PURE — tested on the
// documented shape, and the test says so.
func parseShowTrunks(text string) []driver.LAGState {
	var byNum = map[int]*driver.LAGState{}
	var rows = reTrunkRow.FindAllStringSubmatch(text, -1)
	var i int
	for i = range rows {
		var n, err = strconv.Atoi(rows[i][2])
		if err != nil {
			continue
		}
		var lag, exists = byNum[n]
		if !exists {
			var mode = driver.LAGModeStatic
			if strings.EqualFold(rows[i][3], "LACP") {
				mode = driver.LAGModeLACP
			}
			var fresh = driver.LAGState{
				Name: driver.StandardLAGName(n), Raw: "Trk" + rows[i][2], Mode: mode,
			}
			byNum[n] = &fresh
			lag = &fresh
		}
		var std, ok = (&Driver{}).StandardPortName(rows[i][1])
		if ok {
			lag.Members = append(lag.Members, std)
		}
	}
	var out []driver.LAGState
	var n int
	for n = range byNum {
		out = append(out, *byNum[n])
	}
	driver.SortLAGs(out)
	return out
}

// readLAGs runs "show trunks". Called from ReadState; failure is tolerated
// there (nil = not read).
func (c *Client) readLAGs(ctx context.Context) ([]driver.LAGState, error) {
	var text, err = c.cli.run(ctx, "show trunks", cmdTimeout)
	if err != nil && text == "" {
		return nil, err
	}
	return parseShowTrunks(text), nil
}

// ApplyLAGs writes trunks with the DOCUMENTED forms. Replace semantics.
func (c *Client) ApplyLAGs(ctx context.Context, desired []driver.LAGState) (driver.ApplyResult, error) {
	var out driver.ApplyResult
	var err = driver.ValidateLAGs(desired)
	if err != nil {
		return out, err
	}
	// LACP fallback is REFUSED, not trimmed: this connector does not declare
	// GestureLAGFallback, and ProCurve trunks have no such keyword at all.
	var fb, wantsFB = driver.LAGFallbackWanted(desired)
	if wantsFB {
		return out, driver.RefuseLAGFallback("hpe-procurve", fb.Name)
	}
	var text string
	if text, err = c.cli.run(ctx, "show trunks", cmdTimeout); err != nil && text == "" {
		return out, fmt.Errorf("trunks unreadable before writing: %v", err)
	}
	var have = parseShowTrunks(text)

	var cmds []string
	var wantNames = map[string]bool{}
	var i int
	for i = range desired {
		var want = desired[i]
		wantNames[want.Name] = true
		var n, ok = driver.LAGNumber(want.Name)
		if !ok {
			return out, fmt.Errorf("aggregate %q: not a writable standard name", want.Name)
		}
		var ports = make([]string, 0, len(want.Members))
		var m string
		for _, m = range want.Members {
			var raw = bareNumber(m)
			if raw == "" {
				return out, fmt.Errorf("aggregate %s: member %q is outside the "+
					"naming scheme", want.Name, m)
			}
			ports = append(ports, raw)
		}
		var kind = "trunk"
		if want.Mode == driver.LAGModeLACP {
			kind = "lacp"
		}
		cmds = append(cmds, "trunk "+strings.Join(ports, ",")+" trk"+strconv.Itoa(n)+" "+kind)
		if want.VLAN.Mode == "access" {
			var pvid = want.VLAN.PVID
			if pvid == 0 {
				pvid = 1
			}
			cmds = append(cmds, "vlan "+strconv.Itoa(pvid)+" untagged trk"+strconv.Itoa(n))
		} else if want.VLAN.Mode == "trunk" {
			if want.VLAN.PVID > 0 {
				cmds = append(cmds, "vlan "+strconv.Itoa(want.VLAN.PVID)+" untagged trk"+strconv.Itoa(n))
			}
			var t int
			for _, t = range want.VLAN.Tagged {
				cmds = append(cmds, "vlan "+strconv.Itoa(t)+" tagged trk"+strconv.Itoa(n))
			}
		}
		out.PortsWritten = append(out.PortsWritten, want.Name)
	}
	for i = range have {
		if wantNames[have[i].Name] {
			continue
		}
		var ports = make([]string, 0, len(have[i].Members))
		var m string
		for _, m = range have[i].Members {
			var raw = bareNumber(m)
			if raw != "" {
				ports = append(ports, raw)
			}
		}
		if len(ports) > 0 {
			cmds = append(cmds, "no trunk "+strings.Join(ports, ","))
		}
		out.PortsWritten = append(out.PortsWritten, have[i].Name)
	}
	if len(cmds) == 0 {
		return out, nil
	}
	out.Notes = append(out.Notes, "ProCurve trunk forms are DOCUMENTED, not yet "+
		"captured on hardware (the range is parked): verify the result on the device")
	if err = c.runConfigBlock(ctx, cmds); err != nil {
		return out, err
	}
	return out, nil
}

// bareNumber renders a standard port name as the bare number this range's
// CLI speaks ("GE47" → "47" — its ifDescr is the bare number too).
func bareNumber(std string) string {
	var m = regexp.MustCompile(`^[A-Z]+(\d+)$`).FindStringSubmatch(std)
	if m == nil {
		return ""
	}
	return m[1]
}
