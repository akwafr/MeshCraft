package snmppoll

import (
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"
	"meshcraft/internal/metricbuf"
)

// Values verbatim from the walk of a WS-C2960X-24TD-L on 2026-08-02. The point
// of the test is the SCALES: every one was read off the device, and a regression
// that reintroduced a borrowed divisor would show here.
func ciscoTables() map[string]map[string]gosnmp.SnmpPDU {
	var g = func(v uint) gosnmp.SnmpPDU {
		return gosnmp.SnmpPDU{Type: gosnmp.Gauge32, Value: v}
	}
	var str = func(v string) gosnmp.SnmpPDU {
		return gosnmp.SnmpPDU{Type: gosnmp.OctetString, Value: []byte(v)}
	}
	return map[string]map[string]gosnmp.SnmpPDU{
		oidCiscoCPU5min:   {"1": g(23)},
		oidCiscoMemName:   {"1": str("Processor"), "2": str("I/O"), "20": str("Driver text")},
		oidCiscoMemUsed:   {"1": g(69157616), "2": g(12713764), "20": g(40)},
		oidCiscoMemFree:   {"1": g(296576052), "2": g(20840668), "20": g(1048536)},
		oidCiscoTempDescr: {"1008": str("SW#1, Sensor#1, GREEN")},
		oidCiscoTempValue: {"1008": g(43)},
	}
}

func find(samples []metricbuf.Sample, name string) (metricbuf.Sample, bool) {
	var s metricbuf.Sample
	for _, s = range samples {
		if s.Name == name {
			return s, true
		}
	}
	return metricbuf.Sample{}, false
}

func TestCiscoHealthUsesDeviceScales(t *testing.T) {
	var out = ciscoHealthSamples("dev", ciscoTables(), time.Now(), false, false)

	var cpu, okc = find(out, metricbuf.MetricCPULoad)
	if !okc || cpu.Value != 23 {
		t.Errorf("cpu = %v (%v), want 23 percent unscaled", cpu.Value, okc)
	}
	var temp, okt = find(out, metricbuf.MetricTemperature)
	if !okt || temp.Value != 43 {
		t.Errorf("temp = %v (%v), want 43 Celsius unscaled", temp.Value, okt)
	}
	if temp.Labels["sensor"] != "SW#1, Sensor#1, GREEN" {
		t.Errorf("temp sensor label = %q", temp.Labels["sensor"])
	}
	// Memory: the PROCESSOR pool, not I/O or Driver text.
	var free, okf = find(out, metricbuf.MetricMemoryFree)
	if !okf || free.Value != 296576052 {
		t.Errorf("free = %v, want the Processor pool free bytes", free.Value)
	}
	var tot, _ = find(out, metricbuf.MetricMemoryTotal)
	if tot.Value != 69157616+296576052 {
		t.Errorf("total = %v, want used+free", tot.Value)
	}
}

// If HOST-RESOURCES already covered cpu/memory, the Cisco branch stays quiet — no
// metric published twice with the same labels and timestamp.
func TestCiscoHealthYieldsToHostResources(t *testing.T) {
	var out = ciscoHealthSamples("dev", ciscoTables(), time.Now(), true, true)
	if _, ok := find(out, metricbuf.MetricCPULoad); ok {
		t.Error("cpu published although HOST-RESOURCES already did")
	}
	if _, ok := find(out, metricbuf.MetricMemoryFree); ok {
		t.Error("memory published although HOST-RESOURCES already did")
	}
	// Temperature has no HOST-RESOURCES equivalent — it must still come through.
	if _, ok := find(out, metricbuf.MetricTemperature); !ok {
		t.Error("temperature was suppressed; it has no standard-MIB equivalent")
	}
}

// A MikroTik (empty Cisco tables) must produce nothing here.
func TestCiscoHealthEmptyOnOtherPlatforms(t *testing.T) {
	var out = ciscoHealthSamples("dev", map[string]map[string]gosnmp.SnmpPDU{}, time.Now(), false, false)
	if len(out) != 0 {
		t.Errorf("produced %d samples on a device serving no enterprise-9 tree", len(out))
	}
}
