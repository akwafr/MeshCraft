package snmppoll

import (
	"strconv"
	"strings"
	"time"

	"github.com/gosnmp/gosnmp"

	"meshcraft/internal/metricbuf"
)

// SENSORS: device health and transceiver optical diagnostics.
//
// This file adds to the poller the two OID sets the vault requested from the start without
// anyone implementing them. The finding was recorded in [[Roadmap]]: "there is NO optical
// power collector, and there never was: `internal/snmppoll` only knows `if_state` and
// `if_counters`, i.e. eight standard MIB OIDs and NO sensors -- neither optical, nor
// temperature, nor PoE." And in [[Monitoring]] section 1, the "applied cadence" column showed
// *not collected* on the Health row.
//
// # What is STANDARD vs PROPRIETARY, and why both are needed
//
// That differs from `if_state` and `if_counters`, which live entirely in MIB-II -- therefore
// work on any device without per-vendor catalog. Here it is not possible:
//
//	sysUpTime            MIB-II          STANDARD, everyone serves it
//	hrProcessorLoad      HOST-RESOURCES  standard, but NOT everyone serves it
//	hrStorage*           HOST-RESOURCES  same -- RouterOS publishes RAM here
//	mtxrHealth           MIKROTIK-MIB    PROPRIETARY: temperatures, voltages,
//	                                     fans, power supplies
//	mtxrOpticalTable     MIKROTIK-MIB    PROPRIETARY: full SFP diagnostics
//
// There is NO standardized switch health MIB. ENTITY-SENSOR-MIB (RFC 3433) exists, but
// MikroTik does not implement it -- leaving proprietary branch or no curve at all. Compromise:
// put proprietary OIDs in the POLLER (they cost nothing when device does not serve them: missing
// variable simply returns nothing) not in connectors, because collection runs on proxy, which
// knows no connector.
//
// # THIS FILE WAS CHECKED AGAINST HARDWARE
//
// An earlier version of this comment said "NONE of these OIDs were read on the two reference
// MikroTik devices", and drew caution on scale factors. **That is no longer true**, and saying
// so is half the value of what follows -- stale warning costs twice: keep doubting what is
// verified, stop believing warnings that still hold.
//
// Survey on July 29, 2026 on **MikroTik CSS326-24G-2S+ SwOS 2.17** (10.99.0.36), via proxy
// diagnostic walk (`POST /api/devices/{id}/snmp-walk`, v2c) -- the exact gesture this comment
// prescribed:
//
//	sysDescr.0                      CSS326-24G-2S+ SwOS v2.17
//	sysObjectID.0                   1.3.6.1.4.1.14988.2   (NOT 14988.1)
//	sysUpTime.0                     served
//	mtxrHealth.11.0                 690, when /sys.b `temp` gives 0x45 = 69 C
//	mtxrOptical.6.25 (empty cage)   4294967168 = 0xFFFFFF80 = -128
//	hrProcessorLoad, hrStorage      NO values -- not served by this agent
//	14988.2 (subtree)               NO values -- identity OID is not a branch
//
// **TEMPERATURE DIVIDE-BY-10 IS THEREFORE MEASURED**, not borrowed from Observium and
// LibreNMS: two independent channels on same device, same instant, give 69 and 690. The gesture
// this comment demanded is done, on another product line than targeted, with same effect.
//
// **AND SIGN WAS A REAL BUG** (cf. [healthSensor].Signed and [signed32]). Empty SFP cage gives
// -128 C encoded as two's complement in unsigned `Gauge32`. Without fix, series published
// 429496716 C -- same bug hit optical POWERS, negative in dBm on any real link. Optical column
// comment described the trap; no code applied it.
//
// STILL UNSURVEYED, keep in mind: voltages (`mtxrHealth.8`), fans (`.17`, `.18`), PSUs (`.15`,
// `.16`) exist on neither reference MikroTik -- CSS326 serves zero in /sys.b and SNMP agent
// publishes none. Their scales and `psuOK` convention remain inferred until a chassis that
// carries them enters the fleet.

