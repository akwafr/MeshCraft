package store

// Site extensions and the read-only mirror they import (migration 0046,
// ADR-0015 and ADR-0016).
//
// The token follows the SAME regime as a site credential (cf. sitecreds.go):
// list functions never return it, only its presence; revelation is a separate,
// explicit, audited operation, and this layer hands back CIPHERTEXT — opening
// it and tracing the access is the handler's job, so no store call can
// accidentally produce a cleartext secret.
//
// The mirror is written only by an import pass. Nothing here updates a box, and
// nothing here inserts into `devices`.

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"

	"meshcraft/internal/model"
)

// `has_token` rather than the token: there is no column list in this file that
// could carry a secret into a response (cf. siteCredCols and the same reasoning).
const siteExtCols = `id::text, site_id::text, kind, label, base_url,
	octet_length(token_enc) > 0,
	verify_tls, transport, coalesce(proxy_id::text, ''), enabled,
	remote_site_id, remote_site_ref, remote_site_name,
	paired_at, orphaned_at, last_sync_at, last_error,
	created_at, updated_at`

func scanSiteExt(row pgx.Row) (model.SiteExtension, error) {
	var e model.SiteExtension
	var err = row.Scan(&e.ID, &e.SiteID, &e.Kind, &e.Label, &e.BaseURL,
		&e.HasToken, &e.VerifyTLS, &e.Transport, &e.ProxyID, &e.Enabled,
		&e.RemoteSiteID, &e.RemoteSiteRef, &e.RemoteSiteName,
		&e.PairedAt, &e.OrphanedAt, &e.LastSyncAt, &e.LastError,
		&e.CreatedAt, &e.UpdatedAt)
	return e, notFound(err)
}

