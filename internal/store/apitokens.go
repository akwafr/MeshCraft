package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"meshcraft/internal/model"
)

// Personal API tokens (cf. migration 0045 and internal/auth/apitoken.go).
//
// TWO RULES LIVE IN THE SQL HERE, deliberately, so that no caller can hold them
// wrong — the same reasoning as SetDeviceIdentity's guards:
//
//  1. EVERY QUERY THAT TOUCHES ONE TOKEN IS KEYED ON (id, user_id). A token is a
//     secret in one person's hands, not an administrable resource: there is no
//     query in this file that can read or revoke a token belonging to somebody
//     else, so no handler can be written that does. A global admin who must cut
//     off an account disables the account, which closes its tokens in the same
//     second because rights are read at call time.
//  2. THE LOOKUP NEVER PROJECTS THE VERIFIER INTO A VIEW. The hash leaves this
//     package only inside model.APITokenRecord, which carries no json tags at
//     all; model.APIToken, the shape every response is built from, has no field
//     it could land in.
//
// The lookup returns expired and revoked rows AS THEY ARE rather than filtering
// them out, because the authority on "is this token usable" is
// auth.judgeAPIToken — one pure function, exhaustively tested. Filtering here
// too would split that authority in two, make the judge's branches unreachable
// in production, and cost the ability to answer "revoked" instead of "unknown".

const apiTokenCols = `id::text, name, selector, read_only, site_ids,
	created_at, expires_at, last_used_at, revoked_at`

// scanAPIToken reads the display view, and rebuilds Prefix from the selector
// rather than storing a second copy of it: two columns that must agree is one
// column too many.
func scanAPIToken(row pgx.Row) (model.APIToken, string, error) {
	var t model.APIToken
	var selector string
	var sites []string
	var err = row.Scan(&t.ID, &t.Name, &selector, &t.ReadOnly, &sites,
		&t.CreatedAt, &t.ExpiresAt, &t.LastUsedAt, &t.RevokedAt)
	if err != nil {
		return model.APIToken{}, "", notFound(err)
	}
	if sites == nil {
		sites = []string{}
	}
	t.SiteIDs = sites
	t.Prefix = model.APITokenPrefix(selector)
	return t, selector, nil
}

// CreateAPIToken stores a minted token. The clear value is NOT a parameter —
// it never reaches this package, which is the point of splitting the token into
// a selector and a verifier hash before getting here.
func (s *Store) CreateAPIToken(ctx context.Context, userID, name, selector string,
	verifierHash []byte, readOnly bool, siteIDs []string,
	expiresAt *time.Time) (model.APIToken, error) {
	if siteIDs == nil {
		siteIDs = []string{}
	}
	var t, _, err = scanAPIToken(s.pool.QueryRow(ctx,
		`insert into api_tokens
		     (user_id, name, selector, verifier_hash, read_only, site_ids, expires_at)
		 values ($1::uuid, $2, $3, $4, $5, $6, $7)
		 returning `+apiTokenCols,
		userID, name, selector, verifierHash, readOnly, siteIDs, expiresAt))
	return t, err
}

// ListAPITokens returns the caller's own ACTIVE tokens, newest first.
//
// Revoked rows are kept in the table but not listed: the list is a tool for
// deciding what to revoke next, and a graveyard in it makes that harder, not
// easier.
func (s *Store) ListAPITokens(ctx context.Context, userID string) ([]model.APIToken, error) {
	var rows, err = s.pool.Query(ctx,
		`select `+apiTokenCols+`
		   from api_tokens
		  where user_id = $1::uuid and revoked_at is null
		  order by created_at desc`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out = []model.APIToken{}
	for rows.Next() {
		var t, _, scanErr = scanAPIToken(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// CountAPITokens counts the caller's active tokens, for the per-user cap.
func (s *Store) CountAPITokens(ctx context.Context, userID string) (int, error) {
	var n int
	var err = s.pool.QueryRow(ctx,
		`select count(*) from api_tokens
		  where user_id = $1::uuid and revoked_at is null`, userID).Scan(&n)
	return n, err
}

// APITokenBySelector resolves the public half of a presented token to the row
// and its OWNER, in one round trip — the user is needed on every single
// authenticated call, and a second query per request for it would be the most
// expensive line in the API.
//
// Returns ErrNotFound for an unknown selector. The caller must answer that
// exactly as it answers a wrong verifier.
func (s *Store) APITokenBySelector(ctx context.Context, selector string) (model.APITokenRecord, error) {
	var rec model.APITokenRecord
	var sites []string
	var sel string
	var err = s.pool.QueryRow(ctx,
		`select t.id::text, t.name, t.selector, t.read_only, t.site_ids,
		        t.created_at, t.expires_at, t.last_used_at, t.revoked_at,
		        t.verifier_hash,
		        u.id::text, u.email, u.display_name, u.is_global_admin, u.created_at
		   from api_tokens t join users u on u.id = t.user_id
		  where t.selector = $1`, selector).
		Scan(&rec.Token.ID, &rec.Token.Name, &sel, &rec.Token.ReadOnly, &sites,
			&rec.Token.CreatedAt, &rec.Token.ExpiresAt, &rec.Token.LastUsedAt,
			&rec.Token.RevokedAt, &rec.VerifierHash,
			&rec.User.ID, &rec.User.Email, &rec.User.DisplayName,
			&rec.User.IsGlobalAdmin, &rec.User.CreatedAt)
	if err != nil {
		return model.APITokenRecord{}, notFound(err)
	}
	if sites == nil {
		sites = []string{}
	}
	rec.Token.SiteIDs = sites
	rec.Token.Prefix = model.APITokenPrefix(sel)
	return rec, nil
}

// TouchAPIToken records that the token was presented. Best-effort by design,
// like TouchSocksToken: an API call must not fail because a bookkeeping write
// did. Signature returns nothing so that no caller is tempted to make it fatal.
func (s *Store) TouchAPIToken(ctx context.Context, tokenID string) {
	_, _ = s.pool.Exec(ctx,
		`update api_tokens set last_used_at = now() where id = $1::uuid`, tokenID)
}

// RevokeAPIToken closes ONE of the caller's own tokens.
//
// userID is in the WHERE clause, not in an `if` above the call: somebody else's
// token is simply not found, and the two cases are indistinguishable from
// outside — which is what keeps the list of who holds a token from being
// probeable one id at a time.
func (s *Store) RevokeAPIToken(ctx context.Context, userID, tokenID string) error {
	var tag, err = s.pool.Exec(ctx,
		`update api_tokens set revoked_at = now()
		  where id = $1::uuid and user_id = $2::uuid and revoked_at is null`,
		tokenID, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// PurgeRevokedAPITokens drops revocation records older than `keep`.
//
// A revoked row has one job — answering "revoked" rather than "unknown" to a
// token still in the wild — and that job has a shelf life. Nothing calls this
// yet; it exists so the table has a documented ceiling rather than growing
// forever by default.
func (s *Store) PurgeRevokedAPITokens(ctx context.Context, keep time.Duration) error {
	var _, err = s.pool.Exec(ctx,
		`delete from api_tokens where revoked_at is not null and revoked_at < $1`,
		time.Now().Add(-keep))
	return err
}
