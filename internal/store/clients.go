package store

import (
	"context"
	"time"

	"meshcraft/internal/model"
)

// The Clients page's storage: mirrored forwarding tables (mac_sightings) and
// the per-MAC ledger (clients). See migration 0042 for why they are two tables
// with two lifetimes.

// ReplaceFDB stores one device's forwarding-table snapshot and keeps the
// site's ledger in step, in ONE transaction.
//
// Same shape as ReplaceLLDP and for the same reasons, restated only where the
// stakes differ:
//
//   - upsert-then-delete-the-rest, never delete+insert: first_seen is half of
//     what the page shows ("this machine has been here since June"), and a
//     rewrite every sixty seconds would reset it fleet-wide;
//   - an EMPTY snapshot from a served agent empties the mirror — a bridge that
//     no longer testifies to anything (rebooted, all ports down) must not keep
//     yesterday's clients on screen. The LEDGER keeps them: that is its job.
func (s *Store) ReplaceFDB(ctx context.Context, deviceID, siteID string,
	sightings []model.MACSighting) error {
	var tx, err = s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if len(sightings) == 0 {
		if _, err = tx.Exec(ctx,
			`delete from mac_sightings where device_id = $1::uuid`, deviceID); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}

	var ports = make([]string, len(sightings))
	var vlans = make([]int32, len(sightings))
	var macs = make([]string, len(sightings))
	var i int
	for i = range sightings {
		ports[i] = sightings[i].Port
		vlans[i] = int32(sightings[i].VLAN)
		macs[i] = sightings[i].MAC
	}
	// One statement for the whole snapshot: sixty seconds of cadence per device
	// makes per-row round trips a fleet-scale cost.
	if _, err = tx.Exec(ctx,
		`insert into mac_sightings (device_id, site_id, port, vlan, mac,
		                            first_seen, last_seen)
		 select $1::uuid, $2::uuid, u.port, u.vlan, u.mac, now(), now()
		 from unnest($3::text[], $4::integer[], $5::text[]) as u(port, vlan, mac)
		 on conflict (device_id, port, vlan, mac) do update set
		     site_id = excluded.site_id, last_seen = now()`,
		deviceID, siteID, ports, vlans, macs); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx,
		`delete from mac_sightings
		 where device_id = $1::uuid
		   and (port, vlan, mac) not in (
		     select * from unnest($2::text[], $3::integer[], $4::text[]))`,
		deviceID, ports, vlans, macs); err != nil {
		return err
	}
	// The ledger: every MAC this snapshot testifies to exists as a client of
	// the site, and its last_seen moves. Nothing is ever deleted here.
	//
	// DISTINCT, and it is load-bearing: the sightings legitimately repeat a MAC
	// (one address in four VLANs on one trunk — the lab's RB5009), and ON
	// CONFLICT refuses to touch the same row twice in one statement (SQLSTATE
	// 21000, hit on the first live snapshot).
	if _, err = tx.Exec(ctx,
		`insert into clients (site_id, mac, first_seen, last_seen)
		 select distinct $1::uuid, u.mac, now(), now() from unnest($2::text[]) as u(mac)
		 on conflict (site_id, mac) do update set last_seen = now()`,
		siteID, macs); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ListMACSightings returns a site's mirrored forwarding tables.
func (s *Store) ListMACSightings(ctx context.Context,
	siteID string) ([]model.MACSighting, error) {
	var rows, err = s.pool.Query(ctx,
		`select device_id::text, site_id::text, port, vlan, mac, first_seen, last_seen
		 from mac_sightings where site_id = $1::uuid
		 order by mac, device_id, port`, siteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.MACSighting
	for rows.Next() {
		var m model.MACSighting
		if err = rows.Scan(&m.DeviceID, &m.SiteID, &m.Port, &m.VLAN, &m.MAC,
			&m.FirstSeen, &m.LastSeen); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ListClients returns a site's ledger.
func (s *Store) ListClients(ctx context.Context, siteID string) ([]model.Client, error) {
	var rows, err = s.pool.Query(ctx,
		`select site_id::text, mac, first_seen, last_seen, rx_bytes, tx_bytes, bucket_at
		 from clients where site_id = $1::uuid order by mac`, siteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Client
	for rows.Next() {
		var c model.Client
		if err = rows.Scan(&c.SiteID, &c.MAC, &c.FirstSeen, &c.LastSeen,
			&c.RxBytes, &c.TxBytes, &c.BucketAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// PruneClients forgets every client of a site last seen before `before`, in
// ONE transaction, and reports how many rows each of the two tables lost.
//
// WHY THE LEDGER NEEDS A BROOM AT ALL. ReplaceFDB never deletes from
// `clients` — that is deliberate and it is what lets the page say "this
// machine has been here since June". The cost is that the ledger is
// append-only forever: every phone that ever associated to a guest VLAN with
// a randomized address is a permanent row, and on a site with visitors that
// is the majority of the list within a month. Nothing else will ever remove
// them, so the operator gets a broom with the window in their hands.
//
// BOTH TABLES, and in this order, because the page is their JOIN: a row is
// rendered from a SIGHTING (where the MAC is) enriched by the LEDGER (since
// when, how much). Deleting only the ledger would leave the row on screen
// with its history reset to zero — the worst of both answers — and deleting
// only the sightings would leave a "known but not located" count that never
// comes back down. A device still mirroring its forwarding table re-creates
// what it can still see on its next snapshot, which is the correct outcome:
// the broom forgets the ABSENT, and presence is re-proved every minute.
//
// `before` is computed by the caller and never defaulted here: an unbounded
// cutoff would empty the site, so the only cutoff this function knows is the
// one it was given.
func (s *Store) PruneClients(ctx context.Context, siteID string,
	before time.Time) (int64, int64, error) {
	var tx, err = s.pool.Begin(ctx)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var tag, execErr = tx.Exec(ctx,
		`delete from mac_sightings where site_id = $1::uuid and last_seen < $2`,
		siteID, before)
	if execErr != nil {
		return 0, 0, execErr
	}
	var sightings = tag.RowsAffected()

	if tag, execErr = tx.Exec(ctx,
		`delete from clients where site_id = $1::uuid and last_seen < $2`,
		siteID, before); execErr != nil {
		return 0, 0, execErr
	}
	var clients = tag.RowsAffected()

	if err = tx.Commit(ctx); err != nil {
		return 0, 0, err
	}
	return clients, sightings, nil
}

// AddClientTraffic adds one attribution round to the ledger, in one statement.
//
// The deltas come from the port counters' increase over the accounting window
// (cf. api's bucket job); adding rather than setting is what makes the bucket
// survive a controller restart — the ledger is the accumulator, not a cache of
// someone else's total.
func (s *Store) AddClientTraffic(ctx context.Context, siteID string,
	macs []string, rx, tx []int64, at time.Time) error {
	if len(macs) == 0 {
		return nil
	}
	var _, err = s.pool.Exec(ctx,
		`update clients c set
		     rx_bytes = c.rx_bytes + u.rx,
		     tx_bytes = c.tx_bytes + u.tx,
		     bucket_at = $5
		 from unnest($2::text[], $3::bigint[], $4::bigint[]) as u(mac, rx, tx)
		 where c.site_id = $1::uuid and c.mac = u.mac`,
		siteID, macs, rx, tx, at)
	return err
}
