package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"meshcraft/internal/model"
	"meshcraft/internal/netscope"
)

// LocalProxyID is the fixed id of the "Local" proxy: the meshcraft instance itself,
// which reaches and scans networks within its scope directly. Created by migration 0005
// and not deletable from the UI.
const LocalProxyID = "00000000-0000-0000-0000-000000000001"

// --- Instance encryption key ---

// InstanceSecret reads an instance secret, creating it if missing. The write is
// idempotent under concurrency (on conflict do nothing then re-read): two simultaneous
// startups do not produce two keys.
func (s *Store) InstanceSecret(ctx context.Context, name string, gen func() ([]byte, error)) ([]byte, error) {
	var value []byte
	var err = s.pool.QueryRow(ctx,
		`select value from instance_secrets where name = $1`, name).Scan(&value)
	if err == nil {
		return value, nil
	}
	if notFound(err) != ErrNotFound {
		return nil, err
	}
	var fresh []byte
	fresh, err = gen()
	if err != nil {
		return nil, err
	}
	if _, err = s.pool.Exec(ctx,
		`insert into instance_secrets (name, value) values ($1, $2)
		 on conflict (name) do nothing`, name, fresh); err != nil {
		return nil, err
	}
	err = s.pool.QueryRow(ctx,
		`select value from instance_secrets where name = $1`, name).Scan(&value)
	return value, err
}

// --- Known standard credentials ---

const knownCredCols = `id::text, name, kind, username, priority, enabled,
	octet_length(secret_enc) > 0, created_at`

func scanKnownCred(row pgx.Row) (model.KnownCredential, error) {
	var c model.KnownCredential
	var err = row.Scan(&c.ID, &c.Name, &c.Kind, &c.Username, &c.Priority,
		&c.Enabled, &c.HasSecret, &c.CreatedAt)
	return c, notFound(err)
}

