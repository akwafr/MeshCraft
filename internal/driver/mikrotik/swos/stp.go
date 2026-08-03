package swos

import (
	"context"
	"fmt"
	"strings"

	"meshcraft/internal/driver"
)

// Compile-time assertion: SwOS Admin reads spanning tree.
var _ driver.SpanningTreeReader = (*Admin)(nil)

// SwOS /rstp.b role byte values, observed on CSS326 captures and matching the
// Role column of the web UI (Disabled / Alternate / Root / Designated / Backup).
const (
	swosRoleDisabled   = 0
	swosRoleAlternate  = 1
	swosRoleRoot       = 2
	swosRoleDesignated = 3
	swosRoleBackup     = 4
)

// ReadSpanningTree reads /rstp.b plus STP globals on /sys.b.
func (a *Admin) ReadSpanningTree(ctx context.Context) (driver.SpanningTreeState, error) {
	var out driver.SpanningTreeState
	var rstp, err = a.Get(ctx, PageRSTP)
	if err != nil {
		return out, fmt.Errorf("read %s: %w", PageRSTP, err)
	}
	var sys *Page
	if sys, err = a.Get(ctx, PageSys); err != nil {
		return out, fmt.Errorf("read %s: %w", PageSys, err)
	}

	out.Mode = "rstp"
	out.Enabled = false
	var names = a.portNames(ctx)
	var i int
	var anyEna bool
	for i = 0; i < len(names); i++ {
		var ena, enaOK = rstp.Bit("ena", i)
		if enaOK && ena {
			anyEna = true
			break
		}
	}
	out.Enabled = anyEna || (!rstp.Has("ena"))
	if prio, ok := sys.Num("prio"); ok {
		out.BridgePriority = int(prio)
	}
	if cost, ok := sys.Num("cost"); ok {
		out.RootPathCost = int(cost)
	}
	if rpr, ok := sys.Num("rpr"); ok {
		out.RootPriority = int(rpr)
	}
	// mac / rmac are 12 hex digits (not ASCII hex-encoded text like brd/id).
	// Text() would decode them to binary garbage in the UI.
	if v, ok := sys.Value("rmac"); ok && v.Kind == KindStr {
		if mac, macOK := DecodeMAC(v.Raw); macOK {
			out.RootBridgeID = mac
			if out.RootPriority > 0 {
				out.RootBridgeID = fmt.Sprintf("%04x.%s", out.RootPriority, out.RootBridgeID)
			}
		}
	}
	if v, ok := sys.Value(FieldMAC); ok && v.Kind == KindStr {
		if mac, macOK := DecodeMAC(v.Raw); macOK {
			out.BridgeID = mac
			if out.BridgePriority > 0 {
				out.BridgeID = fmt.Sprintf("%04x.%s", out.BridgePriority, out.BridgeID)
			}
		}
	}
	if out.BridgeID != "" && out.RootBridgeID != "" {
		out.IsRoot = strings.EqualFold(bareMAC(out.BridgeID), bareMAC(out.RootBridgeID)) &&
			(out.RootPriority == 0 || out.RootPriority == out.BridgePriority)
	}
	// Root path cost 0 with matching MAC is the usual root signal on SwOS.
	if out.RootPathCost == 0 && out.BridgeID != "" && out.RootBridgeID != "" &&
		strings.EqualFold(bareMAC(out.BridgeID), bareMAC(out.RootBridgeID)) {
		out.IsRoot = true
	}

	for i = 0; i < len(names); i++ {
		var ena, enaOK = rstp.Bit("ena", i)
		if enaOK && !ena {
			continue
		}
		var p = driver.SpanningTreePort{Port: names[i]}
		if roles, ok := rstp.Nums("role"); ok && i < len(roles) {
			p.Role = swosRoleName(roles[i])
		}
		var fwd, _ = rstp.Bit("fwd", i)
		var lrn, _ = rstp.Bit("lrn", i)
		switch {
		case fwd:
			p.State = driver.STPStateForwarding
		case lrn:
			p.State = driver.STPStateLearning
		default:
			if p.Role != driver.STPRoleDisabled && p.Role != "" {
				p.State = driver.STPStateDiscarding
			}
		}
		if costs, ok := rstp.Nums("cst"); ok && i < len(costs) && costs[i] > 0 {
			p.PathCost = int(costs[i])
		}
		if rpcs, ok := rstp.Nums("rpc"); ok && i < len(rpcs) && rpcs[i] > 0 {
			p.RootPathCost = int(rpcs[i])
		}
		if edge, ok := rstp.Bit("edge", i); ok {
			// Operational edge (detected), not admin policy.
			p.Edge = edge
			if edge {
				p.EdgeMode = driver.STPEdgeYes
			}
		}
		out.Ports = append(out.Ports, p)
	}

	a.step(true, "spanning tree read",
		fmt.Sprintf("mode %s, root=%v, %d port(s)", out.Mode, out.IsRoot, len(out.Ports)))
	return out, nil
}

func (a *Admin) portNames(ctx context.Context) []string {
	var link, err = a.Get(ctx, PageLink)
	if err != nil {
		return nil
	}
	var ports, sfp, sfpFirst int
	ports, sfp, sfpFirst, err = geometryOf(link)
	if err != nil || ports <= 0 {
		ports = 26
		sfp, sfpFirst = 0, ports
	}
	return NewFacade(ports, sfp, sfpFirst, a.portPrefix()).Names()
}

func swosRoleName(v uint32) string {
	switch v {
	case swosRoleDisabled:
		return driver.STPRoleDisabled
	case swosRoleAlternate:
		return driver.STPRoleAlternate
	case swosRoleRoot:
		return driver.STPRoleRoot
	case swosRoleDesignated:
		return driver.STPRoleDesignated
	case swosRoleBackup:
		return driver.STPRoleBackup
	}
	return ""
}

func bareMAC(id string) string {
	var s = strings.ToLower(id)
	if i := strings.LastIndexByte(s, '.'); i >= 0 {
		s = s[i+1:]
	}
	return strings.ReplaceAll(s, ":", "")
}
