package mdpv1

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
	// FeatSTPSuperRoot: the connector KNOWS HOW to express SpanningTreePlan.SuperRoot.
	//
	// SEPARATE FROM WritesExtra("stp"), and conflating the two was a real hazard
	// rather than a tidiness point. WritesExtra says "this connector's STP writes
	// are declared"; this says "this connector implements THIS field". cisco/ios
	// has the first and not the second — it declares stp and deliberately does not
	// implement Super Root — so a UI gated on writability alone offered the
	// checkbox on a live Catalyst, where ticking it did nothing and UN-ticking it
	// would have written a bridge priority to a production switch through the
	// clear path. A control must be offered only where BOTH are true.
	FeatSTPSuperRoot = "STPSuperRoot"
	// FeatMSTRegion: the connector KNOWS HOW to write [SpanningTreePlan.Region] —
	// the name, the revision AND the VLAN-to-instance table.
	//
	// SEPARATE FROM WritesExtra("stp") AND FROM Capabilities.STPModes, and keeping
	// the three apart is the whole of ADR-0022. The first says "this connector's
	// STP writes are declared"; the second says "this is what the RANGE runs" — a
	// fact about hardware; this one says "this CODE emits a region". fs-s3400 has
	// the first and not this one, and the gap was measured rather than argued: it
	// declares stp, its STPModes resolve the site to rstp, the plan arrives WITH a
	// configured region, the connector writes the bridge and says nothing — so
	// sw01-arena sat in ARENA carrying `649D9929F706`, its own bridge MAC, as a
	// region name. A region of one, with a clean ApplyResult behind it.
	//
	// Deduced from STPModes containing "mstp" it would be true today by
	// COINCIDENCE: nothing stops a connector from running the mode line and
	// nothing else, and it would then be advertised as able to join a region.
	//
	// ABSENT MEANS "HAS NOT DECLARED IT", which is the safe default, but it is not
	// itself a refusal. A connector that receives a region it cannot write must
	// still SAY so in [ApplyResult].Skipped, naming the region: the capability
	// gates a UI, the Skipped line is what proves the operator was told.
	FeatMSTRegion = "MSTRegion"
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

// GenericMSTRegionName builds a region name from a site IDENTIFIER, which is the
// only thing about a site that cannot be renamed.
//
// # Why not something readable (cf. ADR-0021)
//
// A region name is not a label, it is one third of an IDENTITY: 802.1s compares
// name, revision and the VLAN-table digest, and a device whose name differs is
// not a device with a different caption — it has LEFT the region, silently (cf.
// [MSTRegion]). So the one property a region name must have is that nothing ever
// changes it, and the one property it must NOT have is looking like something
// worth tidying up.
//
// Deriving it from the site slug — what this package did before — looked safe
// because a slug is unique per site. It is not STABLE: a slug is operator-
// editable, and on the ARENA fleet a site rename left seven switches on the old
// region name and one on the new, which is a split fabric nobody saw. A site's
// UUID is its primary key: no route edits it.
//
// Sixteen characters (`mst-` plus twelve hex digits), well under the 32-character
// limit every surveyed platform enforces. Non-hex characters are dropped so the
// result is the same whether the caller passes a dashed UUID or a bare one. The
// `mst-` prefix is kept so a human reading a switch sees a meshcraft-managed
// region rather than a factory name derived from the bridge MAC.
//
// Returns "" for an identifier with no hex content: a nameless region is not a
// region, and [MSTRegion.Configured] refuses it rather than applying one.
func GenericMSTRegionName(siteID string) string {
	var hex = make([]rune, 0, 12)
	var r rune
	for _, r = range siteID {
		if len(hex) == 12 {
			break
		}
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') {
			hex = append(hex, r)
			continue
		}
		if r >= 'A' && r <= 'F' {
			hex = append(hex, r+('a'-'A'))
		}
	}
	if len(hex) == 0 {
		return ""
	}
	return "mst-" + string(hex)
}

