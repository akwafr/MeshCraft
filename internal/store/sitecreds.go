package store

import (
	"context"

	"github.com/jackc/pgx/v5"

	"meshcraft/internal/model"
)

// Site credentials (cf. migration 0010).
//
// Secrets are NEVER returned by list functions: only their presence and rotation
// date are. Revelation is a separate, explicit, audited operation — so a password
// cannot be read by accident while displaying a page.

// `secret2_enc` is nullable — only `snmp_v3` carries one (migration 0017) — hence
// the `coalesce`: `octet_length(null)` is null, not false, and a null boolean would
// fail the scan on a simple admin password.
const siteCredCols = `id::text, site_id::text, group_name, kind, username,
	octet_length(secret_enc) > 0,
	octet_length(coalesce(secret2_enc, ''::bytea)) > 0,
	note, rotated_at, created_at, per_device, tag, priority`

func scanSiteCred(row pgx.Row) (model.SiteCredential, error) {
	var c model.SiteCredential
	var err = row.Scan(&c.ID, &c.SiteID, &c.GroupName, &c.Kind, &c.Username,
		&c.HasSecret, &c.HasSecret2, &c.Note, &c.RotatedAt, &c.CreatedAt,
		&c.PerDevice, &c.Tag, &c.Priority)
	return c, notFound(err)
}

func (s *Store) ListSiteCredentials(ctx context.Context, siteID string) ([]model.SiteCredential, error) {
	var rows, err = s.pool.Query(ctx,
		// Tag enters the sort: since SNMP kinds, several credentials on one site differ
		// ONLY by it (one `snmp_v3` per crypto family). Without this criterion their
		// relative order is left to the query plan and the list reorders from one refresh
		// to the next, causing clicks on the wrong row.
		`select `+siteCredCols+` from site_credentials
		 where site_id = $1::uuid order by group_name, kind, tag, id`, siteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out = []model.SiteCredential{}
	for rows.Next() {
		var c, scanErr = scanSiteCred(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// UpsertSiteCredential creates or replaces a credential. `secretEnc` nil leaves the
// secret unchanged: the UI never reads it back, so it cannot resubmit it.
//
// `secret2Enc` follows the same rule, and it matters more: a label edit on a v3
// identity sends no phrases, and overwriting the second with NULL would destroy the
// encryption key for an entire crypto family with no warning — the next push would
// replay an identity the devices would no longer recognize.
func (s *Store) UpsertSiteCredential(ctx context.Context, c model.SiteCredential,
	secretEnc, secret2Enc []byte, updatedBy string) (model.SiteCredential, error) {
	// Empty `updatedBy`: meshcraft itself is acting (startup catch-up), not a user.
	// An empty uuid would fail the cast.
	var actor any
	if updatedBy != "" {
		actor = updatedBy
	}
	if secretEnc == nil {
		return scanSiteCred(s.pool.QueryRow(ctx,
			`update site_credentials set username = $4, note = $5, updated_by = $6::uuid,
			        priority = $8
			 where site_id = $1::uuid and group_name = $2 and kind = $3 and tag = $7
			 returning `+siteCredCols,
			c.SiteID, c.GroupName, c.Kind, c.Username, c.Note, actor, c.Tag, c.Priority))
	}
	// A set secret ALWAYS opens a generation, including the first. Without that, a
	// credential created today would start with empty history and the connector would
	// have nothing to try on devices — the secret would exist without ever knowing
	// which one was pushed, or when.
	var tx, err = s.pool.Begin(ctx)
	if err != nil {
		return model.SiteCredential{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// `secret2_enc = coalesce($10, secret2_enc)`: replace only what is provided. Cf.
	// header — a caller that does not know the second phrase must not be able to erase
	// it through here.
	//
	// The EFFECTIVE value is read back in the same RETURNING rather than taken from the
	// parameter: that value, not the caller's intent, must enter the generation. Otherwise
	// rotating only the auth phrase would archive a pair that never existed.
	var out model.SiteCredential
	var effective2 []byte
	err = tx.QueryRow(ctx,
		`insert into site_credentials (site_id, group_name, tag, kind, username, secret_enc,
		     note, updated_by, priority, secret2_enc, rotated_at)
		 values ($1::uuid, $2, $8, $3, $4, $5, $6, $7::uuid, $9, $10, now())
		 on conflict (site_id, group_name, tag, kind) do update set
		   username = $4, secret_enc = $5, note = $6, updated_by = $7::uuid,
		   priority = $9, secret2_enc = coalesce($10, site_credentials.secret2_enc),
		   rotated_at = now()
		 returning `+siteCredCols+`, secret2_enc`,
		c.SiteID, c.GroupName, c.Kind, c.Username, secretEnc, c.Note, actor,
		c.Tag, c.Priority, secret2Enc).Scan(
		&out.ID, &out.SiteID, &out.GroupName, &out.Kind, &out.Username,
		&out.HasSecret, &out.HasSecret2, &out.Note, &out.RotatedAt, &out.CreatedAt,
		&out.PerDevice, &out.Tag, &out.Priority, &effective2)
	if err != nil {
		return model.SiteCredential{}, notFound(err)
	}
	if _, err = addGenerationTx(ctx, tx, out.ID, "", secretEnc, effective2, updatedBy); err != nil {
		return model.SiteCredential{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return model.SiteCredential{}, err
	}
	return out, nil
}

// RevealSiteCredential returns a credential's ENCRYPTED secrets. Decryption and
// audit trail are the caller's responsibility: this is a sensitive operation and
// must not look like an ordinary read.
//
// The second is nil except for a v3 identity. Callers that do not need it ignore
// it with `_` — but the signature forces them to see it exists, which is exactly
// the point: revelation is the only place a secret leaves, and a second secret
// forgotten in a corner is a secret that never rotates.
func (s *Store) RevealSiteCredential(ctx context.Context, id string) (model.SiteCredential, []byte, []byte, error) {
	var c model.SiteCredential
	var enc, enc2 []byte
	// `tag` is part of SCOPE, and it was missing here. Two callers depend on it now:
	// the journal label (`credLabel`, which said "@site" for a tag credential) and the
	// technical namespace guard, which must recognize what it refuses to delete or rotate
	// manually. Half-read scope means a guard that never bites.
	var err = s.pool.QueryRow(ctx,
		`select id::text, site_id::text, group_name, tag, kind, username, secret_enc, secret2_enc
		 from site_credentials where id = $1::uuid`, id).Scan(
		&c.ID, &c.SiteID, &c.GroupName, &c.Tag, &c.Kind, &c.Username, &enc, &enc2)
	c.HasSecret, c.HasSecret2 = len(enc) > 0, len(enc2) > 0
	return c, enc, enc2, notFound(err)
}

func (s *Store) DeleteSiteCredential(ctx context.Context, id string) error {
	var tag, err = s.pool.Exec(ctx, `delete from site_credentials where id = $1::uuid`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// SetCredentialPerDevice toggles a credential's mode.
func (s *Store) SetCredentialPerDevice(ctx context.Context, id string, perDevice bool) error {
	var tag, err = s.pool.Exec(ctx,
		`update site_credentials set per_device = $2 where id = $1::uuid`, id, perDevice)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}
