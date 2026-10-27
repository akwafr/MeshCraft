package snmppoll

import (
	"math"
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"

	"meshcraft/internal/metricbuf"
)

// The two SENSOR sets, exercised without hardware.
//
// # What these tests can prove, and what they cannot
//
// They prove TRANSLATION: the right column in the right series, the right scale, the
// right label set, and rejections. That is where chart correctness is decided, and it
// is verifiable without a network.
//
// They do NOT prove that MikroTik devices serve these OIDs, or that `mtxrHealth` scale
// factors are correct: no OID was read on the two reference devices (the warning at the
// top of health.go explains why, and what would lift it). A test that fabricates its
// own PDUs cannot settle that — it verifies the code does what we decided, not that the
// decision is right.
//
// The distinction is worth writing here because it decides trust: a temperature that
// passes these tests can still be wrong by a factor of ten.

// gaugePDU builds a gauge PDU, the type most of these sensors serve.
func gaugePDU(oid string, v uint) gosnmp.SnmpPDU {
	return gosnmp.SnmpPDU{Name: oid + ".0", Type: gosnmp.Gauge32, Value: v}
}

// table indexes a PDU as the poller does: by the LAST segment of the OID.
func table(oid string, byIndex map[string]gosnmp.SnmpPDU) map[string]map[string]gosnmp.SnmpPDU {
	return map[string]map[string]gosnmp.SnmpPDU{oid: byIndex}
}

// mergeTables assembles several tables into one read set.
func mergeTables(parts ...map[string]map[string]gosnmp.SnmpPDU) map[string]map[string]gosnmp.SnmpPDU {
	var out = map[string]map[string]gosnmp.SnmpPDU{}
	var p int
	for p = range parts {
		var oid string
		var t map[string]gosnmp.SnmpPDU
		for oid, t = range parts[p] {
			out[oid] = t
		}
	}
	return out
}

// valuesByKey buckets samples by (series, sensor) for readable assertions.
func valuesByKey(samples []metricbuf.Sample) map[string]float64 {
	var out = map[string]float64{}
	var s metricbuf.Sample
	for _, s = range samples {
		var key = s.Name
		if s.Labels["sensor"] != "" {
			key += "/" + s.Labels["sensor"]
		}
		if s.Labels["port"] != "" {
			key += "/" + s.Labels["port"]
		}
		out[key] = s.Value
	}
	return out
}

// TestUptimeFromCentisecondsToSeconds
//
// `sysUpTime` is in CENTISECONDS. Publishing it raw would show uptime a hundred times
// too large, which has the drawback of being plausible on a recently booted device —
// five months where there were two days.
func TestUptimeFromCentisecondsToSeconds(t *testing.T) {
	var tables = table(oidSysUpTime, map[string]gosnmp.SnmpPDU{
		"0": {Name: oidSysUpTime + ".0", Type: gosnmp.TimeTicks, Value: uint(14995265)},
	})
	var got = valuesByKey(HealthSamples("dev-1", tables, time.Now()))
	if math.Abs(got[metricbuf.MetricUptime]-149952.65) > 0.01 {
		t.Errorf("uptime: got %v s, expected 149952.65 s",
			got[metricbuf.MetricUptime])
	}
}

// TestCPULoadIsAverageOfCores
//
// `hrProcessorLoad` carries one entry PER CORE, and the CRS326 reports `cpu-count: "2"`.
// Publishing two indexed series would yield two curves no screen knows how to summarize;
// the AVERAGE gives device load, which is the question we ask.
//
// Average, not maximum: one core at 100% on a dual-core is not a saturated device, it
// is a half-busy device. This test locks that in, because it is exactly the kind of
// aggregate a review "fixes" while believing it helps.
func TestCPULoadIsAverageOfCores(t *testing.T) {
	var tables = table(oidHrProcessorLoad, map[string]gosnmp.SnmpPDU{
		"1": {Name: oidHrProcessorLoad + ".1", Type: gosnmp.Integer, Value: 100},
		"2": {Name: oidHrProcessorLoad + ".2", Type: gosnmp.Integer, Value: 0},
	})
	var got = valuesByKey(HealthSamples("dev-1", tables, time.Now()))
	if got[metricbuf.MetricCPULoad] != 50 {
		t.Errorf("CPU load: got %v, expected 50%% (average of 100 and 0). If it is "+
			"100, the maximum was kept, and a half-busy dual-core would look saturated",
			got[metricbuf.MetricCPULoad])
	}
}

