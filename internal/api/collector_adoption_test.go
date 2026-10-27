package api

import (
	"testing"
	"time"

	"meshcraft/internal/model"
)

// Collector choice at adoption, and the source bounds that depend on it.
//
// What these tests lock in is a silent end-to-end failure, observed on real
// hardware: an adopted device without a collector gets an SNMP community bounded
// to the site plan subnet, its proxy polls from a DIFFERENT address because it
// is routed there, and the agent stays silent. Nothing fails — the apply is
// "conformant", the page is green, the chart is empty.
//
// No database, like the rest of the package: the decision is pure on purpose.

// Reference fleet for these tests: two covering proxies and one that does not cover.
var proxyLocal = model.Proxy{ID: "local", Name: "Local", Global: true}
var proxyBranch = model.Proxy{ID: "branch", Name: "Branch"}
var proxyElsewhere = model.Proxy{ID: "elsewhere", Name: "Elsewhere"}

var couvrants = []model.Proxy{proxyLocal, proxyBranch}

func vu(proxyID, name string, latency int, when time.Time) model.Sighting {
	return model.Sighting{ProxyID: proxyID, ProxyName: name, IP: "10.20.0.36",
		LatencyMS: latency, LastSeen: when}
}

// TestCollectorChosenByFastestSighting: between two proxies that SAW the
// device, measured latency decides — not list order.
func TestCollectorChosenByFastestSighting(t *testing.T) {
	var t0 = time.Date(2026, 7, 29, 18, 0, 0, 0, time.UTC)
	var cand = model.Discovered{
		ProxyID: "local", ProxyName: "Local", IP: "10.20.0.36",
		Sightings: []model.Sighting{
			vu("local", "Local", 12, t0),
			vu("branch", "Branch", 3, t0),
		},
	}
	var got, why = collectorForCandidate(cand, couvrants)
	if got.ID != "branch" || why != "" {
		t.Errorf("collector = %q (%q); expected \"branch\", sighting at 3 ms vs 12",
			got.ID, why)
	}
}

// TestCollectorIgnoresProxyNotCoveringSite — the refusal that matters, and it
// does not depend on the network: the site is chosen AT ADOPTION, so the proxy
// that saw the device may perfectly well not cover the retained site.
//
// Attachment grants NO coverage: writing it anyway would remove collection
// instead of moving it (cf. judgeCollector and notCoveringMessage).
func TestCollectorIgnoresProxyNotCoveringSite(t *testing.T) {
	var t0 = time.Date(2026, 7, 29, 18, 0, 0, 0, time.UTC)
	var cand = model.Discovered{
		ProxyID: "elsewhere", ProxyName: "Elsewhere", IP: "10.20.0.36",
		Sightings: []model.Sighting{
			vu("elsewhere", "Elsewhere", 1, t0), // fastest, and out of coverage
			vu("branch", "Branch", 40, t0),
		},
	}
	var got, why = collectorForCandidate(cand, couvrants)
	if got.ID != "branch" {
		t.Errorf("collector = %q; \"elsewhere\" is fastest but does not cover "+
			"the site: designating it would remove collection instead of moving it", got.ID)
	}
	if why != "" {
		t.Errorf("a collector was found, nothing to explain: %q", why)
	}
}

// TestNoCollectorWhenNobodySeesAndCovers: nothing is guessed, and the reason is
// returned so adoption can SAY it.
//
// Guessing would be worse than abstaining: a collector that cannot reach the
// device is dead collection the UI would show as configured.
func TestNoCollectorWhenNobodySeesAndCovers(t *testing.T) {
	var t0 = time.Date(2026, 7, 29, 18, 0, 0, 0, time.UTC)
	var cas = []struct {
		name string
		cand model.Discovered
		couv []model.Proxy
		word string
	}{
		{"no proxy covers the site",
			model.Discovered{ProxyID: "local", Sightings: []model.Sighting{vu("local", "Local", 5, t0)}},
			nil, "covers"},
		{"discovering proxy does not cover, and nothing else saw it",
			model.Discovered{ProxyID: "elsewhere", ProxyName: "Elsewhere",
				Sightings: []model.Sighting{vu("elsewhere", "Elsewhere", 5, t0)}},
			couvrants, "does not cover the site"},
		{"candidate without sighting and without origin proxy",
			model.Discovered{}, couvrants, "seen"},
	}
	var c = cas[0]
	for _, c = range cas {
		t.Run(c.name, func(t *testing.T) {
			var got, why = collectorForCandidate(c.cand, c.couv)
			if got.ID != "" {
				t.Fatalf("collector %q designated when nothing establishes it", got.ID)
			}
			if why == "" {
				t.Fatal("no reason returned: adoption could not say why " +
					"the device stays on coverage-based collection")
			}
			if !containsWord(why, c.word) {
				t.Errorf("reason must mention %q; got %q", c.word, why)
			}
		})
	}
}

// TestCollectorWithoutSightingFallsBackToOriginProxy: a manually entered
// candidate does not always have a latency sighting. The proxy that produced it
// remains a fact — that is how meshcraft learned the device exists.
func TestCollectorWithoutSightingFallsBackToOriginProxy(t *testing.T) {
	var cand = model.Discovered{ProxyID: "branch", ProxyName: "Branch", IP: "10.20.0.36"}
	var got, why = collectorForCandidate(cand, couvrants)
	if got.ID != "branch" || why != "" {
		t.Errorf("collector = %q (%q); expected \"branch\", the proxy that produced the "+
			"candidate", got.ID, why)
	}
}