// Two sensor sets, named like connector channels ([driver.MonitorChannel].Metrics) and
// standard model.
//
// Vocabulary is SHARED with capability declarations, so "this connector declares `health`"
// and "poller reads `health`" mean the same thing. Two vocabularies would declare capability
// nobody polls -- worst of both worlds, screen promises the curve.
const (
	MetricHealth  = "health"
	MetricOptical = "optical_power"
)

// --- Standard OIDs ---

const (
	// oidSysUpTime: MIB-II, in HUNDREDTHS of a second.
	//
	// Usage value is not duration but its BREAK: a value that drops is a reboot. Only way to
	// see reboot between polls without staying unreachable long enough for ICMP watch -- switch
	// reboots in ~thirty seconds, ping debounce asks three.
	//
	// OVERFLOW warning: `sysUpTime` is 32-bit TimeTicks, wraps every 497 days. Well-kept fleet
	// therefore sees ghost "reboot" every sixteen months. SNMP type limit, not this collection,
	// and `sysUpTimeInstance` from SNMPv2-MIB does not fix it either.
	oidSysUpTime = "1.3.6.1.2.1.1.3"
)

const (
	// oidHrProcessorLoad: HOST-RESOURCES-MIB, percent, one entry per core.
	//
	// One entry PER CORE decides translation: CRS326 survey shows `cpu-count: "2"`. Two indexed
	// series would be two curves no screen summarizes; their AVERAGE is device load, the question
	// asked. Average is right aggregate here not max: one core at 100% on dual CPU is half busy
	// device, not saturated.
	oidHrProcessorLoad = "1.3.6.1.2.1.25.3.3.1.2"
	// oidHrStorageDescr / Units / Size / Used: HOST-RESOURCES-MIB.
	//
	// FOUR columns for ONE measure, and four are needed. Table describes ALL device storage --
	// RAM, flash, file systems. Without `hrStorageDescr`, unknown which row is RAM, and first
	// row would pass 16 MiB flash for 512 MiB device. Without `hrStorageAllocationUnits`,
	// sizes have no unit: they count BLOCKS whose size is in that column.
	oidHrStorageDescr = "1.3.6.1.2.1.25.2.3.1.3"
	oidHrStorageUnits = "1.3.6.1.2.1.25.2.3.1.4"
	oidHrStorageSize  = "1.3.6.1.2.1.25.2.3.1.5"
	oidHrStorageUsed  = "1.3.6.1.2.1.25.2.3.1.6"
)

// --- CISCO enterprise-9 health, SURVEYED on hardware ---
//
// IOS serves NONE of the HOST-RESOURCES cpu/memory OIDs above; it carries them in
// its own enterprise tree. Walked on a WS-C2960X-24TD-L (15.2(6)E) 2026-08-02, and
// every scale here is READ FROM THE DEVICE, not borrowed:
//
//	cpmCPUTotal5minRev  .109.1.1.1.1.8.1   = 23     → percent, no scale
//	ciscoMemoryPoolUsed .48.1.1.1.5.1      = 69157616  (Processor pool)
//	ciscoMemoryPoolFree .48.1.1.1.6.1      = 296576052 → bytes, no scale
//	ciscoEnvMonTempValue .13.1.3.1.3.1008  = 43     → Celsius, no scale
//	                     .13.1.3.1.2.1008  = "SW#1, Sensor#1, GREEN"
//
// Walking these on a MikroTik returns nothing, exactly as walking the mtxr
// sensors returns nothing on a Cisco: the poller reads the union and publishes
// what answered, which is the pattern this whole file already follows.
const (
	// oidCiscoCPU5min: cpmCPUTotal5minRev — a percentage gauge, 0..100. The
	// "Rev" column is the one to use: the pre-Rev cpmCPUTotal5min saturates at
	// 100 on some platforms and this one does not.
	oidCiscoCPU5min = "1.3.6.1.4.1.9.9.109.1.1.1.1.8"
	// ciscoMemoryPool: name column identifies the pool, Used/Free carry bytes.
	oidCiscoMemName = "1.3.6.1.4.1.9.9.48.1.1.1.2"
	oidCiscoMemUsed = "1.3.6.1.4.1.9.9.48.1.1.1.5"
	oidCiscoMemFree = "1.3.6.1.4.1.9.9.48.1.1.1.6"
	// ciscoEnvMonTemperature: description names the sensor, value is Celsius.
	oidCiscoTempDescr = "1.3.6.1.4.1.9.9.13.1.3.1.2"
	oidCiscoTempValue = "1.3.6.1.4.1.9.9.13.1.3.1.3"
)

