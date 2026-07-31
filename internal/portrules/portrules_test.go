package portrules

import (
	"encoding/json"
	"strings"
	"testing"
)

// The two reference facades for tests. They are deliberately taken from the real
// fleet rather than invented: the top one is what the Instant On connector
// produces (everything "GE", SFP numbered after copper), the bottom what the
// catalog produces (distinct prefixes, numbering restarts). A profile must work on
// both.
const facade48 = `[
  {"prefix":"GE","from":1,"count":48,"speed":"1G","poe":true,"label":"1-48 · PoE"},
  {"prefix":"GE","from":49,"count":4,"speed":"10G","poe":false,"label":"SFP+"}
]`

const facade8 = `[
  {"prefix":"ether","count":8,"poe":true,"speed":"1G","label":"ETHER 1-8 · PoE"},
  {"prefix":"sfp","count":2,"speed":"1G","label":"SFP"}
]`

func names(a []Assignment) []string {
	var out = make([]string, 0, len(a))
	var x Assignment
	for _, x = range a {
		out = append(out, x.Port)
	}
	return out
}

// byPort finds a port assignment. Returns false when the port received no rule:
// tests must distinguish "nothing" from "zero".
func byPort(p Plan, name string) (Assignment, bool) {
	var x Assignment
	for _, x = range p.Ports {
		if x.Port == name {
			return x, true
		}
	}
	return Assignment{}, false
}

func cfg(mode string, pvid int, tagged []int, all, poe bool) *Config {
	return &Config{Mode: mode, PVID: pvid, Tagged: tagged, TaggedAll: all, PoE: poe}
}

// The operator example, word for word: all SFP+ as trunk, last 4 RJ45 as trunk
// PVID 100, everything else as access PVID 100.
func exempleExploitant() Spec {
	return Spec{Rules: []Rule{
		{Label: "SFP+ trunk",
			Select: Selector{Media: MediaSFPPlus},
			Config: cfg("trunk", 1, nil, true, false)},
		{Label: "last 4 RJ45",
			Select: Selector{Media: MediaCopper, Slice: SliceLast, Count: 4},
			Config: cfg("trunk", 100, []int{100}, false, false)},
		{Label: "everything else",
			Select: Selector{},
			Config: cfg("access", 100, nil, false, true)},
	}}
}

// TestFacadeDeplieeDansLOrdreDeLEquipement : "the last four" only makes sense if
// order matches the front panel. Sorting by name would put GE10 before GE2 and the
// rule would designate four random ports in the middle.
func TestFacadeDeplieeDansLOrdreDeLEquipement(t *testing.T) {
	var ports = Expand(json.RawMessage(facade48))
	if len(ports) != 52 {
		t.Fatalf("52 ports expected, %d", len(ports))
	}
	if ports[0].Name != "GE1" || ports[47].Name != "GE48" {
		t.Errorf("copper misnumbered: %q … %q", ports[0].Name, ports[47].Name)
	}
	// SFP numbering CONTINUES copper on this line: losing that would write
	// configuration to ports that do not exist.
	if ports[48].Name != "GE49" || ports[51].Name != "GE52" {
		t.Errorf("SFP misnumbered: %q … %q", ports[48].Name, ports[51].Name)
	}
}

// TestSFPReconnusMalgreUnPrefixeCommun : on Instant On, copper and fiber share the
// SAME "GE" prefix. Classifying on prefix alone would mark all fiber as copper, and
// "all SFP+ as trunk" would match nothing at all — silently.
func TestSFPReconnusMalgreUnPrefixeCommun(t *testing.T) {
	var ports = Expand(json.RawMessage(facade48))
	if ports[0].Media != MediaCopper {
		t.Errorf("GE1 should be copper: %q", ports[0].Media)
	}
	if ports[48].Media != MediaSFPPlus {
		t.Errorf("GE49 should be SFP+: %q", ports[48].Media)
	}
	// And on the catalog, where prefix distinguishes and speed separates SFP from
	// SFP+.
	var petit = Expand(json.RawMessage(facade8))
	if petit[7].Media != MediaCopper || petit[8].Media != MediaSFP {
		t.Errorf("catalog facade misclassified: %q / %q", petit[7].Media, petit[8].Media)
	}
	if petit[8].Media == MediaSFPPlus {
		t.Error("a 1G SFP cage is not SFP+")
	}
}

