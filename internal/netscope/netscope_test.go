package netscope

import "testing"

// Two sites of the same customer, both on 192.168.1.0/24, each behind its own
// proxy. This is the scenario that motivates this entire package.
const (
	domA = "domain-agency-a"
	domB = "domain-agency-b"
)

func agency(domain string) []Prefix {
	return []Prefix{{Prefix: "192.168.1.0/24", DomainID: domain, Enabled: true}}
}

// TestDomainOfSeparatesTwoSitesSharingSamePlan prevents the costliest model
// confusion: two distinct networks reusing the same private plan must stay
// distinct. If they merged, the two 192.168.1.10 switches from both sites would
// become one record.
func TestDomainOfSeparatesTwoSitesSharingSamePlan(t *testing.T) {
	var a = DomainOf(agency(domA), "home-a", "192.168.1.10")
	var b = DomainOf(agency(domB), "home-b", "192.168.1.10")
	if a == b {
		t.Fatalf("two distinct networks merged: %q", a)
	}
	if a != domA || b != domB {
		t.Errorf("domains resolved incorrectly: %q / %q", a, b)
	}
}

// TestDomainOfMergesTwoPathsToOneNetwork: the other half of the problem. When
// the operator DECLARES that two proxies see the same network, observations must
// join — otherwise the same switch occupies two adoption queue rows and gets
// adopted twice.
func TestDomainOfMergesTwoPathsToOneNetwork(t *testing.T) {
	var shared = "domain-server-vlan"
	var viaA = DomainOf(agency(shared), "home-a", "192.168.1.10")
	var viaB = DomainOf(agency(shared), "home-b", "192.168.1.10")
	if viaA != viaB {
		t.Fatalf("a declared shared network stays split: %q vs %q", viaA, viaB)
	}
}

// TestDomainOfMostSpecificWins: an operator who detaches a /24 from a /16
// expresses an EXCEPTION. An exception that lost to the general rule would be
// useless.
func TestDomainOfMostSpecificWins(t *testing.T) {
	var prefixes = []Prefix{
		{Prefix: "10.0.0.0/16", DomainID: "large", Enabled: true},
		{Prefix: "10.0.5.0/24", DomainID: "exception", Enabled: true},
	}
	var specific = DomainOf(prefixes, "home", "10.0.5.7")
	if specific != "exception" {
		t.Errorf("most specific range must win, got %q", specific)
	}
	var general = DomainOf(prefixes, "home", "10.0.9.7")
	if general != "large" {
		t.Errorf("outside the exception, general rule applies, got %q", general)
	}
}

// TestDomainOfDisabledRangeKeepsDomain: disabling a scan does not erase the
// network. Without this rule, turning off scan for a range would change every
// candidate merge key and duplicate them on the next pass.
func TestDomainOfDisabledRangeKeepsDomain(t *testing.T) {
	var prefixes = []Prefix{{Prefix: "192.168.1.0/24", DomainID: domA, Enabled: false}}
	var d = DomainOf(prefixes, "home", "192.168.1.10")
	if d != domA {
		t.Errorf("a disabled range still describes a real network, got %q", d)
	}
}

// TestDomainOfFallsBackToProxyDomain: manual probe of an address outside any
// declared range must still land somewhere. Without fallback, those observations
// would share an anonymous space where all private IPs blur together.
func TestDomainOfFallsBackToProxyDomain(t *testing.T) {
	var outside = DomainOf(agency(domA), "home-a", "172.16.4.9")
	if outside != "home-a" {
		t.Errorf("expected fallback to proxy domain, got %q", outside)
	}
	var invalid = DomainOf(nil, "home-a", "not-an-address")
	if invalid != "home-a" {
		t.Errorf("invalid input must not yield empty domain, got %q", invalid)
	}
}

// TestIdentityKeySerialAndMACIgnoreDomain: a vendor serial is valid everywhere.
// If domain mattered, a device reached via two declared distinct paths would
// become two devices — even though we hold proof to the contrary.
func TestIdentityKeySerialAndMACIgnoreDomain(t *testing.T) {
	if IdentityKey("SG12345", "", domA, "192.168.1.10") !=
		IdentityKey("SG12345", "", domB, "10.0.0.4") {
		t.Error("serial number must beat domain and address")
	}
	if IdentityKey("", "aa:bb:cc:dd:ee:ff", domA, "192.168.1.10") !=
		IdentityKey("", "aa:bb:cc:dd:ee:ff", domB, "10.0.0.4") {
		t.Error("MAC is globally unique: it must beat domain")
	}
}

// TestIdentityKeyAnonymousQualifiedByDomain: the heart of the fallback. Two
// silent devices at the same address in two distinct networks must NOT merge; the
// same silent device seen via two paths of one declared shared network must merge.
func TestIdentityKeyAnonymousQualifiedByDomain(t *testing.T) {
	if IdentityKey("", "", domA, "192.168.1.10") == IdentityKey("", "", domB, "192.168.1.10") {
		t.Error("two distinct networks must not share an identity key")
	}
	if IdentityKey("", "", domA, "192.168.1.10") != IdentityKey("", "", domA, "192.168.1.10") {
		t.Error("a declared shared network must produce a stable key")
	}
	// Unknown domain: isolate rather than merge everything.
	if IdentityKey("", "", "", "192.168.1.10") == IdentityKey("", "", domA, "192.168.1.10") {
		t.Error("an observation without domain must not merge with another")
	}
}

