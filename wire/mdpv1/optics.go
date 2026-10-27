package mdpv1

import "context"

// Optical transceiver diagnostics (DDM/DOM) over the ADMIN session — the CLI
// fallback for platforms whose SNMP agent serves no optical readings.
//
// # Why this contract exists at all
//
// The periodic collector speaks SNMP, and on platforms whose agent serves
// optical power through a MIB that is where the readings belong (RouterOS
// reads them through its own API channel; cf. mikrotik/routeros/telemetry.go).
// The FS S3400 is the measured blind spot this file exists for: "show
// interface tg0/N" serves a full DDM block — TX/RX power, module
// temperature, supply voltage, bias current — while the SNMP enterprise trees
// of the same firmware answer EMPTY (walked live, 2026-08-05; cf. the
// connector's MonitoringCapabilities note on "optical_power"). The CLI is the
// only place this firmware will ever serve the readings, and the admin
// session is the only channel that reads the CLI.
//
// # Why the read rides the read-state gesture, not the proxy poller
//
// Same arbitration as [LLDPReader], decided there first: the proxy speaks
// SNMP and ICMP, never admin credentials, and the controller already opens an
// admin session on every read-state on which addressing, spanning tree, PoE,
// LLDP and the faceplate are re-read. One more show command on that session
// is the converged pattern; a second scheduler would not be. The freshness
// trade-off is the same and assumed: DDM observations refresh when read-state
// runs, not every thirty seconds — per-port HISTORY (power over time) belongs
// to the metrics pipeline the day a collector can take a CLI-sourced sample
// (cf. TODO.md).

// OpticsPortState is what a device reports about ONE occupied optical cage:
// the module's identity and its DDM readings, RIGHT NOW. Read-only
// observation, same standing as [PoEPortState]: it never feeds VLAN-style
// drift, and it is refreshed at read-state cadence, not polled.
type OpticsPortState struct {
	// Port: facade name ("XG1"), same vocabulary as State.Ports.
	Port string `json:"port"`
	// Vendor / PartNumber / Serial: what the module says about itself, as the
	// platform displays it ("FS", "SFP-10G-BX", "G2210006755"). Any may be
	// empty — presence of the PORT in the list already means a module was
	// seen in the cage.
	Vendor     string `json:"vendor,omitempty"`
	PartNumber string `json:"part_number,omitempty"`
	Serial     string `json:"serial,omitempty"`
	// DDM: the module declares diagnostics support. A module can be present
	// and answer "DDM:NO" — it is still listed, with every reading nil,
	// because "cage occupied by a module that cannot measure" and "empty
	// cage" must not collapse into one silence.
	DDM bool `json:"ddm"`
	// The readings, nil when the module does not serve them. POINTERS, not
	// zero values: 0.0 dBm is a legal transmit power and a legal receive
	// power, and an omitted zero would erase a genuine reading.
	TxPowerDBm    *float64 `json:"tx_power_dbm,omitempty"`
	RxPowerDBm    *float64 `json:"rx_power_dbm,omitempty"`
	TemperatureC  *float64 `json:"temperature_c,omitempty"`
	VoltageV      *float64 `json:"voltage_v,omitempty"`
	BiasCurrentMA *float64 `json:"bias_current_ma,omitempty"`
}

// OpticsState is what a device reports about its optical cages.
//
// Ports lists ONLY the cages a module was observed in: an empty cage serves
// no transceiver block at all on the platform this was captured on, and
// listing it with all-nil readings would be indistinguishable from a module
// that measures nothing. Empty Ports with no Notes therefore reads as "no
// module anywhere", not as a failed read — a failed read is an error from
// [OpticsReader.ReadOptics] itself.
type OpticsState struct {
	Ports []OpticsPortState `json:"ports,omitempty"`
	// Notes: phrases for the operator, no decisions to draw (same contract
	// as SpanningTreeState.Notes).
	Notes []string `json:"notes,omitempty"`
}

// OpticsReader reads transceiver diagnostics over the admin session.
//
// OPTIONAL, like [PoEReader] and [LLDPReader] and for the same reason:
// platforms with no optical cage cannot have it, and platforms whose SNMP
// agent serves the readings do not need it. Callers use [AsOpticsReader] and
// treat absence as the ordinary case.
type OpticsReader interface {
	ReadOptics(ctx context.Context) (OpticsState, error)
}

// AsOpticsReader returns the reader when Admin implements it.
func AsOpticsReader(adm Admin) (OpticsReader, bool) {
	var r, ok = adm.(OpticsReader)
	return r, ok
}