// TestSelecteurSFPPrendAussiLesSFPPlus : "fiber ports" means the cages, all speeds.
// If "sfp" excluded SFP+, a fiber rule would leave 10G uplinks unconfigured — the
// costliest case to miss.
func TestSelecteurSFPPrendAussiLesSFPPlus(t *testing.T) {
	var ports = Expand(json.RawMessage(facade48))
	var spec = Spec{Rules: []Rule{
		{Label: "fiber", Select: Selector{Media: MediaSFP},
			Config: cfg("trunk", 1, nil, true, false)},
	}}
	var plan = Evaluate(spec, ports, nil)
	if len(plan.Ports) != 4 {
		t.Fatalf("4 cages expected, %d : %v", len(plan.Ports), names(plan.Ports))
	}
}

// TestLExempleDeLExploitantSurUn48Ports : the nominal case that motivated the
// feature. It mainly verifies that the catch-all written LAST does not eat what
// previous rules took.
func TestLExempleDeLExploitantSurUn48Ports(t *testing.T) {
	var plan = Evaluate(exempleExploitant(), Expand(json.RawMessage(facade48)), nil)
	if len(plan.Issues) != 0 {
		t.Fatalf("no refusal expected: %v", plan.Issues)
	}
	if len(plan.Uncovered) != 0 {
		t.Errorf("catch-all must cover the whole facade, remaining: %v", plan.Uncovered)
	}
	var sfp, _ = byPort(plan, "GE49")
	if sfp.RuleLabel != "SFP+ trunk" || !sfp.Config.TaggedAll {
		t.Errorf("GE49: %+v", sfp)
	}
	var dernier, _ = byPort(plan, "GE48")
	if dernier.RuleLabel != "last 4 RJ45" || dernier.Config.PVID != 100 ||
		dernier.Config.Mode != "trunk" {
		t.Errorf("GE48: %+v", dernier)
	}
	var premierDesQuatre, _ = byPort(plan, "GE45")
	if premierDesQuatre.RuleLabel != "last 4 RJ45" {
		t.Errorf("GE45 opens the last 4 RJ45: %+v", premierDesQuatre)
	}
	// The boundary counts as much as the interior: "last 4" stops at copper and
	// does not spill onto GE44 or the cages.
	var avant, _ = byPort(plan, "GE44")
	if avant.RuleLabel != "everything else" {
		t.Errorf("GE44 is not part of the last 4: %+v", avant)
	}
	var courant, _ = byPort(plan, "GE1")
	if courant.RuleLabel != "everything else" || courant.Config.Mode != "access" {
		t.Errorf("GE1: %+v", courant)
	}
	// Per-rule counts: what the screen shows to make arbitration readable.
	if plan.Rules[0].Won != 4 || plan.Rules[1].Won != 4 || plan.Rules[2].Won != 44 {
		t.Errorf("unexpected counts: %+v", plan.Rules)
	}
}

// TestLeMemeProfilSurUneAutreFacade : THE test separating a profile from
// configuration. The same spec, without rewriting a line, on an 8-port switch whose
// cages are 1G. If it fails, each model would need its own profile and the object
// would have no reason to exist.
func TestLeMemeProfilSurUneAutreFacade(t *testing.T) {
	var plan = Evaluate(exempleExploitant(), Expand(json.RawMessage(facade8)), nil)
	if len(plan.Uncovered) != 0 {
		t.Errorf("still uncovered: %v", plan.Uncovered)
	}
	// No SFP+ here: the first rule takes nothing, and that is not an error. 1G
	// cages fall through to the catch-all.
	if plan.Rules[0].Won != 0 {
		t.Errorf("no SFP+ on this facade: %+v", plan.Rules[0])
	}
	var cage, _ = byPort(plan, "sfp1")
	if cage.RuleLabel != "everything else" {
		t.Errorf("sfp1: %+v", cage)
	}
	// The last 4 RJ45 are ether5..8, not ether45..48.
	var e5, _ = byPort(plan, "ether5")
	var e4, _ = byPort(plan, "ether4")
	if e5.RuleLabel != "last 4 RJ45" {
		t.Errorf("ether5 should be in the last 4: %+v", e5)
	}
	if e4.RuleLabel != "everything else" {
		t.Errorf("ether4 should not be: %+v", e4)
	}
}

