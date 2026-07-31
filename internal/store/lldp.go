package store

import (
	"context"

	"github.com/jackc/pgx/v5"

	"meshcraft/internal/model"
)

// LLDP neighborhood in the database: two tables, no derivation.
//
// See migration 0028 for the schema and docs/01-Architecture/Voisinage LLDP.md for
// the survey. This file only writes OBSERVATIONS and draws no conclusions: matching
// a neighbor to a fleet device and deriving a link are pure functions in the `api`
// package, where they are testable without a database.

// --- Write: one snapshot REPLACES ---

// ReplaceLLDP writes one device's snapshot and fully replaces the previous one.
//
// # Full replacement is what makes the observation refutable
//
// Same rule as `tunnel.WatchConfig` and for the same reason: a delta assumes both
// sides share state and can diverge. Here the consequence would be worse than
// divergence — a neighbor removed from the cable would stay displayed indefinitely,
// and nothing in the protocol would say so. What the proxy no longer sends disappears,
// with no retraction message to invent.
//
// All in ONE transaction. Without it, delete and insert would be visible separately:
// a weathermap loaded between them would show a site with no LLDP links, and the
// operator would conclude a collection failure for a window of a few milliseconds
// that reopens every thirty seconds, per device.
//
// # `first_seen` is PRESERVED, and that is half the table's value
//
// The `upsert` does not touch that column: an already present neighbor keeps its
// first-seen date, answering "since when has this cable been there". A neighbor that
// disappeared was DELETED on the previous snapshot: if it returns, it starts over,
// which is correct — the cable moved, the old observation's age means nothing.
//
// # What this function does not do
//
// It checks NO permissions and judges no scope: that is the caller's job
// (`handleProxyLLDP`, which checks COLLECTOR authority before arriving here). A store
// that also decided who may write would express the same rule twice, and whichever
// diverged would open a hole without breaking compilation.
func (s *Store) ReplaceLLDP(ctx context.Context, self model.LLDPSelf,
	neighbors []model.LLDPNeighbor) error {
	var tx, err = s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if _, err = tx.Exec(ctx,
		`insert into lldp_self (device_id, site_id, chassis_id, sys_name, port_ids,
		                        served, seen_at)
		 values ($1::uuid, $2::uuid, $3, $4, $5, $6, now())
		 on conflict (device_id) do update set
		     site_id = excluded.site_id, chassis_id = excluded.chassis_id,
		     sys_name = excluded.sys_name, port_ids = excluded.port_ids,
		     served = excluded.served, seen_at = now()`,
		self.DeviceID, self.SiteID, self.ChassisID, self.SysName, self.PortIDs,
		self.Served); err != nil {
		return err
	}

	// DELETE first, bounded by what the snapshot contains. A full `delete` followed by
	// `insert` would have been one line simpler and would destroy `first_seen` every
	// cycle: the table would then answer "this neighbor appeared thirty seconds ago" for
	// a cable in place for six months.
	//
	// The condition is written as an N-tuple comparison against an array, to stay in ONE
	// query regardless of neighbor count — the equivalent loop would round-trip per
	// removed neighbor, and the vast majority of cycles remove none.
	var keys = make([][]any, 0, len(neighbors))
	var i int
	for i = range neighbors {
		keys = append(keys, []any{neighbors[i].LocalPort, neighbors[i].ChassisID,
			neighbors[i].PortID})
	}
	if len(keys) == 0 {
		// No neighbors: everything left for this device is gone. This case is ORDINARY,
		// not degraded — a switch whose neighbors are all SwOS, or whose only cable was
		// just unplugged — and it must empty the table, otherwise a dead link would stay
		// on screen forever.
		if _, err = tx.Exec(ctx,
			`delete from lldp_neighbors where device_id = $1::uuid`, self.DeviceID); err != nil {
			return err
		}
	} else {
		var ports = make([]string, len(keys))
		var chassis = make([]string, len(keys))
		var portIDs = make([]string, len(keys))
		for i = range keys {
			ports[i], _ = keys[i][0].(string)
			chassis[i], _ = keys[i][1].(string)
			portIDs[i], _ = keys[i][2].(string)
		}
		if _, err = tx.Exec(ctx,
			`delete from lldp_neighbors
			 where device_id = $1::uuid
			   and (local_port, chassis_id, port_id) not in (
			     select * from unnest($2::text[], $3::text[], $4::text[]))`,
			self.DeviceID, ports, chassis, portIDs); err != nil {
			return err
		}
		for i = range neighbors {
			var n = neighbors[i]
			if _, err = tx.Exec(ctx,
				`insert into lldp_neighbors (device_id, site_id, local_port, local_port_num,
				         chassis_id, port_id, port_desc, sys_name, sys_desc,
				         first_seen, last_seen)
				 values ($1::uuid, $2::uuid, $3, $4, $5, $6, $7, $8, $9, now(), now())
				 on conflict (device_id, local_port, chassis_id, port_id) do update set
				     site_id = excluded.site_id,
				     local_port_num = excluded.local_port_num,
				     port_desc = excluded.port_desc,
				     sys_name = excluded.sys_name,
				     sys_desc = excluded.sys_desc,
				     last_seen = now()`,
				n.DeviceID, n.SiteID, n.LocalPort, n.LocalPortNum, n.ChassisID,
				n.PortID, n.PortDesc, n.SysName, n.SysDesc); err != nil {
				return err
			}
		}
	}
	return tx.Commit(ctx)
}

