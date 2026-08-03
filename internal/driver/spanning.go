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
	// Region is the MST region the device reports being in. Read back because a
	// region that does not match is invisible any other way: 802.1s handles a
	// mismatch by silently treating the neighbour as an ordinary RSTP bridge, so
	// comparing the intended region with the observed one is the only place the
	// divergence can be caught (cf. MSTRegion).
	Region MSTRegion `json:"region,omitempty"`
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
	FeatSTPEdgeAuto   = "STPEdgeAuto"   // edge policy includes "auto" (RouterOS, Instant On)
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

// MSTRegion is the MST region three switches must agree on, character for
// character, to be in the SAME region.
//
// # Why this is a type and not three loose fields
//
// 802.1s does not decide region membership by asking. Each bridge advertises a
// DIGEST: an MD5 over the full 4096-entry VLAN-to-instance table, sent with the
// region name and revision. Two bridges are in one region only if all three match
// EXACTLY. Anything else — one VLAN mapped differently, a revision off by one, a
// name with different capitalisation — and they are in different regions, which
// they handle by falling back to the CST as if the neighbour were an ordinary
// RSTP bridge.
//
// That failure is SILENT. Nothing errors, no link goes down, no log line appears;
// the topology simply stops behaving the way the design says it does, and the
// only symptom is a blocked port somewhere nobody expected. Keeping the three
// values in one type, computed in one place ([SiteMSTRegion]), is what makes it
// possible to say they are identical rather than hope so.
//
// # The operational consequence, which the caller must carry
//
// The digest covers EVERY VLAN, so the region stays coherent only while every
// member is pushed the same map. Add a VLAN to the site and any switch not
// re-pushed leaves the region — still forwarding, still green, quietly no longer
// part of the same tree. A caller that changes the site VLAN list must re-apply
// STP fleet-wide, and this is the reason.
type MSTRegion struct {
	// Name is the region name, up to 32 characters on every platform surveyed.
	Name string `json:"name,omitempty"`
	// Revision is the region revision level (0-65535). It is NOT a version to
	// bump for bookkeeping: changing it on one device removes that device from
	// the region.
	Revision int `json:"revision"`
	// InstanceVLANs maps MST instance id → the VLANs it carries. Instance 0 (the
	// CIST) is implicit and never listed: it holds every VLAN not named here.
	//
	// A map and not a list of pairs because the same VLAN cannot be in two
	// instances, and a shape that can express the impossible invites it.
	InstanceVLANs map[int][]int `json:"instance_vlans,omitempty"`
}

// Configured reports whether a region is worth applying. A nameless region is
// not a region: the default name on most platforms is derived from the bridge
// MAC, which makes every device its own region.
func (r MSTRegion) Configured() bool {
	return strings.TrimSpace(r.Name) != ""
}

// SiteMSTRegion derives the region from what the SITE already knows, so that
// every device computes the same digest without anyone typing anything.
//
// THIS IS THE WHOLE MECHANISM by which regions stay homogeneous. The alternative
// — an operator-entered name, revision and map per device — is how MST is
// normally run and is exactly how it normally breaks: a value typed twice is a
// value typed differently once.
//
//   - name: the site slug. Stable, unique per site, already validated to a safe
//     character set, and meaningful when read off a switch;
//   - revision: supplied by the caller from the site plan, so a deliberate
//     region change is possible without renaming;
//   - map: every site VLAN into instance 1, everything else left in the CIST.
//     One instance means one tree for the site — no per-VLAN load balancing,
//     which needs redundant paths to be worth anything and must be identical
//     everywhere to be safe at all.
//
// VLANs are SORTED and deduplicated, and VLAN 1 is excluded: it belongs to the
// CIST on every platform and mapping it into an instance is refused by some and
// silently ignored by others — either way the digests would stop matching.
func SiteMSTRegion(siteSlug string, revision int, vlanIDs []int) MSTRegion {
	var name = strings.TrimSpace(siteSlug)
	if len(name) > 32 {
		name = name[:32]
	}
	var seen = map[int]bool{}
	var members []int
	var v int
	for _, v = range vlanIDs {
		if v <= 1 || v > 4094 || seen[v] {
			continue
		}
		seen[v] = true
		members = append(members, v)
	}
	sortInts(members)
	var region = MSTRegion{Name: name, Revision: revision}
	if len(members) > 0 {
		region.InstanceVLANs = map[int][]int{1: members}
	}
	return region
}

// SpanningTreePlan is what meshcraft asks a connector to apply for STP.
type SpanningTreePlan struct {
	// Region is the MST region to place the device in. Meaningful only when Mode
	// is "mstp"; connectors that cannot carry a region ignore it and SAY so in
	// their ApplyResult, so a boundary device is visible rather than assumed
	// (cf. ApplyResult.Skipped).
	Region MSTRegion `json:"region,omitempty"`
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
// Empty input stays empty (meaning "leave alone" on intents).
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

// sortInts sorts in place, ascending. A local helper rather than a sort import
// in a file that otherwise needs none.
func sortInts(v []int) {
	var i, j int
	for i = 1; i < len(v); i++ {
		var x = v[i]
		for j = i; j > 0 && v[j-1] > x; j-- {
			v[j] = v[j-1]
		}
		v[j] = x
	}
}
