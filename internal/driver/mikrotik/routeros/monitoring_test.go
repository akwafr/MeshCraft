package routeros

import (
	"strings"
	"testing"
	"time"

	"meshcraft/internal/driver"
)

// Observability declared by the RouterOS connector.
//
// This is not structure paraphrase: the floors declared here BOUND the controller's
// collection policy. A channel that groups two metrics imposes the same floor on
// them — that is how, on the Instant On connector, interface counters ended up
// capped at 30 s by health and LLDP, when nothing required it. The split is a
// property to hold, not a drafting detail.

func channelOf(t *testing.T, mon driver.MonitoringCapabilities,
	metric string) driver.MonitorChannel {
	t.Helper()
	var ch, ok = mon.Best(metric)
	if !ok {
		t.Fatalf("no channel declared for %q: the metric would be unsupervised", metric)
	}
	return ch
}

// TestMonitoringDeclarationWithoutContradiction — the contract invariant, locked.
//
// A self-contradictory declaration is worse than no declaration: either the screen
// announces a curve that no collection will ever fill, or it shows an absence reason
// next to a curve that exists. In both cases the operator loses the only thing these
// capabilities give them — certainty that what is displayed matches the device.
//
// The test sweeps several models, including the EMPTY model: the project rule
// requires assuming the worst when one does not know, and that branch must not
// contradict itself either. It also includes a PoE model, because this connector's
// only `Unavailable` entry is CONDITIONAL on the model — precisely the kind of
// construction that introduces a contradiction on a branch one had not looked at.
func TestMonitoringDeclarationWithoutContradiction(t *testing.T) {
	var model string
	for _, model = range []string{
		referenceModel,
		"CRS354-48G-4S+2Q+",
		"CRS328-24P-4S+", // PoE variant: the Unavailable entry disappears
		"CRS305-1G-4S+",
		"",
	} {
		t.Run("model "+model, func(t *testing.T) {
			var mon = (&Driver{}).Capabilities(model).Monitoring
			var contradictions = mon.Contradictions()
			if len(contradictions) != 0 {
				t.Errorf("contradictory declaration for %v: a metric is both served "+
					"by a channel and declared unavailable", contradictions)
			}
		})
	}
}

// TestEachChannelItsFloor: the split must be REAL.
//
// The figures come from a measurement campaign on the reference hardware, not from
// a copy (cf. the monitoringFor comment):
//
//	at rest                              → cpu-load 1 %
//	chained /rest/interface reads        → cpu-load 25 %, stable
//	/rest/interface median               → 111 ms for 29 interfaces
//
// Hence 5 s for state and counters — less than 1 % of the switch CPU — and the
// refusal to go much lower.
func TestEachChannelItsFloor(t *testing.T) {
	var mon = (&Driver{}).Capabilities(referenceModel).Monitoring

	// State and counters: REST at 5 s, and that is what `Best` must return.
	var m string
	for _, m = range []string{"if_state", "if_counters"} {
		var ch = channelOf(t, mon, m)
		if ch.Kind != "web" || ch.MinInterval != 5*time.Second {
			t.Errorf("%s expected on web (REST) at 5 s, got %+v", m, ch)
		}
	}

	// LLDP at 30 s, and this floor is imposed by the DEVICE: its own discovery
	// runs at 30 s (observed `/ip/neighbor/discovery-settings`, discover-interval).
	// Polling faster rereads a table the device has not touched.
	var lldp = channelOf(t, mon, "lldp")
	if lldp.MinInterval != 30*time.Second {
		t.Errorf("lldp expected at 30 s — the device's own discovery cadence —, "+
			"got %v", lldp.MinInterval)
	}

	// Health at 60 s: on this board, `/system/health` publishes only the CPU
	// temperature (observed), a thermal mass that does not move in a minute.
	var health = channelOf(t, mon, "health")
	if health.MinInterval != 60*time.Second {
		t.Errorf("health expected at 60 s, got %v", health.MinInterval)
	}

	// The split must be REAL: no channel must put counters in the same bag as
	// health or network tables. Without this assertion, a 'cleanliness'
	// regrouping would redo the defect without failing the checks above — it would
	// suffice to pick 5 s for the whole group, and health would be read at 5 s the
	// day it is read.
	var heavy = map[string]bool{"health": true, "lldp": true, "poe": true}
	var c driver.MonitorChannel
	for _, c = range mon.Channels {
		var carriesCounters, carriesHeavy bool
		var metric string
		for _, metric = range c.Metrics {
			if metric == "if_counters" {
				carriesCounters = true
			}
			if heavy[metric] {
				carriesHeavy = true
			}
		}
		if carriesCounters && carriesHeavy {
			t.Errorf("channel %+v: counters must not share their floor with health, "+
				"LLDP or PoE", c)
		}
	}
}