// TestMemoryPicksTheCorrectHrStorageRow
//
// `hrStorage` describes ALL storage: RAM, but also flash and file systems. Publishing
// the first row would pass the CRS326 flash 16 MiB (`total-hdd-space: "16777216"`) off
// as memory on a device with 512 MiB (`total-memory: "536870912"`).
//
// And sizes are in BLOCKS: forgetting `hrStorageAllocationUnits` would make memory a
// thousand times too small, which is also plausible — 512 KiB looks like an embedded
// device.
func TestMemoryPicksTheCorrectHrStorageRow(t *testing.T) {
	var tables = mergeTables(
		table(oidHrStorageDescr, map[string]gosnmp.SnmpPDU{
			// Flash FIRST, deliberately: that is the trap.
			"1": {Name: oidHrStorageDescr + ".1", Type: gosnmp.OctetString,
				Value: []byte("system disk")},
			"65536": {Name: oidHrStorageDescr + ".65536", Type: gosnmp.OctetString,
				Value: []byte("main memory")},
		}),
		table(oidHrStorageUnits, map[string]gosnmp.SnmpPDU{
			"1":     gaugePDU(oidHrStorageUnits, 1024),
			"65536": gaugePDU(oidHrStorageUnits, 1024),
		}),
		table(oidHrStorageSize, map[string]gosnmp.SnmpPDU{
			"1":     gaugePDU(oidHrStorageSize, 16384),  // 16 MiB flash
			"65536": gaugePDU(oidHrStorageSize, 524288), // 512 MiB RAM
		}),
		table(oidHrStorageUsed, map[string]gosnmp.SnmpPDU{
			"1":     gaugePDU(oidHrStorageUsed, 4096),
			"65536": gaugePDU(oidHrStorageUsed, 84288),
		}),
	)
	var got = valuesByKey(HealthSamples("dev-1", tables, time.Now()))
	if got[metricbuf.MetricMemoryTotal] != 524288*1024 {
		t.Errorf("total memory: got %v, expected %v — if it is 16777216, FLASH was "+
			"published", got[metricbuf.MetricMemoryTotal], 524288*1024)
	}
	// FREE, not used: that is the quantity that decides whether a device will hold, and
	// it matches RouterOS `free-memory` — same quantity as the REST channel publishes.
	if got[metricbuf.MetricMemoryFree] != (524288-84288)*1024 {
		t.Errorf("free memory: got %v, expected %v",
			got[metricbuf.MetricMemoryFree], (524288-84288)*1024)
	}
}

// TestUnknownMemoryProducesNoSeries
//
// An unrecognized label produces NO sample: better no memory curve than a curve
// describing flash.
func TestUnknownMemoryProducesNoSeries(t *testing.T) {
	var tables = mergeTables(
		table(oidHrStorageDescr, map[string]gosnmp.SnmpPDU{
			"1": {Name: oidHrStorageDescr + ".1", Type: gosnmp.OctetString,
				Value: []byte("Virtual memory")},
		}),
		table(oidHrStorageSize, map[string]gosnmp.SnmpPDU{
			"1": gaugePDU(oidHrStorageSize, 1000),
		}),
	)
	var got = valuesByKey(HealthSamples("dev-1", tables, time.Now()))
	var _, published = got[metricbuf.MetricMemoryTotal]
	if published {
		t.Errorf("unrecognized storage row produced a memory series: %v",
			got[metricbuf.MetricMemoryTotal])
	}
}

