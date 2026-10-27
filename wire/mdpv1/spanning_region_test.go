package mdpv1

import "testing"

// The region is what decides whether two switches are in the same MST domain,
// and 802.1s decides it by DIGEST — an MD5 over the full VLAN-to-instance table,
// compared with the name and revision. A mismatch is silent: the neighbour is
// simply treated as an ordinary RSTP bridge. So every property below is one that
// cannot be observed in the field until a port blocks somewhere unexpected.

func TestSiteMSTRegionIsDeterministic(t *testing.T) {
	// Same site, same VLANs in a different order, must give the same region —
	// otherwise two devices provisioned from the same data land in two regions.
	var a = SiteMSTRegion("site-lab2", 1, []int{99, 30, 10})
	var b = SiteMSTRegion("site-lab2", 1, []int{10, 99, 30})
	if a.Name != b.Name || a.Revision != b.Revision {
		t.Fatalf("name/revision differ: %+v vs %+v", a, b)
	}
	var ia, ib = a.InstanceVLANs[1], b.InstanceVLANs[1]
	if len(ia) != len(ib) {
		t.Fatalf("instance 1 differs: %v vs %v", ia, ib)
	}
	var i int
	for i = range ia {
		if ia[i] != ib[i] {
			t.Fatalf("instance 1 order differs: %v vs %v", ia, ib)
		}
	}
}

// VLAN 1 belongs to the CIST on every platform: some refuse to map it into an
// instance and others ignore the attempt, and either way the digests stop
// matching between a device that took it and one that did not.
func TestSiteMSTRegionExcludesVlan1(t *testing.T) {
	var r = SiteMSTRegion("s", 0, []int{1, 10, 20})
	var v int
	for _, v = range r.InstanceVLANs[1] {
		if v == 1 {
			t.Fatal("VLAN 1 was mapped into an instance")
		}
	}
	if len(r.InstanceVLANs[1]) != 2 {
		t.Errorf("instance 1 = %v, want the two real VLANs", r.InstanceVLANs[1])
	}
}

func TestSiteMSTRegionDropsDuplicatesAndOutOfRange(t *testing.T) {
	var r = SiteMSTRegion("s", 0, []int{10, 10, 0, -5, 4095, 9000, 20})
	var want = []int{10, 20}
	var got = r.InstanceVLANs[1]
	if len(got) != len(want) {
		t.Fatalf("instance 1 = %v, want %v", got, want)
	}
	var i int
	for i = range want {
		if got[i] != want[i] {
			t.Fatalf("instance 1 = %v, want %v", got, want)
		}
	}
}

// A site with no VLANs beyond the default still has a region: name and revision
// are what a device is placed by, and an empty instance map is a valid region
// where everything sits in the CIST.
func TestSiteMSTRegionWithNoSiteVlans(t *testing.T) {
	var r = SiteMSTRegion("s", 3, []int{1})
	if !r.Configured() {
		t.Fatal("a region with a name must count as configured")
	}
	if len(r.InstanceVLANs) != 0 {
		t.Errorf("instance map = %v, want empty", r.InstanceVLANs)
	}
}

// The name is bounded: 32 characters is the ceiling on every platform surveyed,
// and a name silently truncated by the device on one side and not the other is
// two regions.
func TestSiteMSTRegionBoundsTheName(t *testing.T) {
	var long = "abcdefghijklmnopqrstuvwxyz0123456789-and-more"
	var r = SiteMSTRegion(long, 0, nil)
	if len(r.Name) != 32 {
		t.Errorf("name is %d characters, want it capped at 32", len(r.Name))
	}
}

// A nameless region is not a region: platform defaults derive the name from the
// bridge MAC, which puts every device in a region of its own.
func TestUnnamedRegionIsNotConfigured(t *testing.T) {
	if (MSTRegion{Revision: 2}).Configured() {
		t.Error("a region with no name reported itself configured")
	}
	if (MSTRegion{Name: "   "}).Configured() {
		t.Error("a whitespace name reported itself configured")
	}
}

// GenericMSTRegionName exists because the previous name — the site SLUG — was
// renameable, and renaming a region EVICTS every device still carrying the old
// name (cf. ADR-0021). The properties below are the ones that make the new name
// incapable of that, and none of them can be observed in the field: a renamed
// region shows up as a port blocking somewhere unexpected, weeks later.

// The only property that matters: the same site identifier always yields the same
// name. Anything else is a rename, and a rename is an outage.
func TestGenericMSTRegionNameIsStable(t *testing.T) {
	var a = GenericMSTRegionName("7f0d238d-f180-440d-be56-cd1a4bcbb225")
	var b = GenericMSTRegionName("7f0d238d-f180-440d-be56-cd1a4bcbb225")
	if a != b {
		t.Fatalf("same site gave two names: %q then %q", a, b)
	}
	if a != "mst-7f0d238df180" {
		t.Errorf("name = %q, want mst-7f0d238df180 — this value is ON HARDWARE, so "+
			"changing the formula renames live regions and must be a new ADR", a)
	}
}

// Dashes are not part of the identity: the same UUID written either way has to
// give one name, or the region depends on which caller formatted it.
func TestGenericMSTRegionNameIgnoresDashesAndCase(t *testing.T) {
	var dashed = GenericMSTRegionName("7f0d238d-f180-440d-be56-cd1a4bcbb225")
	var bare = GenericMSTRegionName("7f0d238df180440dbe56cd1a4bcbb225")
	var upper = GenericMSTRegionName("7F0D238D-F180-440D-BE56-CD1A4BCBB225")
	if dashed != bare || dashed != upper {
		t.Errorf("same UUID, three spellings, three names: %q / %q / %q",
			dashed, bare, upper)
	}
}

// 32 characters is the cap every surveyed platform enforces, and a name silently
// truncated by firmware is a name that no longer matches its neighbours'.
func TestGenericMSTRegionNameFitsEveryPlatform(t *testing.T) {
	var n = GenericMSTRegionName("7f0d238d-f180-440d-be56-cd1a4bcbb225")
	if len(n) != 16 {
		t.Errorf("name %q is %d characters, want 16", n, len(n))
	}
}

// Nothing hex-like in, nothing out — and "" is refused by Configured(), so the
// apply leaves spanning tree alone instead of writing a region named "mst-".
func TestGenericMSTRegionNameRefusesAnEmptyIdentifier(t *testing.T) {
	var n = GenericMSTRegionName("")
	if n != "" {
		t.Errorf("empty identifier gave %q", n)
	}
	if (MSTRegion{Name: GenericMSTRegionName("zzz-----")}).Configured() {
		t.Error("an identifier with no hex content produced a configured region")
	}
}

// SiteMSTRegion must not decide the name any more: the caller owns the
// pinned-or-derived policy, because the pin lives in the site plan and this
// package knows nothing about site plans.
func TestSiteMSTRegionTakesTheNameItIsGiven(t *testing.T) {
	var r = SiteMSTRegion("mst-7f0d238df180", 1, []int{42})
	if r.Name != "mst-7f0d238df180" {
		t.Errorf("name = %q, want the caller's value verbatim", r.Name)
	}
}