// TestSNMPChannelSaysNoOIDWasRead — the test that protects honesty.
//
// # This test changed SHAPE without changing OBJECT, and why must be said
//
// It was called `TestLeCanalSNMPResteUnRepli` and checked two things: that the SNMP
// channel note contain the word 'fallback', and that `Best` return the REST channel
// for state as for counters. The intention was right — a never-exercised channel must
// not become a fleet's primary path 'by the sole effect of a smaller number' — but
// the MEANS rested on a misunderstanding, and the misunderstanding produced a defect:
//
// the product's periodic collection runs on the proxy and speaks ONLY SNMP
// (`internal/snmppoll`). The REST channel therefore won no race, because there was no
// race: it was NEVER taken. What it did, by being the fastest, was LEND its 5 s floor
// to an SNMP poll whose own channel announced 'never more often than 60 s'.
//
// The means is replaced by the fitting mechanism (`MonitoringCapabilities.BestOfKind`,
// which queries the channel by KIND) and this test keeps the object: verify that the
// SNMP channel note continues to say what it knows and what it does not. It is that
// sentence that prevents believing, in six months, that these floors were measured.
func TestSNMPChannelSaysNoOIDWasRead(t *testing.T) {
	var mon = (&Driver{}).Capabilities(referenceModel).Monitoring

	var snmpFound bool
	var c driver.MonitorChannel
	for _, c = range mon.Channels {
		if c.Kind != "snmp" {
			continue
		}
		snmpFound = true
		// 'no oid': the phrase that says this channel is declared from an
		// OBSERVED CONFIGURATION and not from a collection. Removing it would turn an
		// admission into silence, and the floors below would pass for measurements.
		//
		// 'measured' is the third receivable admission, and it arrived AFTER the other
		// two. The `lldp` channel was walked OID by OID on the reference device
		// (observed 2026-07-29, cf. docs/en/01-Architecture/LLDP neighbours.md): forcing
		// it to say 'no OID was read' would have been demanding a LIE to satisfy a
		// test. What this test protects is not the formula, it is that a note say
		// EXPLICITLY where its floor comes from — measurement, observed configuration,
		// or deduction. Silence remains the only refused case, and it still is.
		var note = strings.ToLower(c.Note)
		// `admits`: does this channel acknowledge that its floor is NOT a measurement
		// of itself? Two formulas say so, and a channel may carry both.
		var admits = strings.Contains(note, "no oid") || strings.Contains(note, "derived")
		// `claims`: this channel says it was measured. The word only counts if the
		// channel is not already apologizing elsewhere — the `if_state` channel note
		// contains 'measured' talking about the 1930, i.e. ANOTHER device, and it
		// admits elsewhere that no OID was read. The distinction avoids demanding an
		// observation date it by definition does not have.
		var claims = !admits && strings.Contains(note, "measured")
		if !admits && !claims {
			t.Errorf("an SNMP channel note must say where its floor comes from — that no "+
				"OID was read, that its scale factors are derived, or that it is "+
				"MEASURED. Without that phrase, a declared floor passes for a measurement. "+
				"Got: %q", c.Note)
		}
		// And the measurement admission must be DATED. 'measured' without a date is an
		// unverifiable claim — exactly what this file exists to prevent — whereas a date
		// points to an observation one can go reread in the vault.
		if claims && !strings.Contains(note, "2026") {
			t.Errorf("a channel that claims MEASURED must carry the date of its "+
				"observation: without it, the claim is not verifiable. Got: %q", c.Note)
		}
	}
	if !snmpFound {
		t.Fatal("no SNMP channel declared even though the observed configuration allows it")
	}

	// THE STRUCTURAL POINT, the one the old test could not express: a channel of
	// ANOTHER kind must not be able to decide the cadence of an SNMP poll. Planning
	// goes through `BestOfKind("snmp", …)`, so there must exist an SNMP channel for
	// every metric the poller can read — otherwise the controller falls back to the
	// unknown floor and nobody notices.
	var m string
	for _, m = range []string{"if_state", "if_counters", "health", "optical_power"} {
		var ch, ok = mon.BestOfKind("snmp", m)
		if !ok {
			t.Errorf("no SNMP channel for %q: the proxy poller can read it, but the "+
				"controller will fall back to the unknown floor", m)
			continue
		}
		if ch.MinInterval <= 0 {
			t.Errorf("%s: null SNMP floor (%+v) — a floor that bounds nothing is not "+
				"a floor", m, ch)
		}
	}

	// And the counterpart: the fastest channel ALL KINDS COMBINED remains REST for
	// state and counters. That is what `Best` must continue to say, because a screen
	// that answers 'how fast can this device be observed?' asks exactly that
	// question.
	for _, m = range []string{"if_state", "if_counters"} {
		var best = channelOf(t, mon, m)
		if best.Kind != "web" {
			t.Errorf("%s: the fastest channel on this platform is REST "+
				"(111 ms measured for the 29 interfaces), got %q", m, best.Kind)
		}
	}
}