// --- MikroTik proprietary OIDs: mtxrHealth ---
//
// Each sensor is a SCALAR, full OID followed by `.0`. That forces walking them ONE BY ONE
// instead of whole branch `…1.1.3`, for mechanical reason: poller indexes tables by LAST OID
// segment (cf. suffixIndex), and all scalars on one branch end in `.0`. Whole branch walk
// would overwrite them and keep one at random -- temperature sometimes voltage: defect no
// screen can flag.
//
// COST of split, priced because device pays: ten walks for `mtxrHealth`, one for `sysUpTime`,
// one for `hrProcessorLoad`, four for `hrStorage`, plus six Cisco enterprise-9 branches --
// twenty-two requests per cycle, of which any given device answers only its own half. At health
// cadence (five minutes, cf. `wantedHealthInterval`), one packet every twenty seconds per
// device. Compare six `if_counters` columns every five seconds: health costs two orders of
// magnitude less than traffic.

// healthSensor describes a MIKROTIK-MIB sensor and what we do with it.
type healthSensor struct {
	// Scalar branch OID, without `.0`.
	OID string
	// Name is `sensor` series label. MIB name, lowercase without `mtxrHl` prefix: operator
	// should not need MIB nomenclature to read a graph.
	Name string
	// Metric is destination series in `metricbuf` vocabulary.
	Metric string
	// Scale multiplies raw value. See file header warning: `mtxrHealth` divisors were INFERRED,
	// not surveyed.
	//
	// Temperature divide-by-10 is no longer inferred: MEASURED on hardware. CSS326-24G-2S+ SwOS
	// 2.17 (10.99.0.36), same instant, `/sys.b temp` gives `0x45` (69 C) and `mtxrHealth.11.0`
	// gives `690`. Factor is 0.1, gesture done.
	Scale float64
	// Signed: raw value is 32-bit SIGNED integer, two's complement in `Gauge32`.
	//
	// Field born from MEASURED value, applies to all temperatures. On reference CSS326, empty
	// SFP1 cage optical temperature (`mtxrOptical.6.25`) is `4294967168` -- `0xFFFFFF80`, **-128**
	// two's complement. SwOS connector already says for web ("empty cage recognized by temperature
	// device returns at -128 C"); SNMP arrives UNSIGNED.
	//
	// Without field, chain applied 0.1 to 4294967168 and published **429496716 C** -- value that
	// crushes scale of all sensors on same graph and would trip threshold on empty cage.
	//
	// Does NOT apply to fans or PSUs: RPM and bool have no negative, and Signed everywhere would
	// misread legitimately large counters.
	Signed bool
}

// signed32 reinterprets `Gauge32` as signed 32-bit integer.
//
// MikroTik MIB declares these as `Gauge32`, UNSIGNED by RFC 2578 -- but device writes
// negative temperatures in two's complement. Vendor left the type, not us: best we can do is
// know it. MEASURED: empty SFP cage at `0xFFFFFF80` on reference CSS326.
//
// Threshold is 2^31 not magnitude heuristic: beyond, value cannot be plausible temperature;
// below, taken as-is -- no legitimate value altered.
func signed32(raw float64) float64 {
	const twoPow31 = 2147483648
	const twoPow32 = 4294967296
	if raw >= twoPow31 {
		return raw - twoPow32
	}
	return raw
}

