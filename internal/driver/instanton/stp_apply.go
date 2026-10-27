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

	// SuperRoot is not carried here. The wcd framework does expose a bridge
	// priority, but it has no "root super" equivalent and no MST instance the
	// flag could target, and writing 0 from a checkbox meant for another platform
	// would move the root of whatever tree this switch is in. Said rather than
	// dropped, per driver.SpanningTreePlan.SuperRoot.
	if plan.SuperRoot {
		out.Skipped = append(out.Skipped,
			"SUPER ROOT not applied: this connector does not implement it. Set the bridge "+
				"priority explicitly (STP GlobalSetting) if this switch must be root")
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
	var paramErr = a.writeSection(ctx, sectionSTPGlobalParam, "set", paramInner,
		"spanning-tree admin/mode via "+sectionSTPGlobalParam)
	if paramErr != nil {
		return out, paramErr
	}
	// NO NOTE HERE, deliberately. `ApplyResult.Notes` is reread OBSERVATION — what
	// the connector learned about the device that the standard model cannot say,
	// chiefly "this configuration is in place and inert". The screen that renders it
	// (ConnectorNotesBanner) promises exactly that.
	//
	// `enabled` and `opMode` are the two locals written on the line above: a note
	// built from them observes nothing, it quotes the request back. And it is not
	// even the only copy — [Admin.writeSection] already records every accepted write
	// as a step, so the echo competed with the transcript for the one surface where
	// a real remark has to stand out. Cf. the file-level rule in stp_mst.go.

	// THE REGION, and only in MSTP mode. Writing a region name on a device running
	// RSTP configures a field nothing reads, and would show as "applied" on a
	// screen where nothing changed.
	//
	// Name and revision go through the MSTP section; the VLAN-to-instance map
	// goes through the MSTPLogicalInstanceList / MSTPInstanceList / MSTPVLANList
	// triple the product UI itself uses (cf. stp_mst.go — the shape was read
	// from the served JavaScript, then proven on hardware, never guessed).
	if mode == "mstp" && plan.Region.Configured() {
		var region = "<regionName>" + xmlEscape(plan.Region.Name) + "</regionName>" +
			"<revisionNo>" + fmt.Sprintf("%d", plan.Region.Revision) + "</revisionNo>"
		var regErr = a.writeSection(ctx, sectionMSTP, "set", region,
			"MST region via "+sectionMSTP)
		if regErr != nil {
			return out, regErr
		}
		// Same rule as above: `plan.Region.Name` and `.Revision` are the request, not
		// a reading. The region a site applies is answerable without this echo, from
		// `GET /api/sites/{siteID}/stp-mst-region` (cf. ADR-0021), which is also the
		// only place that can state the three values TOGETHER as the identity they
		// are — a note naming two of the three invited reading the name alone as the
		// region, which is the misconception that splits a fabric.
		if len(plan.Region.InstanceVLANs) > 0 {
			var mstErr = a.applyMSTInstanceVLANs(ctx, plan.Region.InstanceVLANs,
				plan.BridgePriority, &out)
			if mstErr != nil {
				return out, mstErr
			}
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
		var prioErr = a.writeSection(ctx, sectionSTP, "set", bridgeInner,
			"bridge priority via "+sectionSTP)
		if prioErr != nil {
			return out, prioErr
		}
		// `p` is the clamped request, echoed. The transcript carries the write.
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
