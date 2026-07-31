package store

import (
	"context"

	"github.com/jackc/pgx/v5"

	"meshcraft/internal/model"
)

const siteCols = `id::text, name, slug, created_at`

func scanSite(row pgx.Row) (model.Site, error) {
	var st model.Site
	var err = row.Scan(&st.ID, &st.Name, &st.Slug, &st.CreatedAt)
	return st, notFound(err)
}

// CreateSite creates the site WITH its VLAN 1 and a unique URL slug.
//
// VLAN 1 is not an ordinary VLAN: it exists on every device out of the box and
// serves as the implicit native on trunks. A site without it would describe a
// fleet that does not exist, and every device poll would surface an "unknown"
// VLAN. Hence the same transaction as the site: there must be no moment when
// the site exists without its default VLAN.
func (s *Store) CreateSite(ctx context.Context, name string) (model.Site, error) {
	var slug, err = s.AllocSiteSlug(ctx, name, "")
	if err != nil {
		return model.Site{}, err
	}
	var tx pgx.Tx
	tx, err = s.pool.Begin(ctx)
	if err != nil {
		return model.Site{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var site model.Site
	site, err = scanSite(tx.QueryRow(ctx,
		`insert into sites (name, slug) values ($1, $2) returning `+siteCols, name, slug))
	if err != nil {
		return model.Site{}, err
	}
	_, err = tx.Exec(ctx,
		`insert into vlans (site_id, vlan_id, name, color, purpose)
		 values ($1::uuid, 1, 'Default', '#94a3b8', 'Site default VLAN')`, site.ID)
	if err != nil {
		return model.Site{}, err
	}
	return site, tx.Commit(ctx)
}

func (s *Store) GetSite(ctx context.Context, id string) (model.Site, error) {
	return scanSite(s.pool.QueryRow(ctx,
		`select `+siteCols+` from sites where id = $1::uuid`, id))
}

func (s *Store) SiteBySlug(ctx context.Context, slug string) (model.Site, error) {
	return scanSite(s.pool.QueryRow(ctx,
		`select `+siteCols+` from sites where slug = $1`, slug))
}

// RenameSite updates the display name and optionally the URL slug.
// Empty slug keeps the current one; a provided slug is validated and uniquified.
func (s *Store) RenameSite(ctx context.Context, id, name, slug string) (model.Site, error) {
	var cur, err = s.GetSite(ctx, id)
	if err != nil {
		return model.Site{}, err
	}
	if name == "" {
		name = cur.Name
	}
	var nextSlug = cur.Slug
	if slug != "" && slug != cur.Slug {
		nextSlug, err = s.AllocSiteSlug(ctx, slug, id)
		if err != nil {
			return model.Site{}, err
		}
	}
	return scanSite(s.pool.QueryRow(ctx,
		`update sites set name = $2, slug = $3 where id = $1::uuid returning `+siteCols,
		id, name, nextSlug))
}

func (s *Store) DeleteSite(ctx context.Context, id string) error {
	var tag, err = s.pool.Exec(ctx, `delete from sites where id = $1::uuid`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// ListSites: all sites (global admin) or only those assigned to userID.
func (s *Store) ListSites(ctx context.Context, userID string, all bool) ([]model.Site, error) {
	var q = `select ` + siteCols + ` from sites order by name`
	var args = []any{}
	if !all {
		q = `select s.id::text, s.name, s.slug, s.created_at from sites s
		     join user_roles r on r.site_id = s.id and r.user_id = $1::uuid
		     order by s.name`
		args = append(args, userID)
	}
	var rows, err = s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Site
	for rows.Next() {
		var st, scanErr = scanSite(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, st)
	}
	return out, rows.Err()
}