// healthSensors lists sensors from MIKROTIK-MIB survey.
//
// # What is in, and what is out
//
// Ten entries of nineteen `mtxrHealth` defines. Nine others -- 3.3V/5V/12V rails, current,
// power, CPU frequency, alarm jack, gauge table -- are EXCLUDED for two distinct reasons:
//
//   - most exist ONLY on chassis lines (CCR, rack CRS3xx). Walking on access switch costs
//     request for "no such object";
//   - current and power have no series in project vocabulary; creating one for unread value is
//     cardinality without reader.
//
// Adding when chassis enters fleet is one line here. What would NOT be a line, why list is
// explicit not branch walk: decide which series and scale each sensor gets.
var healthSensors = []healthSensor{
	// Temperatures. Five entries because MIB defines five and lines expose different ones:
	// CRS326 CPU temp (`cpu-temperature` REST), chassis board temp, outdoor antenna sensor.
	// Label each by name rather than merge so threshold alerts work without knowing product line.
	{"1.3.6.1.4.1.14988.1.1.3.5", "sensor", metricbuf.MetricTemperature, 0.1, true},
	{"1.3.6.1.4.1.14988.1.1.3.6", "cpu", metricbuf.MetricTemperature, 0.1, true},
	{"1.3.6.1.4.1.14988.1.1.3.7", "board", metricbuf.MetricTemperature, 0.1, true},
	{"1.3.6.1.4.1.14988.1.1.3.10", "ambient", metricbuf.MetricTemperature, 0.1, true},
	{"1.3.6.1.4.1.14988.1.1.3.11", "processor", metricbuf.MetricTemperature, 0.1, true},
	// Input voltage. One of four MIB defines: others are internal rails only redundant PSU
	// cards publish.
	{"1.3.6.1.4.1.14988.1.1.3.8", "input", metricbuf.MetricVoltage, 0.1, false},
	// Fans, RPM with NO scale factor: MIB type is bare `Gauge32`, rotation speed should not
	// be scaled.
	{"1.3.6.1.4.1.14988.1.1.3.17", "fan1", metricbuf.MetricFanRPM, 1, false},
	{"1.3.6.1.4.1.14988.1.1.3.18", "fan2", metricbuf.MetricFanRPM, 1, false},
	// PSU state. See [psuOK] for value convention -- only place in file where misread INVERTS
	// alarm.
	{"1.3.6.1.4.1.14988.1.1.3.15", "main", metricbuf.MetricPSUOK, 1, false},
	{"1.3.6.1.4.1.14988.1.1.3.16", "backup", metricbuf.MetricPSUOK, 1, false},
}

// --- MikroTik proprietary OIDs: optical table ---
//
// Unlike `mtxrHealth`, real TABLE: columns indexed by interface index, so last-segment
// poller indexing works and one column walks all cages at once.
//
// Unlike `mtxrHealth`, scale factors are PUBLISHED by MIB (`GDiv100`, `GDiv1000`, `IDiv1000`).
// Not same certainty as health divide-by-10 -- difference between third-party convention and
// vendor declaration.
const (
	oidOpticalName        = "1.3.6.1.4.1.14988.1.1.19.1.1.2"
	oidOpticalRxLoss      = "1.3.6.1.4.1.14988.1.1.19.1.1.3"
	oidOpticalTxFault     = "1.3.6.1.4.1.14988.1.1.19.1.1.4"
	oidOpticalWavelength  = "1.3.6.1.4.1.14988.1.1.19.1.1.5"
	oidOpticalTemperature = "1.3.6.1.4.1.14988.1.1.19.1.1.6"
	oidOpticalVoltage     = "1.3.6.1.4.1.14988.1.1.19.1.1.7"
	oidOpticalTxBias      = "1.3.6.1.4.1.14988.1.1.19.1.1.8"
	oidOpticalTxPower     = "1.3.6.1.4.1.14988.1.1.19.1.1.9"
	oidOpticalRxPower     = "1.3.6.1.4.1.14988.1.1.19.1.1.10"
)

