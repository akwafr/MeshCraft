package driver

import (
	"testing"
	"time"
)

// TestBestChoisitLeCanalLePlusRapide: planning means asking for the rate the
// device can actually sustain — not the one we'd like. Best returns the fastest
// channel that can return the metric.
func TestBestChoisitLeCanalLePlusRapide(t *testing.T) {
	var caps = MonitoringCapabilities{Channels: []MonitorChannel{
		{Kind: "web", Metrics: []string{"if_state"}, MinInterval: 30 * time.Second},
		{Kind: "snmp", Metrics: []string{"if_state"}, MinInterval: 5 * time.Second},
		{Kind: "snmp", Metrics: []string{"if_counters"}, MinInterval: 30 * time.Second},
	}}

	var c, ok = caps.Best("if_state")
	if !ok || c.Kind != "snmp" || c.MinInterval != 5*time.Second {
		t.Errorf("if_state must go through SNMP at 5 s, got %+v (ok=%v)", c, ok)
	}
	if c, ok = caps.Best("if_counters"); !ok || c.MinInterval != 30*time.Second {
		t.Errorf("if_counters expected at 30 s, got %+v", c)
	}
	// A metric no channel returns is not monitored, and that must be said: a
	// fleet that is half-monitored without anyone noticing is the worst possible
	// outcome.
	if _, ok = caps.Best("streaming_only_metric"); ok {
		t.Error("a metric with no channel must be flagged as absent, not invented")
	}
}

// TestBestSansCanal: a platform with no declared observability must not promise
// anything. The unknown capability is assumed ABSENT.
func TestBestSansCanal(t *testing.T) {
	var _, ok = (MonitoringCapabilities{}).Best("if_state")
	if ok {
		t.Error("no declared capability must never amount to any monitoring")
	}
}

// TestWhyUnavailableSepareLIgnoranceDuFaitEtabli: the two possible answers from
// WhyUnavailable do not say the same thing. A declared reason is a fact about the
// device; the absence of a reason is NOT availability, it simply means nobody
// documented it. Conflating the two would recreate the exact silence this field
// exists to break.
func TestWhyUnavailableSepareLIgnoranceDuFaitEtabli(t *testing.T) {
	var caps = MonitoringCapabilities{
		Channels: []MonitorChannel{
			{Kind: "snmp", Metrics: []string{"if_state"}, MinInterval: 5 * time.Second},
		},
		Unavailable: []MetricUnavailable{
			{Metric: "optical_power", Observed: true, Source: "observed"},
			{Metric: "temperature"},
		},
	}

	var u, ok = caps.WhyUnavailable("optical_power")
	if !ok || !u.Observed || u.Source == "" {
		t.Errorf("the observed reason must come back unchanged: %+v (ok=%v)", u, ok)
	}
	// Observed / assumed must be DISTINGUISHABLE: without that, the UI shows a
	// measured fact and a deduction in the same tone, and makes an operator give
	// up over a mere assumption.
	if u, ok = caps.WhyUnavailable("temperature"); !ok || u.Observed {
		t.Errorf("an assumed absence must not present itself as observed: %+v", u)
	}
	// A metric whose absence nobody documented: no reason, and above all no
	// promise of availability — that's for Best to decide.
	if _, ok = caps.WhyUnavailable("health"); ok {
		t.Error("no reason is declared for health: making one up would be worse than nothing")
	}
	if _, ok = caps.Best("health"); ok {
		t.Error("health has no channel: the metric is not monitored")
	}
}

// TestContradictionsAttrapeLaDeclarationQuiSeMord: declaring a metric both
// returned by a channel and unavailable on the platform is the only defect this
// struct can still allow. It has to be visible, because it produces either a
// promised graph nothing will ever fill, or a stated reason for absence next to
// a graph that is alive and well.
func TestContradictionsAttrapeLaDeclarationQuiSeMord(t *testing.T) {
	var sain = MonitoringCapabilities{
		Channels: []MonitorChannel{
			{Kind: "snmp", Metrics: []string{"if_state"}, MinInterval: 5 * time.Second},
		},
		Unavailable: []MetricUnavailable{{Metric: "optical_power"}},
	}
	var consistent = sain.Contradictions()
	if len(consistent) != 0 {
		t.Errorf("expected a consistent declaration with no contradiction, got %v", consistent)
	}

	var mordue = MonitoringCapabilities{
		Channels: []MonitorChannel{
			{Kind: "snmp", Metrics: []string{"if_state", "optical_power"},
				MinInterval: 5 * time.Second},
		},
		Unavailable: []MetricUnavailable{{Metric: "optical_power"}},
	}
	var got = mordue.Contradictions()
	if len(got) != 1 || got[0] != "optical_power" {
		t.Errorf("the contradiction must be named, got %v", got)
	}
}

