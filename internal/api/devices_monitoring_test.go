package api

import (
	"encoding/json"
	"strings"
	"testing"

	"meshcraft/internal/model"
)

// What the platform does not expose for monitoring, as seen from the API.
//
// These tests lock: connector declaration reaches the device record, and ONLY
// when it exists. Both halves matter equally — a reason that never arrives lets
// the operator expect a meshcraft version for data the device will never publish;
// an empty array on every other record would read "everything else is available",
// which nobody has observed.
//
// No database, like the rest of the package: unavailableMetricsOf was written
// pure on purpose (cf. judgeCollector, effectiveInterval, judgeAskSNMP). What a
// test here does NOT cover: the full HTTP path, and deviceIdentity fallback to
// the discovery candidate, which needs PostgreSQL — this derivation deliberately
// reads only extra.driver.

// deviceForTest: a record reduced to what the derivation needs — declared
// connector and model. Nothing else enters the decision.
func deviceForTest(driverName, modelName string) model.Device {
	var d = model.Device{Model: modelName}
	if driverName != "" {
		d.Extra = json.RawMessage(`{"driver":"` + driverName + `"}`)
	}
	return d
}

// TestSheetSaysWhyOpticalPowerIsMissing: first real case and reason this
// batch exists. The 1930 line publishes no optical sensor; the reason is
// established by SNMP observation on hardware, and must reach the UI with the
// three things that make it usable — metric named as in the standard model, a
// clear reason, and the fact that it was OBSERVED.
func TestSheetSaysWhyOpticalPowerIsMissing(t *testing.T) {
	var gaps = unavailableMetricsOf(deviceForTest("aruba-instant-on-1930", "JL686A 48G PoE"))
	if len(gaps) != 1 {
		t.Fatalf("1930 record must carry connector declaration, got %d", len(gaps))
	}
	var u = gaps[0]
	if u.Metric != "optical_power" {
		t.Errorf("metric must be named as in standard model, got %q", u.Metric)
	}
	if !u.Observed {
		t.Error("this absence is observed on hardware: UI must be able to state it confidently")
	}
	// Observation, for the skeptical operator: they must be able to reproduce it
	// rather than trust us, so queried OIDs must travel with the source.
	if !strings.Contains(u.Source, "1.3.6.1") {
		t.Errorf("source must carry verifiable observation, got %q", u.Source)
	}
}

// TestAbsentDeclarationIsNotAvailability: trap of the whole batch. A
// model whose connector declares nothing must carry NOTHING — not an empty array,
// not a key. Missing key keeps the UI silent; a key with zero elements invites
// showing "no limitation", i.e. asserting availability nobody verified.
func TestAbsentDeclarationIsNotAvailability(t *testing.T) {
	// Manually entered device: no connector, therefore no knowledge.
	var gaps = unavailableMetricsOf(model.Device{Model: "SW-24G"})
	if len(gaps) != 0 {
		t.Fatalf("without connector, no declaration may be invented, got %d", len(gaps))
	}
	// Key must not appear in JSON served to the UI.
	var payload, err = json.Marshal(deviceMonitoringView{})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(payload), "monitoring_unavailable") {
		t.Errorf("record without declaration must not carry the key: %s", payload)
	}
}

// TestUnknownConnectorDoesNotBreakTheRead: record must remain readable
// regardless. extra.driver naming a removed connector, unreadable or missing
// extra: three kinds of ignorance, none is a crash — and none authorizes any
// conclusion about monitoring.
func TestUnknownConnectorDoesNotBreakTheRead(t *testing.T) {
	var cas = []struct {
		name string
		d    model.Device
	}{
		{"connector absent from registry", deviceForTest("acme-9000", "X1")},
		{"unreadable extra", model.Device{Model: "X1", Extra: json.RawMessage(`{`)}},
		{"missing extra", model.Device{Model: "X1"}},
		{"extra without driver key", model.Device{Model: "X1", Extra: json.RawMessage(`{"os":"3.4"}`)}},
		{"empty driver", model.Device{Model: "X1", Extra: json.RawMessage(`{"driver":""}`)}},
	}
	var c = cas[0]
	for _, c = range cas {
		var gaps = unavailableMetricsOf(c.d)
		if len(gaps) != 0 {
			t.Errorf("%s: no declaration expected, got %d", c.name, len(gaps))
		}
	}
}

// TestTheDeclarationTravelsWithTheRestOfTheSheet: added layer must cost NOTHING
// in the existing payload. Reason is served by the same object as management plan
// conformity and sync state — avoiding a second round trip and a UI that flashes
// blank first — and bad nesting would bury it in a sub-object the UI never reads,
// or hide mgmt in passing.
func TestTheDeclarationTravelsWithTheRestOfTheSheet(t *testing.T) {
	var views = []deviceView{
		{Device: deviceForTest("aruba-instant-on-1930", "JL686A 48G PoE")},
		{Device: model.Device{ID: "manual", Model: "SW-24G"}},
	}
	var out = withMonitoringGaps(views)
	if len(out) != 2 {
		t.Fatalf("one record per device, got %d", len(out))
	}
	if len(out[0].MonitoringUnavailable) != 1 {
		t.Errorf("1930 must carry its declaration, got %d", len(out[0].MonitoringUnavailable))
	}
	if out[1].MonitoringUnavailable != nil {
		t.Errorf("device without connector must carry nothing, got %v", out[1].MonitoringUnavailable)
	}

	var payload, err = json.Marshal(out[0])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var text = string(payload)
	// Device and conformity fields stay FLAT: UI reads dev.mgmt, dev.sync and
	// dev.monitoring_unavailable at the same level.
	var want = []string{
		`"mgmt":`, `"model":"JL686A 48G PoE"`,
		`"monitoring_unavailable":`, `"metric":"optical_power"`,
		// Two fields display depends on: tone (observed / assumed) and consultable observation.
		`"observed":true`, `"source":`,
	}
	var token string
	for _, token = range want {
		if !strings.Contains(text, token) {
			t.Errorf("serialized record must carry %s: %s", token, text)
		}
	}
}
