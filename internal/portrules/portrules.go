// Package portrules decides which configuration each port of a device receives,
// from a set of RULES and that device's facade.
//
// This is the "Compile" step of the reconciliation pipeline
// (docs/01-Architecture/Reconciliation section 1), restricted to ports and made
// PURE: a facade and a spec enter, a port-by-port assignment exits. No database,
// no device, no clock — because this is the decision an operator re-reads before
// applying it to forty-eight ports, and a decision that cannot be replayed
// identically is not readable.
//
// # Why rules rather than configuration
//
// Configuration names ports ("GE45 as trunk"): it only works for one facade. A
// rule names a SET ("the last four RJ45 ports"), which lets it work on a 48-port
// switch as on an 8-port one. That is the whole difference between a profile and
// copy-paste, and why selectors never target an absolute port number.
package portrules

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
)

// maxFacade bounds facade group expansion. An outlier in `port_groups` (manual
// entry, import of a badly parsed configuration) must not translate into a million
// ports in memory: we ignore the group, which shows on screen, rather than
// expanding anything.
const maxFacade = 4096

// --- The facade ---

// Media is the physical nature of a port.
//
// It is NOT declared in `devices.port_groups`: the facade states a prefix, a
// speed, and a label, never "copper" or "SFP+". We deduce it here, and only here
// — every screen redoing the reasoning on its own would eventually classify the
// same port differently.
type Media = string

const (
	// MediaAny is used only in a selector: no port has it.
	MediaAny Media = "any"
	// MediaCopper : an RJ45 port.
	MediaCopper Media = "copper"
	// MediaSFP : a 1G SFP cage.
	MediaSFP Media = "sfp"
	// MediaSFPPlus : a 10G or faster SFP cage.
	MediaSFPPlus Media = "sfp_plus"
)

// Port is a facade port, as a rule may designate it.
type Port struct {
	Name  string `json:"name"`
	Group string `json:"group"`
	Media Media  `json:"media"`
	// PoE is the port's CAPABILITY, not the intent placed on it.
	PoE   bool   `json:"poe_cap"`
	Speed string `json:"speed"`
}

// portGroupJSON is a facade block as `devices.port_groups` stores it.
//
// `from` is a pointer because its absence has precise meaning: catalog models do
// not set it and start at 1, while configuration import and the Instant On
// connector set it — on a 48-port switch, SFP ports are numbered 49 to 52,
// continuing the copper numbering.
type portGroupJSON struct {
	Prefix string `json:"prefix"`
	From   *int   `json:"from"`
	Count  int    `json:"count"`
	Speed  string `json:"speed"`
	PoE    bool   `json:"poe"`
	Label  string `json:"label"`
}

// Expand unfolds the facade into ports, in the order the device presents them.
//
// Order is what gives meaning to "the last four": it follows declared group order,
// and within a group physical numbering. Sorting differently (by name, for
// example) would put GE10 before GE2.
//
// An unreadable facade returns an empty list rather than an error: that is the
// case of a device whose model nobody has filled in yet, and the screen must be
// able to say so instead of refusing to display.
func Expand(raw json.RawMessage) []Port {
	var out = []Port{}
	if len(raw) == 0 {
		return out
	}
	var groups []portGroupJSON
	if json.Unmarshal(raw, &groups) != nil {
		return out
	}
	var g portGroupJSON
	for _, g = range groups {
		if g.Count < 1 || g.Count > maxFacade {
			continue
		}
		var from = 1
		if g.From != nil && *g.From > 0 {
			from = *g.From
		}
		var label = g.Label
		if label == "" {
			label = g.Prefix
		}
		var media = mediaOf(g.Prefix, g.Label, g.Speed)
		var i int
		for i = 0; i < g.Count; i++ {
			out = append(out, Port{
				Name:  g.Prefix + strconv.Itoa(from+i),
				Group: label,
				Media: media,
				PoE:   g.PoE,
				Speed: g.Speed,
			})
		}
	}
	return out
}

