package instanton

import (
	"strings"
	"testing"
	"time"

	"meshcraft/internal/driver"
)

// Observability declared by the 1930 connector.
//
// Not structure paraphrase: minimum intervals declared here BOUND the controller's
// collection policy (cf. internal/api/watch_handlers.go). A channel grouping two
// metrics imposes the same floor on both — that is how interface counters ended up
// capped at 30 s by health and LLDP, although nothing in guide 5200-5879 requires it.
// The split is therefore a property to preserve, not a wording detail.

func canalDe(t *testing.T, mon driver.MonitoringCapabilities,
	metric string) driver.MonitorChannel {
	t.Helper()
	var ch, ok = mon.Best(metric)
	if !ok {
		t.Fatalf("no channel declared for %q: metric would be unsupervised", metric)
	}
	return ch
}

func TestMonitoringCanauxSNMPChacunSonPlancher(t *testing.T) {
	var mon = (&Driver1930{}).Capabilities("JL686A 48G PoE").Monitoring

	var counters = canalDe(t, mon, "if_counters")
	if counters.Kind != "snmp" || counters.MinInterval != 5*time.Second {
		t.Errorf("if_counters expected snmp at 5 s, got %+v", counters)
	}

	var m string
	for _, m = range []string{"health", "lldp", "poe"} {
		var ch = canalDe(t, mon, m)
		if ch.Kind != "snmp" || ch.MinInterval != 30*time.Second {
			t.Errorf("%s expected snmp at 30 s, got %+v", m, ch)
		}
	}

	var state = canalDe(t, mon, "if_state")
	if state.Kind != "snmp" || state.MinInterval != 5*time.Second {
		t.Errorf("if_state expected snmp at 5 s, got %+v", state)
	}

	var lourdes = map[string]bool{"health": true, "lldp": true, "poe": true}
	var c driver.MonitorChannel
	for _, c = range mon.Channels {
		var porteCompteurs, porteLourdes bool
		var metric string
		for _, metric = range c.Metrics {
			if metric == "if_counters" {
				porteCompteurs = true
			}
			if lourdes[metric] {
				porteLourdes = true
			}
		}
		if porteCompteurs && porteLourdes {
			t.Errorf("channel %+v: counters must not share their floor with health, LLDP or PoE", c)
		}
	}

	if !mon.Traps {
		t.Error("1930 sends traps: 30 s if_state cadence relies on that fact")
	}
}

// Optical power does not exist on this product line, and the connector SAYS so.
func TestPuissanceOptiqueDeclareeIndisponibleSurLaGamme(t *testing.T) {
	var model string
	for _, model = range []string{"JL685A", "JL685A 48G 4SFP+", "JL686A 48G PoE", ""} {
		var mon = (&Driver1930{}).Capabilities(model).Monitoring

		var u, ok = mon.WhyUnavailable("optical_power")
		if !ok {
			t.Fatalf("model %q: optical power must be declared unavailable, "+
				"otherwise its absence is only silence", model)
		}

		if !u.Observed {
			t.Errorf("model %q: absence is OBSERVED on hardware, not assumed", model)
		}

		// Operator copy is in the UI; Source carries the verifiable survey.
		var expected string
		for _, expected = range []string{"JL685A", "1.3.6.1.2.1.99"} {
			if !strings.Contains(u.Source, expected) {
				t.Errorf("model %q: observation source must cite %q, got: %q",
					model, expected, u.Source)
			}
		}
	}
}

func TestPuissanceOptiqueJamaisAnnonceeDisponible(t *testing.T) {
	var mon = (&Driver1930{}).Capabilities("JL685A 48G 4SFP+").Monitoring

	var ch, ok = mon.Best("optical_power")
	if ok {
		t.Errorf("a channel announces optical power (%+v): product line does not expose it, "+
			"JL685A observation is unambiguous", ch)
	}

	var contradictions = mon.Contradictions()
	if len(contradictions) != 0 {
		t.Errorf("contradictory declaration for %v: metric both in a channel and unavailable",
			contradictions)
	}
}
