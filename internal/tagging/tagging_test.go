package tagging

import (
	"strings"
	"testing"
)

var le1930 = Device{
	Vendor:   "Aruba (HPE)",
	Model:    "HPE Networking Instant On Switch 48p Gigabit 4p SFP+ 1930 JL685A",
	Location: "Lighting booth",
	MgmtIP:   "10.100.0.10",
}

func TestMatches(t *testing.T) {
	var cas = []struct {
		name  string
		match Match
		want  bool
	}{
		{"vendor, case ignored", Match{Vendor: "aruba"}, true},
		{"product line in label", Match{Model: "1930"}, true},
		{"product reference", Match{Model: "JL685A"}, true},
		{"location", Match{Location: "lighting"}, true},
		{"address range", Match{IPPrefix: "10.100.0.0/24"}, true},
		{"criteria combined with AND", Match{Vendor: "aruba", Model: "1930"}, true},
		{"one false criterion is enough", Match{Vendor: "aruba", Model: "1830"}, false},
		{"outside range", Match{IPPrefix: "10.42.0.0/16"}, false},
		// With no criterion, the rule matches NOTHING: a half-filled row must not
		// reassign a password to the whole fleet.
		{"empty rule", Match{}, false},
		// An unreadable prefix is not "no filter": that would turn a typo into a
		// catch-all rule.
		{"unreadable prefix", Match{IPPrefix: "10.100.0.0/33"}, false},
	}
	var c = cas[0]
	for _, c = range cas {
		t.Run(c.name, func(t *testing.T) {
			if Match.Matches(c.match, le1930) != c.want {
				t.Errorf("Matches(%+v) = %v, expected %v", c.match, !c.want, c.want)
			}
		})
	}
}

// TestMatchesWithoutAddress : a device whose address is unknown is not "in the range".
// Counting it in would assign a credential based on absent information.
func TestMatchesWithoutAddress(t *testing.T) {
	var silent = Device{Vendor: "Aruba (HPE)"}
	if (Match{IPPrefix: "10.100.0.0/24"}).Matches(silent) {
		t.Error("a device without an address must not fall in a range")
	}
}

func TestProblems(t *testing.T) {
	var p = Match{}.Problems()
	if len(p) == 0 || !strings.Contains(p[0], "no criteria") {
		t.Errorf("an empty rule must report itself: %v", p)
	}
	p = Match{IPPrefix: "not-a-cidr"}.Problems()
	if len(p) == 0 || !strings.Contains(p[0], "CIDR") {
		t.Errorf("an unreadable prefix must report itself: %v", p)
	}
	if len(Match{Vendor: "aruba"}.Problems()) != 0 {
		t.Error("a valid rule must report nothing")
	}
}

func TestEvaluatePose(t *testing.T) {
	var rules = []Rule{
		{ID: "r1", Tag: "aruba-1930", Match: Match{Model: "1930"}, Enabled: true},
		{ID: "r2", Tag: "ops", Match: Match{Location: "lighting"}, Enabled: true},
		{ID: "r3", Tag: "cisco", Match: Match{Vendor: "cisco"}, Enabled: true},
	}
	var plan = Evaluate(le1930, nil, rules)
	if len(plan.Add) != 2 {
		t.Fatalf("2 tags expected, %d : %+v", len(plan.Add), plan.Add)
	}
	if plan.Add[0].Tag != "aruba-1930" || plan.Add[0].Source != "rule:r1" {
		t.Errorf("unexpected first tag: %+v", plan.Add[0])
	}
	if len(plan.Remove) != 0 {
		t.Errorf("nothing to remove on a blank device: %+v", plan.Remove)
	}
}

// TestEvaluateIdempotent : replaying rules on an already up-to-date fleet must
// produce NOTHING. Without this guarantee, every re-evaluation would add journal
// noise and useless writes.
func TestEvaluateIdempotent(t *testing.T) {
	var rules = []Rule{
		{ID: "r1", Tag: "aruba-1930", Match: Match{Model: "1930"}, Enabled: true},
	}
	var already = []Tag{{Tag: "aruba-1930", Source: "rule:r1"}}
	var plan = Evaluate(le1930, already, rules)
	if !plan.Empty() {
		t.Errorf("re-evaluation of an up-to-date fleet: %+v", plan)
	}
}

// TestEvaluateRemovesWhatTheRuleSet : a disabled or wrong rule removes what it
// had placed — it does not abandon it in place.
func TestEvaluateRemovesWhatTheRuleSet(t *testing.T) {
	var rules = []Rule{
		{ID: "r1", Tag: "aruba-1930", Match: Match{Model: "1930"}, Enabled: false},
	}
	var already = []Tag{{Tag: "aruba-1930", Source: "rule:r1"}}
	var plan = Evaluate(le1930, already, rules)
	if len(plan.Remove) != 1 || plan.Remove[0].Tag != "aruba-1930" {
		t.Errorf("tag from a disabled rule must be removed: %+v", plan)
	}
}

// TestEvaluateNeverTouchesManual : the point that makes re-evaluation safe.
// An operator who placed a tag by hand must find it after any rule pass.
func TestEvaluateNeverTouchesManual(t *testing.T) {
	var already = []Tag{
		{Tag: "external-contractor", Source: SourceManual},
		{Tag: "aruba-1930", Source: SourceManual},
	}
	// No rule claims these tags, and one shares a name with a disabled rule.
	var rules = []Rule{
		{ID: "r1", Tag: "aruba-1930", Match: Match{Model: "1930"}, Enabled: false},
	}
	var plan = Evaluate(le1930, already, rules)
	if len(plan.Remove) != 0 {
		t.Errorf("a manual tag must never be removed: %+v", plan.Remove)
	}
}

// TestEvaluateManualBeatsRule : when both want the same tag, nothing moves —
// and the tag survives disabling the rule.
func TestEvaluateManualBeatsRule(t *testing.T) {
	var already = []Tag{{Tag: "aruba-1930", Source: SourceManual}}
	var rules = []Rule{
		{ID: "r1", Tag: "aruba-1930", Match: Match{Model: "1930"}, Enabled: true},
	}
	var plan = Evaluate(le1930, already, rules)
	if !plan.Empty() {
		t.Errorf("manual source must win without changing anything: %+v", plan)
	}
}