// --- Read, by site ---

const lldpNeighborCols = `device_id::text, site_id::text, local_port, local_port_num,
	chassis_id, port_id, port_desc, sys_name, sys_desc, first_seen, last_seen`

func scanLLDPNeighbor(row pgx.Row) (model.LLDPNeighbor, error) {
	var n model.LLDPNeighbor
	var err = row.Scan(&n.DeviceID, &n.SiteID, &n.LocalPort, &n.LocalPortNum,
		&n.ChassisID, &n.PortID, &n.PortDesc, &n.SysName, &n.SysDesc,
		&n.FirstSeen, &n.LastSeen)
	return n, notFound(err)
}

// ListLLDPNeighbors returns a site's observations, SORTED.
//
// Sorted for the same reason as `ListLinks` and `facade.names`: callers derive links
// from this, and unstable order would make two identical responses differ — redrawing
// a weathermap that did not move on every refresh.
func (s *Store) ListLLDPNeighbors(ctx context.Context,
	siteID string) ([]model.LLDPNeighbor, error) {
	var rows, err = s.pool.Query(ctx,
		`select `+lldpNeighborCols+` from lldp_neighbors where site_id = $1::uuid
		 order by device_id, local_port, chassis_id, port_id`, siteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out = []model.LLDPNeighbor{}
	for rows.Next() {
		var n, scanErr = scanLLDPNeighbor(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// ListLLDPSelf returns the LLDP identity announced by a site's devices.
//
// Returned as a LIST rather than a map, although all callers build a map: that is
// the shape of a database read, and the key they build is not the device id but an
// identity token (chassis, port, name). Returning a map here would freeze the match
// key in the store, outside the pure function that decides and that tests exercise.
func (s *Store) ListLLDPSelf(ctx context.Context, siteID string) ([]model.LLDPSelf, error) {
	var rows, err = s.pool.Query(ctx,
		`select device_id::text, site_id::text, chassis_id, sys_name, port_ids,
		        served, seen_at
		 from lldp_self where site_id = $1::uuid order by device_id`, siteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out = []model.LLDPSelf{}
	for rows.Next() {
		var one model.LLDPSelf
		if err = rows.Scan(&one.DeviceID, &one.SiteID, &one.ChassisID, &one.SysName,
			&one.PortIDs, &one.Served, &one.SeenAt); err != nil {
			return nil, err
		}
		out = append(out, one)
	}
	return out, rows.Err()
}