// opticalColumns maps each column to series and scale.
// `Signed`: column carries 32-bit SIGNED two's complement regardless of MIB declared type.
// Three of eight columns -- missing sign was REAL bug not theory; optical power comment below
// described it ("reading as unsigned gives billions") with no code applying it.
//
// MEASUREMENT: reference CSS326 empty SFP1, `mtxrOptical.6.25` (module temp) is `4294967168`,
// `0xFFFFFF80`, **-128**. SwOS web already announces -128 C for empty cage; SNMP unsigned,
// series published 4294967168 C.
var opticalColumns = []struct {
	OID    string
	Metric string
	Scale  float64
	Signed bool
}{
	// `GDiv100`: wavelength in hundredths of nanometer. UNSIGNED: negative wavelength impossible.
	{oidOpticalWavelength, metricbuf.MetricSFPWavelengthNM, 0.01, false},
	// Bare `Gauge32`: integer Celsius. NOTE: transceiver temp does NOT share CARD scale in same
	// MIB. Applying `mtxrHealth` divisor here would show 5 C for 50 C module.
	{oidOpticalTemperature, metricbuf.MetricSFPTemperature, 1, true},
	// `GDiv1000`: supply voltage in millivolts.
	{oidOpticalVoltage, metricbuf.MetricSFPSupplyVolts, 0.001, false},
	// Bare `Gauge32`: bias current in milliamps.
	{oidOpticalTxBias, metricbuf.MetricSFPTxBiasMilliamps, 1, false},
	// `IDiv1000`, `I` matters: SIGNED integers, thousandths of dBm. Optical power is almost
	// always NEGATIVE dBm -- good link -3 to -15 dBm -- unsigned read gives billions.
	{oidOpticalTxPower, metricbuf.MetricSFPTxPowerDBM, 0.001, true},
	{oidOpticalRxPower, metricbuf.MetricSFPRxPowerDBM, 0.001, true},
	// Module alarm flags. MIB `BoolValue`: 1 = true.
	{oidOpticalRxLoss, metricbuf.MetricSFPRxLoss, 1, false},
	{oidOpticalTxFault, metricbuf.MetricSFPTxFault, 1, false},
}

// healthOIDs returns branches to walk for `health` set.
func healthOIDs() []string {
	var out = []string{oidSysUpTime, oidHrProcessorLoad,
		oidHrStorageDescr, oidHrStorageUnits, oidHrStorageSize, oidHrStorageUsed,
		// Cisco enterprise-9: empty on every other platform, cf. the constants.
		oidCiscoCPU5min, oidCiscoMemName, oidCiscoMemUsed, oidCiscoMemFree,
		oidCiscoTempDescr, oidCiscoTempValue}
	var i int
	for i = range healthSensors {
		out = append(out, healthSensors[i].OID)
	}
	return out
}

// opticalOIDs returns columns to walk for `optical_power` set.
//
// `mtxrOpticalName` included deliberately: it gives front-panel cage name. Without it series
// only has interface index, and "RX power of index 26" tells operator nothing. Poller does NOT
// walk `ifDescr` here -- second source for same info eventually disagrees.
func opticalOIDs() []string {
	var out = []string{oidOpticalName}
	var i int
	for i = range opticalColumns {
		out = append(out, opticalColumns[i].OID)
	}
	return out
}

