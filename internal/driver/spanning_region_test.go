package driver

import "testing"

// The region is what decides whether two switches are in the same MST domain,
// and 802.1s decides it by DIGEST — an MD5 over the full VLAN-to-instance table,
// compared with the name and revision. A mismatch is silent: the neighbour is
// simply treated as an ordinary RSTP bridge. So every property below is one that
// cannot be observed in the field until a port blocks somewhere unexpected.

func TestSiteMSTRegionIsDeterministic(t *testing.T) {
	// Same site, same VLANs in a different order, must give the same region —
	// otherwise two devices provisioned from the same data land in two regions.
	var a = SiteMSTRegion("caserne-lab", 1, []int{99, 30, 10})
	var b = SiteMSTRegion("caserne-lab", 1, []int{10, 99, 30})
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
