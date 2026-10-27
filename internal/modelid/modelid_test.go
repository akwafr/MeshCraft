package modelid

import "testing"

// The bug this package exists for: three labels, twelve devices, one switch.
// The three forms MUST converge on one key and one label, and the raw strings
// are the ones actually recorded in the reference installation.
func TestInstantOn1930FormsConverge(t *testing.T) {
	var forms = []string{
		"Aruba Instant On 1930 48G 4SFP/SFP+ Switch",
		"HPE Networking Instant On Switch 48p Gigabit 4p SFP+ 10G 1930",
		"HPE Networking Instant On Switch 48p Gigabit 4p SFP+ 1930 JL685A",
	}
	var want = "Aruba Instant On 1930 48G 4SFP/SFP+ Switch"
	var raw string
	for _, raw = range forms {
		var id = Of(raw)
		if id.Label != want {
			t.Errorf("Of(%q).Label = %q, want %q", raw, id.Label, want)
		}
		if id.Key != Of(forms[0]).Key {
			t.Errorf("Of(%q).Key = %q: the three grammars must share one key "+
				"(got %q for the first form)", raw, id.Key, Of(forms[0]).Key)
		}
		if id.Key == "" {
			t.Errorf("Of(%q): empty key — an identity is never empty", raw)
		}
	}
}

// The "10G" trap: in the sysDescr grammar it is a speed, not ten copper ports.
// The ordering of the two grammars is what prevents it, so it is pinned here.
func TestSysDescrTenGigIsNotTenPorts(t *testing.T) {
	var id = Of("HPE Networking Instant On Switch 48p Gigabit 4p SFP+ 10G 1930")
	var want = "Aruba Instant On 1930 48G 4SFP/SFP+ Switch"
	if id.Label != want {
		t.Errorf("Label = %q, want %q (10G read as ten copper ports?)", id.Label, want)
	}
}

// The catalogue reference is kept, and kept OUT of the identity: it is absent
// from two of the three forms, so an identity resting on it would re-split the
// group (cf. ADR-0014).
func TestSKUIsKeptButNotPartOfTheIdentity(t *testing.T) {
	var with = Of("HPE Networking Instant On Switch 48p Gigabit 4p SFP+ 1930 JL685A")
	var without = Of("Aruba Instant On 1930 48G 4SFP/SFP+ Switch")
	if with.SKU != "JL685A" {
		t.Errorf("SKU = %q, want JL685A", with.SKU)
	}
	if without.SKU != "" {
		t.Errorf("SKU = %q: that form does not carry one, nothing may be invented",
			without.SKU)
	}
	if with.Key != without.Key {
		t.Errorf("keys differ (%q vs %q): the SKU must not enter the identity",
			with.Key, without.Key)
	}
}

// Geometry is part of the identity: two sizes of the same range are two models.
func TestGeometrySeparatesSizesWithinAFamily(t *testing.T) {
	var a = Of("Aruba Instant On 1930 24G 4SFP/SFP+ Switch")
	var b = Of("Aruba Instant On 1930 48G 4SFP/SFP+ Switch")
	if a.Key == b.Key {
		t.Errorf("24G and 48G share key %q: a port count is part of the identity", a.Key)
	}
	var c = Of("HPE Networking Instant On Switch 24p Gigabit 4p SFP+ 1930")
	if c.Key != a.Key {
		t.Errorf("Key = %q, want %q: both grammars of the 24G form", c.Key, a.Key)
	}
	// The 1830 range, whose cages are plain SFP: the family number is read, not
	// assumed, and the plus is not invented.
	var d = Of("HPE Networking Instant On Switch 8p Gigabit 2p SFP 1830")
	var wantD = "Aruba Instant On 1830 8G 2SFP Switch"
	if d.Label != wantD {
		t.Errorf("Label = %q, want %q", d.Label, wantD)
	}
}

// A family named without a port count identifies a RANGE, not a switch. The raw
// label must stay rather than be collapsed onto a size we do not know.
func TestFamilyWithoutGeometryKeepsTheRawLabel(t *testing.T) {
	var id = Of("Aruba Instant On 1930")
	if id.Label != "Aruba Instant On 1930" {
		t.Errorf("Label = %q: a range must not be canonicalized into a model", id.Label)
	}
}

// Every other vendor in the reference installation: one grammar, so the raw
// label is ALREADY canonical and must come back untouched. A regression here is
// the dangerous one — it would rename hardware nobody complained about.
func TestOtherVendorsAreReturnedUntouched(t *testing.T) {
	var labels = []string{
		"CSS326-24G-2S+",
		"CRS326-24G-2S+",
		"CRS305-1G-4S+",
		"WS-C2960X-24TD-L",
		"S3400-48T4SP",
		"S3900-24T4S",
		"DCS-7050SX-64-R",
		"ProCurve 2610-48",
		"EP-S16",
		"Generic-24G-2X",
	}
	var raw string
	for _, raw = range labels {
		var id = Of(raw)
		if id.Label != raw {
			t.Errorf("Of(%q).Label = %q: no evidence of a second grammar, "+
				"nothing may be renamed", raw, id.Label)
		}
		if id.Key == "" {
			t.Errorf("Of(%q): empty key — an unrecognized label is its own "+
				"identity, not an absent one", raw)
		}
	}
}

// Two unknowns never merge: that would declare different hardware identical,
// which is worse than the duplicate this package removes.
func TestUnknownsNeverMerge(t *testing.T) {
	if Of("CRS326-24G-2S+").Key == Of("CSS326-24G-2S+").Key {
		t.Error("CRS326 and CSS326 share a key: two unknowns must stay apart")
	}
}

// Spacing and case are not identity. A label that travelled through a UI
// carries non-breaking spaces and doubled blanks.
func TestWhitespaceAndCaseAreNotIdentity(t *testing.T) {
	if Of("  CSS326-24G-2S+  ").Key != Of("CSS326-24G-2S+").Key {
		t.Error("surrounding whitespace changed the key")
	}
	if Of("css326-24g-2s+").Key != Of("CSS326-24G-2S+").Key {
		t.Error("case changed the key")
	}
	if Of("Aruba  Instant On 1930 48G 4SFP/SFP+  Switch").Label !=
		"Aruba Instant On 1930 48G 4SFP/SFP+ Switch" {
		t.Error("doubled spaces survived normalization")
	}
}

// An empty label has no identity and says so, instead of inventing a group that
// every unnamed device would join.
func TestEmptyLabelHasNoIdentity(t *testing.T) {
	var id = Of("   ")
	if id.Key != "" || id.Label != "" || id.SKU != "" {
		t.Errorf("Of(blank) = %+v, want the zero value", id)
	}
}
