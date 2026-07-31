package store

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"

	"meshcraft/internal/model"
)

// Coverage is aggregated as JSON to avoid N+1. See migration 0007: `global` and the
// site list are distinct notions; empty coverage does not mean "everywhere".
const proxyCols = `p.id::text, p.name, p.version, p.protocol, p.os, p.arch,
	p.global, p.internal, p.revoked, p.created_at, p.last_seen,
	p.auto_upgrade, p.pin_version, p.upgrade_requested,
	p.upgrade_state, p.upgrade_version, p.upgrade_error,
	p.upgrade_started_at, p.upgraded_at,
	p.migrate_url, p.migrated_at,
	coalesce((select json_agg(json_build_object('id', ps.site_id::text, 'name', s2.name)
	                          order by s2.name)
	          from proxy_sites ps join sites s2 on s2.id = ps.site_id
	          where ps.proxy_id = p.id), '[]')::text`

func scanProxy(row pgx.Row) (model.Proxy, error) {
	var p model.Proxy
	var sites string
	var err = row.Scan(&p.ID, &p.Name, &p.Version, &p.Protocol, &p.OS, &p.Arch,
		&p.Global, &p.Internal, &p.Revoked, &p.CreatedAt, &p.LastSeen,
		&p.AutoUpgrade, &p.PinVersion, &p.UpgradeRequested,
		&p.UpgradeState, &p.UpgradeVersion, &p.UpgradeError,
		&p.UpgradeStartedAt, &p.UpgradedAt,
		&p.MigrateURL, &p.MigratedAt, &sites)
	if err == nil {
		_ = json.Unmarshal([]byte(sites), &p.Sites)
	}
	return p, notFound(err)
}

