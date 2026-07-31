package api

import (
	"testing"
	"time"

	"meshcraft/internal/driver"
	"meshcraft/internal/driver/instanton"
	"meshcraft/internal/model"
	"meshcraft/internal/snmppoll"
)

// deviceForCadence: the device only fills the clamp journal. Cadence does not
// depend on it, and that is verifiable here.
func deviceForCadence() model.Device {
	return model.Device{ID: "6f1c9a2e", Name: "sw-access-1", Model: "JL686A"}
}

// Cadence policy, exercised without database, proxy, or clock.
//
// This is the arbitration rule for the whole batch H: policy CHOOSES a rate,
// the connector floor BOUNDS it from below. It lives in a pure function, which
// is precisely why it is tested here rather than by graph observation.

func TestEffectiveIntervalPolitiqueBorneeParLePlancher(t *testing.T) {
	type cas struct {
		nom           string
		wanted, floor time.Duration
		kept          time.Duration
		raised        bool
	}
	var cases = []cas{{
		// The case that justifies the second return: operator asked for 5 s, the
		// platform only supports 30. We give 30, and they must learn it.
		nom:    "floor slower than wanted cadence: clamp",
		wanted: 5 * time.Second, floor: 30 * time.Second,
		kept: 30 * time.Second, raised: true,
	}, {
		// The defect this batch fixes. Before, a 5 s floor BECAME the cadence:
		// "never more often than 5 s" was read as "every 5 s".
		nom:    "floor faster: wanted cadence is kept",
		wanted: 30 * time.Second, floor: 5 * time.Second,
		kept: 30 * time.Second,
	}, {
		nom:    "floor equals wanted cadence: nothing to clamp",
		wanted: 5 * time.Second, floor: 5 * time.Second,
		kept: 5 * time.Second,
	}, {
		// Unknown connector: ignore what the platform can handle. The safe default
		// is SLOW, not absent — and it is announced as a clamp, otherwise an entire
		// fleet would poll every minute with nobody knowing why.
		nom:    "unknown floor: fall back to safe default, and say so",
		wanted: 5 * time.Second, floor: 0,
		kept: unknownFloorInterval, raised: true,
	}, {
		nom:    "negative floor counts as unknown",
		wanted: 5 * time.Second, floor: -time.Second,
		kept: unknownFloorInterval, raised: true,
	}, {
		// The safe default is a FLOOR, not a target: a policy already slower than
		// it is not sped up to match.
		nom:    "unknown floor but wanted cadence already slower",
		wanted: 5 * time.Minute, floor: 0,
		kept: 5 * time.Minute,
	}}

	var k cas
	for _, k = range cases {
		t.Run(k.nom, func(t *testing.T) {
			var kept, raised = effectiveInterval(k.wanted, k.floor)
			if kept != k.kept {
				t.Errorf("kept cadence %v expected, got %v", k.kept, kept)
			}
			if raised != k.raised {
				t.Errorf("raised=%v expected, got %v", k.raised, raised)
			}
		})
	}
}

// TestPolitiqueDeCadenceSurUn1930 crosses policy and connector, because that
// crossing is what ships — each alone can be correct while the result is not.
//
// This is also the test that breaks if someone merges the 1930 channels again:
// counters would fall back to 30 s and the operator would lose fine grain with
// no policy line changing.
func TestPolitiqueDeCadenceSurUn1930(t *testing.T) {
	var name = (&instanton.Driver1930{}).Info().Name
	var drv, known = driver.Get(name)
	if !known {
		t.Fatalf("connector %q not registered", name)
	}
	var mon = drv.Capabilities("JL686A 48G PoE").Monitoring

	// `if_state`: connector supports 5 s, policy wants only 30. Policy wins —
	// the floor does not pull upward.
	var floor, ok = mon.Best(snmppoll.MetricIfState)
	if !ok {
		t.Fatal("1930 must declare a channel for if_state")
	}
	var kept, raised = effectiveInterval(wantedStateInterval, floor.MinInterval)
	if kept != 30*time.Second || raised {
		t.Errorf("if_state expected at 30 s without clamp, got %v (raised=%v)",
			kept, raised)
	}

	// `if_counters`: the split channel gives 5 s, so policy passes through.
	// With the old grouped channel at 30 s, this case would be a clamp.
	if floor, ok = mon.Best(snmppoll.MetricIfCounters); !ok {
		t.Fatal("1930 must declare a channel for if_counters")
	}
	if kept, raised = effectiveInterval(wantedCountersInterval,
		floor.MinInterval); kept != 5*time.Second || raised {
		t.Errorf("if_counters expected at 5 s without clamp, got %v (raised=%v)",
			kept, raised)
	}
}

// TestIntervalForConnecteurInconnu: the path snmpWatchesFor actually takes when
// `driver.Get` does not know the device. Verify it separately from the pure
// function, because that is where we decide whether to consult a floor or fall
// back to the safe default.
func TestIntervalForConnecteurInconnu(t *testing.T) {
	var got = intervalFor(deviceForCadence(), driver.MonitoringCapabilities{}, false,
		snmppoll.MetricIfCounters, wantedCountersInterval)
	if got != unknownFloorInterval {
		t.Errorf("unknown connector: %v expected, got %v", unknownFloorInterval, got)
	}

	// KNOWN connector but silent on this metric: same safe default. A connector
	// that does not declare `if_counters` promises nothing about it, and assuming
	// it can handle 5 s is exactly the bet we refuse to make.
	var mon = driver.MonitoringCapabilities{Channels: []driver.MonitorChannel{
		{Kind: "snmp", Metrics: []string{snmppoll.MetricIfState},
			MinInterval: time.Second},
	}}
	if got = intervalFor(deviceForCadence(), mon, true,
		snmppoll.MetricIfCounters, wantedCountersInterval); got != unknownFloorInterval {
		t.Errorf("undeclared metric: %v expected, got %v",
			unknownFloorInterval, got)
	}
	// On the metric it does declare, a very low floor changes nothing: policy
	// remains master of cadence.
	if got = intervalFor(deviceForCadence(), mon, true,
		snmppoll.MetricIfState, wantedStateInterval); got != wantedStateInterval {
		t.Errorf("if_state: %v expected, got %v", wantedStateInterval, got)
	}
}
