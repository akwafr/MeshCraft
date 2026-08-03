package routeros

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"meshcraft/internal/driver"
)

var _ driver.SpanningTreeReader = (*Client)(nil)

// ReadSpanningTree reads bridge protocol-mode, priority, and per-port roles
// from /interface/bridge and /interface/bridge/port.
func (c *Client) ReadSpanningTree(ctx context.Context) (driver.SpanningTreeState, error) {
	var out driver.SpanningTreeState
	var bridges, err = c.List(ctx, "interface/bridge")
	if err != nil {
		return out, fmt.Errorf("list interface/bridge: %w", err)
	}
	if len(bridges) == 0 {
		out.Notes = append(out.Notes, "no bridge on this device: spanning tree does not apply")
		return out, nil
	}
	var br = bridges[0]
	var mode = strings.ToLower(br.Get("protocol-mode"))
	switch mode {
	case "none", "":
		out.Enabled = false
		out.Notes = append(out.Notes, "bridge protocol-mode is none — spanning tree disabled")
		return out, nil
	case "stp", "rstp", "mstp":
		out.Mode = mode
		out.Enabled = true
	default:
		out.Mode = mode
		out.Enabled = true
		out.Notes = append(out.Notes, "unrecognised protocol-mode '"+mode+"'")
	}
	out.BridgePriority = parseHexOrDec(br.Get("priority"))
	var mac = br.Get("mac-address")
	if mac == "" {
		mac = br.Get("admin-mac")
	}
	if mac != "" && out.BridgePriority > 0 {
		out.BridgeID = fmt.Sprintf("%04x.%s", out.BridgePriority, strings.ToLower(mac))
	} else if mac != "" {
		out.BridgeID = strings.ToLower(mac)
	}

	var ports []Row
	if ports, err = c.List(ctx, "interface/bridge/port"); err != nil {
		return out, fmt.Errorf("list interface/bridge/port: %w", err)
	}
	var p Row
	for _, p = range ports {
		if p.Get("bridge") != "" && br.Get("name") != "" && p.Get("bridge") != br.Get("name") {
			continue
		}
		var name = p.Get("interface")
		if name == "" {
			continue
		}
		var sp = driver.SpanningTreePort{
			Port:     name,
			PathCost: parseHexOrDec(p.Get("path-cost")),
		}
		var edgeRaw = strings.ToLower(p.Get("edge"))
		switch edgeRaw {
		case "yes":
			sp.Edge, sp.EdgeMode = true, driver.STPEdgeYes
		case "no":
			sp.Edge, sp.EdgeMode = false, driver.STPEdgeNo
		case "auto":
			sp.EdgeMode = driver.STPEdgeAuto
			sp.Edge = p.Get("edge-port") == "true"
		default:
			sp.Edge = p.Get("edge-port") == "true" || edgeRaw == "yes"
			if sp.Edge {
				sp.EdgeMode = driver.STPEdgeYes
			}
		}
		var bg = strings.ToLower(p.Get("bpdu-guard"))
		sp.BPDUGuard = bg == "true" || bg == "yes"
		sp.Role = mapROSRole(p.Get("role"))
		switch {
		case p.Get("forwarding") == "true":
			sp.State = driver.STPStateForwarding
		case p.Get("learning") == "true":
			sp.State = driver.STPStateLearning
		case sp.Role != "" && sp.Role != driver.STPRoleDisabled:
			sp.State = driver.STPStateDiscarding
		}
		if rpc := parseHexOrDec(p.Get("root-path-cost")); rpc > 0 {
			sp.RootPathCost = rpc
		}
		out.Ports = append(out.Ports, sp)

		// Root identity from a root-port's designated bridge, or local when we are root.
		if sp.Role == driver.STPRoleRoot {
			if id := p.Get("designated-bridge-id"); id != "" {
				out.RootBridgeID = normalizeROSBridgeID(id)
			}
			if out.RootPathCost == 0 && sp.RootPathCost > 0 {
				out.RootPathCost = sp.RootPathCost
			}
		}
	}

	// Root port absent and protocol running → we are typically the root.
	var hasRootPort bool
	var i int
	for i = 0; i < len(out.Ports); i++ {
		if out.Ports[i].Role == driver.STPRoleRoot {
			hasRootPort = true
			break
		}
	}
	if !hasRootPort && out.Enabled {
		out.IsRoot = true
		out.RootBridgeID = out.BridgeID
		out.RootPriority = out.BridgePriority
		out.RootPathCost = 0
	} else if out.RootBridgeID != "" && out.BridgeID != "" {
		out.IsRoot = bareBridgeMAC(out.RootBridgeID) == bareBridgeMAC(out.BridgeID)
	}

	c.step(true, "spanning tree read",
		fmt.Sprintf("mode %s, root=%v, %d port(s)", out.Mode, out.IsRoot, len(out.Ports)))
	return out, nil
}

func mapROSRole(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "disabled", "disabled-port":
		return driver.STPRoleDisabled
	case "alternate-port", "alternate":
		return driver.STPRoleAlternate
	case "root-port", "root":
		return driver.STPRoleRoot
	case "designated-port", "designated":
		return driver.STPRoleDesignated
	case "backup-port", "backup":
		return driver.STPRoleBackup
	case "master-port", "master":
		return driver.STPRoleMaster
	}
	return ""
}

func parseHexOrDec(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X") {
		var v, err = strconv.ParseInt(s[2:], 16, 32)
		if err != nil {
			return 0
		}
		return int(v)
	}
	var v, err = strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return v
}

func normalizeROSBridgeID(id string) string {
	// "0x8000.88:25:10:17:61:A7" → "8000.88:25:…"
	id = strings.TrimSpace(id)
	id = strings.TrimPrefix(strings.ToLower(id), "0x")
	return id
}

func bareBridgeMAC(id string) string {
	var s = strings.ToLower(id)
	if i := strings.LastIndexByte(s, '.'); i >= 0 {
		s = s[i+1:]
	}
	return strings.ReplaceAll(s, ":", "")
}