// TestMtxrHealthSensorsAndScales
//
// SCALE is what this test locks, and also what nobody could verify on hardware — the
// warning at the top of health.go says so. The divisor of 10 comes from two reference
// collectors (Observium, LibreNMS), not from a live read.
//
// What the test still provides: certainty that the divisor is applied UNIFORMLY. A
// forgotten scale on one sensor would produce a series ten times larger than its
// neighbors in the same chart, which is harder to notice than a factor of ten on all.
func TestMtxrHealthSensorsAndScales(t *testing.T) {
	var tables = mergeTables(
		// 720 tenths of a degree = 72 °C, the value `/system/health` returns in REST on
		// the reference device. Cross-checking both channels is exactly what will settle
		// the divisor on hardware.
		table("1.3.6.1.4.1.14988.1.1.3.6", map[string]gosnmp.SnmpPDU{
			"0": gaugePDU("1.3.6.1.4.1.14988.1.1.3.6", 720),
		}),
		table("1.3.6.1.4.1.14988.1.1.3.7", map[string]gosnmp.SnmpPDU{
			"0": gaugePDU("1.3.6.1.4.1.14988.1.1.3.7", 455),
		}),
		table("1.3.6.1.4.1.14988.1.1.3.8", map[string]gosnmp.SnmpPDU{
			"0": gaugePDU("1.3.6.1.4.1.14988.1.1.3.8", 241),
		}),
		table("1.3.6.1.4.1.14988.1.1.3.17", map[string]gosnmp.SnmpPDU{
			"0": gaugePDU("1.3.6.1.4.1.14988.1.1.3.17", 4200),
		}),
	)
	var got = valuesByKey(HealthSamples("dev-1", tables, time.Now()))

	if got[metricbuf.MetricTemperature+"/cpu"] != 72 {
		t.Errorf("CPU temperature: got %v, expected 72 °C (720 tenths)",
			got[metricbuf.MetricTemperature+"/cpu"])
	}
	if math.Abs(got[metricbuf.MetricTemperature+"/board"]-45.5) > 0.001 {
		t.Errorf("board temperature: got %v, expected 45.5 °C",
			got[metricbuf.MetricTemperature+"/board"])
	}
	if math.Abs(got[metricbuf.MetricVoltage+"/input"]-24.1) > 0.001 {
		t.Errorf("input voltage: got %v, expected 24.1 V",
			got[metricbuf.MetricVoltage+"/input"])
	}
	// A fan is a raw `Gauge32`: no divisor. Applying the temperature divisor would
	// show 420 RPM for a 4200 RPM fan.
	if got[metricbuf.MetricFanRPM+"/fan1"] != 4200 {
		t.Errorf("fan speed: got %v, expected 4200 RPM — fans do NOT have a scale factor",
			got[metricbuf.MetricFanRPM+"/fan1"])
	}
}

// TestPSUStateNeverInverts
//
// This is the only place in this file where getting it wrong INVERTS an alarm.
// MIKROTIK-MIB `BoolValue` enumerates `true(1)` / `false(2)`: on `raw != 0`, value 2
// ("PSU fault") would be read as "true", therefore "healthy", and the series would
// show a healthy device while its PSU is dead.
//
// And an out-of-enumeration value is rendered "not healthy": on an alarm, unknown is
// treated as fault, never as clearance. That is the "unknown capability is assumed
// absent" rule applied to monitoring.
func TestPSUStateNeverInverts(t *testing.T) {
	var cases = []struct {
		raw    uint
		want   float64
		reason string
	}{
		{1, 1, "enumeration true(1): healthy PSU"},
		{2, 0, "false(2): PSU FAULT. A `raw != 0` check would read it as healthy"},
		{0, 0, "out of enumeration: unknown treated as fault"},
		{42, 0, "out of enumeration"},
	}
	var c int
	for c = range cases {
		var tables = table("1.3.6.1.4.1.14988.1.1.3.15", map[string]gosnmp.SnmpPDU{
			"0": gaugePDU("1.3.6.1.4.1.14988.1.1.3.15", cases[c].raw),
		})
		var got = valuesByKey(HealthSamples("dev-1", tables, time.Now()))
		if got[metricbuf.MetricPSUOK+"/main"] != cases[c].want {
			t.Errorf("PSU state raw %d (%s): got %v, expected %v",
				cases[c].raw, cases[c].reason,
				got[metricbuf.MetricPSUOK+"/main"], cases[c].want)
		}
	}
}

// TestBranchWithMultipleValuesIsRejected
//
// `mtxrHealth` sensors are SCALARS: their branch should carry a single value. Finding
// two means we are not reading what we think, and picking one at random would publish a
// value nobody could attribute to a sensor.
//
// This test also indirectly locks why the poller walks these branches ONE BY ONE: the
// poller indexes by last OID segment, and all scalars on the same branch end in `.0`.
// Walking the whole branch would overwrite them.
func TestBranchWithMultipleValuesIsRejected(t *testing.T) {
	var tables = table("1.3.6.1.4.1.14988.1.1.3.6", map[string]gosnmp.SnmpPDU{
		"0": gaugePDU("1.3.6.1.4.1.14988.1.1.3.6", 720),
		"1": gaugePDU("1.3.6.1.4.1.14988.1.1.3.6", 999),
	})
	var got = valuesByKey(HealthSamples("dev-1", tables, time.Now()))
	var _, published = got[metricbuf.MetricTemperature+"/cpu"]
	if published {
		t.Errorf("scalar branch with two values produced %v: read is not what we think",
			got[metricbuf.MetricTemperature+"/cpu"])
	}
}

