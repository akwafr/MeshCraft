package metricbuf

// THE SERIES VOCABULARY, in one place.
//
// # Why these names live HERE and not with their producers
//
// They were hard-coded in `internal/snmppoll`, the sole sample producer in the
// product — which worked fine as long as it was. It no longer is: MikroTik
// connectors now produce the same series from other channels (the RouterOS binary
// API, page scraping on SwOS), and two producers of one series is exactly where a
// copied name eventually diverges by one letter.
//
// What that divergence costs is not a compile error, it is WORSE: two adjacent
// series in VictoriaMetrics, `meshcraft_if_in_octets_total` on one side and
// `meshcraft_if_in_octet_total` on the other, each populated for half the fleet.
// No screen would flag anything — it would simply show a broken curve where the
// device changes platform, and nobody could tell if it is the network or
// collection.
//
// This package is the right place because it is the LEAF that all producers
// already import: the proxy SNMP poller, and the connectors. None of them import
// each other.
//
// # Conventions, and what they forbid
//
//   - suffix `_total` for a MONOTONIC COUNTER, and for it alone. That is the
//     Prometheus convention, and it is not decorative: applying a rate function
//     to a gauge yields nonsense, and a gauge named `_total` invites it;
//   - the UNIT is always in the name (`_bytes`, `_seconds`, `_celsius`, `_dbm`).
//     A series without a unit forces you to find its producer to know whether a
//     "45" is a percentage, a degree, or a decivolt;
//   - LABELS carry identity (`device`, `port`, `ifindex`, `sensor`), never the
//     value. Putting a temperature in a label would create one series per degree.
//
// # Labels, and the only one that drives a decision
//
//	device    the meshcraft device identifier. Required everywhere: without it,
//	          two switches blend together.
//	port      the facade port name, as the device names it.
//	ifindex   the SNMP interface index. PRESENT ONLY on samples from SNMP.
//	iftype    the IANA interface type. This is THE only label whose controller
//	          derives an inventory consequence (cf. section 2.1 of
//	          [[Monitoring unifie et pipelines]]): without it, a sample feeds
//	          curves but does not touch the facade.
//	sensor    the sensor name, for health series with several sensors.
//
// Direct and INTENDED consequence for non-SNMP channels: samples produced by the
// binary API or page scraping carry NEITHER `ifindex` NOR `iftype`, because those
// channels have no interface index to give. They therefore feed series without
// ever being able to change inventory — which is precisely the rule "telemetry
// never CREATES a port", obtained here by construction rather than a guardrail.
const (
	// --- Per port: state ---

	// MetricIfOperStatus: MIB-II `ifOperStatus`, raw value. 1 = up; everything
	// else does not pass traffic (cf. transition diff in cmd/proxy).
	MetricIfOperStatus = "meshcraft_if_oper_status"
	// MetricIfSpeedMbps: the NEGOTIATED link speed, in Mbit/s. A gauge, not a
	// counter: it drops when a link renegotiates.
	MetricIfSpeedMbps = "meshcraft_if_speed_mbps"
	// MetricIfLinkDowns: how many times the link went down SINCE boot, as the
	// device counts it itself.
	//
	// Supports flap detection differently from transitions: those depend on what
	// collection SAW pass; this counter does not. A port that flaps three times
	// between two polls produces one transition and three increments here — the
	// only way to observe flapping faster than the poll cadence.
	MetricIfLinkDowns = "meshcraft_if_link_downs_total"

	// --- Per port: counters ---

	MetricIfInOctets  = "meshcraft_if_in_octets_total"
	MetricIfOutOctets = "meshcraft_if_out_octets_total"
	// MetricIfInPackets / MetricIfOutPackets: frame count. Distinct from octets,
	// and not redundant — small-frame saturation shows here and not there.
	MetricIfInPackets  = "meshcraft_if_in_packets_total"
	MetricIfOutPackets = "meshcraft_if_out_packets_total"
	// MetricIfInErrors / MetricIfOutErrors: frames REJECTED for format defects
	// (FCS, fragment, oversize frame, collision).
	MetricIfInErrors  = "meshcraft_if_in_errors_total"
	MetricIfOutErrors = "meshcraft_if_out_errors_total"
	// MetricIfInDiscards / MetricIfOutDiscards: frames DROPPED for lack of space,
	// though they were valid.
	//
	// Do not merge with errors — this is the most useful distinction in the set:
	// an error blames the CABLE or transceiver; a discard blames CAPACITY.
	// Adding them would send you looking for a bad patch cable in front of a
	// simply saturated link.
	MetricIfInDiscards  = "meshcraft_if_in_discards_total"
	MetricIfOutDiscards = "meshcraft_if_out_discards_total"
	// MetricIfInBitsPerSecond / MetricIfOutBitsPerSecond: INSTANTANEOUS rate when
	// the DEVICE computes it itself.
	//
	// Populated only by platforms that serve a rate (SwOS does, `rrb`/`trb`), and
	// it does not replace counter derivation: it complements it where the poll
	// cadence is too coarse for a fine derivative. A platform that does not serve
	// it leaves the series empty rather than writing a home-grown derivative —
	// the same information computed twice and differently.
	MetricIfInBitsPerSecond  = "meshcraft_if_in_bits_per_second"
	MetricIfOutBitsPerSecond = "meshcraft_if_out_bits_per_second"

	// --- Device health ---

	// MetricUptime: uptime in seconds.
	//
	// Its useful signal is not the duration but its BREAK: a value that drops is a
	// reboot, and the only way to notice a device rebooted between two polls
	// without being unreachable long enough for ping to notice.
	MetricUptime = "meshcraft_uptime_seconds"
	// MetricCPULoad: processor load, in PERCENT (0 to 100).
	//
	// Percent not ratio, because that is the unit platforms serve it in
	// (`cpu-load: 1` on RouterOS): converting would make every producer carry a
	// division nobody needs.
	MetricCPULoad = "meshcraft_cpu_load_percent"
	// MetricMemoryFree / MetricMemoryTotal: RAM, in bytes. Both, not a percentage
	// — an occupancy rate does not say whether 40 MiB or 4 GiB remain.
	MetricMemoryFree  = "meshcraft_memory_free_bytes"
	MetricMemoryTotal = "meshcraft_memory_total_bytes"
	// MetricTemperature: a temperature in degrees Celsius, labeled by `sensor`.
	//
	// One series for all sensors rather than one per sensor: a board may have one,
	// two, or ten depending on the platform, and one series name per sensor would
	// make a fleet-wide alert impossible to write.
	MetricTemperature = "meshcraft_temperature_celsius"
	// MetricVoltage: a voltage in volts, labeled by `sensor`.
	MetricVoltage = "meshcraft_voltage_volts"
	// MetricFanRPM: fan speed in revolutions per minute, labeled by `sensor`.
	MetricFanRPM = "meshcraft_fan_rpm"
	// MetricPSUOK: PSU state, 1 = present and healthy. Labeled by `sensor`.
	MetricPSUOK = "meshcraft_psu_ok"

	// --- SFP optical diagnostics ---

	// MetricSFPTemperature / MetricSFPSupplyVolts / MetricSFPTxBiasMilliamps:
	// the module electrical environment.
	MetricSFPTemperature     = "meshcraft_sfp_temperature_celsius"
	MetricSFPSupplyVolts     = "meshcraft_sfp_supply_volts"
	MetricSFPTxBiasMilliamps = "meshcraft_sfp_tx_bias_milliamps"
	// MetricSFPTxPowerDBM / MetricSFPRxPowerDBM: optical power in dBm.
	//
	// IN dBm NOT MILLIWATTS, and that is not aesthetic: an optical budget is
	// written in dB, attenuation adds in dB, and transceiver vendor thresholds are
	// given in dBm. Publishing milliwatts would make every alert redo the
	// logarithm.
	//
	// Consequence to know: ZERO power has no dBm value (log10(0) is undefined). A
	// cage with no light therefore produces NO sample, rather than a point at
	// minus infinity that would collapse the whole graph scale.
	MetricSFPTxPowerDBM = "meshcraft_sfp_tx_power_dbm"
	MetricSFPRxPowerDBM = "meshcraft_sfp_rx_power_dbm"
	// MetricSFPWavelengthNM: wavelength advertised by the module, in nanometers. A
	// hardware constant, not a measurement — but it spots mismatched modules at
	// both ends of a fiber.
	MetricSFPWavelengthNM = "meshcraft_sfp_wavelength_nm"
	// MetricSFPRxLoss / MetricSFPTxFault: the two module alarm flags, 1 = fault.
	MetricSFPRxLoss  = "meshcraft_sfp_rx_loss"
	MetricSFPTxFault = "meshcraft_sfp_tx_fault"
)