// TestTrancheRogneeSurUnePetiteFacade : "ranks 1 to 44" on a device with only 8
// must take 8, not zero. Rejecting an out-of-bounds slice would make every profile
// written for a 48-port switch unusable on the rest of the fleet, i.e. having no
// profile at all.
func TestTrancheRogneeSurUnePetiteFacade(t *testing.T) {
	var spec = Spec{Rules: []Rule{
		{Label: "offices",
			Select: Selector{Media: MediaCopper, Slice: SliceRange, From: 1, To: 44},
			Config: cfg("access", 20, nil, false, true)},
	}}
	var plan = Evaluate(spec, Expand(json.RawMessage(facade8)), nil)
	if plan.Rules[0].Won != 8 {
		t.Fatalf("8 copper ports expected, %d", plan.Rules[0].Won)
	}
	// And "last 4" on a set that only has 2.
	var deux = Spec{Rules: []Rule{
		{Label: "cages", Select: Selector{Media: MediaSFP, Slice: SliceLast, Count: 4},
			Config: cfg("trunk", 1, nil, true, false)},
	}}
	var petit = Evaluate(deux, Expand(json.RawMessage(facade8)), nil)
	if petit.Rules[0].Won != 2 {
		t.Errorf("2 cages expected, %d", petit.Rules[0].Won)
	}
}

// TestLaPremiereRegleGagne : the chosen arbitration, and it must be verifiable
// without reading the code. Two rules target the same port; the top one wins, and
// the other knows (Matched without Won).
func TestLaPremiereRegleGagne(t *testing.T) {
	var spec = Spec{Rules: []Rule{
		{Label: "top", Select: Selector{Media: MediaCopper},
			Config: cfg("access", 10, nil, false, false)},
		{Label: "bottom", Select: Selector{Media: MediaCopper},
			Config: cfg("access", 20, nil, false, false)},
	}}
	var plan = Evaluate(spec, Expand(json.RawMessage(facade8)), nil)
	var p, _ = byPort(plan, "ether1")
	if p.Config.PVID != 10 {
		t.Errorf("top rule must win: %+v", p)
	}
	// A masked rule is not an empty rule: it DESIGNATES 8 ports and wins none.
	// Confusing the two prevents understanding why a rule does nothing.
	if plan.Rules[1].Matched != 8 || plan.Rules[1].Won != 0 {
		t.Errorf("masked rule badly reported: %+v", plan.Rules[1])
	}
}

// TestLeSelecteurNeDependPasDesReglesPrecedentes : "last 2 RJ45" designates the
// same two ports whether the rule is alone or preceded by another that already took
// half the facade. Without this guarantee, moving a rule in the list would silently
// change the set designated by later ones, and preview would be the only way to
// know what was written.
func TestLeSelecteurNeDependPasDesReglesPrecedentes(t *testing.T) {
	var spec = Spec{Rules: []Rule{
		{Label: "first 6",
			Select: Selector{Media: MediaCopper, Slice: SliceFirst, Count: 6},
			Config: cfg("access", 10, nil, false, false)},
		{Label: "last 2",
			Select: Selector{Media: MediaCopper, Slice: SliceLast, Count: 2},
			Config: cfg("trunk", 1, nil, true, false)},
	}}
	var plan = Evaluate(spec, Expand(json.RawMessage(facade8)), nil)
	var e7, _ = byPort(plan, "ether7")
	var e8, _ = byPort(plan, "ether8")
	if e7.RuleLabel != "last 2" || e8.RuleLabel != "last 2" {
		t.Errorf("ether7/8: %+v / %+v", e7, e8)
	}
	// Proof: shrink the first rule to 4 ports. If the second worked on the REST
	// (ether5…8), "last 2" would land on ether7/8 by coincidence here — but the
	// same shrink would leave ether5 and ether6 WITHOUT a rule, which would not
	// happen if remainder were considered. That gap proves the selector stays on
	// the whole facade.
	spec.Rules[0].Select.Count = 4
	plan = Evaluate(spec, Expand(json.RawMessage(facade8)), nil)
	var _, couverte = byPort(plan, "ether5")
	if couverte {
		t.Error("ether5 should be designated by neither rule")
	}
	var e8b, _ = byPort(plan, "ether8")
	if e8b.RuleLabel != "last 2" {
		t.Errorf("ether8: %+v", e8b)
	}
}

