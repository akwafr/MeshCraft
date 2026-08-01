package driver

import (
	"context"
	"strings"
)

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
	// Edge: port currently acts as an edge port (operative or admin).
	Edge bool `json:"edge,omitempty"`
	// EdgeMode: configured edge policy when the platform exposes it —
	// "auto", "yes", or "no". Empty when unknown.
	EdgeMode string `json:"edge_mode,omitempty"`
	// BPDUGuard: admin BPDU Guard on this port (RouterOS bpdu-guard,
	// Instant On CST BPDU Guard). False when off or unknown.
	BPDUGuard bool `json:"bpdu_guard,omitempty"`
}

// Port role / state / edge vocabulary shared by connectors and the UI.
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

	// Per-port edge policy (spanning-tree edge / AdminEdge / bridge edge).
	STPEdgeAuto = "auto"
	STPEdgeYes  = "yes"
	STPEdgeNo   = "no"

	// Feature keys in Capabilities.Features.
	FeatSTPEdge       = "STPEdge"       // can set per-port edge policy
	FeatSTPEdgeAuto   = "STPEdgeAuto"   // edge policy includes « auto » (RouterOS, Instant On)
	FeatSTPPortEnable = "STPPortEnable" // can enable/disable STP per port (SwOS ena, Instant On STP State)
	FeatBPDUGuard     = "BPDUGuard"     // can set per-port BPDU Guard (RouterOS, Instant On — not SwOS)
)

// Site / UI preference vocabulary (Configuration → Spanning-Tree).
const (
	STPPrefMSTP     = "mstp"
	STPPrefRSTPPVST = "rstp-pvst"
	STPPrefRSTP     = "rstp"
	STPPrefSTP      = "stp"
	STPPrefNone     = "none"
)

// STPPreferenceCatalog is the ordered stock of modes the Config dual-list offers.
var STPPreferenceCatalog = []string{
	STPPrefMSTP, STPPrefRSTPPVST, STPPrefRSTP, STPPrefSTP, STPPrefNone,
}

// DefaultSTPPreference is used when the site plan has no preference yet.
var DefaultSTPPreference = []string{
	STPPrefRSTP, STPPrefSTP, STPPrefNone,
}

// SpanningTreePortIntent is what meshcraft asks of ONE port for STP.
type SpanningTreePortIntent struct {
	Port string `json:"port"`
	// Enabled: participate in spanning tree. SwOS maps this to /rstp.b `ena`.
	// Nil leaves the device value alone.
	Enabled *bool `json:"enabled,omitempty"`
	// Edge: "auto", "yes", or "no". Empty leaves the device value alone.
	Edge string `json:"edge,omitempty"`
	// BPDUGuard: enable BPDU Guard on the port. Nil leaves the device alone.
	// RouterOS: /interface/bridge/port bpdu-guard=yes|no.
	// Instant On: CST BpduGuardMode. SwOS: unsupported (skipped with a note).
	BPDUGuard *bool `json:"bpdu_guard,omitempty"`
	PathCost  *int  `json:"path_cost,omitempty"`
}

// SpanningTreePlan is what meshcraft asks a connector to apply for STP.
type SpanningTreePlan struct {
	// Mode: "stp", "rstp", or "mstp". Empty when Enabled is false.
	Mode string `json:"mode,omitempty"`
	// Enabled: false turns spanning tree off on platforms that can.
	Enabled bool `json:"enabled"`
	// BridgePriority: nil leaves the device value alone.
	BridgePriority *int                     `json:"bridge_priority,omitempty"`
	Ports          []SpanningTreePortIntent `json:"ports,omitempty"`
}

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

// SpanningTreeApplier writes spanning-tree configuration to a device.
//
// OPTIONAL. Same pattern as SNMP: declare WritableExtras["stp"] only when this
// is implemented and proven.
type SpanningTreeApplier interface {
	ApplySpanningTree(ctx context.Context, plan SpanningTreePlan) (ApplyResult, error)
}

// AsSpanningTreeApplier returns the applier when Admin implements it.
func AsSpanningTreeApplier(adm Admin) (SpanningTreeApplier, bool) {
	var a, ok = adm.(SpanningTreeApplier)
	return a, ok
}

// NormalizeSTPMode maps a preference key or platform mode onto the connector
// vocabulary used in Capabilities.STPModes ("stp", "rstp", "mstp").
//
// "rstp" and "rstp-pvst" both resolve to driver "rstp": SwOS / Instant On RSTP
// only advertise that capability. Prefer listing plain "rstp" first when the
// fleet is IEEE RSTP; keep "rstp-pvst" when the operator wants PVST-oriented
// priority ahead of plain RSTP on platforms that only speak rstp.
func NormalizeSTPMode(mode string) string {
	var m = strings.ToLower(strings.TrimSpace(mode))
	switch m {
	case STPPrefNone, "off", "disabled":
		return STPPrefNone
	case STPPrefRSTP, STPPrefRSTPPVST, "rapid-stp", "rapid":
		return "rstp"
	case STPPrefMSTP, "mst":
		return "mstp"
	case STPPrefSTP, "802.1d":
		return "stp"
	}
	return m
}

// NormalizeSTPEdge maps UI / JSON edge values onto auto|yes|no.
// Empty input stays empty (meaning « leave alone » on intents).
func NormalizeSTPEdge(raw string) string {
	var m = strings.ToLower(strings.TrimSpace(raw))
	switch m {
	case "":
		return ""
	case STPEdgeAuto, "default":
		return STPEdgeAuto
	case STPEdgeYes, "true", "1", "on", "enable", "enabled", "force":
		return STPEdgeYes
	case STPEdgeNo, "false", "0", "off", "disable", "disabled", "normal":
		return STPEdgeNo
	}
	return m
}

// ResolveSTPMode picks the first site preference that the connector can apply.
//
// disable=true means preference resolved to "none" (turn STP off) AND the
// connector lists "none" in Capabilities.STPModes.
// ok=false means no preference matched capabilities (caller skips apply).
func ResolveSTPMode(preference, caps []string) (mode string, disable bool, ok bool) {
	if len(preference) == 0 {
		preference = DefaultSTPPreference
	}
	var allowed = map[string]bool{}
	var c string
	for _, c = range caps {
		allowed[NormalizeSTPMode(c)] = true
	}
	var pref string
	for _, pref = range preference {
		var n = NormalizeSTPMode(pref)
		if n == STPPrefNone {
			if allowed[STPPrefNone] {
				return "", true, true
			}
			continue
		}
		if allowed[n] {
			return n, false, true
		}
	}
	return "", false, false
}

// SanitizeSTPPreference keeps known catalog keys, de-duplicates, preserves order.
func SanitizeSTPPreference(in []string) []string {
	var known = map[string]bool{}
	var k string
	for _, k = range STPPreferenceCatalog {
		known[k] = true
	}
	var seen = map[string]bool{}
	var out []string
	var v string
	for _, v = range in {
		var n = strings.ToLower(strings.TrimSpace(v))
		if !known[n] || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	return out
}
