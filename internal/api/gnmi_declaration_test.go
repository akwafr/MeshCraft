package api

// The honesty rule for the READ side, made mechanical.
//
// The write side has had its guard for a while: a connector does not declare
// driver.Connector.WritesExtra for a command form nobody has run on hardware,
// and every connector package carries a test that fails the day it does. The
// reason is that a declared write puts a button on screen whose first exercise
// would be on a customer's switch.
//
// A declared monitoring CHANNEL is the same promise, and it is easier to break
// because nothing visibly fails when it is. A connector declaring a channel of a
// kind no collector implements produces: a metric shown as available, a device
// nothing ever subscribes to, and a graph empty for ever — no error, no log
// line, and no way for an operator to tell "this platform cannot" from "this
// build does not".
//
// gNMI is exactly that case today. internal/gnmipoll owns the OpenConfig paths
// and the translation into samples, both built and tested; its transport returns
// an explicit "not built" error, because a real gNMI client needs grpc +
// protobuf + openconfig/gnmi and that dependency decision has not been taken
// (cf. TODO.md, "gNMI telemetry"). So no connector may declare one yet.
//
// It lives in internal/api because this is where registry-wide tests already
// live (console_webui_test.go, portnamer_consistency_test.go): the package that
// blank-imports every connector, and the one place a rule can be stated once
// instead of once per connector — including for connectors that do not exist
// yet, which is the half that matters.

import (
	"testing"

	"meshcraft/internal/driver"
	"meshcraft/internal/gnmipoll"

	_ "meshcraft/internal/driver/arista/eos"
	_ "meshcraft/internal/driver/cisco/ios"
	_ "meshcraft/internal/driver/fs/s3400"
	_ "meshcraft/internal/driver/fs/s3900"
	_ "meshcraft/internal/driver/hpe/procurve"
	_ "meshcraft/internal/driver/instanton"
	_ "meshcraft/internal/driver/mikrotik/routeros"
	_ "meshcraft/internal/driver/mikrotik/swos"
	_ "meshcraft/internal/driver/ubiquiti/edgeswitch"
)

// A connector declares a gnmi channel ONLY while something subscribes.
//
// This guard has done its original job — the transport exists, and the Arista
// connector declares the channels — so it now runs in the other direction: it is
// what fails if a future build strips the gRPC stack back out. The declaration
// and the collector flip together or not at all, and the failure message says
// which way round the reader is standing.
func TestGNMIDeclarationsMatchTheTransport(t *testing.T) {
	var declaring []string
	var name string
	for _, name = range driver.Names() {
		var d, ok = driver.Get(name)
		if !ok {
			continue
		}
		var mon = d.Capabilities("").Monitoring
		var ch driver.MonitorChannel
		for _, ch = range mon.Channels {
			if ch.Kind == driver.KindGNMI {
				declaring = append(declaring, name)
				break
			}
		}
	}
	if !gnmipoll.TransportReady && len(declaring) > 0 {
		t.Errorf("connectors %v declare a %q channel while no collector subscribes: "+
			"this promises an operator a graph that stays empty for ever, with no error "+
			"and no log line. Either land the transport and flip gnmipoll.TransportReady, "+
			"or withdraw the declarations — in one commit", declaring, driver.KindGNMI)
	}
}

// NO CONNECTOR SERVES ONE METRIC OVER BOTH SNMP AND gNMI.
//
// The invariant that became dangerous the day the transport landed, and the one
// worth a registry-wide test rather than a comment on one connector.
//
// The controller decides which collector carries a device by asking whether its
// connector declares a gNMI channel for `if_counters` (`carriedOnGNMI`), and then
// zeroes the other side — the two watch lists are exclusive per device. A
// connector declaring both kinds for one metric therefore does not get collected
// twice; it gets read as streaming and has its SNMP declaration SILENTLY
// IGNORED. The operator sees a declared SNMP cadence, on a screen, that nothing
// anywhere honours.
//
// And if the exclusivity were ever relaxed, the same declaration would produce
// the worse failure it currently prevents: one series name fed by two producers
// with different label sets — SNMP carries `ifindex` and `iftype`, gNMI carries
// neither — which VictoriaMetrics stores as two adjacent half-populated series.
// Both look plausible. Every rate computed from either is wrong by whatever
// fraction of the samples went to the other.
//
// Either way the declaration is a mistake, so it is refused here rather than
// arbitrated later.
func TestNoConnectorServesOneMetricOverBothKinds(t *testing.T) {
	var name string
	for _, name = range driver.Names() {
		var d, ok = driver.Get(name)
		if !ok {
			continue
		}
		var mon = d.Capabilities("").Monitoring
		var bySNMP = map[string]bool{}
		var byGNMI = map[string]bool{}
		var ch driver.MonitorChannel
		for _, ch = range mon.Channels {
			var m string
			for _, m = range ch.Metrics {
				switch ch.Kind {
				case driver.KindSNMP:
					bySNMP[m] = true
				case driver.KindGNMI:
					byGNMI[m] = true
				}
			}
		}
		var metric string
		for metric = range byGNMI {
			if bySNMP[metric] {
				t.Errorf("connector %q declares %q over BOTH snmp and gnmi: the controller "+
					"carries a device on one collector or the other, so the snmp declaration "+
					"would be shown to an operator and honoured by nobody", name, metric)
			}
		}
	}
}

// Streaming is a SUMMARY of the channel list, not a second independent claim.
//
// Set without the channel, it tells every screen the platform streams while
// BestOfKind(KindGNMI, …) has nothing to subscribe to. Set the other way round,
// it hides a real capability from every screen that reads the summary rather
// than walking the channels. Neither half is visible at a glance, which is why
// it is a test over the whole registry and not a comment on the field.
func TestStreamingMatchesTheGNMIChannels(t *testing.T) {
	var name string
	for _, name = range driver.Names() {
		var d, ok = driver.Get(name)
		if !ok {
			continue
		}
		var mon = d.Capabilities("").Monitoring
		var declared bool
		var ch driver.MonitorChannel
		for _, ch = range mon.Channels {
			if ch.Kind == driver.KindGNMI {
				declared = true
			}
		}
		if declared != mon.Streaming {
			t.Errorf("connector %q: Streaming=%v but %d gnmi channel(s) declared — the two "+
				"are one fact and must move together", name, mon.Streaming,
				boolToInt(declared))
		}
	}
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// A channel of an UNKNOWN kind is collected by nobody, and the failure is the
// same silence: BestOfKind matches the exact string, so a typo does not fail to
// compile — it fails to match, and the device is simply never collected.
//
// The vocabulary is closed on purpose (internal/driver/gnmi.go). A connector
// that genuinely needs a new kind adds it there, with the prose saying who
// collects it, which is exactly the conversation this test forces.
func TestMonitorChannelKindsAreInTheVocabulary(t *testing.T) {
	var known = map[string]bool{
		driver.KindSNMP: true,
		driver.KindWeb:  true,
		driver.KindAPI:  true,
		driver.KindGNMI: true,
	}
	var name string
	for _, name = range driver.Names() {
		var d, ok = driver.Get(name)
		if !ok {
			continue
		}
		var ch driver.MonitorChannel
		for _, ch = range d.Capabilities("").Monitoring.Channels {
			if !known[ch.Kind] {
				t.Errorf("connector %q declares a channel of kind %q, which no collector "+
					"matches: BestOfKind compares the exact string, so this device is "+
					"silently never collected for %v", name, ch.Kind, ch.Metrics)
			}
		}
	}
}
