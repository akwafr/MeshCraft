// Package tagging decides which tags a rule places on a device.
//
// Everything that decides is here, and everything that decides is PURE: no
// database, no network, no clock. A tagging rule can move a device from one admin
// credential to another (cf. docs/01-Architecture/Etiquettes et portee des
// identifiants) — getting the side wrong is therefore not a cosmetic detail, and
// logic testable in a table beats a SQL query.
package tagging

import (
	"net/netip"
	"strings"
)

// Match : a rule's criteria. Those that are set are combined with AND.
//
// The first three are compared as case-insensitive SUBSTRINGS, not regular
// expressions. Compared values are vendor labels — "HPE Networking Instant On
// Switch 48p Gigabit 4p SFP+ 1930 JL685A" — that an operator recognizes at a
// glance but cannot turn into a regex. And a wrong regex does not say it is
// wrong: it matches nothing.
type Match struct {
	Vendor   string `json:"vendor,omitempty"`
	Model    string `json:"model,omitempty"`
	Location string `json:"location,omitempty"`
	// IPPrefix is a CIDR prefix (IPv4 or IPv6) compared to the management address.
	IPPrefix string `json:"ip_prefix,omitempty"`
}

// Empty says no criterion is set.
//
// An empty rule applies to NOTHING. The opposite would make a half-filled row a
// rule that tags the whole fleet, meaning it silently reassigns a password.
func (m Match) Empty() bool {
	return strings.TrimSpace(m.Vendor) == "" &&
		strings.TrimSpace(m.Model) == "" &&
		strings.TrimSpace(m.Location) == "" &&
		strings.TrimSpace(m.IPPrefix) == ""
}

// Problems lists what prevents a rule from working, in plain language. A rule
// that never matches and does not say why is the worst setting: it looks applied.
func (m Match) Problems() []string {
	var out []string
	if m.Empty() {
		out = append(out, "rule with no criteria: it will not apply to any device")
	}
	if strings.TrimSpace(m.IPPrefix) != "" {
		var _, err = netip.ParsePrefix(strings.TrimSpace(m.IPPrefix))
		if err != nil {
			out = append(out, "unreadable address range: "+
				"expected a CIDR prefix like 10.100.0.0/24 (got \""+m.IPPrefix+"\")")
		}
	}
	return out
}

// Device is the little that must be known about a device to tag it. These are
// facts discovery has ALREADY observed: tagging never queries the fleet.
type Device struct {
	Vendor   string
	Model    string
	Location string
	MgmtIP   string
}

// Matches says whether the device satisfies ALL set criteria.
func (m Match) Matches(d Device) bool {
	if m.Empty() {
		return false
	}
	if !contains(d.Vendor, m.Vendor) ||
		!contains(d.Model, m.Model) ||
		!contains(d.Location, m.Location) {
		return false
	}
	return inPrefix(d.MgmtIP, m.IPPrefix)
}

// contains : true if the criterion is empty (unset = do not filter).
func contains(value, want string) bool {
	want = strings.TrimSpace(want)
	if want == "" {
		return true
	}
	return strings.Contains(strings.ToLower(value), strings.ToLower(want))
}

// inPrefix : true if the criterion is empty. An UNREADABLE prefix matches nothing
// — falling back to "no filter" would turn a typo into a catch-all rule.
func inPrefix(ip, prefix string) bool {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return true
	}
	var net, err = netip.ParsePrefix(prefix)
	if err != nil {
		return false
	}
	var addr netip.Addr
	addr, err = netip.ParseAddr(strings.TrimSpace(ip))
	if err != nil {
		return false
	}
	// A device with no known management address is not "in the range": it has NO
	// address. Counting it in would assign a password based on absent information.
	return net.Contains(addr)
}

// Rule is a rule as the operator entered it.
type Rule struct {
	ID      string `json:"id"`
	Tag     string `json:"tag"`
	Match   Match  `json:"match"`
	Enabled bool   `json:"enabled"`
}

// Source names the origin of a tag placed by this rule. This allows later
// removing what the rule placed without touching what a human decided.
func (r Rule) Source() string { return "rule:" + r.ID }

// SourceManual marks a tag placed by hand. It is NEVER removed by re-evaluation.
const SourceManual = "manual"

// FromRule says whether a source comes from a rule.
func FromRule(source string) bool { return strings.HasPrefix(source, "rule:") }

// Tag is a tag carried by a device, with its origin.
type Tag struct {
	Tag    string `json:"tag"`
	Source string `json:"source"`
}

// Plan is what a re-evaluation would change on ONE device. Computing without
// applying is what lets the UI show a rule's effect before touching the fleet.
type Plan struct {
	Add    []Tag `json:"add"`
	Remove []Tag `json:"remove"`
}

// Empty : nothing to do on this device.
func (p Plan) Empty() bool { return len(p.Add) == 0 && len(p.Remove) == 0 }

// Evaluate compares a device's current tags to what rules prescribe, and returns
// the strict minimum.
//
// Two guarantees, both necessary for replaying rules safely:
//
//   - IDEMPOTENCE — replaying on an already up-to-date fleet produces nothing;
//   - MANUAL tags are never removed. A disabled or wrong rule removes what IT
//     placed, nothing else.
//
// The same tag may be placed by hand AND by a rule: the manual source wins, and
// the tag survives disabling the rule.
func Evaluate(d Device, current []Tag, rules []Rule) Plan {
	var out Plan

	// What rules prescribe, and by which rule.
	var wanted = map[string]string{}
	var r Rule
	for _, r = range rules {
		if !r.Enabled || strings.TrimSpace(r.Tag) == "" {
			continue
		}
		if r.Match.Matches(d) {
			wanted[r.Tag] = r.Source()
		}
	}

	var have = map[string]string{}
	var t Tag
	for _, t = range current {
		have[t.Tag] = t.Source
	}

	// To remove: what a rule had placed and no rule wants anymore.
	for _, t = range current {
		if !FromRule(t.Source) {
			continue // placed by hand: untouchable
		}
		var _, stillWanted = wanted[t.Tag]
		if !stillWanted {
			out.Remove = append(out.Remove, t)
		}
	}

	// To add: what a rule wants and is not already there. We walk rules, not the
	// `wanted` table, so result order follows rule order — a plan whose order
	// varies between runs would be unreadable in a preview.
	for _, r = range rules {
		var source, want = wanted[r.Tag]
		if !want || source != r.Source() {
			continue
		}
		var existing, present = have[r.Tag]
		if present && (existing == source || existing == SourceManual) {
			continue
		}
		out.Add = append(out.Add, Tag{Tag: r.Tag, Source: source})
		have[r.Tag] = source
	}
	return out
}