// CreateProxy registers a proxy after enrollment, with the coverage from the consumed token.
func (s *Store) CreateProxy(ctx context.Context, name string, secretHash []byte,
	global bool, siteIDs []string) (model.Proxy, error) {
	var tx, err = s.pool.Begin(ctx)
	if err != nil {
		return model.Proxy{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var id string
	if err = tx.QueryRow(ctx,
		`insert into proxies (name, secret_hash, global) values ($1, $2, $3) returning id::text`,
		name, secretHash, global).Scan(&id); err != nil {
		return model.Proxy{}, err
	}
	if !global {
		if err = replaceProxySites(ctx, tx, id, siteIDs); err != nil {
			return model.Proxy{}, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return model.Proxy{}, err
	}
	return s.GetProxy(ctx, id)
}

func replaceProxySites(ctx context.Context, tx pgx.Tx, proxyID string, siteIDs []string) error {
	var _, err = tx.Exec(ctx, `delete from proxy_sites where proxy_id = $1::uuid`, proxyID)
	if err != nil {
		return err
	}
	var seen = map[string]bool{}
	var id string
	for _, id = range siteIDs {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		if _, err = tx.Exec(ctx,
			`insert into proxy_sites (proxy_id, site_id) values ($1::uuid, $2::uuid)`,
			proxyID, id); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) GetProxy(ctx context.Context, id string) (model.Proxy, error) {
	return scanProxy(s.pool.QueryRow(ctx,
		`select `+proxyCols+` from proxies p where p.id = $1::uuid`, id))
}

// ListProxies: visible inventory. all = everything (global-admin); otherwise instance
// proxies plus those covering at least one of the given sites.
func (s *Store) ListProxies(ctx context.Context, all bool, siteIDs []string) ([]model.Proxy, error) {
	var q = `select ` + proxyCols + ` from proxies p order by p.global desc, p.created_at`
	var args = []any{}
	if !all {
		q = `select ` + proxyCols + ` from proxies p
		     where p.global
		        or exists (select 1 from proxy_sites ps
		                   where ps.proxy_id = p.id and ps.site_id = any($1::uuid[]))
		     order by p.global desc, p.created_at`
		args = append(args, siteIDs)
	}
	var rows, err = s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out = []model.Proxy{}
	for rows.Next() {
		var p, scanErr = scanProxy(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ProxiesCoveringSite: proxies whose COVERAGE includes this site — therefore the only
// ones that can collect a device living there.
//
// The `where` clause is a word-for-word transcription of DevicesWatchedBy's
// (devices.go), with the site here coming from a parameter instead of the device row:
//
//	p.global or exists (select 1 from proxy_sites ps
//	                    where ps.proxy_id = p.id and ps.site_id = …)
//
// This identity is not stylistic vanity. Coverage is the only entry to a site's
// devices — a confidentiality boundary, cf. migration 0022 — and attachment to a
// non-covering proxy does NOT move collection: it removes it, without anything
// failing (TestRattachementNeDonnePasLaCouverture). This function is what makes that
// trap unreachable: it both offers only valid choices and rejects the others. The day
// the two expressions diverge, it would offer silently sterile attachments — they must
// move together.
//
// `revoked` is deliberately NOT filtered, while ListDiscoveryProxies excludes it. A
// revoked proxy covers no fewer sites: it just no longer connects. That is a liveness
// question, not coverage, and bringing it in here would diverge the predicate for a
// reason that does not concern it — especially since revocation can be undone and an
// attachment becomes valid again without rewriting anything. Display should say a proxy
// is revoked; model.Proxy carries it.
func (s *Store) ProxiesCoveringSite(ctx context.Context, siteID string) ([]model.Proxy, error) {
	var rows, err = s.pool.Query(ctx,
		`select `+proxyCols+` from proxies p
		 where p.global
		    or exists (select 1 from proxy_sites ps
		               where ps.proxy_id = p.id and ps.site_id = $1::uuid)
		 order by p.global desc, p.internal desc, p.name`, siteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out = []model.Proxy{}
	for rows.Next() {
		var p, scanErr = scanProxy(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ProxiesUsingDomain: proxies that declare at least one prefix in this address domain.
// Used for access control: attaching a prefix to a proxy's domain you do not administer
// merges its discoveries with a third party's (cf. api.checkDomainChoice).
func (s *Store) ProxiesUsingDomain(ctx context.Context, domainID string) ([]model.Proxy, error) {
	var rows, err = s.pool.Query(ctx,
		`select `+proxyCols+` from proxies p
		 where exists (select 1 from scan_prefixes sp
		               where sp.proxy_id = p.id and sp.address_domain_id = $1::uuid)
		    or p.address_domain_id = $1::uuid
		 order by p.name`, domainID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out = []model.Proxy{}
	for rows.Next() {
		var p, scanErr = scanProxy(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// SetProxySiteMember adds or removes ONE site from coverage. This is a site-admin
// operation: it only touches their site.
func (s *Store) SetProxySiteMember(ctx context.Context, proxyID, siteID string, member bool) error {
	var err error
	if member {
		_, err = s.pool.Exec(ctx,
			`insert into proxy_sites (proxy_id, site_id) values ($1::uuid, $2::uuid)
			 on conflict do nothing`, proxyID, siteID)
		return err
	}
	_, err = s.pool.Exec(ctx,
		`delete from proxy_sites where proxy_id = $1::uuid and site_id = $2::uuid`,
		proxyID, siteID)
	return err
}

// SetProxyCoverage redefines full coverage: reserved for global-admin, this promotes
// a restricted proxy to instance scope and the reverse.
func (s *Store) SetProxyCoverage(ctx context.Context, proxyID string, global bool, siteIDs []string) error {
	var tx, err = s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var res, execErr = tx.Exec(ctx,
		`update proxies set global = $2 where id = $1::uuid`, proxyID, global)
	if execErr != nil {
		return execErr
	}
	if res.RowsAffected() == 0 {
		return ErrNotFound
	}
	// An instance proxy has no per-site coverage: clear it so a ghost list does not
	// reappear if it becomes restricted again.
	if global {
		siteIDs = nil
	}
	if err = replaceProxySites(ctx, tx, proxyID, siteIDs); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// AuthProxy verifies a proxy identity (id + hashed secret) and returns any pending
// migration order.
func (s *Store) AuthProxy(ctx context.Context, id string, secretHash []byte) (migrateURL, migrateToken string, err error) {
	err = s.pool.QueryRow(ctx,
		`select migrate_url, migrate_token from proxies
		 where id = $1::uuid and secret_hash = $2 and not revoked`,
		id, secretHash).Scan(&migrateURL, &migrateToken)
	return migrateURL, migrateToken, notFound(err)
}

// TouchProxy records proxy presence and what it announces about itself. Empty fields
// are not overwritten: the tunnel, for example, announces neither arch nor interfaces,
// and must not erase what the heartbeat knows.
func (s *Store) TouchProxy(ctx context.Context, id, version string, protocol int,
	osName, arch string, ifaces json.RawMessage) error {
	var _, err = s.pool.Exec(ctx,
		`update proxies set last_seen = now(),
		   version  = coalesce(nullif($2, ''), version),
		   protocol = case when $3 > 0 then $3 else protocol end,
		   os       = coalesce(nullif($4, ''), os),
		   arch     = coalesce(nullif($5, ''), arch),
		   ifaces   = case when $6::jsonb = '[]'::jsonb then ifaces else $6::jsonb end
		 where id = $1::uuid`,
		id, version, protocol, osName, arch, jsonOr(ifaces, `[]`))
	return err
}

// --- Proxy upgrade ---

// SetProxyUpgradeOffered marks an upgrade in flight and consumes the manual request.
// Returns true if this is a FIRST offer for this version.
//
// `upgrade_started_at` is NOT refreshed when the offer is simply repeated: otherwise
// a proxy too old to understand the offer — which therefore never responds — would push
// its timestamp on every heartbeat and block the single progressive deployment slot
// forever. Observed in real testing.
func (s *Store) SetProxyUpgradeOffered(ctx context.Context, id, version string) (bool, error) {
	var first bool
	var err = s.pool.QueryRow(ctx,
		`update proxies set upgrade_state = 'offered', upgrade_version = $2,
		   upgrade_error = '', upgrade_requested = false,
		   upgrade_started_at = case
		     when upgrade_state = 'offered' and upgrade_version = $2 and upgrade_started_at is not null
		       then upgrade_started_at
		     else now() end
		 where id = $1::uuid
		 returning (upgrade_started_at >= now() - interval '2 seconds')`,
		id, version).Scan(&first)
	return first, notFound(err)
}

// MarkProxyUpgradeStalled: the offer went unanswered. Typical case is a proxy whose
// binary ignores the `upgrade` field (predating the feature). Stop offering, and say
// why rather than retry in a loop.
func (s *Store) MarkProxyUpgradeStalled(ctx context.Context, id, version, reason string) error {
	var _, err = s.pool.Exec(ctx,
		`update proxies set upgrade_state = 'stalled', upgrade_version = $2,
		   upgrade_error = $3
		 where id = $1::uuid`, id, version, reason)
	return err
}

// SetProxyUpgradeReport records what the proxy reports about its attempt.
func (s *Store) SetProxyUpgradeReport(ctx context.Context, id, state, version, errMsg string) error {
	var _, err = s.pool.Exec(ctx,
		`update proxies set upgrade_state = $2, upgrade_version = $3, upgrade_error = $4
		 where id = $1::uuid`, id, state, version, errMsg)
	return err
}

// MarkProxyUpgraded: the proxy announces the target version. The confirmation matters
// — not what the controller thought it triggered.
func (s *Store) MarkProxyUpgraded(ctx context.Context, id, version string) error {
	var _, err = s.pool.Exec(ctx,
		`update proxies set upgrade_state = 'up_to_date', upgrade_version = $2,
		   upgrade_error = '', upgraded_at = now(), upgrade_started_at = null
		 where id = $1::uuid`, id, version)
	return err
}

// CountUpgradesInFlight counts in-progress upgrades to this version, excluding one
// proxy. Used for progressive deployment: do not move an entire fleet to a binary you
// have not yet seen start once.
func (s *Store) CountUpgradesInFlight(ctx context.Context, exceptID, version string) (int, error) {
	var n int
	var err = s.pool.QueryRow(ctx,
		`select count(*) from proxies
		 where upgrade_state in ('offered', 'downloading') and upgrade_version = $1
		   and id <> $2::uuid
		   and upgrade_started_at > now() - interval '15 minutes'`, version, exceptID).Scan(&n)
	return n, err
}

// SetProxyUpgradePolicy: auto-upgrade, pin, and one-off request.
func (s *Store) SetProxyUpgradePolicy(ctx context.Context, id string, auto bool,
	pin string, requested bool) error {
	var tag, err = s.pool.Exec(ctx,
		`update proxies set auto_upgrade = $2, pin_version = $3,
		   upgrade_requested = $4,
		   -- A new instruction clears a previous failure: otherwise failure suspension
		   -- would last forever.
		   upgrade_state = case when upgrade_state = 'failed' then '' else upgrade_state end,
		   upgrade_error = case when upgrade_state = 'failed' then '' else upgrade_error end
		 where id = $1::uuid`, id, auto, pin, requested)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// SetProxyMigration sets (or cancels if url empty) a migration order.
func (s *Store) SetProxyMigration(ctx context.Context, id, url, token string) error {
	var tag, err = s.pool.Exec(ctx,
		`update proxies set migrate_url = $2, migrate_token = $3, migrated_at = null
		 where id = $1::uuid`, id, url, token)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// MarkProxyMigrated: the proxy confirmed attachment to the new instance. Clear the
// order secret and timestamp.
func (s *Store) MarkProxyMigrated(ctx context.Context, id string) error {
	var _, err = s.pool.Exec(ctx,
		`update proxies set migrated_at = now(), migrate_token = '', revoked = true
		 where id = $1::uuid`, id)
	return err
}

// RenameProxy changes the proxy label. The name is only a tracking tag: no uniqueness
// enforced — two points of presence can share a name on different sites, identity
// remains the UUID.
func (s *Store) RenameProxy(ctx context.Context, id, name string) error {
	var tag, err = s.pool.Exec(ctx,
		`update proxies set name = $2 where id = $1::uuid`, id, name)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

func (s *Store) SetProxyRevoked(ctx context.Context, id string, revoked bool) error {
	var tag, err = s.pool.Exec(ctx,
		`update proxies set revoked = $2 where id = $1::uuid`, id, revoked)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

func (s *Store) DeleteProxy(ctx context.Context, id string) error {
	var tag, err = s.pool.Exec(ctx, `delete from proxies where id = $1::uuid`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// UseEnrollToken consumes an enrollment token (single use) and returns the coverage it carries.
func (s *Store) UseEnrollToken(ctx context.Context, tokenHash []byte) (global bool, siteIDs []string, err error) {
	err = s.pool.QueryRow(ctx,
		`update enroll_tokens set used_at = now()
		 where token_hash = $1 and expires_at > now() and used_at is null
		 returning global, site_ids::text[]`, tokenHash).Scan(&global, &siteIDs)
	return global, siteIDs, notFound(err)
}
