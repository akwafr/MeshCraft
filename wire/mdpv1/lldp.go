package mdpv1

import "context"

// LLDP neighbour reading over the ADMIN session — the CLI fallback for
// platforms whose SNMP agent serves no LLDP-MIB.
//
// # Why this contract exists at all
//
// The topology's primary LLDP path is the proxy's periodic SNMP walk of the
// standard MIB (cmd/proxy/lldp.go). That path is right for what it is —
// unattended, periodic, no admin credentials involved — and it stays primary.
// But it has a measured blind spot: the FS S3400's BDCOM-derived firmware
// runs LLDP on the wire (`lldp run` configured, two fleet devices record its
// frames) while its agent serves NOTHING under `1.0.8802`, and its private
// enterprise trees (3320, 52642) answer empty too (walked live, 2026-08-05).
// On such a platform the ONLY way to read who the device sees is the CLI
// table its connector already parses.
//
// # Why the read rides the read-state gesture, not the proxy poller
//
// Both options were weighed (cf. TODO.md, "LLDP topology is fed ONLY by
// SNMP"). Teaching the proxy to open CLI sessions would be a new collection
// path with credential distribution the proxy deliberately does not have —
// the proxy speaks SNMP and ICMP, never admin credentials. The controller,
// by contrast, already opens an admin session on every read-state and
// already re-reads management addressing, spanning tree, PoE and the
// faceplate on it. One more `show` command on a session that is already open
// is the pattern this codebase converged on; a second scheduler would not be.
// The freshness trade-off is assumed: CLI observations refresh when an
// operator (or automation) triggers read-state, not every thirty seconds.
//
// # Why the method is NOT named ReadLLDPNeighbors
//
// Three connectors already carry a package-local `ReadLLDPNeighbors` with
// per-platform types and INCOMPATIBLE signatures — fs/s3400 returns
// (rows, enabled, error), ubiquiti/edgeswitch and hpe/procurve return
// (rows, error). Reusing that name for the driver-level interface would
// force a breaking rename on all three at once, including hpe/procurve,
// which is deliberately not touched by this change (it can adopt the
// adapter later, and its CLI read stays available for its own screens
// meanwhile). A distinct name lets each connector adopt the contract by
// ADDITION, the same property Admin.Transcript() was named for.

// LLDPNeighborObservation is ONE neighbour entry as a connector read it over
// the admin session, in the standard-model vocabulary.
type LLDPNeighborObservation struct {
	// LocalPort: STANDARD-MODEL port name ("GE18", "XG1"), already translated
	// by the connector's own naming (StandardPortName). Not the platform
	// spelling: the API layer resolves this against the device facade exactly
	// like the SNMP path does, and a standard name passes that resolution by
	// exact match.
	LocalPort string
	// ChassisID / PortID / PortDesc / SysName / SysDesc: what the neighbour
	// said about itself, AS the platform displays it. Any of them may be
	// empty — the S3400's summary table serves no chassis column at all, and
	// its Device-ID column can be blank (captured) — and the API layer's
	// recognition paths (chassis, then port id, then system name) are built
	// for exactly that.
	ChassisID string
	PortID    string
	PortDesc  string
	SysName   string
	SysDesc   string
}

// LLDPObservation is a full CLI neighbour read: the table, and whether the
// protocol is running at all.
type LLDPObservation struct {
	// Enabled: LLDP is running on the device. False is a CONSTAT, not a
	// failure — the S3400 answers "LLDP is not enabled" to the show
	// command, and "protocol off" must read differently from "zero
	// neighbours" on every screen downstream (same doctrine as
	// snmppoll.LLDPReport.Served).
	Enabled bool
	// Neighbors: the table. Empty with Enabled true means "LLDP runs,
	// nobody in front" — an ordinary state, not a degraded one.
	Neighbors []LLDPNeighborObservation
	// Notes: phrases for the operator, no decisions to draw (same contract
	// as SpanningTreeState.Notes).
	Notes []string
}

// LLDPReader reads the LLDP neighbour table over the admin session.
//
// OPTIONAL, like SpanningTreeReader and for the same reason: platforms whose
// SNMP agent serves the LLDP-MIB do not need it (the poller already covers
// them), and platforms that do not speak 802.1AB at all (SwOS) cannot have
// it. Callers use [AsLLDPReader] and treat absence as the ordinary case.
type LLDPReader interface {
	ReadLLDPObservation(ctx context.Context) (LLDPObservation, error)
}

// AsLLDPReader returns the reader when Admin implements it.
func AsLLDPReader(adm Admin) (LLDPReader, bool) {
	var r, ok = adm.(LLDPReader)
	return r, ok
}