// HealthSamples translates health branches into samples.
//
// PURE function like [Samples]: translation bugs must be testable without device or network.
//
// No port label: DEVICE-level measures. Only dimension label is `sensor` for multi-instance
// quantities on one board.
func HealthSamples(deviceID string, tables map[string]map[string]gosnmp.SnmpPDU,
	at time.Time) []metricbuf.Sample {
	var out []metricbuf.Sample
	var base = map[string]string{"device": deviceID}

	// Uptime: hundredths of second to seconds.
	var v, ok = scalarOf(tables[oidSysUpTime])
	if ok {
		out = append(out, metricbuf.Sample{
			Name: metricbuf.MetricUptime, Labels: base, Value: v / 100, TS: at,
		})
	}

	// CPU load: AVERAGE of cores, cf. oidHrProcessorLoad comment.
	var total float64
	var n int
	var pdu gosnmp.SnmpPDU
	for _, pdu = range tables[oidHrProcessorLoad] {
		var load, okLoad = toFloat(pdu)
		if !okLoad {
			continue
		}
		total += load
		n++
	}
	var cpuDone bool
	if n > 0 {
		out = append(out, metricbuf.Sample{
			Name: metricbuf.MetricCPULoad, Labels: base, Value: total / float64(n), TS: at,
		})
		cpuDone = true
	}

	var mem = memorySamples(deviceID, tables, at)
	out = append(out, mem...)

	// Cisco enterprise-9, published only for the axes HOST-RESOURCES did not
	// cover on this device. On IOS that is all of them; on a MikroTik the tables
	// are empty and this adds nothing.
	out = append(out, ciscoHealthSamples(deviceID, tables, at, cpuDone, len(mem) > 0)...)

	// Proprietary sensors.
	var i int
	for i = range healthSensors {
		var raw, okRaw = scalarOf(tables[healthSensors[i].OID])
		if !okRaw {
			continue
		}
		var labels = map[string]string{"device": deviceID, "sensor": healthSensors[i].Name}
		// SIGN before scale, only correct order: 0.1 on 4294967168 then fix does not yield -128.
		if healthSensors[i].Signed {
			raw = signed32(raw)
		}
		var value = raw * healthSensors[i].Scale
		if healthSensors[i].Metric == metricbuf.MetricPSUOK {
			value = psuOK(raw)
		}
		out = append(out, metricbuf.Sample{
			Name: healthSensors[i].Metric, Labels: labels, Value: value, TS: at,
		})
	}
	return out
}

// psuOK translates MIKROTIK-MIB `BoolValue` to "1 = healthy".
//
// # Only place in file where mistake INVERTS alarm
//
// `BoolValue` enumerates `true(1)` and `false(2)`. ASN.1 usual, matches SNMPv2-TC `TruthValue`
// here, but many vendor MIBs use 0 for false. Explicit translation not `raw != 0`: on latter,
// value 2 ("PSU fault") reads as true/healthy while PSU is dead.
//
// Out-of-enum value returns 0 -- "not healthy" -- right default on alarm: unknown is fault,
// never ack. Same rule as "unknown capability assumed absent", applied to supervision.
func psuOK(raw float64) float64 {
	if raw == 1 {
		return 1
	}
	return 0
}