// TestPoEDeclaredUnavailableOnObservedModel
//
// The absence is an OBSERVATION, and it took two requests to be able to assert it:
// `/rest/interface/ethernet/poe` returns 500, but a 500 proves nothing on this
// API — `/rest/interface/dot1x` also returns 500 while
// `/rest/interface/dot1x/server` returns `[]`. It is the 400 'no such command
// prefix' from `poe/settings` that concludes, the same response as an invented path.
//
// What this test protects is not the structure, it is the DIFFERENCE between 'we do
// not collect it yet' and 'the device does not expose it'. The first makes one
// wait for a version, the second makes one change hardware. Erasing this
// declaration would leave an operator waiting in front of a curve that will never
// arrive.
func TestPoEDeclaredUnavailableOnObservedModel(t *testing.T) {
	var mon = (&Driver{}).Capabilities(referenceModel).Monitoring

	var u, ok = mon.WhyUnavailable("poe")
	if !ok {
		t.Fatal("PoE must be declared unavailable on a CRS326-24G-2S+, " +
			"otherwise its absence is only silence")
	}
	// Observed on hardware: that is what authorizes the screen to assert it without
	// reservation. Flipping this boolean to false would turn an observation into a
	// guess.
	if !u.Observed {
		t.Error("PoE absence is OBSERVED on hardware, not assumed")
	}
	// Operator copy is in the UI; Source carries both requests that establish the fact.
	var expected string
	for _, expected = range []string{
		"CRS326-24G-2S+", "7.21beta3", "no such command prefix", "dot1x",
	} {
		if !strings.Contains(u.Source, expected) {
			t.Errorf("the observation source must cite %q, got: %q", expected, u.Source)
		}
	}
}

// TestPoENotDeclaredUnavailableOnAPoEModel: the symmetric branch.
//
// PoE absence is a fact of MODEL, not of product line — unlike optical power on the
// Instant On connector, which holds for its whole line. A CRS328-24P-4S+ has
// PoE-out, and declaring the metric unavailable for it would make an operator
// renounce a function their device has.
func TestPoENotDeclaredUnavailableOnAPoEModel(t *testing.T) {
	var caps = (&Driver{}).Capabilities("CRS328-24P-4S+")

	if !caps.Has("PoE") {
		t.Error("a CRS328-24P-4S+ carries PoE-out: the `24P` group of MikroTik " +
			"nomenclature designates it")
	}
	var _, ok = caps.Monitoring.WhyUnavailable("poe")
	if ok {
		t.Error("PoE must NOT be declared unavailable on a PoE model: " +
			"that would make an operator renounce a function they paid for")
	}
}