// mediaOf classifies a port from what the facade says about it.
//
// Prefix AND label are checked because neither alone suffices: the catalog
// distinguishes by prefix (`ether` / `sfp-sfpplus`), the Instant On connector
// names EVERYTHING `GE` and distinguishes only by label ("1-48 · PoE" vs "SFP+").
// Checking only one would classify half the fleet as copper.
//
// Speed separates SFP from SFP+, and label is only a fallback: speed is what the
// facade declares most consistently, and a "+" in a label is a display convention,
// not data.
func mediaOf(prefix, label, speed string) Media {
	var hay = strings.ToLower(prefix + " " + label)
	if !strings.Contains(hay, "sfp") {
		return MediaCopper
	}
	if speedMbps(speed) >= 10000 || strings.Contains(strings.ToLower(label), "sfp+") {
		return MediaSFPPlus
	}
	return MediaSFP
}

// speedMbps translates a facade speed ("1G", "10G", "2.5G", "100M") to Mb/s.
// Returns 0 when the string is unreadable: speed then abstains from deciding,
// rather than classifying a 10G cage as 1G.
func speedMbps(s string) int {
	s = strings.ToUpper(strings.TrimSpace(s))
	var mult = 1
	switch {
	case strings.HasSuffix(s, "G"):
		mult, s = 1000, strings.TrimSuffix(s, "G")
	case strings.HasSuffix(s, "M"):
		s = strings.TrimSuffix(s, "M")
	}
	var f, err = strconv.ParseFloat(strings.Replace(s, ",", ".", 1), 64)
	if err != nil {
		return 0
	}
	return int(f * float64(mult))
}

// --- Produced configuration ---

// Config is what a rule places on a port.
//
// Exactly the four fields the rest of the system expects — `mode`, `pvid`,
// `tagged`, `poe` (docs/01-Architecture/Modele de donnees standard) — and
// nothing more. Adding fields no connector writes would make the preview screen
// look like it announces configuration that would never arrive.
type Config struct {
	Mode string
	PVID int
	// Tagged is the explicit enumeration of tagged VLANs.
	Tagged []int
	// TaggedAll : the port is an "all" trunk.
	//
	// We NEVER expand "all" here, though we could (site VLANs are known to the
	// caller). Expansion belongs in recon.Resolve only — a profile doing it too
	// would produce a second version that would diverge. After resolve, "all" is
	// the exact site VLAN set (add and remove); while the site plan is empty it
	// stays unresolved and apply does not strip tagged membership.
	TaggedAll bool
	PoE       bool
}

// configJSON is the serialized form of a Config.
//
// This is the form of a port profile (`port_profiles.spec`) and of a port in
// `devices.ports`. All three deliberately share the same shape: a rule can point
// at an existing port profile without translation, and what it produces is
// written without translation too. A conversion in the middle is where "tagged:
// all" would eventually get lost.
type configJSON struct {
	Mode   string          `json:"mode"`
	PVID   *int            `json:"pvid,omitempty"`
	Tagged json.RawMessage `json:"tagged,omitempty"`
	PoE    bool            `json:"poe"`
}

func (c *Config) UnmarshalJSON(b []byte) error {
	var raw configJSON
	var err = json.Unmarshal(b, &raw)
	if err != nil {
		return err
	}
	*c = Config{Mode: raw.Mode, PoE: raw.PoE}
	if raw.PVID != nil {
		c.PVID = *raw.PVID
	}
	if len(raw.Tagged) == 0 {
		return nil
	}
	// `tagged` is either the string "all" or a list. Try the string first, like
	// recon.ParsePorts: both reads must agree, otherwise an "all" trunk entered
	// here would not be the one diff re-reads.
	var all string
	if json.Unmarshal(raw.Tagged, &all) == nil {
		c.TaggedAll = all == "all"
		return nil
	}
	var ids []int
	if json.Unmarshal(raw.Tagged, &ids) == nil {
		sort.Ints(ids)
		c.Tagged = ids
	}
	return nil
}