// TestHealthCarriesNoPortLabel
//
// These are DEVICE measurements. Routing them through `Samples`, which indexes by port,
// would produce temperature series labeled with a random port — a credible and false chart,
// exactly what the poller strict indexing rule exists to avoid.
func TestHealthCarriesNoPortLabel(t *testing.T) {
	var tables = mergeTables(
		table(oidSysUpTime, map[string]gosnmp.SnmpPDU{
			"0": {Name: oidSysUpTime + ".0", Type: gosnmp.TimeTicks, Value: uint(100)},
		}),
		table("1.3.6.1.4.1.14988.1.1.3.6", map[string]gosnmp.SnmpPDU{
			"0": gaugePDU("1.3.6.1.4.1.14988.1.1.3.6", 720),
		}),
	)
	var samples = HealthSamples("dev-1", tables, time.Now())
	if len(samples) == 0 {
		t.Fatal("no samples produced")
	}
	var s int
	for s = range samples {
		if samples[s].Labels["device"] != "dev-1" {
			t.Fatalf("sample without device: %+v", samples[s])
		}
		var _, hasPort = samples[s].Labels["port"]
		var _, hasIfindex = samples[s].Labels["ifindex"]
		if hasPort || hasIfindex {
			t.Errorf("health sample labeled with a port: %+v — device measurement, not a "+
				"port", samples[s])
		}
	}
}

// TestOpticalAndPublishedScales
//
// Unlike `mtxrHealth`, scale factors for this table are PUBLISHED by the MIB: `GDiv100`
// on wavelength, `GDiv1000` on voltage, `IDiv1000` on power, and nothing on temperature
// or current. They therefore do not share the same certainty as the health divisor of 10.
//
// THE TRAP NOT TO MISS: transceiver temperature does not share the same scale as BOARD
// temperature in the same MIB. Applying the `mtxrHealth` divisor here would show 5 °C
// for a 50 °C module.
func TestOpticalAndPublishedScales(t *testing.T) {
	var tables = mergeTables(
		table(oidOpticalName, map[string]gosnmp.SnmpPDU{
			"26": {Name: oidOpticalName + ".26", Type: gosnmp.OctetString,
				Value: []byte("sfp-sfpplus2")},
		}),
		table(oidOpticalWavelength, map[string]gosnmp.SnmpPDU{
			"26": gaugePDU(oidOpticalWavelength, 127000),
		}),
		table(oidOpticalTemperature, map[string]gosnmp.SnmpPDU{
			"26": gaugePDU(oidOpticalTemperature, 50),
		}),
		table(oidOpticalVoltage, map[string]gosnmp.SnmpPDU{
			"26": gaugePDU(oidOpticalVoltage, 3261),
		}),
		table(oidOpticalTxBias, map[string]gosnmp.SnmpPDU{
			"26": gaugePDU(oidOpticalTxBias, 34),
		}),
		// Power values are SIGNED integers in thousandths of dBm. Optical power is almost
		// always NEGATIVE: reading them unsigned would yield values on the order of four
		// billion.
		table(oidOpticalTxPower, map[string]gosnmp.SnmpPDU{
			"26": {Name: oidOpticalTxPower + ".26", Type: gosnmp.Integer, Value: -3197},
		}),
		table(oidOpticalRxPower, map[string]gosnmp.SnmpPDU{
			"26": {Name: oidOpticalRxPower + ".26", Type: gosnmp.Integer, Value: -5575},
		}),
		table(oidOpticalRxLoss, map[string]gosnmp.SnmpPDU{
			"26": {Name: oidOpticalRxLoss + ".26", Type: gosnmp.Integer, Value: 0},
		}),
	)
	var samples = OpticalSamples("dev-1", tables, time.Now(), 26)
	var got = valuesByKey(samples)

	var cases = []struct {
		key  string
		want float64
	}{
		{metricbuf.MetricSFPWavelengthNM + "/sfp-sfpplus2", 1270},
		{metricbuf.MetricSFPTemperature + "/sfp-sfpplus2", 50},
		{metricbuf.MetricSFPSupplyVolts + "/sfp-sfpplus2", 3.261},
		{metricbuf.MetricSFPTxBiasMilliamps + "/sfp-sfpplus2", 34},
		{metricbuf.MetricSFPTxPowerDBM + "/sfp-sfpplus2", -3.197},
		{metricbuf.MetricSFPRxPowerDBM + "/sfp-sfpplus2", -5.575},
	}
	var c int
	for c = range cases {
		if math.Abs(got[cases[c].key]-cases[c].want) > 0.0001 {
			t.Errorf("%s: got %v, expected %v", cases[c].key, got[cases[c].key], cases[c].want)
		}
	}

	// Port attachment: INDEX comes from the table, NAME from `mtxrOpticalName`. Without
	// the name, an operator reads "received power of index 26" and does not know which
	// port we mean.
	var s int
	for s = range samples {
		if samples[s].Labels["ifindex"] != "26" {
			t.Errorf("wrong interface index: %+v", samples[s])
		}
		if samples[s].Labels["port"] != "sfp-sfpplus2" {
			t.Errorf("missing or wrong port name: %+v", samples[s])
		}
	}
}

