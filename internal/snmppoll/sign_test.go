package snmppoll

import (
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"

	"meshcraft/internal/metricbuf"
)

// SENSOR SIGN, proven on a VALUE READ FROM HARDWARE.
//
// These tests lock down the fix for a bug that published nine-digit values in
// temperature and optical power series. The governing value is `4294967168`
// (0xFFFFFF80, **-128** in 32-bit two's complement), read on hardware from an
// empty SFP cage. Over SNMP it arrives in an unsigned Gauge32 per RFC 2578.

func TestSigned32(t *testing.T) {
	var cases = []struct {
		name string
		raw  float64
		want float64
	}{
		{"empty SFP cage, READ from hardware", 4294967168, -128},
		{"zero", 0, 0},
		{"plausible temperature", 690, 690},
		{"just below threshold: untouched", 2147483647, 2147483647},
		{"threshold itself: negative", 2147483648, -2147483648},
		{"-1", 4294967295, -1},
		{"optical power -7.2 dBm in thousandths", 4294960096, -7200},
	}
	var c = cases[0]
	for _, c = range cases {
		var got = signed32(c.raw)
		if got != c.want {
			t.Errorf("%s: signed32(%v) = %v, want %v", c.name, c.raw, got, c.want)
		}
	}
}

func TestEmptyCageOpticalTemperature(t *testing.T) {
	var tables = mergeTables(
		table(oidOpticalName, map[string]gosnmp.SnmpPDU{
			"25": {Name: oidOpticalName + ".25", Type: gosnmp.OctetString, Value: []byte("SFP1")},
			"26": {Name: oidOpticalName + ".26", Type: gosnmp.OctetString, Value: []byte("SFP2")},
		}),
		table(oidOpticalTemperature, map[string]gosnmp.SnmpPDU{
			"25": {Name: oidOpticalTemperature + ".25", Type: gosnmp.Gauge32, Value: uint(4294967168)},
			"26": {Name: oidOpticalTemperature + ".26", Type: gosnmp.Gauge32, Value: uint(50)},
		}),
	)

	var got = map[string]float64{}
	var s metricbuf.Sample
	for _, s = range OpticalSamples("dev-1", tables, time.Unix(0, 0), 26) {
		if s.Name == metricbuf.MetricSFPTemperature {
			got[s.Labels["ifindex"]] = s.Value
		}
	}
	if got["25"] != -128 {
		t.Errorf("empty cage: temperature = %v, want -128", got["25"])
	}
	if got["26"] != 50 {
		t.Errorf("occupied cage: temperature = %v, want 50", got["26"])
	}
}

func TestNegativeOpticalPowers(t *testing.T) {
	var tables = mergeTables(
		table(oidOpticalName, map[string]gosnmp.SnmpPDU{
			"26": {Name: oidOpticalName + ".26", Type: gosnmp.OctetString, Value: []byte("SFP2")},
		}),
		table(oidOpticalRxPower, map[string]gosnmp.SnmpPDU{
			"26": {Name: oidOpticalRxPower + ".26", Type: gosnmp.Gauge32, Value: uint(4294960096)},
		}),
		table(oidOpticalTxPower, map[string]gosnmp.SnmpPDU{
			"26": {Name: oidOpticalTxPower + ".26", Type: gosnmp.Gauge32, Value: uint(4294964196)},
		}),
	)

	var got = map[string]float64{}
	var s metricbuf.Sample
	for _, s = range OpticalSamples("dev-1", tables, time.Unix(0, 0), 26) {
		got[s.Name] = s.Value
	}
	var close = func(a, b float64) bool { return a-b < 0.0001 && b-a < 0.0001 }
	if !close(got[metricbuf.MetricSFPRxPowerDBM], -7.2) {
		t.Errorf("received power = %v dBm, want -7.2", got[metricbuf.MetricSFPRxPowerDBM])
	}
	if !close(got[metricbuf.MetricSFPTxPowerDBM], -3.1) {
		t.Errorf("transmitted power = %v dBm, want -3.1", got[metricbuf.MetricSFPTxPowerDBM])
	}
}

func TestHealthTemperatureReading(t *testing.T) {
	var tables = table("1.3.6.1.4.1.14988.1.1.3.11", map[string]gosnmp.SnmpPDU{
		"0": gaugePDU("1.3.6.1.4.1.14988.1.1.3.11", 690),
	})
	var found bool
	var s metricbuf.Sample
	for _, s = range HealthSamples("dev-1", tables, time.Unix(0, 0)) {
		if s.Name != metricbuf.MetricTemperature || s.Labels["sensor"] != "processor" {
			continue
		}
		found = true
		if s.Value < 68.9 || s.Value > 69.1 {
			t.Errorf("temperature = %v °C; want 69 (690 × 0.1)", s.Value)
		}
	}
	if !found {
		t.Error("no temperature sample from mtxrHealth.11 processor sensor")
	}
}
