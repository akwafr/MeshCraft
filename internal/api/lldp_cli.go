package api

import (
	"context"
	"errors"
	"strings"

	"meshcraft/internal/driver"
	"meshcraft/internal/model"
	"meshcraft/internal/snmppoll"
)

// CLI-collected LLDP neighbourhood — the read-state leg of the topology feed.
//
// # The design decision, and why this pattern
//
// The topology's LLDP tables were fed by ONE path: the proxy's periodic SNMP
// walk (cmd/proxy/lldp.go → handleProxyLLDP). Measured on the FS S3400, that
// path is structurally blind: the firmware runs LLDP on the wire while its
// agent serves nothing under 1.0.8802 nor under its enterprise trees (walked
// live, 2026-08-05, closed negatively). The two candidate fixes were a
// proxy-side CLI collector or a controller-driven read; the proxy option
// would hand admin credentials to a component that deliberately never holds
// any and build a second scheduler, so this file takes the pattern the
// codebase already converged on: read the neighbour table IN THE READ-STATE
// GESTURE, on the admin session already open — exactly like management
// addressing, spanning tree, PoE and the faceplate (cf. handleReadDeviceState).
// The freshness trade-off is assumed and stated: CLI observations refresh at
// read-state, the SNMP path keeps its sixty-second cadence where it works
// (`wantedLLDPInterval`; the 30 s in the connectors is the channel FLOOR, which
// `effectiveInterval` only ever raises away from).
//
// # How the two paths coexist (migration 0037)
//
// Both write through store.ReplaceLLDP, whose whole point is that a snapshot
// REPLACES the previous one — which would make the poller's next EMPTY walk
// erase a CLI snapshot a minute after every read-state, on exactly the
// platforms this path exists to save. `lldp_self.source` names the writer,
// and `snmpSnapshotOverrides` arbitrates: an SNMP snapshot WITHOUT LLDP
// content yields to a CLI snapshot; one WITH content takes over — the agent
// serves the MIB, the unattended path is the better witness again. Between
// two writers with content, last-writer-wins, and that is fine: rows are
// written in the same vocabulary (facade port names, canonical chassis
// spelling), so the upsert key matches and `first_seen` survives the
// alternation. Staleness handling is unchanged from the SNMP path: nothing
// prunes either kind of row, `last_seen` stops moving and the screen judges
// freshness against the viewer's clock (cf. deriveLLDPLinks, "clockless").
//
// # Failure never interrupts read-state
//
// Same rule as STP / PoE / faceplate, enforced by shape: readLLDPNeighborhood
// returns nothing and only annotates `out` — ports and VLANs are already
// saved when it runs.

// lldpSourceSNMP / lldpSourceCLI: the two values of lldp_self.source.
const (
	lldpSourceSNMP = "snmp"
	lldpSourceCLI  = "cli"
)

// lldpCanonChassis renders a MAC-shaped chassis identifier in the SNMP path's
// spelling — "0x" + twelve lowercase hex digits — and leaves anything else
// as the device said it.
//
// Not cosmetic: `chassis_id` is part of lldp_neighbors' PRIMARY KEY, and the
// same neighbour arrives as "64:9D:99:22:7B:95" from the EdgeSwitch CLI and
// as "0x649d99227b95" from its SNMP agent (both measured, same chassis). Two
// spellings under the key would make each path's write DELETE the other's
// row and reset `first_seen` on every alternation between a poll cycle and a
// read-state — the column whose preservation is half the table's value. The
// conversion is lossless (the store's contract for chassis_id), and
// normalizeChassisID absorbs it identically at read time.
func lldpCanonChassis(raw string) string {
	var token = macIdentityToken(raw)
	if token == "" {
		return strings.TrimSpace(raw)
	}
	return "0x" + token
}

