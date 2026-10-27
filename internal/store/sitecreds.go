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
	note, rotated_at, created_at, per_device, tag, priority,
	transport_tls, ca_pem`

func scanSiteCred(row pgx.Row) (model.SiteCredential, error) {
	var c model.SiteCredential
	var err = row.Scan(&c.ID, &c.SiteID, &c.GroupName, &c.Kind, &c.Username,
		&c.HasSecret, &c.HasSecret2, &c.Note, &c.RotatedAt, &c.CreatedAt,
		&c.PerDevice, &c.Tag, &c.Priority, &c.TransportTLS, &c.CAPEM)
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
			// transport_tls / ca_pem travel on the SECRETLESS path too, and that is
			// deliberate: turning a gnmi credential back from `plaintext` to `verify`
			// is a security change an operator must be able to make without also
			// rotating the password. Coalesced so a caller that sends neither leaves
			// both alone — the same rule secret2_enc follows below.
			`update site_credentials set username = $4, note = $5, updated_by = $6::uuid,
			        priority = $8,
			        transport_tls = coalesce(nullif($9, ''), transport_tls),
			        ca_pem = coalesce($10, ca_pem)
			 where site_id = $1::uuid and group_name = $2 and kind = $3 and tag = $7
			 returning `+siteCredCols,
			c.SiteID, c.GroupName, c.Kind, c.Username, c.Note, actor, c.Tag, c.Priority,
			c.TransportTLS, c.CAPEM))
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
		     note, updated_by, priority, secret2_enc, transport_tls, ca_pem, rotated_at)
		 values ($1::uuid, $2, $8, $3, $4, $5, $6, $7::uuid, $9, $10,
		         coalesce(nullif($11, ''), 'verify'), coalesce($12, ''), now())
		 on conflict (site_id, group_name, tag, kind) do update set
		   -- USERNAME IS COALESCED ON EMPTY, not overwritten blindly.
		   --
		   -- This conflict clause is reached by a CREATE that collides with an
		   -- existing credential — same site, group, tag and kind — which is what
		   -- happens when somebody opens a new-credential form, accepts the proposed
		   -- kind, and saves. That silently rewrote the stored account name to
		   -- whatever the form proposed, and it happened on the lab site: a
		   -- site-wide credential carrying mcadmin came back as admin, so
		   -- meshcraft then held the wrong account name for a fleet that had not
		   -- changed. Nothing failed at the time; the next connection would have.
		   --
		   -- A caller genuinely renaming the account still sends one and it still
		   -- lands. What can no longer happen is an EMPTY or absent username
		   -- quietly replacing a real one.
		   username = coalesce(nullif($4, ''), site_credentials.username),
		   secret_enc = $5, note = $6, updated_by = $7::uuid,
		   priority = $9, secret2_enc = coalesce($10, site_credentials.secret2_enc),
		   transport_tls = coalesce(nullif($11, ''), site_credentials.transport_tls),
		   ca_pem = coalesce($12, site_credentials.ca_pem),
		   rotated_at = now()
		 returning `+siteCredCols+`, secret2_enc`,
		c.SiteID, c.GroupName, c.Kind, c.Username, secretEnc, c.Note, actor,
		c.Tag, c.Priority, secret2Enc, c.TransportTLS, c.CAPEM).Scan(
		&out.ID, &out.SiteID, &out.GroupName, &out.Kind, &out.Username,
		&out.HasSecret, &out.HasSecret2, &out.Note, &out.RotatedAt, &out.CreatedAt,
		&out.PerDevice, &out.Tag, &out.Priority, &out.TransportTLS, &out.CAPEM,
		&effective2)
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
//
// THE SOURCE OF TRUTH IS THE ACTIVE GENERATION, not the head columns. The head
// (`site_credentials.secret_enc`) is a denormalized copy that `AddGeneration` now
// keeps in step — but rotations made before that catch-up existed left heads frozen
// at generation 1, and every consumer of this function (reveal screen, SNMP push,
// trap destinations, watch) then replayed a secret the fleet had already left
// behind. Reading the generation directly makes the answer correct by construction:
// a stale head can no longer be revealed, whatever its history. The head columns
// remain the fallback for a credential that predates generations entirely.
func (s *Store) RevealSiteCredential(ctx context.Context, id string) (model.SiteCredential, []byte, []byte, error) {
	var c model.SiteCredential
	var headEnc, headEnc2, genEnc, genEnc2 []byte
	// `tag` is part of SCOPE, and it was missing here. Two callers depend on it now:
	// the journal label (`credLabel`, which said "@site" for a tag credential) and the
	// technical namespace guard, which must recognize what it refuses to delete or rotate
	// manually. Half-read scope means a guard that never bites.
	//
	// The lateral join picks the SITE-LEVEL active generation (`device_id is null`):
	// per-device generations answer "what does THIS device carry", never "what is the
	// credential's current pair". `limit 1` is defensive — addGenerationTx retires the
	// previous active row in the same transaction, so at most one should exist.
	var err = s.pool.QueryRow(ctx,
		`select c.id::text, c.site_id::text, c.group_name, c.tag, c.kind, c.username,
		        c.secret_enc, c.secret2_enc, g.secret_enc, g.secret2_enc
		 from site_credentials c
		 left join lateral (
		   select secret_enc, secret2_enc from credential_generations
		   where credential_id = c.id and device_id is null and state = 'active'
		   order by generation desc limit 1
		 ) g on true
		 where c.id = $1::uuid`, id).Scan(
		&c.ID, &c.SiteID, &c.GroupName, &c.Tag, &c.Kind, &c.Username,
		&headEnc, &headEnc2, &genEnc, &genEnc2)
	var enc, enc2 = revealPair(headEnc, headEnc2, genEnc, genEnc2)
	c.HasSecret, c.HasSecret2 = len(enc) > 0, len(enc2) > 0
	return c, enc, enc2, notFound(err)
}

// revealPair chooses what revelation returns: the active generation's pair when one
// exists, the head columns otherwise.
//
// Pure, and tested as such (cf. sitecreds_reveal_test.go): the divergence it closes
// was invisible on every screen — a stale head reveals a WELL-FORMED secret, just
// one the devices refuse — so only a locked-in rule keeps it closed.
//
// The second phrase follows the same coalesce rule `AddGeneration` applies when it
// advances the head (`secret2_enc = coalesce(new, old)`): a generation without a
// second phrase (every non-v3 credential) must not HIDE the head's one, and a
// generation carrying one is the pair for THAT generation, never a mix of two.
func revealPair(headEnc, headEnc2, genEnc, genEnc2 []byte) ([]byte, []byte) {
	if len(genEnc) == 0 {
		// No active site-level generation: a credential created before generations
		// existed, or one whose only rotations are per-device. The head is all there is.
		return headEnc, headEnc2
	}
	var enc2 = genEnc2
	if len(enc2) == 0 {
		enc2 = headEnc2
	}
	return genEnc, enc2
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
