package swos

import (
	"context"
	"fmt"

	"meshcraft/internal/driver"
)

// Compile-time: SwOS Admin applies spanning tree.
var _ driver.SpanningTreeApplier = (*Admin)(nil)

// ApplySpanningTree writes RSTP enable masks and optional bridge priority.
//
// SwOS is RSTP-only (Capabilities.STPModes = ["rstp"]). Modes other than rstp
// are refused with a Skipped note. Site preference « none » never reaches here
// (ResolveSTPMode skips none when it is not in capabilities); Enabled=false
// still clears per-port ena if a caller asks directly.
//
// Fields written:
//   - /rstp.b `ena` — per-port RSTP participation
//   - /sys.b `prio` — bridge priority, via full PageSys RMW (mgmt fields preserved)
//
// `/rstp.b` `edge` is operational on SwOS (edge detected), not a sticky admin
// policy — POST is accepted then cleared. Port intents requesting edge are
// noted; use Enabled (ena) to leave the tree.
func (a *Admin) ApplySpanningTree(ctx context.Context, plan driver.SpanningTreePlan) (driver.ApplyResult, error) {
	var out driver.ApplyResult
	if plan.Enabled {
		var mode = driver.NormalizeSTPMode(plan.Mode)
		if mode != "rstp" && mode != "" {
			out.Skipped = append(out.Skipped,
				fmt.Sprintf("SwOS only speaks RSTP: requested mode %q is not applied", plan.Mode))
			a.step(true, "spanning tree mode refused", out.Skipped[len(out.Skipped)-1])
			return out, nil
		}
	}

	var names = a.portNames(ctx)
	if len(names) == 0 {
		return out, fmt.Errorf("port geometry unknown: cannot apply STP")
	}
	var byName = map[string]int{}
	var i int
	for i = range names {
		byName[names[i]] = i
	}

	var rstp, err = a.Get(ctx, PageRSTP)
	if err != nil {
		return out, fmt.Errorf("read %s: %w", PageRSTP, err)
	}
	if !plan.Enabled {
		// Turn RSTP off on every port.
		for i = range names {
			if !rstp.SetBit("ena", i, false) {
				return out, fmt.Errorf("%s: cannot clear ena bit %d", PageRSTP, i)
			}
		}
	} else if len(plan.Ports) == 0 {
		// Site preference resolved to RSTP with no per-port intents: SwOS has no
		// global protocol-mode field — enabling ena on every port is how « turn
		// RSTP on » is expressed on this platform.
		for i = range names {
			if !rstp.SetBit("ena", i, true) {
				return out, fmt.Errorf("%s: cannot set ena bit %d", PageRSTP, i)
			}
		}
	} else {
		var p driver.SpanningTreePortIntent
		for _, p = range plan.Ports {
			var idx, ok = byName[p.Port]
			if !ok {
				out.Skipped = append(out.Skipped, "unknown port "+p.Port+" for STP")
				continue
			}
			if p.Enabled != nil {
				if !rstp.SetBit("ena", idx, *p.Enabled) {
					return out, fmt.Errorf("%s: cannot set ena on %s", PageRSTP, p.Port)
				}
			}
			var edge = driver.NormalizeSTPEdge(p.Edge)
			if edge != "" {
				out.Notes = append(out.Notes,
					p.Port+": SwOS /rstp.b edge is operational (not sticky admin) — use STP on/off (ena); left unchanged")
			}
			if p.BPDUGuard != nil {
				out.Notes = append(out.Notes,
					p.Port+": BPDU Guard is not available on SwOS — left unchanged")
			}
		}
	}

	var fields []string
	var ok bool
	fields, ok = WritableFields(PageRSTP)
	if !ok {
		return out, fmt.Errorf("writable fields for %s unknown", PageRSTP)
	}
	var body *Page
	var missing []string
	body, missing = rstp.Only(fields)
	if len(missing) > 0 {
		return out, fmt.Errorf("%s missing fields %v", PageRSTP, missing)
	}
	if err = a.postRaw(ctx, PageRSTP, body.Encode(), "RSTP port enable mask applied"); err != nil {
		return out, err
	}

	if plan.BridgePriority != nil {
		var sys *Page
		sys, err = a.Get(ctx, PageSys)
		if err != nil {
			return out, fmt.Errorf("read %s: %w", PageSys, err)
		}
		var prio = *plan.BridgePriority
		if prio < 0 {
			prio = 0
		}
		if prio > 61440 {
			prio = 61440
		}
		// Bridge priority is stored in steps of 4096 on IEEE bridges; SwOS accepts
		// the numeric field as-is (sys.b `prio`).
		sys.SetNum("prio", uint32(prio))
		fields, ok = WritableFields(PageSys)
		if !ok {
			return out, fmt.Errorf("writable fields for %s unknown", PageSys)
		}
		body, missing = sys.Only(fields)
		if len(missing) > 0 {
			return out, fmt.Errorf("%s missing fields %v — refusing STP priority write "+
				"(would risk wiping management allow-from)", PageSys, missing)
		}
		if err = a.postRaw(ctx, PageSys, body.Encode(),
			fmt.Sprintf("bridge priority set to %d", prio)); err != nil {
			return out, err
		}
	}

	a.step(true, "spanning tree applied",
		fmt.Sprintf("enabled=%v mode=%s ports=%d", plan.Enabled, plan.Mode, len(plan.Ports)))
	return out, nil
}
