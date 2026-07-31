// Package recon compares what meshcraft WANTS to what the device CARRIES.
//
// This is the "Diff" step of the reconciliation pipeline
// (docs/01-Architecture/Reconciliation section 3), and section 6 "Verify &
// drift". Everything here is pure: two states enter, a list of gaps exits. This
// decision drives an indicator that tells the operator whether the fleet is
// compliant — making it testable without a database or device is not a luxury.
package recon

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Sync is a device's synchronization state.
type Sync string

const (
	// SyncUnknown : the device has never been re-read. We do not claim to know.
	SyncUnknown Sync = "unknown"
	// SyncOK : what is wanted is what is.
	SyncOK Sync = "in_sync"
	// SyncPending : meshcraft holds a configuration not yet on the device. This
	// is the normal case right after a change.
	SyncPending Sync = "pending"
	// SyncDrift : the device was changed OUTSIDE meshcraft. This is the case that
	// requires human arbitration — reapply, or absorb.
	SyncDrift Sync = "drift"
)

// DefaultVLAN is VLAN 1: present by default on every site and every device, and
// the implicit native VLAN of any trunk that does not designate another.
const DefaultVLAN = 1

// PortIntent is the CONFIGURED part of a port.
//
// Link speed, link state, and measured PoE draw are not part of it: they are
// measurements, not intentions. Comparing them would make the whole fleet blink
// constantly, and an indicator that always blinks means nothing.
//
// Configuration intentions that are NOT VLAN membership (desired PoE,
// spanning-tree…) are not here either, but for another reason: they exist in the
// UI and are stored, simply no connector knows how to write them yet. They are
// read with RequestedExtras, and told to the operator instead of being hidden.
type PortIntent struct {
	Mode   string
	PVID   int
	Tagged []int
	// TaggedAll : intent was the string "all". After ResolveTaggedAll, Tagged is
	// the EXACT set of site VLANs (minus PVID) the driver must place — add and
	// remove until the device matches. The device has no notion of "all VLANs";
	// its interface wants an enumeration, and the site plan is that enumeration.
	//
	// While the site declares no VLANs, Resolve leaves Tagged empty and "all"
	// stays unresolved: diff claims nothing and apply must not strip tagged
	// membership (cf. TaggedAtLeast on the apply bridge).
	TaggedAll bool
}

// portJSON is the form stored in `devices.ports`.
type portJSON struct {
	Mode   string          `json:"mode"`
	PVID   int             `json:"pvid"`
	Tagged json.RawMessage `json:"tagged"`
}

// ParsePorts reads a port map in stored format. Telemetry fields are ignored by
// construction: they are not in portJSON.
func ParsePorts(raw json.RawMessage) map[string]PortIntent {
	var out = map[string]PortIntent{}
	if len(raw) == 0 {
		return out
	}
	var m map[string]portJSON
	if json.Unmarshal(raw, &m) != nil {
		return out
	}
	var name string
	var p portJSON
	for name, p = range m {
		var pi = PortIntent{Mode: p.Mode, PVID: p.PVID}
		// A trunk without PVID is not a trunk without a native VLAN: hardware
		// places VLAN 1 there. Leaving 0 would make intent diverge from what the
		// device will always report, therefore permanent false drift — and an
		// apply that would remove the native VLAN on every pass.
		if pi.Mode == "trunk" && pi.PVID == 0 {
			pi.PVID = DefaultVLAN
		}
		// `tagged` is either a VLAN list or the string "all".
		var all string
		if json.Unmarshal(p.Tagged, &all) == nil {
			pi.TaggedAll = all == "all"
		} else {
			var ids []int
			if json.Unmarshal(p.Tagged, &ids) == nil {
				sort.Ints(ids)
				pi.Tagged = ids
			}
		}
		out[name] = pi
	}
	return out
}

// ResolveTaggedAll gives "all" the only meaning it can have: the VLANs the SITE
// declares.
//
// Without this resolution, apply simply dropped ports in "all" — silently, without
// even counting them among spared ports. A trunk configured in the UI never
// reached the device and nothing said so.
//
// PVID is excluded from the enumeration: the device reports as tagged only ports
// from `taggedPorts`, never its native VLAN (cf.
// instanton.portsFromMembership). Leaving it in would fabricate a permanent gap
// that even a successful apply could not clear.
//
// A site WITHOUT declared VLANs leaves "all" unresolved, empty list. That is
// deliberate: an empty enumeration does not mean "strip every tagged VLAN", it
// means we do not know yet. Diff claims nothing and apply must not manage
// tagged membership until the site plan exists.
func ResolveTaggedAll(p PortIntent, siteVLANs []int) PortIntent {
	if !p.TaggedAll {
		return p
	}
	var out = p
	out.Tagged = nil
	var seen = map[int]bool{}
	var v int
	for _, v = range siteVLANs {
		// Native is not tagged, and reserved bounds are not set.
		if v == p.PVID || v < 1 || v > 4092 || seen[v] {
			continue
		}
		seen[v] = true
		out.Tagged = append(out.Tagged, v)
	}
	sort.Ints(out.Tagged)
	return out
}

