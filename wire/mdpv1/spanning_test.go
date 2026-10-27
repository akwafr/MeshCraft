package mdpv1

import "testing"

func TestNormalizeSTPMode(t *testing.T) {
	var cases = []struct {
		in, want string
	}{
		{"rstp-pvst", "rstp"},
		{"RSTP", "rstp"},
		{"mstp", "mstp"},
		{"none", "none"},
		{"off", "none"},
		{"stp", "stp"},
	}
	var i int
	for i = range cases {
		var got = NormalizeSTPMode(cases[i].in)
		if got != cases[i].want {
			t.Errorf("NormalizeSTPMode(%q)=%q want %q", cases[i].in, got, cases[i].want)
		}
	}
}

func TestResolveSTPMode(t *testing.T) {
	var mode string
	var disable, ok bool

	mode, disable, ok = ResolveSTPMode(
		[]string{"mstp", "rstp-pvst", "none"},
		[]string{"rstp"},
	)
	if !ok || disable || mode != "rstp" {
		t.Errorf("swos-like: mode=%q disable=%v ok=%v", mode, disable, ok)
	}

	// SwOS cannot disable via preference "none" — skip past it, then no match.
	mode, disable, ok = ResolveSTPMode(
		[]string{"mstp", "none"},
		[]string{"rstp"},
	)
	if ok {
		t.Errorf("swos must not honor none: mode=%q disable=%v ok=%v", mode, disable, ok)
	}

	mode, disable, ok = ResolveSTPMode(
		[]string{"mstp"},
		[]string{"rstp"},
	)
	if ok {
		t.Errorf("no match should fail: mode=%q disable=%v", mode, disable)
	}

	mode, disable, ok = ResolveSTPMode(
		[]string{"stp", "rstp-pvst"},
		[]string{"none", "stp", "rstp", "mstp"},
	)
	if !ok || disable || mode != "stp" {
		t.Errorf("first match stp: mode=%q disable=%v ok=%v", mode, disable, ok)
	}

	mode, disable, ok = ResolveSTPMode(
		[]string{"mstp", "none"},
		[]string{"none", "stp", "rstp", "mstp"},
	)
	if !ok || disable || mode != "mstp" {
		t.Errorf("routeros/instanton mstp: mode=%q disable=%v ok=%v", mode, disable, ok)
	}

	mode, disable, ok = ResolveSTPMode(
		[]string{"none"},
		[]string{"none", "stp", "rstp", "mstp"},
	)
	if !ok || !disable || mode != "" {
		t.Errorf("routeros/instanton none: mode=%q disable=%v ok=%v", mode, disable, ok)
	}
}

// TestResolveSTPModeEmptyPreferenceResolvesToNothing pins the per-site off
// switch at the level where it was broken.
//
// This function substituted DefaultSTPPreference on an empty list until
// 2026-08-09, which made "the operator emptied this site's Preferred list"
// indistinguishable from "this site has never had one" — and only the caller
// (api.stpPreferenceOf) can tell those apart. `none` is IN capabilities here on
// purpose: the answer must be "resolved to nothing", not "resolved to disable".
// Pushing an STP-off gesture at a site that asked to be left alone would be the
// worse of the two failures.
func TestResolveSTPModeEmptyPreferenceResolvesToNothing(t *testing.T) {
	var caps = []string{"none", "stp", "rstp", "mstp"}
	var mode, disable, ok = ResolveSTPMode(nil, caps)
	if ok || disable || mode != "" {
		t.Errorf("nil preference: mode=%q disable=%v ok=%v — want nothing resolved",
			mode, disable, ok)
	}
	mode, disable, ok = ResolveSTPMode([]string{}, caps)
	if ok || disable || mode != "" {
		t.Errorf("empty preference: mode=%q disable=%v ok=%v — want nothing resolved",
			mode, disable, ok)
	}
}

func TestSanitizeSTPPreference(t *testing.T) {
	var got = SanitizeSTPPreference([]string{"mstp", "rstp", "bogus", "mstp", "none", "rstp-pvst"})
	if len(got) != 4 || got[0] != "mstp" || got[1] != "rstp" || got[2] != "none" || got[3] != "rstp-pvst" {
		t.Errorf("got %#v", got)
	}
}

// AN UNQUALIFIED ROOT DESIGNATION COVERS THE WHOLE REGION.
//
// The regression this pins is a false configuration rather than a crash, and it
// only appears once a region maps VLANs: SiteMSTRegion puts every site VLAN
// above 1 into instance 1, so a designation resolved to the CIST alone makes a
// switch root of the one tree that carries nothing — while the instance holding
// the management VLAN elects its root by lowest MAC.
func TestRootsOfCoversEveryInstanceTheRegionDefines(t *testing.T) {
	var plan = SpanningTreePlan{
		Mode:      "mstp",
		SuperRoot: true,
		Region:    SiteMSTRegion("site-lab2", 1, []int{1, 42, 98, 99, 100}),
	}
	var got = plan.RootsOf()
	if len(got) != 2 || got[0] != 0 || got[1] != 1 {
		t.Fatalf("RootsOf() = %v, want [0 1] — the CIST and the instance holding "+
			"the site's VLANs", got)
	}

	// A region with no instance map (nothing to load-balance) is still the CIST.
	var bare = SpanningTreePlan{Mode: "mstp", SuperRoot: true}
	if got = bare.RootsOf(); len(got) != 1 || got[0] != 0 {
		t.Errorf("RootsOf() with no region = %v, want [0]", got)
	}

	// A caller that NAMES instances is obeyed verbatim: naming instance 0 is how
	// "the CIST alone" is expressed now that empty means the whole region.
	var named = SpanningTreePlan{
		Mode: "mstp", SuperRoot: true, RootInstances: []int{0},
		Region: SiteMSTRegion("site-lab2", 1, []int{42, 99}),
	}
	if got = named.RootsOf(); len(got) != 1 || got[0] != 0 {
		t.Errorf("RootsOf() with an explicit [0] = %v, want [0]", got)
	}

	// No designation at all writes no priority anywhere.
	var none = SpanningTreePlan{Mode: "mstp", Region: SiteMSTRegion("s", 1, []int{42})}
	if got = none.RootsOf(); len(got) != 0 {
		t.Errorf("RootsOf() without SuperRoot = %v, want none", got)
	}
}
