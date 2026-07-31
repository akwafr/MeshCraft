package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"meshcraft/internal/model"
)

var ErrNotFound = errors.New("not found")

// ErrAlreadyAdopted: the candidate already produced a device. Two concurrent
// adoptions of the same candidate must not create two devices.
var ErrAlreadyAdopted = errors.New("candidate already adopted")

func notFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

const userCols = `id::text, email, display_name, is_global_admin, created_at`

func scanUser(row pgx.Row) (model.User, error) {
	var u model.User
	var err = row.Scan(&u.ID, &u.Email, &u.DisplayName, &u.IsGlobalAdmin, &u.CreatedAt)
	return u, notFound(err)
}

func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	var err = s.pool.QueryRow(ctx, `select count(*) from users`).Scan(&n)
	return n, err
}

func (s *Store) CreateUser(ctx context.Context, email, pwHash, displayName string, globalAdmin bool) (model.User, error) {
	return scanUser(s.pool.QueryRow(ctx,
		`insert into users (email, pw_hash, display_name, is_global_admin)
		 values ($1, $2, $3, $4) returning `+userCols,
		email, pwHash, displayName, globalAdmin))
}

func (s *Store) GetUserByEmail(ctx context.Context, email string) (model.User, string, error) {
	var u model.User
	var hash string
	var err = s.pool.QueryRow(ctx,
		`select `+userCols+`, pw_hash from users where lower(email) = lower($1)`, email).
		Scan(&u.ID, &u.Email, &u.DisplayName, &u.IsGlobalAdmin, &u.CreatedAt, &hash)
	return u, hash, notFound(err)
}

func (s *Store) GetUser(ctx context.Context, id string) (model.User, error) {
	return scanUser(s.pool.QueryRow(ctx,
		`select `+userCols+` from users where id = $1::uuid`, id))
}

func (s *Store) ListUsers(ctx context.Context) ([]model.User, error) {
	var rows, err = s.pool.Query(ctx, `select `+userCols+` from users order by email`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.User
	for rows.Next() {
		var u, scanErr = scanUser(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// UpdateUser updates displayName / globalAdmin, and the password if pwHash != "".
func (s *Store) UpdateUser(ctx context.Context, id, displayName string, globalAdmin bool, pwHash string) (model.User, error) {
	if pwHash != "" {
		return scanUser(s.pool.QueryRow(ctx,
			`update users set display_name = $2, is_global_admin = $3, pw_hash = $4
			 where id = $1::uuid returning `+userCols,
			id, displayName, globalAdmin, pwHash))
	}
	return scanUser(s.pool.QueryRow(ctx,
		`update users set display_name = $2, is_global_admin = $3
		 where id = $1::uuid returning `+userCols,
		id, displayName, globalAdmin))
}

func (s *Store) DeleteUser(ctx context.Context, id string) error {
	var tag, err = s.pool.Exec(ctx, `delete from users where id = $1::uuid`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// --- Roles ---

func (s *Store) ListUserRoles(ctx context.Context, userID string) ([]model.Role, error) {
	var rows, err = s.pool.Query(ctx,
		`select r.site_id::text, s.name, r.role
		 from user_roles r join sites s on s.id = r.site_id
		 where r.user_id = $1::uuid order by s.name`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Role
	for rows.Next() {
		var r model.Role
		if err = rows.Scan(&r.SiteID, &r.SiteName, &r.Role); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SetUserRoles replaces all of the user's (role, site) assignments.
func (s *Store) SetUserRoles(ctx context.Context, userID string, roles []model.Role) error {
	var tx, err = s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err = tx.Exec(ctx, `delete from user_roles where user_id = $1::uuid`, userID); err != nil {
		return err
	}
	var r model.Role
	for _, r = range roles {
		if _, err = tx.Exec(ctx,
			`insert into user_roles (user_id, site_id, role) values ($1::uuid, $2::uuid, $3)`,
			userID, r.SiteID, r.Role); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// GetSiteRole returns the user's role on a site ("" if none).
func (s *Store) GetSiteRole(ctx context.Context, userID, siteID string) (string, error) {
	var role string
	var err = s.pool.QueryRow(ctx,
		`select role from user_roles where user_id = $1::uuid and site_id = $2::uuid`,
		userID, siteID).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return role, err
}

// --- Sessions ---

func (s *Store) CreateSession(ctx context.Context, tokenHash []byte, userID string, expiresAt time.Time) error {
	var _, err = s.pool.Exec(ctx,
		`insert into sessions (token_hash, user_id, expires_at) values ($1, $2::uuid, $3)`,
		tokenHash, userID, expiresAt)
	return err
}

func (s *Store) GetSessionUser(ctx context.Context, tokenHash []byte) (model.User, error) {
	return scanUser(s.pool.QueryRow(ctx,
		`select u.id::text, u.email, u.display_name, u.is_global_admin, u.created_at
		 from users u
		 join sessions se on se.user_id = u.id
		 where se.token_hash = $1 and se.expires_at > now()`, tokenHash))
}

func (s *Store) DeleteSession(ctx context.Context, tokenHash []byte) error {
	var _, err = s.pool.Exec(ctx, `delete from sessions where token_hash = $1`, tokenHash)
	return err
}

func (s *Store) PurgeExpiredSessions(ctx context.Context) error {
	var _, err = s.pool.Exec(ctx, `delete from sessions where expires_at <= now()`)
	return err
}