// TestPrisesNonCouvertesLaisseesIntactes : a profile does not mention everything,
// and what it does not mention must not be reset. Setting "libre" on undesignated
// ports would silently destroy configuration nobody asked to touch — and that
// cannot be repaired.
func TestPrisesNonCouvertesLaisseesIntactes(t *testing.T) {
	var spec = Spec{Rules: []Rule{
		{Label: "cages", Select: Selector{Media: MediaSFP},
			Config: cfg("trunk", 1, nil, true, false)},
	}}
	var plan = Evaluate(spec, Expand(json.RawMessage(facade8)), nil)
	if len(plan.Ports) != 2 {
		t.Fatalf("2 configured ports expected, %d", len(plan.Ports))
	}
	if len(plan.Uncovered) != 8 {
		t.Fatalf("8 uncovered ports expected, %d : %v", len(plan.Uncovered), plan.Uncovered)
	}
	if plan.Uncovered[0] != "ether1" {
		t.Errorf("uncovered ports must be named: %v", plan.Uncovered)
	}
}

// TestTrunkAllResteUnJoker : "all" must NEVER be expanded to an enumeration by
// evaluation, even if site VLANs are known. Expansion belongs in recon.Resolve
// only — a profile doing it too would diverge from apply/diff.
func TestTrunkAllResteUnJoker(t *testing.T) {
	var spec = Spec{Rules: []Rule{
		{Label: "uplinks", Select: Selector{Media: MediaSFPPlus},
			Config: cfg("trunk", 1, nil, true, false)},
	}}
	var plan = Evaluate(spec, Expand(json.RawMessage(facade48)), nil)
	var p, _ = byPort(plan, "GE49")
	if !p.Config.TaggedAll {
		t.Fatalf("\"all\" lost: %+v", p.Config)
	}
	if len(p.Config.Tagged) != 0 {
		t.Errorf("\"all\" must not be enumerated here: %v", p.Config.Tagged)
	}
	// And it must rewrite as "all" in devices.ports, not an empty list: an empty
	// list there would mean "no tagged VLANs", the opposite of intent.
	var raw = string(p.Config.Fields()["tagged"])
	if raw != `"all"` {
		t.Errorf("\"all\" serialization: %s", raw)
	}
}

// TestPoEIgnoreSurUnePriseQuiNEnAPas : an "office" profile with PoE must work on a
// mixed facade without an extra rule for cages. Refusal is therefore corrected —
// but STATED, otherwise the operator believes they powered a port that never will.
func TestPoEIgnoreSurUnePriseQuiNEnAPas(t *testing.T) {
	var spec = Spec{Rules: []Rule{
		{Label: "all", Select: Selector{}, Config: cfg("access", 20, nil, false, true)},
	}}
	var plan = Evaluate(spec, Expand(json.RawMessage(facade8)), nil)
	var cuivre, _ = byPort(plan, "ether1")
	if !cuivre.Config.PoE || cuivre.Warn != "" {
		t.Errorf("PoE copper must stay powered without warning: %+v", cuivre)
	}
	var cage, _ = byPort(plan, "sfp1")
	if cage.Config.PoE {
		t.Error("PoE must not be set on an SFP cage")
	}
	if cage.Warn == "" {
		t.Error("a guard that does not report itself looks like a bug")
	}
}

