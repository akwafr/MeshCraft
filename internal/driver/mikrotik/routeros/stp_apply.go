package routeros

import (
	"context"
	"fmt"
	"strconv"

	"meshcraft/internal/driver"
)

// Compile-time: RouterOS Client applies spanning tree.
var _ driver.SpanningTreeApplier = (*Client)(nil)

// bridgeSTPFields builds /interface/bridge fields for a spanning-tree plan.
// skip is non-empty when the mode is not supported (caller should not write).
func bridgeSTPFields(plan driver.SpanningTreePlan) (fields map[string]string, skip string) {
	fields = map[string]string{}
	if !plan.Enabled {
		fields["protocol-mode"] = "none"
	} else {
		var mode = driver.NormalizeSTPMode(plan.Mode)
		switch mode {
		case "stp", "rstp", "mstp":
			fields["protocol-mode"] = mode
		default:
			return nil, fmt.Sprintf(
				"RouterOS STP mode %q is not applied (want none/stp/rstp/mstp)", plan.Mode)
		}
	}
	if plan.BridgePriority != nil {
		var p = *plan.BridgePriority
		if p < 0 {
			p = 0
		}
		if p > 61440 {
			p = 61440
		}
		// RouterOS stores priority as hex (testdata: "0x8000").
		fields["priority"] = fmt.Sprintf("0x%04x", p)
	}
	return fields, ""
}

// ApplySpanningTree sets bridge protocol-mode (none|stp|rstp|mstp), optional
// bridge priority, and per-port edge / path-cost when requested.
//
// Writes:
//   - /interface/bridge `protocol-mode`, `priority`
//   - /interface/bridge/port `edge`, `path-cost`
func (c *Client) ApplySpanningTree(ctx context.Context, plan driver.SpanningTreePlan) (driver.ApplyResult, error) {
	var out driver.ApplyResult
	// SuperRoot is not carried here: RouterOS takes a bridge `priority` and has no
	// "root super" equivalent, and writing 0 on the operator's behalf would make
	// this bridge the root of the whole L2 domain — a topology change the checkbox
	// was designed to make on an Arista, not here. Said rather than dropped, per
	// driver.SpanningTreePlan.SuperRoot.
	if plan.SuperRoot {
		out.Skipped = append(out.Skipped,
			"SUPER ROOT not applied: this connector does not implement it. RouterOS "+
				"expresses the same idea as a bridge priority of 0 and has no dedicated "+
				"keyword; set the priority explicitly if this bridge must be root")
	}
	var bridges, err = c.List(ctx, "interface/bridge")
	if err != nil {
		return out, fmt.Errorf("list interface/bridge: %w", err)
	}
	if len(bridges) == 0 {
		out.Notes = append(out.Notes, "no bridge on this device: spanning tree does not apply")
		return out, nil
	}
	var br = bridges[0]
	var bridgeID = br.Get(".id")
	if bridgeID == "" {
		return out, fmt.Errorf("bridge %q has no .id: refusing STP write", br.Get("name"))
	}

	var fields map[string]string
	var skip string
	fields, skip = bridgeSTPFields(plan)
	if skip != "" {
		out.Skipped = append(out.Skipped, skip)
		return out, nil
	}

	if _, err = c.Set(ctx, "interface/bridge", bridgeID, fields); err != nil {
		return out, fmt.Errorf("set bridge STP: %w", err)
	}
	c.step(true, "bridge spanning-tree mode applied",
		fmt.Sprintf("enabled=%v mode=%s", plan.Enabled, fields["protocol-mode"]))

	if len(plan.Ports) == 0 {
		return out, nil
	}

	var ports []Row
	if ports, err = c.List(ctx, "interface/bridge/port"); err != nil {
		return out, fmt.Errorf("list interface/bridge/port: %w", err)
	}
	var byIface = map[string]Row{}
	var row Row
	for _, row = range ports {
		var name = row.Get("interface")
		if name == "" {
			continue
		}
		if row.Get("bridge") != "" && br.Get("name") != "" && row.Get("bridge") != br.Get("name") {
			continue
		}
		byIface[name] = row
	}

	var p driver.SpanningTreePortIntent
	for _, p = range plan.Ports {
		var portRow, ok = byIface[p.Port]
		if !ok {
			out.Skipped = append(out.Skipped, "unknown bridge port "+p.Port+" for STP")
			continue
		}
		var portID = portRow.Get(".id")
		if portID == "" {
			out.Skipped = append(out.Skipped, p.Port+": no .id — STP port left unchanged")
			continue
		}
		var pf = map[string]string{}
		var edge = driver.NormalizeSTPEdge(p.Edge)
		if edge != "" {
			pf["edge"] = edge
		}
		if p.BPDUGuard != nil {
			if *p.BPDUGuard {
				pf["bpdu-guard"] = "yes"
			} else {
				pf["bpdu-guard"] = "no"
			}
		}
		if p.PathCost != nil && *p.PathCost > 0 {
			pf["path-cost"] = strconv.Itoa(*p.PathCost)
		}
		// RouterOS has no per-port "STP enabled" bit like SwOS ena — skip Enabled.
		if p.Enabled != nil {
			out.Notes = append(out.Notes,
				p.Port+": per-port STP enable is not a RouterOS bridge-port field — left unchanged")
		}
		if len(pf) == 0 {
			continue
		}
		if _, err = c.Set(ctx, "interface/bridge/port", portID, pf); err != nil {
			return out, fmt.Errorf("set STP on %s: %w", p.Port, err)
		}
	}

	c.step(true, "spanning tree applied",
		fmt.Sprintf("enabled=%v mode=%s ports=%d", plan.Enabled, plan.Mode, len(plan.Ports)))
	return out, nil
}