// SiteMSTRegion derives the region from what the SITE already knows, so that
// every device computes the same digest without anyone typing anything.
//
// THIS IS THE WHOLE MECHANISM by which regions stay homogeneous. The alternative
// — an operator-entered name, revision and map per device — is how MST is
// normally run and is exactly how it normally breaks: a value typed twice is a
// value typed differently once.
//
//   - name: decided by the CALLER, not here. It used to be the site slug, and
//     that was the bug ADR-0021 corrects: a slug is renameable, and renaming a
//     region evicts every device carrying the old name. The caller owns the
//     pinned-name-or-[GenericMSTRegionName] policy because the pin lives in the
//     site plan, which this package knows nothing about;
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
func SiteMSTRegion(regionName string, revision int, vlanIDs []int) MSTRegion {
	var name = strings.TrimSpace(regionName)
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
	BridgePriority *int `json:"bridge_priority,omitempty"`
	// SuperRoot asks this device to WIN the root election of its MST region and
	// to keep winning it.
	//
	// # What it means
	//
	// Priority 0 on MST instance 0 — the lowest value 802.1D can encode, since
	// the priority lives in the top four bits of a 16-bit field and the step is
	// 4096. Nothing sorts below it. On platforms that have a dedicated keyword
	// for the idea (Arista EOS spells it "spanning-tree root super") the
	// connector emits that too, AFTER the priority, so a platform that does not
	// know the keyword still lands the half that matters.
	//
	// # Relation to BridgePriority
	//
	// SUPERROOT OVERRIDES IT. The two cannot both be honoured — one says "be 0",
	// the other names a number — and a connector that receives both must apply
	// SuperRoot and SAY in ApplyResult.Notes which one it obeyed. Silently
	// picking either would leave an operator reading a priority on screen that
	// no longer describes the switch.
	//
	// # Scope
	//
	// Meaningful only when Mode is "mstp", the same rule as Region: outside MST
	// there is no instance 0 to be root of, and the per-VLAN modes would need
	// the priority written across the whole VLAN range to mean the same thing.
	// Connectors that cannot carry it — every one but Arista EOS today — ignore
	// it and SAY so in ApplyResult.Skipped rather than dropping it silently,
	// because "the box is checked and nothing happened" is the failure this
	// whole contract exists to make visible.
	//
	// # What it costs, which the caller must carry
	//
	// At priority 0 the root election tie-break falls to the MAC ADDRESS. Two
	// SuperRoot switches in one region therefore elect the lower MAC, and the
	// operator has no knob left short of un-checking one of them — there is
	// nothing below 0 to separate them with. For the same reason the root can no
	// longer be MOVED by raising anyone else's priority: the usual repair
	// ("make the other one 4096") does not work against a bridge that is already
	// at the floor.
	//
	// And there is NO RECORDED UNDO. meshcraft never stored what the priority
	// was before, so clearing this flag cannot restore it; the caller that
	// clears it must supply BridgePriority in the same plan (cf.
	// internal/api/stp_apply.go, which refuses a bare clear for that reason).
	//
	// NOT per-port. It used to say "and not per-instance" too; see
	// [SpanningTreePlan.RootInstances], which supersedes that.
	SuperRoot bool `json:"super_root,omitempty"`
	// RootInstances names the MST instances this device must be root of.
	//
	// # Why the boolean above was not enough
	//
	// [SuperRoot] can only ever mean instance 0, because a boolean has nowhere to
	// put an instance id. Under MSTP a region carries several instances, each with
	// its own tree and its own root — that is what MST is FOR — so a switch being
	// "the root" is an incomplete statement until it says root of WHAT. The
	// boolean quietly made the feature useless on exactly the topologies MST
	// exists to serve.
	//
	// Empty with SuperRoot true keeps the old meaning (the single tree, or the
	// CIST), so nothing that already sets the boolean changes behaviour.
	//
	// # BPDUFilterPorts must be written FIRST
	//
	// When two devices are root of the SAME instance, the trees only stay
	// separate because BPDUs are filtered on the path between them. The order is
	// the safety property and it is stated in [RootApplyOrder]: a connector that
	// wrote the root priority before the filter would have two bridges contesting
	// one election while BPDUs still flow, and the tree would reconverge in the
	// middle of meshcraft's own apply.
	RootInstances []int `json:"root_instances,omitempty"`
	// BPDUFilterPorts are the ports on THIS device that must stop processing
	// BPDUs, computed site-wide by [InterRootFilterPorts].
	//
	// APPLIED BEFORE ANY ROOT PRIORITY IN THE SAME PLAN. Not a preference — see
	// [RootApplyOrder] and the header of internal/driver/rootbridge.go.
	//
	// This is the only control in this contract that can CAUSE a broadcast storm
	// rather than prevent one: a filtered port forwards traffic while taking no
	// part in the spanning tree, so a loop through it is a loop nothing is
	// watching. It is emitted only when the operator has designated a second root
	// and thereby asserted that no redundant path exists between them
	// ([RootWarning] states that assertion in the words the operator sees).
	//
	// A connector whose platform cannot express BPDU filtering MUST REFUSE the
	// whole plan rather than apply the root half — applying half of this is the
	// contested-election state the ordering exists to avoid, made permanent.
	BPDUFilterPorts []string                 `json:"bpdu_filter_ports,omitempty"`
	Ports           []SpanningTreePortIntent `json:"ports,omitempty"`
}

