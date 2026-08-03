package store

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// SOCKS tokens: the per-user credential a browser presents to the SOCKS route
// (cf. migration 0036 and internal/socks).

// SetSocksToken stores (or replaces) a user's token hash and returns nothing:
// the clear token exists only in the caller's hand, and only until it is shown.
func (s *Store) SetSocksToken(ctx context.Context, userID string, hash []byte) error {
	var _, err = s.pool.Exec(ctx,
		`insert into socks_tokens (user_id, token_hash, created_at)
		 values ($1::uuid, $2, now())
		 on conflict (user_id) do update
		     set token_hash = excluded.token_hash,
		         created_at = now(),
		         last_used_at = null`,
		userID, hash)
	return err
}

// SocksTokenFor returns a user's token hash by EMAIL, which is what the SOCKS
// username carries — an operator types the identity they already know.
//
// Returns ok=false for an unknown user or one with no token, and the caller must
// treat the two identically: distinguishing them turns the proxy into an oracle
// for which accounts exist.
func (s *Store) SocksTokenFor(ctx context.Context, email string) (userID string,
	hash []byte, ok bool, err error) {
	err = s.pool.QueryRow(ctx,
		`select u.id::text, t.token_hash
		   from users u join socks_tokens t on t.user_id = u.id
		  where lower(u.email) = lower($1)`, email).Scan(&userID, &hash)
	if err == pgx.ErrNoRows {
		return "", nil, false, nil
	}
	if err != nil {
		return "", nil, false, err
	}
	return userID, hash, true, nil
}

// TouchSocksToken records that the token was presented. Best-effort by design:
// the caller must not fail a connection because a bookkeeping write did.
func (s *Store) TouchSocksToken(ctx context.Context, userID string) {
	_, _ = s.pool.Exec(ctx,
		`update socks_tokens set last_used_at = now() where user_id = $1::uuid`, userID)
}

// SocksTokenState reports whether a user has a token and when it was made,
// WITHOUT returning it. What a screen needs to show is "you have one, from this
// date" — the value itself was shown once and is gone.
func (s *Store) SocksTokenState(ctx context.Context, userID string) (bool, string, string, error) {
	var created, used *string
	var err = s.pool.QueryRow(ctx,
		`select to_char(created_at, 'YYYY-MM-DD"T"HH24:MI:SSOF'),
		        to_char(last_used_at, 'YYYY-MM-DD"T"HH24:MI:SSOF')
		   from socks_tokens where user_id = $1::uuid`, userID).Scan(&created, &used)
	if err == pgx.ErrNoRows {
		return false, "", "", nil
	}
	if err != nil {
		return false, "", "", err
	}
	var c, u string
	if created != nil {
		c = *created
	}
	if used != nil {
		u = *used
	}
	return true, c, u, nil
}

// DeleteSocksToken revokes a user's route.
func (s *Store) DeleteSocksToken(ctx context.Context, userID string) error {
	var _, err = s.pool.Exec(ctx,
		`delete from socks_tokens where user_id = $1::uuid`, userID)
	return err
}

// DeviceByMgmtIP finds the device answering at an address, with the site it
// belongs to. Used by the SOCKS resolver: a destination that matches no device
// is refused (cf. internal/socks, § what this is NOT).
//
// Ordered and limited because two sites CAN reuse a private range: the address
// alone does not always name one device. Taking the first is not a guess the
// resolver is entitled to make on its own — it pairs this with the caller's
// site rights, so a user only ever reaches a device on a site they administer.
func (s *Store) DevicesByMgmtIP(ctx context.Context, ip string) ([]DeviceRoute, error) {
	var rows, err = s.pool.Query(ctx,
		`select d.id::text, d.name, d.site_id::text, coalesce(d.proxy_id::text, '')
		   from devices d
		  where d.mgmt_ip = $1
		  order by d.name`, ip)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out = []DeviceRoute{}
	for rows.Next() {
		var r DeviceRoute
		if err = rows.Scan(&r.DeviceID, &r.Name, &r.SiteID, &r.ProxyID); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// DeviceRoute is the minimum needed to authorize and route one SOCKS CONNECT.
type DeviceRoute struct {
	DeviceID string
	Name     string
	SiteID   string
	ProxyID  string
}