// TestProfilDePriseIntrouvableRefuseLaPose : a rule pointing at a deleted port
// profile must not fall back to the next rule — the operator would see coherent and
// wrong configuration. It takes nothing, says so, and the plan carries a refusal
// that blocks apply.
func TestProfilDePriseIntrouvableRefuseLaPose(t *testing.T) {
	var spec = Spec{Rules: []Rule{
		{Label: "uplink", Select: Selector{Media: MediaSFP}, PortProfileID: "disparu"},
		{Label: "rest", Select: Selector{}, Config: cfg("access", 20, nil, false, false)},
	}}
	var plan = Evaluate(spec, Expand(json.RawMessage(facade8)), map[string]Config{})
	if len(plan.Issues) != 1 || !strings.Contains(plan.Issues[0], "uplink") {
		t.Fatalf("refusal naming the rule expected: %v", plan.Issues)
	}
	// The faulty rule takes nothing, but also does not quietly leave its ports to
	// the next rule: refusal blocks the whole apply.
	var cage, seen = byPort(plan, "sfp1")
	if seen && cage.RuleLabel == "uplink" {
		t.Error("a faulty rule must assign nothing")
	}
	if plan.Rules[0].Problem == "" {
		t.Error("faulty rule must carry its reason")
	}
}

// TestReglePointantUnProfilDePrise : the nominal reuse case. A rule that copied
// the profile configuration would drift on the first profile edit, with nothing
// signaling it.
func TestReglePointantUnProfilDePrise(t *testing.T) {
	var catalogue = map[string]Config{
		"p1": {Mode: "trunk", PVID: 10, TaggedAll: true},
	}
	var spec = Spec{Rules: []Rule{
		{Label: "uplink", Select: Selector{Media: MediaSFP}, PortProfileID: "p1"},
	}}
	var plan = Evaluate(spec, Expand(json.RawMessage(facade8)), catalogue)
	if len(plan.Issues) != 0 {
		t.Fatalf("no refusal expected: %v", plan.Issues)
	}
	var p, _ = byPort(plan, "sfp1")
	if p.Config.PVID != 10 || !p.Config.TaggedAll {
		t.Errorf("port profile configuration was not reused: %+v", p.Config)
	}
}

// TestSpecDUnProfilDePriseSeLitDirectement : a port profile spec
// (port_profiles.spec) and a rule configuration share the SAME shape. If they
// diverged, translation in the middle would be required — and that is where
// "tagged: all" gets lost.
func TestSpecDUnProfilDePriseSeLitDirectement(t *testing.T) {
	var c Config
	var err = json.Unmarshal([]byte(
		`{"description":"Uplink","mode":"trunk","pvid":10,"tagged":"all","poe":false,"eng_only":true}`), &c)
	if err != nil {
		t.Fatalf("unreadable port profile spec: %v", err)
	}
	if c.Mode != "trunk" || c.PVID != 10 || !c.TaggedAll {
		t.Errorf("spec misread: %+v", c)
	}
	var liste Config
	if json.Unmarshal([]byte(`{"mode":"trunk","pvid":1,"tagged":[30,20]}`), &liste) != nil {
		t.Fatal("unreadable VLAN list")
	}
	// Sorted on read: two entries in different order must not produce two
	// configurations diff would see as distinct.
	if len(liste.Tagged) != 2 || liste.Tagged[0] != 20 {
		t.Errorf("list not normalized: %v", liste.Tagged)
	}
}

// TestUnPortLibreNeGardeAucunResidu : setting a port to "libre" must also clear
// PVID and tagged VLANs it carried. Leaving them would make them reappear on the
// next pass to access, and nobody would know where they came from.
func TestUnPortLibreNeGardeAucunResidu(t *testing.T) {
	var c = Config{Mode: "libre", PVID: 42, Tagged: []int{10, 20}}
	var f = c.Fields()
	if string(f["pvid"]) != "null" {
		t.Errorf("residual pvid: %s", f["pvid"])
	}
	if string(f["tagged"]) != "[]" {
		t.Errorf("residual tagged VLANs: %s", f["tagged"])
	}
}