// memorySamples publishes RAM from `hrStorage` table.
//
// # Finding the right ROW is the whole job
//
// `hrStorage` lists all storage. RouterOS RAM appears as "main memory" -- flash, filesystem,
// disks occupy other rows. First row would pass CRS326 16 MiB flash (`total-hdd-space:
// "16777216"`) as 512 MiB RAM (`total-memory: "536870912"`).
//
// Recognition by LABEL, two accepted forms ("main memory" and "memory") because firmwares
// differ. Unknown label yields NO sample: no memory curve beats flash curve.
//
// # Sizes are in BLOCKS
//
// `hrStorageSize` and `hrStorageUsed` count allocation units from `hrStorageAllocationUnits`
// -- typically 1024 bytes. Missing multiply makes memory 1000x too small, plausibly wrong.
//
// # Published FREE not used
//
// Two series `free` and `total`, no utilization percent: percent hides whether 40 MiB or 4 GiB
// remain. Free is CALCULATED (`size - used`) because table publishes used but question is
// free -- same as RouterOS `free-memory`.
func memorySamples(deviceID string, tables map[string]map[string]gosnmp.SnmpPDU,
	at time.Time) []metricbuf.Sample {
	var descrs = tables[oidHrStorageDescr]
	if len(descrs) == 0 {
		return nil
	}
	var idx string
	var pdu gosnmp.SnmpPDU
	var found string
	for idx, pdu = range descrs {
		var label = strings.ToLower(strings.TrimSpace(asString(pdu)))
		if label == "main memory" || label == "memory" {
			found = idx
			break
		}
	}
	if found == "" {
		return nil
	}

	var unit float64 = 1
	var u, okUnit = toFloat(tables[oidHrStorageUnits][found])
	if okUnit && u > 0 {
		unit = u
	}
	var size, okSize = toFloat(tables[oidHrStorageSize][found])
	var used, okUsed = toFloat(tables[oidHrStorageUsed][found])
	if !okSize {
		return nil
	}

	var base = map[string]string{"device": deviceID}
	var out = []metricbuf.Sample{{
		Name: metricbuf.MetricMemoryTotal, Labels: base, Value: size * unit, TS: at,
	}}
	if okUsed {
		out = append(out, metricbuf.Sample{
			Name:   metricbuf.MetricMemoryFree,
			Labels: base, Value: (size - used) * unit, TS: at,
		})
	}
	return out
}

// OpticalSamples translates optical table into samples.
//
// PURE function.
//
// # No `ifType` filter here, ASSUMED difference from [Samples]
//
// `Samples` drops non-`ethernetCsmacd(6)` because `ifTable` mixes front ports with aggregates,
// tunnels, VLAN interfaces. `mtxrOpticalTable` only has optical cages -- device filters by
// construction. Same guard would cost two walks (`ifDescr`, `ifType`) to filter already filtered
// table.
//
// Index bound still applied when known: not to drop logical interfaces -- there are none -- but
// as only protection against aberrant indexes inflating controller series cardinality.
//
// # EMPTY cage produces nothing
//
// Absent module recognized by missing NAME in table. Same principle as other channels,
// different sentinel -- RouterOS omits `sfp-*`, SwOS gives -128 C, here no row. Publishing
// zeros would show dead transceivers on switches with empty cages -- permanent false alert on
// most of fleet.
func OpticalSamples(deviceID string, tables map[string]map[string]gosnmp.SnmpPDU,
	at time.Time, maxIfIndex int) []metricbuf.Sample {
	var out []metricbuf.Sample
	var names = map[string]string{}
	var idx string
	var pdu gosnmp.SnmpPDU
	for idx, pdu = range tables[oidOpticalName] {
		var n = strings.TrimSpace(asString(pdu))
		if n != "" {
			names[idx] = n
		}
	}

	var c int
	for c = range opticalColumns {
		for idx, pdu = range tables[opticalColumns[c].OID] {
			var port, known = names[idx]
			if !known {
				continue // unnamed cage: no module, or incomplete table
			}
			if !withinFacade(idx, maxIfIndex) {
				continue
			}
			var v, ok = toFloat(pdu)
			if !ok {
				continue
			}
			// Sign BEFORE scale: 0.001 on 4294969796 then fix does not yield -7.2 dBm.
			if opticalColumns[c].Signed {
				v = signed32(v)
			}
			out = append(out, metricbuf.Sample{
				Name: opticalColumns[c].Metric,
				Labels: map[string]string{
					"device": deviceID, "ifindex": idx, "port": port,
				},
				Value: v * opticalColumns[c].Scale,
				TS:    at,
			})
		}
	}
	return out
}

// withinFacade reports whether interface index fits declared front panel.
//
// 0 = no bound, take all: facade not always known, and dropping supervision because port
// groups unread yet is wrong side of tradeoff.
func withinFacade(idx string, maxIfIndex int) bool {
	if maxIfIndex <= 0 {
		return true
	}
	var n, err = strconv.Atoi(idx)
	return err == nil && n >= 1 && n <= maxIfIndex
}