// Resolve applies ResolveTaggedAll to an entire port map.
//
// Called by APPLY as by DIFF, and that is the important point: if the two did not
// resolve "all" the same way, apply would write a list that diff would
// immediately report as drift, forever.
func Resolve(desired map[string]PortIntent, siteVLANs []int) map[string]PortIntent {
	var out = make(map[string]PortIntent, len(desired))
	var name string
	var p PortIntent
	for name, p = range desired {
		out[name] = ResolveTaggedAll(p, siteVLANs)
	}
	return out
}

// ExtraIntents lists port configuration intentions OTHER than VLAN membership,
// as they appear in `devices.ports`.
//
// Not all are offered in the UI yet — declaring them early costs nothing and
// avoids a new checkbox becoming silent again.
//
// `label` is NOT listed, and that is not an omission: a port description is a
// meshcraft annotation, not configuration expected on the device. Signaling it as
// "not applied" would fill the banner with noise on every documented port, and
// teach people not to read it.
var ExtraIntents = []string{"poe", "stp", "dhcp_snooping", "port_security"}

// extraLabels names an intention for the operator, who does not read JSON keys.
var extraLabels = map[string]string{
	"poe":           "PoE",
	"stp":           "spanning-tree",
	"dhcp_snooping": "DHCP snooping",
	"port_security": "port security",
}

// ExtraLabel returns the readable name of an intention, or the key if unknown —
// a raw key displayed beats a hidden intention.
func ExtraLabel(name string) string {
	var label, seen = extraLabels[name]
	if !seen {
		return name
	}
	return label
}

// RequestedExtras returns, for each non-VLAN intention, the ports that REQUEST
// it. This allows saying "desired PoE on these three ports was not applied"
// instead of letting people believe everything is in place.
//
// Only POSITIVE values count (`poe: true`, not `poe: false`). Profiles write
// intention on both sides, and counting neutral values would flag nearly the
// whole fleet — a banner listing everything says nothing. The tradeoff is
// assumed: explicitly CUT PoE is not written by the connector either, and is
// not reported here.
//
// An unmanaged port ("libre", or without mode) is ignored: meshcraft claims
// nothing on a port it does not manage.
func RequestedExtras(raw json.RawMessage) map[string][]string {
	var out = map[string][]string{}
	if len(raw) == 0 {
		return out
	}
	var m map[string]map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return out
	}
	var names = make([]string, 0, len(m))
	var name string
	for name = range m {
		names = append(names, name)
	}
	// Stable order: the banner must not reorder its ports on every read.
	sort.Strings(names)

	for _, name = range names {
		var fields = m[name]
		var mode string
		_ = json.Unmarshal(fields["mode"], &mode)
		if mode == "" || mode == "libre" {
			continue
		}
		var key string
		for _, key = range ExtraIntents {
			var v, present = fields[key]
			if !present || !positiveIntent(v) {
				continue
			}
			out[key] = append(out[key], name)
		}
	}
	return out
}

// positiveIntent says whether a JSON value expresses a request, not a default.
// `false`, `0`, `""` and `null` are neutral values: they add no requirement
// beyond what the platform already does.
func positiveIntent(raw json.RawMessage) bool {
	var b bool
	if json.Unmarshal(raw, &b) == nil {
		return b
	}
	var n float64
	if json.Unmarshal(raw, &n) == nil {
		return n != 0
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s != "" && s != "off" && s != "disabled"
	}
	return false
}

// Change is ONE gap, expressed as the spec expects: `port.GE4.pvid: 20 -> 30`.
type Change struct {
	Port     string `json:"port"`
	Field    string `json:"field"`
	Desired  string `json:"desired"`
	Observed string `json:"observed"`
}

// Label renders the gap as displayed.
func (c Change) Label() string {
	return "port." + c.Port + "." + c.Field + ": " + c.Desired + " \u2192 " + c.Observed
}