// TestTrapsDoNotCarryLinksToday
//
// RouterOS CAN push traps — the contract declares it, and `/snmp` does carry
// `trap-target`, `trap-version` and `trap-generators`. But the observed value of
// `trap-generators` is `temp-exception` ONLY: link state changes are not part of
// it, and `trap-target` is empty.
//
// Consequence for planning, and why this test lives here: unlike the Instant On
// connector, whose `if_state` cadence at 30 s RESTS on its linkUp/linkDown traps,
// this one can only count on its polling. That is what justifies the 5 s floor of
// the REST channel — lowering it a notch believing traps will catch up would be a
// mistake.
func TestTrapsDoNotCarryLinksToday(t *testing.T) {
	var mon = (&Driver{}).Capabilities(referenceModel).Monitoring
	if !mon.Traps {
		t.Error("RouterOS can push traps (observed: /snmp carries trap-target, " +
			"trap-version and trap-generators)")
	}
	if !mon.Syslog {
		t.Error("RouterOS can push syslog (observed: /system/logging/action " +
			"carries a `remote` action on 514/udp)")
	}
	if mon.Streaming {
		t.Error("RouterOS has no pushed telemetry (gNMI or equivalent)")
	}

	// And the REAL state of the reference device, read from the capture: traps
	// exist but do not carry links. That fact bounds the cadence.
	var snmp = fixtureOne(t, "snmp.json")
	var state = SNMPState{
		TrapTarget:     snmp.Get("trap-target"),
		TrapGenerators: SplitList(snmp.Get("trap-generators")),
	}
	if state.PushesLinkTraps() {
		t.Errorf("the capture shows trap-generators=%q and trap-target=%q: no "+
			"link trap leaves this device. If that changes, the justification of the "+
			"if_state floor changes with it",
			snmp.Get("trap-generators"), snmp.Get("trap-target"))
	}
}

// TestOpticalPowerNowServedByTwoChannels — the third position has been left.
//
// # What this test said before, and why it changed
//
// It was called `TestPuissanceOptiqueNiServieNiDeclareeAbsente` and locked the
// THIRD position of the contract: neither channel ('we do not declare what we have
// not exercised'), nor `Unavailable` entry ('we do not declare impossible what is
// probably available'). The cause was named: `monitor` is a command, therefore out
// of reach of a read tool restricted to GET.
//
// That cause has been LIFTED, and that is the whole object of the telemetry batch:
// the binary API (api.go) executes commands, and the `mtxrOpticalTable` of
// MIKROTIK-MIB gives the same thing via SNMP. The metric therefore has two channels,
// and it must have two — a single one would have left the proxy's periodic collection
// with nothing to read, since that collection speaks only SNMP.
//
// What this test protects now: that both channels exist, that their floors respect
// the PHENOMENON (a module's I²C bus is the slowest on the board) and that the metric
// is still NOT declared unavailable — declaring it absent would make an operator
// change hardware who does not need to.
func TestOpticalPowerNowServedByTwoChannels(t *testing.T) {
	var mon = (&Driver{}).Capabilities(referenceModel).Monitoring

	var kind string
	for _, kind = range []string{"api", "snmp"} {
		var ch, ok = mon.BestOfKind(kind, "optical_power")
		if !ok {
			t.Errorf("no %q channel for optical power: the `api` channel reads it "+
				"via /interface/ethernet/monitor and the `snmp` channel via mtxrOpticalTable — "+
				"both are needed, the proxy's periodic collection speaking only SNMP", kind)
			continue
		}
		// 30 s at least, and the figure comes from the phenomenon not the protocol: a
		// transceiver's diagnostic registers are read on the module's I²C bus.
		// Going under that value would solicit the slowest bus on the board for a
		// quantity that drifts over days.
		if ch.MinInterval < 30*time.Second {
			t.Errorf("%q channel: optical floor of %v, whereas the module registers "+
				"are read on its I²C bus and an optical budget drifts over days",
				kind, ch.MinInterval)
		}
	}

	var _, declaredAbsent = mon.WhyUnavailable("optical_power")
	if declaredAbsent {
		t.Error("optical power must NOT be declared unavailable: it now has two " +
			"channels, and a metric both served and declared absent is the contradiction " +
			"that Contradictions() refuses")
	}
}
