package api

// The PoE sampler's PURE decisions, exercised without a database, a session,
// or a device: which devices earn an admin session (poeSampleWanted), what
// lands in `poe_w` (poeWattsPatch), and what enters the series store
// (poeSamples). These are the decisions that would silently burn session
// budget or paint a credible false wattage if they went wrong.

import (
	"encoding/json"
	"testing"
	"time"

	"meshcraft/internal/driver"
	"meshcraft/internal/metricbuf"
	"meshcraft/internal/model"
)

// poeDevice builds a device whose facade carries GE5/GE6 (from existing
// ports, the facadeOf fallback) and whose last observation is `obs`.
func poeDevice(t *testing.T, ports string, obs *driver.PoEState) model.Device {
	t.Helper()
	var d = model.Device{
		ID:     "11111111-2222-3333-4444-555555555555",
		MgmtIP: "192.0.2.4",
		Status: "ok",
		Ports:  json.RawMessage(ports),
	}
	if obs != nil {
		var raw, err = json.Marshal(obs)
		if err != nil {
			t.Fatal(err)
		}
		var extra, mErr = json.Marshal(map[string]json.RawMessage{"poe_observed": raw})
		if mErr != nil {
			t.Fatal(mErr)
		}
		d.Extra = extra
	}
	return d
}

func capablePorts() *driver.PoEState {
	return &driver.PoEState{Ports: []driver.PoEPortState{
		{Port: "GE5", Capable: true, Class: driver.PoEClass54V},
		{Port: "GE6", Capable: false},
	}}
}

func TestPoESampleWanted(t *testing.T) {
	var ports = `{"GE5":{},"GE6":{}}`
	var cases = []struct {
		name string
		mut  func(*model.Device)
		want bool
	}{
		{"capable port observed", func(d *model.Device) {}, true},
		{"no observation at all", func(d *model.Device) { d.Extra = nil }, false},
		{"observation without capable port", func(d *model.Device) {
			var raw, _ = json.Marshal(&driver.PoEState{Ports: []driver.PoEPortState{
				{Port: "GE5", Capable: false}}, Notes: []string{"no PSE"}})
			var extra, _ = json.Marshal(map[string]json.RawMessage{"poe_observed": raw})
			d.Extra = extra
		}, false},
		{"device reported off", func(d *model.Device) { d.Status = "off" }, false},
		{"status unknown still tries", func(d *model.Device) { d.Status = "unknown" }, true},
		{"no management address", func(d *model.Device) { d.MgmtIP = "" }, false},
	}
	var c struct {
		name string
		mut  func(*model.Device)
		want bool
	}
	for _, c = range cases {
		var d = poeDevice(t, ports, capablePorts())
		c.mut(&d)
		if poeSampleWanted(d) != c.want {
			t.Errorf("%s: poeSampleWanted = %v, want %v", c.name, !c.want, c.want)
		}
	}
}

func TestPoEWattsPatchWritesDrawAndZeroes(t *testing.T) {
	// GE5 starts drawing, GE6 (no PSE hardware) carries a stale figure that
	// must be zeroed, GE7 is unknown to the facade and must be dropped.
	var d = poeDevice(t, `{"GE5":{"mode":"access","pvid":10},"GE6":{"poe_w":3.5}}`, nil)
	var st = driver.PoEState{Ports: []driver.PoEPortState{
		{Port: "GE5", Capable: true, Delivering: true, PowerW: 6.44},
		{Port: "GE6", Capable: false},
		{Port: "GE7", Capable: true, Delivering: true, PowerW: 9},
	}}
	var patch = poeWattsPatch(d, st)
	if len(patch) != 2 {
		t.Fatalf("patch = %v, want GE5 and GE6 only", patch)
	}
	if patch["GE5"]["poe_w"] != 6.4 {
		t.Errorf("GE5 poe_w = %v, want 6.4 (rounded to 0.1 W)", patch["GE5"]["poe_w"])
	}
	if patch["GE6"]["poe_w"] != 0.0 {
		t.Errorf("GE6 poe_w = %v, want 0 (stale figure zeroed)", patch["GE6"]["poe_w"])
	}
	var _, invented = patch["GE7"]
	if invented {
		t.Error("GE7 is not on the facade and must not be invented")
	}
	// The patch must carry ONLY the measured key: intent survives via
	// UpdatePortTelemetry's jsonb merge, but nothing here may even name it.
	var k string
	for k = range patch["GE5"] {
		if k != "poe_w" {
			t.Errorf("patch carries %q, want poe_w only", k)
		}
	}
}