func (c Config) MarshalJSON() ([]byte, error) {
	var fields = c.Fields()
	var out = map[string]json.RawMessage{}
	var k string
	var v json.RawMessage
	for k, v = range fields {
		out[k] = v
	}
	return json.Marshal(out)
}

// Fields renders configuration KEY BY KEY, in `devices.ports` form.
//
// Key by key and not as a whole object, because apply must MERGE: a port object
// also carries the port label — a meshcraft annotation the device does not know
// — and the last telemetry read (link, rates, watts). Rewriting the whole object
// would erase both: the label permanently, telemetry until the next read. A
// profile applied to forty-eight ports would therefore erase forty-eight port
// descriptions, which cannot be repaired.
func (c Config) Fields() map[string]json.RawMessage {
	var out = map[string]json.RawMessage{}
	out["mode"] = mustJSON(c.Mode)
	out["poe"] = mustJSON(c.PoE)
	// An "libre" port is not managed: writing PVID or a VLAN list would leave
	// residue that reappears on the next pass to access.
	if c.Mode == "libre" || c.Mode == "" {
		out["pvid"] = mustJSON(nil)
		out["tagged"] = mustJSON([]int{})
		return out
	}
	out["pvid"] = mustJSON(c.PVID)
	switch {
	case c.Mode != "trunk":
		// Outside trunk, no tagged VLANs: writing this explicitly REMOVES what a
		// previous trunk left on the port.
		out["tagged"] = mustJSON([]int{})
	case c.TaggedAll:
		out["tagged"] = mustJSON("all")
	default:
		var ids = c.Tagged
		if ids == nil {
			ids = []int{}
		}
		out["tagged"] = mustJSON(ids)
	}
	return out
}

func mustJSON(v any) json.RawMessage {
	var b, err = json.Marshal(v)
	if err != nil {
		return json.RawMessage(`null`)
	}
	return b
}

// --- Selectors ---

// Selector.PoE values: port PoE capability, not intent.
const (
	TriAny = "any"
	TriYes = "yes"
	TriNo  = "no"
)

// Selector.Slice values.
const (
	SliceAll   = "all"
	SliceFirst = "first"
	SliceLast  = "last"
	SliceRange = "range"
)

// Selector designates a set of ports on ANY facade.
//
// It works in two steps, which makes it portable:
//
//  1. a FILTER on what the port is (its facade group, media, PoE capability) —
//     true or false, port by port;
//  2. a SLICE on the resulting set (all, first N, last N, a rank range).
//
// The slice applies to RANK in the filtered set, never to the number printed on
// the front panel. That is deliberate: numbers are not comparable across facades
// — the catalog restarts at 1 per prefix (`ether1…48`, `sfp-sfpplus1…4`) while
// Instant On numbers SFP ports continuing copper (`GE49…52`). A rule written on
// numbers would designate two different sets on two models, which is exactly what
// a profile must not do.
//
// # What was ruled out
//
//   - regular expression on port name: same reason as tagging rules
//     (docs/01-Architecture/Etiquettes et portee des identifiants section 4) — a
//     wrong regex does not say it is wrong, it matches nothing;
//   - selection on OBSERVED state (active link, negotiated speed, LLDP neighbor):
//     telemetry is not intent (cf. recon.PortIntent). A profile whose result
//     changes depending on whether a cable is plugged would not be replayable,
//     and the preview would be worthless;
//   - specificity score between rules: see Evaluate.
type Selector struct {
	// Group : substring of the facade group label, case-insensitive. Empty = all
	// groups.
	//
	// Substring, not equality: labels are not normalized across models ("ETHER 1-48
	// · PoE+" vs "1-48 · PoE"). Requiring equality would silence the rule on the
	// second model. The downside is assumed and told to the screen: targeting a
	// label ties the rule to a product line.
	Group string `json:"group,omitempty"`
	// Media : any | copper | sfp | sfp_plus.
	//
	// "sfp" ALSO matches SFP+: that is what an operator means by "fiber ports".
	// "sfp_plus" matches only 10G and faster cages.
	Media Media `json:"media,omitempty"`
	// PoE : any | yes | no, on port CAPABILITY.
	PoE string `json:"poe,omitempty"`
	// Slice : all | first | last | range. Empty = all.
	Slice string `json:"slice,omitempty"`
	// Count : the N of "first" and "last".
	Count int `json:"count,omitempty"`
	// From, To : 1-based inclusive ranks of "range". To at 0 = through the end.
	From int `json:"from,omitempty"`
	To   int `json:"to,omitempty"`
}

