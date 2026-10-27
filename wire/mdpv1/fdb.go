package mdpv1

import "context"

// Forwarding-table reading over the ADMIN session — the CLI leg of the
// Clients page, for platforms whose collector cannot serve the table.
//
// The pattern and its whole justification are LLDPReader's (lldp.go): the
// PRIMARY path is the proxy's periodic walk of the bridge MIBs
// (internal/snmppoll/fdb.go), which covers every v2c platform of the
// reference fleet — measured, per-connector, before each declared the
// channel. What it cannot cover is the platform whose collection does not
// speak SNMP at all: the Arista is carried on gNMI, and its EOS train
// (4.28.13.1M, probed 2026-08-13) rejects the OpenConfig FDB subtree
// (`failed to access node "fdb" in node "network-instance"`). The only way to
// read who that switch has learned is the CLI table its connector already
// knows how to run — so the read rides the read-state gesture, on the session
// already open, with the freshness trade-off that pattern owns and states.

// FDBEntryObservation is one learned MAC as a connector read it.
type FDBEntryObservation struct {
	// MAC in canonical spelling — lowercase colon-separated hex. The connector
	// converts its platform's spelling ("04f4.1c1d.7458" on EOS): the parser is
	// where the platform form is known, and the store key must have exactly one
	// spelling per address.
	MAC string
	// VLAN the address was learned in; 0 when the platform does not say.
	VLAN int
	// LocalPort: STANDARD-MODEL port name ("ET48"), already translated by the
	// connector's own naming (StandardPortName) — same contract as
	// LLDPNeighborObservation.LocalPort, and it passes facade resolution by
	// exact match.
	LocalPort string
}

// FDBObservation is a full CLI forwarding-table read.
type FDBObservation struct {
	// Entries: dynamic entries only — a static/CPU entry is the switch itself,
	// not a machine behind a port.
	Entries []FDBEntryObservation
	// Notes: phrases for the operator, no decisions to draw.
	Notes []string
}

// FDBReader reads the forwarding table over the admin session. OPTIONAL, like
// LLDPReader and for the same reason.
type FDBReader interface {
	ReadFDBObservation(ctx context.Context) (FDBObservation, error)
}

// AsFDBReader returns the reader when Admin implements it.
func AsFDBReader(adm Admin) (FDBReader, bool) {
	var r, ok = adm.(FDBReader)
	return r, ok
}