// vlanList renders a VLAN list as displayed.
func vlanList(ids []int) string {
	if len(ids) == 0 {
		return "-"
	}
	var parts = make([]string, 0, len(ids))
	var v int
	for _, v = range ids {
		parts = append(parts, strconv.Itoa(v))
	}
	return strings.Join(parts, ",")
}

// joinVLANs renders a port's tagged membership.
//
// "all" is displayed WITH its enumeration: facing a gap, the question the
// operator asks is "what does meshcraft actually require?", and "all" alone does
// not answer.
func joinVLANs(p PortIntent) string {
	if p.TaggedAll {
		if len(p.Tagged) == 0 {
			return "all (no VLANs declared on the site)"
		}
		return "all (" + vlanList(p.Tagged) + ")"
	}
	return vlanList(p.Tagged)
}

// lackingVLANs returns VLANs from `want` that `have` does not carry.
func lackingVLANs(want, have []int) []int {
	var seen = map[int]bool{}
	var v int
	for _, v = range have {
		seen[v] = true
	}
	var out []int
	for _, v = range want {
		if !seen[v] {
			out = append(out, v)
		}
	}
	return out
}

// unmanaged : a port meshcraft does not configure. "absent = unmanaged"
// (Reconciliation section 3) — meshcraft does not claim ownership of what it
// never set, and therefore does not report drift on a port it ignores.
func unmanaged(p PortIntent, known bool) bool {
	return !known || p.Mode == "" || p.Mode == "libre"
}

// DiffPorts compares desired to observed, port by port.
//
// The "absent = unmanaged" rule applies in ONE direction only: a port meshcraft
// does not manage is ignored, but a port it manages that the device no longer
// carries is a real gap.
func DiffPorts(desired, observed map[string]PortIntent) []Change {
	var out = []Change{}
	var names = make([]string, 0, len(desired))
	var name string
	for name = range desired {
		names = append(names, name)
	}
	// Stable order: a diff whose order changes on every read is unreadable, and
	// would suggest change where there is none.
	sort.Strings(names)

	for _, name = range names {
		var want = desired[name]
		if unmanaged(want, true) {
			continue
		}
		var got, seen = observed[name]
		if !seen {
			out = append(out, Change{Port: name, Field: "presence",
				Desired: want.Mode, Observed: "port unknown to device"})
			continue
		}
		if want.Mode != got.Mode {
			out = append(out, Change{Port: name, Field: "mode",
				Desired: want.Mode, Observed: orDash(got.Mode)})
		}
		if want.PVID != got.PVID {
			out = append(out, Change{Port: name, Field: "pvid",
				Desired: strconv.Itoa(want.PVID), Observed: strconv.Itoa(got.PVID)})
		}
		// "all" resolved to the site VLAN list is an EXACT set: extras on the
		// device are drift, and apply removes them. Unresolved "all" (empty site
		// plan) claims nothing — stripping tagged membership would be guessing.
		// Compare the ID lists, not joinVLANs display strings ("all (20,30)" ≠ "20,30").
		if want.TaggedAll && len(want.Tagged) == 0 {
			continue
		}
		if vlanList(want.Tagged) != vlanList(got.Tagged) {
			out = append(out, Change{Port: name, Field: "tagged vlans",
				Desired: joinVLANs(want), Observed: joinVLANs(got)})
		}
	}
	return out
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// State decides the indicator color.
//
//   - never re-read                         -> we do not know, and we say so;
//   - no gap                                -> compliant;
//   - never set by meshcraft                -> IN PROGRESS (blue);
//   - meshcraft change not pushed           -> IN PROGRESS (blue);
//   - gap without meshcraft change          -> DRIFT (orange).
//
// Attribution relies on timestamps rather than guessing: if intent has not moved
// since the last apply, then the DEVICE changed. That is the only case requiring
// human arbitration, and therefore the only one worth interrupting the operator.
//
// The "never set" case deserves its own test, because it is the majority on a
// fleet just adopted: WE DO NOT DRIFT FROM WHAT WE NEVER SET. Without this rule,
// every freshly discovered device would signal as modified by a third party — and
// the operator's first reflex would be to silence an alert that screams wrongly.
func State(observedAt, desiredChangedAt, appliedAt *time.Time, changes []Change) Sync {
	if observedAt == nil {
		return SyncUnknown
	}
	if len(changes) == 0 {
		return SyncOK
	}
	if appliedAt == nil {
		return SyncPending
	}
	if desiredChangedAt != nil && desiredChangedAt.After(*appliedAt) {
		return SyncPending
	}
	return SyncDrift
}