// matches applies the FILTER to a port.
func (sel Selector) matches(p Port) bool {
	if sel.Group != "" &&
		!strings.Contains(strings.ToLower(p.Group), strings.ToLower(sel.Group)) {
		return false
	}
	switch sel.Media {
	case MediaCopper:
		if p.Media != MediaCopper {
			return false
		}
	case MediaSFPPlus:
		if p.Media != MediaSFPPlus {
			return false
		}
	case MediaSFP:
		if p.Media == MediaCopper {
			return false
		}
	}
	switch sel.PoE {
	case TriYes:
		if !p.PoE {
			return false
		}
	case TriNo:
		if p.PoE {
			return false
		}
	}
	return true
}

// cut applies the SLICE to an already filtered rank set.
//
// Bounds are CLAMPED, not rejected: "the last 4" on a facade with only 2
// designates 2, and "ranks 1 to 44" on a 24-port switch designates 24. That is
// what lets the same profile serve 48-, 24-, and 8-port switches. Rejecting would
// be defensible on a form; on a profile applied to a heterogeneous fleet, it would
// mean having no profile at all.
func (sel Selector) cut(idx []int) []int {
	switch sel.Slice {
	case SliceFirst:
		if sel.Count <= 0 {
			return nil
		}
		if sel.Count < len(idx) {
			return idx[:sel.Count]
		}
		return idx
	case SliceLast:
		if sel.Count <= 0 {
			return nil
		}
		if sel.Count < len(idx) {
			return idx[len(idx)-sel.Count:]
		}
		return idx
	case SliceRange:
		var from, to = sel.From, sel.To
		if from < 1 {
			from = 1
		}
		if to < 1 || to > len(idx) {
			to = len(idx)
		}
		if from > to {
			return nil
		}
		return idx[from-1 : to]
	}
	return idx
}

// Problems says what in a selector cannot designate any port on ANY facade. An
// empty slice by construction is an input error, and the safe default is to say
// so rather than silently apply an inert rule.
func (sel Selector) Problems() []string {
	var out []string
	switch sel.Slice {
	case SliceFirst, SliceLast:
		if sel.Count <= 0 {
			out = append(out, "the number of ports to take must be at least 1")
		}
	case SliceRange:
		if sel.From > 0 && sel.To > 0 && sel.From > sel.To {
			out = append(out, "the rank range starts after its end")
		}
	}
	return out
}

// --- Rules ---

// Rule is ONE rule: a set of ports, and what they receive.
type Rule struct {
	Label  string   `json:"label"`
	Select Selector `json:"select"`
	// PortProfileID : the rule applies an EXISTING PORT PROFILE rather than
	// configuration written in the rule.
	//
	// This is the reason port profiles exist (docs/02-Frontend/Profils et
	// groupes): describe configuration once, here as in the switch view. A rule
	// that copied the profile configuration would drift on the first profile edit,
	// with nothing signaling it.
	PortProfileID string `json:"port_profile_id,omitempty"`
	// Config : configuration written in the rule, when it does not reference a
	// port profile.
	Config *Config `json:"config,omitempty"`
}

// Spec is the content of `switch_profiles.spec`.
type Spec struct {
	Description string `json:"description,omitempty"`
	Rules       []Rule `json:"rules"`
}