// scalarOf returns value from branch carrying only ONE scalar.
//
// Poller indexes by last OID segment: scalar arrives under key "0". Not hardcoded because
// agent serving scalar without `.0` (direct `snmpget`) uses another key -- refusing loses
// temperature over form.
//
// MULTIPLE entries return `ok=false`: branch should be scalar; two means misread. Picking one
// at random publishes value nobody can attribute.
func scalarOf(table map[string]gosnmp.SnmpPDU) (float64, bool) {
	if len(table) != 1 {
		return 0, false
	}
	var pdu gosnmp.SnmpPDU
	for _, pdu = range table {
		return toFloat(pdu)
	}
	return 0, false
}

// ciscoHealthSamples publishes CPU, memory and temperature from the Cisco
// enterprise-9 tree.
//
// PURE, like the rest of this file. `cpuDone` / `memDone` say whether the
// HOST-RESOURCES path already covered those axes — on a device serving both
// (none in this fleet does), the standard MIB wins and this stays quiet, so a
// metric is never published twice with the same labels and timestamp.
//
// Scales are the DEVICE'S, surveyed 2026-08-02: CPU is a percentage, memory is
// bytes, temperature is Celsius — none needs adjusting (cf. the OID constants).
func ciscoHealthSamples(deviceID string, tables map[string]map[string]gosnmp.SnmpPDU,
	at time.Time, cpuDone, memDone bool) []metricbuf.Sample {
	var out []metricbuf.Sample
	var base = map[string]string{"device": deviceID}

	// CPU: cpmCPUTotal5minRev. One row per CPU entity; a switch has one, and the
	// MAX is the honest summary if a stack ever reports several — a busy member
	// is not hidden by an idle one.
	if !cpuDone {
		var best float64
		var have bool
		var pdu gosnmp.SnmpPDU
		for _, pdu = range tables[oidCiscoCPU5min] {
			var v, ok = toFloat(pdu)
			if !ok {
				continue
			}
			if !have || v > best {
				best, have = v, true
			}
		}
		if have {
			out = append(out, metricbuf.Sample{
				Name: metricbuf.MetricCPULoad, Labels: base, Value: best, TS: at,
			})
		}
	}

	// Memory: the "Processor" pool. A 2960-X also lists an "I/O" pool and a
	// "Driver text" pool — reporting those as system memory would draw a curve
	// that has nothing to do with how much RAM the switch has left. FREE is
	// published (the question is how much remains), TOTAL is Used+Free.
	if !memDone {
		var names = tables[oidCiscoMemName]
		var idx string
		var pdu gosnmp.SnmpPDU
		var found string
		for idx, pdu = range names {
			if strings.EqualFold(strings.TrimSpace(asString(pdu)), "Processor") {
				found = idx
				break
			}
		}
		if found != "" {
			var used, okU = toFloat(tables[oidCiscoMemUsed][found])
			var free, okF = toFloat(tables[oidCiscoMemFree][found])
			if okF {
				out = append(out, metricbuf.Sample{
					Name: metricbuf.MetricMemoryFree, Labels: base, Value: free, TS: at,
				})
			}
			if okU && okF {
				out = append(out, metricbuf.Sample{
					Name: metricbuf.MetricMemoryTotal, Labels: base, Value: used + free, TS: at,
				})
			}
		}
	}

	// Temperature: one sample per sensor, labelled by its description so a
	// multi-sensor or stacked switch does not collapse onto one curve.
	var descrs = tables[oidCiscoTempDescr]
	var idx string
	var pdu gosnmp.SnmpPDU
	for idx, pdu = range descrs {
		var v, ok = toFloat(tables[oidCiscoTempValue][idx])
		if !ok {
			continue
		}
		var name = strings.TrimSpace(asString(pdu))
		if name == "" {
			name = "sensor " + idx
		}
		out = append(out, metricbuf.Sample{
			Name:   metricbuf.MetricTemperature,
			Labels: map[string]string{"device": deviceID, "sensor": name},
			Value:  v, TS: at,
		})
	}
	return out
}
