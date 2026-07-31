package driver

import "context"

// SpanningTreeState is what a device reports about 802.1D/w/s — mode, bridge
// weight, root identity, and per-port role/cost. READ-ONLY observation for the
// topology Spanning-Tree view; never mixed into VLAN recon.
type SpanningTreeState struct {
	// Mode: "stp", "rstp", "mstp", or empty when unknown / disabled.
	Mode string `json:"mode,omitempty"`
	// Enabled: false when the platform has spanning tree turned off.
	Enabled bool `json:"enabled"`
	// BridgePriority: configured bridge priority (device weight), e.g. 32768.
	BridgePriority int `json:"bridge_priority,omitempty"`
	// BridgeID: local bridge identifier when the platform exposes it (often
	// priority+MAC). Empty when unknown.
	BridgeID string `json:"bridge_id,omitempty"`
	// RootBridgeID: who the device believes is the root. Empty when unknown.
	RootBridgeID string `json:"root_bridge_id,omitempty"`
	// RootPriority: priority of the claimed root, when separate from RootBridgeID.
	RootPriority int `json:"root_priority,omitempty"`
	// IsRoot: this device is the root bridge of its tree (or MST instance 0).
	IsRoot bool `json:"is_root"`
	// RootPathCost: cost from this bridge to the root (0 when IsRoot).
	RootPathCost int `json:"root_path_cost,omitempty"`
	// Ports: per-port role and cost. Ports without STP (edge, disabled) may be omitted.
	Ports []SpanningTreePort `json:"ports,omitempty"`
	Notes []string           `json:"notes,omitempty"`
}

// SpanningTreePort is one port's place in the tree.
type SpanningTreePort struct {
	// Port: facade name (ether1, swos1, …) — same vocabulary as State.Ports.
	Port string `json:"port"`
	// Role: "disabled", "alternate", "root", "designated", "backup", "master",
	// or empty when the platform does not say.
	Role string `json:"role,omitempty"`
	// State: "discarding", "learning", "forwarding", or empty.
	State string `json:"state,omitempty"`
	// PathCost: configured (or operative) path cost of this port.
	PathCost int `json:"path_cost,omitempty"`
	// RootPathCost: cost to root through this port, when exposed.
	RootPathCost int `json:"root_path_cost,omitempty"`
	// Edge: port is (or acts as) an edge port.
	Edge bool `json:"edge,omitempty"`
}

// Port role / state vocabulary shared by connectors and the UI.
const (
	STPRoleDisabled    = "disabled"
	STPRoleAlternate   = "alternate"
	STPRoleRoot        = "root"
	STPRoleDesignated  = "designated"
	STPRoleBackup      = "backup"
	STPRoleMaster      = "master"
	STPStateDiscarding = "discarding"
	STPStateLearning   = "learning"
	STPStateForwarding = "forwarding"
)

// SpanningTreeReader reads spanning-tree observation from a device.
//
// OPTIONAL. Platforms that do not speak STP leave it unimplemented; callers
// use [AsSpanningTreeReader] before offering the topology Spanning-Tree view
// for that device.
type SpanningTreeReader interface {
	ReadSpanningTree(ctx context.Context) (SpanningTreeState, error)
}

// AsSpanningTreeReader returns the reader when Admin implements it.
func AsSpanningTreeReader(adm Admin) (SpanningTreeReader, bool) {
	var r, ok = adm.(SpanningTreeReader)
	return r, ok
}