// ParseSpec reads a stored spec. An absent spec is a spec with no rules: a
// profile just created is not an error, it simply does nothing.
func ParseSpec(raw json.RawMessage) (Spec, error) {
	var s = Spec{Rules: []Rule{}}
	if len(raw) == 0 {
		return s, nil
	}
	var err = json.Unmarshal(raw, &s)
	if err != nil {
		return Spec{Rules: []Rule{}}, err
	}
	if s.Rules == nil {
		s.Rules = []Rule{}
	}
	return s, nil
}

// --- Result ---

// Assignment is what ONE port receives, and from whom.
//
// `Rule` and `RuleLabel` are not decorative: they make conflict resolution
// readable. Facing forty-eight ports, the question is not only "what does it get?"
// but "why that one and not what I thought I wrote?". Without the winning rule
// name, the answer is found by hand.
type Assignment struct {
	Port      string `json:"port"`
	Group     string `json:"group"`
	Media     Media  `json:"media"`
	PoECap    bool   `json:"poe_cap"`
	Rule      int    `json:"rule"`
	RuleLabel string `json:"rule_label"`
	Config    Config `json:"config"`
	// Warn : what the rule asked for and the port cannot do. Rendered configuration
	// already accounts for the refusal — Warn only says it happened, otherwise the
	// guard would look like a bug.
	Warn string `json:"warn,omitempty"`
}

// RuleReport says what a rule designated and what it obtained.
//
// The two counts differ as soon as a higher rule took the same ports. That is THE
// case to show: a rule designating thirty ports and obtaining none is not wrong,
// it is masked — and that is indistinguishable if only one count is shown.
type RuleReport struct {
	Index   int    `json:"index"`
	Label   string `json:"label"`
	Matched int    `json:"matched"`
	Won     int    `json:"won"`
	// Problem : the rule produces no usable configuration. While one exists,
	// apply is refused (cf. Plan.Issues).
	Problem string `json:"problem,omitempty"`
}

// Plan is the full evaluation result on ONE facade.
type Plan struct {
	Ports []Assignment `json:"ports"`
	Rules []RuleReport `json:"rules"`
	// Uncovered : ports no rule designates. They are left INTACT, and that is a
	// choice: a profile that reset what it does not mention would silently destroy
	// configuration on ports nobody asked it to touch. That is the "absent =
	// unmanaged" rule of the standard model, applied to profiles. An operator who
	// wants the opposite adds a catch-all rule last — and they SEE it.
	Uncovered []string `json:"uncovered"`
	// Issues : reasons to refuse apply. Non-empty = apply nothing.
	//
	// Applying "everything except broken rules" would yield a half-configured
	// facade nobody could say what is missing on, and the missing would read as a
	// port deliberately left free.
	Issues []string `json:"issues"`
}