// RootsOf returns the instances this plan asks the device to win, folding the
// legacy [SpanningTreePlan.SuperRoot] boolean into the per-instance list.
//
// One place to ask the question, so a connector cannot honour one form and
// ignore the other — which would make the feature work or not depending on which
// field the caller happened to set.
func (p SpanningTreePlan) RootsOf() []int {
	if len(p.RootInstances) > 0 {
		return p.RootInstances
	}
	if !p.SuperRoot {
		return nil
	}
	// AN UNQUALIFIED DESIGNATION MEANS THE WHOLE REGION, NOT THE CIST ALONE, and
	// returning []int{0} here was a false configuration waiting for its first MST
	// region.
	//
	// It used to return the CIST unconditionally — "which is what the boolean
	// always meant" — and that was true only while nothing mapped VLANs into
	// instances. [SiteMSTRegion] does exactly that: it puts every site VLAN above
	// 1 into INSTANCE 1 and leaves the CIST holding what nobody mapped. So the two
	// halves of one apply disagreed. Measured on the lab site 2026-08-12, with
	// core01.lab designated root and no instance named:
	//
	//	instance 1 vlan 42,98-100     <- every VLAN in use, management (99) included
	//	spanning-tree mst 0 priority 0 <- the only tree this switch would win
	//
	// The operator designates "the root bridge" and gets a switch that is root of
	// a tree carrying nothing they care about, while instance 1 — holding the
	// management VLAN — elects its root by LOWEST MAC ADDRESS, i.e. by accident.
	// Nothing on any screen would contradict it: the designation is stored, the
	// apply succeeds, the priority reads back as 0.
	//
	// So an empty list means every tree the region defines, plus the CIST, which
	// is what "root of this region" means to the person who ticked it. A caller
	// that wants the CIST alone NAMES instance 0 — the distinction RootIntent's
	// own comment asks for, now actually available.
	var out = []int{0}
	var inst int
	for inst = range p.Region.InstanceVLANs {
		if inst == 0 || len(p.Region.InstanceVLANs[inst]) == 0 {
			continue
		}
		out = append(out, inst)
	}
	sortInts(out)
	return out
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
	// "rapid-pvst" is what ARISTA EOS and Cisco IOS both print for RSTP
	// per VLAN, and it was missing here while the fleet's own spelling
	// ("rstp-pvst") was present. A mode that falls through this function
	// unrecognised is returned VERBATIM, so an EOS reporting "rapid-pvst"
	// produced a mode string no UI label and no drift check matches — the
	// device looked like it was running something nobody had heard of.
	case STPPrefRSTP, STPPrefRSTPPVST, "rapid-pvst", "rapid-stp", "rapid":
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
//
// # AN EMPTY PREFERENCE RESOLVES TO NOTHING, and that is the per-site OFF SWITCH
//
// This function substituted [DefaultSTPPreference] on `len(preference) == 0`
// until 2026-08-09, and doing so erased the only distinction the site plan
// makes about spanning tree. api.stpPreferenceOf answers the two cases
// DIFFERENTLY on purpose — an ABSENT `stp_mode_preference` key already comes
// back as [DefaultSTPPreference], while an EXPLICITLY EMPTY array comes back
// empty and means "everything stays Available", i.e. leave the device's tree
// alone. Re-substituting the default here collapsed the second into the first,
// so an operator who cleared the list still had rstp pushed at every apply.
//
// That is not a cosmetic difference. cisco/ios has no read-compare no-op path:
// every apply re-sends `spanning-tree mode <x>`, which IOS treats as a no-op
// when it already matches — but on a Catalyst deliberately left on
// (rapid-)PVST+ mid-migration, under a site whose preference resolves to
// mstp/rstp, ANY port change flips the mode and reconverges the tree on a live
// network. Clearing the site's preferred list is how that operator says "not
// yet", and it now works.
//
// THE DEFAULT IS THE CALLER'S TO APPLY, not this function's: substituting a
// product default is a decision about a MISSING setting, and only the code that
// read the setting knows whether it was missing or emptied.
func ResolveSTPMode(preference, caps []string) (mode string, disable bool, ok bool) {
	if len(preference) == 0 {
		return "", false, false
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