// TestSelectWatchTargetsPassesNormalCase: the common case, and therefore the
// most important. A false rejection here would strip watch from a healthy fleet.
func TestSelectWatchTargetsPassesNormalCase(t *testing.T) {
	var cands = []WatchCandidate{
		{DeviceID: "d1", Name: "sw-a", IP: "192.168.1.10", DomainID: domA},
		{DeviceID: "d2", Name: "sw-b", IP: "192.168.1.11", DomainID: domA},
	}
	var kept, rejected = SelectWatchTargets(cands, map[string]bool{domA: true})
	if len(kept) != 2 || len(rejected) != 0 {
		t.Fatalf("no conflict here: %d kept, %d rejected (%+v)", len(kept), len(rejected), rejected)
	}
}

// TestSelectWatchTargetsArbitratesByDomain: two sites on 192.168.1.0/24, one
// instance proxy watching both in its coverage. The proxy pings by ADDRESS:
// without arbitration, the second target overwrites the first and one switch's
// state is attributed to the other. Keep the one whose network is actually
// declared on this proxy.
func TestSelectWatchTargetsArbitratesByDomain(t *testing.T) {
	var cands = []WatchCandidate{
		{DeviceID: "d1", Name: "agency-a-sw1", IP: "192.168.1.10", DomainID: domA},
		{DeviceID: "d2", Name: "agency-b-sw1", IP: "192.168.1.10", DomainID: domB},
	}
	var kept, rejected = SelectWatchTargets(cands, map[string]bool{domB: true})
	if len(kept) != 1 || kept[0].DeviceID != "d2" {
		t.Fatalf("proxy declares only domain B: d2 must remain (%+v)", kept)
	}
	if len(rejected) != 1 || rejected[0].DeviceID != "d1" {
		t.Fatalf("d1 must be excluded with a reason: %+v", rejected)
	}
	if rejected[0].Reason == "" {
		t.Error("watch removal without reason looks like a meshcraft failure")
	}
}

// TestSelectWatchTargetsRefusesToGuess prevents silent error: when nothing
// breaks the tie between two devices at the same address, none are watched. An
// unwatched device is visible in the UI; a green indicator on a powered-off
// device is not.
func TestSelectWatchTargetsRefusesToGuess(t *testing.T) {
	var cands = []WatchCandidate{
		{DeviceID: "d1", Name: "agency-a-sw1", IP: "192.168.1.10", DomainID: domA},
		{DeviceID: "d2", Name: "agency-b-sw1", IP: "192.168.1.10", DomainID: domB},
	}
	// Proxy declares BOTH domains: no arbiter left.
	var kept, rejected = SelectWatchTargets(cands, map[string]bool{domA: true, domB: true})
	if len(kept) != 0 {
		t.Fatalf("nothing breaks the tie: none must be watched (%+v)", kept)
	}
	if len(rejected) != 2 {
		t.Fatalf("both must be excluded with reason: %+v", rejected)
	}

	// No declared domain either: same conclusion.
	kept, rejected = SelectWatchTargets(cands, map[string]bool{})
	if len(kept) != 0 || len(rejected) != 2 {
		t.Fatalf("with no declared domain, do not guess: %d kept, %d rejected", len(kept), len(rejected))
	}
}

// TestSelectWatchTargetsManualEntryDevice: a manually created device has no
// domain. While alone on its address, it stays watched — requiring a domain would
// cut watch for the entire fleet predating this model.
func TestSelectWatchTargetsManualEntryDevice(t *testing.T) {
	var cands = []WatchCandidate{{DeviceID: "d1", Name: "sw", IP: "10.0.0.4"}}
	var kept, rejected = SelectWatchTargets(cands, map[string]bool{domA: true})
	if len(kept) != 1 || len(rejected) != 0 {
		t.Fatalf("device without domain and without conflict stays watched: %+v / %+v", kept, rejected)
	}
}

// TestSelectWatchTargetsStableOrder: watch config is pushed whole on every
// change. If order varied, every recalculation would look like a change and the
// proxy would reconfigure for nothing.
func TestSelectWatchTargetsStableOrder(t *testing.T) {
	var cands = []WatchCandidate{
		{DeviceID: "d1", Name: "a", IP: "10.0.0.1", DomainID: domA},
		{DeviceID: "d2", Name: "b", IP: "10.0.0.2", DomainID: domA},
		{DeviceID: "d3", Name: "c", IP: "10.0.0.3", DomainID: domA},
	}
	var i int
	for i = 0; i < 20; i++ {
		var kept, _ = SelectWatchTargets(cands, map[string]bool{domA: true})
		if len(kept) != 3 || kept[0].DeviceID != "d1" || kept[2].DeviceID != "d3" {
			t.Fatalf("unstable order on iteration %d: %+v", i, kept)
		}
	}
}
