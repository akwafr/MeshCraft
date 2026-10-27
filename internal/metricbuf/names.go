package metricbuf

// The series vocabulary — every Metric* name and the Sample shape — moved to
// meshcraft/wire/metrics the day the drivers left the controller: an external
// driver cannot import internal/, and the vocabulary must have ONE home
// (wire/metrics/names.go keeps the conventions and the reasoning). These
// aliases keep every controller-side producer and consumer unchanged.

import "meshcraft/wire/metrics"

const (
	MetricIfOperStatus          = metrics.MetricIfOperStatus
	MetricIfSpeedMbps           = metrics.MetricIfSpeedMbps
	MetricIfLinkDowns           = metrics.MetricIfLinkDowns
	MetricIfInOctets            = metrics.MetricIfInOctets
	MetricIfOutOctets           = metrics.MetricIfOutOctets
	MetricIfInPackets           = metrics.MetricIfInPackets
	MetricIfOutPackets          = metrics.MetricIfOutPackets
	MetricIfInMulticastPackets  = metrics.MetricIfInMulticastPackets
	MetricIfOutMulticastPackets = metrics.MetricIfOutMulticastPackets
	MetricIfInBroadcastPackets  = metrics.MetricIfInBroadcastPackets
	MetricIfOutBroadcastPackets = metrics.MetricIfOutBroadcastPackets

	MetricIfInErrors         = metrics.MetricIfInErrors
	MetricIfOutErrors        = metrics.MetricIfOutErrors
	MetricIfInDiscards       = metrics.MetricIfInDiscards
	MetricIfOutDiscards      = metrics.MetricIfOutDiscards
	MetricIfInBitsPerSecond  = metrics.MetricIfInBitsPerSecond
	MetricIfOutBitsPerSecond = metrics.MetricIfOutBitsPerSecond
	MetricUptime             = metrics.MetricUptime
	MetricCPULoad            = metrics.MetricCPULoad
	MetricMemoryFree         = metrics.MetricMemoryFree
	MetricMemoryTotal        = metrics.MetricMemoryTotal
	MetricTemperature        = metrics.MetricTemperature
	MetricVoltage            = metrics.MetricVoltage
	MetricFanRPM             = metrics.MetricFanRPM
	MetricPSUOK              = metrics.MetricPSUOK
	MetricPoEPowerW          = metrics.MetricPoEPowerW
	MetricPoEUsedW           = metrics.MetricPoEUsedW
	MetricSFPTemperature     = metrics.MetricSFPTemperature
	MetricSFPSupplyVolts     = metrics.MetricSFPSupplyVolts
	MetricSFPTxBiasMilliamps = metrics.MetricSFPTxBiasMilliamps
	MetricSFPTxPowerDBM      = metrics.MetricSFPTxPowerDBM
	MetricSFPRxPowerDBM      = metrics.MetricSFPRxPowerDBM
	MetricSFPWavelengthNM    = metrics.MetricSFPWavelengthNM
	MetricSFPRxLoss          = metrics.MetricSFPRxLoss
	MetricSFPTxFault         = metrics.MetricSFPTxFault
)