// TestUnAccessEffaceLesVlansTagues : switching a trunk to access must REMOVE its
// tagged VLANs. Not writing the key would leave them in the database, and the port
// would remain member of VLANs the screen no longer shows.
func TestUnAccessEffaceLesVlansTagues(t *testing.T) {
	var f = Config{Mode: "access", PVID: 20}.Fields()
	if string(f["tagged"]) != "[]" {
		t.Errorf("tagged VLANs must be explicitly cleared: %s", f["tagged"])
	}
	if string(f["pvid"]) != "20" {
		t.Errorf("pvid: %s", f["pvid"])
	}
}

// TestFacadeIllisibleNeFaitPasEchouer : a device whose model nobody filled in has
// an empty or broken facade. The screen must be able to say so — "no known ports"
// — instead of refusing to display.
func TestFacadeIllisibleNeFaitPasEchouer(t *testing.T) {
	if len(Expand(json.RawMessage(`anything`))) != 0 {
		t.Error("unreadable facade must return an empty list")
	}
	if len(Expand(nil)) != 0 {
		t.Error("absent facade must return an empty list")
	}
	// An outlier count is ignored, not expanded.
	if len(Expand(json.RawMessage(`[{"prefix":"x","count":999999}]`))) != 0 {
		t.Error("outlier count must not be expanded")
	}
}

// TestRegleSansConfigurationEstRefusee : a rule that does not say what it sets is
// incomplete input, not a neutral rule. Letting it through would write an empty
// mode on ports it designates.
func TestRegleSansConfigurationEstRefusee(t *testing.T) {
	var spec = Spec{Rules: []Rule{{Label: "empty", Select: Selector{}}}}
	var plan = Evaluate(spec, Expand(json.RawMessage(facade8)), nil)
	if len(plan.Issues) != 1 {
		t.Fatalf("refusal expected: %v", plan.Issues)
	}
	if len(plan.Ports) != 0 {
		t.Errorf("no port must be assigned: %v", names(plan.Ports))
	}
}

// TestTrancheSansNombreEstRefusee : "first N" with N missing matches nothing on
// any facade. That is an input error, and the safe default is to say so rather
// than silently apply an inert rule.
func TestTrancheSansNombreEstRefusee(t *testing.T) {
	var spec = Spec{Rules: []Rule{
		{Label: "first", Select: Selector{Slice: SliceFirst},
			Config: cfg("access", 10, nil, false, false)},
	}}
	var plan = Evaluate(spec, Expand(json.RawMessage(facade8)), nil)
	if len(plan.Issues) != 1 {
		t.Fatalf("refusal expected: %v", plan.Issues)
	}
}

// TestSelectionParGroupeDeFacade : targeting a group uses a SUBSTRING of its
// label, case-insensitive — labels are not normalized across models, and requiring
// equality would silence the rule on the second model.
func TestSelectionParGroupeDeFacade(t *testing.T) {
	var spec = Spec{Rules: []Rule{
		{Label: "poe", Select: Selector{Group: "poe"},
			Config: cfg("access", 20, nil, false, true)},
	}}
	var plan = Evaluate(spec, Expand(json.RawMessage(facade8)), nil)
	if plan.Rules[0].Won != 8 {
		t.Errorf("group \"ETHER 1-8 · PoE\" should match: %+v", plan.Rules[0])
	}
}

// TestSelectionParCapacitePoE : distinguish powerable ports without going through
// media. On a facade where copper is NOT PoE, a "poe: yes" rule must take nothing
// rather than all copper.
func TestSelectionParCapacitePoE(t *testing.T) {
	var sansPoE = `[{"prefix":"ether","count":4,"speed":"1G","label":"ETHER"}]`
	var spec = Spec{Rules: []Rule{
		{Label: "powered", Select: Selector{PoE: TriYes},
			Config: cfg("access", 20, nil, false, true)},
	}}
	var nu = Evaluate(spec, Expand(json.RawMessage(sansPoE)), nil)
	if nu.Rules[0].Won != 0 {
		t.Errorf("no PoE ports on this facade: %+v", nu.Rules[0])
	}
	var alimente = Evaluate(spec, Expand(json.RawMessage(facade8)), nil)
	if alimente.Rules[0].Won != 8 {
		t.Errorf("8 PoE ports expected: %+v", alimente.Rules[0])
	}
}
