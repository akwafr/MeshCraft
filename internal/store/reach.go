package store

import (
	"context"
	"time"

	"meshcraft/internal/model"
)

// Per-proxy reachability, and the identity proof that decides whose verdict counts.
//
// See migration 0035 for WHY this table exists. In short: `devices.status` used to
// be written by whichever proxy spoke last, so a proxy that could not reach a
// device overwrote the verdict of one that could — observed on 2026-08-02, four
// live devices marked down by a proxy on a different network 3.7 seconds after it
// first connected.
//
// This file only stores and returns the observations. The RULE that turns them
// into one status lives in internal/api/reachability.go, as a pure function, so it
// can be tested without a database.

// SetProxyReach records ONE proxy's verdict about one device.
//
// Upsert rather than insert-or-update in two statements: a proxy re-announcing on
// reconnect races with its own steady-state polling, and two statements would let
// the older of the two land last.
//
// `confirmed_at` and `confirmed_by` are deliberately NOT touched here. They are
// written only by [Store.ConfirmProxyIdentity], and a reachability update that
// reset them would erase the proof at the very moment it matters most: a device
// going down would demote the only proxy entitled to say so.
func (s *Store) SetProxyReach(ctx context.Context, deviceID, proxyID string,
	reachable bool, misses, latencyMS int) error {
	var _, err = s.pool.Exec(ctx,
		`insert into device_proxy_reach
		     (device_id, proxy_id, reachable, reachable_at, misses, latency_ms)
		 values ($1::uuid, $2::uuid, $3, now(), $4, $5)
		 on conflict (device_id, proxy_id) do update
		     set reachable = excluded.reachable,
		         reachable_at = now(),
		         misses = excluded.misses,
		         latency_ms = excluded.latency_ms`,
		deviceID, proxyID, reachable, misses, latencyMS)
	return err
}

// ConfirmProxyIdentity records that this proxy PROVED which device it is talking
// to, and what matched.
//
// Called from the SNMP path, which is already restricted to each device's
// declared collector (cf. api/proxyscope.go, authorityPoll) — so a proxy cannot
// confirm a device it was never entrusted to poll.
//
// Idempotent and cheap: it runs on every successful poll cycle, refreshing the
// date. The row is created if the proxy has not yet reported a ping verdict,
// because SNMP polling and ICMP watch do not start in a fixed order.
func (s *Store) ConfirmProxyIdentity(ctx context.Context, deviceID, proxyID,
	by string) error {
	var _, err = s.pool.Exec(ctx,
		`insert into device_proxy_reach
		     (device_id, proxy_id, confirmed_at, confirmed_by)
		 values ($1::uuid, $2::uuid, now(), $3)
		 on conflict (device_id, proxy_id) do update
		     set confirmed_at = now(), confirmed_by = excluded.confirmed_by`,
		deviceID, proxyID, by)
	return err
}

// ProxyReachFor returns every proxy observation about one device, newest verdict
// first, with the proxy NAME joined in.
//
// The name is not a convenience: the status note names the proxy that established
// the verdict (that is the whole point of carrying it), and a UUID in an
// operator-facing note helps nobody.
//
// Rows whose proxy has been revoked are excluded. A revoked proxy's opinion must
// stop counting the moment it is revoked, exactly as its facts stop being
// accepted — leaving it in would let a decommissioned machine hold a device down.
func (s *Store) ProxyReachFor(ctx context.Context, deviceID string) ([]model.ProxyReach, error) {
	var rows, err = s.pool.Query(ctx,
		`select r.proxy_id, coalesce(p.name, ''), r.reachable, r.reachable_at,
		        r.misses, r.latency_ms, r.confirmed_at, r.confirmed_by
		   from device_proxy_reach r
		   join proxies p on p.id = r.proxy_id
		  where r.device_id = $1::uuid and not p.revoked
		  order by r.confirmed_at desc nulls last, r.reachable_at desc nulls last`,
		deviceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out = []model.ProxyReach{}
	for rows.Next() {
		var pr model.ProxyReach
		var at, confirmedAt *time.Time
		err = rows.Scan(&pr.ProxyID, &pr.ProxyName, &pr.Reachable, &at,
			&pr.Misses, &pr.LatencyMS, &confirmedAt, &pr.ConfirmedBy)
		if err != nil {
			return nil, err
		}
		if at != nil {
			pr.ReachableAt = *at
		}
		pr.ConfirmedAt = confirmedAt
		out = append(out, pr)
	}
	return out, rows.Err()
}

// ForgetProxyReach drops one proxy's observations about devices it no longer
// watches.
//
// Called when a watch configuration is pushed, with the set the proxy KEEPS. A
// stale verdict is not harmless: a proxy detached from a site would otherwise go
// on holding a device down forever with an opinion it can no longer form, and
// nothing would ever overwrite it — the row is only refreshed by a proxy that
// still watches the device.
//
// `keep` empty means the proxy watches nothing: every row of its is removed.
func (s *Store) ForgetProxyReach(ctx context.Context, proxyID string,
	keep []string) ([]string, error) {
	// A NIL SLICE IS NOT AN EMPTY ONE HERE, and the difference is silent.
	// pgx encodes nil as SQL NULL, and `device_id = any(NULL)` is NULL, so
	// `not (...)` is NULL and the delete matches NOTHING — the exact opposite of
	// what "this proxy watches nothing" must do. An empty non-nil slice encodes
	// as '{}', which compares false and deletes every row as intended.
	if keep == nil {
		keep = []string{}
	}
	var rows, err = s.pool.Query(ctx,
		`delete from device_proxy_reach
		  where proxy_id = $1::uuid and not (device_id = any($2::uuid[]))
		 returning device_id`,
		proxyID, keep)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out = []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