// TestEmptyCageProducesNothing
//
// An absent module is recognized by missing NAME in the table. Publishing zeros would
// show two dead transceivers on every switch with empty cages — a permanent false alert
// on most of the fleet.
func TestEmptyCageProducesNothing(t *testing.T) {
	var tables = table(oidOpticalRxPower, map[string]gosnmp.SnmpPDU{
		"25": {Name: oidOpticalRxPower + ".25", Type: gosnmp.Integer, Value: 0},
	})
	var samples = OpticalSamples("dev-1", tables, time.Now(), 26)
	if len(samples) != 0 {
		t.Errorf("cage without name produced %d sample(s): %+v",
			len(samples), samples)
	}
}

// TestFaceplateBoundRejectsAberrantIndex
//
// The index bound is not there to drop logical interfaces — the optical table has none;
// the device filters by construction. It is the only protection against a device returning
// aberrant indexes: without it, a proxy could inflate controller series cardinality.
func TestFaceplateBoundRejectsAberrantIndex(t *testing.T) {
	var tables = mergeTables(
		table(oidOpticalName, map[string]gosnmp.SnmpPDU{
			"26": {Name: oidOpticalName + ".26", Type: gosnmp.OctetString,
				Value: []byte("sfp-sfpplus2")},
			"999999": {Name: oidOpticalName + ".999999", Type: gosnmp.OctetString,
				Value: []byte("ghost")},
		}),
		table(oidOpticalRxPower, map[string]gosnmp.SnmpPDU{
			"26":     {Name: oidOpticalRxPower + ".26", Type: gosnmp.Integer, Value: -5575},
			"999999": {Name: oidOpticalRxPower + ".999999", Type: gosnmp.Integer, Value: -1000},
		}),
	)
	var samples = OpticalSamples("dev-1", tables, time.Now(), 26)
	if len(samples) != 1 {
		t.Fatalf("%d sample(s): index 999999 must be dropped by faceplate bound",
			len(samples))
	}
	if samples[0].Labels["ifindex"] != "26" {
		t.Errorf("wrong index kept: %+v", samples[0])
	}

	// Without a bound, take everything: faceplate is not always known, and denying
	// monitoring to a device whose port groups were not read would be the wrong side
	// of the trade-off.
	if len(OpticalSamples("dev-1", tables, time.Now(), 0)) != 2 {
		t.Error("without faceplate bound, all indexes must be kept")
	}
}

