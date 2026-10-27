package api

import (
	"encoding/json"
	"testing"
	"time"

	"meshcraft/internal/driver"
	"meshcraft/internal/metricbuf"
	"meshcraft/internal/model"
)

// The two PURE judgements of the optics sampler: who is worth an admin
// session, and what an observation becomes as samples. Both decide against a
// fleet-wide budget — an eligibility mistake burns sessions on every tick, a
// rendering mistake writes a credible false level.

func opticsDev(t *testing.T, observed *driver.OpticsState) model.Device {
	t.Helper()
	var d = dev(idA, "ok", nil)
	d.MgmtIP = "10.0.0.1"
	if observed != nil {
		var raw, err = json.Marshal(map[string]any{"optics_observed": observed})
		if err != nil {
			t.Fatal(err)
		}
		d.Extra = raw
	}
	return d
}

func fptr(v float64) *float64 { return &v }

func TestOpticsSamplerEligibilityComesFromTheObservation(t *testing.T) {
	var withModule = &driver.OpticsState{Ports: []driver.OpticsPortState{
		{Port: "GE49", DDM: true, RxPowerDBm: fptr(-6.02)},
	}}
	if !opticsSampleWanted(opticsDev(t, withModule)) {
		t.Error("a device with an observation must be sampled")
	}
	if opticsSampleWanted(opticsDev(t, nil)) {
		t.Error("no observation = the connector never proved the read: " +
			"an admin session here is budget burnt on a guess")
	}
	var off = opticsDev(t, withModule)
	off.Status = "off"
	if opticsSampleWanted(off) {
		t.Error("an unreachable device is a guaranteed timeout per tick")
	}
	var noIP = opticsDev(t, withModule)
	noIP.MgmtIP = ""
	if opticsSampleWanted(noIP) {
		t.Error("no management address, no session to open")
	}
}

func TestOpticsSamplesKeepAbsentAndZeroApart(t *testing.T) {
	var now = time.Now()
	var st = driver.OpticsState{Ports: []driver.OpticsPortState{
		{
			Port: "GE49", DDM: true,
			// 0.0 dBm is a LEGAL reading (exactly one milliwatt) and must be
			// published; the nil temperature must produce nothing.
			RxPowerDBm: fptr(0),
			TxPowerDBm: fptr(-2.5),
		},
		{
			// A module that declares no diagnostics: present, all readings nil,
			// zero samples — not five zeros.
			Port: "GE50", DDM: false,
		},
		{
			// A port name the facade does not carry: dropped, never invented.
			Port: "XG9", DDM: true, RxPowerDBm: fptr(-3),
		},
	}}
	var got = opticsSamples(opticsDev(t, &st), st, now)
	if len(got) != 2 {
		t.Fatalf("%d samples, want 2 (rx + tx on GE49): %+v", len(got), got)
	}
	var byName = map[string]metricbuf.Sample{}
	var smp metricbuf.Sample
	for _, smp = range got {
		byName[smp.Name] = smp
		if smp.Labels["port"] != "GE49" {
			t.Errorf("sample on %q, want GE49 only: %+v", smp.Labels["port"], smp)
		}
		if smp.Labels["lane"] != "" {
			t.Errorf("CLI readings are one per cage: no lane label — %+v", smp)
		}
	}
	if byName[metricbuf.MetricSFPRxPowerDBM].Value != 0 {
		t.Errorf("0.0 dBm is a measurement and must survive: %+v",
			byName[metricbuf.MetricSFPRxPowerDBM])
	}
	if byName[metricbuf.MetricSFPTxPowerDBM].Value != -2.5 {
		t.Errorf("tx power lost: %+v", byName[metricbuf.MetricSFPTxPowerDBM])
	}
}
