package instanton

import (
	"context"
	"fmt"
	"strings"

	"meshcraft/internal/driver"
)

// Compile-time: Instant On Admin applies spanning tree (when WritableExtras allows).
var _ driver.SpanningTreeApplier = (*Admin)(nil)

// ApplySpanningTree writes SpanningTreeGlobalParam (enable + STPOperationMode)
// and optional STP GlobalSetting bridge priority, then per-port fields on
// STP.InterfaceList — the same shape the product UI posts (pages1.js).
//
// Mode codes (STPOperationMode): 0=STP, 2=RSTP, 3=MSTP.
func (a *Admin) ApplySpanningTree(ctx context.Context, plan driver.SpanningTreePlan) (driver.ApplyResult, error) {
	var out driver.ApplyResult
	if !WritesExtra("stp") {
		out.Notes = append(out.Notes,
			"Instant On STP apply is not declared writable (WritableExtras)")
		a.step(true, "spanning tree apply deferred", out.Notes[0])
		return out, nil
	}

	var mode = driver.NormalizeSTPMode(plan.Mode)
	if plan.Enabled && mode != "stp" && mode != "rstp" && mode != "mstp" {
		out.Skipped = append(out.Skipped, "unsupported STP mode "+plan.Mode)
		return out, nil
	}

	var enabled = "1"
	if !plan.Enabled || mode == driver.STPPrefNone {
		enabled = "2" // Instant On _FALSE
		mode = "rstp"
	}
	var opMode = ionOperationModeCode(mode)

	var paramInner = "<enabled>" + enabled + "</enabled>" +
		"<STPOperationMode>" + opMode + "</STPOperationMode>"
	if err := a.writeSection(ctx, sectionSTPGlobalParam, "set", paramInner,
		"spanning-tree admin/mode via "+sectionSTPGlobalParam); err != nil {
		return out, err
	}
	out.Notes = append(out.Notes,
		fmt.Sprintf("SpanningTreeGlobalParam enabled=%s STPOperationMode=%s", enabled, opMode))

	// THE REGION, and only in MSTP mode. Writing a region name on a device running
	// RSTP configures a field nothing reads, and would show as "applied" on a
	// screen where nothing changed.
	//
	// Name and revision are written; the VLAN-to-instance map is NOT, and the
	// result says so rather than staying quiet (cf. parseIonRegion for why the
	// entry shape is not guessed). That is an honest partial: two devices sharing
	// a name and revision but differing on the digest are still in DIFFERENT
	// regions, so the caller must not read this as "region homogenised".
	if mode == "mstp" && plan.Region.Configured() {
		var region = "<regionName>" + xmlEscape(plan.Region.Name) + "</regionName>" +
			"<revisionNo>" + fmt.Sprintf("%d", plan.Region.Revision) + "</revisionNo>"
		var regErr = a.writeSection(ctx, sectionMSTP, "set", region,
			"MST region via "+sectionMSTP)
		if regErr != nil {
			return out, regErr
		}
		out.Notes = append(out.Notes, fmt.Sprintf(
			"MSTP region name=%q revision=%d", plan.Region.Name, plan.Region.Revision))
		if len(plan.Region.InstanceVLANs) > 0 {
			out.Skipped = append(out.Skipped,
				"MST instance-to-VLAN mapping NOT written: the InstanceEntry field "+
					"shape has never been observed on this range (every unit surveyed "+
					"serves an empty element), and guessing it produces this platform's "+
					"silent failure — statusCode 0, no change, no message. Name and "+
					"revision are set; devices still differ on the VLAN digest and are "+
					"therefore NOT yet in one region.")
		}
	}

	if plan.BridgePriority != nil && enabled == "1" {
		var p = *plan.BridgePriority
		if p < 0 {
			p = 0
		}
		if p > 61440 {
			p = 61440
		}
		var bridgeInner = "<GlobalSetting><BridgeSetting><bridgePriority>" +
			fmt.Sprintf("%d", p) + "</bridgePriority></BridgeSetting></GlobalSetting>"
		if err := a.writeSection(ctx, sectionSTP, "set", bridgeInner,
			"bridge priority via "+sectionSTP); err != nil {
			return out, err
		}
		out.Notes = append(out.Notes, fmt.Sprintf("STP bridgePriority=%d", p))
	}

	var p driver.SpanningTreePortIntent
	for _, p = range plan.Ports {
		var note, perr = a.applySTPPort(ctx, p)
		if perr != nil {
			return out, perr
		}
		if note != "" {
			out.Notes = append(out.Notes, note)
		}
	}
	a.step(true, "spanning tree applied",
		fmt.Sprintf("enabled=%v mode=%s ports=%d", plan.Enabled, mode, len(plan.Ports)))
	return out, nil
}

// applySTPPort writes one port onto STP.InterfaceList (CST edit form shape).
func (a *Admin) applySTPPort(ctx context.Context, p driver.SpanningTreePortIntent) (string, error) {
	var edge = driver.NormalizeSTPEdge(p.Edge)
	if edge == "" && p.Enabled == nil && p.BPDUGuard == nil && p.PathCost == nil {
		return "", nil
	}
	var iface = devicePortName(p.Port, a.portPrefix())
	var entry = "<InterfaceEntry><interfaceName>" + xmlEscape(iface) + "</interfaceName>"
	if edge != "" {
		var code = portFastCode(edge)
		if code != "" {
			entry += "<portFastEnabled>" + code + "</portFastEnabled>"
		}
	}
	if p.Enabled != nil {
		var st = "1"
		if !*p.Enabled {
			st = "2"
		}
		entry += "<STPEnabled>" + st + "</STPEnabled>"
	}
	if p.BPDUGuard != nil {
		var g = "2"
		if *p.BPDUGuard {
			g = "1"
		}
		entry += "<BPDUGuardEnabled>" + g + "</BPDUGuardEnabled>"
	}
	if p.PathCost != nil && *p.PathCost > 0 {
		entry += "<pathCostConfigured>" + fmt.Sprintf("%d", *p.PathCost) + "</pathCostConfigured>"
	}
	entry += "</InterfaceEntry>"

	var err = a.writeSection(ctx, sectionSTP, "set",
		"<InterfaceList>"+entry+"</InterfaceList>",
		"STP port settings for "+p.Port)
	if err != nil {
		var msg = err.Error()
		if strings.Contains(msg, "Cannot find the section") {
			return p.Port + ": STP port write not applied — " + msg, nil
		}
		return "", fmt.Errorf("%s: STP port write: %w", p.Port, err)
	}
	return fmt.Sprintf("%s: STP port written on section %s", p.Port, sectionSTP), nil
}
