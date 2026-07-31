package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"meshcraft/internal/model"
)

// --- VLANs ---

const vlanCols = `id::text, site_id::text, vlan_id, name, color, purpose`

func scanVlan(row pgx.Row) (model.Vlan, error) {
	var v model.Vlan
	var err = row.Scan(&v.ID, &v.SiteID, &v.VlanID, &v.Name, &v.Color, &v.Purpose)
	return v, notFound(err)
}

func (s *Store) ListVlans(ctx context.Context, siteID string) ([]model.Vlan, error) {
	var rows, err = s.pool.Query(ctx,
		`select `+vlanCols+` from vlans where site_id = $1::uuid order by vlan_id`, siteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Vlan
	for rows.Next() {
		var v, scanErr = scanVlan(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) GetVlan(ctx context.Context, id string) (model.Vlan, error) {
	return scanVlan(s.pool.QueryRow(ctx,
		`select `+vlanCols+` from vlans where id = $1::uuid`, id))
}

func (s *Store) CreateVlan(ctx context.Context, siteID string, vlanID int, name, color, purpose string) (model.Vlan, error) {
	return scanVlan(s.pool.QueryRow(ctx,
		`insert into vlans (site_id, vlan_id, name, color, purpose)
		 values ($1::uuid, $2, $3, $4, $5) returning `+vlanCols,
		siteID, vlanID, name, color, purpose))
}

func (s *Store) UpdateVlan(ctx context.Context, id string, vlanID int, name, color, purpose string) (model.Vlan, error) {
	return scanVlan(s.pool.QueryRow(ctx,
		`update vlans set vlan_id = $2, name = $3, color = $4, purpose = $5
		 where id = $1::uuid returning `+vlanCols,
		id, vlanID, name, color, purpose))
}

func (s *Store) DeleteVlan(ctx context.Context, id string) error {
	var tag, err = s.pool.Exec(ctx, `delete from vlans where id = $1::uuid`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// --- Profiles (ports, devices, switches: same shape, parameterized table) ---

// profileTable bounds allowed table names (injection never possible).
//
// The SWITCH profile (migration 0021) arrived with exactly the same shape as the
// other two to fit this parameterization: all CRUD, routes, and live-flow site
// resolution then work without an extra line. A second CRUD would have diverged
// from the first.
func profileTable(kind string) (string, error) {
	switch kind {
	case "port":
		return "port_profiles", nil
	case "device":
		return "device_profiles", nil
	case "switch":
		return "switch_profiles", nil
	}
	return "", fmt.Errorf("unknown profile kind: %q", kind)
}

func scanProfile(row pgx.Row) (model.Profile, error) {
	var p model.Profile
	var spec []byte
	var err = row.Scan(&p.ID, &p.SiteID, &p.Name, &spec)
	p.Spec = json.RawMessage(spec)
	return p, notFound(err)
}

func scanSwitchProfile(row pgx.Row) (model.Profile, error) {
	var p model.Profile
	var spec []byte
	var tag *string
	var err = row.Scan(&p.ID, &p.SiteID, &p.Name, &tag, &spec)
	p.Spec = json.RawMessage(spec)
	if tag != nil {
		p.Tag = *tag
	}
	return p, notFound(err)
}

const switchProfileCols = `id::text, site_id::text, name, tag, spec`
const profileCols = `id::text, site_id::text, name, spec`

func (s *Store) ListProfiles(ctx context.Context, kind, siteID string) ([]model.Profile, error) {
	var table, err = profileTable(kind)
	if err != nil {
		return nil, err
	}
	var q string
	if kind == "switch" {
		q = `select ` + switchProfileCols + ` from ` + table +
			` where site_id = $1::uuid order by name`
	} else {
		q = `select ` + profileCols + ` from ` + table +
			` where site_id = $1::uuid order by name`
	}
	var rows, qErr = s.pool.Query(ctx, q, siteID)
	if qErr != nil {
		return nil, qErr
	}
	defer rows.Close()
	var out []model.Profile
	for rows.Next() {
		var p model.Profile
		var scanErr error
		if kind == "switch" {
			p, scanErr = scanSwitchProfile(rows)
		} else {
			p, scanErr = scanProfile(rows)
		}
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) GetProfile(ctx context.Context, kind, id string) (model.Profile, error) {
	var table, err = profileTable(kind)
	if err != nil {
		return model.Profile{}, err
	}
	if kind == "switch" {
		return scanSwitchProfile(s.pool.QueryRow(ctx,
			`select `+switchProfileCols+` from `+table+` where id = $1::uuid`, id))
	}
	return scanProfile(s.pool.QueryRow(ctx,
		`select `+profileCols+` from `+table+` where id = $1::uuid`, id))
}

// NormalizeProfileTag matches device-tag normalization (case + collapsed spaces).
func NormalizeProfileTag(tag string) string {
	return strings.ToLower(strings.Join(strings.Fields(tag), " "))
}

func (s *Store) CreateProfile(ctx context.Context, kind, siteID, name string, spec json.RawMessage, tag string) (model.Profile, error) {
	var table, err = profileTable(kind)
	if err != nil {
		return model.Profile{}, err
	}
	if kind == "switch" {
		tag = NormalizeProfileTag(tag)
		var tagArg any
		if tag == "" {
			tagArg = nil
		} else {
			tagArg = tag
		}
		return scanSwitchProfile(s.pool.QueryRow(ctx,
			`insert into `+table+` (site_id, name, spec, tag) values ($1::uuid, $2, $3, $4)
			 returning `+switchProfileCols,
			siteID, name, []byte(spec), tagArg))
	}
	return scanProfile(s.pool.QueryRow(ctx,
		`insert into `+table+` (site_id, name, spec) values ($1::uuid, $2, $3)
		 returning `+profileCols,
		siteID, name, []byte(spec)))
}

func (s *Store) UpdateProfile(ctx context.Context, kind, id, name string, spec json.RawMessage, tag string) (model.Profile, error) {
	var table, err = profileTable(kind)
	if err != nil {
		return model.Profile{}, err
	}
	if kind == "switch" {
		tag = NormalizeProfileTag(tag)
		var tagArg any
		if tag == "" {
			tagArg = nil
		} else {
			tagArg = tag
		}
		return scanSwitchProfile(s.pool.QueryRow(ctx,
			`update `+table+` set name = $2, spec = $3, tag = $4 where id = $1::uuid
			 returning `+switchProfileCols,
			id, name, []byte(spec), tagArg))
	}
	return scanProfile(s.pool.QueryRow(ctx,
		`update `+table+` set name = $2, spec = $3 where id = $1::uuid
		 returning `+profileCols,
		id, name, []byte(spec)))
}

// SwitchProfileByTag finds the switch profile enrolled by this site tag, if any.
func (s *Store) SwitchProfileByTag(ctx context.Context, siteID, tag string) (model.Profile, error) {
	tag = NormalizeProfileTag(tag)
	if tag == "" {
		return model.Profile{}, ErrNotFound
	}
	return scanSwitchProfile(s.pool.QueryRow(ctx,
		`select `+switchProfileCols+` from switch_profiles
		 where site_id = $1::uuid and lower(tag) = $2`,
		siteID, tag))
}

func (s *Store) DeleteProfile(ctx context.Context, kind, id string) error {
	var table, err = profileTable(kind)
	if err != nil {
		return err
	}
	var tag, execErr = s.pool.Exec(ctx, `delete from `+table+` where id = $1::uuid`, id)
	if execErr == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return execErr
}

// --- Device groups ---

const groupCols = `g.id::text, g.site_id::text, g.name, g.device_profile_id::text, p.name`

func scanGroup(row pgx.Row) (model.DeviceGroup, error) {
	var g model.DeviceGroup
	var err = row.Scan(&g.ID, &g.SiteID, &g.Name, &g.DeviceProfileID, &g.DeviceProfileName)
	return g, notFound(err)
}

func (s *Store) ListGroups(ctx context.Context, siteID string) ([]model.DeviceGroup, error) {
	var rows, err = s.pool.Query(ctx,
		`select `+groupCols+` from device_groups g
		 left join device_profiles p on p.id = g.device_profile_id
		 where g.site_id = $1::uuid order by g.name`, siteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.DeviceGroup
	for rows.Next() {
		var g, scanErr = scanGroup(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func (s *Store) GetGroup(ctx context.Context, id string) (model.DeviceGroup, error) {
	return scanGroup(s.pool.QueryRow(ctx,
		`select `+groupCols+` from device_groups g
		 left join device_profiles p on p.id = g.device_profile_id
		 where g.id = $1::uuid`, id))
}

func (s *Store) CreateGroup(ctx context.Context, siteID, name string, deviceProfileID *string) (model.DeviceGroup, error) {
	var id string
	var err = s.pool.QueryRow(ctx,
		`insert into device_groups (site_id, name, device_profile_id)
		 values ($1::uuid, $2, $3::uuid) returning id::text`,
		siteID, name, deviceProfileID).Scan(&id)
	if err != nil {
		return model.DeviceGroup{}, err
	}
	return s.GetGroup(ctx, id)
}

func (s *Store) UpdateGroup(ctx context.Context, id, name string, deviceProfileID *string) (model.DeviceGroup, error) {
	var _, err = s.pool.Exec(ctx,
		`update device_groups set name = $2, device_profile_id = $3::uuid where id = $1::uuid`,
		id, name, deviceProfileID)
	if err != nil {
		return model.DeviceGroup{}, err
	}
	return s.GetGroup(ctx, id)
}

func (s *Store) DeleteGroup(ctx context.Context, id string) error {
	var tag, err = s.pool.Exec(ctx, `delete from device_groups where id = $1::uuid`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// --- Proxy enrollment tokens ---

// The token carries the coverage the proxy will take (see migration 0007).
const tokenCols = `id::text, global, site_ids::text[], created_at, expires_at, used_at`

func scanToken(row pgx.Row) (model.EnrollToken, error) {
	var t model.EnrollToken
	var err = row.Scan(&t.ID, &t.Global, &t.SiteIDs, &t.CreatedAt, &t.ExpiresAt, &t.UsedAt)
	return t, notFound(err)
}

func (s *Store) CreateEnrollToken(ctx context.Context, tokenHash []byte, createdBy string,
	expiresAt time.Time, global bool, siteIDs []string) (model.EnrollToken, error) {
	if siteIDs == nil {
		siteIDs = []string{}
	}
	return scanToken(s.pool.QueryRow(ctx,
		`insert into enroll_tokens (token_hash, created_by, expires_at, global, site_ids)
		 values ($1, $2::uuid, $3, $4, $5::uuid[]) returning `+tokenCols,
		tokenHash, createdBy, expiresAt, global, siteIDs))
}

func (s *Store) ListEnrollTokens(ctx context.Context) ([]model.EnrollToken, error) {
	var rows, err = s.pool.Query(ctx,
		`select `+tokenCols+` from enroll_tokens
		 where expires_at > now() and used_at is null
		 order by created_at desc`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.EnrollToken
	for rows.Next() {
		var t, scanErr = scanToken(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) DeleteEnrollToken(ctx context.Context, id string) error {
	var tag, err = s.pool.Exec(ctx, `delete from enroll_tokens where id = $1::uuid`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}