// TestCollectorRejectsRevokedProxy: it remains in the coverage list and polls
// nothing anymore. Designating it collector would shut off collection for this device.
func TestCollectorRejectsRevokedProxy(t *testing.T) {
	var t0 = time.Date(2026, 7, 29, 18, 0, 0, 0, time.UTC)
	var revoked = model.Proxy{ID: "branch", Name: "Branch", Revoked: true}
	var cand = model.Discovered{
		ProxyID: "branch", ProxyName: "Branch",
		Sightings: []model.Sighting{vu("branch", "Branch", 2, t0), vu("local", "Local", 30, t0)},
	}
	var got, _ = collectorForCandidate(cand, []model.Proxy{proxyLocal, revoked})
	if got.ID != "local" {
		t.Errorf("collector = %q; \"branch\" is revoked, it no longer polls anything", got.ID)
	}
}

// TestUnmeasuredLatencyDoesNotBeatMeasurement: zero is not "instant", it is
// "not filled in". Treating it as the best value would make the proxy that
// measured nothing win.
func TestUnmeasuredLatencyDoesNotBeatMeasurement(t *testing.T) {
	var t0 = time.Date(2026, 7, 29, 18, 0, 0, 0, time.UTC)
	var cand = model.Discovered{
		Sightings: []model.Sighting{
			vu("local", "Local", 0, t0),
			vu("branch", "Branch", 25, t0),
		},
	}
	var got, _ = collectorForCandidate(cand, couvrants)
	if got.ID != "branch" {
		t.Errorf("collector = %q; latency 0 is not a measurement", got.ID)
	}
}

// TestCollectorChoiceIsStable: at equal latency and date, the identifier breaks ties.
//
// Without this last criterion, traversal order decides, and the same fleet adopted
// twice no longer compares — two different collectors for two identical devices.
func TestCollectorChoiceIsStable(t *testing.T) {
	var t0 = time.Date(2026, 7, 29, 18, 0, 0, 0, time.UTC)
	var cand = model.Discovered{
		Sightings: []model.Sighting{
			vu("local", "Local", 7, t0),
			vu("branch", "Branch", 7, t0),
		},
	}
	var inverse = model.Discovered{
		Sightings: []model.Sighting{
			vu("branch", "Branch", 7, t0),
			vu("local", "Local", 7, t0),
		},
	}
	var a, _ = collectorForCandidate(cand, couvrants)
	var b, _ = collectorForCandidate(inverse, couvrants)
	if a.ID != b.ID {
		t.Errorf("sighting order changes collector: %q vs %q", a.ID, b.ID)
	}
	if a.ID != "branch" {
		t.Errorf("collector = %q; at tie, smallest identifier breaks tie", a.ID)
	}
}

// TestMostRecentSightingBreaksLatencyTie: two equally fast proxies, one seen a
// minute ago and one a week ago — the first is the path that still exists.
func TestMostRecentSightingBreaksLatencyTie(t *testing.T) {
	var recent = time.Date(2026, 7, 29, 18, 0, 0, 0, time.UTC)
	var old = recent.Add(-7 * 24 * time.Hour)
	var cand = model.Discovered{
		Sightings: []model.Sighting{
			vu("branch", "Branch", 5, old),
			vu("local", "Local", 5, recent),
		},
	}
	var got, _ = collectorForCandidate(cand, couvrants)
	if got.ID != "local" {
		t.Errorf("collector = %q; at equal latency, most recent sighting breaks tie",
			got.ID)
	}
}

// --- Source bounds: the redundant /32 that must NOT be written ---

// TestAlreadyCoveredSourceIsNotAdded
//
// `addresses` is compared as a STRING to decide whether there is anything to
// write (`snmpConformant` on the connector side). Adding a /32 already
// contained in the plan subnet would rewrite the community of an entire
// conformant fleet on EVERY provisioning pass, on a platform where every write
// is a chance to get it wrong.
func TestAlreadyCoveredSourceIsNotAdded(t *testing.T) {
	var cas = []struct {
		prefixes []string
		addr     string
		expect   bool
		why      string
	}{
		{[]string{"10.20.0.0/16"}, "10.20.0.7", true,
			"proxy is IN the management subnet: nothing to add"},
		{[]string{"10.20.0.0/16"}, "10.42.0.67", false,
			"RELEVANT CASE: proxy is ROUTED to the management network, its " +
				"address is outside the plan — without its /32 the agent stays silent for it"},
		{[]string{"10.20.0.0/24"}, "10.20.1.5", false,
			"a /24 does not contain the neighbor"},
		{nil, "10.42.0.67", false, "no bound: nothing covers it"},
		{[]string{"not-a-prefix"}, "10.42.0.67", false,
			"unreadable bound covers nothing — add an extra /32 rather than " +
				"silencing the device for its collector"},
		{[]string{"10.20.0.0/16"}, "not-an-address", false,
			"unreadable address is covered by nothing"},
		{[]string{"2001:db8::/32"}, "2001:db8::1", true, "IPv6 too"},
		{[]string{"10.20.0.0/16"}, "2001:db8::1", false,
			"a v4 prefix does not contain a v6 address"},
	}
	var c = cas[0]
	for _, c = range cas {
		var got = coveredByAny(c.prefixes, c.addr)
		if got != c.expect {
			t.Errorf("coveredByAny(%v, %q) = %v, expected %v — %s",
				c.prefixes, c.addr, got, c.expect, c.why)
		}
	}
}

func containsWord(hay, needle string) bool {
	var i int
	for i = 0; i+len(needle) <= len(hay); i++ {
		if hay[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