// TestSensorSetsHaveDistinctOIDs
//
// A set that walked another set's OIDs would produce empty series with no error: the
// device would answer "no such object", the poller would have no table, and the message
// would be "no readable SNMP table" -- sending you to diagnose agent failure where the
// poller asked for the wrong OIDs.
func TestSensorSetsHaveDistinctOIDs(t *testing.T) {
	var health = oidsFor(MetricHealth)
	var optical = oidsFor(MetricOptical)
	var state = oidsFor(MetricIfState)

	if len(health) < 10 {
		t.Errorf("health set has only %d branch(es): MIKROTIK-MIB sensors are scalars "+
			"walked one by one", len(health))
	}
	// Cost is documented in health.go and cmd/proxy/snmp.go. 22 = 16 MikroTik/
	// HOST-RESOURCES branches + 6 Cisco enterprise-9 (cpu, mem name/used/free,
	// temp descr/value). If this number changes, both documents lie.
	if len(health) != 22 {
		t.Errorf("%d branch(es) in health set, 22 documented in health.go and "+
			"cmd/proxy/snmp.go: one of them is wrong", len(health))
	}
	// 15 = the MikroTik family (name + eight measurements) plus the SIX branches
	// the Cisco family needs: four sensor columns and the two ENTITY-MIB columns
	// its readings are JOINED to (see the Cisco DOM block in health.go). The cost
	// note in cmd/proxy/snmp.go carries the same arithmetic; if this number moves,
	// that note lies.
	if len(optical) != 15 {
		t.Errorf("%d optical column(s), 15 expected (nine MikroTik + six Cisco)",
			len(optical))
	}

	// No overlap with state set: the three sets are disjoint, otherwise the same column
	// would be paid twice per cycle.
	var seen = map[string]bool{}
	var o string
	for _, o = range state {
		seen[o] = true
	}
	for _, o = range append(append([]string{}, health...), optical...) {
		if seen[o] {
			t.Errorf("OID %s is in two sets: it would be walked twice per cycle", o)
		}
	}
}