// lldpCLISnapshot translates a CLI neighbour read into the rows to store.
//
// PURE — this is the function tests exercise, on both connectors' real
// captured shapes. It funnels through lldpRowsFor, deliberately: local port
// resolution against the facade, and the drop-instead-of-guess rule for a
// port we cannot name, must stay ONE implementation for both collection
// paths, or the two would drift apart at the first facade change. Connector
// adapters return standard-model names, which pass `resolve` by exact match.
//
// What the CLI read cannot see, it must not refute:
//
//   - the device's own chassis identity is NOT in any CLI neighbour table.
//     It is taken from the MAC identities meshcraft already holds
//     (deviceKnownMACs — the spanning-tree bridge identifier refreshed
//     earlier in this same gesture, or the adoption-time MAC), written in
//     the canonical spelling. This is what gives the S3400 a REAL lldp_self
//     chassis, so buildLLDPIdentity resolves it natively instead of only
//     through the deviceKnownMACs fallback;
//   - sys_name is NEVER the meshcraft device name (the doctrine
//     lldp_self's schema states: `lab-crs326` announces "lan-r4"). The CLI
//     read does not see what the device announces, so it writes nothing —
//     and PRESERVES what a prior snapshot knew (chassis, sys_name,
//     port_ids): a read that never looked at those columns is not a witness
//     that they changed.
//
// Served carries the CLI's own witness — the protocol runs or it does not —
// which on the S3400 REPAIRS the poller's over-counting (its walk batch
// includes sysDescr, so an agent with no LLDP-MIB still read as "served").
func lldpCLISnapshot(d model.Device, prior model.LLDPSelf, hasPrior bool,
	obs driver.LLDPObservation) (model.LLDPSelf, []model.LLDPNeighbor, int) {
	var report = snmppoll.LLDPReport{DeviceID: d.ID, Served: obs.Enabled}
	if obs.Enabled {
		var raw string
		for _, raw = range deviceKnownMACs(d) {
			var token = macIdentityToken(raw)
			if token != "" {
				report.ChassisID = "0x" + token
				break
			}
		}
	}
	var i int
	for i = range obs.Neighbors {
		var n = obs.Neighbors[i]
		report.Neighbors = append(report.Neighbors, snmppoll.Neighbor{
			LocalPort: n.LocalPort,
			ChassisID: lldpCanonChassis(n.ChassisID),
			PortID:    n.PortID, PortDesc: n.PortDesc,
			SysName: n.SysName, SysDesc: n.SysDesc,
		})
	}
	var self, rows, dropped = lldpRowsFor(d, report)
	self.Source = lldpSourceCLI
	if hasPrior {
		if self.ChassisID == "" {
			self.ChassisID = prior.ChassisID
		}
		if self.SysName == "" {
			self.SysName = prior.SysName
		}
		if len(self.PortIDs) == 0 && len(prior.PortIDs) > 0 {
			self.PortIDs = prior.PortIDs
		}
	}
	return self, rows, dropped
}

// snmpSnapshotOverrides decides whether an incoming SNMP snapshot may replace
// what is stored. PURE — this is the arbitration migration 0037 exists for.
//
// The report's own `Served` flag is deliberately NOT consulted: it counts any
// answered line in the walk batch, sysDescr included, so an agent with no
// LLDP-MIB at all still reports Served=true (measured on the S3400 — the bug
// that gave it a served-but-empty row). Content is judged directly instead:
// a chassis, a system name, a port list or a neighbour can only exist if the
// agent actually serves something under 1.0.8802.
func snmpSnapshotOverrides(prior model.LLDPSelf, report snmppoll.LLDPReport) bool {
	if prior.Source != lldpSourceCLI {
		// SNMP has always owned this row: replace, as it always did. This
		// branch keeps every SNMP-only device's behaviour byte-identical,
		// including "LLDP went dark" emptying the tables.
		return true
	}
	return report.ChassisID != "" || report.SysName != "" ||
		len(report.PortIDs) > 0 || len(report.Neighbors) > 0
}

// readLLDPNeighborhood re-reads the LLDP neighbour table over the admin
// session and persists it — the read-state hook. Failure only annotates
// `out`; it never interrupts the read (ports and VLANs are already saved).
//
// Silent when the connector implements no CLI read: unlike spanning tree,
// where the admin session is the only witness, LLDP's PRIMARY path is the
// SNMP poller — most platforms are fully covered without this hook, and a
// note on every read-state would send operators hunting for a defect where
// there is a division of labour. (hpe/procurve adopted the adapter on
// 2026-08-10 and is now read here too; mikrotik/swos never will — SwOS does
// not speak 802.1AB at all.)
func (s *Server) readLLDPNeighborhood(ctx context.Context, d model.Device,
	adm driver.Admin, out map[string]any) {
	var reader, canRead = driver.AsLLDPReader(adm)
	if !canRead {
		return
	}
	var obs, err = reader.ReadLLDPObservation(ctx)
	if errors.Is(err, driver.ErrNotImplemented) {
		// The wire form of a failed assertion: silent, as above.
		return
	}
	if err != nil {
		out["lldp_note"] = "LLDP neighbours not read: " + err.Error()
		return
	}
	// Re-fetch the device: readSpanningTree persisted extra.stp_observed
	// EARLIER IN THIS SAME GESTURE, and the bridge identifier it stored is
	// the chassis identity the snapshot needs. The handler's own `d`
	// predates that write. Best-effort: on error the stale row still
	// carries the previous observation, or none — the deviceKnownMACs
	// fallback in buildLLDPIdentity keeps recognition alive either way.
	var fresh, gErr = s.Store.GetDevice(ctx, d.ID)
	if gErr == nil {
		d = fresh
	}
	var prior, priorErr = s.Store.GetLLDPSelf(ctx, d.ID)
	var self, rows, dropped = lldpCLISnapshot(d, prior, priorErr == nil, obs)
	if err = s.Store.ReplaceLLDP(ctx, self, rows); err != nil {
		out["lldp_note"] = "LLDP neighbours read but not saved: " + err.Error()
		return
	}
	out["lldp_neighbors"] = len(rows)
	if dropped > 0 {
		// Same accounting as the SNMP path: a port we cannot name is dropped,
		// and the count is the only thing that explains a missing link.
		out["lldp_dropped_unresolved_port"] = dropped
	}
	if !obs.Enabled {
		out["lldp_note"] = "LLDP is not enabled on this device — the neighbour" +
			" table cannot exist until it is"
	}
	if len(obs.Notes) > 0 {
		out["lldp_notes"] = obs.Notes
	}
}
