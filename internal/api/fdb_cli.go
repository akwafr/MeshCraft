package api

import (
	"context"
	"errors"

	"meshcraft/internal/driver"
	"meshcraft/internal/model"
	"meshcraft/internal/snmppoll"
)

// CLI-collected forwarding table — the read-state leg of the Clients feed.
//
// The design is lldp_cli.go's, on the same reasoning (stated there, not
// repeated): the primary path is the proxy's periodic bridge-MIB walk, and
// this hook exists for exactly one measured blind spot — the Arista, carried
// on gNMI, whose EOS train rejects the OpenConfig FDB subtree (probed
// 2026-08-13; see internal/driver/arista/eos/fdb.go). Freshness follows
// read-state, and the page shows last_seen, so the staleness is legible
// rather than hidden.
//
// One arbitration DIFFERENCE from LLDP, worth its own paragraph: there is no
// source column and no override rule, because the two paths cannot fight —
// a device is either polled over SNMP (its connector declares the `fdb`
// channel; the CLI connectors do not) or read here (EOS, which no SNMP
// snapshot will ever reach). The first platform that grows both paths gets
// to write the arbitration, with a migration like 0037, not before.

// fdbCLISnapshot translates a CLI read into sightings, through the same
// facade resolution as the proxy path (fdbRowsFor) so the two stay one rule.
func fdbCLISnapshot(d model.Device, obs driver.FDBObservation) ([]model.MACSighting, int) {
	var report = snmppoll.FDBReport{DeviceID: d.ID, Served: true}
	var i int
	for i = range obs.Entries {
		report.Entries = append(report.Entries, snmppoll.FDBEntry{
			MAC: obs.Entries[i].MAC, VLAN: obs.Entries[i].VLAN,
			Port: obs.Entries[i].LocalPort,
		})
	}
	return fdbRowsFor(d, report)
}

// readFDBTable re-reads the forwarding table over the admin session and
// persists it — the read-state hook. Failure only annotates `out`; silent on
// connectors that implement no CLI read (the ordinary case: the poller covers
// them).
func (s *Server) readFDBTable(ctx context.Context, d model.Device,
	adm driver.Admin, out map[string]any) {
	var reader, canRead = driver.AsFDBReader(adm)
	if !canRead {
		return
	}
	var obs, err = reader.ReadFDBObservation(ctx)
	if errors.Is(err, driver.ErrNotImplemented) {
		// The wire form of a failed assertion: silent, as above.
		return
	}
	if err != nil {
		out["fdb_note"] = "forwarding table not read: " + err.Error()
		return
	}
	var sightings, dropped = fdbCLISnapshot(d, obs)
	if err = s.Store.ReplaceFDB(ctx, d.ID, d.SiteID, sightings); err != nil {
		out["fdb_note"] = "forwarding table read but not saved: " + err.Error()
		return
	}
	out["fdb_entries"] = len(sightings)
	if dropped > 0 {
		out["fdb_dropped_unresolved_port"] = dropped
	}
	if len(obs.Notes) > 0 {
		out["fdb_notes"] = obs.Notes
	}
}