func (s *Store) ListKnownCredentials(ctx context.Context) ([]model.KnownCredential, error) {
	var rows, err = s.pool.Query(ctx,
		`select `+knownCredCols+` from known_credentials order by priority, created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out = []model.KnownCredential{}
	for rows.Next() {
		var c, scanErr = scanKnownCred(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) CreateKnownCredential(ctx context.Context, name, kind, username string, secretEnc []byte, priority int, enabled bool) (model.KnownCredential, error) {
	return scanKnownCred(s.pool.QueryRow(ctx,
		`insert into known_credentials (name, kind, username, secret_enc, priority, enabled)
		 values ($1, $2, $3, $4, $5, $6) returning `+knownCredCols,
		name, kind, username, secretEnc, priority, enabled))
}

// UpdateKnownCredential: secretEnc nil keeps the existing secret.
func (s *Store) UpdateKnownCredential(ctx context.Context, id, name, kind, username string, secretEnc []byte, priority int, enabled bool) (model.KnownCredential, error) {
	if secretEnc == nil {
		return scanKnownCred(s.pool.QueryRow(ctx,
			`update known_credentials set name = $2, kind = $3, username = $4,
			        priority = $5, enabled = $6
			 where id = $1::uuid returning `+knownCredCols,
			id, name, kind, username, priority, enabled))
	}
	return scanKnownCred(s.pool.QueryRow(ctx,
		`update known_credentials set name = $2, kind = $3, username = $4,
		        secret_enc = $5, priority = $6, enabled = $7
		 where id = $1::uuid returning `+knownCredCols,
		id, name, kind, username, secretEnc, priority, enabled))
}

func (s *Store) DeleteKnownCredential(ctx context.Context, id string) error {
	var tag, err = s.pool.Exec(ctx, `delete from known_credentials where id = $1::uuid`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// SecretOfCredential returns a credential's ciphertext (internal discovery engine use;
// never exposed by the API).
type EncCredential struct {
	ID        string
	Name      string
	Kind      string
	Username  string
	SecretEnc []byte
}

// CredentialSecrets returns the ciphertext of ALL credentials, active or not.
//
// Only answers one question, and the admin screen is the only place it can arise:
// "does the current instance key still open this secret?" A secret that becomes
// unreadable exists nowhere else — discovery's silent decryption just removes it from
// tries, without telling anyone.
//
// Ciphertext never leaves this path, and plaintext even less: the caller opens, keeps the
// boolean, discards the rest.
func (s *Store) CredentialSecrets(ctx context.Context) ([]EncCredential, error) {
	var rows, err = s.pool.Query(ctx,
		`select id::text, name, kind, username, secret_enc from known_credentials
		 order by priority, created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out = []EncCredential{}
	for rows.Next() {
		var c EncCredential
		if err = rows.Scan(&c.ID, &c.Name, &c.Kind, &c.Username, &c.SecretEnc); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// EnabledCredentials returns active credentials in try order.
func (s *Store) EnabledCredentials(ctx context.Context) ([]EncCredential, error) {
	var rows, err = s.pool.Query(ctx,
		`select id::text, name, kind, username, secret_enc from known_credentials
		 where enabled order by priority, created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out = []EncCredential{}
	for rows.Next() {
		var c EncCredential
		if err = rows.Scan(&c.ID, &c.Name, &c.Kind, &c.Username, &c.SecretEnc); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// --- Address domains ---
//
// A domain is the space where one IP address designates one machine (cf. migration 0020
// and internal/netscope). It is declared by the operator: nothing here tries to guess it.

const addressDomainCols = `ad.id::text, ad.name, ad.note, ad.created_at,
	(select count(*) from scan_prefixes sp where sp.address_domain_id = ad.id),
	coalesce((select json_agg(distinct p.name)
	          from scan_prefixes sp2 join proxies p on p.id = sp2.proxy_id
	          where sp2.address_domain_id = ad.id), '[]')::text`

func scanAddressDomain(row pgx.Row) (model.AddressDomain, error) {
	var d model.AddressDomain
	var proxies string
	var err = row.Scan(&d.ID, &d.Name, &d.Note, &d.CreatedAt, &d.Prefixes, &proxies)
	if err == nil {
		d.Proxies = []string{}
		_ = json.Unmarshal([]byte(proxies), &d.Proxies)
	}
	return d, notFound(err)
}

func (s *Store) ListAddressDomains(ctx context.Context) ([]model.AddressDomain, error) {
	var rows, err = s.pool.Query(ctx,
		`select `+addressDomainCols+` from address_domains ad order by ad.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out = []model.AddressDomain{}
	for rows.Next() {
		var d, scanErr = scanAddressDomain(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) GetAddressDomain(ctx context.Context, id string) (model.AddressDomain, error) {
	return scanAddressDomain(s.pool.QueryRow(ctx,
		`select `+addressDomainCols+` from address_domains ad where ad.id = $1::uuid`, id))
}

func (s *Store) CreateAddressDomain(ctx context.Context, name, note string) (model.AddressDomain, error) {
	var id string
	var err = s.pool.QueryRow(ctx,
		`insert into address_domains (name, note) values ($1, $2) returning id::text`,
		name, note).Scan(&id)
	if err != nil {
		return model.AddressDomain{}, err
	}
	return s.GetAddressDomain(ctx, id)
}

func (s *Store) UpdateAddressDomain(ctx context.Context, id, name, note string) (model.AddressDomain, error) {
	var tag, err = s.pool.Exec(ctx,
		`update address_domains set name = $2, note = $3 where id = $1::uuid`, id, name, note)
	if err != nil {
		return model.AddressDomain{}, err
	}
	if tag.RowsAffected() == 0 {
		return model.AddressDomain{}, ErrNotFound
	}
	return s.GetAddressDomain(ctx, id)
}

// ErrDomainInUse: the domain still carries prefixes. Deleting it would silently change
// the identity of all candidates ranked under it — so we refuse and ask the operator to
// move prefixes first.
var ErrDomainInUse = errors.New("this address domain still has scan prefixes: " +
	"move them to another domain before deleting it")

func (s *Store) DeleteAddressDomain(ctx context.Context, id string) error {
	var used int
	var err = s.pool.QueryRow(ctx,
		`select count(*) from scan_prefixes where address_domain_id = $1::uuid`, id).Scan(&used)
	if err != nil {
		return err
	}
	if used > 0 {
		return ErrDomainInUse
	}
	var tag pgconn.CommandTag
	tag, err = s.pool.Exec(ctx, `delete from address_domains where id = $1::uuid`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// HomeDomain returns a proxy's home domain, creating it if none yet — the case for every
// proxy enrolled after migration 0020.
//
// Creating ON DEMAND rather than at enrollment avoids touching both proxy creation paths,
// and guarantees no proxy can end up without a domain: an observation without a domain
// would lose its identity key and merge with all others sharing its address.
func (s *Store) HomeDomain(ctx context.Context, proxyID string) (string, error) {
	if proxyID == "" {
		proxyID = LocalProxyID
	}
	var id *string
	var name string
	var err = s.pool.QueryRow(ctx,
		`select address_domain_id::text, name from proxies where id = $1::uuid`,
		proxyID).Scan(&id, &name)
	if err != nil {
		return "", notFound(err)
	}
	if id != nil && *id != "" {
		return *id, nil
	}
	var fresh string
	if err = s.pool.QueryRow(ctx,
		`insert into address_domains (name, note) values ($1, $2) returning id::text`,
		"Network of "+name,
		"Proxy home domain: what it observes outside declared prefixes.").Scan(&fresh); err != nil {
		return "", err
	}
	// `is null` in the clause: two simultaneous enrollments must not overwrite the domain
	// the other just set.
	if _, err = s.pool.Exec(ctx,
		`update proxies set address_domain_id = $2::uuid
		 where id = $1::uuid and address_domain_id is null`, proxyID, fresh); err != nil {
		return "", err
	}
	return s.HomeDomain(ctx, proxyID)
}

// ProxyDomains: all domains a proxy declares — its own, plus those of its scan prefixes.
// That set defines which networks this proxy may legitimately answer for
// (cf. netscope.SelectWatchTargets).
func (s *Store) ProxyDomains(ctx context.Context, proxyID string) (map[string]bool, error) {
	var out = map[string]bool{}
	var home, err = s.HomeDomain(ctx, proxyID)
	if err != nil {
		return nil, err
	}
	out[home] = true
	var rows pgx.Rows
	rows, err = s.pool.Query(ctx,
		`select distinct address_domain_id::text from scan_prefixes where proxy_id = $1::uuid`,
		proxyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// ScopedPrefixesFor returns a proxy's prefixes in the form netscope.DomainOf expects.
// DISABLED prefixes are included: disabling a scan does not make the network disappear,
// and excluding them would shift their candidates to another domain — duplicating them.
func (s *Store) ScopedPrefixesFor(ctx context.Context, proxyID string) ([]netscope.Prefix, error) {
	var rows, err = s.pool.Query(ctx,
		`select prefix, address_domain_id::text, enabled from scan_prefixes
		 where proxy_id = $1::uuid`, proxyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out = []netscope.Prefix{}
	for rows.Next() {
		var p netscope.Prefix
		if err = rows.Scan(&p.Prefix, &p.DomainID, &p.Enabled); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// DomainForAddress answers the only question that matters when recording an observation:
// which network does this address live in, as seen by this proxy?
func (s *Store) DomainForAddress(ctx context.Context, proxyID, ip string) (string, error) {
	if proxyID == "" {
		proxyID = LocalProxyID
	}
	var home, err = s.HomeDomain(ctx, proxyID)
	if err != nil {
		return "", err
	}
	var prefixes []netscope.Prefix
	prefixes, err = s.ScopedPrefixesFor(ctx, proxyID)
	if err != nil {
		return "", err
	}
	return netscope.DomainOf(prefixes, home, ip), nil
}

// --- Scan prefixes ---

const scanPrefixCols = `sp.id::text, sp.proxy_id::text, p.name,
	sp.address_domain_id::text, ad.name, sp.prefix, sp.enabled,
	sp.note, sp.last_run_at, sp.last_result, sp.created_at`

const scanPrefixFrom = ` from scan_prefixes sp
	join proxies p on p.id = sp.proxy_id
	join address_domains ad on ad.id = sp.address_domain_id`

func scanScanPrefix(row pgx.Row) (model.ScanPrefix, error) {
	var sp model.ScanPrefix
	var err = row.Scan(&sp.ID, &sp.ProxyID, &sp.ProxyName, &sp.AddressDomainID,
		&sp.DomainName, &sp.Prefix, &sp.Enabled,
		&sp.Note, &sp.LastRunAt, &sp.LastResult, &sp.CreatedAt)
	return sp, notFound(err)
}

// ListScanPrefixes: all prefixes, or those of a given proxy.
func (s *Store) ListScanPrefixes(ctx context.Context, proxyID string) ([]model.ScanPrefix, error) {
	var q = `select ` + scanPrefixCols + scanPrefixFrom +
		` order by p.internal desc, p.name, sp.prefix`
	var args = []any{}
	if proxyID != "" {
		q = `select ` + scanPrefixCols + scanPrefixFrom +
			` where sp.proxy_id = $1::uuid order by sp.prefix`
		args = append(args, proxyID)
	}
	var rows, err = s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out = []model.ScanPrefix{}
	for rows.Next() {
		var sp, scanErr = scanScanPrefix(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, sp)
	}
	return out, rows.Err()
}

func (s *Store) GetScanPrefix(ctx context.Context, id string) (model.ScanPrefix, error) {
	return scanScanPrefix(s.pool.QueryRow(ctx,
		`select `+scanPrefixCols+scanPrefixFrom+` where sp.id = $1::uuid`, id))
}

// CreateScanPrefix declares a prefix on a proxy. Empty domainID = the proxy's home domain,
// i.e. "a network of its own, distinct from all others". That default is the only safe
// one: assuming sharing would merge distinct devices; assuming separation costs only a duplicate.
func (s *Store) CreateScanPrefix(ctx context.Context, proxyID, prefix, note, domainID string, enabled bool) (model.ScanPrefix, error) {
	var err error
	if domainID == "" {
		if domainID, err = s.HomeDomain(ctx, proxyID); err != nil {
			return model.ScanPrefix{}, err
		}
	}
	var id string
	if err = s.pool.QueryRow(ctx,
		`insert into scan_prefixes (proxy_id, prefix, note, enabled, address_domain_id)
		 values ($1::uuid, $2, $3, $4, $5::uuid) returning id::text`,
		proxyID, prefix, note, enabled, domainID).Scan(&id); err != nil {
		return model.ScanPrefix{}, err
	}
	return s.GetScanPrefix(ctx, id)
}

// UpdateScanPrefix: changing a prefix's domain declares "these two proxies watch the same
// network" — or the reverse.
//
// The switch is NOT retroactive, deliberately. Candidates already queued keep the domain
// under which they were ranked: bulk reclassification could create collision on the merge
// key, and a data migration must not trigger from a form. Subsequent observations fall into
// the new domain and merge; the orphaned duplicate stops refreshing and disappears with
// PurgeStaleDiscovered.
func (s *Store) UpdateScanPrefix(ctx context.Context, id, prefix, note, domainID string, enabled bool) (model.ScanPrefix, error) {
	var tag, err = s.pool.Exec(ctx,
		`update scan_prefixes set prefix = $2, note = $3, enabled = $4,
		   address_domain_id = coalesce($5::uuid, address_domain_id)
		 where id = $1::uuid`,
		id, prefix, note, enabled, nullableUUID(domainID))
	if err != nil {
		return model.ScanPrefix{}, err
	}
	if tag.RowsAffected() == 0 {
		return model.ScanPrefix{}, ErrNotFound
	}
	return s.GetScanPrefix(ctx, id)
}

// nullableUUID: empty string means "no change", not "clear".
func nullableUUID(id string) any {
	if id == "" {
		return nil
	}
	return id
}

func (s *Store) DeleteScanPrefix(ctx context.Context, id string) error {
	var tag, err = s.pool.Exec(ctx, `delete from scan_prefixes where id = $1::uuid`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

func (s *Store) TouchScanPrefix(ctx context.Context, id, result string) error {
	var _, err = s.pool.Exec(ctx,
		`update scan_prefixes set last_run_at = now(), last_result = $2 where id = $1::uuid`,
		id, result)
	return err
}

// --- Discovered candidates ---

const discoveredCols = `d.id::text, d.source, coalesce(d.proxy_id::text, ''), coalesce(p.name, ''),
	coalesce(d.address_domain_id::text, ''), coalesce(ad.name, ''),
	d.ip, d.mac, d.serial, d.hostname, d.vendor, d.model, d.driver, d.confidence,
	d.transports, d.fingerprint::text, octet_length(d.raw_config) > 0,
	d.credential_id::text, d.note, d.first_seen, d.last_seen,
	d.adopted_device_id::text, d.adopted_site_id::text, d.adopted_at`

// discoveredFrom: both joins are LEFT — a candidate survives its witness proxy or domain
// being deleted, and making it disappear from the queue for that reason would be
// incomprehensible to the operator.
const discoveredFrom = ` from discovered d
	left join proxies p on p.id = d.proxy_id
	left join address_domains ad on ad.id = d.address_domain_id`

func scanDiscovered(row pgx.Row) (model.Discovered, error) {
	var d model.Discovered
	var fingerprint string
	var err = row.Scan(&d.ID, &d.Source, &d.ProxyID, &d.ProxyName,
		&d.AddressDomainID, &d.DomainName, &d.IP, &d.MAC,
		&d.Serial, &d.Hostname, &d.Vendor, &d.Model, &d.Driver, &d.Confidence,
		&d.Transports, &fingerprint, &d.HasConfig, &d.CredentialID, &d.Note,
		&d.FirstSeen, &d.LastSeen, &d.AdoptedDevice, &d.AdoptedSite, &d.AdoptedAt)
	if err != nil {
		return d, notFound(err)
	}
	d.Fingerprint = json.RawMessage(fingerprint)
	d.Sightings = []model.Sighting{}
	return d, nil
}

// DiscoveredKey builds a candidate merge key: serial if known, else MAC, else
// (ADDRESS DOMAIN, IP) pair.
//
// Fallback previously relied on witness proxy. That was prudent but incomplete: two proxies
// on the same network could never merge observations of a silent device. The decision
// itself lives in internal/netscope, where it is exercised without a database.
func DiscoveredKey(serial, mac, domainID, ip string) string {
	return netscope.IdentityKey(serial, mac, domainID, ip)
}

// UpsertDiscovered merges an observation into the candidate queue.
//
// An already adopted candidate is NOT resurrected: rescanning a network must not put a
// device already in a site back in the queue.
func (s *Store) UpsertDiscovered(ctx context.Context, d model.Discovered, rawConfig string) (model.Discovered, error) {
	var tx, err = s.pool.Begin(ctx)
	if err != nil {
		return model.Discovered{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var key = DiscoveredKey(d.Serial, d.MAC, d.AddressDomainID, d.IP)
	var id string
	var adopted *string
	err = tx.QueryRow(ctx,
		`select id::text, adopted_device_id::text from discovered
		 where coalesce(nullif(serial, ''), nullif(mac, ''),
		                coalesce(address_domain_id::text, 'sans-domaine') || '|' || ip) = $1`,
		key).Scan(&id, &adopted)

	// A candidate that ACQUIRES a serial number changes merge key: the first observation,
	// made without serial, was ranked under (domain, ip). Without this catch-up, the same
	// device would appear twice — once identified, once anonymous. Observed in real testing
	// with a switch whose connector reported serial on second pass.
	//
	// Catch-up follows the key: it searches for the anonymous of the same DOMAIN, not the
	// same proxy. That lets a second proxy on the same network absorb the anonymous left by
	// the first instead of creating a twin.
	if d.Serial != "" || d.MAC != "" {
		var stale string
		var staleAdopted *string
		var findErr = tx.QueryRow(ctx,
			// coalesce on BOTH columns: depending on insert path, missing serial is NULL or
			// empty string, and the merge key uses nullif() — the condition must cover both,
			// otherwise catch-up never bites (observed in testing).
			`select id::text, adopted_device_id::text from discovered
			 where coalesce(serial, '') = '' and coalesce(mac::text, '') = ''
			   and ip = $1
			   and coalesce(address_domain_id::text, 'sans-domaine') =
			       coalesce($2::text, 'sans-domaine')`,
			d.IP, nullableUUID(d.AddressDomainID)).Scan(&stale, &staleAdopted)
		if findErr == nil && stale != id {
			// Never delete an already adopted candidate: it carries provenance of a fleet device.
			if staleAdopted == nil {
				var _, delErr = tx.Exec(ctx,
					`delete from discovered where id = $1::uuid`, stale)
				if delErr != nil {
					return model.Discovered{}, delErr
				}
			}
			// If the anonymous was the only one known, it becomes the row to enrich.
			if err != nil && staleAdopted != nil {
				id, adopted, err = stale, staleAdopted, nil
			}
		}
	}

	var proxyArg any = nil
	if d.ProxyID != "" {
		proxyArg = d.ProxyID
	}
	var credArg any = nil
	if d.CredentialID != nil && *d.CredentialID != "" {
		credArg = *d.CredentialID
	}

	switch {
	case err == nil && adopted != nil:
		// Already adopted: only refresh last seen date.
		if _, err = tx.Exec(ctx,
			`update discovered set last_seen = now() where id = $1::uuid`, id); err != nil {
			return model.Discovered{}, err
		}
	case err == nil:
		// Merge: overwrite a field only if the new observation adds something, and do not
		// degrade a stronger identification.
		//
		// `address_domain_id` is NOT updated here, deliberately: it participates in the merge
		// key. Changing it under an already ranked row would move it to another identity space
		// and duplicate it on the next pass. A serial-identified candidate seen from a second
		// network therefore keeps the domain of its first observation; trace of the other path
		// lives in discovery_sightings, which is exactly its role.
		if _, err = tx.Exec(ctx,
			`update discovered set
			   source = case when $2 <> 'scan' then $2 else source end,
			   proxy_id = coalesce($3::uuid, proxy_id),
			   ip = case when $4 <> '' then $4 else ip end,
			   mac = case when $5 <> '' then $5 else mac end,
			   serial = case when $6 <> '' then $6 else serial end,
			   hostname = case when $7 <> '' then $7 else hostname end,
			   vendor = case when $12 >= confidence and $8 <> '' then $8 else vendor end,
			   model = case when $12 >= confidence and $9 <> '' then $9 else model end,
			   driver = case when $12 >= confidence and $10 <> '' then $10 else driver end,
			   transports = case when $11 <> '' then $11 else transports end,
			   confidence = greatest(confidence, $12),
			   fingerprint = $13::jsonb,
			   raw_config = case when $14 <> '' then $14 else raw_config end,
			   credential_id = coalesce($15::uuid, credential_id),
			   note = $16,
			   last_seen = now()
			 where id = $1::uuid`,
			id, d.Source, proxyArg, d.IP, d.MAC, d.Serial, d.Hostname, d.Vendor,
			d.Model, d.Driver, d.Transports, d.Confidence,
			string(jsonOr(d.Fingerprint, `{}`)), rawConfig, credArg, d.Note); err != nil {
			return model.Discovered{}, err
		}
	case notFound(err) == ErrNotFound:
		if err = tx.QueryRow(ctx,
			`insert into discovered (source, proxy_id, ip, mac, serial, hostname,
			     vendor, model, driver, confidence, transports, fingerprint,
			     raw_config, credential_id, note, address_domain_id)
			 values ($1, $2::uuid, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12::jsonb, $13, $14::uuid, $15,
			         $16::uuid)
			 returning id::text`,
			d.Source, proxyArg, d.IP, d.MAC, d.Serial, d.Hostname, d.Vendor, d.Model,
			d.Driver, d.Confidence, d.Transports, string(jsonOr(d.Fingerprint, `{}`)),
			rawConfig, credArg, d.Note, nullableUUID(d.AddressDomainID)).Scan(&id); err != nil {
			return model.Discovered{}, err
		}
	default:
		return model.Discovered{}, err
	}

	// Observation by this proxy at this address.
	if d.ProxyID != "" && d.IP != "" {
		if _, err = tx.Exec(ctx,
			`insert into discovery_sightings (discovered_id, proxy_id, ip, latency_ms)
			 values ($1::uuid, $2::uuid, $3, $4)
			 on conflict (discovered_id, proxy_id, ip)
			 do update set latency_ms = excluded.latency_ms, last_seen = now()`,
			id, d.ProxyID, d.IP, 0); err != nil {
			return model.Discovered{}, err
		}
	}

	if err = tx.Commit(ctx); err != nil {
		return model.Discovered{}, err
	}
	return s.GetDiscovered(ctx, id)
}

func (s *Store) GetDiscovered(ctx context.Context, id string) (model.Discovered, error) {
	var d, err = scanDiscovered(s.pool.QueryRow(ctx,
		`select `+discoveredCols+discoveredFrom+` where d.id = $1::uuid`, id))
	if err != nil {
		return d, err
	}
	d.Sightings, err = s.listSightings(ctx, id)
	return d, err
}

// GetDiscoveredConfig returns raw configuration read from the device.
func (s *Store) GetDiscoveredConfig(ctx context.Context, id string) (string, error) {
	var raw string
	var err = s.pool.QueryRow(ctx,
		`select raw_config from discovered where id = $1::uuid`, id).Scan(&raw)
	return raw, notFound(err)
}

func (s *Store) SetDiscoveredConfig(ctx context.Context, id, raw string) error {
	var tag, err = s.pool.Exec(ctx,
		`update discovered set raw_config = $2, last_seen = now() where id = $1::uuid`, id, raw)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

func (s *Store) listSightings(ctx context.Context, discoveredID string) ([]model.Sighting, error) {
	var rows, err = s.pool.Query(ctx,
		`select ds.proxy_id::text, coalesce(p.name, ''), ds.ip, ds.latency_ms, ds.last_seen
		 from discovery_sightings ds
		 left join proxies p on p.id = ds.proxy_id
		 where ds.discovered_id = $1::uuid order by ds.last_seen desc`, discoveredID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out = []model.Sighting{}
	for rows.Next() {
		var sg model.Sighting
		if err = rows.Scan(&sg.ProxyID, &sg.ProxyName, &sg.IP, &sg.LatencyMS, &sg.LastSeen); err != nil {
			return nil, err
		}
		out = append(out, sg)
	}
	return out, rows.Err()
}

// ListDiscovered: the adoption queue. adopted=false returns only candidates still to process.
func (s *Store) ListDiscovered(ctx context.Context, includeAdopted bool) ([]model.Discovered, error) {
	var where = `where d.adopted_device_id is null`
	if includeAdopted {
		where = ``
	}
	var rows, err = s.pool.Query(ctx,
		`select `+discoveredCols+discoveredFrom+` `+where+`
		 order by d.confidence desc, d.last_seen desc limit 500`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out = []model.Discovered{}
	var ids = []string{}
	for rows.Next() {
		var d, scanErr = scanDiscovered(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, d)
		ids = append(ids, d.ID)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	// Sightings in one query: no N+1 on the queue.
	if len(ids) > 0 {
		var sightings map[string][]model.Sighting
		sightings, err = s.sightingsFor(ctx, ids)
		if err != nil {
			return nil, err
		}
		var i int
		for i = range out {
			var list = sightings[out[i].ID]
			if list != nil {
				out[i].Sightings = list
			}
		}
	}
	return out, nil
}

func (s *Store) sightingsFor(ctx context.Context, ids []string) (map[string][]model.Sighting, error) {
	var rows, err = s.pool.Query(ctx,
		`select ds.discovered_id::text, ds.proxy_id::text, coalesce(p.name, ''),
		        ds.ip, ds.latency_ms, ds.last_seen
		 from discovery_sightings ds
		 left join proxies p on p.id = ds.proxy_id
		 where ds.discovered_id = any($1::uuid[]) order by ds.last_seen desc`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out = map[string][]model.Sighting{}
	for rows.Next() {
		var key string
		var sg model.Sighting
		if err = rows.Scan(&key, &sg.ProxyID, &sg.ProxyName, &sg.IP, &sg.LatencyMS, &sg.LastSeen); err != nil {
			return nil, err
		}
		out[key] = append(out[key], sg)
	}
	return out, rows.Err()
}

func (s *Store) DeleteDiscovered(ctx context.Context, id string) error {
	var tag, err = s.pool.Exec(ctx, `delete from discovered where id = $1::uuid`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// AdoptDiscovered creates the device in the CHOSEN site and marks the candidate consumed,
// in one transaction: no device without marked candidate, nor the reverse.
//
// vlans is applied to the site (creating missing VLANs) when the user chose to import local
// configuration. Returns created and already-existing VLAN ids: the latter are KEPT as-is,
// and the caller must tell the user — announcing a creation that did not happen would make
// them believe the device VLAN plan won.
func (s *Store) AdoptDiscovered(ctx context.Context, discoveredID string, d model.Device, vlans []model.Vlan) (dev model.Device, created, kept []int, err error) {
	var tx pgx.Tx
	tx, err = s.pool.Begin(ctx)
	if err != nil {
		return model.Device{}, nil, nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var alreadyAdopted *string
	// The candidate's address domain FOLLOWS the adopted device: it will later say which
	// proxy is legitimate to answer for its management address. Losing it at adoption would
	// make every discovery-originated device as ambiguous as a manually entered one.
	var candDomain *string
	if err = tx.QueryRow(ctx,
		`select adopted_device_id::text, address_domain_id::text from discovered
		 where id = $1::uuid for update`,
		discoveredID).Scan(&alreadyAdopted, &candDomain); err != nil {
		return model.Device{}, nil, nil, notFound(err)
	}
	if alreadyAdopted != nil {
		return model.Device{}, nil, nil, ErrAlreadyAdopted
	}

	var deviceID string
	// `extra` carries connector and provenance (discovered_id): without it, the adopted
	// device forgets which platform it belongs to and where it came from. Omitting this
	// column at insert lost the driver for all adopted devices and broke relocation — which
	// needs the original candidate to find serial number. Observed in testing.
	if err = tx.QueryRow(ctx,
		`insert into devices (site_id, name, model, vendor, location, mgmt_ip,
		     status, status_note, poe_budget_w, pos_x, pos_y, port_groups, ports, extra,
		     address_domain_id)
		 values ($1::uuid, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12::jsonb, $13::jsonb,
		         $14::jsonb, $15::uuid)
		 returning id::text`,
		d.SiteID, d.Name, d.Model, d.Vendor, d.Location, d.MgmtIP, d.Status,
		d.StatusNote, d.PoEBudgetW, d.PosX, d.PosY,
		jsonOr(d.PortGroups, `[]`), jsonOr(d.Ports, `{}`),
		jsonOr(d.Extra, `{}`), candDomain).Scan(&deviceID); err != nil {
		return model.Device{}, nil, nil, err
	}

	// Imported VLANs: do NOT touch those that already exist — their name and color come from
	// the user, not the device. Distinguish created vs kept so we can say so honestly.
	created = []int{}
	kept = []int{}
	var v model.Vlan
	for _, v = range vlans {
		var tag, insErr = tx.Exec(ctx,
			`insert into vlans (site_id, vlan_id, name, color, purpose)
			 values ($1::uuid, $2, $3, $4, $5)
			 on conflict (site_id, vlan_id) do nothing`,
			d.SiteID, v.VlanID, v.Name, v.Color, v.Purpose)
		if insErr != nil {
			return model.Device{}, nil, nil, insErr
		}
		if tag.RowsAffected() > 0 {
			created = append(created, v.VlanID)
		} else {
			kept = append(kept, v.VlanID)
		}
	}

	if _, err = tx.Exec(ctx,
		`update discovered set adopted_device_id = $2::uuid, adopted_site_id = $3::uuid,
		        adopted_at = now() where id = $1::uuid`,
		discoveredID, deviceID, d.SiteID); err != nil {
		return model.Device{}, nil, nil, err
	}

	if err = tx.Commit(ctx); err != nil {
		return model.Device{}, nil, nil, err
	}
	dev, err = s.GetDevice(ctx, deviceID)
	return dev, created, kept, err
}

// ListDiscoveryProxies: proxies usable as a discovery source FOR THIS SITE — those that
// cover it, plus instance proxies. What they discover remains a site-less candidate
// (cf. docs/en/01-Architecture/Discovery.md): coverage says who may use it, not what scan produces.
func (s *Store) ListDiscoveryProxies(ctx context.Context, siteID string) ([]model.Proxy, error) {
	var rows, err = s.pool.Query(ctx,
		`select `+proxyCols+` from proxies p
		 where not p.revoked and (p.global
		       or exists (select 1 from proxy_sites ps
		                  where ps.proxy_id = p.id and ps.site_id = $1::uuid))
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

// NextFreePosition places a new device on a free grid cell on the plan, so adoption does
// not stack it on another at (0,0).
func (s *Store) NextFreePosition(ctx context.Context, siteID string) (float64, float64, error) {
	var count int
	var err = s.pool.QueryRow(ctx,
		`select count(*) from devices where site_id = $1::uuid`, siteID).Scan(&count)
	if err != nil {
		return 0, 0, err
	}
	var col = count % 5
	var row = count / 5
	return float64(40 + col*200), float64(40 + row*140), nil
}

// PurgeStaleDiscovered removes never-adopted candidates not seen for a while: the queue
// must not accumulate vanished hosts.
func (s *Store) PurgeStaleDiscovered(ctx context.Context, olderThan time.Duration) (int64, error) {
	var tag, err = s.pool.Exec(ctx,
		`delete from discovered
		 where adopted_device_id is null and last_seen < now() - $1::interval`,
		olderThan.String())
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// RevealKnownCredential returns ONE instance credential's ciphertext, by id.
//
// Deliberately narrow where [Store.CredentialSecrets] is broad: that one answers
// "does the key still open these?" over the whole table and its caller keeps only
// a boolean. This one exists to put a secret on an operator's screen, so it reads
// exactly one row and the caller has to name which.
//
// Disabled credentials are returned too. A credential taken out of the discovery
// try order is still the one sitting on every device it was ever applied to — and
// disabling it is a common way to stop trying an old password, not a statement
// that nobody needs to read it again. Refusing here would hide the secret that
// opens the very devices left behind.
func (s *Store) RevealKnownCredential(ctx context.Context, id string) (EncCredential, error) {
	var c EncCredential
	var err = s.pool.QueryRow(ctx,
		`select id::text, name, kind, username, secret_enc
		   from known_credentials where id = $1::uuid`, id).
		Scan(&c.ID, &c.Name, &c.Kind, &c.Username, &c.SecretEnc)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}