func TestPoEWattsPatchSkipsUnchanged(t *testing.T) {
	var d = poeDevice(t, `{"GE5":{"poe_w":6.4},"GE6":{}}`, nil)
	var st = driver.PoEState{Ports: []driver.PoEPortState{
		// 6.44 rounds to the stored 6.4: no write.
		{Port: "GE5", Capable: true, Delivering: true, PowerW: 6.44},
		// Not delivering and nothing stored: zero equals absent-as-zero, no write.
		{Port: "GE6", Capable: true, Delivering: false},
	}}
	var patch = poeWattsPatch(d, st)
	if len(patch) != 0 {
		t.Fatalf("patch = %v, want empty (nothing changed)", patch)
	}
}

func TestPoESamplesPerPortAndTotal(t *testing.T) {
	var d = poeDevice(t, `{"GE5":{},"GE6":{}}`, nil)
	var now = time.Now()
	var st = driver.PoEState{Ports: []driver.PoEPortState{
		{Port: "GE5", Capable: true, Delivering: true, PowerW: 6.4},
		// Capable but idle: publishes ZERO, does not go silent.
		{Port: "GE6", Capable: true, Delivering: false},
	}}
	var samples = poeSamples(d, st, now)
	if len(samples) != 3 {
		t.Fatalf("samples = %d, want 2 ports + 1 total", len(samples))
	}
	var byPort = map[string]float64{}
	var used = -1.0
	var sm metricbuf.Sample
	for _, sm = range samples {
		if sm.Labels["device"] != d.ID {
			t.Errorf("sample %s lacks the device label", sm.Name)
		}
		if !sm.TS.Equal(now) {
			t.Errorf("sample %s carries TS %v, want the pass instant", sm.Name, sm.TS)
		}
		switch sm.Name {
		case metricbuf.MetricPoEPowerW:
			byPort[sm.Labels["port"]] = sm.Value
		case metricbuf.MetricPoEUsedW:
			used = sm.Value
		default:
			t.Errorf("unexpected metric %q", sm.Name)
		}
	}
	if byPort["GE5"] != 6.4 || byPort["GE6"] != 0 {
		t.Errorf("per-port watts = %v, want GE5=6.4 GE6=0", byPort)
	}
	// The platform served no UsedW: the total is the SUM of port draws.
	if used != 6.4 {
		t.Errorf("used = %v, want 6.4 (sum fallback)", used)
	}
}

func TestPoESamplesPrefersPlatformTotal(t *testing.T) {
	var d = poeDevice(t, `{"GE5":{}}`, nil)
	var st = driver.PoEState{
		UsedW: 12.5,
		Ports: []driver.PoEPortState{
			{Port: "GE5", Capable: true, Delivering: true, PowerW: 6.4}},
	}
	var samples = poeSamples(d, st, time.Now())
	var sm metricbuf.Sample
	for _, sm = range samples {
		if sm.Name == metricbuf.MetricPoEUsedW && sm.Value != 12.5 {
			t.Errorf("used = %v, want the platform's own 12.5", sm.Value)
		}
	}
}

func TestPoESamplesNoCapablePortNoSeries(t *testing.T) {
	var d = poeDevice(t, `{"GE5":{}}`, nil)
	var st = driver.PoEState{Ports: []driver.PoEPortState{
		{Port: "GE5", Capable: false}}}
	var got = poeSamples(d, st, time.Now())
	if got != nil {
		t.Fatalf("samples = %v, want nil: a PoE-less device must not promise a flat zero line", got)
	}
}