// Evaluate assigns each port to ONE rule and only one.
//
// # Who wins: the FIRST rule
//
// Conflict is certain — a catch-all necessarily overlaps others — and three
// arbitrations were considered:
//
//   - SPECIFICITY (most precise rule wins): ruled out. It assumes a total order on
//     criterion combinations the operator does not see and must learn. Is "the
//     last 4 PoE RJ45 ports" more specific than "the SFP+ ports"? Any answer is
//     arbitrary, and an invisible arbitration is discovered in production;
//   - LAST rule (CSS style): coherent, but forces writing the catch-all FIRST,
//     opposite to how an operator states intent;
//   - FIRST rule (access control list style): chosen. It matches the statement
//     literally — "SFP+ as trunk; last 4 RJ45 as trunk PVID 100; everything else
//     as access PVID 100" — where the catch-all is naturally written last and must
//     lose.
//
// A port therefore receives configuration from exactly ONE rule. No per-field
// merge between rules: "who set PoE false on this port?" must have a one-word
// answer, and a four-level merge does not.
//
// # Selectors evaluate on the ENTIRE facade
//
// Not on what previous rules left. "The last 4 RJ45 ports" designates the same set
// regardless of rule position; only WINNING depends on order. The other convention
// — filtering remaining ports — would make a rule's meaning depend on everything
// before it: moving a rule would silently change the set designated by later ones.
//
// `profiles` maps port profile id to configuration. An unknown reference is NOT
// recovered by fallback: the rule is faulty, it takes no port, and apply is
// refused. Falling back to the next rule would place configuration nobody asked
// for where the operator believes they asked for something else.
func Evaluate(spec Spec, ports []Port, profiles map[string]Config) Plan {
	var plan = Plan{
		Ports:     []Assignment{},
		Rules:     []RuleReport{},
		Uncovered: []string{},
		Issues:    []string{},
	}
	var owner = make([]int, len(ports))
	var i int
	for i = range owner {
		owner[i] = -1
	}
	var configs = make([]Config, len(spec.Rules))

	var ri int
	var rule Rule
	for ri, rule = range spec.Rules {
		var report = RuleReport{Index: ri, Label: ruleLabel(rule, ri)}
		var cfg, refusal = resolveConfig(rule, profiles)
		var problems = rule.Select.Problems()
		if refusal != "" {
			problems = append(problems, refusal)
		}
		if len(problems) > 0 {
			report.Problem = strings.Join(problems, " ; ")
			plan.Issues = append(plan.Issues,
				"rule \""+report.Label+"\": "+report.Problem)
			plan.Rules = append(plan.Rules, report)
			continue
		}
		configs[ri] = cfg

		var designated []int
		for i = range ports {
			if rule.Select.matches(ports[i]) {
				designated = append(designated, i)
			}
		}
		designated = rule.Select.cut(designated)
		report.Matched = len(designated)
		var k int
		for _, k = range designated {
			if owner[k] == -1 {
				owner[k] = ri
				report.Won++
			}
		}
		plan.Rules = append(plan.Rules, report)
	}

	for i = range ports {
		if owner[i] == -1 {
			plan.Uncovered = append(plan.Uncovered, ports[i].Name)
			continue
		}
		var cfg, warn = fit(configs[owner[i]], ports[i])
		plan.Ports = append(plan.Ports, Assignment{
			Port:      ports[i].Name,
			Group:     ports[i].Group,
			Media:     ports[i].Media,
			PoECap:    ports[i].PoE,
			Rule:      owner[i],
			RuleLabel: ruleLabel(spec.Rules[owner[i]], owner[i]),
			Config:    cfg,
			Warn:      warn,
		})
	}
	return plan
}

// fit adjusts configuration to what the PORT can do.
//
// Only one mismatch is handled today, because it is the only one the facade lets
// us detect: powering PoE on a port that has none. It is CORRECTED rather than
// refused — an "office" profile with PoE must work on SFP ports of the same
// device without an extra rule — but it is STATED, otherwise the operator believes
// they set PoE where there will never be any.
func fit(c Config, p Port) (Config, string) {
	if !c.PoE || p.PoE {
		return c, ""
	}
	c.PoE = false
	return c, "this port has no PoE: requested power is ignored"
}

// resolveConfig returns a rule's configuration, or the reason to refuse it.
func resolveConfig(r Rule, profiles map[string]Config) (Config, string) {
	if r.PortProfileID != "" {
		var c, seen = profiles[r.PortProfileID]
		if !seen {
			return Config{}, "the referenced port profile no longer exists on this site"
		}
		return c, ""
	}
	if r.Config == nil {
		return Config{}, "no configuration: neither port profile nor inline settings"
	}
	return *r.Config, ""
}

// ruleLabel names a rule for the operator. A rule without a name keeps its index
// rather than displaying empty: in an error message, "rule 3" is findable, "rule
// \" \"" is not.
func ruleLabel(r Rule, index int) string {
	if strings.TrimSpace(r.Label) != "" {
		return r.Label
	}
	return "rule " + strconv.Itoa(index+1)
}