// THE CISCO DOM JOIN, against the values CiscoLabFirst1 actually returned
// (2026-08-12, WS-C2960X-24TD-L). Every number below is from that walk.
//
// What this guards is the pair of mistakes that produce a CREDIBLE FALSE GRAPH
// rather than an error: transmit and receive swapped, and a scale or precision
// ignored. Neither shows up as a failure anywhere — a -3.9 dBm reading rendered
// as -39, or TX drawn as RX, looks exactly like a measurement.
func TestCiscoDOMJoinsSensorsToTheirPort(t *testing.T) {
	var tables = map[string]map[string]gosnmp.SnmpPDU{
		oidEntPhysicalName: {
			"1039": {Value: []byte("TenGigabitEthernet1/0/2")},
			"1040": {Value: []byte("Te1/0/2 Module Temperature Sensor")},
			"1041": {Value: []byte("Te1/0/2 Supply Voltage Sensor")},
			"1042": {Value: []byte("Te1/0/2 Bias Current Sensor")},
			"1043": {Value: []byte("Te1/0/2 Transmit Power Sensor")},
			"1044": {Value: []byte("Te1/0/2 Receive Power Sensor")},
			// A chassis thermometer: type 8 like the module's, and NOT a cage.
			"1008": {Value: []byte("SW#1, Sensor#1, GREEN")},
		},
		oidEntPhysicalContainedIn: {
			"1040": {Type: gosnmp.Integer, Value: 1039}, "1041": {Type: gosnmp.Integer, Value: 1039}, "1042": {Type: gosnmp.Integer, Value: 1039},
			"1043": {Type: gosnmp.Integer, Value: 1039}, "1044": {Type: gosnmp.Integer, Value: 1039}, "1008": {Type: gosnmp.Integer, Value: 1},
		},
		oidCiscoSensorType: {
			"1040": {Type: gosnmp.Integer, Value: 8}, "1041": {Type: gosnmp.Integer, Value: 4}, "1042": {Type: gosnmp.Integer, Value: 5},
			"1043": {Type: gosnmp.Integer, Value: 14}, "1044": {Type: gosnmp.Integer, Value: 14}, "1008": {Type: gosnmp.Integer, Value: 8},
		},
		oidCiscoSensorScale: {
			"1040": {Type: gosnmp.Integer, Value: 9}, "1041": {Type: gosnmp.Integer, Value: 9}, "1042": {Type: gosnmp.Integer, Value: 8},
			"1043": {Type: gosnmp.Integer, Value: 9}, "1044": {Type: gosnmp.Integer, Value: 9}, "1008": {Type: gosnmp.Integer, Value: 9},
		},
		oidCiscoSensorPrecision: {
			"1040": {Type: gosnmp.Integer, Value: 1}, "1041": {Type: gosnmp.Integer, Value: 2}, "1042": {Type: gosnmp.Integer, Value: 1},
			"1043": {Type: gosnmp.Integer, Value: 1}, "1044": {Type: gosnmp.Integer, Value: 1}, "1008": {Type: gosnmp.Integer, Value: 0},
		},
		oidCiscoSensorValue: {
			"1040": {Type: gosnmp.Integer, Value: 505}, "1041": {Type: gosnmp.Integer, Value: 325}, "1042": {Type: gosnmp.Integer, Value: 444},
			"1043": {Type: gosnmp.Integer, Value: -39}, "1044": {Type: gosnmp.Integer, Value: -27}, "1008": {Type: gosnmp.Integer, Value: 43},
		},
	}
	var got = ciscoOpticalSamples("dev-1", tables, time.Now())

	var by = map[string]metricbuf.Sample{}
	var s metricbuf.Sample
	for _, s = range got {
		by[s.Name] = s
		if s.Labels["port"] != "TenGigabitEthernet1/0/2" {
			t.Errorf("%s landed on port %q — the port is the sensor's PARENT entity",
				s.Name, s.Labels["port"])
		}
		if s.Labels["ifindex"] != "" {
			t.Errorf("%s carries an ifindex: an entPhysicalIndex is not an "+
				"interface index and must never be published as one", s.Name)
		}
	}
	// The chassis thermometer must NOT have become an SFP temperature.
	if len(got) != 5 {
		t.Fatalf("%d samples, want 5 — the chassis sensor (type 8, parent 1) must "+
			"not be read as module temperature: %+v", len(got), got)
	}
	// scale 9 / precision 1 → raw/10. THE DIRECTION COMES FROM THE NAME.
	if by[metricbuf.MetricSFPTxPowerDBM].Value != -3.9 {
		t.Errorf("TX = %v, want -3.9 dBm", by[metricbuf.MetricSFPTxPowerDBM].Value)
	}
	if by[metricbuf.MetricSFPRxPowerDBM].Value != -2.7 {
		t.Errorf("RX = %v, want -2.7 dBm", by[metricbuf.MetricSFPRxPowerDBM].Value)
	}
	// scale 8 (milli) — the one sensor that does not declare units, and the
	// reason the exponent is applied rather than assumed.
	// The agent declares scale 8 (milli) and the series IS milliamps, so the two
	// agree and only the precision applies: 44.4 mA, not 0.0444 A.
	var bias = by[metricbuf.MetricSFPTxBiasMilliamps].Value
	if bias < 44.39 || bias > 44.41 {
		t.Errorf("bias = %v, want 44.4 mA", bias)
	}
	// precision 2 — likewise the one that does not declare a single decimal.
	if by[metricbuf.MetricSFPSupplyVolts].Value != 3.25 {
		t.Errorf("voltage = %v, want 3.25 V", by[metricbuf.MetricSFPSupplyVolts].Value)
	}
	if by[metricbuf.MetricSFPTemperature].Value != 50.5 {
		t.Errorf("temperature = %v, want 50.5 C", by[metricbuf.MetricSFPTemperature].Value)
	}
}

// A power sensor the agent does not call dBm is REFUSED. The series' whole
// meaning is its unit, and a name alone is not evidence of one.
func TestCiscoDOMRefusesAPowerSensorWithoutTheDBmType(t *testing.T) {
	var tables = map[string]map[string]gosnmp.SnmpPDU{
		oidEntPhysicalName: {
			"9":  {Value: []byte("Gi1/0/1")},
			"10": {Value: []byte("Gi1/0/1 Receive Power Sensor")},
		},
		oidEntPhysicalContainedIn: {"10": {Type: gosnmp.Integer, Value: 9}},
		oidCiscoSensorType:        {"10": {Type: gosnmp.Integer, Value: 6}}, // watts, not dBm
		oidCiscoSensorScale:       {"10": {Type: gosnmp.Integer, Value: 9}},
		oidCiscoSensorPrecision:   {"10": {Type: gosnmp.Integer, Value: 1}},
		oidCiscoSensorValue:       {"10": {Type: gosnmp.Integer, Value: -27}},
	}
	var got = ciscoOpticalSamples("dev-1", tables, time.Now())
	if len(got) != 0 {
		t.Errorf("published %+v for a sensor the agent does not call dBm", got)
	}
}