// TestBestOfKindNeLaissePasUnCanalDeciderPourUnAutre
//
// # The bug this method fixes, and it was not hypothetical
//
// `Best` returns the fastest channel ACROSS ALL KINDS. That is the right answer to
// "how fast can this platform be observed" — the question a UI asks — but not to
// "how fast am I allowed to poll this specific channel," which is the question
// PLANNING asks.
//
// The difference was costly, and it was reproducible: the product's periodic
// collector runs on the proxy and speaks ONLY SNMP. On a platform declaring a
// fast web channel and a slow SNMP channel — the RouterOS connector's case,
// whose REST API measures 111 ms and whose SNMP channel had never served a
// single OID — `Best` returned the REST floor, and the controller applied it to
// an SNMP walk. One channel's floor was pitted against a DIFFERENT channel, which
// is exactly what a floor is not supposed to be.
func TestBestOfKindNeLaissePasUnCanalDeciderPourUnAutre(t *testing.T) {
	var caps = MonitoringCapabilities{Channels: []MonitorChannel{
		{Kind: "web", Metrics: []string{"if_state"}, MinInterval: 5 * time.Second},
		{Kind: "snmp", Metrics: []string{"if_state"}, MinInterval: 60 * time.Second},
		{Kind: "api", Metrics: []string{"if_state"}, MinInterval: 30 * time.Second},
	}}

	// The UI's question: the fastest, across all kinds.
	var global, ok = caps.Best("if_state")
	if !ok || global.Kind != "web" || global.MinInterval != 5*time.Second {
		t.Errorf("Best must return the fastest channel across all kinds: %+v",
			global)
	}

	// Planning's question: the floor OF the channel that will actually be used.
	var cas = []struct {
		genre    string
		plancher time.Duration
	}{
		{"web", 5 * time.Second},
		{"api", 30 * time.Second},
		{"snmp", 60 * time.Second},
	}
	var c int
	for c = range cas {
		var ch, trouve = caps.BestOfKind(cas[c].genre, "if_state")
		if !trouve {
			t.Errorf("no %q channel for if_state", cas[c].genre)
			continue
		}
		if ch.MinInterval != cas[c].plancher {
			t.Errorf("floor for channel %q: %v expected %v — a channel of another kind "+
				"must not be able to decide in its place",
				cas[c].genre, ch.MinInterval, cas[c].plancher)
		}
	}
}

// TestBestOfKindRetiendLePlusRapideDuGENRE
//
// Two channels of the same kind for the same metric do occur: the RouterOS
// connector has two under `api` — one for state and counters, another for
// optics — and nothing prevents a platform from declaring two for the same
// metric at different rates. In that case it's the FASTEST of the kind that sets
// the bound, just like for `Best`.
func TestBestOfKindRetiendLePlusRapideDuGENRE(t *testing.T) {
	var caps = MonitoringCapabilities{Channels: []MonitorChannel{
		{Kind: "snmp", Metrics: []string{"health"}, MinInterval: 120 * time.Second},
		{Kind: "snmp", Metrics: []string{"health"}, MinInterval: 60 * time.Second},
	}}
	var ch, ok = caps.BestOfKind("snmp", "health")
	if !ok || ch.MinInterval != 60*time.Second {
		t.Errorf("expected the fastest of the kind (60 s), got %+v", ch)
	}
}

// TestBestOfKindNeReplieurPasToutSeul
//
// The fallback is deliberately left to the CALLER. A platform that declares no
// channel of the requested kind must be handled EXPLICITLY — either by falling
// back to the floor for the unknown, or by not collecting at all — and hiding
// that choice behind a silent fallback would poll a platform over SNMP that
// never advertised the capability.
func TestBestOfKindNeReplieurPasToutSeul(t *testing.T) {
	var caps = MonitoringCapabilities{Channels: []MonitorChannel{
		{Kind: "web", Metrics: []string{"if_state"}, MinInterval: 10 * time.Second},
	}}
	var ch, ok = caps.BestOfKind("snmp", "if_state")
	if ok {
		t.Errorf("no SNMP channel is declared: BestOfKind must NOT fall back "+
			"to the web channel, got %+v", ch)
	}
	// And a metric unknown to the requested kind must not fall back to another
	// metric of the same channel either.
	var _, ok2 = caps.BestOfKind("web", "optical_power")
	if ok2 {
		t.Error("a metric not carried by the channel must not be found")
	}
}