// ListSiteExtensions returns every extension configured on a site.
func (s *Store) ListSiteExtensions(ctx context.Context, siteID string) ([]model.SiteExtension, error) {
	var rows, err = s.pool.Query(ctx,
		`select `+siteExtCols+` from site_extensions
		 where site_id = $1::uuid order by kind, id`, siteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out = []model.SiteExtension{}
	for rows.Next() {
		var e, scanErr = scanSiteExt(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// GetSiteExtension returns one extension by id, secret-free.
func (s *Store) GetSiteExtension(ctx context.Context, id string) (model.SiteExtension, error) {
	return scanSiteExt(s.pool.QueryRow(ctx,
		`select `+siteExtCols+` from site_extensions where id = $1::uuid`, id))
}

// UpsertSiteExtension creates or replaces the extension of a given kind on a
// site. `tokenEnc` nil leaves the stored token UNCHANGED — the same rule site
// credentials follow, and for the same reason: the UI never reads the token
// back, so it cannot resubmit it, and a label edit must not wipe the secret.
func (s *Store) UpsertSiteExtension(ctx context.Context, e model.SiteExtension,
	tokenEnc []byte, updatedBy string) (model.SiteExtension, error) {

	// A `proxy` transport with no proxy is a configuration that cannot work;
	// an empty string would also fail the uuid cast. Nil means "no proxy".
	var proxy any
	if e.ProxyID != "" {
		proxy = e.ProxyID
	}

	if tokenEnc == nil {
		return scanSiteExt(s.pool.QueryRow(ctx,
			`update site_extensions set label = $3, base_url = $4, verify_tls = $5,
			        transport = $6, proxy_id = $7::uuid, enabled = $8,
			        updated_at = now(), updated_by = $9
			 where site_id = $1::uuid and kind = $2
			 returning `+siteExtCols,
			e.SiteID, e.Kind, e.Label, e.BaseURL, e.VerifyTLS,
			e.Transport, proxy, e.Enabled, updatedBy))
	}
	return scanSiteExt(s.pool.QueryRow(ctx,
		`insert into site_extensions
		   (site_id, kind, label, base_url, token_enc, verify_tls, transport,
		    proxy_id, enabled, updated_by)
		 values ($1::uuid, $2, $3, $4, $5, $6, $7, $8::uuid, $9, $10)
		 on conflict (site_id, kind) do update set
		   label = excluded.label, base_url = excluded.base_url,
		   token_enc = excluded.token_enc, verify_tls = excluded.verify_tls,
		   transport = excluded.transport, proxy_id = excluded.proxy_id,
		   enabled = excluded.enabled, updated_at = now(),
		   updated_by = excluded.updated_by
		 returning `+siteExtCols,
		e.SiteID, e.Kind, e.Label, e.BaseURL, tokenEnc, e.VerifyTLS,
		e.Transport, proxy, e.Enabled, updatedBy))
}

// RevealSiteExtensionToken returns the extension and its token as CIPHERTEXT.
//
// Decryption and the audit line are deliberately left to the caller — exactly
// as RevealSiteCredential does — so that the only code able to produce a
// cleartext token is code that also writes the trace. Untraced cleartext access
// is worth less than no vault at all.
func (s *Store) RevealSiteExtensionToken(ctx context.Context, id string) (model.SiteExtension, []byte, error) {
	var e model.SiteExtension
	var enc []byte
	var row = s.pool.QueryRow(ctx,
		`select `+siteExtCols+`, token_enc from site_extensions where id = $1::uuid`, id)
	var err = row.Scan(&e.ID, &e.SiteID, &e.Kind, &e.Label, &e.BaseURL,
		&e.HasToken, &e.VerifyTLS, &e.Transport, &e.ProxyID, &e.Enabled,
		&e.RemoteSiteID, &e.RemoteSiteRef, &e.RemoteSiteName,
		&e.PairedAt, &e.OrphanedAt, &e.LastSyncAt, &e.LastError,
		&e.CreatedAt, &e.UpdatedAt, &enc)
	if err != nil {
		return model.SiteExtension{}, nil, notFound(err)
	}
	return e, enc, nil
}

// DeleteSiteExtension drops the extension and, by cascade, everything it
// imported. That is the right cascade: a mirror with no extension has no way to
// be refreshed and no way to be explained.
func (s *Store) DeleteSiteExtension(ctx context.Context, id string) error {
	var _, err = s.pool.Exec(ctx, `delete from site_extensions where id = $1::uuid`, id)
	return err
}

// PairSiteExtension records the chosen remote site. It writes the stable id, the
// short reference the legacy API needs, and the name AS A LABEL — and it clears
// `orphaned_at`, because pairing afresh is the answer to an orphaned pairing.
func (s *Store) PairSiteExtension(ctx context.Context, id, remoteID, remoteRef, remoteName string) (model.SiteExtension, error) {
	return scanSiteExt(s.pool.QueryRow(ctx,
		`update site_extensions
		    set remote_site_id = $2, remote_site_ref = $3, remote_site_name = $4,
		        paired_at = now(), orphaned_at = null, last_error = '',
		        updated_at = now()
		  where id = $1::uuid
		 returning `+siteExtCols, id, remoteID, remoteRef, remoteName))
}

// RefreshRemoteSiteLabel updates the CACHED display name of the paired site.
//
// This is the whole payoff of pairing on the id (ADR-0015 §6): "lab Events #04
// - ARENA" can be renamed in UniFi and the only thing that happens here is that
// the label follows. The reference is refreshed with it, because UniFi derives
// one from the other and the legacy SSID endpoint depends on it.
func (s *Store) RefreshRemoteSiteLabel(ctx context.Context, id, remoteRef, remoteName string) error {
	var _, err = s.pool.Exec(ctx,
		`update site_extensions set remote_site_ref = $2, remote_site_name = $3,
		        updated_at = now()
		  where id = $1::uuid
		    and (remote_site_ref <> $2 or remote_site_name <> $3)`,
		id, remoteRef, remoteName)
	return err
}

// MarkSiteExtensionOrphaned is called ONLY when the controller answered and the
// paired site was absent from its list (ADR-0015 §7). It never deletes: the
// mirror stays visible and stamped stale, because the alternative is
// indistinguishable from an outage at the moment it happens, and deletion is
// the one of the two that cannot be undone.
func (s *Store) MarkSiteExtensionOrphaned(ctx context.Context, id, because string) error {
	var _, err = s.pool.Exec(ctx,
		`update site_extensions
		    set orphaned_at = coalesce(orphaned_at, now()), last_error = $2,
		        updated_at = now()
		  where id = $1::uuid`, id, because)
	return err
}

// MarkSiteExtensionError records a transport failure. It does NOT touch
// `orphaned_at`: an unreachable controller says nothing about whether the site
// still exists.
func (s *Store) MarkSiteExtensionError(ctx context.Context, id, because string) error {
	var _, err = s.pool.Exec(ctx,
		`update site_extensions set last_error = $2, updated_at = now()
		  where id = $1::uuid`, id, because)
	return err
}

// --- The mirror ---

const extDevCols = `id::text, extension_id::text, site_id::text, external_id,
	class, name, model, firmware, state, mgmt_ip, mac, radios, ports, seen_at`

func scanExtDevice(row pgx.Row) (model.ExtDevice, error) {
	var d model.ExtDevice
	var radios, ports []byte
	var err = row.Scan(&d.ID, &d.ExtensionID, &d.SiteID, &d.ExternalID,
		&d.Class, &d.Name, &d.Model, &d.Firmware, &d.State, &d.MgmtIP, &d.MAC,
		&radios, &ports, &d.SeenAt)
	if err != nil {
		return d, notFound(err)
	}
	// A mirror row whose JSON child fails to decode is still a row worth
	// showing: losing an access point from a list because its radio snapshot
	// was malformed would hide the box entirely.
	if len(radios) > 0 {
		_ = json.Unmarshal(radios, &d.Radios)
	}
	if len(ports) > 0 {
		_ = json.Unmarshal(ports, &d.Ports)
	}
	if d.Radios == nil {
		d.Radios = []model.ExtRadio{}
	}
	if d.Ports == nil {
		d.Ports = []model.ExtPort{}
	}
	return d, nil
}

// GetExtDevice loads one mirror row by meshcraft's own id.
//
// A gesture needs it (ADR-0025): the browser addresses a box by the mirror's
// id, and the controller must be asked by the VENDOR's identifiers — the
// external id to forget, the MAC to reprovision. Resolving them server-side is
// what stops a caller from naming an arbitrary device on somebody's
// controller: only a box this site has actually mirrored can be reached.
func (s *Store) GetExtDevice(ctx context.Context, id string) (model.ExtDevice, error) {
	return scanExtDevice(s.pool.QueryRow(ctx,
		`select `+extDevCols+` from site_extension_devices where id = $1::uuid`, id))
}

// ListExtDevices returns the mirror of a site. `class` empty means every class.
func (s *Store) ListExtDevices(ctx context.Context, siteID, class string) ([]model.ExtDevice, error) {
	var rows, err = s.pool.Query(ctx,
		`select `+extDevCols+` from site_extension_devices
		  where site_id = $1::uuid and ($2 = '' or class = $2)
		  order by class, name, external_id`, siteID, class)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out = []model.ExtDevice{}
	for rows.Next() {
		var d, scanErr = scanExtDevice(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

const extSSIDCols = `id::text, extension_id::text, site_id::text, external_id,
	name, enabled, security, wpa_mode, bands, seen_at`

// ListPairedExtensions returns every ENABLED, PAIRED extension of every site,
// for the background import pass (ADR-0029 §11).
//
// Deliberately not per-site: there is ONE pass that walks the whole instance
// sequentially, because the cost that has to be bounded is the remote
// controller's and its token's, not meshcraft's. Ordered by id so the walk is
// stable from one pass to the next — an unstable order would make a pass that
// runs out of time starve a different extension each round.
//
// An extension that is disabled or unpaired is excluded by the query rather
// than skipped by the caller: the loop should not have to know what makes one
// importable.
func (s *Store) ListPairedExtensions(ctx context.Context) ([]model.SiteExtension, error) {
	var rows, err = s.pool.Query(ctx,
		`select `+siteExtCols+` from site_extensions
		  where enabled and remote_site_id <> '' order by id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out = []model.SiteExtension{}
	for rows.Next() {
		var e, scanErr = scanSiteExt(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// GetExtSSID loads one mirrored service set by meshcraft's own id.
//
// Same reason GetExtDevice exists (ADR-0029): the browser names a service set
// by the mirror's id, and the controller has to be addressed by the vendor's
// `_id`. Resolving that here is what keeps an edit inside the site's own
// inventory — a caller cannot name an arbitrary SSID on somebody's controller,
// only one this site has actually mirrored.
func (s *Store) GetExtSSID(ctx context.Context, id string) (model.ExtSSID, error) {
	var w model.ExtSSID
	var err = s.pool.QueryRow(ctx,
		`select `+extSSIDCols+` from site_extension_ssids where id = $1::uuid`, id).
		Scan(&w.ID, &w.ExtensionID, &w.SiteID, &w.ExternalID,
			&w.Name, &w.Enabled, &w.Security, &w.WPAMode, &w.Bands, &w.SeenAt)
	if err != nil {
		return model.ExtSSID{}, err
	}
	if w.Bands == nil {
		w.Bands = []string{}
	}
	return w, nil
}

// ListExtSSIDs returns the site's service sets.
func (s *Store) ListExtSSIDs(ctx context.Context, siteID string) ([]model.ExtSSID, error) {
	var rows, err = s.pool.Query(ctx,
		`select `+extSSIDCols+` from site_extension_ssids
		  where site_id = $1::uuid order by name, external_id`, siteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out = []model.ExtSSID{}
	for rows.Next() {
		var w model.ExtSSID
		var scanErr = rows.Scan(&w.ID, &w.ExtensionID, &w.SiteID, &w.ExternalID,
			&w.Name, &w.Enabled, &w.Security, &w.WPAMode, &w.Bands, &w.SeenAt)
		if scanErr != nil {
			return nil, scanErr
		}
		if w.Bands == nil {
			w.Bands = []string{}
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// ReplaceExtInventory writes one import pass, ATOMICALLY.
//
// Atomicity is the point: an interrupted import must not leave a site showing
// half of yesterday's access points and half of today's, because the difference
// between the two is exactly what somebody would read as "a box disappeared".
// Rows the controller no longer lists are deleted — a box absent from a site
// that still ANSWERS has genuinely left that site, which is the opposite of the
// orphaned-pairing case above.
func (s *Store) ReplaceExtInventory(ctx context.Context, extID, siteID string,
	devices []model.ExtDevice, ssids []model.ExtSSID, ssidsKnown bool) (model.ExtSyncReport, error) {

	var rep model.ExtSyncReport
	var tx, err = s.pool.Begin(ctx)
	if err != nil {
		return rep, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var seen = make([]string, 0, len(devices))
	var d model.ExtDevice
	for _, d = range devices {
		var radios, ports []byte
		radios, err = json.Marshal(d.Radios)
		if err != nil {
			return rep, err
		}
		ports, err = json.Marshal(d.Ports)
		if err != nil {
			return rep, err
		}
		_, err = tx.Exec(ctx,
			`insert into site_extension_devices
			   (extension_id, site_id, external_id, class, name, model, firmware,
			    state, mgmt_ip, mac, radios, ports, seen_at)
			 values ($1::uuid, $2::uuid, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, now())
			 on conflict (extension_id, external_id) do update set
			   class = excluded.class, name = excluded.name, model = excluded.model,
			   firmware = excluded.firmware, state = excluded.state,
			   mgmt_ip = excluded.mgmt_ip, mac = excluded.mac,
			   radios = excluded.radios, ports = excluded.ports, seen_at = now()`,
			extID, siteID, d.ExternalID, d.Class, d.Name, d.Model, d.Firmware,
			d.State, d.MgmtIP, d.MAC, radios, ports)
		if err != nil {
			return rep, err
		}
		seen = append(seen, d.ExternalID)
		switch d.Class {
		case model.ExtClassAccessPoint:
			rep.AccessPoints++
		case model.ExtClassSwitch:
			rep.Switches++
		default:
			rep.Others++
		}
	}

	var res, delErr = tx.Exec(ctx,
		`delete from site_extension_devices
		  where extension_id = $1::uuid and not (external_id = any($2::text[]))`,
		extID, seen)
	if delErr != nil {
		return rep, delErr
	}
	rep.Removed = int(res.RowsAffected())

	// `ssidsKnown` false means the legacy wlanconf call failed. We then leave
	// the stored SSIDs ALONE rather than deleting them: an endpoint UniFi may
	// change without notice must degrade the screen, not empty it.
	if ssidsKnown {
		var keep = make([]string, 0, len(ssids))
		var w model.ExtSSID
		for _, w = range ssids {
			_, err = tx.Exec(ctx,
				`insert into site_extension_ssids
				   (extension_id, site_id, external_id, name, enabled, security,
				    wpa_mode, bands, seen_at)
				 values ($1::uuid, $2::uuid, $3, $4, $5, $6, $7, $8, now())
				 on conflict (extension_id, external_id) do update set
				   name = excluded.name, enabled = excluded.enabled,
				   security = excluded.security, wpa_mode = excluded.wpa_mode,
				   bands = excluded.bands, seen_at = now()`,
				extID, siteID, w.ExternalID, w.Name, w.Enabled, w.Security,
				w.WPAMode, w.Bands)
			if err != nil {
				return rep, err
			}
			keep = append(keep, w.ExternalID)
			rep.SSIDs++
		}
		_, err = tx.Exec(ctx,
			`delete from site_extension_ssids
			  where extension_id = $1::uuid and not (external_id = any($2::text[]))`,
			extID, keep)
		if err != nil {
			return rep, err
		}
	} else {
		var count int
		err = tx.QueryRow(ctx,
			`select count(*) from site_extension_ssids where extension_id = $1::uuid`,
			extID).Scan(&count)
		if err != nil {
			return rep, err
		}
		rep.SSIDs = count
	}

	_, err = tx.Exec(ctx,
		`update site_extensions set last_sync_at = now(), last_error = '',
		        orphaned_at = null, updated_at = now()
		  where id = $1::uuid`, extID)
	if err != nil {
		return rep, err
	}
	if err = tx.Commit(ctx); err != nil {
		return rep, err
	}
	rep.SyncedAt = time.Now().UTC()
	return rep, nil
}
